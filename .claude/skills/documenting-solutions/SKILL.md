---
name: documenting-solutions
description: Use when a non-trivial problem has been solved and verified working -- captures the solution as searchable institutional knowledge
---

# Documenting Solutions

## Overview

Capture solved problems as searchable documentation. Non-trivial problems only — skip typos and obvious fixes.

## When to Document

**Document when:**
- Multiple investigation attempts were needed
- Debugging took significant time
- Solution was non-obvious
- Future sessions would benefit from knowing this

**Skip when:**
- Simple typos or syntax errors
- Trivial fixes immediately corrected
- Standard practices well-documented elsewhere

## Steps

### 1. Gather Context

Extract from the session:
- **Symptom** — Observable error/behavior (exact error messages)
- **Investigation** — What didn't work and why
- **Root cause** — Technical explanation
- **Solution** — What fixed it (code/config changes)
- **Prevention** — How to avoid in future

### 2. Check for Similar Issues

```bash
rg "error message keywords" docs/solutions/
```

If similar issue found: cross-reference. If same root cause: update existing doc.

### 3. Create Documentation

Save to: `docs/solutions/YYYY-MM-DD-symptom-description.md`

```markdown
# [Symptom Description]

**Date:** YYYY-MM-DD
**Component:** [what was affected]
**Severity:** critical|high|medium|low

## Symptom

[Exact error message or observable behavior]

## Investigation

[What was tried and why it didn't work]

## Root Cause

[Technical explanation of the actual problem]

## Solution

[What fixed it — include code if helpful]

## Prevention

[How to avoid this in the future]

## Related

[Links to similar issues if any]
```

### 4. Cross-Reference (if applicable)

If 3+ similar issues exist, consider creating a pattern entry:

```markdown
## [Pattern Name]
**Common symptom:** [description]
**Root cause:** [technical explanation]
**Solution pattern:** [general approach]
```

## Quality Checklist

- [ ] Exact error messages (copy-paste, not paraphrased)
- [ ] Specific file:line references
- [ ] Failed attempts documented (helps avoid wrong paths)
- [ ] Technical explanation (not just "what" but "why")
- [ ] Prevention guidance
