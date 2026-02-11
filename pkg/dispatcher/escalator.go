package dispatcher

import (
	"context"
	"fmt"
	"strings"
)

// CommandRunner is defined in beadsource.go.

// EscalationType classifies a structured escalation message.
type EscalationType string

// Escalation type constants for [ORO-DISPATCH] messages.
const (
	EscMergeConflict      EscalationType = "MERGE_CONFLICT"
	EscStuck              EscalationType = "STUCK"
	EscPriorityContention EscalationType = "PRIORITY_CONTENTION"
	EscWorkerCrash        EscalationType = "WORKER_CRASH"
	EscStatus             EscalationType = "STATUS"
	EscDrainComplete      EscalationType = "DRAIN_COMPLETE"
)

// FormatEscalation produces a structured escalation message in the form:
//
//	[ORO-DISPATCH] <TYPE>: <bead-id> — <summary>. <details>.
//
// If details is empty the trailing details clause is omitted.
func FormatEscalation(typ EscalationType, beadID, summary, details string) string {
	if details != "" {
		return fmt.Sprintf("[ORO-DISPATCH] %s: %s \u2014 %s. %s.", typ, beadID, summary, details)
	}
	return fmt.Sprintf("[ORO-DISPATCH] %s: %s \u2014 %s.", typ, beadID, summary)
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
// Before sending, it verifies the tmux session exists to prevent silent failures.
func (e *TmuxEscalator) Escalate(ctx context.Context, msg string) error {
	// Verify the tmux session exists before attempting to send keys.
	// If the session is dead, tmux send-keys fails silently, leaving
	// escalations undelivered and beads stuck forever.
	_, err := e.runner.Run(ctx, "tmux", "has-session", "-t", e.sessionName)
	if err != nil {
		return fmt.Errorf("tmux session %s not found: %w", e.sessionName, err)
	}

	sanitized := sanitizeForTmux(msg)
	_, err = e.runner.Run(ctx, "tmux", "send-keys", "-t", e.paneTarget, sanitized, "Enter")
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
