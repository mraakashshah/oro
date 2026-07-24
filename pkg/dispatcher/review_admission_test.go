package dispatcher //nolint:testpackage // lifecycle coverage requires internal dispatcher state

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"oro/pkg/ops"
	"oro/pkg/protocol"
)

// TestReviewLifecycleStopsWhenBeadGainsBlocker ensures that dependency state is
// checked both before an ops review starts and immediately before an approval
// is delivered. A newly-open blocker must return the parent to the scheduler
// without spending review or merge latency.
func TestReviewLifecycleStopsWhenBeadGainsBlocker(t *testing.T) {
	t.Run("ready admission", func(t *testing.T) {
		d, beads, _, _, _, spawner := newTestDispatcher(t)
		const (
			beadID    = "bead-review-parent"
			blockerID = "bead-review-blocker"
			workerID  = "worker-review-parent"
		)

		assignmentID := seedReviewAssignment(t, d, beadID, workerID)
		beads.mu.Lock()
		beads.beads = []protocol.Bead{{ID: blockerID, Status: "open"}}
		beads.shown[beadID] = &protocol.BeadDetail{
			ID:     beadID,
			Status: "in_progress",
			Dependencies: []protocol.Dependency{{
				IssueID: beadID, DependsOnID: blockerID, Type: "blocks",
			}},
		}
		beads.shown[blockerID] = &protocol.BeadDetail{ID: blockerID, Status: "open"}
		beads.mu.Unlock()

		installReviewingWorker(d, workerID, beadID, assignmentID, t.TempDir())
		d.handleReadyForReview(context.Background(), workerID, protocol.Message{
			Type: protocol.MsgReadyForReview,
			ReadyForReview: &protocol.ReadyForReviewPayload{
				BeadID: beadID, WorkerID: workerID,
			},
		})

		assertDependencyBlockedReviewLifecycle(t, d, beads, workerID, beadID, blockerID, assignmentID, "review_blocked_by_dependency")
		if got := spawner.SpawnCount(); got != 0 {
			t.Fatalf("ops review spawns = %d, want 0", got)
		}
	})

	t.Run("terminal approval", func(t *testing.T) {
		d, beads, _, _, _, spawner := newTestDispatcher(t)
		const (
			beadID    = "bead-approval-parent"
			blockerID = "bead-approval-blocker"
			workerID  = "worker-approval-parent"
		)

		assignmentID := seedReviewAssignment(t, d, beadID, workerID)
		beads.mu.Lock()
		beads.beads = []protocol.Bead{{ID: blockerID, Status: "open"}}
		beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
		beads.shown[blockerID] = &protocol.BeadDetail{ID: blockerID, Status: "open"}
		beads.mu.Unlock()
		installReviewingWorker(d, workerID, beadID, assignmentID, t.TempDir())
		d.mu.Lock()
		d.workers[workerID].state = protocol.WorkerReviewing
		d.mu.Unlock()

		// Model a blocker added while the ops review is in flight, immediately
		// before its otherwise-approved result is consumed.
		beads.mu.Lock()
		beads.shown[beadID].Dependencies = []protocol.Dependency{{
			IssueID: beadID, DependsOnID: blockerID, Type: "blocks",
		}}
		beads.mu.Unlock()

		d.handleReviewApproved(context.Background(), workerID, beadID, ops.Result{
			Verdict:  ops.VerdictApproved,
			Feedback: "VERDICT: APPROVED",
		})

		assertDependencyBlockedReviewLifecycle(t, d, beads, workerID, beadID, blockerID, assignmentID, "review_blocked_by_dependency")
		if got := eventCount(t, d.db, "review_approved"); got != 0 {
			t.Fatalf("review_approved events = %d, want 0", got)
		}
		if got := spawner.SpawnCount(); got != 0 {
			t.Fatalf("ops review spawns = %d, want 0", got)
		}
	})

	t.Run("concurrent terminal verdict cannot bypass dependency claim", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		const (
			beadID    = "bead-concurrent-parent"
			blockerID = "bead-concurrent-blocker"
			workerID  = "worker-concurrent-parent"
		)

		assignmentID := seedReviewAssignment(t, d, beadID, workerID)
		beads.mu.Lock()
		beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress", Dependencies: []protocol.Dependency{{
			IssueID: beadID, DependsOnID: blockerID, Type: "blocks",
		}}}
		beads.shown[blockerID] = &protocol.BeadDetail{ID: blockerID, Status: "open"}
		beads.mu.Unlock()
		installReviewingWorker(d, workerID, beadID, assignmentID, t.TempDir())
		d.mu.Lock()
		d.workers[workerID].state = protocol.WorkerReserved
		d.mu.Unlock()

		d.handleReviewApproved(context.Background(), workerID, beadID, ops.Result{
			Verdict:  ops.VerdictApproved,
			Feedback: "VERDICT: APPROVED",
		})

		if got := eventCount(t, d.db, "review_approved"); got != 0 {
			t.Fatalf("review_approved events = %d, want 0", got)
		}
	})

	t.Run("closed blocker permits approval", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		const (
			beadID    = "bead-closed-parent"
			blockerID = "bead-closed-blocker"
			workerID  = "worker-closed-parent"
		)

		assignmentID := seedReviewAssignment(t, d, beadID, workerID)
		beads.mu.Lock()
		beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress", Dependencies: []protocol.Dependency{{
			IssueID: beadID, DependsOnID: blockerID, Type: "blocks",
		}}}
		beads.shown[blockerID] = &protocol.BeadDetail{ID: blockerID, Status: "closed"}
		beads.mu.Unlock()
		installReviewingWorker(d, workerID, beadID, assignmentID, t.TempDir())
		d.mu.Lock()
		d.workers[workerID].state = protocol.WorkerReviewing
		d.mu.Unlock()

		d.handleReviewApproved(context.Background(), workerID, beadID, ops.Result{Verdict: ops.VerdictApproved})

		if got := eventCount(t, d.db, "review_approved"); got != 1 {
			t.Fatalf("review_approved events = %d, want 1", got)
		}
		if got := eventCount(t, d.db, "review_blocked_by_dependency"); got != 0 {
			t.Fatalf("review_blocked_by_dependency events = %d, want 0", got)
		}
	})

	t.Run("missing edge permits review", func(t *testing.T) {
		d, beads, _, _, _, spawner := newTestDispatcher(t)
		const (
			beadID   = "bead-edge-parent"
			workerID = "worker-edge-parent"
		)

		assignmentID := seedReviewAssignment(t, d, beadID, workerID)
		beads.mu.Lock()
		beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
		beads.mu.Unlock()
		installReviewingWorker(d, workerID, beadID, assignmentID, t.TempDir())

		d.handleReadyForReview(context.Background(), workerID, protocol.Message{
			Type: protocol.MsgReadyForReview,
			ReadyForReview: &protocol.ReadyForReviewPayload{
				BeadID: beadID, WorkerID: workerID,
			},
		})

		waitFor(t, func() bool { return spawner.SpawnCount() == 1 }, time.Second)
		waitFor(t, func() bool { return eventCount(t, d.db, "review_approved") == 1 }, time.Second)
	})

	t.Run("dependency lookup failure fails closed", func(t *testing.T) {
		d, beads, _, _, _, spawner := newTestDispatcher(t)
		const (
			beadID    = "bead-error-parent"
			blockerID = "bead-error-blocker"
			workerID  = "worker-error-parent"
		)

		assignmentID := seedReviewAssignment(t, d, beadID, workerID)
		beads.mu.Lock()
		beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress", Dependencies: []protocol.Dependency{{
			IssueID: beadID, DependsOnID: blockerID, Type: "blocks",
		}}}
		beads.showErrFn = map[string]error{blockerID: errors.New("store unavailable")}
		beads.mu.Unlock()
		installReviewingWorker(d, workerID, beadID, assignmentID, t.TempDir())

		d.handleReadyForReview(context.Background(), workerID, protocol.Message{
			Type: protocol.MsgReadyForReview,
			ReadyForReview: &protocol.ReadyForReviewPayload{
				BeadID: beadID, WorkerID: workerID,
			},
		})

		waitFor(t, func() bool { return eventCount(t, d.db, "review_dependency_lookup_failed") == 1 }, time.Second)
		assertDependencyBlockedReviewLifecycle(t, d, beads, workerID, beadID, blockerID, assignmentID, "review_dependency_lookup_failed")
		if got := eventCount(t, d.db, "review_blocked_by_dependency"); got != 0 {
			t.Fatalf("review_blocked_by_dependency events = %d, want 0", got)
		}
		if got := spawner.SpawnCount(); got != 0 {
			t.Fatalf("ops review spawns = %d, want 0", got)
		}
	})
}

func seedReviewAssignment(t *testing.T, d *Dispatcher, beadID, workerID string) int64 {
	t.Helper()
	result, err := d.db.ExecContext(context.Background(),
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`,
		beadID, workerID, "/tmp/review-admission")
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("assignment ID: %v", err)
	}
	return assignmentID
}

func installReviewingWorker(d *Dispatcher, workerID, beadID string, assignmentID int64, worktree string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         newMockConn(),
		state:        protocol.WorkerBusy,
		assignmentID: assignmentID,
		beadID:       beadID,
		worktree:     worktree,
		lastSeen:     d.nowFunc(),
	}
}

func assertDependencyBlockedReviewLifecycle(
	t *testing.T,
	d *Dispatcher,
	beads *fakeBeadStore,
	workerID, beadID, blockerID string,
	assignmentID int64,
	eventType string,
) {
	t.Helper()
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		worker := d.workers[workerID]
		return worker != nil && worker.state == protocol.WorkerIdle && worker.beadID == ""
	}, time.Second)

	if got := eventCount(t, d.db, eventType); got != 1 {
		t.Fatalf("%s events = %d, want 1", eventType, got)
	}
	if got := assignmentStatus(t, d.db, assignmentID); got != "completed" {
		t.Fatalf("assignment status = %q, want completed", got)
	}
	beads.mu.Lock()
	defer beads.mu.Unlock()
	if got := beads.updated[beadID]; got != "open" {
		t.Fatalf("parent status = %q, want open", got)
	}
	if _, changed := beads.updated[blockerID]; changed {
		t.Fatalf("blocker %q was updated, want it schedulable", blockerID)
	}
}

func assignmentStatus(t *testing.T, db *sql.DB, assignmentID int64) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&status); err != nil {
		t.Fatalf("assignment status: %v", err)
	}
	return status
}
