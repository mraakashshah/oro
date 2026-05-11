# Swarm Throughput Recovery Design

## Goal

Restore Oro swarm throughput by removing the retry loops that consumed worker
and reviewer cycles during the May 11 run.

The target outcome is not just "fewer failures". It is a factory that stops
unproductive work early, preserves useful worker output, and only spends review
and QG cycles on changes that can land.

## Evidence From Shutdown Analysis

In the last roughly 8 hours before shutdown:

- 51 assignments were made.
- 25 beads closed.
- 9 closures were productive merges or fixes.
- 16 closures were deferrals or duplicate incident cleanup.
- 46 QG rejections occurred.
- 48 review rejections occurred.
- 24 progress timeouts occurred.

Top repeat offenders:

- `oro-term`: 14 progress timeouts, 13 review rejections, 8 QG rejections.
- `oro-qg-incident-67`: 10 progress timeouts, 10 review rejections.
- `oro-6hjb`: 9 QG rejections, mostly formatting/import failures.
- `oro-8g2z`: 7 QG rejections and 4 review rejections.
- `oro-4b2w`: 4 QG rejections and 4 review rejections.

Concrete observed failure classes:

1. QG scanned generated/temp paths.
   `yamllint` failed on `.tmp-test/tmp/pytest-of-as21/.../handoff.yaml`,
   which was a generated malformed fixture, not a source file.

2. QG ran load-sensitive tests in normal worker gates.
   `TestPriorityContention_StableUnderLoad` and
   `TestConsolidation_TriggeredAfterNCompletions` failed across unrelated
   beads, so unrelated workers paid for shared timing instability.

3. Test DB setup missed semantic-memory search migrations.
   Repeated `memory_search_events` missing-table failures generated duplicate
   QG incident beads.

4. Workers reached review with untracked task files.
   Several workers created the correct files but did not stage them, so the
   reviewer saw missing implementation in the diff and rejected repeatedly.

5. Reviewer verification used sandbox-hostile full package tests.
   Codex review subprocesses ran broad test commands that hit UDS bind,
   home-dir write, tmp/cache, or other sandbox restrictions.

6. Retry policy spent too many attempts before stopping.
   Repeated QG fingerprints and review patterns were allowed to consume
   multiple assignment, QG, review, and timeout cycles before deferral.

7. Shutdown did not kill all child process groups.
   After `oro stop --force`, orphaned QG shell and `go test` processes remained
   under PID 1.

## Design

### 1. Make QG Source-Scoped

`scripts/quality_gate.sh` and the generated quality gate must only lint source
and committed configuration paths. Tools that walk the filesystem must never
run on `.tmp-test`, `.cache`, `.worktrees`, test output folders, or arbitrary
repo root temp files.

The existing decision in `docs/decisions&discoveries.md` already says tools
that walk the filesystem must use explicit paths. This change enforces that
principle for docs/config/Python lanes.

Acceptance shape:

- `yamllint` runs only on tracked source config files, or on an explicit allow
  list of config/docs files.
- `ruff`, `pyright`, and related Python tooling avoid generated/cache paths.
- Generated quality gate output matches the checked-in script.

### 2. Split Worker QG From Push QG For Load-Sensitive Tests

Worker QG should catch regressions in normal deterministic tests. Push QG can
continue running heavier load-sensitive tests.

Tests known to be timing/load sensitive should either become deterministic or
use the existing `pkg/testutil/loadguard.SkipOutsidePushQG` guard. Worker QG
must not repeatedly reject unrelated docs/setup changes because a global load
test flakes.

Acceptance shape:

- Normal local/worker `./scripts/quality_gate.sh` does not run known
  load-sensitive tests.
- `ORO_QG_CONTEXT=push ./scripts/quality_gate.sh` still includes them.
- The skip reason is explicit so a skipped load test is visible.

### 3. Apply Semantic-Memory Search Migrations In Dispatcher Test DBs

`pkg/dispatcher/dispatcher_test.go:newTestDB` applies `protocol.SchemaDDL` but
does not apply `protocol.MigrateSemanticMemorySearchEvents`. That test-only DB
path produced repeated `memory_search_events` missing-table failures and
duplicate QG incidents.

Acceptance shape:

- Fresh dispatcher test DBs can insert into `memory_search_events`.
- `go test ./pkg/dispatcher -run TestNewTestDBMigratesMemorySearchEvents -count=1`
  fails before the fix and passes after.

### 4. Add A Pre-Review Git Hygiene Gate

No worker should enter ops review while task-relevant files are untracked or
unstaged. The dispatcher already knows the worktree path when handling
`ready_for_review`; that is the right boundary to fail fast before spawning an
ops reviewer.

Policy:

- If `git status --porcelain` shows untracked files or unstaged/staged changes
  that are not represented in the review diff, send feedback to the worker and
  do not spawn review.
- The feedback must include the exact files and the action: stage/commit task
  files or remove unrelated edits.
- This should count as worker feedback, not an ops review rejection.

Acceptance shape:

- A worktree with untracked task files produces `pre_review_git_dirty` and a
  worker retry message.
- No `review_rejected` event is logged for this condition.

### 5. Make Review Verification Environment-Aware

The reviewer should still catch real regressions, but it must not convert
sandbox limitations into repeated task rejections.

Policy:

- Reviewer prompts must prefer the task acceptance command and the directly
  affected package tests.
- If a broader package test fails with known sandbox/environment signatures
  while the acceptance command passes, the result is a review-environment
  escalation or infra classification, not an ordinary worker rejection.
- Known signatures include UDS bind denial, home-dir write denial, uv cache
  permission denial, and isolated tmp/cache restrictions.

Acceptance shape:

- A review result containing a known sandbox signature is classified as
  `review_env_blocked`.
- The dispatcher does not increment the normal rejection count for
  `review_env_blocked`.

### 6. Improve QG Failure Classification And Retry Economics

The classifier currently treats many shared failures as deterministic worker
failures because they mention `FAIL`, `golangci-lint`, or test failures. It
should recognize the dominant shared patterns from the run:

- known load-sensitive tests,
- generated temp path lint failures,
- missing test DB migrations,
- formatting/import failures,
- subprocess died unexpectedly.

Policy:

- Same fingerprint across multiple beads should become systemic quickly.
- Same fingerprint on the same bead should not burn more than the configured
  retry budget.
- Systemic failures should create or reuse one infra bead and defer affected
  originals with notes.
- Formatting/import failures remain worker deterministic but receive precise
  feedback and a small retry budget.

Acceptance shape:

- A repeated `TestPriorityContention_StableUnderLoad` fingerprint across two
  beads classifies as systemic/flaky, not worker deterministic.
- A `.tmp-test/.../handoff.yaml` yamllint failure classifies as QG source-scope
  infra, not task code.
- A gofumpt/goimports failure classifies as worker deterministic with direct
  retry feedback.

### 7. Make Stop Kill The Whole Swarm Process Tree

`oro stop --force` should leave no dispatcher, worker, QG shell, `go test`,
`golangci-lint`, or ops-review child processes alive.

Policy:

- Workers and their subprocesses must run in killable process groups.
- Stop must terminate worker process groups, not only dispatcher/worker PIDs.
- Stop must verify no matching children remain and log residuals if any had to
  be force-killed.

Acceptance shape:

- A test launches a fake worker with a nested sleep child, runs stop cleanup,
  and asserts the nested child is gone.

### 8. Add Throughput Health Metrics

Operators need a compact signal that separates utilization from useful output.

Add a status or report command that shows:

- productive closures per hour,
- deferred/duplicate closures per hour,
- assignments per hour,
- QG rejection count,
- review rejection count,
- progress timeout count,
- top repeated beads/fingerprints.

Acceptance shape:

- A fixture DB with mixed timestamp formats reports normalized counts.
- Productive closure excludes `DEFERRED` and duplicate incident cleanup.

## Plan Premortem

```yaml
premortem:
  mode: deep
  context: "swarm throughput recovery"

  tigers:
    - risk: "QG scoping hides real config problems if allowlists are too narrow"
      severity: high
      mitigation_checked: "Task requires explicit tracked-file allowlist and tests for generated temp exclusion plus real config inclusion."
    - risk: "Review environment classification could mask real regressions"
      severity: high
      mitigation_checked: "Task requires acceptance command to pass before sandbox signatures downgrade rejection."
    - risk: "Retry policy could defer actual worker bugs too early"
      severity: medium
      mitigation_checked: "Task separates formatting/import deterministic failures from systemic fingerprints."
    - risk: "Stop process-tree killing could kill unrelated user processes"
      severity: high
      mitigation_checked: "Task requires process-group ownership and targeted worker descendant matching."

  elephants:
    - risk: "The factory cannot be fast if every small bead pays full-suite QG and broad review tests."

  paper_tigers:
    - risk: "Adding health metrics is extra scope"
      reason: "It is read-only and prevents future subjective throughput debates."
```

## Rollout

1. Land QG/test-environment fixes first.
2. Land pre-review git hygiene next, because it prevents known bad review loops.
3. Land classification/retry economics after the failure taxonomy is tested.
4. Land stop process-tree cleanup before the next long swarm run.
5. Add throughput health reporting last.

Relaunch criteria:

- `go test ./pkg/dispatcher -run 'TestNewTestDBMigratesMemorySearchEvents|TestClassifyQGFailure|TestPreReview' -count=1`
- `scripts/test_quality_gate.sh`
- `go test ./pkg/worker/... -run 'TestRunQualityGate|TestStop' -count=1`
- `make build`
