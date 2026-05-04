package cards

import (
	"math"
	"time"
)

// Scoring constants controlling score bounds and retrieval thresholds.
const (
	ScoreCap         = 5.0  // maximum raw score; prevents runaway accumulation
	ScoreFloor       = -2.0 // minimum raw score; further nacks are no-ops
	AutoRetireThresh = -1.0 // score at which a card is auto-retired
	DefaultThreshold = 0.1  // minimum effective_score for default Relevant results
)

// halfLifeDays holds the decay half-life in days per card type.
var halfLifeDays = map[CardType]float64{ //nolint:gochecknoglobals // static config table, read-only after init
	CardTypeRule:     365,
	CardTypeTaste:    180,
	CardTypePattern:  180,
	CardTypeDecision: 730,
	CardTypeFact:     90,
}

// suppressionWindowDays holds the contradiction suppression window in days per card type.
var suppressionWindowDays = map[CardType]float64{ //nolint:gochecknoglobals // static config table, read-only after init
	CardTypeRule:     14,
	CardTypeTaste:    14,
	CardTypePattern:  14,
	CardTypeDecision: 30,
	CardTypeFact:     7,
}

// DecayMultiplier returns exp(-ln(2)*elapsed/half_life) for a card type.
// A freshly touched card returns ~1.0; a card at exactly one half-life returns 0.5.
func DecayMultiplier(cardType CardType, decayAnchor, now time.Time) float64 {
	days, ok := halfLifeDays[cardType]
	if !ok {
		days = 180 // safe default
	}
	halfLifeSecs := days * 86400
	elapsed := now.Sub(decayAnchor).Seconds()
	return math.Exp(-math.Ln2 * elapsed / halfLifeSecs)
}

// SuppressionMultiplier returns 1.0 when the card is not suppressed, 0.0 when it is.
// A card is suppressed if last_contradicted_at is non-NULL and within the suppression window.
// A nil lastContradictedAt means the card was never contradicted → not suppressed.
func SuppressionMultiplier(cardType CardType, lastContradictedAt *time.Time, now time.Time) float64 {
	if lastContradictedAt == nil {
		return 1.0
	}
	days, ok := suppressionWindowDays[cardType]
	if !ok {
		days = 14
	}
	windowSecs := days * 86400
	elapsed := now.Sub(*lastContradictedAt).Seconds()
	if elapsed >= windowSecs {
		return 1.0
	}
	return 0.0
}

// EffectiveScore computes the effective score at retrieval time.
// effective_score = score * decay * suppression, clamped to [0, +5.0].
// Values below 1e-6 are treated as 0.
func EffectiveScore(c *Card, now time.Time) float64 {
	decay := DecayMultiplier(c.Type, c.DecayAnchor, now)
	suppression := SuppressionMultiplier(c.Type, c.LastContradictedAt, now)
	score := c.Score * decay * suppression
	if score < 1e-6 {
		return 0.0
	}
	if score > ScoreCap {
		return ScoreCap
	}
	return score
}

// scoreDeltaResult holds the result of scoreDelta.
type scoreDeltaResult struct {
	delta               float64
	setContradicted     bool
	setNacked           bool
	clearsContradiction bool
}

// scoreDelta returns the score delta and flag state for a given event kind.
// Pure function with no side effects.
func scoreDelta(kind string) scoreDeltaResult {
	switch kind {
	case "ack":
		return scoreDeltaResult{delta: +0.3}
	case "confirmed":
		return scoreDeltaResult{delta: +0.2, clearsContradiction: true}
	case "nack":
		return scoreDeltaResult{delta: -0.5, setNacked: true}
	case "contradicted":
		return scoreDeltaResult{delta: -0.4, setContradicted: true}
	default:
		return scoreDeltaResult{}
	}
}
