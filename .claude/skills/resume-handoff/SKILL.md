---
name: resume-handoff
description: Use when starting a new session that continues previous work, or when a handoff document is provided
---

# Resume Handoff

## Overview

Resume work from a handoff document by reading it fully, verifying current state, and proposing a course of action.

## Steps

### Step 1: Read and Analyze

1. Read the handoff document completely
2. Read any referenced plans or research documents
3. Read key files mentioned in the handoff
4. Extract `tasks.completed`, `tasks.in_progress`, `tasks.remaining`, and `tasks.epic` before continuing, along with decisions, learnings, and next steps

Handoffs without a `tasks:` section are incomplete state; reconstruct tracked task state before continuing.

### Step 2: Verify Current State

Check for each of the 4 scenarios:

| Scenario | Signal | Action |
|----------|--------|--------|
| **Clean continuation** | All changes present, no conflicts | Proceed with `next:` items |
| **Diverged codebase** | Changes missing or modified since handoff | Reconcile differences, adapt plan |
| **Incomplete work** | Tasks marked in_progress | Complete unfinished work first |
| **Stale handoff** | Significant time passed, major refactoring occurred | Re-evaluate strategy entirely |

Verify tracked task state before continuing:

- Confirm `tasks.completed` entries are actually present in the current codebase or task tracker
- Complete `tasks.in_progress` entries before starting `tasks.remaining`
- Use `tasks.remaining` as the next work queue only after unfinished in-progress work is resolved
- Preserve `tasks.epic` as the parent context when creating or resuming tracked tasks

### Step 3: Present Analysis

```
I've analyzed the handoff from [date]. Current situation:

**Completed work:** [verified present/missing/modified]
**Key decisions:** [still valid/changed]

**Recommended next actions:**
1. [Most logical next step]
2. [Second priority]

Shall I proceed, or adjust the approach?
```

### Step 4: Create Action Plan

- Convert `next:` items from handoff into tasks
- Add new tasks discovered during analysis
- Check `oro task ready` for any tracked work
- Reconcile the extracted `tasks.completed`, `tasks.in_progress`, `tasks.remaining`, and `tasks.epic` state with tracked tasks before selecting the next task
- Get user confirmation before starting

## Principles

- **Never assume handoff state matches current state** — always verify
- **Read referenced files directly** — don't delegate critical context reads
- **Apply learnings** — use `worked:` and avoid `failed:` approaches
- **Be interactive** — present findings and get buy-in before acting

## Red Flags

- Skipping verification of handoff state
- Assuming files haven't changed
- Ignoring `failed:` approaches (repeating mistakes)
- Starting work without user confirmation
