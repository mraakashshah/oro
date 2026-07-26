package dispatcher //nolint:testpackage

import (
	"context"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestAssignBead_StaleWorktreeByBead_CreatesNewWorktree verifies that when
// worktreeByBead has a stale entry pointing to a path that no longer exists,
// assignBead discards the stale entry and creates a new worktree instead of
// reusing the non-existent path.
func TestAssignBead_StaleWorktreeByBead_CreatesNewWorktree(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	d.cfg.HeartbeatTimeout = 100 * time.Millisecond

	beadID := "oro-stale-wt-test"

	// Track Create calls to verify a new worktree is created.
	var createCalls []string
	newWorktreePath := "/tmp/worktree-new-" + beadID
	wt.createFn = func(ctx context.Context, bID, _ string) (string, string, error) {
		createCalls = append(createCalls, bID)
		return newWorktreePath, "agent/" + bID, nil
	}

	// Simulate a worktree path that no longer exists on disk.
	wt.existsFn = func(_ context.Context, path string) bool {
		return false // all paths are "missing" — forces new worktree creation
	}

	// Pre-seed a stale worktreeByBead entry left by a previous crashed worker.
	stalePath := "/stale/nonexistent/path-" + beadID
	d.mu.Lock()
	d.worktreeByBead[beadID] = stalePath
	d.mu.Unlock()

	beads.SetBeads([]protocol.Bead{{ID: beadID, Priority: 2}})

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Connect worker and send heartbeat.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Worker receives ASSIGN message.
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok || msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %v (ok=%v)", msg, ok)
	}
	if msg.Assign.BeadID != beadID {
		t.Fatalf("assigned bead: got %q, want %q", msg.Assign.BeadID, beadID)
	}

	// CRITICAL: stale entry must be discarded — Create must be called once.
	if len(createCalls) != 1 {
		t.Fatalf("Create calls: got %d %v, want 1 (stale path should be replaced)", len(createCalls), createCalls)
	}

	// Assigned worktree must be the newly created path, not the stale one.
	if msg.Assign.Worktree == stalePath {
		t.Fatalf("worktree is stale path %q — dispatcher did not create a new worktree", stalePath)
	}
	if msg.Assign.Worktree != newWorktreePath {
		t.Fatalf("worktree: got %q, want %q", msg.Assign.Worktree, newWorktreePath)
	}
}

// TestWorkerRespawn_PreservesUncommittedChanges verifies that when a worker
// is killed or times out and then a new worker picks up the same bead,
// uncommitted changes in the worktree are preserved by reusing the existing
// worktree instead of creating a new one.
func TestWorkerRespawn_PreservesUncommittedChanges(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	d.cfg.HeartbeatTimeout = 500 * time.Millisecond
	// The mock worktree path is synthetic. Keep recovery inspection synthetic
	// too so this test exercises the existing reusable-worktree path rather
	// than failing closed on a real Git command against a nonexistent path.
	d.shutdownRunner = &mockCommandRunner{}

	beadID := "oro-respawn-test"

	// Track worktree Create calls to verify reuse.
	var worktreeCreates []string
	var firstWorktreePath string
	wt.createFn = func(ctx context.Context, bID, _ string) (string, string, error) {
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
