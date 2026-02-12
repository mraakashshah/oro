package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
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

func TestOpenDB_InvalidPath(t *testing.T) {
	// Opening a DB in a non-existent directory should fail on Ping.
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

// TestBuildDispatcher_UsesOpenDB verifies that buildDispatcher produces a
// database with WAL mode and busy_timeout set (indirectly tests that it uses openDB).
func TestBuildDispatcher_WALMode(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

	d, db, err := buildDispatcher(1)
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
	t.Setenv("ORO_MEMORY_DB", filepath.Join(tmpDir, "memories.db"))

	store, err := defaultMemoryStore()
	if err != nil {
		t.Fatalf("defaultMemoryStore: %v", err)
	}
	// The store wraps a *sql.DB; we can't access it directly, so open another
	// connection and check WAL was set on the file.
	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "memories.db"))
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
