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
		{"resume", "false"},
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
		{"claude-opus-4-6", "opus"},
		{"claude-sonnet-4-5-20250929", "sonnet"},
		{"claude-haiku-4-5-20251001", "haiku"},
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

func TestSetupWorktree_CollisionGuard(t *testing.T) {
	// setupWorktree should fail if worktree dir exists and --resume is false.
	cfg := &workConfig{beadID: "oro-test"}
	deps := &workDeps{repoRoot: t.TempDir()}

	// Create the worktree dir to trigger collision.
	wtDir := deps.repoRoot + "/.worktrees/oro-test"
	if err := os.MkdirAll(wtDir, 0o750); err != nil {
		t.Fatal(err)
	}

	_, _, err := setupWorktree(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected collision guard error")
	}
}
