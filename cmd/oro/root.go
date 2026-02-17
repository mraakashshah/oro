package main

import (
	"fmt"

	"oro/internal/appversion"

	"github.com/spf13/cobra"
)

// newRootCmd creates the root oro command with all subcommands attached.
func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "oro",
		Short:         "Oro agent swarm orchestrator",
		Long:          "oro is the single entry point for the Oro agent swarm.\nIt manages session orchestration and the memory interface.",
		Version:       fmt.Sprintf("oro %s", appversion.String()),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.SetVersionTemplate("{{.Version}}\n")

	cmd.AddCommand(
		newInitCmd(),
		newStartCmd(),
		newDispatcherCmd(),
		newStopCmd(),
		newStatusCmd(),
		newDashCmd(),
		newDirectiveCmd(),
		newRememberCmdWithStore(nil),
		newRecallCmdWithStore(nil),
		newForgetCmd(),
		newIngestCmd(),
		newWorkerCmd(),
		newMemoriesCmd(),
		newLogsCmd(),
		newIndexCmd(),
		newCleanupCmd(),
		newHelpCmd(cmd),
		newWorkCmd(),
	)

	return cmd
}
