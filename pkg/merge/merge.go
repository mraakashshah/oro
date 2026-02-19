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
	Branch   string // branch to merge (e.g., "bead/abc")
	Worktree string // path to the worktree
	BeadID   string // for logging/context
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

// Coordinator serializes merge operations behind a mutex so only one
// merge runs at a time. This prevents the FF-only merge races observed
// in BCR (main moves during agent rebase).
type Coordinator struct {
	mu  sync.Mutex
	git GitRunner

	// worktreeRemover is called to remove the agent worktree after a successful
	// rebase. If nil, falls back to "git worktree remove <path>" via GitRunner.
	worktreeRemover WorktreeRemover

	// abortMu protects activeWorktree for concurrent access from Abort().
	abortMu        sync.Mutex
	activeWorktree string // non-empty while a merge is in progress
}

// NewCoordinator creates a Coordinator with the given GitRunner.
func NewCoordinator(git GitRunner) *Coordinator {
	return &Coordinator{git: git}
}

// Merge performs a sequential rebase-merge using worktree-remove + ff-merge:
//  1. git rebase main <branch> (in worktree)
//  2. If clean: remove the agent worktree
//  3. git merge --ff-only <branch> (in primary repo)
//  4. If conflict: git rebase --abort, return *ConflictError
//
// This approach produces identical commit hashes on main as on the branch
// (no cherry-pick hash mismatch). It also avoids "git checkout main" which
// fails when main is already checked out in the primary worktree.
//
// Only one Merge runs at a time (mutex-protected).
func (c *Coordinator) Merge(ctx context.Context, opts Opts) (*Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	func() {
		c.abortMu.Lock()
		defer c.abortMu.Unlock()
		c.activeWorktree = opts.Worktree
	}()
	defer func() {
		c.abortMu.Lock()
		defer c.abortMu.Unlock()
		c.activeWorktree = ""
	}()

	// Step 0: Check if branch is already merged (agent may have merged inside worktree).
	alreadyMerged, sha, checkErr := c.isBranchMerged(ctx, opts)
	if checkErr == nil && alreadyMerged {
		return &Result{CommitSHA: sha}, nil
	}

	// Step 1: Rebase branch onto main
	_, stderr, err := c.git.Run(ctx, opts.Worktree, "rebase", "main", opts.Branch)
	if err != nil {
		// Context cancelled/deadline exceeded takes priority over conflict handling
		if ctx.Err() != nil {
			return nil, fmt.Errorf("merge cancelled: %w", ctx.Err())
		}
		// Rebase failed — abort and return conflict error
		return nil, c.handleRebaseFailure(ctx, opts, stderr)
	}

	// Steps 2-4: Remove worktree, ff-merge branch onto main in primary repo
	return c.worktreeRemoveAndFFMerge(ctx, opts)
}

// worktreeRemoveAndFFMerge removes the agent worktree and fast-forward merges
// the rebased branch onto main in the primary repository.
//
// This preserves commit hashes — no cherry-pick rewrite occurs.
// Edge cases:
//   - worktree dirty after rebase → Remove fails → return error with guidance
//   - ff-only fails (main moved) → return error; branch still exists, caller can retry
func (c *Coordinator) worktreeRemoveAndFFMerge(ctx context.Context, opts Opts) (*Result, error) {
	// Derive the primary repository path from the worktree's git common dir.
	// --git-common-dir returns the shared .git dir (e.g., "/repo/.git").
	// We derive the primary repo by stripping the "/.git" suffix.
	commonDir, _, err := c.git.Run(ctx, opts.Worktree, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("failed to get git common dir: %w", err)
	}
	commonDir = strings.TrimSpace(commonDir)

	primaryRepo := strings.TrimSuffix(strings.TrimRight(commonDir, "/"), "/.git")
	if primaryRepo == commonDir {
		// Fallback: commonDir didn't end with /.git — ask the worktree instead.
		primaryRepo, _, err = c.git.Run(ctx, opts.Worktree, "rev-parse", "--show-toplevel")
		if err != nil {
			return nil, fmt.Errorf("failed to get primary repo path: %w", err)
		}
		primaryRepo = strings.TrimSpace(primaryRepo)
	}

	// Remove the agent worktree. After this point the worktree directory is gone.
	if removeErr := c.removeWorktree(ctx, primaryRepo, opts.Worktree); removeErr != nil {
		return nil, fmt.Errorf("worktree remove failed (branch %s still intact): %w", opts.Branch, removeErr)
	}

	// Fast-forward merge the rebased branch onto main in the primary repo.
	// This is the key difference from cherry-pick: the same commits land on main
	// with identical SHAs.
	_, _, err = c.git.Run(ctx, primaryRepo, "merge", "--ff-only", opts.Branch)
	if err != nil {
		return nil, fmt.Errorf("ff-only merge of %s failed (main may have moved; retry rebase): %w", opts.Branch, err)
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
	// Fallback: use "git worktree remove" via the GitRunner.
	_, _, err := c.git.Run(ctx, primaryRepo, "worktree", "remove", worktreePath)
	if err != nil {
		return fmt.Errorf("git worktree remove: %w", err)
	}
	return nil
}

// isBranchMerged checks if all commits on branch are already reachable from main.
// This handles the case where an agent merged to main inside the worktree.
func (c *Coordinator) isBranchMerged(ctx context.Context, opts Opts) (merged bool, commitSHA string, err error) {
	// Check if branch has any commits not on main.
	out, _, err := c.git.Run(ctx, opts.Worktree, "rev-list", "--count", "main.."+opts.Branch)
	if err != nil {
		return false, "", fmt.Errorf("rev-list --count failed: %w", err)
	}
	if strings.TrimSpace(out) != "0" {
		return false, "", nil
	}
	// Verify no uncommitted diff between main and branch (fail-open: diff error → not merged).
	diffOut, _, diffErr := c.git.Run(ctx, opts.Worktree, "diff", "main.."+opts.Branch)
	if diffErr != nil {
		return false, "", nil //nolint:nilerr // fail-open: diff error means proceed to rebase
	}
	if strings.TrimSpace(diffOut) != "" {
		return false, "", nil
	}
	// Branch is fully merged — return main HEAD as the merge commit.
	sha, _, err := c.git.Run(ctx, opts.Worktree, "rev-parse", "main")
	if err != nil {
		return false, "", fmt.Errorf("rev-parse main failed: %w", err)
	}
	return true, strings.TrimSpace(sha), nil
}

// handleRebaseFailure aborts the in-progress rebase and returns a ConflictError
// with the parsed conflicting file paths.
func (c *Coordinator) handleRebaseFailure(ctx context.Context, opts Opts, rebaseStderr string) error {
	// Best-effort abort — even if this fails, we still return the conflict error.
	_, _, _ = c.git.Run(ctx, opts.Worktree, "rebase", "--abort")

	files := parseConflictFiles(rebaseStderr)
	return &ConflictError{
		Files:  files,
		BeadID: opts.BeadID,
	}
}

// Abort runs best-effort 'git rebase --abort' on any in-progress merge worktree.
// Safe to call concurrently with Merge — uses a separate lock and a fresh
// context (since the caller's context is typically cancelled at shutdown time).
func (c *Coordinator) Abort() {
	var wt string
	func() {
		c.abortMu.Lock()
		defer c.abortMu.Unlock()
		wt = c.activeWorktree
	}()

	if wt == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _ = c.git.Run(ctx, wt, "rebase", "--abort")
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
