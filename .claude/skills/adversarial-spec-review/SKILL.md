---
name: adversarial-spec-review
description: Use after writing a spec or decomposing into beads — adversarially verifies the spec is complete, wired, and will actually work when all beads pass
user-invocable: true
---

# Adversarial Spec Review

## Overview

Stress-test a spec or bead decomposition by actively trying to construct failure scenarios. The goal is to find a scenario where every bead passes individually but the feature still doesn't work.

> "Imagine every bead passes its QG, every review approves, every merge succeeds. The feature still doesn't work. Why?"

If you can answer that question, the spec has a gap. Fix it before execution begins.

**This is Gate 1 (design) and Gate 2 (coverage) of the Ralph Loop pipeline.**

## Scope — What Is and Is Not a Finding

**Self-check for every finding before writing it down:**
> "If this gap existed and all beads passed, would the feature be visibly broken or untestable?"
> If **no** → not a finding. Drop it.

**In scope (structural failures):**
- Wiring gaps — component built but never called
- Missing bead — no one delivers a required criterion
- Untestable acceptance — can't write a binary pass/fail command
- Format mismatch across a boundary — producer and consumer disagree
- Cross-cutting concern with no covering bead

**Out of scope (handled elsewhere):**
- Error message wording, naming, style → quality gate catches these
- Minor missing error handling → workers use judgment; QG and code review handle it
- "You should also add X" suggestions → feature creep, not spec gaps
- Logging format, output prettiness → not structural
- Anything already covered by the project quality gate or ops review

When in doubt, mark it `minor` in negative_space and do not let it affect the verdict.

## When to Use

- After writing a spec, before decomposing into beads (design gate)
- After decomposing into beads, before starting execution (coverage gate)
- After fix beads land, to re-verify (the Ralph Loop)
- When an epic has failed integration and you need to find why

**Do NOT use for:**
- Individual beads (they have QG + review)
- Trivial specs (< 3 beads, single package, no cross-cutting concerns)

## The Six Checks

Run in order. Each check builds on the previous.

### Check 1: Write The Acceptance Test First

Before reading the spec in detail, attempt to write the epic's machine-verifiable acceptance test from the spec's goal alone.

```
Cmd: <exact command to verify the feature works>
Assert: <exact expected output or exit code>
```

**Rules:**
- The command must run against `main` (not a branch — the feature isn't done until it's on main)
- The command must be a single shell command or test invocation
- "Assert" must be binary — pass/fail, no human judgment

**If you can't write the test, the spec is underspecified.** This is a CRITICAL finding. Stop and fix the spec before continuing.

**If the spec already has an acceptance test, verify it's adequate:**
- Does it actually test the feature, or just a component?
- Would it catch a wiring failure (component exists but isn't called)?
- Does it run against main?

### Check 2: Trace The Call Chain (Wiring Audit)

For every new component the spec describes, trace the ACTUAL call chain from the system's entry point to where the new code must execute.

**Procedure:**
1. Identify the entry point (CLI command, HTTP handler, message handler, event trigger)
2. Read the actual source file at the entry point
3. Follow the call chain through each function, reading actual source at each step
4. Identify the exact location where the new code must be called
5. Check: is there a bead that adds this call? Is the call site explicitly mentioned in any bead's `Read:` field?

**You must read actual source files, not descriptions.** The spec may say "worker calls BuildEpicDecompositionPrompt" but if you read `worker.go:buildAssignPrompt`, you'll see it never checks `IsEpicDecomposition`. That's the wiring gap.

**For each new component, produce:**
```
Entry: cmd/oro/main.go → runWork()
Chain: runWork → executeWork → spawnAndWait → worker.AssemblePrompt
Gap:   AssemblePrompt calls buildAssignPrompt, which never checks IsEpicDecomposition
Bead:  [missing — no bead wires the prompt into the call site]
```

If you can't trace the full chain for any component, that's a finding.

### Check 3: Requirements Traceability Matrix

For every acceptance criterion in the spec, map it to:
1. Which bead delivers it
2. Which test verifies it
3. Current status

```
| # | Criterion              | Bead     | Test                        | Status  |
|---|------------------------|----------|-----------------------------|---------|
| 1 | Epics route to decomp  | oro-abc  | TestDispatcherRoutesEpics   | covered |
| 2 | Worker uses decomp prompt | ???   | ???                         | GAP     |
| 3 | Children created       | oro-def  | TestChildBeadsCreated       | covered |
```

Rules:
- Every criterion must map to at least one bead. No bead = GAP.
- Every criterion must map to at least one test. No test = GAP.
- A bead that "partially" covers a criterion counts as a GAP — partial is not done.

### Check 4: Negative Space Analysis

For each component or behavior the spec describes, ask:

1. **What happens on error?** If the happy path is specified but the error path isn't, that's a gap. Workers will guess, and they'll guess wrong.
2. **What happens with bad/missing input?** Nil, empty, malformed, oversized.
3. **What happens concurrently?** Two requests at once, two beads editing the same file, race conditions between components tested in isolation.
4. **What happens when a dependency is unavailable?** Network down, DB locked, file missing, process crashed.
5. **What's the cleanup/rollback story?** If the feature fails halfway, what state is left? Can the user recover?

For each gap found, determine: is this a missing bead, a missing acceptance criterion on an existing bead, or an acceptable risk that should be documented?

### Check 5: Red Team

**Your job is to break it.** Actively construct scenarios where all individual beads pass but the feature doesn't work.

Common failure patterns to hunt for:

| Pattern | Example | How to detect |
|---------|---------|---------------|
| **Unwired component** | Function exists but never called | Check 2 catches this |
| **Format mismatch** | Producer outputs JSON, consumer expects protobuf | Check bead boundaries |
| **Test-only code** | Marked `//testonly` or behind build tag | Grep for build constraints |
| **Branch-only code** | Code committed to branch, never merged to main | Check acceptance runs on main |
| **Config gap** | Code works but config/flag/env not set | Check initialization path |
| **Import cycle** | New package can't import required dependency | Check Go import graph |
| **Order dependency** | Works if A runs before B, fails if B runs first | Check for implicit ordering |
| **Partial migration** | New path works, old path still active, conflict | Check for dual code paths |

**For each scenario you construct:**
```
Scenario: Worker builds epic decomposition prompt but buildAssignPrompt ignores IsEpicDecomposition
Beads pass: Yes — prompt.go has tests, worker.go has tests, both green
Feature works: No — prompt is never used in production
Root cause: No bead covers the wiring between prompt.go and worker.go
Fix: Add wiring bead with Read: worker.go:buildAssignPrompt, prompt.go:BuildEpicDecompositionPrompt
```

### Check 6: Integration Point Inventory

List every existing file/function in the codebase that this feature MUST touch to work. Not files the spec mentions — files the CODE requires.

**Procedure:**
1. From Check 2's call chains, collect every file in the path
2. For each file, check: does any bead list this in its `Read:` field?
3. For files not in any bead's `Read:` — is the file affected by this feature?
4. If yes and no bead covers it → GAP

This catches the files nobody thought about — the router that needs a new route, the config that needs a new flag, the init function that needs to register a new handler.

## Output Format

```yaml
verdict: PASS | FAIL
spec: "<spec path or epic ID>"
reviewer_note: "<one sentence summary>"

acceptance_test:
  cmd: "<command>"
  assert: "<expected>"
  adequate: true | false
  issues: ["<if not adequate, why>"]

traceability:
  covered: N
  gaps: N
  matrix: |
    | # | Criterion | Bead | Test | Status |
    ...

wiring_gaps:
  - entry: "<entry point>"
    chain: "<call chain>"
    gap: "<where it breaks>"
    fix: "<what bead is needed>"

negative_space:
  - area: "<what's unspecified>"
    severity: critical | important | minor
    fix: "<missing bead or criterion>"

red_team_scenarios:
  - scenario: "<description>"
    beads_pass: true
    feature_works: false
    root_cause: "<why>"
    fix: "<what to add>"

integration_points:
  covered: ["<file:function>"]
  uncovered:
    - file: "<path>"
      reason: "<why it matters>"
      fix: "<what bead should cover it>"
```

## Verdict Rules

- **Any wiring gap** → FAIL (this is how epic decomp shipped broken)
- **Any red team scenario with no covering bead** → FAIL
- **Acceptance test can't be written** → FAIL
- **3+ uncovered criteria in traceability matrix** → FAIL
- **1-2 minor negative space gaps** → PASS with notes
- **All checks clean** → PASS

## After Review

If FAIL:
1. Present findings to user with specific fixes
2. Create beads for each gap (use `bead-craft` create mode)
3. Re-run this review after fixes are applied (the Ralph Loop)

If PASS:
1. Commit the acceptance test as the epic's criterion: `bd update <epic> --acceptance-criteria "Cmd: ... | Assert: ..."`
2. Proceed to execution

## Running as a Subagent

This review should be run by a **fresh-context agent** when possible — the person who wrote the spec is the worst reviewer of the spec. When spawning:

```
Task: adversarial-spec-review
Prompt: "Read <spec path>. Read the actual source files for affected packages.
         Run all 6 checks from the adversarial-spec-review skill.
         Return the full output in the specified YAML format."
```

The agent reads the spec, reads the code, produces the review. Fresh eyes, no assumptions carried from the design conversation.

## Red Flags

- Skipping Check 2 (wiring audit) — this is the #1 failure mode in practice
- Reading spec descriptions instead of actual source files
- Saying "PASS" without constructing at least one red team scenario
- Trusting bead titles instead of reading bead acceptance criteria
- Assuming "covered" without tracing the actual call chain
- Running this on individual beads (wrong tool — use QG + review for that)
- Reviewer who wrote the spec reviewing their own spec (use a subagent)
- Flagging style, naming, or error message wording as findings — use the self-check
- Letting minor negative_space items inflate a PASS into a FAIL
