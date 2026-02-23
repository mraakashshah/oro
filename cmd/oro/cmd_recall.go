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

// recallByID fetches and formats a single memory by ID.
func recallByID(ctx context.Context, s *memory.Store, memoryID int64, out interface{ Write([]byte) (int, error) }) error {
	mem, err := s.GetByID(ctx, memoryID)
	if err != nil {
		return fmt.Errorf("recall: %w", err)
	}
	pinnedTag := ""
	if mem.Pinned {
		pinnedTag = " [pinned]"
	}
	fmt.Fprintf(out, "[%s]%s %s\n", mem.Type, pinnedTag, mem.Content)
	fmt.Fprintf(out, "confidence: %.2f | source: %s | created: %s\n",
		mem.Confidence, mem.Source, formatCreatedAt(mem.CreatedAt))
	return nil
}

// recallByQuery searches memories by text query and formats results.
func recallByQuery(ctx context.Context, s *memory.Store, query, filePath string, out interface{ Write([]byte) (int, error) }) error {
	results, err := s.Search(ctx, query, memory.SearchOpts{Limit: 5, FilePath: filePath})
	if err != nil {
		return fmt.Errorf("recall: %w", err)
	}
	fmt.Fprint(out, formatRecallResults(results))
	return nil
}

// newRecallCmdWithStore creates the "oro recall" subcommand.
// If store is nil, the command lazily opens the default store on execution.
func newRecallCmdWithStore(store *memory.Store) *cobra.Command {
	var filePath string
	var memoryID int64
	var allProjects bool
	cmd := &cobra.Command{
		Use:   "recall <query>",
		Short: "Search memories",
		Long:  "Search the memory store by text query.\nDisplays top 5 results with type, content, confidence, score, and source.\nUse --id to fetch a single memory by ID.\nUse --all-projects to search across all projects.",
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

			// If --all-projects is set, clear project scope
			if allProjects {
				s.SetProject("")
			}

			// Check for conflicting usage
			if memoryID > 0 && len(args) > 0 {
				return fmt.Errorf("cannot use both --id and query arguments")
			}

			ctx := context.Background()
			out := cmd.OutOrStdout()

			// Fetch by ID if specified
			if memoryID > 0 {
				return recallByID(ctx, s, memoryID, out)
			}

			// Otherwise, search by query
			if len(args) == 0 {
				return fmt.Errorf("recall: query required (or use --id)")
			}

			query := strings.Join(args, " ")
			return recallByQuery(ctx, s, query, filePath, out)
		},
	}
	cmd.Flags().StringVar(&filePath, "file", "", "filter memories by file path")
	cmd.Flags().Int64Var(&memoryID, "id", 0, "fetch memory by ID")
	cmd.Flags().BoolVar(&allProjects, "all-projects", false, "search across all projects (ignore project scope)")
	return cmd
}
