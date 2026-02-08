# Oro Architecture Spec

**Date:** 2026-02-08
**Status:** Draft — approved decisions, remaining open questions below

**Reference:** [gastown](https://github.com/steveyegge/gastown) — similar multi-agent orchestration (Mayor + Polecats). Oro differs in using UDS for worker communication (push-based, supports 50-worker swarms) vs gastown's CLI+beads polling approach.

## Roles

### Architect (interactive Claude session, tmux pane 0)

- **Human's primary interface** to the system
- Helps the human understand the codebase, create specs, shape work
- **Only output: beads.** Does not write code. Ever.
- Can spawn Claude subagents for research/analysis (never coding)
- Communicates priority to the manager by creating beads
- Runs as bare `claude` with project CLAUDE.md — no special role prompt needed

### Manager (interactive Claude session, tmux pane 1)

- **Gets stuff done** via the dispatcher and workers
- Does not write code except as absolute last resort (broken merges)
- Creates beads (decomposition, new work discovered)
- **Decides** swarm size — sends `oro scale N` to dispatcher
- Sends directives via `oro` CLI commands (which connect to dispatcher UDS)
- Can spawn Claude subagents for non-coding tasks
- Two input sources: human (interactive) and dispatcher (tmux send-keys escalations)
- Launched as interactive `claude` with project CLAUDE.md (same as architect, different role context)

### Workers (`oro-worker` Go binary + `claude -p` subprocess, isolated worktrees)

- Execute beads — this is where code gets written
- Work in isolated git worktrees (created by dispatcher)
- Create beads when:
  - Merge fails → P0 bead
  - Bead too big → decompose into smaller beads
  - Context limit hit → decompose remaining work into new beads
- Can spawn Claude subagents for coding subtasks
- Maintain persistent UDS connection to dispatcher (heartbeat, status, done, handoff)
- **Spawned by dispatcher** — worker process is `oro-worker --socket=<path> --id=<worker-id>`

### Dispatcher (Go binary, background daemon)

- **Purely mechanical** — no judgment, no AI
- **Manages worker swarm** — spawns/kills `oro-worker` processes to match scale target
- Manages worker lifecycle (UDS connections, heartbeats, crash detection)
- Assigns beads to idle workers (reacts to bead changes via fsnotify)
- Creates/removes git worktrees
- Executes merges (rebase + ff-only onto main)
- Spawns ops agents (review, merge conflict resolution, diagnosis)
- Escalates to manager via tmux send-keys
- Processes directives from manager via UDS (same socket as workers)
- Starts with swarm size 0 — **inert** until manager sends `start` + `scale N`

### Ops Agents (short-lived `claude -p` processes)

- Spawned by dispatcher for specific tasks
- Review, merge conflict resolution, stuck-worker diagnosis
- Disposable — run task, report result, exit

## Communication

### Universal language: Beads

Everyone reads and writes beads. Beads are the primary coordination mechanism.

- Architect → creates beads (human's intent)
- Manager → creates beads (decomposition, discovered work)
- Workers → create beads (failures, decomposition, context handoff)
- Dispatcher → reads beads (assignment), closes beads (completion)

### Two planes: control (UDS) and data (beads)

- **Control plane (UDS):** Directives, lifecycle, heartbeat, assignment — instant, push-based
- **Data plane (beads):** What work exists, priority, status, dependencies — durable, git-backed

### Push-based, not polling

| Channel | Mechanism | Direction |
|---------|-----------|-----------|
| Manager → Dispatcher | `oro` CLI → UDS (short-lived connection) | Push |
| Dispatcher → Workers | UDS messages (persistent connection) | Push |
| Workers → Dispatcher | UDS messages (persistent connection) | Push |
| Dispatcher → Manager | tmux send-keys | Push (escalations) |
| Bead changes → Dispatcher | fsnotify on `.beads/` | Push (triggers `bd ready` re-evaluation) |

Fallback: 60s safety-net poll of `bd ready` in case fsnotify misses an event.

### UDS Protocol

Single socket, line-delimited JSON. Manager and worker connections are discriminated by first message type.

**Manager connections (short-lived):**

```
→ {"type":"DIRECTIVE","directive":{"op":"scale","args":"5"}}
← {"type":"ACK","ack":{"ok":true,"detail":"target=5, spawning 3"}}
(disconnects)
```

**Worker connections (persistent):**

```
→ {"type":"HEARTBEAT","heartbeat":{"worker_id":"w-03","bead_id":"","context_pct":0}}
← {"type":"ASSIGN","assign":{"bead_id":"oro-abc","worktree":"/tmp/oro/w-03","model":"claude-opus-4-6"}}
... (heartbeats every 30s) ...
→ {"type":"DONE","done":{"bead_id":"oro-abc","worker_id":"w-03","quality_gate_passed":true}}
← {"type":"ASSIGN","assign":{...}}  // next bead, or SHUTDOWN if scaling down
```

### Directives (Manager → Dispatcher via `oro` CLI)

| CLI Command | Directive | Effect |
|-------------|-----------|--------|
| `oro start` | `start` | Inert → Running (begin assigning beads) |
| `oro stop` | `stop` | → Stopping (drain workers, finish merges, shutdown) |
| `oro pause` | `pause` | → Paused (workers continue current work, no new assignments) |
| `oro resume` | `resume` | Paused → Running |
| `oro scale N` | `scale` | Set target swarm size to N workers |
| `oro focus <epic>` | `focus` | Prioritize beads from specific epic |
| `oro status` | `status` | Query: returns state, worker count, queue depth, assignments |

SQLite `commands` table retained as **write-after-ACK audit log** only. Dispatcher no longer polls it.

### Message Types

**Dispatcher → Worker (3 types):**

| Type | When | Payload |
|------|------|---------|
| `ASSIGN` | Idle worker + ready bead | `{bead_id, worktree, model, memory_context}` |
| `PREPARE_SHUTDOWN` | Scale down or session end | `{timeout}` |
| `SHUTDOWN` | Grace period expired or after SHUTDOWN_APPROVED | (none) |

**Worker → Dispatcher (7 types):**

| Type | When | Payload |
|------|------|---------|
| `HEARTBEAT` | Every 30s | `{worker_id, bead_id, context_pct}` |
| `STATUS` | Phase change | `{worker_id, bead_id, state, result}` |
| `DONE` | Bead complete | `{worker_id, bead_id, quality_gate_passed}` |
| `HANDOFF` | Context limit hit | `{worker_id, bead_id, learnings, decisions, files_modified}` |
| `READY_FOR_REVIEW` | Pre-merge review | `{worker_id, bead_id}` |
| `RECONNECT` | After disconnect | `{worker_id, bead_id, state, context_pct, buffered_events}` |
| `SHUTDOWN_APPROVED` | After saving context | `{worker_id}` |

### Escalations (Dispatcher → Manager via tmux send-keys)

| Escalation | Trigger |
|------------|---------|
| Semantic merge conflict | Tests fail after ops agent resolution |
| Stuck worker | Bead fails after 2 context cycles |
| Priority contention | All slots busy + new P0 arrives |

## Worker Lifecycle

### Spawning (dispatcher manages)

1. Manager sends `oro scale 5`
2. Dispatcher compares target (5) vs current workers (2)
3. Dispatcher spawns 3 × `oro-worker --socket=/tmp/oro.sock --id=w-03`
4. Each `oro-worker` process connects to dispatcher UDS
5. Sends initial HEARTBEAT (state: idle)
6. Dispatcher assigns beads as they become available

### Assignment

1. Dispatcher detects idle worker + ready bead (via fsnotify or fallback poll)
2. Dispatcher creates git worktree (`git worktree add`)
3. Dispatcher sends ASSIGN `{bead_id, worktree, model, memory_context}`
4. Worker reads bead details (`bd show`)
5. Worker assembles prompt (template + bead + memories)
6. Worker launches `claude -p '<prompt>'` in worktree
7. Worker sends HEARTBEAT every 30s with `context_pct`

### Completion

1. Worker sends DONE `{quality_gate_passed: true}`
2. Dispatcher merges worktree → main (rebase + ff-only)
3. Dispatcher removes worktree
4. Dispatcher closes bead (`bd close`)
5. Worker returns to idle → next ASSIGN

### Quality gate rejection

1. Worker sends DONE `{quality_gate_passed: false}`
2. Dispatcher re-sends ASSIGN for same bead+worktree (retry)

### Context exhaustion (handoff)

1. Worker hits context limit
2. Worker creates new beads for remaining work (`bd create`)
3. Worker sends HANDOFF `{learnings, decisions, files_modified}`
4. Dispatcher persists learnings to memory store
5. Dispatcher sends SHUTDOWN to old worker
6. Bead returns to ready pool with memory context for next assignment

### Crash recovery

1. Worker connection drops (EOF on UDS read)
2. Dispatcher logs dead worker, removes from tracking
3. Worktree preserved (not removed — may have uncommitted work)
4. Bead returns to ready pool for reassignment
5. If target swarm size not met, dispatcher spawns replacement worker

### Scale down

1. Manager sends `oro scale 3` (current: 5)
2. Dispatcher selects 2 workers to remove (idle first, then newest busy)
3. Sends PREPARE_SHUTDOWN to selected workers
4. Workers save context → SHUTDOWN_APPROVED → dispatcher kills process
5. On timeout: SIGTERM → 5s grace → SIGKILL (process group kill)

## Shutdown Cleanup

When `oro stop` or context cancellation occurs, dispatcher runs `shutdownCleanup()`:

1. **Cancel ops agents** — `ops.Active()` + `Cancel()` for each
2. **Abort in-flight merges** — `merger.Abort()` (fresh 5s background context)
3. **Drain workers** — PREPARE_SHUTDOWN → wait for SHUTDOWN_APPROVED (or timeout)
4. **Kill worker processes** — SIGTERM → grace period → SIGKILL (process group)
5. **Remove worktrees** — `git worktree remove` for each active worktree
6. **Sync beads** — `bd sync`
7. **Close UDS listener**

## Session Lifecycle

### Session Start

1. User runs `oro start`
2. Preflight checks: verify tmux, claude, bd, git are available
3. Bootstrap `~/.oro` directory if first run
4. Dispatcher Go binary launches (background, **inert**, swarm size 0)
5. Tmux session created: architect (pane 0) + manager (pane 1)
6. Architect: bare `claude` — human starts interacting
7. Manager: bare `claude` — reads CLAUDE.md for role context, starts autonomously
8. Manager sends `oro start` + `oro scale N` when ready

### Steady State

1. Dispatcher watches `.beads/` via fsnotify for new/changed work
2. On change: re-evaluates `bd ready` for ready beads
3. When bead becomes ready + idle worker available → create worktree, send ASSIGN
4. Worker executes bead (TDD, quality gate, commit to branch)
5. Worker signals DONE → dispatcher merges → dispatcher closes bead
6. On failure/conflict → ops agent or escalation to manager
7. Loop continues until `stop` directive

### Session End

1. User runs `oro stop` (or tells manager/architect to stop)
2. Manager sends `oro stop` directive
3. Dispatcher: graceful shutdown (see Shutdown Cleanup above)
4. `bd sync`, `git push`
5. Tmux session closed

## Resolved Questions

- [x] **Manager → Dispatcher communication:** UDS on same socket as workers. Short-lived connections, discriminated by DIRECTIVE message type. SQLite commands table kept as audit log only (no polling).
- [x] **How `scale N` works:** Dispatcher spawns `oro-worker` processes. Manager decides target, dispatcher reconciles (spawn/kill to match).
- [x] **Manager launch mode:** Interactive `claude` with CLAUDE.md (same as architect). Not `-p`.
- [x] **fsnotify target:** Watch `.beads/` directory. On change, re-run `bd ready`. 60s fallback poll as safety net.
- [x] **SQLite commands table:** Kept as write-after-ACK audit log. Polling removed.
- [x] **Worker spawning:** Dispatcher spawns worker processes (not manager).
- [x] **Architect/Manager same directory:** Safe. Both run in the same repo. Beads uses SQLite WAL + `BEGIN IMMEDIATE` + 30s busy timeout for concurrent writes. With beads daemon, writes are serialized via RPC. No separate worktree needed.
- [x] **Role differentiation:** Initial message via tmux send-keys (gastown's beacon pattern). Go binary assembles role-specific prompt and sends it as the first message after `claude` starts. Both sessions share the same CLAUDE.md.
- [x] **Hooks must be role-aware:** `oro start` sets `ORO_ROLE=architect|manager` env var before launching each `claude` session. Session start hooks (session_start_extras.py, enforce-skills.sh) check `$ORO_ROLE` and filter injected context. Architect doesn't need TDD/commit protocol. Manager doesn't need coding skills. Workers get their own prompt via `claude -p` (no hooks needed).

## Worker Prompt Template

Workers run as `claude -p` which gets NO CLAUDE.md, no `.claude/rules/`, no skills, no hooks.
The prompt is the **only** context. `oro-worker` Go binary assembles it from these sections:

```
 1. Role          — "You are an oro worker. You execute one bead at a time."
 2. Bead          — title, description, acceptance criteria (from `bd show <id>`)
 3. Memory        — learnings/decisions from prior sessions on this bead (from dispatcher)
 4. Coding rules  — inlined from project standards:
                    - Functional first: pure functions, immutability, early returns
                    - Pure core (business logic), impure edges (I/O, CLI)
                    - Go: gofumpt, golangci-lint, go-arch-lint
                    - Python: PEP 8, ruff, pyright, pytest fixtures > classes
 5. TDD           — "Write tests FIRST. Red-green-refactor. Every feature/fix needs a test."
 6. Quality gate  — concrete command: `./quality_gate.sh` (or per-language equivalent)
 7. Worktree      — "You are in <path>. Commit to branch agent/<bead-id>."
 8. Git           — conventional commits (`feat(scope): msg`), no amend, new commits only
 9. Beads tools   — `bd create` (decompose), `bd close` (done), `bd dep add` (blockers)
10. Constraints   — no git push, no files outside worktree, no modifying main
11. Failure       — 3 failed test attempts → create P0 bead, exit
                    Bead too big → decompose with bd create, exit
                    Context limit → create handoff beads with remaining work, exit
                    Blocked → create blocker bead, exit
12. Exit          — "When acceptance criteria pass and quality gate is green, exit."
```

Note: `ORO_ROLE=worker` env var set for any hooks that fire during tool use.

## Open Questions

- [ ] Manager initial message: what should the role-specific beacon contain? (responsibilities, available `oro` CLI commands, behavioral guidelines)
- [ ] fsnotify specifics: watch entire `.beads/` dir? Or just `beads.db` (SQLite WAL changes)?
