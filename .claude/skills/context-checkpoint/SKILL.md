---
name: context-checkpoint
description: Use after every bead completion to monitor context consumption and trigger proactive handoff before context degrades
---

# Context Checkpoint

## Overview

Monitor context consumption and trigger proactive handoff before quality degrades. Called after every bead completion in `executing-beads`.

**Core principle:** Better to hand off early than to lose context and produce bad work.

## How It Works

The `inject_context_usage.py` PreToolUse hook monitors token consumption automatically and injects warnings. Thresholds are model-aware:

| Model | Warn | Critical |
|-------|------|----------|
| Opus 4.6 | 45% | 60% |
| Sonnet | — | 45% |
| Haiku | — | 35% |

Trust those warnings.

## Quality Signals (Override Token Count)

These symptoms indicate context degradation regardless of token usage — treat as immediate Red:

- **Repeating yourself** — suggesting something already tried or discussed
- **Forgetting earlier context** — asking about decisions already made
- **Hallucinating paths** — referencing files or functions that don't exist
- **Growing confusion** — needing to re-read files you already read
- **PreCompact hook fires** — immediate Red, no exceptions

## Checkpoint Protocol

### Green (Continue)

No warnings from the hook. Proceed to next bead.

### Yellow (Hook warns at 30%)

Finish current bead, then evaluate. Only start a new bead if it's small.

### Red (Hook warns at 40%, or quality signals fire)

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

- Ignoring Red signals and starting new beads
- Skipping checkpoint after bead completion
- Not saving in-progress state before handoff
- Producing a handoff without the `beads:` section
- Rationalizing "just one more bead" past Red
