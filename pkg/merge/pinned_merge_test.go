package merge //nolint:testpackage // pinned merge tests exercise internal sequencing.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPinnedMergeRequiresBothExpectedSHAs(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		source string
		target string
	}{
		{name: "source only", source: "source-sha"},
		{name: "target only", target: "target-sha"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			git := &mockGitRunner{}
			_, err := NewCoordinator(git).Merge(context.Background(), Opts{
				Branch:            "agent/pinned",
				Worktree:          "/tmp/pinned",
				ExpectedSourceSHA: tt.source,
				ExpectedTargetSHA: tt.target,
			})
			if err == nil || !strings.Contains(err.Error(), "requires both expected source and target SHAs") {
				t.Fatalf("Merge error = %v, want incomplete pinned identity error", err)
			}
			if calls := git.getCalls(); len(calls) != 0 {
				t.Fatalf("incomplete pinned merge made git calls: %+v", calls)
			}
		})
	}
}

func TestPinnedMergePropagatesEveryGitBoundaryFailure(t *testing.T) {
	t.Parallel()
	const (
		source = "source-sha"
		target = "target-sha"
	)
	successes := []mockResult{
		{Stdout: source + "\n"},
		{Stdout: source + "\n"},
		{Stdout: target + "\n"},
		{Stdout: "/repo/.git\n"},
		{},
		{Stdout: source + "\n"},
	}
	for failAt := range successes {
		failAt := failAt
		t.Run([]string{"source ref", "worktree head", "target ref", "primary repo", "target advance", "integrated target"}[failAt], func(t *testing.T) {
			t.Parallel()
			want := errors.New("injected pinned boundary failure")
			results := append([]mockResult(nil), successes...)
			results[failAt] = mockResult{Err: want}
			git := &mockGitRunner{results: results}
			_, err := NewCoordinator(git).Merge(context.Background(), Opts{
				Branch:            "agent/pinned",
				Worktree:          "/tmp/pinned",
				ExpectedSourceSHA: source,
				ExpectedTargetSHA: target,
			})
			if !errors.Is(err, want) {
				t.Fatalf("Merge error = %v, want injected boundary failure", err)
			}
		})
	}
}

func TestPinnedMergePropagatesWorktreeRemovalFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("injected pinned worktree removal failure")
	git := &mockGitRunner{results: pinnedMergeSuccessResults()}
	coord := NewCoordinator(git)
	coord.worktreeRemover = &mockWorktreeRemover{err: want}
	_, err := coord.Merge(context.Background(), pinnedMergeOpts())
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "worktree remove failed") {
		t.Fatalf("Merge error = %v, want wrapped worktree removal failure", err)
	}
}

func TestPinnedMainMergeUsesOnlyApprovedFastForward(t *testing.T) {
	t.Parallel()
	git := &mockGitRunner{results: pinnedMergeSuccessResults()}
	coord := NewCoordinator(git)
	remover := &mockWorktreeRemover{}
	coord.worktreeRemover = remover
	result, err := coord.Merge(context.Background(), pinnedMergeOpts())
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if result == nil || result.CommitSHA != "source-sha" || result.Noop {
		t.Fatalf("Merge result = %+v, want integrated source-sha", result)
	}
	calls := git.getCalls()
	if len(calls) != 6 {
		t.Fatalf("git calls = %d, want 6: %+v", len(calls), calls)
	}
	assertArgs(t, calls[4], "/repo", "merge", "--ff-only", "source-sha")
	assertArgs(t, calls[5], "/repo", "rev-parse", "main^{commit}")
	if len(remover.calls) != 1 || remover.calls[0] != "/tmp/pinned" {
		t.Fatalf("worktree removals = %v, want [/tmp/pinned]", remover.calls)
	}
}

func pinnedMergeOpts() Opts {
	return Opts{
		Branch:            "agent/pinned",
		Worktree:          "/tmp/pinned",
		ExpectedSourceSHA: "source-sha",
		ExpectedTargetSHA: "target-sha",
	}
}

func pinnedMergeSuccessResults() []mockResult {
	return []mockResult{
		{Stdout: "source-sha\n"},
		{Stdout: "source-sha\n"},
		{Stdout: "target-sha\n"},
		{Stdout: "/repo/.git\n"},
		{},
		{Stdout: "source-sha\n"},
	}
}
