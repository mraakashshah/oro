# Runtime-storage epic rebase conflicts

**Date:** 2026-07-20
**Component:** Git integration for `epic/oro-runtime-storage`
**Severity:** medium

## Symptom

Rebasing the epic-derived agent branch onto `main` stopped on catalog files with:

```
CONFLICT (add/add): Merge conflict in pkg/storage/catalog.go
CONFLICT (add/add): Merge conflict in pkg/storage/catalog_test.go
```

Subsequent historical catalog commits also conflicted while replaying.

## Root Cause

`main` had introduced the catalog foundation independently while the epic carried
the runtime-lease implementation and its follow-up compatibility changes. The
two histories added the same files from different bases, so a normal rebase could
not infer which portions of the two catalog APIs belonged together.

## Solution

Inspect each conflicting commit. Retain `main` for superseded foundation-only
changes, retain the epic's final catalog composition where it combines the
foundation with runtime leases, and run the full test suite after the rebase.
Then create an `ours` merge of the preserved epic ref into the rebased agent
branch. The merge has no tree change, but keeps both `main` and the original epic
tip as ancestors so the dispatcher can fast-forward the epic ref.

The composed catalog migration advances `PRAGMA user_version` to 2. Storage
status, health, and cleanup must compare that value with
`storage.CatalogSchemaVersion` and validate both the foundation and runtime
tables. Otherwise healthy catalogs are reported as corrupt, while a version-2
catalog missing runtime tables can be reported as healthy.

## Prevention

Before resolving an epic rebase conflict, compare the conflicting commit with
the current target branch rather than choosing a side wholesale. After the
rebase, always verify both ancestry checks:

```
git merge-base --is-ancestor main HEAD
git merge-base --is-ancestor epic/<id> HEAD
```

Do not repeat a plain `git rebase main` after adding the ancestry-preserving
merge: Git flattens the merge and queues the preserved epic commits for replay.
If a retry is already integration-ready, keep the existing tip and verify its
ancestry and tests instead.
