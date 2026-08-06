package dispatcher

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"

	"oro/pkg/protocol"
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

var nextReviewWorkerReleaseToken atomic.Uint64 //nolint:gochecknoglobals // process-wide monotonic tokens prevent cross-dispatcher lease ABA

type checkpointWorkerReleaseLease struct {
	d            *Dispatcher
	expected     *trackedWorker
	observedConn net.Conn
	token        uint64
	workerID     string
	beadID       string
	assignmentID int64
	state        protocol.WorkerState
	managed      bool
	spawnFor     bool
	procMgr      ProcessManager
	drain        <-chan struct{}
}

// releaseCheckpointOwnedWorker durably releases expected's checkpoint before
// removing the matching in-memory worker generation.
//
//nolint:unused // lifecycle callers are wired in follow-up slices
func (d *Dispatcher) releaseCheckpointOwnedWorker(
	ctx context.Context,
	expected *trackedWorker,
	cause ReviewReleaseCause,
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
	observedConn := tracked.conn
	d.mu.Unlock()
	return d.releaseCheckpointOwnedWorkerGeneration(
		ctx,
		expected,
		observedConn,
		cause,
	)
}

func (d *Dispatcher) releaseCheckpointOwnedWorkerGeneration(
	ctx context.Context,
	expected *trackedWorker,
	observedConn net.Conn,
	cause ReviewReleaseCause,
) (bool, error) {
	return d.releaseCheckpointOwnedWorkerGenerationUsing(
		ctx, expected, observedConn, cause,
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
	observedConn := tracked.conn
	d.mu.Unlock()
	return d.releaseCheckpointOwnedWorkerGenerationUsing(ctx, expected, observedConn, cause, releaseFn)
}

func (d *Dispatcher) releaseCheckpointOwnedWorkerGenerationUsing(
	ctx context.Context,
	expected *trackedWorker,
	observedConn net.Conn,
	cause ReviewReleaseCause,
	releaseFn checkpointWorkerReleaseFunc,
) (bool, error) {
	return d.releaseCheckpointOwnedWorkerGenerationWithActionUsing(
		ctx, expected, observedConn, cause, releaseFn, nil, nil,
	)
}

func (d *Dispatcher) releaseCheckpointOwnedWorkerGenerationWithActionUsing(
	ctx context.Context,
	expected *trackedWorker,
	observedConn net.Conn,
	cause ReviewReleaseCause,
	releaseFn checkpointWorkerReleaseFunc,
	action func(*checkpointWorkerReleaseLease) error,
	finalizeLocked func(*checkpointWorkerReleaseLease),
) (bool, error) {
	lease, ok := d.acquireCheckpointWorkerRelease(expected, observedConn)
	if !ok {
		return false, nil
	}
	return d.runCheckpointWorkerReleaseLease(ctx, lease, cause, releaseFn, action, finalizeLocked)
}

func (d *Dispatcher) runCheckpointWorkerReleaseLease(
	ctx context.Context,
	lease *checkpointWorkerReleaseLease,
	cause ReviewReleaseCause,
	releaseFn checkpointWorkerReleaseFunc,
	action func(*checkpointWorkerReleaseLease) error,
	finalizeLocked func(*checkpointWorkerReleaseLease),
) (bool, error) {
	ready, err := lease.waitForMessages(ctx)
	if err != nil || !ready {
		return false, err
	}

	durableCommitted := false
	durableFinalized := false
	finishDurable := func() {
		if durableFinalized {
			return
		}
		lease.finalizeDurable(finalizeLocked)
		_ = d.logEvent(ctx, "review_checkpoint_worker_released", "dispatcher", lease.beadID, lease.workerID,
			fmt.Sprintf(`{"cause":%q}`, cause))
		d.notifyAssignLoop()
		durableFinalized = true
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			if durableCommitted {
				finishDurable()
			} else {
				lease.abort()
			}
			panic(recovered)
		}
	}()

	released, err := releaseFn(ctx, lease.beadID, lease.workerID)
	if err != nil || !released {
		lease.abort()
		return released, err
	}
	durableCommitted = true

	var actionErr error
	if lease.current() && action != nil {
		actionErr = action(lease)
	}
	finishDurable()
	return true, actionErr
}

func (d *Dispatcher) acquireCheckpointWorkerRelease(
	expected *trackedWorker,
	observedConn net.Conn,
) (*checkpointWorkerReleaseLease, bool) {
	if expected == nil {
		return nil, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.acquireCheckpointWorkerReleaseLocked(expected, observedConn)
}

func (d *Dispatcher) acquireCheckpointWorkerReleaseLocked(
	expected *trackedWorker,
	observedConn net.Conn,
) (*checkpointWorkerReleaseLease, bool) {
	if expected == nil {
		return nil, false
	}
	tracked, ok := d.workers[expected.id]
	if !ok || tracked != expected || tracked.conn != observedConn ||
		tracked.state != protocol.WorkerReviewing || tracked.beadID == "" || tracked.reviewReleaseToken != 0 {
		return nil, false
	}
	token := nextReviewWorkerReleaseToken.Add(1)
	if token == 0 {
		token = nextReviewWorkerReleaseToken.Add(1)
	}
	tracked.reviewReleaseToken = token
	var drain <-chan struct{}
	if tracked.reviewMessagesInFlight > 0 {
		tracked.reviewMessagesDrained = make(chan struct{})
		tracked.reviewMessagesDrainToken = token
		drain = tracked.reviewMessagesDrained
	}
	return &checkpointWorkerReleaseLease{
		d:            d,
		expected:     expected,
		observedConn: observedConn,
		token:        token,
		workerID:     tracked.id,
		beadID:       tracked.beadID,
		assignmentID: tracked.assignmentID,
		state:        tracked.state,
		managed:      tracked.managed,
		spawnFor:     tracked.spawnFor,
		procMgr:      d.procMgr,
		drain:        drain,
	}, true
}

func (lease *checkpointWorkerReleaseLease) waitForMessages(ctx context.Context) (bool, error) {
	if lease.drain == nil {
		select {
		case <-ctx.Done():
			lease.abort()
			return false, fmt.Errorf("wait for review worker message drain: %w", ctx.Err())
		case <-lease.d.shutdownCh:
			lease.abort()
			return false, context.Canceled
		default:
		}
	} else {
		select {
		case <-lease.drain:
		case <-ctx.Done():
			lease.abort()
			return false, fmt.Errorf("wait for review worker message drain: %w", ctx.Err())
		case <-lease.d.shutdownCh:
			lease.abort()
			return false, context.Canceled
		}
	}
	if !lease.current() {
		lease.abort()
		return false, nil
	}
	return true, nil
}

func (lease *checkpointWorkerReleaseLease) current() bool {
	lease.d.mu.Lock()
	defer lease.d.mu.Unlock()
	tracked := lease.d.workers[lease.workerID]
	return tracked == lease.expected && tracked.reviewReleaseToken == lease.token && tracked.conn == lease.observedConn &&
		tracked.state == lease.state && tracked.state == protocol.WorkerReviewing && tracked.beadID == lease.beadID &&
		tracked.assignmentID == lease.assignmentID
}

func (lease *checkpointWorkerReleaseLease) abort() {
	lease.d.mu.Lock()
	defer lease.d.mu.Unlock()
	tracked := lease.d.workers[lease.workerID]
	if tracked == lease.expected && tracked.reviewReleaseToken == lease.token {
		tracked.reviewReleaseToken = 0
		if tracked.reviewMessagesDrainToken == lease.token {
			tracked.reviewMessagesDrained = nil
			tracked.reviewMessagesDrainToken = 0
		}
	}
}

func (lease *checkpointWorkerReleaseLease) finalizeDurable(finalizeLocked func(*checkpointWorkerReleaseLease)) {
	lease.d.mu.Lock()
	defer lease.d.mu.Unlock()
	tracked := lease.d.workers[lease.workerID]
	if tracked != lease.expected || tracked.reviewReleaseToken != lease.token {
		return
	}
	if tracked.conn != lease.observedConn {
		tracked.reviewReleaseToken = 0
		return
	}
	if finalizeLocked != nil {
		finalizeLocked(lease)
	}
	delete(lease.d.workers, lease.workerID)
}
