package dispatcher

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

// PremortemGateError is returned by CheckPremortemGate when a bead cannot
// enter EXECUTE because the parent's premortem gate is unsatisfied.
type PremortemGateError struct {
	ParentID string
	Kind     string
}

func (e *PremortemGateError) Error() string {
	return fmt.Sprintf("blocker_hit: kind=%s parent=%s", e.Kind, e.ParentID)
}

// CreateBeadGraph creates one or more child beads under parentID.
// Each child's ParentID is forced to parentID — any value the caller
// sets on params.ParentID is overwritten. This is intentional: CreateBeadGraph
// is the single seam for "create N children under one parent," so a stray
// different ParentID would silently break the graph shape.
func CreateBeadGraph(ctx context.Context, store beadstore.Store, parentID string, children []beadstore.CreateParams) ([]*protocol.Bead, error) {
	created := make([]*protocol.Bead, 0, len(children))
	for _, params := range children {
		params.ParentID = parentID
		b, err := store.Create(ctx, params)
		if err != nil {
			return created, fmt.Errorf("create child bead: %w", err)
		}
		created = append(created, b)
	}
	return created, nil
}

// CheckPremortemGate checks whether beadID can enter EXECUTE (§11.4). If the
// bead's parent has gate_state='eligible' and no closed premortem child exists,
// a blocker_hit journey event is recorded and a PremortemGateError is returned.
//
// Premortem-type beads short-circuit early: the auto-spawned premortem is
// itself the satisfier of the gate, so checking it here would deadlock the
// epic (the premortem could never run, the gate could never move off
// 'eligible'). See pattern: gate-self-block.
//
// To avoid spamming the journey on repeated dispatcher ticks, blocker_hit is
// appended only when the most recent journey event is not already a
// blocker_hit for the same parent.
//
//oro:testonly
func CheckPremortemGate(ctx context.Context, store beadstore.Store, beadID string) error {
	bead, err := store.Show(ctx, beadID)
	if err != nil {
		return fmt.Errorf("check premortem gate: show bead %s: %w", beadID, err)
	}
	if bead == nil || bead.Epic == "" {
		return nil // no parent → no gate
	}
	if strings.EqualFold(bead.Type, "premortem") {
		return nil // premortem is the satisfier, must not self-block
	}
	parentGate, err := store.GateState(ctx, bead.Epic)
	if err != nil {
		return fmt.Errorf("check premortem gate: gate state for %s: %w", bead.Epic, err)
	}
	if parentGate != beadstore.GateEligible {
		return nil // gate not in blocking state
	}
	hasClosed, err := store.HasClosedPremortemChild(ctx, bead.Epic)
	if err != nil {
		return fmt.Errorf("check premortem gate: has closed premortem child for %s: %w", bead.Epic, err)
	}
	if hasClosed {
		return nil // premortem satisfied
	}

	if !blockerHitAlreadyRecorded(ctx, store, beadID, bead.Epic) {
		payload := fmt.Sprintf(`{"kind":"premortem_required","parent_id":%q,"bead_id":%q}`, bead.Epic, beadID)
		if appendErr := store.AppendJourney(ctx, beadID, beadstore.JourneyEvent{
			BeadID:  beadID,
			Ts:      time.Now().UTC().Format(time.RFC3339Nano),
			Actor:   "dispatcher",
			Event:   "blocker_hit",
			Payload: payload,
		}); appendErr != nil {
			slog.Warn("check premortem gate: append blocker_hit",
				"bead_id", beadID, "parent_id", bead.Epic, "err", appendErr)
		}
	}
	return &PremortemGateError{ParentID: bead.Epic, Kind: "premortem_required"}
}

// blockerHitAlreadyRecorded reports whether the most recent journey event for
// beadID is already a blocker_hit naming parentID. When true, the caller skips
// appending another event so repeat-tick refusals don't spam bead_journey.
// Errors are treated as "not recorded" so observability fails open.
func blockerHitAlreadyRecorded(ctx context.Context, store beadstore.Store, beadID, parentID string) bool {
	events, err := store.LatestJourney(ctx, beadID, 1)
	if err != nil || len(events) == 0 {
		return false
	}
	last := events[len(events)-1]
	if last.Event != "blocker_hit" {
		return false
	}
	return strings.Contains(last.Payload, fmt.Sprintf(`"parent_id":%q`, parentID))
}
