package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

// TestAutonomousIntakeRejectsForeignEvidence proves that an assignment-owned
// source cannot turn a stale pane from another source into a direct task
// mutation or a self-dependency. The source reports that it continued after
// the rejected proposal, so this is not an absence-only assertion.
func TestAutonomousIntakeRejectsForeignEvidence(t *testing.T) {
	h := newAutonomousIntakeHarness(t)
	controlDir := h.installRejectIntakeShim(t)
	h.start(t)
	h.migrateRejectBeadSchema(t)
	h.createRejectIntakeTask(t)
	h.storeForeignEvidence(t, "evidence-source-b")

	h.directive(t, protocol.DirectiveStart, "")
	h.runCLI(t, "worker", "launch", "--id", h.externalWorkerID)
	h.waitForSourceAAssignment(t)

	for _, stage := range []string{"identity", "create-denied", "self-edge-denied", "foreign-evidence-rejected", "resumed"} {
		if got := waitForFileText(t, filepath.Join(controlDir, stage)); got != "ok" {
			t.Fatalf("source A %s marker = %q, want ok", stage, got)
		}
	}
	if got := h.assignmentCount(t); got != 1 {
		t.Fatalf("assignment count = %d, want source A only", got)
	}
	if got := h.beadCount(t); got != 1 {
		t.Fatalf("bead count = %d, want source A only", got)
	}
	if got := h.dependencyCount(t); got != 0 {
		t.Fatalf("dependency count = %d, want no self-edge", got)
	}
	if got := h.proposalCount(t); got != 0 {
		t.Fatalf("proposal count = %d, want foreign evidence rejected before admission", got)
	}
}

func (h *autonomousIntakeHarness) migrateRejectBeadSchema(t *testing.T) {
	t.Helper()
	if err := protocol.MigrateBeadSchema(context.Background(), h.db); err != nil {
		t.Fatalf("migrate native bead schema: %v", err)
	}
}

func (h *autonomousIntakeHarness) waitForSourceAAssignment(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		err := h.db.QueryRow(`
SELECT COUNT(*) FROM assignments
WHERE worker_id = ? AND bead_id = ? AND status = 'active'`, h.externalWorkerID, "oro-intake-source-a").Scan(&count)
		if err == nil && count == 1 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("source A did not receive an active assignment")
}

func (h *autonomousIntakeHarness) installRejectIntakeShim(t *testing.T) string {
	t.Helper()
	controlDir := filepath.Join(h.rootDir, "reject-shim")
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		t.Fatalf("mkdir reject shim control directory: %v", err)
	}
	t.Setenv("ORO_INTAKE_REJECT_CONTROL_DIR", controlDir)

	shim := filepath.Join(h.binDir, "codex")
	const script = `#!/bin/sh
set -eu
control_dir=${ORO_INTAKE_REJECT_CONTROL_DIR:?}
cat >/dev/null

if [ "${ORO_WORKER_ID:-}" != "external-intake" ] || [ "${ORO_WORKER_BEAD_ID:-}" != "oro-intake-source-a" ] || [ ! -s "${ORO_CAPABILITY_FILE:-}" ]; then
  printf 'identity missing\n' >"$control_dir/error"
  exit 1
fi
printf 'ok\n' >"$control_dir/identity"

if oro task create --id oro-intake-illegal --title illegal --type bug --priority 0 --description illegal --acceptance-criteria illegal >"$control_dir/create.out" 2>"$control_dir/create.err"; then
  printf 'direct create unexpectedly succeeded\n' >"$control_dir/error"
  exit 1
fi
grep -q 'worker identity present' "$control_dir/create.err"
printf 'ok\n' >"$control_dir/create-denied"

if oro task dep add oro-intake-source-a oro-intake-source-a >"$control_dir/edge.out" 2>"$control_dir/edge.err"; then
  printf 'self edge unexpectedly succeeded\n' >"$control_dir/error"
  exit 1
fi
grep -q 'worker identity present' "$control_dir/edge.err"
printf 'ok\n' >"$control_dir/self-edge-denied"

if oro task propose-blocker --evidence-run evidence-source-b --fingerprint stale-pane --kind prerequisite --summary stale --client-id stale-pane >"$control_dir/proposal.out" 2>"$control_dir/proposal.err"; then
  printf 'foreign evidence unexpectedly succeeded\n' >"$control_dir/error"
  exit 1
fi
grep -q 'work proposal evidence run not found' "$control_dir/proposal.err"
printf 'ok\n' >"$control_dir/foreign-evidence-rejected"

printf 'ok\n' >"$control_dir/resumed"
while :; do sleep 1; done
`
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatalf("write reject intake shim: %v", err)
	}
	return controlDir
}

func (h *autonomousIntakeHarness) createRejectIntakeTask(t *testing.T) {
	t.Helper()
	store := beadstore.NewSQLiteStore(h.db)
	if _, err := store.Create(context.Background(), beadstore.CreateParams{
		ID:                 "oro-intake-source-a",
		Title:              "source A intake fixture",
		Type:               "task",
		Priority:           0,
		Description:        "prove stale intake evidence is rejected",
		AcceptanceCriteria: "source A continues after the rejection",
	}); err != nil {
		t.Fatalf("create source A task: %v", err)
	}
}

func (h *autonomousIntakeHarness) storeForeignEvidence(t *testing.T, evidenceID string) {
	t.Helper()
	var response protocol.Message
	err := submitWorkRequest(context.Background(), h.socketPath, protocol.Message{
		Type: protocol.MsgEvidenceRequest,
		EvidenceRequest: &protocol.EvidenceRequest{Evidence: protocol.EvidenceRun{
			ID:           evidenceID,
			AssignmentID: 999,
			WorkerID:     "source-b",
			BeadID:       "oro-intake-source-b",
			Kind:         "diagnostic",
			Status:       "completed",
		}},
	}, &response)
	if err != nil {
		t.Fatalf("submit foreign evidence over UDS: %v", err)
	}
	if response.Type != protocol.MsgEvidenceResponse || response.EvidenceResponse == nil || response.EvidenceResponse.Error != "" {
		t.Fatalf("foreign evidence response = %#v, want acknowledgement", response)
	}
}

func (h *autonomousIntakeHarness) beadCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM beads`).Scan(&count); err != nil {
		t.Fatalf("count beads: %v", err)
	}
	return count
}

func (h *autonomousIntakeHarness) dependencyCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM bead_deps`).Scan(&count); err != nil {
		t.Fatalf("count dependencies: %v", err)
	}
	return count
}

func (h *autonomousIntakeHarness) proposalCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM work_proposals`).Scan(&count); err != nil {
		t.Fatalf("count proposals: %v", err)
	}
	return count
}

func waitForFileText(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path) //nolint:gosec // test-owned deterministic path
		if err == nil && strings.TrimSpace(string(data)) != "" {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q", path)
	return ""
}
