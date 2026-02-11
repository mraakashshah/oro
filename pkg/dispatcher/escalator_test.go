package dispatcher_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"oro/pkg/dispatcher"
)

// mockEscRunner captures commands for assertion without running real tmux.
type mockEscRunner struct {
	calls         []escCall
	err           error
	hasSessionErr error // separate error for has-session check
}

type escCall struct {
	name string
	args []string
}

func (m *mockEscRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, escCall{name: name, args: args})

	// If this is a has-session check, return hasSessionErr (defaults to nil)
	if name == "tmux" && len(args) > 0 && args[0] == "has-session" {
		return nil, m.hasSessionErr
	}

	// Otherwise return the general err
	return nil, m.err
}

// --- Tests ---

func TestTmuxEscalator_Escalate_BasicMessage(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:0.1", runner)

	err := esc.Escalate(context.Background(), "merge failed for bead abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 calls (has-session + send-keys), got %d", len(runner.calls))
	}

	// First call: has-session
	call0 := runner.calls[0]
	if call0.name != "tmux" {
		t.Fatalf("expected tmux, got %s", call0.name)
	}
	if call0.args[0] != "has-session" {
		t.Fatalf("expected has-session, got %s", call0.args[0])
	}

	// Second call: send-keys
	call1 := runner.calls[1]
	if call1.name != "tmux" {
		t.Fatalf("expected tmux, got %s", call1.name)
	}
	// Should contain: send-keys -t oro:0.1 <message> Enter
	if len(call1.args) < 4 {
		t.Fatalf("expected at least 4 args, got %v", call1.args)
	}
	if call1.args[0] != "send-keys" {
		t.Fatalf("expected send-keys, got %s", call1.args[0])
	}
	if call1.args[2] != "oro:0.1" {
		t.Fatalf("expected pane oro:0.1, got %s", call1.args[2])
	}
}

func TestTmuxEscalator_Escalate_Error(t *testing.T) {
	// Simulate send-keys error (session exists but send-keys fails)
	runner := &mockEscRunner{err: fmt.Errorf("tmux not running")}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:0.1", runner)

	err := esc.Escalate(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "tmux escalate") {
		t.Fatalf("error should mention tmux escalate, got: %v", err)
	}
}

func TestTmuxEscalator_DefaultSessionAndPane(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("", "", runner)

	err := esc.Escalate(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(runner.calls))
	}

	// First call: has-session with default session name
	call0 := runner.calls[0]
	if call0.args[0] != "has-session" {
		t.Fatalf("expected has-session, got %s", call0.args[0])
	}
	if call0.args[2] != "oro" {
		t.Fatalf("expected default session oro, got %s", call0.args[2])
	}

	// Second call: send-keys with default pane target
	call1 := runner.calls[1]
	if call1.args[2] != "oro:0.1" {
		t.Fatalf("expected default pane oro:0.1, got %s", call1.args[2])
	}
}

func TestTmuxEscalator_SanitizesNewlines(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:0.1", runner)

	err := esc.Escalate(context.Background(), "line1\nline2\rline3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(runner.calls))
	}

	// Check send-keys call (second call)
	call := runner.calls[1]
	msg := call.args[3] // the sanitized message
	if strings.Contains(msg, "\n") || strings.Contains(msg, "\r") {
		t.Fatalf("message should not contain newlines, got %q", msg)
	}
}

func TestTmuxEscalator_EnterSuffix(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:0.1", runner)

	_ = esc.Escalate(context.Background(), "test")

	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(runner.calls))
	}

	// Check send-keys call (second call)
	call := runner.calls[1]
	lastArg := call.args[len(call.args)-1]
	if lastArg != "Enter" {
		t.Fatalf("last arg should be Enter, got %s", lastArg)
	}
}

func TestTmuxEscalator_ImplementsInterface(t *testing.T) {
	runner := &mockEscRunner{}
	var _ dispatcher.Escalator = dispatcher.NewTmuxEscalator("", "", runner)
}

func TestTmuxEscalator_CustomPaneTarget(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("myapp", "myapp:1.2", runner)

	_ = esc.Escalate(context.Background(), "custom pane")

	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(runner.calls))
	}

	// Check has-session call (first call)
	call0 := runner.calls[0]
	if call0.args[2] != "myapp" {
		t.Fatalf("expected custom session myapp, got %s", call0.args[2])
	}

	// Check send-keys call (second call)
	call1 := runner.calls[1]
	if call1.args[2] != "myapp:1.2" {
		t.Fatalf("expected custom pane myapp:1.2, got %s", call1.args[2])
	}
}

// --- FormatEscalation tests ---

func TestFormatEscalation_WithDetails(t *testing.T) {
	got := dispatcher.FormatEscalation(dispatcher.EscMergeConflict, "bead-abc", "merge failed", "conflicting files in src/")
	want := "[ORO-DISPATCH] MERGE_CONFLICT: bead-abc \u2014 merge failed. conflicting files in src/."
	if got != want {
		t.Fatalf("FormatEscalation with details:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatEscalation_WithoutDetails(t *testing.T) {
	got := dispatcher.FormatEscalation(dispatcher.EscStuck, "bead-xyz", "review failed", "")
	want := "[ORO-DISPATCH] STUCK: bead-xyz \u2014 review failed."
	if got != want {
		t.Fatalf("FormatEscalation without details:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatEscalation_AllTypes(t *testing.T) {
	types := []struct {
		typ  dispatcher.EscalationType
		name string
	}{
		{dispatcher.EscMergeConflict, "MERGE_CONFLICT"},
		{dispatcher.EscStuck, "STUCK"},
		{dispatcher.EscPriorityContention, "PRIORITY_CONTENTION"},
		{dispatcher.EscWorkerCrash, "WORKER_CRASH"},
		{dispatcher.EscStatus, "STATUS"},
		{dispatcher.EscDrainComplete, "DRAIN_COMPLETE"},
	}

	for _, tc := range types {
		t.Run(tc.name, func(t *testing.T) {
			got := dispatcher.FormatEscalation(tc.typ, "bead-1", "summary", "")
			prefix := "[ORO-DISPATCH] " + tc.name + ": bead-1"
			if !strings.HasPrefix(got, prefix) {
				t.Fatalf("expected prefix %q, got %q", prefix, got)
			}
		})
	}
}

func TestFormatEscalation_EmptyBeadID(t *testing.T) {
	got := dispatcher.FormatEscalation(dispatcher.EscDrainComplete, "", "all workers drained", "")
	want := "[ORO-DISPATCH] DRAIN_COMPLETE:  \u2014 all workers drained."
	if got != want {
		t.Fatalf("FormatEscalation empty bead:\n got: %q\nwant: %q", got, want)
	}
}

func TestEscalation_DeadSession(t *testing.T) {
	// Simulate a dead tmux session: has-session returns error
	runner := &mockEscRunner{hasSessionErr: fmt.Errorf("session not found")}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:0.1", runner)

	err := esc.Escalate(context.Background(), "test escalation")
	if err == nil {
		t.Fatal("expected error when tmux session is dead")
	}

	if !strings.Contains(err.Error(), "session oro not found") {
		t.Fatalf("error should mention session not found, got: %v", err)
	}

	// Verify has-session was called but send-keys was NOT
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call (has-session only), got %d", len(runner.calls))
	}

	call := runner.calls[0]
	if call.name != "tmux" {
		t.Fatalf("expected tmux command, got %s", call.name)
	}
	if len(call.args) < 3 {
		t.Fatalf("expected at least 3 args, got %v", call.args)
	}
	if call.args[0] != "has-session" {
		t.Fatalf("expected has-session, got %s", call.args[0])
	}
	if call.args[1] != "-t" {
		t.Fatalf("expected -t flag, got %s", call.args[1])
	}
	if call.args[2] != "oro" {
		t.Fatalf("expected session name 'oro', got %s", call.args[2])
	}
}
