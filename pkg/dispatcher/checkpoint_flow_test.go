package dispatcher //nolint:testpackage // white-box: needs access to unexported dispatcher fields

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"

	"github.com/google/uuid"
)

// captureJournalStore wraps fakeBeadStore and records AppendJourney/Journey calls.
// Used in checkpoint tests where we need to inspect the bead's journey.
type captureJournalStore struct {
	*fakeBeadStore
	mu      sync.Mutex
	entries []captureJournalEntry
}

type captureJournalEntry struct {
	beadID string
	evt    beadstore.JourneyEvent
}

func newCaptureJournalStore() *captureJournalStore {
	return &captureJournalStore{
		fakeBeadStore: &fakeBeadStore{
			beads: []protocol.Bead{},
			shown: make(map[string]*protocol.BeadDetail),
		},
	}
}

func (s *captureJournalStore) AppendJourney(_ context.Context, beadID string, evt beadstore.JourneyEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	evt.BeadID = beadID
	s.entries = append(s.entries, captureJournalEntry{beadID: beadID, evt: evt})
	return nil
}

func (s *captureJournalStore) Journey(_ context.Context, beadID string, _ time.Time) ([]beadstore.JourneyEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []beadstore.JourneyEvent
	for _, e := range s.entries {
		if e.beadID == beadID {
			out = append(out, e.evt)
		}
	}
	return out, nil
}

func (s *captureJournalStore) LatestJourney(_ context.Context, beadID string, limit int) ([]beadstore.JourneyEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []beadstore.JourneyEvent
	for _, e := range s.entries {
		if e.beadID == beadID {
			out = append(out, e.evt)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (s *captureJournalStore) capturedFor(beadID string) []beadstore.JourneyEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []beadstore.JourneyEvent
	for _, e := range s.entries {
		if e.beadID == beadID {
			out = append(out, e.evt)
		}
	}
	return out
}

// makeCheckpointDispatcher builds a Dispatcher backed by a captureJournalStore
// for checkpoint flow tests.  It reuses the existing newTestDispatcher helper and
// then swaps d.beads for our tracking store (same-package access).
func makeCheckpointDispatcher(t *testing.T) (*Dispatcher, *captureJournalStore) {
	t.Helper()
	d, _, _, _, _, _ := newTestDispatcher(t)
	store := newCaptureJournalStore()
	d.beads = store
	d.cfg.CheckpointThreshold = 75
	return d, store
}

// TestCheckpointFlow verifies §9.3 checkpoint correlation-ID contract:
//   - every checkpoint event carries the same checkpoint_id
//   - stale acks are recorded as note events (no state mutation)
//   - late acks proceed with forced=true
//   - a respawned dispatcher discovers an in-flight checkpoint via the journey
func TestCheckpointFlow(t *testing.T) {
	ctx := context.Background()

	// --- sub-test 1: all events carry the same checkpoint_id ---
	t.Run("all_events_carry_checkpoint_id", func(t *testing.T) {
		d, _ := makeCheckpointDispatcher(t)
		const beadID, workerID = "bead-cp1", "w-cp1"

		d.triggerCheckpoint(ctx, beadID, workerID, 80)

		// Read checkpoint_requested payload from SQLite events table.
		var reqPayload string
		if err := d.db.QueryRowContext(ctx,
			`SELECT payload FROM events WHERE type='checkpoint_requested' AND bead_id=? LIMIT 1`,
			beadID,
		).Scan(&reqPayload); err != nil {
			t.Fatalf("checkpoint_requested event not found: %v", err)
		}
		var req struct {
			CheckpointID string `json:"checkpoint_id"`
		}
		if err := json.Unmarshal([]byte(reqPayload), &req); err != nil {
			t.Fatalf("parse checkpoint_requested payload: %v", err)
		}
		if req.CheckpointID == "" {
			t.Fatal("checkpoint_requested event missing checkpoint_id")
		}
		parsedID, err := uuid.Parse(req.CheckpointID)
		if err != nil {
			t.Fatalf("checkpoint_requested checkpoint_id = %q, want RFC 9562 UUID: %v", req.CheckpointID, err)
		}
		if got := parsedID.Version(); got != uuid.Version(7) {
			t.Fatalf("checkpoint_requested checkpoint_id version = %d, want 7", got)
		}
		cpID := req.CheckpointID

		// Send a valid ack — should produce checkpoint_acked + checkpointed events.
		d.handleCheckpointAck(ctx, workerID, protocol.Message{
			Type: protocol.MsgCheckpointAck,
			CheckpointAck: &protocol.CheckpointAckPayload{
				BeadID:        beadID,
				CheckpointID:  cpID,
				IntentSummary: "continue work",
			},
		})

		// checkpoint_acked must carry the same checkpoint_id.
		var ackedPayload string
		if err := d.db.QueryRowContext(ctx,
			`SELECT payload FROM events WHERE type='checkpoint_acked' AND bead_id=? LIMIT 1`,
			beadID,
		).Scan(&ackedPayload); err != nil {
			t.Fatalf("checkpoint_acked event not found: %v", err)
		}
		var acked struct {
			CheckpointID string `json:"checkpoint_id"`
		}
		if err := json.Unmarshal([]byte(ackedPayload), &acked); err != nil {
			t.Fatalf("parse checkpoint_acked payload: %v", err)
		}
		if acked.CheckpointID != cpID {
			t.Fatalf("checkpoint_acked checkpoint_id: got %q, want %q", acked.CheckpointID, cpID)
		}

		// checkpointed must also carry the same checkpoint_id.
		var donePayload string
		if err := d.db.QueryRowContext(ctx,
			`SELECT payload FROM events WHERE type='checkpointed' AND bead_id=? LIMIT 1`,
			beadID,
		).Scan(&donePayload); err != nil {
			t.Fatalf("checkpointed event not found: %v", err)
		}
		var done struct {
			CheckpointID string `json:"checkpoint_id"`
		}
		if err := json.Unmarshal([]byte(donePayload), &done); err != nil {
			t.Fatalf("parse checkpointed payload: %v", err)
		}
		if done.CheckpointID != cpID {
			t.Fatalf("checkpointed checkpoint_id: got %q, want %q", done.CheckpointID, cpID)
		}
	})

	// --- sub-test 2: stale ack → note event, no state mutation ---
	t.Run("stale_ack_recorded_as_note_no_state_mutation", func(t *testing.T) {
		d, store := makeCheckpointDispatcher(t)
		const beadID, workerID = "bead-stale", "w-stale"

		d.triggerCheckpoint(ctx, beadID, workerID, 80)

		cs := d.checkpoints.get(beadID)
		if cs == nil {
			t.Fatal("no active checkpoint after triggerCheckpoint")
		}
		currentID := cs.checkpointID

		// Send a stale ack (wrong checkpoint_id).
		staleID := fmt.Sprintf("%s-old", currentID)
		d.handleCheckpointAck(ctx, workerID, protocol.Message{
			Type: protocol.MsgCheckpointAck,
			CheckpointAck: &protocol.CheckpointAckPayload{
				BeadID:       beadID,
				CheckpointID: staleID,
			},
		})

		// Verify note event in SQLite events table.
		var notePayload string
		if err := d.db.QueryRowContext(ctx,
			`SELECT payload FROM events WHERE type='note' AND bead_id=? LIMIT 1`,
			beadID,
		).Scan(&notePayload); err != nil {
			t.Fatalf("note event not found after stale ack: %v", err)
		}
		var note struct {
			Kind       string `json:"kind"`
			OriginalID string `json:"original_id"`
		}
		if err := json.Unmarshal([]byte(notePayload), &note); err != nil {
			t.Fatalf("parse note payload: %v", err)
		}
		if note.Kind != "stale_checkpoint_ack" {
			t.Fatalf("note kind: got %q, want stale_checkpoint_ack", note.Kind)
		}
		if note.OriginalID != staleID {
			t.Fatalf("note original_id: got %q, want %q", note.OriginalID, staleID)
		}

		// Journey must contain a note event but NOT a checkpointed event.
		journalEvents := store.capturedFor(beadID)
		var hasNote bool
		for _, e := range journalEvents {
			if e.Event == "note" {
				hasNote = true
			}
			if e.Event == "checkpointed" {
				t.Fatal("stale ack must not produce a checkpointed journey event (no state mutation)")
			}
		}
		if !hasNote {
			t.Fatal("stale ack must produce a note journey event")
		}

		// Active checkpoint must still be set (state not mutated).
		if d.checkpoints.get(beadID) == nil {
			t.Fatal("active checkpoint was cleared by stale ack (state mutated)")
		}
		if d.checkpoints.get(beadID).checkpointID != currentID {
			t.Fatalf("checkpoint_id changed by stale ack: got %q, want %q",
				d.checkpoints.get(beadID).checkpointID, currentID)
		}

		// No checkpointed event in the events table.
		var count int
		_ = d.db.QueryRowContext(ctx,
			`SELECT count(*) FROM events WHERE type='checkpointed' AND bead_id=?`, beadID,
		).Scan(&count)
		if count > 0 {
			t.Fatalf("stale ack must not produce checkpointed SQL event: got %d", count)
		}
	})

	// --- sub-test 3: late ack proceeds with forced=true ---
	t.Run("late_ack_proceeds_with_forced_true", func(t *testing.T) {
		d, _ := makeCheckpointDispatcher(t)
		const beadID, workerID = "bead-late", "w-late"

		d.triggerCheckpoint(ctx, beadID, workerID, 80)

		cs := d.checkpoints.get(beadID)
		if cs == nil {
			t.Fatal("no active checkpoint after triggerCheckpoint")
		}
		cpID := cs.checkpointID

		// Advance nowFunc past the deadline.
		past := cs.deadline.Add(time.Second)
		d.nowFunc = func() time.Time { return past }

		// Send valid ack after the deadline.
		d.handleCheckpointAck(ctx, workerID, protocol.Message{
			Type: protocol.MsgCheckpointAck,
			CheckpointAck: &protocol.CheckpointAckPayload{
				BeadID:        beadID,
				CheckpointID:  cpID,
				IntentSummary: "late but valid",
			},
		})

		// checkpointed event must have forced=true.
		var donePayload string
		if err := d.db.QueryRowContext(ctx,
			`SELECT payload FROM events WHERE type='checkpointed' AND bead_id=? LIMIT 1`,
			beadID,
		).Scan(&donePayload); err != nil {
			t.Fatalf("checkpointed event not found: %v", err)
		}
		var done struct {
			CheckpointID string `json:"checkpoint_id"`
			Forced       bool   `json:"forced"`
		}
		if err := json.Unmarshal([]byte(donePayload), &done); err != nil {
			t.Fatalf("parse checkpointed payload: %v", err)
		}
		if !done.Forced {
			t.Fatal("late ack: checkpointed event must have forced=true")
		}
		if done.CheckpointID != cpID {
			t.Fatalf("checkpointed checkpoint_id: got %q, want %q", done.CheckpointID, cpID)
		}
	})

	// --- sub-test 4: respawn discovers in-flight checkpoint via journey ---
	t.Run("respawn_discovers_inflight_checkpoint_via_journey", func(t *testing.T) {
		const cpID = "cp-respawn-12345"
		const wid = "w-respawn"

		// Seed: checkpoint_requested exists, no checkpointed.
		events := []beadstore.JourneyEvent{
			{
				Actor:   "dispatcher",
				Event:   "checkpoint_requested",
				Payload: fmt.Sprintf(`{"checkpoint_id":%q,"worker_id":%q,"trigger":"context_threshold"}`, cpID, wid),
			},
		}

		got := findInflightCheckpoint(events)
		if got == nil {
			t.Fatal("findInflightCheckpoint: got nil, want in-flight state")
		}
		if got.checkpointID != cpID {
			t.Fatalf("findInflightCheckpoint cpID: got %q, want %q", got.checkpointID, cpID)
		}
		if got.workerID != wid {
			t.Fatalf("findInflightCheckpoint workerID: got %q, want %q", got.workerID, wid)
		}
	})

	// --- sub-test 5: no in-flight when checkpointed already exists ---
	t.Run("no_inflight_when_checkpoint_completed", func(t *testing.T) {
		const cpID = "cp-completed-9999"

		events := []beadstore.JourneyEvent{
			{
				Actor:   "dispatcher",
				Event:   "checkpoint_requested",
				Payload: fmt.Sprintf(`{"checkpoint_id":%q}`, cpID),
			},
			{
				Actor:   "dispatcher",
				Event:   "checkpointed",
				Payload: fmt.Sprintf(`{"checkpoint_id":%q,"forced":false}`, cpID),
			},
		}

		got := findInflightCheckpoint(events)
		if got != nil {
			t.Fatalf("findInflightCheckpoint: expected nil (completed), got %+v", got)
		}
	})

	// --- sub-test 6: heartbeat at threshold triggers checkpoint via real path ---
	t.Run("heartbeat_at_threshold_triggers_checkpoint", func(t *testing.T) {
		d, _ := makeCheckpointDispatcher(t)
		const beadID, workerID = "bead-hb", "w-hb"

		// Register a busy worker assigned to the bead so handleHeartbeat's
		// touchProgress lookups don't trip on a missing worker.
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			state:        protocol.WorkerBusy,
			beadID:       beadID,
			lastSeen:     d.nowFunc(),
			lastProgress: d.nowFunc(),
		}
		d.mu.Unlock()

		// Heartbeat with ContextPct=80 (>= threshold 75) must trigger a
		// checkpoint_requested event via the production handleHeartbeat path.
		d.handleHeartbeat(ctx, workerID, protocol.Message{
			Type: protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{
				WorkerID:   workerID,
				BeadID:     beadID,
				ContextPct: 80,
			},
		})

		var count int
		if err := d.db.QueryRowContext(ctx,
			`SELECT count(*) FROM events WHERE type='checkpoint_requested' AND bead_id=?`,
			beadID,
		).Scan(&count); err != nil {
			t.Fatalf("query checkpoint_requested: %v", err)
		}
		if count != 1 {
			t.Fatalf("heartbeat at threshold: expected 1 checkpoint_requested event, got %d", count)
		}

		// Active checkpoint must be registered for the bead.
		if cs := d.checkpoints.get(beadID); cs == nil || cs.workerID != workerID {
			t.Fatalf("checkpoint tracker not populated by heartbeat: cs=%+v", cs)
		}
	})

	// --- sub-test 7: restore-path discovers in-flight checkpoint and populates tracker ---
	t.Run("restoreInflightCheckpoints_populates_tracker", func(t *testing.T) {
		d, store := makeCheckpointDispatcher(t)
		const beadID, workerID = "bead-restore", "w-restore"
		const cpID = "cp-restored-1"

		// Seed the journey with a checkpoint_requested but no completion event.
		_ = store.AppendJourney(ctx, beadID, beadstore.JourneyEvent{
			Actor:   "dispatcher",
			Event:   "checkpoint_requested",
			Payload: fmt.Sprintf(`{"checkpoint_id":%q,"worker_id":%q,"trigger":"context_threshold"}`, cpID, workerID),
		})

		// Tracker is empty before restore.
		if d.checkpoints.get(beadID) != nil {
			t.Fatal("tracker should be empty before restoreInflightCheckpoints")
		}

		d.restoreInflightCheckpoints(ctx, []restoredAssignment{{beadID: beadID}})

		cs := d.checkpoints.get(beadID)
		if cs == nil {
			t.Fatal("restoreInflightCheckpoints did not populate tracker")
		}
		if cs.checkpointID != cpID {
			t.Fatalf("restored cpID: got %q, want %q", cs.checkpointID, cpID)
		}
		if cs.workerID != workerID {
			t.Fatalf("restored workerID: got %q, want %q", cs.workerID, workerID)
		}

		// A checkpoint_recovered event should be logged.
		var count int
		if err := d.db.QueryRowContext(ctx,
			`SELECT count(*) FROM events WHERE type='checkpoint_recovered' AND bead_id=?`,
			beadID,
		).Scan(&count); err != nil {
			t.Fatalf("query checkpoint_recovered: %v", err)
		}
		if count != 1 {
			t.Fatalf("restoreInflightCheckpoints: expected 1 checkpoint_recovered event, got %d", count)
		}
	})
}
