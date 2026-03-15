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
	doltPath, err := exec.LookPath("dolt")
	if err != nil {
		return 0, exec.ErrNotFound
	}

	// If something is already listening on the port, adopt it (skip spawn).
	if isDoltServerRunning(port) {
		return 0, nil
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
	// Respect existing non-default port (don't overwrite if already set and != 3307).
	if existingPort, ok := existing["dolt_server_port"]; !ok || existingPort == float64(3307) {
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
