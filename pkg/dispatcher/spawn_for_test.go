package dispatcher //nolint:testpackage

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"oro/pkg/protocol"
)

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

	d.tryAssign(context.Background())

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
