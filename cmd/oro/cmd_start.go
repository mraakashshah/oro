package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// newStartCmd creates the "oro start" subcommand.
func newStartCmd() *cobra.Command {
	var (
		workers    int
		daemonOnly bool
		model      string
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Launch the Oro swarm (tmux session + dispatcher)",
		Long:  "Creates a tmux session with the full Oro layout and begins autonomous execution.\nStarts the dispatcher daemon and worker pool in the background.",
		RunE: func(cmd *cobra.Command, args []string) error {
			pidPath, err := DefaultPIDPath()
			if err != nil {
				return err
			}

			// Check if already running.
			status, pid, err := DaemonStatus(pidPath)
			if err != nil {
				return err
			}

			switch status {
			case StatusRunning:
				fmt.Fprintf(cmd.OutOrStdout(), "dispatcher already running (PID %d)\n", pid)
				return nil
			case StatusStale:
				// Clean up stale PID file before starting fresh.
				_ = RemovePIDFile(pidPath)
			case StatusStopped:
				// Good to go.
			}

			if daemonOnly {
				// In daemon-only mode, run the dispatcher in the foreground
				// of this process (used for testing/CI).
				fmt.Fprintf(cmd.OutOrStdout(), "starting dispatcher (PID %d, workers=%d)\n", os.Getpid(), workers)
				if err := WritePIDFile(pidPath, os.Getpid()); err != nil {
					return err
				}

				ctx := cmd.Context()
				shutdownCtx, cleanup := SetupSignalHandler(ctx, pidPath)
				defer cleanup()

				// Block until signal — the real dispatcher.Run() goes here.
				<-shutdownCtx.Done()
				fmt.Fprintln(cmd.OutOrStdout(), "dispatcher stopped")
				return nil
			}

			// TODO: full start — spawn daemon subprocess, create tmux session.
			_ = model
			fmt.Fprintf(cmd.OutOrStdout(), "starting oro swarm (workers=%d, model=%s)\n", workers, model)
			return nil
		},
	}

	cmd.Flags().IntVarP(&workers, "workers", "w", 2, "number of workers to spawn")
	cmd.Flags().BoolVarP(&daemonOnly, "daemon-only", "d", false, "start dispatcher without tmux/sessions (for CI or testing)")
	cmd.Flags().StringVar(&model, "model", "sonnet", "model for manager session")

	return cmd
}
