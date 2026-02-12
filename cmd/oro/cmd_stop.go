package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// stopConfig holds injectable dependencies for the graceful shutdown sequence.
type stopConfig struct {
	pidPath  string
	sockPath string
	tmuxName string
	runner   CmdRunner
	w        io.Writer
	signalFn func(int) error // sends SIGTERM; injectable for testing
	aliveFn  func(int) bool  // checks process liveness; injectable for testing
}

// drainTimeout is how long to wait for the dispatcher to exit after SIGTERM.
const drainTimeout = 30 * time.Second

// drainPollInterval is how often to check if the dispatcher has exited.
const drainPollInterval = 200 * time.Millisecond

// newStopCmd creates the "oro stop" subcommand.
func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Graceful shutdown of the Oro swarm",
		Long:  "Sends a stop directive to the dispatcher, waits for workers to finish,\nkills the tmux session, and runs bd sync.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if role := os.Getenv("ORO_ROLE"); role != "" {
				return fmt.Errorf("oro stop can only be run by a human (ORO_ROLE=%s indicates agent context)", role)
			}
			pidPath, err := oroPath("ORO_PID_PATH", "oro.pid")
			if err != nil {
				return fmt.Errorf("get pid path: %w", err)
			}
			sockPath, err := oroPath("ORO_SOCKET_PATH", "oro.sock")
			if err != nil {
				return fmt.Errorf("get socket path: %w", err)
			}

			cfg := &stopConfig{
				pidPath:  pidPath,
				sockPath: sockPath,
				tmuxName: "oro",
				runner:   &ExecRunner{},
				w:        cmd.OutOrStdout(),
				signalFn: defaultSignal,
				aliveFn:  IsProcessAlive,
			}

			return runStopSequence(cmd.Context(), cfg)
		},
	}
}

// defaultSignal sends SIGTERM to the given PID.
func defaultSignal(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("send SIGTERM to PID %d: %w", pid, err)
	}
	return nil
}

// runStopSequence performs the full graceful shutdown:
//  1. Send "stop" directive via UDS (stops new assignments)
//  2. SIGTERM the dispatcher (triggers internal drain)
//  3. Wait for the dispatcher process to exit
//  4. Kill the tmux session
//  5. Run bd sync
//  6. Remove PID file
func runStopSequence(ctx context.Context, cfg *stopConfig) error {
	status, pid, err := DaemonStatus(cfg.pidPath)
	if err != nil {
		return fmt.Errorf("get daemon status: %w", err)
	}

	switch status {
	case StatusStopped:
		fmt.Fprintln(cfg.w, "dispatcher is not running")
		return nil
	case StatusStale:
		fmt.Fprintln(cfg.w, "removing stale PID file (process already dead)")
		return RemovePIDFile(cfg.pidPath)
	}

	// 1. Send "stop" directive via UDS. Best-effort — if socket is gone,
	//    SIGTERM alone still triggers the dispatcher's graceful shutdown.
	fmt.Fprintf(cfg.w, "sending stop directive to dispatcher (PID %d)\n", pid)
	if derr := sendStopDirective(ctx, cfg.sockPath); derr != nil {
		fmt.Fprintf(cfg.w, "warning: could not send stop directive: %v\n", derr)
	}

	// 2. Signal the dispatcher process.
	if err := cfg.signalFn(pid); err != nil {
		return fmt.Errorf("signal dispatcher: %w", err)
	}

	// 3. Wait for the dispatcher to exit (the drain happens internally).
	fmt.Fprintln(cfg.w, "waiting for dispatcher to drain and exit...")
	if err := waitForExit(ctx, pid, cfg.aliveFn); err != nil {
		fmt.Fprintf(cfg.w, "warning: %v\n", err)
	}

	// 4. Kill the tmux session.
	tmux := &TmuxSession{Name: cfg.tmuxName, Runner: cfg.runner}
	if err := tmux.Kill(); err != nil {
		fmt.Fprintf(cfg.w, "warning: tmux kill: %v\n", err)
	}

	// 5. Run bd sync as a safety net.
	if _, err := cfg.runner.Run("bd", "sync", "--flush-only"); err != nil {
		fmt.Fprintf(cfg.w, "warning: bd sync: %v\n", err)
	}

	// 6. Remove PID file (belt and suspenders — signal handler may have already done it).
	_ = RemovePIDFile(cfg.pidPath)

	fmt.Fprintln(cfg.w, "shutdown complete")
	return nil
}

// sendStopDirective connects to the dispatcher UDS and sends a "stop" directive.
func sendStopDirective(ctx context.Context, sockPath string) error {
	conn, err := dialDispatcher(ctx, sockPath)
	if err != nil {
		return fmt.Errorf("dial dispatcher: %w", err)
	}
	defer conn.Close()

	if err := sendDirective(conn, "stop", ""); err != nil {
		return fmt.Errorf("send stop directive: %w", err)
	}

	if _, err := readACK(conn); err != nil {
		return fmt.Errorf("read ack: %w", err)
	}

	return nil
}

// waitForExit polls until the process is no longer alive or timeout.
func waitForExit(ctx context.Context, pid int, aliveFn func(int) bool) error {
	if !aliveFn(pid) {
		return nil
	}

	deadline := time.After(drainTimeout)
	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !aliveFn(pid) {
				return nil
			}
		case <-deadline:
			return fmt.Errorf("timeout waiting for dispatcher (PID %d) to exit", pid)
		case <-ctx.Done():
			return fmt.Errorf("wait for dispatcher exit: %w", ctx.Err())
		}
	}
}
