package dispatcher //nolint:testpackage // white-box tests for checkPreMergeQG lifecycle

import (
	"context"
	"strings"
	"testing"

	"oro/pkg/protocol"
)

// TestPreMergeQGFailureDoesNotOrphan verifies that when the pre-merge mutation
// QG fails the dispatcher:
//  1. Records a "qg_failed" event with the actionable QG output.
//  2. Requeues the bead to "open" (visible retryable state).
//  3. Completes the assignment in the DB so no in-progress bead is left with
//     an idle worker (no orphan).
func TestPreMergeQGFailureDoesNotOrphan(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const beadID = "bead-qg-orphan"
	const workerID = "w-qg-orphan"
	const worktree = "/tmp/worktree-qg-orphan"
	const qgOutput = "mutation testing failed: 3 surviving mutants in pkg/foo"

	// Insert an active assignment so we can verify it is completed.
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
		beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	d.qgRunner = &mockQGRunner{passed: false, output: qgOutput}
	d.mu.Lock()
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	d.checkPreMergeQG(ctx, beadID, workerID, worktree, assignmentID)

	// (1) Event "qg_failed" logged with actionable output.
	var payload string
	err = d.db.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE type='qg_failed' AND bead_id=?`, beadID).Scan(&payload)
	if err != nil {
		t.Fatalf("expected 'qg_failed' event to be logged, got: %v", err)
	}
	if !strings.Contains(payload, qgOutput) {
		t.Errorf("qg_failed event payload = %q, want to contain QG output %q", payload, qgOutput)
	}

	// (2) Bead requeued to visible retryable state.
	beadSrc.mu.Lock()
	status, hasUpdate := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()
	if !hasUpdate || status != "open" {
		t.Errorf("bead status after QG fail = %q (updated=%v), want \"open\"", status, hasUpdate)
	}

	// (3) Assignment completed — no orphan in-progress assignment left.
	var assignStatus string
	err = d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignStatus)
	if err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if assignStatus != "completed" {
		t.Errorf("assignment status = %q after QG fail, want \"completed\" (orphan guard)", assignStatus)
	}
}

// TestClosedBeadNotReopenedByStaleAssignment verifies that when a bead is
// closed on main (e.g. merged externally) after its assignment was created but
// before the pre-merge QG result arrives, the dispatcher does NOT reopen the
// bead to "open". Cleanup (assignment completion, worktree removal) still runs.
func TestClosedBeadNotReopenedByStaleAssignment(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const beadID = "bead-closed-stale"
	const workerID = "w-closed-stale"
	const worktree = "/tmp/worktree-closed-stale"

	// Insert an active assignment (created before bead was closed externally).
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
		beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	// Bead has since been closed on main (before QG failure arrives).
	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "closed"}
	beadSrc.mu.Unlock()

	d.qgRunner = &mockQGRunner{passed: false, output: "mutation testing failed"}
	d.mu.Lock()
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	d.checkPreMergeQG(ctx, beadID, workerID, worktree, assignmentID)

	// Bead must NOT be reopened — it was already closed on main.
	beadSrc.mu.Lock()
	updatedStatus, wasUpdated := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()
	if wasUpdated {
		t.Errorf("closed bead was reopened to %q via stale QG failure, want no update", updatedStatus)
	}

	// Cleanup still ran: assignment must be completed (no orphan).
	var assignStatus string
	err = d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignStatus)
	if err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if assignStatus != "completed" {
		t.Errorf("assignment status = %q after closed-bead QG fail, want \"completed\"", assignStatus)
	}
}
