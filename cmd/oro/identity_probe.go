package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrDataDirMismatch is returned when the dolt server's --data-dir does not
	// match oroHome/dolt, or when --data-dir is absent (rogue server).
	ErrDataDirMismatch = errors.New("dolt data-dir mismatch")

	// ErrCannotIdentify is returned when the dolt server PID cannot be resolved
	// via either the pid file or lsof port scan.
	ErrCannotIdentify = errors.New("cannot identify dolt server process")

	ErrNoCookie      = errors.New("no server identity cookie found")
	ErrInvalidCookie = errors.New("invalid server identity cookie (corrupt JSON)")
	ErrStaleCookie   = errors.New("server identity cookie is stale (process start time mismatch or age >60s)")
)

// processProbe holds injectable functions for runProcessProbeWith. The zero
// value is not usable; use defaultProcessProbe or populate each field.
type processProbe struct {
	readPIDFile func(path string) (int, error)
	discoverPID func(port int) (int, error)
	readPSArgs  func(pid int) (string, error)
}

type serverIdentity struct {
	PID        int       `json:"pid"`
	StartTime  string    `json:"start_time"`
	DataDir    string    `json:"data_dir"`
	ObservedAt time.Time `json:"observed_at"`
}

// runProcessProbe probes the shared dolt server process on port SharedDoltPort.
//
// Resolution order:
//  1. PID file at <oroHome>/dolt-server.pid
//  2. lsof -i :13307 -sTCP:LISTEN -t fallback
//
// Returns the resolved PID and the --data-dir value from the process command
// line. Returns ErrDataDirMismatch if --data-dir is absent or does not equal
// <oroHome>/dolt. Returns ErrCannotIdentify if neither the pid file nor lsof
// can locate the process.
//
//nolint:unused // called by downstream coordination logic (D2 steps 2+)
func runProcessProbe(oroHome string) (pid int, dataDir string, err error) {
	return runProcessProbeWith(oroHome, processProbe{
		readPIDFile: defaultReadPIDFile,
		discoverPID: discoverPIDByPort,
		readPSArgs:  defaultReadPSArgs,
	})
}

func runProcessProbeWith(oroHome string, probe processProbe) (pid int, dataDir string, err error) {
	pidPath := filepath.Join(oroHome, "dolt-server.pid")
	pid, err = probe.readPIDFile(pidPath)
	if err != nil {
		pid, err = probe.discoverPID(SharedDoltPort)
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return 0, "", fmt.Errorf("%w: lsof not available — install lsof to enable port scanning", ErrCannotIdentify)
			}
			return 0, "", fmt.Errorf("%w: %w", ErrCannotIdentify, err)
		}
	}

	args, psErr := probe.readPSArgs(pid)
	if psErr != nil {
		return 0, "", fmt.Errorf("%w: ps failed for PID %d: %w", ErrCannotIdentify, pid, psErr)
	}

	dataDir, parseErr := parseDataDir(args)
	if parseErr != nil {
		return pid, "", ErrDataDirMismatch
	}

	if dataDir != filepath.Join(oroHome, "dolt") {
		return pid, dataDir, ErrDataDirMismatch
	}

	return pid, dataDir, nil
}

// parseDataDir extracts the --data-dir value from a process command line.
// Supports both "--data-dir=VALUE" and "--data-dir VALUE" forms.
func parseDataDir(args string) (string, error) {
	fields := strings.Fields(args)
	for i, f := range fields {
		if strings.HasPrefix(f, "--data-dir=") {
			return strings.TrimPrefix(f, "--data-dir="), nil
		}
		if f == "--data-dir" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", errors.New("--data-dir not found in process args")
}

//nolint:unused // support function for runProcessProbe — used by downstream D2 coordination logic
func defaultReadPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is oroHome/dolt-server.pid — caller-controlled
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse PID file %q: %w", path, err)
	}
	return pid, nil
}

//nolint:unused // support function for runProcessProbe — used by downstream D2 coordination logic
func defaultReadPSArgs(pid int) (string, error) {
	out, err := exec.CommandContext(context.Background(), "ps", "-p", strconv.Itoa(pid), "-o", "args=").Output() //nolint:gosec,noctx // pid is int from trusted internal sources
	if err != nil {
		return "", fmt.Errorf("ps -p %d: %w", pid, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// writeServerIdentity writes the server identity to ~/.oro/dolt/.server-identity.json.
// Creates the dolt directory if it doesn't exist.
func writeServerIdentity(oroHome string, ident serverIdentity) error {
	doltDir := filepath.Join(oroHome, "dolt")
	if err := os.MkdirAll(doltDir, 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", doltDir, err)
	}

	cookiePath := filepath.Join(doltDir, ".server-identity.json")
	data, err := json.MarshalIndent(ident, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal server identity: %w", err)
	}

	if err := os.WriteFile(cookiePath, append(data, '\n'), 0o600); err != nil { //nolint:gosec // cookiePath is caller-controlled
		return fmt.Errorf("write server identity cookie: %w", err)
	}

	return nil
}

// readServerIdentity reads and validates the server identity from ~/.oro/dolt/.server-identity.json.
// Returns:
//   - ErrNoCookie if the file doesn't exist
//   - ErrInvalidCookie if the JSON is corrupt
//   - ErrStaleCookie if the start_time doesn't match the current process's start time
//     or if the cookie age is >60 seconds
func readServerIdentity(oroHome string) (serverIdentity, error) {
	cookiePath := filepath.Join(oroHome, "dolt", ".server-identity.json")
	data, err := os.ReadFile(cookiePath) //nolint:gosec // oroHome is caller-controlled
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return serverIdentity{}, ErrNoCookie
		}
		return serverIdentity{}, fmt.Errorf("read server identity cookie: %w", err)
	}

	var ident serverIdentity
	if err := json.Unmarshal(data, &ident); err != nil {
		return serverIdentity{}, ErrInvalidCookie
	}

	currentStartTime, err := getProcessStartTime(ident.PID)
	if err != nil {
		return serverIdentity{}, ErrStaleCookie
	}
	if currentStartTime != ident.StartTime {
		return serverIdentity{}, ErrStaleCookie
	}

	age := time.Since(ident.ObservedAt)
	if age > 60*time.Second {
		return serverIdentity{}, ErrStaleCookie
	}

	return ident, nil
}

// getProcessStartTime returns the start time of the process with the given PID
// in the format returned by `ps -o lstart=` (e.g., "Mon Apr 20 10:30:45 2026").
func getProcessStartTime(pid int) (string, error) {
	out, err := exec.CommandContext(context.Background(), "ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output() //nolint:gosec,noctx // pid is int from trusted internal sources
	if err != nil {
		return "", fmt.Errorf("get process start time: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
