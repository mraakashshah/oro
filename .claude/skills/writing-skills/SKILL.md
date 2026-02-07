---
name: writing-skills
description: Use when creating new skills, editing existing skills, or verifying skills work
---

# Writing Skills

## Overview

Writing skills IS Test-Driven Development applied to process documentation. If you didn't watch an agent fail without the skill, you don't know if the skill teaches the right thing.

## What is a Skill?

A reusable reference guide for proven techniques, patterns, or tools.

**Skills are:** Reusable techniques, patterns, tools, reference guides
**Skills are NOT:** Narratives about how you solved a problem once

## Skill Types

| Type | Examples | Test With |
|------|----------|-----------|
| **Discipline** | TDD, verification | Pressure scenarios — does agent comply under stress? |
| **Technique** | debugging, refactoring | Application scenarios — can agent apply correctly? |
| **Pattern** | mental models | Recognition scenarios — does agent know when to apply? |
| **Reference** | API docs, tools | Retrieval scenarios — can agent find the right info? |

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

- **Only two fields:** `name` and `description` (max 1024 chars total)
- **Name:** letters, numbers, hyphens only
- **Description:** Start with "Use when..." — triggering CONDITIONS only

### CSO Rule: Description = When, NOT What

**CRITICAL:** Description must ONLY describe triggering conditions. Never summarize the skill's workflow.

```yaml
# BAD: Summarizes workflow — Claude follows description instead of reading body
description: Use when executing plans - dispatches subagent per task with code review

# GOOD: Just conditions — Claude reads the full body
description: Use when you have a written implementation plan to execute
```

## Token Budgets

- Frequently-loaded skills: **<200 words**
- Most skills: **<500 words**
- Discipline skills (with rationalization tables): earn their length

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

## Anti-Patterns

- Narrative examples ("In session 2025-10-03, we found...")
- Multi-language dilution (one excellent example beats many mediocre)
- Code in flowcharts (can't copy-paste)
- Generic labels (helper1, step2)

## Verification

```bash
wc -w .claude/skills/*/SKILL.md  # Check word counts
```
