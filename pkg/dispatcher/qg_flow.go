package dispatcher

import (
	"context"
	"fmt"
	"strings"

	"oro/pkg/agentmodel"
	"oro/pkg/leakscan"
	"oro/pkg/protocol"
)

// handleQGStuckDetected handles the case where a bead has produced the same QG
// output enough consecutive times to be considered stuck. The repeated identical
// output proves the current approach isn't working, so we classify with
// RetryExhausted=true so deterministic failures get ReopenOriginal routing.
func (d *Dispatcher) handleQGStuckDetected(ctx context.Context, workerID, beadID, qgOutput, qgFingerprint, qgSummary string) {
	_ = d.logEvent(ctx, "qg_stuck_detected", workerID, beadID, workerID,
		fmt.Sprintf(`{"repeated_count":%d}`, maxStuckCount))
	d.mu.Lock()
	assignmentID := d.assignmentIDLocked(workerID, beadID)
	d.mu.Unlock()
	stuckRec := QGFailureRecord{
		BeadID:       beadID,
		WorkerID:     workerID,
		AssignmentID: assignmentID,
		Component:    "worker",
		Fingerprint:  qgFingerprint,
		Summary:      qgSummary,
		Output:       qgOutput,
	}
	stuckCls := d.classifyQGFailure(ctx, stuckRec, QGFailureHistory{RetryExhausted: true})
	d.handleRepeatedQGOutput(ctx, workerID, beadID, stuckRec, stuckCls)
}

// handleQGFailure processes a quality-gate failure: checks for stuck detection
// (repeated identical outputs), increments the attempt counter, escalates if
// either cap is reached, or re-assigns with feedback.
func (d *Dispatcher) handleQGFailure(ctx context.Context, workerID, beadID, qgOutput string) {
	d.touchProgress(workerID)

	qg := d.evaluateQGFailure(ctx, workerID, beadID, qgOutput)
	d.logQGFailureRejection(ctx, workerID, beadID, qg)

	// Check stuck detection: hash QGOutput and track consecutive identical hashes.
	if d.isQGStuck(beadID, qgOutput) {
		d.handleQGStuckDetected(ctx, workerID, beadID, qgOutput, qg.record.Fingerprint, qg.record.Summary)
		return
	}
	if qg.targetBaselineFailure() {
		d.handleSystemicQGExhaustion(
			ctx,
			workerID,
			beadID,
			qg.record.AssignmentID,
			qg.record,
			qg.classification,
		)
		return
	}

	// Transient and flaky failures use backoff retry — they do not increment
	// attemptCounts and therefore do not burn the worker-fix retry budget.
	if qg.classification.Decision == QGFailureDecisionBackoffRetry {
		d.handleTransientQGFailure(ctx, workerID, beadID, qg.record, qg.classification)
		return
	}

	retry := d.reserveQGRetryAttempt(workerID, beadID, qg.err)
	if retry.exhausted {
		d.handleQGExhausted(ctx, workerID, beadID, retry.assignmentID, qgOutput, retry.attempt)
		return
	}
	if d.stopQGRetryForBlockingDependency(ctx, workerID, beadID, retry.assignmentID, retry.attempt) {
		return
	}

	d.recordQGFailureIncident(ctx, workerID, beadID, retry.assignmentID, retry.attempt, qgOutput, qg.record.Fingerprint, qg.record.Summary, qg.classification)
	d.persistBeadCount(ctx, retry.assignmentID, beadID, "attempt_count", retry.attempt)
	d.qgRetryWithReservation(ctx, workerID, beadID, qgOutput, retry.attempt)
}

// stopQGRetryForBlockingDependency releases a failed QG assignment when the
// parent gained an unresolved blocker while the worker was running. A failed
// dependency lookup is handled conservatively so an unchanged retry cannot
// consume capacity while the scheduler's readiness view is unknown.
func (d *Dispatcher) stopQGRetryForBlockingDependency(ctx context.Context, workerID, beadID string, assignmentID int64, attempt int) bool {
	blockerID, lookupErr := d.qgRetryBlockingDependency(ctx, beadID)
	if blockerID == "" && lookupErr == nil {
		return false
	}
	if lookupErr != nil {
		_ = d.logEvent(ctx, "qg_retry_dependency_lookup_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"attempt":%d}`, lookupErr.Error(), attempt))
	}
	if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
		_ = d.logEvent(ctx, "qg_retry_blocked_status_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"attempt":%d}`, err.Error(), attempt))
	}
	if err := d.completeAssignment(ctx, assignmentID, beadID); err != nil {
		_ = d.logEvent(ctx, "qg_retry_blocked_assignment_cleanup_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"attempt":%d}`, err.Error(), attempt))
	}
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
	d.clearBeadTracking(beadID)
	_ = d.logEvent(ctx, "qg_retry_blocked_by_dependency", workerID, beadID, workerID,
		fmt.Sprintf(`{"blocker_id":%q,"attempt":%d,"lookup_failed":%t}`, blockerID, attempt, lookupErr != nil))
	return true
}

func (d *Dispatcher) qgRetryBlockingDependency(ctx context.Context, beadID string) (string, error) {
	bead, err := d.beads.Show(ctx, beadID)
	if err != nil {
		return "", fmt.Errorf("show retry bead: %w", err)
	}
	if bead == nil {
		return "", fmt.Errorf("show retry bead: bead %q not found", beadID)
	}
	for _, dep := range bead.Dependencies {
		if dep.Type != "blocks" && dep.Type != "conditional-blocks" {
			continue
		}
		blockingBead, err := d.beads.Show(ctx, dep.DependsOnID)
		if err != nil {
			return "", fmt.Errorf("show dependency %q: %w", dep.DependsOnID, err)
		}
		if blockingBead != nil && blockingBead.Status != "closed" {
			return dep.DependsOnID, nil
		}
	}
	return "", nil
}

type qgFailureEvaluation struct {
	err            *protocol.QualityGateError
	record         QGFailureRecord
	attribution    QGFailureAttribution
	classification QGFailureClassification
}

func (q qgFailureEvaluation) targetBaselineFailure() bool {
	return q.classification.Decision == QGFailureDecisionCreateOrReuseInfra &&
		q.classification.Confidence == QGFailureConfidenceHigh &&
		targetBaselineHasFailure(q.record, q.attribution)
}

func (d *Dispatcher) evaluateQGFailure(ctx context.Context, workerID, beadID, qgOutput string) qgFailureEvaluation {
	fingerprint, summary := FingerprintQGFailure(qgOutput, QGFingerprintOptions{})
	d.mu.Lock()
	assignmentID := d.assignmentIDLocked(workerID, beadID)
	d.mu.Unlock()
	record := QGFailureRecord{
		BeadID:       beadID,
		WorkerID:     workerID,
		AssignmentID: assignmentID,
		Component:    "worker",
		Fingerprint:  fingerprint,
		Summary:      summary,
		Output:       qgOutput,
	}
	attribution := d.qgFailureAttribution(ctx, workerID, record)
	return qgFailureEvaluation{
		err: &protocol.QualityGateError{
			BeadID:   beadID,
			WorkerID: workerID,
			Output:   qgOutput,
		},
		record:         record,
		attribution:    attribution,
		classification: d.classifyQGFailureWithAttribution(ctx, record, QGFailureHistory{}, attribution),
	}
}

func (d *Dispatcher) logQGFailureRejection(ctx context.Context, workerID, beadID string, qg qgFailureEvaluation) {
	_ = d.logEvent(ctx, "quality_gate_rejected", workerID, beadID, workerID,
		fmt.Sprintf(`{"reason":"QualityGatePassed=false","error":%q,"fingerprint":%q,"summary":%q,"class":%q,"decision":%q,"confidence":%q,"classification_reason":%q}`,
			qg.err.Error(), qg.record.Fingerprint, qg.record.Summary, qg.classification.Class, qg.classification.Decision, qg.classification.Confidence, qg.classification.Reason))
}

type qgRetryAttempt struct {
	attempt      int
	assignmentID int64
	exhausted    bool
}

func (d *Dispatcher) reserveQGRetryAttempt(workerID, beadID string, qgErr *protocol.QualityGateError) qgRetryAttempt {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.attemptCounts[beadID]++
	attempt := d.attemptCounts[beadID]
	qgErr.Attempt = attempt
	assignmentID := d.assignmentIDLocked(workerID, beadID)
	if attempt >= maxQGRetries {
		return qgRetryAttempt{attempt: attempt, assignmentID: assignmentID, exhausted: true}
	}
	if w, ok := d.workers[workerID]; ok {
		w.state = protocol.WorkerReserved
	}
	return qgRetryAttempt{attempt: attempt, assignmentID: assignmentID}
}

func (d *Dispatcher) recordQGFailureIncident(ctx context.Context, workerID, beadID string, assignmentID int64, attempt int, output, fingerprint, summary string, cls QGFailureClassification) {
	rec := QGFailureRecord{
		ID:           fmt.Sprintf("%s:%s:%d:%d", beadID, workerID, assignmentID, attempt),
		BeadID:       beadID,
		WorkerID:     workerID,
		AssignmentID: assignmentID,
		Component:    "worker",
		Fingerprint:  fingerprint,
		Summary:      summary,
		Output:       output,
	}
	rec = normalizeQGFailureRecord(rec)
	incident, err := RecordQGFailureOccurrence(ctx, d.db, rec, cls)
	if err != nil {
		_ = d.logEvent(ctx, "qg_failure_record_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q}`, err.Error(), fingerprint))
		return
	}
	if err := d.linkQGFailureToBeads(ctx, incident, rec, cls); err != nil {
		_ = d.logEvent(ctx, "qg_failure_link_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q,"incident_id":%d}`, err.Error(), fingerprint, incident.ID))
	}
	d.rememberQGRetryContext(workerID, rec, attempt)
}

// withReservation executes a two-phase reservation pattern for worker re-assignment:
// Phase 1 (caller): Reserve the worker (set state to WorkerReserved) under lock.
// Phase 2 (this helper): Run ioFn outside lock, then verify reservation still valid
// and call assignFn under lock. The worker must already be in WorkerReserved state
// before calling this helper.
//
// ioFn performs I/O operations (e.g., memory retrieval) and returns context string.
// assignFn receives the worker and I/O result, updates state, and sends ASSIGN message.
// assignFn returns true if the assignment succeeded, false if it failed.
//
// Returns true if assignment succeeded, false if worker was disconnected or assignment failed.
func (d *Dispatcher) withReservation(workerID string, ioFn func() string, assignFn func(w *trackedWorker, memCtx string) bool) bool {
	// I/O phase: run outside lock to avoid blocking other operations.
	if d.testUnlockHook != nil {
		d.testUnlockHook()
	}
	memCtx := ioFn()

	d.mu.Lock()
	defer d.mu.Unlock()

	// Phase 2: Verify reservation still valid, then call assignFn.
	w, ok := d.workers[workerID]
	if !ok || w.state != protocol.WorkerReserved || w.reviewReleaseToken != 0 {
		return false
	}

	return assignFn(w, memCtx)
}

// qgRetryWithReservation performs the I/O phase (memory retrieval) and
// completes the two-phase reservation for a QG retry. The worker must already
// be in protocol.WorkerReserved state before this is called.
func (d *Dispatcher) qgRetryWithReservation(ctx context.Context, workerID, beadID, qgOutput string, attempt int) {
	// Capture a snapshot for buildAssignPayload (I/O runs outside lock).
	// Always set model=Opus on the snapshot — QG retry always escalates.
	d.mu.Lock()
	snap := d.opusEscalationSnapshotLocked(workerID)
	d.mu.Unlock()

	var payload *protocol.AssignPayload
	success := d.withReservation(workerID,
		// I/O function: build full payload outside lock.
		func() string {
			if d.cfg.RegressionRevert {
				if _, err := d.seedQGBaselineFromFailure(ctx, beadID, snap.worktree, qgOutput); err != nil {
					_ = d.logEvent(ctx, "qg_baseline_capture_failed", workerID, beadID, workerID,
						fmt.Sprintf(`{"error":%q,"attempt":%d}`, err.Error(), attempt))
				}
			}
			payload = d.buildAssignPayload(ctx, &snap, attempt, qgOutput, "", snap.execution)
			return ""
		},
		// Assign function: update state and send message under lock.
		func(w *trackedWorker, memCtx string) bool {
			// Escalate runtime+model+reasoning together.
			w.runtime, w.model, w.reasoning = agentmodel.ResolveForRole("worker_escalation")
			payload.Runtime = w.runtime
			payload.Model = w.model // sync with live escalated value
			payload.Reasoning = w.reasoning

			if err := d.sendToWorker(w, protocol.Message{
				Type:   protocol.MsgAssign,
				Assign: payload,
			}); err != nil {
				// Worker is unreachable — release the bead back to the ready pool.
				w.state = protocol.WorkerIdle
				w.beadID = ""
				w.epicID = ""
				w.isEpicDecomp = false
				_ = d.logEvent(ctx, "qg_retry_send_failed", workerID, beadID, workerID,
					fmt.Sprintf(`{"error":%q,"attempt":%d}`, err.Error(), attempt))
				_ = d.completeAssignment(ctx, w.assignmentID, beadID)
				return false
			}
			_ = d.logEventLocked(ctx, "qg_retry_assign_sent", workerID, beadID, workerID,
				fmt.Sprintf(`{"attempt":%d,"model":%q}`, attempt, payload.Model))
			delete(d.pendingQGRetries, workerID)
			w.state = protocol.WorkerBusy
			w.beadID = beadID
			w.lastProgress = d.nowFunc()
			return true
		},
	)

	// If assignment failed, clean up tracking state outside the lock.
	if !success && !d.hasPendingQGRetry(workerID, beadID, attempt) {
		d.clearBeadTracking(beadID)
	}
}

// storeRejectionFeedback persists reviewer feedback in the rejection_history
// table (not memories), so rejections accumulate across retry cycles without

func (d *Dispatcher) checkPreMergeLeaks(ctx context.Context, beadID, workerID, worktree, branch, targetBranch string, assignmentID int64) bool {
	cfg := d.cfg.LeakScan
	if !cfg.Enabled {
		return true
	}
	target := targetBranch
	if target == "" {
		target = d.cfg.DefaultBranch
	}
	diff, err := d.commandRunner().Run(ctx, "git", "-C", worktree, "diff", target+".."+branch)
	if err != nil {
		_ = d.logEvent(ctx, "pre_merge_leakscan_error", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"target":%q,"error":%q}`, branch, target, err.Error()))
		return true
	}
	allow, err := d.loadLeakScanAllowlist()
	if err != nil {
		_ = d.logEvent(ctx, "pre_merge_leakscan_error", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"target":%q,"error":%q}`, branch, target, err.Error()))
		return true
	}
	result := scanPreMergeDiff(string(diff), cfg, allow)
	if len(result.Matches) == 0 {
		return true
	}
	summary := leakscan.Summarize(result)
	_ = d.logEvent(ctx, "pre_merge_leakscan_warn", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"branch":%q,"target":%q,"matches":%q}`, branch, target, summary))
	if !preMergeLeakShouldBlock(cfg.BlockOn, result) {
		return true
	}
	return d.blockPreMergeLeak(ctx, beadID, workerID, worktree, branch, assignmentID, summary)
}

func scanPreMergeDiff(diff string, cfg LeakScanConfig, allow leakscan.Allowlist) leakscan.Result {
	if cfg.EntropyMinBits == 0 {
		return leakscan.ScanDiff(diff, leakscan.DefaultPatterns(), allow)
	}
	return leakscan.ScanDiffWithMinEntropy(diff, leakscan.DefaultPatterns(), allow, cfg.EntropyMinBits)
}

func (d *Dispatcher) loadLeakScanAllowlist() (leakscan.Allowlist, error) {
	if d.cfg.LeakScan.AllowlistPath == "" {
		return leakscan.Allowlist{}, nil
	}
	allow, err := leakscan.LoadAllowlist(d.cfg.LeakScan.AllowlistPath)
	if err != nil {
		return leakscan.Allowlist{}, fmt.Errorf("load pre-merge leakscan allowlist: %w", err)
	}
	return allow, nil
}

func preMergeLeakShouldBlock(blockOn string, result leakscan.Result) bool {
	switch strings.ToLower(strings.TrimSpace(blockOn)) {
	case "", "none":
		return false
	case "critical":
		for _, match := range result.Matches {
			if match.Severity == leakscan.SeverityCritical && match.Action == leakscan.ActionBlock {
				return true
			}
		}
		return false
	default:
		return result.ShouldBlock
	}
}

func (d *Dispatcher) blockPreMergeLeak(ctx context.Context, beadID, workerID, worktree, branch string, assignmentID int64, summary string) bool {
	_ = d.logEvent(ctx, "merge_blocked_secret_leak", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"branch":%q,"worktree":%q,"matches":%q}`, branch, worktree, summary))
	d.quarantineUnsafeRecoveryWork(ctx, recoveryQuarantine{
		BeadID:       beadID,
		AssignmentID: assignmentID,
		WorkerID:     workerID,
		Worktree:     worktree,
		Branch:       branch,
		Reason:       "pre_merge_secret_leak",
		Details:      summary,
	})
	d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID, "pre-merge secret leak", summary), beadID, workerID)
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
	return false
}
