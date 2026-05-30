// Package cards implements the typed card store for durable knowledge.
// Cards replace pkg/memory as the long-lived knowledge layer (§5 of harness spec).
package cards

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a card ID does not exist.
var ErrNotFound = errors.New("card not found")

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

// DeckCard is the deck-view representation of a relevant card.
type DeckCard struct {
	ID          string
	Type        CardType
	Title       string
	BodySummary string
	Score       float64
	Tags        []string
}

// InlinedCard is the inline representation of a relevant card.
type InlinedCard struct {
	ID          string
	Type        CardType
	Title       string
	BodySummary string
	BodyFull    string
	Score       float64
	Tags        []string
}

// CardSummary is retained for callers that work with fully populated card
// summaries outside the relevance wire payload.
type CardSummary = InlinedCard

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
	Deck    []DeckCard    // body_summary only, all relevant
	Inlined []InlinedCard // body_full inlined, fits within MaxTokens
}

// ReadTx exposes read methods within a transaction.
type ReadTx interface {
	Show(ctx context.Context, id string) (*Card, error)
	List(ctx context.Context, q ListQuery) ([]Card, error)
	Relevant(ctx context.Context, q RelevanceQuery) (RelevantCards, error)
}
