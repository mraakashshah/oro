// Package merge implements a merge coordinator for serialized rebase-merge
// operations. It provides a lock-protected Coordinator that performs
// sequential rebase-merge against main, with conflict detection and abort.
//
// This is a library package consumed by the Dispatcher binary. The
// Coordinator handles rebase + merge (or abort on conflict). Delegation
// and escalation are the Dispatcher's responsibility.
package merge

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// effectiveTarget returns the branch to merge into: Opts.TargetBranch if set,
// otherwise "main".
func effectiveTarget(opts Opts) string {
	if opts.TargetBranch != "" {
		return opts.TargetBranch
	}
	return "main"
}

// GitRunner abstracts git command execution for testability.
type GitRunner interface {
	Run(ctx context.Context, dir string, args ...string) (stdout string, stderr string, err error)
}

// WorktreeRemover abstracts worktree removal for testability.
// The production implementation calls "git worktree remove <path>" or os.RemoveAll.
// Tests inject a mock that records Remove calls.
type WorktreeRemover interface {
	Remove(path string) error
}

// Opts holds parameters for a single merge operation.
type Opts struct {
	Branch       string // branch to merge (e.g., "agent/abc")
	Worktree     string // path to the worktree
	BeadID       string // for logging/context
	TargetBranch string // branch to merge into; empty defaults to "main"
}

// Result holds the outcome of a successful merge.
type Result struct {
	CommitSHA string
}

// ConflictError is returned when a rebase encounters merge conflicts.
// The caller (Dispatcher) decides what to do: delegate to ops agent or
// escalate to Manager.
type ConflictError struct {
	Files  []string // files with conflicts
	BeadID string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("merge conflict on bead %s: conflicting files: %s",
		e.BeadID, strings.Join(e.Files, ", "))
}

// Coordinator implements two-level locking for merge operations:
//
//   - Level 1 (rebaseLocks): per-target branch mutex. Allows concurrent
//     rebases to different targets while serializing rebases to the same target.
//
//   - Level 2 (ffLock): global mutex. Serializes all FF merges regardless of
//     target, preventing races where main moves between rebase and FF.
//
// activeWorktrees maps targetBranch → worktreePath for the duration of the
// rebase phase, enabling targeted abort via Abort(targetBranch).
type Coordinator struct {
	git GitRunner

	// worktreeRemover is called to remove the agent worktree after a successful
	// rebase. If nil, falls back to "git worktree remove <path>" via GitRunner.
	worktreeRemover WorktreeRemover

	// rebaseLocks stores *sync.Mutex values keyed by targetBranch.
	// Loaded-or-stored atomically so concurrent goroutines share the same lock.
	rebaseLocks sync.Map

	// ffLock serializes the FF merge step globally.
	ffLock sync.Mutex

	// activeWorktrees maps targetBranch → worktreePath for the rebase phase.
	activeWorktrees sync.Map
}

// NewCoordinator creates a Coordinator with the given GitRunner.
func NewCoordinator(git GitRunner) *Coordinator {
	return &Coordinator{git: git}
}

// getOrCreateRebaseLock returns the per-target rebase mutex, creating it if needed.
// LoadOrStore ensures concurrent callers for the same target share the same mutex.
func (c *Coordinator) getOrCreateRebaseLock(target string) *sync.Mutex {
	mu := &sync.Mutex{}
	actual, _ := c.rebaseLocks.LoadOrStore(target, mu)
	if v, ok := actual.(*sync.Mutex); ok {
		return v
	}
	return mu // unreachable: only *sync.Mutex stored in rebaseLocks
}

// Merge performs a rebase-merge using two-level locking:
//
//  1. Level-1 (per-target rebaseLocks): serializes merges to the same target;
//     merges to different targets run their rebase phases in parallel.
//  2. Level-2 (global ffLock): serializes all FF merges regardless of target.
//
// Merge flow:
//  1. Acquire per-target rebase lock.
//  2. Register worktree in activeWorktrees for abort support.
//  3. git rebase <target> <branch> (in worktree)
//  4. If conflict: leave rebase in-progress, return *ConflictError (ops agent resolves via --continue).
//  5. Deregister from activeWorktrees (rebase done; abort no longer applicable).
//  6. Acquire global ffLock.
//  7. Remove agent worktree + git merge --ff-only <branch> (in primary repo).
//
// This approach produces identical commit hashes on main as on the branch
// (no cherry-pick hash mismatch). It also avoids "git checkout main" which
// fails when main is already checked out in the primary worktree.
func (c *Coordinator) Merge(ctx context.Context, opts Opts) (*Result, error) {
	target := effectiveTarget(opts)

	// Level 1: Acquire per-target rebase lock.
	targetMu := c.getOrCreateRebaseLock(target)
	targetMu.Lock()
	defer targetMu.Unlock()

	// Register worktree for abort support during the rebase phase.
	c.activeWorktrees.Store(target, opts.Worktree)
	defer c.activeWorktrees.Delete(target)

	// Step 0: Check if branch is already merged (agent may have merged inside worktree).
	alreadyMerged, sha, checkErr := c.isBranchMerged(ctx, opts)
	if checkErr == nil && alreadyMerged {
		return &Result{CommitSHA: sha}, nil
	}

	// Step 1: Rebase branch onto target.
	stdout, stderr, err := c.git.Run(ctx, opts.Worktree, "rebase", target, opts.Branch)
	if err != nil {
		// Context cancelled/deadline exceeded takes priority over conflict handling.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("merge cancelled: %w", ctx.Err())
		}
		return nil, c.handleRebaseFailure(ctx, opts, target, stdout, stderr, err)
	}

	// Rebase done — deregister from activeWorktrees (abort no longer applicable).
	c.activeWorktrees.Delete(target)

	// Level 2: Acquire global FF merge lock.
	c.ffLock.Lock()
	defer c.ffLock.Unlock()

	// Steps 2-4: Remove worktree, ff-merge branch onto target in primary repo.
	return c.worktreeRemoveAndFFMerge(ctx, opts)
}

// worktreeRemoveAndFFMerge fast-forward merges the rebased branch onto main,
// then removes the agent worktree.
//
// This preserves commit hashes — no cherry-pick rewrite occurs.
//
// If ff-only fails (main moved between rebase and ffLock acquisition), the
// worktree is still alive, so we re-rebase the branch and retry. Since we hold
// ffLock, main cannot move during the retry. This prevents the dispatcher
// assignment spam loop (oro-mz9v).
func (c *Coordinator) worktreeRemoveAndFFMerge(ctx context.Context, opts Opts) (*Result, error) {
	// Derive the primary repository path from the worktree's git common dir.
	commonDir, _, err := c.git.Run(ctx, opts.Worktree, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("failed to get git common dir: %w", err)
	}
	commonDir = strings.TrimSpace(commonDir)

	primaryRepo := strings.TrimSuffix(strings.TrimRight(commonDir, "/"), "/.git")
	if primaryRepo == commonDir {
		primaryRepo, _, err = c.git.Run(ctx, opts.Worktree, "rev-parse", "--show-toplevel")
		if err != nil {
			return nil, fmt.Errorf("failed to get primary repo path: %w", err)
		}
		primaryRepo = strings.TrimSpace(primaryRepo)
	}

	// Try ff-only merge BEFORE removing the worktree so we can retry on failure.
	_, _, err = c.git.Run(ctx, primaryRepo, "merge", "--ff-only", opts.Branch)
	if err != nil {
		// Primary repo HEAD moved between the rebase (under rebaseLock) and here
		// (under ffLock). Re-rebase the branch onto the primary repo's CURRENT HEAD —
		// not effectiveTarget(opts), which may be a stale epic branch that is now
		// behind the primary HEAD. We hold ffLock, so the HEAD cannot move again.
		currentHead, _, headErr := c.git.Run(ctx, primaryRepo, "rev-parse", "HEAD")
		if headErr != nil {
			return nil, fmt.Errorf("rev-parse HEAD for retry base: %w", headErr)
		}
		retryBase := strings.TrimSpace(currentHead)
		stdout, stderr, rebaseErr := c.git.Run(ctx, opts.Worktree, "rebase", retryBase, opts.Branch)
		if rebaseErr != nil {
			return nil, c.handleRebaseFailure(ctx, opts, retryBase, stdout, stderr, rebaseErr)
		}
		_, _, err = c.git.Run(ctx, primaryRepo, "merge", "--ff-only", opts.Branch)
		if err != nil {
			return nil, fmt.Errorf("ff-only merge of %s failed after rebase retry: %w", opts.Branch, err)
		}
	}

	// Merge succeeded — remove the worktree.
	if removeErr := c.removeWorktree(ctx, primaryRepo, opts.Worktree); removeErr != nil {
		return nil, fmt.Errorf("worktree remove failed (branch %s merged but worktree lingers): %w", opts.Branch, removeErr)
	}

	// Get the final commit SHA on main.
	stdout, _, err := c.git.Run(ctx, primaryRepo, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("rev-parse HEAD failed: %w", err)
	}
	return &Result{CommitSHA: strings.TrimSpace(stdout)}, nil
}

// removeWorktree removes the agent worktree. If a WorktreeRemover is configured,
// it delegates to that; otherwise falls back to "git worktree remove <path>" via
// the GitRunner (executed in the primary repo).
func (c *Coordinator) removeWorktree(ctx context.Context, primaryRepo, worktreePath string) error {
	if c.worktreeRemover != nil {
		if err := c.worktreeRemover.Remove(worktreePath); err != nil {
			return fmt.Errorf("worktree remover: %w", err)
		}
		return nil
	}
	// Fallback: use "git worktree remove --force" via the GitRunner.
	// --force ensures untracked files (e.g. .tmp artifacts left by workers)
	// do not block cleanup.
	_, _, err := c.git.Run(ctx, primaryRepo, "worktree", "remove", "--force", worktreePath)
	if err != nil {
		return fmt.Errorf("git worktree remove: %w", err)
	}
	return nil
}

// isBranchMerged checks if all commits on branch are already reachable from target.
// This handles the case where an agent merged to target inside the worktree.
func (c *Coordinator) isBranchMerged(ctx context.Context, opts Opts) (merged bool, commitSHA string, err error) {
	target := effectiveTarget(opts)
	// Check if branch has any commits not on target.
	out, _, err := c.git.Run(ctx, opts.Worktree, "rev-list", "--count", target+".."+opts.Branch)
	if err != nil {
		return false, "", fmt.Errorf("rev-list --count failed: %w", err)
	}
	if strings.TrimSpace(out) != "0" {
		return false, "", nil
	}
	// Verify no uncommitted diff between target and branch (fail-open: diff error → not merged).
	diffOut, _, diffErr := c.git.Run(ctx, opts.Worktree, "diff", target+".."+opts.Branch)
	if diffErr != nil {
		return false, "", nil //nolint:nilerr // fail-open: diff error means proceed to rebase
	}
	if strings.TrimSpace(diffOut) != "" {
		return false, "", nil
	}
	// Branch is fully merged — return target HEAD as the merge commit.
	sha, _, err := c.git.Run(ctx, opts.Worktree, "rev-parse", target)
	if err != nil {
		return false, "", fmt.Errorf("rev-parse %s failed: %w", target, err)
	}
	return true, strings.TrimSpace(sha), nil
}

// handleRebaseFailure returns a ConflictError only when git left unmerged
// paths behind. The rebase is intentionally left in-progress for real
// conflicts so the ops merge agent can resolve them and run git rebase
// --continue. Non-conflict rebase errors must not be routed through the
// merge-conflict loop.
func (c *Coordinator) handleRebaseFailure(ctx context.Context, opts Opts, target, rebaseStdout, rebaseStderr string, cause error) error {
	output := strings.TrimSpace(rebaseStdout + "\n" + rebaseStderr)
	files := parseConflictFiles(output)
	if len(files) == 0 {
		unmerged, _, err := c.git.Run(ctx, opts.Worktree, "diff", "--name-only", "--diff-filter=U")
		if err == nil {
			files = parseConflictFileList(unmerged)
		}
	}
	if len(files) > 0 {
		return &ConflictError{
			Files:  files,
			BeadID: opts.BeadID,
		}
	}
	if output == "" {
		return fmt.Errorf("rebase %s onto %s failed without unmerged paths: %w", opts.Branch, target, cause)
	}
	return fmt.Errorf("rebase %s onto %s failed without unmerged paths: %w: %s", opts.Branch, target, cause, output)
}

// Abort runs best-effort 'git rebase --abort' on the worktree currently rebasing
// onto targetBranch. Returns nil if no merge is active for that target (no-op).
// Safe to call concurrently with Merge — reads activeWorktrees without locking.
// Uses a fresh context (the caller's context is typically cancelled at shutdown).
//
//oro:testonly
func (c *Coordinator) Abort(targetBranch string) error {
	wtVal, ok := c.activeWorktrees.Load(targetBranch)
	if !ok {
		return nil
	}
	wt, ok := wtVal.(string)
	if !ok {
		return nil // unreachable: only string stored in activeWorktrees
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _ = c.git.Run(ctx, wt, "rebase", "--abort")
	return nil
}

// AbortAll runs best-effort 'git rebase --abort' on all in-progress merge worktrees.
// Returns nil when no merges are active (no-op). Safe to call concurrently with Merge.
// Uses a fresh context (the caller's context is typically cancelled at shutdown).
func (c *Coordinator) AbortAll() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.activeWorktrees.Range(func(_, value any) bool {
		if wt, ok := value.(string); ok {
			_, _, _ = c.git.Run(ctx, wt, "rebase", "--abort")
		}
		return true
	})
	return nil
}

// conflictPattern matches git's CONFLICT output lines.
// Examples:
//
//	CONFLICT (content): Merge conflict in src/main.go
//	CONFLICT (add/add): Merge conflict in new_file.go
var conflictPattern = regexp.MustCompile(`CONFLICT \([^)]+\): Merge conflict in (.+)`)

// parseConflictFiles extracts file paths from git rebase stderr output.
func parseConflictFiles(stderr string) []string {
	matches := conflictPattern.FindAllStringSubmatch(stderr, -1)
	if len(matches) == 0 {
		return nil
	}
	files := make([]string, 0, len(matches))
	for _, m := range matches {
		files = append(files, strings.TrimSpace(m[1]))
	}
	return files
}

func parseConflictFileList(stdout string) []string {
	var files []string
	for _, line := range strings.Split(stdout, "\n") {
		file := strings.TrimSpace(line)
		if file != "" {
			files = append(files, file)
		}
	}
	return files
}
