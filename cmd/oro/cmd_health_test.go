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
