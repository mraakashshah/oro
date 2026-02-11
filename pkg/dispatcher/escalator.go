package dispatcher

import (
	"context"
	"fmt"
	"strings"
)

// CommandRunner is defined in beadsource.go.

// EscalationType and FormatEscalation are now in pkg/protocol/types.go

// TmuxEscalator implements the Escalator interface by sending messages to a
// tmux pane via `tmux send-keys`. This is the production mechanism for
// notifying the human Manager of events that require attention.
type TmuxEscalator struct {
	sessionName string
	paneTarget  string
	runner      CommandRunner
}

// NewTmuxEscalator creates a TmuxEscalator. If sessionName or paneTarget are
// empty, sensible defaults ("oro" and "oro:manager") are used.
func NewTmuxEscalator(sessionName, paneTarget string, runner CommandRunner) *TmuxEscalator {
	if sessionName == "" {
		sessionName = "oro"
	}
	if paneTarget == "" {
		paneTarget = "oro:manager"
	}
	return &TmuxEscalator{
		sessionName: sessionName,
		paneTarget:  paneTarget,
		runner:      runner,
	}
}

// Escalate sends msg to the Manager's tmux pane via `tmux set-buffer` and
// `paste-buffer`. This approach treats the message as completely literal text,
// preventing shell injection through tmux.
// Before sending, it verifies the tmux session exists to prevent silent failures.
func (e *TmuxEscalator) Escalate(ctx context.Context, msg string) error {
	// Verify the tmux session exists before attempting to send.
	// If the session is dead, tmux send-keys fails silently, leaving
	// escalations undelivered and beads stuck forever.
	_, err := e.runner.Run(ctx, "tmux", "has-session", "-t", e.sessionName)
	if err != nil {
		return fmt.Errorf("tmux session %s not found: %w", e.sessionName, err)
	}

	sanitized := sanitizeForTmux(msg)

	// Step 1: Set the message into a named tmux buffer
	_, err = e.runner.Run(ctx, "tmux", "set-buffer", "-b", "oro-escalate", sanitized)
	if err != nil {
		return fmt.Errorf("tmux set-buffer: %w", err)
	}

	// Step 2: Paste the buffer content to the target pane (literal text)
	_, err = e.runner.Run(ctx, "tmux", "paste-buffer", "-b", "oro-escalate", "-t", e.paneTarget, "-d")
	if err != nil {
		return fmt.Errorf("tmux paste-buffer to %s: %w", e.paneTarget, err)
	}

	// Step 3: Send Enter key to display the message
	_, err = e.runner.Run(ctx, "tmux", "send-keys", "-t", e.paneTarget, "Enter")
	if err != nil {
		return fmt.Errorf("tmux send-keys Enter to %s: %w", e.paneTarget, err)
	}

	return nil
}

// sanitizeForTmux prepares a message string for safe use with tmux load-buffer.
// We strip newlines to prevent the message from spanning multiple lines in the
// manager's terminal, which could be confusing.
func sanitizeForTmux(msg string) string {
	// Replace newlines with spaces for single-line display
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	return msg
}
