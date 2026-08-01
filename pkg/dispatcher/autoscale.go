package dispatcher

import (
	"context"
	"fmt"
	"oro/pkg/protocol"
	"strconv"
	"time"
)

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
