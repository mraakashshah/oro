# Assignment Quarantine Freeze Implementation Plan

> **For Codex:** Use executing-plans skill to implement this plan task-by-task.

**Goal:** Make an active quarantine-driven assignment freeze explicit in dispatcher status and factory health.

**Architecture:** Treat `recoveryQuarantineAssignmentScope` as the authoritative gate evaluation and persist its latest frozen/count/reason decision under the dispatcher mutex. Status reads that decision directly, while factory-health evaluation receives the same snapshot and emits both machine-readable metrics and a dedicated critical finding. This distinguishes an active global freeze from recovery quarantines that are empty-safe or auto-redeployable.

**Tech Stack:** Go, SQLite-backed dispatcher state, JSON status/health contracts.

## Global Constraints

- Use TDD: the named dispatcher regression must fail before production changes.
- Preserve `recoveryQuarantineAssignmentScope(ctx) (map[string]bool, bool)` for existing callers.
- Report a boolean frozen indicator plus blocking quarantine count and reason.
- Clear the signal whenever the gate evaluates as not frozen.
- Run `go test ./pkg/dispatcher ./pkg/factoryhealth -run 'Status|Health|Quarantine|Frozen' -count=1` and the repository quality gate.

---

### Task 1: Persist and expose the active quarantine assignment freeze

**Files:**
- Modify: `pkg/dispatcher/health_test.go`
- Modify: `pkg/factoryhealth/health_test.go`
- Modify: `pkg/dispatcher/dispatcher.go`
- Modify: `pkg/dispatcher/health.go`
- Modify: `pkg/factoryhealth/health.go`
- Modify: `cmd/oro/cmd_status.go`
- Modify: `cmd/oro/cmd_status_test.go`

**Interfaces:**
- Consumes: `func (d *Dispatcher) recoveryQuarantineAssignmentScope(ctx context.Context) (map[string]bool, bool)`
- Produces: status/health JSON fields `assignment_frozen_by_quarantine`, `blocking_recovery_quarantines`, and `assignment_freeze_reason`
- Produces: factory-health finding code `assignment_frozen_by_quarantine`

**Step 1: Write the failing tests**

Add `TestStatusReportsAssignmentFrozenByQuarantine` that creates a ready bead and idle worker, inserts a preservable quarantine, invokes `recoveryQuarantineAssignmentScope`, then asserts status and embedded health expose `true`, count `1`, and reason `open_recovery_quarantine`. Resolve the quarantine, reevaluate the scope, and assert the boolean is false and count/reason are cleared. Add a pure factory-health evaluator test that asserts the dedicated finding and metrics.

Add direct `applyHealth` coverage for the frozen state and a CLI parse/format round-trip test proving `oro status --json` preserves all three root-level fields.

**Step 2: Run tests to verify they fail**

Run: `go test ./pkg/dispatcher ./pkg/factoryhealth -run 'StatusReportsAssignmentFrozen|EvaluateAssignmentFrozen' -count=1 -v`

Expected: FAIL because the status response, health metrics, and finding do not yet contain the assignment-freeze fields.

**Step 3: Write minimal implementation**

Add dispatcher mutex-protected fields for frozen/count/reason. Update every return path in `recoveryQuarantineAssignmentScope` to store either the blocking decision or a cleared decision. Snapshot those fields into `statusResponse` and `factoryHealthInput`; map them through `factoryhealth.Snapshot` to `factoryhealth.Metrics`. Add a critical factory-health finding when the frozen flag is true, including count, reason, and idle-worker context in the message.

Mirror the root-level fields in `cmd/oro`'s private `statusResponse` so JSON output preserves the dispatcher contract.

**Step 4: Run tests to verify they pass**

Run: `go test ./pkg/dispatcher ./pkg/factoryhealth -run 'StatusReportsAssignmentFrozen|EvaluateAssignmentFrozen' -count=1 -v`

Expected: PASS.

**Step 5: Refactor and verify the acceptance contract**

Keep freeze state transitions in small setter/snapshot helpers, run gofumpt/goimports on modified Go files, and run:

`go test ./pkg/dispatcher ./pkg/factoryhealth -run 'Status|Health|Quarantine|Frozen' -count=1`

Expected: PASS with the named acceptance test visibly selected.

**Step 6: Commit**

Review the staged diff and commit with `fix(dispatcher): surface quarantine assignment freeze`.
