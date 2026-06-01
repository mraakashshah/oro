package cards_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"oro/pkg/cards"
)

func TestPromotionDecision(t *testing.T) {
	highScoreExisting := cards.CardSummary{
		ID:          "high",
		Type:        cards.CardTypeRule,
		Title:       "Use transactions for promotion",
		BodySummary: "Promotion must create the card and resolve the learning in one transaction.",
		BodyFull:    "Promotion must create the card and resolve the learning in one transaction.",
		Score:       4.2,
	}
	nearDuplicate := cards.CardSummary{
		ID:          "card-dup",
		Type:        cards.CardTypePattern,
		Title:       "Capture stdout separately",
		BodySummary: "Capture stdout separately from stderr when parsing command JSON output.",
		BodyFull:    "Capture stdout separately from stderr when parsing command JSON output.",
		Score:       1.0,
	}

	tests := []struct {
		name       string
		candidate  cards.CardCandidate
		verdict    string
		existing   []cards.CardSummary
		wantAction cards.PromotionAction
		wantReason string
	}{
		{
			name: "rule pass confidence threshold no duplicate promotes",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypeRule),
				Title:      "Wrap errors with context",
				BodyFull:   "When preserving an error cause, wrap it with fmt.Errorf and percent-w.",
				Confidence: 0.8,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionPromote,
		},
		{
			name: "pattern near duplicate defers",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypePattern),
				Title:      "Separate stdout from stderr",
				BodyFull:   "Capture stdout separately from stderr when parsing command JSON output.",
				Confidence: 0.9,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "pass",
			existing:   []cards.CardSummary{nearDuplicate},
			wantAction: cards.PromotionActionDefer,
			wantReason: "near_duplicate_card_dup",
		},
		{
			name: "taste pass defers",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypeTaste),
				Title:      "Prefer terse prompts",
				BodyFull:   "Prefer terse prompts for worker instructions.",
				Confidence: 0.95,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionDefer,
		},
		{
			name: "decision pass defers",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypeDecision),
				Title:      "Keep cards pure",
				BodyFull:   "Keep promotion decision logic pure and wire storage later.",
				Confidence: 0.95,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionDefer,
		},
		{
			name: "fail rejects ops review failed",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypePattern),
				Title:      "Rejected candidate",
				BodyFull:   "Any failing ops review rejects pending learnings.",
				Confidence: 0.95,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "fail",
			wantAction: cards.PromotionActionReject,
			wantReason: "ops_review_failed",
		},
		{
			name: "contradicts high score without rationale rejects",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypePattern),
				Title:      "Promotion can be non-transactional",
				BodyFull:   "Promotion can create the card and resolve the learning in separate non-transactional steps.",
				Confidence: 0.9,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "pass",
			existing:   []cards.CardSummary{highScoreExisting},
			wantAction: cards.PromotionActionReject,
			wantReason: "contradicts_card_high_without_rationale",
		},
		{
			name: "contradicts high score with rationale promotes",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypePattern),
				Title:      "Promotion can be non-transactional",
				BodyFull:   "Promotion can create the card and resolve the learning in separate non-transactional steps. Rationale: prior card was nacked by reviewer.",
				Confidence: 0.9,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "pass",
			existing:   []cards.CardSummary{highScoreExisting},
			wantAction: cards.PromotionActionPromote,
		},
		{
			name: "fact without confirmed flag defers",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypeFact),
				Title:      "SQLite WAL enabled",
				BodyFull:   "SQLite WAL mode is enabled in production.",
				Confidence: 0.95,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionDefer,
		},
		{
			name: "fact with confirmed flag promotes",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypeFact),
				Title:      "SQLite WAL enabled",
				BodyFull:   "SQLite WAL mode is enabled in production.",
				Confidence: 0.95,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
				Confirmed:  true,
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionPromote,
		},
		{
			name: "empty existing means no contradiction",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypePattern),
				Title:      "Promotion can be non-transactional",
				BodyFull:   "Promotion can create the card and resolve the learning in separate non-transactional steps.",
				Confidence: 0.9,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionPromote,
		},
		{
			name: "unknown verdict defers",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypePattern),
				Title:      "Unknown verdict",
				BodyFull:   "Unknown verdicts are not terminal promotion contexts.",
				Confidence: 0.95,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "needs_more",
			wantAction: cards.PromotionActionDefer,
		},
		{
			name: "below confidence threshold defers",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypePattern),
				Title:      "Low confidence",
				BodyFull:   "Low confidence candidates need review.",
				Confidence: 0.69,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionDefer,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cards.DecidePromotion(tt.candidate, tt.verdict, tt.existing)
			if got.Action != tt.wantAction {
				t.Fatalf("Action = %q, want %q; decision=%+v", got.Action, tt.wantAction, got)
			}
			if tt.wantReason != "" && got.Reason != tt.wantReason {
				t.Fatalf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if got.Action == cards.PromotionActionPromote && got.Confidence < cards.PromotionConfidenceThreshold {
				t.Fatalf("promoted with Confidence = %v, below threshold %v", got.Confidence, cards.PromotionConfidenceThreshold)
			}
			if strings.Contains(got.Reason, "near_duplicate") && got.Action == cards.PromotionActionPromote {
				t.Fatalf("near duplicate promoted: %+v", got)
			}
		})
	}
}

func TestPromotedLearning_EntersProposalQueue(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStoreWithBeads(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO beads (id) VALUES (?)`, "bead-proposal"); err != nil {
		t.Fatalf("insert bead: %v", err)
	}
	candidate := cards.CardCandidate{
		Type:        string(cards.CardTypePattern),
		Title:       "Promoted learnings enter proposals",
		BodySummary: "Grade-gated promotions are queued as proposed cards.",
		BodyFull:    "When the grade gate is enabled, DecidePromotion promote results should create proposed cards instead of direct active cards.",
		Confidence:  0.91,
		Evidence:    []string{"pkg/cards/promotion_test.go:TestPromotedLearning_EntersProposalQueue"},
		Tags:        []string{"cards", "grade-gate"},
	}
	decision := cards.DecidePromotion(candidate, "pass", nil)
	if decision.Action != cards.PromotionActionPromote {
		t.Fatalf("promotion action = %q, want promote: %+v", decision.Action, decision)
	}
	learningID, err := store.AppendLearningPending(ctx, "bead-proposal", candidate)
	if err != nil {
		t.Fatalf("AppendLearningPending: %v", err)
	}

	cardID, err := store.PromoteLearningAsProposal(ctx, learningID)
	if err != nil {
		t.Fatalf("PromoteLearningAsProposal: %v", err)
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

	var gradeState, proposalHash sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT grade_state, proposal_hash FROM cards WHERE id = ?`, cardID,
	).Scan(&gradeState, &proposalHash); err != nil {
		t.Fatalf("query promoted proposal: %v", err)
	}
	if !gradeState.Valid || gradeState.String != string(cards.GradeStateProposed) {
		t.Fatalf("grade_state = %q valid=%v, want proposed", gradeState.String, gradeState.Valid)
	}
	if !proposalHash.Valid || strings.TrimSpace(proposalHash.String) == "" {
		t.Fatalf("proposal_hash = %q valid=%v, want non-empty", proposalHash.String, proposalHash.Valid)
	}

	card, err := store.Show(ctx, cardID)
	if err != nil {
		t.Fatalf("Show proposed card: %v", err)
	}
	if card.Title != candidate.Title || card.EmergedFrom == nil || *card.EmergedFrom != "bead-proposal" {
		t.Fatalf("proposed card = %+v, want candidate title and bead provenance", card)
	}
}
