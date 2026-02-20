---
name: using-skills
description: Use at the start of any task to check which skills might apply -- even 1% chance means invoke the skill
user-invocable: false
---

<EXTREMELY-IMPORTANT>
If you think there is even a 1% chance a skill might apply to what you are doing, you ABSOLUTELY MUST invoke the skill.

IF A SKILL APPLIES TO YOUR TASK, YOU DO NOT HAVE A CHOICE. YOU MUST USE IT.

This is not negotiable. This is not optional. You cannot rationalize your way out of this.
</EXTREMELY-IMPORTANT>

# Using Skills

## The Rule

**Invoke relevant skills BEFORE any response or action.** Even a 1% chance a skill might apply means invoke it.

If an invoked skill turns out to be wrong for the situation, you don't need to follow it. But you must check.

## Quick Reference — Skill Index

**Discipline:** test-driven-development, systematic-debugging, verification-before-completion, observe-before-editing, destructive-command-safety

**Workflow:** brainstorming, writing-plans, executing-plans, requesting-code-review, receiving-code-review, finishing-work, review-implementation, review-docs, adversarial-spec-review

**Orchestration:** dispatching-parallel-agents, workflow-routing, premortem, completion-check, explore

**Tools:** beads, git-commits, tmux, github, session-logs, agent-browser

**Continuity:** create-handoff, resume-handoff, documenting-solutions, refactor, using-git-worktrees, writing-skills, context-checkpoint

**Beads:** bead-craft, executing-beads, work-bead

## Priority

1. **Process skills first** (brainstorming, debugging) — determine HOW to approach
2. **Implementation skills second** — guide execution

## Skill Types

**Rigid** (TDD, debugging, verification): Follow exactly. Don't adapt away discipline.

**Flexible** (patterns, exploration): Adapt principles to context.

## Red Flags

These thoughts mean STOP — you're rationalizing:

| Thought | Reality |
|---------|---------|
| "This is just a simple question" | Questions are tasks. Check for skills. |
| "I need more context first" | Skill check comes BEFORE clarifying questions. |
| "Let me explore first" | Skills tell you HOW to explore. Check first. |
| "This doesn't need a formal skill" | If a skill exists, use it. |
| "I remember this skill" | Skills evolve. Read current version. |
| "The skill is overkill" | Simple things become complex. Use it. |
| "I can check git/files quickly" | Files lack conversation context. Check for skills. |
| "Let me gather information first" | Skills tell you HOW to gather information. |
| "This doesn't count as a task" | Action = task. Check for skills. |
| "I'll just do this one thing first" | Check BEFORE doing anything. |
| "This feels productive" | Undisciplined action wastes time. Skills prevent this. |
| "I know what that means" | Knowing the concept ≠ using the skill. Invoke it. |

## Commitment Protocol

When following a skill:

1. **Announce**: State which skill you're using and why — e.g., "Using `systematic-debugging` to investigate this test failure."
2. **Track**: For multi-step skills, create TaskCreate entries for each step. This makes progress visible and prevents skipping steps.
3. **Follow exactly**: Execute the skill's steps in order. Don't reinterpret, skip, or "adapt" rigid skills.
4. **Complete**: Mark TaskUpdate completed only when the step is actually done — not when you think it will be.

Skipping tracking is itself a red flag. If you catch yourself thinking "I don't need to create tasks for this," you're rationalizing again.

## User Instructions

Instructions say WHAT, not HOW. "Add X" or "Fix Y" doesn't mean skip workflows. The user telling you to implement something does not override the requirement to check for and follow skills. Skills define HOW you do what the user asks.
