package dispatcher //nolint:testpackage // targeted white-box tests exercise bounded coordination contracts

import (
	"context"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestSpawnEscalationOneShotReturnsAfterReadingWorktree(t *testing.T) {
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
	const beadID = "mutation-bounded-escalation"
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Title: "Bound escalation dispatch"}
	d.mu.Lock()
	d.worktreeByBead[beadID] = "/tmp/mutation-bounded-escalation"
	d.mu.Unlock()
	returned := make(chan struct{})
	go func() {
		d.spawnEscalationOneShot(
			context.Background(), 0, 0, string(protocol.EscOversizedBead), beadID, "mutation-worker", "split task",
		)
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("spawnEscalationOneShot did not return after reading the worktree under lock")
	}
	spawnDeadline := time.Now().Add(250 * time.Millisecond)
	for spawnMock.SpawnCount() == 0 && time.Now().Before(spawnDeadline) {
		time.Sleep(time.Millisecond)
	}
	if got := spawnMock.SpawnCount(); got != 1 {
		t.Fatalf("decompose spawn count = %d, want 1", got)
	}
}

func TestApplyHealthReturnsAndReleasesDispatcherMutex(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	returned := make(chan struct{})
	var healthJSON string
	var healthErr error
	go func() {
		healthJSON, healthErr = d.applyHealth()
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("applyHealth did not return within its bounded in-memory contract")
	}
	if healthErr != nil {
		t.Fatalf("applyHealth: %v", healthErr)
	}
	if healthJSON == "" {
		t.Fatal("applyHealth returned empty JSON")
	}

	lockAvailable := make(chan struct{})
	go func() {
		d.mu.Lock()
		d.mu.Unlock() //nolint:staticcheck // lock/unlock completion is the bounded mutex-release assertion
		close(lockAvailable)
	}()
	select {
	case <-lockAvailable:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("applyHealth returned while retaining the dispatcher mutex")
	}
}

func TestReviewContextForOpsRunReturnsAndReleasesDispatcherMutex(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	const (
		beadID   = "mutation-review-context"
		workerID = "mutation-review-worker"
		worktree = "/tmp/mutation-review-context"
		opsRunID = int64(7301)
	)
	assignmentID, err := d.createAssignment(ctx, beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	if err := d.requeueAssignment(ctx, assignmentID); err != nil {
		t.Fatalf("requeue assignment: %v", err)
	}
	checkpoint := seedDurableReviewCheckpoint(
		t, d, beadID, assignmentID, worktree, ReviewCheckpointStateReviewRunning,
	)
	if _, err := d.db.ExecContext(ctx, `UPDATE review_checkpoints SET ops_run_id=? WHERE id=?`, opsRunID, checkpoint.ID); err != nil {
		t.Fatalf("link checkpoint to ops run: %v", err)
	}

	reviewCtx := reviewContextForOpsRunWithin(ctx, t, d, OpsRunRecord{
		ID: opsRunID, BeadID: beadID, WorkerID: workerID,
	})
	if reviewCtx.worktree != worktree || reviewCtx.assignmentID != assignmentID {
		t.Fatalf("checkpoint review context = %+v, want worktree %q assignment %d", reviewCtx, worktree, assignmentID)
	}
	assertDispatcherMutexAvailableWithin(t, d, 250*time.Millisecond)

	fallback, _, _, _, _, _ := newTestDispatcher(t)
	fallback.worktreeByBead[beadID] = worktree
	reviewCtx = reviewContextForOpsRunWithin(ctx, t, fallback, OpsRunRecord{BeadID: beadID, WorkerID: workerID})
	if reviewCtx.worktree != worktree {
		t.Fatalf("fallback review context = %+v, want worktree %q", reviewCtx, worktree)
	}
	assertDispatcherMutexAvailableWithin(t, fallback, 250*time.Millisecond)
}

func reviewContextForOpsRunWithin(
	ctx context.Context,
	t *testing.T,
	d *Dispatcher,
	record OpsRunRecord,
) reviewOpsRunContext {
	t.Helper()
	returned := make(chan struct{})
	var reviewCtx reviewOpsRunContext
	go func() {
		reviewCtx = d.reviewContextForOpsRun(ctx, record)
		close(returned)
	}()
	select {
	case <-returned:
		return reviewCtx
	case <-time.After(250 * time.Millisecond):
		t.Fatal("reviewContextForOpsRun did not return within its bounded in-memory contract")
		return reviewOpsRunContext{}
	}
}

func assertDispatcherMutexAvailableWithin(t *testing.T, d *Dispatcher, timeout time.Duration) {
	t.Helper()
	lockAvailable := make(chan struct{})
	go func() {
		d.mu.Lock()
		d.mu.Unlock() //nolint:staticcheck // lock/unlock completion is the bounded mutex-release assertion
		close(lockAvailable)
	}()
	select {
	case <-lockAvailable:
	case <-time.After(timeout):
		t.Fatal("dispatcher mutex remained locked after bounded operation returned")
	}
}
