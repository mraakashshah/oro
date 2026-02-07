---
name: finishing-work
description: Use when implementation is complete and all tests pass -- guides verification, integration options, and cleanup
---

# Finishing Work

## Overview

Verify tests, present integration options, execute choice, clean up.

**Core principle:** Verify tests → Present options → Execute → Clean up.

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

### Step 2: Present Options

Present exactly these 4 options:

```
Implementation complete. What would you like to do?

1. Merge back to <base-branch> locally
2. Push and create a Pull Request
3. Keep the branch as-is (I'll handle it later)
4. Discard this work

Which option?
```

### Step 3: Execute Choice

**Option 1 — Merge Locally:**
```bash
git checkout <base-branch>
git pull
git merge <feature-branch>
# Verify tests on merged result
git branch -d <feature-branch>
```

**Option 2 — Push and Create PR:**
```bash
git push -u origin <feature-branch>
gh pr create --title "<title>" --body "$(cat <<'EOF'
## Summary
<2-3 bullets>

## Test Plan
- [ ] <verification steps>
EOF
)"
```

**Option 3 — Keep As-Is:**
Report: "Keeping branch. Worktree preserved."

**Option 4 — Discard:**
Confirm first — require typed "discard" confirmation.

### Step 4: Reflect

Before cleanup, briefly note friction encountered during this work:

- **What went off-script?** (unexpected failures, wrong assumptions, missing context)
- **What slowed you down?** (unclear requirements, tooling gaps, flaky tests)
- **What should change?** (skill updates, new rules, missing automation)

If genuinely clean run, say so — but clean runs should be rare. Most work has micro-friction worth capturing.

Log friction to the relevant `bd` issue notes or `docs/decisions-and-discoveries.md` if it's a broader insight.

### Step 5: Landing the Plane

After integration choice is executed:

1. **File issues** — Create `bd` entries for remaining/discovered work
2. **Quality gates** — Run tests, `ruff`/`golangci-lint`, formatters
3. **Commit** — Conventional Commits format
4. **Push** — `git pull --rebase && bd sync && git push`
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
- Merging without verifying tests on result
- Deleting work without confirmation
- Force-pushing without explicit request
- Saying "ready to push" instead of just pushing
