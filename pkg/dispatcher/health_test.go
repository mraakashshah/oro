package dispatcher //nolint:testpackage // white-box test needs internal access

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/factoryhealth"
	"oro/pkg/protocol"
)

func TestApplyHealth(t *testing.T) {
	// Create a test dispatcher
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	// Register a worker to verify it appears in health data
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	// Drain clientConn so writes don't block
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := clientConn.Read(buf); err != nil {
				return
			}
		}
	}()

	d.registerWorker("worker-1", serverConn)

	// Call applyHealth
	result, err := d.applyHealth()
	if err != nil {
		t.Fatalf("applyHealth failed: %v", err)
	}

	// Verify it returns valid JSON
	var health SwarmHealth
	if err := json.Unmarshal([]byte(result), &health); err != nil {
		t.Fatalf("Failed to unmarshal health JSON: %v", err)
	}

	// Assert: Returns daemon PID
	if health.Metrics.DaemonPID != os.Getpid() {
		t.Errorf("Expected daemon PID %d, got %d", os.Getpid(), health.Metrics.DaemonPID)
	}

	// Assert: Returns daemon state
	if health.State == "" {
		t.Error("Expected daemon state to be set")
	}

	// Assert: Returns worker statuses
	if health.Metrics.WorkerCount != 1 {
		t.Errorf("Expected 1 worker, got %d", health.Metrics.WorkerCount)
	}

	if health.Metrics.DaemonRunning != true {
		t.Error("Expected daemon_running=true")
	}

	// Note: PaneStatus assertions require DB setup and will be validated in integration tests
}

func TestApplyHealthReportsStorageUnavailableWithoutObservation(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	result, err := d.applyHealth()
	if err != nil {
		t.Fatalf("applyHealth: %v", err)
	}

	var health SwarmHealth
	if err := json.Unmarshal([]byte(result), &health); err != nil {
		t.Fatalf("unmarshal health: %v", err)
	}
	if !hasHealthFinding(health, factoryhealth.FindingStorageUnavailable) {
		t.Fatalf("missing storage unavailable finding: %+v", health.Findings)
	}
}

func TestApplyHealthViaDirective(t *testing.T) {
	// Create a test dispatcher
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	// Test that health directive is wired correctly
	result, err := d.applyDirective("health", "")
	if err != nil {
		t.Fatalf("applyDirective(health) failed: %v", err)
	}

	// Verify it returns valid JSON
	var health SwarmHealth
	if err := json.Unmarshal([]byte(result), &health); err != nil {
		t.Fatalf("Failed to unmarshal health JSON via directive: %v", err)
	}

	// Assert: Returns daemon PID
	if health.Metrics.DaemonPID != os.Getpid() {
		t.Errorf("Expected daemon PID %d, got %d", os.Getpid(), health.Metrics.DaemonPID)
	}
}

func TestApplyHealthReportsRecoveryQuarantine(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, branch, reason, details, status)
VALUES ('oro-q', 'agent/oro-q', 'missing_worktree_path', 'missing path', 'open');
`); err != nil {
		t.Fatalf("insert recovery quarantine: %v", err)
	}

	result, err := d.applyHealth()
	if err != nil {
		t.Fatalf("applyHealth: %v", err)
	}

	var health SwarmHealth
	if err := json.Unmarshal([]byte(result), &health); err != nil {
		t.Fatalf("unmarshal health: %v", err)
	}
	if health.Metrics.OpenRecoveryQuarantines != 1 {
		t.Fatalf("open recovery quarantines = %d, want 1", health.Metrics.OpenRecoveryQuarantines)
	}
	if !hasHealthFinding(health, factoryhealth.FindingRecoveryQuarantineOpen) {
		t.Fatalf("missing recovery quarantine finding: %+v", health.Findings)
	}
}

func TestBuildStatusJSONReportsRecoveryQuarantineHealth(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, branch, reason, details, status)
VALUES ('oro-status-q', 'agent/oro-status-q', 'missing_worktree_path', 'missing path', 'open');
`); err != nil {
		t.Fatalf("insert recovery quarantine: %v", err)
	}

	var status statusResponse
	if err := json.Unmarshal([]byte(d.buildStatusJSON()), &status); err != nil {
		t.Fatalf("unmarshal status JSON: %v", err)
	}
	if status.Health == nil {
		t.Fatal("status health missing")
	}
	if status.Health.Metrics.OpenRecoveryQuarantines != 1 {
		t.Fatalf("status health open recovery quarantines = %d, want 1", status.Health.Metrics.OpenRecoveryQuarantines)
	}
	if !hasHealthFinding(*status.Health, factoryhealth.FindingRecoveryQuarantineOpen) {
		t.Fatalf("status health missing recovery quarantine finding: %+v", status.Health.Findings)
	}
}

func TestBuildStatusJSONOmitsManagerPaneHealthSurface(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	data := d.buildStatusJSON()
	var status statusResponse
	if err := json.Unmarshal([]byte(data), &status); err != nil {
		t.Fatalf("unmarshal status JSON: %v", err)
	}
	if status.Health == nil {
		t.Fatal("status health missing")
	}
	for _, legacy := range []string{"manager_pane_alive", "manager_pane_unhealthy"} {
		if strings.Contains(data, legacy) {
			t.Fatalf("status JSON still contains legacy manager pane surface %q: %s", legacy, data)
		}
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
		if got.Running != 0 || got.Failed != 0 || got.Stale != 0 {
			t.Fatalf("metrics = %+v, want zero counts", got)
		}
	})

	t.Run("counts running failed and stale rows", func(t *testing.T) {
		db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatalf("OpenDB: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("schema: %v", err)
		}

		freshStarted := now.Add(-time.Minute).Format("2006-01-02 15:04:05")
		oldStarted := now.Add(-factoryhealth.OpsRunStaleAfter - time.Second).Format("2006-01-02 15:04:05")
		failedStarted := now.Add(-5 * time.Minute).Format("2006-01-02 15:04:05")
		if _, err := db.ExecContext(ctx, `
INSERT INTO ops_runs (type, bead_id, status, started_at)
VALUES
    ('decompose', 'oro-running', 'running', ?),
    ('decompose', 'oro-failed', 'failed', ?),
    ('diagnosis', 'oro-stale', 'running', ?);
`, freshStarted, failedStarted, oldStarted); err != nil {
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
		if !hasOpsRun(got, "oro-failed", "decompose", "failed") {
			t.Fatalf("failed ops run detail missing from %+v", got.Runs)
		}
		if !hasOpsRun(got, "oro-stale", "diagnosis", "stale") {
			t.Fatalf("stale ops run detail missing from %+v", got.Runs)
		}
	})
}

func hasHealthFinding(health SwarmHealth, code string) bool {
	for _, finding := range health.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func hasOpsRun(metrics factoryhealth.OpsRunMetrics, beadID, runType, status string) bool {
	for _, run := range metrics.Runs {
		if run.BeadID == beadID && run.Type == runType && run.Status == status {
			return true
		}
	}
	return false
}
