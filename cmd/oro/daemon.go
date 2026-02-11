package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"oro/pkg/protocol"
)

// DaemonStatusValue represents the health state of the daemon.
type DaemonStatusValue string

const (
	// StatusRunning means the PID file exists and the process is alive.
	StatusRunning DaemonStatusValue = "running"
	// StatusStopped means no PID file exists.
	StatusStopped DaemonStatusValue = "stopped"
	// StatusStale means the PID file exists but the process is dead.
	StatusStale DaemonStatusValue = "stale"
)

// DefaultPIDPath returns the default PID file path: ~/.oro/oro.pid.
func DefaultPIDPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, protocol.OroDir, "oro.pid"), nil
}

// WritePIDFile writes the given PID to the specified file path.
// It creates parent directories as needed.
func WritePIDFile(path string, pid int) error {
	data := []byte(strconv.Itoa(pid))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write PID file %s: %w", path, err)
	}
	return nil
}

// ReadPIDFile reads and parses the PID from the given file path.
func ReadPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path) //nolint:gosec // PID file path is controlled by the application
	if err != nil {
		return 0, fmt.Errorf("read PID file %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse PID from %s: %w", path, err)
	}
	return pid, nil
}

// RemovePIDFile removes the PID file. It is idempotent: no error if the file
// does not exist.
func RemovePIDFile(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove PID file %s: %w", path, err)
	}
	return nil
}

// IsProcessAlive checks whether a process with the given PID is running.
// On Unix, sending signal 0 checks for existence without actually signaling.
func IsProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0: no signal sent, just checks if process exists.
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// DaemonStatus checks the daemon PID file and process liveness.
// Returns the status, the PID (0 if stopped), and any unexpected error.
func DaemonStatus(pidPath string) (status DaemonStatusValue, pid int, err error) {
	pid, err = ReadPIDFile(pidPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StatusStopped, 0, nil
		}
		return StatusStopped, 0, fmt.Errorf("daemon status: %w", err)
	}

	if IsProcessAlive(pid) {
		return StatusRunning, pid, nil
	}
	return StatusStale, pid, nil
}

// StopDaemon reads the PID file and sends SIGTERM to the daemon process.
func StopDaemon(pidPath string) error {
	pid, err := ReadPIDFile(pidPath)
	if err != nil {
		return fmt.Errorf("stop daemon: %w", err)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("send SIGTERM to PID %d: %w", pid, err)
	}
	return nil
}

// SetupSignalHandler installs a SIGTERM/SIGINT handler that cancels the
// returned context when a signal is received. It also returns a cleanup
// function that removes the PID file — callers should defer it.
func SetupSignalHandler(parent context.Context, pidPath string) (shutdownCtx context.Context, cleanup func()) {
	ctx, cancel := context.WithCancel(parent)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(sigCh)
	}()

	cleanup = func() {
		cancel()
		_ = RemovePIDFile(pidPath)
	}

	return ctx, cleanup
}
