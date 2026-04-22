package dispatcher

import (
	"context"
	"testing"
)

func TestAssignmentReassignmentLeavesSingleActiveRow(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	firstID, err := d.createAssignment(ctx, "bead-reassign", "w1", "/tmp/w1")
	if err != nil {
		t.Fatalf("create first assignment: %v", err)
	}

	if _, err := d.createAssignment(ctx, "bead-reassign", "w2", "/tmp/w2"); err == nil {
		t.Fatal("expected second active assignment for same bead to fail")
	}

	if err := d.completeAssignment(ctx, firstID, "bead-reassign"); err != nil {
		t.Fatalf("complete first assignment: %v", err)
	}

	secondID, err := d.createAssignment(ctx, "bead-reassign", "w2", "/tmp/w2")
	if err != nil {
		t.Fatalf("create second assignment after completion: %v", err)
	}

	var activeCount int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assignments WHERE bead_id=? AND status='active'`,
		"bead-reassign",
	).Scan(&activeCount); err != nil {
		t.Fatalf("count active assignments: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("expected exactly 1 active assignment, got %d", activeCount)
	}

	var activeID int64
	if err := d.db.QueryRowContext(ctx,
		`SELECT id FROM assignments WHERE bead_id=? AND status='active'`,
		"bead-reassign",
	).Scan(&activeID); err != nil {
		t.Fatalf("query active assignment id: %v", err)
	}
	if activeID != secondID {
		t.Fatalf("active assignment id: got %d, want %d", activeID, secondID)
	}
}

func TestCompleteAssignmentTargetsSpecificAttempt(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	firstID, err := d.createAssignment(ctx, "bead-a", "w1", "/tmp/a1")
	if err != nil {
		t.Fatalf("create first assignment: %v", err)
	}
	if err := d.completeAssignment(ctx, firstID, "bead-a"); err != nil {
		t.Fatalf("complete first assignment: %v", err)
	}

	secondID, err := d.createAssignment(ctx, "bead-a", "w2", "/tmp/a2")
	if err != nil {
		t.Fatalf("create second assignment: %v", err)
	}

	if err := d.completeAssignment(ctx, firstID, "bead-a"); err != nil {
		t.Fatalf("re-complete first assignment by id: %v", err)
	}

	var firstStatus, secondStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, firstID).Scan(&firstStatus); err != nil {
		t.Fatalf("query first status: %v", err)
	}
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, secondID).Scan(&secondStatus); err != nil {
		t.Fatalf("query second status: %v", err)
	}
	if firstStatus != "completed" {
		t.Fatalf("first assignment status: got %q, want completed", firstStatus)
	}
	if secondStatus != "active" {
		t.Fatalf("second assignment status: got %q, want active", secondStatus)
	}
}

func TestPersistBeadCountTargetsSpecificAttempt(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	firstID, err := d.createAssignment(ctx, "bead-b", "w1", "/tmp/b1")
	if err != nil {
		t.Fatalf("create first assignment: %v", err)
	}
	if err := d.completeAssignment(ctx, firstID, "bead-b"); err != nil {
		t.Fatalf("complete first assignment: %v", err)
	}

	secondID, err := d.createAssignment(ctx, "bead-b", "w2", "/tmp/b2")
	if err != nil {
		t.Fatalf("create second assignment: %v", err)
	}

	d.persistBeadCount(ctx, secondID, "bead-b", "attempt_count", 7)
	d.persistBeadCount(ctx, firstID, "bead-b", "attempt_count", 3)

	var firstCount, secondCount int
	if err := d.db.QueryRowContext(ctx, `SELECT attempt_count FROM assignments WHERE id=?`, firstID).Scan(&firstCount); err != nil {
		t.Fatalf("query first attempt_count: %v", err)
	}
	if err := d.db.QueryRowContext(ctx, `SELECT attempt_count FROM assignments WHERE id=?`, secondID).Scan(&secondCount); err != nil {
		t.Fatalf("query second attempt_count: %v", err)
	}
	if firstCount != 3 {
		t.Fatalf("first attempt_count: got %d, want 3", firstCount)
	}
	if secondCount != 7 {
		t.Fatalf("second attempt_count: got %d, want 7", secondCount)
	}
}
