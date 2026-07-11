package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// seedResolveAllStore builds a temp state DB with:
//   - two empty-safe quarantines (no branch, no worktree) that
//     discard-empty-safe resolves cleanly, and
//   - one quarantine backed by a real git worktree with a dirty tracked file,
//     which discard-empty-safe MUST refuse (Dirty.Total > 0).
//
// It returns the db path plus the ids of the empty-safe rows and the dirty row.
func seedResolveAllStore(t *testing.T) (dbPath string, emptySafeIDs []int64, dirtyID int64) {
	t.Helper()
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath = filepath.Join(tmpDir, "state.db")

	// Real git worktree with a modified tracked file makes discard-empty-safe
	// refuse: inspectRecoveryDirty reports Total > 0.
	worktree := filepath.Join(tmpDir, "repo")
	initRecoveryTestRepo(t, worktree, "agent/oro-resolve-all-dirty")
	if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()

	for i := 0; i < 2; i++ {
		res, err := db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, reason, details, status)
VALUES (?, 'stale_active_assignment', 'no branch, no worktree', 'open')`,
			"oro-empty-safe-"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("seed empty-safe quarantine: %v", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("empty-safe id: %v", err)
		}
		emptySafeIDs = append(emptySafeIDs, id)
	}

	res, err := db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, worktree, branch, reason, details, status)
VALUES ('oro-dirty', ?, 'agent/oro-resolve-all-dirty', 'stale_active_assignment', 'dirty worktree', 'open')`,
		worktree)
	if err != nil {
		t.Fatalf("seed dirty quarantine: %v", err)
	}
	dirtyID, err = res.LastInsertId()
	if err != nil {
		t.Fatalf("dirty id: %v", err)
	}
	return dbPath, emptySafeIDs, dirtyID
}

func TestRecoveryResolveAllDiscardEmptySafeResolvesSafeSkipsDirty(t *testing.T) {
	dbPath, emptySafeIDs, dirtyID := seedResolveAllStore(t)
	t.Setenv("ORO_HUMAN_CONFIRMED", "1")

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()

	var out bytes.Buffer
	cfg := recoveryResolveAllConfig{
		db:    db,
		mode:  "discard-empty-safe",
		force: true,
		w:     &out,
		stdin: strings.NewReader(""),
		isTTY: func() bool { return false },
	}
	if err := runRecoveryResolveAll(context.Background(), cfg); err != nil {
		t.Fatalf("runRecoveryResolveAll: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "resolved 2, skipped 1 of 3") {
		t.Fatalf("summary missing counts:\n%s", got)
	}
	if !strings.Contains(got, "discard-empty-safe") {
		t.Fatalf("summary missing mode:\n%s", got)
	}
	if !strings.Contains(got, "skipped #"+strconv.FormatInt(dirtyID, 10)) {
		t.Fatalf("summary missing skipped dirty row #%d:\n%s", dirtyID, got)
	}

	// Empty-safe rows are resolved.
	for _, id := range emptySafeIDs {
		var status string
		if err := db.QueryRowContext(context.Background(),
			`SELECT status FROM recovery_quarantines WHERE id=?`, id).Scan(&status); err != nil {
			t.Fatalf("query empty-safe status: %v", err)
		}
		if status != "resolved" {
			t.Errorf("empty-safe #%d status = %q, want resolved", id, status)
		}
	}
	// Dirty row stays open (skipped, not resolved).
	var dirtyStatus string
	if err := db.QueryRowContext(context.Background(),
		`SELECT status FROM recovery_quarantines WHERE id=?`, dirtyID).Scan(&dirtyStatus); err != nil {
		t.Fatalf("query dirty status: %v", err)
	}
	if dirtyStatus != "open" {
		t.Errorf("dirty #%d status = %q, want open (skipped)", dirtyID, dirtyStatus)
	}
}

func TestRecoveryResolveAllRequiresMode(t *testing.T) {
	dbPath, _, _ := seedResolveAllStore(t)
	t.Setenv("ORO_HUMAN_CONFIRMED", "1")

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()

	cfg := recoveryResolveAllConfig{
		db:    db,
		mode:  "",
		force: true,
		w:     &bytes.Buffer{},
		stdin: strings.NewReader(""),
		isTTY: func() bool { return false },
	}
	if err := runRecoveryResolveAll(context.Background(), cfg); err == nil {
		t.Fatal("expected error for --all without --mode, got nil")
	}
}

func TestRecoveryResolveAllRefusesWithoutConfirmation(t *testing.T) {
	dbPath, emptySafeIDs, _ := seedResolveAllStore(t)
	os.Unsetenv("ORO_HUMAN_CONFIRMED")

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()

	cfg := recoveryResolveAllConfig{
		db:    db,
		mode:  "discard-empty-safe",
		force: false,
		w:     &bytes.Buffer{},
		stdin: strings.NewReader(""),
		isTTY: func() bool { return false },
	}
	if err := runRecoveryResolveAll(context.Background(), cfg); err == nil {
		t.Fatal("expected refusal without confirmation, got nil error")
	}

	// Nothing mutated: empty-safe rows stay open.
	for _, id := range emptySafeIDs {
		var status string
		if err := db.QueryRowContext(context.Background(),
			`SELECT status FROM recovery_quarantines WHERE id=?`, id).Scan(&status); err != nil {
			t.Fatalf("query status: %v", err)
		}
		if status != "open" {
			t.Errorf("#%d status = %q, want open (unchanged)", id, status)
		}
	}
}

func TestRecoveryResolveAllRejectsPositionalIDWithAllFlag(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	db.Close()

	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"recovery", "resolve", "1", "--all", "--mode", "discard-empty-safe"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected error when both a positional id and --all are given, got nil")
	}
}

func TestRecoveryResolveAllWithoutModeThroughCommand(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	t.Setenv("ORO_HUMAN_CONFIRMED", "1")

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	db.Close()

	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"recovery", "resolve", "--all", "--force"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected error for --all without --mode, got nil")
	}
}
