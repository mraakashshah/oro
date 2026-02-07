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

### 1. Understand the Idea

- Check current project state (files, docs, recent commits)
- Ask questions **one at a time** — never multiple questions per message
- Prefer multiple choice questions when possible
- Focus on: purpose, constraints, success criteria

### 2. Explore Approaches

- Propose 2-3 approaches with trade-offs
- Lead with recommended option and explain why
- Keep YAGNI in mind — strip unnecessary features

### 3. Premortem Each Decision

**Before committing to any design choice**, stress-test it:

- Run a quick premortem (tigers/elephants/paper tigers) on each option
- Present failure modes alongside trade-offs — not after
- A decision without a premortem is a guess, not a design choice
- Use the `premortem` skill's quick checklist for individual decisions
- Only commit when the user has seen the risks and chosen deliberately

This applies to every architectural decision, not just the final plan.

### 4. Present the Design

- Break into sections of 200-300 words
- Ask after each section: "Does this look right so far?"
- Cover: architecture, components, data flow, error handling, testing
- Go back and clarify if anything doesn't make sense

### 5. Document

- Write validated design to `docs/plans/YYYY-MM-DD-<topic>-design.md`
- Include resolved premortems with each decision (risks accepted, mitigations chosen)
- Commit the design document

### 6. Implementation Handoff

- Ask: "Ready to set up for implementation?"
- Use `writing-plans` skill to create detailed implementation plan

## Key Principles

- **One question at a time** — Don't overwhelm
- **Multiple choice preferred** — Easier to answer
- **YAGNI ruthlessly** — Remove unnecessary features
- **Explore alternatives** — Always 2-3 approaches before settling
- **Incremental validation** — Present in sections, validate each

## Red Flags

- Jumping to implementation without understanding requirements
- Asking 5 questions at once
- Presenting entire design as a wall of text
- Adding features the user didn't ask for
- **Committing to a design decision without premorteming it first**
