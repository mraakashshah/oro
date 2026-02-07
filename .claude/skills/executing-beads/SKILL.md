---
name: executing-beads
description: Use when work is tracked as beads — executes one bead at a time through the full TDD-commit-verify lifecycle
---

# Executing Beads

## Overview

Execute one bead at a time through a full TDD cycle. Each closed bead produces one atomic, tested commit. This replaces `executing-plans` when work is tracked as beads.

**Core principle:** 1 bead = 1 TDD cycle = 1 atomic commit.

## Per-Bead Cycle

### Step 1: Pick

```bash
bd ready                              # find unblocked work
bd show <id>                          # review details + acceptance
bd update <id> --status in_progress   # claim it
```

### Step 2: Parse Acceptance

Extract the verification contract from the bead's `--acceptance` field:

```
Test: <path>:<FnName> | Cmd: <test_cmd> | Assert: <expected>
```

| Field | Meaning |
|-------|---------|
| `Test:` | Test file path and function name |
| `Cmd:` | Command to run verification |
| `Assert:` | What "pass" looks like |

If acceptance is missing or vague: STOP. Update the bead with concrete acceptance before proceeding.

### Step 3: RED — Write Failing Test

Write the test specified in acceptance criteria. Run the verification command. Confirm failure.

```bash
# Go
go test ./path/to/... -run TestFnName -v

# Python
uv run pytest path/to/test_file.py::test_fn_name -v
```

**Verify:** Test fails for the expected reason (missing feature, not a typo).

### Step 4: GREEN — Minimal Implementation

Write the simplest code that makes the test pass.

```bash
# Run verification command from acceptance
<Cmd from acceptance>
```

**Verify:** Test passes. No other tests broken.

### Step 5: REFACTOR

Clean up while tests stay green. No new behavior.

### Step 6: Quality Gate

Run the project quality gate:

```bash
# Go projects
./quality_gate.sh

# Python projects
uv run pytest && ruff check . && ruff format --check .
```

Fix any issues before proceeding.

### Step 7: Atomic Commit

One commit per bead. Include implementation and tests together.

```bash
git add <relevant files>
git commit -m "<type>(<scope>): <desc> (bd-<id>)"
```

**On feature branches:** Intermediate commits during TDD are fine. Squash to one atomic commit when closing the bead.

**On main (no branch):** Produce a single commit at this step.

### Step 8: Close

```bash
bd close <id> --reason "Tests pass, gate clean. Commit: <hash>"
```

### Step 9: Context Checkpoint

After every bead closure, run `context-checkpoint` skill logic:

| Message Pairs | Zone | Action |
|---------------|------|--------|
| 0-10 | Green | Continue to next bead |
| 11-15 | Yellow | Finish current, then evaluate |
| 16-20 | Orange | Do NOT start new bead. Handoff now. |
| 20+ | Red | Stop immediately. Emergency handoff. |

Green → return to Step 1. Otherwise → handoff via `create-handoff` skill.

## Mid-Bead Decomposition

If during Step 3 the bead needs multiple unrelated tests:

1. **STOP** — do not continue implementation
2. Promote: `bd update <id> --type epic`
3. Create children: `bd create --parent <id> --type task --acceptance "..." --estimate <min>` for each piece
4. Wire dependencies: `bd dep add` where ordering matters
5. Return to Step 1 with the first child bead

**Too-large signals:**
- Needs multiple unrelated assertions
- Estimate >7 minutes
- Touches >4 source files
- Title contains "and" (two things)

## Error Handling

| Situation | Action |
|-----------|--------|
| Test won't fail (Step 3) | You're testing existing behavior. Fix the test. |
| Test errors instead of fails | Fix the error (imports, syntax), re-run until proper failure. |
| Quality gate fails (Step 6) | Fix issues. Don't skip the gate. |
| Blocked by another bead | `bd ready` to find a different bead. Note the blocker. |
| Acceptance criteria unclear | STOP. `bd update <id> --notes "Blocked: unclear acceptance"`. Ask user. |

## Red Flags

- Skipping the RED step (writing code before a failing test)
- Closing a bead without a passing quality gate
- Multiple beads in one commit
- Proceeding with failing tests
- Ignoring context checkpoint signals
- Writing implementation before parsing acceptance criteria
