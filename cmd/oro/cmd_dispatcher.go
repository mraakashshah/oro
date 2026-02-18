package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// newDispatcherCmd creates the "oro dispatcher" subcommand group.
func newDispatcherCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dispatcher",
		Short: "Manage the Oro dispatcher daemon",
		Long:  "Subcommands for starting and stopping the Oro dispatcher daemon without a tmux session.",
	}

	cmd.AddCommand(newDispatcherStartCmd())
	cmd.AddCommand(newDispatcherStopCmd())
	return cmd
}

// newDispatcherStopCmd creates the "oro dispatcher stop" subcommand.
// It sends SIGINT to the dispatcher daemon, waits for it to drain and exit,
// runs bd sync --flush-only, and removes the PID file.
// Unlike "oro stop", it does NOT kill the tmux session or clean up pane-died hooks.
func newDispatcherStopCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the dispatcher daemon (no tmux kill)",
		Long: `Stops the Oro dispatcher daemon by sending SIGINT and waiting for it to drain.
Does NOT kill the tmux session or clean up pane-died hooks.
Requires an interactive terminal (TTY) or --force with ORO_HUMAN_CONFIRMED=1.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolvePaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}

			cfg := &stopConfig{
				pidPath:  paths.PIDPath,
				sockPath: paths.SocketPath,
				runner:   &ExecRunner{},
				w:        cmd.OutOrStdout(),
				stdin:    os.Stdin,
				signalFn: defaultSignalINT,
				aliveFn:  IsProcessAlive,
				killFn:   defaultKill,
				isTTY:    isStdinTTY,
				force:    force,
			}

			return runDispatcherStopSequence(cmd.Context(), cfg)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip interactive confirmation (requires ORO_HUMAN_CONFIRMED=1)")
	return cmd
}

// runDispatcherStopSequence performs a dispatcher-only graceful shutdown:
//  0. Confirm the caller is authorized (interactive TTY or --force)
//  1. Send SIGINT to the dispatcher (triggers graceful drain)
//  2. Wait for the dispatcher process to exit
//  3. If process won't exit: SIGKILL as emergency fallback
//  4. Run bd sync as a safety net
//  5. Remove PID file
//
// Key difference from runStopSequence: does NOT kill the tmux session and
// does NOT clean up pane-died hooks.
func runDispatcherStopSequence(ctx context.Context, cfg *stopConfig) error {
	status, pid, err := DaemonStatus(cfg.pidPath, cfg.sockPath)
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

	// 4. Run bd sync as a safety net.
	if _, err := cfg.runner.Run("bd", "sync", "--flush-only"); err != nil {
		fmt.Fprintf(cfg.w, "warning: bd sync: %v\n", err)
	}

	// 5. Remove PID file (belt and suspenders — signal handler may have already done it).
	_ = RemovePIDFile(cfg.pidPath)

	fmt.Fprintln(cfg.w, "shutdown complete")
	return nil
}

// newDispatcherStartCmd creates the "oro dispatcher start" subcommand.
// It spawns the dispatcher daemon (no tmux session), waits for the socket,
// sends the start directive, and prints the PID.
func newDispatcherStartCmd() *cobra.Command {
	var (
		workers int
		force   bool
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the dispatcher daemon (no tmux session)",
		Long: `Starts the Oro dispatcher as a background daemon without creating a tmux session.
Useful for CI environments or manual worker management (--workers 0 disables auto-scaling).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()

			if !force {
				pidPath, err := preflightAndCheckRunning(w)
				if err != nil {
					return err
				}
				if pidPath == "" {
					return nil // already running
				}
			}

			return runDispatcherStart(w, workers, &ExecDaemonSpawner{}, socketPollTimeout)
		},
	}

	cmd.Flags().IntVarP(&workers, "workers", "w", 0, "number of workers to auto-spawn (0 = manual mode)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip running check")

	return cmd
}

// runDispatcherStart implements the core logic for "oro dispatcher start":
// 1. Spawn daemon subprocess (reuses ExecDaemonSpawner / DaemonSpawner interface)
// 2. Wait for socket file to appear
// 3. Send start directive
// 4. Print PID and status
//
// No tmux session is created. The spawner is injected for testability.
func runDispatcherStart(w io.Writer, workers int, spawner DaemonSpawner, socketTimeout time.Duration) error {
	paths, err := ResolvePaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	pidPath := paths.PIDPath
	sockPath := paths.SocketPath

	// Spawn the daemon subprocess.
	pid, err := spawner.SpawnDaemon(pidPath, workers)
	if err != nil {
		return fmt.Errorf("spawn daemon: %w", err)
	}

	// Wait for the dispatcher socket to appear.
	deadline := time.Now().Add(socketTimeout)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(sockPath); statErr == nil {
			break
		}
		time.Sleep(socketPollInterval)
	}
	if _, err := os.Stat(sockPath); err != nil {
		return fmt.Errorf("dispatcher socket not ready at %s: %w", sockPath, err)
	}

	// Send start directive so dispatcher transitions from Inert to Running.
	if err := sendStartDirective(sockPath); err != nil {
		return fmt.Errorf("send start directive: %w", err)
	}

	fmt.Fprintf(w, "dispatcher started (PID %d, workers=%d)\n", pid, workers)
	return nil
}
