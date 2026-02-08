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
			fmt.Fprintln(cmd.OutOrStdout(), "not implemented")
			return nil
		},
	}
}
