package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"oro/pkg/processenv"
)

const docsOnlyReviewPolicy = "docs-only: markdown and documentation paths only"

// ReviewPolicy identifies the rules used to approve a review outcome.
type ReviewPolicy struct {
	Hash             string
	RequiredPersonas []string
}

// requiredPersonas returns only the personas explicitly required by policy.
// A policy without required personas has no implicit fallback coverage.
func requiredPersonas(policy ReviewPolicy) []string {
	seen := make(map[string]struct{}, len(policy.RequiredPersonas))
	personas := make([]string, 0, len(policy.RequiredPersonas))
	for _, persona := range policy.RequiredPersonas {
		persona = strings.TrimSpace(persona)
		if persona == "" {
			continue
		}
		if _, ok := seen[persona]; ok {
			continue
		}
		seen[persona] = struct{}{}
		personas = append(personas, persona)
	}
	return personas
}

func reviewPolicy(opts ReviewOpts) ReviewPolicy {
	if opts.ReviewPolicy != nil {
		return *opts.ReviewPolicy
	}
	sum := sha256.Sum256([]byte(docsOnlyReviewPolicy))
	return ReviewPolicy{
		Hash: hex.EncodeToString(sum[:]),
		RequiredPersonas: []string{
			"correctness",
			"security",
			"adversarial",
			"design",
			"test",
			"architecture",
		},
	}
}

func buildDocsOnlyReviewOutcome(policy ReviewPolicy) (ReviewOutcome, error) {
	if strings.TrimSpace(policy.Hash) == "" {
		return ReviewOutcome{}, fmt.Errorf("docs-only review policy hash is required")
	}
	outcome := ReviewOutcome{
		Decision:   ReviewApproved,
		PolicyHash: policy.Hash,
		Verification: ReviewVerification{
			AcceptanceStatus: "passed",
		},
		Execution: ReviewExecution{
			Kind:     ReviewExecSucceeded,
			Complete: true,
		},
		Summary: "Approved automatically: diff only touches markdown/docs files.",
		Artifact: ReviewArtifactRef{
			SHA256: policy.Hash,
		},
	}
	if err := ValidateReviewOutcome(outcome); err != nil {
		return ReviewOutcome{}, fmt.Errorf("validate docs-only review outcome: %w", err)
	}
	return outcome, nil
}

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

func diffSizeExceeds(opts ReviewOpts, n int) bool {
	threshold := n
	if threshold <= 0 {
		threshold = 400
	}
	return diffSize(opts) > threshold
}

func diffSize(opts ReviewOpts) int {
	if opts.Worktree == "" {
		return 0
	}
	base := opts.BaseBranch
	if base == "" {
		base = "main"
	}
	out, err := gitOutput(context.Background(), opts.Worktree, "diff", "--numstat", base, "--")
	if err != nil {
		return 0
	}
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		total += numstatCount(fields[0]) + numstatCount(fields[1])
	}
	return total
}

func numstatCount(value string) int {
	if value == "-" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return n
}

func scopeToSurvivors(opts ReviewOpts, survivors []Finding) ReviewOpts {
	opts.ScopedFindings = append([]Finding(nil), survivors...)
	return opts
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
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
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
