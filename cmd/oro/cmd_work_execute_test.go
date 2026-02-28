package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/memory"
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

func (m *mockBeadSource) HasChildren(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (m *mockBeadSource) InProgress(_ context.Context) ([]protocol.Bead, error) { return nil, nil }
func (m *mockBeadSource) Sync(_ context.Context) error                          { return nil }

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
func (m *mockWorktreeManager) Prune(_ context.Context) error                  { return nil }
func (m *mockWorktreeManager) DeleteBranch(_ context.Context, _ string) error { return nil }

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
		runQG: func(_ context.Context, _ string, _ bool) (bool, string, error) {
			return qgPassed, "qg output", nil
		},
	}
}

// contentSpawner returns configurable stdout content.
type contentSpawner struct {
	proc    worker.Process
	content string
	called  bool
}

func (m *contentSpawner) Spawn(_ context.Context, _, _, _ string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	m.called = true
	return m.proc, io.NopCloser(strings.NewReader(m.content)), nil, nil
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

	// hasNewWork=false: no commits were made, qgPassed=false: AC not yet satisfied either
	deps := testDeps(bs, wt, sp, mg, false, false)

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

	// Bead should be marked in_progress first (the fix for oro-h06d)
	if len(bs.updates) == 0 || bs.updates[0] != "in_progress" {
		t.Errorf("expected first bead update to be 'in_progress', got updates: %v", bs.updates)
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

func TestWorkLogSetup_SurfacesErrors(t *testing.T) {
	// When log file creation fails, executeWork should surface warnings
	// to stderr (via logStep), not silently swallow them.

	// Create a read-only directory so MkdirAll for workers/ subdir fails.
	tmpDir := t.TempDir()
	readOnlyDir := filepath.Join(tmpDir, "readonly")
	if err := os.MkdirAll(readOnlyDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(readOnlyDir, 0o444); err != nil { //nolint:gosec // intentionally read-only for test
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnlyDir, 0o750) }) //nolint:gosec // restore perms for cleanup

	// Point ORO_HOME inside the read-only dir so MkdirAll will fail.
	t.Setenv("ORO_HOME", filepath.Join(readOnlyDir, "oro"))

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

	// Capture logStep output via logOut.
	var buf strings.Builder
	origLogOut := logOut
	logOut = &buf
	defer func() { logOut = origLogOut }()

	err := executeWork(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("log file failures should not be fatal, got: %v", err)
	}

	output := buf.String()

	// Must contain a warning about the log directory creation failure.
	if !strings.Contains(output, "log dir") && !strings.Contains(output, "log file") {
		t.Errorf("expected warning about log file/dir creation failure in output, got:\n%s", output)
	}
}

func TestWorkWritesLogFile(t *testing.T) {
	// When executeWork runs, it should write Claude output and phase markers
	// to ~/.oro/workers/work-<beadID>/output.log.

	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", tmpDir)

	claudeOutput := "implementing feature X\ntest passed\n"
	sp := &contentSpawner{proc: &mockProcess{}, content: claudeOutput}
	bs := &mockBeadSource{showDetail: testBead()}
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "bead/oro-test"}
	mg := &mockMerger{result: &merge.Result{CommitSHA: "abc123"}}

	// hasNewWork returns false first (don't skip claude), true after (commits exist).
	callCount := 0
	deps := &workDeps{
		beadSrc:  bs,
		wtMgr:    wt,
		spawner:  sp,
		merger:   mg,
		repoRoot: "/tmp/test-repo",
		hasNewWork: func(_, _ string) bool {
			callCount++
			return callCount > 1
		},
		runQG: func(_ context.Context, _ string, _ bool) (bool, string, error) {
			return true, "", nil
		},
	}

	cfg := &workConfig{
		beadID:     "oro-test",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}

	// Reset logOut after test to not leak state.
	origLogOut := logOut
	defer func() { logOut = origLogOut }()

	err := executeWork(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	// Check log file exists and has content.
	logPath := filepath.Join(tmpDir, "workers", "work-oro-test", "output.log")
	data, readErr := os.ReadFile(logPath) //nolint:gosec // test-constructed path
	if readErr != nil {
		t.Fatalf("expected log file at %s, got error: %v", logPath, readErr)
	}
	logContent := string(data)

	// Must contain Claude output.
	if !strings.Contains(logContent, "implementing feature X") {
		t.Errorf("log file missing Claude output, got:\n%s", logContent)
	}

	// Must contain attempt separator.
	if !strings.Contains(logContent, "--- attempt 0 (sonnet) ---") {
		t.Errorf("log file missing attempt separator, got:\n%s", logContent)
	}

	// Must contain phase markers from logStep.
	if !strings.Contains(logContent, "Spawning claude") {
		t.Errorf("log file missing phase marker, got:\n%s", logContent)
	}
}

// captureSpawner captures the prompt passed to Spawn and returns configurable stdout.
type captureSpawner struct {
	proc           worker.Process
	capturedPrompt string
	stdout         string
}

func (m *captureSpawner) Spawn(_ context.Context, _, prompt, _ string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	m.capturedPrompt = prompt
	return m.proc, io.NopCloser(strings.NewReader(m.stdout)), nil, nil
}

// TestSpawnAndWait_MemoryWired verifies that deps.memStore is wired into both
// the prompt (via ForPrompt) and DrainOutput ([MEMORY] marker capture).
func TestSpawnAndWait_MemoryWired(t *testing.T) {
	t.Run("seeded memory appears in prompt", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)
		ctx := context.Background()

		// Seed a memory whose content contains both words from bead title "Test bead"
		// so FTS5 search will return it.
		_, err := store.Insert(ctx, memory.InsertParams{
			Content:    "Test bead approach works well for automation",
			Type:       "lesson",
			Source:     "test",
			Confidence: 0.9,
		})
		if err != nil {
			t.Fatalf("seed memory: %v", err)
		}

		sp := &captureSpawner{proc: &mockProcess{}, stdout: ""}
		deps := &workDeps{
			beadSrc:    &mockBeadSource{showDetail: testBead()},
			wtMgr:      &mockWorktreeManager{createPath: "/tmp/wt", createBranch: "bead/oro-test"},
			spawner:    sp,
			merger:     &mockMerger{},
			repoRoot:   "/tmp",
			memStore:   store,
			hasNewWork: func(_, _ string) bool { return false },
			runQG:      func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:     "oro-test",
			model:      "sonnet",
			timeout:    5 * time.Second,
			skipReview: true,
			bead:       testBead(),
		}

		if err := spawnAndWait(ctx, cfg, deps, "/tmp/wt", "sonnet", 0, "", nil); err != nil {
			t.Fatalf("spawnAndWait: %v", err)
		}

		if !strings.Contains(sp.capturedPrompt, "Test bead approach") {
			t.Errorf("prompt does not contain seeded memory; prompt snippet: %q",
				sp.capturedPrompt[:min(300, len(sp.capturedPrompt))])
		}
	})

	t.Run("[MEMORY] marker in stdout captured to store", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)

		marker := "[MEMORY] type=lesson tags=go: table tests are great"
		sp := &captureSpawner{proc: &mockProcess{}, stdout: marker}
		deps := &workDeps{
			beadSrc:    &mockBeadSource{showDetail: testBead()},
			wtMgr:      &mockWorktreeManager{createPath: "/tmp/wt", createBranch: "bead/oro-test"},
			spawner:    sp,
			merger:     &mockMerger{},
			repoRoot:   "/tmp",
			memStore:   store,
			hasNewWork: func(_, _ string) bool { return false },
			runQG:      func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:     "oro-test",
			model:      "sonnet",
			timeout:    5 * time.Second,
			skipReview: true,
			bead:       testBead(),
		}

		if err := spawnAndWait(context.Background(), cfg, deps, "/tmp/wt", "sonnet", 0, "", nil); err != nil {
			t.Fatalf("spawnAndWait: %v", err)
		}

		mems, listErr := store.List(context.Background(), memory.ListOpts{Limit: 10})
		if listErr != nil {
			t.Fatalf("store.List: %v", listErr)
		}
		if len(mems) != 1 {
			t.Errorf("expected 1 captured memory, got %d", len(mems))
		}
		if len(mems) > 0 && !strings.Contains(mems[0].Content, "table tests are great") {
			t.Errorf("captured memory content = %q, want to contain 'table tests are great'", mems[0].Content)
		}
	})

	t.Run("nil memStore is safe", func(t *testing.T) {
		sp := &captureSpawner{proc: &mockProcess{}, stdout: "[MEMORY] type=lesson: orphan marker"}
		deps := &workDeps{
			beadSrc:    &mockBeadSource{showDetail: testBead()},
			wtMgr:      &mockWorktreeManager{createPath: "/tmp/wt", createBranch: "bead/oro-test"},
			spawner:    sp,
			merger:     &mockMerger{},
			repoRoot:   "/tmp",
			memStore:   nil,
			hasNewWork: func(_, _ string) bool { return false },
			runQG:      func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:     "oro-test",
			model:      "sonnet",
			timeout:    5 * time.Second,
			skipReview: true,
			bead:       testBead(),
		}

		if err := spawnAndWait(context.Background(), cfg, deps, "/tmp/wt", "sonnet", 0, "", nil); err != nil {
			t.Errorf("spawnAndWait with nil memStore should not error: %v", err)
		}
	})
}

// TestExecuteWork_SavesVocabOnExit verifies that executeWork persists the
// embedder vocabulary (SaveVocab) when deps.memStore is non-nil with an embedder.
func TestExecuteWork_SavesVocabOnExit(t *testing.T) {
	t.Run("saves vocab to kv_store when memStore has embedder", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := openWorkerMemoryStore(db)
		ctx := context.Background()

		// Seed a memory so the embedder vocab is non-empty.
		_, err := store.Insert(ctx, memory.InsertParams{
			Content:    "save vocab test: embedder vocabulary must persist",
			Type:       "lesson",
			Source:     "test",
			Confidence: 0.9,
		})
		if err != nil {
			t.Fatalf("seed memory: %v", err)
		}

		tmpDir := t.TempDir()
		bs := &mockBeadSource{showDetail: testBead()}
		wt := &mockWorktreeManager{createPath: tmpDir + "/wt", createBranch: "bead/oro-test"}
		sp := &mockSpawner{proc: &mockProcess{}}
		mg := &mockMerger{result: &merge.Result{CommitSHA: "abc"}}
		deps := &workDeps{
			beadSrc:    bs,
			wtMgr:      wt,
			spawner:    sp,
			merger:     mg,
			repoRoot:   tmpDir,
			memStore:   store,
			hasNewWork: func(_, _ string) bool { return true }, // skip claude spawn
			runQG:      func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:     "oro-test",
			model:      "sonnet",
			timeout:    5 * time.Second,
			skipReview: true,
		}

		_ = executeWork(ctx, cfg, deps)

		// Verify vocab was saved to kv_store.
		var count int
		row := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM kv_store WHERE key = 'embedder_vocab'`)
		if err := row.Scan(&count); err != nil {
			t.Fatalf("query kv_store: %v", err)
		}
		if count == 0 {
			t.Error("expected embedder_vocab row in kv_store after executeWork, got 0 rows")
		}
	})

	t.Run("nil memStore does not panic", func(t *testing.T) {
		tmpDir := t.TempDir()
		bs := &mockBeadSource{showDetail: testBead()}
		wt := &mockWorktreeManager{createPath: tmpDir + "/wt", createBranch: "bead/oro-test"}
		sp := &mockSpawner{proc: &mockProcess{}}
		mg := &mockMerger{result: &merge.Result{CommitSHA: "abc"}}
		deps := &workDeps{
			beadSrc:    bs,
			wtMgr:      wt,
			spawner:    sp,
			merger:     mg,
			repoRoot:   tmpDir,
			memStore:   nil,
			hasNewWork: func(_, _ string) bool { return true },
			runQG:      func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:     "oro-test",
			model:      "sonnet",
			timeout:    5 * time.Second,
			skipReview: true,
		}
		// Should complete without panic.
		_ = executeWork(context.Background(), cfg, deps)
	})
}
