package dispatcher //nolint:testpackage // internal white-box test reaches into d.workers/d.pendingManagedIDs

import (
	"errors"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestReadyForReview_OpsSubprocessDies_BeadResetToOpen reproduces the
// runtime symptom recorded in oro-zs3z: a managed worker emits
// READY_FOR_REVIEW, the ops review subprocess silently dies (mockBatchSpawner
// configured to fail to spawn → ops.Spawner sends VerdictFailed and never
// adds the agent to its active map, so HasActiveForBead returns false), and
// the worker is then recycled by the dispatcher. The fix from oro-9nfh's
// follow-up (reviewDeadStateLocked) must remove the worker via
// checkHeartbeats AND reset the bead's status to "open" so it can be
// reassigned. Before the fix, the bead would stay in_progress with no
// active assignment (the zombie symptom).
//
// This is a true lifecycle test — the worker is registered through the
// real UDS path with the managed flag, and we drive READY_FOR_REVIEW over
// the wire rather than poking d.workers directly. The only direct field
// access is to seed pendingManagedIDs (which is how the dispatcher itself
// marks its own spawned workers).
func TestReadyForReview_OpsSubprocessDies_BeadResetToOpen(t *testing.T) {
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
	// The mock worktree path is synthetic. Model a clean branch with no
	// preserved commits so this test remains focused on dead-review cleanup.
	d.shutdownRunner = &mockCommandRunner{}

	// Make ops review's underlying subprocess "fail to spawn." The ops.Spawner
	// then sends VerdictFailed without ever registering the agent in its
	// active map, so HasActiveForBead returns false from the moment the
	// review is triggered.
	spawnMock.mu.Lock()
	spawnMock.spawnErr = errors.New("simulated subprocess died")
	spawnMock.mu.Unlock()

	// Tight grace so the heartbeat loop classifies the worker dead quickly.
	d.cfg.ReviewDeadGrace = 50 * time.Millisecond

	// procMgr returning IsAlive=true ensures the worker is NOT removed by the
	// existing dead-process check — only the new dead-review check should fire.
	pm := &mockProcessManager{}
	d.procMgr = pm

	const workerID = "w-recycle-zombie"
	const beadID = "bead-recycle-zombie"

	// Pre-mark the worker as managed (the dispatcher does this for IDs it
	// spawns; we mimic that here without a real spawn).
	d.mu.Lock()
	d.pendingManagedIDs[workerID] = true
	d.mu.Unlock()

	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: workerID, ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Keep the worker liveness fresh for the duration of the test so the
	// existing heartbeat-timeout path does not fire — otherwise we cannot
	// attribute the bead reset to the dead-review path under test.
	stopHB := make(chan struct{})
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopHB:
				return
			case <-ticker.C:
				d.mu.Lock()
				if w := d.workers[workerID]; w != nil {
					w.lastSeen = d.nowFunc()
				}
				d.mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() {
		close(stopHB)
		<-hbDone
	})

	// Confirm the worker is registered as managed (lifecycle precondition for
	// the reviewDeadGraceExpired check).
	d.mu.Lock()
	w, ok := d.workers[workerID]
	managed := ok && w.managed
	d.mu.Unlock()
	if !managed {
		t.Fatalf("expected worker %s to be registered as managed, ok=%v", workerID, ok)
	}

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]protocol.Bead{{ID: beadID, Title: "Recycle test", Priority: 1}})
	msg, gotMsg := readMsg(t, conn, 2*time.Second)
	if !gotMsg {
		t.Fatal("expected ASSIGN")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	beadSrc.SetBeads(nil)

	// Worker emits READY_FOR_REVIEW. The dispatcher transitions it to
	// WorkerReviewing and asynchronously kicks off ops review, which fails
	// immediately because the underlying spawner returns spawnErr.
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: beadID, WorkerID: workerID},
	})
	waitForWorkerState(t, d, workerID, protocol.WorkerReviewing, 1*time.Second)

	// Heartbeat loop runs every HeartbeatTimeout/3 = ~166ms. Combined with
	// the 50ms grace, a few cycles must classify the worker as dead.
	waitFor(t, func() bool {
		d.mu.Lock()
		_, present := d.workers[workerID]
		d.mu.Unlock()
		return !present
	}, 5*time.Second)

	// The bead must be reset to "open" so it can be reassigned. Without the
	// fix, the bead stayed in_progress with no active assignment.
	waitFor(t, func() bool {
		beadSrc.mu.Lock()
		defer beadSrc.mu.Unlock()
		return beadSrc.updated[beadID] == "open"
	}, 2*time.Second)
}
