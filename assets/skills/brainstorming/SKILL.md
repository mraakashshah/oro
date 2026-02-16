---
name: brainstorming
description: Use before any creative work -- creating features, building components, adding functionality, or modifying behavior
---

# Brainstorming Ideas Into Designs

## Overview

Turn ideas into fully formed designs through collaborative dialogue. Understand context, ask questions one at a time, then present the design incrementally.

## When to Use

- New feature development
- Significant behavior changes
- Architecture decisions
- Component design

## Steps

### 1. Research Prior Art (GATE -- mandatory before any proposals)

Before proposing anything, gather evidence. **You must not proceed to Step 3 until this checklist is complete.**

#### Research checklist

- [ ] **Read internal references** -- existing specs in `docs/plans/`, related code mentioned in the bead/task description, and reference implementations in the codebase. Use `Read`, `Grep`, `Glob` to find and read them.
- [ ] **Read external references** (when applicable) -- Use `WebSearch` / `WebFetch` when the problem domain has established solutions worth comparing (algorithms, protocols, libraries). Skip for project-internal design.
- [ ] **Present research summary to the user** -- before proposing anything, show:
  1. What files/sources you read (with paths or URLs)
  2. What approaches others took
  3. Key trade-offs observed

#### What counts as research

| Sufficient | Insufficient |
|------------|-------------|
| Read 2+ reference files and summarized findings | Read 0 files, jumped to proposals |
| Searched codebase for related patterns | Assumed you know the codebase |
| Checked `docs/plans/` for prior designs | Skipped because "this is new" |
| Used WebSearch for external prior art | Relied on training data alone |

#### Self-check before moving on

> "Can I cite specific files I read and specific patterns I observed?"
>
> If NO: go back and read more. You are not ready to propose.

### 2. Understand the Idea

- Check current project state (files, docs, recent commits)
- Ask questions **one at a time** — never multiple questions per message
- Prefer multiple choice questions when possible
- Focus on: purpose, constraints, success criteria

### 3. Explore Approaches

- Propose 2-3 approaches with trade-offs
- Lead with recommended option and explain why
- Keep YAGNI in mind — strip unnecessary features

### 4. Premortem Each Decision

**Before committing to any design choice**, stress-test it:

- Run a quick premortem (tigers/elephants/paper tigers) on each option
- Present failure modes alongside trade-offs — not after
- A decision without a premortem is a guess, not a design choice
- Use the `premortem` skill's quick checklist for individual decisions
- Only commit when the user has seen the risks and chosen deliberately

This applies to every architectural decision, not just the final plan.

### 5. Present the Design

- Break into sections of 200-300 words
- Ask after each section: "Does this look right so far?"
- Cover: architecture, components, data flow, error handling, testing
- Go back and clarify if anything doesn't make sense

### 6. Document

- Write validated design to `docs/plans/YYYY-MM-DD-<topic>-design.md`
- Include resolved premortems with each decision (risks accepted, mitigations chosen)
- Commit the design document

### 7. Implementation Handoff

- Ask: "Ready to set up for implementation?"
- Use `writing-plans` skill to create detailed implementation plan

## Key Principles

- **One question at a time** — Don't overwhelm
- **Multiple choice preferred** — Easier to answer
- **YAGNI ruthlessly** — Remove unnecessary features
- **Explore alternatives** — Always 2-3 approaches before settling
- **Incremental validation** — Present in sections, validate each

## Red Flags

- **Proposing approaches without citing specific files or sources you read** -- the #1 failure mode
- Jumping to implementation without understanding requirements
- Asking 5 questions at once
- Presenting entire design as a wall of text
- Adding features the user didn't ask for
- **Committing to a design decision without premorteming it first**
- Saying "based on my understanding" instead of "based on reading X at line Y"
