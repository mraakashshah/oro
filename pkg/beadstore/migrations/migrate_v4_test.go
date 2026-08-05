package migrations_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"oro/pkg/beadstore/migrations"
)

func TestMigrateToV4ExcisesPremortemSchemaAndData(t *testing.T) {
	ctx := context.Background()
	db := openV20DB(t)
	if err := migrations.MigrateToV3(ctx, db); err != nil {
		t.Fatalf("MigrateToV3: %v", err)
	}
	insertLegacyPremortemState(ctx, t, db)

	if err := migrations.MigrateToV4(ctx, db); err != nil {
		t.Fatalf("MigrateToV4: %v", err)
	}

	assertNoBeadsColumn(t, db, "gate_state")
	assertNoBeadsColumn(t, db, "premortem_cycle_count")
	assertBeadsColumn(t, db, "pipeline_stage")
	assertTableSQLExcludes(t, db, "premortem")

	if _, err := db.ExecContext(ctx, `INSERT INTO beads (id, title, status, type) VALUES ('bad-pm', 'bad', 'open', 'premortem')`); err == nil {
		t.Fatal("type=premortem insert succeeded after v4")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO beads (id, title, status, pipeline_stage) VALUES ('bad-stage', 'bad', 'open', 'premortem')`); err == nil {
		t.Fatal("pipeline_stage=premortem insert succeeded after v4")
	}

	var typ string
	var deleted int
	var closeReason string
	if err := db.QueryRowContext(ctx, `SELECT type, deleted, close_reason FROM beads WHERE id='pm-1'`).Scan(&typ, &deleted, &closeReason); err != nil {
		t.Fatalf("query converted premortem: %v", err)
	}
	if typ != "task" || deleted != 1 || !strings.Contains(closeReason, "premortem-excision") {
		t.Fatalf("converted premortem = type %q deleted %d reason %q", typ, deleted, closeReason)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM bead_metadata WHERE key IN ('premortem_verdict','premortem_reason')`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM bead_journey WHERE actor='premortem'`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM bead_journey WHERE actor='migration' AND event='migration_type_converted'`, 1)

	if _, err := db.ExecContext(ctx, `UPDATE beads SET status='in_progress' WHERE id='epic-1'`); err != nil {
		t.Fatalf("update bead after v4 migration: %v", err)
	}
	assertNoTrigger(t, db, "beads_ai")
	assertNoTrigger(t, db, "beads_ad")
	assertNoTrigger(t, db, "beads_au")

	if err := migrations.MigrateToV4(ctx, db); err != nil {
		t.Fatalf("MigrateToV4 second call: %v", err)
	}
	var userVersion int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if userVersion != 4 {
		t.Fatalf("user_version = %d, want 4", userVersion)
	}
}

func TestMigrateToV4RepairsLegacyBadFTSTriggers(t *testing.T) {
	ctx := context.Background()
	db := openV20DB(t)
	if err := migrations.MigrateToV3(ctx, db); err != nil {
		t.Fatalf("MigrateToV3: %v", err)
	}
	if err := migrations.MigrateToV4(ctx, db); err != nil {
		t.Fatalf("MigrateToV4: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 4`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TRIGGER beads_ai AFTER INSERT ON beads BEGIN
  INSERT INTO beads_fts(rowid, title, description, acceptance_criteria, status, type, parent_id, owner)
  VALUES (new.rowid, new.title, new.description, new.acceptance_criteria, new.status, new.type, new.parent_id, new.owner);
END;`); err != nil {
		t.Fatalf("install legacy bad trigger: %v", err)
	}
	if err := migrations.MigrateToV4(ctx, db); err != nil {
		t.Fatalf("MigrateToV4 repair: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE beads SET status='in_progress' WHERE id='epic-1'`); err != nil {
		t.Fatalf("update bead after repair: %v", err)
	}
	assertNoTrigger(t, db, "beads_ai")
}

func TestMigrateToV4RejectsActiveAssignments(t *testing.T) {
	ctx := context.Background()
	db := openV20DB(t)
	if err := migrations.MigrateToV3(ctx, db); err != nil {
		t.Fatalf("MigrateToV3: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO beads (id, title, status) VALUES ('active', 'active', 'open')`); err != nil {
		t.Fatalf("insert bead: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('active', 'w', 'wt', 'active')`); err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	err := migrations.MigrateToV4(ctx, db)
	if err == nil || !strings.Contains(err.Error(), "active assignments") {
		t.Fatalf("MigrateToV4 err = %v, want active assignments error", err)
	}
	assertBeadsColumn(t, db, "gate_state")
}

func TestMigrateToV4RollsBackWhenAMigrationStepFails(t *testing.T) {
	ctx := context.Background()
	db := openV20DB(t)
	if err := migrations.MigrateToV3(ctx, db); err != nil {
		t.Fatalf("MigrateToV3: %v", err)
	}
	insertLegacyPremortemState(ctx, t, db)
	if _, err := db.ExecContext(ctx, `
CREATE TRIGGER reject_migration_journey
BEFORE INSERT ON bead_journey
WHEN NEW.actor = 'migration'
BEGIN
  SELECT RAISE(ABORT, 'injected migration journey failure');
END`); err != nil {
		t.Fatalf("install migration failure trigger: %v", err)
	}

	err := migrations.MigrateToV4(ctx, db)
	if err == nil || !strings.Contains(err.Error(), "injected migration journey failure") {
		t.Fatalf("MigrateToV4 error = %v, want injected step failure", err)
	}
	assertBeadsColumn(t, db, "gate_state")
	assertCount(t, db, `SELECT COUNT(*) FROM beads WHERE id='pm-1' AND type='premortem' AND deleted=0`, 1)
}

func TestMigrateToV4PreservesReviewCheckpointReadyExclusion(t *testing.T) {
	ctx := context.Background()
	db := openV20DB(t)
	if err := migrations.MigrateToV3(ctx, db); err != nil {
		t.Fatalf("MigrateToV3: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO beads (id, title, status) VALUES ('review-owned', 'review owned', 'open')`); err != nil {
		t.Fatalf("insert review-owned bead: %v", err)
	}
	assignment, err := db.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES ('review-owned', 'review-worker', '/tmp/review-owned', 'requeued')`)
	if err != nil {
		t.Fatalf("insert requeued assignment: %v", err)
	}
	assignmentID, err := assignment.LastInsertId()
	if err != nil {
		t.Fatalf("requeued assignment ID: %v", err)
	}
	seedMigrationReviewCheckpoint(ctx, t, db, assignmentID, "review_running")

	if err := migrations.MigrateToV4(ctx, db); err != nil {
		t.Fatalf("MigrateToV4: %v", err)
	}
	assertMigrationReadyCount(ctx, t, db, 0)
	if _, err := db.ExecContext(ctx, `UPDATE review_checkpoints SET state='superseded' WHERE bead_id='review-owned'`); err != nil {
		t.Fatalf("supersede review checkpoint: %v", err)
	}
	assertMigrationReadyCount(ctx, t, db, 1)
}

func seedMigrationReviewCheckpoint(ctx context.Context, t *testing.T, db *sql.DB, assignmentID int64, state string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
INSERT INTO review_checkpoints (
  checkpoint_key, bead_id, origin_assignment_id, worktree, branch, target_branch,
  head_sha, target_sha, acceptance_hash, qg_script_hash, qg_mode,
  review_policy_hash, triage_revision, ready_attempt, state
) VALUES ('checkpoint-review-owned', 'review-owned', ?, '/tmp/review-owned', 'agent/review-owned',
          'main', 'head', 'target', 'acceptance', 'qg', 'full', 'policy', 'triage', 'ready', ?)`,
		assignmentID, state); err != nil {
		t.Fatalf("insert review checkpoint: %v", err)
	}
}

func assertMigrationReadyCount(ctx context.Context, t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads_ready WHERE id='review-owned'`).Scan(&got); err != nil {
		t.Fatalf("query beads_ready: %v", err)
	}
	if got != want {
		t.Fatalf("beads_ready count = %d, want %d", got, want)
	}
}

func insertLegacyPremortemState(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatalf("enable ignore_check_constraints: %v", err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `PRAGMA ignore_check_constraints=OFF`); err != nil {
			t.Fatalf("disable ignore_check_constraints: %v", err)
		}
	}()
	stmts := []string{
		`INSERT INTO beads (id, title, status, type, pipeline_stage) VALUES ('epic-1', 'epic', 'open', 'epic', 'plan')`,
		`INSERT INTO beads (id, title, status, type, parent_id, gate_state, premortem_cycle_count, pipeline_stage) VALUES ('pm-1', 'pm', 'open', 'premortem', 'epic-1', 'eligible', 2, 'premortem')`,
		`INSERT INTO bead_metadata (bead_id, key, value) VALUES ('pm-1', 'premortem_verdict', 'replan')`,
		`INSERT INTO bead_metadata (bead_id, key, value) VALUES ('pm-1', 'premortem_reason', 'legacy')`,
		`INSERT INTO bead_journey (bead_id, ts, actor, event, payload) VALUES ('pm-1', '2026-01-01T00:00:00Z', 'premortem', 'closed', '{}')`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec %s: %v", stmt, err)
		}
	}
}

func assertNoBeadsColumn(t *testing.T, db *sql.DB, col string) {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(beads)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if name == col {
			t.Fatalf("column %s still exists", col)
		}
	}
}

func assertTableSQLExcludes(t *testing.T, db *sql.DB, needle string) {
	t.Helper()
	var sqlText string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='beads'`).Scan(&sqlText); err != nil {
		t.Fatalf("read beads sql: %v", err)
	}
	if strings.Contains(sqlText, needle) {
		t.Fatalf("beads schema still contains %q: %s", needle, sqlText)
	}
}

func assertCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	if got != want {
		t.Fatalf("%s = %d, want %d", query, got, want)
	}
}

func assertNoTrigger(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name=?`, name).Scan(&count); err != nil {
		t.Fatalf("count trigger %s: %v", name, err)
	}
	if count != 0 {
		t.Fatalf("trigger %s exists", name)
	}
}
