package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

// retroactiveGateChildThreshold is the number of children that triggers the
// premortem gate retroactively (§11.4: CountChildren > 5 fires eligible).
const retroactiveGateChildThreshold = 5

// PremortemGateError is returned by CheckPremortemGate when a bead cannot
// enter EXECUTE because the parent's premortem gate is unsatisfied.
type PremortemGateError struct {
	ParentID string
	Kind     string
}

func (e *PremortemGateError) Error() string {
	return fmt.Sprintf("blocker_hit: kind=%s parent=%s", e.Kind, e.ParentID)
}

// CreateBeadGraph creates one or more child beads under parentID and then
// checks the retroactive premortem gate (§11.4). When the 6th child causes
// CountChildren to cross the threshold and the parent's gate_state is 'none',
// the gate transitions to 'eligible' and a premortem bead is auto-spawned.
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
	if err := checkRetroactiveGate(ctx, store, parentID); err != nil {
		return created, fmt.Errorf("retroactive gate check: %w", err)
	}
	return created, nil
}

// checkRetroactiveGate fires the §11.4 retroactive trigger: if CountChildren
// exceeds the threshold and the parent's gate_state is still 'none', it
// atomically transitions to 'eligible' and auto-spawns a premortem bead.
func checkRetroactiveGate(ctx context.Context, store beadstore.Store, parentID string) error {
	count, err := store.CountChildren(ctx, parentID)
	if err != nil {
		return fmt.Errorf("count children: %w", err)
	}
	if count <= retroactiveGateChildThreshold {
		return nil
	}
	err = store.SetGateState(ctx, parentID, beadstore.GateNone, beadstore.GateEligible, "retroactive_gate_threshold_crossed")
	if errors.Is(err, beadstore.ErrStaleGate) {
		return nil // gate already set by a concurrent or earlier call
	}
	if err != nil {
		return fmt.Errorf("set gate state: %w", err)
	}
	return spawnPremortemBead(ctx, store, parentID)
}

// spawnPremortemBead creates a premortem bead whose parent is parentID.
func spawnPremortemBead(ctx context.Context, store beadstore.Store, parentID string) error {
	title := "Premortem for " + parentID
	parent, err := store.Show(ctx, parentID)
	if err == nil && parent != nil && parent.Title != "" {
		title = "Premortem for " + parent.Title
	}
	_, err = store.Create(ctx, beadstore.CreateParams{
		Title:    title,
		Type:     "premortem",
		ParentID: parentID,
	})
	if err != nil {
		return fmt.Errorf("spawn premortem bead for %s: %w", parentID, err)
	}
	return nil
}

// CheckPremortemGate checks whether beadID can enter EXECUTE (§11.4). If the
// bead's parent has gate_state='eligible' and no closed premortem child exists,
// a blocker_hit journey event is recorded and a PremortemGateError is returned.
func CheckPremortemGate(ctx context.Context, store beadstore.Store, beadID string) error {
	bead, err := store.Show(ctx, beadID)
	if err != nil {
		return fmt.Errorf("check premortem gate: show bead %s: %w", beadID, err)
	}
	if bead == nil || bead.Epic == "" {
		return nil // no parent → no gate
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

	payload := fmt.Sprintf(`{"kind":"premortem_required","parent_id":%q,"bead_id":%q}`, bead.Epic, beadID)
	_ = store.AppendJourney(ctx, beadID, beadstore.JourneyEvent{
		BeadID:  beadID,
		Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		Actor:   "dispatcher",
		Event:   "blocker_hit",
		Payload: payload,
	})
	return &PremortemGateError{ParentID: bead.Epic, Kind: "premortem_required"}
}
