package dispatcher //nolint:testpackage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
)

type deadlineTrackingConn struct {
	*mockConn
	mu             sync.Mutex
	writeDeadlines []time.Time
}

func newDeadlineTrackingConn() *deadlineTrackingConn {
	return &deadlineTrackingConn{mockConn: newMockConn()}
}

func (c *deadlineTrackingConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadlines = append(c.writeDeadlines, t)
	return nil
}

func (c *deadlineTrackingConn) writeDeadlineCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.writeDeadlines)
}

func (c *deadlineTrackingConn) lastWriteDeadlineIsZero() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.writeDeadlines) == 0 {
		return false
	}
	return c.writeDeadlines[len(c.writeDeadlines)-1].IsZero()
}

type hookProcessManager struct {
	mockProcessManager
	onSpawn func(id string)
}

func (m *hookProcessManager) Spawn(id string) (*os.Process, error) {
	if m.onSpawn != nil {
		m.onSpawn(id)
	}
	return m.mockProcessManager.Spawn(id)
}

// TestSpawnFor_WorkerImmediatelyReceivesAssignment verifies that a worker
// spawned by the spawn-for directive receives an ASSIGN message without
// waiting for the background poll interval to fire.
//
// Bug: assignLoop only calls tryAssign on fsnotify events or poll ticks.
// A worker spawned by spawn-for connects and becomes idle, but no trigger
// fires tryAssign immediately — it waits until the next poll.
//
// Fix: registerWorker signals workerReadyCh when the worker becomes idle
// (no pending handoff). assignLoop/assignLoopPoll listen on this channel
// and call tryAssign immediately.
func TestSpawnFor_WorkerImmediatelyReceivesAssignment(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	// Use slow poll intervals so assignment can only happen via workerReadyCh.
	// Without the fix: worker sits idle for 10s before getting assigned.
	// With the fix: tryAssign fires immediately after registerWorker.
	d.cfg.PollInterval = 10 * time.Second
	d.cfg.FallbackPollInterval = 10 * time.Second

	beadID := "oro-spawnfor-test"
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: beadID, Priority: 2}})

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Send spawn-for directive: marks beadID as priority, spawns a worker.
	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "spawn-for", beadID)
	if !ack.OK {
		t.Fatalf("spawn-for directive failed: %s", ack.Detail)
	}

	// Wait for procMgr to record the spawned worker ID.
	waitFor(t, func() bool {
		return len(pm.SpawnedIDs()) > 0
	}, 1*time.Second)

	spawnedIDs := pm.SpawnedIDs()
	workerID := spawnedIDs[0]

	// Simulate the spawned worker connecting and sending its first heartbeat.
	// (Real workers send a heartbeat as their first message; extractWorkerID
	// pulls the ID from the heartbeat, then registerWorker is called.)
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: workerID, ContextPct: 5},
	})

	// ASSERTION: Worker receives ASSIGN within 3s.
	// Without the fix: must wait up to 10s for the poll to fire.
	// With the fix: triggered by workerReadyCh immediately after registerWorker.
	msg, ok := readMsg(t, conn, 3*time.Second)
	if !ok || msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN within 3s, got ok=%v type=%v", ok, msg.Type)
	}
	if msg.Assign == nil || msg.Assign.BeadID != beadID {
		t.Fatalf("expected bead %s assigned, got %v", beadID, msg.Assign)
	}

	// Verify worker is now busy with the target bead.
	st, bid, ok := d.WorkerInfo(workerID)
	if !ok || st != protocol.WorkerBusy || bid != beadID {
		t.Fatalf("expected worker=%s busy with bead=%s, got state=%s bead=%s ok=%v",
			workerID, beadID, st, bid, ok)
	}
}

func TestSpawnFor_TargetedWorkerDoesNotReceiveDifferentReadyBead(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	d.cfg.PollInterval = 10 * time.Second
	d.cfg.FallbackPollInterval = 10 * time.Second

	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: otherID, Priority: 0}})

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "spawn-for", requestedID)
	if !ack.OK {
		t.Fatalf("spawn-for directive failed: %s", ack.Detail)
	}
	waitFor(t, func() bool {
		return len(pm.SpawnedIDs()) > 0
	}, 1*time.Second)

	workerID := pm.SpawnedIDs()[0]
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: workerID, ContextPct: 5},
	})

	if msg, ok := readMsg(t, conn, 300*time.Millisecond); ok && msg.Type == protocol.MsgAssign {
		got := "<nil>"
		if msg.Assign != nil {
			got = msg.Assign.BeadID
		}
		t.Fatalf("targeted spawn-for worker was assigned %q, want no assignment until %q is ready", got, requestedID)
	}

	st, bid, ok := d.WorkerInfo(workerID)
	if !ok || st != protocol.WorkerIdle || bid != "" {
		t.Fatalf("expected targeted worker to remain idle, got state=%s bead=%q ok=%v", st, bid, ok)
	}
}

func TestSpawnFor_TargetRegisteredBeforeSpawnedWorkerCanConnect(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	d.cfg.MaxWorkers = 1
	d.mu.Lock()
	d.targetWorkers = 0
	d.mu.Unlock()
	d.setState(StateRunning)

	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	workerConn := newMockConn()
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: otherID, Priority: 0}})

	pm := &hookProcessManager{}
	pm.onSpawn = func(id string) {
		d.registerWorker(id, workerConn)
	}
	d.procMgr = pm

	if _, err := d.applySpawnFor(requestedID); err != nil {
		t.Fatalf("applySpawnFor failed: %v", err)
	}
	for _, workerID := range pm.SpawnedIDs() {
		d.mu.Lock()
		target := d.workers[workerID].targetBeadID
		managed := d.workers[workerID].managed
		spawnFor := d.workers[workerID].spawnFor
		workerCount := len(d.workers)
		d.mu.Unlock()
		if target != requestedID {
			t.Fatalf("spawned worker=%s target=%q, want %q before assignment", workerID, target, requestedID)
		}
		if !managed {
			t.Fatalf("spawned worker=%s is unmanaged before assignment", workerID)
		}
		if !spawnFor {
			t.Fatalf("spawned worker=%s is not marked spawn-for", workerID)
		}
		if workerCount != 1 {
			t.Fatalf("expected exactly 1 worker before assignment, got %d", workerCount)
		}
	}
	if len(workerConn.written) != 0 {
		t.Fatalf("spawn-for worker received %d message(s) during registration, want none", len(workerConn.written))
	}
	d.tryAssign(context.Background())

	if len(workerConn.written) != 0 {
		var msg protocol.Message
		_ = json.Unmarshal(workerConn.written[0], &msg)
		t.Fatalf("spawn-for worker received %d message(s), first type=%s assign=%v; want none until %s is ready",
			len(workerConn.written), msg.Type, msg.Assign, requestedID)
	}
	for _, workerID := range pm.SpawnedIDs() {
		st, bid, ok := d.WorkerInfo(workerID)
		if !ok || st != protocol.WorkerIdle || bid != "" {
			t.Fatalf("expected spawned worker=%s idle with no bead, got state=%s bead=%q ok=%v", workerID, st, bid, ok)
		}
	}
}

func TestSpawnFor_TargetedWorkerDoesNotConsumeUnrelatedPendingHandoff(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	workerID := "worker-spawnfor-test"
	requestedID := "oro-spawnfor-requested"
	handoffID := "oro-handoff-other"
	conn := newMockConn()

	d.mu.Lock()
	d.pendingManagedIDs[workerID] = true
	d.pendingWorkerTargets[workerID] = requestedID
	d.pendingHandoffs[handoffID] = &pendingHandoff{
		beadID:   handoffID,
		worktree: "/tmp/worktree-" + handoffID,
		model:    "haiku",
	}
	d.mu.Unlock()

	d.registerWorker(workerID, conn)

	if len(conn.written) != 0 {
		t.Fatalf("targeted worker received unrelated handoff assignment, wrote %d message(s)", len(conn.written))
	}

	d.mu.Lock()
	w := d.workers[workerID]
	_, handoffStillPending := d.pendingHandoffs[handoffID]
	d.mu.Unlock()
	if w == nil {
		t.Fatal("expected worker to remain registered")
	}
	if w.state != protocol.WorkerIdle || w.beadID != "" || w.targetBeadID != requestedID {
		t.Fatalf("expected targeted worker idle for %s, got state=%s bead=%q target=%q",
			requestedID, w.state, w.beadID, w.targetBeadID)
	}
	if !handoffStillPending {
		t.Fatalf("unrelated handoff %s was consumed by targeted worker", handoffID)
	}
}

func TestSpawnFor_ReconnectingTargetedWorkerDoesNotConsumeUnrelatedPendingHandoff(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	workerID := "worker-spawnfor-test"
	requestedID := "oro-spawnfor-requested"
	handoffID := "oro-handoff-other"
	oldConn := newMockConn()
	newConn := newMockConn()

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         oldConn,
		state:        protocol.WorkerIdle,
		encoder:      json.NewEncoder(oldConn),
		managed:      true,
		targetBeadID: requestedID,
	}
	d.pendingHandoffs[handoffID] = &pendingHandoff{
		beadID:   handoffID,
		worktree: "/tmp/worktree-" + handoffID,
		model:    "haiku",
	}
	d.mu.Unlock()

	d.registerWorker(workerID, newConn)

	if len(newConn.written) != 0 {
		t.Fatalf("reconnected targeted worker received unrelated handoff assignment, wrote %d message(s)", len(newConn.written))
	}

	d.mu.Lock()
	w := d.workers[workerID]
	_, handoffStillPending := d.pendingHandoffs[handoffID]
	d.mu.Unlock()
	if w == nil {
		t.Fatal("expected worker to remain registered")
	}
	if w.state != protocol.WorkerIdle || w.beadID != "" || w.targetBeadID != requestedID {
		t.Fatalf("expected reconnected targeted worker idle for %s, got state=%s bead=%q target=%q",
			requestedID, w.state, w.beadID, w.targetBeadID)
	}
	if !handoffStillPending {
		t.Fatalf("unrelated handoff %s was consumed by reconnected targeted worker", handoffID)
	}
}

func TestSpawnFor_PendingTargetIsNotAssignedToGeneralIdleWorker(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)

	requestedID := "oro-spawnfor-requested"
	generalWorkerID := "worker-general-idle"
	generalConn := newMockConn()
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: requestedID, Priority: 0}})

	d.mu.Lock()
	d.targetWorkers = 1
	d.pendingManagedIDs["worker-spawnfor-pending"] = true
	d.pendingSpawnForWorkers["worker-spawnfor-pending"] = true
	d.pendingWorkerTargets["worker-spawnfor-pending"] = requestedID
	d.priorityBeads[requestedID] = true
	d.workers[generalWorkerID] = &trackedWorker{
		id:      generalWorkerID,
		conn:    generalConn,
		state:   protocol.WorkerIdle,
		encoder: json.NewEncoder(generalConn),
		managed: false,
	}
	d.mu.Unlock()

	d.tryAssign(context.Background())

	if len(generalConn.written) != 0 {
		var msg protocol.Message
		_ = json.Unmarshal(generalConn.written[0], &msg)
		t.Fatalf("general worker received pending spawn-for bead assignment: type=%s assign=%v", msg.Type, msg.Assign)
	}
	st, bid, ok := d.WorkerInfo(generalWorkerID)
	if !ok || st != protocol.WorkerIdle || bid != "" {
		t.Fatalf("expected general worker to remain idle, got state=%s bead=%q ok=%v", st, bid, ok)
	}
}

func TestSpawnFor_StalePendingTargetDoesNotReserveBeadForever(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)

	now := time.Date(2026, 5, 1, 4, 0, 0, 0, time.UTC)
	d.nowFunc = func() time.Time { return now }
	d.cfg.HeartbeatTimeout = time.Second

	requestedID := "oro-spawnfor-requested"
	staleWorkerID := "worker-spawnfor-stale"
	generalWorkerID := "worker-general-idle"
	generalConn := newMockConn()
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: requestedID, Priority: 0}})

	d.mu.Lock()
	d.targetWorkers = 1
	d.pendingManagedIDs[staleWorkerID] = true
	d.pendingSpawnForWorkers[staleWorkerID] = true
	d.pendingManagedSince[staleWorkerID] = now.Add(-2 * time.Second)
	d.pendingWorkerTargets[staleWorkerID] = requestedID
	d.priorityBeads[requestedID] = true
	d.workers[generalWorkerID] = &trackedWorker{
		id:      generalWorkerID,
		conn:    generalConn,
		state:   protocol.WorkerIdle,
		encoder: json.NewEncoder(generalConn),
		managed: false,
	}
	d.mu.Unlock()

	tryAssignAndWait(t, d, context.Background())

	if len(generalConn.written) == 0 {
		t.Fatal("stale pending spawn-for target reserved bead forever; general idle worker received no assignment")
	}
	var msg protocol.Message
	if err := json.Unmarshal(generalConn.written[0], &msg); err != nil {
		t.Fatalf("unmarshal assignment: %v", err)
	}
	if msg.Type != protocol.MsgAssign || msg.Assign == nil || msg.Assign.BeadID != requestedID {
		t.Fatalf("expected general worker assigned stale spawn-for bead %s, got type=%s assign=%v",
			requestedID, msg.Type, msg.Assign)
	}

	d.mu.Lock()
	_, pendingManaged := d.pendingManagedIDs[staleWorkerID]
	_, pendingSince := d.pendingManagedSince[staleWorkerID]
	_, pendingTarget := d.pendingWorkerTargets[staleWorkerID]
	_, pendingSpawnFor := d.pendingSpawnForWorkers[staleWorkerID]
	exits := d.unexpectedManagedExits
	d.mu.Unlock()
	if pendingManaged || pendingSince || pendingTarget || pendingSpawnFor {
		t.Fatalf("stale pending worker was not fully cleared: managed=%v since=%v target=%v spawnFor=%v",
			pendingManaged, pendingSince, pendingTarget, pendingSpawnFor)
	}
	if exits != 0 {
		t.Fatalf("expected stale spawn-for worker to stay out of general managed-exit cap, got %d", exits)
	}
}

func TestSpawnFor_TargetClearedAfterMatchingPendingHandoffAssignment(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	workerID := "worker-spawnfor-test"
	requestedID := "oro-spawnfor-requested"
	conn := newMockConn()

	d.mu.Lock()
	d.pendingManagedIDs[workerID] = true
	d.pendingWorkerTargets[workerID] = requestedID
	d.pendingHandoffs[requestedID] = &pendingHandoff{
		beadID:   requestedID,
		worktree: "/tmp/worktree-" + requestedID,
		model:    "haiku",
	}
	d.mu.Unlock()

	d.registerWorker(workerID, conn)

	d.mu.Lock()
	w := d.workers[workerID]
	_, handoffStillPending := d.pendingHandoffs[requestedID]
	d.mu.Unlock()
	if w == nil {
		t.Fatal("expected worker to remain registered")
	}
	if w.state != protocol.WorkerBusy || w.beadID != requestedID || w.targetBeadID != "" {
		t.Fatalf("expected busy worker on %s with cleared target, got state=%s bead=%q target=%q",
			requestedID, w.state, w.beadID, w.targetBeadID)
	}
	if handoffStillPending {
		t.Fatalf("matching handoff %s should be consumed", requestedID)
	}
}

func TestSpawnFor_TargetedIdleWorkerDoesNotBlockAutoscaleForOtherReadyBead(t *testing.T) {
	d, beads, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.setState(StateRunning)

	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	beads.SetBeads([]protocol.Bead{{ID: otherID, Priority: 0}})

	d.mu.Lock()
	d.targetWorkers = 1
	d.workers["worker-spawnfor-test"] = &trackedWorker{
		id:           "worker-spawnfor-test",
		state:        protocol.WorkerIdle,
		managed:      true,
		spawnFor:     true,
		targetBeadID: requestedID,
	}
	d.mu.Unlock()

	d.tryAssign(context.Background())

	waitFor(t, func() bool {
		return len(pm.SpawnedIDs()) == 1
	}, 1*time.Second)
}

func TestSpawnFor_TargetedIdleWorkerAddsOneGeneralWorkerForOtherReadyBead(t *testing.T) {
	d, beads, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.setState(StateRunning)
	d.cfg.MaxWorkers = 3

	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	beads.SetBeads([]protocol.Bead{{ID: otherID, Priority: 0}})

	d.mu.Lock()
	d.targetWorkers = 0
	d.workers["worker-spawnfor-test"] = &trackedWorker{
		id:           "worker-spawnfor-test",
		state:        protocol.WorkerIdle,
		managed:      true,
		spawnFor:     true,
		targetBeadID: requestedID,
	}
	d.mu.Unlock()

	d.tryAssign(context.Background())

	waitFor(t, func() bool {
		return len(pm.SpawnedIDs()) >= 1
	}, 1*time.Second)
	if got := len(pm.SpawnedIDs()); got != 1 {
		t.Fatalf("spawn-for targeted idle worker should cause exactly one general worker for one unrelated bead, got %d: %v",
			got, pm.SpawnedIDs())
	}
	d.mu.Lock()
	targetWorkers := d.targetWorkers
	d.mu.Unlock()
	if targetWorkers != 1 {
		t.Fatalf("targetWorkers = %d, want 1 for the unrelated ready bead only", targetWorkers)
	}
}

func TestSpawnFor_DoesNotAutoscaleGeneralWorkerWhenManualPoolDisabled(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.setState(StateRunning)
	d.cfg.MaxWorkers = 0
	d.cfg.HeartbeatTimeout = time.Second

	now := time.Date(2026, 5, 1, 5, 0, 0, 0, time.UTC)
	d.nowFunc = func() time.Time { return now }

	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: otherID, Priority: 0}})

	d.mu.Lock()
	d.targetWorkers = 0
	d.mu.Unlock()

	if _, err := d.applySpawnFor(requestedID); err != nil {
		t.Fatalf("applySpawnFor failed: %v", err)
	}
	if got := len(pm.SpawnedIDs()); got != 1 {
		t.Fatalf("spawn-for should spawn exactly one targeted worker, got %d", got)
	}
	workerID := pm.SpawnedIDs()[0]
	conn := newMockConn()
	d.registerWorker(workerID, conn)

	d.tryAssign(context.Background())
	if got := len(pm.SpawnedIDs()); got != 1 {
		t.Fatalf("manual pool spawned unrelated general worker while spawn-for target was idle; spawned=%v", pm.SpawnedIDs())
	}
	if len(conn.written) != 0 {
		var msg protocol.Message
		_ = json.Unmarshal(conn.written[0], &msg)
		t.Fatalf("spawn-for worker received unrelated assignment: type=%s assign=%v", msg.Type, msg.Assign)
	}

	now = now.Add(2 * time.Second)
	d.checkHeartbeats(context.Background())
	d.tryAssign(context.Background())

	if got := len(pm.SpawnedIDs()); got != 1 {
		t.Fatalf("manual pool spawned unrelated general worker after spawn-for worker exit; spawned=%v", pm.SpawnedIDs())
	}
	d.mu.Lock()
	targetWorkers := d.targetWorkers
	managedCount := d.managedWorkerCountLocked()
	d.mu.Unlock()
	if targetWorkers != 0 {
		t.Fatalf("targetWorkers = %d, want 0 after spawn-for in manual mode", targetWorkers)
	}
	if managedCount != 0 {
		t.Fatalf("general managed worker count = %d, want 0", managedCount)
	}
}

func TestSpawnFor_RejectsWhenTotalWorkersAtMaxWorkers(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.setState(StateRunning)
	d.cfg.MaxWorkers = 2

	for i := 0; i < 2; i++ {
		workerID := fmt.Sprintf("worker-%d", i)
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

	_, err := d.applySpawnFor("oro-cap-target")
	if err == nil {
		t.Fatal("expected spawn-for to reject when total workers already reached MaxWorkers")
	}
	if !strings.Contains(err.Error(), "max workers reached") {
		t.Fatalf("expected max workers error, got %v", err)
	}
	if got := len(pm.SpawnedIDs()); got != 0 {
		t.Fatalf("spawn-for spawned %d workers despite MaxWorkers cap", got)
	}

	d.mu.Lock()
	priority := d.priorityBeads["oro-cap-target"]
	pendingCount := len(d.pendingManagedIDs) + len(d.pendingWorkerTargets) + len(d.pendingSpawnForWorkers)
	d.mu.Unlock()
	if priority {
		t.Fatal("spawn-for left capped bead in priorityBeads")
	}
	if pendingCount != 0 {
		t.Fatalf("spawn-for left pending worker state behind: %d pending entries", pendingCount)
	}
}

func TestSpawnFor_DoneShutsDownOneShotWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	workerID := "worker-spawnfor-test"
	beadID := "oro-spawnfor-requested"
	conn := newMockConn()

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerBusy,
		beadID:       beadID,
		assignmentID: 42,
		managed:      true,
		spawnFor:     true,
		encoder:      json.NewEncoder(conn),
	}
	d.mu.Unlock()

	d.handleDone(context.Background(), workerID, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            beadID,
			WorkerID:          workerID,
			QualityGatePassed: true,
		},
	})

	d.mu.Lock()
	w := d.workers[workerID]
	d.mu.Unlock()
	if w == nil {
		t.Fatal("spawn-for worker should remain tracked until it disconnects")
	}
	if w.state != protocol.WorkerShuttingDown {
		t.Fatalf("spawn-for worker state = %s, want %s", w.state, protocol.WorkerShuttingDown)
	}
	if w.beadID != "" || w.assignmentID != 0 || w.targetBeadID != "" {
		t.Fatalf("spawn-for worker tracking not cleared: bead=%q assignment=%d target=%q",
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

func TestSpawnFor_KillIdleWorkerShutsDownWithoutGeneralAssignment(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)

	workerID := "worker-spawnfor-test"
	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	conn := newMockConn()
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: otherID, Priority: 0}})

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerIdle,
		managed:      true,
		spawnFor:     true,
		targetBeadID: requestedID,
		encoder:      json.NewEncoder(conn),
	}
	d.targetWorkers = 0
	d.mu.Unlock()

	if _, err := d.applyKillWorker(workerID); err != nil {
		t.Fatalf("applyKillWorker returned error: %v", err)
	}
	d.tryAssign(context.Background())

	d.mu.Lock()
	w := d.workers[workerID]
	d.mu.Unlock()
	if w == nil {
		t.Fatal("spawn-for worker should remain tracked until it disconnects")
	}
	if w.state != protocol.WorkerShuttingDown {
		t.Fatalf("spawn-for worker state = %s, want %s", w.state, protocol.WorkerShuttingDown)
	}
	if w.beadID != "" || w.assignmentID != 0 || w.targetBeadID != "" {
		t.Fatalf("spawn-for worker tracking not cleared: bead=%q assignment=%d target=%q",
			w.beadID, w.assignmentID, w.targetBeadID)
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.written) != 1 {
		t.Fatalf("expected one shutdown message and no assignment, got %d messages", len(conn.written))
	}
	var msg protocol.Message
	if err := json.Unmarshal(conn.written[0], &msg); err != nil {
		t.Fatalf("decode shutdown message: %v", err)
	}
	if msg.Type != protocol.MsgShutdown {
		t.Fatalf("message type = %s, want %s", msg.Type, protocol.MsgShutdown)
	}
}

func TestSpawnFor_StopIdleDoesNotAssignGeneralWork(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)

	workerID := "worker-spawnfor-stop"
	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	conn := newMockConn()
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: otherID, Priority: 0}})

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerIdle,
		managed:      true,
		spawnFor:     true,
		targetBeadID: requestedID,
		encoder:      json.NewEncoder(conn),
	}
	d.targetWorkers = 0
	d.mu.Unlock()

	d.GracefulShutdownWorker(workerID, time.Hour)
	d.tryAssign(context.Background())

	d.mu.Lock()
	w := d.workers[workerID]
	d.mu.Unlock()
	if w == nil {
		t.Fatal("spawn-for worker should remain tracked during graceful stop")
	}
	if w.state != protocol.WorkerShuttingDown {
		t.Fatalf("spawn-for worker state = %s, want %s", w.state, protocol.WorkerShuttingDown)
	}
	if w.beadID != "" || w.assignmentID != 0 || w.targetBeadID != requestedID {
		t.Fatalf("spawn-for stop mutated assignment unexpectedly: bead=%q assignment=%d target=%q",
			w.beadID, w.assignmentID, w.targetBeadID)
	}

	d.handleShutdownApproved(context.Background(), workerID, protocol.Message{
		Type: protocol.MsgShutdownApproved,
		ShutdownApproved: &protocol.ShutdownApprovedPayload{
			WorkerID: workerID,
		},
	})
	d.tryAssign(context.Background())

	d.mu.Lock()
	w = d.workers[workerID]
	d.mu.Unlock()
	if w == nil {
		t.Fatal("spawn-for worker should remain tracked until it disconnects")
	}
	if w.state != protocol.WorkerShuttingDown {
		t.Fatalf("spawn-for worker state after approval = %s, want %s", w.state, protocol.WorkerShuttingDown)
	}
	if w.beadID != "" || w.assignmentID != 0 || w.targetBeadID != "" {
		t.Fatalf("spawn-for worker tracking not cleared after approval: bead=%q assignment=%d target=%q",
			w.beadID, w.assignmentID, w.targetBeadID)
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.written) != 2 {
		t.Fatalf("expected prepare-shutdown and shutdown messages, got %d", len(conn.written))
	}
	var prepare, shutdown protocol.Message
	if err := json.Unmarshal(conn.written[0], &prepare); err != nil {
		t.Fatalf("decode prepare-shutdown message: %v", err)
	}
	if prepare.Type != protocol.MsgPrepareShutdown {
		t.Fatalf("first message type = %s, want %s", prepare.Type, protocol.MsgPrepareShutdown)
	}
	if err := json.Unmarshal(conn.written[1], &shutdown); err != nil {
		t.Fatalf("decode shutdown message: %v", err)
	}
	if shutdown.Type != protocol.MsgShutdown {
		t.Fatalf("second message type = %s, want %s", shutdown.Type, protocol.MsgShutdown)
	}
}

func TestSpawnFor_DirectShutdownWritesUseDeadline(t *testing.T) {
	conn := newDeadlineTrackingConn()
	w := &trackedWorker{
		id:   "worker-spawnfor-deadline",
		conn: conn,
	}

	sendShutdownWithoutBuffering(w)
	sendPrepareShutdownWithoutBuffering(w, time.Second)

	if got := conn.writeDeadlineCount(); got != 4 {
		t.Fatalf("direct writes should set and clear write deadlines, got %d deadline calls", got)
	}
	if !conn.lastWriteDeadlineIsZero() {
		t.Fatal("direct writes should clear the write deadline after sending")
	}
}

func TestSpawnFor_StopIdleSendFailureReconnectDoesNotAssignGeneralWork(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)

	workerID := "worker-spawnfor-stop-reconnect"
	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	conn := newMockConn()
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: otherID, Priority: 0}})

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerIdle,
		managed:      true,
		spawnFor:     true,
		targetBeadID: requestedID,
		encoder:      json.NewEncoder(conn),
		pendingMsgs:  make([]protocol.Message, maxPendingMessages),
	}
	d.targetWorkers = 0
	d.mu.Unlock()

	if err := conn.Close(); err != nil {
		t.Fatalf("close mock connection: %v", err)
	}
	d.GracefulShutdownWorker(workerID, time.Hour)

	d.mu.Lock()
	if _, ok := d.workers[workerID]; !ok {
		d.mu.Unlock()
		t.Fatal("spawn-for worker should remain tracked when prepare-shutdown write fails")
	}
	d.pendingHandoffs[otherID] = &pendingHandoff{
		beadID:   otherID,
		worktree: "/tmp/worktree-handoff",
		model:    "test-model",
	}
	d.mu.Unlock()

	reconnectConn := newMockConn()
	d.registerWorker(workerID, reconnectConn)
	d.handleReconnect(context.Background(), workerID, protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID: workerID,
			State:    "idle",
		},
	})
	d.tryAssign(context.Background())

	d.mu.Lock()
	w := d.workers[workerID]
	if w == nil {
		d.mu.Unlock()
		t.Fatal("spawn-for worker should remain tracked after reconnect")
	}
	if w.state != protocol.WorkerShuttingDown {
		d.mu.Unlock()
		t.Fatalf("spawn-for worker state after reconnect = %s, want %s", w.state, protocol.WorkerShuttingDown)
	}
	if w.beadID != "" || w.assignmentID != 0 || w.targetBeadID != "" {
		d.mu.Unlock()
		t.Fatalf("stopped spawn-for worker reconnected into assignable state: bead=%q assignment=%d target=%q",
			w.beadID, w.assignmentID, w.targetBeadID)
	}
	if _, ok := d.pendingHandoffs[otherID]; !ok {
		d.mu.Unlock()
		t.Fatalf("stopped spawn-for worker consumed unrelated pending handoff %q", otherID)
	}
	d.mu.Unlock()

	reconnectConn.mu.Lock()
	defer reconnectConn.mu.Unlock()
	if len(reconnectConn.written) != 2 {
		t.Fatalf("expected shutdown on reconnect and no assignment, got %d messages", len(reconnectConn.written))
	}
	for i, written := range reconnectConn.written {
		var msg protocol.Message
		if err := json.Unmarshal(written, &msg); err != nil {
			t.Fatalf("decode reconnect shutdown message %d: %v", i, err)
		}
		if msg.Type != protocol.MsgShutdown {
			t.Fatalf("reconnect message %d type = %s, want %s", i, msg.Type, protocol.MsgShutdown)
		}
	}
}

func TestSpawnFor_KillIdleSendFailureReconnectDoesNotAssignGeneralWork(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)

	workerID := "worker-spawnfor-reconnect"
	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	conn := newMockConn()
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: otherID, Priority: 0}})

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerIdle,
		managed:      true,
		spawnFor:     true,
		targetBeadID: requestedID,
		encoder:      json.NewEncoder(conn),
		pendingMsgs:  make([]protocol.Message, maxPendingMessages),
	}
	d.targetWorkers = 0
	d.mu.Unlock()

	if err := conn.Close(); err != nil {
		t.Fatalf("close mock connection: %v", err)
	}
	if _, err := d.applyKillWorker(workerID); err != nil {
		t.Fatalf("applyKillWorker returned error: %v", err)
	}

	d.mu.Lock()
	d.pendingHandoffs[otherID] = &pendingHandoff{
		beadID:   otherID,
		worktree: "/tmp/worktree-handoff",
		model:    "test-model",
	}
	d.mu.Unlock()

	reconnectConn := newMockConn()
	d.registerWorker(workerID, reconnectConn)
	d.handleReconnect(context.Background(), workerID, protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID: workerID,
			State:    "idle",
		},
	})
	d.tryAssign(context.Background())

	d.mu.Lock()
	w := d.workers[workerID]
	if w == nil {
		d.mu.Unlock()
		t.Fatal("spawn-for worker should remain tracked after reconnect")
	}
	if w.state != protocol.WorkerShuttingDown {
		d.mu.Unlock()
		t.Fatalf("spawn-for worker state after reconnect = %s, want %s", w.state, protocol.WorkerShuttingDown)
	}
	if w.beadID != "" || w.assignmentID != 0 || w.targetBeadID != "" {
		d.mu.Unlock()
		t.Fatalf("spawn-for worker reconnected into assignable state: bead=%q assignment=%d target=%q",
			w.beadID, w.assignmentID, w.targetBeadID)
	}
	if _, ok := d.pendingHandoffs[otherID]; !ok {
		d.mu.Unlock()
		t.Fatalf("stopped spawn-for worker consumed unrelated pending handoff %q", otherID)
	}
	d.mu.Unlock()

	reconnectConn.mu.Lock()
	defer reconnectConn.mu.Unlock()
	if len(reconnectConn.written) != 2 {
		t.Fatalf("expected shutdown on reconnect and no assignment, got %d messages", len(reconnectConn.written))
	}
	for i, written := range reconnectConn.written {
		var msg protocol.Message
		if err := json.Unmarshal(written, &msg); err != nil {
			t.Fatalf("decode reconnect shutdown message %d: %v", i, err)
		}
		if msg.Type != protocol.MsgShutdown {
			t.Fatalf("reconnect message %d type = %s, want %s", i, msg.Type, protocol.MsgShutdown)
		}
	}
}

func TestSpawnFor_KillBusyReconnectDoesNotResumeWork(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)

	workerID := "worker-spawnfor-busy-reconnect"
	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	conn := newMockConn()
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: otherID, Priority: 0}})

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerBusy,
		managed:      true,
		spawnFor:     true,
		beadID:       requestedID,
		targetBeadID: requestedID,
		encoder:      json.NewEncoder(conn),
	}
	d.targetWorkers = 0
	d.mu.Unlock()

	if _, err := d.applyKillWorker(workerID); err != nil {
		t.Fatalf("applyKillWorker returned error: %v", err)
	}

	reconnectConn := newMockConn()
	d.registerWorker(workerID, reconnectConn)
	d.handleReconnect(context.Background(), workerID, protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID: workerID,
			BeadID:   requestedID,
			State:    "running",
		},
	})
	d.tryAssign(context.Background())

	d.mu.Lock()
	w := d.workers[workerID]
	if w == nil {
		d.mu.Unlock()
		t.Fatal("spawn-for worker should remain tracked after busy reconnect")
	}
	if w.state != protocol.WorkerShuttingDown {
		d.mu.Unlock()
		t.Fatalf("spawn-for worker state after busy reconnect = %s, want %s", w.state, protocol.WorkerShuttingDown)
	}
	if w.beadID != "" || w.assignmentID != 0 || w.targetBeadID != "" {
		d.mu.Unlock()
		t.Fatalf("stopped spawn-for worker resumed assignment: bead=%q assignment=%d target=%q",
			w.beadID, w.assignmentID, w.targetBeadID)
	}
	d.mu.Unlock()

	reconnectConn.mu.Lock()
	defer reconnectConn.mu.Unlock()
	if len(reconnectConn.written) != 2 {
		t.Fatalf("expected shutdown on busy reconnect and no assignment, got %d messages", len(reconnectConn.written))
	}
	for i, written := range reconnectConn.written {
		var msg protocol.Message
		if err := json.Unmarshal(written, &msg); err != nil {
			t.Fatalf("decode reconnect shutdown message %d: %v", i, err)
		}
		if msg.Type != protocol.MsgShutdown {
			t.Fatalf("reconnect message %d type = %s, want %s", i, msg.Type, protocol.MsgShutdown)
		}
	}
}

func TestSpawnFor_StopCleanupBeforeReconnectPreservesShutdownState(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)

	workerID := "worker-spawnfor-cleanup-reconnect"
	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	conn := newMockConn()
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: otherID, Priority: 0}})

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		state:        protocol.WorkerIdle,
		managed:      true,
		spawnFor:     true,
		targetBeadID: requestedID,
		encoder:      json.NewEncoder(conn),
	}
	d.targetWorkers = 0
	d.mu.Unlock()

	d.GracefulShutdownWorker(workerID, time.Hour)
	d.connCloseCleanup(workerID, conn)

	d.mu.Lock()
	if w := d.workers[workerID]; w == nil || w.state != protocol.WorkerShuttingDown || !w.spawnFor {
		d.mu.Unlock()
		t.Fatalf("conn cleanup lost stopped spawn-for metadata: worker=%#v", w)
	}
	d.pendingHandoffs[otherID] = &pendingHandoff{
		beadID:   otherID,
		worktree: "/tmp/worktree-handoff",
		model:    "test-model",
	}
	d.mu.Unlock()

	reconnectConn := newMockConn()
	d.registerWorker(workerID, reconnectConn)
	d.handleReconnect(context.Background(), workerID, protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID: workerID,
			BeadID:   requestedID,
			State:    "running",
		},
	})
	d.tryAssign(context.Background())

	d.mu.Lock()
	w := d.workers[workerID]
	if w == nil {
		d.mu.Unlock()
		t.Fatal("spawn-for worker should remain tracked after reconnect")
	}
	if w.state != protocol.WorkerShuttingDown {
		d.mu.Unlock()
		t.Fatalf("spawn-for worker state after reconnect = %s, want %s", w.state, protocol.WorkerShuttingDown)
	}
	if w.beadID != "" || w.assignmentID != 0 || w.targetBeadID != "" {
		d.mu.Unlock()
		t.Fatalf("stopped spawn-for worker resumed assignment after cleanup race: bead=%q assignment=%d target=%q",
			w.beadID, w.assignmentID, w.targetBeadID)
	}
	if _, ok := d.pendingHandoffs[otherID]; !ok {
		d.mu.Unlock()
		t.Fatalf("stopped spawn-for worker consumed unrelated pending handoff %q", otherID)
	}
	d.mu.Unlock()
}

func TestSpawnFor_StoppedWorkerHeartbeatTimeoutDoesNotEscalateCrash(t *testing.T) {
	d, _, _, esc, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)

	now := time.Date(2026, 5, 1, 19, 30, 0, 0, time.UTC)
	d.nowFunc = func() time.Time { return now }
	workerID := "worker-spawnfor-stopped-timeout"
	conn := newMockConn()

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     conn,
		state:    protocol.WorkerShuttingDown,
		managed:  true,
		spawnFor: true,
		lastSeen: now.Add(-2 * d.cfg.HeartbeatTimeout),
	}
	d.mu.Unlock()

	d.checkHeartbeats(context.Background())

	d.mu.Lock()
	_, stillTracked := d.workers[workerID]
	unexpectedManagedExits := d.unexpectedManagedExits
	d.mu.Unlock()
	if stillTracked {
		t.Fatal("stopped spawn-for worker should be reaped after heartbeat timeout")
	}
	if unexpectedManagedExits != 0 {
		t.Fatalf("stopped spawn-for worker should not count as unexpected managed exit, got %d", unexpectedManagedExits)
	}
	if messages := esc.Messages(); len(messages) != 0 {
		t.Fatalf("stopped spawn-for worker should not escalate worker crash, got %v", messages)
	}

	if got := eventCount(t, d.db, "spawn_for_shutdown_timeout"); got != 1 {
		t.Fatalf("spawn_for_shutdown_timeout event count = %d, want 1", got)
	}
}

func TestSpawnFor_TargetedWorkerGetsRequestedBeadNotFirstReady(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	d.cfg.PollInterval = 10 * time.Second
	d.cfg.FallbackPollInterval = 10 * time.Second

	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{
		{ID: otherID, Priority: 0},
		{ID: requestedID, Priority: 3},
	})

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "spawn-for", requestedID)
	if !ack.OK {
		t.Fatalf("spawn-for directive failed: %s", ack.Detail)
	}
	waitFor(t, func() bool {
		return len(pm.SpawnedIDs()) > 0
	}, 1*time.Second)

	workerID := pm.SpawnedIDs()[0]
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: workerID, ContextPct: 5},
	})

	msg, ok := readMsg(t, conn, 3*time.Second)
	if !ok || msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN within 3s, got ok=%v type=%v", ok, msg.Type)
	}
	if msg.Assign == nil || msg.Assign.BeadID != requestedID {
		t.Fatalf("expected bead %s assigned, got %v", requestedID, msg.Assign)
	}
}

// TestSpawnFor_TargetedAssignment_PendingRequestSuppressesAutoscale verifies that
// while a spawn-for worker is pending (spawned but not yet connected), autoscale
// does not launch general workers for either the reserved target or unrelated work.
//
// Bug: tryAssign allowed autoscale to spawn a general worker during the pending
// spawn-for window. The general worker could consume capacity or receive unrelated
// ready work before the targeted worker connected.
//
// Fix: suppress autoscale while any spawn-for worker is pending.
func TestSpawnFor_TargetedAssignment_PendingRequestSuppressesAutoscale(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.setState(StateRunning)
	d.cfg.MaxWorkers = 3

	requestedID := "oro-spawnfor-requested"
	otherID := "oro-spawnfor-other"
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{
		{ID: requestedID, Priority: 2},
		{ID: otherID, Priority: 0},
	})

	d.mu.Lock()
	d.targetWorkers = 0
	d.mu.Unlock()

	// Spawn the targeted worker; it is now pending (not yet connected).
	if _, err := d.applySpawnFor(requestedID); err != nil {
		t.Fatalf("applySpawnFor failed: %v", err)
	}
	spawnCount := len(pm.SpawnedIDs())
	if spawnCount != 1 {
		t.Fatalf("expected exactly 1 spawn-for worker after directive, got %d", spawnCount)
	}

	// tryAssign sees idle=[], a pending spawn-for reservation, and unrelated ready work.
	// Autoscale must not fire until the targeted worker has connected and received
	// its reserved task.
	d.tryAssign(context.Background())

	if got := len(pm.SpawnedIDs()); got != 1 {
		t.Fatalf(
			"autoscale spawned %d workers while spawn-for pending; want 1 (only the targeted worker)",
			got,
		)
	}
	d.mu.Lock()
	targetWorkers := d.targetWorkers
	d.mu.Unlock()
	if targetWorkers != 0 {
		t.Fatalf("targetWorkers = %d after tryAssign, want 0 (no general work to do)", targetWorkers)
	}
}

func TestSpawnFor_PendingRequestSuppressesAutoscaleAfterQueueSnapshot(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.setState(StateRunning)
	d.cfg.MaxWorkers = 3

	d.mu.Lock()
	d.targetWorkers = 0
	d.pendingWorkerTargets["worker-spawnfor-pending"] = "oro-spawnfor-requested"
	d.mu.Unlock()

	d.maybeAutoScale(context.Background(), 2, 0)

	if got := len(pm.SpawnedIDs()); got != 0 {
		t.Fatalf("autoscale spawned %d workers after spawn-for became pending; want 0", got)
	}
	d.mu.Lock()
	targetWorkers := d.targetWorkers
	d.mu.Unlock()
	if targetWorkers != 0 {
		t.Fatalf("targetWorkers = %d after maybeAutoScale with pending spawn-for, want 0", targetWorkers)
	}
}

func TestSpawnFor_PendingRequestSuppressesScaleUpAtReconcile(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.setState(StateRunning)
	d.cfg.MaxWorkers = 3

	d.mu.Lock()
	d.targetWorkers = 2
	d.pendingWorkerTargets["worker-spawnfor-pending"] = "oro-spawnfor-requested"
	d.mu.Unlock()

	detail := d.reconcileScale()

	if got := len(pm.SpawnedIDs()); got != 0 {
		t.Fatalf("reconcileScale spawned %d workers while spawn-for pending; want 0", got)
	}
	if !strings.Contains(detail, "pending spawn-for active") {
		t.Fatalf("reconcileScale detail = %q, want pending spawn-for skip", detail)
	}
}

func TestAutoscaleInputs_TargetedIdleExcludesPendingReservedTargets(t *testing.T) {
	idle := []idleWorker{{targetBeadID: "oro-targeted"}}
	beads := []protocol.Bead{
		{ID: "oro-targeted"},
		{ID: "oro-pending-spawn-for"},
		{ID: "oro-general"},
	}
	reservedTargets := map[string]bool{"oro-pending-spawn-for": true}

	queueDepth, idleCount := autoscaleInputsForIdleWorkers(idle, beads, reservedTargets)

	if queueDepth != 2 || idleCount != 0 {
		t.Fatalf("queueDepth=%d idleCount=%d, want queueDepth=2 idleCount=0", queueDepth, idleCount)
	}
}

// TestIdleWorker_PicksUpQueuedBeadImmediately verifies that a worker connecting
// while beads are queued receives an ASSIGN without waiting for the poll interval.
//
// This covers the general "scale-up idle worker" case: when a new worker
// connects (managed or unmanaged), it should immediately be assigned a bead
// if one is ready, rather than waiting for the next poll tick.
func TestIdleWorker_PicksUpQueuedBeadImmediately(t *testing.T) {
	d, beads, wt, _, _, _ := newTestDispatcher(t)

	// Slow polls: only workerReadyCh can trigger assignment within the test window.
	d.cfg.PollInterval = 10 * time.Second
	d.cfg.FallbackPollInterval = 10 * time.Second

	beadID := "oro-idle-pickup-test"
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/worktree-" + bID, "agent/" + bID, nil
	}
	beads.SetBeads([]protocol.Bead{{ID: beadID, Priority: 2}})

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	workerID := "w-idle-pickup"
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: workerID, ContextPct: 5},
	})

	// ASSERTION: Worker receives ASSIGN within 3s (without waiting 10s for poll).
	msg, ok := readMsg(t, conn, 3*time.Second)
	if !ok || msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN within 3s, got ok=%v type=%v", ok, msg.Type)
	}
	if msg.Assign == nil || msg.Assign.BeadID != beadID {
		t.Fatalf("expected bead %s assigned, got %v", beadID, msg.Assign)
	}
}
