---
name: oro
description: >
  Use when working in an oro-managed project. Two modes:
  "use oro" / "oro work" → single worker on one bead (lightweight, no dispatcher).
  "launch oro" → full swarm with dispatcher, tmux, multiple workers.

  Workflow-specific sub-skills:
  - Running the swarm and monitoring → /watching-oro
  - Restarting after crash or stuck state → /restart-oro
  - Decomposing a spec into beads → /beadcraft
  - Creating a handoff for session continuity → /create-handoff
  - Resuming from a handoff → /resume-handoff
user-invocable: true
---

# Oro

## Mode 1: Work a Bead (`oro work`)

Single worker executes one bead end-to-end. No dispatcher, no tmux, no swarm.

```bash
oro work <bead-id>                              # work a bead against default branch
oro work <bead-id> --base-branch feature/auth   # target a specific branch
oro work <bead-id> --timeout 20m                # extend timeout for complex beads
```

**What happens:** Worker claims bead → creates worktree → TDD implementation → quality gate (tests + lint + format) → ops review → merge to target branch → cleanup.

**When to use:** Default for single beads. Say "use oro" or "oro work" to trigger this.

## Mode 2: Launch Swarm (`oro start`)

Full system: tmux session, dispatcher daemon, multiple parallel workers.

```bash
oro start --workers 3 --detach                  # launch swarm (detached tmux)
oro start --workers 3 --base-branch feature/auth --detach  # swarm targets a branch
oro status                                      # check swarm state
oro attach                                      # connect to running swarm UI
ORO_HUMAN_CONFIRMED=1 oro stop --force          # shutdown (non-TTY safe)
```

**What happens:** Dispatcher polls `oro bead ready` → assigns beads to idle workers in isolated worktrees → quality gate → ops review → merge → next bead. Workers loop until queue is empty or context exhausted (handoff to fresh worker).

**When to use:** Multiple beads to execute in parallel. Say "launch oro" to trigger this.

## Monitoring

After launching the swarm, **always set up monitoring** to catch stuck workers and beads.

### Option A: In-session (`/watching-oro`)

Active babysitting — observe, detect defects, file beads, fix, rebuild, relaunch. Use when you're actively working the swarm.

### Option B: Background cron (`/loop`)

Lightweight automated monitoring. Set up immediately after `oro start`:

```
/loop 5m <monitoring prompt>
```

The monitoring prompt should:

1. Run `oro status` and `oro logs --tail 100 | grep -v heartbeat | grep -v directive | grep -v missing_accept | tail -15`
2. Report worker state and bead assignments
3. Detect and fix stuck states (see table below)
4. Report beads completed since last check

### Stuck Detection

| Signal | Meaning | Auto-fix |
|--------|---------|----------|
| Worker idle + queue > 0 for 2+ checks | Assignment stuck | Check `oro bead ready`, restart dispatcher |
| Same bead on same worker for 3+ checks (>15min) | Worker stuck | Check context %, kill if >80% |
| `REJECTED` repeating >2x for same bead | Worker can't pass review | Read rejection feedback, check if bead AC is achievable |
| `QG_FAILED` repeating >3x for same bead | Worker can't pass QG | Check QG output, may be flaky test vs real failure |
| `merge_failed` repeating for same bead | Stale agent branch | Clean branch: `git branch -D agent/<bead-id>`, reopen bead |
| Bead `IN_PROGRESS` but no worker assigned | Orphaned bead | `oro bead update <id> --status open` to re-queue |
| `progress_timeout` → re-assign → timeout loop | Bead merged but not closed | Check if code is on main/epic branch, `oro bead close <id>` manually |
| Workers idle, queue 0, beads still open | All work done or blocked | Check `oro bead ready` — if empty, epic may need closing |

### When to stop monitoring

- Queue empty + workers idle for 3+ consecutive checks → stop cron, stop swarm
- All target beads/epic closed → stop cron, stop swarm

## Commands

| Command | Purpose |
|---------|---------|
| `oro work <bead-id>` | Execute one bead (lightweight, no dispatcher) |
| `oro start [--workers N] [--detach]` | Launch swarm |
| `oro stop` | Graceful shutdown |
| `oro status` | Dispatcher state, workers, active beads |
| `oro attach` | Connect to tmux session |
| `oro logs [-f] [--tail N]` | Query event logs |
| `oro directive <op>` | Control dispatcher: `status`, `pause`, `resume`, `scale N` |
| `oro cleanup` | Clean stale state after crash |
| `oro dolt setup\|teardown\|status` | Manage shared Dolt server |

Run `oro <command> --help` for flags.

## Branch Targeting

Both modes support `--base-branch`:
- **`oro work`**: `--base-branch feature/auth` — worker branches from and merges to that branch
- **`oro start`**: `--base-branch feature/auth` — all workers default to that branch (per-bead override via `--metadata branch=X`)

Without `--base-branch`, defaults to current HEAD at startup (falls back to `main`).

## Philosophy

1. **Less Context, Better Work** — Workers see only their bead's AC, relevant memories, and a clean worktree. Context is a budget: spend it on signal.
2. **Compound Learnings** — Every session leaves the system smarter. Workers emit learnings, the dispatcher extracts patterns, memory consolidation scores and surfaces them.
3. **Loop Until Done** — Context exhausted → handoff → fresh worker continues. Review fails → feedback → retry. Merge conflicts → ops agent resolves. No work is lost.
4. **Better Specs, Better Outcomes** — Spend tokens upstream: brainstorm alternatives, premortem designs, write validated specs before code.
5. **Guards Over Trust** — TDD, quality gate, ops review, evidence-based verification. Guards aren't overhead — they're what enable fearless execution.

## Architecture

```
oro work <bead-id>           # lightweight — single worker, no dispatcher
  └─ worker process
       ├─ creates worktree
       ├─ TDD implementation
       ├─ quality gate
       ├─ ops review
       └─ merge + cleanup

oro start --workers 3        # full swarm
  └─ tmux session "oro"
       ├─ pane 0: architect (strategic oversight)
       ├─ pane 1: manager (bead triage, reviews)
       └─ panes 2+: workers (one per bead)

  Dispatcher (background daemon)
    ├─ polls oro bead ready for unblocked beads
    ├─ assigns to idle workers in isolated worktrees
    ├─ runs quality gates (tests, lint, format)
    ├─ sends to ops review → merge to target branch
    └─ communicates via UDS (Unix domain sockets)
```

## Beadcraft Quick Reference

### Creating an Epic with Children

```bash
# 1. Create the epic
oro bead create "Feature name" --type epic \
  --acceptance "All child beads closed. Full quality gate passes." \
  --description "Goal from spec"

# 2. Create child tasks with full AC
oro bead create "Specific task" --type task \
  --acceptance "Test: path:FnName | Cmd: test_cmd | Assert: expected
Read: file1.go:Symbol1, file2.go:Symbol2
Signature: func Name(ctx, arg) (Result, error)
Edges: nil input → ErrInvalid" \
  --estimate 7

# 3. Attach parent + wire dependency (order matters!)
oro bead update <child-id> --parent <epic-id>
oro bead dep add <epic-id> <child-id>

# 4. Target a branch (optional — epic children inherit)
oro bead create "Feature name" --type epic \
  --metadata branch=feature/auth ...
```

`oro bead create --parent` is also valid for hierarchy: parentage does not create dependency edges. Add `oro bead dep add <epic-id> <child-id>` explicitly when the epic must wait for the child.

### Bead Anatomy — Every bead needs:

| Field | Required | Example |
|-------|----------|---------|
| `Test:` | Always | `internal/auth/auth_test.go:TestValidateToken` |
| `Cmd:` | Always | `go test ./internal/auth/... -run TestValidateToken -v` |
| `Assert:` | Always | `returns valid=true for unexpired JWT` |
| `Read:` | Always | `internal/auth/token.go:ValidateToken` |
| `Signature:` | When adding funcs | `func ValidateToken(token string) (*Claims, error)` |
| `Edges:` | When non-trivial | `nil secret → ErrNoSecret; expired → ErrExpired` |
| `Branch:` | When targeting non-default | `--metadata branch=feature/auth` |

### Size: Split if ANY apply

- Estimate >7 minutes
- Needs >1 test file or >4 source files
- Title contains "and"

Full decomposition workflow: `/beadcraft`

## Build & Test

```bash
make build                    # required (not go build) — embeds assets
go test ./pkg/dispatcher/... -count=1 -timeout 180s
go test ./pkg/worker/... -v -count=1
```

## Dolt/Beads Recovery

**NEVER run `force-initialization commands`.** It destroys all bead history. This has happened 3 times.

When bead database/Dolt errors occur, follow this recovery ladder:

1. `check Dolt server status` — is the server running?
2. `restart the Dolt server` — restart it
3. `test Dolt connectivity` — can we connect?
4. `run bead-store server diagnostics` — deeper diagnosis
5. `run non-destructive bead-store repair` — auto-repair
6. `rebuild from JSONL backup` — rebuild from backup WITHOUT wiping

If none of these work, **ask the user**. Never nuke the database autonomously.

## Key Gotchas

See [gotchas.md](references/gotchas.md) for the full list.
