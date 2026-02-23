package dispatcher //nolint:testpackage // white-box tests for worktree manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// worktree tests reuse mockCommandRunner from beadsource_test.go

func TestGitWorktreeManager_Create_Success(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/repo/root", runner)

	path, branch, err := mgr.Create(context.Background(), "abc123")
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

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 command call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
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

func TestGitWorktreeManager_Create_Error(t *testing.T) {
	runner := &mockCommandRunner{
		err: fmt.Errorf("git worktree add failed: branch already exists"),
	}
	mgr := NewGitWorktreeManager("/repo/root", runner)

	_, _, err := mgr.Create(context.Background(), "abc123")
	if err == nil {
		t.Fatal("expected error from Create")
	}
	if !strings.Contains(err.Error(), "worktree add") {
		t.Fatalf("error should mention worktree add, got: %v", err)
	}
}

func TestGitWorktreeManager_Remove_Success(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/repo/root", runner)

	err := mgr.Remove(context.Background(), "/repo/root/.worktrees/abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expect 2 calls: git status (returns clean) + git worktree remove
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 command calls, got %d", len(runner.calls))
	}

	// Call 1: git status --porcelain (auto-commit check)
	statusCall := runner.calls[0]
	if !containsAll(statusCall.Args, "status", "--porcelain") {
		t.Fatalf("call[0] should be git status --porcelain, got: %v", statusCall.Args)
	}

	// Call 2: git worktree remove
	removeCall := runner.calls[1]
	wantArgs := []string{"-C", "/repo/root", "worktree", "remove", "/repo/root/.worktrees/abc123", "--force"}
	if len(removeCall.Args) != len(wantArgs) {
		t.Fatalf("args: got %v, want %v", removeCall.Args, wantArgs)
	}
	for i, a := range removeCall.Args {
		if a != wantArgs[i] {
			t.Fatalf("args[%d]: got %q, want %q", i, a, wantArgs[i])
		}
	}
}

func TestGitWorktreeManager_Remove_Error(t *testing.T) {
	runner := &mockCommandRunner{
		err: fmt.Errorf("git worktree remove failed: not a worktree"),
	}
	mgr := NewGitWorktreeManager("/repo/root", runner)

	err := mgr.Remove(context.Background(), "/repo/root/.worktrees/abc123")
	if err == nil {
		t.Fatal("expected error from Remove")
	}
	if !strings.Contains(err.Error(), "worktree remove") {
		t.Fatalf("error should mention worktree remove, got: %v", err)
	}
}

func TestGitWorktreeManager_Create_DifferentBeadIDs(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/my/repo", runner)

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
			path, branch, err := mgr.Create(context.Background(), tt.beadID)
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
	var _ WorktreeManager = NewGitWorktreeManager("/repo", runner)
}

func TestGitWorktreeManager_Prune_CleansOrphanDirs(t *testing.T) {
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
	mgr := NewGitWorktreeManager(tmpDir, runner)

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

	// Verify all orphan directories were removed.
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		t.Fatalf("reading .worktrees after Prune: %v", err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected .worktrees to be empty, still has: %v", names)
	}
}

func TestGitWorktreeManager_Prune_NoWorktreesDir(t *testing.T) {
	tmpDir := t.TempDir()
	// Intentionally do NOT create .worktrees/ — Prune should be graceful.

	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager(tmpDir, runner)

	err := mgr.Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune with no .worktrees dir should not error, got: %v", err)
	}

	// git worktree prune should still be called (it's safe even without .worktrees/).
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

	// git worktree prune fails, but Prune should still remove dirs and not return error.
	runner := &mockCommandRunner{err: fmt.Errorf("git prune failed")}
	mgr := NewGitWorktreeManager(tmpDir, runner)

	err := mgr.Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune should not return error even if git prune fails, got: %v", err)
	}

	// Orphan dir should still be removed even though git prune failed.
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		t.Fatalf("reading .worktrees after Prune: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected .worktrees to be empty after Prune, got %d entries", len(entries))
	}
}

func TestWorktreeManager_PrunesStaleBeforeCreate(t *testing.T) {
	callCount := 0
	runner := &mockCommandRunner{
		callFn: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			callCount++
			// First call: git worktree add fails with "already exists"
			if callCount == 1 {
				return nil, fmt.Errorf("fatal: a branch named 'agent/oro-stale' already exists")
			}
			// All subsequent calls succeed
			return nil, nil
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", runner)

	path, branch, err := mgr.Create(context.Background(), "oro-stale")
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

	// Expect 5 calls:
	// 1. git worktree add (fails - branch already exists)
	// 2. git worktree remove <path> --force
	// 3. git worktree prune
	// 4. git branch -D agent/oro-stale
	// 5. git worktree add (succeeds)
	if len(runner.calls) != 5 {
		var callDescs []string
		for i, c := range runner.calls {
			callDescs = append(callDescs, fmt.Sprintf("  [%d] %s %s", i, c.Name, strings.Join(c.Args, " ")))
		}
		t.Fatalf("expected 5 command calls, got %d:\n%s", len(runner.calls), strings.Join(callDescs, "\n"))
	}

	// Call 1: initial worktree add (fails)
	c1 := runner.calls[0]
	if c1.Name != "git" || !containsAll(c1.Args, "worktree", "add") {
		t.Fatalf("call[0] should be git worktree add, got: %s %v", c1.Name, c1.Args)
	}

	// Call 2: git worktree remove --force
	c2 := runner.calls[1]
	if c2.Name != "git" || !containsAll(c2.Args, "worktree", "remove", "--force") {
		t.Fatalf("call[1] should be git worktree remove --force, got: %s %v", c2.Name, c2.Args)
	}

	// Call 3: git worktree prune
	c3 := runner.calls[2]
	if c3.Name != "git" || !containsAll(c3.Args, "worktree", "prune") {
		t.Fatalf("call[2] should be git worktree prune, got: %s %v", c3.Name, c3.Args)
	}

	// Call 4: git branch -D agent/oro-stale
	c4 := runner.calls[3]
	if c4.Name != "git" || !containsAll(c4.Args, "branch", "-D", "agent/oro-stale") {
		t.Fatalf("call[3] should be git branch -D agent/oro-stale, got: %s %v", c4.Name, c4.Args)
	}

	// Call 5: retry worktree add (succeeds)
	c5 := runner.calls[4]
	if c5.Name != "git" || !containsAll(c5.Args, "worktree", "add") {
		t.Fatalf("call[4] should be git worktree add (retry), got: %s %v", c5.Name, c5.Args)
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

// TestGitWorktreeManager_Create_PathContainsBeadID kills mutant .go.7:
// "path = filepath.Join(...)" assignment removed.
// Verifies the git worktree add command receives the constructed path.
func TestGitWorktreeManager_Create_PathContainsBeadID(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/my/repo", runner)

	path, _, err := mgr.Create(context.Background(), "oro-xyz.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantPath := "/my/repo/.worktrees/oro-xyz.5"
	if path != wantPath {
		t.Fatalf("returned path: got %q, want %q", path, wantPath)
	}

	// The git call must include the constructed path — not an empty string.
	if len(runner.calls) < 1 {
		t.Fatal("expected at least 1 git call")
	}
	args := runner.calls[0].Args
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
	mgr := NewGitWorktreeManager("/my/repo", runner)

	_, branch, err := mgr.Create(context.Background(), "oro-xyz.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantBranch := "agent/oro-xyz.5"
	if branch != wantBranch {
		t.Fatalf("returned branch: got %q, want %q", branch, wantBranch)
	}

	// The git call must pass -b <branch> — not an empty string.
	if len(runner.calls) < 1 {
		t.Fatal("expected at least 1 git call")
	}
	args := runner.calls[0].Args
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
	runner := &mockCommandRunner{err: fmt.Errorf("not a worktree")}
	mgr := NewGitWorktreeManager("/my/repo", runner)

	worktreePath := "/my/repo/.worktrees/failing-bead"
	err := mgr.Remove(context.Background(), worktreePath)
	if err == nil {
		t.Fatal("expected error from Remove with failing runner")
	}

	// Error must include the path so callers can identify which worktree failed.
	if !strings.Contains(err.Error(), worktreePath) {
		t.Fatalf("error %q should contain path %q", err.Error(), worktreePath)
	}
}

// TestGitWorktreeManager_Prune_SkipsNonDirEntries kills mutants .go.6 / .go.12:
// "continue" removed in Prune loop — non-dir entries would be passed to os.RemoveAll.
// Verifies that a plain file inside .worktrees/ is NOT removed.
func TestGitWorktreeManager_Prune_SkipsNonDirEntries(t *testing.T) {
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

	// Create an orphan directory (should be removed).
	orphanDir := filepath.Join(worktreesDir, "old-bead")
	if err := os.MkdirAll(orphanDir, 0o750); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}

	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager(tmpDir, runner)

	if err := mgr.Prune(context.Background()); err != nil {
		t.Fatalf("Prune returned error: %v", err)
	}

	// The file must still exist.
	if _, err := os.Stat(keepFile); os.IsNotExist(err) {
		t.Fatalf("Prune removed non-dir file %q — should have been skipped", keepFile)
	}

	// The orphan directory must be gone.
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Fatalf("Prune did not remove orphan dir %q", orphanDir)
	}
}

// TestGitWorktreeManager_Prune_NoWorktreesDirReturnsNil kills mutant .go.5:
// "return nil" removed when ReadDir fails — would fall through and return nil anyway,
// but this test pins the explicit early-return behaviour.
func TestGitWorktreeManager_Prune_NoWorktreesDirReturnsNilNoOtherCalls(t *testing.T) {
	tmpDir := t.TempDir()
	// Intentionally no .worktrees/ directory.

	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager(tmpDir, runner)

	err := mgr.Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune with missing .worktrees should return nil, got: %v", err)
	}

	// Only the git worktree prune call should have been made.
	if len(runner.calls) != 1 {
		t.Fatalf("expected exactly 1 git call (worktree prune), got %d", len(runner.calls))
	}
}

// TestGitWorktreeManager_Prune_RemovesOrphanDirsViaRemoveAll kills mutant .go.16:
// "os.RemoveAll(...)" removed in Prune loop.
// Verifies orphan dirs are physically gone after Prune (complements existing test,
// but uses a single orphan so the assertion is unambiguous).
func TestGitWorktreeManager_Prune_RemovesOrphanDirsViaRemoveAll(t *testing.T) {
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
	mgr := NewGitWorktreeManager(tmpDir, runner)

	if err := mgr.Prune(context.Background()); err != nil {
		t.Fatalf("Prune error: %v", err)
	}

	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Fatalf("orphan dir %q should be removed by Prune", orphanDir)
	}
}

// TestGitWorktreeManager_PruneStale_CallsWorktreeRemove kills mutant .go.13 (prune call) and .go.14 (branch -D):
// verifies that pruneStale (invoked via Create retry) calls:
//   - git worktree remove <path> --force
//   - git worktree prune
//   - git branch -D <branch>
func TestGitWorktreeManager_PruneStale_CommandSequence(t *testing.T) {
	callCount := 0
	runner := &mockCommandRunner{
		callFn: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			callCount++
			if callCount == 1 {
				// First worktree add fails — triggers pruneStale.
				return nil, fmt.Errorf("fatal: a branch named 'agent/seq-bead' already exists")
			}
			return nil, nil
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", runner)

	_, _, err := mgr.Create(context.Background(), "seq-bead")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if len(runner.calls) != 5 {
		var descs []string
		for i, c := range runner.calls {
			descs = append(descs, fmt.Sprintf("[%d] %s %s", i, c.Name, strings.Join(c.Args, " ")))
		}
		t.Fatalf("expected 5 calls, got %d:\n%s", len(runner.calls), strings.Join(descs, "\n"))
	}

	// call[1]: git worktree remove <path> --force
	c1 := runner.calls[1]
	if !containsAll(c1.Args, "worktree", "remove", "--force") {
		t.Fatalf("call[1] should be git worktree remove --force, got: %v", c1.Args)
	}
	wantPath := "/repo/root/.worktrees/seq-bead"
	if !containsAll(c1.Args, wantPath) {
		t.Fatalf("call[1] must include path %q, got: %v", wantPath, c1.Args)
	}

	// call[2]: git worktree prune  (kills .go.13)
	c2 := runner.calls[2]
	if !containsAll(c2.Args, "worktree", "prune") {
		t.Fatalf("call[2] should be git worktree prune, got: %v", c2.Args)
	}
	if containsAll(c2.Args, "remove") {
		t.Fatalf("call[2] must be 'worktree prune', not 'worktree remove', got: %v", c2.Args)
	}

	// call[3]: git branch -D agent/seq-bead  (kills .go.14)
	c3 := runner.calls[3]
	if !containsAll(c3.Args, "branch", "-D", "agent/seq-bead") {
		t.Fatalf("call[3] should be git branch -D agent/seq-bead, got: %v", c3.Args)
	}
}

// TestGitWorktreeManager_Create_PruneStaleCalledOnAlreadyExists kills mutant .go.10:
// "g.pruneStale(ctx, path, branch)" call removed — retry happens but cleanup is skipped.
// Verifies that when "already exists" fires, at least 4 extra calls are made
// (remove --force, prune, branch -D, retry add) before success.
func TestGitWorktreeManager_Create_PruneStaleCalledOnAlreadyExists(t *testing.T) {
	callCount := 0
	runner := &mockCommandRunner{
		callFn: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			callCount++
			if callCount == 1 {
				return nil, fmt.Errorf("fatal: a branch named 'agent/prune-bead' already exists")
			}
			return nil, nil
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", runner)

	path, branch, err := mgr.Create(context.Background(), "prune-bead")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if path != "/repo/root/.worktrees/prune-bead" {
		t.Fatalf("path: got %q", path)
	}
	if branch != "agent/prune-bead" {
		t.Fatalf("branch: got %q", branch)
	}

	// Without pruneStale call (mutant .go.10), only 2 calls occur: initial add + retry.
	// With pruneStale, 5 calls occur. Verify we got all 5.
	if len(runner.calls) != 5 {
		var descs []string
		for i, c := range runner.calls {
			descs = append(descs, fmt.Sprintf("[%d] %s %v", i, c.Name, c.Args))
		}
		t.Fatalf("expected 5 git calls (initial add, remove --force, prune, branch -D, retry add), got %d:\n%s",
			len(runner.calls), strings.Join(descs, "\n"))
	}

	// Verify calls[1..3] contain the stale cleanup operations (not just two worktree-add calls).
	hasWorktreeRemove := containsAll(runner.calls[1].Args, "worktree", "remove", "--force")
	hasWorktreePrune := containsAll(runner.calls[2].Args, "worktree", "prune")
	hasBranchDelete := containsAll(runner.calls[3].Args, "branch", "-D")

	if !hasWorktreeRemove || !hasWorktreePrune || !hasBranchDelete {
		t.Fatalf("pruneStale sequence not found: remove=%v prune=%v branch-D=%v",
			hasWorktreeRemove, hasWorktreePrune, hasBranchDelete)
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
	runner := &mockCommandRunner{
		callFn: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			callCount++
			// First call: git worktree add fails with "already exists"
			if callCount == 1 {
				return nil, fmt.Errorf("fatal: a branch named 'agent/oro-locked' already exists")
			}
			// All subsequent calls succeed
			return nil, nil
		},
	}
	mgr := NewGitWorktreeManager("/repo/root", runner)

	path, branch, err := mgr.Create(context.Background(), "oro-locked")
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
	// 1. git worktree add (fails)
	// 2. git worktree remove <path> --force (new!)
	// 3. git worktree prune
	// 4. git branch -D <branch>
	// 5. git worktree add (succeeds)
	if len(runner.calls) != 5 {
		var callDescs []string
		for i, c := range runner.calls {
			callDescs = append(callDescs, fmt.Sprintf("  [%d] %s %s", i, c.Name, strings.Join(c.Args, " ")))
		}
		t.Fatalf("expected 5 command calls, got %d:\n%s", len(runner.calls), strings.Join(callDescs, "\n"))
	}

	// Call 2: git worktree remove <path> --force
	c2 := runner.calls[1]
	if c2.Name != "git" || !containsAll(c2.Args, "worktree", "remove", "--force") {
		t.Fatalf("call[1] should be git worktree remove --force, got: %s %v", c2.Name, c2.Args)
	}
	if !containsAll(c2.Args, wantPath) {
		t.Fatalf("call[1] should include worktree path %q, got: %v", wantPath, c2.Args)
	}

	// Call 3: git worktree prune
	c3 := runner.calls[2]
	if c3.Name != "git" || !containsAll(c3.Args, "worktree", "prune") {
		t.Fatalf("call[2] should be git worktree prune, got: %s %v", c3.Name, c3.Args)
	}

	// Call 4: git branch -D <branch>
	c4 := runner.calls[3]
	if c4.Name != "git" || !containsAll(c4.Args, "branch", "-D", wantBranch) {
		t.Fatalf("call[3] should be git branch -D, got: %s %v", c4.Name, c4.Args)
	}
}

func TestGitWorktreeManager_DeleteBranch_Success(t *testing.T) {
	runner := &mockCommandRunner{}
	mgr := NewGitWorktreeManager("/repo/root", runner)

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
	mgr := NewGitWorktreeManager("/repo/root", runner)

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
	mgr := NewGitWorktreeManager("/repo/root", runner)

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

			_, _, err := mgr.Create(context.Background(), tt.beadID)
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
