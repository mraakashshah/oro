package protocol_test

import (
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestSchemaExecsCleanly(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}
}

func TestSchemaCreatesExpectedTables(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	expected := []string{"events", "assignments", "commands", "memories", "memories_fts"}
	for _, table := range expected {
		var name string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type IN ('table','view') AND name = ?",
			table,
		).Scan(&name)
		if err != nil {
			t.Errorf("expected table %q not found: %v", table, err)
		}
	}
}

func TestSchemaDDL(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	// Verify pane_activity table exists
	var name string
	err = db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='pane_activity'",
	).Scan(&name)
	if err != nil {
		t.Fatalf("pane_activity table not found: %v", err)
	}

	// Verify INSERT OR REPLACE works (idempotent upsert)
	_, err = db.Exec(`INSERT OR REPLACE INTO pane_activity VALUES ("architect", 1234567890)`)
	if err != nil {
		t.Fatalf("INSERT OR REPLACE into pane_activity: %v", err)
	}

	_, err = db.Exec(`INSERT OR REPLACE INTO pane_activity VALUES ("architect", 9999999999)`)
	if err != nil {
		t.Fatalf("second INSERT OR REPLACE (idempotent): %v", err)
	}

	var ts int64
	err = db.QueryRow(`SELECT last_seen FROM pane_activity WHERE pane='architect'`).Scan(&ts)
	if err != nil {
		t.Fatalf("query pane_activity: %v", err)
	}
	if ts != 9999999999 {
		t.Errorf("expected last_seen=9999999999, got %d", ts)
	}
}

func TestSchemaDDL_RejectionBeadIndex(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	// idx_rejection_bead must exist after applying only SchemaDDL (no migrations).
	var name string
	err = db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name='idx_rejection_bead'",
	).Scan(&name)
	if err != nil {
		t.Fatalf("idx_rejection_bead index not found in SchemaDDL: %v", err)
	}
}

func TestSchemaIsIdempotent(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Execute twice — IF NOT EXISTS should prevent errors
	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("first exec: %v", err)
	}
	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("second exec (idempotency): %v", err)
	}
}

func TestMigrateSemanticMemoryDense(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	// First migration: add embedding_dense and content_tokens columns
	_, err = db.Exec(protocol.MigrateSemanticMemoryDense)
	if err != nil {
		t.Fatalf("first migration exec: %v", err)
	}

	// Verify embedding_dense column exists
	var colName string
	err = db.QueryRow(
		"SELECT name FROM pragma_table_info('memories') WHERE name='embedding_dense'",
	).Scan(&colName)
	if err != nil {
		t.Fatalf("embedding_dense column not found: %v", err)
	}

	// Verify content_tokens column exists
	err = db.QueryRow(
		"SELECT name FROM pragma_table_info('memories') WHERE name='content_tokens'",
	).Scan(&colName)
	if err != nil {
		t.Fatalf("content_tokens column not found: %v", err)
	}

	// Re-running the migration should not break the database (error is intentionally ignored)
	// This simulates the error-ignoring pattern used in migrateStateDB
	_, _ = db.Exec(protocol.MigrateSemanticMemoryDense)

	// Verify database is still functional and columns exist
	var colName2 string
	err = db.QueryRow(
		"SELECT name FROM pragma_table_info('memories') WHERE name='embedding_dense'",
	).Scan(&colName2)
	if err != nil {
		t.Fatalf("embedding_dense column missing after re-run: %v", err)
	}

	// Insert a row to verify columns are properly populated with defaults
	_, err = db.Exec(
		`INSERT INTO memories (content, type, source) VALUES ('test content', 'test', 'test_source')`,
	)
	if err != nil {
		t.Fatalf("insert into memories: %v", err)
	}

	var contentTokens int
	err = db.QueryRow(
		`SELECT content_tokens FROM memories WHERE content='test content'`,
	).Scan(&contentTokens)
	if err != nil {
		t.Fatalf("query content_tokens: %v", err)
	}
	if contentTokens != 0 {
		t.Errorf("expected content_tokens default=0, got %d", contentTokens)
	}
}

func TestMigrateSemanticMemoryBackfillState(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	// Apply the backfill state migration
	_, err = db.Exec(protocol.MigrateSemanticMemoryBackfillState)
	if err != nil {
		t.Fatalf("exec migration: %v", err)
	}

	// Verify backfill_semantic_memory_state key exists and is set to 'pending'
	var state string
	err = db.QueryRow(
		`SELECT value FROM kv_store WHERE key='backfill_semantic_memory_state'`,
	).Scan(&state)
	if err != nil {
		t.Fatalf("backfill_semantic_memory_state key not found: %v", err)
	}
	if state != "pending" {
		t.Errorf("expected backfill_semantic_memory_state='pending', got %q", state)
	}

	// Verify embedding_dense_model sentinel key exists
	var model string
	err = db.QueryRow(
		`SELECT value FROM kv_store WHERE key='embedding_dense_model'`,
	).Scan(&model)
	if err != nil {
		t.Fatalf("embedding_dense_model key not found: %v", err)
	}
	if model != "bge-small-en-v1.5" {
		t.Errorf("expected embedding_dense_model='bge-small-en-v1.5', got %q", model)
	}

	// Re-running should be idempotent (INSERT OR IGNORE should prevent duplicates)
	_, err = db.Exec(protocol.MigrateSemanticMemoryBackfillState)
	if err != nil {
		t.Fatalf("second migration exec (should be idempotent): %v", err)
	}

	// Verify values unchanged after re-running
	var stateAfter string
	err = db.QueryRow(
		`SELECT value FROM kv_store WHERE key='backfill_semantic_memory_state'`,
	).Scan(&stateAfter)
	if err != nil {
		t.Fatalf("backfill_semantic_memory_state key after re-run: %v", err)
	}
	if stateAfter != "pending" {
		t.Errorf("expected backfill_semantic_memory_state='pending' after re-run, got %q", stateAfter)
	}
}

func TestEmbeddingDenseModelSentinel(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	// Apply the backfill state migration which includes the embedding_dense_model sentinel
	_, err = db.Exec(protocol.MigrateSemanticMemoryBackfillState)
	if err != nil {
		t.Fatalf("exec migration: %v", err)
	}

	// Verify embedding_dense_model sentinel is set to correct value
	var model string
	err = db.QueryRow(
		`SELECT value FROM kv_store WHERE key='embedding_dense_model'`,
	).Scan(&model)
	if err != nil {
		t.Fatalf("embedding_dense_model not found: %v", err)
	}
	if model != "bge-small-en-v1.5" {
		t.Errorf("expected model='bge-small-en-v1.5', got %q", model)
	}

	// Re-running should be idempotent
	_, err = db.Exec(protocol.MigrateSemanticMemoryBackfillState)
	if err != nil {
		t.Fatalf("second migration exec (should be idempotent): %v", err)
	}

	// Verify value unchanged after re-running
	var modelAfter string
	err = db.QueryRow(
		`SELECT value FROM kv_store WHERE key='embedding_dense_model'`,
	).Scan(&modelAfter)
	if err != nil {
		t.Fatalf("query after re-run: %v", err)
	}
	if modelAfter != "bge-small-en-v1.5" {
		t.Errorf("expected model='bge-small-en-v1.5' after re-run, got %q", modelAfter)
	}
}
