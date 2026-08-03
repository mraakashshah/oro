package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"oro/pkg/factoryhealth"
	"oro/pkg/protocol"
)

func TestHealthCmdJSONStoppedDaemonWithActiveAssignments(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

	db, err := openStateDB(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO beads (id, title, status, type) VALUES ('oro-a', 'A', 'in_progress', 'task');
INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-a', 'w-1', '/tmp/oro-a', 'active');
`); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"health", "--json"})

	if err := root.Execute(); err != nil {
		t.Fatalf("health command failed: %v", err)
	}

	var got factoryhealth.FactoryHealth
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("health --json invalid JSON: %v\nraw: %s", err, buf.String())
	}
	if got.State != factoryhealth.StateUnsafe {
		t.Fatalf("state = %q, want unsafe; health=%+v", got.State, got)
	}
	if !healthHasFinding(got, factoryhealth.FindingDaemonStoppedWithActiveAssignments) {
		t.Fatalf("missing daemon_stopped_with_active_assignments finding: %+v", got.Findings)
	}
}

func TestHealthCmdHumanSummarizesFindings(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

	db, err := openStateDB(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO beads (id, title, status, type) VALUES ('oro-a', 'A', 'open', 'task');
INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-a', 'w-1', '/tmp/oro-a', 'active');
`); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"health"})

	if err := root.Execute(); err != nil {
		t.Fatalf("health command failed: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "unsafe") || !strings.Contains(got, "daemon_stopped_with_active_assignments") {
		t.Fatalf("human health output missing state/finding:\n%s", got)
	}
}

func TestHealthCmdJSONReportsRecoveryQuarantine(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

	db, err := openStateDB(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, assignment_id, worker_id, worktree, branch, reason, details, status)
VALUES ('oro-q', 12, 'w1', '/tmp/wt-q', 'agent/oro-q', 'missing_worktree_path', 'missing path', 'open');
`); err != nil {
		t.Fatalf("seed quarantine: %v", err)
	}

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"health", "--json"})

	if err := root.Execute(); err != nil {
		t.Fatalf("health command failed: %v", err)
	}

	var got factoryhealth.FactoryHealth
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("health --json invalid JSON: %v\nraw: %s", err, buf.String())
	}
	if got.Metrics.OpenRecoveryQuarantines != 1 {
		t.Fatalf("open recovery quarantines = %d, want 1", got.Metrics.OpenRecoveryQuarantines)
	}
	if !healthHasFinding(got, factoryhealth.FindingRecoveryQuarantineOpen) {
		t.Fatalf("missing recovery quarantine finding: %+v", got.Findings)
	}
}

func TestEpicBranchAdmissionMetricsAreAdditiveToHealthAndStatus(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", t.TempDir())
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

	d, db, err := buildDispatcherWithReviewTimeouts(0, 0, 0, 0, 0, false, "", false, false, "")
	if err != nil {
		t.Fatalf("build dispatcher: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO epic_branch_admissions
    (branch, epic_id, target_branch, state, generation, lease_token, lease_owner,
     lease_expires_at, created_at, updated_at)
VALUES
    ('epic/oro-blocked', 'oro-blocked', 'main', 'blocked', 2, NULL, NULL,
     NULL, '2026-08-03T00:00:00Z', '2026-08-03T00:00:00Z'),
    ('epic/oro-leased', 'oro-leased', 'main', 'leased', 3, 'active-token', 'dispatcher-a',
     '2099-01-01T00:00:00Z', '2026-08-03T00:00:00Z', '2026-08-03T00:00:00Z'),
    ('epic/oro-expired', 'oro-expired', 'main', 'leased', 4, 'expired-token', 'dispatcher-b',
     '2000-01-01T00:00:00Z', '2026-08-03T00:00:00Z', '2026-08-03T00:00:00Z'),
    ('epic/oro-resolved', 'oro-resolved', 'main', 'resolved', 5, NULL, NULL,
     NULL, '2026-08-03T00:00:00Z', '2026-08-03T00:00:00Z');
`); err != nil {
		t.Fatalf("seed epic branch admissions: %v", err)
	}

	live, err := d.Health()
	if err != nil {
		t.Fatalf("load live health: %v", err)
	}
	paths, err := ResolveDaemonPaths()
	if err != nil {
		t.Fatalf("resolve daemon paths: %v", err)
	}
	offline, err := loadLocalFactoryHealth(ctx, paths.StateDBPath, false, 0, "stopped")
	if err != nil {
		t.Fatalf("load offline health: %v", err)
	}
	assertEpicBranchHealthMetrics(t, live)
	assertEpicBranchHealthMetrics(t, offline)
	if live.Metrics.EpicBranchBlocksOpen != offline.Metrics.EpicBranchBlocksOpen ||
		live.Metrics.EpicBranchLeasesActive != offline.Metrics.EpicBranchLeasesActive {
		t.Fatalf("live/offline epic branch metrics differ: live=%+v offline=%+v", live.Metrics, offline.Metrics)
	}

	healthJSON := executeEpicBranchHealthJSON(t, "health", "--json")
	assertEpicBranchJSONNumber(t, healthJSON, []string{"metrics", "epic_branch_blocks_open"}, 1)
	assertEpicBranchJSONNumber(t, healthJSON, []string{"metrics", "epic_branch_leases_active"}, 1)

	statusJSON := executeEpicBranchHealthJSON(t, "status", "--json")
	assertEpicBranchJSONNumber(t, statusJSON, []string{"epic_branch_blocks_open"}, 1)
	assertEpicBranchJSONNumber(t, statusJSON, []string{"epic_branch_leases_active"}, 1)
	assertEpicBranchJSONNumber(t, statusJSON, []string{"health", "metrics", "epic_branch_blocks_open"}, 1)
	assertEpicBranchJSONNumber(t, statusJSON, []string{"health", "metrics", "epic_branch_leases_active"}, 1)

	var legacy struct {
		State   string `json:"state"`
		Metrics struct {
			DaemonRunning     bool `json:"daemon_running"`
			WorkerCount       int  `json:"worker_count"`
			ReadyQueue        int  `json:"ready_queue"`
			ActiveAssignments int  `json:"active_assignments"`
		} `json:"metrics"`
	}
	healthBytes, err := json.Marshal(healthJSON)
	if err != nil {
		t.Fatalf("marshal health map: %v", err)
	}
	if err := json.Unmarshal(healthBytes, &legacy); err != nil {
		t.Fatalf("legacy health consumer failed to decode additive JSON: %v", err)
	}
	if legacy.Metrics.DaemonRunning || legacy.Metrics.WorkerCount != 0 ||
		legacy.Metrics.ReadyQueue != 0 || legacy.Metrics.ActiveAssignments != 0 {
		t.Fatalf("preexisting health values changed: %+v", legacy)
	}

	healthHuman := executeEpicBranchHealthHuman(t, "health")
	for _, want := range []string{"health:", "posture:", "workers:", "queue:", "assignments:", "findings:", "epic_branch_admission_blocked"} {
		if !strings.Contains(healthHuman, want) {
			t.Fatalf("health human output missing %q:\n%s", want, healthHuman)
		}
	}
	if got := executeEpicBranchHealthHuman(t, "status"); got != "dispatcher: stopped\n" {
		t.Fatalf("stopped status human output changed: %q", got)
	}

	t.Run("missing table reports additive zeros", func(t *testing.T) {
		legacyDB, err := openDB(filepath.Join(t.TempDir(), "legacy.db"))
		if err != nil {
			t.Fatalf("open legacy db: %v", err)
		}
		defer legacyDB.Close()

		metrics, err := factoryhealth.LoadEpicBranchAdmissionMetrics(ctx, legacyDB, time.Now())
		if err != nil {
			t.Fatalf("missing table must be rolling-upgrade safe: %v", err)
		}
		if metrics.Blocked != 0 || metrics.ActiveLeases != 0 {
			t.Fatalf("missing-table metrics = %+v, want zero", metrics)
		}
		zeroHealth := factoryhealth.Evaluate(factoryhealth.Snapshot{EpicBranchAdmissions: metrics})
		zeroJSON, err := json.Marshal(zeroHealth)
		if err != nil {
			t.Fatalf("marshal zero health: %v", err)
		}
		var raw map[string]any
		if err := json.Unmarshal(zeroJSON, &raw); err != nil {
			t.Fatalf("decode zero health: %v", err)
		}
		assertEpicBranchJSONNumber(t, raw, []string{"metrics", "epic_branch_blocks_open"}, 0)
		assertEpicBranchJSONNumber(t, raw, []string{"metrics", "epic_branch_leases_active"}, 0)
	})
}

func assertEpicBranchHealthMetrics(t *testing.T, health factoryhealth.FactoryHealth) {
	t.Helper()
	if health.Metrics.EpicBranchBlocksOpen != 1 || health.Metrics.EpicBranchLeasesActive != 1 {
		t.Fatalf("epic branch metrics = blocks:%d leases:%d, want 1/1", health.Metrics.EpicBranchBlocksOpen, health.Metrics.EpicBranchLeasesActive)
	}
	var branchFindings int
	for _, finding := range health.Findings {
		if finding.Code == factoryhealth.FindingAssignmentFrozenByQuarantine {
			t.Fatalf("branch block must not claim global assignment freeze: %+v", finding)
		}
		if finding.Code != factoryhealth.FindingEpicBranchAdmissionBlocked {
			continue
		}
		branchFindings++
		if finding.Severity != factoryhealth.SeverityWarning || finding.Component != "epic_branch" {
			t.Fatalf("epic branch finding scope = %+v, want warning/epic_branch", finding)
		}
	}
	if branchFindings != 1 {
		t.Fatalf("epic branch findings = %d, want 1: %+v", branchFindings, health.Findings)
	}
	if health.Metrics.AssignmentFrozenByQuarantine || health.Metrics.BlockingRecoveryQuarantines != 0 || health.Metrics.AssignmentFreezeReason != "" {
		t.Fatalf("epic branch block changed global freeze metrics: %+v", health.Metrics)
	}
}

func executeEpicBranchHealthJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("oro %s failed: %v", strings.Join(args, " "), err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("oro %s invalid JSON: %v\nraw: %s", strings.Join(args, " "), err, buf.String())
	}
	return got
}

func executeEpicBranchHealthHuman(t *testing.T, args ...string) string {
	t.Helper()
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("oro %s failed: %v", strings.Join(args, " "), err)
	}
	return buf.String()
}

func assertEpicBranchJSONNumber(t *testing.T, raw map[string]any, path []string, want float64) {
	t.Helper()
	var current any = raw
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("JSON path %s parent = %T, want object", strings.Join(path, "."), current)
		}
		current, ok = object[key]
		if !ok {
			t.Fatalf("JSON missing additive field %s: %+v", strings.Join(path, "."), raw)
		}
	}
	got, ok := current.(float64)
	if !ok || got != want {
		t.Fatalf("JSON %s = %#v, want %v", strings.Join(path, "."), current, want)
	}
}

func TestHealthCmdJSONReconcilesClosedQGIncidentBeads(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

	db, err := openStateDB(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO qg_failure_incidents
    (id, fingerprint, class, decision, confidence, reason, summary, status, occurrence_count)
VALUES
    (1, 'qg:already-fixed', 'systemic', 'create_or_reuse_infra', 'high', 'fixed elsewhere', 'already fixed', 'open', 1);
INSERT INTO beads (id, title, status, type)
VALUES ('oro-qg-incident-1', 'QG incident 1', 'closed', 'bug');
`); err != nil {
		t.Fatalf("seed qg incident: %v", err)
	}

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"health", "--json"})

	if err := root.Execute(); err != nil {
		t.Fatalf("health command failed: %v", err)
	}

	var got factoryhealth.FactoryHealth
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("health --json invalid JSON: %v\nraw: %s", err, buf.String())
	}
	if got.Metrics.OpenQGIncidents != 0 {
		t.Fatalf("open qg incidents = %d, want 0; health=%+v", got.Metrics.OpenQGIncidents, got)
	}
	if healthHasFinding(got, factoryhealth.FindingQGIncidentsOpen) {
		t.Fatalf("closed qg incident bead should not emit qg_incidents_open finding: %+v", got.Findings)
	}

	var dbStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM qg_failure_incidents WHERE id=1`).Scan(&dbStatus); err != nil {
		t.Fatalf("query qg incident status: %v", err)
	}
	if dbStatus != "closed" {
		t.Fatalf("qg incident db status = %q, want closed", dbStatus)
	}
}

func TestHealthJSONIncludesOpsRunMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

	db, err := openStateDB(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	recentStarted := now.Add(-5 * time.Minute).Format("2006-01-02 15:04:05")
	staleStarted := now.Add(-factoryhealth.OpsRunStaleAfter - time.Minute).Format("2006-01-02 15:04:05")
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO ops_runs (type, bead_id, status, started_at)
VALUES
  ('decompose', 'oro-active', 'running', ?),
  ('decompose', 'oro-failed', 'failed', ?),
  ('diagnosis', 'oro-stale', 'running', ?);
`, recentStarted, recentStarted, staleStarted); err != nil {
		t.Fatalf("seed ops runs: %v", err)
	}

	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"health", "--json"})

	if err := root.Execute(); err != nil {
		t.Fatalf("health command failed: %v", err)
	}

	var got factoryhealth.FactoryHealth
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("health --json invalid JSON: %v\nraw: %s", err, buf.String())
	}
	if got.Metrics.OpsRuns.Running != 1 || got.Metrics.OpsRuns.Failed != 1 || got.Metrics.OpsRuns.Stale != 1 {
		t.Fatalf("ops run metrics = %+v, want 1 running, 1 failed, 1 stale", got.Metrics.OpsRuns)
	}
	if len(got.Metrics.OpsRuns.Runs) != 3 {
		t.Fatalf("ops run snapshots = %d, want 3: %+v", len(got.Metrics.OpsRuns.Runs), got.Metrics.OpsRuns.Runs)
	}
	if !healthFindingActionContains(got, factoryhealth.FindingOpsRunFailed, "oro ops list", "oro ops retry", "oro ops resolve") {
		t.Fatalf("missing failed ops run action with list/retry/resolve: %+v", got.Findings)
	}
	if !healthFindingActionContains(got, factoryhealth.FindingOpsRunStale, "oro ops list", "oro ops retry", "oro ops resolve") {
		t.Fatalf("missing stale ops run action with list/retry/resolve: %+v", got.Findings)
	}
}

func TestStorageHealthParity(t *testing.T) {
	tmpDir := t.TempDir()
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

	d, db, err := buildDispatcherWithReviewTimeouts(0, 0, 0, 0, 0, false, "", false, false, "")
	if err != nil {
		t.Fatalf("build dispatcher: %v", err)
	}
	defer func() { _ = db.Close() }()

	paths, err := ResolveDaemonPaths()
	if err != nil {
		t.Fatalf("resolve daemon paths: %v", err)
	}
	offline, err := loadLocalFactoryHealth(context.Background(), paths.StateDBPath, false, 0, "stopped")
	if err != nil {
		t.Fatalf("load offline health: %v", err)
	}
	live, err := d.Health()
	if err != nil {
		t.Fatalf("load live health: %v", err)
	}
	assertStorageHealthParity(t, live, offline, true)

	storagePaths, err := ResolveStoragePaths(oroHome)
	if err != nil {
		t.Fatalf("resolve storage paths: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(storagePaths.CatalogPath), 0o750); err != nil {
		t.Fatalf("create storage directory: %v", err)
	}
	if err := os.WriteFile(storagePaths.CatalogPath, []byte("corrupt catalog"), 0o600); err != nil {
		t.Fatalf("corrupt storage catalog: %v", err)
	}
	offline, err = loadLocalFactoryHealth(context.Background(), paths.StateDBPath, false, 0, "stopped")
	if err != nil {
		t.Fatalf("load offline health after catalog corruption: %v", err)
	}
	live, err = d.Health()
	if err != nil {
		t.Fatalf("load live health after catalog corruption: %v", err)
	}
	assertStorageHealthParity(t, live, offline, false)
}

func assertStorageHealthParity(t *testing.T, live, offline factoryhealth.FactoryHealth, available bool) {
	t.Helper()
	if !reflect.DeepEqual(live.Metrics.Storage, offline.Metrics.Storage) {
		t.Fatalf("storage JSON fields differ: live=%+v offline=%+v", live.Metrics.Storage, offline.Metrics.Storage)
	}
	if live.Metrics.Storage == nil || live.Metrics.Storage.Available != available {
		t.Fatalf("live storage = %+v, want available=%t", live.Metrics.Storage, available)
	}
	if got, want := storageHealthFindings(live), storageHealthFindings(offline); !reflect.DeepEqual(got, want) {
		t.Fatalf("storage findings differ: live=%+v offline=%+v", got, want)
	}
	if !available && !healthHasFinding(live, factoryhealth.FindingStorageUnavailable) {
		t.Fatalf("live health missing unavailable storage finding: %+v", live.Findings)
	}
}

func storageHealthFindings(health factoryhealth.FactoryHealth) []factoryhealth.Finding {
	findings := make([]factoryhealth.Finding, 0)
	for _, finding := range health.Findings {
		if finding.Component == "storage" {
			findings = append(findings, finding)
		}
	}
	return findings
}

func healthHasFinding(health factoryhealth.FactoryHealth, code string) bool {
	for _, finding := range health.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func healthFindingActionContains(health factoryhealth.FactoryHealth, code string, parts ...string) bool {
	for _, finding := range health.Findings {
		if finding.Code != code {
			continue
		}
		for _, part := range parts {
			if !strings.Contains(finding.RecommendedAction, part) {
				return false
			}
		}
		return true
	}
	return false
}
