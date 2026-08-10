package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"oro/pkg/protocol"
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
	if !ok || worker.reviewReleaseToken != 0 || worker.beadID != beadID || !preemptableWorkerState(worker.state) {
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

// heartbeatLoop, checkHeartbeats → worker_pool.go

// --- SQLite helpers ---

// recordWorkerProgress persists a worker event that is useful for auditing
// assignment activity. It deliberately does not update lastProgress: timeout
// state is driven only by real worker protocol transitions.
