package dispatcher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"oro/pkg/protocol"
)

var errAssignmentBlockedByReviewCheckpoint = errors.New("assignment blocked by nonterminal review checkpoint")

func (d *Dispatcher) createAssignment(ctx context.Context, beadID, workerID, worktree string) (int64, error) {
	res, err := d.db.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree)
SELECT ?, ?, ?
WHERE NOT EXISTS (
    SELECT 1
    FROM review_checkpoints_blocking_assignment
    WHERE bead_id = ?
)`, beadID, workerID, worktree, beadID)
	if tableMissingErr(err) {
		// Dispatcher unit fixtures may intentionally construct only SchemaDDL.
		// Production assignment runs after startupRecovery installs the native
		// bead schema and canonical checkpoint-admission view.
		res, err = d.db.ExecContext(ctx,
			`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
			beadID, workerID, worktree)
	}
	if err != nil {
		return 0, fmt.Errorf("create assignment: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("create assignment rows affected: %w", err)
	}
	if rows != 1 {
		return 0, fmt.Errorf("create assignment for %s: %w", beadID, errAssignmentBlockedByReviewCheckpoint)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create assignment last insert id: %w", err)
	}
	return id, nil
}

// persistBeadCount updates a counter column on the active assignment row for a bead.
// column must be one of "attempt_count" or "handoff_count". This is a best-effort
// operation: errors are logged but do not propagate.
func (d *Dispatcher) persistBeadCount(ctx context.Context, assignmentID int64, beadID, column string, value int) {
	if d.db == nil {
		return
	}
	// Allowlist columns to prevent SQL injection.
	switch column {
	case "attempt_count", "handoff_count":
	default:
		return
	}
	var (
		err error
		res sql.Result
	)
	if assignmentID > 0 {
		res, err = d.db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE assignments SET %s=? WHERE id=?`, column),
			value, assignmentID)
	} else {
		res, err = d.db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE assignments SET %s=? WHERE bead_id=? AND status='active'`, column),
			value, beadID)
	}
	if err != nil {
		_ = d.logEvent(ctx, "persist_count_failed", "dispatcher", beadID, "",
			fmt.Sprintf(`{"column":%q,"value":%d,"error":%q}`, column, value, err.Error()))
		return
	}
	if assignmentID > 0 {
		rows, rowsErr := res.RowsAffected()
		if rowsErr == nil && rows != 1 {
			_ = d.logEvent(ctx, "persist_count_target_mismatch", "dispatcher", beadID, "",
				fmt.Sprintf(`{"assignment_id":%d,"column":%q,"value":%d,"rows_affected":%d}`, assignmentID, column, value, rows))
		}
	}
}

// pruneStaleAgentBranches safe-deletes merged agent/* branches at startup.
// Unmerged or checked-out branches are preserved by git branch -d. Non-fatal:
// errors are logged and startup continues.
func (d *Dispatcher) releasePriorAssignment(ctx context.Context, w *trackedWorker, newBeadID string) {
	if w == nil {
		return
	}
	d.mu.Lock()
	priorBeadID := w.beadID
	priorAssignmentID := w.assignmentID
	workerID := w.id
	priorWorktree := d.worktreeByBead[priorBeadID]
	d.mu.Unlock()

	if priorBeadID == "" {
		persistedBeadID, persistedWorktree := d.activeAssignmentBead(ctx, priorAssignmentID, workerID)
		priorBeadID = persistedBeadID
		if priorWorktree == "" {
			priorWorktree = persistedWorktree
		}
	}

	if priorBeadID == "" || priorBeadID == newBeadID {
		return
	}

	// Preserve external close (oro-wp74): if the prior bead has been closed by
	// another party (e.g. manager dedup), do not reopen it. Reopening masks the
	// dedup and lets the bead be re-picked, feeding the oro-jev9 race.
	externallyClosed := false
	if detail, showErr := d.beads.Show(ctx, priorBeadID); showErr == nil && detail != nil && detail.Status == "closed" {
		externallyClosed = true
	}
	if priorAssignmentID > 0 {
		var err error
		if externallyClosed {
			err = d.completeAssignment(ctx, priorAssignmentID, priorBeadID)
		} else {
			err = d.requeueAssignment(ctx, priorAssignmentID)
		}
		if err != nil {
			_ = d.logEvent(ctx, "release_prior_assignment_failed", "dispatcher", priorBeadID, workerID, err.Error())
		}
	}
	if !externallyClosed {
		if err := d.updateBeadStatus(ctx, priorBeadID, "open"); err != nil {
			_ = d.logEvent(ctx, "release_prior_status_failed", "dispatcher", priorBeadID, workerID, err.Error())
		}
	}
	if priorWorktree != "" {
		d.mu.Lock()
		d.worktreeByBead[priorBeadID] = priorWorktree
		d.mu.Unlock()
		_ = d.logEvent(ctx, "worker_abandon_work_preserved", "dispatcher", priorBeadID, workerID,
			fmt.Sprintf(`{"branch":%q,"worktree":%q}`, protocol.BranchPrefix+priorBeadID, priorWorktree))
	}
	_ = d.logEvent(ctx, "worker_abandon_release", "dispatcher", priorBeadID, workerID,
		fmt.Sprintf(`{"reason":"reassign_to_%s","prior_assignment_id":%d,"externally_closed":%t}`, newBeadID, priorAssignmentID, externallyClosed))
}

func (d *Dispatcher) activeAssignmentBead(ctx context.Context, assignmentID int64, workerID string) (beadID, worktree string) {
	if assignmentID <= 0 || d.db == nil {
		return "", ""
	}

	if err := d.db.QueryRowContext(ctx,
		`SELECT bead_id, worktree FROM assignments WHERE id=? AND status='active'`,
		assignmentID).Scan(&beadID, &worktree); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			_ = d.logEvent(ctx, "release_prior_assignment_lookup_failed", "dispatcher", "", workerID, err.Error())
		}
		return "", ""
	}
	return beadID, worktree
}

func (d *Dispatcher) completeAssignment(ctx context.Context, assignmentID int64, beadID string) error {
	const maxSQLiteBusyRetries = 20
	for attempt := 0; ; attempt++ {
		err := d.completeAssignmentOnce(ctx, assignmentID, beadID)
		if err == nil || !isSQLiteBusyError(err) {
			return err
		}
		if attempt >= maxSQLiteBusyRetries {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("complete assignment retry canceled: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (d *Dispatcher) completeAssignmentOnce(ctx context.Context, assignmentID int64, beadID string) error {
	var (
		err error
		res sql.Result
	)
	if assignmentID > 0 {
		res, err = d.db.ExecContext(ctx,
			`UPDATE assignments SET status='completed', completed_at=datetime('now') WHERE id=? AND status!='quarantined'`,
			assignmentID)
	} else {
		res, err = d.db.ExecContext(ctx,
			`UPDATE assignments SET status='completed', completed_at=datetime('now') WHERE bead_id=? AND status='active'`,
			beadID)
	}
	if err != nil {
		return fmt.Errorf("complete assignment: %w", err)
	}
	if assignmentID > 0 {
		rows, rowsErr := res.RowsAffected()
		if rowsErr == nil && rows == 0 && d.assignmentIsQuarantined(ctx, assignmentID) {
			_ = d.logEvent(ctx, "assignment_completion_skipped_quarantined", "dispatcher", beadID, "",
				fmt.Sprintf(`{"assignment_id":%d}`, assignmentID))
			return nil
		}
		if rowsErr == nil && rows != 1 {
			return fmt.Errorf("complete assignment: assignment_id %d affected %d rows", assignmentID, rows)
		}
	}
	return nil
}

func isSQLiteBusyError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "sqlite_busy") ||
		strings.Contains(text, "database is locked") ||
		strings.Contains(text, "database table is locked")
}

func (d *Dispatcher) assignmentIsQuarantined(ctx context.Context, assignmentID int64) bool {
	var status string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&status); err != nil {
		return false
	}
	return status == "quarantined"
}

func (d *Dispatcher) assignmentIDLocked(workerID, beadID string) int64 {
	if w, ok := d.workers[workerID]; ok && (beadID == "" || w.beadID == beadID) {
		return w.assignmentID
	}
	return 0
}

func (d *Dispatcher) activeAssignmentIDForBead(ctx context.Context, beadID string) int64 {
	if d.db == nil || beadID == "" {
		return 0
	}
	var assignmentID int64
	if err := d.db.QueryRowContext(ctx,
		`SELECT id FROM assignments WHERE bead_id=? AND status='active' ORDER BY id DESC LIMIT 1`,
		beadID,
	).Scan(&assignmentID); err != nil {
		return 0
	}
	return assignmentID
}

func (d *Dispatcher) pendingCommands(ctx context.Context) ([]protocol.CommandRow, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, directive, args, status, created_at, COALESCE(processed_at, '') FROM commands WHERE status='pending' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query pending commands: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var cmds []protocol.CommandRow
	for rows.Next() {
		var c protocol.CommandRow
		if err := rows.Scan(&c.ID, &c.Directive, &c.Args, &c.Status, &c.CreatedAt, &c.ProcessedAt); err != nil {
			return nil, fmt.Errorf("scan command: %w", err)
		}
		cmds = append(cmds, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate commands: %w", err)
	}
	return cmds, nil
}

func (d *Dispatcher) markCommandProcessed(ctx context.Context, id int64) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE commands SET status='processed', processed_at=datetime('now') WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("mark command processed: %w", err)
	}
	return nil
}

// sendToWorker, maxPendingMessages → worker_pool.go

// shutdownSequence orchestrates the four-phase graceful shutdown:
//  1. Cancel ops agents and abort in-flight merges (safe before worker stop).
//  2. Send PREPARE_SHUTDOWN to all workers, wait for drain or force-kill.
//     3b. Reset all active assignments back to open so beads are re-assignable on restart.
//  3. Remove worktrees and flush bead state (safe after workers are stopped).
