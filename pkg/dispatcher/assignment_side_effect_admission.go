package dispatcher

import (
	"context"
	"fmt"
	"os"
	"time"
)

type assignmentSideEffectAdmission struct {
	beadID string
	token  string
}

func (d *Dispatcher) acquireAssignmentSideEffectAdmission(
	ctx context.Context,
	beadID, workerID, stage string,
) (*assignmentSideEffectAdmission, error) {
	if d == nil || d.db == nil || beadID == "" {
		return nil, fmt.Errorf("acquire assignment side-effect admission: missing dispatcher database or bead ID")
	}
	token := fmt.Sprintf("%d-%d", os.Getpid(), d.assignmentSideEffectAdmissionSeq.Add(1))
	var rows int64
	err := retrySQLiteBusyOperation(ctx, func() error {
		result, execErr := d.db.ExecContext(ctx, `
INSERT INTO assignment_side_effect_admissions (bead_id, owner_token)
SELECT ?, ?
WHERE NOT EXISTS (
    SELECT 1 FROM review_checkpoints_blocking_assignment WHERE bead_id = ?
)
ON CONFLICT(bead_id) DO NOTHING`, beadID, token, beadID)
		if execErr != nil {
			return fmt.Errorf("insert assignment side-effect admission: %w", execErr)
		}
		rows, execErr = result.RowsAffected()
		if execErr != nil {
			return fmt.Errorf("count inserted assignment side-effect admission: %w", execErr)
		}
		return nil
	})
	if err != nil {
		d.recordAssignmentObservation("review_checkpoint", err)
		return nil, fmt.Errorf("acquire assignment side-effect admission for %s: %w", beadID, err)
	}
	if rows != 1 {
		_ = d.logEvent(ctx, "review_checkpoint_assignment_blocked", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"reason":"durable_nonterminal_review_checkpoint_or_reserved_admission","stage":%q}`, stage))
		return nil, nil
	}
	return &assignmentSideEffectAdmission{beadID: beadID, token: token}, nil
}

func (d *Dispatcher) releaseAssignmentSideEffectAdmission(ctx context.Context, admission *assignmentSideEffectAdmission) {
	if d == nil || d.db == nil || admission == nil {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := retrySQLiteBusyOperation(releaseCtx, func() error {
		_, execErr := d.db.ExecContext(releaseCtx, `
DELETE FROM assignment_side_effect_admissions WHERE bead_id=? AND owner_token=?`, admission.beadID, admission.token)
		if execErr != nil {
			return fmt.Errorf("delete assignment side-effect admission: %w", execErr)
		}
		return nil
	})
	if err != nil {
		_ = d.logEvent(releaseCtx, "assignment_side_effect_admission_release_failed", "dispatcher", admission.beadID, "", err.Error())
	}
}

func (d *Dispatcher) clearStaleAssignmentSideEffectAdmissions(ctx context.Context) error {
	if d == nil || d.db == nil {
		return nil
	}
	if _, err := d.db.ExecContext(ctx, `DELETE FROM assignment_side_effect_admissions`); err != nil {
		return fmt.Errorf("clear stale assignment side-effect admissions: %w", err)
	}
	return nil
}
