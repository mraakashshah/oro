package dispatcher //nolint:testpackage // white-box lifecycle edge assertions

import (
	"context"
	"errors"
	"net"
	"testing"

	"oro/pkg/protocol"
)

func TestReviewWorkerSendFailureDurablyReleasesBeforeFallback(t *testing.T) {
	for _, pending := range []int{0, maxPendingMessages} {
		name := "direct"
		if pending == maxPendingMessages {
			name = "overflow"
		}
		t.Run(name, func(t *testing.T) {
			d, _, _, _, _, _ := newTestDispatcher(t)
			checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(t, d, "send-"+name, ReviewCheckpointStateCorrectionAssigned, "active")
			worker.conn = &failConn{}
			worker.pendingMsgs = make([]protocol.Message, pending)

			d.mu.Lock()
			err := d.sendToWorker(worker, protocol.Message{Type: protocol.MsgReviewResult})
			d.mu.Unlock()

			var unreachable *protocol.WorkerUnreachableError
			if !errors.As(err, &unreachable) {
				t.Fatalf("sendToWorker error = %T %[1]v, want WorkerUnreachableError", err)
			}
			assertCheckpointOwnedEdgeReleased(t, d, checkpointID, assignmentID, ReviewCheckpointStateCorrectionAssigned)
			if got := trackedReleaseWorker(d, worker.id); got != nil {
				t.Fatalf("released review worker remains tracked: %p", got)
			}
			if got := len(worker.pendingMsgs); got != pending {
				t.Fatalf("review send failure buffered messages = %d, want unchanged %d", got, pending)
			}
			assertCheckpointReleaseEvent(t, d, worker.beadID, worker.id, ReviewReleaseCauseSendFailed)
		})
	}
}

func TestReviewWorkerSendFailureReleaseFailurePreservesMemory(t *testing.T) {
	for _, tc := range []struct {
		name             string
		assignmentStatus string
		terminal         bool
	}{
		{name: "durable conflict", assignmentStatus: "requeued"},
		{name: "durable no-op", assignmentStatus: "active", terminal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, _, _, _, _ := newTestDispatcher(t)
			checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(t, d, "send-fail-"+tc.name, ReviewCheckpointStateReviewRunning, tc.assignmentStatus)
			worker.conn = &failConn{}
			worker.pendingMsgs = make([]protocol.Message, maxPendingMessages)
			if tc.terminal {
				mustExec(t, d, `UPDATE review_checkpoints SET state='integrated' WHERE id=?`, checkpointID)
			}

			d.mu.Lock()
			err := d.sendToWorker(worker, protocol.Message{Type: protocol.MsgReviewResult})
			d.mu.Unlock()

			if err == nil {
				t.Fatal("sendToWorker error = nil, want durable release failure")
			}
			if got := trackedReleaseWorker(d, worker.id); got != worker {
				t.Fatalf("failed durable release changed worker: got %p, want %p", got, worker)
			}
			if got := len(worker.pendingMsgs); got != maxPendingMessages {
				t.Fatalf("failed durable release buffered messages = %d, want unchanged %d", got, maxPendingMessages)
			}
			var checkpointWorker, assignmentStatus string
			if err := d.db.QueryRow(`SELECT COALESCE(worker_id, '') FROM review_checkpoints WHERE id=?`, checkpointID).Scan(&checkpointWorker); err != nil {
				t.Fatalf("load checkpoint worker: %v", err)
			}
			if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
				t.Fatalf("load assignment status: %v", err)
			}
			if checkpointWorker != worker.id || assignmentStatus != tc.assignmentStatus {
				t.Fatalf("durable state changed: worker/status=%q/%q, want %q/%q", checkpointWorker, assignmentStatus, worker.id, tc.assignmentStatus)
			}
			assertCheckpointReleaseEventCount(t, d, 0)
		})
	}
}

func TestReviewWorkerSendFailureStaleGenerationPreservesReplacement(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	_, _, stale := seedCheckpointOwnedEdgeWorker(t, d, "send-replacement", ReviewCheckpointStateReviewRunning, "active")
	stale.conn = &failConn{}
	replacement := &trackedWorker{id: stale.id, beadID: "replacement-bead", conn: newMockConn(), state: protocol.WorkerBusy}
	d.mu.Lock()
	d.workers[stale.id] = replacement
	err := d.sendToWorker(stale, protocol.Message{Type: protocol.MsgReviewResult})
	d.mu.Unlock()

	if err == nil {
		t.Fatal("sendToWorker error = nil, want failed send")
	}
	if got := trackedReleaseWorker(d, stale.id); got != replacement {
		t.Fatalf("stale send changed replacement: got %p, want %p", got, replacement)
	}
	if got := len(stale.pendingMsgs); got != 0 {
		t.Fatalf("stale review send buffered %d messages, want none", got)
	}
}

func TestReviewWorkerSendFailurePreservesSamePointerReconnect(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(t, d, "send-same-pointer", ReviewCheckpointStateReviewRunning, "active")
	replacementConn := newMockConn()
	failingConn := &reconnectingFailConn{
		failConn: &failConn{},
		reconnect: func() {
			worker.conn = replacementConn
		},
	}
	worker.conn = failingConn
	drainCheckpointReleaseWakes(d)

	d.mu.Lock()
	err := d.sendToWorker(worker, protocol.Message{Type: protocol.MsgReviewResult})
	d.mu.Unlock()

	if err == nil {
		t.Fatal("sendToWorker error = nil, want failed stale send")
	}
	if got := trackedReleaseWorker(d, worker.id); got != worker {
		t.Fatalf("failed stale send changed replacement worker: got %p, want %p", got, worker)
	} else if got.conn != replacementConn {
		t.Fatalf("failed stale send changed replacement connection: got %p, want %p", got.conn, replacementConn)
	}
	var checkpointWorker, assignmentStatus string
	if err := d.db.QueryRow(`SELECT COALESCE(worker_id, '') FROM review_checkpoints WHERE id=?`, checkpointID).Scan(&checkpointWorker); err != nil {
		t.Fatalf("load checkpoint worker: %v", err)
	}
	if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("load assignment: %v", err)
	}
	if checkpointWorker != worker.id || assignmentStatus != "active" {
		t.Fatalf("stale send released durable ownership: worker/status=%q/%q, want %q/active", checkpointWorker, assignmentStatus, worker.id)
	}
	if got := len(worker.pendingMsgs); got != 0 {
		t.Fatalf("stale review send buffered %d messages, want none", got)
	}
	assertCheckpointReleaseEventCount(t, d, 0)
	assertNoCheckpointReleaseWake(t, d)
}

func TestReviewWorkerSynchronousSendReleasePanicRestoresCallerLock(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	_, _, worker := seedCheckpointOwnedEdgeWorker(t, d, "send-sync-panic", ReviewCheckpointStateReviewRunning, "active")
	observedConn := worker.conn
	d.mu.Lock()
	panicValue := captureCheckpointReleasePanic(func() {
		_, _, _, _ = d.releaseReviewWorkerAfterSendFailureUsing(
			worker, observedConn,
			func(context.Context, string, string) (bool, error) {
				panic("sync send Store panic")
			},
		)
	})
	if panicValue != "sync send Store panic" {
		t.Fatalf("panic = %v, want sync send Store panic", panicValue)
	}
	if unlockPanic := captureCheckpointReleasePanic(func() { d.mu.Unlock() }); unlockPanic != nil {
		t.Fatalf("caller lock was not restored after panic: %v", unlockPanic)
	}
	d.mu.Lock()
	workersInitialized := d.workers != nil
	d.mu.Unlock()
	if !workersInitialized {
		t.Fatal("dispatcher workers map unusable after panic")
	}
	if got := trackedReleaseWorker(d, worker.id); got != worker {
		t.Fatalf("sync Store panic changed worker: got %p, want %p", got, worker)
	}
}

type reconnectingFailConn struct {
	*failConn
	reconnect func()
}

func (c *reconnectingFailConn) Write([]byte) (int, error) {
	c.reconnect()
	return 0, net.ErrClosed
}
