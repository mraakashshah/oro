package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newDoctorCmd creates the "oro doctor" subcommand.
func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose oro installation issues",
		Args:  cobra.NoArgs,
		Long: `Diagnose common oro installation issues.

The legacy Dolt recovery command was removed during the native SQLite
beadstore cutover. bd/Dolt backups remain rollback references, but they are no
longer repaired or restarted by production Oro commands.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "oro doctor: no automatic repairs are currently registered")
			return nil
		},
	}
}
