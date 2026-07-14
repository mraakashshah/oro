package ops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"oro/pkg/processenv"
)

// buildRepoManifest records the full line range of every tracked file in worktree.
func buildRepoManifest(ctx context.Context, worktree string) (PromptManifest, error) {
	shown := make(map[string][][2]int)
	if worktree == "" {
		return PromptManifest{Shown: shown}, nil
	}

	cmd := exec.CommandContext(ctx, "git", "ls-files", "-z") //nolint:gosec // fixed git invocation
	cmd.Dir = worktree
	cmd.Env = processenv.ForWorkdir(os.Environ(), worktree)
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return PromptManifest{}, fmt.Errorf("build repo manifest: %w", ctxErr)
		}
		return PromptManifest{}, fmt.Errorf("list tracked files in %q: %w", worktree, err)
	}

	for _, path := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		clean, err := normalizeManifestPath(path)
		if err != nil {
			continue
		}
		lines := countFileLines(filepath.Join(worktree, filepath.FromSlash(clean)))
		if lines == 0 {
			lines = 1
		}
		shown[clean] = [][2]int{{1, lines}}
	}

	return PromptManifest{Shown: shown}, nil
}
