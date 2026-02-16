package eventlog_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/eventlog"
	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// setupTestDB creates a test database with some sample events
func setupTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	// Initialize schema
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}

	// Insert sample events
	events := []struct {
		evType   string
		source   string
		beadID   string
		workerID string
		payload  string
	}{
		{"heartbeat", "worker-1", "beads-abc", "worker-1", ""},
		{"assign", "dispatcher", "beads-abc", "worker-1", `{"worktree":"/tmp/wt1"}`},
		{"status", "worker-1", "beads-abc", "worker-1", `{"state":"running"}`},
		{"heartbeat", "worker-2", "beads-def", "worker-2", ""},
		{"done", "worker-1", "beads-abc", "worker-1", ""},
		{"merged", "dispatcher", "beads-abc", "worker-1", `{"sha":"abc123"}`},
	}

	for _, e := range events {
		_, err := db.Exec(
			`INSERT INTO events (type, source, bead_id, worker_id, payload) VALUES (?, ?, ?, ?, ?)`,
			e.evType, e.source, e.beadID, e.workerID, e.payload,
		)
		if err != nil {
			t.Fatalf("failed to insert test event: %v", err)
		}
		// Small delay to ensure different timestamps
		time.Sleep(1 * time.Millisecond)
	}

	return db, dbPath
}

func TestNewReader_Success(t *testing.T) {
	db, dbPath := setupTestDB(t)
	defer db.Close()

	reader, err := eventlog.NewReader(dbPath)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer reader.Close()

	if reader == nil {
		t.Fatal("expected non-nil reader")
	}
}

func TestNewReader_MissingDB(t *testing.T) {
	reader, err := eventlog.NewReader("/nonexistent/path.db")
	if err == nil {
		t.Fatal("expected error for missing database")
	}
	if reader != nil {
		reader.Close()
		t.Fatal("expected nil reader for missing database")
	}
}

func TestQueryWorkerEvents_AllEvents(t *testing.T) {
	db, dbPath := setupTestDB(t)
	defer db.Close()

	reader, err := eventlog.NewReader(dbPath)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer reader.Close()

	ctx := context.Background()
	events, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID: "worker-1",
	})
	if err != nil {
		t.Fatalf("QueryWorkerEvents failed: %v", err)
	}

	if len(events) != 5 {
		t.Errorf("expected 5 events for worker-1, got %d", len(events))
	}

	// Verify first event fields
	if len(events) > 0 {
		e := events[0]
		if e.Type == "" {
			t.Error("expected event type to be populated")
		}
		if e.WorkerID != "worker-1" {
			t.Errorf("expected worker_id=worker-1, got %s", e.WorkerID)
		}
	}
}

func TestQueryWorkerEvents_FilterByEventType(t *testing.T) {
	db, dbPath := setupTestDB(t)
	defer db.Close()

	reader, err := eventlog.NewReader(dbPath)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer reader.Close()

	ctx := context.Background()
	events, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID:  "worker-1",
		EventType: "heartbeat",
	})
	if err != nil {
		t.Fatalf("QueryWorkerEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Errorf("expected 1 heartbeat event for worker-1, got %d", len(events))
	}

	if len(events) > 0 && events[0].Type != "heartbeat" {
		t.Errorf("expected event type=heartbeat, got %s", events[0].Type)
	}
}

func TestQueryWorkerEvents_TimeRange(t *testing.T) {
	db, dbPath := setupTestDB(t)
	defer db.Close()

	reader, err := eventlog.NewReader(dbPath)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer reader.Close()

	ctx := context.Background()

	// Query for events in the last minute
	now := time.Now()
	afterTime := now.Add(-1 * time.Minute)

	events, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID: "worker-1",
		After:    &afterTime,
	})
	if err != nil {
		t.Fatalf("QueryWorkerEvents failed: %v", err)
	}

	if len(events) != 5 {
		t.Errorf("expected 5 recent events, got %d", len(events))
	}

	// Query for events before 1 hour ago (should be empty since all events are recent)
	pastTime := now.Add(-1 * time.Hour)
	events, err = reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID: "worker-1",
		Before:   &pastTime,
	})
	if err != nil {
		t.Fatalf("QueryWorkerEvents failed: %v", err)
	}

	if len(events) != 0 {
		t.Errorf("expected 0 old events, got %d", len(events))
	}
}

func TestQueryWorkerEvents_EmptyResult(t *testing.T) {
	db, dbPath := setupTestDB(t)
	defer db.Close()

	reader, err := eventlog.NewReader(dbPath)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer reader.Close()

	ctx := context.Background()
	events, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID: "nonexistent-worker",
	})
	if err != nil {
		t.Fatalf("QueryWorkerEvents failed: %v", err)
	}

	if len(events) != 0 {
		t.Errorf("expected 0 events for nonexistent worker, got %d", len(events))
	}
}

func TestQueryWorkerEvents_MissingDBFile(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "missing.db")

	reader, err := eventlog.NewReader(dbPath)
	if err == nil {
		defer reader.Close()
		t.Fatal("expected error when database doesn't exist")
	}
}

func TestQueryWorkerEvents_EmptyDBPath(t *testing.T) {
	tmpDir := t.TempDir()
	emptyDBPath := filepath.Join(tmpDir, "empty.db")

	// Create empty database with schema but no data
	db, err := sql.Open("sqlite", emptyDBPath)
	if err != nil {
		t.Fatalf("failed to create empty db: %v", err)
	}
	// Initialize schema but don't insert any events
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		db.Close()
		t.Fatalf("failed to init schema: %v", err)
	}
	db.Close()

	reader, err := eventlog.NewReader(emptyDBPath)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer reader.Close()

	ctx := context.Background()
	events, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID: "worker-1",
	})
	if err != nil {
		t.Fatalf("QueryWorkerEvents failed: %v", err)
	}

	// Should return empty slice for empty database
	if len(events) != 0 {
		t.Errorf("expected 0 events for empty db, got %d", len(events))
	}
}

func TestClose_MultipleCalls(t *testing.T) {
	db, dbPath := setupTestDB(t)
	defer db.Close()

	reader, err := eventlog.NewReader(dbPath)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}

	// First close should succeed
	if err := reader.Close(); err != nil {
		t.Errorf("first Close failed: %v", err)
	}

	// Second close should be safe (no panic)
	if err := reader.Close(); err != nil {
		t.Errorf("second Close failed: %v", err)
	}
}

func TestDefaultDBPath(t *testing.T) {
	dbPath := eventlog.DefaultDBPath()
	if dbPath == "" {
		t.Error("expected non-empty default db path")
	}

	// Should contain .oro directory
	if !filepath.IsAbs(dbPath) {
		t.Error("expected absolute path from DefaultDBPath")
	}
}
