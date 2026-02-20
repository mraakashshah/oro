---
name: refactor
description: Use when improving existing code structure without changing behavior, after tests are green
---

# Refactor

## Overview

Safe refactoring with a green test baseline. No behavior changes — only structural improvements.

## The Iron Law

```
NO REFACTORING WITHOUT GREEN TESTS FIRST
```

If tests aren't passing before you start, you can't distinguish refactoring breakage from pre-existing issues.

## When to Use

- Improving code structure
- Extracting modules or functions
- Reducing duplication
- Cleaning up technical debt
- Applying functional-first patterns

## Steps

### 1. Establish Green Baseline

```bash
# Go
go test ./...

# Python
uv run pytest
```

All tests must pass before any changes.

### 2. Analyze Current Code

Identify:
- Code smells and pain points
- Improvement opportunities
- Risk areas
- Test coverage gaps (add tests before refactoring if needed)

### 3. Plan Changes

Each change should be:
- **Small and focused** — one concept per change
- **Independently testable** — run tests after each step
- **Reversible** — easy to undo if tests break

### 4. Implement (Small Steps)

For each step:
1. Make one structural change
2. Run tests — must stay green
3. Commit if green
4. If tests break: revert and try a smaller step

### 5. Verify

After all steps:
- Run full test suite
- Verify no behavior changes
- Review diff for unintended changes

## Functional-First Patterns

When refactoring, prefer these transformations:

| From | To |
|------|----|
| Mutable state | Immutable values |
| Side effects in logic | Pure core, impure edges |
| Imperative loops | map/filter/reduce |
| Class with methods | Functions + data |
| Shared mutable state | Passed parameters |

## Red Flags

- Refactoring without green baseline
- Combining refactoring with behavior changes
- Making too many changes before running tests
- "While I'm here" improvements (scope creep)
- Skipping tests between steps

## Common Rationalizations

| Excuse | Reality |
|--------|---------|
| "Tests are mostly passing" | Mostly ≠ all. Fix first. |
| "This small change can't break anything" | Run tests. Trust evidence, not intuition. |
| "I'll add the feature while refactoring" | Separate concerns. Refactor then feature. |
| "Testing each step is slow" | Slower than debugging a broken refactor. |
