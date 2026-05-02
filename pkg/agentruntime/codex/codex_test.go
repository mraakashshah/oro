package codex_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	codexruntime "oro/pkg/agentruntime/codex"
	"oro/pkg/ops"
	"oro/pkg/worker"
)

func TestCodexRuntimeSpawnContract(t *testing.T) {
	t.Parallel()

	args := codexruntimeTestBuildExecArgs("gpt-5-codex", "finish the bead")
	want := []string{
		"exec",
		"--skip-git-repo-check",
		"--sandbox", "workspace-write",
		"--model", "gpt-5-codex",
		"finish the bead",
	}
	if len(args) != len(want) {
		t.Fatalf("args len = %d, want %d: %v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q (full args: %v)", i, args[i], want[i], args)
		}
	}

	spawner := codexruntime.NewWorkerSpawner()
	if got := spawner.StreamFormat(); got != worker.StreamFormatLineText {
		t.Fatalf("StreamFormat() = %q, want %q", got, worker.StreamFormatLineText)
	}

	withoutModel := codexruntimeTestBuildExecArgs("", "plain prompt")
	joined := strings.Join(withoutModel, " ")
	if strings.Contains(joined, "--json") {
		t.Fatalf("Codex v1 worker contract must default to plain text, got args %v", withoutModel)
	}
	if strings.Contains(joined, "claude") {
		t.Fatalf("Codex contract must not contain Claude-specific flags or paths, got args %v", withoutModel)
	}
}

func TestCodexWorkerSpawnerSetsPWDToWorkdir(t *testing.T) {
	t.Setenv("PWD", "/wrong/root")
	t.Setenv("GIT_DIR", "/wrong/root/.git")
	workdir := t.TempDir()
	binDir := t.TempDir()
	report := filepath.Join(t.TempDir(), "pwd.txt")
	fakeCodex := filepath.Join(binDir, "codex")
	script := "#!/bin/sh\nprintf '%s|%s' \"$PWD\" \"${GIT_DIR-unset}\" > \"$ORO_TEST_REPORT\"\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ORO_TEST_REPORT", report)

	spawner := codexruntime.NewWorkerSpawner()
	proc, stdout, _, err := spawner.Spawn(context.Background(), "gpt-5-codex", "finish the bead", workdir)
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	defer stdout.Close()
	if err := proc.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	gotBytes, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if got := string(gotBytes); got != workdir+"|unset" {
		t.Fatalf("env report = %q, want %q", got, workdir+"|unset")
	}
}

func codexruntimeTestBuildExecArgs(model, prompt string) []string {
	args := []string{"exec", "--skip-git-repo-check", "--sandbox", "workspace-write"}
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, prompt)
}

// TestCodexUnsupportedModel verifies that when the Codex CLI exits with an
// unsupported-model error whose output contains "approved" incidentally
// (e.g. "model not approved for Codex"), the ops spawner returns VerdictFailed
// and does NOT misread the error text as an approval verdict.
func TestCodexUnsupportedModel(t *testing.T) {
	// Use sh -c to simulate a Codex process that exits nonzero with an error
	// message whose text happens to contain "approved" (the failure scenario).
	spawner := ops.NewExecSpawner(ops.RuntimeSpec{
		Command: "sh",
		BuildArgs: func(_, _ string) []string {
			return []string{"-c", "echo 'Error: model codex-opus-4-9 is not approved for Codex runtime'; exit 1"}
		},
	})
	s := ops.NewSpawner(spawner)
	ch := s.Review(context.Background(), ops.ReviewOpts{BeadID: "test-unsupported-model"})
	result := <-ch

	if result.Verdict == ops.VerdictApproved {
		t.Fatal("unsupported model error must NOT yield VerdictApproved; Codex runtime errors must fail closed")
	}
	if result.Verdict != ops.VerdictFailed {
		t.Fatalf("expected VerdictFailed for unsupported model error, got %q", result.Verdict)
	}
	if result.Err == nil {
		t.Fatal("expected non-nil Err for nonzero exit")
	}
}

func TestCodexOpsErrorPayloadFailsClosed(t *testing.T) {
	spawner := ops.NewExecSpawner(ops.RuntimeSpec{
		Command: "sh",
		BuildArgs: func(_, _ string) []string {
			return []string{"-c", "echo 'ERROR: {\"type\":\"error\",\"message\":\"unsupported model\"}'; exit 1"}
		},
	})
	s := ops.NewSpawner(spawner)
	ch := s.Review(context.Background(), ops.ReviewOpts{BeadID: "test-codex-error"})
	result := <-ch

	if result.Verdict != ops.VerdictFailed {
		t.Fatalf("Codex ops error payload verdict = %q, want failed", result.Verdict)
	}
	if result.Err == nil {
		t.Fatal("Codex ops error payload should preserve non-nil Err")
	}
}
