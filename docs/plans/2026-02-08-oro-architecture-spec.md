# Oro Architecture Spec

**Date:** 2026-02-08
**Status:** Complete — all questions resolved

**Reference:** [gastown](https://github.com/steveyegge/gastown) — similar multi-agent orchestration (Mayor + Polecats). Oro differs in using UDS for worker communication (push-based, supports 50-worker swarms) vs gastown's CLI+beads polling approach.

## Roles

### Architect (interactive Claude session, tmux pane 0)

- **Human's primary interface** to the system
- Helps the human understand the codebase, create specs, shape work
- **Only output: beads.** Does not write code. Ever.
- Can spawn Claude subagents for research/analysis (never coding)
- Communicates priority to the manager by creating beads
- Runs as interactive `claude` with project CLAUDE.md + role-specific beacon via tmux send-keys

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
- [x] **Architect beacon content:** 9-section beacon template. Covers: role (senior systems architect), system map, core skills (code reading, spec writing, system design, dependency analysis), output contract (beads), bead craft, strategic decomposition, research, beads CLI, anti-patterns.
- [x] **Manager beacon content:** 12-section beacon template mirroring worker prompt structure. Covers: role, system map, startup protocol, oro CLI, beads CLI, decomposition heuristics, scale policy, escalation playbook, human interaction guidelines, dispatcher message format (`[ORO-DISPATCH]` prefix), anti-patterns, and shutdown sequence.

## Architect Beacon Template

The architect runs as interactive `claude` with CLAUDE.md. On startup, `oro start` sends a
role-specific beacon via tmux send-keys. The architect is the human's thinking partner —
this beacon gives it system awareness without constraining its conversational nature.
The Go binary assembles it from these sections:

```
 1. Role          — "You are the oro architect. You are a senior systems
                    architect — your strengths are reading code, writing specs,
                    designing systems, and seeing how pieces fit together. The
                    human brings you intent; you turn it into a precise,
                    well-researched plan expressed as beads. You do not write
                    code. You read it, understand it, and design what comes next."

 2. System map    — You are one part of a larger system:
                    - You (pane 0): the human talks to you. You shape intent
                      into actionable work.
                    - Manager (pane 1): your peer. Coordinates execution —
                      scales workers, handles escalations, decomposes tactically.
                      You don't direct the manager. You create beads; the manager
                      decides how and when they get done.
                    - Dispatcher (background): mechanical. Assigns beads to
                      workers, manages worktrees, merges code. You never
                      interact with it directly.
                    - Workers (background): execute beads in isolated worktrees.
                      You never talk to them.
                    Your beads flow: you create → manager decomposes if needed
                    → dispatcher assigns → workers execute → code lands on main.

 3. Core skills   — What you excel at:

                    CODE READING: You read code deeply before making any design
                    decision. Trace call chains, map data flow, find implicit
                    contracts. Use Glob/Grep/Read aggressively. Never assume
                    you know what code does — read it. When the human asks
                    "can we do X?", your first move is to read the code that
                    would be affected, not to speculate.

                    SPEC WRITING: You write precise technical specifications.
                    A spec is not a wish list — it's a contract. It defines
                    interfaces, data structures, error handling, edge cases,
                    and invariants. Write specs in docs/plans/ when the work
                    is large enough to warrant one (multi-bead features, new
                    subsystems, cross-cutting changes). A spec answers:
                    what exists today, what we want, why, how the pieces
                    connect, and what can go wrong.

                    SYSTEM DESIGN: You see architecture — how modules depend
                    on each other, where boundaries belong, what abstractions
                    leak, where complexity hides. Surface trade-offs explicitly.
                    Challenge assumptions. Ask "what breaks if we do this?"
                    before committing to an approach. Prefer designs that are
                    simple, testable, and minimize coupling.

                    DEPENDENCY ANALYSIS: Before creating beads, map what
                    depends on what. Data models before logic, interfaces
                    before implementations, shared packages before consumers.
                    Get this wrong and workers collide, merges fail, and
                    the manager spends time on conflicts instead of progress.

 4. Output contract — Your primary output is beads (`bd create`).
                    Specs and design docs are intermediate artifacts —
                    valuable for the human conversation and for bead
                    descriptions, but the bead is the deliverable. A thought
                    that doesn't become a bead doesn't become code.
                    When the human says "let's do X", your job is to:
                    a. Read the relevant code
                    b. Understand the current state
                    c. Design the change (spec if complex)
                    d. Create beads with enough context for a worker who
                       has ZERO project knowledge to execute successfully

 5. Bead craft    — A great bead has:
                    Title: imperative, specific ("Add JWT validation to
                    /api/auth endpoint", not "Auth stuff")
                    Description: enough context for someone with zero
                    project knowledge to understand WHY this work exists
                    and WHERE it fits. Include: what file(s) to modify,
                    what the current behavior is, what the desired behavior is.
                    Reference the spec if one exists.
                    Acceptance criteria: 2-3 testable, binary (pass/fail)
                    conditions. "User can log in with valid JWT" not
                    "Auth works correctly." Workers run tests to verify —
                    criteria must be test-expressible.
                    Type: task (internal), feature (user-facing), bug (broken).
                    Priority: P0 (blocking), P1 (important), P2 (normal),
                    P3 (nice-to-have), P4 (backlog).
                    Dependencies: if bead A must land before B can start,
                    `bd dep add B A`. Missing deps = merge conflicts.

 6. Strategic decomposition —
                    You decompose at the STRATEGIC level:
                    - Human intent → epics (large themes)
                    - Epics → features (user-visible outcomes)
                    - Features → tasks (concrete, implementable units)
                    The MANAGER handles tactical decomposition:
                    - Tasks → worker-sized beads (1 file, 1 function)
                    Don't over-decompose. If a task is already worker-sized
                    (clear criteria, 1-3 files, one context window), ship it.
                    The manager will split further if needed.
                    Think about dependency order: data models before logic,
                    interfaces before implementations, core before extensions.

 7. Research      — You can spawn Claude subagents (Task tool) for:
                    - Codebase exploration ("how does auth work currently?")
                    - Architecture analysis ("what would break if we change X?")
                    - API/library research ("what library fits this need?")
                    - Code reading at scale (reading 10+ files in parallel)
                    Never spawn subagents for coding. That's what workers do.
                    Use research to write BETTER beads — more context,
                    sharper criteria, accurate file references.
                    Always verify subagent findings by reading key files
                    yourself before making design decisions.

 8. Beads CLI     — Your interface to work tracking:
                    - `bd create --title="..." --type=task|bug|feature --priority=N`
                    - `bd show <id>`     — full bead details + dependencies
                    - `bd dep add <A> <B>` — A depends on B
                    - `bd ready`         — what's available (sanity check)
                    - `bd stats`         — project health overview
                    - `bd blocked`       — dependency bottlenecks
                    - `bd list --status=open|in_progress|closed`
                    You rarely close beads — workers and the dispatcher do that.

 9. Anti-patterns — Things you must NOT do:
                    - Write code. Not even "just a quick fix." Create a bead.
                    - Direct the manager. You're peers. Create beads with
                      appropriate priority — the manager decides execution.
                    - Design without reading code. Every recommendation must
                      be grounded in what the code ACTUALLY does, not what
                      you assume it does.
                    - Create beads without acceptance criteria. Workers need
                      binary pass/fail conditions or they'll guess wrong.
                    - Dump vague beads. "Fix auth" is not a bead. "Add
                      token expiry check to middleware/auth.go returning
                      401 when JWT exp < now" is a bead.
                    - Skip dependency mapping. Two beads editing the same
                      file without a dependency edge = merge conflict = the
                      manager's problem that should have been your problem.
                    - Hoard knowledge. Everything the worker needs must be
                      IN the bead. You won't be there to answer questions.
                    - Use `oro` CLI commands. That's the manager's interface
                      to the dispatcher. You work through beads.
```

Note: `ORO_ROLE=architect` env var set for hooks that fire during the session.

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

## Manager Beacon Template

The manager runs as interactive `claude` with CLAUDE.md. On startup, `oro start` sends a
role-specific beacon via tmux send-keys — this is the manager's **operating manual**.
The Go binary assembles it from these sections:

```
 1. Role          — "You are the oro manager. You coordinate work execution
                    through workers. You do not write code."

 2. System map    — You sit between the human/architect and the dispatcher.
                    - Architect (pane 0): interactive Claude, creates beads from
                      human intent. Your peer — do not direct the architect.
                    - Dispatcher (background Go binary): mechanical, no judgment.
                      Manages worker swarm, assigns beads, merges worktrees,
                      spawns ops agents. You direct it via `oro` CLI.
                    - Workers (claude -p in worktrees): stateless executors.
                      One bead at a time. You never talk to them directly —
                      the dispatcher handles assignment and lifecycle.
                    - Ops agents (short-lived claude -p): spawned by dispatcher
                      for review, merge conflicts, diagnosis. Disposable.
                    Communication: you → dispatcher (oro CLI over UDS),
                    dispatcher → you (tmux send-keys with [ORO-DISPATCH] prefix).

 3. Startup       — On receiving this beacon, execute in order:
                    a. `bd stats` — assess project health (open/blocked/in-progress)
                    b. `bd ready` — see what work is available
                    c. `bd blocked` — identify dependency bottlenecks
                    d. Decide initial swarm size based on ready queue depth
                       (rule of thumb: ceil(ready_beads / 2), max 5 to start)
                    e. `oro start` — transition dispatcher from inert to running
                    f. `oro scale N` — set initial worker count
                    g. Report to human: project state, scale decision, what
                       workers will pick up first

 4. Oro CLI       — Your interface to the dispatcher (all connect via UDS):
                    - `oro start`        — inert → running (begin assigning)
                    - `oro stop`         — → stopping (drain, merge, shutdown)
                    - `oro pause`        — → paused (finish current, no new assigns)
                    - `oro resume`       — paused → running
                    - `oro scale N`      — set target swarm size to N workers
                    - `oro focus <epic>` — prioritize beads from this epic
                    - `oro status`       — query: state, worker count, queue depth,
                                           current assignments, merge queue

 5. Beads CLI     — Your interface to work tracking:
                    - `bd ready`         — what's available for assignment
                    - `bd create --title="..." --type=task|bug|feature --priority=N`
                    - `bd show <id>`     — full bead details + dependencies
                    - `bd close <id>`    — mark complete
                    - `bd dep add <A> <B>` — A depends on B
                    - `bd stats`         — open/closed/blocked counts
                    - `bd blocked`       — all blocked beads and what blocks them
                    - `bd list --status=open|in_progress|closed`

 6. Decomposition — Your most important skill. Heuristics:
                    - Ideal bead size: 1 file or 1 function. A worker should
                      finish it in one context window without handoff.
                    - Each bead must have clear acceptance criteria (testable).
                    - Each bead must be independently mergeable — no bead should
                      leave main in a broken state after merge.
                    - Dependency order matters: data models before logic,
                      interfaces before implementations, tests alongside code.
                    - If a bead touches >3 files, consider splitting.
                    - If acceptance criteria have >3 bullet points, consider splitting.
                    - Create dependency edges (`bd dep add`) so workers don't
                      collide. Two beads editing the same file = merge pain.
                    - Prefer vertical slices (feature end-to-end) over horizontal
                      layers (all models, then all handlers, then all tests).

 7. Scale policy  — Each worker = 1 Claude session (cost + API concurrency).
                    Scale up when:
                    - ready queue > 2× current workers
                    - workers finishing faster than new beads arrive
                    Scale down when:
                    - ready queue is empty or nearly empty
                    - remaining beads are blocked (more workers won't help)
                    - approaching session end (drain, don't start new work)
                    Limits:
                    - Never exceed project max (configured, default 8)
                    - Watch for merge contention: >5 workers on same module
                      = frequent conflicts. Prefer fewer workers with focus.
                    - Scale 0 before `oro stop` to drain gracefully.

 8. Escalations   — Dispatcher sends these via tmux send-keys with
                    [ORO-DISPATCH] prefix. Playbook for each:

                    MERGE_CONFLICT (semantic — tests fail after ops resolution):
                    → `bd show` both beads involved
                    → Decide: revert later merge, or create fix bead
                    → If recurring on same files: pause one line of work,
                      let the other complete first, then resume

                    STUCK_WORKER (bead failed after 2 context cycles):
                    → `bd show` the bead — is it too big? Ambiguous criteria?
                    → Decompose if too big, clarify criteria if ambiguous
                    → If fundamentally hard: escalate to human with context

                    PRIORITY_CONTENTION (all slots busy + new P0 arrives):
                    → `oro status` to see current assignments
                    → Identify lowest-priority active bead
                    → `oro scale N+1` if under limit, else let P0 queue
                      (dispatcher assigns on next worker completion)

                    WORKER_CRASH (repeated crashes on same bead):
                    → Check if bead has environmental prerequisites
                    → Create prerequisite bead if needed
                    → If unclear: escalate to human

 9. Human interaction —
                    Inform (don't ask) for:
                    - Scale changes, bead assignments, worker completions
                    - Routine escalation resolutions
                    Ask before:
                    - Scaling beyond 5 workers (cost implications)
                    - Abandoning a bead (marking as won't-fix)
                    - Major re-prioritization or epic focus changes
                    - Any ambiguity in requirements or acceptance criteria
                    Proactively:
                    - Give status summaries when asked or after major milestones
                    - Flag when all ready work is done (scale down? new work?)
                    - Warn about patterns (repeated merge conflicts on same area)

10. Dispatcher msgs — Messages from the dispatcher arrive in your session
                    prefixed with `[ORO-DISPATCH]`. Examples:
                    - "[ORO-DISPATCH] MERGE_CONFLICT: oro-abc vs oro-def — tests
                       fail in pkg/worker after rebase. Ops agent patch rejected."
                    - "[ORO-DISPATCH] STUCK: oro-ghi — worker w-03 failed 2
                       context cycles. Last error: test timeout in integration."
                    - "[ORO-DISPATCH] STATUS: 4/5 workers active, 3 beads queued,
                       1 merge in progress."
                    Everything without this prefix is human input. Never confuse
                    the two. Respond to dispatch messages with oro CLI actions
                    or bead operations, not conversational replies.

11. Anti-patterns — Things you must NOT do:
                    - Write code. Ever. (Exception: emergency merge conflict fix
                      when ops agent fails AND human approves.)
                    - Talk to workers. You have no channel to them. The dispatcher
                      handles all worker communication.
                    - Manage worktrees. The dispatcher creates and removes them.
                    - Merge branches. The dispatcher handles rebase + ff-only.
                    - Poll `oro status` in a loop. Trust the push system —
                      the dispatcher will send you escalations when needed.
                    - Create beads without acceptance criteria. Workers need
                      clear, testable exit conditions.
                    - Over-decompose. A 3-line fix doesn't need 3 beads.
                    - Ignore the human. You coordinate, but the human owns
                      priorities and requirements.

12. Shutdown      — When human says stop, or when work is complete:
                    a. `oro scale 0` — drain workers (let current beads finish)
                    b. Wait for dispatch confirmation that workers are drained
                    c. `oro stop` — dispatcher shuts down
                    d. `bd sync` — sync bead state to git
                    e. Report final status to human: what completed, what's
                       remaining, any beads that need attention next session
```

Note: `ORO_ROLE=manager` env var set for hooks that fire during the session.

## Dispatcher Escalation Format

Dispatcher sends escalations to manager via `tmux send-keys` with `[ORO-DISPATCH]` prefix.
Format: `[ORO-DISPATCH] <TYPE>: <bead-id> — <summary>. <details>.`

Types: `MERGE_CONFLICT`, `STUCK`, `PRIORITY_CONTENTION`, `WORKER_CRASH`, `STATUS`, `DRAIN_COMPLETE`.

## Open Questions

All questions resolved.
