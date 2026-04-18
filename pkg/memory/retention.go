package memory

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// TrimSearchEvents deletes rows from memory_search_events older than maxAge.
// Returns the number of deleted rows and any error from the DELETE.
// If maxAge <= 0, returns (0, nil) without executing a DELETE.
func TrimSearchEvents(ctx context.Context, db *sql.DB, maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	seconds := int64(maxAge.Seconds())
	res, err := db.ExecContext(ctx,
		`DELETE FROM memory_search_events WHERE ts < datetime('now', '-' || ? || ' seconds')`,
		seconds,
	)
	if err != nil {
		return 0, fmt.Errorf("trim search events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("trim search events rows affected: %w", err)
	}
	return n, nil
}
