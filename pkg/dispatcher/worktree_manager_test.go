package dispatcher //nolint:testpackage // white-box tests for worktree manager

import (
	"context"
	"fmt"
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
