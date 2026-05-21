package main

import (
	"context"
	"testing"

	"oro/pkg/cards"
	"oro/pkg/dbutil"
)

func newTestCardStore(t *testing.T) cards.Store {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("new card store: %v", err)
	}
	return store
}

func containsTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

// TestImportFromMemoryClassifiesAndTags seeds the testdata/memory_fixture/
// directory and verifies that import-from-memory:
//   - creates exactly one card per non-index memory file
//   - tags every card with "legacy_memory"
//   - assigns card.type via the tag-classifier heuristic
//   - leaves the card store unchanged when --dry-run is set
func TestImportFromMemoryClassifiesAndTags(t *testing.T) {
	ctx := context.Background()
	store := newTestCardStore(t)
	fixtureDir := "testdata/memory_fixture"

	// --dry-run: card store must remain empty.
	_, err := importFromMemoryDir(ctx, store, fixtureDir, true)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	afterDryRun, err := store.List(ctx, cards.ListQuery{})
	if err != nil {
		t.Fatalf("list after dry-run: %v", err)
	}
	if len(afterDryRun) != 0 {
		t.Errorf("dry-run: want 0 cards in store, got %d", len(afterDryRun))
	}

	// Real import.
	n, err := importFromMemoryDir(ctx, store, fixtureDir, false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n == 0 {
		t.Fatal("import returned 0 cards; fixture must have at least one non-index .md file")
	}

	all, err := store.List(ctx, cards.ListQuery{})
	if err != nil {
		t.Fatalf("list after import: %v", err)
	}

	// Every memory entry produces exactly one card.
	if len(all) != n {
		t.Errorf("card count: store has %d cards, import reported %d", len(all), n)
	}

	// All cards must carry the legacy_memory tag.
	for _, c := range all {
		if !containsTag(c.Tags, "legacy_memory") {
			t.Errorf("card %q (title %q) missing legacy_memory tag; got %v", c.ID, c.Title, c.Tags)
		}
	}

	// card.type is set by the filename-prefix heuristic per §5.8 D.2.
	byTitle := make(map[string]cards.Card, len(all))
	for _, c := range all {
		byTitle[c.Title] = c
	}

	wantTypes := map[string]cards.CardType{
		// feedback_tdd.md   — prefix "feedback_" → rule
		"always run tests before committing": cards.CardTypeRule,
		// fix_cleanup.md    — prefix "fix_" wins over type:feedback → pattern
		"unconditionally clear tracking maps on cleanup": cards.CardTypePattern,
		// decision_sqlite.md — prefix "decision_" → decision
		"use sqlite for beadstore": cards.CardTypeDecision,
		// user_background.md — no special prefix, type:user → pattern (default)
		"user has deep go expertise": cards.CardTypePattern,
		// reference_link.md  — no special prefix, type:reference → pattern (default)
		"grafana latency dashboard": cards.CardTypePattern,
	}

	for title, wantType := range wantTypes {
		c, ok := byTitle[title]
		if !ok {
			t.Errorf("card with title %q not found after import", title)
			continue
		}
		if c.Type != wantType {
			t.Errorf("card %q: type = %q, want %q", title, c.Type, wantType)
		}
	}
}

// TestImportFromMemoryIdempotent verifies that re-running import-from-memory
// inserts 0 additional cards (content-hash deduplication).
func TestImportFromMemoryIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newTestCardStore(t)
	fixtureDir := "testdata/memory_fixture"

	// First run — inserts N cards.
	n1, err := importFromMemoryDir(ctx, store, fixtureDir, false)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if n1 == 0 {
		t.Fatal("first import: expected > 0 cards")
	}

	all1, err := store.List(ctx, cards.ListQuery{})
	if err != nil {
		t.Fatalf("list after first import: %v", err)
	}
	if len(all1) != n1 {
		t.Errorf("after first import: store has %d cards, import reported %d", len(all1), n1)
	}

	// Second run — must insert 0 new cards.
	n2, err := importFromMemoryDir(ctx, store, fixtureDir, false)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second import: want 0 new cards, got %d", n2)
	}

	all2, err := store.List(ctx, cards.ListQuery{})
	if err != nil {
		t.Fatalf("list after second import: %v", err)
	}
	if len(all2) != len(all1) {
		t.Errorf("card count after second import: got %d, want %d (same as after first run)", len(all2), len(all1))
	}
}

func TestMemoryImportTextHelpers(t *testing.T) {
	if got := firstNonEmptyLine("\n\t\n  title line  \nnext"); got != "title line" {
		t.Fatalf("firstNonEmptyLine() = %q, want title line", got)
	}
	if got := firstNonEmptyLine("\n \t"); got != "" {
		t.Fatalf("firstNonEmptyLine(blank) = %q, want empty", got)
	}
	if got := memTruncate("short", 10); got != "short" {
		t.Fatalf("memTruncate short = %q", got)
	}
	if got := memTruncate("longer text", 6); got != "longer…" {
		t.Fatalf("memTruncate long = %q", got)
	}
}
