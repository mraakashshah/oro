package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newStopCmd creates the "oro stop" subcommand.
func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Graceful shutdown of the Oro swarm",
		Long:  "Sends a stop directive to the dispatcher, waits for workers to finish,\nkills the tmux session, and runs bd sync.",
		RunE: func(cmd *cobra.Command, args []string) error {
			pidPath, err := DefaultPIDPath()
			if err != nil {
				return err
			}

			status, pid, err := DaemonStatus(pidPath)
			if err != nil {
				return err
			}

			switch status {
			case StatusStopped:
				fmt.Fprintln(cmd.OutOrStdout(), "dispatcher is not running")
				return nil
			case StatusStale:
				fmt.Fprintln(cmd.OutOrStdout(), "removing stale PID file (process already dead)")
				return RemovePIDFile(pidPath)
			case StatusRunning:
				fmt.Fprintf(cmd.OutOrStdout(), "sending SIGTERM to dispatcher (PID %d)\n", pid)
				if err := StopDaemon(pidPath); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "stop signal sent")
				return nil
			}

			return nil
		},
	}
}
