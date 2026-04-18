package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// ErrBackfillLocked is returned by MaybeStartBackfill when another process
// holds the backfill owner lock and it is not yet stale.
var ErrBackfillLocked = errors.New("backfill already running on another process")

const (
	backfillStateKey      = "backfill_semantic_memory_state"
	backfillOwnerKey      = "backfill_owner_pid"
	backfillStatePending  = "pending"
	backfillStateComplete = "complete"
	backfillBatchSize     = 100
	staleThreshold        = 10 * time.Minute
)

// BackfillStarter is satisfied by any *Store — the interface declaration
// provides a non-test production reference to MaybeStartBackfill, which is
// wired by the next bead.
type BackfillStarter interface {
	MaybeStartBackfill(ctx context.Context) error
}

// MaybeStartBackfill is the public entry point for the backfill lock protocol.
// It returns nil immediately when no backfill is pending. It returns
// ErrBackfillLocked when another process holds a fresh lock.
func (s *Store) MaybeStartBackfill(ctx context.Context) error {
	var state string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM kv_store WHERE key = ?`, backfillStateKey,
	).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // no state row → nothing to backfill
	}
	if err != nil {
		return fmt.Errorf("backfill state query: %w", err)
	}
	if state != backfillStatePending {
		return nil
	}

	ok, err := s.acquireBackfillLock(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBackfillLocked
	}
	go s.backfillWorker(ctx)
	return nil
}

// backfillRow holds a single memory row to be embedded.
type backfillRow struct {
	id      int64
	content string
}

// backfillWorker processes memories without embedding_dense in batches,
// computing and storing embeddings at up to 50/sec. On an empty batch it
// sets state=complete and clears the owner PID in a single transaction.
// On ctx cancellation it stops gracefully without clearing the owner so
// the next launch can steal the stale lock.
func (s *Store) backfillWorker(ctx context.Context) {
	lim := s.backfillLimiter
	if lim == nil {
		lim = rate.NewLimiter(rate.Limit(50), 1)
	}
	chunksTableMissing := false

	for {
		if ctx.Err() != nil {
			return
		}
		batch, err := s.fetchBackfillBatch(ctx)
		if err != nil {
			return
		}
		if len(batch) == 0 {
			s.completeBackfill(ctx)
			return
		}
		if !s.processBatch(ctx, batch, lim, &chunksTableMissing) {
			return
		}
	}
}

// fetchBackfillBatch loads the next batch of rows with embedding_dense IS NULL.
func (s *Store) fetchBackfillBatch(ctx context.Context) ([]backfillRow, error) {
	dbRows, err := s.db.QueryContext(ctx,
		`SELECT id, content FROM memories WHERE embedding_dense IS NULL ORDER BY id LIMIT ?`,
		backfillBatchSize,
	)
	if err != nil {
		return nil, fmt.Errorf("backfill batch query: %w", err)
	}
	defer func() { _ = dbRows.Close() }()

	var batch []backfillRow
	for dbRows.Next() {
		var r backfillRow
		if scanErr := dbRows.Scan(&r.id, &r.content); scanErr != nil {
			return nil, fmt.Errorf("backfill batch scan: %w", scanErr)
		}
		batch = append(batch, r)
	}
	if rowsErr := dbRows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("backfill batch rows: %w", rowsErr)
	}
	return batch, nil
}

// processBatch embeds and stores each row in the batch. Returns false when
// ctx is cancelled so the caller can stop without clearing the owner PID.
func (s *Store) processBatch(
	ctx context.Context,
	batch []backfillRow,
	lim interface{ Wait(context.Context) error },
	chunksTableMissing *bool,
) bool {
	for _, r := range batch {
		if ctx.Err() != nil {
			return false
		}
		if waitErr := lim.Wait(ctx); waitErr != nil {
			return false
		}
		if s.embedder == nil {
			continue
		}
		vec := s.embedder.Embed(r.content)
		if vec == nil {
			continue // embedder returned nil — skip row, do not error
		}
		blob := MarshalEmbedding(vec)
		_, _ = s.db.ExecContext(ctx,
			`UPDATE memories SET embedding_dense=? WHERE id=? AND embedding_dense IS NULL`,
			blob, r.id,
		)
		s.maybeWriteChunk(ctx, r.id, blob, chunksTableMissing)
	}
	return true
}

// maybeWriteChunk attempts INSERT OR IGNORE into memory_chunks. On the first
// "no such table" error it logs once and sets the flag to skip future attempts.
func (s *Store) maybeWriteChunk(ctx context.Context, id int64, blob []byte, missing *bool) {
	if *missing {
		return
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO memory_chunks (memory_id, embedding_dense) VALUES (?, ?)`,
		id, blob,
	)
	if err != nil && strings.Contains(err.Error(), "no such table") {
		log.Printf("memory: memory_chunks table missing, skipping chunk writes")
		*missing = true
	}
}

// completeBackfill marks the backfill as complete and removes the owner PID
// in a single transaction.
func (s *Store) completeBackfill(ctx context.Context) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()

	_, _ = tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO kv_store (key, value, updated_at) VALUES (?, ?, datetime('now'))`,
		backfillStateKey, backfillStateComplete,
	)
	_, _ = tx.ExecContext(ctx,
		`DELETE FROM kv_store WHERE key = ?`,
		backfillOwnerKey,
	)

	_ = tx.Commit()
}

// acquireBackfillLock attempts to claim the backfill owner key via INSERT OR
// IGNORE. If the key already exists and is fresh it returns (false, nil).
// If the existing owner is stale it attempts a CAS steal.
func (s *Store) acquireBackfillLock(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err //nolint:wrapcheck // ctx.Err() sentinel values must not be wrapped
	}

	ownerValue := fmt.Sprintf("%d:%d", os.Getpid(), time.Now().UnixMilli())

	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO kv_store (key, value, updated_at) VALUES (?, ?, datetime('now'))`,
		backfillOwnerKey, ownerValue,
	)
	if err != nil {
		return false, fmt.Errorf("backfill lock insert: %w", err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("backfill lock rows: %w", err)
	}
	if rowsAffected == 1 {
		return true, nil
	}

	// Key already exists — read current owner and check staleness.
	var existing string
	err = s.db.QueryRowContext(ctx,
		`SELECT value FROM kv_store WHERE key = ?`, backfillOwnerKey,
	).Scan(&existing)
	if err != nil {
		return false, fmt.Errorf("backfill lock read existing: %w", err)
	}

	if isStaleOwner(existing) {
		return s.stealStaleBackfillLock(ctx, existing)
	}
	return false, nil
}

// stealStaleBackfillLock replaces oldValue with a new owner atomically via a
// CAS UPDATE. Exactly one winner across racing goroutines because SQLite
// serialises writes: the second UPDATE sees a different value and matches 0 rows.
func (s *Store) stealStaleBackfillLock(ctx context.Context, oldValue string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err //nolint:wrapcheck // ctx.Err() sentinel values must not be wrapped
	}

	newValue := fmt.Sprintf("%d:%d", os.Getpid(), time.Now().UnixMilli())

	res, err := s.db.ExecContext(ctx,
		`UPDATE kv_store SET value = ?, updated_at = datetime('now') WHERE key = ? AND value = ?`,
		newValue, backfillOwnerKey, oldValue,
	)
	if err != nil {
		return false, fmt.Errorf("steal backfill lock: %w", err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("steal backfill lock rows: %w", err)
	}
	return rowsAffected == 1, nil
}

// isStaleOwner reports whether the owner value "PID:TS_MILLIS" represents a
// lock held for longer than staleThreshold. On any parse failure it treats the
// owner as stale so a fresh process can recover.
func isStaleOwner(value string) bool {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 {
		return true
	}
	tsMillis, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return true
	}
	return time.Since(time.UnixMilli(tsMillis)) > staleThreshold
}
