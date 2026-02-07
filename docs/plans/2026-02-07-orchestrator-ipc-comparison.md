# Oro Agent Orchestrator: IPC & Coordination Comparison

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
| 3 | Merge serialization undesigned | HIGH | **Still open.** Manager must hold merge lock + sequential rebase-merge. |
| 4 | Manager SPOF recovery unclear | MEDIUM | **Still open.** Manager restarts, re-reads SQLite, workers reconnect with current state. Need reconnection protocol. |

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

## Remaining Open Questions

1. **Merge protocol** — Manager holds merge lock, sequential rebase-merge. What happens on conflict? Create P0 bead or block and notify Architect?
2. **Manager restart / worker reconnection** — Workers reconnect with `{type: "RECONNECT", bead_id: "...", state: "in_progress"}`. Manager re-reads SQLite to reconcile. Need protocol spec.
3. **Ralph handoff via UDS** — Worker sends HANDOFF with YAML context → Manager persists to SQLite → Manager spawns fresh Worker in same worktree → new Worker reads handoff from SQLite and constructs prompt for `claude -p`. Need message schema.
4. **How does Worker detect context % from Claude's output?** Parse `claude -p` stdout? Use Claude Code hooks that write to a file the Go wrapper watches?
5. **Bead prompt construction** — How does the Worker Go binary construct the `claude -p` prompt from bead metadata? What context gets included (bead description, dependencies, worktree state, handoff YAML)?
6. **Two-stage review** — Do workers self-review (Superpowers pattern), or does Manager dispatch a separate review worker?
