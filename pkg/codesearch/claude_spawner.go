package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// ClaudeRerankSpawner implements RerankSpawner using claude -p.
type ClaudeRerankSpawner struct{}

// BuildCmd constructs the exec.Cmd for a claude -p invocation.
// It sets Stdin to an empty reader (prevents hang in non-TTY daemon context)
// and strips CLAUDECODE* env vars (prevents altered spawned-claude behavior).
func BuildCmd(ctx context.Context, prompt string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt, "--model", "haiku", "--output-format", "json") //nolint:gosec // prompt is constructed internally
	cmd.Stdin = strings.NewReader("")
	cmd.Env = slices.DeleteFunc(os.Environ(), func(e string) bool {
		return strings.HasPrefix(e, "CLAUDECODE")
	})
	return cmd
}

// Spawn runs claude -p with the given prompt and extracts the result from the JSON envelope.
func (s *ClaudeRerankSpawner) Spawn(ctx context.Context, prompt string) (string, error) {
	cmd := BuildCmd(ctx, prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude rerank: %w", err)
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
