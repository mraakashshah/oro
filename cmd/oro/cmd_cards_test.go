package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"oro/pkg/cards"
)

func TestCardsShowPrintsFullBody(t *testing.T) {
	ctx := context.Background()
	store := newTestCardStore(t)

	_, err := store.Create(ctx, cards.CardCreateParams{
		ID:          "card-show-1",
		Type:        cards.CardTypePattern,
		Title:       "Show Card Title",
		BodySummary: "SHOW_SUMMARY_SENTINEL",
		BodyFull:    "SHOW_FULL_BODY_SENTINEL",
		Tags:        []string{"show-tag"},
	})
	if err != nil {
		t.Fatalf("seed card: %v", err)
	}

	var out bytes.Buffer
	cmd := newCardsShowCmdWithStore(store)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"card-show-1"})
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("cards show card-show-1: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"Show Card Title",
		"SHOW_SUMMARY_SENTINEL",
		"SHOW_FULL_BODY_SENTINEL",
		"score",
		"show-tag",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("cards show output missing %q:\n%s", want, got)
		}
	}

	out.Reset()
	cmd = newCardsShowCmdWithStore(store)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"card-show-1", "--json"})
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("cards show card-show-1 --json: %v", err)
	}
	var payload struct {
		BodyFull string `json:"body_full"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("cards show --json emitted invalid JSON: %v\n%s", err, out.String())
	}
	if payload.BodyFull != "SHOW_FULL_BODY_SENTINEL" {
		t.Fatalf("body_full = %q, want SHOW_FULL_BODY_SENTINEL", payload.BodyFull)
	}

	out.Reset()
	cmd = newCardsShowCmdWithStore(store)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"missing-card"})
	if err := cmd.ExecuteContext(ctx); err == nil {
		t.Fatal("cards show missing-card returned nil error")
	}
}
