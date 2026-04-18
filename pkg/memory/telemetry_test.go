package memory //nolint:testpackage // white-box test: accesses unexported logSearchEvent

import (
	"context"
	"database/sql"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

// setupTelemetryDB creates an in-memory SQLite DB with base schema + search events migration.
func setupTelemetryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
	if _, err := db.Exec(protocol.MigrateSemanticMemorySearchEvents); err != nil {
		t.Fatalf("exec search events migration: %v", err)
	}
	return db
}

func TestLogSearchEventWritesRow(t *testing.T) {
	db := setupTelemetryDB(t)
	store := NewStore(db)
	ctx := context.Background()

	evt := SearchEvent{
		Project:       "oro",
		QueryHash:     "abc123",
		TopKIDs:       []int64{1, 2, 3},
		TopKScores:    []float64{0.9, 0.8, 0.7},
		LatencyMs:     42,
		UsedRerank:    true,
		UsedBGE:       true,
		ANNCandidates: 50,
	}

	// First insert must return nil and write exactly one row.
	if err := store.logSearchEvent(ctx, evt); err != nil {
		t.Fatalf("logSearchEvent: %v", err)
	}

	var (
		id            int64
		project       string
		queryHash     string
		topKIDs       string
		topKScores    string
		latencyMs     int
		usedRerank    int
		usedBGE       int
		annCandidates int
	)
	row := db.QueryRowContext(ctx, `
		SELECT id, project, query_hash, top_k_ids, top_k_scores,
		       latency_ms, used_rerank, used_bge, ann_candidates
		FROM memory_search_events
		ORDER BY id DESC
		LIMIT 1
	`)
	if err := row.Scan(&id, &project, &queryHash, &topKIDs, &topKScores,
		&latencyMs, &usedRerank, &usedBGE, &annCandidates); err != nil {
		t.Fatalf("scan row: %v", err)
	}

	if project != "oro" {
		t.Errorf("project: got %q, want %q", project, "oro")
	}
	if queryHash != "abc123" {
		t.Errorf("query_hash: got %q, want %q", queryHash, "abc123")
	}
	if topKIDs != "[1,2,3]" {
		t.Errorf("top_k_ids: got %q, want %q", topKIDs, "[1,2,3]")
	}
	if topKScores != "[0.9,0.8,0.7]" {
		t.Errorf("top_k_scores: got %q, want %q", topKScores, "[0.9,0.8,0.7]")
	}
	if latencyMs != 42 {
		t.Errorf("latency_ms: got %d, want 42", latencyMs)
	}
	if usedRerank != 1 {
		t.Errorf("used_rerank: got %d, want 1", usedRerank)
	}
	if usedBGE != 1 {
		t.Errorf("used_bge: got %d, want 1", usedBGE)
	}
	if annCandidates != 50 {
		t.Errorf("ann_candidates: got %d, want 50", annCandidates)
	}

	// Second insert with identical payload must yield a second row with different ID.
	if err := store.logSearchEvent(ctx, evt); err != nil {
		t.Fatalf("second logSearchEvent: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_search_events").Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 2 {
		t.Errorf("row count after two inserts: got %d, want 2", count)
	}

	var id2 int64
	if err := db.QueryRowContext(ctx, "SELECT id FROM memory_search_events ORDER BY id DESC LIMIT 1").Scan(&id2); err != nil {
		t.Fatalf("scan id2: %v", err)
	}
	if id2 == id {
		t.Errorf("second row id %d == first row id %d, expected different", id2, id)
	}

	// Ensure TopKIDs/TopKScores marshal to "[]" for empty slices.
	emptyEvt := SearchEvent{
		Project:    "empty",
		QueryHash:  "empty",
		TopKIDs:    []int64{},
		TopKScores: []float64{},
	}
	if err := store.logSearchEvent(ctx, emptyEvt); err != nil {
		t.Fatalf("logSearchEvent empty slices: %v", err)
	}
	var emptyIDs, emptyScores string
	if err := db.QueryRowContext(ctx, `
		SELECT top_k_ids, top_k_scores FROM memory_search_events
		WHERE project='empty' LIMIT 1
	`).Scan(&emptyIDs, &emptyScores); err != nil {
		t.Fatalf("scan empty row: %v", err)
	}
	if emptyIDs != "[]" {
		t.Errorf("empty TopKIDs: got %q, want %q", emptyIDs, "[]")
	}
	if emptyScores != "[]" {
		t.Errorf("empty TopKScores: got %q, want %q", emptyScores, "[]")
	}
}
