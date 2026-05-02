package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"oro/pkg/beadstore"

	"github.com/spf13/cobra"
)

// cleanupConfig holds injectable dependencies for the cleanup command.
type cleanupConfig struct {
	runner       CmdRunner
	w            io.Writer
	tmuxName     string
	pidPath      string
	sockPath     string
	stateDBPath  string          // path to native SQLite state.db; empty disables bead state repair
	worktreesDir string          // path to .worktrees directory; empty disables worktree dir removal
	signalFn     func(int) error // sends SIGINT; injectable for testing
	aliveFn      func(int) bool  // checks process liveness; injectable for testing
	isTTY        func() bool     // returns true if stdin is a TTY; injectable for testing
}

// newCleanupCmd creates the "oro cleanup" subcommand.
func newCleanupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cleanup",
		Short: "Clean all stale state after a crash",
		Long: `Idempotently cleans up all Oro state: kills tmux session, dispatcher,
and worker processes; removes stale PID/socket files; prunes git worktrees;
deletes agent/* branches; and resets orphaned in_progress beads to open.

Safe to run anytime. If nothing is running, reports "nothing to clean".`,
		RunE: func(cmd *cobra.Command, args []string) error {
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

			cfg := &cleanupConfig{
				runner:       &ExecRunner{},
				w:            cmd.OutOrStdout(),
				tmuxName:     TmuxSessionName(readProjectNameCWD()),
				pidPath:      paths.PIDPath,
				sockPath:     paths.SocketPath,
				stateDBPath:  paths.StateDBPath,
				worktreesDir: projPaths.WorktreesDir,
				signalFn:     defaultSignalINT,
				aliveFn:      IsProcessAlive,
				isTTY:        isStdinTTY,
			}

			return runCleanup(cmd.Context(), cfg)
		},
	}
}

// runCleanup performs best-effort cleanup of all Oro state.
// Each step continues on error, reporting warnings. Uncertain branch cleanup
// errors are returned so callers can fail loudly instead of deleting unique work.
func runCleanup(ctx context.Context, cfg *cleanupConfig) error {
	if cfg.isTTY != nil && !cfg.isTTY() {
		return fmt.Errorf("oro cleanup requires an interactive terminal (stdin is not a TTY)")
	}

	cleaned := false
	var cleanupErr error

	// 1. Kill tmux session if it exists.
	if cleanedTmux := cleanupTmux(cfg); cleanedTmux {
		cleaned = true
	}

	// 2. Kill dispatcher process if running (read PID file).
	if cleanedDispatcher := cleanupDispatcher(cfg); cleanedDispatcher {
		cleaned = true
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

	// 6. Prune git worktrees.
	cleanupWorktrees(cfg)

	// 7. Remove .worktrees/ directory.
	if cleanedWorktreeDir := cleanupWorktreeDir(cfg); cleanedWorktreeDir {
		cleaned = true
	}

	// 8. Delete agent/* and epic/* branches.
	cleanedBranches, err := cleanupAgentBranches(ctx, cfg)
	if cleanedBranches {
		cleaned = true
	}
	if err != nil {
		cleanupErr = err
	}

	// 9. Reset in_progress beads back to open.
	if cleanedBeads := cleanupBeads(ctx, cfg); cleanedBeads {
		cleaned = true
	}

	if !cleaned {
		fmt.Fprintln(cfg.w, "nothing to clean")
	}

	return cleanupErr
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

// cleanupDispatcher signals the dispatcher process if running. Returns true if something was cleaned.
// Sends SIGINT (always honored by daemon) for graceful shutdown.
// Falls back to socket probe when PID file is missing.
func cleanupDispatcher(cfg *cleanupConfig) bool {
	pid, err := ReadPIDFile(cfg.pidPath)
	if err != nil {
		// No PID file — try socket probe to discover PID.
		pid = probeSocket(cfg.sockPath)
		if pid == 0 {
			return false
		}
	}

	if !cfg.aliveFn(pid) {
		// Process is dead, PID file is stale — will be cleaned in step 4.
		return false
	}

	fmt.Fprintf(cfg.w, "killing dispatcher (PID %d)\n", pid)
	if err := cfg.signalFn(pid); err != nil {
		fmt.Fprintf(cfg.w, "warning: signal dispatcher PID %d: %v\n", pid, err)
	}
	return true
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
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}
