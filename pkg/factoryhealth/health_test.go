package factoryhealth //nolint:testpackage // white-box tests keep snapshots concise.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestEvaluateFactoryHealthStates(t *testing.T) {
	tests := []struct {
		name        string
		snapshot    Snapshot
		wantState   State
		wantFinding string
	}{
		{
			name: "healthy running factory",
			snapshot: Snapshot{
				DaemonRunning:       true,
				DispatcherState:     "running",
				ProgressTimeoutSecs: 600,
				Workers: []WorkerSnapshot{
					{ID: "w-1", State: "idle", LastHeartbeatSecs: 2},
				},
			},
			wantState: StateHealthy,
		},
		{
			name: "degraded with open QG incidents",
			snapshot: Snapshot{
				DaemonRunning:       true,
				DispatcherState:     "running",
				ProgressTimeoutSecs: 600,
				OpenQGIncidents:     1,
			},
			wantState:   StateDegraded,
			wantFinding: FindingQGIncidentsOpen,
		},
		{
			name: "stalled alive worker with no progress",
			snapshot: Snapshot{
				DaemonRunning:        true,
				DispatcherState:      "running",
				ProgressTimeoutSecs:  600,
				HeartbeatTimeoutSecs: 45,
				Workers: []WorkerSnapshot{
					{ID: "w-1", State: "busy", BeadID: "oro-a", LastProgressSecs: 601, LastHeartbeatSecs: 5},
				},
			},
			wantState:   StateStalled,
			wantFinding: FindingAliveNoProgress,
		},
		{
			name: "unsafe stopped daemon with active assignments",
			snapshot: Snapshot{
				DaemonRunning: false,
				ActiveAssignments: []AssignmentSnapshot{
					{ID: 1, BeadID: "oro-a", WorkerID: "w-1", BeadStatus: "in_progress"},
				},
			},
			wantState:   StateUnsafe,
			wantFinding: FindingDaemonStoppedWithActiveAssignments,
		},
		{
			name:      "stopped cleanly",
			snapshot:  Snapshot{DaemonRunning: false},
			wantState: StateStopped,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateWithAvailableStorage(tt.snapshot)
			if got.State != tt.wantState {
				t.Fatalf("state = %q, want %q; findings=%+v", got.State, tt.wantState, got.Findings)
			}
			if tt.wantFinding != "" && !hasFinding(got, tt.wantFinding) {
				t.Fatalf("missing finding %q in %+v", tt.wantFinding, got.Findings)
			}
		})
	}
}

func TestLoadThroughputMetricsCountsAssignmentsAndTimeoutsChronologically(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
CREATE TABLE assignments (assigned_at TEXT);
CREATE TABLE events (type TEXT, created_at TEXT);
INSERT INTO assignments VALUES
	('2026-07-17 11:45:00'),
	('2026-07-17T11:50:00Z'),
	('2026-07-17T11:55:00.123Z'),
	('2026-07-17 11:00:00'),
	('not-a-timestamp');
INSERT INTO events VALUES
	('progress_timeout', '2026-07-17T11:45:00.123Z'),
	('worker_progress', '2026-07-17T11:50:00Z'),
	('progress_timeout', '2026-07-17 10:00:00');
`); err != nil {
		t.Fatalf("schema and fixtures: %v", err)
	}

	got, err := LoadThroughputMetrics(ctx, db, time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC), 30*time.Minute)
	if err != nil {
		t.Fatalf("LoadThroughputMetrics: %v", err)
	}
	if got.AssignmentsStarted != 3 {
		t.Fatalf("assignments started = %d, want 3", got.AssignmentsStarted)
	}
	if got.ProgressTimeouts != 1 {
		t.Fatalf("progress timeouts = %d, want 1", got.ProgressTimeouts)
	}
}

func TestEvaluateFactoryHealthIgnoresPipelineOwnedWorkerStates(t *testing.T) {
	got := evaluateWithAvailableStorage(Snapshot{
		DaemonRunning:        true,
		DispatcherState:      "running",
		ProgressTimeoutSecs:  600,
		HeartbeatTimeoutSecs: 45,
		Workers: []WorkerSnapshot{
			{ID: "reserved", State: "reserved", BeadID: "oro-r", LastProgressSecs: 601, LastHeartbeatSecs: 5},
			{ID: "reviewing", State: "reviewing", BeadID: "oro-v", LastProgressSecs: 601, LastHeartbeatSecs: 5},
			{ID: "busy", State: "busy", BeadID: "oro-b", LastProgressSecs: 601, LastHeartbeatSecs: 5},
		},
	})

	if got.State != StateStalled {
		t.Fatalf("state = %q, want stalled; findings=%+v", got.State, got.Findings)
	}
	if got.Metrics.ActiveWorkers != 3 {
		t.Fatalf("active workers = %d, want 3", got.Metrics.ActiveWorkers)
	}
	if !hasFindingForWorker(got, FindingAliveNoProgress, "busy") {
		t.Fatalf("missing busy alive_no_progress finding in %+v", got.Findings)
	}
	for _, workerID := range []string{"reserved", "reviewing"} {
		if hasFindingForWorker(got, FindingAliveNoProgress, workerID) {
			t.Fatalf("pipeline-owned worker %q emitted alive_no_progress: %+v", workerID, got.Findings)
		}
	}
}

func hasFindingForWorker(health FactoryHealth, code, workerID string) bool {
	for _, finding := range health.Findings {
		if finding.Code == code && finding.WorkerID == workerID {
			return true
		}
	}
	return false
}

func TestLoadThroughputMetricsSelectsLatestMixedFormatEvent(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 6, 0, 0, time.UTC)
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
CREATE TABLE assignments (assigned_at TEXT);
CREATE TABLE beads (status TEXT, close_reason TEXT, closed_at TEXT, updated_at TEXT);
CREATE TABLE events (type TEXT, created_at TEXT);
INSERT INTO events (type, created_at) VALUES ('old', '2026-07-18T12:00:00Z'), ('new', '2026-07-18 12:05:00');`); err != nil {
		t.Fatalf("schema and seed: %v", err)
	}

	got, err := LoadThroughputMetrics(ctx, db, now, time.Hour)
	if err != nil {
		t.Fatalf("LoadThroughputMetrics: %v", err)
	}
	if got.LastEventAgeSecs != 60 {
		t.Fatalf("last event age = %v, want 60", got.LastEventAgeSecs)
	}
}

func TestEvaluateAssignmentContradictions(t *testing.T) {
	got := evaluateWithAvailableStorage(Snapshot{
		DaemonRunning:       true,
		DispatcherState:     "running",
		ReadyQueue:          2,
		ProgressTimeoutSecs: 600,
		Workers: []WorkerSnapshot{
			{ID: "w-1", State: "idle"},
		},
		ActiveAssignments: []AssignmentSnapshot{
			{ID: 7, BeadID: "oro-open", WorkerID: "missing-worker", BeadStatus: "open", AgeSecs: 120},
		},
	})

	for _, code := range []string{
		FindingIdleReadyQueue,
		FindingOrphanActiveAssignment,
		FindingAssignmentBeadStatusMismatch,
	} {
		if !hasFinding(got, code) {
			t.Fatalf("missing finding %q in %+v", code, got.Findings)
		}
	}
	if got.State != StateUnsafe {
		t.Fatalf("state = %q, want unsafe for active assignment contradiction", got.State)
	}
}

func TestEvaluateRecoveryQuarantineOpenIsUnsafe(t *testing.T) {
	got := evaluateWithAvailableStorage(Snapshot{
		DaemonRunning:           true,
		DispatcherState:         "running",
		OpenRecoveryQuarantines: 2,
	})

	if got.State != StateUnsafe {
		t.Fatalf("state = %q, want unsafe; findings=%+v", got.State, got.Findings)
	}
	if got.Metrics.OpenRecoveryQuarantines != 2 {
		t.Fatalf("open recovery quarantine metric = %d, want 2", got.Metrics.OpenRecoveryQuarantines)
	}
	if !hasFinding(got, FindingRecoveryQuarantineOpen) {
		t.Fatalf("missing recovery quarantine finding in %+v", got.Findings)
	}
}

func TestEvaluateStorageFindings(t *testing.T) {
	t.Run("storage signals are stable and deduplicated", func(t *testing.T) {
		snapshot := Snapshot{
			DaemonRunning: true,
			Storage: &StorageHealth{
				Available:          true,
				Pressure:           "critical",
				SweepOverdue:       true,
				BlockedRetirements: 2,
				Failures:           []string{"provider cache cleanup failed", "provider cache cleanup failed"},
				Cancellations:      []string{"namespace oro-a writer cancelled", "namespace oro-a writer cancelled"},
				AdmissionPaused:    true,
			},
		}

		findings := evaluateStorageFindings(snapshot)
		wantCodes := []string{
			FindingStoragePressure,
			FindingStorageSweepOverdue,
			FindingStorageRetirementBlocked,
			FindingStorageFailure,
			FindingStorageCancellation,
			FindingStorageAdmissionPaused,
		}
		if len(findings) != len(wantCodes) {
			t.Fatalf("findings = %+v, want %d stable findings", findings, len(wantCodes))
		}
		for index, wantCode := range wantCodes {
			if findings[index].Code != wantCode {
				t.Fatalf("finding[%d].Code = %q, want %q; findings=%+v", index, findings[index].Code, wantCode, findings)
			}
		}
		if findings[3].Fingerprint != "provider cache cleanup failed" {
			t.Fatalf("failure fingerprint = %q, want deduplicated failure", findings[3].Fingerprint)
		}
		if findings[4].Fingerprint != "namespace oro-a writer cancelled" {
			t.Fatalf("cancellation fingerprint = %q, want deduplicated cancellation", findings[4].Fingerprint)
		}

		health := Evaluate(snapshot)
		if health.Metrics.Storage == nil || !health.Metrics.Storage.AdmissionPaused {
			t.Fatalf("metrics storage = %+v, want storage state", health.Metrics.Storage)
		}
	})

	t.Run("missing storage snapshot is unavailable", func(t *testing.T) {
		findings := evaluateStorageFindings(Snapshot{DaemonRunning: true})
		if len(findings) != 1 || findings[0].Code != FindingStorageUnavailable {
			t.Fatalf("findings = %+v, want one storage unavailable finding", findings)
		}
	})
}

func TestEvaluateAssignmentFrozenByQuarantine(t *testing.T) {
	got := Evaluate(Snapshot{
		DaemonRunning:                true,
		DispatcherState:              "running",
		Workers:                      []WorkerSnapshot{{ID: "w-idle", State: "idle"}},
		AssignmentFrozenByQuarantine: true,
		BlockingRecoveryQuarantines:  2,
		AssignmentFreezeReason:       "open_recovery_quarantine",
		OpenRecoveryQuarantines:      2,
	})

	if !got.Metrics.AssignmentFrozenByQuarantine {
		t.Fatal("assignment frozen metric = false, want true")
	}
	if got.Metrics.BlockingRecoveryQuarantines != 2 {
		t.Fatalf("blocking recovery quarantines = %d, want 2", got.Metrics.BlockingRecoveryQuarantines)
	}
	if got.Metrics.AssignmentFreezeReason != "open_recovery_quarantine" {
		t.Fatalf("assignment freeze reason = %q, want open_recovery_quarantine", got.Metrics.AssignmentFreezeReason)
	}
	if !hasFinding(got, FindingAssignmentFrozenByQuarantine) {
		t.Fatalf("missing assignment freeze finding in %+v", got.Findings)
	}

	unfrozen := Evaluate(Snapshot{DaemonRunning: true, DispatcherState: "running"})
	if unfrozen.Metrics.AssignmentFrozenByQuarantine || hasFinding(unfrozen, FindingAssignmentFrozenByQuarantine) {
		t.Fatalf("unfrozen health retained assignment freeze signal: %+v", unfrozen)
	}
}

func TestLoadRecoveryQuarantineMetricsExcludesHumanOwned(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, reason, details, status)
VALUES
    ('oro-open', 'unsafe_stale_branch', 'assignment filtering blocks this row', 'open'),
    ('oro-human-owned', 'unsafe_stale_branch', 'assignment filtering blocks this row', 'human_owned'),
    ('oro-resolved', 'unsafe_stale_branch', 'resolved rows do not block assignment', 'resolved');
`); err != nil {
		t.Fatalf("seed recovery quarantine: %v", err)
	}

	got, err := LoadRecoveryQuarantineMetrics(ctx, db)
	if err != nil {
		t.Fatalf("LoadRecoveryQuarantineMetrics: %v", err)
	}
	if got != 1 {
		t.Fatalf("open recovery quarantines = %d, want 1", got)
	}

	health := evaluateWithAvailableStorage(Snapshot{
		DaemonRunning:           true,
		DispatcherState:         "running",
		OpenRecoveryQuarantines: got,
	})
	if health.State == StateHealthy {
		t.Fatalf("state = %q, want non-healthy while recovery quarantines block assignment", health.State)
	}
	if !hasFinding(health, FindingRecoveryQuarantineOpen) {
		t.Fatalf("missing recovery quarantine finding in %+v", health.Findings)
	}
}

func TestLoadThroughputMetricsCountsRFC3339Chronologically(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
CREATE TABLE assignments (assigned_at TEXT);
CREATE TABLE events (type TEXT, created_at TEXT);
CREATE TABLE beads (status TEXT, close_reason TEXT, closed_at TEXT, updated_at TEXT);
INSERT INTO beads (status, close_reason, closed_at, updated_at) VALUES
 ('closed', '', '2026-07-17T23:04:00Z', '2026-07-17T23:04:00Z'),
 ('closed', '', '2026-07-17T20:33:00Z', '2026-07-17T20:33:00Z'),
 ('closed', '', '2026-07-17T03:35:00Z', '2026-07-17T03:35:00Z'),
 ('closed', '', '2026-07-17 23:05:00', '2026-07-17 23:05:00'),
 ('closed', 'deferred', '2026-07-17T23:06:00Z', '2026-07-17T23:06:00Z'),
 ('closed', 'duplicate', '2026-07-17T23:07:00Z', '2026-07-17T23:07:00Z'),
 ('closed', 'not_planned', '2026-07-17T23:08:00Z', '2026-07-17T23:08:00Z');
`); err != nil {
		t.Fatalf("seed throughput tables: %v", err)
	}

	got, err := LoadThroughputMetrics(ctx, db, time.Date(2026, 7, 17, 23, 18, 0, 0, time.UTC), 30*time.Minute)
	if err != nil {
		t.Fatalf("LoadThroughputMetrics: %v", err)
	}
	if got.ProductiveClosures != 2 {
		t.Fatalf("productive closures = %d, want 2", got.ProductiveClosures)
	}
}

func TestEvaluateNoManagerPaneFindingByDefault(t *testing.T) {
	got := evaluateWithAvailableStorage(Snapshot{
		DaemonRunning:   true,
		DispatcherState: "running",
	})

	if got.State != StateHealthy {
		t.Fatalf("state = %q, want healthy; findings=%+v", got.State, got.Findings)
	}
	if hasFinding(got, "manager_pane_unhealthy") {
		t.Fatalf("absent manager pane should not be unhealthy by default: %+v", got.Findings)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal health: %v", err)
	}
	for _, legacy := range []string{"manager_pane_alive", "manager_pane_unhealthy"} {
		if strings.Contains(string(data), legacy) {
			t.Fatalf("health JSON still contains legacy manager pane surface %q: %s", legacy, data)
		}
	}
}

func TestEvaluateOpsRunFindings(t *testing.T) {
	got := evaluateWithAvailableStorage(Snapshot{
		DaemonRunning:   true,
		DispatcherState: "running",
		OpsRuns: OpsRunMetrics{
			Running: 1,
			Failed:  1,
			Stale:   1,
			ByType: map[string]OpsRunTypeMetrics{
				"decompose": {Running: 1, Failed: 1},
				"diagnosis": {Stale: 1},
			},
			Runs: []OpsRunSnapshot{
				{
					ID:      2,
					Type:    "decompose",
					BeadID:  "oro-failed",
					Status:  "failed",
					AgeSecs: time.Minute.Seconds(),
				},
				{
					ID:      3,
					Type:    "diagnosis",
					BeadID:  "oro-stale",
					Status:  "stale",
					AgeSecs: (2 * time.Hour).Seconds(),
				},
			},
		},
	})

	if got.Metrics.OpsRuns.Failed != 1 {
		t.Fatalf("failed ops runs metric = %d, want 1", got.Metrics.OpsRuns.Failed)
	}
	if got.Metrics.OpsRuns.Stale != 1 {
		t.Fatalf("stale ops runs metric = %d, want 1", got.Metrics.OpsRuns.Stale)
	}
	if got.State != StateUnsafe {
		t.Fatalf("state = %q, want unsafe; findings=%+v", got.State, got.Findings)
	}

	failed, ok := findingByCode(got, FindingOpsRunFailed)
	if !ok {
		t.Fatalf("missing failed ops run finding in %+v", got.Findings)
	}
	if failed.BeadID != "oro-failed" || failed.Type != "decompose" {
		t.Fatalf("failed finding payload bead/type = %q/%q, want oro-failed/decompose", failed.BeadID, failed.Type)
	}
	if failed.RecommendedAction != "run oro ops list, then oro ops retry 2 or oro ops resolve 2 <reason>" {
		t.Fatalf("failed recommended action = %q", failed.RecommendedAction)
	}

	stale, ok := findingByCode(got, FindingOpsRunStale)
	if !ok {
		t.Fatalf("missing stale ops run finding in %+v", got.Findings)
	}
	if stale.BeadID != "oro-stale" || stale.Type != "diagnosis" {
		t.Fatalf("stale finding payload bead/type = %q/%q, want oro-stale/diagnosis", stale.BeadID, stale.Type)
	}
	if stale.RecommendedAction != "run oro ops list, then oro ops retry 3 or oro ops resolve 3 <reason>" {
		t.Fatalf("stale recommended action = %q", stale.RecommendedAction)
	}
}

func TestEvaluateOpsRunFindingsFromAggregateCounts(t *testing.T) {
	got := evaluateWithAvailableStorage(Snapshot{
		DaemonRunning:   true,
		DispatcherState: "running",
		OpsRuns: OpsRunMetrics{
			Failed: 2,
			Stale:  1,
		},
	})

	if got.State != StateUnsafe {
		t.Fatalf("state = %q, want unsafe; findings=%+v", got.State, got.Findings)
	}
	failed, ok := findingByCode(got, FindingOpsRunFailed)
	if !ok {
		t.Fatalf("missing aggregate failed ops run finding in %+v", got.Findings)
	}
	if failed.Message != "2 ops run(s) failed" {
		t.Fatalf("failed aggregate message = %q", failed.Message)
	}
	if failed.RecommendedAction != "run oro ops list, then use oro ops retry <id> or oro ops resolve <id> <reason>" {
		t.Fatalf("failed aggregate action = %q", failed.RecommendedAction)
	}
	stale, ok := findingByCode(got, FindingOpsRunStale)
	if !ok {
		t.Fatalf("missing aggregate stale ops run finding in %+v", got.Findings)
	}
	if stale.Message != "1 ops run(s) are stale" {
		t.Fatalf("stale aggregate message = %q", stale.Message)
	}
	if stale.RecommendedAction != "run oro ops list, then use oro ops retry <id> or oro ops resolve <id> <reason>" {
		t.Fatalf("stale aggregate action = %q", stale.RecommendedAction)
	}
}

func TestEvaluateOpsRunFindingsIgnoresEmptyCounts(t *testing.T) {
	got := evaluateWithAvailableStorage(Snapshot{
		DaemonRunning:   true,
		DispatcherState: "running",
		OpsRuns: OpsRunMetrics{
			Running: 1,
			Runs: []OpsRunSnapshot{
				{ID: 4, Type: "decompose", BeadID: "oro-running", Status: "running"},
			},
		},
	})

	if hasFinding(got, FindingOpsRunFailed) || hasFinding(got, FindingOpsRunStale) {
		t.Fatalf("unexpected ops run finding in %+v", got.Findings)
	}
}

func TestPendingUnknownEscalationSurfacesUnroutedHealthFinding(t *testing.T) {
	got := evaluateWithAvailableStorage(Snapshot{
		DaemonRunning:   true,
		DispatcherState: "running",
		PendingEscalations: EscalationMetrics{
			Unrouted: 1,
			Escalations: []EscalationSnapshot{
				{
					ID:       9,
					Type:     "FUTURE_ESCALATION",
					BeadID:   "oro-future",
					WorkerID: "w-future",
					AgeSecs:  90,
				},
				{
					ID:     10,
					Type:   string(protocol.EscWorkerCrash),
					BeadID: "oro-known",
				},
			},
		},
	})

	finding, ok := findingByCode(got, FindingPendingEscalationUnrouted)
	if !ok {
		t.Fatalf("missing pending_escalation_unrouted finding in %+v", got.Findings)
	}
	if finding.BeadID != "oro-future" || finding.WorkerID != "w-future" || finding.Type != "FUTURE_ESCALATION" {
		t.Fatalf("unrouted finding payload bead/worker/type = %q/%q/%q, want oro-future/w-future/FUTURE_ESCALATION",
			finding.BeadID, finding.WorkerID, finding.Type)
	}
	if got.State != StateStalled {
		t.Fatalf("state = %q, want stalled for unrouted pending escalation", got.State)
	}
}

func TestPendingEscalationFindingFallsBackToAggregateCount(t *testing.T) {
	got := evaluateWithAvailableStorage(Snapshot{
		DaemonRunning:   true,
		DispatcherState: "running",
		PendingEscalations: EscalationMetrics{
			Unrouted: 2,
		},
	})

	finding, ok := findingByCode(got, FindingPendingEscalationUnrouted)
	if !ok {
		t.Fatalf("missing aggregate pending_escalation_unrouted finding in %+v", got.Findings)
	}
	if finding.Message != "2 pending escalation(s) have no route" {
		t.Fatalf("aggregate finding message = %q", finding.Message)
	}
	if finding.BeadID != "" || finding.WorkerID != "" || finding.Type != "" {
		t.Fatalf("aggregate finding should not include row payload: %+v", finding)
	}
}

func TestLoadPendingEscalationMetrics(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO escalations (type, bead_id, worker_id, message, status, created_at)
VALUES
    ('FUTURE_ESCALATION', 'oro-future', 'w-future', 'future route', 'pending', '2026-05-19 00:00:00'),
    ('WORKER_CRASH', 'oro-known', 'w-known', 'known route', 'pending', '2026-05-19 00:00:00'),
    ('OTHER_ESCALATION', 'oro-acked', 'w-acked', 'acked route', 'acked', '2026-05-19 00:00:00');
`); err != nil {
		t.Fatalf("seed escalations: %v", err)
	}

	got, err := LoadPendingEscalationMetrics(ctx, db, time.Date(2026, 5, 19, 0, 2, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("LoadPendingEscalationMetrics: %v", err)
	}
	if got.Unrouted != 1 {
		t.Fatalf("unrouted count = %d, want 1", got.Unrouted)
	}
	if len(got.Escalations) != 1 {
		t.Fatalf("unrouted escalations = %+v, want exactly one", got.Escalations)
	}
	escalation := got.Escalations[0]
	if escalation.Type != "FUTURE_ESCALATION" || escalation.BeadID != "oro-future" || escalation.WorkerID != "w-future" {
		t.Fatalf("unrouted escalation payload = %+v", escalation)
	}
	if escalation.AgeSecs != 120 {
		t.Fatalf("unrouted escalation age = %.0f, want 120", escalation.AgeSecs)
	}
}

func TestLoadPendingEscalationMetricsHandlesMissingInputs(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC)

	got, err := LoadPendingEscalationMetrics(ctx, nil, now)
	if err != nil {
		t.Fatalf("LoadPendingEscalationMetrics nil db: %v", err)
	}
	if got.Unrouted != 0 || len(got.Escalations) != 0 {
		t.Fatalf("nil db metrics = %+v, want zero", got)
	}

	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	got, err = LoadPendingEscalationMetrics(ctx, db, now)
	if err != nil {
		t.Fatalf("LoadPendingEscalationMetrics missing table: %v", err)
	}
	if got.Unrouted != 0 || len(got.Escalations) != 0 {
		t.Fatalf("missing table metrics = %+v, want zero", got)
	}
}

func TestLoadPendingEscalationMetricsClampsFutureAge(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO escalations (type, bead_id, worker_id, message, status, created_at)
VALUES ('FUTURE_ESCALATION', 'oro-future-age', 'w-future-age', 'future route', 'pending', '2026-05-19 00:05:00');
`); err != nil {
		t.Fatalf("seed escalation: %v", err)
	}

	got, err := LoadPendingEscalationMetrics(ctx, db, time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("LoadPendingEscalationMetrics: %v", err)
	}
	if len(got.Escalations) != 1 {
		t.Fatalf("unrouted escalations = %+v, want exactly one", got.Escalations)
	}
	if got.Escalations[0].AgeSecs != 0 {
		t.Fatalf("future escalation age = %.0f, want 0", got.Escalations[0].AgeSecs)
	}
}

func TestIsKnownEscalationType(t *testing.T) {
	for _, escType := range []protocol.EscalationType{
		protocol.EscMergeConflict,
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
		protocol.EscManualIntegration,
	} {
		if !IsKnownEscalationType(string(escType)) {
			t.Fatalf("IsKnownEscalationType(%q) = false, want true", escType)
		}
	}
	if IsKnownEscalationType("FUTURE_ESCALATION") {
		t.Fatal("IsKnownEscalationType(FUTURE_ESCALATION) = true, want false")
	}
}

func TestLoadQGMetricsReportsRecentFingerprintsForClosedIncidents(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO qg_failure_incidents
    (id, fingerprint, class, decision, confidence, reason, summary, status, occurrence_count)
VALUES
    (1, 'qg:closed-recent', 'worker_deterministic', 'retry_original', 'high', 'closed but still recent', 'closed recent', 'closed', 2);
INSERT INTO qg_failure_occurrences
    (id, incident_id, bead_id, output_hash)
VALUES
    ('occ-1', 1, 'oro-a', 'hash-1'),
    ('occ-2', 1, 'oro-b', 'hash-2');
`); err != nil {
		t.Fatalf("seed qg incidents: %v", err)
	}

	openIncidents, occurrences30m, topFingerprints, err := LoadQGMetrics(ctx, db)
	if err != nil {
		t.Fatalf("LoadQGMetrics: %v", err)
	}

	if openIncidents != 0 {
		t.Fatalf("open incidents = %d, want 0", openIncidents)
	}
	if occurrences30m != 2 {
		t.Fatalf("occurrences 30m = %d, want 2", occurrences30m)
	}
	if len(topFingerprints) != 1 || topFingerprints[0] != "qg:closed-recent" {
		t.Fatalf("top fingerprints = %v, want recent closed fingerprint", topFingerprints)
	}
}

func TestLoadOpsRunMetrics(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 14, 0, 0, 0, time.UTC)

	t.Run("missing ops_runs table returns zero metrics", func(t *testing.T) {
		db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatalf("OpenDB: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })

		got, err := LoadOpsRunMetrics(ctx, db, now)
		if err != nil {
			t.Fatalf("LoadOpsRunMetrics: %v", err)
		}
		if got.Running != 0 || got.Failed != 0 || got.Stale != 0 || len(got.Runs) != 0 {
			t.Fatalf("metrics = %+v, want zero counts", got)
		}
	})

	t.Run("counts rows by status and type", func(t *testing.T) {
		db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatalf("OpenDB: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("schema: %v", err)
		}

		freshStarted := now.Add(-time.Minute).Format("2006-01-02 15:04:05")
		failedStarted := now.Add(-5 * time.Minute).Format("2006-01-02 15:04:05")
		staleStarted := now.Add(-OpsRunStaleAfter - time.Second).Format("2006-01-02 15:04:05")
		if _, err := db.ExecContext(ctx, `
INSERT INTO ops_runs (type, bead_id, status, started_at)
VALUES
    ('decompose', 'oro-running', 'running', ?),
    ('decompose', 'oro-failed', 'failed', ?),
    ('diagnosis', 'oro-stale', 'running', ?);
`, freshStarted, failedStarted, staleStarted); err != nil {
			t.Fatalf("seed ops_runs: %v", err)
		}

		got, err := LoadOpsRunMetrics(ctx, db, now)
		if err != nil {
			t.Fatalf("LoadOpsRunMetrics: %v", err)
		}
		if got.Running != 1 || got.Failed != 1 || got.Stale != 1 {
			t.Fatalf("metrics = %+v, want running=1 failed=1 stale=1", got)
		}
		if got.ByType["decompose"].Running != 1 || got.ByType["decompose"].Failed != 1 {
			t.Fatalf("decompose metrics = %+v, want running=1 failed=1", got.ByType["decompose"])
		}
		if got.ByType["diagnosis"].Stale != 1 {
			t.Fatalf("diagnosis metrics = %+v, want stale=1", got.ByType["diagnosis"])
		}
		if !hasOpsRun(got, "oro-stale", "diagnosis", "stale") {
			t.Fatalf("stale ops run detail missing from %+v", got.Runs)
		}
	})
}

func findingByCode(health FactoryHealth, code string) (Finding, bool) {
	for _, finding := range health.Findings {
		if finding.Code == code {
			return finding, true
		}
	}
	return Finding{}, false
}

func hasFinding(health FactoryHealth, code string) bool {
	for _, finding := range health.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func evaluateWithAvailableStorage(snapshot Snapshot) FactoryHealth {
	snapshot.Storage = &StorageHealth{Available: true}
	return Evaluate(snapshot)
}

func hasOpsRun(metrics OpsRunMetrics, beadID, runType, status string) bool {
	for _, run := range metrics.Runs {
		if run.BeadID == beadID && run.Type == runType && run.Status == status {
			return true
		}
	}
	return false
}
