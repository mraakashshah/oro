package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// seedPreV4Store builds a temp state DB that NEEDS the v4 migration. The v3
// schema built by openStateDB already carries the legacy `gate_state` column
// that needsV4Migration keys on, so simulating a pre-v4 store only requires
// resetting user_version to 0. Returns its path.
func seedPreV4Store(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 0`); err != nil {
		t.Fatalf("reset user_version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	return dbPath
}

// seedPreV4StoreWithActiveAssignment builds a pre-v4 store that also has a
// status='active' assignment, which blocks the v4 migration.
func seedPreV4StoreWithActiveAssignment(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dbPath := seedPreV4Store(t)
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO beads (id, title, status) VALUES ('bead-a','a','open')`); err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('bead-a','dead-w','/tmp/wt-a','active')`); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	// openStateDB may re-mark user_version; force it back to pre-v4 so the
	// migration path (not the fast no-op) is exercised.
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 0`); err != nil {
		t.Fatalf("reset user_version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	return dbPath
}

func doctorTestUserVersion(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db for user_version: %v", err)
	}
	defer func() { _ = db.Close() }()
	var v int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return v
}

func TestDoctorMigrateMigratesV0Store(t *testing.T) {
	dbPath := seedPreV4Store(t)

	var buf bytes.Buffer
	cfg := doctorMigrateConfig{
		stateDBPath: dbPath,
		w:           &buf,
		daemonLive:  func() (bool, int) { return false, 0 },
	}

	if err := runDoctorMigrate(context.Background(), cfg); err != nil {
		t.Fatalf("runDoctorMigrate: %v", err)
	}

	if got := doctorTestUserVersion(t, dbPath); got != 4 {
		t.Errorf("user_version = %d, want 4", got)
	}
}

func TestDoctorMigrateNoOpOnV4Store(t *testing.T) {
	// A fresh openStateDBWithV4Migration store lands at v4 already.
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openStateDBWithV4Migration(dbPath)
	if err != nil {
		t.Fatalf("prepare v4 store: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if before := doctorTestUserVersion(t, dbPath); before != 4 {
		t.Fatalf("precondition: want v4 store, got user_version=%d", before)
	}

	var buf bytes.Buffer
	cfg := doctorMigrateConfig{
		stateDBPath: dbPath,
		w:           &buf,
		daemonLive:  func() (bool, int) { return false, 0 },
	}

	if err := runDoctorMigrate(context.Background(), cfg); err != nil {
		t.Fatalf("runDoctorMigrate on v4 store: %v", err)
	}
	if got := doctorTestUserVersion(t, dbPath); got != 4 {
		t.Errorf("user_version = %d, want 4 (unchanged)", got)
	}
	if out := buf.String(); !strings.Contains(strings.ToLower(out), "already") {
		t.Errorf("output = %q, want it to say already migrated", out)
	}
}

func TestDoctorMigrateBlockedByActiveAssignments(t *testing.T) {
	dbPath := seedPreV4StoreWithActiveAssignment(t)

	var buf bytes.Buffer
	cfg := doctorMigrateConfig{
		stateDBPath: dbPath,
		w:           &buf,
		daemonLive:  func() (bool, int) { return false, 0 },
	}

	err := runDoctorMigrate(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when active assignments block migration, got nil")
	}
	if !strings.Contains(err.Error(), "abandon-stale") {
		t.Errorf("error = %v, want it to mention abandon-stale", err)
	}
	if got := doctorTestUserVersion(t, dbPath); got >= 4 {
		t.Errorf("user_version = %d, want < 4 (migration must not have run)", got)
	}
}

func TestDoctorMigrateRefusesWhenDaemonLive(t *testing.T) {
	dbPath := seedPreV4Store(t)

	var buf bytes.Buffer
	cfg := doctorMigrateConfig{
		stateDBPath: dbPath,
		w:           &buf,
		daemonLive:  func() (bool, int) { return true, 4321 },
	}

	err := runDoctorMigrate(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected refusal when dispatcher is live, got nil")
	}
	if !strings.Contains(err.Error(), "4321") {
		t.Errorf("error = %v, want it to mention the live PID 4321", err)
	}
	if got := doctorTestUserVersion(t, dbPath); got >= 4 {
		t.Errorf("user_version = %d, want < 4 (migration must not have run)", got)
	}
}

func TestDoctorDiagnoseBlockedVerdict(t *testing.T) {
	dbPath := seedPreV4StoreWithActiveAssignment(t)

	var buf bytes.Buffer
	cfg := doctorDiagnoseConfig{
		stateDBPath: dbPath,
		w:           &buf,
		daemonLive:  func() (bool, int) { return false, 0 },
	}

	if err := runDoctorDiagnose(context.Background(), cfg); err != nil {
		t.Fatalf("runDoctorDiagnose: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "BLOCKED") {
		t.Errorf("output = %q, want it to contain the BLOCKED verdict", out)
	}
	if !strings.Contains(out, "abandon-stale") {
		t.Errorf("output = %q, want it to mention abandon-stale", out)
	}
}

func TestDoctorCmdRejectsLegacyRecoverDoltArg(t *testing.T) {
	cmd := newDoctorCmd()
	cmd.SetArgs([]string{"recover-dolt"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	if err := cmd.Execute(); err == nil {
		t.Fatal("oro doctor recover-dolt succeeded; want argument rejection")
	}
}

func TestHelpTextDoctorDoesNotAdvertiseDoltRepair(t *testing.T) {
	for _, forbidden := range []string{"corrupt Dolt", "Diagnose and repair oro installation issues"} {
		if strings.Contains(helpText, forbidden) {
			t.Fatalf("helpText still contains %q", forbidden)
		}
	}
	if !strings.Contains(helpText, "doctor     Diagnose oro installation issues") {
		t.Fatal("helpText does not describe doctor diagnostics")
	}
}
