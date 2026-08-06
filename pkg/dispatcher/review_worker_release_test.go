//nolint:testpackage // white-box lifecycle ordering assertions
package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func TestReviewReleaseCauseValues(t *testing.T) {
	want := map[ReviewReleaseCause]string{
		ReviewReleaseCauseConnectionLost: "connection_lost",
		ReviewReleaseCauseSendFailed:     "send_failed",
		ReviewReleaseCauseKilled:         "killed",
		ReviewReleaseCauseRestarted:      "restarted",
	}
	if len(want) != 4 {
		t.Fatalf("release cause cardinality = %d, want 4", len(want))
	}
	for cause, value := range want {
		if string(cause) != value {
			t.Fatalf("release cause %q = %q, want %q", cause, cause, value)
		}
	}
}

func TestReleaseCheckpointOwnedWorkerProductionPath(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(
		t, d, "production-path", ReviewCheckpointStateReviewRunning, "active",
	)
	drainCheckpointReleaseWakes(d)

	released, err := d.releaseCheckpointOwnedWorker(
		context.Background(), worker, ReviewReleaseCauseConnectionLost,
	)
	if err != nil || !released {
		t.Fatalf("release checkpoint-owned worker = (%t, %v), want (true, nil)", released, err)
	}
	assertCheckpointOwnedEdgeReleased(t, d, checkpointID, assignmentID, ReviewCheckpointStateReviewRunning)
	if got := trackedReleaseWorker(d, worker.id); got != nil {
		t.Fatalf("released production-path worker remains tracked: %p", got)
	}
	assertCheckpointReleaseEvent(t, d, worker.beadID, worker.id, ReviewReleaseCauseConnectionLost)
	assertOneCheckpointReleaseWake(t, d)
}

func TestCheckpointWorkerReleaseLeaseAdmission(t *testing.T) {
	tests := []struct {
		name        string
		configure   func(*Dispatcher, *trackedWorker, *mockConn) (*trackedWorker, *mockConn)
		wantAcquire bool
		wantDrain   bool
	}{
		{name: "nil expected", configure: func(_ *Dispatcher, _ *trackedWorker, conn *mockConn) (*trackedWorker, *mockConn) { return nil, conn }},
		{name: "missing worker", configure: func(_ *Dispatcher, worker *trackedWorker, conn *mockConn) (*trackedWorker, *mockConn) {
			return worker, conn
		}},
		{name: "stale pointer", configure: func(d *Dispatcher, worker *trackedWorker, conn *mockConn) (*trackedWorker, *mockConn) {
			d.workers[worker.id] = &trackedWorker{id: worker.id, conn: conn, state: protocol.WorkerReviewing, beadID: worker.beadID}
			return worker, conn
		}},
		{name: "connection mismatch", configure: func(d *Dispatcher, worker *trackedWorker, _ *mockConn) (*trackedWorker, *mockConn) {
			d.workers[worker.id] = worker
			return worker, newMockConn()
		}},
		{name: "wrong state", configure: func(d *Dispatcher, worker *trackedWorker, conn *mockConn) (*trackedWorker, *mockConn) {
			worker.state = protocol.WorkerBusy
			d.workers[worker.id] = worker
			return worker, conn
		}},
		{name: "empty bead", configure: func(d *Dispatcher, worker *trackedWorker, conn *mockConn) (*trackedWorker, *mockConn) {
			worker.beadID = ""
			d.workers[worker.id] = worker
			return worker, conn
		}},
		{name: "release already active", configure: func(d *Dispatcher, worker *trackedWorker, conn *mockConn) (*trackedWorker, *mockConn) {
			worker.reviewReleaseToken = 41
			d.workers[worker.id] = worker
			return worker, conn
		}},
		{name: "eligible worker", wantAcquire: true, configure: func(d *Dispatcher, worker *trackedWorker, conn *mockConn) (*trackedWorker, *mockConn) {
			d.workers[worker.id] = worker
			return worker, conn
		}},
		{name: "in flight message creates drain", wantAcquire: true, wantDrain: true, configure: func(d *Dispatcher, worker *trackedWorker, conn *mockConn) (*trackedWorker, *mockConn) {
			worker.reviewMessagesInFlight = 1
			d.workers[worker.id] = worker
			return worker, conn
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _, _, _, _, _ := newTestDispatcher(t)
			conn := newMockConn()
			worker := &trackedWorker{id: "lease-admission", conn: conn, state: protocol.WorkerReviewing, beadID: "bead-lease-admission"}
			expected, observedConn := tt.configure(d, worker, conn)

			d.mu.Lock()
			lease, acquired := d.acquireCheckpointWorkerReleaseLocked(expected, observedConn)
			d.mu.Unlock()
			if acquired != tt.wantAcquire {
				t.Fatalf("acquired = %t, want %t", acquired, tt.wantAcquire)
			}
			if !acquired {
				if lease != nil {
					t.Fatalf("rejected lease = %p, want nil", lease)
				}
				return
			}
			if lease == nil || lease.expected != worker || lease.token == 0 {
				t.Fatalf("acquired lease = %#v, want worker %p and nonzero token", lease, worker)
			}
			if (lease.drain != nil) != tt.wantDrain {
				t.Fatalf("lease drain present = %t, want %t", lease.drain != nil, tt.wantDrain)
			}
			if tt.wantDrain && (worker.reviewMessagesDrained == nil || worker.reviewMessagesDrainToken != lease.token) {
				t.Fatalf("worker drain/token = (%v, %d), want channel and %d", worker.reviewMessagesDrained, worker.reviewMessagesDrainToken, lease.token)
			}
			lease.abort()
		})
	}
}

func TestHandleMessageFromConnectionRoutesAcceptedMessage(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	conn := newMockConn()
	worker := &trackedWorker{id: "message-route", conn: conn, state: protocol.WorkerIdle}
	d.workers[worker.id] = worker

	d.handleMessageFromConnection(context.Background(), worker.id, conn, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   worker.id,
			ContextPct: 37,
		},
	})
	if worker.lastSeen.IsZero() {
		t.Fatal("accepted connection message was not dispatched")
	}
	if worker.reviewMessagesInFlight != 0 {
		t.Fatalf("message in-flight count = %d, want 0 after dispatch", worker.reviewMessagesInFlight)
	}
}

func TestBeginReviewWorkerResultAdmission(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	if worker, accepted := d.beginReviewWorkerResult("absent-result-worker"); worker != nil || !accepted {
		t.Fatalf("absent worker admission = (%p, %t), want (nil, true)", worker, accepted)
	}

	worker := &trackedWorker{id: "result-admission", state: protocol.WorkerReviewing, beadID: "bead-result-admission"}
	d.workers[worker.id] = worker
	got, accepted := d.beginReviewWorkerResult(worker.id)
	if !accepted || got != worker {
		t.Fatalf("tracked worker admission = (%p, %t), want (%p, true)", got, accepted, worker)
	}
	if worker.reviewMessagesInFlight != 1 {
		t.Fatalf("result in-flight count = %d, want 1", worker.reviewMessagesInFlight)
	}
	d.finishReviewWorkerMessage(worker)
	if worker.reviewMessagesInFlight != 0 {
		t.Fatalf("result in-flight count after finish = %d, want 0", worker.reviewMessagesInFlight)
	}
}

func TestReleaseCheckpointOwnedWorkerDurableBeforeMemory(t *testing.T) {
	ctx := context.Background()

	t.Run("success deletes exact worker then emits one event and wake", func(t *testing.T) {
		d, expected := newCheckpointWorkerReleaseFixture(t, "success")
		calls := 0
		releaseFn := func(_ context.Context, beadID, workerID string) (bool, error) {
			calls++
			if beadID != expected.beadID || workerID != expected.id {
				t.Fatalf("durable release identity = %q/%q, want %q/%q", beadID, workerID, expected.beadID, expected.id)
			}
			if got := trackedReleaseWorker(d, expected.id); got != expected {
				t.Fatalf("worker changed before durable release: got %p, want %p", got, expected)
			}
			assertCheckpointReleaseEventCount(t, d, 0)
			assertNoCheckpointReleaseWake(t, d)
			return true, nil
		}

		released, err := d.releaseCheckpointOwnedWorkerUsing(ctx, expected, ReviewReleaseCauseSendFailed, releaseFn)
		if err != nil || !released {
			t.Fatalf("releaseCheckpointOwnedWorkerUsing() = (%t, %v), want (true, nil)", released, err)
		}
		if calls != 1 {
			t.Fatalf("durable release calls = %d, want 1", calls)
		}
		if got := trackedReleaseWorker(d, expected.id); got != nil {
			t.Fatalf("released worker remains tracked: %p", got)
		}
		assertCheckpointReleaseEvent(t, d, expected.beadID, expected.id, ReviewReleaseCauseSendFailed)
		assertOneCheckpointReleaseWake(t, d)
	})

	t.Run("durable no-op preserves memory without event or wake", func(t *testing.T) {
		d, expected := newCheckpointWorkerReleaseFixture(t, "no-op")
		calls := 0
		released, err := d.releaseCheckpointOwnedWorkerUsing(ctx, expected, ReviewReleaseCauseConnectionLost,
			func(context.Context, string, string) (bool, error) {
				calls++
				return false, nil
			})
		if err != nil || released {
			t.Fatalf("releaseCheckpointOwnedWorkerUsing() = (%t, %v), want (false, nil)", released, err)
		}
		if calls != 1 {
			t.Fatalf("durable release calls = %d, want 1", calls)
		}
		if got := trackedReleaseWorker(d, expected.id); got != expected {
			t.Fatalf("durable no-op changed worker: got %p, want %p", got, expected)
		}
		assertCheckpointReleaseEventCount(t, d, 0)
		assertNoCheckpointReleaseWake(t, d)
	})

	t.Run("durable error preserves memory without event or wake", func(t *testing.T) {
		d, expected := newCheckpointWorkerReleaseFixture(t, "error")
		wantErr := errors.New("injected durable release failure")
		calls := 0
		released, err := d.releaseCheckpointOwnedWorkerUsing(ctx, expected, ReviewReleaseCauseKilled,
			func(context.Context, string, string) (bool, error) {
				calls++
				return false, wantErr
			})
		if released || !errors.Is(err, wantErr) {
			t.Fatalf("releaseCheckpointOwnedWorkerUsing() = (%t, %v), want (false, injected error)", released, err)
		}
		if calls != 1 {
			t.Fatalf("durable release calls = %d, want 1", calls)
		}
		if got := trackedReleaseWorker(d, expected.id); got != expected {
			t.Fatalf("durable error changed worker: got %p, want %p", got, expected)
		}
		assertCheckpointReleaseEventCount(t, d, 0)
		assertNoCheckpointReleaseWake(t, d)
	})

	t.Run("reconnect during durable release keeps replacement", func(t *testing.T) {
		d, expected := newCheckpointWorkerReleaseFixture(t, "reconnect")
		replacement := &trackedWorker{
			id:     expected.id,
			beadID: "replacement-bead",
			conn:   newMockConn(),
		}
		released, err := d.releaseCheckpointOwnedWorkerUsing(ctx, expected, ReviewReleaseCauseRestarted,
			func(context.Context, string, string) (bool, error) {
				d.mu.Lock()
				d.workers[expected.id] = replacement
				d.mu.Unlock()
				return true, nil
			})
		if err != nil || !released {
			t.Fatalf("releaseCheckpointOwnedWorkerUsing() = (%t, %v), want (true, nil)", released, err)
		}
		if got := trackedReleaseWorker(d, expected.id); got != replacement {
			t.Fatalf("replacement worker changed: got %p, want %p", got, replacement)
		}
		assertCheckpointReleaseEvent(t, d, expected.beadID, expected.id, ReviewReleaseCauseRestarted)
		assertOneCheckpointReleaseWake(t, d)
	})

	t.Run("stale expected pointer does not call durable store", func(t *testing.T) {
		d, live := newCheckpointWorkerReleaseFixture(t, "stale")
		stale := &trackedWorker{id: live.id, beadID: live.beadID, conn: live.conn}
		calls := 0
		released, err := d.releaseCheckpointOwnedWorkerUsing(ctx, stale, ReviewReleaseCauseConnectionLost,
			func(context.Context, string, string) (bool, error) {
				calls++
				return true, nil
			})
		if err != nil || released {
			t.Fatalf("releaseCheckpointOwnedWorkerUsing() = (%t, %v), want (false, nil)", released, err)
		}
		if calls != 0 {
			t.Fatalf("durable release calls = %d, want 0", calls)
		}
		if got := trackedReleaseWorker(d, live.id); got != live {
			t.Fatalf("stale release changed live worker: got %p, want %p", got, live)
		}
		assertCheckpointReleaseEventCount(t, d, 0)
		assertNoCheckpointReleaseWake(t, d)
	})

	t.Run("repeated release is idempotent", func(t *testing.T) {
		d, expected := newCheckpointWorkerReleaseFixture(t, "repeat")
		calls := 0
		releaseFn := func(context.Context, string, string) (bool, error) {
			calls++
			return true, nil
		}
		if released, err := d.releaseCheckpointOwnedWorkerUsing(ctx, expected, ReviewReleaseCauseKilled, releaseFn); err != nil || !released {
			t.Fatalf("first release = (%t, %v), want (true, nil)", released, err)
		}
		assertOneCheckpointReleaseWake(t, d)

		if released, err := d.releaseCheckpointOwnedWorkerUsing(ctx, expected, ReviewReleaseCauseKilled, releaseFn); err != nil || released {
			t.Fatalf("repeated release = (%t, %v), want (false, nil)", released, err)
		}
		if calls != 1 {
			t.Fatalf("durable release calls = %d, want 1", calls)
		}
		assertCheckpointReleaseEventCount(t, d, 1)
		assertNoCheckpointReleaseWake(t, d)
	})
}

func TestReleaseCheckpointOwnedWorkerRejectsStaleConnectionGeneration(t *testing.T) {
	ctx := context.Background()

	t.Run("reconnect before durable release skips store", func(t *testing.T) {
		d, expected := newCheckpointWorkerReleaseFixture(t, "conn-before-store")
		observedConn := expected.conn
		replacementConn := newMockConn()
		d.mu.Lock()
		expected.conn = replacementConn
		d.mu.Unlock()
		calls := 0

		released, err := d.releaseCheckpointOwnedWorkerGenerationUsing(
			ctx, expected, observedConn, ReviewReleaseCauseConnectionLost,
			func(context.Context, string, string) (bool, error) {
				calls++
				return true, nil
			},
		)

		if err != nil || released {
			t.Fatalf("stale generation release = (%t, %v), want (false, nil)", released, err)
		}
		if calls != 0 {
			t.Fatalf("durable release calls = %d, want 0", calls)
		}
		if got := trackedReleaseWorker(d, expected.id); got != expected {
			t.Fatalf("replacement worker changed: got %p, want %p", got, expected)
		} else if got.conn != replacementConn {
			t.Fatalf("replacement connection changed: got %p, want %p", got.conn, replacementConn)
		}
		assertCheckpointReleaseEventCount(t, d, 0)
		assertNoCheckpointReleaseWake(t, d)
	})

	t.Run("reconnect during durable release survives deletion", func(t *testing.T) {
		d, expected := newCheckpointWorkerReleaseFixture(t, "conn-after-store")
		observedConn := expected.conn
		replacementConn := newMockConn()

		released, err := d.releaseCheckpointOwnedWorkerGenerationUsing(
			ctx, expected, observedConn, ReviewReleaseCauseSendFailed,
			func(context.Context, string, string) (bool, error) {
				d.mu.Lock()
				expected.conn = replacementConn
				d.mu.Unlock()
				return true, nil
			},
		)

		if err != nil || !released {
			t.Fatalf("raced generation release = (%t, %v), want (true, nil)", released, err)
		}
		if got := trackedReleaseWorker(d, expected.id); got != expected {
			t.Fatalf("replacement worker changed: got %p, want %p", got, expected)
		} else if got.conn != replacementConn {
			t.Fatalf("replacement connection changed: got %p, want %p", got.conn, replacementConn)
		}
		assertCheckpointReleaseEvent(t, d, expected.beadID, expected.id, ReviewReleaseCauseSendFailed)
		assertOneCheckpointReleaseWake(t, d)
	})
}

func TestCheckpointWorkerReleaseFenceRejectsReconnectAndMessages(t *testing.T) {
	ctx := context.Background()
	d, expected := newCheckpointWorkerReleaseFixture(t, "fence-abort")
	observedConn := expected.conn
	workerID := expected.id
	d.mu.Lock()
	d.pendingManagedIDs[workerID] = true
	d.pendingManagedSince[workerID] = d.nowFunc()
	d.pendingExternalIDs[workerID] = true
	d.pendingExternalSince[workerID] = d.nowFunc()
	d.pendingWorkerTargets[workerID] = "pending-target"
	d.pendingSpawnForWorkers[workerID] = true
	expected.lastSeen = time.Unix(123, 0)
	d.mu.Unlock()

	storeStarted := make(chan struct{})
	releaseStore := make(chan struct{})
	result := make(chan struct {
		released bool
		err      error
	}, 1)
	go func() {
		released, err := d.releaseCheckpointOwnedWorkerGenerationUsing(
			ctx, expected, observedConn, ReviewReleaseCauseConnectionLost,
			func(context.Context, string, string) (bool, error) {
				close(storeStarted)
				<-releaseStore
				return false, nil
			},
		)
		result <- struct {
			released bool
			err      error
		}{released: released, err: err}
	}()
	<-storeStarted

	rejectedConn := newMockConn()
	if accepted := d.registerWorkerWithProtocol(workerID, rejectedConn, false); accepted {
		t.Fatal("same-ID reconnect accepted while review release fenced")
	}
	if !rejectedConn.closed {
		t.Fatal("rejected reconnect connection remains open")
	}
	d.handleMessage(ctx, workerID, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   workerID,
			ContextPct: 99,
		},
	})
	d.mu.Lock()
	if expected.conn != observedConn || !d.pendingManagedIDs[workerID] || !d.pendingExternalIDs[workerID] ||
		d.pendingWorkerTargets[workerID] != "pending-target" || !d.pendingSpawnForWorkers[workerID] {
		d.mu.Unlock()
		t.Fatal("fenced reconnect mutated worker or consumed pending registration state")
	}
	if !expected.lastSeen.Equal(time.Unix(123, 0)) || expected.contextPct != 0 {
		d.mu.Unlock()
		t.Fatalf("fenced heartbeat mutated worker: lastSeen=%v contextPct=%d", expected.lastSeen, expected.contextPct)
	}
	if expected.reviewReleaseToken == 0 {
		d.mu.Unlock()
		t.Fatal("review release fence token was not installed before Store")
	}
	d.mu.Unlock()

	close(releaseStore)
	got := <-result
	if got.err != nil || got.released {
		t.Fatalf("aborted release = (%t, %v), want (false, nil)", got.released, got.err)
	}
	d.mu.Lock()
	if expected.reviewReleaseToken != 0 {
		d.mu.Unlock()
		t.Fatalf("aborted release retained token %d", expected.reviewReleaseToken)
	}
	d.mu.Unlock()
	assertCheckpointReleaseEventCount(t, d, 0)
	assertNoCheckpointReleaseWake(t, d)

	acceptedConn := newMockConn()
	if accepted := d.registerWorkerWithProtocol(workerID, acceptedConn, false); !accepted {
		t.Fatal("same-ID reconnect rejected after release fence cleared")
	}
	if got := trackedReleaseWorker(d, workerID); got != expected || got.conn != acceptedConn {
		t.Fatalf("post-abort reconnect = worker %p conn %p, want %p/%p", got, got.conn, expected, acceptedConn)
	}
}

func TestCheckpointWorkerReleaseFenceTokenCannotBeClearedBySecondRelease(t *testing.T) {
	ctx := context.Background()
	d, expected := newCheckpointWorkerReleaseFixture(t, "fence-owner")
	observedConn := expected.conn
	storeStarted := make(chan struct{})
	releaseStore := make(chan struct{})
	firstDone := make(chan struct{}, 1)
	go func() {
		_, _ = d.releaseCheckpointOwnedWorkerGenerationUsing(ctx, expected, observedConn, ReviewReleaseCauseConnectionLost,
			func(context.Context, string, string) (bool, error) {
				close(storeStarted)
				<-releaseStore
				return false, nil
			})
		firstDone <- struct{}{}
	}()
	<-storeStarted

	var secondCalls atomic.Int32
	if released, err := d.releaseCheckpointOwnedWorkerGenerationUsing(ctx, expected, observedConn, ReviewReleaseCauseKilled,
		func(context.Context, string, string) (bool, error) {
			secondCalls.Add(1)
			return true, nil
		}); err != nil || released {
		t.Fatalf("second release = (%t, %v), want (false, nil)", released, err)
	}
	if secondCalls.Load() != 0 {
		t.Fatalf("second release reached Store %d times", secondCalls.Load())
	}
	d.mu.Lock()
	token := expected.reviewReleaseToken
	d.mu.Unlock()
	if token == 0 {
		t.Fatal("second release cleared first release token")
	}

	close(releaseStore)
	<-firstDone
	d.mu.Lock()
	token = expected.reviewReleaseToken
	d.mu.Unlock()
	if token != 0 {
		t.Fatalf("token after owner abort = %d, want 0", token)
	}
}

func TestCheckpointWorkerReleaseWaitsForAllInFlightMessages(t *testing.T) {
	ctx := context.Background()
	d, expected := newCheckpointWorkerReleaseFixture(t, "message-drain")
	first, ok := d.beginReviewWorkerMessage(expected.id, expected.conn)
	if !ok || first != expected {
		t.Fatal("first message entry rejected")
	}
	second, ok := d.beginReviewWorkerMessage(expected.id, expected.conn)
	if !ok || second != expected {
		t.Fatal("second message entry rejected")
	}
	storeStarted := make(chan struct{})
	releaseStore := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := d.releaseCheckpointOwnedWorkerGenerationUsing(ctx, expected, expected.conn, ReviewReleaseCauseConnectionLost,
			func(context.Context, string, string) (bool, error) {
				close(storeStarted)
				<-releaseStore
				return false, nil
			})
		done <- err
	}()
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return expected.reviewReleaseToken != 0
	}, time.Second)
	select {
	case <-storeStarted:
		t.Fatal("Store started before in-flight messages drained")
	default:
	}
	if _, accepted := d.beginReviewWorkerMessage(expected.id, expected.conn); accepted {
		t.Fatal("third message entered after release token installed")
	}
	d.finishReviewWorkerMessage(first)
	select {
	case <-storeStarted:
		t.Fatal("Store started with one message still in flight")
	default:
	}
	d.finishReviewWorkerMessage(second)
	<-storeStarted
	close(releaseStore)
	if err := <-done; err != nil {
		t.Fatalf("drained release: %v", err)
	}
}

func TestCheckpointWorkerReleaseFenceRejectsConcurrentReviewResult(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(
		t, d, "result-fence", ReviewCheckpointStateReviewRunning, "active",
	)
	worker.conn = &failConn{}
	worker.pendingMsgs = make([]protocol.Message, maxPendingMessages)
	storeStarted := make(chan struct{})
	releaseStore := make(chan struct{})
	releaseDone := make(chan struct {
		released bool
		err      error
	}, 1)
	go func() {
		released, err := d.releaseCheckpointOwnedWorkerGenerationUsing(
			ctx, worker, worker.conn, ReviewReleaseCauseConnectionLost,
			func(context.Context, string, string) (bool, error) {
				close(storeStarted)
				<-releaseStore
				return false, nil
			},
		)
		releaseDone <- struct {
			released bool
			err      error
		}{released: released, err: err}
	}()
	<-storeStarted

	resultCh := make(chan ops.Result, 1)
	resultCh <- ops.Result{Verdict: ops.VerdictRejected, Feedback: "retry feedback"}
	d.handleReviewResultForAssignment(ctx, worker.id, worker.beadID, assignmentID, resultCh)

	close(releaseStore)
	result := <-releaseDone
	if result.err != nil || result.released {
		t.Fatalf("aborted release = (%t, %v), want (false, nil)", result.released, result.err)
	}
	if got := trackedReleaseWorker(d, worker.id); got != worker {
		t.Fatalf("fenced result changed worker: got %p, want %p", got, worker)
	}
	if worker.state != protocol.WorkerReviewing {
		t.Fatalf("fenced result changed state to %q, want %q", worker.state, protocol.WorkerReviewing)
	}
	if got := len(worker.pendingMsgs); got != maxPendingMessages {
		t.Fatalf("fenced result changed pending messages to %d, want %d", got, maxPendingMessages)
	}
	var checkpointWorker, assignmentStatus string
	if err := d.db.QueryRow(`SELECT COALESCE(worker_id, '') FROM review_checkpoints WHERE id=?`, checkpointID).
		Scan(&checkpointWorker); err != nil {
		t.Fatalf("load checkpoint worker: %v", err)
	}
	if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("load assignment status: %v", err)
	}
	if checkpointWorker != worker.id || assignmentStatus != "active" {
		t.Fatalf("fenced result changed durable state: worker/status=%q/%q, want %q/active",
			checkpointWorker, assignmentStatus, worker.id)
	}
	if got := eventCount(t, d.db, "review_rejected"); got != 0 {
		t.Fatalf("fenced result emitted %d review_rejected events, want 0", got)
	}
	assertCheckpointReleaseEventCount(t, d, 0)
	assertNoCheckpointReleaseWake(t, d)
}

func TestReviewReleaseTokenFencesDirectReviewTransitions(t *testing.T) {
	t.Run("dependency claim", func(t *testing.T) {
		d, worker := newCheckpointWorkerReleaseFixture(t, "direct-dependency")
		worker.assignmentID = 41
		worker.reviewReleaseToken = 1

		if assignmentID, claimed := d.claimReviewDependencyCheck(worker.id, worker.beadID); claimed || assignmentID != 0 {
			t.Fatalf("fenced dependency claim = (%d, %t), want (0, false)", assignmentID, claimed)
		}
		if worker.state != protocol.WorkerReviewing {
			t.Fatalf("fenced dependency claim changed state to %q", worker.state)
		}
	})

	t.Run("blocked assignment claim", func(t *testing.T) {
		d, worker := newCheckpointWorkerReleaseFixture(t, "direct-blocked")
		worker.assignmentID = 42
		worker.reviewReleaseToken = 1

		if assignmentID, claimed := d.claimBlockedReviewAssignment(worker.id, worker.beadID, worker.assignmentID); claimed || assignmentID != 0 {
			t.Fatalf("fenced blocked claim = (%d, %t), want (0, false)", assignmentID, claimed)
		}
		if worker.state != protocol.WorkerReviewing {
			t.Fatalf("fenced blocked claim changed state to %q", worker.state)
		}
	})

	t.Run("retry reservation", func(t *testing.T) {
		d, worker := newCheckpointWorkerReleaseFixture(t, "direct-retry")
		worker.assignmentID = 43
		worker.reviewReleaseToken = 1

		reserved, err := d.reserveReviewRetryAttempt(context.Background(), worker.id, worker.beadID, "feedback")
		if err != nil || reserved {
			t.Fatalf("fenced retry reservation = (%t, %v), want (false, nil)", reserved, err)
		}
		if worker.state != protocol.WorkerReviewing {
			t.Fatalf("fenced retry reservation changed state to %q", worker.state)
		}
	})

	t.Run("review match", func(t *testing.T) {
		d, worker := newCheckpointWorkerReleaseFixture(t, "direct-match")
		worker.reviewReleaseToken = 1

		if d.reviewingWorkerMatches(worker.id, worker.beadID) {
			t.Fatal("fenced worker matched active review")
		}
	})

	t.Run("approval send", func(t *testing.T) {
		d, worker := newCheckpointWorkerReleaseFixture(t, "direct-approval")
		conn := worker.conn.(*mockConn)
		worker.reviewReleaseToken = 1

		d.sendReviewApproved(worker.id, "approved")

		conn.mu.Lock()
		writes := len(conn.written)
		conn.mu.Unlock()
		if writes != 0 {
			t.Fatalf("fenced approval wrote %d messages, want 0", writes)
		}
	})

	t.Run("reservation completion", func(t *testing.T) {
		d, worker := newCheckpointWorkerReleaseFixture(t, "direct-reservation")
		worker.state = protocol.WorkerReserved
		worker.reviewReleaseToken = 1
		assigned := false

		if d.withReservation(worker.id, func() string { return "context" }, func(*trackedWorker, string) bool {
			assigned = true
			return true
		}) {
			t.Fatal("fenced reservation completed")
		}
		if assigned {
			t.Fatal("fenced reservation invoked assignment callback")
		}
	})

	t.Run("pre-review dirty feedback", func(t *testing.T) {
		d, worker := newCheckpointWorkerReleaseFixture(t, "direct-dirty")
		conn := worker.conn.(*mockConn)
		worker.reviewReleaseToken = 1

		d.sendPreReviewGitDirtyFeedback(context.Background(), worker.id, "dirty")

		if worker.state != protocol.WorkerReviewing {
			t.Fatalf("fenced dirty feedback changed state to %q", worker.state)
		}
		conn.mu.Lock()
		writes := len(conn.written)
		conn.mu.Unlock()
		if writes != 0 {
			t.Fatalf("fenced dirty feedback wrote %d messages, want 0", writes)
		}
	})

	t.Run("ops-run restore", func(t *testing.T) {
		d, worker := newCheckpointWorkerReleaseFixture(t, "direct-ops-restore")
		worker.assignmentID = 44
		worker.worktree = "/tmp/review-restore"
		worker.targetBranch = "main"
		worker.reviewReleaseToken = 1
		d.mu.Lock()
		reviewCtx := d.reviewContextFromWorkerLocked(OpsRunRecord{WorkerID: worker.id, BeadID: worker.beadID})
		d.mu.Unlock()

		if reviewCtx != (reviewOpsRunContext{}) {
			t.Fatalf("fenced ops-run restore = %#v, want empty context", reviewCtx)
		}
	})
}

func TestCheckpointWorkerReleaseContextCancelClearsOwnedDrain(t *testing.T) {
	d, expected := newCheckpointWorkerReleaseFixture(t, "message-cancel")
	slot, ok := d.beginReviewWorkerMessage(expected.id, expected.conn)
	if !ok {
		t.Fatal("message entry rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	var storeCalls atomic.Int32
	done := make(chan error, 1)
	go func() {
		_, err := d.releaseCheckpointOwnedWorkerGenerationUsing(ctx, expected, expected.conn, ReviewReleaseCauseConnectionLost,
			func(context.Context, string, string) (bool, error) {
				storeCalls.Add(1)
				return true, nil
			})
		done <- err
	}()
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return expected.reviewReleaseToken != 0
	}, time.Second)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled release error = %v, want context.Canceled", err)
	}
	if storeCalls.Load() != 0 {
		t.Fatalf("canceled release reached Store %d times", storeCalls.Load())
	}
	d.mu.Lock()
	token := expected.reviewReleaseToken
	d.mu.Unlock()
	if token != 0 {
		t.Fatalf("canceled release retained token %d", token)
	}
	d.finishReviewWorkerMessage(slot)
}

func TestReviewSendFailureDefersReleaseUntilCurrentMessageExits(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(t, d, "send-self-message", ReviewCheckpointStateReviewRunning, "active")
	worker.conn = &failConn{}
	slot, ok := d.beginReviewWorkerMessage(worker.id, worker.conn)
	if !ok {
		t.Fatal("message entry rejected")
	}

	d.mu.Lock()
	err := d.sendToWorker(worker, protocol.Message{Type: protocol.MsgReviewResult})
	d.mu.Unlock()
	if err == nil {
		t.Fatal("send error = nil")
	}
	var checkpointWorker, assignmentStatus string
	if err := d.db.QueryRow(`SELECT COALESCE(worker_id, '') FROM review_checkpoints WHERE id=?`, checkpointID).Scan(&checkpointWorker); err != nil {
		t.Fatalf("load checkpoint before message exit: %v", err)
	}
	if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("load assignment before message exit: %v", err)
	}
	if checkpointWorker != worker.id || assignmentStatus != "active" {
		t.Fatalf("send released before message exit: worker/status=%q/%q", checkpointWorker, assignmentStatus)
	}
	d.finishReviewWorkerMessage(slot)
	waitFor(t, func() bool {
		var checkpointWorker, assignmentStatus string
		if err := d.db.QueryRow(`SELECT COALESCE(worker_id, '') FROM review_checkpoints WHERE id=?`, checkpointID).Scan(&checkpointWorker); err != nil {
			return false
		}
		if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
			return false
		}
		return checkpointWorker == "" && assignmentStatus == "requeued"
	}, time.Second)
}

func TestCheckpointWorkerReleaseShutdownAbortsBeforeStore(t *testing.T) {
	d, expected := newCheckpointWorkerReleaseFixture(t, "shutdown-abort")
	close(d.shutdownCh)
	var storeCalls atomic.Int32

	released, err := d.releaseCheckpointOwnedWorkerGenerationUsing(
		context.Background(), expected, expected.conn, ReviewReleaseCauseConnectionLost,
		func(context.Context, string, string) (bool, error) {
			storeCalls.Add(1)
			return true, nil
		},
	)

	if released || !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown release = (%t, %v), want (false, context.Canceled)", released, err)
	}
	if storeCalls.Load() != 0 {
		t.Fatalf("shutdown release reached Store %d times", storeCalls.Load())
	}
	d.mu.Lock()
	token := expected.reviewReleaseToken
	d.mu.Unlock()
	if token != 0 {
		t.Fatalf("shutdown release retained token %d", token)
	}
}

func TestCheckpointWorkerReleasePanicCleanup(t *testing.T) {
	t.Run("precommit Store panic aborts fence", func(t *testing.T) {
		d, expected := newCheckpointWorkerReleaseFixture(t, "store-panic")
		panicValue := captureCheckpointReleasePanic(func() {
			_, _ = d.releaseCheckpointOwnedWorkerGenerationUsing(
				context.Background(), expected, expected.conn, ReviewReleaseCauseConnectionLost,
				func(context.Context, string, string) (bool, error) {
					panic("store panic")
				},
			)
		})
		if panicValue != "store panic" {
			t.Fatalf("panic = %v, want store panic", panicValue)
		}
		if got := trackedReleaseWorker(d, expected.id); got != expected {
			t.Fatalf("Store panic changed worker: got %p, want %p", got, expected)
		}
		d.mu.Lock()
		token := expected.reviewReleaseToken
		d.mu.Unlock()
		if token != 0 {
			t.Fatalf("Store panic retained token %d", token)
		}
		assertCheckpointReleaseEventCount(t, d, 0)
		assertNoCheckpointReleaseWake(t, d)
	})

	t.Run("postcommit action panic finalizes durable truth", func(t *testing.T) {
		d, expected := newCheckpointWorkerReleaseFixture(t, "action-panic")
		panicValue := captureCheckpointReleasePanic(func() {
			_, _ = d.releaseCheckpointOwnedWorkerGenerationWithActionUsing(
				context.Background(), expected, expected.conn, ReviewReleaseCauseRestarted,
				func(context.Context, string, string) (bool, error) { return true, nil },
				func(*checkpointWorkerReleaseLease) error { panic("action panic") },
				nil,
			)
		})
		if panicValue != "action panic" {
			t.Fatalf("panic = %v, want action panic", panicValue)
		}
		if got := trackedReleaseWorker(d, expected.id); got != nil {
			t.Fatalf("action panic left worker tracked: %p", got)
		}
		assertCheckpointReleaseEvent(t, d, expected.beadID, expected.id, ReviewReleaseCauseRestarted)
		assertOneCheckpointReleaseWake(t, d)
	})
}

func captureCheckpointReleasePanic(call func()) (recovered any) {
	defer func() { recovered = recover() }()
	call()
	return nil
}

func newCheckpointWorkerReleaseFixture(t *testing.T, suffix string) (*Dispatcher, *trackedWorker) {
	t.Helper()
	d, _, _, _, _, _ := newTestDispatcher(t)
	worker := &trackedWorker{
		id:     "checkpoint-release-worker-" + suffix,
		beadID: "checkpoint-release-bead-" + suffix,
		conn:   newMockConn(),
		state:  protocol.WorkerReviewing,
	}
	d.mu.Lock()
	d.workers[worker.id] = worker
	d.mu.Unlock()
	drainCheckpointReleaseWakes(d)
	return d, worker
}

func trackedReleaseWorker(d *Dispatcher, workerID string) *trackedWorker {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.workers[workerID]
}

func assertCheckpointReleaseEvent(t *testing.T, d *Dispatcher, beadID, workerID string, cause ReviewReleaseCause) {
	t.Helper()
	var gotBeadID, gotWorkerID, payload string
	if err := d.db.QueryRow(`SELECT bead_id, worker_id, payload FROM events
		WHERE type='review_checkpoint_worker_released' ORDER BY id`).Scan(&gotBeadID, &gotWorkerID, &payload); err != nil {
		t.Fatalf("load release event: %v", err)
	}
	var got struct {
		Cause ReviewReleaseCause `json:"cause"`
	}
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("decode release event payload %q: %v", payload, err)
	}
	if gotBeadID != beadID || gotWorkerID != workerID || got.Cause != cause {
		t.Fatalf("release event = bead %q worker %q cause %q, want %q/%q/%q",
			gotBeadID, gotWorkerID, got.Cause, beadID, workerID, cause)
	}
	assertCheckpointReleaseEventCount(t, d, 1)
}

func assertCheckpointReleaseEventCount(t *testing.T, d *Dispatcher, want int) {
	t.Helper()
	var got int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='review_checkpoint_worker_released'`).Scan(&got); err != nil {
		t.Fatalf("count release events: %v", err)
	}
	if got != want {
		t.Fatalf("release event count = %d, want %d", got, want)
	}
}

func assertOneCheckpointReleaseWake(t *testing.T, d *Dispatcher) {
	t.Helper()
	select {
	case <-d.workerReadyCh:
	default:
		t.Fatal("checkpoint worker release did not wake assignment")
	}
	assertNoCheckpointReleaseWake(t, d)
}

func assertNoCheckpointReleaseWake(t *testing.T, d *Dispatcher) {
	t.Helper()
	select {
	case <-d.workerReadyCh:
		t.Fatal("unexpected checkpoint worker release assignment wake")
	default:
	}
}

func drainCheckpointReleaseWakes(d *Dispatcher) {
	for {
		select {
		case <-d.workerReadyCh:
		default:
			return
		}
	}
}
