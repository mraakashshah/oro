package dispatcher //nolint:testpackage // white-box tests for worktree manager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestInspectEpicBranchReportsCheckoutRelationAndCAS(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runAssignmentTestGit(t, repo, "init", "-b", "main")
	runAssignmentTestGit(t, repo, "config", "user.email", "test@example.com")
	runAssignmentTestGit(t, repo, "config", "user.name", "Oro Test")
	runAssignmentTestGit(t, repo, "commit", "--allow-empty", "-m", "base")
	rootOID := gitOut(t, repo, "rev-parse", "HEAD")
	for _, branch := range []string{"epic/behind", "epic/checked", "epic/diverged", "epic/stale"} {
		runAssignmentTestGit(t, repo, "branch", branch, rootOID)
	}

	runAssignmentTestGit(t, repo, "switch", "epic/diverged")
	runAssignmentTestGit(t, repo, "commit", "--allow-empty", "-m", "epic diverged")
	divergedOID := gitOut(t, repo, "rev-parse", "HEAD")
	runAssignmentTestGit(t, repo, "switch", "main")
	runAssignmentTestGit(t, repo, "commit", "--allow-empty", "-m", "main advances")
	baseOID := gitOut(t, repo, "rev-parse", "HEAD")
	runAssignmentTestGit(t, repo, "branch", "epic/equal", baseOID)
	runAssignmentTestGit(t, repo, "switch", "-c", "epic/ahead")
	runAssignmentTestGit(t, repo, "commit", "--allow-empty", "-m", "epic ahead")
	aheadOID := gitOut(t, repo, "rev-parse", "HEAD")
	runAssignmentTestGit(t, repo, "switch", "main")

	checkoutParent := filepath.Join(t.TempDir(), "checkout paths")
	checkedB := filepath.Join(checkoutParent, "z checked path")
	checkedA := filepath.Join(checkoutParent, "a checked path")
	detached := filepath.Join(checkoutParent, "detached path")
	runAssignmentTestGit(t, repo, "worktree", "add", "--force", checkedB, "epic/checked")
	runAssignmentTestGit(t, repo, "worktree", "add", "--force", checkedA, "epic/checked")
	runAssignmentTestGit(t, repo, "worktree", "add", "--detach", detached, "epic/checked")
	canonicalCheckoutParent, err := filepath.EvalSymlinks(checkoutParent)
	if err != nil {
		t.Fatalf("canonicalize checkout parent: %v", err)
	}
	checkedA = filepath.Join(canonicalCheckoutParent, filepath.Base(checkedA))
	checkedB = filepath.Join(canonicalCheckoutParent, filepath.Base(checkedB))

	mgr := NewGitWorktreeManager(repo, "", "", &ExecCommandRunner{})

	_, err = mgr.inspectEpicBranch(ctx, "epic/missing", "main")
	var refErr *epicBranchRefError
	if !errors.As(err, &refErr) {
		t.Fatalf("missing branch error = %v, want *epicBranchRefError", err)
	}

	tests := []struct {
		name      string
		branch    string
		wantOID   string
		want      branchBaseRelation
		wantPaths []string
	}{
		{name: "equal", branch: "epic/equal", wantOID: baseOID, want: branchSame},
		{name: "behind", branch: "epic/behind", wantOID: rootOID, want: branchStrictlyBehind},
		{name: "ahead", branch: "epic/ahead", wantOID: aheadOID, want: branchContainsBase},
		{name: "diverged", branch: "epic/diverged", wantOID: divergedOID, want: branchDiverged},
		{
			name: "checked out paths are sorted and detached is ignored", branch: "epic/checked", wantOID: rootOID,
			want: branchStrictlyBehind, wantPaths: []string{checkedA, checkedB},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := gitOut(t, repo, "rev-parse", "refs/heads/"+tt.branch)
			inspection, inspectErr := mgr.inspectEpicBranch(ctx, tt.branch, "main")
			if inspectErr != nil {
				t.Fatalf("inspectEpicBranch: %v", inspectErr)
			}
			if inspection.BranchOID != tt.wantOID || inspection.BaseOID != baseOID || inspection.Relation != tt.want {
				t.Fatalf("inspection = %#v, want branch=%s base=%s relation=%v", inspection, tt.wantOID, baseOID, tt.want)
			}
			if !slices.Equal(inspection.CheckedOutPaths, tt.wantPaths) {
				t.Fatalf("CheckedOutPaths = %q, want %q", inspection.CheckedOutPaths, tt.wantPaths)
			}
			if gitOut(t, repo, "rev-parse", "refs/heads/"+tt.branch) != before {
				t.Fatalf("inspection mutated %s", tt.branch)
			}
		})
	}

	checkedInspection, err := mgr.inspectEpicBranch(ctx, "epic/checked", "main")
	if err != nil {
		t.Fatalf("inspect checked branch: %v", err)
	}
	err = mgr.compareAndSwapBranch(ctx, "epic/checked", checkedInspection.BranchOID, checkedInspection.BaseOID)
	var checkedOutErr *epicBranchCheckedOutError
	if !errors.As(err, &checkedOutErr) || !slices.Equal(checkedOutErr.CheckedOutPaths, []string{checkedA, checkedB}) {
		t.Fatalf("checked-out CAS error = %v, want sorted *epicBranchCheckedOutError", err)
	}
	if got := gitOut(t, repo, "rev-parse", "epic/checked"); got != rootOID {
		t.Fatalf("checked branch moved to %s, want %s", got, rootOID)
	}

	behind, err := mgr.inspectEpicBranch(ctx, "epic/behind", "main")
	if err != nil {
		t.Fatalf("inspect behind branch: %v", err)
	}
	if err := mgr.compareAndSwapBranch(ctx, "epic/behind", behind.BranchOID, behind.BaseOID); err != nil {
		t.Fatalf("fast-forward CAS: %v", err)
	}
	if got := gitOut(t, repo, "rev-parse", "epic/behind"); got != baseOID {
		t.Fatalf("behind branch = %s after CAS, want %s", got, baseOID)
	}

	stale, err := mgr.inspectEpicBranch(ctx, "epic/stale", "main")
	if err != nil {
		t.Fatalf("inspect stale branch: %v", err)
	}
	runAssignmentTestGit(t, repo, "update-ref", "refs/heads/epic/stale", aheadOID, stale.BranchOID)
	err = mgr.compareAndSwapBranch(ctx, "epic/stale", stale.BranchOID, stale.BaseOID)
	var casErr *epicBranchCASError
	if !errors.As(err, &casErr) {
		t.Fatalf("stale CAS error = %v, want *epicBranchCASError", err)
	}
	if got := gitOut(t, repo, "rev-parse", "epic/stale"); got != aheadOID {
		t.Fatalf("stale CAS clobbered concurrent OID: got %s, want %s", got, aheadOID)
	}
}

func TestGitWorktreeManager_Create_Success(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	path, branch, err := mgr.Create(context.Background(), "abc123", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantPath := "/repo/root/.worktrees/abc123"
	if path != wantPath {
		t.Fatalf("path: got %q, want %q", path, wantPath)
	}

	wantBranch := "agent/abc123"
	if branch != wantBranch {
		t.Fatalf("branch: got %q, want %q", branch, wantBranch)
	}

	// Expect 5 calls: fetch + two ref lookups + ancestry check + worktree add.
	// The mock returns the same empty SHA for both refs, so either base is safe;
	// Create consistently selects the local ref for that case.
	if len(runner.calls) != 5 {
		t.Fatalf("expected 5 command calls, got %d", len(runner.calls))
	}
	call := runner.calls[3] // fetch + two ref lookups precede worktree creation
	if call.Name != "git" {
		t.Fatalf("name: got %q, want %q", call.Name, "git")
	}
	wantArgs := []string{"-C", "/repo/root", "worktree", "add", wantPath, "-b", wantBranch, "main"}
	if len(call.Args) != len(wantArgs) {
		t.Fatalf("args: got %v, want %v", call.Args, wantArgs)
	}
	for i, a := range call.Args {
		if a != wantArgs[i] {
			t.Fatalf("args[%d]: got %q, want %q", i, a, wantArgs[i])
		}
	}
}

func TestGitWorktreeManagerCreateSelectsSafeFreshBase(t *testing.T) {
	const (
		repoRoot    = "/repo/root"
		worktreeDir = "/repo/root/.worktrees/oro-safe"
		agentBranch = "agent/oro-safe"
	)

	tests := []struct {
		name        string
		localHead   string
		localErr    error
		remoteHead  string
		ancestors   map[string]bool
		wantBase    string
		wantCreate  bool
		wantErrPart string
	}{
		{
			name:       "uses ahead local branch",
			localHead:  "local",
			remoteHead: "remote",
			ancestors: map[string]bool{
				"origin/main->main": true,
			},
			wantBase:   "main",
			wantCreate: true,
		},
		{
			name:       "uses ahead remote branch",
			localHead:  "local",
			remoteHead: "remote",
			ancestors: map[string]bool{
				"main->origin/main": true,
			},
			wantBase:   "origin/main",
			wantCreate: true,
		},
		{
			name:       "uses remote branch when local branch is missing",
			localErr:   fmt.Errorf("fatal: ambiguous argument 'main': unknown revision"),
			remoteHead: "remote",
			wantBase:   "origin/main",
			wantCreate: true,
		},
		{
			name:        "rejects divergent branches",
			localHead:   "local",
			remoteHead:  "remote",
			ancestors:   map[string]bool{},
			wantErrPart: "diverged",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockCommandRunner{
				callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
					switch {
					case name == "git" && slices.Equal(args, []string{"-C", repoRoot, "fetch", "origin", "main"}):
						return nil, nil
					case name == "git" && slices.Equal(args, []string{"-C", repoRoot, "rev-parse", "main"}):
						return []byte(tt.localHead + "\n"), tt.localErr
					case name == "git" && slices.Equal(args, []string{"-C", repoRoot, "rev-parse", "origin/main"}):
						return []byte(tt.remoteHead + "\n"), nil
					case name == "git" && slices.Equal(args, []string{"-C", repoRoot, "branch", "--list", "main"}):
						return nil, nil
					case name == "git" && len(args) == 6 && slices.Equal(args[:4], []string{"-C", repoRoot, "merge-base", "--is-ancestor"}):
						if tt.ancestors[args[4]+"->"+args[5]] {
							return nil, nil
						}
						return nil, fmt.Errorf("exit status 1")
					case name == "git" && len(args) == 8 && slices.Equal(args[:6], []string{"-C", repoRoot, "worktree", "add", worktreeDir, "-b"}):
						if args[6] != agentBranch || args[7] != tt.wantBase {
							t.Fatalf("worktree base args = %v, want branch %q from %q", args, agentBranch, tt.wantBase)
						}
						return nil, nil
					case name == "make":
						return nil, nil
					default:
						t.Fatalf("unexpected command: %s %v", name, args)
						return nil, nil
					}
				},
			}
			mgr := NewGitWorktreeManager(repoRoot, "", "", runner)

			_, _, err := mgr.Create(context.Background(), "oro-safe", "main")
			if tt.wantErrPart != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrPart) {
					t.Fatalf("Create error = %v, want %q", err, tt.wantErrPart)
				}
				for _, call := range runner.calls {
					if containsAll(call.Args, "worktree", "add") {
						t.Fatalf("Create ran worktree add after divergent base comparison: %v", call.Args)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if !tt.wantCreate {
				t.Fatal("test setup expected no worktree creation")
			}
		})
	}
}

func TestGitWorktreeManager_Create_Error(t *testing.T) {
	runner := &mockCommandRunner{
		err: fmt.Errorf("git worktree add failed: branch already exists"),
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	_, _, err := mgr.Create(context.Background(), "abc123", "main")
	if err == nil {
		t.Fatal("expected error from Create")
	}
	if !strings.Contains(err.Error(), "worktree add") {
		t.Fatalf("error should mention worktree add, got: %v", err)
	}
}

func TestGitWorktreeManager_Remove_Success(t *testing.T) {
	// Create a temporary directory to simulate an existing worktree
	tmpDir := t.TempDir()
	wtPath := filepath.Join(tmpDir, "worktree")
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatalf("failed to create temp worktree: %v", err)
	}

	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

	err := mgr.Remove(context.Background(), wtPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expect exactly one call: git worktree remove. Remove must not inspect,
	// stage, or auto-commit worker changes before cleanup.
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 command call, got %d: %+v", len(runner.calls), runner.calls)
	}

	removeCall := runner.calls[0]
	if !containsAll(removeCall.Args, "worktree", "remove", "--force") {
		t.Fatalf("call[0] should contain worktree remove --force, got: %v", removeCall.Args)
	}
}

func TestRemoveDoesNotAutoCommitBeforeWorktreeRemoval(t *testing.T) {
	tmpDir := t.TempDir()
	wtPath := filepath.Join(tmpDir, "worktree")
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatalf("create worktree dir: %v", err)
	}

	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

	if err := mgr.Remove(context.Background(), wtPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	for _, call := range runner.calls {
		if call.Name != "git" || !containsAll(call.Args, "-C", tmpDir, "worktree", "remove", wtPath, "--force") {
			t.Fatalf("Remove ran unexpected command: %s %v", call.Name, call.Args)
		}
	}
}

func TestDeleteBranchUsesSafeDelete(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	if err := mgr.DeleteBranch(context.Background(), "agent/oro-safe"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("calls = %+v, want one git branch call", runner.calls)
	}
	if !containsAll(runner.calls[0].Args, "branch", "-d", "agent/oro-safe") {
		t.Fatalf("DeleteBranch args = %v, want safe -d delete", runner.calls[0].Args)
	}
	for _, arg := range runner.calls[0].Args {
		if strings.Contains(arg, "D") || strings.Contains(strings.ToLower(arg), "force") {
			t.Fatalf("DeleteBranch used force delete arg %q in args: %v", arg, runner.calls[0].Args)
		}
	}
}

func TestDeleteBranchMergedIntoUsesTargetProofBeforeForcedDelete(t *testing.T) {
	t.Run("force deletes only after target ancestry proof succeeds", func(t *testing.T) {
		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

		err := mgr.DeleteBranchMergedInto(context.Background(), "agent/oro-safe", "epic/parent")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		wantCalls := [][]string{
			{"-C", "/repo/root", "merge-base", "--is-ancestor", "agent/oro-safe", "epic/parent"},
			{"-C", "/repo/root", "branch", "-D", "agent/oro-safe"},
		}
		if len(runner.calls) != len(wantCalls) {
			t.Fatalf("expected %d command calls, got %d: %#v", len(wantCalls), len(runner.calls), runner.calls)
		}
		for i, wantArgs := range wantCalls {
			call := runner.calls[i]
			if call.Name != "git" {
				t.Fatalf("call[%d] name = %q, want git", i, call.Name)
			}
			if !slices.Equal(call.Args, wantArgs) {
				t.Fatalf("call[%d] args = %v, want %v", i, call.Args, wantArgs)
			}
		}
	})

	t.Run("returns proof error and skips delete when proof fails", func(t *testing.T) {
		proofErr := fmt.Errorf("not an ancestor")
		runner := &mockCommandRunner{err: proofErr}
		mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

		err := mgr.DeleteBranchMergedInto(context.Background(), "agent/oro-safe", "epic/parent")
		if err == nil {
			t.Fatal("expected proof error")
		}
		if !strings.Contains(err.Error(), "prove branch agent/oro-safe merged into epic/parent") {
			t.Fatalf("error = %v, want proof context", err)
		}

		wantArgs := []string{"-C", "/repo/root", "merge-base", "--is-ancestor", "agent/oro-safe", "epic/parent"}
		if len(runner.calls) != 1 {
			t.Fatalf("calls = %#v, want only ancestry proof", runner.calls)
		}
		if call := runner.calls[0]; call.Name != "git" || !slices.Equal(call.Args, wantArgs) {
			t.Fatalf("proof call = %s %v, want git %v", runner.calls[0].Name, runner.calls[0].Args, wantArgs)
		}
	})
}

func TestForceDeleteBranchUsesExplicitAPI(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	if err := mgr.ForceDeleteBranch(context.Background(), "agent/oro-merged"); err != nil {
		t.Fatalf("ForceDeleteBranch: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("calls = %+v, want one git branch call", runner.calls)
	}
	if !containsAll(runner.calls[0].Args, "branch", "-D", "agent/oro-merged") {
		t.Fatalf("ForceDeleteBranch args = %v, want explicit -D delete", runner.calls[0].Args)
	}
}

func TestGitWorktreeManager_Remove_Error(t *testing.T) {
	// Create a temporary directory to simulate an existing worktree
	tmpDir := t.TempDir()
	wtPath := filepath.Join(tmpDir, "worktree")
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatalf("failed to create temp worktree: %v", err)
	}

	runner := &mockCommandRunner{
		err: fmt.Errorf("git worktree remove failed: not a worktree"),
	}
	mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

	err := mgr.Remove(context.Background(), wtPath)
	if err == nil {
		t.Fatal("expected error from Remove")
	}
	if !strings.Contains(err.Error(), "worktree remove") {
		t.Fatalf("error should mention worktree remove, got: %v", err)
	}
}

func TestRemove_AlreadyRemoved(t *testing.T) {
	// Use a path that doesn't exist on the filesystem
	nonExistentPath := "/nonexistent/path/that/does/not/exist"

	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	err := mgr.Remove(context.Background(), nonExistentPath)
	// Should return nil when path doesn't exist (idempotent)
	if err != nil {
		t.Fatalf("expected nil error for non-existent path, got: %v", err)
	}

	// Should not call any git commands when path doesn't exist
	if len(runner.calls) != 0 {
		t.Fatalf("expected 0 command calls for non-existent path, got %d calls", len(runner.calls))
	}
}

func TestGitWorktreeManager_Create_DifferentBeadIDs(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/my/repo", "", "", runner)

	tests := []struct {
		beadID     string
		wantPath   string
		wantBranch string
	}{
		{"bead-1", "/my/repo/.worktrees/bead-1", "agent/bead-1"},
		{"xyz.42", "/my/repo/.worktrees/xyz.42", "agent/xyz.42"},
		{"oro-ujb.3", "/my/repo/.worktrees/oro-ujb.3", "agent/oro-ujb.3"},
	}

	for _, tt := range tests {
		t.Run(tt.beadID, func(t *testing.T) {
			path, branch, err := mgr.Create(context.Background(), tt.beadID, "main")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if path != tt.wantPath {
				t.Fatalf("path: got %q, want %q", path, tt.wantPath)
			}
			if branch != tt.wantBranch {
				t.Fatalf("branch: got %q, want %q", branch, tt.wantBranch)
			}
		})
	}
}

func TestGitWorktreeManager_ImplementsInterface(t *testing.T) {
	runner := &mockCommandRunner{}
	var _ WorktreeManager = NewGitWorktreeManager("/repo", "", "", runner)
}

func TestGitWorktreeManager_PrunePreservesOrphanDirs(t *testing.T) {
	tmpDir := t.TempDir()
	worktreesDir := filepath.Join(tmpDir, ".worktrees")
	if err := os.MkdirAll(worktreesDir, 0o750); err != nil {
		t.Fatalf("mkdir .worktrees: %v", err)
	}

	// Create orphan worktree directories (leftover from a crash).
	orphans := []string{"bead-1", "bead-2", "oro-abc.3"}
	for _, name := range orphans {
		dir := filepath.Join(worktreesDir, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir orphan %s: %v", name, err)
		}
		// Put a file inside to ensure non-empty dirs are removed.
		if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
			t.Fatalf("write file in orphan %s: %v", name, err)
		}
	}

	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

	err := mgr.Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune returned error: %v", err)
	}

	// Verify git worktree prune was called.
	if len(runner.calls) < 1 {
		t.Fatal("expected at least 1 command call for git worktree prune")
	}
	pruneCall := runner.calls[0]
	if pruneCall.Name != "git" {
		t.Fatalf("call[0] name: got %q, want %q", pruneCall.Name, "git")
	}
	wantArgs := []string{"-C", tmpDir, "worktree", "prune"}
	if len(pruneCall.Args) != len(wantArgs) {
		t.Fatalf("prune args: got %v, want %v", pruneCall.Args, wantArgs)
	}
	for i, a := range pruneCall.Args {
		if a != wantArgs[i] {
			t.Fatalf("prune args[%d]: got %q, want %q", i, a, wantArgs[i])
		}
	}

	// Verify orphan directories are preserved for forensic recovery. Git
	// bookkeeping may be stale after a crash, but the directory can still hold
	// worker changes.
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		t.Fatalf("reading .worktrees after Prune: %v", err)
	}
	if len(entries) != len(orphans) {
		t.Fatalf("expected orphan dirs to be preserved, got %d entries want %d", len(entries), len(orphans))
	}
}

func TestGitWorktreeManager_Prune_NoWorktreesDir(t *testing.T) {
	tmpDir := t.TempDir()
	// Intentionally do NOT create .worktrees/ — Prune should be graceful.

	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

	err := mgr.Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune with no .worktrees dir should not error, got: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 command call for git worktree prune, got %d", len(runner.calls))
	}
}

func TestGitWorktreeManager_Prune_GitPruneErrorLogged(t *testing.T) {
	tmpDir := t.TempDir()
	worktreesDir := filepath.Join(tmpDir, ".worktrees")
	if err := os.MkdirAll(worktreesDir, 0o750); err != nil {
		t.Fatalf("mkdir .worktrees: %v", err)
	}
	// Create one orphan.
	if err := os.MkdirAll(filepath.Join(worktreesDir, "stale-1"), 0o750); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}

	// git worktree prune fails, but Prune should not remove directories or return
	// an error. Recovery-owned worktrees must remain inspectable.
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if containsAll(args, "worktree", "prune") {
				return nil, fmt.Errorf("git prune failed")
			}
			return nil, nil
		},
	}
	mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

	err := mgr.Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune should not return error even if git prune fails, got: %v", err)
	}

	if _, err := os.Stat(filepath.Join(worktreesDir, "stale-1")); err != nil {
		t.Fatalf("orphan dir should be preserved after prune failure: %v", err)
	}
}

func TestGitWorktreeManager_Prune_PreservesRegisteredWorktrees(t *testing.T) {
	tmpDir := t.TempDir()
	worktreesDir := filepath.Join(tmpDir, ".worktrees")
	preserved := filepath.Join(worktreesDir, "oro-manual")
	orphan := filepath.Join(worktreesDir, "oro-orphan")
	for _, dir := range []string{preserved, orphan} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	runner := &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if containsAll(args, "worktree", "list", "--porcelain") {
				return []byte("worktree " + preserved + "\nHEAD abc123\nbranch refs/heads/agent/oro-manual\n"), nil
			}
			return nil, nil
		},
	}
	mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

	if err := mgr.Prune(context.Background()); err != nil {
		t.Fatalf("Prune returned error: %v", err)
	}
	if _, err := os.Stat(preserved); err != nil {
		t.Fatalf("registered worktree should be preserved: %v", err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("orphan worktree should be preserved: %v", err)
	}
}

func TestWorktreeManager_PrunesStaleBeforeCreate(t *testing.T) {
	callCount := 0
	worktreeAddFailed := false
	runner := &mockCommandRunner{
		callFn: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			callCount++
			if containsAll(args, "worktree", "add") && !worktreeAddFailed {
				worktreeAddFailed = true
				return nil, fmt.Errorf("fatal: a branch named 'agent/oro-stale' already exists")
			}
			// All subsequent calls succeed
			return nil, nil
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	path, branch, err := mgr.Create(context.Background(), "oro-stale", "main")
	if err != nil {
		t.Fatalf("expected Create to succeed after pruning stale branch, got: %v", err)
	}

	wantPath := "/repo/root/.worktrees/oro-stale"
	if path != wantPath {
		t.Fatalf("path: got %q, want %q", path, wantPath)
	}
	wantBranch := "agent/oro-stale"
	if branch != wantBranch {
		t.Fatalf("branch: got %q, want %q", branch, wantBranch)
	}

	// Expect 10 calls:
	// 0. git fetch origin main (best-effort, succeeds)
	// 1. git worktree add (fails - branch already exists)
	// 2. git worktree remove <path> --force
	// 3. git worktree prune
	// 4. git merge-base --is-ancestor agent/oro-stale origin/main
	// 5. git branch -D agent/oro-stale
	// 6. git worktree add (succeeds)
	// 7. make stage-assets (best-effort)
	if len(runner.calls) != 10 {
		var callDescs []string
		for i, c := range runner.calls {
			callDescs = append(callDescs, fmt.Sprintf("  [%d] %s %s", i, c.Name, strings.Join(c.Args, " ")))
		}
		t.Fatalf("expected 10 command calls, got %d:\n%s", len(runner.calls), strings.Join(callDescs, "\n"))
	}

	// Call 1 (index 1): initial worktree add (fails)
	c1 := runner.calls[3]
	if c1.Name != "git" || !containsAll(c1.Args, "worktree", "add") {
		t.Fatalf("call[1] should be git worktree add, got: %s %v", c1.Name, c1.Args)
	}

	// Call 2 (index 2): git worktree remove --force
	c2 := runner.calls[4]
	if c2.Name != "git" || !containsAll(c2.Args, "worktree", "remove", "--force") {
		t.Fatalf("call[2] should be git worktree remove --force, got: %s %v", c2.Name, c2.Args)
	}

	// Call 3 (index 3): git worktree prune
	c3 := runner.calls[5]
	if c3.Name != "git" || !containsAll(c3.Args, "worktree", "prune") {
		t.Fatalf("call[3] should be git worktree prune, got: %s %v", c3.Name, c3.Args)
	}

	// Call 4 (index 4): git merge-base --is-ancestor agent/oro-stale origin/main
	c4 := runner.calls[6]
	if c4.Name != "git" || !slices.Equal(c4.Args, []string{"-C", "/repo/root", "merge-base", "--is-ancestor", "agent/oro-stale", "main"}) {
		t.Fatalf("call[6] should prove stale branch merged into main, got: %s %v", c4.Name, c4.Args)
	}

	// Call 5 (index 5): git branch -D agent/oro-stale after target proof
	c5 := runner.calls[7]
	if c5.Name != "git" || !slices.Equal(c5.Args, []string{"-C", "/repo/root", "branch", "-D", "agent/oro-stale"}) {
		t.Fatalf("call[5] should be git branch -D agent/oro-stale after proof, got: %s %v", c5.Name, c5.Args)
	}

	// Call 6 (index 6): retry worktree add (succeeds)
	c6 := runner.calls[8]
	if c6.Name != "git" || !containsAll(c6.Args, "worktree", "add") {
		t.Fatalf("call[6] should be git worktree add (retry), got: %s %v", c6.Name, c6.Args)
	}
}

func TestWorktreeManager_PrunesStaleBranchMergedIntoBaseBeforeCreate(t *testing.T) {
	const (
		repoRoot    = "/repo/root"
		beadID      = "oro-stale-qg"
		baseBranch  = "epic/oro-parent"
		agentBranch = "agent/oro-stale-qg"
	)
	var staleBranchDeleted bool
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "make" {
				return nil, nil
			}
			if name != "git" {
				return nil, nil
			}
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "fetch", "origin", baseBranch}):
				return nil, fmt.Errorf("no remote epic branch")
			case slices.Equal(args, []string{"-C", repoRoot, "branch", "--list", baseBranch}):
				return []byte("  " + baseBranch + "\n"), nil
			case containsAll(args, "worktree", "add", "-b", agentBranch, baseBranch):
				if staleBranchDeleted {
					return nil, nil
				}
				return nil, fmt.Errorf("fatal: a branch named %q already exists", agentBranch)
			case containsAll(args, "worktree", "remove", "--force"):
				return nil, nil
			case containsAll(args, "worktree", "prune"):
				return nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "merge-base", "--is-ancestor", agentBranch, baseBranch}):
				return nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "branch", "-D", agentBranch}):
				staleBranchDeleted = true
				return nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "branch", "-d", agentBranch}):
				return nil, fmt.Errorf("error: branch %q is not fully merged", agentBranch)
			default:
				return nil, nil
			}
		},
	}
	mgr := NewGitWorktreeManager(repoRoot, "", "", runner)

	path, branch, err := mgr.Create(context.Background(), beadID, baseBranch)
	if err != nil {
		t.Fatalf("Create should delete stale branch after target proof and retry, got: %v", err)
	}
	if path != filepath.Join(repoRoot, ".worktrees", beadID) {
		t.Fatalf("path = %q", path)
	}
	if branch != agentBranch {
		t.Fatalf("branch = %q, want %q", branch, agentBranch)
	}
	if !staleBranchDeleted {
		t.Fatal("expected stale branch to be deleted with explicit target proof")
	}

	var sawProof, sawForceDelete bool
	for _, call := range runner.calls {
		if slices.Equal(call.Args, []string{"-C", repoRoot, "merge-base", "--is-ancestor", agentBranch, baseBranch}) {
			sawProof = true
		}
		if slices.Equal(call.Args, []string{"-C", repoRoot, "branch", "-D", agentBranch}) {
			sawForceDelete = true
		}
	}
	if !sawProof || !sawForceDelete {
		t.Fatalf("expected target proof and force-delete calls, sawProof=%v sawForceDelete=%v calls=%#v", sawProof, sawForceDelete, runner.calls)
	}
}

func TestWorktreeManager_RetryUsesPreservedBranchWhenPruneCannotDelete(t *testing.T) {
	const (
		repoRoot    = "/repo/root"
		beadID      = "oro-tfsv-qg"
		baseBranch  = "main"
		agentBranch = "agent/oro-tfsv-qg"
		worktreeDir = "/repo/root/.worktrees/oro-tfsv-qg"
	)
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "make" {
				return nil, nil
			}
			if name != "git" {
				return nil, nil
			}
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "fetch", "origin", baseBranch}):
				return nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "rev-parse", "main"}), slices.Equal(args, []string{"-C", repoRoot, "rev-parse", "origin/main"}):
				return []byte("same\n"), nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", worktreeDir, "-b", agentBranch, "main"}):
				return nil, fmt.Errorf("fatal: a branch named %q already exists", agentBranch)
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "remove", worktreeDir, "--force"}):
				return nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "prune"}):
				return nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "merge-base", "--is-ancestor", agentBranch, "main"}):
				return nil, fmt.Errorf("branch has unmerged work")
			case slices.Equal(args, []string{"-C", repoRoot, "branch", "--list", agentBranch}):
				return []byte("  " + agentBranch + "\n"), nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", worktreeDir, agentBranch}):
				return nil, nil
			default:
				t.Fatalf("unexpected command: %s %v", name, args)
				return nil, nil
			}
		},
	}
	mgr := NewGitWorktreeManager(repoRoot, "", "", runner)

	path, branch, err := mgr.Create(context.Background(), beadID, baseBranch)
	if err != nil {
		t.Fatalf("Create should reuse preserved branch after prune cannot delete it: %v", err)
	}
	if path != worktreeDir {
		t.Fatalf("path: got %q, want %q", path, worktreeDir)
	}
	if branch != agentBranch {
		t.Fatalf("branch: got %q, want %q", branch, agentBranch)
	}

	retry := runner.calls[8]
	if retry.Name != "git" || !slices.Equal(retry.Args, []string{"-C", repoRoot, "worktree", "add", worktreeDir, agentBranch}) {
		t.Fatalf("retry should attach existing branch without -b, got: %s %v", retry.Name, retry.Args)
	}
}

// containsAll returns true if haystack contains all needles.
func containsAll(haystack []string, needles ...string) bool {
	for _, n := range needles {
		if !slices.Contains(haystack, n) {
			return false
		}
	}
	return true
}

// TestCreateWithBaseBranch verifies that Create passes baseBranch to
// `git worktree add` and that an empty baseBranch defaults to "main".
func TestCreateWithBaseBranch(t *testing.T) {
	t.Run("custom_base_branch_passed_to_git", func(t *testing.T) {
		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

		_, _, err := mgr.Create(context.Background(), "oro-abc", "agent/epic-bar")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// The default mock reports matching local and remote heads, so either ref
		// is safe and Create consistently selects the local branch.
		var args []string
		for _, call := range runner.calls {
			if call.Name == "git" && containsAll(call.Args, "worktree", "add") {
				args = call.Args
				break
			}
		}
		if args == nil {
			t.Fatal("expected git worktree add call")
		}
		// effectiveBase must be the last argument to `git worktree add <path> -b <branch> <effectiveBase>`.
		if args[len(args)-1] != "agent/epic-bar" {
			t.Fatalf("git worktree add last arg: got %q, want %q", args[len(args)-1], "agent/epic-bar")
		}
	})

	t.Run("empty_base_branch_defaults_to_main", func(t *testing.T) {
		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

		_, _, err := mgr.Create(context.Background(), "oro-abc", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Matching local and remote heads select the equivalent local default.
		var args []string
		for _, call := range runner.calls {
			if call.Name == "git" && containsAll(call.Args, "worktree", "add") {
				args = call.Args
				break
			}
		}
		if args == nil {
			t.Fatal("expected git worktree add call")
		}
		if args[len(args)-1] != "main" {
			t.Fatalf("git worktree add last arg: got %q, want %q (empty baseBranch should default to main)", args[len(args)-1], "main")
		}
	})
}

func TestCreateWithMissingEpicBaseBranchCreatesBaseBeforeWorktree(t *testing.T) {
	const (
		repoRoot   = "/repo/root"
		beadID     = "oro-z0av-qg"
		baseBranch = "epic/oro-z0av"
	)

	epicBranchCreated := false
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			switch {
			case containsAll(args, "fetch", "origin", baseBranch):
				return nil, fmt.Errorf("fatal: couldn't find remote ref %s", baseBranch)
			case containsAll(args, "branch", "--list", baseBranch):
				if epicBranchCreated {
					return []byte("  " + baseBranch + "\n"), nil
				}
				return nil, nil
			case containsAll(args, "branch", baseBranch, "main"):
				epicBranchCreated = true
				return nil, nil
			case containsAll(args, "worktree", "add"):
				if !epicBranchCreated {
					return nil, fmt.Errorf("fatal: not a valid object name: '%s'", baseBranch)
				}
				return nil, nil
			default:
				return nil, nil
			}
		},
	}
	mgr := NewGitWorktreeManager(repoRoot, "", "", runner)

	path, branch, err := mgr.Create(context.Background(), beadID, baseBranch)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if path != filepath.Join(repoRoot, ".worktrees", beadID) {
		t.Fatalf("path = %q", path)
	}
	if branch != "agent/"+beadID {
		t.Fatalf("branch = %q", branch)
	}

	var sawCreateBranch, sawWorktreeAdd bool
	for _, call := range runner.calls {
		if containsAll(call.Args, "branch", baseBranch, "main") {
			sawCreateBranch = true
		}
		if containsAll(call.Args, "worktree", "add") {
			sawWorktreeAdd = true
			if call.Args[len(call.Args)-1] != baseBranch {
				t.Fatalf("worktree add base = %q, want %q", call.Args[len(call.Args)-1], baseBranch)
			}
		}
	}
	if !sawCreateBranch {
		t.Fatalf("expected missing epic branch %q to be created from main; calls=%v", baseBranch, runner.calls)
	}
	if !sawWorktreeAdd {
		t.Fatalf("expected worktree add call; calls=%v", runner.calls)
	}
}

// TestGitWorktreeManager_Create_PathContainsBeadID kills mutant .go.7:
// "path = filepath.Join(...)" assignment removed.
// Verifies the git worktree add command receives the constructed path.
func TestGitWorktreeManager_Create_PathContainsBeadID(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/my/repo", "", "", runner)

	path, _, err := mgr.Create(context.Background(), "oro-xyz.5", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantPath := "/my/repo/.worktrees/oro-xyz.5"
	if path != wantPath {
		t.Fatalf("returned path: got %q, want %q", path, wantPath)
	}

	// Fetch and comparison precede the git worktree add call.
	if len(runner.calls) < 4 {
		t.Fatal("expected fetch, comparison, and worktree add calls")
	}
	args := runner.calls[3].Args
	foundPath := false
	for _, a := range args {
		if a == wantPath {
			foundPath = true
			break
		}
	}
	if !foundPath {
		t.Fatalf("git worktree add args %v must contain path %q", args, wantPath)
	}
}

// TestGitWorktreeManager_Create_BranchContainsBeadID kills mutant .go.8:
// "branch = protocol.BranchPrefix + beadID" assignment removed.
// Verifies the git worktree add command receives the constructed branch name.
func TestGitWorktreeManager_Create_BranchContainsBeadID(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/my/repo", "", "", runner)

	_, branch, err := mgr.Create(context.Background(), "oro-xyz.5", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantBranch := "agent/oro-xyz.5"
	if branch != wantBranch {
		t.Fatalf("returned branch: got %q, want %q", branch, wantBranch)
	}

	// Fetch and comparison precede the git worktree add call.
	if len(runner.calls) < 4 {
		t.Fatal("expected fetch, comparison, and worktree add calls")
	}
	args := runner.calls[3].Args
	for i, a := range args {
		if a == "-b" && i+1 < len(args) {
			if args[i+1] != wantBranch {
				t.Fatalf("-b flag: got %q, want %q", args[i+1], wantBranch)
			}
			return
		}
	}
	t.Fatalf("git args %v do not contain '-b %s'", args, wantBranch)
}

// TestGitWorktreeManager_Remove_ErrorWrapsPath kills mutant .go.4:
// "return fmt.Errorf(...)" in Remove replaced by no-op.
// Verifies the error from Remove mentions the path that failed.
func TestGitWorktreeManager_Remove_ErrorWrapsPath(t *testing.T) {
	// Create a temporary directory to simulate an existing worktree
	tmpDir := t.TempDir()
	worktreePath := filepath.Join(tmpDir, "failing-bead")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("failed to create temp worktree: %v", err)
	}

	runner := &mockCommandRunner{err: fmt.Errorf("not a worktree")}
	mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

	err := mgr.Remove(context.Background(), worktreePath)
	if err == nil {
		t.Fatal("expected error from Remove with failing runner")
	}

	// Error must include the path so callers can identify which worktree failed.
	if !strings.Contains(err.Error(), worktreePath) {
		t.Fatalf("error %q should contain path %q", err.Error(), worktreePath)
	}
}

func TestGitWorktreeManager_PrunePreservesLocalEntries(t *testing.T) {
	tmpDir := t.TempDir()
	worktreesDir := filepath.Join(tmpDir, ".worktrees")
	if err := os.MkdirAll(worktreesDir, 0o750); err != nil {
		t.Fatalf("mkdir .worktrees: %v", err)
	}

	// Create a plain file inside .worktrees/ (should be preserved).
	keepFile := filepath.Join(worktreesDir, "README")
	if err := os.WriteFile(keepFile, []byte("keep this\n"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}

	// Create an orphan directory (should be preserved).
	orphanDir := filepath.Join(worktreesDir, "old-bead")
	if err := os.MkdirAll(orphanDir, 0o750); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}

	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

	if err := mgr.Prune(context.Background()); err != nil {
		t.Fatalf("Prune returned error: %v", err)
	}

	// The file must still exist.
	if _, err := os.Stat(keepFile); os.IsNotExist(err) {
		t.Fatalf("Prune removed non-dir file %q — should have been skipped", keepFile)
	}

	if _, err := os.Stat(orphanDir); err != nil {
		t.Fatalf("Prune removed orphan dir %q: %v", orphanDir, err)
	}
}

// TestGitWorktreeManager_Prune_NoWorktreesDirReturnsNil kills mutant .go.5:
// "return nil" removed when ReadDir fails — would fall through and return nil anyway,
// but this test pins the explicit early-return behaviour.
func TestGitWorktreeManager_Prune_NoWorktreesDirReturnsNilNoOtherCalls(t *testing.T) {
	tmpDir := t.TempDir()
	// Intentionally no .worktrees/ directory.

	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

	err := mgr.Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune with missing .worktrees should return nil, got: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("expected exactly 1 git call (worktree prune), got %d", len(runner.calls))
	}
}

func TestGitWorktreeManager_PruneDoesNotRemoveOrphanDirs(t *testing.T) {
	tmpDir := t.TempDir()
	worktreesDir := filepath.Join(tmpDir, ".worktrees")
	orphanDir := filepath.Join(worktreesDir, "single-orphan")
	if err := os.MkdirAll(orphanDir, 0o750); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}
	// Put a nested file to ensure RemoveAll is needed, not just Rmdir.
	if err := os.WriteFile(filepath.Join(orphanDir, "HEAD"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}

	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

	if err := mgr.Prune(context.Background()); err != nil {
		t.Fatalf("Prune error: %v", err)
	}

	if _, err := os.Stat(orphanDir); err != nil {
		t.Fatalf("orphan dir %q should be preserved by Prune: %v", orphanDir, err)
	}
}

// TestGitWorktreeManager_PruneStale_CallsWorktreeRemove kills mutant .go.13 (prune call) and .go.14 (branch delete):
// verifies that pruneStale (invoked via Create retry) calls:
//   - git worktree remove <path> --force
//   - git worktree prune
//   - git merge-base --is-ancestor <branch> <target>
//   - git branch -D <branch>
func TestGitWorktreeManager_PruneStale_CommandSequence(t *testing.T) {
	callCount := 0
	worktreeAddFailed := false
	runner := &mockCommandRunner{
		callFn: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			callCount++
			if containsAll(args, "worktree", "add") && !worktreeAddFailed {
				worktreeAddFailed = true
				return nil, fmt.Errorf("fatal: a branch named 'agent/seq-bead' already exists")
			}
			return nil, nil
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	_, _, err := mgr.Create(context.Background(), "seq-bead", "main")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if len(runner.calls) != 10 {
		var descs []string
		for i, c := range runner.calls {
			descs = append(descs, fmt.Sprintf("[%d] %s %s", i, c.Name, strings.Join(c.Args, " ")))
		}
		t.Fatalf("expected 10 calls, got %d:\n%s", len(runner.calls), strings.Join(descs, "\n"))
	}

	// call[4]: git worktree remove <path> --force  (pruneStale step 1)
	c1 := runner.calls[4]
	if !containsAll(c1.Args, "worktree", "remove", "--force") {
		t.Fatalf("call[2] should be git worktree remove --force, got: %v", c1.Args)
	}
	wantPath := "/repo/root/.worktrees/seq-bead"
	if !containsAll(c1.Args, wantPath) {
		t.Fatalf("call[2] must include path %q, got: %v", wantPath, c1.Args)
	}

	// call[5]: git worktree prune  (kills .go.13)
	c2 := runner.calls[5]
	if !containsAll(c2.Args, "worktree", "prune") {
		t.Fatalf("call[3] should be git worktree prune, got: %v", c2.Args)
	}
	if containsAll(c2.Args, "remove") {
		t.Fatalf("call[3] must be 'worktree prune', not 'worktree remove', got: %v", c2.Args)
	}

	// call[6]: git merge-base --is-ancestor agent/seq-bead main
	c3 := runner.calls[6]
	if !slices.Equal(c3.Args, []string{"-C", "/repo/root", "merge-base", "--is-ancestor", "agent/seq-bead", "main"}) {
		t.Fatalf("call[6] should prove agent/seq-bead merged into main, got: %v", c3.Args)
	}

	// call[7]: git branch -D agent/seq-bead after proof (kills .go.14)
	c4 := runner.calls[7]
	if !slices.Equal(c4.Args, []string{"-C", "/repo/root", "branch", "-D", "agent/seq-bead"}) {
		t.Fatalf("call[7] should be git branch -D agent/seq-bead, got: %v", c4.Args)
	}
}

// TestGitWorktreeManager_Create_PruneStaleCalledOnAlreadyExists kills mutant .go.10:
// "g.pruneStale(ctx, path, branch, targetBranch)" call removed — retry happens but cleanup is skipped.
// Verifies that when "already exists" fires, at least 4 extra calls are made
// (remove --force, prune, target proof, branch -D, retry add) before success.
func TestGitWorktreeManager_Create_PruneStaleCalledOnAlreadyExists(t *testing.T) {
	callCount := 0
	worktreeAddFailed := false
	runner := &mockCommandRunner{
		callFn: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			callCount++
			if containsAll(args, "worktree", "add") && !worktreeAddFailed {
				worktreeAddFailed = true
				return nil, fmt.Errorf("fatal: a branch named 'agent/prune-bead' already exists")
			}
			return nil, nil
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	path, branch, err := mgr.Create(context.Background(), "prune-bead", "main")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if path != "/repo/root/.worktrees/prune-bead" {
		t.Fatalf("path: got %q", path)
	}
	if branch != "agent/prune-bead" {
		t.Fatalf("branch: got %q", branch)
	}

	// With base comparison and pruneStale, ten calls occur.
	if len(runner.calls) != 10 {
		var descs []string
		for i, c := range runner.calls {
			descs = append(descs, fmt.Sprintf("[%d] %s %v", i, c.Name, c.Args))
		}
		t.Fatalf("expected 10 calls (fetch, comparison, initial add, remove --force, prune, proof, branch -D, retry add, stage-assets), got %d:\n%s",
			len(runner.calls), strings.Join(descs, "\n"))
	}

	// Verify calls[4..7] contain the stale cleanup operations.
	hasWorktreeRemove := containsAll(runner.calls[4].Args, "worktree", "remove", "--force")
	hasWorktreePrune := containsAll(runner.calls[5].Args, "worktree", "prune")
	hasProof := slices.Equal(runner.calls[6].Args, []string{"-C", "/repo/root", "merge-base", "--is-ancestor", "agent/prune-bead", "main"})
	hasBranchDelete := slices.Equal(runner.calls[7].Args, []string{"-C", "/repo/root", "branch", "-D", "agent/prune-bead"})

	if !hasWorktreeRemove || !hasWorktreePrune || !hasProof || !hasBranchDelete {
		t.Fatalf("pruneStale sequence not found: remove=%v prune=%v proof=%v branch-D=%v",
			hasWorktreeRemove, hasWorktreePrune, hasProof, hasBranchDelete)
	}
}

func TestWorktreeManager_PruneStaleUnlocksAndRemovesBeforeRetry(t *testing.T) {
	// When a worktree is locked (e.g., from a crash), pruneStale must:
	// 1. Remove the worktree directory via `git worktree remove --force`
	// 2. Prune stale worktree metadata
	// 3. Delete the branch
	// This ensures that a locked worktree with stale git metadata doesn't
	// block branch deletion, causing an infinite retry loop.
	callCount := 0
	worktreeAddFailed := false
	runner := &mockCommandRunner{
		callFn: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			callCount++
			// Call 1: git fetch (succeeds → effectiveBase = origin/main)
			// Call 2: git worktree add fails with "already exists"
			if containsAll(args, "worktree", "add") && !worktreeAddFailed {
				worktreeAddFailed = true
				return nil, fmt.Errorf("fatal: a branch named 'agent/oro-locked' already exists")
			}
			// All subsequent calls succeed
			return nil, nil
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	path, branch, err := mgr.Create(context.Background(), "oro-locked", "main")
	if err != nil {
		t.Fatalf("expected Create to succeed after stale cleanup, got: %v", err)
	}

	wantPath := "/repo/root/.worktrees/oro-locked"
	if path != wantPath {
		t.Fatalf("path: got %q, want %q", path, wantPath)
	}
	wantBranch := "agent/oro-locked"
	if branch != wantBranch {
		t.Fatalf("branch: got %q, want %q", branch, wantBranch)
	}

	// Verify the cleanup sequence includes worktree remove --force before prune.
	// Expected calls:
	// 0. git fetch origin main (best-effort, succeeds)
	// 1. git worktree add (fails)
	// 2. git worktree remove <path> --force
	// 3. git worktree prune
	// 4. git merge-base --is-ancestor <branch> origin/main
	// 5. git branch -D <branch>
	// 6. git worktree add (succeeds)
	// 7. make stage-assets (best-effort)
	if len(runner.calls) != 10 {
		var callDescs []string
		for i, c := range runner.calls {
			callDescs = append(callDescs, fmt.Sprintf("  [%d] %s %s", i, c.Name, strings.Join(c.Args, " ")))
		}
		t.Fatalf("expected 10 command calls, got %d:\n%s", len(runner.calls), strings.Join(callDescs, "\n"))
	}

	// Call 2 (index 2): git worktree remove <path> --force
	c2 := runner.calls[4]
	if c2.Name != "git" || !containsAll(c2.Args, "worktree", "remove", "--force") {
		t.Fatalf("call[2] should be git worktree remove --force, got: %s %v", c2.Name, c2.Args)
	}
	if !containsAll(c2.Args, wantPath) {
		t.Fatalf("call[2] should include worktree path %q, got: %v", wantPath, c2.Args)
	}

	// Call 3 (index 3): git worktree prune
	c3 := runner.calls[5]
	if c3.Name != "git" || !containsAll(c3.Args, "worktree", "prune") {
		t.Fatalf("call[3] should be git worktree prune, got: %s %v", c3.Name, c3.Args)
	}

	// Call 4 (index 4): git merge-base --is-ancestor <branch> origin/main
	c4 := runner.calls[6]
	if c4.Name != "git" || !slices.Equal(c4.Args, []string{"-C", "/repo/root", "merge-base", "--is-ancestor", wantBranch, "main"}) {
		t.Fatalf("call[6] should prove branch merged into main, got: %s %v", c4.Name, c4.Args)
	}

	// Call 5 (index 5): git branch -D <branch> after proof
	c5 := runner.calls[7]
	if c5.Name != "git" || !slices.Equal(c5.Args, []string{"-C", "/repo/root", "branch", "-D", wantBranch}) {
		t.Fatalf("call[5] should be git branch -D after proof, got: %s %v", c5.Name, c5.Args)
	}
}

func TestGitWorktreeManager_DeleteBranch_Success(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	err := mgr.DeleteBranch(context.Background(), "agent/oro-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 command call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.Name != "git" {
		t.Fatalf("name: got %q, want %q", call.Name, "git")
	}
	wantArgs := []string{"-C", "/repo/root", "branch", "-d", "agent/oro-test"}
	if len(call.Args) != len(wantArgs) {
		t.Fatalf("args: got %v, want %v", call.Args, wantArgs)
	}
	for i, a := range call.Args {
		if a != wantArgs[i] {
			t.Fatalf("args[%d]: got %q, want %q", i, a, wantArgs[i])
		}
	}
}

func TestGitWorktreeManager_DeleteBranch_Error(t *testing.T) {
	runner := &mockCommandRunner{
		err: fmt.Errorf("error: branch 'agent/oro-missing' not found"),
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	err := mgr.DeleteBranch(context.Background(), "agent/oro-missing")
	if err == nil {
		t.Fatal("expected error from DeleteBranch")
	}
	if !strings.Contains(err.Error(), "branch delete") {
		t.Fatalf("error should mention 'branch delete', got: %v", err)
	}
	if !strings.Contains(err.Error(), "agent/oro-missing") {
		t.Fatalf("error should contain branch name, got: %v", err)
	}
}

func TestGitWorktreeManager_Create_InvalidBeadID(t *testing.T) {
	t.Parallel()

	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	tests := []struct {
		name   string
		beadID string
	}{
		{"path_traversal_parent", "../etc"},
		{"path_traversal_double", "../../etc"},
		{"absolute_path", "/etc/passwd"},
		{"backslash", "oro\\test"},
		{"special_chars", "oro@test"},
		{"empty", ""},
		{"uppercase", "ORO-1NF"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := mgr.Create(context.Background(), tt.beadID, "main")
			if err == nil {
				t.Fatalf("Create with invalid bead ID %q should return error", tt.beadID)
			}
			if !strings.Contains(err.Error(), "invalid bead ID") {
				t.Fatalf("error should mention 'invalid bead ID', got: %v", err)
			}

			// Verify git command was never called for invalid IDs.
			if len(runner.calls) > 0 {
				t.Fatalf("expected no git commands for invalid bead ID, got %d calls", len(runner.calls))
			}
		})
	}
}

func TestGitWorktreeManager_Create_RunsStageAssets(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	_, _, err := mgr.Create(context.Background(), "abc123", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Fetch and base comparison precede git worktree add and make stage-assets.
	if len(runner.calls) != 5 {
		t.Fatalf("expected 5 command calls, got %d: %v", len(runner.calls), runner.calls)
	}

	stageCall := runner.calls[4]
	if stageCall.Name != "make" {
		t.Fatalf("third call name: got %q, want %q", stageCall.Name, "make")
	}
	wantArgs := []string{"-C", "/repo/root/.worktrees/abc123", "stage-assets"}
	if !slices.Equal(stageCall.Args, wantArgs) {
		t.Fatalf("stage-assets args: got %v, want %v", stageCall.Args, wantArgs)
	}
}

func TestGitWorktreeManager_Create_StageAssetsFailureNonFatal(t *testing.T) {
	callCount := 0
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			callCount++
			if callCount == 5 {
				return nil, fmt.Errorf("make: *** No rule to make target 'stage-assets'")
			}
			return nil, nil
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	path, branch, err := mgr.Create(context.Background(), "abc123", "main")
	if err != nil {
		t.Fatalf("Create should succeed even if stage-assets fails: %v", err)
	}
	if path == "" || branch == "" {
		t.Fatal("path and branch should be non-empty")
	}
}

// TestPruneStaleReturnsFirstError verifies:
//  1. pruneStale returns the first non-nil error from any git step.
//  2. Create() logs a worktree_create_prune_failed slog event when pruneStale
//     returns non-nil, and still retries (does not abort).
func TestPruneStaleReturnsFirstError(t *testing.T) {
	// Subtest 1: first step (worktree remove) fails → that error is returned.
	t.Run("worktree_remove_fails_returns_error", func(t *testing.T) {
		callCount := 0
		runner := &mockCommandRunner{
			callFn: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				callCount++
				if callCount == 1 {
					return nil, fmt.Errorf("fatal: worktree is locked")
				}
				return nil, nil
			},
		}
		mgr := NewGitWorktreeManager("/repo/root", "", "", runner)
		err := mgr.pruneStale(context.Background(), "/repo/root/.worktrees/prune-err", "agent/prune-err", "main")
		if err == nil {
			t.Fatal("expected pruneStale to return error when worktree remove fails")
		}
		if !strings.Contains(err.Error(), "worktree is locked") {
			t.Fatalf("expected error to contain 'worktree is locked', got: %v", err)
		}
	})

	// Subtest 2: only the proof step fails -> that error is returned.
	t.Run("branch_delete_fails_returns_error", func(t *testing.T) {
		callCount := 0
		runner := &mockCommandRunner{
			callFn: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				callCount++
				if callCount == 3 { // worktree remove=1, prune=2, merge-base proof=3
					return nil, fmt.Errorf("error: branch not yet merged")
				}
				return nil, nil
			},
		}
		mgr := NewGitWorktreeManager("/repo/root", "", "", runner)
		err := mgr.pruneStale(context.Background(), "/repo/root/.worktrees/prune-err", "agent/prune-err", "main")
		if err == nil {
			t.Fatal("expected pruneStale to return error when target proof fails")
		}
		if !strings.Contains(err.Error(), "branch not yet merged") {
			t.Fatalf("expected error about 'branch not yet merged', got: %v", err)
		}
	})

	// Subtest 3: Create() logs worktree_create_prune_failed and still retries.
	t.Run("create_logs_event_and_retries_on_prune_failure", func(t *testing.T) {
		// Redirect slog default logger to capture output.
		var logBuf bytes.Buffer
		h := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})
		origLogger := slog.Default()
		slog.SetDefault(slog.New(h))
		defer slog.SetDefault(origLogger)

		worktreeAddFailed := false
		runner := &mockCommandRunner{
			callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if containsAll(args, "worktree", "add") && !worktreeAddFailed {
					worktreeAddFailed = true
					return nil, fmt.Errorf("fatal: a branch named 'agent/retry-bead' already exists")
				}
				if containsAll(args, "worktree", "remove", "--force") {
					return nil, fmt.Errorf("fatal: worktree is locked")
				}
				return nil, nil
			},
		}
		mgr := NewGitWorktreeManager("/repo/root", "", "", runner)
		_, _, err := mgr.Create(context.Background(), "retry-bead", "main")
		if err != nil {
			t.Fatalf("Create should succeed after prune failure (retry works): %v", err)
		}

		// Verify the worktree_create_prune_failed event was logged.
		if !strings.Contains(logBuf.String(), "worktree_create_prune_failed") {
			t.Fatalf("expected worktree_create_prune_failed to be logged, got: %q", logBuf.String())
		}

		worktreeAddCalls := 0
		sawBranchCheck := false
		for _, call := range runner.calls {
			if containsAll(call.Args, "worktree", "add") {
				worktreeAddCalls++
			}
			if containsAll(call.Args, "branch", "--list", "agent/retry-bead") {
				sawBranchCheck = true
			}
		}
		if worktreeAddCalls != 2 {
			t.Fatalf("worktree add calls = %d, want initial attempt and retry", worktreeAddCalls)
		}
		if !sawBranchCheck {
			t.Fatal("expected branch existence check before retry")
		}
	})
}

func TestBranchExists(t *testing.T) {
	t.Run("existing_branch_returns_true", func(t *testing.T) {
		runner := &mockCommandRunner{output: []byte("  agent/abc123\n")}
		mgr := NewGitWorktreeManager("/repo", "", "", runner)

		exists, err := mgr.BranchExists(context.Background(), "agent/abc123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !exists {
			t.Fatal("expected BranchExists to return true for existing branch")
		}
	})

	t.Run("missing_branch_returns_false", func(t *testing.T) {
		runner := &mockCommandRunner{output: []byte("")}
		mgr := NewGitWorktreeManager("/repo", "", "", runner)

		exists, err := mgr.BranchExists(context.Background(), "agent/missing")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if exists {
			t.Fatal("expected BranchExists to return false for missing branch")
		}
	})

	t.Run("git_error_propagated", func(t *testing.T) {
		runner := &mockCommandRunner{err: fmt.Errorf("not a git repo")}
		mgr := NewGitWorktreeManager("/repo", "", "", runner)

		_, err := mgr.BranchExists(context.Background(), "main")
		if err == nil {
			t.Fatal("expected error from BranchExists on git failure")
		}
	})

	t.Run("calls_git_branch_list_with_branch_name", func(t *testing.T) {
		runner := &mockCommandRunner{output: []byte("  agent/abc123\n")}
		mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

		_, err := mgr.BranchExists(context.Background(), "agent/abc123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(runner.calls) != 1 {
			t.Fatalf("expected 1 git call, got %d", len(runner.calls))
		}
		call := runner.calls[0]
		if call.Name != "git" {
			t.Fatalf("expected 'git', got %q", call.Name)
		}
		if !containsAll(call.Args, "branch", "--list", "agent/abc123") {
			t.Fatalf("expected args to contain branch --list agent/abc123, got %v", call.Args)
		}
	})
}

func TestCurrentBranch(t *testing.T) {
	runner := &mockCommandRunner{output: []byte("agent/oro-current\n")}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	got, err := mgr.CurrentBranch(context.Background(), "/repo/root/.worktrees/oro-current")
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if got != "agent/oro-current" {
		t.Fatalf("CurrentBranch = %q, want agent/oro-current", got)
	}
	if len(runner.calls) != 1 || !containsAll(runner.calls[0].Args, "-C", "/repo/root/.worktrees/oro-current", "rev-parse", "--abbrev-ref", "HEAD") {
		t.Fatalf("CurrentBranch git call = %+v", runner.calls)
	}
}

func TestMergeFFOnly(t *testing.T) {
	t.Run("success_returns_trimmed_sha", func(t *testing.T) {
		wantSHA := "abc123def456abc123def456abc123def456abc123"
		callCount := 0
		runner := &mockCommandRunner{
			callFn: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				callCount++
				if callCount == 2 {
					return []byte(wantSHA + "\n"), nil
				}
				return nil, nil
			},
		}
		mgr := NewGitWorktreeManager("/repo", "", "", runner)

		sha, err := mgr.MergeFFOnly(context.Background(), "agent/abc", "/repo")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sha != wantSHA {
			t.Fatalf("sha: got %q, want %q", sha, wantSHA)
		}
	})

	t.Run("calls_merge_ff_only_then_rev_parse_head", func(t *testing.T) {
		callCount := 0
		runner := &mockCommandRunner{
			callFn: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				callCount++
				if callCount == 2 {
					return []byte("deadbeef\n"), nil
				}
				return nil, nil
			},
		}
		mgr := NewGitWorktreeManager("/repo", "", "", runner)

		_, err := mgr.MergeFFOnly(context.Background(), "agent/abc", "/repo")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(runner.calls) != 2 {
			t.Fatalf("expected 2 git calls, got %d: %v", len(runner.calls), runner.calls)
		}

		mergeCall := runner.calls[0]
		if mergeCall.Name != "git" {
			t.Fatalf("call[0]: expected 'git', got %q", mergeCall.Name)
		}
		if !containsAll(mergeCall.Args, "merge", "--ff-only", "agent/abc") {
			t.Fatalf("call[0]: expected merge --ff-only agent/abc, got %v", mergeCall.Args)
		}

		revParseCall := runner.calls[1]
		if !containsAll(revParseCall.Args, "rev-parse", "HEAD") {
			t.Fatalf("call[1]: expected rev-parse HEAD, got %v", revParseCall.Args)
		}
	})

	t.Run("not_ff_returns_error", func(t *testing.T) {
		runner := &mockCommandRunner{err: fmt.Errorf("fatal: not possible to fast-forward, aborting")}
		mgr := NewGitWorktreeManager("/repo", "", "", runner)

		_, err := mgr.MergeFFOnly(context.Background(), "agent/abc", "/repo")
		if err == nil {
			t.Fatal("expected error when ff-only merge fails")
		}
	})

	t.Run("target_dir_passed_to_git_c", func(t *testing.T) {
		callCount := 0
		runner := &mockCommandRunner{
			callFn: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				callCount++
				if callCount == 2 {
					return []byte("deadbeef\n"), nil
				}
				return nil, nil
			},
		}
		mgr := NewGitWorktreeManager("/repo", "", "", runner)

		_, err := mgr.MergeFFOnly(context.Background(), "agent/abc", "/primary/repo")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify target dir is passed as -C argument.
		if len(runner.calls) < 1 {
			t.Fatal("expected at least 1 git call")
		}
		if !containsAll(runner.calls[0].Args, "-C", "/primary/repo") {
			t.Fatalf("expected -C /primary/repo in args, got %v", runner.calls[0].Args)
		}
	})
}

func TestUpdateBranchRefRequiresFastForward(t *testing.T) {
	var calls []string
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			call := strings.Join(args, " ")
			calls = append(calls, call)
			switch call {
			case "-C /repo/root merge-base --is-ancestor epic/parent epic/child":
				return nil, fmt.Errorf("exit status 1")
			case "-C /repo/root update-ref refs/heads/epic/parent epic/child":
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected git call: %s", call)
			}
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	err := mgr.UpdateBranchRef(context.Background(), "epic/parent", "epic/child")
	if err == nil {
		t.Fatal("expected non-fast-forward UpdateBranchRef to fail")
	}
	if containsCall(calls, "-C /repo/root update-ref refs/heads/epic/parent epic/child") {
		t.Fatalf("UpdateBranchRef moved non-ancestor target; calls=%v", calls)
	}
}

func TestWorktreeManager_CustomDir(t *testing.T) {
	t.Run("create_uses_custom_worktrees_dir", func(t *testing.T) {
		runner := &mockCommandRunner{}
		customDir := "/custom/worktrees"
		mgr := NewGitWorktreeManager("/repo/root", customDir, "", runner)

		path, _, err := mgr.Create(context.Background(), "abc123", "main")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantPath := "/custom/worktrees/abc123"
		if path != wantPath {
			t.Fatalf("Create path: got %q, want %q", path, wantPath)
		}
		// worktree add is at index 1 (index 0 is the best-effort fetch)
		if len(runner.calls) < 4 {
			t.Fatal("expected fetch, comparison, and worktree add calls")
		}
		if !containsAll(runner.calls[3].Args, wantPath) {
			t.Fatalf("git worktree add args should contain custom path %q, got: %v", wantPath, runner.calls[3].Args)
		}
	})

	t.Run("prune_uses_custom_worktrees_dir", func(t *testing.T) {
		tmpCustom := t.TempDir()
		orphanDir := filepath.Join(tmpCustom, "old-bead")
		if err := os.MkdirAll(orphanDir, 0o750); err != nil {
			t.Fatalf("mkdir orphan: %v", err)
		}
		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager("/repo/root", tmpCustom, "", runner)

		if err := mgr.Prune(context.Background()); err != nil {
			t.Fatalf("Prune returned error: %v", err)
		}
		if _, err := os.Stat(orphanDir); err != nil {
			t.Fatalf("Prune should preserve orphan dir in custom worktrees dir: %v", err)
		}
	})

	t.Run("gc_closed_worktrees_uses_custom_dir", func(t *testing.T) {
		tmpCustom := t.TempDir()
		closedDir := filepath.Join(tmpCustom, "oro-closed1")
		if err := os.MkdirAll(closedDir, 0o750); err != nil {
			t.Fatalf("mkdir closed bead: %v", err)
		}
		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager("/repo/root", tmpCustom, "", runner)

		err := mgr.GCClosedWorktrees(context.Background(), func(id string) bool { return id == "oro-closed1" })
		if err != nil {
			t.Fatalf("GCClosedWorktrees returned error: %v", err)
		}
		branchDeleteCalled := false
		for _, call := range runner.calls {
			if call.Name == "git" && containsAll(call.Args, "branch", "-d", "agent/oro-closed1") {
				branchDeleteCalled = true
			}
		}
		if !branchDeleteCalled {
			t.Fatal("expected branch delete to be called for closed bead in custom dir")
		}
	})

	t.Run("empty_worktrees_dir_defaults_to_repo_root_dotworktrees", func(t *testing.T) {
		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager("/my/repo", "", "", runner)

		path, _, err := mgr.Create(context.Background(), "abc123", "main")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantPath := "/my/repo/.worktrees/abc123"
		if path != wantPath {
			t.Fatalf("default path: got %q, want %q", path, wantPath)
		}
	})
}

func TestGCClosedWorktrees(t *testing.T) {
	t.Run("removes_closed_worktree_and_branch", func(t *testing.T) {
		tmpDir := t.TempDir()
		worktreesDir := filepath.Join(tmpDir, ".worktrees")
		if err := os.MkdirAll(worktreesDir, 0o750); err != nil {
			t.Fatalf("mkdir .worktrees: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(worktreesDir, "oro-closed1"), 0o750); err != nil {
			t.Fatalf("mkdir closed bead: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(worktreesDir, "oro-open1"), 0o750); err != nil {
			t.Fatalf("mkdir open bead: %v", err)
		}

		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

		closedBeads := map[string]bool{"oro-closed1": true}
		err := mgr.GCClosedWorktrees(context.Background(), func(id string) bool { return closedBeads[id] })
		if err != nil {
			t.Fatalf("GCClosedWorktrees returned error: %v", err)
		}

		closedPath := filepath.Join(worktreesDir, "oro-closed1")
		openPath := filepath.Join(worktreesDir, "oro-open1")

		worktreeRemoveCalled := false
		branchDeleteCalled := false
		for _, call := range runner.calls {
			if call.Name != "git" {
				continue
			}
			if containsAll(call.Args, "worktree", "remove") && slices.Contains(call.Args, closedPath) {
				worktreeRemoveCalled = true
			}
			if containsAll(call.Args, "branch", "-d", "agent/oro-closed1") {
				branchDeleteCalled = true
			}
			if slices.Contains(call.Args, openPath) {
				t.Fatalf("should not process open bead path, got call: %v", call.Args)
			}
			if containsAll(call.Args, "branch", "-d", "agent/oro-open1") {
				t.Fatalf("should not delete open bead branch, got call: %v", call.Args)
			}
		}
		if !worktreeRemoveCalled {
			t.Fatal("expected git worktree remove to be called for closed bead")
		}
		if !branchDeleteCalled {
			t.Fatal("expected git branch -d to be called for closed bead")
		}
	})

	t.Run("no_worktrees_dir_returns_nil", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Do NOT create .worktrees/
		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

		err := mgr.GCClosedWorktrees(context.Background(), func(_ string) bool { return true })
		if err != nil {
			t.Fatalf("expected nil when .worktrees/ missing, got: %v", err)
		}
	})

	t.Run("remove_failure_continues_to_next_bead", func(t *testing.T) {
		tmpDir := t.TempDir()
		worktreesDir := filepath.Join(tmpDir, ".worktrees")
		if err := os.MkdirAll(worktreesDir, 0o750); err != nil {
			t.Fatalf("mkdir .worktrees: %v", err)
		}
		for _, name := range []string{"oro-fail1", "oro-ok1"} {
			if err := os.MkdirAll(filepath.Join(worktreesDir, name), 0o750); err != nil {
				t.Fatalf("mkdir %s: %v", name, err)
			}
		}

		failPath := filepath.Join(worktreesDir, "oro-fail1")
		runner := &mockCommandRunner{
			callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "git" && slices.Contains(args, "remove") && slices.Contains(args, failPath) {
					return nil, fmt.Errorf("worktree remove failed")
				}
				return nil, nil
			},
		}
		mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

		err := mgr.GCClosedWorktrees(context.Background(), func(_ string) bool { return true })
		if err != nil {
			t.Fatalf("GCClosedWorktrees should not return error on remove failure, got: %v", err)
		}

		// oro-ok1 should still be processed even after oro-fail1 failed.
		branchDeleteOK1 := false
		for _, call := range runner.calls {
			if call.Name == "git" && containsAll(call.Args, "branch", "-d", "agent/oro-ok1") {
				branchDeleteOK1 = true
			}
		}
		if !branchDeleteOK1 {
			t.Fatal("expected git branch -d to be called for oro-ok1 after oro-fail1 remove failure")
		}
	})

	t.Run("skips_non_directory_entries", func(t *testing.T) {
		tmpDir := t.TempDir()
		worktreesDir := filepath.Join(tmpDir, ".worktrees")
		if err := os.MkdirAll(worktreesDir, 0o750); err != nil {
			t.Fatalf("mkdir .worktrees: %v", err)
		}
		if err := os.WriteFile(filepath.Join(worktreesDir, "not-a-dir.txt"), []byte("data"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}

		isClosedCalled := false
		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

		err := mgr.GCClosedWorktrees(context.Background(), func(_ string) bool {
			isClosedCalled = true
			return true
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if isClosedCalled {
			t.Fatal("should not call isBeadClosed for non-directory entries")
		}
	})

	t.Run("open_bead_not_removed", func(t *testing.T) {
		tmpDir := t.TempDir()
		worktreesDir := filepath.Join(tmpDir, ".worktrees")
		if err := os.MkdirAll(worktreesDir, 0o750); err != nil {
			t.Fatalf("mkdir .worktrees: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(worktreesDir, "oro-open1"), 0o750); err != nil {
			t.Fatalf("mkdir open bead: %v", err)
		}

		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager(tmpDir, "", "", runner)

		err := mgr.GCClosedWorktrees(context.Background(), func(_ string) bool { return false })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for _, call := range runner.calls {
			if call.Name == "git" && (containsAll(call.Args, "worktree", "remove") || containsAll(call.Args, "branch", "-d")) {
				t.Fatalf("should not call remove/delete-branch for open bead, got: %v", call.Args)
			}
		}
	})
}

func TestGitWorktreeManager_LinkQualityGate(t *testing.T) {
	t.Run("copies_snapshot_when_neither_file_exists", func(t *testing.T) {
		worktree := t.TempDir()
		targetDir := t.TempDir()
		target := filepath.Join(targetDir, "quality_gate.sh")
		if err := os.WriteFile(target, []byte("#!/bin/sh\necho original\n"), 0o755); err != nil {
			t.Fatalf("create target: %v", err)
		}

		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager("/repo", "", target, runner)
		mgr.linkQualityGate(context.Background(), worktree)

		link := filepath.Join(worktree, "quality_gate.sh")
		info, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("expected quality gate at %s, got error: %v", link, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("quality gate must be an isolated copy, got symlink")
		}
		if err := os.WriteFile(target, []byte("#!/bin/sh\necho mutated\n"), 0o755); err != nil {
			t.Fatalf("mutate target: %v", err)
		}
		got, err := os.ReadFile(link)
		if err != nil {
			t.Fatalf("read copied quality gate: %v", err)
		}
		if strings.Contains(string(got), "mutated") || !strings.Contains(string(got), "original") {
			t.Fatalf("quality gate copy changed after target mutation:\n%s", got)
		}
	})

	t.Run("copies_snapshot_when_scripts_quality_gate_exists", func(t *testing.T) {
		worktree := t.TempDir()
		scriptsDir := filepath.Join(worktree, "scripts")
		if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
			t.Fatalf("mkdir scripts: %v", err)
		}
		if err := os.WriteFile(filepath.Join(scriptsDir, "quality_gate.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("create existing script: %v", err)
		}

		targetDir := t.TempDir()
		target := filepath.Join(targetDir, "quality_gate.sh")
		if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("create target: %v", err)
		}

		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager("/repo", "", target, runner)
		mgr.linkQualityGate(context.Background(), worktree)

		link := filepath.Join(worktree, "quality_gate.sh")
		info, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("expected quality gate at %s, got error: %v", link, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("quality gate must be an isolated copy, got symlink")
		}
	})

	t.Run("replaces_stale_symlink_with_snapshot", func(t *testing.T) {
		worktree := t.TempDir()
		oldTarget := filepath.Join(t.TempDir(), "old_quality_gate.sh")
		if err := os.WriteFile(oldTarget, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
			t.Fatalf("create old target: %v", err)
		}
		link := filepath.Join(worktree, "quality_gate.sh")
		if err := os.Symlink(oldTarget, link); err != nil {
			t.Fatalf("create stale symlink: %v", err)
		}

		target := filepath.Join(t.TempDir(), "quality_gate.sh")
		if err := os.WriteFile(target, []byte("#!/bin/sh\necho new\n"), 0o755); err != nil {
			t.Fatalf("create target: %v", err)
		}

		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager("/repo", "", target, runner)
		mgr.linkQualityGate(context.Background(), worktree)

		info, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("expected quality gate at %s, got error: %v", link, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("quality gate must be an isolated copy, got symlink")
		}
		got, err := os.ReadFile(link)
		if err != nil {
			t.Fatalf("read copied quality gate: %v", err)
		}
		if !strings.Contains(string(got), "new") || strings.Contains(string(got), "old") {
			t.Fatalf("quality gate copy = %q, want new target content only", got)
		}
	})

	t.Run("noop_when_qualityGatePath_empty", func(t *testing.T) {
		worktree := t.TempDir()

		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager("/repo", "", "", runner)
		mgr.linkQualityGate(context.Background(), worktree)

		link := filepath.Join(worktree, "quality_gate.sh")
		if _, err := os.Lstat(link); err == nil {
			t.Fatalf("expected no symlink at %s, but it exists", link)
		}
	})

	t.Run("broken_target_logs_warn_no_error", func(t *testing.T) {
		worktree := t.TempDir()
		nonExistent := "/no/such/path/quality_gate.sh"

		var logBuf bytes.Buffer
		h := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})
		origLogger := slog.Default()
		slog.SetDefault(slog.New(h))
		defer slog.SetDefault(origLogger)

		runner := &mockCommandRunner{}
		mgr := NewGitWorktreeManager("/repo", "", nonExistent, runner)
		mgr.linkQualityGate(context.Background(), worktree)

		if !strings.Contains(logBuf.String(), "WARN") {
			t.Fatalf("expected slog.Warn to be called, got: %q", logBuf.String())
		}
	})
}

func TestLinkQualityGateCreatesIsolatedManagedCopy(t *testing.T) {
	worktree := t.TempDir()
	rootDir := t.TempDir()
	rootScript := filepath.Join(rootDir, "scripts", "quality_gate.sh")
	if err := os.MkdirAll(filepath.Dir(rootScript), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	rootContent := []byte("#!/bin/sh\necho root\n")
	if err := os.WriteFile(rootScript, rootContent, 0o755); err != nil {
		t.Fatalf("create root quality gate: %v", err)
	}

	worktreeScript := filepath.Join(worktree, "quality_gate.sh")
	if err := os.Symlink(rootScript, worktreeScript); err != nil {
		t.Fatalf("create stale worktree symlink: %v", err)
	}

	mgr := NewGitWorktreeManager(rootDir, "", rootScript, &mockCommandRunner{})
	mgr.linkQualityGate(context.Background(), worktree)

	info, err := os.Lstat(worktreeScript)
	if err != nil {
		t.Fatalf("expected worktree quality gate at %s: %v", worktreeScript, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("worktree quality gate must be an isolated copy, got symlink")
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("worktree quality gate is not executable: mode %v", info.Mode().Perm())
	}

	worktreeContent, err := os.ReadFile(worktreeScript)
	if err != nil {
		t.Fatalf("read worktree quality gate: %v", err)
	}
	if !bytes.Equal(worktreeContent, rootContent) {
		t.Fatalf("worktree quality gate content = %q, want %q", worktreeContent, rootContent)
	}

	if err := os.WriteFile(worktreeScript, []byte("#!/bin/sh\necho worktree edit\n"), 0o755); err != nil {
		t.Fatalf("edit worktree quality gate: %v", err)
	}
	rootAfterEdit, err := os.ReadFile(rootScript)
	if err != nil {
		t.Fatalf("read root quality gate after worktree edit: %v", err)
	}
	if !bytes.Equal(rootAfterEdit, rootContent) {
		t.Fatalf("editing worktree quality gate mutated root script: got %q, want %q", rootAfterEdit, rootContent)
	}
}

func TestReusedWorktreeRefreshesManagedQualityGate(t *testing.T) {
	worktree := t.TempDir()
	configuredSource := filepath.Join(t.TempDir(), "quality_gate.sh")
	configuredContent := []byte("#!/bin/sh\necho current\n")
	if err := os.WriteFile(configuredSource, configuredContent, 0o755); err != nil {
		t.Fatalf("create configured quality gate: %v", err)
	}

	worktreeQualityGate := filepath.Join(worktree, "quality_gate.sh")
	if err := os.WriteFile(worktreeQualityGate, []byte("#!/bin/sh\necho stale\n"), 0o644); err != nil {
		t.Fatalf("create stale worktree quality gate: %v", err)
	}
	unrelatedFile := filepath.Join(worktree, "worker-notes.txt")
	unrelatedContent := []byte("preserve me\n")
	if err := os.WriteFile(unrelatedFile, unrelatedContent, 0o600); err != nil {
		t.Fatalf("create unrelated worktree file: %v", err)
	}

	runner := &mockCommandRunner{
		callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "git" && slices.Equal(args, []string{"-C", "/repo", "rev-parse", "agent/oro-reused"}) {
				return []byte("same-head\n"), nil
			}
			if name == "git" && slices.Equal(args, []string{"-C", "/repo", "rev-parse", "main"}) {
				return []byte("same-head\n"), nil
			}
			return nil, fmt.Errorf("unexpected command: %s %v", name, args)
		},
	}
	mgr := NewGitWorktreeManager("/repo", "", configuredSource, runner)

	fastForwarded, err := mgr.PrepareExistingForReuse(context.Background(), worktree, "agent/oro-reused", "main")
	if err != nil {
		t.Fatalf("PrepareExistingForReuse: %v", err)
	}
	if fastForwarded {
		t.Fatal("fastForwarded = true, want false when branch already matches base")
	}

	gotQualityGate, err := os.ReadFile(worktreeQualityGate)
	if err != nil {
		t.Fatalf("read refreshed quality gate: %v", err)
	}
	if !bytes.Equal(gotQualityGate, configuredContent) {
		t.Fatalf("quality gate content = %q, want %q", gotQualityGate, configuredContent)
	}
	info, err := os.Lstat(worktreeQualityGate)
	if err != nil {
		t.Fatalf("lstat refreshed quality gate: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("quality gate must be a regular snapshot, got symlink")
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o755); got != want {
		t.Fatalf("quality gate mode = %o, want %o", got, want)
	}
	gotUnrelated, err := os.ReadFile(unrelatedFile)
	if err != nil {
		t.Fatalf("read unrelated worktree file: %v", err)
	}
	if !bytes.Equal(gotUnrelated, unrelatedContent) {
		t.Fatalf("unrelated worktree file = %q, want %q", gotUnrelated, unrelatedContent)
	}
}

func TestPrepareExistingForReuseFailsClosedWhenManagedQualityGateIsMissing(t *testing.T) {
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "git" && slices.Equal(args, []string{"-C", "/repo", "rev-parse", "agent/oro-reused"}) {
				return []byte("same-head\n"), nil
			}
			if name == "git" && slices.Equal(args, []string{"-C", "/repo", "rev-parse", "main"}) {
				return []byte("same-head\n"), nil
			}
			return nil, fmt.Errorf("unexpected command: %s %v", name, args)
		},
	}
	mgr := NewGitWorktreeManager("/repo", "", filepath.Join(t.TempDir(), "missing-quality_gate.sh"), runner)

	_, err := mgr.PrepareExistingForReuse(context.Background(), t.TempDir(), "agent/oro-reused", "main")
	if err == nil {
		t.Fatal("PrepareExistingForReuse error = nil, want missing managed quality gate to block reuse")
	}
	if !strings.Contains(err.Error(), "managed quality gate") {
		t.Fatalf("PrepareExistingForReuse error = %v, want managed quality gate context", err)
	}
}

func TestNewGitWorktreeManager_StoresQualityGatePath(t *testing.T) {
	runner := &mockCommandRunner{}
	wantQG := "/path/to/quality_gate.sh"
	mgr := NewGitWorktreeManager("/repo/root", "", wantQG, runner)
	if mgr.qualityGatePath != wantQG {
		t.Errorf("qualityGatePath: got %q, want %q", mgr.qualityGatePath, wantQG)
	}
}

func TestNewGitWorktreeManager_EmptyQualityGatePath(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)
	if mgr.qualityGatePath != "" {
		t.Errorf("expected empty qualityGatePath, got %q", mgr.qualityGatePath)
	}
}

func TestRebaseOnto_Success(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	err := mgr.RebaseOnto(context.Background(), "agent/feature", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the branch is checked out before rebasing onto the target.
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 command calls, got %d", len(runner.calls))
	}
	checkoutCall := runner.calls[0]
	if checkoutCall.Name != "git" {
		t.Fatalf("expected checkout command name 'git', got %q", checkoutCall.Name)
	}
	wantCheckoutArgs := []string{"-C", "/repo/root", "checkout", "agent/feature"}
	if len(checkoutCall.Args) != len(wantCheckoutArgs) {
		t.Fatalf("checkout args length: got %d, want %d", len(checkoutCall.Args), len(wantCheckoutArgs))
	}
	for i, arg := range checkoutCall.Args {
		if arg != wantCheckoutArgs[i] {
			t.Fatalf("checkout args[%d]: got %q, want %q", i, arg, wantCheckoutArgs[i])
		}
	}

	call := runner.calls[1]
	if call.Name != "git" {
		t.Fatalf("expected command name 'git', got %q", call.Name)
	}
	wantArgs := []string{"-C", "/repo/root", "rebase", "main"}
	if len(call.Args) != len(wantArgs) {
		t.Fatalf("args length: got %d, want %d", len(call.Args), len(wantArgs))
	}
	for i, arg := range call.Args {
		if arg != wantArgs[i] {
			t.Fatalf("args[%d]: got %q, want %q", i, arg, wantArgs[i])
		}
	}
}

func TestRebaseOnto_Conflict(t *testing.T) {
	runner := &mockCommandRunner{
		err: fmt.Errorf("git rebase failed: CONFLICT (content): Merge conflict in file.go"),
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	err := mgr.RebaseOnto(context.Background(), "agent/feature", "main")
	if err == nil {
		t.Fatal("expected error from RebaseOnto")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected error to contain 'conflict', got: %v", err)
	}
}

func TestPushBranch_Success(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	err := mgr.PushBranch(context.Background(), "agent/feature")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the git push command was called with correct arguments
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 command call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.Name != "git" {
		t.Fatalf("expected command name 'git', got %q", call.Name)
	}
	wantArgs := []string{"-C", "/repo/root", "push", "origin", "agent/feature"}
	if len(call.Args) != len(wantArgs) {
		t.Fatalf("args length: got %d, want %d", len(call.Args), len(wantArgs))
	}
	for i, arg := range call.Args {
		if arg != wantArgs[i] {
			t.Fatalf("args[%d]: got %q, want %q", i, arg, wantArgs[i])
		}
	}
}

func TestPushBranch_NoRemote(t *testing.T) {
	runner := &mockCommandRunner{
		err: fmt.Errorf("fatal: 'origin' does not appear to be a 'git' repository"),
	}
	mgr := NewGitWorktreeManager("/repo/root", "", "", runner)

	err := mgr.PushBranch(context.Background(), "agent/feature")
	if err == nil {
		t.Fatal("expected error from PushBranch when remote doesn't exist")
	}
	// Verify the error is wrapped but still contains useful information
	if !strings.Contains(err.Error(), "push") {
		t.Fatalf("expected error to contain 'push', got: %v", err)
	}
}
