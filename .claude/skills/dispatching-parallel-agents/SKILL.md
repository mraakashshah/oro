---
name: dispatching-parallel-agents
description: Use when facing 2 or more independent tasks that can be worked on without shared state or sequential dependencies
---

# Dispatching Parallel Agents

## Overview

When you have multiple unrelated problems, investigating them sequentially wastes time. Dispatch one agent per independent problem domain.

**Core principle:** One agent per independent problem. Let them work concurrently.

## When to Use

**Use when:**
- 2+ tasks with different root causes
- Multiple subsystems broken independently
- Each problem can be understood without context from others
- No shared state between investigations

**Don't use when:**
- Failures are related (fix one might fix others)
- Need to understand full system state first
- Agents would edit same files (conflicts)
- Exploratory debugging (don't know what's broken yet)

## The Pattern

### 1. Identify Independent Domains

Group by what's broken:
- Domain A: Authentication flow
- Domain B: Data processing pipeline
- Domain C: API response formatting

### 2. Create Focused Agent Tasks

Each agent gets:
- **Specific scope** — one subsystem or test file
- **Clear goal** — make these tests pass / fix this behavior
- **Constraints** — don't change other code
- **Quality gate** — agent must run `./quality_gate.sh` before committing and fix any failures
- **Bead closure** — agent closes bead with `bd close <id> --reason="summary"` (no output files)

### 3. Dispatch in Parallel

Use the Task tool with multiple calls in a single message:
```
Task("Fix auth_test.go failures — timing issues in token refresh")
Task("Fix pipeline_test.py failures — data transformation errors")
Task("Fix api_test.go failures — response format mismatch")
```

**Batch size:** max 15 parallel agents. Beyond that, diminishing returns.

### 4. Protect the Main Context

**Agent transcripts can be 70k+ tokens.** Keep them out of your context window.

- Run background agents with `run_in_background: true`
- **Never poll** with sleep loops — trust system reminders and task completion notifications
- Continue working on other tasks while agents run
- Task completion notifications provide summaries — usually sufficient

**Reporting results — use the project's existing channels, not output files:**

- **If using beads:** Agent closes the bead with `bd close <id> --reason="summary"`. The bead IS the output record. Don't create `docs/agent-output-*.md` files — they accumulate as debt.
- **If no issue tracker:** Agent can write to a summary file as a fallback.
- **Never use TaskOutput** to read full transcripts — floods your context with 70k+ tokens.

```
# RIGHT — agent closes bead, main reads completion notification
Task(run_in_background=true, prompt="... Close bead with bd close <id> --reason='summary'")

# ALSO RIGHT (no issue tracker) — agent writes small summary file
Task(run_in_background=true, prompt="... Write results to /tmp/agent-summary.md")

# WRONG — dumps full transcript into main context
TaskOutput(task_id="...")  # 70k+ tokens flooding your window
```

### 5. Review and Integrate

When agents return:
- Read completion notifications or bead annotations (not TaskOutput)
- Verify fixes don't conflict
- Run full test suite
- Integrate all changes

## Agent Prompt Structure

Good prompts are **focused, self-contained, specific about output:**

```
Fix the 3 failing tests in internal/auth/token_test.go:

1. "TestRefreshExpiredToken" — expects new token, gets expired
2. "TestConcurrentRefresh" — race condition on token store
3. "TestRefreshRetry" — retry count wrong

Root cause is likely timing issues. Your task:
1. Read the test file and understand what each test verifies
2. Identify root cause
3. Fix (don't just increase timeouts — find the real issue)

Return: Summary of root cause and changes made.
```

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Too broad ("fix all tests") | Scope to one file/subsystem |
| No context (error messages) | Paste failures and test names |
| No constraints | Specify what NOT to change |
| Vague output request | Ask for specific summary format |

## Red Flags

- Dispatching agents for related failures
- Agents editing the same files
- Polling agents with sleep loops instead of trusting system reminders
- Using TaskOutput to read agent results (floods context)
- No review after agents return
- Trusting agent results without running full test suite
