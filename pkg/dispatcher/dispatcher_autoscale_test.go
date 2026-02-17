package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"encoding/json"
	"net"
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

// TestReconcileScaleIgnoresUnmanagedWorkers verifies:
//  1. With MaxWorkers=2 and 2 managed + 1 unmanaged worker, reconcileScale does
//     not send PREPARE_SHUTDOWN to the unmanaged worker.
//  2. With MaxWorkers=0, reconcileScale is a no-op even with connected workers.
func TestReconcileScaleIgnoresUnmanagedWorkers(t *testing.T) {
	t.Run("unmanaged worker not killed when at target", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.cfg.MaxWorkers = 2

		// Build three mock connections.
		connManaged1 := newMockConn()
		connManaged2 := newMockConn()
		connUnmanaged := newMockConn()

		// Inject workers directly — bypasses network stack.
		d.mu.Lock()
		d.workers["managed-1"] = &trackedWorker{
			id:      "managed-1",
			conn:    connManaged1,
			state:   protocol.WorkerIdle,
			managed: true,
			encoder: json.NewEncoder(connManaged1),
		}
		d.workers["managed-2"] = &trackedWorker{
			id:      "managed-2",
			conn:    connManaged2,
			state:   protocol.WorkerIdle,
			managed: true,
			encoder: json.NewEncoder(connManaged2),
		}
		d.workers["unmanaged-1"] = &trackedWorker{
			id:      "unmanaged-1",
			conn:    connUnmanaged,
			state:   protocol.WorkerIdle,
			managed: false, // external worker
			encoder: json.NewEncoder(connUnmanaged),
		}
		// targetWorkers equals managed count: scale is balanced for managed workers.
		d.targetWorkers = 2
		d.mu.Unlock()

		result := d.reconcileScale()

		// reconcileScale should report no action needed (or scale-up detail).
		// Key assertion: unmanaged worker must NOT receive PREPARE_SHUTDOWN.
		connUnmanaged.mu.Lock()
		unmanagedWrites := len(connUnmanaged.written)
		connUnmanaged.mu.Unlock()
		if unmanagedWrites > 0 {
			t.Errorf("unmanaged worker received %d message(s), expected 0; reconcileScale must not kill unmanaged workers", unmanagedWrites)
		}

		// Also verify that managed workers were not killed (target == managed count).
		connManaged1.mu.Lock()
		managed1Writes := len(connManaged1.written)
		connManaged1.mu.Unlock()
		connManaged2.mu.Lock()
		managed2Writes := len(connManaged2.written)
		connManaged2.mu.Unlock()
		if managed1Writes > 0 || managed2Writes > 0 {
			t.Errorf("managed workers received messages when already at target: m1=%d m2=%d", managed1Writes, managed2Writes)
		}
		_ = result
	})

	t.Run("scaleDown only kills managed workers", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.cfg.MaxWorkers = 3

		connManaged1 := newMockConn()
		connManaged2 := newMockConn()
		connManaged3 := newMockConn()
		connUnmanaged := newMockConn()

		d.mu.Lock()
		d.workers["managed-1"] = &trackedWorker{
			id:      "managed-1",
			conn:    connManaged1,
			state:   protocol.WorkerIdle,
			managed: true,
			encoder: json.NewEncoder(connManaged1),
		}
		d.workers["managed-2"] = &trackedWorker{
			id:      "managed-2",
			conn:    connManaged2,
			state:   protocol.WorkerIdle,
			managed: true,
			encoder: json.NewEncoder(connManaged2),
		}
		d.workers["managed-3"] = &trackedWorker{
			id:      "managed-3",
			conn:    connManaged3,
			state:   protocol.WorkerBusy,
			managed: true,
			encoder: json.NewEncoder(connManaged3),
		}
		d.workers["unmanaged-1"] = &trackedWorker{
			id:      "unmanaged-1",
			conn:    connUnmanaged,
			state:   protocol.WorkerIdle,
			managed: false,
			encoder: json.NewEncoder(connUnmanaged),
		}
		// 3 managed, target 1 → scaleDown should remove 2 managed (idle first)
		d.targetWorkers = 1
		d.mu.Unlock()

		result := d.reconcileScale()

		if result == "" {
			t.Error("expected non-empty result from scaleDown")
		}

		// Unmanaged worker must NOT receive PREPARE_SHUTDOWN.
		connUnmanaged.mu.Lock()
		unmanagedWrites := len(connUnmanaged.written)
		connUnmanaged.mu.Unlock()
		if unmanagedWrites > 0 {
			t.Errorf("unmanaged worker received %d message(s), expected 0", unmanagedWrites)
		}

		// The 2 idle managed workers should be targeted (idle preferred over busy).
		connManaged1.mu.Lock()
		m1 := len(connManaged1.written)
		connManaged1.mu.Unlock()
		connManaged2.mu.Lock()
		m2 := len(connManaged2.written)
		connManaged2.mu.Unlock()
		connManaged3.mu.Lock()
		m3 := len(connManaged3.written)
		connManaged3.mu.Unlock()
		// Exactly 2 of the 3 managed workers should get messages.
		// Idle workers (managed-1, managed-2) should be preferred over busy (managed-3).
		messaged := 0
		if m1 > 0 {
			messaged++
		}
		if m2 > 0 {
			messaged++
		}
		if m3 > 0 {
			messaged++
		}
		if messaged != 2 {
			t.Errorf("expected 2 managed workers to receive shutdown, got %d (m1=%d m2=%d m3=%d)", messaged, m1, m2, m3)
		}
		// Busy worker should be preserved (idle first policy).
		if m3 > 0 && (m1 > 0 && m2 > 0) {
			t.Error("busy managed worker was shut down even though 2 idle workers were available")
		}
	})

	t.Run("MaxWorkers=0 is no-op", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.cfg.MaxWorkers = 0

		conn1 := newMockConn()
		conn2 := newMockConn()

		d.mu.Lock()
		d.workers["w1"] = &trackedWorker{
			id:      "w1",
			conn:    conn1,
			state:   protocol.WorkerIdle,
			managed: true,
			encoder: json.NewEncoder(conn1),
		}
		d.workers["w2"] = &trackedWorker{
			id:      "w2",
			conn:    conn2,
			state:   protocol.WorkerIdle,
			managed: false,
			encoder: json.NewEncoder(conn2),
		}
		d.targetWorkers = 5 // even with a non-zero target, MaxWorkers=0 means no-op
		d.mu.Unlock()

		result := d.reconcileScale()

		// No worker should receive any message.
		conn1.mu.Lock()
		w1Writes := len(conn1.written)
		conn1.mu.Unlock()
		conn2.mu.Lock()
		w2Writes := len(conn2.written)
		conn2.mu.Unlock()
		if w1Writes > 0 || w2Writes > 0 {
			t.Errorf("MaxWorkers=0: expected no messages sent, got w1=%d w2=%d", w1Writes, w2Writes)
		}
		if result != "" {
			t.Errorf("MaxWorkers=0: expected empty result string, got %q", result)
		}
	})
}

// TestApplyRestartWorker_PreservesManagedFlag verifies that restarting a managed
// worker records the new worker ID as pending-managed, so that when the respawned
// process connects via registerWorker the managed flag is set to true.
func TestApplyRestartWorker_PreservesManagedFlag(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)
	ctx := context.Background()

	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	pm := &mockProcessManager{}
	d.procMgr = pm

	// Register a managed worker.
	workerID := "managed-worker-1"
	conn1, conn2 := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()

	d.mu.Lock()
	d.pendingManagedIDs[workerID] = true
	d.mu.Unlock()
	d.registerWorker(workerID, conn1)

	// Verify it's managed.
	d.mu.Lock()
	if !d.workers[workerID].managed {
		t.Fatal("worker should be managed after registration with pending ID")
	}
	d.targetWorkers = 1
	d.mu.Unlock()

	// Restart the worker.
	_, err = d.applyRestartWorker(workerID)
	if err != nil {
		t.Fatalf("applyRestartWorker failed: %v", err)
	}

	// After restart, the worker ID should be in pendingManagedIDs so that when
	// the respawned process connects, registerWorker sets managed=true.
	d.mu.Lock()
	pending := d.pendingManagedIDs[workerID]
	d.mu.Unlock()

	if !pending {
		t.Errorf("expected workerID %q in pendingManagedIDs after restart, but it was absent", workerID)
	}

	// Simulate the respawned worker connecting.
	conn3, conn4 := net.Pipe()
	defer conn3.Close()
	defer conn4.Close()
	d.registerWorker(workerID, conn3)

	d.mu.Lock()
	w := d.workers[workerID]
	isManaged := w != nil && w.managed
	d.mu.Unlock()

	if !isManaged {
		t.Errorf("respawned worker %q should be managed after reconnect, but managed=%v", workerID, isManaged)
	}
}
