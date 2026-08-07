//nolint:testpackage // Mutation owners require direct access to dispatcher lifecycle state.
package dispatcher

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestRestartCheckpointOwnedWorkerUsingBoundedMutation(t *testing.T) {
	const childEnv = "ORO_TEST_RESTART_CHECKPOINT_OWNED_WORKER_USING_BOUNDED"
	if os.Getenv(childEnv) != "1" {
		runLifecycleLockMutationSubprocess(t, childEnv, "TestRestartCheckpointOwnedWorkerUsingBoundedMutation")
		runLifecycleMutationTestCases(t,
			"TestReviewWorkerDirectivesDurablyReleaseCheckpoint",
			"TestReviewWorkerRestartActionErrorsStillFinalizeDurableRelease",
			"TestReviewWorkerRestartFenceSpansStoreKillAndSpawn",
		)
		return
	}

	d, worker := newCheckpointWorkerReleaseFixture(t, "bounded-restart-using")
	d.procMgr = &mockProcessManager{}
	worker.managed = true
	result := make(chan lifecycleRestartMutationResult, 1)
	go func() {
		message, err := d.restartCheckpointOwnedWorkerUsing(
			context.Background(), worker, worker.conn,
			func(context.Context, string, string) (bool, error) { return true, nil },
		)
		result <- lifecycleRestartMutationResult{message: message, err: err}
	}()

	got := waitForDispatcherOperationWithin(t, d, result, 250*time.Millisecond,
		"restartCheckpointOwnedWorkerUsing retained the dispatcher mutex")
	if got.err != nil || got.message == "" {
		t.Fatalf("restart result = (%q, %v), want non-empty success", got.message, got.err)
	}
	assertDispatcherMutexAvailableWithin(t, d, 250*time.Millisecond)
}

func TestReleaseReviewWorkerAfterSendFailureUsingBoundedMutation(t *testing.T) {
	const childEnv = "ORO_TEST_RELEASE_REVIEW_WORKER_AFTER_SEND_FAILURE_USING_BOUNDED"
	if os.Getenv(childEnv) != "1" {
		runLifecycleLockMutationSubprocess(t, childEnv, "TestReleaseReviewWorkerAfterSendFailureUsingBoundedMutation")
		runLifecycleMutationTestCases(t,
			"TestReviewSendFailureDefersReleaseUntilCurrentMessageExits",
			"TestReviewWorkerSendFailureDurablyReleasesBeforeFallback",
			"TestReviewWorkerSendFailurePreservesSamePointerReconnect",
			"TestReviewWorkerSendFailureReleaseFailurePreservesMemory",
			"TestReviewWorkerSendFailureStaleGenerationPreservesReplacement",
			"TestReviewWorkerSynchronousSendReleasePanicRestoresCallerLock",
		)
		return
	}

	t.Run("synchronous release restores caller lock", func(t *testing.T) {
		d, worker := newCheckpointWorkerReleaseFixture(t, "bounded-send-failure-sync")
		result := invokeReleaseReviewWorkerAfterSendFailureUsingBounded(t, d, worker,
			func(context.Context, string, string) (bool, error) { return false, nil })
		if !result.attempted || result.released || result.pending || result.err != nil {
			t.Fatalf("release result = %+v, want attempted abort without pending", result)
		}
		assertDispatcherMutexAvailableWithin(t, d, 250*time.Millisecond)
	})

	t.Run("in-flight release returns without waiting for drain", func(t *testing.T) {
		d, worker := newCheckpointWorkerReleaseFixture(t, "bounded-send-failure-drain")
		slot, ok := d.beginReviewWorkerMessage(worker.id, worker.conn)
		if !ok {
			t.Fatal("review message entry rejected")
		}
		finished := false
		result := make(chan lifecycleSendFailureMutationResult, 1)
		t.Cleanup(func() {
			if !finished {
				d.finishReviewWorkerMessage(slot)
				select {
				case <-result:
				case <-time.After(250 * time.Millisecond):
				}
			}
		})
		go func() {
			d.mu.Lock()
			attempted, released, pending, err := d.releaseReviewWorkerAfterSendFailureUsing(
				worker, worker.conn,
				func(context.Context, string, string) (bool, error) { return false, nil },
			)
			d.mu.Unlock()
			result <- lifecycleSendFailureMutationResult{
				attempted: attempted, released: released, pending: pending, err: err,
			}
		}()

		got := waitForDispatcherOperationWithin(t, d, result, 250*time.Millisecond,
			"releaseReviewWorkerAfterSendFailureUsing waited for an in-flight message drain")
		if !got.attempted || got.released || !got.pending || got.err != nil {
			t.Fatalf("draining release result = %+v, want attempted pending release", got)
		}
		d.finishReviewWorkerMessage(slot)
		finished = true
	})
}

func TestRegisterWorkerWithProtocolReleasesMutexBoundedMutation(t *testing.T) {
	const childEnv = "ORO_TEST_REGISTER_WORKER_WITH_PROTOCOL_BOUNDED"
	if os.Getenv(childEnv) != "1" {
		runLifecycleLockMutationSubprocess(t, childEnv, "TestRegisterWorkerWithProtocolReleasesMutexBoundedMutation")
		runLifecycleMutationTestCases(t, "TestCheckpointWorkerReleaseFenceRejectsReconnectAndMessages")
		return
	}

	d, _, _, _, _, _ := newTestDispatcher(t)
	if accepted := d.registerWorkerWithProtocol("bounded-register-worker", newMockConn(), false); !accepted {
		t.Fatal("registerWorkerWithProtocol rejected an idle worker")
	}
	assertDispatcherMutexAvailableWithin(t, d, 250*time.Millisecond)
}

type lifecycleRestartMutationResult struct {
	message string
	err     error
}

type lifecycleSendFailureMutationResult struct {
	attempted bool
	released  bool
	pending   bool
	err       error
}

func invokeReleaseReviewWorkerAfterSendFailureUsingBounded(
	t *testing.T,
	d *Dispatcher,
	worker *trackedWorker,
	releaseFn checkpointWorkerReleaseFunc,
) lifecycleSendFailureMutationResult {
	t.Helper()
	result := make(chan lifecycleSendFailureMutationResult, 1)
	go func() {
		d.mu.Lock()
		attempted, released, pending, err := d.releaseReviewWorkerAfterSendFailureUsing(
			worker, worker.conn, releaseFn,
		)
		d.mu.Unlock()
		result <- lifecycleSendFailureMutationResult{
			attempted: attempted, released: released, pending: pending, err: err,
		}
	}()
	return waitForDispatcherOperationWithin(t, d, result, 250*time.Millisecond,
		"releaseReviewWorkerAfterSendFailureUsing retained the dispatcher mutex")
}

func runLifecycleLockMutationSubprocess(t *testing.T, childEnv, testName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], //nolint:gosec // test helper re-executes this binary with a fixed test name
		"-test.run=^"+testName+"$", "-test.count=1", "-test.timeout=1500ms")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	output, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("%s child exceeded its two-second process boundary: %s", testName, output)
	}
	if err != nil {
		t.Fatalf("%s child failed: %v\n%s", testName, err, output)
	}
}

func runLifecycleMutationTestCases(t *testing.T, testNames ...string) {
	t.Helper()
	for _, testName := range testNames {
		t.Run(testName, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3500*time.Millisecond)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], //nolint:gosec // fixed test names select checked-in lifecycle contracts
				"-test.run=^"+testName+"$", "-test.count=1", "-test.timeout=3s")
			output, err := cmd.CombinedOutput()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("%s exceeded its bounded subprocess contract: %s", testName, output)
			}
			if err != nil {
				t.Fatalf("%s failed: %v\n%s", testName, err, output)
			}
		})
	}
}
