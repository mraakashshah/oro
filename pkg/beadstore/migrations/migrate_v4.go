package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const v4BeadColumns = `id, title, contract_version, draft, description, acceptance_criteria, status, priority, type, parent_id, owner, estimated_minutes, tier, model, deferred_until, close_reason, created_at, updated_at, closed_at, deleted, next_action, blockers, linked_artifacts, worker_state, pipeline_stage, sandbox_session, allowed_external_fns, context_thresholds`

const v4BeadTableDDL = `
CREATE TABLE beads (
    id                    TEXT PRIMARY KEY,
    title                 TEXT NOT NULL,
    contract_version      INTEGER NOT NULL DEFAULT 0,
    draft                 INTEGER NOT NULL DEFAULT 0,
    description           TEXT NOT NULL DEFAULT '',
    acceptance_criteria   TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL CHECK (status IN
                          ('open','in_progress','blocked','closed')),
    priority              INTEGER NOT NULL DEFAULT 2,
    type                  TEXT NOT NULL DEFAULT 'task' CHECK (type IN
                          ('task','bug','epic','research','chore','review')),
    parent_id             TEXT REFERENCES beads(id),
    owner                 TEXT,
    estimated_minutes     INTEGER,
    tier                  TEXT,
    model                 TEXT,
    deferred_until        TEXT,
    close_reason          TEXT,
    created_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    closed_at             TEXT,
    deleted               INTEGER NOT NULL DEFAULT 0,
    next_action           TEXT,
    blockers              TEXT,
    linked_artifacts      TEXT,
    worker_state          TEXT,
    pipeline_stage        TEXT CHECK (pipeline_stage IN ('assess','plan','prepare','execute','validate','evolve','none')),
    sandbox_session       TEXT,
    allowed_external_fns  TEXT,
    context_thresholds    TEXT
);
`

const v4BeadsFTSTriggersDDL = `
CREATE TRIGGER IF NOT EXISTS beads_fts_ai AFTER INSERT ON beads BEGIN
  INSERT INTO beads_fts(rowid, title, description, acceptance_criteria)
  VALUES (new.rowid, new.title, new.description, new.acceptance_criteria);
END;
CREATE TRIGGER IF NOT EXISTS beads_fts_ad AFTER DELETE ON beads BEGIN
  INSERT INTO beads_fts(beads_fts, rowid, title, description, acceptance_criteria)
  VALUES ('delete', old.rowid, old.title, old.description, old.acceptance_criteria);
END;
CREATE TRIGGER IF NOT EXISTS beads_fts_au AFTER UPDATE ON beads BEGIN
  INSERT INTO beads_fts(beads_fts, rowid, title, description, acceptance_criteria)
  VALUES ('delete', old.rowid, old.title, old.description, old.acceptance_criteria);
  INSERT INTO beads_fts(rowid, title, description, acceptance_criteria)
  VALUES (new.rowid, new.title, new.description, new.acceptance_criteria);
END;
`

const v4DropBeadsFTSTriggersDDL = `
DROP TRIGGER IF EXISTS beads_ai;
DROP TRIGGER IF EXISTS beads_ad;
DROP TRIGGER IF EXISTS beads_au;
DROP TRIGGER IF EXISTS beads_fts_ai;
DROP TRIGGER IF EXISTS beads_fts_ad;
DROP TRIGGER IF EXISTS beads_fts_au;
`

// MigrateToV4 removes the legacy premortem-as-bead-type schema and data.
func MigrateToV4(ctx context.Context, db *sql.DB) error {
	needed, err := needsV4Migration(ctx, db)
	if err != nil {
		return err
	}
	if !needed {
		return EnsureV4BeadsFTSTriggers(ctx, db)
	}
	if err := ensureNoActiveAssignments(ctx, db); err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate v4 begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := execV4MigrationSteps(ctx, tx); err != nil {
		return err
	}
	if err := checkForeignKeys(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate v4 commit: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA legacy_alter_table=OFF`); err != nil {
		return fmt.Errorf("migrate v4 disable legacy_alter_table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 4`); err != nil {
		return fmt.Errorf("migrate v4 mark user_version: %w", err)
	}
	return nil
}

func execV4MigrationSteps(ctx context.Context, tx *sql.Tx) error {
	steps := []string{
		`INSERT INTO bead_journey (bead_id, ts, actor, event, payload)
		 SELECT id, strftime('%Y-%m-%dT%H:%M:%fZ','now'), 'migration', 'migration_type_converted',
		        '{"original_type":"premortem","reason":"premortem-excision"}'
		   FROM beads WHERE type='premortem'`,
		`UPDATE beads
		    SET deleted=1,
		        type='task',
		        pipeline_stage = CASE WHEN pipeline_stage = 'premortem' THEN 'none' ELSE pipeline_stage END,
		        close_reason='premortem-excision: auto-soft-deleted by migrate_v4',
		        updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		  WHERE type='premortem'`,
		`UPDATE beads SET pipeline_stage='none' WHERE pipeline_stage='premortem'`,
		`DELETE FROM bead_metadata WHERE key IN ('premortem_verdict','premortem_reason')`,
		`DELETE FROM bead_journey WHERE actor='premortem'`,
		`DROP VIEW IF EXISTS beads_ready`,
		`DROP VIEW IF EXISTS beads_blocked`,
		v4DropBeadsFTSTriggersDDL,
		`PRAGMA legacy_alter_table=ON`,
		`ALTER TABLE beads RENAME TO beads_v4_rebuild_old`,
		v4BeadTableDDL,
		`INSERT INTO beads (` + v4BeadColumns + `) SELECT ` + v4BeadColumns + ` FROM beads_v4_rebuild_old`,
		`DROP TABLE beads_v4_rebuild_old`,
		v3ViewsDDL,
		v4BeadsFTSTriggersDDL,
		`INSERT INTO beads_fts(beads_fts) VALUES('rebuild')`,
	}
	for _, stmt := range steps {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate v4: %w", err)
		}
	}
	return nil
}

// RepairV4BeadsFTSTriggers removes the transient bad v4 trigger family that
// wrote non-existent metadata columns into beads_fts, then reinstalls the
// canonical content-column triggers.
func RepairV4BeadsFTSTriggers(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("repair v4 fts triggers begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, v4DropBeadsFTSTriggersDDL); err != nil {
		return fmt.Errorf("repair v4 fts triggers drop: %w", err)
	}
	if _, err := tx.ExecContext(ctx, v4BeadsFTSTriggersDDL); err != nil {
		return fmt.Errorf("repair v4 fts triggers create: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO beads_fts(beads_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("repair v4 fts rebuild: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("repair v4 fts triggers commit: %w", err)
	}
	return nil
}

// EnsureV4BeadsFTSTriggers repairs FTS triggers only when the legacy bad
// trigger family is present or the canonical trigger family is incomplete.
func EnsureV4BeadsFTSTriggers(ctx context.Context, db *sql.DB) error {
	needed, err := v4BeadsFTSTriggersNeedRepair(ctx, db)
	if err != nil {
		return err
	}
	if !needed {
		return nil
	}
	return RepairV4BeadsFTSTriggers(ctx, db)
}

func v4BeadsFTSTriggersNeedRepair(ctx context.Context, db *sql.DB) (bool, error) {
	var badTriggers int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name IN ('beads_ai','beads_ad','beads_au')`).Scan(&badTriggers); err != nil {
		return false, fmt.Errorf("inspect bad v4 fts triggers: %w", err)
	}
	if badTriggers > 0 {
		return true, nil
	}

	rows, err := db.QueryContext(ctx, `SELECT name, sql FROM sqlite_schema WHERE type='trigger' AND name IN ('beads_fts_ai','beads_fts_ad','beads_fts_au')`)
	if err != nil {
		return false, fmt.Errorf("inspect canonical v4 fts triggers: %w", err)
	}
	defer rows.Close()
	canonical := map[string]bool{
		"beads_fts_ai": false,
		"beads_fts_ad": false,
		"beads_fts_au": false,
	}
	for rows.Next() {
		var name, sqlText string
		if err := rows.Scan(&name, &sqlText); err != nil {
			return false, fmt.Errorf("scan canonical v4 fts trigger: %w", err)
		}
		if strings.Contains(sqlText, "status, type, parent_id, owner") {
			return true, nil
		}
		canonical[name] = true
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate canonical v4 fts triggers: %w", err)
	}
	for _, present := range canonical {
		if !present {
			return true, nil
		}
	}
	return false, nil
}

func needsV4Migration(ctx context.Context, db *sql.DB) (bool, error) {
	var userVersion int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		return false, fmt.Errorf("migrate v4 user_version: %w", err)
	}
	if userVersion >= 4 {
		return false, nil
	}
	hasGateColumn, err := beadsColumnExists(ctx, db, "gate_state")
	if err != nil {
		return false, err
	}
	if hasGateColumn {
		return true, nil
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 4`); err != nil {
		return false, fmt.Errorf("migrate v4 mark user_version: %w", err)
	}
	return false, nil
}

func ensureNoActiveAssignments(ctx context.Context, db *sql.DB) error {
	var activeAssignments int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignments WHERE status='active'`).Scan(&activeAssignments); err != nil {
		return fmt.Errorf("migrate v4 active assignment check: %w", err)
	}
	if activeAssignments > 0 {
		return fmt.Errorf("migrate_v4: cannot migrate while %d active assignments exist; run 'oro stop' first then re-run 'oro start'", activeAssignments)
	}
	return nil
}

func checkForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("migrate v4 foreign_key_check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("migrate v4 foreign_key_check failed")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("migrate v4 foreign_key_check rows: %w", err)
	}
	return nil
}

func beadsColumnExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(beads)`)
	if err != nil {
		return false, fmt.Errorf("inspect beads columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var colName, colType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &defaultValue, &pk); err != nil {
			return false, fmt.Errorf("scan beads column: %w", err)
		}
		if colName == name {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate beads columns: %w", err)
	}
	return false, nil
}
