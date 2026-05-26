package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"oro/pkg/cards"
	"oro/pkg/dbutil"
	"oro/pkg/memory"
	"oro/pkg/protocol"
)

func newMemoryAndCardStores(t *testing.T) (*memory.Store, cards.Store) {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("apply memory schema: %v", err)
	}
	cardStore, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("new card store: %v", err)
	}
	return memory.NewStore(db), cardStore
}

func TestBackfillMemoryDriftMirrorsLegacyRows(t *testing.T) {
	ctx := context.Background()

	t.Run("backfills_missing_mirror_and_is_idempotent", func(t *testing.T) {
		memStore, cardStore := newMemoryAndCardStores(t)
		memID, err := memStore.Insert(ctx, memory.InsertParams{
			Content:    "Backfilled memory card should preserve this complete body",
			Type:       "lesson",
			Tags:       []string{"repair", "cards"},
			Source:     "self_report",
			Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert legacy memory: %v", err)
		}

		n, err := backfillMemoryCardMirrors(ctx, memStore, cardStore, false)
		if err != nil {
			t.Fatalf("backfill: %v", err)
		}
		if n != 1 {
			t.Fatalf("backfill count = %d, want 1", n)
		}

		all, err := cardStore.List(ctx, cards.ListQuery{Type: cards.CardTypePattern})
		if err != nil {
			t.Fatalf("list cards: %v", err)
		}
		card := findCardByTag(all, fmt.Sprintf("mem-id:%d", memID))
		if card == nil {
			t.Fatalf("missing card tagged mem-id:%d", memID)
		}
		if !containsTag(card.Tags, "legacy_memory_dual_write") {
			t.Fatalf("card tags = %v, want legacy_memory_dual_write", card.Tags)
		}
		if card.BodyFull != "Backfilled memory card should preserve this complete body" {
			t.Fatalf("card body = %q", card.BodyFull)
		}

		n, err = backfillMemoryCardMirrors(ctx, memStore, cardStore, false)
		if err != nil {
			t.Fatalf("second backfill: %v", err)
		}
		if n != 0 {
			t.Fatalf("second backfill count = %d, want 0", n)
		}
		allAfter, err := cardStore.List(ctx, cards.ListQuery{Type: cards.CardTypePattern})
		if err != nil {
			t.Fatalf("list after second backfill: %v", err)
		}
		if len(allAfter) != 1 {
			t.Fatalf("card count after second backfill = %d, want 1", len(allAfter))
		}

		failures, err := memory.CheckCardDrift(ctx, memStore, cardStore)
		if err != nil {
			t.Fatalf("check drift: %v", err)
		}
		if len(failures) != 0 {
			t.Fatalf("drift failures = %+v, want none", failures)
		}
	})

	t.Run("dry_run_reports_without_writes", func(t *testing.T) {
		memStore, cardStore := newMemoryAndCardStores(t)
		if _, err := memStore.Insert(ctx, memory.InsertParams{
			Content:    "Dry run should not create a card mirror",
			Type:       "gotcha",
			Tags:       []string{"dry-run"},
			Source:     "self_report",
			Confidence: 0.7,
		}); err != nil {
			t.Fatalf("insert legacy memory: %v", err)
		}

		n, err := backfillMemoryCardMirrors(ctx, memStore, cardStore, true)
		if err != nil {
			t.Fatalf("dry-run backfill: %v", err)
		}
		if n != 1 {
			t.Fatalf("dry-run count = %d, want 1", n)
		}
		all, err := cardStore.List(ctx, cards.ListQuery{})
		if err != nil {
			t.Fatalf("list after dry-run: %v", err)
		}
		if len(all) != 0 {
			t.Fatalf("dry-run wrote %d card(s), want 0", len(all))
		}
	})

	t.Run("existing_mem_id_mirror_is_skipped", func(t *testing.T) {
		memStore, cardStore := newMemoryAndCardStores(t)
		memID, err := memStore.Insert(ctx, memory.InsertParams{
			Content:    "Existing mirror should make backfill skip this memory",
			Type:       "lesson",
			Tags:       []string{"existing"},
			Source:     "self_report",
			Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert legacy memory: %v", err)
		}
		if _, err := cardStore.Create(ctx, cards.CardCreateParams{
			Type:        cards.CardTypePattern,
			Title:       "already mirrored",
			BodySummary: "already mirrored",
			BodyFull:    "already mirrored",
			Tags:        []string{"legacy_memory_dual_write", fmt.Sprintf("mem-id:%d", memID)},
		}); err != nil {
			t.Fatalf("create existing mirror: %v", err)
		}

		n, err := backfillMemoryCardMirrors(ctx, memStore, cardStore, false)
		if err != nil {
			t.Fatalf("backfill: %v", err)
		}
		if n != 0 {
			t.Fatalf("backfill count = %d, want 0", n)
		}
	})

	t.Run("card_store_errors_include_memory_id", func(t *testing.T) {
		memStore, cardStore := newMemoryAndCardStores(t)
		memID, err := memStore.Insert(ctx, memory.InsertParams{
			Content:    "Create failure should include the memory id",
			Type:       "lesson",
			Tags:       []string{"error"},
			Source:     "self_report",
			Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert legacy memory: %v", err)
		}

		_, err = backfillMemoryCardMirrors(ctx, memStore, &failingCreateCardStore{Store: cardStore}, false)
		if err == nil {
			t.Fatal("backfill error = nil, want create failure")
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("memory %d", memID)) {
			t.Fatalf("error %q does not include memory id %d", err, memID)
		}
	})
}

func TestCheckDriftDoesNotWriteMemoryReadEvents(t *testing.T) {
	ctx := context.Background()
	db, memStore, cardStore := newMemoryAndCardStoresWithDB(t)

	mirroredID, err := memStore.Insert(ctx, memory.InsertParams{
		Content:    "Mirrored memory should not be reported as drift",
		Type:       "lesson",
		Tags:       []string{"drift-check"},
		Source:     "self_report",
		Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert mirrored memory: %v", err)
	}
	driftID, err := memStore.Insert(ctx, memory.InsertParams{
		Content:    "Unmirrored memory should be reported as drift",
		Type:       "gotcha",
		Tags:       []string{"drift-check"},
		Source:     "self_report",
		Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert drift memory: %v", err)
	}
	if _, err := cardStore.Create(ctx, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "already mirrored",
		BodySummary: "already mirrored",
		BodyFull:    "already mirrored",
		Tags:        []string{"legacy_memory_dual_write", fmt.Sprintf("mem-id:%d", mirroredID)},
	}); err != nil {
		t.Fatalf("create mirror card: %v", err)
	}
	clearMemoryReadEvents(t, db)
	before := countMemoryReadEvents(t, db)

	failures, err := checkCardDriftWithoutReadTelemetry(ctx, db, cardStore)
	if err != nil {
		t.Fatalf("check drift: %v", err)
	}
	if got, want := driftMemoryIDs(failures), []int64{driftID}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("drift memory IDs = %v, want %v", got, want)
	}
	if count := countMemoryReadEvents(t, db); count != before {
		t.Fatalf("memory_read_events count after drift check = %d, want %d", count, before)
	}

	if _, err := cardStore.Create(ctx, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "drift now mirrored",
		BodySummary: "drift now mirrored",
		BodyFull:    "drift now mirrored",
		Tags:        []string{"legacy_memory_dual_write", fmt.Sprintf("mem-id:%d", driftID)},
	}); err != nil {
		t.Fatalf("create drift mirror card: %v", err)
	}
	failures, err = checkCardDriftWithoutReadTelemetry(ctx, db, cardStore)
	if err != nil {
		t.Fatalf("check no drift: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("drift failures after mirror = %+v, want none", failures)
	}
	if count := countMemoryReadEvents(t, db); count != before {
		t.Fatalf("memory_read_events count after no-drift check = %d, want %d", count, before)
	}

	bareDB, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open bare db: %v", err)
	}
	t.Cleanup(func() { _ = bareDB.Close() })
	bareDB.SetMaxOpenConns(1)
	if _, err := bareDB.ExecContext(ctx, protocol.MigrateSemanticMemoryReadEvents); err != nil {
		t.Fatalf("migrate read events on bare db: %v", err)
	}
	bareCards, err := cards.NewStore(bareDB)
	if err != nil {
		t.Fatalf("new bare card store: %v", err)
	}
	_, err = checkCardDriftWithoutReadTelemetry(ctx, bareDB, bareCards)
	if err == nil {
		t.Fatal("check drift on missing memories table error = nil, want error")
	}
	if !strings.Contains(err.Error(), "memories") {
		t.Fatalf("missing memories error = %q, want memories context", err)
	}
}

func findCardByTag(all []cards.Card, tag string) *cards.Card {
	for i := range all {
		if containsTag(all[i].Tags, tag) {
			return &all[i]
		}
	}
	return nil
}

type failingCreateCardStore struct {
	cards.Store
}

func (s *failingCreateCardStore) Create(_ context.Context, _ cards.CardCreateParams) (*cards.Card, error) {
	return nil, sql.ErrConnDone
}

func newMemoryAndCardStoresWithDB(t *testing.T) (*sql.DB, *memory.Store, cards.Store) {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("apply memory schema: %v", err)
	}
	cardStore, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("new card store: %v", err)
	}
	return db, memory.NewStore(db), cardStore
}

func clearMemoryReadEvents(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `DELETE FROM memory_read_events`); err != nil {
		t.Fatalf("clear memory_read_events: %v", err)
	}
}

func countMemoryReadEvents(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM memory_read_events`).Scan(&count); err != nil {
		t.Fatalf("count memory_read_events: %v", err)
	}
	return count
}

func driftMemoryIDs(failures []memory.DriftResult) []int64 {
	ids := make([]int64, 0, len(failures))
	for _, f := range failures {
		ids = append(ids, f.MemoryID)
	}
	return ids
}
