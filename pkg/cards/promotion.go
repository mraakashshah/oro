package cards

import (
	"fmt"
	"strings"
	"unicode"
)

// PromotionConfidenceThreshold is the minimum confidence for auto-promoting confirmed facts.
const PromotionConfidenceThreshold = 0.7

const (
	highScoreContradictionThreshold = 4.0
	nearDuplicateJaccardThreshold   = 0.75
)

// PromotionAction is the terminal action selected for a pending learning.
type PromotionAction string

// Promotion action constants.
const (
	PromotionActionPromote PromotionAction = "promote"
	PromotionActionDefer   PromotionAction = "defer"
	PromotionActionReject  PromotionAction = "reject"
)

// PromotionDecision is the pure decision result for a pending card candidate.
type PromotionDecision struct {
	Action     PromotionAction
	Reason     string
	Confidence float64
}

// GradeVerdictValue is the closed enum of judge verdicts for proposal grading.
type GradeVerdictValue string

// Grade verdict constants.
const (
	GradeVerdictCorrect      GradeVerdictValue = "correct"
	GradeVerdictIncorrect    GradeVerdictValue = "incorrect"
	GradeVerdictPartial      GradeVerdictValue = "partial"
	GradeVerdictUnresolvable GradeVerdictValue = "unresolvable"
)

// GradeVerdict is a single judge's calibrated proposal grade.
type GradeVerdict struct {
	Verdict    GradeVerdictValue
	Confidence float64
}

// GateConfig controls the proposal grade confidence gate.
type GateConfig struct {
	AutoApplyConfidence   []float64
	EnsembleMinConfidence float64
}

// GradeAction is the storage action implied by the grade gate.
type GradeAction string

// Grade action constants.
const (
	GradeActionApply           GradeAction = "apply"
	GradeActionRejectAndRetire GradeAction = "reject_and_retire"
	GradeActionQueue           GradeAction = "queue"
)

// GradeState is the card grade_state value selected by the gate.
type GradeState string

// Grade state constants.
const (
	GradeStateApplied  GradeState = "applied"
	GradeStateRejected GradeState = "rejected"
	GradeStateProposed GradeState = "proposed"
)

// GradeOutcome is the pure decision result for a graded proposal.
type GradeOutcome struct {
	Action     GradeAction
	GradeState GradeState
	Verdict    GradeVerdictValue
	Confidence float64
	Reason     string
}

// DecidePromotion applies the conservative §5.7 promotion rules without side effects.
func DecidePromotion(c CardCandidate, verdict string, existing []CardSummary) PromotionDecision {
	confidence := clampConfidence(c.Confidence)
	switch verdict {
	case "fail":
		return rejectPromotion(confidence, "ops_review_failed")
	case "pass":
	default:
		return rejectPromotion(confidence, "unknown_verdict")
	}

	if contradictionID := contradictingHighScoreCard(c, existing); contradictionID != "" && !hasContradictionRationale(c) {
		return rejectPromotion(confidence, fmt.Sprintf("contradicts_card_%s_without_rationale", reasonID(contradictionID)))
	}
	if duplicateID := nearDuplicateCard(c, existing); duplicateID != "" {
		return rejectPromotion(confidence, fmt.Sprintf("near_duplicate_%s", reasonID(duplicateID)))
	}

	switch CardType(c.Type) {
	case CardTypeRule, CardTypePattern:
		return PromotionDecision{Action: PromotionActionPromote, Confidence: confidence}
	case CardTypeTaste, CardTypeDecision:
		return PromotionDecision{Action: PromotionActionPromote, Confidence: confidence}
	case CardTypeFact:
		if c.Confirmed && confidence >= PromotionConfidenceThreshold {
			return PromotionDecision{Action: PromotionActionPromote, Confidence: confidence}
		}
		return rejectPromotion(confidence, "fact_unconfirmed")
	default:
		return rejectPromotion(confidence, "invalid_card_type")
	}
}

func gradeGate(verdicts []GradeVerdict, cfg GateConfig, rungs ...int) GradeOutcome {
	if len(verdicts) == 0 {
		return queueGrade(0, "", "no_verdicts")
	}

	if len(verdicts) == 1 {
		return singleGradeGate(verdicts[0], cfg, gradeRung(rungs))
	}
	return ensembleGradeGate(verdicts, cfg)
}

func singleGradeGate(verdict GradeVerdict, cfg GateConfig, rung int) GradeOutcome {
	confidence := clampConfidence(verdict.Confidence)
	switch verdict.Verdict {
	case GradeVerdictCorrect:
		if threshold, ok := autoApplyConfidence(cfg, rung); ok && confidence >= threshold {
			return applyGrade(confidence, GradeVerdictCorrect)
		}
		return queueGrade(confidence, GradeVerdictCorrect, "ensemble_required")
	case GradeVerdictIncorrect:
		return rejectAndRetireGrade(confidence, GradeVerdictIncorrect)
	case GradeVerdictPartial:
		return queueGrade(confidence, GradeVerdictPartial, "partial")
	case GradeVerdictUnresolvable:
		return queueGrade(confidence, GradeVerdictUnresolvable, "unresolvable")
	default:
		return queueGrade(confidence, verdict.Verdict, "unknown_verdict")
	}
}

func gradeRung(rungs []int) int {
	if len(rungs) == 0 {
		return 0
	}
	return rungs[0]
}

func autoApplyConfidence(cfg GateConfig, rung int) (float64, bool) {
	if rung < 0 || rung >= len(cfg.AutoApplyConfidence) {
		return 0, false
	}
	return clampConfidence(cfg.AutoApplyConfidence[rung]), true
}

func ensembleGradeGate(verdicts []GradeVerdict, cfg GateConfig) GradeOutcome {
	first := verdicts[0].Verdict
	minConfidence := clampConfidence(verdicts[0].Confidence)
	for _, verdict := range verdicts {
		confidence := clampConfidence(verdict.Confidence)
		if confidence < minConfidence {
			minConfidence = confidence
		}
		if verdict.Verdict == GradeVerdictUnresolvable {
			return queueGrade(minConfidence, GradeVerdictUnresolvable, "ensemble_unresolvable")
		}
		if verdict.Verdict != first {
			return queueGrade(minConfidence, first, "ensemble_not_unanimous")
		}
	}

	switch first {
	case GradeVerdictCorrect:
		if minConfidence >= clampConfidence(cfg.EnsembleMinConfidence) {
			return applyGrade(minConfidence, GradeVerdictCorrect)
		}
		return queueGrade(minConfidence, GradeVerdictCorrect, "ensemble_confidence_below_threshold")
	case GradeVerdictIncorrect:
		return rejectAndRetireGrade(minConfidence, GradeVerdictIncorrect)
	case GradeVerdictPartial:
		return queueGrade(minConfidence, GradeVerdictPartial, "partial")
	default:
		return queueGrade(minConfidence, first, "unknown_verdict")
	}
}

func applyGrade(confidence float64, verdict GradeVerdictValue) GradeOutcome {
	return GradeOutcome{
		Action:     GradeActionApply,
		GradeState: GradeStateApplied,
		Verdict:    verdict,
		Confidence: confidence,
	}
}

func rejectAndRetireGrade(confidence float64, verdict GradeVerdictValue) GradeOutcome {
	return GradeOutcome{
		Action:     GradeActionRejectAndRetire,
		GradeState: GradeStateRejected,
		Verdict:    verdict,
		Confidence: confidence,
	}
}

func queueGrade(confidence float64, verdict GradeVerdictValue, reason string) GradeOutcome {
	return GradeOutcome{
		Action:     GradeActionQueue,
		GradeState: GradeStateProposed,
		Verdict:    verdict,
		Confidence: confidence,
		Reason:     reason,
	}
}

func rejectPromotion(confidence float64, reason string) PromotionDecision {
	return PromotionDecision{Action: PromotionActionReject, Reason: reason, Confidence: confidence}
}

func deferPromotion(confidence float64, reason string) PromotionDecision {
	return PromotionDecision{Action: PromotionActionDefer, Reason: reason, Confidence: confidence}
}

func contradictingHighScoreCard(c CardCandidate, existing []CardSummary) string {
	for _, card := range existing {
		if card.Score < highScoreContradictionThreshold {
			continue
		}
		if sharesSubject(c, card) && hasOpposingLanguage(c, card) {
			return card.ID
		}
	}
	return ""
}

func nearDuplicateCard(c CardCandidate, existing []CardSummary) string {
	candidateWords := contentWordSet(candidateText(c))
	for _, card := range existing {
		if jaccard(candidateWords, contentWordSet(summaryText(card))) >= nearDuplicateJaccardThreshold {
			return card.ID
		}
	}
	return ""
}

func hasContradictionRationale(c CardCandidate) bool {
	text := strings.ToLower(candidateText(c))
	return strings.Contains(text, "rationale:") ||
		strings.Contains(text, "ack rationale") ||
		strings.Contains(text, "nack rationale") ||
		strings.Contains(text, "nacked") ||
		strings.Contains(text, "acked")
}

func sharesSubject(c CardCandidate, card CardSummary) bool {
	return jaccard(contentWordSet(candidateText(c)), contentWordSet(summaryText(card))) >= 0.25
}

func hasOpposingLanguage(c CardCandidate, card CardSummary) bool {
	candidate := strings.ToLower(candidateText(c))
	existing := strings.ToLower(summaryText(card))
	return (strings.Contains(candidate, "non-") && !strings.Contains(existing, "non-")) ||
		(strings.Contains(candidate, "not ") && !strings.Contains(existing, "not ")) ||
		(strings.Contains(candidate, "separate") && strings.Contains(existing, " one ")) ||
		(strings.Contains(candidate, "separate") && strings.Contains(existing, "transaction"))
}

func candidateText(c CardCandidate) string {
	return c.Title + " " + c.BodySummary + " " + c.BodyFull
}

func summaryText(card CardSummary) string {
	return card.Title + " " + card.BodySummary + " " + card.BodyFull
}

func jaccard(left, right map[string]bool) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	intersection := 0
	for word := range left {
		if right[word] {
			intersection++
		}
	}
	union := len(left) + len(right) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func contentWordSet(text string) map[string]bool {
	words := make(map[string]bool)
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		word = lemmatizeWord(word)
		if len(word) < 3 || isStopword(word) {
			continue
		}
		words[word] = true
	}
	return words
}

func reasonID(id string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return r
		}
		return '_'
	}, id)
}

func lemmatizeWord(word string) string {
	switch {
	case len(word) >= 5 && strings.HasSuffix(word, "es"):
		return strings.TrimSuffix(word, "es")
	case len(word) >= 3 && strings.HasSuffix(word, "s"):
		return strings.TrimSuffix(word, "s")
	default:
		return word
	}
}

func isStopword(word string) bool {
	switch word {
	case "and", "are", "but", "can", "for", "from", "into", "must", "not", "one", "the", "then", "this", "when", "with":
		return true
	default:
		return false
	}
}
