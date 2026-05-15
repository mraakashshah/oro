package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

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

func healthHasFinding(health factoryhealth.FactoryHealth, code string) bool {
	for _, finding := range health.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
