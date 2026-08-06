package factoryhealth_test

import (
	"testing"

	"oro/pkg/factoryhealth"
)

func TestEvaluateFactoryHealthBoundarySemantics(t *testing.T) {
	t.Parallel()

	busyWorker := func(beadID string, progress, heartbeat float64) factoryhealth.WorkerSnapshot {
		return factoryhealth.WorkerSnapshot{
			ID:                "worker-boundary",
			State:             "busy",
			BeadID:            beadID,
			LastProgressSecs:  progress,
			LastHeartbeatSecs: heartbeat,
		}
	}
	base := func() factoryhealth.Snapshot {
		return factoryhealth.Snapshot{
			DaemonRunning:        true,
			ProgressTimeoutSecs:  60,
			HeartbeatTimeoutSecs: 30,
		}
	}

	tests := []struct {
		name     string
		snapshot factoryhealth.Snapshot
		code     string
		want     bool
	}{
		{
			name: "progress exactly at timeout is stalled",
			snapshot: func() factoryhealth.Snapshot {
				s := base()
				s.Workers = []factoryhealth.WorkerSnapshot{busyWorker("oro-progress", 60, 1)}
				return s
			}(),
			code: factoryhealth.FindingAliveNoProgress,
			want: true,
		},
		{
			name: "missing heartbeat age is not evidence of a live stall",
			snapshot: func() factoryhealth.Snapshot {
				s := base()
				s.Workers = []factoryhealth.WorkerSnapshot{busyWorker("oro-no-heartbeat", 60, 0)}
				return s
			}(),
			code: factoryhealth.FindingAliveNoProgress,
		},
		{
			name: "heartbeat exactly at timeout is not alive",
			snapshot: func() factoryhealth.Snapshot {
				s := base()
				s.Workers = []factoryhealth.WorkerSnapshot{busyWorker("oro-heartbeat-timeout", 60, 30)}
				return s
			}(),
			code: factoryhealth.FindingAliveNoProgress,
		},
		{
			name: "busy worker without a bead is not evaluated for progress",
			snapshot: func() factoryhealth.Snapshot {
				s := base()
				s.Workers = []factoryhealth.WorkerSnapshot{busyWorker("", 60, 1)}
				return s
			}(),
			code: factoryhealth.FindingAliveNoProgress,
		},
		{
			name: "paused empty queue is not blocked work",
			snapshot: func() factoryhealth.Snapshot {
				s := base()
				s.DispatcherState = "paused"
				return s
			}(),
			code: factoryhealth.FindingPausedWithReadyQueue,
		},
		{
			name: "running ready queue without idle workers is not idle",
			snapshot: func() factoryhealth.Snapshot {
				s := base()
				s.DispatcherState = "running"
				s.ReadyQueue = 1
				s.Workers = []factoryhealth.WorkerSnapshot{busyWorker("oro-busy", 0, 1)}
				return s
			}(),
			code: factoryhealth.FindingIdleReadyQueue,
		},
		{
			name: "idle worker outside running state is not an idle queue stall",
			snapshot: func() factoryhealth.Snapshot {
				s := base()
				s.DispatcherState = "starting"
				s.ReadyQueue = 1
				s.Workers = []factoryhealth.WorkerSnapshot{{ID: "worker-idle", State: "idle"}}
				return s
			}(),
			code: factoryhealth.FindingIdleReadyQueue,
		},
		{
			name: "unknown bead status is not a known mismatch",
			snapshot: func() factoryhealth.Snapshot {
				s := base()
				s.ActiveAssignments = []factoryhealth.AssignmentSnapshot{{ID: 1, BeadID: "oro-unknown", BeadStatus: ""}}
				return s
			}(),
			code: factoryhealth.FindingAssignmentBeadStatusMismatch,
		},
		{
			name: "in progress bead agrees with active assignment",
			snapshot: func() factoryhealth.Snapshot {
				s := base()
				s.ActiveAssignments = []factoryhealth.AssignmentSnapshot{{ID: 2, BeadID: "oro-active", BeadStatus: "in_progress"}}
				return s
			}(),
			code: factoryhealth.FindingAssignmentBeadStatusMismatch,
		},
		{
			name: "equal incident and occurrence counts are not an increase",
			snapshot: func() factoryhealth.Snapshot {
				s := base()
				s.OpenQGIncidents = 2
				s.QGOccurrences30m = 2
				return s
			}(),
			code: factoryhealth.FindingQGIncidentIncrease,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := hasFactoryFinding(factoryhealth.Evaluate(tt.snapshot), tt.code)
			if got != tt.want {
				t.Fatalf("finding %q present = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestEvaluateFactoryHealthQGIncreaseWithoutFingerprint(t *testing.T) {
	t.Parallel()
	health := factoryhealth.Evaluate(factoryhealth.Snapshot{
		DaemonRunning:    true,
		OpenQGIncidents:  1,
		QGOccurrences30m: 2,
	})
	if !hasFactoryFinding(health, factoryhealth.FindingQGIncidentIncrease) {
		t.Fatalf("missing %q in %+v", factoryhealth.FindingQGIncidentIncrease, health.Findings)
	}
}

func hasFactoryFinding(health factoryhealth.FactoryHealth, code string) bool {
	for _, finding := range health.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
