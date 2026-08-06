package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"oro/pkg/protocol"
)

// applyKillWorker terminates a specific worker, cleans up its worktree,
// resets its bead to open, and clears bead tracking. Decrements targetWorkers
// only for managed workers. Returns an error if args is empty or the worker
// ID is not found.
func (d *Dispatcher) applyKillWorker(args string) (string, error) {
	if args == "" {
		return "", fmt.Errorf("worker ID required")
	}

	workerID := args
	ctx := context.Background()

	d.mu.Lock()
	w, ok := d.workers[workerID]
	if !ok {
		d.mu.Unlock()
		return "", fmt.Errorf("worker not found")
	}
	if w.state == protocol.WorkerReviewing && w.beadID != "" {
		d.mu.Unlock()
		return d.killCheckpointOwnedWorker(ctx, w)
	}

	// Capture fields before removing worker.
	beadID := w.beadID
	assignmentID := w.assignmentID
	managed := w.managed
	spawnFor := w.spawnFor

	if spawnFor {
		sendShutdownWithoutBuffering(w)
		if current := d.workers[workerID]; current == w {
			w.markShuttingDownWithoutAssignment()
		}
	} else {
		// Tell the worker process to exit before removing dispatcher bookkeeping.
		// Closing the connection alone makes `oro worker` treat it as a transient
		// connection drop and reconnect while the dispatcher is still alive.
		sendShutdownWithoutBuffering(w)
		delete(d.workers, workerID)
	}

	// Decrement target count only for managed workers; external workers are
	// not counted against targetWorkers. Spawn-for workers are one-shot
	// managed processes and are also outside targetWorkers.
	if managed && !spawnFor && d.targetWorkers > 0 {
		d.targetWorkers--
	}
	d.mu.Unlock()

	// DO NOT remove the worktree here - preserve it for respawn reuse (oro-1eo8).
	// The worktree will be reused if the same bead is reassigned, or cleaned up
	// on successful completion or explicit shutdown.

	// Reset bead to open so it can be reassigned.
	if beadID != "" {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "kill_worker_bead_reset_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
		d.clearBeadTracking(beadID)
		_ = d.completeAssignment(ctx, assignmentID, beadID)
		_ = d.logEvent(ctx, "worker_killed", "dispatcher", beadID, workerID,
			`{"reason":"kill-worker directive"}`)
	}

	return fmt.Sprintf("worker %s killed", workerID), nil
}

func (d *Dispatcher) killCheckpointOwnedWorker(ctx context.Context, expected *trackedWorker) (string, error) {
	released, err := d.releaseCheckpointOwnedWorker(ctx, expected, ReviewReleaseCauseKilled)
	if err != nil {
		return "", fmt.Errorf("release review checkpoint worker for kill: %w", err)
	}
	if !released {
		return "", fmt.Errorf("release review checkpoint worker for kill: ownership changed")
	}

	d.mu.Lock()
	_, replacementPresent := d.workers[expected.id]
	if replacementPresent {
		d.mu.Unlock()
		return fmt.Sprintf("worker %s killed", expected.id), nil
	}
	if expected.managed && !expected.spawnFor && d.targetWorkers > 0 {
		d.targetWorkers--
	}
	d.mu.Unlock()

	// The durable release owns worker removal, event emission, and assignment-loop
	// notification. Only process shutdown and capacity bookkeeping remains here.
	sendShutdownWithoutBuffering(expected)
	return fmt.Sprintf("worker %s killed", expected.id), nil
}

// applySpawnFor spawns a dedicated worker for a specific bead. The bead is
// added to priorityBeads so tryAssign assigns it before normal queue ordering.
func (d *Dispatcher) applySpawnFor(args string) (string, error) {
	if args == "" {
		return "", fmt.Errorf("bead ID required")
	}
	beadID := args

	newID := fmt.Sprintf("worker-spawnfor-%d", d.nowFunc().UnixNano())
	d.mu.Lock()
	for _, w := range d.workers {
		if w.beadID == beadID {
			workerID := w.id
			d.mu.Unlock()
			return "", fmt.Errorf("bead %s already assigned to %s", beadID, workerID)
		}
	}
	if d.procMgr == nil {
		d.mu.Unlock()
		return "", fmt.Errorf("no process manager configured")
	}
	totalWorkers := d.liveWorkerCountLocked()
	if d.cfg.MaxWorkers > 0 && totalWorkers >= d.cfg.MaxWorkers {
		maxWorkers := d.cfg.MaxWorkers
		d.mu.Unlock()
		return "", fmt.Errorf("max workers reached: total=%d MaxWorkers=%d", totalWorkers, maxWorkers)
	}
	procMgr := d.procMgr
	d.priorityBeads[beadID] = true
	d.pendingManagedIDs[newID] = true
	d.pendingManagedSince[newID] = d.nowFunc()
	d.pendingWorkerTargets[newID] = beadID
	d.pendingSpawnForWorkers[newID] = true
	d.mu.Unlock()

	if _, err := procMgr.Spawn(newID); err != nil {
		d.mu.Lock()
		delete(d.priorityBeads, beadID)
		delete(d.pendingManagedIDs, newID)
		delete(d.pendingManagedSince, newID)
		delete(d.pendingWorkerTargets, newID)
		delete(d.pendingSpawnForWorkers, newID)
		d.mu.Unlock()
		return "", fmt.Errorf("spawn failed: %w", err)
	}

	_ = d.logEvent(context.Background(), "spawn_for", "dispatcher", beadID, newID, "")
	return fmt.Sprintf("spawned worker %s for bead %s", newID, beadID), nil
}

func (d *Dispatcher) parseWorkerLaunchReservation(args string) (workerLaunchReservation, error) {
	var req workerLaunchReservation
	if err := json.Unmarshal([]byte(args), &req); err != nil {
		return req, fmt.Errorf("invalid worker launch args: %w", err)
	}
	if len(req.WorkerIDs) == 0 {
		return req, fmt.Errorf("worker IDs required")
	}
	seen := make(map[string]bool, len(req.WorkerIDs))
	for _, id := range req.WorkerIDs {
		if strings.TrimSpace(id) == "" {
			return req, fmt.Errorf("worker ID required")
		}
		if seen[id] {
			return req, fmt.Errorf("duplicate worker ID %q", id)
		}
		seen[id] = true
	}
	return req, nil
}

func (d *Dispatcher) applyLaunchWorkers(args string) (string, error) {
	req, err := d.parseWorkerLaunchReservation(args)
	if err != nil {
		return "", err
	}

	d.mu.Lock()
	d.cleanupStalePendingManagedLocked(d.nowFunc())
	for _, id := range req.WorkerIDs {
		if _, exists := d.workers[id]; exists {
			d.mu.Unlock()
			return "", fmt.Errorf("worker %s already connected", id)
		}
		if d.pendingManagedIDs[id] || d.pendingExternalIDs[id] {
			d.mu.Unlock()
			return "", fmt.Errorf("worker %s already pending", id)
		}
	}
	totalWorkers := d.liveWorkerCountLocked()
	if d.cfg.MaxWorkers > 0 {
		available := d.cfg.MaxWorkers - totalWorkers
		if len(req.WorkerIDs) > available {
			maxWorkers := d.cfg.MaxWorkers
			d.mu.Unlock()
			return "", fmt.Errorf("max workers reached: requested=%d available=%d total=%d MaxWorkers=%d",
				len(req.WorkerIDs), available, totalWorkers, maxWorkers)
		}
	}
	now := d.nowFunc()
	for _, id := range req.WorkerIDs {
		d.pendingExternalIDs[id] = true
		d.pendingExternalSince[id] = now
	}
	d.mu.Unlock()

	return fmt.Sprintf("reserved %d workers", len(req.WorkerIDs)), nil
}

func (d *Dispatcher) applyCancelWorkerLaunch(args string) (string, error) {
	req, err := d.parseWorkerLaunchReservation(args)
	if err != nil {
		return "", err
	}

	d.mu.Lock()
	cancelled := 0
	for _, id := range req.WorkerIDs {
		if d.pendingExternalIDs[id] {
			delete(d.pendingExternalIDs, id)
			delete(d.pendingExternalSince, id)
			cancelled++
		}
	}
	d.mu.Unlock()

	return fmt.Sprintf("cancelled %d worker reservations", cancelled), nil
}

// applyRestartWorker terminates a specific worker, returns its bead to the
// ready queue, spawns a new worker with the same ID, and keeps targetWorkers
// unchanged. Returns an error if args is empty, the worker ID is not found,
// or spawning the new worker fails.
// restartWorkerPreservingQGRetry restarts a worker that still owes a QG retry: the
// persisted feedback is restored as a pending handoff before the process is replaced
// so the replacement receives the exact failure. On restore failure the managed-ID
// bookkeeping is rolled back, otherwise registerWorker would mark an unrelated
// future connection as managed. Extracted from applyRestartWorker for gocognit and
// nestif; behaviour is unchanged.
func (d *Dispatcher) restartWorkerPreservingQGRetry(ctx context.Context, workerID string, st restartWorkerState) (string, error) {
	if err := d.restoreQGRetryHandoff(ctx, workerID, st.beadID, st.assignmentID, st.retryContext, st.retrySnapshot); err != nil {
		d.forgetPendingManagedWorker(workerID)
		return "", fmt.Errorf("restore qg retry feedback: %w", err)
	}
	if err := d.killManagedWorkerForRestart(ctx, st.procMgr, workerID, st.beadID, st.wasManaged); err != nil {
		return "", err
	}
	if st.procMgr != nil {
		if _, err := st.procMgr.Spawn(workerID); err != nil {
			return "", fmt.Errorf("spawn new worker: %w", err)
		}
	}
	_ = d.logEvent(ctx, "qg_retry_worker_restarted", "dispatcher", st.beadID, workerID,
		fmt.Sprintf(`{"attempt":%d,"occurrence_id":%q}`, st.retryContext.Attempt, st.retryContext.OccurrenceID))
	return fmt.Sprintf("worker %s restarted", workerID), nil
}

// forgetPendingManagedWorker drops the managed-respawn bookkeeping for workerID.
func (d *Dispatcher) forgetPendingManagedWorker(workerID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.pendingManagedIDs, workerID)
	delete(d.pendingManagedSince, workerID)
}

// restartWorkerState is the snapshot applyRestartWorker takes under d.mu before
// it respawns the worker.
type restartWorkerState struct {
	beadID        string
	assignmentID  int64
	wasManaged    bool
	retryContext  QGRetryContext
	retryPending  bool
	retrySnapshot workerAssignmentSnapshot
	procMgr       ProcessManager
}

// takeRestartWorkerState performs applyRestartWorker's locked phase: snapshot the
// assignment, close the connection, drop the worker row, and remember a managed ID
// so registerWorker re-marks the respawned process as managed. Extracted to keep
// applyRestartWorker under the gocognit limit; behaviour is unchanged.
func (d *Dispatcher) takeRestartWorkerState(workerID string) (restartWorkerState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	w, ok := d.workers[workerID]
	if !ok {
		return restartWorkerState{}, fmt.Errorf("worker not found")
	}

	st := restartWorkerState{
		beadID:       w.beadID,
		assignmentID: w.assignmentID,
		wasManaged:   w.managed,
		retrySnapshot: workerAssignmentSnapshot{
			execution:     w.execution,
			worktree:      w.worktree,
			runtime:       w.runtime,
			model:         w.model,
			reasoning:     w.reasoning,
			epicID:        w.epicID,
			baseBranch:    w.baseBranch,
			targetBranch:  w.targetBranch,
			qgEvidenceDir: w.qgEvidenceDir,
			targetSHA:     w.targetSHA,
		},
		procMgr: d.procMgr,
	}
	st.retryContext, st.retryPending = d.pendingQGRetries[workerID]

	_ = w.conn.Close()
	delete(d.workers, workerID)

	// If the original worker was managed, record the ID so registerWorker
	// sets managed=true when the respawned process connects.
	if st.wasManaged {
		d.pendingManagedIDs[workerID] = true
		d.pendingManagedSince[workerID] = d.nowFunc()
	}
	// Target count remains unchanged (unlike kill-worker).
	return st, nil
}

func (d *Dispatcher) applyRestartWorker(args string) (string, error) {
	if args == "" {
		return "", fmt.Errorf("worker ID required")
	}

	workerID := args
	ctx := context.Background()
	d.mu.Lock()
	w, ok := d.workers[workerID]
	if !ok {
		d.mu.Unlock()
		return "", fmt.Errorf("worker not found")
	}
	if w.state == protocol.WorkerReviewing && w.beadID != "" {
		procMgr := d.procMgr
		d.mu.Unlock()
		return d.restartCheckpointOwnedWorker(ctx, w, procMgr)
	}
	d.mu.Unlock()

	st, err := d.takeRestartWorkerState(workerID)
	if err != nil {
		return "", err
	}
	if st.retryPending {
		return d.restartWorkerPreservingQGRetry(ctx, workerID, st)
	}
	beadID, assignmentID, wasManaged := st.beadID, st.assignmentID, st.wasManaged
	procMgr := st.procMgr

	killErr := d.killManagedWorkerForRestart(ctx, procMgr, workerID, beadID, wasManaged)
	completeErr := d.completeRestartAssignment(ctx, beadID, assignmentID, workerID)
	if completeErr != nil || killErr != nil {
		if wasManaged {
			d.forgetPendingManagedWorker(workerID)
		}
		if completeErr != nil {
			_ = d.logEvent(ctx, "restart_worker_assignment_completion_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, completeErr.Error()))
			return "", completeErr
		}
		return "", killErr
	}

	// Spawn new worker process with same ID
	if procMgr != nil {
		_, err := procMgr.Spawn(workerID)
		if err != nil {
			_ = d.logEvent(ctx, "worker_spawn_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
			return "", fmt.Errorf("spawn new worker: %w", err)
		}
	}
	if beadID != "" {
		_ = d.logEvent(ctx, "worker_restarted", "dispatcher", beadID, workerID,
			`{"reason":"restart-worker directive"}`)
	}

	return fmt.Sprintf("worker %s restarted", workerID), nil
}

func (d *Dispatcher) restartCheckpointOwnedWorker(
	ctx context.Context,
	expected *trackedWorker,
	procMgr ProcessManager,
) (string, error) {
	released, err := d.releaseCheckpointOwnedWorker(ctx, expected, ReviewReleaseCauseRestarted)
	if err != nil {
		return "", fmt.Errorf("release review checkpoint worker for restart: %w", err)
	}
	if !released {
		return "", fmt.Errorf("release review checkpoint worker for restart: ownership changed")
	}

	// A replacement generation may connect while the durable transaction is in
	// flight. It already owns the worker ID, so never kill or respawn over it.
	d.mu.Lock()
	current := d.workers[expected.id]
	d.mu.Unlock()
	if current != nil {
		return fmt.Sprintf("worker %s restarted", expected.id), nil
	}

	sendShutdownWithoutBuffering(expected)
	if err := d.killManagedWorkerForRestart(ctx, procMgr, expected.id, expected.beadID, expected.managed); err != nil {
		return "", err
	}
	if expected.managed {
		d.mu.Lock()
		d.pendingManagedIDs[expected.id] = true
		d.pendingManagedSince[expected.id] = d.nowFunc()
		d.mu.Unlock()
	}
	if procMgr != nil {
		if _, err := procMgr.Spawn(expected.id); err != nil {
			return "", fmt.Errorf("spawn new worker: %w", err)
		}
	}
	return fmt.Sprintf("worker %s restarted", expected.id), nil
}

func (d *Dispatcher) killManagedWorkerForRestart(ctx context.Context, procMgr ProcessManager, workerID, beadID string, wasManaged bool) error {
	if !wasManaged || procMgr == nil {
		return nil
	}
	if err := procMgr.Kill(workerID); err != nil {
		_ = d.logEvent(ctx, "restart_worker_kill_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return fmt.Errorf("kill managed worker for restart: %w", err)
	}
	return nil
}

// completeRestartAssignment makes a restarted worker's assignment available
// again. Tracking is cleared only after completion succeeds so failed cleanup
// remains visible for recovery instead of stranding an active assignment.
func (d *Dispatcher) completeRestartAssignment(ctx context.Context, beadID string, assignmentID int64, workerID string) error {
	if beadID == "" {
		return nil
	}
	if err := d.completeAssignment(ctx, assignmentID, beadID); err != nil {
		_ = d.logEvent(ctx, "restart_worker_assignment_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
		return fmt.Errorf("complete restart assignment: %w", err)
	}
	if d.shouldReopenBead(ctx, beadID) {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "restart_worker_bead_reset_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
	}
	d.clearBeadTracking(beadID)
	_ = d.logEvent(ctx, "restart_worker_assignment_recovered", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"assignment_id":%d}`, assignmentID))
	d.notifyAssignLoop()
	return nil
}

// applyPreempt gracefully preempts a worker for higher-priority work.
// Unlike restart-worker, this sends a PREEMPT message to allow the worker
// to complete its current operation cleanly before stopping.
func (d *Dispatcher) applyPreempt(args string) (string, error) {
	if args == "" {
		return "", fmt.Errorf("worker ID required")
	}

	workerID := args
	ctx := context.Background()

	d.mu.Lock()
	w, ok := d.workers[workerID]
	if !ok {
		d.mu.Unlock()
		return "", fmt.Errorf("worker not found")
	}

	// Mark worker as preempting; save previous state for rollback on send failure.
	prevState := w.state
	w.state = protocol.WorkerPreempting

	// Send PREEMPT message through sendToWorker (handles disconnected workers).
	msg := protocol.Message{
		Type: protocol.MsgPreempt,
	}
	if err := d.sendToWorker(w, msg); err != nil {
		// Reset state: preempt message was not delivered.
		w.state = prevState
		d.mu.Unlock()
		return "", fmt.Errorf("send preempt message: %w", err)
	}

	beadID := w.beadID
	d.mu.Unlock()

	// Log the preemption event
	if beadID != "" {
		_ = d.logEvent(ctx, "worker_preempted", "dispatcher", beadID, workerID,
			`{"reason":"preempt directive"}`)
	}

	return fmt.Sprintf("worker %s preempted", workerID), nil
}

// handleDirectiveWithACK handles a DIRECTIVE message from the manager and sends an ACK response.
// This is used for short-lived manager connections that send a directive and expect an ACK.
func (d *Dispatcher) handleDirectiveWithACK(ctx context.Context, conn net.Conn, msg protocol.Message) {
	if msg.Directive == nil {
		return
	}

	dir := protocol.Directive(msg.Directive.Op)
	args := msg.Directive.Args
	source, reason := directiveProvenance(msg.Directive)
	ack := protocol.ACKPayload{OK: true}

	if !dir.Valid() && dir != directiveLaunchWorkers && dir != directiveCancelWorkerLaunch {
		ack.OK = false
		ack.Detail = "invalid directive"
	} else {
		detail, err := d.applyDirectiveWithProvenance(dir, args, source, reason)
		if err != nil {
			ack.OK = false
			ack.Detail = err.Error()
		} else {
			_ = d.logEvent(ctx, "directive", source, "", "",
				fmt.Sprintf(`{"directive":%q,"args":%q,"source":%q,"reason":%q}`, msg.Directive.Op, args, source, reason))
			ack.Detail = detail
		}
	}

	// Send ACK response
	ackMsg := protocol.Message{
		Type: protocol.MsgACK,
		ACK:  &ack,
	}
	data, err := json.Marshal(ackMsg)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = conn.Write(data)
}

const (
	directiveLaunchWorkers      protocol.Directive = "launch-workers"
	directiveCancelWorkerLaunch protocol.Directive = "cancel-worker-launch"
)

type workerLaunchReservation struct {
	WorkerIDs []string `json:"worker_ids"`
}
