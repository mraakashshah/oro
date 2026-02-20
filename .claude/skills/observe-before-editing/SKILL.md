---
name: observe-before-editing
description: Use when about to edit code to fix a bug or change behavior -- check actual system outputs before making changes
---

# Observe Before Editing

## Overview

Outputs don't lie. Code might. Check outputs first.

**Core principle:** Never edit code based on what you *think* should happen. Verify what *actually* happened.

## The Iron Law

```
NO CODE EDITS WITHOUT OBSERVING ACTUAL SYSTEM STATE FIRST
```

## When to Use

- About to fix a bug — check what the system actually produced
- About to change behavior — confirm current behavior first
- Something "didn't work" — verify before assuming what failed
- Debugging a failure — observe the actual error, don't guess

## Steps

1. **Check if expected outputs exist**
   ```bash
   ls -la path/to/expected/output/
   ```

2. **Check logs for actual errors**
   ```bash
   # Read recent logs, test output, build output
   ```

3. **Run the failing command manually** to see the real error

4. **Mark your confidence level:**

   | Marker | Meaning | Action |
   |--------|---------|--------|
   | VERIFIED | Read the file, traced the code | Safe to assert |
   | INFERRED | Based on grep/search pattern | Must verify before claiming |
   | UNCERTAIN | Haven't checked | Must investigate |

5. **Only then edit code**

## Common Traps

| Trap | Reality |
|------|---------|
| "Hook didn't run" | Check output files — it probably did |
| "Function is broken" | Did you run it? What was the actual error? |
| "File doesn't exist" | Searched wrong directory or wrong name |
| "Feature is missing" | Grep returned nothing ≠ doesn't exist (try alternate names) |
| "Test should have caught this" | Run the test. Read the output. |

## Two-Pass Audit Pattern

When reviewing or auditing code:

**Pass 1 — Hypotheses:**
```
"I think X might be missing" (INFERRED)
```

**Pass 2 — Verification:**
```
Read the actual file
→ Found X at line 42
→ "X exists at file.go:42" (VERIFIED)
```

## Red Flags - STOP

- Editing code based on what you *think* should happen
- Assuming failure without observing the output
- Trusting grep results without reading the actual file
- Confusing paths (project vs global, relative vs absolute)
- Claiming "X doesn't exist" without exhaustive search

**If you catch yourself doing any of these: STOP. Observe first.**

## Common Rationalizations

| Excuse | Reality |
|--------|---------|
| "I know what the error is" | You're guessing. Check. |
| "Obviously this is the problem" | Obvious ≠ verified. Run it. |
| "I'll check after I fix it" | Fix might mask the real issue. Observe first. |
| "Grep returned nothing" | Try alternate names, check different paths |
| "The code clearly shows..." | Code shows intent. Output shows reality. |
