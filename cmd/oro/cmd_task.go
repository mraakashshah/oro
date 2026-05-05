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
		Long:  "Manage native Oro tasks. The bead command is the legacy alias; migrate-from-dolt is only available via bead.",
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
		newBeadReopenCmd(store),
		newBeadDeferCmd(store),
		newBeadUndeferCmd(store),
		newBeadBlockedCmd(store),
		newBeadClosedCmd(store),
		newBeadDepCmd(store),
		newBeadTagCmd(store),
		newBeadMetaCmd(store),
		newBeadNoteCmd(store),
		newBeadStubCmd(store, "search <query>", "Search tasks", cobra.ExactArgs(1)),
		newBeadExportCmd(store),
		newBeadStubCmd(store, "import <path>", "Import task snapshot", cobra.ExactArgs(1)),
		newBeadStubCmd(store, "doctor", "Check task store health", cobra.NoArgs),
		newBeadStatusCmd(store),
		newBeadGateStateCmd(store),
		newBeadPremortemCloseCmd(store),
		newTaskMigrationUnavailableCmd(),
	)
	adaptTaskCommandHelp(cmd)

	return cmd
}

func newTaskMigrationUnavailableCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "migrate-from-dolt",
		Hidden:             true,
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("task migrate-from-dolt is unavailable; use oro bead migrate-from-dolt")
		},
	}
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
