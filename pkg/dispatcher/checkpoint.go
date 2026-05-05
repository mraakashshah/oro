package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

// checkpointState tracks an in-flight checkpoint for a single bead (§9.3).
type checkpointState struct {
	checkpointID string
	workerID     string
	deadline     time.Time
}

// checkpointTracker is a concurrency-safe map from beadID → active checkpointState.
type checkpointTracker struct {
	mu     sync.Mutex
	active map[string]*checkpointState
}

func newCheckpointTracker() *checkpointTracker {
	return &checkpointTracker{active: make(map[string]*checkpointState)}
}

func (t *checkpointTracker) set(beadID string, cs *checkpointState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active[beadID] = cs
}

func (t *checkpointTracker) get(beadID string) *checkpointState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active[beadID]
}

func (t *checkpointTracker) clear(beadID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.active, beadID)
}

// generateCheckpointID returns a unique checkpoint correlation ID.
//
// TODO(oro-l024): spec §9.3 specifies UUIDv7. Current `cp-<UnixNano>` form is
// process-locally unique and time-ordered but not RFC 9562 compliant and not
// sortable across dispatcher processes. Swap to github.com/google/uuid v7.
func generateCheckpointID() string {
	return fmt.Sprintf("cp-%d", time.Now().UnixNano())
}

// triggerCheckpoint initiates a checkpoint for beadID/workerID when context
// usage crosses the configured threshold (§9.3 step 1).  It generates a fresh
// checkpoint_id, registers the in-flight state, and emits the
// checkpoint_requested event to both the SQLite events table and the bead's
// journey so that a restarted dispatcher can recover the in-flight ID.
func (d *Dispatcher) triggerCheckpoint(ctx context.Context, beadID, workerID string, contextPct int) {
	cpID := generateCheckpointID()
	deadline := d.nowFunc().Add(30 * time.Second)

	d.checkpoints.set(beadID, &checkpointState{
		checkpointID: cpID,
		workerID:     workerID,
		deadline:     deadline,
	})

	payload := fmt.Sprintf(
		`{"checkpoint_id":%q,"worker_id":%q,"trigger":"context_threshold","context_pct":%d,"deadline_seconds":30}`,
		cpID, workerID, contextPct,
	)
	_ = d.logEvent(ctx, "checkpoint_requested", "dispatcher", beadID, workerID, payload)
	_ = d.beads.AppendJourney(ctx, beadID, beadstore.JourneyEvent{
		Ts:      d.nowFunc().UTC().Format(time.RFC3339Nano),
		Actor:   "dispatcher",
		Event:   "checkpoint_requested",
		Payload: payload,
	})
}

// handleCheckpointAck processes a CHECKPOINT_ACK message from a worker (§9.3
// steps 3–5).  Three outcomes are possible:
//   - Valid ack within deadline  → checkpoint_acked + checkpointed (forced=false)
//   - Valid ack after deadline   → checkpoint_acked + checkpointed (forced=true)
//   - Stale ack (wrong ID)       → note event only, no state mutation
func (d *Dispatcher) handleCheckpointAck(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.CheckpointAck == nil {
		return
	}
	ack := msg.CheckpointAck
	beadID := ack.BeadID

	cs := d.checkpoints.get(beadID)

	// Stale ack rules (§9.3 step 3):
	//   - no active checkpoint for this bead, or
	//   - ack's checkpoint_id differs from the active one, or
	//   - ack came from a different worker_id than the one in the original
	//     checkpoint_requested event (defense-in-depth on respawn races).
	if cs == nil || ack.CheckpointID != cs.checkpointID || (cs.workerID != "" && workerID != cs.workerID) {
		originalID := ack.CheckpointID
		payload := fmt.Sprintf(`{"kind":"stale_checkpoint_ack","original_id":%q}`, originalID)
		_ = d.logEvent(ctx, "note", "dispatcher", beadID, workerID, payload)
		_ = d.beads.AppendJourney(ctx, beadID, beadstore.JourneyEvent{
			Ts:      d.nowFunc().UTC().Format(time.RFC3339Nano),
			Actor:   "dispatcher",
			Event:   "note",
			Payload: payload,
		})
		return
	}

	// Valid ack — determine forced flag then emit both events.
	forced := d.nowFunc().After(cs.deadline)

	ackedPayload := fmt.Sprintf(
		`{"checkpoint_id":%q,"committed_sha":%q,"intent_summary":%q}`,
		ack.CheckpointID, ack.CommittedSHA, ack.IntentSummary,
	)
	_ = d.logEvent(ctx, "checkpoint_acked", "dispatcher", beadID, workerID, ackedPayload)
	_ = d.beads.AppendJourney(ctx, beadID, beadstore.JourneyEvent{
		Ts:      d.nowFunc().UTC().Format(time.RFC3339Nano),
		Actor:   "dispatcher",
		Event:   "checkpoint_acked",
		Payload: ackedPayload,
	})

	checkpointedPayload := fmt.Sprintf(
		`{"checkpoint_id":%q,"forced":%v}`,
		ack.CheckpointID, forced,
	)
	_ = d.logEvent(ctx, "checkpointed", "dispatcher", beadID, workerID, checkpointedPayload)
	_ = d.beads.AppendJourney(ctx, beadID, beadstore.JourneyEvent{
		Ts:      d.nowFunc().UTC().Format(time.RFC3339Nano),
		Actor:   "dispatcher",
		Event:   "checkpointed",
		Payload: checkpointedPayload,
	})

	d.checkpoints.clear(beadID)
}

// findInflightCheckpoint scans journey events (ascending chronological order)
// and returns the checkpoint state for the most recent checkpoint_requested
// event that has no corresponding checkpointed or checkpoint_failed event.
// Returns nil when no in-flight checkpoint exists.
//
// The returned state has deadline=zero — after a dispatcher restart the
// original deadline cannot be reconstructed, so any subsequent ack will be
// treated as late (forced=true), per §9.3 step 5.
//
// Used by restoreInflightCheckpoints after a dispatcher restart to
// reconstruct in-memory checkpoint state from the durable bead journey
// (§9.3 respawn path).
func findInflightCheckpoint(events []beadstore.JourneyEvent) *checkpointState {
	completed := make(map[string]bool)
	var lastRequested *checkpointState

	for _, evt := range events {
		switch evt.Event {
		case "checkpoint_requested":
			var p struct {
				CheckpointID string `json:"checkpoint_id"`
				WorkerID     string `json:"worker_id"`
			}
			if err := json.Unmarshal([]byte(evt.Payload), &p); err == nil && p.CheckpointID != "" {
				lastRequested = &checkpointState{
					checkpointID: p.CheckpointID,
					workerID:     p.WorkerID,
				}
			}
		case "checkpointed", "checkpoint_failed":
			var p struct {
				CheckpointID string `json:"checkpoint_id"`
			}
			if err := json.Unmarshal([]byte(evt.Payload), &p); err == nil && p.CheckpointID != "" {
				completed[p.CheckpointID] = true
			}
		}
	}

	if lastRequested == nil || completed[lastRequested.checkpointID] {
		return nil
	}
	return lastRequested
}

// restoreInflightCheckpoints reads each restored bead's journey and re-populates
// the in-memory checkpoint tracker with any in-flight checkpoint discovered.
// Called from restoreState after applyRestoredAssignments so that an ack
// arriving post-restart is correlated with the original checkpoint_id rather
// than treated as stale.
func (d *Dispatcher) restoreInflightCheckpoints(ctx context.Context, restored []restoredAssignment) {
	for _, a := range restored {
		events, err := d.beads.Journey(ctx, a.beadID, time.Time{})
		if err != nil {
			continue
		}
		cs := findInflightCheckpoint(events)
		if cs == nil {
			continue
		}
		d.checkpoints.set(a.beadID, cs)
		_ = d.logEvent(ctx, "checkpoint_recovered", "dispatcher", a.beadID, cs.workerID,
			fmt.Sprintf(`{"checkpoint_id":%q}`, cs.checkpointID))
	}
}
