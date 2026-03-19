package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/codesearch"
	"oro/pkg/memory"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
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

	gotPath, _, err := setupWorktree(context.Background(), cfg, deps)
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

	gotPath, gotBranch, err := setupWorktree(context.Background(), cfg, deps)
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

func (m *envCapturingWorktreeManager) GCClosedWorktrees(_ context.Context, _ func(string) bool) error {
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

// TestWorkCommandEpicBranch verifies that when a bead has an Epic set,
// the epic branch is used as baseBranch/targetBranch throughout the pipeline:
//   - setupWorktree passes baseBranch from BeadDetail.Epic to Create
//   - hasCommitsAhead uses targetBranch not hardcoded main
//   - reviewLoop passes targetBranch as BaseBranch to ops review
//   - mergeToMain passes TargetBranch in merge.Opts
func TestWorkCommandEpicBranch(t *testing.T) {
	epicBead := &protocol.BeadDetail{
		ID:                 "oro-child",
		Title:              "Child bead",
		AcceptanceCriteria: "Tests pass",
		Epic:               "oro-epic",
	}
	epicTargetBranch := protocol.BranchPrefix + "oro-epic" // "agent/oro-epic"

	t.Run("setupWorktree passes baseBranch from Epic to Create", func(t *testing.T) {
		cfg := &workConfig{beadID: "oro-child", bead: epicBead}
		repoRoot := t.TempDir()
		wt := &mockWorktreeManager{
			createPath:   filepath.Join(repoRoot, ".worktrees", "oro-child"),
			createBranch: protocol.BranchPrefix + "oro-child",
		}
		deps := &workDeps{repoRoot: repoRoot, wtMgr: wt}

		_, _, err := setupWorktree(context.Background(), cfg, deps)
		if err != nil {
			t.Fatalf("setupWorktree: %v", err)
		}
		if wt.capturedBaseBranch != epicTargetBranch {
			t.Errorf("baseBranch passed to Create = %q, want %q", wt.capturedBaseBranch, epicTargetBranch)
		}
	})

	t.Run("hasNewWork uses targetBranch from Epic not main", func(t *testing.T) {
		var capturedTarget string
		bs := &mockBeadSource{showDetail: epicBead}
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
