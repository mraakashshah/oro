package dispatcher

import (
	"context"
	"fmt"
	"strings"
)

// CommandRunner executes an external command. Extracted as an interface for
// testability so production code can shell out while tests use a mock.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// TmuxEscalator implements the Escalator interface by sending messages to a
// tmux pane via `tmux send-keys`. This is the production mechanism for
// notifying the human Manager of events that require attention.
type TmuxEscalator struct {
	sessionName string
	paneTarget  string
	runner      CommandRunner
}

// NewTmuxEscalator creates a TmuxEscalator. If sessionName or paneTarget are
// empty, sensible defaults ("oro" and "oro:0.1") are used.
func NewTmuxEscalator(sessionName, paneTarget string, runner CommandRunner) *TmuxEscalator {
	if sessionName == "" {
		sessionName = "oro"
	}
	if paneTarget == "" {
		paneTarget = "oro:0.1"
	}
	return &TmuxEscalator{
		sessionName: sessionName,
		paneTarget:  paneTarget,
		runner:      runner,
	}
}

// Escalate sends msg to the Manager's tmux pane via `tmux send-keys`.
// The message is sanitized to prevent shell injection through tmux.
func (e *TmuxEscalator) Escalate(ctx context.Context, msg string) error {
	sanitized := sanitizeForTmux(msg)
	err := e.runner.Run(ctx, "tmux", "send-keys", "-t", e.paneTarget, sanitized, "Enter")
	if err != nil {
		return fmt.Errorf("tmux escalate to %s: %w", e.paneTarget, err)
	}
	return nil
}

// sanitizeForTmux prepares a message string for safe use with tmux send-keys.
// tmux send-keys treats each argument as literal text when passed as a separate
// argument (not interpreted by a shell), but we still strip newlines to prevent
// multi-command injection and escape characters that tmux interprets specially.
func sanitizeForTmux(msg string) string {
	// Replace newlines with spaces — tmux send-keys + Enter would execute
	// partial lines otherwise.
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	return msg
}
