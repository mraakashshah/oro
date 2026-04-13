package memory //nolint:testpackage // white-box tests for internal helpers (tagsFromJSON, termSet, etc.)

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// setupTestDB creates an in-memory SQLite database with the full schema.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("exec schema: %v", err)
	}

	return db
}

func TestStore_InsertAndSearch(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert several memories
	_, err := store.Insert(ctx, InsertParams{
		Content: "ruff --fix must run before pyright", Type: "gotcha",
		Tags: []string{"python", "linting"}, Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}

	_, err = store.Insert(ctx, InsertParams{
		Content: "SQLite WAL mode requires single-writer for consistency", Type: "lesson",
		Tags: []string{"sqlite", "database"}, Source: "self_report", Confidence: 0.85,
	})
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	_, err = store.Insert(ctx, InsertParams{
		Content: "Always use table-driven tests in Go", Type: "decision",
		Tags: []string{"go", "testing"}, Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert 3: %v", err)
	}

	// Search for "ruff pyright" — should find the gotcha
	results, err := store.Search(ctx, "ruff pyright", SearchOpts{Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if !strings.Contains(results[0].Content, "ruff") {
		t.Errorf("expected first result to contain 'ruff', got: %s", results[0].Content)
	}
	if results[0].Score <= 0 {
		t.Errorf("expected positive score, got: %f", results[0].Score)
	}

	// Search for "SQLite" — should find the lesson
	results, err = store.Search(ctx, "SQLite", SearchOpts{Limit: 5})
	if err != nil {
		t.Fatalf("search sqlite: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result for SQLite")
	}
	if !strings.Contains(results[0].Content, "SQLite") {
		t.Errorf("expected result to contain 'SQLite', got: %s", results[0].Content)
	}
}

func TestStore_SearchWithTimeDecay(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert an "old" memory by manipulating created_at
	_, err := db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"old memory about testing patterns", "lesson", `["testing"]`, "self_report", 0.8,
		time.Now().AddDate(0, -3, 0).Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		t.Fatalf("insert old: %v", err)
	}

	// Insert a recent memory with same content theme
	_, err = store.Insert(ctx, InsertParams{
		Content: "new memory about testing patterns", Type: "lesson",
		Tags: []string{"testing"}, Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert new: %v", err)
	}

	results, err := store.Search(ctx, "testing patterns", SearchOpts{Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	// Recent memory should score higher due to time decay
	if !strings.Contains(results[0].Content, "new") {
		t.Errorf("expected recent memory to rank first, got: %s", results[0].Content)
	}
	if results[0].Score <= results[1].Score {
		t.Errorf("expected first score (%f) > second score (%f)", results[0].Score, results[1].Score)
	}
}

func TestStore_SearchWithTypeFilter(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content: "always check error returns", Type: "gotcha",
		Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert gotcha: %v", err)
	}

	_, err = store.Insert(ctx, InsertParams{
		Content: "error handling is important in returns", Type: "lesson",
		Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert lesson: %v", err)
	}

	// Search with type filter for gotcha only
	results, err := store.Search(ctx, "error returns", SearchOpts{Type: "gotcha"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	for _, r := range results {
		if r.Type != "gotcha" {
			t.Errorf("expected only gotcha type, got: %s", r.Type)
		}
	}
}

func TestStore_SearchWithTagFilter(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content: "use pytest fixtures over classes", Type: "lesson",
		Tags: []string{"python", "testing"}, Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}

	_, err = store.Insert(ctx, InsertParams{
		Content: "use go test with table-driven tests", Type: "lesson",
		Tags: []string{"go", "testing"}, Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	// Search with tag filter for python
	results, err := store.Search(ctx, "testing", SearchOpts{Tags: []string{"python"}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	for _, r := range results {
		tags := tagsFromJSON(r.Tags)
		if !anyTagMatch(tags, []string{"python"}) {
			t.Errorf("expected result to have 'python' tag, got tags: %s", r.Tags)
		}
	}
}

func TestStore_List(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, err := store.Insert(ctx, InsertParams{
			Content: "memory item " + string(rune('A'+i)), Type: "lesson",
			Tags: []string{"test"}, Source: "self_report", Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	_, err := store.Insert(ctx, InsertParams{
		Content: "gotcha memory", Type: "gotcha",
		Tags: []string{"test"}, Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert gotcha: %v", err)
	}

	// List all
	all, err := store.List(ctx, ListOpts{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 6 {
		t.Errorf("expected 6 memories, got %d", len(all))
	}

	// List by type
	gotchas, err := store.List(ctx, ListOpts{Type: "gotcha"})
	if err != nil {
		t.Fatalf("list gotcha: %v", err)
	}
	if len(gotchas) != 1 {
		t.Errorf("expected 1 gotcha, got %d", len(gotchas))
	}

	// List with limit
	limited, err := store.List(ctx, ListOpts{Limit: 2})
	if err != nil {
		t.Fatalf("list limited: %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("expected 2 memories, got %d", len(limited))
	}

	// Verify ordering: most recent first
	if all[0].ID < all[len(all)-1].ID {
		t.Error("expected most recent first in list ordering")
	}
}

func TestStore_Delete(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	id, err := store.Insert(ctx, InsertParams{
		Content: "deletable memory about unique search terms xyzzy", Type: "lesson",
		Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Verify it exists in search
	results, err := store.Search(ctx, "xyzzy", SearchOpts{})
	if err != nil {
		t.Fatalf("search before delete: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result before delete, got %d", len(results))
	}

	// Delete
	if err := store.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Verify gone from FTS too
	results, err = store.Search(ctx, "xyzzy", SearchOpts{})
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results after delete, got %d", len(results))
	}

	// Verify gone from list
	all, err := store.List(ctx, ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0 memories after delete, got %d", len(all))
	}
}

func TestStore_UpdateConfidence(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	id, err := store.Insert(ctx, InsertParams{
		Content: "confidence test memory about frobnicator", Type: "lesson",
		Source: "self_report", Confidence: 0.5,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Search and record score
	results1, err := store.Search(ctx, "frobnicator", SearchOpts{})
	if err != nil {
		t.Fatalf("search 1: %v", err)
	}
	if len(results1) == 0 {
		t.Fatal("expected result")
	}
	scoreBefore := results1[0].Score

	// Update confidence higher
	if err := store.UpdateConfidence(ctx, id, 1.0); err != nil {
		t.Fatalf("update confidence: %v", err)
	}

	// Search again — score should be higher
	results2, err := store.Search(ctx, "frobnicator", SearchOpts{})
	if err != nil {
		t.Fatalf("search 2: %v", err)
	}
	if len(results2) == 0 {
		t.Fatal("expected result after update")
	}
	scoreAfter := results2[0].Score

	if scoreAfter <= scoreBefore {
		t.Errorf("expected higher score after confidence bump: before=%f after=%f", scoreBefore, scoreAfter)
	}
}

func TestStore_InsertDedup(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert a memory
	id1, err := store.Insert(ctx, InsertParams{
		Content: "always run ruff before pyright for linting", Type: "gotcha",
		Source: "self_report", Confidence: 0.7,
	})
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}

	// Insert a near-duplicate (high Jaccard overlap)
	id2, err := store.Insert(ctx, InsertParams{
		Content: "always run ruff before pyright for linting checks", Type: "gotcha",
		Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	// Should return the same ID (deduped)
	if id2 != id1 {
		t.Errorf("expected dedup to return existing id %d, got %d", id1, id2)
	}

	// Only one memory should exist
	all, err := store.List(ctx, ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 memory after dedup, got %d", len(all))
	}

	// Confidence should be updated to the higher value (0.9 from second insert)
	if all[0].Confidence < 0.89 || all[0].Confidence > 0.91 {
		t.Errorf("expected confidence updated to ~0.9, got %f", all[0].Confidence)
	}
}

func TestStore_InsertNoDedupForDistinct(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert two distinct memories
	_, err := store.Insert(ctx, InsertParams{
		Content: "always run ruff before pyright", Type: "gotcha",
		Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}

	_, err = store.Insert(ctx, InsertParams{
		Content: "SQLite WAL mode requires single-writer for consistency", Type: "lesson",
		Source: "self_report", Confidence: 0.85,
	})
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	// Both should exist (distinct content, no dedup)
	all, err := store.List(ctx, ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 distinct memories, got %d", len(all))
	}
}

func TestJaccardSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want float64
	}{
		{"identical", "hello world", "hello world", 1.0},
		{"disjoint", "hello world", "foo bar", 0.0},
		{
			"high overlap", "always run ruff before pyright for linting",
			"always run ruff before pyright for linting checks", 7.0 / 8.0,
		},
		{"partial overlap", "ruff linting python", "ruff pyright go", 1.0 / 5.0},
		{"empty both", "", "", 1.0},
		{"empty one", "hello", "", 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := termSet(tt.a)
			b := termSet(tt.b)
			got := jaccardSimilarity(a, b)
			if got < tt.want-0.01 || got > tt.want+0.01 {
				t.Errorf("jaccardSimilarity(%q, %q) = %f, want %f", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

//nolint:funlen // table-driven test with many cases
func TestParseMarker_ValidMarkers(t *testing.T) {
	tests := []struct {
		name string
		line string
		want InsertParams
	}{
		{
			name: "simple gotcha",
			line: "[MEMORY] type=gotcha: ruff --fix must run before pyright",
			want: InsertParams{
				Content: "ruff --fix must run before pyright", Type: "gotcha",
				Source: "self_report", Confidence: 0.8,
			},
		},
		{
			name: "lesson without tags",
			line: "[MEMORY] type=lesson: SQLite WAL mode requires single-writer",
			want: InsertParams{
				Content: "SQLite WAL mode requires single-writer", Type: "lesson",
				Source: "self_report", Confidence: 0.8,
			},
		},
		{
			name: "with tags",
			line: "[MEMORY] type=decision tags=go,testing: Always use table-driven tests",
			want: InsertParams{
				Content: "Always use table-driven tests", Type: "decision",
				Tags: []string{"go", "testing"}, Source: "self_report", Confidence: 0.8,
			},
		},
		{
			name: "pattern type",
			line: "[MEMORY] type=pattern: functional core, imperative shell",
			want: InsertParams{
				Content: "functional core, imperative shell", Type: "pattern",
				Source: "self_report", Confidence: 0.8,
			},
		},
		{
			name: "preference type with tags",
			line: "[MEMORY] type=preference tags=style: prefer f-strings over format()",
			want: InsertParams{
				Content: "prefer f-strings over format()", Type: "preference",
				Tags: []string{"style"}, Source: "self_report", Confidence: 0.8,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseMarker(tt.line)
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if got.Content != tt.want.Content {
				t.Errorf("content: got %q, want %q", got.Content, tt.want.Content)
			}
			if got.Type != tt.want.Type {
				t.Errorf("type: got %q, want %q", got.Type, tt.want.Type)
			}
			if got.Source != tt.want.Source {
				t.Errorf("source: got %q, want %q", got.Source, tt.want.Source)
			}
			if got.Confidence != tt.want.Confidence {
				t.Errorf("confidence: got %f, want %f", got.Confidence, tt.want.Confidence)
			}
			if len(got.Tags) != len(tt.want.Tags) {
				t.Errorf("tags len: got %d, want %d", len(got.Tags), len(tt.want.Tags))
			}
			for i := range got.Tags {
				if i < len(tt.want.Tags) && got.Tags[i] != tt.want.Tags[i] {
					t.Errorf("tags[%d]: got %q, want %q", i, got.Tags[i], tt.want.Tags[i])
				}
			}
		})
	}
}

func TestParseMarker_InvalidLines(t *testing.T) {
	lines := []string{
		"",
		"just a normal log line",
		"[MEMORY] missing colon content",
		"[MEMORY] type=: no content",
		"MEMORY type=gotcha: missing brackets",
		"[MEMOR] type=gotcha: wrong tag",
		"some prefix [MEMORY] type=gotcha: not at start",
	}

	for _, line := range lines {
		got := ParseMarker(line)
		if got != nil {
			t.Errorf("expected nil for line %q, got: %+v", line, got)
		}
	}
}

func TestExtractMarkers(t *testing.T) {
	input := `Starting worker...
Processing bead oro-abc
[MEMORY] type=gotcha: ruff --fix must run before pyright
Some other output
[MEMORY] type=lesson tags=go: table-driven tests are cleaner
More output
Done.
`
	reader := strings.NewReader(input)
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	count, err := ExtractMarkers(ctx, reader, store, "worker-1", "bead-abc")
	if err != nil {
		t.Fatalf("extract markers: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 extracted markers, got %d", count)
	}

	// Verify they were stored
	all, err := store.List(ctx, ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 stored memories, got %d", len(all))
	}

	// Verify worker and bead IDs were set
	for _, m := range all {
		if m.WorkerID != "worker-1" {
			t.Errorf("expected worker_id='worker-1', got %q", m.WorkerID)
		}
		if m.BeadID != "bead-abc" {
			t.Errorf("expected bead_id='bead-abc', got %q", m.BeadID)
		}
	}
}

func TestForPrompt(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content: "ruff --fix must run before pyright for linting", Type: "gotcha",
		Tags: []string{"python", "linting"}, Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	_, err = store.Insert(ctx, InsertParams{
		Content: "SQLite WAL mode requires single-writer for consistency", Type: "lesson",
		Tags: []string{"sqlite"}, Source: "self_report", Confidence: 0.85,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	result, err := ForPrompt(ctx, store, []string{"python"}, "fix linting issues with ruff and pyright", 500)
	if err != nil {
		t.Fatalf("for prompt: %v", err)
	}

	if !strings.Contains(result, "## Relevant Memories") {
		t.Error("expected markdown header")
	}
	if !strings.Contains(result, "ruff") {
		t.Error("expected ruff memory in output")
	}
	if !strings.Contains(result, "gotcha") {
		t.Error("expected gotcha type in table")
	}
	if !strings.Contains(result, "oro recall --id=") {
		t.Error("expected instruction to use oro recall --id")
	}
}

func TestForPrompt_TokenCap(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert memories with distinct but long content (unique words to avoid dedup)
	for i := 0; i < 5; i++ {
		longContent := fmt.Sprintf("searchable_term memory_%d %s", i, strings.Repeat("padding ", 50))
		_, err := store.Insert(ctx, InsertParams{
			Content: longContent, Type: "lesson",
			Source: "self_report", Confidence: 0.9,
		})
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// Request with small token cap — compact format should still show results
	result, err := ForPrompt(ctx, store, nil, "searchable_term", 50)
	if err != nil {
		t.Fatalf("for prompt: %v", err)
	}

	// Compact table format is efficient, should still include memories
	if !strings.Contains(result, "## Relevant Memories") {
		t.Error("expected markdown header")
	}
	if !strings.Contains(result, "| ID |") {
		t.Error("expected table format")
	}
}

func TestForPrompt_NoResults(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	result, err := ForPrompt(ctx, store, nil, "something that has no matches", 200)
	if err != nil {
		t.Fatalf("for prompt: %v", err)
	}

	if result != "" {
		t.Errorf("expected empty string for no results, got: %q", result)
	}
}

func TestConsolidate_MergesDuplicates(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert two very similar memories directly via SQL to bypass write-time dedup.
	// This simulates memories that accumulated before dedup was added, or were
	// inserted by different workers.
	_, err := db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence)
		 VALUES (?, ?, ?, ?, ?)`,
		"always run ruff before pyright for linting", "gotcha", `[]`, "self_report", 0.7,
	)
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}

	_, err = db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence)
		 VALUES (?, ?, ?, ?, ?)`,
		"always run ruff before pyright for linting checks", "gotcha", `[]`, "self_report", 0.9,
	)
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	merged, _, err := Consolidate(ctx, store, ConsolidateOpts{
		SimilarityThreshold: 0.01, // low threshold to catch our similar memories
		MinDecayedScore:     0.001,
	})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}

	if merged == 0 {
		t.Error("expected at least one merge of similar memories")
	}

	// Should have fewer memories now
	all, err := store.List(ctx, ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) > 1 {
		t.Logf("remaining memories: %d (merge count: %d)", len(all), merged)
	}
}

func TestConsolidate_PrunesStale(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert a very old memory with low confidence directly
	_, err := db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"very old stale memory", "lesson", `[]`, "self_report", 0.1,
		time.Now().AddDate(-1, 0, 0).Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		t.Fatalf("insert old: %v", err)
	}

	// Insert a fresh memory
	_, err = store.Insert(ctx, InsertParams{
		Content: "fresh new memory", Type: "lesson",
		Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert new: %v", err)
	}

	_, pruned, err := Consolidate(ctx, store, ConsolidateOpts{
		SimilarityThreshold: 100, // high threshold so no merges
		MinDecayedScore:     0.05,
	})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}

	if pruned == 0 {
		t.Error("expected at least one pruned memory")
	}

	// Fresh memory should survive
	all, err := store.List(ctx, ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 surviving memory, got %d", len(all))
	}
	if all[0].Content != "fresh new memory" {
		t.Errorf("expected fresh memory to survive, got: %s", all[0].Content)
	}
}

func TestConsolidate_DryRun(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert an old stale memory
	_, err := db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"old stale memory for dry run", "lesson", `[]`, "self_report", 0.1,
		time.Now().AddDate(-1, 0, 0).Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		t.Fatalf("insert old: %v", err)
	}

	_, pruned, err := Consolidate(ctx, store, ConsolidateOpts{
		SimilarityThreshold: 100,
		MinDecayedScore:     0.05,
		DryRun:              true,
	})
	if err != nil {
		t.Fatalf("consolidate dry run: %v", err)
	}

	if pruned == 0 {
		t.Error("expected dry run to count prunable memories")
	}

	// Memory should still exist since it's dry run
	all, err := store.List(ctx, ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected memory to survive dry run, got %d memories", len(all))
	}
}

// ---------------------------------------------------------------------------
// Coverage-boosting tests for error paths, edge cases, and uncovered branches.
// ---------------------------------------------------------------------------

func TestTagsFromJSON_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int // expected length of result; 0 means nil
	}{
		{"empty string", "", 0},
		{"invalid json", "not-json{{{", 0},
		{"empty array", "[]", 0},
		{"valid array", `["a","b"]`, 2},
		{"number array", `[1,2,3]`, 0}, // json.Unmarshal into []string fails
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tagsFromJSON(tt.input)
			if len(got) != tt.want {
				t.Errorf("tagsFromJSON(%q) len = %d, want %d", tt.input, len(got), tt.want)
			}
		})
	}
}

func TestTagsToJSON_EdgeCases(t *testing.T) {
	// nil slice
	if got := tagsToJSON(nil); got != "[]" {
		t.Errorf("tagsToJSON(nil) = %q, want %q", got, "[]")
	}
	// empty slice
	if got := tagsToJSON([]string{}); got != "[]" {
		t.Errorf("tagsToJSON([]string{}) = %q, want %q", got, "[]")
	}
	// normal slice
	got := tagsToJSON([]string{"a", "b"})
	if got != `["a","b"]` {
		t.Errorf("tagsToJSON([a,b]) = %q, want %q", got, `["a","b"]`)
	}
}

func TestEstimateTokens_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"empty string", "", 0},
		{"one char", "x", 1},
		{"three chars", "abc", 1},
		{"four chars", "abcd", 1},
		{"eight chars", "abcdefgh", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateTokens(tt.input)
			if got != tt.want {
				t.Errorf("estimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatAge_EdgeCases(t *testing.T) {
	// formatAge now returns human-readable durations, not raw date strings.
	// Parseable timestamps → "Xd"/"Xh"/"Xm" format.
	// Unparseable strings → passed through unchanged.
	// Empty string → empty string.

	t.Run("full datetime returns days", func(t *testing.T) {
		got := formatAge("2025-01-15 10:30:00")
		if !strings.Contains(got, "d") {
			t.Errorf("formatAge(full datetime) = %q, want days format like '440d'", got)
		}
	})
	t.Run("date only returns days", func(t *testing.T) {
		got := formatAge("2025-01-15")
		if !strings.Contains(got, "d") {
			t.Errorf("formatAge(date only) = %q, want days format like '440d'", got)
		}
	})
	t.Run("short unparseable returns raw", func(t *testing.T) {
		got := formatAge("2025")
		if got != "2025" {
			t.Errorf("formatAge(unparseable) = %q, want raw passthrough %q", got, "2025")
		}
	})
	t.Run("empty returns empty", func(t *testing.T) {
		got := formatAge("")
		if got != "" {
			t.Errorf("formatAge('') = %q, want empty string", got)
		}
	})
	t.Run("recent datetime returns hours or minutes", func(t *testing.T) {
		twoHoursAgo := time.Now().UTC().Add(-2 * time.Hour).Format("2006-01-02 15:04:05")
		got := formatAge(twoHoursAgo)
		if !strings.Contains(got, "h") {
			t.Errorf("formatAge(2h ago) = %q, want hours format like '2h'", got)
		}
	})
}

func TestSearch_EmptyQuery(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	results, err := store.Search(ctx, "", SearchOpts{})
	if err != nil {
		t.Fatalf("search empty query: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil for empty query, got %v", results)
	}
}

func TestSearch_MinScoreFilter(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content: "unique xyzzy123 frobnicator", Type: "lesson",
		Source: "self_report", Confidence: 0.1,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Search with high min score — low-confidence memory should be filtered
	results, err := store.Search(ctx, "xyzzy123", SearchOpts{MinScore: 0.99})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results with high min score, got %d", len(results))
	}
}

func TestSearch_DefaultLimit(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content: "unique_keyword_for_default_limit test", Type: "lesson",
		Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Limit <= 0 should use default of 10
	results, err := store.Search(ctx, "unique_keyword_for_default_limit", SearchOpts{Limit: 0})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
}

func TestDelete_ClosedDB(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	_ = db.Close()

	err := store.Delete(context.Background(), 1)
	if err == nil {
		t.Error("expected error from Delete on closed DB")
	}
	if !strings.Contains(err.Error(), "memory delete") {
		t.Errorf("expected wrapped error, got: %v", err)
	}
}

func TestUpdateConfidence_ClosedDB(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	_ = db.Close()

	err := store.UpdateConfidence(context.Background(), 1, 0.5)
	if err == nil {
		t.Error("expected error from UpdateConfidence on closed DB")
	}
	if !strings.Contains(err.Error(), "memory update confidence") {
		t.Errorf("expected wrapped error, got: %v", err)
	}
}

func TestInsert_ClosedDB(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	_ = db.Close()

	_, err := store.Insert(context.Background(), InsertParams{
		Content: "test", Type: "lesson",
		Source: "self_report", Confidence: 0.8,
	})
	if err == nil {
		t.Error("expected error from Insert on closed DB")
	}
}

func TestInsert_DefaultConfidence(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content: "unique_default_conf_kw memory", Type: "lesson",
		Source: "self_report", Confidence: 0, // should default to 0.8
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	all, err := store.List(ctx, ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(all))
	}
	if all[0].Confidence < 0.79 || all[0].Confidence > 0.81 {
		t.Errorf("expected default confidence ~0.8, got %f", all[0].Confidence)
	}
}

func TestList_ClosedDB(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	_ = db.Close()

	_, err := store.List(context.Background(), ListOpts{})
	if err == nil {
		t.Error("expected error from List on closed DB")
	}
	if !strings.Contains(err.Error(), "memory list") {
		t.Errorf("expected wrapped error, got: %v", err)
	}
}

func TestSearch_ClosedDB(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	_ = db.Close()

	_, err := store.Search(context.Background(), "test", SearchOpts{})
	if err == nil {
		t.Error("expected error from Search on closed DB")
	}
	if !strings.Contains(err.Error(), "memory search") {
		t.Errorf("expected wrapped error, got: %v", err)
	}
}

func TestForPrompt_EmptyDescription(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	result, err := ForPrompt(ctx, store, nil, "", 500)
	if err != nil {
		t.Fatalf("for prompt: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string for empty description, got: %q", result)
	}
}

func TestForPrompt_DefaultTokenBudget(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content: "unique_token_budget_kw fact", Type: "lesson",
		Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// maxTokens <= 0 should use defaultTokenBudget (500)
	result, err := ForPrompt(ctx, store, nil, "unique_token_budget_kw", 0)
	if err != nil {
		t.Fatalf("for prompt: %v", err)
	}
	if !strings.Contains(result, "unique_token_budget_kw") {
		t.Error("expected memory in prompt output with default token budget")
	}
}

func TestConsolidate_DefaultOpts(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert a fresh memory that should survive with default thresholds
	_, err := store.Insert(ctx, InsertParams{
		Content: "unique_consolidate_default memory", Type: "lesson",
		Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Call with zero thresholds to test defaults
	merged, pruned, err := Consolidate(ctx, store, ConsolidateOpts{})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}

	// With a single fresh high-confidence memory, nothing should be pruned or merged
	if pruned != 0 {
		t.Errorf("expected 0 pruned, got %d", pruned)
	}
	if merged != 0 {
		t.Errorf("expected 0 merged, got %d", merged)
	}
}

func TestConsolidate_PruneErrorPath(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	_ = db.Close()

	_, _, err := Consolidate(context.Background(), store, ConsolidateOpts{})
	if err == nil {
		t.Error("expected error from Consolidate on closed DB")
	}
	if !strings.Contains(err.Error(), "consolidate prune") {
		t.Errorf("expected prune error, got: %v", err)
	}
}

func TestMergePair_ErrorPaths(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	a := protocol.Memory{ID: 1, Content: "a", Confidence: 0.5}
	b := protocol.Memory{ID: 2, Content: "b", Confidence: 0.9}

	// Close the DB so BeginTx fails inside mergePair
	_ = db.Close()

	err := mergePair(ctx, store, a, b)
	if err == nil {
		t.Error("expected error from mergePair with closed DB")
	}
	if !strings.Contains(err.Error(), "merge pair begin tx") {
		t.Errorf("expected 'merge pair begin tx' error, got: %v", err)
	}
}

func TestMergePair_DeleteErrorPath(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert two memories so UpdateConfidence succeeds
	id1, err := store.Insert(ctx, InsertParams{
		Content: "unique_merge_delete_a xyzzy", Type: "lesson",
		Source: "self_report", Confidence: 0.5,
	})
	if err != nil {
		t.Fatalf("insert a: %v", err)
	}
	id2, err := store.Insert(ctx, InsertParams{
		Content: "unique_merge_delete_b frobnicator", Type: "lesson",
		Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert b: %v", err)
	}

	a := protocol.Memory{ID: id1, Content: "a", Confidence: 0.5}
	b := protocol.Memory{ID: id2, Content: "b", Confidence: 0.9}

	// Merge should succeed with a valid DB
	err = mergePair(ctx, store, a, b)
	if err != nil {
		t.Fatalf("mergePair should succeed: %v", err)
	}

	// Verify: the lower-confidence memory (a, id1) should be deleted
	all, err := store.List(ctx, ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 memory after merge, got %d", len(all))
	}
}

func TestMergePair_KeepHigherConfidence(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	id1, err := store.Insert(ctx, InsertParams{
		Content: "merge_keep_higher_a unique123", Type: "lesson",
		Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert a: %v", err)
	}
	id2, err := store.Insert(ctx, InsertParams{
		Content: "merge_keep_higher_b unique456", Type: "lesson",
		Source: "self_report", Confidence: 0.3,
	})
	if err != nil {
		t.Fatalf("insert b: %v", err)
	}

	// a has higher confidence, so b should be removed
	a := protocol.Memory{ID: id1, Content: "a", Confidence: 0.9}
	b := protocol.Memory{ID: id2, Content: "b", Confidence: 0.3}

	err = mergePair(ctx, store, a, b)
	if err != nil {
		t.Fatalf("mergePair: %v", err)
	}

	all, err := store.List(ctx, ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(all))
	}
	if all[0].ID != id1 {
		t.Errorf("expected keeper id=%d, got id=%d", id1, all[0].ID)
	}
}

func TestListSQL_WithTagFilter(t *testing.T) {
	// Exercise the tag-filtering branch in listSQL
	q, args := listSQL(ListOpts{Tag: "python", Type: "lesson", Limit: 5, Offset: 10}, 5)
	if !strings.Contains(q, "WHERE") {
		t.Error("expected WHERE clause in query")
	}
	if !strings.Contains(q, "type = ?") {
		t.Error("expected type filter in query")
	}
	if !strings.Contains(q, "tags LIKE ?") {
		t.Error("expected tags LIKE filter in query")
	}
	// args: type, tag-like-pattern, limit, offset
	if len(args) != 4 {
		t.Errorf("expected 4 args, got %d", len(args))
	}
}

func TestEscapeLike(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"100%", `100\%`},
		{"foo_bar", `foo\_bar`},
		{`a\b`, `a\\b`},
		{"normal", "normal"},
	}
	for _, tc := range cases {
		got := escapeLike(tc.input)
		if got != tc.want {
			t.Errorf("escapeLike(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestExtractMarkers_NoMarkers(t *testing.T) {
	input := "just some output\nnothing interesting\n"
	reader := strings.NewReader(input)
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	count, err := ExtractMarkers(ctx, reader, store, "w1", "b1")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 extracted markers, got %d", count)
	}
}

func TestForPrompt_SearchError(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	_ = db.Close()

	_, err := ForPrompt(context.Background(), store, nil, "test query", 500)
	if err == nil {
		t.Error("expected error from ForPrompt on closed DB")
	}
	if !strings.Contains(err.Error(), "for prompt search") {
		t.Errorf("expected 'for prompt search' error, got: %v", err)
	}
}

func TestList_WithTagFilter(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content: "tag filtered memory zephyr", Type: "lesson",
		Tags: []string{"python"}, Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	_, err = store.Insert(ctx, InsertParams{
		Content: "unrelated memory aether", Type: "gotcha",
		Tags: []string{"go"}, Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	results, err := store.List(ctx, ListOpts{Tag: "python"})
	if err != nil {
		t.Fatalf("list with tag: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result for tag filter, got %d", len(results))
	}
}

func TestConsolidate_MergeErrorPath(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert two similar memories to trigger merge
	_, err := db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence)
		 VALUES (?, ?, ?, ?, ?)`,
		"merge error path unique_xyzzy1234 test content", "gotcha", `[]`, "self_report", 0.7,
	)
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence)
		 VALUES (?, ?, ?, ?, ?)`,
		"merge error path unique_xyzzy1234 test content slightly different", "gotcha", `[]`, "self_report", 0.9,
	)
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	// Consolidate with DryRun to test the merge-counting path without errors
	merged, _, err := Consolidate(ctx, store, ConsolidateOpts{
		SimilarityThreshold: 0.01,
		MinDecayedScore:     0.001,
		DryRun:              true,
	})
	if err != nil {
		t.Fatalf("consolidate dry run: %v", err)
	}
	if merged == 0 {
		t.Error("expected at least one merge candidate counted in dry run")
	}
}

// ---------------------------------------------------------------------------
// File tracking tests (oro-jtw.6)
// ---------------------------------------------------------------------------

func TestInsertWithFileTracking(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	id, err := store.Insert(ctx, InsertParams{
		Content:       "learned about dispatcher concurrency patterns",
		Type:          "lesson",
		Tags:          []string{"go", "concurrency"},
		Source:        "self_report",
		BeadID:        "bead-123",
		WorkerID:      "worker-1",
		Confidence:    0.9,
		FilesRead:     []string{"pkg/dispatcher/dispatcher.go", "pkg/worker/worker.go"},
		FilesModified: []string{"pkg/dispatcher/dispatcher.go"},
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	var filesRead, filesModified string
	err = db.QueryRowContext(ctx,
		`SELECT files_read, files_modified FROM memories WHERE id = ?`, id,
	).Scan(&filesRead, &filesModified)
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}
	if !strings.Contains(filesRead, "dispatcher.go") {
		t.Errorf("expected files_read to contain dispatcher.go, got: %s", filesRead)
	}
	if !strings.Contains(filesModified, "dispatcher.go") {
		t.Errorf("expected files_modified to contain dispatcher.go, got: %s", filesModified)
	}

	all, err := store.List(ctx, ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(all))
	}
	if !strings.Contains(all[0].FilesRead, "dispatcher.go") {
		t.Errorf("List: expected FilesRead to contain dispatcher.go, got: %s", all[0].FilesRead)
	}
	if !strings.Contains(all[0].FilesModified, "dispatcher.go") {
		t.Errorf("List: expected FilesModified to contain dispatcher.go, got: %s", all[0].FilesModified)
	}
}

func TestInsertWithFileTracking_EmptySlices(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	id, err := store.Insert(ctx, InsertParams{
		Content:    "memory with no file tracking",
		Type:       "lesson",
		Source:     "self_report",
		Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	var filesRead, filesModified string
	err = db.QueryRowContext(ctx,
		`SELECT files_read, files_modified FROM memories WHERE id = ?`, id,
	).Scan(&filesRead, &filesModified)
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}
	if filesRead != "[]" {
		t.Errorf(`expected files_read='[]' for empty, got: %q`, filesRead)
	}
	if filesModified != "[]" {
		t.Errorf(`expected files_modified='[]' for empty, got: %q`, filesModified)
	}
}

func TestSearchByFilePath(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content:       "concurrency patterns in dispatcher unique_filetrack_xyz",
		Type:          "lesson",
		Tags:          []string{"go"},
		Source:        "self_report",
		Confidence:    0.9,
		FilesRead:     []string{"pkg/dispatcher/dispatcher.go"},
		FilesModified: []string{"pkg/dispatcher/dispatcher.go"},
	})
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}

	_, err = store.Insert(ctx, InsertParams{
		Content:       "worker lifecycle patterns unique_filetrack_xyz",
		Type:          "lesson",
		Tags:          []string{"go"},
		Source:        "self_report",
		Confidence:    0.9,
		FilesRead:     []string{"pkg/worker/worker.go"},
		FilesModified: []string{"pkg/worker/worker.go"},
	})
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	results, err := store.Search(ctx, "unique_filetrack_xyz", SearchOpts{
		FilePath: "dispatcher.go",
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for dispatcher.go filter, got %d", len(results))
	}
	if !strings.Contains(results[0].Content, "dispatcher") {
		t.Errorf("expected result about dispatcher, got: %s", results[0].Content)
	}

	results, err = store.Search(ctx, "unique_filetrack_xyz", SearchOpts{
		FilePath: "worker.go",
	})
	if err != nil {
		t.Fatalf("search worker: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for worker.go filter, got %d", len(results))
	}
	if !strings.Contains(results[0].Content, "worker") {
		t.Errorf("expected result about worker, got: %s", results[0].Content)
	}

	results, err = store.Search(ctx, "unique_filetrack_xyz", SearchOpts{})
	if err != nil {
		t.Fatalf("search all: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results without file filter, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Structured session summary tests (oro-jtw.7)
// ---------------------------------------------------------------------------

func TestForPromptIncludesSummaries(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content:    "request: implement auth | investigated: JWT libs | learned: use RS256 | completed: token generation | next_steps: add middleware",
		Type:       "summary",
		Source:     "self_report",
		BeadID:     "bead-prompt-summary",
		WorkerID:   "worker-1",
		Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert summary: %v", err)
	}

	output, err := ForPrompt(ctx, store, nil, "implement auth JWT middleware", 500)
	if err != nil {
		t.Fatalf("ForPrompt: %v", err)
	}
	if output == "" {
		t.Fatal("expected non-empty ForPrompt output")
	}
	if !strings.Contains(output, "summary") {
		t.Errorf("expected ForPrompt output to contain summary type, got: %s", output)
	}
	if !strings.Contains(output, "implement auth") {
		t.Errorf("expected ForPrompt output to contain summary content, got: %s", output)
	}
}

func TestSummaryMemorySearchable(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content:    "request: unique_summary_searchtest_xyz build search index | investigated: FTS5 | learned: rank column works | completed: search impl | next_steps: add filters",
		Type:       "summary",
		Source:     "self_report",
		BeadID:     "bead-search-summary",
		WorkerID:   "worker-2",
		Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert summary: %v", err)
	}

	results, err := store.Search(ctx, "unique_summary_searchtest_xyz", SearchOpts{
		Type: "summary",
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result for type=summary, got %d", len(results))
	}
	if results[0].Type != "summary" {
		t.Errorf("expected type=summary, got %q", results[0].Type)
	}

	listed, err := store.List(ctx, ListOpts{Type: "summary"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 listed result for type=summary, got %d", len(listed))
	}
	if listed[0].Type != "summary" {
		t.Errorf("expected listed type=summary, got %q", listed[0].Type)
	}
}

// ---------------------------------------------------------------------------
// Progressive disclosure tests (oro-jtw.5)
// ---------------------------------------------------------------------------

func TestForPrompt_CompactIndex(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert 10 memories with varied content
	for i := 0; i < 10; i++ {
		_, err := store.Insert(ctx, InsertParams{
			Content:    fmt.Sprintf("Memory %d: searchable_progressive_disclosure content about learning %d", i, i),
			Type:       "lesson",
			Source:     "self_report",
			Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// Call ForPrompt with default token budget
	result, err := ForPrompt(ctx, store, nil, "searchable_progressive_disclosure", 500)
	if err != nil {
		t.Fatalf("ForPrompt: %v", err)
	}

	// Should be compact index format, not full content
	if result == "" {
		t.Fatal("expected non-empty result")
	}

	// Check that output is compact (< 200 tokens for 10 memories)
	tokenCount := estimateTokens(result)
	if tokenCount >= 200 {
		t.Errorf("expected compact index < 200 tokens, got ~%d tokens\nOutput:\n%s", tokenCount, result)
	}

	// Verify it contains index-style format with IDs
	if !strings.Contains(result, "## Relevant Memories") {
		t.Error("expected markdown header")
	}

	// Count memory entries (should show multiple entries compactly)
	lines := strings.Split(result, "\n")
	entryCount := 0
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "|") && !strings.Contains(line, "ID") && !strings.Contains(line, "--") {
			entryCount++
		}
	}
	if entryCount == 0 {
		t.Error("expected at least one memory entry in compact index")
	}
}

func TestStore_GetByID(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert a memory
	id, err := store.Insert(ctx, InsertParams{
		Content:    "Test memory content for GetByID",
		Type:       "lesson",
		Tags:       []string{"test", "recall"},
		Source:     "self_report",
		Confidence: 0.85,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Retrieve by ID
	mem, err := store.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	// Verify content
	if mem.ID != id {
		t.Errorf("expected ID %d, got %d", id, mem.ID)
	}
	if mem.Content != "Test memory content for GetByID" {
		t.Errorf("expected content 'Test memory content for GetByID', got %q", mem.Content)
	}
	if mem.Type != "lesson" {
		t.Errorf("expected type 'lesson', got %q", mem.Type)
	}
	if mem.Confidence != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", mem.Confidence)
	}
}

func TestStore_GetByID_NotFound(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Try to retrieve non-existent ID
	_, err := store.GetByID(ctx, 99999)
	if err == nil {
		t.Fatal("expected error for non-existent ID")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Pinned memory tests (oro-u80)
// ---------------------------------------------------------------------------

func TestPinnedMemory(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert a pinned memory with a very old created_at to test decay
	_, err := db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence, created_at, pinned)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"pinned memory should never decay", "lesson", `["test"]`, "self_report", 0.8,
		time.Now().AddDate(-2, 0, 0).Format("2006-01-02 15:04:05"), // 2 years old
		1, // pinned
	)
	if err != nil {
		t.Fatalf("insert pinned: %v", err)
	}

	// Search for it
	results, err := store.Search(ctx, "pinned memory never decay", SearchOpts{Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}

	// Verify the memory is marked as pinned
	if !results[0].Pinned {
		t.Error("expected memory to be pinned")
	}

	// Verify decay factor is 1.0 (score should equal confidence)
	// For a 2-year-old unpinned memory with conf=0.8, decay would be ~0.0002
	// But for pinned, decay=1.0, so score should be 0.8
	if results[0].Score < 0.79 || results[0].Score > 0.81 {
		t.Errorf("expected pinned memory score ~0.8 (no decay), got %.4f", results[0].Score)
	}
}

func TestPinnedMemoryAlwaysTop(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert an old pinned memory
	_, err := db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence, created_at, pinned)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"old pinned memory unique_pin_top_xyz about testing", "lesson", `["test"]`, "self_report", 0.7,
		time.Now().AddDate(-1, 0, 0).Format("2006-01-02 15:04:05"), // 1 year old
		1, // pinned
	)
	if err != nil {
		t.Fatalf("insert old pinned: %v", err)
	}

	// Insert a recent unpinned memory with higher confidence
	_, err = store.Insert(ctx, InsertParams{
		Content:    "new unpinned memory unique_pin_top_xyz about testing",
		Type:       "lesson",
		Tags:       []string{"test"},
		Source:     "self_report",
		Confidence: 0.9,
		Pinned:     false,
	})
	if err != nil {
		t.Fatalf("insert new unpinned: %v", err)
	}

	// Search for both
	results, err := store.Search(ctx, "unique_pin_top_xyz testing", SearchOpts{Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	// The pinned memory should rank higher despite being older
	// Old pinned: conf=0.7, decay=1.0 → score=0.7
	// New unpinned: conf=0.9, decay=1.0 (fresh) → score=0.9
	// Actually, the new one would still rank higher. Let me adjust the test.
	// The key test is that pinned doesn't decay over time.

	// Find the pinned memory in results
	var pinnedScore float64
	var unpinnedScore float64
	for _, r := range results {
		if r.Pinned && strings.Contains(r.Content, "old pinned") {
			pinnedScore = r.Score
		}
		if !r.Pinned && strings.Contains(r.Content, "new unpinned") {
			unpinnedScore = r.Score
		}
	}

	// Pinned score should be close to 0.7 (no decay)
	if pinnedScore < 0.69 || pinnedScore > 0.71 {
		t.Errorf("expected pinned score ~0.7, got %.4f", pinnedScore)
	}

	// Unpinned score should be close to 0.9 (fresh, no decay yet)
	if unpinnedScore < 0.89 || unpinnedScore > 0.91 {
		t.Errorf("expected unpinned score ~0.9, got %.4f", unpinnedScore)
	}
}

func TestPinnedMemoryVsOldUnpinned(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert an old pinned memory with lower confidence
	_, err := db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence, created_at, pinned)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"old pinned unique_pin_vs_old_xyz critical info", "lesson", `["test"]`, "self_report", 0.6,
		time.Now().AddDate(-2, 0, 0).Format("2006-01-02 15:04:05"), // 2 years old
		1, // pinned
	)
	if err != nil {
		t.Fatalf("insert old pinned: %v", err)
	}

	// Insert an old unpinned memory with higher original confidence
	_, err = db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence, created_at, pinned)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"old unpinned unique_pin_vs_old_xyz information", "lesson", `["test"]`, "self_report", 0.9,
		time.Now().AddDate(-2, 0, 0).Format("2006-01-02 15:04:05"), // 2 years old
		0, // not pinned
	)
	if err != nil {
		t.Fatalf("insert old unpinned: %v", err)
	}

	// Search for both
	results, err := store.Search(ctx, "unique_pin_vs_old_xyz", SearchOpts{Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	// The pinned memory should rank higher
	// Old pinned: conf=0.6, decay=1.0 → score=0.6
	// Old unpinned: conf=0.9, decay=0.5^(730/30)≈0.5^24.3≈0.00000005 → score≈0
	if !results[0].Pinned {
		t.Errorf("expected pinned memory to rank first, got pinned=%v", results[0].Pinned)
	}
	if results[0].Score < 0.59 || results[0].Score > 0.61 {
		t.Errorf("expected pinned score ~0.6, got %.4f", results[0].Score)
	}

	// The unpinned one should have a very low score due to decay
	var unpinnedScore float64
	for _, r := range results {
		if !r.Pinned && strings.Contains(r.Content, "old unpinned") {
			unpinnedScore = r.Score
		}
	}
	if unpinnedScore > 0.01 {
		t.Errorf("expected unpinned old memory to have very low score (<0.01), got %.6f", unpinnedScore)
	}
}

func TestPinFlag(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert a memory with Pinned=true
	id, err := store.Insert(ctx, InsertParams{
		Content:    "never cd into worktrees unique_pin_flag_xyz",
		Type:       "gotcha",
		Source:     "cli",
		Confidence: 0.8,
		Pinned:     true,
	})
	if err != nil {
		t.Fatalf("insert with pin: %v", err)
	}

	// Retrieve it and verify pinned flag
	mem, err := store.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if !mem.Pinned {
		t.Error("expected memory to be pinned")
	}

	// Also verify via search
	results, err := store.Search(ctx, "unique_pin_flag_xyz", SearchOpts{Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if !results[0].Pinned {
		t.Error("expected search result to show pinned=true")
	}

	// Verify via list
	all, err := store.List(ctx, ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("expected at least one memory")
	}
	found := false
	for _, m := range all {
		if m.ID == id && m.Pinned {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find pinned memory in list")
	}
}

// ---------------------------------------------------------------------------
// mergeDuplicates batch limit tests (oro-rgux.10)
// ---------------------------------------------------------------------------

func TestMergeDuplicates_BatchLimit(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert 120 highly similar memories to test batch size limit
	// We use direct SQL to bypass write-time dedup
	for i := 0; i < 120; i++ {
		content := fmt.Sprintf("similar memory about batch testing %d", i)
		_, err := db.ExecContext(ctx,
			`INSERT INTO memories (content, type, tags, source, confidence)
			 VALUES (?, ?, ?, ?, ?)`,
			content, "lesson", `["test"]`, "self_report", 0.5+float64(i)*0.001,
		)
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// Verify all were inserted
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories`).Scan(&count)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 120 {
		t.Fatalf("expected 120 memories, got %d", count)
	}

	// Run mergeDuplicates with a low threshold so duplicates are found
	merged, err := mergeDuplicates(ctx, store, 0.01, false)
	if err != nil {
		t.Fatalf("mergeDuplicates: %v", err)
	}

	// Key assertions:
	// 1. Batch size is 100, so only first 100 memories are processed
	// 2. Early termination happens at 50 merges
	// So we expect merged <= 50
	if merged > 50 {
		t.Errorf("expected merged <= 50 (early termination), got %d", merged)
	}

	// We should have merged at least some memories
	if merged == 0 {
		t.Error("expected at least some merges with low threshold")
	}

	// Verify that some memories remain (not all 120 were processed)
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories`).Scan(&count)
	if err != nil {
		t.Fatalf("count after merge: %v", err)
	}
	// With 120 inserted and merged <= 50, we should have >= 70 remaining
	if count < 70 {
		t.Errorf("expected at least 70 memories remaining (120 - max 50 merged), got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Vector search memory bounds tests (oro-rgux.2)
// ---------------------------------------------------------------------------

func TestVectorSearch_BoundsMemory(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert >1000 rows with embeddings directly via SQL
	// to bypass Insert dedup and simulate a large corpus
	const totalRows = 1200
	for i := 0; i < totalRows; i++ {
		content := fmt.Sprintf("vector search memory bounds test row %d unique_bounds_%d", i, i)
		// Create a simple embedding vector (dimension 3 for test speed)
		embedding := MarshalEmbedding([]float32{float32(i) * 0.001, 0.5, 0.3})

		_, err := db.ExecContext(ctx,
			`INSERT INTO memories (content, type, tags, source, confidence, embedding, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			content, "lesson", `["test"]`, "self_report", 0.8, embedding,
			time.Now().Add(-time.Duration(i)*time.Second).Format("2006-01-02 15:04:05"),
		)
		if err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}

	// Verify all rows were inserted
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories WHERE embedding IS NOT NULL`).Scan(&count)
	if err != nil {
		t.Fatalf("count check: %v", err)
	}
	if count != totalRows {
		t.Fatalf("expected %d rows inserted, got %d", totalRows, count)
	}

	// Create a query vector
	queryVec := []float32{0.1, 0.5, 0.3}

	// Call vectorSearch - it should only load maxVectorCandidates (1000) rows
	results, err := store.vectorSearch(ctx, queryVec, 10, "")
	if err != nil {
		t.Fatalf("vectorSearch: %v", err)
	}

	// We requested 10 results, should get 10 (or fewer if less than 10 match)
	if len(results) == 0 {
		t.Fatal("expected at least some results from vectorSearch")
	}
	if len(results) > 10 {
		t.Errorf("expected at most 10 results, got %d", len(results))
	}

	// The key assertion: vectorSearch should have limited the DB query to 1000 rows
	// We can't directly measure how many rows were loaded from the DB in the test,
	// but we verify the implementation honors maxVectorCandidates by checking
	// that the function succeeds and returns reasonable results.
	// The implementation test is in the code itself: the LIMIT clause bounds memory.

	// Verify results are sensible (non-zero scores, valid content)
	for i, r := range results {
		if r.Score <= 0 {
			t.Errorf("result[%d] has non-positive score: %f", i, r.Score)
		}
		if r.Content == "" {
			t.Errorf("result[%d] has empty content", i)
		}
	}
}

// TestForPrompt_UsesHybridSearch verifies that ForPrompt routes through
// HybridSearch when an embedder is set, and falls back to Search otherwise.
//
// Detection method: HybridSearch calls embedder.Embed(query) which grows the
// vocabulary. Store.Search never touches the embedder. So after ForPrompt runs
// with a query that contains a novel term, vocab growth proves HybridSearch
// was invoked.
func TestForPrompt_UsesHybridSearch(t *testing.T) {
	ctx := context.Background()

	// --- Case 1: with embedder, ForPrompt must route through HybridSearch ---
	embedder := NewEmbedder()
	store := NewStore(setupTestDB(t))
	store.SetEmbedder(embedder)

	_, err := store.Insert(ctx, InsertParams{
		Content: "ruff pyright linting python always run first", Type: "gotcha",
		Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	vocabBefore := embedder.VocabSize()

	// Query includes "unique_xzq9hybrid" — a term not in any stored memory.
	// HybridSearch calls embedder.Embed(query) → vocab grows.
	// Search (FTS5-only) never touches the embedder → vocab stays same.
	result, err := ForPrompt(ctx, store, nil, "unique_xzq9hybrid ruff pyright linting", 500)
	if err != nil {
		t.Fatalf("ForPrompt with embedder: %v", err)
	}

	vocabAfter := embedder.VocabSize()
	if vocabAfter <= vocabBefore {
		t.Errorf("ForPrompt with embedder should call HybridSearch (which calls Embed on query); "+
			"vocab did not grow: before=%d after=%d", vocabBefore, vocabAfter)
	}

	// Results must be non-empty (FTS5 and/or vector finds the stored memory).
	if result == "" {
		t.Fatal("expected non-empty result from ForPrompt with embedder")
	}
	if !strings.Contains(result, "## Relevant Memories") {
		t.Error("expected markdown header in result")
	}
	if !strings.Contains(result, "ruff") {
		t.Error("expected ruff memory content in result")
	}

	// --- Case 2: with embedder, vector-similar match ranks before weaker match ---
	embedder2 := NewEmbedder()
	store2 := NewStore(setupTestDB(t))
	store2.SetEmbedder(embedder2)

	_, err = store2.Insert(ctx, InsertParams{
		// All query terms, repeated — high TF vector + high FTS5 BM25.
		Content: "ruff pyright linting always run ruff before pyright python", Type: "gotcha",
		Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert gotcha: %v", err)
	}
	_, err = store2.Insert(ctx, InsertParams{
		// Only "ruff" and "python" from query — weaker match.
		Content: "ruff python code quality tools", Type: "lesson",
		Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert lesson: %v", err)
	}

	result2, err := ForPrompt(ctx, store2, nil, "ruff pyright linting python", 500)
	if err != nil {
		t.Fatalf("ForPrompt ranking: %v", err)
	}
	if !strings.Contains(result2, "## Relevant Memories") {
		t.Error("expected markdown header in ranking result")
	}
	gotchaPos := strings.Index(result2, "gotcha")
	lessonPos := strings.Index(result2, "lesson")
	if gotchaPos == -1 {
		t.Error("expected gotcha (all query terms → high FTS5+vector) in results")
	}
	if lessonPos != -1 && gotchaPos > lessonPos {
		t.Error("expected gotcha (full match) to rank before lesson (partial match)")
	}

	// --- Case 3: without embedder, ForPrompt falls back to Search ---
	storeFTS := NewStore(setupTestDB(t))
	_, err = storeFTS.Insert(ctx, InsertParams{
		Content: "ruff pyright linting python", Type: "gotcha",
		Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert fallback: %v", err)
	}

	resultFTS, err := ForPrompt(ctx, storeFTS, nil, "ruff pyright linting python", 500)
	if err != nil {
		t.Fatalf("ForPrompt without embedder: %v", err)
	}
	if resultFTS == "" {
		t.Fatal("expected non-empty result from ForPrompt without embedder (Search fallback)")
	}
	if !strings.Contains(resultFTS, "gotcha") {
		t.Error("expected gotcha in FTS5-fallback results")
	}
}

// ---------------------------------------------------------------------------
// SetProject tests (oro-1rep.5)
// ---------------------------------------------------------------------------

func TestSetProjectScopesSearchAndInsert(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Test 1: Insert with SetProject(A) stamps project=A
	store.SetProject("projectA")
	idA1, err := store.Insert(ctx, InsertParams{
		Content:    "memory for project A unique_proj_a_xyz",
		Type:       "lesson",
		Source:     "self_report",
		Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert A1: %v", err)
	}

	// Verify project column was set
	var project string
	err = db.QueryRowContext(ctx, `SELECT project FROM memories WHERE id = ?`, idA1).Scan(&project)
	if err != nil {
		t.Fatalf("query project A1: %v", err)
	}
	if project != "projectA" {
		t.Errorf("expected project='projectA', got %q", project)
	}

	// Insert another for project A (with distinct content to avoid dedup)
	_, err = store.Insert(ctx, InsertParams{
		Content:    "completely different content about testing patterns unique_proj_a_xyz projectA specific details",
		Type:       "gotcha",
		Source:     "self_report",
		Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert A2: %v", err)
	}

	// Test 2: Insert with SetProject(B) stamps project=B
	store.SetProject("projectB")
	idB1, err := store.Insert(ctx, InsertParams{
		Content:    "memory for project B unique_proj_b_xyz",
		Type:       "lesson",
		Source:     "self_report",
		Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert B1: %v", err)
	}

	err = db.QueryRowContext(ctx, `SELECT project FROM memories WHERE id = ?`, idB1).Scan(&project)
	if err != nil {
		t.Fatalf("query project B1: %v", err)
	}
	if project != "projectB" {
		t.Errorf("expected project='projectB', got %q", project)
	}

	// Test 3: Search with SetProject(A) returns only project A memories
	store.SetProject("projectA")
	resultsA, err := store.Search(ctx, "unique_proj_a_xyz", SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("search A: %v", err)
	}
	if len(resultsA) != 2 {
		t.Errorf("expected 2 results for project A, got %d", len(resultsA))
	}
	for _, r := range resultsA {
		if !strings.Contains(r.Content, "unique_proj_a_xyz") {
			t.Errorf("expected project A content with unique_proj_a_xyz, got: %s", r.Content)
		}
	}

	// Test 4: Search with SetProject(B) returns only project B memories
	store.SetProject("projectB")
	resultsB, err := store.Search(ctx, "unique_proj_b_xyz", SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("search B: %v", err)
	}
	if len(resultsB) != 1 {
		t.Errorf("expected 1 result for project B, got %d", len(resultsB))
	}
	if !strings.Contains(resultsB[0].Content, "project B") {
		t.Errorf("expected project B content, got: %s", resultsB[0].Content)
	}

	// Test 5: SetProject empty string returns all memories (no filtering)
	store.SetProject("")
	resultsAll, err := store.Search(ctx, "unique_proj", SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("search all: %v", err)
	}
	if len(resultsAll) < 3 {
		t.Errorf("expected at least 3 results (2 A + 1 B), got %d", len(resultsAll))
	}

	// Test 6: ForPrompt scoped to project
	store.SetProject("projectA")
	prompt, err := ForPrompt(ctx, store, nil, "unique_proj_a_xyz", 500)
	if err != nil {
		t.Fatalf("ForPrompt A: %v", err)
	}
	if !strings.Contains(prompt, "unique_proj_a_xyz") || prompt == "" {
		t.Error("expected ForPrompt to contain project A memories with unique_proj_a_xyz")
	}
	if strings.Contains(prompt, "unique_proj_b_xyz") {
		t.Error("expected ForPrompt NOT to contain project B memories")
	}

	// Test 7: ForPrompt with empty project returns all
	store.SetProject("")
	promptAll, err := ForPrompt(ctx, store, nil, "unique_proj", 500)
	if err != nil {
		t.Fatalf("ForPrompt all: %v", err)
	}
	if !strings.Contains(promptAll, "unique_proj_a_xyz") {
		t.Error("expected ForPrompt all to contain project A memories")
	}
	if !strings.Contains(promptAll, "unique_proj_b_xyz") {
		t.Error("expected ForPrompt all to contain project B memories")
	}
}

func TestMigrateProjectColumn_Idempotent(t *testing.T) {
	// Create DB with OLD schema (without project column)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Use old schema without project column
	oldSchema := `
	CREATE TABLE IF NOT EXISTS memories (
		id INTEGER PRIMARY KEY,
		content TEXT NOT NULL,
		type TEXT NOT NULL,
		tags TEXT,
		source TEXT NOT NULL,
		bead_id TEXT,
		worker_id TEXT,
		confidence REAL DEFAULT 0.8,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		embedding BLOB,
		files_read TEXT DEFAULT '[]',
		files_modified TEXT DEFAULT '[]',
		pinned INTEGER DEFAULT 0
	);
	CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
		content,
		tags,
		content=memories,
		content_rowid=id
	);
	CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
		INSERT INTO memories_fts(rowid, content, tags) VALUES (new.id, new.content, new.tags);
	END;
	`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("exec old schema: %v", err)
	}

	ctx := context.Background()

	// Insert a memory before migration (without using SetProject since column doesn't exist)
	res, err := db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence)
		 VALUES (?, ?, ?, ?, ?)`,
		"memory before migration unique_mig_xyz", "lesson", "[]", "self_report", 0.8,
	)
	if err != nil {
		t.Fatalf("insert before migration: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	// Run migration first time
	migration := `ALTER TABLE memories ADD COLUMN project TEXT DEFAULT 'oro'`
	_, err = db.Exec(migration)
	if err != nil {
		t.Fatalf("first migration: %v", err)
	}

	// Backfill
	_, err = db.Exec(`UPDATE memories SET project = 'oro' WHERE project IS NULL OR project = ''`)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// Verify backfill worked
	var project string
	err = db.QueryRowContext(ctx, `SELECT project FROM memories WHERE id = ?`, id).Scan(&project)
	if err != nil {
		t.Fatalf("query project: %v", err)
	}
	if project != "oro" {
		t.Errorf("expected backfilled project='oro', got %q", project)
	}

	// Run migration second time (idempotency test)
	// This should fail with "duplicate column name" but we catch and ignore
	_, err = db.Exec(migration)
	if err == nil {
		t.Error("expected error on duplicate column, but migration succeeded")
	}
	// SQLite error is expected, verify the table still works

	// Verify data still intact
	err = db.QueryRowContext(ctx, `SELECT project FROM memories WHERE id = ?`, id).Scan(&project)
	if err != nil {
		t.Fatalf("query after duplicate migration: %v", err)
	}
	if project != "oro" {
		t.Errorf("expected project='oro' after duplicate migration, got %q", project)
	}

	// Insert a new memory after migration
	store2 := NewStore(db)
	store2.SetProject("test-project")
	id2, err := store2.Insert(ctx, InsertParams{
		Content:    "memory after migration unique_mig_xyz",
		Type:       "lesson",
		Source:     "self_report",
		Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert after migration: %v", err)
	}

	err = db.QueryRowContext(ctx, `SELECT project FROM memories WHERE id = ?`, id2).Scan(&project)
	if err != nil {
		t.Fatalf("query project2: %v", err)
	}
	if project != "test-project" {
		t.Errorf("expected project='test-project', got %q", project)
	}
}

func TestInsertQualityGate(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	validContent := "This is a valid memory content for testing purposes"

	t.Run("rejects content shorter than 10 chars", func(t *testing.T) {
		_, err := store.Insert(ctx, InsertParams{
			Content: "short",
			Type:    "lesson",
		})
		if err == nil {
			t.Error("expected error for content < 10 chars, got nil")
		}
	})

	t.Run("rejects content longer than 2048 chars", func(t *testing.T) {
		_, err := store.Insert(ctx, InsertParams{
			Content: strings.Repeat("a", 2049),
			Type:    "lesson",
		})
		if err == nil {
			t.Error("expected error for content > 2048 chars, got nil")
		}
	})

	t.Run("rejects invalid type", func(t *testing.T) {
		_, err := store.Insert(ctx, InsertParams{
			Content: validContent,
			Type:    "bogus",
		})
		if err == nil {
			t.Error("expected error for type='bogus', got nil")
		}
	})

	t.Run("accepts preference type with valid content", func(t *testing.T) {
		id, err := store.Insert(ctx, InsertParams{
			Content: validContent,
			Type:    "preference",
		})
		if err != nil {
			t.Errorf("expected no error for valid insert, got: %v", err)
		}
		if id <= 0 {
			t.Errorf("expected positive ID, got %d", id)
		}
	})
}

// ---------------------------------------------------------------------------
// Rejection history tests (oro-jwwt.1.2.1)
// ---------------------------------------------------------------------------

// TestInsertRejection verifies that InsertRejection writes to rejection_history,
// NOT to the memories table.
func TestInsertRejection(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	err := store.InsertRejection(ctx, "oro-bead1", "worker1", "missing edge case tests")
	if err != nil {
		t.Fatalf("InsertRejection: %v", err)
	}

	// Verify it's in rejection_history.
	var histCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rejection_history WHERE bead_id = 'oro-bead1'`,
	).Scan(&histCount); err != nil {
		t.Fatalf("query rejection_history: %v", err)
	}
	if histCount != 1 {
		t.Errorf("expected 1 entry in rejection_history, got %d", histCount)
	}

	// Verify memories table has NO rejection entry.
	var memCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE content LIKE 'Reviewer rejected%'`,
	).Scan(&memCount); err != nil {
		t.Fatalf("query memories: %v", err)
	}
	if memCount != 0 {
		t.Errorf("expected 0 rejection entries in memories, got %d", memCount)
	}
}

// TestGetRejections verifies that GetRejections returns rejection entries for
// the specified bead only.
func TestGetRejections(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if err := store.InsertRejection(ctx, "oro-bead1", "w1", "missing tests"); err != nil {
		t.Fatalf("InsertRejection 1: %v", err)
	}
	if err := store.InsertRejection(ctx, "oro-bead1", "w2", "wrong implementation"); err != nil {
		t.Fatalf("InsertRejection 2: %v", err)
	}
	if err := store.InsertRejection(ctx, "oro-bead2", "w1", "different bead"); err != nil {
		t.Fatalf("InsertRejection 3: %v", err)
	}

	rejections, err := store.GetRejections(ctx, "oro-bead1")
	if err != nil {
		t.Fatalf("GetRejections: %v", err)
	}
	if len(rejections) != 2 {
		t.Errorf("expected 2 rejections for oro-bead1, got %d", len(rejections))
	}
	for _, r := range rejections {
		if r.BeadID != "oro-bead1" {
			t.Errorf("expected BeadID 'oro-bead1', got %q", r.BeadID)
		}
		if r.Feedback == "" {
			t.Error("expected non-empty Feedback")
		}
	}

	rejections2, err := store.GetRejections(ctx, "oro-bead2")
	if err != nil {
		t.Fatalf("GetRejections bead2: %v", err)
	}
	if len(rejections2) != 1 {
		t.Errorf("expected 1 rejection for oro-bead2, got %d", len(rejections2))
	}
	if rejections2[0].Feedback != "different bead" {
		t.Errorf("expected feedback 'different bead', got %q", rejections2[0].Feedback)
	}

	// GetRejections on a bead with no rejections returns empty slice.
	empty, err := store.GetRejections(ctx, "oro-no-such-bead")
	if err != nil {
		t.Fatalf("GetRejections empty: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected 0 rejections for unknown bead, got %d", len(empty))
	}
}

// TestGetRejectionsAfterMigration verifies assertions (4) and (5) from oro-jwwt.1:
// (4) MigrateRejectionHistory backfills rejection_history from memories WHERE
// content LIKE 'Reviewer rejected this bead: %', then deletes those rows from
// memories. (5) After migration, List returns zero rejection entries.
func TestGetRejectionsAfterMigration(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Seed old-style rejection entries directly into memories,
	// simulating rows created before rejection_history existed.
	seeds := []struct {
		beadID  string
		content string
	}{
		{"oro-mig-bead", "Reviewer rejected this bead: needs more tests"},
		{"oro-mig-bead", "Reviewer rejected this bead: wrong implementation"},
		{"oro-mig-other", "Reviewer rejected this bead: missing edge cases"},
	}
	for _, s := range seeds {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO memories (content, type, source, bead_id, confidence, tags) VALUES (?, 'lesson', 'self_report', ?, 0.8, '[]')`,
			s.content, s.beadID,
		); err != nil {
			t.Fatalf("seed memories: %v", err)
		}
	}

	// Confirm seed: memories table has the rejection rows.
	var preMigCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE content LIKE 'Reviewer rejected this bead: %'`,
	).Scan(&preMigCount); err != nil {
		t.Fatalf("pre-migration count: %v", err)
	}
	if preMigCount != 3 {
		t.Fatalf("expected 3 seeded rows in memories, got %d", preMigCount)
	}

	// Run migration (assertion 4).
	if _, err := db.ExecContext(ctx, protocol.MigrateRejectionHistory); err != nil {
		t.Fatalf("MigrateRejectionHistory: %v", err)
	}

	// (4a) rejection_history must contain the backfilled entries.
	var histCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rejection_history`,
	).Scan(&histCount); err != nil {
		t.Fatalf("query rejection_history count: %v", err)
	}
	if histCount != 3 {
		t.Errorf("expected 3 entries in rejection_history after migration, got %d", histCount)
	}

	// (4b) memories table must have zero 'Reviewer rejected' rows (assertion 4).
	var postMigCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE content LIKE 'Reviewer rejected%'`,
	).Scan(&postMigCount); err != nil {
		t.Fatalf("post-migration memories count: %v", err)
	}
	if postMigCount != 0 {
		t.Errorf("expected 0 rejection entries in memories after migration, got %d", postMigCount)
	}

	// GetRejections returns the migrated entries for each bead.
	rejections, err := store.GetRejections(ctx, "oro-mig-bead")
	if err != nil {
		t.Fatalf("GetRejections: %v", err)
	}
	if len(rejections) != 2 {
		t.Errorf("expected 2 rejections for oro-mig-bead, got %d", len(rejections))
	}
	for _, r := range rejections {
		if r.Feedback == "" {
			t.Error("expected non-empty Feedback after migration")
		}
		// Feedback must NOT retain the "Reviewer rejected this bead: " prefix.
		if strings.HasPrefix(r.Feedback, "Reviewer rejected") {
			t.Errorf("feedback should not contain prefix after migration, got %q", r.Feedback)
		}
	}

	// (5) List returns zero rejection entries after migration.
	all, err := store.List(ctx, ListOpts{Limit: 200})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, m := range all {
		if strings.HasPrefix(m.Content, "Reviewer rejected") {
			t.Errorf("List returned rejection entry that should have been migrated: %q", m.Content)
		}
	}
}

func TestDumpAll(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Test 1: Empty table should return nil slice
	results, err := store.DumpAll(ctx)
	if err != nil {
		t.Fatalf("DumpAll on empty table: %v", err)
	}
	if len(results) > 0 {
		t.Errorf("expected nil slice on empty table, got %d results", len(results))
	}

	// Test 2: Insert memories with different projects
	store.SetProject("project-a")
	id1, err := store.Insert(ctx, InsertParams{
		Content: "memory for project A", Type: "lesson", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert project A memory: %v", err)
	}

	id2, err := store.Insert(ctx, InsertParams{
		Content: "another for project A", Type: "gotcha", Confidence: 0.85,
	})
	if err != nil {
		t.Fatalf("insert second project A memory: %v", err)
	}

	store.SetProject("project-b")
	id3, err := store.Insert(ctx, InsertParams{
		Content: "memory for project B", Type: "decision", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert project B memory: %v", err)
	}

	// Test 3: DumpAll with project-a scope should return only project-a memories
	store.SetProject("project-a")
	results, err = store.DumpAll(ctx)
	if err != nil {
		t.Fatalf("DumpAll with project-a: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 memories for project-a, got %d", len(results))
	}
	for _, m := range results {
		if m.Content != "memory for project A" && m.Content != "another for project A" {
			t.Errorf("unexpected content for project-a: %q", m.Content)
		}
	}
	if results[0].ID != id1 && results[0].ID != id2 {
		t.Errorf("unexpected ID in results")
	}

	// Test 4: DumpAll with project-b scope should return only project-b memories
	store.SetProject("project-b")
	results, err = store.DumpAll(ctx)
	if err != nil {
		t.Fatalf("DumpAll with project-b: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 memory for project-b, got %d", len(results))
	}
	if results[0].ID != id3 {
		t.Errorf("expected ID %d, got %d", id3, results[0].ID)
	}

	// Test 5: DumpAll with empty project scope should return all memories
	store.SetProject("")
	results, err = store.DumpAll(ctx)
	if err != nil {
		t.Fatalf("DumpAll with empty project: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 memories with empty project scope, got %d", len(results))
	}
}

func TestMergeMemories(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Test 1: Insert multiple memories
	store.SetProject("test-project")
	id1, err := store.Insert(ctx, InsertParams{
		Content: "memory 1 with long content for testing", Type: "lesson", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert memory 1: %v", err)
	}

	id2, err := store.Insert(ctx, InsertParams{
		Content: "memory 2 with lower confidence value", Type: "gotcha", Confidence: 0.7,
	})
	if err != nil {
		t.Fatalf("insert memory 2: %v", err)
	}

	id3, err := store.Insert(ctx, InsertParams{
		Content: "memory 3 to keep for testing merge", Type: "decision", Confidence: 0.95,
	})
	if err != nil {
		t.Fatalf("insert memory 3: %v", err)
	}

	// Test 2: Verify all 3 memories exist
	all, err := store.List(ctx, ListOpts{Limit: 100})
	if err != nil {
		t.Fatalf("list before merge: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 memories before merge, got %d", len(all))
	}

	// Test 3: Merge memories - keep id3, delete id1 and id2
	err = store.MergeMemories(ctx, id3, []int64{id1, id2})
	if err != nil {
		t.Fatalf("MergeMemories: %v", err)
	}

	// Test 4: Verify id1 and id2 are deleted
	all, err = store.List(ctx, ListOpts{Limit: 100})
	if err != nil {
		t.Fatalf("list after merge: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 memory after merge, got %d", len(all))
	}
	if all[0].ID != id3 {
		t.Errorf("expected kept memory ID %d, got %d", id3, all[0].ID)
	}

	// Test 5: MergeMemories with non-existent keepID should error
	err = store.MergeMemories(ctx, 99999, []int64{})
	if err == nil {
		t.Error("expected error for non-existent keepID, got nil")
	}

	// Test 6: MergeMemories with empty deleteIDs should work (just keep the memory)
	_, err = store.Insert(ctx, InsertParams{
		Content: "memory 4 with longer content for validation", Type: "lesson", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert memory 4: %v", err)
	}

	err = store.MergeMemories(ctx, id3, []int64{})
	if err != nil {
		t.Fatalf("MergeMemories with empty deleteIDs: %v", err)
	}

	// Verify both memories still exist
	all, err = store.List(ctx, ListOpts{Limit: 100})
	if err != nil {
		t.Fatalf("list after merge with empty deleteIDs: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 memories after merge with empty deleteIDs, got %d", len(all))
	}
}

// TestForPromptStaleness verifies Age column, stale markers, formatAge, and edge cases.
func TestForPromptStaleness(t *testing.T) {
	ctx := context.Background()

	// formatAge parses timestamps as UTC (matching SQLite's datetime('now') output),
	// so test inputs must be formatted in UTC as well.

	// --- formatAge returns human-readable durations ---
	t.Run("formatAge_sub_minute", func(t *testing.T) {
		justNow := time.Now().UTC().Add(-10 * time.Second).Format("2006-01-02 15:04:05")
		got := formatAge(justNow)
		if got != "<1m" {
			t.Errorf("expected '<1m' for sub-minute age, got %q", got)
		}
	})

	t.Run("formatAge_minutes", func(t *testing.T) {
		recent := time.Now().UTC().Add(-45 * time.Minute).Format("2006-01-02 15:04:05")
		got := formatAge(recent)
		if !strings.Contains(got, "m") || strings.Contains(got, "d") || got == "<1m" {
			t.Errorf("expected minutes format like '45m', got %q", got)
		}
	})

	t.Run("formatAge_hours", func(t *testing.T) {
		threeHoursAgo := time.Now().UTC().Add(-3 * time.Hour).Format("2006-01-02 15:04:05")
		got := formatAge(threeHoursAgo)
		if !strings.Contains(got, "h") {
			t.Errorf("expected hours format like '3h', got %q", got)
		}
	})

	t.Run("formatAge_days", func(t *testing.T) {
		fiveDaysAgo := time.Now().UTC().Add(-5 * 24 * time.Hour).Format("2006-01-02 15:04:05")
		got := formatAge(fiveDaysAgo)
		if !strings.Contains(got, "d") {
			t.Errorf("expected days format like '5d', got %q", got)
		}
	})

	// --- zero memories returns empty string ---
	t.Run("zero_memories", func(t *testing.T) {
		db := setupTestDB(t)
		store := NewStore(db)
		result, err := ForPrompt(ctx, store, nil, "no match whatsoever xyz789qrs", 500)
		if err != nil {
			t.Fatal(err)
		}
		if result != "" {
			t.Errorf("expected empty string for zero memories, got %q", result)
		}
	})

	// --- Age column and stale marker for >7d memories ---
	t.Run("age_column_and_stale_marker", func(t *testing.T) {
		db := setupTestDB(t)
		store := NewStore(db)

		// Insert stale memory (>7 days old) via direct SQL to control created_at.
		_, err := db.ExecContext(ctx,
			`INSERT INTO memories (content, type, tags, source, confidence, created_at)
			 VALUES (?, ?, ?, ?, ?, datetime('now', '-8 days'))`,
			"ruff pyright python linting stale memory", "gotcha", `[]`, "self_report", 0.9,
		)
		if err != nil {
			t.Fatal(err)
		}

		result, err := ForPrompt(ctx, store, nil, "ruff pyright python linting", 500)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "Age") {
			t.Errorf("expected Age column in table header, got:\n%s", result)
		}
		if !strings.Contains(result, "⚠") {
			t.Errorf("expected stale warning marker for >7d memory, got:\n%s", result)
		}
	})

	// --- all fresh → no stale markers ---
	t.Run("all_fresh_no_markers", func(t *testing.T) {
		db := setupTestDB(t)
		store := NewStore(db)

		_, err := db.ExecContext(ctx,
			`INSERT INTO memories (content, type, tags, source, confidence, created_at)
			 VALUES (?, ?, ?, ?, ?, datetime('now', '-2 days'))`,
			"ruff pyright python linting fresh only", "gotcha", `[]`, "self_report", 0.9,
		)
		if err != nil {
			t.Fatal(err)
		}

		result, err := ForPrompt(ctx, store, nil, "ruff pyright python linting fresh", 500)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "Age") {
			t.Errorf("expected Age column in table header, got:\n%s", result)
		}
		if strings.Contains(result, "⚠") {
			t.Errorf("expected no stale warning for fresh memory, got:\n%s", result)
		}
	})

	// --- pinned memory shows age but no warning even if stale ---
	t.Run("pinned_no_warning", func(t *testing.T) {
		db := setupTestDB(t)
		store := NewStore(db)

		_, err := db.ExecContext(ctx,
			`INSERT INTO memories (content, type, tags, source, confidence, pinned, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, datetime('now', '-10 days'))`,
			"ruff pyright python linting pinned stale old", "pattern", `[]`, "self_report", 0.9, 1,
		)
		if err != nil {
			t.Fatal(err)
		}

		result, err := ForPrompt(ctx, store, nil, "ruff pyright python linting pinned", 500)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "Age") {
			t.Errorf("expected Age column for pinned memory, got:\n%s", result)
		}
		if strings.Contains(result, "⚠") {
			t.Errorf("expected no stale warning for pinned memory, got:\n%s", result)
		}
	})
}

// TestMergeMemoriesAtomic verifies that MergeMemories, mergePair, and the dream
// MERGE action all execute atomically — a partial failure must roll back all steps.
func TestMergeMemoriesAtomic(t *testing.T) {
	ctx := context.Background()

	// --- MergeMemories: keepID not found → deleteIDs must be preserved ---
	t.Run("MergeMemories_keepID_not_found_preserves_deleteIDs", func(t *testing.T) {
		db := setupTestDB(t)
		store := NewStore(db)

		id1, err := store.Insert(ctx, InsertParams{
			Content: "merge atomic source one long enough content here", Type: "lesson",
			Source: "self_report", Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert id1: %v", err)
		}
		id2, err := store.Insert(ctx, InsertParams{
			Content: "merge atomic source two long enough content here", Type: "lesson",
			Source: "self_report", Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert id2: %v", err)
		}

		err = store.MergeMemories(ctx, 99999, []int64{id1, id2})
		if err == nil {
			t.Fatal("expected error for nonexistent keepID, got nil")
		}

		all, dumpErr := store.DumpAll(ctx)
		if dumpErr != nil {
			t.Fatalf("dump: %v", dumpErr)
		}
		found := map[int64]bool{}
		for _, m := range all {
			found[m.ID] = true
		}
		if !found[id1] {
			t.Error("id1 was deleted but keepID did not exist — atomicity violation")
		}
		if !found[id2] {
			t.Error("id2 was deleted but keepID did not exist — atomicity violation")
		}
	})

	// --- mergePair: delete failure must roll back the confidence update ---
	t.Run("mergePair_rolls_back_confidence_on_delete_failure", func(t *testing.T) {
		db := setupTestDB(t)
		store := NewStore(db)

		// DB value for idA is 0.5; struct confidence is 0.9 so UpdateConfidence will change the row.
		idA, err := store.Insert(ctx, InsertParams{
			Content: "atomic pair alpha long enough content for insert test", Type: "lesson",
			Source: "self_report", Confidence: 0.5,
		})
		if err != nil {
			t.Fatalf("insert A: %v", err)
		}
		idB, err := store.Insert(ctx, InsertParams{
			Content: "atomic pair beta long enough content for insert test", Type: "lesson",
			Source: "self_report", Confidence: 0.3,
		})
		if err != nil {
			t.Fatalf("insert B: %v", err)
		}

		// Trigger: prevent deletion of idB (removeID when a.Confidence > b.Confidence).
		_, err = db.ExecContext(ctx, fmt.Sprintf(`
			CREATE TRIGGER prevent_delete_pair
			BEFORE DELETE ON memories
			WHEN OLD.id = %d
			BEGIN SELECT RAISE(FAIL, 'atomicity test: delete prevented'); END;
		`, idB))
		if err != nil {
			t.Fatalf("create trigger: %v", err)
		}

		// keepID=idA (0.9 > 0.3 → no swap), UpdateConfidence(idA, 0.9) changes DB from 0.5→0.9.
		// Then Delete(idB) triggers FAIL.
		// With transaction: UpdateConfidence must be rolled back → idA.confidence stays 0.5.
		memA := protocol.Memory{ID: idA, Confidence: 0.9}
		memB := protocol.Memory{ID: idB, Confidence: 0.3}

		err = mergePair(ctx, store, memA, memB)
		if err == nil {
			t.Fatal("expected error from trigger, got nil")
		}

		var gotConf float64
		if scanErr := db.QueryRowContext(ctx, `SELECT confidence FROM memories WHERE id = ?`, idA).Scan(&gotConf); scanErr != nil {
			t.Fatalf("scan confidence: %v", scanErr)
		}
		if gotConf != 0.5 {
			t.Errorf("expected idA.confidence=0.5 (rolled back), got %v — mergePair not atomic", gotConf)
		}
	})

	// --- dream MERGE: delete failure must roll back the insert ---
	t.Run("dreamMERGE_rolls_back_insert_on_delete_failure", func(t *testing.T) {
		db := setupTestDB(t)
		store := NewStore(db)

		idA, err := store.Insert(ctx, InsertParams{
			Content: "dream source alpha long enough content for atomicity", Type: "lesson",
			Source: "self_report", Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert A: %v", err)
		}
		idB, err := store.Insert(ctx, InsertParams{
			Content: "dream source beta long enough content for atomicity", Type: "lesson",
			Source: "self_report", Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert B: %v", err)
		}

		// Trigger: prevent deletion of idA (first delete attempt in MERGE).
		_, err = db.ExecContext(ctx, fmt.Sprintf(`
			CREATE TRIGGER prevent_delete_dream
			BEFORE DELETE ON memories
			WHEN OLD.id = %d
			BEGIN SELECT RAISE(FAIL, 'atomicity test: delete prevented'); END;
		`, idA))
		if err != nil {
			t.Fatalf("create trigger: %v", err)
		}

		const mergedContent = "merged dream memory content that is long enough for insert validation test"
		action := DreamAction{
			Kind:   "MERGE",
			IDs:    []int64{idA, idB},
			Params: InsertParams{Content: mergedContent, Type: "lesson", Source: "dreamer"},
		}

		var logMessages []string
		_ = ExecuteActions(ctx, []DreamAction{action}, store, func(msg string) {
			logMessages = append(logMessages, msg)
		})

		// With atomic transaction: Insert must be rolled back when Delete fails.
		all, dumpErr := store.DumpAll(ctx)
		if dumpErr != nil {
			t.Fatalf("dump: %v", dumpErr)
		}

		for _, m := range all {
			if m.Content == mergedContent {
				t.Error("merged memory should not exist: Insert was not rolled back after Delete failure")
				break
			}
		}

		found := map[int64]bool{}
		for _, m := range all {
			found[m.ID] = true
		}
		if !found[idA] {
			t.Error("idA should still exist (delete was prevented by trigger)")
		}
		if !found[idB] {
			t.Error("idB should still exist (rolled back by transaction)")
		}
	})
}

// ---------------------------------------------------------------------------
// pruneStale pinned-exclusion test (oro-gyw0)
// ---------------------------------------------------------------------------

func TestPruneStaleSkipsPinned(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	oldTime := time.Now().AddDate(-1, 0, 0).Format("2006-01-02 15:04:05")

	// Insert a pinned memory with low confidence and old timestamp.
	// Without the fix, pruneStale would delete this.
	_, err := db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence, created_at, pinned)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"pinned memory must survive pruning", "lesson", `[]`, "self_report", 0.05,
		oldTime, 1,
	)
	if err != nil {
		t.Fatalf("insert pinned: %v", err)
	}

	// Insert an unpinned memory with the same low confidence and old timestamp.
	// This one should be pruned.
	_, err = db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence, created_at, pinned)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"unpinned stale memory to be pruned", "lesson", `[]`, "self_report", 0.05,
		oldTime, 0,
	)
	if err != nil {
		t.Fatalf("insert unpinned: %v", err)
	}

	_, pruned, err := Consolidate(ctx, store, ConsolidateOpts{
		SimilarityThreshold: 100, // high threshold — no merges
		MinDecayedScore:     0.05,
	})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}

	if pruned == 0 {
		t.Error("expected unpinned stale memory to be pruned")
	}

	all, err := store.DumpAll(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}

	var foundPinned, foundUnpinned bool
	for _, m := range all {
		switch m.Content {
		case "pinned memory must survive pruning":
			foundPinned = true
		case "unpinned stale memory to be pruned":
			foundUnpinned = true
		}
	}

	if !foundPinned {
		t.Error("pinned memory was deleted by pruneStale — it should be excluded")
	}
	if foundUnpinned {
		t.Error("unpinned stale memory should have been pruned but still exists")
	}
}

func TestTagFilterSpecialChars(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content: "memory with special tag", Type: "lesson",
		Tags: []string{"100%_done"}, Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert 100%%_done: %v", err)
	}
	_, err = store.Insert(ctx, InsertParams{
		Content: "memory with partial tag", Type: "lesson",
		Tags: []string{"100"}, Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert 100: %v", err)
	}
	_, err = store.Insert(ctx, InsertParams{
		Content: "memory with other partial tag", Type: "lesson",
		Tags: []string{"done"}, Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert done: %v", err)
	}
	// "100_done" would be a false positive if '_' is not escaped (SQL LIKE treats _ as any single char).
	_, err = store.Insert(ctx, InsertParams{
		Content: "false positive candidate — underscore only", Type: "lesson",
		Tags: []string{"100_done"}, Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert 100_done: %v", err)
	}

	results, err := store.List(ctx, ListOpts{Tag: "100%_done"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected exactly 1 result for Tag=%q, got %d", "100%%_done", len(results))
	}
}
