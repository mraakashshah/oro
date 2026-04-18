package memory //nolint:testpackage // white-box: tests unexported acquireBackfillLock / stealStaleBackfillLock

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

// setupBackfillDB creates an in-memory SQLite DB with the full schema.
// MaxOpenConns=1 ensures all goroutines share a single connection so the
// in-memory database is visible across goroutine boundaries.
func setupBackfillDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
	return db
}

// seedKV inserts a single row into kv_store.
func seedKV(t *testing.T, db *sql.DB, key, value string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO kv_store (key, value) VALUES (?, ?)`, key, value)
	if err != nil {
		t.Fatalf("seed kv %s=%s: %v", key, value, err)
	}
}

// TestBackfillCASOwnerLockAcquires verifies that acquireBackfillLock inserts
// the owner key via INSERT OR IGNORE (rows_affected==1) and returns (true,nil)
// when the key does not yet exist.
func TestBackfillCASOwnerLockAcquires(t *testing.T) {
	t.Run("fresh key acquires lock", func(t *testing.T) {
		db := setupBackfillDB(t)
		store := NewStore(db)
		ctx := context.Background()

		ok, err := store.acquireBackfillLock(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Error("expected lock acquired (true), got false")
		}

		var value string
		err = db.QueryRowContext(ctx,
			`SELECT value FROM kv_store WHERE key = ?`, backfillOwnerKey,
		).Scan(&value)
		if err != nil {
			t.Fatalf("owner key not found after acquire: %v", err)
		}
		if value == "" {
			t.Error("owner value must be non-empty (PID:TS)")
		}
	})

	t.Run("MaybeStartBackfill with state=pending acquires lock", func(t *testing.T) {
		db := setupBackfillDB(t)
		store := NewStore(db)
		ctx := context.Background()

		seedKV(t, db, backfillStateKey, backfillStatePending)

		err := store.MaybeStartBackfill(ctx)
		if err != nil {
			t.Fatalf("expected nil, got: %v", err)
		}
	})

	t.Run("MaybeStartBackfill with state!=pending is a no-op", func(t *testing.T) {
		db := setupBackfillDB(t)
		store := NewStore(db)
		ctx := context.Background()

		seedKV(t, db, backfillStateKey, "done")

		err := store.MaybeStartBackfill(ctx)
		if err != nil {
			t.Fatalf("expected nil for non-pending state, got: %v", err)
		}

		// Owner key must NOT have been inserted.
		var cnt int
		_ = db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM kv_store WHERE key = ?`, backfillOwnerKey,
		).Scan(&cnt)
		if cnt != 0 {
			t.Errorf("owner key should not exist when state!=pending, found %d row(s)", cnt)
		}
	})

	t.Run("MaybeStartBackfill with no state row is a no-op", func(t *testing.T) {
		db := setupBackfillDB(t)
		store := NewStore(db)
		ctx := context.Background()

		err := store.MaybeStartBackfill(ctx)
		if err != nil {
			t.Fatalf("expected nil when no state row, got: %v", err)
		}
	})

	t.Run("ctx cancellation returns ctx.Err()", func(t *testing.T) {
		db := setupBackfillDB(t)
		store := NewStore(db)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		_, err := store.acquireBackfillLock(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	})
}

// TestBackfillCASLoserBacksOff verifies that a second acquireBackfillLock call
// returns (false, nil) — not an error — when a fresh (non-stale) owner exists.
// The public MaybeStartBackfill should map this to ErrBackfillLocked.
func TestBackfillCASLoserBacksOff(t *testing.T) {
	t.Run("non-stale owner present → returns (false, nil)", func(t *testing.T) {
		db := setupBackfillDB(t)
		store := NewStore(db)
		ctx := context.Background()

		freshTS := time.Now().UnixMilli()
		freshValue := fmt.Sprintf("12345:%d", freshTS)
		seedKV(t, db, backfillOwnerKey, freshValue)

		ok, err := store.acquireBackfillLock(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Error("expected lock NOT acquired (false), got true")
		}
	})

	t.Run("MaybeStartBackfill returns ErrBackfillLocked when lock taken", func(t *testing.T) {
		db := setupBackfillDB(t)
		store := NewStore(db)
		ctx := context.Background()

		seedKV(t, db, backfillStateKey, backfillStatePending)

		freshTS := time.Now().UnixMilli()
		freshValue := fmt.Sprintf("12345:%d", freshTS)
		seedKV(t, db, backfillOwnerKey, freshValue)

		err := store.MaybeStartBackfill(ctx)
		if !errors.Is(err, ErrBackfillLocked) {
			t.Errorf("expected ErrBackfillLocked, got: %v", err)
		}
	})
}

// TestBackfillStaleOwnerStealRowsAffected verifies CAS steal semantics:
// two goroutines both call stealStaleBackfillLock with the same oldValue;
// exactly one should succeed (rows_affected==1).
func TestBackfillStaleOwnerStealRowsAffected(t *testing.T) {
	t.Run("exactly one winner across two racing goroutines", func(t *testing.T) {
		db := setupBackfillDB(t)
		store := NewStore(db)
		ctx := context.Background()

		staleTS := time.Now().Add(-15 * time.Minute).UnixMilli()
		staleValue := fmt.Sprintf("99999:%d", staleTS)
		seedKV(t, db, backfillOwnerKey, staleValue)

		const racers = 2
		wins := make([]bool, racers)
		errs := make([]error, racers)
		var wg sync.WaitGroup
		wg.Add(racers)
		for i := range racers {
			go func(idx int) {
				defer wg.Done()
				wins[idx], errs[idx] = store.stealStaleBackfillLock(ctx, staleValue)
			}(i)
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Errorf("goroutine %d: unexpected error: %v", i, err)
			}
		}

		total := 0
		for _, ok := range wins {
			if ok {
				total++
			}
		}
		if total != 1 {
			t.Errorf("expected exactly 1 winner, got %d (wins=%v)", total, wins)
		}
	})

	t.Run("stale detection: timestamp older than threshold", func(t *testing.T) {
		staleTS := time.Now().Add(-15 * time.Minute).UnixMilli()
		staleValue := fmt.Sprintf("42:%d", staleTS)
		if !isStaleOwner(staleValue) {
			t.Error("expected 15-minute-old owner to be stale")
		}
	})

	t.Run("stale detection: fresh timestamp is not stale", func(t *testing.T) {
		freshTS := time.Now().UnixMilli()
		freshValue := fmt.Sprintf("42:%d", freshTS)
		if isStaleOwner(freshValue) {
			t.Error("expected fresh owner to not be stale")
		}
	})

	t.Run("stale detection: parse failure treated as stale", func(t *testing.T) {
		if !isStaleOwner("bad-value") {
			t.Error("expected parse failure to be treated as stale")
		}
		if !isStaleOwner("nodots") {
			t.Error("expected no-colon value to be treated as stale")
		}
		if !isStaleOwner("pid:notanumber") {
			t.Error("expected non-numeric ts to be treated as stale")
		}
	})

	t.Run("acquireBackfillLock steals stale owner", func(t *testing.T) {
		db := setupBackfillDB(t)
		store := NewStore(db)
		ctx := context.Background()

		staleTS := time.Now().Add(-11 * time.Minute).UnixMilli()
		staleValue := fmt.Sprintf("99999:%d", staleTS)
		seedKV(t, db, backfillOwnerKey, staleValue)

		ok, err := store.acquireBackfillLock(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Error("expected stale lock to be stolen (true), got false")
		}
	})
}
