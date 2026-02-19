package merge //nolint:testpackage // internal test needs access to unexported types

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// mockWorktreeRemover records calls to Remove.
type mockWorktreeRemover struct {
	calls []string
	err   error
}

func (m *mockWorktreeRemover) Remove(path string) error {
	m.calls = append(m.calls, path)
	return m.err
}

// TestMergeToMain_FFMerge verifies the new worktree-remove + ff-merge flow:
//  1. git rev-list --count (already-merged check)
//  2. git rebase main <branch> (in worktree)
//  3. git rev-parse --git-common-dir (derive primary repo)
//  4. WorktreeRemover.Remove(worktree)
//  5. git merge --ff-only <branch> (in primary repo)
//  6. git rev-parse HEAD (in primary repo)
//
// The final commit SHA on main must match the branch tip SHA (ff-only guarantee).
func TestMergeToMain_FFMerge(t *testing.T) {
	t.Run("ff-merge succeeds: final SHA matches branch tip", func(t *testing.T) {
		remover := &mockWorktreeRemover{}
		mock := &mockGitRunner{
			results: []mockResult{
				// 0. git rev-list --count main..bead/abc — not merged yet
				{Stdout: "2\n", Stderr: "", Err: nil},
				// 1. git rebase main bead/abc — success
				{Stdout: "", Stderr: "", Err: nil},
				// 2. git rev-parse --git-common-dir
				{Stdout: "/repo/.git\n", Stderr: "", Err: nil},
				// 3. git merge --ff-only bead/abc (in primary repo)
				{Stdout: "", Stderr: "", Err: nil},
				// 4. git rev-parse HEAD (in primary repo) — same SHA as branch tip
				{Stdout: "branchTipSHA\n", Stderr: "", Err: nil},
			},
		}

		coord := NewCoordinator(mock)
		coord.worktreeRemover = remover
		opts := Opts{
			Branch:   "bead/abc",
			Worktree: "/tmp/wt-abc",
			BeadID:   "oro-abc",
		}

		result, err := coord.Merge(context.Background(), opts)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if result.CommitSHA != "branchTipSHA" {
			t.Errorf("expected commitSHA=branchTipSHA, got %q", result.CommitSHA)
		}

		// Verify WorktreeRemover was called with the correct path
		if len(remover.calls) != 1 {
			t.Fatalf("expected 1 WorktreeRemover.Remove call, got %d", len(remover.calls))
		}
		if remover.calls[0] != "/tmp/wt-abc" {
			t.Errorf("expected Remove(/tmp/wt-abc), got Remove(%q)", remover.calls[0])
		}

		// Verify git commands: NO cherry-pick, uses ff-only instead
		calls := mock.getCalls()
		if len(calls) != 5 {
			t.Fatalf("expected 5 git calls, got %d: %+v", len(calls), calls)
		}
		assertArgs(t, calls[0], "/tmp/wt-abc", "rev-list", "--count", "main..bead/abc")
		assertArgs(t, calls[1], "/tmp/wt-abc", "rebase", "main", "bead/abc")
		assertArgs(t, calls[2], "/tmp/wt-abc", "rev-parse", "--git-common-dir")
		assertArgs(t, calls[3], "/repo", "merge", "--ff-only", "bead/abc")
		assertArgs(t, calls[4], "/repo", "rev-parse", "HEAD")

		// Verify no cherry-pick was issued
		for _, c := range calls {
			for _, arg := range c.Args {
				if arg == "cherry-pick" {
					t.Errorf("cherry-pick should not be called; got call: %+v", c)
				}
			}
		}
	})

	t.Run("worktree remove fails: returns error with guidance", func(t *testing.T) {
		remover := &mockWorktreeRemover{
			err: fmt.Errorf("exit status 128: worktree has uncommitted changes"),
		}
		mock := &mockGitRunner{
			results: []mockResult{
				// 0. git rev-list --count — not merged yet
				{Stdout: "1\n", Stderr: "", Err: nil},
				// 1. git rebase — success
				{Stdout: "", Stderr: "", Err: nil},
				// 2. git rev-parse --git-common-dir
				{Stdout: "/repo/.git\n", Stderr: "", Err: nil},
				// Remove fails — no further git calls
			},
		}

		coord := NewCoordinator(mock)
		coord.worktreeRemover = remover
		_, err := coord.Merge(context.Background(), Opts{
			Branch:   "bead/dirty",
			Worktree: "/tmp/wt-dirty",
			BeadID:   "oro-dirty",
		})

		if err == nil {
			t.Fatal("expected error on worktree remove failure, got nil")
		}
		if !strings.Contains(err.Error(), "worktree remove") {
			t.Errorf("expected 'worktree remove' in error, got: %v", err)
		}
		// Should NOT be a ConflictError
		var conflictErr *ConflictError
		if errors.As(err, &conflictErr) {
			t.Error("worktree remove failure should not produce ConflictError")
		}
	})

	t.Run("ff-only merge fails: returns error, branch still exists for retry", func(t *testing.T) {
		remover := &mockWorktreeRemover{}
		mock := &mockGitRunner{
			results: []mockResult{
				// 0. git rev-list --count — not merged yet
				{Stdout: "1\n", Stderr: "", Err: nil},
				// 1. git rebase — success
				{Stdout: "", Stderr: "", Err: nil},
				// 2. git rev-parse --git-common-dir
				{Stdout: "/repo/.git\n", Stderr: "", Err: nil},
				// 3. git merge --ff-only — fails (main moved)
				{Stdout: "", Stderr: "fatal: Not possible to fast-forward, aborting.", Err: fmt.Errorf("exit status 128")},
			},
		}

		coord := NewCoordinator(mock)
		coord.worktreeRemover = remover
		_, err := coord.Merge(context.Background(), Opts{
			Branch:   "bead/ff",
			Worktree: "/tmp/wt-ff",
			BeadID:   "oro-ff",
		})

		if err == nil {
			t.Fatal("expected error on ff-only failure, got nil")
		}
		if !strings.Contains(err.Error(), "ff-only") {
			t.Errorf("expected 'ff-only' in error, got: %v", err)
		}
		// Should NOT be a ConflictError
		var conflictErr *ConflictError
		if errors.As(err, &conflictErr) {
			t.Error("ff-only failure should not produce ConflictError")
		}
		// Remove should have been called (worktree was removed before merge attempt)
		if len(remover.calls) != 1 {
			t.Errorf("expected Remove to be called once, got %d calls", len(remover.calls))
		}
	})

	t.Run("no worktreeRemover set: uses git worktree remove via GitRunner", func(t *testing.T) {
		// When worktreeRemover is nil, the coordinator falls back to
		// running "git worktree remove <path>" via the GitRunner.
		mock := &mockGitRunner{
			results: []mockResult{
				// 0. git rev-list --count — not merged yet
				{Stdout: "1\n", Stderr: "", Err: nil},
				// 1. git rebase — success
				{Stdout: "", Stderr: "", Err: nil},
				// 2. git rev-parse --git-common-dir
				{Stdout: "/repo/.git\n", Stderr: "", Err: nil},
				// 3. git worktree remove (via GitRunner fallback)
				{Stdout: "", Stderr: "", Err: nil},
				// 4. git merge --ff-only bead/fallback
				{Stdout: "", Stderr: "", Err: nil},
				// 5. git rev-parse HEAD
				{Stdout: "fallbackSHA\n", Stderr: "", Err: nil},
			},
		}

		coord := NewCoordinator(mock) // no worktreeRemover set
		result, err := coord.Merge(context.Background(), Opts{
			Branch:   "bead/fallback",
			Worktree: "/tmp/wt-fallback",
			BeadID:   "oro-fallback",
		})
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if result.CommitSHA != "fallbackSHA" {
			t.Errorf("expected fallbackSHA, got %q", result.CommitSHA)
		}

		calls := mock.getCalls()
		if len(calls) != 6 {
			t.Fatalf("expected 6 git calls, got %d: %+v", len(calls), calls)
		}
		// call[3] should be the worktree remove via git
		assertArgs(t, calls[3], "/repo", "worktree", "remove", "/tmp/wt-fallback")
		assertArgs(t, calls[4], "/repo", "merge", "--ff-only", "bead/fallback")
	})
}
