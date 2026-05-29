package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/cards"
)

func TestCardsCreateRetireList(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PROJECT", "")

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	createOut, _, err := executeCommand(
		"cards", "create", "pattern", "Use worktree oro for cards CLI",
		"--summary", "Run the worktree command when checking new CLI behavior.",
		"--body", "The installed oro on PATH may lag the current worktree build.",
		"--tag", "cli",
		"--tag", "cards",
	)
	if err != nil {
		t.Fatalf("cards create: %v", err)
	}
	if !strings.Contains(createOut, "Created card ") {
		t.Fatalf("create output = %q, want creation confirmation", createOut)
	}

	created := onlyCard(t, db)
	if created.Type != cards.CardTypePattern {
		t.Fatalf("created card type = %q, want pattern", created.Type)
	}
	if created.Title != "Use worktree oro for cards CLI" {
		t.Fatalf("created title = %q", created.Title)
	}
	if created.PromotionConfidence != nil {
		t.Fatalf("promotion confidence = %v, want nil without --confidence", *created.PromotionConfidence)
	}
	if !containsTag(created.Tags, "cli") || !containsTag(created.Tags, "cards") {
		t.Fatalf("created tags = %v, want cli and cards", created.Tags)
	}

	listOut, _, err := executeCommand("cards", "list")
	if err != nil {
		t.Fatalf("cards list: %v", err)
	}
	if !strings.Contains(listOut, created.ID) ||
		!strings.Contains(listOut, "pattern") ||
		!strings.Contains(listOut, "Use worktree oro for cards CLI") ||
		!strings.Contains(listOut, "Run the worktree command") {
		t.Fatalf("list output = %q, want card summary", listOut)
	}

	retireOut, _, err := executeCommand("cards", "retire", created.ID, "--reason", "superseded by store-backed cards")
	if err != nil {
		t.Fatalf("cards retire: %v", err)
	}
	if !strings.Contains(retireOut, "Retired card "+created.ID) {
		t.Fatalf("retire output = %q, want retire confirmation", retireOut)
	}
	retired := showCard(ctx, t, db, created.ID)
	if retired.RetiredAt == nil {
		t.Fatalf("retired card RetiredAt = nil")
	}
	if retired.RetiredReason == nil || *retired.RetiredReason != "superseded by store-backed cards" {
		t.Fatalf("retired reason = %v", retired.RetiredReason)
	}

	listOut, _, err = executeCommand("cards", "list")
	if err != nil {
		t.Fatalf("cards list after retire: %v", err)
	}
	if strings.Contains(listOut, created.ID) {
		t.Fatalf("list output after retire = %q, retired card should be hidden by default", listOut)
	}

	_, _, err = executeCommand("cards", "retire", "card-does-not-exist", "--reason", "missing")
	if err == nil {
		t.Fatal("cards retire unknown id err = nil, want non-zero exit")
	}
}

func onlyCard(t *testing.T, db *sql.DB) cards.Card {
	t.Helper()
	store, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("cards.NewStore: %v", err)
	}
	all, err := store.List(context.Background(), cards.ListQuery{IncludeRetired: true})
	if err != nil {
		t.Fatalf("List cards: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("card count = %d, want 1", len(all))
	}
	return all[0]
}

func showCard(ctx context.Context, t *testing.T, db *sql.DB, id string) cards.Card {
	t.Helper()
	store, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("cards.NewStore: %v", err)
	}
	card, err := store.Show(ctx, id)
	if err != nil {
		t.Fatalf("Show card %s: %v", id, err)
	}
	return *card
}
