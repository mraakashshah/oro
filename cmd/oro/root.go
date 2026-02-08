package main

import (
	"fmt"

	"oro/internal/version"

	"github.com/spf13/cobra"
)

// newRootCmd creates the root oro command with all subcommands attached.
func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "oro",
		Short:         "Oro agent swarm orchestrator",
		Long:          "oro is the single entry point for the Oro agent swarm.\nIt manages session orchestration and the memory interface.",
		Version:       fmt.Sprintf("oro %s", version.String()),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.SetVersionTemplate("{{.Version}}\n")

	cmd.AddCommand(
		newStartCmd(),
		newStopCmd(),
		newStatusCmd(),
		newDirectiveCmd(),
		newRememberCmd(),
		newRecallCmd(),
	)

	return cmd
}
