---
name: oro
description: >
  Use when working in an oro-managed project. Two modes:
  "use oro" / "oro work" → one task in a single execution tier (lightweight, no dispatcher).
  "launch oro" → full swarm with dispatcher, tmux, and multiple execution tiers.

  Workflow-specific sub-skills:
  - Running the swarm and monitoring → /watching-oro
  - Restarting after crash or stuck state → /restart-oro
  - Decomposing a spec into tasks → /beadcraft
  - Creating a handoff for session continuity → /create-handoff
  - Resuming from a handoff → /resume-handoff
user-invocable: true
---

# Oro

## Mode 1: Work a Task (`oro work`)

One agent executes one task end-to-end in a single execution tier. No dispatcher, no tmux, no swarm.

```bash
oro work <task-id>                              # work a task against default branch
oro work <task-id> --base-branch feature/auth   # target a specific branch
oro work <task-id> --timeout 20m                # extend timeout for complex tasks
```

**What happens:** Agent claims task → creates worktree → TDD implementation → quality gate (tests + lint + format) → ops review → merge to target branch → cleanup.

**When to use:** Default for single tasks. Say "use oro" or "oro work" to trigger this.

## Mode 2: Launch Swarm (`oro start`)

Full system: tmux session, dispatcher daemon, multiple parallel execution tiers.

```bash
oro start --workers 3 --detach                  # launch swarm (detached tmux)
oro start --workers 3 --base-branch feature/auth --detach  # swarm targets a branch
oro status                                      # check swarm state
oro attach                                      # connect to running swarm UI
ORO_HUMAN_CONFIRMED=1 oro stop --force          # shutdown (non-TTY safe)
```

**What happens:** Dispatcher polls `oro task ready` → assigns tasks to idle agents in isolated worktrees → quality gate → ops review → merge → next task. Agents loop until queue is empty or their tier context is exhausted (handoff to a fresh agent).

**When to use:** Multiple tasks to execute in parallel. Say "launch oro" to trigger this.

## Monitoring

After launching the swarm, **always set up monitoring** to catch stuck agents, saturated tiers, and tasks.

### Option A: In-session (`/watching-oro`)

Active babysitting — observe, detect defects, file tasks, fix, rebuild, relaunch. Use when you're actively working the swarm.

### Option B: Background cron (`/loop`)

Lightweight automated monitoring. Set up immediately after `oro start`:

```
/loop 5m <monitoring prompt>
```

The monitoring prompt should:

1. Run `oro status` and `oro logs --tail 100 | grep -v heartbeat | grep -v directive | grep -v missing_accept | tail -15`
2. Report agent state, tier, and task assignments
3. Detect and fix stuck states (see table below)
4. Report tasks completed since last check

### Stuck Detection

| Signal | Meaning | Auto-fix |
|--------|---------|----------|
| Agent idle + queue > 0 for 2+ checks | Assignment stuck | Check `oro task ready`, restart dispatcher |
| Same task on same agent for 3+ checks (>15min) | Agent stuck | Check tier context %, kill if >80% |
| `REJECTED` repeating >2x for same task | Agent can't pass review | Read rejection feedback, check if task AC is achievable |
| `QG_FAILED` repeating >3x for same task | Agent can't pass QG | Check QG output, may be flaky test vs real failure |
| `merge_failed` repeating for same task | Stale agent branch | Clean branch: `git branch -D agent/<task-id>`, reopen task |
| Task `IN_PROGRESS` but no agent assigned | Orphaned task | `oro task update <id> --status open` to re-queue |
| `progress_timeout` → re-assign → timeout loop | Task merged but not closed | Check if code is on main/epic branch, `oro task close <id>` manually |
| Agents idle, queue 0, tasks still open | All work done or blocked | Check `oro task ready` — if empty, epic may need closing |

### When to stop monitoring

- Queue empty + agents idle for 3+ consecutive checks → stop cron, stop swarm
- All target tasks/epic closed → stop cron, stop swarm

## Commands

| Command | Purpose |
|---------|---------|
| `oro work <task-id>` | Execute one task (lightweight, no dispatcher) |
| `oro start [--workers N] [--detach]` | Launch swarm |
| `oro stop` | Graceful shutdown |
| `oro status` | Dispatcher state, agents, tiers, active tasks |
| `oro attach` | Connect to tmux session |
| `oro logs [-f] [--tail N]` | Query event logs |
| `oro directive <op>` | Control dispatcher: `status`, `pause`, `resume`, `scale N` |
| `oro cleanup` | Clean stale state after crash |

Run `oro <command> --help` for flags.

## Branch Targeting

Both modes support `--base-branch`:
- **`oro work`**: `--base-branch feature/auth` — the agent branch starts from and merges to that branch
- **`oro start`**: `--base-branch feature/auth` — all tiers default to that branch (per-task override via `--metadata branch=X`)

Without `--base-branch`, defaults to current HEAD at startup (falls back to `main`).

## Philosophy

1. **Less Context, Better Work** — Agents see only their task's AC, relevant memories, and a clean worktree. Context is a tier budget: spend it on signal.
2. **Compound Learnings** — Every session leaves the system smarter. Agents emit learnings, the dispatcher extracts patterns, memory consolidation scores and surfaces them.
3. **Loop Until Done** — Tier context exhausted → handoff → fresh agent continues. Review fails → feedback → retry. Merge conflicts → ops agent resolves. No work is lost.
4. **Better Specs, Better Outcomes** — Spend tokens upstream: brainstorm alternatives, premortem designs, write validated specs before code.
5. **Guards Over Trust** — TDD, quality gate, ops review, evidence-based verification. Guards aren't overhead — they're what enable fearless execution.

## Architecture

```
oro work <task-id>           # lightweight — single execution tier, no dispatcher
  └─ agent process
       ├─ creates worktree
       ├─ TDD implementation
       ├─ quality gate
       ├─ ops review
       └─ merge + cleanup

oro start --workers 3        # full swarm
  └─ tmux session "oro"
       ├─ pane 0: manager (task triage, reviews)
       └─ panes 1+: agents (one per task)

  Dispatcher (background daemon)
    ├─ polls oro task ready for unblocked tasks
    ├─ assigns to idle agents in isolated worktrees
    ├─ runs quality gates (tests, lint, format)
    ├─ sends to ops review → merge to target branch
    └─ communicates via UDS (Unix domain sockets)
```

## Beadcraft Quick Reference

### Creating an Epic with Children

```bash
# 1. Create the epic
oro task create "Feature name" --type epic \
  --acceptance "All child tasks closed. Full quality gate passes." \
  --description "Goal from spec"

# 2. Create child tasks with full AC
oro task create "Specific task" --type task \
  --acceptance "Test: path:FnName | Cmd: test_cmd | Assert: expected
Read: file1.go:Symbol1, file2.go:Symbol2
Signature: func Name(ctx, arg) (Result, error)
Edges: nil input → ErrInvalid" \
  --estimate 7

# 3. Attach parent + wire dependency (order matters!)
oro task update <child-id> --parent <epic-id>
oro task dep add <epic-id> <child-id>

# 4. Target a branch (optional — epic children inherit)
oro task create "Feature name" --type epic \
  --metadata branch=feature/auth ...
```

`oro task create --parent` is also valid for hierarchy: parentage does not create dependency edges. Add `oro task dep add <epic-id> <child-id>` explicitly when the epic must wait for the child.

### Task Anatomy — Every task needs:

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

## Native Beadstore Recovery

**NEVER run `force-initialization commands`.** It destroys all task history. This has happened 3 times.

bd/Dolt is an import, audit, and rollback reference only. When native beadstore
errors occur, inspect the SQLite state directly, verify `oro task ready`,
`oro task blocked`, and `oro task show`, and follow
`docs/runbooks/beadstore-recovery.md` for backup or restore operations. Do not
restart or repair Dolt from Oro; the `oro dolt` operator commands are removed.

If the native store cannot be recovered with the reviewed runbook, **ask the
user**. Never nuke the database autonomously.

## Key Gotchas

See [gotchas.md](references/gotchas.md) for the full list.
