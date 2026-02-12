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

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 command call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	wantArgs := []string{"-C", "/repo/root", "worktree", "remove", "/repo/root/.worktrees/abc123", "--force"}
	if len(call.Args) != len(wantArgs) {
		t.Fatalf("args: got %v, want %v", call.Args, wantArgs)
	}
	for i, a := range call.Args {
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

	// Expect 4 calls:
	// 1. git worktree add (fails - branch already exists)
	// 2. git worktree prune
	// 3. git branch -D agent/oro-stale
	// 4. git worktree add (succeeds)
	if len(runner.calls) != 4 {
		var callDescs []string
		for i, c := range runner.calls {
			callDescs = append(callDescs, fmt.Sprintf("  [%d] %s %s", i, c.Name, strings.Join(c.Args, " ")))
		}
		t.Fatalf("expected 4 command calls, got %d:\n%s", len(runner.calls), strings.Join(callDescs, "\n"))
	}

	// Call 1: initial worktree add (fails)
	c1 := runner.calls[0]
	if c1.Name != "git" || !containsAll(c1.Args, "worktree", "add") {
		t.Fatalf("call[0] should be git worktree add, got: %s %v", c1.Name, c1.Args)
	}

	// Call 2: git worktree prune
	c2 := runner.calls[1]
	if c2.Name != "git" || !containsAll(c2.Args, "worktree", "prune") {
		t.Fatalf("call[1] should be git worktree prune, got: %s %v", c2.Name, c2.Args)
	}

	// Call 3: git branch -D agent/oro-stale
	c3 := runner.calls[2]
	if c3.Name != "git" || !containsAll(c3.Args, "branch", "-D", "agent/oro-stale") {
		t.Fatalf("call[2] should be git branch -D agent/oro-stale, got: %s %v", c3.Name, c3.Args)
	}

	// Call 4: retry worktree add (succeeds)
	c4 := runner.calls[3]
	if c4.Name != "git" || !containsAll(c4.Args, "worktree", "add") {
		t.Fatalf("call[3] should be git worktree add (retry), got: %s %v", c4.Name, c4.Args)
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
