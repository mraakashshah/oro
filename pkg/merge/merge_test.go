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
			// 1. git rebase main bead/abc — success
			{Stdout: "", Stderr: "", Err: nil},
			// 2. git checkout main — success
			{Stdout: "", Stderr: "", Err: nil},
			// 3. git merge --ff-only bead/abc — success
			{Stdout: "", Stderr: "", Err: nil},
			// 4. git rev-parse HEAD — returns commit SHA
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

	// Verify the git commands issued
	calls := mock.getCalls()
	if len(calls) != 4 {
		t.Fatalf("expected 4 git calls, got %d: %+v", len(calls), calls)
	}

	// Call 1: rebase
	assertArgs(t, calls[0], "/tmp/wt-abc", "rebase", "main", "bead/abc")
	// Call 2: checkout main
	assertArgs(t, calls[1], "/tmp/wt-abc", "checkout", "main")
	// Call 3: merge --ff-only
	assertArgs(t, calls[2], "/tmp/wt-abc", "merge", "--ff-only", "bead/abc")
	// Call 4: rev-parse HEAD
	assertArgs(t, calls[3], "/tmp/wt-abc", "rev-parse", "HEAD")
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
	if len(calls) != 2 {
		t.Fatalf("expected 2 git calls, got %d: %+v", len(calls), calls)
	}
	assertArgs(t, calls[0], "/tmp/wt-xyz", "rebase", "main", "bead/xyz")
	assertArgs(t, calls[1], "/tmp/wt-xyz", "rebase", "--abort")
}

func TestMerge_LockPreventsConcurrentMerges(t *testing.T) { //nolint:funlen // concurrency test requires sequential setup
	// First merge blocks until we signal it
	var firstMergeStarted atomic.Bool
	unblockFirst := make(chan struct{})

	// A blocking GitRunner for the first merge
	blockingRunner := &blockingGitRunner{
		onFirstCall: func() {
			firstMergeStarted.Store(true)
			<-unblockFirst // block until signaled
		},
		results: []mockResult{
			{Stdout: "", Stderr: "", Err: nil},       // rebase
			{Stdout: "", Stderr: "", Err: nil},       // checkout
			{Stdout: "", Stderr: "", Err: nil},       // merge
			{Stdout: "sha1\n", Stderr: "", Err: nil}, // rev-parse
			{Stdout: "", Stderr: "", Err: nil},       // rebase (second merge)
			{Stdout: "", Stderr: "", Err: nil},       // checkout
			{Stdout: "", Stderr: "", Err: nil},       // merge
			{Stdout: "sha2\n", Stderr: "", Err: nil}, // rev-parse
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
	for !firstMergeStarted.Load() {
		time.Sleep(time.Millisecond)
	}

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

	// Give second goroutine time to attempt to acquire the lock
	time.Sleep(50 * time.Millisecond)

	// Unblock the first merge
	close(unblockFirst)

	wg.Wait()

	// The second merge must have started its git operations after the first finished
	// Verify all 8 git calls happened sequentially (4 per merge)
	calls := blockingRunner.getCalls()
	if len(calls) != 8 {
		t.Fatalf("expected 8 git calls, got %d", len(calls))
	}

	// First 4 calls should be for bead/first
	if !containsArg(calls[0].Args, "bead/first") {
		t.Errorf("expected first call to be for bead/first, got %v", calls[0].Args)
	}
	// Last 4 calls should be for bead/second (rebase call)
	if !containsArg(calls[4].Args, "bead/second") {
		t.Errorf("expected fifth call to be for bead/second, got %v", calls[4].Args)
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

	// Wait for the git command to start
	time.Sleep(20 * time.Millisecond)

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
	// Rebase succeeds but merge --ff-only fails (main moved)
	mock := &mockGitRunner{
		results: []mockResult{
			// 1. git rebase — success
			{Stdout: "", Stderr: "", Err: nil},
			// 2. git checkout main — success
			{Stdout: "", Stderr: "", Err: nil},
			// 3. git merge --ff-only — fails
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
	// Should NOT be a ConflictError — this is a different kind of failure
	var conflictErr3 *ConflictError
	if errors.As(err, &conflictErr3) {
		t.Error("ff-only failure should not produce ConflictError")
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
