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
			fmt.Fprintln(cmd.OutOrStdout(), "not implemented")
			return nil
		},
	}
}
