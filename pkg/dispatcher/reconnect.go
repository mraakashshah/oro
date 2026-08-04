package dispatcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"oro/pkg/evidencefs"
	"oro/pkg/protocol"
)

var errLegacyReconnectSuperseded = errors.New("legacy reconnect assignment is not canonical")

func (d *Dispatcher) validateReconnectBead(ctx context.Context, beadID, workerID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil || detail == nil {
		_ = d.logEvent(ctx, "reconnect_closed_bead_rejected", workerID, beadID, workerID,
			"rejecting reconnect: bead lookup failed (not found or error)")
		return false
	}
	if detail.Status == "closed" {
		_ = d.logEvent(ctx, "reconnect_closed_bead_rejected", workerID, beadID, workerID,
			"rejecting reconnect: bead is closed")
		return false
	}
	return true
}

// processReconnectUnderLock handles reconnection logic while holding d.mu.
// Caller must hold d.mu.
// oro-ovpc: Prevents bead stealing by checking for existing assignments.
func (d *Dispatcher) processReconnectUnderLock(ctx context.Context, w *trackedWorker, workerID, beadID, state string) {
	if w.state == protocol.WorkerReserved {
		w.lastSeen = d.nowFunc()
		for _, pending := range w.pendingMsgs {
			_ = d.sendToWorker(w, pending)
		}
		w.pendingMsgs = nil
		return
	}

	// Check if another worker is already assigned to this bead.
	var beadStolenFrom string
	for otherID, other := range d.workers {
		if otherID != workerID && other.beadID == beadID && other.state == protocol.WorkerBusy {
			beadStolenFrom = otherID
			break
		}
	}

	if beadStolenFrom != "" {
		_ = d.logEvent(ctx, "reconnect_bead_conflict", workerID, beadID, beadStolenFrom,
			fmt.Sprintf("worker %s already assigned to %s", beadStolenFrom, beadID))
	} else {
		w.beadID = beadID
	}

	w.lastSeen = d.nowFunc()
	if state == "running" && w.beadID == beadID {
		w.state = protocol.WorkerBusy
		w.lastProgress = d.nowFunc()
	} else {
		w.state = protocol.WorkerIdle
	}

	// Replay pending messages
	for _, pending := range w.pendingMsgs {
		_ = d.sendToWorker(w, pending)
	}
	w.pendingMsgs = nil
}

func (d *Dispatcher) reactivateRequeuedAssignment(ctx context.Context, beadID, workerID string) int64 {
	if d.db == nil || beadID == "" {
		return 0
	}
	admission, err := d.beginAssignmentAdmission(ctx, "reactivate requeued")
	if err != nil {
		_ = d.logEvent(ctx, "assignment_reactivate_failed", "dispatcher", beadID, workerID, err.Error())
		return 0
	}
	defer admission.close()
	var assignmentID int64
	if err := admission.conn.QueryRowContext(ctx,
		`SELECT id FROM assignments WHERE bead_id=? AND status='requeued' ORDER BY id DESC LIMIT 1`,
		beadID,
	).Scan(&assignmentID); err != nil {
		_ = admission.conn.QueryRowContext(ctx,
			`SELECT id FROM assignments WHERE bead_id=? AND status='active' ORDER BY id DESC LIMIT 1`,
			beadID).Scan(&assignmentID)
		return assignmentID
	}
	if _, err := admission.conn.ExecContext(ctx,
		`UPDATE assignments SET status='active', completed_at=NULL, worker_id=? WHERE id=?`,
		workerID, assignmentID,
	); err != nil {
		admission.close()
		_ = d.logEvent(ctx, "assignment_reactivate_failed", "dispatcher", beadID, workerID, err.Error())
		return 0
	}
	if err := admission.commit(ctx, "reactivate requeued"); err != nil {
		admission.close()
		_ = d.logEvent(ctx, "assignment_reactivate_failed", "dispatcher", beadID, workerID, err.Error())
		return 0
	}
	admission.close()
	_ = d.logEvent(ctx, "assignment_reactivated", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"assignment_id":%d}`, assignmentID))
	return assignmentID
}

func (d *Dispatcher) handleReconnect(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Reconnect == nil {
		return
	}

	// Validate the reconnect payload to prevent unbounded buffered events
	if err := msg.Reconnect.Validate(); err != nil {
		_ = d.logEvent(ctx, "reconnect_rejected", workerID, msg.Reconnect.BeadID, workerID, err.Error())
		return
	}

	_ = d.logEvent(ctx, "reconnect", workerID, msg.Reconnect.BeadID, workerID, msg.Reconnect.State)

	beadID := msg.Reconnect.BeadID
	if d.shutdownReconnectIfSpawnForStopping(workerID) {
		return
	}

	// oro-sydf: If BeadID is empty, the worker was idle before the network glitch.
	// Skip bead validation entirely — there is no bead to look up — and
	// transition the worker directly to idle so tryAssign can pick it up.
	if beadID == "" {
		d.markReconnectWorkerIdle(workerID)
		return
	}

	// oro-3xdf: Check if the bead is valid (open, not closed/missing).
	// Do this outside the lock to avoid I/O while holding mutex.
	if !d.validateReconnectBead(ctx, beadID, workerID) {
		// oro-xj37: Transition worker to Idle so tryAssign can pick it up.
		// Without this, the worker stays in its previous state permanently.
		d.markReconnectWorkerIdle(workerID)
		return
	}

	if d.handleAwaitingReviewReconnect(ctx, workerID, msg.Reconnect) {
		return
	}
	if d.handleLegacyIdleReconnect(ctx, workerID, msg.Reconnect) {
		return
	}

	assignmentID := d.reactivateRequeuedAssignment(ctx, beadID, workerID)
	alreadyReviewing, restored := d.restoreReconnectWorker(
		ctx, workerID, beadID, msg.Reconnect.State, assignmentID, durableReadyIdentity{}, false,
	)
	if !restored {
		return
	}

	d.replayReconnectEvents(ctx, workerID, msg.Reconnect.BufferedEvents,
		false, alreadyReviewing, protocol.ReadyForReviewPayload{})
}

//nolint:funlen,gocognit,gocyclo // explicit lock, transaction, and compensation branches preserve fail-closed ordering
func (d *Dispatcher) handleLegacyIdleReconnect(
	ctx context.Context,
	workerID string,
	reconnect *protocol.ReconnectPayload,
) bool {
	if reconnect.State != "idle" || !d.workerDrainingAfterAssignment(workerID) {
		return false
	}
	if d.testLegacyReconnectAdmissionHook != nil {
		d.testLegacyReconnectAdmissionHook()
	}

	d.mu.Lock()
	w := d.workers[workerID]
	if w == nil || !w.drainAfterAssignment {
		d.mu.Unlock()
		return false
	}
	admission, err := d.beginAssignmentAdmission(ctx, "legacy reconnect")
	if err != nil {
		d.preserveLegacyReconnectClaimLocked(workerID, reconnect.BeadID)
		d.mu.Unlock()
		return true
	}
	identity, status, err := d.claimCanonicalLegacyReconnectAssignment(ctx, admission.conn, workerID, reconnect.BeadID)
	if err != nil {
		admission.close()
		if errors.Is(err, errLegacyReconnectSuperseded) {
			d.holdSupersededLegacyReconnectLocked(workerID)
			d.mu.Unlock()
			_ = d.logEvent(ctx, "legacy_reconnect_superseded", "dispatcher", reconnect.BeadID, workerID, err.Error())
			return true
		}
		d.preserveLegacyReconnectClaimLocked(workerID, reconnect.BeadID)
		d.mu.Unlock()
		return true
	}
	if d.testLegacyReconnectClaimedHook != nil {
		d.testLegacyReconnectClaimedHook()
	}
	if err := d.verifyCanonicalLegacyReconnectAssignment(ctx, admission.conn, identity); err != nil {
		admission.close()
		d.holdSupersededLegacyReconnectLocked(workerID)
		d.mu.Unlock()
		_ = d.logEvent(ctx, "legacy_reconnect_superseded", "dispatcher", reconnect.BeadID, workerID, err.Error())
		return true
	}
	if d.testLegacyReconnectVerifiedHook != nil {
		d.testLegacyReconnectVerifiedHook()
	}
	d.restoreLegacyReconnectOwnershipLocked(workerID, identity)

	if bufferedLegacyReady(reconnect.BufferedEvents, workerID, reconnect.BeadID) {
		if err := admission.commit(ctx, "legacy reconnect"); err != nil {
			admission.close()
			d.holdSupersededLegacyReconnectLocked(workerID)
			d.mu.Unlock()
			_ = d.logEvent(ctx, "legacy_reconnect_commit_failed", "dispatcher", reconnect.BeadID, workerID, err.Error())
			return true
		}
		admission.close()
		d.mu.Unlock()
		d.replayReconnectEvents(ctx, workerID, reconnect.BufferedEvents,
			false, false, protocol.ReadyForReviewPayload{})
		return true
	}
	beadTransitionTransactional, releaseErr := d.releaseLegacyIdleOwnership(ctx, admission.conn, identity, status)
	commitErr := admission.commit(ctx, "legacy idle release")
	admission.close()
	if commitErr != nil {
		d.holdSupersededLegacyReconnectLocked(workerID)
		d.mu.Unlock()
		if releaseErr == nil && !beadTransitionTransactional {
			d.compensateLegacyBeadReopen(ctx, identity.beadID)
		}
		_ = d.logEvent(ctx, "legacy_idle_release_failed", "dispatcher", identity.beadID, workerID, commitErr.Error())
		return true
	}
	if releaseErr != nil {
		d.mu.Unlock()
		_ = d.logEvent(ctx, "legacy_idle_release_failed", "dispatcher", identity.beadID, workerID, releaseErr.Error())
		return true
	}
	if w := d.workers[workerID]; w != nil && w.assignmentID == identity.assignmentID {
		w.state = protocol.WorkerIdle
		w.lastSeen = d.nowFunc()
	}
	d.mu.Unlock()
	return true
}

func (d *Dispatcher) holdSupersededLegacyReconnectLocked(workerID string) {
	if w := d.workers[workerID]; w != nil {
		w.state = protocol.WorkerReserved
		w.assignmentID = 0
		w.beadID = ""
		w.worktree = ""
		w.qgEvidenceDir = ""
		w.qgEvidencePath = ""
		w.targetSHA = ""
		w.baseBranch = ""
		w.targetBranch = ""
		w.lastSeen = d.nowFunc()
	}
}

func (d *Dispatcher) releaseLegacyIdleOwnership(
	ctx context.Context,
	conn *sql.Conn,
	identity durableReadyIdentity,
	originalStatus string,
) (bool, error) {
	if originalStatus == "active" {
		if err := transitionLegacyAssignmentStatus(ctx, conn, identity, "active", "requeued"); err != nil {
			return false, err
		}
		if d.testLegacyReconnectRequeuedHook != nil {
			d.testLegacyReconnectRequeuedHook()
		}
	}
	opened, transactional, err := d.updateLegacyBeadStatus(ctx, conn, identity.beadID, "in_progress", "open")
	if err == nil && !opened {
		var detail *protocol.BeadDetail
		detail, err = d.beads.Show(ctx, identity.beadID)
		opened = err == nil && detail != nil && detail.Status == "open"
		if !opened && err == nil {
			err = errors.New("authoritative bead was not in_progress or open")
		}
	}
	if err == nil && opened {
		return transactional, nil
	}
	if originalStatus == "active" {
		if restoreErr := transitionLegacyAssignmentStatus(ctx, conn, identity, "requeued", "active"); restoreErr != nil {
			return transactional, fmt.Errorf("reopen authoritative bead: %w; restore assignment ownership: %w", err, restoreErr)
		}
	}
	return transactional, fmt.Errorf("reopen authoritative bead: %w", err)
}

func (d *Dispatcher) updateLegacyBeadStatus(
	ctx context.Context,
	conn *sql.Conn,
	beadID, expected, next string,
) (updated, transactional bool, err error) {
	if store, ok := d.beads.(interface {
		UpdateStatusIfConn(context.Context, *sql.Conn, string, string, string) (bool, error)
	}); ok {
		updated, err = store.UpdateStatusIfConn(ctx, conn, beadID, expected, next)
		if err != nil {
			return false, true, fmt.Errorf("update legacy bead status on admission connection: %w", err)
		}
		return updated, true, nil
	}
	updated, err = d.beads.UpdateStatusIf(ctx, beadID, expected, next)
	if err != nil {
		return false, false, fmt.Errorf("update legacy bead status: %w", err)
	}
	return updated, false, nil
}

func (d *Dispatcher) compensateLegacyBeadReopen(ctx context.Context, beadID string) {
	restored, err := d.beads.UpdateStatusIf(ctx, beadID, "open", "in_progress")
	if err != nil || !restored {
		payload := fmt.Sprintf(`{"restored":%t}`, restored)
		if err != nil {
			payload = err.Error()
		}
		_ = d.logEvent(ctx, "legacy_bead_reopen_compensation_failed", "dispatcher", beadID, "", payload)
	}
}

func transitionLegacyAssignmentStatus(
	ctx context.Context,
	ex execer,
	identity durableReadyIdentity,
	from, to string,
) error {
	var (
		result sql.Result
		err    error
	)
	if from == "active" && to == "requeued" {
		result, err = ex.ExecContext(ctx, `
UPDATE assignments SET status='requeued', completed_at=datetime('now')
WHERE id=? AND bead_id=? AND worker_id=? AND status='active'
  AND id=(SELECT MAX(id) FROM assignments WHERE bead_id=? AND status IN ('active','requeued'))`,
			identity.assignmentID, identity.beadID, identity.workerID, identity.beadID)
	} else {
		result, err = ex.ExecContext(ctx, `
UPDATE assignments SET status='active', completed_at=NULL
WHERE id=? AND bead_id=? AND worker_id=? AND status='requeued'`,
			identity.assignmentID, identity.beadID, identity.workerID)
	}
	if err != nil {
		return fmt.Errorf("transition legacy assignment %d from %s to %s: %w", identity.assignmentID, from, to, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count legacy assignment %d transition: %w", identity.assignmentID, err)
	}
	if rows != 1 {
		return fmt.Errorf("transition legacy assignment %d from %s to %s affected %d rows", identity.assignmentID, from, to, rows)
	}
	return nil
}

func (d *Dispatcher) preserveLegacyReconnectClaimLocked(workerID, beadID string) {
	if w := d.workers[workerID]; w != nil {
		w.state = protocol.WorkerBusy
		w.beadID = beadID
		w.lastSeen = d.nowFunc()
	}
}

func (d *Dispatcher) workerDrainingAfterAssignment(workerID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workers[workerID]
	return w != nil && w.drainAfterAssignment
}

func (d *Dispatcher) claimCanonicalLegacyReconnectAssignment(
	ctx context.Context,
	ex execer,
	workerID, beadID string,
) (durableReadyIdentity, string, error) {
	identity := durableReadyIdentity{beadID: beadID}
	var status string
	err := ex.QueryRowContext(ctx, `
SELECT id, worker_id, worktree, qg_evidence_dir, target_sha, target_branch, status
FROM assignments
WHERE id=(SELECT MAX(id) FROM assignments WHERE bead_id=? AND status IN ('active','requeued'))
  AND bead_id=? AND status IN ('active','requeued')`, beadID, beadID).Scan(
		&identity.assignmentID, &identity.workerID, &identity.worktree, &identity.evidenceRoot,
		&identity.targetSHA, &identity.targetBranch, &status,
	)
	if err != nil {
		return durableReadyIdentity{}, "", fmt.Errorf("load canonical legacy reconnect assignment: %w", err)
	}
	if identity.workerID != workerID {
		return durableReadyIdentity{}, "", fmt.Errorf("%w: assignment %d belongs to %s", errLegacyReconnectSuperseded, identity.assignmentID, identity.workerID)
	}
	result, err := ex.ExecContext(ctx, `
UPDATE assignments
SET status='active', completed_at=CASE WHEN status='requeued' THEN NULL ELSE completed_at END
WHERE id=? AND bead_id=? AND worker_id=? AND status=?
  AND id=(SELECT MAX(id) FROM assignments WHERE bead_id=? AND status IN ('active','requeued'))`,
		identity.assignmentID, beadID, workerID, status, beadID)
	if err != nil {
		return durableReadyIdentity{}, "", fmt.Errorf("claim canonical legacy reconnect assignment: %w", err)
	}
	if rowsAffected(result) != 1 {
		return durableReadyIdentity{}, "", fmt.Errorf("%w: assignment %d changed during claim", errLegacyReconnectSuperseded, identity.assignmentID)
	}
	if identity.targetBranch == "" {
		identity.targetBranch = d.cfg.DefaultBranch
	}
	return identity, "active", nil
}

func (d *Dispatcher) verifyCanonicalLegacyReconnectAssignment(
	ctx context.Context,
	ex execer,
	identity durableReadyIdentity,
) error {
	var canonical bool
	err := ex.QueryRowContext(ctx, `
SELECT EXISTS(
	SELECT 1
	FROM assignments
	WHERE id=? AND bead_id=? AND worker_id=? AND status='active'
	  AND id=(SELECT MAX(id) FROM assignments WHERE bead_id=? AND status IN ('active','requeued'))
)`, identity.assignmentID, identity.beadID, identity.workerID, identity.beadID).Scan(&canonical)
	if err != nil {
		return fmt.Errorf("verify canonical legacy reconnect assignment %d: %w", identity.assignmentID, err)
	}
	if !canonical {
		return fmt.Errorf("%w: assignment %d changed before ownership restore", errLegacyReconnectSuperseded, identity.assignmentID)
	}
	return nil
}

func (d *Dispatcher) restoreLegacyReconnectOwnershipLocked(workerID string, identity durableReadyIdentity) {
	if w := d.workers[workerID]; w != nil {
		w.state = protocol.WorkerBusy
		w.assignmentID = identity.assignmentID
		w.beadID = identity.beadID
		w.worktree = identity.worktree
		w.qgEvidenceDir = identity.evidenceRoot
		w.targetSHA = identity.targetSHA
		w.baseBranch = identity.targetBranch
		w.targetBranch = identity.targetBranch
		w.lastSeen = d.nowFunc()
		w.lastProgress = d.nowFunc()
	}
}

func bufferedLegacyReady(events []protocol.Message, workerID, beadID string) bool {
	for _, event := range events {
		ready := event.ReadyForReview
		if event.Type == protocol.MsgReadyForReview && legacyReadyEvidenceIdentity(ready) &&
			ready.WorkerID == workerID && ready.BeadID == beadID {
			return true
		}
	}
	return false
}

func rowsAffected(result sql.Result) int64 {
	if result == nil {
		return 0
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0
	}
	return rows
}

func (d *Dispatcher) markReconnectWorkerIdle(workerID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if w := d.workers[workerID]; w != nil {
		w.state = protocol.WorkerIdle
		w.beadID = ""
		w.lastSeen = d.nowFunc()
	}
}

func (d *Dispatcher) handleAwaitingReviewReconnect(
	ctx context.Context,
	workerID string,
	reconnect *protocol.ReconnectPayload,
) bool {
	if reconnect.State != "awaiting_review" {
		return false
	}
	_, replayReady, alreadyReviewing, restored := d.restoreCanonicalReadyReconnect(ctx, workerID, reconnect)
	if restored {
		d.replayReconnectEvents(ctx, workerID, reconnect.BufferedEvents, true, alreadyReviewing, replayReady)
		return true
	}
	_, restored = d.restoreReconnectWorker(ctx, workerID, reconnect.BeadID,
		reconnect.State, 0, durableReadyIdentity{}, false)
	if restored {
		d.replayReconnectEvents(ctx, workerID, reconnect.BufferedEvents,
			false, false, protocol.ReadyForReviewPayload{})
	}
	return true
}

func (d *Dispatcher) replayReconnectEvents(
	ctx context.Context,
	workerID string,
	events []protocol.Message,
	canonicalReconnect, alreadyReviewing bool,
	replayReady protocol.ReadyForReviewPayload,
) {
	for _, buffered := range events {
		d.handleMessage(ctx, workerID, buffered)
	}
	if canonicalReconnect && !alreadyReviewing && !bufferedCanonicalReady(events, replayReady) {
		d.handleReadyForReview(ctx, workerID, protocol.Message{
			Type: protocol.MsgReadyForReview, ReadyForReview: &replayReady,
		})
	}
}

func (d *Dispatcher) restoreReconnectWorker(
	ctx context.Context,
	workerID, beadID, reconnectState string,
	assignmentID int64,
	readyIdentity durableReadyIdentity,
	canonicalReconnect bool,
) (alreadyReviewing, restored bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if !ok {
		return false, false
	}

	alreadyReviewing = canonicalReconnect && w.state == protocol.WorkerReviewing &&
		w.assignmentID == readyIdentity.assignmentID && w.beadID == readyIdentity.beadID
	if canonicalReconnect {
		return alreadyReviewing, d.restoreCanonicalReadyWorkerLocked(w, readyIdentity, alreadyReviewing)
	}
	d.processReconnectUnderLock(ctx, w, workerID, beadID, reconnectState)
	if assignmentID > 0 && w.beadID == beadID {
		w.assignmentID = assignmentID
	}
	return false, true
}

//nolint:funlen // lock and admission cleanup stays explicit on every fail-closed return
func (d *Dispatcher) restoreCanonicalReadyReconnect(
	ctx context.Context,
	workerID string,
	reconnect *protocol.ReconnectPayload,
) (durableReadyIdentity, protocol.ReadyForReviewPayload, bool, bool) {
	if !canonicalReconnectRequestValid(reconnect, d.db) {
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}

	d.mu.Lock()
	admission, err := d.beginAssignmentAdmission(ctx, "canonical ready reconnect")
	if err != nil {
		d.mu.Unlock()
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}
	if d.testCanonicalReconnectAdmissionHook != nil {
		d.testCanonicalReconnectAdmissionHook()
	}
	w := d.workers[workerID]
	if w == nil {
		admission.close()
		d.mu.Unlock()
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}

	prepared, err := d.prepareCanonicalReadyReconnect(
		ctx, admission.conn, w, workerID, reconnect.BeadID,
	)
	if err != nil {
		admission.close()
		d.mu.Unlock()
		if !errors.Is(err, sql.ErrNoRows) {
			_ = d.logEvent(ctx, "reconnect_ready_restore_failed", "dispatcher", reconnect.BeadID, workerID, err.Error())
		}
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}
	if !prepared.valid {
		admission.close()
		d.mu.Unlock()
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}

	if claimCanonicalReconnectAssignment(ctx, admission.conn, prepared.identity, prepared.status) != nil {
		admission.close()
		d.mu.Unlock()
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}
	priorWorker := *w
	if !d.restoreCanonicalReadyWorkerLocked(w, prepared.identity, prepared.alreadyReviewing) {
		admission.close()
		d.mu.Unlock()
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}
	if err := admission.commit(ctx, "canonical ready reconnect"); err != nil {
		*w = priorWorker
		admission.close()
		d.mu.Unlock()
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}
	admission.close()
	d.mu.Unlock()
	return prepared.identity, prepared.ready, prepared.alreadyReviewing, true
}

type canonicalReadyReconnectPreparation struct {
	identity         durableReadyIdentity
	ready            protocol.ReadyForReviewPayload
	status           string
	alreadyReviewing bool
	valid            bool
}

func (d *Dispatcher) prepareCanonicalReadyReconnect(
	ctx context.Context,
	ex execer,
	w *trackedWorker,
	workerID, beadID string,
) (canonicalReadyReconnectPreparation, error) {
	identity, status, err := loadCanonicalReconnectCandidate(ctx, ex, workerID, beadID)
	if err != nil {
		return canonicalReadyReconnectPreparation{}, err
	}
	if identity.targetBranch == "" {
		identity.targetBranch = d.cfg.DefaultBranch
	}
	ready, valid := d.canonicalReconnectReady(identity)
	if !valid || !d.canonicalReadyWorkerRestorableLocked(w, identity) {
		return canonicalReadyReconnectPreparation{}, nil
	}
	alreadyReviewing := w.state == protocol.WorkerReviewing &&
		w.assignmentID == identity.assignmentID && w.beadID == identity.beadID
	return canonicalReadyReconnectPreparation{
		identity: identity, ready: ready, status: status,
		alreadyReviewing: alreadyReviewing, valid: true,
	}, nil
}

func canonicalReconnectRequestValid(reconnect *protocol.ReconnectPayload, db *sql.DB) bool {
	return reconnect != nil && reconnect.State == "awaiting_review" && reconnect.BeadID != "" && db != nil
}

func loadCanonicalReconnectCandidate(
	ctx context.Context,
	ex execer,
	workerID, beadID string,
) (identity durableReadyIdentity, status string, err error) {
	err = ex.QueryRowContext(ctx, `
SELECT id, bead_id, worker_id, worktree, qg_evidence_dir, target_sha, target_branch, status
FROM assignments
WHERE id = (SELECT MAX(id) FROM assignments WHERE bead_id = ?)
  AND bead_id = ? AND worker_id = ? AND status IN ('active', 'requeued')`,
		beadID, beadID, workerID).Scan(
		&identity.assignmentID, &identity.beadID, &identity.workerID, &identity.worktree,
		&identity.evidenceRoot, &identity.targetSHA, &identity.targetBranch, &status,
	)
	if err != nil {
		return durableReadyIdentity{}, "", fmt.Errorf("load canonical reconnect assignment: %w", err)
	}
	return identity, status, nil
}

func (d *Dispatcher) canonicalReconnectReady(identity durableReadyIdentity) (protocol.ReadyForReviewPayload, bool) {
	path, err := canonicalReadyEvidencePath(identity.evidenceRoot, identity.beadID, identity.assignmentID)
	if err != nil || filepath.Clean(identity.evidenceRoot) != filepath.Clean(d.cfg.ReviewEvidenceDir) {
		return protocol.ReadyForReviewPayload{}, false
	}
	ready := protocol.ReadyForReviewPayload{
		BeadID: identity.beadID, WorkerID: identity.workerID, AssignmentID: identity.assignmentID,
		Worktree: identity.worktree, QGEvidencePath: path, TargetSHA: identity.targetSHA,
	}
	return ready, ready.Validate() == nil && canonicalReconnectEvidenceMatches(identity, ready)
}

func claimCanonicalReconnectAssignment(
	ctx context.Context,
	ex execer,
	identity durableReadyIdentity,
	status string,
) error {
	result, err := ex.ExecContext(ctx, `
UPDATE assignments
SET status = 'active',
    completed_at = CASE WHEN status = 'requeued' THEN NULL ELSE completed_at END
WHERE id = ? AND worker_id = ? AND status = ?`, identity.assignmentID, identity.workerID, status)
	if err != nil {
		return fmt.Errorf("claim canonical reconnect assignment: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count canonical reconnect claim: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("canonical reconnect claim changed %d rows", rows)
	}
	return nil
}

func canonicalReconnectEvidenceMatches(identity durableReadyIdentity, ready protocol.ReadyForReviewPayload) bool {
	data, err := evidencefs.ReadFile(identity.evidenceRoot,
		[]string{identity.beadID, strconv.FormatInt(identity.assignmentID, 10)},
		readyEvidenceAttempt, protocol.MaxMessageSize)
	if err != nil {
		return false
	}
	var evidence protocol.ReadyForReviewPayload
	return json.Unmarshal(data, &evidence) == nil && evidence.Validate() == nil && evidence == ready
}

func (d *Dispatcher) canonicalReadyWorkerRestorableLocked(w *trackedWorker, identity durableReadyIdentity) bool {
	for otherID, other := range d.workers {
		if otherID != w.id && other.beadID == identity.beadID &&
			other.state != protocol.WorkerIdle && other.state != protocol.WorkerShuttingDown {
			return false
		}
	}
	return w.assignmentID == 0 || w.assignmentID == identity.assignmentID
}

func (d *Dispatcher) restoreCanonicalReadyWorkerLocked(w *trackedWorker, identity durableReadyIdentity, alreadyReviewing bool) bool {
	if !d.canonicalReadyWorkerRestorableLocked(w, identity) {
		return false
	}
	w.assignmentID = identity.assignmentID
	w.beadID = identity.beadID
	w.worktree = identity.worktree
	w.qgEvidenceDir = identity.evidenceRoot
	w.qgEvidencePath = filepath.Join(identity.evidenceRoot, identity.beadID,
		strconv.FormatInt(identity.assignmentID, 10), readyEvidenceAttempt)
	w.targetSHA = identity.targetSHA
	w.baseBranch = identity.targetBranch
	w.targetBranch = identity.targetBranch
	w.lastSeen = d.nowFunc()
	if !alreadyReviewing {
		w.state = protocol.WorkerBusy
	}
	for _, pending := range w.pendingMsgs {
		_ = d.sendToWorker(w, pending)
	}
	w.pendingMsgs = nil
	return true
}

func bufferedCanonicalReady(events []protocol.Message, want protocol.ReadyForReviewPayload) bool {
	for _, event := range events {
		if event.Type == protocol.MsgReadyForReview && event.ReadyForReview != nil && *event.ReadyForReview == want {
			return true
		}
	}
	return false
}

func (d *Dispatcher) shutdownReconnectIfSpawnForStopping(workerID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if !ok || !w.spawnFor || w.state != protocol.WorkerShuttingDown {
		return false
	}
	w.markShuttingDownWithoutAssignment()
	sendShutdownWithoutBuffering(w)
	w.lastSeen = d.nowFunc()
	return true
}
