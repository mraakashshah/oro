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
    qg_evidence_dir TEXT NOT NULL DEFAULT '',
    target_sha TEXT NOT NULL DEFAULT '',
	target_branch TEXT NOT NULL DEFAULT '',
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

-- Assignment-scoped bearer capability metadata. The raw bearer value is
-- intentionally never persisted; token_hash is SHA-256 encoded as hex.
CREATE TABLE IF NOT EXISTS assignment_capabilities (
    capability_id TEXT PRIMARY KEY,
    assignment_id INTEGER NOT NULL REFERENCES assignments(id),
    generation INTEGER NOT NULL,
    role TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('active', 'pending', 'superseded', 'revoked')),
    pending_replacement_id TEXT REFERENCES assignment_capabilities(capability_id),
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    acknowledged_at TEXT,
    superseded_at TEXT,
    revoked_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_assignment_capabilities_assignment
ON assignment_capabilities(assignment_id, generation, state);

-- Request nonces make capability-authenticated requests durable and
-- idempotent. response stores the prior serialized response, never a token.
CREATE TABLE IF NOT EXISTS assignment_capability_nonces (
    capability_id TEXT NOT NULL REFERENCES assignment_capabilities(capability_id),
    nonce TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (capability_id, nonce)
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

-- Durable ops subprocess runs for managerless orchestration.
CREATE TABLE IF NOT EXISTS ops_runs (
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
    completed_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_ops_runs_open
ON ops_runs(status, type, bead_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_ops_runs_blocking_key
ON ops_runs(type, bead_id)
WHERE status IN ('running', 'failed', 'stale');

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

-- Deduped quality-gate failure incidents keyed by normalized fingerprint.
CREATE TABLE IF NOT EXISTS qg_failure_incidents (
    id INTEGER PRIMARY KEY,
    fingerprint TEXT NOT NULL UNIQUE,
    class TEXT NOT NULL,
    decision TEXT NOT NULL,
    confidence TEXT NOT NULL,
    reason TEXT NOT NULL,
    summary TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'open',
    occurrence_count INTEGER NOT NULL DEFAULT 0,
    first_seen TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS qg_failure_occurrences (
    id TEXT PRIMARY KEY,
    incident_id INTEGER NOT NULL REFERENCES qg_failure_incidents(id),
    bead_id TEXT,
    worker_id TEXT,
    assignment_id INTEGER,
    component TEXT,
    output_hash TEXT NOT NULL,
    raw_output TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_qg_failure_occurrences_incident ON qg_failure_occurrences(incident_id);
CREATE INDEX IF NOT EXISTS idx_qg_failure_incidents_status ON qg_failure_incidents(status);

-- Durable recovery quarantine queue for unsafe or ambiguous recovery state.
CREATE TABLE IF NOT EXISTS recovery_quarantines (
    id INTEGER PRIMARY KEY,
    bead_id TEXT NOT NULL,
    assignment_id INTEGER,
    worker_id TEXT,
    worktree TEXT,
    branch TEXT,
    preserved_ref TEXT,
    reason TEXT NOT NULL,
    details TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'open',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    resolved_at TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_quarantines_open_unique
ON recovery_quarantines(bead_id, reason)
WHERE status = 'open';

CREATE INDEX IF NOT EXISTS idx_recovery_quarantines_status
ON recovery_quarantines(status);

-- Durable ledger of bounded monitor --act decisions. The monitor consults this
-- across process restarts so repeated health findings do not cause repeated
-- mutations in the same recovery window.
CREATE TABLE IF NOT EXISTS monitor_actions (
    id INTEGER PRIMARY KEY,
    action TEXT NOT NULL,
    action_key TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_monitor_actions_action_key_created
ON monitor_actions(action, action_key, created_at);

-- Tracks which epic fix beads have been created per (epic_id, fingerprint).
-- Prevents duplicate fix beads when the same QG fingerprint reappears for the same epic.
CREATE TABLE IF NOT EXISTS qg_epic_fix_beads (
    epic_id TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    bead_id TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (epic_id, fingerprint)
);

-- Evidence retained for worker-submitted work proposals. Evidence validation
-- and execution are owned by the dispatcher, while this table preserves the
-- durable record across controller restarts.
CREATE TABLE IF NOT EXISTS evidence_runs (
    id TEXT PRIMARY KEY,
    assignment_id INTEGER NOT NULL,
    worker_id TEXT NOT NULL,
    bead_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    argv_json TEXT NOT NULL DEFAULT '[]',
    manifest_hash TEXT,
    exit_code INTEGER,
    output TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    started_at TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_evidence_runs_assignment
ON evidence_runs(assignment_id, id);

-- Work proposals retain their provisional identity until the controller has
-- derived a canonical scope. In particular, fingerprint and scope_hint are
-- intentionally not unique here.
CREATE TABLE IF NOT EXISTS work_proposals (
    id TEXT PRIMARY KEY,
    assignment_id INTEGER NOT NULL,
    worker_id TEXT NOT NULL,
    bead_id TEXT NOT NULL,
    evidence_run_id TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    provisional_scope_hint TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL,
    summary TEXT NOT NULL,
    suggested_title TEXT NOT NULL DEFAULT '',
    suggested_type TEXT NOT NULL DEFAULT '',
    suggested_priority INTEGER NOT NULL DEFAULT 2,
    state TEXT NOT NULL DEFAULT 'pending',
    decision TEXT NOT NULL DEFAULT '',
    repair_attempts INTEGER NOT NULL DEFAULT 0,
    canonical_scope_key TEXT,
    executable_bead_id TEXT,
    generation INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_work_proposals_assignment
ON work_proposals(assignment_id, state);

CREATE TABLE IF NOT EXISTS work_proposal_transitions (
    proposal_id TEXT NOT NULL REFERENCES work_proposals(id),
    generation INTEGER NOT NULL,
    from_state TEXT NOT NULL DEFAULT '',
    to_state TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (proposal_id, generation)
);

CREATE TABLE IF NOT EXISTS work_proposal_events (
    proposal_id TEXT NOT NULL REFERENCES work_proposals(id),
    generation INTEGER NOT NULL,
    event_type TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (proposal_id, generation, event_type)
);

-- This is the replay boundary for worker submissions. response_json preserves
-- the exact original response, while content_hash rejects a reused client ID
-- carrying different content.
CREATE TABLE IF NOT EXISTS work_proposal_submissions (
    assignment_id INTEGER NOT NULL,
    client_proposal_id TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    proposal_id TEXT NOT NULL REFERENCES work_proposals(id),
    response_json TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (assignment_id, client_proposal_id)
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

const beadTableDDL = `
CREATE TABLE IF NOT EXISTS beads (
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
    deleted               INTEGER NOT NULL DEFAULT 0
);
`

const reviewCheckpointTableDDL = `CREATE TABLE IF NOT EXISTS review_checkpoints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    checkpoint_key TEXT NOT NULL,
    bead_id TEXT NOT NULL,
    origin_assignment_id INTEGER NOT NULL,
    current_assignment_id INTEGER,
    worker_id TEXT,
    worktree TEXT NOT NULL,
    branch TEXT NOT NULL,
    target_branch TEXT NOT NULL,
    head_sha TEXT NOT NULL,
    target_sha TEXT NOT NULL,
    acceptance_hash TEXT NOT NULL,
    qg_run_id TEXT,
    qg_script_hash TEXT NOT NULL,
    qg_mode TEXT NOT NULL,
    qg_output_hash TEXT,
    qg_evidence_path TEXT,
    qg_evidence_sha256 TEXT,
    review_policy_hash TEXT NOT NULL,
    triage_revision TEXT NOT NULL,
    ready_attempt TEXT NOT NULL,
    state TEXT NOT NULL,
    review_attempt INTEGER NOT NULL DEFAULT 0,
    recovery_attempt INTEGER NOT NULL DEFAULT 0,
    recovery_strategy TEXT,
    failure_fingerprint TEXT,
    next_recovery_at TEXT,
    quarantined_at TEXT,
    next_quarantine_reminder_at TEXT,
    quarantine_reminded_at TEXT,
    quarantine_reminder_count INTEGER NOT NULL DEFAULT 0,
    blockers_json TEXT NOT NULL DEFAULT '[]',
    verification_json TEXT NOT NULL DEFAULT '{}',
    summary TEXT NOT NULL DEFAULT '',
    artifact_path TEXT,
    artifact_sha256 TEXT,
    artifact_bytes INTEGER NOT NULL DEFAULT 0,
    recovery_artifact_path TEXT,
    recovery_artifact_sha256 TEXT,
    recovery_artifact_bytes INTEGER NOT NULL DEFAULT 0,
    recovery_artifact_finding_count INTEGER NOT NULL DEFAULT 0,
    ops_run_id INTEGER,
    integration_target_before_sha TEXT,
    integration_approved_head_sha TEXT,
    integration_observed_target_sha TEXT,
    integration_step TEXT,
    override_kind TEXT,
    override_source TEXT,
    overridden_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at TEXT
);`

const reviewCheckpointActiveKeyIndexDDL = `CREATE UNIQUE INDEX idx_review_checkpoints_active_key
ON review_checkpoints(checkpoint_key)
WHERE state <> 'superseded'`

const reviewCheckpointSchemaDDL = reviewCheckpointTableDDL + `
CREATE UNIQUE INDEX IF NOT EXISTS idx_review_checkpoints_active_key
ON review_checkpoints(checkpoint_key)
WHERE state <> 'superseded';
CREATE UNIQUE INDEX IF NOT EXISTS idx_review_checkpoints_ops_run
ON review_checkpoints(ops_run_id)
WHERE ops_run_id IS NOT NULL;
` + assignmentSideEffectAdmissionsTableDDL + reviewCheckpointFindingsTableDDL + reviewRecoveryAttemptsTableDDL + reviewQuarantineDeliveriesTableDDL

const assignmentSideEffectAdmissionsTableDDL = `
CREATE TABLE IF NOT EXISTS assignment_side_effect_admissions (
    bead_id TEXT PRIMARY KEY,
    owner_token TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`

const reviewCheckpointFindingsTableDDL = `
CREATE TABLE IF NOT EXISTS review_checkpoint_findings (
    checkpoint_id INTEGER NOT NULL,
    finding_id TEXT NOT NULL,
    severity TEXT NOT NULL,
    file TEXT NOT NULL,
    line INTEGER,
    contract_impact TEXT NOT NULL,
    required_action TEXT NOT NULL,
    compact_json TEXT NOT NULL,
    PRIMARY KEY(checkpoint_id, finding_id)
);`

const reviewRecoveryAttemptsTableDDL = `
CREATE TABLE IF NOT EXISTS review_recovery_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    checkpoint_id INTEGER NOT NULL,
    failure_fingerprint TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    strategy TEXT NOT NULL,
    action_json TEXT NOT NULL,
    status TEXT NOT NULL,
    proof_json TEXT NOT NULL DEFAULT '{}',
    started_at TEXT NOT NULL,
    completed_at TEXT
);`

const reviewQuarantineDeliveriesTableDDL = `
CREATE TABLE IF NOT EXISTS review_quarantine_deliveries (
    checkpoint_id INTEGER NOT NULL,
    scheduled_at TEXT NOT NULL,
    delivered_at TEXT,
    sink TEXT NOT NULL,
    PRIMARY KEY(checkpoint_id, scheduled_at, sink)
);`

const beadSchemaDDL = beadTableDDL + `
CREATE TABLE IF NOT EXISTS assignments (
    id INTEGER PRIMARY KEY,
    bead_id TEXT NOT NULL,
    worker_id TEXT NOT NULL,
    worktree TEXT NOT NULL,
    qg_evidence_dir TEXT NOT NULL DEFAULT '',
    target_sha TEXT NOT NULL DEFAULT '',
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

-- Branch-keyed admission state serializes epic branch inspection and mutation
-- across dispatcher processes and restarts.
CREATE TABLE IF NOT EXISTS epic_branch_admissions (
    branch TEXT PRIMARY KEY,
    epic_id TEXT NOT NULL,
    target_branch TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('leased', 'blocked', 'resolved')),
    generation INTEGER NOT NULL DEFAULT 1,
    lease_token TEXT,
    lease_owner TEXT,
    lease_expires_at TEXT,
    blocker_kind TEXT,
    checkout_path TEXT,
    branch_sha TEXT NOT NULL DEFAULT '',
    target_sha TEXT NOT NULL DEFAULT '',
    recovery_bead_id TEXT,
    details TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resolved_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_epic_branch_admissions_state
ON epic_branch_admissions(state);

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

` + BeadParentTouchTriggerDDL + BeadQueueViewsDDL

// BeadQueueViewsDDL is the canonical definition of the bead readiness views.
// Schema migrations must execute this exact DDL after rebuilding beads so
// durable review-checkpoint ownership cannot drift between schema versions.
const BeadQueueViewsDDL = `
DROP VIEW IF EXISTS beads_ready;
DROP VIEW IF EXISTS beads_blocked;
DROP VIEW IF EXISTS review_checkpoints_blocking_assignment;

-- This view is the single durable predicate for ordinary assignment admission.
-- Every checkpoint state blocks until review lifecycle ownership is terminal.
CREATE VIEW review_checkpoints_blocking_assignment AS
SELECT id, bead_id
FROM review_checkpoints
WHERE state NOT IN ('integrated', 'superseded');

CREATE VIEW IF NOT EXISTS beads_ready AS
SELECT b.*
FROM beads b
WHERE b.deleted = 0
  AND b.status = 'open'
  AND b.draft = 0
  AND (b.deferred_until IS NULL OR b.deferred_until = '' OR julianday(b.deferred_until) <= julianday('now'))
  AND NOT EXISTS (
    SELECT 1 FROM assignments a
    WHERE a.bead_id = b.id
      AND a.status = 'active'
  )
  AND NOT EXISTS (
    SELECT 1 FROM review_checkpoints_blocking_assignment checkpoint
    WHERE checkpoint.bead_id = b.id
  )
  AND NOT EXISTS (
    SELECT 1 FROM bead_deps d
    LEFT JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
    WHERE d.bead_id = b.id
      AND d.type IN ('blocks','conditional-blocks')
      AND (parent.id IS NULL OR parent.status != 'closed')
  )
  AND NOT EXISTS (
    SELECT 1 FROM bead_tags t
    WHERE t.bead_id = b.id
      AND t.tag = 'awaits_parent_close'
      AND (
           b.parent_id IS NULL
        OR NOT EXISTS (
               SELECT 1 FROM beads p
               WHERE p.id = b.parent_id
                 AND p.deleted = 0
                 AND p.status = 'closed'
           )
      )
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
    OR julianday(b.deferred_until) <= julianday('now')
    OR EXISTS (
      SELECT 1 FROM bead_deps d
      LEFT JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
      WHERE d.bead_id = b.id
        AND d.type IN ('blocks','conditional-blocks')
        AND (parent.id IS NULL OR parent.status != 'closed')
    )
    OR EXISTS (
      SELECT 1 FROM bead_tags t
      WHERE t.bead_id = b.id
        AND t.tag = 'awaits_parent_close'
        AND (
             b.parent_id IS NULL
          OR NOT EXISTS (
                 SELECT 1 FROM beads p
                 WHERE p.id = b.parent_id
                   AND p.deleted = 0
                   AND p.status = 'closed'
             )
        )
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
        AND d.type IN ('blocks','conditional-blocks')
        AND (parent.id IS NULL OR parent.status != 'closed')
    )
    OR EXISTS (
      SELECT 1 FROM bead_tags t
      WHERE t.bead_id = b.id
        AND t.tag = 'awaits_parent_close'
        AND (
             b.parent_id IS NULL
          OR NOT EXISTS (
                 SELECT 1 FROM beads p
                 WHERE p.id = b.parent_id
                   AND p.deleted = 0
                   AND p.status = 'closed'
             )
        )
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
	if err := ensureBeadContractColumns(ctx, db); err != nil {
		return fmt.Errorf("migrate bead contract columns: %w", err)
	}
	if err := ensureAssignmentEvidenceColumns(ctx, db); err != nil {
		return fmt.Errorf("migrate assignment evidence columns: %w", err)
	}
	if err := ensureReviewCheckpointSchema(ctx, db); err != nil {
		return fmt.Errorf("migrate review checkpoint schema: %w", err)
	}
	if err := ensureRecoveryQuarantineSchema(ctx, db); err != nil {
		return fmt.Errorf("migrate recovery quarantine schema: %w", err)
	}
	rebuiltStatusConstraint, err := ensureBeadStatusAllowsBlocked(ctx, db)
	if err != nil {
		return fmt.Errorf("migrate bead status constraint: %w", err)
	}
	if err := ensureOpsRunsDropsLegacyEscalationUnique(ctx, db); err != nil {
		return fmt.Errorf("migrate ops_runs legacy uniqueness: %w", err)
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

func ensureAssignmentEvidenceColumns(ctx context.Context, db *sql.DB) error {
	columns, exists, err := sqliteTableColumns(ctx, db, "assignments")
	if err != nil {
		return fmt.Errorf("inspect assignments columns: %w", err)
	}
	if !exists {
		return nil
	}
	for _, column := range []struct {
		name string
		ddl  string
	}{
		{name: "qg_evidence_dir", ddl: `ALTER TABLE assignments ADD COLUMN qg_evidence_dir TEXT NOT NULL DEFAULT ''`},
		{name: "target_sha", ddl: `ALTER TABLE assignments ADD COLUMN target_sha TEXT NOT NULL DEFAULT ''`},
		{name: "target_branch", ddl: `ALTER TABLE assignments ADD COLUMN target_branch TEXT NOT NULL DEFAULT ''`},
	} {
		if columns[column.name] {
			continue
		}
		if _, err := db.ExecContext(ctx, column.ddl); err != nil {
			return fmt.Errorf("add assignments.%s: %w", column.name, err)
		}
	}
	return nil
}

func ensureRecoveryQuarantineSchema(ctx context.Context, db *sql.DB) error {
	columns, exists, err := sqliteTableColumns(ctx, db, "recovery_quarantines")
	if err != nil {
		return fmt.Errorf("inspect recovery_quarantines columns: %w", err)
	}
	if !exists {
		return nil
	}
	if _, ok := columns["preserved_ref"]; ok {
		return nil
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE recovery_quarantines ADD COLUMN preserved_ref TEXT`); err != nil {
		return fmt.Errorf("add recovery_quarantines.preserved_ref: %w", err)
	}
	return nil
}

func ensureBeadContractColumns(ctx context.Context, db *sql.DB) error {
	columns, exists, err := sqliteTableColumns(ctx, db, "beads")
	if err != nil {
		return fmt.Errorf("inspect beads columns: %w", err)
	}
	if !exists {
		return nil
	}
	for column, definition := range map[string]string{
		"contract_version": "INTEGER NOT NULL DEFAULT 0",
		"draft":            "INTEGER NOT NULL DEFAULT 0",
	} {
		if _, ok := columns[column]; ok {
			continue
		}
		if _, err := db.ExecContext(ctx, `ALTER TABLE beads ADD COLUMN `+column+` `+definition); err != nil {
			return fmt.Errorf("add beads.%s: %w", column, err)
		}
	}
	return nil
}

func ensureReviewCheckpointSchema(ctx context.Context, db *sql.DB) error {
	columns, exists, err := sqliteTableColumns(ctx, db, "review_checkpoints")
	if err != nil {
		return fmt.Errorf("inspect review checkpoints: %w", err)
	}
	if exists && !hasCanonicalReviewCheckpointColumns(columns) {
		if err := rebuildReviewCheckpoints(ctx, db, columns); err != nil {
			return fmt.Errorf("rebuild legacy review checkpoints: %w", err)
		}
	}
	if err := ensureReviewCheckpointChildSchemas(ctx, db); err != nil {
		return fmt.Errorf("repair review checkpoint child schemas: %w", err)
	}
	if _, err := db.ExecContext(ctx, reviewCheckpointSchemaDDL); err != nil {
		return fmt.Errorf("create review checkpoint schema: %w", err)
	}
	if err := ensureReviewCheckpointActiveKeyIndex(ctx, db); err != nil {
		return fmt.Errorf("repair review checkpoint active key index: %w", err)
	}
	return nil
}

func ensureReviewCheckpointActiveKeyIndex(ctx context.Context, db *sql.DB) error {
	var indexSQL string
	err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'index' AND name = 'idx_review_checkpoints_active_key'`).Scan(&indexSQL)
	if err != nil {
		return fmt.Errorf("inspect active key index: %w", err)
	}
	if normalizeReviewCheckpointSchemaSQL(indexSQL) == normalizeReviewCheckpointSchemaSQL(reviewCheckpointActiveKeyIndexDDL) {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin active key index repair: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DROP INDEX idx_review_checkpoints_active_key`); err != nil {
		return fmt.Errorf("drop mismatched active key index: %w", err)
	}
	if _, err := tx.ExecContext(ctx, reviewCheckpointActiveKeyIndexDDL); err != nil {
		return fmt.Errorf("create canonical active key index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit active key index repair: %w", err)
	}
	return nil
}

type reviewCheckpointChildSchema struct {
	table       string
	ddl         string
	constraints []string
	columns     []string
	notNull     []string
}

func ensureReviewCheckpointChildSchemas(ctx context.Context, db *sql.DB) error {
	for _, schema := range reviewCheckpointChildSchemas() {
		columns, exists, err := sqliteTableColumns(ctx, db, schema.table)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", schema.table, err)
		}
		if !exists || hasCanonicalReviewCheckpointChildSchema(ctx, db, schema, columns) {
			continue
		}
		if err := rebuildReviewCheckpointChildSchema(ctx, db, schema, columns); err != nil {
			return fmt.Errorf("rebuild %s: %w", schema.table, err)
		}
	}
	return nil
}

func reviewCheckpointChildSchemas() []reviewCheckpointChildSchema {
	return []reviewCheckpointChildSchema{
		{
			table:       "review_checkpoint_findings",
			ddl:         reviewCheckpointFindingsTableDDL,
			constraints: []string{"primarykey(checkpoint_id,finding_id)"},
			columns:     []string{"checkpoint_id", "finding_id", "severity", "file", "line", "contract_impact", "required_action", "compact_json"},
			notNull:     []string{"checkpoint_id", "finding_id", "severity", "file", "contract_impact", "required_action", "compact_json"},
		},
		{
			table:       "review_recovery_attempts",
			ddl:         reviewRecoveryAttemptsTableDDL,
			constraints: []string{"primarykeyautoincrement", "idempotency_keytextnotnullunique", "proof_jsontextnotnulldefault'{}'"},
			columns:     []string{"id", "checkpoint_id", "failure_fingerprint", "idempotency_key", "strategy", "action_json", "status", "proof_json", "started_at", "completed_at"},
			notNull:     []string{"checkpoint_id", "failure_fingerprint", "idempotency_key", "strategy", "action_json", "status", "proof_json", "started_at"},
		},
		{
			table:       "review_quarantine_deliveries",
			ddl:         reviewQuarantineDeliveriesTableDDL,
			constraints: []string{"primarykey(checkpoint_id,scheduled_at,sink)"},
			columns:     []string{"checkpoint_id", "scheduled_at", "delivered_at", "sink"},
			notNull:     []string{"checkpoint_id", "scheduled_at", "sink"},
		},
	}
}

func hasCanonicalReviewCheckpointChildSchema(ctx context.Context, db *sql.DB, schema reviewCheckpointChildSchema, columns map[string]bool) bool {
	for _, column := range schema.columns {
		if _, ok := columns[column]; !ok {
			return false
		}
	}
	for _, column := range schema.notNull {
		if !columns[column] {
			return false
		}
	}
	var tableSQL string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?`, schema.table).Scan(&tableSQL); err != nil {
		return false
	}
	normalized := normalizeReviewCheckpointSchemaSQL(tableSQL)
	for _, constraint := range schema.constraints {
		if !strings.Contains(normalized, constraint) {
			return false
		}
	}
	return true
}

func normalizeReviewCheckpointSchemaSQL(sqlText string) string {
	return strings.NewReplacer(" ", "", "\n", "", "\t", "").Replace(strings.ToLower(sqlText))
}

func rebuildReviewCheckpointChildSchema(ctx context.Context, db *sql.DB, schema reviewCheckpointChildSchema, columns map[string]bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	legacyTable := schema.table + "_legacy"
	if _, err := tx.ExecContext(ctx, `ALTER TABLE `+schema.table+` RENAME TO `+legacyTable); err != nil {
		return fmt.Errorf("rename legacy table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, schema.ddl); err != nil {
		return fmt.Errorf("create canonical table: %w", err)
	}
	insertColumns, selectColumns := reviewCheckpointChildCopyColumns(schema, columns)
	//nolint:gosec // G202: identifiers and expressions are selected from static migration lists.
	query := `INSERT INTO ` + schema.table + ` (` + strings.Join(insertColumns, ", ") + `) SELECT ` + strings.Join(selectColumns, ", ") + ` FROM ` + legacyTable
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("copy compatible rows: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE `+legacyTable); err != nil {
		return fmt.Errorf("drop legacy table: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit rebuild: %w", err)
	}
	return nil
}

func reviewCheckpointChildCopyColumns(schema reviewCheckpointChildSchema, columns map[string]bool) (insertColumns, selectColumns []string) {
	for _, column := range schema.columns {
		insertColumns = append(insertColumns, column)
		selectColumns = append(selectColumns, reviewCheckpointChildCopyExpression(schema.table, column, columns))
	}
	return insertColumns, selectColumns
}

func reviewCheckpointChildCopyExpression(table, column string, columns map[string]bool) string {
	if _, ok := columns[column]; ok {
		return column
	}
	switch table {
	case "review_checkpoint_findings":
		return reviewCheckpointFindingCopyExpression(column)
	case "review_recovery_attempts":
		return reviewRecoveryAttemptCopyExpression(column)
	case "review_quarantine_deliveries":
		return reviewQuarantineDeliveryCopyExpression(column)
	}
	return "NULL"
}

func reviewCheckpointFindingCopyExpression(column string) string {
	switch column {
	case "finding_id":
		return `'legacy-finding:' || rowid`
	case "severity":
		return `'unknown'`
	case "file", "contract_impact", "required_action":
		return `''`
	case "compact_json":
		return `'{}'`
	default:
		return "NULL"
	}
}

func reviewRecoveryAttemptCopyExpression(column string) string {
	switch column {
	case "id":
		return "rowid"
	case "checkpoint_id":
		return "0"
	case "failure_fingerprint":
		return `''`
	case "idempotency_key":
		return `'legacy-recovery:' || rowid`
	case "strategy":
		return `'legacy'`
	case "action_json", "proof_json":
		return `'{}'`
	case "status":
		return `'failed'`
	case "started_at":
		return "datetime('now')"
	default:
		return "NULL"
	}
}

func reviewQuarantineDeliveryCopyExpression(column string) string {
	switch column {
	case "checkpoint_id":
		return "0"
	case "scheduled_at":
		return "datetime('now') || ':' || rowid"
	case "sink":
		return `'legacy'`
	default:
		return "NULL"
	}
}

func sqliteTableColumns(ctx context.Context, db *sql.DB, table string) (columns map[string]bool, exists bool, err error) {
	rows, err := db.QueryContext(ctx, `SELECT name, "notnull" FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, false, fmt.Errorf("query table columns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	columns = make(map[string]bool)
	for rows.Next() {
		var name string
		var notNull int
		if err := rows.Scan(&name, &notNull); err != nil {
			return nil, false, fmt.Errorf("scan table column: %w", err)
		}
		columns[name] = notNull != 0
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate table columns: %w", err)
	}
	return columns, len(columns) > 0, nil
}

func hasCanonicalReviewCheckpointColumns(columns map[string]bool) bool {
	for _, column := range reviewCheckpointColumnNames() {
		if _, ok := columns[column]; !ok {
			return false
		}
	}
	return columns["checkpoint_key"]
}

func rebuildReviewCheckpoints(ctx context.Context, db *sql.DB, columns map[string]bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin review checkpoint rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The assignment-admission views depend on review_checkpoints. Drop them
	// inside the rebuild transaction so SQLite does not retarget the durable
	// predicate at review_checkpoints_legacy during ALTER TABLE. beadSchemaDDL
	// recreates both views after the checkpoint migration completes.
	if _, err := tx.ExecContext(ctx, `DROP VIEW IF EXISTS beads_ready`); err != nil {
		return fmt.Errorf("drop ready view before review checkpoint rebuild: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP VIEW IF EXISTS review_checkpoints_blocking_assignment`); err != nil {
		return fmt.Errorf("drop assignment admission view before review checkpoint rebuild: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE review_checkpoints RENAME TO review_checkpoints_legacy`); err != nil {
		return fmt.Errorf("rename legacy review checkpoints: %w", err)
	}
	if _, err := tx.ExecContext(ctx, reviewCheckpointTableDDL); err != nil {
		return fmt.Errorf("create canonical review checkpoints: %w", err)
	}

	insertColumns, selectColumns := reviewCheckpointCopyColumns(columns)
	//nolint:gosec // G202: column names and expressions are selected from static migration lists.
	query := `INSERT INTO review_checkpoints (` + strings.Join(insertColumns, ", ") + `) SELECT ` + strings.Join(selectColumns, ", ") + ` FROM review_checkpoints_legacy`
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("copy legacy review checkpoints: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE review_checkpoints_legacy`); err != nil {
		return fmt.Errorf("drop legacy review checkpoints: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit review checkpoint rebuild: %w", err)
	}
	return nil
}

func reviewCheckpointCopyColumns(columns map[string]bool) (insertColumns, selectColumns []string) {
	insertColumns = reviewCheckpointColumnNames()
	selectColumns = make([]string, 0, len(insertColumns))
	for _, column := range insertColumns {
		selectColumns = append(selectColumns, reviewCheckpointCopyExpression(column, columns))
	}
	return insertColumns, selectColumns
}

func reviewCheckpointColumnNames() []string {
	return []string{
		"id", "checkpoint_key", "bead_id", "origin_assignment_id", "current_assignment_id", "worker_id",
		"worktree", "branch", "target_branch", "head_sha", "target_sha", "acceptance_hash",
		"qg_run_id", "qg_script_hash", "qg_mode", "qg_output_hash", "qg_evidence_path", "qg_evidence_sha256",
		"review_policy_hash", "triage_revision", "ready_attempt", "state", "review_attempt", "recovery_attempt",
		"recovery_strategy", "failure_fingerprint", "next_recovery_at", "quarantined_at", "next_quarantine_reminder_at",
		"quarantine_reminded_at", "quarantine_reminder_count", "blockers_json", "verification_json", "summary",
		"artifact_path", "artifact_sha256", "artifact_bytes", "recovery_artifact_path", "recovery_artifact_sha256",
		"recovery_artifact_bytes", "recovery_artifact_finding_count", "ops_run_id", "integration_target_before_sha",
		"integration_approved_head_sha", "integration_observed_target_sha", "integration_step", "override_kind",
		"override_source", "overridden_at", "created_at", "updated_at", "completed_at",
	}
}

func reviewCheckpointCopyExpression(column string, columns map[string]bool) string {
	if column == "checkpoint_key" {
		if _, ok := columns[column]; ok {
			return `CASE WHEN COALESCE(checkpoint_key, '') = '' THEN 'legacy-unverified:' || id ELSE checkpoint_key END`
		}
		return `'legacy-unverified:' || id`
	}
	if _, ok := columns[column]; ok {
		return column
	}
	switch column {
	case "bead_id", "worktree", "branch", "target_branch", "head_sha", "target_sha", "acceptance_hash", "qg_script_hash", "qg_mode", "review_policy_hash", "triage_revision", "ready_attempt":
		return `'legacy-unverified'`
	case "origin_assignment_id":
		return "0"
	case "state":
		return `'failed'`
	case "review_attempt", "recovery_attempt", "quarantine_reminder_count", "artifact_bytes", "recovery_artifact_bytes", "recovery_artifact_finding_count":
		return "0"
	case "blockers_json":
		return `'[]'`
	case "verification_json":
		return `'{}'`
	case "summary":
		return `''`
	case "created_at", "updated_at":
		return "datetime('now')"
	default:
		return "NULL"
	}
}

const currentOpsRunsTableDDL = `CREATE TABLE IF NOT EXISTS ops_runs (
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
    completed_at DATETIME
);`

const currentOpsRunsIndexesDDL = `CREATE INDEX IF NOT EXISTS idx_ops_runs_open
ON ops_runs(status, type, bead_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_ops_runs_blocking_key
ON ops_runs(type, bead_id)
WHERE status IN ('running', 'failed', 'stale');`

func ensureOpsRunsDropsLegacyEscalationUnique(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire sqlite connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	var tableSQL string
	err = conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name='ops_runs'`).Scan(&tableSQL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect ops_runs table: %w", err)
	}
	if !hasLegacyOpsRunsEscalationUnique(tableSQL) {
		return nil
	}
	return runOpsRunsLegacyUniqueRebuild(ctx, conn)
}

func hasLegacyOpsRunsEscalationUnique(tableSQL string) bool {
	normalized := strings.NewReplacer(" ", "", "\n", "", "\t", "").Replace(strings.ToLower(tableSQL))
	return strings.Contains(normalized, "unique(escalation_id,type,bead_id)")
}

func runOpsRunsLegacyUniqueRebuild(ctx context.Context, conn *sql.Conn) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ops_runs rebuild tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const opsRunsColumns = `id, escalation_id, type, bead_id, worker_id, dispatcher_pid, process_pid, runtime, model, status, verdict, feedback, error, started_at, completed_at`
	rebuildSteps := []string{
		`DROP INDEX IF EXISTS idx_ops_runs_blocking_key`,
		`DROP INDEX IF EXISTS idx_ops_runs_open`,
		`ALTER TABLE ops_runs RENAME TO ops_runs_legacy_unique_old`,
		currentOpsRunsTableDDL,
		`INSERT INTO ops_runs (` + opsRunsColumns + `) SELECT ` + opsRunsColumns + ` FROM ops_runs_legacy_unique_old`,
		`DROP TABLE ops_runs_legacy_unique_old`,
		currentOpsRunsIndexesDDL,
	}
	if err := execStmts(ctx, tx, rebuildSteps); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ops_runs rebuild tx: %w", err)
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
	if err := runBeadsStatusRebuild(ctx, conn, foreignKeysEnabled); err != nil {
		return false, err
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

// runBeadsStatusRebuild executes the legacy-alter-table rebuild sequence that
// adds 'blocked' to the beads.status CHECK constraint. The rebuild is wrapped
// in a transaction so any failure rolls back atomically, leaving the original
// beads table intact (oro-pyr2).
func runBeadsStatusRebuild(ctx context.Context, conn *sql.Conn, foreignKeysEnabled bool) error {
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	defer func() { _ = restoreSQLiteForeignKeys(context.Background(), conn, foreignKeysEnabled) }()
	if _, err := conn.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return fmt.Errorf("enable legacy alter table: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `PRAGMA legacy_alter_table=OFF`) }()

	if err := dropBeadSchemaRebuildTriggers(ctx, conn); err != nil {
		return err
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rebuild tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const beadColumns = `id, title, contract_version, draft, description, acceptance_criteria, status, priority, type, parent_id, owner, estimated_minutes, tier, model, deferred_until, close_reason, created_at, updated_at, closed_at, deleted`
	rebuildSteps := []string{
		`DROP VIEW IF EXISTS beads_ready`,
		`DROP VIEW IF EXISTS beads_blocked`,
		`ALTER TABLE beads RENAME TO beads_status_rebuild_old`,
		beadTableDDL,
		`INSERT INTO beads (` + beadColumns + `) SELECT ` + beadColumns + ` FROM beads_status_rebuild_old`,
		`DROP TABLE beads_status_rebuild_old`,
	}
	if err := execStmts(ctx, tx, rebuildSteps); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit rebuild tx: %w", err)
	}
	return nil
}

func sqliteForeignKeysEnabled(ctx context.Context, conn *sql.Conn) (bool, error) {
	var enabled int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return false, fmt.Errorf("query foreign_keys pragma: %w", err)
	}
	return enabled != 0, nil
}

func restoreSQLiteForeignKeys(ctx context.Context, conn *sql.Conn, enabled bool) error {
	if enabled {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
			return fmt.Errorf("restore foreign_keys on: %w", err)
		}
		return nil
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("restore foreign_keys off: %w", err)
	}
	return nil
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

func execStmts(ctx context.Context, tx *sql.Tx, stmts []string) error {
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("rebuild beads table: %w", err)
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

// MigrateSemanticMemoryReadEvents creates the memory_read_events table for
// recording legacy memory reads by operation and project. Idempotent.
const MigrateSemanticMemoryReadEvents = `
CREATE TABLE IF NOT EXISTS memory_read_events (
    id INTEGER PRIMARY KEY,
    ts DATETIME NOT NULL DEFAULT (datetime('now')),
    project TEXT,
    operation TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_mre_ts ON memory_read_events(ts);
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
