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
		return 0, fmt.Errorf("port %d is already in use by another process", port)
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

// stopDoltServer reads <beadsDir>/dolt-server.pid, sends SIGTERM to the
// process, waits up to 5 seconds, then sends SIGKILL. Removes the PID and
// port files regardless of whether the process was found. Idempotent: returns
// nil if no PID file exists.
func stopDoltServer(beadsDir string) error {
	pidPath := filepath.Join(beadsDir, "dolt-server.pid")
	portPath := filepath.Join(beadsDir, "dolt-server.port")

	data, err := os.ReadFile(pidPath) //nolint:gosec // beadsDir is caller-controlled
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read dolt PID file: %w", err)
	}

	pid, err := strconv.Atoi(string(data))
	if err != nil {
		// Malformed PID file — clean up and return.
		_ = os.Remove(pidPath)
		_ = os.Remove(portPath)
		return fmt.Errorf("parse dolt PID file: %w", err)
	}

	proc, err := os.FindProcess(pid)
	if err == nil {
		_ = proc.Signal(syscall.SIGTERM)

		done := make(chan struct{})
		go func() {
			_, _ = proc.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = proc.Signal(syscall.SIGKILL)
		}
	}

	_ = os.Remove(pidPath)
	_ = os.Remove(portPath)
	return nil
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
