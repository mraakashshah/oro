package dispatcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"oro/pkg/factoryhealth"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func (d *Dispatcher) escalate(ctx context.Context, msg, beadID, workerID string) {
	d.escalateWithOneShot(ctx, msg, beadID, workerID, true)
}

// escalateWithoutOneShot records and delivers an escalation without starting a
// corrective ops process. It is used when a review timeout has just cancelled
// the bead's active review, so cleanup has a stable no-active-ops boundary.
func (d *Dispatcher) escalateWithoutOneShot(ctx context.Context, msg, beadID, workerID string) {
	d.escalateWithOneShot(ctx, msg, beadID, workerID, false)
}

func (d *Dispatcher) escalateWithOneShot(ctx context.Context, msg, beadID, workerID string, allowOneShot bool) {
	// Extract escalation type for database storage (separate from one-shot determination).
	dbEscType := extractEscalationType(msg)

	oneShot := ""
	if allowOneShot && d.ops != nil {
		oneShot = parseEscalationType(msg)
	}
	if protocol.EscalationType(oneShot) == protocol.EscOversizedBead {
		if d.routeNewRoutableEscalation(ctx, protocol.EscalationType(oneShot), beadID, workerID, msg) {
			return
		}
	}

	// Persist escalation to SQLite before attempting tmux delivery.
	escalationID := d.insertEscalation(ctx, dbEscType, beadID, workerID, msg)

	if protocol.EscalationType(oneShot) == protocol.EscOversizedBead {
		d.spawnEscalationOneShot(ctx, escalationID, oneShot, beadID, workerID, msg)
		return
	}

	if err := d.escalator.Escalate(ctx, msg); err != nil {
		if isInformationalEscalation(protocol.EscalationType(dbEscType)) {
			_ = d.logEvent(ctx, "notification_skipped", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q,"message":%q,"type":%q}`, err.Error(), msg, dbEscType))
			return
		}
		_ = d.logEvent(ctx, "escalation_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"error":%q,"message":%q}`, err.Error(), msg))
	}

	// Spawn one-shot manager agent for actionable escalation types.
	// Only spawn for types with a one-shot playbook (use parseEscalationType, not extractEscalationType).
	if oneShot != "" {
		d.spawnEscalationOneShot(ctx, escalationID, oneShot, beadID, workerID, msg)
	}
}

func (d *Dispatcher) insertEscalation(ctx context.Context, escType, beadID, workerID, msg string) int64 {
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO escalations (type, bead_id, worker_id, message) VALUES (?, ?, ?, ?)`,
		escType, beadID, workerID, msg)
	if err != nil {
		return 0
	}
	escalationID, _ := res.LastInsertId()
	return escalationID
}

func (d *Dispatcher) routeNewRoutableEscalation(ctx context.Context, escType protocol.EscalationType, beadID, workerID, msg string) bool {
	if d.ops == nil || !isRoutableEscalationType(escType) || beadID == "" {
		return false
	}

	rec, wasCreated, err := d.createRoutableOpsRun(ctx, 0, escType, beadID, workerID, msg)
	if err != nil {
		_ = d.logEvent(ctx, "ops_run_route_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"type":%q,"error":%q}`, escType, err.Error()))
		return false
	}
	if !wasCreated {
		d.logOpsRunBlockedAssignment(ctx, rec, 0, escType, beadID, workerID)
		return true
	}

	escalationID := d.insertEscalation(ctx, string(escType), beadID, workerID, msg)
	if escalationID > 0 {
		if _, err := d.db.ExecContext(ctx, `UPDATE ops_runs SET escalation_id=? WHERE id=?`, escalationID, rec.ID); err != nil {
			_ = d.logEvent(ctx, "ops_run_route_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"ops_run_id":%d,"type":%q,"error":%q}`, rec.ID, escType, err.Error()))
		}
	}

	d.spawnEscalationOneShot(ctx, escalationID, string(escType), beadID, workerID, msg)
	return true
}

// applyPendingEscalations returns all unacked escalations as JSON.
func (d *Dispatcher) applyPendingEscalations() (string, error) {
	rows, err := d.db.QueryContext(context.Background(),
		`SELECT id, type, bead_id, worker_id, message, status, created_at, retry_count
		 FROM escalations WHERE status = 'pending' ORDER BY id`)
	if err != nil {
		return "", fmt.Errorf("query pending escalations: %w", err)
	}
	defer rows.Close()

	var escs []protocol.Escalation
	for rows.Next() {
		var e protocol.Escalation
		if err := rows.Scan(&e.ID, &e.Type, &e.BeadID, &e.WorkerID, &e.Message, &e.Status, &e.CreatedAt, &e.RetryCount); err != nil {
			return "", fmt.Errorf("scan escalation: %w", err)
		}
		escs = append(escs, e)
	}

	b, err := json.Marshal(escs)
	if err != nil {
		return "", fmt.Errorf("marshal escalations: %w", err)
	}
	return string(b), nil
}

// applyAckEscalation marks an escalation as acknowledged by ID.
func (d *Dispatcher) applyAckEscalation(args string) (string, error) {
	id := strings.TrimSpace(args)
	if id == "" {
		return "", fmt.Errorf("ack-escalation requires an escalation ID")
	}

	res, err := d.db.ExecContext(context.Background(),
		`UPDATE escalations SET status = 'acked', acked_at = datetime('now') WHERE id = ? AND status = 'pending'`,
		id)
	if err != nil {
		return "", fmt.Errorf("ack escalation: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Sprintf("escalation %s not found or already acked", id), nil
	}
	return fmt.Sprintf("acked escalation %s", id), nil
}

// escalationRetryLoop periodically re-delivers unacked escalations via tmux.
// Runs every EscalationRetryInterval (default 2 minutes), retries up to 5 times.
// Each iteration is wrapped in a defer/recover so a panic inside the body
// logs a goroutine_panic event and restarts the loop after exponential backoff.
func (d *Dispatcher) escalationRetryLoop(ctx context.Context) {
	interval := d.escalationRetryInterval
	if interval == 0 {
		interval = 2 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var restartCount int
	var lastPanicTime time.Time

	for {
		exit := func() (shouldExit bool) {
			defer func() {
				if r := recover(); r != nil {
					if d.handleLoopPanic(ctx, r, &restartCount, &lastPanicTime) {
						shouldExit = true
					}
				}
			}()
			select {
			case <-ctx.Done():
				return true
			case <-d.shutdownCh:
				return true
			case <-ticker.C:
				d.callRetryPendingEscalations(ctx)
			}
			return false
		}()
		if exit {
			return
		}
	}
}

func (d *Dispatcher) retryPendingEscalations(ctx context.Context) {
	if err := d.routePendingRoutableEscalations(ctx); err != nil {
		_ = d.logEvent(ctx, "pending_escalation_route_failed", "dispatcher", "", "",
			fmt.Sprintf(`{"error":%q}`, err.Error()))
	}

	rows, err := d.db.QueryContext(ctx,
		`SELECT id, type, bead_id, message FROM escalations
		 WHERE status = 'pending' AND retry_count < 5
		 ORDER BY id`)
	if err != nil {
		return
	}
	defer rows.Close()

	// Collect all escalations first (can't update while iterating)
	type pendingEscalation struct {
		id      int64
		escType string
		beadID  string
		msg     string
	}
	var pending []pendingEscalation

	for rows.Next() {
		var id int64
		var escType, msg string
		var beadID sql.NullString
		if err := rows.Scan(&id, &escType, &beadID, &msg); err != nil {
			continue
		}

		beadIDStr := ""
		if beadID.Valid {
			beadIDStr = beadID.String
		}
		pending = append(pending, pendingEscalation{
			id:      id,
			escType: escType,
			beadID:  beadIDStr,
			msg:     msg,
		})
	}
	_ = rows.Close() // #nosec G104 - defer handles cleanup on error

	// Process escalations after closing the query
	for _, esc := range pending {
		if d.shouldSkipPendingEscalationRetry(esc.escType) {
			continue
		}

		// Check if the underlying condition is resolved
		if !d.shouldRetryEscalation(ctx, esc.escType, esc.beadID) {
			// Condition resolved - auto-ack the escalation
			_, _ = d.db.ExecContext(ctx,
				`UPDATE escalations SET status = 'acked', acked_at = datetime('now') WHERE id = ?`,
				esc.id)
			continue
		}

		// Condition still holds - retry the escalation
		_ = d.escalator.Escalate(ctx, esc.msg)
		_, _ = d.db.ExecContext(ctx,
			`UPDATE escalations SET retry_count = retry_count + 1, last_retry_at = datetime('now') WHERE id = ?`,
			esc.id)
	}
}

func (d *Dispatcher) shouldSkipPendingEscalationRetry(escType string) bool {
	return !factoryhealth.IsKnownEscalationType(escType) ||
		(protocol.EscalationType(escType) == protocol.EscOversizedBead && d.ops != nil)
}

func (d *Dispatcher) routePendingRoutableEscalations(ctx context.Context) error {
	if d.ops == nil {
		return nil
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, type, bead_id, worker_id, message
		 FROM escalations
		 WHERE status = 'pending' AND retry_count < 5
		 ORDER BY id`)
	if err != nil {
		return fmt.Errorf("query pending routable escalations: %w", err)
	}
	defer rows.Close()

	type pendingRoutableEscalation struct {
		id       int64
		escType  protocol.EscalationType
		beadID   string
		workerID string
		msg      string
	}
	var pending []pendingRoutableEscalation
	for rows.Next() {
		var (
			id       int64
			escType  string
			beadID   sql.NullString
			workerID sql.NullString
			msg      string
		)
		if err := rows.Scan(&id, &escType, &beadID, &workerID, &msg); err != nil {
			return fmt.Errorf("scan pending routable escalation: %w", err)
		}
		if !isRoutableEscalationType(protocol.EscalationType(escType)) || !beadID.Valid || beadID.String == "" {
			continue
		}
		pending = append(pending, pendingRoutableEscalation{
			id:       id,
			escType:  protocol.EscalationType(escType),
			beadID:   beadID.String,
			workerID: workerID.String,
			msg:      msg,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pending routable escalations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close pending routable escalations: %w", err)
	}

	for _, esc := range pending {
		if !d.shouldRetryEscalation(ctx, string(esc.escType), esc.beadID) {
			d.ackEscalation(ctx, esc.id, esc.beadID, esc.workerID)
			continue
		}
		if err := d.routeExistingRoutableEscalation(ctx, esc.id, esc.escType, esc.beadID, esc.workerID, esc.msg); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) routeExistingRoutableEscalation(ctx context.Context, escalationID int64, escType protocol.EscalationType, beadID, workerID, msg string) error {
	rec, wasCreated, err := d.createRoutableOpsRun(ctx, escalationID, escType, beadID, workerID, msg)
	if err != nil {
		return err
	}
	if !wasCreated {
		d.logOpsRunBlockedAssignment(ctx, rec, escalationID, escType, beadID, workerID)
		d.ackEscalation(ctx, escalationID, beadID, workerID)
		return nil
	}
	d.spawnEscalationOneShot(ctx, escalationID, string(escType), beadID, workerID, msg)
	return nil
}

func (d *Dispatcher) createRoutableOpsRun(ctx context.Context, escalationID int64, escType protocol.EscalationType, beadID, workerID, msg string) (OpsRunRecord, bool, error) {
	runType, ok := routedOpsRunType(escType)
	if !ok {
		return OpsRunRecord{}, false, fmt.Errorf("unsupported routable escalation type %q", escType)
	}
	rec, wasCreated, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		EscalationID:  escalationID,
		Type:          string(runType),
		BeadID:        beadID,
		WorkerID:      workerID,
		DispatcherPID: os.Getpid(),
		Status:        opsRunStatusRunning,
		Error:         msg,
	})
	if err != nil {
		return OpsRunRecord{}, false, err
	}
	return rec, wasCreated, nil
}

func isRoutableEscalationType(escType protocol.EscalationType) bool {
	_, ok := routedOpsRunType(escType)
	return ok
}

func routedOpsRunType(escType protocol.EscalationType) (ops.Type, bool) {
	switch escType {
	case protocol.EscOversizedBead:
		return ops.OpsDecompose, true
	default:
		return "", false
	}
}

func (d *Dispatcher) logOpsRunBlockedAssignment(ctx context.Context, rec OpsRunRecord, escalationID int64, escType protocol.EscalationType, beadID, workerID string) {
	_ = d.logEvent(ctx, "ops_run_blocked_assignment", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"ops_run_id":%d,"escalation_id":%d,"type":%q,"escalation_type":%q}`, rec.ID, escalationID, rec.Type, escType))
}

// shouldRetryEscalation checks if an escalation's underlying condition still
// holds. Returns false if the condition is resolved (preventing spam), true
// if the escalation should be retried.
//
// Edge cases:
//   - Empty beadID + WORKER_CRASH: auto-ack — stale alert from a prev-session
//     worker with no bead assigned, stops the 2-minute replay loop (oro-p2ey)
//   - Empty beadID (other types): always retry (no bead context to check)
//   - beads.Show error: always retry (don't suppress on error)
//   - OVERSIZED_BEAD: never retries — the gate that raised it was removed
//   - Unknown escType: always retry (don't block future escalation types)
func (d *Dispatcher) shouldRetryEscalation(ctx context.Context, escType, beadID string) bool {
	// OVERSIZED_BEAD is checked before the beadID branch because it is stale
	// unconditionally: the admission gate that raised it was removed (it counted
	// Read: citations in the acceptance criteria, not change size), so no new
	// rows are created and every surviving one must drain regardless of bead
	// context. Keep this ahead of the switch's `default: return true`.
	if protocol.EscalationType(escType) == protocol.EscOversizedBead {
		return false
	}

	// Empty beadID: WORKER_CRASH auto-acks (stale prev-session alert, oro-p2ey);
	// all other types retry because there's no bead context to check.
	if beadID == "" {
		return protocol.EscalationType(escType) != protocol.EscWorkerCrash
	}

	// Check per-type conditions — helpers live in escalation_precheck.go
	switch protocol.EscalationType(escType) {
	case protocol.EscMissingAC:
		return d.retryMissingAC(ctx, beadID)
	case protocol.EscStuckWorker:
		return d.retryStuckWorker(beadID)
	case protocol.EscWorkerCrash, protocol.EscStuck:
		return d.retryBeadStillAssigned(ctx, beadID)
	case protocol.EscMergeConflict:
		return d.retryMergeConflict(ctx, beadID)
	case protocol.EscPriorityContention:
		return d.retryPriorityContention(ctx, beadID)
	case protocol.EscOversizedBead:
		// The oversized admission gate was removed (it counted Read: citations
		// in the acceptance criteria, not change size). Nothing raises this
		// escalation any more, so every surviving pending row is stale and must
		// drain. Keep the case: deleting it falls through to `default: return
		// true` below and retries these rows forever.
		return false
	case protocol.EscNonTDDAC:
		return d.retryNonTDDAC(ctx, beadID)
	case protocol.EscMergeComplete, protocol.EscManualIntegration, protocol.EscDependencyCycle:
		return false
	default:
		return true
	}
}

func isInformationalEscalation(escType protocol.EscalationType) bool {
	switch escType {
	case protocol.EscMergeComplete, protocol.EscManualIntegration:
		return true
	default:
		return false
	}
}

// parseEscalationType extracts the escalation type from a formatted
// [ORO-DISPATCH] message. Returns empty string if not a recognized type
// that has a one-shot playbook.
func parseEscalationType(msg string) string {
	// Format: [ORO-DISPATCH] TYPE: bead-id — summary.
	const prefix = "[ORO-DISPATCH] "
	_, after, found := strings.Cut(msg, prefix)
	if !found {
		return ""
	}
	escType, _, found := strings.Cut(after, ":")
	if !found {
		return ""
	}
	switch protocol.EscalationType(escType) {
	case protocol.EscStuckWorker, protocol.EscMergeConflict,
		protocol.EscPriorityContention, protocol.EscMissingAC, protocol.EscOversizedBead:
		return escType
	default:
		return ""
	}
}

// extractEscalationType extracts the escalation type from a formatted message
// without checking if it has a one-shot playbook. Used for database storage.
func extractEscalationType(msg string) string {
	// Format: [ORO-DISPATCH] TYPE: bead-id — summary.
	const prefix = "[ORO-DISPATCH] "
	_, after, found := strings.Cut(msg, prefix)
	if !found {
		return ""
	}
	escType, _, found := strings.Cut(after, ":")
	if !found {
		return ""
	}
	return escType
}

// spawnEscalationOneShot launches a one-shot claude -p process to handle
// the escalation. The result is logged asynchronously.
func (d *Dispatcher) spawnEscalationOneShot(ctx context.Context, escalationID int64, escType, beadID, workerID, msg string) {
	// Look up bead details for context (best-effort).
	var beadTitle, beadContext string
	if beadID != "" {
		if detail, err := d.beads.Show(ctx, beadID); err == nil && detail != nil {
			beadTitle = detail.Title
			beadContext = detail.Description
		}
	}

	// Look up worktree path, falling back to "." if not found.
	d.mu.Lock()
	workdir := d.worktreeByBead[beadID]
	d.mu.Unlock()
	if workdir == "" {
		workdir = "."
	}

	var resultCh <-chan ops.Result
	switch protocol.EscalationType(escType) {
	case protocol.EscMissingAC:
		// Dedup guard: skip if a WriteAC agent is already running for this bead.
		if d.ops.HasActiveForBead(beadID) {
			return
		}
		resultCh = d.ops.WriteAC(ctx, ops.WriteACOpts{
			BeadID:          beadID,
			BeadTitle:       beadTitle,
			BeadDescription: beadContext,
			Workdir:         workdir,
		})
	case protocol.EscOversizedBead:
		if d.ops.HasActiveForBead(beadID) {
			return
		}
		resultCh = d.ops.Decompose(ctx, ops.DecomposeOpts{
			BeadID:  beadID,
			Workdir: d.workdirForOpsRun(beadID),
			Reason:  msg,
		})
	default:
		resultCh = d.ops.Escalate(ctx, ops.EscalationOpts{
			EscalationType: escType,
			BeadID:         beadID,
			BeadTitle:      beadTitle,
			BeadContext:    beadContext,
			RecentHistory:  msg,
			Workdir:        workdir,
		})
	}

	d.safeGo(func() {
		d.handleEscalationResult(ctx, escalationID, escType, beadID, workerID, resultCh)
	})
}

// handleEscalationResult logs the one-shot escalation agent's outcome.
// If the one-shot fails (timeout, error, or non-zero exit), it records a
// failed ops run so health reporting can surface the failure.
func (d *Dispatcher) handleEscalationResult(ctx context.Context, escalationID int64, escType, beadID, workerID string, resultCh <-chan ops.Result) {
	result := <-resultCh
	if result.Err != nil {
		_ = d.logEvent(ctx, "oneshot_escalation_failed", "ops", beadID, workerID,
			fmt.Sprintf(`{"type":%q,"error":%q}`, escType, result.Err.Error()))
		if protocol.EscalationType(escType) == protocol.EscMissingAC || protocol.EscalationType(escType) == protocol.EscOversizedBead {
			d.recordAssignmentFailure(beadID)
		}
		d.completeOneShotOpsRunFailureBestEffort(ctx, escalationID, escType, beadID, workerID, result)
		d.ackEscalation(ctx, escalationID, beadID, workerID)
		return
	}
	_ = d.logEvent(ctx, "oneshot_escalation_complete", "ops", beadID, workerID,
		fmt.Sprintf(`{"type":%q,"verdict":%q,"feedback":%q}`, escType, result.Verdict, result.Feedback))

	if protocol.EscalationType(escType) == protocol.EscOversizedBead && result.Verdict == ops.VerdictFailed {
		d.recordAssignmentFailure(beadID)
		d.completeDecomposeOpsRunBestEffort(ctx, beadID, opsRunStatusFailed, string(result.Verdict), result.Feedback, result.Feedback)
		d.ackEscalation(ctx, escalationID, beadID, workerID)
		return
	}
	if protocol.EscalationType(escType) == protocol.EscOversizedBead {
		if err := d.validateDecomposeResult(ctx, beadID); err != nil {
			if errors.Is(err, errDecomposeValidationUnavailable) {
				_ = d.logEvent(ctx, "oneshot_escalation_validation_skipped", "ops", beadID, workerID,
					fmt.Sprintf(`{"type":%q,"error":%q}`, escType, err.Error()))
				d.clearAssignmentFailure(beadID)
				d.completeDecomposeOpsRunBestEffort(ctx, beadID, opsRunStatusResolved, string(result.Verdict), result.Feedback, "")
				d.ackEscalation(ctx, escalationID, beadID, workerID)
				return
			}
			d.recordAssignmentFailure(beadID)
			d.completeDecomposeOpsRunBestEffort(ctx, beadID, opsRunStatusFailed, string(result.Verdict), result.Feedback, err.Error())
			_ = d.logEvent(ctx, "oneshot_escalation_validation_failed", "ops", beadID, workerID,
				fmt.Sprintf(`{"type":%q,"error":%q}`, escType, err.Error()))
			return
		}
		d.clearAssignmentFailure(beadID)
		d.completeDecomposeOpsRunBestEffort(ctx, beadID, opsRunStatusResolved, string(result.Verdict), result.Feedback, "")
	}

	// Ack the escalation in the persistent queue so the retry loop doesn't re-deliver it.
	d.ackEscalation(ctx, escalationID, beadID, workerID)
}

func (d *Dispatcher) validateDecomposeResult(ctx context.Context, beadID string) error {
	if d == nil || d.db == nil {
		return fmt.Errorf("%w for %s: dispatcher db is nil", errDecomposeValidationUnavailable, beadID)
	}

	var parent struct {
		ID     string
		Type   string
		Status string
	}
	err := d.db.QueryRowContext(ctx, `
SELECT id, COALESCE(type, ''), COALESCE(status, '')
FROM beads
WHERE id=? AND deleted=0`, beadID).Scan(&parent.ID, &parent.Type, &parent.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("decompose validation failed for %s: parent bead missing", beadID)
	}
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return fmt.Errorf("%w for %s: %w", errDecomposeValidationUnavailable, beadID, err)
		}
		return fmt.Errorf("decompose validation failed for %s: load parent: %w", beadID, err)
	}

	children, err := d.loadDecomposeChildren(ctx, beadID)
	if err != nil {
		return err
	}
	if len(children) == 0 {
		if strings.EqualFold(parent.Type, "epic") || parent.Status == "closed" {
			return nil
		}
		return fmt.Errorf("decompose validation failed for %s: non-epic parent has no child tasks", beadID)
	}

	for _, child := range children {
		if !hasTDDAcceptance(child.AcceptanceCriteria) {
			return fmt.Errorf("decompose validation failed for %s: child %s acceptance criteria must include Test:, Cmd:, and Assert: markers", beadID, child.ID)
		}
		hasDep, err := d.parentDependsOnChild(ctx, beadID, child.ID)
		if err != nil {
			return err
		}
		if !hasDep {
			return fmt.Errorf("decompose validation failed for %s: parent does not depend on child %s", beadID, child.ID)
		}
	}
	return nil
}

func (d *Dispatcher) loadDecomposeChildren(ctx context.Context, beadID string) ([]protocol.Bead, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, COALESCE(type, ''), COALESCE(acceptance_criteria, '')
FROM beads
WHERE parent_id=? AND deleted=0
ORDER BY id`, beadID)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, fmt.Errorf("%w for %s: %w", errDecomposeValidationUnavailable, beadID, err)
		}
		return nil, fmt.Errorf("decompose validation failed for %s: load children: %w", beadID, err)
	}
	defer rows.Close()

	var children []protocol.Bead
	for rows.Next() {
		var child protocol.Bead
		if err := rows.Scan(&child.ID, &child.Type, &child.AcceptanceCriteria); err != nil {
			return nil, fmt.Errorf("decompose validation failed for %s: scan child: %w", beadID, err)
		}
		children = append(children, child)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("decompose validation failed for %s: iterate children: %w", beadID, err)
	}
	return children, nil
}

func hasTDDAcceptance(acceptance string) bool {
	return strings.Contains(acceptance, "Test:") &&
		strings.Contains(acceptance, "Cmd:") &&
		strings.Contains(acceptance, "Assert:")
}

func (d *Dispatcher) parentDependsOnChild(ctx context.Context, parentID, childID string) (bool, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM bead_deps
WHERE bead_id=? AND depends_on_id=? AND type IN ('blocks', 'conditional-blocks')`, parentID, childID).Scan(&n)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return false, fmt.Errorf("%w for %s: %w", errDecomposeValidationUnavailable, parentID, err)
		}
		return false, fmt.Errorf("decompose validation failed for %s: check dependency on %s: %w", parentID, childID, err)
	}
	return n > 0, nil
}

func (d *Dispatcher) clearAssignmentFailure(beadID string) {
	d.mu.Lock()
	delete(d.worktreeFailures, beadID)
	d.mu.Unlock()
}

func (d *Dispatcher) completeDecomposeOpsRunBestEffort(ctx context.Context, beadID, status, verdict, feedback, errorText string) {
	rec, err := FindBlockingOpsRun(ctx, d.db, string(ops.OpsDecompose), beadID)
	if err != nil {
		_ = d.logEvent(ctx, "ops_run_complete_failed", "dispatcher", beadID, "",
			fmt.Sprintf(`{"type":%q,"status":%q,"error":%q}`, ops.OpsDecompose, status, err.Error()))
		return
	}
	if rec == nil {
		return
	}
	if err := CompleteOpsRun(ctx, d.db, rec.ID, status, verdict, feedback, errorText); err != nil {
		_ = d.logEvent(ctx, "ops_run_complete_failed", "dispatcher", beadID, "",
			fmt.Sprintf(`{"ops_run_id":%d,"type":%q,"status":%q,"error":%q}`, rec.ID, ops.OpsDecompose, status, err.Error()))
	}
}

func (d *Dispatcher) completeOneShotOpsRunFailureBestEffort(ctx context.Context, escalationID int64, escType, beadID, workerID string, result ops.Result) {
	runType, ok := opsRunTypeForEscalationResult(escType, result)
	if !ok {
		return
	}
	errorText := ""
	if result.Err != nil {
		errorText = result.Err.Error()
	}
	verdict := string(result.Verdict)
	if verdict == "" {
		verdict = string(ops.VerdictFailed)
	}
	rec, err := FindBlockingOpsRun(ctx, d.db, string(runType), beadID)
	if err != nil {
		_ = d.logEvent(ctx, "ops_run_complete_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"type":%q,"status":%q,"error":%q}`, runType, opsRunStatusFailed, err.Error()))
		return
	}
	if rec != nil {
		d.populateOpsRunEscalationIDBestEffort(ctx, rec.ID, escalationID, runType, beadID, workerID)
		if err := CompleteOpsRun(ctx, d.db, rec.ID, opsRunStatusFailed, verdict, result.Feedback, errorText); err != nil {
			_ = d.logEvent(ctx, "ops_run_complete_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"ops_run_id":%d,"type":%q,"status":%q,"error":%q}`, rec.ID, runType, opsRunStatusFailed, err.Error()))
		}
		return
	}
	if _, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		EscalationID:  escalationID,
		Type:          string(runType),
		BeadID:        beadID,
		WorkerID:      workerID,
		DispatcherPID: os.Getpid(),
		Status:        opsRunStatusFailed,
		Verdict:       verdict,
		Feedback:      result.Feedback,
		Error:         errorText,
	}); err != nil {
		_ = d.logEvent(ctx, "ops_run_complete_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"type":%q,"status":%q,"error":%q}`, runType, opsRunStatusFailed, err.Error()))
	}
}

func (d *Dispatcher) populateOpsRunEscalationIDBestEffort(ctx context.Context, opsRunID, escalationID int64, runType ops.Type, beadID, workerID string) {
	if escalationID <= 0 {
		return
	}
	result, err := d.db.ExecContext(ctx, `
UPDATE ops_runs
SET escalation_id = ?
WHERE id = ?
  AND escalation_id IS NULL`, escalationID, opsRunID)
	if err != nil {
		_ = d.logEvent(ctx, "ops_run_update_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"ops_run_id":%d,"escalation_id":%d,"type":%q,"error":%q}`, opsRunID, escalationID, runType, err.Error()))
		return
	}
	if _, err := result.RowsAffected(); err != nil {
		_ = d.logEvent(ctx, "ops_run_update_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"ops_run_id":%d,"escalation_id":%d,"type":%q,"error":%q}`, opsRunID, escalationID, runType, err.Error()))
	}
}

func opsRunTypeForEscalationResult(escType string, result ops.Result) (ops.Type, bool) {
	if result.Type != "" {
		return result.Type, true
	}
	switch protocol.EscalationType(escType) {
	case protocol.EscMissingAC:
		return ops.OpsWriteAC, true
	case protocol.EscOversizedBead:
		return ops.OpsDecompose, true
	case protocol.EscStuckWorker, protocol.EscMergeConflict, protocol.EscPriorityContention:
		return ops.OpsEscalation, true
	default:
		return "", false
	}
}

func (d *Dispatcher) ackEscalation(ctx context.Context, escalationID int64, beadID, workerID string) {
	if escalationID > 0 {
		res, err := d.db.ExecContext(ctx,
			`UPDATE escalations SET status='acked', acked_at=datetime('now') WHERE id=? AND status='pending'`,
			escalationID)
		if err != nil {
			_ = d.logEvent(ctx, "escalation_ack_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"escalation_id":%d,"error":%q}`, escalationID, err.Error()))
		} else {
			n, _ := res.RowsAffected()
			_ = d.logEvent(ctx, "escalation_acked", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"escalation_id":%d,"rows_affected":%d}`, escalationID, n))
		}
	}
}
