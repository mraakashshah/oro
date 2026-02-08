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
- **Quality gate** — run tests/lint in the worktree
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
- Run tests: cd .worktrees/task-1 && [test_command]
- Do NOT push, merge, or rebase
- Close bead: bd close <id> --reason="summary"
```

### 3. Protect the Main Context

- Run background agents with `run_in_background: true`
- **Never poll** — trust task completion notifications
- Agent closes bead with `bd close <id> --reason="summary"` — the bead IS the output
- **Never use TaskOutput** to read full transcripts (70k+ tokens)

### 4. Merge Results

When all agents complete, sequentially merge each branch ff-only:

```bash
# For each agent branch:
git rebase main agent/task-1
git checkout main
git merge --ff-only agent/task-1

git rebase main agent/task-2
git checkout main
git merge --ff-only agent/task-2
```

If rebase conflicts: fail-fast, investigate, resolve manually.

### 5. Cleanup

```bash
git worktree remove .worktrees/task-1
git worktree remove .worktrees/task-2
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

## Red Flags

- Dispatching agents for related failures
- Agents without worktree isolation
- Agents merging or pushing (only manager does this)
- No test suite run on main after merge
- Trusting agent results without verification
