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

// newMemoriesCmd creates the "oro memories" parent command with subcommands.
func newMemoriesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memories",
		Short: "Browse and manage memories",
		Long:  "Commands for browsing and managing the project memory store.",
	}

	cmd.AddCommand(newMemoriesListCmd())
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
