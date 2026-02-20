---
name: executing-plans
description: Use when you have a written implementation plan to execute, with review checkpoints between tasks
---

# Executing Plans

## Overview

Load plan, review critically, execute tasks in batches, report for review between batches.

**Core principle:** Batch execution with checkpoints. Review after every batch.

## Steps

### Step 1: Load and Review Plan

1. Read plan file completely
2. Review critically — identify questions or concerns
3. If concerns: raise with the user before starting
4. If clear: proceed

### Step 2: Execute Batch (3 tasks default)

For each task:
1. Mark as in_progress
2. Follow each step exactly (plan has bite-sized steps)
3. Run verifications as specified
4. Mark as completed

### Step 3: Two-Stage Review

After each task (or batch):

**Stage 1 — Spec Compliance:**
- Does implementation match plan requirements?
- Any missing or extra functionality?
- If issues found: fix before moving to Stage 2

**Stage 2 — Code Quality:**
- Clean architecture?
- Magic numbers, naming, duplication?
- If issues found: fix before moving to next task

### Step 4: Report and Continue

When batch complete:
- Show what was implemented
- Show verification output
- Say: "Ready for feedback."
- Apply feedback, execute next batch
- Repeat until complete

### Step 5: Complete Development

After all tasks verified:
- Use `finishing-work` skill to handle merge/PR/cleanup

## When to STOP

**Stop executing immediately when:**
- Hit a blocker (missing dependency, test fails, unclear instruction)
- Plan has critical gaps
- You don't understand an instruction
- Verification fails repeatedly

**Ask for clarification rather than guessing.**

## Subagent Execution (Optional)

For independent tasks, dispatch subagents:
- Provide full task text (don't make subagent read plan file)
- Include scene-setting context
- Run spec compliance review after each subagent completes
- Run code quality review after spec compliance passes
- Answer subagent questions before letting them proceed

## Red Flags

- Skipping verifications
- Guessing when blocked instead of asking
- Starting on main/master without user consent
- Batching without review checkpoints
- Proceeding with unfixed review issues
