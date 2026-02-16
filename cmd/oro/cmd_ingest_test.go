package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/memory"
	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// newTestMemoryStoreForIngest creates an in-memory SQLite store for ingest tests.
func newTestMemoryStoreForIngest(t *testing.T) *memory.Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
	return memory.NewStore(db)
}

func TestCmdIngest_DryRun(t *testing.T) {
	// Create a test knowledge.jsonl file
	tmpDir := t.TempDir()
	knowledgeFile := filepath.Join(tmpDir, "knowledge.jsonl")
	content := `{"key":"test-1","type":"lesson","content":"Always write tests first","bead":"test-bead","tags":["tdd"],"ts":"2024-01-15T10:00:00Z"}
{"key":"test-2","type":"decision","content":"Use Go for backend services","bead":"test-bead","tags":["architecture"],"ts":"2024-01-15T11:00:00Z"}
`
	if err := os.WriteFile(knowledgeFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	store := newTestMemoryStoreForIngest(t)
	cmd := newIngestCmdWithStore(store)

	// Set flags for dry-run
	if err := cmd.Flags().Set("file", knowledgeFile); err != nil {
		t.Fatalf("set file flag: %v", err)
	}
	if err := cmd.Flags().Set("dry-run", "true"); err != nil {
		t.Fatalf("set dry-run flag: %v", err)
	}

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	// Execute command
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute ingest dry-run: %v", err)
	}

	// Verify output mentions dry-run and shows entries
	output := out.String()
	if !strings.Contains(output, "dry-run") && !strings.Contains(output, "would ingest") {
		t.Errorf("expected dry-run indication in output, got: %s", output)
	}

	// Verify no entries were inserted (dry-run mode)
	ctx := context.Background()
	results, err := store.Search(ctx, "test", memory.SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("search after dry-run: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("dry-run inserted %d entries, expected 0", len(results))
	}
}

func TestCmdIngest_Integration(t *testing.T) {
	// Create a test knowledge.jsonl file
	tmpDir := t.TempDir()
	knowledgeFile := filepath.Join(tmpDir, "knowledge.jsonl")
	content := `{"key":"integration-1","type":"lesson","content":"Pure functions are easier to test","bead":"oro-test","tags":["functional","testing"],"ts":"2024-01-15T12:00:00Z"}
{"key":"integration-2","type":"gotcha","content":"SQLite PRAGMA statements must use ExecContext","bead":"oro-test","tags":["sqlite"],"ts":"2024-01-15T13:00:00Z"}
{"key":"integration-3","type":"pattern","content":"Use context.Background() for CLI commands","bead":"oro-test","tags":["go"],"ts":"2024-01-15T14:00:00Z"}
`
	if err := os.WriteFile(knowledgeFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	store := newTestMemoryStoreForIngest(t)
	cmd := newIngestCmdWithStore(store)

	// Set file flag
	if err := cmd.Flags().Set("file", knowledgeFile); err != nil {
		t.Fatalf("set file flag: %v", err)
	}

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	// Execute command
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute ingest: %v", err)
	}

	// Verify output reports ingested count
	output := out.String()
	if !strings.Contains(output, "ingested") && !strings.Contains(output, "3") {
		t.Errorf("expected ingestion confirmation in output, got: %s", output)
	}

	// Verify entries were inserted by searching for each one
	ctx := context.Background()

	// Search for first entry
	results1, err := store.Search(ctx, "Pure functions", memory.SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("search after ingest: %v", err)
	}
	if len(results1) != 1 {
		t.Errorf("expected 1 entry for 'Pure functions', got %d", len(results1))
	} else if results1[0].Type != "lesson" {
		t.Errorf("expected type 'lesson', got '%s'", results1[0].Type)
	}

	// Search for second entry
	results2, err := store.Search(ctx, "SQLite PRAGMA", memory.SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("search for second entry: %v", err)
	}
	if len(results2) != 1 {
		t.Errorf("expected 1 entry for 'SQLite PRAGMA', got %d", len(results2))
	} else if results2[0].Type != "gotcha" {
		t.Errorf("expected type 'gotcha', got '%s'", results2[0].Type)
	}

	// Search for third entry
	results3, err := store.Search(ctx, "context.Background", memory.SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("search for third entry: %v", err)
	}
	if len(results3) != 1 {
		t.Errorf("expected 1 entry for 'context.Background', got %d", len(results3))
	} else if results3[0].Type != "pattern" {
		t.Errorf("expected type 'pattern', got '%s'", results3[0].Type)
	}
}

func TestCmdIngest_FileFlag(t *testing.T) {
	tmpDir := t.TempDir()
	knowledgeFile := filepath.Join(tmpDir, "custom.jsonl")
	content := `{"key":"flag-test","type":"lesson","content":"Test file flag","bead":"oro-test","tags":[],"ts":"2024-01-15T15:00:00Z"}
`
	if err := os.WriteFile(knowledgeFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	store := newTestMemoryStoreForIngest(t)
	cmd := newIngestCmdWithStore(store)

	if err := cmd.Flags().Set("file", knowledgeFile); err != nil {
		t.Fatalf("set file flag: %v", err)
	}

	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute ingest with file flag: %v", err)
	}

	// Verify ingestion happened
	ctx := context.Background()
	results, err := store.Search(ctx, "Test file flag", memory.SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 entry, got %d", len(results))
	}
}

func TestCmdIngest_EnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	knowledgeFile := filepath.Join(tmpDir, "env.jsonl")
	content := `{"key":"env-test","type":"decision","content":"Test env var","bead":"oro-test","tags":[],"ts":"2024-01-15T16:00:00Z"}
`
	if err := os.WriteFile(knowledgeFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Set environment variable
	t.Setenv("ORO_KNOWLEDGE_FILE", knowledgeFile)

	store := newTestMemoryStoreForIngest(t)
	cmd := newIngestCmdWithStore(store)

	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute ingest with env var: %v", err)
	}

	// Verify ingestion
	ctx := context.Background()
	results, err := store.Search(ctx, "Test env var", memory.SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 entry, got %d", len(results))
	}
}

func TestCmdIngest_MissingFile(t *testing.T) {
	store := newTestMemoryStoreForIngest(t)
	cmd := newIngestCmdWithStore(store)

	// Point to a non-existent file
	if err := cmd.Flags().Set("file", "/nonexistent/knowledge.jsonl"); err != nil {
		t.Fatalf("set file flag: %v", err)
	}

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	// Should not error - best-effort behavior
	if err := cmd.Execute(); err != nil {
		t.Errorf("expected no error for missing file, got: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "not found") && !strings.Contains(output, "skipping") {
		t.Logf("output: %s", output)
	}
}
