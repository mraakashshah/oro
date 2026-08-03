// Package migrations provides additive schema migration steps for the beadstore database.
package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"oro/pkg/protocol"
)

// MigrateToV3 applies the v20 → v3 additive schema changes to db.
// It is idempotent: applying it to an already-migrated database is a no-op.
//
// Changes applied:
//   - §4.6.a: 4 new columns on beads (next_action, blockers, linked_artifacts, worker_state)
//   - §4.6.b: 6 new columns on beads (gate_state, premortem_cycle_count, pipeline_stage,
//     sandbox_session, allowed_external_fns, context_thresholds)
//   - §4.6.d: 4 new tables (bead_journey, cards, bead_learnings_pending, card_events) + indexes
//   - §4.6.e: beads_ready and beads_blocked views amended with awaits_parent_close clause
func MigrateToV3(ctx context.Context, db *sql.DB) error {
	var userVersion int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		return fmt.Errorf("migrate v3 user_version: %w", err)
	}
	if userVersion >= 4 {
		return nil
	}

	// §4.6.a + §4.6.b — bead-table column additions (10 total).
	alters := []string{
		`ALTER TABLE beads ADD COLUMN next_action      TEXT`,
		`ALTER TABLE beads ADD COLUMN blockers         TEXT`,
		`ALTER TABLE beads ADD COLUMN linked_artifacts TEXT`,
		`ALTER TABLE beads ADD COLUMN worker_state     TEXT`,
		`ALTER TABLE beads ADD COLUMN gate_state TEXT NOT NULL DEFAULT 'none'
		   CHECK (gate_state IN ('none','eligible','satisfied','blocked','replan','escalated'))`,
		`ALTER TABLE beads ADD COLUMN premortem_cycle_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE beads ADD COLUMN pipeline_stage TEXT
		   CHECK (pipeline_stage IN ('assess','plan','premortem','prepare','execute','validate','evolve','none'))`,
		`ALTER TABLE beads ADD COLUMN sandbox_session       TEXT`,
		`ALTER TABLE beads ADD COLUMN allowed_external_fns  TEXT`,
		`ALTER TABLE beads ADD COLUMN context_thresholds    TEXT`,
	}
	for _, stmt := range alters {
		if err := tryAlterTableAddColumn(ctx, db, stmt); err != nil {
			return err
		}
	}

	// §4.6.d — new tables and indexes.
	if _, err := db.ExecContext(ctx, v3TablesDDL); err != nil {
		return fmt.Errorf("migrate v3 tables: %w", err)
	}

	// §4.6.e — view rewrites (drop-and-recreate is inherently idempotent).
	if _, err := db.ExecContext(ctx, protocol.BeadQueueViewsDDL); err != nil {
		return fmt.Errorf("migrate v3 views: %w", err)
	}

	return nil
}

// tryAlterTableAddColumn executes an ALTER TABLE ... ADD COLUMN statement and
// silences the "duplicate column name" error SQLite returns when the column
// already exists, making the step idempotent.
func tryAlterTableAddColumn(ctx context.Context, db *sql.DB, stmt string) error {
	_, err := db.ExecContext(ctx, stmt)
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("alter table: %w", err)
	}
	return nil
}

// v3TablesDDL creates the four new v3 tables and their indexes.
// cards is created before bead_learnings_pending because the latter references cards(id).
const v3TablesDDL = `
CREATE TABLE IF NOT EXISTS bead_journey (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  bead_id TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
  ts      TEXT NOT NULL,
  actor   TEXT NOT NULL,
  event   TEXT NOT NULL,
  payload TEXT
);
CREATE INDEX IF NOT EXISTS idx_journey_bead_ts ON bead_journey(bead_id, ts);
CREATE INDEX IF NOT EXISTS idx_journey_ts      ON bead_journey(ts);

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
  emerged_from         TEXT REFERENCES beads(id),
  retired_reason       TEXT
);
CREATE INDEX IF NOT EXISTS idx_cards_type_score ON cards(type, score DESC) WHERE retired_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cards_tags       ON cards(tags);

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
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  card_id TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
  ts      TEXT NOT NULL,
  bead_id TEXT REFERENCES beads(id),
  actor   TEXT NOT NULL,
  kind    TEXT NOT NULL,
  payload TEXT
);
CREATE INDEX IF NOT EXISTS idx_card_events_card_ts ON card_events(card_id, ts);
`
