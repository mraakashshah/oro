package dispatcher //nolint:testpackage // white-box lifecycle edge assertions

import (
	"errors"
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
