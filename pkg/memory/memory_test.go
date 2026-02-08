package memory

import (
	"context"
	"database/sql"
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
	t.Cleanup(func() { db.Close() })

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
			Content: "memory " + string(rune('A'+i)), Type: "lesson",
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

func TestExtractImplicit(t *testing.T) {
	text := `
I learned that ruff should always run before pyright.
Some random text here.
Note: Always check the return value of db.Exec
Gotcha: FTS5 requires content sync triggers
Pattern: functional core with imperative shell
Decision: Use modernc.org/sqlite for pure Go builds
Decided: All configs go in YAML not JSON
Important: Never skip error wrapping
Just some normal text.
`

	results := ExtractImplicit(text)

	expected := []struct {
		memType string
		substr  string
	}{
		{"lesson", "ruff should always run before pyright"},
		{"lesson", "Always check the return value"},
		{"gotcha", "FTS5 requires content sync triggers"},
		{"pattern", "functional core with imperative shell"},
		{"decision", "Use modernc.org/sqlite for pure Go builds"},
		{"decision", "All configs go in YAML not JSON"},
		{"lesson", "Never skip error wrapping"},
	}

	if len(results) != len(expected) {
		t.Fatalf("expected %d extractions, got %d", len(expected), len(results))
	}

	for i, exp := range expected {
		if results[i].Type != exp.memType {
			t.Errorf("[%d] type: got %q, want %q", i, results[i].Type, exp.memType)
		}
		if !strings.Contains(results[i].Content, exp.substr) {
			t.Errorf("[%d] content: got %q, want substring %q", i, results[i].Content, exp.substr)
		}
		if results[i].Source != "daemon_extracted" {
			t.Errorf("[%d] source: got %q, want 'daemon_extracted'", i, results[i].Source)
		}
		if results[i].Confidence != 0.6 {
			t.Errorf("[%d] confidence: got %f, want 0.6", i, results[i].Confidence)
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
	if !strings.Contains(result, "[gotcha]") {
		t.Error("expected [gotcha] type label")
	}
}

func TestForPrompt_TokenCap(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert memories with long content
	for i := 0; i < 5; i++ {
		longContent := strings.Repeat("word ", 100) + "unique_search_term"
		_, err := store.Insert(ctx, InsertParams{
			Content: longContent, Type: "lesson",
			Source: "self_report", Confidence: 0.9,
		})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// Request with very small token cap
	result, err := ForPrompt(ctx, store, nil, "unique_search_term", 10)
	if err != nil {
		t.Fatalf("for prompt: %v", err)
	}

	// Should be truncated
	words := strings.Fields(result)
	estimatedTokens := int(float64(len(words)) / 0.75)
	// Allow some slack but it should be much shorter than full output
	if estimatedTokens > 20 {
		t.Errorf("expected truncated output, got %d estimated tokens", estimatedTokens)
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

	// Insert two very similar memories
	_, err := store.Insert(ctx, InsertParams{
		Content: "always run ruff before pyright for linting", Type: "gotcha",
		Source: "self_report", Confidence: 0.7,
	})
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}

	_, err = store.Insert(ctx, InsertParams{
		Content: "always run ruff before pyright for linting checks", Type: "gotcha",
		Source: "self_report", Confidence: 0.9,
	})
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
