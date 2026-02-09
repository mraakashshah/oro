# [HISTORICAL] Oro Agent Orchestrator: IPC & Coordination Comparison

> **Status: Historical.** Superseded by `2026-02-07-manager-redesign.md`. This doc assumed Manager = Go binary. Actual design: Manager = Claude session + Dispatcher (Go). IPC comparison analysis and resolved questions (R1-R6) remain valid reference material — the architectural framing changed, not the protocol-level decisions.

## Stated Objectives

From architecture docs, BCR postmortem, and design conversations:

| # | Objective | Source |
|---|-----------|--------|
| O1 | **Push-based coordination** — no polling | User requirement |
| O2 | **Daemon-driven** — long-lived processes, not ephemeral subagents | User requirement |
| O3 | **Tmux panes** — each agent visible in its own pane | User choice |
| O4 | **Beads as rich atomic units** — carry enough context to be self-contained | User requirement |
| O5 | **Stateless orchestrator** — all state externalized via storage layer | arch.md |
| O6 | **Crash-safe & restartable** — orchestrator restarts without losing state | Ouroboros Guarantee |
| O7 | **No zombie processes** — agent lifecycle tied to work lifecycle | BCR postmortem |
| O8 | **No race conditions** — concurrent agents can't corrupt shared state | BCR postmortem |
| O9 | **Workers ralph-loop** — cycle context autonomously until done or handoff | User requirement |
| O10 | **Three roles** — Architect (thinking), Manager (scheduling), Worker (doing) | User requirement |

### BCR Failure Modes to Avoid

| Failure | Root Cause | Lesson |
|---------|-----------|--------|
| 354 zombie watchers | Lifecycle not tied to worktree | O7: Supervised process trees |
| FF-only merge races | Main moved during agent rebase | O8: Serialize merges |
| Stale PID files block restart | No cleanup on crash | O6: Don't use PID files |
| Unstaged changes block merge | Agent exited without clean commit | O9: Ralph loop must guarantee clean exit |
| Pipe buffer fills silently | Watcher stopped consuming | O1: Bidirectional flow, backpressure |
| Test processes hung forever | No timeout/cancellation | O7: Supervised with kill |

---

## Reference Patterns Worth Adopting

| Pattern | Source | What It Gives Us |
|---------|--------|-----------------|
| **Memory compounding + YAML handoffs** | CC-v3 | Multi-session awareness, token-efficient state transfer, semantic recall |
| **Push-based dispatch + two-stage review** | Superpowers | Spec compliance + code quality gates before completion |
| **Git worktrees per agent** | Compound | Filesystem isolation, parallel development, clean merge path |
| **Push telemetry (exhaust pipe)** | BCR | Real-time progress visibility (good idea, brittle execution) |
| **Event-driven state machine** | Loom | Autonomous agent loop without polling, explicit state transitions |
| **Gateway as control plane** | OpenClaw | Single coordination point, WebSocket push |

---

## Three Proposed IPC Mechanisms

### Option A: Unix Domain Sockets (UDS)

Manager runs a UDS server. Workers connect on startup, send structured events (done/blocked/handoff/heartbeat). Manager pushes assignments and signals. Go's `net` package handles this natively.

```
┌──────────┐     UDS      ┌──────────┐     UDS      ┌──────────┐
│ Architect │────────────▶│  Manager  │◀────────────│  Worker  │
│ (tmux 0) │             │ (tmux 1)  │              │ (tmux 2) │
└──────────┘             └──────────┘              └──────────┘
                              │ UDS
                              ▼
                         ┌──────────┐
                         │  Worker  │
                         │ (tmux 3) │
                         └──────────┘
```

**Protocol:** Line-delimited JSON over UDS, bidirectional.

```json
// Worker → Manager
{"type": "STATUS", "bead_id": "beads-abc", "state": "done", "result": "merged"}
{"type": "HANDOFF", "bead_id": "beads-abc", "context": "...yaml..."}
{"type": "HEARTBEAT", "bead_id": "beads-abc", "context_pct": 28}

// Manager → Worker
{"type": "ASSIGN", "bead_id": "beads-xyz", "worktree": ".worktrees/beads-xyz"}
{"type": "SIGNAL", "action": "shutdown"}
```

### Option B: Structured Files + fsnotify

Like BCR but in Go with `fsnotify` (kernel-level file events, not polling). Events written as JSON files to a watched directory. Manager watches the directory for new files.

```
.oro/events/
├── 1707312345-worker1-done.json
├── 1707312346-worker2-blocked.json
└── 1707312347-worker1-heartbeat.json

.oro/assignments/
├── worker1.json    # Manager writes, worker reads
└── worker2.json
```

**Protocol:** One JSON file per event. Manager watches with fsnotify. Workers watch their assignment file.

### Option C: SQLite + WAL Notify

Shared SQLite database in WAL mode. Workers write status rows. Manager detects changes via `PRAGMA data_version` (increments on any write). Transactional — no race conditions by construction.

```sql
CREATE TABLE events (
    id INTEGER PRIMARY KEY,
    ts TEXT DEFAULT (datetime('now')),
    agent TEXT NOT NULL,
    bead_id TEXT,
    type TEXT NOT NULL,  -- ASSIGN | DONE | BLOCKED | HANDOFF | HEARTBEAT
    payload TEXT         -- JSON
);

CREATE TABLE assignments (
    agent TEXT PRIMARY KEY,
    bead_id TEXT,
    worktree TEXT,
    assigned_at TEXT
);
```

**Protocol:** INSERT to events table. Manager polls `data_version` (lightweight — no query, just pragma check). Or: use `sqlite3_update_hook` callback in Go for true push.

---

## Comparison Against Objectives

| Objective | A: UDS | B: fsnotify | C: SQLite+WAL |
|-----------|--------|-------------|----------------|
| **O1: Push-based** | True push. Bidirectional stream. | Kernel-level push (inotify/kqueue). No application polling. | Pseudo-push via `data_version` check or `update_hook` callback. |
| **O2: Daemon-driven** | Manager is a long-lived server. Natural fit. | Manager is a long-lived watcher. Natural fit. | Manager is a long-lived loop. Natural fit. |
| **O3: Tmux panes** | Each agent is a process in a tmux pane. UDS connects them. | Same — agents are processes. Files connect them. | Same — agents are processes. DB connects them. |
| **O4: Rich beads** | Bead context passed in ASSIGN payload or read from DB. IPC is orthogonal. | Same. | Bead context can live in the DB itself — natural colocation. |
| **O5: Stateless orchestrator** | State lives in connections + storage layer. Manager restarts = workers reconnect. | State lives in files. Manager restarts = re-scan directory. | State lives in DB. Manager restarts = re-query tables. |
| **O6: Crash-safe** | Connections drop on crash. Workers detect disconnect, pause. Manager restarts, workers reconnect. No orphan state. | Files persist on crash. Manager restarts, re-scans. But: partial writes can leave corrupt JSON. | DB is transactional. Crash-safe by construction. WAL survives power loss. |
| **O7: No zombies** | Manager knows all connected workers (connection = liveness). Disconnect = dead. No PID files needed. | Must infer liveness from heartbeat files + process checks. Same failure mode as BCR. | Must infer liveness from heartbeat rows + process checks. Same issue. |
| **O8: No races** | Sequential message processing in Manager's event loop. No shared mutable state. | File rename/delete races possible (BCR's exact problem). fsnotify can miss events under load. | Transactions eliminate races by construction. SQLite serializes all writes. |
| **O9: Ralph loop** | Worker loops internally. Sends HEARTBEAT with context %. Sends HANDOFF when cycling. Manager receives immediately. | Worker loops internally. Writes heartbeat files. Manager detects via fsnotify. | Worker loops internally. Writes heartbeat rows. Manager detects via hook/poll. |
| **O10: Three roles** | Architect ↔ Manager over UDS. Manager ↔ Workers over UDS. Clean role separation. | All roles communicate via filesystem. Less clear boundaries. | All roles communicate via DB. Clear boundaries via table design. |

---

## Comparison Against Reference Patterns

| Pattern | A: UDS | B: fsnotify | C: SQLite+WAL |
|---------|--------|-------------|----------------|
| **CC-v3: Memory + handoffs** | Handoff YAML sent over socket or stored in DB (orthogonal to IPC). | Handoff written as file (natural fit for fsnotify). | Handoff stored in DB table (natural fit for SQLite). |
| **Superpowers: Push dispatch** | Manager pushes ASSIGN message to worker socket. True push. | Manager writes assignment file, worker detects via fsnotify. Push-ish. | Manager writes assignment row. Worker polls or uses hook. Weakest push. |
| **Compound: Git worktrees** | Orthogonal — worktrees are filesystem isolation, IPC is how agents coordinate about them. | Worktree events (new files, .done markers) trigger fsnotify naturally. Tight coupling. | Orthogonal — worktree paths stored in DB, but worktree ops are filesystem. |
| **BCR: Exhaust pipe** | UDS replaces named pipes. Same push model, more reliable. No buffer limit. Bidirectional. | File events replace pipe events. Loses real-time streaming granularity. | DB rows replace pipe events. Loses real-time granularity. Gains queryability. |
| **Loom: State machine** | Agent state machine runs independently. Reports transitions over socket. | Agent state machine runs independently. Reports transitions via files. | Agent state machine runs independently. Reports transitions via DB rows. |
| **OpenClaw: Gateway** | Manager IS the gateway. UDS is the local equivalent of OpenClaw's WebSocket. Closest match. | No central gateway — files are the "bus." Weakest match. | DB is a passive gateway. No active push capability. |

---

## Failure Mode Analysis

| Failure Mode | A: UDS | B: fsnotify | C: SQLite+WAL |
|-------------|--------|-------------|----------------|
| **Manager crashes** | Workers detect broken connection immediately. Pause, retry connect. No orphan state. | Workers continue writing files. Manager restarts, re-scans. May miss events during downtime (fsnotify not retroactive). | Workers continue writing rows. Manager restarts, queries from last known position. Nothing lost. |
| **Worker crashes** | Manager detects broken connection immediately. Reassigns bead. No cleanup needed. | Manager must detect missing heartbeat file updates. Delayed detection. | Manager must detect missing heartbeat rows. Delayed detection. |
| **Concurrent writes** | Impossible — each connection is independent. Manager serializes in event loop. | Possible — two workers write to same directory simultaneously. OS handles, but fsnotify may coalesce or miss events. | Impossible — SQLite serializes all writes. WAL allows concurrent reads. |
| **Event ordering** | Guaranteed per-connection. Manager processes in arrival order. | Not guaranteed — filesystem timestamps have second granularity, fsnotify event order can vary. | Guaranteed — monotonic rowid. |
| **Partial writes** | Impossible — messages are atomic (line-delimited, flushed). | Possible — JSON file write interrupted by crash = corrupt file. | Impossible — transactions are atomic. |

---

## Verdict Summary

| Dimension | Winner | Why |
|-----------|--------|-----|
| **True push** | A: UDS | Only option with genuine bidirectional push. B is push-ish. C requires polling or hooks. |
| **Crash safety** | C: SQLite | Transactions + WAL = strongest durability guarantee. A loses in-flight messages. B can corrupt files. |
| **No zombie detection** | A: UDS | Connection = liveness. Disconnect = immediate detection. B and C require heartbeat timeout heuristics. |
| **No race conditions** | Tie: A, C | A serializes in event loop. C serializes via transactions. B has filesystem races. |
| **Queryability / debugging** | C: SQLite | `SELECT * FROM events WHERE type='DONE'` beats grepping files or replaying sockets. |
| **Simplicity** | B: fsnotify | No server code. No schema. Just files. But: inherits BCR's fragility. |
| **Closest to references** | A: UDS | Mirrors OpenClaw's gateway (WebSocket→UDS) and BCR's exhaust pipe (pipe→socket). |
| **Operational visibility** | C: SQLite | DB is always inspectable. Socket state is transient. Files accumulate and need cleanup. |

---

## Recommendation

**Hybrid: UDS for real-time coordination + SQLite for durable state.**

### Design Decisions (from premortem)

1. **Workers are Go binaries that spawn `claude -p`** — the Go binary owns the UDS connection and manages Claude Code as a subprocess. This resolves the runtime mismatch (Claude Code is not a daemon, but the Go wrapper is).
2. **Workers do NOT need tmux panes** — they run as background processes supervised by the Manager. Only Architect (user-facing) and Manager (monitoring) need tmux visibility.
3. **UDS is a must-have** — CLI-mediated or polling alternatives rejected.

### Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                        Tmux Session                           │
│                                                              │
│  ┌──────────────────┐         ┌──────────────────────────┐   │
│  │  Architect Pane   │         │     Manager Pane          │   │
│  │  (interactive)    │   UDS   │     (daemon + dashboard)  │   │
│  │  claude session   │────────▶│     Go binary             │   │
│  │  user talks here  │         │     UDS server            │   │
│  └──────────────────┘         │     SQLite writer         │   │
│                                │     worker supervisor     │   │
│                                └───────────┬──────────────┘   │
│                                            │                  │
└────────────────────────────────────────────┼──────────────────┘
                                             │ UDS (bidirectional)
                      ┌──────────────────────┼──────────────────────┐
                      │                      │                      │
                      ▼                      ▼                      ▼
              ┌──────────────┐      ┌──────────────┐      ┌──────────────┐
              │  Worker (Go) │      │  Worker (Go) │      │  Worker (Go) │
              │  UDS client  │      │  UDS client  │      │  UDS client  │
              │  spawns:     │      │  spawns:     │      │  spawns:     │
              │  claude -p   │      │  claude -p   │      │  claude -p   │
              │  in worktree │      │  in worktree │      │  in worktree │
              └──────────────┘      └──────────────┘      └──────────────┘
                (background)          (background)          (background)
```

**Worker Go binary responsibilities:**
- Connects to Manager UDS on startup
- Receives ASSIGN with bead ID + worktree path
- Spawns `claude -p "<bead prompt>"` in the worktree as a subprocess
- Monitors Claude's stdout/stderr for progress signals
- Sends HEARTBEAT, STATUS, HANDOFF messages to Manager over UDS
- On ralph handoff: kills Claude subprocess, sends HANDOFF to Manager, Manager respawns a fresh Worker in same worktree
- On completion: sends DONE, Manager handles merge
- On crash: UDS connection drops, Manager detects immediately, reassigns bead

**Why this works:**

| Concern | Solution |
|---------|----------|
| O1: Push-based | UDS provides true bidirectional push |
| O6: Crash-safe | SQLite persists all events; UDS reconnects on Manager restart |
| O7: No zombies | Manager supervises Worker processes directly. UDS disconnect = dead. Manager kills Claude subprocess via Worker's process group. |
| O8: No races | Manager event loop serializes; SQLite transactions for persistence |
| O9: Ralph loop | Worker Go binary detects context % from Claude output, triggers handoff autonomously |
| O10: Three roles | Architect = interactive Claude. Manager = Go daemon. Worker = Go wrapper around `claude -p`. |
| Debugging | `sqlite3 .oro/state.db "SELECT * FROM events"` |
| BCR exhaust pipe | UDS replaces named pipes — same push model, no buffer limits, bidirectional |

**What each layer does:**
- **UDS**: Real-time signals (assign, done, heartbeat, shutdown). Volatile. Reconnectable.
- **SQLite**: Durable state (bead status, event log, handoff context, assignments). Queryable. Crash-proof.
- **Beads DB**: Work definitions (what to do). Already exists via `bd`.
- **Git worktrees**: Filesystem isolation (where to do it). Already proven in Compound + BCR.

---

## Premortem Findings

### Tigers (addressed by design decisions above)

| # | Risk | Severity | Resolution |
|---|------|----------|------------|
| 1 | Claude Code is not a daemon — runtime mismatch | HIGH | Workers are Go binaries that spawn `claude -p`. Go binary owns UDS. |
| 2 | Workers can't hold UDS connections | HIGH | Go wrapper holds the connection, Claude is a managed subprocess. |
| 3 | Merge serialization undesigned | HIGH | **Resolved as R1.** Worker stays alive through merge. Retry-first, escalate-last. |
| 4 | Manager SPOF recovery unclear | MEDIUM | **Resolved as R2.** Heartbeat + SHUTDOWN-before-reassign. No special restart mode. |

### Elephants (acknowledged, watch for)

| # | Risk | Notes |
|---|------|-------|
| 5 | Complexity budget similar to BCR | Fewer moving parts (no filesystem markers, no PID files, no named pipes). But Go binary per worker is new. |
| 6 | Tmux management is stateful | Reduced: only 2 panes (Architect + Manager), not N panes. |
| 7 | Two databases can drift (beads DB + SQLite) | Manager is single writer for SQLite. Beads DB is source of truth for work definitions. SQLite is source of truth for runtime state. Clear ownership. |

### Paper Tigers (fine)

| # | Risk | Why fine |
|---|------|---------|
| 8 | Stale socket file | Go unlink-before-listen. Standard pattern. |
| 9 | SQLite write contention | Manager is single writer. Eliminated by design. |

---

## Decided (from brainstorming session)

### D1: Architect signals Manager via UDS
Architect runs `bd create` then `oro enqueue beads-xyz`. The `oro enqueue` CLI sends `{type: "NEW_WORK", bead_id: "..."}` to Manager over UDS. This is a *nudge* — Manager also proactively pulls work.

### D2: Manager has a proactive priority queue
Manager doesn't just wait for Architect pushes. It continuously pulls work by priority:
```
P0: bugs, test failures      → immediate, preempt current work
P1: merge conflicts           → next, unblocks pipeline
P2: current epic beads        → steady state, pull when worker slots open
P3: other ready beads         → only if epic is drained
```

### D3: 5 concurrent workers, internally parallel
- Manager runs up to 5 Worker processes (Go binaries spawning `claude -p`)
- Each Worker can use Claude Code's Task tool to spawn subagents internally
- Hierarchy: Manager → 5 Workers → N subagents per worker

---

## Resolved Design Questions

### R1: Merge protocol (was Q1)

**Decision:** Worker stays alive through merge. Retry-first, escalate-last.

**Flow:**
1. Worker signals DONE (with merge context annotations on the bead)
2. Manager acquires merge lock
3. Manager runs `git rebase main` in worker's worktree
4. **Clean?** → `git checkout main && git merge --ff-only` → release lock → recycle worker
5. **Conflict?** → `git rebase --abort` → release lock → tell worker to rebase onto new main
   - Auto-resolves? (most cases — import order, adjacent lines) → re-enter merge queue
   - True conflict? → worker resolves using its own context + conflicting bead's merge annotations (`bd show <other-bead>`)
   - Tests fail after resolution? → escalate to Architect via UDS notification

**Merge context lives on the bead, not in SQLite.** When a worker completes, it annotates the bead with: intent, files changed, invariants, key decisions, commit SHA. This keeps SQLite focused on runtime coordination while beads own work artifacts. Any future resolver reads both beads to understand both sides.

**Why not an external merge agent:** The original worker has the most context about its own changes. An external agent pays full cold-start cost to do a worse job. If the worker ralph'd, the fresh worker inherits handoff YAML + can read both beads' annotations — still better than a blind agent.

**Why not block+notify Architect:** Pipeline stalls on human availability. Architect becomes a merge-conflict bottleneck with 5 workers. Only escalate when tests fail (semantic conflict), not for mechanical conflicts.

---

### R2: Manager restart / worker reconnection (was Q2)

**Decision:** No special restart mode. Same heartbeat mechanism handles both normal operation and recovery. SHUTDOWN-before-reassign prevents dual workers.

**Worker behavior on disconnect:**
- Detect broken UDS connection → continue working (don't kill `claude -p`)
- Buffer events (DONE, HEARTBEAT, HANDOFF) that can't be sent
- Retry UDS connection every 2s with jitter, capped at 5s. No timeout — retry indefinitely.
- On reconnect: send RECONNECT with current state + flush buffered events

**RECONNECT message:**
```json
{"type": "RECONNECT", "worker_id": "worker-3", "bead_id": "beads-abc",
 "state": "in_progress", "context_pct": 45,
 "buffered_events": [{"type": "HEARTBEAT", "ts": "...", "context_pct": 40}]}
```

**Manager startup:** Read SQLite → start UDS server (same socket path) → accept connections normally. No special reconciliation window.

**Reconciliation (Manager matches RECONNECT against SQLite):**
| SQLite says | Worker says | Action |
|------------|------------|--------|
| Bead assigned to this worker | in_progress | Resume |
| Bead assigned to this worker | done | Enter merge queue |
| Bead reassigned to another worker | anything | SHUTDOWN worker |
| Bead closed/merged | anything | SHUTDOWN worker |

**Dead worker detection:** Heartbeat timeout (3N seconds, same as normal operation). On timeout:
1. Send SHUTDOWN on old UDS connection
2. If connection alive (false positive) → worker receives SHUTDOWN, exits cleanly
3. If connection broken (truly dead) → fails silently
4. **Only then** reassign bead to new worker

**Why SHUTDOWN-before-reassign:** Eliminates dual-worker tiger entirely. You can't have two workers on the same bead because the old one is explicitly told to stop before the new one starts. No race condition — it's sequential.

**Idempotent reconciliation:** Manager reconciliation is pure-read from SQLite + accept connections. No writes until reconciliation is validated. Safe to crash and re-run.

**Premortem findings (accepted risks):**
- Elephant: Heartbeat timeout calibration is a tuning problem (start at N=15s, 3N=45s, adjust empirically)
- Elephant: Workers retry indefinitely if Manager is permanently dead (acceptable — operator will notice and kill manually)
- Paper tiger: Stale socket path (standard unlink-before-listen)
- Paper tiger: SQLite stale by one event (idempotent event processing handles replay)

---

### R3: Ralph handoff via UDS (was Q3)

**Decision:** Dual-path — worktree file for speed, bead annotation for durability. UDS carries only the signal, not the payload.

**Flow:**
1. Go wrapper detects `context_pct > threshold` (see Q4)
2. Go wrapper injects prompt asking Claude to write `.oro/handoff.yaml` in worktree
3. Claude writes handoff YAML (what's done, what remains, key decisions, files touched)
4. Go wrapper validates file exists and is non-empty. If Claude failed, writes minimal fallback: `{bead_id, files_modified (git diff), last_action: "context_limit_reached"}`
5. Go wrapper annotates bead: `bd update <id> --notes="handoff: <summary>"` (permanent record)
6. Go wrapper sends lightweight UDS signal: `{"type": "HANDOFF", "bead_id": "beads-abc", "worker_id": "worker-3"}`
7. Manager sends SHUTDOWN to old worker (reuses R2 pattern)
8. Manager spawns fresh Worker in same worktree
9. Fresh worker reads `.oro/handoff.yaml` + `bd show <id>` for full context
10. Fresh worker deletes `.oro/handoff.yaml` at startup (prevents stale reads on future ralphs)

**Why dual-path:**
- **Worktree file:** Operational. Fresh worker reads it immediately. No serialization over UDS. Co-located with the code.
- **Bead annotation:** Permanent record. Manager can inspect handoff quality. Survives worktree cleanup. Queryable via `bd show`.
- **UDS:** Just a signal. Stays lightweight. No large payloads.

**Premortem findings (accepted risks):**
- Tiger (mitigated): Claude fails to write handoff → Go wrapper writes minimal fallback from git diff. Degraded but functional.
- Tiger (mitigated): Stale handoff file → fresh worker deletes at startup before beginning work.
- Elephant: Handoff quality depends on Claude's summarization ability. Bad handoff = fresh worker wastes cycles rediscovering context. Bead annotation lets Manager spot-check.

---

### R4: Context % detection (was Q4)

**Decision:** Claude Code hook writes context % to `.oro/context_pct` in the worktree. Go wrapper watches via fsnotify + 5s poll fallback.

**Flow:**
1. `PreToolUse` hook (already exists in repo at `817d978`) writes context % to `.oro/context_pct`
2. Go wrapper watches file via fsnotify, with 5s poll as fallback
3. When `context_pct > threshold` → Go wrapper triggers ralph handoff (R3 flow)

**Mitigations:**
- Hook not installed in worker session → Go wrapper verifies `.claude/hooks/` exists in worktree at startup
- Hook writes lag behind sudden jumps → accept as elephant; threshold set conservatively (e.g., 70% triggers handoff, leaving 30% buffer)
- fsnotify miss → 5s poll fallback catches it

---

### R5: Bead prompt construction (was Q5)

**Decision:** Go wrapper is a template engine. Assembles prompt from structured sources with a token budget.

**Prompt template:**
```
You are a Worker agent executing bead {bead_id}.

## Task
{bead.description}

## Acceptance Criteria
{bead.acceptance_criteria or "See task description"}

## Dependencies (completed)
{for dep in bead.dependencies: dep.title — dep.notes (merge context)}

## Worktree State
Branch: {branch_name}
Modified files: {git status --short}

## Handoff Context (if ralph continuation)
{contents of .oro/handoff.yaml, or "Fresh start — no prior context"}

## Rules
- Work in this worktree only
- Run tests before signaling DONE
- When context gets high, write .oro/handoff.yaml and stop
- Commit your work before stopping (clean worktree always)
```

**Token budget enforcement:** If assembled prompt exceeds 4K tokens, truncate lowest-priority sections first: dependency notes → git diff → handoff context. Never truncate task description or acceptance criteria. Log truncation.

**Note:** No surviving BCR prompt code to reference. BCR used `bcr agent <task-id>` but its source was external to this repo. This is a clean-sheet design informed by BCR's failure modes.

**Premortem findings (accepted risks):**
- Tiger (mitigated): Prompt too large → token budget with priority-based truncation
- Elephant: Stale dependency context → accepted; dependency notes are best-effort enrichment, not critical
- Paper tiger: Template rigidity → Claude adapts; core structure works across bead types

---

### R6: Two-stage review (was Q6)

**Decision:** Two-stage review following Superpowers pattern. Self-review first (tests + acceptance criteria), then Manager dispatches spec-reviewer in same worktree.

**Flow:**
1. Worker self-reviews: runs tests, checks own work against acceptance criteria
2. Worker signals READY_FOR_REVIEW (not DONE)
3. Manager dispatches spec-reviewer worker in same worktree
4. Reviewer reads `bd show <bead>` (acceptance criteria) + `git diff` (changes) — does NOT need implementer's full context
5. Reviewer signals APPROVED → Worker signals DONE → enters merge queue
6. Reviewer signals REJECTED with feedback → Manager sends feedback to original worker → worker fixes → back to step 1

**Why two-stage (informed by reference implementations):**
- Superpowers mandates: self-review → spec-compliance review → code-quality review. Two reviewers per task.
- Compound uses 13+ parallel review agents synthesized into consolidated findings.
- Key insight: reviewer doesn't need implementation context — it needs the **spec** (acceptance criteria on the bead). This avoids the "context-blind external agent" problem from Q1.

**Simplification from Superpowers:** Combine spec-compliance and code-quality into a single reviewer pass. Superpowers separates them because it has abundant subagent capacity. Oro has 5 worker slots — a second review pass is expensive. One reviewer checking both spec compliance and code quality is sufficient.

**Premortem findings (accepted risks):**
- Tiger (mitigated): Vague acceptance criteria → rubber-stamp review. Mitigated by enforcing testable acceptance criteria at bead creation (spec-to-beads skill).
- Elephant: Review worker consumes a worker slot (~20% throughput reduction). Accepted — correctness over speed.
- Elephant: Review loops (reject → fix → re-review) can cycle if acceptance criteria are ambiguous. Cap at 2 review cycles, then escalate to Architect.

---

## All Design Questions Resolved

All 6 open questions from the original design have been answered:
- R1: Merge protocol (worker stays alive, retry-first, bead merge context)
- R2: Reconnection (heartbeat + SHUTDOWN-before-reassign)
- R3: Ralph handoff (worktree file + bead annotation, lightweight UDS signal)
- R4: Context detection (Claude Code hook writes to file, Go wrapper watches)
- R5: Bead prompt construction (Go wrapper as template engine, token budget)
- R6: Two-stage review (self-review + spec-reviewer, Superpowers pattern)

**Next:** Close oro-1s7. This unblocks: oro-w6n (Manager), oro-773 (Worker), oro-r7b (CLI), oro-68t (merge coordinator).
