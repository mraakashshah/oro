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
oro bead ready                       # get prioritized queue
oro bead list --status=in_progress   # check for stale claims
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
- **Same file → sequential**: Wire deps with `oro bead dep add`
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

FIRST: oro bead update <id> --status=in_progress

## Task
[task description]

## Rules
- ONLY modify files within /absolute/path/.worktrees/<id>
- Run tests: go test -C /absolute/path/.worktrees/<id> ./pkg/... -race -count=1
- Commit your work with a descriptive message before completing
- Do NOT push, merge, or rebase
- Close bead: oro bead close <id> --reason="summary"
```

**IMPORTANT:** Tell agents to use `go -C <worktree>` or absolute paths, never `cd <worktree>`. The `cd` persists in the Bash tool cwd — if the worktree is later removed, ALL subsequent bash commands fail silently.

### 2. Monitor

- Run background agents with `run_in_background: true`
- **Never poll** — trust task completion notifications
- **Never use TaskOutput** to read full transcripts (70k+ tokens eats context)
- **Do NOT create TaskCreate/TodoWrite entries** to track agents — they go stale after compaction. Use `beads` for persistent tracking.
- Agent closes bead with `oro bead close <id>` — the bead IS the output

### 3. Merge (as each worker completes)

Rebase + fast-forward merge. Always. This gives clean linear history without duplicate commits.

```bash
git stash                                    # save dirty beads file
git worktree remove --force .worktrees/<id>  # remove worktree, keep branch
git rebase main <branch>                     # rebase onto current main
git checkout main                            # switch back to main
git merge --ff-only <branch>                 # fast-forward (no merge commit)
git branch -D <branch>                       # delete merged branch
git stash pop                                # restore beads state
```

**Why not cherry-pick?** Cherry-pick duplicates commit hashes, polluting `git log` and breaking `git bisect`. Rebase preserves authorship and produces identical diffs with clean history.

**Worktree removal unblocks rebase.** The rebase guard hook only fires when worktrees are active. Remove the worktree first (step 2), then rebase freely — even while other worktrees exist for different agents.

**CRITICAL: Do not backfill or use any tools between removing a worktree and completing the merge.** Removing a worktree can corrupt the hook resolver's CWD — all tools break until the merge sequence finishes. Complete the full 7-step sequence atomically.

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

## Error Recovery

| Situation | Action |
|-----------|--------|
| Worker completes, merge succeeds | Pull. Launch next. |
| Worker completes, merge conflicts | Resolve during rebase, build + test, continue. |
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
- `oro bead update <id> --status=in_progress` at start
- `oro bead close <id>` at end
- Commit instructions (do NOT push/merge/rebase)

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Batch dispatch (wait for all, then merge all) | Pipeline: merge each as it finishes, backfill |
| Not claiming beads (`in_progress`) | Worker prompt must include `oro bead update` at start |
| Dispatching overlapping file scopes | Same file = sequential deps via `oro bead dep add` |
| Using TaskOutput to read transcripts | Trust notifications. Read bead, not transcript. |
| Using tools between worktree remove and merge complete | Complete the 7-step merge atomically |
| Polling agents with sleep loops | Trust task notifications |
| Forgetting stale worktree cleanup | Preflight: `git worktree list` |
| Using `cd` into worktrees | Shell cwd persists — use absolute paths |

## Red Flags

- All slots empty while queue has work (pipeline stall)
- Two workers editing the same file (merge conflict guaranteed)
- Worker running >10min on a 7min bead (check progress)
- signal:killed appearing (reduce concurrency)
- Saying "ready to push" (just push)
