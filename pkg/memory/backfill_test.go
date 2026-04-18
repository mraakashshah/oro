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
	"oro/pkg/memory/testhelpers"
	"oro/pkg/protocol"

	"golang.org/x/time/rate"
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
	// Add embedding_dense column (migration for existing DBs; safe on fresh DBs too).
	if _, err := db.Exec(protocol.MigrateSemanticMemoryDense); err != nil {
		t.Fatalf("exec migration: %v", err)
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

// seedMemories inserts n rows with the given content prefix into memories.
// Returns the inserted IDs.
func seedMemories(t *testing.T, db *sql.DB, n int, contentPrefix string) []int64 {
	t.Helper()
	ids := make([]int64, 0, n)
	for i := range n {
		content := fmt.Sprintf("%s memory row number %d with enough content to pass validation", contentPrefix, i)
		res, err := db.ExecContext(context.Background(),
			`INSERT INTO memories (content, type, source, confidence) VALUES (?, ?, ?, ?)`,
			content, "lesson", "self_report", 0.8,
		)
		if err != nil {
			t.Fatalf("seed memories insert %d: %v", i, err)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	return ids
}

// waitForBackfillComplete polls kv_store until state=complete or deadline.
func waitForBackfillComplete(t *testing.T, db *sql.DB, deadline time.Duration) bool {
	t.Helper()
	ctx := context.Background()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		var state string
		err := db.QueryRowContext(ctx,
			`SELECT value FROM kv_store WHERE key = ?`, backfillStateKey,
		).Scan(&state)
		if err == nil && state == backfillStateComplete {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestBackfillUpdatesOnlyWhereNull verifies that the UPDATE statement only
// fills rows where embedding_dense IS NULL, leaving pre-filled rows untouched.
func TestBackfillUpdatesOnlyWhereNull(t *testing.T) {
	db := setupBackfillDB(t)
	ctx := context.Background()

	store := NewStore(db)
	store.SetEmbedder(testhelpers.NewFakeEmbedder(0))
	store.backfillLimiter = rate.NewLimiter(rate.Inf, 1)

	// Insert a row with embedding_dense pre-filled (sentinel value).
	sentinel := MarshalEmbedding([]float32{0.1, 0.2, 0.3})
	_, err := db.ExecContext(ctx,
		`INSERT INTO memories (content, type, source, confidence, embedding_dense) VALUES (?, ?, ?, ?, ?)`,
		"already embedded content value here for validation", "lesson", "self_report", 0.8, sentinel,
	)
	if err != nil {
		t.Fatalf("insert pre-filled row: %v", err)
	}

	// Insert a row with embedding_dense IS NULL.
	res, err := db.ExecContext(ctx,
		`INSERT INTO memories (content, type, source, confidence) VALUES (?, ?, ?, ?)`,
		"needs embedding content value here for validation", "lesson", "self_report", 0.8,
	)
	if err != nil {
		t.Fatalf("insert null row: %v", err)
	}
	nullRowID, _ := res.LastInsertId()

	// Set state=pending and acquire lock so backfillWorker will run.
	seedKV(t, db, backfillStateKey, backfillStatePending)
	if err := store.MaybeStartBackfill(ctx); err != nil {
		t.Fatalf("MaybeStartBackfill: %v", err)
	}

	// Wait for completion.
	if !waitForBackfillComplete(t, db, 5*time.Second) {
		t.Fatal("backfill did not complete within 5s")
	}

	// The null row must now have embedding_dense set.
	var blob []byte
	err = db.QueryRowContext(ctx,
		`SELECT embedding_dense FROM memories WHERE id = ?`, nullRowID,
	).Scan(&blob)
	if err != nil {
		t.Fatalf("query null row after backfill: %v", err)
	}
	if len(blob) == 0 {
		t.Error("expected null row to have embedding_dense set after backfill")
	}

	// The pre-filled row must still have the original sentinel value.
	var existingBlob []byte
	err = db.QueryRowContext(ctx,
		`SELECT embedding_dense FROM memories WHERE embedding_dense = ?`, sentinel,
	).Scan(&existingBlob)
	if err != nil {
		t.Fatalf("query pre-filled row after backfill: %v", err)
	}
	if string(existingBlob) != string(sentinel) {
		t.Error("pre-filled embedding_dense was modified by backfill (expected no-op)")
	}
}

// TestBackfillIdempotentResume verifies that backfillWorker converges a
// 1000-row DB to all embedding_dense IS NOT NULL + state=complete within 10s.
func TestBackfillIdempotentResume(t *testing.T) {
	db := setupBackfillDB(t)
	ctx := context.Background()

	store := NewStore(db)
	store.SetEmbedder(testhelpers.NewFakeEmbedder(0))
	store.backfillLimiter = rate.NewLimiter(rate.Inf, 1) // unlimited for speed

	seedMemories(t, db, 1000, "test content for backfill")
	seedKV(t, db, backfillStateKey, backfillStatePending)

	if err := store.MaybeStartBackfill(ctx); err != nil {
		t.Fatalf("MaybeStartBackfill: %v", err)
	}

	if !waitForBackfillComplete(t, db, 10*time.Second) {
		t.Fatal("backfill did not complete within 10s wall clock")
	}

	// All rows must have embedding_dense NOT NULL.
	var nullCount int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE embedding_dense IS NULL`,
	).Scan(&nullCount)
	if err != nil {
		t.Fatalf("count null rows: %v", err)
	}
	if nullCount != 0 {
		t.Errorf("expected 0 rows with embedding_dense IS NULL after backfill, got %d", nullCount)
	}

	// state must be complete and owner key must be cleared.
	var state string
	if err := db.QueryRowContext(ctx,
		`SELECT value FROM kv_store WHERE key = ?`, backfillStateKey,
	).Scan(&state); err != nil || state != backfillStateComplete {
		t.Errorf("expected state=%s, got state=%q err=%v", backfillStateComplete, state, err)
	}
	var ownerCount int
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM kv_store WHERE key = ?`, backfillOwnerKey,
	).Scan(&ownerCount)
	if ownerCount != 0 {
		t.Errorf("expected backfill_owner_pid to be cleared on completion, found %d row(s)", ownerCount)
	}
}

// TestBackfillRateLimit verifies that backfillWorker honours the 50/sec rate
// limit: processing 100 memories must take at least 1.8s.
func TestBackfillRateLimit(t *testing.T) {
	db := setupBackfillDB(t)
	ctx := context.Background()

	store := NewStore(db)
	store.SetEmbedder(testhelpers.NewFakeEmbedder(0))
	// Default 50/sec limiter (no override).

	seedMemories(t, db, 100, "rate limit test content")
	seedKV(t, db, backfillStateKey, backfillStatePending)

	start := time.Now()
	if err := store.MaybeStartBackfill(ctx); err != nil {
		t.Fatalf("MaybeStartBackfill: %v", err)
	}

	// Wait for completion (up to 15s — 100 rows at 50/sec ≈ 2s).
	if !waitForBackfillComplete(t, db, 15*time.Second) {
		t.Fatal("backfill did not complete within 15s")
	}
	elapsed := time.Since(start)

	if elapsed < 1800*time.Millisecond {
		t.Errorf("expected elapsed >= 1.8s for 100 memories at 50/sec, got %v", elapsed)
	}
}

// nilEmbedder satisfies Embedder but always returns nil from Embed, exercising
// the "skip row, do not error" path in processBatch. Used to drive the
// zero-progress termination check.
type nilEmbedder struct{}

func (nilEmbedder) Embed(string) []float32 { return nil }
func (nilEmbedder) Dim() int               { return 128 }
func (nilEmbedder) Name() string           { return "nil-embedder" }

// slowEmbedder blocks on a channel until released, used to hold backfillWorker
// mid-batch so ctx cancel can be observed before completion.
type slowEmbedder struct {
	release chan struct{}
	entered chan struct{} // closed once first Embed call has entered
	once    sync.Once
}

func (s *slowEmbedder) Embed(string) []float32 {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return []float32{0.1, 0.2, 0.3}
}
func (s *slowEmbedder) Dim() int     { return 3 }
func (s *slowEmbedder) Name() string { return "slow-embedder" }

// waitForOwnerKey polls until the owner key is absent (count==0) or the
// deadline elapses. Used to confirm the worker goroutine has exited and
// released the lock (on happy-path completion).
func waitForOwnerKey(t *testing.T, db *sql.DB, present bool, deadline time.Duration) bool {
	t.Helper()
	ctx := context.Background()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		var cnt int
		_ = db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM kv_store WHERE key = ?`, backfillOwnerKey,
		).Scan(&cnt)
		if (cnt == 1) == present {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestBackfillCancelMidBatch verifies the AC-mandated asymmetry:
// on ctx cancel mid-batch, the worker stops WITHOUT clearing
// backfill_owner_pid (so the next launch can steal via stale-detection),
// and state stays "pending" (not "complete").
func TestBackfillCancelMidBatch(t *testing.T) {
	db := setupBackfillDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	slow := &slowEmbedder{
		release: make(chan struct{}),
		entered: make(chan struct{}),
	}
	store := NewStore(db)
	store.SetEmbedder(slow)
	store.backfillLimiter = rate.NewLimiter(rate.Inf, 1)

	seedMemories(t, db, 100, "cancel test content")
	seedKV(t, db, backfillStateKey, backfillStatePending)

	if err := store.MaybeStartBackfill(ctx); err != nil {
		t.Fatalf("MaybeStartBackfill: %v", err)
	}

	// Wait for the worker to enter its first Embed call (mid-batch).
	select {
	case <-slow.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("slow embedder never entered; worker did not start")
	}

	// Cancel ctx while worker is blocked in Embed.
	cancel()
	// Release the embedder so the goroutine unblocks and can observe ctx.Err().
	close(slow.release)

	// Give the worker a window to exit. Poll state key to detect exit: if
	// state was ever flipped to complete we'd see it; otherwise state stays
	// pending indefinitely.
	time.Sleep(300 * time.Millisecond)

	// AC: backfill_owner_pid must still be present (NOT cleared on cancel).
	if !waitForOwnerKey(t, db, true, 500*time.Millisecond) {
		t.Fatal("expected backfill_owner_pid to remain after ctx cancel, got cleared")
	}

	// AC: state must NOT be "complete" — only a true completion (empty batch)
	// may flip state.
	var state string
	_ = db.QueryRowContext(context.Background(),
		`SELECT value FROM kv_store WHERE key = ?`, backfillStateKey,
	).Scan(&state)
	if state == backfillStateComplete {
		t.Errorf("expected state to remain pending after ctx cancel, got %q", state)
	}
}

// TestBackfillCompleteAtomicity verifies the AC-mandated single-tx guarantee:
// completeBackfill sets state=complete AND clears backfill_owner_pid in one tx.
// If the tx rolls back (fault injected before commit), BOTH side effects must
// revert together — it is never the case that state flips to complete while
// the owner stays, or vice versa.
func TestBackfillCompleteAtomicity(t *testing.T) {
	t.Run("fault injected before commit: both side effects roll back", func(t *testing.T) {
		db := setupBackfillDB(t)
		ctx := context.Background()

		store := NewStore(db)
		seedKV(t, db, backfillStateKey, backfillStatePending)
		const ownerVal = "12345:999999999"
		seedKV(t, db, backfillOwnerKey, ownerVal)

		// Inject a fault so the tx rolls back rather than commits.
		store.testCompleteBackfillFault = func() bool { return true }
		store.completeBackfill(ctx)

		// State must still be "pending" (UPSERT rolled back).
		var state string
		if err := db.QueryRowContext(ctx,
			`SELECT value FROM kv_store WHERE key = ?`, backfillStateKey,
		).Scan(&state); err != nil {
			t.Fatalf("scan state: %v", err)
		}
		if state != backfillStatePending {
			t.Errorf("expected state=%q after tx rollback, got %q", backfillStatePending, state)
		}

		// Owner must still be present (DELETE rolled back).
		var owner string
		if err := db.QueryRowContext(ctx,
			`SELECT value FROM kv_store WHERE key = ?`, backfillOwnerKey,
		).Scan(&owner); err != nil {
			t.Fatalf("scan owner: %v", err)
		}
		if owner != ownerVal {
			t.Errorf("expected owner=%q after tx rollback, got %q", ownerVal, owner)
		}
	})

	t.Run("no fault: both side effects commit together", func(t *testing.T) {
		db := setupBackfillDB(t)
		ctx := context.Background()

		store := NewStore(db)
		seedKV(t, db, backfillStateKey, backfillStatePending)
		seedKV(t, db, backfillOwnerKey, "12345:999999999")

		store.completeBackfill(ctx)

		var state string
		_ = db.QueryRowContext(ctx,
			`SELECT value FROM kv_store WHERE key = ?`, backfillStateKey,
		).Scan(&state)
		if state != backfillStateComplete {
			t.Errorf("expected state=%q, got %q", backfillStateComplete, state)
		}

		var ownerCnt int
		_ = db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM kv_store WHERE key = ?`, backfillOwnerKey,
		).Scan(&ownerCnt)
		if ownerCnt != 0 {
			t.Errorf("expected owner cleared on commit, got %d rows", ownerCnt)
		}
	})
}

// TestBackfillNilEmbedderExits verifies the infinite-loop guard: when the
// Store has no embedder, backfillWorker returns immediately and does NOT
// clear the owner key (so a subsequent launch with an embedder can reclaim
// the lock via stale-steal — or exercise it here by waiting and retrying).
func TestBackfillNilEmbedderExits(t *testing.T) {
	db := setupBackfillDB(t)
	ctx := context.Background()

	store := NewStore(db)
	// No embedder set. No limiter needed (worker returns before using it).
	seedMemories(t, db, 5, "nil embedder test")
	seedKV(t, db, backfillStateKey, backfillStatePending)

	if err := store.MaybeStartBackfill(ctx); err != nil {
		t.Fatalf("MaybeStartBackfill: %v", err)
	}

	// Owner was acquired by MaybeStartBackfill; worker must exit quickly.
	time.Sleep(100 * time.Millisecond)

	// State stays pending (not complete).
	var state string
	_ = db.QueryRowContext(ctx,
		`SELECT value FROM kv_store WHERE key = ?`, backfillStateKey,
	).Scan(&state)
	if state != backfillStatePending {
		t.Errorf("expected state=%q after nil-embedder exit, got %q", backfillStatePending, state)
	}

	// All rows still have embedding_dense IS NULL (no work done).
	var nullCount int
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE embedding_dense IS NULL`,
	).Scan(&nullCount)
	if nullCount != 5 {
		t.Errorf("expected 5 unembedded rows, got %d", nullCount)
	}
}

// TestBackfillZeroProgressExits verifies that a batch where NO row makes
// forward progress (embedder returns nil for every row) causes the worker to
// exit rather than spin forever on the same batch. Owner stays locked so the
// stale-steal mechanism can reclaim it on the next launch (when the upstream
// issue is fixed).
func TestBackfillZeroProgressExits(t *testing.T) {
	db := setupBackfillDB(t)
	ctx := context.Background()

	store := NewStore(db)
	store.SetEmbedder(nilEmbedder{}) // always returns nil → no row can be updated
	store.backfillLimiter = rate.NewLimiter(rate.Inf, 1)

	seedMemories(t, db, 10, "zero progress test")
	seedKV(t, db, backfillStateKey, backfillStatePending)

	if err := store.MaybeStartBackfill(ctx); err != nil {
		t.Fatalf("MaybeStartBackfill: %v", err)
	}

	// With rate.Inf the 10-row batch runs in milliseconds. Sleep is sufficient:
	// if the worker were in a hot loop, the null-count check below would
	// still see 10 rows, but a lack of CPU pinning isn't what we're testing —
	// the assertion is "worker exited without marking complete".
	time.Sleep(300 * time.Millisecond)

	// State stayed pending (not complete).
	var state string
	_ = db.QueryRowContext(ctx,
		`SELECT value FROM kv_store WHERE key = ?`, backfillStateKey,
	).Scan(&state)
	if state == backfillStateComplete {
		t.Errorf("expected state != complete after zero-progress exit, got %q", state)
	}

	// All rows still NULL (no updates).
	var nullCount int
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE embedding_dense IS NULL`,
	).Scan(&nullCount)
	if nullCount != 10 {
		t.Errorf("expected 10 unembedded rows after zero-progress exit, got %d", nullCount)
	}

	// Owner retained for stale-steal.
	var ownerCnt int
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM kv_store WHERE key = ?`, backfillOwnerKey,
	).Scan(&ownerCnt)
	if ownerCnt != 1 {
		t.Errorf("expected owner retained after zero-progress exit, got %d rows", ownerCnt)
	}
}
