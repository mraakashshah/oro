package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestEpicWithChildrenNotAssigned verifies that when the ready list contains
// an already-decomposed epic and a valid task, the dispatcher skips the epic and
// assigns the task.
func TestEpicWithChildrenNotAssigned(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	d.cfg.HeartbeatTimeout = 10 * time.Second
	d.shutdownRunner = &mockCommandRunner{err: errors.New("branch missing")}
	startDispatcher(t, d)

	// Connect a worker.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w1",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.mu.Lock()
	beadSrc.hasChildrenMap = map[string]bool{"epic-1": true}
	beadSrc.allChildrenClosedMap = map[string]bool{"epic-1": false}
	beadSrc.mu.Unlock()

	// Ready list: one already-decomposed epic and one valid task. The epic must
	// be skipped because it has open children.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "epic-1", Title: "Big epic", Priority: 0, Type: "epic"},
		{ID: "task-1", Title: "Implement thing", Priority: 1, Type: "task"},
	})
	all, _ := beadSrc.Ready(context.Background())
	filtered := d.filterAssignable(context.Background(), all)
	if len(filtered) != 1 || filtered[0].ID != "task-1" {
		t.Fatalf("filtered beads = %+v, want only task-1", filtered)
	}
	d.mu.Lock()
	worker := d.workers["w1"]
	d.mu.Unlock()
	if worker == nil {
		t.Fatal("worker w1 not registered")
	}
	if err := d.assignBead(context.Background(), worker, filtered[0]); err != nil {
		t.Fatalf("assignBead: %v", err)
	}

	// The ASSIGN message must be for the task, not the epic.
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("timed out waiting for ASSIGN; expected task-1 to be assigned")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected MsgAssign, got %s", msg.Type)
	}
	if msg.Assign.BeadID != "task-1" {
		t.Fatalf("expected task-1 assigned, got %s — decomposed epic must not be assigned to workers", msg.Assign.BeadID)
	}
}

// TestReadyQueueSkipsNonExecutableOperationalBeads verifies that operational
// ready beads with command-only acceptance criteria do not consume workers or
// block the next executable bug/task bead.
func TestReadyQueueSkipsNonExecutableOperationalBeads(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	d.cfg.HeartbeatTimeout = 10 * time.Second
	d.shutdownRunner = &mockCommandRunner{err: errors.New("branch missing")}
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w1",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	const (
		operationalID = "oro-operational"
		executableID  = "oro-executable"
	)
	beadSrc.mu.Lock()
	beadSrc.hasChildrenMap = map[string]bool{"oro-epic": true}
	beadSrc.allChildrenClosedMap = map[string]bool{"oro-epic": false}
	beadSrc.mu.Unlock()
	beadSrc.shown[operationalID] = &protocol.BeadDetail{
		ID:                 operationalID,
		Title:              "Restart dispatcher",
		AcceptanceCriteria: "Cmd: oro restart\nAssert: dispatcher PID changes",
	}
	beadSrc.shown[executableID] = &protocol.BeadDetail{
		ID:                 executableID,
		Title:              "Fix executable bug",
		AcceptanceCriteria: "Test: pkg/dispatcher/epic_filter_test.go:TestExecutable | Cmd: go test ./pkg/dispatcher/... | Assert: PASS",
	}
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "oro-epic", Title: "Planning epic", Priority: 0, Type: "epic"},
		{ID: operationalID, Title: "Restart dispatcher", Priority: 0, Type: "task"},
		{ID: executableID, Title: "Fix executable bug", Priority: 1, Type: "bug"},
	})
	all, _ := beadSrc.Ready(context.Background())
	filtered := d.filterAssignable(context.Background(), all)
	if len(filtered) != 2 || filtered[0].ID != operationalID || filtered[1].ID != executableID {
		t.Fatalf("filtered beads = %+v, want operational then executable", filtered)
	}
	d.mu.Lock()
	worker := d.workers["w1"]
	d.mu.Unlock()
	if worker == nil {
		t.Fatal("worker w1 not registered")
	}
	if err := d.assignBead(context.Background(), worker, filtered[0]); err != nil {
		t.Fatalf("assign operational: %v", err)
	}
	if err := d.assignBead(context.Background(), worker, filtered[1]); err != nil {
		t.Fatalf("assign executable: %v", err)
	}

	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("timed out waiting for ASSIGN; expected executable bead to be assigned")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected MsgAssign, got %s", msg.Type)
	}
	if msg.Assign.BeadID != executableID {
		t.Fatalf("expected %s assigned, got %s", executableID, msg.Assign.BeadID)
	}

	var operationalSkips int
	if err := d.db.QueryRow(
		`SELECT COUNT(*) FROM events WHERE type = ? AND bead_id = ?`,
		"bead_skipped_non_tdd_acceptance", operationalID,
	).Scan(&operationalSkips); err != nil {
		t.Fatalf("query operational skip event: %v", err)
	}
	if operationalSkips == 0 {
		t.Fatalf("expected bead_skipped_non_tdd_acceptance event for %s", operationalID)
	}
}

// TestQueueSkipNonTDDDoesNotBypassPriority proves that when checkBeadReady
// rejects a higher-priority bead for non_tdd_acceptance, an escalation is
// raised so the silent-skip can't masquerade as priority being respected.
// Covers oro-5833: P0 beads with Cmd+Assert (no Test:) were being silently
// dropped while a P2 stale resume was offered.
func TestQueueSkipNonTDDDoesNotBypassPriority(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const opID = "oro-p0-operational"
	beadSrc.shown[opID] = &protocol.BeadDetail{
		ID:                 opID,
		Title:              "Delete current.md",
		AcceptanceCriteria: "Cmd: ls current.md 2>&1 | grep -c 'No such'\nAssert: returns 1",
	}
	bead := protocol.Bead{ID: opID, Title: "Delete current.md", Priority: 0, Type: "task"}

	_, _, ok := d.checkBeadReady(ctx, bead, "w1")
	if ok {
		t.Fatalf("checkBeadReady = true; expected non-TDD AC bead to be deferred")
	}

	msgs := esc.Messages()
	found := false
	for _, m := range msgs {
		if strings.Contains(m, opID) && (strings.Contains(m, "NON_TDD_AC") || strings.Contains(m, "non-TDD")) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected escalation surfacing the non-TDD AC skip for %s, got: %#v", opID, msgs)
	}
}

// TestAssignableQueueCountsChildlessEpics verifies that queue depth reports
// ready epic decomposition work instead of blanket-filtering epic types.
func TestAssignableQueueCountsChildlessEpics(t *testing.T) {
	t.Run("only childless epics ready gives depth 2", func(t *testing.T) {
		beads := []protocol.Bead{
			{ID: "epic-1", Type: "epic", Title: "Big feature"},
			{ID: "epic-2", Type: "Epic", Title: "Another epic"}, // case variation
		}
		workers := map[string]*trackedWorker{}
		depth := calculateLiveQueueDepth(beads, workers)
		if depth != 2 {
			t.Errorf("expected assignable queue depth 2 with only childless epics ready, got %d", depth)
		}
	})

	t.Run("mixed ready list counts epics and non-epics", func(t *testing.T) {
		beads := []protocol.Bead{
			{ID: "epic-1", Type: "epic"},
			{ID: "task-1", Type: "task"},
			{ID: "bug-1", Type: "bug"},
		}
		workers := map[string]*trackedWorker{}
		depth := calculateLiveQueueDepth(beads, workers)
		if depth != 3 {
			t.Errorf("expected assignable queue depth 3 (epic + task + bug), got %d", depth)
		}
	})

	t.Run("assigned non-epic not counted but unassigned epic is counted", func(t *testing.T) {
		beads := []protocol.Bead{
			{ID: "epic-1", Type: "epic"},
			{ID: "task-1", Type: "task"},
		}
		workers := map[string]*trackedWorker{
			"w1": {beadID: "task-1"},
		}
		depth := calculateLiveQueueDepth(beads, workers)
		if depth != 1 {
			t.Errorf("expected depth 1 (task assigned, epic ready), got %d", depth)
		}
	})
}

func TestStatusQueueBeadsSkipsDecomposedEpics(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	beadSrc.mu.Lock()
	beadSrc.hasChildrenMap = map[string]bool{
		"epic-with-children": true,
		"epic-childless":     false,
	}
	beadSrc.mu.Unlock()

	got := d.statusQueueBeads(ctx, []protocol.Bead{
		{ID: "epic-with-children", Type: "Epic"},
		{ID: "epic-childless", Type: "epic"},
		{ID: "task-ready", Type: "task"},
	})
	gotIDs := make([]string, len(got))
	for i, bead := range got {
		gotIDs[i] = bead.ID
	}
	want := []string{"epic-childless", "task-ready"}
	if len(gotIDs) != len(want) {
		t.Fatalf("status queue beads = %v, want %v", gotIDs, want)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("status queue beads = %v, want %v", gotIDs, want)
		}
	}
}
