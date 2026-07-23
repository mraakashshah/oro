package dispatcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"oro/pkg/factoryhealth"
)

// SwarmHealth is the dispatcher-facing alias for the shared factory health contract.
type SwarmHealth = factoryhealth.FactoryHealth

type factoryHealthInput struct {
	daemonRunning                bool
	daemonPID                    int
	dispatcherState              string
	pauseSource                  string
	pauseReason                  string
	workers                      []workerStatus
	queueDepth                   int
	targetWorkers                int
	maxWorkers                   int
	pendingWorkerCount           int
	pendingHandoffCount          int
	qgStatus                     QGFailureStatus
	openRecoveryQuarantines      int
	assignmentFrozenByQuarantine bool
	blockingRecoveryQuarantines  int
	assignmentFreezeReason       string
	progressTimeoutSecs          float64
	heartbeatTimeoutSecs         float64
	storage                      *factoryhealth.StorageHealth
}

// applyHealth returns the repo-owned FactoryHealth JSON contract.
func (d *Dispatcher) applyHealth() (string, error) {
	now := d.nowFunc()
	ctx := context.Background()
	storage := d.storageHealth(ctx)

	readyBeads, err := d.beads.Ready(ctx)
	if err != nil {
		readyBeads = nil
	}
	readyBeads = d.statusQueueBeads(ctx, readyBeads)
	qgStatus := d.qgFailureStatus(ctx)
	openRecoveryQuarantines, err := factoryhealth.LoadRecoveryQuarantineMetrics(ctx, d.db)
	if err != nil {
		_ = d.logEvent(ctx, "factory_health_recovery_quarantine_load_failed", "dispatcher", "", "", err.Error())
	}

	d.mu.Lock()
	workers, _, _, _ := d.snapshotWorkers(now)
	queueDepth := calculateLiveQueueDepth(readyBeads, d.workers)
	input := factoryHealthInput{
		daemonRunning:                true,
		daemonPID:                    os.Getpid(),
		dispatcherState:              string(d.state),
		pauseSource:                  d.pauseSource,
		pauseReason:                  d.pauseReason,
		workers:                      workers,
		queueDepth:                   queueDepth,
		targetWorkers:                d.targetWorkers,
		maxWorkers:                   d.cfg.MaxWorkers,
		pendingWorkerCount:           len(d.pendingManagedIDs) + len(d.pendingExternalIDs),
		pendingHandoffCount:          len(d.pendingHandoffs),
		qgStatus:                     qgStatus,
		openRecoveryQuarantines:      openRecoveryQuarantines,
		assignmentFrozenByQuarantine: d.assignmentFrozenByQuarantine,
		blockingRecoveryQuarantines:  d.blockingRecoveryQuarantines,
		assignmentFreezeReason:       d.assignmentFreezeReason,
		progressTimeoutSecs:          d.cfg.ProgressTimeout.Seconds(),
		heartbeatTimeoutSecs:         d.cfg.HeartbeatTimeout.Seconds(),
		storage:                      storage,
	}
	d.mu.Unlock()

	health := d.evaluateFactoryHealth(ctx, now, input)
	data, err := json.Marshal(health)
	if err != nil {
		return "", fmt.Errorf("marshal health: %w", err)
	}
	return string(data), nil
}

func (d *Dispatcher) evaluateFactoryHealth(ctx context.Context, now time.Time, input factoryHealthInput) factoryhealth.FactoryHealth {
	activeAssignments, err := factoryhealth.LoadActiveAssignments(ctx, d.db, now)
	if err != nil {
		_ = d.logEvent(ctx, "factory_health_assignment_load_failed", "dispatcher", "", "", err.Error())
	}
	throughput, err := factoryhealth.LoadThroughputMetrics(ctx, d.db, now, 30*time.Minute)
	if err != nil {
		_ = d.logEvent(ctx, "factory_health_throughput_load_failed", "dispatcher", "", "", err.Error())
	}
	opsRuns, err := LoadOpsRunMetrics(ctx, d.db, now)
	if err != nil {
		_ = d.logEvent(ctx, "factory_health_ops_runs_load_failed", "dispatcher", "", "", err.Error())
	}
	pendingEscalations, err := factoryhealth.LoadPendingEscalationMetrics(ctx, d.db, now)
	if err != nil {
		_ = d.logEvent(ctx, "factory_health_pending_escalations_load_failed", "dispatcher", "", "", err.Error())
	}
	qgFingerprints := input.qgStatus.RecentFingerprints
	if len(qgFingerprints) == 0 {
		qgFingerprints = input.qgStatus.TopFingerprints
	}
	return factoryhealth.Evaluate(factoryhealth.Snapshot{
		DaemonRunning:                input.daemonRunning,
		DaemonPID:                    input.daemonPID,
		DispatcherState:              input.dispatcherState,
		PauseSource:                  input.pauseSource,
		PauseReason:                  input.pauseReason,
		Workers:                      toFactoryWorkers(input.workers),
		ReadyQueue:                   input.queueDepth,
		TargetWorkers:                input.targetWorkers,
		MaxWorkers:                   input.maxWorkers,
		PendingWorkerCount:           input.pendingWorkerCount,
		PendingHandoffCount:          input.pendingHandoffCount,
		ActiveAssignments:            activeAssignments,
		OpenQGIncidents:              input.qgStatus.OpenIncidents,
		QGOccurrences30m:             input.qgStatus.Occurrences30m,
		QGTopFingerprints:            qgFingerprints,
		OpenRecoveryQuarantines:      input.openRecoveryQuarantines,
		AssignmentFrozenByQuarantine: input.assignmentFrozenByQuarantine,
		BlockingRecoveryQuarantines:  input.blockingRecoveryQuarantines,
		AssignmentFreezeReason:       input.assignmentFreezeReason,
		ProgressTimeoutSecs:          input.progressTimeoutSecs,
		HeartbeatTimeoutSecs:         input.heartbeatTimeoutSecs,
		Throughput:                   throughput,
		OpsRuns:                      opsRuns,
		PendingEscalations:           pendingEscalations,
		Storage:                      input.storage,
	})
}

func (d *Dispatcher) storageHealth(ctx context.Context) *factoryhealth.StorageHealth {
	if d.cfg.StorageHealth == nil {
		return nil
	}
	return d.cfg.StorageHealth(ctx)
}

// LoadOpsRunMetrics reads health-relevant ops run counts from the state database.
func LoadOpsRunMetrics(ctx context.Context, db *sql.DB, now time.Time) (factoryhealth.OpsRunMetrics, error) {
	metrics, err := factoryhealth.LoadOpsRunMetrics(ctx, db, now)
	if err != nil {
		return metrics, fmt.Errorf("load ops run metrics: %w", err)
	}
	return metrics, nil
}

func toFactoryWorkers(workers []workerStatus) []factoryhealth.WorkerSnapshot {
	out := make([]factoryhealth.WorkerSnapshot, 0, len(workers))
	for _, worker := range workers {
		out = append(out, factoryhealth.WorkerSnapshot{
			ID:                worker.ID,
			State:             worker.State,
			BeadID:            worker.BeadID,
			LastProgressSecs:  worker.LastProgressSecs,
			LastHeartbeatSecs: worker.LastHeartbeatSecs,
			Managed:           worker.Managed,
		})
	}
	return out
}
