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

### 2. Run Project Setup

```bash
# Go
if [ -f go.mod ]; then go mod download; fi

# Python
if [ -f pyproject.toml ]; then uv sync; fi

# Node
if [ -f package.json ]; then npm install; fi
```

### 3. Verify Clean Baseline

```bash
go test ./...      # Go
uv run pytest      # Python
```

If tests fail: report failures, ask whether to proceed.

### 4. Report

```
Worktree ready at <full-path>
Tests passing (<N> tests, 0 failures)
Ready to implement <feature-name>
```

## Quick Reference

| Situation | Action |
|-----------|--------|
| `.worktrees/` exists | Use it (verify ignored) |
| `worktrees/` exists | Use it (verify ignored) |
| Neither exists | Check CLAUDE.md → ask user |
| Directory not ignored | Add to .gitignore + commit |
| Tests fail at baseline | Report + ask |

## Red Flags

- Creating worktree without verifying it's gitignored
- Skipping baseline test verification
- Proceeding with failing tests without asking
- Assuming directory location when ambiguous
