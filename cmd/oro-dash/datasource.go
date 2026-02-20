package main

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// FetchWorkers reads active worker assignments from the dispatcher's sqlite
// state database at dbPath and returns one WorkerStatus per active row.
//
// Error cases:
//   - dbPath does not exist or is not a valid sqlite DB → returns error
//   - SQL query error → returns error
func FetchWorkers(dbPath string) ([]WorkerStatus, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open state db %s: %w", dbPath, err)
	}
	defer db.Close() //nolint:errcheck // best-effort close on read-only query path

	ctx := context.Background()

	// Verify the database is reachable (catches missing file / bad path).
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping state db %s: %w", dbPath, err)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT worker_id, bead_id, status
		FROM   assignments
		WHERE  status = 'active'
		ORDER  BY assigned_at
	`)
	if err != nil {
		return nil, fmt.Errorf("query assignments: %w", err)
	}
	defer rows.Close() //nolint:errcheck // best-effort close after full iteration

	var workers []WorkerStatus
	for rows.Next() {
		var w WorkerStatus
		if err := rows.Scan(&w.ID, &w.BeadID, &w.Status); err != nil {
			return nil, fmt.Errorf("scan assignment row: %w", err)
		}
		workers = append(workers, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assignments: %w", err)
	}

	if workers == nil {
		workers = []WorkerStatus{}
	}

	return workers, nil
}
