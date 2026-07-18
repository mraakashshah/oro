package protocol_test

import (
	"context"
	"database/sql"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestReviewCheckpointCanonicalKeyMigration(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.ExecContext(ctx, `CREATE TABLE review_checkpoints (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		bead_id TEXT NOT NULL,
		origin_assignment_id INTEGER NOT NULL,
		worktree TEXT NOT NULL,
		branch TEXT NOT NULL,
		state TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create legacy review checkpoints: %v", err)
	}
	result, err := db.ExecContext(ctx, `INSERT INTO review_checkpoints
		(bead_id, origin_assignment_id, worktree, branch, state)
		VALUES ('oro-legacy', 7, '/tmp/legacy', 'agent/legacy', 'review_running')`)
	if err != nil {
		t.Fatalf("insert legacy review checkpoint: %v", err)
	}
	legacyID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("legacy checkpoint id: %v", err)
	}
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("schema DDL must tolerate legacy review checkpoints: %v", err)
	}

	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("second migration: %v", err)
	}

	for table, columns := range map[string][]string{
		"review_checkpoints": {
			"checkpoint_key", "qg_script_hash", "qg_mode", "review_policy_hash", "triage_revision",
			"artifact_path", "artifact_sha256", "artifact_bytes",
			"recovery_artifact_path", "recovery_artifact_sha256", "recovery_artifact_bytes",
			"override_kind", "override_source", "overridden_at",
		},
		"review_checkpoint_findings":   {"checkpoint_id", "finding_id", "severity", "file", "line"},
		"review_recovery_attempts":     {"checkpoint_id", "idempotency_key", "strategy", "proof_json"},
		"review_quarantine_deliveries": {"checkpoint_id", "scheduled_at", "delivered_at", "sink"},
	} {
		assertSQLiteObjectExists(t, db, "table", table)
		assertSQLiteTableColumns(ctx, t, db, table, columns)
	}
	assertSQLiteObjectExists(t, db, "index", "idx_review_checkpoints_active_key")

	var checkpointKey string
	if err := db.QueryRowContext(ctx, `SELECT checkpoint_key FROM review_checkpoints WHERE id = ?`, legacyID).Scan(&checkpointKey); err != nil {
		t.Fatalf("read migrated checkpoint key: %v", err)
	}
	if checkpointKey != "legacy-unverified:1" {
		t.Fatalf("migrated checkpoint key = %q, want deterministic legacy sentinel", checkpointKey)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO review_checkpoints (
		checkpoint_key, bead_id, origin_assignment_id, worktree, branch, target_branch,
		head_sha, target_sha, acceptance_hash, qg_script_hash, qg_mode,
		review_policy_hash, triage_revision, ready_attempt, state
	) VALUES (?, 'oro-duplicate', 8, '/tmp/duplicate', 'agent/duplicate', 'main',
		'head', 'target', 'acceptance', 'qg', 'default', 'policy', 'triage', 'ready', 'review_running')`, checkpointKey); err == nil {
		t.Fatal("duplicate active canonical key succeeded, want unique index failure")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO review_checkpoints (
		checkpoint_key, bead_id, origin_assignment_id, worktree, branch, target_branch,
		head_sha, target_sha, acceptance_hash, qg_script_hash, qg_mode,
		review_policy_hash, triage_revision, ready_attempt, state
	) VALUES (NULL, 'oro-null', 9, '/tmp/null', 'agent/null', 'main',
		'head', 'target', 'acceptance', 'qg', 'default', 'policy', 'triage', 'ready', 'review_running')`); err == nil {
		t.Fatal("nullable canonical key succeeded, want NOT NULL failure")
	}

	partialDB, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open partial-schema db: %v", err)
	}
	defer func() { _ = partialDB.Close() }()
	if _, err := partialDB.ExecContext(ctx, `CREATE TABLE review_checkpoints (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		checkpoint_key TEXT NOT NULL,
		bead_id TEXT NOT NULL,
		origin_assignment_id INTEGER NOT NULL,
		worktree TEXT NOT NULL,
		branch TEXT NOT NULL,
		target_branch TEXT NOT NULL,
		head_sha TEXT NOT NULL,
		target_sha TEXT NOT NULL,
		acceptance_hash TEXT NOT NULL,
		qg_script_hash TEXT NOT NULL,
		qg_mode TEXT NOT NULL,
		review_policy_hash TEXT NOT NULL,
		triage_revision TEXT NOT NULL,
		ready_attempt TEXT NOT NULL,
		state TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create partial review checkpoints: %v", err)
	}
	if _, err := partialDB.ExecContext(ctx, `CREATE TABLE review_checkpoint_findings (
		checkpoint_id INTEGER NOT NULL,
		finding_id TEXT NOT NULL,
		PRIMARY KEY(checkpoint_id, finding_id)
	)`); err != nil {
		t.Fatalf("create partial review findings: %v", err)
	}
	if _, err := partialDB.ExecContext(ctx, `INSERT INTO review_checkpoint_findings
		(checkpoint_id, finding_id) VALUES (1, 'finding-1')`); err != nil {
		t.Fatalf("insert partial review finding: %v", err)
	}
	if _, err := partialDB.ExecContext(ctx, `CREATE TABLE review_recovery_attempts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		checkpoint_id INTEGER NOT NULL,
		idempotency_key TEXT NOT NULL UNIQUE
	)`); err != nil {
		t.Fatalf("create partial review recovery attempts: %v", err)
	}
	if _, err := partialDB.ExecContext(ctx, `INSERT INTO review_recovery_attempts
		(checkpoint_id, idempotency_key) VALUES (1, 'attempt-1')`); err != nil {
		t.Fatalf("insert partial review recovery attempt: %v", err)
	}
	if _, err := partialDB.ExecContext(ctx, `CREATE TABLE review_quarantine_deliveries (
		checkpoint_id INTEGER NOT NULL,
		scheduled_at TEXT NOT NULL,
		PRIMARY KEY(checkpoint_id, scheduled_at)
	)`); err != nil {
		t.Fatalf("create partial quarantine deliveries: %v", err)
	}
	if _, err := partialDB.ExecContext(ctx, `INSERT INTO review_quarantine_deliveries
		(checkpoint_id, scheduled_at) VALUES (1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert partial quarantine delivery: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, partialDB); err != nil {
		t.Fatalf("migrate partial review checkpoints: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, partialDB); err != nil {
		t.Fatalf("second partial migration: %v", err)
	}
	assertSQLiteTableColumns(ctx, t, partialDB, "review_checkpoints", []string{"recovery_artifact_path"})
	for table, columns := range map[string][]string{
		"review_checkpoint_findings":   {"checkpoint_id", "finding_id", "severity", "file", "line", "contract_impact", "required_action", "compact_json"},
		"review_recovery_attempts":     {"id", "checkpoint_id", "failure_fingerprint", "idempotency_key", "strategy", "action_json", "status", "proof_json", "started_at", "completed_at"},
		"review_quarantine_deliveries": {"checkpoint_id", "scheduled_at", "delivered_at", "sink"},
	} {
		assertSQLiteTableColumns(ctx, t, partialDB, table, columns)
	}
	for table := range map[string]struct{}{
		"review_checkpoint_findings":   {},
		"review_recovery_attempts":     {},
		"review_quarantine_deliveries": {},
	} {
		var count int
		if err := partialDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			t.Fatalf("count migrated %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("migrated %s rows = %d, want 1", table, count)
		}
	}
	if _, err := partialDB.ExecContext(ctx, `INSERT INTO review_checkpoint_findings
		(checkpoint_id, finding_id, severity, file, contract_impact, required_action, compact_json)
		VALUES (2, 'invalid-finding', NULL, 'file.go', '', '', '{}')`); err == nil {
		t.Fatal("nullable finding severity succeeded, want NOT NULL failure")
	}
	if _, err := partialDB.ExecContext(ctx, `INSERT INTO review_recovery_attempts
		(checkpoint_id, failure_fingerprint, idempotency_key, strategy, action_json, status, proof_json, started_at)
		VALUES (2, '', 'attempt-1', 'legacy', '{}', 'failed', '{}', '2026-01-01T00:00:00Z')`); err == nil {
		t.Fatal("duplicate recovery idempotency key succeeded, want UNIQUE failure")
	}
	if _, err := partialDB.ExecContext(ctx, `INSERT INTO review_quarantine_deliveries
		(checkpoint_id, scheduled_at, sink) VALUES (1, '2026-01-01T00:00:00Z', 'legacy')`); err == nil {
		t.Fatal("duplicate quarantine delivery succeeded, want primary key failure")
	}
	if _, err := partialDB.ExecContext(ctx, `INSERT INTO review_checkpoints (
		checkpoint_key, bead_id, origin_assignment_id, worktree, branch, target_branch,
		head_sha, target_sha, acceptance_hash, qg_script_hash, qg_mode,
		review_policy_hash, triage_revision, ready_attempt, state
	) VALUES (?, 'oro-original', 9, '/tmp/original', 'agent/original', 'main',
		'head', 'target', 'acceptance', 'qg', 'default', 'policy', 'triage', 'ready', 'review_running')`, checkpointKey); err != nil {
		t.Fatalf("seed canonical active checkpoint: %v", err)
	}

	if _, err := partialDB.ExecContext(ctx, `DROP INDEX idx_review_checkpoints_active_key;
		CREATE INDEX idx_review_checkpoints_active_key ON review_checkpoints(checkpoint_key);
		ALTER TABLE review_recovery_attempts RENAME TO review_recovery_attempts_legacy;
		CREATE TABLE review_recovery_attempts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			checkpoint_id INTEGER NOT NULL,
			failure_fingerprint TEXT NOT NULL,
			idempotency_key TEXT NOT NULL UNIQUE,
			strategy TEXT NOT NULL,
			action_json TEXT NOT NULL,
			status TEXT NOT NULL,
			proof_json TEXT NOT NULL,
			started_at TEXT NOT NULL,
			completed_at TEXT
		);
		INSERT INTO review_recovery_attempts
			SELECT * FROM review_recovery_attempts_legacy;
		DROP TABLE review_recovery_attempts_legacy`); err != nil {
		t.Fatalf("create mismatched review checkpoint schema: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, partialDB); err != nil {
		t.Fatalf("repair mismatched review checkpoint schema: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, partialDB); err != nil {
		t.Fatalf("repeat repaired review checkpoint migration: %v", err)
	}
	if _, err := partialDB.ExecContext(ctx, `INSERT INTO review_checkpoints (
		checkpoint_key, bead_id, origin_assignment_id, worktree, branch, target_branch,
		head_sha, target_sha, acceptance_hash, qg_script_hash, qg_mode,
		review_policy_hash, triage_revision, ready_attempt, state
	) VALUES (?, 'oro-duplicate-after-repair', 10, '/tmp/duplicate', 'agent/duplicate', 'main',
		'head', 'target', 'acceptance', 'qg', 'default', 'policy', 'triage', 'ready', 'review_running')`, checkpointKey); err == nil {
		t.Fatal("duplicate active canonical key succeeded after index repair, want unique index failure")
	}
	if _, err := partialDB.ExecContext(ctx, `INSERT INTO review_recovery_attempts (
		checkpoint_id, failure_fingerprint, idempotency_key, strategy, action_json, status, started_at
	) VALUES (3, '', 'attempt-default-proof', 'legacy', '{}', 'failed', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("recovery insert without proof_json: %v", err)
	}
}

func assertSQLiteTableColumns(ctx context.Context, t *testing.T, db *sql.DB, table string, columns []string) {
	t.Helper()
	for _, column := range columns {
		var got string
		if err := db.QueryRowContext(ctx,
			"SELECT name FROM pragma_table_info(?) WHERE name = ?", table, column,
		).Scan(&got); err != nil {
			t.Errorf("%s missing column %q: %v", table, column, err)
		}
	}
}
