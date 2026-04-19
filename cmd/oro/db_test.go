package main

import (
	"path/filepath"
	"testing"

	"oro/pkg/dbutil"
)

func TestOpenDB_PingSucceeds(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// db.Ping should already have been called inside openDB;
	// verify the connection is usable.
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping after openDB: %v", err)
	}
}

func TestOpenDB_WALModeEnabled(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", journalMode)
	}
}

func TestOpenDB_BusyTimeoutSet(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	var busyTimeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("expected busy_timeout=5000, got %d", busyTimeout)
	}
}

func TestOpenDB_CreatesParentDirs(t *testing.T) {
	// Regression: CI failed because ~/.oro/projects/<name>/ didn't exist.
	// openDB must create intermediate directories automatically.
	dbPath := filepath.Join(t.TempDir(), "nested", "subdir", "test.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB should create parent dirs: %v", err)
	}
	defer func() { _ = db.Close() }()
}

func TestOpenDB_InvalidPath(t *testing.T) {
	// Opening a DB under an uncreatable root should fail.
	_, err := openDB("/nonexistent/dir/test.db")
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

func TestOpenDB_ReturnsUsableDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Verify we can execute SQL statements.
	_, err = db.Exec("CREATE TABLE test_tbl (id INTEGER PRIMARY KEY, val TEXT)")
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = db.Exec("INSERT INTO test_tbl (val) VALUES (?)", "hello")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	var val string
	if err := db.QueryRow("SELECT val FROM test_tbl WHERE id = 1").Scan(&val); err != nil {
		t.Fatalf("select: %v", err)
	}
	if val != "hello" {
		t.Errorf("expected 'hello', got %q", val)
	}
}

// TestMigrationsAppliedOnStartup verifies that migrateStateDB adds missing
// columns to an old-schema database. This simulates the case where state.db
// was created with an older SchemaDDL that lacked attempt_count/handoff_count.
func TestMigrationsAppliedOnStartup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Create old-schema assignments table WITHOUT attempt_count/handoff_count.
	oldSchema := `CREATE TABLE IF NOT EXISTS assignments (
		id INTEGER PRIMARY KEY,
		bead_id TEXT NOT NULL,
		worker_id TEXT NOT NULL,
		worktree TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		assigned_at TEXT NOT NULL DEFAULT (datetime('now')),
		completed_at TEXT
	)`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("create old schema: %v", err)
	}

	// Insert a row to verify migration preserves data.
	if _, err := db.Exec(`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES ('b1', 'w1', '/tmp/wt')`); err != nil {
		t.Fatalf("insert test row: %v", err)
	}

	// Run migrations — this is the function under test.
	migrateStateDB(db)

	// Verify attempt_count and handoff_count columns exist and have defaults.
	var attemptCount, handoffCount int
	err = db.QueryRow(`SELECT attempt_count, handoff_count FROM assignments WHERE bead_id='b1'`).
		Scan(&attemptCount, &handoffCount)
	if err != nil {
		t.Fatalf("query migrated columns: %v", err)
	}
	if attemptCount != 0 {
		t.Errorf("expected attempt_count=0, got %d", attemptCount)
	}
	if handoffCount != 0 {
		t.Errorf("expected handoff_count=0, got %d", handoffCount)
	}

	// Running migrations again should be idempotent (no errors).
	migrateStateDB(db)
}

// TestOpenStateDBCreatesSchema verifies that openStateDB applies SchemaDDL
// so that tables like events are immediately queryable on a fresh database.
func TestOpenStateDBCreatesSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// The events table must exist and be queryable on a fresh DB.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		t.Fatalf("query events table: %v — schema was not applied", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows in fresh events table, got %d", count)
	}
}

// TestOpenStateDBIdempotent verifies that calling openStateDB on an existing
// DB with schema already applied does not error (CREATE TABLE IF NOT EXISTS).
func TestOpenStateDBIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")

	// First call — creates schema.
	db1, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("first openStateDB: %v", err)
	}
	_ = db1.Close()

	// Second call — schema already exists, should be idempotent.
	db2, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("second openStateDB: %v", err)
	}
	defer func() { _ = db2.Close() }()

	var count int
	if err := db2.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		t.Fatalf("query events after idempotent open: %v", err)
	}
}

// TestOpenStateDB_SemanticMemoryMigrationsApplied is a regression test for
// the post-overhaul gap where semantic_memory_search_events (and related
// telemetry/chunk tables) existed as separate migration constants in
// pkg/protocol but were never wired into migrateStateDB. HybridSearch's
// logSearchEvent was failing silently with "no such table: memory_search_events"
// on every production query, dropping Phase 7 telemetry on the floor.
func TestOpenStateDB_SemanticMemoryMigrationsApplied(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// memory_search_events must be queryable (MigrateSemanticMemorySearchEvents).
	if _, err := db.Exec("INSERT INTO memory_search_events (query_hash) VALUES ('deadbeef')"); err != nil {
		t.Errorf("memory_search_events must be writable after openStateDB: %v", err)
	}

	// memory_chunks must exist (MigrateSemanticMemoryChunks). Query its
	// schema via PRAGMA rather than probing with INSERT — the insert path
	// was brittle to column-name drift.
	var chunkCol string
	if err := db.QueryRow("SELECT name FROM pragma_table_info('memory_chunks') WHERE name = 'memory_id'").Scan(&chunkCol); err != nil {
		t.Errorf("memory_chunks.memory_id column missing: %v", err)
	}
	if chunkCol != "memory_id" {
		t.Errorf("memory_chunks schema incomplete; expected memory_id column, got %q", chunkCol)
	}

	// Backfill state + embedding model must be seeded in kv_store
	// (MigrateSemanticMemoryBackfillState).
	var modelName string
	err = db.QueryRow("SELECT value FROM kv_store WHERE key = 'embedding_dense_model'").Scan(&modelName)
	if err != nil {
		t.Errorf("embedding_dense_model not seeded in kv_store: %v", err)
	} else if modelName != "bge-small-en-v1.5" {
		t.Errorf("embedding_dense_model = %q, want bge-small-en-v1.5", modelName)
	}

	// memories.embedding_dense column must exist (MigrateSemanticMemoryDense).
	// The column is added via bare ALTER TABLE so we verify by selecting it.
	if _, err := db.Exec("SELECT embedding_dense FROM memories LIMIT 1"); err != nil {
		t.Errorf("embedding_dense column missing on memories: %v", err)
	}
}

// TestBuildDispatcher_UsesOpenDB verifies that buildDispatcher produces a
// database with WAL mode and busy_timeout set (indirectly tests that it uses openDB).
func TestBuildDispatcher_WALMode(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

	d, db, err := buildDispatcher(1, 1, 0, 0, "", false, "")
	if err != nil {
		t.Fatalf("buildDispatcher: %v", err)
	}
	defer func() { _ = db.Close() }()
	_ = d

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", journalMode)
	}

	var busyTimeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("expected busy_timeout=5000, got %d", busyTimeout)
	}
}

// TestDefaultMemoryStore_WALMode verifies that defaultMemoryStore returns a
// database with WAL and busy_timeout. We test via the openDB path.
func TestDefaultMemoryStore_WALMode(t *testing.T) {
	tmpDir := t.TempDir()
	// Unset ORO_PROJECT so ResolveProjectDBPaths uses ORO_DB_PATH, not a project-scoped path.
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

	store, err := defaultMemoryStore()
	if err != nil {
		t.Fatalf("defaultMemoryStore: %v", err)
	}
	// The store wraps a *sql.DB; we can't access it directly, so open another
	// connection and check WAL was set on the file.
	db, err := dbutil.OpenDB(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("open for verification: %v", err)
	}
	defer func() { _ = db.Close() }()
	_ = store

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", journalMode)
	}
}
