package dispatcher //nolint:testpackage // white-box tests for checkEpicQG

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestEpicQGFailureClassifiedBeforeFixBeadCreation verifies that handleEpicQGFailure
// classifies QG output before deciding whether to create an epic fix task.
//
//   - deterministic: one targeted fix bead per (epic, fingerprint); no duplicates on repeat.
//   - systemic/flaky: infra incident recorded; no epic fix bead created; incident reused on repeat.
func TestEpicQGFailureClassifiedBeforeFixBeadCreation(t *testing.T) {
	// deterministicOut classifies as worker_deterministic (contains "--- fail:" and golangci-lint).
	const deterministicOut = "--- FAIL: TestFoo (0.12s)\n    testfoo_test.go:42: unexpected result\ngolangci-lint: unused variable\nFAIL\toro/pkg/foo\t0.12s"
	// systemicOut classifies as systemic (contains "cannot load stdlib").
	const systemicOut = "package loader failure: cannot load stdlib"
	// flakyOut classifies as flaky (contains "race detected").
	const flakyOut = "WARNING: DATA RACE\nrace detected in TestParallel\nrace detected under parallel load"

	setup := func(t *testing.T) (d *Dispatcher, beadSrc *fakeBeadStore) {
		t.Helper()
		d, beadSrc, _, _, _, _ = newTestDispatcher(t)
		ctx := context.Background()
		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}
		return d, beadSrc
	}

	t.Run("deterministic creates one fix bead; second call creates no duplicate", func(t *testing.T) {
		d, beadSrc := setup(t)
		ctx := context.Background()
		const epicID = "epic-cls-det"
		const epicBranch = protocol.EpicBranchPrefix + epicID

		beadSrc.mu.Lock()
		beadSrc.shown[epicID] = &protocol.BeadDetail{ID: epicID, Title: "Deterministic Epic"}
		beadSrc.mu.Unlock()

		result := d.handleEpicQGFailure(ctx, epicID, "worker-det", epicBranch, deterministicOut)
		if result {
			t.Error("handleEpicQGFailure must return false (epic stays open on QG failure)")
		}

		beadSrc.mu.Lock()
		firstCreated := append([]createCall(nil), beadSrc.created...)
		beadSrc.mu.Unlock()

		epicFixBeads := fixBeadsForEpic(firstCreated, epicID)
		if len(epicFixBeads) != 1 {
			t.Fatalf("expected 1 fix bead after first call, got %d: %v", len(epicFixBeads), firstCreated)
		}
		if epicFixBeads[0].beadType != "bug" {
			t.Errorf("fix bead type = %q, want bug", epicFixBeads[0].beadType)
		}
		if epicFixBeads[0].priority != 0 {
			t.Errorf("fix bead priority = %d, want 0", epicFixBeads[0].priority)
		}

		// Second call with identical output → same fingerprint → no duplicate bead.
		d.handleEpicQGFailure(ctx, epicID, "worker-det-2", epicBranch, deterministicOut)

		beadSrc.mu.Lock()
		secondCreated := append([]createCall(nil), beadSrc.created...)
		beadSrc.mu.Unlock()

		if got := len(fixBeadsForEpic(secondCreated, epicID)); got != 1 {
			t.Errorf("expected still 1 fix bead after second call, got %d", got)
		}
	})

	t.Run("systemic reuses infra incident; no epic fix bead created", func(t *testing.T) {
		d, beadSrc := setup(t)
		ctx := context.Background()
		const epicID = "epic-cls-sys"
		const epicBranch = protocol.EpicBranchPrefix + epicID

		beadSrc.mu.Lock()
		beadSrc.shown[epicID] = &protocol.BeadDetail{ID: epicID, Title: "Systemic Epic"}
		beadSrc.mu.Unlock()

		d.handleEpicQGFailure(ctx, epicID, "worker-sys-1", epicBranch, systemicOut)

		beadSrc.mu.Lock()
		afterFirst := append([]createCall(nil), beadSrc.created...)
		beadSrc.mu.Unlock()

		if got := len(fixBeadsForEpic(afterFirst, epicID)); got != 0 {
			t.Errorf("expected 0 epic fix beads after systemic failure, got %d", got)
		}

		// Incident must be recorded in DB.
		var incidentCount int
		if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qg_failure_incidents`).Scan(&incidentCount); err != nil {
			t.Fatalf("count incidents: %v", err)
		}
		if incidentCount == 0 {
			t.Error("expected at least one QG incident recorded for systemic failure")
		}

		// Second call: reuses same incident (count stays 1); still no fix bead.
		d.handleEpicQGFailure(ctx, epicID, "worker-sys-2", epicBranch, systemicOut)

		var incidentCountAfter int
		if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qg_failure_incidents`).Scan(&incidentCountAfter); err != nil {
			t.Fatalf("count incidents after second call: %v", err)
		}
		if incidentCountAfter != incidentCount {
			t.Errorf("expected incident count to stay %d (reused), got %d", incidentCount, incidentCountAfter)
		}

		beadSrc.mu.Lock()
		afterSecond := append([]createCall(nil), beadSrc.created...)
		beadSrc.mu.Unlock()

		if got := len(fixBeadsForEpic(afterSecond, epicID)); got != 0 {
			t.Errorf("expected still 0 epic fix beads after second systemic call, got %d", got)
		}
	})

	t.Run("flaky reuses infra incident; no epic fix bead created", func(t *testing.T) {
		d, beadSrc := setup(t)
		ctx := context.Background()
		const epicID = "epic-cls-flaky"
		const epicBranch = protocol.EpicBranchPrefix + epicID

		beadSrc.mu.Lock()
		beadSrc.shown[epicID] = &protocol.BeadDetail{ID: epicID, Title: "Flaky Epic"}
		beadSrc.mu.Unlock()

		d.handleEpicQGFailure(ctx, epicID, "worker-flaky-1", epicBranch, flakyOut)
		d.handleEpicQGFailure(ctx, epicID, "worker-flaky-2", epicBranch, flakyOut)

		beadSrc.mu.Lock()
		created := append([]createCall(nil), beadSrc.created...)
		beadSrc.mu.Unlock()

		if got := len(fixBeadsForEpic(created, epicID)); got != 0 {
			t.Errorf("expected 0 epic fix beads for flaky failure, got %d: %v", got, created)
		}
	})

	t.Run("impossible missing acceptance does not create another epic fix bead", func(t *testing.T) {
		d, beadSrc := setup(t)
		ctx := context.Background()
		const epicID = "epic-cls-impossible"
		const epicBranch = protocol.EpicBranchPrefix + epicID
		const impossibleOut = "missing acceptance criteria: no Cmd field for child oro-no-ac"

		beadSrc.mu.Lock()
		beadSrc.shown[epicID] = &protocol.BeadDetail{ID: epicID, Title: "Impossible Epic"}
		beadSrc.mu.Unlock()

		result := d.handleEpicQGFailure(ctx, epicID, "worker-impossible", epicBranch, impossibleOut)
		if result {
			t.Error("handleEpicQGFailure must return false (epic stays open on impossible QG failure)")
		}

		beadSrc.mu.Lock()
		created := append([]createCall(nil), beadSrc.created...)
		beadSrc.mu.Unlock()

		if got := len(fixBeadsForEpic(created, epicID)); got != 0 {
			t.Fatalf("expected 0 epic fix beads for impossible missing-AC failure, got %d: %v", got, created)
		}
	})
}

// fixBeadsForEpic returns createCalls that have parent == epicID.
func fixBeadsForEpic(created []createCall, epicID string) []createCall {
	var out []createCall
	for _, c := range created {
		if c.parent == epicID {
			out = append(out, c)
		}
	}
	return out
}

// epicQGTestSetup builds a dispatcher ready for tryCloseEpic tests.
// All children are closed, the acceptance runner always passes, and the
// epic branch exists. Individual tests override createFn / qgRunner as needed.
func epicQGTestSetup(t *testing.T, epicID string) (*Dispatcher, *fakeBeadStore, *mockWorktreeManager) {
	t.Helper()
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	epicBranch := protocol.EpicBranchPrefix + epicID
	beadSrc.allChildrenClosedMap = map[string]bool{epicID: true}
	beadSrc.mu.Lock()
	beadSrc.shown[epicID] = &protocol.BeadDetail{
		ID:                 epicID,
		Title:              "My Epic",
		AcceptanceCriteria: "Test: pkg/... | Cmd: go test ./... | Assert: PASS",
	}
	beadSrc.mu.Unlock()

	d.acceptance = &mockAcceptanceRunner{passed: true}

	wtMgr.mu.Lock()
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == epicBranch, nil
	}
	wtMgr.createFn = func(_ context.Context, beadID, baseBranch string) (string, string, error) {
		return "/tmp/worktree-" + beadID, "agent/" + beadID, nil
	}
	wtMgr.mu.Unlock()

	return d, beadSrc, wtMgr
}

func TestTryCloseEpicFallsBackWhenEpicDetailFetchFails(t *testing.T) {
	for _, tc := range []struct {
		name     string
		showErr  error
		shownNil bool
	}{
		{name: "show error", showErr: fmt.Errorf("show failed")},
		{name: "nil detail", shownNil: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const epicID = "epic-fetch-fallback"
			const workerID = "w-fetch-fallback"

			d, beadSrc, _ := epicQGTestSetup(t, epicID)
			runner := &mockAcceptanceRunner{passed: true}
			d.acceptance = runner
			beadSrc.mu.Lock()
			beadSrc.showErr = tc.showErr
			beadSrc.shownNil = map[string]bool{epicID: tc.shownNil}
			beadSrc.mu.Unlock()

			d.tryCloseEpic(context.Background(), epicID, workerID)

			beadSrc.mu.Lock()
			closed := append([]string(nil), beadSrc.closed...)
			beadSrc.mu.Unlock()
			if !slices.Contains(closed, epicID) {
				t.Fatalf("closed beads = %v, want %q", closed, epicID)
			}
			runner.mu.Lock()
			calls := runner.calls
			runner.mu.Unlock()
			if calls != 0 {
				t.Fatalf("acceptance calls = %d, want 0 when epic detail fetch fails", calls)
			}
		})
	}
}

// TestEpicQGErrorCreatesOrReusesIncident verifies that when the QG runner
// returns an error during epic auto-close, an infra incident is recorded in
// the database and no direct child fix bead is created for the epic.
func TestEpicQGErrorCreatesOrReusesIncident(t *testing.T) {
	const epicID = "epic-qg-inc-err"
	const workerID = "w-qg-inc-err"

	d, beadSrc, _ := epicQGTestSetup(t, epicID)
	ctx := context.Background()

	// QG runner returns a systemic error.
	d.qgRunner = &mockQGRunner{err: fmt.Errorf("quality_gate.sh: script not found")}

	d.tryCloseEpic(ctx, epicID, workerID)

	// Assert: an infra incident was recorded in the database.
	var incidentCount int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qg_failure_incidents`).Scan(&incidentCount); err != nil {
		t.Fatalf("query qg_failure_incidents: %v", err)
	}
	if incidentCount == 0 {
		t.Error("expected infra incident to be created in qg_failure_incidents, got 0")
	}

	// Assert: no direct fix bead was created with epicID as parent.
	beadSrc.mu.Lock()
	created := append([]createCall(nil), beadSrc.created...)
	beadSrc.mu.Unlock()
	for _, cc := range created {
		if cc.parent == epicID {
			t.Errorf("unexpected direct fix bead with parent=%q: title=%q type=%q", epicID, cc.title, cc.beadType)
		}
	}
}

// TestEpicQGWorktreeCreateFailureCreatesOrReusesIncident verifies that when
// the QG worktree cannot be created, an infra incident is recorded and no
// direct child fix bead is spawned for the epic.
func TestEpicQGWorktreeCreateFailureCreatesOrReusesIncident(t *testing.T) {
	const epicID = "epic-qg-inc-wt"
	const workerID = "w-qg-inc-wt"

	d, beadSrc, wtMgr := epicQGTestSetup(t, epicID)
	ctx := context.Background()

	// Override: QG worktree creation fails.
	wtMgr.mu.Lock()
	wtMgr.createFn = func(_ context.Context, beadID, baseBranch string) (string, string, error) {
		if strings.HasPrefix(beadID, epicID+"-qg-") {
			return "", "", fmt.Errorf("out of memory: cannot create worktree")
		}
		return "/tmp/worktree-" + beadID, "agent/" + beadID, nil
	}
	wtMgr.mu.Unlock()

	d.tryCloseEpic(ctx, epicID, workerID)

	// Assert: an infra incident was recorded in the database.
	var incidentCount int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qg_failure_incidents`).Scan(&incidentCount); err != nil {
		t.Fatalf("query qg_failure_incidents: %v", err)
	}
	if incidentCount == 0 {
		t.Error("expected infra incident to be created in qg_failure_incidents, got 0")
	}

	// Assert: no direct fix bead was created with epicID as parent.
	beadSrc.mu.Lock()
	created := append([]createCall(nil), beadSrc.created...)
	beadSrc.mu.Unlock()
	for _, cc := range created {
		if cc.parent == epicID {
			t.Errorf("unexpected direct fix bead with parent=%q: title=%q type=%q", epicID, cc.title, cc.beadType)
		}
	}
}

// TestEpicQGPassesThenMerges covers all assertion points from the acceptance criteria:
//
//  1. checkEpicQG creates a unique temp worktree via Create(epicID+"-qg-*", epicBranch), runs
//     qgRunner.Run without the legacy skipMutation override, removes worktree on completion.
//  2. QG passes  → returns true, tryCloseEpic proceeds to completeEpicClose.
//  3. QG fails   → logs epic_qg_failed, creates fix bead, returns false (no close).
//  4. QG error   → logs epic_qg_error, escalates, returns false.
//  5. Worktree create fails → logs, returns false.
func TestEpicQGPassesThenMerges(t *testing.T) {
	// --- helper: build a dispatcher whose tryCloseEpic path reaches checkEpicQG.
	//
	// We go through tryCloseEpic (not checkEpicQG directly) so the integration
	// path is covered: acceptance test passes → checkEpicQG → completeEpicClose.
	setupEpicQGDispatcher := func(t *testing.T, epicID string) (
		d *Dispatcher,
		beadSrc *fakeBeadStore,
		wtMgr *mockWorktreeManager,
		esc *mockEscalator,
	) {
		t.Helper()
		d, beadSrc, wtMgr, esc, _, _ = newTestDispatcher(t)
		ctx := context.Background()
		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicBranch := protocol.EpicBranchPrefix + epicID

		// Epic: all children closed, acceptance cmd present.
		beadSrc.allChildrenClosedMap = map[string]bool{epicID: true}
		beadSrc.mu.Lock()
		beadSrc.shown[epicID] = &protocol.BeadDetail{
			ID:                 epicID,
			Title:              "My Epic",
			AcceptanceCriteria: "Test: pkg/... | Cmd: go test ./... | Assert: PASS",
		}
		beadSrc.mu.Unlock()

		// Acceptance test always passes so we reach checkEpicQG.
		d.acceptance = &mockAcceptanceRunner{passed: true}

		// Epic branch exists for all tests; individual tests override createFn.
		wtMgr.mu.Lock()
		wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
			return branch == epicBranch, nil
		}
		wtMgr.mu.Unlock()

		return d, beadSrc, wtMgr, esc
	}

	t.Run("QG pass - creates worktree, runs QG, removes worktree, epic closed", func(t *testing.T) {
		const epicID = "epic-qg-pass"
		const workerID = "w-qg-pass"
		epicBranch := protocol.EpicBranchPrefix + epicID
		qgWorktreePath := "/tmp/worktree-" + epicID + "-qg"

		d, beadSrc, wtMgr, _ := setupEpicQGDispatcher(t, epicID)
		ctx := context.Background()

		// Capture Create calls so we can assert beadID and baseBranch.
		// createsMu is separate from wtMgr.mu — Create() holds wtMgr.mu when
		// calling createFn, so we must not re-acquire it inside the closure.
		type createArgs struct{ beadID, baseBranch string }
		var createsMu sync.Mutex
		var creates []createArgs
		wtMgr.mu.Lock()
		wtMgr.createFn = func(_ context.Context, beadID, baseBranch string) (string, string, error) {
			createsMu.Lock()
			creates = append(creates, createArgs{beadID, baseBranch})
			createsMu.Unlock()
			// Return qgWorktreePath only for the QG worktree; use default for others.
			if strings.HasPrefix(beadID, epicID+"-qg-") {
				return qgWorktreePath, "agent/" + beadID, nil
			}
			return "/tmp/worktree-" + beadID, "agent/" + beadID, nil
		}
		wtMgr.mu.Unlock()

		// Passing QG runner.
		qgRunner := &mockQGRunner{passed: true, output: "all green"}
		d.qgRunner = qgRunner

		d.tryCloseEpic(ctx, epicID, workerID)

		// Assert (1): worktree created with a unique epic QG ID and epicBranch as base.
		var qgCreate *createArgs
		createsMu.Lock()
		for i := range creates {
			if strings.HasPrefix(creates[i].beadID, epicID+"-qg-") {
				c := creates[i]
				qgCreate = &c
				break
			}
		}
		createsMu.Unlock()
		if qgCreate == nil {
			t.Fatalf("expected worktrees.Create called with beadID prefix %q, got creates=%v", epicID+"-qg-", creates)
		}
		if qgCreate.beadID == epicID+"-qg" {
			t.Fatalf("QG worktree ID must be unique, got fixed ID %q", qgCreate.beadID)
		}
		if qgCreate.baseBranch != epicBranch {
			t.Errorf("Create baseBranch = %q, want %q", qgCreate.baseBranch, epicBranch)
		}

		// Assert (1): QG was run on the QG worktree path.
		qgRunner.mu.Lock()
		qgCalls := append([]string(nil), qgRunner.calls...)
		skipMutations := append([]bool(nil), qgRunner.skipMutations...)
		qgRunner.mu.Unlock()
		foundQGCall := false
		for i, c := range qgCalls {
			if c == qgWorktreePath {
				foundQGCall = true
				if skipMutations[i] {
					t.Error("epic local QG should use local context without ORO_SKIP_MUTATION; mutation is deferred by quality_gate.sh itself")
				}
				break
			}
		}
		if !foundQGCall {
			t.Errorf("qgRunner.Run not called with worktree=%q; calls=%v", qgWorktreePath, qgCalls)
		}

		// Assert (1): worktree removed after completion.
		wtMgr.mu.Lock()
		removed := append([]string(nil), wtMgr.removed...)
		wtMgr.mu.Unlock()
		foundRemoved := false
		for _, r := range removed {
			if r == qgWorktreePath {
				foundRemoved = true
				break
			}
		}
		if !foundRemoved {
			t.Errorf("expected QG worktree %q to be removed; removed=%v", qgWorktreePath, removed)
		}

		// Assert (2): QG pass → completeEpicClose → epic closed.
		waitFor(t, func() bool {
			beadSrc.mu.Lock()
			defer beadSrc.mu.Unlock()
			for _, id := range beadSrc.closed {
				if id == epicID {
					return true
				}
			}
			return false
		}, 2*time.Second)

		beadSrc.mu.Lock()
		epicClosed := false
		for _, id := range beadSrc.closed {
			if id == epicID {
				epicClosed = true
				break
			}
		}
		beadSrc.mu.Unlock()
		if !epicClosed {
			t.Error("expected epic to be closed after QG pass")
		}
	})

	t.Run("QG fail - logs epic_qg_failed, creates fix bead, epic not closed", func(t *testing.T) {
		const epicID = "epic-qg-fail"
		const workerID = "w-qg-fail"

		d, beadSrc, wtMgr, _ := setupEpicQGDispatcher(t, epicID)
		ctx := context.Background()

		// Default createFn returns a path.
		wtMgr.mu.Lock()
		origBranchExistsFn := wtMgr.branchExistsFn
		wtMgr.createFn = func(_ context.Context, beadID, baseBranch string) (string, string, error) {
			return "/tmp/worktree-" + beadID, "agent/" + beadID, nil
		}
		wtMgr.branchExistsFn = origBranchExistsFn
		wtMgr.mu.Unlock()

		// Failing QG.
		const qgOut = "tests failed: panic in TestFoo"
		qgRunner := &mockQGRunner{passed: false, output: qgOut}
		d.qgRunner = qgRunner

		d.tryCloseEpic(ctx, epicID, workerID)

		// Assert (3): epic_qg_failed event logged.
		count := eventCount(t, d.db, "epic_qg_failed")
		if count == 0 {
			t.Error("expected epic_qg_failed event to be logged")
		}

		// Assert (3): fix bead created with QG output in description.
		beadSrc.mu.Lock()
		created := append([]createCall(nil), beadSrc.created...)
		beadSrc.mu.Unlock()
		var fixBead *createCall
		for i := range created {
			if strings.Contains(created[i].description, qgOut) {
				fixBead = &created[i]
				break
			}
		}
		if fixBead == nil {
			t.Errorf("expected fix bead created with QG output in description; got created=%v", created)
		}
		if fixBead != nil && fixBead.parent != epicID {
			t.Errorf("fix bead parent = %q, want %q", fixBead.parent, epicID)
		}
		if fixBead != nil && fixBead.beadType != "bug" {
			t.Errorf("fix bead type = %q, want bug", fixBead.beadType)
		}
		if fixBead != nil && fixBead.priority != 0 {
			t.Errorf("fix bead priority = %d, want 0", fixBead.priority)
		}
		if fixBead != nil && !strings.Contains(fixBead.acceptanceCriteria, "Test:") {
			t.Errorf("fix bead acceptance criteria missing Test: %q", fixBead.acceptanceCriteria)
		}
		if fixBead != nil && !strings.Contains(fixBead.acceptanceCriteria, "./scripts/quality_gate.sh") {
			t.Errorf("fix bead acceptance criteria missing quality gate command: %q", fixBead.acceptanceCriteria)
		}

		// Assert (3): epic NOT closed.
		beadSrc.mu.Lock()
		epicClosed := false
		for _, id := range beadSrc.closed {
			if id == epicID {
				epicClosed = true
				break
			}
		}
		beadSrc.mu.Unlock()
		if epicClosed {
			t.Error("expected epic NOT to be closed after QG fail")
		}
	})

	t.Run("QG error - logs epic_qg_error, escalates, epic not closed", func(t *testing.T) {
		const epicID = "epic-qg-err"
		const workerID = "w-qg-err"

		d, beadSrc, wtMgr, esc := setupEpicQGDispatcher(t, epicID)
		ctx := context.Background()

		wtMgr.mu.Lock()
		origBranchExistsFn := wtMgr.branchExistsFn
		wtMgr.createFn = func(_ context.Context, beadID, baseBranch string) (string, string, error) {
			return "/tmp/worktree-" + beadID, "agent/" + beadID, nil
		}
		wtMgr.branchExistsFn = origBranchExistsFn
		wtMgr.mu.Unlock()

		// QG returns an error.
		qgRunner := &mockQGRunner{err: fmt.Errorf("quality gate script not found")}
		d.qgRunner = qgRunner

		d.tryCloseEpic(ctx, epicID, workerID)

		// Assert (4): epic_qg_error event logged.
		count := eventCount(t, d.db, "epic_qg_error")
		if count == 0 {
			t.Error("expected epic_qg_error event to be logged")
		}

		// Assert (4): escalation sent.
		msgs := esc.Messages()
		found := false
		for _, m := range msgs {
			if strings.Contains(m, epicID) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected escalation message containing %q; got %v", epicID, msgs)
		}

		// Assert (4): epic NOT closed.
		beadSrc.mu.Lock()
		epicClosed := false
		for _, id := range beadSrc.closed {
			if id == epicID {
				epicClosed = true
				break
			}
		}
		beadSrc.mu.Unlock()
		if epicClosed {
			t.Error("expected epic NOT to be closed after QG error")
		}
	})

	t.Run("worktree create fails - logs, returns false, epic not closed", func(t *testing.T) {
		const epicID = "epic-qg-wt-fail"
		const workerID = "w-qg-wt-fail"

		d, beadSrc, wtMgr, _ := setupEpicQGDispatcher(t, epicID)
		ctx := context.Background()

		// createFn fails for the QG worktree.
		wtMgr.mu.Lock()
		origBranchExistsFn := wtMgr.branchExistsFn
		wtMgr.createFn = func(_ context.Context, beadID, baseBranch string) (string, string, error) {
			if strings.HasPrefix(beadID, epicID+"-qg-") {
				return "", "", fmt.Errorf("failed to create worktree: disk full")
			}
			return "/tmp/worktree-" + beadID, "agent/" + beadID, nil
		}
		wtMgr.branchExistsFn = origBranchExistsFn
		wtMgr.mu.Unlock()

		d.tryCloseEpic(ctx, epicID, workerID)

		// Assert (5): some event logged for worktree failure.
		count := eventCount(t, d.db, "epic_qg_worktree_failed")
		if count == 0 {
			t.Error("expected epic_qg_worktree_failed event to be logged")
		}

		// Assert (5): epic NOT closed.
		beadSrc.mu.Lock()
		epicClosed := false
		for _, id := range beadSrc.closed {
			if id == epicID {
				epicClosed = true
				break
			}
		}
		beadSrc.mu.Unlock()
		if epicClosed {
			t.Error("expected epic NOT to be closed when worktree creation fails")
		}
	})
}
