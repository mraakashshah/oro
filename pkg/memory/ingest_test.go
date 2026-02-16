package memory //nolint:testpackage // white-box tests for IngestKnowledge and helpers

import (
	"context"
	"strings"
	"testing"
)

// TestIngestKnowledge verifies basic ingestion: parse valid JSONL and return correct count.
func TestIngestKnowledge(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	jsonl := `{"key":"learned-test-1","type":"learned","content":"Always run tests before pushing","bead":"oro-abc1","tags":["git","test"],"ts":"2026-02-15T10:30:00Z"}
{"key":"learned-test-2","type":"learned","content":"Use early returns for error handling","bead":"oro-def2","tags":["go","pattern"],"ts":"2026-02-15T11:00:00Z"}`

	r := strings.NewReader(jsonl)
	count, err := IngestKnowledge(context.Background(), store, r)
	if err != nil {
		t.Fatalf("IngestKnowledge failed: %v", err)
	}

	if count != 2 {
		t.Errorf("expected 2 entries ingested, got %d", count)
	}

	// Verify entries are in the database with correct mapping
	results, err := store.Search(context.Background(), "tests before pushing", SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected to find ingested content")
	}

	// Check first entry mapping
	m := results[0]
	if m.Type != "lesson" {
		t.Errorf("expected type 'lesson', got %q", m.Type)
	}
	if m.Confidence != 0.9 {
		t.Errorf("expected confidence 0.9, got %f", m.Confidence)
	}
	if !strings.HasPrefix(m.Source, "knowledge_import:") {
		t.Errorf("expected source to start with 'knowledge_import:', got %q", m.Source)
	}
	if m.BeadID != "oro-abc1" {
		t.Errorf("expected bead_id 'oro-abc1', got %q", m.BeadID)
	}
}

// TestIngestKnowledge_Dedup verifies that running ingest twice skips duplicates.
func TestIngestKnowledge_Dedup(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	jsonl := `{"key":"learned-dedup-test","type":"learned","content":"Deduplication works by key prefix","bead":"oro-test","tags":[],"ts":"2026-02-15T12:00:00Z"}`

	// First ingest
	r1 := strings.NewReader(jsonl)
	count1, err := IngestKnowledge(context.Background(), store, r1)
	if err != nil {
		t.Fatalf("first ingest failed: %v", err)
	}
	if count1 != 1 {
		t.Errorf("first ingest: expected 1, got %d", count1)
	}

	// Second ingest (should skip duplicate)
	r2 := strings.NewReader(jsonl)
	count2, err := IngestKnowledge(context.Background(), store, r2)
	if err != nil {
		t.Fatalf("second ingest failed: %v", err)
	}
	if count2 != 0 {
		t.Errorf("second ingest: expected 0 (all duplicates), got %d", count2)
	}

	// Verify only one entry exists
	results, err := store.List(context.Background(), ListOpts{Limit: 100})
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 total entry, got %d", len(results))
	}
}

// TestIngestKnowledge_EmptyFile verifies that an empty file returns 0 without error.
func TestIngestKnowledge_EmptyFile(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	r := strings.NewReader("")
	count, err := IngestKnowledge(context.Background(), store, r)
	if err != nil {
		t.Fatalf("expected no error for empty file, got: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 entries, got %d", count)
	}
}

// TestIngestKnowledge_MalformedLines verifies that bad lines are skipped and good ones processed.
func TestIngestKnowledge_MalformedLines(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	jsonl := `{"key":"learned-good-1","type":"learned","content":"Good entry 1","bead":"oro-g1","tags":[],"ts":"2026-02-15T10:00:00Z"}
not valid json at all
{"key":"learned-good-2","type":"learned","content":"Good entry 2","bead":"oro-g2","tags":[],"ts":"2026-02-15T10:05:00Z"}
{"incomplete": "missing required fields"}
{"key":"learned-good-3","type":"learned","content":"Good entry 3","bead":"oro-g3","tags":[],"ts":"2026-02-15T10:10:00Z"}`

	r := strings.NewReader(jsonl)
	count, err := IngestKnowledge(context.Background(), store, r)
	if err != nil {
		t.Fatalf("expected no error (skip bad lines), got: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 good entries ingested, got %d", count)
	}
}

// TestIngestKnowledge_TypeMapping verifies that "learned" type maps to "lesson".
func TestIngestKnowledge_TypeMapping(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	jsonl := `{"key":"learned-type-test","type":"learned","content":"Type mapping test","bead":"oro-type","tags":[],"ts":"2026-02-15T13:00:00Z"}`

	r := strings.NewReader(jsonl)
	_, err := IngestKnowledge(context.Background(), store, r)
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	// Retrieve and verify type is "lesson" not "learned"
	results, err := store.List(context.Background(), ListOpts{Limit: 10})
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected to find ingested entry")
	}

	if results[0].Type != "lesson" {
		t.Errorf("expected type 'lesson', got %q", results[0].Type)
	}
}
