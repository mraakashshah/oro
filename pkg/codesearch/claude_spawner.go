package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"oro/pkg/agentruntime"
	"oro/pkg/processenv"
)

const codexRerankModel = "gpt-5-codex"

// RuntimeRerankSpawner implements RerankSpawner using the configured runtime CLI.
type RuntimeRerankSpawner struct{}

// ClaudeRerankSpawner is retained as a compatibility alias for older call sites.
type ClaudeRerankSpawner = RuntimeRerankSpawner

// BuildCmd constructs the exec.Cmd for the configured rerank runtime.
func BuildCmd(ctx context.Context, prompt string) *exec.Cmd {
	if agentruntime.ReadRuntime() == agentruntime.RuntimeCodex {
		return buildCodexCmd(ctx, prompt)
	}
	return buildClaudeCmd(ctx, prompt)
}

func buildClaudeCmd(ctx context.Context, prompt string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt, "--model", "haiku", "--output-format", "json") //nolint:gosec // prompt is constructed internally
	cmd.Stdin = strings.NewReader("")
	env := slices.DeleteFunc(os.Environ(), func(e string) bool {
		return strings.HasPrefix(e, "CLAUDECODE")
	})
	bindRerankCmdToNeutralWorkdir(cmd, env)
	return cmd
}

func buildCodexCmd(ctx context.Context, prompt string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "codex", "exec", "--skip-git-repo-check", "--sandbox", "workspace-write", "--model", codexRerankModel, prompt) //nolint:gosec // prompt is constructed internally
	cmd.Stdin = strings.NewReader("")
	bindRerankCmdToNeutralWorkdir(cmd, os.Environ())
	return cmd
}

func bindRerankCmdToNeutralWorkdir(cmd *exec.Cmd, env []string) {
	workdir := os.TempDir()
	cmd.Dir = workdir
	cmd.Env = processenv.ForWorkdir(env, workdir)
}

// Spawn runs the configured rerank CLI and normalizes its output into raw JSON.
func (s *RuntimeRerankSpawner) Spawn(ctx context.Context, prompt string) (string, error) {
	cmd := BuildCmd(ctx, prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("runtime rerank: %w", err)
	}
	if agentruntime.ReadRuntime() == agentruntime.RuntimeCodex {
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
