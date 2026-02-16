package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"oro/pkg/dispatcher"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
	"oro/pkg/worker"

	"github.com/spf13/cobra"
)

// Exit codes for oro work.
const (
	exitCodeBeadError   = 3
	exitCodeRetries     = 1
	exitCodeMergeFail   = 2
	maxQGRetriesPerTier = 3
	maxReviewRejects    = 2
)

// workConfig holds parsed flags and loaded bead for the work command.
type workConfig struct {
	beadID     string
	model      string
	timeout    time.Duration
	skipReview bool
	resume     bool
	dryRun     bool
	bead       *protocol.BeadDetail
}

// validate checks that the loaded bead has the required fields.
func (c *workConfig) validate() error {
	if c.bead.Title == "" {
		return fmt.Errorf("bead %s has no title", c.bead.ID)
	}
	if c.bead.AcceptanceCriteria == "" {
		return fmt.Errorf("bead %s has no acceptance criteria — add with: bd update %s --acceptance-criteria \"...\"", c.bead.ID, c.bead.ID)
	}
	return nil
}

// newWorkCmd creates the "oro work" subcommand.
func newWorkCmd() *cobra.Command {
	var cfg workConfig

	cmd := &cobra.Command{
		Use:   "work <bead-id>",
		Short: "Execute a bead through the full lifecycle",
		Long: `Drives a single bead end-to-end: worktree → claude → quality gate →
ops review → merge → close. Runnable by a human or a claude agent.

All retries, model escalation, and review feedback loops are handled
automatically. Exit code 0 means the bead landed on main.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.beadID = args[0]
			return runWork(cmd, &cfg)
		},
	}

	cmd.Flags().StringVar(&cfg.model, "model", protocol.DefaultModel, "starting Claude model")
	cmd.Flags().DurationVar(&cfg.timeout, "timeout", 15*time.Minute, "per-claude-spawn timeout")
	cmd.Flags().BoolVar(&cfg.skipReview, "skip-review", false, "skip ops review gate")
	cmd.Flags().BoolVar(&cfg.resume, "resume", false, "resume from existing worktree")
	cmd.Flags().BoolVar(&cfg.dryRun, "dry-run", false, "show execution plan without running")

	return cmd
}

// workDeps holds injectable dependencies for testability.
type workDeps struct {
	beadSrc  dispatcher.BeadSource
	wtMgr    dispatcher.WorktreeManager
	spawner  worker.StreamingSpawner
	opsMgr   *ops.Spawner
	merger   *merge.Coordinator
	repoRoot string
}

// newProductionDeps creates real dependencies.
func newProductionDeps() (*workDeps, error) {
	repoRoot, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getwd: %w", err)
	}
	runner := &dispatcher.ExecCommandRunner{}
	return &workDeps{
		beadSrc:  dispatcher.NewCLIBeadSource(runner),
		wtMgr:    dispatcher.NewGitWorktreeManager(repoRoot, runner),
		spawner:  &worker.ClaudeSpawner{},
		opsMgr:   ops.NewSpawner(&ops.ClaudeOpsSpawner{}),
		merger:   merge.NewCoordinator(&merge.ExecGitRunner{}),
		repoRoot: repoRoot,
	}, nil
}

// runWork orchestrates the full bead lifecycle.
func runWork(_ *cobra.Command, cfg *workConfig) error {
	// Set up signal handling for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	deps, err := newProductionDeps()
	if err != nil {
		return err
	}

	return executeWork(ctx, cfg, deps)
}

// executeWork is the testable core of the work command.
func executeWork(ctx context.Context, cfg *workConfig, deps *workDeps) error { //nolint:funlen,gocognit,cyclop,gocyclo // orchestration logic, splitting would obscure the linear flow
	// Step 1: Load bead.
	detail, err := deps.beadSrc.Show(ctx, cfg.beadID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitCodeBeadError)
	}
	cfg.bead = detail

	if err := cfg.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitCodeBeadError)
	}
	logStep("Loaded %s: %s", cfg.bead.ID, cfg.bead.Title)

	if cfg.dryRun {
		logStep("Dry run — would execute bead %s with model=%s, timeout=%s, skip-review=%t, resume=%t",
			cfg.beadID, cfg.model, cfg.timeout, cfg.skipReview, cfg.resume)
		return nil
	}

	// Step 2: Mark in_progress.
	_ = deps.beadSrc.Update(ctx, cfg.beadID, "in_progress")

	// Step 3: Create or resume worktree.
	worktree, branch, err := setupWorktree(ctx, cfg, deps)
	if err != nil {
		return fmt.Errorf("worktree setup: %w", err)
	}

	// Step 4-7: Execute claude + QG retry loop.
	model := cfg.model
	var feedback string
	var attempt int

	// On --resume with commits ahead, skip first claude spawn.
	skipClaude := cfg.resume && hasCommitsAhead(deps.repoRoot, branch)
	if skipClaude {
		logStep("Resuming — branch %s has commits, skipping to QG", branch)
	}

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("interrupted")
		}

		if !skipClaude {
			logStep("Spawning claude (%s, attempt %d)...", modelShort(model), attempt)
			if err := spawnAndWait(ctx, cfg, deps, worktree, model, attempt, feedback); err != nil {
				return fmt.Errorf("claude spawn: %w", err)
			}
			logStep("Claude completed")
		}
		skipClaude = false // Only skip the first iteration.

		logStep("Running quality gate...")
		passed, qgOutput, qgErr := worker.RunQualityGate(ctx, worktree)
		if qgErr != nil {
			return fmt.Errorf("quality gate error: %w", qgErr)
		}

		if passed {
			logStep("Quality gate passed")
			break
		}

		attempt++
		feedback = qgOutput
		logStep("Quality gate failed (attempt %d)", attempt)

		// Model escalation: after maxQGRetriesPerTier on sonnet, switch to opus.
		if attempt >= maxQGRetriesPerTier && model != protocol.ModelOpus {
			logStep("Escalating to opus")
			model = protocol.ModelOpus
			attempt = 0
		}
		if attempt >= maxQGRetriesPerTier {
			fmt.Fprintf(os.Stderr, "Quality gate failed %d times. Last output:\n%s\n", attempt, qgOutput)
			os.Exit(exitCodeRetries)
		}
	}

	// Step 8: Ops review.
	if !cfg.skipReview {
		if err := reviewLoop(ctx, cfg, deps, worktree, &model, &attempt, &feedback); err != nil {
			return err
		}
	} else {
		logStep("Skipping review (--skip-review)")
	}

	// Step 9: Merge to main.
	mergeResult, mergeErr := mergeToMain(ctx, cfg, deps, worktree, branch)
	if mergeErr != nil {
		fmt.Fprintf(os.Stderr, "Merge failed: %v\n", mergeErr)
		os.Exit(exitCodeMergeFail)
	}
	logStep("Merged (commit %s)", mergeResult.CommitSHA)

	// Step 10: Close bead.
	_ = deps.beadSrc.Close(ctx, cfg.beadID, fmt.Sprintf("Merged: %s", mergeResult.CommitSHA))
	logStep("Bead %s closed", cfg.beadID)

	// Step 11: Remove worktree.
	if err := deps.wtMgr.Remove(ctx, worktree); err != nil {
		logStep("Warning: worktree cleanup failed: %v", err)
	} else {
		logStep("Worktree cleaned up")
	}

	return nil
}

// setupWorktree creates a new worktree or validates an existing one for --resume.
func setupWorktree(ctx context.Context, cfg *workConfig, deps *workDeps) (wtPath, branch string, err error) {
	wtPath = filepath.Join(deps.repoRoot, ".worktrees", cfg.beadID)
	branch = protocol.BranchPrefix + cfg.beadID

	if cfg.resume {
		if _, statErr := os.Stat(wtPath); statErr != nil {
			return "", "", fmt.Errorf("--resume but worktree %s does not exist", wtPath)
		}
		logStep("Resuming worktree: %s", wtPath)
		return wtPath, branch, nil
	}

	// Collision guard.
	if _, statErr := os.Stat(wtPath); statErr == nil {
		return "", "", fmt.Errorf("worktree %s already exists — use --resume to continue, or remove it first", wtPath)
	}

	wtPath, branch, err = deps.wtMgr.Create(ctx, cfg.beadID)
	if err != nil {
		return "", "", fmt.Errorf("create worktree: %w", err)
	}
	logStep("Worktree: %s (branch %s)", wtPath, branch)
	return wtPath, branch, nil
}

// hasCommitsAhead checks if a branch has commits ahead of main.
func hasCommitsAhead(repoRoot, branch string) bool {
	runner := &merge.ExecGitRunner{}
	stdout, _, err := runner.Run(context.Background(), repoRoot, "rev-list", "--count", "main.."+branch)
	if err != nil {
		return false
	}
	return strings.TrimSpace(stdout) != "0"
}

// spawnAndWait spawns claude -p and waits for it to exit, with timeout.
func spawnAndWait(ctx context.Context, cfg *workConfig, deps *workDeps, worktree, model string, attempt int, feedback string) error {
	prompt := worker.AssemblePrompt(worker.PromptParams{
		BeadID:             cfg.beadID,
		Title:              cfg.bead.Title,
		Description:        cfg.bead.Description,
		AcceptanceCriteria: cfg.bead.AcceptanceCriteria,
		WorktreePath:       worktree,
		Model:              model,
		Attempt:            attempt,
		Feedback:           feedback,
	})

	timeoutCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	proc, stdout, _, err := deps.spawner.Spawn(timeoutCtx, model, prompt, worktree)
	if err != nil {
		return fmt.Errorf("spawn: %w", err)
	}

	// Drain stdout (echoes to stderr, extracts memories).
	if stdout != nil {
		worker.DrainOutput(ctx, stdout, nil, cfg.beadID, os.Stderr)
	}

	if err := proc.Wait(); err != nil {
		// Non-zero exit is common for claude -p; log but don't fail.
		logStep("Claude exited with: %v", err)
	}
	return nil
}

// reviewLoop runs ops review and handles rejection retries.
func reviewLoop(ctx context.Context, cfg *workConfig, deps *workDeps, worktree string, model *string, attempt *int, feedback *string) error {
	for rejects := 0; ; {
		logStep("Running ops review (opus)...")
		resultCh := deps.opsMgr.Review(ctx, ops.ReviewOpts{
			BeadID:             cfg.beadID,
			BeadTitle:          cfg.bead.Title,
			Worktree:           worktree,
			AcceptanceCriteria: cfg.bead.AcceptanceCriteria,
			BaseBranch:         "main",
			ProjectRoot:        worktree,
		})
		result := <-resultCh

		switch result.Verdict {
		case ops.VerdictApproved:
			logStep("Review: APPROVED")
			return nil

		case ops.VerdictRejected:
			rejects++
			logStep("Review REJECTED (%d/%d): %s", rejects, maxReviewRejects, truncate(result.Feedback, 200))

			if rejects >= maxReviewRejects {
				fmt.Fprintf(os.Stderr, "Review rejected %d times. Last feedback:\n%s\n", rejects, result.Feedback)
				os.Exit(exitCodeRetries)
			}

			// Re-execute with review feedback.
			*model = protocol.ModelOpus
			*attempt = rejects
			*feedback = result.Feedback

			logStep("Re-executing with review feedback (opus)...")
			if err := spawnAndWait(ctx, cfg, deps, worktree, *model, *attempt, *feedback); err != nil {
				return fmt.Errorf("claude re-spawn after review: %w", err)
			}

			// Re-run QG before next review.
			logStep("Re-running quality gate...")
			passed, qgOutput, qgErr := worker.RunQualityGate(ctx, worktree)
			if qgErr != nil {
				return fmt.Errorf("quality gate error: %w", qgErr)
			}
			if !passed {
				fmt.Fprintf(os.Stderr, "Quality gate failed after review fix:\n%s\n", qgOutput)
				os.Exit(exitCodeRetries)
			}
			logStep("Quality gate passed")

		default:
			// Review failed (timeout, etc.) — log and continue without review.
			logStep("Review failed: %s — continuing without review", result.Feedback)
			return nil
		}
	}
}

// logStep prints a status line to stderr.
func logStep(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// modelShort returns a human-friendly model name.
func modelShort(model string) string {
	switch {
	case strings.Contains(model, "opus"):
		return "opus"
	case strings.Contains(model, "sonnet"):
		return "sonnet"
	case strings.Contains(model, "haiku"):
		return "haiku"
	default:
		return model
	}
}

// mergeToMain performs the merge and handles conflict errors.
func mergeToMain(ctx context.Context, cfg *workConfig, deps *workDeps, worktree, branch string) (*merge.Result, error) {
	logStep("Merging to main...")
	result, err := deps.merger.Merge(ctx, merge.Opts{
		Branch:   branch,
		Worktree: worktree,
		BeadID:   cfg.beadID,
	})
	if err == nil {
		return result, nil
	}

	var conflictErr *merge.ConflictError
	if errors.As(err, &conflictErr) {
		return nil, fmt.Errorf("merge conflict on %s (%v) — resolve manually and re-run with --resume",
			cfg.beadID, conflictErr.Files)
	}
	return nil, fmt.Errorf("merge: %w", err)
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
