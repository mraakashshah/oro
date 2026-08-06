package dispatcher //nolint:testpackage // white-box lifecycle edge assertions

import (
	"strings"
	"testing"

	"oro/pkg/protocol"
)

func TestReviewWorkerDirectivesDurablyReleaseCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause ReviewReleaseCause
		apply func(*Dispatcher, string) (string, error)
	}{
		{name: "kill", cause: ReviewReleaseCauseKilled, apply: (*Dispatcher).applyKillWorker},
		{name: "restart", cause: ReviewReleaseCauseRestarted, apply: (*Dispatcher).applyRestartWorker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, beadSrc, _, _, _, _ := newTestDispatcher(t)
			pm := &mockProcessManager{}
			d.procMgr = pm
			d.targetWorkers = 3
			checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(t, d, "directive-"+tc.name, ReviewCheckpointStateCorrectionAssigned, "active")
			worker.managed = true
			drainCheckpointReleaseWakes(d)

			detail, err := tc.apply(d, worker.id)

			if err != nil {
				t.Fatalf("%s review worker: %v", tc.name, err)
			}
			if !strings.Contains(detail, worker.id) {
				t.Fatalf("directive detail = %q, want worker ID", detail)
			}
			assertCheckpointOwnedEdgeReleased(t, d, checkpointID, assignmentID, ReviewCheckpointStateCorrectionAssigned)
			if got := trackedReleaseWorker(d, worker.id); got != nil {
				t.Fatalf("released directive worker remains tracked: %p", got)
			}
			assertCheckpointReleaseEvent(t, d, worker.beadID, worker.id, tc.cause)
			assertOneCheckpointReleaseWake(t, d)
			beadSrc.mu.Lock()
			updated := beadSrc.updated[worker.beadID]
			beadSrc.mu.Unlock()
			if updated != "" {
				t.Fatalf("checkpoint-owned bead status changed to %q", updated)
			}

			switch tc.name {
			case "kill":
				if d.targetWorkers != 2 {
					t.Fatalf("targetWorkers = %d, want 2 after managed kill", d.targetWorkers)
				}
				if got := pm.SpawnedIDs(); len(got) != 0 {
					t.Fatalf("kill spawned workers: %v", got)
				}
			case "restart":
				if d.targetWorkers != 3 {
					t.Fatalf("targetWorkers = %d, want unchanged 3 after restart", d.targetWorkers)
				}
				d.mu.Lock()
				pending := d.pendingManagedIDs[worker.id]
				_, hasSince := d.pendingManagedSince[worker.id]
				d.mu.Unlock()
				if !pending || !hasSince {
					t.Fatalf("restart managed bookkeeping = pending %t since %t, want true/true", pending, hasSince)
				}
				if got := pm.KilledIDs(); len(got) != 1 || got[0] != worker.id {
					t.Fatalf("restart killed IDs = %v, want [%s]", got, worker.id)
				}
				if got := pm.SpawnedIDs(); len(got) != 1 || got[0] != worker.id {
					t.Fatalf("restart spawned IDs = %v, want [%s]", got, worker.id)
				}
			}
		})
	}
}

func TestReviewWorkerDirectiveReleaseFailureDoesNotFallBack(t *testing.T) {
	for _, directive := range []struct {
		name  string
		apply func(*Dispatcher, string) (string, error)
	}{
		{name: "kill", apply: (*Dispatcher).applyKillWorker},
		{name: "restart", apply: (*Dispatcher).applyRestartWorker},
	} {
		for _, release := range []struct {
			name             string
			assignmentStatus string
			terminal         bool
		}{
			{name: "conflict", assignmentStatus: "requeued"},
			{name: "no-op", assignmentStatus: "active", terminal: true},
		} {
			t.Run(directive.name+"/"+release.name, func(t *testing.T) {
				d, beadSrc, _, _, _, _ := newTestDispatcher(t)
				pm := &mockProcessManager{}
				d.procMgr = pm
				d.targetWorkers = 3
				checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(t, d, "directive-fail-"+directive.name+"-"+release.name, ReviewCheckpointStateReviewRunning, release.assignmentStatus)
				worker.managed = true
				if release.terminal {
					mustExec(t, d, `UPDATE review_checkpoints SET state='integrated' WHERE id=?`, checkpointID)
				}
				drainCheckpointReleaseWakes(d)

				_, err := directive.apply(d, worker.id)

				if err == nil {
					t.Fatal("directive error = nil, want durable release failure")
				}
				if got := trackedReleaseWorker(d, worker.id); got != worker {
					t.Fatalf("failed release changed worker: got %p, want %p", got, worker)
				}
				if d.targetWorkers != 3 {
					t.Fatalf("failed release changed targetWorkers to %d", d.targetWorkers)
				}
				d.mu.Lock()
				pending := d.pendingManagedIDs[worker.id]
				d.mu.Unlock()
				if pending || len(pm.KilledIDs()) != 0 || len(pm.SpawnedIDs()) != 0 {
					t.Fatalf("failed release entered managed fallback: pending=%t killed=%v spawned=%v", pending, pm.KilledIDs(), pm.SpawnedIDs())
				}
				var checkpointWorker, assignmentStatus string
				if err := d.db.QueryRow(`SELECT COALESCE(worker_id, '') FROM review_checkpoints WHERE id=?`, checkpointID).Scan(&checkpointWorker); err != nil {
					t.Fatalf("load checkpoint worker: %v", err)
				}
				if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
					t.Fatalf("load assignment: %v", err)
				}
				if checkpointWorker != worker.id || assignmentStatus != release.assignmentStatus {
					t.Fatalf("durable state changed: worker/status=%q/%q, want %q/%q", checkpointWorker, assignmentStatus, worker.id, release.assignmentStatus)
				}
				beadSrc.mu.Lock()
				updated := beadSrc.updated[worker.beadID]
				beadSrc.mu.Unlock()
				if updated != "" {
					t.Fatalf("failed release changed bead status to %q", updated)
				}
				assertCheckpointReleaseEventCount(t, d, 0)
				assertNoCheckpointReleaseWake(t, d)
			})
		}
	}
}

func TestOrdinaryWorkerDirectivesRetainExistingBehavior(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*Dispatcher, string) (string, error)
	}{
		{name: "kill", apply: (*Dispatcher).applyKillWorker},
		{name: "restart", apply: (*Dispatcher).applyRestartWorker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, _, _, _, _ := newTestDispatcher(t)
			worker := &trackedWorker{id: "ordinary-" + tc.name, conn: newMockConn(), state: protocol.WorkerIdle}
			d.mu.Lock()
			d.workers[worker.id] = worker
			d.mu.Unlock()

			if _, err := tc.apply(d, worker.id); err != nil {
				t.Fatalf("ordinary %s: %v", tc.name, err)
			}
			if got := trackedReleaseWorker(d, worker.id); got != nil {
				t.Fatalf("ordinary %s worker remains tracked: %p", tc.name, got)
			}
		})
	}
}
