package memory_test

import (
	"context"
	"testing"

	"oro/pkg/memory"
	"oro/pkg/memory/testhelpers"
	"oro/pkg/protocol"
)

// TestStoreVectorSearchReturnsANNResults verifies that Store.HybridSearch
// returns results sourced from a SQLiteVecIndex (HNSW path) when one is
// injected. The test is skipped when the sqlite-vec extension is unavailable
// (expected on macOS with the modernc.org/sqlite driver until oro-p545).
func TestStoreVectorSearchReturnsANNResults(t *testing.T) {
	db := openVecTestDB(t) // skips if sqlite-vec extension not available
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	idx, err := memory.NewSQLiteVecIndex(db)
	if err != nil {
		t.Skipf("SQLiteVecIndex unavailable: %v", err)
	}

	store := memory.NewStore(db)
	embedder := testhelpers.NewFakeEmbedder(0) // dim=128
	store.SetEmbedder(embedder)
	store.SetVectorIndex(idx)

	// Insert a memory; embedder stores the DB embedding but vectorSearch will
	// use the vecIndex path once we also upsert into it below.
	id, err := store.Insert(ctx, memory.InsertParams{
		Content:    "goroutine channel select statement concurrency",
		Type:       "lesson",
		Source:     "self_report",
		Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Upsert the same embedding into the vec index so Search can find it.
	vec := embedder.Embed("goroutine channel select statement concurrency")
	if err := idx.Upsert(ctx, id, vec, ""); err != nil {
		t.Fatalf("idx upsert: %v", err)
	}

	results, err := store.HybridSearch(ctx, "goroutine concurrency", memory.SearchOpts{Limit: 5})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result from HybridSearch with SQLiteVecIndex")
	}

	found := false
	for _, r := range results {
		if r.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("memory id=%d not found in HybridSearch results with SQLiteVecIndex; got %d result(s)", id, len(results))
	}
}
