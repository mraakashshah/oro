// Package eventlog provides read-only access to the dispatcher's SQLite event log.
// It enables querying worker-related events for display in oro-dash and other tools.
package eventlog

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // SQLite driver
)

// Event represents a single event from the dispatcher log.
type Event struct {
	ID        int64
	Type      string
	Source    string
	BeadID    string
	WorkerID  string
	Payload   string
	CreatedAt time.Time
}

// QueryOpts specifies filter criteria for querying events.
type QueryOpts struct {
	// WorkerID filters events to a specific worker (required unless EventType is set)
	WorkerID string

	// EventType filters to a specific event type (e.g., "heartbeat", "assign", "done")
	EventType string

	// After filters events created after this time (inclusive)
	After *time.Time

	// Before filters events created before this time (inclusive)
	Before *time.Time

	// Limit restricts the number of results (0 = no limit)
	Limit int
}

// Reader provides read-only access to the dispatcher event log.
type Reader struct {
	db *sql.DB
}

// NewReader opens the dispatcher's SQLite database in read-only mode with WAL.
// Returns an error if the database doesn't exist or cannot be opened.
func NewReader(dbPath string) (*Reader, error) {
	// Verify database file exists before attempting to open
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("database not found: %w", err)
	}

	// Open in read-only mode with WAL to avoid blocking the dispatcher
	dsn := fmt.Sprintf("file:%s?mode=ro&_journal_mode=WAL", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &Reader{db: db}, nil
}

// Close releases the database connection.
// Safe to call multiple times.
func (r *Reader) Close() error {
	if r.db != nil {
		return r.db.Close()
	}
	return nil
}

// QueryWorkerEvents retrieves events matching the given filter criteria.
// Returns an empty slice if no events match.
func (r *Reader) QueryWorkerEvents(ctx context.Context, opts QueryOpts) ([]Event, error) {
	query, args := buildQuery(opts)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var createdAtStr string

		err := rows.Scan(
			&e.ID,
			&e.Type,
			&e.Source,
			&e.BeadID,
			&e.WorkerID,
			&e.Payload,
			&createdAtStr,
		)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}

		// Parse ISO8601 timestamp from SQLite
		if createdAtStr != "" {
			parsedTime, err := time.Parse("2006-01-02 15:04:05", createdAtStr)
			if err != nil {
				// Fallback: try with timezone format
				parsedTime, err = time.Parse(time.RFC3339, createdAtStr)
				if err != nil {
					return nil, fmt.Errorf("parse created_at: %w", err)
				}
			}
			e.CreatedAt = parsedTime
		}

		events = append(events, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}

	return events, nil
}

// buildQuery constructs the SQL query and arguments from QueryOpts.
func buildQuery(opts QueryOpts) (string, []any) {
	var conditions []string
	var args []any

	// Base query
	query := "SELECT id, type, source, bead_id, worker_id, payload, created_at FROM events WHERE 1=1"

	// Filter by worker ID
	if opts.WorkerID != "" {
		conditions = append(conditions, "worker_id = ?")
		args = append(args, opts.WorkerID)
	}

	// Filter by event type
	if opts.EventType != "" {
		conditions = append(conditions, "type = ?")
		args = append(args, opts.EventType)
	}

	// Filter by time range
	if opts.After != nil {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, opts.After.Format("2006-01-02 15:04:05"))
	}

	if opts.Before != nil {
		conditions = append(conditions, "created_at <= ?")
		args = append(args, opts.Before.Format("2006-01-02 15:04:05"))
	}

	// Append WHERE conditions
	if len(conditions) > 0 {
		query += " AND " + strings.Join(conditions, " AND ")
	}

	// Order by newest first
	query += " ORDER BY id DESC"

	// Apply limit
	if opts.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", opts.Limit)
	}

	return query, args
}

// DefaultDBPath returns the default path to the dispatcher's event database.
func DefaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".oro", "dispatcher.db")
}
