# Superpowers Learnings

## Overview

Sophisticated system for guiding AI agents through structured software development workflows. Combines TDD principles, systematic debugging, and careful process design for autonomous work with minimal supervision.

## Core Philosophy

1. **Process Before Code** - Agents brainstorm and plan before implementing
2. **Evidence Over Claims** - Verification must precede any completion statements
3. **Discipline Through Documentation** - Skills encode non-negotiable workflows

## Skills as Process Documentation

Skills are NOT typical documentation. They are:
- Reusable reference guides for proven techniques
- Triggered automatically when relevant
- Written following TDD principles

**Skill Structure:**
```markdown
---
name: skill-name
description: Triggering CONDITIONS only (max 1024 chars)
---

# When to Use
Symptoms and triggers

# Overview
Core principle

# Steps
Implementation guidance

# Common Mistakes
Rationalization prevention
```

**Critical:** Description lists triggering CONDITIONS only, never workflow summary. Testing showed Claude follows description instead of reading full content.

## The Skills Ecosystem (15+ Core)

### Process Skills (invoke before action)
- `brainstorming` - Socratic refinement into specs (200-300 word sections)
- `writing-plans` - Converts specs into 2-5 minute tasks
- `executing-plans` - Batched execution with review checkpoints
- `using-git-worktrees` - Isolated workspaces with safety verification

### Implementation Skills (invoke during execution)
- `test-driven-development` - RED-GREEN-REFACTOR with anti-rationalizations
- `subagent-driven-development` - Fresh subagent per task with two-stage review
- `dispatching-parallel-agents` - Parallelizes independent investigations

### Quality Skills (invoke when uncertain)
- `systematic-debugging` - 4-phase root cause process
- `requesting-code-review` - Pre-review checklist with spec then quality gates
- `verification-before-completion` - No success claims without fresh verification

## The Development Lifecycle

```
1. Brainstorming (required before code)
   ├─ Understand project context
   ├─ Ask clarifying questions (one at a time)
   ├─ Explore 2-3 approaches
   └─ Present design in 200-300 word sections

2. Using Git Worktrees
   ├─ Check for existing worktree directories
   ├─ Verify directory is in .gitignore
   ├─ Auto-detect project setup
   └─ Verify clean test baseline

3. Writing Plans
   ├─ Create bite-sized tasks (2-5 minutes each)
   ├─ Include exact file paths and complete code
   ├─ Specify exact verification commands
   └─ Save to docs/plans/YYYY-MM-DD-<feature>.md

4. Execution (two paths):

   Path A: Subagent-Driven (same session)
   ├─ Fresh subagent per task
   ├─ Two-stage review: spec compliance, then code quality
   └─ Faster iteration

   Path B: Executing Plans (parallel session)
   ├─ Execute batches of 3 tasks
   └─ Human feedback between batches

5. Finishing
   ├─ Verify all tests pass
   ├─ Present 4 options (merge/PR/keep/discard)
   └─ Execute cleanup
```

## The Iron Laws (Non-Negotiable)

### Test-Driven Development
- Write test FIRST, watch it fail, write minimal code
- "Code before test? Delete it. Start over."
- No exceptions: not "simple code", not "just this once"

**Anti-rationalizations explicitly listed:**
- Tests-after aren't TDD
- Sunk cost fallacy
- "Pragmatism" excuse

### Systematic Debugging
- ALWAYS find root cause before proposing fixes
- 4-phase process: root cause → pattern analysis → hypothesis → implementation
- If 3+ fixes fail → question the architecture

### Verification Before Completion
- No completion claims without fresh verification evidence
- "Should work" ≠ verified
- "I'm confident" ≠ evidence
- Run the command, read the output, THEN claim success

## Subagent-Driven Development

The most sophisticated autonomous mode:

```
For each task:
1. Dispatch implementer subagent with full task text
2. Answer subagent questions before implementation
3. Implementer implements, tests, self-reviews, commits
4. Dispatch spec-reviewer subagent (spec compliance)
5. If issues: implementer fixes, reviewer re-reviews
6. Dispatch code-quality-reviewer (code quality)
7. If issues: implementer fixes, reviewer re-reviews
8. Mark task complete
```

**Two-stage review ensures:**
- Spec compliance prevents over/under-building
- Code quality prevents technical debt
- Review loops ensure fixes work
- Self-review catches obvious issues first

## Skill Design Patterns

### Claude Search Optimization (CSO)
- "Use when..." format focuses on triggering conditions
- Symptoms and context keywords for search
- Technology-agnostic unless skill-specific
- NO workflow summaries

**Good:** "Use when executing implementation plans with independent tasks"
**Bad:** "Execute plan by dispatching subagent per task with code review" (causes ONE review instead of TWO)

### Token Efficiency
Frequently-loaded skills target <200 words:
- Use cross-references to other skills
- Move heavy reference to separate files
- Compress examples

### Rationalization Prevention
For discipline-enforcing skills:
- Close every loophole explicitly
- Address "spirit vs letter" arguments upfront
- Build rationalization tables from testing
- Create red flags list

## Supporting Techniques

### Root Cause Tracing
- Trace bugs backward through call stack
- Add instrumentation when can't trace manually
- Fix at source, not symptom
- Often requires 5+ levels of tracing

### Defense-in-Depth Validation
After fixing a bug, add validation at 4 layers:
1. Entry point validation
2. Business logic validation
3. Environment guards
4. Debug instrumentation

### Condition-Based Waiting
- Replace arbitrary timeouts with event-based polling
- Critical for flaky tests
- Requires understanding when condition becomes true

## Skill Creation Methodology

TDD applied to documentation:

1. **RED**: Run pressure scenarios WITHOUT skill, document baseline
2. **GREEN**: Write minimal skill addressing failures
3. **REFACTOR**: Close loopholes through testing

**Testing types:**
- Academic questions (understanding)
- Pressure scenarios (under stress)
- Combined pressures (time + sunk cost + exhaustion)
- Edge cases and variations

## Real-World Results

- Autonomous sessions lasting 2+ hours without deviation
- Fresh subagent per task eliminates context pollution
- Two-stage review catches issues before cascade
- Systematic debugging: 15-30 min vs 2-3 hours random fixes
- First-time fix rate: 95% vs 40%

## Key Insight

The separation of "what to do" from "when to do it" (in the description field) ensures agents read full context before acting. This is an elegant pattern for constraining AI behavior through well-designed process documentation.
