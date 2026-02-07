---
name: beads
description: Use when work spans multiple sessions, has dependencies or blockers, or needs context that survives conversation compaction
---

# Beads — Persistent Task Memory

## Overview

Git-backed issue tracker that survives conversation compaction. Provides persistent memory for multi-session work with dependencies.

**CLI reference**: Run `bd prime` for AI-optimized context. Run `bd <command> --help` for specific usage. Do not memorize commands — always check `--help`.

## bd vs TodoWrite

| bd (persistent) | TodoWrite (ephemeral) |
|-----------------|----------------------|
| Multi-session work | Single-session tasks |
| Complex dependencies | Linear execution |
| Survives compaction | Conversation-scoped |
| Git-backed, team sync | Local to session |

**Decision test**: "Will I need this context in 2 weeks?" YES = bd.

## When to Use bd

- Work spans multiple sessions or days
- Tasks have dependencies or blockers
- Need to survive conversation compaction
- Collaboration with team (git sync)
- Exploratory/research work with fuzzy boundaries

## When to Use TodoWrite

- Single-session linear tasks
- Simple checklist for immediate work
- All context is in current conversation

## Session Protocol

1. `bd ready` — Find unblocked work
2. `bd show <id>` — Get full context
3. `bd update <id> --status in_progress` — Claim work
4. Work, adding notes as you go (critical for compaction survival)
5. `bd close <id> --reason "..."` — Complete task
6. `bd sync` — Persist to git (mandatory at session end)

## Key Commands

| Action | Command |
|--------|---------|
| Find work | `bd ready` |
| Create issue | `bd create "title" -p <priority>` |
| Show details | `bd show <id>` |
| Update fields | `bd update <id> --status/--title/--notes/--description` |
| Add dependency | `bd dep add <issue> <depends-on>` |
| Close | `bd close <id> --reason "..."` |
| Sync to git | `bd sync` |

**Never use `bd edit`** — it opens `$EDITOR` which agents cannot use. Use `bd update` with flags.

## Advanced Features

Run `bd prime` for full details on:
- **Molecules** (templates): `bd mol --help`
- **Chemistry** (pour/wisp): `bd pour`, `bd wisp`
- **Agent beads**: `bd agent --help`
- **Async gates**: `bd gate --help`
- **Worktrees**: `bd worktree --help`

## Red Flags

- Forgetting `bd sync` at session end (changes stay local)
- Using `bd edit` (interactive editor, breaks agents)
- Creating issues in production DB during testing (use `BEADS_DB=/tmp/test.db`)
- Duplicating CLI docs in notes — point to `bd prime` instead
