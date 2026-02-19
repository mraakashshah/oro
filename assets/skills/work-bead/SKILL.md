---
name: work-bead
description: Use when picking up a bead to execute end-to-end — from claim through worktree, TDD, merge to main, and cleanup
---

# Work Bead

## Overview

End-to-end workflow for executing exactly one bead in isolation. Uses a git worktree for safety, TDD for correctness, and fast-forward merge for clean history on main.

**Core principle:** 1 invocation = 1 bead, worktree-isolated, rebased + fast-forward merged to main.

## Workflow

### Step 1: PICK

```bash
bd ready                              # find unblocked work
bd show <id>                          # review details + acceptance
bd update <id> --status in_progress   # claim it
```

If `bd ready` returns nothing: report "No beads ready." STOP.

### Step 2: WORKTREE

Create an isolated workspace for this bead:

```bash
git worktree add .worktrees/bead-<id> -b bead/<id>
cd .worktrees/bead-<id>
```

Copy environment files:

```bash
main_root=$(git rev-parse --show-toplevel)
for env in .env .env.local .env.test; do
  [ -f "$main_root/$env" ] && cp "$main_root/$env" .
done
```

Run project setup:

```bash
# Python
if [ -f pyproject.toml ]; then uv sync; fi

# Go
if [ -f go.mod ]; then go mod download; fi

# Node
if [ -f package.json ]; then npm install; fi
```

Verify baseline tests pass:

```bash
# Go: go test ./...
# Python: uv run pytest
```

If baseline tests fail: report failures, ask whether to proceed.

### Step 3: PARSE

Extract the verification contract from the bead's `--acceptance` field:

```
Test: <path>:<FnName> | Cmd: <test_cmd> | Assert: <expected>
```

| Field | Meaning |
|-------|---------|
| `Test:` | Test file path and function name |
| `Cmd:` | Command to run verification |
| `Assert:` | What "pass" looks like |

If acceptance is missing or vague:
- `bd update <id> --notes "Blocked: unclear acceptance criteria"`
- Ask user for clarification. STOP.

### Step 4: RED

Write the failing test specified in acceptance criteria. Run the verification command. Confirm failure.

```bash
# Go
go test ./path/to/... -run TestFnName -v

# Python
uv run pytest path/to/test_file.py::test_fn_name -v
```

**Verify:** Test fails for the expected reason (missing feature, not a typo).

**If bead is too large** (see `bead-craft` size heuristics): **DECOMPOSE AND STOP** (see Mid-Bead Decomposition below).

### Step 5: GREEN

Write the simplest code that makes the test pass. Run the verification command from acceptance.

**Verify:** Test passes. No other tests broken.

### Step 6: REFACTOR

Clean up while tests stay green. No new behavior.

### Step 7: GATE

Run the project quality gate:

```bash
# Go projects
./quality_gate.sh

# Python projects
uv run pytest && ruff check . && ruff format --check .
```

Fix any issues. Re-run until clean. Never skip the gate.

### Step 8: COMMIT

One atomic commit per bead. Include implementation and tests together.

```bash
git add <relevant files>
git commit -m "<type>(<scope>): <desc> (bd-<id>)"
```

### Step 9: CLOSE

```bash
bd close <id> --reason "Tests pass, gate clean. Commit: <hash>"
```

### Step 10: MERGE — Rebase in-place

Rebase the agent branch onto main inside the worktree (bypasses the worktree guard hook):

```bash
git -C .worktrees/bead-<id> rebase main
```

If rebase conflict: resolve in the worktree, `git rebase --continue`, re-run gate.

### Step 11: REMOVE WORKTREE

The worktree must be clean after rebase before it can be removed:

```bash
git worktree remove .worktrees/bead-<id>
```

If worktree is dirty after rebase: `git -C .worktrees/bead-<id> commit --amend` to fold changes in, then retry removal.

### Step 12: FAST-FORWARD MERGE

Fast-forward main to the rebased branch tip (same commit hashes, clean linear history):

```bash
git merge --ff-only bead/<id>
```

If `--ff-only` fails (main moved since rebase): re-run Step 10 rebase, then retry.

### Step 13: PUSH

```bash
git push
```

Note: `bd sync --flush-only` is not needed here — the pre-commit hook runs it automatically on every commit.

If push fails (no remote): report. Commit is local.

### Step 14: CLEANUP

```bash
git branch -d bead/<id>
```

## Mid-Bead Decomposition

If during RED the bead needs multiple unrelated tests:

1. Discard uncommitted work in worktree
2. `bd update <id> --type epic --notes "Decomposed: needed multiple unrelated tests"`
3. Create child beads with `--parent <id>`, wire deps with `bd dep add`
4. Remove worktree: `git worktree remove .worktrees/bead-<id>`
5. Delete branch: `git checkout main && git branch -D bead/<id>`
6. **STOP.** Report what was decomposed. Next invocation picks up a child.

**Too-large signals:** See `bead-craft` size heuristics for the full list.

## Error Handling

| Situation | Action |
|-----------|--------|
| `bd ready` returns nothing | Report "No beads ready." STOP. |
| Acceptance missing/vague | `bd update <id> --notes "Blocked: unclear acceptance"`. Ask user. STOP. |
| Baseline tests fail in worktree | Report failures. Ask whether to proceed. |
| Test won't fail (RED) | Testing existing behavior. Fix test. |
| Quality gate fails | Fix issues. Re-run. Never skip. |
| Merge conflict (rebase) | Resolve in worktree, `git rebase --continue`, re-run gate. |
| `--ff-only` fails (main moved) | Re-run Step 10 rebase from inside worktree, then retry `--ff-only`. |
| Worktree dirty after rebase | `git -C .worktrees/bead-<id> commit --amend` in worktree, then remove. |
| Push fails (no remote) | Report. Commit is local. |

## Red Flags

- Skipping the RED step (writing code before a failing test)
- Closing a bead without a passing quality gate
- Multiple beads in one commit
- Proceeding with failing baseline tests without asking
- Continuing after discovering bead is too large (decompose and stop instead)
- Skipping worktree cleanup
