package ops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"oro/pkg/processenv"
)

// buildRepoManifest records the full line range of every tracked file in worktree.
func buildRepoManifest(ctx context.Context, worktree string) PromptManifest {
	shown := make(map[string][][2]int)
	if worktree == "" {
		return PromptManifest{Shown: shown}
	}

	cmd := exec.CommandContext(ctx, "git", "ls-files", "-z") //nolint:gosec // fixed git invocation
	cmd.Dir = worktree
	cmd.Env = processenv.ForWorkdir(os.Environ(), worktree)
	out, err := cmd.Output()
	if err != nil {
		return PromptManifest{Shown: shown}
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

	return PromptManifest{Shown: shown}
}
