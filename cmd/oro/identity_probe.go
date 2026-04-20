package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	// ErrDataDirMismatch is returned when the dolt server's --data-dir does not
	// match oroHome/dolt, or when --data-dir is absent (rogue server).
	ErrDataDirMismatch = errors.New("dolt data-dir mismatch")

	// ErrCannotIdentify is returned when the dolt server PID cannot be resolved
	// via either the pid file or lsof port scan.
	ErrCannotIdentify = errors.New("cannot identify dolt server process")
)

// processProbe holds injectable functions for runProcessProbeWith. The zero
// value is not usable; use defaultProcessProbe or populate each field.
type processProbe struct {
	readPIDFile func(path string) (int, error)
	discoverPID func(port int) (int, error)
	readPSArgs  func(pid int) (string, error)
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
