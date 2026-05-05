package dispatcher //nolint:testpackage // white-box: needs access to unexported dispatcher fields

import (
	"context"
	"encoding/json"
	"testing"

	"oro/pkg/protocol"
)

// TestCheckpointRespawn verifies §9.3 steps 7-9: after a valid checkpoint ack
// the dispatcher kills the old worker subprocess, spawns a fresh one with the
// same worktree, and preserves the bead's next_action for the incoming worker.
//
// Assert: new worker PID different; same worktree path; worker reads previous
// next_action; turn N+1 of bead lifetime.
func TestCheckpointRespawn(t *testing.T) {
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

// stringSliceContains reports whether s is in the slice.
func stringSliceContains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
