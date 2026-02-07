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
- **Expected output** — summary of findings and changes

### 3. Dispatch in Parallel

Use the Task tool with multiple calls in a single message:
```
Task("Fix auth_test.go failures — timing issues in token refresh")
Task("Fix pipeline_test.py failures — data transformation errors")
Task("Fix api_test.go failures — response format mismatch")
```

**Batch size:** max 15 parallel agents. Beyond that, diminishing returns.

### 4. Review and Integrate

When agents return:
- Read each summary
- Verify fixes don't conflict
- Run full test suite
- Integrate all changes

**Do NOT use TaskOutput to poll agents.** Wait for them to complete and return results.

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
- No review after agents return
- Trusting agent results without running full test suite
