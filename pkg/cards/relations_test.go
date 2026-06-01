package cards_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"oro/pkg/cards"
	"oro/pkg/dbutil"
)

func TestAddRelation_AccumulatesStrength(t *testing.T) {
	ctx := context.Background()
	store, db := newRelationTestStore(t)
	source := mustCreate(t, store, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "Caller",
		BodySummary: "source summary",
		BodyFull:    "source body",
	})
	target := mustCreate(t, store, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "Callee",
		BodySummary: "target summary",
		BodyFull:    "target body",
	})

	if err := store.AddRelation(ctx, source.ID, target.ID, cards.RelationSignalCall); err != nil {
		t.Fatalf("add call relation: %v", err)
	}
	if err := store.AddRelation(ctx, source.ID, target.ID, cards.RelationSignalComention); err != nil {
		t.Fatalf("add comention relation: %v", err)
	}

	assertRelationStrength(t, db, source.ID, target.ID, 5)
	assertRelationRows(t, db, source.ID, target.ID, 2)
	assertSignalStrength(t, db, source.ID, target.ID, cards.RelationSignalCall, 3)
	assertSignalStrength(t, db, source.ID, target.ID, cards.RelationSignalComention, 2)
	assertRelationStrength(t, db, target.ID, source.ID, 2)
}

func TestAddRelation_RejectsSelf(t *testing.T) {
	ctx := context.Background()
	store, db := newRelationTestStore(t)
	card := mustCreate(t, store, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "Self",
		BodySummary: "summary",
		BodyFull:    "body",
	})

	if err := store.AddRelation(ctx, card.ID, card.ID, cards.RelationSignalCall); err == nil {
		t.Fatal("add self relation succeeded, want error")
	}
	assertRelationRows(t, db, card.ID, card.ID, 0)
}

func TestSeeAlso_OrdersByStrength(t *testing.T) {
	ctx := context.Background()
	store, _ := newRelationTestStore(t)
	source := mustCreateRelationCard(t, store, "Source")
	low := mustCreateRelationCard(t, store, "Low")
	high := mustCreateRelationCard(t, store, "High")

	if err := store.AddRelation(ctx, source.ID, low.ID, cards.RelationSignalCall); err != nil {
		t.Fatalf("add low relation: %v", err)
	}
	if err := store.AddRelation(ctx, source.ID, high.ID, cards.RelationSignalCall); err != nil {
		t.Fatalf("add high call relation: %v", err)
	}
	if err := store.AddRelation(ctx, source.ID, high.ID, cards.RelationSignalComention); err != nil {
		t.Fatalf("add high comention relation: %v", err)
	}

	got, err := store.SeeAlso(ctx, source.ID, 10)
	if err != nil {
		t.Fatalf("see also: %v", err)
	}
	assertCardIDs(t, got, []string{high.ID, low.ID})
}

func TestSeeAlso_CycleSafe(t *testing.T) {
	ctx := context.Background()
	store, _ := newRelationTestStore(t)
	first := mustCreateRelationCard(t, store, "First")
	second := mustCreateRelationCard(t, store, "Second")

	if err := store.AddRelation(ctx, first.ID, second.ID, cards.RelationSignalCall); err != nil {
		t.Fatalf("add forward relation: %v", err)
	}
	if err := store.AddRelation(ctx, second.ID, first.ID, cards.RelationSignalCall); err != nil {
		t.Fatalf("add back relation: %v", err)
	}

	got, err := store.SeeAlso(ctx, first.ID, 10)
	if err != nil {
		t.Fatalf("see also: %v", err)
	}
	assertCardIDs(t, got, []string{second.ID})
}

func TestLineage_CycleDetected(t *testing.T) {
	ctx := context.Background()
	store, db := newRelationTestStore(t)
	first := mustCreate(t, store, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "First",
		BodySummary: "first summary",
		BodyFull:    "first body",
	})
	second := mustCreate(t, store, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "Second",
		BodySummary: "second summary",
		BodyFull:    "second body",
	})

	setSupersededBy(t, db, first.ID, second.ID)
	setSupersededBy(t, db, second.ID, first.ID)

	if _, err := store.Lineage(ctx, first.ID); !errors.Is(err, cards.ErrCycleDetected) {
		t.Fatalf("lineage error = %v, want ErrCycleDetected", err)
	}
}

func TestLatestInChain_ReturnsTip(t *testing.T) {
	ctx := context.Background()
	store, _ := newRelationTestStore(t)
	first := mustCreate(t, store, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "Original",
		BodySummary: "original summary",
		BodyFull:    "original body",
	})
	second := mustCreate(t, store, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "Revision",
		BodySummary: "revision summary",
		BodyFull:    "revision body",
	})
	tip := mustCreate(t, store, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "Tip",
		BodySummary: "tip summary",
		BodyFull:    "tip body",
	})

	if err := store.Retire(ctx, first.ID, "superseded", second.ID); err != nil {
		t.Fatalf("retire first: %v", err)
	}
	if err := store.Retire(ctx, second.ID, "superseded", tip.ID); err != nil {
		t.Fatalf("retire second: %v", err)
	}

	got, err := store.LatestInChain(ctx, first.ID)
	if err != nil {
		t.Fatalf("latest in chain: %v", err)
	}
	if got.ID != tip.ID {
		t.Fatalf("latest id = %s, want %s", got.ID, tip.ID)
	}
	if got.RetiredAt != nil {
		t.Fatal("latest card is retired, want active tip")
	}
}

func TestParseWikilinks_Extracts(t *testing.T) {
	got := cards.ParseWikilinks("see [[Foo]] and [[Bar/Baz]]")
	want := []string{"Foo", "Bar/Baz"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseWikilinks() = %#v, want %#v", got, want)
	}
}

func TestCreate_AddsWikilinkRelations(t *testing.T) {
	store, db := newRelationTestStore(t)
	target := mustCreate(t, store, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "Foo",
		BodySummary: "target summary",
		BodyFull:    "target body",
	})
	source := mustCreate(t, store, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "Source",
		BodySummary: "source summary",
		BodyFull:    "see [[Foo]]",
	})

	assertSignalStrength(t, db, source.ID, target.ID, cards.RelationSignalWikilink, 2)
}

func newRelationTestStore(t *testing.T) (*cards.SQLiteCardStore, *sql.DB) {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store, db
}

func mustCreateRelationCard(t *testing.T, store *cards.SQLiteCardStore, title string) *cards.Card {
	t.Helper()
	return mustCreate(t, store, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       title,
		BodySummary: title + " summary",
		BodyFull:    title + " body",
	})
}

func assertCardIDs(t *testing.T, got []cards.CardSummary, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("card count = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("card[%d] ID = %s, want %s", i, got[i].ID, want[i])
		}
	}
}

func setSupersededBy(t *testing.T, db *sql.DB, id, supersededBy string) {
	t.Helper()
	result, err := db.Exec(
		`UPDATE cards SET superseded_by = ?, retired_at = COALESCE(retired_at, updated_at) WHERE id = ?`,
		supersededBy,
		id,
	)
	if err != nil {
		t.Fatalf("set superseded_by: %v", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("set superseded_by rows affected: %v", err)
	}
	if affected != 1 {
		t.Fatalf("set superseded_by affected %d rows, want 1", affected)
	}
}

func assertRelationStrength(t *testing.T, db *sql.DB, sourceID, targetID string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(
		`SELECT COALESCE(SUM(strength), 0) FROM card_relations WHERE source_id = ? AND target_id = ?`,
		sourceID,
		targetID,
	).Scan(&got); err != nil {
		t.Fatalf("sum relation strength: %v", err)
	}
	if got != want {
		t.Fatalf("relation strength %s -> %s = %d, want %d", sourceID, targetID, got, want)
	}
}

func assertRelationRows(t *testing.T, db *sql.DB, sourceID, targetID string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM card_relations WHERE source_id = ? AND target_id = ?`,
		sourceID,
		targetID,
	).Scan(&got); err != nil {
		t.Fatalf("count relation rows: %v", err)
	}
	if got != want {
		t.Fatalf("relation rows %s -> %s = %d, want %d", sourceID, targetID, got, want)
	}
}

func assertSignalStrength(
	t *testing.T,
	db *sql.DB,
	sourceID string,
	targetID string,
	signal cards.RelationSignal,
	want int,
) {
	t.Helper()
	var got int
	if err := db.QueryRow(
		`SELECT strength FROM card_relations WHERE source_id = ? AND target_id = ? AND signal = ?`,
		sourceID,
		targetID,
		signal,
	).Scan(&got); err != nil {
		t.Fatalf("query signal strength: %v", err)
	}
	if got != want {
		t.Fatalf("signal strength %s -> %s %s = %d, want %d", sourceID, targetID, signal, got, want)
	}
}
