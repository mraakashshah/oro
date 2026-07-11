---
name: finishing-work
description: Use when implementation is complete and all tests pass -- guides verification, integration options, and cleanup
---

# Finishing Work

## Overview

Verify tests, detect the workspace, present integration options, execute the choice, clean up, then land the plane.

**Core principle:** Verify tests → Detect workspace → Present options → Execute → Clean up → Land.

## Steps

### Step 1: Verify Tests

**Before presenting options, verify tests pass:**

```bash
# Go
go test ./...

# Python
uv run pytest
```

If tests fail: fix first. Don't proceed to Step 2.

### Step 2: Detect Workspace

**Before showing a menu, figure out what kind of workspace you're in.** oro is worktree-heavy and workers run in detached or externally-managed workspaces, so the menu and cleanup depend on this.

```bash
GIT_DIR=$(git -C "$(git rev-parse --show-toplevel)" rev-parse --git-dir)
GIT_COMMON=$(git -C "$(git rev-parse --show-toplevel)" rev-parse --git-common-dir)
```

Compare the two (resolve to absolute paths before comparing):

| State | Menu | Cleanup |
|-------|------|---------|
| `GIT_DIR == GIT_COMMON` (normal repo) | Standard 4 options | No worktree to clean up |
| `GIT_DIR != GIT_COMMON`, on a named branch | Standard 4 options | Provenance-based (Step 5) |
| `GIT_DIR != GIT_COMMON`, detached HEAD | Reduced 3 options (no local merge) | None — externally managed |

A detached HEAD in a worktree means the workspace is externally managed (a harness/worker checkout, not a branch you own). Don't offer a local merge and don't remove it.

### Step 3: Present Options

**Normal repo or named-branch worktree — present exactly these 4 options:**

```
Implementation complete. What would you like to do?

1. Merge back to <base-branch> locally
2. Push and create a Pull Request
3. Keep the branch as-is (I'll handle it later)
4. Discard this work

Which option?
```

**Detached HEAD (externally-managed workspace) — present exactly these 3 options:**

```
Implementation complete. You're on a detached HEAD (externally managed workspace).

1. Push as a new branch and create a Pull Request
2. Keep as-is (I'll handle it later)
3. Discard this work

Which option?
```

Keep options concise — no extra explanation.

### Step 4: Execute Choice

**Option 1 — Merge Locally:**
```bash
git checkout <base-branch>
git pull
git merge <feature-branch>
# Verify tests on merged result BEFORE removing anything
```
Only after the merge succeeds and tests pass: clean up the worktree (Step 5), then delete the branch:
```bash
git branch -d <feature-branch>
```
Order matters — remove the worktree before `git branch -d`, or the delete fails because the worktree still references the branch.

**Option 2 — Push and Create PR:**
```bash
git push -u origin <feature-branch>
```
Detect the remote host before assuming a tool — oro projects are not always on GitHub:
```bash
REMOTE_URL=$(git remote get-url origin)
```
- `github.com` → `gh pr create --title "<title>" --body "<body>"`
- `gitlab.*`   → `glab mr create --title "<title>" --description "<body>"`
- unknown host → open the compare URL printed by `git push`, or ask the user

GitHub body template:
```bash
gh pr create --title "<title>" --body "$(cat <<'EOF'
## Summary
<2-3 bullets>

## Test Plan
- [ ] <verification steps>
EOF
)"
```
**Do NOT clean up the worktree** — the user needs it alive to iterate on PR feedback.

**Option 3 — Keep As-Is:**
Report: "Keeping branch <name>. Worktree preserved at <path>." Don't clean up the worktree.

**Option 4 — Discard:**
Confirm first — show what will be deleted (branch, commit list, worktree path) and require a typed `discard` before doing anything. Then clean up the worktree (Step 5) and force-delete the branch:
```bash
git branch -D <feature-branch>
```

### Step 5: Cleanup Workspace

**Only runs for Options 1 and 4.** Options 2 and 3 always preserve the worktree.

Decide ownership by provenance — only remove worktrees oro created under `.worktrees/` (or `worktrees/`):

- **`GIT_DIR == GIT_COMMON`** (normal repo): nothing to remove. Done.
- **Worktree path is under `.worktrees/` / `worktrees/`**: oro owns it — remove it.
- **Anything else** (detached HEAD, harness-owned checkout, worktree outside `.worktrees/`): leave it in place. If your platform provides a workspace-exit tool, use it; otherwise do nothing.

**Removing an oro-owned worktree — follow oro's mandatory sequence (see `using-git-worktrees`, "Worktree Removal (CRITICAL)"):**

Removing a worktree while the shell cwd is inside it permanently kills the Bash tool (Claude Code bug #9190) — recovery requires a session restart. The `worktree_guard.py` PreToolUse hook will block dangerous removals, but don't rely on it. Follow the sequence exactly:

```bash
# 1. ALWAYS cd to the MAIN repo root FIRST (never chain this with removal)
cd "$(git -C "$(git rev-parse --git-common-dir)/.." rev-parse --show-toplevel)"
```
```bash
# 2. THEN remove the worktree (its own command — never chained with && or ;)
git worktree remove <worktree-path>
```
```bash
# 3. Prune stale registrations, then verify bash still works
git worktree prune
echo "bash ok"
```

Rules (consistent with `using-git-worktrees`):
- NEVER chain worktree removal with other commands (`&&`, `;`)
- NEVER run `git worktree remove` from inside the worktree or a subdirectory
- ALWAYS verify bash works after removal before continuing
- Run `git worktree prune` after removal to self-heal stale registrations

### Step 6: Reflect and Document

Before wrapping up, briefly note friction encountered during this work:

- **What went off-script?** (unexpected failures, wrong assumptions, missing context)
- **What slowed you down?** (unclear requirements, tooling gaps, flaky tests)
- **What should change?** (skill updates, new rules, missing automation)
- **Were you corrected?** If the user corrected you on something generalizable, propose a skill or rule edit so it doesn't recur.

If genuinely clean run, say so — but clean runs should be rare. Most work has micro-friction worth capturing.

Log friction to the relevant `tasks` issue notes or `~/.oro/projects/<name>/decisions&discoveries.md` if it's a broader insight.

**Then invoke `review-docs`** to check that documentation still matches the code that just landed. Stale docs are worse than no docs.

**Then invoke `documenting-solutions`** to capture any non-trivial problems solved during this work. This is not optional — if you learned something worth knowing next time, document it.

### Step 7: Landing the Plane

After the integration choice is executed:

1. **File issues** — Create `tasks` entries for remaining/discovered work
2. **Quality gates** — Run `./quality_gate.sh` (Go) or `uv run pytest && ruff check . && ruff format --check .` (Python)
3. **Commit** — Conventional Commits format
4. **Push** — `git pull --rebase && git push` (pre-commit hook auto-syncs tasks)
5. **Verify** — `git status` shows "up to date with origin"

## Quick Reference

| Option | Merge | Push | Keep Worktree | Cleanup Branch |
|--------|-------|------|---------------|----------------|
| 1. Merge | Yes | - | - | Yes |
| 2. PR | - | Yes | Yes | - |
| 3. Keep | - | - | Yes | - |
| 4. Discard | - | - | - | Yes (force) |

## Red Flags

- Proceeding with failing tests
- Merging without verifying tests on the merged result
- Deleting work without a typed `discard` confirmation
- Force-pushing without explicit request
- Saying "ready to push" instead of just pushing
- Offering a local merge on a detached HEAD (externally-managed workspace)
- Using `gh pr create` without checking `git remote get-url origin` first — the repo may not be on GitHub
- Removing a worktree oro didn't create (must live under `.worktrees/` / `worktrees/`)
- Removing a worktree before the merge is confirmed successful
- Running `git worktree remove` from inside the worktree, or chaining it with `&&` / `;`
- Deleting the branch before removing the worktree that references it
