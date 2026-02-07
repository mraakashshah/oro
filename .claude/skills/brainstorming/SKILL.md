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

### 3. Present the Design

- Break into sections of 200-300 words
- Ask after each section: "Does this look right so far?"
- Cover: architecture, components, data flow, error handling, testing
- Go back and clarify if anything doesn't make sense

### 4. Document

- Write validated design to `docs/plans/YYYY-MM-DD-<topic>-design.md`
- Commit the design document

### 5. Implementation Handoff

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
