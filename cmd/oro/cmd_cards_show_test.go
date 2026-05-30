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

func TestCardsShowCommandReadsProjectStateDB(t *testing.T) {
	t.Setenv("ORO_HOME", t.TempDir())
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_DB_PATH", "")
	t.Setenv("ORO_PID_PATH", "")
	t.Setenv("ORO_SOCKET_PATH", "")

	ctx := context.Background()
	paths, err := ResolveProjectDBPaths()
	if err != nil {
		t.Fatalf("resolve paths: %v", err)
	}
	db, err := openStateDB(paths.StateDBPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	store, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("new card store: %v", err)
	}
	_, err = store.Create(ctx, cards.CardCreateParams{
		ID:          "card-state-show",
		Type:        cards.CardTypeDecision,
		Title:       "state DB card",
		BodySummary: "summary from state DB",
		BodyFull:    "Full body from the project state DB.\nSecond line from state DB.",
	})
	if err != nil {
		t.Fatalf("seed card: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"cards", "show", "card-state-show"})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("cards show seeded card: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "Full body from the project state DB.\nSecond line from state DB.") {
		t.Fatalf("output missing full body:\n%s", got)
	}

	out.Reset()
	root = newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"cards", "show", "missing-card"})
	err = root.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("cards show missing-card returned nil error")
	}
	if !strings.Contains(err.Error(), "card missing-card not found") {
		t.Fatalf("missing-card error = %q, want not-found card id", err.Error())
	}

	out.Reset()
	root = newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"cards", "show"})
	err = root.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("cards show without id returned nil error")
	}
	if !strings.Contains(err.Error(), "accepts 1 arg(s), received 0") {
		t.Fatalf("missing-id error = %q, want cobra usage error", err.Error())
	}
}
