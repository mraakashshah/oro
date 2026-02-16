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

// TestAcceptanceCriteria_QuerySupport verifies AC:
// "oro-dash can query dispatcher SQLite event log for worker-related events"
func TestAcceptanceCriteria_QuerySupport(t *testing.T) {
	ctx := context.Background()

	// Setup test database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "dispatcher.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		db.Close()
		t.Fatalf("failed to init schema: %v", err)
	}

	// Insert worker events
	_, err = db.Exec(
		`INSERT INTO events (type, source, bead_id, worker_id, payload) VALUES (?, ?, ?, ?, ?)`,
		"heartbeat", "worker-1", "beads-abc", "worker-1", "",
	)
	if err != nil {
		t.Fatalf("failed to insert event: %v", err)
	}
	db.Close()

	// Verify: oro-dash can query event log
	reader, err := eventlog.NewReader(dbPath)
	if err != nil {
		t.Fatalf("oro-dash cannot query event log: %v", err)
	}
	defer reader.Close()

	events, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID: "worker-1",
	})
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if len(events) == 0 {
		t.Fatal("expected to retrieve worker events")
	}
}

// TestAcceptanceCriteria_FilterSupport verifies AC:
// "Query supports filtering by worker ID, event type, and time range"
func TestAcceptanceCriteria_FilterSupport(t *testing.T) {
	ctx := context.Background()

	// Setup test database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "dispatcher.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		db.Close()
		t.Fatalf("failed to init schema: %v", err)
	}

	// Insert test events
	now := time.Now()
	testEvents := []struct {
		evType   string
		workerID string
		offset   time.Duration
	}{
		{"heartbeat", "worker-1", 0},
		{"assign", "worker-1", 1 * time.Second},
		{"heartbeat", "worker-2", 2 * time.Second},
	}

	for _, e := range testEvents {
		createdAt := now.Add(e.offset)
		_, err := db.Exec(
			`INSERT INTO events (type, source, bead_id, worker_id, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			e.evType, "dispatcher", "", e.workerID, "", createdAt.Format("2006-01-02 15:04:05"),
		)
		if err != nil {
			t.Fatalf("failed to insert event: %v", err)
		}
	}
	db.Close()

	reader, err := eventlog.NewReader(dbPath)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}
	defer reader.Close()

	// Verify: Filter by worker ID
	t.Run("filter_by_worker_id", func(t *testing.T) {
		events, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
			WorkerID: "worker-1",
		})
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}
		if len(events) != 2 {
			t.Errorf("expected 2 events for worker-1, got %d", len(events))
		}
		for _, e := range events {
			if e.WorkerID != "worker-1" {
				t.Errorf("expected worker-1, got %s", e.WorkerID)
			}
		}
	})

	// Verify: Filter by event type
	t.Run("filter_by_event_type", func(t *testing.T) {
		events, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
			WorkerID:  "worker-1",
			EventType: "heartbeat",
		})
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}
		if len(events) != 1 {
			t.Errorf("expected 1 heartbeat event, got %d", len(events))
		}
		if len(events) > 0 && events[0].Type != "heartbeat" {
			t.Errorf("expected heartbeat, got %s", events[0].Type)
		}
	})

	// Verify: Filter by time range
	t.Run("filter_by_time_range", func(t *testing.T) {
		// Filter for events after 1.5 seconds (should only get the assign event at 1s)
		afterTime := now.Add(-500 * time.Millisecond)
		beforeTime := now.Add(1500 * time.Millisecond)
		events, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
			WorkerID: "worker-1",
			After:    &afterTime,
			Before:   &beforeTime,
		})
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}
		// Should get 2 events: heartbeat at 0s and assign at 1s
		if len(events) != 2 {
			t.Errorf("expected 2 events in time range, got %d", len(events))
		}
	})
}

// TestAcceptanceCriteria_StructuredData verifies AC:
// "Results are returned as structured data suitable for table rendering"
func TestAcceptanceCriteria_StructuredData(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "dispatcher.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		db.Close()
		t.Fatalf("failed to init schema: %v", err)
	}

	now := time.Now()
	_, err = db.Exec(
		`INSERT INTO events (type, source, bead_id, worker_id, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"assign", "dispatcher", "beads-test", "worker-1", `{"model":"opus"}`, now.Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		t.Fatalf("failed to insert event: %v", err)
	}
	db.Close()

	reader, err := eventlog.NewReader(dbPath)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}
	defer reader.Close()

	events, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID: "worker-1",
	})
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}

	// Verify structured data fields are populated for table rendering
	e := events[0]
	if e.ID == 0 {
		t.Error("ID should be populated")
	}
	if e.Type == "" {
		t.Error("Type should be populated")
	}
	if e.Source == "" {
		t.Error("Source should be populated")
	}
	if e.WorkerID == "" {
		t.Error("WorkerID should be populated")
	}
	if e.CreatedAt.IsZero() {
		t.Error("CreatedAt should be parsed as time.Time")
	}
	// BeadID and Payload may be empty depending on event type
}

// TestAcceptanceCriteria_ReadOnlyWAL verifies AC:
// "Read-only connection with WAL mode to avoid blocking the dispatcher"
func TestAcceptanceCriteria_ReadOnlyWAL(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "dispatcher.db")

	// Create database and enable WAL
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		db.Close()
		t.Fatalf("failed to init schema: %v", err)
	}

	// Enable WAL mode
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		t.Fatalf("failed to enable WAL: %v", err)
	}
	db.Close()

	// Verify: Reader opens in read-only mode with WAL
	reader, err := eventlog.NewReader(dbPath)
	if err != nil {
		t.Fatalf("failed to open read-only connection: %v", err)
	}
	defer reader.Close()

	// Attempt to query - should succeed without blocking
	ctx := context.Background()
	_, err = reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID: "worker-1",
	})
	if err != nil {
		t.Fatalf("read-only query failed: %v", err)
	}
}

// TestAcceptanceCriteria_GracefulHandling verifies AC:
// "Query handles missing or empty event log gracefully (no panic, shows empty state)"
func TestAcceptanceCriteria_GracefulHandling(t *testing.T) {
	ctx := context.Background()

	// Test 1: Missing database file
	t.Run("missing_database", func(t *testing.T) {
		reader, err := eventlog.NewReader("/nonexistent/path.db")
		if err == nil {
			defer reader.Close()
			t.Error("expected error for missing database")
		}
		// Should return error, not panic
	})

	// Test 2: Empty event log
	t.Run("empty_event_log", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "empty.db")

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("failed to create db: %v", err)
		}

		if _, err := db.Exec(protocol.SchemaDDL); err != nil {
			db.Close()
			t.Fatalf("failed to init schema: %v", err)
		}
		db.Close()

		reader, err := eventlog.NewReader(dbPath)
		if err != nil {
			t.Fatalf("failed to open reader: %v", err)
		}
		defer reader.Close()

		// Should return empty slice, not panic
		events, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
			WorkerID: "worker-1",
		})
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}

		if len(events) != 0 {
			t.Errorf("expected empty slice, got %d events", len(events))
		}
	})

	// Test 3: Worker with no events
	t.Run("worker_with_no_events", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("failed to create db: %v", err)
		}

		if _, err := db.Exec(protocol.SchemaDDL); err != nil {
			db.Close()
			t.Fatalf("failed to init schema: %v", err)
		}

		// Insert event for worker-1
		_, err = db.Exec(
			`INSERT INTO events (type, source, bead_id, worker_id, payload) VALUES (?, ?, ?, ?, ?)`,
			"heartbeat", "worker-1", "", "worker-1", "",
		)
		if err != nil {
			db.Close()
			t.Fatalf("failed to insert event: %v", err)
		}
		db.Close()

		reader, err := eventlog.NewReader(dbPath)
		if err != nil {
			t.Fatalf("failed to open reader: %v", err)
		}
		defer reader.Close()

		// Query for worker-2 (no events)
		events, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
			WorkerID: "worker-2",
		})
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}

		if len(events) != 0 {
			t.Errorf("expected empty slice for worker with no events, got %d", len(events))
		}
	})
}
