package codesearch

import (
	"context"
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

// Spawn runs claude -p with the given prompt and returns stdout.
func (s *ClaudeRerankSpawner) Spawn(ctx context.Context, prompt string) (string, error) {
	cmd := BuildCmd(ctx, prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude rerank: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
