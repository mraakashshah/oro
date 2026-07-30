package ops //nolint:testpackage // internal test needs access to unexported types

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// --- Mock infrastructure ---

// mockProcess simulates a claude -p subprocess.
type mockProcess struct {
	mu           sync.Mutex
	stdout       string
	waitErr      error
	killed       bool
	lastOutputAt time.Time
	waitDone     chan struct{} // closed when Wait should return
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

func (m *mockProcess) LastOutputAt() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastOutputAt
}

func (m *mockProcess) wasKilled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.killed
}

func (m *mockProcess) setLastOutputAt(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastOutputAt = t
}

type reapGatedProcess struct {
	*mockProcess
	reap chan struct{}
}

func newReapGatedProcess() *reapGatedProcess {
	return &reapGatedProcess{
		mockProcess: newMockProcess("", nil),
		reap:        make(chan struct{}),
	}
}

func (p *reapGatedProcess) Wait() error {
	<-p.reap
	return p.waitErr
}

// mockBatchSpawner records spawn calls and returns preconfigured processes.
type mockBatchSpawner struct {
	mu      sync.Mutex
	calls   []spawnCall
	process Process
	err     error
}

type spawnCall struct {
	runtime   string
	model     string
	reasoning string
	prompt    string
	workdir   string
}

func TestWaitForProcessPublishesFailureAfterReaping(t *testing.T) {
	tests := []struct {
		name  string
		start func(*Spawner, Process, chan<- Result)
	}{
		{
			name: "timeout",
			start: func(s *Spawner, proc Process, results chan<- Result) {
				s.timeout = 10 * time.Millisecond
				go s.waitForProcess(context.Background(), proc, OpsDecompose, "oro-reap", results)
			},
		},
		{
			name: "cancellation",
			start: func(s *Spawner, proc Process, results chan<- Result) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				go s.waitForProcess(ctx, proc, OpsDecompose, "oro-reap", results)
			},
		},
		{
			name: "idle wedge",
			start: func(s *Spawner, proc Process, results chan<- Result) {
				s.reviewIdle = 10 * time.Millisecond
				go s.waitForProcess(context.Background(), proc, OpsReview, "oro-reap", results)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proc := newReapGatedProcess()
			results := make(chan Result, 1)
			s := NewSpawner(nil)
			tt.start(s, proc, results)

			waitForKilled(t, proc)
			select {
			case result := <-results:
				t.Fatalf("published verdict before Wait completed: %+v", result)
			case <-time.After(25 * time.Millisecond):
			}

			close(proc.reap)
			result := waitResult(t, results)
			if result.Verdict != VerdictFailed {
				t.Fatalf("Verdict = %q, want %q", result.Verdict, VerdictFailed)
			}
		})
	}
}

func waitForKilled(t *testing.T, proc *reapGatedProcess) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if proc.wasKilled() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("process was not killed before timeout")
}

func (m *mockBatchSpawner) Spawn(_ context.Context, model, prompt, workdir string) (Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, spawnCall{model: model, prompt: prompt, workdir: workdir})
	return m.process, m.err
}

func (m *mockBatchSpawner) SpawnRuntime(_ context.Context, runtime, model, reasoning, prompt, workdir string) (Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, spawnCall{runtime: runtime, model: model, reasoning: reasoning, prompt: prompt, workdir: workdir})
	return m.process, m.err
}

func (m *mockBatchSpawner) getCalls() []spawnCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]spawnCall, len(m.calls))
	copy(out, m.calls)
	return out
}

func TestOpsRoutingUsesLockedRoleModels(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, ".oro"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".oro", "config.yaml"), []byte("agent: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		run       func(*Spawner) <-chan Result
		runtime   string
		model     string
		reasoning string
	}{
		{"ops_escalation", func(s *Spawner) <-chan Result {
			return s.Escalate(context.Background(), EscalationOpts{BeadID: "b", Workdir: dir})
		}, "codex", "gpt-5.6-sol", "low"},
		{"ops_merge", func(s *Spawner) <-chan Result {
			return s.ResolveMergeConflict(context.Background(), MergeOpts{BeadID: "b", Worktree: dir})
		}, "codex", "gpt-5.6-sol", "low"},
		{"ops_diagnosis", func(s *Spawner) <-chan Result {
			return s.Diagnose(context.Background(), DiagOpts{BeadID: "b", Worktree: dir})
		}, "codex", "gpt-5.6-sol", "low"},
		{"ops_review", func(s *Spawner) <-chan Result {
			return s.Review(context.Background(), ReviewOpts{BeadID: "b", Worktree: ""})
		}, "claude", "claude-opus-4-8", "high"},
		{"ops_decompose", func(s *Spawner) <-chan Result {
			return s.Decompose(context.Background(), DecomposeOpts{BeadID: "b"})
		}, "codex", "gpt-5.6-sol", "low"},
		{"ops_epic_fix", func(s *Spawner) <-chan Result {
			return s.DiagnoseEpicFailure(context.Background(), EpicFixOpts{EpicID: "e"})
		}, "codex", "gpt-5.6-sol", "low"},
		{"ops_write_ac", func(s *Spawner) <-chan Result {
			return s.WriteAC(context.Background(), WriteACOpts{BeadID: "b", Workdir: dir})
		}, "codex", "gpt-5.6-sol", "low"},
		{"ops_dream", func(s *Spawner) <-chan Result {
			return s.Dream(context.Background(), DreamOpts{})
		}, "codex", "gpt-5.6-luna", "low"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockBatchSpawner{process: newReadyMockProcess("VERDICT: APPROVED", nil)}
			s := NewSpawner(mock)
			<-tc.run(s)
			calls := mock.getCalls()
			if len(calls) != 1 {
				t.Fatalf("calls = %d, want 1", len(calls))
			}
			got := calls[0]
			if got.runtime != tc.runtime || got.model != tc.model || got.reasoning != tc.reasoning {
				t.Fatalf("spawn = (%q, %q, %q), want (%q, %q, %q)", got.runtime, got.model, got.reasoning, tc.runtime, tc.model, tc.reasoning)
			}
		})
	}
}

func TestSpawner_Grade(t *testing.T) {
	proposal := Card{
		ID:       "card-grade",
		Title:    "Verify current retry state",
		BodyFull: "Run the exact acceptance command before changing a retry task.",
	}
	evidence := GradeEvidence{} // Dream proposals may not have an originating bead.
	mock := &mockBatchSpawner{process: newReadyMockProcess(`{"verdict":"correct","confidence":0.96,"reasoning":"evidence supports the proposal"}`, nil)}

	result := waitResult(t, NewSpawner(mock).Grade(context.Background(), GradeOpts{
		Card:      proposal,
		Evidence:  evidence,
		Role:      "grade",
		Model:     "gpt-5.6-terra",
		Reasoning: "low",
	}))
	if result.Err != nil {
		t.Fatalf("Grade() error = %v", result.Err)
	}
	if result.GradeVerdict != GradeVerdictCorrect || result.GradeConfidence != 0.96 || result.GradeReasoning != "evidence supports the proposal" {
		t.Fatalf("Grade() result = %+v", result)
	}

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("spawn calls = %d, want 1", len(calls))
	}
	if calls[0].runtime != "codex" || calls[0].model != "gpt-5.6-terra" || calls[0].reasoning != "low" {
		t.Fatalf("spawn routing = (%q, %q, %q), want (codex, gpt-5.6-terra, low)", calls[0].runtime, calls[0].model, calls[0].reasoning)
	}
	if got, want := calls[0].prompt, buildGradePrompt(proposal, evidence); got != want {
		t.Fatalf("grade prompt = %q, want %q", got, want)
	}

	malformed := waitResult(t, NewSpawner(&mockBatchSpawner{
		process: newReadyMockProcess(`{"verdict":"not-a-grade"}`, nil),
	}).Grade(context.Background(), GradeOpts{Card: proposal, Evidence: evidence}))
	if malformed.Err == nil || malformed.Verdict != VerdictFailed || malformed.GradeVerdict != "" {
		t.Fatalf("malformed grade result = %+v, want failed error without a grade verdict", malformed)
	}
}

func TestDecomposeOpsUsesRepoRootWorkdir(t *testing.T) {
	repoRoot := t.TempDir()
	mock := &mockBatchSpawner{process: newReadyMockProcess("VERDICT: resolved", nil)}
	s := NewSpawner(mock)

	<-s.Decompose(context.Background(), DecomposeOpts{BeadID: "oro-big7", Workdir: repoRoot})

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("spawn calls = %d, want 1", len(calls))
	}
	if calls[0].workdir != repoRoot {
		t.Fatalf("decompose workdir = %q, want %q", calls[0].workdir, repoRoot)
	}
}

func TestRuntimeSpawnerRoutesDecomposeThroughConfiguredRuntime(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, ".oro"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".oro", "config.yaml"), []byte(`agent:
  roles:
    ops_decompose:
      transport: cli
      runtime: codex
      model: gpt-5.5
      reasoning: high
`), 0o600); err != nil {
		t.Fatal(err)
	}

	mock := &mockBatchSpawner{process: newReadyMockProcess("VERDICT: resolved", nil)}
	s := NewSpawner(mock)
	<-s.Decompose(context.Background(), DecomposeOpts{BeadID: "oro-big8", Workdir: dir})

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("spawn calls = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.runtime != "codex" || got.model != "gpt-5.5" || got.reasoning != "high" {
		t.Fatalf("decompose runtime spawn = (%q, %q, %q), want (codex, gpt-5.5, high)", got.runtime, got.model, got.reasoning)
	}
	if got.workdir != dir {
		t.Fatalf("decompose workdir = %q, want %q", got.workdir, dir)
	}
}

func TestDecomposeSpawnFailureIncludesRuntimeContext(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, ".oro"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".oro", "config.yaml"), []byte(`agent:
  roles:
    ops_decompose:
      transport: cli
      runtime: codex
      model: gpt-5.5
      reasoning: high
`), 0o600); err != nil {
		t.Fatal(err)
	}

	mock := &mockBatchSpawner{err: errors.New("pre-start spawn error")}
	s := NewSpawner(mock)

	result := <-s.Decompose(context.Background(), DecomposeOpts{BeadID: "oro-big9", Workdir: dir})

	if result.Type != OpsDecompose {
		t.Fatalf("result Type = %q, want %q", result.Type, OpsDecompose)
	}
	if result.BeadID != "oro-big9" {
		t.Fatalf("result BeadID = %q, want oro-big9", result.BeadID)
	}
	if result.Verdict != VerdictFailed {
		t.Fatalf("result Verdict = %q, want %q", result.Verdict, VerdictFailed)
	}
	if result.Err == nil {
		t.Fatal("result Err = nil, want spawn failure")
	}
	errText := result.Err.Error()
	for _, want := range []string{
		"ops: spawn failed",
		`runtime "codex"`,
		`model "gpt-5.5"`,
		`reasoning "high"`,
		"pre-start spawn error",
	} {
		if !strings.Contains(errText, want) {
			t.Fatalf("result Err = %q, want substring %q", errText, want)
		}
	}
	if active := s.Active(); len(active) != 0 {
		t.Fatalf("active ops agents = %v, want none", active)
	}
}

func TestDecomposeRuntimeCanMutateOroStateDB(t *testing.T) {
	repoRoot := t.TempDir()
	oroHome := filepath.Join(t.TempDir(), "home")
	stateDB := filepath.Join(t.TempDir(), "project-state.db")
	binDir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "oro-calls.log")

	if err := os.MkdirAll(oroHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "oro"), []byte("#!/bin/sh\necho repo-local ./oro must not be used >&2\nexit 42\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".oro"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".oro", "config.yaml"), []byte(`agent:
  roles:
    ops_decompose:
      transport: cli
      runtime: codex
      model: gpt-5.5
      reasoning: high
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoRoot)
	fakeOro := `#!/bin/sh
{
  printf 'argv0=%s\n' "$0"
  printf 'pwd=%s\n' "$PWD"
  printf 'ORO_HOME=%s\n' "$ORO_HOME"
  printf 'ORO_DB_PATH=%s\n' "$ORO_DB_PATH"
  printf 'args=%s\n' "$*"
} >> "$ORO_CAPTURE"
case "$1 $2" in
  "task create"|"task update") exit 0 ;;
  *) exit 7 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "oro"), []byte(fakeOro), 0o755); err != nil {
		t.Fatal(err)
	}
	agent := `#!/bin/sh
set -eu
oro task create --title="child" --type=task --parent=oro-parent --acceptance="Test: pkg/x_test.go:TestX | Cmd: go test ./pkg/x -run TestX -count=1 | Assert: child works" --estimate=5
oro task update oro-parent --type=epic
printf 'VERDICT: resolved\n'
`
	agentPath := filepath.Join(binDir, "decompose-agent")
	if err := os.WriteFile(agentPath, []byte(agent), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_DB_PATH", stateDB)
	t.Setenv("ORO_CAPTURE", capturePath)

	spawner := NewRuntimeSpawnerRouter(nil, NewExecSpawner(RuntimeSpec{
		Command: agentPath,
		BuildArgsWithReasoning: func(_, _, _ string) []string {
			return nil
		},
	}))
	s := NewSpawner(spawner)

	result := waitResult(t, s.Decompose(context.Background(), DecomposeOpts{
		BeadID:  "oro-parent",
		Workdir: repoRoot,
	}))
	if result.Err != nil {
		t.Fatalf("decompose result error: %v; feedback=%s", result.Err, result.Feedback)
	}
	if result.Verdict != VerdictResolved {
		t.Fatalf("decompose verdict = %q, want %q; feedback=%s", result.Verdict, VerdictResolved, result.Feedback)
	}

	capturedBytes, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	captured := string(capturedBytes)
	if strings.Contains(captured, "argv0="+filepath.Join(repoRoot, "oro")) {
		t.Fatalf("decompose used repo-local ./oro; captured:\n%s", captured)
	}
	for _, want := range []string{
		"argv0=" + filepath.Join(binDir, "oro"),
		"pwd=" + repoRoot,
		"ORO_HOME=" + oroHome,
		"ORO_DB_PATH=" + stateDB,
		"args=task create",
		"args=task update",
	} {
		if !strings.Contains(captured, want) {
			t.Fatalf("capture missing %q:\n%s", want, captured)
		}
	}
}

func TestDecomposeSandboxDenialIsShortActionableError(t *testing.T) {
	fullPrompt := buildDecomposePrompt(DecomposeOpts{BeadID: "oro-denied", QGOutput: strings.Repeat("full prompt transcript ", 40)})
	stdout := fullPrompt + "\nerror: Landlock sandbox blocked write to /tmp/oro/state.db\nattempt to write a readonly database\n"

	result := parseResult(OpsDecompose, "oro-denied", stdout, errors.New("exit status 1"))

	if result.Verdict != VerdictFailed {
		t.Fatalf("verdict = %q, want failed", result.Verdict)
	}
	if result.Err == nil {
		t.Fatal("expected sanitized sandbox error")
	}
	if !strings.Contains(result.Err.Error(), "sandbox blocked Oro state DB write") {
		t.Fatalf("error = %q, want actionable sandbox message", result.Err.Error())
	}
	if strings.Contains(result.Feedback, "full prompt transcript") {
		t.Fatalf("feedback stored full prompt transcript: %q", result.Feedback)
	}
	if len(result.Feedback) > 240 {
		t.Fatalf("feedback too long: %d bytes: %q", len(result.Feedback), result.Feedback)
	}
}

func TestClaudeArgsWithReasoningAppendsEffort(t *testing.T) {
	// Non-empty reasoning appends --effort so Claude runs at the configured level.
	got := buildClaudeReviewArgsWithReasoning("claude-opus-4-8", "xhigh", "review prompt")
	want := []string{"-p", "review prompt", "--model", "claude-opus-4-8", "--verbose", "--output-format", "stream-json", "--effort", "xhigh"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("buildClaudeReviewArgsWithReasoning = %q, want %q", got, want)
	}

	gotOps := buildClaudeOpsArgsWithReasoning("claude-opus-4-8", "high", "plain prompt")
	wantOps := []string{"-p", "plain prompt", "--model", "claude-opus-4-8", "--effort", "high"}
	if strings.Join(gotOps, "\x00") != strings.Join(wantOps, "\x00") {
		t.Fatalf("buildClaudeOpsArgsWithReasoning = %q, want %q", gotOps, wantOps)
	}

	// Empty reasoning must not append --effort (falls back to plain args).
	gotEmpty := buildClaudeReviewArgsWithReasoning("claude-opus-4-8", "", "review prompt")
	wantEmpty := buildClaudeReviewArgs("claude-opus-4-8", "review prompt")
	if strings.Join(gotEmpty, "\x00") != strings.Join(wantEmpty, "\x00") {
		t.Fatalf("buildClaudeReviewArgsWithReasoning empty reasoning = %q, want %q", gotEmpty, wantEmpty)
	}

	// Both Claude spawners must wire the reasoning-aware builder so
	// SpawnWithReasoning passes --effort instead of dropping it.
	if NewClaudeReviewOpsSpawner().spec.BuildArgsWithReasoning == nil {
		t.Fatal("NewClaudeReviewOpsSpawner must set BuildArgsWithReasoning")
	}
	if NewClaudeOpsSpawner().spec.BuildArgsWithReasoning == nil {
		t.Fatal("NewClaudeOpsSpawner must set BuildArgsWithReasoning")
	}
}

func TestRunSelectsReviewSpawner(t *testing.T) {
	reviewArgs := buildClaudeReviewArgs("claude-opus", "review prompt")
	wantReviewArgs := []string{"-p", "review prompt", "--model", "claude-opus", "--verbose", "--output-format", "stream-json"}
	if strings.Join(reviewArgs, "\x00") != strings.Join(wantReviewArgs, "\x00") {
		t.Fatalf("buildClaudeReviewArgs = %q, want %q", reviewArgs, wantReviewArgs)
	}
	plainArgs := buildClaudeOpsArgs("claude-opus", "plain prompt")
	if strings.Contains(strings.Join(plainArgs, " "), "stream-json") {
		t.Fatalf("buildClaudeOpsArgs = %q, want plain non-streaming args", plainArgs)
	}

	defaultSpawner := &mockBatchSpawner{process: newReadyMockProcess("VERDICT: resolved", nil)}
	reviewSpawner := &mockBatchSpawner{process: newReadyMockProcess("VERDICT: APPROVED", nil)}
	s := NewSpawner(defaultSpawner)
	s.reviewSpawner = reviewSpawner

	<-s.run(context.Background(), OpsReview, "oro-review", "/tmp/review", "review prompt")
	<-s.run(context.Background(), OpsMerge, "oro-merge", "/tmp/merge", "merge prompt")
	<-s.run(context.Background(), OpsDecompose, "oro-decompose", "/tmp/decompose", "decompose prompt")

	reviewCalls := reviewSpawner.getCalls()
	if len(reviewCalls) != 1 {
		t.Fatalf("review spawner calls = %d, want 1", len(reviewCalls))
	}
	if reviewCalls[0].prompt != "review prompt" {
		t.Fatalf("review spawner prompt = %q, want review prompt", reviewCalls[0].prompt)
	}

	defaultCalls := defaultSpawner.getCalls()
	if len(defaultCalls) != 2 {
		t.Fatalf("default spawner calls = %d, want 2", len(defaultCalls))
	}
	if defaultCalls[0].prompt != "merge prompt" {
		t.Fatalf("default call[0] prompt = %q, want merge prompt", defaultCalls[0].prompt)
	}
	if defaultCalls[1].prompt != "decompose prompt" {
		t.Fatalf("default call[1] prompt = %q, want decompose prompt", defaultCalls[1].prompt)
	}
}

func TestRunWith_DefaultRoutingMatchesRun(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, ".oro"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".oro", "config.yaml"), []byte(`agent:
  roles:
    ops_review:
      transport: cli
      runtime: codex
      model: gpt-5.5
      reasoning: high
`), 0o600); err != nil {
		t.Fatal(err)
	}

	runMock := &mockBatchSpawner{process: newReadyMockProcess("VERDICT: APPROVED\n\nsame feedback", nil)}
	runSpawner := NewSpawner(runMock)
	runResult := <-runSpawner.run(context.Background(), OpsReview, "oro-review", dir, "same prompt")

	runWithMock := &mockBatchSpawner{process: newReadyMockProcess("VERDICT: APPROVED\n\nsame feedback", nil)}
	runWithSpawner := NewSpawner(runWithMock)
	runWithResult := <-runWithSpawner.runWith(
		context.Background(),
		OpsReview,
		spawnRouting{role: OpsReview.Role()},
		"oro-review",
		dir,
		"same prompt",
	)

	if runResult.Type != runWithResult.Type ||
		runResult.BeadID != runWithResult.BeadID ||
		runResult.Verdict != runWithResult.Verdict ||
		runResult.Feedback != runWithResult.Feedback {
		t.Fatalf("runWith result = %#v, want run result %#v", runWithResult, runResult)
	}

	runCalls := runMock.getCalls()
	runWithCalls := runWithMock.getCalls()
	if len(runCalls) != 1 || len(runWithCalls) != 1 {
		t.Fatalf("spawn calls = run:%d runWith:%d, want 1 each", len(runCalls), len(runWithCalls))
	}
	if runCalls[0] != runWithCalls[0] {
		t.Fatalf("runWith spawn = %#v, want run spawn %#v", runWithCalls[0], runCalls[0])
	}
}

// multiProcessSpawner returns different processes on each Spawn call.
type multiProcessSpawner struct {
	mu        sync.Mutex
	processes []*mockProcess
	index     int
}

func (m *multiProcessSpawner) Spawn(_ context.Context, model, prompt, workdir string) (Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.index >= len(m.processes) {
		return nil, errors.New("no more processes configured")
	}
	proc := m.processes[m.index]
	m.index++
	return proc, nil
}

// --- Tests ---

func TestReviewApproved(t *testing.T) {
	proc := newReadyMockProcess("Looking at the code...\n\nAll criteria met.\n\nVERDICT: APPROVED", nil)
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
	proc := newReadyMockProcess("Reviewing changes...\n\nMissing error handling in parse function.\n\nVERDICT: REJECTED", nil)
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
	proc := newReadyMockProcess("VERDICT: APPROVED", nil)
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
	if calls[0].model != "claude-opus-4-8" || calls[0].reasoning != "high" {
		t.Fatalf("expected Opus high, got model=%q reasoning=%q", calls[0].model, calls[0].reasoning)
	}
}

func TestReviewDocsOnlyDiffShortCircuits(t *testing.T) {
	worktree := initReviewTestRepo(t)
	if err := os.MkdirAll(filepath.Join(worktree, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "docs", "guide.md"), []byte("# Guide\n"), 0o644); err != nil {
		t.Fatalf("write docs change: %v", err)
	}

	mock := &mockBatchSpawner{process: newReadyMockProcess("VERDICT: REJECTED", nil)}
	s := NewSpawner(mock)

	ch := s.Review(context.Background(), ReviewOpts{
		BeadID:     "oro-docs",
		Worktree:   worktree,
		BaseBranch: "main",
	})
	result := waitResult(t, ch)

	if result.Verdict != VerdictApproved {
		t.Fatalf("docs-only review verdict = %q, want approved", result.Verdict)
	}
	if result.Feedback == "" {
		t.Fatal("expected feedback explaining docs-only short-circuit")
	}
	if calls := mock.getCalls(); len(calls) != 0 {
		t.Fatalf("docs-only review should not spawn ops process, got %d calls", len(calls))
	}
}

func TestIsDocsOnlyDiff_NormalizesGitEnvToWorktree(t *testing.T) {
	mainRepo := initReviewTestRepo(t)
	assignedRepo := initReviewTestRepo(t)

	if err := os.WriteFile(filepath.Join(mainRepo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write poisoned main code change: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(assignedRepo, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assignedRepo, "docs", "guide.md"), []byte("# Guide\n"), 0o644); err != nil {
		t.Fatalf("write assigned docs change: %v", err)
	}

	t.Setenv("PWD", mainRepo)
	t.Setenv("GIT_DIR", filepath.Join(mainRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", mainRepo)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(mainRepo, ".git", "index"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(mainRepo, ".git"))

	docsOnly, err := isDocsOnlyDiff(context.Background(), assignedRepo, "main")
	if err != nil {
		t.Fatalf("isDocsOnlyDiff: %v", err)
	}
	if !docsOnly {
		t.Fatalf("expected assigned worktree docs-only diff despite poisoned main git env")
	}
}

func TestReviewCodeDiffStillSpawns(t *testing.T) {
	worktree := initReviewTestRepo(t)
	if err := os.WriteFile(filepath.Join(worktree, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write code change: %v", err)
	}

	mock := &mockBatchSpawner{process: newReadyMockProcess("VERDICT: APPROVED", nil)}
	s := NewSpawner(mock)

	ch := s.Review(context.Background(), ReviewOpts{
		BeadID:     "oro-code",
		Worktree:   worktree,
		BaseBranch: "main",
	})
	result := waitResult(t, ch)

	if result.Verdict != VerdictApproved {
		t.Fatalf("code review verdict = %q, want approved", result.Verdict)
	}
	if calls := mock.getCalls(); len(calls) != 1 {
		t.Fatalf("code review should spawn ops process, got %d calls", len(calls))
	}
}

func initReviewTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // test helper with fixed args
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, string(out))
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
	if calls[0].model != "gpt-5.6-sol" || calls[0].reasoning != "low" {
		t.Fatalf("expected Sol low, got model=%q reasoning=%q", calls[0].model, calls[0].reasoning)
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
	if calls[0].model != "gpt-5.6-sol" || calls[0].reasoning != "low" {
		t.Fatalf("expected Sol low, got model=%q reasoning=%q", calls[0].model, calls[0].reasoning)
	}
}

func TestModelRouting(t *testing.T) {
	tests := []struct {
		opsType Type
		want    string
	}{
		{OpsReview, "opus"},
		{OpsMerge, "opus"},
		{OpsDiagnosis, "opus"},
		{Type("unknown"), "sonnet"},
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

func TestOpsTasksRouteByTier(t *testing.T) {
	tests := []struct {
		opsType    Type
		wantTier   protocol.Tier
		wantLegacy string
	}{
		{OpsReview, protocol.TierDeep, protocol.ModelOpus},
		{OpsMerge, protocol.TierDeep, protocol.ModelOpus},
		{OpsDiagnosis, protocol.TierDeep, protocol.ModelOpus},
		{OpsEscalation, protocol.TierBalanced, protocol.ModelSonnet},
		{OpsDream, protocol.TierBackground, protocol.ModelHaiku},
		{Type("unknown"), protocol.DefaultTier, protocol.DefaultModel},
	}

	for _, tt := range tests {
		t.Run(string(tt.opsType), func(t *testing.T) {
			if got := tt.opsType.Tier(); got != tt.wantTier {
				t.Fatalf("Tier() = %q, want %q", got, tt.wantTier)
			}
			if got := tt.opsType.Model(); got != tt.wantLegacy {
				t.Fatalf("Model() = %q, want %q", got, tt.wantLegacy)
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

func TestSpawnerEscalateRuntimeConfigFailureReturnsTypedResult(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, ".oro"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".oro", "config.yaml"), []byte("agent: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mock := &mockBatchSpawner{
		process: nil,
		err:     errors.New("codex profile missing API key"),
	}
	s := NewSpawner(mock)

	ch := s.Escalate(context.Background(), EscalationOpts{
		BeadID:  "oro-runtime-config",
		Workdir: dir,
	})

	result := waitResult(t, ch)
	if result.Type != OpsEscalation {
		t.Fatalf("expected Type %q, got %q", OpsEscalation, result.Type)
	}
	if result.BeadID != "oro-runtime-config" {
		t.Fatalf("expected BeadID %q, got %q", "oro-runtime-config", result.BeadID)
	}
	if result.Verdict != VerdictFailed {
		t.Fatalf("expected VerdictFailed on runtime config error, got %q", result.Verdict)
	}
	if result.Err == nil {
		t.Fatal("expected runtime config error")
	}
	errText := result.Err.Error()
	for _, want := range []string{
		`ops: spawn failed for runtime "codex"`,
		"spawn codex ops runtime",
		"codex profile missing API key",
	} {
		if !strings.Contains(errText, want) {
			t.Fatalf("expected error %q to contain %q", errText, want)
		}
	}

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(calls))
	}
	if calls[0].runtime != "codex" {
		t.Fatalf("expected codex runtime spawn, got %q", calls[0].runtime)
	}
	if active := s.Active(); len(active) != 0 {
		t.Fatalf("expected no active ops agents after spawn failure, got %v", active)
	}
}

func TestReviewPromptContainsCriteria(t *testing.T) {
	proc := newReadyMockProcess("VERDICT: APPROVED", nil)
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

func TestCancelForBeadKillsMatchingAgents(t *testing.T) {
	// Create separate blocking processes.
	procs := []*mockProcess{
		newMockProcess("", nil), // oro-target review
		newMockProcess("", nil), // oro-target diagnosis
		newMockProcess("", nil), // oro-other review
	}

	// Create a multi-process spawner that returns different processes on each call.
	spawner := &multiProcessSpawner{processes: procs}
	s := NewSpawner(spawner)

	// Spawn agents for the same bead (oro-target) with different ops types.
	_ = s.Review(context.Background(), ReviewOpts{BeadID: "oro-target", Worktree: "/tmp/wt1"})
	_ = s.Diagnose(context.Background(), DiagOpts{BeadID: "oro-target", Worktree: "/tmp/wt2", Symptom: "stuck"})
	// Spawn an agent for a different bead.
	_ = s.Review(context.Background(), ReviewOpts{BeadID: "oro-other", Worktree: "/tmp/wt3"})

	waitActive(t, s, 3)

	// Verify the BeadIDs are correctly set by checking the active agents.
	// We expect 2 agents with "oro-target" and 1 with "oro-other".
	s.mu.Lock()
	targetCount := 0
	otherCount := 0
	for _, agent := range s.active {
		switch agent.BeadID { //nolint:gocritic // test helper, switch overkill
		case "oro-target":
			targetCount++
		case "oro-other":
			otherCount++
		}
	}
	s.mu.Unlock()

	if targetCount != 2 {
		t.Fatalf("expected 2 agents with BeadID=oro-target, got %d", targetCount)
	}
	if otherCount != 1 {
		t.Fatalf("expected 1 agent with BeadID=oro-other, got %d", otherCount)
	}

	// Cancel all agents for oro-target.
	count, err := s.CancelForBead("oro-target")
	if err != nil {
		t.Fatalf("CancelForBead returned error: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 agents cancelled, got %d", count)
	}

	// Exactly 2 of 3 procs should be killed (the oro-target ones).
	// Don't assume index order — goroutines may call Spawn in any order.
	killedCount := 0
	for _, p := range procs {
		if p.wasKilled() {
			killedCount++
		}
	}
	if killedCount != 2 {
		t.Fatalf("expected exactly 2 procs killed, got %d", killedCount)
	}

	// Wait for cleanup of killed agents (active count should drop to 1).
	waitActive(t, s, 1)
}

func TestCancelForBeadNonExistentBead(t *testing.T) {
	s := NewSpawner(&mockBatchSpawner{})

	// Cancelling a bead with no agents should return count=0, no error.
	count, err := s.CancelForBead("oro-nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 agents cancelled, got %d", count)
	}
}

func TestCancelReviewsForBeadPreservesOtherOps(t *testing.T) {
	procs := []*mockProcess{
		newMockProcess("", nil), // target review
		newMockProcess("", nil), // target diagnosis
		newMockProcess("", nil), // other review
	}
	s := NewSpawner(&multiProcessSpawner{processes: procs})

	_ = s.Review(context.Background(), ReviewOpts{BeadID: "oro-target", Worktree: "/tmp/wt1"})
	_ = s.Diagnose(context.Background(), DiagOpts{BeadID: "oro-target", Worktree: "/tmp/wt2", Symptom: "stuck"})
	_ = s.Review(context.Background(), ReviewOpts{BeadID: "oro-other", Worktree: "/tmp/wt3"})
	waitActive(t, s, 3)

	var targetReview, targetDiagnosis, otherReview *mockProcess
	s.mu.Lock()
	for _, agent := range s.active {
		proc, ok := agent.proc.(*mockProcess)
		if !ok {
			s.mu.Unlock()
			t.Fatalf("agent process = %T, want *mockProcess", agent.proc)
		}
		switch {
		case agent.BeadID == "oro-target" && agent.Type == OpsReview:
			targetReview = proc
		case agent.BeadID == "oro-target" && agent.Type == OpsDiagnosis:
			targetDiagnosis = proc
		case agent.BeadID == "oro-other" && agent.Type == OpsReview:
			otherReview = proc
		}
	}
	s.mu.Unlock()
	if targetReview == nil || targetDiagnosis == nil || otherReview == nil {
		t.Fatalf("missing expected agents: targetReview=%v targetDiagnosis=%v otherReview=%v", targetReview != nil, targetDiagnosis != nil, otherReview != nil)
	}

	count, err := s.CancelReviewsForBead("oro-target")
	if err != nil {
		t.Fatalf("CancelReviewsForBead returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("review cancellation count = %d, want 1", count)
	}

	if !targetReview.wasKilled() {
		t.Fatal("target review process was not killed")
	}
	if targetDiagnosis.wasKilled() {
		t.Fatal("target diagnosis process was killed")
	}
	if otherReview.wasKilled() {
		t.Fatal("other bead review process was killed")
	}

	waitActive(t, s, 2)
	_, _ = s.CancelForBead("oro-target")
	_, _ = s.CancelForBead("oro-other")
}

func TestParseReviewOutputRequiresVerdictPrefix(t *testing.T) {
	tests := []struct {
		name        string
		stdout      string
		wantVerdict Verdict
	}{
		{
			name:        "bare approved fails closed",
			stdout:      "Review complete.\n\nAPPROVED\n\nAll good.",
			wantVerdict: VerdictFailed,
		},
		{
			name:        "bare rejected fails closed",
			stdout:      "Review complete.\n\nREJECTED: missing tests\n",
			wantVerdict: VerdictFailed,
		},
		{
			name:        "prefixed approved parses",
			stdout:      "Review complete.\n\nAll good.\n\nVERDICT: APPROVED",
			wantVerdict: VerdictApproved,
		},
		{
			name:        "prefixed rejected parses",
			stdout:      "Review complete.\n\nMissing tests.\n\nVERDICT: REJECTED",
			wantVerdict: VerdictRejected,
		},
		{
			name:        "prefixed verdict must be whole trimmed line",
			stdout:      "Review complete.\n\nVERDICT: APPROVED because tests pass\n",
			wantVerdict: VerdictFailed,
		},
		{
			name:        "prefixed verdict must be final non-empty line",
			stdout:      "Review complete.\n\nVERDICT: APPROVED\n\nAll good.",
			wantVerdict: VerdictFailed,
		},
		{
			name:        "only final prefixed verdict controls",
			stdout:      "Review complete.\n\nVERDICT: APPROVED\n\nFound a blocker.\n\nVERDICT: REJECTED\n\n",
			wantVerdict: VerdictRejected,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict, _ := parseReviewOutput(tt.stdout)
			if verdict != tt.wantVerdict {
				t.Fatalf("parseReviewOutput() verdict = %q, want %q", verdict, tt.wantVerdict)
			}
		})
	}
}

func TestParseReviewOutputStreamJSON(t *testing.T) {
	streamResult := reviewStreamJSONLine(t, "result", "Interpreting output\n\nVERDICT: APPROVED")
	streamText := reviewStreamJSONLine(t, "content_block_delta", "Review complete.\n\nVERDICT: APPROVED")
	systemHook := `{"type":"system","subtype":"hook_started","hook_name":"SessionStart:startup"}`

	tests := []struct {
		name        string
		stdout      string
		wantVerdict Verdict
	}{
		{
			name:        "result event text controls verdict",
			stdout:      streamText + "\n" + streamResult + "\n",
			wantVerdict: VerdictApproved,
		},
		{
			name:        "plain text fallback still works",
			stdout:      "Review complete.\n\nVERDICT: APPROVED",
			wantVerdict: VerdictApproved,
		},
		{
			name:        "no result scans accumulated assistant text",
			stdout:      streamText + "\n",
			wantVerdict: VerdictApproved,
		},
		{
			name:        "malformed stream falls back to raw scan",
			stdout:      "not-json\nVERDICT: APPROVED\n",
			wantVerdict: VerdictApproved,
		},
		{
			name:        "system hook noise does not hide result verdict",
			stdout:      systemHook + "\n" + streamText + "\n" + streamResult + "\n",
			wantVerdict: VerdictApproved,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict, _ := parseReviewOutput(tt.stdout)
			if verdict != tt.wantVerdict {
				t.Fatalf("parseReviewOutput() verdict = %q, want %q", verdict, tt.wantVerdict)
			}
		})
	}
}

func reviewStreamJSONLine(t *testing.T, kind string, text string) string {
	t.Helper()

	var payload any
	switch kind {
	case "result":
		payload = struct {
			Type   string `json:"type"`
			Result string `json:"result"`
		}{
			Type:   "result",
			Result: text,
		}
	case "content_block_delta":
		payload = struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}{
			Type: "content_block_delta",
			Delta: struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{
				Type: "text_delta",
				Text: text,
			},
		}
	default:
		t.Fatalf("unknown stream event kind %q", kind)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal stream event: %v", err)
	}
	return string(encoded)
}

// --- Non-zero exit code with verdict in stdout ---

func TestParseResultReviewProcessErrorFailsClosed(t *testing.T) {
	waitErr := errors.New("exit status 1")
	result := parseResult(OpsReview, "oro-process-error", "VERDICT: APPROVED\n\nAll good.", waitErr)

	if result.Verdict != VerdictFailed {
		t.Fatalf("expected VerdictFailed, got %q", result.Verdict)
	}
	if result.Err == nil {
		t.Fatal("expected non-nil Err")
	}
	if result.Feedback == "" {
		t.Fatal("expected stdout to be preserved as feedback")
	}
}

func TestParseResultNonZeroExitApproved(t *testing.T) {
	// A non-zero review process exit must fail closed even when stdout contains
	// an approved machine-readable verdict.
	waitErr := errors.New("exit status 1")
	result := parseResult(OpsReview, "oro-nz1", "Looking at code...\n\nVERDICT: APPROVED", waitErr)

	if result.Verdict != VerdictFailed {
		t.Fatalf("expected VerdictFailed, got %q", result.Verdict)
	}
	if result.Type != OpsReview {
		t.Fatalf("expected OpsReview, got %q", result.Type)
	}
	if result.BeadID != "oro-nz1" {
		t.Fatalf("expected bead ID oro-nz1, got %q", result.BeadID)
	}
	if result.Err == nil {
		t.Fatal("expected non-nil Err to record the non-zero exit")
	}
}

func TestParseResultNonZeroExitRejected(t *testing.T) {
	// A non-zero review process exit must fail closed even when stdout contains
	// a rejected machine-readable verdict.
	waitErr := errors.New("exit status 1")
	result := parseResult(OpsReview, "oro-nz2", "Code review...\n\nVERDICT: REJECTED\n", waitErr)

	if result.Verdict != VerdictFailed {
		t.Fatalf("expected VerdictFailed, got %q", result.Verdict)
	}
	if result.Feedback == "" {
		t.Fatal("expected non-empty feedback")
	}
	if result.Err == nil {
		t.Fatal("expected non-nil Err to record the non-zero exit")
	}
}

func TestParseResultNonZeroExitNoKeyword(t *testing.T) {
	// Non-zero exit with no APPROVED/REJECTED keyword should still be VerdictFailed.
	waitErr := errors.New("exit status 1")
	result := parseResult(OpsReview, "oro-nz3", "Something went wrong\n", waitErr)

	if result.Verdict != VerdictFailed {
		t.Fatalf("expected VerdictFailed, got %q", result.Verdict)
	}
	if result.Err == nil {
		t.Fatal("expected non-nil Err")
	}
}

func TestParseResultNonZeroExitNonReviewStillFails(t *testing.T) {
	// For non-review ops types without a successful merge signal, non-zero exit
	// should still produce VerdictFailed.
	waitErr := errors.New("exit status 1")
	result := parseResult(OpsDiagnosis, "oro-nz4", "diagnosis output\n", waitErr)

	if result.Verdict != VerdictFailed {
		t.Fatalf("expected VerdictFailed for non-review ops type with non-zero exit, got %q", result.Verdict)
	}
}

func TestParseMergeOutputRecognizesCleanRebaseSummary(t *testing.T) {
	stdout := "Rebase completed cleanly onto main (HEAD is `2d7cadb6`, also main's tip).\n" +
		"Working tree clean. No new commit required.\n"

	verdict, feedback := parseMergeOutput(stdout)

	if verdict != VerdictResolved {
		t.Fatalf("parseMergeOutput verdict = %q, want %q", verdict, VerdictResolved)
	}
	if !strings.Contains(feedback, "Rebase completed cleanly") {
		t.Fatalf("feedback should preserve successful resolver output, got %q", feedback)
	}
}

func TestParseResultMergeNonZeroCleanRebaseOutputResolved(t *testing.T) {
	waitErr := errors.New("exit status 1")
	stdout := "Rebase completed cleanly onto main (HEAD is `2d7cadb6`, also main's tip).\n" +
		"Working tree clean. Branch agent/oro-acqj is ahead of origin/main.\n"

	result := parseResult(OpsMerge, "oro-14zr", stdout, waitErr)

	if result.Verdict != VerdictResolved {
		t.Fatalf("parseResult verdict = %q, want %q", result.Verdict, VerdictResolved)
	}
	if result.Err != nil {
		t.Fatalf("resolved merge output should suppress non-zero process error, got %v", result.Err)
	}
	if !strings.Contains(result.Feedback, "Working tree clean") {
		t.Fatalf("feedback should include resolver output, got %q", result.Feedback)
	}
}

// --- Runtime error / fail-closed regression tests ---

// TestParseResultReviewRuntimeError verifies that when the review subprocess exits
// nonzero and the output is a runtime error message that incidentally contains
// "approved" (e.g. "model not approved for endpoint"), parseResult must return
// VerdictFailed — not VerdictApproved. This guards against false approvals from
// error payloads and echoed prompt template text.
func TestParseResultReviewRuntimeError(t *testing.T) {
	waitErr := errors.New("exit status 1")
	tests := []struct {
		name   string
		stdout string
	}{
		{
			name:   "model_not_approved_in_error_message",
			stdout: "Error: model 'codex-opus-4-9' is not approved for this endpoint.\n",
		},
		{
			name:   "api_error_not_approved",
			stdout: "API error: request not approved — contact support\n",
		},
		{
			name:   "prompt_template_text_echoed",
			stdout: "## Output\nAPPROVED or REJECTED\n\nFindings as: [severity] file:line\n",
		},
		{
			name:   "verdict_section_in_template",
			stdout: "## Verdict\n- Any Critical → REJECTED\n- Minor only → APPROVED\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseResult(OpsReview, "bead-rt-err", tt.stdout, waitErr)
			if result.Verdict == VerdictApproved {
				t.Errorf("stdout %q: must NOT yield VerdictApproved; runtime error output must fail closed", tt.stdout)
			}
			if result.Verdict != VerdictFailed {
				t.Errorf("stdout %q: expected VerdictFailed, got %q", tt.stdout, result.Verdict)
			}
		})
	}
}

// --- Timeout tests ---

func TestOpsReviewTimeout(t *testing.T) {
	if OpsReview.Timeout() != 35*time.Minute {
		t.Fatalf("OpsReview.Timeout() = %v, want %v", OpsReview.Timeout(), 35*time.Minute)
	}
}

func TestTypeTimeout(t *testing.T) {
	tests := []struct {
		name string
		typ  Type
		want time.Duration
	}{
		{name: "review", typ: OpsReview, want: 35 * time.Minute},
		{name: "write ac", typ: OpsWriteAC, want: 10 * time.Minute},
		{name: "dream", typ: OpsDream, want: 60 * time.Second},
		{name: "recovery ops", typ: OpsMerge, want: 15 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.typ.Timeout(); got != tt.want {
				t.Fatalf("%s.Timeout() = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}

func TestRecoveryOpsTimeoutBudget(t *testing.T) {
	spawner := NewSpawnerWithReviewTimeout(&mockBatchSpawner{process: newReadyMockProcess("", nil)}, 0)
	want := 15 * time.Minute
	for _, opsType := range []Type{OpsMerge, OpsDiagnosis, OpsEscalation, OpsEpicFix} {
		t.Run(string(opsType), func(t *testing.T) {
			if got := spawner.effectiveTimeout(opsType); got != want {
				t.Fatalf("%s effective timeout = %v, want %v", opsType, got, want)
			}
		})
	}
}

func TestSpawnerReviewTimeoutOverride(t *testing.T) {
	mock := &mockBatchSpawner{process: newReadyMockProcess("VERDICT: APPROVED", nil)}
	s := NewSpawnerWithReviewTimeout(mock, 45*time.Minute)

	if got := s.effectiveTimeout(OpsReview); got != 45*time.Minute {
		t.Fatalf("OpsReview effective timeout = %v, want 45m", got)
	}
	if got := s.effectiveTimeout(OpsWriteAC); got != 10*time.Minute {
		t.Fatalf("OpsWriteAC effective timeout = %v, want 10m", got)
	}
	if got := s.effectiveTimeout(OpsDream); got != 60*time.Second {
		t.Fatalf("OpsDream effective timeout = %v, want 60s", got)
	}

	fallback := NewSpawnerWithReviewTimeout(mock, 0)
	if got := fallback.effectiveTimeout(OpsReview); got != 35*time.Minute {
		t.Fatalf("zero override OpsReview effective timeout = %v, want 35m", got)
	}
	if got := fallback.effectiveTimeout(OpsMerge); got != 15*time.Minute {
		t.Fatalf("zero override OpsMerge effective timeout = %v, want 15m", got)
	}
}

func TestOneShotTimeout(t *testing.T) {
	// Process that never completes (blocks forever).
	proc := newMockProcess("", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)
	// Use a short timeout for testing (100ms instead of 5 minutes).
	s.timeout = 100 * time.Millisecond

	ctx := context.Background()
	ch := s.run(ctx, OpsDecompose, "oro-timeout", ".", "")

	result := waitResult(t, ch)

	// After timeout, the process should be killed and a failure result returned.
	if result.Verdict != VerdictFailed {
		t.Fatalf("expected VerdictFailed after timeout, got %q", result.Verdict)
	}
	if result.Err == nil {
		t.Fatal("expected timeout error")
	}
	if !containsSubstring(result.Err.Error(), "timeout") {
		t.Fatalf("expected timeout error message, got: %v", result.Err)
	}

	// Verify the process was killed.
	if !proc.wasKilled() {
		t.Fatal("expected process to be killed after timeout")
	}
}

func TestWaitForProcessIdleWatchdog(t *testing.T) {
	t.Run("stalled review killed with wedged error", func(t *testing.T) {
		proc := newMockProcess("", nil)
		mock := &mockBatchSpawner{process: proc}
		s := NewSpawner(mock)
		s.reviewIdle = 20 * time.Millisecond

		ch := s.Review(context.Background(), ReviewOpts{BeadID: "oro-idle", Worktree: "/tmp/wt"})

		result := waitResult(t, ch)
		if result.Verdict != VerdictFailed {
			t.Fatalf("result Verdict = %q, want %q", result.Verdict, VerdictFailed)
		}
		if result.Err == nil {
			t.Fatal("result Err = nil, want wedged error")
		}
		if got, want := result.Err.Error(), "ops: review wedged (no output for 20ms)"; got != want {
			t.Fatalf("result Err = %q, want %q", got, want)
		}
		if !proc.wasKilled() {
			t.Fatal("review process was not killed after idle watchdog fired")
		}
	})

	t.Run("steadily streaming review is not killed by idle watchdog", func(t *testing.T) {
		proc := newMockProcess("VERDICT: APPROVED", nil)
		mock := &mockBatchSpawner{process: proc}
		s := NewSpawner(mock)
		s.reviewIdle = 25 * time.Millisecond

		ch := s.Review(context.Background(), ReviewOpts{BeadID: "oro-stream", Worktree: "/tmp/wt"})
		done := make(chan struct{})
		defer close(done)
		go func() {
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					proc.setLastOutputAt(time.Now())
				case <-done:
					return
				}
			}
		}()

		time.Sleep(75 * time.Millisecond)
		if proc.wasKilled() {
			t.Fatal("streaming review process was killed by idle watchdog")
		}
		if err := proc.Kill(); err != nil {
			t.Fatalf("Kill() returned error: %v", err)
		}

		result := waitResult(t, ch)
		if result.Err != nil {
			t.Fatalf("streaming review result Err = %v, want nil", result.Err)
		}
		if result.Verdict != VerdictApproved {
			t.Fatalf("streaming review Verdict = %q, want %q", result.Verdict, VerdictApproved)
		}
	})

	t.Run("stalled merge is not killed by review idle watchdog", func(t *testing.T) {
		proc := newMockProcess("RESOLVED", nil)
		mock := &mockBatchSpawner{process: proc}
		s := NewSpawner(mock)
		s.reviewIdle = 20 * time.Millisecond
		s.timeout = 250 * time.Millisecond

		ch := s.ResolveMergeConflict(context.Background(), MergeOpts{BeadID: "oro-merge", Worktree: "/tmp/wt"})

		time.Sleep(75 * time.Millisecond)
		if proc.wasKilled() {
			t.Fatal("merge process was killed by review idle watchdog")
		}
		if err := proc.Kill(); err != nil {
			t.Fatalf("Kill() returned error: %v", err)
		}

		result := waitResult(t, ch)
		if result.Err != nil {
			t.Fatalf("merge result Err = %v, want nil", result.Err)
		}
		if result.Verdict != VerdictResolved {
			t.Fatalf("merge Verdict = %q, want %q", result.Verdict, VerdictResolved)
		}
	})

	t.Run("zero last output measures idle from process start", func(t *testing.T) {
		proc := newMockProcess("", nil)
		mock := &mockBatchSpawner{process: proc}
		s := NewSpawner(mock)
		s.reviewIdle = 35 * time.Millisecond

		start := time.Now()
		ch := s.Review(context.Background(), ReviewOpts{BeadID: "oro-zero", Worktree: "/tmp/wt"})
		result := waitResult(t, ch)
		elapsed := time.Since(start)

		if result.Err == nil || !strings.Contains(result.Err.Error(), "ops: review wedged") {
			t.Fatalf("result Err = %v, want review wedged error", result.Err)
		}
		if elapsed < s.reviewIdle {
			t.Fatalf("watchdog fired after %v, want at least %v", elapsed, s.reviewIdle)
		}
	})
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

func TestOpsWriteAC(t *testing.T) {
	// Verify OpsWriteAC.Model() returns "opus"
	if OpsWriteAC.Model() != "opus" {
		t.Fatalf("OpsWriteAC.Model() = %q, want %q", OpsWriteAC.Model(), "opus")
	}

	// Verify OpsWriteAC.Timeout() returns 10*time.Minute
	if OpsWriteAC.Timeout() != 10*time.Minute {
		t.Fatalf("OpsWriteAC.Timeout() = %v, want %v", OpsWriteAC.Timeout(), 10*time.Minute)
	}

	// Verify OpsReview.Timeout() returns 35 minutes (review needs time for test runs + analysis)
	if OpsReview.Timeout() != 35*time.Minute {
		t.Fatalf("OpsReview.Timeout() = %v, want %v", OpsReview.Timeout(), 35*time.Minute)
	}

	// Verify WriteAC spawns with the deep Sol route.
	proc := newReadyMockProcess("", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.WriteAC(context.Background(), WriteACOpts{
		BeadID:          "oro-wac",
		BeadTitle:       "Test bead",
		BeadDescription: "Test description",
		Workdir:         "/tmp/wt",
	})

	waitResult(t, ch)

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(calls))
	}
	if calls[0].model != "gpt-5.6-sol" || calls[0].reasoning != "low" {
		t.Fatalf("WriteAC expected Sol low, got model=%q reasoning=%q", calls[0].model, calls[0].reasoning)
	}
}

func TestMergePromptContainsBranch(t *testing.T) {
	proc := newReadyMockProcess("", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.ResolveMergeConflict(context.Background(), MergeOpts{
		BeadID:        "oro-test",
		Branch:        "agent/oro-xyz",
		ConflictFiles: []string{"file.go"},
	})

	_ = waitResult(t, ch)

	calls := mock.getCalls()
	if len(calls) == 0 {
		t.Fatal("expected at least one spawn call")
	}
	if !containsSubstring(calls[0].prompt, "agent/oro-xyz") {
		t.Errorf("merge prompt missing branch name 'agent/oro-xyz'")
	}
}

func TestMergePromptContainsTargetBranch(t *testing.T) {
	proc := newReadyMockProcess("", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	// Test with explicit TargetBranch
	ch := s.ResolveMergeConflict(context.Background(), MergeOpts{
		BeadID:        "oro-test",
		Branch:        "agent/oro-xyz",
		Worktree:      "/tmp/wt",
		ConflictFiles: []string{"file.go"},
		TargetBranch:  "develop",
	})

	_ = waitResult(t, ch)

	calls := mock.getCalls()
	if len(calls) == 0 {
		t.Fatal("expected at least one spawn call")
	}
	prompt := calls[0].prompt
	if !containsSubstring(prompt, "git rebase develop") {
		t.Errorf("merge prompt missing 'git rebase develop', got: %s", prompt)
	}

	// Test with empty TargetBranch (should default to 'main')
	mock = &mockBatchSpawner{process: proc}
	s = NewSpawner(mock)
	ch = s.ResolveMergeConflict(context.Background(), MergeOpts{
		BeadID:        "oro-test2",
		Branch:        "agent/oro-abc",
		Worktree:      "/tmp/wt",
		ConflictFiles: []string{"file.go"},
		TargetBranch:  "", // empty, should default to 'main'
	})

	_ = waitResult(t, ch)

	calls = mock.getCalls()
	if len(calls) == 0 {
		t.Fatal("expected at least one spawn call")
	}
	prompt = calls[0].prompt
	if !containsSubstring(prompt, "git rebase main") {
		t.Errorf("merge prompt missing default 'git rebase main', got: %s", prompt)
	}
}
