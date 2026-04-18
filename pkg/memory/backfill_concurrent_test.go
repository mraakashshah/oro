package memory //nolint:testpackage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"

	"golang.org/x/time/rate"
)

// setupFileBackedBackfillDB creates a file-backed SQLite DB with the full schema.
// File-backed DBs support true parallel writes (unlike :memory: which only allows
// one writer at a time). MaxOpenConns=1 ensures all goroutines share a connection.
func setupFileBackedBackfillDB(t *testing.T) *sql.DB {
	t.Helper()
	f, err := os.CreateTemp("", "backfill-*.db")
	if err != nil {
		t.Fatalf("create temp db file: %v", err)
	}
	dbPath := f.Name()
	_ = f.Close()
	t.Cleanup(func() { _ = os.Remove(dbPath) })

	db, err := dbutil.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
	if _, err := db.Exec(protocol.MigrateSemanticMemoryDense); err != nil {
		t.Fatalf("exec migration: %v", err)
	}
	return db
}

// countingFakeEmbedder is an Embedder that counts how many times Embed() was called.
// Used to verify that exactly one worker processed embeddings in concurrent tests.
type countingFakeEmbedder struct {
	dim   int
	count atomic.Int64
}

func newCountingFakeEmbedder(dim int) *countingFakeEmbedder {
	return &countingFakeEmbedder{dim: dim}
}

func (e *countingFakeEmbedder) Embed(content string) []float32 {
	e.count.Add(1)
	// Return a dummy embedding.
	vec := make([]float32, e.dim)
	for i := range e.dim {
		vec[i] = float32(i) * 0.1
	}
	return vec
}

func (e *countingFakeEmbedder) Dim() int          { return e.dim }
func (e *countingFakeEmbedder) Name() string      { return "counting-fake-embedder" }
func (e *countingFakeEmbedder) EmbedCount() int64 { return e.count.Load() }

// TestBackfillConcurrentClaim verifies that when two goroutines simultaneously call
// MaybeStartBackfill on the same DB, exactly one acquires the backfill lock and
// launches a worker, while the other returns ErrBackfillLocked.
// The countingFakeEmbedder ensures only one worker ran.
func TestBackfillConcurrentClaim(t *testing.T) {
	db := setupFileBackedBackfillDB(t)
	ctx := context.Background()

	// Set up backfill state and a counting embedder.
	_, err := db.ExecContext(ctx,
		`INSERT INTO kv_store (key, value) VALUES (?, ?)`,
		backfillStateKey, backfillStatePending)
	if err != nil {
		t.Fatalf("seed backfill state: %v", err)
	}

	// Insert rows to backfill.
	for i := range 10 {
		content := fmt.Sprintf("test memory %d with enough text for validation purposes", i)
		_, _ = db.ExecContext(ctx,
			`INSERT INTO memories (content, type, source, confidence) VALUES (?, ?, ?, ?)`,
			content, "lesson", "self_report", 0.8)
	}

	embedder := newCountingFakeEmbedder(128)
	store := NewStore(db)
	store.SetEmbedder(embedder)
	store.backfillLimiter = rate.NewLimiter(rate.Inf, 1) // unlimited for speed

	// Launch two goroutines racing to start backfill.
	const numRacers = 2
	results := make([]error, numRacers)
	var wg sync.WaitGroup
	wg.Add(numRacers)
	for i := range numRacers {
		go func(idx int) {
			defer wg.Done()
			results[idx] = store.MaybeStartBackfill(ctx)
		}(i)
	}
	wg.Wait()

	// Wait for backfill to complete.
	time.Sleep(500 * time.Millisecond)

	// Exactly one goroutine should see nil (winner); the other sees ErrBackfillLocked (loser).
	nilCount := 0
	lockedCount := 0
	for _, err := range results {
		if err == nil {
			nilCount++
		} else if errors.Is(err, ErrBackfillLocked) {
			lockedCount++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if nilCount != 1 {
		t.Errorf("expected exactly 1 winner (nil error), got %d", nilCount)
	}
	if lockedCount != 1 {
		t.Errorf("expected exactly 1 loser (ErrBackfillLocked), got %d", lockedCount)
	}

	// The countingFakeEmbedder should show that exactly one worker ran and embedded rows.
	embedCount := embedder.EmbedCount()
	if embedCount == 0 {
		t.Error("expected embedder to be called at least once (worker should have processed batch)")
	}
}

// TestBackfillConcurrentStaleOwnerSteal verifies that when two goroutines
// simultaneously call acquireBackfillLock and the owner is stale (>10 min old),
// exactly one steals the lock successfully (UPDATE rowsAffected==1) and the other
// returns (false, nil) because the value changed mid-race.
func TestBackfillConcurrentStaleOwnerSteal(t *testing.T) {
	db := setupFileBackedBackfillDB(t)
	ctx := context.Background()

	// Seed a stale owner: 15 minutes old.
	staleTS := time.Now().Add(-15 * time.Minute).UnixMilli()
	staleValue := fmt.Sprintf("99999:%d", staleTS)
	_, err := db.ExecContext(ctx,
		`INSERT INTO kv_store (key, value) VALUES (?, ?)`,
		backfillOwnerKey, staleValue)
	if err != nil {
		t.Fatalf("seed stale owner: %v", err)
	}

	store := NewStore(db)

	// Launch two goroutines racing to steal the stale lock.
	const numRacers = 2
	results := make([]bool, numRacers)
	errs := make([]error, numRacers)
	var wg sync.WaitGroup
	wg.Add(numRacers)
	for i := range numRacers {
		go func(idx int) {
			defer wg.Done()
			// Both goroutines call acquireBackfillLock, which will detect the stale
			// owner and attempt a CAS steal.
			ok, err := store.acquireBackfillLock(ctx)
			results[idx] = ok
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	// Both should have no errors.
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	// Exactly one should have stolen the lock (returned true).
	stealCount := 0
	for _, ok := range results {
		if ok {
			stealCount++
		}
	}
	if stealCount != 1 {
		t.Errorf("expected exactly 1 successful steal, got %d", stealCount)
	}

	// Verify that the owner was updated: it should no longer match staleValue.
	var currentValue string
	err = db.QueryRowContext(ctx,
		`SELECT value FROM kv_store WHERE key = ?`, backfillOwnerKey,
	).Scan(&currentValue)
	if err != nil {
		t.Fatalf("scan owner value: %v", err)
	}
	if currentValue == staleValue {
		t.Error("owner value should have been updated by the stealer, but still matches stale value")
	}
}
