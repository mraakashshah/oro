package dispatcher //nolint:testpackage

import (
	"context"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestWorkerRespawn_PreservesUncommittedChanges verifies that when a worker
// is killed or times out and then a new worker picks up the same bead,
// uncommitted changes in the worktree are preserved by reusing the existing
// worktree instead of creating a new one.
func TestWorkerRespawn_PreservesUncommittedChanges(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	d.cfg.HeartbeatTimeout = 100 * time.Millisecond

	beadID := "oro-respawn-test"

	// Track worktree Create calls to verify reuse.
	var worktreeCreates []string
	var firstWorktreePath string
	wt.createFn = func(ctx context.Context, bID string) (string, string, error) {
		worktreeCreates = append(worktreeCreates, bID)
		path := "/tmp/worktree-" + bID
		if firstWorktreePath == "" {
			firstWorktreePath = path
		}
		return path, "agent/" + bID, nil
	}

	beads.SetBeads([]protocol.Bead{{ID: beadID, Priority: 2}})

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Step 1: Connect first worker and get assignment.
	conn1, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn1, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Worker receives ASSIGN message.
	msg1, ok := readMsg(t, conn1, 2*time.Second)
	if !ok || msg1.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %v", msg1)
	}
	if msg1.Assign.BeadID != beadID {
		t.Fatalf("assigned bead: got %s, want %s", msg1.Assign.BeadID, beadID)
	}

	// Verify worktree was created for first worker.
	if len(worktreeCreates) != 1 || worktreeCreates[0] != beadID {
		t.Fatalf("after first assignment: got %v creates, want [%s]", worktreeCreates, beadID)
	}

	// Remove beads from queue so second worker doesn't get a different bead.
	beads.SetBeads(nil)

	// Step 2: Simulate heartbeat timeout by not sending more heartbeats.
	waitFor(t, func() bool {
		return d.ConnectedWorkers() == 0
	}, 2*time.Second)

	// Step 3: Make the bead available again for reassignment (simulating bd ready returning it).
	beads.SetBeads([]protocol.Bead{{ID: beadID, Priority: 2}})

	// Connect second worker (respawn).
	conn2, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn2, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w2", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Worker receives ASSIGN message with the same bead.
	msg2, ok := readMsg(t, conn2, 2*time.Second)
	if !ok || msg2.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN for w2, got %v", msg2)
	}
	if msg2.Assign.BeadID != beadID {
		t.Fatalf("assigned bead for w2: got %s, want %s", msg2.Assign.BeadID, beadID)
	}

	// CRITICAL ASSERTION: Second worker should reuse existing worktree path,
	// so Create should NOT be called again.
	if len(worktreeCreates) != 1 {
		t.Fatalf("after respawn: got %d creates %v, want 1 (should reuse existing worktree)", len(worktreeCreates), worktreeCreates)
	}

	// Verify the worktree path sent to second worker matches first worker's path.
	if msg2.Assign.Worktree != firstWorktreePath {
		t.Fatalf("worktree path for w2: got %s, want %s (same as w1)", msg2.Assign.Worktree, firstWorktreePath)
	}
}
