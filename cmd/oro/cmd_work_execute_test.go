package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"oro/pkg/merge"
	"oro/pkg/protocol"
	"oro/pkg/worker"
)

// --- Mock implementations ---

// mockBeadSource records calls and returns pre-configured results.
type mockBeadSource struct {
	showDetail *protocol.BeadDetail
	showErr    error
	updates    []string // status values passed to Update
	updateErr  error
	closeID    string
	closeErr   error
}

func (m *mockBeadSource) Ready(_ context.Context) ([]protocol.Bead, error) { return nil, nil }
func (m *mockBeadSource) Show(_ context.Context, _ string) (*protocol.BeadDetail, error) {
	return m.showDetail, m.showErr
}

func (m *mockBeadSource) Close(_ context.Context, id, _ string) error {
	m.closeID = id
	return m.closeErr
}

func (m *mockBeadSource) Create(_ context.Context, _, _ string, _ int, _, _, _ string) (string, error) {
	return "", nil
}

func (m *mockBeadSource) Update(_ context.Context, _ string, status string) error {
	m.updates = append(m.updates, status)
	return m.updateErr
}

func (m *mockBeadSource) AllChildrenClosed(_ context.Context, _ string) (bool, error) {
	return true, nil
}
func (m *mockBeadSource) Sync(_ context.Context) error { return nil }

// mockWorktreeManager records Create/Remove calls.
type mockWorktreeManager struct {
	createPath   string
	createBranch string
	createErr    error
	removed      []string
	removeErr    error
}

func (m *mockWorktreeManager) Create(_ context.Context, beadID string) (string, string, error) {
	if m.createErr != nil {
		return "", "", m.createErr
	}
	return m.createPath, m.createBranch, nil
}

func (m *mockWorktreeManager) Remove(_ context.Context, path string) error {
	m.removed = append(m.removed, path)
	return m.removeErr
}
func (m *mockWorktreeManager) Prune(_ context.Context) error { return nil }

// mockProcess implements worker.Process with configurable exit.
type mockProcess struct {
	waitErr error
}

func (m *mockProcess) Wait() error { return m.waitErr }
func (m *mockProcess) Kill() error { return nil }

// mockSpawner implements worker.StreamingSpawner.
type mockSpawner struct {
	proc   worker.Process
	err    error
	called bool
}

func (m *mockSpawner) Spawn(_ context.Context, _, _, _ string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	m.called = true
	if m.err != nil {
		return nil, nil, nil, m.err
	}
	return m.proc, io.NopCloser(strings.NewReader("")), nil, nil
}

// mockMerger implements the merger interface.
type mockMerger struct {
	result *merge.Result
	err    error
	called bool
}

func (m *mockMerger) Merge(_ context.Context, _ merge.Opts) (*merge.Result, error) {
	m.called = true
	return m.result, m.err
}

// --- Test helpers ---

func testBead() *protocol.BeadDetail {
	return &protocol.BeadDetail{
		ID:                 "oro-test",
		Title:              "Test bead",
		AcceptanceCriteria: "Tests pass",
	}
}

func testDeps(bs *mockBeadSource, wt *mockWorktreeManager, sp *mockSpawner, mg *mockMerger, hasWork bool, qgPassed bool) *workDeps {
	return &workDeps{
		beadSrc:  bs,
		wtMgr:    wt,
		spawner:  sp,
		merger:   mg,
		repoRoot: "/tmp/test-repo",
		hasNewWork: func(_, _ string) bool {
			return hasWork
		},
		runQG: func(_ context.Context, _ string) (bool, string, error) {
			return qgPassed, "qg output", nil
		},
	}
}

// --- Tests ---

func TestExecuteWork_NoCommits_BailsOut(t *testing.T) {
	// When claude exits cleanly but produces no commits, executeWork should:
	// 1. NOT proceed to quality gate or merge
	// 2. Reset bead status to "open"
	// 3. Clean up worktree
	// 4. Return an error

	bs := &mockBeadSource{showDetail: testBead()}
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{result: &merge.Result{CommitSHA: "abc123"}}

	// hasNewWork=false: no commits were made
	deps := testDeps(bs, wt, sp, mg, false, true)

	cfg := &workConfig{
		beadID:     "oro-test",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}

	err := executeWork(context.Background(), cfg, deps)

	// Must return an error
	if err == nil {
		t.Fatal("expected error when no commits produced, got nil")
	}
	if !strings.Contains(err.Error(), "without producing commits") {
		t.Errorf("expected 'without producing commits' in error, got: %v", err)
	}

	// Claude should have been spawned
	if !sp.called {
		t.Error("expected spawner to be called")
	}

	// Merger should NOT have been called
	if mg.called {
		t.Error("merger should not be called when no work was done")
	}

	// Bead should be reset to open (updates: [in_progress, open])
	if len(bs.updates) < 2 {
		t.Fatalf("expected at least 2 bead updates, got %d: %v", len(bs.updates), bs.updates)
	}
	lastUpdate := bs.updates[len(bs.updates)-1]
	if lastUpdate != "open" {
		t.Errorf("expected last bead update to be 'open', got %q", lastUpdate)
	}

	// Worktree should be cleaned up
	if len(wt.removed) == 0 {
		t.Error("expected worktree to be removed")
	}
}

func TestExecuteWork_QGExhaustion_ResetsBead(t *testing.T) {
	// When the quality gate fails maxQGRetriesPerTier times on both tiers,
	// the bead should be reset to "open" (not left in_progress).

	bs := &mockBeadSource{showDetail: testBead()}
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{}

	// hasNewWork=true (claude made commits), but QG always fails
	deps := testDeps(bs, wt, sp, mg, true, false)

	cfg := &workConfig{
		beadID:     "oro-test",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}

	err := executeWork(context.Background(), cfg, deps)

	// Must return an error (QG exhaustion)
	if err == nil {
		t.Fatal("expected error on QG exhaustion, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *exitError, got %T: %v", err, err)
	}
	if ee.code != exitCodeRetries {
		t.Errorf("expected exit code %d, got %d", exitCodeRetries, ee.code)
	}

	// Bead should be reset to open via deferred cleanup
	lastUpdate := bs.updates[len(bs.updates)-1]
	if lastUpdate != "open" {
		t.Errorf("expected bead reset to 'open' after QG exhaustion, got %q (all updates: %v)", lastUpdate, bs.updates)
	}
}

func TestExecuteWork_MergeFail_ResetsBead(t *testing.T) {
	// When merge fails, bead should be reset to "open".

	bs := &mockBeadSource{showDetail: testBead()}
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{err: errors.New("merge: failed to get primary repo path")}

	// hasNewWork=true, QG passes, but merge fails
	deps := testDeps(bs, wt, sp, mg, true, true)

	cfg := &workConfig{
		beadID:     "oro-test",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}

	err := executeWork(context.Background(), cfg, deps)

	if err == nil {
		t.Fatal("expected error on merge failure, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *exitError, got %T: %v", err, err)
	}
	if ee.code != exitCodeMergeFail {
		t.Errorf("expected exit code %d, got %d", exitCodeMergeFail, ee.code)
	}

	// Bead should be reset to open
	lastUpdate := bs.updates[len(bs.updates)-1]
	if lastUpdate != "open" {
		t.Errorf("expected bead reset to 'open' after merge failure, got %q", lastUpdate)
	}
}

func TestExecuteWork_Success_NoReset(t *testing.T) {
	// On success, bead should be closed (not reset to open).

	bs := &mockBeadSource{showDetail: testBead()}
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{result: &merge.Result{CommitSHA: "abc123"}}

	deps := testDeps(bs, wt, sp, mg, true, true)

	cfg := &workConfig{
		beadID:     "oro-test",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}

	err := executeWork(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	// Bead should NOT have been reset to open — only in_progress
	for _, u := range bs.updates {
		if u == "open" {
			t.Error("bead should not be reset to open on success")
		}
	}

	// Bead should have been closed
	if bs.closeID != "oro-test" {
		t.Errorf("expected bead to be closed, closeID=%q", bs.closeID)
	}

	// Merger should have been called
	if !mg.called {
		t.Error("expected merger to be called on success")
	}
}
