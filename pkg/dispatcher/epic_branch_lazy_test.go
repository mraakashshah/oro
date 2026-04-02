package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"errors"
	"testing"
)

// capturingRunner is a CommandRunner that records the most-recent invocation.
type capturingRunner struct {
	args []string
	out  []byte
	err  error
}

func (r *capturingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.args = append([]string{name}, args...)
	return r.out, r.err
}

// TestCreateBranch_Interface verifies:
//   - CreateBranch method exists on WorktreeManager interface
//   - GitWorktreeManager implements it (runs `git -C <root> branch <name> <from>`)
//   - mockWorktreeManager has createBranchErr field and CreateBranch method
//   - All test files compile
func TestCreateBranch_Interface(t *testing.T) {
	t.Run("mock_default_success", func(t *testing.T) {
		m := &mockWorktreeManager{}
		if err := m.CreateBranch(context.Background(), "epic/some-epic", "main"); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("mock_returns_createBranchErr", func(t *testing.T) {
		want := errors.New("branch already exists")
		m := &mockWorktreeManager{createBranchErr: want}
		if err := m.CreateBranch(context.Background(), "epic/some-epic", "main"); !errors.Is(err, want) {
			t.Fatalf("expected %v, got %v", want, err)
		}
	})

	t.Run("git_worktree_manager_calls_git_branch", func(t *testing.T) {
		cr := &capturingRunner{}
		mgr := NewGitWorktreeManager("/repo", "/repo/.worktrees", "", cr)
		if err := mgr.CreateBranch(context.Background(), "epic/oro-test", "main"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Expect: git -C /repo branch epic/oro-test main
		wantArgs := []string{"git", "-C", "/repo", "branch", "epic/oro-test", "main"}
		if len(cr.args) != len(wantArgs) {
			t.Fatalf("got args %v, want %v", cr.args, wantArgs)
		}
		for i, a := range wantArgs {
			if cr.args[i] != a {
				t.Errorf("arg[%d]: got %q, want %q", i, cr.args[i], a)
			}
		}
	})

	t.Run("git_worktree_manager_wraps_git_error", func(t *testing.T) {
		gitErr := errors.New("exit status 128")
		cr := &capturingRunner{
			out: []byte("fatal: A branch named 'epic/oro-test' already exists."),
			err: gitErr,
		}
		mgr := NewGitWorktreeManager("/repo", "/repo/.worktrees", "", cr)
		err := mgr.CreateBranch(context.Background(), "epic/oro-test", "main")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, gitErr) {
			t.Errorf("expected wrapped gitErr, got: %v", err)
		}
	})
}
