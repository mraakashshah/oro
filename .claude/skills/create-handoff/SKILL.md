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

beads:
  completed: [bd-xxx]
  in_progress: [bd-yyy]
  remaining: [bd-zzz]
  epic: bd-www
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
| `beads:` | Bead state for multi-session work (completed/in_progress/remaining/epic) |

### 4. Capture Learnings

Before writing the handoff, ask yourself: "Did I learn anything this session worth preserving?"

If yes, run for each learning:
```bash
bd comment <bead-id> "LEARNED: <what you learned>"
```

This feeds into knowledge.jsonl and gets resurfaced in future sessions. Examples:
- "LEARNED: modernc sqlite doesn't support FTS5 bm25() — use rank column instead"
- "LEARNED: git rebase fails if branch is checked out in any worktree — remove worktree first"

## Principles

- **More information, not less** — this is the minimum, always add more if needed
- **Be thorough and precise** — include top-level objectives AND low-level details
- **Avoid excessive code snippets** — prefer `path/to/file.go:12-24` references
- **Use YAML** — 5-10x more token-efficient than markdown prose
