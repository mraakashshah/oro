package main

import (
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
	return cmd
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
