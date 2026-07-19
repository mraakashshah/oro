package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"oro/pkg/testutil/qgserial"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestNoMultipleAssignmentsToSameBead verifies that when assignBead is called
// concurrently for the same bead, only one assignment succeeds because the bead
// status is updated to in_progress atomically before worktree creation.
// This test reproduces oro-ptp2: the race condition where multiple workers
// could be spawned for the same P0 bead, causing resource thrashing.
func TestNoMultipleAssignmentsToSameBead(t *testing.T) {
	qgserial.RequireSerial(t)
	t.Parallel()

	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Track how many worktrees are created for oro-test1.
	// If the race condition exists, both assignBead calls will create worktrees.
	var worktreeCreateCount atomic.Int32

	// Channel-based synchronization: createStarted signals that the first
	// worktree creation has entered the callback; createProceed gates it
	// from returning, widening the race window without a raw sleep.
	createStarted := make(chan struct{}, 2)
	createProceed := make(chan struct{})

	wtMgr.createFn = func(_ context.Context, beadID, _ string) (string, string, error) {
		if beadID == "oro-test1" {
			createStarted <- struct{}{}
			<-createProceed // block until test signals to proceed
			worktreeCreateCount.Add(1)
		}
		return "/tmp/" + beadID, "branch-" + beadID, nil
	}

	// Connect two workers
	conn1, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn1, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w1",
			ContextPct: 5,
		},
	})

	conn2, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn2, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w2",
			ContextPct: 5,
		},
	})

	waitForWorkers(t, d, 2, 1*time.Second)

	// Setup bead detail
	beadSrc.mu.Lock()
	beadSrc.shown["oro-test1"] = &protocol.BeadDetail{
		ID:                 "oro-test1",
		Title:              "Test bead",
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
	}
	beadSrc.mu.Unlock()

	// Get the workers
	d.mu.Lock()
	worker1 := d.workers["w1"]
	worker2 := d.workers["w2"]
	d.mu.Unlock()

	if worker1 == nil || worker2 == nil {
		t.Fatal("workers not registered")
	}

	// Concurrently call assignBead for the SAME bead from two "dispatcher threads".
	// This simulates the race condition where multiple dispatchers pick up the same
	// bead from Ready() before either marks it in_progress.
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_ = d.assignBead(ctx, worker1, protocol.Bead{ID: "oro-test1", Priority: 0})
	}()

	go func() {
		defer wg.Done()
		_ = d.assignBead(ctx, worker2, protocol.Bead{ID: "oro-test1", Priority: 0})
	}()

	// Wait for at least one worktree creation to start, then unblock it.
	// The race guard should ensure only one goroutine reaches createFn.
	select {
	case <-createStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worktree creation to start")
	}
	close(createProceed)

	wg.Wait()

	// CRITICAL ASSERTION: Only one worktree should be created.
	// If both assignBead calls proceeded past the status check, worktreeCreateCount would be 2.
	// The fix ensures status is marked in_progress BEFORE worktree creation, so the
	// second call fails the status check and aborts early.
	actualWorktreeCreates := worktreeCreateCount.Load()
	if actualWorktreeCreates != 1 {
		t.Errorf("Race condition detected: expected 1 worktree create, got %d", actualWorktreeCreates)
	}

	// Verify: Only one update to in_progress
	beadSrc.mu.Lock()
	status := beadSrc.updated["oro-test1"]
	beadSrc.mu.Unlock()

	if status != "in_progress" {
		t.Errorf("Expected bead oro-test1 marked in_progress, got %q", status)
	}

	// Verify: Only one worker is busy with oro-test1
	d.mu.Lock()
	busyCount := 0
	for _, w := range d.workers {
		if w.beadID == "oro-test1" && w.state == protocol.WorkerBusy {
			busyCount++
		}
	}
	d.mu.Unlock()

	if busyCount != 1 {
		t.Errorf("Expected exactly 1 busy worker on oro-test1, got %d", busyCount)
	}
}

// TestScaleUpDoesNotDuplicateAssignment verifies that when the dispatcher scales up
// (e.g. 5→6 workers), the new worker does NOT get assigned a bead that is currently
// in-flight (assigningBeads set but worker state not yet updated to Busy).
//
// Regression test for oro-30o: assignLoop must check assigningBeads in
// filterAssignable before dispatching to a worker, preventing the window where
// a newly scaled-up worker races with an in-progress assignment.
func TestScaleUpDoesNotDuplicateAssignment(t *testing.T) {
	qgserial.RequireSerial(t)
	t.Parallel()

	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	d.cfg.MaxWorkers = 6
	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 5*time.Second)

	// Provide one bead.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-scale", Title: "Scale up task", Priority: 1, Type: "task"},
	})

	// Simulate 5 busy workers (W1–W5) each holding a different bead.
	busyConns := make([]net.Conn, 5)
	for i := 0; i < 5; i++ {
		wid := fmt.Sprintf("busy-w%d", i+1)
		bid := fmt.Sprintf("bead-%d", i+1)

		c1, c2 := net.Pipe()
		t.Cleanup(func() { _ = c1.Close(); _ = c2.Close() })
		busyConns[i] = c1

		d.mu.Lock()
		d.workers[wid] = &trackedWorker{
			id:      wid,
			conn:    c1,
			state:   protocol.WorkerBusy,
			beadID:  bid,
			encoder: json.NewEncoder(c1),
		}
		d.mu.Unlock()
	}

	// Inject assigningBeads["bead-scale"] to simulate that an in-flight
	// assignment is underway — the bead has been claimed but the worker that
	// claimed it has not yet transitioned to WorkerBusy.
	d.mu.Lock()
	if d.assigningBeads == nil {
		d.assigningBeads = make(map[string]bool)
	}
	d.assigningBeads["bead-scale"] = true
	d.mu.Unlock()

	// Connect the new (scale-up) worker W6 — it starts idle.
	conn6, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn6, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "scale-w6",
			ContextPct: 5,
		},
	})

	// Wait for W6 to be registered (total: 5 injected + 1 connected = 6).
	waitForWorkers(t, d, 6, 5*time.Second)

	// Wait for the assign loop to have run at least once with W6 idle.
	// cachedQueueDepth > 0 proves tryAssign ran and saw the ready bead.
	waitFor(t, func() bool {
		d.mu.Lock()
		depth := d.cachedQueueDepth
		d.mu.Unlock()
		return depth > 0
	}, 10*time.Second)

	// ASSERTION 1: No assignment_race_detected event should have been logged.
	// Before the fix, filterAssignable does NOT check assigningBeads, so
	// tryAssign calls assignBead(W6, bead-scale), which then detects the race
	// and logs assignment_race_detected. After the fix, filterAssignable
	// excludes in-flight beads so assignBead is never called.
	raceCount := eventCount(t, d.db, "assignment_race_detected")
	if raceCount > 0 {
		t.Errorf("assignment_race_detected logged %d time(s) — filterAssignable must exclude in-flight beads (assigningBeads)", raceCount)
	}

	// ASSERTION 2: No worktree_error event (belt-and-suspenders check).
	wtErrCount := eventCount(t, d.db, "worktree_error")
	if wtErrCount > 0 {
		t.Errorf("worktree_error logged %d time(s) — bead-scale was assigned to scale-up worker despite being in-flight", wtErrCount)
	}

	// ASSERTION 3: W6 must still be idle — it must not have received an ASSIGN.
	d.mu.Lock()
	w6, exists := d.workers["scale-w6"]
	var w6State protocol.WorkerState
	if exists {
		w6State = w6.state
	}
	d.mu.Unlock()
	if !exists {
		t.Fatal("scale-w6 not found in worker pool")
	}
	if w6State != protocol.WorkerIdle {
		t.Errorf("scale-w6 state = %q, want Idle — it should not have been assigned bead-scale", w6State)
	}
}

// TestReconnectDoesNotStealBead verifies that when a worker reconnects and
// reports it's working on a bead that is already assigned to another worker,
// the reconnect does NOT override the existing assignment. This prevents the
// race condition documented in oro-ovpc where two workers (one via normal
// assignment, one via reconnect) can be simultaneously assigned to the same bead.
func TestReconnectDoesNotStealBead(t *testing.T) {
	qgserial.RequireSerial(t)
	t.Parallel()

	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Setup bead detail
	beadSrc.mu.Lock()
	beadSrc.shown["oro-test1"] = &protocol.BeadDetail{
		ID:                 "oro-test1",
		Title:              "Test bead",
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
	}
	beadSrc.mu.Unlock()

	// Connect worker 1 and assign it to oro-test1 via normal assignment flow
	conn1, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn1, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w1",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Assign w1 to oro-test1
	ctx := context.Background()
	d.mu.Lock()
	worker1 := d.workers["w1"]
	d.mu.Unlock()
	if worker1 == nil {
		t.Fatal("worker1 not registered")
	}

	// Simulate normal assignment (this sets assigningBeads and worker state)
	_ = d.assignBead(ctx, worker1, protocol.Bead{ID: "oro-test1", Priority: 0})

	// Wait for assignment to complete
	time.Sleep(100 * time.Millisecond)

	// Verify w1 is assigned to oro-test1
	d.mu.Lock()
	w1State := d.workers["w1"].state
	w1BeadID := d.workers["w1"].beadID
	d.mu.Unlock()

	if w1State != protocol.WorkerBusy || w1BeadID != "oro-test1" {
		t.Fatalf("worker1 not assigned correctly: state=%s beadID=%s", w1State, w1BeadID)
	}

	// Now connect worker 2 and have it RECONNECT claiming the same bead
	conn2, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn2, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w2",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 2, 1*time.Second)

	// Send RECONNECT message claiming oro-test1 (the bug: this overrides w1's assignment)
	sendMsg(t, conn2, protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			BeadID: "oro-test1",
			State:  "running",
		},
	})

	// Give reconnect time to process
	time.Sleep(100 * time.Millisecond)

	// CRITICAL ASSERTIONS: Only ONE worker should be assigned to oro-test1
	d.mu.Lock()
	busyCount := 0
	var assignedWorkerIDs []string
	for id, w := range d.workers {
		if w.beadID == "oro-test1" && w.state == protocol.WorkerBusy {
			busyCount++
			assignedWorkerIDs = append(assignedWorkerIDs, id)
		}
	}
	d.mu.Unlock()

	if busyCount != 1 {
		t.Errorf("Race condition detected: %d workers assigned to oro-test1 (workers: %v), expected exactly 1",
			busyCount, assignedWorkerIDs)
	}

	// ASSERTION 2: w1 should still be the assigned worker (it was assigned first)
	d.mu.Lock()
	w1StillAssigned := d.workers["w1"].beadID == "oro-test1" && d.workers["w1"].state == protocol.WorkerBusy
	w2StolenBead := d.workers["w2"].beadID == "oro-test1" && d.workers["w2"].state == protocol.WorkerBusy
	d.mu.Unlock()

	if !w1StillAssigned {
		t.Error("worker1 lost its assignment to oro-test1 after w2 reconnect — w1 should retain the bead")
	}

	if w2StolenBead {
		t.Error("worker2 stole oro-test1 from worker1 via reconnect — this is the oro-ovpc race condition")
	}
}
