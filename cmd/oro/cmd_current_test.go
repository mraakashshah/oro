package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/cards"
	"oro/pkg/protocol"
)

// txCountStore wraps a beadstore.Store and counts WithReadTx invocations.
type txCountStore struct {
	beadstore.Store
	count int
}

func (s *txCountStore) WithReadTx(ctx context.Context, fn func(beadstore.ReadTx) error) error {
	s.count++
	return s.Store.WithReadTx(ctx, fn)
}

// openTestRenderStore opens a SQLite store with all schemas (bead v3 + cards) applied.
// Both bead and card data are written to the returned stores; the underlying DB is shared
// so that beadstore.WithReadTx's cards.NewReadTx accessor sees the card rows.
func openTestRenderStore(t *testing.T) (beadstore.Store, cards.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cs, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("cards.NewStore: %v", err)
	}
	return beadstore.NewSQLiteStore(db), cs
}

func TestCurrentRendersInProgressJourneyAndCards(t *testing.T) {
	ctx := context.Background()
	beadStore, cardStore := openTestRenderStore(t)

	// Seed 2 in-progress beads.
	inProg := "in_progress"
	for _, b := range []protocol.Bead{
		{ID: "bead-curr-1", Title: "Task Alpha", Status: "open", Type: "task", AcceptanceCriteria: "ac alpha"},
		{ID: "bead-curr-2", Title: "Task Beta", Status: "open", Type: "task", AcceptanceCriteria: "ac beta"},
	} {
		_, err := beadStore.Create(ctx, beadstore.CreateParams{
			ID:                 b.ID,
			Title:              b.Title,
			Type:               b.Type,
			AcceptanceCriteria: b.AcceptanceCriteria,
		})
		if err != nil {
			t.Fatalf("Create %s: %v", b.ID, err)
		}
		if err := beadStore.Update(ctx, b.ID, beadstore.UpdateParams{Status: &inProg}); err != nil {
			t.Fatalf("Update %s to in_progress: %v", b.ID, err)
		}
	}

	// Seed journey events (older first so order is visible).
	now := time.Now().UTC()
	events := []beadstore.JourneyEvent{
		{Ts: now.Add(-10 * time.Minute).Format(time.RFC3339Nano), Actor: "worker", Event: "started"},
		{Ts: now.Add(-5 * time.Minute).Format(time.RFC3339Nano), Actor: "worker", Event: "checkpoint"},
	}
	if err := beadStore.AppendJourney(ctx, "bead-curr-1", events[0]); err != nil {
		t.Fatalf("AppendJourney: %v", err)
	}
	if err := beadStore.AppendJourney(ctx, "bead-curr-2", events[1]); err != nil {
		t.Fatalf("AppendJourney: %v", err)
	}

	// Seed a card that will match via score (fresh cards start at 1.0 > DefaultThreshold 0.1).
	_, err := cardStore.Create(ctx, cards.CardCreateParams{
		ID:          "card-curr-1",
		Type:        cards.CardTypeRule,
		Title:       "Always write tests",
		BodySummary: "test coverage matters",
		BodyFull:    "full body",
		Tags:        []string{"testing", "task"},
	})
	if err != nil {
		t.Fatalf("Create card: %v", err)
	}

	// Wrap with tx counter to assert single-tx constraint.
	counter := &txCountStore{Store: beadStore}

	cmd := newCurrentCmdWithStore(counter)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--format", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("oro current --format json: %v\nstderr: %s", err, stderr.String())
	}

	var result struct {
		InProgress    []string         `json:"in_progress"`
		RecentJourney []map[string]any `json:"recent_journey"`
		Cards         []map[string]any `json:"cards"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal output: %v\noutput: %s", err, stdout.String())
	}

	// .in_progress must be exactly 2 elements containing both bead IDs.
	if len(result.InProgress) != 2 {
		t.Fatalf("in_progress len = %d, want 2; ids = %v", len(result.InProgress), result.InProgress)
	}
	ids := map[string]bool{"bead-curr-1": false, "bead-curr-2": false}
	for _, id := range result.InProgress {
		if _, ok := ids[id]; !ok {
			t.Fatalf("unexpected bead id in in_progress: %q", id)
		}
		ids[id] = true
	}
	for id, seen := range ids {
		if !seen {
			t.Fatalf("bead %q missing from in_progress", id)
		}
	}

	// .recent_journey must be non-empty and ordered timestamp DESC.
	if len(result.RecentJourney) == 0 {
		t.Fatal("recent_journey is empty")
	}
	for i := 1; i < len(result.RecentJourney); i++ {
		tsA, _ := result.RecentJourney[i-1]["ts"].(string)
		tsB, _ := result.RecentJourney[i]["ts"].(string)
		if tsA < tsB {
			t.Fatalf("recent_journey not sorted DESC: [%d]=%q < [%d]=%q", i-1, tsA, i, tsB)
		}
	}

	// .cards must contain the seeded card.
	if len(result.Cards) == 0 {
		t.Fatal("cards is empty; expected at least one card")
	}
	found := false
	for _, c := range result.Cards {
		if c["id"] == "card-curr-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("card-curr-1 missing from cards: %v", result.Cards)
	}

	// Entire snapshot must be read inside exactly one WithReadTx span.
	if counter.count != 1 {
		t.Fatalf("WithReadTx call count = %d, want 1", counter.count)
	}
}

func TestCurrentRendersDeckCardSummariesWithoutFullBody(t *testing.T) {
	ctx := context.Background()
	beadStore, cardStore := openTestRenderStore(t)

	_, err := beadStore.Create(ctx, beadstore.CreateParams{
		ID:                 "bead-current-deck",
		Title:              "Current Deck Bead",
		Type:               "task",
		AcceptanceCriteria: "current deck acceptance",
	})
	if err != nil {
		t.Fatalf("Create bead: %v", err)
	}
	inProgress := "in_progress"
	if err := beadStore.Update(ctx, "bead-current-deck", beadstore.UpdateParams{Status: &inProgress}); err != nil {
		t.Fatalf("Update bead to in_progress: %v", err)
	}

	_, err = cardStore.Create(ctx, cards.CardCreateParams{
		ID:          "card-current-deck",
		Type:        cards.CardTypePattern,
		Title:       "Current Deck Card",
		BodySummary: "CURRENT_SUMMARY_SENTINEL",
		BodyFull:    "CURRENT_FULL_BODY_SENTINEL",
		Tags:        []string{"current-tag"},
	})
	if err != nil {
		t.Fatalf("Create card: %v", err)
	}

	cmd := newCurrentCmdWithStore(beadStore)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--format", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("oro current --format json: %v\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	if strings.Contains(output, "body_full") {
		t.Fatalf("current JSON included body_full field:\n%s", output)
	}
	if strings.Contains(output, "CURRENT_FULL_BODY_SENTINEL") {
		t.Fatalf("current JSON included full body sentinel:\n%s", output)
	}

	var result struct {
		Cards []cardSummaryJSON `json:"cards"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal output: %v\noutput: %s", err, output)
	}

	for _, got := range result.Cards {
		if got.ID != "card-current-deck" {
			continue
		}
		if got.Title != "Current Deck Card" {
			t.Fatalf("title = %q, want Current Deck Card", got.Title)
		}
		if got.BodySummary != "CURRENT_SUMMARY_SENTINEL" {
			t.Fatalf("body_summary = %q, want CURRENT_SUMMARY_SENTINEL", got.BodySummary)
		}
		if got.Score == 0 {
			t.Fatalf("score = %v, want non-zero score", got.Score)
		}
		for _, tag := range got.Tags {
			if tag == "current-tag" {
				return
			}
		}
		t.Fatalf("tags = %v, want current-tag", got.Tags)
	}
	t.Fatalf("card-current-deck missing from cards: %#v", result.Cards)
}

func TestBuildCurrentViewDedupesDuplicateDeckCards(t *testing.T) {
	ctx := context.Background()
	tx := &currentReadTx{
		beads: []protocol.Bead{
			{ID: "bead-current-1", Type: "task"},
			{ID: "bead-current-2", Type: "task"},
		},
		cards: &currentCardsReadTx{relevant: cards.RelevantCards{
			Deck: []cards.DeckCard{
				{ID: "card-duplicate-deck", Type: cards.CardTypePattern, Title: "Duplicate Deck Card", BodySummary: "summary", Score: 1, Tags: []string{"tag"}},
			},
		}},
	}

	view, err := buildCurrentView(ctx, tx)
	if err != nil {
		t.Fatalf("buildCurrentView: %v", err)
	}

	if len(view.Cards) != 1 {
		t.Fatalf("cards len = %d, want 1; cards = %#v", len(view.Cards), view.Cards)
	}
	if view.Cards[0].ID != "card-duplicate-deck" {
		t.Fatalf("card id = %q, want card-duplicate-deck", view.Cards[0].ID)
	}
}

func TestBuildCurrentViewNilCardsRendersNoCards(t *testing.T) {
	ctx := context.Background()
	tx := &currentReadTx{
		beads: []protocol.Bead{{ID: "bead-current-nil-cards", Type: "task"}},
	}

	view, err := buildCurrentView(ctx, tx)
	if err != nil {
		t.Fatalf("buildCurrentView: %v", err)
	}

	if len(view.InProgress) != 1 {
		t.Fatalf("in_progress len = %d, want 1", len(view.InProgress))
	}
	if len(view.Cards) != 0 {
		t.Fatalf("cards len = %d, want 0; cards = %#v", len(view.Cards), view.Cards)
	}
}

func TestBeadRelevanceQueryDerivesSymbolHints(t *testing.T) {
	bead := protocol.Bead{
		Type:        "task",
		Tags:        []string{"cli", "cards"},
		Description: "surface relevant cards for current work",
		AcceptanceCriteria: strings.Join([]string{
			"Test: cmd/oro/cmd_current_test.go:TestBeadRelevanceQueryDerivesSymbolHints",
			"Read: cmd/oro/cmd_current.go:beadRelevanceQuery, pkg/codestruct/relate.go:ResolveCallee",
			"Read: docs/current.md, missing-bare-symbol",
			"Read: pkg/missing.go:/, pkg/line.go:25",
		}, "\n"),
	}

	got := beadRelevanceQuery(bead)

	if got.BeadType != bead.Type {
		t.Fatalf("BeadType = %q, want %q", got.BeadType, bead.Type)
	}
	if strings.Join(got.BeadTags, ",") != strings.Join(bead.Tags, ",") {
		t.Fatalf("BeadTags = %v, want %v", got.BeadTags, bead.Tags)
	}
	if got.BeadDescription != bead.Description {
		t.Fatalf("BeadDescription = %q, want %q", got.BeadDescription, bead.Description)
	}
	if got.MaxTokens != 2000 {
		t.Fatalf("MaxTokens = %d, want 2000", got.MaxTokens)
	}
	wantHints := []string{
		"cmd/oro/cmd_current.go:beadRelevanceQuery",
		"pkg/codestruct/relate.go:ResolveCallee",
	}
	if strings.Join(got.SymbolHints, ",") != strings.Join(wantHints, ",") {
		t.Fatalf("SymbolHints = %v, want %v", got.SymbolHints, wantHints)
	}
}

type currentReadTx struct {
	errorReadTx
	beads []protocol.Bead
	cards cards.ReadTx
}

func (tx *currentReadTx) InProgress(_ context.Context) ([]protocol.Bead, error) {
	return tx.beads, nil
}

func (tx *currentReadTx) LatestJourney(_ context.Context, _ string, _ int) ([]beadstore.JourneyEvent, error) {
	return nil, nil
}

func (tx *currentReadTx) Cards() cards.ReadTx {
	return tx.cards
}

type currentCardsReadTx struct {
	relevant cards.RelevantCards
}

func (tx *currentCardsReadTx) Show(_ context.Context, _ string) (*cards.Card, error) {
	return nil, cards.ErrNotFound
}

func (tx *currentCardsReadTx) List(_ context.Context, _ cards.ListQuery) ([]cards.Card, error) {
	return nil, nil
}

func (tx *currentCardsReadTx) Relevant(_ context.Context, _ cards.RelevanceQuery) (cards.RelevantCards, error) {
	return tx.relevant, nil
}

func TestCurrentCommandRegisteredInRoot(t *testing.T) {
	root := newRootCmd()
	for _, cmd := range root.Commands() {
		if cmd.Name() == "current" {
			return
		}
	}
	t.Fatal("root command did not register current subcommand")
}

func TestRenderCurrentTextCoversEmptyAndPopulatedViews(t *testing.T) {
	var empty bytes.Buffer
	if err := renderCurrentText(&empty, currentViewJSON{Snapshot: "2026-05-21T00:00:00Z"}); err != nil {
		t.Fatalf("render empty current text: %v", err)
	}
	if !strings.Contains(empty.String(), "No in-progress work.") {
		t.Fatalf("empty current text = %q, want no-work message", empty.String())
	}

	view := currentViewJSON{
		Snapshot:   "2026-05-21T00:00:00Z",
		InProgress: []string{"oro-a", "oro-b"},
		RecentJourney: []journeyItemJSON{
			{Ts: "2026-05-21T00:00:02Z", Actor: "worker", Event: "checkpoint"},
			{Ts: "2026-05-21T00:00:01Z", Actor: "dispatcher", Event: "assign"},
		},
		Cards: []cardSummaryJSON{
			{ID: "card-1", Title: "Preserve recovery work"},
		},
	}
	var populated bytes.Buffer
	if err := renderCurrentText(&populated, view); err != nil {
		t.Fatalf("render populated current text: %v", err)
	}
	out := populated.String()
	for _, want := range []string{"**In Progress:** [oro-a oro-b]", "**Recent Events:**", "worker] checkpoint", "**Cards:**", "[card-1] Preserve recovery work"} {
		if !strings.Contains(out, want) {
			t.Fatalf("populated current text missing %q:\n%s", want, out)
		}
	}
}
