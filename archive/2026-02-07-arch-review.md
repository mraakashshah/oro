# [HISTORICAL] Architectural Review

> **Status: Historical.** This review targeted earlier spec versions (IPC comparison, lifecycle, memory specs). Many identified contradictions (C1-C3) and gaps (G1-G5) were resolved in `2026-02-08-oro-architecture-spec.md`. Retained as reference for the resolution rationale.

**Date:** 2026-02-07
**Bead:** oro-1s5
**Status:** Historical

## Summary

Green light with six actionable issues to resolve before implementation. The specs are coherent and well-designed. The main risks are: naming drift between specs, one missing SQLite table (the schema in the protocol package doesn't exist yet), a contradiction in the memory injection token budget, and two undefined inter-component interfaces.

## Contradictions

### C1: Memory injection token budget — 200 vs 500 tokens

The **memory system spec** says prompt injection uses a "200 token cap" (Approach 1, line ~192: "Inject top 3 into prompt as a `## Relevant Memories` section (200 token cap)"). The **memory search spec** explicitly overrides this to 500 tokens (Decision 5) and explains the rationale ("200 tokens... Very tight -- ~2 short memories"). The search spec also raises the memory count from 3 to 5.

**Resolution needed:** The memory system spec should be updated to reference 500 tokens / 5 memories, deferring to the search spec as authoritative.

### C2: Who prepares the worker prompt — "Manager" vs "Go wrapper"

The **memory search spec** read path says "Manager prepares worker prompt for bead" (both day-one and future sections). The **IPC comparison** (R5) and **lifecycle spec** (Phase 3) correctly say the Worker Go binary assembles the prompt. The Manager redesign confirms the Manager is a Claude session that only handles escalations — it never constructs prompts.

**Resolution needed:** The memory search spec should say "Worker Go binary" or "Go wrapper", not "Manager."

### C3: Memory file reference — `.oro/memories.jsonl` vs SQLite

The memory system spec references `.oro/memories.jsonl` in two places:
- Approach 1 injection: "Go wrapper queries `.oro/memories.jsonl` before constructing bead prompt" (line ~187)
- Elephant E3: "Memories are per-project (`.oro/memories.jsonl`)" (line ~233)

But the decided storage is SQLite (`memories` table in `.oro/state.db`). These are leftover references from an earlier draft.

**Resolution needed:** Replace `.oro/memories.jsonl` references with `.oro/state.db` / SQLite queries.

## Gaps

### G1: No ASSIGN payload for review context

When the Dispatcher spawns a reviewer ops agent (Phase 5, R6), it needs to communicate what bead to review and what mode (review vs implementation). The current `AssignPayload` only has `BeadID` and `Worktree`. There's no field indicating the assignment type (execute bead vs review bead vs resolve merge conflict vs diagnose stuck worker).

**Recommendation:** Add an `AssignType` field to `AssignPayload`:
```go
type AssignPayload struct {
    BeadID     string `json:"bead_id"`
    Worktree   string `json:"worktree"`
    AssignType string `json:"assign_type"` // "execute" | "review" | "merge_resolve" | "diagnose"
}
```

### G2: No feedback message type for review rejections

Phase 5 says "REJECTED -> Dispatcher sends feedback to Worker -> Worker fixes". But there is no message type for the Dispatcher to send review feedback to a Worker. The only Dispatcher-to-Worker messages are ASSIGN and SHUTDOWN. The Worker needs to receive rejection details (what failed, reviewer comments) to fix the issues.

**Recommendation:** Add a `MsgReviewFeedback` message type:
```go
MsgReviewFeedback MessageType = "REVIEW_FEEDBACK"
```
With a payload carrying the reviewer's findings.

### G3: No message type for ops agent results

Ops agents (merge resolver, reviewer, diagnostician) are spawned by the Dispatcher, but no protocol message type exists for them to report results back. They aren't Workers (they don't connect via UDS per the current design). The specs say they're short-lived `claude -p` processes.

**Question to resolve:** Do ops agents communicate via UDS (reusing the Worker protocol), or via exit code + stdout + file artifacts? If UDS, they need a RECONNECT-free subset of the protocol. If file-based, this needs to be specified.

**Recommendation:** Ops agents should use UDS with the existing Worker message types (DONE for success, STATUS for failure). Their `AssignPayload.AssignType` distinguishes them from regular workers. This keeps one protocol for all agent types.

### G4: Worker-local SQLite not defined in schema

The memory system spec (Resolved Question 2) says: "Worker INSERTs to a local SQLite in its worktree (`.oro/state.db`). On bead completion, Manager copies new rows to the main `.oro/state.db`."

But neither the protocol package nor any spec defines:
- The schema for the worker-local SQLite (same as main? subset?)
- Who performs the merge (the spec says "Manager" but the Manager is a Claude session — it should be the Dispatcher)
- The merge mechanism (INSERT from worker DB to main DB during the merge step)

**Recommendation:** Worker-local SQLite should use the same `memories` table schema. The Dispatcher (not Manager) copies rows during the merge phase. Define this as part of the merge coordinator (oro-68t) spec.

### G5: Code search hook is independent of the protocol package

The code search spec (oro-w2u) describes a `PreToolUse` hook binary (`oro-search-hook`) that is entirely independent of the Dispatcher/Worker protocol. This is correct -- it runs inside Claude Code sessions, not the Go orchestration layer. However, the spec doesn't clarify whether Workers (which run `claude -p`) will have the hook installed in their worktree's `.claude/hooks/` directory.

**Recommendation:** The Worker Go binary should ensure `.claude/hooks/oro-search-hook` exists in the worktree before spawning `claude -p`. Add this to the Worker startup checklist (already partially implied by R4's hook verification, but not explicit for the code search hook).

## Schema Coverage

### What's defined (across specs, not yet implemented in Go)

The IPC comparison spec defines these tables:

```sql
-- events: runtime event log
CREATE TABLE events (
    id INTEGER PRIMARY KEY,
    ts TEXT DEFAULT (datetime('now')),
    agent TEXT NOT NULL,
    bead_id TEXT,
    type TEXT NOT NULL,  -- ASSIGN | DONE | BLOCKED | HANDOFF | HEARTBEAT
    payload TEXT         -- JSON
);

-- assignments: who's working on what
CREATE TABLE assignments (
    agent TEXT PRIMARY KEY,
    bead_id TEXT,
    worktree TEXT,
    assigned_at TEXT
);
```

The memory system spec adds:

```sql
-- memories: cross-session learnings
CREATE TABLE memories (
    id INTEGER PRIMARY KEY,
    content TEXT NOT NULL,
    type TEXT NOT NULL,
    tags TEXT,
    source TEXT NOT NULL,
    bead_id TEXT,
    worker_id TEXT,
    confidence REAL DEFAULT 0.8,
    created_at TEXT DEFAULT (datetime('now')),
    embedding BLOB
);

CREATE VIRTUAL TABLE memories_fts USING fts5(
    content, tags, content=memories, content_rowid=id
);
```

The directive system (Manager -> Dispatcher) adds:

```sql
-- commands: Manager directives (implied by directive.go Command struct)
CREATE TABLE commands (
    id INTEGER PRIMARY KEY,
    ts TEXT DEFAULT (datetime('now')),
    directive TEXT NOT NULL,
    target TEXT,
    processed BOOLEAN DEFAULT 0
);
```

### Missing from schema

| Gap | Which spec needs it | Recommendation |
|-----|-------------------|---------------|
| **`events.type` values outdated** | IPC comparison says `ASSIGN \| DONE \| BLOCKED \| HANDOFF \| HEARTBEAT` but protocol has 8 message types including `READY_FOR_REVIEW`, `RECONNECT`, `STATUS`, and `SHUTDOWN`. Also no `BLOCKED` exists in the protocol. | Update the events table `type` column comment to match actual protocol message types. Drop `BLOCKED` or add it to protocol if needed. |
| **`assignments` table uses `agent` not `worker_id`** | Protocol payloads all use `worker_id`. The assignments table uses `agent`. | Rename to `worker_id` for consistency. |
| **No `assignments.status` column** | The Dispatcher needs to track assignment state (assigned, in_progress, reviewing, merging, done). The reconnection logic (R2) reconciles "SQLite says" vs "Worker says" but there's no status column in assignments. | Add `status TEXT NOT NULL DEFAULT 'assigned'` to the assignments table. |
| **No `assignments.model` column** | The Dispatcher does model routing (Opus vs Sonnet). No column tracks which model was assigned. | Add `model TEXT` to assignments for observability. |
| **No merge lock table** | R1 says "Manager acquires merge lock." The merge coordinator needs to serialize merges. No table or mechanism defined. | Either use a simple `merge_lock` table with one row, or use an in-memory mutex in the Dispatcher (simpler -- only one process merges). |

## Protocol Coverage

### Message types vs workflows

| Workflow | Required messages | Covered? |
|----------|------------------|----------|
| Bead assignment | ASSIGN | Yes |
| Worker heartbeat | HEARTBEAT | Yes |
| Worker state change | STATUS | Yes |
| Ralph handoff | HANDOFF | Yes |
| Work completion | DONE | Yes |
| Two-stage review request | READY_FOR_REVIEW | Yes |
| Worker reconnection | RECONNECT | Yes |
| Worker shutdown | SHUTDOWN | Yes |
| **Review feedback** | (none) | **No -- see G2** |
| **Ops agent result** | (none) | **Unclear -- see G3** |

### Directives vs workflows

| Workflow | Required directive | Covered? |
|----------|-------------------|----------|
| Session start (activate Dispatcher) | `start` | Yes |
| Session stop (graceful shutdown) | `stop` | Yes |
| Hold new work | `pause` | Yes |
| Epic prioritization | `focus` | Yes |
| **Resume from pause** | `start` (reuse) | Yes (implicit) |
| **Preempt worker for P0** | (none) | **Not a directive -- Dispatcher handles autonomously. OK.** |

### Missing protocol element: SHUTDOWN payload

The `SHUTDOWN` message type exists but has no payload struct defined in `message.go`. The `Message` struct has no `Shutdown *ShutdownPayload` field. Currently SHUTDOWN is fire-and-forget (just the type field), but R2 mentions potential reasons for shutdown (bead reassigned, bead closed). A payload with a `Reason` field would help workers log why they were shut down.

**Recommendation:** Add `ShutdownPayload` with a `Reason` field, and add it to the `Message` struct.

## Naming Inconsistencies

| Concept | Name in spec A | Name in spec B | Recommendation |
|---------|---------------|---------------|----------------|
| The Go coordination binary | "Manager" (IPC comparison, historical) | "Dispatcher" (Manager redesign, lifecycle) | Use **Dispatcher** everywhere. The IPC doc is marked historical but still referenced. |
| Worker identifier | `agent` (events table, assignments table) | `worker_id` (all protocol payloads) | Use **worker_id** everywhere. |
| Memory file | `.oro/memories.jsonl` (memory system spec, 2 places) | `.oro/state.db` memories table (memory system spec, decided) | Use **`.oro/state.db`**. Fix the stale JSONL references. |
| Token budget for memory injection | "200 token cap" (memory system spec) | "500 tokens" (memory search spec) | Use **500 tokens** (search spec is authoritative). |
| Who assembles prompt | "Manager" (memory search spec read path) | "Go wrapper" / "Worker Go binary" (IPC R5, lifecycle Phase 3) | Use **Worker Go binary**. |
| Runtime state database | `.oro/state.db` (IPC comparison, lifecycle) | `.oro/state.db` (memory spec, consistent) | Consistent. No issue. |
| Bead merge context location | "bead annotations" (R1, lifecycle Phase 6) | "bead annotations" (consistent) | Consistent. No issue. |

## Recommendations

### Before starting implementation (ordered by priority)

1. **Fix the three contradictions (C1-C3).** Update the memory system spec to say 500 tokens / 5 memories, replace `.oro/memories.jsonl` with `.oro/state.db`, and update the memory search spec to say "Worker Go binary" instead of "Manager." These are doc-only fixes that prevent implementers from building to the wrong spec.

2. **Add `AssignType` to `AssignPayload` (G1).** This is needed before implementing the Dispatcher (oro-w6n) or ops agent spawner (oro-rue). Without it, the protocol can't distinguish worker assignments from review assignments from merge resolution assignments.

3. **Add `REVIEW_FEEDBACK` message type (G2).** Needed before implementing two-stage review in the Dispatcher. Without it, rejected reviews have no way to send feedback to the worker.

4. **Decide ops agent communication channel (G3).** Before implementing the ops agent spawner (oro-rue), decide whether ops agents use UDS or file-based communication. Recommendation: UDS with the same protocol.

5. **Add `ShutdownPayload` to protocol.** Small addition, improves observability.

6. **Normalize schema naming.** Change `agent` to `worker_id` in the events and assignments table definitions. This can be done when implementing the schema in Go (oro-2jb).

7. **Add `status` column to assignments table.** The reconnection logic (R2) needs it. Can be done during oro-2jb implementation.

8. **Define worker-local SQLite merge strategy (G4).** Should be part of oro-68t (merge coordinator) implementation. The Dispatcher copies memory rows from worker-local DB to main DB during merge.

9. **Ensure code search hook deployment in worktrees (G5).** Add to Worker Go binary startup checklist during oro-773 implementation.

### Items that are fine as-is

- The protocol message types cover all major workflows except review feedback.
- The directive system (start/stop/pause/focus) covers all Manager-to-Dispatcher control flows.
- The three-layer memory architecture (bead annotations, handoff files, project memory) has clear ownership boundaries.
- The code search system is properly independent of the orchestration protocol.
- The hybrid UDS + SQLite architecture addresses all 10 original objectives.
- The FTS5 -> hybrid RRF upgrade path is clean (embedding column reserved, scoring formula accommodates both).
- The `Command` struct in `directive.go` matches the `commands` table design.
