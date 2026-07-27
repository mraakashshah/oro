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

	"oro/pkg/agentmodel"
	"oro/pkg/beadstore"
	"oro/pkg/cards"
	"oro/pkg/codesearch"
	"oro/pkg/codestruct"
	"oro/pkg/config"
	"oro/pkg/dispatcher"
	embeddings "oro/pkg/embed"
	"oro/pkg/langprofile"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
	"oro/pkg/storage"
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
	beadID            string
	model             string
	runtime           string
	timeout           time.Duration
	reviewTimeout     time.Duration
	skipReview        bool
	dryRun            bool
	dryRunSpawn       bool
	auto              bool
	baseBranch        string
	mutationTesting   bool
	bead              *protocol.Bead
	storageController *storage.Controller
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

// observeStandaloneStorageController refreshes standalone work admission before
// it starts an Oro-owned subprocess. A nil controller preserves existing work
// behavior.
func observeStandaloneStorageController(ctx context.Context, controller *storage.Controller) error {
	if controller == nil {
		return nil
	}
	if err := controller.Observe(ctx, time.Now().UTC()); err != nil {
		return fmt.Errorf("observe standalone storage controller: %w", err)
	}
	if !controller.Admit() {
		return errors.New("standalone storage admission paused")
	}
	return nil
}

func workMutationMode(cfg *workConfig) string {
	if cfg != nil && cfg.mutationTesting {
		return "mutation testing enabled"
	}
	return "mutation testing disabled"
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

	cmd.Flags().StringVar(&cfg.model, "model", "", "routing tier (fast/balanced/deep/background) or provider-native model override; empty uses task metadata")
	cmd.Flags().StringVar(&cfg.runtime, "runtime", "", "force worker runtime (claude or codex); empty uses agent model resolution")
	cmd.Flags().DurationVar(&cfg.timeout, "timeout", 15*time.Minute, "per-claude-spawn timeout")
	cmd.Flags().DurationVar(&cfg.reviewTimeout, "review-timeout", 0, "ops review process timeout override (default: ops review default)")
	cmd.Flags().BoolVar(&cfg.skipReview, "skip-review", false, "skip ops review gate")
	cmd.Flags().BoolVar(&cfg.dryRun, "dry-run", false, "show execution plan without running")
	cmd.Flags().BoolVar(&cfg.dryRunSpawn, "dry-run-spawn", false, "print the worker spawn prompt without running")
	cmd.Flags().BoolVar(&cfg.auto, "auto", false, "run non-interactively")
	cmd.Flags().StringVar(&cfg.baseBranch, "base-branch", "", "base branch for worktree (default: config default_branch, or current HEAD)")
	cmd.Flags().BoolVar(&cfg.mutationTesting, "mutation-testing", false, "run mutation-testing tiers in quality gates (off by default)")

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

type workMemoryStore interface {
	worker.LearningSink
	SaveVocab(context.Context) error
}

// workDeps holds injectable dependencies for testability.
type workDeps struct {
	beadSrc         beadstore.Store
	wtMgr           dispatcher.WorktreeManager
	spawner         worker.StreamingSpawner
	runtimeSpawner  worker.RuntimeStreamingSpawner
	opsMgr          opsReviewer
	merger          merger
	repoRoot        string
	memStore        workMemoryStore
	codeIndex       *codesearch.CodeIndex
	defaultBranch   string
	hasNewWork      func(repoRoot, branch, targetBranch string) bool                                    // defaults to hasCommitsAhead
	runQG           func(ctx context.Context, worktree string, skipMutation bool) (bool, string, error) // defaults to worker.RunQualityGate
	runShellCmd     func(ctx context.Context, dir, cmd string) (bool, error)                            // defaults to defaultRunShellCmd
	worktreeDirty   func(ctx context.Context, worktree string) (bool, string, error)                    // defaults to worktreeHasUncommittedChanges
	recordQGFailure func(ctx context.Context, rec dispatcher.QGFailureRecord, cls dispatcher.QGFailureClassification) error
	stdout          io.Writer
	cardStore       cards.Store
	storagePolicy   config.StoragePolicy
}

type standaloneBaseBranchPreparer interface {
	PrepareBaseBranchForAssignment(ctx context.Context, branch, baseBranch string) (fastForwarded bool, err error)
}

type standaloneBaseBranchSafetyChecker interface {
	BaseBranchHasUniqueCommits(ctx context.Context, branch, baseBranch string) (bool, error)
}

type standaloneExistingWorktreePreparer interface {
	PrepareExistingForReuse(ctx context.Context, worktree, branch, baseBranch string) (fastForwarded bool, err error)
}

func updateWorkBeadStatus(ctx context.Context, beads beadstore.Store, id, status string) error {
	if err := beads.Update(ctx, id, beadstore.UpdateParams{Status: &status}); err != nil {
		return fmt.Errorf("update bead %s status to %s: %w", id, status, err)
	}
	return nil
}

func newWorkerBeadStore(db *sql.DB, _ workMemoryStore) *beadstore.SQLiteStore {
	return beadstore.NewSQLiteStore(db)
}

func openWorkerCardStore(db *sql.DB) cards.Store {
	embedder, err := embeddings.NewEmbedder("")
	if err != nil {
		logStep("cards embedder unavailable for work prompts: %v", err)
	}
	opts := []cards.StoreOption{}
	if embedder != nil {
		opts = append(opts, cards.WithEmbedder(embedder))
	}
	store, err := cards.NewStore(db, opts...)
	if err != nil {
		logStep("cards store unavailable for work prompts: %v", err)
		return nil
	}
	return store
}

// exitError carries an exit code through the normal error return path,
// allowing deferred cleanup to run (unlike os.Exit).
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// newStateDBQGFailureRecorder returns a recorder that persists QG failure
// incidents and occurrences to the state DB.
func newStateDBQGFailureRecorder(db *sql.DB) func(context.Context, dispatcher.QGFailureRecord, dispatcher.QGFailureClassification) error {
	return func(ctx context.Context, rec dispatcher.QGFailureRecord, cls dispatcher.QGFailureClassification) error {
		if _, err := dispatcher.RecordQGFailureOccurrence(ctx, db, rec, cls); err != nil {
			return fmt.Errorf("record qg failure occurrence: %w", err)
		}
		return nil
	}
}

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
	var memStore workMemoryStore
	var cardStore cards.Store
	var codeIdx *codesearch.CodeIndex
	paths, pathsErr := ResolveProjectDBPaths()
	if pathsErr != nil {
		return nil, fmt.Errorf("resolve project db paths: %w", pathsErr)
	}
	beadDB, dbErr := openStateDBWithV4Migration(paths.StateDBPath)
	if dbErr != nil {
		return nil, fmt.Errorf("open beadstore db: %w", dbErr)
	}
	memStore = openWorkerMemoryStore(beadDB)
	cardStore = openWorkerCardStore(beadDB)
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
		beadSrc:         newWorkerBeadStore(beadDB, memStore),
		wtMgr:           dispatcher.NewGitWorktreeManager(repoRoot, "", projectPaths.QualityGate, runner),
		spawner:         runtime.workerSpawn,
		runtimeSpawner:  workerSpawnerForRuntime(),
		opsMgr:          newProductionOpsManager(runtime, reviewTimeout),
		merger:          merge.NewCoordinator(&merge.ExecGitRunner{}),
		repoRoot:        repoRoot,
		memStore:        memStore,
		cardStore:       cardStore,
		codeIndex:       codeIdx,
		defaultBranch:   defaultBranch,
		hasNewWork:      hasCommitsAhead,
		runQG:           worker.RunQualityGate,
		runShellCmd:     defaultRunShellCmd,
		worktreeDirty:   worktreeHasUncommittedChanges,
		recordQGFailure: newStateDBQGFailureRecorder(beadDB),
		stdout:          os.Stdout,
	}, nil
}

func newProductionOpsManager(runtime *productionRuntime, reviewTimeout time.Duration) *ops.Spawner {
	opsMgr := ops.NewSpawnerWithReviewTimeout(runtime.opsSpawn, reviewTimeout)
	opsMgr.SetReviewSpawner(runtime.reviewOpsSpawn)
	return opsMgr
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
	storagePolicy, err := config.LoadStoragePolicy(ctx, config.StoragePolicySources{
		ProjectConfigPath: filepath.Join(currentRepoRoot(), ".oro", "config.yaml"),
	})
	if err != nil {
		return fmt.Errorf("load storage policy: %w", err)
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	if _, err := ensureRuntimeProjectEnv(repoRoot); err != nil {
		return fmt.Errorf("resolve runtime project environment: %w", err)
	}

	deps, err := newProductionDeps(cfg.reviewTimeout)
	if err != nil {
		return err
	}
	deps.storagePolicy = storagePolicy

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
	if _, err := ensureRuntimeProjectEnv(deps.repoRoot); err != nil {
		return fmt.Errorf("resolve runtime project environment: %w", err)
	}

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

	// Apply --model flag: parse into (tier, providerModel) and update bead fields.
	// Tier names and legacy shortnames (opus/sonnet/haiku) set Bead.Tier.
	// Provider-native strings (e.g. claude-opus-4-7) set Bead.Model directly.
	// Empty flag leaves bead metadata unchanged.
	if cfg.model != "" {
		tier, providerModel := parseModelFlag(cfg.model)
		if tier != "" {
			cfg.bead.Tier = tier
			cfg.bead.Model = ""
		} else {
			cfg.bead.Model = providerModel
			cfg.bead.Tier = ""
		}
	}
	// Resolve runtime, model, and reasoning using standard bead resolution.
	runtime, model, reasoning := resolveWorkerRuntimeModel(cfg)

	if cfg.dryRun {
		reviewRuntime, reviewModel, reviewReasoning := agentmodel.ResolveForRole("ops_review")
		logStep("Dry run — would execute bead %s with runtime=%s, model=%s, reasoning=%s, review-runtime=%s, review-model=%s, review-reasoning=%s, timeout=%s, skip-review=%t",
			cfg.beadID, runtime, model, reasoning, reviewRuntime, reviewModel, reviewReasoning, cfg.timeout, cfg.skipReview)
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
	if err := observeStandaloneStorageController(ctx, cfg.storageController); err != nil {
		return err
	}

	// Step 3: Create or resume worktree.
	// Resolve defaultBranch: --base-branch flag > config default_branch > "main"
	defaultBranch := deps.defaultBranch
	if cfg.baseBranch != "" {
		defaultBranch = cfg.baseBranch
	}
	// Resolve targetBranch by walking the parent chain: returns "epic/<id>" only when
	// an epic-type ancestor exists. Non-epic parents (tasks, features) resolve to defaultBranch.
	targetBranch, resolvedEpicID, resolveErr := dispatcher.ResolveEpicBranch(ctx, deps.beadSrc, cfg.bead.Epic, defaultBranch)
	if resolveErr != nil {
		return fmt.Errorf("resolve epic branch: %w", resolveErr)
	}
	if prepareErr := prepareStandaloneWorkTargetBranch(ctx, deps, targetBranch, defaultBranch, resolvedEpicID, cfg.bead); prepareErr != nil {
		return fmt.Errorf("prepare target branch: %w", prepareErr)
	}
	worktree, branch, err := setupWorktree(ctx, cfg, deps, targetBranch)
	if err != nil {
		return fmt.Errorf("worktree setup: %w", err)
	}

	var feedback string
	var attempt int
	var escalated bool

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
			if err := observeStandaloneStorageController(ctx, cfg.storageController); err != nil {
				return err
			}
			logStep("--- attempt %d (%s) ---", attempt, modelShort(model))
			logStep("Spawning %s (%s, attempt %d)...", runtime, modelShort(model), attempt)
			if err := spawnAndWait(ctx, cfg, deps, worktree, runtime, model, reasoning, attempt, feedback, logFile); err != nil {
				return fmt.Errorf("%s spawn: %w", runtime, err)
			}
			logStep("%s completed", runtime)

			// Guard: bail out if claude produced no commits.
			if !deps.hasNewWork(deps.repoRoot, branch, targetBranch) {
				return noCommitsResult(ctx, cfg, deps, worktree, &merged)
			}
		}
		skipClaude = false // Only skip the first iteration.

		mutationMode := workMutationMode(cfg)
		if err := observeStandaloneStorageController(ctx, cfg.storageController); err != nil {
			return err
		}
		logStep("Running local quality gate (%s)...", mutationMode)
		passed, qgOutput, qgErr := deps.runQG(ctx, worktree, !cfg.mutationTesting)
		if qgErr != nil {
			recordWorkQGFailure(ctx, cfg, deps, "oro-work-implementation", qgErr.Error())
			return fmt.Errorf("quality gate error: %w", qgErr)
		}

		if passed {
			logStep("Quality gate passed (%s)", mutationMode)
			break
		}

		attempt++
		feedback = qgOutput
		logStep("Quality gate failed (attempt %d)", attempt)

		if attempt >= maxQGRetriesPerTier && !escalated {
			runtime, model, reasoning = resolveWorkerEscalationRuntimeModel(cfg)
			logStep("Escalating to %s (%s)", runtime, modelShort(model))
			attempt = 0
			escalated = true
		}
		if attempt >= maxQGRetriesPerTier {
			recordWorkQGFailure(ctx, cfg, deps, "oro-work-implementation", qgOutput)
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

	// Step 9: Merge to main. The final quality gate runs inside mergeToMain
	// after rebase and while the FF lock prevents the target from advancing.
	mergeResult, mergeErr := mergeToMain(ctx, cfg, deps, worktree, branch, targetBranch)
	if mergeErr != nil {
		var exitErr *exitError
		if errors.As(mergeErr, &exitErr) {
			return exitErr
		}
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
	if err := deps.wtMgr.DeleteBranchMergedInto(ctx, branchName, targetBranch); err != nil {
		logStep("Warning: branch cleanup failed: %v", err)
	}

	return nil
}

func prepareStandaloneWorkTargetBranch(ctx context.Context, deps *workDeps, targetBranch, defaultBranch, resolvedEpicID string, bead *protocol.BeadDetail) error {
	if deps == nil || deps.wtMgr == nil || resolvedEpicID == "" || targetBranch == "" || targetBranch == defaultBranch {
		return nil
	}
	preparer, ok := deps.wtMgr.(standaloneBaseBranchPreparer)
	if !ok {
		return nil
	}
	fastForwarded, err := preparer.PrepareBaseBranchForAssignment(ctx, targetBranch, defaultBranch)
	if err != nil {
		return fmt.Errorf("prepare target branch %s from %s: %w", targetBranch, defaultBranch, err)
	}
	if fastForwarded {
		logStep("Fast-forwarded target branch %s to %s", targetBranch, defaultBranch)
	}
	return validateStandaloneEpicBranchSafe(ctx, deps, targetBranch, defaultBranch, bead, resolvedEpicID)
}

func validateStandaloneEpicBranchSafe(ctx context.Context, deps *workDeps, targetBranch, defaultBranch string, bead *protocol.BeadDetail, resolvedEpicID string) error {
	checker, ok := deps.wtMgr.(standaloneBaseBranchSafetyChecker)
	if !ok {
		return nil
	}
	diverged, err := standaloneEpicBranchesDiverged(ctx, checker, targetBranch, defaultBranch)
	if err != nil {
		return fmt.Errorf("check whether %s diverged from %s: %w", targetBranch, defaultBranch, err)
	}
	if diverged {
		if dispatcher.IsEpicRebaseChild(bead, resolvedEpicID, targetBranch) {
			return nil
		}
		return fmt.Errorf("epic branch %q diverged from %q; preserved divergent branch/worktree state and aborted before worker spawn. Inspect `git log --oneline --graph %s %s`, then preserve or port wanted commits before resetting %s to %s",
			targetBranch, defaultBranch, defaultBranch, targetBranch, targetBranch, defaultBranch)
	}
	return nil
}

func standaloneEpicBranchesDiverged(ctx context.Context, checker standaloneBaseBranchSafetyChecker, targetBranch, defaultBranch string) (bool, error) {
	targetHasUniqueCommits, err := checker.BaseBranchHasUniqueCommits(ctx, targetBranch, defaultBranch)
	if err != nil {
		return false, fmt.Errorf("check unique commits on %s relative to %s: %w", targetBranch, defaultBranch, err)
	}
	if !targetHasUniqueCommits {
		return false, nil
	}
	defaultHasUniqueCommits, err := checker.BaseBranchHasUniqueCommits(ctx, defaultBranch, targetBranch)
	if err != nil {
		return false, fmt.Errorf("check unique commits on %s relative to %s: %w", defaultBranch, targetBranch, err)
	}
	return defaultHasUniqueCommits, nil
}

func resolveWorkerRuntimeModel(cfg *workConfig) (runtime, model, reasoning string) {
	runtime, model, reasoning = agentmodel.ResolveForBead("worker", *cfg.bead)
	if cfg.runtime != "" {
		runtime = cfg.runtime
	}
	return runtime, model, reasoning
}

func resolveWorkerEscalationRuntimeModel(cfg *workConfig) (runtime, model, reasoning string) {
	runtime, model, reasoning = agentmodel.ResolveForRole("worker_escalation")
	if cfg.runtime != "" {
		runtime = cfg.runtime
	}
	return runtime, model, reasoning
}

func recordWorkQGFailure(ctx context.Context, cfg *workConfig, deps *workDeps, component, output string) {
	if deps == nil || deps.recordQGFailure == nil {
		logStep("qg failure recorder degraded: no recorder (component=%s bead=%s)", component, cfg.beadID)
		return
	}
	fingerprint, summary := dispatcher.FingerprintQGFailure(output, dispatcher.QGFingerprintOptions{})
	rec := dispatcher.QGFailureRecord{
		ID:          fmt.Sprintf("%s:%s:%s", cfg.beadID, component, fingerprint),
		BeadID:      cfg.beadID,
		Component:   component,
		Fingerprint: fingerprint,
		Summary:     summary,
		Output:      output,
	}
	cls := dispatcher.ClassifyQGFailure(rec, dispatcher.QGFailureHistory{RetryExhausted: true})
	if err := deps.recordQGFailure(ctx, rec, cls); err != nil {
		logStep("qg failure recorder error: %v", err)
	}
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
	cardCtx := relevantCardsForWorkPrompt(context.Background(), deps, cfg.bead)

	return worker.AssemblePrompt(worker.PromptParams{
		BeadID:             cfg.beadID,
		Title:              cfg.bead.Title,
		Description:        cfg.bead.Description,
		AcceptanceCriteria: cfg.bead.AcceptanceCriteria,
		Cards:              cardCtx,
		WorktreePath:       worktree,
		Model:              model,
		ProjectRoot:        deps.repoRoot,
	}), nil
}

func relevantCardsForWorkPrompt(ctx context.Context, deps *workDeps, bead *protocol.Bead) cards.RelevantCards {
	if deps == nil || deps.cardStore == nil || bead == nil {
		return cards.RelevantCards{}
	}
	relevant, err := deps.cardStore.Relevant(ctx, beadRelevanceQuery(*bead))
	if err != nil {
		logStep("cards relevant context unavailable for %s: %v", bead.ID, err)
		return cards.RelevantCards{}
	}
	return relevant
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
		if err := prepareExistingStandaloneWorktree(ctx, deps, wtPath, branch, baseBranch); err != nil {
			return "", "", err
		}
		return wtPath, branch, nil
	}

	wtPath, branch, err = deps.wtMgr.Create(ctx, cfg.beadID, baseBranch)
	if err != nil {
		return "", "", fmt.Errorf("create worktree: %w", err)
	}
	logStep("Worktree: %s (branch %s)", wtPath, branch)
	return wtPath, branch, nil
}

func prepareExistingStandaloneWorktree(ctx context.Context, deps *workDeps, worktree, branch, baseBranch string) error {
	if deps == nil || deps.wtMgr == nil {
		return nil
	}
	currentBranch, err := deps.wtMgr.CurrentBranch(ctx, worktree)
	if err != nil {
		return fmt.Errorf("inspect existing worktree branch: %w", err)
	}
	if currentBranch != branch {
		return fmt.Errorf("existing worktree %s is on branch %s, want %s", worktree, currentBranch, branch)
	}
	preparer, ok := deps.wtMgr.(standaloneExistingWorktreePreparer)
	if !ok {
		return nil
	}
	fastForwarded, err := preparer.PrepareExistingForReuse(ctx, worktree, branch, baseBranch)
	if err != nil {
		return fmt.Errorf("prepare existing worktree for reuse: %w", err)
	}
	if fastForwarded {
		logStep("Fast-forwarded worktree %s (%s) to %s", worktree, branch, baseBranch)
	}
	return nil
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

// spawnAndWait spawns the configured agent runtime and waits for it to exit, with timeout.
// logFile, when non-nil, receives a copy of runtime stdout alongside stderr.
func spawnAndWait(ctx context.Context, cfg *workConfig, deps *workDeps, worktree, runtime, model, reasoning string, attempt int, feedback string, logFile *os.File) error {
	// Resolve project root from worktree path
	projectRoot := ""
	if resolved, err := langprofile.ResolveProjectRoot(worktree); err == nil {
		projectRoot = resolved
	}

	cardCtx := relevantCardsForWorkPrompt(ctx, deps, cfg.bead)

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
		Cards:                cardCtx,
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

	proc, stdout, streamFormat, err := spawnRuntimeProcess(timeoutCtx, deps, runtime, model, reasoning, prompt, worktree)
	if err != nil {
		return err
	}

	// Drain stdout (echoes to stderr + optional log file, extracts memories).
	drainRuntimeOutput(ctx, deps, stdout, streamFormat, cfg.beadID, worktree, logFile)

	if err := proc.Wait(); err != nil {
		// Non-zero exit is common for agent CLIs; log but don't fail.
		logStep("Runtime exited with: %v", err)
	}
	return nil
}

func spawnRuntimeProcess(ctx context.Context, deps *workDeps, runtime, model, reasoning, prompt, worktree string) (worker.Process, io.ReadCloser, worker.StreamFormat, error) {
	var proc worker.Process
	var stdout io.ReadCloser
	var err error
	streamFormat := deps.spawner.StreamFormat()
	if deps.runtimeSpawner != nil {
		var format worker.StreamFormat
		proc, stdout, _, format, err = deps.runtimeSpawner.Spawn(ctx, runtime, model, reasoning, prompt, worktree)
		streamFormat = format
	} else {
		proc, stdout, _, err = deps.spawner.Spawn(ctx, model, prompt, worktree)
	}
	if err != nil {
		return nil, nil, "", fmt.Errorf("spawn: %w", err)
	}
	return proc, stdout, streamFormat, nil
}

func drainRuntimeOutput(ctx context.Context, deps *workDeps, stdout io.ReadCloser, streamFormat worker.StreamFormat, beadID, worktree string, logFile *os.File) {
	if stdout != nil {
		writers := []io.Writer{os.Stderr}
		if logFile != nil {
			writers = append(writers, logFile)
		}
		var learningSink worker.LearningSink
		if deps.cardStore != nil {
			learningSink = deps.cardStore
		}
		worker.DrainOutputInWorkdir(ctx, stdout, streamFormat, learningSink, beadID, newWorkerMemoryExtractSpawner(), worktree, writers...)
	}
}

// reviewLoop runs ops review and handles rejection retries.
// targetBranch is the branch the worker merges into (epic branch or "main").
func reviewLoop(ctx context.Context, cfg *workConfig, deps *workDeps, worktree, targetBranch string, model *string, attempt *int, feedback *string, logFile *os.File) error {
	projPaths, err := ResolvePaths(worktree)
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}

	for rejects := 0; ; {
		if err := observeStandaloneStorageController(ctx, cfg.storageController); err != nil {
			return err
		}
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

	runtime, escalationModel, reasoning := resolveWorkerEscalationRuntimeModel(cfg)
	*model = escalationModel
	*attempt = rejects
	*feedback = result.Feedback

	logStep("Re-executing with review feedback (%s)...", modelShort(*model))
	if err := observeStandaloneStorageController(ctx, cfg.storageController); err != nil {
		return rejects, err
	}
	if err := spawnAndWait(ctx, cfg, deps, worktree, runtime, *model, reasoning, *attempt, *feedback, logFile); err != nil {
		return rejects, fmt.Errorf("%s re-spawn after review: %w", runtime, err)
	}

	logStep("Re-running local quality gate (%s)...", workMutationMode(cfg))
	if err := observeStandaloneStorageController(ctx, cfg.storageController); err != nil {
		return rejects, err
	}
	passed, qgOutput, qgErr := deps.runQG(ctx, worktree, !cfg.mutationTesting)
	if qgErr != nil {
		recordWorkQGFailure(ctx, cfg, deps, "oro-work-implementation", qgErr.Error())
		return rejects, fmt.Errorf("quality gate error: %w", qgErr)
	}
	if !passed {
		recordWorkQGFailure(ctx, cfg, deps, "oro-work-implementation", qgOutput)
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

// parseModelFlag interprets a --model flag value and returns (tier, providerModel).
// Tier names (fast/balanced/deep/background) and legacy shortnames (opus/sonnet/haiku)
// return a non-empty Tier and empty providerModel; the tier drives model selection.
// Anything else is treated as a provider-native model string (e.g. claude-opus-4-7).
// Empty input returns ("", "") meaning "unset — use bead metadata".
func parseModelFlag(raw string) (tier protocol.Tier, providerModel string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if t := protocol.Tier(strings.ToLower(raw)); t.IsKnown() {
		return t, ""
	}
	if t, ok := protocol.LegacyModelToTier(raw); ok {
		return t, ""
	}
	return "", raw
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
	if err := observeStandaloneStorageController(ctx, cfg.storageController); err != nil {
		return nil, err
	}
	logStep("Merging to main...")
	result, err := deps.merger.Merge(ctx, merge.Opts{
		Branch:       branch,
		Worktree:     worktree,
		BeadID:       cfg.beadID,
		TargetBranch: targetBranch,
		PreFFCheck: func(checkCtx context.Context, finalWorktree string) error {
			if err := observeStandaloneStorageController(checkCtx, cfg.storageController); err != nil {
				return err
			}
			logStep("Running pre-merge quality gate (%s)...", workMutationMode(cfg))
			passed, output, qgErr := deps.runQG(checkCtx, finalWorktree, !cfg.mutationTesting)
			if qgErr != nil {
				return &merge.PreFFCheckError{Output: qgErr.Error(), Err: qgErr}
			}
			if !passed {
				return &merge.PreFFCheckError{Output: output, Err: errors.New("quality gate failed")}
			}
			logStep("Pre-merge quality gate passed")
			return nil
		},
	})
	if err == nil {
		return result, nil
	}

	var preFFErr *merge.PreFFCheckError
	if errors.As(err, &preFFErr) {
		output := preFFErr.Output
		if output == "" {
			output = preFFErr.Error()
		}
		recordWorkQGFailure(ctx, cfg, deps, "oro-work-pre-merge", output)
		return nil, &exitError{
			code: exitCodeRetries,
			msg:  fmt.Sprintf("Pre-merge quality gate failed:\n%s", output),
		}
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
	dirty, dirtyStatus, dirtyErr := noCommitWorktreeDirty(ctx, deps, worktree)
	if dirtyErr != nil {
		logStep("No commits on branch — preserving worktree because cleanliness check failed: %v", dirtyErr)
		return fmt.Errorf("worker exited without producing commits on bead %s; preserved worktree %s because cleanliness check failed: %w",
			cfg.beadID, worktree, dirtyErr)
	}
	if dirty {
		logStep("No commits on branch — preserving worktree with uncommitted changes")
		return fmt.Errorf("worker exited without producing commits on bead %s; preserved worktree %s because it has uncommitted changes:\n%s",
			cfg.beadID, worktree, dirtyStatus)
	}

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

func noCommitWorktreeDirty(ctx context.Context, deps *workDeps, worktree string) (dirty bool, status string, err error) {
	if deps == nil || deps.worktreeDirty == nil {
		return false, "", nil
	}
	return deps.worktreeDirty(ctx, worktree)
}

func worktreeHasUncommittedChanges(ctx context.Context, worktree string) (dirty bool, status string, err error) {
	cmd := exec.CommandContext(ctx, "git", "-C", worktree, "status", "--porcelain") //nolint:gosec // worktree is an internally managed path
	out, runErr := cmd.CombinedOutput()
	status = strings.TrimSpace(string(out))
	if runErr != nil {
		return false, status, fmt.Errorf("git status --porcelain: %w: %s", runErr, status)
	}
	return status != "", status, nil
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
	if err := observeStandaloneStorageController(ctx, cfg.storageController); err != nil {
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
