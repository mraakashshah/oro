package memory //nolint:testpackage // white-box tests for checkEmbedderModelMatch

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/memory/testhelpers"
	"oro/pkg/protocol"
)

// setupTestDBWithSemanticTables creates an in-memory SQLite database with the
// full schema plus the semantic memory tables needed for model matching tests.
func setupTestDBWithSemanticTables(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("exec schema: %v", err)
	}

	// Add embedding_dense column to memories table for semantic search
	if _, err := db.Exec(`
		ALTER TABLE memories ADD COLUMN embedding_dense BLOB;
	`); err != nil {
		// Ignore if column already exists (driver wording varies slightly).
		if !strings.Contains(err.Error(), "duplicate column name") {
			t.Fatalf("add embedding_dense column: %v", err)
		}
	}

	// Create memory_chunks table for chunked semantic embeddings
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS memory_chunks (
			id INTEGER PRIMARY KEY,
			memory_id INTEGER NOT NULL,
			chunk_index INTEGER NOT NULL,
			content TEXT NOT NULL,
			embedding_dense BLOB,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (memory_id) REFERENCES memories(id) ON DELETE CASCADE,
			UNIQUE(memory_id, chunk_index)
		);
	`); err != nil {
		t.Fatalf("create memory_chunks: %v", err)
	}

	// Create backfill state table
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS backfill_semantic_memory_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			state TEXT NOT NULL DEFAULT 'pending'
		);
		INSERT OR IGNORE INTO backfill_semantic_memory_state (id, state) VALUES (1, 'pending');
	`); err != nil {
		t.Fatalf("create backfill state: %v", err)
	}

	return db
}

func TestCheckEmbedderModelMatchHappyNoop(t *testing.T) {
	db := setupTestDBWithSemanticTables(t)
	embedder := testhelpers.NewFakeEmbedder(0)
	store := NewStore(db)
	store.SetEmbedder(embedder)
	ctx := context.Background()

	currentModel := "fake-jaccard"

	// Write sentinel matching current model
	_, err := db.ExecContext(ctx,
		`INSERT INTO kv_store (key, value, updated_at) VALUES (?, ?, datetime('now'))`,
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
		`UPDATE backfill_semantic_memory_state SET state = ? WHERE id = 1`,
		"completed",
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
		`SELECT state FROM backfill_semantic_memory_state WHERE id = 1`,
	).Scan(&state)
	if err != nil {
		t.Fatalf("query backfill state: %v", err)
	}
	if state != "completed" {
		t.Errorf("backfill state changed; expected 'completed', got %q", state)
	}
}

func TestCheckEmbedderModelMatchResetsOnMismatch(t *testing.T) {
	db := setupTestDBWithSemanticTables(t)
	embedder := testhelpers.NewFakeEmbedder(0)
	store := NewStore(db)
	store.SetEmbedder(embedder)
	ctx := context.Background()

	oldModel := "bge-small-en-v1.5"
	newModel := "bge-base-en-v1.5"

	// Write old sentinel
	_, err := db.ExecContext(ctx,
		`INSERT INTO kv_store (key, value, updated_at) VALUES (?, ?, datetime('now'))`,
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
		`INSERT INTO memory_chunks (memory_id, chunk_index, content, embedding_dense)
		 VALUES (?, ?, ?, ?)`,
		1, 0, "chunk content", testVecBlob,
	)
	if err != nil {
		t.Fatalf("insert chunk: %v", err)
	}

	// Set backfill state to "completed"
	_, err = db.ExecContext(ctx,
		`UPDATE backfill_semantic_memory_state SET state = ? WHERE id = 1`,
		"completed",
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
		`SELECT state FROM backfill_semantic_memory_state WHERE id = 1`,
	).Scan(&state)
	if err != nil {
		t.Fatalf("query backfill state: %v", err)
	}
	if state != "pending" {
		t.Errorf("backfill state not reset; expected 'pending', got %q", state)
	}
}

func TestCheckEmbedderModelMatchFirstRunWritesSentinel(t *testing.T) {
	db := setupTestDBWithSemanticTables(t)
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
