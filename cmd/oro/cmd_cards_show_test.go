package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"oro/pkg/cards"
)

func TestCardsShowPrintsBody(t *testing.T) {
	ctx := context.Background()
	store := newTestCardStore(t)

	seeded, err := store.Create(ctx, cards.CardCreateParams{
		ID:          "card-show-1",
		Type:        cards.CardTypePattern,
		Title:       "prefer injected stores for CLI reads",
		BodySummary: "short summary",
		BodyFull:    "Full body line one.\nFull body line two.",
	})
	if err != nil {
		t.Fatalf("seed card: %v", err)
	}

	var out bytes.Buffer
	if err := runCardsShow(ctx, store, seeded.ID, &out); err != nil {
		t.Fatalf("runCardsShow: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"prefer injected stores for CLI reads",
		"card-show-1",
		string(cards.CardTypePattern),
		"Full body line one.\nFull body line two.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, seeded.BodyFull) != 1 {
		t.Fatalf("BodyFull printed %d times, want exactly once:\n%s", strings.Count(got, seeded.BodyFull), got)
	}
}

func TestCardsShowNilStoreErrors(t *testing.T) {
	var out bytes.Buffer
	err := runCardsShow(context.Background(), nil, "card-show-1", &out)
	if err == nil {
		t.Fatal("runCardsShow with nil store returned nil error")
	}
	if !strings.Contains(err.Error(), "card store is required") {
		t.Fatalf("error = %q, want explicit nil store error", err.Error())
	}
}
