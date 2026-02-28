package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/merge"
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
		{"model", protocol.DefaultModel},
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
	cfg := &workConfig{beadID: "oro-test"}
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

func TestWorkAlreadySatisfiedBead(t *testing.T) {
	// When AC already passes and worker produces 0 commits, oro work should
	// close the bead and exit 0 — not return an error.
	// Observed with oro-30o which was already fixed before the worker ran.

	bs := &mockBeadSource{showDetail: testBead()}
	wt := &mockWorktreeManager{createPath: "/tmp/wt-test", createBranch: "bead/oro-test"}
	sp := &mockSpawner{proc: &mockProcess{}}
	mg := &mockMerger{result: &merge.Result{CommitSHA: "abc123"}}

	// hasNewWork=false (no commits produced), qgPassed=true (AC already satisfied)
	deps := testDeps(bs, wt, sp, mg, false, true)

	cfg := &workConfig{
		beadID:     "oro-test",
		model:      "sonnet",
		timeout:    5 * time.Second,
		skipReview: true,
	}

	err := executeWork(context.Background(), cfg, deps)
	// Must NOT return an error — AC was already satisfied.
	if err != nil {
		t.Fatalf("expected clean exit when AC already passes, got: %v", err)
	}

	// Bead must be closed.
	if bs.closeID != "oro-test" {
		t.Errorf("expected bead to be closed, closeID=%q", bs.closeID)
	}

	// Bead must NOT be reset to open.
	for _, u := range bs.updates {
		if u == "open" {
			t.Error("bead should not be reset to open when AC already passes")
		}
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
			hasNewWork: func(_, _ string) bool { return false },
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

func (m *envCapturingWorktreeManager) Create(_ context.Context, _ string) (string, string, error) {
	if m.captureEnv != nil {
		m.captureEnv()
	}
	return m.createPath, m.createBranch, nil
}
func (m *envCapturingWorktreeManager) Remove(_ context.Context, _ string) error       { return nil }
func (m *envCapturingWorktreeManager) Prune(_ context.Context) error                  { return nil }
func (m *envCapturingWorktreeManager) DeleteBranch(_ context.Context, _ string) error { return nil }
