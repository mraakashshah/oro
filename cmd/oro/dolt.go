package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	doltPortBase  = 13307
	doltPortRange = 1000

	// SharedDoltPort is the fixed TCP port for the machine-wide shared Dolt server
	// stored in ~/.oro/. All projects that opt into shared-server mode connect here.
	SharedDoltPort = 13307

	// doltUpstreamDefaultPort is the default MySQL-compatible port that dolt
	// sql-server uses when no --port flag is given (upstream default). We treat
	// this as a sentinel: metadata.json files written before oro assigned a
	// derived port will have dolt_server_port=3307, and ensureDoltMetadata
	// should overwrite it with the project-specific derived port.
	doltUpstreamDefaultPort = 3307
)

// doltMeta holds the fields from .beads/metadata.json relevant to dolt lifecycle.
type doltMeta struct {
	Backend        string `json:"backend"`
	DoltServerPort int    `json:"dolt_server_port"`
	DoltDatabase   string `json:"dolt_database"`
}

// DerivePort computes a stable port in [13307, 14306] for the given beads
// directory using FNV-32a hash of the absolute path. Two calls with the same
// resolved absolute path always return the same port.
func DerivePort(beadsDir string) int {
	abs, err := filepath.Abs(beadsDir)
	if err != nil {
		abs = beadsDir
	}
	h := fnv.New32a()
	h.Write([]byte(abs)) //nolint:gosec // G104: hash.Hash.Write never returns an error
	return doltPortBase + int(h.Sum32()%doltPortRange)
}

// readDoltMeta reads .beads/metadata.json and returns its contents if the
// backend is "dolt". Returns nil (no error) for missing directories, missing
// metadata.json, or any non-dolt backend.
func readDoltMeta(beadsDir string) (*doltMeta, error) {
	metaPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metaPath) //nolint:gosec // beadsDir is caller-controlled
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", metaPath, err)
	}

	var meta doltMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse %s: %w", metaPath, err)
	}

	if meta.Backend != "dolt" {
		return nil, nil
	}
	return &meta, nil
}

// isDoltServerRunning returns true if a TCP listener is accepting connections
// on 127.0.0.1:<port> within a 200ms timeout.
func isDoltServerRunning(port int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// startDoltServer spawns `dolt sql-server` bound to 127.0.0.1:<port> with
// data directory at <beadsDir>/dolt. It writes the PID to
// <beadsDir>/dolt-server.pid and the port to <beadsDir>/dolt-server.port.
//
// Returns exec.ErrNotFound if dolt is not in PATH.
// Returns an error if the port is already occupied by a non-dolt process.
func startDoltServer(beadsDir string, port int) (int, error) {
	// If something is already listening on the port, check whether we own it.
	// Check before LookPath so adoption works even when dolt isn't in PATH.
	if isDoltServerRunning(port) {
		return 0, checkPortConflict(beadsDir, port)
	}

	doltPath, err := exec.LookPath("dolt")
	if err != nil {
		return 0, exec.ErrNotFound
	}

	dataDir := filepath.Join(beadsDir, "dolt")
	//nolint:gosec // args constructed from trusted internal values
	cmd := exec.CommandContext(context.Background(), doltPath, //nolint:noctx // background context appropriate for long-lived server process
		"sql-server",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--data-dir", dataDir,
	)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start dolt server: %w", err)
	}

	pid := cmd.Process.Pid

	// Write PID and port files so bd can find the server.
	pidPath := filepath.Join(beadsDir, "dolt-server.pid")
	portPath := filepath.Join(beadsDir, "dolt-server.port")

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		return 0, fmt.Errorf("write dolt PID file: %w", err)
	}
	if err := os.WriteFile(portPath, []byte(strconv.Itoa(port)), 0o600); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		return 0, fmt.Errorf("write dolt port file: %w", err)
	}

	// Reap the child process in the background to avoid zombies.
	go func() { _ = cmd.Wait() }()

	return pid, nil
}

const (
	doltKillTimeout      = 5 * time.Second
	doltKillPollInterval = 100 * time.Millisecond
)

// discoverPIDByPort uses lsof to find the PID of the process listening on the
// given TCP port. Returns exec.ErrNotFound if lsof is not in PATH. Returns an
// error if no LISTEN process is found. If multiple PIDs are reported, the
// first is used.
func discoverPIDByPort(port int) (int, error) {
	lsofPath, err := exec.LookPath("lsof")
	if err != nil {
		return 0, exec.ErrNotFound
	}

	//nolint:gosec // args constructed from trusted internal values
	out, err := exec.CommandContext(context.Background(), lsofPath, "-ti", fmt.Sprintf("TCP:%d", port), "-s", "TCP:LISTEN").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return 0, fmt.Errorf("no process found listening on port %d", port)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, fmt.Errorf("parse lsof output %q: %w", lines[0], err)
	}
	return pid, nil
}

// killAndWait sends SIGTERM to the given PID and polls IsProcessAlive until
// the process exits or 5 seconds elapse, then falls back to SIGKILL. Removes
// <beadsDir>/dolt-server.pid and <beadsDir>/dolt-server.port on completion.
//
// A background goroutine calls proc.Wait() to reap the zombie if we are the
// parent; this allows the IsProcessAlive poll to detect the process exit via
// ESRCH once the zombie is reaped.
func killAndWait(pid int, beadsDir string) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		removeDoltServerFiles(beadsDir)
		return nil
	}

	_ = proc.Signal(syscall.SIGTERM)

	// Reap zombie in background so IsProcessAlive returns false once the process
	// exits. If we are not the parent, Wait returns ECHILD immediately (no-op).
	go func() { _, _ = proc.Wait() }()

	deadline := time.After(doltKillTimeout)
	ticker := time.NewTicker(doltKillPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !IsProcessAlive(pid) {
				removeDoltServerFiles(beadsDir)
				return nil
			}
		case <-deadline:
			_ = proc.Signal(syscall.SIGKILL)
			removeDoltServerFiles(beadsDir)
			return nil
		}
	}
}

// removeDoltServerFiles removes the dolt-server.pid and dolt-server.port
// files from beadsDir. Errors are silently ignored (best-effort cleanup).
func removeDoltServerFiles(beadsDir string) {
	_ = os.Remove(filepath.Join(beadsDir, "dolt-server.pid"))
	_ = os.Remove(filepath.Join(beadsDir, "dolt-server.port"))
}

// stopDoltServer stops the dolt server for the given beads directory.
//
// Strategy:
//  1. PID file present + process alive → SIGTERM via killAndWait.
//  2. PID file missing → read metadata.json to get port; if port is listening
//     → discover PID via lsof (discoverPIDByPort) → killAndWait.
//  3. PID file missing + port not listening → no-op (nil).
//  4. PID file missing + metadata.json missing → no-op (nil).
//
// Idempotent: safe to call when the server is already stopped.
func stopDoltServer(beadsDir string) error {
	pidPath := filepath.Join(beadsDir, "dolt-server.pid")

	data, err := os.ReadFile(pidPath) //nolint:gosec // beadsDir is caller-controlled
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read dolt PID file: %w", err)
	}

	if err == nil {
		// PID file present — try to kill by PID.
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr != nil {
			removeDoltServerFiles(beadsDir)
			return fmt.Errorf("parse dolt PID file: %w", parseErr)
		}
		if IsProcessAlive(pid) {
			return killAndWait(pid, beadsDir)
		}
		// Process already dead — just clean up.
		removeDoltServerFiles(beadsDir)
		return nil
	}

	// PID file missing — fall back to port-based discovery.
	meta, err := readDoltMeta(beadsDir)
	if err != nil {
		return err
	}
	if meta == nil {
		return nil // not a dolt project or metadata missing
	}

	port := meta.DoltServerPort
	if port == 0 {
		port = DerivePort(beadsDir)
	}

	if !isDoltServerRunning(port) {
		return nil // nothing listening on the expected port
	}

	pid, err := discoverPIDByPort(port)
	if errors.Is(err, exec.ErrNotFound) {
		return nil // lsof not available — degrade gracefully
	}
	if err != nil {
		return fmt.Errorf("discover PID on port %d: %w", port, err)
	}

	return killAndWait(pid, beadsDir)
}

// ensureDoltMetadata creates or updates <beadsDir>/metadata.json with the
// given port under the key "dolt_server_port". If the file already exists, it
// merges the port into the existing JSON object. If the file does not exist,
// it creates a minimal metadata.json with backend="dolt" and the given port.
func ensureDoltMetadata(beadsDir string, port int) error {
	metaPath := filepath.Join(beadsDir, "metadata.json")

	// Try to read existing metadata.
	existing := map[string]interface{}{}
	data, err := os.ReadFile(metaPath) //nolint:gosec // beadsDir is caller-controlled
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read metadata.json: %w", err)
	}
	if err == nil {
		if jsonErr := json.Unmarshal(data, &existing); jsonErr != nil {
			return fmt.Errorf("parse metadata.json: %w", jsonErr)
		}
	}

	// Set backend and port.
	if _, ok := existing["backend"]; !ok {
		existing["backend"] = "dolt"
	}
	// Respect existing non-default port (don't overwrite if already set and != upstream default).
	if existingPort, ok := existing["dolt_server_port"]; !ok || existingPort == float64(doltUpstreamDefaultPort) {
		existing["dolt_server_port"] = port
	}
	if _, ok := existing["dolt_database"]; !ok {
		existing["dolt_database"] = "beads"
	}

	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata.json: %w", err)
	}

	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", beadsDir, err)
	}

	if err := os.WriteFile(metaPath, append(out, '\n'), 0o600); err != nil { //nolint:gosec // beadsDir is caller-controlled
		return fmt.Errorf("write metadata.json: %w", err)
	}

	return nil
}

// setDoltPort unconditionally overwrites the "dolt_server_port" field in
// <beadsDir>/metadata.json. Used by dolt setup to migrate projects to the
// shared server port regardless of any previously-set custom port.
func setDoltPort(beadsDir string, port int) error {
	metaPath := filepath.Join(beadsDir, "metadata.json")

	existing := map[string]any{}
	data, err := os.ReadFile(metaPath) //nolint:gosec // beadsDir is caller-controlled
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read metadata.json: %w", err)
	}
	if err == nil {
		if jsonErr := json.Unmarshal(data, &existing); jsonErr != nil {
			return fmt.Errorf("parse metadata.json: %w", jsonErr)
		}
	}

	existing["dolt_server_port"] = port

	out, marshalErr := json.MarshalIndent(existing, "", "  ")
	if marshalErr != nil {
		return fmt.Errorf("marshal metadata.json: %w", marshalErr)
	}

	if mkdirErr := os.MkdirAll(beadsDir, 0o750); mkdirErr != nil {
		return fmt.Errorf("mkdir %s: %w", beadsDir, mkdirErr)
	}

	if writeErr := os.WriteFile(metaPath, append(out, '\n'), 0o600); writeErr != nil { //nolint:gosec // beadsDir is caller-controlled
		return fmt.Errorf("write metadata.json: %w", writeErr)
	}

	return nil
}

// isSharedServer reports whether port is the machine-wide shared Dolt port
// (SharedDoltPort = 13307).
func isSharedServer(port int) bool {
	return port == SharedDoltPort
}

// startSharedDoltServer starts a shared Dolt server bound to 127.0.0.1:13307
// with data directory at <oroHome>/dolt. It writes the PID to
// <oroHome>/dolt-server.pid and the port to <oroHome>/dolt-server.port.
//
// If port 13307 is already occupied by our own server (valid PID file present
// and process alive), it returns (0, nil) — adoption, no new process spawned.
//
// If port 13307 is occupied by a foreign process (no PID file or stale PID),
// it returns an error that includes the blocking PID so the caller can
// diagnose the conflict.
//
// Returns exec.ErrNotFound if dolt is not in PATH.
//
// LEGAL CALLERS: newDoltSetupCmd, newDoltRepairCmd — DO NOT add more without
// updating D6 in docs/plans/2026-04-20-oro-dolt-shared-lifecycle-coordination-design.md
// and adding to allowedStartSharedDoltServerCallers in allowlist_test.go.
func startSharedDoltServer(oroHome string) (int, error) { //nolint:unparam // PID return will be used by downstream callers (oro-4zky, oro-hcuy)
	if isDoltServerRunning(SharedDoltPort) {
		return 0, checkSharedPortConflict(oroHome)
	}

	doltPath, err := exec.LookPath("dolt")
	if err != nil {
		return 0, exec.ErrNotFound
	}

	dataDir := filepath.Join(oroHome, "dolt")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return 0, fmt.Errorf("mkdir %s: %w", dataDir, err)
	}

	//nolint:gosec // args constructed from trusted internal values
	cmd := exec.CommandContext(context.Background(), doltPath, //nolint:noctx // background context appropriate for long-lived server process
		"sql-server",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(SharedDoltPort),
		"--data-dir", dataDir,
	)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start shared dolt server: %w", err)
	}

	pid := cmd.Process.Pid

	pidPath := filepath.Join(oroHome, "dolt-server.pid")
	portPath := filepath.Join(oroHome, "dolt-server.port")

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		return 0, fmt.Errorf("write dolt PID file: %w", err)
	}
	if err := os.WriteFile(portPath, []byte(strconv.Itoa(SharedDoltPort)), 0o600); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		return 0, fmt.Errorf("write dolt port file: %w", err)
	}

	// Reap the child process in the background to avoid zombies.
	go func() { _ = cmd.Wait() }()

	return pid, nil
}

// ensureSharedDoltRunning verifies the shared Dolt server on SharedDoltPort is
// reachable. If not, it attempts launchctl kickstart (launchd auto-start).
//
// D6.1: direct spawn is intentionally removed. If kickstart fails the caller
// must run `oro dolt setup` (first-time install) or `oro dolt repair`
// (post-failure restart). This prevents race conditions when multiple oro
// instances start concurrently and each falls through to spawn.
func ensureSharedDoltRunning(oroHome string) (int, error) {
	// Already running — adopt.
	if isDoltServerRunning(SharedDoltPort) {
		return 0, nil
	}

	// Try launchctl kickstart (macOS launchd service).
	if tryLaunchctlKickstart() {
		if waitForPort(SharedDoltPort, 3*time.Second) {
			return 0, nil
		}
	}

	return 0, fmt.Errorf(
		"shared dolt server is not running and launchctl kickstart failed: "+
			"run 'oro dolt setup' to install the server, or 'oro dolt repair' to restart it (oroHome: %s)",
		oroHome,
	)
}

// kickstartServiceTarget builds the launchctl kickstart service target string
// for the shared Dolt server. The label MUST match launchAgentLabel (used at
// install time) — a mismatch makes kickstart a silent no-op.
func kickstartServiceTarget(uid int) string {
	return fmt.Sprintf("gui/%d/%s", uid, launchAgentLabel)
}

// tryLaunchctlKickstart attempts to start the shared Dolt server via the
// macOS launchd service. Returns true if the kickstart command succeeds,
// false otherwise (not macOS, service not installed, etc.).
func tryLaunchctlKickstart() bool {
	launchctlPath, err := exec.LookPath("launchctl")
	if err != nil {
		return false
	}
	//nolint:gosec // uid from trusted os.Getuid()
	cmd := exec.CommandContext(context.Background(), launchctlPath, "kickstart", "-k",
		kickstartServiceTarget(os.Getuid()))
	return cmd.Run() == nil
}

// isSharedBeadsDir returns true when beadsDir's metadata.json declares the
// shared Dolt port. The shared dolt server's lifecycle is owned by launchd
// (or another oro instance) and must NOT be stopped from this process — the
// signal handler uses this to preserve the server across stop/restart cycles.
func isSharedBeadsDir(beadsDir string) bool {
	meta, err := readDoltMeta(beadsDir)
	if err != nil || meta == nil {
		return false
	}
	return meta.DoltServerPort == SharedDoltPort
}

// waitForPort polls isDoltServerRunning until the port is reachable or timeout
// elapses. Used after launchctl kickstart to give the server time to bind.
func waitForPort(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if isDoltServerRunning(port) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// checkPortConflict checks whether we own the server on the given port by
// reading the PID file at <beadsDir>/dolt-server.pid. Returns nil if the PID
// file is present and the recorded process is alive (adoption). Returns an
// error if no PID file exists or the recorded PID is stale.
func checkPortConflict(beadsDir string, port int) error {
	pidPath := filepath.Join(beadsDir, "dolt-server.pid")
	data, err := os.ReadFile(pidPath) //nolint:gosec // beadsDir is caller-controlled
	if err == nil {
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr == nil && IsProcessAlive(pid) {
			return nil // adopt our own server
		}
	}
	return fmt.Errorf("port %d already in use (not a managed dolt server)", port)
}

// checkSharedPortConflict checks whether we own the server on SharedDoltPort.
// Returns nil if we own it (adoption). Returns an error if a foreign process
// holds the port.
func checkSharedPortConflict(oroHome string) error {
	pidPath := filepath.Join(oroHome, "dolt-server.pid")
	data, err := os.ReadFile(pidPath) //nolint:gosec // oroHome is caller-controlled
	if err == nil {
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr == nil && IsProcessAlive(pid) {
			return nil // adopt our own server
		}
	}

	// PID file is stale or missing but port is in use. If the listener is a
	// dolt process (e.g. respawned by launchd with a new PID), adopt it by
	// updating the PID file instead of erroring.
	blockerPID, lsofErr := discoverPIDByPort(SharedDoltPort)
	if lsofErr != nil {
		return fmt.Errorf("port %d already in use by an unidentified process", SharedDoltPort)
	}
	if isDoltProcess(blockerPID) {
		_ = os.WriteFile(pidPath, []byte(strconv.Itoa(blockerPID)), 0o600) //nolint:gosec // oroHome is trusted
		return nil                                                         // adopted launchd-respawned server
	}
	return fmt.Errorf("port %d already in use by PID %d (not a managed dolt server)", SharedDoltPort, blockerPID)
}

// isDoltProcess checks whether the given PID is a dolt process by inspecting
// its command name via ps. Used to distinguish launchd-respawned dolt servers
// (adoptable) from unrelated processes occupying the port.
func isDoltProcess(pid int) bool {
	out, err := exec.CommandContext(context.Background(), "ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output() //nolint:gosec // pid is int from lsof
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(string(out))
	return strings.HasSuffix(comm, "dolt") || strings.Contains(comm, "/dolt")
}
