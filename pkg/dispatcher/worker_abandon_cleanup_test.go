package dispatcher //nolint:testpackage // white-box: asserts releasePriorAssignment cleans up + preserves external close

import (
	"context"
	"testing"

	"oro/pkg/protocol"
)

// TestReleasePriorAssignmentCleansWorktreeAndBranch asserts that when a
// worker is reassigned away from a bead, the prior bead's worktree is
// removed and its agent branch is deleted. Otherwise the orphan branch can
// be picked up later (e.g. by a re-assignment cycle) and merged on top of
// an already-closed bead — exactly what oro-jev9 was observed doing live.
//
// Regression: oro-wp74.
func TestReleasePriorAssignmentCleansWorktreeAndBranch(t *testing.T) {
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

	foundRemove := false
	for _, p := range removed {
		if p == priorWorktree {
			foundRemove = true
			break
		}
	}
	if !foundRemove {
		t.Fatalf("expected prior worktree %q to be removed, removed=%v", priorWorktree, removed)
	}

	wantBranch := protocol.BranchPrefix + priorID
	foundDelete := false
	for _, b := range deletedBranches {
		if b == wantBranch {
			foundDelete = true
			break
		}
	}
	if !foundDelete {
		t.Fatalf("expected prior branch %q to be deleted, deletedBranches=%v", wantBranch, deletedBranches)
	}

	d.mu.Lock()
	_, stillTracked := d.worktreeByBead[priorID]
	d.mu.Unlock()
	if stillTracked {
		t.Fatalf("worktreeByBead[%q] must be cleared after release", priorID)
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
