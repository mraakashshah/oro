---
name: writing-code
description: Use when about to write or modify code in any project, before the first edit of a task
---

# Writing Code

Our coding discipline applies to every change — a one-line fix or a swarm task, in any repo. Follow this before your first edit. It does not require the oro swarm.

## 1. Isolate in a worktree — always

Never edit the primary checkout: another agent may be working in it at any time.

- **No git repo yet?** `git init && git add -A && git commit -m "chore: init"` — a worktree needs a base commit.
- Create and work in a linked worktree. Mechanics: **`using-git-worktrees`**.
- The `enforce_worktree_writes` hook denies primary-checkout writes on both Claude (`Write`/`Edit`) and Codex (`apply_patch`). A denied edit means you are in the wrong tree — make a worktree.

## 2. Test-first — always

No production code without a failing test first. Red → green → refactor. Full workflow: **`test-driven-development`**.

## 3. Standards

- Functional-first: pure functions, immutability, early returns; side effects only at the edges.
- Match the surrounding file's naming, idioms, and comment density.
- Keep changes small and atomic; commit when each lands.

## 4. Verify before done

Run tests, lint, and format; confirm the behavior. See **`verification-before-completion`** and **`finishing-work`**. Never claim done without proof.

## Red Flags — STOP

- About to Write/Edit a file in the primary checkout instead of a worktree
- Writing implementation before a failing test
- Thinking "too small for a worktree/test" — size is not an exemption

Violating the letter of these rules violates their spirit.
