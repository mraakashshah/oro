package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// FetchBeads runs `bd list --json` and parses the output into a []protocol.Bead slice.
//
// Error cases:
//   - bd not in PATH → returns error
//   - bd returns empty array → returns empty slice, nil error
//   - JSON parse error → returns nil, error
func FetchBeads() ([]protocol.Bead, error) {
	// Verify bd is available on PATH before running.
	if _, err := exec.LookPath("bd"); err != nil {
		return nil, fmt.Errorf("bd not in PATH: %w", err)
	}

	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "bd", "list", "--json") //nolint:gosec // G204: "bd" is a trusted internal CLI, not user input
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bd list --json: %w", err)
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return []protocol.Bead{}, nil
	}

	var beads []protocol.Bead
	if err := json.Unmarshal([]byte(output), &beads); err != nil {
		return nil, fmt.Errorf("parse beads JSON: %w", err)
	}

	return beads, nil
}

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
