# Documentation Index

This directory keeps project documentation that is useful to contributors,
operators, and future agents.

## Structure

- `INSTALL.md` - install and contributor setup instructions.
- `dev-setup.md` - local development toolchain notes.
- `decisions&discoveries.md` - long-lived project decisions and discoveries.
- `plans/` - active design docs, specs, and implementation plans.
- `plans/done/` - completed plans retained as historical context.
- `plans/notes/` - supporting notes, inventories, and spike reports for plans.
- `runbooks/` - current operator procedures.
- `runbooks/drills/` - recovery and operations drill records.
- `runbooks/incidents/` - incident writeups and blockers.
- `runbooks/logs/` - dated operator logs.
- `audits/` - technical audits and pressure reviews.
- `research/` - external or comparative research.
- `learnings/` - synthesized learnings from prior art and project work.
- `proofs/` - proof artifacts for critical workflows.
- `solutions/` - solved problem writeups.
- `handoffs/` - generated session handoff state; most snapshots are ignored.
- `archive/` - superseded, compatibility-only, or noncanonical docs.

## Naming Conventions

- Use dated kebab-case filenames for time-bound docs:
  `YYYY-MM-DD-topic-name.md`.
- Keep active design work in `plans/`; move completed design work to
  `plans/done/`.
- Use `runbooks/` for current procedures. Put dated execution notes under
  `runbooks/logs/` and incident-specific notes under `runbooks/incidents/`.
- Prefer archiving over deleting unless a file is an exact duplicate with a
  canonical copy already present.
- Keep generated handoff snapshots under `handoffs/`; do not manually curate
  ignored snapshot copies.
- Update links whenever a doc moves.

## Review Pattern Inbox

Ops review pattern capture no longer appends directly to the curated
`assets/review-patterns.md` file during normal operation. That direct append
behavior is superseded by the resolved `review-pattern-candidates` inbox:
inspect candidates with `oro review-patterns candidates`, then promote deduped
entries intentionally with `oro review-patterns promote --all`.
