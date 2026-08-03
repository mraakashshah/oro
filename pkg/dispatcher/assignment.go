package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"oro/pkg/agentmodel"
	"oro/pkg/protocol"
)

func (d *Dispatcher) assignBead(ctx context.Context, w *trackedWorker, bead protocol.Bead, focusVersionOpt ...uint64) error { //nolint:funlen,gocognit,gocyclo // orchestration logic, splitting would obscure flow
	return d.assignBeadWithClaim(ctx, w, bead, focusVersionOpt, nil)
}

func (d *Dispatcher) assignBeadWithClaim(ctx context.Context, w *trackedWorker, bead protocol.Bead, focusVersionOpt []uint64, onClaim func(bool)) error { //nolint:funlen,gocognit,gocyclo // orchestration logic, splitting would obscure flow
	claimReported := false
	reportClaim := func(claimed bool) {
		if onClaim != nil && !claimReported {
			onClaim(claimed)
			claimReported = true
		}
	}
	defer reportClaim(false)

	if strings.TrimSpace(bead.ID) == "" {
		return fmt.Errorf("assignBead: empty bead ID")
	}
	focusVersion := d.currentFocusVersion()
	if len(focusVersionOpt) > 0 {
		focusVersion = focusVersionOpt[0]
	}

	title, acceptance, ok := d.checkBeadReady(ctx, bead, w.id)
	if !ok {
		return nil
	}

	// Epic routing: check children before proceeding (requires I/O, must be outside lock).
	isEpicDecomp, skip := d.checkEpicAssignable(ctx, bead, w.id)
	if skip {
		return nil
	}
	if d.focusChangedSince(focusVersion) {
		d.notifyAssignLoop()
		return nil
	}
	checkpointBlocked, checkpointErr := d.reviewCheckpointBlocksAssignment(ctx, bead.ID)
	if checkpointErr != nil {
		_ = d.logEvent(ctx, "review_checkpoint_assignment_recheck_failed", "dispatcher", bead.ID, w.id, checkpointErr.Error())
		return nil
	}
	if checkpointBlocked {
		_ = d.logEvent(ctx, "review_checkpoint_assignment_blocked", "dispatcher", bead.ID, w.id,
			`{"reason":"durable_nonterminal_review_checkpoint","stage":"final_recheck"}`)
		return nil
	}

	// Atomically claim this bead for assignment (oro-ptp2: prevents race condition).
	// If another concurrent assignBead call already claimed it, abort.
	d.mu.Lock()
	if d.assigningBeads[bead.ID] {
		// Another assignment is already in progress for this bead
		d.mu.Unlock()
		_ = d.logEvent(ctx, "assignment_race_detected", "dispatcher", bead.ID, w.id,
			"bead already being assigned by another worker")
		return nil
	}
	// Belt-and-suspenders: check if a worker already completed assignment for
	// this bead. assigningBeads is ephemeral (cleared on completion), so a slow
	// goroutine could arrive after the flag is gone. This check catches that
	// case by inspecting the persistent worker state under the same lock.
	for _, w2 := range d.workers {
		if w2.beadID == bead.ID && (w2.state == protocol.WorkerBusy || w2.state == protocol.WorkerReserved) {
			d.mu.Unlock()
			_ = d.logEvent(ctx, "assignment_race_detected", "dispatcher", bead.ID, w.id,
				fmt.Sprintf("bead already assigned to worker %s", w2.id))
			return nil
		}
	}
	if live, ok := d.workers[w.id]; !ok || live != w || w.state != protocol.WorkerIdle {
		d.mu.Unlock()
		return nil
	}
	d.assigningBeads[bead.ID] = true
	delete(d.escalatedBeads, bead.ID)
	w.state = protocol.WorkerReserved
	w.assignmentID = 0
	w.beadID = bead.ID
	w.epicID = ""
	w.isEpicDecomp = isEpicDecomp
	w.worktree = ""
	w.baseBranch = ""
	w.targetBranch = ""
	w.runtime = ""
	w.model = ""
	w.reasoning = ""
	w.lastProgress = d.nowFunc()
	w.setupReservedAt = w.lastProgress
	w.reservationGen++
	reservationGen := w.reservationGen
	d.mu.Unlock()
	reportClaim(true)

	// Mark bead as in_progress BEFORE worktree creation.
	// This updates external state so other dispatchers see the bead is claimed.
	if err := d.updateBeadStatus(ctx, bead.ID, "in_progress"); err != nil {
		_ = d.logEvent(ctx, "update_status_failed", "dispatcher", bead.ID, w.id, err.Error())
		d.recordAssignmentFailure(bead.ID)
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		d.releaseAssignmentReservation(w.id, bead.ID, reservationGen)
		return nil
	}
	if d.focusChangedSince(focusVersion) {
		d.abortAssignmentForFocusChange(ctx, bead.ID, w.id, reservationGen, "", false, 0)
		return nil
	}

	// Check if a worktree already exists for this bead (from previous worker timeout/kill).
	// If it exists, reuse it to preserve uncommitted changes (oro-1eo8).
	d.mu.Lock()
	existingWorktree := d.worktreeByBead[bead.ID]
	d.mu.Unlock()

	var worktree, branch string
	var createdWorktree bool
	var err error
	// Resolve the base/target branch for this bead.
	// resolveEpicBranch walks the parent chain to find the actual epic ancestor —
	// bead.Epic maps to the JSON "parent" field and may point to a non-epic bead.
	// If the bead carries Metadata[MetaBranch], use that as the fallback default
	// branch instead of d.cfg.DefaultBranch (e.g. a standalone bead targeting a
	// custom integration branch).
	defaultBranch := d.cfg.DefaultBranch
	if bead.Metadata != nil {
		if v, ok := bead.Metadata[MetaBranch]; ok {
			if s, ok := v.(string); ok && s != "" {
				defaultBranch = s
			}
		}
	}
	baseBranch, resolvedEpicID, resolveErr := resolveEpicBranch(ctx, d.beads, bead.Epic, defaultBranch)
	if resolveErr != nil {
		_ = d.logEvent(ctx, "epic_branch_resolve_error", "dispatcher", bead.ID, w.id, resolveErr.Error())
		d.recordAssignmentFailure(bead.ID)
		_ = d.updateBeadStatus(ctx, bead.ID, "open")
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		d.releaseAssignmentReservation(w.id, bead.ID, reservationGen)
		return nil
	}
	if baseBranch != d.cfg.DefaultBranch {
		if !d.withEpicBranchAdmission(ctx, bead, w.id, baseBranch, resolvedEpicID, d.cfg.DefaultBranch) {
			d.releaseAssignmentReservation(w.id, bead.ID, reservationGen)
			return nil
		}
	}
	targetBranch := baseBranch

	if existingWorktree != "" && !d.worktrees.Exists(ctx, existingWorktree) {
		// Stale entry — the worktree was removed externally after the previous
		// worker timed out. Clear it so we fall through to create a fresh one.
		stalePath := existingWorktree
		existingWorktree = ""
		d.mu.Lock()
		delete(d.worktreeByBead, bead.ID)
		d.mu.Unlock()
		_ = d.logEvent(ctx, "stale_worktree_cleared", "dispatcher", bead.ID, w.id,
			fmt.Sprintf(`{"stale_path":%q}`, stalePath))
	}

	worktree, branch, createdWorktree = d.prepareAssignmentWorktree(ctx, bead.ID, w.id, reservationGen, existingWorktree, baseBranch, targetBranch)
	if worktree == "" {
		d.releaseAssignmentReservation(w.id, bead.ID, reservationGen)
		return nil
	}
	if d.focusChangedSince(focusVersion) {
		d.abortAssignmentForFocusChange(ctx, bead.ID, w.id, reservationGen, worktree, createdWorktree, 0)
		return nil
	}
	if !d.assignmentReservationHeld(w.id, bead.ID, reservationGen) {
		d.abortAssignmentReservationLost(ctx, bead.ID, w.id, reservationGen, worktree, createdWorktree, 0)
		return nil
	}

	assignmentID, assignErr := d.createAssignment(ctx, bead.ID, w.id, worktree)
	if assignErr != nil {
		if errors.Is(assignErr, errAssignmentBlockedByReviewCheckpoint) {
			_ = d.logEvent(ctx, "review_checkpoint_assignment_blocked", "dispatcher", bead.ID, w.id,
				`{"reason":"durable_nonterminal_review_checkpoint","stage":"atomic_insert"}`)
		} else {
			_ = d.logEvent(ctx, "assignment_persist_failed", "dispatcher", bead.ID, w.id, assignErr.Error())
			d.recordAssignmentFailure(bead.ID)
		}
		_ = d.updateBeadStatus(ctx, bead.ID, "open")
		if createdWorktree {
			_ = d.worktrees.Remove(ctx, worktree)
			d.mu.Lock()
			delete(d.worktreeByBead, bead.ID)
			d.mu.Unlock()
		}
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		d.releaseAssignmentReservation(w.id, bead.ID, reservationGen)
		return nil
	}
	if d.focusChangedSince(focusVersion) {
		d.abortAssignmentForFocusChange(ctx, bead.ID, w.id, reservationGen, worktree, createdWorktree, assignmentID)
		return nil
	}
	if !d.attachAssignmentToReservation(w.id, bead.ID, reservationGen, assignmentID, worktree, baseBranch, targetBranch, resolvedEpicID, isEpicDecomp) {
		d.abortAssignmentReservationLost(ctx, bead.ID, w.id, reservationGen, worktree, createdWorktree, assignmentID)
		return nil
	}
	_ = d.logEvent(ctx, "assign", "dispatcher", bead.ID, w.id,
		fmt.Sprintf(`{"worktree":%q,"branch":%q}`, worktree, branch))
	d.recordWorkerProgress(ctx, w.id, bead.ID, "assign")

	var codeCtx string
	if d.codeIndex != nil {
		ctx5s, cancel5s := context.WithTimeout(ctx, 5*time.Second)
		defer cancel5s()
		results, _ := d.searchCodeInWorkdir(ctx5s, bead.Title, 5, worktree)
		if len(results) > 0 {
			codeCtx = formatSearchResults(results)
		}
	}

	// Call estimator if bead needs estimation (no explicit model and no estimate yet)
	if bead.Model == "" && bead.EstimatedMinutes == 0 && d.estimator != nil {
		bead.EstimatedMinutes = d.estimator.Estimate(ctx, bead.Title, acceptance)
	}

	// Runtime launch selection is intentionally deferred to oro-zdqd/oro-snx1.
	// This step propagates runtime/model while preserving the existing
	// Claude-only worker launch path.
	resolvedRuntime, resolvedModel, resolvedReasoning := agentmodel.ResolveForBead("worker", bead)
	if isEpicDecomp {
		resolvedRuntime, resolvedModel, resolvedReasoning = agentmodel.ResolveForRole("ops_decompose")
	}
	execution := workerExecutionContext(assignmentID, isEpicDecomp, filepath.Base(d.cfg.RepoRoot))
	capability, capabilityErr := d.issueAssignmentCapability(
		ctx,
		execution.AssignmentID,
		execution.Generation,
		ActorRole(execution.ActorRole),
	)
	if capabilityErr != nil {
		_ = d.logEvent(ctx, "assignment_capability_issue_failed", "dispatcher", bead.ID, w.id, capabilityErr.Error())
		if completeErr := d.completeAssignment(ctx, assignmentID, bead.ID); completeErr != nil {
			_ = d.logEvent(ctx, "assignment_cleanup_failed", "dispatcher", bead.ID, w.id, completeErr.Error())
		}
		_ = d.updateBeadStatus(ctx, bead.ID, "open")
		if createdWorktree {
			_ = d.worktrees.Remove(ctx, worktree)
			d.mu.Lock()
			delete(d.worktreeByBead, bead.ID)
			d.mu.Unlock()
		}
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		d.releaseAssignmentReservation(w.id, bead.ID, reservationGen)
		return nil
	}
	execution.Capability = capability.Token
	payload := d.buildAssignPayload(ctx, &trackedWorker{
		id:           w.id,
		beadID:       bead.ID,
		worktree:     worktree,
		runtime:      resolvedRuntime,
		model:        resolvedModel,
		reasoning:    resolvedReasoning,
		isEpicDecomp: isEpicDecomp,
		targetBranch: targetBranch,
	}, 0, "", "", execution)
	if payload.Title == "" {
		payload.Title = title
	}
	if payload.AcceptanceCriteria == "" {
		payload.AcceptanceCriteria = acceptance
	}
	payload.CodeSearchContext = codeCtx
	// Release any prior bead this worker was carrying — the new assignment is
	// committed, so any leftover in_progress state on the old bead must be
	// cleared (oro-xqrh).
	d.releasePriorAssignment(ctx, w, bead.ID)
	d.mu.Lock()
	if d.focusVersion != focusVersion {
		d.mu.Unlock()
		d.abortAssignmentForFocusChange(ctx, bead.ID, w.id, reservationGen, worktree, createdWorktree, assignmentID)
		return nil
	}
	if !d.assignmentReservationHeldLocked(w.id, bead.ID, reservationGen) {
		d.mu.Unlock()
		d.abortAssignmentReservationLost(ctx, bead.ID, w.id, reservationGen, worktree, createdWorktree, assignmentID)
		return nil
	}
	w.state = protocol.WorkerBusy
	w.assignmentID = assignmentID
	w.execution = execution
	w.beadID = bead.ID
	w.epicID = resolvedEpicID // actual epic ancestor ID for auto-close on merge
	w.isEpicDecomp = isEpicDecomp
	w.worktree = worktree
	w.baseBranch = baseBranch
	w.targetBranch = targetBranch
	w.runtime = resolvedRuntime
	w.model = resolvedModel
	w.reasoning = resolvedReasoning
	w.lastProgress = d.nowFunc()
	w.setupReservedAt = time.Time{}
	err = d.sendToWorker(w, protocol.Message{
		Type:   protocol.MsgAssign,
		Assign: payload,
	})
	if err != nil {
		// Socket is dead — remove worker entirely to prevent tryAssign from
		// cycling beads through a zombie (oro-e2jk). Same fix as
		// bead_closed_externally path.
		_ = w.conn.Close()
		delete(d.workers, w.id)
		delete(d.worktreeByBead, bead.ID) // clear stale entry so next assignment creates a fresh worktree (oro-fhn3)
	}
	// Clear assignment-in-progress flag now that worker state is updated (oro-ptp2).
	delete(d.assigningBeads, bead.ID)
	d.mu.Unlock()
	if err != nil {
		if completeErr := d.completeAssignment(ctx, assignmentID, bead.ID); completeErr != nil {
			_ = d.logEvent(ctx, "assignment_cleanup_failed", "dispatcher", bead.ID, w.id, completeErr.Error())
		}
		_ = d.worktrees.Remove(ctx, worktree)
		_ = d.logEvent(ctx, "worktree_cleanup", "dispatcher", bead.ID, w.id, err.Error())
	}
	return nil
}

func (d *Dispatcher) prepareAssignmentWorktree(
	ctx context.Context,
	beadID, workerID string,
	reservationGen uint64,
	existingWorktree, baseBranch, targetBranch string,
) (worktree, branch string, created bool) {
	if existingWorktree != "" {
		expectedBranch := protocol.BranchPrefix + beadID
		if !d.validateExistingWorktreeForReuse(ctx, beadID, workerID, existingWorktree, expectedBranch, baseBranch) {
			return "", "", false
		}
		_ = d.logEvent(ctx, "worktree_reused", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"worktree":%q}`, existingWorktree))
		return existingWorktree, expectedBranch, false
	}
	if !d.createFreshAssignmentWorktreeAllowed(ctx, beadID, workerID, targetBranch) {
		return "", "", false
	}
	worktree, branch, err := d.worktrees.Create(ctx, beadID, baseBranch)
	if err != nil {
		_ = d.logEvent(ctx, "worktree_error", "dispatcher", beadID, workerID, err.Error())
		d.recordAssignmentFailure(beadID)
		_ = d.updateBeadStatus(ctx, beadID, "open")
		d.mu.Lock()
		delete(d.assigningBeads, beadID)
		d.mu.Unlock()
		return "", "", false
	}
	d.mu.Lock()
	if !d.assignmentReservationHeldLocked(workerID, beadID, reservationGen) {
		d.mu.Unlock()
		d.removeUnsharedAssignmentWorktree(ctx, beadID, worktree)
		return "", "", false
	}
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()
	return worktree, branch, true
}

func (d *Dispatcher) createFreshAssignmentWorktreeAllowed(ctx context.Context, beadID, workerID, targetBranch string) bool {
	if cleanErr := d.deleteStaleAgentBranch(ctx, beadID, workerID, targetBranch); cleanErr != nil {
		return d.rejectFreshAssignmentWorktree(ctx, beadID)
	}
	return true
}

func (d *Dispatcher) rejectFreshAssignmentWorktree(ctx context.Context, beadID string) bool {
	d.recordAssignmentFailure(beadID)
	_ = d.updateBeadStatus(ctx, beadID, "open")
	d.mu.Lock()
	delete(d.assigningBeads, beadID)
	d.mu.Unlock()
	return false
}

func (d *Dispatcher) validateExistingWorktreeForReuse(ctx context.Context, beadID, workerID, worktree, expectedBranch, baseBranch string) bool {
	currentBranch, currentErr := d.worktrees.CurrentBranch(ctx, worktree)
	if currentErr != nil || currentBranch != expectedBranch {
		d.quarantineUnsafeRecoveryWork(ctx, recoveryQuarantine{
			BeadID:   beadID,
			WorkerID: workerID,
			Worktree: worktree,
			Branch:   expectedBranch,
			Reason:   "branch_worktree_mismatch",
			Details:  "tracked worktree is not checked out on expected agent branch during assignment",
		})
		d.recordAssignmentFailure(beadID)
		_ = d.updateBeadStatus(ctx, beadID, "open")
		d.mu.Lock()
		delete(d.assigningBeads, beadID)
		d.mu.Unlock()
		return false
	}
	preparer, ok := d.worktrees.(existingWorktreeReusePreparer)
	if !ok {
		return true
	}
	fastForwarded, err := preparer.PrepareExistingForReuse(ctx, worktree, expectedBranch, baseBranch)
	if err != nil {
		recovered, recoveryErr := d.recoverExistingWorktreeReuseDivergence(ctx,
			beadID, workerID, worktree, expectedBranch, baseBranch, err)
		if recovered {
			return true
		}
		if recoveryErr != nil {
			err = recoveryErr
		}
		d.quarantineUnsafeRecoveryWork(ctx, recoveryQuarantine{
			BeadID:   beadID,
			WorkerID: workerID,
			Worktree: worktree,
			Branch:   expectedBranch,
			Reason:   "unsafe_stale_branch",
			Details:  err.Error(),
		})
		d.recordAssignmentFailure(beadID)
		_ = d.updateBeadStatus(ctx, beadID, "open")
		d.mu.Lock()
		delete(d.assigningBeads, beadID)
		d.mu.Unlock()
		return false
	}
	if fastForwarded {
		_ = d.logEvent(ctx, "worktree_fast_forwarded", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"worktree":%q,"branch":%q,"base_branch":%q}`, worktree, expectedBranch, baseBranch))
	}
	return true
}

func (d *Dispatcher) recoverExistingWorktreeReuseDivergence(ctx context.Context, beadID, workerID, worktree, expectedBranch, baseBranch string, prepareErr error) (bool, error) {
	if !isBranchDivergedFromBase(prepareErr) {
		return false, prepareErr
	}
	if d.isEpicRebaseChildForBase(ctx, beadID, baseBranch) {
		_ = d.logEvent(ctx, "epic_rebase_child_reuse_diverged", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"worktree":%q,"branch":%q,"base_branch":%q,"error":%q}`,
				worktree, expectedBranch, baseBranch, prepareErr.Error()))
		return true, nil
	}
	rebaser, ok := d.worktrees.(existingWorktreeDivergedRebaser)
	if !ok {
		return false, prepareErr
	}
	if err := rebaser.RebaseDivergedExistingForReuse(ctx, worktree, expectedBranch, baseBranch); err != nil {
		return false, fmt.Errorf("%w; rebase diverged existing worktree: %w", prepareErr, err)
	}
	_ = d.logEvent(ctx, "worktree_rebased_for_reuse", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"worktree":%q,"branch":%q,"base_branch":%q}`,
			worktree, expectedBranch, baseBranch))
	return true, nil
}

func (d *Dispatcher) isEpicRebaseChildForBase(ctx context.Context, beadID, baseBranch string) bool {
	if !strings.HasPrefix(baseBranch, protocol.EpicBranchPrefix) {
		return false
	}
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil || detail == nil {
		return false
	}
	epicID := strings.TrimPrefix(baseBranch, protocol.EpicBranchPrefix)
	return IsEpicRebaseChild(detail, epicID, baseBranch)
}

func isBranchDivergedFromBase(err error) bool {
	return err != nil && strings.Contains(err.Error(), "diverged from base")
}

func (d *Dispatcher) focusChangedSince(version uint64) bool {
	d.mu.Lock()
	changed := d.focusVersion != version
	d.mu.Unlock()
	return changed
}

func (d *Dispatcher) currentFocusVersion() uint64 {
	d.mu.Lock()
	version := d.focusVersion
	d.mu.Unlock()
	return version
}

func (d *Dispatcher) assignmentReservationHeld(workerID, beadID string, reservationGen uint64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.assignmentReservationHeldLocked(workerID, beadID, reservationGen)
}

func (d *Dispatcher) assignmentReservationHeldLocked(workerID, beadID string, reservationGen uint64) bool {
	w, ok := d.workers[workerID]
	return ok && w != nil && w.state == protocol.WorkerReserved && w.beadID == beadID && w.reservationGen == reservationGen
}

func (d *Dispatcher) attachAssignmentToReservation(workerID, beadID string, reservationGen uint64, assignmentID int64, worktree, baseBranch, targetBranch, epicID string, isEpicDecomp bool) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.assignmentReservationHeldLocked(workerID, beadID, reservationGen) {
		return false
	}
	w := d.workers[workerID]
	if w == nil {
		return false
	}
	w.assignmentID = assignmentID
	w.worktree = worktree
	w.baseBranch = baseBranch
	w.targetBranch = targetBranch
	w.epicID = epicID
	w.isEpicDecomp = isEpicDecomp
	return true
}

func (d *Dispatcher) releaseAssignmentReservation(workerID, beadID string, reservationGen uint64) {
	d.mu.Lock()
	released := d.releaseAssignmentReservationLocked(workerID, beadID, reservationGen)
	d.mu.Unlock()
	if released {
		d.notifyAssignLoop()
	}
}

func (d *Dispatcher) releaseAssignmentReservationLocked(workerID, beadID string, reservationGen uint64) bool {
	w, ok := d.workers[workerID]
	if !ok || w.state != protocol.WorkerReserved || w.beadID != beadID || w.reservationGen != reservationGen {
		return false
	}
	w.state = protocol.WorkerIdle
	w.assignmentID = 0
	w.beadID = ""
	w.epicID = ""
	w.isEpicDecomp = false
	w.worktree = ""
	w.baseBranch = ""
	w.targetBranch = ""
	w.runtime = ""
	w.model = ""
	w.reasoning = ""
	w.lastProgress = d.nowFunc()
	w.setupReservedAt = time.Time{}
	w.reservationGen++
	return true
}

func (d *Dispatcher) abortAssignmentReservationLost(ctx context.Context, beadID, workerID string, reservationGen uint64, worktree string, removeWorktree bool, assignmentID int64) {
	current := d.assignmentReservationHeld(workerID, beadID, reservationGen)
	if assignmentID != 0 {
		_ = d.completeAssignment(ctx, assignmentID, beadID)
	}
	if !current {
		if removeWorktree {
			d.removeUnsharedAssignmentWorktree(ctx, beadID, worktree)
		}
		_ = d.logEvent(ctx, "assignment_aborted_reservation_lost", "dispatcher", beadID, workerID, "")
		return
	}
	if !d.isBeadClosed(ctx, beadID) {
		_ = d.updateBeadStatus(ctx, beadID, "open")
	}
	if removeWorktree && worktree != "" {
		_ = d.worktrees.Remove(ctx, worktree)
	}
	d.releaseAssignmentClaim(workerID, beadID, reservationGen)
	_ = d.logEvent(ctx, "assignment_aborted_reservation_lost", "dispatcher", beadID, workerID, "")
	d.notifyAssignLoop()
}

// removeUnsharedAssignmentWorktree removes a setup worktree only when a newer
// reservation has not published that same deterministic path for the bead.
func (d *Dispatcher) removeUnsharedAssignmentWorktree(ctx context.Context, beadID, worktree string) {
	if worktree == "" {
		return
	}
	d.mu.Lock()
	shared := d.worktreeByBead[beadID] == worktree
	d.mu.Unlock()
	if !shared {
		_ = d.worktrees.Remove(ctx, worktree)
	}
}

func (d *Dispatcher) abortAssignmentForFocusChange(ctx context.Context, beadID, workerID string, reservationGen uint64, worktree string, removeWorktree bool, assignmentID int64) {
	if !d.releaseAssignmentClaim(workerID, beadID, reservationGen) {
		return
	}
	if assignmentID != 0 {
		_ = d.completeAssignment(ctx, assignmentID, beadID)
	}
	if !d.isBeadClosed(ctx, beadID) {
		_ = d.updateBeadStatus(ctx, beadID, "open")
	}
	if removeWorktree && worktree != "" {
		_ = d.worktrees.Remove(ctx, worktree)
		d.mu.Lock()
		delete(d.worktreeByBead, beadID)
		d.mu.Unlock()
	}
	_ = d.logEvent(ctx, "assignment_aborted_focus_changed", "dispatcher", beadID, workerID, "")
	d.notifyAssignLoop()
}

func (d *Dispatcher) releaseAssignmentClaim(workerID, beadID string, reservationGen uint64) bool {
	d.mu.Lock()
	released := d.releaseAssignmentReservationLocked(workerID, beadID, reservationGen)
	if released {
		delete(d.assigningBeads, beadID)
	}
	d.mu.Unlock()
	return released
}

func (d *Dispatcher) isBeadClosed(ctx context.Context, beadID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	return err == nil && detail != nil && detail.Status == "closed"
}

// checkEpicAssignable determines whether an epic bead should proceed to assignment.
// Returns (isEpicDecomp=true, skip=false) when the epic has no children and should
// be assigned for decomposition. Returns (false, true) to skip in all other cases:
// epic with open children (not ready), epic with all children closed (auto-closed here),
// or any HasChildren/AllChildrenClosed error. For non-epic beads both values are false.
func (d *Dispatcher) checkEpicAssignable(ctx context.Context, bead protocol.Bead, workerID string) (isEpicDecomp, skip bool) {
	if !strings.EqualFold(bead.Type, "epic") {
		return false, false
	}
	hasChildren, err := d.beads.HasChildren(ctx, bead.ID)
	if err != nil {
		_ = d.logEvent(ctx, "epic_has_children_error", "dispatcher", bead.ID, workerID, err.Error())
		return false, true
	}
	if !hasChildren {
		return d.checkChildlessEpicAssignable(ctx, bead, workerID)
	}
	// Epic has children: auto-close if all done, otherwise skip.
	allClosed, err := d.beads.AllChildrenClosed(ctx, bead.ID)
	if err != nil {
		_ = d.logEvent(ctx, "epic_all_children_closed_error", "dispatcher", bead.ID, workerID, err.Error())
		return false, true
	}
	if allClosed {
		targetBranch := resolveEpicTargetBranch(bead.Metadata, d.cfg.DefaultBranch)
		d.completeEpicClose(ctx, bead.ID, workerID, "All children completed", targetBranch)
	}
	return false, true
}

func (d *Dispatcher) checkChildlessEpicAssignable(ctx context.Context, bead protocol.Bead, workerID string) (isEpicDecomp, skip bool) {
	detail, err := d.beads.Show(ctx, bead.ID)
	if err != nil {
		_ = d.logEvent(ctx, "epic_pre_decompose_ac_fetch_failed", "dispatcher", bead.ID, workerID,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return true, false
	}
	if detail == nil {
		_ = d.logEvent(ctx, "epic_pre_decompose_ac_fetch_failed", "dispatcher", bead.ID, workerID,
			`{"error":"show returned nil epic"}`)
		return true, false
	}

	cmd, ok := d.parseEpicAcceptanceCmd(ctx, "epic_pre_decompose_acceptance_parse_error", bead.ID, workerID, detail.AcceptanceCriteria)
	if !ok {
		return true, false
	}
	if cmd == "" {
		return true, false
	}
	if d.acceptance == nil {
		_ = d.logEvent(ctx, "epic_pre_decompose_acceptance_unavailable", "dispatcher", bead.ID, workerID,
			fmt.Sprintf(`{"cmd":%q}`, cmd))
		return true, false
	}

	output, passed, runErr := d.acceptance.Run(ctx, cmd)
	if runErr != nil {
		_ = d.logEvent(ctx, "epic_pre_decompose_acceptance_error", "dispatcher", bead.ID, workerID,
			fmt.Sprintf(`{"cmd":%q,"error":%q}`, cmd, runErr.Error()))
		return true, false
	}
	if !passed {
		_ = d.logEvent(ctx, "epic_pre_decompose_acceptance_failed", "dispatcher", bead.ID, workerID,
			fmt.Sprintf(`{"cmd":%q,"output":%q}`, cmd, output))
		return true, false
	}

	_ = d.logEvent(ctx, "epic_pre_decompose_acceptance_passed", "dispatcher", bead.ID, workerID,
		fmt.Sprintf(`{"cmd":%q}`, cmd))
	targetBranch := resolveEpicTargetBranch(detail.Metadata, d.cfg.DefaultBranch)
	d.completeEpicClose(ctx, bead.ID, workerID, "Acceptance test passed before decomposition", targetBranch)
	return false, true
}

// lookupBeadDetail retrieves the title, acceptance criteria, and status for a bead (best-effort).
