package protocol_test

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"oro/pkg/protocol"
)

// openTestDB creates an in-memory SQLite database with schema applied.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}
	return db
}

func TestEventFieldsMatchSchema(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		"INSERT INTO events (type, source, bead_id, worker_id, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		"assign", "dispatcher", "bead-1", "worker-1", `{"key":"val"}`, now,
	)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	row := db.QueryRow("SELECT id, type, source, bead_id, worker_id, payload, created_at FROM events WHERE id = 1")
	var e protocol.Event
	var beadID, workerID, payload sql.NullString
	err = row.Scan(&e.ID, &e.Type, &e.Source, &beadID, &workerID, &payload, &e.CreatedAt)
	if err != nil {
		t.Fatalf("scan event: %v", err)
	}
	if beadID.Valid {
		e.BeadID = beadID.String
	}
	if workerID.Valid {
		e.WorkerID = workerID.String
	}
	if payload.Valid {
		e.Payload = payload.String
	}

	if e.ID != 1 {
		t.Errorf("expected ID 1, got %d", e.ID)
	}
	if e.Type != "assign" {
		t.Errorf("expected type 'assign', got %q", e.Type)
	}
	if e.Source != "dispatcher" {
		t.Errorf("expected source 'dispatcher', got %q", e.Source)
	}
	if e.BeadID != "bead-1" {
		t.Errorf("expected bead_id 'bead-1', got %q", e.BeadID)
	}
	if e.WorkerID != "worker-1" {
		t.Errorf("expected worker_id 'worker-1', got %q", e.WorkerID)
	}
	if e.Payload != `{"key":"val"}` {
		t.Errorf("expected payload, got %q", e.Payload)
	}
}

func TestAssignmentFieldsMatchSchema(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		"INSERT INTO assignments (bead_id, worker_id, worktree, status, assigned_at, completed_at) VALUES (?, ?, ?, ?, ?, ?)",
		"bead-1", "worker-1", "/tmp/.worktrees/feat", "active", now, now,
	)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}

	row := db.QueryRow("SELECT id, bead_id, worker_id, worktree, status, assigned_at, completed_at FROM assignments WHERE id = 1")
	var a protocol.Assignment
	var completedAt sql.NullString
	err = row.Scan(&a.ID, &a.BeadID, &a.WorkerID, &a.Worktree, &a.Status, &a.AssignedAt, &completedAt)
	if err != nil {
		t.Fatalf("scan assignment: %v", err)
	}
	if completedAt.Valid {
		a.CompletedAt = completedAt.String
	}

	if a.ID != 1 {
		t.Errorf("expected ID 1, got %d", a.ID)
	}
	if a.BeadID != "bead-1" {
		t.Errorf("expected bead_id 'bead-1', got %q", a.BeadID)
	}
	if a.Worktree != "/tmp/.worktrees/feat" {
		t.Errorf("expected worktree, got %q", a.Worktree)
	}
	if a.Status != "active" {
		t.Errorf("expected status 'active', got %q", a.Status)
	}
}

func TestCommandRowFieldsMatchSchema(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	_, err := db.Exec(
		"INSERT INTO commands (directive, args, status) VALUES (?, ?, ?)",
		"focus", "epic-123", "pending",
	)
	if err != nil {
		t.Fatalf("insert command: %v", err)
	}

	row := db.QueryRow("SELECT id, directive, args, status, created_at, processed_at FROM commands WHERE id = 1")
	var c protocol.CommandRow
	var args, processedAt sql.NullString
	err = row.Scan(&c.ID, &c.Directive, &args, &c.Status, &c.CreatedAt, &processedAt)
	if err != nil {
		t.Fatalf("scan command: %v", err)
	}
	if args.Valid {
		c.Args = args.String
	}
	if processedAt.Valid {
		c.ProcessedAt = processedAt.String
	}

	if c.ID != 1 {
		t.Errorf("expected ID 1, got %d", c.ID)
	}
	if c.Directive != "focus" {
		t.Errorf("expected directive 'focus', got %q", c.Directive)
	}
	if c.Args != "epic-123" {
		t.Errorf("expected args 'epic-123', got %q", c.Args)
	}
	if c.Status != "pending" {
		t.Errorf("expected status 'pending', got %q", c.Status)
	}
}

func TestMemoryFieldsMatchSchema(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	_, err := db.Exec(
		"INSERT INTO memories (content, type, tags, source, bead_id, worker_id, confidence) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"Use uv sync not pip", "learned", `["python","uv"]`, "worker-1", "bead-1", "worker-1", 0.95,
	)
	if err != nil {
		t.Fatalf("insert memory: %v", err)
	}

	row := db.QueryRow("SELECT id, content, type, tags, source, bead_id, worker_id, confidence, created_at, embedding FROM memories WHERE id = 1")
	var m protocol.Memory
	var tags, beadID, workerID sql.NullString
	var embedding []byte
	var confidence sql.NullFloat64
	err = row.Scan(&m.ID, &m.Content, &m.Type, &tags, &m.Source, &beadID, &workerID, &confidence, &m.CreatedAt, &embedding)
	if err != nil {
		t.Fatalf("scan memory: %v", err)
	}
	if tags.Valid {
		m.Tags = tags.String
	}
	if beadID.Valid {
		m.BeadID = beadID.String
	}
	if workerID.Valid {
		m.WorkerID = workerID.String
	}
	if confidence.Valid {
		m.Confidence = confidence.Float64
	}
	if embedding != nil {
		m.Embedding = embedding
	}

	if m.Content != "Use uv sync not pip" {
		t.Errorf("expected content, got %q", m.Content)
	}
	if m.Type != "learned" {
		t.Errorf("expected type 'learned', got %q", m.Type)
	}
	if m.Tags != `["python","uv"]` {
		t.Errorf("expected tags JSON, got %q", m.Tags)
	}
	if m.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", m.Confidence)
	}
	if m.Embedding != nil {
		t.Errorf("expected nil embedding, got %v", m.Embedding)
	}
}

func TestMemoryTagsJSON(t *testing.T) {
	t.Parallel()

	m := protocol.Memory{
		Tags: `["python","uv","pip"]`,
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal memory: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}

	if _, ok := raw["tags"]; !ok {
		t.Fatal("expected 'tags' key in JSON output")
	}

	var got protocol.Memory
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal memory: %v", err)
	}
	if got.Tags != m.Tags {
		t.Errorf("tags round-trip mismatch: want %q, got %q", m.Tags, got.Tags)
	}
}

func TestEventJSONRoundTrip(t *testing.T) {
	t.Parallel()

	e := protocol.Event{
		ID:        1,
		Type:      "assign",
		Source:    "dispatcher",
		BeadID:    "bead-1",
		WorkerID:  "worker-1",
		Payload:   `{"key":"val"}`,
		CreatedAt: "2026-02-07T10:00:00Z",
	}

	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got protocol.Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got != e {
		t.Errorf("round-trip mismatch:\n  want: %+v\n  got:  %+v", e, got)
	}
}

func TestAssignmentJSONRoundTrip(t *testing.T) {
	t.Parallel()

	a := protocol.Assignment{
		ID:          1,
		BeadID:      "bead-1",
		WorkerID:    "worker-1",
		Worktree:    "/tmp/.worktrees/feat",
		Status:      "active",
		AssignedAt:  "2026-02-07T10:00:00Z",
		CompletedAt: "2026-02-07T11:00:00Z",
	}

	data, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got protocol.Assignment
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got != a {
		t.Errorf("round-trip mismatch:\n  want: %+v\n  got:  %+v", a, got)
	}
}

func TestCommandRowJSONRoundTrip(t *testing.T) {
	t.Parallel()

	c := protocol.CommandRow{
		ID:          1,
		Directive:   "focus",
		Args:        "epic-123",
		Status:      "pending",
		CreatedAt:   "2026-02-07T10:00:00Z",
		ProcessedAt: "",
	}

	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got protocol.CommandRow
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got != c {
		t.Errorf("round-trip mismatch:\n  want: %+v\n  got:  %+v", c, got)
	}
}
