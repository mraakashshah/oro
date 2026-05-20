# Missing Epic Base During Worktree Add

**Date:** 2026-05-18
**Component:** dispatcher worktree creation
**Severity:** high

## Symptom

Quality gate retry worktree creation failed with:

```text
git -C /Users/as21/codehouse/oro worktree add <worktree> -b agent/oro-z0av-qg epic/oro-z0av: exit status 255
Preparing worktree (new branch 'agent/oro-z0av-qg')
fatal: not a valid object name: 'epic/oro-z0av'
```

## Investigation

The affected `oro-z0av` and `oro-z0av-qg` worktrees were absent, and the local
`epic/oro-z0av` branch was absent too. The failure reproduced at the worktree
manager boundary: when `git fetch origin epic/oro-z0av` fails and no local epic
branch exists, `git worktree add ... epic/oro-z0av` cannot resolve the base.

## Root Cause

Dispatcher assignment paths can lazily create missing epic branches before
normal worker worktree assignment, but direct `GitWorktreeManager.Create`
callers such as QG retry paths need the same protection. Without it, a missing
local-only `epic/<id>` base falls through to `git worktree add`, which fails
before the retry worker can start.

## Solution

`GitWorktreeManager.Create` preserves the existing remote-first behavior when
`git fetch origin <baseBranch>` succeeds. If fetch fails and the requested base
is an `epic/<id>` branch, it checks for the local branch and creates it from
`main` before running `git worktree add`.

Key references:

- `pkg/dispatcher/worktree_manager.go:66`
- `pkg/dispatcher/worktree_manager_test.go:563`

## Prevention

Keep missing epic branch protection at the worktree manager boundary, not only
in dispatcher assignment flow. Regression tests should simulate both failures:
missing remote epic ref and missing local epic branch before `git worktree add`.
