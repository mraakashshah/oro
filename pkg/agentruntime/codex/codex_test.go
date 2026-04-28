package codex_test

import (
	"context"
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
