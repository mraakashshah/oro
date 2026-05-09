package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strings"
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

	// Wait for auto-scale logic to increase targetWorkers (replaces time.Sleep)
	waitFor(t, func() bool {
		d.mu.Lock()
		tw := d.targetWorkers
		d.mu.Unlock()
		return tw > 1
	}, 2*time.Second)

	// Read final value for assertions
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

	// Wait for auto-scale to increase targetWorkers (replaces time.Sleep)
	waitFor(t, func() bool {
		d.mu.Lock()
		tw := d.targetWorkers
		d.mu.Unlock()
		return tw > 1
	}, 2*time.Second)

	// Read final value for assertions
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
//     not send PREPARE_SHUTDOWN to the unmanaged worker, but does drain managed
//     capacity until total live workers are within MaxWorkers.
//  2. With MaxWorkers=0, reconcileScale drains managed workers and leaves
//     unmanaged/manual workers alone.
func TestReconcileScaleIgnoresUnmanagedWorkers(t *testing.T) {
	t.Run("manual worker consumes capacity but is not killed", func(t *testing.T) {
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
		// targetWorkers equals managed count, but the manual worker pushes total
		// live workers over MaxWorkers.
		d.targetWorkers = 2
		d.mu.Unlock()

		result := d.reconcileScale()

		// Key assertion: unmanaged worker must NOT receive PREPARE_SHUTDOWN.
		connUnmanaged.mu.Lock()
		unmanagedWrites := len(connUnmanaged.written)
		connUnmanaged.mu.Unlock()
		if unmanagedWrites > 0 {
			t.Errorf("unmanaged worker received %d message(s), expected 0; reconcileScale must not kill unmanaged workers", unmanagedWrites)
		}

		// One managed worker must be drained so total live workers can return to
		// MaxWorkers once the shutdown completes.
		connManaged1.mu.Lock()
		managed1Writes := len(connManaged1.written)
		connManaged1.mu.Unlock()
		connManaged2.mu.Lock()
		managed2Writes := len(connManaged2.written)
		connManaged2.mu.Unlock()
		messaged := 0
		if managed1Writes > 0 {
			messaged++
		}
		if managed2Writes > 0 {
			messaged++
		}
		if messaged != 1 {
			t.Errorf("expected 1 managed worker shutdown to restore total cap, got %d (m1=%d m2=%d)",
				messaged, managed1Writes, managed2Writes)
		}
		if !strings.Contains(result, "shutting down 1") {
			t.Fatalf("result = %q, want one managed shutdown", result)
		}
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

	t.Run("MaxWorkers=0 drains managed workers", func(t *testing.T) {
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
		d.targetWorkers = 0
		d.mu.Unlock()

		result := d.reconcileScale()

		// Managed worker should receive shutdown; unmanaged should not.
		conn1.mu.Lock()
		w1Writes := len(conn1.written)
		conn1.mu.Unlock()
		conn2.mu.Lock()
		w2Writes := len(conn2.written)
		conn2.mu.Unlock()
		if w1Writes == 0 {
			t.Error("MaxWorkers=0: managed worker should receive shutdown message")
		}
		if w2Writes > 0 {
			t.Errorf("MaxWorkers=0: unmanaged worker should not receive messages, got %d", w2Writes)
		}
		if result == "" {
			t.Error("MaxWorkers=0: expected non-empty result string from scaleDown")
		}
	})
}

func TestReconcileScaleCapsTotalWorkersAtMaxWorkers(t *testing.T) {
	t.Run("manual workers consume max worker capacity", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		pm := &mockProcessManager{}
		d.procMgr = pm
		d.cfg.MaxWorkers = 2

		d.mu.Lock()
		d.targetWorkers = 2
		for i := 0; i < 2; i++ {
			id := fmt.Sprintf("manual-%d", i)
			conn := newMockConn()
			d.workers[id] = &trackedWorker{
				id:      id,
				conn:    conn,
				state:   protocol.WorkerBusy,
				managed: false,
				encoder: json.NewEncoder(conn),
			}
		}
		d.mu.Unlock()

		result := d.reconcileScale()

		if got := len(pm.SpawnedIDs()); got != 0 {
			t.Fatalf("spawned %d workers despite total workers already at MaxWorkers; result=%q", got, result)
		}
		if !strings.Contains(result, "total") || !strings.Contains(result, "MaxWorkers") {
			t.Fatalf("result = %q, want MaxWorkers total cap detail", result)
		}
	})

	t.Run("partial manual capacity limits scale up", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		pm := &mockProcessManager{}
		d.procMgr = pm
		d.cfg.MaxWorkers = 3

		d.mu.Lock()
		d.targetWorkers = 3
		conn := newMockConn()
		d.workers["manual-1"] = &trackedWorker{
			id:      "manual-1",
			conn:    conn,
			state:   protocol.WorkerBusy,
			managed: false,
			encoder: json.NewEncoder(conn),
		}
		d.mu.Unlock()

		result := d.reconcileScale()

		if got := len(pm.SpawnedIDs()); got != 2 {
			t.Fatalf("spawned %d workers, want 2 to keep total live workers <= MaxWorkers; result=%q", got, result)
		}
	})

	t.Run("pending spawn-for workers consume max worker capacity", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		pm := &mockProcessManager{}
		d.procMgr = pm
		d.cfg.MaxWorkers = 2

		d.mu.Lock()
		d.targetWorkers = 2
		d.pendingManagedIDs["worker-spawnfor-pending"] = true
		d.pendingSpawnForWorkers["worker-spawnfor-pending"] = true
		conn := newMockConn()
		d.workers["manual-1"] = &trackedWorker{
			id:      "manual-1",
			conn:    conn,
			state:   protocol.WorkerIdle,
			managed: false,
			encoder: json.NewEncoder(conn),
		}
		d.mu.Unlock()

		result := d.reconcileScale()

		if got := len(pm.SpawnedIDs()); got != 0 {
			t.Fatalf("spawned %d workers despite pending spawn-for plus manual worker at cap; result=%q", got, result)
		}
	})
}

func TestScaleUpReservesPendingBeforeSpawnRejectsConcurrentManualWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.cfg.MaxWorkers = 1

	manualConn := newMockConn()
	pm := &hookProcessManager{}
	pm.onSpawn = func(_ string) {
		d.registerWorker("manual-during-spawn", manualConn)
	}
	d.procMgr = pm

	result := d.scaleUp(1, 0, 1)

	if got := len(pm.SpawnedIDs()); got != 1 {
		t.Fatalf("spawned %d managed workers, want 1; result=%q", got, result)
	}
	d.mu.Lock()
	pending := d.pendingManagedIDs[pm.SpawnedIDs()[0]]
	d.mu.Unlock()
	if !pending {
		t.Fatal("managed worker was not reserved before Spawn returned")
	}

	d.mu.Lock()
	manual := d.workers["manual-during-spawn"]
	d.mu.Unlock()
	if manual == nil {
		t.Fatal("manual worker should be tracked until it disconnects")
	}
	if manual.state != protocol.WorkerShuttingDown {
		t.Fatalf("manual worker state = %s, want %s", manual.state, protocol.WorkerShuttingDown)
	}
	manualConn.mu.Lock()
	defer manualConn.mu.Unlock()
	if len(manualConn.written) != 1 {
		t.Fatalf("expected one shutdown message for concurrent manual worker, got %d", len(manualConn.written))
	}
}

func TestScaleUpRechecksMaxWorkerCapacityBeforeEachReservation(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.cfg.MaxWorkers = 2

	spawnCalls := 0
	pm := &hookProcessManager{}
	pm.onSpawn = func(_ string) {
		spawnCalls++
		if spawnCalls != 1 {
			return
		}
		if _, err := d.applyDirective(protocol.Directive("launch-workers"), `{"worker_ids":["manual-last-slot"]}`); err != nil {
			t.Fatalf("launch-workers directive failed while scaleUp spawn lock was released: %v", err)
		}
	}
	d.procMgr = pm

	result := d.scaleUp(2, 0, 2)

	if got := len(pm.SpawnedIDs()); got != 1 {
		t.Fatalf("spawned %d managed workers, want 1 after manual reservation consumed last slot; result=%q", got, result)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if got := d.liveWorkerCountLocked(); got != 2 {
		t.Fatalf("live worker reservations = %d, want 2 (one managed, one manual)", got)
	}
	if got := len(d.pendingExternalIDs); got != 1 {
		t.Fatalf("pending external reservations = %d, want 1", got)
	}
	if got := len(d.pendingManagedIDs); got != 1 {
		t.Fatalf("pending managed reservations = %d, want 1", got)
	}
}

func TestApplyMaxWorkersDrainsManagedWhenManualWorkersConsumeCapacity(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.cfg.MaxWorkers = 5

	managedConns := make([]*mockConn, 3)
	d.mu.Lock()
	d.targetWorkers = 3
	for i := 0; i < 3; i++ {
		conn := newMockConn()
		managedConns[i] = conn
		id := fmt.Sprintf("managed-%d", i)
		d.workers[id] = &trackedWorker{
			id:      id,
			conn:    conn,
			state:   protocol.WorkerIdle,
			managed: true,
			encoder: json.NewEncoder(conn),
		}
	}
	for i := 0; i < 2; i++ {
		conn := newMockConn()
		id := fmt.Sprintf("manual-%d", i)
		d.workers[id] = &trackedWorker{
			id:      id,
			conn:    conn,
			state:   protocol.WorkerIdle,
			managed: false,
			encoder: json.NewEncoder(conn),
		}
	}
	d.mu.Unlock()

	detail, err := d.applyMaxWorkersDirective("2")
	if err != nil {
		t.Fatalf("applyMaxWorkersDirective failed: %v", err)
	}
	if !strings.Contains(detail, "max_workers=2") {
		t.Fatalf("detail = %q, want max_workers=2", detail)
	}

	shutdowns := 0
	for i, conn := range managedConns {
		conn.mu.Lock()
		writes := len(conn.written)
		conn.mu.Unlock()
		if writes > 0 {
			shutdowns++
		} else {
			t.Errorf("managed-%d did not receive shutdown despite manual workers consuming all capacity", i)
		}
	}
	if shutdowns != 3 {
		t.Fatalf("managed shutdown count = %d, want 3", shutdowns)
	}
}

func TestApplyMaxWorkersDirectiveCancelsExcessPendingManagedSpawns(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.cfg.MaxWorkers = 4

	d.mu.Lock()
	d.targetWorkers = 4
	d.pendingManagedIDs["pending-1"] = true
	d.pendingManagedSince["pending-1"] = d.nowFunc()
	d.pendingManagedIDs["pending-2"] = true
	d.pendingManagedSince["pending-2"] = d.nowFunc()
	for i := 0; i < 2; i++ {
		conn := newMockConn()
		id := fmt.Sprintf("managed-%d", i)
		d.workers[id] = &trackedWorker{
			id:      id,
			conn:    conn,
			state:   protocol.WorkerIdle,
			managed: true,
			encoder: json.NewEncoder(conn),
		}
	}
	d.mu.Unlock()

	detail, err := d.applyMaxWorkersDirective("2")
	if err != nil {
		t.Fatalf("applyMaxWorkersDirective failed: %v", err)
	}
	if !strings.Contains(detail, "max_workers=2") {
		t.Fatalf("detail = %q, want max_workers=2", detail)
	}

	killed := pm.KilledIDs()
	if len(killed) != 2 {
		t.Fatalf("killed pending workers = %v, want two pending kills", killed)
	}
	d.mu.Lock()
	pendingCount := len(d.pendingManagedIDs)
	d.mu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pending managed count = %d, want 0 after cap cancellation", pendingCount)
	}
}

func TestReconcileScaleCancelsPendingManagedWhenManualWorkersConsumeCapacity(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.cfg.MaxWorkers = 2

	d.mu.Lock()
	d.targetWorkers = 2
	d.pendingManagedIDs["pending-1"] = true
	d.pendingManagedSince["pending-1"] = d.nowFunc()
	d.pendingManagedIDs["pending-2"] = true
	d.pendingManagedSince["pending-2"] = d.nowFunc()
	for i := 0; i < 2; i++ {
		conn := newMockConn()
		id := fmt.Sprintf("manual-%d", i)
		d.workers[id] = &trackedWorker{
			id:      id,
			conn:    conn,
			state:   protocol.WorkerIdle,
			managed: false,
			encoder: json.NewEncoder(conn),
		}
	}
	d.mu.Unlock()

	detail := d.reconcileScale()
	if !strings.Contains(detail, "shutting down 2") {
		t.Fatalf("reconcile detail = %q, want pending shutdown detail", detail)
	}
	if killed := pm.KilledIDs(); len(killed) != 2 {
		t.Fatalf("killed pending workers = %v, want 2", killed)
	}
	d.mu.Lock()
	pendingCount := len(d.pendingManagedIDs)
	d.mu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pending managed count = %d, want 0", pendingCount)
	}
}

func TestApplyLaunchWorkersDirectiveReservesExternalWorkersAgainstMaxWorkers(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.cfg.MaxWorkers = 1

	detail, err := d.applyDirective(protocol.Directive("launch-workers"), `{"worker_ids":["manual-reserved"]}`)
	if err != nil {
		t.Fatalf("launch-workers directive failed: %v", err)
	}
	if !strings.Contains(detail, "reserved 1") {
		t.Fatalf("detail = %q, want reservation detail", detail)
	}

	d.mu.Lock()
	live := d.liveWorkerCountLocked()
	d.mu.Unlock()
	if live != 1 {
		t.Fatalf("live worker count = %d, want 1 reserved external worker", live)
	}

	_, err = d.applyDirective(protocol.Directive("launch-workers"), `{"worker_ids":["manual-over-cap"]}`)
	if err == nil {
		t.Fatal("expected second launch reservation to fail at MaxWorkers cap")
	}
	if !strings.Contains(err.Error(), "max workers reached") {
		t.Fatalf("error = %v, want max workers reached", err)
	}
}

func TestReconcileScaleIgnoresWorkersAlreadyShuttingDownForDrainPressure(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)
	d.cfg.MaxWorkers = 2

	managedConns := make([]*mockConn, 2)
	d.mu.Lock()
	d.targetWorkers = 2
	for i := 0; i < 2; i++ {
		conn := newMockConn()
		managedConns[i] = conn
		id := fmt.Sprintf("managed-%d", i)
		d.workers[id] = &trackedWorker{
			id:      id,
			conn:    conn,
			state:   protocol.WorkerBusy,
			managed: true,
			encoder: json.NewEncoder(conn),
		}
	}
	manualConn := newMockConn()
	d.workers["manual-rejected"] = &trackedWorker{
		id:      "manual-rejected",
		conn:    manualConn,
		state:   protocol.WorkerShuttingDown,
		managed: false,
		encoder: json.NewEncoder(manualConn),
	}
	d.mu.Unlock()

	result := d.reconcileScale()
	if result != "" {
		t.Fatalf("reconcileScale result = %q, want no drain while rejected manual worker is already shutting down", result)
	}
	for i, conn := range managedConns {
		conn.mu.Lock()
		writes := len(conn.written)
		conn.mu.Unlock()
		if writes != 0 {
			t.Fatalf("managed-%d received %d shutdown messages due to already-shutting-down manual worker", i, writes)
		}
	}
}

func TestScaleDownSkipsManagedWorkersAlreadyShuttingDown(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	activeConn := newMockConn()
	shuttingConn1 := newMockConn()
	shuttingConn2 := newMockConn()
	d.mu.Lock()
	d.workers["active-managed"] = &trackedWorker{
		id:      "active-managed",
		conn:    activeConn,
		state:   protocol.WorkerBusy,
		managed: true,
		encoder: json.NewEncoder(activeConn),
	}
	d.workers["shutting-managed-1"] = &trackedWorker{
		id:      "shutting-managed-1",
		conn:    shuttingConn1,
		state:   protocol.WorkerShuttingDown,
		managed: true,
		encoder: json.NewEncoder(shuttingConn1),
	}
	d.workers["shutting-managed-2"] = &trackedWorker{
		id:      "shutting-managed-2",
		conn:    shuttingConn2,
		state:   protocol.WorkerShuttingDown,
		managed: true,
		encoder: json.NewEncoder(shuttingConn2),
	}
	d.mu.Unlock()

	result := d.scaleDown(0, 3)
	if !strings.Contains(result, "shutting down 1") {
		t.Fatalf("scaleDown result = %q, want only active managed worker selected", result)
	}

	activeConn.mu.Lock()
	activeWrites := len(activeConn.written)
	activeConn.mu.Unlock()
	if activeWrites != 1 {
		t.Fatalf("active managed worker writes = %d, want 1 shutdown", activeWrites)
	}
	for id, conn := range map[string]*mockConn{
		"shutting-managed-1": shuttingConn1,
		"shutting-managed-2": shuttingConn2,
	} {
		conn.mu.Lock()
		writes := len(conn.written)
		conn.mu.Unlock()
		if writes != 0 {
			t.Fatalf("%s received %d shutdown messages despite already shutting down", id, writes)
		}
	}
}

func TestStatusJSONExposesWorkerCapacityRoles(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.cfg.MaxWorkers = 2

	managedConn := newMockConn()
	manualConn := newMockConn()
	d.mu.Lock()
	d.workers["managed-1"] = &trackedWorker{
		id:      "managed-1",
		conn:    managedConn,
		state:   protocol.WorkerIdle,
		managed: true,
		encoder: json.NewEncoder(managedConn),
	}
	d.workers["manual-1"] = &trackedWorker{
		id:      "manual-1",
		conn:    manualConn,
		state:   protocol.WorkerIdle,
		managed: false,
		encoder: json.NewEncoder(manualConn),
	}
	d.pendingManagedIDs["pending-managed"] = true
	d.pendingManagedSince["pending-managed"] = d.nowFunc()
	d.mu.Unlock()

	var status statusResponse
	if err := json.Unmarshal([]byte(d.buildStatusJSON()), &status); err != nil {
		t.Fatalf("decode status JSON: %v", err)
	}
	if status.MaxWorkers != 2 {
		t.Fatalf("max_workers = %d, want 2", status.MaxWorkers)
	}
	if status.ManagedCount != 1 || status.UnmanagedCount != 1 || status.PendingWorkerCount != 1 {
		t.Fatalf("worker role counts: managed=%d unmanaged=%d pending=%d, want 1/1/1",
			status.ManagedCount, status.UnmanagedCount, status.PendingWorkerCount)
	}

	roles := make(map[string]bool)
	for _, worker := range status.Workers {
		roles[worker.ID] = worker.Managed
	}
	if !roles["managed-1"] {
		t.Fatal("managed worker status did not expose managed=true")
	}
	if roles["manual-1"] {
		t.Fatal("manual worker status exposed managed=true")
	}
}

// TestReconcileScale_CapsAtDoubleTarget verifies that reconcileScale refuses to
// spawn workers when managed worker count (connected + pending + unexpected exits)
// reaches 2*targetWorkers. This prevents runaway crash-respawn loops where stuck
// workers get reaped by heartbeat timeout, drop managedCount, and trigger spawning
// of replacements that also get stuck (oro-135n, oro-kdne).
func TestReconcileScale_CapsAtDoubleTarget(t *testing.T) {
	t.Run("blocks spawn when managed+exits at 2x target", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		pm := &mockProcessManager{}
		d.procMgr = pm
		d.cfg.MaxWorkers = 20 // high ceiling so MaxWorkers doesn't interfere

		d.mu.Lock()
		d.targetWorkers = 5
		// 3 managed workers connected (managedCount=3 < target=5 → scaleUp path)
		for i := 0; i < 3; i++ {
			id := fmt.Sprintf("managed-%d", i)
			conn := newMockConn()
			d.workers[id] = &trackedWorker{
				id:      id,
				conn:    conn,
				state:   protocol.WorkerBusy,
				managed: true,
				encoder: json.NewEncoder(conn),
			}
		}
		// 7 managed workers have already died unexpectedly.
		// managedCount(3) + exits(7) = 10 >= 2*5 = 10 → cap blocks scaleUp.
		d.unexpectedManagedExits = 7
		d.mu.Unlock()

		_ = d.reconcileScale()

		pm.mu.Lock()
		spawned := len(pm.spawned)
		pm.mu.Unlock()

		if spawned > 0 {
			t.Errorf("expected 0 spawns when managed+exits (10) >= 2*target (10), got %d", spawned)
		}
	})

	t.Run("allows spawn when managed+exits below 2x target", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		pm := &mockProcessManager{}
		d.procMgr = pm
		d.cfg.MaxWorkers = 20

		d.mu.Lock()
		d.targetWorkers = 5
		// 3 managed workers, 2 unmanaged (unmanaged must not affect cap)
		for i := 0; i < 3; i++ {
			id := fmt.Sprintf("managed-%d", i)
			conn := newMockConn()
			d.workers[id] = &trackedWorker{
				id:      id,
				conn:    conn,
				state:   protocol.WorkerBusy,
				managed: true,
				encoder: json.NewEncoder(conn),
			}
		}
		for i := 0; i < 2; i++ {
			id := fmt.Sprintf("unmanaged-%d", i)
			conn := newMockConn()
			d.workers[id] = &trackedWorker{
				id:      id,
				conn:    conn,
				state:   protocol.WorkerBusy,
				managed: false,
				encoder: json.NewEncoder(conn),
			}
		}
		// exits=0: managedCount(3)+exits(0)=3 < 2*5=10 → cap allows; spawn 2
		d.mu.Unlock()

		_ = d.reconcileScale()

		pm.mu.Lock()
		spawned := len(pm.spawned)
		pm.mu.Unlock()

		if spawned != 2 {
			t.Errorf("expected 2 spawns when managed+exits (3) < 2*target (10), got %d", spawned)
		}
	})

	t.Run("cap counts pending managed IDs toward managed total", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		pm := &mockProcessManager{}
		d.procMgr = pm
		d.cfg.MaxWorkers = 20

		d.mu.Lock()
		d.targetWorkers = 5
		// 1 stale pending managed ID + 9 unexpected exits = managed+exits 10 >= 2*5=10 → blocked.
		// Unmanaged workers are present but must not influence the cap.
		for i := 0; i < 5; i++ {
			id := fmt.Sprintf("unmanaged-%d", i)
			conn := newMockConn()
			d.workers[id] = &trackedWorker{
				id:      id,
				conn:    conn,
				state:   protocol.WorkerBusy,
				managed: false,
				encoder: json.NewEncoder(conn),
			}
		}
		d.pendingManagedIDs["stale-pending-0"] = true
		d.unexpectedManagedExits = 9
		d.mu.Unlock()
		// managedCount = 1 pending; exits = 9; 1+9 = 10 >= 2*5 = 10 → cap blocks

		_ = d.reconcileScale()

		pm.mu.Lock()
		spawned := len(pm.spawned)
		pm.mu.Unlock()

		if spawned > 0 {
			t.Errorf("expected 0 spawns when managed+exits (pending+exits=10) >= 2*target (10), got %d", spawned)
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

func TestApplyRestartWorker_KillsManagedProcessBeforeSameIDRespawn(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	pm := &mockProcessManager{}
	d.procMgr = pm

	workerID := "managed-restart-kill"
	conn1, conn2 := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()

	d.mu.Lock()
	d.pendingManagedIDs[workerID] = true
	d.mu.Unlock()
	d.registerWorker(workerID, conn1)

	if _, err := d.applyRestartWorker(workerID); err != nil {
		t.Fatalf("applyRestartWorker failed: %v", err)
	}

	killed := pm.KilledIDs()
	if len(killed) != 1 || killed[0] != workerID {
		t.Fatalf("restart-worker must kill old managed process before respawn; killed=%v", killed)
	}

	spawned := pm.SpawnedIDs()
	if len(spawned) != 1 || spawned[0] != workerID {
		t.Fatalf("restart-worker must respawn same worker ID once; spawned=%v", spawned)
	}

	wantEvents := []string{"kill:" + workerID, "spawn:" + workerID}
	if events := pm.Events(); !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("restart-worker process lifecycle order = %v, want %v", events, wantEvents)
	}
}

func TestRespawnWorker_ReservesHandoffWorkerAsManagedAtMaxWorkers(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)
	d.cfg.MaxWorkers = 2
	pm := &mockProcessManager{}
	d.procMgr = pm

	now := time.Date(2026, 5, 2, 17, 40, 0, 0, time.UTC)
	d.nowFunc = func() time.Time { return now }

	oldConn := newMockConn()
	d.mu.Lock()
	d.workers["old-handoff-worker"] = &trackedWorker{
		id:      "old-handoff-worker",
		conn:    oldConn,
		state:   protocol.WorkerShuttingDown,
		managed: true,
		encoder: json.NewEncoder(oldConn),
	}
	d.mu.Unlock()

	d.respawnWorker(context.Background(), "bead-handoff", "/tmp/wt-handoff", protocol.ModelSonnet, "", "main", "main", "Handoff bead", nil)

	spawned := pm.SpawnedIDs()
	if len(spawned) != 1 {
		t.Fatalf("spawned %d handoff workers, want 1", len(spawned))
	}
	workerID := spawned[0]

	d.mu.Lock()
	pending := d.pendingManagedIDs[workerID]
	d.mu.Unlock()
	if !pending {
		t.Fatalf("handoff worker %q was not reserved as managed before reconnect", workerID)
	}

	conn := newMockConn()
	d.registerWorker(workerID, conn)

	d.mu.Lock()
	w := d.workers[workerID]
	d.mu.Unlock()
	if w == nil {
		t.Fatal("handoff worker should be tracked after registration")
	}
	if !w.managed {
		t.Fatal("handoff worker should reconnect as managed")
	}
	if w.state != protocol.WorkerBusy {
		t.Fatalf("handoff worker state = %s, want %s", w.state, protocol.WorkerBusy)
	}
	if w.beadID != "bead-handoff" {
		t.Fatalf("handoff worker bead = %q, want bead-handoff", w.beadID)
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.written) != 1 {
		t.Fatalf("expected one ASSIGN message, got %d", len(conn.written))
	}
	var msg protocol.Message
	if err := json.Unmarshal(conn.written[0], &msg); err != nil {
		t.Fatalf("decode worker message: %v", err)
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("message type = %s, want %s", msg.Type, protocol.MsgAssign)
	}
}

func TestRespawnWorker_DefersSpawnWhenMaxLiveWorkersReached(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)
	d.cfg.MaxWorkers = 1
	pm := &mockProcessManager{}
	d.procMgr = pm

	oldConn := newMockConn()
	d.mu.Lock()
	d.workers["old-handoff-worker"] = &trackedWorker{
		id:      "old-handoff-worker",
		conn:    oldConn,
		state:   protocol.WorkerShuttingDown,
		managed: true,
		encoder: json.NewEncoder(oldConn),
	}
	d.mu.Unlock()

	d.respawnWorker(context.Background(), "bead-handoff", "/tmp/wt-handoff", protocol.ModelSonnet, "", "main", "main", "Handoff bead", nil)

	if spawned := pm.SpawnedIDs(); len(spawned) != 0 {
		t.Fatalf("spawned handoff workers despite MaxWorkers live cap: %v", spawned)
	}
	d.mu.Lock()
	handoff := d.pendingHandoffs["bead-handoff"]
	pendingCount := len(d.pendingManagedIDs)
	d.mu.Unlock()
	if handoff == nil {
		t.Fatal("pending handoff should remain queued when spawn is deferred")
	}
	if pendingCount != 0 {
		t.Fatalf("pending managed workers = %d, want 0 when handoff spawn is deferred", pendingCount)
	}
}

// TestApplyScaleDirective_ClampsToMaxWorkers verifies that applyScaleDirective
// clamps targetWorkers to MaxWorkers when the requested target exceeds the limit.
func TestApplyScaleDirective_ClampsToMaxWorkers(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)

	pm := &mockProcessManager{}
	d.procMgr = pm
	d.cfg.MaxWorkers = 3

	_, err := d.applyScaleDirective("10")
	if err != nil {
		t.Fatalf("applyScaleDirective failed: %v", err)
	}

	d.mu.Lock()
	target := d.targetWorkers
	d.mu.Unlock()

	if target != 3 {
		t.Errorf("expected targetWorkers to be clamped to MaxWorkers=3, got %d", target)
	}
}

// TestReconcileScale_CapByManagedExits verifies the corrected 2*target cap:
// (a) unmanaged workers do not contribute to the cap and must not block managed
// worker spawning (the oro-kdne root cause), (b) unexpected managed exits tracked
// in d.unexpectedManagedExits do contribute, (c) checkHeartbeats increments
// d.unexpectedManagedExits only for dead managed workers, and (d)
// applyScaleDirective resets the counter when the target changes.
func TestReconcileScale_CapByManagedExits(t *testing.T) {
	t.Run("unmanaged workers do not block managed spawning", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		pm := &mockProcessManager{}
		d.procMgr = pm
		d.cfg.MaxWorkers = 20

		d.mu.Lock()
		d.targetWorkers = 3
		// Simulate 10 orphaned unmanaged workers from a previous session.
		for i := 0; i < 10; i++ {
			id := fmt.Sprintf("orphaned-%d", i)
			conn := newMockConn()
			d.workers[id] = &trackedWorker{
				id:      id,
				conn:    conn,
				state:   protocol.WorkerIdle,
				managed: false,
				encoder: json.NewEncoder(conn),
			}
		}
		d.mu.Unlock()
		// managedCount=0 + unexpectedManagedExits=0 < 2*3=6 → cap must not fire → spawn 3

		_ = d.reconcileScale()

		pm.mu.Lock()
		spawned := len(pm.spawned)
		pm.mu.Unlock()

		if spawned != 3 {
			t.Errorf("expected 3 spawns (unmanaged workers must not block managed spawning), got %d", spawned)
		}
	})

	t.Run("unexpectedManagedExits at 2*target blocks scaleUp", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		pm := &mockProcessManager{}
		d.procMgr = pm
		d.cfg.MaxWorkers = 20

		d.mu.Lock()
		d.targetWorkers = 3
		// 6 managed workers have already died → cap = 2*3 = 6 reached.
		d.unexpectedManagedExits = 6
		d.mu.Unlock()
		// managedCount=0 + unexpectedManagedExits=6 >= 2*3=6 → cap fires

		_ = d.reconcileScale()

		pm.mu.Lock()
		spawned := len(pm.spawned)
		pm.mu.Unlock()

		if spawned > 0 {
			t.Errorf("expected 0 spawns when unexpectedManagedExits (6) >= 2*target (6), got %d", spawned)
		}
	})

	t.Run("checkHeartbeats increments counter only for dead managed workers", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		now := time.Now()
		d.nowFunc = func() time.Time { return now }

		d.mu.Lock()
		// One dead managed worker.
		conn1 := newMockConn()
		d.workers["managed-dead"] = &trackedWorker{
			id:       "managed-dead",
			conn:     conn1,
			state:    protocol.WorkerBusy,
			managed:  true,
			lastSeen: now.Add(-2 * d.cfg.HeartbeatTimeout),
			encoder:  json.NewEncoder(conn1),
		}
		// One dead unmanaged worker — must NOT be counted.
		conn2 := newMockConn()
		d.workers["unmanaged-dead"] = &trackedWorker{
			id:       "unmanaged-dead",
			conn:     conn2,
			state:    protocol.WorkerBusy,
			managed:  false,
			lastSeen: now.Add(-2 * d.cfg.HeartbeatTimeout),
			encoder:  json.NewEncoder(conn2),
		}
		d.mu.Unlock()

		d.checkHeartbeats(context.Background())

		d.mu.Lock()
		exits := d.unexpectedManagedExits
		d.mu.Unlock()

		if exits != 1 {
			t.Errorf("expected unexpectedManagedExits=1 (only managed worker counted), got %d", exits)
		}
	})

	t.Run("applyScaleDirective resets unexpectedManagedExits", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.setState(StateRunning)

		pm := &mockProcessManager{}
		d.procMgr = pm
		d.cfg.MaxWorkers = 20

		d.mu.Lock()
		d.unexpectedManagedExits = 99
		d.mu.Unlock()

		_, err := d.applyScaleDirective("3")
		if err != nil {
			t.Fatalf("applyScaleDirective failed: %v", err)
		}

		d.mu.Lock()
		exits := d.unexpectedManagedExits
		d.mu.Unlock()

		if exits != 0 {
			t.Errorf("expected unexpectedManagedExits=0 after applyScaleDirective, got %d", exits)
		}
	})
}
