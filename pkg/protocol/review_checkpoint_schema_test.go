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
		assertSQLiteTableColumns(t, ctx, db, table, columns)
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
	if err := protocol.MigrateBeadSchema(ctx, partialDB); err != nil {
		t.Fatalf("migrate partial review checkpoints: %v", err)
	}
	assertSQLiteTableColumns(t, ctx, partialDB, "review_checkpoints", []string{"recovery_artifact_path"})
}

func assertSQLiteTableColumns(t *testing.T, ctx context.Context, db *sql.DB, table string, columns []string) {
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
