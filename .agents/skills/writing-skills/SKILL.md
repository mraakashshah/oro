---
name: writing-skills
description: Use when creating or editing Codex skills
---

# Writing Skills

## Overview

Writing skills IS Test-Driven Development applied to process documentation. If you didn't watch an agent fail without the skill, you don't know if the skill teaches the right thing.

## Core Principles

**The context window is a public good.** Skills share it with system prompts, conversation history, other skills, and the actual task. Only add what the model doesn't already know. Challenge each paragraph: "Does this justify its token cost?"

**Set appropriate degrees of freedom.** Match specificity to task fragility:

| Freedom | When | Example |
|---------|------|---------|
| **High** (prose guidance) | Multiple valid approaches, context-dependent | brainstorming, explore |
| **Medium** (pseudocode/templates) | Preferred pattern exists, some variation OK | writing-plans, workflow-routing |
| **Low** (exact steps, scripts) | Fragile operations, consistency critical | TDD, destructive-command-safety |

Think of a narrow bridge with cliffs (low freedom) vs an open field (high freedom).

## What is a Skill?

A reusable reference guide for proven techniques, patterns, or tools.

**Skills are:** Reusable techniques, patterns, tools, reference guides
**Skills are NOT:** Narratives, README files, changelogs, setup guides, or user-facing documentation

## Skill vs Rule vs Hook

When a recurring pattern needs to become permanent, pick the right artifact:

```
Is it a sequence of steps/commands? → SKILL (executable > declarative)
Should it fire automatically on an event? → HOOK (automatic > manual)
Is it "when X, do Y" or "never do X"? → RULE (heuristic)
Does it enhance an existing agent? → AGENT UPDATE
None of the above? → Skip (not worth capturing)
```

Signal threshold: 1 occurrence = note, 2 = consider, 3+ = create.

## Skill Types

| Type | Examples | Test With |
|------|----------|-----------|
| **Discipline** | TDD, verification | Pressure scenarios — does agent comply under stress? |
| **Technique** | debugging, refactoring | Application scenarios — can agent apply correctly? |
| **Pattern** | mental models | Recognition scenarios — does agent know when to apply? |
| **Reference** | API docs, tools | Retrieval scenarios — can agent find the right info? |

## Skill Anatomy

```
skill-name/
├── SKILL.md              (required)
├── scripts/              (optional — deterministic, reusable code)
├── references/           (optional — docs loaded into context on demand)
└── assets/               (optional — files used in output, not loaded into context)
```

### Progressive Disclosure

Skills load in three levels — design for this:

1. **Metadata** (name + description) — always in context (~100 words)
2. **SKILL.md body** — loaded when skill triggers (<500 words target)
3. **Bundled resources** — loaded only when needed (unlimited)

Keep SKILL.md lean. Move detailed reference material, schemas, and examples to `references/` files. Reference them clearly so the agent knows they exist:

```markdown
## Advanced Features
- **Form filling**: See [FORMS.md](references/FORMS.md) for complete guide
- **API reference**: See [REFERENCE.md](references/REFERENCE.md) for all methods
```

For skills with multiple domains, organize by domain so only relevant context loads:

```
bigquery-skill/
├── SKILL.md (overview + navigation)
└── references/
    ├── finance.md
    ├── sales.md
    └── product.md
```

## SKILL.md Structure

```markdown
---
name: skill-name-with-hyphens
description: Use when [specific triggering conditions]
---

# Skill Name

## Overview
Core principle in 1-2 sentences.

## When to Use
Symptoms and use cases.

## Steps / Core Pattern
The actual technique.

## Common Rationalizations (discipline skills)
| Excuse | Reality |

## Red Flags
When to STOP.
```

## Frontmatter Rules

- **Required:** `name` and `description` (max 1024 chars total)
- **Optional:** `allowed-tools` (tools usable without asking), `model` (specific model)
- **Name:** lowercase letters, numbers, hyphens only (under 64 chars)
- **Description:** Start with "Use when..." — triggering CONDITIONS only

### CSO Rule: Description = When, NOT What

**CRITICAL:** Description must ONLY describe triggering conditions. Never summarize the skill's workflow.

```yaml
# BAD: Summarizes workflow — Codex follows description instead of reading body
description: Use when executing plans - dispatches subagent per task with code review

# GOOD: Just conditions — Codex reads the full body
description: Use when you have a written implementation plan to execute
```

## Token Budgets

- Frequently-loaded skills: **<200 words**
- Most skills: **<500 words**
- Discipline skills (with rationalization tables): earn their length

## Match the Form to the Failure

Before writing guidance, classify the baseline failure. The form that fixes one failure type backfires on another.

| Baseline failure | Right form | Wrong form |
|---|---|---|
| Knows the rule, skips it under pressure | Prohibition + rationalization table + red flags | Soft guidance ("prefer...", "consider...") |
| Complies, but output has wrong shape (bloated, buried verdict, restated spec) | Positive recipe: state what the output IS — its parts, in order | Prohibition list ("don't restate", "never narrate") |
| Omits a required element they otherwise produce | Structural slot: a REQUIRED field in the template they fill | Prose reminders near the template |
| Behavior should depend on a condition | Conditional keyed to an observable predicate ("if the brief exists, reference it") | Unconditional rule + exemption clauses |

**Prohibitions backfire on shaping problems.** When the agent has a competing incentive, "don't X" is something to negotiate against — in wording tests, a prohibition arm produced more of the unwanted content than the positive-recipe arm, and trended worse than even a no-guidance control. A recipe or a slot leaves nothing to negotiate: the output matches the stated shape or it doesn't. Reach for prohibition only for discipline failures (knows-better-does-it-anyway).

**No nuance clauses.** "Don't X unless it matters" reopens the negotiation. Express a real exception as its own conditional on an observable predicate. Exemption clauses ("this limit doesn't apply to code blocks") don't scope — they suppress the exempt part too. Restructure so the rule can't reach it.

## Bulletproofing Discipline Skills

### Close Every Loophole

Don't just state the rule — forbid specific workarounds:

```markdown
Write code before test? Delete it. Start over.

**No exceptions:**
- Don't keep it as "reference"
- Don't "adapt" it while writing tests
- Delete means delete
```

### Address "Spirit vs Letter"

Add early: **"Violating the letter of the rules is violating the spirit of the rules."**

### Build Rationalization Table

Capture every excuse agents make. Each gets an explicit counter.

### Create Red Flags List

Easy self-check when rationalizing:
```markdown
## Red Flags - STOP
- [specific behavior]
- [specific thought pattern]
**All of these mean: [consequence]**
```

## Creation Process

1. **Understand** — Gather concrete usage examples. What triggers the skill? What does success look like?
2. **Plan resources** — For each example, identify what scripts, references, or assets would help when doing this repeatedly
3. **Create** — Write SKILL.md, add bundled resources. Prefer concise examples over verbose explanations
4. **Test** — Use the skill on real tasks. Watch for struggles or inefficiencies
5. **Iterate** — Update based on real usage, not theory

## Micro-Test Wording Before Full Scenarios

Full pressure scenarios are the final gate, but they're slow per iteration. Verify the wording itself first with cheap micro-tests:

1. **One fresh-context sample per call.** System prompt = the realistic context the guidance will live in (the whole skill or prompt, not the snippet in isolation); user message = a task that tempts the failure.
2. **Always include a no-guidance control.** If the control doesn't exhibit the failure, there's nothing to fix — stop, don't author the guidance.
3. **5+ reps per variant.** Single samples lie.
4. **Read every flagged match by hand.** Template echoes and quoted counter-examples masquerade as hits; automated counts overstate both failure and success.
5. **Variance is a signal.** When guidance binds, reps converge on the same shape. Five different interpretations across five reps means the wording isn't binding — tighten the form before adding words.

Micro-tests verify wording; they don't replace pressure scenarios for discipline skills.

## Anti-Patterns

- Narrative examples ("In session 2025-10-03, we found...")
- Multi-language dilution (one excellent example beats many mediocre)
- Code in flowcharts (can't copy-paste)
- Generic labels (helper1, step2)
- Duplicating info between SKILL.md and references (pick one home)
- Deeply nested references (keep one level deep from SKILL.md)
- Auxiliary files (README, CHANGELOG, INSTALLATION_GUIDE) inside skills
- XML tags in body (use standard markdown headings)
- Time-sensitive information (breaks when skill outlives the moment)

## Verification

### Word counts
```bash
wc -w .Codex/skills/*/SKILL.md
```

### Audit checklist

- [ ] Valid YAML frontmatter (name + description)
- [ ] Description has trigger conditions only (CSO rule)
- [ ] SKILL.md under 500 words (discipline skills may exceed)
- [ ] Standard markdown headings (no XML tags)
- [ ] References one level deep, clearly linked
- [ ] Examples are concrete, not abstract
- [ ] No time-sensitive information
- [ ] Tested with real tasks across models (Haiku behaves differently than Opus)
