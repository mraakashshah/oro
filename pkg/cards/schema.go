package cards

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaDDL defines the SQLite tables for the cards store.
// Applied idempotently via CREATE TABLE IF NOT EXISTS.
const schemaDDL = `
CREATE TABLE IF NOT EXISTS cards (
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

CREATE INDEX IF NOT EXISTS idx_cards_type_score ON cards(type, score DESC) WHERE retired_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cards_tags       ON cards(tags);

CREATE TABLE IF NOT EXISTS card_symbols (
  card_id TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
  symbol  TEXT NOT NULL,
  PRIMARY KEY (card_id, symbol)
);

CREATE INDEX IF NOT EXISTS idx_card_symbols_symbol ON card_symbols(symbol);

CREATE TABLE IF NOT EXISTS bead_learnings_pending (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  bead_id              TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
  ts                   TEXT NOT NULL,
  candidate            TEXT NOT NULL,
  promoted_to          TEXT REFERENCES cards(id),
  rejected_at          TEXT,
  reason               TEXT,
  queued_for_review_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_learnings_bead    ON bead_learnings_pending(bead_id);
CREATE INDEX IF NOT EXISTS idx_learnings_pending ON bead_learnings_pending(promoted_to, rejected_at);
CREATE INDEX IF NOT EXISTS idx_learnings_review  ON bead_learnings_pending(queued_for_review_at)
  WHERE queued_for_review_at IS NOT NULL AND promoted_to IS NULL AND rejected_at IS NULL;

CREATE TABLE IF NOT EXISTS card_events (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  card_id   TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
  ts        TEXT NOT NULL,
  bead_id   TEXT,
  actor     TEXT NOT NULL,
  kind      TEXT NOT NULL,
  payload   TEXT
);

CREATE INDEX IF NOT EXISTS idx_card_events_card_ts ON card_events(card_id, ts);

CREATE TABLE IF NOT EXISTS card_relations (
  source_id TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
  target_id TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
  signal    TEXT NOT NULL,
  strength  INTEGER NOT NULL,
  PRIMARY KEY (source_id, target_id, signal)
);

CREATE INDEX IF NOT EXISTS idx_card_relations_source ON card_relations(source_id);

CREATE TABLE IF NOT EXISTS card_symbols (
  card_id TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
  symbol  TEXT NOT NULL,
  PRIMARY KEY (card_id, symbol)
);

CREATE INDEX IF NOT EXISTS idx_card_symbols_symbol ON card_symbols(symbol);
`

func ensureColumn(db *sql.DB, table, col, ddl string) error {
	rows, err := db.QueryContext(context.Background(), `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan %s columns: %w", table, err)
		}
		if name == col {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate %s columns: %w", table, err)
	}
	if _, err := db.ExecContext(context.Background(), ddl); err != nil {
		return fmt.Errorf("add %s.%s column: %w", table, col, err)
	}
	return nil
}
