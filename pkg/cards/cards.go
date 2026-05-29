// Package cards implements the typed card store for durable knowledge.
// Cards replace pkg/memory as the long-lived knowledge layer (§5 of harness spec).
package cards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a card ID does not exist.
var ErrNotFound = errors.New("card not found")

// ErrInvalidCardType is returned when a candidate uses an unknown card type.
var ErrInvalidCardType = errors.New("invalid card type")

// CardType is the closed enum of card types.
type CardType string

// Card type constants — closed enum matching the SQL CHECK constraint.
const (
	CardTypeRule     CardType = "rule"
	CardTypePattern  CardType = "pattern"
	CardTypeTaste    CardType = "taste"
	CardTypeDecision CardType = "decision"
	CardTypeFact     CardType = "fact"
)

// Card is a durable knowledge record.
type Card struct {
	ID                  string
	Type                CardType
	Title               string
	BodySummary         string // one line, < 200 chars
	BodyFull            string
	BodyDeep            *string
	Tags                []string
	Score               float64
	PromotionConfidence *float64
	DecayAnchor         time.Time
	LastContradictedAt  *time.Time
	LastNackedAt        *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	RetiredAt           *time.Time
	SupersededBy        *string
	EmergedFrom         *string
	RetiredReason       *string
}

// CardCandidate is the JSON shape emitted by workers before promotion.
type CardCandidate struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	BodySummary string   `json:"body_summary"`
	BodyFull    string   `json:"body_full"`
	Confidence  float64  `json:"confidence"`
	Evidence    []string `json:"evidence"`
	Tags        []string `json:"tags"`
}

// PendingLearning is a bead-scoped card candidate awaiting promotion or rejection.
type PendingLearning struct {
	ID                int64
	BeadID            string
	TS                time.Time
	Candidate         CardCandidate
	PromotedTo        *string
	RejectedAt        *time.Time
	Reason            *string
	QueuedForReviewAt *time.Time
}

// ParseCardCandidate unmarshals and normalizes a candidate JSON payload.
//
//oro:testonly — production wiring deferred to learning-pending persistence (§4.2).
func ParseCardCandidate(b []byte) (CardCandidate, error) {
	var candidate CardCandidate
	if err := json.Unmarshal(b, &candidate); err != nil {
		return CardCandidate{}, fmt.Errorf("parse card candidate: %w", err)
	}
	if !isValidCardType(CardType(candidate.Type)) {
		return CardCandidate{}, ErrInvalidCardType
	}
	candidate.Confidence = clampConfidence(candidate.Confidence)
	if len(candidate.Evidence) == 0 && candidate.Confidence > 0.4 {
		candidate.Confidence = 0.4
	}
	return candidate, nil
}

func isValidCardType(cardType CardType) bool {
	switch cardType {
	case CardTypeRule, CardTypePattern, CardTypeTaste, CardTypeDecision, CardTypeFact:
		return true
	default:
		return false
	}
}

func clampConfidence(confidence float64) float64 {
	switch {
	case confidence < 0:
		return 0
	case confidence > 1:
		return 1
	default:
		return confidence
	}
}

// CardSummary is the deck-view representation of a card.
type CardSummary struct {
	ID          string
	Type        CardType
	Title       string
	BodySummary string
	BodyFull    string
	Score       float64
	Tags        []string
}

// CardEvent represents an event to record against a card.
type CardEvent struct {
	CardID  string
	BeadID  string
	Actor   string
	Kind    string
	Payload string
}

// CardCreateParams are the parameters for creating a new card.
type CardCreateParams struct {
	ID                  string // optional; auto-generated if empty
	Type                CardType
	Title               string
	BodySummary         string
	BodyFull            string
	BodyDeep            *string
	Tags                []string
	EmergedFrom         *string
	PromotionConfidence *float64
}

// ListQuery filters for listing cards.
type ListQuery struct {
	Type           CardType
	Tags           []string
	IncludeRetired bool
	Limit          int
	Offset         int
}

// RelevanceQuery drives the Relevant read.
type RelevanceQuery struct {
	BeadType          string
	BeadTags          []string
	BeadDescription   string
	SymbolHints       []string
	MaxTokens         int
	IncludeLowScore   bool
	IncludeSuppressed bool
}

// RelevantCards is the result of a Relevant query.
type RelevantCards struct {
	Deck    []CardSummary // body_summary only, all relevant
	Inlined []CardSummary // body_full inlined, fits within MaxTokens
}

// ReadTx exposes read methods within a transaction.
type ReadTx interface {
	Show(ctx context.Context, id string) (*Card, error)
	List(ctx context.Context, q ListQuery) ([]Card, error)
	Relevant(ctx context.Context, q RelevanceQuery) (RelevantCards, error)
}
