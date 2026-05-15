package factoryhealth //nolint:testpackage // white-box tests keep snapshots concise.

import (
	"context"
	"path/filepath"
	"testing"

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
				ManagerPaneAlive:    true,
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
				ManagerPaneAlive:    true,
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
				ManagerPaneAlive:     true,
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
		ManagerPaneAlive:    true,
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
		ManagerPaneAlive:        true,
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

func hasFinding(health FactoryHealth, code string) bool {
	for _, finding := range health.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
