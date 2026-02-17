# Component Lifecycle: Independent Dispatcher and Worker Management

**Date:** 2026-02-17
**Status:** Design approved
**Epic:** oro-TBD (P0)

## Problem

`oro start` and `oro stop` are monolithic — they couple dispatcher, tmux session, and workers into a single lifecycle. There's no way to:
- Start/stop the dispatcher independently (without tmux)
- Launch workers manually for specific beads
- Stop individual workers with proper cleanup (worktree + bead reset)
- Mix dispatcher-managed and externally-launched workers

Additionally, `applyKillWorker` has cleanup gaps: it doesn't remove worktrees, reset beads to `open`, or clear bead tracking maps. The shutdown sequence also doesn't reset in-progress beads.

## Design

### CLI Surface

```
oro dispatcher start [--workers N]    # default N=0 (manual worker mode)
oro dispatcher stop [--force]         # graceful shutdown, no tmux

oro worker launch [--count N] [--id <name>] [--bead <id>]
oro worker stop <id> [--all]
```

Existing `oro start` / `oro stop` are unchanged (full swarm mode).

### Managed vs External Workers

Add `managed bool` field to `trackedWorker`:
- `true` when spawned by dispatcher's `scaleUp` (via `procMgr.Spawn`)
- `false` when a worker connects externally (default in `registerWorker`)

`reconcileScale` behavior:
- `MaxWorkers=0` → no-op (skip entirely, manual mode)
- `MaxWorkers>0` → count/kill only `managed` workers; external workers are invisible to scaling

### Kill Worker Cleanup (applyKillWorker)

Current behavior (gaps):
1. Closes connection, removes from pool ✓
2. Decrements `targetWorkers` ✓ (but should skip for unmanaged workers)
3. Calls `completeAssignment` (SQLite only) ✓
4. **Missing:** worktree removal
5. **Missing:** bead status reset to `open`
6. **Missing:** `clearBeadTracking(beadID)`

Fixed behavior:
1. Capture `w.worktree` and `w.beadID` before removal
2. Close connection, remove from pool
3. Decrement `targetWorkers` **only if `w.managed`**
4. `d.worktrees.Remove(ctx, worktree)` — remove git worktree
5. `d.beads.Update(ctx, beadID, "open")` — reset bead for re-assignment
6. `d.clearBeadTracking(beadID)` — clear all tracking maps
7. `completeAssignment` — mark SQLite assignment row completed

### Shutdown Sequence Bead Reset

Add Phase 3b to `shutdownSequence` (after workers drained, before worktree removal):

```
Phase 1: Cancel ops agents, abort merges (existing)
Phase 2: Drain workers via PREPARE_SHUTDOWN (existing)
Phase 3: Collect in-progress bead IDs from assignments table
Phase 3b: Reset each to "open" via beads.Update (NEW)
Phase 4: Remove worktrees (existing)
Phase 5: Flush bead state via beads.Sync (existing)
```

### Command Details

**`oro dispatcher start [--workers N]`**
- Runs preflight + bootstrap (reuse `preflightAndCheckRunning`)
- Spawns daemon via `runDaemonOnly` path (no tmux)
- Default `--workers 0` (manual mode)
- Auto-sends `start` directive after socket ready
- Prints `dispatcher started (PID <pid>, workers=<N>)`

**`oro dispatcher stop [--force]`**
- Same authorization as `oro stop` (TTY or `--force` + `ORO_HUMAN_CONFIRMED=1`)
- SIGINT to daemon PID → wait for drain → bd sync → remove PID
- Does NOT kill tmux session
- Reuses `waitForExit`, `confirmStop` from cmd_stop.go

**`oro worker launch [--count N] [--id <name>] [--bead <id>]`**
- Verifies dispatcher is running (check socket exists)
- Resolves socket path from standard location
- Generates worker ID if not provided (`ext-<timestamp>-<i>`)
- Spawns `oro worker --socket <path> --id <id>` as detached subprocess
- `--bead <id>`: sends `spawn-for <bead-id>` directive instead of plain launch
- `--count N`: spawns N workers (default 1)

**`oro worker stop <id> [--all]`**
- Sends `kill-worker <id>` directive via UDS socket
- `--all`: queries status for all worker IDs, kills each
- Cleanup happens in dispatcher's `applyKillWorker` (worktree + bead reset)

### Interaction Matrix

| Scenario | MaxWorkers | Managed | External | reconcileScale |
|----------|-----------|---------|----------|---------------|
| Full auto (`oro start`) | N>0 | N | 0 | Manages N workers |
| Manual only (`oro dispatcher start`) | 0 | 0 | user-launched | No-op |
| Mixed (auto + dedicated bead worker) | N>0 | N | 1+ | Ignores external |
| Scale down in mixed | N→M | N→M killed | untouched | Only kills managed |

## Dependencies

```
managed flag (1)
├── applyKillWorker cleanup (2) [depends on 1]
│   └── oro worker stop CLI (7) [depends on 2]
├── reconcileScale guard (bundled with 1)
│
shutdownSequence bead reset (3) [independent]
└── oro dispatcher stop CLI (5) [depends on 3]

oro dispatcher start CLI (4) [independent]
oro worker launch CLI (6) [independent]
```

Beads 1, 3, 4, 6 can all start in parallel.
After 1 lands → 2 unblocks → 7 unblocks.
After 3 lands → 5 unblocks.
