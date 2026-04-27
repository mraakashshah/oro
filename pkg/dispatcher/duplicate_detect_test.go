package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// injectBusyWorker injects a worker with state WorkerBusy and a given beadID
// directly into d.workers. It uses mockConn (buffered, non-blocking) so that
// writes from detectAndResolveDuplicateActiveAssignments never block.
// Returns the mock so callers can inspect written messages.
func injectBusyWorker(t *testing.T, d *Dispatcher, workerID, beadID string, assignmentID int64) *mockConn {
	t.Helper()
	mc := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         mc,
		state:        protocol.WorkerBusy,
		beadID:       beadID,
		assignmentID: assignmentID,
		lastSeen:     d.nowFunc(),
		lastProgress: d.nowFunc(),
		encoder:      json.NewEncoder(mc),
	}
	d.mu.Unlock()
	return mc
}

// firstWrittenMsg decodes the first message written to a mockConn.
// Returns (msg, true) if any message was written, (zero, false) otherwise.
func firstWrittenMsg(mc *mockConn) (protocol.Message, bool) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	if len(mc.written) == 0 {
		return protocol.Message{}, false
	}
	raw := bytes.TrimRight(mc.written[0], "\n")
	var msg protocol.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return protocol.Message{}, false
	}
	return msg, true
}

// insertActiveAssignment inserts an active assignment record directly into the DB.
// Callers that need two active rows for the same bead must drop the unique index first.
func insertActiveAssignment(t *testing.T, d *Dispatcher, beadID, workerID, worktree string) int64 {
	t.Helper()
	ctx := context.Background()
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`,
		beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("insert active assignment: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestDuplicateWorkerBead_Detected verifies that detectAndResolveDuplicateActiveAssignments
// finds two in-memory WorkerBusy workers sharing the same beadID and reduces
// ownership to exactly one worker, logging a duplicate_worker_bead event.
func TestDuplicateWorkerBead_Detected(t *testing.T) {
	t.Parallel()

	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const beadID = "oro-dup"

	injectBusyWorker(t, d, "w-winner", beadID, 20)
	injectBusyWorker(t, d, "w-loser", beadID, 10)

	d.detectAndResolveDuplicateActiveAssignments(ctx)

	// Exactly one worker should still hold the bead.
	d.mu.Lock()
	var busyCount int
	for _, w := range d.workers {
		if w.beadID == beadID && w.state == protocol.WorkerBusy {
			busyCount++
		}
	}
	d.mu.Unlock()

	if busyCount != 1 {
		t.Errorf("after duplicate resolution: %d workers hold %s, want 1", busyCount, beadID)
	}

	// Duplicate event must be logged.
	if n := eventCount(t, d.db, "duplicate_worker_bead"); n < 1 {
		t.Errorf("expected ≥1 duplicate_worker_bead event, got %d", n)
	}
}

// TestDuplicateWorkerBead_WinnerIsHighestAssignmentID verifies that when two workers
// share a bead the one with the higher assignmentID is kept and the loser's beadID
// is cleared.
func TestDuplicateWorkerBead_WinnerIsHighestAssignmentID(t *testing.T) {
	t.Parallel()

	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const beadID = "oro-dup2"

	injectBusyWorker(t, d, "w-old", beadID, 5)
	injectBusyWorker(t, d, "w-new", beadID, 15)

	d.detectAndResolveDuplicateActiveAssignments(ctx)

	d.mu.Lock()
	oldWorker := d.workers["w-old"]
	newWorker := d.workers["w-new"]
	d.mu.Unlock()

	if newWorker == nil || newWorker.beadID != beadID || newWorker.state != protocol.WorkerBusy {
		t.Errorf("w-new (higher assignmentID) should still hold %s as WorkerBusy", beadID)
	}
	if oldWorker != nil && oldWorker.beadID == beadID && oldWorker.state == protocol.WorkerBusy {
		t.Errorf("w-old (lower assignmentID) should not still hold %s as WorkerBusy", beadID)
	}
}

// TestDuplicateWorkerBead_LoserReceivesShutdown verifies that the losing worker
// receives a SHUTDOWN message after duplicate resolution.
func TestDuplicateWorkerBead_LoserReceivesShutdown(t *testing.T) {
	t.Parallel()

	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const beadID = "oro-dup3"

	// w-winner3 has higher assignmentID and keeps the bead.
	_ = injectBusyWorker(t, d, "w-winner3", beadID, 99)
	// w-loser3 has lower assignmentID and should receive SHUTDOWN.
	loserMock := injectBusyWorker(t, d, "w-loser3", beadID, 1)

	d.detectAndResolveDuplicateActiveAssignments(ctx)

	msg, ok := firstWrittenMsg(loserMock)
	if !ok {
		t.Fatal("loser did not receive any message; expected SHUTDOWN")
	}
	if msg.Type != protocol.MsgShutdown {
		t.Errorf("loser received %s, want SHUTDOWN", msg.Type)
	}
}

// TestDuplicateAssignment_DBRecordCleaned verifies that after duplicate resolution
// the loser's active DB assignment record is marked completed.
func TestDuplicateAssignment_DBRecordCleaned(t *testing.T) {
	t.Parallel()

	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const beadID = "oro-dup4"

	// Drop the unique index so we can insert two active rows for the same bead.
	if _, err := d.db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_assignments_one_active_per_bead`); err != nil {
		t.Fatalf("drop index: %v", err)
	}

	winnerAssID := insertActiveAssignment(t, d, beadID, "w-db-winner", "/tmp/winner")
	loserAssID := insertActiveAssignment(t, d, beadID, "w-db-loser", "/tmp/loser")

	injectBusyWorker(t, d, "w-db-winner", beadID, winnerAssID)
	injectBusyWorker(t, d, "w-db-loser", beadID, loserAssID)

	d.detectAndResolveDuplicateActiveAssignments(ctx)

	var activeCount int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assignments WHERE bead_id=? AND status='active'`, beadID,
	).Scan(&activeCount); err != nil {
		t.Fatalf("query active count: %v", err)
	}
	if activeCount != 1 {
		t.Errorf("expected 1 active assignment after resolution, got %d", activeCount)
	}
}

// TestHandoffReconnect_NoDuplicateActive simulates the handoff-then-reconnect
// race path: worker A hands off bead X, a new worker B is assigned the bead,
// then A reconnects claiming it. At most one WorkerBusy worker should hold
// the bead at any point.
func TestHandoffReconnect_NoDuplicateActive(t *testing.T) {
	t.Parallel()

	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	const beadID = "bead-hro"

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Handoff reconnect test bead",
		AcceptanceCriteria: "Test: auto | Assert: PASS",
	}
	beadSrc.mu.Unlock()

	// Connect worker A and wait for it to register.
	connA, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, connA, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w-A",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Inject worker A as busy on beadID.
	d.mu.Lock()
	wA := d.workers["w-A"]
	wA.beadID = beadID
	wA.state = protocol.WorkerBusy
	wA.assignmentID = 10
	wA.worktree = "/tmp/hro"
	d.mu.Unlock()

	// Worker A sends HANDOFF — clears A's beadID, populates pendingHandoffs.
	sendMsg(t, connA, protocol.Message{
		Type: protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{
			BeadID:         beadID,
			WorkerID:       "w-A",
			ContextSummary: "partial progress",
		},
	})

	// Wait for A's beadID to be cleared.
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		w, ok := d.workers["w-A"]
		return !ok || w.beadID == ""
	}, 1*time.Second)

	// Connect worker B and assign it the bead (simulates assignHandoffToWorker).
	connB, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, connB, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w-B",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 2, 1*time.Second)

	d.mu.Lock()
	wB := d.workers["w-B"]
	wB.beadID = beadID
	wB.state = protocol.WorkerBusy
	wB.assignmentID = 20
	d.mu.Unlock()

	// Worker A reconnects claiming the same bead (stale knowledge).
	sendMsg(t, connA, protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			BeadID: beadID,
			State:  "running",
		},
	})

	// Allow reconnect handling to complete.
	time.Sleep(100 * time.Millisecond)

	// INVARIANT: at most one worker is WorkerBusy on beadID.
	d.mu.Lock()
	var busyCount int
	for _, w := range d.workers {
		if w.beadID == beadID && w.state == protocol.WorkerBusy {
			busyCount++
		}
	}
	d.mu.Unlock()

	if busyCount > 1 {
		t.Errorf("duplicate active assignment: %d workers hold %s as WorkerBusy, want ≤1", busyCount, beadID)
	}
}

// TestReconnectBead_ConflictLogged verifies that when a reconnecting worker claims
// a bead already held by another busy worker, a reconnect_bead_conflict event is
// logged (and the reconnecting worker does NOT steal the bead).
func TestReconnectBead_ConflictLogged(t *testing.T) {
	t.Parallel()

	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	const beadID = "bead-conflict"

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Conflict test bead",
		AcceptanceCriteria: "Test: auto | Assert: PASS",
	}
	beadSrc.mu.Unlock()

	// Establish worker A (already holds the bead).
	connA, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, connA, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w-conflict-A",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	d.mu.Lock()
	d.workers["w-conflict-A"].beadID = beadID
	d.workers["w-conflict-A"].state = protocol.WorkerBusy
	d.workers["w-conflict-A"].assignmentID = 5
	d.mu.Unlock()

	// Worker B connects then reconnects claiming the same bead.
	connB, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, connB, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w-conflict-B",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 2, 1*time.Second)

	sendMsg(t, connB, protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			BeadID: beadID,
			State:  "running",
		},
	})

	// Allow reconnect to be processed.
	waitFor(t, func() bool {
		return eventCount(t, d.db, "reconnect_bead_conflict") > 0
	}, 1*time.Second)

	// A must still hold the bead; B must not.
	d.mu.Lock()
	aHolds := d.workers["w-conflict-A"] != nil &&
		d.workers["w-conflict-A"].beadID == beadID &&
		d.workers["w-conflict-A"].state == protocol.WorkerBusy
	var busyCount int
	for _, w := range d.workers {
		if w.beadID == beadID && w.state == protocol.WorkerBusy {
			busyCount++
		}
	}
	d.mu.Unlock()

	if !aHolds {
		t.Error("w-conflict-A should still hold the bead after B's reconnect conflict")
	}
	if busyCount > 1 {
		t.Errorf("expected ≤1 busy worker on %s, got %d", beadID, busyCount)
	}
}

// Compile-time check: net.Pipe satisfies net.Conn (used in other tests, keep import live).
var _ net.Conn = (net.Conn)(nil)
