package dispatcher //nolint:testpackage // white-box mutation coverage for retry reservation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

type reviewRetryAttemptResult struct {
	reserved bool
	err      error
}

func TestReserveReviewRetryAttemptOutcomes(t *testing.T) {
	t.Run("release token rejects and unlocks", func(t *testing.T) {
		d, _, workerID, beadID, _ := newReviewRetryAttemptFixture(t, "release-token")
		d.mu.Lock()
		d.workers[workerID].reviewReleaseToken = 1
		d.mu.Unlock()
		result := invokeReviewRetryAttempt(t, d, workerID, beadID, "fenced feedback")
		if result.reserved || result.err != nil {
			t.Fatalf("reserveReviewRetryAttempt() = (%t, %v), want (false, nil)", result.reserved, result.err)
		}
		assertReviewRetryMutexUsable(t, d)
	})

	t.Run("no blocker reserves worker without cleanup effects", func(t *testing.T) {
		const feedback = "retry after review"
		d, beads, workerID, beadID, assignmentID := newReviewRetryAttemptFixture(t, "no-blocker")
		setReviewRetryDependencies(beads, beadID)

		result := invokeReviewRetryAttempt(t, d, workerID, beadID, feedback)
		if !result.reserved || result.err != nil {
			t.Fatalf("reserveReviewRetryAttempt() = (%t, %v), want (true, nil)", result.reserved, result.err)
		}
		assertReviewRetryMutexUsable(t, d)

		d.mu.Lock()
		worker := d.workers[workerID]
		state, gotBeadID, gotAssignmentID := worker.state, worker.beadID, worker.assignmentID
		d.mu.Unlock()
		if state != protocol.WorkerReserved || gotBeadID != beadID || gotAssignmentID != assignmentID {
			t.Fatalf("worker reservation = (%s, %q, %d), want (%s, %q, %d)",
				state, gotBeadID, gotAssignmentID, protocol.WorkerReserved, beadID, assignmentID)
		}
		assertReviewRetryAssignmentStatus(t, d, assignmentID, "active")
		assertReviewRetryFeedbackCount(t, d, beadID, feedback, 0)
		for _, eventType := range []string{
			"review_retry_dependency_lookup_failed",
			"review_retry_blocked_status_failed",
			"review_retry_blocked_assignment_cleanup_failed",
			"review_retry_blocked_by_dependency",
		} {
			assertReviewRetryEventCount(t, d, eventType, beadID, 0)
		}
	})

	t.Run("blocker persists feedback and releases all ownership", func(t *testing.T) {
		const feedback = "blocked by unfinished prerequisite"
		d, beads, workerID, beadID, assignmentID := newReviewRetryAttemptFixture(t, "blocker")
		blockerID := beadID + "-dependency"
		setReviewRetryDependencies(beads, beadID, blockerID)

		result := invokeReviewRetryAttempt(t, d, workerID, beadID, feedback)
		if result.reserved || result.err != nil {
			t.Fatalf("reserveReviewRetryAttempt() = (%t, %v), want (false, nil)", result.reserved, result.err)
		}
		assertReviewRetryMutexUsable(t, d)
		assertReviewRetryBlockedCleanup(t, d, beads, workerID, beadID, assignmentID)
		assertReviewRetryFeedbackCount(t, d, beadID, feedback, 1)
		assertReviewRetryEventPayload(t, d, "review_retry_blocked_by_dependency", beadID,
			fmt.Sprintf(`{"blocker_id":%q,"lookup_failed":false}`, blockerID))
	})

	t.Run("dependency lookup error fails closed and records the cause", func(t *testing.T) {
		const feedback = "dependency lookup was uncertain"
		d, beads, workerID, beadID, assignmentID := newReviewRetryAttemptFixture(t, "lookup-error")
		blockerID := beadID + "-dependency"
		setReviewRetryDependencies(beads, beadID, blockerID)
		beads.mu.Lock()
		beads.showErrFn[blockerID] = errors.New("injected dependency lookup failure")
		beads.mu.Unlock()

		result := invokeReviewRetryAttempt(t, d, workerID, beadID, feedback)
		if result.reserved || result.err == nil || !strings.Contains(result.err.Error(), "injected dependency lookup failure") {
			t.Fatalf("reserveReviewRetryAttempt() = (%t, %v), want false and injected lookup error", result.reserved, result.err)
		}
		assertReviewRetryMutexUsable(t, d)
		assertReviewRetryBlockedCleanup(t, d, beads, workerID, beadID, assignmentID)
		assertReviewRetryFeedbackCount(t, d, beadID, feedback, 1)
		assertReviewRetryEventPayload(t, d, "review_retry_dependency_lookup_failed", beadID,
			`{"error":"show dependency \"bead-review-retry-lookup-error-dependency\": injected dependency lookup failure"}`)
		assertReviewRetryEventPayload(t, d, "review_retry_blocked_by_dependency", beadID,
			`{"blocker_id":"","lookup_failed":true}`)
	})

	t.Run("bead update failure is durable while remaining cleanup continues", func(t *testing.T) {
		d, beads, workerID, beadID, assignmentID := newReviewRetryAttemptFixture(t, "status-error")
		blockerID := beadID + "-dependency"
		setReviewRetryDependencies(beads, beadID, blockerID)
		beads.mu.Lock()
		beads.updateErrs[beadID] = errors.New("injected bead update failure")
		beads.mu.Unlock()

		result := invokeReviewRetryAttempt(t, d, workerID, beadID, "status failure feedback")
		if result.reserved || result.err != nil {
			t.Fatalf("reserveReviewRetryAttempt() = (%t, %v), want (false, nil)", result.reserved, result.err)
		}
		assertReviewRetryMutexUsable(t, d)
		assertReviewRetryWorkerReleasedAndTrackingCleared(t, d, workerID, beadID)
		assertReviewRetryAssignmentStatus(t, d, assignmentID, "completed")
		beads.mu.Lock()
		_, updated := beads.updated[beadID]
		beads.mu.Unlock()
		if updated {
			t.Fatal("bead status was recorded as updated despite injected update failure")
		}
		assertReviewRetryEventPayload(t, d, "review_retry_blocked_status_failed", beadID,
			`{"error":"update bead bead-review-retry-status-error status to open: injected bead update failure"}`)
		assertReviewRetryEventPayload(t, d, "review_retry_blocked_by_dependency", beadID,
			fmt.Sprintf(`{"blocker_id":%q,"lookup_failed":false}`, blockerID))
	})

	t.Run("assignment cleanup failure is durable while worker release continues", func(t *testing.T) {
		d, beads, workerID, beadID, activeAssignmentID := newReviewRetryAttemptFixture(t, "assignment-error")
		blockerID := beadID + "-dependency"
		setReviewRetryDependencies(beads, beadID, blockerID)
		missingAssignmentID := activeAssignmentID + 1000
		d.mu.Lock()
		d.workers[workerID].assignmentID = missingAssignmentID
		d.mu.Unlock()

		result := invokeReviewRetryAttempt(t, d, workerID, beadID, "assignment failure feedback")
		if result.reserved || result.err != nil {
			t.Fatalf("reserveReviewRetryAttempt() = (%t, %v), want (false, nil)", result.reserved, result.err)
		}
		assertReviewRetryMutexUsable(t, d)
		assertReviewRetryWorkerReleasedAndTrackingCleared(t, d, workerID, beadID)
		assertReviewRetryAssignmentStatus(t, d, activeAssignmentID, "active")
		assertReviewRetryEventPayload(t, d, "review_retry_blocked_assignment_cleanup_failed", beadID,
			fmt.Sprintf(`{"error":"complete assignment: assignment_id %d affected 0 rows"}`, missingAssignmentID))
		assertReviewRetryEventPayload(t, d, "review_retry_blocked_by_dependency", beadID,
			fmt.Sprintf(`{"blocker_id":%q,"lookup_failed":false}`, blockerID))
	})
}

func newReviewRetryAttemptFixture(t *testing.T, suffix string) (*Dispatcher, *fakeBeadStore, string, string, int64) {
	t.Helper()
	d, beads, _, _, _, _ := newTestDispatcher(t)
	workerID := "worker-review-retry-" + suffix
	beadID := "bead-review-retry-" + suffix
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, t.TempDir())
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		state:        protocol.WorkerReviewing,
		beadID:       beadID,
		assignmentID: assignmentID,
		worktree:     t.TempDir(),
	}
	d.attemptCounts[beadID] = 2
	d.rejectionCounts[beadID] = 1
	d.assigningBeads[beadID] = true
	d.mu.Unlock()
	return d, beads, workerID, beadID, assignmentID
}

func setReviewRetryDependencies(beads *fakeBeadStore, beadID string, blockerIDs ...string) {
	dependencies := make([]protocol.Dependency, 0, len(blockerIDs))
	for _, blockerID := range blockerIDs {
		dependencies = append(dependencies, protocol.Dependency{
			IssueID: beadID, DependsOnID: blockerID, Type: "blocks",
		})
	}
	bead := protocol.Bead{ID: beadID, Status: "in_progress", Dependencies: dependencies}
	beads.mu.Lock()
	if beads.showErrFn == nil {
		beads.showErrFn = make(map[string]error)
	}
	if beads.updateErrs == nil {
		beads.updateErrs = make(map[string]error)
	}
	beads.shown[beadID] = &bead
	for _, blockerID := range blockerIDs {
		blocker := protocol.Bead{ID: blockerID, Status: "open"}
		beads.shown[blockerID] = &blocker
	}
	beads.mu.Unlock()
}

func invokeReviewRetryAttempt(t *testing.T, d *Dispatcher, workerID, beadID, feedback string) reviewRetryAttemptResult {
	t.Helper()
	resultCh := make(chan reviewRetryAttemptResult, 1)
	go func() {
		reserved, err := d.reserveReviewRetryAttempt(context.Background(), workerID, beadID, feedback)
		resultCh <- reviewRetryAttemptResult{reserved: reserved, err: err}
	}()
	select {
	case result := <-resultCh:
		return result
	case <-time.After(750 * time.Millisecond):
		t.Fatal("reserveReviewRetryAttempt did not finish; dispatcher mutex may be locked")
		return reviewRetryAttemptResult{}
	}
}

func assertReviewRetryMutexUsable(t *testing.T, d *Dispatcher) {
	t.Helper()
	assertDispatcherMutexAvailableWithin(t, d, 100*time.Millisecond)
}

func assertReviewRetryBlockedCleanup(
	t *testing.T,
	d *Dispatcher,
	beads *fakeBeadStore,
	workerID, beadID string,
	assignmentID int64,
) {
	t.Helper()
	assertReviewRetryWorkerReleasedAndTrackingCleared(t, d, workerID, beadID)
	assertReviewRetryAssignmentStatus(t, d, assignmentID, "completed")
	beads.mu.Lock()
	status := beads.updated[beadID]
	beads.mu.Unlock()
	if status != "open" {
		t.Fatalf("bead status = %q, want open", status)
	}
}

func assertReviewRetryWorkerReleasedAndTrackingCleared(t *testing.T, d *Dispatcher, workerID, beadID string) {
	t.Helper()
	d.mu.Lock()
	worker := d.workers[workerID]
	_, hasAttempts := d.attemptCounts[beadID]
	_, hasRejections := d.rejectionCounts[beadID]
	_, isAssigning := d.assigningBeads[beadID]
	d.mu.Unlock()
	if worker == nil || worker.state != protocol.WorkerIdle || worker.beadID != "" || worker.assignmentID != 0 || worker.worktree != "" {
		t.Fatalf("worker was not terminally released: %#v", worker)
	}
	if hasAttempts || hasRejections || isAssigning {
		t.Fatalf("bead tracking not cleared: attempts=%t rejections=%t assigning=%t",
			hasAttempts, hasRejections, isAssigning)
	}
}

func assertReviewRetryAssignmentStatus(t *testing.T, d *Dispatcher, assignmentID int64, want string) {
	t.Helper()
	var got string
	if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&got); err != nil {
		t.Fatalf("read assignment %d status: %v", assignmentID, err)
	}
	if got != want {
		t.Fatalf("assignment %d status = %q, want %q", assignmentID, got, want)
	}
}

func assertReviewRetryFeedbackCount(t *testing.T, d *Dispatcher, beadID, feedback string, want int) {
	t.Helper()
	var got int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM rejection_history WHERE bead_id=? AND feedback=?`, beadID, feedback).Scan(&got); err != nil {
		t.Fatalf("count rejection feedback: %v", err)
	}
	if got != want {
		t.Fatalf("rejection feedback count = %d, want %d", got, want)
	}
}

func assertReviewRetryEventCount(t *testing.T, d *Dispatcher, eventType, beadID string, want int) {
	t.Helper()
	var got int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type=? AND bead_id=?`, eventType, beadID).Scan(&got); err != nil {
		t.Fatalf("count %s events: %v", eventType, err)
	}
	if got != want {
		t.Fatalf("%s event count = %d, want %d", eventType, got, want)
	}
}

func assertReviewRetryEventPayload(t *testing.T, d *Dispatcher, eventType, beadID, want string) {
	t.Helper()
	var got string
	if err := d.db.QueryRow(`SELECT payload FROM events WHERE type=? AND bead_id=?`, eventType, beadID).Scan(&got); err != nil {
		t.Fatalf("read %s event: %v", eventType, err)
	}
	if got != want {
		t.Fatalf("%s payload = %q, want %q", eventType, got, want)
	}
	assertReviewRetryEventCount(t, d, eventType, beadID, 1)
}
