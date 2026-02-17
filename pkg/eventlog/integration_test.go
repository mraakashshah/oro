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

// TestIntegration_WorkerHistoryQuery demonstrates the full workflow:
// 1. Create a database with dispatcher schema
// 2. Insert worker-related events
// 3. Query events by worker ID, event type, and time range
// 4. Verify results are suitable for table rendering
func TestIntegration_WorkerHistoryQuery(t *testing.T) {
	ctx := context.Background()

	// Setup: Create test database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "dispatcher.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	// Initialize dispatcher schema
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		_ = db.Close()
		t.Fatalf("failed to init schema: %v", err)
	}

	// Insert sample worker events
	events := []struct {
		evType   string
		source   string
		beadID   string
		workerID string
		payload  string
	}{
		// Worker 1 timeline
		{"heartbeat", "worker-1", "", "worker-1", ""},
		{"assign", "dispatcher", "beads-abc", "worker-1", `{"worktree":"/tmp/wt1","model":"sonnet"}`},
		{"heartbeat", "worker-1", "beads-abc", "worker-1", ""},
		{"status", "worker-1", "beads-abc", "worker-1", `{"state":"running","result":"in_progress"}`},
		{"heartbeat", "worker-1", "beads-abc", "worker-1", ""},
		{"done", "worker-1", "beads-abc", "worker-1", `{"qg_passed":true}`},
		{"merged", "dispatcher", "beads-abc", "worker-1", `{"sha":"abc123def456"}`},
		// Worker 2 timeline
		{"heartbeat", "worker-2", "", "worker-2", ""},
		{"assign", "dispatcher", "beads-xyz", "worker-2", `{"worktree":"/tmp/wt2","model":"opus"}`},
		{"heartbeat", "worker-2", "beads-xyz", "worker-2", ""},
	}

	now := time.Now()
	for i, e := range events {
		createdAt := now.Add(time.Duration(i) * time.Second)
		_, err := db.Exec(
			`INSERT INTO events (type, source, bead_id, worker_id, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			e.evType, e.source, e.beadID, e.workerID, e.payload, createdAt.Format("2006-01-02 15:04:05"),
		)
		if err != nil {
			t.Fatalf("failed to insert event: %v", err)
		}
	}
	_ = db.Close()

	// Test: Open read-only connection with WAL mode
	reader, err := eventlog.NewReader(dbPath)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer reader.Close()

	// Query 1: All events for worker-1
	worker1Events, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID: "worker-1",
	})
	if err != nil {
		t.Fatalf("QueryWorkerEvents failed: %v", err)
	}

	if len(worker1Events) != 7 {
		t.Errorf("expected 7 events for worker-1, got %d", len(worker1Events))
	}

	// Verify events are suitable for table rendering
	for _, e := range worker1Events {
		if e.Type == "" {
			t.Error("event type should not be empty")
		}
		if e.WorkerID != "worker-1" {
			t.Errorf("expected worker_id=worker-1, got %s", e.WorkerID)
		}
		if e.CreatedAt.IsZero() {
			t.Error("created_at should be parsed")
		}
	}

	// Query 2: Filter by event type
	heartbeats, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID:  "worker-1",
		EventType: "heartbeat",
	})
	if err != nil {
		t.Fatalf("QueryWorkerEvents failed: %v", err)
	}

	if len(heartbeats) != 3 {
		t.Errorf("expected 3 heartbeat events for worker-1, got %d", len(heartbeats))
	}

	// Query 3: Filter by time range
	recentTime := now.Add(5 * time.Second)
	recentEvents, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID: "worker-1",
		After:    &recentTime,
	})
	if err != nil {
		t.Fatalf("QueryWorkerEvents failed: %v", err)
	}

	if len(recentEvents) != 2 {
		t.Errorf("expected 2 recent events for worker-1, got %d", len(recentEvents))
	}

	// Query 4: Limit results
	limitedEvents, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID: "worker-1",
		Limit:    3,
	})
	if err != nil {
		t.Fatalf("QueryWorkerEvents failed: %v", err)
	}

	if len(limitedEvents) != 3 {
		t.Errorf("expected 3 limited events, got %d", len(limitedEvents))
	}

	// Verify events are ordered newest first (DESC)
	if len(limitedEvents) >= 2 {
		if limitedEvents[0].ID < limitedEvents[1].ID {
			t.Error("events should be ordered newest first (DESC)")
		}
	}

	// Query 5: Handle empty event log gracefully (no panic)
	emptyEvents, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID: "nonexistent-worker",
	})
	if err != nil {
		t.Fatalf("QueryWorkerEvents failed: %v", err)
	}

	if len(emptyEvents) != 0 {
		t.Errorf("expected 0 events for nonexistent worker, got %d", len(emptyEvents))
	}
}
