package main

import (
	"context"
	"fmt"

	"oro/pkg/cards"
	"oro/pkg/memory"

	"github.com/spf13/cobra"
)

// newCheckDriftCmd creates the "oro cards check-drift" subcommand that reports
// memory entries that have no corresponding dual-write card mirror.
func newCheckDriftCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check-drift",
		Short: "Report memory entries missing a card mirror (D.3 dual-write drift)",
		Long: "Compares all memory entries against pattern cards tagged with\n" +
			"legacy_memory_dual_write. Prints one line per unmirrored entry.\n" +
			"Exit code 1 if any drift is found.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolveProjectDBPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			db, err := openStateDB(paths.StateDBPath)
			if err != nil {
				return fmt.Errorf("open state db: %w", err)
			}
			defer func() { _ = db.Close() }()

			memStore := memory.NewStore(db)
			cardStore, err := cards.NewStore(db)
			if err != nil {
				return fmt.Errorf("init card store: %w", err)
			}

			ctx := context.Background()
			failures, err := cards.CheckDrift(ctx, memStore, cardStore)
			if err != nil {
				return fmt.Errorf("check-drift: %w", err)
			}

			if len(failures) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no drift detected")
				return nil
			}

			for _, f := range failures {
				fmt.Fprintf(cmd.OutOrStdout(), "DRIFT: memory %d — %s\n", f.MemoryID, truncateLine(f.Content, 80))
			}
			return fmt.Errorf("%d unmirrored memory entry(s) detected", len(failures))
		},
	}
}

// truncateLine truncates s to maxLen characters, appending "…" if cut.
func truncateLine(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}
