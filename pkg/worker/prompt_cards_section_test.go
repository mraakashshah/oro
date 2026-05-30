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
		inlined := []cards.InlinedCard{
			{
				ID:          "card-inline-01",
				Type:        cards.CardTypeRule,
				Title:       "Always wrap errors",
				BodySummary: "Use %w to preserve error chain",
				BodyFull:    "Always wrap errors with %%w so callers can inspect the chain.",
				Score:       2.4,
			},
		}
		deck := []cards.DeckCard{
			{
				ID:          "card-inline-01",
				Type:        cards.CardTypeRule,
				Title:       "Always wrap errors",
				BodySummary: "Use %w to preserve error chain",
				Score:       2.4,
			},
			{
				ID:          "card-deck-02",
				Type:        cards.CardTypePattern,
				Title:       "Auth middleware pattern",
				BodySummary: "Auth uses middleware X then validation Y",
				Score:       1.8,
			},
		}
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

func TestPromptCardsSectionDeckFooterReferencesCardsShow(t *testing.T) {
	t.Parallel()

	const footer = "To see full body of any card: `oro cards show <id>`"

	t.Run("empty_deck_has_placeholder_only", func(t *testing.T) {
		t.Parallel()
		prompt := worker.AssemblePrompt(worker.PromptParams{
			BeadID: "bead-c04",
			Title:  "Empty cards test",
		})

		if !strings.Contains(prompt, "No relevant cards for this task.") {
			t.Fatal("empty deck must render the no relevant cards placeholder")
		}
		if strings.Contains(prompt, footer) {
			t.Fatal("empty deck must not render a deck footer")
		}
	})

	t.Run("all_inlined_has_no_deck_footer", func(t *testing.T) {
		t.Parallel()
		inlined := []cards.InlinedCard{{
			ID:       "card-inline-01",
			Type:     cards.CardTypeRule,
			Title:    "Inline only",
			BodyFull: "Inline body",
			Score:    1.2,
		}}
		deck := []cards.DeckCard{{
			ID:    "card-inline-01",
			Type:  cards.CardTypeRule,
			Title: "Inline only",
			Score: 1.2,
		}}
		prompt := worker.AssemblePrompt(worker.PromptParams{
			BeadID: "bead-c05",
			Title:  "All inline cards test",
			Cards: cards.RelevantCards{
				Deck:    deck,
				Inlined: inlined,
			},
		})

		if strings.Contains(prompt, footer) {
			t.Fatal("all-inlined cards must not render a deck footer")
		}
	})

	t.Run("deck_only_footer_uses_registered_cards_show_command", func(t *testing.T) {
		t.Parallel()
		prompt := worker.AssemblePrompt(worker.PromptParams{
			BeadID: "bead-c06",
			Title:  "Deck footer test",
			Cards: cards.RelevantCards{
				Deck: []cards.DeckCard{{
					ID:    "card-deck-01",
					Type:  cards.CardTypePattern,
					Title: "Deck only",
					Score: 1.1,
				}},
			},
		})

		if !strings.Contains(prompt, footer) {
			t.Fatalf("deck-only cards must render footer %q", footer)
		}
		if strings.Contains(prompt, "oro card show <id>") {
			t.Fatal("deck footer must not use stale singular card command")
		}
	})
}
