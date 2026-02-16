package worker //nolint:testpackage // need access to buildClaudeArgs

import (
	"testing"
)

func TestBuildClaudeArgs(t *testing.T) {
	t.Run("base args without env vars", func(t *testing.T) {
		// Ensure env vars are not set
		t.Setenv("ORO_HOME", "")
		t.Setenv("ORO_PROJECT", "")

		args := buildClaudeArgs("claude-sonnet-4-20250514", "do the thing")
		expected := []string{"-p", "do the thing", "--model", "claude-sonnet-4-20250514"}
		if len(args) != len(expected) {
			t.Fatalf("got %d args, want %d: %v", len(args), len(expected), args)
		}
		for i, want := range expected {
			if args[i] != want {
				t.Errorf("arg[%d] = %q, want %q", i, args[i], want)
			}
		}
	})

	t.Run("adds add-dir and settings when ORO_HOME and ORO_PROJECT set", func(t *testing.T) {
		t.Setenv("ORO_HOME", "/home/user/.oro")
		t.Setenv("ORO_PROJECT", "myproj")

		args := buildClaudeArgs("claude-sonnet-4-20250514", "do the thing")

		expected := []string{
			"-p", "do the thing",
			"--model", "claude-sonnet-4-20250514",
			"--add-dir", "/home/user/.oro",
			"--settings", "/home/user/.oro/projects/myproj/settings.json",
		}
		if len(args) != len(expected) {
			t.Fatalf("got %d args, want %d: %v", len(args), len(expected), args)
		}
		for i, want := range expected {
			if args[i] != want {
				t.Errorf("arg[%d] = %q, want %q", i, args[i], want)
			}
		}
	})

	t.Run("no flags when only ORO_HOME set", func(t *testing.T) {
		t.Setenv("ORO_HOME", "/home/user/.oro")
		t.Setenv("ORO_PROJECT", "") // Explicitly unset

		args := buildClaudeArgs("claude-sonnet-4-20250514", "do the thing")
		expected := []string{"-p", "do the thing", "--model", "claude-sonnet-4-20250514"}
		if len(args) != len(expected) {
			t.Fatalf("got %d args, want %d: %v", len(args), len(expected), args)
		}
	})

	t.Run("no flags when only ORO_PROJECT set", func(t *testing.T) {
		t.Setenv("ORO_HOME", "") // Explicitly unset
		t.Setenv("ORO_PROJECT", "myproj")

		args := buildClaudeArgs("claude-sonnet-4-20250514", "do the thing")
		expected := []string{"-p", "do the thing", "--model", "claude-sonnet-4-20250514"}
		if len(args) != len(expected) {
			t.Fatalf("got %d args, want %d: %v", len(args), len(expected), args)
		}
	})
}

func TestBuildClaudeEnv(t *testing.T) {
	t.Run("returns nil when ORO_PROJECT not set", func(t *testing.T) {
		t.Setenv("ORO_PROJECT", "") // Explicitly unset
		env := buildClaudeEnv()
		if env != nil {
			t.Errorf("expected nil env, got %v", env)
		}
	})

	t.Run("includes CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD when ORO_PROJECT set", func(t *testing.T) {
		t.Setenv("ORO_PROJECT", "myproj")

		env := buildClaudeEnv()
		if env == nil {
			t.Fatal("expected non-nil env")
		}

		found := false
		for _, e := range env {
			if e == "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1 in env")
		}
	})

	t.Run("inherits existing environment when ORO_PROJECT set", func(t *testing.T) {
		t.Setenv("ORO_PROJECT", "myproj")
		t.Setenv("SOME_EXISTING_VAR", "hello")

		env := buildClaudeEnv()
		if env == nil {
			t.Fatal("expected non-nil env")
		}

		found := false
		for _, e := range env {
			if e == "SOME_EXISTING_VAR=hello" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected inherited env vars to be present")
		}
	})
}
