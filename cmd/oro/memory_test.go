package main

import (
	"context"
	"database/sql"
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
