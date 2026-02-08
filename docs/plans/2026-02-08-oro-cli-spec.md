# Oro CLI Spec

**Date:** 2026-02-08
**Bead:** oro-r7b
**Status:** Spec

## Overview

`oro` is the single entry point for the Oro agent swarm. It manages two concerns:

1. **Session orchestration** — tmux layout, Claude Code sessions, daemon lifecycle
2. **Memory interface** — remember/recall commands for the memory store

The dispatcher daemon is an implementation detail — it launches automatically and the user never interacts with it directly.

## Commands

### `oro start`

Creates a tmux session with the full Oro layout and begins autonomous execution.

**What happens:**

1. Check if an `oro` tmux session already exists. If so, attach to it.
2. Start the dispatcher daemon in background (if not already running).
   - Dispatcher opens UDS socket, SQLite WAL, begins heartbeat monitor.
   - Spawns worker pool (default 2, configurable via `oro start -w N`).
   - Workers connect to dispatcher via UDS, wait for assignments.
3. Create tmux session `oro` with two vertical panes:
   - **Left pane: Architect** — Interactive `claude` session. User's workspace for exploration, design, bead creation.
   - **Right pane: Manager** — Autonomous `claude -p` session primed with a system prompt that instructs it to:
     - Run `bd ready` to find work
     - Send `start` directive to dispatcher (via SQLite command row)
     - Monitor worker completion events
     - Handle merge (via merge coordinator)
     - Run quality gate on main after each merge
     - Loop: pull next ready bead, assign, review, merge, repeat
     - Never stop unless told to (`oro stop` or explicit instruction)
4. Attach to the tmux session.

**Flags:**
- `-w N` — Number of workers (default 2)
- `-d` — Daemon-only mode (start dispatcher without tmux/sessions, for CI or testing)
- `--model <model>` — Model for manager session (default sonnet)

### `oro stop`

Graceful shutdown of the entire swarm.

**What happens:**

1. Send `stop` directive to dispatcher (SQLite command row).
2. Dispatcher sends `MsgPrepareShutdown` to all workers (graceful shutdown protocol).
3. Workers save context, send `MsgShutdownApproved`, exit.
4. Dispatcher waits for all workers (timeout 30s), then exits.
5. Manager session receives shutdown signal, writes handoff, exits.
6. Kill tmux session `oro`.
7. Run `bd sync`.

### `oro status`

Show current swarm state.

**Output:**
- Dispatcher: running/stopped, PID, uptime
- Workers: count, active beads, context %, last heartbeat
- Manager: running/stopped
- Beads: in_progress count, ready count, blocked count
- Recent merges: last 3 merged beads

### `oro remember <text>`

Insert a memory into the store.

**What happens:**
1. Parse text for type hints (prefix with `lesson:`, `decision:`, `gotcha:`, `pattern:`).
2. Default type: `lesson`, source: `user_manual`, confidence: 1.0.
3. Insert via `memory.Store.Insert()`.
4. Print confirmation with ID.

### `oro recall <query>`

Search memories by text query.

**What happens:**
1. FTS5 search via `memory.Store.Search()`.
2. Display top 5 results with type, content, age, confidence, source.

### `oro logs [worker-id]`

Tail dispatcher event log. Optional filter by worker.

### `oro pause` / `oro resume`

Send pause/resume directives to dispatcher. Pause: workers finish current bead but don't get new assignments. Resume: dispatcher starts assigning again.

## Architecture

```
oro start
  │
  ├── Dispatcher daemon (background Go process)
  │     ├── UDS listener (/tmp/oro-dispatcher.sock)
  │     ├── SQLite WAL (oro.db — events, assignments, commands, memories)
  │     ├── Worker pool (N workers, each in a worktree)
  │     │     ├── Worker 1: .worktrees/worker-1, branch agent/worker-1
  │     │     └── Worker 2: .worktrees/worker-2, branch agent/worker-2
  │     ├── Heartbeat monitor (45s timeout)
  │     └── Merge coordinator (rebase + ff-only, sequential lock)
  │
  ├── tmux session "oro"
  │     ├── Left pane: Architect (interactive claude)
  │     └── Right pane: Manager (autonomous claude -p)
  │
  └── Manager prompt:
        "You are the Oro Manager. Your job is to execute beads autonomously.
         Run bd ready, send start directive, monitor workers, review+merge,
         run quality gate, repeat. Never stop unless oro stop is called."
```

## Manager Prompt (Key Sections)

The manager is a `claude -p` session with a carefully crafted prompt:

```
You are the Oro Manager — an autonomous agent that executes beads continuously.

## Your Loop
1. Run `bd ready` to find unblocked work
2. For each ready bead, insert an assignment row into oro.db
3. Monitor worker status via dispatcher events table
4. When a worker signals READY_FOR_REVIEW:
   a. Check that QualityGatePassed=true in DonePayload
   b. If false: reassign bead (worker must fix)
   c. If true: trigger merge coordinator
5. After merge: run quality_gate.sh on main
6. If gate fails: revert merge, reassign bead with failure context
7. Loop back to step 1
8. Never stop. Never ask for input. Work autonomously.

## Directives
- You communicate with the dispatcher via SQLite command rows
- INSERT INTO commands (directive, args, status) VALUES ('start', '', 'pending')
- Check processed commands for acknowledgment

## Rules
- Never merge untested code
- Never skip the quality gate
- Log decisions to bd comments
- If stuck: create a new bead describing the blocker, continue with other work
```

## Daemon Lifecycle

The dispatcher daemon is managed via a PID file (`/tmp/oro-dispatcher.pid`).

- `oro start`: Check PID file. If process alive, reuse. If stale/missing, start new.
- `oro stop`: Send SIGTERM to PID. Daemon handles graceful shutdown.
- `oro status`: Check PID file + process liveness.
- `-d` flag: Start daemon without tmux (for testing/CI).

## File Layout

```
cmd/
  oro/
    main.go           # CLI entry point (cobra/ff)
    cmd_start.go      # oro start
    cmd_stop.go       # oro stop
    cmd_status.go     # oro status
    cmd_remember.go   # oro remember
    cmd_recall.go     # oro recall
    daemon.go         # Daemon lifecycle (PID file, start/stop)
    manager.go        # Manager prompt generation
    tmux.go           # tmux session/pane management
```

## Open Questions

1. **Manager model**: Should the manager use opus (better reasoning) or sonnet (faster, cheaper) by default? Probably sonnet with `--model` override.
2. **Worker count**: Default 2 workers. What's the right max? Probably capped at 5 (git worktree contention, API rate limits).
3. **Architect session**: Plain `claude` or `claude` with special CLAUDE.md? Probably just `claude` with the project's existing CLAUDE.md — the architect is the user's session.
4. **Manager context exhaustion**: What happens when the manager hits context limits? Options: (a) manager writes handoff and respawns itself, (b) oro detects and restarts manager. Probably (b) — oro monitors the manager tmux pane and restarts if it exits.
