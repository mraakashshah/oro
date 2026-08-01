package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"oro/pkg/merge"
	"oro/pkg/protocol"
)

func (d *Dispatcher) guardMerge(beadID string) func() {
	d.mu.Lock()
	d.mergingBeads[beadID] = true
	d.mu.Unlock()
	return func() {
		d.mu.Lock()
		delete(d.mergingBeads, beadID)
		d.mu.Unlock()
	}
}

// classifyQGError returns "systemic" for persistent environment failures (e.g.
// missing quality_gate.sh) and "transient" for recoverable interruptions (e.g.
// context cancellation). Systemic errors trigger work preservation; transient
// errors proceed with standard cleanup.
func classifyQGError(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "systemic"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "transient"
	}
	return "systemic"
}

// handlePreMergeQGError classifies a pre-merge QG infrastructure error, records the
// classification before any cleanup, and performs class-appropriate cleanup.
// Systemic errors preserve the agent branch for human discovery; transient errors
// proceed with full cleanup. Always returns false.
func (d *Dispatcher) handlePreMergeQGError(ctx context.Context, beadID, workerID, worktree string, assignmentID int64, err error) bool {
	class := classifyQGError(err)

	// Record classification before any escalation or cleanup so it is observable
	// even if subsequent steps fail.
	_ = d.logEvent(ctx, "pre_merge_qg_error_classified", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"class":%q,"error":%q}`, class, err.Error()))

	if class == "systemic" {
		d.handleSystemicPreMergeQGError(ctx, beadID, workerID, worktree, assignmentID, err)
		return false
	}

	// Transient: standard escalation and full cleanup.
	d.escalate(ctx,
		protocol.FormatEscalation(protocol.EscStuck, beadID, "pre-merge QG error", err.Error()),
		beadID, workerID)
	if d.shouldReopenQGOriginal(ctx, beadID) {
		_ = d.updateBeadStatus(ctx, beadID, "open")
		_ = d.requeueAssignment(ctx, assignmentID)
	} else {
		_ = d.completeAssignment(ctx, assignmentID, beadID)
		_ = d.logEvent(ctx, "pre_merge_qg_work_preserved", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"worktree":%q,"reason":"external_close_without_merge_proof"}`, protocol.BranchPrefix+beadID, worktree))
	}
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
	return false
}

func (d *Dispatcher) handleSystemicPreMergeQGError(ctx context.Context, beadID, workerID, worktree string, assignmentID int64, err error) {
	branch := protocol.BranchPrefix + beadID
	_ = d.logEvent(ctx, "pre_merge_qg_work_preserved", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"branch":%q,"worktree":%q}`, branch, worktree))
	d.escalate(ctx,
		protocol.FormatEscalation(protocol.EscStuck, beadID, "pre-merge QG systemic error", err.Error()),
		beadID, workerID)
	d.finalizeSystemicPreMergeQGError(ctx, beadID, workerID, assignmentID)
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
}

func (d *Dispatcher) finalizeSystemicPreMergeQGError(ctx context.Context, beadID, workerID string, assignmentID int64) {
	if d.shouldReopenQGOriginal(ctx, beadID) {
		if updateErr := d.updateBeadStatus(ctx, beadID, "open"); updateErr != nil {
			_ = d.logEvent(ctx, "pre_merge_qg_reopen_failed", "dispatcher", beadID, workerID, updateErr.Error())
		}
		if requeueErr := d.requeueAssignment(ctx, assignmentID); requeueErr != nil {
			_ = d.logEvent(ctx, "pre_merge_qg_requeue_failed", "dispatcher", beadID, workerID, requeueErr.Error())
		}
		return
	}
	_ = d.completeAssignment(ctx, assignmentID, beadID)
}

// handlePreMergeQGFailure classifies the pre-merge QG failure output, records the
// occurrence, handles cleanup, and returns false so the caller aborts the merge.
// For deterministic failures it records via RecordQGFailureOccurrence; for systemic
// failures it creates or reuses an infra incident. In both cases the original bead
// is reopened unless it was externally closed before this handler runs.
func (d *Dispatcher) handlePreMergeQGFailure(ctx context.Context, beadID, workerID, worktree string, assignmentID int64, qgOutput string) bool {
	qgFingerprint, qgSummary := FingerprintQGFailure(qgOutput, QGFingerprintOptions{})
	rec := QGFailureRecord{
		ID:           fmt.Sprintf("%s:%s:%d:pre-merge", beadID, workerID, assignmentID),
		BeadID:       beadID,
		WorkerID:     workerID,
		AssignmentID: assignmentID,
		Component:    "pre-merge",
		Fingerprint:  qgFingerprint,
		Summary:      qgSummary,
		Output:       qgOutput,
	}
	cls := d.classifyQGFailure(ctx, rec, QGFailureHistory{RetryExhausted: true})

	_ = d.logEvent(ctx, "qg_failed", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"output":%q,"fingerprint":%q,"class":%q,"decision":%q}`,
			qgOutput, qgFingerprint, cls.Class, cls.Decision))

	if cls.Decision == QGFailureDecisionCreateOrReuseInfra {
		d.recordPreMergeInfraIncident(ctx, rec, cls)
	} else {
		d.recordPreMergeDeterministicFailure(ctx, rec, cls)
	}

	// Only requeue if not already closed on main — a stale QG failure must
	// not reopen a bead that was successfully merged externally.
	if d.shouldReopenQGOriginal(ctx, beadID) {
		_ = d.updateBeadStatus(ctx, beadID, "open")
		_ = d.logEvent(ctx, "pre_merge_qg_work_preserved", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"worktree":%q}`, protocol.BranchPrefix+beadID, worktree))
		_ = d.requeueAssignment(ctx, assignmentID)
	} else {
		_ = d.completeAssignment(ctx, assignmentID, beadID)
		_ = d.logEvent(ctx, "pre_merge_qg_work_preserved", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"worktree":%q,"reason":"external_close_without_merge_proof"}`, protocol.BranchPrefix+beadID, worktree))
	}
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
	return false
}

// recordPreMergeInfraIncident creates or reuses the infra incident for a
// systemic pre-merge QG failure and logs a qg_infra_incident_reused event.
func (d *Dispatcher) recordPreMergeInfraIncident(ctx context.Context, rec QGFailureRecord, cls QGFailureClassification) {
	incident, err := d.createOrReuseQGInfraIncident(ctx, rec, cls)
	if err != nil {
		_ = d.logEvent(ctx, "qg_failure_record_failed", "dispatcher", rec.BeadID, rec.WorkerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q}`, err.Error(), rec.Fingerprint))
		return
	}
	_ = d.logEvent(ctx, "qg_infra_incident_reused", "dispatcher", rec.BeadID, rec.WorkerID,
		fmt.Sprintf(`{"incident_id":%d,"class":%q,"fingerprint":%q}`, incident.ID, cls.Class, rec.Fingerprint))
}

// recordPreMergeDeterministicFailure records the QG occurrence and links it to
// the originating bead. Errors at either step are logged as separate events so
// they remain debuggable without altering the reopen path.
func (d *Dispatcher) recordPreMergeDeterministicFailure(ctx context.Context, rec QGFailureRecord, cls QGFailureClassification) {
	incident, err := RecordQGFailureOccurrence(ctx, d.db, rec, cls)
	if err != nil {
		_ = d.logEvent(ctx, "qg_failure_record_failed", "dispatcher", rec.BeadID, rec.WorkerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q}`, err.Error(), rec.Fingerprint))
		return
	}
	if err := d.linkQGFailureToBeads(ctx, incident, rec, cls); err != nil {
		_ = d.logEvent(ctx, "qg_failure_link_failed", "dispatcher", rec.BeadID, rec.WorkerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q,"incident_id":%d}`, err.Error(), rec.Fingerprint, incident.ID))
	}
}

func (d *Dispatcher) guardQGRegression(ctx context.Context, beadID, workerID, worktree string, assignmentID int64, targetBranch string) bool {
	if !d.cfg.RegressionRevert {
		return true
	}
	base, ok := d.takeQGBaselineForBead(beadID)
	if !ok {
		return true
	}

	regression, err := d.detectQGRegression(ctx, base, worktree, d.qgMutationBase(targetBranch))
	if err != nil {
		return d.handlePreMergeQGError(ctx, beadID, workerID, worktree, assignmentID, err)
	}
	if regression == (qgRegression{}) {
		return true
	}

	if err := d.revertRegressedRetry(ctx, base, worktree); err != nil {
		_ = d.logEvent(ctx, "qg_regression_revert_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"test":%q,"error":%q}`, regression.TestName, err.Error()))
		d.escalate(ctx,
			protocol.FormatEscalation(protocol.EscStuck, beadID, "QG_REGRESSION_REVERT_FAILED", err.Error()),
			beadID, workerID)
		d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
		return false
	}

	_ = d.logEvent(ctx, "qg_regression_reverted", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"test":%q,"baseline_passed":%t,"current_passed":%t}`,
			regression.TestName, regression.BaselinePassed, regression.CurrentPassed))
	if d.shouldReopenQGOriginal(ctx, beadID) {
		_ = d.updateBeadStatus(ctx, beadID, "open")
		_ = d.requeueAssignment(ctx, assignmentID)
	} else {
		_ = d.completeAssignment(ctx, assignmentID, beadID)
	}
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
	return false
}

// checkPreMergeQG runs the local pre-merge quality gate before merging. Mutation
// testing is opt-in so local branch merges do not pay that cost by default.
// It returns true when the gate passes and the merge should proceed. On failure
// or error it handles cleanup and returns false so the caller can return early.
type preMergeQGFailureError struct {
	output string
}

func (e *preMergeQGFailureError) Error() string {
	return "pre-merge quality gate failed"
}

type preMergeQGRunError struct {
	err error
}

func (e *preMergeQGRunError) Error() string {
	return fmt.Sprintf("run pre-merge quality gate: %v", e.err)
}

func (e *preMergeQGRunError) Unwrap() error {
	return e.err
}

// runPreMergeQG executes the dispatcher quality gate for a final candidate
// worktree. It leaves failure handling to its caller, except for regression
// protection, which already performs the required recovery itself.
func (d *Dispatcher) runPreMergeQG(ctx context.Context, beadID, workerID, worktree string, assignmentID int64, targetBranch string) error {
	if err := d.observeStorageController(ctx); err != nil {
		return err
	}
	if !d.storageAdmissionAllowed() {
		return errStorageAdmissionPaused
	}
	mutationBase := d.qgMutationBase(targetBranch)
	if !d.guardQGRegression(ctx, beadID, workerID, worktree, assignmentID, mutationBase) {
		return errPreMergeQGAlreadyHandled
	}
	qgPassed, qgOutput, qgErr := d.qgRunner.Run(ctx, worktree, !d.cfg.MutationTesting, mutationBase)
	if qgErr != nil {
		return &preMergeQGRunError{err: qgErr}
	}
	if !qgPassed {
		return &preMergeQGFailureError{output: qgOutput}
	}
	return nil
}

// checkPreMergeQG preserves the direct local-gate entry point used by the
// existing lifecycle checks. Dispatcher merges invoke runPreMergeQG through
// merge.Opts.PreFFCheck so the gate sees the rebased worktree.
func (d *Dispatcher) checkPreMergeQG(ctx context.Context, beadID, workerID, worktree string, assignmentID int64, targetBranch string) bool {
	err := d.runPreMergeQG(ctx, beadID, workerID, worktree, assignmentID, targetBranch)
	if err == nil {
		return true
	}
	if errors.Is(err, errPreMergeQGAlreadyHandled) {
		return false
	}
	var qgFailure *preMergeQGFailureError
	if errors.As(err, &qgFailure) {
		return d.handlePreMergeQGFailure(ctx, beadID, workerID, worktree, assignmentID, qgFailure.output)
	}
	var qgRunErr *preMergeQGRunError
	if errors.As(err, &qgRunErr) {
		return d.handlePreMergeQGError(ctx, beadID, workerID, worktree, assignmentID, qgRunErr.err)
	}
	return d.handlePreMergeQGError(ctx, beadID, workerID, worktree, assignmentID, err)
}

func (d *Dispatcher) handlePreFFCheckError(ctx context.Context, beadID, workerID, worktree string, assignmentID int64, err error) bool {
	var preFFErr *merge.PreFFCheckError
	if !errors.As(err, &preFFErr) {
		return false
	}
	if errors.Is(preFFErr, errPreMergeQGAlreadyHandled) {
		return true
	}
	var qgFailure *preMergeQGFailureError
	if errors.As(preFFErr, &qgFailure) {
		d.handlePreMergeQGFailure(ctx, beadID, workerID, worktree, assignmentID, qgFailure.output)
		return true
	}
	var qgRunErr *preMergeQGRunError
	if errors.As(preFFErr, &qgRunErr) {
		d.handlePreMergeQGError(ctx, beadID, workerID, worktree, assignmentID, qgRunErr.err)
		return true
	}
	return false
}

func (d *Dispatcher) handleRepeatedQGOutput(ctx context.Context, workerID, beadID string, rec QGFailureRecord, cls QGFailureClassification) {
	_ = d.logEvent(ctx, "qg_repeated_classified", workerID, beadID, workerID,
		fmt.Sprintf(`{"class":%q,"decision":%q,"fingerprint":%q}`,
			cls.Class, cls.Decision, rec.Fingerprint))

	switch cls.Decision {
	case QGFailureDecisionReopenOriginal:
		d.handleClassifiedQGExhaustion(ctx, workerID, beadID, rec.AssignmentID, rec, cls)
	case QGFailureDecisionCreateOrReuseInfra:
		d.handleSystemicQGExhaustion(ctx, workerID, beadID, rec.AssignmentID, rec, cls)
	default:
		_ = d.completeAssignment(ctx, rec.AssignmentID, beadID)
		d.releaseWorkerAfterQGExhaustion(workerID, beadID)
		if d.shouldReopenQGOriginal(ctx, beadID) {
			if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
				_ = d.logEvent(ctx, "qg_repeated_reopen_failed", "dispatcher", beadID, workerID,
					fmt.Sprintf(`{"error":%q}`, err.Error()))
			}
		}
		_ = d.logEvent(ctx, "qg_repeated_triage", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"class":%q,"decision":%q,"fingerprint":%q}`,
				cls.Class, cls.Decision, rec.Fingerprint))
	}
}

// handleQGExhausted handles the case when quality gate retries are exhausted.
// It classifies before creating any follow-up work: deterministic failures stay
// on the original bead, systemic failures reuse/create infra incidents, and
// low-confidence failures stop for triage without creating legacy QG P0 beads.
func (d *Dispatcher) handleQGExhausted(ctx context.Context, workerID, beadID string, assignmentID int64, qgOutput string, attempt int) {
	d.persistBeadCount(ctx, assignmentID, beadID, "attempt_count", attempt)
	rec := qgExhaustionRecord(workerID, beadID, assignmentID, qgOutput, attempt)
	cls := d.classifyQGFailure(ctx, rec, QGFailureHistory{RetryExhausted: true})
	if cls.Decision == QGFailureDecisionReopenOriginal {
		d.handleClassifiedQGExhaustion(ctx, workerID, beadID, assignmentID, rec, cls)
		return
	}
	if cls.Decision == QGFailureDecisionCreateOrReuseInfra {
		d.handleSystemicQGExhaustion(ctx, workerID, beadID, assignmentID, rec, cls)
		return
	}
	d.handleTriageQGExhaustion(ctx, workerID, beadID, assignmentID, rec, cls)
}

func qgExhaustionRecord(workerID, beadID string, assignmentID int64, qgOutput string, attempt int) QGFailureRecord {
	qgFingerprint, qgSummary := FingerprintQGFailure(qgOutput, QGFingerprintOptions{})
	return QGFailureRecord{
		ID:           fmt.Sprintf("%s:%s:%d:%d", beadID, workerID, assignmentID, attempt),
		BeadID:       beadID,
		WorkerID:     workerID,
		AssignmentID: assignmentID,
		Component:    "worker",
		Fingerprint:  qgFingerprint,
		Summary:      qgSummary,
		Output:       qgOutput,
	}
}

func (d *Dispatcher) handleSystemicQGExhaustion(ctx context.Context, workerID, beadID string, assignmentID int64, rec QGFailureRecord, cls QGFailureClassification) {
	incident, err := d.createOrReuseQGInfraIncident(ctx, rec, cls)
	if err != nil {
		_ = d.logEvent(ctx, "qg_failure_record_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q}`, err.Error(), rec.Fingerprint))
	}
	_ = d.completeAssignment(ctx, assignmentID, beadID)
	d.releaseWorkerAfterQGExhaustion(workerID, beadID)
	if d.shouldReopenQGOriginal(ctx, beadID) {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "qg_original_reopen_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
	}
	_ = d.logEvent(ctx, "qg_infra_incident_reused", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"incident_id":%d,"class":%q,"fingerprint":%q}`, incident.ID, cls.Class, rec.Fingerprint))
}

func (d *Dispatcher) handleClassifiedQGExhaustion(ctx context.Context, workerID, beadID string, assignmentID int64, rec QGFailureRecord, cls QGFailureClassification) {
	rec = normalizeQGFailureRecord(rec)
	incident, err := RecordQGFailureOccurrence(ctx, d.db, rec, cls)
	if err != nil {
		_ = d.logEvent(ctx, "qg_failure_record_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q}`, err.Error(), rec.Fingerprint))
	} else if err := d.linkQGFailureToBeads(ctx, incident, rec, cls); err != nil {
		_ = d.logEvent(ctx, "qg_failure_link_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q,"incident_id":%d}`, err.Error(), rec.Fingerprint, incident.ID))
	}

	_ = d.completeAssignment(ctx, assignmentID, beadID)
	d.releaseWorkerAfterQGExhaustion(workerID, beadID)
	if d.shouldReopenQGOriginal(ctx, beadID) {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "qg_original_reopen_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		} else {
			d.deferReopenedQGOriginal(ctx, beadID, workerID, rec.Fingerprint)
		}
	}
	_ = d.logEvent(ctx, "qg_original_reopened", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"class":%q,"decision":%q,"fingerprint":%q}`, cls.Class, cls.Decision, rec.Fingerprint))
}

func (d *Dispatcher) handleTriageQGExhaustion(ctx context.Context, workerID, beadID string, assignmentID int64, rec QGFailureRecord, cls QGFailureClassification) {
	rec = normalizeQGFailureRecord(rec)
	incident, err := RecordQGFailureOccurrence(ctx, d.db, rec, cls)
	if err != nil {
		_ = d.logEvent(ctx, "qg_failure_record_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q}`, err.Error(), rec.Fingerprint))
	} else if err := d.linkQGFailureToBeads(ctx, incident, rec, cls); err != nil {
		_ = d.logEvent(ctx, "qg_failure_link_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q,"incident_id":%d}`, err.Error(), rec.Fingerprint, incident.ID))
	}

	_ = d.completeAssignment(ctx, assignmentID, beadID)
	d.releaseWorkerAfterQGExhaustion(workerID, beadID)
	if d.shouldReopenQGOriginal(ctx, beadID) {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "qg_original_reopen_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		} else {
			d.deferReopenedQGOriginal(ctx, beadID, workerID, rec.Fingerprint)
		}
	}
	_ = d.logEvent(ctx, "qg_failure_triage_required", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"class":%q,"decision":%q,"fingerprint":%q,"reason":%q}`,
			cls.Class, cls.Decision, rec.Fingerprint, cls.Reason))
}

func (d *Dispatcher) deferReopenedQGOriginal(ctx context.Context, beadID, workerID, fingerprint string) {
	until := d.nowFunc().UTC().Add(qgOriginalReopenDeferDuration).Format(time.RFC3339)
	if err := d.beads.Defer(ctx, beadID, until); err != nil {
		d.mu.Lock()
		d.exhaustedBeads[beadID] = true
		d.mu.Unlock()
		_ = d.logEvent(ctx, "qg_original_defer_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q}`, err.Error(), fingerprint))
		return
	}
	_ = d.logEvent(ctx, "qg_original_deferred", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"until":%q,"fingerprint":%q}`, until, fingerprint))
}

func (d *Dispatcher) releaseWorkerAfterQGExhaustion(workerID, beadID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if w, ok := d.workers[workerID]; ok {
		w.state = protocol.WorkerIdle
		w.assignmentID = 0
		w.beadID = ""
		w.epicID = ""
		w.isEpicDecomp = false
	}
	delete(d.attemptCounts, beadID)
	delete(d.transientCounts, beadID)
	delete(d.handoffCounts, beadID)
	delete(d.rejectionCounts, beadID)
	delete(d.reviewBlockedCounts, beadID)
	delete(d.pendingHandoffs, beadID)
	delete(d.qgStuckTracker, beadID)
	delete(d.escalatedBeads, beadID)
	delete(d.worktreeFailures, beadID)
	delete(d.assigningBeads, beadID)
	delete(d.exhaustedBeads, beadID)
}

func (d *Dispatcher) shouldReopenQGOriginal(ctx context.Context, beadID string) bool {
	return d.shouldReopenBead(ctx, beadID)
}

func (d *Dispatcher) shouldReopenBead(ctx context.Context, beadID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil || detail == nil {
		return true
	}
	return detail.Status != "closed"
}
