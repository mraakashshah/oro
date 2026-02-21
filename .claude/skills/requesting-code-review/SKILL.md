---
name: requesting-code-review
description: Use when completing tasks, implementing major features, or before merging -- to dispatch review and verify work meets requirements
---

# Requesting Code Review

## Overview

Review early, review often. Dispatch a reviewer to catch issues before they cascade.

## When to Request Review

**Mandatory:**
- After each task in plan execution
- After completing major feature
- Before merge to main

**Optional but valuable:**
- When stuck (fresh perspective)
- Before refactoring (baseline check)
- After fixing complex bug

## How to Request

### 1. Gather context

```bash
BASE_SHA=$(git rev-parse HEAD~1)  # or origin/main
HEAD_SHA=$(git rev-parse HEAD)
```

### 2. Structure the review request

Provide:
- **What was implemented** — brief summary
- **Requirements/plan** — what it should do
- **Base SHA → Head SHA** — commit range to review
- **Description** — any special context

### 3. Review template

```
Review changes from {BASE_SHA} to {HEAD_SHA}.

**What was built:** {SUMMARY}
**Requirements:** {PLAN_OR_REQUIREMENTS}

Check for:
- Spec compliance (matches requirements?)
- Code quality (clean, tested, no regressions?)
- Triage as Critical / Important / Minor
```

### 4. Act on feedback

When feedback arrives, invoke `receiving-code-review` before implementing anything.

| Severity | Action |
|----------|--------|
| **Critical** | Fix immediately |
| **Important** | Fix before proceeding |
| **Minor** | Note for later |

If reviewer is wrong: push back with technical reasoning.

## Integration

- **Plan execution:** Review after each batch (3 tasks)
- **Subagent-driven:** Review after each task
- **Ad-hoc development:** Review before merge

## Red Flags

- Skip review because "it's simple"
- Ignore Critical issues
- Proceed with unfixed Important issues
- Argue with valid technical feedback without evidence
