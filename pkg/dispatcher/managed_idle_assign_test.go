package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestManagedIdleWorker_HeartbeatCycleAssignsReadyTask reproduces the bug where
// a managed idle worker does not pick up a ready bead after the assign loop's
// workerReadyCh signal has already been consumed (e.g. from initial registration
// with no beads available).  The heartbeat/check cycle must wake the assign loop
// when managed workers are idle and fewer than target workers are actively assigned.
//
// Bug: oro-ntr3 — checkHeartbeats only calls notifyAssignLoop when dead/stuck
// workers are removed, so a healthy idle managed worker never gets a newly-ready
// bead unless the poll ticker fires (up to 60 s in production).
func TestManagedIdleWorker_HeartbeatCycleAssignsReadyTask(t *testing.T) {
	t.Parallel()

	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	// Use SQLite mode + very long poll so the assign loop only wakes via
	// workerReadyCh.  This lets us prove checkHeartbeats (not the ticker)
	// triggers the assignment.
	d.beadSourceMode = "sqlite"
	d.cfg.PollInterval = 24 * time.Hour
	d.cfg.FallbackPollInterval = 24 * time.Hour

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Target 2 workers, 0 currently active — fewer active assignments than target.
	d.mu.Lock()
	d.targetWorkers = 2
	d.pendingManagedIDs["w-managed"] = true
	d.mu.Unlock()

	// Connect the managed worker with no ready beads — the initial tryAssign
	// (triggered by registerWorker via workerReadyCh) finds nothing to assign.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-managed", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Wait until the initial tryAssign has run (Ready() called at least once).
	// After it returns, the assign loop goroutine re-enters its select and the
	// workerReadyCh channel is empty again.
	waitFor(t, func() bool {
		beadSrc.mu.Lock()
		defer beadSrc.mu.Unlock()
		return beadSrc.readyCalled >= 1
	}, 2*time.Second)

	// Confirm: managed idle worker, 0 active assignments, target = 2.
	d.mu.Lock()
	w, workerExists := d.workers["w-managed"]
	isManaged := workerExists && w.managed
	isIdle := workerExists && w.state == protocol.WorkerIdle
	d.mu.Unlock()
	if !isManaged {
		t.Fatal("worker should be managed")
	}
	if !isIdle {
		t.Fatal("worker should be idle (no beads were ready during initial tryAssign)")
	}

	// Add a ready bead AFTER the initial tryAssign has already run.
	// With 24-hour poll intervals and no fsnotify events, the ONLY way the
	// assign loop can wake up is if checkHeartbeats calls notifyAssignLoop.
	beadSrc.mu.Lock()
	beadSrc.shown["bead-1"] = &protocol.BeadDetail{
		ID:                 "bead-1",
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
	}
	beadSrc.mu.Unlock()
	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-1", Title: "Task", Priority: 1}})

	// Trigger the heartbeat/check cycle.  With the fix, checkHeartbeats calls
	// notifyAssignLoop when managed workers are idle, waking the assign loop
	// immediately.  Without the fix, the worker would stay idle indefinitely
	// (until the 24-hour poll ticker fires).
	d.callCheckHeartbeats(context.Background())

	// Assert: the managed idle worker receives an assignment from the heartbeat cycle.
	msg, ok := readMsg(t, conn, 3*time.Second)
	if !ok {
		t.Fatal("managed idle worker: expected ASSIGN from heartbeat/check cycle, got timeout — checkHeartbeats must call notifyAssignLoop when managed workers are idle below target")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign == nil || msg.Assign.BeadID != "bead-1" {
		t.Fatalf("expected assignment of bead-1, got %+v", msg.Assign)
	}
}
