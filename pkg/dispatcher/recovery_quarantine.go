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

// RecoveryQuarantineEmptySafe reports whether an inspected quarantine has no
// dirty files or unique branch commits to preserve. The recovery CLI and the
// dispatcher share this predicate so their definition of discardable state
// cannot drift.
func RecoveryQuarantineEmptySafe(dirtyFiles int, branchExists bool, branchAhead int) bool {
	if dirtyFiles > 0 {
		return false
	}
	return !branchExists || branchAhead == 0
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

	if q.AssignmentID <= 0 {
		return insertRecoveryQuarantineRow(ctx, d.db, q)
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin recovery quarantine transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := markAssignmentQuarantinedExec(ctx, tx, q.AssignmentID); err != nil {
		return 0, err
	}

	id, found, err := findOpenRecoveryQuarantineForAssignment(ctx, tx, q.AssignmentID)
	if err != nil {
		return 0, err
	}
	if found {
		if _, err := tx.ExecContext(ctx, `
UPDATE recovery_quarantines
SET bead_id=?, worker_id=?, worktree=?, branch=?, reason=?, details=?
WHERE id=? AND status='open'`,
			q.BeadID, q.WorkerID, q.Worktree, q.Branch, q.Reason, q.Details, id); err != nil {
			return 0, fmt.Errorf("update recovery quarantine: %w", err)
		}
	} else {
		id, err = insertRecoveryQuarantineRow(ctx, tx, q)
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit recovery quarantine transaction: %w", err)
	}
	return id, nil
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

func findOpenRecoveryQuarantineForAssignment(ctx context.Context, ex execer, assignmentID int64) (id int64, found bool, err error) {
	if assignmentID <= 0 {
		return 0, false, nil
	}

	if err := ex.QueryRowContext(ctx, `
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

// resolvedPreservedMismatchAssignment reports whether startup has already
// quarantined and explicitly requeued this exact preserved assignment. The
// caller has selected only requeued assignments, so this durable record lets a
// later restart retain the operator's decision without claiming its detached
// dirty worktree for reuse.
func (d *Dispatcher) resolvedPreservedMismatchAssignment(ctx context.Context, assignmentID int64) bool {
	if d.db == nil || assignmentID <= 0 {
		return false
	}

	var found bool
	err := d.db.QueryRowContext(ctx, `
SELECT EXISTS(
    SELECT 1
    FROM recovery_quarantines
    WHERE assignment_id=?
      AND status='resolved'
)`, assignmentID).Scan(&found)
	return err == nil && found
}

func (d *Dispatcher) resolvedPreservedMismatchForRequeuedBead(
	ctx context.Context, beadID string,
) (assignmentID int64, found bool, err error) {
	if d.db == nil || beadID == "" {
		return 0, false, nil
	}

	err = d.db.QueryRowContext(ctx, `
SELECT a.id
FROM assignments a
JOIN recovery_quarantines q ON q.assignment_id=a.id
WHERE a.bead_id=?
  AND a.status='requeued'
  AND q.reason='branch_worktree_mismatch'
  AND q.status='resolved'
  AND NOT EXISTS (
      SELECT 1
      FROM assignments newer
      WHERE newer.bead_id=a.bead_id AND newer.id>a.id
  )
ORDER BY q.id DESC
LIMIT 1`, beadID).Scan(&assignmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("query resolved preserved mismatch for bead %s: %w", beadID, err)
	}
	return assignmentID, true, nil
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

// autoRedeployablePreservedWorktrees returns only quarantined beads whose
// preserved state is safe to prove again at dispatch time. Quarantine reasons
// that represent an unresolved merge conflict or a known branch/worktree
// mismatch always remain human-owned recovery work.
func (d *Dispatcher) autoRedeployablePreservedWorktrees(ctx context.Context) (map[string]bool, error) {
	if d.db == nil {
		return nil, nil
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT q.bead_id, q.worktree, q.branch, q.status
FROM recovery_quarantines q
LEFT JOIN assignments a ON a.id=q.assignment_id
WHERE (q.status='open' OR (q.status='resolved' AND a.status='requeued'))
  AND q.reason NOT IN ('merge_conflict_resolution_failed', 'branch_worktree_mismatch')
  AND q.worktree IS NOT NULL AND q.worktree != ''
  AND q.branch IS NOT NULL AND q.branch != ''`)
	if err != nil {
		return nil, fmt.Errorf("query auto-redeployable recovery quarantines: %w", err)
	}
	defer func() { _ = rows.Close() }()

	redeployable := make(map[string]bool)
	for rows.Next() {
		var beadID, worktree, branch, status string
		if err := rows.Scan(&beadID, &worktree, &branch, &status); err != nil {
			return nil, fmt.Errorf("scan auto-redeployable recovery quarantine: %w", err)
		}
		if d.preservedWorktreeSafeForRedeploy(ctx, beadID, worktree, branch, status == "resolved") {
			redeployable[beadID] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate auto-redeployable recovery quarantines: %w", err)
	}
	return redeployable, nil
}

func (d *Dispatcher) preservedWorktreeSafeForRedeploy(
	ctx context.Context, beadID, worktree, branch string, restoreUntracked bool,
) bool {
	expectedBranch := protocol.BranchPrefix + beadID
	if beadID == "" || worktree == "" || branch != expectedBranch || !d.worktrees.Exists(ctx, worktree) {
		return false
	}
	d.mu.Lock()
	available := d.preservedWorktreeUnownedLocked(beadID, worktree, restoreUntracked)
	d.mu.Unlock()
	if !available {
		return false
	}
	currentBranch, err := d.worktrees.CurrentBranch(ctx, worktree)
	if err != nil || currentBranch != expectedBranch {
		return false
	}
	dirty, _, err := d.worktreeDirty(ctx, beadID, worktree)
	if err != nil || dirty {
		return false
	}

	// The offline recovery command resolves the quarantine and requeues its
	// preserved assignment durably, but cannot rebuild this process-local map.
	// Recheck live ownership before restoring it so a concurrent assignment
	// cannot be displaced by stale recovery state.
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.preservedWorktreeUnownedLocked(beadID, worktree, restoreUntracked) {
		return false
	}
	if restoreUntracked && d.worktreeByBead[beadID] == "" {
		d.worktreeByBead[beadID] = worktree
	}
	return true
}

// preservedWorktreeUnownedLocked checks the process-local ownership state.
// The caller holds d.mu so validation and an optional restore can be made
// atomically after the filesystem safety checks complete.
func (d *Dispatcher) preservedWorktreeUnownedLocked(beadID, worktree string, allowUntracked bool) bool {
	for _, worker := range d.workers {
		if worker.beadID == beadID && worker.state != protocol.WorkerIdle {
			return false
		}
	}
	trackedWorktree := d.worktreeByBead[beadID]
	return trackedWorktree == worktree || (allowUntracked && trackedWorktree == "")
}

// markAssignmentQuarantinedExec flips an assignment to status='quarantined'
// and clears completed_at. It is the single source of truth for the
// quarantine UPDATE, shared by the online dispatcher and the offline recovery
// command so the two paths cannot drift.
func markAssignmentQuarantinedExec(ctx context.Context, ex execer, assignmentID int64) error {
	res, err := ex.ExecContext(ctx,
		`UPDATE assignments SET status='quarantined', completed_at=NULL WHERE id=? AND status IN ('active', 'quarantined')`,
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

// countPreservableRecoveryQuarantines returns open quarantines that must freeze
// unrelated assignment. An unsafe stale branch on an open bead remains visible
// and protected per-bead, but its unmerged branch is expected active work, not
// a factory-wide recovery hazard. Human-owned recovery work is also excluded:
// its branch and worktree remain protected by per-bead filtering, but an
// operator taking ownership must not freeze unrelated factory work. Without a
// stored branch or worktree, the dispatcher can prove the CLI empty-safe
// predicate with zero dirty files and no branch; any stored recovery location
// remains blocking until it is inspected or resolved explicitly.
func (d *Dispatcher) countPreservableRecoveryQuarantines(ctx context.Context) (int, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT bead_id, reason, COALESCE(branch, ''), COALESCE(worktree, '')
FROM recovery_quarantines
WHERE status='open'`)
	if err != nil {
		if tableMissingErr(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("query preservable recovery quarantines: %w", err)
	}
	defer func() { _ = rows.Close() }()

	preservable := 0
	for rows.Next() {
		var beadID, reason, branch, worktree string
		if err := rows.Scan(&beadID, &reason, &branch, &worktree); err != nil {
			return 0, fmt.Errorf("scan preservable recovery quarantine: %w", err)
		}
		if d.unsafeStaleBranchOnOpenBead(ctx, beadID, reason) {
			continue
		}
		if branch == "" && worktree == "" && RecoveryQuarantineEmptySafe(0, false, 0) {
			continue
		}
		preservable++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate preservable recovery quarantines: %w", err)
	}
	return preservable, nil
}

func (d *Dispatcher) unsafeStaleBranchOnOpenBead(ctx context.Context, beadID, reason string) bool {
	if reason != "unsafe_stale_branch" {
		return false
	}
	detail, err := d.beads.Show(ctx, beadID)
	return err == nil && detail != nil && strings.EqualFold(detail.Status, "open")
}

func (d *Dispatcher) autoResolveEmptySafeRecoveryQuarantines(ctx context.Context) int {
	records, err := d.listOpenRecoveryQuarantines(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "startup_recovery_quarantine_auto_resolve_failed", "dispatcher", "", "", err.Error())
		return 0
	}

	resolved := 0
	for _, record := range records {
		emptySafe, inspectErr := d.recoveryQuarantineEmptySafeAtStartup(ctx, record)
		if inspectErr != nil {
			_ = d.logEvent(ctx, "startup_recovery_quarantine_inspection_failed", "dispatcher", record.BeadID, record.WorkerID,
				fmt.Sprintf(`{"quarantine_id":%d,"error":%q}`, record.ID, inspectErr.Error()))
			continue
		}
		if !emptySafe {
			continue
		}
		if err := d.resolveRecoveryQuarantine(ctx, record.ID); err != nil {
			_ = d.logEvent(ctx, "startup_recovery_quarantine_auto_resolve_failed", "dispatcher", record.BeadID, record.WorkerID,
				fmt.Sprintf(`{"quarantine_id":%d,"error":%q}`, record.ID, err.Error()))
			continue
		}
		resolved++
		_ = d.logEvent(ctx, "startup_recovery_quarantine_auto_resolved", "dispatcher", record.BeadID, record.WorkerID,
			fmt.Sprintf(`{"quarantine_id":%d,"assignment_id":%d,"status":"closed","mode":"discard-empty-safe","reason":%q}`,
				record.ID, record.AssignmentID, record.Reason))
	}
	return resolved
}

func (d *Dispatcher) recoveryQuarantineEmptySafeAtStartup(ctx context.Context, record recoveryQuarantineRecord) (bool, error) {
	if record.Worktree != "" && d.worktrees.Exists(ctx, record.Worktree) {
		return false, nil
	}
	branchExists := false
	if record.Branch != "" {
		var err error
		branchExists, err = d.worktrees.BranchExists(ctx, record.Branch)
		if err != nil {
			return false, fmt.Errorf("inspect recovery branch %s: %w", record.Branch, err)
		}
	}
	if branchExists {
		return false, nil
	}
	return RecoveryQuarantineEmptySafe(0, branchExists, 0), nil
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
