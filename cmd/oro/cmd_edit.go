package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// editSubcommandFactories returns factory functions for the 12 edit subcommands.
// Each factory is called once for the real subcommand under 'edit' and once for
// the hidden root-level colon alias (e.g. 'edit:replace').
func editSubcommandFactories() []func() *cobra.Command {
	return []func() *cobra.Command{
		newEditReplaceCmd,
		newEditAfterCmd,
		newEditDeleteCmd,
		newEditRenameCmd,
		newEditRenameAllCmd,
		newEditMoveCmd,
		newEditMoveToFileCmd,
		newEditReadCmd,
		newEditDiffCmd,
		newEditUndoCmd,
		newEditBatchCmd,
		newEditCheckCmd,
	}
}

// newEditCmd creates the 'oro edit' parent command with all 12 subcommands.
func newEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "AST-aware file editing operations",
		Long: "Deterministic AST-aware editing for Go, Python, TypeScript, and JavaScript source files.\n" +
			"Workers invoke subcommands as 'oro edit:op ...' in their Bash tool (see 'oro edit:replace --help' etc.).",
	}
	for _, factory := range editSubcommandFactories() {
		cmd.AddCommand(factory())
	}
	return cmd
}

// editRootAliases returns hidden root-level commands for the 12 edit subcommands
// using colon notation (e.g. 'edit:replace'). Workers invoke these from Bash.
func editRootAliases() []*cobra.Command {
	cmds := make([]*cobra.Command, 0, 12)
	for _, factory := range editSubcommandFactories() {
		sub := factory()
		alias := &cobra.Command{
			Use:    "edit:" + sub.Use,
			Short:  sub.Short,
			Long:   sub.Long,
			Hidden: true,
			Args:   sub.Args,
			RunE:   sub.RunE,
		}
		alias.Flags().AddFlagSet(sub.Flags())
		cmds = append(cmds, alias)
	}
	return cmds
}

func newEditReplaceCmd() *cobra.Command {
	var snippet string
	cmd := &cobra.Command{
		Use:   "replace FILE SYMBOL",
		Short: "Replace a symbol's body with a snippet",
		Long: "Replaces the body of the named SYMBOL in FILE with the provided --snippet.\n" +
			"Returns EFALLTHROUGH when the anchor is not deterministically locatable.",
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, _ []string) error {
			_ = snippet
			return fmt.Errorf("oro edit:replace: not yet implemented")
		},
	}
	cmd.Flags().StringVar(&snippet, "snippet", "", "replacement source text (required)")
	_ = cmd.MarkFlagRequired("snippet")
	return cmd
}

func newEditAfterCmd() *cobra.Command {
	var snippet string
	cmd := &cobra.Command{
		Use:   "after FILE SYMBOL",
		Short: "Insert a snippet immediately after a symbol",
		Long:  "Inserts the --snippet text immediately after the named SYMBOL in FILE.",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, _ []string) error {
			_ = snippet
			return fmt.Errorf("oro edit:after: not yet implemented")
		},
	}
	cmd.Flags().StringVar(&snippet, "snippet", "", "source text to insert (required)")
	_ = cmd.MarkFlagRequired("snippet")
	return cmd
}

func newEditDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete FILE SYMBOL",
		Short: "Delete a symbol from a file",
		Long:  "Removes the named SYMBOL from FILE. Use --force to skip safety checks.",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, _ []string) error {
			_ = force
			return fmt.Errorf("oro edit:delete: not yet implemented")
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip safety checks")
	return cmd
}

func newEditRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename FILE OLD NEW",
		Short: "Rename a symbol within a file",
		Long:  "Renames the symbol OLD to NEW in FILE, updating all references within the file.",
		Args:  cobra.ExactArgs(3),
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("oro edit:rename: not yet implemented")
		},
	}
}

func newEditRenameAllCmd() *cobra.Command {
	var onlyKind string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "rename-all DIR OLD NEW",
		Short: "Rename a symbol across all files in a directory",
		Long: "Renames the symbol OLD to NEW across all source files under DIR.\n" +
			"Use --only to restrict to a specific symbol kind. Use --dry-run to preview changes.",
		Args: cobra.ExactArgs(3),
		RunE: func(_ *cobra.Command, _ []string) error {
			_, _ = onlyKind, dryRun
			return fmt.Errorf("oro edit:rename-all: not yet implemented")
		},
	}
	cmd.Flags().StringVar(&onlyKind, "only", "", "restrict to a specific symbol kind (e.g. func, type, var)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without writing")
	return cmd
}

func newEditMoveCmd() *cobra.Command {
	var after string
	cmd := &cobra.Command{
		Use:   "move FILE SYMBOL",
		Short: "Move a symbol to a different position within a file",
		Long:  "Repositions the named SYMBOL in FILE to appear after the symbol named by --after.",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, _ []string) error {
			_ = after
			return fmt.Errorf("oro edit:move: not yet implemented")
		},
	}
	cmd.Flags().StringVar(&after, "after", "", "symbol to insert after (required)")
	_ = cmd.MarkFlagRequired("after")
	return cmd
}

func newEditMoveToFileCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "move-to-file SYMBOL FROM_FILE TO_FILE",
		Short: "Move a symbol from one file to another",
		Long: "Moves SYMBOL from FROM_FILE to TO_FILE, updating import paths where possible.\n" +
			"Use --dry-run to preview changes without writing.",
		Args: cobra.ExactArgs(3),
		RunE: func(_ *cobra.Command, _ []string) error {
			_ = dryRun
			return fmt.Errorf("oro edit:move-to-file: not yet implemented")
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without writing")
	return cmd
}

func newEditReadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "read FILE",
		Short: "Print the symbol map for a file",
		Long: "Parses FILE and prints its symbol map: all top-level declarations with line ranges.\n" +
			"Equivalent to 'oro outline' but scoped to the edit workflow.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("oro edit:read: not yet implemented")
		},
	}
}

func newEditDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff FILE",
		Short: "Show pending edits to a file",
		Long:  "Displays a unified diff of all pending (uncommitted) edits to FILE.",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("oro edit:diff: not yet implemented")
		},
	}
}

func newEditUndoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "undo FILE",
		Short: "Reverse the last edit to a file",
		Long:  "Reverses the most recent pending edit to FILE, restoring the prior state.",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("oro edit:undo: not yet implemented")
		},
	}
}

func newEditBatchCmd() *cobra.Command {
	var edits string
	cmd := &cobra.Command{
		Use:   "batch FILE",
		Short: "Apply multiple edits to a file atomically",
		Long: "Applies a JSON array of edit operations to FILE as a single atomic transaction.\n" +
			"All operations succeed or all are rolled back.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, _ []string) error {
			_ = edits
			return fmt.Errorf("oro edit:batch: not yet implemented")
		},
	}
	cmd.Flags().StringVar(&edits, "edits", "", "JSON array of edit operations (required)")
	_ = cmd.MarkFlagRequired("edits")
	return cmd
}

func newEditCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Reparse all edited files and surface errors",
		Long:  "Re-parses every file that has pending edits and reports any syntax or type errors.",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runEditCheck()
		},
	}
}

func runEditCheck() error {
	return fmt.Errorf("oro edit:check: not yet implemented")
}
