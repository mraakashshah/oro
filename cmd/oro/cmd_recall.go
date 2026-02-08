package main

import (
	"context"
	"fmt"
	"strings"

	"oro/pkg/memory"

	"github.com/spf13/cobra"
)

// formatRecallResults formats search results for CLI output.
func formatRecallResults(results []memory.ScoredMemory) string {
	if len(results) == 0 {
		return "No memories found.\n"
	}

	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, r.Type, r.Content)
		fmt.Fprintf(&b, "   confidence: %.2f | score: %.4f | source: %s | created: %s\n",
			r.Confidence, r.Score, r.Source, formatCreatedAt(r.CreatedAt))
	}
	return b.String()
}

// formatCreatedAt returns the date portion of a datetime string.
func formatCreatedAt(createdAt string) string {
	if len(createdAt) >= 10 {
		return createdAt[:10]
	}
	return createdAt
}

// newRecallCmdWithStore creates the "oro recall" subcommand wired to a memory.Store.
func newRecallCmdWithStore(store *memory.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "recall <query>",
		Short: "Search memories",
		Long:  "Search the memory store by text query.\nDisplays top 5 results with type, content, confidence, score, and source.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.Join(args, " ")

			results, err := store.Search(context.Background(), query, memory.SearchOpts{Limit: 5})
			if err != nil {
				return fmt.Errorf("recall: %w", err)
			}

			fmt.Fprint(cmd.OutOrStdout(), formatRecallResults(results))
			return nil
		},
	}
}

// newRecallCmd creates the "oro recall" subcommand.
// In production, it creates a store from the default DB path.
func newRecallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recall <query>",
		Short: "Search memories",
		Long:  "Search the memory store by text query.\nDisplays top 5 results with type, content, confidence, score, and source.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := defaultMemoryStore()
			if err != nil {
				return fmt.Errorf("recall: %w", err)
			}
			query := strings.Join(args, " ")

			results, searchErr := store.Search(context.Background(), query, memory.SearchOpts{Limit: 5})
			if searchErr != nil {
				return fmt.Errorf("recall: %w", searchErr)
			}

			fmt.Fprint(cmd.OutOrStdout(), formatRecallResults(results))
			return nil
		},
	}
}
