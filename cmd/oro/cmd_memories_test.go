package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/memory"
	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// newTestMemoryStoreAndDB creates an in-memory SQLite-backed memory.Store for testing.
// Returns both the raw *sql.DB (for inserting records with specific timestamps)
// and the memory.Store. Uses t.Cleanup to close the database.
func newTestMemoryStoreAndDB(t *testing.T) (*sql.DB, *memory.Store) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
	return db, memory.NewStore(db)
}

// seedMemoriesCmd inserts a standard set of memories for list tests.
func seedMemoriesCmd(t *testing.T, store *memory.Store) {
	t.Helper()
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
}

func TestMemoriesCmd(t *testing.T) {
	t.Run("list_default_returns_up_to_limit_20", func(t *testing.T) {
		_, store := newTestMemoryStoreAndDB(t)
		seedMemoriesCmd(t, store)

		cmd := newMemoriesListCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}

		output := out.String()
		// Header must be present.
		if !strings.Contains(output, "ID") {
			t.Errorf("expected header with ID column, got:\n%s", output)
		}
		if !strings.Contains(output, "TYPE") {
			t.Errorf("expected header with TYPE column, got:\n%s", output)
		}
		if !strings.Contains(output, "CONTENT") {
			t.Errorf("expected header with CONTENT column, got:\n%s", output)
		}

		// All 5 seeded memories should appear (well under 20 limit).
		for _, needle := range []string{
			"always run tests",
			"decided to use FTS5",
			"modernc sqlite",
			"table-driven tests",
			"worker memory usage",
		} {
			if !strings.Contains(output, needle) {
				t.Errorf("expected output to contain %q, got:\n%s", needle, output)
			}
		}

		// Count data rows (skip header line).
		lines := strings.Split(strings.TrimSpace(output), "\n")
		dataLines := 0
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "ID") {
				continue
			}
			dataLines++
		}
		if dataLines != 5 {
			t.Errorf("expected 5 data lines, got %d", dataLines)
		}
	})

	t.Run("list_type_filters_by_type", func(t *testing.T) {
		_, store := newTestMemoryStoreAndDB(t)
		seedMemoriesCmd(t, store)

		cmd := newMemoriesListCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--type", "gotcha"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "modernc sqlite") {
			t.Errorf("expected gotcha memory in output, got:\n%s", output)
		}
		// Other types must not appear.
		if strings.Contains(output, "always run tests") {
			t.Errorf("expected no lesson memories when filtering by gotcha, got:\n%s", output)
		}
		if strings.Contains(output, "decided to use FTS5") {
			t.Errorf("expected no decision memories when filtering by gotcha, got:\n%s", output)
		}
	})

	t.Run("list_tag_filters_by_tag", func(t *testing.T) {
		_, store := newTestMemoryStoreAndDB(t)
		seedMemoriesCmd(t, store)

		cmd := newMemoriesListCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--tag", "performance"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "worker memory usage") {
			t.Errorf("expected performance-tagged memory, got:\n%s", output)
		}
		if strings.Contains(output, "decided to use FTS5") {
			t.Errorf("expected no search-tagged memories when filtering by performance, got:\n%s", output)
		}
	})

	t.Run("list_limit_overrides_default", func(t *testing.T) {
		_, store := newTestMemoryStoreAndDB(t)
		seedMemoriesCmd(t, store)

		cmd := newMemoriesListCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--limit", "2"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
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

	t.Run("consolidate_runs_and_prints_summary", func(t *testing.T) {
		_, store := newTestMemoryStoreAndDB(t)
		ctx := context.Background()

		// Insert a fresh memory that will survive consolidation.
		if _, err := store.Insert(ctx, memory.InsertParams{
			Content: "fresh memory survives consolidation", Type: "lesson",
			Source: "cli", Confidence: 0.9,
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}

		cmd := newMemoriesConsolidateCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "Consolidation complete") {
			t.Errorf("expected 'Consolidation complete', got:\n%s", output)
		}
		if !strings.Contains(output, "Pruned:") {
			t.Errorf("expected 'Pruned:' in output, got:\n%s", output)
		}
		if !strings.Contains(output, "Merged:") {
			t.Errorf("expected 'Merged:' in output, got:\n%s", output)
		}
		if !strings.Contains(output, "Remaining:") {
			t.Errorf("expected 'Remaining:' in output, got:\n%s", output)
		}
	})

	t.Run("consolidate_dry_run_prints_DRY_RUN_without_modifying_store", func(t *testing.T) {
		db, store := newTestMemoryStoreAndDB(t)
		ctx := context.Background()

		// Insert a very old, low-confidence memory that would be pruned.
		_, err := db.Exec(
			`INSERT INTO memories (content, type, tags, source, confidence, created_at)
			 VALUES (?, ?, ?, ?, ?, datetime('now', '-1 year'))`,
			"stale dry run memory xyzzy", "lesson", "[]", "self_report", 0.1,
		)
		if err != nil {
			t.Fatalf("insert old: %v", err)
		}
		// Sync FTS5 index for the raw insert.
		_, err = db.Exec(
			`INSERT INTO memories_fts(rowid, content, tags) VALUES (last_insert_rowid(), ?, ?)`,
			"stale dry run memory xyzzy", "[]",
		)
		if err != nil {
			t.Fatalf("sync fts: %v", err)
		}

		countBefore, err := store.List(ctx, memory.ListOpts{})
		if err != nil {
			t.Fatalf("list before: %v", err)
		}

		cmd := newMemoriesConsolidateCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--dry-run"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "DRY RUN") {
			t.Errorf("expected 'DRY RUN' indicator, got:\n%s", output)
		}

		// Store must be unchanged after dry run.
		countAfter, err := store.List(ctx, memory.ListOpts{})
		if err != nil {
			t.Fatalf("list after: %v", err)
		}
		if len(countAfter) != len(countBefore) {
			t.Errorf("dry run modified store: before=%d after=%d", len(countBefore), len(countAfter))
		}
	})

	t.Run("truncateContent_at_boundary", func(t *testing.T) {
		// Exact length: should not be truncated.
		exact := strings.Repeat("a", 10)
		got := truncateContent(exact, 10)
		if got != exact {
			t.Errorf("exact length should not be truncated, got: %q", got)
		}

		// Over length: should be truncated with "...".
		over := strings.Repeat("b", 15)
		got = truncateContent(over, 10)
		if len(got) != 13 { // 10 + len("...")
			t.Errorf("expected length 13 (10+...), got %d", len(got))
		}
		if !strings.HasSuffix(got, "...") {
			t.Errorf("expected truncated string to end with '...', got: %q", got)
		}
		if got[:10] != strings.Repeat("b", 10) {
			t.Errorf("expected first 10 chars to be 'b', got: %q", got[:10])
		}

		// Under length: should not be truncated.
		under := "short"
		got = truncateContent(under, 10)
		if got != under {
			t.Errorf("under length should not be truncated, got: %q", got)
		}

		// Also verify with the production maxLen (60) used in formatMemoriesTable.
		exact60 := strings.Repeat("c", 60)
		got = truncateContent(exact60, 60)
		if got != exact60 {
			t.Errorf("exact 60-char string should not be truncated, got: %q", got)
		}
	})

	t.Run("empty_list_prints_no_memories_found", func(t *testing.T) {
		_, store := newTestMemoryStoreAndDB(t)

		cmd := newMemoriesListCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "No memories found") {
			t.Errorf("expected 'No memories found' for empty store, got:\n%s", output)
		}
	})

	t.Run("consolidate_with_no_memories_zero_pruned_zero_merged", func(t *testing.T) {
		_, store := newTestMemoryStoreAndDB(t)

		cmd := newMemoriesConsolidateCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "Pruned:    0") {
			t.Errorf("expected 'Pruned:    0' for empty store, got:\n%s", output)
		}
		if !strings.Contains(output, "Merged:    0") {
			t.Errorf("expected 'Merged:    0' for empty store, got:\n%s", output)
		}
		if !strings.Contains(output, "Remaining: 0") {
			t.Errorf("expected 'Remaining: 0' for empty store, got:\n%s", output)
		}
	})
}

// TestCLIAndDispatcherUseSameDB verifies that defaultMemoryStore() opens the
// same database that the dispatcher and workers use (StateDBPath / state.db),
// not the legacy MemoryDBPath (memories.db). This guards against the split
// where workers insert memories into state.db but the CLI reads memories.db.
func TestCLIAndDispatcherUseSameDB(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", tmpDir)

	// Resolve paths the same way the dispatcher does.
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatalf("resolve paths: %v", err)
	}

	// Verify the dispatcher DB lives at state.db (sanity check on test setup).
	wantStateDB := filepath.Join(tmpDir, "state.db")
	if paths.StateDBPath != wantStateDB {
		t.Fatalf("StateDBPath = %q, want %q", paths.StateDBPath, wantStateDB)
	}

	// Simulate the dispatcher: open state.db and insert a memory.
	dispDB, err := sql.Open("sqlite", paths.StateDBPath)
	if err != nil {
		t.Fatalf("open dispatcher db: %v", err)
	}
	t.Cleanup(func() { _ = dispDB.Close() })

	if _, err := dispDB.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	dispStore := memory.NewStore(dispDB)
	ctx := context.Background()
	id, err := dispStore.Insert(ctx, memory.InsertParams{
		Content:    "dispatcher memory visible to CLI",
		Type:       "lesson",
		Source:     "test",
		Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert via dispatcher store: %v", err)
	}

	// Simulate the CLI: open the store via defaultMemoryStore().
	cliStore, err := defaultMemoryStore()
	if err != nil {
		t.Fatalf("defaultMemoryStore: %v", err)
	}

	mems, err := cliStore.List(ctx, memory.ListOpts{})
	if err != nil {
		t.Fatalf("list via CLI store: %v", err)
	}

	for _, m := range mems {
		if m.ID == id {
			return // memory is visible: test passes
		}
	}
	t.Errorf("memory inserted via dispatcher store (id=%d) not visible via CLI store (got %d memories) — CLI and dispatcher use different DBs", id, len(mems))
}
