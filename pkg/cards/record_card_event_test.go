package cards_test

import (
	"context"
	"math"
	"sync"
	"testing"

	"oro/pkg/cards"
	"oro/pkg/dbutil"
)

// newSerialStore opens an in-memory store with MaxOpenConns=1 so all SQLite
// writes are serialized through a single connection — required for in-memory
// WAL-mode correctness in concurrent tests.
func newSerialStore(t *testing.T) *cards.SQLiteCardStore {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func TestRecordCardEvent(t *testing.T) {
	ctx := context.Background()

	t.Run("concurrent_acks_serialize_no_score_loss", func(t *testing.T) {
		store := newSerialStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "concurrent ack test",
			BodySummary: "s", BodyFull: "b",
		})

		const n = 20
		var wg sync.WaitGroup
		errs := make(chan error, n)
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := store.RecordCardEvent(ctx, cards.CardEvent{
					CardID: card.ID, Actor: "worker", Kind: "ack",
				}); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("concurrent ack: %v", err)
		}

		got, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		// 20 acks × 0.3 = +6.0 above initial 1.0 → capped at 5.0
		if got.Score > cards.ScoreCap {
			t.Errorf("score exceeded cap after concurrent acks: %f > %f", got.Score, cards.ScoreCap)
		}
		if math.Abs(got.Score-cards.ScoreCap) > 0.001 {
			t.Errorf("score should be at cap after 20 acks: got %f, want %f", got.Score, cards.ScoreCap)
		}
	})

	t.Run("concurrent_nacks_serialize_no_score_loss", func(t *testing.T) {
		store := newSerialStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "concurrent nack test",
			BodySummary: "s", BodyFull: "b",
		})

		const n = 20
		var wg sync.WaitGroup
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Ignore error: card may be retired by auto-retire mid-loop.
				_ = store.RecordCardEvent(ctx, cards.CardEvent{
					CardID: card.ID, Actor: "worker", Kind: "nack",
				})
			}()
		}
		wg.Wait()

		got, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if got.Score < cards.ScoreFloor {
			t.Errorf("score went below floor after concurrent nacks: %f < %f", got.Score, cards.ScoreFloor)
		}
	})

	t.Run("concurrent_mixed_acks_nacks_bounded", func(t *testing.T) {
		store := newSerialStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "concurrent mixed test",
			BodySummary: "s", BodyFull: "b",
		})

		const n = 30
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				kind := "ack"
				if i%3 == 0 {
					kind = "nack"
				}
				_ = store.RecordCardEvent(ctx, cards.CardEvent{
					CardID: card.ID, Actor: "worker", Kind: kind,
				})
			}(i)
		}
		wg.Wait()

		got, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if got.Score < cards.ScoreFloor {
			t.Errorf("score below floor: %f", got.Score)
		}
		if got.Score > cards.ScoreCap {
			t.Errorf("score above cap: %f", got.Score)
		}
	})

	t.Run("score_capped_at_ScoreCap", func(t *testing.T) {
		store := newTestStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "cap test",
			BodySummary: "s", BodyFull: "b",
		})

		// 20 acks × 0.3 = +6.0 over initial 1.0 → must be capped
		for range 20 {
			if err := store.RecordCardEvent(ctx, cards.CardEvent{
				CardID: card.ID, Actor: "worker", Kind: "ack",
			}); err != nil {
				t.Fatalf("ack: %v", err)
			}
		}

		got, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if got.Score > cards.ScoreCap {
			t.Errorf("score exceeded cap: %f > %f", got.Score, cards.ScoreCap)
		}
		if math.Abs(got.Score-cards.ScoreCap) > 0.001 {
			t.Errorf("score not at cap: got %f, want %f", got.Score, cards.ScoreCap)
		}
	})

	t.Run("score_floored_at_ScoreFloor", func(t *testing.T) {
		store := newTestStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "floor test",
			BodySummary: "s", BodyFull: "b",
		})

		// Enough nacks to drive well below floor: initial 1.0 - 10*0.5 = -4.0
		// floor must hold at -2.0
		for range 10 {
			_ = store.RecordCardEvent(ctx, cards.CardEvent{
				CardID: card.ID, Actor: "worker", Kind: "nack",
			})
		}

		got, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if got.Score < cards.ScoreFloor {
			t.Errorf("score went below floor: %f < %f", got.Score, cards.ScoreFloor)
		}
	})

	t.Run("confirmed_clears_contradiction", func(t *testing.T) {
		store := newTestStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "confirmed clears contradiction",
			BodySummary: "s", BodyFull: "b",
		})

		if err := store.RecordCardEvent(ctx, cards.CardEvent{
			CardID: card.ID, Actor: "worker", Kind: "contradicted",
		}); err != nil {
			t.Fatalf("contradicted: %v", err)
		}
		after, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show after contradicted: %v", err)
		}
		if after.LastContradictedAt == nil {
			t.Fatal("last_contradicted_at should be set after contradicted event")
		}

		if err := store.RecordCardEvent(ctx, cards.CardEvent{
			CardID: card.ID, Actor: "worker", Kind: "confirmed",
		}); err != nil {
			t.Fatalf("confirmed: %v", err)
		}
		after, err = store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show after confirmed: %v", err)
		}
		// R3.2: confirmed must reset last_contradicted_at to NULL
		if after.LastContradictedAt != nil {
			t.Error("last_contradicted_at must be NULL after confirmed event (R3.2 fix)")
		}
	})

	t.Run("auto_retire_fires_at_threshold", func(t *testing.T) {
		store := newTestStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "auto retire",
			BodySummary: "s", BodyFull: "b",
		})

		// 3 nacks: 1.0 - 3*0.5 = -0.5 — not yet at AutoRetireThresh (-1.0)
		for range 3 {
			if err := store.RecordCardEvent(ctx, cards.CardEvent{
				CardID: card.ID, Actor: "worker", Kind: "nack",
			}); err != nil {
				t.Fatalf("nack: %v", err)
			}
		}
		notYet, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show after 3 nacks: %v", err)
		}
		if notYet.RetiredAt != nil {
			t.Error("card must NOT be retired after 3 nacks (score = -0.5 > -1.0 threshold)")
		}

		// 4th nack: -0.5 - 0.5 = -1.0, exactly at AutoRetireThresh — must trigger
		if err := store.RecordCardEvent(ctx, cards.CardEvent{
			CardID: card.ID, Actor: "worker", Kind: "nack",
		}); err != nil {
			t.Fatalf("4th nack: %v", err)
		}
		retired, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show after 4th nack: %v", err)
		}
		if retired.RetiredAt == nil {
			t.Errorf("card must be auto-retired when score reaches AutoRetireThresh (%f), got score=%f",
				cards.AutoRetireThresh, retired.Score)
		}
		if retired.RetiredReason == nil || *retired.RetiredReason != "auto: persistent nack" {
			t.Errorf("retire reason: got %v, want 'auto: persistent nack'", retired.RetiredReason)
		}
	})

	t.Run("auto_retire_does_not_override_manual_retire", func(t *testing.T) {
		store := newTestStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "no double retire",
			BodySummary: "s", BodyFull: "b",
		})

		if err := store.Retire(ctx, card.ID, "manual reason", ""); err != nil {
			t.Fatalf("Retire: %v", err)
		}

		for range 5 {
			if err := store.RecordCardEvent(ctx, cards.CardEvent{
				CardID: card.ID, Actor: "worker", Kind: "nack",
			}); err != nil {
				t.Fatalf("nack after retire: %v", err)
			}
		}

		got, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if got.RetiredReason == nil || *got.RetiredReason != "manual reason" {
			t.Errorf("retired_reason should remain 'manual reason', got %v", got.RetiredReason)
		}
	})
}
