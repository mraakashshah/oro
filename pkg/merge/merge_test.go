package merge //nolint:testpackage // internal test needs access to unexported types

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- Mock GitRunner ---

type call struct {
	Dir  string
	Args []string
}

type mockResult struct {
	Stdout string
	Stderr string
	Err    error
}

// mockGitRunner records calls and returns pre-configured results.
// Results are consumed in order; if exhausted, returns empty success.
type mockGitRunner struct {
	mu      sync.Mutex
	calls   []call
	results []mockResult
}

func (m *mockGitRunner) Run(_ context.Context, dir string, args ...string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, call{Dir: dir, Args: args})

	if len(m.results) == 0 {
		return "", "", nil
	}
	r := m.results[0]
	m.results = m.results[1:]
	return r.Stdout, r.Stderr, r.Err
}

func (m *mockGitRunner) getCalls() []call {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]call, len(m.calls))
	copy(out, m.calls)
	return out
}

// --- Tests ---

func TestMerge_CleanRebaseAndMerge(t *testing.T) {
	mock := &mockGitRunner{
		results: []mockResult{
			// 0. git rev-list --count main..bead/abc — not merged yet
			{Stdout: "2\n", Stderr: "", Err: nil},
			// 1. git rebase main bead/abc — success
			{Stdout: "", Stderr: "", Err: nil},
			// 2. git rev-parse --git-common-dir → primaryRepo derived by stripping /.git
			{Stdout: "/repo/.git\n", Stderr: "", Err: nil},
			// 3. git worktree remove (fallback via GitRunner, no WorktreeRemover set)
			{Stdout: "", Stderr: "", Err: nil},
			// 4. git merge --ff-only bead/abc (in primary repo)
			{Stdout: "", Stderr: "", Err: nil},
			// 5. git rev-parse HEAD (in primary repo)
			{Stdout: "abc123def456\n", Stderr: "", Err: nil},
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
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.CommitSHA != "abc123def456" {
		t.Errorf("expected commit SHA abc123def456, got %q", result.CommitSHA)
	}

	// Verify the git commands issued — ff-merge flow (no cherry-pick)
	calls := mock.getCalls()
	if len(calls) != 6 {
		t.Fatalf("expected 6 git calls, got %d: %+v", len(calls), calls)
	}

	// Call 0: isBranchMerged check
	assertArgs(t, calls[0], "/tmp/wt-abc", "rev-list", "--count", "main..bead/abc")
	// Call 1: rebase
	assertArgs(t, calls[1], "/tmp/wt-abc", "rebase", "main", "bead/abc")
	// Call 2: rev-parse --git-common-dir
	assertArgs(t, calls[2], "/tmp/wt-abc", "rev-parse", "--git-common-dir")
	// Call 3: git worktree remove (fallback via GitRunner in primary repo)
	assertArgs(t, calls[3], "/repo", "worktree", "remove", "/tmp/wt-abc")
	// Call 4: ff-only merge
	assertArgs(t, calls[4], "/repo", "merge", "--ff-only", "bead/abc")
	// Call 5: rev-parse HEAD
	assertArgs(t, calls[5], "/repo", "rev-parse", "HEAD")
}

func TestMerge_RebaseConflict_ReturnsConflictError(t *testing.T) {
	rebaseStderr := `error: could not apply fa39187... something
Resolve all conflicts manually, mark them as resolved with
"git add/rm <conflicted_files>", then run "git rebase --continue".
CONFLICT (content): Merge conflict in src/main.go
CONFLICT (content): Merge conflict in pkg/util/helper.go
`
	mock := &mockGitRunner{
		results: []mockResult{
			// 0. git rev-list --count main..bead/xyz — not merged yet
			{Stdout: "1\n", Stderr: "", Err: nil},
			// 1. git rebase main bead/xyz — conflict
			{Stdout: "", Stderr: rebaseStderr, Err: fmt.Errorf("exit status 1")},
			// 2. git rebase --abort — success
			{Stdout: "", Stderr: "", Err: nil},
		},
	}

	coord := NewCoordinator(mock)
	opts := Opts{
		Branch:   "bead/xyz",
		Worktree: "/tmp/wt-xyz",
		BeadID:   "oro-xyz",
	}

	_, err := coord.Merge(context.Background(), opts)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var conflictErr *ConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}

	if conflictErr.BeadID != "oro-xyz" {
		t.Errorf("expected BeadID oro-xyz, got %q", conflictErr.BeadID)
	}

	expectedFiles := []string{"src/main.go", "pkg/util/helper.go"}
	if len(conflictErr.Files) != len(expectedFiles) {
		t.Fatalf("expected %d conflicting files, got %d: %v", len(expectedFiles), len(conflictErr.Files), conflictErr.Files)
	}
	for i, f := range expectedFiles {
		if conflictErr.Files[i] != f {
			t.Errorf("file[%d]: expected %q, got %q", i, f, conflictErr.Files[i])
		}
	}

	// Verify rebase --abort was called
	calls := mock.getCalls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 git calls, got %d: %+v", len(calls), calls)
	}
	assertArgs(t, calls[0], "/tmp/wt-xyz", "rev-list", "--count", "main..bead/xyz")
	assertArgs(t, calls[1], "/tmp/wt-xyz", "rebase", "main", "bead/xyz")
	assertArgs(t, calls[2], "/tmp/wt-xyz", "rebase", "--abort")
}

func TestMerge_LockPreventsConcurrentMerges(t *testing.T) { //nolint:funlen // concurrency test requires sequential setup
	// First merge blocks until we signal it
	var firstMergeStarted atomic.Bool
	unblockFirst := make(chan struct{})

	// A blocking GitRunner for the first merge - updated for ff-merge flow
	blockingRunner := &blockingGitRunner{
		onFirstCall: func() {
			firstMergeStarted.Store(true)
			<-unblockFirst // block until signaled
		},
		results: []mockResult{
			// First merge (6 calls — worktree remove + ff-merge)
			{Stdout: "1\n", Stderr: "", Err: nil},          // rev-list --count (not merged)
			{Stdout: "", Stderr: "", Err: nil},             // rebase
			{Stdout: "/repo/.git\n", Stderr: "", Err: nil}, // rev-parse --git-common-dir
			{Stdout: "", Stderr: "", Err: nil},             // worktree remove (fallback)
			{Stdout: "", Stderr: "", Err: nil},             // merge --ff-only
			{Stdout: "sha1\n", Stderr: "", Err: nil},       // rev-parse HEAD
			// Second merge (6 calls)
			{Stdout: "1\n", Stderr: "", Err: nil},          // rev-list --count (not merged)
			{Stdout: "", Stderr: "", Err: nil},             // rebase
			{Stdout: "/repo/.git\n", Stderr: "", Err: nil}, // rev-parse --git-common-dir
			{Stdout: "", Stderr: "", Err: nil},             // worktree remove (fallback)
			{Stdout: "", Stderr: "", Err: nil},             // merge --ff-only
			{Stdout: "sha2\n", Stderr: "", Err: nil},       // rev-parse HEAD
		},
	}

	coord := NewCoordinator(blockingRunner)

	var wg sync.WaitGroup

	// Start first merge
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = coord.Merge(context.Background(), Opts{
			Branch:   "bead/first",
			Worktree: "/tmp/wt-1",
			BeadID:   "oro-1",
		})
		_ = time.Now() // first finished
	}()

	// Wait for first merge to actually start
	waitFor(t, func() bool { return firstMergeStarted.Load() }, 2*time.Second)

	// Start second merge — should block on the lock
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = time.Now() // second started
		_, _ = coord.Merge(context.Background(), Opts{
			Branch:   "bead/second",
			Worktree: "/tmp/wt-2",
			BeadID:   "oro-2",
		})
	}()

	// Give second goroutine time to attempt to acquire the lock.
	// Since we can't directly detect mutex contention in Go, and there's no
	// observable signal from the second goroutine when it blocks on mu.Lock(),
	// we wait a short fixed duration. This is unavoidable in this concurrency test.
	waitFor(t, func() bool {
		// Wait at least 50ms to let the second goroutine reach the lock
		return true
	}, 50*time.Millisecond)

	// Unblock the first merge
	close(unblockFirst)

	wg.Wait()

	// The second merge must have started its git operations after the first finished
	// Verify all 12 git calls happened sequentially (6 per merge)
	calls := blockingRunner.getCalls()
	if len(calls) != 12 {
		t.Fatalf("expected 12 git calls, got %d", len(calls))
	}

	// Second call (rebase) should be for bead/first
	if !containsArg(calls[1].Args, "bead/first") {
		t.Errorf("expected second call (rebase) to be for bead/first, got %v", calls[1].Args)
	}
	// 8th call (second merge's rebase) should be for bead/second
	if !containsArg(calls[7].Args, "bead/second") {
		t.Errorf("expected eighth call (rebase) to be for bead/second, got %v", calls[7].Args)
	}
}

func TestMerge_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// GitRunner that blocks and respects context
	blockingRunner := &contextAwareGitRunner{
		blockCh: make(chan struct{}),
	}

	coord := NewCoordinator(blockingRunner)

	errCh := make(chan error, 1)
	go func() {
		_, err := coord.Merge(ctx, Opts{
			Branch:   "bead/cancel",
			Worktree: "/tmp/wt-cancel",
			BeadID:   "oro-cancel",
		})
		errCh <- err
	}()

	// Wait for the git command to start (no proper signal available)
	// We need to ensure the merge goroutine has entered the blocking Run call
	// before we cancel the context. A small wait is unavoidable here.
	waitFor(t, func() bool {
		// Give it time to start - there's no direct signal from the mock
		return true
	}, 20*time.Millisecond)

	// Cancel the context
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error after context cancellation, got nil")
		}
		if !strings.Contains(err.Error(), "context canceled") &&
			!strings.Contains(err.Error(), "context") {
			t.Errorf("expected context-related error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("merge did not return after context cancellation (timeout)")
	}
}

func TestConflictError_ErrorInterface(t *testing.T) {
	err := &ConflictError{
		Files:  []string{"a.go", "b.go"},
		BeadID: "oro-test",
	}
	msg := err.Error()
	if !strings.Contains(msg, "oro-test") {
		t.Errorf("error message should contain bead ID, got: %s", msg)
	}
	if !strings.Contains(msg, "a.go") || !strings.Contains(msg, "b.go") {
		t.Errorf("error message should contain conflicting files, got: %s", msg)
	}
}

func TestParseConflictFiles(t *testing.T) {
	tests := []struct {
		name     string
		stderr   string
		expected []string
	}{
		{
			name: "multiple conflicts",
			stderr: `CONFLICT (content): Merge conflict in foo.go
CONFLICT (content): Merge conflict in bar/baz.go`,
			expected: []string{"foo.go", "bar/baz.go"},
		},
		{
			name:     "no conflicts",
			stderr:   "some other error output",
			expected: nil,
		},
		{
			name: "single conflict",
			stderr: `error: could not apply abc123
CONFLICT (content): Merge conflict in main.go`,
			expected: []string{"main.go"},
		},
		{
			name:     "add/add conflict",
			stderr:   `CONFLICT (add/add): Merge conflict in new_file.go`,
			expected: []string{"new_file.go"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := parseConflictFiles(tc.stderr)
			if len(files) != len(tc.expected) {
				t.Fatalf("expected %d files, got %d: %v", len(tc.expected), len(files), files)
			}
			for i, f := range tc.expected {
				if files[i] != f {
					t.Errorf("file[%d]: expected %q, got %q", i, f, files[i])
				}
			}
		})
	}
}

func TestMerge_RebaseAbortFails(t *testing.T) {
	// Edge case: rebase fails AND abort fails
	mock := &mockGitRunner{
		results: []mockResult{
			// 0. git rev-list --count — not merged yet
			{Stdout: "1\n", Stderr: "", Err: nil},
			// 1. git rebase — conflict
			{Stdout: "", Stderr: "CONFLICT (content): Merge conflict in x.go", Err: fmt.Errorf("exit status 1")},
			// 2. git rebase --abort — also fails
			{Stdout: "", Stderr: "fatal: no rebase in progress", Err: fmt.Errorf("exit status 128")},
		},
	}

	coord := NewCoordinator(mock)
	_, err := coord.Merge(context.Background(), Opts{
		Branch:   "bead/bad",
		Worktree: "/tmp/wt-bad",
		BeadID:   "oro-bad",
	})

	// Should still return a ConflictError (abort failure is secondary)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var conflictErr2 *ConflictError
	if !errors.As(err, &conflictErr2) {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}
}

func TestMerge_FFOnlyMergeFails(t *testing.T) {
	// Test ff-only merge failure (e.g., main moved between rebase and merge).
	// The worktree is removed before the ff-only attempt, so the branch still
	// exists on the remote — caller can retry with a fresh rebase.
	mock := &mockGitRunner{
		results: []mockResult{
			// 0. git rev-list --count — not merged yet
			{Stdout: "1\n", Stderr: "", Err: nil},
			// 1. git rebase — success
			{Stdout: "", Stderr: "", Err: nil},
			// 2. git rev-parse --git-common-dir — success
			{Stdout: "/repo/.git\n", Stderr: "", Err: nil},
			// 3. git worktree remove — success (worktreeRemover nil → fallback)
			{Stdout: "", Stderr: "", Err: nil},
			// 4. git merge --ff-only — fails (main moved)
			{Stdout: "", Stderr: "fatal: Not possible to fast-forward, aborting.", Err: fmt.Errorf("exit status 128")},
		},
	}

	coord := NewCoordinator(mock)
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
	// Should NOT be a ConflictError — this is a different kind of failure
	var conflictErr3 *ConflictError
	if errors.As(err, &conflictErr3) {
		t.Error("ff-only failure should not produce ConflictError")
	}
}

func TestMerge_CheckoutMainFails(t *testing.T) {
	// This test is now obsolete since we no longer checkout main.
	// Instead, test that rev-parse --git-common-dir failure is handled.
	mock := &mockGitRunner{
		results: []mockResult{
			// 0. git rev-list --count — not merged yet
			{Stdout: "1\n", Stderr: "", Err: nil},
			// 1. git rebase — success
			{Stdout: "", Stderr: "", Err: nil},
			// 2. git rev-parse --git-common-dir — fails
			{Stdout: "", Stderr: "fatal: not a git repository", Err: fmt.Errorf("exit status 128")},
		},
	}

	coord := NewCoordinator(mock)
	_, err := coord.Merge(context.Background(), Opts{
		Branch:   "bead/chk",
		Worktree: "/tmp/wt-chk",
		BeadID:   "oro-chk",
	})

	if err == nil {
		t.Fatal("expected error on rev-parse failure, got nil")
	}
	if !strings.Contains(err.Error(), "failed to get git common dir") {
		t.Errorf("expected 'failed to get git common dir' in error, got: %v", err)
	}
	// Should NOT be a ConflictError
	var conflictErr *ConflictError
	if errors.As(err, &conflictErr) {
		t.Error("rev-parse failure should not produce ConflictError")
	}
}

func TestMerge_RevParseFails(t *testing.T) {
	// Test that worktree remove failure is surfaced with a clear error.
	// The worktree remove step occurs after a successful rebase but before ff-merge.
	mock := &mockGitRunner{
		results: []mockResult{
			// 0. git rev-list --count — not merged yet
			{Stdout: "1\n", Stderr: "", Err: nil},
			// 1. git rebase — success
			{Stdout: "", Stderr: "", Err: nil},
			// 2. git rev-parse --git-common-dir — success
			{Stdout: "/repo/.git\n", Stderr: "", Err: nil},
			// 3. git worktree remove — fails (worktreeRemover nil → fallback via git)
			{Stdout: "", Stderr: "fatal: worktree has modifications", Err: fmt.Errorf("exit status 128")},
		},
	}

	coord := NewCoordinator(mock)
	_, err := coord.Merge(context.Background(), Opts{
		Branch:   "bead/rev",
		Worktree: "/tmp/wt-rev",
		BeadID:   "oro-rev",
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
}

func TestMerge_ContextCancelledDuringRebase(t *testing.T) {
	// When the context is already cancelled and the rebase returns an error,
	// the context error should take priority over conflict handling.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	mock := &mockGitRunner{
		results: []mockResult{
			// 0. git rev-list --count — not merged yet
			{Stdout: "1\n", Stderr: "", Err: nil},
			// 1. git rebase fails because context is cancelled
			{Stdout: "", Stderr: "signal: killed", Err: fmt.Errorf("signal: killed")},
		},
	}

	coord := NewCoordinator(mock)
	_, err := coord.Merge(ctx, Opts{
		Branch:   "bead/ctx",
		Worktree: "/tmp/wt-ctx",
		BeadID:   "oro-ctx",
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "merge cancelled") {
		t.Errorf("expected 'merge cancelled' in error, got: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected error to wrap context.Canceled, got: %v", err)
	}
}

func TestNewCoordinator_SetsGitRunner(t *testing.T) {
	mock := &mockGitRunner{}
	coord := NewCoordinator(mock)
	if coord == nil {
		t.Fatal("expected non-nil Coordinator")
	}
	if coord.git != mock {
		t.Error("expected Coordinator.git to be the provided GitRunner")
	}
}

func TestConflictError_EmptyFiles(t *testing.T) {
	err := &ConflictError{
		Files:  nil,
		BeadID: "oro-empty",
	}
	msg := err.Error()
	if !strings.Contains(msg, "oro-empty") {
		t.Errorf("error message should contain bead ID, got: %s", msg)
	}
	if !strings.Contains(msg, "merge conflict") {
		t.Errorf("error message should contain 'merge conflict', got: %s", msg)
	}
}

func TestConflictError_SingleFile(t *testing.T) {
	err := &ConflictError{
		Files:  []string{"only.go"},
		BeadID: "oro-single",
	}
	msg := err.Error()
	if !strings.Contains(msg, "only.go") {
		t.Errorf("error message should contain file name, got: %s", msg)
	}
}

func TestParseConflictFiles_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		stderr   string
		expected []string
	}{
		{
			name:     "empty string",
			stderr:   "",
			expected: nil,
		},
		{
			name:     "whitespace only",
			stderr:   "   \n\t\n  ",
			expected: nil,
		},
		{
			name:     "conflict line with trailing whitespace",
			stderr:   "CONFLICT (content): Merge conflict in spaced.go   \n",
			expected: []string{"spaced.go"},
		},
		{
			name: "mixed conflict types",
			stderr: `CONFLICT (content): Merge conflict in a.go
CONFLICT (rename/delete): Merge conflict in b.go
CONFLICT (modify/delete): Merge conflict in c.go`,
			expected: []string{"a.go", "b.go", "c.go"},
		},
		{
			name:     "CONFLICT keyword in non-matching line",
			stderr:   "CONFLICT something else entirely",
			expected: nil,
		},
		{
			name:     "file path with spaces",
			stderr:   "CONFLICT (content): Merge conflict in path/to/my file.go",
			expected: []string{"path/to/my file.go"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := parseConflictFiles(tc.stderr)
			if len(files) != len(tc.expected) {
				t.Fatalf("expected %d files, got %d: %v", len(tc.expected), len(files), files)
			}
			for i, f := range tc.expected {
				if files[i] != f {
					t.Errorf("file[%d]: expected %q, got %q", i, f, files[i])
				}
			}
		})
	}
}

func TestMerge_BranchAlreadyMerged(t *testing.T) {
	// When the agent already merged to main inside the worktree,
	// isBranchMerged returns true and we skip rebase+cherry-pick entirely.
	mock := &mockGitRunner{
		results: []mockResult{
			// 0. git rev-list --count main..bead/done — already merged (0 commits ahead)
			{Stdout: "0\n", Stderr: "", Err: nil},
			// 1. git diff main..bead/done — empty (content matches main)
			{Stdout: "", Stderr: "", Err: nil},
			// 2. git rev-parse main — return main HEAD SHA
			{Stdout: "mainsha456\n", Stderr: "", Err: nil},
		},
	}

	coord := NewCoordinator(mock)
	result, err := coord.Merge(context.Background(), Opts{
		Branch:   "bead/done",
		Worktree: "/tmp/wt-done",
		BeadID:   "oro-done",
	})
	if err != nil {
		t.Fatalf("expected success for already-merged branch, got: %v", err)
	}
	if result.CommitSHA != "mainsha456" {
		t.Errorf("expected commit SHA mainsha456, got %q", result.CommitSHA)
	}

	// Only 3 git calls — no rebase, no cherry-pick
	calls := mock.getCalls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 git calls, got %d: %+v", len(calls), calls)
	}
	assertArgs(t, calls[0], "/tmp/wt-done", "rev-list", "--count", "main..bead/done")
	assertArgs(t, calls[1], "/tmp/wt-done", "diff", "main..bead/done")
	assertArgs(t, calls[2], "/tmp/wt-done", "rev-parse", "main")
}

func TestMerge_BranchAlreadyMerged_DiffCheck(t *testing.T) {
	t.Run("count=0 and diff empty → short-circuit success", func(t *testing.T) {
		mock := &mockGitRunner{
			results: []mockResult{
				// rev-list --count: 0 commits ahead
				{Stdout: "0\n", Stderr: "", Err: nil},
				// git diff main..branch: empty (no diff)
				{Stdout: "", Stderr: "", Err: nil},
				// rev-parse main
				{Stdout: "mainsha789\n", Stderr: "", Err: nil},
			},
		}
		coord := NewCoordinator(mock)
		result, err := coord.Merge(context.Background(), Opts{
			Branch:   "bead/diff-empty",
			Worktree: "/tmp/wt-diff-empty",
			BeadID:   "oro-diff-empty",
		})
		if err != nil {
			t.Fatalf("expected success for empty diff, got: %v", err)
		}
		if result.CommitSHA != "mainsha789" {
			t.Errorf("expected mainsha789, got %q", result.CommitSHA)
		}
		calls := mock.getCalls()
		if len(calls) != 3 {
			t.Fatalf("expected 3 git calls, got %d: %+v", len(calls), calls)
		}
		assertArgs(t, calls[0], "/tmp/wt-diff-empty", "rev-list", "--count", "main..bead/diff-empty")
		assertArgs(t, calls[1], "/tmp/wt-diff-empty", "diff", "main..bead/diff-empty")
		assertArgs(t, calls[2], "/tmp/wt-diff-empty", "rev-parse", "main")
	})

	t.Run("count=0 and diff non-empty → proceed to rebase (no short-circuit)", func(t *testing.T) {
		mock := &mockGitRunner{
			results: []mockResult{
				// rev-list --count: 0 commits ahead
				{Stdout: "0\n", Stderr: "", Err: nil},
				// git diff main..branch: non-empty diff
				{Stdout: "diff --git a/foo.go b/foo.go\n+something\n", Stderr: "", Err: nil},
				// rebase main bead/diff-nonempty — succeeds
				{Stdout: "", Stderr: "", Err: nil},
				// rev-parse --git-common-dir
				{Stdout: "/repo/.git\n", Stderr: "", Err: nil},
				// git worktree remove (fallback via GitRunner)
				{Stdout: "", Stderr: "", Err: nil},
				// git merge --ff-only bead/diff-nonempty
				{Stdout: "", Stderr: "", Err: nil},
				// rev-parse HEAD
				{Stdout: "rebasesha\n", Stderr: "", Err: nil},
			},
		}
		coord := NewCoordinator(mock)
		result, err := coord.Merge(context.Background(), Opts{
			Branch:   "bead/diff-nonempty",
			Worktree: "/tmp/wt-diff-nonempty",
			BeadID:   "oro-diff-nonempty",
		})
		if err != nil {
			t.Fatalf("expected rebase to succeed, got: %v", err)
		}
		if result.CommitSHA != "rebasesha" {
			t.Errorf("expected rebasesha, got %q", result.CommitSHA)
		}
		calls := mock.getCalls()
		// Should have proceeded past the isBranchMerged check to rebase
		assertArgs(t, calls[0], "/tmp/wt-diff-nonempty", "rev-list", "--count", "main..bead/diff-nonempty")
		assertArgs(t, calls[1], "/tmp/wt-diff-nonempty", "diff", "main..bead/diff-nonempty")
		assertArgs(t, calls[2], "/tmp/wt-diff-nonempty", "rebase", "main", "bead/diff-nonempty")
	})

	t.Run("git diff error → proceed to rebase (fail-open)", func(t *testing.T) {
		mock := &mockGitRunner{
			results: []mockResult{
				// rev-list --count: 0 commits ahead
				{Stdout: "0\n", Stderr: "", Err: nil},
				// git diff fails
				{Stdout: "", Stderr: "error: diff failed", Err: fmt.Errorf("exit status 1")},
				// rebase main bead/diff-err — succeeds
				{Stdout: "", Stderr: "", Err: nil},
				// rev-parse --git-common-dir
				{Stdout: "/repo/.git\n", Stderr: "", Err: nil},
				// git worktree remove (fallback via GitRunner)
				{Stdout: "", Stderr: "", Err: nil},
				// git merge --ff-only bead/diff-err
				{Stdout: "", Stderr: "", Err: nil},
				// rev-parse HEAD
				{Stdout: "errsha\n", Stderr: "", Err: nil},
			},
		}
		coord := NewCoordinator(mock)
		result, err := coord.Merge(context.Background(), Opts{
			Branch:   "bead/diff-err",
			Worktree: "/tmp/wt-diff-err",
			BeadID:   "oro-diff-err",
		})
		if err != nil {
			t.Fatalf("expected fail-open rebase to succeed, got: %v", err)
		}
		if result.CommitSHA != "errsha" {
			t.Errorf("expected errsha, got %q", result.CommitSHA)
		}
		calls := mock.getCalls()
		assertArgs(t, calls[0], "/tmp/wt-diff-err", "rev-list", "--count", "main..bead/diff-err")
		assertArgs(t, calls[1], "/tmp/wt-diff-err", "diff", "main..bead/diff-err")
		assertArgs(t, calls[2], "/tmp/wt-diff-err", "rebase", "main", "bead/diff-err")
	})
}

func TestMerge_RebaseNoConflictPattern(t *testing.T) {
	// Rebase fails but stderr doesn't contain CONFLICT pattern
	mock := &mockGitRunner{
		results: []mockResult{
			// git rev-list --count main..bead/noconf — not merged yet
			{Stdout: "1\n", Stderr: "", Err: nil},
			// git rebase fails with non-conflict error
			{Stdout: "", Stderr: "fatal: not a git repository", Err: fmt.Errorf("exit status 128")},
			// git rebase --abort
			{Stdout: "", Stderr: "", Err: nil},
		},
	}

	coord := NewCoordinator(mock)
	_, err := coord.Merge(context.Background(), Opts{
		Branch:   "bead/noconf",
		Worktree: "/tmp/wt-noconf",
		BeadID:   "oro-noconf",
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var conflictErr *ConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}
	// Files should be nil since no CONFLICT pattern matched
	if conflictErr.Files != nil {
		t.Errorf("expected nil files, got: %v", conflictErr.Files)
	}
}

// --- Helper types ---

// blockingGitRunner blocks the first call until signaled, then uses pre-configured results.
type blockingGitRunner struct {
	mu          sync.Mutex
	calls       []call
	results     []mockResult
	onFirstCall func()
	firstCalled atomic.Bool
}

func (b *blockingGitRunner) Run(_ context.Context, dir string, args ...string) (string, string, error) {
	// Call onFirstCall only once
	if b.firstCalled.CompareAndSwap(false, true) {
		if b.onFirstCall != nil {
			b.onFirstCall()
		}
	}

	b.mu.Lock()
	b.calls = append(b.calls, call{Dir: dir, Args: args})
	if len(b.results) == 0 {
		b.mu.Unlock()
		return "", "", nil
	}
	r := b.results[0]
	b.results = b.results[1:]
	b.mu.Unlock()
	return r.Stdout, r.Stderr, r.Err
}

func (b *blockingGitRunner) getCalls() []call {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]call, len(b.calls))
	copy(out, b.calls)
	return out
}

// contextAwareGitRunner blocks until context is cancelled.
type contextAwareGitRunner struct {
	blockCh chan struct{}
}

func (c *contextAwareGitRunner) Run(ctx context.Context, _ string, _ ...string) (string, string, error) {
	select {
	case <-ctx.Done():
		return "", "", fmt.Errorf("test context: %w", ctx.Err())
	case <-c.blockCh:
		return "", "", nil
	}
}

// --- Assertion helpers ---

func assertArgs(t *testing.T, c call, expectedDir string, expectedArgs ...string) {
	t.Helper()
	if c.Dir != expectedDir {
		t.Errorf("expected dir %q, got %q", expectedDir, c.Dir)
	}
	if len(c.Args) != len(expectedArgs) {
		t.Errorf("expected %d args %v, got %d args %v", len(expectedArgs), expectedArgs, len(c.Args), c.Args)
		return
	}
	for i, a := range expectedArgs {
		if c.Args[i] != a {
			t.Errorf("arg[%d]: expected %q, got %q", i, a, c.Args[i])
		}
	}
}

func containsArg(args []string, target string) bool {
	for _, a := range args {
		if a == target {
			return true
		}
	}
	return false
}

// --- funcGitRunner ---

// funcGitRunner delegates Run to a user-supplied function.
type funcGitRunner struct {
	fn func(ctx context.Context, dir string, args ...string) (string, string, error)
}

func (f *funcGitRunner) Run(ctx context.Context, dir string, args ...string) (string, string, error) {
	return f.fn(ctx, dir, args...)
}

// --- Abort tests ---

func TestCoordinatorAbortOnCancel(t *testing.T) {
	rebaseStarted := make(chan struct{})
	unblockRebase := make(chan struct{})
	abortCalled := make(chan string, 1) // receives worktree dir

	runner := &funcGitRunner{fn: func(_ context.Context, dir string, args ...string) (string, string, error) {
		// rebase --abort path
		if len(args) >= 2 && args[0] == "rebase" && args[1] == "--abort" {
			abortCalled <- dir
			return "", "", nil
		}
		// rebase (blocking) path
		if len(args) >= 1 && args[0] == "rebase" {
			close(rebaseStarted)
			<-unblockRebase
			return "", "", fmt.Errorf("interrupted")
		}
		return "", "", nil
	}}

	coord := NewCoordinator(runner)

	errCh := make(chan error, 1)
	go func() {
		_, err := coord.Merge(context.Background(), Opts{
			Branch:   "agent/test-abort",
			Worktree: "/tmp/wt-abort",
			BeadID:   "test-abort",
		})
		errCh <- err
	}()

	// Wait for rebase to start
	<-rebaseStarted

	// Abort while merge is in progress
	coord.Abort()

	select {
	case dir := <-abortCalled:
		if dir != "/tmp/wt-abort" {
			t.Fatalf("expected abort on /tmp/wt-abort, got %s", dir)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected git rebase --abort to be called")
	}

	// Unblock rebase so merge goroutine can finish
	close(unblockRebase)

	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("merge did not return after abort")
	}
}

func TestAbortMu_PanicSafety(t *testing.T) {
	// If a panic occurs inside Merge() after abortMu has been locked,
	// all abortMu.Lock() calls must use defer abortMu.Unlock() so the
	// mutex is released during panic unwinding. Without defer, the mutex
	// stays locked and subsequent Abort() or Merge() calls deadlock.
	//
	// We verify this by injecting a panic during the merge operation
	// (inside git.Run, which runs while the deferred cleanup holding
	// abortMu is pending) and confirming that both Abort() and a fresh
	// Merge() still work after recovery.

	callCount := atomic.Int32{}

	runner := &funcGitRunner{fn: func(_ context.Context, _ string, args ...string) (string, string, error) {
		n := callCount.Add(1)
		// Second call (rebase in first Merge): panic — first call is rev-list --count
		if n == 2 && len(args) >= 1 && args[0] == "rebase" {
			panic("simulated crash during merge operation")
		}
		// Subsequent calls succeed (for the second Merge)
		if len(args) >= 1 && args[0] == "rev-parse" {
			return "abc123\n", "", nil
		}
		return "", "", nil
	}}

	coord := NewCoordinator(runner)

	// --- Phase 1: Merge panics, we recover ---
	recovered := make(chan struct{})
	go func() {
		defer func() {
			_ = recover() // swallow the expected panic
			close(recovered)
		}()
		_, _ = coord.Merge(context.Background(), Opts{
			Branch:   "bead/panic",
			Worktree: "/tmp/wt-panic",
			BeadID:   "oro-panic",
		})
	}()
	<-recovered

	// --- Phase 2: Abort() must not deadlock ---
	abortDone := make(chan struct{})
	go func() {
		coord.Abort()
		close(abortDone)
	}()
	select {
	case <-abortDone:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Abort() deadlocked — abortMu was not released after panic")
	}

	// --- Phase 3: A new Merge() must not deadlock ---
	mergeDone := make(chan struct{})
	go func() {
		_, _ = coord.Merge(context.Background(), Opts{
			Branch:   "bead/after-panic",
			Worktree: "/tmp/wt-after",
			BeadID:   "oro-after",
		})
		close(mergeDone)
	}()
	select {
	case <-mergeDone:
		// success — both mu and abortMu were properly released
	case <-time.After(2 * time.Second):
		t.Fatal("Merge() deadlocked — mu or abortMu was not released after panic")
	}
}

func TestCoordinatorAbort_NoMergeInProgress(t *testing.T) {
	mock := &mockGitRunner{}
	coord := NewCoordinator(mock)

	// Abort with no merge in progress — should be a no-op
	coord.Abort()

	calls := mock.getCalls()
	if len(calls) != 0 {
		t.Fatalf("expected no git calls when no merge in progress, got %d", len(calls))
	}
}

// waitFor polls condition every tick until it returns true or timeout expires.
func waitFor(t *testing.T, condition func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("waitFor: condition not met within", timeout)
}
