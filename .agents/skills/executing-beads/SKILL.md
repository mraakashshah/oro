---
name: executing-beads
description: Use when executing tracked tasks through the full implementation lifecycle
---

# Executing Tasks

## Overview

Execute one task at a time through a full TDD cycle. Each closed task produces one atomic, tested commit. This replaces `executing-plans` when work is tracked as tasks.

**Core principle:** 1 task = 1 TDD cycle = 1 atomic commit.

## Per-Task Cycle

### Step 1: Pick

```bash
oro task ready                              # find unblocked work
oro task show <id>                          # review details + acceptance
oro task update <id> --status in_progress   # claim it
```

### Step 2: Parse Acceptance

Extract the verification contract from the task's `--acceptance` field:

```
Test: <path>:<FnName> | Cmd: <test_cmd> | Assert: <expected>
```

| Field | Meaning |
|-------|---------|
| `Test:` | Test file path and function name |
| `Cmd:` | Command to run verification |
| `Assert:` | What "pass" looks like |

If acceptance is missing or vague: STOP. Update the task with concrete acceptance before proceeding.

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

### Step 5b: Integration Side-Effect Check

Before declaring implementation complete, pause and ask these questions about the code you just changed:

| Question | What to do |
|----------|------------|
| What fires when this runs? | Read actual code for callbacks, middleware, hooks, event handlers triggered by your change |
| Do my tests exercise the real chain? | Verify integration tests use real objects, not mocks that hide side-effects |
| Can failure leave orphaned state? | Trace the failure path — partial writes, leaked goroutines, unclosed resources |
| What other interfaces expose this? | Grep for the changed function/type in CLI commands, API handlers, worker prompts |
| Do error strategies align? | Check that error types at each layer are consistent (don't wrap then unwrap) |

Skip this check for trivial changes (rename, docs, config). Apply for any change touching shared interfaces, error handling, or cross-package boundaries.

### Step 5c: Spec Check

Invoke `review-implementation` against the task's acceptance criteria and description. Confirm every requirement is met before proceeding to the quality gate.

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

One commit per task. Include implementation and tests together.

```bash
git add <relevant files>
git commit -m "<type>(<scope>): <desc> (oro-<id>)"
```

**On feature branches:** Intermediate commits during TDD are fine. Squash to one atomic commit when closing the task.

**On main (no branch):** Produce a single commit at this step.

### Step 8: Close

```bash
oro task close <id> --reason "Tests pass, gate clean. Commit: <hash>"
```

### Step 9: Context Checkpoint

After every task closure, run `context-checkpoint` skill logic:

| Message Pairs | Zone | Action |
|---------------|------|--------|
| 0-10 | Green | Continue to next task |
| 11-15 | Yellow | Finish current, then evaluate |
| 16-20 | Orange | Do NOT start new task. Handoff now. |
| 20+ | Red | Stop immediately. Emergency handoff. |

Green → return to Step 1. Otherwise → handoff via `create-handoff` skill.

## Mid-Task Decomposition

If during Step 3 the task needs multiple unrelated tests:

1. **STOP** — do not continue implementation
2. Promote: `oro task update <id> --type epic`
3. Create children: `oro task create --type task --parent <id> --acceptance "..." --estimate <min>`, then `oro task dep add <id> <child>` for each child that must finish before the parent
4. Wire dependencies: `oro task dep add` where ordering matters
5. Return to Step 1 with the first child task

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
| Blocked by another task | `oro task ready` to find a different task. Note the blocker. |
| Acceptance criteria unclear | STOP. `oro task update <id> --notes "Blocked: unclear acceptance"`. Ask user. |

## Red Flags

- Skipping the RED step (writing code before a failing test)
- Closing a task without a passing quality gate
- Multiple tasks in one commit
- Proceeding with failing tests
- Ignoring context checkpoint signals
- Writing implementation before parsing acceptance criteria
