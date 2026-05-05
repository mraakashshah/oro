package protocol_test

import (
	"context"
	"strings"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

// TestMigrateBeadSchemaRollsBackRebuildOnCheckViolation pins the property that
// MigrateBeadSchema's bead-table rebuild is atomic: if the INSERT...SELECT
// inside the rebuild violates a CHECK constraint, the entire rebuild rolls
// back so the original beads table and its data are preserved.
//
// Regression: oro-pyr2 — live incident 2026-05-05 ~00:20Z. A worker's
// in-flight TABLE REBUILD migration (running outside a transaction) failed
// midway when 61 existing rows had `type` values not in the new CHECK list.
// The non-transactional rebuild left the new beads table empty and stranded
// 1833 rows in beads_*_rebuild_old. All bead state was inaccessible to the
// dispatcher until manual recovery. The exact migration code wasn't
// preserved (worker branch was cleaned up), but the same failure mode lives
// in MigrateBeadSchema's status-rebuild path, so we pin it there.
func TestMigrateBeadSchemaRollsBackRebuildOnCheckViolation(t *testing.T) {
	db, err := dbutil.OpenDB(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("exec runtime schema: %v", err)
	}
	// Old beads schema with NO status CHECK so we can seed a row whose value
	// the new schema's CHECK rejects.
	if _, err := db.ExecContext(ctx, `
CREATE TABLE beads (
    id                    TEXT PRIMARY KEY,
    title                 TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    acceptance_criteria   TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL,
    priority              INTEGER NOT NULL DEFAULT 2,
    type                  TEXT NOT NULL DEFAULT 'task',
    parent_id             TEXT,
    owner                 TEXT,
    estimated_minutes     INTEGER,
    tier                  TEXT,
    model                 TEXT,
    deferred_until        TEXT,
    close_reason          TEXT,
    created_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    closed_at             TEXT,
    deleted               INTEGER NOT NULL DEFAULT 0
);
INSERT INTO beads (id, title, status) VALUES
    ('oro-good', 'has a valid status', 'open'),
    ('oro-rogue', 'has an invalid status', 'rogue');
`); err != nil {
		t.Fatalf("seed old bead schema with rogue row: %v", err)
	}

	err = protocol.MigrateBeadSchema(ctx, db)
	if err == nil {
		t.Fatal("expected MigrateBeadSchema to fail when existing data violates new CHECK constraint, got nil")
	}
	if !strings.Contains(err.Error(), "rebuild") && !strings.Contains(err.Error(), "CHECK") {
		t.Logf("error message: %v (acceptable, but should mention rebuild or CHECK)", err)
	}

	var rogueStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM beads WHERE id='oro-rogue'`).Scan(&rogueStatus); err != nil {
		t.Fatalf("rogue row not preserved after failed rebuild: %v", err)
	}
	if rogueStatus != "rogue" {
		t.Fatalf("rogue status = %q, want 'rogue' (data must be preserved on rollback)", rogueStatus)
	}

	var goodStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM beads WHERE id='oro-good'`).Scan(&goodStatus); err != nil {
		t.Fatalf("good row not preserved after failed rebuild: %v", err)
	}
	if goodStatus != "open" {
		t.Fatalf("good status = %q, want 'open'", goodStatus)
	}

	var leftover string
	queryErr := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='beads_status_rebuild_old'`).Scan(&leftover)
	if queryErr == nil {
		t.Fatalf("beads_status_rebuild_old table left over after failed rebuild — rebuild must be transactional")
	}
}
