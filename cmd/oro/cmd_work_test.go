package main

import (
	"context"
	"os"
	"testing"

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
