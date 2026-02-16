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
		pinnedTag := ""
		if r.Pinned {
			pinnedTag = " [pinned]"
		}
		fmt.Fprintf(&b, "%d. [%s]%s %s\n", i+1, r.Type, pinnedTag, r.Content)
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

// newRecallCmdWithStore creates the "oro recall" subcommand.
// If store is nil, the command lazily opens the default store on execution.
func newRecallCmdWithStore(store *memory.Store) *cobra.Command {
	var filePath string
	var memoryID int64
	cmd := &cobra.Command{
		Use:   "recall <query>",
		Short: "Search memories",
		Long:  "Search the memory store by text query.\nDisplays top 5 results with type, content, confidence, score, and source.\nUse --id to fetch a single memory by ID.",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Lazy store initialization if not provided
			s := store
			if s == nil {
				var err error
				s, err = defaultMemoryStore()
				if err != nil {
					return fmt.Errorf("recall: %w", err)
				}
			}

			// Check for conflicting usage
			if memoryID > 0 && len(args) > 0 {
				return fmt.Errorf("cannot use both --id and query arguments")
			}

			// Fetch by ID if specified
			if memoryID > 0 {
				mem, err := s.GetByID(context.Background(), memoryID)
				if err != nil {
					return fmt.Errorf("recall: %w", err)
				}
				pinnedTag := ""
				if mem.Pinned {
					pinnedTag = " [pinned]"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "[%s]%s %s\n", mem.Type, pinnedTag, mem.Content)
				fmt.Fprintf(cmd.OutOrStdout(), "confidence: %.2f | source: %s | created: %s\n",
					mem.Confidence, mem.Source, formatCreatedAt(mem.CreatedAt))
				return nil
			}

			// Otherwise, search by query
			if len(args) == 0 {
				return fmt.Errorf("recall: query required (or use --id)")
			}

			query := strings.Join(args, " ")
			results, err := s.Search(context.Background(), query, memory.SearchOpts{Limit: 5, FilePath: filePath})
			if err != nil {
				return fmt.Errorf("recall: %w", err)
			}

			fmt.Fprint(cmd.OutOrStdout(), formatRecallResults(results))
			return nil
		},
	}
	cmd.Flags().StringVar(&filePath, "file", "", "filter memories by file path")
	cmd.Flags().Int64Var(&memoryID, "id", 0, "fetch memory by ID")
	return cmd
}
