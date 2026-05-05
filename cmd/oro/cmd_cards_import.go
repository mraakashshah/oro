package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"oro/pkg/cards"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// memFrontmatter holds the YAML header of a Claude auto-memory markdown file.
type memFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Type        string `yaml:"type"`
}

// parsedMemFile is one memory markdown file ready for import.
type parsedMemFile struct {
	Filename    string
	Frontmatter memFrontmatter
	Body        string
}

// memContentHash returns a stable SHA-256 hash of the body for idempotency.
func memContentHash(body string) string {
	h := sha256.Sum256([]byte(body))
	return fmt.Sprintf("mem:%x", h)
}

// classifyMemCard maps a memory file to a card type using the filename prefix
// heuristic from §5.8 D.2, falling back to the frontmatter type field.
func classifyMemCard(filename, memType string) cards.CardType {
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(filename), ".md"))
	switch {
	case strings.HasPrefix(base, "feedback_"):
		return cards.CardTypeRule
	case strings.HasPrefix(base, "fix_"):
		return cards.CardTypePattern
	case strings.HasPrefix(base, "decision_"):
		return cards.CardTypeDecision
	}
	switch memType {
	case "feedback":
		return cards.CardTypeRule
	case "project":
		return cards.CardTypeDecision
	default:
		return cards.CardTypePattern
	}
}

// parseMemoryDir reads and parses all .md files in dir, skipping MEMORY.md.
func parseMemoryDir(dir string) ([]parsedMemFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read memory dir %s: %w", dir, err)
	}

	var results []parsedMemFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if strings.EqualFold(e.Name(), "MEMORY.md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path) //nolint:gosec // path is dir + os.DirEntry.Name(), not user input
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		fm, body, err := parseMemFrontmatter(string(data))
		if err != nil {
			return nil, fmt.Errorf("parse frontmatter %s: %w", e.Name(), err)
		}
		results = append(results, parsedMemFile{
			Filename:    e.Name(),
			Frontmatter: fm,
			Body:        body,
		})
	}
	return results, nil
}

// parseMemFrontmatter extracts YAML frontmatter and the body from markdown content.
// Returns empty frontmatter and the original content if no valid delimiter is found.
func parseMemFrontmatter(content string) (memFrontmatter, string, error) {
	if !strings.HasPrefix(content, "---\n") {
		return memFrontmatter{}, content, nil
	}
	rest := content[4:] // skip opening "---\n"
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return memFrontmatter{}, content, nil
	}
	yamlPart := rest[:end]
	body := strings.TrimPrefix(rest[end+4:], "\n") // skip "\n---" then optional leading newline
	var fm memFrontmatter
	if err := yaml.Unmarshal([]byte(yamlPart), &fm); err != nil {
		return memFrontmatter{}, body, fmt.Errorf("unmarshal yaml: %w", err)
	}
	return fm, body, nil
}

// existingMemoryHashes loads the emerged_from values of all non-retired cards
// so the importer can skip duplicates in O(1) per entry.
func existingMemoryHashes(ctx context.Context, store cards.Store) (map[string]bool, error) {
	all, err := store.List(ctx, cards.ListQuery{})
	if err != nil {
		return nil, fmt.Errorf("list cards: %w", err)
	}
	hashes := make(map[string]bool, len(all))
	for _, c := range all {
		if c.EmergedFrom != nil {
			hashes[*c.EmergedFrom] = true
		}
	}
	return hashes, nil
}

// buildMemCardParams constructs CardCreateParams from a parsed memory file.
func buildMemCardParams(pf parsedMemFile, hash string) cards.CardCreateParams {
	title := pf.Frontmatter.Name
	if title == "" {
		title = firstNonEmptyLine(pf.Body)
	}
	summary := pf.Frontmatter.Description
	if summary == "" {
		summary = memTruncate(strings.TrimSpace(pf.Body), 200)
	}
	return cards.CardCreateParams{
		Type:        classifyMemCard(pf.Filename, pf.Frontmatter.Type),
		Title:       title,
		BodySummary: summary,
		BodyFull:    pf.Body,
		Tags:        []string{"legacy_memory"},
		EmergedFrom: &hash, // hash param escapes to heap via pointer
	}
}

// firstNonEmptyLine returns the first non-blank line of s.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// memTruncate returns s capped at maxLen characters, appending "…" if cut.
func memTruncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

// importFromMemoryDir reads Claude auto-memory markdown files from dir and
// creates cards in store. If dryRun is true, no cards are written.
// Returns the number of cards created (or that would be created in dry-run).
// Already-imported entries are detected via content hash stored in emerged_from.
func importFromMemoryDir(ctx context.Context, store cards.Store, dir string, dryRun bool) (int, error) {
	files, err := parseMemoryDir(dir)
	if err != nil {
		return 0, fmt.Errorf("parse memory dir: %w", err)
	}
	imported, err := existingMemoryHashes(ctx, store)
	if err != nil {
		return 0, fmt.Errorf("load existing hashes: %w", err)
	}
	count := 0
	for _, pf := range files {
		hash := memContentHash(pf.Body)
		if imported[hash] {
			continue
		}
		if dryRun {
			count++
			continue
		}
		if _, err := store.Create(ctx, buildMemCardParams(pf, hash)); err != nil {
			return count, fmt.Errorf("create card from %s: %w", pf.Filename, err)
		}
		count++
	}
	return count, nil
}

// newImportFromMemoryCmd creates the production "import-from-memory" subcommand
// that opens the card store from the project's state database.
func newImportFromMemoryCmd() *cobra.Command {
	var dryRun bool
	var memoryDir string

	cmd := &cobra.Command{
		Use:   "import-from-memory",
		Short: "Import Claude auto-memory files into the card store",
		Long: "Reads .md files from --memory-dir, classifies them by filename prefix\n" +
			"(feedback_* → rule, fix_* → pattern, decision_* → decision, else → pattern),\n" +
			"and creates cards tagged with legacy_memory.\n" +
			"Idempotent: re-running skips cards already imported by content hash.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if memoryDir == "" {
				return fmt.Errorf("--memory-dir is required")
			}
			paths, err := ResolveProjectDBPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			db, err := openStateDB(paths.StateDBPath)
			if err != nil {
				return fmt.Errorf("open state db: %w", err)
			}
			defer func() { _ = db.Close() }()
			store, err := cards.NewStore(db)
			if err != nil {
				return fmt.Errorf("init card store: %w", err)
			}
			n, err := importFromMemoryDir(cmd.Context(), store, memoryDir, dryRun)
			if err != nil {
				return fmt.Errorf("import-from-memory: %w", err)
			}
			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "dry-run: would import %d card(s)\n", n)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "imported %d card(s)\n", n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing")
	cmd.Flags().StringVar(&memoryDir, "memory-dir", "", "path to Claude auto-memory directory")
	return cmd
}
