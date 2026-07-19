package dispatcher //nolint:testpackage // white-box: needs access to unexported dispatcher fields

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"oro/pkg/testutil/qgserial"

	"oro/pkg/protocol"
)

// TestCheckpointRespawn verifies §9.3 steps 7-9: after a valid checkpoint ack
// the dispatcher kills the old worker subprocess, spawns a fresh one with the
// same worktree, and preserves the bead's next_action for the incoming worker.
//
// Assert: new worker PID different; same worktree path; worker reads previous
// next_action; turn N+1 of bead lifetime.
func TestCheckpointRespawn(t *testing.T) {
	qgserial.RequireSerial(t)
	ctx := context.Background()

	d, store := makeCheckpointDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	const (
		beadID        = "bead-cr-respawn"
		workerID      = "w-cr-1"
		worktreePath  = "/tmp/wt-bead-cr-respawn"
		intentSummary = "continue implementing checkpoint respawn"
	)

	// Pre-seed: worker w-cr-1 is busy on beadID at worktreePath (§9.3 pre-condition).
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		state:    protocol.WorkerBusy,
		beadID:   beadID,
		worktree: worktreePath,
		model:    protocol.ModelSonnet,
		lastSeen: d.nowFunc(),
	}
	d.worktreeByBead[beadID] = worktreePath
	d.mu.Unlock()

	// Trigger checkpoint — simulates context threshold crossing.
	d.triggerCheckpoint(ctx, beadID, workerID, 80)

	cs := d.checkpoints.get(beadID)
	if cs == nil {
		t.Fatal("expected active checkpoint after triggerCheckpoint")
	}
	cpID := cs.checkpointID

	// Worker sends checkpoint_ack with intent_summary (§9.3 step 3).
	d.handleCheckpointAck(ctx, workerID, protocol.Message{
		Type: protocol.MsgCheckpointAck,
		CheckpointAck: &protocol.CheckpointAckPayload{
			BeadID:        beadID,
			CheckpointID:  cpID,
			IntentSummary: intentSummary,
		},
	})

	// 1. New worker PID different: old worker killed, new spawned (§9.3 step 6+8).
	killed := pm.KilledIDs()
	if !stringSliceContains(killed, workerID) {
		t.Fatalf("old worker %q must be killed after checkpoint ack; killed=%v", workerID, killed)
	}
	spawned := pm.SpawnedIDs()
	if len(spawned) == 0 {
		t.Fatalf("a new worker must be spawned after checkpoint ack; spawned=%v", spawned)
	}
	newWorkerID := spawned[0]
	if newWorkerID == workerID {
		t.Fatalf("new worker ID must differ from old %q; got same ID", workerID)
	}

	// 2. Same worktree path preserved (§9.3 step 8).
	d.mu.Lock()
	actualWorktree := d.worktreeByBead[beadID]
	d.mu.Unlock()
	if actualWorktree != worktreePath {
		t.Fatalf("worktreeByBead[%q]: got %q, want %q (path must be preserved across respawn)", beadID, actualWorktree, worktreePath)
	}

	// 3. Worker reads previous next_action: handoff carries intent_summary (§9.3 step 8).
	d.mu.Lock()
	handoff, hasHandoff := d.pendingHandoffs[beadID]
	d.mu.Unlock()
	if !hasHandoff {
		t.Fatalf("pending handoff for %q expected after checkpoint respawn", beadID)
	}
	if handoff.nextAction != intentSummary {
		t.Fatalf("handoff.nextAction: got %q, want %q", handoff.nextAction, intentSummary)
	}

	// 4. Turn N+1 of bead lifetime: checkpointCounts incremented (§9.3 step 9).
	d.mu.Lock()
	cpCount := d.checkpointCounts[beadID]
	d.mu.Unlock()
	if cpCount != 1 {
		t.Fatalf("checkpointCounts[%q]: got %d, want 1 (first checkpoint = turn N+1)", beadID, cpCount)
	}

	// Journal must record checkpoint_acked with intent_summary.
	journalEvts := store.capturedFor(beadID)
	var foundAck bool
	for _, e := range journalEvts {
		if e.Event == "checkpoint_acked" && containsStr(e.Payload, intentSummary) {
			foundAck = true
			break
		}
	}
	if !foundAck {
		t.Fatalf("journal must have checkpoint_acked with intent_summary %q; events=%v", intentSummary, journalEvts)
	}

	// 5. Spawned worker must be routed back to *this* bead's handoff
	// (§9.3 step 8). With multiple pending handoffs the new worker would
	// otherwise pick one at random — pendingWorkerTargets pins it.
	d.mu.Lock()
	target, hasTarget := d.pendingWorkerTargets[newWorkerID]
	d.mu.Unlock()
	if !hasTarget {
		t.Fatalf("pendingWorkerTargets[%q] missing — checkpoint respawn must route the spawned worker to this bead", newWorkerID)
	}
	if target != beadID {
		t.Fatalf("pendingWorkerTargets[%q]: got %q, want %q", newWorkerID, target, beadID)
	}

	// 6. Bead status must remain in_progress between respawn and
	// reassignment — the respawn flow must not flip it to "open" (which
	// would let an unrelated worker pick it up via bd-ready).
	store.fakeBeadStore.mu.Lock()
	status, wasUpdated := store.updated[beadID]
	store.fakeBeadStore.mu.Unlock()
	if wasUpdated && status == "open" {
		t.Fatalf("bead %q status was flipped to %q during checkpoint respawn; status must stay in_progress", beadID, status)
	}
}

// TestCheckpointRespawn_ASSIGN_carries_next_action verifies that the ASSIGN
// delivered to the new worker includes the intent_summary as Feedback and
// Attempt == checkpointTurn (turn N+1 of bead lifetime).
func TestCheckpointRespawn_ASSIGN_carries_next_action(t *testing.T) {
	ctx := context.Background()

	d, _ := makeCheckpointDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	const (
		beadID        = "bead-assign-cr"
		workerID      = "w-assign-cr-1"
		worktreePath  = "/tmp/wt-bead-assign-cr"
		intentSummary = "implement the remaining edge cases"
	)

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		state:    protocol.WorkerBusy,
		beadID:   beadID,
		worktree: worktreePath,
		model:    protocol.ModelSonnet,
		lastSeen: d.nowFunc(),
	}
	d.worktreeByBead[beadID] = worktreePath
	d.mu.Unlock()

	d.triggerCheckpoint(ctx, beadID, workerID, 80)
	cpID := d.checkpoints.get(beadID).checkpointID

	d.handleCheckpointAck(ctx, workerID, protocol.Message{
		Type: protocol.MsgCheckpointAck,
		CheckpointAck: &protocol.CheckpointAckPayload{
			BeadID:        beadID,
			CheckpointID:  cpID,
			IntentSummary: intentSummary,
		},
	})

	// Verify handoff has nextAction and checkpointTurn=1.
	d.mu.Lock()
	h, ok := d.pendingHandoffs[beadID]
	d.mu.Unlock()
	if !ok {
		t.Fatal("pending handoff must exist after checkpoint respawn")
	}
	if h.nextAction != intentSummary {
		t.Fatalf("handoff.nextAction: got %q, want %q", h.nextAction, intentSummary)
	}
	if h.checkpointTurn != 1 {
		t.Fatalf("handoff.checkpointTurn: got %d, want 1", h.checkpointTurn)
	}

	// Simulate new worker connecting and receiving the handoff via assignHandoffToWorker.
	mockC := newMockConn()
	newWorkerID := "w-assign-cr-2"
	d.mu.Lock()
	d.workers[newWorkerID] = &trackedWorker{
		id:      newWorkerID,
		state:   protocol.WorkerIdle,
		conn:    mockC,
		encoder: json.NewEncoder(mockC),
	}
	d.mu.Unlock()

	d.assignPendingHandoffsToIdleWorkers()

	// Decode the ASSIGN message written to the mock conn.
	mockC.mu.Lock()
	written := mockC.written
	mockC.mu.Unlock()

	var assign *protocol.AssignPayload
	for _, raw := range written {
		var msg protocol.Message
		if err := json.Unmarshal(raw, &msg); err == nil && msg.Type == protocol.MsgAssign {
			assign = msg.Assign
			break
		}
	}
	if assign == nil {
		t.Fatalf("new worker must receive ASSIGN after checkpoint respawn handoff; written=%d msgs", len(written))
	}

	// Same worktree path.
	if assign.Worktree != worktreePath {
		t.Fatalf("ASSIGN.Worktree: got %q, want %q", assign.Worktree, worktreePath)
	}

	// Carries previous next_action as Feedback.
	if !containsStr(assign.Feedback, intentSummary) {
		t.Fatalf("ASSIGN.Feedback must contain intent_summary %q; got %q", intentSummary, assign.Feedback)
	}

	// Attempt = checkpointTurn (turn N+1).
	if assign.Attempt != 1 {
		t.Fatalf("ASSIGN.Attempt: got %d, want 1 (first checkpoint respawn)", assign.Attempt)
	}
}

// TestCheckpointRespawn_ConnCloseDoesNotWipeState is a regression test for a
// race in respawnAfterCheckpoint: when the dispatcher kills the old worker
// subprocess, that subprocess closes its UDS socket. handleConn's deferred
// connCloseCleanup then runs and — if the worker is still attached to the bead
// — calls clearBeadTracking(beadID) and updateBeadStatus(beadID, "open"),
// wiping the freshly-set pendingHandoffs and checkpointCounts entries. The
// just-spawned new worker would then connect with no handoff to consume.
//
// The fix detaches the worker from its bead (state, beadID, assignmentID,
// epicID) before procMgr.Kill, so connCloseCleanup observes beadID == "" and
// skips the bead-tracking cleanup. This mirrors shutdownWorkerForHandoff which
// already defends the ralph-handoff path.
func TestCheckpointRespawn_ConnCloseDoesNotWipeState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d, store := makeCheckpointDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	const (
		beadID        = "bead-cr-conn-race"
		workerID      = "w-cr-conn-1"
		worktreePath  = "/tmp/wt-bead-cr-conn-race"
		intentSummary = "preserve respawn state across conn close"
	)

	// Wire the worker through handleConn so the deferred connCloseCleanup
	// fires when the client side closes — exactly what happens when
	// procMgr.Kill terminates the real subprocess in production.
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	handleConnDone := make(chan struct{})
	go func() {
		defer close(handleConnDone)
		d.handleConn(ctx, server)
	}()

	// Register the worker with the dispatcher via heartbeat so handleConn
	// owns the connection and will drive cleanup on close.
	sendMsg(t, client, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   workerID,
			ContextPct: 5,
		},
	})
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, ok := d.workers[workerID]
		return ok
	}, 2*time.Second)

	// Promote the registered worker to busy on beadID with a known worktree.
	d.mu.Lock()
	w := d.workers[workerID]
	w.state = protocol.WorkerBusy
	w.beadID = beadID
	w.worktree = worktreePath
	w.model = protocol.ModelSonnet
	d.worktreeByBead[beadID] = worktreePath
	d.mu.Unlock()

	d.triggerCheckpoint(ctx, beadID, workerID, 80)
	cs := d.checkpoints.get(beadID)
	if cs == nil {
		t.Fatal("expected active checkpoint after triggerCheckpoint")
	}
	cpID := cs.checkpointID

	// Send the checkpoint ack via the same connection so handleMessage
	// drives the full handleCheckpointAck → respawnAfterCheckpoint path.
	sendMsg(t, client, protocol.Message{
		Type: protocol.MsgCheckpointAck,
		CheckpointAck: &protocol.CheckpointAckPayload{
			BeadID:        beadID,
			CheckpointID:  cpID,
			IntentSummary: intentSummary,
		},
	})

	// Wait for respawn to complete: handoff registered, new worker spawned.
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, hasHandoff := d.pendingHandoffs[beadID]
		return hasHandoff && len(pm.SpawnedIDs()) > 0
	}, 2*time.Second)

	// In production procMgr.Kill terminates the subprocess and its socket
	// closes. Mock Kill is a no-op, so close the client side ourselves to
	// drive the deferred cleanup that happens regardless in production.
	_ = client.Close()
	select {
	case <-handleConnDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handleConn did not exit after pipe close")
	}
	// connCloseCleanup runs in handleConn's defer — wait for the worker to
	// disappear from the map before asserting.
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, ok := d.workers[workerID]
		return !ok
	}, 2*time.Second)

	// The respawn state must SURVIVE the conn close. Without the detach-
	// before-kill fix, connCloseCleanup observes the still-attached beadID
	// and wipes pendingHandoffs/checkpointCounts via clearBeadTracking.
	d.mu.Lock()
	handoff, hasHandoff := d.pendingHandoffs[beadID]
	cpCount := d.checkpointCounts[beadID]
	d.mu.Unlock()
	if !hasHandoff {
		t.Fatalf("pendingHandoffs[%q] was wiped by connCloseCleanup — respawnAfterCheckpoint must detach worker.beadID before procMgr.Kill", beadID)
	}
	if handoff.nextAction != intentSummary {
		t.Fatalf("handoff.nextAction: got %q, want %q (must survive conn close)", handoff.nextAction, intentSummary)
	}
	if cpCount != 1 {
		t.Fatalf("checkpointCounts[%q]: got %d, want 1 (must survive conn close)", beadID, cpCount)
	}

	// Bead status must NOT be flipped to "open" — that would let an
	// unrelated worker pick the bead up via bd-ready before the spawned
	// respawn worker connects, producing a duplicate assignment.
	store.fakeBeadStore.mu.Lock()
	status, wasUpdated := store.updated[beadID]
	store.fakeBeadStore.mu.Unlock()
	if wasUpdated && status == "open" {
		t.Fatalf("bead %q status flipped to %q during conn close; respawn must keep it in_progress", beadID, status)
	}

	// The spawned worker must be pinned to this bead's handoff via
	// pendingWorkerTargets so it cannot consume a different pending handoff
	// when it eventually connects.
	spawned := pm.SpawnedIDs()
	if len(spawned) == 0 {
		t.Fatal("checkpoint respawn must spawn a new worker")
	}
	newID := spawned[0]
	d.mu.Lock()
	target := d.pendingWorkerTargets[newID]
	d.mu.Unlock()
	if target != beadID {
		t.Fatalf("pendingWorkerTargets[%q]: got %q, want %q", newID, target, beadID)
	}
}

// TestCheckpointRespawn_HonorsMaxWorkers verifies that enqueueCheckpointHandoff
// respects the MaxWorkers cap. The killed worker is still in d.workers until
// connCloseCleanup runs, so liveWorkerCountLocked still counts it — without
// the cap, a saturated dispatcher would spawn past the limit on every
// checkpoint respawn.
func TestCheckpointRespawn_HonorsMaxWorkers(t *testing.T) {
	ctx := context.Background()

	d, _ := makeCheckpointDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.cfg.MaxWorkers = 1

	const (
		beadID        = "bead-cr-cap"
		workerID      = "w-cr-cap-1"
		worktreePath  = "/tmp/wt-bead-cr-cap"
		intentSummary = "respect max workers"
	)

	// Single worker saturates the pool at MaxWorkers=1.
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		state:    protocol.WorkerBusy,
		beadID:   beadID,
		worktree: worktreePath,
		model:    protocol.ModelSonnet,
		lastSeen: d.nowFunc(),
	}
	d.worktreeByBead[beadID] = worktreePath
	d.mu.Unlock()

	d.triggerCheckpoint(ctx, beadID, workerID, 80)
	cpID := d.checkpoints.get(beadID).checkpointID

	d.handleCheckpointAck(ctx, workerID, protocol.Message{
		Type: protocol.MsgCheckpointAck,
		CheckpointAck: &protocol.CheckpointAckPayload{
			BeadID:        beadID,
			CheckpointID:  cpID,
			IntentSummary: intentSummary,
		},
	})

	// Handoff must be queued for an idle worker to pick up later, but the
	// dispatcher must NOT spawn a new managed worker because the pool is
	// already at MaxWorkers (the killed worker is still in d.workers).
	d.mu.Lock()
	_, hasHandoff := d.pendingHandoffs[beadID]
	d.mu.Unlock()
	if !hasHandoff {
		t.Fatal("pending handoff must be queued even when MaxWorkers prevents spawning")
	}
	if spawned := pm.SpawnedIDs(); len(spawned) != 0 {
		t.Fatalf("checkpoint respawn must honor MaxWorkers; spawned=%v", spawned)
	}
}

// stringSliceContains reports whether s is in the slice.
func stringSliceContains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
