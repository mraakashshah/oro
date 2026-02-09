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
	calls []escCall
	err   error
}

type escCall struct {
	name string
	args []string
}

func (m *mockEscRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, escCall{name: name, args: args})
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

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.name != "tmux" {
		t.Fatalf("expected tmux, got %s", call.name)
	}
	// Should contain: send-keys -t oro:0.1 <message> Enter
	if len(call.args) < 4 {
		t.Fatalf("expected at least 4 args, got %v", call.args)
	}
	if call.args[0] != "send-keys" {
		t.Fatalf("expected send-keys, got %s", call.args[0])
	}
	if call.args[2] != "oro:0.1" {
		t.Fatalf("expected pane oro:0.1, got %s", call.args[2])
	}
}

func TestTmuxEscalator_Escalate_Error(t *testing.T) {
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

	call := runner.calls[0]
	// Default pane target is oro:0.1
	if call.args[2] != "oro:0.1" {
		t.Fatalf("expected default pane oro:0.1, got %s", call.args[2])
	}
}

func TestTmuxEscalator_SanitizesNewlines(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:0.1", runner)

	err := esc.Escalate(context.Background(), "line1\nline2\rline3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	call := runner.calls[0]
	msg := call.args[3] // the sanitized message
	if strings.Contains(msg, "\n") || strings.Contains(msg, "\r") {
		t.Fatalf("message should not contain newlines, got %q", msg)
	}
}

func TestTmuxEscalator_EnterSuffix(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:0.1", runner)

	_ = esc.Escalate(context.Background(), "test")

	call := runner.calls[0]
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

	call := runner.calls[0]
	if call.args[2] != "myapp:1.2" {
		t.Fatalf("expected custom pane myapp:1.2, got %s", call.args[2])
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
