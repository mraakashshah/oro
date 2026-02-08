package main

import (
	"fmt"

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
			fmt.Fprintln(cmd.OutOrStdout(), "not implemented")
			_ = workers
			_ = daemonOnly
			_ = model
			return nil
		},
	}

	cmd.Flags().IntVarP(&workers, "workers", "w", 2, "number of workers to spawn")
	cmd.Flags().BoolVarP(&daemonOnly, "daemon-only", "d", false, "start dispatcher without tmux/sessions (for CI or testing)")
	cmd.Flags().StringVar(&model, "model", "sonnet", "model for manager session")

	return cmd
}
