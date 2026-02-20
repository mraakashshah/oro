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
    pinned INTEGER DEFAULT 0
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
