---
name: writing-plans
description: Use when you have validated requirements or a design and need a step-by-step implementation plan before writing code
---

# Writing Plans

## Overview

Write comprehensive implementation plans assuming the engineer has zero context. Document everything: which files to touch, code, testing commands, how to verify. Give the whole plan as bite-sized tasks.

Assume a skilled developer who knows almost nothing about the toolset or problem domain.

**Save plans to:** `docs/plans/YYYY-MM-DD-<feature-name>.md`

## Plan Document Header

Every plan MUST start with:

```markdown
# [Feature Name] Implementation Plan

> **For Claude:** Use executing-plans skill to implement this plan task-by-task.

**Goal:** [One sentence]
**Architecture:** [2-3 sentences about approach]
**Tech Stack:** [Key technologies/libraries]

## Global Constraints

[The spec's project-wide requirements — version floors, dependency limits, naming and copy rules, platform requirements — one line each, with exact values copied verbatim from the spec. Every task implicitly inherits this section; workers see only their own task, so anything not repeated in a task lives here or nowhere.]

---
```

## Task Right-Sizing

A task is the smallest unit that carries its own test cycle and can pass or fail a fresh reviewer's gate. When drawing task boundaries, fold setup, configuration, scaffolding, and documentation steps into the task whose deliverable needs them — a standalone "add config" or "write docs" task rarely earns its own review. Split only where a reviewer could reasonably reject one task while approving its neighbor. Each task ends with an independently testable deliverable.

## Bite-Sized Task Granularity

Each step is one action (2-5 minutes):
- "Write the failing test" — step
- "Run it to verify it fails" — step
- "Implement minimal code to pass" — step
- "Run tests to verify they pass" — step
- "Commit" — step

## Task Structure

```markdown
### Task N: [Component Name]

**Files:**
- Create: `exact/path/to/file.go`
- Modify: `exact/path/to/existing.go:123-145`
- Test: `exact/path/to/file_test.go`

**Interfaces:**
- Consumes: [what this task uses from earlier tasks — exact signatures]
- Produces: [what later tasks rely on — exact function names, parameter and return types]

Oro dispatches an independent worker per task, and each worker sees ONLY its own task in its own worktree. This block is the entire cross-task contract: it is how a worker learns the names and types its neighbors expose without reading their tasks. If a signature isn't written here, the worker cannot know it.

**Step 1: Write the failing test**
[Complete test code]

**Step 2: Run test to verify it fails**
Run: `go test ./path/to/... -run TestName -v`
Expected: FAIL

**Step 3: Write minimal implementation**
[Complete implementation code]

**Step 4: Run test to verify it passes**
Run: `go test ./path/to/... -run TestName -v`
Expected: PASS

**Step 5: Commit**
```

## Principles

- **Exact file paths** always
- **Complete code** in plan (not "add validation")
- **Exact commands** with expected output
- **DRY, YAGNI, TDD**, frequent commits
- **Assume zero context** — spell everything out

## Self-Review

After writing the complete plan, read the spec again with fresh eyes and check the plan against it. Run this checklist yourself — it is not a worker dispatch.

1. **Spec coverage:** Skim each requirement in the spec. Can you point to a task that implements it? List any gaps and add the missing tasks.
2. **Placeholder scan:** Search the plan for red flags — "TBD", "add validation", "handle edge cases", "similar to Task N", any code step without actual code. Fix them inline.
3. **Interface consistency:** Do the signatures, method names, and types a task Consumes match what an earlier task Produces? A function called `clearLayers()` in Task 3 but `clearFullLayers()` in Task 7 is a bug — and since workers never see each other's tasks, it is a bug no worker can catch. Reconcile the Interfaces blocks.

Fix issues inline and move on — no need to re-review.

## Execution Handoff

After saving, offer:
1. **Same session** — Execute with `executing-plans` skill, review between batches
2. **Parallel agents** — Dispatch subagents per task if tasks are independent
