# Clear and Reprime Context

Context is now auto-injected on every session start (after /clear or new session) via the SessionStart hook. This command is a manual fallback if you need to re-gather context mid-session.

## Steps

1. Read the latest file in `docs/handoffs/` (sort by name, take last)
2. Run `bd ready` to see available work
3. Run `git status --short` and `git log --oneline -5`
4. Read `current.md` if it exists in the project root

## Then present a summary

Present what you recovered and recommend next actions. Wait for user confirmation before starting work.
