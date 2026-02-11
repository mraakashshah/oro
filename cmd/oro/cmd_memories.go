package main

import (
	"context"
	"fmt"
	"strings"

	"oro/pkg/memory"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

// truncateContent truncates s to maxLen characters, appending "..." if truncated.
func truncateContent(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// formatMemoriesTable formats a slice of memories as a tabular string.
func formatMemoriesTable(memories []protocol.Memory) string {
	if len(memories) == 0 {
		return "No memories found.\n"
	}

	const maxContent = 60

	var b strings.Builder
	fmt.Fprintf(&b, "%-6s %-12s %-62s %-12s %s\n", "ID", "TYPE", "CONTENT", "CONFIDENCE", "CREATED")
	for _, m := range memories {
		content := truncateContent(strings.ReplaceAll(m.Content, "\n", " "), maxContent)
		fmt.Fprintf(&b, "%-6d %-12s %-62s %-12.2f %s\n",
			m.ID, m.Type, content, m.Confidence, formatCreatedAt(m.CreatedAt))
	}
	return b.String()
}

// newMemoriesListCmdWithStore creates the "oro memories list" subcommand wired to a memory.Store.
func newMemoriesListCmdWithStore(store *memory.Store) *cobra.Command {
	var typeFilter string
	var tagFilter string
	var limit int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List memories",
		Long:  "List memories from the store with optional filtering by type and tag.\nOutputs a table with id, type, content (truncated), confidence, and created_at.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			results, err := store.List(context.Background(), memory.ListOpts{
				Type:  typeFilter,
				Tag:   tagFilter,
				Limit: limit,
			})
			if err != nil {
				return fmt.Errorf("memories list: %w", err)
			}

			fmt.Fprint(cmd.OutOrStdout(), formatMemoriesTable(results))
			return nil
		},
	}

	cmd.Flags().StringVar(&typeFilter, "type", "", "filter by memory type (lesson|decision|gotcha|pattern|self_report)")
	cmd.Flags().StringVar(&tagFilter, "tag", "", "filter by tag")
	cmd.Flags().IntVar(&limit, "limit", 20, "maximum number of memories to return")

	return cmd
}

// newMemoriesConsolidateCmdWithStore creates the "oro memories consolidate" subcommand wired to a memory.Store.
func newMemoriesConsolidateCmdWithStore(store *memory.Store) *cobra.Command {
	var minScore float64
	var similarity float64
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "consolidate",
		Short: "Consolidate memories by deduplicating and pruning stale entries",
		Long: `Consolidate the memory store by:
  - Pruning memories with decayed scores below the threshold (default: 0.1)
  - Merging near-duplicate memories based on similarity (default: 0.8)

The decayed score is calculated as: confidence * 0.5^(age_days/30)

Use --dry-run to preview what would be changed without modifying the store.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := memory.ConsolidateOpts{
				MinDecayedScore:     minScore,
				SimilarityThreshold: similarity,
				DryRun:              dryRun,
			}

			merged, pruned, err := memory.Consolidate(context.Background(), store, opts)
			if err != nil {
				return fmt.Errorf("consolidate: %w", err)
			}

			// Count remaining memories
			remaining, err := store.List(context.Background(), memory.ListOpts{})
			if err != nil {
				return fmt.Errorf("consolidate list: %w", err)
			}

			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "DRY RUN - No changes made\n")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Consolidation complete:\n")
			fmt.Fprintf(cmd.OutOrStdout(), "  Pruned:    %d\n", pruned)
			fmt.Fprintf(cmd.OutOrStdout(), "  Merged:    %d\n", merged)
			fmt.Fprintf(cmd.OutOrStdout(), "  Remaining: %d\n", len(remaining))

			return nil
		},
	}

	cmd.Flags().Float64Var(&minScore, "min-score", 0.1, "minimum decayed score to keep (0.0-1.0)")
	cmd.Flags().Float64Var(&similarity, "similarity", 0.8, "BM25 similarity threshold for merging duplicates")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without modifying the store")

	return cmd
}

// newMemoriesConsolidateCmd creates the production "oro memories consolidate" subcommand.
func newMemoriesConsolidateCmd() *cobra.Command {
	var minScore float64
	var similarity float64
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "consolidate",
		Short: "Consolidate memories by deduplicating and pruning stale entries",
		Long: `Consolidate the memory store by:
  - Pruning memories with decayed scores below the threshold (default: 0.1)
  - Merging near-duplicate memories based on similarity (default: 0.8)

The decayed score is calculated as: confidence * 0.5^(age_days/30)

Use --dry-run to preview what would be changed without modifying the store.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := defaultMemoryStore()
			if err != nil {
				return fmt.Errorf("consolidate: %w", err)
			}

			opts := memory.ConsolidateOpts{
				MinDecayedScore:     minScore,
				SimilarityThreshold: similarity,
				DryRun:              dryRun,
			}

			merged, pruned, err := memory.Consolidate(context.Background(), store, opts)
			if err != nil {
				return fmt.Errorf("consolidate: %w", err)
			}

			// Count remaining memories
			remaining, err := store.List(context.Background(), memory.ListOpts{})
			if err != nil {
				return fmt.Errorf("consolidate list: %w", err)
			}

			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "DRY RUN - No changes made\n")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Consolidation complete:\n")
			fmt.Fprintf(cmd.OutOrStdout(), "  Pruned:    %d\n", pruned)
			fmt.Fprintf(cmd.OutOrStdout(), "  Merged:    %d\n", merged)
			fmt.Fprintf(cmd.OutOrStdout(), "  Remaining: %d\n", len(remaining))

			return nil
		},
	}

	cmd.Flags().Float64Var(&minScore, "min-score", 0.1, "minimum decayed score to keep (0.0-1.0)")
	cmd.Flags().Float64Var(&similarity, "similarity", 0.8, "BM25 similarity threshold for merging duplicates")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without modifying the store")

	return cmd
}

// newMemoriesCmd creates the "oro memories" parent command with subcommands.
func newMemoriesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memories",
		Short: "Browse and manage memories",
		Long:  "Commands for browsing and managing the project memory store.",
	}

	cmd.AddCommand(newMemoriesListCmd())
	cmd.AddCommand(newMemoriesConsolidateCmd())
	return cmd
}

// newMemoriesListCmd creates the production "oro memories list" subcommand.
func newMemoriesListCmd() *cobra.Command {
	var typeFilter string
	var tagFilter string
	var limit int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List memories",
		Long:  "List memories from the store with optional filtering by type and tag.\nOutputs a table with id, type, content (truncated), confidence, and created_at.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := defaultMemoryStore()
			if err != nil {
				return fmt.Errorf("memories list: %w", err)
			}

			results, listErr := store.List(context.Background(), memory.ListOpts{
				Type:  typeFilter,
				Tag:   tagFilter,
				Limit: limit,
			})
			if listErr != nil {
				return fmt.Errorf("memories list: %w", listErr)
			}

			fmt.Fprint(cmd.OutOrStdout(), formatMemoriesTable(results))
			return nil
		},
	}

	cmd.Flags().StringVar(&typeFilter, "type", "", "filter by memory type (lesson|decision|gotcha|pattern|self_report)")
	cmd.Flags().StringVar(&tagFilter, "tag", "", "filter by tag")
	cmd.Flags().IntVar(&limit, "limit", 20, "maximum number of memories to return")

	return cmd
}
