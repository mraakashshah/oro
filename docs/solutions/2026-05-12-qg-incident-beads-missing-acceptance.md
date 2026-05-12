# QG Incident Beads Skipped As Missing Acceptance

**Date:** 2026-05-12
**Component:** dispatcher QG failure handling
**Severity:** high

## Symptom

The factory showed ready P0 QG incident bugs, but workers kept taking lower-priority work. Filtered logs showed:

```text
bead_skipped_missing_ac | oro-qg-incident-105 | {"reason":"missing_acceptance"}
bead_skipped_missing_ac | oro-qg-incident-4 | {"reason":"missing_acceptance"}
```

## Investigation

`oro task ready` listed the P0 incidents, but `oro directive status` showed workers assigned to regular tasks. The dispatcher log made the actual skip reason visible once `missing_accept` events were no longer filtered out.

Focused tests for the apparent worker QG failures passed on main. Full `./scripts/quality_gate.sh` then exposed an unrelated `shfmt` drift, which was fixed first so new worktrees would start from a clean base.

## Root Cause

`ensureQGIncidentBead` created systemic QG incident bugs without `AcceptanceCriteria`. The dispatcher requires non-epic worker tasks to have acceptance criteria, so these P0 repair tasks were visible in ready lists but skipped during assignment.

Existing incident beads also kept missing AC when reused or reopened.

## Solution

`pkg/dispatcher/qg_failure_notes.go` now assigns executable `Test/Cmd/Assert` acceptance to newly created QG incident beads and backfills acceptance for existing incident beads when they are reused. Regression coverage lives in `pkg/dispatcher/qg_failure_notes_test.go`.

For live incidents created before this fix, update the task acceptance criteria manually:

```bash
oro task update oro-qg-incident-105 --acceptance "Test: quality gate reproduction
Cmd: ./scripts/quality_gate.sh
Assert: quality gate passes after addressing the incident fingerprint"
```

## Prevention

When P0 tasks remain ready while workers take lower-priority work, inspect unfiltered logs for `bead_skipped_missing_ac` and `bead_skipped_non_tdd_acceptance`. Ready-list visibility alone does not prove a task is executable by workers.
