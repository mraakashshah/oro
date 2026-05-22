---
name: create-handoff
description: Use when ending a session, switching context, or needing to transfer work state to another session or agent
---

# Create Handoff

## Overview

Write a concise handoff document that compacts and summarizes your context without losing key details. YAML format is 5-10x more token-efficient than markdown.

## When to Use

- Ending a work session
- Switching to a different task
- Handing off to another agent/session
- Context window getting full

## Steps

### 1. Determine File Path

Save to: `docs/handoffs/YYYY-MM-DD_HH-MM_description.yaml`

Example: `docs/handoffs/2026-01-08_16-30_auth-refactor.yaml`

### 2. Write YAML Handoff

```yaml
---
date: YYYY-MM-DD
status: complete|partial|blocked
---

goal: What this session accomplished
now: What next session should do first
test: Command to verify this work (e.g., go test ./... or uv run pytest)

done_this_session:
  - task: First completed task
    files: [file1.go, file2.go]
  - task: Second completed task
    files: [file3.py]

blockers: [any blocking issues]

questions: [unresolved questions for next session]

decisions:
  - decision_name: rationale

findings:
  - key_finding: details

worked: [approaches that worked]
failed: [approaches that failed and why]

next:
  - First next step
  - Second next step

files:
  created: [new files]
  modified: [changed files]

tasks:
  completed: [oro-xxx]
  in_progress: [oro-yyy]
  remaining: [oro-zzz]
  epic: oro-www
```

### 3. Field Guide

| Field | Purpose |
|-------|---------|
| `goal:` | What was accomplished (required) |
| `now:` | What to do next (required) |
| `test:` | Verification command |
| `done_this_session:` | Work completed with file refs |
| `decisions:` | Important choices and rationale |
| `worked:` / `failed:` | What to repeat vs avoid |
| `next:` | Action items for next session |
| `tasks:` | Task state for multi-session work (completed/in_progress/remaining/epic) |
| `tasks.completed:` | Completed Oro task IDs; use `[]` when none |
| `tasks.in_progress:` | Oro task IDs currently in progress; use `[]` when none |
| `tasks.remaining:` | Remaining Oro task IDs; use `[]` when none |
| `tasks.epic:` | Parent epic/task ID for the handoff |

### 4. Capture Learnings

Before writing the handoff, ask yourself: "Did I learn anything this session worth preserving?"

If yes, run for each learning:
```bash
oro remember "lesson: <what you learned>"
```

This persists the learning to your memory store. Examples:
- `oro remember "lesson: modernc sqlite doesn't support FTS5 bm25() — use rank column instead"`
- `oro remember "gotcha: git rebase fails if branch is checked out in any worktree — remove worktree first"`

## Principles

**Before** touching `.oro/handoff_done`, write a compact context summary so the dispatcher can embed it in the continuation task description. Without this file, continuation tasks have no context and workers must start blind.

Use the `goal:` and `now:` values from your handoff YAML:

```bash
python3 ~/.oro/hooks/write_context_summary.py \
  --goal "<goal value from your handoff YAML>" \
  --now "<now value from your handoff YAML>"
```

This writes `.oro/context_summary.txt` relative to the current worktree root. The dispatcher reads this file in `handleHandoffExhaustion()` (`pkg/worker/worker.go`) to populate `ContextSummary` in the continuation task.

**Edges:**
- `.oro/` does not exist → the script creates it automatically
- `context_summary.txt` already exists → it will be overwritten

### 6. Write Sentinel File

After writing the context summary, write the sentinel file so the dispatcher detects the handoff:

```bash
touch .oro/handoff_done
```

This file signals to the dispatcher that the handoff document is complete and the worker is ready to be cycled.

## Principles

- **More information, not less** — this is the minimum, always add more if needed
- **Be thorough and precise** — include top-level objectives AND low-level details
- **Avoid excessive code snippets** — prefer `path/to/file.go:12-24` references
- **Use YAML** — 5-10x more token-efficient than markdown prose
