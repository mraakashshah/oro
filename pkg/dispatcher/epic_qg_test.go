package dispatcher //nolint:testpackage // white-box tests for checkEpicQG

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestEpicQGPassesThenMerges covers all assertion points from the acceptance criteria:
//
//  1. checkEpicQG creates temp worktree via Create(epicID+"-qg", epicBranch), runs
//     qgRunner.Run with skipMutation=false, removes worktree on completion.
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
		beadSrc *mockBeadSource,
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
			if strings.HasSuffix(beadID, "-qg") {
				return qgWorktreePath, "agent/" + beadID, nil
			}
			return "/tmp/worktree-" + beadID, "agent/" + beadID, nil
		}
		wtMgr.mu.Unlock()

		// Passing QG runner.
		qgRunner := &mockQGRunner{passed: true, output: "all green"}
		d.qgRunner = qgRunner

		d.tryCloseEpic(ctx, epicID, workerID)

		// Assert (1): worktree created with epicID+"-qg" and epicBranch as base.
		var qgCreate *createArgs
		createsMu.Lock()
		for i := range creates {
			if creates[i].beadID == epicID+"-qg" {
				c := creates[i]
				qgCreate = &c
				break
			}
		}
		createsMu.Unlock()
		if qgCreate == nil {
			t.Fatalf("expected worktrees.Create called with beadID=%q, got creates=%v", epicID+"-qg", creates)
		}
		if qgCreate.baseBranch != epicBranch {
			t.Errorf("Create baseBranch = %q, want %q", qgCreate.baseBranch, epicBranch)
		}

		// Assert (1): QG was run on the QG worktree path.
		qgRunner.mu.Lock()
		qgCalls := append([]string(nil), qgRunner.calls...)
		qgRunner.mu.Unlock()
		foundQGCall := false
		for _, c := range qgCalls {
			if c == qgWorktreePath {
				foundQGCall = true
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
			if strings.HasSuffix(beadID, "-qg") {
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
