package cards_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"oro/pkg/cards"
	"oro/pkg/dbutil"
)

func TestSchema_AddsRelationTablesAndSessionID(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cards.db")
	db := openSchemaTestDB(t, dbPath)

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE cards (
			id                   TEXT PRIMARY KEY,
			type                 TEXT NOT NULL CHECK (type IN ('rule','taste','pattern','decision','fact')),
			title                TEXT NOT NULL,
			body_summary         TEXT NOT NULL,
			body_full            TEXT NOT NULL,
			body_deep            TEXT,
			tags                 TEXT NOT NULL DEFAULT '[]',
			score                REAL NOT NULL DEFAULT 1.0,
			promotion_confidence REAL,
			decay_anchor         TEXT NOT NULL,
			last_contradicted_at TEXT,
			last_nacked_at       TEXT,
			created_at           TEXT NOT NULL,
			updated_at           TEXT NOT NULL,
			retired_at           TEXT,
			superseded_by        TEXT REFERENCES cards(id),
			emerged_from         TEXT,
			retired_reason       TEXT
		);
		CREATE TABLE card_events (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			card_id   TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
			ts        TEXT NOT NULL,
			bead_id   TEXT,
			actor     TEXT NOT NULL,
			kind      TEXT NOT NULL,
			payload   TEXT
		);
		INSERT INTO cards (id, type, title, body_summary, body_full, tags, decay_anchor, created_at, updated_at)
		VALUES ('card-existing', 'pattern', 'existing', 'summary', 'body', '[]',
			'2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO card_events (card_id, ts, actor, kind)
		VALUES ('card-existing', '2026-01-01T00:00:00Z', 'test', 'created');
	`); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}

	if _, err := cards.NewStore(db); err != nil {
		t.Fatalf("new store on legacy schema: %v", err)
	}
	assertTableExists(t, db, "card_relations")
	assertTableExists(t, db, "card_symbols")
	assertColumnExists(t, db, "card_events", "session_id")
	assertExistingCardSurvived(t, db)
	assertSessionIDNullable(t, db)

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	db = openSchemaTestDB(t, dbPath)
	if _, err := cards.NewStore(db); err != nil {
		t.Fatalf("reopen store after schema additions: %v", err)
	}
	assertTableExists(t, db, "card_relations")
	assertTableExists(t, db, "card_symbols")
	assertColumnExists(t, db, "card_events", "session_id")
	assertExistingCardSurvived(t, db)
}

func openSchemaTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func assertTableExists(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT name FROM sqlite_schema WHERE type = 'table' AND name = ?`, name).Scan(&got); err != nil {
		t.Fatalf("table %s exists: %v", name, err)
	}
}

func assertColumnExists(t *testing.T, db *sql.DB, table, col string) {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		if name == col {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	t.Fatalf("column %s.%s does not exist", table, col)
}

func assertExistingCardSurvived(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cards WHERE id = 'card-existing'`).Scan(&count); err != nil {
		t.Fatalf("count existing card: %v", err)
	}
	if count != 1 {
		t.Fatalf("existing card count = %d, want 1", count)
	}
}

func assertSessionIDNullable(t *testing.T, db *sql.DB) {
	t.Helper()
	var sessionID sql.NullString
	if err := db.QueryRow(`SELECT session_id FROM card_events WHERE card_id = 'card-existing'`).Scan(&sessionID); err != nil {
		t.Fatalf("query session_id: %v", err)
	}
	if sessionID.Valid {
		t.Fatalf("session_id = %q, want NULL", sessionID.String)
	}
}
