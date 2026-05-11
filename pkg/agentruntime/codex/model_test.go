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

func TestBuildExecArgsAddsReasoningEffort(t *testing.T) {
	t.Parallel()

	args := buildExecArgsWithReasoning("gpt-5.5-codex", "high", "do work")
	if slices.Contains(args, "--reasoning-effort") {
		t.Fatalf("codex 0.130.0 does not support --reasoning-effort; got args: %v", args)
	}
	configIndex := slices.Index(args, "-c")
	if configIndex == -1 || configIndex+1 >= len(args) {
		t.Fatalf("codex reasoning effort should be passed through via -c config: %v", args)
	}
	if got, want := args[configIndex+1], `model_reasoning_effort="high"`; got != want {
		t.Fatalf("reasoning config = %q, want %q (args: %v)", got, want, args)
	}
	if got, want := args[len(args)-1], "do work"; got != want {
		t.Fatalf("prompt must remain the final positional arg; got final arg %q, want %q (args: %v)", got, want, args)
	}
}
