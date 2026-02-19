package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
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
	t.Parallel()

	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Track how many worktrees are created for oro-test1.
	// If the race condition exists, both assignBead calls will create worktrees.
	var worktreeCreateCount atomic.Int32

	// Wrap worktree Create to add delay and track calls
	wtMgr.createFn = func(_ context.Context, beadID string) (string, string, error) {
		if beadID == "oro-test1" {
			// Simulate slow worktree creation to widen the race window
			time.Sleep(50 * time.Millisecond)
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
	t.Parallel()

	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

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
	waitForWorkers(t, d, 6, 1*time.Second)

	// Give the assign loop time to run tryAssign with W6 idle.
	time.Sleep(200 * time.Millisecond)

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
