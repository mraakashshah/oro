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
			got := Evaluate(tt.snapshot)
			if got.State != tt.wantState {
				t.Fatalf("state = %q, want %q; findings=%+v", got.State, tt.wantState, got.Findings)
			}
			if tt.wantFinding != "" && !hasFinding(got, tt.wantFinding) {
				t.Fatalf("missing finding %q in %+v", tt.wantFinding, got.Findings)
			}
		})
	}
}

func TestEvaluateAssignmentContradictions(t *testing.T) {
	got := Evaluate(Snapshot{
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
	got := Evaluate(Snapshot{
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

func TestLoadRecoveryQuarantineMetricsIgnoresHumanOwned(t *testing.T) {
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
INSERT INTO recovery_quarantines (bead_id, reason, details, status, resolved_at)
VALUES ('oro-human-owned', 'unsafe_stale_branch', 'operator owns branch', 'human_owned', datetime('now'));
`); err != nil {
		t.Fatalf("seed recovery quarantine: %v", err)
	}

	got, err := LoadRecoveryQuarantineMetrics(ctx, db)
	if err != nil {
		t.Fatalf("LoadRecoveryQuarantineMetrics: %v", err)
	}
	if got != 0 {
		t.Fatalf("open recovery quarantines = %d, want 0", got)
	}
}

func TestLoadActiveAssignments(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 14, 0, 0, 0, time.UTC)
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("bead schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO beads (id, title, status, type) VALUES
    ('oro-open', 'open task', 'open', 'task'),
    ('oro-progress', 'progress task', 'in_progress', 'task');
INSERT INTO assignments (id, bead_id, worker_id, worktree, status, assigned_at) VALUES
    (3, 'oro-progress', 'worker-progress', '/tmp/progress', 'active', '2026-05-19 13:50:00'),
    (4, 'oro-open', 'worker-open', '/tmp/open', 'completed', '2026-05-19 13:45:00'),
    (5, 'oro-missing', 'worker-missing', '/tmp/missing', 'active', '2026-05-19 14:05:00');
`); err != nil {
		t.Fatalf("seed assignments: %v", err)
	}

	got, err := LoadActiveAssignments(ctx, db, now)
	if err != nil {
		t.Fatalf("LoadActiveAssignments: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("active assignments = %+v, want 2 rows", got)
	}
	if got[0].ID != 3 || got[0].BeadID != "oro-progress" || got[0].WorkerID != "worker-progress" {
		t.Fatalf("first assignment payload = %+v", got[0])
	}
	if got[0].BeadStatus != "in_progress" || got[0].AgeSecs != 600 {
		t.Fatalf("first assignment status/age = %q/%.0f, want in_progress/600", got[0].BeadStatus, got[0].AgeSecs)
	}
	if got[1].BeadID != "oro-missing" || got[1].BeadStatus != "" {
		t.Fatalf("missing-bead assignment = %+v, want empty bead status", got[1])
	}
	if got[1].AgeSecs != 0 {
		t.Fatalf("future assignment age = %.0f, want 0", got[1].AgeSecs)
	}
}

func TestLoadThroughputMetrics(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 14, 0, 0, 0, time.UTC)
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("bead schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, status, assigned_at) VALUES
    ('oro-started', 'worker-started', '/tmp/started', 'active', '2026-05-19 13:55:00'),
    ('oro-old', 'worker-old', '/tmp/old', 'completed', '2026-05-19 12:30:00');
INSERT INTO beads (id, title, status, type, close_reason, closed_at, updated_at) VALUES
    ('oro-closed', 'closed task', 'closed', 'task', 'completed', '2026-05-19 13:58:00', '2026-05-19 13:58:00'),
    ('oro-deferred', 'deferred task', 'closed', 'task', 'deferred', '2026-05-19 13:59:00', '2026-05-19 13:59:00'),
    ('oro-open', 'open task', 'open', 'task', '', NULL, '2026-05-19 13:59:00');
INSERT INTO events (type, source, created_at) VALUES
    ('progress_timeout', 'dispatcher', '2026-05-19 13:57:00'),
    ('worker_heartbeat', 'worker', '2026-05-19 13:59:30');
`); err != nil {
		t.Fatalf("seed throughput data: %v", err)
	}

	got, err := LoadThroughputMetrics(ctx, db, now, 30*time.Minute)
	if err != nil {
		t.Fatalf("LoadThroughputMetrics: %v", err)
	}

	if got.WindowSecs != 1800 {
		t.Fatalf("window secs = %.0f, want 1800", got.WindowSecs)
	}
	if got.AssignmentsStarted != 1 {
		t.Fatalf("assignments started = %d, want 1", got.AssignmentsStarted)
	}
	if got.ProductiveClosures != 1 {
		t.Fatalf("productive closures = %d, want 1", got.ProductiveClosures)
	}
	if got.ProgressTimeouts != 1 {
		t.Fatalf("progress timeouts = %d, want 1", got.ProgressTimeouts)
	}
	if got.LastEventAgeSecs != 30 {
		t.Fatalf("last event age = %.0f, want 30", got.LastEventAgeSecs)
	}
}

func TestEvaluateNoManagerPaneFindingByDefault(t *testing.T) {
	got := Evaluate(Snapshot{
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
	got := Evaluate(Snapshot{
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
	got := Evaluate(Snapshot{
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
	got := Evaluate(Snapshot{
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
	got := Evaluate(Snapshot{
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
	got := Evaluate(Snapshot{
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

func hasOpsRun(metrics OpsRunMetrics, beadID, runType, status string) bool {
	for _, run := range metrics.Runs {
		if run.BeadID == beadID && run.Type == runType && run.Status == status {
			return true
		}
	}
	return false
}
