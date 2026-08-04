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
	var assignmentID int64
	if err := d.db.QueryRowContext(ctx,
		`SELECT id FROM assignments WHERE bead_id=? AND status='requeued' ORDER BY id DESC LIMIT 1`,
		beadID,
	).Scan(&assignmentID); err != nil {
		return d.activeAssignmentIDForBead(ctx, beadID)
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE assignments SET status='active', completed_at=NULL, worker_id=? WHERE id=?`,
		workerID, assignmentID,
	); err != nil {
		_ = d.logEvent(ctx, "assignment_reactivate_failed", "dispatcher", beadID, workerID, err.Error())
		return 0
	}
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

func (d *Dispatcher) handleLegacyIdleReconnect(
	ctx context.Context,
	workerID string,
	reconnect *protocol.ReconnectPayload,
) bool {
	if reconnect.State != "idle" || !d.workerDrainingAfterAssignment(workerID) {
		return false
	}

	identity, status, err := d.claimCanonicalLegacyReconnectAssignment(ctx, workerID, reconnect.BeadID)
	if err != nil {
		if errors.Is(err, errLegacyReconnectSuperseded) {
			d.holdSupersededLegacyReconnect(workerID)
			_ = d.logEvent(ctx, "legacy_reconnect_superseded", "dispatcher", reconnect.BeadID, workerID, err.Error())
			return true
		}
		d.preserveLegacyReconnectClaim(workerID, reconnect.BeadID)
		return true
	}
	if d.testLegacyReconnectClaimedHook != nil {
		d.testLegacyReconnectClaimedHook()
	}
	if err := d.verifyCanonicalLegacyReconnectAssignment(ctx, identity); err != nil {
		d.holdSupersededLegacyReconnect(workerID)
		_ = d.logEvent(ctx, "legacy_reconnect_superseded", "dispatcher", reconnect.BeadID, workerID, err.Error())
		return true
	}
	d.restoreLegacyReconnectOwnership(workerID, identity)

	if bufferedLegacyReady(reconnect.BufferedEvents, workerID, reconnect.BeadID) {
		d.replayReconnectEvents(ctx, workerID, reconnect.BufferedEvents,
			false, false, protocol.ReadyForReviewPayload{})
		return true
	}

	if err := d.releaseLegacyIdleOwnership(ctx, identity, status); err != nil {
		_ = d.logEvent(ctx, "legacy_idle_release_failed", "dispatcher", identity.beadID, workerID, err.Error())
		return true
	}
	d.mu.Lock()
	if w := d.workers[workerID]; w != nil && w.assignmentID == identity.assignmentID {
		w.state = protocol.WorkerIdle
		w.lastSeen = d.nowFunc()
	}
	d.mu.Unlock()
	return true
}

func (d *Dispatcher) holdSupersededLegacyReconnect(workerID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
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
	identity durableReadyIdentity,
	originalStatus string,
) error {
	if originalStatus == "active" {
		if err := d.transitionLegacyAssignmentStatus(ctx, identity, "active", "requeued"); err != nil {
			return err
		}
	}
	opened, err := d.beads.UpdateStatusIf(ctx, identity.beadID, "in_progress", "open")
	if err == nil && !opened {
		var detail *protocol.BeadDetail
		detail, err = d.beads.Show(ctx, identity.beadID)
		opened = err == nil && detail != nil && detail.Status == "open"
		if !opened && err == nil {
			err = errors.New("authoritative bead was not in_progress or open")
		}
	}
	if err == nil && opened {
		return nil
	}
	if originalStatus == "active" {
		if restoreErr := d.transitionLegacyAssignmentStatus(ctx, identity, "requeued", "active"); restoreErr != nil {
			return fmt.Errorf("reopen authoritative bead: %w; restore assignment ownership: %w", err, restoreErr)
		}
	}
	return fmt.Errorf("reopen authoritative bead: %w", err)
}

func (d *Dispatcher) transitionLegacyAssignmentStatus(
	ctx context.Context,
	identity durableReadyIdentity,
	from, to string,
) error {
	var (
		result sql.Result
		err    error
	)
	if from == "active" && to == "requeued" {
		result, err = d.db.ExecContext(ctx, `
UPDATE assignments SET status='requeued', completed_at=datetime('now')
WHERE id=? AND bead_id=? AND worker_id=? AND status='active'
  AND id=(SELECT MAX(id) FROM assignments WHERE bead_id=? AND status IN ('active','requeued'))`,
			identity.assignmentID, identity.beadID, identity.workerID, identity.beadID)
	} else {
		result, err = d.db.ExecContext(ctx, `
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

func (d *Dispatcher) preserveLegacyReconnectClaim(workerID, beadID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
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
	workerID, beadID string,
) (durableReadyIdentity, string, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return durableReadyIdentity{}, "", fmt.Errorf("begin legacy reconnect claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	identity := durableReadyIdentity{beadID: beadID}
	var status string
	err = tx.QueryRowContext(ctx, `
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
	result, err := tx.ExecContext(ctx, `
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
	if err := tx.Commit(); err != nil {
		return durableReadyIdentity{}, "", fmt.Errorf("commit canonical legacy reconnect assignment: %w", err)
	}
	if identity.targetBranch == "" {
		identity.targetBranch = d.cfg.DefaultBranch
	}
	return identity, "active", nil
}

func (d *Dispatcher) verifyCanonicalLegacyReconnectAssignment(
	ctx context.Context,
	identity durableReadyIdentity,
) error {
	var canonical bool
	err := d.db.QueryRowContext(ctx, `
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

func (d *Dispatcher) restoreLegacyReconnectOwnership(workerID string, identity durableReadyIdentity) {
	d.mu.Lock()
	defer d.mu.Unlock()
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

func (d *Dispatcher) restoreCanonicalReadyReconnect(
	ctx context.Context,
	workerID string,
	reconnect *protocol.ReconnectPayload,
) (durableReadyIdentity, protocol.ReadyForReviewPayload, bool, bool) {
	if !canonicalReconnectRequestValid(reconnect, d.db) {
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workers[workerID]
	if w == nil {
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}
	defer func() { _ = tx.Rollback() }()

	identity, status, err := loadCanonicalReconnectCandidate(ctx, tx, workerID, reconnect.BeadID)
	if err != nil {
		_ = tx.Rollback()
		if !errors.Is(err, sql.ErrNoRows) {
			_ = d.logEvent(ctx, "reconnect_ready_restore_failed", "dispatcher", reconnect.BeadID, workerID, err.Error())
		}
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}
	if identity.targetBranch == "" {
		identity.targetBranch = d.cfg.DefaultBranch
	}
	ready, valid := d.canonicalReconnectReady(identity)
	if !valid || !d.canonicalReadyWorkerRestorableLocked(w, identity) {
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}

	alreadyReviewing := w.state == protocol.WorkerReviewing &&
		w.assignmentID == identity.assignmentID && w.beadID == identity.beadID
	if claimCanonicalReconnectAssignment(ctx, tx, identity, status) != nil {
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}
	if err := tx.Commit(); err != nil {
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}
	if !d.restoreCanonicalReadyWorkerLocked(w, identity, alreadyReviewing) {
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, false, false
	}
	return identity, ready, alreadyReviewing, true
}

func canonicalReconnectRequestValid(reconnect *protocol.ReconnectPayload, db *sql.DB) bool {
	return reconnect != nil && reconnect.State == "awaiting_review" && reconnect.BeadID != "" && db != nil
}

func loadCanonicalReconnectCandidate(
	ctx context.Context,
	tx *sql.Tx,
	workerID, beadID string,
) (identity durableReadyIdentity, status string, err error) {
	err = tx.QueryRowContext(ctx, `
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
	tx *sql.Tx,
	identity durableReadyIdentity,
	status string,
) error {
	result, err := tx.ExecContext(ctx, `
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
