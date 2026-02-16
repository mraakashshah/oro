package merge //nolint:testpackage // internal test needs access to unexported types

import (
	"context"
	"testing"
)

// TestMergeToMain simulates the scenario where main is checked out in the primary
// worktree, causing "git checkout main" to fail with exit status 128.
// The fix uses cherry-pick to apply commits from the agent branch to main in the
// primary repo instead of checking out main in the worktree.
func TestMergeToMain(t *testing.T) {
	t.Run("merge succeeds when main is checked out elsewhere", func(t *testing.T) {
		mock := &mockGitRunner{
			results: []mockResult{
				// 1. git rebase main bead/abc — success
				{Stdout: "", Stderr: "", Err: nil},
				// 2. git rev-parse --git-common-dir — returns common git dir
				{Stdout: "/path/to/repo/.git\n", Stderr: "", Err: nil},
				// 3. git rev-parse --show-toplevel (from common dir) — returns primary repo
				{Stdout: "/path/to/repo\n", Stderr: "", Err: nil},
				// 4. git rev-list --reverse main..bead/abc — returns commit SHAs
				{Stdout: "commit1\ncommit2\n", Stderr: "", Err: nil},
				// 5. git cherry-pick commit1 (in primary repo) — success
				{Stdout: "", Stderr: "", Err: nil},
				// 6. git cherry-pick commit2 (in primary repo) — success
				{Stdout: "", Stderr: "", Err: nil},
				// 7. git rev-parse HEAD (in primary repo) — returns final SHA
				{Stdout: "finalsha123\n", Stderr: "", Err: nil},
			},
		}

		coord := NewCoordinator(mock)
		opts := Opts{
			Branch:   "bead/abc",
			Worktree: "/tmp/wt-abc",
			BeadID:   "oro-abc",
		}

		result, err := coord.Merge(context.Background(), opts)
		if err != nil {
			t.Fatalf("expected merge to succeed with cherry-pick, got error: %v", err)
		}

		if result.CommitSHA != "finalsha123" {
			t.Errorf("expected commit SHA finalsha123, got %q", result.CommitSHA)
		}

		// Verify the sequence of git commands
		calls := mock.getCalls()
		if len(calls) != 7 {
			t.Fatalf("expected 7 git calls, got %d: %+v", len(calls), calls)
		}

		// Verify rebase happened in worktree
		assertArgs(t, calls[0], "/tmp/wt-abc", "rebase", "main", "bead/abc")
		// Verify rev-parse --git-common-dir
		assertArgs(t, calls[1], "/tmp/wt-abc", "rev-parse", "--git-common-dir")
		// Verify rev-parse --show-toplevel from common dir
		assertArgs(t, calls[2], "/path/to/repo/.git", "rev-parse", "--show-toplevel")
		// Verify rev-list to get commits
		assertArgs(t, calls[3], "/tmp/wt-abc", "rev-list", "--reverse", "main..bead/abc")
		// Verify cherry-picks happened in primary repo
		assertArgs(t, calls[4], "/path/to/repo", "cherry-pick", "commit1")
		assertArgs(t, calls[5], "/path/to/repo", "cherry-pick", "commit2")
		// Verify final rev-parse in primary repo
		assertArgs(t, calls[6], "/path/to/repo", "rev-parse", "HEAD")
	})

	t.Run("commits from agent branch land on main", func(t *testing.T) {
		mock := &mockGitRunner{
			results: []mockResult{
				// 1. git rebase main bead/xyz — success
				{Stdout: "", Stderr: "", Err: nil},
				// 2. git rev-parse --git-common-dir
				{Stdout: "/repo/.git\n", Stderr: "", Err: nil},
				// 3. git rev-parse --show-toplevel
				{Stdout: "/repo\n", Stderr: "", Err: nil},
				// 4. git rev-list --reverse main..bead/xyz
				{Stdout: "abc123\n", Stderr: "", Err: nil},
				// 5. git cherry-pick abc123
				{Stdout: "", Stderr: "", Err: nil},
				// 6. git rev-parse HEAD
				{Stdout: "abc123\n", Stderr: "", Err: nil},
			},
		}

		coord := NewCoordinator(mock)
		result, err := coord.Merge(context.Background(), Opts{
			Branch:   "bead/xyz",
			Worktree: "/tmp/wt-xyz",
			BeadID:   "oro-xyz",
		})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}

		if result.CommitSHA != "abc123" {
			t.Errorf("expected commit SHA abc123, got %q", result.CommitSHA)
		}
	})
}
