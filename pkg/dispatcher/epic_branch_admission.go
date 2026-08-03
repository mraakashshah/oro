package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"oro/pkg/protocol"
)

type epicBranchAdmissionWorktreeManager interface {
	inspectEpicBranch(ctx context.Context, branch, targetBranch string) (epicBranchInspection, error)
	compareAndSwapBranch(ctx context.Context, branch, oldOID, newOID string) error
}

const epicBranchAdmissionCleanupTimeout = 5 * time.Second

func (d *Dispatcher) withEpicBranchAdmission(
	ctx context.Context,
	bead protocol.Bead,
	workerID, branch, epicID, targetBranch string,
) (admitted bool) {
	defer func() {
		if admitted {
			return
		}
		cleanupCtx, cancelCleanup := epicBranchAdmissionCleanupContext(ctx)
		defer cancelCleanup()
		d.restoreEpicBranchAdmissionClaim(cleanupCtx, bead.ID, workerID)
	}()
	if epicID == "" || !strings.HasPrefix(branch, protocol.EpicBranchPrefix) {
		return d.ensureEpicBranchReady(ctx, bead, &trackedWorker{id: workerID}, branch, epicID)
	}
	store := newEpicBranchAdmissionStore(d.db)
	lease, acquired, err := store.acquire(ctx, branch, epicID, targetBranch, uuid.NewString(), workerID, d.nowFunc())
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		return d.rejectEpicBranchPreparation(ctx, bead.ID, workerID, branch, err)
	}
	if !acquired {
		return d.admitLinkedEpicBranchRecovery(ctx, bead.ID, workerID, lease)
	}

	operationCtx, cancelOperation := context.WithCancelCause(ctx)
	defer cancelOperation(nil)
	lease.operation = &epicBranchAdmissionOperation{cancel: cancelOperation}
	done := make(chan struct{})
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		d.renewEpicBranchAdmission(operationCtx, lease, done)
	}()
	prepared := d.prepareFreshEpicBranchAdmission(operationCtx, bead, workerID, lease)
	close(done)
	<-renewed
	releaseCtx, cancelRelease := epicBranchAdmissionCleanupContext(ctx)
	defer cancelRelease()
	releaseErr := store.release(releaseCtx, lease.branch, lease.leaseToken, lease.generation, d.nowFunc())
	if !prepared || operationCtx.Err() != nil {
		if releaseErr != nil && !errors.Is(releaseErr, ErrEpicBranchAdmissionCAS) {
			_ = d.logEvent(releaseCtx, "epic_branch_admission_release_failed", "dispatcher", bead.ID, workerID, releaseErr.Error())
		}
		return false
	}
	if releaseErr != nil {
		if errors.Is(releaseErr, ErrEpicBranchAdmissionCAS) {
			return false
		}
		return d.rejectEpicBranchPreparation(ctx, bead.ID, workerID, branch, releaseErr)
	}
	return true
}

func (d *Dispatcher) admitLinkedEpicBranchRecovery(
	ctx context.Context,
	beadID, workerID string,
	admission epicBranchAdmission,
) bool {
	if admission.state != "blocked" {
		return false
	}
	recovery, err := d.ensureEpicBranchBlockRecovery(ctx, admission)
	if err != nil {
		_ = d.logEvent(ctx, "epic_branch_recovery_ensure_failed", "dispatcher", admission.epicID, workerID, err.Error())
		return false
	}
	return recovery != nil && beadID == recovery.ID
}

func epicBranchAdmissionCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), epicBranchAdmissionCleanupTimeout)
}

func (d *Dispatcher) restoreEpicBranchAdmissionClaim(ctx context.Context, beadID, workerID string) {
	if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
		_ = d.logEvent(ctx, "epic_branch_admission_reopen_failed", "dispatcher", beadID, workerID, err.Error())
	}
	d.mu.Lock()
	delete(d.assigningBeads, beadID)
	d.mu.Unlock()
}

func (d *Dispatcher) renewEpicBranchAdmission(ctx context.Context, lease epicBranchAdmission, done <-chan struct{}) {
	interval := d.epicAdmissionRenewEvery
	if interval <= 0 {
		interval = epicBranchAdmissionLeaseRenewInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	store := newEpicBranchAdmissionStore(d.db)
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if err := store.renew(ctx, lease.branch, lease.leaseToken, lease.generation, d.nowFunc()); err != nil {
				_ = d.logEvent(ctx, "epic_branch_admission_renew_failed", "dispatcher", lease.epicID, lease.leaseOwner, err.Error())
				if lease.operation != nil {
					lease.operation.cancel(fmt.Errorf("renew epic branch admission: %w", err))
				}
				return
			}
		}
	}
}

func (d *Dispatcher) prepareFreshEpicBranchAdmission(ctx context.Context, bead protocol.Bead, workerID string, lease epicBranchAdmission) bool {
	exists, err := d.worktrees.BranchExists(ctx, lease.branch)
	if err != nil {
		return d.handleEpicBranchExistenceFailure(ctx, bead, workerID, lease, err)
	}
	if !exists {
		if !d.retainEpicBranchAdmissionOwnership(ctx, bead.ID, workerID, lease) {
			return false
		}
		return d.lazyCreateEpicBranchFrom(ctx, bead.ID, lease.branch, lease.targetBranch)
	}
	manager, ok := d.worktrees.(epicBranchAdmissionWorktreeManager)
	if !ok {
		return d.prepareLegacyEpicBranchAdmission(ctx, bead.ID, workerID, lease)
	}
	inspection, err := manager.inspectEpicBranch(ctx, lease.branch, lease.targetBranch)
	if err != nil {
		return d.rejectFreshEpicBranchPreparation(ctx, bead.ID, workerID, lease.branch, err)
	}
	if ctx.Err() != nil {
		return false
	}
	if len(inspection.CheckedOutPaths) != 0 {
		return d.blockEpicBranchAdmission(ctx, bead.ID, lease, "checked_out", inspection.CheckedOutPaths[0], inspection,
			fmt.Sprintf("epic branch is checked out in: %s", strings.Join(inspection.CheckedOutPaths, ", ")))
	}
	switch inspection.Relation {
	case branchSame, branchContainsBase:
		return true
	case branchStrictlyBehind:
		if !d.retainEpicBranchAdmissionOwnership(ctx, bead.ID, workerID, lease) {
			return false
		}
		if err := manager.compareAndSwapBranch(ctx, lease.branch, inspection.BranchOID, inspection.BaseOID); err != nil {
			var checkedOutErr *epicBranchCheckedOutError
			if errors.As(err, &checkedOutErr) && len(checkedOutErr.CheckedOutPaths) != 0 {
				return d.blockEpicBranchAdmission(ctx, bead.ID, lease, "checked_out", checkedOutErr.CheckedOutPaths[0], inspection, err.Error())
			}
			return d.rejectFreshEpicBranchPreparation(ctx, bead.ID, workerID, lease.branch, err)
		}
		return true
	case branchDiverged:
		return d.blockEpicBranchAdmission(ctx, bead.ID, lease, "diverged", "", inspection,
			fmt.Sprintf("epic branch %s diverged from %s", lease.branch, lease.targetBranch))
	default:
		return d.rejectFreshEpicBranchPreparation(ctx, bead.ID, workerID, lease.branch,
			fmt.Errorf("unknown epic branch relation %d", inspection.Relation))
	}
}

func (d *Dispatcher) prepareLegacyEpicBranchAdmission(
	ctx context.Context,
	beadID, workerID string,
	lease epicBranchAdmission,
) bool {
	if !d.retainEpicBranchAdmissionOwnership(ctx, beadID, workerID, lease) {
		return false
	}
	return d.prepareEpicBranchForAssignment(ctx, beadID, workerID, lease.branch)
}

func (d *Dispatcher) handleEpicBranchExistenceFailure(
	ctx context.Context,
	bead protocol.Bead,
	workerID string,
	lease epicBranchAdmission,
	err error,
) bool {
	if ctx.Err() != nil {
		return false
	}
	d.handleEpicBranchMissing(ctx, bead, &trackedWorker{id: workerID}, lease.branch, lease.epicID, err)
	return false
}

func (d *Dispatcher) retainEpicBranchAdmissionOwnership(
	ctx context.Context,
	beadID, workerID string,
	lease epicBranchAdmission,
) bool {
	err := newEpicBranchAdmissionStore(d.db).renew(
		ctx, lease.branch, lease.leaseToken, lease.generation, d.nowFunc(),
	)
	if err == nil {
		return true
	}
	if ctx.Err() != nil || errors.Is(err, ErrEpicBranchAdmissionCAS) {
		return false
	}
	return d.rejectFreshEpicBranchPreparation(ctx, beadID, workerID, lease.branch, err)
}

func (d *Dispatcher) rejectFreshEpicBranchPreparation(ctx context.Context, beadID, workerID, branch string, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	return d.rejectEpicBranchPreparation(ctx, beadID, workerID, branch, err)
}

func (d *Dispatcher) blockEpicBranchAdmission(
	ctx context.Context,
	beadID string,
	lease epicBranchAdmission,
	blockerKind, checkoutPath string,
	inspection epicBranchInspection,
	details string,
) bool {
	store := newEpicBranchAdmissionStore(d.db)
	admission, err := store.block(ctx, lease.branch, lease.leaseToken, lease.generation, blockerKind, checkoutPath,
		inspection.BranchOID, inspection.BaseOID, "", details, d.nowFunc())
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, ErrEpicBranchAdmissionCAS) {
			return false
		}
		_ = d.logEvent(ctx, "epic_branch_admission_block_failed", "dispatcher", lease.epicID, lease.leaseOwner, err.Error())
		return d.rejectEpicBranchPreparation(ctx, beadID, lease.leaseOwner, lease.branch, err)
	}
	if _, err := d.ensureEpicBranchBlockRecovery(ctx, admission); err != nil {
		_ = d.logEvent(ctx, "epic_branch_recovery_ensure_failed", "dispatcher", lease.epicID, lease.leaseOwner, err.Error())
	}
	return false
}
