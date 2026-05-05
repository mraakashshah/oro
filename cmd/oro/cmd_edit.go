package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newEditCmd creates the "oro edit" command with all 12 AST-aware edit subcommands.
func newEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "AST-aware file editing operations",
		Long:  "Deterministic AST-aware editing for Go, Python, TypeScript, and JavaScript source files.\nWorkers invoke subcommands as 'oro edit:<op> ...' in their Bash tool.",
	}

	cmd.AddCommand(
		newEditReplaceCmd(),
		newEditAfterCmd(),
		newEditDeleteCmd(),
		newEditRenameCmd(),
		newEditRenameAllCmd(),
		newEditMoveCmd(),
		newEditMoveToFileCmd(),
		newEditReadCmd(),
		newEditDiffCmd(),
		newEditUndoCmd(),
		newEditBatchCmd(),
		newEditCheckCmd(),
	)

	return cmd
}

func newEditReplaceCmd() *cobra.Command {
	var snippet string
	cmd := &cobra.Command{
		Use:   "replace FILE SYMBOL",
		Short: "Replace a symbol's body using anchor-splice",
		Long:  "Replaces the body of SYMBOL in FILE using anchor-splice. Returns EFALLTHROUGH when the snippet is ineligible; use the Edit tool with a full block in that case.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEditReplace(args[0], args[1], snippet)
		},
	}
	cmd.Flags().StringVar(&snippet, "snippet", "", "replacement snippet (required)")
	_ = cmd.MarkFlagRequired("snippet")
	return cmd
}

func newEditAfterCmd() *cobra.Command {
	var snippet string
	cmd := &cobra.Command{
		Use:   "after FILE SYMBOL",
		Short: "Insert a snippet after a symbol",
		Long:  "Inserts the given snippet immediately after SYMBOL in FILE.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEditAfter(args[0], args[1], snippet)
		},
	}
	cmd.Flags().StringVar(&snippet, "snippet", "", "text to insert (required)")
	_ = cmd.MarkFlagRequired("snippet")
	return cmd
}

func newEditDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete FILE SYMBOL",
		Short: "Delete a symbol from a file",
		Long:  "Removes SYMBOL from FILE. Use --force to skip confirmation when the symbol has callers.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEditDelete(args[0], args[1], force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip caller-presence check")
	return cmd
}

func newEditRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename FILE OLD NEW",
		Short: "Rename a symbol within a file",
		Long:  "Renames symbol OLD to NEW in FILE, updating all intra-file references.",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEditRename(args[0], args[1], args[2])
		},
	}
}

func newEditRenameAllCmd() *cobra.Command {
	var onlyKind string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "rename-all DIR OLD NEW",
		Short: "Rename a symbol across all files in DIR",
		Long:  "Renames all occurrences of OLD to NEW across every supported source file under DIR.",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEditRenameAll(args[0], args[1], args[2], onlyKind, dryRun)
		},
	}
	cmd.Flags().StringVar(&onlyKind, "only", "", "restrict to symbol kind (func, type, var, …)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print changes without writing")
	return cmd
}

func newEditMoveCmd() *cobra.Command {
	var after string
	cmd := &cobra.Command{
		Use:   "move FILE SYMBOL",
		Short: "Move a symbol to a different position within the same file",
		Long:  "Repositions SYMBOL in FILE immediately after OTHER_SYMBOL.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEditMove(args[0], args[1], after)
		},
	}
	cmd.Flags().StringVar(&after, "after", "", "place SYMBOL after this symbol (required)")
	_ = cmd.MarkFlagRequired("after")
	return cmd
}

func newEditMoveToFileCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "move-to-file SYMBOL FROM_FILE TO_FILE",
		Short: "Move a symbol from one file to another",
		Long:  "Cuts SYMBOL from FROM_FILE and appends it to TO_FILE, updating package references.",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEditMoveToFile(args[0], args[1], args[2], dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print changes without writing")
	return cmd
}

func newEditReadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "read FILE",
		Short: "Print the symbol map for a file",
		Long:  "Parses FILE and prints all declared symbols with kind, visibility, and line range (same output as 'oro outline').",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runEditRead(args[0])
		},
	}
}

func newEditDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff FILE",
		Short: "Show pending edits for a file",
		Long:  "Displays the diff of any pending (uncommitted) edits to FILE.",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runEditDiff(args[0])
		},
	}
}

func newEditUndoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "undo FILE",
		Short: "Reverse the last edit to a file",
		Long:  "Reverts the most recent edit operation applied to FILE.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEditUndo(args[0])
		},
	}
}

func newEditBatchCmd() *cobra.Command {
	var edits string
	cmd := &cobra.Command{
		Use:   "batch FILE",
		Short: "Apply multiple edits to a file atomically",
		Long:  "Applies a JSON array of edit operations to FILE in a single atomic transaction.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEditBatch(args[0], edits)
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

// stub run functions — full implementation follows in Phase C.3+.

func runEditReplace(_, _, _ string) error {
	return errNotImplemented("edit:replace")
}

func runEditAfter(_, _, _ string) error {
	return errNotImplemented("edit:after")
}

func runEditDelete(_, _ string, _ bool) error {
	return errNotImplemented("edit:delete")
}

func runEditRename(_, _, _ string) error {
	return errNotImplemented("edit:rename")
}

func runEditRenameAll(_, _, _, _ string, _ bool) error {
	return errNotImplemented("edit:rename-all")
}

func runEditMove(_, _, _ string) error {
	return errNotImplemented("edit:move")
}

func runEditMoveToFile(_, _, _ string, _ bool) error {
	return errNotImplemented("edit:move-to-file")
}

func runEditRead(_ string) error {
	return errNotImplemented("edit:read")
}

func runEditDiff(_ string) error {
	return errNotImplemented("edit:diff")
}

func runEditUndo(_ string) error {
	return errNotImplemented("edit:undo")
}

func runEditBatch(_, _ string) error {
	return errNotImplemented("edit:batch")
}

func runEditCheck() error {
	return errNotImplemented("edit:check")
}

func errNotImplemented(op string) error {
	return fmt.Errorf("%s: not yet implemented", op)
}
