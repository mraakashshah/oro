package cards_test

import (
	"context"
	"database/sql"
	"math"
	"strconv"
	"strings"
	"testing"

	"oro/pkg/cards"
	"oro/pkg/dbutil"
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
			name: "pattern near duplicate rejects",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypePattern),
				Title:      "Separate stdout from stderr",
				BodyFull:   "Capture stdout separately from stderr when parsing command JSON output.",
				Confidence: 0.9,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "pass",
			existing:   []cards.CardSummary{nearDuplicate},
			wantAction: cards.PromotionActionReject,
			wantReason: "near_duplicate_card_dup",
		},
		{
			name: "taste pass promotes",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypeTaste),
				Title:      "Prefer terse prompts",
				BodyFull:   "Prefer terse prompts for worker instructions.",
				Confidence: 0.95,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionPromote,
		},
		{
			name: "decision pass promotes",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypeDecision),
				Title:      "Keep cards pure",
				BodyFull:   "Keep promotion decision logic pure and wire storage later.",
				Confidence: 0.95,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionPromote,
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
			name: "fact without confirmed flag rejects",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypeFact),
				Title:      "SQLite WAL enabled",
				BodyFull:   "SQLite WAL mode is enabled in production.",
				Confidence: 0.95,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionReject,
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
			name: "unknown verdict rejects",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypePattern),
				Title:      "Unknown verdict",
				BodyFull:   "Unknown verdicts are not terminal promotion contexts.",
				Confidence: 0.95,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "needs_more",
			wantAction: cards.PromotionActionReject,
		},
		{
			name: "below confidence threshold promotes",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypePattern),
				Title:      "Low confidence",
				BodyFull:   "Low confidence candidates need review.",
				Confidence: 0.69,
				Evidence:   []string{"pkg/cards/promotion_test.go"},
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionPromote,
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
			if strings.Contains(got.Reason, "near_duplicate") && got.Action == cards.PromotionActionPromote {
				t.Fatalf("near duplicate promoted: %+v", got)
			}
		})
	}
}

func TestDecidePromotionNoHumanQueue(t *testing.T) {
	nearDuplicate := cards.CardSummary{
		ID:          "duplicate",
		Type:        cards.CardTypePattern,
		Title:       "Capture output separately",
		BodySummary: "Capture stdout separately from stderr when parsing command JSON output.",
		BodyFull:    "Capture stdout separately from stderr when parsing command JSON output.",
	}

	tests := []struct {
		name       string
		candidate  cards.CardCandidate
		verdict    string
		existing   []cards.CardSummary
		wantAction cards.PromotionAction
	}{
		{
			name: "taste promotes as proposal",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypeTaste),
				Title:      "Prefer concise prompts",
				Confidence: 0.9,
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionPromote,
		},
		{
			name: "decision promotes as proposal",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypeDecision),
				Title:      "Keep promotion logic pure",
				Confidence: 0.9,
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionPromote,
		},
		{
			name: "below threshold pattern promotes as proposal",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypePattern),
				Title:      "Low confidence pattern",
				Confidence: 0.2,
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionPromote,
		},
		{
			name: "near duplicate rejects",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypePattern),
				Title:      "Capture output separately",
				BodyFull:   "Capture stdout separately from stderr when parsing command JSON output.",
				Confidence: 0.9,
			},
			verdict:    "pass",
			existing:   []cards.CardSummary{nearDuplicate},
			wantAction: cards.PromotionActionReject,
		},
		{
			name: "unconfirmed fact rejects",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypeFact),
				Title:      "Unconfirmed fact",
				Confidence: 0.9,
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionReject,
		},
		{
			name: "unknown verdict rejects",
			candidate: cards.CardCandidate{
				Type:       string(cards.CardTypePattern),
				Title:      "Unknown verdict",
				Confidence: 0.9,
			},
			verdict:    "",
			wantAction: cards.PromotionActionReject,
		},
		{
			name: "invalid type rejects",
			candidate: cards.CardCandidate{
				Type:       "unsupported",
				Title:      "Unsupported type",
				Confidence: 0.9,
			},
			verdict:    "pass",
			wantAction: cards.PromotionActionReject,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := cards.DecidePromotion(tt.candidate, tt.verdict, tt.existing)
			if decision.Action != tt.wantAction {
				t.Fatalf("Action = %q, want %q; decision=%+v", decision.Action, tt.wantAction, decision)
			}
			if decision.Action == cards.PromotionActionDefer {
				t.Fatalf("decision unexpectedly queues human review: %+v", decision)
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

func TestCalibration_ReportsRates(t *testing.T) {
	ctx := context.Background()
	store, db := newCalibrationStore(t)

	seedResolvedGrade(ctx, t, store, db, "pattern", "task", "correct")
	seedResolvedGrade(ctx, t, store, db, "pattern", "task", "correct")
	seedResolvedGrade(ctx, t, store, db, "pattern", "task", "correct")
	seedResolvedGrade(ctx, t, store, db, "pattern", "task", "incorrect")
	seedResolvedGrade(ctx, t, store, db, "rule", "bug", "incorrect")
	seedResolvedGrade(ctx, t, store, db, "rule", "bug", "incorrect")
	seedResolvedGrade(ctx, t, store, db, "rule", "bug", "partial")

	scorecard, err := store.Calibration(ctx)
	if err != nil {
		t.Fatalf("Calibration: %v", err)
	}
	if scorecard.Skipped {
		t.Fatalf("Calibration skipped with %d resolved verdicts", scorecard.Resolved)
	}
	if scorecard.Resolved != 7 {
		t.Fatalf("Resolved = %d, want 7", scorecard.Resolved)
	}

	patternTask := findBucket(t, scorecard, cards.CardTypePattern, "task")
	if patternTask.Resolved != 4 || patternTask.Correct != 3 {
		t.Fatalf("pattern/task bucket = %+v, want 3/4 correct", patternTask)
	}
	assertFloat(t, patternTask.Accuracy, 0.75)
	assertFloat(t, patternTask.Brier, 0.1875)

	ruleBug := findBucket(t, scorecard, cards.CardTypeRule, "bug")
	if ruleBug.Resolved != 3 || ruleBug.Correct != 0 {
		t.Fatalf("rule/bug bucket = %+v, want 0/3 correct", ruleBug)
	}
	assertFloat(t, ruleBug.Accuracy, 0)
	assertFloat(t, ruleBug.Brier, 0)
	if !containsString(scorecard.ActiveBiasTags, "low_accuracy:rule:bug") {
		t.Fatalf("ActiveBiasTags = %v, want low_accuracy:rule:bug", scorecard.ActiveBiasTags)
	}

	coldStore, _ := newCalibrationStore(t)
	cold, err := coldStore.Calibration(ctx)
	if err != nil {
		t.Fatalf("cold Calibration: %v", err)
	}
	if !cold.Skipped || cold.Resolved != 0 || len(cold.Buckets) != 0 {
		t.Fatalf("cold scorecard = %+v, want skipped empty scorecard", cold)
	}
}

func newCalibrationStore(t *testing.T) (*cards.SQLiteCardStore, *sql.DB) {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), `
		CREATE TABLE beads (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL
		)`); err != nil {
		t.Fatalf("create beads table: %v", err)
	}
	store, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store, db
}

func seedResolvedGrade(
	ctx context.Context,
	t *testing.T,
	store *cards.SQLiteCardStore,
	db *sql.DB,
	cardType string,
	beadType string,
	verdict string,
) {
	t.Helper()
	var existing int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cards`).Scan(&existing); err != nil {
		t.Fatalf("count cards: %v", err)
	}
	beadID := "bead-" + cardType + "-" + beadType + "-" + verdict + "-" +
		strings.ReplaceAll(t.Name(), "/", "-") + "-" + strconv.Itoa(existing)
	if _, err := db.ExecContext(ctx, `INSERT INTO beads (id, type) VALUES (?, ?)`, beadID, beadType); err != nil {
		t.Fatalf("insert bead: %v", err)
	}
	card, err := store.Create(ctx, cards.CardCreateParams{
		Type:        cards.CardType(cardType),
		Title:       cardType + " " + beadType + " " + verdict,
		BodySummary: "calibration fixture",
		BodyFull:    "calibration fixture",
		GradeState:  "applied",
	})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE cards
		   SET grade_verdict = ?, grade_confidence = 0.8
		 WHERE id = ?`, verdict, card.ID); err != nil {
		t.Fatalf("update grade: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO card_events (card_id, ts, bead_id, actor, kind)
		VALUES (?, '2026-01-01T00:00:00Z', ?, 'test', 'grade_resolved')`, card.ID, beadID); err != nil {
		t.Fatalf("insert grade event: %v", err)
	}
}

func findBucket(t *testing.T, scorecard cards.Scorecard, cardType cards.CardType, beadType string) cards.ScorecardBucket {
	t.Helper()
	for _, bucket := range scorecard.Buckets {
		if bucket.CardType == cardType && bucket.BeadType == beadType {
			return bucket
		}
	}
	t.Fatalf("missing bucket %s/%s in %+v", cardType, beadType, scorecard.Buckets)
	return cards.ScorecardBucket{}
}

func assertFloat(t *testing.T, got float64, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.000001 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
