package protocol_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestSchemaExecsCleanly(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}
}

func TestInitializeBeadSchemaCreatesCanonicalFreshSchema(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("init runtime schema: %v", err)
	}

	if err := protocol.InitializeBeadSchema(ctx, db); err != nil {
		t.Fatalf("initialize bead schema: %v", err)
	}

	for _, object := range []struct {
		kind string
		name string
	}{
		{kind: "table", name: "beads"},
		{kind: "table", name: "review_checkpoints"},
		{kind: "index", name: "idx_review_checkpoints_active_key"},
		{kind: "view", name: "review_checkpoints_blocking_assignment"},
		{kind: "view", name: "beads_ready"},
	} {
		var count int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_schema WHERE type = ? AND name = ?`, object.kind, object.name,
		).Scan(&count); err != nil {
			t.Fatalf("inspect %s %s: %v", object.kind, object.name, err)
		}
		if count != 1 {
			t.Fatalf("%s %s count = %d, want 1", object.kind, object.name, count)
		}
	}

	if _, err := db.ExecContext(ctx, `
INSERT INTO review_checkpoints (
    checkpoint_key, bead_id, origin_assignment_id, worktree, branch,
    target_branch, head_sha, target_sha, acceptance_hash, qg_script_hash,
    qg_mode, review_policy_hash, triage_revision, ready_attempt, state
) VALUES (
    'fresh-schema-checkpoint', 'oro-fresh-schema', 1, '/tmp/fresh-schema',
    'agent/oro-fresh-schema', 'main', 'head', 'target', 'acceptance',
    'script', 'full', 'policy', 'triage', 'attempt', 'review_pending'
)`); err != nil {
		t.Fatalf("insert canonical review checkpoint: %v", err)
	}
	var blocked int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM review_checkpoints_blocking_assignment WHERE bead_id = 'oro-fresh-schema'`,
	).Scan(&blocked); err != nil {
		t.Fatalf("query checkpoint admission view: %v", err)
	}
	if blocked != 1 {
		t.Fatalf("blocking checkpoint count = %d, want 1", blocked)
	}
}

func TestMigrateBeadSchemaAddsAssignmentEvidenceIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE assignments (
  id INTEGER PRIMARY KEY,
  bead_id TEXT NOT NULL,
  worker_id TEXT NOT NULL,
  worktree TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  assigned_at TEXT NOT NULL DEFAULT (datetime('now')),
  completed_at TEXT,
  attempt_count INTEGER DEFAULT 0,
  handoff_count INTEGER DEFAULT 0
)`); err != nil {
		t.Fatalf("create legacy assignments: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO assignments (id, bead_id, worker_id, worktree) VALUES (1, 'oro-legacy', 'worker-legacy', '/tmp/legacy')`); err != nil {
		t.Fatalf("seed legacy assignment: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}
	for _, column := range []string{"qg_evidence_dir", "target_sha", "target_branch"} {
		var count int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('assignments') WHERE name = ?`, column,
		).Scan(&count); err != nil {
			t.Fatalf("inspect assignments.%s: %v", column, err)
		}
		if count != 1 {
			t.Fatalf("assignments.%s count = %d, want 1", column, count)
		}
	}
	var migratedTargetBranch string
	if err := db.QueryRowContext(ctx, `SELECT target_branch FROM assignments WHERE id = 1`).Scan(&migratedTargetBranch); err != nil {
		t.Fatalf("read migrated target branch: %v", err)
	}
	if migratedTargetBranch != "" {
		t.Fatalf("migrated legacy target branch = %q, want empty for tracked-identity fallback", migratedTargetBranch)
	}
}

func TestSchemaCreatesExpectedTables(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
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
	db, err := dbutil.OpenDB(":memory:")
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
	_, err = db.Exec(`INSERT OR REPLACE INTO pane_activity VALUES ("manager", 1234567890)`)
	if err != nil {
		t.Fatalf("INSERT OR REPLACE into pane_activity: %v", err)
	}

	_, err = db.Exec(`INSERT OR REPLACE INTO pane_activity VALUES ("manager", 9999999999)`)
	if err != nil {
		t.Fatalf("second INSERT OR REPLACE (idempotent): %v", err)
	}

	var ts int64
	err = db.QueryRow(`SELECT last_seen FROM pane_activity WHERE pane='manager'`).Scan(&ts)
	if err != nil {
		t.Fatalf("query pane_activity: %v", err)
	}
	if ts != 9999999999 {
		t.Errorf("expected last_seen=9999999999, got %d", ts)
	}
}

func TestSchemaDDL_RejectionBeadIndex(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	// idx_rejection_bead must exist after applying only SchemaDDL (no migrations).
	var name string
	err = db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name='idx_rejection_bead'",
	).Scan(&name)
	if err != nil {
		t.Fatalf("idx_rejection_bead index not found in SchemaDDL: %v", err)
	}
}

func TestSchemaIsIdempotent(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
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

func TestMigrateEpicBranchAdmissionLedger(t *testing.T) {
	t.Run("fresh database enforces the branch ledger contract", func(t *testing.T) {
		db, err := dbutil.OpenDB(":memory:")
		if err != nil {
			t.Fatalf("open in-memory db: %v", err)
		}
		defer func() { _ = db.Close() }()

		ctx := context.Background()
		if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
			t.Fatalf("migrate fresh database: %v", err)
		}

		type columnInfo struct {
			name       string
			columnType string
			notNull    int
			defaultSQL sql.NullString
			primaryKey int
		}
		var columns []columnInfo
		rows, err := db.QueryContext(ctx, `PRAGMA table_info(epic_branch_admissions)`)
		if err != nil {
			t.Fatalf("inspect epic_branch_admissions columns: %v", err)
		}
		for rows.Next() {
			var column columnInfo
			var cid int
			if err := rows.Scan(&cid, &column.name, &column.columnType, &column.notNull, &column.defaultSQL, &column.primaryKey); err != nil {
				t.Fatalf("scan epic_branch_admissions column: %v", err)
			}
			columns = append(columns, column)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close epic_branch_admissions columns: %v", err)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate epic_branch_admissions columns: %v", err)
		}

		wantColumns := []struct {
			name       string
			columnType string
			notNull    int
			defaultSQL string
			primaryKey int
		}{
			{name: "branch", columnType: "TEXT", primaryKey: 1},
			{name: "epic_id", columnType: "TEXT", notNull: 1},
			{name: "target_branch", columnType: "TEXT", notNull: 1},
			{name: "state", columnType: "TEXT", notNull: 1},
			{name: "generation", columnType: "INTEGER", notNull: 1, defaultSQL: "1"},
			{name: "lease_token", columnType: "TEXT"},
			{name: "lease_owner", columnType: "TEXT"},
			{name: "lease_expires_at", columnType: "TEXT"},
			{name: "blocker_kind", columnType: "TEXT"},
			{name: "checkout_path", columnType: "TEXT"},
			{name: "branch_sha", columnType: "TEXT", notNull: 1, defaultSQL: "''"},
			{name: "target_sha", columnType: "TEXT", notNull: 1, defaultSQL: "''"},
			{name: "recovery_bead_id", columnType: "TEXT"},
			{name: "details", columnType: "TEXT", notNull: 1, defaultSQL: "''"},
			{name: "created_at", columnType: "TEXT", notNull: 1},
			{name: "updated_at", columnType: "TEXT", notNull: 1},
			{name: "resolved_at", columnType: "TEXT"},
		}
		if len(columns) != len(wantColumns) {
			t.Fatalf("epic_branch_admissions column count = %d, want %d", len(columns), len(wantColumns))
		}
		for i, want := range wantColumns {
			got := columns[i]
			if got.name != want.name || got.columnType != want.columnType || got.notNull != want.notNull || got.primaryKey != want.primaryKey || got.defaultSQL.String != want.defaultSQL {
				t.Errorf("epic_branch_admissions column %d = %+v, want %+v", i, got, want)
			}
		}

		var indexColumns string
		if err := db.QueryRowContext(ctx, `
SELECT group_concat(name, ',')
FROM pragma_index_info('idx_epic_branch_admissions_state')
`).Scan(&indexColumns); err != nil {
			t.Fatalf("inspect epic branch admission state index: %v", err)
		}
		if indexColumns != "state" {
			t.Fatalf("idx_epic_branch_admissions_state columns = %q, want state", indexColumns)
		}

		for _, state := range []string{"leased", "blocked", "resolved"} {
			if _, err := db.ExecContext(ctx, `
INSERT INTO epic_branch_admissions (branch, epic_id, target_branch, state, created_at, updated_at)
VALUES (?, ?, 'main', ?, '2026-08-03T00:00:00Z', '2026-08-03T00:00:00Z')
`, "epic/oro-"+state, "oro-"+state, state); err != nil {
				t.Fatalf("insert %q admission: %v", state, err)
			}
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO epic_branch_admissions (branch, epic_id, target_branch, state, created_at, updated_at)
VALUES ('epic/oro-leased', 'oro-duplicate', 'main', 'leased', '2026-08-03T00:00:00Z', '2026-08-03T00:00:00Z')
`); err == nil {
			t.Fatal("duplicate branch admission succeeded, want primary-key failure")
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO epic_branch_admissions (branch, epic_id, target_branch, state, created_at, updated_at)
VALUES ('epic/oro-invalid', 'oro-invalid', 'main', 'available', '2026-08-03T00:00:00Z', '2026-08-03T00:00:00Z')
`); err == nil {
			t.Fatal("invalid admission state succeeded, want CHECK failure")
		}

		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM epic_branch_admissions`).Scan(&count); err != nil {
			t.Fatalf("count branch admissions: %v", err)
		}
		if count != 3 {
			t.Fatalf("branch admission rows = %d, want exactly one for each of three branches", count)
		}
		var generation int
		var branchSHA, targetSHA, details string
		var leaseToken, leaseOwner, leaseExpiresAt, blockerKind, checkoutPath, recoveryBeadID, resolvedAt sql.NullString
		if err := db.QueryRowContext(ctx, `
SELECT generation, branch_sha, target_sha, details,
       lease_token, lease_owner, lease_expires_at, blocker_kind, checkout_path, recovery_bead_id, resolved_at
FROM epic_branch_admissions WHERE branch='epic/oro-resolved'
`).Scan(&generation, &branchSHA, &targetSHA, &details, &leaseToken, &leaseOwner, &leaseExpiresAt, &blockerKind, &checkoutPath, &recoveryBeadID, &resolvedAt); err != nil {
			t.Fatalf("read admission defaults and nullable fields: %v", err)
		}
		if generation != 1 || branchSHA != "" || targetSHA != "" || details != "" {
			t.Fatalf("admission defaults = generation %d, branch_sha %q, target_sha %q, details %q", generation, branchSHA, targetSHA, details)
		}
		if leaseToken.Valid || leaseOwner.Valid || leaseExpiresAt.Valid || blockerKind.Valid || checkoutPath.Valid || recoveryBeadID.Valid || resolvedAt.Valid {
			t.Fatalf("minimal admission invented optional state: token=%v owner=%v lease=%v blocker=%v checkout=%v recovery=%v resolved=%v", leaseToken, leaseOwner, leaseExpiresAt, blockerKind, checkoutPath, recoveryBeadID, resolvedAt)
		}
	})

	t.Run("existing database migration is idempotent and preserves data", func(t *testing.T) {
		db, err := dbutil.OpenDB(t.TempDir() + "/state.db")
		if err != nil {
			t.Fatalf("open legacy state db: %v", err)
		}
		defer func() { _ = db.Close() }()

		ctx := context.Background()
		if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("seed runtime schema: %v", err)
		}
		if _, err := db.ExecContext(ctx, `
CREATE TABLE runtime_leases (id TEXT PRIMARY KEY, marker TEXT NOT NULL);
CREATE TABLE leases (id TEXT PRIMARY KEY, marker TEXT NOT NULL);
INSERT INTO recovery_quarantines (bead_id, reason, details, status)
VALUES ('oro-quarantine', 'legacy', 'preserve me', 'open');
INSERT INTO runtime_leases VALUES ('runtime-lease', 'preserve me');
INSERT INTO leases VALUES ('storage-lease', 'preserve me');
`); err != nil {
			t.Fatalf("seed legacy state: %v", err)
		}
		unrelatedTableSQL := make(map[string]string)
		for _, table := range []string{"recovery_quarantines", "runtime_leases", "leases"} {
			var tableSQL string
			if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&tableSQL); err != nil {
				t.Fatalf("read pre-migration %s schema: %v", table, err)
			}
			unrelatedTableSQL[table] = tableSQL
		}
		if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
			t.Fatalf("first legacy migration: %v", err)
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO epic_branch_admissions
    (branch, epic_id, target_branch, state, blocker_kind, checkout_path, branch_sha, target_sha,
     recovery_bead_id, details, created_at, updated_at)
VALUES
    ('epic/oro-existing', 'oro-existing', 'main', 'blocked', 'diverged', '/tmp/epic', 'abc', 'def',
     'oro-recovery', 'preserve me',
     '2026-08-03T00:00:00Z', '2026-08-03T00:00:00Z')
`); err != nil {
			t.Fatalf("seed existing admission: %v", err)
		}
		if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
			t.Fatalf("second legacy migration: %v", err)
		}
		if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
			t.Fatalf("third legacy migration: %v", err)
		}
		for table, wantSQL := range unrelatedTableSQL {
			var gotSQL string
			if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&gotSQL); err != nil {
				t.Fatalf("read post-migration %s schema: %v", table, err)
			}
			if gotSQL != wantSQL {
				t.Errorf("%s schema changed during admission migration\ngot:  %s\nwant: %s", table, gotSQL, wantSQL)
			}
		}

		for name, query := range map[string]string{
			"admission":     `SELECT COUNT(*) FROM epic_branch_admissions WHERE branch='epic/oro-existing' AND epic_id='oro-existing' AND target_branch='main' AND state='blocked' AND blocker_kind='diverged' AND checkout_path='/tmp/epic' AND branch_sha='abc' AND target_sha='def' AND recovery_bead_id='oro-recovery' AND details='preserve me'`,
			"quarantine":    `SELECT COUNT(*) FROM recovery_quarantines WHERE bead_id='oro-quarantine' AND reason='legacy' AND details='preserve me' AND status='open'`,
			"runtime lease": `SELECT COUNT(*) FROM runtime_leases WHERE id='runtime-lease' AND marker='preserve me'`,
			"storage lease": `SELECT COUNT(*) FROM leases WHERE id='storage-lease' AND marker='preserve me'`,
		} {
			var count int
			if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
				t.Fatalf("count preserved %s: %v", name, err)
			}
			if count != 1 {
				t.Errorf("preserved %s rows = %d, want 1", name, count)
			}
		}
	})
}

func TestSchemaCreatesOpsRunsTable(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	expectedColumns := map[string]string{
		"id":             "INTEGER",
		"escalation_id":  "INTEGER",
		"type":           "TEXT",
		"bead_id":        "TEXT",
		"worker_id":      "TEXT",
		"dispatcher_pid": "INTEGER",
		"process_pid":    "INTEGER",
		"runtime":        "TEXT",
		"model":          "TEXT",
		"status":         "TEXT",
		"verdict":        "TEXT",
		"feedback":       "TEXT",
		"error":          "TEXT",
		"started_at":     "DATETIME",
		"completed_at":   "DATETIME",
	}
	rows, err := db.Query(`PRAGMA table_info(ops_runs)`)
	if err != nil {
		t.Fatalf("pragma table_info(ops_runs): %v", err)
	}
	defer func() { _ = rows.Close() }()

	columns := make(map[string]string)
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan column info: %v", err)
		}
		columns[name] = columnType
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate column info: %v", err)
	}

	for name, expectedType := range expectedColumns {
		if columns[name] != expectedType {
			t.Errorf("ops_runs column %q type = %q, want %q", name, columns[name], expectedType)
		}
	}

	assertSQLiteObjectExists(t, db, "index", "idx_ops_runs_open")
	assertSQLiteObjectExists(t, db, "index", "idx_ops_runs_blocking_key")
}

func TestOpsRunUniqueBlockingIndex(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	for _, status := range []string{"running", "failed", "stale"} {
		if _, err := db.Exec(
			`INSERT INTO ops_runs (type, bead_id, status) VALUES ('decompose', 'oro-blocked', ?)`,
			status,
		); err != nil {
			t.Fatalf("insert first blocking status %q: %v", status, err)
		}
		if _, err := db.Exec(
			`INSERT INTO ops_runs (type, bead_id, status) VALUES ('decompose', 'oro-blocked', ?)`,
			status,
		); err == nil {
			t.Fatalf("duplicate blocking status %q succeeded, want unique constraint failure", status)
		}
		if _, err := db.Exec(`DELETE FROM ops_runs`); err != nil {
			t.Fatalf("clear ops_runs after status %q: %v", status, err)
		}
	}

	for _, status := range []string{"resolved", "superseded"} {
		if _, err := db.Exec(
			`INSERT INTO ops_runs (type, bead_id, status) VALUES ('decompose', 'oro-finished', ?)`,
			status,
		); err != nil {
			t.Fatalf("insert first non-blocking status %q: %v", status, err)
		}
		if _, err := db.Exec(
			`INSERT INTO ops_runs (type, bead_id, status) VALUES ('decompose', 'oro-finished', ?)`,
			status,
		); err != nil {
			t.Fatalf("duplicate non-blocking status %q failed: %v", status, err)
		}
		if _, err := db.Exec(`DELETE FROM ops_runs`); err != nil {
			t.Fatalf("clear ops_runs after status %q: %v", status, err)
		}
	}
}

func TestMigrateBeadSchemaDropsLegacyOpsRunsEscalationUnique(t *testing.T) {
	db, err := dbutil.OpenDB(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("exec runtime schema: %v", err)
	}
	_, err = db.ExecContext(ctx, `
DROP INDEX IF EXISTS idx_ops_runs_blocking_key;
DROP INDEX IF EXISTS idx_ops_runs_open;
DROP TABLE ops_runs;
CREATE TABLE ops_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    escalation_id INTEGER,
    type TEXT NOT NULL,
    bead_id TEXT,
    worker_id TEXT,
    dispatcher_pid INTEGER,
    process_pid INTEGER,
    runtime TEXT,
    model TEXT,
    status TEXT NOT NULL DEFAULT 'running',
    verdict TEXT,
    feedback TEXT,
    error TEXT,
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    UNIQUE(escalation_id, type, bead_id)
);
CREATE INDEX idx_ops_runs_open
ON ops_runs(status, type, bead_id);
CREATE UNIQUE INDEX idx_ops_runs_blocking_key
ON ops_runs(type, bead_id)
WHERE status IN ('running', 'failed', 'stale');
INSERT INTO ops_runs (escalation_id, type, bead_id, status, error)
VALUES (2675, 'decompose', 'oro-nkse', 'superseded', 'orphaned dead process superseded on dispatcher startup');
`)
	if err != nil {
		t.Fatalf("seed legacy ops_runs schema: %v", err)
	}

	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}

	var tableSQL string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name='ops_runs'`).Scan(&tableSQL); err != nil {
		t.Fatalf("query ops_runs sql: %v", err)
	}
	normalized := strings.NewReplacer(" ", "", "\n", "", "\t", "").Replace(strings.ToLower(tableSQL))
	if strings.Contains(normalized, "unique(escalation_id,type,bead_id)") {
		t.Fatalf("legacy ops_runs escalation unique constraint still present: %s", tableSQL)
	}

	if _, err := db.ExecContext(ctx, `
INSERT INTO ops_runs (escalation_id, type, bead_id, status, error)
VALUES (2675, 'decompose', 'oro-nkse', 'running', 'replacement ops run');
`); err != nil {
		t.Fatalf("insert replacement ops_run with same escalation key: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO ops_runs (escalation_id, type, bead_id, status)
VALUES (9999, 'decompose', 'oro-nkse', 'failed');
`); err == nil {
		t.Fatal("duplicate blocking ops_run succeeded, want partial unique index failure")
	}
	var supersededCount int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM ops_runs
WHERE escalation_id=2675 AND type='decompose' AND bead_id='oro-nkse' AND status='superseded';
`).Scan(&supersededCount); err != nil {
		t.Fatalf("count preserved superseded ops_run: %v", err)
	}
	if supersededCount != 1 {
		t.Fatalf("preserved superseded ops_runs = %d, want 1", supersededCount)
	}
	assertSQLiteObjectExists(t, db, "index", "idx_ops_runs_open")
	assertSQLiteObjectExists(t, db, "index", "idx_ops_runs_blocking_key")
}

func TestMigration11(t *testing.T) {
	testBeadSchemaMigration(t)
}

func TestSchemaMigration11(t *testing.T) {
	testBeadSchemaMigration(t)
}

func TestMigrateBeadSchemaRebuildsOldStatusConstraint(t *testing.T) {
	db, err := dbutil.OpenDB(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("exec runtime schema: %v", err)
	}
	_, err = db.ExecContext(ctx, `
CREATE TABLE beads (
    id                    TEXT PRIMARY KEY,
    title                 TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    acceptance_criteria   TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL CHECK (status IN ('open','in_progress','closed')),
    priority              INTEGER NOT NULL DEFAULT 2,
    type                  TEXT NOT NULL DEFAULT 'task',
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
    deleted               INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE bead_deps (
    bead_id       TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    depends_on_id TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    type          TEXT NOT NULL DEFAULT 'blocks',
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    created_by    TEXT,
    PRIMARY KEY (bead_id, depends_on_id, type)
);
INSERT INTO beads (id, title, status) VALUES
    ('oro-parent', 'parent', 'open'),
    ('oro-child', 'child', 'open');
INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('oro-child', 'oro-parent', 'parent-child');
INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-parent', 'worker-1', '/tmp/parent', 'active');
`)
	if err != nil {
		t.Fatalf("seed old bead schema: %v", err)
	}

	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("migrate old bead schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE beads SET status='blocked' WHERE id='oro-child'`); err != nil {
		t.Fatalf("blocked status rejected after migration: %v", err)
	}
	var readyCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads_ready`).Scan(&readyCount); err != nil {
		t.Fatalf("query beads_ready: %v", err)
	}
	if readyCount != 0 {
		t.Fatalf("beads_ready count = %d, want 0 with active assignment and blocked child", readyCount)
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatalf("foreign_key_check returned a violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("foreign_key_check rows: %v", err)
	}
}

func TestMigrateBeadSchemaToleratesPreexistingForeignKeyViolations(t *testing.T) {
	db, err := dbutil.OpenDB(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("exec runtime schema: %v", err)
	}
	_, err = db.ExecContext(ctx, `
CREATE TABLE beads (
    id                    TEXT PRIMARY KEY,
    title                 TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    acceptance_criteria   TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL CHECK (status IN ('open','in_progress','closed')),
    priority              INTEGER NOT NULL DEFAULT 2,
    type                  TEXT NOT NULL DEFAULT 'task',
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
    deleted               INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE bead_deps (
    bead_id       TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    depends_on_id TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    type          TEXT NOT NULL DEFAULT 'blocks',
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    created_by    TEXT,
    PRIMARY KEY (bead_id, depends_on_id, type)
);
INSERT INTO beads (id, title, status) VALUES ('oro-child', 'child', 'open');
INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('oro-child', 'oro-missing-parent', 'parent-child');
`)
	if err != nil {
		t.Fatalf("seed old bead schema with dangling dependency: %v", err)
	}

	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("migrate old bead schema with preexisting FK violation: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE beads SET status='blocked' WHERE id='oro-child'`); err != nil {
		t.Fatalf("blocked status rejected after migration: %v", err)
	}
	var foreignKeys int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("foreign_keys pragma: %v", err)
	}
	if foreignKeys != 0 {
		t.Fatalf("foreign_keys pragma = %d, want original default OFF after schema rebuild", foreignKeys)
	}
	var violationCount int
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	for rows.Next() {
		violationCount++
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close foreign_key_check rows: %v", err)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("foreign_key_check rows: %v", err)
	}
	if violationCount != 1 {
		t.Fatalf("foreign_key_check violations = %d, want the one preexisting dangling dependency", violationCount)
	}
}

func testBeadSchemaMigration(t *testing.T) {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("second migration: %v", err)
	}

	for _, table := range []string{
		"beads",
		"bead_deps",
		"bead_tags",
		"bead_labels",
		"bead_metadata",
		"bead_notes",
		"beads_fts",
	} {
		assertSQLiteObjectExists(t, db, "table", table)
	}
	for _, index := range []string{
		"idx_beads_status",
		"idx_beads_parent",
		"idx_beads_type",
		"idx_beads_priority",
		"idx_beads_deferred",
		"idx_bead_deps_depends_on",
		"idx_bead_tags_tag",
		"idx_bead_labels_label",
		"idx_bead_notes_bead",
	} {
		assertSQLiteObjectExists(t, db, "index", index)
	}
	for _, view := range []string{"beads_ready", "beads_blocked"} {
		assertSQLiteObjectExists(t, db, "view", view)
	}
	for _, trigger := range []string{
		"beads_fts_ai",
		"beads_fts_ad",
		"beads_fts_au",
		"bead_deps_touch_parent_ai",
		"bead_deps_touch_parent_au",
		"bead_deps_touch_parent_ad",
		"bead_tags_touch_parent_ai",
		"bead_tags_touch_parent_au",
		"bead_tags_touch_parent_ad",
		"bead_labels_touch_parent_ai",
		"bead_labels_touch_parent_au",
		"bead_labels_touch_parent_ad",
		"bead_metadata_touch_parent_ai",
		"bead_metadata_touch_parent_au",
		"bead_metadata_touch_parent_ad",
		"bead_notes_touch_parent_ai",
		"bead_notes_touch_parent_au",
		"bead_notes_touch_parent_ad",
	} {
		assertSQLiteObjectExists(t, db, "trigger", trigger)
	}
}

func assertSQLiteObjectExists(t *testing.T, db *sql.DB, objectType, name string) {
	t.Helper()
	var got string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type = ? AND name = ?",
		objectType,
		name,
	).Scan(&got)
	if err != nil {
		t.Fatalf("%s %q not found: %v", objectType, name, err)
	}
}

func TestMigrateSemanticMemoryDense(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	// First migration: add embedding_dense and content_tokens columns
	_, err = db.Exec(protocol.MigrateSemanticMemoryDense)
	if err != nil {
		t.Fatalf("first migration exec: %v", err)
	}

	// Verify embedding_dense column exists
	var colName string
	err = db.QueryRow(
		"SELECT name FROM pragma_table_info('memories') WHERE name='embedding_dense'",
	).Scan(&colName)
	if err != nil {
		t.Fatalf("embedding_dense column not found: %v", err)
	}

	// Verify content_tokens column exists
	err = db.QueryRow(
		"SELECT name FROM pragma_table_info('memories') WHERE name='content_tokens'",
	).Scan(&colName)
	if err != nil {
		t.Fatalf("content_tokens column not found: %v", err)
	}

	// Re-running the migration should not break the database (error is intentionally ignored)
	// This simulates the error-ignoring pattern used in migrateStateDB
	_, _ = db.Exec(protocol.MigrateSemanticMemoryDense)

	// Verify database is still functional and columns exist
	var colName2 string
	err = db.QueryRow(
		"SELECT name FROM pragma_table_info('memories') WHERE name='embedding_dense'",
	).Scan(&colName2)
	if err != nil {
		t.Fatalf("embedding_dense column missing after re-run: %v", err)
	}

	// Insert a row to verify columns are properly populated with defaults
	_, err = db.Exec(
		`INSERT INTO memories (content, type, source) VALUES ('test content', 'test', 'test_source')`,
	)
	if err != nil {
		t.Fatalf("insert into memories: %v", err)
	}

	var contentTokens int
	err = db.QueryRow(
		`SELECT content_tokens FROM memories WHERE content='test content'`,
	).Scan(&contentTokens)
	if err != nil {
		t.Fatalf("query content_tokens: %v", err)
	}
	if contentTokens != 0 {
		t.Errorf("expected content_tokens default=0, got %d", contentTokens)
	}
}

func TestMigrateSemanticMemoryBackfillState(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	// Apply the backfill state migration
	_, err = db.Exec(protocol.MigrateSemanticMemoryBackfillState)
	if err != nil {
		t.Fatalf("exec migration: %v", err)
	}

	// Verify backfill_semantic_memory_state key exists and is set to 'pending'
	var state string
	err = db.QueryRow(
		`SELECT value FROM kv_store WHERE key='backfill_semantic_memory_state'`,
	).Scan(&state)
	if err != nil {
		t.Fatalf("backfill_semantic_memory_state key not found: %v", err)
	}
	if state != "pending" {
		t.Errorf("expected backfill_semantic_memory_state='pending', got %q", state)
	}

	// Verify embedding_dense_model sentinel key exists
	var model string
	err = db.QueryRow(
		`SELECT value FROM kv_store WHERE key='embedding_dense_model'`,
	).Scan(&model)
	if err != nil {
		t.Fatalf("embedding_dense_model key not found: %v", err)
	}
	if model != "bge-small-en-v1.5" {
		t.Errorf("expected embedding_dense_model='bge-small-en-v1.5', got %q", model)
	}

	// Re-running should be idempotent (INSERT OR IGNORE should prevent duplicates)
	_, err = db.Exec(protocol.MigrateSemanticMemoryBackfillState)
	if err != nil {
		t.Fatalf("second migration exec (should be idempotent): %v", err)
	}

	// Verify values unchanged after re-running
	var stateAfter string
	err = db.QueryRow(
		`SELECT value FROM kv_store WHERE key='backfill_semantic_memory_state'`,
	).Scan(&stateAfter)
	if err != nil {
		t.Fatalf("backfill_semantic_memory_state key after re-run: %v", err)
	}
	if stateAfter != "pending" {
		t.Errorf("expected backfill_semantic_memory_state='pending' after re-run, got %q", stateAfter)
	}
}

func TestEmbeddingDenseModelSentinel(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	// Apply the backfill state migration which includes the embedding_dense_model sentinel
	_, err = db.Exec(protocol.MigrateSemanticMemoryBackfillState)
	if err != nil {
		t.Fatalf("exec migration: %v", err)
	}

	// Verify embedding_dense_model sentinel is set to correct value
	var model string
	err = db.QueryRow(
		`SELECT value FROM kv_store WHERE key='embedding_dense_model'`,
	).Scan(&model)
	if err != nil {
		t.Fatalf("embedding_dense_model not found: %v", err)
	}
	if model != "bge-small-en-v1.5" {
		t.Errorf("expected model='bge-small-en-v1.5', got %q", model)
	}

	// Re-running should be idempotent
	_, err = db.Exec(protocol.MigrateSemanticMemoryBackfillState)
	if err != nil {
		t.Fatalf("second migration exec (should be idempotent): %v", err)
	}

	// Verify value unchanged after re-running
	var modelAfter string
	err = db.QueryRow(
		`SELECT value FROM kv_store WHERE key='embedding_dense_model'`,
	).Scan(&modelAfter)
	if err != nil {
		t.Fatalf("query after re-run: %v", err)
	}
	if modelAfter != "bge-small-en-v1.5" {
		t.Errorf("expected model='bge-small-en-v1.5' after re-run, got %q", modelAfter)
	}
}

func TestMigrateSemanticMemorySearchEvents(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	_, err = db.Exec(protocol.MigrateSemanticMemorySearchEvents)
	if err != nil {
		t.Fatalf("exec MigrateSemanticMemorySearchEvents: %v", err)
	}

	// Verify table exists.
	var tableName string
	err = db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='memory_search_events'",
	).Scan(&tableName)
	if err != nil {
		t.Fatalf("memory_search_events table not found: %v", err)
	}

	// Verify exact column set via PRAGMA table_info.
	type colInfo struct {
		name    string
		typ     string
		notNull bool
		dflt    *string
		pk      bool
	}
	wantCols := []colInfo{
		{name: "id", typ: "INTEGER", notNull: false, pk: true},
		{name: "ts", typ: "DATETIME", notNull: true, dflt: ptr("datetime('now')")},
		{name: "project", typ: "TEXT", notNull: false},
		{name: "query_hash", typ: "TEXT", notNull: false},
		{name: "top_k_ids", typ: "TEXT", notNull: false},
		{name: "top_k_scores", typ: "TEXT", notNull: false},
		{name: "latency_ms", typ: "INTEGER", notNull: false},
		{name: "used_rerank", typ: "INTEGER", notNull: false, dflt: ptr("0")},
		{name: "used_bge", typ: "INTEGER", notNull: false, dflt: ptr("0")},
		{name: "ann_candidates", typ: "INTEGER", notNull: false},
	}

	rows, err := db.Query("PRAGMA table_info(memory_search_events)")
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()

	var gotCols []colInfo
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dfltVal *string
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltVal, &pk); err != nil {
			t.Fatalf("scan column info: %v", err)
		}
		gotCols = append(gotCols, colInfo{
			name:    name,
			typ:     typ,
			notNull: notNull != 0,
			dflt:    dfltVal,
			pk:      pk != 0,
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}

	if len(gotCols) != len(wantCols) {
		t.Fatalf("expected %d columns, got %d: %v", len(wantCols), len(gotCols), gotCols)
	}
	for i, want := range wantCols {
		got := gotCols[i]
		if got.name != want.name {
			t.Errorf("col[%d] name: want %q, got %q", i, want.name, got.name)
		}
		if got.typ != want.typ {
			t.Errorf("col[%d] %q type: want %q, got %q", i, want.name, want.typ, got.typ)
		}
		if got.notNull != want.notNull {
			t.Errorf("col[%d] %q notNull: want %v, got %v", i, want.name, want.notNull, got.notNull)
		}
		if got.pk != want.pk {
			t.Errorf("col[%d] %q pk: want %v, got %v", i, want.name, want.pk, got.pk)
		}
		wantDflt := want.dflt
		gotDflt := got.dflt
		switch {
		case wantDflt == nil && gotDflt == nil:
			// both nil — ok
		case wantDflt == nil && gotDflt != nil:
			t.Errorf("col[%d] %q default: want nil, got %q", i, want.name, *gotDflt)
		case wantDflt != nil && gotDflt == nil:
			t.Errorf("col[%d] %q default: want %q, got nil", i, want.name, *wantDflt)
		case *wantDflt != *gotDflt:
			t.Errorf("col[%d] %q default: want %q, got %q", i, want.name, *wantDflt, *gotDflt)
		}
	}

	// Verify idx_mse_ts index exists.
	var indexName string
	err = db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name='idx_mse_ts'",
	).Scan(&indexName)
	if err != nil {
		t.Fatalf("idx_mse_ts index not found: %v", err)
	}

	// Idempotency: running migration a second time must not error.
	_, err = db.Exec(protocol.MigrateSemanticMemorySearchEvents)
	if err != nil {
		t.Fatalf("second exec (idempotency): %v", err)
	}
}

func TestMigrateSemanticMemoryReadEventsCreatesEmptyTable(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	if _, err := db.Exec(protocol.MigrateSemanticMemoryReadEvents); err != nil {
		t.Fatalf("exec MigrateSemanticMemoryReadEvents: %v", err)
	}
	if _, err := db.Exec(protocol.MigrateSemanticMemoryReadEvents); err != nil {
		t.Fatalf("second exec MigrateSemanticMemoryReadEvents: %v", err)
	}

	assertSQLiteObjectExists(t, db, "table", "memory_read_events")
	assertSQLiteObjectExists(t, db, "index", "idx_mre_ts")

	var count int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_read_events`).Scan(&count); err != nil {
		t.Fatalf("count memory_read_events: %v", err)
	}
	if count != 0 {
		t.Fatalf("memory_read_events count = %d, want 0", count)
	}
}

func ptr(s string) *string { return &s }

func TestMigrateSemanticMemoryChunksConstant(t *testing.T) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Apply base schema first (creates memories table)
	_, err = db.Exec(protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("exec schema DDL: %v", err)
	}

	// Apply the migration
	_, err = db.Exec(protocol.MigrateSemanticMemoryChunks)
	if err != nil {
		t.Fatalf("exec migration: %v", err)
	}

	// Verify memory_chunks table exists
	var tableName string
	err = db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='memory_chunks'",
	).Scan(&tableName)
	if err != nil {
		t.Fatalf("memory_chunks table not found: %v", err)
	}

	// Verify required columns exist
	requiredCols := []string{"id", "memory_id", "chunk_idx", "text", "embedding"}
	for _, col := range requiredCols {
		var colName string
		err := db.QueryRow(
			"SELECT name FROM pragma_table_info('memory_chunks') WHERE name=?",
			col,
		).Scan(&colName)
		if err != nil {
			t.Errorf("required column %q not found: %v", col, err)
		}
	}

	// Verify idx_memory_chunks_memory_id index exists
	var indexName string
	err = db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name='idx_memory_chunks_memory_id'",
	).Scan(&indexName)
	if err != nil {
		t.Fatalf("idx_memory_chunks_memory_id index not found: %v", err)
	}

	// Test idempotency: apply migration again (should not error due to IF NOT EXISTS)
	_, err = db.Exec(protocol.MigrateSemanticMemoryChunks)
	if err != nil {
		t.Fatalf("second migration exec (idempotency): %v", err)
	}

	// Insert a memory row to test FK constraint
	_, err = db.Exec(
		`INSERT INTO memories (content, type, source) VALUES ('test memory', 'test', 'test_source')`,
	)
	if err != nil {
		t.Fatalf("insert test memory: %v", err)
	}

	var memoryID int64
	err = db.QueryRow(`SELECT id FROM memories WHERE content='test memory'`).Scan(&memoryID)
	if err != nil {
		t.Fatalf("query memory ID: %v", err)
	}

	// Insert a chunk row to verify the table works
	_, err = db.Exec(
		`INSERT INTO memory_chunks (memory_id, chunk_idx, text, embedding) VALUES (?, ?, ?, ?)`,
		memoryID, 0, "chunk text", []byte{},
	)
	if err != nil {
		t.Fatalf("insert memory chunk: %v", err)
	}

	// Verify the chunk was inserted
	var chunkText string
	err = db.QueryRow(
		`SELECT text FROM memory_chunks WHERE memory_id=? AND chunk_idx=?`,
		memoryID, 0,
	).Scan(&chunkText)
	if err != nil {
		t.Fatalf("query memory chunk: %v", err)
	}
	if chunkText != "chunk text" {
		t.Errorf("expected chunk_text='chunk text', got %q", chunkText)
	}
}
