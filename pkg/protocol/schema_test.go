package protocol_test

import (
	"context"
	"database/sql"
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
	_, err = db.Exec(`INSERT OR REPLACE INTO pane_activity VALUES ("manager", 1234567890)`)
	if err != nil {
		t.Fatalf("INSERT OR REPLACE into pane_activity: %v", err)
	}

	_, err = db.Exec(`INSERT OR REPLACE INTO pane_activity VALUES ("manager", 9999999999)`)
	if err != nil {
		t.Fatalf("second INSERT OR REPLACE (idempotent): %v", err)
	}

	var ts int64
	err = db.QueryRow(`SELECT last_seen FROM pane_activity WHERE pane='manager'`).Scan(&ts)
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

func TestMigration11(t *testing.T) {
	testBeadSchemaMigration(t)
}

func TestSchemaMigration11(t *testing.T) {
	testBeadSchemaMigration(t)
}

func testBeadSchemaMigration(t *testing.T) {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("second migration: %v", err)
	}

	for _, table := range []string{
		"beads",
		"bead_deps",
		"bead_tags",
		"bead_labels",
		"bead_metadata",
		"bead_notes",
		"beads_fts",
	} {
		assertSQLiteObjectExists(t, db, "table", table)
	}
	for _, index := range []string{
		"idx_beads_status",
		"idx_beads_parent",
		"idx_beads_type",
		"idx_beads_priority",
		"idx_beads_deferred",
		"idx_bead_deps_depends_on",
		"idx_bead_tags_tag",
		"idx_bead_labels_label",
		"idx_bead_notes_bead",
	} {
		assertSQLiteObjectExists(t, db, "index", index)
	}
	for _, view := range []string{"beads_ready", "beads_blocked"} {
		assertSQLiteObjectExists(t, db, "view", view)
	}
	for _, trigger := range []string{
		"beads_fts_ai",
		"beads_fts_ad",
		"beads_fts_au",
		"bead_deps_touch_parent_ai",
		"bead_deps_touch_parent_au",
		"bead_deps_touch_parent_ad",
		"bead_tags_touch_parent_ai",
		"bead_tags_touch_parent_au",
		"bead_tags_touch_parent_ad",
		"bead_labels_touch_parent_ai",
		"bead_labels_touch_parent_au",
		"bead_labels_touch_parent_ad",
		"bead_metadata_touch_parent_ai",
		"bead_metadata_touch_parent_au",
		"bead_metadata_touch_parent_ad",
		"bead_notes_touch_parent_ai",
		"bead_notes_touch_parent_au",
		"bead_notes_touch_parent_ad",
	} {
		assertSQLiteObjectExists(t, db, "trigger", trigger)
	}
}

func assertSQLiteObjectExists(t *testing.T, db *sql.DB, objectType, name string) {
	t.Helper()
	var got string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type = ? AND name = ?",
		objectType,
		name,
	).Scan(&got)
	if err != nil {
		t.Fatalf("%s %q not found: %v", objectType, name, err)
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

func TestMigrateSemanticMemorySearchEvents(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	_, err = db.Exec(protocol.MigrateSemanticMemorySearchEvents)
	if err != nil {
		t.Fatalf("exec MigrateSemanticMemorySearchEvents: %v", err)
	}

	// Verify table exists.
	var tableName string
	err = db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='memory_search_events'",
	).Scan(&tableName)
	if err != nil {
		t.Fatalf("memory_search_events table not found: %v", err)
	}

	// Verify exact column set via PRAGMA table_info.
	type colInfo struct {
		name    string
		typ     string
		notNull bool
		dflt    *string
		pk      bool
	}
	wantCols := []colInfo{
		{name: "id", typ: "INTEGER", notNull: false, pk: true},
		{name: "ts", typ: "DATETIME", notNull: true, dflt: ptr("datetime('now')")},
		{name: "project", typ: "TEXT", notNull: false},
		{name: "query_hash", typ: "TEXT", notNull: false},
		{name: "top_k_ids", typ: "TEXT", notNull: false},
		{name: "top_k_scores", typ: "TEXT", notNull: false},
		{name: "latency_ms", typ: "INTEGER", notNull: false},
		{name: "used_rerank", typ: "INTEGER", notNull: false, dflt: ptr("0")},
		{name: "used_bge", typ: "INTEGER", notNull: false, dflt: ptr("0")},
		{name: "ann_candidates", typ: "INTEGER", notNull: false},
	}

	rows, err := db.Query("PRAGMA table_info(memory_search_events)")
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()

	var gotCols []colInfo
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dfltVal *string
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltVal, &pk); err != nil {
			t.Fatalf("scan column info: %v", err)
		}
		gotCols = append(gotCols, colInfo{
			name:    name,
			typ:     typ,
			notNull: notNull != 0,
			dflt:    dfltVal,
			pk:      pk != 0,
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}

	if len(gotCols) != len(wantCols) {
		t.Fatalf("expected %d columns, got %d: %v", len(wantCols), len(gotCols), gotCols)
	}
	for i, want := range wantCols {
		got := gotCols[i]
		if got.name != want.name {
			t.Errorf("col[%d] name: want %q, got %q", i, want.name, got.name)
		}
		if got.typ != want.typ {
			t.Errorf("col[%d] %q type: want %q, got %q", i, want.name, want.typ, got.typ)
		}
		if got.notNull != want.notNull {
			t.Errorf("col[%d] %q notNull: want %v, got %v", i, want.name, want.notNull, got.notNull)
		}
		if got.pk != want.pk {
			t.Errorf("col[%d] %q pk: want %v, got %v", i, want.name, want.pk, got.pk)
		}
		wantDflt := want.dflt
		gotDflt := got.dflt
		switch {
		case wantDflt == nil && gotDflt == nil:
			// both nil — ok
		case wantDflt == nil && gotDflt != nil:
			t.Errorf("col[%d] %q default: want nil, got %q", i, want.name, *gotDflt)
		case wantDflt != nil && gotDflt == nil:
			t.Errorf("col[%d] %q default: want %q, got nil", i, want.name, *wantDflt)
		case *wantDflt != *gotDflt:
			t.Errorf("col[%d] %q default: want %q, got %q", i, want.name, *wantDflt, *gotDflt)
		}
	}

	// Verify idx_mse_ts index exists.
	var indexName string
	err = db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name='idx_mse_ts'",
	).Scan(&indexName)
	if err != nil {
		t.Fatalf("idx_mse_ts index not found: %v", err)
	}

	// Idempotency: running migration a second time must not error.
	_, err = db.Exec(protocol.MigrateSemanticMemorySearchEvents)
	if err != nil {
		t.Fatalf("second exec (idempotency): %v", err)
	}
}

func ptr(s string) *string { return &s }

func TestMigrateSemanticMemoryChunksConstant(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Apply base schema first (creates memories table)
	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	// Apply the migration
	_, err = db.Exec(protocol.MigrateSemanticMemoryChunks)
	if err != nil {
		t.Fatalf("exec migration: %v", err)
	}

	// Verify memory_chunks table exists
	var tableName string
	err = db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='memory_chunks'",
	).Scan(&tableName)
	if err != nil {
		t.Fatalf("memory_chunks table not found: %v", err)
	}

	// Verify required columns exist
	requiredCols := []string{"id", "memory_id", "chunk_idx", "text", "embedding"}
	for _, col := range requiredCols {
		var colName string
		err := db.QueryRow(
			"SELECT name FROM pragma_table_info('memory_chunks') WHERE name=?",
			col,
		).Scan(&colName)
		if err != nil {
			t.Errorf("required column %q not found: %v", col, err)
		}
	}

	// Verify idx_memory_chunks_memory_id index exists
	var indexName string
	err = db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name='idx_memory_chunks_memory_id'",
	).Scan(&indexName)
	if err != nil {
		t.Fatalf("idx_memory_chunks_memory_id index not found: %v", err)
	}

	// Test idempotency: apply migration again (should not error due to IF NOT EXISTS)
	_, err = db.Exec(protocol.MigrateSemanticMemoryChunks)
	if err != nil {
		t.Fatalf("second migration exec (idempotency): %v", err)
	}

	// Insert a memory row to test FK constraint
	_, err = db.Exec(
		`INSERT INTO memories (content, type, source) VALUES ('test memory', 'test', 'test_source')`,
	)
	if err != nil {
		t.Fatalf("insert test memory: %v", err)
	}

	var memoryID int64
	err = db.QueryRow(`SELECT id FROM memories WHERE content='test memory'`).Scan(&memoryID)
	if err != nil {
		t.Fatalf("query memory ID: %v", err)
	}

	// Insert a chunk row to verify the table works
	_, err = db.Exec(
		`INSERT INTO memory_chunks (memory_id, chunk_idx, text, embedding) VALUES (?, ?, ?, ?)`,
		memoryID, 0, "chunk text", []byte{},
	)
	if err != nil {
		t.Fatalf("insert memory chunk: %v", err)
	}

	// Verify the chunk was inserted
	var chunkText string
	err = db.QueryRow(
		`SELECT text FROM memory_chunks WHERE memory_id=? AND chunk_idx=?`,
		memoryID, 0,
	).Scan(&chunkText)
	if err != nil {
		t.Fatalf("query memory chunk: %v", err)
	}
	if chunkText != "chunk text" {
		t.Errorf("expected chunk_text='chunk text', got %q", chunkText)
	}
}
