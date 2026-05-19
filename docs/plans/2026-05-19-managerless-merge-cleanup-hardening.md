# Managerless Merge Cleanup Hardening Implementation Plan

> **For Claude:** Use executing-plans skill to implement this plan task-by-task.

**Goal:** Prevent managerless successful/no-op merges into epic branches from reopening work, creating avoidable recovery quarantines, or spawning failed ops runs.
**Architecture:** Treat merge cleanup as target-aware: a branch is safely cleanable after it is proven merged into the branch it actually targeted, not only after it reaches `main`. Treat no-op merge results as successful terminal outcomes when the target already contains the branch tip.
**Tech Stack:** Go dispatcher, Git worktree manager, Oro native beadstore, existing dispatcher tests.

---

## Task 1: Close Proven No-Op Merges

**Files:**
- Modify: `pkg/dispatcher/dispatcher.go`
- Test: `pkg/dispatcher/dispatcher_test.go`

**Step 1: Write the failing test**
Add or replace the existing no-op merge test with:

```go
func TestMergeAndCompleteNoopMergeClosesBeadWithoutEscalation(t *testing.T) {
	d, beadSrc, _, esc, gitRunner, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	beadID := "bead-noop-merge"
	workerID := "w-noop"
	worktree := "/tmp/worktree-" + beadID
	branch := "agent/" + beadID
	targetBranch := protocol.EpicBranchPrefix + "epic-noop"

	gitRunner.mu.Lock()
	gitRunner.revListCount = "0"
	gitRunner.mu.Unlock()

	d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "epic-noop", targetBranch, 0)

	beadSrc.mu.Lock()
	closed := append([]string(nil), beadSrc.closed...)
	status := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()

	if !slices.Contains(closed, beadID) {
		t.Fatalf("no-op merge must close bead %q; closed=%v", beadID, closed)
	}
	if status == "open" {
		t.Fatal("no-op merge reopened bead; want terminal close")
	}
	if len(esc.Messages()) != 0 {
		t.Fatalf("no-op merge must not escalate, got messages: %v", esc.Messages())
	}
}
```

**Step 2: Run test to verify it fails**
Run:

```bash
go test ./pkg/dispatcher -run TestMergeAndCompleteNoopMergeClosesBeadWithoutEscalation -count=1 -v
```

Expected: FAIL because current `handleNoopMerge` reopens the bead and escalates.

**Step 3: Implement minimal code**
Change `handleNoopMerge` to:
- complete the assignment,
- call `CloseBead(ctx, beadID, fmt.Sprintf("No-op merge: %s already contains %s at %s", target, branch, sha))`,
- log `merge_noop`,
- release the worker,
- clear ops agents for the bead,
- call `autoCloseEpicIfComplete`,
- remove worktree/tracking using the target-aware cleanup from Task 2 when available; until Task 2 lands, use the existing cleanup path.

Do not call `updateBeadStatus(..., "open")`.
Do not call `escalate`.

**Step 4: Run test to verify it passes**
Run the command from Step 2. Expected: PASS.

---

## Task 2: Delete Branches Against Their Actual Merge Target

**Files:**
- Modify: `pkg/dispatcher/dispatcher.go`
- Modify: `pkg/dispatcher/worktree_manager.go`
- Test: `pkg/dispatcher/dispatcher_test.go`
- Test: `pkg/dispatcher/worktree_manager_test.go`

**Step 1: Write failing worktree-manager test**
Add:

```go
func TestDeleteBranchMergedIntoUsesTargetProofBeforeSafeDelete(t *testing.T) {
	runner := &recordingRunner{}
	mgr := &GitWorktreeManager{repoRoot: "/repo", runner: runner}

	if err := mgr.DeleteBranchMergedInto(context.Background(), "agent/oro-child", "epic/oro-parent"); err != nil {
		t.Fatalf("DeleteBranchMergedInto: %v", err)
	}

	want := [][]string{
		{"git", "-C", "/repo", "merge-base", "--is-ancestor", "agent/oro-child", "epic/oro-parent"},
		{"git", "-C", "/repo", "branch", "-d", "agent/oro-child"},
	}
	if !reflect.DeepEqual(runner.callsArgs(), want) {
		t.Fatalf("calls = %#v, want %#v", runner.callsArgs(), want)
	}
}
```

**Step 2: Write failing dispatcher cleanup test**
Add:

```go
func TestRemoveWorktreeAndClearTrackingDeletesBranchMergedIntoTarget(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)

	d.removeWorktreeAndClearTracking(context.Background(), "oro-child", "w1", "/tmp/worktree-oro-child", "epic/oro-parent")

	wtMgr.mu.Lock()
	defer wtMgr.mu.Unlock()

	if len(wtMgr.deletedBranchesInto) != 1 {
		t.Fatalf("target-aware delete calls = %v, want one", wtMgr.deletedBranchesInto)
	}
	call := wtMgr.deletedBranchesInto[0]
	if call.branch != "agent/oro-child" || call.target != "epic/oro-parent" {
		t.Fatalf("target-aware delete = %+v, want agent/oro-child into epic/oro-parent", call)
	}
}
```

**Step 3: Run tests to verify they fail**
Run:

```bash
go test ./pkg/dispatcher -run 'TestDeleteBranchMergedIntoUsesTargetProofBeforeSafeDelete|TestRemoveWorktreeAndClearTrackingDeletesBranchMergedIntoTarget' -count=1 -v
```

Expected: FAIL because `DeleteBranchMergedInto` and target-aware cleanup do not exist.

**Step 4: Implement minimal code**
- Extend `WorktreeManager` with:

```go
DeleteBranchMergedInto(ctx context.Context, branch, targetBranch string) error
```

- Implement `GitWorktreeManager.DeleteBranchMergedInto`:
  1. Run `git -C repoRoot merge-base --is-ancestor <branch> <targetBranch>`.
  2. If proof fails, return `fmt.Errorf("branch %s is not merged into %s: %w", branch, targetBranch, err)`.
  3. Run `git -C repoRoot branch -d <branch>`.

- Update `mockWorktreeManager` to record target-aware calls.
- Change `removeWorktreeAndClearTracking` signature to include `targetBranch string`.
- Use `d.cfg.DefaultBranch` when target is empty.
- Call `DeleteBranchMergedInto(ctx, branch, target)` instead of `DeleteBranch`.
- Update all callers.
- Keep `DeleteBranch` unchanged for default safe deletion callers.

**Step 5: Run tests to verify they pass**
Run the command from Step 3. Expected: PASS.

---

## Task 3: Suppress Informational Managerless Ops Noise

**Files:**
- Modify: `pkg/dispatcher/dispatcher.go`
- Test: `pkg/dispatcher/dispatcher_test.go`

**Step 1: Write failing test**
Add:

```go
func TestMergeCompleteDoesNotFailEscalationWhenManagerMissing(t *testing.T) {
	d, _, _, esc, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	esc.fail = true

	d.mergeAndComplete(ctx, "bead-managerless", "w1", "/tmp/wt-managerless", "agent/bead-managerless", "", "", 0)

	var failed int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE type='escalation_failed' AND bead_id='bead-managerless'`).Scan(&failed); err != nil {
		t.Fatalf("query escalation_failed: %v", err)
	}
	if failed != 0 {
		t.Fatalf("MERGE_COMPLETE managerless notification logged escalation_failed = %d, want 0", failed)
	}
}
```

**Step 2: Run test to verify it fails**
Run:

```bash
go test ./pkg/dispatcher -run TestMergeCompleteDoesNotFailEscalationWhenManagerMissing -count=1 -v
```

Expected: FAIL because `escalate` currently logs `escalation_failed` for managerless informational `MERGE_COMPLETE` delivery failures.

**Step 3: Implement minimal code**
- Add helper:

```go
func isInformationalEscalation(escType protocol.EscalationType) bool {
	return escType == protocol.EscMergeComplete || escType == protocol.EscManualIntegration
}
```

- In `escalate`, when `d.escalator.Escalate` fails for an informational escalation:
  - log a non-error event such as `notification_skipped`,
  - do not log `escalation_failed`,
  - do not create or route an ops run.
- Preserve current behavior for actionable escalations.

**Step 4: Run focused tests**
Run:

```bash
go test ./pkg/dispatcher -run 'TestMergeCompleteDoesNotFailEscalationWhenManagerMissing|TestMergeAndCompleteEscalatesMergeComplete|TestMergeCompleteEscalationAutoAcked' -count=1 -v
```

Expected: PASS.

---

## Final Verification

Run:

```bash
go test ./pkg/dispatcher -run 'TestMergeAndCompleteNoopMergeClosesBeadWithoutEscalation|TestDeleteBranchMergedIntoUsesTargetProofBeforeSafeDelete|TestRemoveWorktreeAndClearTrackingDeletesBranchMergedIntoTarget|TestMergeCompleteDoesNotFailEscalationWhenManagerMissing|TestMergeAndCompleteEscalatesMergeComplete|TestMergeCompleteEscalationAutoAcked' -count=1
go test ./pkg/dispatcher/... -count=1 -timeout 180s
./scripts/quality_gate.sh
make install
ORO_HUMAN_CONFIRMED=1 /Users/as21/go/bin/oro stop --force
/Users/as21/go/bin/oro start --workers 2 --max-workers 2 --detach
/Users/as21/go/bin/oro health --json
/Users/as21/go/bin/oro status --json
```

Expected:
- Focused tests pass.
- Dispatcher package tests pass.
- Quality gate passes.
- Installed binary reports the new version.
- Restarted factory is healthy, idle, and has no recovery quarantines or failed ops runs.
