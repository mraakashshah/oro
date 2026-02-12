package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"oro/pkg/codesearch"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

// newIndexCmd creates the "oro index" subcommand with build and search subcommands.
func newIndexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Semantic code search index",
		Long:  "Build and search a semantic code index using vector embeddings.",
	}

	cmd.AddCommand(
		newIndexBuildCmd(),
		newIndexSearchCmd(),
	)

	return cmd
}

// newIndexBuildCmd creates the "oro index build" subcommand.
func newIndexBuildCmd() *cobra.Command {
	var rootDir string

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build the semantic code index",
		Long:  "Walk Go source files, extract code chunks, embed them, and store in a SQLite index.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rootDir == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				rootDir = cwd
			}

			dbPath := defaultIndexDBPath()
			w := cmd.OutOrStdout()
			return runIndexBuild(w, rootDir, dbPath)
		},
	}

	cmd.Flags().StringVar(&rootDir, "dir", "", "root directory to index (default: cwd)")

	return cmd
}

// newIndexSearchCmd creates the "oro index search" subcommand.
func newIndexSearchCmd() *cobra.Command {
	var topK int

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search the semantic code index",
		Long:  "Embed a natural-language query and find the most similar code chunks.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath := defaultIndexDBPath()
			w := cmd.OutOrStdout()
			return runIndexSearch(w, args[0], dbPath, topK)
		},
	}

	cmd.Flags().IntVar(&topK, "top", 10, "number of results to return")

	return cmd
}

// runIndexBuild is the core logic for building the index, separated for testability.
func runIndexBuild(w io.Writer, rootDir, dbPath string) error {
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		return fmt.Errorf("open code index: %w", err)
	}
	defer idx.Close()

	fmt.Fprintf(w, "Building index for %s ...\n", rootDir)

	ctx := context.Background()
	stats, err := idx.Build(ctx, rootDir)
	if err != nil {
		return fmt.Errorf("build index: %w", err)
	}

	fmt.Fprintf(w, "Indexed %d chunks from %d files in %s\n",
		stats.ChunksIndexed, stats.FilesProcessed, stats.Duration.Round(1e6))
	fmt.Fprintf(w, "Database: %s\n", dbPath)

	return nil
}

// runIndexSearch is the core logic for searching the index, separated for testability.
func runIndexSearch(w io.Writer, query, dbPath string, topK int) error {
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		return fmt.Errorf("open code index: %w", err)
	}
	defer idx.Close()

	ctx := context.Background()
	results, err := idx.Search(ctx, query, topK)
	if err != nil {
		return fmt.Errorf("search index: %w", err)
	}

	if len(results) == 0 {
		fmt.Fprintln(w, "No results found.")
		return nil
	}

	fmt.Fprintf(w, "Top %d results for %q:\n\n", len(results), query)
	for i, r := range results {
		fmt.Fprintf(w, "%d. %s (%s) — %s L%d-%d [score: %.4f]\n",
			i+1, r.Chunk.Name, r.Chunk.Kind, r.Chunk.FilePath,
			r.Chunk.StartLine, r.Chunk.EndLine, r.Score)
	}

	return nil
}

// defaultIndexDBPath returns the default path for the code index database.
func defaultIndexDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(protocol.OroDir, "code_index.db")
	}
	return filepath.Join(home, protocol.OroDir, "code_index.db")
}
