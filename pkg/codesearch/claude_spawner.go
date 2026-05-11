package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"oro/pkg/agentmodel"
	"oro/pkg/agentruntime"
	"oro/pkg/processenv"
)

const codexRerankModel = "gpt-5-codex"

// RuntimeRerankSpawner implements RerankSpawner using the configured runtime CLI.
type RuntimeRerankSpawner struct{}

// ClaudeRerankSpawner is retained as a compatibility alias for older call sites.
type ClaudeRerankSpawner = RuntimeRerankSpawner

// BuildCmd constructs the exec.Cmd for the configured rerank runtime.
//
//oro:testonly
func BuildCmd(ctx context.Context, prompt string) *exec.Cmd {
	return BuildCmdInWorkdir(ctx, prompt, "", "")
}

// BuildCmdInWorkdir constructs the exec.Cmd bound to workdir. Empty workdir
// falls back to a neutral temp dir for callers without an assigned worktree.
// role is resolved via agentmodel.ResolveForRole; empty role falls back to
// agentruntime.ReadRuntime() with the legacy haiku default.
func BuildCmdInWorkdir(ctx context.Context, prompt, workdir, role string) *exec.Cmd {
	runtime, model := resolveRerankModel(role)
	if runtime == agentruntime.RuntimeCodex {
		return buildCodexCmd(ctx, prompt, workdir, model)
	}
	return buildClaudeCmd(ctx, prompt, workdir, model)
}

// resolveRerankModel returns the runtime and model for the reranker.
// When role is non-empty, it calls agentmodel.ResolveForRole; otherwise it
// falls back to agentruntime.ReadRuntime() with a hardcoded codex or haiku default.
func resolveRerankModel(role string) (runtime, model string) {
	if role != "" {
		runtime, model, _ := agentmodel.ResolveForRole(role)
		return runtime, model
	}
	rt := agentruntime.ReadRuntime()
	if rt == agentruntime.RuntimeCodex {
		return rt, codexRerankModel
	}
	return rt, "haiku"
}

func buildClaudeCmd(ctx context.Context, prompt, workdir, model string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt, "--model", model, "--output-format", "json") //nolint:gosec // prompt is constructed internally
	cmd.Stdin = strings.NewReader("")
	env := slices.DeleteFunc(os.Environ(), func(e string) bool {
		return strings.HasPrefix(e, "CLAUDECODE")
	})
	bindRerankCmdToWorkdir(cmd, env, workdir)
	return cmd
}

func buildCodexCmd(ctx context.Context, prompt, workdir, model string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "codex", "exec", "--skip-git-repo-check", "--sandbox", "workspace-write", "--model", model, prompt) //nolint:gosec // prompt is constructed internally
	cmd.Stdin = strings.NewReader("")
	bindRerankCmdToWorkdir(cmd, os.Environ(), workdir)
	return cmd
}

func bindRerankCmdToWorkdir(cmd *exec.Cmd, env []string, workdir string) {
	if workdir == "" {
		workdir = os.TempDir()
	}
	cmd.Dir = workdir
	cmd.Env = processenv.ForWorkdir(env, workdir)
}

// Spawn runs the configured rerank CLI and normalizes its output into raw JSON.
func (s *RuntimeRerankSpawner) Spawn(ctx context.Context, prompt string) (string, error) {
	return s.SpawnInWorkdir(ctx, prompt, "")
}

// SpawnInWorkdir runs the configured rerank CLI with cwd/env bound to workdir.
func (s *RuntimeRerankSpawner) SpawnInWorkdir(ctx context.Context, prompt, workdir string) (string, error) {
	cmd := BuildCmdInWorkdir(ctx, prompt, workdir, "codesearch_reranker")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("runtime rerank: %w", err)
	}
	if cmd.Args[0] == "codex" {
		return strings.TrimSpace(string(out)), nil
	}
	return ExtractResultFromEnvelope(out)
}

// claudeEnvelope is the JSON structure returned by claude -p --output-format json.
type claudeEnvelope struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
}

// ExtractResultFromEnvelope parses the JSON envelope returned by claude -p --output-format json,
// extracts the "result" field, and strips any markdown code fences (```json ... ```).
func ExtractResultFromEnvelope(data []byte) (string, error) {
	var env claudeEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return "", fmt.Errorf("claude envelope parse: %w", err)
	}
	result := strings.TrimSpace(env.Result)
	// Strip opening fence line: ```json or ``` followed by newline
	if strings.HasPrefix(result, "```") {
		if idx := strings.Index(result, "\n"); idx >= 0 {
			result = result[idx+1:]
		}
		// Strip closing ```
		result = strings.TrimRight(result, "\n")
		result = strings.TrimSuffix(result, "```")
		result = strings.TrimSpace(result)
	}
	return result, nil
}
