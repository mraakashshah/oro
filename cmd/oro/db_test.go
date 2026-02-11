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
