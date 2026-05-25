package dispatcher //nolint:testpackage // white-box tests need access to unexported dispatcher methods

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestFilterAlreadyMergedBranchesUsesResolvedTargetBranch(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const (
		epicID = "oro-parent"
		beadID = "oro-child"
		target = protocol.EpicBranchPrefix + epicID
	)
	beadSrc.shown[epicID] = &protocol.BeadDetail{
		ID:   epicID,
		Type: "epic",
	}

	var closeReason string
	beadSrc.closeFn = func(_ context.Context, id, reason string) error {
		if id != beadID {
			t.Fatalf("CloseBead id = %q, want %q", id, beadID)
		}
		closeReason = reason
		return nil
	}

	var calls []string
	d.shutdownRunner = &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			call := strings.Join(args, " ")
			calls = append(calls, call)
			switch call {
			case "rev-parse agent/oro-child":
				return []byte("child-tip\n"), nil
			case "merge-base agent/oro-child " + target:
				return []byte("merge-base\n"), nil
			case "merge-base --is-ancestor agent/oro-child " + target:
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected git call: %s", call)
			}
		},
	}

	candidates := []protocol.Bead{{
		ID:   beadID,
		Epic: epicID,
	}}
	got := d.filterAlreadyMergedBranches(ctx, candidates)
	if len(got) != 0 {
		t.Fatalf("filtered candidates = %v, want none", got)
	}
	if closeReason != "branch already merged to "+target {
		t.Fatalf("CloseBead reason = %q, want resolved target %q", closeReason, target)
	}
	for _, call := range calls {
		if strings.HasSuffix(call, " main") {
			t.Fatalf("checked main instead of resolved target, calls=%v", calls)
		}
	}
}

// TestDeleteStaleAgentBranch_BranchCheckedOutQuarantines verifies that when
// git refuses to safely delete a branch because it is checked out in a
// worktree, deleteStaleAgentBranch preserves the worktree and records a
// recovery quarantine.
func TestDeleteStaleAgentBranch_BranchCheckedOutQuarantines(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)
	d.repoRoot = "/repo/root"

	beadID := "oro-stale"
	expectedWorktreePath := filepath.Join(d.repoRoot, ".worktrees", beadID)

	var mu sync.Mutex
	var ops []string // records attempted branch deletes

	// BranchExists: branch is present.
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == protocol.BranchPrefix+beadID, nil
	}

	wtMgr.deleteBranchFn = func(branch string) error {
		mu.Lock()
		ops = append(ops, "delete:"+branch)
		mu.Unlock()
		return fmt.Errorf("error: Cannot delete branch '%s' checked out at '%s'",
			branch, expectedWorktreePath)
	}

	if err := d.deleteStaleAgentBranch(context.Background(), beadID, "w-1", d.cfg.DefaultBranch); err == nil {
		t.Fatal("expected deleteStaleAgentBranch to quarantine checked-out branch")
	}

	mu.Lock()
	snapshot := make([]string, len(ops))
	copy(snapshot, ops)
	mu.Unlock()

	wantOps := []string{"delete:" + protocol.BranchPrefix + beadID}
	if len(snapshot) != len(wantOps) || snapshot[0] != wantOps[0] {
		t.Fatalf("ops = %v, want %v", snapshot, wantOps)
	}
	var reason string
	if err := d.db.QueryRowContext(context.Background(),
		`SELECT reason FROM recovery_quarantines WHERE bead_id=? AND status='open'`,
		beadID).Scan(&reason); err != nil {
		t.Fatalf("query recovery quarantine: %v", err)
	}
	if reason != "branch_worktree_mismatch" {
		t.Fatalf("quarantine reason = %q, want branch_worktree_mismatch", reason)
	}
}

// TestDeleteStaleAgentBranch_BranchCheckedOutLogsQuarantine verifies that a
// checked-out branch is surfaced through the event log instead of being cleaned.
func TestDeleteStaleAgentBranch_BranchCheckedOutLogsQuarantine(t *testing.T) {
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

	err := d.deleteStaleAgentBranch(context.Background(), beadID, "w-1", d.cfg.DefaultBranch)
	if err == nil {
		t.Fatal("expected error when cleanup fails, got nil")
	}

	if eventCount(t, d.db, "stale_agent_branch_quarantined") == 0 {
		t.Error("expected stale_agent_branch_quarantined event to be logged")
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

func TestDeleteStaleAgentBranchUsesAssignmentTargetBranch(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	beadID := "oro-merged-child"
	branch := protocol.BranchPrefix + beadID
	target := "epic/oro-parent"

	var calls []string
	d.worktrees = NewGitWorktreeManager("/repo/root", "", "", &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			call := strings.Join(args, " ")
			calls = append(calls, call)
			switch call {
			case "-C /repo/root branch --list " + branch:
				return []byte(branch + "\n"), nil
			case "-C /repo/root merge-base --is-ancestor " + branch + " " + target:
				return nil, nil
			case "-C /repo/root branch -D " + branch:
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected git call: %s", call)
			}
		},
	})

	if err := d.deleteStaleAgentBranch(ctx, beadID, "w-1", target); err != nil {
		t.Fatalf("deleteStaleAgentBranch: %v", err)
	}
	if !containsCall(calls, "-C /repo/root merge-base --is-ancestor "+branch+" "+target) {
		t.Fatalf("did not prove stale branch merged into assignment target, calls=%v", calls)
	}
	if containsCall(calls, "-C /repo/root merge-base --is-ancestor "+branch+" main") {
		t.Fatalf("proved against main instead of assignment target, calls=%v", calls)
	}

	var quarantineCount int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_quarantines WHERE bead_id=? AND reason='unsafe_stale_branch'`,
		beadID).Scan(&quarantineCount); err != nil {
		t.Fatalf("query quarantine count: %v", err)
	}
	if quarantineCount != 0 {
		t.Fatalf("unsafe_stale_branch quarantines = %d, want 0", quarantineCount)
	}
}

func TestDeleteStaleAgentBranch_DivergedFromTargetQuarantines(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	beadID := "oro-diverged-child"
	branch := protocol.BranchPrefix + beadID
	target := "epic/oro-parent"

	var calls []string
	d.worktrees = NewGitWorktreeManager("/repo/root", "", "", &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			call := strings.Join(args, " ")
			calls = append(calls, call)
			switch call {
			case "-C /repo/root branch --list " + branch:
				return []byte(branch + "\n"), nil
			case "-C /repo/root merge-base --is-ancestor " + branch + " " + target:
				return nil, errors.New("exit status 1")
			default:
				return nil, fmt.Errorf("unexpected git call: %s", call)
			}
		},
	})

	err := d.deleteStaleAgentBranch(ctx, beadID, "w-1", target)
	if err == nil {
		t.Fatal("expected diverged stale branch to be quarantined")
	}
	if !containsCall(calls, "-C /repo/root merge-base --is-ancestor "+branch+" "+target) {
		t.Fatalf("did not check target ancestry, calls=%v", calls)
	}
	if containsCall(calls, "-C /repo/root branch -d "+branch) {
		t.Fatalf("diverged branch should be preserved, calls=%v", calls)
	}

	var reason, details string
	if err := d.db.QueryRowContext(ctx,
		`SELECT reason, details FROM recovery_quarantines WHERE bead_id=? AND status='open'`,
		beadID).Scan(&reason, &details); err != nil {
		t.Fatalf("query quarantine: %v", err)
	}
	if reason != "unsafe_stale_branch" {
		t.Fatalf("reason = %q, want unsafe_stale_branch", reason)
	}
	if !strings.Contains(details, target) {
		t.Fatalf("details = %q, want resolved target %q", details, target)
	}
}

func TestPrepareExistingForReuse_FastForwardsBehindCleanBranch(t *testing.T) {
	var calls []string
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			call := strings.Join(args, " ")
			calls = append(calls, call)
			switch call {
			case "-C /repo/root rev-parse agent/oro-stale":
				return []byte("old\n"), nil
			case "-C /repo/root rev-parse main":
				return []byte("new\n"), nil
			case "-C /repo/root merge-base --is-ancestor agent/oro-stale main":
				return nil, nil
			case "-C /repo/root/.worktrees/oro-stale status --porcelain --untracked-files=no":
				return nil, nil
			case "-C /repo/root/.worktrees/oro-stale merge --ff-only main":
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected git call: %s", call)
			}
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	fastForwarded, err := mgr.PrepareExistingForReuse(context.Background(),
		"/repo/root/.worktrees/oro-stale", "agent/oro-stale", "main")
	if err != nil {
		t.Fatalf("PrepareExistingForReuse: %v", err)
	}
	if !fastForwarded {
		t.Fatal("fastForwarded = false, want true")
	}
	if !containsCall(calls, "-C /repo/root/.worktrees/oro-stale merge --ff-only main") {
		t.Fatalf("merge --ff-only not called, calls=%v", calls)
	}
}

func TestPrepareExistingForReuse_BehindDirtyTrackedBranchBlocks(t *testing.T) {
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			call := strings.Join(args, " ")
			switch call {
			case "-C /repo/root rev-parse agent/oro-dirty":
				return []byte("old\n"), nil
			case "-C /repo/root rev-parse main":
				return []byte("new\n"), nil
			case "-C /repo/root merge-base --is-ancestor agent/oro-dirty main":
				return nil, nil
			case "-C /repo/root/.worktrees/oro-dirty status --porcelain --untracked-files=no":
				return []byte(" M pkg/worker/worker_test.go\n"), nil
			default:
				return nil, fmt.Errorf("unexpected git call: %s", call)
			}
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	fastForwarded, err := mgr.PrepareExistingForReuse(context.Background(),
		"/repo/root/.worktrees/oro-dirty", "agent/oro-dirty", "main")
	if err == nil {
		t.Fatal("expected dirty tracked worktree to block reuse")
	}
	if fastForwarded {
		t.Fatal("fastForwarded = true, want false on blocked dirty worktree")
	}
	if !strings.Contains(err.Error(), "tracked changes") {
		t.Fatalf("error = %v, want tracked changes context", err)
	}
}

func TestRebaseDivergedExistingForReuse_CleanBranchRebasesInWorktree(t *testing.T) {
	var calls []string
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			call := strings.Join(args, " ")
			calls = append(calls, call)
			switch call {
			case "-C /repo/root/.worktrees/oro-ebzg status --porcelain --untracked-files=no":
				return nil, nil
			case "-C /repo/root/.worktrees/oro-ebzg rebase epic/oro-ebzp":
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected git call: %s", call)
			}
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	err := mgr.RebaseDivergedExistingForReuse(context.Background(),
		"/repo/root/.worktrees/oro-ebzg", "agent/oro-ebzg", "epic/oro-ebzp")
	if err != nil {
		t.Fatalf("RebaseDivergedExistingForReuse: %v", err)
	}
	if !containsCall(calls, "-C /repo/root/.worktrees/oro-ebzg rebase epic/oro-ebzp") {
		t.Fatalf("worktree rebase not called, calls=%v", calls)
	}
}

func TestRebaseDivergedExistingForReuse_DirtyTrackedBranchBlocks(t *testing.T) {
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			call := strings.Join(args, " ")
			switch call {
			case "-C /repo/root/.worktrees/oro-ebzg status --porcelain --untracked-files=no":
				return []byte(" M pkg/memory/memory.go\n"), nil
			default:
				return nil, fmt.Errorf("unexpected git call: %s", call)
			}
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	err := mgr.RebaseDivergedExistingForReuse(context.Background(),
		"/repo/root/.worktrees/oro-ebzg", "agent/oro-ebzg", "epic/oro-ebzp")
	if err == nil {
		t.Fatal("expected dirty tracked worktree to block rebase")
	}
	if !strings.Contains(err.Error(), "tracked changes") {
		t.Fatalf("error = %v, want tracked changes context", err)
	}
}

func TestPrepareBaseBranchForAssignment_FastForwardsBehindBranch(t *testing.T) {
	var calls []string
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			call := strings.Join(args, " ")
			calls = append(calls, call)
			switch call {
			case "-C /repo/root rev-parse epic/oro-stale":
				return []byte("old\n"), nil
			case "-C /repo/root rev-parse main":
				return []byte("new\n"), nil
			case "-C /repo/root merge-base --is-ancestor epic/oro-stale main":
				return nil, nil
			case "-C /repo/root branch -f epic/oro-stale main":
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected git call: %s", call)
			}
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	fastForwarded, err := mgr.PrepareBaseBranchForAssignment(context.Background(), "epic/oro-stale", "main")
	if err != nil {
		t.Fatalf("PrepareBaseBranchForAssignment: %v", err)
	}
	if !fastForwarded {
		t.Fatal("fastForwarded = false, want true")
	}
	if !containsCall(calls, "-C /repo/root branch -f epic/oro-stale main") {
		t.Fatalf("branch -f not called, calls=%v", calls)
	}
}

func TestPrepareBaseBranchForAssignment_DivergedBranchIsPreserved(t *testing.T) {
	var calls []string
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			call := strings.Join(args, " ")
			calls = append(calls, call)
			switch call {
			case "-C /repo/root rev-parse epic/oro-diverged":
				return []byte("epic\n"), nil
			case "-C /repo/root rev-parse main":
				return []byte("main\n"), nil
			case "-C /repo/root merge-base --is-ancestor epic/oro-diverged main":
				return nil, fmt.Errorf("exit status 1")
			case "-C /repo/root merge-base --is-ancestor main epic/oro-diverged":
				return nil, fmt.Errorf("exit status 1")
			default:
				return nil, fmt.Errorf("unexpected git call: %s", call)
			}
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	fastForwarded, err := mgr.PrepareBaseBranchForAssignment(context.Background(), "epic/oro-diverged", "main")
	if err != nil {
		t.Fatalf("PrepareBaseBranchForAssignment: %v", err)
	}
	if fastForwarded {
		t.Fatal("fastForwarded = true, want false for diverged branch")
	}
	if containsCall(calls, "-C /repo/root branch -f epic/oro-diverged main") {
		t.Fatalf("diverged branch should not be moved, calls=%v", calls)
	}
}

func TestValidateExistingWorktreeForReuse_PreparerFailureQuarantines(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	beadID := "oro-stale-reuse"
	worktree := "/repo/root/.worktrees/" + beadID
	branch := protocol.BranchPrefix + beadID

	wtMgr.currentBranchFn = func(_ context.Context, path string) (string, error) {
		if path != worktree {
			t.Fatalf("current branch path = %q, want %q", path, worktree)
		}
		return branch, nil
	}
	wtMgr.prepareReuseFn = func(_ context.Context, gotWorktree, gotBranch, gotBase string) (bool, error) {
		if gotWorktree != worktree || gotBranch != branch || gotBase != "main" {
			t.Fatalf("prepare args = %q %q %q", gotWorktree, gotBranch, gotBase)
		}
		return false, fmt.Errorf("stale branch behind base with tracked changes")
	}

	if d.validateExistingWorktreeForReuse(ctx, beadID, "worker-1", worktree, branch, "main") {
		t.Fatal("validateExistingWorktreeForReuse returned true, want quarantine")
	}

	var reason, details string
	if err := d.db.QueryRowContext(ctx,
		`SELECT reason, details FROM recovery_quarantines WHERE bead_id=? AND status='open'`,
		beadID).Scan(&reason, &details); err != nil {
		t.Fatalf("query quarantine: %v", err)
	}
	if reason != "unsafe_stale_branch" {
		t.Fatalf("reason = %q, want unsafe_stale_branch", reason)
	}
	if !strings.Contains(details, "tracked changes") {
		t.Fatalf("details = %q, want tracked changes context", details)
	}
}

func TestValidateExistingWorktreeForReuse_RebasesRegularDivergedBranch(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID   = "oro-ebzg"
		worktree = "/repo/root/.worktrees/oro-ebzg"
		branch   = "agent/oro-ebzg"
		base     = "epic/oro-ebzp"
	)
	wtMgr.currentBranchFn = func(_ context.Context, path string) (string, error) {
		if path != worktree {
			t.Fatalf("current branch path = %q, want %q", path, worktree)
		}
		return branch, nil
	}
	wtMgr.prepareReuseFn = func(_ context.Context, gotWorktree, gotBranch, gotBase string) (bool, error) {
		if gotWorktree != worktree || gotBranch != branch || gotBase != base {
			t.Fatalf("prepare args = %q %q %q", gotWorktree, gotBranch, gotBase)
		}
		return false, fmt.Errorf("agent branch %s diverged from base %s", branch, base)
	}
	rebased := false
	wtMgr.rebaseReuseFn = func(_ context.Context, gotWorktree, gotBranch, gotBase string) error {
		if gotWorktree != worktree || gotBranch != branch || gotBase != base {
			t.Fatalf("rebase args = %q %q %q", gotWorktree, gotBranch, gotBase)
		}
		rebased = true
		return nil
	}

	if !d.validateExistingWorktreeForReuse(ctx, beadID, "worker-1", worktree, branch, base) {
		t.Fatal("validateExistingWorktreeForReuse returned false, want reuse after rebase")
	}
	if !rebased {
		t.Fatal("regular diverged branch was not rebased")
	}

	var quarantines int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_quarantines WHERE bead_id=? AND status='open'`,
		beadID).Scan(&quarantines); err != nil {
		t.Fatalf("count quarantines: %v", err)
	}
	if quarantines != 0 {
		t.Fatalf("open quarantines = %d, want 0", quarantines)
	}
}

func TestValidateExistingWorktreeForReuse_AllowsEpicRebaseChildDivergence(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		epicID   = "oro-mp9r"
		beadID   = "oro-aeaw"
		worktree = "/repo/root/.worktrees/oro-aeaw"
		branch   = "agent/oro-aeaw"
		base     = "epic/oro-mp9r"
	)
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Rebase epic/oro-mp9r onto main",
		Type:               "task",
		Status:             "open",
		AcceptanceCriteria: rebaseChildAcceptance(epicID, base, "main"),
	}
	wtMgr.currentBranchFn = func(_ context.Context, path string) (string, error) {
		if path != worktree {
			t.Fatalf("current branch path = %q, want %q", path, worktree)
		}
		return branch, nil
	}
	wtMgr.prepareReuseFn = func(_ context.Context, gotWorktree, gotBranch, gotBase string) (bool, error) {
		if gotWorktree != worktree || gotBranch != branch || gotBase != base {
			t.Fatalf("prepare args = %q %q %q", gotWorktree, gotBranch, gotBase)
		}
		return false, fmt.Errorf("agent branch %s diverged from base %s", branch, base)
	}
	wtMgr.rebaseReuseFn = func(_ context.Context, _, _, _ string) error {
		t.Fatal("rebase child should not be rebased onto its epic base during reuse")
		return nil
	}

	if !d.validateExistingWorktreeForReuse(ctx, beadID, "worker-1", worktree, branch, base) {
		t.Fatal("validateExistingWorktreeForReuse returned false, want rebase child reuse allowed")
	}

	var quarantines int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_quarantines WHERE bead_id=? AND status='open'`,
		beadID).Scan(&quarantines); err != nil {
		t.Fatalf("count quarantines: %v", err)
	}
	if quarantines != 0 {
		t.Fatalf("open quarantines = %d, want 0", quarantines)
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

// TestWorktreeManager_PruneStale_RemovesWorktreeBeforeBranchDelete verifies the command
// sequence inside pruneStale: worktree remove → worktree prune → branch -d.
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
	err := mgr.pruneStale(context.Background(), "/repo/root/.worktrees/oro-x", "agent/oro-x", "epic/oro-parent")
	if err != nil {
		t.Fatalf("pruneStale: %v", err)
	}

	// Expected: worktree:remove -> worktree:prune -> branch:-D
	// with a merge-base proof before the branch delete.
	if len(ops) < 3 {
		t.Fatalf("expected at least 3 ops, got %v", ops)
	}
	wantOps := []string{"worktree:remove", "worktree:prune", "branch:-D"}
	for i, want := range wantOps {
		if ops[i] != want {
			t.Errorf("ops[%d]: got %q, want %q", i, ops[i], want)
		}
	}
	// remove MUST come before target-proven branch deletion.
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
