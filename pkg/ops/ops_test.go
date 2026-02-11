package ops //nolint:testpackage // internal test needs access to unexported types

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// --- Mock infrastructure ---

// mockProcess simulates a claude -p subprocess.
type mockProcess struct {
	mu       sync.Mutex
	stdout   string
	waitErr  error
	killed   bool
	waitDone chan struct{} // closed when Wait should return
}

func newMockProcess(stdout string, waitErr error) *mockProcess {
	return &mockProcess{
		stdout:   stdout,
		waitErr:  waitErr,
		waitDone: make(chan struct{}),
	}
}

func newReadyMockProcess(stdout string, waitErr error) *mockProcess {
	p := newMockProcess(stdout, waitErr)
	close(p.waitDone) // immediately ready
	return p
}

func (m *mockProcess) Wait() error {
	<-m.waitDone
	return m.waitErr
}

func (m *mockProcess) Kill() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killed = true
	// Unblock Wait if still waiting.
	select {
	case <-m.waitDone:
	default:
		close(m.waitDone)
	}
	return nil
}

func (m *mockProcess) Output() (string, error) {
	return m.stdout, nil
}

func (m *mockProcess) wasKilled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.killed
}

// mockBatchSpawner records spawn calls and returns preconfigured processes.
type mockBatchSpawner struct {
	mu      sync.Mutex
	calls   []spawnCall
	process Process
	err     error
}

type spawnCall struct {
	model   string
	prompt  string
	workdir string
}

func (m *mockBatchSpawner) Spawn(_ context.Context, model, prompt, workdir string) (Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, spawnCall{model: model, prompt: prompt, workdir: workdir})
	return m.process, m.err
}

func (m *mockBatchSpawner) getCalls() []spawnCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]spawnCall, len(m.calls))
	copy(out, m.calls)
	return out
}

// --- Tests ---

func TestReviewApproved(t *testing.T) {
	proc := newReadyMockProcess("Looking at the code...\n\nAPPROVED\n\nAll criteria met.", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.Review(context.Background(), ReviewOpts{
		BeadID:             "oro-abc",
		Worktree:           "/tmp/wt",
		AcceptanceCriteria: "Must have tests",
	})

	result := waitResult(t, ch)
	if result.Verdict != VerdictApproved {
		t.Fatalf("expected VerdictApproved, got %q", result.Verdict)
	}
	if result.Type != OpsReview {
		t.Fatalf("expected OpsReview, got %q", result.Type)
	}
	if result.BeadID != "oro-abc" {
		t.Fatalf("expected bead ID oro-abc, got %q", result.BeadID)
	}
	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
}

func TestReviewRejected(t *testing.T) {
	proc := newReadyMockProcess("Reviewing changes...\n\nREJECTED: missing error handling in parse function", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.Review(context.Background(), ReviewOpts{
		BeadID:             "oro-def",
		Worktree:           "/tmp/wt",
		AcceptanceCriteria: "Must handle errors",
	})

	result := waitResult(t, ch)
	if result.Verdict != VerdictRejected {
		t.Fatalf("expected VerdictRejected, got %q", result.Verdict)
	}
	if result.Feedback == "" {
		t.Fatal("expected non-empty feedback for rejection")
	}
	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
}

func TestReviewUsesCorrectModel(t *testing.T) {
	proc := newReadyMockProcess("APPROVED", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.Review(context.Background(), ReviewOpts{
		BeadID:   "oro-m1",
		Worktree: "/tmp/wt",
	})
	waitResult(t, ch)

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(calls))
	}
	if calls[0].model != OpsReview.Model() {
		t.Fatalf("expected model %q, got %q", OpsReview.Model(), calls[0].model)
	}
}

func TestMergeResolved(t *testing.T) {
	proc := newReadyMockProcess("Fixed conflicts in main.go\n\nRESOLVED\n\nMerge completed successfully.", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.ResolveMergeConflict(context.Background(), MergeOpts{
		BeadID:           "oro-mrg",
		Worktree:         "/tmp/wt",
		ConflictFiles:    []string{"main.go", "util.go"},
		OurBeadContext:   "Adding new feature X",
		TheirBeadContext: "Refactoring module Y",
	})

	result := waitResult(t, ch)
	if result.Verdict != VerdictResolved {
		t.Fatalf("expected VerdictResolved, got %q", result.Verdict)
	}
	if result.Type != OpsMerge {
		t.Fatalf("expected OpsMerge, got %q", result.Type)
	}
}

func TestMergeFailed(t *testing.T) {
	proc := newReadyMockProcess("Cannot resolve conflicts automatically.\n\nFAILED\n\nSemantic conflict between features.", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.ResolveMergeConflict(context.Background(), MergeOpts{
		BeadID:   "oro-mrg2",
		Worktree: "/tmp/wt",
	})

	result := waitResult(t, ch)
	if result.Verdict != VerdictFailed {
		t.Fatalf("expected VerdictFailed, got %q", result.Verdict)
	}
}

func TestMergeUsesCorrectModel(t *testing.T) {
	proc := newReadyMockProcess("RESOLVED", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.ResolveMergeConflict(context.Background(), MergeOpts{
		BeadID:   "oro-m2",
		Worktree: "/tmp/wt",
	})
	waitResult(t, ch)

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(calls))
	}
	if calls[0].model != OpsMerge.Model() {
		t.Fatalf("expected model %q, got %q", OpsMerge.Model(), calls[0].model)
	}
}

func TestDiagnosisCapturesFeedback(t *testing.T) {
	diagText := "Worker stuck because test suite has infinite loop in TestFoo. " +
		"The loop at line 42 never terminates when input is empty."
	proc := newReadyMockProcess(diagText, nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.Diagnose(context.Background(), DiagOpts{
		BeadID:   "oro-diag",
		Worktree: "/tmp/wt",
		Symptom:  "worker stuck after 2 ralph cycles",
	})

	result := waitResult(t, ch)
	if result.Type != OpsDiagnosis {
		t.Fatalf("expected OpsDiagnosis, got %q", result.Type)
	}
	if result.Feedback == "" {
		t.Fatal("expected non-empty diagnosis feedback")
	}
	if result.Feedback != diagText {
		t.Fatalf("expected feedback %q, got %q", diagText, result.Feedback)
	}
}

func TestDiagnosisUsesCorrectModel(t *testing.T) {
	proc := newReadyMockProcess("diagnosis here", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.Diagnose(context.Background(), DiagOpts{
		BeadID:   "oro-m3",
		Worktree: "/tmp/wt",
		Symptom:  "stuck",
	})
	waitResult(t, ch)

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(calls))
	}
	if calls[0].model != OpsDiagnosis.Model() {
		t.Fatalf("expected model %q, got %q", OpsDiagnosis.Model(), calls[0].model)
	}
}

func TestModelRouting(t *testing.T) {
	tests := []struct {
		opsType Type
		want    string
	}{
		{OpsReview, "claude-sonnet-4-5-20250929"},
		{OpsMerge, "claude-opus-4-6"},
		{OpsDiagnosis, "claude-opus-4-6"},
		{Type("unknown"), "claude-sonnet-4-5-20250929"},
	}
	for _, tt := range tests {
		t.Run(string(tt.opsType), func(t *testing.T) {
			got := tt.opsType.Model()
			if got != tt.want {
				t.Fatalf("Model() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCancelKillsActiveAgent(t *testing.T) {
	proc := newMockProcess("", nil) // Will block on Wait until killed
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	_ = s.Review(context.Background(), ReviewOpts{
		BeadID:   "oro-cancel",
		Worktree: "/tmp/wt",
	})

	// Wait for agent to be registered.
	waitActive(t, s, 1)

	active := s.Active()
	if len(active) != 1 {
		t.Fatalf("expected 1 active agent, got %d", len(active))
	}

	err := s.Cancel(active[0])
	if err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}

	if !proc.wasKilled() {
		t.Fatal("expected process to be killed")
	}

	// Wait for agent to be cleaned up.
	waitActive(t, s, 0)
}

func TestCancelUnknownTask(t *testing.T) {
	s := NewSpawner(&mockBatchSpawner{})

	err := s.Cancel("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown task ID")
	}
}

func TestContextCancellationPropagates(t *testing.T) {
	proc := newMockProcess("", nil) // blocks on Wait
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ctx, cancel := context.WithCancel(context.Background())

	ch := s.Review(ctx, ReviewOpts{
		BeadID:   "oro-ctx",
		Worktree: "/tmp/wt",
	})

	// Wait for agent to be registered.
	waitActive(t, s, 1)

	cancel()

	result := waitResult(t, ch)
	if result.Err == nil {
		t.Fatal("expected error from context cancellation")
	}

	if !proc.wasKilled() {
		t.Fatal("expected process to be killed on context cancellation")
	}
}

func TestSpawnError(t *testing.T) {
	mock := &mockBatchSpawner{
		process: nil,
		err:     errors.New("spawn failed"),
	}
	s := NewSpawner(mock)

	ch := s.Review(context.Background(), ReviewOpts{
		BeadID:   "oro-err",
		Worktree: "/tmp/wt",
	})

	result := waitResult(t, ch)
	if result.Err == nil {
		t.Fatal("expected error from spawn failure")
	}
	if result.Verdict != VerdictFailed {
		t.Fatalf("expected VerdictFailed on spawn error, got %q", result.Verdict)
	}
}

func TestReviewPromptContainsCriteria(t *testing.T) {
	proc := newReadyMockProcess("APPROVED", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.Review(context.Background(), ReviewOpts{
		BeadID:             "oro-p1",
		Worktree:           "/tmp/wt",
		AcceptanceCriteria: "All functions must have docstrings",
	})
	waitResult(t, ch)

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(calls))
	}
	prompt := calls[0].prompt
	if !containsSubstring(prompt, "All functions must have docstrings") {
		t.Fatalf("prompt does not contain acceptance criteria: %s", prompt)
	}
	if calls[0].workdir != "/tmp/wt" {
		t.Fatalf("expected workdir /tmp/wt, got %q", calls[0].workdir)
	}
}

func TestMergePromptContainsConflictFiles(t *testing.T) {
	proc := newReadyMockProcess("RESOLVED", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.ResolveMergeConflict(context.Background(), MergeOpts{
		BeadID:        "oro-p2",
		Worktree:      "/tmp/wt",
		ConflictFiles: []string{"main.go", "util.go"},
	})
	waitResult(t, ch)

	calls := mock.getCalls()
	prompt := calls[0].prompt
	if !containsSubstring(prompt, "main.go") || !containsSubstring(prompt, "util.go") {
		t.Fatalf("prompt does not contain conflict files: %s", prompt)
	}
}

func TestDiagnosisPromptContainsSymptom(t *testing.T) {
	proc := newReadyMockProcess("diagnosis", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.Diagnose(context.Background(), DiagOpts{
		BeadID:   "oro-p3",
		Worktree: "/tmp/wt",
		Symptom:  "test timeout after 30s",
	})
	waitResult(t, ch)

	calls := mock.getCalls()
	prompt := calls[0].prompt
	if !containsSubstring(prompt, "test timeout after 30s") {
		t.Fatalf("prompt does not contain symptom: %s", prompt)
	}
}

func TestActiveTracking(t *testing.T) {
	proc := newMockProcess("", nil) // blocks on Wait
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	if len(s.Active()) != 0 {
		t.Fatal("expected no active agents initially")
	}

	_ = s.Review(context.Background(), ReviewOpts{BeadID: "oro-a1", Worktree: "/tmp/wt"})
	_ = s.Diagnose(context.Background(), DiagOpts{BeadID: "oro-a2", Worktree: "/tmp/wt", Symptom: "stuck"})

	waitActive(t, s, 2)

	active := s.Active()
	if len(active) != 2 {
		t.Fatalf("expected 2 active agents, got %d", len(active))
	}
}

// --- Helpers ---

func waitResult(t *testing.T, ch <-chan Result) Result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for result")
		return Result{}
	}
}

func waitActive(t *testing.T, s *Spawner, count int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if len(s.Active()) == count {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d active agents, have %d", count, len(s.Active()))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && findSubstring(s, sub)
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
