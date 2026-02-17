package ops //nolint:testpackage // internal test needs access to unexported types

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestEscalateSpawnsOneShotWithCorrectPrompt(t *testing.T) {
	proc := newReadyMockProcess("ACK: investigated stuck worker, restarted via oro directive restart-worker", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.Escalate(context.Background(), EscalationOpts{
		EscalationType: "STUCK_WORKER",
		BeadID:         "oro-esc1",
		BeadTitle:      "Implement feature X",
		BeadContext:    "Worker stuck for 15 minutes",
		RecentHistory:  "heartbeat timeout at 14:30",
		Workdir:        "/tmp/project",
	})

	result := waitResult(t, ch)
	if result.Type != OpsEscalation {
		t.Fatalf("expected OpsEscalation, got %q", result.Type)
	}
	if result.BeadID != "oro-esc1" {
		t.Fatalf("expected bead ID oro-esc1, got %q", result.BeadID)
	}

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(calls))
	}
	prompt := calls[0].prompt
	if !strings.Contains(prompt, "STUCK_WORKER") {
		t.Fatalf("prompt missing escalation type: %s", prompt)
	}
	if !strings.Contains(prompt, "oro-esc1") {
		t.Fatalf("prompt missing bead ID: %s", prompt)
	}
	if !strings.Contains(prompt, "Worker stuck for 15 minutes") {
		t.Fatalf("prompt missing bead context: %s", prompt)
	}
	if !strings.Contains(prompt, "heartbeat timeout at 14:30") {
		t.Fatalf("prompt missing recent history: %s", prompt)
	}
	if calls[0].workdir != "/tmp/project" {
		t.Fatalf("expected workdir /tmp/project, got %q", calls[0].workdir)
	}
}

func TestEscalateUsesCorrectModel(t *testing.T) {
	proc := newReadyMockProcess("ACK: done", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.Escalate(context.Background(), EscalationOpts{
		EscalationType: "MERGE_CONFLICT",
		BeadID:         "oro-m4",
		Workdir:        "/tmp/wt",
	})
	waitResult(t, ch)

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(calls))
	}
	if calls[0].model != OpsEscalation.Model() {
		t.Fatalf("expected model %q, got %q", OpsEscalation.Model(), calls[0].model)
	}
}

func TestEscalateCapturesFeedback(t *testing.T) {
	proc := newReadyMockProcess("ACK: restarted worker-3, bead oro-abc reassigned", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.Escalate(context.Background(), EscalationOpts{
		EscalationType: "STUCK_WORKER",
		BeadID:         "oro-fb",
		Workdir:        "/tmp/wt",
	})

	result := waitResult(t, ch)
	if result.Feedback == "" {
		t.Fatal("expected non-empty feedback from escalation one-shot")
	}
}

func TestEscalateSpawnError(t *testing.T) {
	mock := &mockBatchSpawner{
		process: nil,
		err:     errors.New("spawn failed"),
	}
	s := NewSpawner(mock)

	ch := s.Escalate(context.Background(), EscalationOpts{
		EscalationType: "MISSING_AC",
		BeadID:         "oro-err2",
		Workdir:        "/tmp/wt",
	})

	result := waitResult(t, ch)
	if result.Err == nil {
		t.Fatal("expected error from spawn failure")
	}
	if result.Verdict != VerdictFailed {
		t.Fatalf("expected VerdictFailed on spawn error, got %q", result.Verdict)
	}
}

func TestEscalateModelRouting(t *testing.T) {
	got := OpsEscalation.Model()
	if got != "sonnet" {
		t.Fatalf("OpsEscalation.Model() = %q, want claude-sonnet-4-5", got)
	}
}
