package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/cards"
	"oro/pkg/config"
	"oro/pkg/ops"
)

var gradeAutoApplyConfidence = []float64{0.80, 0.90, 0.95}

// drainGradeProposals drives each outstanding proposal through its complete
// escalation ladder. A failed card is logged by the caller while later cards
// still receive a grading attempt during the same sweep.
func (d *Dispatcher) drainGradeProposals(ctx context.Context) error {
	if !d.cfg.GradeGateEnabled || d.cardStore == nil {
		return nil
	}
	proposals, err := d.cardStore.ListProposed(ctx)
	if err != nil {
		return fmt.Errorf("list proposed cards: %w", err)
	}
	for _, proposal := range proposals {
		if err := d.driveGradeProposal(ctx, proposal); err != nil {
			slog.WarnContext(ctx, "grade proposal failed", "card_id", proposal.ID, "err", err)
		}
	}
	return nil
}

func (d *Dispatcher) driveGradeProposal(ctx context.Context, proposal cards.Card) error {
	if d.ops == nil {
		return fmt.Errorf("grade proposal %s: ops spawner unavailable", proposal.ID)
	}
	rungs := config.GradeLadder(*config.DefaultAgentConfig())
	for rung, routing := range rungs {
		result := <-d.ops.Grade(ctx, ops.GradeOpts{
			Card:      proposal,
			Role:      "grade",
			Model:     routing.Model,
			Reasoning: routing.Reasoning,
		})
		if result.Err != nil {
			return fmt.Errorf("grade proposal %s at rung %d: %w", proposal.ID, rung+1, result.Err)
		}
		outcome, terminal := gradeOutcomeForRung(proposal, result, rung, len(rungs))
		if !terminal {
			continue
		}
		if err := d.cardStore.ResolveProposal(ctx, proposal.ID, outcome); err != nil {
			return fmt.Errorf("resolve proposal %s: %w", proposal.ID, err)
		}
		return nil
	}
	return fmt.Errorf("grade proposal %s exhausted without a terminal outcome", proposal.ID)
}

func gradeOutcomeForRung(proposal cards.Card, result ops.Result, rung, rungCount int) (cards.GradeOutcome, bool) {
	verdict := cards.GradeVerdictValue(result.GradeVerdict)
	confidence := result.GradeConfidence
	if verdict == cards.GradeVerdictIncorrect {
		return cards.GradeOutcome{
			Action:     cards.GradeActionRejectAndRetire,
			GradeState: cards.GradeStateRejected,
			Verdict:    verdict,
			Confidence: confidence,
		}, true
	}
	if verdict == cards.GradeVerdictCorrect && confidence >= gradeConfidenceAt(rung) {
		return cards.GradeOutcome{
			Action:     cards.GradeActionApply,
			GradeState: cards.GradeStateApplied,
			Verdict:    verdict,
			Confidence: confidence,
		}, true
	}
	if isSubjectiveProposal(proposal) && rung+1 < rungCount {
		return cards.GradeOutcome{}, false
	}
	return cards.GradeOutcome{
		Action:     cards.GradeActionRejectAndRetire,
		GradeState: cards.GradeStateRejected,
		Verdict:    verdict,
		Confidence: confidence,
		Reason:     "grade_escalation_exhausted",
	}, true
}

func gradeConfidenceAt(rung int) float64 {
	if rung >= 0 && rung < len(gradeAutoApplyConfidence) {
		return gradeAutoApplyConfidence[rung]
	}
	return 1
}

func isSubjectiveProposal(proposal cards.Card) bool {
	return proposal.Type == cards.CardTypeDecision || proposal.Type == cards.CardTypeTaste
}

func promotionVerdictFromCloseReason(reason string) string {
	lower := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case strings.HasPrefix(lower, "merged:"):
		return "pass"
	case strings.Contains(lower, "branch already merged"):
		return "pass"
	case strings.Contains(lower, "review failed"):
		return "fail"
	case strings.Contains(lower, "review rejected"):
		return "fail"
	default:
		return ""
	}
}

func (d *Dispatcher) runLearningPromotion(ctx context.Context, beadID, verdict string) error {
	if d.cardStore == nil {
		return nil
	}
	pending, err := d.cardStore.PendingLearnings(ctx, beadID)
	if err != nil {
		return fmt.Errorf("pending learnings for %s: %w", beadID, err)
	}
	if len(pending) == 0 {
		return nil
	}
	existing, err := d.cardStore.List(ctx, cards.ListQuery{IncludeRetired: false})
	if err != nil {
		return fmt.Errorf("list cards for promotion: %w", err)
	}
	summaries := cardSummaries(existing)
	for _, learning := range pending {
		decision := cards.DecidePromotion(learning.Candidate, verdict, summaries)
		if err := d.applyLearningPromotionDecision(ctx, beadID, learning.ID, decision); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) applyLearningPromotionDecision(ctx context.Context, beadID string, learningID int64, decision cards.PromotionDecision) error {
	switch decision.Action {
	case cards.PromotionActionPromote:
		cardID, err := d.promoteLearningCard(ctx, learningID)
		if err != nil {
			return fmt.Errorf("promote learning %d: %w", learningID, err)
		}
		return d.appendLearningPromotionEvent(ctx, beadID, "learning_promoted", learningID, decision, cardID)
	case cards.PromotionActionReject:
		if err := d.cardStore.RejectLearning(ctx, learningID, decision.Reason); err != nil {
			return fmt.Errorf("reject learning %d: %w", learningID, err)
		}
		return nil
	case cards.PromotionActionDefer:
		if err := d.cardStore.DeferToReviewQueue(ctx, learningID, decision.Reason); err != nil {
			return fmt.Errorf("defer learning %d to review queue: %w", learningID, err)
		}
		return d.appendLearningPromotionEvent(ctx, beadID, "learning_deferred_to_review", learningID, decision, "")
	default:
		return fmt.Errorf("unknown promotion action %q for learning %d", decision.Action, learningID)
	}
}

func (d *Dispatcher) promoteLearningCard(ctx context.Context, learningID int64) (string, error) {
	if d.cfg.GradeGateEnabled {
		cardID, err := d.cardStore.PromoteLearningAsProposal(ctx, learningID)
		if err != nil {
			return "", fmt.Errorf("promote learning as proposal: %w", err)
		}
		return cardID, nil
	}
	cardID, err := d.cardStore.PromoteLearning(ctx, learningID)
	if err != nil {
		return "", fmt.Errorf("promote learning directly: %w", err)
	}
	return cardID, nil
}

func (d *Dispatcher) appendLearningPromotionEvent(ctx context.Context, beadID, event string, learningID int64, decision cards.PromotionDecision, cardID string) error {
	payload, err := json.Marshal(map[string]any{
		"learning_id": learningID,
		"card_id":     cardID,
		"reason":      decision.Reason,
		"confidence":  decision.Confidence,
	})
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", event, err)
	}
	if err := d.beads.AppendJourney(ctx, beadID, beadstore.JourneyEvent{
		Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		Actor:   "dispatcher",
		Event:   event,
		Payload: string(payload),
	}); err != nil {
		return fmt.Errorf("append %s journey event: %w", event, err)
	}
	return nil
}

func cardSummaries(in []cards.Card) []cards.CardSummary {
	out := make([]cards.CardSummary, 0, len(in))
	for _, card := range in {
		out = append(out, cards.CardSummary{
			ID:          card.ID,
			Type:        card.Type,
			Title:       card.Title,
			BodySummary: card.BodySummary,
			BodyFull:    card.BodyFull,
			Score:       card.Score,
			Tags:        card.Tags,
		})
	}
	return out
}
