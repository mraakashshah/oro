# Forensic-Safe Recovery Runbook

Use this when `oro health` reports `recovery_quarantine_open`.

If instead `oro start` refuses to launch because active assignments block the
v4 migration, see
[stale-assignment-migration-deadlock.md](stale-assignment-migration-deadlock.md)
first — it quarantines the stale rows so they surface as the open quarantines
this runbook resolves.

## Inspect

```sh
oro health --json
oro status --json
oro recovery list
oro recovery list --json
```

For each open row, inspect the named branch and worktree before taking any
cleanup action:

```sh
git status --short
git branch --list 'agent/*'
git -C <worktree> status --short
git log --oneline --decorate --max-count=20 <branch>
```

A task can have more than one open quarantine when it has multiple unsafe
conditions. Inspect and resolve each row separately.

## Preserve Work

Before resolving, choose one explicit preservation outcome:

- Merge the branch through the normal Oro path if the work should land.
- Create a backup branch or patch if the work needs offline inspection.
- Intentionally discard only after confirming the branch/worktree has no useful
  changes.

Useful commands:

```sh
git branch recovery/<task>-<id> <branch>
git -C <worktree> diff > /tmp/<task>-<id>.patch
git -C <worktree> status --short
```

After the work is preserved, clean the branch/worktree deliberately if the task
should be re-readied. Prefer safe deletion:

```sh
git worktree remove <worktree>
git branch -d <branch>
```

If `git branch -d` refuses because the branch is unmerged, do not force-delete
until the work has been backed up or intentionally discarded.

## Resolve

Resolve only after the work has been preserved, merged, or intentionally
discarded:

```sh
oro recovery resolve <id>
```

Resolving a quarantine only closes the `recovery_quarantines` row. Historical
assignment rows may remain `quarantined`; create a fresh assignment by returning
the task to the normal ready path after the work is preserved or merged.

Then rerun:

```sh
oro health --json
oro status
```

Do not force-delete an `agent/<task>` branch or remove a worktree just to clear
the finding. If the branch is unmerged or ambiguous, preserve it outside the
factory first.
