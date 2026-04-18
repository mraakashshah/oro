package memory_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/memory"
)

const createSearchEventsTable = `
CREATE TABLE IF NOT EXISTS memory_search_events (
	id INTEGER PRIMARY KEY,
	ts DATETIME NOT NULL DEFAULT (datetime('now')),
	project TEXT,
	query_hash TEXT,
	top_k_ids TEXT,
	top_k_scores TEXT,
	latency_ms INTEGER,
	used_rerank INTEGER DEFAULT 0,
	used_bge INTEGER DEFAULT 0,
	ann_candidates INTEGER
);
CREATE INDEX IF NOT EXISTS idx_mse_ts ON memory_search_events(ts);
`

func setupRetentionDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func Test30DayRetention(t *testing.T) {
	ctx := context.Background()

	t.Run("empty table returns (0, nil)", func(t *testing.T) {
		db := setupRetentionDB(t)
		if _, err := db.Exec(createSearchEventsTable); err != nil {
			t.Fatalf("create table: %v", err)
		}

		n, err := memory.TrimSearchEvents(ctx, db, 30*24*time.Hour)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 0 {
			t.Errorf("expected 0 deleted, got %d", n)
		}
	})

	t.Run("maxAge<=0 returns (0, nil) without touching table", func(t *testing.T) {
		db := setupRetentionDB(t)
		// No table created — if DELETE ran it would error; maxAge<=0 must return early.

		n, err := memory.TrimSearchEvents(ctx, db, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 0 {
			t.Errorf("expected 0 deleted, got %d", n)
		}

		n, err = memory.TrimSearchEvents(ctx, db, -time.Hour)
		if err != nil {
			t.Fatalf("unexpected error for negative maxAge: %v", err)
		}
		if n != 0 {
			t.Errorf("expected 0 deleted for negative maxAge, got %d", n)
		}
	})

	t.Run("deletes old rows and retains newer rows", func(t *testing.T) {
		db := setupRetentionDB(t)
		if _, err := db.Exec(createSearchEventsTable); err != nil {
			t.Fatalf("create table: %v", err)
		}

		// Insert one row that is 40 days old (should be deleted).
		_, err := db.ExecContext(ctx,
			`INSERT INTO memory_search_events (ts) VALUES (datetime('now', '-40 days'))`)
		if err != nil {
			t.Fatalf("insert old row: %v", err)
		}

		// Insert one row that is 20 days old (should be retained).
		_, err = db.ExecContext(ctx,
			`INSERT INTO memory_search_events (ts) VALUES (datetime('now', '-20 days'))`)
		if err != nil {
			t.Fatalf("insert recent row: %v", err)
		}

		// Insert one row that is exactly now (should be retained).
		_, err = db.ExecContext(ctx,
			`INSERT INTO memory_search_events (ts) VALUES (datetime('now'))`)
		if err != nil {
			t.Fatalf("insert now row: %v", err)
		}

		n, err := memory.TrimSearchEvents(ctx, db, 30*24*time.Hour)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 1 {
			t.Errorf("expected 1 deleted, got %d", n)
		}

		var remaining int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_search_events`).Scan(&remaining); err != nil {
			t.Fatalf("count: %v", err)
		}
		if remaining != 2 {
			t.Errorf("expected 2 remaining rows, got %d", remaining)
		}
	})

	t.Run("missing table returns error", func(t *testing.T) {
		db := setupRetentionDB(t)
		// Do NOT create the table.

		_, err := memory.TrimSearchEvents(ctx, db, 30*24*time.Hour)
		if err == nil {
			t.Fatal("expected error for missing table, got nil")
		}
	})

	t.Run("all old rows deleted", func(t *testing.T) {
		db := setupRetentionDB(t)
		if _, err := db.Exec(createSearchEventsTable); err != nil {
			t.Fatalf("create table: %v", err)
		}

		// Insert two old rows (both >30 days).
		for _, days := range []int{31, 60} {
			_, err := db.ExecContext(ctx,
				`INSERT INTO memory_search_events (ts) VALUES (datetime('now', ? || ' days'))`,
				-days)
			if err != nil {
				t.Fatalf("insert %d-day-old row: %v", days, err)
			}
		}

		n, err := memory.TrimSearchEvents(ctx, db, 30*24*time.Hour)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 2 {
			t.Errorf("expected 2 deleted, got %d", n)
		}
	})
}
