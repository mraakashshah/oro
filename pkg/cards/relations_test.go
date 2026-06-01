package cards_test

import (
	"context"
	"database/sql"
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
