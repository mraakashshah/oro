package protocol_test

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"oro/pkg/protocol"
)

// seedIntegrationDB inserts one row into each table for integration testing.
func seedIntegrationDB(t *testing.T, db *sql.DB) {
	t.Helper()

	_, err := db.Exec(
		"INSERT INTO events (type, source, bead_id, worker_id, payload) VALUES (?, ?, ?, ?, ?)",
		"assign", "dispatcher", "bead-1", "worker-1", `{"worktree":"/tmp/w1"}`,
	)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	_, err = db.Exec(
		"INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, ?)",
		"bead-1", "worker-1", "/tmp/.worktrees/bead-1", "active",
	)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}

	_, err = db.Exec(
		"INSERT INTO commands (directive, args, status) VALUES (?, ?, ?)",
		"focus", "epic-42", "pending",
	)
	if err != nil {
		t.Fatalf("insert command: %v", err)
	}

	_, err = db.Exec(
		"INSERT INTO memories (content, type, tags, source, bead_id, worker_id, confidence) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"Always rebase before merge to keep linear history", "learned", "git,workflow", "worker-1", "bead-1", "worker-1", 0.9,
	)
	if err != nil {
		t.Fatalf("insert memory: %v", err)
	}
}

func TestIntegrationScanEvent(t *testing.T) {
	db := openTestDB(t)
	seedIntegrationDB(t, db)

	var e protocol.Event
	var beadID, workerID, payload sql.NullString
	err := db.QueryRow("SELECT id, type, source, bead_id, worker_id, payload, created_at FROM events WHERE id = 1").
		Scan(&e.ID, &e.Type, &e.Source, &beadID, &workerID, &payload, &e.CreatedAt)
	if err != nil {
		t.Fatalf("scan event: %v", err)
	}
	if e.Type != "assign" {
		t.Errorf("event type: want %q, got %q", "assign", e.Type)
	}
	if e.Source != "dispatcher" {
		t.Errorf("event source: want %q, got %q", "dispatcher", e.Source)
	}
	if !beadID.Valid || beadID.String != "bead-1" {
		t.Errorf("event bead_id: want %q, got %v", "bead-1", beadID)
	}
	if e.CreatedAt == "" {
		t.Error("event created_at should be populated by DEFAULT")
	}
}

func TestIntegrationScanAssignment(t *testing.T) {
	db := openTestDB(t)
	seedIntegrationDB(t, db)

	var a protocol.Assignment
	var completedAt sql.NullString
	err := db.QueryRow("SELECT id, bead_id, worker_id, worktree, status, assigned_at, completed_at FROM assignments WHERE id = 1").
		Scan(&a.ID, &a.BeadID, &a.WorkerID, &a.Worktree, &a.Status, &a.AssignedAt, &completedAt)
	if err != nil {
		t.Fatalf("scan assignment: %v", err)
	}
	if a.Status != "active" {
		t.Errorf("assignment status: want %q, got %q", "active", a.Status)
	}
	if a.AssignedAt == "" {
		t.Error("assignment assigned_at should be populated by DEFAULT")
	}
}

func TestIntegrationScanCommand(t *testing.T) {
	db := openTestDB(t)
	seedIntegrationDB(t, db)

	var c protocol.CommandRow
	var args, processedAt sql.NullString
	err := db.QueryRow("SELECT id, directive, args, status, created_at, processed_at FROM commands WHERE id = 1").
		Scan(&c.ID, &c.Directive, &args, &c.Status, &c.CreatedAt, &processedAt)
	if err != nil {
		t.Fatalf("scan command: %v", err)
	}
	if c.Directive != "focus" {
		t.Errorf("command directive: want %q, got %q", "focus", c.Directive)
	}
	if c.Status != "pending" {
		t.Errorf("command status: want %q, got %q", "pending", c.Status)
	}
}

func TestIntegrationScanMemory(t *testing.T) {
	db := openTestDB(t)
	seedIntegrationDB(t, db)

	var m protocol.Memory
	var tags, beadID, workerID sql.NullString
	var confidence sql.NullFloat64
	var embedding []byte
	err := db.QueryRow("SELECT id, content, type, tags, source, bead_id, worker_id, confidence, created_at, embedding FROM memories WHERE id = 1").
		Scan(&m.ID, &m.Content, &m.Type, &tags, &m.Source, &beadID, &workerID, &confidence, &m.CreatedAt, &embedding)
	if err != nil {
		t.Fatalf("scan memory: %v", err)
	}
	if m.Content != "Always rebase before merge to keep linear history" {
		t.Errorf("memory content mismatch")
	}
	if !confidence.Valid || confidence.Float64 != 0.9 {
		t.Errorf("memory confidence: want 0.9, got %v", confidence)
	}
}

func TestIntegrationFTS5Search(t *testing.T) {
	db := openTestDB(t)
	seedIntegrationDB(t, db)

	var id int64
	var rank float64
	err := db.QueryRow(
		"SELECT rowid, rank FROM memories_fts WHERE memories_fts MATCH ? ORDER BY rank LIMIT 1",
		"rebase merge linear",
	).Scan(&id, &rank)
	if err != nil {
		t.Fatalf("FTS5 search: %v", err)
	}
	if id != 1 {
		t.Errorf("FTS5 match: want rowid 1, got %d", id)
	}
}

func TestIntegrationFTS5UpdateTrigger(t *testing.T) {
	db := openTestDB(t)
	seedIntegrationDB(t, db)

	_, err := db.Exec("UPDATE memories SET content = ?, tags = ? WHERE id = 1",
		"Never force-push to shared branches", "git,safety")
	if err != nil {
		t.Fatalf("update memory: %v", err)
	}

	var count int
	err = db.QueryRow(
		"SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH ?",
		"rebase",
	).Scan(&count)
	if err != nil {
		t.Fatalf("FTS5 search after update: %v", err)
	}
	if count != 0 {
		t.Errorf("old content still in FTS index: got %d matches, want 0", count)
	}

	var id int64
	err = db.QueryRow(
		"SELECT rowid FROM memories_fts WHERE memories_fts MATCH ? LIMIT 1",
		"shared branches",
	).Scan(&id)
	if err != nil {
		t.Fatalf("FTS5 search for new content: %v", err)
	}
	if id != 1 {
		t.Errorf("FTS5 update match: want rowid 1, got %d", id)
	}
}

func TestIntegrationFTS5DeleteTrigger(t *testing.T) {
	db := openTestDB(t)
	seedIntegrationDB(t, db)

	_, err := db.Exec("DELETE FROM memories WHERE id = 1")
	if err != nil {
		t.Fatalf("delete memory: %v", err)
	}

	var count int
	err = db.QueryRow(
		"SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH ?",
		"rebase",
	).Scan(&count)
	if err != nil {
		t.Fatalf("FTS5 search after delete: %v", err)
	}
	if count != 0 {
		t.Errorf("deleted content still in FTS index: got %d matches, want 0", count)
	}
}

func TestIntegrationDefaultValues(t *testing.T) {
	db := openTestDB(t)

	_, err := db.Exec(
		"INSERT INTO events (type, source) VALUES (?, ?)",
		"heartbeat", "worker-2",
	)
	if err != nil {
		t.Fatalf("insert minimal event: %v", err)
	}

	var createdAt string
	err = db.QueryRow("SELECT created_at FROM events WHERE id = 1").Scan(&createdAt)
	if err != nil {
		t.Fatalf("scan minimal event: %v", err)
	}
	if createdAt == "" {
		t.Error("created_at DEFAULT not applied")
	}

	_, err = db.Exec(
		"INSERT INTO commands (directive) VALUES (?)", "start",
	)
	if err != nil {
		t.Fatalf("insert minimal command: %v", err)
	}

	var status, cmdCreatedAt string
	err = db.QueryRow("SELECT status, created_at FROM commands WHERE id = 1").Scan(&status, &cmdCreatedAt)
	if err != nil {
		t.Fatalf("scan minimal command: %v", err)
	}
	if status != "pending" {
		t.Errorf("command default status: want %q, got %q", "pending", status)
	}
	if cmdCreatedAt == "" {
		t.Error("command created_at DEFAULT not applied")
	}

	res, err := db.Exec(
		"INSERT INTO memories (content, type, source) VALUES (?, ?, ?)",
		"Test memory", "learned", "test",
	)
	if err != nil {
		t.Fatalf("insert minimal memory: %v", err)
	}
	memID, _ := res.LastInsertId()

	var confidence float64
	err = db.QueryRow("SELECT confidence FROM memories WHERE id = ?", memID).Scan(&confidence)
	if err != nil {
		t.Fatalf("scan minimal memory confidence: %v", err)
	}
	if confidence != 0.8 {
		t.Errorf("memory default confidence: want 0.8, got %f", confidence)
	}
}
