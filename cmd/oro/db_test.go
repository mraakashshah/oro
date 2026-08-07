package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestOpenDB_PingSucceeds(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// db.Ping should already have been called inside openDB;
	// verify the connection is usable.
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping after openDB: %v", err)
	}
}

func TestOpenDB_WALModeEnabled(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", journalMode)
	}
}

func TestOpenDB_BusyTimeoutSet(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	var busyTimeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("expected busy_timeout=5000, got %d", busyTimeout)
	}
}

func TestOpenDB_CreatesParentDirs(t *testing.T) {
	// Regression: CI failed because ~/.oro/projects/<name>/ didn't exist.
	// openDB must create intermediate directories automatically.
	dbPath := filepath.Join(t.TempDir(), "nested", "subdir", "test.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB should create parent dirs: %v", err)
	}
	defer func() { _ = db.Close() }()
}

func TestOpenDB_InvalidPath(t *testing.T) {
	// Opening a DB under an uncreatable root should fail.
	_, err := openDB("/nonexistent/dir/test.db")
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

func TestOpenDB_ReturnsUsableDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Verify we can execute SQL statements.
	_, err = db.Exec("CREATE TABLE test_tbl (id INTEGER PRIMARY KEY, val TEXT)")
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = db.Exec("INSERT INTO test_tbl (val) VALUES (?)", "hello")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	var val string
	if err := db.QueryRow("SELECT val FROM test_tbl WHERE id = 1").Scan(&val); err != nil {
		t.Fatalf("select: %v", err)
	}
	if val != "hello" {
		t.Errorf("expected 'hello', got %q", val)
	}
}

// TestMigrationsAppliedOnStartup verifies that migrateStateDB adds missing
// columns to an old-schema database. This simulates the case where state.db
// was created with an older SchemaDDL that lacked attempt_count/handoff_count.
func TestMigrationsAppliedOnStartup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Create old-schema assignments table WITHOUT attempt_count/handoff_count.
	oldSchema := `CREATE TABLE IF NOT EXISTS assignments (
		id INTEGER PRIMARY KEY,
		bead_id TEXT NOT NULL,
		worker_id TEXT NOT NULL,
		worktree TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		assigned_at TEXT NOT NULL DEFAULT (datetime('now')),
		completed_at TEXT
	)`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("create old schema: %v", err)
	}

	// Insert a row to verify migration preserves data.
	if _, err := db.Exec(`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES ('b1', 'w1', '/tmp/wt')`); err != nil {
		t.Fatalf("insert test row: %v", err)
	}

	// Run migrations — this is the function under test.
	migrateStateDB(db)

	// Verify attempt_count and handoff_count columns exist and have defaults.
	var attemptCount, handoffCount int
	err = db.QueryRow(`SELECT attempt_count, handoff_count FROM assignments WHERE bead_id='b1'`).
		Scan(&attemptCount, &handoffCount)
	if err != nil {
		t.Fatalf("query migrated columns: %v", err)
	}
	if attemptCount != 0 {
		t.Errorf("expected attempt_count=0, got %d", attemptCount)
	}
	if handoffCount != 0 {
		t.Errorf("expected handoff_count=0, got %d", handoffCount)
	}

	// Running migrations again should be idempotent (no errors).
	migrateStateDB(db)
}

// TestOpenStateDBCreatesSchema verifies that openStateDB applies SchemaDDL
// so that tables like events are immediately queryable on a fresh database.
func TestOpenStateDBCreatesSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// The events table must exist and be queryable on a fresh DB.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		t.Fatalf("query events table: %v — schema was not applied", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows in fresh events table, got %d", count)
	}
}

// TestOpenStateDBIdempotent verifies that calling openStateDB on an existing
// DB with schema already applied does not error (CREATE TABLE IF NOT EXISTS).
func TestOpenStateDBIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")

	// First call — creates schema.
	db1, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("first openStateDB: %v", err)
	}
	_ = db1.Close()

	// Second call — schema already exists, should be idempotent.
	db2, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("second openStateDB: %v", err)
	}
	defer func() { _ = db2.Close() }()

	var count int
	if err := db2.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		t.Fatalf("query events after idempotent open: %v", err)
	}
}

func TestOpenStateDBConcurrentSchemaSetupIsIdempotent(t *testing.T) {
	const openerCount = 4

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	seed, err := openStateDBWithV4Migration(dbPath)
	if err != nil {
		t.Fatalf("seed current state DB: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close current state DB: %v", err)
	}
	// Complete post-v4 startup repairs before measuring current-schema opens.
	warmed, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("warm current state DB schema: %v", err)
	}
	if err := warmed.Close(); err != nil {
		t.Fatalf("close warmed state DB: %v", err)
	}

	locker, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open schema locker: %v", err)
	}
	defer func() { _ = locker.Close() }()
	lockConn, err := locker.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve schema locker connection: %v", err)
	}
	defer func() { _ = lockConn.Close() }()
	if _, err := lockConn.ExecContext(ctx, `PRAGMA journal_mode=DELETE`); err != nil {
		t.Fatalf("set rollback journal mode: %v", err)
	}
	if _, err := lockConn.ExecContext(ctx, `BEGIN EXCLUSIVE`); err != nil {
		t.Fatalf("hold exclusive schema lock: %v", err)
	}
	locked := true
	defer func() {
		if locked {
			_, _ = lockConn.ExecContext(ctx, `ROLLBACK`)
		}
	}()

	var schemaVersionBefore int
	if err := lockConn.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&schemaVersionBefore); err != nil {
		t.Fatalf("query schema version before concurrent opens: %v", err)
	}
	viewsBefore := dbTestQueueViewDefinitions(ctx, t, lockConn)
	if len(viewsBefore) != 3 {
		t.Fatalf("canonical queue view count = %d, want 3", len(viewsBefore))
	}

	type openResult struct {
		db  *sql.DB
		err error
	}
	results := make(chan openResult, openerCount)
	for range openerCount {
		go func() {
			db, openErr := openStateDB(dbPath)
			results <- openResult{db: db, err: openErr}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := lockConn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatalf("release exclusive schema lock: %v", err)
	}
	locked = false

	for range openerCount {
		select {
		case result := <-results:
			if result.err != nil {
				t.Errorf("concurrent openStateDB: %v", result.err)
				continue
			}
			if err := result.db.Close(); err != nil {
				t.Errorf("close concurrent state DB: %v", err)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("timed out waiting for concurrent openStateDB")
		}
	}

	var schemaVersionAfter int
	if err := lockConn.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&schemaVersionAfter); err != nil {
		t.Fatalf("query schema version after concurrent opens: %v", err)
	}
	if schemaVersionAfter != schemaVersionBefore {
		t.Errorf("schema_version after concurrent opens = %d, want unchanged %d", schemaVersionAfter, schemaVersionBefore)
	}
	viewsAfter := dbTestQueueViewDefinitions(ctx, t, lockConn)
	if !reflect.DeepEqual(viewsAfter, viewsBefore) {
		t.Errorf("queue view definitions changed after concurrent opens\nbefore: %#v\n after: %#v", viewsBefore, viewsAfter)
	}
}

func TestOpenStateDBRepairsStaleQueueViews(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openStateDBWithV4Migration(dbPath)
	if err != nil {
		t.Fatalf("seed current state DB: %v", err)
	}
	canonical := dbTestQueueViewDefinitions(ctx, t, db)
	if len(canonical) != 3 {
		t.Fatalf("canonical queue view count = %d, want 3", len(canonical))
	}
	if _, err := db.ExecContext(ctx, `
DROP VIEW beads_ready;
DROP VIEW beads_blocked;
DROP VIEW review_checkpoints_blocking_assignment;
CREATE VIEW review_checkpoints_blocking_assignment AS
SELECT id, bead_id FROM review_checkpoints WHERE state = 'review_running';
CREATE VIEW beads_ready AS SELECT b.* FROM beads b WHERE 0;
CREATE VIEW beads_blocked AS SELECT b.* FROM beads b WHERE 0;`); err != nil {
		t.Fatalf("install stale queue views: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close stale state DB: %v", err)
	}

	const openerCount = 4
	type openResult struct {
		db  *sql.DB
		err error
	}
	start := make(chan struct{})
	results := make(chan openResult, openerCount)
	for range openerCount {
		go func() {
			<-start
			opened, openErr := openStateDB(dbPath)
			results <- openResult{db: opened, err: openErr}
		}()
	}
	close(start)
	for range openerCount {
		select {
		case result := <-results:
			if result.err != nil {
				t.Errorf("concurrent stale queue view repair: %v", result.err)
				continue
			}
			if err := result.db.Close(); err != nil {
				t.Errorf("close repaired state DB: %v", err)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("timed out waiting for concurrent stale queue view repair")
		}
	}

	db, err = openDB(dbPath)
	if err != nil {
		t.Fatalf("inspect repaired queue views: %v", err)
	}
	defer func() { _ = db.Close() }()
	repaired := dbTestQueueViewDefinitions(ctx, t, db)
	if !reflect.DeepEqual(repaired, canonical) {
		t.Errorf("repaired queue views are not canonical\nwant: %#v\n got: %#v", canonical, repaired)
	}
}

type dbTestQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func dbTestQueueViewDefinitions(ctx context.Context, t *testing.T, db dbTestQueryer) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
SELECT name, sql
FROM sqlite_schema
WHERE type = 'view'
  AND name IN ('beads_ready', 'beads_blocked', 'review_checkpoints_blocking_assignment')
ORDER BY name`)
	if err != nil {
		t.Fatalf("query queue view definitions: %v", err)
	}
	defer func() { _ = rows.Close() }()

	definitions := make(map[string]string)
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatalf("scan queue view definition: %v", err)
		}
		definitions[name] = definition
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate queue view definitions: %v", err)
	}
	return definitions
}

func TestOpenStateDBPreV4PreservesReviewCheckpointReadyExclusion(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("open pre-v4 state DB: %v", err)
	}
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("apply state schema: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("apply pre-v4 bead schema: %v", err)
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
	seedDBTestReviewCheckpoint(ctx, t, db, "review-owned", assignmentID, "review_running")
	if err := db.Close(); err != nil {
		t.Fatalf("close pre-v4 state DB: %v", err)
	}

	db, err = openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB pre-v4 migration: %v", err)
	}
	defer func() { _ = db.Close() }()
	assertDBTestReadyCount(ctx, t, db, "review-owned", 0)
	if _, err := db.ExecContext(ctx, `UPDATE review_checkpoints SET state='integrated' WHERE bead_id='review-owned'`); err != nil {
		t.Fatalf("integrate review checkpoint: %v", err)
	}
	assertDBTestReadyCount(ctx, t, db, "review-owned", 1)
}

func seedDBTestReviewCheckpoint(ctx context.Context, t *testing.T, db *sql.DB, beadID string, assignmentID int64, state string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
INSERT INTO review_checkpoints (
  checkpoint_key, bead_id, origin_assignment_id, worktree, branch, target_branch,
  head_sha, target_sha, acceptance_hash, qg_script_hash, qg_mode,
  review_policy_hash, triage_revision, ready_attempt, state
) VALUES (?, ?, ?, ?, ?, 'main', 'head', 'target', 'acceptance', 'qg', 'full', 'policy', 'triage', 'ready', ?)`,
		"checkpoint-"+beadID, beadID, assignmentID, "/tmp/"+beadID, protocol.BranchPrefix+beadID, state); err != nil {
		t.Fatalf("insert review checkpoint: %v", err)
	}
}

func assertDBTestReadyCount(ctx context.Context, t *testing.T, db *sql.DB, beadID string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads_ready WHERE id=?`, beadID).Scan(&got); err != nil {
		t.Fatalf("query beads_ready: %v", err)
	}
	if got != want {
		t.Fatalf("beads_ready count for %q = %d, want %d", beadID, got, want)
	}
}

func TestOpenStateDBWithV4MigrationRepairsBadFTSTriggers(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openStateDBWithV4Migration(dbPath)
	if err != nil {
		t.Fatalf("openStateDBWithV4Migration: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO beads (id, title, status, type) VALUES ('repair-target', 'repair target', 'open', 'task')`); err != nil {
		t.Fatalf("insert bead: %v", err)
	}
	_ = db.Close()

	db, err = openDB(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	if _, err := db.Exec(`
CREATE TRIGGER beads_ai AFTER INSERT ON beads BEGIN
  INSERT INTO beads_fts(rowid, title, description, acceptance_criteria, status, type, parent_id, owner)
  VALUES (new.rowid, new.title, new.description, new.acceptance_criteria, new.status, new.type, new.parent_id, new.owner);
END;
CREATE TRIGGER beads_au AFTER UPDATE ON beads BEGIN
  INSERT INTO beads_fts(beads_fts, rowid, title, description, acceptance_criteria, status, type, parent_id, owner)
  VALUES ('delete', old.rowid, old.title, old.description, old.acceptance_criteria, old.status, old.type, old.parent_id, old.owner);
  INSERT INTO beads_fts(rowid, title, description, acceptance_criteria, status, type, parent_id, owner)
  VALUES (new.rowid, new.title, new.description, new.acceptance_criteria, new.status, new.type, new.parent_id, new.owner);
END;`); err != nil {
		t.Fatalf("install bad triggers: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 4`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	_ = db.Close()

	db, err = openStateDBWithV4Migration(dbPath)
	if err != nil {
		t.Fatalf("openStateDBWithV4Migration repair: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`UPDATE beads SET status='in_progress' WHERE id='repair-target'`); err != nil {
		t.Fatalf("update bead after startup repair: %v", err)
	}
}

func TestBackupStateDBForV4ReturnsErrorForMissingSource(t *testing.T) {
	backupPath, err := backupStateDBForV4(filepath.Join(t.TempDir(), "missing-state.db"))
	if err == nil {
		t.Fatal("expected missing source error")
	}
	if backupPath != "" {
		t.Fatalf("backupPath = %q, want empty on error", backupPath)
	}
	if !strings.Contains(err.Error(), "open state db for v4 backup") {
		t.Fatalf("error = %q, want open state db context", err)
	}
}

func TestBackupStateDBForV4CopiesSource(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	want := []byte("state-v3\nwith content")
	if err := os.WriteFile(dbPath, want, 0o600); err != nil {
		t.Fatalf("write source db: %v", err)
	}

	backupPath, err := backupStateDBForV4(dbPath)
	if err != nil {
		t.Fatalf("backupStateDBForV4: %v", err)
	}
	if backupPath == dbPath {
		t.Fatal("backup path must differ from source path")
	}
	if !strings.HasPrefix(backupPath, dbPath+".pre-v4-") {
		t.Fatalf("backup path = %q, want %q prefix", backupPath, dbPath+".pre-v4-")
	}
	got, err := os.ReadFile(backupPath) // #nosec G304 -- backupPath was returned by the function under test.
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("backup content = %q, want %q", got, want)
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("backup mode = %o, want 600", gotMode)
	}
}

func TestBackupStateDBForV4ReturnsErrorWhenBackupCreateFails(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, strings.Repeat("a", 240))
	if err := os.WriteFile(dbPath, []byte("state"), 0o600); err != nil {
		t.Fatalf("write source db: %v", err)
	}

	backupPath, err := backupStateDBForV4(dbPath)
	if err == nil {
		t.Fatal("expected backup create error")
	}
	if backupPath != "" {
		t.Fatalf("backupPath = %q, want empty on create error", backupPath)
	}
	if !strings.Contains(err.Error(), "create v4 backup") {
		t.Fatalf("error = %q, want create v4 backup context", err)
	}
}

func TestBackupStateDBForV4RemovesPartialBackupOnCopyFailure(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "state.db")
	if err := os.Mkdir(sourceDir, 0o700); err != nil {
		t.Fatalf("make source dir: %v", err)
	}

	backupPath, err := backupStateDBForV4(sourceDir)
	if err == nil {
		t.Fatal("expected copy error for directory source")
	}
	if backupPath != "" {
		t.Fatalf("backupPath = %q, want empty on copy error", backupPath)
	}
	matches, globErr := filepath.Glob(sourceDir + ".pre-v4-*")
	if globErr != nil {
		t.Fatalf("glob backups: %v", globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("partial backups were not removed: %v", matches)
	}
}

// TestOpenStateDB_SemanticMemoryMigrationsApplied is a regression test for
// the post-overhaul gap where semantic_memory_search_events (and related
// telemetry/chunk tables) existed as separate migration constants in
// pkg/protocol but were never wired into migrateStateDB. HybridSearch's
// logSearchEvent was failing silently with "no such table: memory_search_events"
// on every production query, dropping Phase 7 telemetry on the floor.
func TestOpenStateDB_SemanticMemoryMigrationsApplied(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// memory_search_events must be queryable (MigrateSemanticMemorySearchEvents).
	if _, err := db.Exec("INSERT INTO memory_search_events (query_hash) VALUES ('deadbeef')"); err != nil {
		t.Errorf("memory_search_events must be writable after openStateDB: %v", err)
	}

	// memory_chunks must exist (MigrateSemanticMemoryChunks). Query its
	// schema via PRAGMA rather than probing with INSERT — the insert path
	// was brittle to column-name drift.
	var chunkCol string
	if err := db.QueryRow("SELECT name FROM pragma_table_info('memory_chunks') WHERE name = 'memory_id'").Scan(&chunkCol); err != nil {
		t.Errorf("memory_chunks.memory_id column missing: %v", err)
	}
	if chunkCol != "memory_id" {
		t.Errorf("memory_chunks schema incomplete; expected memory_id column, got %q", chunkCol)
	}

	// Backfill state + embedding model must be seeded in kv_store
	// (MigrateSemanticMemoryBackfillState).
	var modelName string
	err = db.QueryRow("SELECT value FROM kv_store WHERE key = 'embedding_dense_model'").Scan(&modelName)
	if err != nil {
		t.Errorf("embedding_dense_model not seeded in kv_store: %v", err)
	} else if modelName != "bge-small-en-v1.5" {
		t.Errorf("embedding_dense_model = %q, want bge-small-en-v1.5", modelName)
	}

	// memories.embedding_dense column must exist (MigrateSemanticMemoryDense).
	// The column is added via bare ALTER TABLE so we verify by selecting it.
	if _, err := db.Exec("SELECT embedding_dense FROM memories LIMIT 1"); err != nil {
		t.Errorf("embedding_dense column missing on memories: %v", err)
	}
}

// TestBuildDispatcher_UsesOpenDB verifies that buildDispatcher produces a
// database with WAL mode and busy_timeout set (indirectly tests that it uses openDB).
func TestBuildDispatcher_WALMode(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

	d, db, err := buildDispatcher("", false, "")
	if err != nil {
		t.Fatalf("buildDispatcher: %v", err)
	}
	defer func() { _ = db.Close() }()
	_ = d

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", journalMode)
	}

	var busyTimeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("expected busy_timeout=5000, got %d", busyTimeout)
	}
}

// TestDefaultMemoryStore_WALMode verifies that defaultMemoryStore returns a
// database with WAL and busy_timeout. We test via the openDB path.
func TestDefaultMemoryStore_WALMode(t *testing.T) {
	tmpDir := t.TempDir()
	// Unset ORO_PROJECT so ResolveProjectDBPaths uses ORO_DB_PATH, not a project-scoped path.
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

	store, err := defaultMemoryStore()
	if err != nil {
		t.Fatalf("defaultMemoryStore: %v", err)
	}
	// The store wraps a *sql.DB; we can't access it directly, so open another
	// connection and check WAL was set on the file.
	db, err := dbutil.OpenDB(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("open for verification: %v", err)
	}
	defer func() { _ = db.Close() }()
	_ = store

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", journalMode)
	}
}
