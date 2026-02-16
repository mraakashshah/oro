package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

// logsConfig holds configuration for the logs command.
type logsConfig struct {
	tail   int
	follow bool
}

// newLogsCmd creates the "oro logs" subcommand.
func newLogsCmd() *cobra.Command {
	var cfg logsConfig

	cmd := &cobra.Command{
		Use:   "logs [worker-id]",
		Short: "Query and tail dispatcher event logs",
		Long:  "Displays events from the dispatcher event log.\nOptionally filter by worker-id and follow new events.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := ResolvePaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}

			db, err := openDB(paths.StateDBPath)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer db.Close()

			var workerID string
			if len(args) == 1 {
				workerID = args[0]
			}

			w := cmd.OutOrStdout()

			if cfg.follow {
				return followLogs(cmd.Context(), db, w, workerID, cfg.tail)
			}

			return printLogs(cmd.Context(), db, w, workerID, cfg.tail)
		},
	}

	cmd.Flags().IntVar(&cfg.tail, "tail", 20, "number of recent events to show")
	cmd.Flags().BoolVarP(&cfg.follow, "follow", "f", false, "poll for new events every 1s")

	return cmd
}

// event represents a row from the events table.
type event struct {
	ID        int
	Type      string
	Source    string
	BeadID    sql.NullString
	WorkerID  sql.NullString
	Payload   sql.NullString
	CreatedAt string
}

// printLogs queries and displays the last N events, optionally filtered by worker.
func printLogs(ctx context.Context, db *sql.DB, w io.Writer, workerID string, tail int) error {
	events, err := queryEvents(ctx, db, workerID, tail, "")
	if err != nil {
		return err
	}

	if len(events) == 0 {
		fmt.Fprintln(w, "no events found")
		return nil
	}

	for _, evt := range events {
		formatEvent(w, &evt)
	}

	return nil
}

// followLogs continuously polls for new events and displays them.
func followLogs(ctx context.Context, db *sql.DB, w io.Writer, workerID string, tail int) error {
	// First, display initial batch of events
	events, err := queryEvents(ctx, db, workerID, tail, "")
	if err != nil {
		return err
	}

	var lastTimestamp string
	if len(events) > 0 {
		for _, evt := range events {
			formatEvent(w, &evt)
		}
		lastTimestamp = events[len(events)-1].CreatedAt
	}

	// Poll for new events every second
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			newEvents, err := queryEvents(ctx, db, workerID, 100, lastTimestamp)
			if err != nil {
				return err
			}

			for _, evt := range newEvents {
				formatEvent(w, &evt)
				lastTimestamp = evt.CreatedAt
			}
		}
	}
}

// queryEvents retrieves events from the database.
// If sinceTimestamp is non-empty, only events newer than that timestamp are returned.
// Otherwise, returns the last 'limit' events in chronological order.
func queryEvents(ctx context.Context, db *sql.DB, workerID string, limit int, sinceTimestamp string) ([]event, error) {
	query, args := buildEventQuery(workerID, limit, sinceTimestamp)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	events, err := scanEvents(rows)
	if err != nil {
		return nil, err
	}

	// For non-since queries, reverse to chronological order
	if sinceTimestamp == "" {
		reverseEvents(events)
	}

	return events, nil
}

// buildEventQuery constructs the SQL query and args based on filters.
func buildEventQuery(workerID string, limit int, sinceTimestamp string) (query string, args []interface{}) {
	if sinceTimestamp != "" {
		return buildSinceQuery(workerID, limit, sinceTimestamp)
	}
	return buildTailQuery(workerID, limit)
}

// buildSinceQuery builds a query for events after a timestamp.
func buildSinceQuery(workerID string, limit int, sinceTimestamp string) (query string, args []interface{}) {
	if workerID != "" {
		query = `
			SELECT id, type, source, bead_id, worker_id, payload, created_at
			FROM events
			WHERE created_at > ? AND worker_id = ?
			ORDER BY created_at ASC
			LIMIT ?
		`
		args = []interface{}{sinceTimestamp, workerID, limit}
		return query, args
	}

	query = `
		SELECT id, type, source, bead_id, worker_id, payload, created_at
		FROM events
		WHERE created_at > ?
		ORDER BY created_at ASC
		LIMIT ?
	`
	args = []interface{}{sinceTimestamp, limit}
	return query, args
}

// buildTailQuery builds a query for the last N events.
func buildTailQuery(workerID string, limit int) (query string, args []interface{}) {
	if workerID != "" {
		query = `
			SELECT id, type, source, bead_id, worker_id, payload, created_at
			FROM events
			WHERE worker_id = ?
			ORDER BY created_at DESC
			LIMIT ?
		`
		args = []interface{}{workerID, limit}
		return query, args
	}

	query = `
		SELECT id, type, source, bead_id, worker_id, payload, created_at
		FROM events
		ORDER BY created_at DESC
		LIMIT ?
	`
	args = []interface{}{limit}
	return query, args
}

// scanEvents scans all rows into a slice of events.
func scanEvents(rows *sql.Rows) ([]event, error) {
	var events []event
	for rows.Next() {
		var evt event
		if err := rows.Scan(&evt.ID, &evt.Type, &evt.Source, &evt.BeadID, &evt.WorkerID, &evt.Payload, &evt.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, evt)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}

	return events, nil
}

// reverseEvents reverses a slice of events in place.
func reverseEvents(events []event) {
	for i := 0; i < len(events)/2; i++ {
		j := len(events) - 1 - i
		events[i], events[j] = events[j], events[i]
	}
}

// formatEvent writes a single event in a human-readable format.
func formatEvent(w io.Writer, evt *event) {
	workerID := ""
	if evt.WorkerID.Valid {
		workerID = evt.WorkerID.String
	}

	beadID := ""
	if evt.BeadID.Valid {
		beadID = evt.BeadID.String
	}

	payload := ""
	if evt.Payload.Valid {
		payload = evt.Payload.String
	}

	// Format: timestamp | worker_id | event_type | bead_id | source | payload
	fmt.Fprintf(w, "%s | %-12s | %-20s | %-15s | %-12s | %s\n",
		evt.CreatedAt, workerID, evt.Type, beadID, evt.Source, payload)
}
