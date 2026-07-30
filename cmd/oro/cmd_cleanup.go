package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/storage"

	"github.com/spf13/cobra"
)

const (
	cleanupDispatcherExitWait = 5 * time.Second
	cleanupPIDLockMaxAge      = time.Hour
)

// cleanupConfig holds injectable dependencies for the cleanup command.
type cleanupConfig struct {
	runner          CmdRunner
	w               io.Writer
	tmuxName        string
	pidPath         string
	sockPath        string
	stateDBPath     string // path to native SQLite state.db; empty disables bead state repair
	worktreesDir    string // path to .worktrees directory; empty disables worktree dir removal
	AssignmentsOnly bool
	signalFn        func(int) error // sends SIGINT; injectable for testing
	aliveFn         func(int) bool  // checks process liveness; injectable for testing
	isTTY           func() bool     // returns true if stdin is a TTY; injectable for testing
	exitWait        time.Duration   // bounded wait for dispatcher exit after SIGINT
	liveWorkerIDs   func(context.Context) (map[string]bool, error)
}

// newCleanupCmd creates the "oro cleanup" subcommand.
func newCleanupCmd() *cobra.Command {
	cfg := cleanupConfig{}
	var apply bool
	var dryRun bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Clean all stale state after a crash",
		Long: `Idempotently cleans up all Oro state: kills tmux session, dispatcher,
and worker processes; removes stale PID/socket files; prunes git worktrees;
deletes agent/* branches; and resets orphaned in_progress beads to open.

Safe to run anytime. If nothing is running, reports "nothing to clean".

Use --dry-run or --apply for compatibility with the preservation-first runtime
storage planner.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if apply && dryRun {
				return fmt.Errorf("--apply and --dry-run cannot be used together")
			}
			if apply || dryRun || jsonOut {
				oroHome, err := resolveOroHome()
				if err != nil {
					return fmt.Errorf("resolve Oro home: %w", err)
				}
				result, err := runLegacyCleanupStorage(cmd.Context(), oroHome, apply)
				if err != nil {
					return err
				}
				return writeStorageCleanup(cmd.OutOrStdout(), result, jsonOut)
			}

			paths, err := ResolveDaemonPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getwd: %w", err)
			}
			projPaths, err := ResolvePaths(cwd)
			if err != nil {
				return fmt.Errorf("resolve project paths: %w", err)
			}

			cleanupCfg := &cleanupConfig{
				runner:          &ExecRunner{},
				w:               cmd.OutOrStdout(),
				tmuxName:        TmuxSessionName(readProjectNameCWD()),
				pidPath:         paths.PIDPath,
				sockPath:        paths.SocketPath,
				stateDBPath:     paths.StateDBPath,
				worktreesDir:    projPaths.WorktreesDir,
				signalFn:        defaultSignalINT,
				aliveFn:         IsProcessAlive,
				isTTY:           isStdinTTY,
				exitWait:        cleanupDispatcherExitWait,
				AssignmentsOnly: cfg.AssignmentsOnly,
			}

			return runCleanup(cmd.Context(), cleanupCfg)
		},
	}
	cmd.Flags().BoolVar(&cfg.AssignmentsOnly, "assignments-only", false, "complete orphaned assignment rows only; leave processes, worktrees and agent branches untouched")
	cmd.Flags().BoolVar(&apply, "apply", false, "remove runtime candidates proven safe by the storage cleanup plan")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the runtime storage cleanup plan")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the runtime storage cleanup plan as JSON")
	return cmd
}

func runLegacyCleanupStorage(ctx context.Context, oroHome string, apply bool) (storageCleanupOutput, error) {
	return runStorageClean(ctx, oroHome, storage.ScopeRuntime, apply)
}

// runCleanup performs best-effort cleanup of all Oro state.
// Each step continues on error, reporting warnings. Uncertain branch cleanup
// errors are returned so callers can fail loudly instead of deleting unique work.
func runCleanup(ctx context.Context, cfg *cleanupConfig) error {
	if cfg.isTTY != nil && !cfg.isTTY() {
		return fmt.Errorf("oro cleanup requires an interactive terminal (stdin is not a TTY)")
	}
	if cfg.AssignmentsOnly {
		cleaned, err := cleanupOrphanedAssignments(ctx, cfg)
		if err == nil && !cleaned {
			fmt.Fprintln(cfg.w, "nothing to clean")
		}
		return err
	}

	return runFullCleanup(ctx, cfg)
}

// runFullCleanup performs the destructive cleanup sequence: processes, stale
// runtime files, worktrees, agent branches, and bead reset. Extracted from
// runCleanup so that adding guard branches there does not push a single
// function past the gocyclo limit — the eleven sequential steps already sit
// close to it on their own.
func runFullCleanup(ctx context.Context, cfg *cleanupConfig) error {
	cleaned := false
	var cleanupErr error

	// 1. Kill tmux session if it exists.
	if cleanedTmux := cleanupTmux(cfg); cleanedTmux {
		cleaned = true
	}

	// 2. Kill dispatcher process if running (read PID file).
	if cleanedDispatcher, dispatcherPID := cleanupDispatcher(cfg); cleanedDispatcher {
		cleaned = true
		waitForDispatcherExit(cfg, dispatcherPID)
	}

	// 3. Kill worker claude processes with ORO_ROLE env var.
	if cleanedWorkers := cleanupWorkers(cfg); cleanedWorkers {
		cleaned = true
	}

	// 4. Remove stale PID file.
	if cleanedPID := cleanupPIDFile(cfg); cleanedPID {
		cleaned = true
	}

	// 5. Remove stale socket file.
	if cleanedSock := cleanupSocketFile(cfg); cleanedSock {
		cleaned = true
	}

	// 6. Remove stale dispatcher state DB lock.
	if cleanedLock := cleanupStateDBLock(cfg); cleanedLock {
		cleaned = true
	}

	// 7. Prune git worktrees.
	cleanupWorktrees(cfg)

	// 8. Remove .worktrees/ directory.
	if cleanedWorktreeDir := cleanupWorktreeDir(cfg); cleanedWorktreeDir {
		cleaned = true
	}

	// 9. Delete agent/* and epic/* branches.
	cleanedBranches, err := cleanupAgentBranches(ctx, cfg)
	if cleanedBranches {
		cleaned = true
	}
	if err != nil {
		cleanupErr = err
	}

	// 10. Reset in_progress beads back to open.
	if cleanedBeads := cleanupBeads(ctx, cfg); cleanedBeads {
		cleaned = true
	}

	if !cleaned {
		fmt.Fprintln(cfg.w, "nothing to clean")
	}

	return cleanupErr
}

func cleanupOrphanedAssignments(ctx context.Context, cfg *cleanupConfig) (bool, error) {
	if cfg.stateDBPath == "" {
		return false, nil
	}
	liveWorkerIDs, err := cleanupLiveWorkerIDs(ctx, cfg)
	if err != nil {
		return false, err
	}
	db, err := openStateDB(cfg.stateDBPath)
	if err != nil {
		return false, fmt.Errorf("open state db: %w", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(ctx, `SELECT id, bead_id, worker_id FROM assignments WHERE status='active'`)
	if err != nil {
		return false, fmt.Errorf("list active assignments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type activeAssignment struct {
		id       int64
		beadID   string
		workerID string
	}
	var assignments []activeAssignment
	for rows.Next() {
		var assignmentID int64
		var beadID, workerID string
		if err := rows.Scan(&assignmentID, &beadID, &workerID); err != nil {
			return false, fmt.Errorf("scan active assignment: %w", err)
		}
		if liveWorkerIDs[workerID] {
			continue
		}
		assignments = append(assignments, activeAssignment{id: assignmentID, beadID: beadID, workerID: workerID})
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate active assignments: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close active assignments: %w", err)
	}

	cleaned := false
	for _, assignment := range assignments {
		completed, err := completeOrphanedAssignment(ctx, db, assignment.id, assignment.beadID)
		if err != nil {
			return false, err
		}
		if completed {
			cleaned = true
			fmt.Fprintf(cfg.w, "completed orphaned assignment for bead %s\n", assignment.beadID)
		}
	}
	return cleaned, nil
}

func completeOrphanedAssignment(ctx context.Context, db *sql.DB, assignmentID int64, beadID string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin complete orphaned assignment %d: %w", assignmentID, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
UPDATE beads
   SET status='open'
 WHERE id=?
   AND status='in_progress'
   AND EXISTS (SELECT 1 FROM assignments WHERE id=? AND status='active')
   AND NOT EXISTS (SELECT 1 FROM assignments WHERE bead_id=? AND status='active' AND id<>?)`, beadID, assignmentID, beadID, assignmentID); err != nil {
		return false, fmt.Errorf("reset orphaned bead %s: %w", beadID, err)
	}

	res, err := tx.ExecContext(ctx, `
UPDATE assignments
   SET status='completed', completed_at=datetime('now')
 WHERE id=? AND status='active'`, assignmentID)
	if err != nil {
		return false, fmt.Errorf("complete orphaned assignment %d: %w", assignmentID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read completed assignment count: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit completed orphaned assignment %d: %w", assignmentID, err)
	}
	return affected > 0, nil
}

func cleanupLiveWorkerIDs(ctx context.Context, cfg *cleanupConfig) (map[string]bool, error) {
	if cfg.liveWorkerIDs != nil {
		return cfg.liveWorkerIDs(ctx)
	}
	status, _, err := DaemonStatus(cfg.pidPath, cfg.sockPath)
	if err != nil {
		return nil, fmt.Errorf("get daemon status: %w", err)
	}
	if status != StatusRunning {
		return map[string]bool{}, nil
	}
	resp, err := fetchDispatcherStatusAt(ctx, cfg.sockPath)
	if err != nil {
		return nil, fmt.Errorf("get live workers: %w", err)
	}
	liveWorkerIDs := make(map[string]bool, len(resp.Workers))
	for _, worker := range resp.Workers {
		liveWorkerIDs[worker.ID] = true
	}
	return liveWorkerIDs, nil
}

// cleanupTmux kills the tmux session if it exists. Returns true if something was cleaned.
func cleanupTmux(cfg *cleanupConfig) bool {
	tmux := &TmuxSession{Name: cfg.tmuxName, Runner: cfg.runner}
	if !tmux.Exists() {
		return false
	}

	fmt.Fprintf(cfg.w, "killing tmux session %q\n", cfg.tmuxName)
	if err := tmux.Kill(); err != nil {
		fmt.Fprintf(cfg.w, "warning: tmux kill: %v\n", err)
	}
	return true
}

// cleanupDispatcher signals the dispatcher process if running. Returns cleaned
// and the signaled PID if something was cleaned.
// Sends SIGINT (always honored by daemon) for graceful shutdown.
// Falls back to socket probe when PID file is missing.
func cleanupDispatcher(cfg *cleanupConfig) (cleaned bool, signaledPID int) {
	pid, err := ReadPIDFile(cfg.pidPath)
	if err != nil {
		// No PID file — try socket probe to discover PID.
		pid = probeSocket(cfg.sockPath)
		if pid == 0 {
			return false, 0
		}
	}

	if !cfg.aliveFn(pid) {
		// Process is dead, PID file is stale — will be cleaned in step 4.
		return false, 0
	}

	fmt.Fprintf(cfg.w, "killing dispatcher (PID %d)\n", pid)
	if err := cfg.signalFn(pid); err != nil {
		fmt.Fprintf(cfg.w, "warning: signal dispatcher PID %d: %v\n", pid, err)
	}
	return true, pid
}

func waitForDispatcherExit(cfg *cleanupConfig, pid int) {
	if cfg.exitWait <= 0 || pid <= 0 {
		return
	}
	deadline := time.Now().Add(cfg.exitWait)
	for time.Now().Before(deadline) {
		if !cfg.aliveFn(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	if cfg.aliveFn(pid) {
		fmt.Fprintf(cfg.w, "warning: dispatcher PID %d still alive after %s\n", pid, cfg.exitWait)
	}
}

// cleanupWorkers finds and kills worker processes with ORO_ROLE env var.
// Returns true if something was cleaned.
func cleanupWorkers(cfg *cleanupConfig) bool {
	out, err := cfg.runner.Run("pgrep", "-f", "ORO_ROLE")
	if err != nil {
		// No matching processes.
		return false
	}

	pids := parseWorkerPIDs(out)
	if len(pids) == 0 {
		return false
	}

	fmt.Fprintf(cfg.w, "killing %d worker process(es)\n", len(pids))
	for _, pid := range pids {
		if err := cfg.signalFn(pid); err != nil {
			fmt.Fprintf(cfg.w, "warning: signal worker PID %d: %v\n", pid, err)
		}
	}
	return true
}

// parseWorkerPIDs parses newline-separated PIDs from pgrep output.
func parseWorkerPIDs(output string) []int {
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// cleanupPIDFile removes a stale PID file. Returns true if the file existed and was removed.
func cleanupPIDFile(cfg *cleanupConfig) bool {
	if _, err := os.Stat(cfg.pidPath); errors.Is(err, os.ErrNotExist) {
		return false
	}

	fmt.Fprintf(cfg.w, "removing stale pid file %s\n", cfg.pidPath)
	if err := RemovePIDFile(cfg.pidPath); err != nil {
		fmt.Fprintf(cfg.w, "warning: remove pid file: %v\n", err)
	}
	return true
}

// cleanupSocketFile removes a stale socket file. Returns true if the file existed and was removed.
func cleanupSocketFile(cfg *cleanupConfig) bool {
	if _, err := os.Stat(cfg.sockPath); errors.Is(err, os.ErrNotExist) {
		return false
	}

	fmt.Fprintf(cfg.w, "removing stale socket file %s\n", cfg.sockPath)
	err := os.Remove(cfg.sockPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(cfg.w, "warning: remove socket file: %v\n", err)
	}
	return true
}

func cleanupStateDBLock(cfg *cleanupConfig) bool {
	if cfg.stateDBPath == "" {
		return false
	}
	canonicalStateDBPath, err := cleanupCanonicalStateDBPath(cfg.stateDBPath)
	if err != nil {
		fmt.Fprintf(cfg.w, "warning: canonicalize state DB lock: %v\n", err)
		return false
	}
	lockPath := canonicalStateDBPath + ".lock"
	lock, err := inspectPIDLock(lockPath)
	if err != nil {
		fmt.Fprintf(cfg.w, "warning: inspect state DB lock: %v\n", err)
		return false
	}
	if !lock.exists || !lock.stale {
		return false
	}
	removed, err := removeInspectedPIDLock(lock)
	if err != nil {
		fmt.Fprintf(cfg.w, "warning: remove state DB lock: %v\n", err)
		return false
	}
	if removed {
		fmt.Fprintf(cfg.w, "removed stale state DB lock %s\n", lockPath)
	}
	return removed
}

func cleanupCanonicalStateDBPath(dbPath string) (string, error) {
	resolved, err := filepath.EvalSymlinks(dbPath)
	if err == nil {
		return resolved, nil
	}
	parent := filepath.Dir(dbPath)
	if resolvedParent, parentErr := filepath.EvalSymlinks(parent); parentErr == nil {
		return filepath.Join(resolvedParent, filepath.Base(dbPath)), nil
	}
	abs, absErr := filepath.Abs(dbPath)
	if absErr != nil {
		return "", fmt.Errorf("canonicalize state db path %s: %w", dbPath, absErr)
	}
	return abs, nil
}

type inspectedPIDLock struct {
	path    string
	pid     int
	exists  bool
	alive   bool
	old     bool
	stale   bool
	modTime time.Time
}

func inspectPIDLock(lockPath string) (inspectedPIDLock, error) {
	info, err := os.Stat(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return inspectedPIDLock{path: lockPath, stale: true}, nil
		}
		return inspectedPIDLock{}, fmt.Errorf("stat lock file %s: %w", lockPath, err)
	}
	data, err := os.ReadFile(lockPath) //nolint:gosec // lock path is derived from StateDBPath
	if err != nil {
		return inspectedPIDLock{}, fmt.Errorf("read lock file %s: %w", lockPath, err)
	}
	pid := parsePIDLockText(data)
	if pid <= 0 {
		return staleExistingPIDLock(lockPath, info.ModTime()), nil
	}
	alive := processAlive(pid)
	old := time.Since(info.ModTime()) > cleanupPIDLockMaxAge
	return inspectedPIDLock{
		path:    lockPath,
		pid:     pid,
		exists:  true,
		alive:   alive,
		old:     old,
		stale:   !alive || old,
		modTime: info.ModTime(),
	}, nil
}

func parsePIDLockText(data []byte) int {
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

func staleExistingPIDLock(lockPath string, modTime time.Time) inspectedPIDLock {
	return inspectedPIDLock{path: lockPath, exists: true, old: true, stale: true, modTime: modTime}
}

func removeInspectedPIDLock(lock inspectedPIDLock) (bool, error) {
	current, err := inspectPIDLock(lock.path)
	if err != nil {
		return false, err
	}
	if !current.exists {
		return true, nil
	}
	if current.pid != lock.pid || !current.modTime.Equal(lock.modTime) || current.stale != lock.stale {
		return false, nil
	}
	stalePath := fmt.Sprintf("%s.stale.%d.%d", lock.path, os.Getpid(), time.Now().UnixNano())
	if err := os.Rename(lock.path, stalePath); err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("rename stale lock %s: %w", lock.path, err)
	}
	moved, err := inspectPIDLock(stalePath)
	if err != nil {
		return false, err
	}
	if moved.pid != lock.pid || !moved.modTime.Equal(lock.modTime) || moved.stale != lock.stale {
		if restoreErr := os.Rename(stalePath, lock.path); restoreErr != nil {
			return false, fmt.Errorf("restore changed lock %s: %w", lock.path, restoreErr)
		}
		return false, nil
	}
	if err := os.Remove(stalePath); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("remove stale lock %s: %w", stalePath, err)
	}
	return true, nil
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// cleanupWorktrees runs git worktree prune.
func cleanupWorktrees(cfg *cleanupConfig) {
	if _, err := cfg.runner.Run("git", "worktree", "prune"); err != nil {
		fmt.Fprintf(cfg.w, "warning: git worktree prune: %v\n", err)
	}
}

// cleanupWorktreeDir force-removes the worktrees directory. Returns true if directory was removed.
func cleanupWorktreeDir(cfg *cleanupConfig) bool {
	if cfg.worktreesDir == "" {
		return false
	}
	if _, err := os.Stat(cfg.worktreesDir); errors.Is(err, os.ErrNotExist) {
		return false
	}
	fmt.Fprintf(cfg.w, "removing %s directory\n", cfg.worktreesDir)
	if err := os.RemoveAll(cfg.worktreesDir); err != nil {
		fmt.Fprintf(cfg.w, "warning: remove %s: %v\n", cfg.worktreesDir, err)
	}
	return true
}

type branchCleanupAction string

const (
	branchCleanupDelete   branchCleanupAction = "delete"
	branchCleanupPreserve branchCleanupAction = "preserve"
)

type branchCleanupDecision struct {
	Branch string
	Action branchCleanupAction
	Reason string
	Ahead  int
	Err    error
}

// cleanupAgentBranches deletes merged local agent/* branches and force-deletes epic/* branches.
// It preserves checked-out, unmerged, and uncertain agent branches.
func cleanupAgentBranches(ctx context.Context, cfg *cleanupConfig) (bool, error) {
	cleaned := false
	var cleanupErr error

	decisions, err := classifyAgentBranches(ctx, "", cfg.runner)
	if err != nil {
		fmt.Fprintf(cfg.w, "warning: classify agent branches: %v\n", err)
		cleanupErr = err
	}
	for _, decision := range decisions {
		branchCleaned, err := applyAgentBranchDecision(cfg, decision)
		if branchCleaned {
			cleaned = true
		}
		if err != nil {
			cleanupErr = err
		}
	}

	// Delete epic/* branches
	out, err := cfg.runner.Run("git", "branch", "--list", "epic/*")
	if err != nil {
		fmt.Fprintf(cfg.w, "warning: list epic branches: %v\n", err)
	} else {
		branches := parseBranchNames(out)
		for _, branch := range branches {
			fmt.Fprintf(cfg.w, "deleting branch %s\n", branch)
			if _, err := cfg.runner.Run("git", "branch", "-D", branch); err != nil {
				fmt.Fprintf(cfg.w, "warning: delete branch %s: %v\n", branch, err)
			}
		}
		if len(branches) > 0 {
			cleaned = true
		}
	}

	return cleaned, cleanupErr
}

func applyAgentBranchDecision(cfg *cleanupConfig, decision branchCleanupDecision) (bool, error) {
	switch decision.Action {
	case branchCleanupDelete:
		fmt.Fprintf(cfg.w, "deleting merged branch %s\n", decision.Branch)
		if _, err := cfg.runner.Run("git", "branch", "-d", decision.Branch); err != nil {
			fmt.Fprintf(cfg.w, "warning: delete branch %s: %v\n", decision.Branch, err)
			return false, fmt.Errorf("delete branch %s: %w", decision.Branch, err)
		}
		return true, nil
	case branchCleanupPreserve:
		printPreservedAgentBranch(cfg.w, decision)
		return false, decision.Err
	default:
		return false, nil
	}
}

func printPreservedAgentBranch(w io.Writer, decision branchCleanupDecision) {
	switch decision.Reason {
	case "checked_out":
		fmt.Fprintf(w, "preserving checked-out branch %s\n", decision.Branch)
	case "unmerged_unique_commits":
		fmt.Fprintf(w, "preserving unmerged branch %s (%d unique commit(s))\n", decision.Branch, decision.Ahead)
	default:
		fmt.Fprintf(w, "preserving uncertain branch %s: %s\n", decision.Branch, decision.Reason)
	}
}

func classifyAgentBranches(ctx context.Context, repoRoot string, runner CmdRunner) ([]branchCleanupDecision, error) {
	_ = repoRoot
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("check cleanup context: %w", err)
	}

	out, err := runner.Run("git", "branch", "--list", "agent/*")
	if err != nil {
		return nil, fmt.Errorf("list agent branches: %w", err)
	}
	branches := parseBranchNames(out)
	if len(branches) == 0 {
		return nil, nil
	}

	out, err = runner.Run("git", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}
	checkedOut := parseCheckedOutBranches(out)

	decisions := make([]branchCleanupDecision, 0, len(branches))
	var classifyErr error
	for _, branch := range branches {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("check cleanup context: %w", err)
		}
		if checkedOut[branch] {
			decisions = append(decisions, branchCleanupDecision{
				Branch: branch,
				Action: branchCleanupPreserve,
				Reason: "checked_out",
			})
			continue
		}

		decision, err := classifyAgentBranch(branch, runner)
		if err != nil {
			classifyErr = errors.Join(classifyErr, err)
		}
		decisions = append(decisions, decision)
	}

	return decisions, classifyErr
}

func classifyAgentBranch(branch string, runner CmdRunner) (branchCleanupDecision, error) {
	if _, err := runner.Run("git", "merge-base", "--is-ancestor", branch, "main"); err == nil {
		return branchCleanupDecision{
			Branch: branch,
			Action: branchCleanupDelete,
			Reason: "merged",
		}, nil
	}

	countOut, err := runner.Run("git", "rev-list", "--count", "main.."+branch)
	if err != nil {
		wrapped := fmt.Errorf("count unique commits for %s: %w", branch, err)
		return branchCleanupDecision{
			Branch: branch,
			Action: branchCleanupPreserve,
			Reason: "classification_error",
			Err:    wrapped,
		}, wrapped
	}
	ahead, err := strconv.Atoi(strings.TrimSpace(countOut))
	if err != nil {
		wrapped := fmt.Errorf("parse unique commit count for %s: %w", branch, err)
		return branchCleanupDecision{
			Branch: branch,
			Action: branchCleanupPreserve,
			Reason: "classification_error",
			Err:    wrapped,
		}, wrapped
	}
	return branchCleanupDecision{
		Branch: branch,
		Action: branchCleanupPreserve,
		Reason: "unmerged_unique_commits",
		Ahead:  ahead,
	}, nil
}

func parseCheckedOutBranches(output string) map[string]bool {
	branches := make(map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "branch refs/heads/") {
			continue
		}
		branch := strings.TrimPrefix(line, "branch refs/heads/")
		if branch != "" {
			branches[branch] = true
		}
	}
	return branches
}

// parseBranchNames parses branch names from git branch output (strips leading whitespace and *).
func parseBranchNames(output string) []string {
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "* ")
		if line == "" {
			continue
		}
		branches = append(branches, line)
	}
	return branches
}

// cleanupBeads resets in_progress beads back to open. Returns true if beads were reset.
func cleanupBeads(ctx context.Context, cfg *cleanupConfig) bool {
	if cfg.stateDBPath == "" {
		return false
	}

	db, err := openStateDB(cfg.stateDBPath)
	if err != nil {
		fmt.Fprintf(cfg.w, "warning: open bead store: %v\n", err)
		return false
	}
	defer func() { _ = db.Close() }()
	store := beadstore.NewSQLiteStore(db)

	beads, err := store.InProgress(ctx)
	if err != nil {
		fmt.Fprintf(cfg.w, "warning: list in_progress beads: %v\n", err)
		return false
	}

	if len(beads) == 0 {
		return false
	}

	cleaned := false
	for _, bead := range beads {
		if cleared, err := completeActiveAssignments(ctx, db, bead.ID); err != nil {
			fmt.Fprintf(cfg.w, "warning: clear active assignment for bead %s: %v\n", bead.ID, err)
		} else if cleared {
			cleaned = true
			fmt.Fprintf(cfg.w, "cleared active assignment for bead %s\n", bead.ID)
		}
		if bead.Status != "in_progress" {
			continue
		}
		fmt.Fprintf(cfg.w, "resetting bead %s to open\n", bead.ID)
		status := "open"
		if err := store.Update(ctx, bead.ID, beadstore.UpdateParams{Status: &status}); err != nil {
			fmt.Fprintf(cfg.w, "warning: reset bead %s: %v\n", bead.ID, err)
			continue
		}
		cleaned = true
	}
	return cleaned
}

func completeActiveAssignments(ctx context.Context, db *sql.DB, beadID string) (bool, error) {
	res, err := db.ExecContext(ctx,
		`UPDATE assignments SET status='completed', completed_at=datetime('now') WHERE bead_id=? AND status='active'`,
		beadID)
	if err != nil {
		return false, fmt.Errorf("complete active assignments: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read completed assignment count: %w", err)
	}
	return affected > 0, nil
}
