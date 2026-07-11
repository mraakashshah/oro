package dispatcher //nolint:testpackage // white-box: asserts AbandonAllActiveAssignments mutates DB rows directly

import (
	"context"
	"path/filepath"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestAbandonAllActiveAssignments(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("apply bead schema: %v", err)
	}

	// Seed beads: one closed, one open. The orphan assignment has NO bead row.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO beads (id, title, status) VALUES ('bead-closed','closed work','closed')`); err != nil {
		t.Fatalf("seed closed bead: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO beads (id, title, status) VALUES ('bead-open','open work','open')`); err != nil {
		t.Fatalf("seed open bead: %v", err)
	}

	// Seed three active assignments: closed bead, open bead, orphan (no bead).
	for _, a := range []struct{ bead, worker, worktree string }{
		{"bead-closed", "dead-1", "/tmp/wt-closed"},
		{"bead-open", "dead-2", "/tmp/wt-open"},
		{"bead-orphan", "dead-3", "/tmp/wt-orphan"},
	} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?,?,?,'active')`,
			a.bead, a.worker, a.worktree); err != nil {
			t.Fatalf("seed assignment %s: %v", a.bead, err)
		}
	}

	res, err := AbandonAllActiveAssignments(ctx, db)
	if err != nil {
		t.Fatalf("AbandonAllActiveAssignments: %v", err)
	}

	if res.Total != 3 {
		t.Errorf("Total = %d, want 3", res.Total)
	}
	if res.WithBead != 2 {
		t.Errorf("WithBead = %d, want 2", res.WithBead)
	}
	if res.Orphaned != 1 {
		t.Errorf("Orphaned = %d, want 1", res.Orphaned)
	}
	if len(res.Quarantined) != 3 {
		t.Fatalf("Quarantined entries = %d, want 3", len(res.Quarantined))
	}

	// No active assignments remain.
	var activeCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assignments WHERE status='active'`).Scan(&activeCount); err != nil {
		t.Fatalf("count active: %v", err)
	}
	if activeCount != 0 {
		t.Errorf("active assignments = %d, want 0", activeCount)
	}

	// All three are quarantined.
	var quarantinedCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assignments WHERE status='quarantined'`).Scan(&quarantinedCount); err != nil {
		t.Fatalf("count quarantined: %v", err)
	}
	if quarantinedCount != 3 {
		t.Errorf("quarantined assignments = %d, want 3", quarantinedCount)
	}

	// Three open recovery quarantine rows.
	var openRows int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_quarantines WHERE status='open'`).Scan(&openRows); err != nil {
		t.Fatalf("count open quarantines: %v", err)
	}
	if openRows != 3 {
		t.Errorf("open recovery_quarantines = %d, want 3", openRows)
	}

	// Orphan carries the distinct reason; others carry stale_active_assignment.
	var orphanReason string
	if err := db.QueryRowContext(ctx,
		`SELECT reason FROM recovery_quarantines WHERE bead_id='bead-orphan' AND status='open'`).Scan(&orphanReason); err != nil {
		t.Fatalf("query orphan reason: %v", err)
	}
	if orphanReason != "orphan_bead_assignment" {
		t.Errorf("orphan reason = %q, want orphan_bead_assignment", orphanReason)
	}
	for _, beadID := range []string{"bead-closed", "bead-open"} {
		var reason string
		if err := db.QueryRowContext(ctx,
			`SELECT reason FROM recovery_quarantines WHERE bead_id=? AND status='open'`, beadID).Scan(&reason); err != nil {
			t.Fatalf("query reason for %s: %v", beadID, err)
		}
		if reason != "stale_active_assignment" {
			t.Errorf("%s reason = %q, want stale_active_assignment", beadID, reason)
		}
	}

	// beads rows must be unchanged (never touched by recovery).
	var closedStatus, openStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM beads WHERE id='bead-closed'`).Scan(&closedStatus); err != nil {
		t.Fatalf("query closed bead: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM beads WHERE id='bead-open'`).Scan(&openStatus); err != nil {
		t.Fatalf("query open bead: %v", err)
	}
	if closedStatus != "closed" {
		t.Errorf("bead-closed status = %q, want closed", closedStatus)
	}
	if openStatus != "open" {
		t.Errorf("bead-open status = %q, want open", openStatus)
	}
	var beadCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads`).Scan(&beadCount); err != nil {
		t.Fatalf("count beads: %v", err)
	}
	if beadCount != 2 {
		t.Errorf("bead count = %d, want 2 (orphan must not be created)", beadCount)
	}

	// Idempotency: running again neither errors nor double-inserts.
	res2, err := AbandonAllActiveAssignments(ctx, db)
	if err != nil {
		t.Fatalf("second AbandonAllActiveAssignments: %v", err)
	}
	if res2.Total != 0 {
		t.Errorf("second run Total = %d, want 0 (nothing left active)", res2.Total)
	}
	var openRowsAfter int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_quarantines WHERE status='open'`).Scan(&openRowsAfter); err != nil {
		t.Fatalf("count open quarantines after second run: %v", err)
	}
	if openRowsAfter != 3 {
		t.Errorf("open recovery_quarantines after second run = %d, want 3 (no double-insert)", openRowsAfter)
	}
}
