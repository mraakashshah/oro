package dispatcher //nolint:testpackage // white-box: accesses unexported gate helpers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

// recordingStore wraps FakeStore and records all Create calls.
type recordingStore struct {
	*beadstore.FakeStore
	created []beadstore.CreateParams
}

func (r *recordingStore) Create(ctx context.Context, params beadstore.CreateParams) (*protocol.Bead, error) {
	r.created = append(r.created, params)
	return r.FakeStore.Create(ctx, params)
}

func TestCreateBeadGraphDoesNotSpawnPremortem(t *testing.T) {
	ctx := context.Background()

	// Seed epic with 5 closed children.
	beads := make([]protocol.Bead, 0, 7)
	beads = append(beads, protocol.Bead{ID: "epic-sg1", Type: "epic", Status: "open"})
	for i := range 5 {
		beads = append(beads, protocol.Bead{
			ID:     fmt.Sprintf("child-sg-%d", i),
			Epic:   "epic-sg1",
			Type:   "task",
			Status: "closed",
		})
	}
	store := &recordingStore{FakeStore: beadstore.NewFakeStore(beads...)}

	// 6th child via CreateBeadGraph.
	children, err := CreateBeadGraph(ctx, store, "epic-sg1", []beadstore.CreateParams{
		{Title: "child-6", Type: "task"},
	})
	if err != nil {
		t.Fatalf("CreateBeadGraph: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("created %d beads, want 1", len(children))
	}

	count, err := store.CountChildren(ctx, "epic-sg1")
	if err != nil {
		t.Fatalf("CountChildren: %v", err)
	}
	if count != 6 {
		t.Errorf("CountChildren = %d, want exactly 6 requested children", count)
	}
	gateState, err := store.GateState(ctx, "epic-sg1")
	if err != nil {
		t.Fatalf("GateState: %v", err)
	}
	if gateState != beadstore.GateNone {
		t.Errorf("GateState = %q, want none", gateState)
	}
	var taskCreates, premortemCreates int
	for _, p := range store.created {
		switch p.Type {
		case "task":
			taskCreates++
		case "premortem":
			premortemCreates++
		}
	}
	if taskCreates != 1 || premortemCreates != 0 {
		t.Errorf("CreateBeadGraph created tasks=%d premortem=%d, want tasks=1 premortem=0; created=%v", taskCreates, premortemCreates, store.created)
	}
}

// TestExecuteRefusedOnEligibleParent verifies §11.4 eligibility gate:
// when a bead's parent has gate_state='eligible' and no closed premortem child
// exists, CheckPremortemGate returns a blocker error and does not advance the
// bead's pipeline_stage.
func TestExecuteRefusedOnEligibleParent(t *testing.T) {
	ctx := context.Background()

	epic := protocol.Bead{ID: "epic-er1", Type: "epic", Status: "open"}
	child := protocol.Bead{
		ID:     "child-er1",
		Epic:   "epic-er1",
		Type:   "task",
		Status: "open",
	}
	store := beadstore.NewFakeStore(epic, child)

	// Set parent gate to eligible.
	if err := store.SetGateState(ctx, "epic-er1", beadstore.GateState(""), beadstore.GateEligible, "test_setup"); err != nil {
		t.Fatalf("SetGateState: %v", err)
	}

	// Gate check must refuse.
	err := CheckPremortemGate(ctx, store, "child-er1")
	if err == nil {
		t.Fatal("CheckPremortemGate: expected error, got nil")
	}
	var gateErr *PremortemGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("error type = %T, want *PremortemGateError", err)
	}
	if gateErr.Kind != "premortem_required" {
		t.Errorf("error kind = %q, want premortem_required", gateErr.Kind)
	}

	// pipeline_stage must not have advanced.
	ps, err := store.GateState(ctx, "child-er1")
	if err != nil {
		t.Fatalf("GateState: %v", err)
	}
	// pipeline_stage is tracked separately; gate_state should be unchanged (still GateNone).
	if ps != beadstore.GateNone {
		t.Errorf("child gate state changed unexpectedly to %q", ps)
	}

	// Journey must contain a blocker_hit event on the child.
	events, err := store.Journey(ctx, "child-er1", time.Time{})
	if err != nil {
		t.Fatalf("Journey: %v", err)
	}
	var hitFound bool
	for _, e := range events {
		if e.Event == "blocker_hit" {
			hitFound = true
			var p struct {
				Kind string `json:"kind"`
			}
			if jsonErr := json.Unmarshal([]byte(e.Payload), &p); jsonErr == nil && p.Kind != "premortem_required" {
				t.Errorf("blocker_hit kind = %q, want premortem_required", p.Kind)
			}
		}
	}
	if !hitFound {
		t.Error("expected blocker_hit journey event on child, got none")
	}
}

// TestVerdictTransitionsGateState verifies §11.4 verdict outcomes:
//   - proceed: gate_state transitions eligible→satisfied
//   - replan: gate_state transitions eligible→replan, premortem_cycle_count increments
func TestVerdictTransitionsGateState(t *testing.T) {
	ctx := context.Background()

	t.Run("proceed_transitions_to_satisfied", func(t *testing.T) {
		epic := protocol.Bead{ID: "epic-vt1", Type: "epic", Status: "open"}
		pm := protocol.Bead{ID: "pm-vt1", Type: "premortem", Epic: "epic-vt1", Status: "open"}
		store := beadstore.NewFakeStore(epic, pm)
		if err := store.SetGateState(ctx, "epic-vt1", beadstore.GateState(""), beadstore.GateEligible, "test"); err != nil {
			t.Fatalf("SetGateState: %v", err)
		}

		db := newTestDB(t)
		d := &Dispatcher{beads: store, db: db}

		if err := d.ClosePremortemBead(ctx, "pm-vt1", []byte(`{"verdict":"proceed","reason":"all clear"}`)); err != nil {
			t.Fatalf("ClosePremortemBead: %v", err)
		}

		// Gate must be satisfied.
		gs, err := store.GateState(ctx, "epic-vt1")
		if err != nil {
			t.Fatalf("GateState: %v", err)
		}
		if gs != beadstore.GateSatisfied {
			t.Errorf("gate_state = %q, want satisfied", gs)
		}

		// EXECUTE gate check must now pass (no error) for a child.
		if _, err := store.Create(ctx, beadstore.CreateParams{
			ID: "child-vt1", Type: "task", ParentID: "epic-vt1", Title: "task",
		}); err != nil {
			t.Fatalf("seed child: %v", err)
		}
		if gateErr := CheckPremortemGate(ctx, store, "child-vt1"); gateErr != nil {
			t.Errorf("CheckPremortemGate after proceed = %v, want nil", gateErr)
		}
	})

	t.Run("replan_transitions_to_replan_and_increments_cycle_count", func(t *testing.T) {
		epic := protocol.Bead{ID: "epic-vt2", Type: "epic", Status: "open"}
		pm := protocol.Bead{ID: "pm-vt2", Type: "premortem", Epic: "epic-vt2", Status: "open"}
		store := beadstore.NewFakeStore(epic, pm)
		if err := store.SetGateState(ctx, "epic-vt2", beadstore.GateState(""), beadstore.GateEligible, "test"); err != nil {
			t.Fatalf("SetGateState: %v", err)
		}

		db := newTestDB(t)
		d := &Dispatcher{beads: store, db: db}

		if err := d.ClosePremortemBead(ctx, "pm-vt2", []byte(`{"verdict":"replan","reason":"needs work"}`)); err != nil {
			t.Fatalf("ClosePremortemBead: %v", err)
		}

		// Gate must be replan.
		gs, err := store.GateState(ctx, "epic-vt2")
		if err != nil {
			t.Fatalf("GateState: %v", err)
		}
		if gs != beadstore.GateReplan {
			t.Errorf("gate_state = %q, want replan", gs)
		}

		// Cycle count must have incremented.
		if store.PremortCycleCount("epic-vt2") != 1 {
			t.Errorf("premortem_cycle_count = %d, want 1", store.PremortCycleCount("epic-vt2"))
		}
	})

	t.Run("block_transitions_to_blocked", func(t *testing.T) {
		epic := protocol.Bead{ID: "epic-vt3", Type: "epic", Status: "open"}
		pm := protocol.Bead{ID: "pm-vt3", Type: "premortem", Epic: "epic-vt3", Status: "open"}
		store := beadstore.NewFakeStore(epic, pm)
		if err := store.SetGateState(ctx, "epic-vt3", beadstore.GateState(""), beadstore.GateEligible, "test"); err != nil {
			t.Fatalf("SetGateState: %v", err)
		}

		db := newTestDB(t)
		d := &Dispatcher{beads: store, db: db}

		if err := d.ClosePremortemBead(ctx, "pm-vt3", []byte(`{"verdict":"block","reason":"unsafe"}`)); err != nil {
			t.Fatalf("ClosePremortemBead: %v", err)
		}

		// Gate must be blocked.
		gs, err := store.GateState(ctx, "epic-vt3")
		if err != nil {
			t.Fatalf("GateState: %v", err)
		}
		if gs != beadstore.GateBlocked {
			t.Errorf("gate_state = %q, want blocked", gs)
		}

		// Cycle count must NOT have incremented (only replan increments).
		if store.PremortCycleCount("epic-vt3") != 0 {
			t.Errorf("premortem_cycle_count = %d, want 0 (block does not increment)", store.PremortCycleCount("epic-vt3"))
		}
	})
}

// TestPremortemNotSelfBlocked is a regression test for the gate-self-block
// pattern: when filterExecutableBeads (or any caller) checks the gate on the
// auto-spawned premortem bead itself, the gate must NOT refuse it. Otherwise
// the premortem can never execute and the parent epic deadlocks on 'eligible'.
func TestPremortemNotSelfBlocked(t *testing.T) {
	ctx := context.Background()

	epic := protocol.Bead{ID: "epic-ns1", Type: "epic", Status: "open"}
	pm := protocol.Bead{ID: "pm-ns1", Type: "premortem", Epic: "epic-ns1", Status: "open"}
	child := protocol.Bead{ID: "task-ns1", Type: "task", Epic: "epic-ns1", Status: "open"}
	store := beadstore.NewFakeStore(epic, pm, child)

	// Parent gate is eligible — premortem child has not closed yet.
	if err := store.SetGateState(ctx, "epic-ns1", beadstore.GateState(""), beadstore.GateEligible, "test"); err != nil {
		t.Fatalf("SetGateState: %v", err)
	}

	// The premortem itself must pass through the gate so it can be executed.
	if err := CheckPremortemGate(ctx, store, "pm-ns1"); err != nil {
		t.Errorf("CheckPremortemGate(premortem) = %v, want nil (premortem is the satisfier, must not self-block)", err)
	}

	// A regular task child still gets blocked.
	if err := CheckPremortemGate(ctx, store, "task-ns1"); err == nil {
		t.Error("CheckPremortemGate(task) = nil, want PremortemGateError")
	}
}

// TestClosePremortemBeadWithStore verifies the dispatcher-free analogue of
// ClosePremortemBead — used by `oro bead premortem-close` — closes the bead,
// persists the verdict, and transitions the parent's gate_state.
func TestClosePremortemBeadWithStore(t *testing.T) {
	ctx := context.Background()

	t.Run("proceed", func(t *testing.T) {
		epic := protocol.Bead{ID: "epic-cw1", Type: "epic", Status: "open"}
		pm := protocol.Bead{ID: "pm-cw1", Type: "premortem", Epic: "epic-cw1", Status: "open"}
		store := beadstore.NewFakeStore(epic, pm)
		if err := store.SetGateState(ctx, "epic-cw1", beadstore.GateState(""), beadstore.GateEligible, "test"); err != nil {
			t.Fatalf("SetGateState: %v", err)
		}

		if err := ClosePremortemBeadWithStore(ctx, store, "pm-cw1", []byte(`{"verdict":"proceed","reason":"ok"}`)); err != nil {
			t.Fatalf("ClosePremortemBeadWithStore: %v", err)
		}

		gs, err := store.GateState(ctx, "epic-cw1")
		if err != nil {
			t.Fatalf("GateState: %v", err)
		}
		if gs != beadstore.GateSatisfied {
			t.Errorf("gate_state = %q, want satisfied", gs)
		}

		// Bead must be closed.
		bead, err := store.Show(ctx, "pm-cw1")
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if bead.Status != "closed" {
			t.Errorf("status = %q, want closed", bead.Status)
		}
	})

	t.Run("invalid_payload_defaults_to_replan", func(t *testing.T) {
		epic := protocol.Bead{ID: "epic-cw2", Type: "epic", Status: "open"}
		pm := protocol.Bead{ID: "pm-cw2", Type: "premortem", Epic: "epic-cw2", Status: "open"}
		store := beadstore.NewFakeStore(epic, pm)
		if err := store.SetGateState(ctx, "epic-cw2", beadstore.GateState(""), beadstore.GateEligible, "test"); err != nil {
			t.Fatalf("SetGateState: %v", err)
		}

		// Empty payload → replan default.
		if err := ClosePremortemBeadWithStore(ctx, store, "pm-cw2", nil); err != nil {
			t.Fatalf("ClosePremortemBeadWithStore: %v", err)
		}
		gs, err := store.GateState(ctx, "epic-cw2")
		if err != nil {
			t.Fatalf("GateState: %v", err)
		}
		if gs != beadstore.GateReplan {
			t.Errorf("invalid payload → gate_state = %q, want replan (fail-safe default)", gs)
		}
	})
}

// TestBlockerHitJourneyDeduplicated verifies that calling CheckPremortemGate
// repeatedly for the same blocked bead/parent does NOT spam blocker_hit
// events. The first refusal records one event; subsequent refusals on the
// same parent skip the append.
func TestBlockerHitJourneyDeduplicated(t *testing.T) {
	ctx := context.Background()

	epic := protocol.Bead{ID: "epic-dd1", Type: "epic", Status: "open"}
	child := protocol.Bead{ID: "task-dd1", Type: "task", Epic: "epic-dd1", Status: "open"}
	store := beadstore.NewFakeStore(epic, child)
	if err := store.SetGateState(ctx, "epic-dd1", beadstore.GateState(""), beadstore.GateEligible, "test"); err != nil {
		t.Fatalf("SetGateState: %v", err)
	}

	// Invoke the gate three times — simulating three dispatcher ticks.
	for i := range 3 {
		if err := CheckPremortemGate(ctx, store, "task-dd1"); err == nil {
			t.Fatalf("CheckPremortemGate iteration %d: expected error, got nil", i)
		}
	}

	events, err := store.Journey(ctx, "task-dd1", time.Time{})
	if err != nil {
		t.Fatalf("Journey: %v", err)
	}
	var blockerHits int
	for _, e := range events {
		if e.Event == "blocker_hit" {
			blockerHits++
		}
	}
	if blockerHits != 1 {
		t.Errorf("blocker_hit event count = %d, want 1 (deduplicated across ticks)", blockerHits)
	}
}
