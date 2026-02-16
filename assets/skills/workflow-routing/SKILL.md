---
name: workflow-routing
description: Use when a user request arrives and the appropriate workflow or skill chain is not immediately obvious
---

# Workflow Routing

## Overview

Route user requests to the right workflow based on their goal. Keep main context for coordination; delegate work to agents.

## Goal Detection

Detect the user's primary goal from their message:

| Signal | Goal | Skill Chain |
|--------|------|-------------|
| "how does", "what is", "find", "understand" | **Research** | `explore` → document findings |
| "design", "architect", "plan", "break down" | **Plan** | `brainstorming` → `premortem` → `writing-plans` |
| "spec", "decompose", "break into beads", "encode" | **Encode** | `bead-craft` (decompose mode) |
| "add", "implement", "create", "build" | **Build** | `executing-beads` → `finishing-work` |
| "fix", "broken", "failing", "debug", "bug" | **Fix** | `systematic-debugging` → `test-driven-development` → `finishing-work` |
| "work bead", "pick up a bead", "execute bead", "do <id>" | **Work Bead** | `work-bead` |

If intent is clear from context, infer the goal. Otherwise, ask:

```
What's your primary goal?
1. Research — understand/explore something
2. Plan — design and spec a solution (brainstorming → premortem → writing-plans)
3. Encode — decompose a spec into beads (bead-craft)
4. Build — execute beads and ship
5. Fix — debug/fix an issue
```

## Workflow Chains

### Research
1. `explore` skill at appropriate depth
2. Document findings
3. Suggest: "Ready for planning?"

### Plan

**Runs automatically as a single chain — do not stop between steps.**

1. `brainstorming` — explore requirements, generate design options, make decisions (includes per-decision premortems)
2. `premortem` — full plan-level risk analysis on the chosen design
3. `writing-plans` — produce a spec document incorporating brainstorming output and premortem mitigations
4. Suggest: "Ready to encode into beads?"

**Output:** A spec document ready for decomposition.

### Encode

**Runs automatically — invoke bead-craft and present the tree.**

1. `bead-craft` (decompose mode) — parse spec into epic + task beads with full quality (Rule of Five, acceptance criteria, Read/Signature/Edges)
2. Present bead tree for user confirmation
3. Suggest: "Ready to build?"

**Input:** Spec from Plan phase. **Output:** Bead dependency graph.

### Build

**Runs automatically — execute beads in dependency order.**

1. `executing-beads` — TDD cycle per bead, quality gate, atomic commit
2. `requesting-code-review` — review between batches
3. `finishing-work` — integrate and clean up

**Input:** Bead graph from Encode phase.

### Fix
1. `systematic-debugging` — find root cause
2. `test-driven-development` — write failing test, fix
3. `verification-before-completion` — verify fix
4. `finishing-work` — integrate

## Parallel Detection

When tasks are independent, suggest parallel agents:

```
User: "Research auth patterns and fix the login bug"

→ Detect: 2 independent tasks (Research + Fix)
→ Suggest parallel agents
→ Synthesize results
```

## Main Context = Coordination

**Delegate to agents:**
- Reading 3+ files → agent
- External research → agent
- Implementation → agent
- Running tests → agent

**Keep in main context:**
- Understanding user intent
- Workflow selection
- Agent coordination
- Presenting summaries

## After Completing a Workflow

Suggest the natural next step:

| After | Suggest |
|-------|---------|
| Research | "Ready to plan?" |
| Plan | "Ready to encode into beads?" |
| Encode | "Ready to build?" |
| Build | "All beads closed. Ready to finish?" |
| Fix | "Create commit for the fix?" |
