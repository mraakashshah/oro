package worker //nolint:testpackage // need access to unexported buildClaudeArgs and buildClaudeEnv

import (
	"strings"
	"testing"
)

func TestBuildClaudeArgs_NoEnv(t *testing.T) {
	t.Setenv("ORO_HOME", "")
	t.Setenv("ORO_PROJECT", "")

	got := buildClaudeArgs("claude-opus-4-6", "hello")
	want := []string{"-p", "hello", "--model", "claude-opus-4-6"}
	if len(got) != 4 {
		t.Fatalf("expected length 4, got %d: %v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestBuildClaudeArgs_WithORO(t *testing.T) {
	t.Setenv("ORO_HOME", "/tmp/h")
	t.Setenv("ORO_PROJECT", "p")

	got := buildClaudeArgs("claude-opus-4-6", "hello")
	want := []string{
		"-p", "hello",
		"--model", "claude-opus-4-6",
		"--add-dir", "/tmp/h",
		"--settings", "/tmp/h/projects/p/settings.json",
	}
	if len(got) != len(want) {
		t.Fatalf("expected length %d, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: want %q, got %q", i, want[i], got[i])
		}
	}

	t.Run("only ORO_HOME set returns length-4 slice", func(t *testing.T) {
		t.Setenv("ORO_HOME", "/tmp/h")
		t.Setenv("ORO_PROJECT", "")

		got := buildClaudeArgs("claude-opus-4-6", "hello")
		if len(got) != 4 {
			t.Fatalf("expected length 4 without ORO_PROJECT, got %d: %v", len(got), got)
		}
	})
}

func TestBuildClaudeEnv_StripsClaudecode(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("ORO_PROJECT", "")

	env := buildClaudeEnv()
	if env == nil {
		t.Fatal("expected non-nil env (nil would inherit parent env including CLAUDECODE)")
	}
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDECODE=") {
			t.Errorf("CLAUDECODE must be stripped, but found %q", e)
		}
		if strings.HasPrefix(e, "CLAUDE_CODE_ADDITIONAL_DIRECTORIES") {
			t.Errorf("unexpected CLAUDE_CODE_ADDITIONAL_DIRECTORIES without ORO_PROJECT: %s", e)
		}
	}
}

func TestBuildClaudeEnv_AddsAdditionalDirs(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("ORO_PROJECT", "p")

	env := buildClaudeEnv()
	if env == nil {
		t.Fatal("expected non-nil env")
	}
	found := false
	for _, e := range env {
		if e == "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1" {
			found = true
		}
		if strings.HasPrefix(e, "CLAUDECODE=") {
			t.Errorf("CLAUDECODE must be stripped, but found %q", e)
		}
	}
	if !found {
		t.Error("expected CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1 to be present")
	}
}
