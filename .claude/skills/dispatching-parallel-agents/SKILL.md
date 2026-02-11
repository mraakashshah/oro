---
name: dispatching-parallel-agents
description: Use when facing 2 or more independent tasks that can be worked on without shared state or sequential dependencies
---

# Dispatching Parallel Agents

## Overview

Dispatch one agent per independent problem. Each agent gets its own git worktree, commits on a branch, and the manager merges ff-only to main.

**Core principle:** Every agent gets a worktree. Every agent commits. No agent merges or pushes.

## When to Use

- 2+ tasks with different root causes or independent scope
- Multiple subsystems to build/fix independently
- Each problem can be understood without context from others

**Don't use when:**
- Failures are related (fix one might fix others)
- Need to understand full system state first
- Exploratory debugging (don't know what's broken yet)

## The Pattern

### 1. Create Worktrees

Before dispatching, create one worktree + branch per agent:

```bash
git worktree add .worktrees/task-1 -b agent/task-1
git worktree add .worktrees/task-2 -b agent/task-2
```

### 2. Dispatch Agents to Worktrees

Each agent gets:
- **Worktree path** — work exclusively in this directory
- **Specific scope** — one subsystem or task
- **Commit instructions** — commit on the branch before completing
- **Quality gate** — run tests AND full linter (golangci-lint/ruff) in the worktree
- **Bead closure** — `bd close <id> --reason="summary"`

Use the Task tool with multiple calls in a single message, `run_in_background: true`.

**Agent prompt template:**

```
You are working in an isolated git worktree at: .worktrees/task-1
Branch: agent/task-1

## Task
[task description]

## Rules
- ONLY modify files within .worktrees/task-1
- Commit your work with a descriptive message before completing
- Run tests: go -C .worktrees/task-1 test -race -count=1 ./pkg/... -v
- Do NOT push, merge, or rebase
- Close bead: bd close <id> --reason="summary"
```

**IMPORTANT:** Tell agents to use `go -C <worktree>` instead of `cd <worktree> && go test`. The `cd` persists in the Bash tool cwd — if the worktree is later removed, ALL subsequent bash commands fail silently. Use absolute paths everywhere.

### 3. Protect the Main Context

- Run background agents with `run_in_background: true`
- **Never poll** — trust task completion notifications
- Agent closes bead with `bd close <id> --reason="summary"` — the bead IS the output
- **Never use TaskOutput** to read full transcripts (70k+ tokens)
- **Do NOT create TaskCreate/TodoWrite entries** to track agents — they go stale after compaction. Use `bd` for persistent tracking.

### 4. Merge Results

**Remove ALL worktrees before rebasing** — the rebase guard hook blocks `git rebase` when any branch is checked out in a worktree.

```bash
# 1. Stash beads changes (bd operations modify .beads/issues.jsonl)
git stash

# 2. Remove ALL worktrees (even for still-running agents if needed)
git worktree remove .worktrees/task-1
git worktree remove .worktrees/task-2
# If worktree has untracked files: git worktree remove --force .worktrees/task-X

# 3. Merge in order (see Merge Ordering below)
git rebase main agent/task-1
git checkout main
git merge --ff-only agent/task-1

# 4. Repeat for each branch, then restore stash
git stash pop
```

#### Merge Ordering Strategy

Order matters. Merge in this sequence to minimize conflicts:

1. **New files only** (no existing file modifications) — zero conflict risk
2. **Small, isolated packages** — low conflict risk
3. **Mechanical/formatting changes** — medium risk but easy to resolve
4. **Cross-cutting changes** (interface renames, type moves) — merge before dependents
5. **Large structural refactors** (dispatcher.go, big test files) — highest conflict risk, merge last

Within the same risk tier, merge smaller changesets first.

#### Fixing Agent Mistakes at Merge Time

Agents frequently make these errors — check before merging:

| Problem | Detection | Fix |
|---------|-----------|-----|
| **Didn't commit** | `git -C .worktrees/X log` shows same commit as main | Stage + commit from manager: `git -C .worktrees/X add . && git -C .worktrees/X commit -m "..."` |
| **Syntax errors** | Diagnostics show compile errors in worktree | Read the file, fix with Edit tool, then commit |
| **Wrong package name** | Linter rejects (e.g., `package foo` should be `package foo_test`) | Fix the package declaration, recommit |
| **Extra closing braces** | Common when replacing `time.Sleep` blocks with `waitFor` closures — leaves orphan `}` | Search for pattern: `}, timeout)\n\t}\n\n\t//` and remove extra `}` |

**Always verify agent commits compile** before merging: `go -C .worktrees/X build ./...`

#### Conflict Resolution

When `git rebase main agent/X` produces conflicts:

1. **Read the conflict markers** — understand what both sides changed
2. **Keep both changes** when they're additive (new fields, new methods, new tests)
3. **Prefer HEAD** for structural changes (type renames, package moves) that later branches should adopt
4. **Fix cross-references** after resolution — e.g., if branch A moved `WorkerState` to `protocol.WorkerState`, branch B's code needs updating post-merge
5. **Always build + test after conflict resolution** before merging the next branch

### 5. Cleanup

```bash
git branch -d agent/task-1 agent/task-2
```

Run full test suite on main. Push when green.

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Too broad ("fix all tests") | Scope to one file/subsystem |
| No worktree path in prompt | Agent must know where to work |
| No commit instruction | Agent must commit before completing |
| Polling agents with sleep loops | Trust system reminders |
| Using TaskOutput for results | Read bead annotations instead |
| Using `cd` into worktrees | Shell cwd persists — use `go -C` or absolute paths |
| Creating TaskCreate entries for agents | They go stale after compaction — use bd |
| Merging before removing worktrees | Rebase guard hook blocks — remove first |
| Dispatching overlapping file scopes | Two agents editing dispatcher_test.go = guaranteed merge conflict |

## Red Flags

- Dispatching agents for related failures
- Agents without worktree isolation
- Agents merging or pushing (only manager does this)
- No test suite run on main after merge
- Trusting agent results without verification
- Two agents modifying the same large file (split by package instead)
