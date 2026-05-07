//nolint:testpackage // These tests exercise SQLiteStore internals such as the writeMu lock path and raw DB state.
package beadstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"oro/pkg/beadstore/migrations"
)

// newV3TestStore opens an in-memory SQLiteStore with both the v20 bead schema
// and the v3 migration applied, so bead_journey and the new beads columns are
// available.
func newV3TestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store := newTestSQLiteStore(t)
	if err := migrations.MigrateToV3(context.Background(), store.db); err != nil {
		t.Fatalf("MigrateToV3: %v", err)
	}
	return store
}

// mustCreateBead is a convenience wrapper that creates a bead and fails the
// test on error.
func mustCreateBead(t *testing.T, s *SQLiteStore, id, title string) {
	t.Helper()
	if _, err := s.Create(context.Background(), CreateParams{ID: id, Title: title}); err != nil {
		t.Fatalf("create bead %s: %v", id, err)
	}
}

// TestV3Methods is the acceptance test for the v3 Store methods:
// AppendJourney, Journey, LatestJourney, TransitionPipelineStage.
func TestV3Methods(t *testing.T) {
	t.Run("AppendJourney_single_insert", func(t *testing.T) {
		ctx := context.Background()
		store := newV3TestStore(t)
		mustCreateBead(t, store, "b1", "bead one")

		evt := JourneyEvent{
			BeadID:  "b1",
			Ts:      time.Now().UTC().Format(time.RFC3339Nano),
			Actor:   "worker",
			Event:   "started",
			Payload: `{"turn":1}`,
		}
		if err := store.AppendJourney(ctx, "b1", evt); err != nil {
			t.Fatalf("AppendJourney: %v", err)
		}

		events, err := store.LatestJourney(ctx, "b1", 10)
		if err != nil {
			t.Fatalf("LatestJourney: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("got %d events, want 1", len(events))
		}
		got := events[0]
		if got.BeadID != "b1" || got.Actor != "worker" || got.Event != "started" || got.Payload != `{"turn":1}` {
			t.Fatalf("unexpected event: %+v", got)
		}
	})

	t.Run("AppendJourney_multiple_events_ordered", func(t *testing.T) {
		ctx := context.Background()
		store := newV3TestStore(t)
		mustCreateBead(t, store, "b2", "bead two")

		ts := time.Now().UTC()
		for i, evtName := range []string{"claimed", "started", "commit"} {
			evt := JourneyEvent{
				BeadID: "b2",
				Ts:     ts.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
				Actor:  "worker",
				Event:  evtName,
			}
			if err := store.AppendJourney(ctx, "b2", evt); err != nil {
				t.Fatalf("AppendJourney %s: %v", evtName, err)
			}
		}

		events, err := store.LatestJourney(ctx, "b2", 10)
		if err != nil {
			t.Fatalf("LatestJourney: %v", err)
		}
		if len(events) != 3 {
			t.Fatalf("got %d events, want 3", len(events))
		}
		// LatestJourney returns chronological order (oldest first).
		want := []string{"claimed", "started", "commit"}
		for i, e := range events {
			if e.Event != want[i] {
				t.Errorf("event[%d].Event = %q, want %q", i, e.Event, want[i])
			}
		}
	})

	t.Run("LatestJourney_limit_respected", func(t *testing.T) {
		ctx := context.Background()
		store := newV3TestStore(t)
		mustCreateBead(t, store, "b3", "bead three")

		ts := time.Now().UTC()
		for i := range 5 {
			evt := JourneyEvent{
				BeadID: "b3",
				Ts:     ts.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
				Actor:  "dispatcher",
				Event:  "note",
			}
			if err := store.AppendJourney(ctx, "b3", evt); err != nil {
				t.Fatalf("AppendJourney: %v", err)
			}
		}

		events, err := store.LatestJourney(ctx, "b3", 3)
		if err != nil {
			t.Fatalf("LatestJourney: %v", err)
		}
		if len(events) != 3 {
			t.Fatalf("LatestJourney(3): got %d events, want 3", len(events))
		}
	})

	t.Run("Journey_since_filter", func(t *testing.T) {
		ctx := context.Background()
		store := newV3TestStore(t)
		mustCreateBead(t, store, "b4", "bead four")

		base := time.Now().UTC()
		for i, evtName := range []string{"claimed", "started", "edit", "commit"} {
			evt := JourneyEvent{
				BeadID: "b4",
				Ts:     base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
				Actor:  "worker",
				Event:  evtName,
			}
			if err := store.AppendJourney(ctx, "b4", evt); err != nil {
				t.Fatalf("AppendJourney %s: %v", evtName, err)
			}
		}

		// Since after the first two events.
		since := base.Add(2 * time.Second)
		events, err := store.Journey(ctx, "b4", since)
		if err != nil {
			t.Fatalf("Journey: %v", err)
		}
		if len(events) != 2 {
			t.Fatalf("Journey(since+2s): got %d events, want 2", len(events))
		}
		if events[0].Event != "edit" || events[1].Event != "commit" {
			t.Fatalf("unexpected events: %v %v", events[0].Event, events[1].Event)
		}
	})

	t.Run("TransitionPipelineStage_happy_path", func(t *testing.T) {
		ctx := context.Background()
		store := newV3TestStore(t)
		mustCreateBead(t, store, "p1", "pipeline bead")

		// Set pipeline_stage to 'none' to establish a known starting point.
		if _, err := store.db.ExecContext(ctx, `UPDATE beads SET pipeline_stage='none' WHERE id='p1'`); err != nil {
			t.Fatalf("seed pipeline_stage: %v", err)
		}

		if err := store.TransitionPipelineStage(ctx, "p1", StageNone, StageAssess); err != nil {
			t.Fatalf("TransitionPipelineStage none→assess: %v", err)
		}

		var ps string
		if err := store.db.QueryRowContext(ctx, `SELECT pipeline_stage FROM beads WHERE id='p1'`).Scan(&ps); err != nil {
			t.Fatalf("query pipeline_stage: %v", err)
		}
		if ps != "assess" {
			t.Fatalf("pipeline_stage = %q, want assess", ps)
		}

		// Verify pipeline_stage_changed event was appended.
		events, err := store.LatestJourney(ctx, "p1", 10)
		if err != nil {
			t.Fatalf("LatestJourney: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 journey event, got %d", len(events))
		}
		if events[0].Event != "pipeline_stage_changed" {
			t.Fatalf("event = %q, want pipeline_stage_changed", events[0].Event)
		}
	})

	t.Run("TransitionPipelineStage_stale_rolls_back", func(t *testing.T) {
		ctx := context.Background()
		store := newV3TestStore(t)
		mustCreateBead(t, store, "p2", "pipeline stale")

		// Seed to a known stage.
		if _, err := store.db.ExecContext(ctx, `UPDATE beads SET pipeline_stage='assess' WHERE id='p2'`); err != nil {
			t.Fatalf("seed pipeline_stage: %v", err)
		}

		// Attempt transition with wrong `from` — should return ErrStaleStage.
		err := store.TransitionPipelineStage(ctx, "p2", StageNone, StagePlan)
		if !errors.Is(err, ErrStaleStage) {
			t.Fatalf("expected ErrStaleStage, got %v", err)
		}

		// pipeline_stage must remain 'assess' — transaction rolled back.
		var ps string
		if err := store.db.QueryRowContext(ctx, `SELECT pipeline_stage FROM beads WHERE id='p2'`).Scan(&ps); err != nil {
			t.Fatalf("query pipeline_stage: %v", err)
		}
		if ps != "assess" {
			t.Fatalf("pipeline_stage = %q after stale attempt, want assess", ps)
		}

		// No journey events should have been emitted.
		events, err := store.LatestJourney(ctx, "p2", 10)
		if err != nil {
			t.Fatalf("LatestJourney: %v", err)
		}
		if len(events) != 0 {
			t.Fatalf("expected 0 journey events after stale rollback, got %d", len(events))
		}
	})

	t.Run("TransitionPipelineStage_sequential_transitions", func(t *testing.T) {
		ctx := context.Background()
		store := newV3TestStore(t)
		mustCreateBead(t, store, "p3", "pipeline chain")

		if _, err := store.db.ExecContext(ctx, `UPDATE beads SET pipeline_stage='none' WHERE id='p3'`); err != nil {
			t.Fatalf("seed pipeline_stage: %v", err)
		}

		transitions := []struct{ from, to PipelineStage }{
			{StageNone, StageAssess},
			{StageAssess, StagePlan},
			{StagePlan, StagePrepare},
			{StagePrepare, StageExecute},
		}
		for _, tr := range transitions {
			if err := store.TransitionPipelineStage(ctx, "p3", tr.from, tr.to); err != nil {
				t.Fatalf("TransitionPipelineStage %s→%s: %v", tr.from, tr.to, err)
			}
		}

		events, err := store.LatestJourney(ctx, "p3", 10)
		if err != nil {
			t.Fatalf("LatestJourney: %v", err)
		}
		if len(events) != 4 {
			t.Fatalf("expected 4 journey events for 4 transitions, got %d", len(events))
		}
	})
}
