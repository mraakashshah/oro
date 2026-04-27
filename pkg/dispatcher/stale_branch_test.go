package dispatcher //nolint:testpackage // white-box tests need access to unexported dispatcher methods

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestDeleteStaleAgentBranch_BranchCheckedOut_RemovesWorktreeBeforeDelete verifies that
// when git refuses to delete a branch because it is checked out in a worktree,
// deleteStaleAgentBranch removes the worktree at the expected path and retries the delete.
func TestDeleteStaleAgentBranch_BranchCheckedOut_RemovesWorktreeBeforeDelete(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)
	d.repoRoot = "/repo/root"

	beadID := "oro-stale"
	expectedWorktreePath := filepath.Join(d.repoRoot, ".worktrees", beadID)

	var mu sync.Mutex
	var ops []string // records "remove:<path>" and "delete:<branch>" in call order

	// BranchExists: branch is present.
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == protocol.BranchPrefix+beadID, nil
	}

	deleteCalls := 0
	wtMgr.deleteBranchFn = func(branch string) error {
		mu.Lock()
		deleteCalls++
		call := deleteCalls
		mu.Unlock()
		if call == 1 {
			// First call: branch is checked out — git refuses.
			mu.Lock()
			ops = append(ops, "delete:"+branch)
			mu.Unlock()
			return fmt.Errorf("error: Cannot delete branch '%s' checked out at '%s'",
				branch, expectedWorktreePath)
		}
		// Second call: branch is no longer checked out after Remove.
		mu.Lock()
		ops = append(ops, "delete:"+branch)
		mu.Unlock()
		return nil
	}

	wtMgr.removeFn = func(_ context.Context, path string) error {
		mu.Lock()
		ops = append(ops, "remove:"+path)
		mu.Unlock()
		return nil
	}

	if err := d.deleteStaleAgentBranch(context.Background(), beadID, "w-1"); err != nil {
		t.Fatalf("deleteStaleAgentBranch should succeed after remove+retry, got: %v", err)
	}

	mu.Lock()
	snapshot := make([]string, len(ops))
	copy(snapshot, ops)
	mu.Unlock()

	// Expect: first delete (fails) → remove → second delete (succeeds).
	if len(snapshot) < 3 {
		t.Fatalf("expected at least 3 ops, got %v", snapshot)
	}
	wantOps := []string{
		"delete:" + protocol.BranchPrefix + beadID,
		"remove:" + expectedWorktreePath,
		"delete:" + protocol.BranchPrefix + beadID,
	}
	for i, want := range wantOps {
		if snapshot[i] != want {
			t.Errorf("ops[%d]: got %q, want %q", i, snapshot[i], want)
		}
	}
}

// TestDeleteStaleAgentBranch_BranchCheckedOut_LogsStaleBranchCleanupFailed verifies that
// when the worktree removal fails (or the branch delete still fails after removal),
// stale_branch_cleanup_failed is written to the event log.
func TestDeleteStaleAgentBranch_BranchCheckedOut_LogsStaleBranchCleanupFailed(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)
	d.repoRoot = "/repo/root"

	beadID := "oro-stuck"

	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == protocol.BranchPrefix+beadID, nil
	}

	// DeleteBranch always fails with "checked out".
	wtMgr.deleteBranchFn = func(branch string) error {
		return fmt.Errorf("error: Cannot delete branch '%s' checked out at '/some/path'", branch)
	}

	// Remove also fails.
	wtMgr.removeFn = func(_ context.Context, _ string) error {
		return errors.New("worktree remove: permission denied")
	}

	err := d.deleteStaleAgentBranch(context.Background(), beadID, "w-1")
	if err == nil {
		t.Fatal("expected error when cleanup fails, got nil")
	}

	if eventCount(t, d.db, "stale_branch_cleanup_failed") == 0 {
		t.Error("expected stale_branch_cleanup_failed event to be logged")
	}
}

// TestDeleteStaleAgentBranch_BranchCheckedOut_AbortsAssignmentOnCleanupFailure verifies that
// when stale branch cleanup fails the dispatcher does not create a new worktree (no stale QG context).
func TestDeleteStaleAgentBranch_BranchCheckedOut_AbortsAssignmentOnCleanupFailure(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	d.repoRoot = "/repo/root"
	d.cfg.HeartbeatTimeout = 100 * time.Millisecond

	beadID := "oro-abort"

	// BranchExists: stale branch is present.
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == protocol.BranchPrefix+beadID, nil
	}

	// DeleteBranch always fails with "checked out".
	wtMgr.deleteBranchFn = func(branch string) error {
		return fmt.Errorf("error: Cannot delete branch '%s' checked out at '/stale/path'", branch)
	}

	// Remove also fails — cleanup is impossible.
	wtMgr.removeFn = func(_ context.Context, _ string) error {
		return errors.New("permission denied")
	}

	var createCalled bool
	wtMgr.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		createCalled = true
		return "/tmp/wt-" + bID, "agent/" + bID, nil
	}

	// isBranchMerged must return non-zero so the bead isn't skipped.
	d.shutdownRunner = &mockCommandRunner{err: fmt.Errorf("exit status 1")}

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, time.Second)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-abort", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, time.Second)

	beadSrc.SetBeads([]protocol.Bead{{
		ID:       beadID,
		Priority: 1,
	}})

	// No ASSIGN should arrive since cleanup failed.
	_, ok := readMsg(t, conn, 500*time.Millisecond)
	if ok {
		if !createCalled {
			// Got a message but Create was never called — unexpected.
			t.Fatal("received unexpected message from dispatcher when cleanup should have aborted assignment")
		}
		t.Fatal("Create was called despite stale branch cleanup failure — stale QG context not prevented")
	}

	if createCalled {
		t.Error("Create was called even though stale branch cleanup failed")
	}
}

// TestWorktreeManager_PruneStale_RemovesWorktreeBeforeBranchDelete verifies the command
// sequence inside pruneStale: worktree remove → worktree prune → branch -D.
// This is the order that prevents "Cannot delete branch checked out" errors.
func TestWorktreeManager_PruneStale_RemovesWorktreeBeforeBranchDelete(t *testing.T) {
	var ops []string

	runner := &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			// Identify which git sub-command this is.
			for i, a := range args {
				if a == "worktree" && i+1 < len(args) {
					ops = append(ops, "worktree:"+args[i+1])
					return nil, nil
				}
				if a == "branch" && i+1 < len(args) {
					ops = append(ops, "branch:"+args[i+1])
					return nil, nil
				}
			}
			return nil, nil
		},
	}

	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)
	err := mgr.pruneStale(context.Background(), "/repo/root/.worktrees/oro-x", "agent/oro-x")
	if err != nil {
		t.Fatalf("pruneStale: %v", err)
	}

	// Expected: worktree:remove → worktree:prune → branch:-D
	if len(ops) < 3 {
		t.Fatalf("expected 3 ops, got %v", ops)
	}
	wantOps := []string{"worktree:remove", "worktree:prune", "branch:-D"}
	for i, want := range wantOps {
		if ops[i] != want {
			t.Errorf("ops[%d]: got %q, want %q", i, ops[i], want)
		}
	}
	// remove MUST come before -D.
	removeIdx := -1
	deleteIdx := -1
	for i, op := range ops {
		if op == "worktree:remove" {
			removeIdx = i
		}
		if op == "branch:-D" {
			deleteIdx = i
		}
	}
	if removeIdx == -1 || deleteIdx == -1 {
		t.Fatalf("did not find both remove and branch -D in ops: %v", ops)
	}
	if removeIdx >= deleteIdx {
		t.Errorf("worktree remove (%d) must happen before branch -D (%d)", removeIdx, deleteIdx)
	}
}
