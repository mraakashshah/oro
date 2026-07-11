package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedOfflineRecoveryStore builds a temp state DB with a stale active
// assignment (bead present) and returns its path.
func seedOfflineRecoveryStore(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO beads (id, title, status) VALUES ('bead-a','a','open')`); err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('bead-a','dead-w','/tmp/wt-a','active')`); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	return dbPath
}

func TestRecoveryAbandonStaleForceQuarantinesAndBacksUp(t *testing.T) {
	dbPath := seedOfflineRecoveryStore(t)
	t.Setenv("ORO_HUMAN_CONFIRMED", "1")

	cfg := recoveryAbandonConfig{
		stateDBPath: dbPath,
		force:       true,
		w:           &bytes.Buffer{},
		stdin:       strings.NewReader(""),
		isTTY:       func() bool { return false },
		daemonLive:  func() (bool, int) { return false, 0 },
	}

	if err := runRecoveryAbandonStale(context.Background(), cfg); err != nil {
		t.Fatalf("runRecoveryAbandonStale: %v", err)
	}

	// A timestamped backup of state.db must have been created.
	matches, err := filepath.Glob(dbPath + ".bak-*")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("expected a state.db.bak-* backup, found none")
	}

	// The active assignment is now quarantined.
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRowContext(context.Background(),
		`SELECT status FROM assignments WHERE bead_id='bead-a'`).Scan(&status); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "quarantined" {
		t.Errorf("assignment status = %q, want quarantined", status)
	}
	var openRows int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM recovery_quarantines WHERE status='open'`).Scan(&openRows); err != nil {
		t.Fatalf("count quarantines: %v", err)
	}
	if openRows != 1 {
		t.Errorf("open recovery_quarantines = %d, want 1", openRows)
	}
}

func TestRecoveryAbandonStaleRefusesWithoutConfirmation(t *testing.T) {
	dbPath := seedOfflineRecoveryStore(t)
	// No ORO_HUMAN_CONFIRMED, not a TTY, no --force.
	os.Unsetenv("ORO_HUMAN_CONFIRMED")

	cfg := recoveryAbandonConfig{
		stateDBPath: dbPath,
		force:       false,
		w:           &bytes.Buffer{},
		stdin:       strings.NewReader(""),
		isTTY:       func() bool { return false },
		daemonLive:  func() (bool, int) { return false, 0 },
	}

	if err := runRecoveryAbandonStale(context.Background(), cfg); err == nil {
		t.Fatal("expected refusal without confirmation, got nil error")
	}

	// Nothing should have been mutated: assignment stays active.
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRowContext(context.Background(),
		`SELECT status FROM assignments WHERE bead_id='bead-a'`).Scan(&status); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "active" {
		t.Errorf("assignment status = %q, want active (unchanged)", status)
	}
}

func TestRecoveryAbandonStaleRefusesWhenDaemonLive(t *testing.T) {
	dbPath := seedOfflineRecoveryStore(t)
	t.Setenv("ORO_HUMAN_CONFIRMED", "1")

	cfg := recoveryAbandonConfig{
		stateDBPath: dbPath,
		force:       true,
		w:           &bytes.Buffer{},
		stdin:       strings.NewReader(""),
		isTTY:       func() bool { return false },
		daemonLive:  func() (bool, int) { return true, 4321 },
	}

	err := runRecoveryAbandonStale(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected refusal when dispatcher is live, got nil error")
	}
	if !strings.Contains(err.Error(), "4321") {
		t.Errorf("error = %v, want it to mention the live PID 4321", err)
	}
}
