package dispatcher

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"oro/pkg/protocol"
)

// GitWorktreeManager is the production WorktreeManager that shells out
// to git to create and remove worktrees.
type GitWorktreeManager struct {
	repoRoot        string
	worktreesDir    string
	qualityGatePath string
	runner          CommandRunner
}

// NewGitWorktreeManager returns a WorktreeManager backed by real git commands.
// worktreesDir is the directory where worktrees are created; if empty it
// defaults to filepath.Join(repoRoot, ".worktrees").
// qualityGatePath is an optional path to a quality_gate.sh script that will be
// symlinked into new worktrees; pass empty string to disable.
func NewGitWorktreeManager(repoRoot, worktreesDir, qualityGatePath string, runner CommandRunner) *GitWorktreeManager {
	if worktreesDir == "" {
		worktreesDir = filepath.Join(repoRoot, ".worktrees")
	}
	return &GitWorktreeManager{
		repoRoot:        repoRoot,
		worktreesDir:    worktreesDir,
		qualityGatePath: qualityGatePath,
		runner:          runner,
	}
}

// Create runs `git worktree add <path> -b agent/<beadID> <baseBranch>` and returns
// the worktree path and branch name. baseBranch is the branch to branch from
// (e.g. "main" for standalone beads, "epic/<epicID>" for epic child beads).
//
// Before creating the worktree, Create performs a best-effort `git fetch origin
// <baseBranch>` so that the new agent branch always starts from the current
// remote HEAD, not a potentially-stale local ref. On success the worktree is
// branched from `origin/<baseBranch>`; if the fetch fails (e.g. no remote), epic
// base branches are created locally when missing and the local ref is used as a
// fallback.
func (g *GitWorktreeManager) Create(ctx context.Context, beadID, baseBranch string) (path, branch string, err error) {
	// Validate bead ID before using it in filepath operations to prevent
	// directory traversal attacks.
	if err := protocol.ValidateBeadID(beadID); err != nil {
		return "", "", fmt.Errorf("invalid bead ID: %w", err)
	}

	if baseBranch == "" {
		baseBranch = "main"
	}

	// Best-effort: fetch from origin so the worktree branches from the current
	// remote HEAD, not a potentially-stale local ref (govulncheck loop root cause).
	// On success use origin/<baseBranch>; fall back to the local ref if there is
	// no remote or the fetch fails (e.g. local-only repos, no network).
	effectiveBase := baseBranch
	_, fetchErr := g.runner.Run(ctx, "git", "-C", g.repoRoot, "fetch", "origin", baseBranch)
	if fetchErr == nil {
		effectiveBase = "origin/" + baseBranch
	} else if err := g.ensureLocalEpicBaseBranch(ctx, baseBranch); err != nil {
		return "", "", err
	}

	path = filepath.Join(g.worktreesDir, beadID)
	branch = protocol.BranchPrefix + beadID

	_, err = g.runner.Run(ctx, "git", "-C", g.repoRoot,
		"worktree", "add", path, "-b", branch, effectiveBase,
	)
	if err == nil {
		g.stageAssets(ctx, path)
		return path, branch, nil
	}

	// Non-recoverable error — fail immediately.
	if !strings.Contains(err.Error(), "already exists") {
		return "", "", fmt.Errorf("worktree add %s: %w", beadID, err)
	}

	// Branch already exists from a previous crashed run: prune stale state and retry once.
	if pruneErr := g.pruneStale(ctx, path, branch, effectiveBase); pruneErr != nil {
		slog.WarnContext(ctx, "worktree_create_prune_failed", "error", pruneErr.Error())
	}

	_, err = g.runner.Run(ctx, "git", "-C", g.repoRoot,
		"worktree", "add", path, "-b", branch, effectiveBase,
	)
	if err != nil {
		return "", "", fmt.Errorf("worktree add %s (after prune): %w", beadID, err)
	}
	g.stageAssets(ctx, path)
	return path, branch, nil
}

func (g *GitWorktreeManager) ensureLocalEpicBaseBranch(ctx context.Context, baseBranch string) error {
	if !strings.HasPrefix(baseBranch, protocol.EpicBranchPrefix) {
		return nil
	}
	exists, branchErr := g.BranchExists(ctx, baseBranch)
	if branchErr != nil {
		return branchErr
	}
	if exists {
		return nil
	}
	return g.CreateBranch(ctx, baseBranch, "main")
}

// stageAssets runs `make stage-assets` in the worktree to prepare embedded
// assets (skills, hooks, beacons) required by go:embed directives.
// Best-effort: failures are silently ignored since some worktrees may not
// need assets (e.g., some beads still compile without them).
func (g *GitWorktreeManager) stageAssets(ctx context.Context, path string) {
	_, _ = g.runner.Run(ctx, "make", "-C", path, "stage-assets")
	g.linkQualityGate(ctx, path)
}

// linkQualityGate creates a symlink at <worktreePath>/quality_gate.sh pointing
// to g.qualityGatePath, unless the worktree already has its own quality gate
// (scripts/quality_gate.sh or quality_gate.sh). It is a no-op when
// qualityGatePath is empty. If qualityGatePath does not exist on disk, a
// warning is logged and no symlink is created.
func (g *GitWorktreeManager) linkQualityGate(ctx context.Context, worktreePath string) {
	if g.qualityGatePath == "" {
		return
	}

	// Verify the target exists before creating a broken symlink.
	if _, err := os.Stat(g.qualityGatePath); err != nil {
		slog.WarnContext(ctx, "link_quality_gate_target_missing",
			"path", g.qualityGatePath, "error", err.Error())
		return
	}

	// Skip if the worktree already has scripts/quality_gate.sh.
	if _, err := os.Stat(filepath.Join(worktreePath, "scripts", "quality_gate.sh")); err == nil {
		return
	}

	// Skip if the worktree already has quality_gate.sh at root.
	linkPath := filepath.Join(worktreePath, "quality_gate.sh")
	if _, err := os.Lstat(linkPath); err == nil {
		return
	}

	if err := os.Symlink(g.qualityGatePath, linkPath); err != nil {
		slog.WarnContext(ctx, "link_quality_gate_symlink_failed",
			"link", linkPath, "target", g.qualityGatePath, "error", err.Error())
	}
}

// pruneStale cleans up stale git worktree metadata left by a previous crash.
// It removes the registered worktree, prunes stale git metadata, then asks git
// to safe-delete the branch. Unmerged branches are preserved by git branch -d.
// Returns the first non-nil error from any git step; all steps still run.
func (g *GitWorktreeManager) pruneStale(ctx context.Context, path, branch, targetBranch string) error {
	var firstErr error
	// Force-remove worktree reference (handles locked or stale worktrees).
	if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "worktree", "remove", path, "--force"); err != nil {
		firstErr = err
	}
	// Prune stale worktree metadata from git's internal tracking.
	if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "worktree", "prune"); err != nil && firstErr == nil {
		firstErr = err
	}
	// Delete the stale branch only when it is proven merged into the branch
	// Create is about to use as its base; git branch -d checks HEAD/upstream,
	// which is too broad for epic-targeted worktrees.
	if err := g.DeleteBranchMergedInto(ctx, branch, targetBranch); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// Remove runs `git worktree remove <path> --force`.
// If the worktree path does not exist, Remove returns nil (idempotent).
func (g *GitWorktreeManager) Remove(ctx context.Context, path string) error {
	// Check if path exists. If it doesn't, the worktree is already gone (idempotent).
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil // Path doesn't exist — already removed, not an error
		}
		// If Stat fails for a different reason, continue with git remove
		// (let git report the actual error).
	}

	_, err := g.runner.Run(ctx, "git", "-C", g.repoRoot,
		"worktree", "remove", path, "--force",
	)
	if err != nil {
		return fmt.Errorf("worktree remove %s: %w", path, err)
	}
	return nil
}

// DeleteBranch runs `git branch -d <branch>` to delete a merged branch.
// Uses -d (not -D) so git refuses if the branch is not fully merged — a safety net.
func (g *GitWorktreeManager) DeleteBranch(ctx context.Context, branch string) error {
	_, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "branch", "-d", branch)
	if err != nil {
		return fmt.Errorf("branch delete %s: %w", branch, err)
	}
	return nil
}

// DeleteBranchMergedInto proves branch is an ancestor of targetBranch before
// force-deleting it. Without the target-specific proof, force deletion could
// discard work that is not merged into the intended integration branch.
func (g *GitWorktreeManager) DeleteBranchMergedInto(ctx context.Context, branch, targetBranch string) error {
	if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "merge-base", "--is-ancestor", branch, targetBranch); err != nil {
		return fmt.Errorf("prove branch %s merged into %s: %w", branch, targetBranch, err)
	}
	return g.ForceDeleteBranch(ctx, branch)
}

// ForceDeleteBranch runs `git branch -D <branch>`.
// Callers must only use this after separately proving that deleting the branch
// cannot discard unmerged work.
func (g *GitWorktreeManager) ForceDeleteBranch(ctx context.Context, branch string) error {
	_, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "branch", "-D", branch)
	if err != nil {
		return fmt.Errorf("force branch delete %s: %w", branch, err)
	}
	return nil
}

// Exists reports whether the worktree at path is still present on disk.
func (g *GitWorktreeManager) Exists(_ context.Context, path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// CurrentBranch returns the branch checked out in path. Detached HEAD is
// returned as "HEAD", matching git rev-parse --abbrev-ref HEAD.
func (g *GitWorktreeManager) CurrentBranch(ctx context.Context, path string) (string, error) {
	out, err := g.runner.Run(ctx, "git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("worktree current branch %s: %w", path, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// PrepareExistingForReuse verifies that an already-preserved assignment
// worktree is safe to hand to another worker. If the agent branch has no unique
// commits and is only behind the assignment base, it fast-forwards the worktree
// after proving there are no tracked dirty changes. Untracked artifacts are
// preserved.
func (g *GitWorktreeManager) PrepareExistingForReuse(ctx context.Context, worktree, branch, baseBranch string) (bool, error) {
	if baseBranch == "" {
		baseBranch = "main"
	}
	branchHead, err := g.revParse(ctx, g.repoRoot, branch)
	if err != nil {
		return false, err
	}
	baseHead, err := g.revParse(ctx, g.repoRoot, baseBranch)
	if err != nil {
		return false, err
	}
	if branchHead == baseHead {
		return false, nil
	}

	branchBehind, err := g.isAncestor(ctx, branch, baseBranch)
	if err == nil && branchBehind {
		dirty, dirtyErr := g.trackedStatus(ctx, worktree)
		if dirtyErr != nil {
			return false, dirtyErr
		}
		if dirty != "" {
			return false, fmt.Errorf("stale branch %s is behind %s but worktree has tracked changes: %s", branch, baseBranch, dirty)
		}
		if _, mergeErr := g.runner.Run(ctx, "git", "-C", worktree, "merge", "--ff-only", baseBranch); mergeErr != nil {
			return false, fmt.Errorf("fast-forward existing worktree %s to %s: %w", worktree, baseBranch, mergeErr)
		}
		return true, nil
	}

	baseBehind, err := g.isAncestor(ctx, baseBranch, branch)
	if err == nil && baseBehind {
		return false, nil
	}
	return false, fmt.Errorf("agent branch %s diverged from base %s", branch, baseBranch)
}

// PrepareBaseBranchForAssignment refreshes an assignment base branch before a
// child worktree is reused. It only mutates branch when branch is strictly
// behind baseBranch; branches with unique commits are left untouched.
func (g *GitWorktreeManager) PrepareBaseBranchForAssignment(ctx context.Context, branch, baseBranch string) (bool, error) {
	if branch == "" || baseBranch == "" || branch == baseBranch {
		return false, nil
	}
	relation, err := g.branchRelationToBase(ctx, branch, baseBranch)
	if err != nil {
		return false, err
	}
	if relation != branchStrictlyBehind {
		return false, nil
	}
	if _, updateErr := g.runner.Run(ctx, "git", "-C", g.repoRoot, "branch", "-f", branch, baseBranch); updateErr != nil {
		return false, fmt.Errorf("fast-forward base branch %s to %s: %w", branch, baseBranch, updateErr)
	}
	return true, nil
}

// BaseBranchHasUniqueCommits reports whether branch contains work that is not
// reachable from baseBranch. The branch is left untouched.
func (g *GitWorktreeManager) BaseBranchHasUniqueCommits(ctx context.Context, branch, baseBranch string) (bool, error) {
	if branch == "" || baseBranch == "" || branch == baseBranch {
		return false, nil
	}
	relation, err := g.branchRelationToBase(ctx, branch, baseBranch)
	if err != nil {
		return false, err
	}
	return relation == branchContainsBase || relation == branchDiverged, nil
}

type branchBaseRelation int

const (
	branchSame branchBaseRelation = iota
	branchStrictlyBehind
	branchContainsBase
	branchDiverged
)

func (g *GitWorktreeManager) branchRelationToBase(ctx context.Context, branch, baseBranch string) (branchBaseRelation, error) {
	branchHead, err := g.revParse(ctx, g.repoRoot, branch)
	if err != nil {
		return branchDiverged, err
	}
	baseHead, err := g.revParse(ctx, g.repoRoot, baseBranch)
	if err != nil {
		return branchDiverged, err
	}
	if branchHead == baseHead {
		return branchSame, nil
	}

	branchBehind, err := g.isAncestorOrUnrelated(ctx, branch, baseBranch)
	if err != nil {
		return branchDiverged, err
	}
	if branchBehind {
		return branchStrictlyBehind, nil
	}

	baseBehind, err := g.isAncestorOrUnrelated(ctx, baseBranch, branch)
	if err != nil {
		return branchDiverged, err
	}
	if baseBehind {
		return branchContainsBase, nil
	}
	return branchDiverged, nil
}

func (g *GitWorktreeManager) isAncestorOrUnrelated(ctx context.Context, older, newer string) (bool, error) {
	ok, err := g.isAncestor(ctx, older, newer)
	if err == nil {
		return ok, nil
	}
	if isMergeBaseNotAncestor(err) {
		return false, nil
	}
	return false, err
}

func isMergeBaseNotAncestor(err error) bool {
	return err != nil && strings.Contains(err.Error(), "exit status 1")
}

func (g *GitWorktreeManager) revParse(ctx context.Context, dir, ref string) (string, error) {
	out, err := g.runner.Run(ctx, "git", "-C", dir, "rev-parse", ref)
	if err != nil {
		return "", fmt.Errorf("rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *GitWorktreeManager) isAncestor(ctx context.Context, older, newer string) (bool, error) {
	_, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "merge-base", "--is-ancestor", older, newer)
	if err == nil {
		return true, nil
	}
	return false, fmt.Errorf("merge-base --is-ancestor %s %s: %w", older, newer, err)
}

func (g *GitWorktreeManager) trackedStatus(ctx context.Context, worktree string) (string, error) {
	out, err := g.runner.Run(ctx, "git", "-C", worktree, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return "", fmt.Errorf("tracked status %s: %w", worktree, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// BranchExists reports whether the named branch exists in the local repository.
// Returns (false, nil) when the branch is simply absent — not found is not an error.
// Returns (false, err) only when git itself fails (e.g., not a git repo).
func (g *GitWorktreeManager) BranchExists(ctx context.Context, branch string) (bool, error) {
	out, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "branch", "--list", branch)
	if err != nil {
		return false, fmt.Errorf("branch exists check %s: %w", branch, err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// MergeFFOnly runs `git merge --ff-only <branch>` in the directory specified by
// target, then returns the resulting HEAD commit SHA. target is the filesystem
// path of the repository (or worktree) to run the merge in.
// If the merge cannot be fast-forwarded, the git error (including stderr) is
// returned wrapped.
func (g *GitWorktreeManager) MergeFFOnly(ctx context.Context, branch, target string) (commitSHA string, err error) {
	_, err = g.runner.Run(ctx, "git", "-C", target, "merge", "--ff-only", branch)
	if err != nil {
		return "", fmt.Errorf("ff-only merge of %s: %w", branch, err)
	}
	out, err := g.runner.Run(ctx, "git", "-C", target, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD after merge: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// UpdateBranchRef advances targetBranch to point at the tip of sourceBranch
// using `git update-ref`. This does not require sourceBranch to be checked out,
// making it suitable for advancing non-HEAD branches (e.g. an epic's parent branch).
func (g *GitWorktreeManager) UpdateBranchRef(ctx context.Context, targetBranch, sourceBranch string) error {
	_, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "update-ref",
		"refs/heads/"+targetBranch, sourceBranch)
	if err != nil {
		return fmt.Errorf("update-ref %s to %s: %w", targetBranch, sourceBranch, err)
	}
	return nil
}

// GCClosedWorktrees removes worktree directories and branches for beads that
// are closed. It calls isBeadClosed for each directory found under .worktrees/;
// entries for which isBeadClosed returns false are skipped conservatively.
// ReadDir failure returns nil (same as Prune). Remove failures are logged and
// do not prevent other entries from being processed.
func (g *GitWorktreeManager) GCClosedWorktrees(ctx context.Context, isBeadClosed func(string) bool) error {
	entries, err := os.ReadDir(g.worktreesDir)
	if err != nil {
		return nil //nolint:nilerr // missing dir is expected, not an error
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		beadID := entry.Name()
		if !isBeadClosed(beadID) {
			continue
		}

		path := filepath.Join(g.worktreesDir, beadID)
		branch := protocol.BranchPrefix + beadID

		if err := g.Remove(ctx, path); err != nil {
			slog.WarnContext(ctx, "gc_closed_worktrees_remove_failed", "bead_id", beadID, "error", err.Error())
			continue
		}

		if err := g.DeleteBranch(ctx, branch); err != nil {
			slog.WarnContext(ctx, "gc_closed_worktrees_branch_delete_failed", "bead_id", beadID, "error", err.Error())
		}
	}

	return nil
}

// Prune asks git to clean stale internal worktree bookkeeping.
// It intentionally does not remove directories under .worktrees/: after a
// crash, an unregistered directory can still contain recovery-owned work.
// Errors are logged but do not prevent startup.
func (g *GitWorktreeManager) Prune(ctx context.Context) error {
	if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "worktree", "prune"); err != nil {
		slog.WarnContext(ctx, "worktree_prune_failed", "error", err.Error())
	}

	return nil
}

// RebaseOnto checks out branch and rebases it onto onto.
func (g *GitWorktreeManager) RebaseOnto(ctx context.Context, branch, onto string) error {
	if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "checkout", branch); err != nil {
		return fmt.Errorf("checkout branch %s: %w", branch, err)
	}
	if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "rebase", onto); err != nil {
		return fmt.Errorf("rebase %s onto %s: %w", branch, onto, err)
	}
	return nil
}

// PushBranch pushes branch to origin using `git push origin branch`.
func (g *GitWorktreeManager) PushBranch(ctx context.Context, branch string) error {
	_, err := g.runner.Run(ctx, "git", "-C", g.repoRoot,
		"push", "origin", branch,
	)
	if err != nil {
		return fmt.Errorf("push branch %s: %w", branch, err)
	}
	return nil
}

// CreateBranch creates a new branch named `name` starting from `from` using
// `git branch <name> <from>`. If the branch already exists git returns a
// non-zero exit code; the caller is responsible for deciding whether that is
// an error.
func (g *GitWorktreeManager) CreateBranch(ctx context.Context, name, from string) error {
	out, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "branch", name, from)
	if err != nil {
		return fmt.Errorf("create branch %s from %s: %w (stdout: %s)", name, from, err, strings.TrimSpace(string(out)))
	}
	return nil
}
