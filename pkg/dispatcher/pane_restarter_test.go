package dispatcher_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"oro/pkg/dispatcher"
)

// mockPaneRestartRunner captures runner calls for assertion without running real tmux.
type mockPaneRestartRunner struct {
	calls []paneRestartCall
	err   error
}

type paneRestartCall struct {
	name string
	args []string
}

func (m *mockPaneRestartRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, paneRestartCall{name: name, args: args})
	return nil, m.err
}

func TestTmuxPaneRestarter_Restart(t *testing.T) {
	t.Run("sends respawn-pane with correct target and cmdStr", func(t *testing.T) {
		runner := &mockPaneRestartRunner{}
		r := dispatcher.NewTmuxPaneRestarter("oro", "oro worker", runner)

		if err := r.Restart("manager"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(runner.calls) != 1 {
			t.Fatalf("expected 1 runner call, got %d", len(runner.calls))
		}

		call := runner.calls[0]
		if call.name != "tmux" {
			t.Errorf("expected command 'tmux', got %q", call.name)
		}
		if len(call.args) == 0 || call.args[0] != "respawn-pane" {
			t.Errorf("expected 'respawn-pane' as first arg, got %v", call.args)
		}

		// Verify target is sessionName:role
		targetFound := false
		for i, arg := range call.args {
			if arg == "-t" && i+1 < len(call.args) && call.args[i+1] == "oro:manager" {
				targetFound = true
				break
			}
		}
		if !targetFound {
			t.Errorf("expected -t oro:manager in args, got %v", call.args)
		}

		// Verify cmdStr is included
		cmdFound := false
		for _, arg := range call.args {
			if arg == "oro worker" {
				cmdFound = true
				break
			}
		}
		if !cmdFound {
			t.Errorf("expected cmdStr 'oro worker' in args, got %v", call.args)
		}
	})

	t.Run("kill flag is set", func(t *testing.T) {
		runner := &mockPaneRestartRunner{}
		r := dispatcher.NewTmuxPaneRestarter("oro", "oro worker", runner)

		_ = r.Restart("manager")

		if len(runner.calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(runner.calls))
		}
		args := runner.calls[0].args
		killFound := false
		for _, arg := range args {
			if arg == "-k" {
				killFound = true
				break
			}
		}
		if !killFound {
			t.Errorf("expected -k (kill) flag in respawn-pane args, got %v", args)
		}
	})

	t.Run("empty cmdStr returns ErrNoCmdStr without calling runner", func(t *testing.T) {
		runner := &mockPaneRestartRunner{}
		r := dispatcher.NewTmuxPaneRestarter("oro", "", runner)

		err := r.Restart("manager")

		if !errors.Is(err, dispatcher.ErrNoCmdStr) {
			t.Fatalf("expected ErrNoCmdStr, got: %v", err)
		}
		if len(runner.calls) != 0 {
			t.Errorf("expected no runner calls when cmdStr is empty, got %d", len(runner.calls))
		}
	})

	t.Run("runner error is wrapped and returned", func(t *testing.T) {
		runner := &mockPaneRestartRunner{err: fmt.Errorf("tmux not found")}
		r := dispatcher.NewTmuxPaneRestarter("oro", "oro worker", runner)

		err := r.Restart("manager")

		if err == nil {
			t.Fatal("expected error when runner fails, got nil")
		}
		if !strings.Contains(err.Error(), "tmux not found") {
			t.Errorf("expected wrapped error to contain 'tmux not found', got: %v", err)
		}
	})

	t.Run("implements PaneRestarter interface", func(t *testing.T) {
		runner := &mockPaneRestartRunner{}
		var _ dispatcher.PaneRestarter = dispatcher.NewTmuxPaneRestarter("oro", "oro worker", runner)
	})
}
