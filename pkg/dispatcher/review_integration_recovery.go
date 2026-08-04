package dispatcher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"oro/pkg/merge"
)

const (
	integrationStepIntent              = "intent"
	integrationStepMergeObserved       = "merge_observed"
	integrationStepAssignmentCompleted = "assignment_completed"
	integrationStepBeadClosed          = "bead_closed"
	integrationStepIntegrated          = "integrated"
)

// reconcileReviewIntegrationsOnStartup runs before ops reconciliation. It
// converts durable git proof into resumable finalization, and never infers a
// successful merge from process death or an assignment state alone.
func (d *Dispatcher) reconcileReviewIntegrationsOnStartup(ctx context.Context) error {
	store := NewReviewCheckpointStore(d.db)
	checkpoints, err := store.ListPendingIntegrations(ctx)
	if err != nil {
		return err
	}
	for i := range checkpoints {
		if err := d.reconcileReviewIntegration(ctx, store, &checkpoints[i]); err != nil {
			return fmt.Errorf("reconcile review integration checkpoint %d: %w", checkpoints[i].ID, err)
		}
	}
	return nil
}

func (d *Dispatcher) reconcileReviewIntegration(
	ctx context.Context,
	store *ReviewCheckpointStore,
	checkpoint *ReviewIntegrationCheckpoint,
) error {
	currentTargetSHA, err := d.reviewIntegrationTargetSHA(ctx, checkpoint.TargetBranch)
	if err != nil {
		return d.blockReviewIntegration(ctx, store, checkpoint,
			fmt.Sprintf("cannot observe integration target %s: %v", checkpoint.TargetBranch, err))
	}
	approvedHeadSHA := checkpoint.IntegrationApprovedHeadSHA
	if approvedHeadSHA == "" {
		approvedHeadSHA = checkpoint.HeadSHA
	}

	if checkpoint.State == ReviewCheckpointStateApproved {
		if err := d.prepareApprovedReviewIntegration(ctx, store, checkpoint, currentTargetSHA, approvedHeadSHA); err != nil {
			return err
		}
	}

	if checkpoint.IntegrationTargetBeforeSHA == "" || approvedHeadSHA == "" {
		return d.blockReviewIntegration(ctx, store, checkpoint, "integration intent is missing exact target or approved-head identity")
	}

	switch checkpoint.State {
	case ReviewCheckpointStateManualIntegrationPending:
		return d.reconcileManualReviewIntegration(ctx, store, checkpoint, currentTargetSHA, approvedHeadSHA)
	case ReviewCheckpointStateIntegrating:
		return d.reconcileAutomaticReviewIntegration(ctx, store, checkpoint, currentTargetSHA, approvedHeadSHA)
	default:
		return nil
	}
}

func (d *Dispatcher) prepareApprovedReviewIntegration(
	ctx context.Context,
	store *ReviewCheckpointStore,
	checkpoint *ReviewIntegrationCheckpoint,
	currentTargetSHA, approvedHeadSHA string,
) error {
	alreadyMerged, proofErr := d.reviewIntegrationProof(ctx, checkpoint.TargetSHA, approvedHeadSHA, currentTargetSHA)
	if proofErr != nil {
		return d.blockReviewIntegration(ctx, store, checkpoint,
			fmt.Sprintf("cannot prove approved head against integration target: %v", proofErr))
	}
	if !alreadyMerged && currentTargetSHA != checkpoint.TargetSHA {
		return d.blockReviewIntegration(ctx, store, checkpoint, "approved target moved before integration intent")
	}
	if !alreadyMerged {
		if err := d.verifyApprovedIntegrationSource(ctx, checkpoint, approvedHeadSHA); err != nil {
			return d.blockReviewIntegration(ctx, store, checkpoint, fmt.Sprintf("approved source moved before integration intent: %v", err))
		}
	}
	nextState := ReviewCheckpointStateIntegrating
	if d.cfg.ManualIntegration {
		nextState = ReviewCheckpointStateManualIntegrationPending
	}
	if err := store.BeginIntegration(ctx, checkpoint.ID, checkpoint.State, nextState, checkpoint.TargetSHA, approvedHeadSHA); err != nil {
		return err
	}
	checkpoint.State = nextState
	checkpoint.IntegrationTargetBeforeSHA = checkpoint.TargetSHA
	checkpoint.IntegrationApprovedHeadSHA = approvedHeadSHA
	checkpoint.IntegrationStep = integrationStepIntent
	return nil
}

func (d *Dispatcher) reconcileManualReviewIntegration(
	ctx context.Context,
	store *ReviewCheckpointStore,
	checkpoint *ReviewIntegrationCheckpoint,
	currentTargetSHA, approvedHeadSHA string,
) error {
	if currentTargetSHA == checkpoint.IntegrationTargetBeforeSHA {
		return nil
	}
	proven, err := d.reviewIntegrationProof(ctx, checkpoint.IntegrationTargetBeforeSHA, approvedHeadSHA, currentTargetSHA)
	if err != nil {
		return d.blockReviewIntegration(ctx, store, checkpoint, fmt.Sprintf("cannot observe manual integration proof: %v", err))
	}
	if !proven {
		return d.blockReviewIntegration(ctx, store, checkpoint, "target moved without integration proof")
	}
	if err := store.PromoteManualIntegration(ctx, checkpoint.ID, currentTargetSHA); err != nil {
		return err
	}
	checkpoint.State = ReviewCheckpointStateIntegrating
	checkpoint.IntegrationObservedTargetSHA = currentTargetSHA
	checkpoint.IntegrationStep = integrationStepMergeObserved
	return d.finalizeReviewIntegration(ctx, store, checkpoint)
}

func (d *Dispatcher) reconcileAutomaticReviewIntegration(
	ctx context.Context,
	store *ReviewCheckpointStore,
	checkpoint *ReviewIntegrationCheckpoint,
	currentTargetSHA, approvedHeadSHA string,
) error {
	if checkpoint.IntegrationObservedTargetSHA != "" {
		if currentTargetSHA != checkpoint.IntegrationObservedTargetSHA {
			return d.blockReviewIntegration(ctx, store, checkpoint, "target moved after recorded integration proof")
		}
		return d.finalizeReviewIntegration(ctx, store, checkpoint)
	}

	if currentTargetSHA == checkpoint.IntegrationTargetBeforeSHA {
		if err := d.retryReviewIntegrationMerge(ctx, checkpoint); err != nil {
			return d.blockReviewIntegration(ctx, store, checkpoint, fmt.Sprintf("integration retry failed: %v", err))
		}
		var err error
		currentTargetSHA, err = d.reviewIntegrationTargetSHA(ctx, checkpoint.TargetBranch)
		if err != nil {
			return d.blockReviewIntegration(ctx, store, checkpoint,
				fmt.Sprintf("cannot observe target after integration retry: %v", err))
		}
	}

	proven, err := d.reviewIntegrationProof(ctx, checkpoint.IntegrationTargetBeforeSHA, approvedHeadSHA, currentTargetSHA)
	if err != nil {
		return d.blockReviewIntegration(ctx, store, checkpoint, fmt.Sprintf("cannot observe integration proof: %v", err))
	}
	if !proven {
		return d.blockReviewIntegration(ctx, store, checkpoint, "target moved without integration proof")
	}
	if err := store.ObserveIntegration(ctx, checkpoint.ID, currentTargetSHA); err != nil {
		return err
	}
	checkpoint.IntegrationObservedTargetSHA = currentTargetSHA
	checkpoint.IntegrationStep = integrationStepMergeObserved
	return d.finalizeReviewIntegration(ctx, store, checkpoint)
}

func (d *Dispatcher) retryReviewIntegrationMerge(ctx context.Context, checkpoint *ReviewIntegrationCheckpoint) error {
	if d.merger == nil {
		return errors.New("merge coordinator is unavailable")
	}
	currentTargetSHA, err := d.reviewIntegrationTargetSHA(ctx, checkpoint.TargetBranch)
	if err != nil {
		return err
	}
	if currentTargetSHA != checkpoint.IntegrationTargetBeforeSHA {
		return fmt.Errorf("approved target moved from %s to %s before merge", checkpoint.IntegrationTargetBeforeSHA, currentTargetSHA)
	}
	if err := d.verifyApprovedIntegrationSource(ctx, checkpoint, checkpoint.IntegrationApprovedHeadSHA); err != nil {
		return fmt.Errorf("approved source moved before merge: %w", err)
	}
	_, err = d.merger.Merge(ctx, merge.Opts{
		Branch:            checkpoint.Branch,
		Worktree:          checkpoint.Worktree,
		BeadID:            checkpoint.BeadID,
		TargetBranch:      checkpoint.TargetBranch,
		ExpectedSourceSHA: checkpoint.IntegrationApprovedHeadSHA,
		ExpectedTargetSHA: checkpoint.IntegrationTargetBeforeSHA,
	})
	if err != nil {
		return fmt.Errorf("retry durable review integration merge: %w", err)
	}
	return nil
}

func (d *Dispatcher) verifyApprovedIntegrationSource(
	ctx context.Context,
	checkpoint *ReviewIntegrationCheckpoint,
	approvedHeadSHA string,
) error {
	branchSHA, err := d.reviewIntegrationRefSHA(ctx, d.repoRoot, checkpoint.Branch+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve approved branch %s: %w", checkpoint.Branch, err)
	}
	if branchSHA != approvedHeadSHA {
		return fmt.Errorf("branch %s is %s, approved %s", checkpoint.Branch, branchSHA, approvedHeadSHA)
	}
	worktreeSHA, err := d.reviewIntegrationRefSHA(ctx, checkpoint.Worktree, "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("resolve approved worktree HEAD: %w", err)
	}
	if worktreeSHA != approvedHeadSHA {
		return fmt.Errorf("worktree HEAD is %s, approved %s", worktreeSHA, approvedHeadSHA)
	}
	return nil
}

func (d *Dispatcher) reviewIntegrationRefSHA(ctx context.Context, workdir, ref string) (string, error) {
	if workdir == "" || ref == "" {
		return "", errors.New("missing workdir or ref")
	}
	out, err := d.commandRunner().Run(ctx, "git", "-C", workdir, "rev-parse", ref)
	if err != nil {
		return "", fmt.Errorf("resolve %s in %s: %w", ref, workdir, err)
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("resolve %s in %s: empty object ID", ref, workdir)
	}
	return sha, nil
}

func (d *Dispatcher) finalizeReviewIntegration(
	ctx context.Context,
	store *ReviewCheckpointStore,
	checkpoint *ReviewIntegrationCheckpoint,
) error {
	assignmentID := checkpoint.CurrentAssignmentID
	if assignmentID == 0 {
		assignmentID = checkpoint.OriginAssignmentID
	}
	switch checkpoint.IntegrationStep {
	case integrationStepIntent:
		return errors.New("cannot finalize integration without observed merge proof")
	case integrationStepMergeObserved, "":
		if err := d.completeCheckpointAssignment(ctx, assignmentID, checkpoint.BeadID); err != nil {
			return err
		}
		if err := store.AdvanceIntegrationStep(ctx, checkpoint.ID, integrationStepAssignmentCompleted); err != nil {
			return err
		}
		checkpoint.IntegrationStep = integrationStepAssignmentCompleted
		fallthrough
	case integrationStepAssignmentCompleted:
		if err := d.closeIntegratedBeadOnce(ctx, checkpoint.BeadID,
			checkpoint.IntegrationObservedTargetSHA); err != nil {
			return fmt.Errorf("close integrated bead %s: %w", checkpoint.BeadID, err)
		}
		if err := store.AdvanceIntegrationStep(ctx, checkpoint.ID, integrationStepBeadClosed); err != nil {
			return err
		}
		checkpoint.IntegrationStep = integrationStepBeadClosed
		fallthrough
	case integrationStepBeadClosed:
		if err := store.CompleteIntegration(ctx, checkpoint.ID); err != nil {
			return err
		}
		checkpoint.State = ReviewCheckpointStateIntegrated
		checkpoint.IntegrationStep = integrationStepIntegrated
		_ = d.logEvent(ctx, "review_integration_reconciled", "dispatcher", checkpoint.BeadID,
			checkpoint.WorkerID, fmt.Sprintf(`{"checkpoint_id":%d,"target":%q,"observed_sha":%q}`,
				checkpoint.ID, checkpoint.TargetBranch, checkpoint.IntegrationObservedTargetSHA))
		return nil
	case integrationStepIntegrated:
		return nil
	default:
		return d.blockReviewIntegration(ctx, store, checkpoint,
			fmt.Sprintf("unknown durable integration step %q", checkpoint.IntegrationStep))
	}
}

// closeIntegratedBeadOnce makes the checkpoint step following CloseBead
// restart-safe. A process can die after the native store and all close side
// effects commit but before integration_step advances to bead_closed. In that
// case the durable closed status and exact merge reason prove the operation
// already completed, so replay must not append another bead_closed event.
func (d *Dispatcher) closeIntegratedBeadOnce(ctx context.Context, beadID, observedTargetSHA string) error {
	if beadID == "" || observedTargetSHA == "" {
		return errors.New("missing integrated bead or observed target identity")
	}
	expectedReason := fmt.Sprintf("Merged: %s", observedTargetSHA)
	bead, err := d.beads.Show(ctx, beadID)
	if err != nil {
		return fmt.Errorf("observe integrated bead before close: %w", err)
	}
	if bead == nil {
		return fmt.Errorf("observe integrated bead before close: bead %s not found", beadID)
	}
	if bead.Status == "closed" {
		if bead.CloseReason != expectedReason {
			return fmt.Errorf("bead is already closed with reason %q, want %q", bead.CloseReason, expectedReason)
		}
		if err := PromoteChildrenOnParentClose(ctx, d.beads, beadID); err != nil {
			d.warnSweepFailure(ctx, beadID, err)
		}
		if err := d.runLearningPromotion(ctx, beadID, promotionVerdictFromCloseReason(expectedReason)); err != nil {
			return fmt.Errorf("resume learning promotion for %s: %w", beadID, err)
		}
		return nil
	}
	return d.CloseBead(ctx, beadID, expectedReason)
}

func (d *Dispatcher) completeCheckpointAssignment(ctx context.Context, assignmentID int64, beadID string) error {
	if assignmentID <= 0 || beadID == "" {
		return errors.New("complete checkpoint assignment: missing exact assignment identity")
	}
	result, err := d.db.ExecContext(ctx, `
UPDATE assignments
SET status='completed', completed_at=COALESCE(completed_at, datetime('now'))
WHERE id=? AND bead_id=? AND status IN ('active', 'requeued')`, assignmentID, beadID)
	if err != nil {
		return fmt.Errorf("complete checkpoint assignment: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count completed checkpoint assignment: %w", err)
	}
	if rows == 1 {
		return nil
	}
	var currentBead, status string
	if err := d.db.QueryRowContext(ctx, `SELECT bead_id, status FROM assignments WHERE id=?`, assignmentID).
		Scan(&currentBead, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("complete checkpoint assignment: assignment %d not found", assignmentID)
		}
		return fmt.Errorf("load checkpoint assignment: %w", err)
	}
	if currentBead == beadID && status == "completed" {
		return nil
	}
	return fmt.Errorf("complete checkpoint assignment: assignment %d owns bead %q in status %q", assignmentID, currentBead, status)
}

func (d *Dispatcher) reviewIntegrationTargetSHA(ctx context.Context, targetBranch string) (string, error) {
	if d.repoRoot == "" || targetBranch == "" {
		return "", errors.New("missing repository or target branch")
	}
	out, err := d.commandRunner().Run(ctx, "git", "-C", d.repoRoot, "rev-parse", targetBranch+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve integration target %s: %w", targetBranch, err)
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", errors.New("target branch resolved to an empty object ID")
	}
	return sha, nil
}

func (d *Dispatcher) reviewIntegrationProof(ctx context.Context, targetBeforeSHA, approvedHeadSHA, currentTargetSHA string) (bool, error) {
	approvedMerged, err := d.reviewIntegrationAncestor(ctx, approvedHeadSHA, currentTargetSHA)
	if err != nil || !approvedMerged {
		return false, err
	}
	targetAdvanced, err := d.reviewIntegrationAncestor(ctx, targetBeforeSHA, currentTargetSHA)
	if err != nil || !targetAdvanced {
		return false, err
	}
	return true, nil
}

func (d *Dispatcher) reviewIntegrationAncestor(ctx context.Context, olderSHA, newerSHA string) (bool, error) {
	if olderSHA == "" || newerSHA == "" {
		return false, errors.New("missing ancestry identity")
	}
	_, err := d.commandRunner().Run(ctx, "git", "-C", d.repoRoot, "merge-base", "--is-ancestor", olderSHA, newerSHA)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("prove integration ancestry %s..%s: %w", olderSHA, newerSHA, err)
}

func (d *Dispatcher) blockReviewIntegration(
	ctx context.Context,
	store *ReviewCheckpointStore,
	checkpoint *ReviewIntegrationCheckpoint,
	reason string,
) error {
	changed, err := store.BlockIntegration(ctx, checkpoint.ID, reason)
	if err != nil {
		return err
	}
	if changed {
		checkpoint.State = ReviewCheckpointStateBlocked
		checkpoint.IntegrationStep = "blocked"
		_ = d.logEvent(ctx, "review_integration_blocked", "dispatcher", checkpoint.BeadID,
			checkpoint.WorkerID, fmt.Sprintf(`{"checkpoint_id":%d,"reason":%q}`, checkpoint.ID, reason))
	}
	return nil
}
