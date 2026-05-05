package main

import (
	"context"
	"database/sql"
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

	"oro/pkg/beadstore"
	"oro/pkg/codesearch"
	"oro/pkg/codestruct"
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
	beadID        string
	model         string
	timeout       time.Duration
	reviewTimeout time.Duration
	skipReview    bool
	dryRun        bool
	dryRunSpawn   bool
	auto          bool
	baseBranch    string
	bead          *protocol.Bead
}

// validate checks that the loaded bead has the required fields.
func (c *workConfig) validate() error {
	if c.bead.Title == "" {
		return fmt.Errorf("bead %s has no title", c.bead.ID)
	}
	if c.bead.AcceptanceCriteria == "" {
		return fmt.Errorf("task %s has no acceptance criteria — add with: oro task update %s --acceptance \"...\"", c.bead.ID, c.bead.ID)
	}
	return nil
}

// newWorkCmd creates the "oro work" subcommand.
func newWorkCmd() *cobra.Command {
	var cfg workConfig

	cmd := &cobra.Command{
		Use:   "work <task-id>",
		Short: "Execute a task through the full lifecycle",
		Long: `Drives a single task end-to-end: worktree → claude → quality gate →
ops review → merge → close. Runnable by a human or a claude agent.

All retries, model escalation, and review feedback loops are handled
automatically. Exit code 0 means the task landed on main.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.beadID = args[0]
			return runWork(cmd, &cfg)
		},
	}

	cmd.Flags().StringVar(&cfg.model, "model", "", "starting Claude model (opus/sonnet/haiku); empty uses task metadata")
	cmd.Flags().DurationVar(&cfg.timeout, "timeout", 15*time.Minute, "per-claude-spawn timeout")
	cmd.Flags().DurationVar(&cfg.reviewTimeout, "review-timeout", 0, "ops review process timeout override (default: ops review default)")
	cmd.Flags().BoolVar(&cfg.skipReview, "skip-review", false, "skip ops review gate")
	cmd.Flags().BoolVar(&cfg.dryRun, "dry-run", false, "show execution plan without running")
	cmd.Flags().BoolVar(&cfg.dryRunSpawn, "dry-run-spawn", false, "print the worker spawn prompt without running")
	cmd.Flags().BoolVar(&cfg.auto, "auto", false, "run non-interactively")
	cmd.Flags().StringVar(&cfg.baseBranch, "base-branch", "", "base branch for worktree (default: config default_branch, or current HEAD)")

	return cmd
}

// merger abstracts merge operations for testability.
type merger interface {
	Merge(ctx context.Context, opts merge.Opts) (*merge.Result, error)
}

// opsReviewer abstracts ops review for testability.
type opsReviewer interface {
	Review(ctx context.Context, opts ops.ReviewOpts) <-chan ops.Result
}

// workDeps holds injectable dependencies for testability.
type workDeps struct {
	beadSrc       beadstore.Store
	wtMgr         dispatcher.WorktreeManager
	spawner       worker.StreamingSpawner
	opsMgr        opsReviewer
	merger        merger
	repoRoot      string
	memStore      *memory.Store
	codeIndex     *codesearch.CodeIndex
	defaultBranch string
	hasNewWork    func(repoRoot, branch, targetBranch string) bool                                    // defaults to hasCommitsAhead
	runQG         func(ctx context.Context, worktree string, skipMutation bool) (bool, string, error) // defaults to worker.RunQualityGate
	runShellCmd   func(ctx context.Context, dir, cmd string) (bool, error)                            // defaults to defaultRunShellCmd
	stdout        io.Writer
}

func updateWorkBeadStatus(ctx context.Context, beads beadstore.Store, id, status string) error {
	if err := beads.Update(ctx, id, beadstore.UpdateParams{Status: &status}); err != nil {
		return fmt.Errorf("update bead %s status to %s: %w", id, status, err)
	}
	return nil
}

func newWorkerBeadStore(db *sql.DB, memories *memory.Store) *beadstore.SQLiteStore {
	return beadstore.NewSQLiteStore(db, beadstore.WithMemoryFetcher(func(ctx context.Context, tags []string, description string, maxTokens int) (string, error) {
		if memories == nil {
			return "", nil
		}
		return memory.ForPrompt(ctx, memories, tags, description, maxTokens)
	}))
}

// exitError carries an exit code through the normal error return path,
// allowing deferred cleanup to run (unlike os.Exit).
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// newProductionDeps creates real dependencies.
func newProductionDeps(reviewTimeout time.Duration) (*workDeps, error) {
	if err := requireNativeProductionBeadSourceMode("oro work"); err != nil {
		return nil, err
	}
	repoRoot, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getwd: %w", err)
	}
	runtime, err := resolveProductionRuntime()
	if err != nil {
		return nil, err
	}
	runner := &dispatcher.ExecCommandRunner{}

	// Initialize memory store and code index from project-scoped DB paths.
	// The native beadstore is required; memory/code index degrade gracefully.
	var beadDB *sql.DB
	var memStore *memory.Store
	var codeIdx *codesearch.CodeIndex
	paths, pathsErr := ResolveProjectDBPaths()
	if pathsErr != nil {
		return nil, fmt.Errorf("resolve project db paths: %w", pathsErr)
	}
	beadDB, dbErr := openStateDB(paths.StateDBPath)
	if dbErr != nil {
		return nil, fmt.Errorf("open beadstore db: %w", dbErr)
	}
	memStore = openWorkerMemoryStore(beadDB)
	if paths.CodeIndexDBPath != "" {
		if idx, idxErr := codesearch.NewCodeIndex(paths.CodeIndexDBPath); idxErr == nil {
			codeIdx = idx
		}
	}

	projectPaths, _ := ResolvePaths(repoRoot)

	// Load DefaultBranch from config.yaml, defaulting to "main" if not specified.
	defaultBranch := readDefaultBranch(".")
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	return &workDeps{
		beadSrc:       newWorkerBeadStore(beadDB, memStore),
		wtMgr:         dispatcher.NewGitWorktreeManager(repoRoot, "", projectPaths.QualityGate, runner),
		spawner:       runtime.workerSpawn,
		opsMgr:        ops.NewSpawnerWithReviewTimeout(runtime.opsSpawn, reviewTimeout),
		merger:        merge.NewCoordinator(&merge.ExecGitRunner{}),
		repoRoot:      repoRoot,
		memStore:      memStore,
		codeIndex:     codeIdx,
		defaultBranch: defaultBranch,
		hasNewWork:    hasCommitsAhead,
		runQG:         worker.RunQualityGate,
		runShellCmd:   defaultRunShellCmd,
		stdout:        os.Stdout,
	}, nil
}

// readDefaultBranch reads the default_branch field from .oro/config.yaml in the given directory.
// Returns empty string (no error) if the file doesn't exist.
func readDefaultBranch(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".oro", "config.yaml")) //nolint:gosec // path from trusted dir
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		return ""
	}
	// Simple line-based parsing — avoid YAML dependency for one field.
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "default_branch:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "default_branch:"))
		}
	}
	return ""
}

// runWork orchestrates the full bead lifecycle.
func runWork(_ *cobra.Command, cfg *workConfig) error {
	// Set up signal handling for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	deps, err := newProductionDeps(cfg.reviewTimeout)
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
	if cfg.dryRunSpawn {
		detail, err = dryRunSpawnBeadDetail(ctx, cfg.beadID, detail, err)
	}
	if err != nil {
		return &exitError{code: exitCodeBeadError, msg: fmt.Sprintf("error: %v", err)}
	}
	cfg.bead = detail

	if err := cfg.validate(); err != nil {
		return &exitError{code: exitCodeBeadError, msg: fmt.Sprintf("error: %v", err)}
	}
	logStep("Loaded %s: %s", cfg.bead.ID, cfg.bead.Title)

	// §11.4 retroactive premortem gate: refuse EXECUTE when the parent epic's
	// gate_state is 'eligible' and no closed premortem child exists. This
	// path bypasses the dispatcher's filterAssignable, so the gate must be
	// checked here too.
	if gateErr := dispatcher.CheckPremortemGate(ctx, deps.beadSrc, cfg.beadID); gateErr != nil {
		var pmErr *dispatcher.PremortemGateError
		if errors.As(gateErr, &pmErr) {
			return &exitError{
				code: exitCodeBeadError,
				msg:  fmt.Sprintf("blocker_hit kind=%s parent=%s: oro work refused — close the premortem first", pmErr.Kind, pmErr.ParentID),
			}
		}
		return &exitError{code: exitCodeBeadError, msg: fmt.Sprintf("premortem gate check failed: %v", gateErr)}
	}

	// Resolve model: explicit flag > bead metadata > default.
	// Empty cfg.model means no --model flag was provided, so we check bead metadata.
	// Must happen before dry-run so the resolved model is displayed.
	model := cfg.model
	if model == "" {
		if cfg.bead.Model != "" {
			model = cfg.bead.Model
		} else {
			model = protocol.DefaultModel
		}
	}

	if cfg.dryRun {
		logStep("Dry run — would execute bead %s with model=%s, timeout=%s, skip-review=%t",
			cfg.beadID, model, cfg.timeout, cfg.skipReview)
		return nil
	}
	if cfg.dryRunSpawn {
		prompt, promptErr := dryRunSpawnPrompt(cfg, deps, model)
		if promptErr != nil {
			return promptErr
		}
		out := deps.stdout
		if out == nil {
			out = os.Stdout
		}
		fmt.Fprintln(out, prompt)
		logStep("Dry-run spawn prompt printed")
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
	_ = updateWorkBeadStatus(ctx, deps.beadSrc, cfg.beadID, "in_progress")
	var merged bool
	defer func() {
		if !merged {
			// Reset bead to open so it can be re-assigned.
			// Use Background context because the parent ctx may be cancelled.
			_ = updateWorkBeadStatus(context.Background(), deps.beadSrc, cfg.beadID, "open")
		}
	}()

	// Propagate project name to subprocesses. readProjectName reads from
	// ORO_PROJECT env var first, then .oro/config.yaml in CWD. Setting it
	// ensures worker subprocesses inherit it even when it came from config.yaml.
	if project := readProjectNameCWD(); project != "" {
		_ = os.Setenv("ORO_PROJECT", project)
	}

	// Step 3: Create or resume worktree.
	// Resolve defaultBranch: --base-branch flag > config default_branch > "main"
	defaultBranch := deps.defaultBranch
	if cfg.baseBranch != "" {
		defaultBranch = cfg.baseBranch
	}
	// Resolve targetBranch by walking the parent chain: returns "epic/<id>" only when
	// an epic-type ancestor exists. Non-epic parents (tasks, features) resolve to defaultBranch.
	targetBranch, _, resolveErr := dispatcher.ResolveEpicBranch(ctx, deps.beadSrc, cfg.bead.Epic, defaultBranch)
	if resolveErr != nil {
		return fmt.Errorf("resolve epic branch: %w", resolveErr)
	}
	worktree, branch, err := setupWorktree(ctx, cfg, deps, targetBranch)
	if err != nil {
		return fmt.Errorf("worktree setup: %w", err)
	}

	var feedback string
	var attempt int

	// Auto-resume: if worktree has commits ahead of targetBranch, skip first claude spawn.
	skipClaude := deps.hasNewWork(deps.repoRoot, branch, targetBranch)
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
			if !deps.hasNewWork(deps.repoRoot, branch, targetBranch) {
				return noCommitsResult(ctx, cfg, deps, worktree, &merged)
			}
		}
		skipClaude = false // Only skip the first iteration.

		logStep("Running local quality gate (mutation deferred to push)...")
		passed, qgOutput, qgErr := deps.runQG(ctx, worktree, false)
		if qgErr != nil {
			return fmt.Errorf("quality gate error: %w", qgErr)
		}

		if passed {
			logStep("Quality gate passed (mutation deferred to push)")
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
		if err := reviewLoop(ctx, cfg, deps, worktree, targetBranch, &model, &attempt, &feedback, logFile); err != nil {
			return err
		}
	} else {
		logStep("Skipping review (--skip-review)")
	}

	// Step 9: Pre-merge quality gate. Mutation testing is deferred to push.
	logStep("Running pre-merge quality gate (mutation deferred to push)...")
	mutPassed, mutOutput, mutErr := deps.runQG(ctx, worktree, false)
	if mutErr != nil {
		return fmt.Errorf("pre-merge quality gate error: %w", mutErr)
	}
	if !mutPassed {
		return &exitError{
			code: exitCodeRetries,
			msg:  fmt.Sprintf("Pre-merge quality gate failed:\n%s", mutOutput),
		}
	}
	logStep("Pre-merge quality gate passed")

	// Step 10: Merge to main.
	mergeResult, mergeErr := mergeToMain(ctx, cfg, deps, worktree, branch, targetBranch)
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

	// Step 12: Delete branch (best-effort).
	branchName := protocol.BranchPrefix + cfg.beadID
	if err := deps.wtMgr.DeleteBranch(ctx, branchName); err != nil {
		logStep("Warning: branch cleanup failed: %v", err)
	}

	return nil
}

func dryRunSpawnBeadDetail(_ context.Context, beadID string, detail *protocol.Bead, showErr error) (*protocol.Bead, error) {
	if showErr == nil && detail != nil && detail.ID != "" {
		return detail, nil
	}
	if showErr != nil {
		return nil, showErr
	}
	if detail == nil {
		return nil, fmt.Errorf("bead %s not found", beadID)
	}
	return detail, nil
}

func dryRunSpawnPrompt(cfg *workConfig, deps *workDeps, model string) (string, error) {
	projPaths, err := ResolvePaths(deps.repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve paths: %w", err)
	}
	worktree := filepath.Join(projPaths.WorktreesDir, cfg.beadID)

	return worker.AssemblePrompt(worker.PromptParams{
		BeadID:             cfg.beadID,
		Title:              cfg.bead.Title,
		Description:        cfg.bead.Description,
		AcceptanceCriteria: cfg.bead.AcceptanceCriteria,
		WorktreePath:       worktree,
		Model:              model,
		ProjectRoot:        deps.repoRoot,
	}), nil
}

// setupWorktree auto-detects worktree state:
//   - exists → resume from it
//   - doesn't exist → create new, branching from baseBranch
//
// baseBranch is the resolved target branch for this bead (e.g. "main" or
// "epic/<id>"), computed by the caller via dispatcher.ResolveEpicBranch.
func setupWorktree(ctx context.Context, cfg *workConfig, deps *workDeps, baseBranch string) (wtPath, branch string, err error) {
	projPaths, err := ResolvePaths(deps.repoRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve paths: %w", err)
	}
	wtPath = filepath.Join(projPaths.WorktreesDir, cfg.beadID)
	branch = protocol.BranchPrefix + cfg.beadID

	if _, statErr := os.Stat(wtPath); statErr == nil {
		logStep("Resuming worktree: %s", wtPath)
		return wtPath, branch, nil
	}

	wtPath, branch, err = deps.wtMgr.Create(ctx, cfg.beadID, baseBranch)
	if err != nil {
		return "", "", fmt.Errorf("create worktree: %w", err)
	}
	logStep("Worktree: %s (branch %s)", wtPath, branch)
	return wtPath, branch, nil
}

// hasCommitsAhead checks if a branch has commits ahead of targetBranch.
func hasCommitsAhead(repoRoot, branch, targetBranch string) bool {
	runner := &merge.ExecGitRunner{}
	stdout, _, err := runner.Run(context.Background(), repoRoot, "rev-list", "--count", targetBranch+".."+branch)
	if err != nil {
		return false
	}
	return strings.TrimSpace(stdout) != "0"
}

// codeSearchContext runs a timed FTS5 search against the code index and
// formats the top results for prompt injection. Returns empty string on
// error or timeout (5 s) — errors are non-fatal so the worker always runs.
func codeSearchContext(ctx context.Context, idx *codesearch.CodeIndex, query, worktree string) string {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	results, _ := idx.SearchInWorkdir(sctx, query, 5, worktree)
	return codesearch.FormatResults(results)
}

// codeStructureContext builds Code Structure nav-maps for Go source files
// referenced in the acceptance criteria (Test: <path>:<Func> pattern).
// Returns empty string if no files are found or all extractions fail.
func codeStructureContext(worktree, acceptanceCriteria string) string {
	path := parseTestFilePath(acceptanceCriteria)
	if path == "" || !strings.HasSuffix(path, ".go") {
		return ""
	}
	abs := filepath.Join(worktree, path)
	src, err := os.ReadFile(abs) //nolint:gosec // path comes from bead acceptance criteria, within worktree
	if err != nil {
		return ""
	}
	syms, err := codestruct.ExtractGoSymbols(abs)
	if err != nil {
		return ""
	}
	totalLines := strings.Count(string(src), "\n") + 1
	return worker.FormatNavMap(abs, totalLines, syms)
}

// parseTestFilePath extracts the file path from "Test: path:Func" in the
// acceptance criteria string. Returns empty string if not found.
func parseTestFilePath(ac string) string {
	idx := strings.Index(ac, "Test: ")
	if idx < 0 {
		return ""
	}
	rest := ac[idx+len("Test: "):]
	if pipeIdx := strings.Index(rest, " | "); pipeIdx >= 0 {
		rest = rest[:pipeIdx]
	}
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0])
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
		memCtx, _ = memory.ForPrompt(ctx, deps.memStore, nil, buildSearchQuery(cfg.bead.Title, cfg.bead.Labels), 0)
	}

	var codeCtx string
	if deps.codeIndex != nil {
		codeCtx = codeSearchContext(ctx, deps.codeIndex, cfg.bead.Title, worktree)
	}

	codeStructCtx := codeStructureContext(worktree, cfg.bead.AcceptanceCriteria)

	prompt := worker.AssemblePrompt(worker.PromptParams{
		BeadID:               cfg.beadID,
		Title:                cfg.bead.Title,
		Description:          cfg.bead.Description,
		AcceptanceCriteria:   cfg.bead.AcceptanceCriteria,
		MemoryContext:        memCtx,
		CodeSearchContext:    codeCtx,
		CodeStructureContext: codeStructCtx,
		WorktreePath:         worktree,
		Model:                model,
		Attempt:              attempt,
		Feedback:             feedback,
		ProjectRoot:          projectRoot,
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
		worker.DrainOutputInWorkdir(ctx, stdout, deps.spawner.StreamFormat(), memInserter, cfg.beadID, &memory.CLISpawner{}, worktree, writers...)
	}

	if err := proc.Wait(); err != nil {
		// Non-zero exit is common for claude -p; log but don't fail.
		logStep("Claude exited with: %v", err)
	}
	return nil
}

// reviewLoop runs ops review and handles rejection retries.
// targetBranch is the branch the worker merges into (epic branch or "main").
func reviewLoop(ctx context.Context, cfg *workConfig, deps *workDeps, worktree, targetBranch string, model *string, attempt *int, feedback *string, logFile *os.File) error {
	projPaths, err := ResolvePaths(worktree)
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}

	for rejects := 0; ; {
		logStep("Running ops review (opus)...")
		resultCh := deps.opsMgr.Review(ctx, ops.ReviewOpts{
			BeadID:             cfg.beadID,
			BeadTitle:          cfg.bead.Title,
			Worktree:           worktree,
			AcceptanceCriteria: cfg.bead.AcceptanceCriteria,
			BaseBranch:         targetBranch,
			ProjectRoot:        worktree,
			ReviewPatterns:     projPaths.ReviewPatterns,
		})
		result, err := waitForReviewResult(ctx, resultCh)
		if err != nil {
			return err
		}

		switch result.Verdict {
		case ops.VerdictApproved:
			logStep("Review: APPROVED")
			return nil

		case ops.VerdictRejected:
			nextRejects, err := handleReviewRejection(ctx, cfg, deps, worktree, result, rejects, model, attempt, feedback, logFile)
			if err != nil {
				return err
			}
			rejects = nextRejects

		default:
			return reviewFailure(result)
		}
	}
}

func waitForReviewResult(ctx context.Context, resultCh <-chan ops.Result) (ops.Result, error) {
	select {
	case <-ctx.Done():
		return ops.Result{}, fmt.Errorf("review interrupted: %w", ctx.Err())
	case result, ok := <-resultCh:
		if !ok {
			return ops.Result{}, &exitError{
				code: exitCodeRetries,
				msg:  "Review failed without returning a verdict",
			}
		}
		return result, nil
	}
}

func handleReviewRejection(ctx context.Context, cfg *workConfig, deps *workDeps, worktree string, result ops.Result, rejects int, model *string, attempt *int, feedback *string, logFile *os.File) (int, error) {
	rejects++
	logStep("Review REJECTED (%d/%d): %s", rejects, maxReviewRejects, truncate(result.Feedback, 200))

	if rejects >= maxReviewRejects {
		return rejects, &exitError{
			code: exitCodeRetries,
			msg:  fmt.Sprintf("Review rejected %d times. Last feedback:\n%s", rejects, result.Feedback),
		}
	}

	*model = protocol.ModelOpus
	*attempt = rejects
	*feedback = result.Feedback

	logStep("Re-executing with review feedback (opus)...")
	if err := spawnAndWait(ctx, cfg, deps, worktree, *model, *attempt, *feedback, logFile); err != nil {
		return rejects, fmt.Errorf("claude re-spawn after review: %w", err)
	}

	logStep("Re-running local quality gate (mutation deferred to push)...")
	passed, qgOutput, qgErr := deps.runQG(ctx, worktree, false)
	if qgErr != nil {
		return rejects, fmt.Errorf("quality gate error: %w", qgErr)
	}
	if !passed {
		return rejects, &exitError{
			code: exitCodeRetries,
			msg:  fmt.Sprintf("Quality gate failed after review fix:\n%s", qgOutput),
		}
	}
	logStep("Quality gate passed")
	return rejects, nil
}

func reviewFailure(result ops.Result) error {
	msg := result.Feedback
	if msg == "" {
		msg = fmt.Sprintf("missing or unsupported verdict %q", result.Verdict)
	}
	if result.Err != nil {
		msg = fmt.Sprintf("%s: %v", msg, result.Err)
	}
	logStep("Review failed: %s", msg)
	return &exitError{
		code: exitCodeRetries,
		msg:  fmt.Sprintf("Review failed without approval:\n%s", msg),
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
	fmt.Fprintf(logOut, format+"\n", args...) //nolint:gosec // logStep is only called with internal format strings.
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
// targetBranch is the branch to merge into (epic branch or "main").
func mergeToMain(ctx context.Context, cfg *workConfig, deps *workDeps, worktree, branch, targetBranch string) (*merge.Result, error) {
	logStep("Merging to main...")
	result, err := deps.merger.Merge(ctx, merge.Opts{
		Branch:       branch,
		Worktree:     worktree,
		BeadID:       cfg.beadID,
		TargetBranch: targetBranch,
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

// buildSearchQuery combines a bead title and labels into a single search string.
// Labels are appended after the title, separated by spaces.
// Empty labels are ignored. If title is empty, only labels are joined.
func buildSearchQuery(title string, labels []string) string {
	parts := make([]string, 0, 1+len(labels))
	if title != "" {
		parts = append(parts, title)
	}
	parts = append(parts, labels...)
	return strings.Join(parts, " ")
}
