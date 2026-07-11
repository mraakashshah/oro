package dispatcher

import (
	"context"
	"database/sql"
	"fmt"

	"oro/pkg/protocol"
)

// Recovery quarantine reasons emitted by the offline recovery path.
const (
	// reasonStaleActiveAssignment matches the online dispatcher's
	// abandonStaleActiveAssignments reason so operators see one consistent
	// label whether a stale row was quarantined online or offline.
	reasonStaleActiveAssignment = "stale_active_assignment"
	// reasonOrphanBeadAssignment marks an active assignment whose beads row
	// no longer exists. These cannot be requeued (there is no bead to run),
	// so a distinct reason lets operators triage them separately.
	reasonOrphanBeadAssignment = "orphan_bead_assignment"
)

// QuarantinedAssignment records one assignment moved to quarantine by an
// offline recovery run.
type QuarantinedAssignment struct {
	AssignmentID int64
	BeadID       string
	Reason       string
}

// AbandonResult summarizes an AbandonAllActiveAssignments run.
type AbandonResult struct {
	// Total is the number of assignments quarantined.
	Total int
	// WithBead counts quarantined assignments whose beads row still exists.
	WithBead int
	// Orphaned counts quarantined assignments whose beads row is missing.
	Orphaned int
	// Quarantined lists every assignment moved to quarantine.
	Quarantined []QuarantinedAssignment
}

// activeAssignment is a single status='active' row read during offline recovery.
type activeAssignment struct {
	id       int64
	beadID   string
	workerID string
	worktree string
}

// AbandonAllActiveAssignments quarantines every status='active' assignment in
// the state DB without starting the dispatcher or running the v3->v4
// migration. It exists to break the deadlock where stale active assignment
// rows block ensureNoActiveAssignments (and thus the v4 migration and
// `oro start`), so the dispatcher can never come up to clean them.
//
// It mirrors the online abandonStaleActiveAssignments/createRecoveryQuarantine
// path exactly — reusing markAssignmentQuarantinedExec and
// insertRecoveryQuarantineRow — but runs unconditionally (there is no
// connected-worker check, because no dispatcher is running) and inside a
// single transaction. Assignments whose beads row is missing are quarantined
// with reasonOrphanBeadAssignment; all others use reasonStaleActiveAssignment.
//
// It only ever writes to the assignments and recovery_quarantines tables; it
// never modifies beads or any task table.
func AbandonAllActiveAssignments(ctx context.Context, db *sql.DB) (AbandonResult, error) {
	active, err := selectActiveAssignments(ctx, db)
	if err != nil {
		return AbandonResult{}, err
	}
	if len(active) == 0 {
		return AbandonResult{}, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return AbandonResult{}, fmt.Errorf("begin offline recovery transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := quarantineActiveAssignments(ctx, tx, active)
	if err != nil {
		return AbandonResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return AbandonResult{}, fmt.Errorf("commit offline recovery transaction: %w", err)
	}
	return result, nil
}

// selectActiveAssignments reads every status='active' assignment row. It runs
// before the transaction so the row set is stable while we quarantine.
func selectActiveAssignments(ctx context.Context, db *sql.DB) ([]activeAssignment, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, bead_id, worker_id, worktree FROM assignments WHERE status='active'`)
	if err != nil {
		return nil, fmt.Errorf("scan active assignments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var active []activeAssignment
	for rows.Next() {
		var a activeAssignment
		if err := rows.Scan(&a.id, &a.beadID, &a.workerID, &a.worktree); err != nil {
			return nil, fmt.Errorf("scan active assignment: %w", err)
		}
		active = append(active, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active assignments: %w", err)
	}
	return active, nil
}

// quarantineActiveAssignments quarantines each active assignment inside tx,
// reusing the shared dispatcher primitives so the SQL matches the online path.
func quarantineActiveAssignments(ctx context.Context, tx *sql.Tx, active []activeAssignment) (AbandonResult, error) {
	var result AbandonResult
	for _, a := range active {
		beadExists, err := beadRowExists(ctx, tx, a.beadID)
		if err != nil {
			return AbandonResult{}, err
		}
		reason := reasonStaleActiveAssignment
		details := "active assignment belongs to a disconnected worker"
		if !beadExists {
			reason = reasonOrphanBeadAssignment
			details = "active assignment references a missing bead row"
		}

		if err := markAssignmentQuarantinedExec(ctx, tx, a.id); err != nil {
			return AbandonResult{}, err
		}
		if _, err := insertRecoveryQuarantineRow(ctx, tx, recoveryQuarantine{
			BeadID:       a.beadID,
			AssignmentID: a.id,
			WorkerID:     a.workerID,
			Worktree:     a.worktree,
			Branch:       protocol.BranchPrefix + a.beadID,
			Reason:       reason,
			Details:      details,
		}); err != nil {
			return AbandonResult{}, err
		}

		result.Total++
		if beadExists {
			result.WithBead++
		} else {
			result.Orphaned++
		}
		result.Quarantined = append(result.Quarantined, QuarantinedAssignment{
			AssignmentID: a.id,
			BeadID:       a.beadID,
			Reason:       reason,
		})
	}
	return result, nil
}

// beadRowExists reports whether a beads row with the given id exists.
func beadRowExists(ctx context.Context, ex execer, beadID string) (bool, error) {
	var exists int
	err := ex.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM beads WHERE id=?)`, beadID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check bead row exists: %w", err)
	}
	return exists == 1, nil
}
