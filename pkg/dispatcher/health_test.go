package dispatcher //nolint:testpackage // white-box test needs internal access

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
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

func TestReadyObservationFailureBlocksAssignmentAndDegradesHealthAndStatus(t *testing.T) {
	d, beads, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	beads.readyErr = errors.New("injected ready observation failure")
	beads.beads = []protocol.Bead{{ID: "oro-ready-observation", Title: "Ready observation", Status: "open", Type: "task"}}
	d.cfg.StorageHealth = func(context.Context) *factoryhealth.StorageHealth {
		return &factoryhealth.StorageHealth{Available: true}
	}
	conn := newMockConn()
	d.mu.Lock()
	d.state = StateRunning
	d.workers["idle-ready-observation"] = &trackedWorker{
		id: "idle-ready-observation", conn: conn, encoder: json.NewEncoder(conn), state: protocol.WorkerIdle,
	}
	d.mu.Unlock()

	tryAssignAndWait(t, d, ctx)
	assertNoAssignmentMessage(t, conn)
	assertAssignmentObservationDegraded(t, d, "ready", "injected ready observation failure")
}

func TestCheckpointObservationFailureBlocksAssignmentAndDegradesHealthAndStatus(t *testing.T) {
	d, beads, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const beadID = "oro-checkpoint-observation"
	beads.beads = []protocol.Bead{{ID: beadID, Title: "Checkpoint observation", Status: "open", Type: "task"}}
	beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Title: "Checkpoint observation", Status: "open"}
	d.cfg.StorageHealth = func(context.Context) *factoryhealth.StorageHealth {
		return &factoryhealth.StorageHealth{Available: true}
	}
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}
	if _, err := d.db.ExecContext(ctx, `
DROP VIEW review_checkpoints_blocking_assignment;
CREATE VIEW review_checkpoints_blocking_assignment AS
SELECT id, bead_id FROM review_checkpoints WHERE missing_checkpoint_state = 1;`); err != nil {
		t.Fatalf("install failing checkpoint observation view: %v", err)
	}
	conn := newMockConn()
	d.mu.Lock()
	d.state = StateRunning
	d.workers["idle-checkpoint-observation"] = &trackedWorker{
		id: "idle-checkpoint-observation", conn: conn, encoder: json.NewEncoder(conn), state: protocol.WorkerIdle,
	}
	d.mu.Unlock()

	tryAssignAndWait(t, d, ctx)
	assertNoAssignmentMessage(t, conn)
	assertAssignmentObservationDegraded(t, d, "review_checkpoint", "missing_checkpoint_state")
}

func assertNoAssignmentMessage(t *testing.T, conn *mockConn) {
	t.Helper()
	conn.mu.Lock()
	writes := len(conn.written)
	conn.mu.Unlock()
	if writes != 0 {
		t.Fatalf("worker messages = %d, want no ASSIGN", writes)
	}
}

func assertAssignmentObservationDegraded(t *testing.T, d *Dispatcher, source, detail string) {
	t.Helper()
	healthJSON, err := d.applyHealth()
	if err != nil {
		t.Fatalf("applyHealth: %v", err)
	}
	var health SwarmHealth
	if err := json.Unmarshal([]byte(healthJSON), &health); err != nil {
		t.Fatalf("unmarshal health: %v", err)
	}
	assertAssignmentObservationFinding(t, health, source, detail)

	var status statusResponse
	if err := json.Unmarshal([]byte(d.buildStatusJSON()), &status); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if status.Health == nil {
		t.Fatal("status health = nil, want degraded health")
	}
	assertAssignmentObservationFinding(t, *status.Health, source, detail)
}

func assertAssignmentObservationFinding(t *testing.T, health SwarmHealth, source, detail string) {
	t.Helper()
	if health.State == factoryhealth.StateHealthy {
		t.Fatalf("health state = %q with unknown assignment admission, want non-healthy", health.State)
	}
	for _, finding := range health.Findings {
		if finding.Code == "assignment_admission_unknown" {
			if !strings.Contains(finding.Message, source) || !strings.Contains(finding.Message, detail) {
				t.Fatalf("assignment observation finding = %+v, want source %q detail %q", finding, source, detail)
			}
			return
		}
	}
	t.Fatalf("missing assignment admission unknown finding: %+v", health.Findings)
}

func TestDirectiveStatusStorageHealthMatchesHealthDirective(t *testing.T) {
	tests := []struct {
		name      string
		storage   func(context.Context) *factoryhealth.StorageHealth
		available bool
	}{
		{
			name: "healthy catalog",
			storage: func(context.Context) *factoryhealth.StorageHealth {
				return &factoryhealth.StorageHealth{Available: true}
			},
			available: true,
		},
		{
			name: "unavailable catalog",
			storage: func(context.Context) *factoryhealth.StorageHealth {
				return &factoryhealth.StorageHealth{Available: false}
			},
			available: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _, _, _, _, _ := newTestDispatcher(t)
			d.cfg.StorageHealth = tt.storage

			healthJSON, err := d.applyDirective(protocol.DirectiveHealth, "")
			if err != nil {
				t.Fatalf("apply health directive: %v", err)
			}
			var health factoryhealth.FactoryHealth
			if err := json.Unmarshal([]byte(healthJSON), &health); err != nil {
				t.Fatalf("unmarshal health: %v", err)
			}

			statusJSON, err := d.applyDirective(protocol.DirectiveStatus, "")
			if err != nil {
				t.Fatalf("apply status directive: %v", err)
			}
			var status statusResponse
			if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
				t.Fatalf("unmarshal status: %v", err)
			}
			if status.Health == nil {
				t.Fatal("status health missing")
			}
			if !reflect.DeepEqual(health.Metrics.Storage, status.Health.Metrics.Storage) {
				t.Fatalf("storage snapshots differ: health=%+v status=%+v", health.Metrics.Storage, status.Health.Metrics.Storage)
			}
			healthAvailable := health.Metrics.Storage != nil && health.Metrics.Storage.Available
			statusAvailable := status.Health.Metrics.Storage != nil && status.Health.Metrics.Storage.Available
			if healthAvailable != tt.available || statusAvailable != tt.available {
				t.Fatalf("storage availability = health:%t status:%t, want %t", healthAvailable, statusAvailable, tt.available)
			}
			if hasHealthFinding(health, factoryhealth.FindingStorageUnavailable) != !tt.available ||
				hasHealthFinding(*status.Health, factoryhealth.FindingStorageUnavailable) != !tt.available {
				t.Fatalf("storage findings = health:%+v status:%+v", health.Findings, status.Health.Findings)
			}
		})
	}
}

func TestDirectiveStatusRefreshesStorageHealthInsideThrottleWindow(t *testing.T) {
	tests := []struct {
		name string
		from factoryhealth.StorageHealth
		to   factoryhealth.StorageHealth
	}{
		{
			name: "catalog becomes unavailable",
			from: factoryhealth.StorageHealth{Available: true},
			to:   factoryhealth.StorageHealth{Available: false},
		},
		{
			name: "catalog recovers",
			from: factoryhealth.StorageHealth{Available: false},
			to:   factoryhealth.StorageHealth{Available: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _, _, _, _, _ := newTestDispatcher(t)
			storage := tt.from
			d.cfg.StorageHealth = func(context.Context) *factoryhealth.StorageHealth {
				snapshot := storage
				return &snapshot
			}
			cancel := startDispatcher(t, d)
			defer cancel()

			warmStatus := sendDirectiveWithArgs(t, d.cfg.SocketPath, string(protocol.DirectiveStatus), "")
			if !warmStatus.OK {
				t.Fatalf("warm status cache: %s", warmStatus.Detail)
			}
			storage = tt.to

			healthACK := sendDirectiveWithArgs(t, d.cfg.SocketPath, string(protocol.DirectiveHealth), "")
			if !healthACK.OK {
				t.Fatalf("apply health directive: %s", healthACK.Detail)
			}
			var health factoryhealth.FactoryHealth
			if err := json.Unmarshal([]byte(healthACK.Detail), &health); err != nil {
				t.Fatalf("unmarshal health: %v", err)
			}

			statusACK := sendDirectiveWithArgs(t, d.cfg.SocketPath, string(protocol.DirectiveStatus), "")
			if !statusACK.OK {
				t.Fatalf("apply cached status directive: %s", statusACK.Detail)
			}
			var status statusResponse
			if err := json.Unmarshal([]byte(statusACK.Detail), &status); err != nil {
				t.Fatalf("unmarshal status: %v", err)
			}
			if status.Health == nil {
				t.Fatal("status health missing")
			}
			if !reflect.DeepEqual(health.Metrics.Storage, status.Health.Metrics.Storage) {
				t.Fatalf("storage snapshots differ after transition: health=%+v status=%+v", health.Metrics.Storage, status.Health.Metrics.Storage)
			}

			wantUnavailable := !tt.to.Available
			if hasHealthFinding(health, factoryhealth.FindingStorageUnavailable) != wantUnavailable ||
				hasHealthFinding(*status.Health, factoryhealth.FindingStorageUnavailable) != wantUnavailable {
				t.Fatalf("storage findings after transition = health:%+v status:%+v", health.Findings, status.Health.Findings)
			}
			if wantUnavailable {
				for _, finding := range status.Health.Findings {
					if finding.Code == factoryhealth.FindingStorageUnavailable && finding.Severity != factoryhealth.SeverityCritical {
						t.Fatalf("storage unavailable severity = %q, want %q", finding.Severity, factoryhealth.SeverityCritical)
					}
				}
			}
		})
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

func TestApplyHealthReportsAssignmentFrozenByQuarantine(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "oro-ready-health", Status: "open", Priority: 1, Type: "task"},
	})
	d.mu.Lock()
	d.state = StateRunning
	d.workers["w-idle-health"] = &trackedWorker{id: "w-idle-health", state: protocol.WorkerIdle}
	d.mu.Unlock()
	if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, branch, reason, details, status)
VALUES ('oro-frozen-health', 'agent/oro-frozen-health', 'unsafe_stale_branch', 'unmerged branch', 'open');
`); err != nil {
		t.Fatalf("insert recovery quarantine: %v", err)
	}
	if _, blocked := d.recoveryQuarantineAssignmentScope(ctx); !blocked {
		t.Fatal("assignment scope blocked = false, want true")
	}

	result, err := d.applyHealth()
	if err != nil {
		t.Fatalf("applyHealth: %v", err)
	}
	var health SwarmHealth
	if err := json.Unmarshal([]byte(result), &health); err != nil {
		t.Fatalf("unmarshal health: %v", err)
	}
	if !health.Metrics.AssignmentFrozenByQuarantine ||
		health.Metrics.BlockingRecoveryQuarantines != 1 ||
		health.Metrics.AssignmentFreezeReason != "open_recovery_quarantine" {
		t.Fatalf("assignment freeze metrics = %+v", health.Metrics)
	}
	if health.Metrics.IdleWorkers != 1 || health.Metrics.ReadyQueue != 1 {
		t.Fatalf("health capacity = idle %d ready %d, want idle 1 ready 1", health.Metrics.IdleWorkers, health.Metrics.ReadyQueue)
	}
	if !hasHealthFinding(health, factoryhealth.FindingAssignmentFrozenByQuarantine) {
		t.Fatalf("health missing assignment freeze finding: %+v", health.Findings)
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

func TestBuildStatusJSONReportsEpicBranchAdmissionMetrics(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS epic_branch_admissions (
    branch TEXT PRIMARY KEY,
    epic_id TEXT NOT NULL,
    target_branch TEXT NOT NULL,
    state TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 1,
    lease_token TEXT,
    lease_owner TEXT,
    lease_expires_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
INSERT INTO epic_branch_admissions
    (branch, epic_id, target_branch, state, generation, lease_token, lease_owner,
     lease_expires_at, created_at, updated_at)
VALUES
    ('epic/oro-status-blocked', 'oro-status-blocked', 'main', 'blocked', 2,
     NULL, NULL, NULL, '2026-08-03T00:00:00Z', '2026-08-03T00:00:00Z'),
    ('epic/oro-status-leased', 'oro-status-leased', 'main', 'leased', 3,
     'active-token', 'dispatcher-a', '2099-01-01T00:00:00Z',
     '2026-08-03T00:00:00Z', '2026-08-03T00:00:00Z'),
    ('epic/oro-status-expired', 'oro-status-expired', 'main', 'leased', 4,
     'expired-token', 'dispatcher-b', '2000-01-01T00:00:00Z',
     '2026-08-03T00:00:00Z', '2026-08-03T00:00:00Z');
`); err != nil {
		t.Fatalf("insert epic branch admissions: %v", err)
	}

	var status statusResponse
	if err := json.Unmarshal([]byte(d.buildStatusJSON()), &status); err != nil {
		t.Fatalf("unmarshal status JSON: %v", err)
	}
	if status.EpicBranchBlocksOpen != 1 || status.EpicBranchLeasesActive != 1 {
		t.Fatalf("top-level epic branch metrics = blocks:%d leases:%d, want 1/1", status.EpicBranchBlocksOpen, status.EpicBranchLeasesActive)
	}
	if status.Health == nil {
		t.Fatal("status health missing")
	}
	if status.Health.Metrics.EpicBranchBlocksOpen != 1 || status.Health.Metrics.EpicBranchLeasesActive != 1 {
		t.Fatalf("nested epic branch metrics = blocks:%d leases:%d, want 1/1", status.Health.Metrics.EpicBranchBlocksOpen, status.Health.Metrics.EpicBranchLeasesActive)
	}
	if status.AssignmentFrozenByQuarantine || status.Health.Metrics.AssignmentFrozenByQuarantine {
		t.Fatalf("branch-scoped block changed global assignment freeze: %+v", status)
	}
}

func TestStatusReportsAssignmentFrozenByQuarantine(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "oro-ready", Status: "open", Priority: 1, Type: "task"},
	})
	d.mu.Lock()
	d.state = StateRunning
	d.workers["w-idle"] = &trackedWorker{id: "w-idle", state: protocol.WorkerIdle}
	d.mu.Unlock()

	if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, branch, reason, details, status)
VALUES ('oro-frozen', 'agent/oro-frozen', 'unsafe_stale_branch', 'unmerged branch', 'open');
`); err != nil {
		t.Fatalf("insert recovery quarantine: %v", err)
	}

	if redeployable, blocked := d.recoveryQuarantineAssignmentScope(ctx); !blocked || len(redeployable) != 0 {
		t.Fatalf("assignment scope = redeployable %+v blocked %t, want globally blocked", redeployable, blocked)
	}

	var frozen statusResponse
	if err := json.Unmarshal([]byte(d.buildStatusJSON()), &frozen); err != nil {
		t.Fatalf("unmarshal frozen status JSON: %v", err)
	}
	if !frozen.AssignmentFrozenByQuarantine {
		t.Fatal("status assignment_frozen_by_quarantine = false, want true")
	}
	if frozen.BlockingRecoveryQuarantines != 1 {
		t.Fatalf("status blocking recovery quarantines = %d, want 1", frozen.BlockingRecoveryQuarantines)
	}
	if frozen.AssignmentFreezeReason != "open_recovery_quarantine" {
		t.Fatalf("status assignment freeze reason = %q, want open_recovery_quarantine", frozen.AssignmentFreezeReason)
	}
	if frozen.Health == nil {
		t.Fatal("status health missing")
	}
	if !frozen.Health.Metrics.AssignmentFrozenByQuarantine {
		t.Fatal("health assignment_frozen_by_quarantine = false, want true")
	}
	if frozen.Health.Metrics.BlockingRecoveryQuarantines != 1 {
		t.Fatalf("health blocking recovery quarantines = %d, want 1", frozen.Health.Metrics.BlockingRecoveryQuarantines)
	}
	if frozen.Health.Metrics.AssignmentFreezeReason != "open_recovery_quarantine" {
		t.Fatalf("health assignment freeze reason = %q, want open_recovery_quarantine", frozen.Health.Metrics.AssignmentFreezeReason)
	}
	if frozen.Health.Metrics.IdleWorkers != 1 || frozen.Health.Metrics.ReadyQueue != 1 {
		t.Fatalf("health capacity = idle %d ready %d, want idle 1 ready 1", frozen.Health.Metrics.IdleWorkers, frozen.Health.Metrics.ReadyQueue)
	}
	if !hasHealthFinding(*frozen.Health, factoryhealth.FindingAssignmentFrozenByQuarantine) {
		t.Fatalf("health missing assignment freeze finding: %+v", frozen.Health.Findings)
	}

	if _, err := d.db.ExecContext(ctx, `
UPDATE recovery_quarantines SET status='resolved', resolved_at=datetime('now') WHERE status='open';
`); err != nil {
		t.Fatalf("resolve recovery quarantine: %v", err)
	}
	if redeployable, blocked := d.recoveryQuarantineAssignmentScope(ctx); blocked || len(redeployable) != 0 {
		t.Fatalf("resolved assignment scope = redeployable %+v blocked %t, want open", redeployable, blocked)
	}

	var unfrozen statusResponse
	if err := json.Unmarshal([]byte(d.buildStatusJSON()), &unfrozen); err != nil {
		t.Fatalf("unmarshal unfrozen status JSON: %v", err)
	}
	if unfrozen.AssignmentFrozenByQuarantine || unfrozen.BlockingRecoveryQuarantines != 0 || unfrozen.AssignmentFreezeReason != "" {
		t.Fatalf("unfrozen status retained assignment gate: %+v", unfrozen)
	}
	if unfrozen.Health == nil {
		t.Fatal("unfrozen status health missing")
	}
	if unfrozen.Health.Metrics.AssignmentFrozenByQuarantine ||
		unfrozen.Health.Metrics.BlockingRecoveryQuarantines != 0 ||
		unfrozen.Health.Metrics.AssignmentFreezeReason != "" {
		t.Fatalf("unfrozen health retained assignment gate: %+v", unfrozen.Health.Metrics)
	}
	if hasHealthFinding(*unfrozen.Health, factoryhealth.FindingAssignmentFrozenByQuarantine) {
		t.Fatalf("unfrozen health retained assignment freeze finding: %+v", unfrozen.Health.Findings)
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
