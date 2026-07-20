---
name: using-git-worktrees
description: Use when starting feature work that needs isolation from the current workspace
---

# Using Git Worktrees

## Overview

Git worktrees create isolated workspaces sharing the same repository. Work on multiple branches simultaneously without switching.

**Core principle:** Systematic directory selection + safety verification = reliable isolation.

## Directory Selection (Priority Order)

1. **Check existing directories:**
   ```bash
   ls -d .worktrees 2>/dev/null || ls -d worktrees 2>/dev/null
   ```
   If found: use it. If both exist, `.worktrees` wins.

2. **Check CLAUDE.md** for worktree directory preference.

3. **Ask the user** if nothing found.

## Safety Verification

For project-local directories, verify the directory is gitignored:

```bash
git check-ignore -q .worktrees 2>/dev/null
```

**If NOT ignored:** Add to `.gitignore` and commit before proceeding.

## Creation Steps

### 1. Create Worktree

```bash
project=$(basename "$(git rev-parse --show-toplevel)")
git worktree add .worktrees/$BRANCH_NAME -b $BRANCH_NAME
cd .worktrees/$BRANCH_NAME
```

### 2. Copy Environment Files

```bash
main_root=$(git rev-parse --show-toplevel)
for env in .env .env.local .env.test; do
  [ -f "$main_root/$env" ] && cp "$main_root/$env" .
done
```

### 3. Run Project Setup

```bash
# Go
if [ -f go.mod ]; then go mod download; fi

# Python
if [ -f pyproject.toml ]; then uv sync; fi

# Node
if [ -f package.json ]; then npm install; fi
```

### 4. Verify Clean Baseline

```bash
go test ./...      # Go
uv run pytest      # Python
```

If tests fail: report failures, ask whether to proceed.

### 5. Report

```
Worktree ready at <full-path>
Tests passing (<N> tests, 0 failures)
Ready to implement <feature-name>
```

## Worktree Removal (CRITICAL)

Removing a worktree while an active tool's working directory is inside it can break subsequent shell calls; recovery may require restarting the session.

**Mandatory sequence:**

1. Resolve and record the absolute main repo root without removing anything.
2. For every remaining shell or tool call, explicitly set its working directory to that recorded main repo root.
3. In a separate call configured with the main-root working directory, run `pwd` and verify the result.
4. In another call with that same working directory, run `git worktree remove <worktree-path>`.
5. Still using the main-root working directory, run `git worktree prune`, then `pwd` to verify shell calls still work.

**Rules:**
- NEVER chain worktree removal with other commands (`&&`, `;`)
- NEVER remove a worktree from inside it or any of its subdirectories
- NEVER assume a `cd` in one tool call changes the next call's working directory
- ALWAYS verify shell calls work after removal before continuing
- A PreToolUse hook (`worktree_guard.py`) will block dangerous removals, but don't rely on it — follow the sequence above

**If shell calls fail anyway:** Move to a known-safe working directory if the platform allows it; otherwise restart the session.

## Quick Reference

| Situation | Action |
|-----------|--------|
| `.worktrees/` exists | Use it (verify ignored) |
| `worktrees/` exists | Use it (verify ignored) |
| Neither exists | Check CLAUDE.md → ask user |
| Directory not ignored | Add to .gitignore + commit |
| Tests fail at baseline | Report + ask |
| Removing a worktree | Set each tool call's working directory to the main root, then remove |

## Red Flags

- Creating worktree without verifying it's gitignored
- Skipping baseline test verification
- Proceeding with failing tests without asking
- Assuming directory location when ambiguous
- Running `git worktree remove` without explicitly using the main-root working directory
- Chaining worktree removal with other commands
