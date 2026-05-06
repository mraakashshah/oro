package dispatcher //nolint:testpackage // internal white-box test exercising dispatcher state

import (
	"context"
	"testing"

	"oro/pkg/protocol"
)

// TestWorkerAbandonClearsBeadAssignment proves that when a worker is
// reassigned mid-run (its prior bead never reached DONE), the dispatcher
// finalizes the prior assignment row and returns the prior bead's status to
// "open" so it remains visible to oro task ready. Covers oro-xqrh: in the
// 2026-05-04 dispatcher proof run, two beads (oro-m1nv, oro-v5km) were
// observed stuck status=in_progress with worker_id pointing at a worker that
// had moved on to a different bead. Without this release, the bead is leaked
// — invisible to ready while no one is working on it.
func TestWorkerAbandonClearsBeadAssignment(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const priorID = "oro-prior"
	const newID = "oro-next"

	priorAssignmentID, err := d.createAssignment(ctx, priorID, "w1", "/tmp/oro-prior")
	if err != nil {
		t.Fatalf("createAssignment: %v", err)
	}

	w := &trackedWorker{
		id:           "w1",
		beadID:       priorID,
		assignmentID: priorAssignmentID,
	}
	d.mu.Lock()
	d.workers["w1"] = w
	d.mu.Unlock()

	beadSrc.mu.Lock()
	beadSrc.updated = map[string]string{priorID: "in_progress"}
	beadSrc.mu.Unlock()

	d.releasePriorAssignment(ctx, w, newID)

	beadSrc.mu.Lock()
	gotStatus := beadSrc.updated[priorID]
	beadSrc.mu.Unlock()
	if gotStatus != "open" {
		t.Fatalf("prior bead status = %q, want %q (worker_abandon must release)", gotStatus, "open")
	}

	var assignmentStatus string
	if err := d.db.QueryRowContext(ctx,
		`SELECT status FROM assignments WHERE id=?`, priorAssignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if assignmentStatus != "completed" {
		t.Fatalf("prior assignment status = %q, want %q", assignmentStatus, "completed")
	}
}

// TestWorkerAbandonUsesPersistedAssignmentWhenBeadStateCleared covers the
// observed oro-fksf drift: the worker was live and reassigned, but its previous
// active assignment survived because in-memory beadID had already been cleared.
func TestWorkerAbandonUsesPersistedAssignmentWhenBeadStateCleared(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const priorID = "oro-prior-cleared"
	const newID = "oro-next-cleared"

	priorAssignmentID, err := d.createAssignment(ctx, priorID, "w1", "/tmp/oro-prior-cleared")
	if err != nil {
		t.Fatalf("createAssignment: %v", err)
	}

	w := &trackedWorker{
		id:           "w1",
		assignmentID: priorAssignmentID,
	}
	d.mu.Lock()
	d.workers["w1"] = w
	d.mu.Unlock()

	beadSrc.mu.Lock()
	beadSrc.shown = map[string]*protocol.BeadDetail{
		priorID: {ID: priorID, Status: "in_progress"},
	}
	beadSrc.mu.Unlock()

	d.releasePriorAssignment(ctx, w, newID)

	var assignmentStatus string
	if err := d.db.QueryRowContext(ctx,
		`SELECT status FROM assignments WHERE id=?`, priorAssignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if assignmentStatus != "completed" {
		t.Fatalf("prior assignment status = %q, want %q", assignmentStatus, "completed")
	}

	beadSrc.mu.Lock()
	gotStatus := beadSrc.updated[priorID]
	beadSrc.mu.Unlock()
	if gotStatus != "open" {
		t.Fatalf("prior bead status = %q, want %q", gotStatus, "open")
	}
}
