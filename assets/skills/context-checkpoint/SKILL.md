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

**Layer 1 — Native context awareness (always present).** Claude Sonnet 4.5+, Sonnet 4.6, and Haiku 4.5 receive context state after every tool call:

```xml
<system_warning>Token usage: 35000/200000; 165000 remaining</system_warning>
```

No hook needed — workers already know their context state from `system_warning`. Thresholds are loaded from `thresholds.json` at the project root:

| Model  | Soft threshold | Hard threshold |
|--------|---------------|----------------|
| Opus   | 65%           | 85%            |
| Sonnet | 50%           | 70%            |
| Haiku  | 40%           | 60%            |

Source of truth: `thresholds.json`.

The Go worker enforces the hard stop as a backstop: if the agent does not respond to the soft threshold, `handleContextThreshold` sends a handoff and kills the subprocess at hard threshold (soft + 20%).

## Quality Signals (Override Token Count)

These symptoms indicate context degradation regardless of token usage — treat as immediate handoff:

- **Repeating yourself** — suggesting something already tried or discussed
- **Forgetting earlier context** — asking about decisions already made
- **Hallucinating paths** — referencing files or functions that don't exist
- **Growing confusion** — needing to re-read files you already read

## Checkpoint Protocol

### Green (Continue)

`system_warning` shows usage well below soft threshold. Proceed to next bead.

### Soft Threshold Breached

Finish your current atomic step (complete the file edit, run its verification, record the result). Then:

- Do NOT start another edit-verify cycle.
- Follow handoff steps below.

### Hard Threshold Breached

Stop after your very next tool call. Write a minimal handoff (goal + files modified + next steps). Any in-progress work will be captured by the compaction safety net.

### Handoff Steps

1. Close current bead if work is complete
2. For in-progress work: `oro bead update <id> --notes "Partial: <what's done, what remains>"`
3. Verify remaining work exists as beads (create if needed)
4. Use `create-handoff` skill with `beads:` section
5. `git pull --rebase && git push`

## Handoff Template Addition

When handing off due to context checkpoint, add `beads:` section to handoff YAML:

```yaml
beads:
  completed: [oro-xxx, oro-yyy]
  in_progress: [oro-zzz]
  remaining: [oro-aaa, oro-bbb]
  epic: oro-www
```

## Red Flags

- Ignoring hook messages and starting new beads
- Skipping checkpoint after bead completion
- Not saving in-progress state before handoff
- Producing a handoff without the `beads:` section
- Rationalizing "just one more bead" after handoff message
