---
name: context-checkpoint
description: Use after every bead completion to monitor context consumption and trigger proactive handoff before context degrades
user-invocable: false
---

# Context Checkpoint

## Overview

Monitor context consumption and trigger proactive handoff before quality degrades. Called after every bead completion in `executing-beads`.

**Core principle:** Better to hand off early than to lose context and produce bad work.

## How It Works

The `inject_context_usage.py` PreToolUse hook monitors token consumption automatically. Thresholds are loaded from `thresholds.json` at the project root — one threshold per model:

| Model | Threshold |
|-------|-----------|
| Opus | 65% |
| Sonnet | 50% |
| Haiku | 40% |
| Default | 50% |

Source of truth: `thresholds.json` (read by both Go worker and Python hook).

### Two-Stage Response

When the threshold is breached:

1. **First breach** — hook injects a `/compact` message. The Go worker sends `/compact` to the subprocess stdin and creates `.oro/compacted` flag.
2. **Second breach** — hook injects a handoff message. The Go worker triggers a ralph handoff (kill + respawn with fresh context).

Trust the hook messages and act on them immediately.

## Learning Checkpoint

After completing a bead and before checking context zones, scan for learnings using `learning_analysis.py` (`.claude/hooks/learning_analysis.py`):

1. **Scan knowledge.jsonl** for entries where `bead` matches the just-completed bead ID:
   ```python
   # Uses learning_analysis.py functions
   from learning_analysis import load_knowledge, filter_by_bead, tag_frequency, frequency_level
   entries = load_knowledge(Path(".beads/memory/knowledge.jsonl"))
   matched = filter_by_bead(entries, "<completed-bead-id>")
   ```

2. **Check tag frequency** across ALL entries (not just this bead) using `tag_frequency()`:
   ```python
   global_freq = tag_frequency(entries)  # all entries, not just matched
   # Check tags from this bead's entries against global frequency
   for tag in bead_tags:
       level = frequency_level(global_freq.get(tag, 0))
   ```

   | Count | Level | Action |
   |-------|-------|--------|
   | 1 | `note` | Already surfaced at session start. No additional action. |
   | 2 | `consider` | Note: "this has come up before" -- include previous occurrence. |
   | 3+ | `create` | Propose specific codification target (see decision tree below). |

3. **Propose actions** (if learnings found):
   - Learnings only in knowledge.jsonl (not in memory store): suggest `oro remember "<content>"` to promote
   - Tag at 3+ frequency (`frequency_level() == "create"`): propose codification per decision tree:
     - Repeatable sequence --> **skill** (`.claude/skills/`)
     - Event-triggered --> **hook** (`.claude/hooks/`)
     - Heuristic/constraint --> **rule** (`.claude/rules/`)
     - Solved problem --> **solution doc** (`~/.oro/projects/<name>/decisions&discoveries.md`)
   - Format proposals as a brief `## Learning Checkpoint` note (3-5 lines max)

4. **If no learnings**: Skip silently. Don't add noise.

## Quality Signals (Override Token Count)

These symptoms indicate context degradation regardless of token usage — treat as immediate handoff:

- **Repeating yourself** — suggesting something already tried or discussed
- **Forgetting earlier context** — asking about decisions already made
- **Hallucinating paths** — referencing files or functions that don't exist
- **Growing confusion** — needing to re-read files you already read

## Checkpoint Protocol

### Green (Continue)

No messages from the hook. Proceed to next bead.

### Threshold Breached (Hook injects message)

Follow the hook's instruction:

- **If told to compact**: Run `/compact`, then continue working.
- **If told to handoff**: Stop starting new work. Follow handoff steps below.

### Handoff Steps

1. Close current bead if work is complete
2. For in-progress work: `bd update <id> --notes "Partial: <what's done, what remains>"`
3. Verify remaining work exists as beads (create if needed)
4. Use `create-handoff` skill with `beads:` section
5. `git pull --rebase && git push`
   - Note: The pre-commit hook automatically runs `bd sync --flush-only`, so manual sync is not needed

## Handoff Template Addition

When handing off due to context checkpoint, add `beads:` section to handoff YAML:

```yaml
beads:
  completed: [bd-xxx, bd-yyy]
  in_progress: [bd-zzz]
  remaining: [bd-aaa, bd-bbb]
  epic: bd-www
```

## Red Flags

- Ignoring hook messages and starting new beads
- Skipping checkpoint after bead completion
- Not saving in-progress state before handoff
- Producing a handoff without the `beads:` section
- Rationalizing "just one more bead" after handoff message
