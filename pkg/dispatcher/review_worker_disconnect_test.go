package dispatcher //nolint:testpackage // white-box lifecycle edge assertions

import (
	"context"
	"testing"

	"oro/pkg/protocol"
)

func TestReviewWorkerDisconnectDurablyReleasesCurrentCheckpoint(t *testing.T) {
	for _, phase := range []ReviewCheckpointState{
		ReviewCheckpointStateReviewRunning,
		ReviewCheckpointStateCorrectionAssigning,
		ReviewCheckpointStateCorrectionAssigned,
		ReviewCheckpointStateContractRepairRunning,
		ReviewCheckpointStateRecoveryRunning,
		ReviewCheckpointStateManualIntegrationPending,
		ReviewCheckpointStateIntegrating,
	} {
		t.Run(string(phase), func(t *testing.T) {
			d, beadSrc, _, _, _, _ := newTestDispatcher(t)
			checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(t, d, "disconnect-"+string(phase), phase, "active")

			d.connCloseCleanup(worker.id, worker.conn)

			assertCheckpointOwnedEdgeReleased(t, d, checkpointID, assignmentID, phase)
			if got := trackedReleaseWorker(d, worker.id); got != nil {
				t.Fatalf("disconnected review worker remains tracked: %p", got)
			}
			beadSrc.mu.Lock()
			updated := beadSrc.updated[worker.beadID]
			beadSrc.mu.Unlock()
			if updated != "" {
				t.Fatalf("checkpoint-owned bead status changed to %q", updated)
			}
		})
	}
}

func TestReviewWorkerDisconnectReleaseFailurePreservesWorker(t *testing.T) {
	for _, tc := range []struct {
		name             string
		assignmentStatus string
		terminal         bool
	}{
		{name: "durable conflict", assignmentStatus: "requeued"},
		{name: "durable no-op", assignmentStatus: "active", terminal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, beadSrc, _, _, _, _ := newTestDispatcher(t)
			checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(t, d, "disconnect-fail-"+tc.name, ReviewCheckpointStateReviewRunning, tc.assignmentStatus)
			if tc.terminal {
				mustExec(t, d, `UPDATE review_checkpoints SET state='integrated' WHERE id=?`, checkpointID)
			}

			d.connCloseCleanup(worker.id, worker.conn)

			if got := trackedReleaseWorker(d, worker.id); got != worker {
				t.Fatalf("failed durable release changed worker: got %p, want %p", got, worker)
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
			beadSrc.mu.Lock()
			updated := beadSrc.updated[worker.beadID]
			beadSrc.mu.Unlock()
			if updated != "" {
				t.Fatalf("failed release fell through to bead status %q", updated)
			}
		})
	}
}

func TestReviewWorkerDisconnectStaleConnectionPreservesReplacement(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	_, _, stale := seedCheckpointOwnedEdgeWorker(t, d, "disconnect-reconnect", ReviewCheckpointStateReviewRunning, "active")
	replacement := &trackedWorker{id: stale.id, beadID: "replacement-bead", conn: newMockConn(), state: protocol.WorkerBusy}
	d.mu.Lock()
	d.workers[stale.id] = replacement
	d.mu.Unlock()

	d.connCloseCleanup(stale.id, stale.conn)

	if got := trackedReleaseWorker(d, stale.id); got != replacement {
		t.Fatalf("stale disconnect changed replacement: got %p, want %p", got, replacement)
	}
}

func TestReviewWorkerDisconnectStaleConnectionPreservesSamePointerReconnect(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	_, _, worker := seedCheckpointOwnedEdgeWorker(t, d, "disconnect-same-pointer", ReviewCheckpointStateReviewRunning, "active")
	staleConn := worker.conn
	replacementConn := newMockConn()
	d.mu.Lock()
	worker.conn = replacementConn
	d.mu.Unlock()

	d.connCloseCleanup(worker.id, staleConn)

	if got := trackedReleaseWorker(d, worker.id); got != worker {
		t.Fatalf("stale disconnect changed same-pointer worker: got %p, want %p", got, worker)
	} else if got.conn != replacementConn {
		t.Fatalf("stale disconnect changed replacement connection: got %p, want %p", got.conn, replacementConn)
	}
}

func seedCheckpointOwnedEdgeWorker(
	t *testing.T,
	d *Dispatcher,
	suffix string,
	phase ReviewCheckpointState,
	assignmentStatus string,
) (int64, int64, *trackedWorker) {
	t.Helper()
	beadID := "edge-bead-" + suffix
	workerID := "edge-worker-" + suffix
	store := NewReviewCheckpointStore(d.db)
	checkpointID, assignmentID := seedReviewCheckpointWorkerRelease(context.Background(), t, store, beadID, workerID, phase, assignmentStatus)
	worker := &trackedWorker{
		id:           workerID,
		beadID:       beadID,
		assignmentID: assignmentID,
		worktree:     "/tmp/" + beadID,
		conn:         newMockConn(),
		state:        protocol.WorkerReviewing,
	}
	d.mu.Lock()
	d.workers[workerID] = worker
	d.mu.Unlock()
	return checkpointID, assignmentID, worker
}

func assertCheckpointOwnedEdgeReleased(
	t *testing.T,
	d *Dispatcher,
	checkpointID, assignmentID int64,
	wantPhase ReviewCheckpointState,
) {
	t.Helper()
	var checkpointWorker, checkpointPhase, assignmentStatus string
	if err := d.db.QueryRow(`SELECT COALESCE(worker_id, ''), state FROM review_checkpoints WHERE id=?`, checkpointID).
		Scan(&checkpointWorker, &checkpointPhase); err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("load assignment: %v", err)
	}
	if checkpointWorker != "" || checkpointPhase != string(wantPhase) || assignmentStatus != "requeued" {
		t.Fatalf("released durable state = worker %q phase %q assignment %q, want empty/%q/requeued",
			checkpointWorker, checkpointPhase, assignmentStatus, wantPhase)
	}
}
