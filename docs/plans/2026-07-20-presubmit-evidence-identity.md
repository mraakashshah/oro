# Presubmit Evidence Identity Implementation Plan

> **For Codex:** Use executing-plans skill to implement this plan task-by-task.

**Goal:** Persist bounded, exact local-presubmit action evidence and admit only a complete passing plan whose candidate, base, command, profile, tool inventory, and resource identities all match.

**Architecture:** Extend the dispatcher-owned SQLite remote-gate store introduced by `oro-repf`. `RecordPresubmitResult` remains the idempotent write boundary; a new `PresubmitEvidencePlan` value is the pure expected identity used by `PresubmitPlanPassed` to load only exact evidence and apply completion-based pass policy.

**Tech Stack:** Go 1.26, `database/sql`, SQLite, table-driven Go tests.

## Global Constraints

- Preserve `func (s *Store) RecordPresubmitResult(ctx context.Context, result PresubmitResult) error`.
- Stale candidate/base/command/profile/tool/resource evidence never satisfies the current plan.
- Missing, skipped, cancelled, or failed actions never pass.
- Duplicate completion is idempotent and cannot overwrite the first terminal observation.
- Persist at most 64 KiB of logs per action.

---

### Task 1: Exact Presubmit Evidence Persistence and Eligibility

**Files:**

- Modify: `pkg/dispatcher/presubmit_test.go`
- Modify: `pkg/dispatcher/store.go`

**Interfaces:**

- Consumes: `NewStore(context.Context, *sql.DB) (*Store, error)`, `(*Store).AdoptCandidate`, `PresubmitAction`, and `PresubmitResult`.
- Produces: `type PresubmitEvidencePlan struct { GateID int64; CandidateSHA, BaseSHA, Profile, ToolHash string; Actions []PresubmitAction }` and `func (s *Store) PresubmitPlanPassed(context.Context, PresubmitEvidencePlan) (bool, error)`.

**Step 1: Write the failing test**

Add `TestPresubmitEvidenceIdentity` as a table-driven test. Each case adopts a fresh gate, writes one result per supplied action, and asserts `PresubmitPlanPassed`:

- exact passed evidence for every action returns true;
- one missing action returns false;
- stale candidate SHA, base SHA, command, profile, tool hash, or resource class returns false;
- `skipped`, `cancelled`, and `failed` exact evidence returns false;
- duplicate exact completion leaves one row and the original terminal observation unchanged;
- stored timestamps equal the supplied RFC3339Nano timestamps;
- stored logs are no larger than `maxPresubmitLogBytes`.

**Step 2: Run the test to verify RED**

Run: `go test ./pkg/dispatcher -run '^TestPresubmitEvidenceIdentity$' -count=1 -v`

Expected: build failure because `PresubmitEvidencePlan`, `PresubmitPlanPassed`, and `maxPresubmitLogBytes` do not exist.

**Step 3: Write the minimal implementation**

In `pkg/dispatcher/store.go`:

```go
const maxPresubmitLogBytes = 64 * 1024

type PresubmitEvidencePlan struct {
    GateID       int64
    CandidateSHA string
    BaseSHA      string
    Profile      string
    ToolHash     string
    Actions      []PresubmitAction
}
```

Before insertion, parse `StartedAt` and `CompletedAt` as RFC3339Nano, reject completion before start, and truncate `Logs` to `maxPresubmitLogBytes`. Keep the existing `ON CONFLICT ... DO NOTHING` key so duplicate completion remains immutable and idempotent.

Implement `PresubmitPlanPassed` by validating the expected plan, rejecting empty or duplicate action names, and querying each exact action identity including resource class. Return false for `sql.ErrNoRows`, any outcome other than `passed`, or invalid/reversed timestamps; wrap other database errors.

**Step 4: Run focused and package tests**

Run: `go test ./pkg/dispatcher -run '^TestPresubmitEvidenceIdentity$' -count=1 -v`

Expected: `=== RUN   TestPresubmitEvidenceIdentity` followed by PASS.

Run: `go test ./pkg/dispatcher -count=1`

Expected: PASS.

**Step 5: Format and commit**

Run `go tool gofumpt -w pkg/dispatcher/store.go pkg/dispatcher/presubmit_test.go`, then commit the plan, test, and implementation together with `feat(dispatcher): persist exact presubmit evidence`.

**Step 6: Run the full gate**

Run `./quality_gate.sh` and retain the explicit final summary. Do not infer success from the launcher returning before its detached lanes finish.
