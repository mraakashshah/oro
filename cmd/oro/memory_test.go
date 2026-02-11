package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"oro/pkg/memory"
	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// setupTestMemoryDB creates an in-memory SQLite database with the full schema.
func setupTestMemoryDB(t *testing.T) *sql.DB {
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

func TestRememberRecall(t *testing.T) {
	db := setupTestMemoryDB(t)
	store := memory.NewStore(db)
	ctx := context.Background()

	t.Run("remember inserts to SQLite", func(t *testing.T) {
		cmd := newRememberCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"always use TDD when writing Go code"})

		err := cmd.Execute()
		if err != nil {
			t.Fatalf("remember execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "Remembered") {
			t.Errorf("expected output to contain 'Remembered', got: %s", output)
		}

		// Verify it was actually inserted into the store
		results, err := store.Search(ctx, "TDD Go code", memory.SearchOpts{Limit: 5})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected at least one memory after remember")
		}
		if !strings.Contains(results[0].Content, "TDD") {
			t.Errorf("expected memory to contain 'TDD', got: %s", results[0].Content)
		}
		if results[0].Source != "cli" {
			t.Errorf("expected source='cli', got: %s", results[0].Source)
		}
	})

	t.Run("remember with type prefix", func(t *testing.T) {
		cmd := newRememberCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"gotcha:", "FTS5 requires content sync triggers"})

		err := cmd.Execute()
		if err != nil {
			t.Fatalf("remember execute: %v", err)
		}

		results, err := store.Search(ctx, "FTS5 content sync triggers", memory.SearchOpts{Limit: 5})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected at least one memory for gotcha prefix")
		}
		if results[0].Type != "gotcha" {
			t.Errorf("expected type='gotcha', got: %s", results[0].Type)
		}
	})

	t.Run("recall searches FTS5 and prints results", func(t *testing.T) {
		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"TDD"})

		err := cmd.Execute()
		if err != nil {
			t.Fatalf("recall execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "TDD") {
			t.Errorf("expected recall output to contain 'TDD', got: %s", output)
		}
	})

	t.Run("recall with no results", func(t *testing.T) {
		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"xyzzy_nonexistent_query_12345"})

		err := cmd.Execute()
		if err != nil {
			t.Fatalf("recall execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "No memories found") {
			t.Errorf("expected 'No memories found' message, got: %s", output)
		}
	})

	t.Run("remember default type is self_report", func(t *testing.T) {
		cmd := newRememberCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"unique_default_type_test_zqwerty"})

		err := cmd.Execute()
		if err != nil {
			t.Fatalf("remember execute: %v", err)
		}

		results, err := store.Search(ctx, "unique_default_type_test_zqwerty", memory.SearchOpts{Limit: 1})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected at least one memory")
		}
		if results[0].Type != "self_report" {
			t.Errorf("expected default type='self_report', got: %s", results[0].Type)
		}
	})

	t.Run("recall output includes type and confidence", func(t *testing.T) {
		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"FTS5"})

		err := cmd.Execute()
		if err != nil {
			t.Fatalf("recall execute: %v", err)
		}

		output := out.String()
		// Should show type label and confidence
		if !strings.Contains(output, "gotcha") {
			t.Errorf("expected recall output to contain type 'gotcha', got: %s", output)
		}
		if !strings.Contains(output, "confidence") || !strings.Contains(output, "0.") {
			t.Errorf("expected recall output to contain confidence score, got: %s", output)
		}
	})
}

func TestMemoriesList(t *testing.T) {
	db := setupTestMemoryDB(t)
	store := memory.NewStore(db)
	ctx := context.Background()

	seeds := []memory.InsertParams{
		{Content: "always run tests before committing", Type: "lesson", Tags: []string{"testing", "ci"}, Source: "cli", Confidence: 0.9},
		{Content: "decided to use FTS5 for search", Type: "decision", Tags: []string{"search"}, Source: "cli", Confidence: 0.8},
		{Content: "modernc sqlite does not support bm25 well", Type: "gotcha", Tags: []string{"search", "sqlite"}, Source: "daemon_extracted", Confidence: 0.7},
		{Content: "use table-driven tests in Go", Type: "pattern", Tags: []string{"testing", "go"}, Source: "cli", Confidence: 0.85},
		{Content: "worker memory usage spikes after 50 beads", Type: "self_report", Tags: []string{"performance"}, Source: "daemon_extracted", Confidence: 0.6},
	}
	for _, s := range seeds {
		if _, err := store.Insert(ctx, s); err != nil {
			t.Fatalf("seed insert: %v", err)
		}
	}

	t.Run("lists all memories with default limit", func(t *testing.T) {
		cmd := newMemoriesListCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("memories list execute: %v", err)
		}
		output := out.String()
		if !strings.Contains(output, "ID") {
			t.Errorf("expected header with ID column, got: %s", output)
		}
		if !strings.Contains(output, "TYPE") {
			t.Errorf("expected header with TYPE column, got: %s", output)
		}
		if !strings.Contains(output, "CONTENT") {
			t.Errorf("expected header with CONTENT column, got: %s", output)
		}
		for _, s := range seeds {
			snippet := s.Content
			if len(snippet) > 20 {
				snippet = snippet[:20]
			}
			if !strings.Contains(output, snippet) {
				t.Errorf("expected output to contain %q, got: %s", snippet, output)
			}
		}
	})

	t.Run("filter by type", func(t *testing.T) {
		cmd := newMemoriesListCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--type", "gotcha"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("memories list --type execute: %v", err)
		}
		output := out.String()
		if !strings.Contains(output, "modernc sqlite") {
			t.Errorf("expected gotcha memory in output, got: %s", output)
		}
		if strings.Contains(output, "always run tests") {
			t.Errorf("expected no lesson memories when filtering by gotcha, got: %s", output)
		}
	})

	t.Run("filter by tag", func(t *testing.T) {
		cmd := newMemoriesListCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--tag", "performance"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("memories list --tag execute: %v", err)
		}
		output := out.String()
		if !strings.Contains(output, "worker memory usage") {
			t.Errorf("expected performance-tagged memory, got: %s", output)
		}
		if strings.Contains(output, "decided to use FTS5") {
			t.Errorf("expected no search-tagged memories when filtering by performance, got: %s", output)
		}
	})

	t.Run("limit flag restricts count", func(t *testing.T) {
		cmd := newMemoriesListCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--limit", "2"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("memories list --limit execute: %v", err)
		}
		output := out.String()
		lines := strings.Split(strings.TrimSpace(output), "\n")
		dataLines := 0
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "ID") {
				continue
			}
			dataLines++
		}
		if dataLines != 2 {
			t.Errorf("expected 2 data lines with --limit=2, got %d lines:\n%s", dataLines, output)
		}
	})

	t.Run("empty result prints message", func(t *testing.T) {
		cmd := newMemoriesListCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--type", "nonexistent_type"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("memories list --type nonexistent execute: %v", err)
		}
		output := out.String()
		if !strings.Contains(output, "No memories found") {
			t.Errorf("expected 'No memories found' for empty result, got: %s", output)
		}
	})

	t.Run("content is truncated for long entries", func(t *testing.T) {
		longContent := strings.Repeat("abcdefghij", 10)
		if _, err := store.Insert(ctx, memory.InsertParams{
			Content: longContent, Type: "lesson", Source: "cli", Confidence: 0.5,
		}); err != nil {
			t.Fatalf("insert long memory: %v", err)
		}
		cmd := newMemoriesListCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--type", "lesson"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("memories list execute: %v", err)
		}
		output := out.String()
		if strings.Contains(output, longContent) {
			t.Errorf("expected long content to be truncated, but found full string in output")
		}
		if !strings.Contains(output, "abcdefghij") {
			t.Errorf("expected truncated content to start with prefix, got: %s", output)
		}
	})
}

func TestConsolidate(t *testing.T) {
	t.Run("removes stale memories and reports stats", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)
		ctx := context.Background()
		// Insert a very old memory with low confidence that should be pruned
		// After 1 year (365 days), decayed score = 0.1 * 0.5^(365/30) = 0.1 * 0.5^12.17 ≈ 0.0002
		// This is well below default threshold of 0.1
		_, err := db.Exec(
			`INSERT INTO memories (content, type, tags, source, confidence, created_at)
			 VALUES (?, ?, ?, ?, ?, datetime('now', '-1 year'))`,
			"unique_xyzzy_stale_content_12345", "lesson", "[]", "self_report", 0.1,
		)
		if err != nil {
			t.Fatalf("insert old memory: %v", err)
		}

		// Insert two similar memories to test merging
		_, err = db.Exec(
			`INSERT INTO memories (content, type, tags, source, confidence)
			 VALUES (?, ?, ?, ?, ?)`,
			"always run tests before commit", "lesson", "[]", "cli", 0.8,
		)
		if err != nil {
			t.Fatalf("insert similar 1: %v", err)
		}

		_, err = db.Exec(
			`INSERT INTO memories (content, type, tags, source, confidence)
			 VALUES (?, ?, ?, ?, ?)`,
			"always run tests before committing code", "lesson", "[]", "cli", 0.7,
		)
		if err != nil {
			t.Fatalf("insert similar 2: %v", err)
		}

		// Insert a fresh memory with good confidence that should survive
		_, err = store.Insert(ctx, memory.InsertParams{
			Content:    "fresh memory with good confidence",
			Type:       "lesson",
			Source:     "cli",
			Confidence: 0.9,
		})
		if err != nil {
			t.Fatalf("insert fresh: %v", err)
		}

		// Run consolidate command
		cmd := newMemoriesConsolidateCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{})

		err = cmd.Execute()
		if err != nil {
			t.Fatalf("consolidate execute: %v", err)
		}

		output := out.String()
		t.Logf("Consolidate output:\n%s", output)

		// Verify output contains stats
		if !strings.Contains(output, "Consolidation complete") {
			t.Errorf("expected 'Consolidation complete' in output, got: %s", output)
		}
		if !strings.Contains(output, "Pruned:") {
			t.Errorf("expected 'Pruned:' in output, got: %s", output)
		}
		if !strings.Contains(output, "Merged:") {
			t.Errorf("expected 'Merged:' in output, got: %s", output)
		}
		if !strings.Contains(output, "Remaining:") {
			t.Errorf("expected 'Remaining:' in output, got: %s", output)
		}

		// List all remaining memories to debug
		allMemories, err := store.List(ctx, memory.ListOpts{})
		if err != nil {
			t.Fatalf("list all memories: %v", err)
		}
		t.Logf("Remaining memories after consolidate:")
		for _, m := range allMemories {
			t.Logf("  ID=%d, Content=%q, Confidence=%.2f, Created=%s",
				m.ID, m.Content, m.Confidence, m.CreatedAt)
		}

		// Verify stale memory was removed
		results, err := store.Search(ctx, "unique_xyzzy_stale_content_12345", memory.SearchOpts{Limit: 5})
		if err != nil {
			t.Fatalf("search after consolidate: %v", err)
		}
		if len(results) > 0 {
			t.Errorf("expected stale memory to be pruned, but found %d results: %+v", len(results), results)
		}

		// Verify fresh memory survived
		results, err = store.Search(ctx, "fresh memory with good confidence", memory.SearchOpts{Limit: 5})
		if err != nil {
			t.Fatalf("search fresh: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected fresh memory to survive consolidation")
		}
	})

	t.Run("dry run shows stats without modifying", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)
		ctx := context.Background()

		// Insert an old memory
		_, err := db.Exec(
			`INSERT INTO memories (content, type, tags, source, confidence, created_at)
			 VALUES (?, ?, ?, ?, ?, datetime('now', '-1 year'))`,
			"old memory for dry run test", "lesson", "[]", "self_report", 0.1,
		)
		if err != nil {
			t.Fatalf("insert old: %v", err)
		}

		countBefore, err := store.List(ctx, memory.ListOpts{})
		if err != nil {
			t.Fatalf("list before: %v", err)
		}

		// Run consolidate with dry-run flag
		cmd := newMemoriesConsolidateCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--dry-run"})

		err = cmd.Execute()
		if err != nil {
			t.Fatalf("consolidate dry-run execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "DRY RUN") {
			t.Errorf("expected dry run indicator in output, got: %s", output)
		}

		// Verify no memories were actually removed
		countAfter, err := store.List(ctx, memory.ListOpts{})
		if err != nil {
			t.Fatalf("list after: %v", err)
		}
		if len(countAfter) != len(countBefore) {
			t.Errorf("expected same count in dry run, before=%d after=%d", len(countBefore), len(countAfter))
		}
	})

	t.Run("custom threshold flags work", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)

		cmd := newMemoriesConsolidateCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--min-score", "0.05", "--similarity", "0.9"})

		err := cmd.Execute()
		if err != nil {
			t.Fatalf("consolidate with flags execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "Consolidation complete") {
			t.Errorf("expected success output, got: %s", output)
		}
	})
}

func TestForget(t *testing.T) {
	db := setupTestMemoryDB(t)
	store := memory.NewStore(db)
	ctx := context.Background()

	t.Run("deletes memory by id", func(t *testing.T) {
		id, err := store.Insert(ctx, memory.InsertParams{
			Content:    "forget me test memory unique_forget_abc",
			Type:       "lesson",
			Source:     "cli",
			Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}

		cmd := newForgetCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{fmt.Sprintf("%d", id)})

		err = cmd.Execute()
		if err != nil {
			t.Fatalf("forget execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "Forgot") {
			t.Errorf("expected output to contain 'Forgot', got: %s", output)
		}
		if !strings.Contains(output, fmt.Sprintf("%d", id)) {
			t.Errorf("expected output to contain id %d, got: %s", id, output)
		}

		// Verify the memory is gone
		results, err := store.Search(ctx, "unique_forget_abc", memory.SearchOpts{Limit: 5})
		if err != nil {
			t.Fatalf("search after forget: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results after forget, got %d", len(results))
		}
	})

	t.Run("deletes multiple memories by id", func(t *testing.T) {
		id1, err := store.Insert(ctx, memory.InsertParams{
			Content: "multi forget first unique_mf1", Type: "lesson",
			Source: "cli", Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert 1: %v", err)
		}
		id2, err := store.Insert(ctx, memory.InsertParams{
			Content: "multi forget second unique_mf2", Type: "lesson",
			Source: "cli", Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert 2: %v", err)
		}

		cmd := newForgetCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{fmt.Sprintf("%d", id1), fmt.Sprintf("%d", id2)})

		err = cmd.Execute()
		if err != nil {
			t.Fatalf("forget execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, fmt.Sprintf("%d", id1)) {
			t.Errorf("expected output to contain id %d, got: %s", id1, output)
		}
		if !strings.Contains(output, fmt.Sprintf("%d", id2)) {
			t.Errorf("expected output to contain id %d, got: %s", id2, output)
		}

		results1, _ := store.Search(ctx, "unique_mf1", memory.SearchOpts{Limit: 5})
		results2, _ := store.Search(ctx, "unique_mf2", memory.SearchOpts{Limit: 5})
		if len(results1) != 0 || len(results2) != 0 {
			t.Errorf("expected 0 results after forget, got %d and %d", len(results1), len(results2))
		}
	})

	t.Run("returns error for nonexistent id", func(t *testing.T) {
		cmd := newForgetCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"999999"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for nonexistent id, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected error to contain 'not found', got: %s", err.Error())
		}
	})

	t.Run("returns error for invalid id", func(t *testing.T) {
		cmd := newForgetCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"notanumber"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for invalid id, got nil")
		}
	})
}

func TestRecallByID(t *testing.T) {
	db := setupTestMemoryDB(t)
	store := memory.NewStore(db)
	ctx := context.Background()

	// Insert a memory
	id, err := store.Insert(ctx, memory.InsertParams{
		Content:    "Full detailed memory content for recall by ID test",
		Type:       "lesson",
		Tags:       []string{"test", "recall"},
		Source:     "self_report",
		Confidence: 0.85,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	t.Run("recall by id returns full memory content", func(t *testing.T) {
		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--id", fmt.Sprintf("%d", id)})

		err := cmd.Execute()
		if err != nil {
			t.Fatalf("recall --id execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "Full detailed memory content for recall by ID test") {
			t.Errorf("expected full memory content, got: %s", output)
		}
		if !strings.Contains(output, "[lesson]") {
			t.Errorf("expected type label, got: %s", output)
		}
		if !strings.Contains(output, "0.85") {
			t.Errorf("expected confidence 0.85, got: %s", output)
		}
	})

	t.Run("recall by id with nonexistent id returns error", func(t *testing.T) {
		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--id", "99999"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for nonexistent ID")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found' error, got: %v", err)
		}
	})

	t.Run("recall cannot use both query and id", func(t *testing.T) {
		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--id", fmt.Sprintf("%d", id), "some query"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error when using both --id and query")
		}
		if !strings.Contains(err.Error(), "cannot use both") {
			t.Errorf("expected 'cannot use both' error, got: %v", err)
		}
	})
}

func TestPinnedMemoryCLI(t *testing.T) {
	db := setupTestMemoryDB(t)
	store := memory.NewStore(db)
	ctx := context.Background()

	t.Run("remember with --pin flag creates pinned memory", func(t *testing.T) {
		cmd := newRememberCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--pin", "never cd into worktrees"})

		err := cmd.Execute()
		if err != nil {
			t.Fatalf("remember --pin execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "[pinned]") {
			t.Errorf("expected output to contain '[pinned]', got: %s", output)
		}
		if !strings.Contains(output, "Remembered") {
			t.Errorf("expected output to contain 'Remembered', got: %s", output)
		}

		// Verify the memory is actually pinned in the database
		results, err := store.Search(ctx, "never cd into worktrees", memory.SearchOpts{Limit: 5})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected at least one memory")
		}
		if !results[0].Pinned {
			t.Error("expected memory to be pinned=true")
		}
	})

	t.Run("recall shows [pinned] indicator for pinned memories", func(t *testing.T) {
		// The memory should already exist from the previous test
		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"never cd into worktrees"})

		err := cmd.Execute()
		if err != nil {
			t.Fatalf("recall execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "[pinned]") {
			t.Errorf("expected recall output to show '[pinned]' indicator, got: %s", output)
		}
	})

	t.Run("recall by id shows [pinned] indicator", func(t *testing.T) {
		// Insert a new pinned memory
		id, err := store.Insert(ctx, memory.InsertParams{
			Content:    "unique_pin_recall_id_test always backup before deploy",
			Type:       "gotcha",
			Source:     "cli",
			Confidence: 0.9,
			Pinned:     true,
		})
		if err != nil {
			t.Fatalf("insert pinned: %v", err)
		}

		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--id", fmt.Sprintf("%d", id)})

		err = cmd.Execute()
		if err != nil {
			t.Fatalf("recall --id execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "[pinned]") {
			t.Errorf("expected recall --id output to show '[pinned]' indicator, got: %s", output)
		}
		if !strings.Contains(output, "[gotcha]") {
			t.Errorf("expected type label, got: %s", output)
		}
	})

	t.Run("unpinned memory does not show [pinned] indicator", func(t *testing.T) {
		// Insert an unpinned memory
		_, err := store.Insert(ctx, memory.InsertParams{
			Content:    "unique_unpin_test regular unpinned memory",
			Type:       "lesson",
			Source:     "cli",
			Confidence: 0.8,
			Pinned:     false,
		})
		if err != nil {
			t.Fatalf("insert unpinned: %v", err)
		}

		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"unique_unpin_test"})

		err = cmd.Execute()
		if err != nil {
			t.Fatalf("recall execute: %v", err)
		}

		output := out.String()
		lines := strings.Split(output, "\n")
		for _, line := range lines {
			if strings.Contains(line, "unique_unpin_test") && strings.Contains(line, "[pinned]") {
				t.Errorf("unpinned memory should not show '[pinned]' indicator, got line: %s", line)
			}
		}
	})
}
