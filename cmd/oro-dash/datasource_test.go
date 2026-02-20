package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// createTestDB opens a temporary sqlite DB, applies the assignments schema, and
// returns the path. The caller is responsible for closing and removing the DB.
func createTestDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("close test db: %v", cerr)
		}
	}()

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS assignments (
		id INTEGER PRIMARY KEY,
		bead_id TEXT NOT NULL,
		worker_id TEXT NOT NULL,
		worktree TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		assigned_at TEXT NOT NULL DEFAULT (datetime('now')),
		completed_at TEXT,
		attempt_count INTEGER DEFAULT 0,
		handoff_count INTEGER DEFAULT 0
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	return dbPath
}

// TestFetchWorkers_EmptyDB verifies that FetchWorkers returns an empty slice
// when the database has no active assignments.
func TestFetchWorkers_EmptyDB(t *testing.T) {
	dbPath := createTestDB(t)

	workers, err := FetchWorkers(dbPath)
	if err != nil {
		t.Fatalf("FetchWorkers() error = %v", err)
	}
	if len(workers) != 0 {
		t.Fatalf("expected 0 workers, got %d", len(workers))
	}
}

// TestFetchWorkers_ActiveAssignments verifies that FetchWorkers returns one
// WorkerStatus per active assignment row in the database.
func TestFetchWorkers_ActiveAssignments(t *testing.T) {
	dbPath := createTestDB(t)

	// Insert two active and one completed assignment.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	_, err = db.Exec(`INSERT INTO assignments (bead_id, worker_id, worktree, status)
		VALUES ('oro-001', 'worker-a', '/tmp/wt1', 'active'),
		       ('oro-002', 'worker-b', '/tmp/wt2', 'active'),
		       ('oro-003', 'worker-c', '/tmp/wt3', 'completed')`)
	if err != nil {
		t.Fatalf("insert rows: %v", err)
	}
	if cerr := db.Close(); cerr != nil {
		t.Fatalf("close db: %v", cerr)
	}

	workers, err := FetchWorkers(dbPath)
	if err != nil {
		t.Fatalf("FetchWorkers() error = %v", err)
	}

	if len(workers) != 2 {
		t.Fatalf("expected 2 active workers, got %d", len(workers))
	}

	// Verify field mapping via ID-keyed map (order may vary).
	byID := make(map[string]WorkerStatus)
	for _, w := range workers {
		byID[w.ID] = w
	}

	wa, ok := byID["worker-a"]
	if !ok {
		t.Fatal("worker-a not found in results")
	}
	if wa.BeadID != "oro-001" {
		t.Errorf("worker-a BeadID = %q, want %q", wa.BeadID, "oro-001")
	}
	if wa.Status != "active" {
		t.Errorf("worker-a Status = %q, want %q", wa.Status, "active")
	}
}

// TestFetchWorkers_DBNotFound verifies that FetchWorkers returns an error when
// the database file does not exist.
func TestFetchWorkers_DBNotFound(t *testing.T) {
	_, err := FetchWorkers("/nonexistent/path/state.db")
	if err == nil {
		t.Fatal("expected error for missing DB, got nil")
	}
}

// compile-time signature check.
var _ func(string) ([]WorkerStatus, error) = FetchWorkers
