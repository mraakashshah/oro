# gstack Pattern Adaptation Design

**Date:** 2026-03-23
**Status:** Draft — pending premortem + adversarial review

## Goal

Inject proven prompt engineering patterns from gstack into oro's 4 LLM contexts (architect, manager, worker, ops) to raise quality ceilings and prevent known failure modes.

## Context: Oro's 4 LLM Contexts

| Context | File | Role | Interactive? | Talks to user? |
|---------|------|------|-------------|----------------|
| Architect | `cmd/oro/architect.go` | Strategic: reads code, writes specs, creates beads | Yes (tmux pane) | Yes |
| Manager | `cmd/oro/manager.go` | Tactical: coordinates workers, handles escalations | Yes (tmux pane) | Yes |
| Worker | `pkg/worker/prompt.go` | Autonomous: executes beads | No | Never |
| Ops | `pkg/ops/review_prompt.go`, `escalation_prompt.go`, `ac_prompt.go` | One-shot: reviews, resolves, writes AC | No | Never |

## Pattern → Context Mapping

### 1. Anti-Sycophancy Rules

**What:** Explicit list of banned hedging phrases ("likely handled", "probably tested", "that's interesting", "could work") with required replacements (take a position, cite evidence).

**Where:**
- **Architect** — YES. Interacts with human, must take positions during brainstorming. Add to architect.go Anti-patterns section.
- **Manager** — YES. Reports status to human, must be direct. Add to manager.go Anti-patterns section.
- **Worker** — NO. Never talks to anyone. Would be wasted tokens.
- **Ops Review** — YES. Reviews must be decisive, not hedging. Add to review_prompt.go writePhases.

**Rationale:** Anti-sycophancy only matters when an LLM produces text a human (or another LLM acting on it) will read and act on. Workers produce code, not opinions.

### 2. Verification of Claims

**What:** "If you claim 'this is handled elsewhere' → cite the file and line. If you claim 'tests cover this' → name the test. Never say 'likely' or 'probably'."

**Where:**
- **Ops Review** — YES. Primary target. Reviews currently can say "this looks fine" without evidence. Add to writePhases after Critique section.
- **Ops AC Writer** — YES. AC must reference real files. Add to ac_prompt.go writeACPlaybook.
- **Architect** — PARTIAL. Already has "Never assume — always verify by reading the actual code." Strengthen.
- **Worker** — NO. Workers verify by running tests, not by citing evidence.
- **Manager** — NO. Manager acts on dispatcher messages, not on claims.

### 3. Anti-Rationalization Table

**What:** Table of common excuses and rebuttals: "Issue is simple" → "Simple issues have root causes too"; "Emergency, no time" → "Systematic is FASTER than thrashing."

**Where:**
- **Worker** — YES. Primary target. Workers are the ones tempted to skip process. Already partially in systematic-debugging skill but not in the always-visible prompt. Add to worker prompt Constraints section.
- **Architect** — NO. Architect doesn't write code or debug.
- **Manager** — NO. Manager doesn't implement.
- **Ops** — NO. Ops agents are one-shot with specific playbooks.

### 4. AskUserQuestion Format

**What:** 4-part structure: (1) Reground — state project/branch/task, (2) Simplify — plain English, (3) Recommend — with completeness score, (4) Options — lettered with effort estimates.

**Where:**
- **Architect** — YES. Primary target. Asks user questions during brainstorming/design. Add as new section in architect.go.
- **Manager** — YES. Asks user about priorities, scale decisions. Add as new section in manager.go.
- **Worker** — NO. Workers should never ask questions. They decide autonomously or create blocker beads.
- **Ops** — NO. One-shot agents output ACK/ESCALATE, not questions.

### 5. Constraint Specification (DO/NEVER stops)

**What:** Explicit lists of what DOES stop work (test failures, 3 failed hypotheses, security concerns) vs what NEVER stops work (style preferences, naming choices, trivial confirmations).

**Where:**
- **Worker** — YES. Primary target. Workers currently have a Constraints section (4 items) and Autonomy section (3 strategies) but no explicit DO/NEVER lists. Merge into existing sections.
- **Manager** — PARTIAL. Already has "Inform, don't ask" for routine ops and "Ask before" for major decisions. Could strengthen.
- **Architect** — NO. Architect works interactively with human.
- **Ops** — NO. One-shot with specific playbooks.

### 6. Cognitive Pattern Injection

**What:** Named mental models for thinking about decisions. CEO patterns (18: inversion reflex, focus as subtraction, speed calibration...) and Engineering patterns (15: boring by default, blast radius instinct, systems over heroes...).

**Where:**
- **Architect** — YES for engineering patterns. Architect makes design decisions that benefit from "boring by default", "essential vs accidental complexity", "blast radius instinct". Add subset (8-10 most relevant) to architect.go.
- **Ops Review** — YES for engineering patterns. Reviewer evaluates architecture decisions. Add subset to review_prompt.go.
- **Manager** — NO. Manager is tactical, not strategic.
- **Worker** — NO. Workers execute, they don't make strategic decisions.
- **CEO patterns** — SKIP. Oro is a code factory, not a startup. CEO patterns are for product/business decisions, not code.

### 7. Investigate Discipline (3-Strike Rule, Scope Lock, Pattern Table)

**What:** (1) After 3 failed hypotheses, STOP and escalate. (2) Lock edits to affected module after forming hypothesis. (3) Bug pattern table (race condition, nil propagation, state corruption, etc.).

**Where:**
- **Worker** — YES. Workers debug when tests fail. The 3-strike rule prevents infinite hypothesis loops. Add to worker prompt as part of Constraints.
- **Systematic-debugging skill** — YES. Enhance existing skill with all three patterns.
- **Ops Escalation** — PARTIAL. STUCK_WORKER playbook could reference 3-strike.
- **Architect/Manager** — NO. They don't debug.

### 8. Fix-First Heuristic

**What:** Classify review findings as AUTO-FIX (mechanical: formatting, unused imports, missing error check) vs ASK (judgment: architectural choice, API design). Auto-fix trivial issues, batch ambiguous ones.

**Where:**
- **Ops Review** — YES. Primary target. Currently reviews are binary APPROVED/REJECTED. Add Fix-First classification to writePhases.
- **Worker** — NO. Workers implement, they don't review.
- **Architect/Manager** — NO. They don't review code.

### 9. Scope Drift Detection

**What:** Compare stated intent (acceptance criteria) against actual diff before approving. Flag work that doesn't match.

**Where:**
- **Ops Review** — YES. Primary target. Add as Phase 1.5 between Understand and Critique: "Compare AC against diff. If the diff does work not described in AC, or AC describes work not in the diff → Critical finding."
- **Others** — NO.

### 10. Pushback Patterns (BAD/GOOD Examples)

**What:** Example pairs showing weak exploration vs rigorous diagnosis: "Founder: 'I'm building an AI tool'" → BAD: "That's a big market!" / GOOD: "There are 10,000 AI tools. What specific task..."

**Where:**
- **Architect** — YES. During brainstorming. Adapt from startup context to technical context (vague requirements → force specificity, "just add a flag" → demand design).
- **Brainstorming skill** — YES. Enhance with technical pushback patterns.
- **Manager/Worker/Ops** — NO.

### 11. Iron Law

**What:** "NO FIXES WITHOUT ROOT CAUSE INVESTIGATION FIRST."

**Where:**
- Already in `systematic-debugging` skill. Consider promoting to worker prompt for always-on visibility.
- **Worker** — YES. Add one-liner to Constraints: "No fixes without root cause. If a test fails, diagnose before changing code."
- **Others** — Already adequate through skills.

## Implementation Plan

### Group A: Worker Prompt Changes (`pkg/worker/prompt.go`)

Single bead. All changes to the same file:
- Anti-rationalization table (pattern 3)
- DO/NEVER constraint lists (pattern 5)
- 3-strike rule one-liner (pattern 7)
- Iron Law one-liner (pattern 11)

### Group B: Ops Review Changes (`pkg/ops/review_prompt.go`)

Single bead. All changes to the same file:
- Anti-sycophancy rules (pattern 1)
- Verification of claims (pattern 2)
- Fix-First heuristic (pattern 8)
- Scope drift detection (pattern 9)
- Engineering cognitive patterns subset (pattern 6)

### Group C: Architect Prompt Changes (`cmd/oro/architect.go`)

Single bead. All changes to the same file:
- AskUserQuestion format (pattern 4)
- Anti-sycophancy rules (pattern 1)
- Engineering cognitive patterns subset (pattern 6)
- Pushback patterns (pattern 10)

### Group D: Manager Prompt Changes (`cmd/oro/manager.go`)

Single bead. All changes to the same file:
- AskUserQuestion format (pattern 4)
- Anti-sycophancy rules (pattern 1)

### Group E: Skill Enhancements (markdown files)

Single bead. All skill changes:
- systematic-debugging: 3-strike rule, scope lock, pattern table (pattern 7)
- brainstorming: pushback patterns, anti-sycophancy (pattern 10)

## Token Budget

Each prompt has a context budget. Adding content means removing something or accepting the cost.

| Prompt | Current ~tokens | Additions ~tokens | % increase |
|--------|----------------|-------------------|------------|
| Worker | ~800 | ~200 (table + constraints) | 25% |
| Ops Review | ~600 | ~300 (5 patterns) | 50% |
| Architect | ~900 | ~400 (4 patterns) | 44% |
| Manager | ~1200 | ~150 (2 patterns) | 12% |

The ops review and architect prompts grow significantly. Monitor for quality degradation — if models start ignoring instructions, the prompt is too long.

## What We're NOT Doing

- **No CEO cognitive patterns.** Oro is a code factory, not a product company.
- **No /plan-design-review equivalent.** Oro has no UI to audit.
- **No /ship equivalent.** Oro's merge pipeline already handles this (QG → review → ff-merge).
- **No template generation system.** Oro's prompts are Go code, not markdown templates. Shared preambles would require a code refactor.
- **No telemetry.** Oro tracks events in the eventlog DB already.
- **No PreToolUse hooks.** Requires Claude Code infrastructure, not an oro-level change.
