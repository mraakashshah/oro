package codex //nolint:testpackage // white-box test needs access to buildExecArgs

import (
	"slices"
	"testing"
)

func TestBuildExecArgsOmitsLegacyClaudeModels(t *testing.T) {
	t.Parallel()

	for _, model := range []string{"sonnet", "opus", "haiku", "claude-sonnet-4-5", "claude-opus-4-1"} {
		t.Run(model, func(t *testing.T) {
			args := buildExecArgs(model, "do work")
			if slices.Contains(args, "--model") {
				t.Fatalf("legacy Claude model %q should not be passed to codex exec: %v", model, args)
			}
		})
	}
}

func TestBuildExecArgsKeepsNativeCodexModel(t *testing.T) {
	t.Parallel()

	args := buildExecArgs("gpt-5-codex", "do work")
	for i, arg := range args {
		if arg == "--model" && i+1 < len(args) && args[i+1] == "gpt-5-codex" {
			return
		}
	}
	t.Fatalf("native Codex model should be passed through: %v", args)
}
