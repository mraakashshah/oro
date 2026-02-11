package dispatcher //nolint:testpackage // white-box test needs internal access

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"

	"oro/pkg/ops"
)

// TestReservationPattern_HeartbeatSkipsReserved verifies that checkHeartbeats
// does NOT delete a worker in WorkerReserved state, even when the heartbeat
// timeout has been exceeded. This is the core invariant of the two-phase
// reservation pattern: reserved workers are immune to heartbeat reaping so
// that the I/O window (memory.ForPrompt) cannot silently lose the assignment.
func TestReservationPattern_HeartbeatSkipsReserved(t *testing.T) {
	d, _, _, esc, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	go drainConn(clientConn)

	workerID := "reserved-heartbeat-worker"

	// Seed the worker directly with WorkerReserved state and an old lastSeen
	// that exceeds the heartbeat timeout.
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     serverConn,
		state:    WorkerReserved,
		beadID:   "reserved-bead",
		worktree: "/tmp/reserved-wt",
		model:    "test-model",
		lastSeen: d.nowFunc().Add(-2 * d.cfg.HeartbeatTimeout), // well past timeout
		encoder:  json.NewEncoder(serverConn),
	}
	d.mu.Unlock()

	// Run checkHeartbeats — the reserved worker must survive.
	d.checkHeartbeats(context.Background())

	// Verify the worker still exists.
	st, beadID, ok := d.WorkerInfo(workerID)
	if !ok {
		t.Fatal("checkHeartbeats deleted a WorkerReserved worker; reservation must protect against heartbeat reaping")
	}
	if st != WorkerReserved {
		t.Fatalf("expected WorkerReserved, got %s", st)
	}
	if beadID != "reserved-bead" {
		t.Fatalf("expected beadID=reserved-bead, got %s", beadID)
	}

	// No escalation should have been triggered.
	if msgs := esc.Messages(); len(msgs) > 0 {
		t.Fatalf("expected no escalation for reserved worker, got %d: %v", len(msgs), msgs)
	}
}

// TestReservationPattern_RegisterWorkerUsesReserved verifies that registerWorker
// sets the worker to WorkerReserved before unlocking for memory.ForPrompt, and
// transitions to WorkerBusy after re-acquiring the lock. During the unlock
// window, checkHeartbeats must NOT delete the reserved worker.
func TestReservationPattern_RegisterWorkerUsesReserved(t *testing.T) {
	d, _, _, esc, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	go drainConn(clientConn)

	spy := &spyConn{Conn: serverConn}
	spy.armed.Store(true)

	workerID := "reservation-register-worker"

	// stateObserved captures the worker state during the unlock window.
	var stateObserved WorkerState
	var stateOK bool

	unlockDone := make(chan struct{})

	d.testUnlockHook = func() {
		// While the lock is released, inspect the worker state from another
		// goroutine perspective. Since the lock is released, we can grab it.
		d.mu.Lock()
		w, exists := d.workers[workerID]
		if exists {
			stateObserved = w.state
			stateOK = true
		}
		d.mu.Unlock()

		// Also run checkHeartbeats to prove it doesn't kill the reserved worker.
		// Force the worker to look timed-out.
		d.mu.Lock()
		if w, exists := d.workers[workerID]; exists {
			w.lastSeen = d.nowFunc().Add(-2 * d.cfg.HeartbeatTimeout)
		}
		d.mu.Unlock()

		d.checkHeartbeats(context.Background())

		close(unlockDone)
	}

	// Seed a pending handoff so registerWorker enters the h != nil path.
	d.mu.Lock()
	d.pendingHandoffs["reservation-bead"] = &pendingHandoff{
		beadID:   "reservation-bead",
		worktree: "/tmp/reservation-wt",
		model:    "test-model",
	}
	d.mu.Unlock()

	d.registerWorker(workerID, spy)
	<-unlockDone

	d.testUnlockHook = nil

	// The state during the unlock window must have been WorkerReserved.
	if !stateOK {
		t.Fatal("worker was not found in the map during the unlock window")
	}
	if stateObserved != WorkerReserved {
		t.Fatalf("expected WorkerReserved during unlock window, got %s", stateObserved)
	}

	// After registerWorker completes, the worker must exist and be WorkerBusy.
	st, _, ok := d.WorkerInfo(workerID)
	if !ok {
		t.Fatal("worker was deleted despite being reserved; reservation pattern failed")
	}
	if st != WorkerBusy {
		t.Fatalf("expected WorkerBusy after registerWorker completes, got %s", st)
	}

	// ASSIGN must have been sent (spy was armed from the start).
	if n := spy.writeCalled.Load(); n == 0 {
		t.Fatal("registerWorker did not send ASSIGN; expected at least 1 Write")
	}

	// No escalation should have been triggered (worker survived heartbeat check).
	if msgs := esc.Messages(); len(msgs) > 0 {
		t.Fatalf("expected no escalation, got %d: %v", len(msgs), msgs)
	}
}

// TestReservationPattern_HandleQGFailureUsesReserved verifies that
// handleQGFailure sets the worker to WorkerReserved before releasing the lock
// for memory.ForPrompt I/O, preventing checkHeartbeats from deleting it.
func TestReservationPattern_HandleQGFailureUsesReserved(t *testing.T) {
	d, _, _, esc, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	go drainConn(clientConn)

	spy := &spyConn{Conn: serverConn}
	spy.armed.Store(true)

	workerID := "reservation-qg-worker"

	var stateObserved WorkerState
	var stateOK bool
	unlockDone := make(chan struct{})

	d.testUnlockHook = func() {
		d.mu.Lock()
		w, exists := d.workers[workerID]
		if exists {
			stateObserved = w.state
			stateOK = true
		}
		d.mu.Unlock()

		// Force heartbeat timeout and run check.
		d.mu.Lock()
		if w, exists := d.workers[workerID]; exists {
			w.lastSeen = d.nowFunc().Add(-2 * d.cfg.HeartbeatTimeout)
		}
		d.mu.Unlock()

		d.checkHeartbeats(context.Background())

		close(unlockDone)
	}

	// Seed the worker.
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     spy,
		state:    WorkerBusy,
		beadID:   "reservation-qg-bead",
		worktree: "/tmp/reservation-qg-wt",
		model:    "test-model",
		lastSeen: d.nowFunc(),
		encoder:  json.NewEncoder(spy),
	}
	d.attemptCounts["reservation-qg-bead"] = 0
	d.mu.Unlock()

	d.handleQGFailure(context.Background(), workerID, "reservation-qg-bead", "qg output")
	<-unlockDone

	d.testUnlockHook = nil

	// During the unlock window, the worker must have been in WorkerReserved state.
	if !stateOK {
		t.Fatal("worker was not found in the map during the unlock window")
	}
	if stateObserved != WorkerReserved {
		t.Fatalf("expected WorkerReserved during unlock window, got %s", stateObserved)
	}

	// After completion, the worker must still exist.
	st, _, ok := d.WorkerInfo(workerID)
	if !ok {
		t.Fatal("worker was deleted despite being reserved during QG failure handling")
	}
	if st != WorkerBusy {
		t.Fatalf("expected WorkerBusy after handleQGFailure, got %s", st)
	}

	// No escalation should have been triggered (worker survived).
	if msgs := esc.Messages(); len(msgs) > 0 {
		t.Fatalf("expected no escalation, got %d: %v", len(msgs), msgs)
	}
}

// TestReservationPattern_HandleReviewResultUsesReserved verifies that
// handleReviewResult (rejected path) protects the worker with WorkerReserved
// state between the rejection count update and the re-assign lock acquisition.
func TestReservationPattern_HandleReviewResultUsesReserved(t *testing.T) {
	d, _, _, _, _, spawnMock := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	// Make the review ops return rejected verdict.
	spawnMock.mu.Lock()
	spawnMock.verdict = "REJECTED: needs fixes"
	spawnMock.mu.Unlock()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	go drainConn(clientConn)

	spy := &spyConn{Conn: serverConn}
	spy.armed.Store(true)

	workerID := "reservation-review-worker"
	beadID := "reservation-review-bead"

	// Seed the worker as reviewing.
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     spy,
		state:    WorkerReviewing,
		beadID:   beadID,
		worktree: "/tmp/reservation-review-wt",
		model:    "test-model",
		lastSeen: d.nowFunc(),
		encoder:  json.NewEncoder(spy),
	}
	d.mu.Unlock()

	// Create a result channel simulating a rejected review.
	resultCh := make(chan ops.Result, 1)
	resultCh <- ops.Result{
		Verdict:  ops.VerdictRejected,
		Feedback: "test rejection feedback",
	}

	d.handleReviewResult(context.Background(), workerID, beadID, resultCh)

	// After handleReviewResult (rejected), worker must still exist and be busy.
	st, _, ok := d.WorkerInfo(workerID)
	if !ok {
		t.Fatal("worker was deleted during handleReviewResult rejected path")
	}
	if st != WorkerBusy {
		t.Fatalf("expected WorkerBusy after review rejection re-assign, got %s", st)
	}

	// ASSIGN must have been sent.
	if n := spy.writeCalled.Load(); n == 0 {
		t.Fatal("handleReviewResult did not send ASSIGN after rejection")
	}
}

// TestReservationPattern_ConcurrentHeartbeatDuringRegister is the full
// integration scenario: registerWorker with a pending handoff races against
// checkHeartbeats. Without the reservation pattern, the heartbeat checker
// would delete the worker during the memory.ForPrompt unlock window, losing
// the bead assignment silently. With WorkerReserved, the worker survives.
func TestReservationPattern_ConcurrentHeartbeatDuringRegister(t *testing.T) {
	d, _, _, esc, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	go drainConn(clientConn)

	spy := &spyConn{Conn: serverConn}
	workerID := "concurrent-hb-worker"

	// Channels for synchronizing the race.
	unlocked := make(chan struct{})
	heartbeatDone := make(chan struct{})

	d.testUnlockHook = func() {
		close(unlocked)
		<-heartbeatDone
	}

	// Seed a pending handoff.
	d.mu.Lock()
	d.pendingHandoffs["concurrent-hb-bead"] = &pendingHandoff{
		beadID:   "concurrent-hb-bead",
		worktree: "/tmp/concurrent-hb-wt",
		model:    "test-model",
	}
	d.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: registerWorker (will unlock for memory.ForPrompt).
	go func() {
		defer wg.Done()
		d.registerWorker(workerID, spy)
	}()

	// Goroutine 2: wait for unlock, force heartbeat timeout, run checkHeartbeats.
	go func() {
		defer wg.Done()
		<-unlocked

		// Force the worker to look timed-out.
		d.mu.Lock()
		if w, exists := d.workers[workerID]; exists {
			w.lastSeen = d.nowFunc().Add(-2 * d.cfg.HeartbeatTimeout)
		}
		d.mu.Unlock()

		d.checkHeartbeats(context.Background())

		// Arm the spy AFTER heartbeat check.
		spy.armed.Store(true)

		close(heartbeatDone)
	}()

	wg.Wait()

	d.testUnlockHook = nil

	// The worker must still exist — reservation protected it.
	st, _, ok := d.WorkerInfo(workerID)
	if !ok {
		t.Fatal("worker was deleted by checkHeartbeats during unlock window; " +
			"WorkerReserved state should protect against this")
	}
	if st != WorkerBusy {
		t.Fatalf("expected WorkerBusy after registerWorker, got %s", st)
	}

	// ASSIGN must have been sent.
	if n := spy.writeCalled.Load(); n == 0 {
		t.Fatal("registerWorker did not send ASSIGN after reservation survived heartbeat check")
	}

	// No escalation for the reserved worker.
	if msgs := esc.Messages(); len(msgs) > 0 {
		t.Fatalf("expected no escalation, got %d: %v", len(msgs), msgs)
	}
}

// TestReservationPattern_InvalidReservationAfterRelock verifies that if a
// worker's reservation is somehow invalidated (state changed) between unlock
// and relock, the function detects this and does not proceed with the
// assignment.
func TestReservationPattern_InvalidReservationAfterRelock(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	go drainConn(clientConn)

	spy := &spyConn{Conn: serverConn}
	workerID := "invalidated-reservation-worker"

	unlocked := make(chan struct{})
	tampered := make(chan struct{})

	d.testUnlockHook = func() {
		close(unlocked)
		<-tampered
	}

	// Seed a pending handoff.
	d.mu.Lock()
	d.pendingHandoffs["invalidated-bead"] = &pendingHandoff{
		beadID:   "invalidated-bead",
		worktree: "/tmp/invalidated-wt",
		model:    "test-model",
	}
	d.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		d.registerWorker(workerID, spy)
	}()

	// Goroutine 2: tamper with the worker state during the unlock window.
	go func() {
		defer wg.Done()
		<-unlocked
		d.mu.Lock()
		if w, exists := d.workers[workerID]; exists {
			// Change state away from Reserved — simulating unexpected state change.
			w.state = WorkerShuttingDown
		}
		d.mu.Unlock()
		spy.armed.Store(true)
		close(tampered)
	}()

	wg.Wait()

	d.testUnlockHook = nil

	// The spy must NOT have received an ASSIGN since the reservation was invalidated.
	if n := spy.writeCalled.Load(); n > 0 {
		t.Fatalf("registerWorker sent ASSIGN %d time(s) despite invalidated reservation; "+
			"expected 0 writes after reservation was tampered with", n)
	}
}

// compileSentinel ensures WorkerReserved is defined at compile time.
// This test function exists purely to create a compilation dependency on the
// WorkerReserved constant — if it doesn't exist, the test file won't compile.
var _ = WorkerReserved
