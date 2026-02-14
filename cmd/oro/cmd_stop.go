package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// stopConfig holds injectable dependencies for the graceful shutdown sequence.
type stopConfig struct {
	pidPath  string
	tmuxName string
	runner   CmdRunner
	w        io.Writer
	stdin    io.Reader       // stdin for interactive confirmation
	signalFn func(int) error // sends SIGINT; injectable for testing
	aliveFn  func(int) bool  // checks process liveness; injectable for testing
	killFn   func(int) error // sends SIGKILL; injectable for testing
	isTTY    func() bool     // returns true if stdin is a TTY; injectable for testing
	force    bool            // --force flag: skip interactive confirmation
}

// drainTimeout is how long to wait for the dispatcher to exit after SIGTERM.
const drainTimeout = 30 * time.Second

// drainPollInterval is how often to check if the dispatcher has exited.
const drainPollInterval = 200 * time.Millisecond

// isStdinTTY returns true if os.Stdin is connected to a terminal.
func isStdinTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// newStopCmd creates the "oro stop" subcommand.
func newStopCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Graceful shutdown of the Oro swarm",
		Long:  "Sends a stop directive to the dispatcher, waits for workers to finish,\nkills the tmux session, and runs bd sync.",
		RunE: func(cmd *cobra.Command, args []string) error {
			pidPath, err := oroPath("ORO_PID_PATH", "oro.pid")
			if err != nil {
				return fmt.Errorf("get pid path: %w", err)
			}

			cfg := &stopConfig{
				pidPath:  pidPath,
				tmuxName: "oro",
				runner:   &ExecRunner{},
				w:        cmd.OutOrStdout(),
				stdin:    os.Stdin,
				signalFn: defaultSignalINT,
				aliveFn:  IsProcessAlive,
				killFn:   defaultKill,
				isTTY:    isStdinTTY,
				force:    force,
			}

			return runStopSequence(cmd.Context(), cfg)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip interactive confirmation (requires ORO_HUMAN_CONFIRMED=1)")
	return cmd
}

// defaultSignalINT sends SIGINT to the given PID.
// SIGINT is always honored by the daemon (like Ctrl+C), unlike SIGTERM which
// requires prior authorization via shutdown directive. This avoids the UDS
// directive path which agents can abuse.
func defaultSignalINT(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGINT); err != nil {
		return fmt.Errorf("send SIGINT to PID %d: %w", pid, err)
	}
	return nil
}

// defaultKill sends SIGKILL to the given PID.
func defaultKill(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("send SIGKILL to PID %d: %w", pid, err)
	}
	return nil
}

// confirmStop checks that the caller is authorized to stop the dispatcher.
// In interactive mode, it prompts for "YES" on stdin.
// With --force, it requires ORO_HUMAN_CONFIRMED=1.
// Returns an error if confirmation fails.
func confirmStop(cfg *stopConfig) error {
	if cfg.force {
		if os.Getenv("ORO_HUMAN_CONFIRMED") != "1" {
			return fmt.Errorf("--force requires ORO_HUMAN_CONFIRMED=1 environment variable")
		}
		return nil
	}

	if cfg.isTTY != nil && !cfg.isTTY() {
		return fmt.Errorf("oro stop requires an interactive terminal (stdin is not a TTY)\n" +
			"Hint: use --force with ORO_HUMAN_CONFIRMED=1 for non-interactive use")
	}

	fmt.Fprint(cfg.w, "Type YES to confirm shutdown: ")
	scanner := bufio.NewScanner(cfg.stdin)
	if !scanner.Scan() {
		return fmt.Errorf("failed to read confirmation from stdin")
	}
	if strings.TrimSpace(scanner.Text()) != "YES" {
		return fmt.Errorf("shutdown aborted (expected YES)")
	}
	return nil
}

// runStopSequence performs the full graceful shutdown:
//  0. Confirm the caller is authorized (interactive TTY or --force)
//  1. Send SIGINT to the dispatcher (always honored, triggers graceful drain)
//  2. Wait for the dispatcher process to exit
//  3. If process won't exit: SIGKILL as emergency fallback
//  4. Clean up pane-died hooks
//  5. Kill the tmux session
//  6. Run bd sync
//  7. Remove PID file
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

	// 0. Confirm authorization before proceeding.
	if err := confirmStop(cfg); err != nil {
		return err
	}

	// 1. Send SIGINT (always honored by daemon, like Ctrl+C).
	fmt.Fprintf(cfg.w, "sending SIGINT to dispatcher (PID %d)\n", pid)
	if err := cfg.signalFn(pid); err != nil {
		fmt.Fprintf(cfg.w, "warning: SIGINT failed: %v\n", err)
	}

	// 2. Wait for the dispatcher to exit.
	fmt.Fprintln(cfg.w, "waiting for dispatcher to drain and exit...")
	if err := waitForExit(ctx, pid, cfg.aliveFn); err != nil {
		fmt.Fprintf(cfg.w, "warning: %v\n", err)
		// 3. Emergency fallback: SIGKILL if process won't exit.
		if cfg.killFn != nil {
			fmt.Fprintf(cfg.w, "sending SIGKILL to dispatcher (PID %d)\n", pid)
			if killErr := cfg.killFn(pid); killErr != nil {
				fmt.Fprintf(cfg.w, "warning: SIGKILL failed: %v\n", killErr)
			}
		}
	}

	// 4. Clean up pane-died hooks before killing the tmux session.
	tmux := &TmuxSession{Name: cfg.tmuxName, Runner: cfg.runner}
	_ = tmux.CleanupPaneDiedHooks() // Best effort; non-fatal if hooks weren't registered

	// 5. Kill the tmux session.
	if err := tmux.Kill(); err != nil {
		fmt.Fprintf(cfg.w, "warning: tmux kill: %v\n", err)
	}

	// 6. Run bd sync as a safety net.
	if _, err := cfg.runner.Run("bd", "sync", "--flush-only"); err != nil {
		fmt.Fprintf(cfg.w, "warning: bd sync: %v\n", err)
	}

	// 7. Remove PID file (belt and suspenders — signal handler may have already done it).
	_ = RemovePIDFile(cfg.pidPath)

	fmt.Fprintln(cfg.w, "shutdown complete")
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
