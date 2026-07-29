package dispatcher //nolint:testpackage // white-box: needs access to unexported dispatcher fields

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestCheckpointE2EFromHighContext is the §18.3 verify-checkpoint E2E test.
//
// It spawns a managed test worker that publishes context_pct=0.80 on its first
// active heartbeat, then asserts the full checkpoint control loop:
//   - dispatcher emits a checkpoint directive within 2 worker turns of crossing
//     checkpoint_threshold (0.75)
//   - bead journey contains WORKER_ASSIGNED → CHECKPOINT_REQUESTED →
//     CHECKPOINT_RECEIVED → BEAD_CLOSED (in order, no gaps)
//   - bead reaches status=closed
//   - no work-state loss across the checkpoint (worktree preserved in both ASSIGNs)
func TestCheckpointE2EFromHighContext(t *testing.T) {
	const (
		beadID       = "e2e-cp-bead"
		worktreePath = "/tmp/wt-e2e-cp"
		worker1ID    = "w-e2e-cp-1"
	)

	d, store := makeCheckpointDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	store.SetBeads([]protocol.Bead{{ID: beadID, Priority: 1}})
	wt := d.worktrees.(*mockWorktreeManager)
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return worktreePath, "agent/" + bID, nil
	}

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Worker 1 connects and receives ASSIGN.
	conn1, _ := connectWorker(t, d.cfg.SocketPath)
	t.Cleanup(func() { _ = conn1.Close() })
	sendMsg(t, conn1, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: worker1ID, ContextPct: 5},
	})

	assign1, ok := readMsg(t, conn1, 3*time.Second)
	if !ok || assign1.Type != protocol.MsgAssign {
		t.Fatalf("§18.3 WORKER_ASSIGNED: expected ASSIGN for worker1, got ok=%v type=%v", ok, assign1.Type)
	}
	if assign1.Assign == nil || assign1.Assign.BeadID != beadID {
		t.Fatalf("§18.3 WORKER_ASSIGNED: expected bead %s assigned, got %v", beadID, assign1.Assign)
	}
	preCheckpointWorktree := assign1.Assign.Worktree

	// Worker 1 sends a high-context heartbeat (80% > threshold 75%) → triggers checkpoint.
	// "Within 2 worker turns" is satisfied immediately (turn 1 = this heartbeat).
	sendMsg(t, conn1, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   worker1ID,
			BeadID:     beadID,
			ContextPct: 80,
		},
	})

	// Wait for checkpoint_requested event and extract checkpoint_id.
	var checkpointID string
	waitFor(t, func() bool {
		var payload string
		if err := d.db.QueryRowContext(context.Background(),
			`SELECT payload FROM events WHERE type='checkpoint_requested' AND bead_id=? LIMIT 1`,
			beadID,
		).Scan(&payload); err != nil {
			return false
		}
		var req struct {
			CheckpointID string `json:"checkpoint_id"`
		}
		if err := json.Unmarshal([]byte(payload), &req); err != nil {
			return false
		}
		checkpointID = req.CheckpointID
		return checkpointID != ""
	}, 2*time.Second)
	if checkpointID == "" {
		t.Fatal("§18.3 CHECKPOINT_REQUESTED: checkpoint_requested event not found in events table")
	}

	// Worker 1 sends CHECKPOINT_ACK → triggers respawn (kill old, spawn new).
	sendMsg(t, conn1, protocol.Message{
		Type: protocol.MsgCheckpointAck,
		CheckpointAck: &protocol.CheckpointAckPayload{
			BeadID:        beadID,
			CheckpointID:  checkpointID,
			IntentSummary: "continue implementation after checkpoint",
		},
	})

	// Wait for respawn: old worker killed and new worker spawned.
	waitFor(t, func() bool {
		return len(pm.KilledIDs()) > 0 && len(pm.SpawnedIDs()) > 0
	}, 2*time.Second)

	// Assert no work-state loss: worktree must survive respawn.
	d.mu.Lock()
	actualWorktree := d.worktreeByBead[beadID]
	d.mu.Unlock()
	if actualWorktree != worktreePath {
		t.Fatalf("§18.3 work-state loss: worktreeByBead[%q]=%q, want %q",
			beadID, actualWorktree, worktreePath)
	}

	// Close worker 1 to simulate the subprocess being killed.
	// connCloseCleanup runs and must NOT wipe the pending handoff (worker already detached).
	_ = conn1.Close()
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, exists := d.workers[worker1ID]
		return !exists
	}, 2*time.Second)

	// Worker 2 (respawn) connects and receives the handoff ASSIGN immediately.
	worker2ID := pm.SpawnedIDs()[0]
	conn2, _ := connectWorker(t, d.cfg.SocketPath)
	t.Cleanup(func() { _ = conn2.Close() })
	sendMsg(t, conn2, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: worker2ID, ContextPct: 5},
	})

	assign2, ok := readMsg(t, conn2, 3*time.Second)
	if !ok || assign2.Type != protocol.MsgAssign {
		t.Fatalf("§18.3 handoff ASSIGN: expected ASSIGN for worker2, got ok=%v type=%v", ok, assign2.Type)
	}
	if assign2.Assign == nil || assign2.Assign.BeadID != beadID {
		t.Fatalf("§18.3 handoff ASSIGN: expected bead %s, got %v", beadID, assign2.Assign)
	}
	// No work-state loss: worker 2 must use the same worktree as worker 1.
	if assign2.Assign.Worktree != preCheckpointWorktree {
		t.Fatalf("§18.3 work-state loss: assign2.Worktree=%q, want %q (pre-checkpoint worktree)",
			assign2.Assign.Worktree, preCheckpointWorktree)
	}

	// Worker 2 completes the bead.
	sendMsg(t, conn2, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            beadID,
			WorkerID:          worker2ID,
			QualityGatePassed: true,
		},
	})

	// Wait for BEAD_CLOSED: beadstore.Close must be called for beadID.
	waitFor(t, func() bool {
		store.fakeBeadStore.mu.Lock() //nolint:staticcheck // must lock fakeBeadStore.mu, not captureJournalStore.mu
		defer store.fakeBeadStore.mu.Unlock()
		for _, id := range store.closed {
			if id == beadID {
				return true
			}
		}
		return false
	}, 5*time.Second)

	// CloseBead precedes the merged event in finalizeSuccessfulMerge, so wait
	// for that event before snapshotting the ordered journey.
	waitFor(t, func() bool {
		var count int
		err := d.db.QueryRowContext(context.Background(),
			`SELECT count(*) FROM events WHERE type='merged' AND bead_id=?`,
			beadID,
		).Scan(&count)
		return err == nil && count > 0
	}, 5*time.Second)

	// Assert event chain in SQLite events table (ordered):
	// WORKER_ASSIGNED → CHECKPOINT_REQUESTED → CHECKPOINT_RECEIVED → BEAD_CLOSED
	ctx := context.Background()
	rows, err := d.db.QueryContext(ctx,
		`SELECT type FROM events WHERE bead_id=? ORDER BY id ASC`, beadID)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	var evTypes []string
	for rows.Next() {
		var ev string
		if err := rows.Scan(&ev); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		evTypes = append(evTypes, ev)
	}
	_ = rows.Close()

	chain := []string{"assign", "checkpoint_requested", "checkpoint_acked", "merged"}
	if err := assertEventChainE2E(evTypes, chain); err != nil {
		t.Fatalf("§18.3 event chain: %v\nall events: %v", err, evTypes)
	}

	// Assert no journey gaps: all required event types present.
	required := map[string]bool{
		"assign":               false,
		"checkpoint_requested": false,
		"checkpoint_acked":     false,
		"merged":               false,
	}
	for _, ev := range evTypes {
		if _, ok := required[ev]; ok {
			required[ev] = true
		}
	}
	for ev, found := range required {
		if !found {
			t.Errorf("§18.3 journey gap: event %q missing from events table", ev)
		}
	}

	// Assert beadstore journal records the checkpoint lifecycle events.
	journalEvts := store.capturedFor(beadID)
	var journalTypes []string
	for _, e := range journalEvts {
		journalTypes = append(journalTypes, e.Event)
	}
	journalChain := []string{"checkpoint_requested", "checkpoint_acked", "checkpointed"}
	if err := assertEventChainE2E(journalTypes, journalChain); err != nil {
		t.Fatalf("§18.3 journal chain: %v\njournal events: %v", err, journalTypes)
	}
}

// assertEventChainE2E verifies that every element of chain appears in actual in order.
// Elements of actual not in chain are skipped (subsequence check, not equality).
func assertEventChainE2E(actual, chain []string) error {
	pos := 0
	for _, ev := range actual {
		if pos < len(chain) && ev == chain[pos] {
			pos++
		}
	}
	if pos < len(chain) {
		return fmt.Errorf("event %q missing or out of order in %v", chain[pos], actual)
	}
	return nil
}
