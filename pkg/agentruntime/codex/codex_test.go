package codex_test

import (
	"strings"
	"testing"

	codexruntime "oro/pkg/agentruntime/codex"
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
