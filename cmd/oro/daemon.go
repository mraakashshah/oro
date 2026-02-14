package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

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

// probeSocket connects to the dispatcher UDS, sends a status directive,
// and extracts the PID from the response. Returns 0 on any failure.
func probeSocket(sockPath string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	conn, err := dialDispatcher(ctx, sockPath)
	if err != nil {
		return 0
	}
	defer conn.Close()

	if err := sendDirective(conn, "status", ""); err != nil {
		return 0
	}

	ack, err := readACK(conn)
	if err != nil {
		return 0
	}

	var resp struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal([]byte(ack.Detail), &resp); err != nil {
		return 0
	}
	return resp.PID
}

// DaemonStatus checks daemon liveness using socket-first with PID file fallback.
// The socket is the authoritative liveness signal; the PID file covers the
// startup race (socket not ready yet) and legacy scenarios.
// Returns the status, the PID (0 if stopped), and any unexpected error.
func DaemonStatus(pidPath, sockPath string) (status DaemonStatusValue, pid int, err error) {
	// 1. Try socket probe first — this is the real liveness signal.
	if sockPID := probeSocket(sockPath); sockPID > 0 {
		if IsProcessAlive(sockPID) {
			return StatusRunning, sockPID, nil
		}
	}

	// 2. Fall back to PID file (covers startup race: socket not ready yet).
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
//
// The authorized flag gates SIGTERM handling:
//   - nil: SIGTERM always honored (backward compatibility)
//   - false: SIGTERM logged and ignored (not authorized by shutdown directive)
//   - true: SIGTERM honored (authorized by shutdown directive)
//
// SIGINT is always honored regardless of authorization (human Ctrl+C).
// SIGPIPE is explicitly ignored so the daemon survives broken stdout/stderr
// pipes after the parent process exits.
func SetupSignalHandler(parent context.Context, pidPath string, authorized *atomic.Bool) (shutdownCtx context.Context, cleanup func()) {
	ctx, cancel := context.WithCancel(parent)

	signal.Ignore(syscall.SIGPIPE)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		for {
			select {
			case sig := <-sigCh:
				// SIGINT: always honor (human Ctrl+C).
				if sig == syscall.SIGINT {
					fmt.Fprintf(os.Stderr, "shutdown: received %v (PID %d)\n", sig, os.Getpid())
					cancel()
					signal.Stop(sigCh)
					return
				}
				// SIGTERM: check authorization.
				if authorized != nil && !authorized.Load() {
					fmt.Fprintf(os.Stderr, "shutdown: ignoring %v — not authorized (use 'oro stop'); PID %d\n", sig, os.Getpid())
					continue
				}
				fmt.Fprintf(os.Stderr, "shutdown: received %v (PID %d)\n", sig, os.Getpid())
				dumpProcessSnapshot()
				cancel()
				signal.Stop(sigCh)
				return
			case <-ctx.Done():
				signal.Stop(sigCh)
				return
			}
		}
	}()

	cleanup = func() {
		cancel()
		_ = RemovePIDFile(pidPath)
	}

	return ctx, cleanup
}

// dumpProcessSnapshot logs all oro-related processes at signal receipt time.
// This helps identify which process sent the fatal signal.
func dumpProcessSnapshot() {
	out, err := exec.Command("ps", "axo", "pid,ppid,pgid,command").CombinedOutput() //nolint:gosec,noctx // diagnostic snapshot, no context needed
	if err != nil {
		fmt.Fprintf(os.Stderr, "SIGNAL-SNAPSHOT: ps failed: %v\n", err)
		return
	}
	fmt.Fprintln(os.Stderr, "SIGNAL-SNAPSHOT: process list at signal time:")
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "oro") || strings.Contains(line, "kill") ||
			strings.Contains(line, "pkill") || strings.Contains(line, "PID") {
			fmt.Fprintf(os.Stderr, "  %s\n", line)
		}
	}
}
