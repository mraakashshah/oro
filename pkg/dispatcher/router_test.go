package dispatcher //nolint:testpackage // white-box: needs access to Dispatcher.beads

import (
	"context"
	"errors"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

// TestRouterAndCloseBead verifies:
//  1. BuildPrompt routes by bead type to the correct assembler (§10.2)
//  2. Dispatcher.CloseBead calls store.Close then runs the child-promote sweep (§10.4)
//  3. Sweep failure (including ErrStaleStage) is non-fatal — CloseBead returns nil
func TestRouterAndCloseBead(t *testing.T) {
	ctx := context.Background()

	t.Run("task_routes_to_worker_prompt", func(t *testing.T) {
		b := protocol.Bead{ID: "t1", Type: "task", Title: "Fix bug", AcceptanceCriteria: "pass"}
		got, err := BuildPrompt(ctx, beadstore.NewFakeStore(), b)
		if err != nil {
			t.Fatalf("task routing: unexpected error: %v", err)
		}
		if got == "" {
			t.Error("task routing: expected non-empty prompt")
		}
	})

	t.Run("bug_routes_to_worker_prompt", func(t *testing.T) {
		b := protocol.Bead{ID: "b1", Type: "bug", Title: "Fix crash"}
		got, err := BuildPrompt(ctx, beadstore.NewFakeStore(), b)
		if err != nil {
			t.Fatalf("bug routing: unexpected error: %v", err)
		}
		if got == "" {
			t.Error("bug routing: expected non-empty prompt")
		}
	})

	t.Run("chore_routes_to_worker_prompt", func(t *testing.T) {
		b := protocol.Bead{ID: "c1", Type: "chore", Title: "Cleanup"}
		got, err := BuildPrompt(ctx, beadstore.NewFakeStore(), b)
		if err != nil {
			t.Fatalf("chore routing: unexpected error: %v", err)
		}
		if got == "" {
			t.Error("chore routing: expected non-empty prompt")
		}
	})

	t.Run("research_routes_to_oracle_stub", func(t *testing.T) {
		b := protocol.Bead{ID: "r1", Type: "research", Title: "Investigate X"}
		got, err := BuildPrompt(ctx, beadstore.NewFakeStore(), b)
		if err != nil {
			t.Fatalf("research routing: unexpected error: %v", err)
		}
		if got == "" {
			t.Error("research routing: expected non-empty prompt")
		}
	})

	t.Run("premortem_routes_to_premortem_stub", func(t *testing.T) {
		b := protocol.Bead{ID: "p1", Type: "premortem", Title: "Review risks"}
		got, err := BuildPrompt(ctx, beadstore.NewFakeStore(), b)
		if err != nil {
			t.Fatalf("premortem routing: unexpected error: %v", err)
		}
		if got == "" {
			t.Error("premortem routing: expected non-empty prompt")
		}
	})

	t.Run("epic_returns_error", func(t *testing.T) {
		b := protocol.Bead{ID: "e1", Type: "epic", Title: "Big feature"}
		_, err := BuildPrompt(ctx, beadstore.NewFakeStore(), b)
		if err == nil {
			t.Error("epic routing: expected error, got nil")
		}
	})

	t.Run("review_returns_error", func(t *testing.T) {
		b := protocol.Bead{ID: "rv1", Type: "review", Title: "Code review"}
		_, err := BuildPrompt(ctx, beadstore.NewFakeStore(), b)
		if err == nil {
			t.Error("review routing: expected error, got nil")
		}
	})

	t.Run("unknown_type_returns_error", func(t *testing.T) {
		b := protocol.Bead{ID: "u1", Type: "mystery-type"}
		_, err := BuildPrompt(ctx, beadstore.NewFakeStore(), b)
		if err == nil {
			t.Error("unknown routing: expected error, got nil")
		}
	})

	t.Run("close_bead_closes_and_sweeps", func(t *testing.T) {
		parent := protocol.Bead{
			ID:     "parent-1",
			Type:   "research",
			Status: "open",
		}
		child := protocol.Bead{
			ID:     "child-1",
			Type:   "task",
			Status: "open",
			Epic:   "parent-1",
			Tags:   []string{"awaits_parent_close", "other-tag"},
		}
		store := beadstore.NewFakeStore(parent, child)
		d := &Dispatcher{beads: store}

		if err := d.CloseBead(ctx, "parent-1", "done"); err != nil {
			t.Fatalf("CloseBead: unexpected error: %v", err)
		}

		// Parent must be closed.
		got, _ := store.Show(ctx, "parent-1")
		if got == nil || got.Status != "closed" {
			t.Errorf("parent status: want closed, got %v", got)
		}

		// Child's awaits_parent_close tag must be stripped; other tags preserved.
		gotChild, _ := store.Show(ctx, "child-1")
		for _, tag := range gotChild.Tags {
			if tag == "awaits_parent_close" {
				t.Error("child still carries awaits_parent_close tag after sweep")
			}
		}
		found := false
		for _, tag := range gotChild.Tags {
			if tag == "other-tag" {
				found = true
			}
		}
		if !found {
			t.Error("child lost unrelated tag 'other-tag' after sweep")
		}
	})

	t.Run("close_bead_sweep_failure_is_nonfatal", func(t *testing.T) {
		// Sweep error (including ErrStaleStage) must not bubble out of CloseBead.
		store := &sweepFailStore{
			FakeStore: beadstore.NewFakeStore(
				protocol.Bead{ID: "px", Type: "research", Status: "open"},
			),
		}
		d := &Dispatcher{beads: store}

		err := d.CloseBead(ctx, "px", "done")
		if err != nil {
			t.Fatalf("CloseBead with sweep failure: expected nil, got %v", err)
		}
		// Bead must still be closed even though sweep failed.
		got, _ := store.Show(ctx, "px")
		if got == nil || got.Status != "closed" {
			t.Errorf("bead status after sweep failure: want closed, got %v", got)
		}
	})

	t.Run("close_bead_store_error_propagates", func(t *testing.T) {
		// If store.Close itself fails the error must surface.
		store := beadstore.NewFakeStore() // no bead with this ID
		d := &Dispatcher{beads: store}

		err := d.CloseBead(ctx, "nonexistent", "done")
		if err == nil {
			t.Error("CloseBead on missing bead: expected error, got nil")
		}
	})
}

// sweepFailStore causes FindByParentAndTag (used by the sweep) to return an
// error so PromoteChildrenOnParentClose always fails. This simulates ErrStaleStage
// or any other sweep-time error.
type sweepFailStore struct {
	*beadstore.FakeStore
}

func (s *sweepFailStore) FindByParentAndTag(_ context.Context, _, _ string) ([]protocol.Bead, error) {
	return nil, errors.New("simulated sweep failure (ErrStaleStage equivalent)")
}
