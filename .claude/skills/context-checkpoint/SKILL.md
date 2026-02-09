---
name: context-checkpoint
description: Use after every bead completion to monitor context consumption and trigger proactive handoff before context degrades
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
5. `bd sync --flush-only`
6. `git pull --rebase && git push`

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
