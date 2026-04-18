// ad_hoc/memory_eval/extract.go
// ExtractCorpus: reads memories from a SQLite state.db and writes 100 candidate
// (query, candidate_memory_id) pairs to a JSONL corpus file.
// Falls back to built-in fixture memories when the DB is inaccessible or empty.
package memoryeval

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

const targetPairs = 100

type memoryRow struct {
	id      int64
	content string
	memType string
}

// ExtractCorpus extracts exactly 100 candidate (query, memory_id) pairs to
// outputPath. Reads from dbPath (expected to be state.db); falls back to
// built-in fixture memories if dbPath is inaccessible or returns zero rows.
// The corpus is deterministic: re-running with the same DB snapshot produces
// the same 100 pairs (ORDER BY created_at DESC, id ASC in the DB query).
func ExtractCorpus(dbPath, outputPath string) error {
	source := "history"
	memories, err := loadMemoriesFromDB(dbPath)
	if err != nil || len(memories) == 0 {
		source = "fixture"
		memories = builtinFixtures()
	}

	queries := generateQueries(memories)
	pairs := buildPairs(queries, memories, source)

	if len(pairs) < targetPairs {
		return fmt.Errorf("insufficient pairs: got %d, need %d (increase fixtures or queries)", len(pairs), targetPairs)
	}
	pairs = pairs[:targetPairs]

	return writeCorpus(outputPath, source, pairs)
}

func loadMemoriesFromDB(dbPath string) ([]memoryRow, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("empty db path")
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("db not accessible: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query(`SELECT id, content, type FROM memories ORDER BY created_at DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var memories []memoryRow
	for rows.Next() {
		var m memoryRow
		if err := rows.Scan(&m.id, &m.content, &m.memType); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		memories = append(memories, m)
	}
	return memories, rows.Err()
}

// generateQueries produces a deduplicated set of query strings from memories.
// Each memory contributes up to 3 variants: full content, 8-word prefix, and
// type + 5-word prefix. This guarantees enough distinct queries to reach 100
// pairs with the built-in fixture set (12 memories × ~3 = 36 queries × 12 = 432).
func generateQueries(memories []memoryRow) []string {
	seen := make(map[string]struct{})
	var queries []string
	add := func(q string) {
		q = strings.TrimSpace(q)
		if q == "" {
			return
		}
		if _, ok := seen[q]; !ok {
			seen[q] = struct{}{}
			queries = append(queries, q)
		}
	}

	for _, m := range memories {
		add(m.content)
		add(firstWords(m.content, 8))
		add(m.memType + ": " + firstWords(m.content, 5))
	}
	return queries
}

// buildPairs cross-joins queries with memories, deduplicating by (query, id),
// and preserves insertion order for determinism.
func buildPairs(queries []string, memories []memoryRow, source string) []CorpusEntry {
	seen := make(map[string]struct{})
	var pairs []CorpusEntry

	for _, q := range queries {
		for _, m := range memories {
			key := q + "\x00" + fmt.Sprintf("%d", m.id)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			pairs = append(pairs, CorpusEntry{
				Query:             q,
				CandidateMemoryID: m.id,
				Source:            source,
			})
		}
	}
	return pairs
}

func writeCorpus(outputPath, source string, pairs []CorpusEntry) error {
	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create corpus: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := fmt.Fprintf(f, "# source: %s\n", source); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	for _, e := range pairs {
		b, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("marshal entry: %w", err)
		}
		if _, err := fmt.Fprintf(f, "%s\n", b); err != nil {
			return fmt.Errorf("write entry: %w", err)
		}
	}
	return nil
}

// firstWords returns the first n whitespace-separated words of s joined by spaces.
// Returns s unchanged when s has n or fewer words.
func firstWords(s string, n int) string {
	words := strings.Fields(s)
	if len(words) <= n {
		return s
	}
	return strings.Join(words[:n], " ")
}

// builtinFixtures returns hardcoded memories derived from pkg/memory test patterns.
// Used as the fallback when state.db is inaccessible or empty.
// IDs start at 1 (positive, as required by the corpus spec).
func builtinFixtures() []memoryRow {
	return []memoryRow{
		{1, "ruff --fix must run before pyright in CI/CD pipelines for Python linting", "gotcha"},
		{2, "SQLite WAL mode requires single-writer for consistency under concurrent access", "lesson"},
		{3, "always use table-driven tests in Go for comprehensive test coverage", "decision"},
		{4, "git worktree branches must be rebased onto main before fast-forward merge", "pattern"},
		{5, "pre-commit hooks run golangci-lint and go test before every commit", "pattern"},
		{6, "context cancellation in goroutines must use select on ctx.Done channel", "gotcha"},
		{7, "write failing test before any production code — TDD workflow has no exceptions", "lesson"},
		{8, "FTS5 search uses rank ordering for relevance with scores computed in Go side", "pattern"},
		{9, "embedding vectors stored as binary blobs in SQLite memories table columns", "pattern"},
		{10, "worker prompt assembled from 12 sections including bead description and memory", "pattern"},
		{11, "dispatcher uses CAS owner-lock to prevent concurrent backfill conflicts safely", "gotcha"},
		{12, "memory consolidation prunes stale entries with low decayed confidence scores", "lesson"},
	}
}
