# Oro: System Lifecycle

> How Oro runs, end-to-end. The authoritative overview of all roles, capabilities, and behaviors.
>
> **Detail docs:** [Manager Redesign (archived)](../../archive/2026-02-07-manager-redesign.md) · [IPC Comparison (archived)](../../archive/2026-02-07-orchestrator-ipc-comparison.md) · [Memory System](2026-02-07-memory-system-spec.md)
> **Authoritative spec:** [Architecture Spec](2026-02-08-oro-architecture-spec.md) — resolves all open questions and supersedes detail docs above.

## What Oro Is

Oro is a multi-agent orchestration system for autonomous software engineering. A human (the Architect) designs work. An AI coordinator (the Manager) provides judgment. A Go binary (the Dispatcher) handles all mechanical coordination. AI workers execute tasks in isolated git worktrees, cycling context autonomously (ralph loop) until work is done.

## Roles

| Role | Runtime | Who drives it | Purpose |
|------|---------|--------------|---------|
| **Architect** | Claude Code (interactive, tmux pane) | Human | Design specs, create beads, set priorities |
| **Manager** | Claude Code (event-driven, tmux pane) | Dispatcher (via tmux send-keys) | Judgment calls: merge escalations, stuck workers, priority tradeoffs |
| **Dispatcher** | Go binary (autonomous, background) | Itself, after Manager says `start` | All mechanical coordination: assign, heartbeat, crash recovery, merge, review, worker lifecycle |
| **Workers** | Go binary + `claude -p` (background) | Dispatcher (via UDS) | Execute beads in isolated worktrees |
| **Ops Agents** | `claude -p` (short-lived, background) | Dispatcher | Merge conflict resolution, code review, crash analysis |

## End-to-End Lifecycle

### Phase 1: Session Start

```
User opens terminal
  → oro start
  → Tmux session created: Architect pane + Manager pane
  → Dispatcher Go binary launches (background, inert)
  → Manager Claude session starts, reviews bd ready, issues `start` directive
  → Dispatcher activates
```

### Phase 2: Work Creation

```
Architect designs a feature
  → Writes spec
  → bd create --title="..." --type=feature --priority=2
  → bd dep add <child> <parent>  (wire dependencies)
  → Beads exist in beads DB with priorities and dependency graph
```

The Architect does not talk to the Manager or Dispatcher. Creating beads IS the signal. The Dispatcher watches the beads DB for new ready work.

### Phase 3: Dispatch

```
Dispatcher polls bd ready
  → Sorts by priority: P0 → P1 → P2 → P3
  → Picks highest-priority unblocked bead
  → Creates git worktree: .worktrees/bead-<id>
  → Spawns Worker Go binary in worktree
  → Sends ASSIGN over UDS with bead context
  → Worker spawns claude -p with assembled prompt (template + bead desc + acceptance criteria + handoff context + relevant memories)
```

Model routing: Dispatcher picks Opus 4.6 for judgment-heavy work, latest Sonnet for mechanical tasks.

### Phase 4: Execution (Worker Loop)

```
Worker claude -p session begins
  → Reads bead acceptance criteria
  → Writes tests (TDD: red → green → refactor)
  → Runs quality gate (tests + lint + format)
  → Commits work
  → Emits [MEMORY] markers for learnings
  → Signals READY_FOR_REVIEW to Dispatcher
```

Worker Go binary monitors throughout:
- Heartbeat messages to Dispatcher over UDS
- Context % via Claude Code hook writing to `.oro/context_pct`
- If context > threshold → triggers ralph handoff (see Phase 4b)

### Phase 4b: Ralph Handoff (Context Cycling)

```
Worker context hits threshold (e.g. 70%)
  → Go binary injects prompt: "Write .oro/handoff.yaml and stop"
  → Claude writes handoff (what's done, what remains, key decisions)
  → Go binary annotates bead: bd update <id> --notes="handoff: <summary>"
  → Go binary sends HANDOFF signal to Dispatcher over UDS
  → Dispatcher sends SHUTDOWN to old worker
  → Dispatcher spawns fresh Worker in same worktree
  → Fresh worker reads .oro/handoff.yaml + bd show <id>
  → Continues where previous worker left off
```

Workers can ralph indefinitely. Each cycle gets a fresh context window with handoff continuity.

### Phase 5: Review (Two-Stage)

```
Worker signals READY_FOR_REVIEW
  → Dispatcher spawns Ops Agent (reviewer) in same worktree
  → Reviewer reads bd show <bead> (acceptance criteria) + git diff (changes)
  → APPROVED → Worker signals DONE → enters merge queue
  → REJECTED → Dispatcher sends feedback to Worker → Worker fixes → back to review
  → 2 rejection cycles → escalate to Manager
```

Reviewer and worker share the same pool slot — no net increase in concurrent sessions.

### Phase 6: Merge

```
Worker signals DONE
  → Dispatcher acquires merge lock
  → Dispatcher runs git rebase main in worker's worktree
  → Clean? → git checkout main && git merge --ff-only → release lock → recycle worker
  → Conflict? → git rebase --abort → Dispatcher spawns Ops Agent to resolve
    → Ops Agent resolves + tests pass → re-enter merge queue
    → Tests still fail → escalate to Manager
```

Merge context lives on the bead (annotations), not in SQLite. Any resolver reads both beads' annotations to understand both sides.

### Phase 7: Memory

Throughout execution, memories are captured and fed back:

**Extraction (automatic):**
- Worker emits `[MEMORY]` markers → Go binary INSERTs to `.oro/state.db`
- Daemon extracts implicit learnings from session logs post-completion
- Manager runs `oro memories consolidate` periodically (dedup, prune stale)

**Injection (automatic):**

- Go wrapper queries memories before constructing bead prompt
- Top 3 relevant memories (by tag overlap + BM25 + time decay) injected as `## Relevant Memories` section
- Workers can also query mid-task: `oro recall "query"`

**Storage:** SQLite FTS5 (`memories` table in `.oro/state.db`). Embeddings column reserved for future semantic search (oro-ldf epic).

### Phase 8: Escalation

The Dispatcher handles everything except three cases that require the Manager's judgment:

| Escalation | Trigger | Manager response |
|------------|---------|-----------------|
| Semantic merge conflict | Tests fail after ops agent resolution attempt | Review conflict, decide resolution or deprioritize a bead |
| Stuck worker | Same bead fails after 2 ralph cycles | Investigate, rewrite acceptance criteria, or decompose bead |
| Priority contention | All slots busy + new P0 arrives | Decide which work to preempt |

Dispatcher sends escalations via `tmux send-keys` to Manager pane. Manager responds with `oro` CLI commands → UDS → Dispatcher acts immediately.

### Phase 9: Session End

```
User decides to stop
  → Architect runs oro stop
  → Manager issues `stop` directive to Dispatcher
  → Dispatcher finishes current merges, stops assigning new work
  → Workers complete current tasks or write handoffs
  → bd sync --flush-only
  → Handoff YAML written with session state
  → git commit && git push
```

## Communication Map

```
                Beads DB (work definitions)
                    ▲           │
          bd create │           │ bd ready / bd show / bd close
                    │           ▼
  ┌──────────┐     │     ┌─────────────┐
  │ Architect │     │     │ Dispatcher  │
  │ (Claude)  │     │     │ (Go binary) │
  └──────────┘     │     └──┬──┬──┬────┘
                   │        │  │  │
              ┌────┘   UDS ─┘  │  └─ tmux send-keys
              │        ┌──────┘         │
              │        │                ▼
              │   ┌────┴───┐     ┌──────────┐
              │   │Workers │     │ Manager  │
              │   │(Go+    │     │ (Claude) │
              │   │claude-p│     │          │
              │   └────────┘     └────┬─────┘
              │                       │
              │       oro CLI → UDS ──┘
              │
         SQLite (.oro/state.db)
         ├── events (runtime log)
         ├── assignments (who's doing what)
         ├── commands (audit log only)
         └── memories (cross-session learnings)
```

## Manager Directives

| Directive | Meaning |
|-----------|---------|
| `start` | Begin pulling and assigning ready work |
| `stop` | Finish current work, don't assign new beads |
| `pause` | Hold new assignments, workers keep running |
| `focus <epic>` | Prioritize beads from this epic |

Manager sends directives via `oro` CLI commands, which connect to the Dispatcher's UDS socket. SQLite `commands` table is retained as a write-after-ACK audit log only. Dispatcher is inert until it receives `start`. After that, fully autonomous.

## Worker Pool

- **Target:** 5 concurrent slots (soft guideline, not hard limit)
- **Shared:** Workers and ops agents draw from the same pool
- **Model routing:** Dispatcher selects per task:
  - Opus 4.6: complex merges, architectural review, stuck-bead diagnosis
  - Latest Sonnet: straightforward bead execution, clean reviews, mechanical tasks

## Key Behaviors

### Ralph Loop (Context Cycling)
Workers cycle context autonomously. Go binary detects high context %, triggers handoff, respawns fresh worker in same worktree. Handoff YAML + bead annotations carry continuity. No human intervention needed.

### Priority Queue
Dispatcher pulls work by priority: P0 (bugs, test failures) → P1 (merge conflicts) → P2 (current epic) → P3 (backlog). The Architect controls priority through bead creation. No runtime negotiation.

### Crash Recovery
- Worker crashes → UDS connection drops → Dispatcher detects immediately → reassigns bead
- Dispatcher crashes → Workers buffer events, retry UDS connection → Dispatcher restarts, reads SQLite, accepts reconnections
- Manager crashes → Dispatcher continues autonomously (already running) → Manager restarts, reviews state

### Code Search (oro-w2u, not yet specced)

All roles get a full code search stack for navigating the codebase efficiently. Three layers from CC-v3 reference: AST-aware read enforcer (token savings), search router (structural/literal/semantic), broad codebase search. One configuration for all roles.

## Implementation Beads

| Bead | What | Blocked by |
|------|------|------------|
| ~~oro-o7r~~ | Manager redesign (this doc) | — |
| oro-2jb | Shared protocol package (UDS message types + SQLite schema + Go types) | — |
| oro-68t | Merge coordinator (Go package, integrated by Dispatcher) | — |
| oro-rue | Ops agent spawner (review, merge conflict, diagnosis) | oro-2jb |
| oro-w6n | Dispatcher Go binary | oro-2jb, oro-68t, oro-rue |
| oro-773 | Worker Go binary | oro-2jb |
| oro-r7b | `oro` CLI (start/stop/status, directives, remember/recall) | oro-2jb |
| oro-0q5 | Memory system (SQLite tables + extraction + prompt injection) | oro-2jb |
| oro-w2u | Code search spec (all roles) | ~~oro-o7r~~ |
| oro-seg | Memory search/retrieval algorithm spec (FTS5 + RRF) | — |
| oro-ldf | Embedding-based memory (future) | oro-w6n |
