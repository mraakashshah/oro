package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"oro/pkg/codesearch"
	embeddings "oro/pkg/embed"

	"github.com/spf13/cobra"
)

// newIndexCmd creates the "oro index" subcommand with build and search subcommands.
func newIndexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Semantic code search index",
		Long:  "Build and search a code index using FTS5 full-text search with Claude reranking.",
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
		Long:  "Walk Go source files, extract code chunks, and store in a SQLite FTS5 index.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rootDir == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				rootDir = cwd
			}

			paths, err := ResolveProjectDBPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			w := cmd.OutOrStdout()
			return runIndexBuild(w, rootDir, paths.CodeIndexDBPath)
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
		Long:  "Search the code index using FTS5 full-text search with Claude reranking.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := ResolveProjectDBPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			w := cmd.OutOrStdout()
			return runIndexSearch(w, args[0], paths.CodeIndexDBPath, topK, &codesearch.ClaudeRerankSpawner{})
		},
	}

	cmd.Flags().IntVar(&topK, "top", 10, "number of results to return")

	return cmd
}

// runIndexBuild is the core logic for building the index, separated for testability.
func runIndexBuild(w io.Writer, rootDir, dbPath string) error {
	idx, err := codesearch.NewCodeIndex(dbPath)
	if err != nil {
		return fmt.Errorf("open code index: %w", err)
	}
	defer idx.Close()
	configureCodeIndexEmbedder(idx)

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
// If spawner is nil, search uses FTS5-only (no reranking).
func runIndexSearch(w io.Writer, query, dbPath string, topK int, spawner codesearch.RerankSpawner) error {
	idx, err := codesearch.NewCodeIndex(dbPath)
	if err != nil {
		return fmt.Errorf("open code index: %w", err)
	}
	defer idx.Close()
	configureCodeIndexEmbedder(idx)
	if spawner != nil {
		idx.SetReranker(codesearch.NewReranker(spawner))
	}

	ctx := context.Background()
	var results []codesearch.Result
	if codesearch.RouteGrep(query).Route == codesearch.RouteSemantic {
		results, err = idx.SearchSemantic(ctx, query)
	} else {
		results, err = idx.Search(ctx, query, topK)
	}
	if err != nil {
		return fmt.Errorf("search index: %w", err)
	}
	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}

	if len(results) == 0 {
		fmt.Fprintln(w, "No results found.")
		return nil
	}

	fmt.Fprintf(w, "Top %d results for %q:\n\n", len(results), query)
	for i, r := range results {
		line := fmt.Sprintf("%d. %s (%s) — %s L%d-%d [score: %.4f]",
			i+1, r.Chunk.Name, r.Chunk.Kind, r.Chunk.FilePath,
			r.Chunk.StartLine, r.Chunk.EndLine, r.Score)
		if r.Reason != "" {
			line += " — " + r.Reason
		}
		fmt.Fprintln(w, line)
	}

	return nil
}

func configureCodeIndexEmbedder(idx *codesearch.CodeIndex) {
	embedder, err := embeddings.NewEmbedder("")
	if err != nil {
		return
	}
	idx.SetEmbedder(embedder)
}
