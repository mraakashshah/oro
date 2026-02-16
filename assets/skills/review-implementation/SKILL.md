---
name: review-implementation
description: Use when implementation phase is complete -- verifies code matches spec, identifies gaps, and tracks completion state
---

# Review Implementation

## Overview

Compare code against the plan/spec line by line. Report what's done, what's missing, and what diverged.

## Steps

### 1. Locate the Spec

Find the source of truth — plan file, issue description, or requirements doc. If none exists, ask before proceeding.

### 2. Build the Checklist

Extract every requirement, acceptance criterion, and edge case from the spec into a flat checklist.

### 3. Verify Each Item

For each checklist item:
- **Find the code** that implements it (file, function, line)
- **Classify**: done, partial, missing, or diverged
- If diverged: note what was built vs. what was specified

Read the actual code. Do not infer from file names or function signatures.

### 4. Report

```
## Implementation Review: [feature/scope]

### Summary
X/Y requirements met | Z gaps | W divergences

### Checklist
- [x] Requirement A — `src/foo.py:42`
- [~] Requirement B — partial, missing error handling
- [ ] Requirement C — not implemented
- [!] Requirement D — diverged: spec says X, code does Y

### Gaps (action needed)
1. [description + suggested fix or issue]

### Intentional Divergences (confirm with user)
1. [what changed and why it might be fine]
```

## Red Flags

- Marking items "done" without reading the implementation
- Skipping edge cases listed in the spec
- Treating "code exists" as "requirement met"
- Reviewing against memory instead of re-reading the spec
