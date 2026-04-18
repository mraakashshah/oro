package protocol

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

-- Architect/manager pane SessionStart activity tracking
CREATE TABLE IF NOT EXISTS pane_activity (
    pane TEXT PRIMARY KEY,  -- 'architect' | 'manager'
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
