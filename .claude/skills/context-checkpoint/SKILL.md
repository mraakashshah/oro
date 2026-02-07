---
name: context-checkpoint
description: Use after every bead completion to monitor context consumption and trigger proactive handoff before context degrades
---

# Context Checkpoint

## Overview

Monitor context consumption and trigger proactive handoff before quality degrades. Called after every bead completion in `executing-beads`.

**Core principle:** Better to hand off early than to lose context and produce bad work.

## Threshold Table

Proxy: count user-assistant message pairs in the current session.

| Pairs | Zone | Action |
|-------|------|--------|
| 0-10 | Green | Continue. Pick next bead. |
| 11-15 | Yellow | Finish current bead, then evaluate. Only start a new bead if it's small (<=5min estimate). |
| 16-20 | Orange | Do NOT start a new bead. Initiate handoff now. |
| 20+ | Red | Stop immediately. Emergency handoff. |

## Quality Signals (Override Message Count)

These symptoms indicate context degradation regardless of message count — treat as immediate Orange:

- **Repeating yourself** — suggesting something already tried or discussed
- **Forgetting earlier context** — asking about decisions already made
- **Hallucinating paths** — referencing files or functions that don't exist
- **Growing confusion** — needing to re-read files you already read
- **PreCompact hook fires** — immediate Red, no exceptions

## Checkpoint Protocol

### Green Zone (Continue)

```
Context check: Green (N pairs). Proceeding to next bead.
```

Return to `executing-beads` Step 1.

### Yellow Zone (Evaluate)

```
Context check: Yellow (N pairs). Evaluating next bead size.
```

- Check next bead's estimate via `bd ready` + `bd show <id>`
- If estimate <=5min: proceed with caution
- If estimate >5min: escalate to Orange

### Orange Zone (Handoff)

```
Context check: Orange (N pairs). Initiating handoff.
```

1. Close current bead if work is complete
2. For in-progress work: `bd update <id> --notes "Partial: <what's done, what remains>"`
3. Verify remaining work exists as beads (create if needed)
4. Use `create-handoff` skill with `beads:` section
5. `bd sync --flush-only`
6. `git pull --rebase && git push`

### Red Zone (Emergency)

```
Context check: RED. Emergency handoff.
```

1. Save current state immediately — `bd update <id> --notes "Emergency handoff: <state>"`
2. Minimal handoff document (skip polish)
3. `bd sync --flush-only && git push`

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

- Ignoring Orange/Red signals and starting new beads
- Skipping checkpoint after bead completion
- Not saving in-progress state before handoff
- Producing a handoff without the `beads:` section
- Rationalizing "just one more bead" past Orange
