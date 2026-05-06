package worker_test

import (
	"strings"
	"testing"

	"oro/pkg/cards"
	"oro/pkg/worker"
)

// TestPromptCardsSection verifies the Cards section behaviour in AssemblePrompt:
//  1. prompt has a ## Cards section
//  2. prompt has no ## Memory or ## Previous Feedback sections
//  3. deck view appears for cards beyond the inline budget
func TestPromptCardsSection(t *testing.T) {
	t.Parallel()

	t.Run("has_cards_section", func(t *testing.T) {
		t.Parallel()
		params := worker.PromptParams{
			BeadID: "bead-c01",
			Title:  "Cards test",
		}
		prompt := worker.AssemblePrompt(params)
		if !strings.Contains(prompt, "## Cards") {
			t.Error("prompt must contain ## Cards section")
		}
	})

	t.Run("no_memory_or_previous_feedback", func(t *testing.T) {
		t.Parallel()
		params := worker.PromptParams{
			BeadID:   "bead-c02",
			Title:    "No memory test",
			Attempt:  1,
			Feedback: "previous feedback text",
		}
		prompt := worker.AssemblePrompt(params)
		if strings.Contains(prompt, "## Memory") {
			t.Error("prompt must NOT contain ## Memory section (subsumed by Cards)")
		}
		if strings.Contains(prompt, "## Previous Feedback") {
			t.Error("prompt must NOT contain ## Previous Feedback section (subsumed by Cards)")
		}
	})

	t.Run("deck_shows_for_cards_beyond_inline_budget", func(t *testing.T) {
		t.Parallel()
		inlined := []cards.CardSummary{
			{
				ID:          "card-inline-01",
				Type:        cards.CardTypeRule,
				Title:       "Always wrap errors",
				BodySummary: "Use %w to preserve error chain",
				BodyFull:    "Always wrap errors with %%w so callers can inspect the chain.",
				Score:       2.4,
			},
		}
		deck := append(inlined, cards.CardSummary{
			ID:          "card-deck-02",
			Type:        cards.CardTypePattern,
			Title:       "Auth middleware pattern",
			BodySummary: "Auth uses middleware X then validation Y",
			Score:       1.8,
		})
		params := worker.PromptParams{
			BeadID: "bead-c03",
			Title:  "Deck overflow test",
			Cards: cards.RelevantCards{
				Deck:    deck,
				Inlined: inlined,
			},
		}
		prompt := worker.AssemblePrompt(params)
		if !strings.Contains(prompt, "card-deck-02") {
			t.Error("prompt must show deck view entry for card-deck-02 (beyond inline budget)")
		}
	})
}
