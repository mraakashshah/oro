package dispatcher_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"oro/pkg/dispatcher"
)

// mockCommandRunner captures commands for assertion without running real tmux.
type mockCommandRunner struct {
	calls []cmdCall
	err   error // if set, Run returns this error
}

type cmdCall struct {
	name string
	args []string
}

func (m *mockCommandRunner) Run(_ context.Context, name string, args ...string) error {
	m.calls = append(m.calls, cmdCall{name: name, args: args})
	return m.err
}

// --- Tests ---

func TestTmuxEscalator_Escalate_BasicMessage(t *testing.T) {
	runner := &mockCommandRunner{}
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
		t.Fatalf("expected command 'tmux', got %q", call.name)
	}

	// Verify args: send-keys -t <paneTarget> <msg> Enter
	if len(call.args) < 4 {
		t.Fatalf("expected at least 4 args, got %d: %v", len(call.args), call.args)
	}
	if call.args[0] != "send-keys" {
		t.Fatalf("expected 'send-keys', got %q", call.args[0])
	}
	if call.args[1] != "-t" {
		t.Fatalf("expected '-t', got %q", call.args[1])
	}
	if call.args[2] != "oro:0.1" {
		t.Fatalf("expected pane target 'oro:0.1', got %q", call.args[2])
	}
	// The message is the 4th arg, "Enter" is the 5th
	if call.args[len(call.args)-1] != "Enter" {
		t.Fatalf("expected last arg 'Enter', got %q", call.args[len(call.args)-1])
	}
	// The message should be present somewhere in the args
	if !strings.Contains(strings.Join(call.args, " "), "merge failed for bead abc123") {
		t.Fatalf("expected message in args, got %v", call.args)
	}
}

func TestTmuxEscalator_Escalate_SpecialCharacters(t *testing.T) {
	tests := []struct {
		name string
		msg  string
	}{
		{
			name: "double_quotes",
			msg:  `merge failed: "unexpected EOF"`,
		},
		{
			name: "single_quotes",
			msg:  "it's broken",
		},
		{
			name: "semicolons",
			msg:  "error; please check logs",
		},
		{
			name: "backticks",
			msg:  "run `bd ready` to check",
		},
		{
			name: "dollar_sign",
			msg:  "env $HOME not set",
		},
		{
			name: "newlines",
			msg:  "line1\nline2",
		},
		{
			name: "backslash",
			msg:  `path\to\file`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockCommandRunner{}
			esc := dispatcher.NewTmuxEscalator("oro", "oro:0.1", runner)

			err := esc.Escalate(context.Background(), tt.msg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(runner.calls) != 1 {
				t.Fatalf("expected 1 call, got %d", len(runner.calls))
			}

			call := runner.calls[0]
			if call.name != "tmux" {
				t.Fatalf("expected 'tmux', got %q", call.name)
			}
			// Verify the command structure is correct
			if call.args[0] != "send-keys" {
				t.Fatalf("expected 'send-keys', got %q", call.args[0])
			}
			if call.args[len(call.args)-1] != "Enter" {
				t.Fatalf("expected last arg 'Enter', got %q", call.args[len(call.args)-1])
			}
		})
	}
}

func TestTmuxEscalator_Escalate_RunnerError(t *testing.T) {
	runner := &mockCommandRunner{err: fmt.Errorf("tmux not found")}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:0.1", runner)

	err := esc.Escalate(context.Background(), "test message")
	if err == nil {
		t.Fatal("expected error when runner fails")
	}
	if !strings.Contains(err.Error(), "tmux not found") {
		t.Fatalf("expected error to contain 'tmux not found', got %q", err.Error())
	}
}

func TestTmuxEscalator_Escalate_DefaultValues(t *testing.T) {
	runner := &mockCommandRunner{}
	// Use default constructor
	esc := dispatcher.NewTmuxEscalator("", "", runner)

	err := esc.Escalate(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}

	// Defaults: sessionName "oro", paneTarget "oro:0.1"
	call := runner.calls[0]
	if call.args[2] != "oro:0.1" {
		t.Fatalf("expected default pane target 'oro:0.1', got %q", call.args[2])
	}
}

func TestTmuxEscalator_Escalate_CustomPaneTarget(t *testing.T) {
	runner := &mockCommandRunner{}
	esc := dispatcher.NewTmuxEscalator("my-session", "my-session:1.2", runner)

	err := esc.Escalate(context.Background(), "custom target test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	call := runner.calls[0]
	if call.args[2] != "my-session:1.2" {
		t.Fatalf("expected pane target 'my-session:1.2', got %q", call.args[2])
	}
}

func TestTmuxEscalator_Escalate_ContextCancelled(t *testing.T) {
	runner := &mockCommandRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:0.1", runner)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// The escalator should still attempt to run (context is passed to runner)
	// but this tests that it propagates context correctly
	_ = esc.Escalate(ctx, "cancelled context")

	// Runner should still have been called (it's the runner's job to respect ctx)
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call even with cancelled context, got %d", len(runner.calls))
	}
}

func TestTmuxEscalator_ImplementsEscalator(t *testing.T) {
	runner := &mockCommandRunner{}
	var _ dispatcher.Escalator = dispatcher.NewTmuxEscalator("oro", "oro:0.1", runner)
}

func TestTmuxEscalator_Escalate_EmptyMessage(t *testing.T) {
	runner := &mockCommandRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:0.1", runner)

	err := esc.Escalate(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}

	call := runner.calls[0]
	if call.args[len(call.args)-1] != "Enter" {
		t.Fatalf("expected last arg 'Enter', got %q", call.args[len(call.args)-1])
	}
}
