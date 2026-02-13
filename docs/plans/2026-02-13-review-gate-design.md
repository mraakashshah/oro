# Code Review Gate Design

**Date:** 2026-02-13
**Status:** Approved

## Goal

Upgrade the existing ops review agent from a 4-line acceptance-criteria check into a full code review gate that catches design issues, missing error handling, architecture misfit, and test quality problems that automated tools miss.

## Approach

**Option chosen:** Upgrade ops review prompt + model (Approach A). Zero new infrastructure — the `READY_FOR_REVIEW` → ops agent → verdict → rejection cycle already exists.

**Rejected alternatives:**
- Two-phase review (ops + architect) — architect becomes bottleneck, serial reviews
- Dedicated review subagent with rich context — marginal gain over A with good prompt

## Design

### Review Prompt

The 4-line `buildReviewPrompt()` in `pkg/ops/ops.go` becomes a structured two-phase review prompt assembled at runtime from project files.

**Prompt structure:**

```
You are reviewing code changes before merge to {base_branch}.
The quality gate already passed. Do NOT check formatting, linting,
compilation, or test pass/fail. Focus on what automated tools miss.

## Context
Bead: {bead_id} — {title}
Acceptance: {acceptance_criteria}

## Project Standards
{contents of CLAUDE.md}
{contents of .claude/rules/*.md}

## Known Anti-Patterns
{contents of .claude/review-patterns.md, if it exists}

## Phase 1: Understand
1. git diff {base_branch} --stat (scope)
2. git diff {base_branch} (changes)
3. For each modified file: read the full file (if >300 lines, read
   modified sections with 50 lines of surrounding context)
4. Read the test file(s) — these are the spec. Understand what
   behavior is being claimed before evaluating the implementation.
5. Read 2-3 neighboring files in the same package (architecture context)
6. Summarize the INTENT in one sentence.
   If you cannot articulate the intent → Critical finding.

## Phase 2: Critique
Classify each finding: Critical / Important / Minor.

- **Absence**: For each public function, trace error paths — are they
  all handled? For each test, do error and boundary cases exist?
- **Adversarial**: What input breaks this? What call sequence hits a
  race condition or deadlock?
- **Design**: Right abstraction? Right coupling level? Single
  responsibility?
- **Architecture fit**: Consistent with neighboring files' patterns?
  Right package for this code?
- **Test-as-spec**: Can you understand the feature by ONLY reading
  the test? If not, the test is underspecified — Important finding.
- **Anti-patterns**: Does any pattern from the list above apply?

## Verdict
- Any Critical → REJECTED
- 2+ Important → REJECTED
- 1 Important → REJECTED if fix is a small, localized change
- Minor only → APPROVED

## Output
APPROVED or REJECTED

Findings as: [severity] file:line — description and specific fix.

If APPROVED and you noticed a recurring pattern worth capturing,
add a line:
PATTERN: tag: trigger → fix
```

### Pattern Capture (Living Anti-Pattern File)

When the reviewer outputs `PATTERN:` lines, the dispatcher appends them to `.claude/review-patterns.md`. Format:

```
loose-match: strings.Contains for exact lookup → use map key or ==
prefix-leak: env filtering with == misses VAR_FOO → use HasPrefix
happy-only: test covers happy path only → add error/boundary cases
```

One line per pattern, ~15 tokens each. File grows organically, reviewer gets smarter over time.

### Model Routing

| Operation | Current | New |
|-----------|---------|-----|
| Worker (first attempt) | Sonnet | Sonnet |
| Ops Review | Sonnet | **Opus** |
| Worker (after review rejection) | Sonnet | **Opus** |
| QG retry | Sonnet | Sonnet |
| Merge conflict | Opus | Opus |

### Model Escalation on Rejection

When the reviewer rejects, the dispatcher re-assigns the bead to the worker with:
- `Model` upgraded to Opus
- Rejection feedback in `MemoryContext`
- Incremented `Attempt` counter

Rationale: Sonnet handles most beads. When a design issue is caught, Opus is better at incorporating targeted critique.

## Premortem

**Tiger — false rejection rate:** Opus reviewer may be too opinionated, rejecting "good enough" code.
*Mitigation:* Verdict rules are specific — Minor-only always approves. Only Critical or multiple Important findings reject. Prompt explicitly says don't duplicate QG checks.

**Elephant — cost:** Opus review on every bead adds API spend.
*Accepted:* Quality improvement justifies cost. Review is short (single prompt, ~60-90s). Relative to worker runtime (5-15 min), review cost is marginal.

## Implementation

| File | Change |
|------|--------|
| `pkg/ops/ops.go` | Replace `buildReviewPrompt()` with call to new prompt builder. Change review model to Opus. Parse `PATTERN:` lines from output. |
| `pkg/ops/review_prompt.go` | **New file.** Prompt assembly: reads CLAUDE.md, .claude/rules/*, .claude/review-patterns.md, templates the full prompt. |
| `pkg/dispatcher/dispatcher.go` | `handleReviewResult()`: append captured patterns to review-patterns.md. `handleReviewRejection()`: set Model to Opus on re-assign. |
| `pkg/protocol/message.go` | Add `Patterns []string` to `ReviewResultPayload`. |
