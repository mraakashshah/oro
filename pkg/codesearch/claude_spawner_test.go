package codesearch_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"oro/pkg/agentmodel"
	"oro/pkg/codesearch"
)

// TestClaudeSpawner_ExtractsResultFromEnvelope verifies that ExtractResultFromEnvelope
// parses the JSON envelope produced by claude -p --output-format json, extracts the
// "result" field, and strips markdown code fences.
func TestClaudeSpawner_ExtractsResultFromEnvelope(t *testing.T) {
	inner := `[{"id": "9", "reason": "most relevant"}]`
	// Build a realistic Claude --output-format json envelope using json.Marshal
	// so that newlines and backticks are correctly escaped in the JSON output.
	type envelopeShape struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	e := envelopeShape{
		Type:    "result",
		Subtype: "success",
		Result:  "```json\n" + inner + "\n```",
		IsError: false,
	}
	envelopeJSON, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	got, err := codesearch.ExtractResultFromEnvelope(envelopeJSON)
	if err != nil {
		t.Fatalf("ExtractResultFromEnvelope: %v", err)
	}
	if got != inner {
		t.Errorf("got %q, want %q", got, inner)
	}
}

// TestSpawnCmdSetup verifies that BuildCmd constructs an exec.Cmd with:
//  1. Stdin set to a non-nil reader (prevents claude -p from hanging in non-TTY contexts)
//  2. No env var with CLAUDECODE prefix in cmd.Env (prevents altered spawned-claude behavior)
func TestSpawnCmdSetup(t *testing.T) {
	ctx := context.Background()
	cmd := codesearch.BuildCmd(ctx, "test prompt")

	if cmd.Stdin == nil {
		t.Error("cmd.Stdin must be non-nil: claude -p hangs indefinitely in non-TTY contexts when stdin is nil")
	}

	for _, env := range cmd.Env {
		if strings.HasPrefix(env, "CLAUDECODE") {
			t.Errorf("cmd.Env must not contain CLAUDECODE* vars, got: %s", env)
		}
	}
}

func TestBuildCmd_UsesCodexWhenConfigured(t *testing.T) {
	t.Setenv("ORO_AGENT_RUNTIME", "codex")

	cmd := codesearch.BuildCmd(context.Background(), "test prompt")

	if got := cmd.Args[0]; got != "codex" {
		t.Fatalf("BuildCmd command = %q, want codex", got)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "--skip-git-repo-check") {
		t.Fatalf("BuildCmd args = %v, want codex exec args", cmd.Args)
	}
}

func TestBuildCmd_NormalizesWorkdirAndGitEnv(t *testing.T) {
	t.Setenv("ORO_AGENT_RUNTIME", "codex")
	t.Setenv("PWD", "/poisoned/main")
	t.Setenv("GIT_DIR", "/poisoned/main/.git")
	t.Setenv("GIT_WORK_TREE", "/poisoned/main")
	t.Setenv("GIT_INDEX_FILE", "/poisoned/main/.git/index")

	cmd := codesearch.BuildCmd(context.Background(), "test prompt")

	if cmd.Dir == "" {
		t.Fatal("BuildCmd Dir must be set to a neutral workdir")
	}
	if cmd.Dir == "/poisoned/main" {
		t.Fatalf("BuildCmd Dir = poisoned main checkout %q", cmd.Dir)
	}
	if got := envValue(cmd.Env, "PWD"); got != cmd.Dir {
		t.Fatalf("PWD env = %q, want cmd.Dir %q", got, cmd.Dir)
	}
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		if got := envValue(cmd.Env, key); got != "" {
			t.Fatalf("%s env = %q, want unset", key, got)
		}
	}
}

func TestBuildCmdInWorkdir_UsesAssignedWorkdir(t *testing.T) {
	t.Setenv("ORO_AGENT_RUNTIME", "codex")
	t.Setenv("PWD", "/poisoned/main")
	t.Setenv("GIT_DIR", "/poisoned/main/.git")
	t.Setenv("GIT_WORK_TREE", "/poisoned/main")
	t.Setenv("GIT_INDEX_FILE", "/poisoned/main/.git/index")
	assigned := t.TempDir()

	cmd := codesearch.BuildCmdInWorkdir(context.Background(), "test prompt", assigned, "")

	if cmd.Dir != assigned {
		t.Fatalf("BuildCmdInWorkdir Dir = %q, want assigned %q", cmd.Dir, assigned)
	}
	if got := envValue(cmd.Env, "PWD"); got != assigned {
		t.Fatalf("PWD env = %q, want assigned %q", got, assigned)
	}
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		if got := envValue(cmd.Env, key); got != "" {
			t.Fatalf("%s env = %q, want unset", key, got)
		}
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

// TestCodesearchRerankerRoleResolves verifies that BuildCmdInWorkdir resolves the
// model via agentmodel.ResolveForRole when a role is provided, and falls back to
// ReadRuntime() when role is empty.
func TestCodesearchRerankerRoleResolves(t *testing.T) {
	t.Run("role resolves model via agentmodel", func(t *testing.T) {
		t.Setenv("ORO_AGENT_RUNTIME", "")

		_, expectedModel := agentmodel.ResolveForRole("codesearch_reranker")
		cmd := codesearch.BuildCmdInWorkdir(context.Background(), "probe", "", "codesearch_reranker")

		gotModel := argsValue(cmd.Args, "--model")
		if gotModel != expectedModel {
			t.Errorf("BuildCmdInWorkdir model = %q, want %q (from agentmodel.ResolveForRole)", gotModel, expectedModel)
		}
	})

	t.Run("empty role falls back to ReadRuntime default", func(t *testing.T) {
		t.Setenv("ORO_AGENT_RUNTIME", "")

		cmd := codesearch.BuildCmdInWorkdir(context.Background(), "probe", "", "")

		if cmd.Args[0] != "claude" {
			t.Errorf("empty role: command = %q, want claude (ReadRuntime default)", cmd.Args[0])
		}
	})
}

// argsValue finds the value following key in args (e.g. "--model" → next element).
func argsValue(args []string, key string) string {
	for i, arg := range args {
		if arg == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
