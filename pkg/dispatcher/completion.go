package dispatcher

import (
	"context"

	"oro/pkg/protocol"
)

func (d *Dispatcher) handleDone(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Done == nil {
		return
	}
	beadID := msg.Done.BeadID

	d.touchProgress(workerID)
	_ = d.logEvent(ctx, "done", workerID, beadID, workerID, "")

	// Reject merge if quality gate did not pass — retry or escalate.
	if !msg.Done.QualityGatePassed {
		d.handleQGFailure(ctx, workerID, beadID, msg.Done.QGOutput)
		return
	}

	d.mu.Lock()
	release := d.releaseWorkerAfterDoneLocked(workerID, beadID)
	d.mu.Unlock()
	d.assignPendingHandoffsToIdleWorkers()

	if !release.ok || release.worktree == "" {
		return
	}

	// Clear tracking state for completed bead.
	d.clearBeadTracking(beadID)

	// Re-check bead type: if a task bead was promoted to an epic mid-flight,
	// skip merge to avoid landing decomposition work as a finished task.
	// Show errors are best-effort — fall through to the normal merge path.
	if d.handleTypeChangedToEpic(ctx, workerID, beadID, release) {
		return
	}

	if release.isEpicDecomp {
		// Epic decomposition complete — skip merge/close; just clean up the worktree.
		_ = d.logEvent(ctx, "epic_decomp_done", workerID, beadID, workerID, "")
		if err := d.completeAssignment(ctx, release.assignmentID, beadID); err != nil {
			_ = d.logEvent(ctx, "assignment_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
		}
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "epic_decomp_reopen_failed", "dispatcher", beadID, workerID, err.Error())
		}
		d.safeGo(func() {
			if err := d.worktrees.Remove(ctx, release.worktree); err != nil {
				_ = d.logEvent(ctx, "worktree_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
			}
		})
		d.releaseWorkerAfterDoneTerminal(workerID, beadID, release.assignmentID)
		return
	}

	if d.cfg.ManualIntegration {
		d.completeManualIntegration(ctx, beadID, workerID, release)
		return
	}

	// Merge in background
	d.safeGo(func() {
		d.mergeAndComplete(ctx, beadID, workerID, release.worktree, release.branch, release.epicID, release.targetBranch, release.assignmentID)
	})
}

func (d *Dispatcher) shutdownCompletedSpawnForWorkerLocked(w *trackedWorker) {
	sendShutdownWithoutBuffering(w)
	w.markShuttingDownWithoutAssignment()
}
