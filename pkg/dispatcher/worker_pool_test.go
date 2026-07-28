package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

// --- failConn: a net.Conn whose Write always fails ---

type failConn struct {
	mu     sync.Mutex
	closed bool
}

type blockingReviewSpawner struct {
	release <-chan struct{}
}

func (s *blockingReviewSpawner) Spawn(context.Context, string, string, string) (ops.Process, error) {
	return &blockingReviewProcess{release: s.release}, nil
}

type blockingReviewProcess struct {
	release <-chan struct{}
}

type countingReviewProcess struct {
	kills atomic.Int32
	done  chan struct{}
}

func (p *countingReviewProcess) Wait() error           { <-p.done; return nil }
func (p *countingReviewProcess) Kill() error           { p.kills.Add(1); close(p.done); return nil }
func (*countingReviewProcess) Output() (string, error) { return "", nil }
func (*countingReviewProcess) LastOutputAt() time.Time { return time.Time{} }

type sequentialReviewSpawner struct {
	mu        sync.Mutex
	processes []ops.Process
	spawns    int
}

func (s *sequentialReviewSpawner) Spawn(context.Context, string, string, string) (ops.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.processes) == 0 {
		return nil, errors.New("unexpected ops spawn")
	}
	process := s.processes[0]
	s.processes = s.processes[1:]
	s.spawns++
	return process, nil
}

func (s *sequentialReviewSpawner) SpawnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spawns
}

func (p *blockingReviewProcess) Wait() error {
	<-p.release
	return nil
}

func (*blockingReviewProcess) Kill() error             { return nil }
func (*blockingReviewProcess) Output() (string, error) { return "", nil }
func (*blockingReviewProcess) LastOutputAt() time.Time { return time.Time{} }

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

func TestRegisterWorker_ShutsDownExcessUnmanagedWorkerAtMaxWorkers(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.cfg.MaxWorkers = 2

	for i := 0; i < 2; i++ {
		workerID := fmt.Sprintf("managed-%d", i)
		conn := newMockConn()
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:      workerID,
			conn:    conn,
			state:   protocol.WorkerIdle,
			managed: true,
			encoder: json.NewEncoder(conn),
		}
		d.mu.Unlock()
	}

	conn := newMockConn()
	d.registerWorker("manual-excess", conn)

	d.mu.Lock()
	w := d.workers["manual-excess"]
	d.mu.Unlock()
	if w == nil {
		t.Fatal("excess unmanaged worker should remain tracked until it disconnects")
	}
	if w.state != protocol.WorkerShuttingDown {
		t.Fatalf("excess unmanaged worker state = %s, want %s", w.state, protocol.WorkerShuttingDown)
	}
	if w.beadID != "" || w.assignmentID != 0 || w.targetBeadID != "" {
		t.Fatalf("excess unmanaged worker retained assignment state: bead=%q assignment=%d target=%q",
			w.beadID, w.assignmentID, w.targetBeadID)
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.written) != 1 {
		t.Fatalf("expected one shutdown message, got %d", len(conn.written))
	}
	var msg protocol.Message
	if err := json.Unmarshal(conn.written[0], &msg); err != nil {
		t.Fatalf("decode shutdown message: %v", err)
	}
	if msg.Type != protocol.MsgShutdown {
		t.Fatalf("message type = %s, want %s", msg.Type, protocol.MsgShutdown)
	}
}

func TestRegisterWorker_ShutsDownExcessPendingManagedWorkerAtMaxWorkers(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.cfg.MaxWorkers = 1

	existingConn := newMockConn()
	d.mu.Lock()
	d.workers["managed-existing"] = &trackedWorker{
		id:      "managed-existing",
		conn:    existingConn,
		state:   protocol.WorkerIdle,
		managed: true,
		encoder: json.NewEncoder(existingConn),
	}
	d.pendingManagedIDs["managed-pending"] = true
	d.pendingManagedSince["managed-pending"] = d.nowFunc()
	d.mu.Unlock()

	pendingConn := newMockConn()
	d.registerWorker("managed-pending", pendingConn)

	d.mu.Lock()
	w := d.workers["managed-pending"]
	d.mu.Unlock()
	if w == nil {
		t.Fatal("excess pending managed worker should remain tracked until it disconnects")
	}
	if w.state != protocol.WorkerShuttingDown {
		t.Fatalf("pending managed worker state = %s, want %s", w.state, protocol.WorkerShuttingDown)
	}
	if w.beadID != "" || w.assignmentID != 0 || w.targetBeadID != "" {
		t.Fatalf("pending managed worker retained assignment state: bead=%q assignment=%d target=%q",
			w.beadID, w.assignmentID, w.targetBeadID)
	}

	pendingConn.mu.Lock()
	defer pendingConn.mu.Unlock()
	if len(pendingConn.written) != 1 {
		t.Fatalf("expected one shutdown message for excess pending managed worker, got %d", len(pendingConn.written))
	}
	var msg protocol.Message
	if err := json.Unmarshal(pendingConn.written[0], &msg); err != nil {
		t.Fatalf("decode shutdown message: %v", err)
	}
	if msg.Type != protocol.MsgShutdown {
		t.Fatalf("message type = %s, want %s", msg.Type, protocol.MsgShutdown)
	}
}

func TestRespawnWorkerAssignsPendingHandoffToExistingIdleWorkerAtCap(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.cfg.MaxWorkers = 1
	pm := &mockProcessManager{}
	d.procMgr = pm

	conn := newMockConn()
	d.mu.Lock()
	d.workers["idle-at-cap"] = &trackedWorker{
		id:      "idle-at-cap",
		conn:    conn,
		state:   protocol.WorkerIdle,
		managed: true,
		encoder: json.NewEncoder(conn),
	}
	d.mu.Unlock()

	d.respawnWorker(context.Background(), "handoff-at-cap", workerAssignmentSnapshot{
		worktree:     "/tmp/handoff-at-cap",
		runtime:      "claude",
		model:        protocol.ModelSonnet,
		baseBranch:   "main",
		targetBranch: "main",
	}, "Handoff at cap", nil)

	if spawned := pm.SpawnedIDs(); len(spawned) != 0 {
		t.Fatalf("spawned handoff workers despite idle worker at cap: %v", spawned)
	}
	d.mu.Lock()
	w := d.workers["idle-at-cap"]
	_, stillPending := d.pendingHandoffs["handoff-at-cap"]
	d.mu.Unlock()
	if stillPending {
		t.Fatal("pending handoff remained queued despite existing idle worker")
	}
	if w.state != protocol.WorkerBusy {
		t.Fatalf("idle worker state = %s, want %s", w.state, protocol.WorkerBusy)
	}
	if w.beadID != "handoff-at-cap" {
		t.Fatalf("worker bead = %q, want handoff-at-cap", w.beadID)
	}
	conn.mu.Lock()
	writes := len(conn.written)
	conn.mu.Unlock()
	if writes != 1 {
		t.Fatalf("assign writes = %d, want 1", writes)
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

	d.mu.Lock()
	_, stillPending := d.pendingHandoffs["gone-bead"]
	d.mu.Unlock()
	if !stillPending {
		t.Fatal("expected pending handoff to remain recoverable after worker deletion")
	}
	// Test succeeds if there's no panic/deadlock — the guard prevents sending to a deleted worker
}

func TestRegisterWorkerRetainsPendingHandoffOnSendFailure(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	workerID := "send-failure-worker"
	beadID := "send-failure-bead"
	conn := &failConn{}

	d.mu.Lock()
	d.pendingHandoffs[beadID] = &pendingHandoff{
		beadID:   beadID,
		worktree: "/tmp/send-failure-wt",
		model:    "test-model",
	}
	d.mu.Unlock()

	d.registerWorker(workerID, conn)

	d.mu.Lock()
	_, stillPending := d.pendingHandoffs[beadID]
	_, workerExists := d.workers[workerID]
	d.mu.Unlock()

	if !stillPending {
		t.Fatal("expected pending handoff to remain recoverable after ASSIGN send failure")
	}
	if workerExists {
		t.Fatal("expected failed worker registration to be discarded after ASSIGN send failure")
	}
}

func TestReconnectStaleConnCleanupPreservesLiveWorker(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	workerID := "worker-reconnect-cleanup"
	conn1 := newMockConn()
	conn2 := newMockConn()

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     conn1,
		state:    protocol.WorkerBusy,
		beadID:   "bead-reconnect",
		worktree: "/tmp/reconnect-worktree",
		runtime:  "claude",
		model:    "sonnet",
		encoder:  json.NewEncoder(conn1),
	}
	d.mu.Unlock()

	d.registerWorker(workerID, conn2)
	d.connCloseCleanup(workerID, conn1)

	d.mu.Lock()
	w, exists := d.workers[workerID]
	if !exists {
		d.mu.Unlock()
		t.Fatal("stale connection cleanup deleted reconnected worker")
	}
	if w.conn != conn2 {
		d.mu.Unlock()
		t.Fatal("worker conn was not preserved as conn2")
	}
	if w.beadID != "bead-reconnect" || w.state != protocol.WorkerBusy || w.worktree != "/tmp/reconnect-worktree" {
		d.mu.Unlock()
		t.Fatalf("reconnect did not preserve assignment metadata: bead=%q state=%s worktree=%q", w.beadID, w.state, w.worktree)
	}
	err := d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
	pending := len(w.pendingMsgs)
	d.mu.Unlock()

	if err != nil {
		t.Fatalf("sendToWorker on live reconnect conn returned error: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pendingMsgs length = %d, want 0", pending)
	}
	if len(conn2.written) == 0 {
		t.Fatal("expected sendToWorker to write to conn2")
	}
	if len(conn1.written) != 0 {
		t.Fatal("sendToWorker wrote to stale conn1")
	}

	activeWorkerID := "worker-active-cleanup"
	activeConn := newMockConn()
	d.mu.Lock()
	d.workers[activeWorkerID] = &trackedWorker{
		id:      activeWorkerID,
		conn:    activeConn,
		state:   protocol.WorkerIdle,
		encoder: json.NewEncoder(activeConn),
	}
	d.mu.Unlock()

	d.connCloseCleanup(activeWorkerID, activeConn)

	d.mu.Lock()
	_, activeExists := d.workers[activeWorkerID]
	d.mu.Unlock()
	if activeExists {
		t.Fatal("cleanup for active connection did not remove worker")
	}
}

func TestDisconnectedWorkerWithPreservedWorktreeQuarantinesAssignment(t *testing.T) {
	d, beadSrc, worktrees, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID     = "oro-preserved-disconnect"
		workerID   = "worker-preserved-disconnect"
		worktree   = "/tmp/worktree-preserved-disconnect"
		baseBranch = "main"
	)

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
	beadSrc.mu.Unlock()
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
	d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "git" {
			t.Fatalf("command = %q, want git", name)
		}
		if len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "status" && args[3] == "--porcelain" {
			return nil, nil
		}
		if len(args) == 5 && args[0] == "-C" && args[1] == worktree && args[2] == "rev-list" && args[3] == "--count" && args[4] == baseBranch+".."+protocol.BranchPrefix+beadID {
			return []byte("1\n"), nil
		}
		t.Fatalf("unexpected git args: %q", args)
		return nil, nil
	}}
	worktrees.existsFn = func(_ context.Context, path string) bool { return path == worktree }

	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerBusy,
		beadID:       beadID,
		assignmentID: assignmentID,
		worktree:     worktree,
		baseBranch:   baseBranch,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	d.connCloseCleanup(workerID, conn)

	var assignmentStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if assignmentStatus != "quarantined" {
		t.Fatalf("assignment status = %q, want quarantined", assignmentStatus)
	}

	var gotBeadID, gotWorkerID, gotWorktree, gotBranch, gotReason, gotStatus string
	var gotAssignmentID int64
	if err := d.db.QueryRowContext(ctx, `
SELECT bead_id, assignment_id, worker_id, worktree, branch, reason, status
FROM recovery_quarantines
WHERE bead_id=?`, beadID).Scan(
		&gotBeadID, &gotAssignmentID, &gotWorkerID, &gotWorktree, &gotBranch, &gotReason, &gotStatus,
	); err != nil {
		t.Fatalf("query recovery quarantine: %v", err)
	}
	if gotBeadID != beadID || gotAssignmentID != assignmentID || gotWorkerID != workerID || gotWorktree != worktree || gotBranch != protocol.BranchPrefix+beadID || gotReason != "stale_active_assignment" || gotStatus != "open" {
		t.Fatalf("recovery quarantine = bead=%q assignment=%d worker=%q worktree=%q branch=%q reason=%q status=%q", gotBeadID, gotAssignmentID, gotWorkerID, gotWorktree, gotBranch, gotReason, gotStatus)
	}

	d.mu.Lock()
	_, workerStillTracked := d.workers[workerID]
	d.mu.Unlock()
	if workerStillTracked {
		t.Fatal("disconnected worker remained tracked")
	}
	beadSrc.mu.Lock()
	status := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()
	if status != "blocked" {
		t.Fatalf("preserved disconnected bead status = %q, want blocked", status)
	}
}

func TestDisconnectedWorkerComparesPreservedWorkAgainstAssignmentBase(t *testing.T) {
	d, _, worktrees, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID     = "oro-epic-base-disconnect"
		workerID   = "worker-epic-base-disconnect"
		worktree   = "/tmp/worktree-epic-base-disconnect"
		baseBranch = "epic/oro-parent"
	)
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
	worktrees.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	var comparedRange string
	d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 4 && args[2] == "status" {
			return nil, nil
		}
		if len(args) == 5 && args[2] == "rev-list" {
			comparedRange = args[4]
			return []byte("0\n"), nil
		}
		t.Fatalf("unexpected git args: %q", args)
		return nil, nil
	}}

	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerBusy,
		beadID:       beadID,
		assignmentID: assignmentID,
		worktree:     worktree,
		baseBranch:   baseBranch,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	d.connCloseCleanup(workerID, conn)

	wantRange := baseBranch + ".." + protocol.BranchPrefix + beadID
	if comparedRange != wantRange {
		t.Fatalf("preserved work comparison = %q, want %q", comparedRange, wantRange)
	}
	var quarantines int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_quarantines WHERE assignment_id=?`, assignmentID).Scan(&quarantines); err != nil {
		t.Fatalf("count recovery quarantines: %v", err)
	}
	if quarantines != 0 {
		t.Fatalf("recovery quarantines for assignment at epic base = %d, want 0", quarantines)
	}
}

func TestHeartbeatTimeoutWithPreservedWorktreeQuarantinesAssignment(t *testing.T) {
	d, beadSrc, worktrees, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID   = "oro-preserved-heartbeat-timeout"
		workerID = "worker-preserved-heartbeat-timeout"
		worktree = "/tmp/worktree-preserved-heartbeat-timeout"
	)

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
	beadSrc.mu.Unlock()
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
	worktrees.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 4 && args[2] == "status" {
			return nil, nil
		}
		return []byte("1\n"), nil
	}}

	now := time.Now()
	d.nowFunc = func() time.Time { return now }
	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerBusy,
		beadID:       beadID,
		assignmentID: assignmentID,
		worktree:     worktree,
		lastSeen:     now,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()
	d.nowFunc = func() time.Time { return now.Add(2 * d.cfg.HeartbeatTimeout) }

	d.checkHeartbeats(ctx)

	var assignmentStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if assignmentStatus != "quarantined" {
		t.Fatalf("assignment status = %q, want quarantined", assignmentStatus)
	}
	var quarantines int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_quarantines WHERE assignment_id=? AND reason='stale_active_assignment' AND status='open'`, assignmentID).Scan(&quarantines); err != nil {
		t.Fatalf("count recovery quarantines: %v", err)
	}
	if quarantines != 1 {
		t.Fatalf("open stale assignment quarantines = %d, want 1", quarantines)
	}
	beadSrc.mu.Lock()
	status := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()
	if status != "blocked" {
		t.Fatalf("preserved timeout bead status = %q, want blocked", status)
	}
}

func TestDisconnectedWorkerQuarantineIncludesHeartbeatTimeout(t *testing.T) {
	d, beadSrc, worktrees, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID   = "oro-heartbeat-quarantine-details"
		workerID = "worker-heartbeat-quarantine-details"
		worktree = "/tmp/worktree-heartbeat-quarantine-details"
	)

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
	beadSrc.mu.Unlock()
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
	worktrees.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 4 && args[2] == "status" {
			return nil, nil
		}
		return []byte("1\n"), nil
	}}

	now := time.Now()
	d.nowFunc = func() time.Time { return now }
	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerBusy,
		beadID:       beadID,
		assignmentID: assignmentID,
		worktree:     worktree,
		lastSeen:     now,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()
	d.nowFunc = func() time.Time { return now.Add(2 * d.cfg.HeartbeatTimeout) }

	d.checkHeartbeats(ctx)

	var details string
	if err := d.db.QueryRowContext(ctx, `SELECT details FROM recovery_quarantines WHERE assignment_id=?`, assignmentID).Scan(&details); err != nil {
		t.Fatalf("query quarantine details: %v", err)
	}
	if !strings.Contains(details, "heartbeat timeout for worker "+workerID) {
		t.Fatalf("quarantine details = %q, want heartbeat timeout cause and worker ID", details)
	}
}

func TestDisconnectedWorkerWithCompletedAssignmentDoesNotQuarantine(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID   = "oro-completed-disconnect"
		workerID = "worker-completed-disconnect"
		worktree = "/tmp/worktree-completed-disconnect"
	)
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
	if err := d.completeAssignment(ctx, assignmentID, beadID); err != nil {
		t.Fatalf("complete assignment: %v", err)
	}

	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{id: workerID, conn: conn, state: protocol.WorkerBusy, beadID: beadID, assignmentID: assignmentID, worktree: worktree, encoder: json.NewEncoder(conn)}
	d.mu.Unlock()

	d.connCloseCleanup(workerID, conn)

	var quarantines int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_quarantines WHERE assignment_id=?`, assignmentID).Scan(&quarantines); err != nil {
		t.Fatalf("count recovery quarantines: %v", err)
	}
	if quarantines != 0 {
		t.Fatalf("recovery quarantines for completed assignment = %d, want 0", quarantines)
	}
}

func TestDisconnectedWorkerCompletedDuringInspectionDoesNotBlockOrQuarantine(t *testing.T) {
	d, beadSrc, worktrees, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID   = "oro-completed-during-inspection"
		workerID = "worker-completed-during-inspection"
		worktree = "/tmp/worktree-completed-during-inspection"
	)
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
	worktrees.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 4 && args[2] == "status" {
			return nil, nil
		}
		if len(args) == 5 && args[2] == "rev-list" {
			if err := d.completeAssignment(ctx, assignmentID, beadID); err != nil {
				t.Fatalf("complete assignment during inspection: %v", err)
			}
			return []byte("1\n"), nil
		}
		t.Fatalf("unexpected git args: %q", args)
		return nil, nil
	}}

	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerBusy,
		beadID:       beadID,
		assignmentID: assignmentID,
		worktree:     worktree,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	d.connCloseCleanup(workerID, conn)

	var assignmentStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if assignmentStatus != "completed" {
		t.Fatalf("assignment status = %q, want completed", assignmentStatus)
	}
	var quarantines int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_quarantines WHERE assignment_id=?`, assignmentID).Scan(&quarantines); err != nil {
		t.Fatalf("count recovery quarantines: %v", err)
	}
	if quarantines != 0 {
		t.Fatalf("recovery quarantines for concurrently completed assignment = %d, want 0", quarantines)
	}
	beadSrc.mu.Lock()
	status, updated := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()
	if updated {
		t.Fatalf("concurrently completed bead status updated to %q", status)
	}
}

func TestDisconnectedWorkerWithPreservedWorktreeCoalescesDuplicateCleanup(t *testing.T) {
	d, _, worktrees, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID   = "oro-duplicate-disconnect"
		workerID = "worker-duplicate-disconnect"
		worktree = "/tmp/worktree-duplicate-disconnect"
	)
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
	worktrees.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 4 && args[2] == "status" {
			return nil, nil
		}
		return []byte("1\n"), nil
	}}

	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{id: workerID, conn: conn, state: protocol.WorkerBusy, beadID: beadID, assignmentID: assignmentID, worktree: worktree, encoder: json.NewEncoder(conn)}
	d.mu.Unlock()

	d.connCloseCleanup(workerID, conn)
	d.connCloseCleanup(workerID, conn)

	var quarantines int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_quarantines WHERE assignment_id=? AND status='open'`, assignmentID).Scan(&quarantines); err != nil {
		t.Fatalf("count recovery quarantines: %v", err)
	}
	if quarantines != 1 {
		t.Fatalf("open recovery quarantines = %d, want 1", quarantines)
	}
}

func TestDisconnectedWorkerQuarantineFailurePreservesActiveAssignment(t *testing.T) {
	d, _, worktrees, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID   = "oro-quarantine-write-failure"
		workerID = "worker-quarantine-write-failure"
		worktree = "/tmp/worktree-quarantine-write-failure"
	)
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
	if _, err := d.db.ExecContext(ctx, `
CREATE TRIGGER reject_recovery_quarantine
BEFORE INSERT ON recovery_quarantines
BEGIN
  SELECT RAISE(ABORT, 'forced quarantine write failure');
END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	worktrees.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 4 && args[2] == "status" {
			return nil, nil
		}
		return []byte("1\n"), nil
	}}

	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{id: workerID, conn: conn, state: protocol.WorkerBusy, beadID: beadID, assignmentID: assignmentID, worktree: worktree, encoder: json.NewEncoder(conn)}
	d.mu.Unlock()

	d.connCloseCleanup(workerID, conn)

	var assignmentStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if assignmentStatus != "active" {
		t.Fatalf("assignment status after quarantine failure = %q, want active", assignmentStatus)
	}
	if got := eventCount(t, d.db, "disconnected_assignment_quarantine_failed"); got != 1 {
		t.Fatalf("quarantine failure events = %d, want 1", got)
	}
}

func TestDisconnectedWorkerBeadStatusFailurePreservesActiveAssignment(t *testing.T) {
	d, beadSrc, worktrees, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID   = "oro-block-status-failure"
		workerID = "worker-block-status-failure"
		worktree = "/tmp/worktree-block-status-failure"
	)
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
	beadSrc.updateErrs = map[string]error{beadID: errors.New("forced bead status failure")}
	worktrees.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 4 && args[2] == "status" {
			return nil, nil
		}
		return []byte("1\n"), nil
	}}

	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerBusy,
		beadID:       beadID,
		assignmentID: assignmentID,
		worktree:     worktree,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	d.connCloseCleanup(workerID, conn)

	var assignmentStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if assignmentStatus != "active" {
		t.Fatalf("assignment status after bead update failure = %q, want active", assignmentStatus)
	}
	var quarantines int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_quarantines WHERE assignment_id=?`, assignmentID).Scan(&quarantines); err != nil {
		t.Fatalf("count recovery quarantines: %v", err)
	}
	if quarantines != 1 {
		t.Fatalf("recovery quarantines after bead update failure = %d, want 1", quarantines)
	}
	if got := eventCount(t, d.db, "disconnected_assignment_block_failed"); got != 1 {
		t.Fatalf("bead block failure events = %d, want 1", got)
	}
}

func TestWorkerReachableThroughAssignment(t *testing.T) {
	t.Parallel()
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)

	ctx := context.Background()
	workerID := "worker-assignment-reconnect"
	beadID := "bead-assignment-reconnect"
	conn1 := newMockConn()

	wtMgr.createFn = func(_ context.Context, gotBeadID, _ string) (string, string, error) {
		if gotBeadID != beadID {
			t.Fatalf("worktree create beadID = %q, want %q", gotBeadID, beadID)
		}
		return "/tmp/" + beadID, "branch-" + beadID, nil
	}

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Assignment reconnect",
		Status:             "open",
		AcceptanceCriteria: "Test: reachable | Cmd: go test ./pkg/dispatcher | Assert: pass",
	}
	beadSrc.mu.Unlock()

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:      workerID,
		conn:    conn1,
		state:   protocol.WorkerIdle,
		encoder: json.NewEncoder(conn1),
	}
	w := d.workers[workerID]
	d.mu.Unlock()

	if err := d.assignBead(ctx, w, protocol.Bead{ID: beadID, Title: "Assignment reconnect", Priority: 0}); err != nil {
		t.Fatalf("assignBead: %v", err)
	}
	if got := beadSrc.updated[beadID]; got != "in_progress" {
		t.Fatalf("assigned bead status = %q, want in_progress", got)
	}
	conn1.mu.Lock()
	conn1Writes := len(conn1.written)
	conn1.mu.Unlock()

	conn2 := newMockConn()
	d.registerWorker(workerID, conn2)
	d.connCloseCleanup(workerID, conn1)

	d.mu.Lock()
	live, exists := d.workers[workerID]
	if !exists {
		d.mu.Unlock()
		t.Fatal("worker disappeared after reconnect and stale cleanup")
	}
	if live.conn != conn2 {
		d.mu.Unlock()
		t.Fatal("live worker is not using reconnected conn2")
	}
	if live.beadID != beadID || live.state != protocol.WorkerBusy {
		d.mu.Unlock()
		t.Fatalf("assignment metadata not preserved after reconnect: bead=%q state=%s", live.beadID, live.state)
	}
	err := d.sendToWorker(live, protocol.Message{
		Type: protocol.MsgPrepareShutdown,
		PrepareShutdown: &protocol.PrepareShutdownPayload{
			Timeout: time.Second,
		},
	})
	pending := len(live.pendingMsgs)
	d.mu.Unlock()
	if err != nil {
		t.Fatalf("dispatcher write to reconnected worker returned error: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pendingMsgs length = %d, want 0", pending)
	}
	conn2.mu.Lock()
	conn2Writes := append([][]byte(nil), conn2.written...)
	conn2.mu.Unlock()
	if len(conn2Writes) == 0 {
		t.Fatal("dispatcher write was not delivered to conn2")
	}
	var delivered protocol.Message
	if err := json.Unmarshal(conn2Writes[len(conn2Writes)-1], &delivered); err != nil {
		t.Fatalf("decode conn2 write: %v", err)
	}
	if delivered.Type != protocol.MsgPrepareShutdown {
		t.Fatalf("conn2 delivered message = %s, want PREPARE_SHUTDOWN", delivered.Type)
	}
	conn1.mu.Lock()
	conn1WritesAfter := len(conn1.written)
	conn1.mu.Unlock()
	if conn1WritesAfter != conn1Writes {
		t.Fatalf("stale conn1 received dispatcher write: before=%d after=%d", conn1Writes, conn1WritesAfter)
	}

	statusTime := time.Now().Add(time.Hour)
	d.nowFunc = func() time.Time { return statusTime }
	d.handleMessage(ctx, workerID, protocol.Message{
		Type: protocol.MsgStatus,
		Status: &protocol.StatusPayload{
			WorkerID: workerID,
			BeadID:   beadID,
			State:    "running",
		},
	})

	d.mu.Lock()
	gotProgress := d.workers[workerID].lastProgress
	d.mu.Unlock()
	if !gotProgress.Equal(statusTime) {
		t.Fatalf("worker STATUS did not round-trip through dispatcher progress: got %v want %v", gotProgress, statusTime)
	}
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

// TestHeartbeatDoesNotTouchProgressForBusyWorker verifies that heartbeats are
// liveness-only, even when context_pct climbs. Meaningful protocol messages
// update lastProgress instead.
func TestHeartbeatDoesNotTouchProgressForBusyWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	d.cfg.ProgressTimeout = 1 * time.Second

	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	// Register a busy worker with stale lastProgress (past timeout) and a
	// known prior context_pct. A higher context_pct heartbeat must not refresh
	// the real-progress clock.
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
		contextPct:   5,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	// Heartbeat with CLIMBING context_pct — must remain liveness-only.
	d.handleHeartbeat(context.Background(), workerID, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   workerID,
			BeadID:     "test-bead",
			ContextPct: 7,
		},
	})

	// Verify lastProgress was not updated.
	d.mu.Lock()
	got := d.workers[workerID].lastProgress
	d.mu.Unlock()
	if !got.Equal(staleProgress) {
		t.Errorf("lastProgress = %v, want %v (context drift must not refresh progress)", got, staleProgress)
	}

	// The worker must be reaped because no real progress occurred.
	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stillPresent := d.workers[workerID]
	d.mu.Unlock()
	if stillPresent {
		t.Error("busy worker with only context drift should be removed by checkHeartbeats")
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

// TestProgressTimeoutReapsWedgedNonBusyWorker verifies that the progress
// reaper covers non-review workers and counts only protocol progress, never
// context_pct drift, as progress.
func TestProgressTimeoutReapsWedgedNonBusyWorker(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	now := time.Now()
	d.nowFunc = func() time.Time { return now }
	d.cfg.ProgressTimeout = time.Second
	d.cfg.ReviewTimeout = time.Hour

	staleProgress := now.Add(-(d.cfg.ProgressTimeout + time.Second))
	newWorker := func(id string, state protocol.WorkerState) *trackedWorker {
		conn := newMockConn()
		return &trackedWorker{
			id:           id,
			conn:         conn,
			state:        state,
			beadID:       id + "-bead",
			lastSeen:     now,
			lastProgress: staleProgress,
			encoder:      json.NewEncoder(conn),
		}
	}

	creeping := newWorker("context-creep", protocol.WorkerBusy)
	creeping.contextPct = 10
	fresh := newWorker("genuine-progress", protocol.WorkerBusy)

	d.mu.Lock()
	d.workers[creeping.id] = creeping
	d.workers[fresh.id] = fresh
	d.mu.Unlock()

	// A spinning session may keep changing context_pct, but that alone is not
	// a STATUS/DONE/READY_FOR_REVIEW/QG progress transition.
	for _, contextPct := range []int{20, 30, 40} {
		d.handleHeartbeat(context.Background(), creeping.id, protocol.Message{
			Type: protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{
				WorkerID:   creeping.id,
				BeadID:     creeping.beadID,
				ContextPct: contextPct,
			},
		})
	}

	// A real protocol transition refreshes the progress clock.
	d.handleStatus(context.Background(), fresh.id, protocol.Message{
		Type: protocol.MsgStatus,
		Status: &protocol.StatusPayload{
			BeadID: fresh.beadID,
			State:  "running_progress",
		},
	})

	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, creepingPresent := d.workers[creeping.id]
	_, freshPresent := d.workers[fresh.id]
	d.mu.Unlock()

	if creepingPresent {
		t.Error("stale non-review worker present after progress scan")
	}
	if !freshPresent {
		t.Error("worker with a genuine progress transition was reaped")
	}
	if !creeping.conn.(*mockConn).closed {
		t.Error("reaped worker connections must be closed")
	}
	if got := eventCount(t, d.db, "progress_timeout"); got != 1 {
		t.Errorf("progress_timeout events = %d, want 1", got)
	}
}

// TestCheckHeartbeats_ActiveReviewUsesReviewTimeout verifies that an active
// managed review outlives ProgressTimeout and is reaped only after ReviewTimeout.
func TestCheckHeartbeats_ActiveReviewUsesReviewTimeout(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	d.cfg.ProgressTimeout = time.Second
	d.cfg.ReviewTimeout = 3 * time.Second
	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	d.ops = ops.NewSpawner(&blockingReviewSpawner{release: release})

	const workerID = "active-review-worker"
	const beadID = "active-review-bead"
	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerReviewing,
		managed:      true,
		beadID:       beadID,
		lastSeen:     now,
		lastProgress: now.Add(-(d.cfg.ProgressTimeout + time.Second)),
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	_ = d.ops.Review(context.Background(), ops.ReviewOpts{BeadID: beadID, Worktree: d.repoRoot, BaseBranch: "missing-base"})
	waitFor(t, func() bool { return d.ops.HasActiveForBead(beadID) }, time.Second)

	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, presentBeforeReviewTimeout := d.workers[workerID]
	d.mu.Unlock()
	if !presentBeforeReviewTimeout {
		t.Fatal("active review worker was reaped by ProgressTimeout")
	}
	if conn.closed {
		t.Fatal("active review worker connection was closed by ProgressTimeout")
	}
	if got := eventCount(t, d.db, "progress_timeout"); got != 0 {
		t.Fatalf("progress_timeout events before ReviewTimeout = %d, want 0", got)
	}

	now = now.Add(d.cfg.ReviewTimeout + time.Second)
	d.mu.Lock()
	d.workers[workerID].lastSeen = now
	d.mu.Unlock()
	d.checkHeartbeats(context.Background())
	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, presentAfterReviewTimeout := d.workers[workerID]
	d.mu.Unlock()
	if presentAfterReviewTimeout {
		t.Fatal("active review worker remained after ReviewTimeout")
	}
	if !conn.closed {
		t.Fatal("review timeout should close the worker connection")
	}
	if got := eventCount(t, d.db, "progress_timeout"); got != 1 {
		t.Fatalf("progress_timeout events after ReviewTimeout = %d, want 1", got)
	}
}

// TestWorkerProgressTimedOutSkipsSpawnFor verifies that spawn-for workers keep
// using their dedicated heartbeat/shutdown timeout path.
func TestWorkerProgressTimedOutSkipsSpawnFor(t *testing.T) {
	t.Parallel()

	now := time.Now()
	w := &trackedWorker{
		state:        protocol.WorkerBusy,
		spawnFor:     true,
		lastProgress: now.Add(-2 * time.Second),
	}
	if workerProgressTimedOut(w, now, time.Second) {
		t.Fatal("spawn-for worker should not use the progress timeout path")
	}
}

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

// TestTimedOutSetupCannotClobberReplacement proves that a setup operation
// which outlives its reservation cannot mutate the successor reservation for
// the same worker and bead.
func TestTimedOutSetupCannotClobberReplacement(t *testing.T) {
	d, beadSrc, _ := setupTryAssignSchedulingTest(t, 1)
	const (
		beadID      = "oro-stale-setup"
		successorWT = "/tmp/successor-worktree"
	)
	seedTryAssignBead(t, beadSrc, protocol.Bead{ID: beadID, Priority: 0})

	now := time.Now()
	d.nowFunc = func() time.Time { return now }
	d.cfg.ProgressTimeout = time.Second

	createStarted := make(chan struct{})
	releaseCreate := make(chan struct{})
	wt := d.worktrees.(*mockWorktreeManager)
	wt.createFn = func(_ context.Context, id, _ string) (string, string, error) {
		if id == beadID {
			close(createStarted)
			<-releaseCreate
		}
		return "/tmp/stale-worktree", protocol.BranchPrefix + id, nil
	}

	d.mu.Lock()
	worker := d.workers["w-sched-0"]
	d.mu.Unlock()
	claimed, staleDone := d.launchAssignment(context.Background(), worker, protocol.Bead{ID: beadID, Priority: 0}, 0)
	if !claimed {
		t.Fatal("stale assignment was not claimed")
	}
	select {
	case <-createStarted:
	case <-time.After(time.Second):
		t.Fatal("stale worktree creation did not start")
	}

	// Reap the setup, then install a successor that owns the same worker and
	// bead. Its generation and tracking must survive the stale goroutine.
	now = now.Add(d.cfg.ProgressTimeout + time.Second)
	d.checkHeartbeats(context.Background())
	d.mu.Lock()
	if worker.state != protocol.WorkerIdle {
		d.mu.Unlock()
		t.Fatalf("worker state after timeout = %s, want idle", worker.state)
	}
	worker.state = protocol.WorkerReserved
	worker.beadID = beadID
	worker.setupReservedAt = now
	worker.reservationGen++
	successorGen := worker.reservationGen
	d.assigningBeads[beadID] = true
	d.worktreeByBead[beadID] = successorWT
	d.attemptCounts[beadID] = 1
	d.mu.Unlock()
	beadSrc.mu.Lock()
	beadSrc.updated[beadID] = "in_progress"
	beadSrc.mu.Unlock()

	close(releaseCreate)
	select {
	case <-staleDone:
	case <-time.After(time.Second):
		t.Fatal("stale setup did not finish")
	}

	d.mu.Lock()
	state, assignmentID := worker.state, worker.assignmentID
	gotGen := worker.reservationGen
	gotWorktree := d.worktreeByBead[beadID]
	assigning := d.assigningBeads[beadID]
	attempts := d.attemptCounts[beadID]
	d.mu.Unlock()
	if state != protocol.WorkerReserved || assignmentID != 0 || gotGen != successorGen {
		t.Fatalf("stale setup clobbered successor worker: state=%s assignment=%d generation=%d want generation=%d", state, assignmentID, gotGen, successorGen)
	}
	if !assigning || gotWorktree != successorWT || attempts != 1 {
		t.Fatalf("stale setup clobbered successor tracking: assigning=%t worktree=%q attempts=%d", assigning, gotWorktree, attempts)
	}
	beadSrc.mu.Lock()
	status := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()
	if status != "in_progress" {
		t.Fatalf("stale setup reopened successor bead: status=%q", status)
	}
}

// TestTimedOutReaperCannotClobberReplacement proves a setup reaper does not
// reopen or clear a successor that claims the bead after the reaper releases
// its reservation but before its external cleanup runs.
func TestTimedOutReaperCannotClobberReplacement(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	const (
		workerID = "timed-out-setup-worker"
		beadID   = "timed-out-setup-bead"
	)
	now := time.Now()
	d.nowFunc = func() time.Time { return now }
	d.cfg.ProgressTimeout = time.Second

	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	beadSrc.showFn = func(ctx context.Context, id string) (*protocol.BeadDetail, error) {
		if id != beadID {
			return &protocol.BeadDetail{Status: "open"}, nil
		}
		close(cleanupStarted)
		select {
		case <-releaseCleanup:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &protocol.BeadDetail{Status: "in_progress"}, nil
	}

	d.mu.Lock()
	worker := &trackedWorker{
		id:              workerID,
		conn:            newMockConn(),
		state:           protocol.WorkerReserved,
		beadID:          beadID,
		setupReservedAt: now.Add(-2 * time.Second),
		reservationGen:  1,
		encoder:         json.NewEncoder(newMockConn()),
	}
	d.workers[workerID] = worker
	d.assigningBeads[beadID] = true
	d.attemptCounts[beadID] = 1
	d.worktreeByBead[beadID] = "/tmp/g1"
	d.mu.Unlock()
	beadSrc.mu.Lock()
	beadSrc.updated = make(map[string]string)
	beadSrc.updated[beadID] = "in_progress"
	beadSrc.mu.Unlock()

	reaped := make(chan struct{})
	go func() {
		d.checkHeartbeats(context.Background())
		close(reaped)
	}()

	select {
	case <-d.workerReadyCh:
	case <-time.After(time.Second):
		t.Fatal("timed-out setup did not notify scheduling")
	}
	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("timed-out setup did not begin external cleanup")
	}

	d.mu.Lock()
	worker.state = protocol.WorkerReserved
	worker.beadID = beadID
	worker.setupReservedAt = now
	worker.reservationGen++
	g2 := worker.reservationGen
	d.assigningBeads[beadID] = true
	d.attemptCounts[beadID] = 2
	d.worktreeByBead[beadID] = "/tmp/g2"
	d.mu.Unlock()

	close(releaseCleanup)
	select {
	case <-reaped:
	case <-time.After(time.Second):
		t.Fatal("timed-out reaper did not finish")
	}

	d.mu.Lock()
	state, gotGen := worker.state, worker.reservationGen
	assigning := d.assigningBeads[beadID]
	attempts := d.attemptCounts[beadID]
	worktree := d.worktreeByBead[beadID]
	d.mu.Unlock()
	if state != protocol.WorkerReserved || gotGen != g2 {
		t.Fatalf("g1 clobbered g2 worker reservation: state=%s generation=%d, want generation=%d", state, gotGen, g2)
	}
	if !assigning || attempts != 2 || worktree != "/tmp/g2" {
		t.Fatalf("g1 clobbered g2 tracking: assigning=%t attempts=%d worktree=%q", assigning, attempts, worktree)
	}
	beadSrc.mu.Lock()
	status := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()
	if status != "in_progress" {
		t.Fatalf("g1 reopened g2 bead: status=%q", status)
	}
}

// TestTimedOutReaperCleanupCannotClobberSuccessor proves timeout cleanup keeps
// g1's reservation until its last bead effect is complete. The update seam
// installs g2 only when the reservation is visibly released, reproducing the
// former ownership-check-to-update window without holding dispatcher locks
// across the bead-store call.
func TestTimedOutReaperCleanupCannotClobberSuccessor(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	const (
		workerID = "timed-out-cleanup-worker"
		beadID   = "timed-out-cleanup-bead"
	)
	now := time.Now()
	d.nowFunc = func() time.Time { return now }
	d.cfg.ProgressTimeout = time.Second

	d.mu.Lock()
	worker := &trackedWorker{
		id:              workerID,
		conn:            newMockConn(),
		state:           protocol.WorkerReserved,
		beadID:          beadID,
		setupReservedAt: now.Add(-2 * time.Second),
		reservationGen:  1,
		encoder:         json.NewEncoder(newMockConn()),
	}
	d.workers[workerID] = worker
	d.assigningBeads[beadID] = true
	d.attemptCounts[beadID] = 1
	d.handoffCounts[beadID] = 1
	d.worktreeByBead[beadID] = "/tmp/g1"
	d.mu.Unlock()
	beadSrc.mu.Lock()
	beadSrc.updated = map[string]string{beadID: "in_progress"}
	beadSrc.mu.Unlock()

	var injected bool
	beadSrc.updateFn = func(_ context.Context, id string, params beadstore.UpdateParams) error {
		if id != beadID || params.Status == nil || *params.Status != "open" {
			return nil
		}
		d.mu.Lock()
		if worker.state == protocol.WorkerIdle && !d.assigningBeads[beadID] {
			worker.state = protocol.WorkerReserved
			worker.beadID = beadID
			worker.setupReservedAt = now
			worker.reservationGen++
			d.assigningBeads[beadID] = true
			d.attemptCounts[beadID] = 2
			d.handoffCounts[beadID] = 2
			d.worktreeByBead[beadID] = "/tmp/g2"
			injected = true
		}
		d.mu.Unlock()

		beadSrc.mu.Lock()
		beadSrc.updated[beadID] = *params.Status
		beadSrc.mu.Unlock()
		return nil
	}

	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	state, generation := worker.state, worker.reservationGen
	assigning := d.assigningBeads[beadID]
	attempts := d.attemptCounts[beadID]
	handoffs := d.handoffCounts[beadID]
	worktree := d.worktreeByBead[beadID]
	d.mu.Unlock()
	beadSrc.mu.Lock()
	status := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()

	if injected {
		if state != protocol.WorkerReserved || !assigning || attempts != 2 || handoffs != 2 || worktree != "/tmp/g2" {
			t.Fatalf("g1 clobbered g2 tracking: state=%s assigning=%t attempts=%d handoffs=%d worktree=%q", state, assigning, attempts, handoffs, worktree)
		}
		if status != "in_progress" {
			t.Fatalf("g1 reopened g2 bead: status=%q", status)
		}
		return
	}
	if state != protocol.WorkerIdle || generation != 2 || assigning || attempts != 0 || handoffs != 0 || worktree != "/tmp/g1" || status != "open" {
		t.Fatalf("g1 cleanup = state=%s generation=%d assigning=%t attempts=%d handoffs=%d worktree=%q status=%q", state, generation, assigning, attempts, handoffs, worktree, status)
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

// TestCheckHeartbeats_ReviewTimeout verifies that a reviewing worker with stale
// lastProgress exceeding ReviewTimeout is killed and escalated as STUCK_WORKER.
func TestCheckHeartbeats_ReviewTimeout(t *testing.T) {
	t.Parallel()
	d, _, _, esc, _, _ := newTestDispatcher(t)

	d.cfg.ReviewTimeout = 100 * time.Millisecond

	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

	workerID := "review-stuck-worker"
	beadID := "review-bead"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         server,
		state:        protocol.WorkerReviewing,
		beadID:       beadID,
		lastSeen:     now.Add(-10 * time.Millisecond),
		lastProgress: now.Add(-(d.cfg.ReviewTimeout + time.Second)),
		encoder:      json.NewEncoder(server),
	}
	d.mu.Unlock()

	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stillPresent := d.workers[workerID]
	d.mu.Unlock()

	if stillPresent {
		t.Error("reviewing worker past ReviewTimeout should be removed")
	}

	// Assert: escalation sent with STUCK_WORKER type.
	msgs := esc.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 escalation message, got %d: %v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0], string(protocol.EscStuckWorker)) {
		t.Errorf("expected escalation to contain %q, got %q", protocol.EscStuckWorker, msgs[0])
	}
	if !strings.Contains(msgs[0], beadID) {
		t.Errorf("expected escalation to mention bead %q, got %q", beadID, msgs[0])
	}
}

func TestCheckHeartbeats_ReviewTimeoutCancelsOps(t *testing.T) {
	d, _, _, esc, _, _ := newTestDispatcher(t)
	timedOutProcess := &countingReviewProcess{done: make(chan struct{})}
	unrelatedProcess := &countingReviewProcess{done: make(chan struct{})}
	escalationRelease := make(chan struct{})
	spawner := &sequentialReviewSpawner{processes: []ops.Process{
		timedOutProcess,
		unrelatedProcess,
		&blockingReviewProcess{release: escalationRelease},
	}}
	d.ops = ops.NewSpawner(spawner)
	t.Cleanup(func() {
		close(escalationRelease)
		_, _ = d.ops.CancelForBead("unrelated-review-bead")
	})
	d.cfg.ReviewTimeout = 100 * time.Millisecond
	now := time.Now()
	d.nowFunc = func() time.Time { return now }
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	beadID := "review-timeout-cancel-bead"
	d.mu.Lock()
	d.workers["review-timeout-cancel-worker"] = &trackedWorker{
		id: "review-timeout-cancel-worker", conn: server, state: protocol.WorkerReviewing,
		managed: true, beadID: beadID, lastSeen: now, lastProgress: now.Add(-d.cfg.ReviewTimeout - time.Second),
		encoder: json.NewEncoder(server),
	}
	d.mu.Unlock()
	_ = d.ops.Review(context.Background(), ops.ReviewOpts{BeadID: beadID, Worktree: t.TempDir()})
	_ = d.ops.Review(context.Background(), ops.ReviewOpts{BeadID: "unrelated-review-bead", Worktree: t.TempDir()})
	waitFor(t, func() bool { return d.ops.HasActiveForBead(beadID) }, time.Second)
	waitFor(t, func() bool { return d.ops.HasActiveForBead("unrelated-review-bead") }, time.Second)

	d.checkHeartbeats(context.Background())
	if got := timedOutProcess.kills.Load(); got != 1 {
		t.Fatalf("review process kills = %d, want 1", got)
	}
	if d.ops.HasActiveForBead(beadID) {
		t.Fatal("timed-out review remained active")
	}
	if unrelatedProcess.kills.Load() != 0 || !d.ops.HasActiveForBead("unrelated-review-bead") {
		t.Fatal("unrelated review was cancelled")
	}
	if got := spawner.SpawnCount(); got != 2 {
		t.Fatalf("ops spawn count = %d, want 2 (no escalation process)", got)
	}
	if msgs := esc.Messages(); len(msgs) != 1 || !strings.Contains(msgs[0], string(protocol.EscStuckWorker)) {
		t.Fatalf("timeout escalation = %v, want one %s escalation", msgs, protocol.EscStuckWorker)
	}
	var persisted int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM escalations WHERE type = ? AND bead_id = ?`, protocol.EscStuckWorker, beadID).Scan(&persisted); err != nil {
		t.Fatalf("query persisted timeout escalation: %v", err)
	}
	if persisted != 1 {
		t.Fatalf("persisted timeout escalations = %d, want 1", persisted)
	}
}

func TestCheckHeartbeats_ReviewTimeoutPreservesSameBeadEscalation(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	reviewProcess := &countingReviewProcess{done: make(chan struct{})}
	escalationProcess := &countingReviewProcess{done: make(chan struct{})}
	spawner := &sequentialReviewSpawner{processes: []ops.Process{reviewProcess, escalationProcess}}
	d.ops = ops.NewSpawner(spawner)
	beadID := "review-timeout-preserve-escalation-bead"
	t.Cleanup(func() { _, _ = d.ops.CancelForBead(beadID) })
	d.cfg.ReviewTimeout = 100 * time.Millisecond
	now := time.Now()
	d.nowFunc = func() time.Time { return now }
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	d.mu.Lock()
	d.workers["review-timeout-preserve-escalation-worker"] = &trackedWorker{
		id: "review-timeout-preserve-escalation-worker", conn: server, state: protocol.WorkerReviewing,
		managed: true, beadID: beadID, lastSeen: now, lastProgress: now.Add(-d.cfg.ReviewTimeout - time.Second),
		encoder: json.NewEncoder(server),
	}
	d.mu.Unlock()
	_ = d.ops.Review(context.Background(), ops.ReviewOpts{BeadID: beadID, Worktree: t.TempDir()})
	waitFor(t, func() bool { return spawner.SpawnCount() == 1 }, time.Second)
	_ = d.ops.Escalate(context.Background(), ops.EscalationOpts{BeadID: beadID, Workdir: t.TempDir()})
	waitFor(t, func() bool { return spawner.SpawnCount() == 2 }, time.Second)

	d.checkHeartbeats(context.Background())
	if got := reviewProcess.kills.Load(); got != 1 {
		t.Fatalf("review process kills = %d, want 1", got)
	}
	if escalationProcess.kills.Load() != 0 || !d.ops.HasActiveForBead(beadID) {
		t.Fatal("same-bead escalation was cancelled")
	}
}

// TestCheckHeartbeats_ReviewingDefersToReviewTimeout verifies reviewing workers
// are not reaped by the shorter progress deadline.
func TestCheckHeartbeats_ReviewingDefersToReviewTimeout(t *testing.T) {
	t.Parallel()
	d, _, _, esc, _, _ := newTestDispatcher(t)

	d.cfg.ProgressTimeout = 100 * time.Millisecond
	d.cfg.ReviewTimeout = 500 * time.Millisecond

	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	staleConn := newMockConn()
	freshConn := newMockConn()
	staleWorkerID := "review-stale-progress"
	freshWorkerID := "review-fresh-progress"
	staleBeadID := "bead-review-stale"
	freshBeadID := "bead-review-fresh"

	d.mu.Lock()
	d.workers[staleWorkerID] = &trackedWorker{
		id:           staleWorkerID,
		conn:         staleConn,
		state:        protocol.WorkerReviewing,
		beadID:       staleBeadID,
		lastSeen:     now.Add(-10 * time.Millisecond),
		lastProgress: now.Add(-(d.cfg.ProgressTimeout + 100*time.Millisecond)),
		encoder:      json.NewEncoder(staleConn),
	}
	d.workers[freshWorkerID] = &trackedWorker{
		id:           freshWorkerID,
		conn:         freshConn,
		state:        protocol.WorkerReviewing,
		beadID:       freshBeadID,
		lastSeen:     now.Add(-10 * time.Millisecond),
		lastProgress: now.Add(-50 * time.Millisecond),
		encoder:      json.NewEncoder(freshConn),
	}
	d.mu.Unlock()

	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stalePresent := d.workers[staleWorkerID]
	_, freshPresent := d.workers[freshWorkerID]
	d.mu.Unlock()

	if !stalePresent {
		t.Error("reviewing worker older than ProgressTimeout should remain until ReviewTimeout")
	}
	if staleConn.closed {
		t.Error("reviewing worker older than ProgressTimeout should not have its connection closed")
	}
	if !freshPresent {
		t.Error("reviewing worker with recent real progress should remain active")
	}
	if freshConn.closed {
		t.Error("reviewing worker with recent real progress should not have its connection closed")
	}

	if msgs := esc.Messages(); len(msgs) != 0 {
		t.Fatalf("reviewing workers before ReviewTimeout should not escalate, got %v", msgs)
	}
}

// TestCheckHeartbeats_ReviewTimeout_ZeroLastProgressSkipped verifies that a
// reviewing worker with zero lastProgress is NOT removed.
func TestCheckHeartbeats_ReviewTimeout_ZeroLastProgressSkipped(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	d.cfg.ReviewTimeout = 100 * time.Millisecond

	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	conn := newMockConn()
	workerID := "review-zero-progress-worker"
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerReviewing,
		beadID:       "test-bead",
		lastSeen:     now.Add(-10 * time.Millisecond),
		lastProgress: time.Time{}, // zero time — should not trigger review timeout
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stillPresent := d.workers[workerID]
	d.mu.Unlock()

	if !stillPresent {
		t.Error("reviewing worker with zero lastProgress should not be removed")
	}
}

// TestCheckHeartbeats_ManagedReviewingWorkerWithDeadProcessIsRemoved verifies
// that a managed reviewing worker whose underlying process has exited is removed
// by checkHeartbeats even when heartbeat and review timeouts have not fired.
func TestCheckHeartbeats_ManagedReviewingWorkerWithDeadProcessIsRemoved(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	pm := &mockProcessManager{}
	d.procMgr = pm

	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	reviewingID := "reviewing-dead-process"
	conn := newMockConn()

	d.mu.Lock()
	d.workers[reviewingID] = &trackedWorker{
		id:           reviewingID,
		conn:         conn,
		state:        protocol.WorkerReviewing,
		beadID:       "bead-reviewing",
		lastSeen:     now.Add(-50 * time.Millisecond), // recent — heartbeat NOT timed out (timeout=500ms)
		lastProgress: now.Add(-50 * time.Millisecond), // recent — review timeout NOT triggered (timeout=15m)
		managed:      true,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	pm.MarkDead(reviewingID) // process has exited

	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stillPresent := d.workers[reviewingID]
	d.mu.Unlock()

	if stillPresent {
		t.Errorf("expected managed reviewing worker with dead process to be removed, but it was still present")
	}
}

// TestCheckHeartbeats_ReviewDeadProcessReaped verifies that a managed reviewing
// worker is retained for ReviewDeadGrace after its ops review disappears, then
// reaped exactly once so its assignment becomes recoverable.
func TestCheckHeartbeats_ReviewDeadProcessReaped(t *testing.T) {
	t.Parallel()
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)

	d.procMgr = &mockProcessManager{} // IsAlive returns true for all IDs.
	d.cfg.ReviewDeadGrace = 100 * time.Millisecond
	d.cfg.ReviewTimeout = time.Second

	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	workerID := "reviewing-live-process-dead-review"
	beadID := "bead-dead-review"
	conn := newMockConn()

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerReviewing,
		beadID:       beadID,
		lastSeen:     now,
		lastProgress: now.Add(-(d.cfg.ReviewTimeout + time.Second)),
		managed:      true,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	// d.ops.HasActiveForBead(beadID) returns false: no review was started for
	// this bead. The stale progress timestamp makes this fail if the missing
	// review's grace window is bypassed by review-timeout evaluation.
	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	w, stillPresent := d.workers[workerID]
	d.mu.Unlock()
	if !stillPresent {
		t.Fatal("reviewing worker was reaped before ReviewDeadGrace elapsed")
	}
	if !w.reviewDeadSince.Equal(now) {
		t.Fatalf("reviewDeadSince = %v, want %v", w.reviewDeadSince, now)
	}
	if conn.closed {
		t.Fatal("worker connection was closed before ReviewDeadGrace elapsed")
	}
	if msgs := esc.Messages(); len(msgs) != 0 {
		t.Fatalf("escalations before ReviewDeadGrace = %v, want none", msgs)
	}

	// Advance past grace while keeping the worker heartbeat fresh. The absent
	// ops review must now reap the worker without waiting for ReviewTimeout.
	now = now.Add(d.cfg.ReviewDeadGrace + time.Millisecond)
	d.mu.Lock()
	d.workers[workerID].lastSeen = now
	d.mu.Unlock()

	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stillPresent = d.workers[workerID]
	d.mu.Unlock()

	if stillPresent {
		t.Errorf("expected reviewing worker with dead review (past grace) to be removed, but it was still present")
	}
	if !conn.closed {
		t.Error("expected worker connection to be closed")
	}

	beadSrc.mu.Lock()
	status, hasUpdate := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()
	if !hasUpdate || status != "open" {
		t.Errorf("bead status = %q (updated=%v), want %q", status, hasUpdate, "open")
	}

	// Further heartbeat scans cannot reap or recover the same assignment again.
	d.checkHeartbeats(context.Background())
	if msgs := esc.Messages(); len(msgs) != 1 {
		t.Errorf("dead review escalations = %v, want exactly one", msgs)
	}
}

// TestCheckHeartbeats_ReviewDeadGracePrecedesReviewTimeout verifies that a
// newly absent ops review gets its configured grace period even when the
// worker's progress clock was already stale.
func TestCheckHeartbeats_ReviewDeadGracePrecedesReviewTimeout(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	d.procMgr = &mockProcessManager{}
	d.cfg.ReviewTimeout = time.Second
	d.cfg.ReviewDeadGrace = time.Minute

	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	workerID := "reviewing-newly-dead-review"
	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerReviewing,
		beadID:       "bead-newly-dead-review",
		lastSeen:     now,
		lastProgress: now.Add(-(d.cfg.ReviewTimeout + time.Second)),
		managed:      true,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	// No ops review is active. The first scan starts ReviewDeadGrace and must
	// not let the already-stale progress clock bypass that grace period.
	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	w, stillPresent := d.workers[workerID]
	d.mu.Unlock()

	if !stillPresent {
		t.Fatal("reviewing worker was reaped before ReviewDeadGrace elapsed")
	}
	if conn.closed {
		t.Error("worker connection was closed before ReviewDeadGrace elapsed")
	}
	if !w.reviewDeadSince.Equal(now) {
		t.Errorf("reviewDeadSince = %v, want %v", w.reviewDeadSince, now)
	}
	if got := eventCount(t, d.db, "progress_timeout"); got != 0 {
		t.Errorf("progress_timeout events = %d, want 0 during ReviewDeadGrace", got)
	}
}

// TestCheckHeartbeats_KillsManagedWorkerProcess verifies that checkHeartbeats calls
// procMgr.Kill for managed workers (heartbeat and progress timeout) but NOT for
// unmanaged workers. prevSession managed workers are still killed (the OS process
// is local even when bead tracking is stale).
func TestCheckHeartbeats_KillsManagedWorkerProcess(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	pm := &mockProcessManager{}
	d.procMgr = pm

	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	timeout := d.cfg.HeartbeatTimeout
	d.cfg.ProgressTimeout = 100 * time.Millisecond

	managedDeadConn := newMockConn()
	managedDeadID := "managed-dead"

	unmanagedDeadConn := newMockConn()
	unmanagedDeadID := "unmanaged-dead"

	prevSessionConn := newMockConn()
	prevSessionID := "managed-prevsession"

	managedStuckConn := newMockConn()
	managedStuckID := "managed-stuck"

	d.mu.Lock()
	d.workers[managedDeadID] = &trackedWorker{
		id:       managedDeadID,
		conn:     managedDeadConn,
		state:    protocol.WorkerBusy,
		beadID:   "bead-dead",
		lastSeen: now.Add(-(timeout + time.Second)),
		managed:  true,
		encoder:  json.NewEncoder(managedDeadConn),
	}
	d.workers[unmanagedDeadID] = &trackedWorker{
		id:       unmanagedDeadID,
		conn:     unmanagedDeadConn,
		state:    protocol.WorkerBusy,
		beadID:   "bead-unmanaged",
		lastSeen: now.Add(-(timeout + time.Second)),
		managed:  false,
		encoder:  json.NewEncoder(unmanagedDeadConn),
	}
	d.workers[prevSessionID] = &trackedWorker{
		id:          prevSessionID,
		conn:        prevSessionConn,
		state:       protocol.WorkerBusy,
		lastSeen:    now.Add(-(timeout + time.Second)),
		managed:     true,
		prevSession: true,
		encoder:     json.NewEncoder(prevSessionConn),
	}
	d.workers[managedStuckID] = &trackedWorker{
		id:           managedStuckID,
		conn:         managedStuckConn,
		state:        protocol.WorkerBusy,
		beadID:       "bead-stuck",
		lastSeen:     now.Add(-10 * time.Millisecond),
		lastProgress: now.Add(-(d.cfg.ProgressTimeout + time.Second)),
		managed:      true,
		encoder:      json.NewEncoder(managedStuckConn),
	}
	d.mu.Unlock()

	d.checkHeartbeats(context.Background())

	killed := pm.KilledIDs()
	killedSet := make(map[string]bool, len(killed))
	for _, id := range killed {
		killedSet[id] = true
	}

	if !killedSet[managedDeadID] {
		t.Errorf("expected procMgr.Kill(%q), killed=%v", managedDeadID, killed)
	}
	if !killedSet[prevSessionID] {
		t.Errorf("expected procMgr.Kill(%q) for prevSession managed worker, killed=%v", prevSessionID, killed)
	}
	if !killedSet[managedStuckID] {
		t.Errorf("expected procMgr.Kill(%q) for stuck managed worker, killed=%v", managedStuckID, killed)
	}
	if killedSet[unmanagedDeadID] {
		t.Errorf("procMgr.Kill should NOT be called for unmanaged worker %q", unmanagedDeadID)
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

func TestSendToWorker_TimesOutBlockedWrite(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	w := &trackedWorker{
		id:      "blocked-worker",
		conn:    serverConn,
		beadID:  "bead-blocked",
		encoder: json.NewEncoder(serverConn),
	}
	d.mu.Lock()
	d.workers[w.id] = w
	d.mu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		d.mu.Lock()
		errCh <- d.sendToWorker(w, protocol.Message{Type: protocol.MsgAssign, Assign: &protocol.AssignPayload{BeadID: "bead-blocked"}})
		d.mu.Unlock()
	}()

	select {
	case err := <-errCh:
		var unreachable *protocol.WorkerUnreachableError
		if !errors.As(err, &unreachable) {
			t.Fatalf("sendToWorker error = %T %[1]v, want WorkerUnreachableError", err)
		}
		if len(w.pendingMsgs) != 1 {
			t.Fatalf("pendingMsgs length = %d, want 1", len(w.pendingMsgs))
		}
	case <-time.After(directWorkerWriteTimeout * 4):
		_ = clientConn.Close()
		t.Fatal("sendToWorker blocked past direct worker write timeout")
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

// TestHeartbeatTimeoutIgnoresPrevSessionWorkers verifies that WORKER_CRASH
// escalation is suppressed for workers from a previous dispatcher session,
// while current-session workers still receive the alert (oro-x7ru).
// Workers are identified as previous-session when their embedded nanosecond
// timestamp predates d.startTime.
func TestHeartbeatTimeoutIgnoresPrevSessionWorkers(t *testing.T) {
	t.Parallel()
	d, _, _, esc, _, _ := newTestDispatcher(t)

	// Fix a session start time.
	sessionStart := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	d.mu.Lock()
	d.startTime = sessionStart
	d.mu.Unlock()

	// "now" is well past the heartbeat timeout from sessionStart.
	now := sessionStart.Add(10 * time.Minute)
	d.nowFunc = func() time.Time { return now }

	// Build worker IDs embedding nanosecond timestamps relative to sessionStart.
	// parseWorkerEpoch extracts \b(\d{15,19})\b from the ID.
	prevNano := sessionStart.Add(-1 * time.Second).UnixNano() // before session → prevSession=true
	currNano := sessionStart.Add(1 * time.Second).UnixNano()  // after session → prevSession=false
	prevWorkerID := fmt.Sprintf("worker-%d-0", prevNano)
	currWorkerID := fmt.Sprintf("worker-%d-0", currNano)

	conn1 := newMockConn()
	conn2 := newMockConn()

	// Register workers — upsertWorker computes prevSession from the embedded timestamp.
	d.mu.Lock()
	d.upsertWorker(prevWorkerID, conn1, false)
	d.upsertWorker(currWorkerID, conn2, false)

	// Verify prevSession flags before proceeding.
	if !d.workers[prevWorkerID].prevSession {
		t.Fatal("prevWorkerID should have prevSession=true")
	}
	if d.workers[currWorkerID].prevSession {
		t.Fatal("currWorkerID should have prevSession=false")
	}

	// Force both workers' lastSeen to be stale (past heartbeat timeout).
	stale := now.Add(-(d.cfg.HeartbeatTimeout + time.Second))
	d.workers[prevWorkerID].lastSeen = stale
	d.workers[currWorkerID].lastSeen = stale
	d.mu.Unlock()

	d.checkHeartbeats(context.Background())

	// Both workers should be removed (heartbeat timeout applies to all sessions).
	d.mu.Lock()
	_, prevStillPresent := d.workers[prevWorkerID]
	_, currStillPresent := d.workers[currWorkerID]
	d.mu.Unlock()

	if prevStillPresent {
		t.Error("previous-session worker should be removed by heartbeat timeout")
	}
	if currStillPresent {
		t.Error("current-session worker should be removed by heartbeat timeout")
	}

	// WORKER_CRASH must NOT be emitted for the previous-session worker.
	// WORKER_CRASH MUST be emitted for the current-session worker.
	msgs := esc.Messages()
	for _, msg := range msgs {
		if strings.Contains(msg, "WORKER_CRASH") && strings.Contains(msg, prevWorkerID) {
			t.Errorf("unexpected WORKER_CRASH alert for previous-session worker %q: %s", prevWorkerID, msg)
		}
	}
	var foundCurrCrash bool
	for _, msg := range msgs {
		if strings.Contains(msg, "WORKER_CRASH") && strings.Contains(msg, currWorkerID) {
			foundCurrCrash = true
		}
	}
	if !foundCurrCrash {
		t.Errorf("expected WORKER_CRASH alert for current-session worker %q, got messages: %v", currWorkerID, msgs)
	}
}

// TestPrevSessionWorkerDoesNotResetBead verifies that when a prev-session worker
// times out its heartbeat, the bead is NOT reset to "open". Only current-session
// workers should trigger a bead reset — prev-session workers carry stale bead
// assignments that may have already been closed (oro-p2ey).
func TestPrevSessionWorkerDoesNotResetBead(t *testing.T) {
	t.Parallel()
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	beadID := "bead-prev-session-reset"
	workerID := "w-prev-session-reset"

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:          workerID,
		conn:        server,
		state:       protocol.WorkerBusy,
		beadID:      beadID,
		worktree:    "/tmp/worktree-prev-session",
		lastSeen:    now,
		encoder:     json.NewEncoder(server),
		prevSession: true, // worker from a previous dispatcher session
	}
	d.mu.Unlock()

	// Advance time past HeartbeatTimeout to trigger dead-worker detection.
	d.nowFunc = func() time.Time { return now.Add(600 * time.Millisecond) }

	d.checkHeartbeats(context.Background())

	// Assert: beads.Update must NOT be called for a prev-session worker.
	// Calling it would reopen a bead that was already closed in a previous session.
	beadSrc.mu.Lock()
	_, ok := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()
	if ok {
		t.Fatal("beads.Update must not be called for a prev-session worker timeout; bead must not be reopened")
	}
}

// TestRegisterWorkerIncludesTargetBranch verifies that when a pendingHandoff
// has TargetBranch set, the ASSIGN message includes TargetBranch in the payload.
// When TargetBranch is empty, it must be omitted from the payload (backward compat).
func TestRegisterWorkerIncludesTargetBranch(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	workerID := "target-branch-worker"
	beadID := "target-branch-bead"
	targetBranch := "agent/target-branch"

	d.mu.Lock()
	d.pendingHandoffs[beadID] = &pendingHandoff{
		beadID:       beadID,
		worktree:     "/tmp/wt",
		model:        "haiku",
		targetBranch: targetBranch,
	}
	d.mu.Unlock()

	d.registerWorker(workerID, conn)

	if len(conn.written) != 1 {
		t.Fatalf("expected 1 message, got %d", len(conn.written))
	}

	var msg protocol.Message
	err := json.Unmarshal(conn.written[0], &msg)
	if err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}

	if msg.Type != protocol.MsgAssign {
		t.Errorf("message type = %v, want ASSIGN", msg.Type)
	}
	if msg.Assign == nil {
		t.Fatal("message Assign payload is nil")
	}

	if msg.Assign.TargetBranch != targetBranch {
		t.Errorf("TargetBranch = %q, want %q", msg.Assign.TargetBranch, targetBranch)
	}

	if msg.Assign.BeadID != beadID {
		t.Errorf("BeadID = %q, want %q", msg.Assign.BeadID, beadID)
	}
	if msg.Assign.Model != "haiku" {
		t.Errorf("Model = %q, want %q", msg.Assign.Model, "haiku")
	}
}

func TestHandoffPathCarriesRuntime(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	workerID := "runtime-handoff-worker"
	beadID := "runtime-handoff-bead"

	d.mu.Lock()
	d.pendingHandoffs[beadID] = &pendingHandoff{
		beadID:   beadID,
		worktree: "/tmp/wt",
		runtime:  "codex",
		model:    "gpt-5.5",
	}
	d.mu.Unlock()

	d.registerWorker(workerID, conn)

	if len(conn.written) != 1 {
		t.Fatalf("expected 1 message, got %d", len(conn.written))
	}

	var msg protocol.Message
	if err := json.Unmarshal(conn.written[0], &msg); err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}
	if msg.Assign == nil {
		t.Fatal("message Assign payload is nil")
	}
	if msg.Assign.Runtime != "codex" {
		t.Fatalf("Assign.Runtime = %q, want codex", msg.Assign.Runtime)
	}

	d.mu.Lock()
	runtime := d.workers[workerID].runtime
	d.mu.Unlock()
	if runtime != "codex" {
		t.Fatalf("trackedWorker.runtime = %q, want codex", runtime)
	}
}

// TestRegisterWorkerOmitsEmptyTargetBranch verifies that when TargetBranch is
// empty in the pendingHandoff, it is omitted from the ASSIGN payload.
func TestRegisterWorkerOmitsEmptyTargetBranch(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)

	conn := newMockConn()
	workerID := "no-target-branch-worker"
	beadID := "no-target-branch-bead"

	d.mu.Lock()
	d.pendingHandoffs[beadID] = &pendingHandoff{
		beadID:       beadID,
		worktree:     "/tmp/wt",
		model:        "sonnet",
		targetBranch: "",
	}
	d.mu.Unlock()

	d.registerWorker(workerID, conn)

	var msg protocol.Message
	err := json.Unmarshal(conn.written[0], &msg)
	if err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}

	if msg.Assign.TargetBranch != "" {
		t.Errorf("TargetBranch = %q, want empty string", msg.Assign.TargetBranch)
	}
}
