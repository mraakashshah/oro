package merge //nolint:testpackage // internal test needs access to unexported types

import (
	"context"
	"testing"
)

// TestMergeToMain simulates the scenario where main is checked out in the primary
// worktree. The ff-merge approach avoids "git checkout main" entirely by removing
// the agent worktree and running git merge --ff-only in the primary repo.
func TestMergeToMain(t *testing.T) {
	t.Run("merge succeeds when main is checked out elsewhere", func(t *testing.T) {
		mock := &mockGitRunner{
			results: []mockResult{
				// 0. git rev-list --count main..bead/abc — not merged yet
				{Stdout: "2\n", Stderr: "", Err: nil},
				// 1. git rebase main bead/abc — success
				{Stdout: "", Stderr: "", Err: nil},
				// 2. git rev-parse --git-common-dir — returns common git dir
				//    primaryRepo derived by stripping /.git suffix
				{Stdout: "/path/to/repo/.git\n", Stderr: "", Err: nil},
				// 3. git worktree remove (fallback via GitRunner, no WorktreeRemover set)
				{Stdout: "", Stderr: "", Err: nil},
				// 4. git merge --ff-only bead/abc (in primary repo)
				{Stdout: "", Stderr: "", Err: nil},
				// 5. git rev-parse HEAD (in primary repo) — returns final SHA
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
			t.Fatalf("expected merge to succeed with ff-merge, got error: %v", err)
		}

		if result.CommitSHA != "finalsha123" {
			t.Errorf("expected commit SHA finalsha123, got %q", result.CommitSHA)
		}

		// Verify the sequence of git commands
		calls := mock.getCalls()
		if len(calls) != 6 {
			t.Fatalf("expected 6 git calls, got %d: %+v", len(calls), calls)
		}

		// Verify already-merged check
		assertArgs(t, calls[0], "/tmp/wt-abc", "rev-list", "--count", "main..bead/abc")
		// Verify rebase happened in worktree
		assertArgs(t, calls[1], "/tmp/wt-abc", "rebase", "main", "bead/abc")
		// Verify rev-parse --git-common-dir
		assertArgs(t, calls[2], "/tmp/wt-abc", "rev-parse", "--git-common-dir")
		// Verify worktree remove (fallback via git, in primary repo)
		assertArgs(t, calls[3], "/path/to/repo", "worktree", "remove", "/tmp/wt-abc")
		// Verify ff-only merge happened in primary repo (NOT cherry-pick)
		assertArgs(t, calls[4], "/path/to/repo", "merge", "--ff-only", "bead/abc")
		// Verify final rev-parse in primary repo
		assertArgs(t, calls[5], "/path/to/repo", "rev-parse", "HEAD")
	})

	t.Run("commits from agent branch land on main with same SHA", func(t *testing.T) {
		// The ff-only guarantee: the final SHA on main == the branch tip SHA.
		// With cherry-pick, SHAs would differ because a new commit object is created.
		mock := &mockGitRunner{
			results: []mockResult{
				// 0. git rev-list --count main..bead/xyz — not merged yet
				{Stdout: "1\n", Stderr: "", Err: nil},
				// 1. git rebase main bead/xyz — success
				{Stdout: "", Stderr: "", Err: nil},
				// 2. git rev-parse --git-common-dir
				{Stdout: "/repo/.git\n", Stderr: "", Err: nil},
				// 3. git worktree remove (fallback)
				{Stdout: "", Stderr: "", Err: nil},
				// 4. git merge --ff-only bead/xyz
				{Stdout: "", Stderr: "", Err: nil},
				// 5. git rev-parse HEAD — same SHA as branch tip (ff guarantee)
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
