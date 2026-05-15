package dispatcher //nolint:testpackage // white-box: asserts releasePriorAssignment preserves work + external close

import (
	"context"
	"testing"

	"oro/pkg/protocol"
)

// TestReleasePriorAssignmentPreservesWorktreeAndBranch asserts that when a
// worker is reassigned away from a bead, the prior assignment is requeued and
// its worktree/agent branch remain available for inspection or retry.
func TestReleasePriorAssignmentPreservesWorktreeAndBranch(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	const priorID = "oro-prior-cleanup"
	const newID = "oro-next"
	priorWorktree := "/tmp/worktree-" + priorID

	priorAssignmentID, err := d.createAssignment(ctx, priorID, "w1", priorWorktree)
	if err != nil {
		t.Fatalf("createAssignment: %v", err)
	}

	w := &trackedWorker{id: "w1", beadID: priorID, assignmentID: priorAssignmentID}
	d.mu.Lock()
	d.workers["w1"] = w
	d.worktreeByBead[priorID] = priorWorktree
	d.mu.Unlock()

	d.releasePriorAssignment(ctx, w, newID)

	wtMgr.mu.Lock()
	removed := append([]string(nil), wtMgr.removed...)
	deletedBranches := append([]string(nil), wtMgr.deletedBranches...)
	wtMgr.mu.Unlock()

	if len(removed) != 0 {
		t.Fatalf("prior worktree should be preserved, removed=%v", removed)
	}
	if len(deletedBranches) != 0 {
		t.Fatalf("prior branch should be preserved, deletedBranches=%v", deletedBranches)
	}

	var status string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, priorAssignmentID).Scan(&status); err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if status != "requeued" {
		t.Fatalf("assignment status = %q, want requeued", status)
	}

	d.mu.Lock()
	tracked := d.worktreeByBead[priorID]
	d.mu.Unlock()
	if tracked != priorWorktree {
		t.Fatalf("worktreeByBead[%q] = %q, want %q", priorID, tracked, priorWorktree)
	}

	if eventCount(t, d.db, "worker_abandon_work_preserved") == 0 {
		t.Fatal("expected worker_abandon_work_preserved event")
	}
}

// TestReleasePriorAssignmentPreservesExternalClose asserts that
// releasePriorAssignment does NOT reopen a bead that an external party (e.g.
// the manager via dedup) has closed. Prior behaviour unconditionally set the
// status back to "open", masking the close and letting the bead be picked up
// again — feeding the oro-jev9 race.
//
// Regression: oro-wp74.
func TestReleasePriorAssignmentPreservesExternalClose(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	const priorID = "oro-prior-deduped"
	const newID = "oro-next"

	priorAssignmentID, err := d.createAssignment(ctx, priorID, "w1", "/tmp/wt-"+priorID)
	if err != nil {
		t.Fatalf("createAssignment: %v", err)
	}

	w := &trackedWorker{id: "w1", beadID: priorID, assignmentID: priorAssignmentID}
	d.mu.Lock()
	d.workers["w1"] = w
	d.worktreeByBead[priorID] = "/tmp/wt-" + priorID
	d.mu.Unlock()

	beadSrc.mu.Lock()
	beadSrc.shown = map[string]*protocol.BeadDetail{
		priorID: {ID: priorID, Status: "closed"},
	}
	beadSrc.mu.Unlock()

	d.releasePriorAssignment(ctx, w, newID)

	beadSrc.mu.Lock()
	gotStatus, recorded := beadSrc.updated[priorID]
	beadSrc.mu.Unlock()
	if recorded {
		t.Fatalf("releasePriorAssignment must NOT reopen externally-closed bead, but updated[%q]=%q", priorID, gotStatus)
	}
}
