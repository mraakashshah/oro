package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"oro/pkg/memory"

	"github.com/spf13/cobra"
)

// newIngestCmdWithStore creates the "oro ingest" subcommand wired to a memory.Store.
func newIngestCmdWithStore(store *memory.Store) *cobra.Command {
	var (
		filePath string
		dryRun   bool
	)

	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Import knowledge from JSONL file into memory store",
		Long: `Import knowledge entries from a JSONL file into the memory store.

The command looks for the knowledge file in this order:
1. --file flag
2. ORO_KNOWLEDGE_FILE environment variable
3. Project config discovery (.oro/knowledge.jsonl)

Use --dry-run to preview what would be ingested without writing to the database.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			// Resolve knowledge file path
			resolvedPath, err := resolveKnowledgeFile(filePath)
			if err != nil {
				// Best-effort: missing file is not an error
				fmt.Fprintf(cmd.OutOrStdout(), "knowledge file not found, skipping ingest\n")
				return nil
			}

			// Open file
			f, err := os.Open(resolvedPath) //nolint:gosec // path from trusted sources
			if err != nil {
				// Best-effort: file open error is not fatal
				fmt.Fprintf(cmd.OutOrStdout(), "could not open knowledge file: %v, skipping ingest\n", err)
				return nil
			}
			defer f.Close()

			// Dry-run mode: read and count entries without writing
			if dryRun {
				count, err := countKnowledgeEntries(f)
				if err != nil {
					return fmt.Errorf("dry-run count: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "dry-run: would ingest %d entries from %s\n", count, resolvedPath)
				return nil
			}

			// Ingest knowledge
			count, err := memory.IngestKnowledge(ctx, store, f)
			if err != nil {
				return fmt.Errorf("ingest knowledge: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "ingested %d entries from %s\n", count, resolvedPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&filePath, "file", "", "path to knowledge.jsonl file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be ingested without writing")

	return cmd
}

// newIngestCmd creates the "oro ingest" subcommand.
// Opens the default memory store from ~/.oro/memories.db.
func newIngestCmd() *cobra.Command {
	var (
		filePath string
		dryRun   bool
	)

	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Import knowledge from JSONL file into memory store",
		Long: `Import knowledge entries from a JSONL file into the memory store.

The command looks for the knowledge file in this order:
1. --file flag
2. ORO_KNOWLEDGE_FILE environment variable
3. Project config discovery (.oro/knowledge.jsonl)

Use --dry-run to preview what would be ingested without writing to the database.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			// Open default memory store
			store, err := defaultMemoryStore()
			if err != nil {
				return fmt.Errorf("open memory store: %w", err)
			}

			// Resolve knowledge file path
			resolvedPath, err := resolveKnowledgeFile(filePath)
			if err != nil {
				// Best-effort: missing file is not an error
				fmt.Fprintf(cmd.OutOrStdout(), "knowledge file not found, skipping ingest\n")
				return nil
			}

			// Open file
			f, err := os.Open(resolvedPath) //nolint:gosec // path from trusted sources
			if err != nil {
				// Best-effort: file open error is not fatal
				fmt.Fprintf(cmd.OutOrStdout(), "could not open knowledge file: %v, skipping ingest\n", err)
				return nil
			}
			defer f.Close()

			// Dry-run mode: read and count entries without writing
			if dryRun {
				count, err := countKnowledgeEntries(f)
				if err != nil {
					return fmt.Errorf("dry-run count: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "dry-run: would ingest %d entries from %s\n", count, resolvedPath)
				return nil
			}

			// Ingest knowledge
			count, err := memory.IngestKnowledge(ctx, store, f)
			if err != nil {
				return fmt.Errorf("ingest knowledge: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "ingested %d entries from %s\n", count, resolvedPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&filePath, "file", "", "path to knowledge.jsonl file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be ingested without writing")

	return cmd
}

// resolveKnowledgeFile determines the knowledge file path from:
// 1. Explicit filePath argument (if non-empty)
// 2. ORO_KNOWLEDGE_FILE environment variable
// 3. Project config discovery (.oro/knowledge.jsonl)
// Returns an error if no file is found.
func resolveKnowledgeFile(filePath string) (string, error) {
	// Priority 1: --file flag
	if filePath != "" {
		if _, err := os.Stat(filePath); err == nil {
			return filePath, nil
		}
		return "", fmt.Errorf("file not found: %s", filePath)
	}

	// Priority 2: ORO_KNOWLEDGE_FILE env var
	if envPath := os.Getenv("ORO_KNOWLEDGE_FILE"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath, nil
		}
		return "", fmt.Errorf("ORO_KNOWLEDGE_FILE not found: %s", envPath)
	}

	// Priority 3: Project config discovery (.oro/knowledge.jsonl)
	projectPath := filepath.Join(".oro", "knowledge.jsonl")
	if _, err := os.Stat(projectPath); err == nil {
		return projectPath, nil
	}

	return "", fmt.Errorf("no knowledge file found (checked --file, ORO_KNOWLEDGE_FILE, .oro/knowledge.jsonl)")
}

// countKnowledgeEntries counts valid JSONL entries in the file without inserting.
// Used for dry-run mode.
func countKnowledgeEntries(r io.Reader) (int, error) {
	count := 0
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Simple validation: check if it's valid JSON with required fields
		if isValidKnowledgeEntry(line) {
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan file: %w", err)
	}
	return count, nil
}

// isValidKnowledgeEntry checks if a JSONL line has required fields.
func isValidKnowledgeEntry(line string) bool {
	var entry struct {
		Key     string `json:"key"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return false
	}
	return entry.Key != "" && entry.Content != ""
}
