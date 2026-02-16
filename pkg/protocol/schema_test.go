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

func TestSchemaDDL(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	// Verify pane_activity table exists
	var name string
	err = db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='pane_activity'",
	).Scan(&name)
	if err != nil {
		t.Fatalf("pane_activity table not found: %v", err)
	}

	// Verify INSERT OR REPLACE works (idempotent upsert)
	_, err = db.Exec(`INSERT OR REPLACE INTO pane_activity VALUES ("architect", 1234567890)`)
	if err != nil {
		t.Fatalf("INSERT OR REPLACE into pane_activity: %v", err)
	}

	_, err = db.Exec(`INSERT OR REPLACE INTO pane_activity VALUES ("architect", 9999999999)`)
	if err != nil {
		t.Fatalf("second INSERT OR REPLACE (idempotent): %v", err)
	}

	var ts int64
	err = db.QueryRow(`SELECT last_seen FROM pane_activity WHERE pane='architect'`).Scan(&ts)
	if err != nil {
		t.Fatalf("query pane_activity: %v", err)
	}
	if ts != 9999999999 {
		t.Errorf("expected last_seen=9999999999, got %d", ts)
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
