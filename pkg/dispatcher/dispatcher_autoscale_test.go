package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestAutoScale verifies that when tryAssign finds assignable beads and
// there are no idle workers, the dispatcher automatically increases
// targetWorkers up to MaxWorkers and calls reconcileScale to spawn workers.
func TestAutoScale(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	// Set MaxWorkers to 3 for this test
	d.cfg.MaxWorkers = 3

	// Set initial targetWorkers to 1 (will scale up from here)
	d.mu.Lock()
	d.targetWorkers = 1
	d.mu.Unlock()

	startDispatcher(t, d)

	// Start the dispatcher
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Create 3 assignable beads
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-1", Title: "Task 1", Priority: 1, Type: "task"},
		{ID: "bead-2", Title: "Task 2", Priority: 1, Type: "task"},
		{ID: "bead-3", Title: "Task 3", Priority: 1, Type: "task"},
	})

	// Add acceptance criteria for all beads (required for assignment)
	beadSrc.shown["bead-1"] = &protocol.BeadDetail{
		ID:                 "bead-1",
		Title:              "Task 1",
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
	}
	beadSrc.shown["bead-2"] = &protocol.BeadDetail{
		ID:                 "bead-2",
		Title:              "Task 2",
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
	}
	beadSrc.shown["bead-3"] = &protocol.BeadDetail{
		ID:                 "bead-3",
		Title:              "Task 3",
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
	}

	// Connect one worker and make it busy (non-idle)
	conn1, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn1, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "worker-1",
			ContextPct: 10,
		},
	})

	waitForWorkers(t, d, 1, 1*time.Second)

	// Worker receives assignment (becomes busy)
	msg, ok := readMsg(t, conn1, 2*time.Second)
	if !ok || msg.Type != protocol.MsgAssign {
		t.Fatal("expected worker-1 to receive assignment")
	}

	// Send STATUS to transition worker to busy state
	sendMsg(t, conn1, protocol.Message{
		Type: protocol.MsgStatus,
		Status: &protocol.StatusPayload{
			State: string(protocol.WorkerBusy),
		},
	})

	// Wait a bit for auto-scale logic to kick in
	time.Sleep(200 * time.Millisecond)

	// Verify that targetWorkers was increased from initial value of 1
	d.mu.Lock()
	target := d.targetWorkers
	d.mu.Unlock()

	// Auto-scale should have increased targetWorkers because:
	// - We have 3 assignable beads
	// - We have 0 idle workers (worker-1 is busy)
	// - Current targetWorkers (1) < MaxWorkers (3)
	// Expected: targetWorkers should be at least 2 (to handle remaining beads)
	if target <= 1 {
		t.Errorf("expected targetWorkers to auto-scale from 1 to at least 2, got %d", target)
	}

	if target > d.cfg.MaxWorkers {
		t.Errorf("targetWorkers (%d) should not exceed MaxWorkers (%d)", target, d.cfg.MaxWorkers)
	}
}

// TestAutoScaleRespectsMax verifies that auto-scaling never exceeds the
// configured MaxWorkers limit, even when more assignable beads exist.
func TestAutoScaleRespectsMax(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	// Set MaxWorkers to 2 for this test
	d.cfg.MaxWorkers = 2

	// Set initial targetWorkers to 1 (will attempt to scale up)
	d.mu.Lock()
	d.targetWorkers = 1
	d.mu.Unlock()

	startDispatcher(t, d)

	// Start the dispatcher
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Create 5 assignable beads (more than MaxWorkers)
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-1", Title: "Task 1", Priority: 1, Type: "task"},
		{ID: "bead-2", Title: "Task 2", Priority: 1, Type: "task"},
		{ID: "bead-3", Title: "Task 3", Priority: 1, Type: "task"},
		{ID: "bead-4", Title: "Task 4", Priority: 1, Type: "task"},
		{ID: "bead-5", Title: "Task 5", Priority: 1, Type: "task"},
	})

	// Add acceptance criteria for all beads
	beadSrc.shown["bead-1"] = &protocol.BeadDetail{
		ID:                 "bead-1",
		Title:              "Task 1",
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
	}
	beadSrc.shown["bead-2"] = &protocol.BeadDetail{
		ID:                 "bead-2",
		Title:              "Task 2",
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
	}
	beadSrc.shown["bead-3"] = &protocol.BeadDetail{
		ID:                 "bead-3",
		Title:              "Task 3",
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
	}
	beadSrc.shown["bead-4"] = &protocol.BeadDetail{
		ID:                 "bead-4",
		Title:              "Task 4",
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
	}
	beadSrc.shown["bead-5"] = &protocol.BeadDetail{
		ID:                 "bead-5",
		Title:              "Task 5",
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
	}

	// Connect one worker and make it busy
	conn1, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn1, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "worker-1",
			ContextPct: 10,
		},
	})

	waitForWorkers(t, d, 1, 1*time.Second)

	// Worker receives assignment
	msg, ok := readMsg(t, conn1, 2*time.Second)
	if !ok || msg.Type != protocol.MsgAssign {
		t.Fatal("expected worker-1 to receive assignment")
	}

	// Send STATUS to transition worker to busy state
	sendMsg(t, conn1, protocol.Message{
		Type: protocol.MsgStatus,
		Status: &protocol.StatusPayload{
			State: string(protocol.WorkerBusy),
		},
	})

	// Wait for auto-scale attempts
	time.Sleep(300 * time.Millisecond)

	// Verify that targetWorkers never exceeded MaxWorkers
	d.mu.Lock()
	target := d.targetWorkers
	d.mu.Unlock()

	// Auto-scale should respect MaxWorkers even with 5 beads available
	// Expected: targetWorkers should be capped at MaxWorkers (2)
	if target > d.cfg.MaxWorkers {
		t.Errorf("auto-scale violated MaxWorkers limit: targetWorkers=%d, MaxWorkers=%d", target, d.cfg.MaxWorkers)
	}
}
