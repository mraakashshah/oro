package cards

import (
	"fmt"
	"strings"
	"unicode"
)

// PromotionConfidenceThreshold is the minimum candidate confidence for auto-promotion.
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

// DecidePromotion applies the conservative §5.7 promotion rules without side effects.
//
//oro:testonly — production wiring deferred to bead-close promotion flow.
func DecidePromotion(c CardCandidate, verdict string, existing []CardSummary) PromotionDecision {
	confidence := clampConfidence(c.Confidence)
	switch verdict {
	case "fail":
		return rejectPromotion(confidence, "ops_review_failed")
	case "pass":
	default:
		return deferPromotion(confidence, "unknown_verdict")
	}

	if contradictionID := contradictingHighScoreCard(c, existing); contradictionID != "" && !hasContradictionRationale(c) {
		return rejectPromotion(confidence, fmt.Sprintf("contradicts_card_%s_without_rationale", reasonID(contradictionID)))
	}
	if duplicateID := nearDuplicateCard(c, existing); duplicateID != "" {
		return deferPromotion(confidence, fmt.Sprintf("near_duplicate_%s", reasonID(duplicateID)))
	}

	switch CardType(c.Type) {
	case CardTypeRule, CardTypePattern:
		if confidence >= PromotionConfidenceThreshold {
			return PromotionDecision{Action: PromotionActionPromote, Confidence: confidence}
		}
		return deferPromotion(confidence, "confidence_below_threshold")
	case CardTypeTaste, CardTypeDecision:
		return deferPromotion(confidence, "human_review_required")
	case CardTypeFact:
		if c.Confirmed && confidence >= PromotionConfidenceThreshold {
			return PromotionDecision{Action: PromotionActionPromote, Confidence: confidence}
		}
		return deferPromotion(confidence, "fact_unconfirmed")
	default:
		return deferPromotion(confidence, "invalid_card_type")
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
