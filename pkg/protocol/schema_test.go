package protocol_test

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"oro/pkg/protocol"
)

func TestSchemaExecsCleanly(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}
}

func TestSchemaCreatesExpectedTables(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	expected := []string{"events", "assignments", "commands", "memories", "memories_fts"}
	for _, table := range expected {
		var name string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type IN ('table','view') AND name = ?",
			table,
		).Scan(&name)
		if err != nil {
			t.Errorf("expected table %q not found: %v", table, err)
		}
	}
}

func TestSchemaIsIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Execute twice — IF NOT EXISTS should prevent errors
	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("first exec: %v", err)
	}
	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("second exec (idempotency): %v", err)
	}
}
