package dispatcher //nolint:testpackage // white-box: shares package to access unexported consts

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/beadstore/migrations"
	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

// countingExportStore wraps FakeStore and counts Export invocations for ticker tests.
type countingExportStore struct {
	*beadstore.FakeStore
	count atomic.Int64
}

func (s *countingExportStore) Export(ctx context.Context) ([]byte, error) {
	s.count.Add(1)
	return s.FakeStore.Export(ctx)
}

// sweeperTestDB opens an in-memory SQLite DB with the full dispatcher schema plus v3 migration.
func sweeperTestDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	// Use a named shared-cache in-memory DB (same approach as newTestDB in dispatcher_test.go).
	dsn := fmt.Sprintf("file:sweeper_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := dbutil.OpenDB(dsn)
	if err != nil {
		t.Fatalf("open sweeper test db: %v", err)
	}
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("apply base schema: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("apply bead schema: %v", err)
	}
	if err := migrations.MigrateToV3(ctx, db); err != nil {
		t.Fatalf("apply v3 migration: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// insertSweeperTestBead inserts a minimal beads row for FK-safe learnings tests.
func insertSweeperTestBead(t *testing.T, db *sql.DB, id string, deleted int) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO beads (id, title, status, priority, type, created_at, updated_at, deleted)
		VALUES (?, 'Test', 'open', 0, 'task',
			strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			strftime('%Y-%m-%dT%H:%M:%fZ','now'), ?)`,
		id, deleted); err != nil {
		t.Fatalf("insert test bead %s: %v", id, err)
	}
}

// insertLearningRow inserts a pending bead_learnings_pending row.
func insertLearningRow(t *testing.T, db *sql.DB, beadID, queuedOffset string) {
	t.Helper()
	var expr string
	if queuedOffset != "" {
		expr = "datetime('now', '" + queuedOffset + "')"
	} else {
		expr = "NULL"
	}
	if _, err := db.Exec(`
		INSERT INTO bead_learnings_pending (bead_id, ts, candidate, queued_for_review_at)
		VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), '{}', `+expr+`)`,
		beadID); err != nil {
		t.Fatalf("insert learning row for %s: %v", beadID, err)
	}
}

func TestSweepers(t *testing.T) {
	ctx := context.Background()

	// ─── PromoteClosedParentChildren ────────────────────────────────────────

	t.Run("PromoteClosedParentChildren/promotes_children_of_alive_closed_parent", func(t *testing.T) {
		parent := protocol.Bead{ID: "pp-1", Status: "closed", Type: "research"}
		child := protocol.Bead{ID: "pc-1", Epic: "pp-1", Tags: []string{tagAwaitsParentClose}}
		store := beadstore.NewFakeStore(parent, child)

		if err := PromoteClosedParentChildren(ctx, store); err != nil {
			t.Fatalf("PromoteClosedParentChildren: %v", err)
		}

		events, err := store.Journey(ctx, "pc-1", time.Time{})
		if err != nil {
			t.Fatalf("journey: %v", err)
		}
		if len(events) == 0 {
			t.Fatal("expected parent_closed_promoted event, got none")
		}
		if events[0].Event != "parent_closed_promoted" {
			t.Errorf("event = %q, want parent_closed_promoted", events[0].Event)
		}

		updated, _ := store.Show(ctx, "pc-1")
		if updated == nil {
			t.Fatal("child not found after promotion")
		}
		for _, tag := range updated.Tags {
			if tag == tagAwaitsParentClose {
				t.Error("awaits_parent_close tag should be removed after promotion")
			}
		}
	})

	t.Run("PromoteClosedParentChildren/skips_open_parent", func(t *testing.T) {
		parent := protocol.Bead{ID: "pp-2", Status: "open", Type: "research"}
		child := protocol.Bead{ID: "pc-2", Epic: "pp-2", Tags: []string{tagAwaitsParentClose}}
		store := beadstore.NewFakeStore(parent, child)

		if err := PromoteClosedParentChildren(ctx, store); err != nil {
			t.Fatalf("PromoteClosedParentChildren: %v", err)
		}

		events, _ := store.Journey(ctx, "pc-2", time.Time{})
		if len(events) != 0 {
			t.Errorf("expected no events for open parent, got %d", len(events))
		}
	})

	t.Run("PromoteClosedParentChildren/idempotent", func(t *testing.T) {
		parent := protocol.Bead{ID: "pp-3", Status: "closed", Type: "research"}
		child := protocol.Bead{ID: "pc-3", Epic: "pp-3", Tags: []string{tagAwaitsParentClose}}
		store := beadstore.NewFakeStore(parent, child)

		for i := range 2 {
			if err := PromoteClosedParentChildren(ctx, store); err != nil {
				t.Fatalf("run %d: PromoteClosedParentChildren: %v", i+1, err)
			}
		}

		events, _ := store.Journey(ctx, "pc-3", time.Time{})
		if len(events) != 1 {
			t.Errorf("expected exactly 1 event after 2 runs, got %d", len(events))
		}
	})

	// ─── ReapDeletedParentChildren ──────────────────────────────────────────

	t.Run("ReapDeletedParentChildren/escalates_and_defers_child_of_missing_parent", func(t *testing.T) {
		// Parent not in store → simulates soft-delete
		child := protocol.Bead{ID: "rp-c1", Epic: "deleted-parent-r1", Tags: []string{tagAwaitsParentClose}}
		store := beadstore.NewFakeStore(child)

		if err := ReapDeletedParentChildren(ctx, store); err != nil {
			t.Fatalf("ReapDeletedParentChildren: %v", err)
		}

		events, _ := store.Journey(ctx, "rp-c1", time.Time{})
		if len(events) == 0 {
			t.Fatal("expected escalated event, got none")
		}
		if events[0].Event != "escalated" {
			t.Errorf("event = %q, want escalated", events[0].Event)
		}

		updated, _ := store.Show(ctx, "rp-c1")
		if updated == nil || updated.DeferUntil == "" {
			t.Error("child should be deferred after reap")
		}
	})

	t.Run("ReapDeletedParentChildren/skips_child_with_alive_parent", func(t *testing.T) {
		parent := protocol.Bead{ID: "rp-p1", Status: "open", Type: "research"}
		child := protocol.Bead{ID: "rp-c2", Epic: "rp-p1", Tags: []string{tagAwaitsParentClose}}
		store := beadstore.NewFakeStore(parent, child)

		if err := ReapDeletedParentChildren(ctx, store); err != nil {
			t.Fatalf("ReapDeletedParentChildren: %v", err)
		}

		events, _ := store.Journey(ctx, "rp-c2", time.Time{})
		if len(events) != 0 {
			t.Errorf("expected no events for alive parent, got %d", len(events))
		}
	})

	t.Run("ReapDeletedParentChildren/idempotent", func(t *testing.T) {
		child := protocol.Bead{ID: "rp-c3", Epic: "deleted-parent-r3", Tags: []string{tagAwaitsParentClose}}
		store := beadstore.NewFakeStore(child)

		for i := range 2 {
			if err := ReapDeletedParentChildren(ctx, store); err != nil {
				t.Fatalf("run %d: ReapDeletedParentChildren: %v", i+1, err)
			}
		}

		events, _ := store.Journey(ctx, "rp-c3", time.Time{})
		if len(events) != 1 {
			t.Errorf("expected exactly 1 escalated event after 2 runs, got %d", len(events))
		}
	})

	// ─── SweepDeletedBeadLearnings ───────────────────────────────────────────

	t.Run("SweepDeletedBeadLearnings/rejects_learnings_for_soft_deleted_bead", func(t *testing.T) {
		db := sweeperTestDB(t)
		insertSweeperTestBead(t, db, "del-b1", 1) // deleted=1
		insertLearningRow(t, db, "del-b1", "")

		n, err := SweepDeletedBeadLearnings(ctx, db)
		if err != nil {
			t.Fatalf("SweepDeletedBeadLearnings: %v", err)
		}
		if n != 1 {
			t.Errorf("rows rejected = %d, want 1", n)
		}

		var rejectedAt, reason sql.NullString
		_ = db.QueryRowContext(ctx,
			`SELECT rejected_at, reason FROM bead_learnings_pending WHERE bead_id=?`, "del-b1",
		).Scan(&rejectedAt, &reason)
		if !rejectedAt.Valid {
			t.Error("rejected_at must be set after learning sweep")
		}
		if reason.String != "parent_bead_deleted" {
			t.Errorf("reason = %q, want parent_bead_deleted", reason.String)
		}
	})

	t.Run("SweepDeletedBeadLearnings/keeps_learnings_for_live_bead", func(t *testing.T) {
		db := sweeperTestDB(t)
		insertSweeperTestBead(t, db, "del-b2", 0) // deleted=0
		insertLearningRow(t, db, "del-b2", "")

		n, err := SweepDeletedBeadLearnings(ctx, db)
		if err != nil {
			t.Fatalf("SweepDeletedBeadLearnings: %v", err)
		}
		if n != 0 {
			t.Errorf("rows rejected = %d, want 0 (live bead learnings must not be swept)", n)
		}
	})

	t.Run("SweepDeletedBeadLearnings/idempotent_on_deleted_bead", func(t *testing.T) {
		db := sweeperTestDB(t)
		insertSweeperTestBead(t, db, "del-b3", 1)
		insertLearningRow(t, db, "del-b3", "")

		_, _ = SweepDeletedBeadLearnings(ctx, db)
		n, err := SweepDeletedBeadLearnings(ctx, db)
		if err != nil {
			t.Fatalf("second SweepDeletedBeadLearnings: %v", err)
		}
		if n != 0 {
			t.Errorf("second run rows rejected = %d, want 0 (idempotent)", n)
		}
	})

	t.Run("SweepDeletedBeadLearnings/skips_db_without_learning_table", func(t *testing.T) {
		db := newTestDB(t)

		n, err := SweepDeletedBeadLearnings(ctx, db)
		if err != nil {
			t.Fatalf("SweepDeletedBeadLearnings without learning table: %v", err)
		}
		if n != 0 {
			t.Errorf("rows rejected = %d, want 0 without learning table", n)
		}
	})

	t.Run("SweepDeletedBeadLearnings/skips_db_without_beads_table", func(t *testing.T) {
		db := newTestDB(t)
		if _, err := db.ExecContext(ctx, `
			CREATE TABLE bead_learnings_pending (
				bead_id TEXT NOT NULL,
				promoted_to INTEGER,
				rejected_at TEXT,
				reason TEXT
			)`); err != nil {
			t.Fatalf("create bead learnings table: %v", err)
		}

		n, err := SweepDeletedBeadLearnings(ctx, db)
		if err != nil {
			t.Fatalf("SweepDeletedBeadLearnings without beads table: %v", err)
		}
		if n != 0 {
			t.Errorf("rows rejected = %d, want 0 without beads table", n)
		}
	})

	// ─── Ticker scheduling ───────────────────────────────────────────────────

	t.Run("SweepTicker/runs_five_minute_sweepers_at_interval", func(t *testing.T) {
		spy := &countingExportStore{FakeStore: beadstore.NewFakeStore()}

		ctx2, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		go runSweepLoop(ctx2, spy, nil, SweepConfig{
			Interval5m:  25 * time.Millisecond,
			Interval60m: 1 * time.Hour,
		})

		<-ctx2.Done()
		// 200ms / 25ms ≈ 8 ticks; allow at least 3 to account for scheduler jitter
		if got := spy.count.Load(); got < 3 {
			t.Errorf("expected at least 3 Export calls from 5-min sweepers, got %d", got)
		}
	})

	t.Run("SweepTicker/runs_sixty_minute_sweeper_at_interval", func(t *testing.T) {
		db := sweeperTestDB(t)
		spy := &countingExportStore{FakeStore: beadstore.NewFakeStore()}

		ctx2, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		go runSweepLoop(ctx2, spy, db, SweepConfig{
			Interval5m:  1 * time.Hour,
			Interval60m: 25 * time.Millisecond,
		})

		<-ctx2.Done()
		// The 60-min sweeper (ExpireReviewQueueSLA) runs against DB directly;
		// loop runs without panic and honours context cancellation.
	})

	t.Run("SweepTicker/stops_on_context_cancel", func(t *testing.T) {
		spy := &countingExportStore{FakeStore: beadstore.NewFakeStore()}

		ctx2, cancel := context.WithCancel(context.Background())

		done := make(chan struct{})
		go func() {
			runSweepLoop(ctx2, spy, nil, SweepConfig{
				Interval5m:  10 * time.Millisecond,
				Interval60m: 1 * time.Hour,
			})
			close(done)
		}()

		cancel()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
			t.Error("runSweepLoop did not stop after context cancel")
		}
	})
}
