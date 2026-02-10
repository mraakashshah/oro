package dispatcher //nolint:testpackage // white-box test needs internal access

import (
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// spyConn wraps a net.Conn and counts Write calls made after an armed flag is set.
type spyConn struct {
	net.Conn
	armed       atomic.Bool
	writeCalled atomic.Int32
}

func (s *spyConn) Write(b []byte) (int, error) {
	if s.armed.Load() {
		s.writeCalled.Add(1)
	}
	return s.Conn.Write(b)
}

// drainConn reads and discards all data from conn until it is closed or errors.
func drainConn(conn net.Conn) {
	buf := make([]byte, 4096)
	for {
		if _, err := conn.Read(buf); err != nil {
			return
		}
	}
}

// TestRegisterWorker_SafeAgainstConcurrentDeletion verifies that registerWorker
// does not call sendToWorker on a stale worker pointer after the worker was
// deleted from the map while the lock was released for memory.ForPrompt.
//
// The race window: registerWorker grabs w := d.workers[id] (under lock),
// unlocks to call ForPrompt, and another goroutine (checkHeartbeats) deletes
// the worker. After re-acquiring the lock, registerWorker must re-check that
// the worker still exists before calling sendToWorker.
func TestRegisterWorker_SafeAgainstConcurrentDeletion(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	// Drain clientConn so pipe writes don't block (the bug path writes to
	// the stale conn while holding the lock, which would deadlock the test
	// if the pipe blocked).
	go drainConn(clientConn)

	spy := &spyConn{Conn: serverConn}

	workerID := "race-worker"

	// unlocked is signalled when registerWorker releases the lock.
	unlocked := make(chan struct{})
	// deleted is signalled after the deleter removes the worker, letting
	// registerWorker proceed to re-acquire the lock.
	deleted := make(chan struct{})

	d.testUnlockHook = func() {
		close(unlocked)
		<-deleted
	}

	// Seed a pending handoff so registerWorker enters the h != nil path.
	d.mu.Lock()
	d.pendingHandoffs["race-bead"] = &pendingHandoff{
		beadID:   "race-bead",
		worktree: "/tmp/race-wt",
		model:    "test-model",
	}
	d.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: call registerWorker with the spy conn.
	go func() {
		defer wg.Done()
		d.registerWorker(workerID, spy)
	}()

	// Goroutine 2: wait for unlock, delete the worker, arm the spy, proceed.
	go func() {
		defer wg.Done()
		<-unlocked
		d.mu.Lock()
		// Delete the worker from the map (simulating checkHeartbeats).
		delete(d.workers, workerID)
		d.mu.Unlock()
		// Arm the spy AFTER deletion. Any Write after this point means
		// sendToWorker was called on a stale worker — the bug.
		spy.armed.Store(true)
		close(deleted)
	}()

	wg.Wait()

	d.testUnlockHook = nil

	if n := spy.writeCalled.Load(); n > 0 {
		t.Fatalf("registerWorker called sendToWorker %d time(s) on a worker "+
			"that was deleted during the unlock window; expected 0", n)
	}
}

// TestHandleQGFailure_SafeAgainstConcurrentDeletion verifies that
// handleQGFailure does not send to a deleted worker. handleQGFailure already
// re-checks worker existence after re-acquiring the lock, so this test
// confirms the existing guard works deterministically.
func TestHandleQGFailure_SafeAgainstConcurrentDeletion(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	go drainConn(clientConn)

	spy := &spyConn{Conn: serverConn}
	workerID := "qg-race-worker"

	unlocked := make(chan struct{})
	deleted := make(chan struct{})

	d.testUnlockHook = func() {
		close(unlocked)
		<-deleted
	}

	// Seed the worker directly.
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     spy,
		state:    WorkerBusy,
		beadID:   "qg-race-bead",
		worktree: "/tmp/qg-race-wt",
		model:    "test-model",
		lastSeen: d.nowFunc(),
		encoder:  json.NewEncoder(spy),
	}
	d.attemptCounts["qg-race-bead"] = 0
	d.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		d.handleQGFailure(t.Context(), workerID, "qg-race-bead", "test failure")
	}()

	go func() {
		defer wg.Done()
		<-unlocked
		d.mu.Lock()
		delete(d.workers, workerID)
		d.mu.Unlock()
		spy.armed.Store(true)
		close(deleted)
	}()

	wg.Wait()

	d.testUnlockHook = nil

	if n := spy.writeCalled.Load(); n > 0 {
		t.Fatalf("handleQGFailure called sendToWorker %d time(s) on a worker "+
			"that was deleted during the unlock window; expected 0", n)
	}
}

// TestRegisterWorker_SendsAssignWhenWorkerSurvives is a positive control:
// registerWorker should send ASSIGN when no concurrent deletion occurs.
func TestRegisterWorker_SendsAssignWhenWorkerSurvives(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	go drainConn(clientConn)

	spy := &spyConn{Conn: serverConn}
	spy.armed.Store(true) // arm from the start

	workerID := "survive-worker"

	// Seed a pending handoff.
	d.mu.Lock()
	d.pendingHandoffs["survive-bead"] = &pendingHandoff{
		beadID:   "survive-bead",
		worktree: "/tmp/survive-wt",
		model:    "test-model",
	}
	d.mu.Unlock()

	// No testUnlockHook — no deletion happens.
	d.registerWorker(workerID, spy)

	// Give a moment for any deferred writes.
	time.Sleep(10 * time.Millisecond)

	if n := spy.writeCalled.Load(); n == 0 {
		t.Fatal("registerWorker did not send ASSIGN when worker survived; expected at least 1 Write")
	}
}
