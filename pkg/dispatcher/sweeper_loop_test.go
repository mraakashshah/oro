package dispatcher //nolint:testpackage // white-box: exercises the dispatcher-owned sweep seam

import (
	"context"
	"testing"

	"oro/pkg/cards"
)

func TestGradeDrainSweep(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, spawner := newTestDispatcher(t)

	proposal, err := d.cardStore.Create(ctx, cards.CardCreateParams{
		ID:          "card-grade-drain",
		Type:        cards.CardTypeDecision,
		Title:       "Grade drain proposal",
		BodySummary: "The sweeper must resolve proposed cards.",
		BodyFull:    "The sweeper must resolve proposed cards through the escalation driver.",
		GradeState:  string(cards.GradeStateProposed),
	})
	if err != nil {
		t.Fatalf("create proposal: %v", err)
	}
	secondProposal, err := d.cardStore.Create(ctx, cards.CardCreateParams{
		ID:          "card-grade-drain-second",
		Type:        cards.CardTypePattern,
		Title:       "Second grade drain proposal",
		BodySummary: "Every proposed card is handled in a single sweep.",
		BodyFull:    "Every proposed card is handled in a single sweep.",
		GradeState:  string(cards.GradeStateProposed),
	})
	if err != nil {
		t.Fatalf("create second proposal: %v", err)
	}

	d.cfg.GradeGateEnabled = true
	spawner.verdict = `{"verdict":"correct","confidence":0.99,"reasoning":"proposal is supported"}`
	d.run5MinSweepers(ctx)

	proposed, err := d.cardStore.ListProposed(ctx)
	if err != nil {
		t.Fatalf("ListProposed: %v", err)
	}
	if len(proposed) != 0 {
		t.Fatalf("proposed cards = %d, want 0", len(proposed))
	}
	if got := spawner.SpawnCount(); got != 2 {
		t.Fatalf("grade spawns = %d, want 2", got)
	}

	for _, cardID := range []string{proposal.ID, secondProposal.ID} {
		var state string
		if err := d.db.QueryRowContext(ctx, `SELECT grade_state FROM cards WHERE id = ?`, cardID).Scan(&state); err != nil {
			t.Fatalf("query grade state for %s: %v", cardID, err)
		}
		if state != string(cards.GradeStateApplied) && state != string(cards.GradeStateRejected) {
			t.Fatalf("grade_state for %s = %q, want applied or rejected", cardID, state)
		}
	}

	untouched, err := d.cardStore.Create(ctx, cards.CardCreateParams{
		ID:          "card-grade-gate-off",
		Type:        cards.CardTypePattern,
		Title:       "Disabled drain proposal",
		BodySummary: "The disabled grade gate does not drain proposals.",
		BodyFull:    "The disabled grade gate does not drain proposals.",
		GradeState:  string(cards.GradeStateProposed),
	})
	if err != nil {
		t.Fatalf("create disabled proposal: %v", err)
	}

	d.cfg.GradeGateEnabled = false
	d.run5MinSweepers(ctx)
	proposed, err = d.cardStore.ListProposed(ctx)
	if err != nil {
		t.Fatalf("ListProposed after disabled sweep: %v", err)
	}
	if len(proposed) != 1 || proposed[0].ID != untouched.ID {
		t.Fatalf("proposed cards after disabled sweep = %#v, want only %q", proposed, untouched.ID)
	}
	if got := spawner.SpawnCount(); got != 2 {
		t.Fatalf("grade spawns after disabled sweep = %d, want 2", got)
	}
}
