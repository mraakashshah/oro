---
name: create-handoff
description: Use when ending a session, switching context, or needing to transfer work state to another session or agent
---

# Create Handoff

Write a concise YAML handoff that lets the next session continue without reconstructing the work.

## Steps

### 1. Choose the Path

Use `docs/handoffs/YYYY-MM-DD_HH-MM_description.yaml`.

### 2. Write the YAML

```yaml
---
date: YYYY-MM-DD
status: complete|partial|blocked
---

goal: What this session accomplished
now: What the next session should do first
test: Command that verifies the current state

done_this_session:
  - task: Completed work
    files: [path/to/file]

blockers: []
questions: []
decisions: []
findings: []
worked: []
failed: []
next: []

files:
  created: []
  modified: []

tasks:
  completed: []
  in_progress: []
  remaining: []
  epic: null
```

Keep `goal`, `now`, and `test` concrete. Include exact task IDs, file paths, verification results, unresolved decisions, and failed approaches when they matter.

### 3. Field Guide

| Field | Meaning |
|-------|---------|
| `tasks.completed` | Closed task IDs |
| `tasks.in_progress` | Claimed work still underway |
| `tasks.remaining` | Known follow-up task IDs |
| `tasks.epic` | Parent epic ID, or `null` |

### 4. Preserve Reusable Learnings

If the session produced a reusable lesson, record it with:

```bash
oro remember "lesson: <specific reusable finding>"
```

Skip this for task-local details already captured in the handoff.

### 5. Signal an Oro Worker Continuation

Run this step only when `ORO_WORKER=1`. That environment flag means the dispatcher is waiting for the current worker to yield a continuation.

1. Write `.oro/context_summary.txt` from the handoff's `goal` and `now` values:

   ```bash
   python3 ~/.oro/hooks/write_context_summary.py \
     --goal "<goal>" \
     --now "<now>"
   ```

2. Only after the summary exists, signal completion:

   ```bash
   touch .oro/handoff_done
   ```

For every other handoff, stop after writing the handoff document. Do not create or overwrite `.oro/context_summary.txt` or `.oro/handoff_done`.

## Principles

- Preserve decisions and evidence, not a transcript.
- Prefer precise file and symbol references over large code excerpts.
- Distinguish completed work from assumptions and next steps.
- Keep the YAML compact enough to scan quickly.
