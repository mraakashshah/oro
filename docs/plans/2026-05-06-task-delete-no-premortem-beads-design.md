# Task Delete And No Automatic Premortem Beads Design

Date: 2026-05-06
Status: draft

## Problem

Oro currently creates premortem beads as a side effect of normal child task
creation. That was intended as a planning gate for large epics, but in practice
it creates extra executable work that the operator did not ask for. The most
recent QG failure-handling epic produced an auto-created premortem child that
had to be manually closed as a duplicate process artifact.

Oro also has a soft-delete column in the bead schema, and several migration
paths already rely on it, but there is no normal `oro task delete` command. That
leaves operators using close reasons to hide unwanted tasks, or manual SQL when
a bead is plainly a process artifact.

## Goals

- Stop automatic premortem bead creation from normal task and graph creation.
- Stop premortem gate enforcement from blocking ordinary worker assignment or
  `oro work`.
- Add `oro task delete <id>` for operator cleanup of spurious beads.
- Use soft delete, not physical row deletion.
- Keep deleted beads hidden from normal task list, ready, blocked,
  in-progress, show, and dispatcher assignment paths.
- Preserve enough audit evidence to explain who deleted the bead and why.
- Keep implementation small enough that the factory can resume quickly after the
  fix lands.

## Non-goals

- Do not remove the historical `premortem` bead type from the schema in this
  change. That touches migrations, prompt routing, card behavior, and old data.
- Do not remove `oro bead premortem-close` in this change. It can stay as a
  legacy command for existing premortem rows.
- Do not add recursive delete in the first implementation.
- Do not make deletion a substitute for shutdown reset or assignment recovery.
- Do not hard-delete rows from `beads`.

## Current Behavior

Read:

- `cmd/oro/cmd_bead.go:119` routes child creates through
  `dispatcher.CreateBeadGraph` so the retroactive premortem gate fires.
- `cmd/oro/cmd_task.go:33` builds `oro task` by reusing bead subcommands, so
  adding a bead delete subcommand naturally exposes `oro task delete`.
- `pkg/dispatcher/retroactive_gate.go:30` creates child beads and then calls
  the retroactive gate.
- `pkg/dispatcher/retroactive_gate.go:55` transitions the parent gate to
  `eligible` and calls `spawnPremortemBead` once the child count exceeds five.
- `pkg/dispatcher/retroactive_gate.go:94` blocks child execution while the
  parent gate is `eligible` and no closed premortem child exists.
- `pkg/dispatcher/dispatcher.go:3804` applies that gate inside dispatcher
  executable filtering.
- `cmd/oro/cmd_work.go:273` repeats the premortem gate in the direct `oro work`
  path.
- `pkg/protocol/schema.go:166` already has `beads.deleted`.
- `pkg/protocol/schema.go:271` and `pkg/protocol/schema.go:304` hide deleted
  beads from ready and blocked views.
- `pkg/beadstore/sqlite.go:136` and `pkg/beadstore/readtx.go:93` hide deleted
  beads from `Show`.
- `cmd/oro/cmd_bead_migrate.go:1250` already soft-deletes migrated duplicate
  rows with `UPDATE beads SET deleted=1`.

## Design

### 1. Disable Automatic Premortem Creation

`createBeadFromParams` should call `Store.Create` for all CLI-created beads,
including beads with `ParentID`.

The `dispatcher.CreateBeadGraph` helper should no longer run
`checkRetroactiveGate` or spawn a premortem bead. It should remain a small
helper for creating a batch of child beads under one parent, because existing
dispatcher code uses it for graph creation and QG-fix bead creation.

The legacy retroactive gate code can remain present but unused in the first
change. The immediate acceptance rule is behavioral: normal task creation and
graph creation must not create type `premortem` rows.

Tests to replace:

- Replace `TestBeadCreateFiresRetroactiveGate` with a test that creates six
  child tasks under an epic and asserts no premortem child exists.
- Replace `dispatcher` retroactive gate tests that require auto-spawned
  premortem beads with tests asserting `CreateBeadGraph` creates only the
  requested children.

### 2. Disable Premortem Blocking

Remove the dispatcher assignment gate at `pkg/dispatcher/dispatcher.go:3804`.
Already-decomposed epics should continue to be skipped, but ordinary child
tasks should no longer be filtered through `CheckPremortemGate`.

Remove the direct `oro work` gate at `cmd/oro/cmd_work.go:273`. If a user asks
to work a bead directly, the premortem subsystem should not refuse execution.

The `CheckPremortemGate` function and related legacy tests can remain for a
later cleanup, but no production assignment path should call it.

Tests to add or update:

- Dispatcher filtering includes a child task whose parent epic has
  `gate_state='eligible'` and no closed premortem child.
- `oro work --dry-run <child>` succeeds for a child task whose parent epic has
  `gate_state='eligible'` and no closed premortem child.

### 3. Add Soft Delete To Beadstore

Add a store method:

```go
Delete(ctx context.Context, id string, reason string) error
```

`SQLiteStore.Delete` should run in one write transaction guarded by `writeMu`.
For the first implementation it should:

1. Confirm the bead exists with `deleted=0`.
2. Reject if the bead has any active assignment.
3. Reject if the bead has non-deleted child beads.
4. Remove dependency edges where the deleted bead is either `bead_id` or
   `depends_on_id`.
5. Set `deleted=1`, `updated_at=now`, and preserve all other bead fields.
6. Insert a `bead_deleted` event with `{ "reason": reason }`.
7. Append a bead journey event named `deleted` with actor `human` and the same
   reason if journey storage is available.

Deletion should not set `status='closed'`. A deleted bead is not a closed bead;
it is hidden from normal operational surfaces. Keeping the prior status
preserves what state was deleted.

The active-assignment guard is intentionally strict. If the factory is running,
operators should stop or reset active work first. `delete` is cleanup, not an
assignment-abandon mechanism.

The child guard is also intentional. Deleting an epic while children remain
would leave children with a deleted parent, which current ready/blocked views
treat as blocked. Recursive delete can be designed later if needed.

### 4. Add `oro task delete`

Add a `newBeadDeleteCmd` and register it on both `bead` and `task`, following
the existing alias pattern in `cmd/oro/cmd_task.go`.

Command:

```bash
oro task delete <id> [--reason <text>] [--json]
```

Behavior:

- If `--reason` is omitted, use `deleted by user`.
- Human output: `deleted <id>`.
- JSON output:

```json
{"id":"oro-xxxx","deleted":true,"reason":"..."}
```

Errors:

- Unknown or already deleted bead returns a not-found style error.
- Active assignment returns a clear refusal that names the bead.
- Non-deleted children return a clear refusal and say recursive delete is not
  supported.

Because `oro task` reuses bead subcommands, `oro bead delete` should work too,
but the user-facing acceptance target is `oro task delete`.

## Acceptance Test

The epic is done when this command passes against `main`:

```bash
go test ./cmd/oro ./pkg/beadstore ./pkg/dispatcher
```

Required assertions inside those tests:

- Six CLI-created child tasks under an epic do not create a premortem bead.
- `CreateBeadGraph` creates only requested child beads.
- Dispatcher executable filtering no longer blocks a task due to a premortem
  gate state.
- `oro work --dry-run` no longer blocks on the premortem gate.
- `oro task delete <id>` soft-deletes an open leaf task and hides it from
  `show`, `list`, `ready`, and dispatcher assignment reads.
- Deleting a dependency blocker removes dependency edges so dependents are not
  stuck behind a deleted bead.
- Deleting a bead with active assignment is rejected.
- Deleting an epic with non-deleted children is rejected.
- Deleting an unknown or already deleted bead is rejected.
- `--json` emits stable machine-readable output.

## Implementation Plan

1. Add `Store.Delete` to `pkg/beadstore/store.go` and implement it in
   `pkg/beadstore/sqlite.go` and `pkg/beadstore/testfake.go`.
2. Add focused beadstore tests for soft delete, active-assignment rejection,
   child rejection, dependency-edge cleanup, and already-deleted behavior.
3. Add `newBeadDeleteCmd` and register it in `newBeadCmdWithStore` and
   `newTaskCmdWithStore`.
4. Add CLI tests for human and JSON `oro task delete` output.
5. Change `createBeadFromParams` to call `Store.Create` even when `ParentID` is
   set.
6. Change `CreateBeadGraph` to stop invoking the retroactive gate.
7. Remove production calls to `CheckPremortemGate` from dispatcher filtering and
   `oro work`.
8. Replace old premortem auto-spawn and gate-blocking tests with no-premortem
   behavioral tests.
9. Run `go test ./cmd/oro ./pkg/beadstore ./pkg/dispatcher`.

## Deep Premortem

```yaml
premortem:
  mode: deep
  context: "no automatic premortem beads plus oro task delete"
  tigers:
    - risk: "Deleting a bead that is a dependency blocker could leave downstream tasks blocked forever if dependency edges remain."
      severity: high
      mitigation_checked: "beads_ready treats a missing or deleted depends_on_id as blocked at pkg/protocol/schema.go:282-288, so Delete must remove dependency edges involving the deleted bead."
    - risk: "Deleting an epic with live children could create a hidden-parent state where children are permanently blocked or confusing."
      severity: high
      mitigation_checked: "ready and blocked views require non-deleted parents for awaits_parent_close at pkg/protocol/schema.go:293-300 and 321-333, so v1 delete rejects non-deleted children."
    - risk: "Removing only CLI premortem creation would still let dispatcher graph creation create premortem beads."
      severity: high
      mitigation_checked: "dispatcher uses CreateBeadGraph at pkg/dispatcher/dispatcher.go:1943; the graph helper itself must stop gate checks."
    - risk: "Removing only auto-spawn would still leave old eligible gate states blocking workers."
      severity: high
      mitigation_checked: "dispatcher filtering calls CheckPremortemGate at pkg/dispatcher/dispatcher.go:3804 and oro work calls it at cmd/oro/cmd_work.go:273; both production calls must be removed."
  elephants:
    - risk: "The premortem subsystem is now legacy code, but fully deleting it is larger than the requested fix and risks migration churn."
  paper_tigers:
    - risk: "Keeping the premortem type in the schema might create new premortem beads."
      reason: "Schema allowance does not create rows by itself; creation and gating call sites are the actual behavior to remove."
    - risk: "Soft delete without status=closed may confuse closed counts."
      reason: "Existing count, list, show, ready, and blocked paths filter deleted=0, so deleted beads are outside normal operational counts."
```

## Adversarial Review

```yaml
verdict: PASS
spec: "docs/plans/2026-05-06-task-delete-no-premortem-beads-design.md"
reviewer_note: "The spec covers the production call chains that create or enforce premortem beads and defines delete semantics around the existing soft-delete column."
acceptance_test:
  cmd: "go test ./cmd/oro ./pkg/beadstore ./pkg/dispatcher"
  assert: "exit code 0 with the listed behavioral assertions present"
  adequate: true
  issues: []
traceability:
  covered: 10
  gaps: 0
  matrix: |
    | # | Criterion | Implementation step | Test |
    | 1 | CLI child create makes no premortem | 5 | cmd/oro create test |
    | 2 | Graph create makes no premortem | 6 | dispatcher graph test |
    | 3 | Dispatcher does not gate on premortem | 7 | dispatcher filter test |
    | 4 | oro work does not gate on premortem | 7 | cmd/oro work dry-run test |
    | 5 | task delete soft-deletes and hides bead | 1, 3 | beadstore and CLI tests |
    | 6 | delete removes dependency edges | 1 | beadstore ready/dependency test |
    | 7 | delete rejects active assignment | 1 | beadstore test |
    | 8 | delete rejects non-empty epic | 1 | beadstore test |
    | 9 | delete rejects missing/deleted id | 1, 3 | beadstore and CLI tests |
    | 10 | delete emits JSON output | 3 | CLI test |
wiring_gaps: []
negative_space:
  - area: "Recursive delete"
    severity: minor
    fix: "Explicitly non-goal; reject epics with children in v1."
  - area: "Hard removal of premortem schema and prompts"
    severity: minor
    fix: "Explicitly non-goal; stop behavior by removing production call sites."
red_team_scenarios:
  - scenario: "Worker removes CLI auto-spawn only; dispatcher CreateBeadGraph still spawns premortems."
    beads_pass: false
    feature_works: false
    root_cause: "Spec requires changing CreateBeadGraph itself and testing dispatcher graph creation."
    fix: "Covered by steps 6 and graph test."
  - scenario: "Worker adds Delete by setting deleted=1 but leaves dependency edges; dependents stay blocked."
    beads_pass: false
    feature_works: false
    root_cause: "Acceptance requires dependency-edge cleanup and ready behavior test."
    fix: "Covered by step 1 and dependency test."
  - scenario: "Old eligible premortem gate states remain in DB and block assignment."
    beads_pass: false
    feature_works: false
    root_cause: "Spec requires removing dispatcher and oro work production gate calls."
    fix: "Covered by step 7."
integration_points:
  covered:
    - "cmd/oro/cmd_bead.go:createBeadFromParams"
    - "cmd/oro/cmd_bead.go:newBeadCmdWithStore"
    - "cmd/oro/cmd_task.go:newTaskCmdWithStore"
    - "cmd/oro/cmd_work.go:executeWork"
    - "pkg/beadstore/store.go:Store"
    - "pkg/beadstore/sqlite.go:SQLiteStore"
    - "pkg/beadstore/testfake.go:FakeStore"
    - "pkg/dispatcher/retroactive_gate.go:CreateBeadGraph"
    - "pkg/dispatcher/dispatcher.go:filterExecutableBeads"
    - "pkg/protocol/schema.go:beads_ready and beads_blocked"
  uncovered: []
```
