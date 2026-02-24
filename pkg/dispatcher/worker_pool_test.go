package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// --- failConn: a net.Conn whose Write always fails ---

type failConn struct {
	mu     sync.Mutex
	closed bool
}

func (f *failConn) Write([]byte) (int, error) {
	return 0, net.ErrClosed
}

func (f *failConn) Read([]byte) (int, error) {
	return 0, net.ErrClosed
}

func (f *failConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}
func (f *failConn) LocalAddr() net.Addr              { return nil }
func (f *failConn) RemoteAddr() net.Addr             { return nil }
func (f *failConn) SetDeadline(time.Time) error      { return nil }
func (f *failConn) SetReadDeadline(time.Time) error  { return nil }
func (f *failConn) SetWriteDeadline(time.Time) error { return nil }

// --- upsertWorker tests ---

// TestUpsertWorker_NewWorkerCreatedWithAllFields verifies that a new worker entry
// is created with id, conn, state=Idle, encoder, and managed flag.
// Kills mutations 7 (skip new worker struct init).
func TestUpsertWorker_NewWorkerCreatedWithAllFields(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	workerID := "new-worker-1"

	d.mu.Lock()
	d.upsertWorker(workerID, conn, true)
	w := d.workers[workerID]
	d.mu.Unlock()

	if w == nil {
		t.Fatal("expected worker to be created")
	}
	if w.id != workerID {
		t.Errorf("worker id = %q, want %q", w.id, workerID)
	}
	if w.conn != conn {
		t.Error("worker conn not set")
	}
	if w.state != protocol.WorkerIdle {
		t.Errorf("worker state = %v, want Idle", w.state)
	}
	if w.encoder == nil {
		t.Error("worker encoder should not be nil")
	}
	if !w.managed {
		t.Error("worker managed should be true")
	}
}

// TestUpsertWorker_NewWorkerUnmanagedFlagFalse verifies that managed=false is stored.
// Kills mutation 7 (skip new worker struct init).
func TestUpsertWorker_NewWorkerUnmanagedFlagFalse(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	workerID := "unmanaged-worker"

	d.mu.Lock()
	d.upsertWorker(workerID, conn, false)
	w := d.workers[workerID]
	d.mu.Unlock()

	if w == nil {
		t.Fatal("expected worker to be created")
	}
	if w.managed {
		t.Error("worker managed should be false when unmanaged")
	}
}

// TestUpsertWorker_ReconnectUpdatesConnAndEncoder verifies that on reconnect
// the conn and encoder fields are updated to the new connection.
// Kills mutations 6, 71, 73 (skip conn/encoder update on reconnect).
func TestUpsertWorker_ReconnectUpdatesConnAndEncoder(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn1 := newMockConn()
	conn2 := newMockConn()
	workerID := "reconnect-worker"

	d.mu.Lock()
	d.upsertWorker(workerID, conn1, false)
	d.mu.Unlock()

	d.mu.Lock()
	d.upsertWorker(workerID, conn2, false)
	w := d.workers[workerID]
	d.mu.Unlock()

	if w.conn != conn2 {
		t.Error("worker conn should be updated to conn2 on reconnect")
	}
	if w.encoder == nil {
		t.Error("encoder should not be nil after reconnect")
	}
}

// TestUpsertWorker_ReconnectUpdatesLastSeen verifies that lastSeen is refreshed
// on reconnect.
// Kills mutation 72 (skip lastSeen update on reconnect).
func TestUpsertWorker_ReconnectUpdatesLastSeen(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	fixedTime1 := time.Now().Add(-5 * time.Second)
	fixedTime2 := time.Now()

	callCount := 0
	d.nowFunc = func() time.Time {
		callCount++
		if callCount == 1 {
			return fixedTime1
		}
		return fixedTime2
	}

	conn1 := newMockConn()
	conn2 := newMockConn()
	workerID := "lastseen-worker"

	d.mu.Lock()
	d.upsertWorker(workerID, conn1, false)
	d.mu.Unlock()

	d.mu.Lock()
	d.upsertWorker(workerID, conn2, false)
	w := d.workers[workerID]
	d.mu.Unlock()

	if !w.lastSeen.Equal(fixedTime2) {
		t.Errorf("lastSeen = %v, want %v", w.lastSeen, fixedTime2)
	}
}

// TestUpsertWorker_ReconnectPreservesManagedFlag verifies that managed=true
// is preserved on reconnect even when managed=false is passed.
// Kills mutation 8 (skip managed flag preservation on reconnect).
func TestUpsertWorker_ReconnectPreservesManagedFlag(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn1 := newMockConn()
	conn2 := newMockConn()
	workerID := "managed-preserve-worker"

	d.mu.Lock()
	d.upsertWorker(workerID, conn1, true) // initially managed
	d.mu.Unlock()

	d.mu.Lock()
	d.upsertWorker(workerID, conn2, false) // reconnect without managed
	w := d.workers[workerID]
	d.mu.Unlock()

	if !w.managed {
		t.Error("managed flag should remain true on reconnect even if managed=false passed")
	}
}

// TestUpsertWorker_ReconnectSetsNewManagedFlag verifies that managed becomes true
// when reconnect passes managed=true even if previously false.
// Kills mutation 8 (skip managed flag update).
func TestUpsertWorker_ReconnectSetsNewManagedFlag(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn1 := newMockConn()
	conn2 := newMockConn()
	workerID := "managed-set-worker"

	d.mu.Lock()
	d.upsertWorker(workerID, conn1, false) // initially unmanaged
	d.mu.Unlock()

	d.mu.Lock()
	d.upsertWorker(workerID, conn2, true) // reconnect as managed
	w := d.workers[workerID]
	d.mu.Unlock()

	if !w.managed {
		t.Error("managed flag should be set to true on reconnect with managed=true")
	}
}

// --- registerWorker tests ---

// TestRegisterWorker_ConsumesPendingManagedID verifies that a pending managed ID
// is consumed from the map during registerWorker.
// Kills mutation 9 (skip delete from pendingManagedIDs).
func TestRegisterWorker_ConsumesPendingManagedID(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	workerID := "managed-consume"
	d.mu.Lock()
	d.pendingManagedIDs[workerID] = true
	d.mu.Unlock()

	conn := newMockConn()
	d.registerWorker(workerID, conn)

	d.mu.Lock()
	_, stillPresent := d.pendingManagedIDs[workerID]
	w := d.workers[workerID]
	d.mu.Unlock()

	if stillPresent {
		t.Error("pendingManagedIDs should be consumed after registerWorker")
	}
	if w == nil {
		t.Fatal("worker should be registered")
	}
	if !w.managed {
		t.Error("worker should be marked managed since it was in pendingManagedIDs")
	}
}

// TestRegisterWorker_NoPendingHandoff_NoAssign verifies that a worker is registered
// without any assignment when no pending handoff exists.
func TestRegisterWorker_NoPendingHandoff_NoAssign(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	workerID := "no-handoff-worker"
	d.registerWorker(workerID, conn)

	d.mu.Lock()
	w := d.workers[workerID]
	d.mu.Unlock()

	if w == nil {
		t.Fatal("worker should be registered")
	}
	if w.state != protocol.WorkerIdle {
		t.Errorf("worker state = %v, want Idle when no pending handoff", w.state)
	}
	if len(conn.written) != 0 {
		t.Errorf("no message should be sent, got %d writes", len(conn.written))
	}
}

// TestRegisterWorker_EarlyReturnWhenWorkerGoneAfterRelock verifies mutation 13
// (skip unlock+return when worker is gone after reacquiring lock).
// This tests the guard condition d.workers[id] check after lock re-acquire.
func TestRegisterWorker_EarlyReturnWhenWorkerGoneAfterRelock(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	go drainConn(clientConn)

	workerID := "race-gone-worker"

	// unlocked signals when registerWorker releases the lock
	unlocked := make(chan struct{})
	deleted := make(chan struct{})

	d.testUnlockHook = func() {
		close(unlocked)
		<-deleted
	}

	// Seed a pending handoff so registerWorker enters the h != nil path
	d.mu.Lock()
	d.pendingHandoffs["gone-bead"] = &pendingHandoff{
		beadID:   "gone-bead",
		worktree: "/tmp/gone-wt",
		model:    "test-model",
	}
	d.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		d.registerWorker(workerID, serverConn)
	}()

	go func() {
		defer wg.Done()
		<-unlocked
		d.mu.Lock()
		delete(d.workers, workerID)
		d.mu.Unlock()
		close(deleted)
	}()

	wg.Wait()
	d.testUnlockHook = nil
	// Test succeeds if there's no panic/deadlock — the guard prevents sending to a deleted worker
}

// --- ConnectedWorkers tests ---

// TestConnectedWorkers_ReturnsCorrectCount verifies the count is accurate.
// Kills mutations 93 (skip mu.Lock).
func TestConnectedWorkers_ReturnsCorrectCount(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	if n := d.ConnectedWorkers(); n != 0 {
		t.Errorf("initial count = %d, want 0", n)
	}

	conn := newMockConn()
	d.mu.Lock()
	d.workers["w1"] = &trackedWorker{id: "w1", conn: conn, state: protocol.WorkerIdle, encoder: json.NewEncoder(conn)}
	d.workers["w2"] = &trackedWorker{id: "w2", conn: conn, state: protocol.WorkerBusy, encoder: json.NewEncoder(conn)}
	d.mu.Unlock()

	if n := d.ConnectedWorkers(); n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
}

// --- WorkerInfo tests ---

// TestWorkerInfo_ReturnsCorrectStateAndBeadID verifies WorkerInfo returns proper values.
// Kills mutation 14 (wrong return for missing worker), mutation 95 (skip lock).
func TestWorkerInfo_ReturnsCorrectStateAndBeadID(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	d.mu.Lock()
	d.workers["info-worker"] = &trackedWorker{
		id:     "info-worker",
		conn:   conn,
		state:  protocol.WorkerBusy,
		beadID: "test-bead",
	}
	d.mu.Unlock()

	state, beadID, ok := d.WorkerInfo("info-worker")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if state != protocol.WorkerBusy {
		t.Errorf("state = %v, want Busy", state)
	}
	if beadID != "test-bead" {
		t.Errorf("beadID = %q, want %q", beadID, "test-bead")
	}
}

// TestWorkerInfo_ReturnsFalseForMissingWorker verifies WorkerInfo returns ok=false
// when the worker doesn't exist.
// Kills mutation 14 (remove return "","",false).
func TestWorkerInfo_ReturnsFalseForMissingWorker(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	_, _, ok := d.WorkerInfo("nonexistent")
	if ok {
		t.Error("expected ok=false for nonexistent worker")
	}
}

// --- WorkerModel tests ---

// TestWorkerModel_ReturnsCorrectModel verifies WorkerModel returns the correct model.
// Kills mutation 96 (skip lock).
func TestWorkerModel_ReturnsCorrectModel(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	d.mu.Lock()
	d.workers["model-worker"] = &trackedWorker{
		id:    "model-worker",
		conn:  conn,
		model: "claude-opus",
	}
	d.mu.Unlock()

	model, ok := d.WorkerModel("model-worker")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if model != "claude-opus" {
		t.Errorf("model = %q, want %q", model, "claude-opus")
	}
}

// TestWorkerModel_ReturnsFalseForMissingWorker verifies WorkerModel returns ok=false.
// Kills mutation 15 (remove return "","" false).
func TestWorkerModel_ReturnsFalseForMissingWorker(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	_, ok := d.WorkerModel("nonexistent")
	if ok {
		t.Error("expected ok=false for nonexistent worker")
	}
}

// --- touchProgress tests ---

// TestTouchProgress_UpdatesLastProgress verifies that touchProgress sets lastProgress
// to the current time.
// Kills mutations 16, 97, 98 (skip lastProgress update or lock).
func TestTouchProgress_UpdatesLastProgress(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	fixedTime := time.Now().Add(10 * time.Second)
	d.nowFunc = func() time.Time { return fixedTime }

	conn := newMockConn()
	workerID := "progress-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:    workerID,
		conn:  conn,
		state: protocol.WorkerBusy,
	}
	d.mu.Unlock()

	d.touchProgress(workerID)

	d.mu.Lock()
	got := d.workers[workerID].lastProgress
	d.mu.Unlock()

	if !got.Equal(fixedTime) {
		t.Errorf("lastProgress = %v, want %v", got, fixedTime)
	}
}

// TestTouchProgress_NoopForUnknownWorker verifies no panic for unknown worker.
// Ensures the ok guard works.
func TestTouchProgress_NoopForUnknownWorker(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Should not panic
	d.touchProgress("nonexistent")
}

// --- handleHeartbeat progress tests ---

// TestHeartbeatTouchesProgressForBusyWorker verifies that a heartbeat from a
// busy worker updates lastProgress, preventing the progress timeout from
// killing workers that are legitimately running Claude for >10 min.
func TestHeartbeatTouchesProgressForBusyWorker(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	d.cfg.ProgressTimeout = 1 * time.Second

	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	// Register a busy worker with stale lastProgress (past timeout).
	staleProgress := now.Add(-(d.cfg.ProgressTimeout + time.Second))
	conn := newMockConn()
	workerID := "busy-heartbeating"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerBusy,
		beadID:       "test-bead",
		lastSeen:     now,
		lastProgress: staleProgress,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	// Send heartbeat — should refresh lastProgress for busy worker.
	d.handleHeartbeat(context.Background(), workerID, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID: workerID,
			BeadID:   "test-bead",
		},
	})

	// Verify lastProgress was updated.
	d.mu.Lock()
	got := d.workers[workerID].lastProgress
	d.mu.Unlock()
	if !got.Equal(now) {
		t.Errorf("lastProgress = %v, want %v (heartbeat should refresh progress for busy worker)", got, now)
	}

	// The worker must survive checkHeartbeats.
	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stillPresent := d.workers[workerID]
	d.mu.Unlock()
	if !stillPresent {
		t.Error("busy worker with recent heartbeat should NOT be removed by checkHeartbeats")
	}
}

// TestHeartbeatDoesNotTouchProgressForIdleWorker verifies that heartbeats
// from idle workers do NOT update lastProgress.
func TestHeartbeatDoesNotTouchProgressForIdleWorker(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	staleProgress := now.Add(-5 * time.Minute)
	conn := newMockConn()
	workerID := "idle-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerIdle,
		lastSeen:     now,
		lastProgress: staleProgress,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	// Send heartbeat — should NOT refresh lastProgress for idle worker.
	d.handleHeartbeat(context.Background(), workerID, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID: workerID,
		},
	})

	d.mu.Lock()
	got := d.workers[workerID].lastProgress
	d.mu.Unlock()
	if !got.Equal(staleProgress) {
		t.Errorf("lastProgress = %v, want %v (heartbeat should NOT touch progress for idle worker)", got, staleProgress)
	}
}

// --- checkHeartbeats tests ---

// TestCheckHeartbeats_RemovesStaleIdleWorkers verifies that idle workers with
// stale heartbeats are removed (they are disconnected). Only reserved workers
// are exempt from heartbeat timeout.
func TestCheckHeartbeats_RemovesStaleIdleWorkers(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	conn := newMockConn()
	workerID := "idle-stale"
	// Place worker with lastSeen far in the past — state is Idle
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     conn,
		state:    protocol.WorkerIdle,
		lastSeen: now.Add(-10 * time.Hour), // way past timeout
	}
	d.mu.Unlock()

	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stillPresent := d.workers[workerID]
	d.mu.Unlock()

	if stillPresent {
		t.Error("stale idle worker should be removed by checkHeartbeats")
	}
}

// TestCheckHeartbeats_SkipsReservedWorkers verifies that reserved workers are not timed out.
// Kills mutations 17, 39 (skip reserved check).
func TestCheckHeartbeats_SkipsReservedWorkers(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	conn := newMockConn()
	workerID := "reserved-skipped"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     conn,
		state:    protocol.WorkerReserved,
		lastSeen: now.Add(-10 * time.Hour), // way past timeout
	}
	d.mu.Unlock()

	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stillPresent := d.workers[workerID]
	d.mu.Unlock()

	if !stillPresent {
		t.Error("reserved worker should not be removed by checkHeartbeats")
	}
}

// TestCheckHeartbeats_RemovesDeadBusyWorker verifies that a busy worker exceeding
// heartbeat timeout is removed.
// Kills mutations 18, 33, 102, 106, 107.
func TestCheckHeartbeats_RemovesDeadBusyWorker(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	now := time.Now()
	timeout := d.cfg.HeartbeatTimeout

	conn := newMockConn()
	workerID := "dead-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     conn,
		state:    protocol.WorkerBusy,
		beadID:   "dead-bead",
		lastSeen: now.Add(-(timeout + time.Second)), // past timeout
		encoder:  json.NewEncoder(conn),
	}
	d.mu.Unlock()

	// Override time to "now"
	d.nowFunc = func() time.Time { return now }

	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stillPresent := d.workers[workerID]
	d.mu.Unlock()

	if stillPresent {
		t.Error("dead worker (heartbeat timeout) should be removed from map")
	}
	if !conn.closed {
		t.Error("dead worker connection should be closed")
	}
}

// TestCheckHeartbeats_StrictlyGreaterThanTimeout verifies that a worker at exactly
// the timeout is NOT removed (must be strictly greater than).
// Kills mutation 33 (> vs >=).
func TestCheckHeartbeats_StrictlyGreaterThanTimeout(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	timeout := d.cfg.HeartbeatTimeout
	now := time.Now()

	// Set lastSeen exactly at the timeout boundary — should NOT trigger
	lastSeen := now.Add(-timeout)

	conn := newMockConn()
	workerID := "boundary-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     conn,
		state:    protocol.WorkerBusy,
		lastSeen: lastSeen,
	}
	d.mu.Unlock()

	d.nowFunc = func() time.Time { return now }
	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stillPresent := d.workers[workerID]
	d.mu.Unlock()

	if !stillPresent {
		t.Error("worker at exactly timeout boundary should NOT be removed (need strictly >)")
	}
}

// TestCheckHeartbeats_DetectsStuckWorker verifies that a busy worker with no progress
// beyond ProgressTimeout is treated as stuck.
// Kills mutations 19, 34, 40, 41, 42, 43, 108, 110, 111.
func TestCheckHeartbeats_DetectsStuckWorker(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	// Use a short ProgressTimeout
	d.cfg.ProgressTimeout = 100 * time.Millisecond

	now := time.Now()
	// lastSeen is recent (within heartbeat timeout) — not dead
	// lastProgress is old (past progress timeout) — stuck
	progressTime := now.Add(-(d.cfg.ProgressTimeout + time.Second))

	conn := newMockConn()
	workerID := "stuck-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerBusy,
		beadID:       "stuck-bead",
		lastSeen:     now.Add(-10 * time.Millisecond), // recent heartbeat
		lastProgress: progressTime,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	d.nowFunc = func() time.Time { return now }
	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stillPresent := d.workers[workerID]
	d.mu.Unlock()

	if stillPresent {
		t.Error("stuck worker (progress timeout) should be removed")
	}
	if !conn.closed {
		t.Error("stuck worker connection should be closed")
	}
}

// TestCheckHeartbeats_StuckRequiresNonZeroLastProgress verifies that a busy worker
// with zero lastProgress is not treated as stuck.
// Kills mutation 43 (skip lastProgress.IsZero() check).
func TestCheckHeartbeats_StuckRequiresNonZeroLastProgress(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	d.cfg.ProgressTimeout = 100 * time.Millisecond

	now := time.Now()
	conn := newMockConn()
	workerID := "zero-progress-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerBusy,
		beadID:       "test-bead",
		lastSeen:     now.Add(-10 * time.Millisecond),
		lastProgress: time.Time{}, // zero time — should not trigger stuck
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	d.nowFunc = func() time.Time { return now }
	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stillPresent := d.workers[workerID]
	d.mu.Unlock()

	if !stillPresent {
		t.Error("worker with zero lastProgress should not be removed as stuck")
	}
}

// TestCheckHeartbeats_StuckProgressTimeout_GreaterThan verifies the strictly >
// comparison for progress timeout.
// Kills mutation 34 (> vs >=).
func TestCheckHeartbeats_StuckProgressTimeout_GreaterThan(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	d.cfg.ProgressTimeout = 100 * time.Millisecond

	now := time.Now()
	progressTime := now.Add(-d.cfg.ProgressTimeout) // exactly at timeout — should NOT trigger

	conn := newMockConn()
	workerID := "stuck-boundary-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerBusy,
		beadID:       "test-bead",
		lastSeen:     now.Add(-10 * time.Millisecond),
		lastProgress: progressTime,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	d.nowFunc = func() time.Time { return now }
	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stillPresent := d.workers[workerID]
	d.mu.Unlock()

	if !stillPresent {
		t.Error("worker at exactly progress timeout boundary should NOT be stuck (need strictly >)")
	}
}

// --- sendToWorker tests ---

// TestSendToWorker_SuccessfulSendReturnsNil verifies that a successful write returns nil error.
func TestSendToWorker_SuccessfulSendReturnsNil(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	w := &trackedWorker{
		id:      "send-worker",
		conn:    conn,
		encoder: json.NewEncoder(conn),
	}
	d.mu.Lock()
	d.workers[w.id] = w
	d.mu.Unlock()

	msg := protocol.Message{Type: protocol.MsgHeartbeat}
	d.mu.Lock()
	err := d.sendToWorker(w, msg)
	d.mu.Unlock()

	if err != nil {
		t.Errorf("sendToWorker returned error: %v", err)
	}
	if len(conn.written) == 0 {
		t.Error("expected at least one write to conn")
	}
}

// TestSendToWorker_BuffersMessageOnFailure verifies that a failed write buffers
// the message in pendingMsgs.
// Kills mutations 23, 118 (skip buffering).
func TestSendToWorker_BuffersMessageOnFailure(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	fc := &failConn{}
	w := &trackedWorker{
		id:      "fail-worker",
		conn:    fc,
		encoder: json.NewEncoder(fc),
	}
	d.mu.Lock()
	d.workers[w.id] = w
	d.mu.Unlock()

	msg := protocol.Message{Type: protocol.MsgHeartbeat}
	d.mu.Lock()
	err := d.sendToWorker(w, msg)
	d.mu.Unlock()

	if err == nil {
		t.Error("expected error from failConn")
	}
	d.mu.Lock()
	pendingCount := len(w.pendingMsgs)
	d.mu.Unlock()

	if pendingCount != 1 {
		t.Errorf("expected 1 pending message, got %d", pendingCount)
	}
}

// TestSendToWorker_RemovesWorkerAtMaxPendingMessages verifies that exceeding
// maxPendingMessages causes the worker to be removed.
// Kills mutations 24, 35, 59, 66, 119, 120.
func TestSendToWorker_RemovesWorkerAtMaxPendingMessages(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	fc := &failConn{}
	w := &trackedWorker{
		id:      "overflow-worker",
		conn:    fc,
		encoder: json.NewEncoder(fc),
	}
	d.mu.Lock()
	d.workers[w.id] = w
	d.mu.Unlock()

	msg := protocol.Message{Type: protocol.MsgHeartbeat}

	// Send maxPendingMessages + 1 to trigger removal
	var lastErr error
	d.mu.Lock()
	for i := 0; i <= maxPendingMessages; i++ {
		lastErr = d.sendToWorker(w, msg)
	}
	d.mu.Unlock()

	if lastErr == nil {
		t.Error("expected WorkerUnreachableError after exceeding pending message limit")
	}
	var unreachable *protocol.WorkerUnreachableError
	if !isWorkerUnreachable(lastErr, &unreachable) {
		t.Errorf("expected WorkerUnreachableError, got %T: %v", lastErr, lastErr)
	}

	d.mu.Lock()
	_, stillPresent := d.workers["overflow-worker"]
	d.mu.Unlock()

	if stillPresent {
		t.Error("worker should be removed after exceeding maxPendingMessages")
	}
}

// TestSendToWorker_BuffersExactlyMaxMessages verifies that at exactly maxPendingMessages
// the worker is NOT removed (needs strictly >).
// Kills mutation 35 (>= vs >).
func TestSendToWorker_BuffersExactlyMaxMessages(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	fc := &failConn{}
	w := &trackedWorker{
		id:      "exact-max-worker",
		conn:    fc,
		encoder: json.NewEncoder(fc),
	}
	d.mu.Lock()
	d.workers[w.id] = w
	d.mu.Unlock()

	msg := protocol.Message{Type: protocol.MsgHeartbeat}

	// Send exactly maxPendingMessages — should NOT remove the worker
	d.mu.Lock()
	for i := 0; i < maxPendingMessages; i++ {
		d.sendToWorker(w, msg) //nolint:errcheck,gosec
	}
	d.mu.Unlock()

	d.mu.Lock()
	_, stillPresent := d.workers["exact-max-worker"]
	d.mu.Unlock()

	if !stillPresent {
		t.Errorf("worker with exactly %d pending msgs should NOT be removed yet (need strictly > %d)", maxPendingMessages, maxPendingMessages)
	}
}

// isWorkerUnreachable checks if err is a *protocol.WorkerUnreachableError.
func isWorkerUnreachable(err error, out **protocol.WorkerUnreachableError) bool {
	var e *protocol.WorkerUnreachableError
	if !errors.As(err, &e) {
		return false
	}
	if out != nil {
		*out = e
	}
	return true
}

// --- GracefulShutdownWorker tests ---

// TestGracefulShutdownWorker_NoopForMissingWorker verifies early return for
// unknown worker.
// Kills mutation 25 (skip unlock+return when worker not found).
func TestGracefulShutdownWorker_NoopForMissingWorker(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Should not panic or deadlock
	d.GracefulShutdownWorker("nonexistent", 100*time.Millisecond)
}

// TestGracefulShutdownWorker_SendsPrepareShutdown verifies that
// GracefulShutdownWorker sends PREPARE_SHUTDOWN to the worker.
// Kills mutations 123, 128.
func TestGracefulShutdownWorker_SendsPrepareShutdown(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	go drainConn(clientConn)

	spy := &spyConn{Conn: serverConn}
	spy.armed.Store(true)

	workerID := "shutdown-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:      workerID,
		conn:    spy,
		state:   protocol.WorkerBusy,
		encoder: json.NewEncoder(spy),
	}
	d.mu.Unlock()

	d.GracefulShutdownWorker(workerID, 200*time.Millisecond)

	// Verify PREPARE_SHUTDOWN was sent
	waitFor(t, func() bool {
		return spy.writeCalled.Load() > 0
	}, 2*time.Second)

	if spy.writeCalled.Load() == 0 {
		t.Error("expected PREPARE_SHUTDOWN message to be sent")
	}
}

// TestGracefulShutdownWorker_CancelsPreviousShutdown verifies that calling
// GracefulShutdownWorker again cancels the previous shutdown goroutine.
// Kills mutation 26 (skip calling previous shutdownCancel).
func TestGracefulShutdownWorker_CancelsPreviousShutdown(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	go drainConn(clientConn)

	spy := &spyConn{Conn: serverConn}

	workerID := "double-shutdown-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:      workerID,
		conn:    spy,
		state:   protocol.WorkerBusy,
		encoder: json.NewEncoder(spy),
	}
	d.mu.Unlock()

	var cancelled bool
	prevCancel := func() {
		cancelled = true
	}
	d.mu.Lock()
	d.workers[workerID].shutdownCancel = prevCancel
	d.mu.Unlock()

	d.GracefulShutdownWorker(workerID, 200*time.Millisecond)

	if !cancelled {
		t.Error("previous shutdown cancel should have been called")
	}
}

// TestGracefulShutdownWorker_SetsShutdownCancel verifies that shutdownCancel
// is set on the worker.
// Kills mutation 122 (skip setting shutdownCancel).
func TestGracefulShutdownWorker_SetsShutdownCancel(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	go drainConn(clientConn)

	workerID := "cancel-set-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:      workerID,
		conn:    serverConn,
		state:   protocol.WorkerBusy,
		encoder: json.NewEncoder(serverConn),
	}
	d.mu.Unlock()

	d.GracefulShutdownWorker(workerID, 500*time.Millisecond)

	d.mu.Lock()
	w, ok := d.workers[workerID]
	var hasCancel bool
	if ok {
		hasCancel = w.shutdownCancel != nil
	}
	d.mu.Unlock()

	if ok && !hasCancel {
		t.Error("shutdownCancel should be set on worker after GracefulShutdownWorker")
	}
}

// --- checkShutdownApproved tests ---

// TestCheckShutdownApproved_ReturnsFalseForMissingWorker verifies that a missing
// worker returns false.
// Kills mutation 30 (remove return false when worker not found).
func TestCheckShutdownApproved_ReturnsFalseForMissingWorker(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	result := d.checkShutdownApproved("nonexistent")
	if result {
		t.Error("expected false for nonexistent worker")
	}
}

// TestCheckShutdownApproved_TrueWhenFlagSet verifies that the explicit
// shutdownApproved flag means approved.
// Kills mutation 44 (skip approved check).
func TestCheckShutdownApproved_TrueWhenFlagSet(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	workerID := "idle-approved"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:               workerID,
		conn:             conn,
		state:            protocol.WorkerIdle,
		shutdownApproved: true,
	}
	d.mu.Unlock()

	result := d.checkShutdownApproved(workerID)
	if !result {
		t.Error("expected true when shutdownApproved is set")
	}
}

// TestCheckShutdownApproved_FalseWhenWorkerIsBusy verifies that non-Idle state
// means not approved.
// Kills mutation 44 (true instead of approved).
func TestCheckShutdownApproved_FalseWhenWorkerIsBusy(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	workerID := "busy-not-approved"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:    workerID,
		conn:  conn,
		state: protocol.WorkerBusy,
	}
	d.mu.Unlock()

	result := d.checkShutdownApproved(workerID)
	if result {
		t.Error("expected false when worker is Busy")
	}
}

// TestCheckShutdownApproved_ClearsShutdownCancelOnApproval verifies that
// shutdownCancel is set to nil after approval.
// Kills mutation 31 (skip clearing shutdownCancel), mutation 45 (skip nil check).
func TestCheckShutdownApproved_ClearsShutdownCancelOnApproval(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	workerID := "cancel-clear"
	cancelCalled := false
	cancelFn := func() { cancelCalled = true }

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:               workerID,
		conn:             conn,
		state:            protocol.WorkerIdle,
		shutdownApproved: true,
		shutdownCancel:   cancelFn,
	}
	d.mu.Unlock()

	result := d.checkShutdownApproved(workerID)
	if !result {
		t.Error("expected true when shutdownApproved is set")
	}

	d.mu.Lock()
	w := d.workers[workerID]
	hasCancel := w != nil && w.shutdownCancel != nil
	d.mu.Unlock()

	if hasCancel {
		t.Error("shutdownCancel should be cleared after approval")
	}
	_ = cancelCalled // Cancel is NOT called here — it is just nilled
}

// TestCheckShutdownApproved_DoesNotClearCancelWhenNotApproved verifies that
// shutdownCancel remains when not approved.
// Kills mutation 45 (skip nil check -> always clear).
func TestCheckShutdownApproved_DoesNotClearCancelWhenNotApproved(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	workerID := "busy-with-cancel"
	cancelFn := func() {}

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:             workerID,
		conn:           conn,
		state:          protocol.WorkerBusy,
		shutdownCancel: cancelFn,
	}
	d.mu.Unlock()

	result := d.checkShutdownApproved(workerID)
	if result {
		t.Error("expected false for busy worker")
	}

	d.mu.Lock()
	w := d.workers[workerID]
	hasCancel := w != nil && w.shutdownCancel != nil
	d.mu.Unlock()

	if !hasCancel {
		t.Error("shutdownCancel should NOT be cleared when not approved")
	}
}

// --- handleShutdownTimeout tests ---

// TestHandleShutdownTimeout_SendsHardShutdown verifies that a hard SHUTDOWN
// is sent after timeout.
// Kills mutations 29, 131.
func TestHandleShutdownTimeout_SendsHardShutdown(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	go drainConn(clientConn)

	spy := &spyConn{Conn: serverConn}
	spy.armed.Store(true)

	workerID := "timeout-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:      workerID,
		conn:    spy,
		state:   protocol.WorkerBusy,
		encoder: json.NewEncoder(spy),
	}
	d.mu.Unlock()

	d.handleShutdownTimeout(workerID)

	if spy.writeCalled.Load() == 0 {
		t.Error("expected hard SHUTDOWN message to be sent")
	}
}

// TestHandleShutdownTimeout_ResetsWorkerState verifies that the worker state
// is reset to Idle and beadID cleared.
// Kills mutations 132, 133, 134.
func TestHandleShutdownTimeout_ResetsWorkerState(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	workerID := "reset-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:             workerID,
		conn:           conn,
		state:          protocol.WorkerBusy,
		beadID:         "some-bead",
		shutdownCancel: func() {},
		encoder:        json.NewEncoder(conn),
	}
	d.mu.Unlock()

	d.handleShutdownTimeout(workerID)

	d.mu.Lock()
	w := d.workers[workerID]
	d.mu.Unlock()

	if w == nil {
		t.Fatal("worker should still exist after handleShutdownTimeout")
	}
	if w.state != protocol.WorkerIdle {
		t.Errorf("state = %v, want Idle", w.state)
	}
	if w.beadID != "" {
		t.Errorf("beadID = %q, want empty", w.beadID)
	}
	if w.shutdownCancel != nil {
		t.Error("shutdownCancel should be nil after timeout")
	}
}

// TestHandleShutdownTimeout_NoopForMissingWorker verifies no panic for missing worker.
func TestHandleShutdownTimeout_NoopForMissingWorker(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Should not panic
	d.handleShutdownTimeout("nonexistent")
}

// --- shutdownWaitForWorkers tests ---

// TestShutdownWaitForWorkers_ExitsWhenNoWorkers verifies that the function returns
// immediately when there are no workers.
// Kills mutation 62, 69 (wrong count comparisons).
func TestShutdownWaitForWorkers_ExitsWhenNoWorkers(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	done := make(chan struct{})
	go func() {
		d.shutdownWaitForWorkers()
		close(done)
	}()

	select {
	case <-done:
		// Good — returned promptly
	case <-time.After(2 * time.Second):
		t.Fatal("shutdownWaitForWorkers should return immediately when no workers")
	}
}

// TestShutdownWaitForWorkers_ForceClosesAfterTimeout verifies that remaining
// connections are force-closed after ShutdownTimeout.
// Kills mutations 137, 138.
func TestShutdownWaitForWorkers_ForceClosesAfterTimeout(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Use a short shutdown timeout
	d.cfg.ShutdownTimeout = 150 * time.Millisecond

	conn := newMockConn()
	workerID := "lingering-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:    workerID,
		conn:  conn,
		state: protocol.WorkerBusy,
	}
	d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		d.shutdownWaitForWorkers()
		close(done)
	}()

	select {
	case <-done:
		// Good
	case <-time.After(2 * time.Second):
		t.Fatal("shutdownWaitForWorkers should return after ShutdownTimeout")
	}

	if !conn.closed {
		t.Error("lingering worker connection should be force-closed after timeout")
	}

	d.mu.Lock()
	_, stillPresent := d.workers[workerID]
	d.mu.Unlock()

	if stillPresent {
		t.Error("lingering worker should be removed after force-close")
	}
}

// TestShutdownWaitForWorkers_ExitsWhenWorkersDrain verifies that it exits early
// when all workers disconnect (without waiting for timeout).
// Kills mutation 62, 69.
func TestShutdownWaitForWorkers_ExitsWhenWorkersDrain(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	d.cfg.ShutdownTimeout = 10 * time.Second // long timeout

	conn := newMockConn()
	workerID := "draining-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:    workerID,
		conn:  conn,
		state: protocol.WorkerBusy,
	}
	d.mu.Unlock()

	// Remove the worker shortly after starting shutdownWaitForWorkers
	go func() {
		time.Sleep(100 * time.Millisecond)
		d.mu.Lock()
		delete(d.workers, workerID)
		d.mu.Unlock()
	}()

	done := make(chan struct{})
	go func() {
		d.shutdownWaitForWorkers()
		close(done)
	}()

	select {
	case <-done:
		// Good — returned when workers drained
	case <-time.After(3 * time.Second):
		t.Fatal("shutdownWaitForWorkers should exit when ConnectedWorkers() returns 0")
	}
}

// --- pending handoff tests ---

// TestRegisterWorker_OnlyFirstHandoffAssigned verifies that only one pending handoff
// is consumed per registerWorker call (the break stops after the first).
// Kills mutation 46 (break -> continue in pending handoff loop).
func TestRegisterWorker_OnlyFirstHandoffAssigned(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	go drainConn(clientConn)

	workerID := "first-handoff-worker"

	// Add two pending handoffs
	d.mu.Lock()
	d.pendingHandoffs["handoff-bead-1"] = &pendingHandoff{
		beadID:   "handoff-bead-1",
		worktree: "/tmp/wt1",
		model:    "model1",
	}
	d.pendingHandoffs["handoff-bead-2"] = &pendingHandoff{
		beadID:   "handoff-bead-2",
		worktree: "/tmp/wt2",
		model:    "model2",
	}
	d.mu.Unlock()

	d.registerWorker(workerID, serverConn)

	d.mu.Lock()
	remaining := len(d.pendingHandoffs)
	d.mu.Unlock()

	// Only one handoff should be consumed
	if remaining != 1 {
		t.Errorf("expected 1 remaining pending handoff, got %d", remaining)
	}
}

// TestCheckHeartbeats_DeadWorkerContinuesLoop verifies that after processing
// a dead worker, the loop continues to check other workers.
// Kills mutation 47/48 (continue -> break in heartbeat loop).
func TestCheckHeartbeats_DeadWorkerContinuesLoop(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	now := time.Now()
	timeout := d.cfg.HeartbeatTimeout

	conn1 := newMockConn()
	conn2 := newMockConn()

	// Two dead workers
	d.mu.Lock()
	d.workers["dead-1"] = &trackedWorker{
		id:       "dead-1",
		conn:     conn1,
		state:    protocol.WorkerBusy,
		lastSeen: now.Add(-(timeout + time.Second)),
		encoder:  json.NewEncoder(conn1),
	}
	d.workers["dead-2"] = &trackedWorker{
		id:       "dead-2",
		conn:     conn2,
		state:    protocol.WorkerBusy,
		lastSeen: now.Add(-(timeout + time.Second)),
		encoder:  json.NewEncoder(conn2),
	}
	d.mu.Unlock()

	d.nowFunc = func() time.Time { return now }
	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, still1 := d.workers["dead-1"]
	_, still2 := d.workers["dead-2"]
	d.mu.Unlock()

	if still1 || still2 {
		t.Errorf("both dead workers should be removed: dead-1=%v, dead-2=%v", still1, still2)
	}
}

// TestUpsertWorker_NewWorkerLastSeenSet verifies that lastSeen is set from nowFunc.
// Kills mutation 7 (skip struct init).
func TestUpsertWorker_NewWorkerLastSeenSet(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	fixedTime := time.Now().Add(100 * time.Second)
	d.nowFunc = func() time.Time { return fixedTime }

	conn := newMockConn()
	workerID := "lastseen-new-worker"

	d.mu.Lock()
	d.upsertWorker(workerID, conn, false)
	w := d.workers[workerID]
	d.mu.Unlock()

	if !w.lastSeen.Equal(fixedTime) {
		t.Errorf("lastSeen = %v, want %v", w.lastSeen, fixedTime)
	}
}

// TestShutdownKillsManagedWorkers verifies that shutdownWaitForWorkers calls
// procMgr.Kill for managed workers but NOT for unmanaged workers.
// Edges: procMgr==nil → skip; Kill error → ignored (best-effort).
func TestShutdownKillsManagedWorkers(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	pm := &mockProcessManager{}
	d.procMgr = pm
	d.cfg.ShutdownTimeout = 150 * time.Millisecond

	managedID := "managed-worker-1"
	unmanagedID := "unmanaged-worker-1"

	managedConn := newMockConn()
	unmanagedConn := newMockConn()

	d.mu.Lock()
	d.workers[managedID] = &trackedWorker{
		id:      managedID,
		conn:    managedConn,
		state:   protocol.WorkerBusy,
		managed: true,
	}
	d.workers[unmanagedID] = &trackedWorker{
		id:      unmanagedID,
		conn:    unmanagedConn,
		state:   protocol.WorkerBusy,
		managed: false,
	}
	d.mu.Unlock()

	// Workers won't drain — timeout fires and force-closes them.
	d.shutdownWaitForWorkers()

	killed := pm.KilledIDs()

	foundManaged := false
	for _, id := range killed {
		if id == managedID {
			foundManaged = true
		}
	}
	if !foundManaged {
		t.Errorf("expected procMgr.Kill(%q), killed=%v", managedID, killed)
	}

	for _, id := range killed {
		if id == unmanagedID {
			t.Errorf("procMgr.Kill should NOT be called for unmanaged worker %q", unmanagedID)
		}
	}
}

// TestShutdownKillsManagedWorkers_NilProcMgr verifies that shutdownWaitForWorkers
// is a no-op when procMgr is nil (does not panic).
func TestShutdownKillsManagedWorkers_NilProcMgr(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	// procMgr is nil by default
	d.cfg.ShutdownTimeout = 150 * time.Millisecond

	conn := newMockConn()
	d.mu.Lock()
	d.workers["managed-w"] = &trackedWorker{
		id:      "managed-w",
		conn:    conn,
		state:   protocol.WorkerBusy,
		managed: true,
	}
	d.mu.Unlock()

	// Should not panic even without a procMgr.
	d.shutdownWaitForWorkers()
}

// TestShutdownKillsManagedWorkers_DrainPath verifies that Kill is called
// for managed workers even when workers drain before the timeout.
func TestShutdownKillsManagedWorkers_DrainPath(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	pm := &mockProcessManager{}
	d.procMgr = pm
	d.cfg.ShutdownTimeout = 10 * time.Second // long timeout — drain path

	managedID := "drain-managed"
	conn := newMockConn()

	d.mu.Lock()
	d.workers[managedID] = &trackedWorker{
		id:      managedID,
		conn:    conn,
		state:   protocol.WorkerBusy,
		managed: true,
	}
	d.mu.Unlock()

	// Simulate worker draining shortly after start.
	go func() {
		time.Sleep(100 * time.Millisecond)
		d.mu.Lock()
		delete(d.workers, managedID)
		d.mu.Unlock()
	}()

	d.shutdownWaitForWorkers()

	killed := pm.KilledIDs()
	found := false
	for _, id := range killed {
		if id == managedID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected procMgr.Kill(%q) on drain path, killed=%v", managedID, killed)
	}
}

// TestSendToWorker_MarshalErrorReturnsError verifies that a marshal error
// is returned (mutation 22 removes error return).
// Note: It's hard to provoke marshal errors with valid protocol.Message but
// we cover the success path to validate the append of newline and write.
// Kills mutation 116 (skip append newline).
func TestSendToWorker_AppendNewlineThenWrite(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	w := &trackedWorker{
		id:      "newline-worker",
		conn:    conn,
		encoder: json.NewEncoder(conn),
	}
	d.mu.Lock()
	d.workers[w.id] = w
	d.mu.Unlock()

	msg := protocol.Message{Type: protocol.MsgHeartbeat}
	d.mu.Lock()
	err := d.sendToWorker(w, msg)
	d.mu.Unlock()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(conn.written) == 0 {
		t.Fatal("nothing was written")
	}
	written := conn.written[0]
	if len(written) == 0 || written[len(written)-1] != '\n' {
		t.Error("written data should end with newline")
	}
}
