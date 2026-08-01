package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"oro/pkg/protocol"
	"strconv"
	"strings"
	"time"
)

func (d *Dispatcher) applyDirective(dir protocol.Directive, args string) (string, error) {
	return d.applyDirectiveWithProvenance(dir, args, "operator", "operator_request")
}

//nolint:gocyclo // dispatcher routing function - complexity is inherent to the pattern
func (d *Dispatcher) applyDirectiveWithProvenance(dir protocol.Directive, args, source, reason string) (string, error) {
	if detail, handled, err := d.applyCapacityDirective(dir, args); handled {
		return detail, err
	}
	if detail, handled, err := d.applyOpsDirective(dir, args); handled {
		return detail, err
	}
	if detail, handled, err := d.applyEscalationDirective(dir, args); handled {
		return detail, err
	}
	switch dir {
	case protocol.DirectiveScale:
		return d.applyScaleDirective(args)
	case protocol.DirectiveKillWorker:
		return d.applyKillWorker(args)
	case protocol.DirectiveSpawnFor:
		return d.applySpawnFor(args)
	case protocol.DirectiveRestartWorker:
		return d.applyRestartWorker(args)
	case protocol.DirectivePreempt:
		return d.applyPreempt(args)
	case protocol.DirectiveHealth:
		return d.applyHealth()
	case protocol.DirectiveWorkerLogs:
		return d.applyWorkerLogs(args)
	case protocol.DirectiveStart:
		return d.applyStart()
	case protocol.DirectiveStop:
		return "", fmt.Errorf("stop directive disabled; use 'oro stop' for graceful shutdown")
	case protocol.DirectivePause:
		return d.applyPause(source, reason)
	case protocol.DirectiveResume:
		return d.applyResume()
	case protocol.DirectiveStatus:
		return d.applyStatus()
	case protocol.DirectiveFocus:
		return d.applyFocus(args)
	case protocol.DirectiveShutdown:
		// Reject shutdown via UDS directive — agents can bypass ORO_ROLE guards.
		// Legitimate shutdown uses SIGINT (oro stop) which the daemon always honors.
		return "", fmt.Errorf("shutdown directive rejected; use 'oro stop' (sends SIGINT)")
	case protocol.DirectiveRestartDaemon:
		return d.applyRestartDaemon()
	default:
		return fmt.Sprintf("applied %s", dir), nil
	}
}

func (d *Dispatcher) applyEscalationDirective(dir protocol.Directive, args string) (detail string, handled bool, err error) {
	switch dir {
	case protocol.DirectivePendingEscalations:
		detail, err := d.applyPendingEscalations()
		return detail, true, err
	case protocol.DirectiveAckEscalation:
		detail, err := d.applyAckEscalation(args)
		return detail, true, err
	default:
		return "", false, nil
	}
}

func (d *Dispatcher) applyCapacityDirective(dir protocol.Directive, args string) (detail string, handled bool, err error) {
	switch dir {
	case protocol.DirectiveMaxWorkers:
		detail, err := d.applyMaxWorkersDirective(args)
		return detail, true, err
	case directiveLaunchWorkers:
		detail, err := d.applyLaunchWorkers(args)
		return detail, true, err
	case directiveCancelWorkerLaunch:
		detail, err := d.applyCancelWorkerLaunch(args)
		return detail, true, err
	default:
		return "", false, nil
	}
}

// applyStart transitions the dispatcher to running state.
func (d *Dispatcher) applyStart() (string, error) {
	d.setState(StateRunning)
	return "started", nil
}

// directiveProvenance normalizes legacy directives as explicit operator actions.
func directiveProvenance(payload *protocol.DirectivePayload) (source, reason string) {
	source = payload.Source
	if source == "" {
		source = "operator"
	}
	reason = payload.Reason
	if reason == "" {
		reason = "operator_request"
	}
	return source, reason
}

// applyPause transitions the dispatcher to paused state with its provenance.
func (d *Dispatcher) applyPause(source, reason string) (string, error) {
	d.mu.Lock()
	d.pauseSource = source
	d.pauseReason = reason
	d.mu.Unlock()
	d.setState(StatePaused)
	return "paused", nil
}

// applyResume transitions the dispatcher from paused to running.
func (d *Dispatcher) applyResume() (string, error) {
	if d.GetState() == StateRunning {
		return "already running", nil
	}
	d.setState(StateRunning)
	d.mu.Lock()
	d.pauseSource = ""
	d.pauseReason = ""
	d.mu.Unlock()
	return "resumed", nil
}

// applyStatus returns the dispatcher status JSON, throttled to avoid redundant
// rebuilds when the manager sends bursts of status requests. If a cached
// response exists and was built within statusThrottleWindow, it is returned
// immediately. Otherwise the status is rebuilt and cached.
func (d *Dispatcher) applyStatus() (string, error) {
	ctx := context.Background()
	storageHealth := d.storageHealth(ctx)
	storageJSON, err := json.Marshal(storageHealth)
	if err != nil {
		return "", fmt.Errorf("marshal storage health cache key: %w", err)
	}
	storageKey := string(storageJSON)

	now := d.nowFunc()
	d.mu.Lock()
	cached := d.lastStatusJSON
	elapsed := now.Sub(d.lastStatusTime)
	cachedStorageKey := d.lastStatusStorageKey
	d.mu.Unlock()
	if cached != "" && elapsed < statusThrottleWindow && cachedStorageKey == storageKey {
		return cached, nil
	}
	result := d.buildStatusJSONWithStorage(ctx, storageHealth)
	d.mu.Lock()
	d.lastStatusTime = now
	d.lastStatusJSON = result
	d.lastStatusStorageKey = storageKey
	d.mu.Unlock()
	return result, nil
}

// applyRestartDaemon initiates graceful shutdown for daemon restart.
// It closes shutdownCh to trigger the graceful shutdown sequence in Run(),
// which sends PREPARE_SHUTDOWN to all workers and exits cleanly.
func (d *Dispatcher) applyRestartDaemon() (string, error) {
	select {
	case <-d.shutdownCh:
		// Already closed
		return "restart already in progress", nil
	default:
		close(d.shutdownCh)
		return "restarting daemon", nil
	}
}

// applyFocus sets the focused epic and resumes the dispatcher if paused.
func (d *Dispatcher) applyFocus(args string) (string, error) {
	epic, immediate, err := parseFocusArgs(args)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	d.focusedEpic = epic
	d.focusVersion++
	d.mu.Unlock()
	if d.GetState() != StateRunning {
		d.setState(StateRunning)
	}
	if epic == "" {
		return "focus cleared", nil
	}
	if !immediate {
		return fmt.Sprintf("focused on %s", epic), nil
	}
	preempted := d.preemptWorkersOutsideFocus(context.Background(), epic)
	return fmt.Sprintf("focused on %s; preempted %d non-focused %s", epic, preempted, pluralize(preempted, "worker", "workers")), nil
}

func parseFocusArgs(args string) (epic string, immediate bool, err error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return "", false, nil
	}
	for _, field := range fields {
		switch field {
		case "--immediate", "-i":
			immediate = true
		default:
			if strings.HasPrefix(field, "-") {
				return "", false, fmt.Errorf("unknown focus option %q", field)
			}
			if epic != "" {
				return "", false, fmt.Errorf("focus accepts one epic ID")
			}
			epic = field
		}
	}
	if immediate && epic == "" {
		return "", false, fmt.Errorf("epic ID required with --immediate")
	}
	return epic, immediate, nil
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func (d *Dispatcher) preemptWorkersOutsideFocus(ctx context.Context, focusedEpic string) int {
	type candidate struct {
		workerID string
		beadID   string
	}
	d.mu.Lock()
	candidates := make([]candidate, 0, len(d.workers))
	for workerID, worker := range d.workers {
		if worker.beadID == "" || !preemptableWorkerState(worker.state) {
			continue
		}
		candidates = append(candidates, candidate{workerID: workerID, beadID: worker.beadID})
	}
	d.mu.Unlock()

	parentCache := make(map[string]string)
	preempted := 0
	for _, candidate := range candidates {
		if d.beadIsFocusedDescendant(ctx, candidate.beadID, focusedEpic, parentCache) {
			continue
		}
		if d.restartWorkerIfStillOnBead(ctx, candidate.workerID, candidate.beadID, "focus --immediate") {
			preempted++
		}
	}
	if preempted > 0 {
		d.notifyAssignLoop()
	}
	return preempted
}

func preemptableWorkerState(state protocol.WorkerState) bool {
	return state == protocol.WorkerBusy || state == protocol.WorkerReviewing
}

func (d *Dispatcher) beadIsFocusedDescendant(ctx context.Context, beadID, focusedEpic string, parentCache map[string]string) bool {
	if beadID == focusedEpic {
		return true
	}
	if cached, ok := parentCache[beadID]; ok {
		return d.isFocusedDescendant(ctx, cached, focusedEpic, parentCache)
	}
	bead, err := d.beads.Show(ctx, beadID)
	if err != nil || bead == nil {
		parentCache[beadID] = ""
		return false
	}
	parentCache[beadID] = bead.Epic
	return d.isFocusedDescendant(ctx, bead.Epic, focusedEpic, parentCache)
}

func (d *Dispatcher) restartWorkerIfStillOnBead(ctx context.Context, workerID, beadID, reason string) bool {
	d.mu.Lock()
	worker, ok := d.workers[workerID]
	if !ok || worker.beadID != beadID || !preemptableWorkerState(worker.state) {
		d.mu.Unlock()
		return false
	}
	assignmentID := worker.assignmentID
	wasManaged := worker.managed
	_ = worker.conn.Close()
	delete(d.workers, workerID)
	if wasManaged {
		d.pendingManagedIDs[workerID] = true
		d.pendingManagedSince[workerID] = d.nowFunc()
	}
	procMgr := d.procMgr
	d.mu.Unlock()

	if d.shouldReopenBead(ctx, beadID) {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "focus_immediate_bead_reset_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
	}
	d.clearBeadTracking(beadID)
	_ = d.completeAssignment(ctx, assignmentID, beadID)
	_ = d.logEvent(ctx, "worker_restarted", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"reason":%q}`, reason))

	if procMgr != nil {
		if _, err := procMgr.Spawn(workerID); err != nil {
			_ = d.logEvent(ctx, "worker_spawn_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
	}
	return true
}

// buildStatusJSON constructs the status response JSON string.
// snapshotWorkers builds the per-worker status slice, assignments map, and
// active/idle counts. Caller must hold d.mu.
func (d *Dispatcher) applyScaleDirective(args string) (string, error) {
	target, err := strconv.Atoi(args)
	if err != nil {
		return "", fmt.Errorf("invalid scale args %q: %w", args, err)
	}
	if target < 0 {
		return "", fmt.Errorf("invalid scale target %d: must be non-negative", target)
	}

	d.mu.Lock()
	if maxW := d.cfg.MaxWorkers; maxW > 0 && target > maxW {
		target = maxW
	}
	d.targetWorkers = target
	d.explicitScaleTarget = true
	d.unexpectedManagedExits = 0
	connected := len(d.workers)
	d.mu.Unlock()

	detail := d.reconcileScale()
	if detail == "" {
		detail = fmt.Sprintf("target=%d, current=%d, no change", target, connected)
	}
	return detail, nil
}

// applyMaxWorkersDirective sets the maximum worker pool size at runtime.
// It updates cfg.MaxWorkers, clamps targetWorkers to the new ceiling if needed,
// and calls reconcileScale to enforce the updated limit immediately.
func (d *Dispatcher) applyMaxWorkersDirective(args string) (string, error) {
	if args == "" {
		return "", fmt.Errorf("worker count required")
	}
	n, err := strconv.Atoi(args)
	if err != nil {
		return "", fmt.Errorf("invalid worker count %q: %w", args, err)
	}
	if n < 0 {
		return "", fmt.Errorf("worker count must be non-negative, got %d", n)
	}

	d.mu.Lock()
	d.cfg.MaxWorkers = n
	if d.targetWorkers > n {
		d.targetWorkers = n
	}
	var killPending []string
	procMgr := d.procMgr
	if n > 0 {
		live := d.liveWorkerCountLocked()
		for id := range d.pendingManagedIDs {
			if live <= n {
				break
			}
			killPending = append(killPending, id)
			delete(d.pendingManagedIDs, id)
			delete(d.pendingManagedSince, id)
			delete(d.pendingWorkerTargets, id)
			delete(d.pendingSpawnForWorkers, id)
			live--
		}
	}
	d.mu.Unlock()

	if procMgr != nil {
		for _, id := range killPending {
			_ = procMgr.Kill(id)
		}
	}
	d.reconcileScale()
	return fmt.Sprintf("max_workers=%d", n), nil
}

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
			execution:    w.execution,
			worktree:     w.worktree,
			runtime:      w.runtime,
			model:        w.model,
			reasoning:    w.reasoning,
			epicID:       w.epicID,
			baseBranch:   w.baseBranch,
			targetBranch: w.targetBranch,
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

// maybeAutoScale increases targetWorkers when assignable beads exist but no
// idle workers are available. Scales up to min(queue depth, MaxWorkers).
func (d *Dispatcher) maybeAutoScale(ctx context.Context, queueDepth, idleCount int) {
	if queueDepth == 0 || idleCount > 0 {
		return
	}

	d.mu.Lock()
	if d.hasPendingSpawnForLocked() {
		d.mu.Unlock()
		return
	}
	currentTarget := d.targetWorkers
	maxWorkers := d.cfg.MaxWorkers
	explicitScaleTarget := d.explicitScaleTarget
	liveManagedCount := d.liveManagedWorkerCountLocked()
	if explicitScaleTarget && currentTarget > 0 && liveManagedCount <= currentTarget {
		d.explicitScaleTarget = false
		explicitScaleTarget = false
	}
	d.mu.Unlock()

	if explicitScaleTarget {
		return
	}

	if currentTarget >= maxWorkers {
		return
	}

	// Scale to min(queue depth, MaxWorkers)
	newTarget := queueDepth
	if newTarget > maxWorkers {
		newTarget = maxWorkers
	}

	if newTarget > currentTarget {
		d.mu.Lock()
		d.targetWorkers = newTarget
		d.mu.Unlock()
		d.reconcileScale()
		_ = d.logEvent(ctx, "auto_scale", "dispatcher", "", "",
			fmt.Sprintf("scaled to %d workers (queue depth: %d)", newTarget, queueDepth))
	}
}

// reconcileScale compares target vs connected managed workers and spawns or
// shuts down managed workers to reach the target. Unmanaged (externally
// connected) workers are invisible to scaling in all modes.
//
// Uses atomic flag to prevent concurrent execution. If already running, returns
// immediately to avoid duplicate spawns. See oro-ovpc.1.
func (d *Dispatcher) reconcileScale() string {
	// Use atomic CAS to ensure only one reconcileScale runs at a time (oro-ovpc.1).
	// If another call is in progress, return immediately - the running call will
	// handle the reconciliation. This prevents duplicate spawns without deadlock.
	if !d.reconcilingScale.CompareAndSwap(false, true) {
		return "" // already reconciling
	}
	defer d.reconcilingScale.Store(false)

	d.mu.Lock()
	d.cleanupStalePendingManagedLocked(d.nowFunc())
	target := d.targetWorkers
	// Count both connected managed workers AND pending spawns (oro-ovpc).
	// Without counting pending, concurrent reconcileScale calls both see
	// managedCount=0 and spawn duplicates before workers connect.
	managedCount := d.managedWorkerCountLocked()
	// Guard: cap at 2*target using only managed workers (connected + pending +
	// exits) to prevent runaway crash-respawn loops (oro-135n, oro-kdne).
	// Unmanaged (orphaned) workers are excluded so they cannot block managed
	// worker spawning.
	managedExits := d.unexpectedManagedExits
	totalWorkers := d.activeWorkerCountLocked()
	totalLiveWorkers := d.liveWorkerCountLocked()
	maxWorkers := d.cfg.MaxWorkers
	hasPendingSpawnFor := d.hasPendingSpawnForLocked()
	d.mu.Unlock()

	desiredManaged := target
	if maxWorkers > 0 && totalWorkers > maxWorkers {
		capDesired := managedCount - (totalWorkers - maxWorkers)
		if capDesired < desiredManaged {
			desiredManaged = capDesired
		}
	}
	if desiredManaged < 0 {
		desiredManaged = 0
	}

	switch {
	case managedCount > desiredManaged:
		return d.scaleDown(desiredManaged, managedCount)
	case managedCount < target:
		if hasPendingSpawnFor {
			return fmt.Sprintf("target=%d, managed=%d, pending spawn-for active, skipping scaleUp", target, managedCount)
		}
		if managedCount+managedExits >= 2*target {
			return fmt.Sprintf("target=%d, managed=%d, exits=%d, managed+exits %d >= 2*target %d — cap reached, skipping scaleUp",
				target, managedCount, managedExits, managedCount+managedExits, 2*target)
		}
		capacity := target - managedCount
		if maxWorkers > 0 {
			capacity = maxWorkers - totalLiveWorkers
		}
		if capacity <= 0 {
			return fmt.Sprintf("target=%d, managed=%d, total=%d, MaxWorkers=%d — total cap reached, skipping scaleUp",
				target, managedCount, totalLiveWorkers, maxWorkers)
		}
		return d.scaleUp(target, managedCount, capacity)
	default:
		return ""
	}
}

func (d *Dispatcher) cleanupStalePendingManagedLocked(now time.Time) {
	if d.cfg.HeartbeatTimeout <= 0 {
		return
	}
	for id := range d.pendingManagedSince {
		if !d.pendingManagedIDs[id] {
			delete(d.pendingManagedSince, id)
			continue
		}
		if now.Sub(d.pendingManagedSince[id]) <= d.cfg.HeartbeatTimeout {
			continue
		}
		spawnFor := d.pendingSpawnForWorkers[id]
		delete(d.pendingManagedIDs, id)
		delete(d.pendingManagedSince, id)
		delete(d.pendingWorkerTargets, id)
		delete(d.pendingSpawnForWorkers, id)
		if !spawnFor {
			d.unexpectedManagedExits++
		}
	}
	for id, since := range d.pendingExternalSince {
		if !d.pendingExternalIDs[id] {
			delete(d.pendingExternalSince, id)
			continue
		}
		if now.Sub(since) <= d.cfg.HeartbeatTimeout {
			continue
		}
		delete(d.pendingExternalIDs, id)
		delete(d.pendingExternalSince, id)
	}
}

func (d *Dispatcher) managedWorkerCountLocked() int {
	count := 0
	for id := range d.pendingManagedIDs {
		if !d.pendingSpawnForWorkers[id] {
			count++
		}
	}
	for _, w := range d.workers {
		if w.managed && !w.spawnFor && w.state != protocol.WorkerShuttingDown {
			count++
		}
	}
	return count
}

func (d *Dispatcher) liveManagedWorkerCountLocked() int {
	count := 0
	for id := range d.pendingManagedIDs {
		if !d.pendingSpawnForWorkers[id] {
			count++
		}
	}
	for _, w := range d.workers {
		if w.managed && !w.spawnFor {
			count++
		}
	}
	return count
}

func (d *Dispatcher) activeWorkerCountLocked() int {
	count := 0
	for _, w := range d.workers {
		if w.state != protocol.WorkerShuttingDown {
			count++
		}
	}
	for id := range d.pendingManagedIDs {
		if _, connected := d.workers[id]; !connected {
			count++
		}
	}
	for id := range d.pendingExternalIDs {
		if _, connected := d.workers[id]; !connected {
			count++
		}
	}
	return count
}

func (d *Dispatcher) liveWorkerCountLocked() int {
	count := len(d.workers)
	for id := range d.pendingManagedIDs {
		if _, connected := d.workers[id]; !connected {
			count++
		}
	}
	for id := range d.pendingExternalIDs {
		if _, connected := d.workers[id]; !connected {
			count++
		}
	}
	return count
}

// scaleUp spawns (target - connected) new worker processes.
func (d *Dispatcher) scaleUp(target, connected, capacity int) string {
	toSpawn := target - connected
	if toSpawn > capacity {
		toSpawn = capacity
	}
	if d.procMgr == nil {
		return fmt.Sprintf("target=%d, need %d workers but no ProcessManager configured", target, toSpawn)
	}

	spawned := 0
	for i := 0; i < toSpawn; i++ {
		id := fmt.Sprintf("worker-%d-%d", d.nowFunc().UnixNano(), i)
		d.mu.Lock()
		if d.cfg.MaxWorkers > 0 && d.liveWorkerCountLocked() >= d.cfg.MaxWorkers {
			d.mu.Unlock()
			break
		}
		d.pendingManagedIDs[id] = true
		d.pendingManagedSince[id] = d.nowFunc()
		d.mu.Unlock()
		if _, err := d.procMgr.Spawn(id); err != nil {
			d.mu.Lock()
			delete(d.pendingManagedIDs, id)
			delete(d.pendingManagedSince, id)
			d.mu.Unlock()
			continue
		}
		spawned++
	}
	return fmt.Sprintf("target=%d, spawning %d", target, spawned)
}

// scaleDown initiates graceful shutdown for excess managed workers, preferring
// idle workers first, then newest busy workers. Unmanaged workers are skipped.
func (d *Dispatcher) scaleDown(target, connected int) string {
	toRemove := connected - target

	d.mu.Lock()
	killPending := d.removePendingManagedForScaleDownLocked(&toRemove)
	idle, busy := d.managedScaleDownCandidatesLocked(toRemove)
	procMgr := d.procMgr
	d.mu.Unlock()

	// Build removal list: idle first, then busy (newest = end of slice).
	var victims []string
	victims = append(victims, idle...)
	victims = append(victims, busy...)

	// Trim to the number we need to remove.
	if len(victims) > toRemove {
		victims = victims[:toRemove]
	}

	if procMgr != nil {
		for _, id := range killPending {
			_ = procMgr.Kill(id)
		}
	}
	for _, id := range victims {
		d.gracefulShutdownWorker(id, d.cfg.ShutdownTimeout, shutdownReasonScaleDown)
	}

	return fmt.Sprintf("target=%d, shutting down %d", target, len(killPending)+len(victims))
}

func (d *Dispatcher) removePendingManagedForScaleDownLocked(toRemove *int) []string {
	var killPending []string
	for id := range d.pendingManagedIDs {
		if *toRemove == 0 {
			break
		}
		if d.pendingSpawnForWorkers[id] {
			continue
		}
		killPending = append(killPending, id)
		delete(d.pendingManagedIDs, id)
		delete(d.pendingManagedSince, id)
		delete(d.pendingWorkerTargets, id)
		delete(d.pendingSpawnForWorkers, id)
		(*toRemove)--
	}
	return killPending
}

func (d *Dispatcher) managedScaleDownCandidatesLocked(toRemove int) (idle, busy []string) {
	if toRemove <= 0 {
		return nil, nil
	}
	for id, w := range d.workers {
		if !isManagedScaleDownCandidate(w) {
			continue
		}
		if w.state == protocol.WorkerIdle {
			idle = append(idle, id)
		} else {
			busy = append(busy, id)
		}
	}
	return idle, busy
}

func isManagedScaleDownCandidate(w *trackedWorker) bool {
	return w.managed && !w.spawnFor && w.state != protocol.WorkerShuttingDown
}

// heartbeatLoop, checkHeartbeats → worker_pool.go

// --- SQLite helpers ---

// recordWorkerProgress persists a worker event that is useful for auditing
// assignment activity. It deliberately does not update lastProgress: timeout
// state is driven only by real worker protocol transitions.
