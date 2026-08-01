package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
	"strings"
)

func (d *Dispatcher) mergeAndComplete(ctx context.Context, beadID, workerID, worktree, branch, epicID, targetBranch string, assignmentID int64) { //nolint:funlen // orchestrates merge pipeline; splitting would obscure the sequential flow
	defer d.guardMerge(beadID)()

	// Closed-bead guard (oro-jev9): if the bead was closed externally between
	// assignment and review (e.g. manager dedup-closed it as a duplicate),
	// abort before merging. Otherwise the worker's commit lands on the target
	// branch even though the bead is already resolved.
	detail, showErr := d.beads.Show(ctx, beadID)
	if showErr == nil && detail != nil && detail.Status == "closed" {
		_ = d.logEvent(ctx, "merge_aborted_closed_bead", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"target":%q}`, branch, targetBranch))
		d.quarantineUnsafeRecoveryWork(ctx, recoveryQuarantine{
			BeadID:       beadID,
			AssignmentID: assignmentID,
			WorkerID:     workerID,
			Worktree:     worktree,
			Branch:       branch,
			Reason:       "external_close_without_merge_proof",
			Details:      "merge aborted because bead was already closed before merge proof",
		})
		d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
		return
	}

	if !d.checkPreMergeLeaks(ctx, beadID, workerID, worktree, branch, targetBranch, assignmentID) {
		return
	}
	if showErr == nil && d.completeEpicRebaseChild(ctx, detail, beadID, workerID, worktree, branch, epicID, targetBranch, assignmentID) {
		return
	}

	result, err := d.merger.Merge(ctx, merge.Opts{
		Branch:       branch,
		Worktree:     worktree,
		BeadID:       beadID,
		TargetBranch: targetBranch,
		PreFFCheck: func(checkCtx context.Context, finalWorktree string) error {
			return d.runPreMergeQG(checkCtx, beadID, workerID, finalWorktree, assignmentID, targetBranch)
		},
	})
	if err != nil {
		if d.handlePreFFCheckError(ctx, beadID, workerID, worktree, assignmentID, err) {
			return
		}
		var conflictErr *merge.ConflictError
		if errors.As(err, &conflictErr) {
			// Spawn ops agent to resolve conflict
			resultCh := d.ops.ResolveMergeConflict(ctx, ops.MergeOpts{
				BeadID:        beadID,
				Branch:        branch,
				Worktree:      worktree,
				ConflictFiles: conflictErr.Files,
				TargetBranch:  targetBranch,
			})
			d.safeGo(func() {
				d.handleMergeConflictResult(ctx, beadID, workerID, worktree, epicID, targetBranch, assignmentID, resultCh)
			})
			_ = d.logEvent(ctx, "merge_conflict", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"files":%q}`, conflictErr.Files))
			return
		}
		// Non-conflict merge failure after rebase/ff retry is still recoverable.
		// Keep the worktree and agent branch available for the escalation agent
		// and for a future reassignment; otherwise the only recovery context can
		// be deleted before ops gets a chance to inspect it.
		_ = d.completeAssignment(ctx, assignmentID, beadID)
		if updateErr := d.updateBeadStatus(ctx, beadID, "open"); updateErr != nil {
			_ = d.logEvent(ctx, "merge_failed_reopen_failed", "dispatcher", beadID, workerID, updateErr.Error())
		}
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscMergeConflict, beadID, "merge failed", err.Error()), beadID, workerID)
		_ = d.logEvent(ctx, "merge_failed", "dispatcher", beadID, workerID, err.Error())
		d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
		return
	}
	if result.Noop {
		d.handleNoopMerge(ctx, beadID, workerID, worktree, branch, epicID, targetBranch, assignmentID, result.CommitSHA)
		return
	}

	d.finalizeSuccessfulMerge(ctx, beadID, workerID, worktree, epicID, targetBranch, assignmentID, result.CommitSHA)
}

func (d *Dispatcher) handleNoopMerge(ctx context.Context, beadID, workerID, worktree, branch, epicID, targetBranch string, assignmentID int64, sha string) {
	target := targetBranch
	if target == "" {
		target = d.cfg.DefaultBranch
	}
	if err := d.completeAssignment(ctx, assignmentID, beadID); err != nil {
		_ = d.logEvent(ctx, "assignment_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			"merged but assignment cleanup failed", err.Error()), beadID, workerID)
		d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
		return
	}
	if err := d.CloseBead(ctx, beadID, fmt.Sprintf("Merged: %s", sha)); err != nil {
		_ = d.logEvent(ctx, "close_bead_after_noop_merge_failed", "dispatcher", beadID, workerID, err.Error())
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			"no-op merge proven but bead close failed", err.Error()), beadID, workerID)
	}
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
	d.cancelOpsAgents(ctx, beadID, workerID, "bead_merged_noop")
	_ = d.logEvent(ctx, "merge_noop", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"branch":%q,"target":%q,"sha":%q}`, branch, target, sha))
	if epicID != "" {
		d.mu.Lock()
		delete(d.epicMergeFailed, epicID)
		d.mu.Unlock()
	}
	d.autoCloseEpicIfComplete(ctx, workerID, epicID)
	d.removeWorktreeAndClearTracking(ctx, beadID, workerID, worktree, target)
	d.maybeConsolidateMemory(ctx)
	d.maybeTriggerDream(ctx)
	d.maybeTriggerJanitor(ctx)
}

func (d *Dispatcher) completeEpicRebaseChild(ctx context.Context, detail *protocol.BeadDetail, beadID, workerID, worktree, branch, epicID, targetBranch string, assignmentID int64) bool {
	if !IsEpicRebaseChild(detail, epicID, targetBranch) {
		return false
	}
	recoveryTarget := epicRebaseChildRecoveryTarget(detail, targetBranch)
	if recoveryTarget == "" {
		d.failEpicRebaseChild(ctx, beadID, workerID, assignmentID, "epic rebase child target resolution failed", fmt.Errorf("cannot resolve recovery target for %s", beadID))
		return true
	}
	if err := d.validateEpicRebaseChildAncestry(ctx, branch, recoveryTarget, targetBranch); err != nil {
		d.failEpicRebaseChild(ctx, beadID, workerID, assignmentID, "epic rebase child ancestry check failed", err)
		return true
	}
	if err := d.worktrees.UpdateBranchRef(ctx, targetBranch, branch); err != nil {
		d.failEpicRebaseChild(ctx, beadID, workerID, assignmentID, "epic rebase child update failed", err)
		return true
	}
	sha, err := d.worktrees.BranchHead(ctx, branch)
	if err != nil || strings.TrimSpace(sha) == "" {
		sha = branch
	}
	_ = d.logEvent(ctx, "epic_rebase_child_ref_updated", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"epic":%q,"branch":%q,"source":%q}`, epicID, targetBranch, branch))
	d.finalizeSuccessfulMerge(ctx, beadID, workerID, worktree, epicID, targetBranch, assignmentID, sha)
	return true
}

func epicRebaseChildRecoveryTarget(detail *protocol.BeadDetail, epicBranch string) string {
	if detail == nil {
		return ""
	}
	if target, _ := detail.Metadata["epic_rebase_target"].(string); strings.TrimSpace(target) != "" {
		return strings.TrimSpace(target)
	}
	return strings.TrimSpace(strings.TrimPrefix(detail.Title, "Rebase "+epicBranch+" onto "))
}

func (d *Dispatcher) validateEpicRebaseChildAncestry(ctx context.Context, branch, targetBranch, epicBranch string) error {
	checker, ok := d.worktrees.(assignmentBaseBranchSafetyChecker)
	if !ok {
		return fmt.Errorf("cannot verify required ancestry for recovery branch %s", branch)
	}
	for _, requiredAncestor := range []string{targetBranch, epicBranch} {
		hasUniqueCommits, err := checker.BaseBranchHasUniqueCommits(ctx, requiredAncestor, branch)
		if err != nil {
			return fmt.Errorf("check whether recovery branch %s contains %s: %w", branch, requiredAncestor, err)
		}
		if hasUniqueCommits {
			return fmt.Errorf("recovery branch %s does not contain required ancestry from %s", branch, requiredAncestor)
		}
	}
	return nil
}

func (d *Dispatcher) failEpicRebaseChild(ctx context.Context, beadID, workerID string, assignmentID int64, summary string, cause error) {
	if updateErr := d.updateBeadStatus(ctx, beadID, "open"); updateErr != nil {
		_ = d.logEvent(ctx, "merge_failed_reopen_failed", "dispatcher", beadID, workerID, updateErr.Error())
	}
	if requeueErr := d.requeueAssignment(ctx, assignmentID); requeueErr != nil {
		_ = d.logEvent(ctx, "merge_failed_requeue_failed", "dispatcher", beadID, workerID, requeueErr.Error())
	}
	d.escalate(ctx, protocol.FormatEscalation(protocol.EscMergeConflict, beadID, summary, cause.Error()), beadID, workerID)
	_ = d.logEvent(ctx, "merge_failed", "dispatcher", beadID, workerID, cause.Error())
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
}

// IsEpicRebaseChild reports whether detail is the canonical recovery task for
// rebasing an epic branch onto its target. Recovery tasks must be allowed to
// run against the divergence they were created to repair.
func IsEpicRebaseChild(detail *protocol.BeadDetail, epicID, targetBranch string) bool {
	if detail == nil || epicID == "" || targetBranch == "" {
		return false
	}
	epicBranch := protocol.EpicBranchPrefix + epicID
	if targetBranch != epicBranch {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(detail.Title), "Rebase "+epicBranch+" onto ")
}

func (d *Dispatcher) finalizeSuccessfulMerge(ctx context.Context, beadID, workerID, worktree, epicID, targetBranch string, assignmentID int64, sha string) {
	if err := d.completeAssignment(ctx, assignmentID, beadID); err != nil {
		_ = d.logEvent(ctx, "assignment_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			"merged but assignment cleanup failed", err.Error()), beadID, workerID)
		d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
		return
	}
	if err := d.CloseBead(ctx, beadID, fmt.Sprintf("Merged: %s", sha)); err != nil {
		_ = d.logEvent(ctx, "close_bead_after_merge_failed", "dispatcher", beadID, workerID, err.Error())
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			"merged but bead close failed", err.Error()), beadID, workerID)
	}
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)

	// A successful merge proves the system is producing, not crash-looping —
	// reset the unexpected-exit counter so reconcileScale's runaway cap
	// (managed+exits >= 2*target) can't strand a long-running dispatcher with
	// fewer workers than target after natural worker turnover (oro-1dbr).
	d.mu.Lock()
	d.unexpectedManagedExits = 0
	d.mu.Unlock()

	d.cancelOpsAgents(ctx, beadID, workerID, "bead_merged")

	_ = d.logEvent(ctx, "merged", "dispatcher", beadID, workerID, fmt.Sprintf(`{"sha":%q}`, sha))
	mergedTo := targetBranch
	if mergedTo == "" {
		mergedTo = d.cfg.DefaultBranch
	}
	d.escalate(ctx, protocol.FormatEscalation(protocol.EscMergeComplete, beadID, "merged to "+mergedTo, sha), beadID, workerID)

	if epicID != "" {
		d.mu.Lock()
		delete(d.epicMergeFailed, epicID)
		d.mu.Unlock()
	}
	d.autoCloseEpicIfComplete(ctx, workerID, epicID)
	d.removeWorktreeAndClearTracking(ctx, beadID, workerID, worktree, targetBranch)

	d.maybeConsolidateMemory(ctx)
	d.maybeTriggerDream(ctx)
	d.maybeTriggerJanitor(ctx)
}

// maybeConsolidateMemory increments the completion counter and triggers an
// async memory consolidation when the threshold is reached.
