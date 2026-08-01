package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"oro/pkg/protocol"
)

// WorktreeManager creates and removes git worktrees.
type WorktreeManager interface {
	Create(ctx context.Context, beadID, baseBranch string) (path string, branch string, err error)
	Remove(ctx context.Context, path string) error
	Prune(ctx context.Context) error
	DeleteBranch(ctx context.Context, branch string) error
	DeleteBranchMergedInto(ctx context.Context, branch, targetBranch string) error
	ForceDeleteBranch(ctx context.Context, branch string) error
	BranchExists(ctx context.Context, branch string) (bool, error)
	MergeFFOnly(ctx context.Context, branch string, target string) (commitSHA string, err error)
	// UpdateBranchRef advances targetBranch to point at the tip of sourceBranch
	// without requiring sourceBranch to be checked out. Used when the target is
	// not the HEAD branch (i.e., not the branch checked out in the main worktree).
	UpdateBranchRef(ctx context.Context, targetBranch, sourceBranch string) error
	BranchHead(ctx context.Context, branch string) (string, error)
	GCClosedWorktrees(ctx context.Context, isBeadClosed func(string) bool) error
	// Exists reports whether the worktree at path is still present on disk.
	// Returns false if the path does not exist or cannot be accessed.
	Exists(ctx context.Context, path string) bool
	// CurrentBranch reports the branch checked out in the worktree.
	CurrentBranch(ctx context.Context, path string) (string, error)
	// RebaseOnto rebases branch onto onto using git rebase --onto.
	RebaseOnto(ctx context.Context, branch, onto string) error
	// PushBranch pushes branch to origin.
	PushBranch(ctx context.Context, branch string) error
	// CreateBranch creates a new branch named `name` starting from `from`.
	// If the branch already exists git returns a non-zero exit code; the
	// caller is responsible for deciding whether that is an error.
	CreateBranch(ctx context.Context, name string, from string) error
}

type existingWorktreeReusePreparer interface {
	PrepareExistingForReuse(ctx context.Context, worktree, branch, baseBranch string) (fastForwarded bool, err error)
}

type existingWorktreeDivergedRebaser interface {
	RebaseDivergedExistingForReuse(ctx context.Context, worktree, branch, baseBranch string) error
}

type assignmentBaseBranchPreparer interface {
	PrepareBaseBranchForAssignment(ctx context.Context, branch, baseBranch string) (fastForwarded bool, err error)
}

type assignmentBaseBranchSafetyChecker interface {
	BaseBranchHasUniqueCommits(ctx context.Context, branch, baseBranch string) (bool, error)
}

// epicPreserveOutcome is the result of a deterministic epic-ancestry preserve
// merge. On any error the caller falls back regardless of outcome.
type epicPreserveOutcome int

const (
	// epicPreserveNoop means target's tip is already an ancestor of the epic
	// branch: nothing to do.
	epicPreserveNoop epicPreserveOutcome = iota
	// epicPreserveMerged means a new preserve commit was created and the epic
	// ref advanced to it via compare-and-swap.
	epicPreserveMerged
	// epicPreserveConflict means the merge could not be computed without a
	// content conflict; the caller must fall back to LLM recovery.
	epicPreserveConflict
)

// epicMergePreserver deterministically preserves both target and epic ancestry
// on the epic branch without an LLM worker or a checked-out worktree.
// Implemented by *GitWorktreeManager; worktree managers that do not implement
// it cause the dispatcher to fall back to ensureEpicRebaseChild.
type epicMergePreserver interface {
	// preserveEpicAncestry merges target into epicBranch so that both the epic
	// branch's current tip and target become ancestors of the epic branch,
	// advancing the epic ref transactionally (compare-and-swap). It never
	// checks out a worktree. Returns the new epic tip on epicPreserveMerged
	// (or the unchanged tip on epicPreserveNoop). Any failure before the ref
	// mutation leaves all refs untouched.
	preserveEpicAncestry(ctx context.Context, epicBranch, target string) (epicPreserveOutcome, string, error)
	// rollbackEpicPreserve reverts a preserve merge that failed post-merge
	// verification (e.g. the quality gate), advancing epicBranch from newOID
	// back to oldOID via compare-and-swap. It fails without mutating the ref
	// if epicBranch no longer points at newOID.
	rollbackEpicPreserve(ctx context.Context, epicBranch, oldOID, newOID string) error
}

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
// copied into new worktrees; pass empty string to disable.
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

// ManagedQualityGatePath returns the dispatcher-managed quality gate target
// linked into worker worktrees.
func (g *GitWorktreeManager) ManagedQualityGatePath() string {
	return g.qualityGatePath
}

// Create runs `git worktree add <path> -b agent/<beadID> <baseBranch>` and returns
// the worktree path and branch name. baseBranch is the branch to branch from
// (e.g. "main" for standalone beads, "epic/<epicID>" for epic child beads).
//
// Before creating the worktree, Create performs a best-effort `git fetch origin
// <baseBranch>`, then selects the fetched remote when no local ref exists or
// whichever ref is the descendant when both exist. Divergent refs fail closed.
// If the fetch fails (e.g. no remote), epic base branches are created locally
// when missing and the local ref is used as a fallback.
func (g *GitWorktreeManager) Create(ctx context.Context, beadID, baseBranch string) (path, branch string, err error) {
	// Validate bead ID before using it in filepath operations to prevent
	// directory traversal attacks.
	if err := protocol.ValidateBeadID(beadID); err != nil {
		return "", "", fmt.Errorf("invalid bead ID: %w", err)
	}

	if baseBranch == "" {
		baseBranch = "main"
	}

	// Best-effort: fetch from origin so the worktree can branch from the freshest
	// safe ref. If local and remote disagree, prefer the descendant and refuse
	// divergent histories rather than starting a worker from a stale target.
	// Fall back to the local ref if there is no remote or fetch fails (e.g.
	// local-only repos, no network).
	effectiveBase := baseBranch
	_, fetchErr := g.runner.Run(ctx, "git", "-C", g.repoRoot, "fetch", "origin", baseBranch)
	if fetchErr == nil {
		selectedBase, selectErr := g.selectFreshBase(ctx, baseBranch, "origin/"+baseBranch)
		if selectErr != nil {
			return "", "", selectErr
		}
		effectiveBase = selectedBase
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
	pruneFailed := false
	if pruneErr := g.pruneStale(ctx, path, branch, effectiveBase); pruneErr != nil {
		pruneFailed = true
		slog.WarnContext(ctx, "worktree_create_prune_failed", "error", pruneErr.Error())
	}

	if err := g.retryCreateAfterPrune(ctx, path, branch, effectiveBase, pruneFailed); err != nil {
		return "", "", fmt.Errorf("worktree add %s (after prune): %w", beadID, err)
	}
	g.stageAssets(ctx, path)
	return path, branch, nil
}

func (g *GitWorktreeManager) selectFreshBase(ctx context.Context, localBase, remoteBase string) (string, error) {
	relation, err := g.branchRelationToBase(ctx, localBase, remoteBase)
	if err != nil {
		return g.selectFetchedRemoteIfLocalMissing(ctx, localBase, remoteBase, err)
	}
	switch relation {
	case branchStrictlyBehind:
		return remoteBase, nil
	case branchDiverged:
		return "", fmt.Errorf("local base %s and %s diverged", localBase, remoteBase)
	default:
		return localBase, nil
	}
}

func (g *GitWorktreeManager) selectFetchedRemoteIfLocalMissing(
	ctx context.Context, localBase, remoteBase string, compareErr error,
) (string, error) {
	localExists, err := g.BranchExists(ctx, localBase)
	if err != nil {
		return "", fmt.Errorf("check local base %s after comparison failure: %w", localBase, err)
	}
	if localExists {
		return "", fmt.Errorf("compare local base %s with %s: %w", localBase, remoteBase, compareErr)
	}
	if _, err := g.revParse(ctx, g.repoRoot, remoteBase); err != nil {
		return "", fmt.Errorf("verify remote base %s: %w", remoteBase, err)
	}
	return remoteBase, nil
}

func (g *GitWorktreeManager) retryCreateAfterPrune(ctx context.Context, path, branch, effectiveBase string, pruneFailed bool) error {
	if !pruneFailed {
		_, err := g.runner.Run(ctx, "git", "-C", g.repoRoot,
			"worktree", "add", path, "-b", branch, effectiveBase,
		)
		if err != nil {
			return fmt.Errorf("retry create new branch: %w", err)
		}
		return nil
	}

	branchExists, err := g.BranchExists(ctx, branch)
	if err != nil {
		return fmt.Errorf("branch check: %w", err)
	}
	if branchExists {
		_, err = g.runner.Run(ctx, "git", "-C", g.repoRoot,
			"worktree", "add", path, branch,
		)
		if err != nil {
			return fmt.Errorf("retry existing branch: %w", err)
		}
		return nil
	}
	_, err = g.runner.Run(ctx, "git", "-C", g.repoRoot,
		"worktree", "add", path, "-b", branch, effectiveBase,
	)
	if err != nil {
		return fmt.Errorf("retry create branch after prune: %w", err)
	}
	return nil
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

// linkQualityGate creates a dispatcher-managed snapshot at
// <worktreePath>/quality_gate.sh copied from g.qualityGatePath. This root
// script intentionally coexists with a tracked scripts/quality_gate.sh so old
// epic branches cannot bypass current factory safety fixes.
func (g *GitWorktreeManager) linkQualityGate(ctx context.Context, worktreePath string) {
	g.RefreshQualityGate(ctx, worktreePath)
}

// RefreshQualityGate replaces the dispatcher-managed root quality gate with a
// current executable snapshot. It is intentionally best-effort for newly
// created worktrees; reuse calls refreshQualityGate directly so it can fail
// closed rather than handing a stale gate to a worker.
func (g *GitWorktreeManager) RefreshQualityGate(ctx context.Context, worktreePath string) {
	if err := g.refreshQualityGate(worktreePath); err != nil {
		slog.WarnContext(ctx, "refresh_quality_gate_failed",
			"worktree", worktreePath, "target", g.qualityGatePath, "error", err.Error())
	}
}

func (g *GitWorktreeManager) refreshQualityGate(worktreePath string) error {
	if g.qualityGatePath == "" {
		return nil
	}

	if _, err := os.Stat(g.qualityGatePath); err != nil {
		return fmt.Errorf("stat managed quality gate %s: %w", g.qualityGatePath, err)
	}

	linkPath := filepath.Join(worktreePath, "quality_gate.sh")
	if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale managed quality gate %s: %w", linkPath, err)
	}

	return copyQualityGateSnapshot(g.qualityGatePath, linkPath)
}

func copyQualityGateSnapshot(src, dst string) error {
	data, err := os.ReadFile(src) //nolint:gosec // src is configured by the dispatcher for the current project.
	if err != nil {
		return fmt.Errorf("read quality gate target: %w", err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil { //nolint:gosec // G703: dst is the dispatcher-managed quality_gate.sh path for a validated worktree.
		return fmt.Errorf("write quality gate snapshot: %w", err)
	}
	if err := os.Chmod(dst, 0o755); err != nil { //nolint:gosec // worker quality gate snapshots must be executable scripts.
		return fmt.Errorf("chmod quality gate snapshot: %w", err)
	}
	return nil
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
		if err := g.refreshQualityGate(worktree); err != nil {
			return false, fmt.Errorf("refresh managed quality gate for reused worktree %s: %w", worktree, err)
		}
		return false, nil
	}

	branchBehind, err := g.isAncestor(ctx, branch, baseBranch)
	if err == nil && branchBehind {
		return g.fastForwardExistingForReuse(ctx, worktree, branch, baseBranch)
	}

	baseBehind, err := g.isAncestor(ctx, baseBranch, branch)
	if err == nil && baseBehind {
		if err := g.refreshQualityGate(worktree); err != nil {
			return false, fmt.Errorf("refresh managed quality gate for reused worktree %s: %w", worktree, err)
		}
		return false, nil
	}
	return false, fmt.Errorf("agent branch %s diverged from base %s", branch, baseBranch)
}

func (g *GitWorktreeManager) fastForwardExistingForReuse(ctx context.Context, worktree, branch, baseBranch string) (bool, error) {
	dirty, err := g.trackedStatus(ctx, worktree)
	if err != nil {
		return false, err
	}
	if dirty != "" {
		return false, fmt.Errorf("stale branch %s is behind %s but worktree has tracked changes: %s", branch, baseBranch, dirty)
	}
	if _, err := g.runner.Run(ctx, "git", "-C", worktree, "merge", "--ff-only", baseBranch); err != nil {
		return false, fmt.Errorf("fast-forward existing worktree %s to %s: %w", worktree, baseBranch, err)
	}
	if err := g.refreshQualityGate(worktree); err != nil {
		return false, fmt.Errorf("refresh managed quality gate for reused worktree %s: %w", worktree, err)
	}
	return true, nil
}

// RebaseDivergedExistingForReuse rebases a clean preserved assignment
// worktree onto its current base branch. It leaves untracked artifacts in
// place, but refuses to run over tracked worktree changes.
func (g *GitWorktreeManager) RebaseDivergedExistingForReuse(ctx context.Context, worktree, branch, baseBranch string) error {
	if baseBranch == "" {
		baseBranch = "main"
	}
	dirty, err := g.trackedStatus(ctx, worktree)
	if err != nil {
		return err
	}
	if dirty != "" {
		return fmt.Errorf("diverged branch %s cannot rebase onto %s with tracked changes: %s", branch, baseBranch, dirty)
	}
	if _, err := g.runner.Run(ctx, "git", "-C", worktree, "rebase", baseBranch); err != nil {
		if _, abortErr := g.runner.Run(ctx, "git", "-C", worktree, "rebase", "--abort"); abortErr != nil {
			return fmt.Errorf("rebase existing worktree %s branch %s onto %s: %w; abort failed: %w",
				worktree, branch, baseBranch, err, abortErr)
		}
		return fmt.Errorf("rebase existing worktree %s branch %s onto %s: %w", worktree, branch, baseBranch, err)
	}
	return nil
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
	if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "merge-base", "--is-ancestor", targetBranch, sourceBranch); err != nil {
		return fmt.Errorf("refuse non-fast-forward update of %s to %s: %w", targetBranch, sourceBranch, err)
	}
	_, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "update-ref",
		"refs/heads/"+targetBranch, sourceBranch)
	if err != nil {
		return fmt.Errorf("update-ref %s to %s: %w", targetBranch, sourceBranch, err)
	}
	return nil
}

// BranchHead returns the commit SHA at the tip of branch.
func (g *GitWorktreeManager) BranchHead(ctx context.Context, branch string) (string, error) {
	out, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "rev-parse", branch)
	if err != nil {
		return "", fmt.Errorf("rev-parse %s: %w", branch, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// preserveEpicAncestry merges target into epicBranch so both the epic branch's
// current tip and target become ancestors of the epic branch, without checking
// out a worktree. It resolves both refs to immutable OIDs up front, computes a
// merge tree with `git merge-tree --write-tree`, builds a two-parent merge
// commit, validates both parents are ancestors of it, and advances the epic ref
// with a compare-and-swap `update-ref`. Any failure before the CAS leaves all
// refs untouched, so the caller can fall back losslessly. It satisfies the
// dispatcher's epicMergePreserver capability interface.
func (g *GitWorktreeManager) preserveEpicAncestry(ctx context.Context, epicBranch, target string) (epicPreserveOutcome, string, error) {
	oldEpicOID, err := g.revParse(ctx, g.repoRoot, epicBranch)
	if err != nil {
		return epicPreserveConflict, "", err
	}
	targetOID, err := g.revParse(ctx, g.repoRoot, target)
	if err != nil {
		return epicPreserveConflict, "", err
	}

	// Idempotency: if the epic already contains target's current tip there is
	// nothing to preserve. isAncestorOrUnrelated maps a clean "not an ancestor"
	// (git exit 1) to (false, nil) and surfaces operational failures as errors.
	contains, err := g.isAncestorOrUnrelated(ctx, targetOID, oldEpicOID)
	if err != nil {
		return epicPreserveConflict, "", fmt.Errorf("check target ancestry of epic %s: %w", epicBranch, err)
	}
	if contains {
		return epicPreserveNoop, oldEpicOID, nil
	}

	tree, conflict, err := g.mergeTreeWrite(ctx, oldEpicOID, targetOID)
	if err != nil {
		return epicPreserveConflict, "", err
	}
	if conflict {
		return epicPreserveConflict, "", nil
	}

	msg := fmt.Sprintf("chore(epic): preserve %s ancestry over %s", epicBranch, target)
	commitOut, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "commit-tree", tree,
		"-p", oldEpicOID, "-p", targetOID, "-m", msg)
	if err != nil {
		return epicPreserveConflict, "", fmt.Errorf("commit-tree preserve merge for %s: %w", epicBranch, err)
	}
	newCommit := strings.TrimSpace(string(commitOut))

	if err := g.validatePreserveMergeContent(ctx, newCommit, oldEpicOID, targetOID); err != nil {
		return epicPreserveConflict, "", fmt.Errorf("validate preserve commit content for %s: %w", epicBranch, err)
	}

	// Validate against the captured OIDs (not branch names, which would make
	// the epic-side check tautological once the ref is advanced).
	for _, parent := range []string{oldEpicOID, targetOID} {
		isAnc, ancErr := g.isAncestorOrUnrelated(ctx, parent, newCommit)
		if ancErr != nil {
			return epicPreserveConflict, "", fmt.Errorf("validate preserve commit ancestry for %s: %w", epicBranch, ancErr)
		}
		if !isAnc {
			return epicPreserveConflict, "", fmt.Errorf("preserve commit %s does not contain required ancestor %s", newCommit, parent)
		}
	}

	// Compare-and-swap: fail if the epic ref moved since we resolved it.
	if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "update-ref",
		"refs/heads/"+epicBranch, newCommit, oldEpicOID); err != nil {
		return epicPreserveConflict, "", fmt.Errorf("compare-and-swap epic ref %s: %w", epicBranch, err)
	}
	return epicPreserveMerged, newCommit, nil
}

// validatePreserveMergeContent rejects a tree-neutral merge that discards files
// newly added by the target parent. Ancestry alone is insufficient: `-s ours`
// records both parents while preserving only the first parent's tree.
func (g *GitWorktreeManager) validatePreserveMergeContent(ctx context.Context, mergeCommit, firstParent, targetParent string) error {
	mergeTree, err := g.revParse(ctx, g.repoRoot, mergeCommit+"^{tree}")
	if err != nil {
		return err
	}
	firstParentTree, err := g.revParse(ctx, g.repoRoot, firstParent+"^{tree}")
	if err != nil {
		return err
	}
	if mergeTree != firstParentTree {
		return nil
	}

	base, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "merge-base", firstParent, targetParent)
	if err != nil {
		return fmt.Errorf("find merge base: %w", err)
	}
	added, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "diff", "--name-only", "-z", "--diff-filter=A",
		strings.TrimSpace(string(base)), targetParent)
	if err != nil {
		return fmt.Errorf("list target-added files: %w", err)
	}
	for _, path := range strings.Split(string(added), "\x00") {
		if path == "" {
			continue
		}
		if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "cat-file", "-e", mergeCommit+":"+path); err != nil {
			return fmt.Errorf("merge %s drops target-added file %s", mergeCommit, path)
		}
	}
	return nil
}

// rollbackEpicPreserve reverts epicBranch from newOID back to oldOID using a
// compare-and-swap `update-ref`, undoing a preserve merge that failed
// post-merge verification (e.g. the quality gate). It fails without mutating
// the ref if epicBranch no longer points at newOID, so it never clobbers
// unrelated work that landed on the epic branch since the preserve merge.
func (g *GitWorktreeManager) rollbackEpicPreserve(ctx context.Context, epicBranch, oldOID, newOID string) error {
	if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "update-ref",
		"refs/heads/"+epicBranch, oldOID, newOID); err != nil {
		return fmt.Errorf("compare-and-swap rollback of epic ref %s to %s: %w", epicBranch, oldOID, err)
	}
	return nil
}

// mergeTreeWrite runs `git merge-tree --write-tree a b`. On a clean merge it
// returns the written tree OID. A merge conflict is git exit status 1
// (conflict=true, err=nil); any other non-zero exit is an operational error.
// Exit status is read from the wrapped *exec.ExitError rather than string
// matching so status 128 (unrelated histories, bad ref) is not mistaken for a
// content conflict.
func (g *GitWorktreeManager) mergeTreeWrite(ctx context.Context, a, b string) (tree string, conflict bool, err error) {
	out, runErr := g.runner.Run(ctx, "git", "-C", g.repoRoot, "merge-tree", "--write-tree", a, b)
	if runErr == nil {
		return strings.TrimSpace(string(out)), false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 {
		return "", true, nil
	}
	return "", false, fmt.Errorf("merge-tree --write-tree %s %s: %w", a, b, runErr)
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
