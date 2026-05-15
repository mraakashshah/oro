package dispatcher //nolint:testpackage // white-box: asserts mergeAndComplete short-circuits before merger.Merge

import (
	"context"
	"testing"

	"oro/pkg/protocol"
)

// TestMergeAndCompleteAbortsOnClosedBead asserts that when a bead is already
// closed (e.g. by manager dedup) before the worker's review completes,
// mergeAndComplete must NOT call merger.Merge — otherwise an orphan commit can
// land on the target branch on top of an already-resolved bead.
//
// Regression: oro-jev9 — live observation 2026-05-04. Manager closed oro-0yse
// as duplicate of oro-mdrh while a worker was mid-review. Dispatcher proceeded
// to merge the worker's commit (8cc25431) to main on the already-closed bead.
func TestMergeAndCompleteAbortsOnClosedBead(t *testing.T) {
	d, beadSrc, wtMgr, _, gitRunner, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	const beadID = "bead-already-closed"

	beadSrc.mu.Lock()
	beadSrc.shown = map[string]*protocol.BeadDetail{
		beadID: {ID: beadID, Status: "closed"},
	}
	beadSrc.mu.Unlock()

	worktree := "/tmp/wt-" + beadID
	d.mergeAndComplete(ctx, beadID, "w-x", worktree, "agent/"+beadID, "", "", 0)

	if calls := gitRunner.RebaseCalls(); len(calls) != 0 {
		t.Fatalf("expected no rebase calls when bead is already closed, got %d: %v", len(calls), calls)
	}

	beadSrc.mu.Lock()
	closedCalls := append([]string(nil), beadSrc.closed...)
	beadSrc.mu.Unlock()
	for _, id := range closedCalls {
		if id == beadID {
			t.Fatalf("dispatcher must not re-close already-closed bead %q (would emit \"Merged: <sha>\" reason masking the dedup close)", beadID)
		}
	}

	wtMgr.mu.Lock()
	removed := append([]string(nil), wtMgr.removed...)
	wtMgr.mu.Unlock()
	if len(removed) != 0 {
		t.Fatalf("closed-bead merge abort has no merge proof; worktree should be preserved, removed=%v", removed)
	}
	if eventCount(t, d.db, "recovery_work_quarantined") == 0 {
		t.Fatalf("expected recovery_work_quarantined event")
	}
}

func TestFinalizeSuccessfulMergeCompletesAssignmentBeforeClosingBead(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const (
		beadID   = "bead-merged-cleanup-order"
		workerID = "worker-merged-cleanup-order"
		worktree = "/tmp/wt-bead-merged-cleanup-order"
	)

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
	beadSrc.closeFn = func(ctx context.Context, id, reason string) error {
		if id != beadID {
			t.Fatalf("CloseBead id = %q, want %q", id, beadID)
		}
		var activeAtClose int
		if err := d.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM assignments WHERE bead_id=? AND status='active'`,
			beadID).Scan(&activeAtClose); err != nil {
			t.Fatalf("count active assignments at close: %v", err)
		}
		if activeAtClose != 0 {
			t.Fatalf("active assignments at CloseBead = %d, want 0", activeAtClose)
		}
		return nil
	}
	beadSrc.mu.Unlock()

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`,
		beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}

	d.finalizeSuccessfulMerge(ctx, beadID, workerID, worktree, "", "", assignmentID, "abc123")
}
