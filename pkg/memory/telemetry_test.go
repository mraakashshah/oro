package memory //nolint:testpackage // white-box test: accesses unexported logSearchEvent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
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

// queryHash returns hex(sha256(q))[:16].
func queryHash(q string) string {
	h := sha256.Sum256([]byte(q))
	return fmt.Sprintf("%x", h)[:16]
}

// countSearchEvents returns the row count in memory_search_events.
func countSearchEvents(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM memory_search_events").Scan(&n); err != nil {
		t.Fatalf("count search events: %v", err)
	}
	return n
}

// TestHybridSearchLogsOneRow verifies that HybridSearch writes exactly one
// telemetry row per call, with correct field values, and is non-fatal on error.
func TestHybridSearchLogsOneRow(t *testing.T) {
	ctx := context.Background()

	t.Run("NoEmbedder_logsRow", func(t *testing.T) {
		db := setupTelemetryDB(t)
		store := NewStore(db)

		if _, err := store.Insert(ctx, InsertParams{
			Content: "unique telemetry content xyz123", Type: "lesson",
			Source: "test", Confidence: 0.9,
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}

		query := "unique telemetry content"
		results, err := store.HybridSearch(ctx, query, SearchOpts{Limit: 5})
		if err != nil {
			t.Fatalf("HybridSearch: %v", err)
		}

		if n := countSearchEvents(t, db); n != 1 {
			t.Fatalf("want 1 search event row, got %d", n)
		}

		var (
			gotHash       string
			topKIDs       string
			topKScores    string
			latencyMs     int
			usedBGE       int
			usedRerank    int
			annCandidates int
		)
		row := db.QueryRowContext(ctx, `
			SELECT query_hash, top_k_ids, top_k_scores, latency_ms,
			       used_bge, used_rerank, ann_candidates
			FROM memory_search_events LIMIT 1
		`)
		if err := row.Scan(&gotHash, &topKIDs, &topKScores, &latencyMs,
			&usedBGE, &usedRerank, &annCandidates); err != nil {
			t.Fatalf("scan event row: %v", err)
		}

		if want := queryHash(query); gotHash != want {
			t.Errorf("query_hash: got %q, want %q", gotHash, want)
		}

		wantIDs := make([]int64, len(results))
		for i, r := range results {
			wantIDs[i] = r.ID
		}
		wantIDsJSON, _ := json.Marshal(wantIDs)
		if topKIDs != string(wantIDsJSON) {
			t.Errorf("top_k_ids: got %s, want %s", topKIDs, wantIDsJSON)
		}

		wantScores := make([]float64, len(results))
		for i, r := range results {
			wantScores[i] = r.Score
		}
		wantScoresJSON, _ := json.Marshal(wantScores)
		if topKScores != string(wantScoresJSON) {
			t.Errorf("top_k_scores: got %s, want %s", topKScores, wantScoresJSON)
		}

		if latencyMs < 0 {
			t.Errorf("latency_ms should be >= 0, got %d", latencyMs)
		}
		if usedBGE != 0 {
			t.Errorf("used_bge: got %d, want 0 (no embedder)", usedBGE)
		}
		if usedRerank != 0 {
			t.Errorf("used_rerank: got %d, want 0", usedRerank)
		}
		if annCandidates != 0 {
			t.Errorf("ann_candidates: got %d, want 0 (no embedder)", annCandidates)
		}
	})

	t.Run("EmptyQuery_noRow", func(t *testing.T) {
		db := setupTelemetryDB(t)
		store := NewStore(db)

		if _, err := store.HybridSearch(ctx, "", SearchOpts{}); err != nil {
			t.Fatalf("HybridSearch empty: %v", err)
		}
		if n := countSearchEvents(t, db); n != 0 {
			t.Errorf("empty query must log no rows, got %d", n)
		}
	})

	t.Run("DBError_nonFatal", func(t *testing.T) {
		db := setupTelemetryDB(t)
		store := NewStore(db)

		if _, err := store.Insert(ctx, InsertParams{
			Content: "db error test content unique_abc987", Type: "lesson",
			Source: "test", Confidence: 0.9,
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}

		// Drop the table to force logSearchEvent to fail.
		if _, err := db.ExecContext(ctx, "DROP TABLE memory_search_events"); err != nil {
			t.Fatalf("drop table: %v", err)
		}

		results, err := store.HybridSearch(ctx, "db error test content", SearchOpts{Limit: 5})
		if err != nil {
			t.Errorf("HybridSearch must not return error on telemetry failure, got: %v", err)
		}
		if len(results) == 0 {
			t.Error("HybridSearch must still return results when telemetry fails")
		}
	})
}
