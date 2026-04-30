package protocol

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// SchemaDDL defines the SQLite schema for the Oro dispatcher runtime database.
// Tables: events, assignments, commands, memories, memories_fts (FTS5).
// Execute against a SQLite database with: db.Exec(SchemaDDL)
const SchemaDDL = `
-- Runtime event log: all dispatcher/worker lifecycle events
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY,
    type TEXT NOT NULL,
    source TEXT NOT NULL,
    bead_id TEXT,
    worker_id TEXT,
    payload TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Worker-to-bead assignment tracking
CREATE TABLE IF NOT EXISTS assignments (
    id INTEGER PRIMARY KEY,
    bead_id TEXT NOT NULL,
    worker_id TEXT NOT NULL,
    worktree TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    assigned_at TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at TEXT,
    attempt_count INTEGER DEFAULT 0,
    handoff_count INTEGER DEFAULT 0
);

-- Normalize any legacy duplicate active rows before enforcing the invariant.
UPDATE assignments
SET status = 'completed',
    completed_at = COALESCE(completed_at, datetime('now'))
WHERE status = 'active'
  AND id NOT IN (
    SELECT MAX(id)
    FROM assignments
    WHERE status = 'active'
    GROUP BY bead_id
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_assignments_one_active_per_bead
ON assignments(bead_id)
WHERE status = 'active';

-- Manager directives to the dispatcher (start, stop, pause, focus)
CREATE TABLE IF NOT EXISTS commands (
    id INTEGER PRIMARY KEY,
    directive TEXT NOT NULL,
    args TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    processed_at TEXT
);

-- Cross-session project memory (learnings, decisions, gotchas, patterns)
CREATE TABLE IF NOT EXISTS memories (
    id INTEGER PRIMARY KEY,
    content TEXT NOT NULL,
    type TEXT NOT NULL,
    tags TEXT,
    source TEXT NOT NULL,
    bead_id TEXT,
    worker_id TEXT,
    confidence REAL DEFAULT 0.8,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    embedding BLOB,
    files_read TEXT DEFAULT '[]',
    files_modified TEXT DEFAULT '[]',
    pinned INTEGER DEFAULT 0,
    project TEXT DEFAULT 'oro'
);

-- Manager pane SessionStart activity tracking
CREATE TABLE IF NOT EXISTS pane_activity (
    pane TEXT PRIMARY KEY,  -- 'manager'
    last_seen INTEGER       -- unix timestamp (seconds since epoch)
);

-- Persistent escalation queue: dispatcher writes, manager acks
CREATE TABLE IF NOT EXISTS escalations (
    id INTEGER PRIMARY KEY,
    type TEXT NOT NULL,
    bead_id TEXT,
    worker_id TEXT,
    message TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    acked_at TEXT,
    retry_count INTEGER DEFAULT 0,
    last_retry_at TEXT
);

-- Persistent key-value store for dispatcher runtime state (e.g. embedder vocab)
CREATE TABLE IF NOT EXISTS kv_store (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Reviewer rejection history: stored separately from learnings so rejections
-- don't pollute the memory search index.
CREATE TABLE IF NOT EXISTS rejection_history (
    id INTEGER PRIMARY KEY,
    bead_id TEXT NOT NULL,
    worker_id TEXT,
    feedback TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_rejection_bead ON rejection_history(bead_id);

-- FTS5 full-text index over memories for BM25-ranked search
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    content,
    tags,
    content=memories,
    content_rowid=id
);

-- Triggers to keep FTS index in sync with memories table
CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, content, tags) VALUES (new.id, new.content, new.tags);
END;

CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, content, tags) VALUES ('delete', old.id, old.content, old.tags);
END;

CREATE TRIGGER IF NOT EXISTS memories_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, content, tags) VALUES ('delete', old.id, old.content, old.tags);
    INSERT INTO memories_fts(rowid, content, tags) VALUES (new.id, new.content, new.tags);
END;
`

const beadTableDDL = `
CREATE TABLE IF NOT EXISTS beads (
    id                    TEXT PRIMARY KEY,
    title                 TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    acceptance_criteria   TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL CHECK (status IN
                          ('open','in_progress','blocked','closed')),
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
`

const beadSchemaDDL = beadTableDDL + `
CREATE TABLE IF NOT EXISTS assignments (
    id INTEGER PRIMARY KEY,
    bead_id TEXT NOT NULL,
    worker_id TEXT NOT NULL,
    worktree TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    assigned_at TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at TEXT,
    attempt_count INTEGER DEFAULT 0,
    handoff_count INTEGER DEFAULT 0
);

UPDATE assignments
SET status = 'completed',
    completed_at = COALESCE(completed_at, datetime('now'))
WHERE status = 'active'
  AND id NOT IN (
    SELECT MAX(id)
    FROM assignments
    WHERE status = 'active'
    GROUP BY bead_id
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_assignments_one_active_per_bead
ON assignments(bead_id)
WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_beads_status     ON beads(status) WHERE deleted = 0;
CREATE INDEX IF NOT EXISTS idx_beads_parent     ON beads(parent_id) WHERE deleted = 0;
CREATE INDEX IF NOT EXISTS idx_beads_type       ON beads(type) WHERE deleted = 0;
CREATE INDEX IF NOT EXISTS idx_beads_priority   ON beads(priority) WHERE deleted = 0;
CREATE INDEX IF NOT EXISTS idx_beads_deferred   ON beads(deferred_until) WHERE deleted = 0;

CREATE TABLE IF NOT EXISTS bead_deps (
    bead_id          TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    depends_on_id    TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    type             TEXT NOT NULL DEFAULT 'blocks',
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    created_by       TEXT,
    PRIMARY KEY (bead_id, depends_on_id, type)
);
CREATE INDEX IF NOT EXISTS idx_bead_deps_depends_on ON bead_deps(depends_on_id);

CREATE TABLE IF NOT EXISTS bead_tags (
    bead_id    TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    tag        TEXT NOT NULL,
    PRIMARY KEY (bead_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_bead_tags_tag ON bead_tags(tag);

CREATE TABLE IF NOT EXISTS bead_labels (
    bead_id    TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    label      TEXT NOT NULL,
    PRIMARY KEY (bead_id, label)
);
CREATE INDEX IF NOT EXISTS idx_bead_labels_label ON bead_labels(label);

CREATE TABLE IF NOT EXISTS bead_metadata (
    bead_id    TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    PRIMARY KEY (bead_id, key)
);

CREATE TABLE IF NOT EXISTS bead_notes (
    id          INTEGER PRIMARY KEY,
    bead_id     TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    author      TEXT,
    content     TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_bead_notes_bead ON bead_notes(bead_id);

CREATE VIRTUAL TABLE IF NOT EXISTS beads_fts USING fts5(
    title, description, acceptance_criteria,
    content='beads', content_rowid='rowid'
);

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

` + BeadParentTouchTriggerDDL + `

DROP VIEW IF EXISTS beads_ready;
DROP VIEW IF EXISTS beads_blocked;

CREATE VIEW IF NOT EXISTS beads_ready AS
SELECT b.*
FROM beads b
WHERE b.deleted = 0
  AND b.status = 'open'
  AND (b.deferred_until IS NULL OR b.deferred_until = '')
  AND NOT EXISTS (
    SELECT 1 FROM assignments a
    WHERE a.bead_id = b.id
      AND a.status = 'active'
  )
  AND NOT EXISTS (
    SELECT 1 FROM bead_deps d
    LEFT JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
    WHERE d.bead_id = b.id
      AND d.type IN ('blocks','conditional-blocks','parent-child')
      AND (parent.id IS NULL OR parent.status != 'closed')
  );

CREATE VIEW IF NOT EXISTS beads_blocked AS
SELECT b.*
FROM beads b
WHERE b.deleted = 0
  AND b.status IN ('open','blocked')
  AND (
    b.status = 'blocked'
    OR b.deferred_until IS NULL
    OR b.deferred_until = ''
    OR EXISTS (
      SELECT 1 FROM bead_deps d
      LEFT JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
      WHERE d.bead_id = b.id
        AND d.type IN ('blocks','conditional-blocks')
        AND (parent.id IS NULL OR parent.status != 'closed')
    )
  )
  AND NOT EXISTS (
    SELECT 1 FROM assignments a
    WHERE a.bead_id = b.id
      AND a.status = 'active'
  )
  AND (
    b.status = 'blocked'
    OR EXISTS (
      SELECT 1 FROM bead_deps d
      LEFT JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
      WHERE d.bead_id = b.id
        AND d.type IN ('blocks','conditional-blocks','parent-child')
        AND (parent.id IS NULL OR parent.status != 'closed')
    )
  )
;
`

// BeadParentTouchTriggerNames names the triggers that bump a bead's updated_at
// after child-table mutations. Migrations can drop and recreate these around
// verbatim imports.
var BeadParentTouchTriggerNames = []string{ //nolint:gochecknoglobals // static migration metadata
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
}

// BeadParentTouchTriggerDDL creates the triggers listed in
// BeadParentTouchTriggerNames.
const BeadParentTouchTriggerDDL = `
CREATE TRIGGER IF NOT EXISTS bead_deps_touch_parent_ai AFTER INSERT ON bead_deps BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_deps_touch_parent_au AFTER UPDATE ON bead_deps
  WHEN old.type IS NOT new.type
    OR old.depends_on_id IS NOT new.depends_on_id
    OR old.created_at IS NOT new.created_at
    OR old.created_by IS NOT new.created_by
BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_deps_touch_parent_ad AFTER DELETE ON bead_deps BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = old.bead_id;
END;

CREATE TRIGGER IF NOT EXISTS bead_tags_touch_parent_ai AFTER INSERT ON bead_tags BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_tags_touch_parent_au AFTER UPDATE ON bead_tags
  WHEN old.tag IS NOT new.tag
BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_tags_touch_parent_ad AFTER DELETE ON bead_tags BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = old.bead_id;
END;

CREATE TRIGGER IF NOT EXISTS bead_labels_touch_parent_ai AFTER INSERT ON bead_labels BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_labels_touch_parent_au AFTER UPDATE ON bead_labels
  WHEN old.label IS NOT new.label
BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_labels_touch_parent_ad AFTER DELETE ON bead_labels BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = old.bead_id;
END;

CREATE TRIGGER IF NOT EXISTS bead_metadata_touch_parent_ai AFTER INSERT ON bead_metadata BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_metadata_touch_parent_au AFTER UPDATE ON bead_metadata
  WHEN old.value IS NOT new.value OR old.key IS NOT new.key
BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_metadata_touch_parent_ad AFTER DELETE ON bead_metadata BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = old.bead_id;
END;

CREATE TRIGGER IF NOT EXISTS bead_notes_touch_parent_ai AFTER INSERT ON bead_notes BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_notes_touch_parent_au AFTER UPDATE ON bead_notes
  WHEN old.content IS NOT new.content
    OR old.author IS NOT new.author
    OR old.created_at IS NOT new.created_at
BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_notes_touch_parent_ad AFTER DELETE ON bead_notes BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = old.bead_id;
END;
`

// MigrateBeadSchema adds the native bead store schema to the dispatcher state DB.
func MigrateBeadSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, beadSchemaDDL)
	if err != nil {
		return fmt.Errorf("migrate bead schema: %w", err)
	}
	rebuiltStatusConstraint, err := ensureBeadStatusAllowsBlocked(ctx, db)
	if err != nil {
		return fmt.Errorf("migrate bead status constraint: %w", err)
	}
	_, err = db.ExecContext(ctx, beadSchemaDDL)
	if err != nil {
		return fmt.Errorf("refresh bead schema: %w", err)
	}
	if rebuiltStatusConstraint {
		if _, err := db.ExecContext(ctx, `INSERT INTO beads_fts(beads_fts) VALUES('rebuild')`); err != nil {
			return fmt.Errorf("rebuild beads fts: %w", err)
		}
	}
	return nil
}

func ensureBeadStatusAllowsBlocked(ctx context.Context, db *sql.DB) (bool, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("acquire sqlite connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	var tableSQL string
	err = conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name='beads'`).Scan(&tableSQL)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect beads table: %w", err)
	}
	if strings.Contains(tableSQL, "'blocked'") {
		return false, nil
	}

	foreignKeysEnabled, err := sqliteForeignKeysEnabled(ctx, conn)
	if err != nil {
		return false, fmt.Errorf("inspect foreign_keys pragma: %w", err)
	}
	fkViolationsBefore, err := countSQLiteForeignKeyViolations(ctx, conn)
	if err != nil {
		return false, fmt.Errorf("count foreign keys before beads rebuild: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return false, fmt.Errorf("disable foreign keys: %w", err)
	}
	defer func() { _ = restoreSQLiteForeignKeys(context.Background(), conn, foreignKeysEnabled) }()
	if _, err := conn.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return false, fmt.Errorf("enable legacy alter table: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `PRAGMA legacy_alter_table=OFF`) }()

	if err := dropBeadSchemaRebuildTriggers(ctx, conn); err != nil {
		return false, err
	}

	const beadColumns = `id, title, description, acceptance_criteria, status, priority, type, parent_id, owner, estimated_minutes, tier, model, deferred_until, close_reason, created_at, updated_at, closed_at, deleted`
	rebuild := `
DROP VIEW IF EXISTS beads_ready;
DROP VIEW IF EXISTS beads_blocked;
ALTER TABLE beads RENAME TO beads_status_rebuild_old;
` + beadTableDDL + `
INSERT INTO beads (` + beadColumns + `)
SELECT ` + beadColumns + ` FROM beads_status_rebuild_old;
DROP TABLE beads_status_rebuild_old;
`
	if _, err := conn.ExecContext(ctx, rebuild); err != nil {
		return false, fmt.Errorf("rebuild beads table: %w", err)
	}
	fkViolationsAfter, err := countSQLiteForeignKeyViolations(ctx, conn)
	if err != nil {
		return false, fmt.Errorf("count foreign keys after beads rebuild: %w", err)
	}
	if fkViolationsAfter > fkViolationsBefore {
		return false, fmt.Errorf("foreign key violations increased after beads rebuild: before=%d after=%d", fkViolationsBefore, fkViolationsAfter)
	}
	return true, nil
}

func sqliteForeignKeysEnabled(ctx context.Context, conn *sql.Conn) (bool, error) {
	var enabled int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return false, err
	}
	return enabled != 0, nil
}

func restoreSQLiteForeignKeys(ctx context.Context, conn *sql.Conn, enabled bool) error {
	if enabled {
		_, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
		return err
	}
	_, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`)
	return err
}

func dropBeadSchemaRebuildTriggers(ctx context.Context, conn *sql.Conn) error {
	dropTriggers := make([]string, 0, 3+len(BeadParentTouchTriggerNames))
	dropTriggers = append(dropTriggers, "beads_fts_ai", "beads_fts_ad", "beads_fts_au")
	dropTriggers = append(dropTriggers, BeadParentTouchTriggerNames...)
	for _, name := range dropTriggers {
		if _, err := conn.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+name); err != nil {
			return fmt.Errorf("drop trigger %s: %w", name, err)
		}
	}
	return nil
}

func countSQLiteForeignKeyViolations(ctx context.Context, conn *sql.Conn) (int, error) {
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return 0, fmt.Errorf("check foreign keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var count int
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate foreign key check: %w", err)
	}
	return count, nil
}

// MigrateFileTracking adds files_read and files_modified columns to existing memories tables.
const MigrateFileTracking = `
ALTER TABLE memories ADD COLUMN files_read TEXT DEFAULT '[]';
ALTER TABLE memories ADD COLUMN files_modified TEXT DEFAULT '[]';
`

// MigratePinnedMemories adds the pinned column to existing memories tables.
// Uses a try/ignore pattern since SQLite doesn't support IF NOT EXISTS for ALTER TABLE.
const MigratePinnedMemories = `
ALTER TABLE memories ADD COLUMN pinned INTEGER DEFAULT 0;
`

// MigrateAssignmentCounts adds attempt_count and handoff_count columns to
// existing assignments tables. Uses a try/ignore pattern since SQLite doesn't
// support IF NOT EXISTS for ALTER TABLE.
const MigrateAssignmentCounts = `
ALTER TABLE assignments ADD COLUMN attempt_count INTEGER DEFAULT 0;
ALTER TABLE assignments ADD COLUMN handoff_count INTEGER DEFAULT 0;
`

// MigrateKVStore creates the kv_store table on existing databases.
// Uses CREATE TABLE IF NOT EXISTS so it is safe to run on any database.
const MigrateKVStore = `
CREATE TABLE IF NOT EXISTS kv_store (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`

// MigrateProjectColumn adds the project column to existing memories tables.
// Idempotent: will fail silently if column already exists (SQLite limitation).
// After running, execute: UPDATE memories SET project = 'oro' WHERE project IS NULL
const MigrateProjectColumn = `
ALTER TABLE memories ADD COLUMN project TEXT DEFAULT 'oro';
`

// MigrateRejectionHistory creates the rejection_history table and backfills it
// from memories rows that look like rejection feedback
// (content LIKE 'Reviewer rejected%'). After backfill those rows are deleted
// from memories so they no longer appear in oro memories list.
// Safe to apply on a fresh DB (rejection_history already exists via SchemaDDL).
const MigrateRejectionHistory = `
CREATE TABLE IF NOT EXISTS rejection_history (
    id INTEGER PRIMARY KEY,
    bead_id TEXT NOT NULL,
    worker_id TEXT,
    feedback TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
BEGIN;
INSERT INTO rejection_history (bead_id, worker_id, feedback, created_at)
SELECT
    COALESCE(bead_id, ''),
    COALESCE(worker_id, ''),
    SUBSTR(content, LENGTH('Reviewer rejected this bead: ') + 1),
    created_at
FROM memories
WHERE content LIKE 'Reviewer rejected this bead: %';
DELETE FROM memories WHERE content LIKE 'Reviewer rejected this bead: %';
COMMIT;
`

// MigrateSemanticMemoryDense adds embedding_dense and content_tokens columns
// to existing memories tables to support semantic memory embeddings.
// Uses a try/ignore pattern since SQLite doesn't support IF NOT EXISTS for ALTER TABLE.
const MigrateSemanticMemoryDense = `
ALTER TABLE memories ADD COLUMN embedding_dense BLOB;
ALTER TABLE memories ADD COLUMN content_tokens INTEGER DEFAULT 0;
`

// MigrateSemanticMemoryBackfillState initializes the backfill tracking state
// and sets the embedding model sentinel in the kv_store table.
// Uses INSERT OR IGNORE for idempotency.
const MigrateSemanticMemoryBackfillState = `
INSERT OR IGNORE INTO kv_store (key, value, updated_at) VALUES ('backfill_semantic_memory_state', 'pending', datetime('now'));
INSERT OR IGNORE INTO kv_store (key, value, updated_at) VALUES ('embedding_dense_model', 'bge-small-en-v1.5', datetime('now'));
`

// MigrateSemanticMemorySearchEvents creates the memory_search_events table for
// recording hybrid-search queries (query hash, top-k results, latency, feature
// flags). Idempotent: CREATE TABLE IF NOT EXISTS + CREATE INDEX IF NOT EXISTS.
const MigrateSemanticMemorySearchEvents = `
CREATE TABLE IF NOT EXISTS memory_search_events (
    id INTEGER PRIMARY KEY,
    ts DATETIME NOT NULL DEFAULT (datetime('now')),
    project TEXT,
    query_hash TEXT,
    top_k_ids TEXT,
    top_k_scores TEXT,
    latency_ms INTEGER,
    used_rerank INTEGER DEFAULT 0,
    used_bge INTEGER DEFAULT 0,
    ann_candidates INTEGER
);

CREATE INDEX IF NOT EXISTS idx_mse_ts ON memory_search_events(ts);
`

// MigrateSemanticMemoryChunks creates the memory_chunks table for storing
// chunked semantic memory embeddings. Each chunk belongs to a parent memory
// and includes the text and its embedding vector. ON DELETE CASCADE ensures
// that chunk orphans are cleaned up when the parent memory is deleted.
// Idempotent: CREATE TABLE IF NOT EXISTS guards both table and index creation.
const MigrateSemanticMemoryChunks = `
CREATE TABLE IF NOT EXISTS memory_chunks (
    id INTEGER PRIMARY KEY,
    memory_id INTEGER NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    chunk_idx INTEGER NOT NULL,
    text TEXT NOT NULL,
    embedding BLOB NOT NULL,
    UNIQUE(memory_id, chunk_idx)
);

CREATE INDEX IF NOT EXISTS idx_memory_chunks_memory_id ON memory_chunks(memory_id);
`
