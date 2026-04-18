package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrBackfillLocked is returned by MaybeStartBackfill when another process
// holds the backfill owner lock and it is not yet stale.
var ErrBackfillLocked = errors.New("backfill already running on another process")

const (
	backfillStateKey     = "backfill_semantic_memory_state"
	backfillOwnerKey     = "backfill_owner_pid"
	backfillStatePending = "pending"
	staleThreshold       = 10 * time.Minute
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
	return nil
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
