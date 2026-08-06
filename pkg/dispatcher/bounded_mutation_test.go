package dispatcher //nolint:testpackage // targeted white-box tests exercise bounded coordination contracts

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestDispatcherMutexWatchdogReleasesRetainedMutexForCleanup(t *testing.T) {
	const childEnv = "ORO_TEST_RETAINED_DISPATCHER_MUTEX"
	if os.Getenv(childEnv) == "1" {
		d := &Dispatcher{}
		d.mu.Lock()
		t.Cleanup(func() {
			d.mu.Lock()
			d.mu.Unlock() //nolint:staticcheck // cleanup completes only after the mutation watchdog releases a retained lock
		})
		assertDispatcherMutexAvailableWithin(t, d, 20*time.Millisecond)
		return
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], //nolint:gosec // test helper re-executes this binary with fixed arguments
		"-test.run=^TestDispatcherMutexWatchdogReleasesRetainedMutexForCleanup$", "-test.count=1")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	output, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("retained-mutex watchdog detected the lock but hung during cleanup: %s", output)
	}
	if err == nil {
		t.Fatal("retained-mutex watchdog child passed, want the watchdog assertion to fail")
	}
	if !strings.Contains(string(output), "dispatcher mutex remained locked after bounded operation returned") {
		t.Fatalf("retained-mutex watchdog output = %s", output)
	}
}

func TestDispatcherOperationWatchdogReleasesRetainedMutexForCleanup(t *testing.T) {
	const childEnv = "ORO_TEST_RETAINED_DISPATCHER_MUTEX_DURING_OPERATION"
	if os.Getenv(childEnv) == "1" {
		d := &Dispatcher{}
		d.mu.Lock()
		returned := make(chan struct{})
		go func() {
			d.mu.Lock()
			d.mu.Unlock()
			close(returned)
		}()
		t.Cleanup(func() {
			d.mu.Lock()
			d.mu.Unlock()
		})
		waitForDispatcherOperationWithin(t, d, returned, 20*time.Millisecond,
			"bounded operation did not return; dispatcher mutex may be retained")
		return
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], //nolint:gosec // test helper re-executes this binary with fixed arguments
		"-test.run=^TestDispatcherOperationWatchdogReleasesRetainedMutexForCleanup$", "-test.count=1")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	output, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("in-flight retained-mutex watchdog hung during cleanup: %s", output)
	}
	if err == nil {
		t.Fatal("in-flight retained-mutex watchdog child passed, want the watchdog assertion to fail")
	}
	if !strings.Contains(string(output), "bounded operation did not return; dispatcher mutex may be retained") {
		t.Fatalf("in-flight retained-mutex watchdog output = %s", output)
	}
}

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
	returned := make(chan reviewOpsRunContext, 1)
	go func() {
		returned <- d.reviewContextForOpsRun(ctx, record)
	}()
	reviewCtx := waitForDispatcherOperationWithin(t, d, returned, 250*time.Millisecond,
		"reviewContextForOpsRun did not return within its bounded in-memory contract")
	return reviewCtx
}

func waitForDispatcherOperationWithin[T any](
	t *testing.T,
	d *Dispatcher,
	returned <-chan T,
	timeout time.Duration,
	failure string,
) T {
	t.Helper()
	select {
	case result := <-returned:
		return result
	case <-time.After(timeout):
	}
	if d.mu.TryLock() {
		d.mu.Unlock()
	} else {
		// The bounded operation is the only possible mutex owner. Release a
		// mutant-retained lock so it and test cleanup can finish before failing.
		d.mu.Unlock()
	}
	select {
	case result := <-returned:
		t.Fatal(failure)
		return result
	case <-time.After(timeout):
		var zero T
		t.Fatal(failure)
		return zero
	}
}

func assertDispatcherMutexAvailableWithin(t *testing.T, d *Dispatcher, _ time.Duration) {
	t.Helper()
	if !d.mu.TryLock() {
		// Every caller invokes this after an isolated operation has returned, so
		// no competing mutex user remains. Release a mutant-retained lock before
		// failing so test cleanup cannot hang.
		d.mu.Unlock()
		t.Fatal("dispatcher mutex remained locked after bounded operation returned")
	}
	d.mu.Unlock()
}
