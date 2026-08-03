//nolint:testpackage // white-box startup lifecycle assertions
package dispatcher

import (
	"context"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

// TestReviewCheckpointStartupOrdering ensures startup restores checkpoint work
// state before reconciling orphaned ops runs or routing pending escalations.
func TestReviewCheckpointStartupOrdering(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)

	const (
		beadID     = "review-checkpoint-startup"
		workerID   = "review-checkpoint-worker"
		worktree   = "/tmp/worktree-review-checkpoint-startup"
		checkpoint = "checkpoint-startup-ordering"
	)
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Resume checkpoint review",
		AcceptanceCriteria: "Assert: review resumes from the recovered checkpoint worktree",
	}
	if err := beadSrc.AppendJourney(ctx, beadID, beadstore.JourneyEvent{
		Actor:   "dispatcher",
		Event:   "checkpoint_requested",
		Payload: `{"checkpoint_id":"` + checkpoint + `","worker_id":"` + workerID + `","trigger":"context_threshold"}`,
	}); err != nil {
		t.Fatalf("seed checkpoint journey: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO beads (id, title, status) VALUES (?, ?, 'open')`,
		beadID, "Resume checkpoint review"); err != nil {
		t.Fatalf("seed checkpoint bead: %v", err)
	}

	if _, err := d.createAssignment(ctx, beadID, workerID, worktree); err != nil {
		t.Fatalf("create checkpoint assignment: %v", err)
	}
	orphaned, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		Type:          string(ops.OpsReview),
		BeadID:        beadID,
		WorkerID:      workerID,
		DispatcherPID: -1,
		ProcessPID:    -1,
	})
	if err != nil {
		t.Fatalf("create orphaned review run: %v", err)
	}
	escalationID := insertDispatcherTestEscalation(t, d.db, protocol.EscOversizedBead, beadID, workerID)

	if err := d.startupRecovery(ctx); err != nil {
		t.Fatalf("startupRecovery: %v", err)
	}

	var recoveredEventID, escalationEventID int64
	if err := d.db.QueryRowContext(ctx,
		`SELECT id FROM events WHERE type='checkpoint_recovered' AND bead_id=?`, beadID).
		Scan(&recoveredEventID); err != nil {
		t.Fatalf("query checkpoint recovery event: %v", err)
	}
	if err := d.db.QueryRowContext(ctx,
		`SELECT id FROM events WHERE type='escalation_acked' AND bead_id=?`, beadID).
		Scan(&escalationEventID); err != nil {
		t.Fatalf("query escalation routing event: %v", err)
	}
	if recoveredEventID >= escalationEventID {
		t.Fatalf("checkpoint recovery event id = %d, want before escalation routing event id %d", recoveredEventID, escalationEventID)
	}
	if status := dispatcherTestEscalationStatus(t, d.db, escalationID); status != "acked" {
		t.Fatalf("pending escalation status = %q, want acked", status)
	}

	waitFor(t, func() bool { return spawnMock.SpawnCount() == 1 }, time.Second)

	d.mu.Lock()
	restoredWorktree := d.worktreeByBead[beadID]
	d.mu.Unlock()
	if restoredWorktree != worktree {
		t.Fatalf("restored worktree = %q, want %q", restoredWorktree, worktree)
	}
	if restoredCheckpoint := d.checkpoints.get(beadID); restoredCheckpoint == nil || restoredCheckpoint.checkpointID != checkpoint {
		t.Fatalf("restored checkpoint = %#v, want checkpoint %q", restoredCheckpoint, checkpoint)
	}

	superseded := fetchOpsRunForTest(t, d.db, orphaned.ID)
	if superseded.Status != opsRunStatusSuperseded {
		t.Fatalf("orphaned review status = %q, want %q", superseded.Status, opsRunStatusSuperseded)
	}

	spawnMock.mu.Lock()
	spawn := spawnMock.spawns[0]
	spawnMock.mu.Unlock()
	if spawn.workdir != worktree {
		t.Fatalf("rerouted review worktree = %q, want recovered %q", spawn.workdir, worktree)
	}

	var readyCount int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads_ready WHERE id=?`, beadID).Scan(&readyCount); err != nil {
		t.Fatalf("query ordinary ready queue: %v", err)
	}
	if readyCount != 0 {
		t.Fatalf("checkpoint bead appeared in ordinary ready queue %d times, want 0", readyCount)
	}
}
