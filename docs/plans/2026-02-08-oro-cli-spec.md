# Oro CLI Spec

**Date:** 2026-02-08 (updated 2026-02-09)
**Bead:** oro-r7b
**Status:** Spec
**Reference:** [Architecture Spec](2026-02-08-oro-architecture-spec.md)

## Overview

`oro` is the single entry point for the Oro agent swarm. It manages two concerns:

1. **Session orchestration** — tmux layout, Claude Code sessions, dispatcher lifecycle
2. **Runtime commands** — directives, memory interface, status queries

All commands that interact with the dispatcher communicate via UDS (Unix domain socket). The dispatcher daemon is started by `oro start` and runs in the background.

## Commands

### `oro start`

Creates a tmux session with the full Oro layout and launches the dispatcher.

**What happens:**

1. Preflight checks: verify `tmux`, `claude`, `bd`, `git` are available.
2. Bootstrap `~/.oro` directory if first run.
3. Check if an `oro` tmux session already exists. If so, attach to it.
4. Launch dispatcher Go binary in background (**inert**, swarm size 0).
   - Dispatcher opens UDS socket at `/tmp/oro.sock`.
   - Begins fsnotify watch on `.beads/` with 60s fallback poll.
5. Create tmux session `oro` with two panes:
   - **Pane 0: Architect** — Interactive `claude` session. Human's workspace.
   - **Pane 1: Manager** — Interactive `claude` session. Reads CLAUDE.md for role context.
6. Send role-specific beacons via `tmux send-keys`:
   - Architect beacon (9-section template, sets `ORO_ROLE=architect`)
   - Manager beacon (12-section template, sets `ORO_ROLE=manager`)
7. Manager autonomously runs startup protocol: `bd stats` → `bd ready` → `bd blocked` → decides swarm size → `oro start` directive → `oro scale N`.
8. Attach to the tmux session.

**Flags:**
- `-d` — Daemon-only mode (start dispatcher without tmux/sessions, for CI or testing)

### `oro stop`

Graceful shutdown of the entire swarm.

**What happens:**

1. Send `stop` directive to dispatcher via UDS.
2. Dispatcher runs shutdown cleanup:
   a. Cancel ops agents
   b. Abort in-flight merges
   c. PREPARE_SHUTDOWN → wait for SHUTDOWN_APPROVED (or timeout)
   d. SIGTERM → grace period → SIGKILL worker processes
   e. Remove worktrees
   f. `bd sync`
   g. Close UDS listener
3. Kill tmux session `oro`.

### `oro scale N`

Set target worker swarm size.

**What happens:**

1. Send `scale` directive with target N to dispatcher via UDS.
2. Dispatcher compares target vs current workers.
3. If scaling up: spawn `oro-worker --socket=<path> --id=<worker-id>` processes.
4. If scaling down: select workers to remove (idle first, then newest busy), send PREPARE_SHUTDOWN.
5. Returns ACK with detail: `"target=N, spawning/killing M"`.

### `oro status`

Query current swarm state via UDS.

**What happens:**

1. Send `status` directive to dispatcher via UDS.
2. Dispatcher returns JSON: state, worker count, queue depth, current assignments, merge queue.
3. Display:
   - Dispatcher: state (inert/running/paused/stopping), uptime
   - Workers: count, active beads, context %
   - Beads: in_progress count, ready count, blocked count

### `oro pause` / `oro resume`

Send pause/resume directives to dispatcher via UDS. Pause: workers finish current bead but don't get new assignments. Resume: dispatcher starts assigning again.

### `oro focus <epic>`

Send focus directive to dispatcher via UDS. Prioritize beads from a specific epic.

### `oro remember <text>`

Insert a memory into the store.

**What happens:**
1. Parse text for type hints (prefix with `lesson:`, `decision:`, `gotcha:`, `pattern:`, `preference:`).
2. Default type: `lesson`, source: `user_manual`, confidence: 1.0.
3. Insert via `memory.Store.Insert()` into `.oro/state.db`.
4. Print confirmation with ID.

### `oro recall <query>`

Search memories by text query.

**What happens:**
1. FTS5 search via `memory.Store.Search()`.
2. Score by BM25 × confidence × time decay (30-day half-life).
3. Display top 5 results with type, content, age, confidence, source.

### `oro logs [worker-id]`

Tail dispatcher event log from SQLite. Optional filter by worker.

### `oro dash`

Launch TUI dashboard (see [TUI Dashboard Spec](2026-02-08-tui-dashboard-spec.md)).

## UDS Protocol

All `oro` CLI commands (except `remember`, `recall`, `logs`, `dash`) connect to the dispatcher's UDS socket as short-lived connections. Protocol: line-delimited JSON.

```
→ {"type":"DIRECTIVE","directive":{"op":"scale","args":"5"}}
← {"type":"ACK","ack":{"ok":true,"detail":"target=5, spawning 3"}}
(disconnects)
```

SQLite `commands` table retained as write-after-ACK audit log only. Dispatcher does NOT poll it.

## Architecture

```
oro start
  │
  ├── Dispatcher daemon (background Go process)
  │     ├── UDS listener (/tmp/oro.sock)
  │     ├── fsnotify on .beads/ (+ 60s fallback poll)
  │     ├── SQLite WAL (.oro/state.db — events, assignments, audit log, memories)
  │     ├── Worker pool (0..N workers, each in a worktree)
  │     ├── Heartbeat monitor (30s interval, timeout detection)
  │     └── Merge coordinator (rebase + ff-only, sequential lock)
  │
  ├── tmux session "oro"
  │     ├── Pane 0: Architect (interactive claude, ORO_ROLE=architect)
  │     └── Pane 1: Manager (interactive claude, ORO_ROLE=manager)
  │
  └── Role beacons sent via tmux send-keys on startup
```

## File Layout

```
cmd/
  oro/
    main.go           # CLI entry point (cobra)
    cmd_start.go      # oro start (tmux + dispatcher + beacons)
    cmd_stop.go       # oro stop
    cmd_status.go     # oro status
    cmd_scale.go      # oro scale N
    cmd_pause.go      # oro pause / resume
    cmd_focus.go      # oro focus <epic>
    cmd_remember.go   # oro remember
    cmd_recall.go     # oro recall
    cmd_logs.go       # oro logs
  oro-worker/
    main.go           # Worker binary (UDS client + claude -p subprocess)
```

## Resolved Questions

1. **Manager model**: Interactive `claude` with CLAUDE.md + role beacon (not `claude -p`). Manager decides its own approach based on beacon instructions.
2. **Worker count**: Starts at 0 (inert). Manager decides scale via `oro scale N`. Default max 8, recommended start ceil(ready_beads / 2).
3. **Architect session**: Interactive `claude` with project CLAUDE.md + architect beacon. The human's session.
4. **Manager context exhaustion**: Manager is an interactive Claude session — handles its own context like any session. If it exits, `oro` can detect and restart the pane.
5. **Directive transport**: UDS, not SQLite. SQLite `commands` table is audit log only.
