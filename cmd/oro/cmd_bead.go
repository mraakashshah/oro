package main

import (
	"fmt"

	"oro/pkg/beadstore"

	"github.com/spf13/cobra"
)

func newBeadCmd() *cobra.Command {
	return newBeadCmdWithStore(nil)
}

func newBeadCmdWithStore(store beadstore.Store) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "bead",
		Short: "Manage native Oro beads",
		Long:  "Manage native Oro beads.",
	}
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "emit machine-readable JSON output")

	cmd.AddCommand(
		newBeadReadyCmd(store),
		newBeadListCmd(store),
		newBeadShowCmd(store),
		newBeadCreateCmd(store),
		newBeadUpdateCmd(store),
		newBeadCloseCmd(store),
		newBeadBlockedCmd(store),
		newBeadClosedCmd(store),
		newBeadDepCmd(store),
		newBeadExportCmd(store),
	)

	return cmd
}

func newBeadReadyCmd(store beadstore.Store) *cobra.Command {
	return newBeadStubCmd(store, "ready", "List unblocked open beads", cobra.NoArgs)
}

func newBeadListCmd(store beadstore.Store) *cobra.Command {
	cmd := newBeadStubCmd(store, "list", "List beads with optional filters", cobra.NoArgs)
	cmd.Flags().String("status", "", "filter by status")
	cmd.Flags().String("parent", "", "filter by parent bead ID")
	cmd.Flags().String("tag", "", "filter by tag")
	cmd.Flags().Int("limit", 0, "maximum beads to return")
	return cmd
}

func newBeadShowCmd(store beadstore.Store) *cobra.Command {
	cmd := newBeadStubCmd(store, "show <id>", "Show one bead", cobra.ExactArgs(1))
	cmd.Flags().Bool("long", false, "show full bead details")
	return cmd
}

func newBeadCreateCmd(store beadstore.Store) *cobra.Command {
	cmd := newBeadStubCmd(store, "create", "Create a bead", cobra.NoArgs)
	cmd.Flags().String("id", "", "explicit bead ID")
	cmd.Flags().String("title", "", "bead title")
	cmd.Flags().String("type", "task", "bead type")
	cmd.Flags().Int("priority", 2, "bead priority; 0 is highest")
	cmd.Flags().String("parent", "", "parent bead ID")
	cmd.Flags().String("description", "", "bead description")
	cmd.Flags().String("acceptance", "", "acceptance criteria")
	cmd.Flags().String("acceptance-criteria", "", "acceptance criteria")
	cmd.Flags().Int("estimate", 0, "estimated minutes")
	cmd.Flags().StringArray("tag", nil, "tag to attach; repeatable")
	return cmd
}

func newBeadUpdateCmd(store beadstore.Store) *cobra.Command {
	cmd := newBeadStubCmd(store, "update <id>", "Update a bead", cobra.ExactArgs(1))
	cmd.Flags().String("status", "", "new status")
	cmd.Flags().Int("priority", -1, "new priority")
	cmd.Flags().String("type", "", "new bead type")
	cmd.Flags().String("parent", "", "new parent bead ID")
	cmd.Flags().String("notes", "", "notes to append")
	cmd.Flags().String("acceptance", "", "acceptance criteria")
	cmd.Flags().String("owner", "", "new owner")
	return cmd
}

func newBeadCloseCmd(store beadstore.Store) *cobra.Command {
	cmd := newBeadStubCmd(store, "close <id>", "Close a bead", cobra.ExactArgs(1))
	cmd.Flags().String("reason", "", "close reason")
	return cmd
}

func newBeadBlockedCmd(store beadstore.Store) *cobra.Command {
	return newBeadStubCmd(store, "blocked", "List blocked beads", cobra.NoArgs)
}

func newBeadClosedCmd(store beadstore.Store) *cobra.Command {
	cmd := newBeadStubCmd(store, "closed", "List recently closed beads", cobra.NoArgs)
	cmd.Flags().Int("limit", 50, "maximum closed beads to return")
	return cmd
}

func newBeadDepCmd(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dep",
		Short: "Manage bead dependencies",
	}

	addCmd := newBeadStubCmd(store, "add <bead-id> <depends-on-id>", "Add a dependency", cobra.ExactArgs(2))
	addCmd.Flags().String("type", "blocks", "dependency type")

	cmd.AddCommand(
		addCmd,
		newBeadStubCmd(store, "rm <bead-id> <depends-on-id>", "Remove a dependency", cobra.ExactArgs(2)),
		newBeadStubCmd(store, "list <bead-id>", "List dependencies for a bead", cobra.ExactArgs(1)),
	)

	return cmd
}

func newBeadExportCmd(store beadstore.Store) *cobra.Command {
	cmd := newBeadStubCmd(store, "export", "Export a bead snapshot", cobra.NoArgs)
	cmd.Flags().String("out", "", "output path")
	cmd.Flags().String("format", "jsonl", "output format: jsonl or json")
	return cmd
}

func newBeadStubCmd(store beadstore.Store, use, short string, args cobra.PositionalArgs) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  args,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = store
			return fmt.Errorf("%s is not implemented yet", cmd.CommandPath())
		},
	}
}
