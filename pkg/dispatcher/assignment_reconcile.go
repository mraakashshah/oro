package dispatcher

import (
	"context"
	"fmt"
	"strings"
	"time"

	"oro/pkg/merge"
	"oro/pkg/protocol"
)

func (d *Dispatcher) checkClosedBeadAssignments(ctx context.Context) {
	// Collect (workerID, beadID) pairs for all busy/reserved workers under lock.
	type assignment struct {
		workerID string
		beadID   string
	}
	d.mu.Lock()
	var active []assignment
	for _, w := range d.workers {
		if w.beadID != "" && (w.state == protocol.WorkerBusy || w.state == protocol.WorkerReserved) {
			active = append(active, assignment{w.id, w.beadID})
		}
	}
	d.mu.Unlock()

	for _, a := range active {
		d.handleClosedAssignment(ctx, a.workerID, a.beadID)
	}
}

// handleClosedAssignment checks whether a single bead has been closed
// externally and, if so, shuts down the assigned worker and triggers cleanup.
func (d *Dispatcher) handleClosedAssignment(ctx context.Context, workerID, beadID string) {
	// Guard against re-entry: if we already processed this external close, skip (FM2).
	d.mu.Lock()
	alreadyProcessed := d.processedExternalClose[beadID]
	d.mu.Unlock()
	if alreadyProcessed {
		return
	}

	// Skip beads with in-flight merges to prevent duplicate mergeAndComplete (oro-x4x8).
	d.mu.Lock()
	merging := d.mergingBeads[beadID]
	d.mu.Unlock()
	if merging {
		return
	}

	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		// Transient lookup error — don't kill the worker, retry next cycle.
		return
	}
	switch {
	case detail == nil:
		// Bead not found in source — treat as externally removed.
		_ = d.logEvent(ctx, "bead_closed_externally", "dispatcher", beadID, workerID,
			"bead not found in source; sending shutdown")
	case detail.Status == "closed":
		_ = d.logEvent(ctx, "bead_closed_externally", "dispatcher", beadID, workerID,
			"bead closed while worker assigned; sending shutdown")
	case detail.Status == "open":
		if err := d.updateBeadStatus(ctx, beadID, "in_progress"); err != nil {
			_ = d.logEvent(ctx, "assigned_bead_status_reconcile_failed", "dispatcher", beadID, workerID, err.Error())
			return
		}
		_ = d.logEvent(ctx, "assigned_bead_status_reconciled", "dispatcher", beadID, workerID,
			`{"from":"open","to":"in_progress"}`)
		return
	default:
		// Bead exists and is not explicitly closed — keep worker assigned.
		return
	}

	worktree, epicID, targetBranch, assignmentID := d.shutdownWorkerForClose(workerID, beadID)
	d.finalizeExternalClose(ctx, workerID, beadID, worktree, epicID, targetBranch, assignmentID)
}

// shutdownWorkerForClose sends SHUTDOWN, captures the worker's worktree/epic/
// targetBranch/assignmentID, clears worker state under lock, and marks the
// close as processed (FM2). Removes the worker entirely when sendToWorker
// fails so tryAssign doesn't cycle beads through a zombie (oro-e2jk).
func (d *Dispatcher) shutdownWorkerForClose(workerID, beadID string) (worktree, epicID, targetBranch string, assignmentID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.processedExternalClose[beadID] = true
	w, ok := d.workers[workerID]
	if !ok || w.beadID != beadID {
		return "", "", "", 0
	}
	assignmentID = w.assignmentID
	worktree = w.worktree
	epicID = w.epicID
	targetBranch = w.targetBranch
	if err := d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown}); err != nil {
		_ = w.conn.Close()
		delete(d.workers, workerID)
		return worktree, epicID, targetBranch, assignmentID
	}
	w.state = protocol.WorkerShuttingDown
	w.assignmentID = 0
	w.beadID = ""
	w.epicID = ""
	w.worktree = ""
	w.baseBranch = ""
	w.targetBranch = ""
	w.model = ""
	return worktree, epicID, targetBranch, assignmentID
}

// finalizeExternalClose cleans up the assignment record, worktree, and ops
// agents after an external close. If the worker has a worktree (and therefore
// possibly committed work on agent/<beadID>), the dispatcher first attempts
// to ff-merge that branch to its target so a worker that called
// `oro task close` itself doesn't silently drop committed work
// (oro-0xqv: oro-ohlro lost commit 099cc7a6 this way). Merger handles the
// no-commits / branch-missing cases by returning an error which we treat as
// the legacy cancellation path.
//
// Recovery outcomes:
//   - Merge succeeds: log external_close_recovered with the SHA. The merger
//     also removes the worktree, so we only complete the assignment and clear
//     tracking afterward.
//   - Merge fails (conflict, missing branch, transient error): log
//     external_close_recovery_failed, escalate with the worktree path and
//     error so the manager can recover manually, then proceed with the
//     legacy cleanup (worktree remove, tracking clear, cancellation event).
func (d *Dispatcher) finalizeExternalClose(ctx context.Context, workerID, beadID, worktree, epicID, targetBranch string, assignmentID int64) {
	logCancelled := func() {
		_ = d.logEvent(ctx, "external_close_cancelled", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"assignment_id":%d,"epic_id":%q,"target_branch":%q}`, assignmentID, epicID, targetBranch))
	}
	if worktree != "" {
		d.safeGo(func() {
			recovered := d.tryRecoverExternalCloseWork(ctx, workerID, beadID, worktree, targetBranch)
			d.cancelOpsAgents(ctx, beadID, workerID, "external_close")
			if recovered {
				_ = d.completeAssignment(ctx, assignmentID, beadID)
				// removeWorktreeAndClearTracking is a no-op if the merger already
				// took the worktree on a successful recovery merge.
				d.removeWorktreeAndClearTracking(ctx, beadID, workerID, worktree, targetBranch)
				d.clearBeadTracking(beadID)
			} else {
				d.quarantineUnsafeRecoveryWork(ctx, recoveryQuarantine{
					BeadID:       beadID,
					AssignmentID: assignmentID,
					WorkerID:     workerID,
					Worktree:     worktree,
					Branch:       protocol.BranchPrefix + beadID,
					Reason:       "external_close_recovery_failed",
					Details:      "external close recovery merge failed",
				})
			}
			logCancelled()
		})
		return
	}
	_ = d.completeAssignment(ctx, assignmentID, beadID)
	d.clearBeadTracking(beadID)
	logCancelled()
}

// tryRecoverExternalCloseWork attempts to ff-merge the agent branch for a
// bead that was closed externally so committed work isn't silently dropped.
// Logs external_close_recovered on success, external_close_recovery_failed
// + escalates on failure. Returns true only when merge proof exists.
func (d *Dispatcher) tryRecoverExternalCloseWork(ctx context.Context, workerID, beadID, worktree, targetBranch string) bool {
	branch := protocol.BranchPrefix + beadID
	result, err := d.merger.Merge(ctx, merge.Opts{
		Branch:       branch,
		Worktree:     worktree,
		BeadID:       beadID,
		TargetBranch: targetBranch,
	})
	if err == nil && result != nil {
		_ = d.logEvent(ctx, "external_close_recovered", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"sha":%q,"branch":%q,"target":%q}`, result.CommitSHA, branch, targetBranch))
		return true
	}
	errMsg := "no recoverable result"
	if err != nil {
		errMsg = err.Error()
	}
	_ = d.logEvent(ctx, "external_close_recovery_failed", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"branch":%q,"worktree":%q,"target":%q,"error":%q}`, branch, worktree, targetBranch, errMsg))
	d.escalate(ctx, protocol.FormatEscalation(protocol.EscMergeConflict, beadID,
		"external close: failed to recover worker branch "+branch,
		"worktree="+worktree+"; error="+errMsg), beadID, workerID)
	return false
}

// filterAssignable returns beads eligible for assignment: excludes closed beads,
// beads with status in_progress or blocked, beads with recent worktree creation
// failures (within cooldown window), beads currently in-flight (assigningBeads),
// beads with unresolved blocking dependencies, beads with a durable nonterminal
// review checkpoint, and beads whose agent branch is already merged to main.
// Epics are allowed through; assignBead performs the HasChildren check.
func (d *Dispatcher) filterAssignable(ctx context.Context, allBeads []protocol.Bead) []protocol.Bead {
	now := d.nowFunc()

	allBeads = d.filterReviewCheckpointBlockedBeads(ctx, allBeads)
	allBeads = d.filterExecutableBeads(ctx, allBeads)
	allBeads = d.filterRecoveryQuarantinedBeads(ctx, allBeads)

	d.mu.Lock()
	candidates := d.assignmentCandidatesLocked(allBeads, now)
	d.mu.Unlock()

	return d.filterAlreadyMergedBranches(ctx, candidates)
}

func (d *Dispatcher) filterReviewCheckpointBlockedBeads(ctx context.Context, allBeads []protocol.Bead) []protocol.Bead {
	if len(allBeads) == 0 || d.db == nil {
		return allBeads
	}
	blockedBeads, err := d.reviewCheckpointBlockedBeads(ctx)
	d.recordAssignmentObservation("review_checkpoint", err)
	if err != nil {
		_ = d.logEvent(ctx, "review_checkpoint_assignment_filter_failed", "dispatcher", "", "", err.Error())
		return nil
	}
	filtered := make([]protocol.Bead, 0, len(allBeads))
	for _, bead := range allBeads {
		if blockedBeads[bead.ID] {
			_ = d.logEvent(ctx, "review_checkpoint_assignment_blocked", "dispatcher", bead.ID, "",
				`{"reason":"durable_nonterminal_review_checkpoint"}`)
			continue
		}
		filtered = append(filtered, bead)
	}
	return filtered
}

func (d *Dispatcher) reviewCheckpointBlockedBeads(ctx context.Context) (map[string]bool, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT DISTINCT bead_id FROM review_checkpoints_blocking_assignment`)
	if err != nil {
		return nil, fmt.Errorf("query blocking review checkpoint beads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	blocked := make(map[string]bool)
	for rows.Next() {
		var beadID string
		if err := rows.Scan(&beadID); err != nil {
			return nil, fmt.Errorf("scan blocking review checkpoint bead: %w", err)
		}
		blocked[beadID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate blocking review checkpoint beads: %w", err)
	}
	return blocked, nil
}

func (d *Dispatcher) reviewCheckpointBlocksAssignment(ctx context.Context, beadID string) (bool, error) {
	if d.db == nil {
		return false, nil
	}
	var blocked bool
	if err := d.db.QueryRowContext(ctx, `
SELECT EXISTS(
    SELECT 1
    FROM review_checkpoints_blocking_assignment
    WHERE bead_id = ?
)`, beadID).Scan(&blocked); err != nil {
		return false, fmt.Errorf("query blocking review checkpoint: %w", err)
	}
	return blocked, nil
}

func (d *Dispatcher) filterExecutableBeads(ctx context.Context, allBeads []protocol.Bead) []protocol.Bead {
	// Already-decomposed epics are not executable worker tasks. Childless epics
	// remain assignable so a decomposition worker can create child beads.
	executable := make([]protocol.Bead, 0, len(allBeads))
	for _, b := range allBeads {
		if d.executableAfterEpicSideEffects(ctx, b) {
			executable = append(executable, b)
		}
	}
	return executable
}

func (d *Dispatcher) executableAfterEpicSideEffects(ctx context.Context, bead protocol.Bead) bool {
	if !strings.EqualFold(bead.Type, "epic") {
		return true
	}
	hasChildren, err := d.beads.HasChildren(ctx, bead.ID)
	if err != nil {
		_ = d.logEvent(ctx, "epic_has_children_error", "dispatcher", bead.ID, "", err.Error())
		return false
	}
	if !hasChildren {
		return true
	}
	if d.beforeAssignmentSideEffectAdmission != nil {
		d.beforeAssignmentSideEffectAdmission()
	}
	admission, err := d.acquireAssignmentSideEffectAdmission(ctx, bead.ID, "", "bulk_epic_validation")
	if err != nil || admission == nil {
		return false
	}
	d.processEpicSkip(ctx, bead)
	d.releaseAssignmentSideEffectAdmission(ctx, admission)
	return false
}

func (d *Dispatcher) filterRecoveryQuarantinedBeads(ctx context.Context, allBeads []protocol.Bead) []protocol.Bead {
	if len(allBeads) == 0 || d.db == nil {
		return allBeads
	}
	tracked, err := d.openRecoveryQuarantineBeads(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "recovery_quarantine_filter_failed", "dispatcher", "", "", err.Error())
		return nil
	}
	blocking, err := d.blockingRecoveryQuarantineBeads(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "recovery_quarantine_filter_failed", "dispatcher", "", "", err.Error())
		return nil
	}
	if len(tracked) == 0 && len(blocking) == 0 {
		return allBeads
	}
	redeployable, err := d.autoRedeployablePreservedWorktrees(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "recovery_quarantine_redeploy_inspection_failed", "dispatcher", "", "", err.Error())
		return nil
	}
	filtered := make([]protocol.Bead, 0, len(allBeads))
	for _, bead := range allBeads {
		if blocking[bead.ID] {
			if redeployable[bead.ID] {
				filtered = append(filtered, bead)
				continue
			}
			_ = d.logEvent(ctx, "recovery_quarantined_bead_skipped", "dispatcher", bead.ID, "",
				`{"reason":"open_recovery_quarantine"}`)
			continue
		}
		filtered = append(filtered, bead)
	}
	return filtered
}

func (d *Dispatcher) openRecoveryQuarantineBeads(ctx context.Context) (map[string]bool, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT DISTINCT q.bead_id
FROM recovery_quarantines q
LEFT JOIN assignments a ON a.id=q.assignment_id
WHERE q.status IN ('open', 'human_owned')
   OR (q.status='resolved' AND a.status='requeued' AND q.reason != 'branch_worktree_mismatch')`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("query open recovery quarantines: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]bool)
	for rows.Next() {
		var beadID string
		if err := rows.Scan(&beadID); err != nil {
			return nil, fmt.Errorf("scan open recovery quarantine: %w", err)
		}
		out[beadID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open recovery quarantines: %w", err)
	}
	return out, nil
}

// blockingRecoveryQuarantineBeads returns only unresolved recovery work that
// must stay out of normal assignment. Resolved requeue-preserved records remain
// visible to worktree reuse and garbage collection, but must not suppress a
// fresh assignment when their preserved worktree is no longer redeployable.
func (d *Dispatcher) blockingRecoveryQuarantineBeads(ctx context.Context) (map[string]bool, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT DISTINCT bead_id
FROM recovery_quarantines
WHERE status IN ('open', 'human_owned')`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("query blocking recovery quarantines: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]bool)
	for rows.Next() {
		var beadID string
		if err := rows.Scan(&beadID); err != nil {
			return nil, fmt.Errorf("scan blocking recovery quarantine: %w", err)
		}
		out[beadID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate blocking recovery quarantines: %w", err)
	}
	return out, nil
}

func (d *Dispatcher) assignmentCandidatesLocked(allBeads []protocol.Bead, now time.Time) []protocol.Bead {
	// Build the set of open bead IDs for dependency resolution.
	// A bead is "open" (can block others) if it is not closed.
	openBeadIDs := make(map[string]bool, len(allBeads))
	for _, b := range allBeads {
		if b.Status != "closed" {
			openBeadIDs[b.ID] = true
		}
	}

	// Collect bead IDs already assigned to busy/reserved workers.
	activeBeads := make(map[string]bool)
	for _, w := range d.workers {
		if w.beadID != "" && w.state != protocol.WorkerIdle {
			activeBeads[w.beadID] = true
		}
	}

	// First pass: cheap in-memory filters (no I/O). Lock is held.
	candidates := make([]protocol.Bead, 0, len(allBeads))
	for _, b := range allBeads {
		if d.isBeadAssignable(b, now, activeBeads) && !hasUnresolvedBlockingDep(b, openBeadIDs) {
			candidates = append(candidates, b)
		}
	}
	return candidates
}

func (d *Dispatcher) filterAlreadyMergedBranches(ctx context.Context, candidates []protocol.Bead) []protocol.Bead {
	// Second pass: check whether the agent branch is already merged to the
	// branch this bead would target if assigned.
	// This requires a git subprocess, so it runs outside the lock.
	out := make([]protocol.Bead, 0, len(candidates))
	for _, b := range candidates {
		targetBranch, err := d.assignmentTargetBranch(ctx, b)
		if err != nil {
			_ = d.logEvent(ctx, "assignment_target_resolve_error", "dispatcher", b.ID, "", err.Error())
			out = append(out, b)
			continue
		}
		if d.isBranchMergedInto(ctx, b.ID, targetBranch) {
			_ = d.CloseBead(ctx, b.ID, fmt.Sprintf("branch already merged to %s", targetBranch))
			_ = d.logEvent(ctx, "bead_branch_already_merged", "dispatcher", b.ID, "", "")
			continue
		}
		out = append(out, b)
	}
	return out
}

func (d *Dispatcher) assignmentTargetBranch(ctx context.Context, bead protocol.Bead) (string, error) {
	defaultBranch := d.cfg.DefaultBranch
	if bead.Metadata != nil {
		if v, ok := bead.Metadata[MetaBranch]; ok {
			if s, ok := v.(string); ok && s != "" {
				defaultBranch = s
			}
		}
	}
	targetBranch, _, err := resolveEpicBranch(ctx, d.beads, bead.Epic, defaultBranch)
	if err != nil {
		return "", err
	}
	return targetBranch, nil
}

// processEpicSkip handles an epic found in the ready queue that must not be
// assigned to a worker. It logs non_executable_issue_type and checks whether
// all children are done so the epic can be auto-closed (fallback path for epics
// whose last child completed before the epic status was updated).
func (d *Dispatcher) processEpicSkip(ctx context.Context, bead protocol.Bead) {
	d.mu.Lock()
	alreadyLogged := d.epicSkipLogged[bead.ID]
	if !alreadyLogged {
		d.epicSkipLogged[bead.ID] = true
	}
	d.mu.Unlock()
	if !alreadyLogged {
		_ = d.logEvent(ctx, "non_executable_issue_type", "dispatcher", bead.ID, "",
			`{"reason":"non_executable_issue_type","issue_type":"epic"}`)
	}
	hasChildren, err := d.beads.HasChildren(ctx, bead.ID)
	if err != nil || !hasChildren {
		return
	}
	allClosed, err := d.beads.AllChildrenClosed(ctx, bead.ID)
	if err != nil || !allClosed {
		return
	}
	targetBranch := resolveEpicTargetBranch(bead.Metadata, d.cfg.DefaultBranch)
	d.completeEpicClose(ctx, bead.ID, "", "All children completed", targetBranch)
}

// isBranchMergedInto reports whether agent/<beadID> represents work that has
// been merged into targetBranch. A branch is considered merged only when it
// (1) has at least one commit beyond its merge-base with targetBranch AND
// (2) is an ancestor of targetBranch.
//
// The empty-branch guard (1) prevents a destructive false positive: a stale
// agent branch sitting at a commit already in targetBranch's history (e.g., the
// worker never committed implementation) would otherwise satisfy --is-ancestor
// trivially, causing the dispatcher to close the bead as "branch already
// merged" and orphan any earlier worker's implementation commits. Returns false
// when the branch does not exist or any git command fails.
func (d *Dispatcher) isBranchMergedInto(ctx context.Context, beadID, targetBranch string) bool {
	branch := protocol.BranchPrefix + beadID // "agent/<beadID>"
	tipOut, err := d.commandRunner().Run(ctx, "git", "rev-parse", branch)
	if err != nil {
		return false
	}
	baseOut, err := d.commandRunner().Run(ctx, "git", "merge-base", branch, targetBranch)
	if err != nil {
		return false
	}
	if strings.TrimSpace(string(tipOut)) == strings.TrimSpace(string(baseOut)) {
		return false
	}
	_, err = d.commandRunner().Run(ctx, "git", "merge-base", "--is-ancestor", branch, targetBranch)
	return err == nil
}

// isBeadAssignable reports whether a bead passes all assignment filters.
// Caller must hold d.mu. activeBeads maps bead IDs held by non-idle workers.
// Epics are allowed through here; HasChildren is checked in assignBead (requires I/O).
func (d *Dispatcher) isBeadAssignable(b protocol.Bead, now time.Time, activeBeads map[string]bool) bool {
	if b.Status == "closed" {
		return false
	}
	// oro-wee1: Filter out beads with status in_progress (human-owned) or blocked.
	// Only beads with status "open" or empty (defaulting to open) should be assignable.
	if b.Status == "in_progress" || b.Status == "blocked" {
		return false
	}
	if failedAt, ok := d.worktreeFailures[b.ID]; ok && now.Sub(failedAt) < worktreeFailureCooldown {
		return false
	}
	if activeBeads[b.ID] {
		return false
	}
	// oro-30o: Skip beads currently in-flight (assigningBeads set but worker not yet
	// transitioned to Busy). This prevents the scale-up duplicate assignment window
	// where a newly connected worker picks up a bead already being assigned to
	// another worker, causing a worktree_error (branch already exists).
	if d.assigningBeads[b.ID] {
		return false
	}
	// Skip beads currently being merged and closed. There's a race window
	// between mergeAndComplete setting mergingBeads and oro task close propagating
	// the status change — without this check the task appears "ready" to
	// oro task ready --json and gets re-assigned, causing bead_closed_externally spam.
	if d.mergingBeads[b.ID] {
		return false
	}
	if d.exhaustedBeads[b.ID] {
		return false
	}
	return true
}

// hasUnresolvedBlockingDep reports whether bead b has at least one unresolved
// blocking dependency. A dependency is blocking when its Type is "blocks" or
// "conditional-blocks" AND its DependsOnID is present in openBeadIDs (i.e. not
// yet closed). Parent-child deps and dangling deps (DependsOnID absent from
// openBeadIDs) are never considered blocking.
func hasUnresolvedBlockingDep(b protocol.Bead, openBeadIDs map[string]bool) bool {
	for _, dep := range b.Dependencies {
		if dep.Type != "blocks" && dep.Type != "conditional-blocks" {
			continue
		}
		if openBeadIDs[dep.DependsOnID] {
			return true
		}
	}
	return false
}

// recordAssignmentFailure marks a bead as having failed assignment (worktree
// creation error, missing acceptance criteria, etc). The bead will be skipped
// for worktreeFailureCooldown to prevent infinite retry loops.
func (d *Dispatcher) recordAssignmentFailure(beadID string) {
	d.mu.Lock()
	d.worktreeFailures[beadID] = d.nowFunc()
	d.mu.Unlock()
}

// checkPriorityContention is no longer used. Priority contention is now handled
// by the preemption system (oro-wofg). Removed in oro-721i.

// assignBead creates a worktree and sends ASSIGN to the worker.
// If memories exist for the bead's description, they are included in the
// AssignPayload.MemoryContext field for cross-session continuity.
// checkBeadReady validates bead ID and acceptance criteria. Returns title,
// acceptance, and true if the bead is ready for assignment. Escalates to
// manager if AC is missing.
func (d *Dispatcher) checkBeadReady(ctx context.Context, bead protocol.Bead, workerID string) (title, acceptance string, ok bool) {
	if err := protocol.ValidateBeadID(bead.ID); err != nil {
		_ = d.logEvent(ctx, "invalid_bead_id", "dispatcher", bead.ID, workerID,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return "", "", false
	}
	title, acceptance, status := d.lookupBeadDetail(ctx, bead.ID, workerID)
	if status == "closed" || status == "in_progress" {
		_ = d.logEvent(ctx, "bead_not_ready_before_assign", "dispatcher", bead.ID, workerID,
			fmt.Sprintf("bead status %q — skipping assignment", status))
		return title, acceptance, false
	}
	if acceptance == "" {
		_ = d.logEvent(ctx, "bead_skipped_missing_ac", "dispatcher", bead.ID, workerID,
			`{"reason":"missing_acceptance"}`)
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscMissingAC, bead.ID, "no acceptance criteria — spawning AC writer", ""), bead.ID, workerID)
		d.recordAssignmentFailure(bead.ID) // 60-second cooldown prevents re-triggering
		return title, "", false            // skip assignment this cycle
	}
	if !strings.EqualFold(bead.Type, "epic") {
		if executable, reason := isWorkerExecutableBead(bead, protocol.BeadDetail{AcceptanceCriteria: acceptance}); !executable {
			_ = d.logEvent(ctx, "bead_skipped_non_tdd_acceptance", "dispatcher", bead.ID, workerID,
				fmt.Sprintf(`{"reason":%q}`, reason))
			if reason == "non_tdd_acceptance" {
				d.escalate(ctx, protocol.FormatEscalation(protocol.EscNonTDDAC, bead.ID,
					fmt.Sprintf("priority %d bead has Cmd/Assert without 'Test:' prefix — rewrite acceptance or move out of worker queue (oro-5833)", bead.Priority), ""), bead.ID, workerID)
			}
			d.recordAssignmentFailure(bead.ID)
			return title, "", false
		}
	}
	// The oversized admission gate was removed here. It counted distinct
	// directories cited in the acceptance criteria's "Read:" lines, which
	// measures how thoroughly the criteria were researched — not how large the
	// change is. A well-cited task was rejected; a task with no Read: line
	// counted zero and always passed. It fired 1,104 times, and its only remedy
	// was decomposition into more tasks, each paying full quality-gate and
	// review cost. Oversized work is now caught at review instead.
	return title, acceptance, true
}

func isWorkerExecutableBead(bead protocol.Bead, detail protocol.BeadDetail) (executable bool, reason string) {
	if strings.EqualFold(bead.Type, "epic") {
		return false, "non_executable_type"
	}
	if strings.TrimSpace(detail.AcceptanceCriteria) == "" {
		return false, "missing_acceptance"
	}
	hasTest := strings.Contains(detail.AcceptanceCriteria, "Test:")
	hasOperationalMarker := strings.Contains(detail.AcceptanceCriteria, "Cmd:") ||
		strings.Contains(detail.AcceptanceCriteria, "Assert:")
	if !hasTest && hasOperationalMarker {
		return false, "non_tdd_acceptance"
	}
	return true, ""
}

// handleEpicBranchMissing checks if an epic branch is missing and decides whether to
// escalate or retry based on epic status. Handles all cases and returns early from assignBead.
func (d *Dispatcher) handleEpicBranchMissing(ctx context.Context, bead protocol.Bead, w *trackedWorker,
	baseBranch string, resolvedEpicID string, branchCheckErr error,
) {
	// Before escalating, check the epic's status to decide if this is
	// a genuine problem or a transient state.
	epicDetail, showErr := d.beads.Show(ctx, resolvedEpicID)

	// If Show returns an error, this is transient (e.g., DB issue).
	// Log and return without escalating — will retry next cycle.
	if showErr != nil {
		_ = d.logEvent(ctx, "epic_show_error", "dispatcher", bead.ID, w.id,
			fmt.Sprintf("error fetching epic %s: %v", resolvedEpicID, showErr))
		_ = d.updateBeadStatus(ctx, bead.ID, "open")
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		return
	}

	// If Show returns nil detail with no error, treat as error (retry).
	if epicDetail == nil {
		_ = d.logEvent(ctx, "epic_show_error", "dispatcher", bead.ID, w.id,
			fmt.Sprintf("epic %s returned nil detail", resolvedEpicID))
		_ = d.updateBeadStatus(ctx, bead.ID, "open")
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		return
	}

	// Check epic status to decide whether to escalate or retry.
	// open: epic not yet assigned → don't escalate, retry next cycle
	// blocked: epic is blocked → don't escalate, skip for now
	// in_progress: epic being worked on, branch missing → escalate (genuine problem)
	// closed: epic finished, branch missing → escalate (genuine problem)
	switch epicDetail.Status {
	case "open", "blocked":
		// Epic not yet assigned or is blocked; branch will be created when epic is worked.
		// Return without escalating — will retry.
		_ = d.logEvent(ctx, "epic_branch_pending", "dispatcher", bead.ID, w.id,
			fmt.Sprintf("epic %s in %s status, branch not yet created", resolvedEpicID, epicDetail.Status))
		_ = d.updateBeadStatus(ctx, bead.ID, "open")
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		return
	}

	// For in_progress and closed statuses, escalate — branch should exist.
	reason := fmt.Sprintf("epic branch %q not found for bead %s", baseBranch, bead.ID)
	if branchCheckErr != nil {
		reason = fmt.Sprintf("checking epic branch %q: %v", baseBranch, branchCheckErr)
	}
	_ = d.logEvent(ctx, "epic_branch_missing", "dispatcher", bead.ID, w.id, reason)
	d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuckWorker, bead.ID, "epic branch missing", reason), bead.ID, w.id)
	_ = d.updateBeadStatus(ctx, bead.ID, "open")
	d.mu.Lock()
	delete(d.assigningBeads, bead.ID)
	d.mu.Unlock()
}

// ensureEpicBranchReady checks whether baseBranch exists and, if not, creates it
// lazily. Returns true if assignment should proceed, false if it should abort.
// When BranchExists itself fails, the existing handleEpicBranchMissing path is
// preserved. When the branch is simply absent and resolvedEpicID is non-empty,
// lazyCreateEpicBranch is attempted.
func (d *Dispatcher) ensureEpicBranchReady(ctx context.Context, bead protocol.Bead, w *trackedWorker, baseBranch, resolvedEpicID string) bool {
	exists, beErr := d.worktrees.BranchExists(ctx, baseBranch)
	if beErr != nil {
		// BranchExists itself failed (git broken) — preserve existing retry/escalate behavior.
		d.handleEpicBranchMissing(ctx, bead, w, baseBranch, resolvedEpicID, beErr)
		return false
	}
	// resolvedEpicID != "" guards against MetaBranch custom targets (e.g. "develop")
	// that resolve with an empty epic ID — those skip lazy creation.
	if !exists && resolvedEpicID != "" {
		return d.lazyCreateEpicBranch(ctx, bead.ID, baseBranch)
	}
	if exists && resolvedEpicID != "" {
		if !d.prepareEpicBranchForAssignment(ctx, bead.ID, w.id, baseBranch) {
			return false
		}
	}
	return true
}

func (d *Dispatcher) prepareEpicBranchForAssignment(ctx context.Context, beadID, workerID, baseBranch string) bool {
	preparer, ok := d.worktrees.(assignmentBaseBranchPreparer)
	if !ok {
		return true
	}
	fastForwarded, err := preparer.PrepareBaseBranchForAssignment(ctx, baseBranch, d.cfg.DefaultBranch)
	if err != nil {
		return d.rejectEpicBranchPreparation(ctx, beadID, workerID, baseBranch, err)
	}
	if fastForwarded {
		_ = d.logEvent(ctx, "epic_branch_fast_forwarded", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"base_branch":%q}`, baseBranch, d.cfg.DefaultBranch))
	}
	checker, ok := d.worktrees.(assignmentBaseBranchSafetyChecker)
	if !ok {
		return true
	}
	diverged, err := assignmentBaseBranchDiverged(ctx, checker, baseBranch, d.cfg.DefaultBranch)
	if err != nil {
		return d.rejectEpicBranchPreparation(ctx, beadID, workerID, baseBranch, err)
	}
	if !diverged {
		return true
	}
	if d.isEpicRebaseChildForBase(ctx, beadID, baseBranch) {
		_ = d.logEvent(ctx, "epic_rebase_child_prepare_diverged", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"base_branch":%q}`, baseBranch, d.cfg.DefaultBranch))
		return true
	}
	epicID := strings.TrimPrefix(baseBranch, protocol.EpicBranchPrefix)
	if d.tryDeterministicEpicRebase(ctx, epicID, workerID, baseBranch, d.cfg.DefaultBranch) {
		_ = d.logEvent(ctx, "epic_deterministic_rebase_prepare_diverged", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"base_branch":%q}`, baseBranch, d.cfg.DefaultBranch))
		return true
	}
	divergenceErr := fmt.Errorf("epic branch %s diverged from %s", baseBranch, d.cfg.DefaultBranch)
	if _, ensureErr := d.ensureEpicRebaseChild(ctx, epicID, baseBranch, d.cfg.DefaultBranch, divergenceErr.Error()); ensureErr != nil {
		_ = d.logEvent(ctx, "epic_rebase_child_ensure_failed", "dispatcher", beadID, workerID, ensureErr.Error())
	}
	return d.rejectEpicBranchPreparation(ctx, beadID, workerID, baseBranch, divergenceErr)
}

func assignmentBaseBranchDiverged(ctx context.Context, checker assignmentBaseBranchSafetyChecker, branch, baseBranch string) (bool, error) {
	branchHasUniqueCommits, err := checker.BaseBranchHasUniqueCommits(ctx, branch, baseBranch)
	if err != nil {
		return false, fmt.Errorf("check unique commits on %s relative to %s: %w", branch, baseBranch, err)
	}
	if !branchHasUniqueCommits {
		return false, nil
	}
	baseHasUniqueCommits, err := checker.BaseBranchHasUniqueCommits(ctx, baseBranch, branch)
	if err != nil {
		return false, fmt.Errorf("check unique commits on %s relative to %s: %w", baseBranch, branch, err)
	}
	return baseHasUniqueCommits, nil
}

func (d *Dispatcher) rejectEpicBranchPreparation(ctx context.Context, beadID, workerID, baseBranch string, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	_ = d.logEvent(ctx, "epic_branch_prepare_failed", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"branch":%q,"base_branch":%q,"error":%q}`, baseBranch, d.cfg.DefaultBranch, err.Error()))
	_ = d.updateBeadStatus(ctx, beadID, "open")
	d.mu.Lock()
	delete(d.assigningBeads, beadID)
	d.mu.Unlock()
	d.recordAssignmentFailure(beadID)
	return false
}

// lazyCreateEpicBranch creates baseBranch from d.cfg.DefaultBranch when it is
// absent. Returns true if the caller should continue with assignment, false if
// the creation failed genuinely (bead reverted, failure recorded, escalation sent).
func (d *Dispatcher) lazyCreateEpicBranch(ctx context.Context, beadID, baseBranch string) bool {
	return d.lazyCreateEpicBranchFrom(ctx, beadID, baseBranch, d.cfg.DefaultBranch)
}

func (d *Dispatcher) lazyCreateEpicBranchFrom(ctx context.Context, beadID, baseBranch, targetBranch string) bool {
	if err := d.worktrees.CreateBranch(ctx, baseBranch, targetBranch); err != nil {
		if ctx.Err() != nil {
			return false
		}
		// Branch may already exist due to a concurrent child assignment (race) — re-check.
		exists2, _ := d.worktrees.BranchExists(ctx, baseBranch)
		if !exists2 {
			// Genuine failure (permissions, disk) — revert bead and escalate.
			_ = d.logEvent(ctx, "epic_branch_create_failed", "dispatcher", beadID, "",
				fmt.Sprintf(`{"branch":%q,"error":%q}`, baseBranch, err.Error()))
			_ = d.updateBeadStatus(ctx, beadID, "open")
			d.mu.Lock()
			delete(d.assigningBeads, beadID)
			d.mu.Unlock()
			d.recordAssignmentFailure(beadID)
			d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuckWorker, beadID,
				"epic branch creation failed", err.Error()), beadID, "")
			return false
		}
		// Race resolved — another goroutine created the branch first.
		_ = d.logEvent(ctx, "epic_branch_race_resolved", "dispatcher", beadID, "",
			fmt.Sprintf(`{"branch":%q}`, baseBranch))
		return true
	}
	_ = d.logEvent(ctx, "epic_branch_created", "dispatcher", beadID, "",
		fmt.Sprintf(`{"branch":%q,"from":%q}`, baseBranch, targetBranch))
	return true
}
