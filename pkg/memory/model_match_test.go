package memory //nolint:testpackage // white-box tests for checkEmbedderModelMatch

import (
	"context"
	"testing"

	"oro/pkg/memory/testhelpers"
)

func TestCheckEmbedderModelMatchHappyNoop(t *testing.T) {
	db := setupSemanticProductionDB(t)
	embedder := testhelpers.NewFakeEmbedder(0)
	store := NewStore(db)
	store.SetEmbedder(embedder)
	ctx := context.Background()

	currentModel := "fake-jaccard"

	// Write sentinel matching current model
	_, err := db.ExecContext(ctx,
		`INSERT OR REPLACE INTO kv_store (key, value, updated_at) VALUES (?, ?, datetime('now'))`,
		"embedding_dense_model", currentModel,
	)
	if err != nil {
		t.Fatalf("insert sentinel: %v", err)
	}

	// Insert a test memory with embedding
	testVec := embedder.Embed("test memory content")
	testVecBlob := MarshalEmbedding(testVec)
	_, err = db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence, embedding_dense)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"test memory content", "lesson", `["test"]`, "self_report", 0.9, testVecBlob,
	)
	if err != nil {
		t.Fatalf("insert memory: %v", err)
	}

	// Seed backfill state to 'completed' so we can assert it stays unchanged.
	_, err = db.ExecContext(ctx,
		`UPDATE kv_store SET value = ?, updated_at = datetime('now') WHERE key = ?`,
		"completed", backfillStateKey,
	)
	if err != nil {
		t.Fatalf("seed backfill state: %v", err)
	}

	// Call checkEmbedderModelMatch with matching model
	err = store.checkEmbedderModelMatch(ctx, currentModel)
	if err != nil {
		t.Fatalf("checkEmbedderModelMatch: %v", err)
	}

	// Verify embedding_dense is still present (not cleared)
	var count int
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE embedding_dense IS NOT NULL`,
	).Scan(&count)
	if err != nil {
		t.Fatalf("query embeddings: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 memory with embedding_dense, got %d", count)
	}

	// Verify memory_chunks is unchanged
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_chunks`,
	).Scan(&count)
	if err != nil {
		t.Fatalf("query chunks: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 chunks (unchanged), got %d", count)
	}

	// Verify sentinel unchanged
	var sentinel string
	err = db.QueryRowContext(ctx,
		`SELECT value FROM kv_store WHERE key = 'embedding_dense_model'`,
	).Scan(&sentinel)
	if err != nil {
		t.Fatalf("query sentinel: %v", err)
	}
	if sentinel != currentModel {
		t.Errorf("sentinel changed; expected %q, got %q", currentModel, sentinel)
	}

	// Verify backfill state is unchanged (still 'completed')
	var state string
	err = db.QueryRowContext(ctx,
		`SELECT value FROM kv_store WHERE key = ?`, backfillStateKey,
	).Scan(&state)
	if err != nil {
		t.Fatalf("query backfill state: %v", err)
	}
	if state != "completed" {
		t.Errorf("backfill state changed; expected 'completed', got %q", state)
	}
}

func TestCheckEmbedderModelMatchAgainstProductionSchema(t *testing.T) {
	db := setupSemanticProductionDB(t)
	embedder := testhelpers.NewFakeEmbedder(0)
	store := NewStore(db)
	store.SetEmbedder(embedder)
	ctx := context.Background()

	oldModel := "bge-small-en-v1.5"
	newModel := "bge-base-en-v1.5"

	// Write old sentinel
	_, err := db.ExecContext(ctx,
		`INSERT OR REPLACE INTO kv_store (key, value, updated_at) VALUES (?, ?, datetime('now'))`,
		"embedding_dense_model", oldModel,
	)
	if err != nil {
		t.Fatalf("insert old sentinel: %v", err)
	}

	// Insert a test memory with embedding
	testVec := embedder.Embed("test memory content")
	testVecBlob := MarshalEmbedding(testVec)
	_, err = db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence, embedding_dense)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"test memory content", "lesson", `["test"]`, "self_report", 0.9, testVecBlob,
	)
	if err != nil {
		t.Fatalf("insert memory: %v", err)
	}

	// Insert a test chunk
	_, err = db.ExecContext(ctx,
		`INSERT INTO memory_chunks (memory_id, chunk_idx, text, embedding)
		 VALUES (?, ?, ?, ?)`,
		1, 0, "chunk content", testVecBlob,
	)
	if err != nil {
		t.Fatalf("insert chunk: %v", err)
	}

	// Set backfill state to "completed"
	_, err = db.ExecContext(ctx,
		`UPDATE kv_store SET value = ?, updated_at = datetime('now') WHERE key = ?`,
		"completed", backfillStateKey,
	)
	if err != nil {
		t.Fatalf("update backfill state: %v", err)
	}

	// Call checkEmbedderModelMatch with different model
	err = store.checkEmbedderModelMatch(ctx, newModel)
	if err != nil {
		t.Fatalf("checkEmbedderModelMatch: %v", err)
	}

	// Verify embedding_dense is NULL (cleared)
	var count int
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE embedding_dense IS NULL`,
	).Scan(&count)
	if err != nil {
		t.Fatalf("query null embeddings: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 memory with NULL embedding_dense, got %d", count)
	}

	// Verify memory_chunks is deleted
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_chunks`,
	).Scan(&count)
	if err != nil {
		t.Fatalf("query chunks: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 chunks (all deleted), got %d", count)
	}

	// Verify sentinel updated to new model
	var sentinel string
	err = db.QueryRowContext(ctx,
		`SELECT value FROM kv_store WHERE key = 'embedding_dense_model'`,
	).Scan(&sentinel)
	if err != nil {
		t.Fatalf("query sentinel: %v", err)
	}
	if sentinel != newModel {
		t.Errorf("sentinel not updated; expected %q, got %q", newModel, sentinel)
	}

	// Verify backfill state flipped to pending
	var state string
	err = db.QueryRowContext(ctx,
		`SELECT value FROM kv_store WHERE key = ?`, backfillStateKey,
	).Scan(&state)
	if err != nil {
		t.Fatalf("query backfill state: %v", err)
	}
	if state != "pending" {
		t.Errorf("backfill state not reset; expected 'pending', got %q", state)
	}
}

func TestCheckEmbedderModelMatchFirstRunWritesSentinel(t *testing.T) {
	db := setupSemanticProductionDB(t)
	embedder := testhelpers.NewFakeEmbedder(0)
	store := NewStore(db)
	store.SetEmbedder(embedder)
	ctx := context.Background()

	currentModel := "fake-jaccard"

	// Do NOT write any sentinel initially (first run)

	// Insert a test memory (shouldn't have embedding yet on first run)
	_, err := db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, confidence)
		 VALUES (?, ?, ?, ?, ?)`,
		"test memory content", "lesson", `["test"]`, "self_report", 0.9,
	)
	if err != nil {
		t.Fatalf("insert memory: %v", err)
	}

	// Call checkEmbedderModelMatch on first run
	err = store.checkEmbedderModelMatch(ctx, currentModel)
	if err != nil {
		t.Fatalf("checkEmbedderModelMatch first run: %v", err)
	}

	// Verify sentinel was written
	var sentinel string
	err = db.QueryRowContext(ctx,
		`SELECT value FROM kv_store WHERE key = 'embedding_dense_model'`,
	).Scan(&sentinel)
	if err != nil {
		t.Fatalf("query sentinel: %v", err)
	}
	if sentinel != currentModel {
		t.Errorf("sentinel not written; expected %q, got %q", currentModel, sentinel)
	}

	// Verify memory is unchanged (no clearing on first run)
	var count int
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories`,
	).Scan(&count)
	if err != nil {
		t.Fatalf("query memories: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 memory (unchanged), got %d", count)
	}
}

func TestSemanticTestsUseProductionMigrations(t *testing.T) {
	db := setupSemanticProductionDB(t)
	ctx := context.Background()

	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='backfill_semantic_memory_state'`,
	).Scan(&count); err != nil {
		t.Fatalf("query fabricated table: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no fabricated backfill_semantic_memory_state table, found %d", count)
	}

	for _, col := range []string{"chunk_idx", "text", "embedding"} {
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('memory_chunks') WHERE name=?`, col,
		).Scan(&count); err != nil {
			t.Fatalf("query memory_chunks column %s: %v", col, err)
		}
		if count != 1 {
			t.Fatalf("expected production memory_chunks column %q to exist", col)
		}
	}
}
