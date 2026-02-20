---
name: dispatching-parallel-agents
description: Use when facing 2 or more independent tasks that can be worked on without shared state or sequential dependencies
---

# Dispatching Parallel Agents

## Overview

Run a pipeline of N workers against a prioritized bead queue. Each worker executes one bead end-to-end via `oro work`. As workers complete, merge results and immediately launch the next bead.

**Core principle:** Keep N worker slots saturated. Merge as you go. Never wait for all workers to finish before starting more.

## When to Use

- Queue of 2+ independent beads to execute
- Multiple subsystems to build/fix in parallel
- Each bead can be understood without context from others

**Don't use when:**
- Failures are related (fix one might fix others)
- Need to understand full system state first
- Exploratory debugging (don't know what's broken yet)

## Preflight

Before dispatching, clean house:

```bash
git worktree list              # check for orphaned worktrees from previous sessions
bd ready                       # get prioritized queue
bd list --status=in_progress   # check for stale claims
```

Remove stale worktrees from previous sessions. Reset stale in_progress beads if the worker is gone.

## Priority Order

Process the queue in this order:

1. **Individual beads** (not epic children), bugs first
2. **Decomposed epic children**, bugs first then tasks
3. **New epics** needing decomposition

Within each tier: higher priority (P0 > P1 > P2) first.

## Scheduling Rules

- **Different files → parallel**: Launch simultaneously
- **Same file → sequential**: Wire deps with `bd dep add`
- **Default concurrency: 5 oro workers**

## The Pipeline

### 1. Launch Workers

Fill empty slots up to N concurrency:

**Primary — `oro work` (handles worktree, TDD, QG, ops review, merge):**

```bash
oro work <bead-id> &    # background, or use Task tool with run_in_background
```

**Fallback — Task agent (when work is not a bead, or oro unavailable):**

```
You are working in an isolated git worktree at: /absolute/path/.worktrees/<id>
Branch: agent/<id>

FIRST: bd update <id> --status=in_progress

## Task
[task description]

## Rules
- ONLY modify files within /absolute/path/.worktrees/<id>
- Run tests: go test -C /absolute/path/.worktrees/<id> ./pkg/... -race -count=1
- Commit your work with a descriptive message before completing
- Do NOT push, merge, or rebase
- Close bead: bd close <id> --reason="summary"
```

**IMPORTANT:** Tell agents to use `go -C <worktree>` or absolute paths, never `cd <worktree>`. The `cd` persists in the Bash tool cwd — if the worktree is later removed, ALL subsequent bash commands fail silently.

### 2. Monitor

- Run background agents with `run_in_background: true`
- **Never poll** — trust task completion notifications
- **Never use TaskOutput** to read full transcripts (70k+ tokens eats context)
- **Do NOT create TaskCreate/TodoWrite entries** to track agents — they go stale after compaction. Use `bd` for persistent tracking.
- Agent closes bead with `bd close <id>` — the bead IS the output

### 3. Merge (as each worker completes)

#### Happy Path (oro work auto-merged)

```bash
git pull --rebase    # pick up the worker's merge
```

#### oro work failed to merge / Task agent completed

Cherry-pick is the safe default when other worktrees are still active:

```bash
git stash
git worktree remove .worktrees/<id>
git cherry-pick agent/<id>
git branch -D agent/<id>
git stash pop
```

#### When all worktrees are removed (can rebase)

Rebase gives cleaner history but requires no active worktrees (rebase guard hook blocks otherwise):

```bash
git stash
git rebase main agent/<id>
git checkout main
git merge --ff-only agent/<id>
git branch -D agent/<id>
git stash pop
```

### 4. Backfill

Launch the next bead from the queue into the freed slot. Repeat until queue is empty.

Check dependency chains: if bead X was blocking bead Y (same file), Y is now unblocked.

### 5. Finalize

When all workers are done and merged:

```bash
go test ./...              # full test suite on main
git push                   # push when green
```

## Merge Strategy Details

### Fixing Agent Mistakes at Merge Time

Agents frequently make these errors — check before merging:

| Problem | Detection | Fix |
|---------|-----------|-----|
| **Didn't commit** | `git -C .worktrees/X log` shows same commit as main | Stage + commit from manager |
| **Syntax errors** | `go -C .worktrees/X build ./...` fails | Read the file, fix, recommit |
| **Wrong package name** | Linter rejects | Fix the package declaration, recommit |

**Always verify agent commits compile** before merging: `go -C .worktrees/X build ./...`

### Conflict Resolution

When `git rebase main agent/X` produces conflicts:

1. **Read the conflict markers** — understand what both sides changed
2. **Keep both changes** when they're additive (new fields, new methods, new tests)
3. **Prefer HEAD** for structural changes (type renames, package moves)
4. **Fix cross-references** after resolution
5. **Always build + test after conflict resolution** before merging the next branch

### bd File Contention

Multiple workers closing beads modify `.beads/issues.jsonl`. This is expected merge friction — take the latest version when cherry-picking. The file is auto-synced by hooks.

## Error Recovery

| Situation | Action |
|-----------|--------|
| Worker completes, merge succeeds | Pull. Launch next. |
| Worker completes, merge fails | Cherry-pick or rebase manually. |
| Worker fails (test failure) | Inspect worktree. Fix + recommit, or re-dispatch. |
| Worker killed (signal:killed) | Resource contention. Reduce concurrency. Re-dispatch. |
| Worker stuck (no progress) | Check output file tail. Kill and re-dispatch if needed. |
| Bead too large (worker decomposes) | Worker promotes to epic. Pick up children in next cycle. |

**Do NOT blindly retry failed workers.** Inspect first.

## Resource Limits

- **Default concurrency: 5 oro workers**
- Monitor for signal:killed — reduce to 3 if it occurs
- Task agents are lighter weight — can run more concurrently

## Task Agent Fallback

Use raw Task agents instead of `oro work` when:

- Work is not tracked as a bead (ad-hoc exploration, research)
- `oro` binary is unavailable or broken
- Need custom agent behavior beyond `oro work`'s lifecycle

Task agent prompt template must include:
- Absolute worktree path
- `bd update <id> --status=in_progress` at start
- `bd close <id>` at end
- Commit instructions (do NOT push/merge/rebase)

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Batch dispatch (wait for all, then merge all) | Pipeline: merge each as it finishes, backfill |
| Not claiming beads (`in_progress`) | Worker prompt must include `bd update` at start |
| Dispatching overlapping file scopes | Same file = sequential deps via `bd dep add` |
| Using TaskOutput to read transcripts | Trust notifications. Read bead, not transcript. |
| Rebasing with active worktrees | Cherry-pick instead |
| Polling agents with sleep loops | Trust task notifications |
| Forgetting stale worktree cleanup | Preflight: `git worktree list` |
| Using `cd` into worktrees | Shell cwd persists — use absolute paths |

## Red Flags

- All slots empty while queue has work (pipeline stall)
- Two workers editing the same file (merge conflict guaranteed)
- Worker running >10min on a 7min bead (check progress)
- signal:killed appearing (reduce concurrency)
- Saying "ready to push" (just push)
