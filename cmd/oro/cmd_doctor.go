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
		Long:  `Diagnose common oro installation issues.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "oro doctor: no automatic repairs are currently registered")
			return nil
		},
	}
}
