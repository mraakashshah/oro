package main

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// setupTestDB creates an in-memory SQLite database with the events table schema.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}

	schema := `
		CREATE TABLE events (
			id INTEGER PRIMARY KEY,
			type TEXT NOT NULL,
			source TEXT NOT NULL,
			bead_id TEXT,
			worker_id TEXT,
			payload TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
	`

	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	return db
}

// insertTestEvent inserts a test event into the database.
func insertTestEvent(t *testing.T, db *sql.DB, eventType, source, beadID, workerID, payload, createdAt string) {
	t.Helper()

	query := `
		INSERT INTO events (type, source, bead_id, worker_id, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`

	_, err := db.Exec(query, eventType, source, beadID, workerID, payload, createdAt)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
}

func TestLogsCommand(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Insert test events
	insertTestEvent(t, db, "worker_started", "dispatcher", "", "worker-1", "", "2026-02-10 10:00:00")
	insertTestEvent(t, db, "bead_assigned", "dispatcher", "bead-123", "worker-1", "", "2026-02-10 10:00:01")
	insertTestEvent(t, db, "worker_started", "dispatcher", "", "worker-2", "", "2026-02-10 10:00:02")

	// Query all events
	var buf bytes.Buffer
	err := printLogs(context.Background(), db, &buf, "", 20)
	if err != nil {
		t.Fatalf("printLogs failed: %v", err)
	}

	output := buf.String()

	// Verify output contains all events
	if !strings.Contains(output, "worker_started") {
		t.Errorf("output missing worker_started event")
	}
	if !strings.Contains(output, "bead_assigned") {
		t.Errorf("output missing bead_assigned event")
	}
	if !strings.Contains(output, "worker-1") {
		t.Errorf("output missing worker-1")
	}
	if !strings.Contains(output, "worker-2") {
		t.Errorf("output missing worker-2")
	}
	if !strings.Contains(output, "bead-123") {
		t.Errorf("output missing bead-123")
	}

	// Verify chronological order (first event should appear first)
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
}

func TestLogsFilterByWorker(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Insert events for multiple workers
	insertTestEvent(t, db, "worker_started", "dispatcher", "", "worker-1", "", "2026-02-10 10:00:00")
	insertTestEvent(t, db, "bead_assigned", "dispatcher", "bead-123", "worker-1", "", "2026-02-10 10:00:01")
	insertTestEvent(t, db, "worker_started", "dispatcher", "", "worker-2", "", "2026-02-10 10:00:02")
	insertTestEvent(t, db, "bead_assigned", "dispatcher", "bead-456", "worker-2", "", "2026-02-10 10:00:03")

	// Filter by worker-1
	var buf bytes.Buffer
	err := printLogs(context.Background(), db, &buf, "worker-1", 20)
	if err != nil {
		t.Fatalf("printLogs failed: %v", err)
	}

	output := buf.String()

	// Verify output contains only worker-1 events
	if !strings.Contains(output, "worker-1") {
		t.Errorf("output missing worker-1")
	}
	if strings.Contains(output, "worker-2") {
		t.Errorf("output should not contain worker-2")
	}
	if !strings.Contains(output, "bead-123") {
		t.Errorf("output missing bead-123")
	}
	if strings.Contains(output, "bead-456") {
		t.Errorf("output should not contain bead-456")
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines for worker-1, got %d", len(lines))
	}
}

func TestLogsTailLimit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Insert 30 events
	baseTime := time.Date(2026, 2, 10, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		timestamp := baseTime.Add(time.Duration(i) * time.Second).Format("2006-01-02 15:04:05")
		insertTestEvent(t, db, "test_event", "dispatcher", "", "worker-1", "", timestamp)
	}

	// Query with tail limit of 5
	var buf bytes.Buffer
	err := printLogs(context.Background(), db, &buf, "", 5)
	if err != nil {
		t.Fatalf("printLogs failed: %v", err)
	}

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	if len(lines) != 5 {
		t.Errorf("expected 5 lines with --tail 5, got %d", len(lines))
	}

	// Verify we got the most recent 5 events (timestamps 25-29)
	// The last line should contain the most recent timestamp
	lastLine := lines[len(lines)-1]
	if !strings.Contains(lastLine, "10:00:29") {
		t.Errorf("expected last event to be at 10:00:29, got: %s", lastLine)
	}
}

func TestLogsNoEvents(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Query empty database
	var buf bytes.Buffer
	err := printLogs(context.Background(), db, &buf, "", 20)
	if err != nil {
		t.Fatalf("printLogs failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "no events found") {
		t.Errorf("expected 'no events found' message, got: %s", output)
	}
}

func TestQueryEventsWithSinceTimestamp(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Insert events with specific timestamps
	insertTestEvent(t, db, "event1", "dispatcher", "", "worker-1", "", "2026-02-10 10:00:00")
	insertTestEvent(t, db, "event2", "dispatcher", "", "worker-1", "", "2026-02-10 10:00:01")
	insertTestEvent(t, db, "event3", "dispatcher", "", "worker-1", "", "2026-02-10 10:00:02")

	// Query events since 10:00:00 (should return event2 and event3)
	events, err := queryEvents(context.Background(), db, "", 100, "2026-02-10 10:00:00")
	if err != nil {
		t.Fatalf("queryEvents failed: %v", err)
	}

	if len(events) != 2 {
		t.Errorf("expected 2 events after timestamp, got %d", len(events))
	}

	// Verify events are in chronological order
	if events[0].Type != "event2" {
		t.Errorf("expected first event to be event2, got %s", events[0].Type)
	}
	if events[1].Type != "event3" {
		t.Errorf("expected second event to be event3, got %s", events[1].Type)
	}
}

func TestFormatEvent(t *testing.T) {
	evt := event{
		ID:        1,
		Type:      "worker_started",
		Source:    "dispatcher",
		BeadID:    sql.NullString{String: "bead-123", Valid: true},
		WorkerID:  sql.NullString{String: "worker-1", Valid: true},
		Payload:   sql.NullString{String: "test payload", Valid: true},
		CreatedAt: "2026-02-10 10:00:00",
	}

	var buf bytes.Buffer
	formatEvent(&buf, &evt)

	output := buf.String()
	if !strings.Contains(output, "worker_started") {
		t.Errorf("output missing event type")
	}
	if !strings.Contains(output, "worker-1") {
		t.Errorf("output missing worker_id")
	}
	if !strings.Contains(output, "bead-123") {
		t.Errorf("output missing bead_id")
	}
	if !strings.Contains(output, "test payload") {
		t.Errorf("output missing payload")
	}
}
