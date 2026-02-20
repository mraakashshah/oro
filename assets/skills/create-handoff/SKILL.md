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

### 4. Scan for Undocumented Learnings

Before writing the handoff, scan for learnings using `.claude/hooks/learning_analysis.py`:

1. **Load and filter entries** using the analysis library:
   ```python
   from learning_analysis import load_knowledge, filter_by_bead, tag_frequency, frequency_level, cross_reference_docs, content_similarity
   from pathlib import Path

   entries = load_knowledge(Path(".beads/memory/knowledge.jsonl"))
   # Filter to beads worked this session
   session_entries = []
   for bead_id in beads_worked_this_session:
       session_entries.extend(filter_by_bead(entries, bead_id))
   ```

2. **Cross-reference with docs** using `cross_reference_docs()`:
   ```python
   undocumented = cross_reference_docs(session_entries, Path("~/.oro/projects/<name>/decisions&discoveries.md"))
   ```

3. **Check tag frequency** across ALL entries using `tag_frequency()`:
   ```python
   global_freq = tag_frequency(entries)  # all entries, not just this session
   # For each tag in session entries, check frequency_level()
   for tag, count in global_freq.items():
       level = frequency_level(count)  # "note", "consider", or "create"
   ```

4. **Detect cross-bead patterns** using `content_similarity()`:
   ```python
   clusters = content_similarity(session_entries)
   # Clusters with entries from multiple beads indicate cross-bead patterns
   ```

**Frequency thresholds** (from `frequency_level()`):

| Count | Level | Action |
|-------|-------|--------|
| 1 | `note` | Already surfaced at session start. No additional action. |
| 2 | `consider` | Note: "this has come up before" -- include previous occurrence. |
| 3+ | `create` | Propose specific codification target (see decision tree below). |

**Decision tree for 3+ frequency:**
- Repeatable sequence of steps --> Propose a **skill** (`.claude/skills/<name>/SKILL.md`)
- Triggered by specific event --> Propose a **hook** (`.claude/hooks/<name>.py`)
- Heuristic or constraint --> Propose a **rule** (`.claude/rules/<file>.md`)
- Solved problem with context --> Propose a **solution doc** (`~/.oro/projects/<name>/decisions&discoveries.md` entry)

**Add `learnings:` field to handoff YAML** (after `next:` section):

```yaml
learnings:
  undocumented:
    - "<content>" (<bead>, <tags>)
  proposed_codification:
    - type: skill|hook|rule|solution
      target: <file path>
      content: "<what to codify>"
      frequency: <count>
      tag: <primary tag>
```

If no undocumented learnings exist, omit the `learnings:` section entirely.

### 5. Capture Learnings

Before writing the handoff, ask yourself: "Did I learn anything this session worth preserving?"

If yes, run for each learning:
```bash
bd comments add <bead-id> "LEARNED: <what you learned>"
```

This feeds into knowledge.jsonl and gets resurfaced in future sessions. Examples:
- "LEARNED: modernc sqlite doesn't support FTS5 bm25() — use rank column instead"
- "LEARNED: git rebase fails if branch is checked out in any worktree — remove worktree first"

## Principles

- **More information, not less** — this is the minimum, always add more if needed
- **Be thorough and precise** — include top-level objectives AND low-level details
- **Avoid excessive code snippets** — prefer `path/to/file.go:12-24` references
- **Use YAML** — 5-10x more token-efficient than markdown prose
