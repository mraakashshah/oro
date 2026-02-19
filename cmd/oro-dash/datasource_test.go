package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// writeFakeBd writes an executable shell script at fakeBin/bd that emits the given JSON.
// 0o755 is required for the script to be executable; the gosec G306 warning is suppressed
// because this is test-only code producing a throwaway temp file.
func writeFakeBd(t *testing.T, fakeBin, jsonPayload string) {
	t.Helper()
	script := "#!/bin/sh\necho '" + jsonPayload + "'\n"
	path := filepath.Join(fakeBin, "bd")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // G306: test-only executable stub
		t.Fatalf("write fake bd: %v", err)
	}
}

// TestFetchBeads verifies that FetchBeads runs `bd list --json` and parses
// the output into a []Bead slice. It exercises the happy path by stubbing out
// the bd CLI with a tiny shell script that emits known JSON.
func TestFetchBeads(t *testing.T) {
	fakeBeadsJSON := `[{"id":"oro-001","title":"First bead","status":"open","priority":1,"issue_type":"task"},{"id":"oro-002","title":"Second bead","status":"in_progress","priority":2,"issue_type":"bug"}]`

	fakeBin := t.TempDir()
	writeFakeBd(t, fakeBin, fakeBeadsJSON)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+":"+origPath)

	beads, err := FetchBeads()
	if err != nil {
		t.Fatalf("FetchBeads() error = %v", err)
	}

	if len(beads) != 2 {
		t.Fatalf("expected 2 beads, got %d", len(beads))
	}

	if beads[0].ID != "oro-001" {
		t.Errorf("beads[0].ID = %q, want %q", beads[0].ID, "oro-001")
	}
	if beads[0].Title != "First bead" {
		t.Errorf("beads[0].Title = %q, want %q", beads[0].Title, "First bead")
	}
	if beads[1].Status != "in_progress" {
		t.Errorf("beads[1].Status = %q, want %q", beads[1].Status, "in_progress")
	}
}

// TestFetchBeads_NotInPath verifies that FetchBeads returns an error when bd
// is not available on PATH.
func TestFetchBeads_NotInPath(t *testing.T) {
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	_, err := FetchBeads()
	if err == nil {
		t.Fatal("expected error when bd not in PATH, got nil")
	}
}

// TestFetchBeads_EmptyOutput verifies that FetchBeads returns an empty slice
// when bd outputs an empty JSON array.
func TestFetchBeads_EmptyOutput(t *testing.T) {
	fakeBin := t.TempDir()
	writeFakeBd(t, fakeBin, "[]")

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+":"+origPath)

	beads, err := FetchBeads()
	if err != nil {
		t.Fatalf("FetchBeads() error = %v", err)
	}
	if len(beads) != 0 {
		t.Fatalf("expected empty slice, got %d beads", len(beads))
	}
}

// TestFetchBeads_MalformedJSON verifies that FetchBeads returns a parse error
// when bd emits malformed JSON.
func TestFetchBeads_MalformedJSON(t *testing.T) {
	fakeBin := t.TempDir()
	writeFakeBd(t, fakeBin, "not json")

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+":"+origPath)

	_, err := FetchBeads()
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

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

// compile-time signature checks.
var (
	_ func() ([]Bead, error)               = FetchBeads
	_ func(string) ([]WorkerStatus, error) = FetchWorkers
)

// Ensure json and exec packages are referenced (used in implementation cross-check).
var (
	_ = json.Marshal
	_ = exec.LookPath
)
