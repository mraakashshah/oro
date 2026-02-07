# Oro Orchestrator: Manager Redesign

> Supersedes the architecture section of `2026-02-07-orchestrator-ipc-comparison.md`.
> Protocol-level decisions (R1-R6) from that doc remain valid — this doc reframes *who* owns each responsibility.

## Design Context

The original IPC comparison assumed Manager = Go binary running a UDS server. The actual intent: Manager is a Claude session that makes strategic decisions, not a daemon that handles plumbing. This redesign introduces the **Dispatcher** (Go binary) to handle mechanical coordination, with the Manager Claude session as an escalation-only decision-maker.

## Roles

| Role | Runtime | Purpose | Lifecycle |
|------|---------|---------|-----------|
| **Architect** | Claude (interactive, tmux pane) | Design, specs, bead creation | User-driven. Starts/stops with the user. |
| **Manager** | Claude (escalation-driven, tmux pane) | Judgment calls: merge conflicts, stuck workers, priority tradeoffs | Launched at session start. Receives nudges from Dispatcher via `tmux send-keys`. |
| **Dispatcher** | Go binary (autonomous, background) | All mechanical coordination: assignment, heartbeat, crash recovery, merge execution, worker lifecycle, ops agent spawning | Launched at session start. Inert until Manager issues `start` directive. |
| **Workers** | Go binary + `claude -p` (background) | Execute beads in isolated worktrees | Spawned by Dispatcher. Ralph-cycle until bead complete or escalation needed. |
| **Ops Agents** | `claude -p` (short-lived, background) | Merge resolution, code review, crash analysis | Spawned by Dispatcher for specific operational tasks. Exit on completion. |

## Architecture

```
Architect (Claude, interactive, tmux pane)
    │
    │ bd create / bd dep add
    ▼
┌─────────────────────────────────────────────────────┐
│                    Beads DB                          │
│         (source of truth for work definitions)       │
└──────────────────────┬──────────────────────────────┘
                       │ watches for ready work
                       ▼
┌─────────────────────────────────────────────────────┐
│              Dispatcher (Go binary)                  │
│                                                     │
│  ┌─────────────┐  ┌──────────────┐  ┌───────────┐  │
│  │ Priority Q   │  │ Worker Supv.  │  │ Merge Exec │  │
│  │ bd ready →   │  │ UDS server    │  │ rebase+FF  │  │
│  │ assign slots │  │ heartbeat mon │  │ lock mgmt  │  │
│  └─────────────┘  └──────────────┘  └───────────┘  │
│                                                     │
│  ┌──────────────────────────────────────────────┐   │
│  │ Ops Agent Spawner                             │   │
│  │ claude -p for: review, merge conflict, diag   │   │
│  │ Model routing: Opus for judgment, Sonnet mech │   │
│  └──────────────────────────────────────────────┘   │
│                                                     │
│  SQLite (runtime state, commands, events)            │
└──────┬──────────────┬───────────────────────────────┘
       │              │
       │ UDS          │ tmux send-keys (escalations only)
       ▼              ▼
   ┌────────┐    ┌──────────────────────────────────┐
   │Workers │    │  Manager (Claude, tmux pane)      │
   │(1..N)  │    │                                   │
   │Go+claude│    │  Receives: escalation nudges      │
   │  -p    │    │  Responds: oro commands → SQLite   │
   └────────┘    │  Directives: start/stop/pause      │
                 └──────────────────────────────────┘
```

## Communication Channels

| From → To | Mechanism | Content |
|-----------|-----------|---------|
| Architect → Beads DB | `bd create`, `bd dep add` | Work definitions, dependencies |
| Dispatcher → Beads DB | `bd ready`, `bd show`, `bd close` | Read ready work, close completed |
| Dispatcher → Workers | UDS (bidirectional) | ASSIGN, SHUTDOWN, heartbeat |
| Workers → Dispatcher | UDS (bidirectional) | STATUS, DONE, HANDOFF, HEARTBEAT |
| Dispatcher → Manager | `tmux send-keys` | Escalation messages (structured text) |
| Manager → Dispatcher | `oro` CLI → SQLite | Directives (start/stop/pause), decisions |
| Dispatcher → SQLite | Go (long-lived connection) | Runtime state, event log |
| Manager → SQLite | `oro` CLI (per-invocation) | Commands, directive responses |
| Dispatcher → Ops Agents | Spawns `claude -p` | Merge context, review prompts |

## Manager Directives

The Manager issues directives to the Dispatcher via `oro` CLI commands that write to SQLite. The Dispatcher watches for new directives.

| Directive | Meaning |
|-----------|---------|
| `start` | Begin pulling and assigning ready work |
| `stop` | Finish current work, don't assign new beads |
| `pause` | Hold new assignments, workers keep running |
| `focus <epic>` | Prioritize beads from this epic |

The Dispatcher is **inert until it receives `start`**. After that, it runs autonomously.

## Dispatcher Autonomy Boundary

### Dispatcher handles (no Manager involvement)

- Assigning ready beads to free worker slots (priority queue: P0 → P1 → P2 → P3)
- Worker lifecycle: spawn, heartbeat monitoring, crash detection, restart
- Clean merges: rebase onto main, FF merge, lock management (per R1)
- Ralph handoffs: detect context %, trigger handoff, respawn fresh worker (per R3)
- Two-stage review: spawn reviewer ops agent in worker's worktree (per R6)
- Worker reconnection after Dispatcher restart (per R2)
- Model routing: Opus for judgment-heavy ops tasks, Sonnet for mechanical ones

### Dispatcher escalates to Manager

- Merge conflict where tests fail after resolution (semantic conflict — ops agent couldn't resolve)
- Bead stuck after 2 ralph cycles (worker can't make progress)
- All worker slots busy with active work and new P0 work arrives (priority tradeoff)

Escalation format (sent via `tmux send-keys`):

```
[ESCALATION] Merge conflict: beads-xyz tests failing after resolution.
Files: src/foo.go, src/bar.go
Both sides: beads-xyz (add auth middleware) vs beads-abc (refactor router)
Ops agent attempted resolution, tests still fail.
Action needed: review conflict and decide resolution, or deprioritize one bead.
```

The Manager responds with `oro` commands that write decisions to SQLite. The Dispatcher picks them up and acts.

## Worker Pool

- **Target:** 5 concurrent slots (soft limit, not hard)
- **Shared pool:** Workers and ops agents draw from the same pool
- **Model routing:** Dispatcher selects model per task:
  - Opus 4.6: complex merges, architectural review, stuck-bead diagnosis
  - Latest Sonnet: straightforward bead execution, clean reviews, mechanical tasks

When a worker completes a bead and needs review, its slot stays occupied by the reviewer ops agent. No net increase in concurrent sessions.

## What Changed from Original Design

| Aspect | Original (IPC doc) | New |
|--------|-------------------|-----|
| Manager runtime | Go binary (UDS server) | Claude session (tmux pane, escalation-only) |
| Who runs UDS server | Manager | Dispatcher |
| Who assigns work | Manager | Dispatcher (autonomously after `start`) |
| Who handles merges | Manager | Dispatcher (+ ops agents for conflicts) |
| Who supervises workers | Manager | Dispatcher |
| New role | — | Dispatcher (Go binary, mechanical coordinator) |
| New capability | — | Ops agents (short-lived `claude -p` for operational tasks) |
| Manager ↔ Workers | Direct UDS | No direct channel. Dispatcher mediates. |
| Architect ↔ Manager | UDS + `oro enqueue` | No direct channel. Beads DB is the interface. |
| `oro enqueue` command | Required (nudge Manager) | Removed. Dispatcher watches beads DB directly. |

## Unchanged from Original Design

The protocol-level decisions from the IPC comparison doc remain valid:

- **R1: Merge protocol** — Worker stays alive through merge. Retry-first, escalate-last. Merge context on bead annotations. (Now: Dispatcher executes, ops agent resolves conflicts, Manager is final escalation.)
- **R2: Reconnection** — Heartbeat + SHUTDOWN-before-reassign. (Now: Dispatcher owns this entirely.)
- **R3: Ralph handoff** — Worktree file + bead annotation, lightweight UDS signal. (Unchanged — Worker ↔ Dispatcher over UDS.)
- **R4: Context % detection** — Claude Code hook writes to file, Go wrapper watches. (Unchanged — Worker Go binary handles.)
- **R5: Bead prompt construction** — Go wrapper as template engine with token budget. (Unchanged — Worker Go binary handles.)
- **R6: Two-stage review** — Self-review + spec-reviewer. (Now: Dispatcher spawns reviewer ops agent instead of Manager dispatching.)

## Premortem

### Tigers (mitigated)

| # | Risk | Severity | Mitigation |
|---|------|----------|------------|
| 1 | Manager misses time-sensitive events (Claude sessions aren't event loops) | HIGH | Dispatcher handles all time-critical operations autonomously. Manager only gets non-urgent escalations. Latency of minutes is acceptable for escalation-class decisions. |
| 2 | Two-writer SQLite contention (CLI + Go long-lived connection) | MEDIUM | WAL mode + busy timeout + retry. Low write volume (dozens/minute). CLI opens/closes quickly. |
| 3 | tmux send-keys messages pile up before Manager processes them | MEDIUM | Trust the Manager to read and prioritize. Dispatcher batches related events into single messages. Messages are structured text, not commands — Manager decides what to act on. |

### Elephants (acknowledged)

| # | Risk | Notes |
|---|------|-------|
| 4 | Manager can't be truly reactive — near-real-time, not real-time | Accepted. All real-time work lives in the Dispatcher. Manager decisions (merge escalations, stuck beads) tolerate minutes of latency. |
| 5 | Dispatcher complexity approaches BCR territory | Fewer moving parts than BCR (no filesystem markers, no PID files, no named pipes). But it's still a substantial Go binary. Mitigate with good test coverage and clear state machine. |

### Paper Tigers

| # | Risk | Why fine |
|---|------|---------|
| 6 | 5+ concurrent Claude sessions overload API | Soft pool limit. Model routing (Sonnet for mechanical tasks) reduces cost. Ops agents are short-lived. |
| 7 | No direct Architect ↔ Manager channel | They don't need one. Beads DB is the interface. If the Architect needs to escalate, they create a P0 bead. |
