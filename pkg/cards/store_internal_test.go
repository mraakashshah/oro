package cards

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"oro/pkg/dbutil"
)

func TestRelevantDeckOmitsBodyFull(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	_, err = store.Create(ctx, CardCreateParams{
		Type:        CardTypePattern,
		Title:       "relevant payload shape",
		BodySummary: "deck summary",
		BodyFull:    "INLINE_FULL_BODY_SENTINEL DECK_FULL_BODY_SENTINEL",
		Tags:        []string{"sqlite"},
	})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}

	result, err := store.Relevant(ctx, RelevanceQuery{
		BeadTags:  []string{"sqlite"},
		BeadType:  "task",
		MaxTokens: 1000,
	})
	if err != nil {
		t.Fatalf("Relevant: %v", err)
	}

	deckJSON, err := json.Marshal(result.Deck)
	if err != nil {
		t.Fatalf("marshal deck: %v", err)
	}
	if strings.Contains(string(deckJSON), "DECK_FULL_BODY_SENTINEL") {
		t.Fatalf("deck JSON includes full body: %s", deckJSON)
	}

	inlinedJSON, err := json.Marshal(result.Inlined)
	if err != nil {
		t.Fatalf("marshal inlined: %v", err)
	}
	if !strings.Contains(string(inlinedJSON), "INLINE_FULL_BODY_SENTINEL") {
		t.Fatalf("inlined JSON omits full body: %s", inlinedJSON)
	}

	c := Card{ID: "card-test", Type: CardTypePattern, Title: "helper", BodySummary: "summary", BodyFull: "full"}
	if toInlinedCard(c).BodyFull != "full" {
		t.Fatalf("toInlinedCard omitted BodyFull")
	}
}
