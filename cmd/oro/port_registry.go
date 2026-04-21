package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type portAllocation struct {
	Port        int       `json:"port"`
	Project     string    `json:"project"`
	AllocatedAt time.Time `json:"allocated_at"`
}

type portRegistry struct {
	Version     int                       `json:"version"`
	Allocations map[string]portAllocation `json:"allocations"`
}

func emptyRegistry() *portRegistry {
	return &portRegistry{
		Version:     1,
		Allocations: make(map[string]portAllocation),
	}
}

func readRegistry(path string) (*portRegistry, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caller-controlled path
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyRegistry(), nil
		}
		return nil, fmt.Errorf("read registry %s: %w", path, err)
	}
	var reg portRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		// Corrupt JSON → intentionally return empty so AllocatePort rebuilds via migration.
		return emptyRegistry(), nil //nolint:nilerr // by design: corrupt registry is treated as empty
	}
	if reg.Allocations == nil {
		reg.Allocations = make(map[string]portAllocation)
	}
	return &reg, nil
}

func writeRegistryAtomic(path string, reg *portRegistry) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil { //nolint:gosec // caller-controlled path
		return fmt.Errorf("write registry tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename registry: %w", err)
	}
	return nil
}

func acquireRegistryLock(lockPath string) (func() error, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // caller-controlled path
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	tryLock := func() bool {
		return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil //nolint:gosec // fd from trusted f
	}
	unlock := func() error {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // fd from trusted f
		return f.Close()
	}

	if tryLock() {
		return unlock, nil
	}
	for _, delay := range []time.Duration{
		50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond,
		300 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond,
		500 * time.Millisecond, 500 * time.Millisecond,
	} {
		time.Sleep(delay)
		if tryLock() {
			return unlock, nil
		}
	}
	_ = f.Close()
	return nil, fmt.Errorf("registry lock contended after retries")
}

// clearPortRegistry replaces the on-disk registry with an empty one.
// Best-effort: errors are silently ignored since callers use it for cleanup.
func clearPortRegistry(oroHome string) {
	registryPath := filepath.Join(oroHome, "port-registry.json")
	lockPath := filepath.Join(oroHome, "port-registry.lock")
	unlock, err := acquireRegistryLock(lockPath)
	if err != nil {
		return
	}
	defer func() { _ = unlock() }()
	_ = writeRegistryAtomic(registryPath, emptyRegistry())
}

// projectRootAlive returns true if the project root for beadsDir still exists.
// For beadsDirs under <oroHome>/projects/, it reads project.root to find the
// actual repo root. For other beadsDirs, the parent directory is the project root.
func projectRootAlive(beadsDir, projectsBase string) bool {
	rel, relErr := filepath.Rel(projectsBase, beadsDir)
	if relErr == nil && !strings.HasPrefix(rel, "..") {
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) < 1 {
			return false
		}
		rootFile := filepath.Join(projectsBase, parts[0], "project.root")
		data, err := os.ReadFile(rootFile) //nolint:gosec // path from trusted projectsBase
		if err != nil {
			return false
		}
		_, err = os.Stat(strings.TrimSpace(string(data)))
		return err == nil
	}
	_, err := os.Stat(filepath.Dir(beadsDir))
	return err == nil
}

// pruneRegistry removes entries whose project root no longer exists.
// Returns the number of entries removed.
//
//nolint:unparam // return value used for debugging, not critical path
func pruneRegistry(reg *portRegistry, oroHome string) int {
	projectsBase := filepath.Join(oroHome, "projects")
	removed := 0
	for beadsDir := range reg.Allocations {
		if !projectRootAlive(beadsDir, projectsBase) {
			delete(reg.Allocations, beadsDir)
			removed++
		}
	}
	return removed
}

// resolveCandidatePort returns the port to assign to beadsDir given the set of
// already-allocated ports. It uses DerivePort as the preferred candidate, bumps
// 13307 to 13308 unconditionally, then scans [13308, doltPortBase+doltPortRange)
// for a free slot if the candidate is taken.
func resolveCandidatePort(beadsDir string, allocated map[int]bool) (int, error) {
	candidate := DerivePort(beadsDir)
	if candidate == SharedDoltPort {
		candidate = SharedDoltPort + 1
	}
	if !allocated[candidate] {
		return candidate, nil
	}
	for p := doltPortBase + 1; p < doltPortBase+doltPortRange; p++ {
		if !allocated[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free ports in range [%d, %d)", doltPortBase+1, doltPortBase+doltPortRange)
}

// migrateExistingPorts discovers existing per-project dolt-server.port files
// from discoverBreadsDirs and populates the registry with them. Falls back to
// DerivePort if dolt-server.port is missing. Handles collisions by bumping to
// the next free port. Prunes stale entries afterward.
func migrateExistingPorts(reg *portRegistry, oroHome string) error {
	dirs := discoverBreadsDirs(oroHome)
	if len(dirs) == 0 {
		return nil // no-op if no projects discovered
	}

	// Build set of already-allocated ports. 13307 is always reserved.
	allocated := make(map[int]bool, len(reg.Allocations)+1)
	allocated[SharedDoltPort] = true
	for _, a := range reg.Allocations {
		allocated[a.Port] = true
	}

	for _, beadsDir := range dirs {
		absBeadsDir, err := filepath.Abs(beadsDir)
		if err != nil {
			absBeadsDir = beadsDir
		}

		// Skip if already in registry.
		if _, ok := reg.Allocations[absBeadsDir]; ok {
			continue
		}

		// Try to read dolt-server.port file; fall back to DerivePort if missing.
		var port int
		portPath := filepath.Join(beadsDir, "dolt-server.port")
		data, readErr := os.ReadFile(portPath) //nolint:gosec // beadsDir from trusted discoverBreadsDirs
		if readErr == nil {
			p, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil {
				port = p
			} else {
				port = DerivePort(beadsDir)
			}
		} else {
			port = DerivePort(beadsDir)
		}

		// Handle collision: bump to next free port if already allocated.
		if allocated[port] {
			candidate, resolveErr := resolveCandidatePort(beadsDir, allocated)
			if resolveErr != nil {
				return resolveErr
			}
			port = candidate
		}

		allocated[port] = true
		reg.Allocations[absBeadsDir] = portAllocation{
			Port:        port,
			Project:     filepath.Base(filepath.Dir(beadsDir)),
			AllocatedAt: time.Now().UTC(),
		}
	}

	// Prune stale entries (project roots no longer exist).
	_ = pruneRegistry(reg, oroHome)

	return nil
}

func AllocatePort(beadsDir, projectName, oroHome string) (int, error) {
	absBeadsDir, err := filepath.Abs(beadsDir)
	if err != nil {
		absBeadsDir = beadsDir
	}

	registryPath := filepath.Join(oroHome, "port-registry.json")
	lockPath := filepath.Join(oroHome, "port-registry.lock")

	if mkdirErr := os.MkdirAll(oroHome, 0o750); mkdirErr != nil {
		return 0, fmt.Errorf("mkdir %s: %w", oroHome, mkdirErr)
	}

	unlock, err := acquireRegistryLock(lockPath)
	if err != nil {
		return 0, fmt.Errorf("acquire registry lock: %w", err)
	}
	defer func() { _ = unlock() }()

	reg, err := readRegistry(registryPath)
	if err != nil {
		return 0, err
	}

	// Idempotent: return existing allocation unchanged.
	if alloc, ok := reg.Allocations[absBeadsDir]; ok {
		return alloc.Port, nil
	}

	_ = pruneRegistry(reg, oroHome)

	// Build set of already-allocated ports. 13307 is always reserved.
	allocated := make(map[int]bool, len(reg.Allocations)+1)
	allocated[SharedDoltPort] = true
	for _, a := range reg.Allocations {
		allocated[a.Port] = true
	}

	candidate, err := resolveCandidatePort(absBeadsDir, allocated)
	if err != nil {
		return 0, err
	}

	reg.Allocations[absBeadsDir] = portAllocation{
		Port:        candidate,
		Project:     projectName,
		AllocatedAt: time.Now().UTC(),
	}

	if err := writeRegistryAtomic(registryPath, reg); err != nil {
		return 0, err
	}

	return candidate, nil
}
