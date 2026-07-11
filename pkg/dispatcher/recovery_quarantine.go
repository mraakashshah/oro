package dispatcher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"oro/pkg/protocol"
)

// execer is the subset of database access shared by *sql.DB and *sql.Tx.
// Extracting the two primitive recovery-quarantine writes behind this
// interface lets both the online *Dispatcher path and the offline
// AbandonAllActiveAssignments transaction reuse identical SQL, so the
// quarantine semantics cannot drift between them.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

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
		id, ok, err := d.coalesceOpenRecoveryQuarantineForAssignment(ctx, q)
		if err != nil {
			return 0, err
		}
		if ok {
			return id, nil
		}
	}

	return insertRecoveryQuarantineRow(ctx, d.db, q)
}

// insertRecoveryQuarantineRow inserts (or coalesces onto the existing open
// row for the same bead_id+reason) a recovery_quarantines row and returns its
// id. It is the single source of truth for the open-quarantine INSERT so the
// online dispatcher and the offline recovery command emit identical SQL.
func insertRecoveryQuarantineRow(ctx context.Context, ex execer, q recoveryQuarantine) (int64, error) {
	if _, err := ex.ExecContext(ctx, `
INSERT INTO recovery_quarantines
    (bead_id, assignment_id, worker_id, worktree, branch, reason, details, status)
VALUES (?, ?, ?, ?, ?, ?, ?, 'open')
ON CONFLICT(bead_id, reason) WHERE status='open' DO UPDATE SET
    assignment_id=excluded.assignment_id,
    worker_id=excluded.worker_id,
    worktree=excluded.worktree,
    branch=excluded.branch,
    details=excluded.details`,
		q.BeadID, nullableInt64(q.AssignmentID), q.WorkerID, q.Worktree, q.Branch, q.Reason, q.Details); err != nil {
		return 0, fmt.Errorf("create recovery quarantine: %w", err)
	}

	var id int64
	if err := ex.QueryRowContext(ctx,
		`SELECT id FROM recovery_quarantines WHERE bead_id=? AND reason=? AND status='open'`,
		q.BeadID, q.Reason).Scan(&id); err != nil {
		return 0, fmt.Errorf("lookup recovery quarantine: %w", err)
	}
	return id, nil
}

func (d *Dispatcher) coalesceOpenRecoveryQuarantineForAssignment(ctx context.Context, q recoveryQuarantine) (id int64, ok bool, err error) {
	if err := d.markAssignmentQuarantined(ctx, q.AssignmentID); err != nil {
		return 0, false, err
	}

	id, ok, err = d.findOpenRecoveryQuarantineForAssignment(ctx, q.AssignmentID)
	if err != nil || !ok {
		return id, ok, err
	}

	if _, err := d.db.ExecContext(ctx, `
UPDATE recovery_quarantines
SET bead_id=?, worker_id=?, worktree=?, branch=?, reason=?, details=?
WHERE id=? AND status='open'`,
		q.BeadID, q.WorkerID, q.Worktree, q.Branch, q.Reason, q.Details, id); err != nil {
		return 0, false, fmt.Errorf("update recovery quarantine: %w", err)
	}
	return id, true, nil
}

func (d *Dispatcher) findOpenRecoveryQuarantineForAssignment(ctx context.Context, assignmentID int64) (id int64, found bool, err error) {
	if d.db == nil {
		return 0, false, fmt.Errorf("find recovery quarantine: db is nil")
	}
	if assignmentID <= 0 {
		return 0, false, nil
	}

	if err := d.db.QueryRowContext(ctx, `
SELECT id
FROM recovery_quarantines
WHERE assignment_id=? AND status='open'
ORDER BY id
LIMIT 1`, assignmentID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("find recovery quarantine: %w", err)
	}
	return id, true, nil
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
	return markAssignmentQuarantinedExec(ctx, d.db, assignmentID)
}

// markAssignmentQuarantinedExec flips an assignment to status='quarantined'
// and clears completed_at. It is the single source of truth for the
// quarantine UPDATE, shared by the online dispatcher and the offline recovery
// command so the two paths cannot drift.
func markAssignmentQuarantinedExec(ctx context.Context, ex execer, assignmentID int64) error {
	res, err := ex.ExecContext(ctx,
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
