package dispatcher

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/factoryhealth"
	"oro/pkg/protocol"

	"github.com/fsnotify/fsnotify"
)

// assignLoop watches the filesystem task-data directory and assigns work when
// files change. Native sqlite mode skips that watch.
func (d *Dispatcher) assignLoop(ctx context.Context) {
	if d.shouldSkipTaskDataWatch() {
		d.assignLoopPoll(ctx)
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		// Fallback to pure polling if fsnotify fails
		d.assignLoopPoll(ctx)
		return
	}
	defer func() { _ = watcher.Close() }()

	if err := watcher.Add(d.beadsDir); err != nil {
		// Fallback to pure polling if watch fails
		d.assignLoopPoll(ctx)
		return
	}

	// Fallback poll as safety net (default 60s)
	fallbackTicker := time.NewTicker(d.cfg.FallbackPollInterval)
	defer fallbackTicker.Stop()

	var restartCount int
	var lastPanicTime time.Time

	for {
		if d.assignLoopIter(ctx, watcher, fallbackTicker, &restartCount, &lastPanicTime) {
			return
		}
	}
}

func (d *Dispatcher) shouldSkipTaskDataWatch() bool {
	return strings.EqualFold(strings.TrimSpace(d.beadSourceMode), "sqlite")
}

// assignLoopIter runs one select iteration of assignLoop with panic recovery.
// Returns true when the loop should exit cleanly (ctx cancelled or shutdown).
func (d *Dispatcher) assignLoopIter(
	ctx context.Context,
	watcher *fsnotify.Watcher,
	fallbackTicker *time.Ticker,
	restartCount *int,
	lastPanicTime *time.Time,
) (exit bool) {
	defer func() {
		if r := recover(); r != nil {
			if d.handleLoopPanic(ctx, r, restartCount, lastPanicTime) {
				exit = true
			}
		}
	}()
	select {
	case <-ctx.Done():
		return true
	case <-d.shutdownCh:
		return true
	case <-watcher.Events:
		// File changed in task-data directory.
		d.callTryAssign(ctx)
	case err := <-watcher.Errors:
		if err != nil {
			_ = d.logEvent(ctx, "watcher_error", "dispatcher", "", "", err.Error())
		}
	case <-d.workerReadyCh:
		// A new idle worker connected — assign immediately without waiting for poll.
		d.callTryAssign(ctx)
	case <-fallbackTicker.C:
		// Safety net poll
		d.callTryAssign(ctx)
	}
	return false
}

// assignLoopPoll is a fallback polling loop when fsnotify is unavailable.
// Each iteration is wrapped in a defer/recover so a panic inside tryAssign
// logs a goroutine_panic event and restarts the loop after exponential backoff.
func (d *Dispatcher) assignLoopPoll(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	var restartCount int
	var lastPanicTime time.Time

	for {
		exit := func() (shouldExit bool) {
			defer func() {
				if r := recover(); r != nil {
					if d.handleLoopPanic(ctx, r, &restartCount, &lastPanicTime) {
						shouldExit = true
					}
				}
			}()
			select {
			case <-ctx.Done():
				return true
			case <-d.shutdownCh:
				return true
			case <-d.workerReadyCh:
				// A new idle worker connected — assign immediately without waiting for poll.
				d.callTryAssign(ctx)
			case <-ticker.C:
				d.callTryAssign(ctx)
			}
			return false
		}()
		if exit {
			return
		}
	}
}

type schedulingUnitKind int

const (
	unitSpawnFor schedulingUnitKind = iota
	unitFocused
	unitIndependent
	unitEpic
)

type schedulingUnit struct {
	kind          schedulingUnitKind
	epicID        string
	epicPriority  int
	epicCreatedAt string
	beads         []protocol.Bead
}

type schedulingPlan struct {
	units []schedulingUnit
}

type schedulingEpicRoot struct {
	id        string
	priority  int
	createdAt string
	ok        bool
}

// buildSchedulingPlan groups ready beads into assignment units. Independent
// work is scheduled before epic units, while epic units are ordered by their
// root epic priority so one epic's frontier stays contiguous.
func (d *Dispatcher) buildSchedulingPlan(ctx context.Context, beads []protocol.Bead) (plan schedulingPlan, prioritySnapshot map[string]bool, focusVersion uint64) {
	d.mu.Lock()
	epic := d.focusedEpic
	focusVersion = d.focusVersion
	prioritySnapshot = make(map[string]bool, len(d.priorityBeads))
	for id := range d.priorityBeads {
		prioritySnapshot[id] = true
	}
	d.mu.Unlock()

	focused := d.focusedDescendants(ctx, beads, epic)
	parentCache := make(map[string]*protocol.BeadDetail)
	epicUnitIndexes := make(map[string]int)

	for _, bead := range beads {
		switch {
		case prioritySnapshot[bead.ID]:
			plan.appendUnit(unitSpawnFor, bead)
		case focused[bead.ID]:
			plan.appendUnit(unitFocused, bead)
		case bead.Epic == "":
			plan.appendUnit(unitIndependent, bead)
		default:
			plan.appendEpicUnit(d.schedulingEpicRoot(ctx, bead.Epic, parentCache), bead, epicUnitIndexes)
		}
	}
	plan.sort()

	return plan, prioritySnapshot, focusVersion
}

func (p *schedulingPlan) appendUnit(kind schedulingUnitKind, bead protocol.Bead) {
	p.units = append(p.units, schedulingUnit{
		kind:  kind,
		beads: []protocol.Bead{bead},
	})
}

func (p *schedulingPlan) appendEpicUnit(root schedulingEpicRoot, bead protocol.Bead, unitIndexes map[string]int) {
	if !root.ok {
		p.appendUnit(unitIndependent, bead)
		return
	}
	unitIdx, ok := unitIndexes[root.id]
	if !ok {
		p.units = append(p.units, schedulingUnit{
			kind:          unitEpic,
			epicID:        root.id,
			epicPriority:  root.priority,
			epicCreatedAt: root.createdAt,
		})
		unitIdx = len(p.units) - 1
		unitIndexes[root.id] = unitIdx
	}
	p.units[unitIdx].beads = append(p.units[unitIdx].beads, bead)
}

func (p *schedulingPlan) sort() {
	for i := range p.units {
		sort.SliceStable(p.units[i].beads, func(a, b int) bool {
			return p.units[i].beads[a].Priority < p.units[i].beads[b].Priority
		})
	}
	sort.SliceStable(p.units, func(i, j int) bool {
		return schedulingUnitLess(p.units[i], p.units[j])
	})
}

func (p schedulingPlan) beads() []protocol.Bead {
	total := 0
	for _, unit := range p.units {
		total += len(unit.beads)
	}
	beads := make([]protocol.Bead, 0, total)
	for _, unit := range p.units {
		beads = append(beads, unit.beads...)
	}
	return beads
}

func schedulingUnitLess(left, right schedulingUnit) bool {
	if left.kind != right.kind {
		return left.kind < right.kind
	}
	if left.kind != unitEpic {
		return left.beads[0].Priority < right.beads[0].Priority
	}
	if left.epicPriority != right.epicPriority {
		return left.epicPriority < right.epicPriority
	}
	if left.epicCreatedAt != right.epicCreatedAt {
		return left.epicCreatedAt < right.epicCreatedAt
	}
	return left.epicID < right.epicID
}

func (d *Dispatcher) schedulingEpicRoot(ctx context.Context, parentID string, parentCache map[string]*protocol.BeadDetail) schedulingEpicRoot {
	visited := make(map[string]bool)
	var root schedulingEpicRoot
	current := parentID
	for current != "" {
		if visited[current] {
			return schedulingEpicRoot{}
		}
		visited[current] = true

		parent, ok := parentCache[current]
		if !ok {
			detail, err := d.beads.Show(ctx, current)
			if err != nil || detail == nil {
				return schedulingEpicRoot{}
			}
			parent = detail
			parentCache[current] = detail
		}
		if strings.EqualFold(parent.Type, "epic") {
			root = schedulingEpicRoot{
				id:        current,
				priority:  parent.Priority,
				createdAt: parent.CreatedAt,
				ok:        true,
			}
		}
		current = parent.Epic
	}
	return root
}

func (d *Dispatcher) focusedDescendants(ctx context.Context, beads []protocol.Bead, focusedEpic string) map[string]bool {
	focused := make(map[string]bool)
	if focusedEpic == "" {
		return focused
	}
	parentCache := make(map[string]string)
	for _, bead := range beads {
		if d.isFocusedDescendant(ctx, bead.Epic, focusedEpic, parentCache) {
			focused[bead.ID] = true
		}
	}
	return focused
}

func (d *Dispatcher) isFocusedDescendant(ctx context.Context, parentID, focusedEpic string, parentCache map[string]string) bool {
	seen := make(map[string]bool)
	for parentID != "" {
		if parentID == focusedEpic {
			return true
		}
		if seen[parentID] {
			return false
		}
		seen[parentID] = true
		if cached, ok := parentCache[parentID]; ok {
			parentID = cached
			continue
		}
		parent, err := d.beads.Show(ctx, parentID)
		if err != nil || parent == nil {
			parentCache[parentID] = ""
			return false
		}
		parentCache[parentID] = parent.Epic
		parentID = parent.Epic
	}
	return false
}

// tryAssign attempts to assign ready beads to idle workers.
func (d *Dispatcher) tryAssign(ctx context.Context) {
	_ = d.tryAssignBatch(ctx)
}

// tryAssignBatch runs one scheduling pass and returns a handle per assignment
// setup this pass launched. Production callers discard it; safeGo remains the
// lifecycle owner for shutdown.
//
// The scheduling pass must never block on a returned handle — every early
// return below yields nil or the handles accumulated so far, never an await.
func (d *Dispatcher) tryAssignBatch(ctx context.Context) []<-chan struct{} {
	// Only assign in running state.
	if d.GetState() != StateRunning {
		return nil
	}
	if err := d.observeStorageController(ctx); err != nil || !d.storageAdmissionAllowed() {
		return nil
	}

	// Detect beads closed externally while a worker is assigned and clean up.
	d.checkClosedBeadAssignments(ctx)

	// Reconcile worker pool size (spawns/removes workers to match target).
	d.reconcileScale()
	d.assignPendingHandoffsToIdleWorkers()

	// Find idle workers and count total workers.
	d.mu.Lock()
	var idle []idleWorker
	totalWorkers := 0
	for _, w := range d.workers {
		totalWorkers++
		if w.state == protocol.WorkerIdle {
			idle = append(idle, idleWorker{worker: w, targetBeadID: w.targetBeadID, spawnFor: w.spawnFor})
		}
	}
	d.mu.Unlock()

	// Poll for ready beads.
	allBeads, err := d.readyBeadsForScheduling(ctx)
	if err != nil {
		return nil
	}
	// Cache queue depth for status reporting.
	d.mu.Lock()
	d.cachedQueueDepth = len(allBeads)
	d.cachedIdleWorkers = len(idle)
	d.mu.Unlock()

	if d.shouldScanForCycles() {
		d.scanDependencyCycles(ctx)
	}

	beads := d.filterAssignable(ctx, allBeads)
	if redeployable, blocked := d.recoveryQuarantineAssignmentScope(ctx); blocked {
		return nil
	} else if len(redeployable) > 0 {
		beads = filterBeadsByID(beads, redeployable)
	}

	plan, pbSnapshot, focusVersion := d.buildSchedulingPlan(ctx, beads)
	beads = plan.beads()
	reservedTargets, hasPendingSpawnFor := d.reservedSpawnForTargets()

	// Auto-scale: if we have assignable beads but no idle workers, scale up to MaxWorkers.
	if !hasPendingSpawnFor {
		queueDepth, idleCount := autoscaleInputsForIdleWorkers(idle, beads, reservedTargets)
		d.maybeAutoScale(ctx, queueDepth, idleCount)
	}

	// Priority contention is now handled by the preemption system (oro-wofg).
	// Escalating to the manager is noisy and unhelpful.
	// if len(idle) == 0 && totalWorkers > 0 {
	// 	d.checkPriorityContention(ctx, beads, totalWorkers)
	// 	return
	// }
	if len(idle) == 0 {
		return nil
	}

	assignedBeads := d.assignTargetedIdleWorkers(ctx, idle, beads, focusVersion)
	return d.assignGeneralIdleWorkers(ctx, idle, plan, pbSnapshot, assignedBeads, reservedTargets, focusVersion)
}

func (d *Dispatcher) readyBeadsForScheduling(ctx context.Context) ([]protocol.Bead, error) {
	beads, err := d.beads.Ready(ctx)
	if err != nil {
		return nil, fmt.Errorf("load ready beads for scheduling: %w", err)
	}
	return d.filterBlockedEpicBranchReady(ctx, beads)
}

func filterBeadsByID(beads []protocol.Bead, ids map[string]bool) []protocol.Bead {
	filtered := make([]protocol.Bead, 0, len(beads))
	for _, bead := range beads {
		if ids[bead.ID] {
			filtered = append(filtered, bead)
		}
	}
	return filtered
}

// recoveryQuarantineAssignmentScope preserves the recovery safety interlock:
// open quarantines block ordinary work, but a clean preserved worktree may be
// handed to one fresh worker to continue its own bead.
func (d *Dispatcher) recoveryQuarantineAssignmentScope(ctx context.Context) (map[string]bool, bool) {
	if d.db == nil {
		d.setRecoveryAssignmentFreeze(false, 0, "")
		return nil, false
	}
	openQuarantines, err := factoryhealth.LoadRecoveryQuarantineMetrics(ctx, d.db)
	if err != nil {
		reason := "recovery_quarantine_metric_load_failed: " + err.Error()
		d.setRecoveryAssignmentFreeze(true, 0, reason)
		d.logRecoveryAssignmentBlocked(ctx, 0, reason)
		return nil, true
	}
	if openQuarantines == 0 {
		d.setRecoveryAssignmentFreeze(false, 0, "")
		return nil, false
	}
	preservableQuarantines, err := d.countPreservableRecoveryQuarantines(ctx)
	if err != nil {
		reason := "recovery_quarantine_classification_failed: " + err.Error()
		d.setRecoveryAssignmentFreeze(true, openQuarantines, reason)
		d.logRecoveryAssignmentBlocked(ctx, openQuarantines, reason)
		return nil, true
	}
	if preservableQuarantines == 0 {
		d.setRecoveryAssignmentFreeze(false, 0, "")
		return nil, false
	}
	redeployable, err := d.autoRedeployablePreservedWorktrees(ctx)
	if err != nil {
		reason := "recovery_quarantine_inspection_failed: " + err.Error()
		d.setRecoveryAssignmentFreeze(true, preservableQuarantines, reason)
		d.logRecoveryAssignmentBlocked(ctx, preservableQuarantines, reason)
		return nil, true
	}
	if len(redeployable) == 0 {
		const reason = "open_recovery_quarantine"
		d.setRecoveryAssignmentFreeze(true, preservableQuarantines, reason)
		d.logRecoveryAssignmentBlocked(ctx, preservableQuarantines, reason)
		return nil, true
	}
	d.setRecoveryAssignmentFreeze(false, 0, "")
	return redeployable, false
}

func (d *Dispatcher) setRecoveryAssignmentFreeze(frozen bool, blockingQuarantines int, reason string) {
	d.mu.Lock()
	d.assignmentFrozenByQuarantine = frozen
	d.blockingRecoveryQuarantines = blockingQuarantines
	d.assignmentFreezeReason = reason
	d.mu.Unlock()
}

func (d *Dispatcher) logRecoveryAssignmentBlocked(ctx context.Context, openQuarantines int, reason string) {
	now := d.nowFunc()
	d.mu.Lock()
	if !d.lastRecoveryAssignmentBlockLog.IsZero() && now.Sub(d.lastRecoveryAssignmentBlockLog) < time.Minute {
		d.mu.Unlock()
		return
	}
	d.lastRecoveryAssignmentBlockLog = now
	d.mu.Unlock()

	_ = d.logEvent(ctx, "assignment_blocked_by_recovery_quarantine", "dispatcher", "", "",
		fmt.Sprintf(`{"open_recovery_quarantines":%d,"reason":%q}`, openQuarantines, reason))
}

func (d *Dispatcher) shouldScanForCycles() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cachedQueueDepth == 0 || d.cachedIdleWorkers == 0 {
		return false
	}
	return d.lastCycleScanAt.IsZero() || d.nowFunc().Sub(d.lastCycleScanAt) >= d.cfg.CycleScanInterval
}

func (d *Dispatcher) scanDependencyCycles(ctx context.Context) {
	d.mu.Lock()
	d.lastCycleScanAt = d.nowFunc()
	d.mu.Unlock()

	cycles, err := d.beads.DependencyCycles(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "dependency_cycle_scan_failed", "dispatcher", "", "",
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return
	}
	for _, cycle := range cycles {
		d.escalateDependencyCycle(ctx, cycle)
	}
}

func (d *Dispatcher) escalateDependencyCycle(ctx context.Context, cycle beadstore.Cycle) {
	path := canonicalDependencyCyclePath(cycle)
	if len(path) < 2 {
		return
	}
	key := strings.Join(path, "\x00")
	d.mu.Lock()
	if d.escalatedCycles[key] {
		d.mu.Unlock()
		return
	}
	d.escalatedCycles[key] = true
	d.mu.Unlock()

	anchor := path[0]
	pathText := strings.Join(path, " -> ")
	msg := protocol.FormatEscalation(
		protocol.EscDependencyCycle,
		anchor,
		"blocking dependency cycle detected",
		fmt.Sprintf("Path: %s", pathText),
	)
	_ = d.beads.AppendJourney(ctx, anchor, beadstore.JourneyEvent{
		Ts:      d.nowFunc().UTC().Format(time.RFC3339Nano),
		Actor:   "dispatcher",
		Event:   "dependency_cycle_detected",
		Payload: fmt.Sprintf(`{"cycle_key":%q,"path":%q}`, key, pathText),
	})
	d.escalate(ctx, msg, anchor, "")
}

func canonicalDependencyCyclePath(cycle beadstore.Cycle) []string {
	if len(cycle) == 0 {
		return nil
	}
	nodes := append([]string(nil), cycle...)
	if len(nodes) > 1 && nodes[0] == nodes[len(nodes)-1] {
		nodes = nodes[:len(nodes)-1]
	}
	if len(nodes) == 0 {
		return nil
	}
	start := 0
	for i := 1; i < len(nodes); i++ {
		if nodes[i] < nodes[start] {
			start = i
		}
	}
	out := make([]string, 0, len(nodes)+1)
	for i := range nodes {
		out = append(out, nodes[(start+i)%len(nodes)])
	}
	out = append(out, out[0])
	return out
}

func (d *Dispatcher) reservedSpawnForTargets() (map[string]bool, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	targets := make(map[string]bool, len(d.pendingWorkerTargets))
	for _, target := range d.pendingWorkerTargets {
		if target != "" {
			targets[target] = true
		}
	}
	for _, worker := range d.workers {
		if worker.state == protocol.WorkerIdle && worker.targetBeadID != "" {
			targets[worker.targetBeadID] = true
		}
	}
	return targets, d.hasPendingSpawnForLocked()
}

func (d *Dispatcher) hasPendingSpawnForLocked() bool {
	for _, target := range d.pendingWorkerTargets {
		if target != "" {
			return true
		}
	}
	return false
}

// autoscaleInputsForIdleWorkers computes (queueDepth, idleCount) for autoscaling.
// reservedTargets contains bead IDs that are exclusively reserved for spawn-for workers
// (both pending and connected-idle). These beads must not inflate the autoscale queue
// depth because no general worker can claim them.
func autoscaleInputsForIdleWorkers(idle []idleWorker, beads []protocol.Bead, reservedTargets map[string]bool) (queueDepth, idleCount int) {
	if len(idle) == 0 {
		// No connected workers: count only beads that general workers can actually claim.
		// Excluding reserved spawn-for targets prevents autoscale from spawning general
		// workers for beads they can never take, which wastes worker slots.
		return countGeneralQueueDepth(beads, reservedTargets), 0
	}

	autoscaleIdle := 0
	targetedIdle := 0
	generalIdle := 0
	targets := make(map[string]bool)
	for _, candidate := range idle {
		if candidate.targetBeadID != "" {
			targets[candidate.targetBeadID] = true
		}
		if candidate.spawnFor {
			continue
		}
		autoscaleIdle++
		if candidate.targetBeadID == "" {
			generalIdle++
			continue
		}
		targetedIdle++
	}

	if autoscaleIdle == 0 {
		// All idle workers are spawn-for workers. Compute general queue depth excluding
		// both connected and pending spawn-for targets.
		return countGeneralQueueDepth(beads, targets, reservedTargets), 0
	}
	if targetedIdle == 0 || generalIdle > 0 {
		return len(beads), autoscaleIdle
	}

	generalQueueDepth := countGeneralQueueDepth(beads, targets, reservedTargets)
	if generalQueueDepth == 0 {
		return len(beads), autoscaleIdle
	}
	return targetedIdle + generalQueueDepth, 0
}

func countGeneralQueueDepth(beads []protocol.Bead, reservedSets ...map[string]bool) int {
	depth := 0
	for _, bead := range beads {
		if isReservedBead(bead.ID, reservedSets...) {
			continue
		}
		depth++
	}
	return depth
}

func isReservedBead(beadID string, reservedSets ...map[string]bool) bool {
	for _, reserved := range reservedSets {
		if reserved[beadID] {
			return true
		}
	}
	return false
}

func (d *Dispatcher) assignTargetedIdleWorkers(ctx context.Context, idle []idleWorker, beads []protocol.Bead, focusVersion uint64) map[string]bool {
	assignedBeads := make(map[string]bool)
	beadsByID := make(map[string]protocol.Bead, len(beads))
	for _, bead := range beads {
		beadsByID[bead.ID] = bead
	}

	for _, candidate := range idle {
		if candidate.targetBeadID == "" {
			continue
		}
		bead, ok := beadsByID[candidate.targetBeadID]
		if !ok {
			continue
		}
		_ = d.assignBead(ctx, candidate.worker, bead, focusVersion)
		d.mu.Lock()
		if candidate.worker.state != protocol.WorkerIdle {
			assignedBeads[bead.ID] = true
			candidate.worker.targetBeadID = ""
			delete(d.priorityBeads, bead.ID)
		}
		d.mu.Unlock()
	}
	return assignedBeads
}

// assignGeneralIdleWorkers starts assignment setup for each scheduling unit
// in turn and accumulates the completion handle from every launch. Nothing
// here awaits a handle — the caller decides whether and how to wait.
func (d *Dispatcher) assignGeneralIdleWorkers(ctx context.Context, idle []idleWorker, plan schedulingPlan, pbSnapshot, assignedBeads, reservedTargets map[string]bool, focusVersion uint64) []<-chan struct{} {
	// Assign beads to idle workers. Advance the idle cursor only when a worker is
	// actually claimed — epics skipped in assignBead leave the worker idle so the
	// next bead in the list can still be paired with it.
	//
	// Worktree creation can fetch and run several git commands. Start each
	// assignment after its worker is reserved, but do not wait for its setup to
	// finish here: safeGo tracks the background work for shutdown while allowing
	// the assignment loop to process later worker-ready signals immediately. The
	// per-launch completion handle is still collected and handed back up so
	// callers (tests, in particular) can opt into a bounded wait.
	idleIdx := 0
	var done []<-chan struct{}
	for _, unit := range plan.units {
		idleIdx = d.assignGeneralSchedulingUnit(ctx, idle, idleIdx, unit, pbSnapshot, assignedBeads, reservedTargets, focusVersion, &done)
	}
	return done
}

func (d *Dispatcher) assignGeneralSchedulingUnit(ctx context.Context, idle []idleWorker, idleIdx int, unit schedulingUnit, pbSnapshot, assignedBeads, reservedTargets map[string]bool, focusVersion uint64, done *[]<-chan struct{}) int {
	nextIdleIdx := idleIdx
	for _, bead := range unit.beads {
		if assignedBeads[bead.ID] {
			continue
		}
		if reservedTargets[bead.ID] {
			continue
		}
		nextIdleIdx = d.nextGeneralIdleIndex(idle, nextIdleIdx)
		if nextIdleIdx >= len(idle) {
			break
		}
		claimed, setupDone := d.launchAssignment(ctx, idle[nextIdleIdx].worker, bead, focusVersion)
		*done = append(*done, setupDone)
		if !claimed {
			continue
		}
		_, nextIdleIdx = d.advanceAssignedGeneralIdle(idle, nextIdleIdx, bead.ID, pbSnapshot)
	}
	return nextIdleIdx
}

// launchAssignment starts the slow assignment preparation in the background
// and waits only until assignBead has either reserved the worker or declined
// the candidate. The safeGo wrapper tracks slow setup for graceful shutdown;
// the returned done channel closes when that background setup finishes, but
// launchAssignment itself never waits on it — callers decide whether to.
func (d *Dispatcher) launchAssignment(ctx context.Context, w *trackedWorker, bead protocol.Bead, focusVersion uint64) (claimed bool, done <-chan struct{}) {
	claimedCh := make(chan bool, 1)
	setupDone := make(chan struct{})
	d.safeGo(func() {
		defer close(setupDone)
		_ = d.assignBeadWithClaim(ctx, w, bead, []uint64{focusVersion}, func(claimed bool) {
			claimedCh <- claimed
		})
	})
	return <-claimedCh, setupDone
}

func (d *Dispatcher) nextGeneralIdleIndex(idle []idleWorker, idleIdx int) int {
	for idleIdx < len(idle) {
		d.mu.Lock()
		isAssignableIdle := idle[idleIdx].worker.state == protocol.WorkerIdle &&
			idle[idleIdx].worker.targetBeadID == "" &&
			!idle[idleIdx].worker.spawnFor
		d.mu.Unlock()
		if isAssignableIdle {
			return idleIdx
		}
		idleIdx++
	}
	return idleIdx
}

func (d *Dispatcher) advanceAssignedGeneralIdle(idle []idleWorker, idleIdx int, beadID string, pbSnapshot map[string]bool) (claimed bool, nextIdleIdx int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	nextIdleIdx = idleIdx
	claimed = idle[idleIdx].worker.state != protocol.WorkerIdle
	if claimed {
		nextIdleIdx++
	}
	if pbSnapshot[beadID] {
		delete(d.priorityBeads, beadID)
	}
	return claimed, nextIdleIdx
}

// checkClosedBeadAssignments detects beads that have been closed externally
// while a worker is still assigned to them. For each such bead it clears the
// in-memory worker state, completes the DB assignment record, and sends a
// SHUTDOWN signal so the worker exits cleanly. Called on every assign-loop
// tick, ensuring cleanup occurs within one tick interval of external closure.
