package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"oro/pkg/factoryhealth"
	"oro/pkg/protocol"
)

type fakeMonitorRunner struct {
	health        factoryhealth.FactoryHealth
	calls         []string
	recentActions map[string]bool
	actions       []monitorAction
}

func (f *fakeMonitorRunner) FactoryHealth(context.Context) (factoryhealth.FactoryHealth, error) {
	return f.health, nil
}

func (f *fakeMonitorRunner) Resume(context.Context) error {
	f.calls = append(f.calls, "resume")
	return nil
}

func (f *fakeMonitorRunner) Scale(ctx context.Context, n int) error {
	_ = ctx
	f.calls = append(f.calls, "scale")
	return nil
}

func (f *fakeMonitorRunner) MaxWorkers(ctx context.Context, n int) error {
	_ = ctx
	f.calls = append(f.calls, "max-workers")
	return nil
}

func (f *fakeMonitorRunner) Pause(context.Context) error {
	f.calls = append(f.calls, "pause")
	return nil
}

func (f *fakeMonitorRunner) RestartDaemon(_ context.Context, workers, maxWorkers int) error {
	f.calls = append(f.calls, "restart-daemon:"+strconv.Itoa(workers)+":"+strconv.Itoa(maxWorkers))
	return nil
}

func (f *fakeMonitorRunner) RecentMonitorAction(_ context.Context, action, key string, _ time.Duration) (bool, error) {
	return f.recentActions[monitorActionDedupeKey(action, key)], nil
}

func (f *fakeMonitorRunner) PendingMonitorPause(context.Context) (monitorAction, bool, error) {
	for key := range f.recentActions {
		if !strings.HasPrefix(key, monitorActionQGChurnPause+"\x00") {
			continue
		}
		pauseKey := strings.TrimPrefix(key, monitorActionQGChurnPause+"\x00")
		if !f.recentActions[monitorActionDedupeKey(monitorActionQGChurnResume, pauseKey)] {
			return monitorAction{Action: monitorActionQGChurnPause, Key: pauseKey}, true, nil
		}
	}
	return monitorAction{}, false, nil
}

func (f *fakeMonitorRunner) RecordMonitorAction(_ context.Context, action monitorAction) error {
	if f.recentActions == nil {
		f.recentActions = make(map[string]bool)
	}
	f.recentActions[monitorActionDedupeKey(action.Action, action.Key)] = true
	f.actions = append(f.actions, action)
	return nil
}

func TestMonitorActionLedgerRecordsRecentActions(t *testing.T) {
	db, err := openStateDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	action := monitorAction{
		Action:  monitorActionDaemonRestart,
		Key:     "throughput_stall:2:2",
		Payload: `{"finding":"throughput_stall"}`,
	}

	recent, err := recentMonitorAction(ctx, db, action.Action, action.Key, time.Hour)
	if err != nil {
		t.Fatalf("recent before record: %v", err)
	}
	if recent {
		t.Fatal("action is recent before it is recorded")
	}
	if err := recordMonitorAction(ctx, db, action); err != nil {
		t.Fatalf("record monitor action: %v", err)
	}
	recent, err = recentMonitorAction(ctx, db, action.Action, action.Key, time.Hour)
	if err != nil {
		t.Fatalf("recent after record: %v", err)
	}
	if !recent {
		t.Fatal("recorded action was not reported as recent")
	}
}

func TestMonitorObserveModeNeverMutates(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateStalled,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingPausedWithReadyQueue, Severity: factoryhealth.SeverityWarning, RecommendedAction: "resume dispatcher"},
			{Code: factoryhealth.FindingIdleReadyQueue, Severity: factoryhealth.SeverityWarning, RecommendedAction: "scale workers"},
		},
		Metrics: factoryhealth.Metrics{ReadyQueue: 2, WorkerCount: 0},
	}}

	var buf bytes.Buffer
	state := newMonitorState()
	if err := runMonitorIteration(context.Background(), &buf, monitorConfig{targetWorkers: 2, maxWorkers: 2, act: false}, runner, state); err != nil {
		t.Fatalf("monitor iteration: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("observe mode mutated via calls %v", runner.calls)
	}
	if !strings.Contains(buf.String(), factoryhealth.FindingPausedWithReadyQueue) {
		t.Fatalf("observe output missing finding:\n%s", buf.String())
	}
}

func TestMonitorActResumesPausedQueueAndMaintainsWorkers(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateDegraded,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingPausedWithReadyQueue, Severity: factoryhealth.SeverityWarning},
		},
		Metrics: factoryhealth.Metrics{DaemonRunning: true, ReadyQueue: 3, WorkerCount: 1, TargetWorkers: 1, MaxWorkers: 1},
	}, recentActions: map[string]bool{
		monitorActionDedupeKey(monitorActionQGChurnPause, "qg:resume"): true,
	}}

	var buf bytes.Buffer
	state := newMonitorState()
	if err := runMonitorIteration(context.Background(), &buf, monitorConfig{targetWorkers: 2, maxWorkers: 2, act: true}, runner, state); err != nil {
		t.Fatalf("monitor iteration: %v", err)
	}

	want := []string{"max-workers", "scale", "resume"}
	if strings.Join(runner.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", runner.calls, want)
	}
}

func TestMonitorActPreservesOperatorPauseAcrossRepeatedCycles(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateDegraded,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingPausedWithReadyQueue, Severity: factoryhealth.SeverityWarning},
			{Code: factoryhealth.FindingQGIncidentsOpen, Severity: factoryhealth.SeverityError},
			{Code: factoryhealth.FindingQGIncidentIncrease, Severity: factoryhealth.SeverityError, Fingerprint: "qg:systemic"},
		},
		Metrics: factoryhealth.Metrics{
			DaemonRunning: true,
			ReadyQueue:    3,
			PauseSource:   "operator",
			PauseReason:   "operator_request",
		},
	}}
	cfg := monitorConfig{act: true, restartAfter: 2}
	state := newMonitorState()

	for i := 0; i < 3; i++ {
		if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, state); err != nil {
			t.Fatalf("iteration %d: %v", i+1, err)
		}
	}

	if got := strings.Join(runner.calls, ","); got != "" {
		t.Fatalf("calls = %q, want no silent resume of operator pause", got)
	}
}

func TestMonitorActPreservesOperatorPause(t *testing.T) {
	ctx := context.Background()
	cfg := monitorConfig{act: true}
	health := factoryhealth.FactoryHealth{
		State: factoryhealth.StateDegraded,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingPausedWithReadyQueue, Severity: factoryhealth.SeverityWarning},
		},
		Metrics: factoryhealth.Metrics{DaemonRunning: true, ReadyQueue: 1},
	}

	t.Run("operator pause is preserved without monitor ledger ownership", func(t *testing.T) {
		runner := &fakeMonitorRunner{health: health}

		if err := runMonitorIteration(ctx, &bytes.Buffer{}, cfg, runner, newMonitorState()); err != nil {
			t.Fatalf("monitor iteration: %v", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("calls = %v, want no resume for operator pause", runner.calls)
		}
	})

	t.Run("monitor owned QG pause resumes once after finding clears", func(t *testing.T) {
		runner := &fakeMonitorRunner{
			health: factoryhealth.FactoryHealth{
				State:    health.State,
				Findings: health.Findings,
				Metrics:  factoryhealth.Metrics{DaemonRunning: true, ReadyQueue: 1, PauseSource: "monitor"},
			},
			recentActions: map[string]bool{
				monitorActionDedupeKey(monitorActionQGChurnPause, "qg:resolved"): true,
			},
		}
		state := newMonitorState()

		if err := runMonitorIteration(ctx, &bytes.Buffer{}, cfg, runner, state); err != nil {
			t.Fatalf("first monitor iteration: %v", err)
		}
		if err := runMonitorIteration(ctx, &bytes.Buffer{}, cfg, runner, state); err != nil {
			t.Fatalf("second monitor iteration: %v", err)
		}
		if got := strings.Join(runner.calls, ","); got != "resume" {
			t.Fatalf("calls = %v, want one resume for monitor-owned pause", runner.calls)
		}
	})
}

func TestMonitorActWorkerTargetActionsSurviveMonitorRestart(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State:   factoryhealth.StateDegraded,
		Metrics: factoryhealth.Metrics{DaemonRunning: true, ReadyQueue: 3, WorkerCount: 1, TargetWorkers: 1, MaxWorkers: 1},
	}}
	cfg := monitorConfig{targetWorkers: 2, maxWorkers: 2, act: true}

	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, newMonitorState()); err != nil {
		t.Fatalf("first monitor iteration: %v", err)
	}
	if got := strings.Join(runner.calls, ","); got != "max-workers,scale" {
		t.Fatalf("initial calls = %v, want max-workers and scale", runner.calls)
	}

	runner.calls = nil
	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, newMonitorState()); err != nil {
		t.Fatalf("post-restart monitor iteration: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("post-restart monitor repeated worker target actions: %v", runner.calls)
	}
}

func TestMonitorActDoesNotRestartForRepeatedIdleReadyQueue(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateStalled,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingIdleReadyQueue, Severity: factoryhealth.SeverityWarning},
		},
		Metrics: factoryhealth.Metrics{DaemonRunning: true, ReadyQueue: 2, WorkerCount: 2, TargetWorkers: 2},
	}}
	cfg := monitorConfig{targetWorkers: 2, maxWorkers: 2, act: true, restartAfter: 2}
	state := newMonitorState()

	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, state); err != nil {
		t.Fatalf("first iteration: %v", err)
	}
	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, state); err != nil {
		t.Fatalf("second iteration: %v", err)
	}

	if len(runner.calls) != 0 {
		t.Fatalf("calls = %v, want no daemon restart for idle ready queue alone", runner.calls)
	}
}

func TestMonitorActRestartsAfterRepeatedThroughputStall(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateStalled,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingThroughputStall, Severity: factoryhealth.SeverityWarning},
		},
		Metrics: factoryhealth.Metrics{DaemonRunning: true, ReadyQueue: 2, WorkerCount: 2, TargetWorkers: 2},
	}}
	cfg := monitorConfig{targetWorkers: 2, maxWorkers: 2, act: true, restartAfter: 2}
	state := newMonitorState()

	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, state); err != nil {
		t.Fatalf("first iteration: %v", err)
	}
	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, state); err != nil {
		t.Fatalf("second iteration: %v", err)
	}

	if got := strings.Join(runner.calls, ","); got != "restart-daemon:2:2" {
		t.Fatalf("calls = %v, want bounded restart after repeated throughput stall", runner.calls)
	}
	if len(runner.actions) != 1 || runner.actions[0].Action != monitorActionDaemonRestart {
		t.Fatalf("recorded actions = %+v, want one daemon restart", runner.actions)
	}
}

func TestMonitorActDoesNotRestartThroughputStallWithActiveWorkers(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateStalled,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingThroughputStall, Severity: factoryhealth.SeverityWarning},
		},
		Metrics: factoryhealth.Metrics{
			ActiveWorkers: 2, DaemonRunning: true, ReadyQueue: 2, WorkerCount: 2, TargetWorkers: 2,
		},
	}}
	cfg := monitorConfig{targetWorkers: 2, maxWorkers: 2, act: true, restartAfter: 2}
	state := newMonitorState()

	for i := 0; i < 2; i++ {
		if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, state); err != nil {
			t.Fatalf("iteration %d: %v", i+1, err)
		}
	}

	if len(runner.calls) != 0 {
		t.Fatalf("calls = %v, want no daemon restart while workers are active", runner.calls)
	}
}

func TestMonitorActDaemonRestartSurvivesMonitorRestart(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateStalled,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingThroughputStall, Severity: factoryhealth.SeverityWarning},
		},
		Metrics: factoryhealth.Metrics{DaemonRunning: true, ReadyQueue: 2, WorkerCount: 2, TargetWorkers: 2},
	}}
	cfg := monitorConfig{targetWorkers: 2, maxWorkers: 2, act: true, restartAfter: 1}

	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, newMonitorState()); err != nil {
		t.Fatalf("first restart iteration: %v", err)
	}
	if got := strings.Join(runner.calls, ","); got != "restart-daemon:2:2" {
		t.Fatalf("initial calls = %v, want restart", runner.calls)
	}

	runner.calls = nil
	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, newMonitorState()); err != nil {
		t.Fatalf("post-restart iteration: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("post-restart monitor repeated daemon restart: %v", runner.calls)
	}
}

func TestMonitorActPausesRepeatedIncreasingQGChurn(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateDegraded,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingQGIncidentIncrease, Severity: factoryhealth.SeverityWarning, Fingerprint: "qg:telemetry"},
		},
		Metrics: factoryhealth.Metrics{DaemonRunning: true, OpenQGIncidents: 1, QGOccurrences30m: 1},
	}}
	cfg := monitorConfig{act: true, restartAfter: 2}
	state := newMonitorState()
	var buf bytes.Buffer

	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("first iteration: %v", err)
	}
	runner.health.Metrics.QGOccurrences30m = 2
	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("second iteration: %v", err)
	}
	runner.health.Metrics.QGOccurrences30m = 3
	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("third iteration: %v", err)
	}

	if got := strings.Join(runner.calls, ","); got != "pause" {
		t.Fatalf("calls = %v, want one pause after repeated QG churn", runner.calls)
	}
	if !strings.Contains(buf.String(), "blocked_by_qg_churn") || !strings.Contains(buf.String(), "qg:telemetry") {
		t.Fatalf("monitor output missing QG churn block:\n%s", buf.String())
	}
	if len(runner.actions) != 1 || runner.actions[0].Action != monitorActionQGChurnPause {
		t.Fatalf("recorded actions = %+v, want one QG churn pause", runner.actions)
	}
}

func TestMonitorActQGChurnPauseSurvivesMonitorRestart(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateDegraded,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingQGIncidentIncrease, Severity: factoryhealth.SeverityWarning, Fingerprint: "qg:restart"},
		},
		Metrics: factoryhealth.Metrics{
			DaemonRunning:    true,
			WorkerCount:      2,
			TargetWorkers:    2,
			MaxWorkers:       2,
			OpenQGIncidents:  1,
			QGOccurrences30m: 3,
		},
	}}
	cfg := monitorConfig{targetWorkers: 2, maxWorkers: 2, act: true, restartAfter: 1}
	state := newMonitorState()
	var buf bytes.Buffer

	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("first baseline iteration: %v", err)
	}
	runner.health.Metrics.QGOccurrences30m = 4
	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("first churn iteration: %v", err)
	}
	if got := strings.Join(runner.calls, ","); got != "pause" {
		t.Fatalf("initial calls = %v, want pause", runner.calls)
	}

	runner.calls = nil
	runner.health = factoryhealth.FactoryHealth{
		State: factoryhealth.StateStalled,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingQGIncidentIncrease, Severity: factoryhealth.SeverityWarning, Fingerprint: "qg:restart"},
			{Code: factoryhealth.FindingPausedWithReadyQueue, Severity: factoryhealth.SeverityWarning},
			{Code: factoryhealth.FindingThroughputStall, Severity: factoryhealth.SeverityError},
		},
		Metrics: factoryhealth.Metrics{
			DaemonRunning:    true,
			ReadyQueue:       3,
			WorkerCount:      0,
			TargetWorkers:    1,
			MaxWorkers:       1,
			OpenQGIncidents:  1,
			QGOccurrences30m: 5,
		},
	}
	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, newMonitorState()); err != nil {
		t.Fatalf("post-restart iteration: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("post-restart monitor mutated despite recorded QG pause: %v", runner.calls)
	}
	if got := strings.Count(buf.String(), "blocked_by_qg_churn"); got < 2 {
		t.Fatalf("monitor output missing durable QG churn block after restart:\n%s", buf.String())
	}
}

func TestMonitorActDoesNotPauseWhenQGOccurrenceCountIsStable(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateDegraded,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingQGIncidentIncrease, Severity: factoryhealth.SeverityWarning, Fingerprint: "qg:stable"},
		},
		Metrics: factoryhealth.Metrics{DaemonRunning: true, OpenQGIncidents: 1, QGOccurrences30m: 2},
	}}
	cfg := monitorConfig{act: true, restartAfter: 2}
	state := newMonitorState()

	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, state); err != nil {
		t.Fatalf("first iteration: %v", err)
	}
	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, state); err != nil {
		t.Fatalf("second iteration: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("calls = %v, want no pause for stable QG occurrence count", runner.calls)
	}
}

func TestMonitorActDifferentQGFingerprintCanPauseAfterReset(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateDegraded,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingQGIncidentIncrease, Severity: factoryhealth.SeverityWarning, Fingerprint: "qg:first"},
		},
		Metrics: factoryhealth.Metrics{DaemonRunning: true, OpenQGIncidents: 1, QGOccurrences30m: 1},
	}}
	cfg := monitorConfig{act: true, restartAfter: 1}
	state := newMonitorState()

	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, state); err != nil {
		t.Fatalf("first baseline iteration: %v", err)
	}
	runner.health.Metrics.QGOccurrences30m = 2
	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, state); err != nil {
		t.Fatalf("first churn iteration: %v", err)
	}
	runner.health.Findings[0].Fingerprint = "qg:second"
	runner.health.Metrics.QGOccurrences30m = 1
	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, state); err != nil {
		t.Fatalf("second baseline iteration: %v", err)
	}
	runner.health.Metrics.QGOccurrences30m = 2
	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, state); err != nil {
		t.Fatalf("second churn iteration: %v", err)
	}

	if got := strings.Join(runner.calls, ","); got != "pause,pause" {
		t.Fatalf("calls = %v, want pause for each repeatedly increasing fingerprint", runner.calls)
	}
}

func TestMonitorActQGChurnBlocksOtherMutations(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateDegraded,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingQGIncidentIncrease, Severity: factoryhealth.SeverityWarning, Fingerprint: "qg:churn"},
		},
		Metrics: factoryhealth.Metrics{
			DaemonRunning:    true,
			WorkerCount:      2,
			TargetWorkers:    2,
			MaxWorkers:       2,
			OpenQGIncidents:  1,
			QGOccurrences30m: 1,
		},
	}}
	var buf bytes.Buffer
	cfg := monitorConfig{targetWorkers: 2, maxWorkers: 2, act: true, restartAfter: 1}
	state := newMonitorState()

	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("baseline iteration: %v", err)
	}
	runner.health = factoryhealth.FactoryHealth{
		State: factoryhealth.StateStalled,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingQGIncidentIncrease, Severity: factoryhealth.SeverityWarning, Fingerprint: "qg:churn"},
			{Code: factoryhealth.FindingPausedWithReadyQueue, Severity: factoryhealth.SeverityWarning},
			{Code: factoryhealth.FindingThroughputStall, Severity: factoryhealth.SeverityError},
		},
		Metrics: factoryhealth.Metrics{
			DaemonRunning:    true,
			ReadyQueue:       3,
			WorkerCount:      0,
			TargetWorkers:    1,
			MaxWorkers:       1,
			OpenQGIncidents:  1,
			QGOccurrences30m: 2,
		},
	}
	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("churn iteration: %v", err)
	}
	if got := strings.Join(runner.calls, ","); got != "pause" {
		t.Fatalf("calls = %v, want QG churn pause only", runner.calls)
	}
	if !strings.Contains(buf.String(), "blocked_by_qg_churn") {
		t.Fatalf("monitor output missing QG churn block:\n%s", buf.String())
	}
}

func TestMonitorRestartUsesHealthWorkerTargetsWhenFlagsUnset(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateStalled,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingThroughputStall, Severity: factoryhealth.SeverityWarning},
		},
		Metrics: factoryhealth.Metrics{DaemonRunning: true, WorkerCount: 1, TargetWorkers: 3, MaxWorkers: 5},
	}}
	cfg := monitorConfig{act: true, restartAfter: 1}
	state := newMonitorState()

	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, state); err != nil {
		t.Fatalf("monitor iteration: %v", err)
	}

	if got := strings.Join(runner.calls, ","); got != "restart-daemon:3:5" {
		t.Fatalf("calls = %v, want restart with health worker targets", runner.calls)
	}
}

func TestMonitorRestartDoesNotStartWithMaxBelowTarget(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateStalled,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingThroughputStall, Severity: factoryhealth.SeverityWarning},
		},
		Metrics: factoryhealth.Metrics{DaemonRunning: true, WorkerCount: 1, TargetWorkers: 1, MaxWorkers: 1},
	}}
	cfg := monitorConfig{targetWorkers: 3, act: true, restartAfter: 1}
	state := newMonitorState()

	if err := runMonitorIteration(context.Background(), &bytes.Buffer{}, cfg, runner, state); err != nil {
		t.Fatalf("monitor iteration: %v", err)
	}

	if got := strings.Join(runner.calls, ","); got != "scale,restart-daemon:3:3" {
		t.Fatalf("calls = %v, want restart max raised to target", runner.calls)
	}
}

func TestCLIMonitorRestartUsesDetachedStartHandoff(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("oro-monitor-%d-%d.sock", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	t.Setenv("ORO_HOME", tmpDir)
	t.Setenv("ORO_PROJECT", "oro")
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", socketPath)
	t.Setenv("ORO_BEADSOURCE_MODE", "sqlite")

	hookPath := filepath.Join(tmpDir, "hooks", "oro-search-hook")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o750); err != nil {
		t.Fatalf("create hook directory: %v", err)
	}
	if err := os.WriteFile(hookPath, []byte("test hook\n"), 0o700); err != nil {
		t.Fatalf("write hook fixture: %v", err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(hookPath, future, future); err != nil {
		t.Fatalf("mark hook fixture current: %v", err)
	}

	previousRunFullStart := runFullStartFn
	t.Cleanup(func() { runFullStartFn = previousRunFullStart })
	var capturedArgs []string
	var capturedDetach bool
	runFullStartFn = func(_ io.Writer, workers, maxWorkers int, _, _ string, spawner DaemonSpawner, _ CmdRunner, _ func(int) error, _ time.Duration, _ func(time.Duration), _ time.Duration, detach bool) error {
		execSpawner, ok := spawner.(*ExecDaemonSpawner)
		if !ok {
			t.Fatalf("monitor restart spawner = %T, want *ExecDaemonSpawner", spawner)
		}
		capturedArgs = execSpawner.buildArgs(workers, maxWorkers)
		capturedDetach = detach
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	received := make(chan protocol.DirectivePayload, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		runMockDispatcher(ctx, t, socketPath, received)
	}()
	waitForSocket(t, socketPath, time.Second)

	if err := (&cliMonitorRunner{}).RestartDaemon(ctx, 3, 5); err != nil {
		t.Fatalf("RestartDaemon: %v", err)
	}
	if !capturedDetach {
		t.Fatal("monitor restart did not use detached start handoff")
	}
	if got := strings.Join(capturedArgs, " "); !strings.Contains(got, "--workers 3 --max-workers 5") {
		t.Fatalf("monitor restart daemon args = %q, want worker target 3/5", got)
	}
	for _, arg := range capturedArgs {
		if strings.HasPrefix(arg, "--base-branch=") {
			t.Fatalf("monitor restart emitted unexpected base branch argument %q", arg)
		}
	}

	select {
	case directive := <-received:
		if directive.Op != "restart-daemon" || directive.Source != "monitor" {
			t.Fatalf("monitor restart directive = %+v", directive)
		}
	case <-ctx.Done():
		t.Fatal("monitor restart directive was not received")
	}
	select {
	case <-serverDone:
	case <-ctx.Done():
		t.Fatal("monitor restart mock dispatcher did not stop")
	}
}

func TestMonitorActRefusesMutationWhenRecoveryQuarantineOpen(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateUnsafe,
		Findings: []factoryhealth.Finding{
			{
				Code:              factoryhealth.FindingRecoveryQuarantineOpen,
				Severity:          factoryhealth.SeverityCritical,
				Message:           "1 recovery quarantine is open",
				RecommendedAction: "inspect recovery quarantine before automation",
			},
			{Code: factoryhealth.FindingPausedWithReadyQueue, Severity: factoryhealth.SeverityWarning},
			{Code: factoryhealth.FindingIdleReadyQueue, Severity: factoryhealth.SeverityError},
		},
		Metrics: factoryhealth.Metrics{
			ReadyQueue:              3,
			WorkerCount:             0,
			TargetWorkers:           1,
			MaxWorkers:              1,
			OpenRecoveryQuarantines: 1,
		},
	}}

	var buf bytes.Buffer
	state := newMonitorState()
	cfg := monitorConfig{targetWorkers: 2, maxWorkers: 2, act: true, restartAfter: 1}
	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("monitor iteration: %v", err)
	}

	if len(runner.calls) != 0 {
		t.Fatalf("monitor --act mutated quarantined recovery state via calls %v", runner.calls)
	}
	if !strings.Contains(buf.String(), factoryhealth.FindingRecoveryQuarantineOpen) {
		t.Fatalf("monitor output missing recovery quarantine finding:\n%s", buf.String())
	}
}

func TestMonitorActDoesNotResolveFailedOpsRuns(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateStalled,
		Findings: []factoryhealth.Finding{
			{
				Code:              factoryhealth.FindingOpsRunFailed,
				Severity:          factoryhealth.SeverityError,
				Message:           "decompose ops run for oro-failed failed",
				RecommendedAction: "run oro ops list, then oro ops retry 42 or oro ops resolve 42 <reason>",
			},
			{Code: factoryhealth.FindingPausedWithReadyQueue, Severity: factoryhealth.SeverityWarning},
			{Code: factoryhealth.FindingThroughputStall, Severity: factoryhealth.SeverityError},
		},
		Metrics: factoryhealth.Metrics{
			DaemonRunning: true,
			ReadyQueue:    3,
			WorkerCount:   0,
			TargetWorkers: 1,
			MaxWorkers:    1,
			OpsRuns: factoryhealth.OpsRunMetrics{
				Failed: 1,
				Runs: []factoryhealth.OpsRunSnapshot{
					{ID: 42, Type: "decompose", BeadID: "oro-failed", Status: "failed"},
				},
			},
		},
	}}

	var buf bytes.Buffer
	state := newMonitorState()
	cfg := monitorConfig{targetWorkers: 2, maxWorkers: 2, act: true, restartAfter: 1}
	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("monitor iteration: %v", err)
	}

	if len(runner.calls) != 0 {
		t.Fatalf("monitor --act mutated failed ops runs via calls %v", runner.calls)
	}
	got := buf.String()
	assertContainsAll(t, got, []string{
		factoryhealth.FindingOpsRunFailed,
		"blocked_by_ops_runs",
		"failed=1 stale=0",
		"oro ops list",
		"oro ops retry 42",
		"oro ops resolve 42",
	})
}

func TestMonitorActIgnoresStaleOpsRunFindingWhenCountsAreZero(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateStalled,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingOpsRunStale, Severity: factoryhealth.SeverityWarning},
			{Code: factoryhealth.FindingPausedWithReadyQueue, Severity: factoryhealth.SeverityWarning},
		},
		Metrics: factoryhealth.Metrics{
			DaemonRunning: true,
			ReadyQueue:    3,
			WorkerCount:   1,
			TargetWorkers: 1,
			MaxWorkers:    2,
			PauseSource:   "monitor",
			OpsRuns:       factoryhealth.OpsRunMetrics{},
		},
	}, recentActions: map[string]bool{
		monitorActionDedupeKey(monitorActionQGChurnPause, "qg:stale-ops"): true,
	}}

	var buf bytes.Buffer
	state := newMonitorState()
	cfg := monitorConfig{targetWorkers: 1, maxWorkers: 2, act: true, restartAfter: 1}
	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("monitor iteration: %v", err)
	}

	if strings.Contains(buf.String(), "blocked_by_ops_runs") {
		t.Fatalf("monitor blocked despite zero failed/stale ops runs:\n%s", buf.String())
	}
	if strings.Join(runner.calls, ",") != "resume" {
		t.Fatalf("calls = %v, want resume", runner.calls)
	}
}

func TestMonitorActPrintsRecoveryQuarantineBlockedOncePerCount(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateUnsafe,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingRecoveryQuarantineOpen, Severity: factoryhealth.SeverityCritical},
		},
		Metrics: factoryhealth.Metrics{OpenRecoveryQuarantines: 1},
	}}
	var buf bytes.Buffer
	state := newMonitorState()
	cfg := monitorConfig{targetWorkers: 2, maxWorkers: 2, act: true}

	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("first iteration: %v", err)
	}
	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("second iteration: %v", err)
	}
	if got := strings.Count(buf.String(), "blocked_by_recovery_quarantine"); got != 1 {
		t.Fatalf("blocked message count = %d, want 1\n%s", got, buf.String())
	}

	runner.health.Metrics.OpenRecoveryQuarantines = 2
	runner.health.Findings[0].Message = "2 recovery quarantines are open"
	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("third iteration: %v", err)
	}
	if got := strings.Count(buf.String(), "blocked_by_recovery_quarantine"); got != 2 {
		t.Fatalf("blocked message count after count change = %d, want 2\n%s", got, buf.String())
	}
}

func TestMonitorActRecoveryQuarantineBlockSurvivesMonitorRestart(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateUnsafe,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingRecoveryQuarantineOpen, Severity: factoryhealth.SeverityCritical},
		},
		Metrics: factoryhealth.Metrics{OpenRecoveryQuarantines: 1},
	}}
	cfg := monitorConfig{act: true}
	var first bytes.Buffer
	if err := runMonitorIteration(context.Background(), &first, cfg, runner, newMonitorState()); err != nil {
		t.Fatalf("first monitor iteration: %v", err)
	}
	if got := strings.Count(first.String(), "blocked_by_recovery_quarantine"); got != 1 {
		t.Fatalf("first blocked message count = %d, want 1\n%s", got, first.String())
	}

	var second bytes.Buffer
	if err := runMonitorIteration(context.Background(), &second, cfg, runner, newMonitorState()); err != nil {
		t.Fatalf("post-restart monitor iteration: %v", err)
	}
	if strings.Contains(second.String(), "blocked_by_recovery_quarantine") {
		t.Fatalf("post-restart monitor repeated recovery block message:\n%s", second.String())
	}
}

func TestMonitorFailOnUnsafeReturnsError(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateUnsafe,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingRecoveryQuarantineOpen, Severity: factoryhealth.SeverityCritical},
		},
		Metrics: factoryhealth.Metrics{OpenRecoveryQuarantines: 1},
	}}

	var buf bytes.Buffer
	state := newMonitorState()
	err := runMonitorIteration(context.Background(), &buf, monitorConfig{act: true, failOnUnsafe: true}, runner, state)
	if err == nil || !strings.Contains(err.Error(), "factory health unsafe") {
		t.Fatalf("runMonitorIteration error = %v, want factory health unsafe", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("monitor mutated unsafe state via calls %v", runner.calls)
	}
	if !strings.Contains(buf.String(), factoryhealth.FindingRecoveryQuarantineOpen) {
		t.Fatalf("monitor output missing unsafe finding:\n%s", buf.String())
	}
}

func TestMonitorActRefusesMutationWhenUnsafeWithoutRecoveryQuarantine(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateUnsafe,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingOrphanActiveAssignment, Severity: factoryhealth.SeverityCritical},
			{Code: factoryhealth.FindingIdleReadyQueue, Severity: factoryhealth.SeverityError},
		},
		Metrics: factoryhealth.Metrics{DaemonRunning: true, ReadyQueue: 3, WorkerCount: 0, TargetWorkers: 1, MaxWorkers: 1},
	}}

	var buf bytes.Buffer
	state := newMonitorState()
	cfg := monitorConfig{targetWorkers: 2, maxWorkers: 2, act: true, restartAfter: 1}
	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("monitor iteration: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("monitor mutated unsafe state via calls %v", runner.calls)
	}
	if !strings.Contains(buf.String(), "blocked_by_unsafe_health") {
		t.Fatalf("monitor output missing unsafe block:\n%s", buf.String())
	}
}

func TestMonitorActRefusesMutationWhenDaemonStopped(t *testing.T) {
	runner := &fakeMonitorRunner{health: factoryhealth.FactoryHealth{
		State:   factoryhealth.StateStopped,
		Metrics: factoryhealth.Metrics{DaemonRunning: false, ReadyQueue: 3, WorkerCount: 0},
	}}

	var buf bytes.Buffer
	state := newMonitorState()
	cfg := monitorConfig{targetWorkers: 2, maxWorkers: 2, act: true}
	if err := runMonitorIteration(context.Background(), &buf, cfg, runner, state); err != nil {
		t.Fatalf("monitor iteration: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("monitor mutated stopped daemon via calls %v", runner.calls)
	}
	if !strings.Contains(buf.String(), "blocked_by_daemon_stopped") {
		t.Fatalf("monitor output missing stopped block:\n%s", buf.String())
	}
}
