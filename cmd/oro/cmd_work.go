package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"oro/pkg/codesearch"
	"oro/pkg/dispatcher"
	"oro/pkg/langprofile"
	"oro/pkg/memory"
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
	cmd.Flags().BoolVar(&cfg.dryRun, "dry-run", false, "show execution plan without running")

	return cmd
}

// merger abstracts merge operations for testability.
type merger interface {
	Merge(ctx context.Context, opts merge.Opts) (*merge.Result, error)
}

// workDeps holds injectable dependencies for testability.
type workDeps struct {
	beadSrc     dispatcher.BeadSource
	wtMgr       dispatcher.WorktreeManager
	spawner     worker.StreamingSpawner
	opsMgr      *ops.Spawner
	merger      merger
	repoRoot    string
	memStore    *memory.Store
	codeIndex   *codesearch.CodeIndex
	hasNewWork  func(repoRoot, branch string) bool                                                  // defaults to hasCommitsAhead
	runQG       func(ctx context.Context, worktree string, skipMutation bool) (bool, string, error) // defaults to worker.RunQualityGate
	runShellCmd func(ctx context.Context, dir, cmd string) (bool, error)                            // defaults to defaultRunShellCmd
}

// exitError carries an exit code through the normal error return path,
// allowing deferred cleanup to run (unlike os.Exit).
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// newProductionDeps creates real dependencies.
func newProductionDeps() (*workDeps, error) {
	repoRoot, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getwd: %w", err)
	}
	runner := &dispatcher.ExecCommandRunner{}

	// Initialize memory store and code index from project-scoped DB paths.
	// Both are nil on failure — errors are non-fatal, the worker degrades gracefully.
	var memStore *memory.Store
	var codeIdx *codesearch.CodeIndex
	if paths, pathsErr := ResolveProjectDBPaths(); pathsErr == nil {
		if db, dbErr := openStateDB(paths.StateDBPath); dbErr == nil {
			memStore = openWorkerMemoryStore(db)
		}
		if idx, idxErr := codesearch.NewCodeIndex(paths.CodeIndexDBPath); idxErr == nil {
			codeIdx = idx
		}
	}

	return &workDeps{
		beadSrc:     dispatcher.NewCLIBeadSource(runner),
		wtMgr:       dispatcher.NewGitWorktreeManager(repoRoot, runner),
		spawner:     &worker.ClaudeSpawner{},
		opsMgr:      ops.NewSpawner(&ops.ClaudeOpsSpawner{}),
		merger:      merge.NewCoordinator(&merge.ExecGitRunner{}),
		repoRoot:    repoRoot,
		memStore:    memStore,
		codeIndex:   codeIdx,
		hasNewWork:  hasCommitsAhead,
		runQG:       worker.RunQualityGate,
		runShellCmd: defaultRunShellCmd,
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

	err = executeWork(ctx, cfg, deps)
	var ee *exitError
	if errors.As(err, &ee) {
		stop() // release signal handler before exit
		fmt.Fprintf(os.Stderr, "%s\n", ee.msg)
		os.Exit(ee.code) //nolint:gocritic // stop() called above; defer is backup only
	}
	return err
}

// executeWork is the testable core of the work command.
func executeWork(ctx context.Context, cfg *workConfig, deps *workDeps) error { //nolint:funlen,gocognit,cyclop,gocyclo // orchestration logic, splitting would obscure the linear flow
	// Persist embedder vocabulary on exit so future sessions start with the
	// same vector space. Mirrors the SaveVocab call in cmd_worker.go:runWorker.
	if deps.memStore != nil {
		defer func() { _ = deps.memStore.SaveVocab(context.Background()) }()
	}

	// Step 1: Load bead.
	detail, err := deps.beadSrc.Show(ctx, cfg.beadID)
	if err != nil {
		return &exitError{code: exitCodeBeadError, msg: fmt.Sprintf("error: %v", err)}
	}
	cfg.bead = detail

	if err := cfg.validate(); err != nil {
		return &exitError{code: exitCodeBeadError, msg: fmt.Sprintf("error: %v", err)}
	}
	logStep("Loaded %s: %s", cfg.bead.ID, cfg.bead.Title)

	if cfg.dryRun {
		logStep("Dry run — would execute bead %s with model=%s, timeout=%s, skip-review=%t",
			cfg.beadID, cfg.model, cfg.timeout, cfg.skipReview)
		return nil
	}

	// Open per-bead log file for observability.
	logFile, logFileErr := openBeadLog(cfg.beadID)
	if logFileErr != nil {
		logStep("Warning: %v", logFileErr)
	}
	if logFile != nil {
		defer logFile.Close()
		logOut = io.MultiWriter(os.Stderr, logFile)
		defer func() { logOut = os.Stderr }()
	}

	// Step 2: Mark in_progress and set up deferred bead reset.
	_ = deps.beadSrc.Update(ctx, cfg.beadID, "in_progress")
	var merged bool
	defer func() {
		if !merged {
			// Reset bead to open so it can be re-assigned.
			// Use Background context because the parent ctx may be cancelled.
			_ = deps.beadSrc.Update(context.Background(), cfg.beadID, "open")
		}
	}()

	// Propagate project name to subprocesses. readProjectName reads from
	// ORO_PROJECT env var first, then .oro/config.yaml in CWD. Setting it
	// ensures worker subprocesses inherit it even when it came from config.yaml.
	if project := readProjectName(); project != "" {
		_ = os.Setenv("ORO_PROJECT", project)
	}

	// Step 3: Create or resume worktree.
	worktree, branch, err := setupWorktree(ctx, cfg, deps)
	if err != nil {
		return fmt.Errorf("worktree setup: %w", err)
	}

	// Step 4-7: Execute claude + QG retry loop.
	model := cfg.model
	var feedback string
	var attempt int

	// Auto-resume: if worktree has commits ahead, skip first claude spawn.
	skipClaude := deps.hasNewWork(deps.repoRoot, branch)
	if skipClaude {
		logStep("Resuming — branch %s has commits, skipping to QG", branch)
	}

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("interrupted")
		}

		if !skipClaude {
			logStep("--- attempt %d (%s) ---", attempt, modelShort(model))
			logStep("Spawning claude (%s, attempt %d)...", modelShort(model), attempt)
			if err := spawnAndWait(ctx, cfg, deps, worktree, model, attempt, feedback, logFile); err != nil {
				return fmt.Errorf("claude spawn: %w", err)
			}
			logStep("Claude completed")

			// Guard: bail out if claude produced no commits.
			if !deps.hasNewWork(deps.repoRoot, branch) {
				return noCommitsResult(ctx, cfg, deps, worktree, &merged)
			}
		}
		skipClaude = false // Only skip the first iteration.

		logStep("Running quality gate (skip mutation)...")
		passed, qgOutput, qgErr := deps.runQG(ctx, worktree, true)
		if qgErr != nil {
			return fmt.Errorf("quality gate error: %w", qgErr)
		}

		if passed {
			logStep("Quality gate passed (mutation deferred to pre-merge)")
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
			return &exitError{
				code: exitCodeRetries,
				msg:  fmt.Sprintf("Quality gate failed %d times. Last output:\n%s", attempt, qgOutput),
			}
		}
	}

	// Step 8: Ops review.
	if !cfg.skipReview {
		if err := reviewLoop(ctx, cfg, deps, worktree, &model, &attempt, &feedback, logFile); err != nil {
			return err
		}
	} else {
		logStep("Skipping review (--skip-review)")
	}

	// Step 9: Pre-merge mutation testing.
	logStep("Running mutation testing...")
	mutPassed, mutOutput, mutErr := deps.runQG(ctx, worktree, false)
	if mutErr != nil {
		return fmt.Errorf("pre-merge quality gate error: %w", mutErr)
	}
	if !mutPassed {
		return &exitError{
			code: exitCodeRetries,
			msg:  fmt.Sprintf("Pre-merge quality gate (mutation) failed:\n%s", mutOutput),
		}
	}
	logStep("Mutation testing passed")

	// Step 10: Merge to main.
	mergeResult, mergeErr := mergeToMain(ctx, cfg, deps, worktree, branch)
	if mergeErr != nil {
		return &exitError{
			code: exitCodeMergeFail,
			msg:  fmt.Sprintf("Merge failed: %v", mergeErr),
		}
	}
	merged = true
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

// setupWorktree auto-detects worktree state:
//   - exists → resume from it
//   - doesn't exist → create new
func setupWorktree(ctx context.Context, cfg *workConfig, deps *workDeps) (wtPath, branch string, err error) {
	wtPath = filepath.Join(deps.repoRoot, ".worktrees", cfg.beadID)
	branch = protocol.BranchPrefix + cfg.beadID

	if _, statErr := os.Stat(wtPath); statErr == nil {
		logStep("Resuming worktree: %s", wtPath)
		return wtPath, branch, nil
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

// codeSearchContext runs a timed FTS5 search against the code index and
// formats the top results for prompt injection. Returns empty string on
// error or timeout (5 s) — errors are non-fatal so the worker always runs.
func codeSearchContext(ctx context.Context, idx *codesearch.CodeIndex, query string) string {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	results, _ := idx.Search(sctx, query, 5)
	return codesearch.FormatResults(results)
}

// spawnAndWait spawns claude -p and waits for it to exit, with timeout.
// logFile, when non-nil, receives a copy of Claude's stdout alongside stderr.
func spawnAndWait(ctx context.Context, cfg *workConfig, deps *workDeps, worktree, model string, attempt int, feedback string, logFile *os.File) error {
	// Resolve project root from worktree path
	projectRoot := ""
	if resolved, err := langprofile.ResolveProjectRoot(worktree); err == nil {
		projectRoot = resolved
	}

	var memCtx string
	if deps.memStore != nil {
		memCtx, _ = memory.ForPrompt(ctx, deps.memStore, nil, cfg.bead.Title, 0)
	}

	var codeCtx string
	if deps.codeIndex != nil {
		codeCtx = codeSearchContext(ctx, deps.codeIndex, cfg.bead.Title)
	}

	prompt := worker.AssemblePrompt(worker.PromptParams{
		BeadID:             cfg.beadID,
		Title:              cfg.bead.Title,
		Description:        cfg.bead.Description,
		AcceptanceCriteria: cfg.bead.AcceptanceCriteria,
		MemoryContext:      memCtx,
		CodeSearchContext:  codeCtx,
		WorktreePath:       worktree,
		Model:              model,
		Attempt:            attempt,
		Feedback:           feedback,
		ProjectRoot:        projectRoot,
	})

	timeoutCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	proc, stdout, _, err := deps.spawner.Spawn(timeoutCtx, model, prompt, worktree)
	if err != nil {
		return fmt.Errorf("spawn: %w", err)
	}

	// Drain stdout (echoes to stderr + optional log file, extracts memories).
	if stdout != nil {
		writers := []io.Writer{os.Stderr}
		if logFile != nil {
			writers = append(writers, logFile)
		}
		var memInserter worker.MemoryInserter
		if deps.memStore != nil {
			memInserter = deps.memStore
		}
		worker.DrainOutput(ctx, stdout, memInserter, cfg.beadID, &memory.CLISpawner{}, writers...)
	}

	if err := proc.Wait(); err != nil {
		// Non-zero exit is common for claude -p; log but don't fail.
		logStep("Claude exited with: %v", err)
	}
	return nil
}

// reviewLoop runs ops review and handles rejection retries.
func reviewLoop(ctx context.Context, cfg *workConfig, deps *workDeps, worktree string, model *string, attempt *int, feedback *string, logFile *os.File) error {
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
				return &exitError{
					code: exitCodeRetries,
					msg:  fmt.Sprintf("Review rejected %d times. Last feedback:\n%s", rejects, result.Feedback),
				}
			}

			// Re-execute with review feedback.
			*model = protocol.ModelOpus
			*attempt = rejects
			*feedback = result.Feedback

			logStep("Re-executing with review feedback (opus)...")
			if err := spawnAndWait(ctx, cfg, deps, worktree, *model, *attempt, *feedback, logFile); err != nil {
				return fmt.Errorf("claude re-spawn after review: %w", err)
			}

			// Re-run QG before next review (skip mutation — deferred to pre-merge).
			logStep("Re-running quality gate (skip mutation)...")
			passed, qgOutput, qgErr := deps.runQG(ctx, worktree, true)
			if qgErr != nil {
				return fmt.Errorf("quality gate error: %w", qgErr)
			}
			if !passed {
				return &exitError{
					code: exitCodeRetries,
					msg:  fmt.Sprintf("Quality gate failed after review fix:\n%s", qgOutput),
				}
			}
			logStep("Quality gate passed")

		default:
			// Review failed (timeout, etc.) — log and continue without review.
			logStep("Review failed: %s — continuing without review", result.Feedback)
			return nil
		}
	}
}

// openBeadLog creates the per-bead log directory and opens the output.log file.
// Returns (nil, err) on failure — callers should warn but not abort.
func openBeadLog(beadID string) (*os.File, error) {
	oroHome, err := resolveOroHome()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve oro home for log file: %w", err)
	}
	logDir := filepath.Join(oroHome, "workers", "work-"+beadID)
	if err := os.MkdirAll(logDir, 0o750); err != nil { //nolint:gosec // user-private log dir
		return nil, fmt.Errorf("cannot create log dir %s: %w", logDir, err)
	}
	logPath := filepath.Join(logDir, "output.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644) //nolint:gosec // path from resolveOroHome, not user input
	if err != nil {
		return nil, fmt.Errorf("cannot open log file %s: %w", logPath, err)
	}
	return f, nil
}

// logOut is the destination for logStep output. Default is os.Stderr.
// executeWork sets it to io.MultiWriter(os.Stderr, logFile) when a log file is available.
var logOut io.Writer = os.Stderr //nolint:gochecknoglobals // package-level writer for logStep fan-out

// logStep prints a status line to logOut (stderr + optional log file).
func logStep(format string, args ...any) {
	fmt.Fprintf(logOut, format+"\n", args...)
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
		return nil, fmt.Errorf("merge conflict on %s (%v) — resolve manually and re-run",
			cfg.beadID, conflictErr.Files)
	}
	return nil, fmt.Errorf("merge: %w", err)
}

// noCommitsResult handles the case where claude exits without producing commits.
// If the bead has structured acceptance criteria (Test: + Cmd: fields) and the
// specific AC test file exists and the AC command passes, the code is already on
// main — close the bead. Otherwise return an error.
func noCommitsResult(ctx context.Context, cfg *workConfig, deps *workDeps, worktree string, merged *bool) error {
	if acAlreadySatisfied(ctx, cfg, deps, worktree) {
		logStep("AC already satisfied — closing bead (code already on main)")
		*merged = true
		_ = deps.beadSrc.Close(ctx, cfg.beadID, "AC already satisfied — code already on main")
		_ = deps.wtMgr.Remove(ctx, worktree)
		return nil
	}

	logStep("No commits on branch — claude produced no work")
	_ = deps.wtMgr.Remove(ctx, worktree)
	return fmt.Errorf("claude exited without producing commits on bead %s", cfg.beadID)
}

// acAlreadySatisfied checks if the bead's structured acceptance criteria
// (Test: + Cmd: fields) are already satisfied on the unmodified worktree.
// Returns false if AC is unparseable, test file is missing, or command fails.
func acAlreadySatisfied(ctx context.Context, cfg *workConfig, deps *workDeps, worktree string) bool {
	if cfg.bead == nil || deps.runShellCmd == nil {
		return false
	}
	testFile, hasFile := parseACTestFile(cfg.bead.AcceptanceCriteria)
	cmd, hasCmd := parseACCmd(cfg.bead.AcceptanceCriteria)
	if !hasFile || !hasCmd {
		return false
	}
	if _, err := os.Stat(filepath.Join(worktree, testFile)); err != nil {
		return false
	}
	passed, err := deps.runShellCmd(ctx, worktree, cmd)
	return err == nil && passed
}

// acCmdRe matches "Cmd: <command>" in acceptance criteria, delimited by " | " or newline.
var acCmdRe = regexp.MustCompile(`(?m)(?:^|[|\n]\s*)Cmd:\s*(.+?)(?:\s*\||\s*$)`)

// parseACCmd extracts the Cmd field from structured acceptance criteria.
func parseACCmd(ac string) (string, bool) {
	m := acCmdRe.FindStringSubmatch(ac)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// acTestFileRe matches "Test: <filepath>[:FnName]" in acceptance criteria.
var acTestFileRe = regexp.MustCompile(`(?:^|[|\n]\s*)Test:\s*(\S+?)(?::\S+)?(?:\s*\||\s*$)`)

// parseACTestFile extracts the test file path from structured acceptance criteria.
func parseACTestFile(ac string) (string, bool) {
	m := acTestFileRe.FindStringSubmatch(ac)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// defaultRunShellCmd runs a shell command in a directory and returns whether it exited 0.
// The cmd argument comes from bead acceptance criteria (trusted internal data).
func defaultRunShellCmd(ctx context.Context, dir, cmd string) (bool, error) {
	c := exec.CommandContext(ctx, "bash", "-c", cmd) //nolint:gosec // cmd from trusted AC field
	c.Dir = dir
	if err := c.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("run AC command: %w", err)
	}
	return true, nil
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
