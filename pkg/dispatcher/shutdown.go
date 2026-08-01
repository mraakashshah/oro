package dispatcher

import (
	"context"
	"fmt"

	"oro/pkg/protocol"
)

func (d *Dispatcher) handleShutdownApproved(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.ShutdownApproved == nil {
		return
	}

	_ = d.logEvent(ctx, "shutdown_approved", workerID, "", workerID, "")

	// Send hard SHUTDOWN to finalize
	d.mu.Lock()
	w, ok := d.workers[workerID]
	var beadID string
	var assignmentID int64
	if ok {
		w.shutdownApproved = true
		beadID = w.beadID // capture before clearing
		assignmentID = w.assignmentID
		if w.shutdownReason == shutdownReasonScaleDown || w.spawnFor {
			sendShutdownWithoutBuffering(w)
		} else {
			_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
		}
		w.markShuttingDownWithoutAssignment()
	}
	d.mu.Unlock()

	// Requeue any in-flight bead so it can be reassigned.
	if beadID != "" {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "scale_down_bead_reset_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
		d.clearBeadTracking(beadID)
		_ = d.completeAssignment(ctx, assignmentID, beadID)
		_ = d.logEvent(ctx, "bead_requeued_scale_down", "dispatcher", beadID, workerID,
			`{"reason":"shutdown_approved"}`)
	}
}

// GracefulShutdownWorker, shutdownWaitLoop, handleShutdownTimeout, checkShutdownApproved → worker_pool.go

// --- Priority queue / assignment loop ---
func (d *Dispatcher) shutdownSequence() {
	d.mu.Lock()
	d.state = StateStopping
	d.mu.Unlock()

	// Phase 1: Cancel ops agents and abort in-flight merges.
	d.shutdownCancelOps()

	// Phase 2: Send PREPARE_SHUTDOWN to all workers and wait for them to drain.
	// Collect worker IDs and worktree paths under lock BEFORE the wait loop,
	// because workers will be deleted from the map as they disconnect.
	d.mu.Lock()
	workerIDs := make([]string, 0, len(d.workers))
	for id := range d.workers {
		workerIDs = append(workerIDs, id)
	}
	d.mu.Unlock()

	for _, id := range workerIDs {
		d.GracefulShutdownWorker(id, d.cfg.ShutdownTimeout)
	}

	d.shutdownWaitForWorkers()

	// Phase 3b: Reset in-progress beads to open so they become re-assignable
	// on the next dispatcher start. Best-effort: log warnings on failure, continue.
	d.shutdownResetActiveBeads()

	// Phase 3: Workers are stopped. Active assignment worktrees are preserved
	// as requeued recovery-owned state; no dispatcher-owned cleanup runs here.
	d.shutdownRemoveWorktrees(nil)
}

// shutdownWaitForWorkers → worker_pool.go

// shutdownCancelOps cancels active ops agents and aborts in-flight merges.
// Safe to call before workers are stopped.
func (d *Dispatcher) shutdownCancelOps() {
	for _, taskID := range d.ops.Active() {
		if err := d.ops.Cancel(taskID); err == nil {
			_ = d.logEvent(context.Background(), "ops_cancelled", "dispatcher", "", "", taskID)
		}
	}
	_ = d.merger.AbortAll()
}

// shutdownRemoveWorktrees removes the given worktrees and flushes bead state.
// Must be called AFTER all workers have been stopped so their working
// directories are no longer in use.
func (d *Dispatcher) shutdownRemoveWorktrees(paths []string) {
	// Remove worktrees best-effort (don't block shutdown).
	ctx := context.Background()
	for _, p := range paths {
		if err := d.worktrees.Remove(ctx, p); err != nil {
			_ = d.logEvent(ctx, "worktree_cleanup_failed", "dispatcher", "", "", err.Error())
		} else {
			_, _, _ = d.logEvent, ctx, p
		}
	}

	// Bead state is persisted by the store implementation.
}

// shutdownResetActiveBeads queries active assignments and resets each bead to
// "open" so it becomes re-assignable on next dispatcher start. Best-effort:
// failures are logged but do not block shutdown.
func (d *Dispatcher) shutdownResetActiveBeads() {
	ctx := context.Background()
	rows, err := d.db.QueryContext(ctx, `SELECT id, bead_id, worker_id FROM assignments WHERE status='active'`)
	if err != nil {
		_ = d.logEvent(ctx, "shutdown_reset_query_failed", "dispatcher", "", "", err.Error())
		return
	}
	defer func() { _ = rows.Close() }()

	type shutdownAssignment struct {
		id       int64
		beadID   string
		workerID string
	}
	var assignments []shutdownAssignment
	active := make(map[string]bool)
	for rows.Next() {
		var (
			assignmentID int64
			beadID       string
			workerID     string
		)
		if scanErr := rows.Scan(&assignmentID, &beadID, &workerID); scanErr != nil {
			_ = d.logEvent(ctx, "shutdown_reset_scan_failed", "dispatcher", "", "", scanErr.Error())
			continue
		}
		active[beadID] = true
		assignments = append(assignments, shutdownAssignment{id: assignmentID, beadID: beadID, workerID: workerID})
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = d.logEvent(ctx, "shutdown_reset_rows_failed", "dispatcher", "", "", rowsErr.Error())
	}
	_ = rows.Close()

	for _, assignment := range assignments {
		if updateErr := updateBeadStatus(ctx, d.beads, assignment.beadID, "open"); updateErr != nil {
			_ = d.logEvent(ctx, "shutdown_reset_bead_failed", "dispatcher", assignment.beadID, "", updateErr.Error())
			continue
		}
		if requeueErr := d.requeueAssignmentForShutdown(ctx, assignment.id); requeueErr != nil {
			_ = d.logEvent(ctx, "shutdown_assignment_requeue_failed", "dispatcher", assignment.beadID, assignment.workerID, requeueErr.Error())
			continue
		}
		_ = d.logEvent(ctx, "shutdown_assignment_requeued", "dispatcher", assignment.beadID, assignment.workerID,
			fmt.Sprintf(`{"assignment_id":%d}`, assignment.id))
	}

	inProgress, listErr := d.beads.InProgress(ctx)
	if listErr != nil {
		_ = d.logEvent(ctx, "shutdown_reset_in_progress_list_failed", "dispatcher", "", "", listErr.Error())
		return
	}
	for _, bead := range inProgress {
		if active[bead.ID] || bead.Type == "epic" {
			continue
		}
		if updateErr := updateBeadStatus(ctx, d.beads, bead.ID, "open"); updateErr != nil {
			_ = d.logEvent(ctx, "shutdown_reset_in_progress_bead_failed", "dispatcher", bead.ID, "", updateErr.Error())
		}
	}
}

func (d *Dispatcher) requeueAssignmentForShutdown(ctx context.Context, assignmentID int64) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE assignments SET status='requeued', completed_at=datetime('now') WHERE id=? AND status='active'`,
		assignmentID)
	if err != nil {
		return fmt.Errorf("requeue assignment for shutdown: %w", err)
	}
	rows, rowsErr := res.RowsAffected()
	if rowsErr == nil && rows != 1 {
		return fmt.Errorf("requeue assignment for shutdown: assignment_id %d affected %d rows", assignmentID, rows)
	}
	return nil
}

// cancelOpsAgents cancels all in-flight ops agents for the given bead and logs the result.
func (d *Dispatcher) cancelOpsAgents(ctx context.Context, beadID, workerID, reason string) {
	if n, err := d.ops.CancelForBead(beadID); n > 0 {
		_ = d.logEvent(ctx, "ops_agents_cancelled", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"count":%d,"reason":%q}`, n, reason))
		if err != nil {
			_ = d.logEvent(ctx, "ops_cancel_error", "dispatcher", beadID, workerID, err.Error())
		}
	}
}

// handleRepeatedQGOutput is called when isQGStuck detects maxStuckCount
// consecutive identical QG outputs for a bead. It classifies the failure and
// routes to the appropriate cleanup path without generic escalation:
//   - QGFailureDecisionReopenOriginal  → handleClassifiedQGExhaustion (reopen bead)
//   - QGFailureDecisionCreateOrReuseInfra → handleSystemicQGExhaustion (infra incident)
//   - default (StopForTriage, etc.)    → complete assignment, release worker, log triage event
//
// All paths leave no active assignment, stale worker state, stale qgStuckTracker,
// or stranded original bead. Worker-facing sends are never attempted, so the
// function is safe to call even when the worker has already disconnected.
