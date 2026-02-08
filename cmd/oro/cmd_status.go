package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newStatusCmd creates the "oro status" subcommand.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current swarm state",
		Long:  "Displays dispatcher status, worker count and active beads,\nmanager state, and bead summary.",
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
			case StatusRunning:
				fmt.Fprintf(cmd.OutOrStdout(), "dispatcher: running (PID %d)\n", pid)
			case StatusStale:
				fmt.Fprintf(cmd.OutOrStdout(), "dispatcher: stale (PID %d, process dead)\n", pid)
			case StatusStopped:
				fmt.Fprintln(cmd.OutOrStdout(), "dispatcher: stopped")
			}

			return nil
		},
	}
}
