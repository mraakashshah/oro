package dispatcher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"oro/pkg/protocol"
)

type recoveryQuarantine struct {
	BeadID       string
	AssignmentID int64
	WorkerID     string
	Worktree     string
	Branch       string
	Reason       string
	Details      string
}

type recoveryQuarantineRecord struct {
	ID           int64
	BeadID       string
	AssignmentID int64
	WorkerID     string
	Worktree     string
	Branch       string
	Reason       string
	Details      string
	Status       string
	CreatedAt    string
	ResolvedAt   string
}

func (d *Dispatcher) createRecoveryQuarantine(ctx context.Context, q recoveryQuarantine) (int64, error) {
	if d.db == nil {
		return 0, fmt.Errorf("create recovery quarantine: db is nil")
	}
	if q.BeadID == "" {
		return 0, fmt.Errorf("create recovery quarantine: bead id is required")
	}
	if q.Reason == "" {
		return 0, fmt.Errorf("create recovery quarantine: reason is required")
	}

	if q.AssignmentID > 0 {
		if err := d.markAssignmentQuarantined(ctx, q.AssignmentID); err != nil {
			return 0, err
		}
	}

	_, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines
    (bead_id, assignment_id, worker_id, worktree, branch, reason, details, status)
VALUES (?, ?, ?, ?, ?, ?, ?, 'open')
ON CONFLICT(bead_id, reason) WHERE status='open' DO UPDATE SET
    assignment_id=excluded.assignment_id,
    worker_id=excluded.worker_id,
    worktree=excluded.worktree,
    branch=excluded.branch,
    details=excluded.details`,
		q.BeadID, nullableInt64(q.AssignmentID), q.WorkerID, q.Worktree, q.Branch, q.Reason, q.Details)
	if err != nil {
		return 0, fmt.Errorf("create recovery quarantine: %w", err)
	}

	var id int64
	if err := d.db.QueryRowContext(ctx,
		`SELECT id FROM recovery_quarantines WHERE bead_id=? AND reason=? AND status='open'`,
		q.BeadID, q.Reason).Scan(&id); err != nil {
		return 0, fmt.Errorf("lookup recovery quarantine: %w", err)
	}
	return id, nil
}

func (d *Dispatcher) quarantineUnsafeRecoveryWork(ctx context.Context, q recoveryQuarantine) {
	if q.Branch == "" && q.BeadID != "" {
		q.Branch = protocol.BranchPrefix + q.BeadID
	}
	if q.Worktree != "" && q.BeadID != "" {
		d.mu.Lock()
		d.worktreeByBead[q.BeadID] = q.Worktree
		d.mu.Unlock()
	}
	id, err := d.createRecoveryQuarantine(ctx, q)
	if err != nil {
		_ = d.logEvent(ctx, "recovery_quarantine_create_failed", "dispatcher", q.BeadID, q.WorkerID, err.Error())
		return
	}
	_ = d.logEvent(ctx, "recovery_work_quarantined", "dispatcher", q.BeadID, q.WorkerID,
		fmt.Sprintf(`{"quarantine_id":%d,"assignment_id":%d,"reason":%q,"branch":%q,"worktree":%q}`, id, q.AssignmentID, q.Reason, q.Branch, q.Worktree))
}

func (d *Dispatcher) markAssignmentQuarantined(ctx context.Context, assignmentID int64) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE assignments SET status='quarantined', completed_at=NULL WHERE id=?`,
		assignmentID)
	if err != nil {
		return fmt.Errorf("mark assignment quarantined: %w", err)
	}
	rows, rowsErr := res.RowsAffected()
	if rowsErr == nil && rows != 1 {
		return fmt.Errorf("mark assignment quarantined: assignment_id %d affected %d rows", assignmentID, rows)
	}
	return nil
}

func (d *Dispatcher) listOpenRecoveryQuarantines(ctx context.Context) ([]recoveryQuarantineRecord, error) {
	if d.db == nil {
		return nil, fmt.Errorf("list recovery quarantines: db is nil")
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT id, bead_id, COALESCE(assignment_id, 0), COALESCE(worker_id, ''), COALESCE(worktree, ''),
       COALESCE(branch, ''), reason, details, status, created_at, COALESCE(resolved_at, '')
FROM recovery_quarantines
WHERE status='open'
ORDER BY id`)
	if err != nil {
		if tableMissingErr(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list recovery quarantines: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var records []recoveryQuarantineRecord
	for rows.Next() {
		var r recoveryQuarantineRecord
		if err := rows.Scan(&r.ID, &r.BeadID, &r.AssignmentID, &r.WorkerID, &r.Worktree,
			&r.Branch, &r.Reason, &r.Details, &r.Status, &r.CreatedAt, &r.ResolvedAt); err != nil {
			return nil, fmt.Errorf("scan recovery quarantine: %w", err)
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recovery quarantines: %w", err)
	}
	return records, nil
}

func (d *Dispatcher) resolveRecoveryQuarantine(ctx context.Context, id int64) error {
	if d.db == nil {
		return fmt.Errorf("resolve recovery quarantine: db is nil")
	}
	res, err := d.db.ExecContext(ctx,
		`UPDATE recovery_quarantines SET status='resolved', resolved_at=datetime('now') WHERE id=? AND status='open'`,
		id)
	if err != nil {
		return fmt.Errorf("resolve recovery quarantine: %w", err)
	}
	rows, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("resolve recovery quarantine rows affected: %w", rowsErr)
	}
	if rows == 1 {
		return nil
	}

	var status string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM recovery_quarantines WHERE id=?`, id).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("resolve recovery quarantine: id %d not found", id)
		}
		return fmt.Errorf("resolve recovery quarantine lookup: %w", err)
	}
	if status == "resolved" {
		return nil
	}
	return fmt.Errorf("resolve recovery quarantine: id %d has status %q", id, status)
}

func (d *Dispatcher) requeueAssignment(ctx context.Context, assignmentID int64) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE assignments SET status='requeued', completed_at=datetime('now') WHERE id=? AND status IN ('active','quarantined','completed')`,
		assignmentID)
	if err != nil {
		return fmt.Errorf("requeue assignment: %w", err)
	}
	rows, rowsErr := res.RowsAffected()
	if rowsErr == nil && rows != 1 {
		return fmt.Errorf("requeue assignment: assignment_id %d affected %d rows", assignmentID, rows)
	}
	return nil
}

func tableMissingErr(err error) bool {
	return err != nil && (errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no such table"))
}
