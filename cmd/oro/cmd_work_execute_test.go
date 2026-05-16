package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/dispatcher"
	"oro/pkg/memory"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
	"oro/pkg/worker"
)

// --- Mock implementations ---

// fakeBeadStore records calls and returns pre-configured results.
type fakeBeadStore struct {
	*beadstore.FakeStore
	showDetail *protocol.BeadDetail
	showErr    error
	shownByID  map[string]*protocol.BeadDetail // per-ID overrides; checked before showDetail
	updates    []string                        // status values passed to Update
	updateErr  error
	closeID    string
	closeErr   error
}

func (m *fakeBeadStore) ensureStore() {
	if m.FakeStore != nil {
		return
	}
	var seed []protocol.Bead
	if m.showDetail != nil {
		seed = append(seed, *m.showDetail)
	}
	for _, detail := range m.shownByID {
		if detail != nil {
			seed = append(seed, *detail)
		}
	}
	m.FakeStore = beadstore.NewFakeStore(seed...)
}

func (m *fakeBeadStore) Ready(ctx context.Context) ([]protocol.Bead, error) {
	m.ensureStore()
	return m.FakeStore.Ready(ctx)
}

func (m *fakeBeadStore) Show(ctx context.Context, id string) (*protocol.BeadDetail, error) {
	if m.showErr != nil {
		return nil, m.showErr
	}
	m.ensureStore()
	return m.FakeStore.Show(ctx, id)
}

func (m *fakeBeadStore) Close(ctx context.Context, id, reason string) error {
	m.closeID = id
	if m.closeErr != nil {
		return m.closeErr
	}
	m.ensureStore()
	return m.FakeStore.Close(ctx, id, reason)
}

func (m *fakeBeadStore) Create(ctx context.Context, params beadstore.CreateParams) (*protocol.Bead, error) {
	m.ensureStore()
	return m.FakeStore.Create(ctx, params)
}

func (m *fakeBeadStore) Update(ctx context.Context, id string, params beadstore.UpdateParams) error {
	if params.Status != nil {
		m.updates = append(m.updates, *params.Status)
	}
	if m.updateErr != nil {
		return m.updateErr
	}
	m.ensureStore()
	return m.FakeStore.Update(ctx, id, params)
}

func (m *fakeBeadStore) AllChildrenClosed(_ context.Context, _ string) (bool, error) {
	return true, nil
}

func (m *fakeBeadStore) HasChildren(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (m *fakeBeadStore) FindByParentAndTag(_ context.Context, _ string, _ string) ([]protocol.Bead, error) {
	return []protocol.Bead{}, nil
}

func (m *fakeBeadStore) InProgress(ctx context.Context) ([]protocol.Bead, error) {
	m.ensureStore()
	return m.FakeStore.InProgress(ctx)
}

func (m *fakeBeadStore) Blocked(ctx context.Context) ([]protocol.Bead, error) {
	m.ensureStore()
	return m.FakeStore.Blocked(ctx)
}

func (m *fakeBeadStore) Closed(ctx context.Context, limit int) ([]protocol.Bead, error) {
	m.ensureStore()
	return m.FakeStore.Closed(ctx, limit)
}
func (m *fakeBeadStore) Sync(_ context.Context) error { return nil }
func (m *fakeBeadStore) Export(ctx context.Context) ([]byte, error) {
	m.ensureStore()
	return m.FakeStore.Export(ctx)
}

func (m *fakeBeadStore) Defer(ctx context.Context, id, until string) error {
	m.ensureStore()
	return m.FakeStore.Defer(ctx, id, until)
}

func (m *fakeBeadStore) Undefer(ctx context.Context, id string) error {
	m.ensureStore()
	return m.FakeStore.Undefer(ctx, id)
}

func (m *fakeBeadStore) AppendJourney(ctx context.Context, beadID string, evt beadstore.JourneyEvent) error {
	m.ensureStore()
	return m.FakeStore.AppendJourney(ctx, beadID, evt)
}

func (m *fakeBeadStore) Journey(ctx context.Context, beadID string, since time.Time) ([]beadstore.JourneyEvent, error) {
	m.ensureStore()
	return m.FakeStore.Journey(ctx, beadID, since)
}

func (m *fakeBeadStore) LatestJourney(ctx context.Context, beadID string, limit int) ([]beadstore.JourneyEvent, error) {
	m.ensureStore()
	return m.FakeStore.LatestJourney(ctx, beadID, limit)
}

func (m *fakeBeadStore) TransitionPipelineStage(ctx context.Context, beadID string, from, to beadstore.PipelineStage) error {
	m.ensureStore()
	return m.FakeStore.TransitionPipelineStage(ctx, beadID, from, to)
}

// mockWorktreeManager records Create/Remove calls.
type mockWorktreeManager struct {
	createPath         string
	createBranch       string
	createErr          error
	capturedBaseBranch string
	currentBranch      string
	removed            []string
	removeErr          error
	deletedBranches    []string
	deleteBranchErr    error
	preparedBranch     string
	preparedBaseBranch string
	prepareFastForward bool
	prepareErr         error
	reuseWorktree      string
	reuseBranch        string
	reuseBaseBranch    string
	reuseFastForward   bool
	reuseErr           error
}

func (m *mockWorktreeManager) Create(_ context.Context, _, baseBranch string) (string, string, error) {
	m.capturedBaseBranch = baseBranch
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
func (m *mockWorktreeManager) DeleteBranch(_ context.Context, branch string) error {
	m.deletedBranches = append(m.deletedBranches, branch)
	return m.deleteBranchErr
}

func (m *mockWorktreeManager) ForceDeleteBranch(_ context.Context, branch string) error {
	m.deletedBranches = append(m.deletedBranches, branch)
	return m.deleteBranchErr
}

func (m *mockWorktreeManager) BranchExists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (m *mockWorktreeManager) MergeFFOnly(_ context.Context, _ string, _ string) (string, error) {
	return "", nil
}

func (m *mockWorktreeManager) UpdateBranchRef(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockWorktreeManager) GCClosedWorktrees(_ context.Context, _ func(string) bool) error {
	return nil
}

func (m *mockWorktreeManager) Exists(_ context.Context, _ string) bool {
	return true // default: paths are valid
}

func (m *mockWorktreeManager) CurrentBranch(_ context.Context, _ string) (string, error) {
	if m.currentBranch != "" {
		return m.currentBranch, nil
	}
	return m.createBranch, nil
}

func (m *mockWorktreeManager) PrepareBaseBranchForAssignment(_ context.Context, branch, baseBranch string) (bool, error) {
	m.preparedBranch = branch
	m.preparedBaseBranch = baseBranch
	return m.prepareFastForward, m.prepareErr
}

func (m *mockWorktreeManager) PrepareExistingForReuse(_ context.Context, worktree, branch, baseBranch string) (bool, error) {
	m.reuseWorktree = worktree
	m.reuseBranch = branch
	m.reuseBaseBranch = baseBranch
	return m.reuseFastForward, m.reuseErr
}

func (m *mockWorktreeManager) RebaseOnto(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockWorktreeManager) PushBranch(_ context.Context, _ string) error {
	return nil
}

func (m *mockWorktreeManager) CreateBranch(_ context.Context, _, _ string) error {
	return nil
}

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

func (m *mockSpawner) StreamFormat() worker.StreamFormat { return worker.StreamFormatClaudeJSON }

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

func testDeps(bs *fakeBeadStore, wt *mockWorktreeManager, sp *mockSpawner, mg *mockMerger, hasWork bool, qgPassed bool) *workDeps {
	return &workDeps{
		beadSrc:  bs,
		wtMgr:    wt,
		spawner:  sp,
		merger:   mg,
		repoRoot: "/tmp/test-repo",
		hasNewWork: func(_, _, _ string) bool {
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

func (m *contentSpawner) StreamFormat() worker.StreamFormat { return worker.StreamFormatClaudeJSON }

// --- Tests ---

func TestExecuteWork_NoCommits_BailsOut(t *testing.T) {
	// When claude exits cleanly but produces no commits, executeWork should:
	// 1. NOT proceed to quality gate or merge
	// 2. Reset bead status to "open"
	// 3. Clean up worktree
	// 4. Return an error

	bs := &fakeBeadStore{showDetail: testBead()}
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

	bs := &fakeBeadStore{showDetail: testBead()}
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

func TestExecuteWorkQGExhaustionUsesClassifiedPolicy(t *testing.T) {
	bs := &fakeBeadStore{showDetail: testBead()}
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{}
	deps := testDeps(bs, wt, sp, mg, true, false)

	var records []dispatcher.QGFailureRecord
	var classes []dispatcher.QGFailureClassification
	deps.runQG = func(_ context.Context, _ string, _ bool) (bool, string, error) {
		return false, "FAIL: go test ./cmd/oro failed", nil
	}
	deps.recordQGFailure = func(_ context.Context, rec dispatcher.QGFailureRecord, cls dispatcher.QGFailureClassification) error {
		records = append(records, rec)
		classes = append(classes, cls)
		return nil
	}

	err := executeWork(context.Background(), &workConfig{
		beadID:     "oro-test",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}, deps)
	if err == nil {
		t.Fatal("expected QG exhaustion error")
	}
	if len(records) != 1 {
		t.Fatalf("recorded QG failures = %d, want 1", len(records))
	}
	if records[0].Component != "oro-work-implementation" {
		t.Fatalf("component = %q, want oro-work-implementation", records[0].Component)
	}
	if records[0].ID == "" || !strings.Contains(records[0].ID, "oro-work-implementation") {
		t.Fatalf("record ID = %q, want unique component-scoped ID", records[0].ID)
	}
	if classes[0].Class != dispatcher.QGFailureClassWorkerDeterministic ||
		classes[0].Decision != dispatcher.QGFailureDecisionReopenOriginal {
		t.Fatalf("classification = class %q decision %q, want worker_deterministic/reopen_original",
			classes[0].Class, classes[0].Decision)
	}
}

func TestExecuteWorkQGErrorUsesClassifiedPolicy(t *testing.T) {
	bs := &fakeBeadStore{showDetail: testBead()}
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{}
	deps := testDeps(bs, wt, sp, mg, true, true)

	var record dispatcher.QGFailureRecord
	var class dispatcher.QGFailureClassification
	deps.runQG = func(_ context.Context, _ string, _ bool) (bool, string, error) {
		return false, "", errors.New("FAIL: go test ./cmd/oro failed")
	}
	deps.recordQGFailure = func(_ context.Context, rec dispatcher.QGFailureRecord, cls dispatcher.QGFailureClassification) error {
		record = rec
		class = cls
		return nil
	}

	err := executeWork(context.Background(), &workConfig{
		beadID:     "oro-test",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "quality gate error") {
		t.Fatalf("executeWork error = %v, want quality gate error", err)
	}
	if record.Component != "oro-work-implementation" {
		t.Fatalf("component = %q, want oro-work-implementation", record.Component)
	}
	if !strings.Contains(record.Output, "go test") {
		t.Fatalf("record output = %q, want qgErr text", record.Output)
	}
	if class.Class != dispatcher.QGFailureClassWorkerDeterministic ||
		class.Decision != dispatcher.QGFailureDecisionReopenOriginal {
		t.Fatalf("classification = class %q decision %q, want worker_deterministic/reopen_original",
			class.Class, class.Decision)
	}
}

func TestExecuteWorkPreMergeQGFailureUsesClassifiedPolicy(t *testing.T) {
	bs := &fakeBeadStore{showDetail: testBead()}
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{result: &merge.Result{CommitSHA: "abc123"}}
	deps := testDeps(bs, wt, sp, mg, true, true)

	qgCalls := 0
	var records []dispatcher.QGFailureRecord
	var classes []dispatcher.QGFailureClassification
	deps.runQG = func(_ context.Context, _ string, _ bool) (bool, string, error) {
		qgCalls++
		if qgCalls == 1 {
			return true, "implementation passed", nil
		}
		return false, "FAIL: go test ./cmd/oro failed", nil
	}
	deps.recordQGFailure = func(_ context.Context, rec dispatcher.QGFailureRecord, cls dispatcher.QGFailureClassification) error {
		records = append(records, rec)
		classes = append(classes, cls)
		return nil
	}

	err := executeWork(context.Background(), &workConfig{
		beadID:     "oro-test",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}, deps)
	if err == nil {
		t.Fatal("expected pre-merge QG failure")
	}
	if len(records) != 1 {
		t.Fatalf("recorded QG failures = %d, want 1", len(records))
	}
	if records[0].Component != "oro-work-pre-merge" {
		t.Fatalf("component = %q, want oro-work-pre-merge", records[0].Component)
	}
	if records[0].ID == "" || !strings.Contains(records[0].ID, "oro-work-pre-merge") {
		t.Fatalf("record ID = %q, want unique component-scoped ID", records[0].ID)
	}
	if classes[0].Class != dispatcher.QGFailureClassWorkerDeterministic ||
		classes[0].Decision != dispatcher.QGFailureDecisionReopenOriginal {
		t.Fatalf("classification = class %q decision %q, want worker_deterministic/reopen_original",
			classes[0].Class, classes[0].Decision)
	}
	if mg.called {
		t.Fatal("merge must not run after pre-merge QG failure")
	}
}

func TestExecuteWorkPreMergeQGErrorUsesClassifiedPolicy(t *testing.T) {
	bs := &fakeBeadStore{showDetail: testBead()}
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{result: &merge.Result{CommitSHA: "abc123"}}
	deps := testDeps(bs, wt, sp, mg, true, true)

	qgCalls := 0
	var record dispatcher.QGFailureRecord
	var class dispatcher.QGFailureClassification
	deps.runQG = func(_ context.Context, _ string, _ bool) (bool, string, error) {
		qgCalls++
		if qgCalls == 1 {
			return true, "implementation passed", nil
		}
		return false, "", errors.New("FAIL: go test ./cmd/oro failed")
	}
	deps.recordQGFailure = func(_ context.Context, rec dispatcher.QGFailureRecord, cls dispatcher.QGFailureClassification) error {
		record = rec
		class = cls
		return nil
	}

	err := executeWork(context.Background(), &workConfig{
		beadID:     "oro-test",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "pre-merge quality gate error") {
		t.Fatalf("executeWork error = %v, want pre-merge quality gate error", err)
	}
	if record.Component != "oro-work-pre-merge" {
		t.Fatalf("component = %q, want oro-work-pre-merge", record.Component)
	}
	if !strings.Contains(record.Output, "go test") {
		t.Fatalf("record output = %q, want qgErr text", record.Output)
	}
	if class.Class != dispatcher.QGFailureClassWorkerDeterministic ||
		class.Decision != dispatcher.QGFailureDecisionReopenOriginal {
		t.Fatalf("classification = class %q decision %q, want worker_deterministic/reopen_original",
			class.Class, class.Decision)
	}
	if mg.called {
		t.Fatal("merge must not run after pre-merge QG error")
	}
}

func TestHandleReviewRejectionQGFailureUsesClassifiedPolicy(t *testing.T) {
	bs := &fakeBeadStore{showDetail: testBead()}
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{}
	deps := testDeps(bs, wt, sp, mg, true, true)
	deps.runQG = func(_ context.Context, _ string, _ bool) (bool, string, error) {
		return false, "FAIL: go test ./cmd/oro failed", nil
	}

	var record dispatcher.QGFailureRecord
	var class dispatcher.QGFailureClassification
	deps.recordQGFailure = func(_ context.Context, rec dispatcher.QGFailureRecord, cls dispatcher.QGFailureClassification) error {
		record = rec
		class = cls
		return nil
	}

	model := protocol.ModelSonnet
	attempt := 0
	feedback := ""
	rejects, err := handleReviewRejection(context.Background(), &workConfig{
		beadID:  "oro-test",
		bead:    testBead(),
		model:   protocol.ModelSonnet,
		timeout: 5 * time.Second,
	}, deps, t.TempDir(), ops.Result{
		Verdict:  ops.VerdictRejected,
		Feedback: "fix review issue",
	}, 0, &model, &attempt, &feedback, nil)
	if err == nil {
		t.Fatal("expected QG failure after review rejection")
	}
	if rejects != 1 {
		t.Fatalf("rejects = %d, want 1", rejects)
	}
	if record.Component != "oro-work-implementation" {
		t.Fatalf("component = %q, want oro-work-implementation", record.Component)
	}
	if record.ID == "" || !strings.Contains(record.ID, "oro-work-implementation") {
		t.Fatalf("record ID = %q, want unique component-scoped ID", record.ID)
	}
	if class.Class != dispatcher.QGFailureClassWorkerDeterministic ||
		class.Decision != dispatcher.QGFailureDecisionReopenOriginal {
		t.Fatalf("classification = class %q decision %q, want worker_deterministic/reopen_original",
			class.Class, class.Decision)
	}
}

func TestHandleReviewRejectionQGErrorUsesClassifiedPolicy(t *testing.T) {
	bs := &fakeBeadStore{showDetail: testBead()}
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{}
	deps := testDeps(bs, wt, sp, mg, true, true)
	deps.runQG = func(_ context.Context, _ string, _ bool) (bool, string, error) {
		return false, "", errors.New("FAIL: go test ./cmd/oro failed")
	}

	var record dispatcher.QGFailureRecord
	var class dispatcher.QGFailureClassification
	deps.recordQGFailure = func(_ context.Context, rec dispatcher.QGFailureRecord, cls dispatcher.QGFailureClassification) error {
		record = rec
		class = cls
		return nil
	}

	model := protocol.ModelSonnet
	attempt := 0
	feedback := ""
	rejects, err := handleReviewRejection(context.Background(), &workConfig{
		beadID:  "oro-test",
		bead:    testBead(),
		model:   protocol.ModelSonnet,
		timeout: 5 * time.Second,
	}, deps, t.TempDir(), ops.Result{
		Verdict:  ops.VerdictRejected,
		Feedback: "fix review issue",
	}, 0, &model, &attempt, &feedback, nil)
	if err == nil || !strings.Contains(err.Error(), "quality gate error") {
		t.Fatalf("handleReviewRejection error = %v, want quality gate error", err)
	}
	if rejects != 1 {
		t.Fatalf("rejects = %d, want 1", rejects)
	}
	if record.Component != "oro-work-implementation" {
		t.Fatalf("component = %q, want oro-work-implementation", record.Component)
	}
	if !strings.Contains(record.Output, "go test") {
		t.Fatalf("record output = %q, want qgErr text", record.Output)
	}
	if class.Class != dispatcher.QGFailureClassWorkerDeterministic ||
		class.Decision != dispatcher.QGFailureDecisionReopenOriginal {
		t.Fatalf("classification = class %q decision %q, want worker_deterministic/reopen_original",
			class.Class, class.Decision)
	}
}

func TestExecuteWork_MergeFail_ResetsBead(t *testing.T) {
	// When merge fails, bead should be reset to "open".

	bs := &fakeBeadStore{showDetail: testBead()}
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

	bs := &fakeBeadStore{showDetail: testBead()}
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{result: &merge.Result{CommitSHA: "abc123"}}

	deps := testDeps(bs, wt, sp, mg, true, true)
	var qgSkipMutations []bool
	deps.runQG = func(_ context.Context, _ string, skipMutation bool) (bool, string, error) {
		qgSkipMutations = append(qgSkipMutations, skipMutation)
		return true, "qg output", nil
	}

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
	if len(qgSkipMutations) != 2 {
		t.Fatalf("expected implementation and pre-merge QG calls, got %d", len(qgSkipMutations))
	}
	for i, skipMutation := range qgSkipMutations {
		if skipMutation {
			t.Fatalf("QG call %d used ORO_SKIP_MUTATION; local quality_gate.sh should defer mutation by context without disabling other tiers", i)
		}
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

	bs := &fakeBeadStore{showDetail: testBead()}
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

	claudeOutput := sjNDJSON(
		sjToolUse("Read"),
		sjTextDelta("implementing feature X\n"),
		sjToolUse("Bash"),
		sjTextDelta("test passed\n"),
	)
	sp := &contentSpawner{proc: &mockProcess{}, content: claudeOutput}
	bs := &fakeBeadStore{showDetail: testBead()}
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
		hasNewWork: func(_, _, _ string) bool {
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

	// Must contain formatted tool-call activity from stream-json parsing.
	if !strings.Contains(logContent, "-> Read") {
		t.Errorf("log file missing tool activity, got:\n%s", logContent)
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
// stdout must be NDJSON (stream-json format). Use sjTextDelta/sjToolUse/sjResult helpers.
type captureSpawner struct {
	proc           worker.Process
	capturedPrompt string
	stdout         string
}

func (m *captureSpawner) Spawn(_ context.Context, _, prompt, _ string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	m.capturedPrompt = prompt
	return m.proc, io.NopCloser(strings.NewReader(m.stdout)), nil, nil
}

func (m *captureSpawner) StreamFormat() worker.StreamFormat { return worker.StreamFormatClaudeJSON }

// --- stream-json NDJSON helpers for cmd/oro tests ---

// sjTextDelta wraps text in a stream-json assistant text event.
func sjTextDelta(text string) string {
	escaped, _ := json.Marshal(text) // JSON-encodes the string with proper escaping
	return `{"type":"assistant","message":{"content":[{"type":"text","text":` + string(escaped) + `}]}}`
}

// sjToolUse wraps a tool name in a stream-json assistant tool_use event.
func sjToolUse(name string) string {
	escaped, _ := json.Marshal(name)
	return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":` + string(escaped) + `}]}}`
}

// sjNDJSON joins stream-json lines with newlines into NDJSON input.
func sjNDJSON(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// TestSpawnAndWait_MemoryWired verifies that deps.memStore is wired into
// DrainOutput for [MEMORY] marker capture. Per the D.4 read cutover (§13.2),
// memory content is no longer rendered in the prompt — the `## Cards` section
// replaces the old `## Memory` and `## Previous Feedback` sections. The first
// subtest pins this contract: seeded memory text must NOT appear in the prompt.
func TestSpawnAndWait_MemoryWired(t *testing.T) {
	t.Run("seeded memory not rendered in prompt after D.4 cutover", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)
		ctx := context.Background()

		// Seed a memory whose content would have appeared in the old `## Memory`
		// section. After the D.4 cutover it must not leak into the prompt.
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
			beadSrc:    &fakeBeadStore{showDetail: testBead()},
			wtMgr:      &mockWorktreeManager{createPath: "/tmp/wt", createBranch: "bead/oro-test"},
			spawner:    sp,
			merger:     &mockMerger{},
			repoRoot:   "/tmp",
			memStore:   store,
			hasNewWork: func(_, _, _ string) bool { return false },
			runQG:      func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:     "oro-test",
			model:      "sonnet",
			timeout:    5 * time.Second,
			skipReview: true,
			bead:       testBead(),
		}

		if err := spawnAndWait(ctx, cfg, deps, "/tmp/wt", "claude", "sonnet", "", 0, "", nil); err != nil {
			t.Fatalf("spawnAndWait: %v", err)
		}

		if strings.Contains(sp.capturedPrompt, "Test bead approach") {
			t.Errorf("seeded memory content leaked into prompt after D.4 cutover; prompt snippet: %q",
				sp.capturedPrompt[:min(300, len(sp.capturedPrompt))])
		}
		if !strings.Contains(sp.capturedPrompt, "## Cards") {
			t.Errorf("prompt missing Cards section; prompt snippet: %q",
				sp.capturedPrompt[:min(300, len(sp.capturedPrompt))])
		}
		if strings.Contains(sp.capturedPrompt, "## Memory") || strings.Contains(sp.capturedPrompt, "## Previous Feedback") {
			t.Errorf("legacy Memory/Previous Feedback section still present after D.4 cutover")
		}
	})

	t.Run("[MEMORY] marker in stdout captured to store", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)

		marker := sjNDJSON(sjTextDelta("[MEMORY] type=lesson tags=go: table tests are great\n"))
		sp := &captureSpawner{proc: &mockProcess{}, stdout: marker}
		deps := &workDeps{
			beadSrc:    &fakeBeadStore{showDetail: testBead()},
			wtMgr:      &mockWorktreeManager{createPath: "/tmp/wt", createBranch: "bead/oro-test"},
			spawner:    sp,
			merger:     &mockMerger{},
			repoRoot:   "/tmp",
			memStore:   store,
			hasNewWork: func(_, _, _ string) bool { return false },
			runQG:      func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:     "oro-test",
			model:      "sonnet",
			timeout:    5 * time.Second,
			skipReview: true,
			bead:       testBead(),
		}

		if err := spawnAndWait(context.Background(), cfg, deps, "/tmp/wt", "claude", "sonnet", "", 0, "", nil); err != nil {
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
		sp := &captureSpawner{proc: &mockProcess{}, stdout: sjNDJSON(sjTextDelta("[MEMORY] type=lesson: orphan marker\n"))}
		deps := &workDeps{
			beadSrc:    &fakeBeadStore{showDetail: testBead()},
			wtMgr:      &mockWorktreeManager{createPath: "/tmp/wt", createBranch: "bead/oro-test"},
			spawner:    sp,
			merger:     &mockMerger{},
			repoRoot:   "/tmp",
			memStore:   nil,
			hasNewWork: func(_, _, _ string) bool { return false },
			runQG:      func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:     "oro-test",
			model:      "sonnet",
			timeout:    5 * time.Second,
			skipReview: true,
			bead:       testBead(),
		}

		if err := spawnAndWait(context.Background(), cfg, deps, "/tmp/wt", "claude", "sonnet", "", 0, "", nil); err != nil {
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
		bs := &fakeBeadStore{showDetail: testBead()}
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
			hasNewWork: func(_, _, _ string) bool { return true }, // skip claude spawn
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
		bs := &fakeBeadStore{showDetail: testBead()}
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
			hasNewWork: func(_, _, _ string) bool { return true },
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

// modelCapturingSpawner captures the model argument passed to Spawn.
type modelCapturingSpawner struct {
	proc          worker.Process
	capturedModel string
	stdout        string
}

func (m *modelCapturingSpawner) Spawn(_ context.Context, model, _, _ string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	m.capturedModel = model
	return m.proc, io.NopCloser(strings.NewReader(m.stdout)), nil, nil
}

func (m *modelCapturingSpawner) StreamFormat() worker.StreamFormat {
	return worker.StreamFormatClaudeJSON
}

func TestExecuteWork_DryRunShowsResolvedModel(t *testing.T) {
	// Regression: dry-run must show the resolved model (bead metadata or default),
	// not the raw (empty) cfg.model flag value.

	t.Run("dry-run shows bead metadata model", func(t *testing.T) {
		bead := testBead()
		bead.Model = "opus"

		bs := &fakeBeadStore{showDetail: bead}
		deps := &workDeps{
			beadSrc:  bs,
			wtMgr:    &mockWorktreeManager{},
			spawner:  &mockSpawner{proc: &mockProcess{}},
			merger:   &mockMerger{},
			repoRoot: "/tmp",
		}
		cfg := &workConfig{
			beadID:  "oro-test",
			model:   "", // no --model flag
			timeout: 5 * time.Second,
			dryRun:  true,
		}

		var buf strings.Builder
		origLogOut := logOut
		logOut = &buf
		defer func() { logOut = origLogOut }()

		err := executeWork(context.Background(), cfg, deps)
		if err != nil {
			t.Fatalf("dry-run should not error: %v", err)
		}

		output := buf.String()
		if !strings.Contains(output, "model=opus") {
			t.Errorf("dry-run output should show resolved model=opus, got: %s", output)
		}
	})

	t.Run("dry-run shows default model when bead has none", func(t *testing.T) {
		bead := testBead()
		bead.Model = ""

		bs := &fakeBeadStore{showDetail: bead}
		deps := &workDeps{
			beadSrc:  bs,
			wtMgr:    &mockWorktreeManager{},
			spawner:  &mockSpawner{proc: &mockProcess{}},
			merger:   &mockMerger{},
			repoRoot: "/tmp",
		}
		cfg := &workConfig{
			beadID:  "oro-test",
			model:   "", // no --model flag
			timeout: 5 * time.Second,
			dryRun:  true,
		}

		var buf strings.Builder
		origLogOut := logOut
		logOut = &buf
		defer func() { logOut = origLogOut }()

		err := executeWork(context.Background(), cfg, deps)
		if err != nil {
			t.Fatalf("dry-run should not error: %v", err)
		}

		output := buf.String()
		if !strings.Contains(output, "model=sonnet") {
			t.Errorf("dry-run output should show resolved model=sonnet (default), got: %s", output)
		}
	})
}

func TestExecuteWorkIgnoresPremortemGate(t *testing.T) {
	ctx := context.Background()
	child := testBead()
	child.ID = "oro-child-gated"
	child.Epic = "epic-gated"
	parent := &protocol.BeadDetail{ID: "epic-gated", Type: "epic", Title: "gated epic", Status: "open"}
	bs := &fakeBeadStore{
		showDetail: child,
		shownByID:  map[string]*protocol.BeadDetail{"epic-gated": parent},
	}
	deps := &workDeps{
		beadSrc:  bs,
		wtMgr:    &mockWorktreeManager{},
		spawner:  &mockSpawner{proc: &mockProcess{}},
		merger:   &mockMerger{},
		repoRoot: "/tmp",
	}
	cfg := &workConfig{
		beadID:  child.ID,
		timeout: 5 * time.Second,
		dryRun:  true,
	}

	err := executeWork(ctx, cfg, deps)
	if err != nil {
		t.Fatalf("executeWork dry-run with parent epic = %v, want nil", err)
	}
}

func TestExecuteWork_HonorsBeadMetadataModel(t *testing.T) {
	// Test that executeWork honors bead metadata model in standalone path.
	// Priority: explicit --model flag > bead.Model > default

	t.Run("uses bead Model when no explicit --model flag", func(t *testing.T) {
		bead := testBead()
		bead.Model = "opus" // Bead specifies opus

		sp := &modelCapturingSpawner{proc: &mockProcess{}, stdout: ""}
		bs := &fakeBeadStore{showDetail: bead}
		wt := &mockWorktreeManager{createPath: "/tmp/wt", createBranch: "bead/oro-test"}
		mg := &mockMerger{result: &merge.Result{CommitSHA: "abc"}}

		callCount := 0
		deps := &workDeps{
			beadSrc:  bs,
			wtMgr:    wt,
			spawner:  sp,
			merger:   mg,
			repoRoot: "/tmp",
			hasNewWork: func(_, _, _ string) bool {
				// First call (before claude) returns false to trigger spawn
				// Second call (after claude) returns true to skip second spawn
				callCount++
				return callCount > 1
			},
			runQG: func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}

		cfg := &workConfig{
			beadID:     "oro-test",
			model:      "", // User didn't pass --model, so it's empty
			timeout:    5 * time.Second,
			skipReview: true,
		}

		err := executeWork(context.Background(), cfg, deps)
		if err != nil {
			t.Fatalf("executeWork failed: %v", err)
		}

		// Should spawn with opus (from bead Model), NOT sonnet (the default)
		if sp.capturedModel != "opus" {
			t.Errorf("expected spawner to be called with opus, got %q", sp.capturedModel)
		}
	})

	t.Run("explicit --model flag takes priority over bead Model", func(t *testing.T) {
		bead := testBead()
		bead.Model = "opus" // Bead specifies opus

		sp := &modelCapturingSpawner{proc: &mockProcess{}, stdout: ""}
		bs := &fakeBeadStore{showDetail: bead}
		wt := &mockWorktreeManager{createPath: "/tmp/wt", createBranch: "bead/oro-test"}
		mg := &mockMerger{result: &merge.Result{CommitSHA: "abc"}}

		callCount := 0
		deps := &workDeps{
			beadSrc:  bs,
			wtMgr:    wt,
			spawner:  sp,
			merger:   mg,
			repoRoot: "/tmp",
			hasNewWork: func(_, _, _ string) bool {
				// First call (before claude) returns false to trigger spawn
				// Second call (after claude) returns true to skip second spawn
				callCount++
				return callCount > 1
			},
			runQG: func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}

		cfg := &workConfig{
			beadID:     "oro-test",
			model:      "haiku", // User explicitly passed --model=haiku
			timeout:    5 * time.Second,
			skipReview: true,
		}

		err := executeWork(context.Background(), cfg, deps)
		if err != nil {
			t.Fatalf("executeWork failed: %v", err)
		}

		// Should spawn with haiku (explicit flag), NOT opus (bead model)
		if sp.capturedModel != "haiku" {
			t.Errorf("expected spawner to be called with haiku, got %q", sp.capturedModel)
		}
	})

	t.Run("defaults to sonnet when bead Model is empty and no --model flag", func(t *testing.T) {
		bead := testBead()
		bead.Model = "" // No model specified in bead

		sp := &modelCapturingSpawner{proc: &mockProcess{}, stdout: ""}
		bs := &fakeBeadStore{showDetail: bead}
		wt := &mockWorktreeManager{createPath: "/tmp/wt", createBranch: "bead/oro-test"}
		mg := &mockMerger{result: &merge.Result{CommitSHA: "abc"}}

		callCount := 0
		deps := &workDeps{
			beadSrc:  bs,
			wtMgr:    wt,
			spawner:  sp,
			merger:   mg,
			repoRoot: "/tmp",
			hasNewWork: func(_, _, _ string) bool {
				callCount++
				return callCount > 1
			},
			runQG: func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}

		cfg := &workConfig{
			beadID:     "oro-test",
			model:      "", // User didn't pass --model
			timeout:    5 * time.Second,
			skipReview: true,
		}

		err := executeWork(context.Background(), cfg, deps)
		if err != nil {
			t.Fatalf("executeWork failed: %v", err)
		}

		// Should spawn with sonnet (default)
		if sp.capturedModel != "sonnet" {
			t.Errorf("expected spawner to be called with sonnet, got %q", sp.capturedModel)
		}
	})
}

func TestExecuteWork_DeletesBranchAfterMerge(t *testing.T) {
	// On successful merge, DeleteBranch should be called with agent/<beadID>.

	bs := &fakeBeadStore{showDetail: testBead()}
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "agent/oro-test"}
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

	// DeleteBranch should have been called with agent/oro-test
	expectedBranch := "agent/oro-test"
	if len(wt.deletedBranches) == 0 {
		t.Error("expected DeleteBranch to be called after merge")
	} else if wt.deletedBranches[0] != expectedBranch {
		t.Errorf("expected DeleteBranch(%q), got DeleteBranch(%q)", expectedBranch, wt.deletedBranches[0])
	}
}

// sequentialOpsReviewer returns pre-configured Review results in order.
// After all configured results are consumed, it returns VerdictFailed.
type sequentialOpsReviewer struct {
	results []ops.Result
	idx     int
}

func (r *sequentialOpsReviewer) Review(_ context.Context, _ ops.ReviewOpts) <-chan ops.Result {
	ch := make(chan ops.Result, 1)
	if r.idx < len(r.results) {
		ch <- r.results[r.idx]
		r.idx++
	} else {
		ch <- ops.Result{Verdict: ops.VerdictFailed, Feedback: "no more results"}
	}
	return ch
}

// TestNewProductionDepsWiresQGFailureRecorder verifies that newProductionDeps
// wires a state DB backed QG failure recorder so standalone oro work can
// persist incidents/occurrences when the state DB is available.
func TestNewProductionDepsWiresQGFailureRecorder(t *testing.T) {
	t.Run("wires non-nil recorder backed by state DB", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("ORO_HOME", tmpDir)
		t.Setenv("ORO_PROJECT", "")
		t.Chdir(tmpDir)

		deps, err := newProductionDeps(0)
		if err != nil {
			t.Fatalf("newProductionDeps: %v", err)
		}
		if deps.recordQGFailure == nil {
			t.Fatal("expected non-nil recordQGFailure when state DB is available")
		}

		rec := dispatcher.QGFailureRecord{
			BeadID:    "oro-test",
			Component: "oro-work",
			Output:    "FAIL: go test ./... failed",
		}
		cls := dispatcher.QGFailureClassification{
			Class:      dispatcher.QGFailureClassWorkerDeterministic,
			Decision:   dispatcher.QGFailureDecisionRetryOriginal,
			Confidence: dispatcher.QGFailureConfidenceHigh,
			Reason:     "deterministic test failure",
		}
		if err := deps.recordQGFailure(context.Background(), rec, cls); err != nil {
			t.Fatalf("production recordQGFailure returned error: %v", err)
		}
	})

	t.Run("degraded recorder logs and does not error when no DB", func(t *testing.T) {
		var buf strings.Builder
		oldLogOut := logOut
		logOut = &buf
		defer func() { logOut = oldLogOut }()

		recorder := newDegradedQGFailureRecorder()
		rec := dispatcher.QGFailureRecord{BeadID: "oro-test", Component: "oro-work", Output: "FAIL"}
		cls := dispatcher.QGFailureClassification{
			Class:      dispatcher.QGFailureClassWorkerDeterministic,
			Decision:   dispatcher.QGFailureDecisionRetryOriginal,
			Confidence: dispatcher.QGFailureConfidenceHigh,
			Reason:     "no db",
		}
		if err := recorder(context.Background(), rec, cls); err != nil {
			t.Fatalf("degraded recorder must not error: %v", err)
		}
		if !strings.Contains(buf.String(), "qg") {
			t.Errorf("degraded recorder should log a qg event, got: %q", buf.String())
		}
	})
}
