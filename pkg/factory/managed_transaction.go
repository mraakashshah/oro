package factory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// beginTransition retries only a failed BEGIN, before any journal callback runs.
// The returned release holds the mutex and transaction context until rollback or commit.
func (j *managedJournal) beginTransition(parent context.Context) (*sql.Tx, func(), error) {
	var ctx context.Context
	var cancel context.CancelFunc
	if _, ok := parent.Deadline(); ok {
		ctx, cancel = context.WithCancel(parent)
	} else {
		ctx, cancel = context.WithTimeout(parent, 30*time.Second)
	}
	for {
		if err := j.lockTransition(ctx); err != nil {
			cancel()
			return nil, nil, err
		}
		if err := ctx.Err(); err != nil {
			j.mu.Unlock()
			cancel()
			return nil, nil, fmt.Errorf("managed transition canceled: %w", err)
		}
		tx, err := j.conn.BeginTx(ctx, nil)
		if err == nil {
			if ctx.Err() != nil {
				_ = tx.Rollback()
				j.mu.Unlock()
				cancel()
				return nil, nil, fmt.Errorf("managed transition canceled: %w", ctx.Err())
			}
			return tx, func() { j.mu.Unlock(); cancel() }, nil
		}
		j.mu.Unlock()
		if ctx.Err() != nil {
			cancel()
			return nil, nil, fmt.Errorf("managed transition canceled: %w", ctx.Err())
		}
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code() != sqlite3.SQLITE_BUSY {
			cancel()
			return nil, nil, fmt.Errorf("begin managed transition: %w", err)
		}
		if err := waitManagedTransition(ctx); err != nil {
			cancel()
			return nil, nil, err
		}
	}
}

func (j *managedJournal) lockTransition(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("managed transition canceled: %w", err)
		}
		if j.mu.TryLock() {
			return nil
		}
		if err := waitManagedTransition(ctx); err != nil {
			return err
		}
	}
}

func waitManagedTransition(ctx context.Context) error {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("managed transition canceled: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
