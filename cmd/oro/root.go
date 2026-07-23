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
		Long:          "oro is the single entry point for the Oro agent swarm.\nIt manages session orchestration and knowledge cards.",
		Version:       fmt.Sprintf("oro %s", appversion.String()),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.SetVersionTemplate("{{.Version}}\n")

	cmd.AddCommand(rootSubcommands(cmd)...)

	for _, alias := range editRootAliases() {
		cmd.AddCommand(alias)
	}

	return cmd
}

func rootSubcommands(root *cobra.Command) []*cobra.Command {
	return []*cobra.Command{
		newInitCmd(),
		newSetupCmd(),
		newStartCmd(),
		newAttachCmd(),
		newShellCmd(),
		newDispatcherCmd(),
		newStopCmd(),
		newStatusCmd(),
		newStorageCmd(),
		newHealthCmd(),
		newOpsCmd(),
		newRecoveryCmd(),
		newMonitorCmd(),
		newThroughputCmd(),
		newDashboardCmd(),
		newDirectiveCmd(),
		newRememberCmdWithStore(nil),
		newRecallCmdWithStore(nil),
		newForgetCmd(),
		newWorkerCmd(),
		newMemoriesCmd(),
		newLogsCmd(),
		newEventsCmd(),
		newIndexCmd(),
		newCleanupCmd(),
		newHelpCmd(root),
		newWorkCmd(),
		newEvidenceCmd(),
		newTaskCmd(),
		newGlobalOroApproachCmd(),
		newGlobalOroApproachAliasCmd("global-skills"),
		newGlobalOroApproachAliasCmd("global-oro-approach"),
		newDoctorCmd(),
		newUninstallCmd(),
		newModelsCmd(),
		newOutlineCmd(),
		newImpactCmd(),
		newLeakscanCmd(),
		newEditCmd(),
		newCardsCmd(),
		newDoctrineCmd(),
		newTestContextSafetyCmd(),
		newHarnessCmd(),
		newCurrentCmd(),
		newVersionCmd(root),
		newHandoffCmd(),
		newResumeCmd(),
		newReviewCmd(),
		newReviewPatternsCmd(),
		newJanitorDetectCmd(),
	}
}
