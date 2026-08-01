package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"oro/pkg/factoryhealth"
	"oro/pkg/protocol"
	"os"
	"strings"
	"time"
)

func (d *Dispatcher) snapshotWorkers(now time.Time) (workers []workerStatus, assignments map[string]string, active, idle int) {
	assignments = make(map[string]string, len(d.workers))
	workers = make([]workerStatus, 0, len(d.workers))
	for id, w := range d.workers {
		if w.beadID != "" {
			assignments[id] = w.beadID
		}
		var progressSecs float64
		if !w.lastProgress.IsZero() {
			progressSecs = now.Sub(w.lastProgress).Seconds()
		}
		var heartbeatSecs float64
		if !w.lastSeen.IsZero() {
			heartbeatSecs = now.Sub(w.lastSeen).Seconds()
		}
		workers = append(workers, workerStatus{
			ID:                id,
			State:             string(w.state),
			BeadID:            w.beadID,
			LastProgressSecs:  progressSecs,
			LastHeartbeatSecs: heartbeatSecs,
			ContextPct:        w.contextPct,
			Managed:           w.managed,
			SpawnFor:          w.spawnFor,
			TargetBeadID:      w.targetBeadID,
		})
		if w.state == protocol.WorkerBusy || w.state == protocol.WorkerReserved {
			active++
		} else {
			idle++
		}
	}
	return workers, assignments, active, idle
}

// calculateLiveQueueDepth returns the count of ready beads that are not assigned to workers.
func calculateLiveQueueDepth(readyBeads []protocol.Bead, workers map[string]*trackedWorker) int {
	// Build set of assigned bead IDs.
	assignedBeadIDs := make(map[string]bool)
	for _, w := range workers {
		if w.beadID != "" {
			assignedBeadIDs[w.beadID] = true
		}
	}

	// Count ready beads that are not assigned. Childless epics are executable
	// decomposition work, so status must not blanket-filter epic types here.
	queueDepth := 0
	for _, bead := range readyBeads {
		if !assignedBeadIDs[bead.ID] {
			queueDepth++
		}
	}
	return queueDepth
}

func (d *Dispatcher) statusQueueBeads(ctx context.Context, readyBeads []protocol.Bead) []protocol.Bead {
	if len(readyBeads) == 0 {
		return readyBeads
	}
	queueBeads := make([]protocol.Bead, 0, len(readyBeads))
	for _, bead := range readyBeads {
		if strings.EqualFold(bead.Type, "epic") {
			hasChildren, err := d.beads.HasChildren(ctx, bead.ID)
			if err != nil || hasChildren {
				continue
			}
		}
		queueBeads = append(queueBeads, bead)
	}
	return queueBeads
}

// qgFailureStatus queries the state DB and returns a snapshot of open QG
// failure incidents. On DB error it logs to stderr and returns a zero value.
func (d *Dispatcher) qgFailureStatus(ctx context.Context) QGFailureStatus {
	if d.db == nil {
		return QGFailureStatus{}
	}

	openRows, err := d.openQGIncidentRows(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "qgFailureStatus: open incidents: %v\n", err)
		return QGFailureStatus{}
	}
	openRows = d.filterClosedQGIncidentBeads(ctx, openRows)

	var occ30m int
	if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM qg_failure_occurrences
 WHERE created_at >= datetime('now', '-30 minutes')`,
	).Scan(&occ30m); err != nil {
		fmt.Fprintf(os.Stderr, "qgFailureStatus: count occurrences 30m: %v\n", err)
		return QGFailureStatus{}
	}

	rows, err := d.db.QueryContext(ctx, `
SELECT id, fingerprint FROM qg_failure_incidents
 WHERE status = 'open'
 ORDER BY occurrence_count DESC
 LIMIT 5`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "qgFailureStatus: top fingerprints: %v\n", err)
		return QGFailureStatus{}
	}
	defer func() { _ = rows.Close() }()

	var fps []string
	for rows.Next() {
		var row qgIncidentStatusRow
		if err := rows.Scan(&row.ID, &row.Fingerprint); err != nil {
			fmt.Fprintf(os.Stderr, "qgFailureStatus: scan fingerprint: %v\n", err)
			return QGFailureStatus{}
		}
		if d.qgIncidentBeadClosed(ctx, row.ID) {
			_ = d.closeQGIncidentRow(ctx, row.ID)
			continue
		}
		fps = append(fps, row.Fingerprint)
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "qgFailureStatus: rows error: %v\n", err)
		return QGFailureStatus{}
	}
	recentFingerprints, err := factoryhealth.LoadRecentQGFingerprints(ctx, d.db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "qgFailureStatus: recent fingerprints: %v\n", err)
	}

	return QGFailureStatus{
		OpenIncidents:      len(openRows),
		Occurrences30m:     occ30m,
		TopFingerprints:    fps,
		RecentFingerprints: recentFingerprints,
	}
}

func (d *Dispatcher) openQGIncidentRows(ctx context.Context) ([]qgIncidentStatusRow, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, fingerprint FROM qg_failure_incidents
 WHERE status = 'open'
 ORDER BY occurrence_count DESC`)
	if err != nil {
		return nil, fmt.Errorf("query open qg incidents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []qgIncidentStatusRow
	for rows.Next() {
		var row qgIncidentStatusRow
		if err := rows.Scan(&row.ID, &row.Fingerprint); err != nil {
			return nil, fmt.Errorf("scan open qg incident: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open qg incidents: %w", err)
	}
	return out, nil
}

func (d *Dispatcher) filterClosedQGIncidentBeads(ctx context.Context, rows []qgIncidentStatusRow) []qgIncidentStatusRow {
	if len(rows) == 0 {
		return rows
	}
	open := rows[:0]
	for _, row := range rows {
		if d.qgIncidentBeadClosed(ctx, row.ID) {
			_ = d.closeQGIncidentRow(ctx, row.ID)
			continue
		}
		open = append(open, row)
	}
	return open
}

func (d *Dispatcher) qgIncidentBeadClosed(ctx context.Context, incidentID int64) bool {
	if d.beads == nil || incidentID <= 0 {
		return false
	}
	detail, err := d.beads.Show(ctx, fmt.Sprintf("oro-qg-incident-%d", incidentID))
	if err != nil {
		var notFound *protocol.BeadNotFoundError
		return errors.As(err, &notFound)
	}
	if detail == nil {
		return true
	}
	if detail.Status == "closed" {
		return true
	}
	return false
}

func (d *Dispatcher) closeQGIncidentRow(ctx context.Context, incidentID int64) error {
	_, err := d.db.ExecContext(ctx, `
UPDATE qg_failure_incidents
   SET status = 'closed'
 WHERE id = ? AND status = 'open'`, incidentID)
	if err != nil {
		return fmt.Errorf("close qg incident row: %w", err)
	}
	return nil
}

func (d *Dispatcher) buildStatusJSON() string {
	ctx := context.Background()
	return d.buildStatusJSONWithStorage(ctx, d.storageHealth(ctx))
}

//nolint:funlen // Status JSON intentionally assembles one wire contract in field order.
func (d *Dispatcher) buildStatusJSONWithStorage(ctx context.Context, storageHealth *factoryhealth.StorageHealth) string {
	now := d.nowFunc()

	// Fetch ready beads to determine which attempt counts are valid.
	readyBeads, err := d.beads.Ready(ctx)
	if err != nil {
		readyBeads = nil // Continue with empty ready list on error.
	}
	readyBeads = d.statusQueueBeads(ctx, readyBeads)

	qgStatus := d.qgFailureStatus(ctx)
	openRecoveryQuarantines, err := factoryhealth.LoadRecoveryQuarantineMetrics(ctx, d.db)
	if err != nil {
		_ = d.logEvent(ctx, "status_recovery_quarantine_load_failed", "dispatcher", "", "", err.Error())
	}

	d.mu.Lock()
	workers, assignments, activeCount, idleCount := d.snapshotWorkers(now)

	// Calculate live queue depth (ready beads minus assigned beads).
	queueDepth := calculateLiveQueueDepth(readyBeads, d.workers)
	managedCount, unmanagedCount := workerRoleCounts(d.workers)

	// Build set of active bead IDs (assigned to workers OR in ready queue).
	activeBeadIDs := make(map[string]bool)
	for _, w := range d.workers {
		if w.beadID != "" {
			activeBeadIDs[w.beadID] = true
		}
	}
	for _, bead := range readyBeads {
		activeBeadIDs[bead.ID] = true
	}

	// Filter attempt counts to only include active beads.
	attemptCounts := filterAttemptCounts(d.attemptCounts, activeBeadIDs)

	resp := statusResponse{
		State:                        string(d.state),
		PID:                          os.Getpid(),
		WorkerCount:                  len(d.workers),
		QueueDepth:                   queueDepth,
		Assignments:                  assignments,
		FocusedEpic:                  d.focusedEpic,
		Workers:                      workers,
		ActiveCount:                  activeCount,
		IdleCount:                    idleCount,
		TargetCount:                  d.targetWorkers,
		MaxWorkers:                   d.cfg.MaxWorkers,
		ManagedCount:                 managedCount,
		UnmanagedCount:               unmanagedCount,
		PendingWorkerCount:           len(d.pendingManagedIDs) + len(d.pendingExternalIDs),
		UptimeSeconds:                now.Sub(d.startTime).Seconds(),
		PendingHandoffCount:          len(d.pendingHandoffs),
		AttemptCounts:                attemptCounts,
		ProgressTimeoutSecs:          d.cfg.ProgressTimeout.Seconds(),
		QGFailureIncidentsOpen:       qgStatus.OpenIncidents,
		QGFailureOccurrences30m:      qgStatus.Occurrences30m,
		QGFailureTopFingerprints:     qgStatus.TopFingerprints,
		AssignmentFrozenByQuarantine: d.assignmentFrozenByQuarantine,
		BlockingRecoveryQuarantines:  d.blockingRecoveryQuarantines,
		AssignmentFreezeReason:       d.assignmentFreezeReason,
	}
	state := string(d.state)
	targetWorkers := d.targetWorkers
	maxWorkers := d.cfg.MaxWorkers
	pendingWorkerCount := len(d.pendingManagedIDs) + len(d.pendingExternalIDs)
	pendingHandoffCount := len(d.pendingHandoffs)
	progressTimeoutSecs := d.cfg.ProgressTimeout.Seconds()
	heartbeatTimeoutSecs := d.cfg.HeartbeatTimeout.Seconds()
	assignmentFrozenByQuarantine := d.assignmentFrozenByQuarantine
	blockingRecoveryQuarantines := d.blockingRecoveryQuarantines
	assignmentFreezeReason := d.assignmentFreezeReason
	d.mu.Unlock()

	health := d.evaluateFactoryHealth(ctx, now, factoryHealthInput{
		daemonRunning:                true,
		daemonPID:                    os.Getpid(),
		dispatcherState:              state,
		workers:                      workers,
		queueDepth:                   queueDepth,
		targetWorkers:                targetWorkers,
		maxWorkers:                   maxWorkers,
		pendingWorkerCount:           pendingWorkerCount,
		pendingHandoffCount:          pendingHandoffCount,
		qgStatus:                     qgStatus,
		openRecoveryQuarantines:      openRecoveryQuarantines,
		assignmentFrozenByQuarantine: assignmentFrozenByQuarantine,
		blockingRecoveryQuarantines:  blockingRecoveryQuarantines,
		assignmentFreezeReason:       assignmentFreezeReason,
		progressTimeoutSecs:          progressTimeoutSecs,
		heartbeatTimeoutSecs:         heartbeatTimeoutSecs,
		storage:                      storageHealth,
	})
	resp.Health = &health

	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(data)
}

func workerRoleCounts(workers map[string]*trackedWorker) (managedCount, unmanagedCount int) {
	for _, w := range workers {
		if w.managed {
			managedCount++
		} else {
			unmanagedCount++
		}
	}
	return managedCount, unmanagedCount
}

func filterAttemptCounts(attemptCounts map[string]int, activeBeadIDs map[string]bool) map[string]int {
	if len(attemptCounts) == 0 {
		return nil
	}
	filtered := make(map[string]int)
	for beadID, count := range attemptCounts {
		if activeBeadIDs[beadID] {
			filtered[beadID] = count
		}
	}
	return filtered
}

// applyScaleDirective parses the target count from args, stores it, and
// calls reconcileScale. Returns the ACK detail string.
