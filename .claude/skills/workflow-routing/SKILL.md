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
| "design", "architect", "plan", "break down" | **Plan** | `brainstorming` → `writing-plans` → `premortem` |
| "add", "implement", "create", "build" | **Build** | `writing-plans` → `executing-plans` → `finishing-work` |
| "fix", "broken", "failing", "debug", "bug" | **Fix** | `systematic-debugging` → `test-driven-development` → `finishing-work` |
| "spec", "decompose", "break into beads" | **Decompose** | `spec-to-beads` → `executing-beads` → `finishing-work` |
| "work bead", "pick up a bead", "execute bead" | **Work Bead** | `work-bead` |

If intent is clear from context, infer the goal. Otherwise, ask:

```
What's your primary goal?
1. Research — understand/explore something
2. Plan — design/architect a solution
3. Build — implement/code something
4. Fix — debug/fix an issue
5. Decompose — break a spec into beads and execute
```

## Workflow Chains

### Research
1. `explore` skill at appropriate depth
2. Document findings
3. Suggest: "Ready for planning?"

### Plan
1. `brainstorming` — understand requirements
2. `writing-plans` — create implementation plan
3. `premortem` — identify risks before building
4. Suggest: "Ready to build?"

### Build
1. `executing-plans` — implement task-by-task
2. `requesting-code-review` — review between batches
3. `finishing-work` — integrate and clean up

### Fix
1. `systematic-debugging` — find root cause
2. `test-driven-development` — write failing test, fix
3. `verification-before-completion` — verify fix
4. `finishing-work` — integrate

### Decompose
1. `spec-to-beads` — parse spec, create epic + task beads with acceptance criteria
2. `executing-beads` — TDD cycle per bead, quality gate, atomic commit
3. `finishing-work` — integrate and clean up

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
| Research | "Ready for planning?" |
| Plan | "Run premortem before building?" |
| Fix | "Create commit for the fix?" |
| Build | "Ready to finish and merge?" |
| Decompose | "All beads closed. Ready to finish?" |
