// Package factoryhealth defines the shared Oro factory health contract and
// pure evaluator used by the dispatcher and CLI.
package factoryhealth

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/storage"
)

// State is the top-level factory health state.
type State string

// Factory health states.
const (
	StateHealthy  State = "healthy"
	StateDegraded State = "degraded"
	StateStalled  State = "stalled"
	StateUnsafe   State = "unsafe"
	StateStopped  State = "stopped"
)

// Severity ranks the operator impact of a finding.
type Severity string

// Finding severities.
const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

// Canonical v1 factory health finding codes.
const (
	FindingDaemonStoppedWithActiveAssignments = "daemon_stopped_with_active_assignments"
	FindingOrphanActiveAssignment             = "orphan_active_assignment"
	FindingAssignmentBeadStatusMismatch       = "assignment_bead_status_mismatch"
	FindingIdleReadyQueue                     = "idle_ready_queue"
	FindingPausedWithReadyQueue               = "paused_with_ready_queue"
	FindingAliveNoProgress                    = "alive_no_progress"
	FindingThroughputStall                    = "throughput_stall"
	FindingQGIncidentsOpen                    = "qg_incidents_open"
	FindingQGIncidentIncrease                 = "qg_incident_increase"
	FindingRecoveryQuarantineOpen             = "recovery_quarantine_open"
	FindingAssignmentFrozenByQuarantine       = "assignment_frozen_by_quarantine"
	FindingEpicBranchAdmissionBlocked         = "epic_branch_admission_blocked"
	FindingOpsRunFailed                       = "ops_run_failed"
	FindingOpsRunStale                        = "ops_run_stale"
	FindingPendingEscalationUnrouted          = "pending_escalation_unrouted"
	FindingStorageUnavailable                 = "storage_unavailable"
	FindingStoragePressure                    = "storage_pressure"
	FindingStorageSweepOverdue                = "storage_sweep_overdue"
	FindingStorageRetirementBlocked           = "storage_retirement_blocked"
	FindingStorageFailure                     = "storage_failure"
	FindingStorageCancellation                = "storage_cancellation"
	FindingStorageAdmissionPaused             = "storage_admission_paused"
)

// OpsRunStaleAfter is the age threshold after which a running ops run is
// reported as stale in factory health.
const OpsRunStaleAfter = 30 * time.Minute

// FactoryHealth is the JSON contract emitted by `oro health --json`.
type FactoryHealth struct {
	State    State     `json:"state"`
	Posture  string    `json:"posture"`
	Findings []Finding `json:"findings"`
	Metrics  Metrics   `json:"metrics"`
}

// Finding describes one actionable factory health problem.
type Finding struct {
	Code              string   `json:"code"`
	Severity          Severity `json:"severity"`
	Component         string   `json:"component"`
	Message           string   `json:"message"`
	WorkerID          string   `json:"worker_id,omitempty"`
	BeadID            string   `json:"bead_id,omitempty"`
	Type              string   `json:"type,omitempty"`
	Fingerprint       string   `json:"fingerprint,omitempty"`
	AgeSecs           float64  `json:"age_secs,omitempty"`
	RecommendedAction string   `json:"recommended_action,omitempty"`
}

// Metrics holds the numeric inputs used to evaluate factory health.
type Metrics struct {
	DaemonRunning                bool              `json:"daemon_running"`
	DaemonPID                    int               `json:"daemon_pid,omitempty"`
	DispatcherState              string            `json:"dispatcher_state,omitempty"`
	PauseSource                  string            `json:"pause_source,omitempty"`
	PauseReason                  string            `json:"pause_reason,omitempty"`
	WorkerCount                  int               `json:"worker_count"`
	ActiveWorkers                int               `json:"active_workers"`
	IdleWorkers                  int               `json:"idle_workers"`
	TargetWorkers                int               `json:"target_workers"`
	MaxWorkers                   int               `json:"max_workers"`
	ReadyQueue                   int               `json:"ready_queue"`
	ActiveAssignments            int               `json:"active_assignments"`
	OrphanAssignments            int               `json:"orphan_assignments"`
	OpenQGIncidents              int               `json:"open_qg_incidents"`
	QGOccurrences30m             int               `json:"qg_occurrences_30m"`
	OpenRecoveryQuarantines      int               `json:"recovery_quarantines_open"`
	EpicBranchBlocksOpen         int               `json:"epic_branch_blocks_open"`
	EpicBranchLeasesActive       int               `json:"epic_branch_leases_active"`
	AssignmentFrozenByQuarantine bool              `json:"assignment_frozen_by_quarantine"`
	BlockingRecoveryQuarantines  int               `json:"blocking_recovery_quarantines,omitempty"`
	AssignmentFreezeReason       string            `json:"assignment_freeze_reason,omitempty"`
	ThroughputWindow             ThroughputMetrics `json:"throughput_window"`
	OpsRuns                      OpsRunMetrics     `json:"ops_runs"`
	PendingEscalations           EscalationMetrics `json:"pending_escalations"`
	Storage                      *StorageHealth    `json:"storage,omitempty"`
	PendingWorkerCount           int               `json:"pending_worker_count,omitempty"`
	PendingHandoffCount          int               `json:"pending_handoff_count,omitempty"`
}

// EpicBranchAdmissionMetrics summarizes branch-scoped dispatcher admission state.
type EpicBranchAdmissionMetrics struct {
	Blocked      int
	ActiveLeases int
}

// ThroughputMetrics summarizes recent assignment and closure activity.
type ThroughputMetrics struct {
	WindowSecs         float64 `json:"window_secs"`
	AssignmentsStarted int     `json:"assignments_started"`
	ProductiveClosures int     `json:"productive_closures"`
	ProgressTimeouts   int     `json:"progress_timeouts"`
	LastEventAgeSecs   float64 `json:"last_event_age_secs,omitempty"`
}

// OpsRunMetrics summarizes active and blocking ops subprocess runs.
type OpsRunMetrics struct {
	Running int                          `json:"running"`
	Failed  int                          `json:"failed"`
	Stale   int                          `json:"stale"`
	ByType  map[string]OpsRunTypeMetrics `json:"by_type,omitempty"`
	Runs    []OpsRunSnapshot             `json:"runs,omitempty"`
}

// OpsRunTypeMetrics counts ops runs of one type by health-relevant status.
type OpsRunTypeMetrics struct {
	Running int `json:"running,omitempty"`
	Failed  int `json:"failed,omitempty"`
	Stale   int `json:"stale,omitempty"`
}

// OpsRunSnapshot is a health-relevant ops subprocess sample.
type OpsRunSnapshot struct {
	ID      int64   `json:"id,omitempty"`
	Type    string  `json:"type"`
	BeadID  string  `json:"bead_id,omitempty"`
	Status  string  `json:"status"`
	AgeSecs float64 `json:"age_secs,omitempty"`
}

// EscalationMetrics summarizes pending escalations that need operator or
// routing attention.
type EscalationMetrics struct {
	Unrouted    int                  `json:"unrouted"`
	Escalations []EscalationSnapshot `json:"escalations,omitempty"`
}

// EscalationSnapshot is a health-relevant pending escalation sample.
type EscalationSnapshot struct {
	ID       int64   `json:"id,omitempty"`
	Type     string  `json:"type"`
	BeadID   string  `json:"bead_id,omitempty"`
	WorkerID string  `json:"worker_id,omitempty"`
	AgeSecs  float64 `json:"age_secs,omitempty"`
}

// StorageHealth summarizes the storage control plane observed by a health
// caller. A nil snapshot means storage health could not be observed.
type StorageHealth struct {
	Available          bool                      `json:"available"`
	Pressure           string                    `json:"pressure,omitempty"`
	SweepOverdue       bool                      `json:"sweep_overdue,omitempty"`
	BlockedRetirements int                       `json:"blocked_retirements,omitempty"`
	Failures           []string                  `json:"failures,omitempty"`
	Cancellations      []string                  `json:"cancellations,omitempty"`
	AdmissionPaused    bool                      `json:"admission_paused,omitempty"`
	DevCleanup         *storage.DevCleanupHealth `json:"dev_cleanup,omitempty"`
}

// WorkerSnapshot is a worker state sample used by the evaluator.
type WorkerSnapshot struct {
	ID                string
	State             string
	BeadID            string
	LastProgressSecs  float64
	LastHeartbeatSecs float64
	Managed           bool
}

// AssignmentSnapshot is an active assignment state sample used by the evaluator.
type AssignmentSnapshot struct {
	ID         int64
	BeadID     string
	WorkerID   string
	BeadStatus string
	AgeSecs    float64
}

// Snapshot contains all observed inputs for one health evaluation.
type Snapshot struct {
	DaemonRunning                bool
	DaemonPID                    int
	DispatcherState              string
	PauseSource                  string
	PauseReason                  string
	Workers                      []WorkerSnapshot
	ReadyQueue                   int
	TargetWorkers                int
	MaxWorkers                   int
	PendingWorkerCount           int
	PendingHandoffCount          int
	ActiveAssignments            []AssignmentSnapshot
	OpenQGIncidents              int
	QGOccurrences30m             int
	QGTopFingerprints            []string
	OpenRecoveryQuarantines      int
	EpicBranchAdmissions         EpicBranchAdmissionMetrics
	AssignmentFrozenByQuarantine bool
	BlockingRecoveryQuarantines  int
	AssignmentFreezeReason       string
	ProgressTimeoutSecs          float64
	HeartbeatTimeoutSecs         float64
	Throughput                   ThroughputMetrics
	OpsRuns                      OpsRunMetrics
	PendingEscalations           EscalationMetrics
	Storage                      *StorageHealth
}

// Evaluate converts an observed snapshot into the FactoryHealth contract.
func Evaluate(snapshot Snapshot) FactoryHealth {
	metrics := metricsFromSnapshot(snapshot)
	findings := evaluateFindings(snapshot, &metrics)
	state := stateFromFindings(snapshot, findings)
	return FactoryHealth{
		State:    state,
		Posture:  postureFor(state, findings),
		Findings: findings,
		Metrics:  metrics,
	}
}

func metricsFromSnapshot(snapshot Snapshot) Metrics {
	metrics := Metrics{
		DaemonRunning:                snapshot.DaemonRunning,
		DaemonPID:                    snapshot.DaemonPID,
		DispatcherState:              snapshot.DispatcherState,
		PauseSource:                  snapshot.PauseSource,
		PauseReason:                  snapshot.PauseReason,
		WorkerCount:                  len(snapshot.Workers),
		ReadyQueue:                   snapshot.ReadyQueue,
		TargetWorkers:                snapshot.TargetWorkers,
		MaxWorkers:                   snapshot.MaxWorkers,
		ActiveAssignments:            len(snapshot.ActiveAssignments),
		OpenQGIncidents:              snapshot.OpenQGIncidents,
		QGOccurrences30m:             snapshot.QGOccurrences30m,
		OpenRecoveryQuarantines:      snapshot.OpenRecoveryQuarantines,
		EpicBranchBlocksOpen:         snapshot.EpicBranchAdmissions.Blocked,
		EpicBranchLeasesActive:       snapshot.EpicBranchAdmissions.ActiveLeases,
		AssignmentFrozenByQuarantine: snapshot.AssignmentFrozenByQuarantine,
		BlockingRecoveryQuarantines:  snapshot.BlockingRecoveryQuarantines,
		AssignmentFreezeReason:       snapshot.AssignmentFreezeReason,
		ThroughputWindow:             snapshot.Throughput,
		OpsRuns:                      snapshot.OpsRuns,
		PendingEscalations:           snapshot.PendingEscalations,
		Storage:                      snapshot.Storage,
		PendingWorkerCount:           snapshot.PendingWorkerCount,
		PendingHandoffCount:          snapshot.PendingHandoffCount,
	}
	for _, worker := range snapshot.Workers {
		if activeWorkerState(worker.State) {
			metrics.ActiveWorkers++
		} else {
			metrics.IdleWorkers++
		}
	}
	return metrics
}

//nolint:gocognit,gocyclo,funlen // Canonical finding policy is clearer as one ordered rule list.
func evaluateFindings(snapshot Snapshot, metrics *Metrics) []Finding {
	var findings []Finding
	if snapshot.OpenRecoveryQuarantines > 0 {
		findings = append(findings, Finding{
			Code:              FindingRecoveryQuarantineOpen,
			Severity:          SeverityCritical,
			Component:         "recovery",
			Message:           fmt.Sprintf("%d recovery quarantine(s) are open", snapshot.OpenRecoveryQuarantines),
			RecommendedAction: "run oro health --json, inspect recovery_quarantines and preserved worktrees/branches, then resolve after preserving or merging work",
		})
	}
	if snapshot.EpicBranchAdmissions.Blocked > 0 {
		findings = append(findings, Finding{
			Code:              FindingEpicBranchAdmissionBlocked,
			Severity:          SeverityWarning,
			Component:         "epic_branch",
			Message:           fmt.Sprintf("%d epic branch admission block(s) are open", snapshot.EpicBranchAdmissions.Blocked),
			RecommendedAction: "run oro epic-branch list --json, then resolve a branch only after fresh Git inspection proves it is safe",
		})
	}
	if snapshot.AssignmentFrozenByQuarantine {
		findings = append(findings, Finding{
			Code:              FindingAssignmentFrozenByQuarantine,
			Severity:          SeverityCritical,
			Component:         "dispatcher",
			Message:           fmt.Sprintf("assignment is frozen by %d recovery quarantine(s) while %d worker(s) are idle (reason: %s)", snapshot.BlockingRecoveryQuarantines, metrics.IdleWorkers, snapshot.AssignmentFreezeReason),
			RecommendedAction: "inspect recovery quarantines and resolve or safely redeploy the preserved work before resuming ordinary assignment",
		})
	}
	findings = append(findings, opsRunFindings(snapshot.OpsRuns)...)
	findings = append(findings, pendingEscalationFindings(snapshot.PendingEscalations)...)
	findings = append(findings, evaluateStorageFindings(snapshot)...)
	if !snapshot.DaemonRunning {
		if len(snapshot.ActiveAssignments) > 0 {
			findings = append(findings, Finding{
				Code:              FindingDaemonStoppedWithActiveAssignments,
				Severity:          SeverityCritical,
				Component:         "daemon",
				Message:           fmt.Sprintf("daemon is stopped while %d assignment(s) remain active", len(snapshot.ActiveAssignments)),
				RecommendedAction: "run oro health --json, inspect active assignments, and requeue or complete them before restart",
			})
		}
		return findings
	}

	progressTimeout := defaultFloat(snapshot.ProgressTimeoutSecs, 600)
	heartbeatTimeout := defaultFloat(snapshot.HeartbeatTimeoutSecs, 45)
	for _, worker := range snapshot.Workers {
		if worker.State != "busy" || worker.BeadID == "" {
			continue
		}
		if worker.LastProgressSecs >= progressTimeout && worker.LastHeartbeatSecs > 0 && worker.LastHeartbeatSecs < heartbeatTimeout {
			findings = append(findings, Finding{
				Code:              FindingAliveNoProgress,
				Severity:          SeverityError,
				Component:         "worker",
				Message:           fmt.Sprintf("%s is heartbeating but has not reported progress for %.0fs", worker.ID, worker.LastProgressSecs),
				WorkerID:          worker.ID,
				BeadID:            worker.BeadID,
				AgeSecs:           worker.LastProgressSecs,
				RecommendedAction: "inspect worker logs; restart or preempt the worker if the same finding repeats",
			})
		}
	}

	if strings.EqualFold(snapshot.DispatcherState, "paused") && snapshot.ReadyQueue > 0 {
		findings = append(findings, Finding{
			Code:              FindingPausedWithReadyQueue,
			Severity:          SeverityWarning,
			Component:         "dispatcher",
			Message:           fmt.Sprintf("dispatcher is paused with %d ready task(s)", snapshot.ReadyQueue),
			RecommendedAction: "run oro directive resume when ready to assign work",
		})
	} else if strings.EqualFold(snapshot.DispatcherState, "running") && snapshot.ReadyQueue > 0 && metrics.IdleWorkers > 0 {
		findings = append(findings, Finding{
			Code:              FindingIdleReadyQueue,
			Severity:          SeverityError,
			Component:         "dispatcher",
			Message:           fmt.Sprintf("%d worker(s) idle while %d task(s) are ready", metrics.IdleWorkers, snapshot.ReadyQueue),
			RecommendedAction: "wait one monitor interval; restart the dispatcher only if the finding repeats",
		})
	}

	workerAssignments := assignedWorkerPairs(snapshot.Workers)
	for _, assignment := range snapshot.ActiveAssignments {
		if !workerAssignments[assignment.WorkerID+"\x00"+assignment.BeadID] {
			metrics.OrphanAssignments++
			findings = append(findings, Finding{
				Code:              FindingOrphanActiveAssignment,
				Severity:          SeverityCritical,
				Component:         "assignment",
				Message:           "active assignment has no matching live worker",
				WorkerID:          assignment.WorkerID,
				BeadID:            assignment.BeadID,
				AgeSecs:           assignment.AgeSecs,
				RecommendedAction: "complete the assignment row after resetting the bead to open",
			})
		}
		if assignment.BeadStatus != "" && assignment.BeadStatus != "in_progress" {
			findings = append(findings, Finding{
				Code:              FindingAssignmentBeadStatusMismatch,
				Severity:          SeverityCritical,
				Component:         "assignment",
				Message:           fmt.Sprintf("active assignment points at bead status %q", assignment.BeadStatus),
				WorkerID:          assignment.WorkerID,
				BeadID:            assignment.BeadID,
				AgeSecs:           assignment.AgeSecs,
				RecommendedAction: "make assignment state and bead state agree before assigning more work",
			})
		}
	}

	if snapshot.OpenQGIncidents > 0 {
		findings = append(findings, Finding{
			Code:              FindingQGIncidentsOpen,
			Severity:          SeverityWarning,
			Component:         "quality_gate",
			Message:           fmt.Sprintf("%d quality gate incident(s) are open", snapshot.OpenQGIncidents),
			RecommendedAction: "inspect oro logs for qg_failure_classified events and close or fix incident beads",
		})
	}
	if snapshot.QGOccurrences30m > snapshot.OpenQGIncidents && snapshot.QGOccurrences30m > 0 {
		finding := Finding{
			Code:              FindingQGIncidentIncrease,
			Severity:          SeverityWarning,
			Component:         "quality_gate",
			Message:           fmt.Sprintf("%d quality gate occurrence(s) in the last 30 minutes", snapshot.QGOccurrences30m),
			RecommendedAction: "pause assignment churn if the same fingerprint keeps increasing",
		}
		if len(snapshot.QGTopFingerprints) > 0 {
			finding.Fingerprint = snapshot.QGTopFingerprints[0]
		}
		findings = append(findings, finding)
	}

	if throughputStalled(snapshot.Throughput) {
		findings = append(findings, Finding{
			Code:              FindingThroughputStall,
			Severity:          SeverityError,
			Component:         "throughput",
			Message:           "recent assignments are not producing closures",
			RecommendedAction: "inspect worker logs and restart the dispatcher only if the stall repeats",
		})
	}
	return findings
}

// evaluateStorageFindings converts storage control-plane state into stable,
// deduplicated health findings.
func evaluateStorageFindings(snapshot Snapshot) []Finding {
	storageHealth := snapshot.Storage
	if storageHealth == nil || !storageHealth.Available {
		return []Finding{{
			Code:              FindingStorageUnavailable,
			Severity:          SeverityCritical,
			Component:         "storageHealth",
			Message:           "storageHealth health is unavailable",
			RecommendedAction: "inspect oro storageHealth status and repair the storageHealth catalog before admitting new work",
		}}
	}

	findings := storageStateFindings(*storageHealth)
	findings = append(findings, storageMessageFindings(FindingStorageFailure, SeverityError, "storageHealth cleanup failed", "inspect oro storageHealth status and retry the failed cleanup", storageHealth.Failures)...)
	findings = append(findings, storageMessageFindings(FindingStorageCancellation, SeverityWarning, "storageHealth cancelled an active writer", "inspect the cancelled process and storageHealth pressure before retrying work", storageHealth.Cancellations)...)
	if storageHealth.AdmissionPaused {
		findings = append(findings, Finding{
			Code:              FindingStorageAdmissionPaused,
			Severity:          SeverityWarning,
			Component:         "storageHealth",
			Message:           "storageHealth admission is paused",
			RecommendedAction: "inspect oro storageHealth status and resume admissions only after pressure and active leases are clear",
		})
	}
	return findings
}

func storageStateFindings(storageHealth StorageHealth) []Finding {
	findings := make([]Finding, 0, 3)
	if pressure := strings.TrimSpace(storageHealth.Pressure); pressure != "" && !strings.EqualFold(pressure, "normal") {
		severity := SeverityWarning
		if strings.EqualFold(pressure, "critical") {
			severity = SeverityCritical
		}
		findings = append(findings, Finding{
			Code:              FindingStoragePressure,
			Severity:          severity,
			Component:         "storage",
			Message:           fmt.Sprintf("storage pressure is %s", pressure),
			Type:              pressure,
			RecommendedAction: "inspect oro storage status and reclaim only storage proven safe to retire",
		})
	}
	if storageHealth.SweepOverdue {
		findings = append(findings, Finding{
			Code:              FindingStorageSweepOverdue,
			Severity:          SeverityWarning,
			Component:         "storage",
			Message:           "storage maintenance sweep is overdue",
			RecommendedAction: "run oro storage clean after controllers acknowledge the maintenance pause",
		})
	}
	if storageHealth.BlockedRetirements > 0 {
		findings = append(findings, Finding{
			Code:              FindingStorageRetirementBlocked,
			Severity:          SeverityWarning,
			Component:         "storage",
			Message:           fmt.Sprintf("%d storage retirement(s) are blocked", storageHealth.BlockedRetirements),
			RecommendedAction: "inspect oro storage status for leased, dirty, or ownership-uncertain retirement records",
		})
	}
	return findings
}

func storageMessageFindings(code string, severity Severity, prefix, action string, messages []string) []Finding {
	unique := make(map[string]struct{}, len(messages))
	for _, message := range messages {
		if message = strings.TrimSpace(message); message != "" {
			unique[message] = struct{}{}
		}
	}
	values := make([]string, 0, len(unique))
	for message := range unique {
		values = append(values, message)
	}
	sort.Strings(values)
	findings := make([]Finding, 0, len(values))
	for _, message := range values {
		findings = append(findings, Finding{
			Code:              code,
			Severity:          severity,
			Component:         "storage",
			Message:           fmt.Sprintf("%s: %s", prefix, message),
			Fingerprint:       message,
			RecommendedAction: action,
		})
	}
	return findings
}

func stateFromFindings(snapshot Snapshot, findings []Finding) State {
	if !snapshot.DaemonRunning && len(findings) == 0 {
		return StateStopped
	}
	state := StateHealthy
	for _, finding := range findings {
		switch finding.Severity {
		case SeverityCritical:
			return StateUnsafe
		case SeverityError:
			state = StateStalled
		case SeverityWarning:
			if state == StateHealthy {
				state = StateDegraded
			}
		}
	}
	return state
}

func postureFor(state State, findings []Finding) string {
	switch {
	case state == StateStopped:
		return "daemon stopped cleanly"
	case len(findings) == 0:
		return "no findings"
	case state == StateUnsafe:
		return "operator action required before more automation"
	case state == StateStalled:
		return "work is not making expected progress"
	default:
		return "operator attention recommended"
	}
}

func activeWorkerState(state string) bool {
	switch state {
	case "busy", "reviewing", "reserved":
		return true
	default:
		return false
	}
}

func assignedWorkerPairs(workers []WorkerSnapshot) map[string]bool {
	out := make(map[string]bool, len(workers))
	for _, worker := range workers {
		if worker.BeadID != "" {
			out[worker.ID+"\x00"+worker.BeadID] = true
		}
	}
	return out
}

func throughputStalled(metrics ThroughputMetrics) bool {
	return metrics.WindowSecs > 0 &&
		metrics.AssignmentsStarted > 0 &&
		metrics.ProductiveClosures == 0 &&
		metrics.ProgressTimeouts > 0
}

func opsRunFindings(metrics OpsRunMetrics) []Finding {
	findings := make([]Finding, 0, metrics.Failed+metrics.Stale)
	for _, run := range metrics.Runs {
		switch run.Status {
		case "failed":
			findings = append(findings, Finding{
				Code:              FindingOpsRunFailed,
				Severity:          SeverityCritical,
				Component:         "ops",
				Message:           fmt.Sprintf("%s ops run for %s failed", run.Type, run.BeadID),
				BeadID:            run.BeadID,
				Type:              run.Type,
				AgeSecs:           run.AgeSecs,
				RecommendedAction: OpsRunRecommendedAction(run),
			})
		case "stale":
			findings = append(findings, Finding{
				Code:              FindingOpsRunStale,
				Severity:          SeverityCritical,
				Component:         "ops",
				Message:           fmt.Sprintf("%s ops run for %s is stale", run.Type, run.BeadID),
				BeadID:            run.BeadID,
				Type:              run.Type,
				AgeSecs:           run.AgeSecs,
				RecommendedAction: OpsRunRecommendedAction(run),
			})
		}
	}
	if len(findings) > 0 {
		return findings
	}
	if metrics.Failed > 0 {
		findings = append(findings, Finding{
			Code:              FindingOpsRunFailed,
			Severity:          SeverityCritical,
			Component:         "ops",
			Message:           fmt.Sprintf("%d ops run(s) failed", metrics.Failed),
			RecommendedAction: "run oro ops list, then use oro ops retry <id> or oro ops resolve <id> <reason>",
		})
	}
	if metrics.Stale > 0 {
		findings = append(findings, Finding{
			Code:              FindingOpsRunStale,
			Severity:          SeverityCritical,
			Component:         "ops",
			Message:           fmt.Sprintf("%d ops run(s) are stale", metrics.Stale),
			RecommendedAction: "run oro ops list, then use oro ops retry <id> or oro ops resolve <id> <reason>",
		})
	}
	return findings
}

func pendingEscalationFindings(metrics EscalationMetrics) []Finding {
	findings := make([]Finding, 0, metrics.Unrouted)
	for _, escalation := range metrics.Escalations {
		if IsKnownEscalationType(escalation.Type) {
			continue
		}
		findings = append(findings, Finding{
			Code:              FindingPendingEscalationUnrouted,
			Severity:          SeverityError,
			Component:         "escalation",
			Message:           fmt.Sprintf("pending escalation type %q has no route", escalation.Type),
			WorkerID:          escalation.WorkerID,
			BeadID:            escalation.BeadID,
			Type:              escalation.Type,
			AgeSecs:           escalation.AgeSecs,
			RecommendedAction: "inspect oro pending-escalations, then add routing support or ack the escalation if obsolete",
		})
	}
	if len(findings) > 0 {
		return findings
	}
	if metrics.Unrouted > 0 {
		findings = append(findings, Finding{
			Code:              FindingPendingEscalationUnrouted,
			Severity:          SeverityError,
			Component:         "escalation",
			Message:           fmt.Sprintf("%d pending escalation(s) have no route", metrics.Unrouted),
			RecommendedAction: "inspect oro pending-escalations, then add routing support or ack obsolete escalations",
		})
	}
	return findings
}

// OpsRunRecommendedAction returns the explicit operator commands for a blocking ops run.
func OpsRunRecommendedAction(run OpsRunSnapshot) string {
	if run.ID > 0 {
		return fmt.Sprintf("run oro ops list, then oro ops retry %d or oro ops resolve %d <reason>", run.ID, run.ID)
	}
	return "run oro ops list, then use oro ops retry <id> or oro ops resolve <id> <reason>"
}

func defaultFloat(v, fallback float64) float64 {
	if v > 0 {
		return v
	}
	return fallback
}

// LoadActiveAssignments reads active assignment rows from the state database.
func LoadActiveAssignments(ctx context.Context, db *sql.DB, now time.Time) ([]AssignmentSnapshot, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
SELECT a.id,
       a.bead_id,
       a.worker_id,
       COALESCE(b.status, ''),
       COALESCE(CAST(strftime('%s', a.assigned_at) AS INTEGER), 0)
  FROM assignments a
  LEFT JOIN beads b ON b.id = a.bead_id
 WHERE a.status = 'active'
 ORDER BY a.id`)
	if err != nil {
		if tableMissing(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("query active assignments: %w", err)
	}
	defer rows.Close()

	var assignments []AssignmentSnapshot
	for rows.Next() {
		var assignment AssignmentSnapshot
		var assignedUnix int64
		if err := rows.Scan(&assignment.ID, &assignment.BeadID, &assignment.WorkerID, &assignment.BeadStatus, &assignedUnix); err != nil {
			return nil, fmt.Errorf("scan active assignment: %w", err)
		}
		if assignedUnix > 0 && !now.IsZero() {
			assignment.AgeSecs = now.Sub(time.Unix(assignedUnix, 0)).Seconds()
			if assignment.AgeSecs < 0 {
				assignment.AgeSecs = 0
			}
		}
		assignments = append(assignments, assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active assignments: %w", err)
	}
	return assignments, nil
}

// LoadQGMetrics reads quality-gate incident counts from the state database.
func LoadQGMetrics(ctx context.Context, db *sql.DB) (openIncidents, occurrences30m int, topFingerprints []string, err error) {
	if db == nil {
		return 0, 0, nil, nil
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qg_failure_incidents WHERE status='open'`).Scan(&openIncidents); err != nil {
		if tableMissing(err) {
			return 0, 0, nil, nil
		}
		return 0, 0, nil, fmt.Errorf("count open qg incidents: %w", err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM qg_failure_occurrences
 WHERE created_at >= datetime('now', '-30 minutes')`).Scan(&occurrences30m); err != nil {
		if tableMissing(err) {
			return openIncidents, 0, nil, nil
		}
		return 0, 0, nil, fmt.Errorf("count qg occurrences: %w", err)
	}
	topFingerprints, err = LoadRecentQGFingerprints(ctx, db)
	if err != nil {
		return 0, 0, nil, err
	}
	return openIncidents, occurrences30m, topFingerprints, nil
}

// LoadRecentQGFingerprints returns the most frequent quality-gate fingerprints
// with occurrences in the last 30 minutes, regardless of incident open status.
func LoadRecentQGFingerprints(ctx context.Context, db *sql.DB) ([]string, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
SELECT i.fingerprint
  FROM qg_failure_occurrences o
  JOIN qg_failure_incidents i ON i.id = o.incident_id
 WHERE o.created_at >= datetime('now', '-30 minutes')
 GROUP BY i.fingerprint
 ORDER BY COUNT(*) DESC, MAX(o.created_at) DESC, i.fingerprint ASC
 LIMIT 5`)
	if err != nil {
		if tableMissing(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("query recent qg fingerprints: %w", err)
	}
	defer rows.Close()
	var topFingerprints []string
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, fmt.Errorf("scan recent qg fingerprint: %w", err)
		}
		topFingerprints = append(topFingerprints, fp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent qg fingerprints: %w", err)
	}
	return topFingerprints, nil
}

// LoadRecoveryQuarantineMetrics reads globally assignment-blocking recovery
// quarantine counts from the state database. Human-owned records still protect
// their own bead, but do not freeze unrelated factory work.
func LoadRecoveryQuarantineMetrics(ctx context.Context, db *sql.DB) (openQuarantines int, err error) {
	if db == nil {
		return 0, nil
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_quarantines WHERE status='open'`).Scan(&openQuarantines); err != nil {
		if tableMissing(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("count open recovery quarantines: %w", err)
	}
	return openQuarantines, nil
}

// LoadEpicBranchAdmissionMetrics reads branch-scoped blocks and unexpired leases.
func LoadEpicBranchAdmissionMetrics(ctx context.Context, db *sql.DB, now time.Time) (EpicBranchAdmissionMetrics, error) {
	var metrics EpicBranchAdmissionMetrics
	if db == nil {
		return metrics, nil
	}
	if err := db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(CASE WHEN state = 'blocked' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE
           WHEN state = 'leased'
            AND lease_expires_at IS NOT NULL
            AND julianday(lease_expires_at) > julianday(?)
           THEN 1 ELSE 0 END), 0)
  FROM epic_branch_admissions`, now.UTC().Format(time.RFC3339Nano)).Scan(&metrics.Blocked, &metrics.ActiveLeases); err != nil {
		if tableMissing(err) {
			return EpicBranchAdmissionMetrics{}, nil
		}
		return EpicBranchAdmissionMetrics{}, fmt.Errorf("load epic branch admission metrics: %w", err)
	}
	return metrics, nil
}

// LoadThroughputMetrics reads recent throughput counters from the state database.
func LoadThroughputMetrics(ctx context.Context, db *sql.DB, now time.Time, window time.Duration) (ThroughputMetrics, error) {
	metrics := ThroughputMetrics{WindowSecs: window.Seconds()}
	if db == nil || window <= 0 {
		return metrics, nil
	}
	startTime := now.Add(-window)
	start := startTime.UTC().Format("2006-01-02 15:04:05")
	assignmentsStarted, err := countTimestampsInWindow(
		ctx,
		db,
		`SELECT assigned_at FROM assignments`,
		startTime,
		now,
	)
	if err != nil {
		if tableMissing(err) {
			return metrics, nil
		}
		return metrics, fmt.Errorf("count throughput assignments: %w", err)
	}
	metrics.AssignmentsStarted = assignmentsStarted
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
  FROM beads
 WHERE status='closed'
   AND COALESCE(close_reason, '') NOT IN ('deferred', 'duplicate', 'not_planned')
   AND datetime(COALESCE(closed_at, updated_at)) >= datetime(?)`, start).Scan(&metrics.ProductiveClosures); err != nil {
		if !tableMissing(err) {
			return metrics, fmt.Errorf("count throughput closures: %w", err)
		}
	}
	progressTimeouts, err := countTimestampsInWindow(
		ctx,
		db,
		`SELECT created_at FROM events WHERE type='progress_timeout'`,
		startTime,
		now,
	)
	if err != nil && !tableMissing(err) {
		return metrics, fmt.Errorf("count throughput progress timeouts: %w", err)
	}
	if err == nil {
		metrics.ProgressTimeouts = progressTimeouts
	}
	latest, err := latestEventTimestamp(ctx, db)
	if err != nil {
		return metrics, err
	}
	if !latest.IsZero() {
		metrics.LastEventAgeSecs = now.Sub(latest).Seconds()
		if metrics.LastEventAgeSecs < 0 {
			metrics.LastEventAgeSecs = 0
		}
	}
	return metrics, nil
}

func countTimestampsInWindow(
	ctx context.Context,
	db *sql.DB,
	query string,
	start, end time.Time,
) (int, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("query timestamps: %w", err)
	}
	count := 0
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan timestamp: %w", err)
		}
		if ts, ok := parseSQLiteTime(raw); ok && !ts.Before(start) && ts.Before(end) {
			count++
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("iterate timestamps: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close timestamps: %w", err)
	}
	return count, nil
}

func latestEventTimestamp(ctx context.Context, db *sql.DB) (time.Time, error) {
	rows, err := db.QueryContext(ctx, `SELECT created_at FROM events`)
	if err != nil {
		if tableMissing(err) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("load latest event: %w", err)
	}
	defer rows.Close()
	var latest time.Time
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return time.Time{}, fmt.Errorf("scan latest event: %w", err)
		}
		if ts, ok := parseSQLiteTime(raw); ok && ts.After(latest) {
			latest = ts
		}
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, fmt.Errorf("iterate latest events: %w", err)
	}
	return latest, nil
}

// LoadPendingEscalationMetrics reads pending escalations that have no known
// dispatcher route and should remain visible in factory health.
func LoadPendingEscalationMetrics(ctx context.Context, db *sql.DB, now time.Time) (EscalationMetrics, error) {
	var metrics EscalationMetrics
	if db == nil {
		return metrics, nil
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, type, COALESCE(bead_id, ''), COALESCE(worker_id, ''), COALESCE(created_at, '')
  FROM escalations
 WHERE status = 'pending'
 ORDER BY id`)
	if err != nil {
		if tableMissing(err) {
			return metrics, nil
		}
		return metrics, fmt.Errorf("query pending escalation metrics: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			escalation EscalationSnapshot
			createdAt  string
		)
		if err := rows.Scan(&escalation.ID, &escalation.Type, &escalation.BeadID, &escalation.WorkerID, &createdAt); err != nil {
			return metrics, fmt.Errorf("scan pending escalation metrics: %w", err)
		}
		if IsKnownEscalationType(escalation.Type) {
			continue
		}
		if ts, ok := parseSQLiteTime(createdAt); ok && !now.IsZero() {
			escalation.AgeSecs = now.Sub(ts).Seconds()
			if escalation.AgeSecs < 0 {
				escalation.AgeSecs = 0
			}
		}
		metrics.Unrouted++
		metrics.Escalations = append(metrics.Escalations, escalation)
	}
	if err := rows.Err(); err != nil {
		return metrics, fmt.Errorf("iterate pending escalation metrics: %w", err)
	}
	return metrics, nil
}

// LoadOpsRunMetrics reads health-relevant ops run counts from the state database.
func LoadOpsRunMetrics(ctx context.Context, db *sql.DB, now time.Time) (OpsRunMetrics, error) {
	var metrics OpsRunMetrics
	if db == nil {
		return metrics, nil
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, type, COALESCE(bead_id, ''), status, COALESCE(started_at, '')
  FROM ops_runs
 WHERE status IN ('running', 'failed', 'stale')
 ORDER BY id`)
	if err != nil {
		if tableMissing(err) {
			return metrics, nil
		}
		return metrics, fmt.Errorf("query ops run metrics: %w", err)
	}
	defer rows.Close()

	metrics.ByType = make(map[string]OpsRunTypeMetrics)
	for rows.Next() {
		var (
			run       OpsRunSnapshot
			startedAt string
		)
		if err := rows.Scan(&run.ID, &run.Type, &run.BeadID, &run.Status, &startedAt); err != nil {
			return metrics, fmt.Errorf("scan ops run metrics: %w", err)
		}
		if ts, ok := parseSQLiteTime(startedAt); ok && !now.IsZero() {
			run.AgeSecs = now.Sub(ts).Seconds()
			if run.AgeSecs < 0 {
				run.AgeSecs = 0
			}
		}
		if run.Status == "running" && run.AgeSecs >= OpsRunStaleAfter.Seconds() {
			run.Status = "stale"
		}
		addOpsRunMetric(&metrics, run)
	}
	if err := rows.Err(); err != nil {
		return metrics, fmt.Errorf("iterate ops run metrics: %w", err)
	}
	if len(metrics.ByType) == 0 {
		metrics.ByType = nil
	}
	return metrics, nil
}

// IsKnownEscalationType reports whether an escalation type is part of the
// dispatcher protocol known to this build.
func IsKnownEscalationType(escType string) bool {
	switch protocol.EscalationType(escType) {
	case protocol.EscMergeConflict,
		protocol.EscStuck,
		protocol.EscStuckWorker,
		protocol.EscPriorityContention,
		protocol.EscWorkerCrash,
		protocol.EscStatus,
		protocol.EscDrainComplete,
		protocol.EscMissingAC,
		protocol.EscEpicComplete,
		protocol.EscMergeComplete,
		protocol.EscOversizedBead,
		protocol.EscNonTDDAC,
		protocol.EscManualIntegration:
		return true
	default:
		return false
	}
}

func addOpsRunMetric(metrics *OpsRunMetrics, run OpsRunSnapshot) {
	counts := metrics.ByType[run.Type]
	switch run.Status {
	case "running":
		metrics.Running++
		counts.Running++
	case "failed":
		metrics.Failed++
		counts.Failed++
	case "stale":
		metrics.Stale++
		counts.Stale++
	default:
		return
	}
	metrics.ByType[run.Type] = counts
	metrics.Runs = append(metrics.Runs, run)
}

func parseSQLiteTime(raw string) (time.Time, bool) {
	layouts := []string{
		"2006-01-02 15:04:05",
		time.RFC3339Nano,
		time.RFC3339,
	}
	for _, layout := range layouts {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts, true
		}
	}
	if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(unix, 0), true
	}
	return time.Time{}, false
}

func tableMissing(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}
