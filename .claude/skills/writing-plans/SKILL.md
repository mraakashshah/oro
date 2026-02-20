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

---
```

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

## Execution Handoff

After saving, offer:
1. **Same session** — Execute with `executing-plans` skill, review between batches
2. **Parallel agents** — Dispatch subagents per task if tasks are independent
