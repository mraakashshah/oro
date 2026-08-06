package dispatcher

import (
	"context"
	"fmt"
)

// ReviewReleaseCause identifies why a checkpoint-owned worker was released.
type ReviewReleaseCause string

// ReviewReleaseCause values describe checkpoint worker lifecycle exits.
const (
	ReviewReleaseCauseConnectionLost ReviewReleaseCause = "connection_lost"
	ReviewReleaseCauseSendFailed     ReviewReleaseCause = "send_failed"
	ReviewReleaseCauseKilled         ReviewReleaseCause = "killed"
	ReviewReleaseCauseRestarted      ReviewReleaseCause = "restarted"
)

type checkpointWorkerReleaseFunc func(context.Context, string, string) (bool, error)

// releaseCheckpointOwnedWorker durably releases expected's checkpoint before
// removing the matching in-memory worker generation.
//
//nolint:unused // lifecycle callers are wired in follow-up slices
func (d *Dispatcher) releaseCheckpointOwnedWorker(
	ctx context.Context,
	expected *trackedWorker,
	cause ReviewReleaseCause,
) (bool, error) {
	return d.releaseCheckpointOwnedWorkerUsing(
		ctx,
		expected,
		cause,
		NewReviewCheckpointStore(d.db).ReleaseWorker,
	)
}

func (d *Dispatcher) releaseCheckpointOwnedWorkerUsing(
	ctx context.Context,
	expected *trackedWorker,
	cause ReviewReleaseCause,
	releaseFn checkpointWorkerReleaseFunc,
) (bool, error) {
	if expected == nil {
		return false, nil
	}

	d.mu.Lock()
	tracked, ok := d.workers[expected.id]
	if !ok || tracked != expected {
		d.mu.Unlock()
		return false, nil
	}
	workerID := expected.id
	beadID := expected.beadID
	workerConn := expected.conn
	d.mu.Unlock()

	released, err := releaseFn(ctx, beadID, workerID)
	if err != nil || !released {
		return released, err
	}

	d.mu.Lock()
	tracked, ok = d.workers[workerID]
	if ok && tracked == expected && tracked.conn == workerConn {
		delete(d.workers, workerID)
	}
	d.mu.Unlock()

	_ = d.logEvent(ctx, "review_checkpoint_worker_released", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"cause":%q}`, cause))
	d.notifyAssignLoop()
	return true, nil
}
