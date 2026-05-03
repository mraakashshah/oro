package main

import (
	"fmt"

	"oro/pkg/beadstore"

	"github.com/spf13/cobra"
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
		newBeadStubCmd(store, "search <query>", "Search beads", cobra.ExactArgs(1)),
		newBeadExportCmd(store),
		newBeadStubCmd(store, "import <path>", "Import bead snapshot", cobra.ExactArgs(1)),
		newBeadStubCmd(store, "doctor", "Check bead-store health", cobra.NoArgs),
		newBeadStatusCmd(store),
	)

	return cmd
}
