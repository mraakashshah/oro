//nolint:testpackage // Mutation coverage requires direct access to dispatcher lifecycle state.
package dispatcher

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func TestHandleReviewResultForAssignmentOutcomeMatrix(t *testing.T) {
	t.Run("approved success", func(t *testing.T) {
		fixture := newReviewResultAssignmentMutationFixture(t)

		invokeReviewResultForAssignmentMutation(t, fixture, ops.Result{
			Verdict:  ops.VerdictApproved,
			Feedback: "approved without extracted patterns",
		})

		assertReviewResultAssignmentMutationEvents(t, fixture.d, map[string]int{
			"review_approved":      1,
			"review_rejected":      0,
			"review_failed":        0,
			"review_env_blocked":   0,
			"review_infra_blocked": 0,
		})
		msg := lastMockConnMessage(t, fixture.conn)
		if msg.Type != protocol.MsgReviewResult || msg.ReviewResult == nil || msg.ReviewResult.Verdict != "approved" {
			t.Fatalf("worker message = %#v, want approved review result", msg)
		}
		assertReviewResultAssignmentMutationWorker(t, fixture, protocol.WorkerReviewing, fixture.beadID, 0)
		assertReviewResultAssignmentMutationStatus(t, fixture, "active")
	})

	t.Run("rejected ordinary", func(t *testing.T) {
		fixture := newReviewResultAssignmentMutationFixture(t)
		const feedback = "ordinary rejection feedback"

		invokeReviewResultForAssignmentMutation(t, fixture, ops.Result{
			Verdict:  ops.VerdictRejected,
			Feedback: feedback,
		})

		assertReviewResultAssignmentMutationEvents(t, fixture.d, map[string]int{
			"review_approved":      0,
			"review_rejected":      1,
			"review_failed":        0,
			"review_env_blocked":   0,
			"review_infra_blocked": 0,
		})
		msg := lastMockConnMessage(t, fixture.conn)
		if msg.Type != protocol.MsgAssign || msg.Assign == nil || !strings.Contains(msg.Assign.Feedback, feedback) {
			t.Fatalf("worker message = %#v, want assignment containing %q", msg, feedback)
		}
		assertReviewResultAssignmentMutationWorker(t, fixture, protocol.WorkerBusy, fixture.beadID, 0)
		assertReviewResultAssignmentMutationStatus(t, fixture, "active")
		fixture.d.mu.Lock()
		rejections := fixture.d.rejectionCounts[fixture.beadID]
		fixture.d.mu.Unlock()
		if rejections != 1 {
			t.Fatalf("rejection count = %d, want 1", rejections)
		}
	})

	t.Run("rejected environment blocked", func(t *testing.T) {
		fixture := newReviewResultAssignmentMutationFixture(t)
		seedReviewResultAssignmentMutationTracking(fixture)

		invokeReviewResultForAssignmentMutation(t, fixture, ops.Result{
			Verdict: ops.VerdictRejected,
			Feedback: "acceptance command passed\n" +
				"broader test failed: listen unix /tmp/oro.sock: bind: operation not permitted\n" +
				"VERDICT: REJECTED",
		})

		assertReviewResultAssignmentMutationEvents(t, fixture.d, map[string]int{
			"review_approved":      0,
			"review_rejected":      0,
			"review_failed":        0,
			"review_env_blocked":   1,
			"review_infra_blocked": 0,
		})
		assertReviewResultAssignmentMutationBlocked(t, fixture)
	})

	t.Run("default ordinary failure", func(t *testing.T) {
		fixture := newReviewResultAssignmentMutationFixture(t)

		invokeReviewResultForAssignmentMutation(t, fixture, ops.Result{
			Verdict:  ops.VerdictFailed,
			Feedback: "review process exited without VERDICT",
		})

		assertReviewResultAssignmentMutationEvents(t, fixture.d, map[string]int{
			"review_approved":      0,
			"review_rejected":      1,
			"review_failed":        1,
			"review_env_blocked":   0,
			"review_infra_blocked": 0,
		})
		msg := lastMockConnMessage(t, fixture.conn)
		if msg.Type != protocol.MsgAssign || msg.Assign == nil || !strings.Contains(msg.Assign.Feedback, "Review failed:") {
			t.Fatalf("worker message = %#v, want review-failed assignment", msg)
		}
		assertReviewResultAssignmentMutationWorker(t, fixture, protocol.WorkerBusy, fixture.beadID, 0)
		assertReviewResultAssignmentMutationStatus(t, fixture, "active")
	})

	t.Run("default infrastructure blocked", func(t *testing.T) {
		fixture := newReviewResultAssignmentMutationFixture(t)
		seedReviewResultAssignmentMutationTracking(fixture)

		invokeReviewResultForAssignmentMutation(t, fixture, ops.Result{
			Verdict:  ops.VerdictFailed,
			Feedback: `{"type":"system","subtype":"hook_started","hook_name":"SessionStart:startup"}`,
			Err:      errors.New("exit status 1"),
		})

		assertReviewResultAssignmentMutationEvents(t, fixture.d, map[string]int{
			"review_approved":      0,
			"review_rejected":      0,
			"review_failed":        0,
			"review_env_blocked":   0,
			"review_infra_blocked": 1,
		})
		assertReviewResultAssignmentMutationBlocked(t, fixture)
	})

	t.Run("release token fenced", func(t *testing.T) {
		fixture := newReviewResultAssignmentMutationFixture(t)
		fixture.d.mu.Lock()
		worker := fixture.d.workers[fixture.workerID]
		worker.reviewReleaseToken = 41
		fixture.d.mu.Unlock()

		invokeReviewResultForAssignmentMutation(t, fixture, ops.Result{
			Verdict:  ops.VerdictRejected,
			Feedback: "fenced rejection must have no side effects",
		})

		assertReviewResultAssignmentMutationEvents(t, fixture.d, map[string]int{
			"review_approved":      0,
			"review_rejected":      0,
			"review_failed":        0,
			"review_env_blocked":   0,
			"review_infra_blocked": 0,
		})
		if writes := reviewResultAssignmentMutationWriteCount(fixture.conn); writes != 0 {
			t.Fatalf("worker writes = %d, want 0 for fenced result", writes)
		}
		fixture.d.mu.Lock()
		got := fixture.d.workers[fixture.workerID]
		fixture.d.mu.Unlock()
		if got != worker {
			t.Fatalf("worker pointer = %p, want preserved %p", got, worker)
		}
		assertReviewResultAssignmentMutationWorker(t, fixture, protocol.WorkerReviewing, fixture.beadID, 41)
		assertReviewResultAssignmentMutationStatus(t, fixture, "active")
	})
}

type reviewResultAssignmentMutationFixture struct {
	d            *Dispatcher
	beads        *fakeBeadStore
	conn         *mockConn
	workerID     string
	beadID       string
	assignmentID int64
}

func newReviewResultAssignmentMutationFixture(t *testing.T) reviewResultAssignmentMutationFixture {
	t.Helper()
	d, beads, _, _, _, _ := newTestDispatcher(t)
	d.repoRoot = t.TempDir()
	const (
		workerID = "worker-review-result-assignment-mutation"
		beadID   = "bead-review-result-assignment-mutation"
		worktree = "/tmp/review-result-assignment-mutation"
	)
	conn := registerReviewingWorker(t, d, beads, workerID, beadID, worktree)
	d.mu.Lock()
	assignmentID := d.workers[workerID].assignmentID
	d.mu.Unlock()
	return reviewResultAssignmentMutationFixture{
		d:            d,
		beads:        beads,
		conn:         conn,
		workerID:     workerID,
		beadID:       beadID,
		assignmentID: assignmentID,
	}
}

func invokeReviewResultForAssignmentMutation(
	t *testing.T,
	fixture reviewResultAssignmentMutationFixture,
	result ops.Result,
) {
	t.Helper()
	resultCh := make(chan ops.Result, 1)
	resultCh <- result
	done := make(chan struct{})
	go func() {
		fixture.d.handleReviewResultForAssignment(
			context.Background(), fixture.workerID, fixture.beadID, fixture.assignmentID, resultCh,
		)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(750 * time.Millisecond):
		t.Fatal("handleReviewResultForAssignment did not complete within 750ms")
	}
	assertReviewResultAssignmentMutationMutexReusable(t, fixture.d)
}

func assertReviewResultAssignmentMutationEvents(t *testing.T, d *Dispatcher, want map[string]int) {
	t.Helper()
	for eventType, wantCount := range want {
		if got := eventCount(t, d.db, eventType); got != wantCount {
			t.Errorf("%s event count = %d, want %d", eventType, got, wantCount)
		}
	}
}

func assertReviewResultAssignmentMutationWorker(
	t *testing.T,
	fixture reviewResultAssignmentMutationFixture,
	wantState protocol.WorkerState,
	wantBeadID string,
	wantReleaseToken uint64,
) {
	t.Helper()
	fixture.d.mu.Lock()
	worker := fixture.d.workers[fixture.workerID]
	fixture.d.mu.Unlock()
	if worker == nil {
		t.Fatal("worker missing after review result")
	}
	if worker.state != wantState || worker.beadID != wantBeadID || worker.assignmentID != fixture.assignmentID ||
		worker.reviewReleaseToken != wantReleaseToken || worker.reviewMessagesInFlight != 0 {
		t.Fatalf("worker state = %s bead=%q assignment=%d token=%d in-flight=%d, want %s/%q/%d/%d/0",
			worker.state, worker.beadID, worker.assignmentID, worker.reviewReleaseToken, worker.reviewMessagesInFlight,
			wantState, wantBeadID, fixture.assignmentID, wantReleaseToken)
	}
}

func assertReviewResultAssignmentMutationStatus(
	t *testing.T,
	fixture reviewResultAssignmentMutationFixture,
	want string,
) {
	t.Helper()
	var got string
	if err := fixture.d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, fixture.assignmentID).Scan(&got); err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if got != want {
		t.Fatalf("assignment status = %q, want %q", got, want)
	}
}

func seedReviewResultAssignmentMutationTracking(fixture reviewResultAssignmentMutationFixture) {
	fixture.d.mu.Lock()
	fixture.d.attemptCounts[fixture.beadID] = 2
	fixture.d.assigningBeads[fixture.beadID] = true
	fixture.d.rejectionCounts[fixture.beadID] = 3
	fixture.d.mu.Unlock()
}

func assertReviewResultAssignmentMutationBlocked(t *testing.T, fixture reviewResultAssignmentMutationFixture) {
	t.Helper()
	assertReviewResultAssignmentMutationWorker(t, fixture, protocol.WorkerIdle, "", 0)
	assertReviewResultAssignmentMutationStatus(t, fixture, "completed")
	if writes := reviewResultAssignmentMutationWriteCount(fixture.conn); writes != 0 {
		t.Fatalf("worker writes = %d, want 0 for blocked review", writes)
	}
	fixture.beads.mu.Lock()
	beadStatus := fixture.beads.updated[fixture.beadID]
	fixture.beads.mu.Unlock()
	if beadStatus != "open" {
		t.Fatalf("bead status = %q, want open", beadStatus)
	}
	fixture.d.mu.Lock()
	blockedCount := fixture.d.reviewBlockedCounts[fixture.beadID]
	_, attemptTracked := fixture.d.attemptCounts[fixture.beadID]
	_, assigningTracked := fixture.d.assigningBeads[fixture.beadID]
	_, rejectionTracked := fixture.d.rejectionCounts[fixture.beadID]
	fixture.d.mu.Unlock()
	if blockedCount != 1 || attemptTracked || assigningTracked || rejectionTracked {
		t.Fatalf("blocked tracking = count %d attempt=%t assigning=%t rejection=%t, want 1/false/false/false",
			blockedCount, attemptTracked, assigningTracked, rejectionTracked)
	}
}

func reviewResultAssignmentMutationWriteCount(conn *mockConn) int {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return len(conn.written)
}

func assertReviewResultAssignmentMutationMutexReusable(t *testing.T, d *Dispatcher) {
	t.Helper()
	locked := make(chan int, 1)
	go func() {
		d.mu.Lock()
		workerCount := len(d.workers)
		d.mu.Unlock()
		locked <- workerCount
	}()
	select {
	case <-locked:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("dispatcher mutex was not reusable after review result")
	}
}
