package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestEpicNotAssigned verifies that when the ready list contains an epic and a
// valid task, the dispatcher skips the epic (logging non_executable_issue_type)
// and assigns the task. This covers the type-level filter regardless of whether
// the epic has children.
func TestEpicNotAssigned(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
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

	// Ready list: one epic (no children set — mock default returns hasChildren=false),
	// one valid task. With type-level filtering, the epic must be skipped regardless
	// of its children state.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "epic-1", Title: "Big epic", Priority: 0, Type: "epic"},
		{ID: "task-1", Title: "Implement thing", Priority: 1, Type: "task"},
	})

	// The ASSIGN message must be for the task, not the epic.
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("timed out waiting for ASSIGN; expected task-1 to be assigned")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected MsgAssign, got %s", msg.Type)
	}
	if msg.Assign.BeadID != "task-1" {
		t.Fatalf("expected task-1 assigned, got %s — epic must not be assigned to workers", msg.Assign.BeadID)
	}

	// Wait for the non_executable_issue_type event to be logged for the epic.
	waitFor(t, func() bool {
		var count int
		row := d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type = ? AND bead_id = ?`,
			"non_executable_issue_type", "epic-1")
		_ = row.Scan(&count)
		return count > 0
	}, 2*time.Second)
}

// TestAssignableQueueFiltersEpics verifies that calculateLiveQueueDepth excludes
// epics from the assignable queue depth. If only epics are ready, depth must be 0.
func TestAssignableQueueFiltersEpics(t *testing.T) {
	t.Run("only epics ready gives depth 0", func(t *testing.T) {
		beads := []protocol.Bead{
			{ID: "epic-1", Type: "epic", Title: "Big feature"},
			{ID: "epic-2", Type: "Epic", Title: "Another epic"}, // case variation
		}
		workers := map[string]*trackedWorker{}
		depth := calculateLiveQueueDepth(beads, workers)
		if depth != 0 {
			t.Errorf("expected assignable queue depth 0 with only epics ready, got %d", depth)
		}
	})

	t.Run("mixed ready list counts only non-epics", func(t *testing.T) {
		beads := []protocol.Bead{
			{ID: "epic-1", Type: "epic"},
			{ID: "task-1", Type: "task"},
			{ID: "bug-1", Type: "bug"},
		}
		workers := map[string]*trackedWorker{}
		depth := calculateLiveQueueDepth(beads, workers)
		if depth != 2 {
			t.Errorf("expected assignable queue depth 2 (task + bug), got %d", depth)
		}
	})

	t.Run("assigned non-epic not counted", func(t *testing.T) {
		beads := []protocol.Bead{
			{ID: "epic-1", Type: "epic"},
			{ID: "task-1", Type: "task"},
		}
		workers := map[string]*trackedWorker{
			"w1": {beadID: "task-1"},
		}
		depth := calculateLiveQueueDepth(beads, workers)
		if depth != 0 {
			t.Errorf("expected depth 0 (task assigned, epic filtered), got %d", depth)
		}
	})
}
