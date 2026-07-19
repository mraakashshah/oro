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
- The `enforce_worktree_writes` hook denies primary-checkout writes on both Codex (`Write`/`Edit`) and Codex (`apply_patch`). A denied edit means you are in the wrong tree — make a worktree.

## 2. Test-first — always

No production code without a failing test first. Red → green → refactor. Every feature and every bugfix needs a test. Full workflow: **`test-driven-development`**.

## 3. Standards

- **Python: always `uv`, never `pip`** — `uv run python`, `uv add <pkg>`, `uvx <tool>`, `uv init`. Never bare `python` or `pip`.
- **Functional-first**: pure functions, immutability, `map`/`filter`/`reduce`; pure core, impure edges (I/O, CLI). Early returns, fail fast.
- **Python style**: PEP 8, f-strings, docstrings, `PascalCase` classes, `snake_case`, `pytest` fixtures over classes.
- **Reusable over inline**: prefer standalone, callable scripts (`ad_hoc/`) over one-off inline code. Before building a scraper, API client, or reusable utility, check the shared `kit` inventory (surfaced at session start) and reuse it via `uv add --editable <path-to>/kit`; reusable tools you build belong in kit, not inline.
- **Data work**: pandas for manipulation; matplotlib + seaborn (objects interface) for viz; save plots to `data/` or `ad_hoc/`, never only in notebook state.
- **Match the surrounding file's** naming, idioms, and comment density. Keep changes small and atomic.
- **Log architectural decisions** to `docs/decisions&discoveries.md` — `## YYYY-MM-DD: Title` with Tags / Context / Decision / Implications. Notes in Markdown only.

## 4. Verify & land

Run the quality gate — tests, lint/format (`ruff`, `biome`, `go fmt`, `pyright`), build — and fix everything before proceeding. Commit with a conventional message (`type(scope): description`, always pass `-m`) — see **`git-commits`** — then `git pull --rebase && git push`. See **`verification-before-completion`** / **`finishing-work`**. Never claim done without proof.

## Red Flags — STOP

- About to Write/Edit a file in the primary checkout instead of a worktree
- Writing implementation before a failing test
- Bare `python` or `pip` instead of `uv`
- Thinking "too small for a worktree/test" — size is not an exemption

Violating the letter of these rules violates their spirit.
