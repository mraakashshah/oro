package cards_test

import (
	"context"
	"errors"
	"testing"

	"oro/pkg/cards"
)

func TestPromoteLearning(t *testing.T) {
	ctx := context.Background()

	t.Run("creates_card_resolves_learning_and_records_created_event", func(t *testing.T) {
		store, db := newTestStoreWithBeads(t)
		if _, err := db.ExecContext(ctx, `INSERT INTO beads (id) VALUES (?)`, "bead-1"); err != nil {
			t.Fatalf("insert bead: %v", err)
		}
		candidate := cards.CardCandidate{
			Type:        string(cards.CardTypePattern),
			Title:       "Use shared tx for promotion",
			BodySummary: "Promoting a learning resolves it atomically.",
			BodyFull:    "Create the card, mark the learning promoted, and write the created event in one transaction.",
			Confidence:  0.87,
			Evidence:    []string{"go test ./pkg/cards/... -run TestPromoteLearning -count=1"},
			Tags:        []string{"cards", "tx"},
		}
		learningID, err := store.AppendLearningPending(ctx, "bead-1", candidate)
		if err != nil {
			t.Fatalf("AppendLearningPending: %v", err)
		}

		cardID, err := store.PromoteLearning(ctx, learningID)
		if err != nil {
			t.Fatalf("PromoteLearning: %v", err)
		}
		if cardID == "" {
			t.Fatal("PromoteLearning cardID is empty")
		}

		card, err := store.Show(ctx, cardID)
		if err != nil {
			t.Fatalf("Show promoted card: %v", err)
		}
		if card.Title != candidate.Title || card.Type != cards.CardTypePattern {
			t.Fatalf("promoted card = %+v, want candidate %+v", card, candidate)
		}
		if card.PromotionConfidence == nil || *card.PromotionConfidence != candidate.Confidence {
			t.Fatalf("PromotionConfidence = %v, want %v", card.PromotionConfidence, candidate.Confidence)
		}
		if card.EmergedFrom == nil || *card.EmergedFrom != "bead-1" {
			t.Fatalf("EmergedFrom = %v, want bead-1", card.EmergedFrom)
		}

		var promotedTo string
		if err := db.QueryRowContext(ctx,
			`SELECT promoted_to FROM bead_learnings_pending WHERE id = ?`, learningID,
		).Scan(&promotedTo); err != nil {
			t.Fatalf("query promoted_to: %v", err)
		}
		if promotedTo != cardID {
			t.Fatalf("promoted_to = %q, want %q", promotedTo, cardID)
		}

		var eventCount int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM card_events WHERE card_id = ? AND kind = 'created'`, cardID,
		).Scan(&eventCount); err != nil {
			t.Fatalf("query created event: %v", err)
		}
		if eventCount != 1 {
			t.Fatalf("created event count = %d, want 1", eventCount)
		}
	})

	t.Run("already_resolved_learning_returns_ErrAlreadyResolved", func(t *testing.T) {
		store, db := newTestStoreWithBeads(t)
		if _, err := db.ExecContext(ctx, `INSERT INTO beads (id) VALUES (?)`, "bead-1"); err != nil {
			t.Fatalf("insert bead: %v", err)
		}
		learningID, err := store.AppendLearningPending(ctx, "bead-1", cards.CardCandidate{
			Type:        string(cards.CardTypePattern),
			Title:       "Already resolved",
			BodySummary: "Already resolved learnings are not promoted twice.",
			BodyFull:    "The store rejects a learning once promoted_to or rejected_at is set.",
			Confidence:  0.7,
			Evidence:    []string{"fixture"},
		})
		if err != nil {
			t.Fatalf("AppendLearningPending: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE bead_learnings_pending SET rejected_at = '2026-01-01T00:00:00Z', reason = 'duplicate' WHERE id = ?`,
			learningID,
		); err != nil {
			t.Fatalf("mark rejected: %v", err)
		}

		_, err = store.PromoteLearning(ctx, learningID)
		if !errors.Is(err, cards.ErrAlreadyResolved) {
			t.Fatalf("PromoteLearning err = %v, want ErrAlreadyResolved", err)
		}
	})

	t.Run("rolls_back_when_card_create_fails", func(t *testing.T) {
		store, db := newTestStoreWithBeads(t)
		if _, err := db.ExecContext(ctx, `INSERT INTO beads (id) VALUES (?)`, "bead-1"); err != nil {
			t.Fatalf("insert bead: %v", err)
		}
		learningID, err := store.AppendLearningPending(ctx, "bead-1", cards.CardCandidate{
			Type:        "unknown",
			Title:       "Bad candidate",
			BodySummary: "Invalid card types cannot be promoted.",
			BodyFull:    "The learning must remain unresolved if card creation fails.",
			Confidence:  0.7,
			Evidence:    []string{"fixture"},
		})
		if err != nil {
			t.Fatalf("AppendLearningPending: %v", err)
		}

		_, err = store.PromoteLearning(ctx, learningID)
		if err == nil {
			t.Fatal("PromoteLearning err = nil, want create failure")
		}

		var promotedTo *string
		if err := db.QueryRowContext(ctx,
			`SELECT promoted_to FROM bead_learnings_pending WHERE id = ?`, learningID,
		).Scan(&promotedTo); err != nil {
			t.Fatalf("query promoted_to: %v", err)
		}
		if promotedTo != nil {
			t.Fatalf("promoted_to = %v, want nil after rollback", *promotedTo)
		}
		var cardCount, eventCount int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cards`).Scan(&cardCount); err != nil {
			t.Fatalf("query cards count: %v", err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM card_events`).Scan(&eventCount); err != nil {
			t.Fatalf("query card events count: %v", err)
		}
		if cardCount != 0 || eventCount != 0 {
			t.Fatalf("card/event counts = %d/%d, want 0/0 after rollback", cardCount, eventCount)
		}
	})
}
