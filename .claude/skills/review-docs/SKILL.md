---
name: review-docs
description: Use after code changes land -- checks documentation for staleness, contradictions, and missing coverage
---

# Review Docs

## Overview

After code changes, verify that documentation still matches reality. Stale docs are worse than no docs.

## Steps

### 1. Identify Changed Code

Review the diff or recent commits to understand what changed — new features, renamed APIs, changed behavior, removed functionality.

### 2. Find Affected Docs

Search for documentation that references the changed code:

```bash
rg "changed_thing" docs/ README.md *.md
```

Include docstrings, inline comments, and config files — not just standalone docs.

### 3. Review Categories

Check each affected document against these lenses:

- **Codebase match** — Does it describe current behavior? File paths correct?
- **Cross-doc consistency** — Do different docs agree with each other?
- **API & interface assumptions** — Are assumptions about APIs, tools, external services still correct?
- **Examples** — Do code examples still work?
- **Project standards** — Does it align with CLAUDE.md and documented conventions?

### 4. Report (severity-weighted)

```
## Doc Review: [scope]

### Summary
X docs checked | Y issues (Z critical, W high)

### CRITICAL
- **[Issue]** `file:line` — [what's wrong] → [fix]

### HIGH
- **[Issue]** `file:line` — [what's wrong] → [fix]

### MEDIUM / LOW
- ...

### Missing Coverage
- New feature X has no documentation

### Clean
- `docs/setup.md` — still accurate
```

### 5. Fix (after user reviews findings)

Review phase is read-only. Present findings first, then fix only after user says go.

## Red Flags

- Assuming docs are fine because tests pass
- Only checking README and ignoring inline docs/comments
- Skipping docstrings in changed functions
- Updating code without searching for affected docs
- Fixing docs before presenting findings
