---
name: premortem
description: Use when a plan or design is approved but before implementation begins, to identify failure modes and risks
---

# Pre-Mortem

## Overview

Identify failure modes before they occur. Based on Gary Klein's technique.

> "Imagine it's 3 months from now and this project has failed spectacularly. Why did it fail?"

## Risk Categories (Shreyas Doshi Framework)

| Category | Symbol | Meaning |
|----------|--------|---------|
| **Tiger** | `[TIGER]` | Clear threat that will hurt us if not addressed |
| **Paper Tiger** | `[PAPER]` | Looks threatening but probably fine |
| **Elephant** | `[ELEPHANT]` | Thing nobody wants to talk about |

## Two-Pass Verification

**CRITICAL: Do NOT flag risks based on pattern-matching alone.**

**Pass 1 — Gather potential risks** (hypotheses only)

**Pass 2 — Verify each one:**
- Read ±20 lines around the finding
- Check for fallback/error handling
- Confirm it's in scope (not test/dev-only code)
- Look for existing mitigations

If ANY verification check fails, it's not a tiger.

Every tiger MUST include evidence:
```yaml
tiger:
  risk: "<description>"
  location: "file.go:42"
  severity: high|medium
  mitigation_checked: "No fallback found, no error handling, no retry"
```

## Quick Checklist (Plans, PRs)

1. What's the single biggest thing that could go wrong?
2. Any external dependencies that could fail?
3. Is rollback possible if this breaks?
4. Edge cases not covered in tests?
5. Unclear requirements that could cause rework?

## Deep Checklist (Before Implementation)

**Technical:** Scalability, dependencies + fallbacks, data consistency, latency, security, error handling

**Integration:** Breaking changes, migration path, rollback strategy, feature flags

**Process:** Requirements clarity, stakeholder input, tech debt tracking, maintenance burden

**Testing:** Coverage gaps, integration tests, load testing, manual test plan

## Output Format

```yaml
premortem:
  mode: quick|deep
  context: "<what was analyzed>"

  tigers:
    - risk: "<description>"
      severity: high|medium
      mitigation_checked: "<what was NOT found>"

  elephants:
    - risk: "<unspoken concern>"

  paper_tigers:
    - risk: "<looks scary>"
      reason: "<why it's fine — cite the mitigation>"
```

## Severity Thresholds

| Severity | Blocking? | Action |
|----------|-----------|--------|
| HIGH | Yes | Must address or explicitly accept |
| MEDIUM | No | Recommend addressing |
| LOW | No | Note for awareness |

## After Analysis

Present findings to the user with options:
1. **Accept risks and proceed** — acknowledged, not blocking
2. **Add mitigations to plan** — update plan before building
3. **Research mitigation options** — investigate solutions
4. **Discuss specific risks** — talk through concerns

## Red Flags

- Flagging risks without reading surrounding code
- Trusting grep results as evidence of missing handling
- Marking dev-only or test code as production risks
- Skipping Pass 2 verification
