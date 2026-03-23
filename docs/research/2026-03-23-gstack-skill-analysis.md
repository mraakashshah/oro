# gstack Skill System Analysis

**Date:** 2026-03-23
**Analyst:** oro research session
**Sources:** `/Users/as21/codehouse/oro/archive/yap/reference/gstack/` (gstack), `~/.claude/skills/` and `/Users/as21/codehouse/oro/.claude/skills/` (oro)

---

## Executive Summary

gstack is a skill system built around a headless Chromium browser daemon, designed for Y Combinator's founder-facing workflow. It structures 25+ skills into SKILL.md template files with a code-generated preamble, hook-based safety guardrails, cross-skill artifact persistence via `~/.gstack/projects/`, and a review readiness dashboard that gates shipping. Its key innovations are: (1) template-based doc generation from source code metadata, (2) PreToolUse hooks for safety enforcement, (3) cognitive pattern injection in plan review skills, (4) anti-sycophancy rules embedded directly in prompts, and (5) a multi-tier eval system testing skills with real Claude sessions.

oro's skill system is structurally simpler: 35+ plain Markdown skills in `~/.claude/skills/`, dispatched by name via a `using-skills` index. oro's strengths are its programmatic worker prompt assembly (`pkg/worker/prompt.go`), bead-based issue tracking, worktree isolation, and ops review system (`pkg/ops/review_prompt.go`). oro lacks gstack's template generation, hook-based safety, cross-session artifact persistence, and structured review chaining.

---

## Skill Anatomy

### gstack File Structure

Every gstack skill is a directory containing at minimum:

```
skill-name/
  SKILL.md.tmpl    # Source template (human-authored + placeholders)
  SKILL.md         # Generated output (committed, read by Claude)
```

Some skills add executable infrastructure:

```
careful/
  SKILL.md.tmpl
  SKILL.md
  bin/
    check-careful.sh   # PreToolUse hook script
```

Source: `ARCHITECTURE.md` lines 179-209:
> ```
> SKILL.md.tmpl          (human-written prose + placeholders)
>        |
> gen-skill-docs.ts      (reads source code metadata)
>        |
> SKILL.md               (committed, auto-generated sections)
> ```

### Frontmatter

Every SKILL.md.tmpl starts with YAML frontmatter defining the skill's metadata and permissions:

From `investigate/SKILL.md.tmpl` lines 1-21:
```yaml
---
name: investigate
version: 1.0.0
description: |
  Systematic debugging with root cause investigation. Four phases: investigate,
  analyze, hypothesize, implement. Iron Law: no fixes without root cause.
  Use when asked to "debug this", "fix this bug", "why is this broken",
  "investigate this error", or "root cause analysis".
  Proactively suggest when the user reports errors, unexpected behavior, or
  is troubleshooting why something stopped working.
allowed-tools:
  - Bash
  - Read
  - Write
  - Edit
  - Grep
  - Glob
  - AskUserQuestion
  - WebSearch
hooks:
  PreToolUse:
    - matcher: "Edit"
      hooks:
        - type: command
          command: "bash ${CLAUDE_SKILL_DIR}/../freeze/bin/check-freeze.sh"
          statusMessage: "Checking debug scope boundary..."
```

Key elements:
- **`name`**: Used for invocation and routing
- **`version`**: Semantic versioning for skill evolution
- **`description`**: Rich text that (a) describes the skill, (b) lists trigger phrases ("Use when asked to..."), and (c) specifies proactive suggestion triggers
- **`allowed-tools`**: Whitelist of Claude tools the skill may use
- **`hooks`**: PreToolUse hooks that run shell scripts before specified tools execute
- **`benefits-from`**: Optional field declaring upstream skill dependencies (e.g., `plan-eng-review` benefits from `office-hours`)

### The Preamble

Every skill begins with `{{PREAMBLE}}`, a generated block that handles five cross-cutting concerns in a single bash command. From `ARCHITECTURE.md` lines 211-219:

> 1. **Update check** -- calls `gstack-update-check`, reports if an upgrade is available.
> 2. **Session tracking** -- touches `~/.gstack/sessions/$PPID` and counts active sessions.
>    When 3+ sessions are running, all skills enter "ELI16 mode" -- every question re-grounds
>    the user on context because they're juggling windows.
> 3. **Contributor mode** -- reads `gstack_contributor` from config.
> 4. **AskUserQuestion format** -- universal format: context, question, `RECOMMENDATION: Choose X
>    because ___`, lettered options.
> 5. **Search Before Building** -- three layers of knowledge framework from ETHOS.md.

### oro Skill Structure

oro skills are single-file Markdown documents with minimal frontmatter:

From `~/.claude/skills/test-driven-development/skill.md` lines 1-4:
```yaml
---
name: test-driven-development
description: Use when implementing any feature or bugfix, before writing implementation code
---
```

oro has no template generation, no hook system, no version field, no allowed-tools whitelist, and no preamble injection. Skills are plain Markdown read directly by Claude Code.

---

## Skill Discovery and Routing

### gstack Routing

gstack uses two routing mechanisms:

**1. Description-based trigger matching.** Every skill description includes natural language triggers. From the root `SKILL.md.tmpl` lines 13-29:

```
  - Brainstorming a new idea -> suggest /office-hours
  - Reviewing a plan (strategy) -> suggest /plan-ceo-review
  - Reviewing a plan (architecture) -> suggest /plan-eng-review
  - Reviewing a plan (design) -> suggest /plan-design-review
  - Creating a design system -> suggest /design-consultation
  - Debugging errors -> suggest /investigate
  - Testing the app -> suggest /qa
  - Code review before merge -> suggest /review
  - Visual design audit -> suggest /design-review
  - Ready to deploy / create PR -> suggest /ship
  - Post-ship doc updates -> suggest /document-release
  - Weekly retrospective -> suggest /retro
  - Wanting a second opinion or adversarial code review -> suggest /codex
  - Working with production or live systems -> suggest /careful
  - Want to scope edits to one module/directory -> suggest /freeze
  - Maximum safety mode (destructive warnings + edit restrictions) -> suggest /guard
  - Removing edit restrictions -> suggest /unfreeze
  - Upgrading gstack to latest version -> suggest /gstack-upgrade
```

**2. Proactive suggestion with opt-out.** From `SKILL.md.tmpl` lines 32-40:
```
  If the user pushes back on skill suggestions ("stop suggesting things",
  "I don't need suggestions", "too aggressive"):
  1. Stop suggesting for the rest of this session
  2. Run: gstack-config set proactive false
  3. Say: "Got it -- I'll stop suggesting skills. Just tell me to be proactive
     again if you change your mind."
```

**3. `.agents/skills/` symlink directory.** gstack also publishes skills as separate entries under `.agents/skills/gstack-*` for agent-level discovery (25 entries found).

### oro Routing

oro uses a centralized `using-skills` skill that acts as a router. From `~/.claude/skills/using-skills/skill.md` lines 22-31:

```
**Discipline:** test-driven-development, systematic-debugging, verification-before-completion,
  observe-before-editing, destructive-command-safety
**Workflow:** brainstorming, writing-plans, executing-plans, requesting-code-review,
  receiving-code-review, finishing-work, review-implementation, review-docs
**Orchestration:** dispatching-parallel-agents, workflow-routing, premortem, completion-check, explore
**Tools:** beads, git-commits, tmux, github, session-logs, agent-browser
**Continuity:** create-handoff, resume-handoff, documenting-solutions, refactor,
  using-git-worktrees, writing-skills, context-checkpoint
**Beads:** beadcraft, executing-beads, work-bead
```

Additionally, `workflow-routing` provides goal-to-skill-chain mappings. From `~/.claude/skills/workflow-routing/skill.md` lines 14-25:

```
| Signal | Goal | Skill Chain |
|--------|------|-------------|
| "how does", "what is", "find", "understand" | Research | explore -> document findings |
| "design", "architect", "plan", "break down" | Plan | brainstorming -> premortem -> writing-plans |
| "spec", "decompose", "break into beads" | Encode | beadcraft (decompose mode) |
| "add", "implement", "create", "build" | Build | executing-beads -> finishing-work |
| "fix", "broken", "failing", "debug", "bug" | Fix | systematic-debugging -> test-driven-development -> finishing-work |
| "work bead", "pick up a bead" | Work Bead | work-bead |
```

**Key difference:** gstack's routing is embedded in each skill's description (distributed); oro's routing is centralized in `using-skills` and `workflow-routing`. gstack has proactive suggestion with persistence; oro has a mandatory 1%-chance invocation rule.

---

## Skill Composition

### gstack Cross-Skill References

gstack skills explicitly reference and chain to each other:

**1. Automatic chaining.** The `/ship` skill auto-invokes `/document-release` after PR creation. From `ship/SKILL.md.tmpl` lines 621-639:

```
## Step 8.5: Auto-invoke /document-release

After the PR is created, automatically sync project documentation. Read the
`document-release/SKILL.md` skill file (adjacent to this skill's directory) and
execute its full workflow:

1. Read the `/document-release` skill: `cat ${CLAUDE_SKILL_DIR}/../document-release/SKILL.md`
2. Follow its instructions...
```

**2. Hook composition.** `/guard` composes `/careful` + `/freeze` by referencing their hook scripts. From `guard/SKILL.md.tmpl` lines 18-30:

```yaml
hooks:
  PreToolUse:
    - matcher: "Bash"
      hooks:
        - type: command
          command: "bash ${CLAUDE_SKILL_DIR}/../careful/bin/check-careful.sh"
    - matcher: "Edit"
      hooks:
        - type: command
          command: "bash ${CLAUDE_SKILL_DIR}/../freeze/bin/check-freeze.sh"
```

**3. Review chaining with dashboard.** Skills persist review results via `gstack-review-log` and the Review Readiness Dashboard gates `/ship`. The `/plan-eng-review` skill ends with next-step suggestions based on dashboard state. From `plan-eng-review/SKILL.md.tmpl` lines 293-303:

```
**Suggest /plan-design-review if UI changes exist and no design review has been run**
**Mention /plan-ceo-review if this is a significant product change and no CEO review exists**
**If no additional reviews are needed**: state "All relevant reviews complete. Run /ship when ready."
```

**4. `benefits-from` field.** From `plan-eng-review/SKILL.md.tmpl` line 11:

```yaml
benefits-from: [office-hours]
```
This tells the skill to look for prior `office-hours` output in `~/.gstack/projects/`.

**5. Shared template placeholders.** Skills share methodology via placeholders. `{{QA_METHODOLOGY}}` is used by both `/qa` and `/qa-only`. `{{DESIGN_METHODOLOGY}}` is shared by `/plan-design-review` and `/design-review`.

### oro Cross-Skill References

oro skills reference each other through prose. From `workflow-routing/skill.md`:
```
### Fix
1. systematic-debugging -- find root cause
2. test-driven-development -- write failing test, fix
3. verification-before-completion -- verify fix
4. finishing-work -- integrate
```

oro's programmatic composition happens in Go code, not in skills. The worker prompt (`pkg/worker/prompt.go`) assembles 12 sections programmatically, and the ops review prompt (`pkg/ops/review_prompt.go`) reads `assets/review-patterns.md` at build time.

---

## Conductor / Parallel Execution

gstack has a `conductor.json` at the root but it is minimal:

From `conductor.json`:
```json
{
  "scripts": {
    "setup": "bin/dev-setup",
    "archive": "bin/dev-teardown"
  }
}
```

The real parallel execution in gstack comes from within skills. For example, `/ship` runs test suites in parallel:
```bash
bin/test-lane 2>&1 | tee /tmp/ship_tests.txt &
npm run test 2>&1 | tee /tmp/ship_vitest.txt &
wait
```

And `/retro` explicitly runs all git queries in parallel.

### oro Parallel Execution

oro has a full dispatcher system (`pkg/dispatcher/`) that manages parallel worker processes in git worktrees. Each worker gets an isolated worktree and runs independently. The dispatcher handles:
- Worker lifecycle (spawn, monitor, kill)
- Merge target resolution
- Quality gate retries
- Ops review

This is fundamentally different from gstack's approach. gstack runs one skill at a time in a single Claude Code session. oro runs multiple workers in parallel via programmatic orchestration.

---

## Common Patterns

### Pattern 1: Iron Laws

Both systems use "Iron Law" statements to create non-negotiable constraints.

**gstack** `/investigate` (line 40):
```
**NO FIXES WITHOUT ROOT CAUSE INVESTIGATION FIRST.**
```

**gstack** `/ship` Step 6.5 (lines 545-558):
```
**IRON LAW: NO COMPLETION CLAIMS WITHOUT FRESH VERIFICATION EVIDENCE.**

3. **Rationalization prevention:**
   - "Should work now" -> RUN IT.
   - "I'm confident" -> Confidence is not evidence.
   - "I already tested earlier" -> Code changed since then. Test again.
   - "It's a trivial change" -> Trivial changes break production.
```

**oro** `test-driven-development/skill.md` (lines 32-33):
```
NO PRODUCTION CODE WITHOUT A FAILING TEST FIRST
```

**oro** `systematic-debugging/skill.md` (line 22):
```
NO FIXES WITHOUT ROOT CAUSE INVESTIGATION FIRST
```

Both systems discovered the same pattern: short, capitalized imperative statements create the strongest behavioral constraints.

### Pattern 2: Anti-Rationalization Tables

Both systems enumerate common rationalizations and counter them.

**gstack** `/office-hours` startup mode (lines 109-116):
```
**Never say these during the diagnostic (Phases 2-5):**
- "That's an interesting approach" -- take a position instead
- "There are many ways to think about this" -- pick one and state what evidence
  would change your mind
- "You might want to consider..." -- say "This is wrong because..." or "This works because..."
- "That could work" -- say whether it WILL work based on the evidence you have
- "I can see why you'd think that" -- if they're wrong, say they're wrong and why
```

**oro** `using-skills/skill.md` (lines 50-65):
```
| Thought | Reality |
|---------|---------|
| "This is just a simple question" | Questions are tasks. Check for skills. |
| "I need more context first" | Skill check comes BEFORE clarifying questions. |
| "Let me explore first" | Skills tell you HOW to explore. Check first. |
| "This doesn't need a formal skill" | If a skill exists, use it. |
```

**oro** `test-driven-development/skill.md` (lines 173-183):
```
| Excuse | Reality |
|--------|---------|
| "Too simple to test" | Simple code breaks. Test takes 30 seconds. |
| "I'll test after" | Tests passing immediately prove nothing. |
| "Already manually tested" | Ad-hoc != systematic. No record, can't re-run. |
```

### Pattern 3: Structured Output Templates

Both systems define exact output formats.

**gstack** `/investigate` Phase 5 (lines 172-183):

```
DEBUG REPORT
================
Symptom:         [what the user observed]
Root cause:      [what was actually wrong]
Fix:             [what was changed, with file:line references]
Evidence:        [test output, reproduction attempt showing fix works]
Regression test: [file:line of the new test]
Related:         [TODOS.md items, prior bugs in same area, architectural notes]
Status:          DONE | DONE_WITH_CONCERNS | BLOCKED
================
```

**gstack** `/plan-design-review` Completion Summary (lines 238-258):
```
  +====================================================================+
  |         DESIGN PLAN REVIEW -- COMPLETION SUMMARY                    |
  +====================================================================+
  | Pass 1  (Info Arch)  | ___/10 -> ___/10 after fixes                |
  | Pass 2  (States)     | ___/10 -> ___/10 after fixes                |
  ...
  | Overall design score | ___/10 -> ___/10                             |
  +====================================================================+
```

**oro** `review-implementation/skill.md` (lines 33-50):
```
## Implementation Review: [feature/scope]
### Summary
X/Y requirements met | Z gaps | W divergences
### Checklist
- [x] Requirement A -- `src/foo.py:42`
- [~] Requirement B -- partial, missing error handling
- [ ] Requirement C -- not implemented
- [!] Requirement D -- diverged: spec says X, code does Y
```

---

## Prompt Engineering Techniques

### Role Framing

gstack assigns strong personas to skills.

**`/office-hours`** (line 31):
> You are a **YC office hours partner**. Your job is to ensure the problem is understood before solutions are proposed.

**`/design-consultation`** (line 27):
> You are a senior product designer with strong opinions about typography, color, and visual systems. You don't present menus -- you listen, think, research, and propose.

**`/canary`** (line 26):
> You are a **Release Reliability Engineer** watching production after a deploy. You've seen deploys that pass CI but break in production.

**`/codex`** (line 29):

> Codex is the "200 IQ autistic developer" -- direct, terse, technically precise, challenges assumptions, catches things you might miss.

oro uses minimal role framing. From `pkg/worker/prompt.go` line 50:
```go
section(&b, "Role", "You are an oro worker. You execute one bead at a time.")
```

### Anti-Sycophancy Rules

This is one of gstack's most distinctive techniques. From `/office-hours` lines 109-119:

```
### Anti-Sycophancy Rules

**Never say these during the diagnostic (Phases 2-5):**
- "That's an interesting approach" -- take a position instead
- "There are many ways to think about this" -- pick one and state what evidence
  would change your mind
- "You might want to consider..." -- say "This is wrong because..." or
  "This works because..."
- "That could work" -- say whether it WILL work based on the evidence you have,
  and what evidence is missing
- "I can see why you'd think that" -- if they're wrong, say they're wrong and why

**Always do:**
- Take a position on every answer. State your position AND what evidence would
  change it.
- Challenge the strongest version of the founder's claim, not a strawman.
```

oro has nothing equivalent. oro's skills do not explicitly address sycophancy patterns.

### Cognitive Pattern Injection

gstack injects named cognitive patterns from real-world leaders into plan review skills.

**`/plan-ceo-review`** lines 66-87 (16 patterns):
```
## Cognitive Patterns -- How Great CEOs Think
1. **Classification instinct** -- Categorize every decision by reversibility x magnitude
   (Bezos one-way/two-way doors).
2. **Paranoid scanning** -- Continuously scan for strategic inflection points, cultural drift
   (Grove: "Only the paranoid survive").
3. **Inversion reflex** -- For every "how do we win?" also ask "what would make us fail?" (Munger).
4. **Focus as subtraction** -- Primary value-add is what to *not* do. Jobs went from 350
   products to 10.
...
```

**`/plan-eng-review`** lines 39-58 (15 patterns):

```
## Cognitive Patterns -- How Great Eng Managers Think
1. **State diagnosis** -- Teams exist in four states (Larson, An Elegant Puzzle).
2. **Blast radius instinct** -- Every decision evaluated through "what's the worst case?"
3. **Boring by default** -- "Every company gets about three innovation tokens."
   (McKinley, Choose Boring Technology).
...
13. **Make the change easy, then make the easy change** (Beck).
```

**`/plan-design-review`** lines 57-72 (12 patterns):
```
## Cognitive Patterns -- How Great Designers See
1. **Seeing the system, not the screen** -- Never evaluate in isolation
2. **Empathy as simulation** -- Running mental simulations: bad signal, one hand free,
   boss watching, first time vs. 1000th time.
...
8. **Principled taste** -- "This feels wrong" is traceable to a broken principle.
   Taste is *debuggable*, not subjective.
```

oro does not inject cognitive patterns. Worker prompts are functional and constraint-oriented rather than persona-based.

### Constraint Specification

gstack uses "Only stop for / Never stop for" tables to specify behavioral boundaries. From `/ship` lines 27-45:

```
**Only stop for:**
- On the base branch (abort)
- Merge conflicts that can't be auto-resolved
- Test failures
- Pre-landing review finds ASK items that need user judgment
- MINOR or MAJOR version bump needed

**Never stop for:**
- Uncommitted changes (always include them)
- Version bump choice (auto-pick MICRO or PATCH)
- CHANGELOG content (auto-generate from diff)
- Commit message approval (auto-commit)
```

oro uses a simpler constraint model. From `pkg/worker/prompt.go` lines 231-236:
```go
section(b, "Constraints", strings.Join([]string{
    "- Do no git push",
    "- Do not modify files outside your worktree",
    "- Do not modify the main branch",
    "- NEVER replace function/method calls with blank identifier assignments",
}, "\n"))
```

### Output Formatting -- AskUserQuestion

gstack standardizes every user question. From the preamble (referenced in ARCHITECTURE.md line 218):

> **AskUserQuestion format** -- universal format: context, question, `RECOMMENDATION: Choose X because ___`, lettered options. Consistent across all skills.

This appears in every skill that interacts with the user. From `/review` Step 5c:
```
1. [CRITICAL] app/models/post.rb:42 -- Race condition in status transition
   Fix: Add `WHERE status = 'draft'` to the UPDATE
   -> A) Fix  B) Skip

RECOMMENDATION: Fix both -- #1 is a real race condition.
```

oro does not standardize user interaction formatting across skills.

---

## Safety and Guardrails

### /careful -- Destructive Command Detection

`careful` uses a PreToolUse hook to intercept Bash commands before execution. From `careful/bin/check-careful.sh`:

The script reads JSON from stdin, extracts the `command` field, and pattern-matches against destructive commands:

```bash
# rm -rf / rm -r / rm --recursive
if printf '%s' "$CMD" | grep -qE 'rm\s+(-[a-zA-Z]*r|--recursive)' 2>/dev/null; then
  WARN="Destructive: recursive delete (rm -r). This permanently removes files."
  PATTERN="rm_recursive"
fi
```

It also handles safe exceptions:
```bash
case "$target" in
  */node_modules|node_modules|*/\.next|\.next|*/dist|dist|*/__pycache__|...)
    ;; # safe target
```

When a match is found, it returns `{"permissionDecision":"ask","message":"..."}` which triggers a user confirmation.

### /freeze -- Edit Boundary Enforcement

`freeze` uses a PreToolUse hook on Edit and Write tools. From `freeze/bin/check-freeze.sh`:

```bash
case "$FILE_PATH" in
  "${FREEZE_DIR}"*)
    echo '{}'       # Inside boundary -- allow
    ;;
  *)
    printf '{"permissionDecision":"deny","message":"[freeze] Blocked: %s is outside
    the freeze boundary (%s)."}\n' "$FILE_PATH" "$FREEZE_DIR"
    ;;
esac
```

Key design: `deny` (hard block) not `ask` (warning). The user cannot override a freeze boundary without running `/unfreeze`.

### /guard -- Composite Safety

`guard` composes both by referencing sibling hook scripts:
```yaml
hooks:
  PreToolUse:
    - matcher: "Bash"
      hooks:
        - type: command
          command: "bash ${CLAUDE_SKILL_DIR}/../careful/bin/check-careful.sh"
    - matcher: "Edit"
      hooks:
        - type: command
          command: "bash ${CLAUDE_SKILL_DIR}/../freeze/bin/check-freeze.sh"
```

### /investigate Auto-Lock

`/investigate` auto-applies freeze after forming a hypothesis. From `investigate/SKILL.md.tmpl` lines 66-83:

```
After forming your root cause hypothesis, lock edits to the affected module
to prevent scope creep.

STATE_DIR="${CLAUDE_PLUGIN_DATA:-$HOME/.gstack}"
mkdir -p "$STATE_DIR"
echo "<detected-directory>/" > "$STATE_DIR/freeze-dir.txt"
echo "Debug scope locked to: <detected-directory>/"
```

### oro Safety

oro has a `destructive-command-safety` skill (plain Markdown advisory) and its `no_cd_guard` shell hook that blocks `cd` into worktrees. However, oro has no PreToolUse hook infrastructure. Safety is advisory (prose in skills) rather than mechanically enforced.

---

## Browser and Tool Integration

### gstack Browse Daemon

gstack's unique capability is a persistent headless Chromium browser. From `ARCHITECTURE.md` lines 6-36:

```
Claude Code                     gstack
---------                      ------
  Tool call: $B snapshot -i     CLI (compiled binary)
  ------------------------->    POST /command to localhost:PORT
                                Server (Bun.serve)
                                talks to Chromium via CDP
```

Key features:
- **Sub-second latency**: ~100-200ms per command after initial 3s startup
- **Persistent state**: Cookies, tabs, localStorage survive between commands
- **Ref system**: `@e1`, `@e2` refs for element addressing without CSS selectors
- **Cookie import**: Decrypts real browser cookies via macOS Keychain

This browser powers `/qa`, `/qa-only`, `/design-review`, `/canary`, `/benchmark`, `/design-consultation`, and `/setup-browser-cookies`.

### oro Tool Integration

oro has `agent-browser`, a Playwright-based CLI skill with the same snapshot-ref pattern (`@e1`, `@e2` selectors), form automation, and screenshot capabilities. However, it is a skill invoked on demand — not a persistent daemon like gstack's browse. oro's tooling is also centered on:
- `bd` (beads CLI) for issue tracking
- Git worktrees for parallel isolation
- `claude -p` for spawning workers
- Quality gate scripts
- `oro remember` for memory persistence

---

## Quality and Review Workflows

### gstack Review Pipeline

gstack has a multi-stage review pipeline with persistent tracking:

1. **`/office-hours`** -- Problem understanding and design doc
2. **`/plan-ceo-review`** -- CEO-level scope and ambition review
3. **`/plan-eng-review`** -- Architecture, code quality, tests, performance review
4. **`/plan-design-review`** -- 7-pass design audit with 0-10 ratings
5. **`/review`** -- Pre-landing code review with two-pass critical/informational split
6. **`/ship`** -- Automated: tests, coverage audit, pre-landing review, version bump, changelog, PR creation, auto-invokes `/document-release`
7. **`/land-and-deploy`** -- Merge PR, wait for deploy, canary monitoring
8. **`/canary`** -- Post-deploy continuous monitoring with screenshots
9. **`/benchmark`** -- Performance regression detection

Each review skill persists its result via `gstack-review-log`, and `/ship` reads the Review Readiness Dashboard to gate shipping.

### gstack 3-Tier Testing

From `ARCHITECTURE.md` lines 229-237:

```
| Tier | What | Cost | Speed |
|------|------|------|-------|
| 1 -- Static validation | Parse $B commands, validate against registry | Free | <2s |
| 2 -- E2E via claude -p | Spawn real Claude session, run each skill | ~$3.85 | ~20min |
| 3 -- LLM-as-judge | Sonnet scores docs on clarity/completeness | ~$0.15 | ~30s |
```

### oro Review Pipeline

oro's review pipeline is:

1. Worker executes bead with TDD
2. Quality gate script runs (`scripts/quality_gate.sh`)
3. Ops review (`pkg/ops/review_prompt.go`) reads diff + `assets/review-patterns.md`
4. Dispatcher merges if review passes

oro has `review-implementation` and `requesting-code-review` skills, but these are advisory Markdown, not automated pipelines.

---

## Comparison Table: gstack vs oro

| Feature | gstack | oro |
|---------|--------|-----|
| **Skill count** | 25+ | 35+ |
| **Skill format** | Generated .md from .tmpl | Plain .md |
| **Template system** | `gen-skill-docs.ts` with 8+ placeholders | None |
| **Frontmatter** | name, version, description, allowed-tools, hooks, benefits-from | name, description only |
| **Preamble injection** | Yes (update check, session tracking, AskUserQuestion format) | None |
| **Safety hooks** | PreToolUse hooks (careful, freeze, guard) | No hook infrastructure |
| **Destructive cmd guard** | Mechanical enforcement via shell script hook | Advisory prose only |
| **Edit boundary** | Hard deny via freeze hook | Worktree isolation (different mechanism) |
| **Browser integration** | Persistent Chromium daemon (~100ms/cmd) | `agent-browser` skill (on-demand Playwright CLI, same snapshot-ref pattern) |
| **Parallel execution** | Within-skill parallelism (test suites, git queries) | Dispatcher manages parallel workers in worktrees |
| **Worker orchestration** | None (single-session skills) | Full dispatcher with spawn/monitor/merge |
| **Issue tracking** | TODOS.md (file-based) | `bd` CLI (JSONL-based beads) |
| **Review pipeline** | 8-stage pipeline with persistent dashboard | QG + ops review (2-stage) |
| **Cross-session persistence** | `~/.gstack/projects/` artifacts | `.beads/issues.jsonl` + `oro remember` |
| **Cognitive patterns** | 43 named patterns across 3 review skills | None |
| **Anti-sycophancy rules** | Explicit "never say" lists in /office-hours | None |
| **Eval system** | 3-tier (static + E2E + LLM-judge) | Go test suite + QG script |
| **Skill composition** | Hook composition, auto-invocation, benefits-from | Prose references in workflow-routing |
| **Role framing** | Rich personas (YC partner, Release Engineer, etc.) | Minimal ("You are an oro worker") |
| **Output formatting** | Standardized AskUserQuestion format, structured reports | Varies by skill |
| **Proactive suggestions** | Configurable with opt-out persistence | Mandatory 1%-chance rule |
| **Version tracking** | 4-digit VERSION file, CHANGELOG, version auto-restart | Git commits only |
| **Documentation sync** | Auto via /document-release after /ship | Manual |

---

## Recommendations for oro

### Easy (1-2 hours each)

**E1. Add anti-sycophancy rules to ops review prompt.**
File: `/Users/as21/codehouse/oro/pkg/ops/review_prompt.go`

Add to the `writeHeader` function a section like:
```
Never say: "This looks fine", "Probably tested", "Likely handled elsewhere".
Either cite evidence it IS fine, or flag as unverified.
```

gstack's `/review` skill has this pattern at lines 180-187:
```
- If you claim "this pattern is safe" -> cite the specific line proving safety
- If you claim "this is handled elsewhere" -> read and cite the handling code
- If you claim "tests cover this" -> name the test file and method
- Never say "likely handled" or "probably tested" -- verify or flag as unknown
```

**E2. Add "Iron Law" rationalization prevention to worker prompt.**
File: `/Users/as21/codehouse/oro/pkg/worker/prompt.go` line 214

Currently: `section(b, "TDD", "Write tests FIRST. Red-green-refactor. Every feature/fix needs a test.")`

Expand to include rationalization prevention from gstack's `/ship` verification gate:
```
"Should work now" -> RUN IT.
"I'm confident" -> Confidence is not evidence.
"It's a trivial change" -> Trivial changes break production.
```

**E3. Add cognitive patterns to beadcraft decomposition prompt.**
File: `/Users/as21/codehouse/oro/pkg/worker/prompt.go` function `BuildEpicDecompositionPrompt` (line 121)

Add 3-5 patterns from gstack's eng manager cognitive patterns:
- Blast radius instinct
- Boring by default
- Essential vs accidental complexity
- Make the change easy, then make the easy change

**E4. Standardize AskUserQuestion format in interactive skills.**
Files: `~/.claude/skills/brainstorming/skill.md`, `~/.claude/skills/workflow-routing/skill.md`

Add a universal question format: context, question, `RECOMMENDATION: Choose X because ___`, lettered options.

### Medium (2-8 hours each)

**M1. Add review pattern persistence and cross-session context.**
Implement `~/.oro/projects/{slug}/` directory structure similar to gstack's `~/.gstack/projects/`. Persist:
- Design doc output from brainstorming
- Review results from ops review
- Test outcomes from QG runs

This requires changes to:
- `/Users/as21/codehouse/oro/pkg/ops/review_prompt.go` -- write review result JSONL
- `/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go` -- read prior review results

**M2. Add structured completion status to worker prompts.**
gstack skills end with explicit completion statuses. From `/investigate` (lines 193-196):
```
- DONE -- root cause found, fix applied, regression test written, all tests pass
- DONE_WITH_CONCERNS -- fixed but cannot fully verify
- BLOCKED -- root cause unclear after investigation, escalated
```

Add similar structured exit statuses to:
- `/Users/as21/codehouse/oro/pkg/worker/prompt.go` `appendExitSection` function (line 286)
- The dispatcher should parse these statuses for better merge/retry decisions

**M3. Build a review readiness dashboard for beads.**
gstack's `/ship` checks a Review Readiness Dashboard before proceeding. oro could build an equivalent that checks:
- QG pass/fail history
- Ops review results
- Test coverage delta
- Bead acceptance criteria coverage

File: New function in `/Users/as21/codehouse/oro/pkg/ops/` or extend `review_prompt.go`

**M4. Add "Only stop for / Never stop for" constraint tables to worker prompts.**
File: `/Users/as21/codehouse/oro/pkg/worker/prompt.go` `appendStaticSections` function (line 209)

Add explicit behavioral boundaries:
```
Only stop for: 3 failed test attempts, bead too big to execute, context limit reached
Never stop for: lint warnings (fix them), test naming style, import ordering
```

This would reduce unnecessary worker stalls.

**M5. Add anti-sycophancy rules to the brainstorming skill.**
File: `~/.claude/skills/brainstorming/skill.md`

gstack's `/office-hours` is dramatically better at brainstorming because of its anti-sycophancy rules and pushback patterns. Port the core pattern:
- Take a position on every answer
- State what evidence would change your mind
- Never say "that's interesting" -- say whether it works and why

### Hard (1-2 days each)

**H1. Implement PreToolUse hook infrastructure.**
gstack's hook system is the foundation of its safety features. oro would need:

- A hook registration mechanism (YAML frontmatter or Go config)
- A hook runner that intercepts tool calls
- Shell script hooks for destructive command detection and edit boundary enforcement

This would replace oro's advisory `destructive-command-safety` skill with mechanical enforcement. Files to modify:
- `/Users/as21/codehouse/oro/pkg/worker/` -- add hook runner to worker spawn
- New files: `pkg/hooks/` package with PreToolUse interceptor
- `~/.claude/skills/destructive-command-safety/` -- convert from advisory to hook-based

**H2. Build a template generation system for worker prompts.**
gstack's template system prevents doc drift by generating SKILL.md from source code. oro could apply this to:
- Worker prompt sections (currently hardcoded strings in Go)
- Ops review prompt
- Skills that reference project-specific commands

The template system would read `pkg/worker/prompt.go` static sections from `.md.tmpl` files with placeholders, validate them against actual code, and catch drift in CI.

**H3. Upgrade agent-browser to a persistent daemon for QA verification.**
oro already has `agent-browser` with snapshot-refs, form automation, and screenshots. gstack's advantage is the persistent daemon (~100ms vs ~3s cold start) and cookie import from real browsers. Upgrade oro's agent-browser to:

- Verifying web-facing projects after bead completion
- Post-merge canary checks
- Visual regression detection

This is the highest-effort recommendation but would unlock an entirely new category of automated verification.

**H4. Build a multi-skill pipeline orchestrator.**
gstack's review chaining (office-hours -> plan-ceo-review -> plan-eng-review -> plan-design-review -> ship -> document-release -> land-and-deploy -> canary) is not just a sequence of commands -- it's a pipeline where each stage persists artifacts consumed by the next. oro's `workflow-routing` suggests chains but doesn't persist cross-stage artifacts.

Build a pipeline system where:
- Each skill/stage writes structured output to a known location
- Downstream stages read upstream artifacts
- A dashboard shows pipeline progress
- The dispatcher can orchestrate the full pipeline

Files to modify:
- `/Users/as21/codehouse/oro/.claude/skills/workflow-routing/skill.md` -- add artifact persistence
- New file: `pkg/pipeline/` package
- `/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go` -- pipeline awareness

---

## Appendix A: Complete gstack Skill Inventory

| # | Skill | Type | Has Hooks | Has Browse | Key Innovation |
|---|-------|------|-----------|------------|----------------|
| 1 | gstack (root) | Browser + router | No | Yes | Proactive skill suggestion with opt-out |
| 2 | office-hours | Brainstorming | No | Optional | Anti-sycophancy rules, 6 forcing questions, 3-tier founder plea |
| 3 | plan-ceo-review | Plan review | No | No | 16 CEO cognitive patterns, 4 scope modes |
| 4 | plan-eng-review | Plan review | No | No | 15 eng manager cognitive patterns, test plan artifact |
| 5 | plan-design-review | Plan review | No | No | 7-pass 0-10 rating, 12 designer cognitive patterns |
| 6 | design-consultation | Design system | No | Optional | Full design system builder with preview HTML |
| 7 | review | Code review | No | No | Two-pass critical/informational, fix-first heuristic |
| 8 | investigate | Debugging | Yes (freeze) | No | Auto-scope-lock, 3-strike rule, pattern analysis |
| 9 | qa | QA + fix | No | Yes | 11-phase test-fix-verify loop, WTF-likelihood metric |
| 10 | qa-only | QA report | No | Yes | Report-only mode, no code changes |
| 11 | design-review | Design audit + fix | No | Yes | Design audit with atomic fix commits |
| 12 | ship | Deploy workflow | No | No | 8.5-step fully automated pipeline |
| 13 | document-release | Doc sync | No | No | Cross-doc consistency, CHANGELOG voice polish |
| 14 | retro | Retrospective | No | No | Team-aware, streak tracking, 14-step analysis |
| 15 | codex | Second opinion | No | No | OpenAI Codex CLI wrapper, cross-model comparison |
| 16 | careful | Safety | Yes (Bash) | No | Destructive command detection via hook |
| 17 | freeze | Safety | Yes (Edit/Write) | No | Hard edit boundary enforcement |
| 18 | guard | Safety | Yes (Bash + Edit/Write) | No | Composite: careful + freeze |
| 19 | unfreeze | Safety | No | No | Clears freeze boundary |
| 20 | gstack-upgrade | Maintenance | No | No | Auto-upgrade with snooze backoff |
| 21 | setup-browser-cookies | Setup | No | Yes | Cookie import from real browsers |
| 22 | setup-deploy | Setup | No | No | Deploy platform detection + CLAUDE.md config |
| 23 | land-and-deploy | Deploy | No | Yes | Merge PR, wait for deploy, canary verify |
| 24 | benchmark | Performance | No | Yes | Core Web Vitals, bundle size tracking |
| 25 | canary | Monitoring | No | Yes | Post-deploy continuous monitoring with alerts |

## Appendix B: Complete oro Skill Inventory

| # | Skill | Category | Key Feature |
|---|-------|----------|-------------|
| 1 | using-skills | Routing | 1%-chance mandatory invocation rule |
| 2 | workflow-routing | Routing | Goal-to-chain mapping |
| 3 | test-driven-development | Discipline | Iron Law: no code without failing test |
| 4 | systematic-debugging | Discipline | 4-phase root cause investigation |
| 5 | verification-before-completion | Discipline | Prevents premature completion claims |
| 6 | observe-before-editing | Discipline | Read before write |
| 7 | destructive-command-safety | Discipline | Advisory destructive command warnings |
| 8 | brainstorming | Workflow | Requirements exploration |
| 9 | writing-plans | Workflow | Plan document creation |
| 10 | executing-plans | Workflow | Task-by-task plan execution |
| 11 | requesting-code-review | Workflow | Code review request |
| 12 | receiving-code-review | Workflow | Code review response |
| 13 | finishing-work | Workflow | Integration and cleanup |
| 14 | review-implementation | Workflow | Spec-vs-code comparison |
| 15 | review-docs | Workflow | Documentation review |
| 16 | dispatching-parallel-agents | Orchestration | Parallel agent management |
| 17 | premortem | Orchestration | Risk analysis (tigers/elephants/paper tigers) |
| 18 | completion-check | Orchestration | Completion verification |
| 19 | explore | Orchestration | Codebase exploration at 3 depths |
| 20 | beads | Tools | Bead CLI reference |
| 21 | git-commits | Tools | Commit conventions |
| 22 | tmux | Tools | Terminal multiplexer usage |
| 23 | github | Tools | GitHub CLI usage |
| 24 | session-logs | Tools | Session logging |
| 25 | agent-browser | Tools | Browser-based research |
| 26 | create-handoff | Continuity | Context handoff creation |
| 27 | resume-handoff | Continuity | Context handoff resumption |
| 28 | documenting-solutions | Continuity | Solution documentation |
| 29 | refactor | Continuity | Refactoring workflow |
| 30 | using-git-worktrees | Continuity | Worktree management |
| 31 | writing-skills | Continuity | Skill authoring |
| 32 | context-checkpoint | Continuity | Context state saving |
| 33 | beadcraft | Beads | Bead decomposition with Rule of Five |
| 34 | executing-beads | Beads | Bead execution workflow |
| 35 | work-bead | Beads | Single bead execution |
| 36 | adversarial-spec-review | Workflow | Spec adversarial review |
| 37 | spec | Workflow | Spec creation (project-level) |
| 38 | watching-oro | Tools | Oro process monitoring (project-level) |
| 39 | restart-oro | Tools | Oro restart procedure (project-level) |

## Appendix C: Template Placeholders

| Placeholder | Source File | What It Generates |
|-------------|-----------|-------------------|
| `{{PREAMBLE}}` | `gen-skill-docs.ts` | Update check, session tracking, AskUserQuestion format |
| `{{BROWSE_SETUP}}` | `gen-skill-docs.ts` | Binary discovery + setup instructions |
| `{{COMMAND_REFERENCE}}` | `commands.ts` | Categorized browse command table |
| `{{SNAPSHOT_FLAGS}}` | `snapshot.ts` | Snapshot flag reference with examples |
| `{{BASE_BRANCH_DETECT}}` | `gen-skill-docs.ts` | Dynamic base branch detection |
| `{{QA_METHODOLOGY}}` | `gen-skill-docs.ts` | Shared QA methodology for /qa and /qa-only |
| `{{DESIGN_METHODOLOGY}}` | `gen-skill-docs.ts` | Shared design audit methodology |
| `{{REVIEW_DASHBOARD}}` | `gen-skill-docs.ts` | Review Readiness Dashboard |
| `{{TEST_BOOTSTRAP}}` | `gen-skill-docs.ts` | Test framework detection + bootstrap |
| `{{BENEFITS_FROM}}` | `gen-skill-docs.ts` | Upstream skill dependency check |
| `{{DESIGN_REVIEW_LITE}}` | `gen-skill-docs.ts` | Inline design check for /ship and /review |
| `{{ADVERSARIAL_STEP}}` | `gen-skill-docs.ts` | Optional Codex adversarial review step |
| `{{PLAN_FILE_REVIEW_REPORT}}` | `gen-skill-docs.ts` | Plan file review output |
| `{{SPEC_REVIEW_LOOP}}` | `gen-skill-docs.ts` | Spec self-review loop for /office-hours |
| `{{DESIGN_SKETCH}}` | `gen-skill-docs.ts` | Design sketch phase for /office-hours |
