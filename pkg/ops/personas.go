package ops

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"oro/pkg/processenv"
)

// Persona identifies one focused reviewer in the multi-persona review team.
type Persona struct {
	ID       string
	Role     string
	Fragment string
}

func selectPersonas(opts ReviewOpts) []Persona {
	paths, err := reviewDiffPaths(context.Background(), opts.Worktree, opts.BaseBranch)
	if err != nil || len(paths) == 0 {
		return nil
	}
	if allDocsOnlyPaths(paths) {
		return nil
	}

	return []Persona{
		{
			ID:       "correctness",
			Role:     "ops_review_correctness",
			Fragment: "\n\nPersona focus: correctness. Look for behavior regressions, broken edge cases, and mismatches with the acceptance criteria.",
		},
		{
			ID:       "security",
			Role:     "ops_review_security",
			Fragment: "\n\nPersona focus: security. Look for injection, unsafe file/process/network handling, auth bypass, data leaks, and privilege boundary mistakes.",
		},
		{
			ID:       "adversarial",
			Role:     "ops_review_adversarial",
			Fragment: "\n\nPersona focus: adversarial review. Try to disprove the implementation by exercising unusual inputs, race windows, and failure paths.",
		},
		{
			ID:       "design",
			Role:     "ops_review_design",
			Fragment: "\n\nPersona focus: design. Check whether the change preserves local abstractions, keeps responsibilities clear, and avoids avoidable complexity.",
		},
		{
			ID:       "test",
			Role:     "ops_review_test",
			Fragment: "\n\nPersona focus: tests. Verify that tests specify the requested behavior, would fail on the old behavior, and cover important regressions.",
		},
		{
			ID:       "architecture",
			Role:     "ops_review_architecture",
			Fragment: "\n\nPersona focus: architecture. Check package boundaries, dependency direction, runtime wiring, and cross-module contracts.",
		},
	}
}

func reviewDiffPaths(ctx context.Context, worktree, baseBranch string) ([]string, error) {
	if worktree == "" {
		return nil, nil
	}
	base := baseBranch
	if base == "" {
		base = "main"
	}

	diffOut, err := gitOutput(ctx, worktree, "diff", "--name-only", base, "--")
	if err != nil {
		return nil, err
	}
	untrackedOut, err := gitOutput(ctx, worktree, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return append(strings.Fields(diffOut), strings.Fields(untrackedOut)...), nil
}

func gitOutput(ctx context.Context, worktree string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // fixed git invocation
	cmd.Dir = worktree
	cmd.Env = processenv.ForWorkdir(os.Environ(), worktree)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func allDocsOnlyPaths(paths []string) bool {
	for _, path := range paths {
		if !isDocsOnlyPath(path) {
			return false
		}
	}
	return len(paths) > 0
}
