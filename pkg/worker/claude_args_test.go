package worker //nolint:testpackage // need access to unexported buildClaudeArgs and buildClaudeEnv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildClaudeArgs_NoEnv(t *testing.T) {
	t.Setenv("ORO_HOME", "")
	t.Setenv("ORO_PROJECT", "")

	got := buildClaudeArgs("claude-opus-4-7", "hello")
	want := []string{"-p", "hello", "--model", "claude-opus-4-7", "--verbose", "--output-format", "stream-json"}
	if len(got) != 7 {
		t.Fatalf("expected length 7, got %d: %v", len(got), got)
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

	got := buildClaudeArgs("claude-opus-4-7", "hello")
	want := []string{
		"-p", "hello",
		"--model", "claude-opus-4-7",
		"--verbose", "--output-format", "stream-json",
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

		got := buildClaudeArgs("claude-opus-4-7", "hello")
		if len(got) != 7 {
			t.Fatalf("expected length 7 without ORO_PROJECT, got %d: %v", len(got), got)
		}
	})
}

func TestClaudeSpawnerForwardsReasoningAsEffort(t *testing.T) {
	tmpDir := t.TempDir()
	capturePath := filepath.Join(tmpDir, "args.txt")
	claudePath := filepath.Join(tmpDir, "claude")
	if err := os.WriteFile(claudePath, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPTURE", capturePath)
	t.Setenv("ORO_HOME", "")
	t.Setenv("ORO_PROJECT", "")

	router := NewRuntimeSpawnerRouter(&ClaudeSpawner{}, nil)
	proc, stdout, _, _, err := router.Spawn(context.Background(), "claude", "fable", "xhigh", "hello", tmpDir)
	if err != nil {
		t.Fatalf("spawn Claude: %v", err)
	}
	if stdout != nil {
		defer stdout.Close()
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("wait for Claude: %v", err)
	}

	gotBytes, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(gotBytes))
	if !slicesContainSequence(got, "--effort", "xhigh") {
		t.Fatalf("Claude args = %v, want --effort xhigh", got)
	}
}

func slicesContainSequence(values []string, first, second string) bool {
	for i := 0; i+1 < len(values); i++ {
		if values[i] == first && values[i+1] == second {
			return true
		}
	}
	return false
}

func TestBuildClaudeEnv_StripsClaudecode(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("ORO_PROJECT", "")

	env := buildClaudeEnv("")
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

	env := buildClaudeEnv("")
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

func TestBuildClaudeEnvSetsPWDToWorkdir(t *testing.T) {
	t.Setenv("PWD", "/wrong/root")
	t.Setenv("GIT_DIR", "/wrong/root/.git")
	t.Setenv("GIT_WORK_TREE", "/wrong/root")
	workdir := t.TempDir()

	env := buildClaudeEnv(workdir)
	found := false
	for _, e := range env {
		if e == "PWD="+workdir {
			found = true
		}
		if strings.HasPrefix(e, "PWD=") && e != "PWD="+workdir {
			t.Fatalf("PWD was not normalized to workdir: %q", e)
		}
		if strings.HasPrefix(e, "GIT_DIR=") || strings.HasPrefix(e, "GIT_WORK_TREE=") {
			t.Fatalf("git override env leaked: %q", e)
		}
	}
	if !found {
		t.Fatalf("expected PWD=%s in env", workdir)
	}
}
