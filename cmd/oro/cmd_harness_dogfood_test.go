package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"oro/pkg/factoryhealth"
)

type dogfoodHarnessTestRunner struct {
	health        factoryhealth.FactoryHealth
	calls         []string
	recentActions map[string]bool
}

func (r *dogfoodHarnessTestRunner) FactoryHealth(context.Context) (factoryhealth.FactoryHealth, error) {
	return r.health, nil
}

func (r *dogfoodHarnessTestRunner) Resume(context.Context) error {
	r.calls = append(r.calls, "resume")
	r.health.Findings = nil
	return nil
}

func (r *dogfoodHarnessTestRunner) Pause(context.Context) error {
	r.calls = append(r.calls, "pause")
	return nil
}

func (r *dogfoodHarnessTestRunner) Scale(_ context.Context, workers int) error {
	r.calls = append(r.calls, "scale:"+strconv.Itoa(workers))
	return nil
}

func (r *dogfoodHarnessTestRunner) MaxWorkers(_ context.Context, workers int) error {
	r.calls = append(r.calls, "max-workers:"+strconv.Itoa(workers))
	return nil
}

func (r *dogfoodHarnessTestRunner) RestartDaemon(_ context.Context, workers, maxWorkers int) error {
	r.calls = append(r.calls, "restart:"+strconv.Itoa(workers)+":"+strconv.Itoa(maxWorkers))
	return nil
}

func (r *dogfoodHarnessTestRunner) RecentMonitorAction(_ context.Context, action, key string, _ time.Duration) (bool, error) {
	return r.recentActions[monitorActionDedupeKey(action, key)], nil
}

func (r *dogfoodHarnessTestRunner) PendingMonitorPause(context.Context) (monitorAction, bool, error) {
	for key := range r.recentActions {
		if !strings.HasPrefix(key, monitorActionQGChurnPause+"\x00") {
			continue
		}
		pauseKey := strings.TrimPrefix(key, monitorActionQGChurnPause+"\x00")
		if !r.recentActions[monitorActionDedupeKey(monitorActionQGChurnResume, pauseKey)] {
			return monitorAction{Action: monitorActionQGChurnPause, Key: pauseKey}, true, nil
		}
	}
	return monitorAction{}, false, nil
}

func (r *dogfoodHarnessTestRunner) RecordMonitorAction(_ context.Context, action monitorAction) error {
	if r.recentActions == nil {
		r.recentActions = make(map[string]bool)
	}
	r.recentActions[monitorActionDedupeKey(action.Action, action.Key)] = true
	return nil
}

func TestDogfoodHarnessSeedsRunsAndAssertsInvariants(t *testing.T) {
	dbPath := dogfoodHarnessTestEnv(t)
	var out bytes.Buffer

	seedCmd := newHarnessDogfoodCmdWithRunner(&dogfoodHarnessTestRunner{})
	seedCmd.SetOut(&out)
	seedCmd.SetErr(&out)
	seedCmd.SetArgs([]string{"seed"})
	if err := seedCmd.Execute(); err != nil {
		t.Fatalf("seed command error: %v\n%s", err, out.String())
	}
	assertContainsAll(t, out.String(), []string{
		"seeded dogfood scenario default",
		"oro-dogfood-noop-merge",
		"state_db=" + dbPath,
	})

	db := openDogfoodTestDB(t, dbPath)
	closeSeededDogfoodWork(t, db, "oro-dogfood-noop-merge")

	runner := &dogfoodHarnessTestRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateDegraded,
		Findings: []factoryhealth.Finding{
			{Code: factoryhealth.FindingPausedWithReadyQueue, Severity: factoryhealth.SeverityWarning},
		},
		Metrics: factoryhealth.Metrics{
			DaemonRunning: true,
			ReadyQueue:    1,
			WorkerCount:   1,
			TargetWorkers: 1,
			MaxWorkers:    1,
			PauseSource:   "monitor",
		},
	}, recentActions: map[string]bool{
		monitorActionDedupeKey(monitorActionQGChurnPause, "dogfood:recovery"): true,
	}}
	out.Reset()
	runCmd := newHarnessDogfoodCmdWithRunner(runner)
	runCmd.SetOut(&out)
	runCmd.SetErr(&out)
	runCmd.SetArgs([]string{"run", "--iterations", "2", "--workers", "2", "--interval", "1ms"})
	if err := runCmd.Execute(); err != nil {
		t.Fatalf("run command error: %v\n%s", err, out.String())
	}
	if got := strings.Join(runner.calls, ","); got != "max-workers:2,scale:2,resume" {
		t.Fatalf("run calls = %q, want monitor target maintenance and resume", got)
	}
	if !strings.Contains(out.String(), "monitor --act iterations=2 workers=2") {
		t.Fatalf("run output missing finite monitor detail:\n%s", out.String())
	}

	out.Reset()
	assertCmd := newHarnessDogfoodCmdWithRunner(&dogfoodHarnessTestRunner{})
	assertCmd.SetOut(&out)
	assertCmd.SetErr(&out)
	assertCmd.SetArgs([]string{"assert"})
	if err := assertCmd.Execute(); err != nil {
		t.Fatalf("assert command error after closed work: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "dogfood invariants PASS") {
		t.Fatalf("assert output missing PASS:\n%s", out.String())
	}

	reopenSeededDogfoodWork(t, db, "oro-dogfood-noop-merge")
	out.Reset()
	assertCmd = newHarnessDogfoodCmdWithRunner(&dogfoodHarnessTestRunner{})
	assertCmd.SetOut(&out)
	assertCmd.SetErr(&out)
	assertCmd.SetArgs([]string{"assert"})
	err := assertCmd.Execute()
	if err == nil {
		t.Fatalf("assert succeeded with ready seeded work:\n%s", out.String())
	}
	assertContainsAll(t, out.String(), []string{
		"dogfood invariants FAIL",
		"ready seeded work",
		"oro-dogfood-noop-merge",
		"state_db=" + dbPath,
	})
}

func TestDogfoodHarnessReliabilityV2ScenarioExercisesHardeningPaths(t *testing.T) {
	dbPath := dogfoodHarnessTestEnv(t)
	db := openDogfoodTestDB(t, dbPath)

	var out bytes.Buffer
	seedCmd := newHarnessDogfoodCmdWithRunner(&dogfoodHarnessTestRunner{})
	seedCmd.SetOut(&out)
	seedCmd.SetErr(&out)
	seedCmd.SetArgs([]string{"seed", "--scenario", "reliability-v2"})
	if err := seedCmd.Execute(); err != nil {
		t.Fatalf("seed reliability-v2 error: %v\n%s", err, out.String())
	}
	assertContainsAll(t, out.String(), []string{
		"seeded dogfood scenario reliability-v2",
		"oro-dogfood-target-cleanup",
	})
	if strings.Contains(out.String(), "oro-dogfood-noop-merge") {
		t.Fatalf("reliability-v2 should use one deterministic seeded task, got:\n%s", out.String())
	}

	out.Reset()
	runCmd := newHarnessDogfoodCmdWithRunner(&dogfoodHarnessTestRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateUnsafe,
		Findings: []factoryhealth.Finding{{
			Code:     factoryhealth.FindingOpsRunFailed,
			Severity: factoryhealth.SeverityCritical,
			Message:  "dogfood reliability-v2 ops run failed",
		}},
		Metrics: factoryhealth.Metrics{
			DaemonRunning: true,
			OpsRuns: factoryhealth.OpsRunMetrics{
				Failed: 1,
			},
		},
	}})
	runCmd.SetOut(&out)
	runCmd.SetErr(&out)
	runCmd.SetArgs([]string{"run", "--scenario", "reliability-v2", "--iterations", "1", "--interval", "1ms"})
	if err := runCmd.Execute(); err != nil {
		t.Fatalf("run reliability-v2 error: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), factoryhealth.FindingOpsRunFailed) {
		t.Fatalf("run reliability-v2 output missing ops failure visibility:\n%s", out.String())
	}

	closeSeededDogfoodWork(t, db, "oro-dogfood-target-cleanup")
	insertDogfoodEvent(t, db, "merge_noop", "oro-dogfood-target-cleanup", `{"target":"dogfood-target"}`)

	out.Reset()
	assertCmd := newHarnessDogfoodCmdWithRunner(&dogfoodHarnessTestRunner{})
	assertCmd.SetOut(&out)
	assertCmd.SetErr(&out)
	assertCmd.SetArgs([]string{"assert", "--scenario", "reliability-v2"})
	if err := assertCmd.Execute(); err != nil {
		t.Fatalf("assert reliability-v2 error: %v\n%s", err, out.String())
	}
	assertContainsAll(t, out.String(), []string{
		"dogfood invariants PASS",
		"target-aware cleanup evidence",
		"no-op merge closure evidence",
		"ops failure visibility evidence",
	})

	if _, err := db.Exec(`DELETE FROM events WHERE type=?`, dogfoodReliabilityV2OpsVisibilityEvent); err != nil {
		t.Fatalf("delete ops visibility evidence: %v", err)
	}
	out.Reset()
	assertCmd = newHarnessDogfoodCmdWithRunner(&dogfoodHarnessTestRunner{})
	assertCmd.SetOut(&out)
	assertCmd.SetErr(&out)
	assertCmd.SetArgs([]string{"assert", "--scenario", "reliability-v2"})
	err := assertCmd.Execute()
	if err == nil {
		t.Fatalf("assert reliability-v2 succeeded without ops evidence:\n%s", out.String())
	}
	assertContainsAll(t, out.String(), []string{
		"dogfood invariants FAIL",
		"missing reliability-v2 ops failure visibility evidence",
	})
}

func TestMonitorIterationsActKeepsHealthyFactory(t *testing.T) {
	runner := &dogfoodHarnessTestRunner{health: factoryhealth.FactoryHealth{
		State: factoryhealth.StateHealthy,
		Metrics: factoryhealth.Metrics{
			DaemonRunning: true,
			WorkerCount:   2,
			TargetWorkers: 2,
			MaxWorkers:    2,
		},
	}}
	var out bytes.Buffer
	cfg := monitorConfig{
		targetWorkers: 2,
		maxWorkers:    2,
		interval:      time.Millisecond,
		act:           true,
		iterations:    2,
		restartAfter:  1,
	}

	if err := runMonitor(context.Background(), &out, cfg, runner, newMonitorState()); err != nil {
		t.Fatalf("runMonitor finite healthy factory: %v\n%s", err, out.String())
	}
	if len(runner.calls) != 0 {
		t.Fatalf("healthy finite monitor mutated factory: %v", runner.calls)
	}
	if got := strings.Count(out.String(), "health=healthy"); got != 2 {
		t.Fatalf("health line count = %d, want 2\n%s", got, out.String())
	}
}

func dogfoodHarnessTestEnv(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	t.Setenv("ORO_HOME", filepath.Join(tmpDir, "oro-home"))
	t.Setenv("ORO_PROJECT", "dogfood-test")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	return dbPath
}

func openDogfoodTestDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func closeSeededDogfoodWork(t *testing.T, db *sql.DB, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := db.Exec(
			`UPDATE beads SET status='closed', closed_at=datetime('now'), close_reason='dogfood test' WHERE id=?`,
			id,
		); err != nil {
			t.Fatalf("close seeded work %s: %v", id, err)
		}
	}
}

func reopenSeededDogfoodWork(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(
		`UPDATE beads SET status='open', closed_at=NULL, close_reason=NULL WHERE id=?`,
		id,
	); err != nil {
		t.Fatalf("reopen seeded work %s: %v", id, err)
	}
}

func insertDogfoodEvent(t *testing.T, db *sql.DB, eventType, beadID, payload string) {
	t.Helper()
	if !json.Valid([]byte(payload)) {
		t.Fatalf("invalid event payload fixture: %s", payload)
	}
	if _, err := db.Exec(
		`INSERT INTO events (type, source, bead_id, worker_id, payload) VALUES (?, 'dogfood-test', ?, 'worker-test', ?)`,
		eventType,
		beadID,
		payload,
	); err != nil {
		t.Fatalf("insert event %s/%s: %v", eventType, beadID, err)
	}
}
