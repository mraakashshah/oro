# Superpowers Learnings

_Distilled from obra/superpowers at v6.1.1._

## Overview

Superpowers is a complete software-development methodology for coding agents, built on a set of composable **skills** plus a small bootstrap that guarantees the agent actually uses them. It packages TDD, systematic debugging, Socratic design, and subagent-driven execution into workflows that trigger automatically, letting an agent work autonomously for hours without deviating from an approved plan.

The pitch: the moment the agent sees you're building something, it does _not_ jump into code. It steps back, teases a spec out of the conversation, shows it in digestible chunks, builds a plan "clear enough for an enthusiastic junior engineer with poor taste and no context to follow," then executes it through reviewed subagents. You don't do anything special — the skills fire on their own.

## Core Philosophy

1. **Process Before Code** — brainstorm and plan before implementing; a HARD-GATE blocks any implementation action until a design is presented and approved, regardless of perceived simplicity.
2. **Evidence Over Claims** — verification must precede any completion statement.
3. **Discipline Through Documentation** — skills encode non-negotiable workflows, hardened against rationalization.
4. **Complexity Reduction** — simplicity, YAGNI, and DRY as primary goals.
5. **"Your human partner"** — deliberate terminology (not "the user"); the agent's job is to protect its partner from bad outcomes, not merely execute requests.

## Skills as Process Documentation

Skills are NOT typical documentation. They are reusable reference guides for proven techniques, they trigger automatically when relevant, and they are themselves written and tested following TDD.

**SKILL.md structure:**
```markdown
---
name: skill-name
description: Triggering CONDITIONS only (third person, "Use when...", max 1024 chars)
---

# Skill Name
## Overview        — core principle in 1-2 sentences
## When to Use     — symptoms, triggers, small inline flowchart if non-obvious
## Core Pattern    — before/after for techniques
## Quick Reference — scannable table
## Common Mistakes — rationalization prevention
```

**Critical rule:** the description lists triggering CONDITIONS only, never a workflow summary. Testing showed that when a description summarizes the workflow, the agent follows the description instead of reading the full skill (a "code review between tasks" description caused ONE review instead of the two the body specifies). This is called Skill Discovery Optimization (SDO).

## Multi-Harness Packaging

A major shift since the early releases: Superpowers is no longer Claude-only. The same skills library now ships to **ten harnesses** — Claude Code, Antigravity, Codex App, Codex CLI, Cursor, Factory Droid, GitHub Copilot CLI, Kimi Code, OpenCode, and Pi. (Gemini CLI support was removed in v6.1.0.)

- Each harness gets its own plugin manifest directory (`.claude-plugin/`, `.codex-plugin/`, `.cursor-plugin/`, `.kimi-plugin/`, `.opencode/`, `.pi/`, `.agents/`), a `marketplace.json`, and its own install path. If you use more than one harness, you install separately for each.
- Distribution is via official marketplaces (Anthropic's `claude-plugins-official`, OpenAI's Codex plugin marketplace) and the project's own `obra/superpowers-marketplace`.
- Superpowers is **zero-dependency by design** — the maintainers reject any PR adding third-party dependencies unless it's new harness support.
- A per-harness sync/packaging toolchain (`scripts/sync-to-codex-plugin.sh`, `scripts/package-codex-plugin.sh`) mirrors the core skills into each harness's package format.

## The SessionStart Bootstrap

Skills only auto-trigger because a **SessionStart hook injects the `using-superpowers` skill** into context at the start of every session (and after `clear`/`compact`). This bootstrap is the load-bearing piece: without it the skills sit dead on disk, never invoked.

- `hooks/hooks.json` registers the hook with matcher `startup|clear|compact`. Matching only these events (not `resume`) was a deliberate fix so the bootstrap doesn't re-fire on session resume.
- `hooks/session-start` reads `using-superpowers/SKILL.md`, JSON-escapes it, and emits it as context. It branches on harness: Cursor wants `additional_context`, Claude Code wants nested `hookSpecificOutput.additionalContext`, Copilot CLI and the SDK standard want top-level `additionalContext` — it emits only the field the current platform consumes to avoid duplication.
- v6.1.x reworked how Codex handles this: the Codex portal package strips hooks (Codex auto-discovers an empty hooks object differently), and the SessionStart hook re-registration bug was fixed in v6.1.1. The bootstrap was also compressed for a leaner per-session cost.

`using-superpowers` is the only skill loaded eagerly; every other skill is pulled on demand via the harness's `Skill` tool. It carries a `<SUBAGENT-STOP>` guard so dispatched subagents ignore the "check for skills before anything" rule, and a Red Flags table of rationalizations that mean "stop, you're about to skip a skill."

## The Skills Ecosystem (14 core skills)

The library is intentionally small and general-purpose. Domain-specific, tool-specific, or project-specific skills are explicitly rejected from core — they belong in standalone plugins.

### Meta
- `using-superpowers` — the bootstrap; how to find and invoke skills, injected at session start
- `writing-skills` — create/edit skills via TDD, with adversarial pressure testing

### Testing
- `test-driven-development` — RED-GREEN-REFACTOR with anti-rationalization armor

### Debugging & Verification
- `systematic-debugging` — 4-phase root-cause process
- `verification-before-completion` — no success claims without fresh evidence

### Collaboration / Workflow
- `brainstorming` — Socratic refinement into a spec, presented in digestible sections
- `writing-plans` — specs into bite-sized (2-5 min) tasks with exact code and verification
- `executing-plans` — batched execution with human checkpoints (parallel session)
- `subagent-driven-development` — fresh subagent per task, reviewed, same session
- `dispatching-parallel-agents` — concurrent independent subagents
- `using-git-worktrees` — isolated branch workspaces
- `requesting-code-review` / `receiving-code-review` — review request and response
- `finishing-a-development-branch` — merge/PR/keep/discard decision workflow

Note the renames vs. earlier snapshots: `finishing` → `finishing-a-development-branch`; `using-superpowers` is the new bootstrap; several once-standalone techniques (root-cause-tracing, defense-in-depth, condition-based-waiting, testing anti-patterns) are now folded into their parent skills as reference files rather than separate skills.

## The Development Lifecycle

```
1. brainstorming (HARD-GATE before any code)
   ├─ Explore project context (files, docs, recent commits)
   ├─ Offer the visual companion just-in-time (not upfront)
   ├─ Ask clarifying questions one at a time
   ├─ Propose 2-3 approaches with trade-offs + recommendation
   ├─ Present design in sections scaled to complexity, approve each
   ├─ Write spec → docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md
   └─ Spec self-review + human review before proceeding

2. using-git-worktrees
   ├─ Check for existing worktrees, verify .gitignore
   ├─ Auto-detect project setup
   └─ Verify clean test baseline

3. writing-plans
   ├─ Bite-sized tasks (2-5 minutes each)
   ├─ Exact file paths + complete code + exact verification commands
   └─ Save to docs/superpowers/plans/YYYY-MM-DD-<feature>.md

4. Execution (two paths):
   Path A — subagent-driven-development (same session, autonomous)
   Path B — executing-plans (parallel session, human checkpoints between batches)

5. finishing-a-development-branch
   ├─ Verify all tests pass
   ├─ Present options (merge/PR/keep/discard)
   └─ Clean up worktree
```

Artifacts live under `docs/superpowers/` (specs and plans) in the working tree.

## The Iron Laws (Non-Negotiable)

Each discipline skill states its law as an absolute, then closes the loopholes.

### Test-Driven Development
- `NO PRODUCTION CODE WITHOUT A FAILING TEST FIRST`
- Write the test, watch it fail, write minimal code, watch it pass, commit.
- "Code before test? Delete it. Start over." — not "reference," not "adapt it," not "look at it." Delete means delete.
- **"Violating the letter of the rules is violating the spirit of the rules"** — cuts off the whole class of "I'm following the spirit" rationalizations.
- Explicit exceptions (ask your human partner): throwaway prototypes, generated code, config files.

### Systematic Debugging
- `NO FIXES WITHOUT ROOT CAUSE INVESTIGATION FIRST`
- Four phases, each a gate: (1) root-cause investigation, (2) pattern analysis, (3) hypothesis and testing, (4) implementation.
- Fix the root cause, not the symptom. If < 3 hypotheses tried and failing, return to Phase 1; if 3+ fixes fail, question the architecture.

### Verification Before Completion
- No completion claims without fresh verification evidence.
- "Should work" ≠ verified. "I'm confident" ≠ evidence. Run the command, read the output, THEN claim success.

## Skill Creation Methodology

**Writing skills IS TDD applied to process documentation.** Same Iron Law: `NO SKILL WITHOUT A FAILING TEST FIRST` — applies to new skills AND edits.

1. **RED**: run pressure scenarios with subagents WITHOUT the skill; document exact rationalizations verbatim.
2. **GREEN**: write the minimal skill addressing those specific failures.
3. **REFACTOR**: agents find a new loophole → add an explicit counter → re-test until bulletproof.

Testing types: academic questions (understanding), pressure scenarios (under stress), combined pressures (time + sunk cost + authority + exhaustion), edge cases. Skill-behavior evals run through the external `superpowers-evals` drill harness, which drives real tmux Claude Code / Codex sessions and judges compliance with an LLM verifier; plugin-infrastructure tests live in `tests/`.

### Match the Form to the Failure (newer meta-guidance)
Not every failure wants a prohibition. Classify the baseline failure first:

| Baseline failure | Right form | Wrong form |
|---|---|---|
| Skips/violates a rule under pressure | Prohibition + rationalization table + red flags | Soft "prefer…/consider…" |
| Complies but produces the wrong _shape_ (bloated prompt, buried verdict) | Positive recipe/contract: state what the output IS, in order | Prohibition list ("don't restate") |
| Omits a required element it already produces | Structural REQUIRED slot in the template | Prose reminders |
| Behavior should be conditional | Conditional keyed to an observable predicate | Unconditional rule + exemptions |

Key empirical finding: **prohibitions backfire on shaping problems.** In head-to-head wording tests, "don't X" produced _more_ of the unwanted content than a positive recipe — and worse than no guidance at all. No nuance clauses, no exemption clauses (they don't scope). Micro-test wording against a no-guidance control (5+ reps, read every match manually) before running expensive full pressure scenarios.

## Subagent-Driven Development (the autonomous engine)

The most sophisticated mode, and the one that has changed most. The controller delegates each task to a fresh subagent with precisely constructed context, so subagents never inherit session history and the controller preserves its own context for coordination.

```
Read plan once → note global constraints → create todos → pre-flight plan review
For each task:
  1. task-brief PLAN N  → extract task text to a file; dispatch implementer with brief + report paths
  2. Implementer asks questions? → answer, re-dispatch
  3. Implementer implements, tests, self-reviews, commits → returns status only
  4. review-package BASE HEAD → dispatch ONE task reviewer (spec compliance + code quality together)
  5. Critical/Important findings → dispatch fix subagent → re-review until clean
  6. Append "Task N: complete" to the progress ledger
After all tasks:
  → dispatch a broad whole-branch final review on the most capable model
  → finishing-a-development-branch
```

Notable evolutions vs. the old two-stage model:
- **Single per-task review, one broad final review.** Spec compliance and code quality are now a single task-scoped verdict per task; the sweeping cross-cutting review happens once at the end, on the most capable model.
- **File handoffs, not pasted text.** Everything pasted into a dispatch stays resident in context and is re-read every turn. Task briefs, implementer reports, review packages, and diffs move as files via helper scripts (`scripts/task-brief`, `scripts/review-package`). One real session ballooned a dispatch to 42k chars, 99% of it pasted history.
- **Model selection by role.** Use the least capable model that fits: cheapest tier for transcription-style tasks where the plan carries complete code; mid-tier floor for reviewers and prose-driven implementers; most capable for architecture and the final review. _Always specify the model explicitly_ — an omitted model silently inherits the session's expensive default. Turn count beats token price: cheap models take 2-3× the turns on multi-step work.
- **Durable progress ledger.** Conversation memory doesn't survive compaction; controllers have re-dispatched entire completed task sequences after losing their place. A ledger at `.superpowers/sdd/progress.md` (in the working tree, git-ignored) records completed commits so the controller can trust `git log` over its own recollection after compaction.
- **Pre-flight plan review.** Before Task 1, scan the plan for internal contradictions and plan-mandated defects (a test that asserts nothing, verbatim duplication), and batch them to the human as one question — rather than interrupting per discovery mid-plan.
- **Reviewer prompts are attention lenses, not verdicts.** Copy binding constraints verbatim into the reviewer's prompt; never pre-judge findings ("treat as Minor at most," "don't flag X") — that pre-rating is itself an anti-pattern. A plan-mandated finding is the human's call, presented alongside the plan text.
- **Continuous execution.** Don't check in between tasks; "Should I continue?" prompts waste the partner's time. Stop only on unresolvable BLOCKED status, genuine ambiguity, or completion.

Implementer status is one of DONE / DONE_WITH_CONCERNS / NEEDS_CONTEXT / BLOCKED, each with a defined controller response; never force the same model to retry a BLOCKED task without changing something.

## Skill Design Patterns

### Skill Discovery Optimization (SDO)
- Description = "Use when…" triggering conditions only; describe the _problem_ (race conditions), not language symptoms (setTimeout).
- Keyword coverage: error messages, symptoms ("flaky," "hanging," "zombie"), synonyms, real tool/command names.
- Active, verb-first names (`condition-based-waiting`, not `async-test-helpers`); gerunds for processes.

### Token Efficiency
Frequently-loaded skills target <200 words (getting-started workflows <150). Push details to `--help`, cross-reference other skills by name (never `@`-link — that force-loads 200k+ context), and compress examples.

### Rationalization Prevention (discipline skills only)
Close every loophole explicitly, state the letter-vs-spirit principle early, build a rationalization table from real baseline testing, and provide a Red Flags self-check list. Persuasion principles (Cialdini; authority, commitment, scarcity, social proof, unity) are applied deliberately to make the guidance stick.

## The Brainstorming Visual Companion

`brainstorming` ships an optional browser-based visual companion (a small local server) offered _just-in-time_ — only the first time a question would genuinely be clearer shown than described, never upfront. It carries lightweight telemetry: the Prime Radiant logo is fetched from the project's site and reports only the Superpowers version in use (no project, prompt, or click data), giving the maintainers a rough usage count. Disable with `SUPERPOWERS_DISABLE_TELEMETRY`; it also honors Claude Code's `DISABLE_TELEMETRY` and `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`.

## Supporting Techniques (now folded into parent skills)

- **Root-cause tracing** — trace bugs backward through the call stack, add instrumentation when you can't trace manually, fix at source (often 5+ levels deep).
- **Defense-in-depth validation** — after a fix, add validation at entry point, business logic, environment guards, and debug instrumentation.
- **Condition-based waiting** — replace arbitrary timeouts with event-based polling; critical for flaky tests.

## Contribution Culture (a distinctive signal)

The repo openly states a **94% PR rejection rate** and treats agent-generated slop as the primary threat. This shapes the whole system: contributions must target the `dev` branch, complete every PR-template section with real answers, disclose the exact model/harness/version/plugins that produced them, and prove new-harness integrations with a transcript showing `brainstorming` auto-triggers on "Let's make a react todo list." Skill changes require eval evidence — the maintainers explicitly reject "compliance" rewrites toward Anthropic's published skill-authoring guidance, because their own content is tuned against real agent behavior and differs on purpose. Domain-specific skills, third-party dependencies, and fork-specific changes are out of scope for core.

## Key Insight

The separation of "what to do" (skill body) from "when to do it" (description field), enforced by a SessionStart bootstrap that guarantees the agent checks for skills before acting, is what makes autonomous multi-hour sessions possible. Everything else — file handoffs, model tiers, progress ledgers, rationalization tables — exists to keep that autonomy from degrading: to stop context from bloating, models from over-spending, memory from evaporating at compaction, and smart agents from rationalizing their way out of discipline.
