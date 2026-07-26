package main

import (
	"fmt"
	"strings"

	"oro/pkg/beadstore"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func newTaskCmd() *cobra.Command {
	return newTaskCmdWithStore(nil)
}

func newTaskCmdWithStore(store beadstore.Store) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "task",
		Short: "Manage native Oro tasks",
		Long:  "Manage native Oro tasks.",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return guardTaskWorkerMutation(cmd)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unknown task command %q", args[0])
			}
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "emit machine-readable JSON output")

	cmd.AddCommand(
		newBeadReadyCmd(store),
		newBeadListCmd(store),
		newBeadShowCmd(store),
		newBeadCreateCmd(store),
		newBeadUpdateCmd(store),
		newBeadCloseCmd(store),
		newBeadDeleteCmd(store),
		newBeadReopenCmd(store),
		newBeadDeferCmd(store),
		newBeadUndeferCmd(store),
		newBeadBlockedCmd(store),
		newBeadClosedCmd(store),
		newBeadDepCmd(store),
		newTaskNoteCmd(store),
		newBeadExportCmd(store),
		newBeadStatusCmd(store),
		newTaskProposeBlockerCmd(),
	)
	adaptTaskCommandHelp(cmd)

	return cmd
}

func newTaskNoteCmd(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "note",
		Short: "Manage bead notes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newBeadNoteAddCmd(store))
	return cmd
}

func adaptTaskCommandHelp(cmd *cobra.Command) {
	taskHelpReplacer := strings.NewReplacer(
		"Beads", "Tasks",
		"beads", "tasks",
		"Bead", "Task",
		"bead", "task",
	)
	for _, sub := range cmd.Commands() {
		sub.Short = taskHelpReplacer.Replace(sub.Short)
		sub.Long = taskHelpReplacer.Replace(sub.Long)
		sub.Example = taskHelpReplacer.Replace(sub.Example)
		sub.Use = taskHelpReplacer.Replace(sub.Use)
		sub.Flags().VisitAll(func(flag *pflag.Flag) {
			flag.Usage = taskHelpReplacer.Replace(flag.Usage)
		})
		adaptTaskCommandHelp(sub)
	}
}
