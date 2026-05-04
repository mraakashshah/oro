package cards

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
`
