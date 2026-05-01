package dispatcher //nolint:testpackage

import (
	"context"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestSpawnFor_WorkerImmediatelyReceivesAssignment verifies that a worker
// spawned by the spawn-for directive receives an ASSIGN message without
// waiting for the background poll interval to fire.
//
// Bug: assignLoop only calls tryAssign on fsnotify events or poll ticks.
// A worker spawned by spawn-for connects and becomes idle, but no trigger
// fires tryAssign immediately — it waits until the next poll.
//
// Fix: registerWorker signals workerReadyCh when the worker becomes idle
// (no pending handoff). assignLoop/assignLoopPoll listen on this channel
// and call tryAssign immediately.
func TestSpawnFor_WorkerImmediatelyReceivesAssignment(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	// Use slow poll intervals so assignment can only happen via workerReadyCh.
	// Without the fix: worker sits idle for 10s before getting assigned.
	// With the fix: tryAssign fires immediately after registerWorker.
	d.cfg.PollInterval = 10 * time.Second
	d.cfg.FallbackPollInterval = 10 * time.Second

	beadID := "oro-spawnfor-test"
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: beadID, Priority: 2}})

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Send spawn-for directive: marks beadID as priority, spawns a worker.
	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "spawn-for", beadID)
	if !ack.OK {
		t.Fatalf("spawn-for directive failed: %s", ack.Detail)
	}

	// Wait for procMgr to record the spawned worker ID.
	waitFor(t, func() bool {
		return len(pm.SpawnedIDs()) > 0
	}, 1*time.Second)

	spawnedIDs := pm.SpawnedIDs()
	workerID := spawnedIDs[0]

	// Simulate the spawned worker connecting and sending its first heartbeat.
	// (Real workers send a heartbeat as their first message; extractWorkerID
	// pulls the ID from the heartbeat, then registerWorker is called.)
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: workerID, ContextPct: 5},
	})

	// ASSERTION: Worker receives ASSIGN within 3s.
	// Without the fix: must wait up to 10s for the poll to fire.
	// With the fix: triggered by workerReadyCh immediately after registerWorker.
	msg, ok := readMsg(t, conn, 3*time.Second)
	if !ok || msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN within 3s, got ok=%v type=%v", ok, msg.Type)
	}
	if msg.Assign == nil || msg.Assign.BeadID != beadID {
		t.Fatalf("expected bead %s assigned, got %v", beadID, msg.Assign)
	}

	// Verify worker is now busy with the target bead.
	st, bid, ok := d.WorkerInfo(workerID)
	if !ok || st != protocol.WorkerBusy || bid != beadID {
		t.Fatalf("expected worker=%s busy with bead=%s, got state=%s bead=%s ok=%v",
			workerID, beadID, st, bid, ok)
	}
}

func TestSpawnFor_TargetedWorkerDoesNotReceiveDifferentReadyBead(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	d.cfg.PollInterval = 10 * time.Second
	d.cfg.FallbackPollInterval = 10 * time.Second

	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: otherID, Priority: 0}})

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "spawn-for", requestedID)
	if !ack.OK {
		t.Fatalf("spawn-for directive failed: %s", ack.Detail)
	}
	waitFor(t, func() bool {
		return len(pm.SpawnedIDs()) > 0
	}, 1*time.Second)

	workerID := pm.SpawnedIDs()[0]
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: workerID, ContextPct: 5},
	})

	if msg, ok := readMsg(t, conn, 300*time.Millisecond); ok && msg.Type == protocol.MsgAssign {
		got := "<nil>"
		if msg.Assign != nil {
			got = msg.Assign.BeadID
		}
		t.Fatalf("targeted spawn-for worker was assigned %q, want no assignment until %q is ready", got, requestedID)
	}

	st, bid, ok := d.WorkerInfo(workerID)
	if !ok || st != protocol.WorkerIdle || bid != "" {
		t.Fatalf("expected targeted worker to remain idle, got state=%s bead=%q ok=%v", st, bid, ok)
	}
}

func TestSpawnFor_TargetedIdleWorkerDoesNotBlockAutoscaleForOtherReadyBead(t *testing.T) {
	d, beads, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.setState(StateRunning)

	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	beads.SetBeads([]protocol.Bead{{ID: otherID, Priority: 0}})

	d.mu.Lock()
	d.targetWorkers = 1
	d.workers["worker-spawnfor-test"] = &trackedWorker{
		id:           "worker-spawnfor-test",
		state:        protocol.WorkerIdle,
		managed:      true,
		targetBeadID: requestedID,
	}
	d.mu.Unlock()

	d.tryAssign(context.Background())

	waitFor(t, func() bool {
		return len(pm.SpawnedIDs()) == 1
	}, 1*time.Second)
}

func TestSpawnFor_TargetedWorkerGetsRequestedBeadNotFirstReady(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	d.cfg.PollInterval = 10 * time.Second
	d.cfg.FallbackPollInterval = 10 * time.Second

	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{
		{ID: otherID, Priority: 0},
		{ID: requestedID, Priority: 3},
	})

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "spawn-for", requestedID)
	if !ack.OK {
		t.Fatalf("spawn-for directive failed: %s", ack.Detail)
	}
	waitFor(t, func() bool {
		return len(pm.SpawnedIDs()) > 0
	}, 1*time.Second)

	workerID := pm.SpawnedIDs()[0]
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: workerID, ContextPct: 5},
	})

	msg, ok := readMsg(t, conn, 3*time.Second)
	if !ok || msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN within 3s, got ok=%v type=%v", ok, msg.Type)
	}
	if msg.Assign == nil || msg.Assign.BeadID != requestedID {
		t.Fatalf("expected bead %s assigned, got %v", requestedID, msg.Assign)
	}
}

// TestIdleWorker_PicksUpQueuedBeadImmediately verifies that a worker connecting
// while beads are queued receives an ASSIGN without waiting for the poll interval.
//
// This covers the general "scale-up idle worker" case: when a new worker
// connects (managed or unmanaged), it should immediately be assigned a bead
// if one is ready, rather than waiting for the next poll tick.
func TestIdleWorker_PicksUpQueuedBeadImmediately(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)

	// Slow polls: only workerReadyCh can trigger assignment within the test window.
	d.cfg.PollInterval = 10 * time.Second
	d.cfg.FallbackPollInterval = 10 * time.Second

	beadID := "oro-idle-pickup-test"
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: beadID, Priority: 2}})

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	workerID := "w-idle-pickup"
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: workerID, ContextPct: 5},
	})

	// ASSERTION: Worker receives ASSIGN within 3s (without waiting 10s for poll).
	msg, ok := readMsg(t, conn, 3*time.Second)
	if !ok || msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN within 3s, got ok=%v type=%v", ok, msg.Type)
	}
	if msg.Assign == nil || msg.Assign.BeadID != beadID {
		t.Fatalf("expected bead %s assigned, got %v", beadID, msg.Assign)
	}
}
