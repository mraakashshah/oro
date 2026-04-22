package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	codexruntime "oro/pkg/agentruntime/codex"
	"oro/pkg/codesearch"
	"oro/pkg/memory"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
	"oro/pkg/worker"
)

func TestNewWorkCmd_Flags(t *testing.T) {
	cmd := newWorkCmd()

	if cmd.Use != "work <bead-id>" {
		t.Fatalf("expected Use='work <bead-id>', got %s", cmd.Use)
	}

	tests := []struct {
		name     string
		defValue string
	}{
		{"model", ""}, // empty means "use bead metadata then default"; resolved at runtime
		{"timeout", "15m0s"},
		{"skip-review", "false"},
		{"dry-run", "false"},
	}
	for _, tt := range tests {
		f := cmd.Flag(tt.name)
		if f == nil {
			t.Fatalf("expected --%s flag", tt.name)
		}
		if f.DefValue != tt.defValue {
			t.Fatalf("--%s default: expected %q, got %q", tt.name, tt.defValue, f.DefValue)
		}
	}
}

func TestNewWorkCmd_RequiresBeadID(t *testing.T) {
	cmd := newWorkCmd()
	cmd.SetArgs([]string{})
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error when no bead ID provided")
	}
}

func TestNewWorkCmd_RegisteredInRoot(t *testing.T) {
	root := newRootCmd()
	found := false
	for _, sub := range root.Commands() {
		if sub.Name() == "work" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'work' subcommand in root")
	}
}

func TestWorkConfig_Validate_MissingAC(t *testing.T) {
	cfg := workConfig{
		bead: &protocol.BeadDetail{
			ID:    "oro-test",
			Title: "Test bead",
		},
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error for missing acceptance criteria")
	}
}

func TestWorkConfig_Validate_MissingTitle(t *testing.T) {
	cfg := workConfig{
		bead: &protocol.BeadDetail{
			ID: "oro-test",
		},
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error for missing title")
	}
}

func TestWorkConfig_Validate_OK(t *testing.T) {
	cfg := workConfig{
		bead: &protocol.BeadDetail{
			ID:                 "oro-test",
			Title:              "Test bead",
			AcceptanceCriteria: "Tests pass",
		},
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestModelShort(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"opus", "opus"},
		{"sonnet", "sonnet"},
		{"haiku", "haiku"},
		{"unknown-model", "unknown-model"},
	}
	for _, tt := range tests {
		got := modelShort(tt.input)
		if got != tt.want {
			t.Errorf("modelShort(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate short: got %q", got)
	}
	if got := truncate("this is a long string", 10); got != "this is a ..." {
		t.Errorf("truncate long: got %q", got)
	}
}

func TestSetupWorktree_ExistingWorktreeAutoResumes(t *testing.T) {
	// When worktree dir exists, setupWorktree should auto-resume (not error).
	cfg := &workConfig{beadID: "oro-test"}
	deps := &workDeps{repoRoot: t.TempDir()}

	// Create the worktree dir to simulate a previous run.
	wtDir := deps.repoRoot + "/.worktrees/oro-test"
	if err := os.MkdirAll(wtDir, 0o750); err != nil {
		t.Fatal(err)
	}

	gotPath, _, err := setupWorktree(context.Background(), cfg, deps, "main")
	if err != nil {
		t.Fatalf("expected auto-resume, got error: %v", err)
	}
	if gotPath != wtDir {
		t.Fatalf("expected path %s, got %s", wtDir, gotPath)
	}
}

func TestSetupWorktree_NoWorktreeCreatesNew(t *testing.T) {
	// When worktree dir does not exist, setupWorktree should call Create.
	cfg := &workConfig{beadID: "oro-test", bead: &protocol.BeadDetail{ID: "oro-test"}}
	repoRoot := t.TempDir()
	wtPath := repoRoot + "/.worktrees/oro-test"
	deps := &workDeps{
		repoRoot: repoRoot,
		wtMgr: &mockWorktreeManager{
			createPath:   wtPath,
			createBranch: protocol.BranchPrefix + "oro-test",
		},
	}

	gotPath, gotBranch, err := setupWorktree(context.Background(), cfg, deps, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != wtPath {
		t.Fatalf("expected path %s, got %s", wtPath, gotPath)
	}
	if gotBranch != protocol.BranchPrefix+"oro-test" {
		t.Fatalf("expected branch %s, got %s", protocol.BranchPrefix+"oro-test", gotBranch)
	}
}

func TestWorkNoCommits_AlwaysFails(t *testing.T) {
	// When worker produces 0 commits and AC is unparseable (no structured
	// Cmd: field), oro work must return an error. The general QG passing on
	// a clean checkout is NOT evidence of AC satisfaction.

	bs := &mockBeadSource{showDetail: testBead()} // testBead has AC="Tests pass" (unparseable)
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{result: &merge.Result{CommitSHA: "abc123"}}

	// hasNewWork=false (no commits produced), qgPassed=true (QG passes on clean checkout)
	deps := testDeps(bs, wt, sp, mg, false, true)

	cfg := &workConfig{
		beadID:     "oro-test",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}

	err := executeWork(context.Background(), cfg, deps)
	// MUST return an error — no commits and no parseable AC to verify.
	if err == nil {
		t.Fatal("expected error when worker produces no commits, got nil")
	}
	if !strings.Contains(err.Error(), "without producing commits") {
		t.Errorf("expected 'without producing commits' error, got: %v", err)
	}

	// Bead must NOT be closed.
	if bs.closeID != "" {
		t.Errorf("bead should not be closed when no commits produced, closeID=%q", bs.closeID)
	}

	// Merger must NOT be called — no commits to merge.
	if mg.called {
		t.Error("merger should not be called when no commits were produced")
	}

	// Worktree must be cleaned up.
	if len(wt.removed) == 0 {
		t.Error("expected worktree to be removed")
	}
}

func TestWorkNoCommits_ACAlreadySatisfied(t *testing.T) {
	// When worker produces 0 commits BUT the bead's specific acceptance
	// test already passes on main (code already implemented), oro work
	// should close the bead and return nil (success).

	// Bead with structured AC containing Cmd: and Test: fields.
	bead := &protocol.BeadDetail{
		ID:                 "oro-test",
		Title:              "Test bead",
		AcceptanceCriteria: "Test: pkg/foo/foo_test.go:TestFoo | Cmd: go test ./pkg/foo/... -run TestFoo | Assert: PASS",
	}

	// Create a temp worktree dir with the test file present.
	wtDir := t.TempDir()
	testFileDir := filepath.Join(wtDir, "pkg", "foo")
	if err := os.MkdirAll(testFileDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testFileDir, "foo_test.go"), []byte("package foo"), 0o600); err != nil {
		t.Fatal(err)
	}

	bs := &mockBeadSource{showDetail: bead}
	wt := &mockWorktreeManager{createPath: wtDir, createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{result: &merge.Result{CommitSHA: "abc123"}}

	deps := testDeps(bs, wt, sp, mg, false, true)
	// AC command passes (code already on main).
	deps.runShellCmd = func(_ context.Context, _, _ string) (bool, error) {
		return true, nil
	}

	cfg := &workConfig{
		beadID:     "oro-test",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}

	err := executeWork(context.Background(), cfg, deps)
	// Must succeed — AC already satisfied.
	if err != nil {
		t.Fatalf("expected nil error (AC satisfied), got: %v", err)
	}

	// Bead must be closed with reason.
	if bs.closeID != "oro-test" {
		t.Errorf("expected bead to be closed, closeID=%q", bs.closeID)
	}

	// Merger must NOT be called — no commits to merge.
	if mg.called {
		t.Error("merger should not be called when no commits were produced")
	}

	// Worktree must be cleaned up.
	if len(wt.removed) == 0 {
		t.Error("expected worktree to be removed")
	}
}

func TestWorkNoCommits_ACAlreadySatisfied_TestFileMissing(t *testing.T) {
	// When AC has structured Cmd:/Test: but the test file doesn't exist
	// on main, the feature is NOT done — return error as usual.

	bead := &protocol.BeadDetail{
		ID:                 "oro-test",
		Title:              "Test bead",
		AcceptanceCriteria: "Test: pkg/foo/foo_test.go:TestFoo | Cmd: go test ./pkg/foo/... -run TestFoo | Assert: PASS",
	}

	wtDir := t.TempDir() // empty — no test file

	bs := &mockBeadSource{showDetail: bead}
	wt := &mockWorktreeManager{createPath: wtDir, createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{result: &merge.Result{CommitSHA: "abc123"}}

	deps := testDeps(bs, wt, sp, mg, false, true)
	deps.runShellCmd = func(_ context.Context, _, _ string) (bool, error) {
		t.Error("runShellCmd should not be called when test file is missing")
		return false, nil
	}

	cfg := &workConfig{
		beadID:     "oro-test",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}

	err := executeWork(context.Background(), cfg, deps)

	// Must fail — test file missing means feature not implemented.
	if err == nil {
		t.Fatal("expected error when test file missing, got nil")
	}
	if bs.closeID != "" {
		t.Errorf("bead should not be closed, closeID=%q", bs.closeID)
	}
}

func TestWorkNoCommits_ACAlreadySatisfied_CmdFails(t *testing.T) {
	// When AC has structured Cmd:/Test:, test file exists, but the AC
	// command fails — feature is NOT done, return error.

	bead := &protocol.BeadDetail{
		ID:                 "oro-test",
		Title:              "Test bead",
		AcceptanceCriteria: "Test: pkg/foo/foo_test.go:TestFoo | Cmd: go test ./pkg/foo/... -run TestFoo | Assert: PASS",
	}

	wtDir := t.TempDir()
	testFileDir := filepath.Join(wtDir, "pkg", "foo")
	if err := os.MkdirAll(testFileDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testFileDir, "foo_test.go"), []byte("package foo"), 0o600); err != nil {
		t.Fatal(err)
	}

	bs := &mockBeadSource{showDetail: bead}
	wt := &mockWorktreeManager{createPath: wtDir, createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{result: &merge.Result{CommitSHA: "abc123"}}

	deps := testDeps(bs, wt, sp, mg, false, true)
	// AC command fails (feature not done).
	deps.runShellCmd = func(_ context.Context, _, _ string) (bool, error) {
		return false, nil
	}

	cfg := &workConfig{
		beadID:     "oro-test",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}

	err := executeWork(context.Background(), cfg, deps)

	if err == nil {
		t.Fatal("expected error when AC cmd fails, got nil")
	}
	if bs.closeID != "" {
		t.Errorf("bead should not be closed, closeID=%q", bs.closeID)
	}
}

func TestParseACCmd(t *testing.T) {
	tests := []struct {
		name string
		ac   string
		want string
		ok   bool
	}{
		{
			name: "standard format",
			ac:   "Test: pkg/foo/foo_test.go:TestFoo | Cmd: go test ./pkg/foo/... -run TestFoo | Assert: PASS",
			want: "go test ./pkg/foo/... -run TestFoo",
			ok:   true,
		},
		{
			name: "no cmd field",
			ac:   "Tests pass",
			want: "",
			ok:   false,
		},
		{
			name: "cmd at end (no Assert)",
			ac:   "Test: foo_test.go:TestBar | Cmd: pytest tests/test_bar.py",
			want: "pytest tests/test_bar.py",
			ok:   true,
		},
		{
			name: "empty string",
			ac:   "",
			want: "",
			ok:   false,
		},
		{
			name: "multiline ac with Cmd on second line",
			ac:   "Test: pkg/x_test.go:TestX\nCmd: go test ./pkg/... -run TestX\nAssert: PASS",
			want: "go test ./pkg/... -run TestX",
			ok:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseACCmd(tt.ac)
			if ok != tt.ok {
				t.Errorf("parseACCmd ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("parseACCmd = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseACTestFile(t *testing.T) {
	tests := []struct {
		name string
		ac   string
		want string
		ok   bool
	}{
		{
			name: "standard format",
			ac:   "Test: pkg/foo/foo_test.go:TestFoo | Cmd: go test ./pkg/foo/...",
			want: "pkg/foo/foo_test.go",
			ok:   true,
		},
		{
			name: "no test field",
			ac:   "Cmd: go test ./...",
			want: "",
			ok:   false,
		},
		{
			name: "test without function name",
			ac:   "Test: pkg/foo/foo_test.go | Cmd: go test",
			want: "pkg/foo/foo_test.go",
			ok:   true,
		},
		{
			name: "empty string",
			ac:   "",
			want: "",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseACTestFile(tt.ac)
			if ok != tt.ok {
				t.Errorf("parseACTestFile ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("parseACTestFile = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestWorkDepsMemoryAndCodeIndex verifies that newProductionDeps initializes
// memStore and codeIndex from ResolveProjectDBPaths, and that executeWork sets
// the ORO_PROJECT env var from readProjectName before worktree creation.
func TestWorkDepsMemoryAndCodeIndex(t *testing.T) {
	t.Run("newProductionDeps sets memStore and codeIndex when paths valid", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("ORO_HOME", tmpDir)
		t.Setenv("ORO_PROJECT", "")
		t.Chdir(tmpDir) // avoid picking up project from repo .oro/config.yaml

		deps, err := newProductionDeps()
		if err != nil {
			t.Fatalf("newProductionDeps: %v", err)
		}
		if deps.memStore == nil {
			t.Error("memStore should be non-nil when StateDBPath is valid")
		}
		if deps.codeIndex == nil {
			t.Error("codeIndex should be non-nil when CodeIndexDBPath is valid")
		}
	})

	t.Run("executeWork sets ORO_PROJECT from config.yaml before worktree creation", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroDir := filepath.Join(tmpDir, ".oro")
		if err := os.MkdirAll(oroDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte("project: testproject\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(tmpDir)
		t.Setenv("ORO_PROJECT", "") // project name must come from config.yaml

		var capturedProject string
		spyWt := &envCapturingWorktreeManager{
			captureEnv:   func() { capturedProject = os.Getenv("ORO_PROJECT") },
			createPath:   filepath.Join(tmpDir, ".worktrees", "oro-test"),
			createBranch: "bead/oro-test",
		}

		bs := &mockBeadSource{showDetail: testBead()}
		deps := &workDeps{
			beadSrc:    bs,
			wtMgr:      spyWt,
			spawner:    &mockSpawner{proc: &mockProcess{}},
			merger:     &mockMerger{result: &merge.Result{CommitSHA: "abc"}},
			repoRoot:   tmpDir,
			hasNewWork: func(_, _, _ string) bool { return false },
			runQG:      func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:     "oro-test",
			model:      "sonnet",
			timeout:    5 * time.Second,
			skipReview: true,
		}

		_ = executeWork(context.Background(), cfg, deps)

		if capturedProject != "testproject" {
			t.Errorf("ORO_PROJECT at worktree creation = %q, want %q", capturedProject, "testproject")
		}
	})
}

type testRuntimeWorkerSpawner struct{}

func (s *testRuntimeWorkerSpawner) Spawn(_ context.Context, _, _, _ string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	return nil, nil, nil, nil
}

func (s *testRuntimeWorkerSpawner) StreamFormat() worker.StreamFormat {
	return worker.StreamFormatClaudeJSON
}

type testRuntimeOpsSpawner struct{}

func (s *testRuntimeOpsSpawner) Spawn(_ context.Context, _, _, _ string) (ops.Process, error) {
	return nil, nil
}

func TestBuildDepsResolvesRuntime(t *testing.T) {
	t.Run("defaults to claude runtime when unset", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		t.Setenv(agentRuntimeEnvVar, "")
		t.Setenv("ORO_HOME", tmpDir)
		t.Setenv("ORO_PROJECT", "")

		wantWorker := &testRuntimeWorkerSpawner{}
		wantOps := &testRuntimeOpsSpawner{}
		prevWorker := newClaudeWorkerSpawner
		prevOps := newClaudeOpsSpawner
		newClaudeWorkerSpawner = func() worker.StreamingSpawner { return wantWorker }
		newClaudeOpsSpawner = func() ops.BatchSpawner { return wantOps }
		defer func() {
			newClaudeWorkerSpawner = prevWorker
			newClaudeOpsSpawner = prevOps
		}()

		deps, err := newProductionDeps()
		if err != nil {
			t.Fatalf("newProductionDeps: %v", err)
		}
		if deps.spawner != wantWorker {
			t.Fatalf("spawner = %#v, want injected claude runtime spawner %#v", deps.spawner, wantWorker)
		}

		rt, err := resolveProductionRuntime()
		if err != nil {
			t.Fatalf("resolveProductionRuntime: %v", err)
		}
		if rt.opsSpawn != wantOps {
			t.Fatalf("ops spawner = %#v, want injected claude ops spawner %#v", rt.opsSpawn, wantOps)
		}
	})

	t.Run("codex runtime resolves injected spawners", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		t.Setenv(agentRuntimeEnvVar, runtimeCodex)
		t.Setenv("ORO_HOME", tmpDir)
		t.Setenv("ORO_PROJECT", "")

		wantWorker := &testRuntimeWorkerSpawner{}
		wantOps := &testRuntimeOpsSpawner{}
		prevWorker := newCodexWorkerSpawner
		prevOps := newCodexOpsSpawner
		newCodexWorkerSpawner = func() worker.StreamingSpawner { return wantWorker }
		newCodexOpsSpawner = func() ops.BatchSpawner { return wantOps }
		defer func() {
			newCodexWorkerSpawner = prevWorker
			newCodexOpsSpawner = prevOps
		}()

		deps, err := newProductionDeps()
		if err != nil {
			t.Fatalf("newProductionDeps: %v", err)
		}
		if deps.spawner != wantWorker {
			t.Fatalf("spawner = %#v, want injected codex runtime spawner %#v", deps.spawner, wantWorker)
		}

		rt, err := resolveProductionRuntime()
		if err != nil {
			t.Fatalf("resolveProductionRuntime: %v", err)
		}
		if rt.opsSpawn != wantOps {
			t.Fatalf("ops spawner = %#v, want injected codex ops spawner %#v", rt.opsSpawn, wantOps)
		}
	})
}

func TestCodexBootstrapWithoutHooks(t *testing.T) {
	tmpDir := t.TempDir()
	worktree := filepath.Join(tmpDir, "wt")
	if err := os.MkdirAll(worktree, 0o750); err != nil {
		t.Fatal(err)
	}
	sharedInstructions := "# Shared Oro Instructions\nAlways use using-skills first.\n"
	if err := os.WriteFile(filepath.Join(worktree, "ORO_AGENT.md"), []byte(sharedInstructions), 0o644); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}

	prompt := codexruntime.BuildBootstrapPrompt("Finish the bead.", worktree)
	if !strings.Contains(prompt, "Shared Oro Instructions") {
		t.Fatal("codex bootstrap prompt should include shared instructions without relying on Claude hooks")
	}
	if !strings.Contains(prompt, "using-skills") {
		t.Fatal("codex bootstrap prompt should carry portable skill guidance")
	}
	if !strings.Contains(prompt, "## Task") || !strings.Contains(prompt, "Finish the bead.") {
		t.Fatal("codex bootstrap prompt should preserve the materialized task prompt")
	}
	if strings.Contains(prompt, ".claude/hooks") {
		t.Fatal("codex bootstrap prompt should not depend on Claude hook paths")
	}
}

func TestClaudeRuntimeDefaultPath(t *testing.T) {
	t.Setenv(agentRuntimeEnvVar, "")
	rt, err := resolveProductionRuntime()
	if err != nil {
		t.Fatalf("resolveProductionRuntime: %v", err)
	}
	if rt == nil {
		t.Fatal("resolveProductionRuntime returned nil runtime")
	}
	if rt.id != runtimeClaude {
		t.Fatalf("runtime id = %q, want %q", rt.id, runtimeClaude)
	}
	if rt.workerSpawn == nil || rt.opsSpawn == nil {
		t.Fatal("default Claude runtime must provide worker and ops spawners")
	}
}

func TestCodexRuntimeNoHookBootstrap(t *testing.T) {
	t.Parallel()

	TestCodexBootstrapWithoutHooks(t)
}

// envCapturingWorktreeManager implements dispatcher.WorktreeManager and
// captures the value of ORO_PROJECT at the moment Create() is called.
type envCapturingWorktreeManager struct {
	captureEnv   func()
	createPath   string
	createBranch string
}

func (m *envCapturingWorktreeManager) Create(_ context.Context, _, _ string) (string, string, error) {
	if m.captureEnv != nil {
		m.captureEnv()
	}
	return m.createPath, m.createBranch, nil
}
func (m *envCapturingWorktreeManager) Remove(_ context.Context, _ string) error       { return nil }
func (m *envCapturingWorktreeManager) Prune(_ context.Context) error                  { return nil }
func (m *envCapturingWorktreeManager) DeleteBranch(_ context.Context, _ string) error { return nil }
func (m *envCapturingWorktreeManager) BranchExists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (m *envCapturingWorktreeManager) MergeFFOnly(_ context.Context, _ string, _ string) (string, error) {
	return "", nil
}

func (m *envCapturingWorktreeManager) UpdateBranchRef(_ context.Context, _, _ string) error {
	return nil
}

func (m *envCapturingWorktreeManager) GCClosedWorktrees(_ context.Context, _ func(string) bool) error {
	return nil
}

func (m *envCapturingWorktreeManager) Exists(_ context.Context, _ string) bool {
	return true // default: paths are valid
}

func (m *envCapturingWorktreeManager) RebaseOnto(_ context.Context, _, _ string) error {
	return nil
}

func (m *envCapturingWorktreeManager) PushBranch(_ context.Context, _ string) error {
	return nil
}

func (m *envCapturingWorktreeManager) CreateBranch(_ context.Context, _, _ string) error {
	return nil
}

// TestSpawnAndWaitWithMemoryAndCodeContext verifies that spawnAndWait wires
// MemoryContext and CodeSearchContext into AssemblePrompt, and passes memStore
// to DrainOutput.
func TestSpawnAndWaitWithMemoryAndCodeContext(t *testing.T) {
	ctx := context.Background()

	t.Run("non-nil memStore with memories gives non-empty MemoryContext", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)
		_, err := store.Insert(ctx, memory.InsertParams{
			Content:    "Test bead dependency injection pattern works well",
			Type:       "lesson",
			Source:     "test",
			Confidence: 0.9,
		})
		if err != nil {
			t.Fatalf("seed memory: %v", err)
		}

		sp := &captureSpawner{proc: &mockProcess{}}
		deps := &workDeps{
			spawner:    sp,
			memStore:   store,
			hasNewWork: func(_, _, _ string) bool { return false },
			runQG:      func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:  "oro-test",
			timeout: 5 * time.Second,
			bead:    testBead(),
		}

		if err := spawnAndWait(ctx, cfg, deps, "/tmp/wt", "sonnet", 0, "", nil); err != nil {
			t.Fatalf("spawnAndWait: %v", err)
		}

		if !strings.Contains(sp.capturedPrompt, "dependency injection") {
			t.Errorf("MemoryContext not injected: seeded memory content missing from prompt")
		}
	})

	t.Run("non-nil codeIndex with indexed code gives non-empty CodeSearchContext", func(t *testing.T) {
		// Create a temp CodeIndex backed by an on-disk SQLite file.
		dbPath := filepath.Join(t.TempDir(), "code.db")
		idx, err := codesearch.NewCodeIndex(dbPath)
		if err != nil {
			t.Fatalf("NewCodeIndex: %v", err)
		}
		defer idx.Close() //nolint:errcheck // test cleanup

		// Write a Go file whose content matches the bead title "Test bead".
		srcDir := t.TempDir()
		goSrc := `package work

// TestBeadHelper executes a test bead cycle.
func TestBeadHelper() string {
	return "test bead result"
}
`
		if err := os.WriteFile(filepath.Join(srcDir, "work.go"), []byte(goSrc), 0o600); err != nil {
			t.Fatalf("write go file: %v", err)
		}

		if _, err := idx.Build(ctx, srcDir); err != nil {
			t.Fatalf("Build code index: %v", err)
		}

		sp := &captureSpawner{proc: &mockProcess{}}
		deps := &workDeps{
			spawner:    sp,
			codeIndex:  idx,
			hasNewWork: func(_, _, _ string) bool { return false },
			runQG:      func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:  "oro-test",
			timeout: 5 * time.Second,
			bead:    testBead(),
		}

		if err := spawnAndWait(ctx, cfg, deps, "/tmp/wt", "sonnet", 0, "", nil); err != nil {
			t.Fatalf("spawnAndWait: %v", err)
		}

		if !strings.Contains(sp.capturedPrompt, "Relevant Code") {
			t.Errorf("CodeSearchContext not injected: 'Relevant Code' section missing from prompt")
		}
		if !strings.Contains(sp.capturedPrompt, "TestBeadHelper") {
			t.Errorf("CodeSearchContext missing indexed function; prompt snippet: %q",
				sp.capturedPrompt[:min(500, len(sp.capturedPrompt))])
		}
	})

	t.Run("both nil assembles prompt without error", func(t *testing.T) {
		sp := &captureSpawner{proc: &mockProcess{}}
		deps := &workDeps{
			spawner:    sp,
			memStore:   nil,
			codeIndex:  nil,
			hasNewWork: func(_, _, _ string) bool { return false },
			runQG:      func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:  "oro-test",
			timeout: 5 * time.Second,
			bead:    testBead(),
		}

		if err := spawnAndWait(ctx, cfg, deps, "/tmp/wt", "sonnet", 0, "", nil); err != nil {
			t.Errorf("spawnAndWait with nil deps should not error: %v", err)
		}
		if sp.capturedPrompt == "" {
			t.Error("expected non-empty prompt even with nil context fields")
		}
	})

	t.Run("DrainOutput receives deps.memStore", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)

		// [MEMORY] marker in claude stdout should be captured into memStore via DrainOutput.
		marker := sjNDJSON(sjTextDelta("[MEMORY] type=gotcha tags=test: code search requires indexed content to return results\n"))
		sp := &captureSpawner{proc: &mockProcess{}, stdout: marker}
		deps := &workDeps{
			spawner:    sp,
			memStore:   store,
			hasNewWork: func(_, _, _ string) bool { return false },
			runQG:      func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:  "oro-test",
			timeout: 5 * time.Second,
			bead:    testBead(),
		}

		if err := spawnAndWait(ctx, cfg, deps, "/tmp/wt", "sonnet", 0, "", nil); err != nil {
			t.Fatalf("spawnAndWait: %v", err)
		}

		mems, listErr := store.List(ctx, memory.ListOpts{Limit: 10})
		if listErr != nil {
			t.Fatalf("store.List: %v", listErr)
		}
		found := false
		for _, m := range mems {
			if strings.Contains(m.Content, "code search requires indexed content") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DrainOutput did not receive memStore: [MEMORY] marker not captured (got %d memories)", len(mems))
		}
	})
}

// --- Epic branch test helpers ---

// spyOpsReviewer implements opsReviewer and records the BaseBranch used in Review calls.
type spyOpsReviewer struct {
	capturedBaseBranch string
}

func (s *spyOpsReviewer) Review(_ context.Context, opts ops.ReviewOpts) <-chan ops.Result {
	s.capturedBaseBranch = opts.BaseBranch
	ch := make(chan ops.Result, 1)
	ch <- ops.Result{Verdict: ops.VerdictApproved}
	return ch
}

// captureMerger implements merger and records the Opts passed to Merge.
type captureMerger struct {
	capturedOpts merge.Opts
	result       *merge.Result
}

func (m *captureMerger) Merge(_ context.Context, opts merge.Opts) (*merge.Result, error) {
	m.capturedOpts = opts
	return m.result, nil
}

// TestWorkCommandEpicBranch verifies that when a bead has an Epic set pointing
// to an epic-type parent, the epic branch is used as baseBranch/targetBranch
// throughout the pipeline:
//   - setupWorktree receives and passes baseBranch to Create
//   - hasCommitsAhead uses targetBranch not hardcoded main
//   - reviewLoop passes targetBranch as BaseBranch to ops review
//   - mergeToMain passes TargetBranch in merge.Opts
func TestWorkCommandEpicBranch(t *testing.T) {
	epicParent := &protocol.BeadDetail{
		ID:    "oro-epic",
		Title: "The epic",
		Type:  "epic",
	}
	epicBead := &protocol.BeadDetail{
		ID:                 "oro-child",
		Title:              "Child bead",
		AcceptanceCriteria: "Tests pass",
		Epic:               "oro-epic",
	}
	epicTargetBranch := protocol.EpicBranchPrefix + "oro-epic" // "epic/oro-epic"

	t.Run("setupWorktree passes baseBranch to Create", func(t *testing.T) {
		cfg := &workConfig{beadID: "oro-child", bead: epicBead}
		repoRoot := t.TempDir()
		wt := &mockWorktreeManager{
			createPath:   filepath.Join(repoRoot, ".worktrees", "oro-child"),
			createBranch: protocol.BranchPrefix + "oro-child",
		}
		deps := &workDeps{repoRoot: repoRoot, wtMgr: wt}

		_, _, err := setupWorktree(context.Background(), cfg, deps, epicTargetBranch)
		if err != nil {
			t.Fatalf("setupWorktree: %v", err)
		}
		if wt.capturedBaseBranch != epicTargetBranch {
			t.Errorf("baseBranch passed to Create = %q, want %q", wt.capturedBaseBranch, epicTargetBranch)
		}
	})

	t.Run("hasNewWork uses targetBranch from Epic not main", func(t *testing.T) {
		var capturedTarget string
		bs := &mockBeadSource{
			showDetail: epicBead,
			shownByID: map[string]*protocol.BeadDetail{
				"oro-child": epicBead,
				"oro-epic":  epicParent,
			},
		}
		wt := &mockWorktreeManager{createPath: "/tmp/wt", createBranch: protocol.BranchPrefix + "oro-child"}
		sp := &mockSpawner{proc: &mockProcess{}}
		mg := &mockMerger{result: &merge.Result{CommitSHA: "abc"}}

		deps := &workDeps{
			beadSrc:  bs,
			wtMgr:    wt,
			spawner:  sp,
			merger:   mg,
			repoRoot: "/tmp",
			hasNewWork: func(_, _, target string) bool {
				capturedTarget = target
				return true
			},
			runQG: func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:     "oro-child",
			model:      "sonnet",
			timeout:    5 * time.Second,
			skipReview: true,
		}

		_ = executeWork(context.Background(), cfg, deps)

		if capturedTarget != epicTargetBranch {
			t.Errorf("targetBranch passed to hasNewWork = %q, want %q", capturedTarget, epicTargetBranch)
		}
	})

	t.Run("reviewLoop passes targetBranch as BaseBranch", func(t *testing.T) {
		spy := &spyOpsReviewer{}
		cfg := &workConfig{beadID: "oro-child", bead: epicBead}
		deps := &workDeps{opsMgr: spy}

		model := "sonnet"
		attempt := 0
		feedback := ""
		err := reviewLoop(context.Background(), cfg, deps, "/tmp/wt", epicTargetBranch, &model, &attempt, &feedback, nil)
		if err != nil {
			t.Fatalf("reviewLoop: %v", err)
		}
		if spy.capturedBaseBranch != epicTargetBranch {
			t.Errorf("BaseBranch in ReviewOpts = %q, want %q", spy.capturedBaseBranch, epicTargetBranch)
		}
	})

	t.Run("mergeToMain passes TargetBranch in merge.Opts", func(t *testing.T) {
		mg := &captureMerger{result: &merge.Result{CommitSHA: "abc"}}
		cfg := &workConfig{beadID: "oro-child", bead: epicBead}
		deps := &workDeps{merger: mg}

		_, err := mergeToMain(context.Background(), cfg, deps, "/tmp/wt", protocol.BranchPrefix+"oro-child", epicTargetBranch)
		if err != nil {
			t.Fatalf("mergeToMain: %v", err)
		}
		if mg.capturedOpts.TargetBranch != epicTargetBranch {
			t.Errorf("TargetBranch in merge.Opts = %q, want %q", mg.capturedOpts.TargetBranch, epicTargetBranch)
		}
	})

	t.Run("standalone bead (empty Epic) uses main for baseBranch and targetBranch", func(t *testing.T) {
		standaloneBead := &protocol.BeadDetail{
			ID:                 "oro-standalone",
			Title:              "Standalone bead",
			AcceptanceCriteria: "Tests pass",
			Epic:               "",
		}
		var capturedTarget string
		bs := &mockBeadSource{showDetail: standaloneBead}
		wt := &mockWorktreeManager{
			createPath:   "/tmp/wt-standalone",
			createBranch: protocol.BranchPrefix + "oro-standalone",
		}
		sp := &mockSpawner{proc: &mockProcess{}}
		mg := &mockMerger{result: &merge.Result{CommitSHA: "abc"}}

		deps := &workDeps{
			beadSrc:       bs,
			wtMgr:         wt,
			spawner:       sp,
			merger:        mg,
			repoRoot:      "/tmp",
			defaultBranch: "main",
			hasNewWork: func(_, _, target string) bool {
				capturedTarget = target
				return true
			},
			runQG: func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:     "oro-standalone",
			model:      "sonnet",
			timeout:    5 * time.Second,
			skipReview: true,
		}

		_ = executeWork(context.Background(), cfg, deps)

		if capturedTarget != "main" {
			t.Errorf("standalone targetBranch = %q, want %q", capturedTarget, "main")
		}
		if wt.capturedBaseBranch != "main" {
			t.Errorf("standalone baseBranch = %q, want %q", wt.capturedBaseBranch, "main")
		}
	})
}

// TestExecuteWork_NonEpicParent_UsesMain verifies that when a bead's parent
// (bead.Epic) is a non-epic bead (e.g. type "task"), the worktree is created
// from "main" and not from "agent/<parentID>".
//
// Acceptance criteria: sub-beads of non-epic parents branch from main.
func TestExecuteWork_NonEpicParent_UsesMain(t *testing.T) {
	taskParent := &protocol.BeadDetail{
		ID:    "task-parent",
		Title: "A task bead (not an epic)",
		Type:  "task",
	}
	childBead := &protocol.BeadDetail{
		ID:                 "child-bead",
		Title:              "Child of task",
		AcceptanceCriteria: "Tests pass",
		Epic:               "task-parent", // parent is a task, not an epic
	}

	bs := &mockBeadSource{
		showDetail: childBead,
		shownByID: map[string]*protocol.BeadDetail{
			"child-bead":  childBead,
			"task-parent": taskParent,
		},
	}
	wt := &mockWorktreeManager{
		createPath:   "/tmp/wt-child",
		createBranch: protocol.BranchPrefix + "child-bead",
	}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{result: &merge.Result{CommitSHA: "deadbeef"}}

	deps := &workDeps{
		beadSrc:       bs,
		wtMgr:         wt,
		spawner:       sp,
		merger:        mg,
		repoRoot:      "/tmp",
		defaultBranch: "main",
		hasNewWork:    func(_, _, _ string) bool { return true },
		runQG:         func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
	}
	cfg := &workConfig{
		beadID:     "child-bead",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}

	_ = executeWork(context.Background(), cfg, deps)

	// Non-epic parent must not produce an "agent/<parentID>" base branch.
	if wt.capturedBaseBranch != "main" {
		t.Errorf("baseBranch = %q; want %q (non-epic parent must not produce agent/ branch)", wt.capturedBaseBranch, "main")
	}
}

// TestExitError_Error verifies that exitError.Error() returns its message.
func TestExitError_Error(t *testing.T) {
	e := &exitError{code: 1, msg: "something went wrong"}
	if got := e.Error(); got != "something went wrong" {
		t.Errorf("exitError.Error() = %q, want %q", got, "something went wrong")
	}
}

// TestReviewLoop_RejectedThenApproved verifies the rejection-then-approval path:
// one rejection triggers a re-spawn + re-QG, then the next review approves.
func TestReviewLoop_RejectedThenApproved(t *testing.T) {
	opsMgr := &sequentialOpsReviewer{results: []ops.Result{
		{Verdict: ops.VerdictRejected, Feedback: "needs more tests"},
		{Verdict: ops.VerdictApproved},
	}}
	sp := &mockSpawner{proc: &mockProcess{}}
	deps := &workDeps{
		opsMgr:  opsMgr,
		spawner: sp,
		runQG:   func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
	}
	cfg := &workConfig{
		beadID:  "oro-test",
		timeout: 5 * time.Second,
		bead:    testBead(),
	}
	model := "sonnet"
	attempt := 0
	feedback := ""

	err := reviewLoop(context.Background(), cfg, deps, "/tmp/wt", "main", &model, &attempt, &feedback, nil)
	if err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
	if model != protocol.ModelOpus {
		t.Errorf("model after rejection = %q, want %q", model, protocol.ModelOpus)
	}
	if !sp.called {
		t.Error("expected spawner to be called for re-execution after rejection")
	}
}

// TestReviewLoop_RejectedMaxTimes returns an exitError after maxReviewRejects.
func TestReviewLoop_RejectedMaxTimes(t *testing.T) {
	opsMgr := &sequentialOpsReviewer{results: []ops.Result{
		{Verdict: ops.VerdictRejected, Feedback: "first reject"},
		{Verdict: ops.VerdictRejected, Feedback: "second reject"},
	}}
	sp := &mockSpawner{proc: &mockProcess{}}
	deps := &workDeps{
		opsMgr:  opsMgr,
		spawner: sp,
		runQG:   func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
	}
	cfg := &workConfig{
		beadID:  "oro-test",
		timeout: 5 * time.Second,
		bead:    testBead(),
	}
	model := "sonnet"
	attempt := 0
	feedback := ""

	err := reviewLoop(context.Background(), cfg, deps, "/tmp/wt", "main", &model, &attempt, &feedback, nil)
	if err == nil {
		t.Fatal("expected error on max rejections")
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *exitError, got %T: %v", err, err)
	}
	if ee.code != exitCodeRetries {
		t.Errorf("exit code = %d, want %d", ee.code, exitCodeRetries)
	}
	if !strings.Contains(ee.msg, "second reject") {
		t.Errorf("expected last feedback in msg, got: %q", ee.msg)
	}
}

// TestReviewLoop_QGFailAfterRejection returns an exitError when QG fails after re-spawn.
func TestReviewLoop_QGFailAfterRejection(t *testing.T) {
	opsMgr := &sequentialOpsReviewer{results: []ops.Result{
		{Verdict: ops.VerdictRejected, Feedback: "fix required"},
	}}
	sp := &mockSpawner{proc: &mockProcess{}}
	deps := &workDeps{
		opsMgr:  opsMgr,
		spawner: sp,
		runQG:   func(_ context.Context, _ string, _ bool) (bool, string, error) { return false, "tests failed", nil },
	}
	cfg := &workConfig{
		beadID:  "oro-test",
		timeout: 5 * time.Second,
		bead:    testBead(),
	}
	model := "sonnet"
	attempt := 0
	feedback := ""

	err := reviewLoop(context.Background(), cfg, deps, "/tmp/wt", "main", &model, &attempt, &feedback, nil)
	if err == nil {
		t.Fatal("expected error when QG fails after review rejection")
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *exitError, got %T: %v", err, err)
	}
	if ee.code != exitCodeRetries {
		t.Errorf("exit code = %d, want %d", ee.code, exitCodeRetries)
	}
}

// TestReviewLoop_QGErrorAfterRejection returns a wrapped error when runQG itself errors.
func TestReviewLoop_QGErrorAfterRejection(t *testing.T) {
	opsMgr := &sequentialOpsReviewer{results: []ops.Result{
		{Verdict: ops.VerdictRejected, Feedback: "fix required"},
	}}
	sp := &mockSpawner{proc: &mockProcess{}}
	deps := &workDeps{
		opsMgr:  opsMgr,
		spawner: sp,
		runQG: func(_ context.Context, _ string, _ bool) (bool, string, error) {
			return false, "", errors.New("qg crashed")
		},
	}
	cfg := &workConfig{
		beadID:  "oro-test",
		timeout: 5 * time.Second,
		bead:    testBead(),
	}
	model := "sonnet"
	attempt := 0
	feedback := ""

	err := reviewLoop(context.Background(), cfg, deps, "/tmp/wt", "main", &model, &attempt, &feedback, nil)
	if err == nil {
		t.Fatal("expected error when runQG returns error")
	}
	if !strings.Contains(err.Error(), "quality gate error") {
		t.Errorf("expected 'quality gate error' in error, got: %v", err)
	}
}

// TestReviewLoop_SpawnErrorAfterRejection returns a wrapped error when re-spawn fails.
func TestReviewLoop_SpawnErrorAfterRejection(t *testing.T) {
	opsMgr := &sequentialOpsReviewer{results: []ops.Result{
		{Verdict: ops.VerdictRejected, Feedback: "fix required"},
	}}
	sp := &mockSpawner{proc: &mockProcess{}, err: errors.New("spawn failed")}
	deps := &workDeps{
		opsMgr:  opsMgr,
		spawner: sp,
	}
	cfg := &workConfig{
		beadID:  "oro-test",
		timeout: 5 * time.Second,
		bead:    testBead(),
	}
	model := "sonnet"
	attempt := 0
	feedback := ""

	err := reviewLoop(context.Background(), cfg, deps, "/tmp/wt", "main", &model, &attempt, &feedback, nil)
	if err == nil {
		t.Fatal("expected error when spawn fails after rejection")
	}
	if !strings.Contains(err.Error(), "claude re-spawn after review") {
		t.Errorf("expected 're-spawn' in error, got: %v", err)
	}
}

// TestReviewLoop_VerdictFailed_ReturnsNil verifies that a failed verdict (timeout etc.)
// causes reviewLoop to log and return nil (continue without review).
func TestReviewLoop_VerdictFailed_ReturnsNil(t *testing.T) {
	opsMgr := &sequentialOpsReviewer{results: []ops.Result{
		{Verdict: ops.VerdictFailed, Feedback: "timed out"},
	}}
	deps := &workDeps{opsMgr: opsMgr}
	cfg := &workConfig{
		beadID:  "oro-test",
		timeout: 5 * time.Second,
		bead:    testBead(),
	}
	model := "sonnet"
	attempt := 0
	feedback := ""

	err := reviewLoop(context.Background(), cfg, deps, "/tmp/wt", "main", &model, &attempt, &feedback, nil)
	if err != nil {
		t.Fatalf("expected nil when review verdict is Failed (timeout), got: %v", err)
	}
}

// TestDefaultRunShellCmd_Success verifies that a succeeding command returns (true, nil).
func TestDefaultRunShellCmd_Success(t *testing.T) {
	dir := t.TempDir()
	passed, err := defaultRunShellCmd(context.Background(), dir, "true")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !passed {
		t.Error("expected passed=true for 'true' command")
	}
}

// TestDefaultRunShellCmd_ExitFailure verifies that a failing command returns (false, nil).
func TestDefaultRunShellCmd_ExitFailure(t *testing.T) {
	dir := t.TempDir()
	passed, err := defaultRunShellCmd(context.Background(), dir, "false")
	if err != nil {
		t.Fatalf("unexpected error (ExitError should not propagate): %v", err)
	}
	if passed {
		t.Error("expected passed=false for 'false' command")
	}
}

// TestHasCommitsAhead_NonGitDir returns false when the directory is not a git repo.
func TestHasCommitsAhead_NonGitDir(t *testing.T) {
	tmpDir := t.TempDir()
	if hasCommitsAhead(tmpDir, "feature", "main") {
		t.Error("expected false for non-git directory")
	}
}

// TestHasCommitsAhead_WithRealGitRepo verifies both the "ahead" and "not ahead" paths
// against a real (temporary) git repository.
func TestHasCommitsAhead_WithRealGitRepo(t *testing.T) {
	tmpDir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_COMMITTER_NAME=test",
			"GIT_AUTHOR_EMAIL=t@t.com", "GIT_COMMITTER_EMAIL=t@t.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init", "-b", "main")
	run("config", "commit.gpgsign", "false")
	_ = os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("init"), 0o600)
	run("add", ".")
	run("commit", "-m", "init")

	// Same branch: main..main = 0 → false.
	if hasCommitsAhead(tmpDir, "main", "main") {
		t.Error("expected false when branch == targetBranch")
	}

	// Create feature branch with one commit ahead of main.
	run("checkout", "-b", "feature")
	_ = os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("change"), 0o600)
	run("add", ".")
	run("commit", "-m", "feature change")

	if !hasCommitsAhead(tmpDir, "feature", "main") {
		t.Error("expected true when feature has 1 commit ahead of main")
	}
}

// TestWorkDeps_DefaultBranch verifies that newWorkDeps resolves DefaultBranch from
// config or --base-branch flag, and that setupWorktree passes it to wtMgr.Create.
func TestWorkDeps_DefaultBranch(t *testing.T) {
	t.Run("newWorkDeps uses DefaultBranch from config", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroDir := filepath.Join(tmpDir, ".oro")
		if err := os.MkdirAll(oroDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"),
			[]byte("project: testproject\ndefault_branch: develop\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(tmpDir)

		var capturedDefaultBranch string
		spyWt := &branchCapturingWorktreeManager{
			captureDefaultBranch: func(branch string) { capturedDefaultBranch = branch },
			createPath:           filepath.Join(tmpDir, ".worktrees", "oro-test"),
			createBranch:         "bead/oro-test",
		}

		bs := &mockBeadSource{showDetail: testBead()}
		// Load DefaultBranch from config, like newProductionDeps does.
		defaultBranch := readDefaultBranch(".")
		if defaultBranch == "" {
			defaultBranch = "main"
		}
		deps := &workDeps{
			beadSrc:       bs,
			wtMgr:         spyWt,
			spawner:       &mockSpawner{proc: &mockProcess{}},
			merger:        &mockMerger{result: &merge.Result{CommitSHA: "abc"}},
			repoRoot:      tmpDir,
			defaultBranch: defaultBranch,
			hasNewWork:    func(_, _, _ string) bool { return false },
			runQG:         func(_ context.Context, _ string, _ bool) (bool, string, error) { return true, "", nil },
		}
		cfg := &workConfig{
			beadID:     "oro-test",
			model:      "sonnet",
			timeout:    5 * time.Second,
			skipReview: true,
		}

		_ = executeWork(context.Background(), cfg, deps)

		if capturedDefaultBranch != "develop" {
			t.Errorf("defaultBranch passed to wtMgr.Create = %q, want %q", capturedDefaultBranch, "develop")
		}
	})

	t.Run("setupWorktree passes resolved defaultBranch to wtMgr.Create", func(t *testing.T) {
		tmpDir := t.TempDir()
		wtPath := filepath.Join(tmpDir, ".worktrees", "oro-test")
		branchName := "bead/oro-test"

		var capturedDefaultBranch string
		spyWt := &branchCapturingWorktreeManager{
			captureDefaultBranch: func(branch string) { capturedDefaultBranch = branch },
			createPath:           wtPath,
			createBranch:         branchName,
		}

		cfg := &workConfig{beadID: "oro-test"}
		deps := &workDeps{wtMgr: spyWt, repoRoot: tmpDir}

		_, _, err := setupWorktree(context.Background(), cfg, deps, "epic/oro-parent")
		if err != nil {
			t.Fatalf("setupWorktree: %v", err)
		}

		if capturedDefaultBranch != "epic/oro-parent" {
			t.Errorf("defaultBranch = %q, want %q", capturedDefaultBranch, "epic/oro-parent")
		}
	})
}

// branchCapturingWorktreeManager implements dispatcher.WorktreeManager and
// captures the branch parameter passed to Create().
type branchCapturingWorktreeManager struct {
	captureDefaultBranch func(string)
	createPath           string
	createBranch         string
}

func (m *branchCapturingWorktreeManager) Create(_ context.Context, _, branch string) (string, string, error) {
	if m.captureDefaultBranch != nil {
		m.captureDefaultBranch(branch)
	}
	return m.createPath, m.createBranch, nil
}
func (m *branchCapturingWorktreeManager) Remove(_ context.Context, _ string) error       { return nil }
func (m *branchCapturingWorktreeManager) Prune(_ context.Context) error                  { return nil }
func (m *branchCapturingWorktreeManager) DeleteBranch(_ context.Context, _ string) error { return nil }
func (m *branchCapturingWorktreeManager) BranchExists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (m *branchCapturingWorktreeManager) MergeFFOnly(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func (m *branchCapturingWorktreeManager) GCClosedWorktrees(_ context.Context, _ func(string) bool) error {
	return nil
}

func (m *branchCapturingWorktreeManager) Exists(_ context.Context, _ string) bool {
	return false // default: worktree doesn't exist, so Create() will be called
}

func (m *branchCapturingWorktreeManager) UpdateBranchRef(_ context.Context, _, _ string) error {
	return nil
}

func (m *branchCapturingWorktreeManager) RebaseOnto(_ context.Context, _, _ string) error {
	return nil
}

func (m *branchCapturingWorktreeManager) PushBranch(_ context.Context, _ string) error {
	return nil
}

func (m *branchCapturingWorktreeManager) CreateBranch(_ context.Context, _, _ string) error {
	return nil
}
