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
4. Extract: tasks, decisions, learnings, next steps

### Step 2: Verify Current State

Check for each of the 4 scenarios:

| Scenario | Signal | Action |
|----------|--------|--------|
| **Clean continuation** | All changes present, no conflicts | Proceed with `next:` items |
| **Diverged codebase** | Changes missing or modified since handoff | Reconcile differences, adapt plan |
| **Incomplete work** | Tasks marked in_progress | Complete unfinished work first |
| **Stale handoff** | Significant time passed, major refactoring occurred | Re-evaluate strategy entirely |

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
- Check `bd ready` for any tracked work
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
