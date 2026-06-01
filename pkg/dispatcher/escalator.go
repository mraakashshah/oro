package dispatcher

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const tmuxSetBufferArgLimit = 64 * 1024

// EscalationType and FormatEscalation are now in pkg/protocol/types.go.

// NoopEscalator records no side effects while satisfying the Escalator
// interface for managerless dispatcher starts.
type NoopEscalator struct{}

func (NoopEscalator) Escalate(context.Context, string) error {
	return nil
}

// TmuxEscalator implements the Escalator interface by sending messages to a
// tmux pane via `tmux send-keys`. This is the production mechanism for
// notifying the human Manager of events that require attention.
type TmuxEscalator struct {
	mu          sync.Mutex
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
	// Serialize escalations to prevent interleaved set-buffer/paste-buffer
	// calls from corrupting each other's messages (shared buffer name).
	e.mu.Lock()
	defer e.mu.Unlock()

	// Verify the tmux session exists before attempting to send.
	// If the session is dead, tmux send-keys fails silently, leaving
	// escalations undelivered and beads stuck forever.
	_, err := e.runner.Run(ctx, "tmux", "has-session", "-t", e.sessionName)
	if err != nil {
		// Session/window not found — skip tmux escalation gracefully.
		// The ops-only path will handle escalation delivery.
		fmt.Fprintf(os.Stderr, "[oro] warning: tmux session %s not found, skipping tmux escalation: %v\n", e.sessionName, err)
		return nil
	}
	if _, err := e.runner.Run(ctx, "tmux", "display-message", "-p", "-t", e.paneTarget, "#{pane_id}"); err != nil {
		// Managerless starts can leave a tmux attach surface without a manager
		// pane. Treat that the same as a missing session so ops routing can
		// continue without marking the task escalation as failed.
		fmt.Fprintf(os.Stderr, "[oro] warning: tmux pane %s not found, skipping tmux escalation: %v\n", e.paneTarget, err)
		return nil
	}

	// Step 0.5: Clear any pending input from a previous partial delivery.
	// Without this, paste-buffer appends to leftover text, garbling the message.
	_, err = e.runner.Run(ctx, "tmux", "send-keys", "-t", e.paneTarget, "C-u")
	if err != nil {
		return fmt.Errorf("tmux send-keys C-u to %s: %w", e.paneTarget, err)
	}

	sanitized := sanitizeForTmux(msg)

	// Step 1: Set the message into a named tmux buffer
	if err := e.loadEscalationBuffer(ctx, sanitized); err != nil {
		return err
	}

	// Step 2: Paste the buffer content to the target pane (literal text)
	_, err = e.runner.Run(ctx, "tmux", "paste-buffer", "-b", "oro-escalate", "-t", e.paneTarget, "-d")
	if err != nil {
		return fmt.Errorf("tmux paste-buffer to %s: %w", e.paneTarget, err)
	}

	// Wake Ink so it processes the pasted text before Escape arrives.
	e.wakeIfDetached(ctx)
	time.Sleep(100 * time.Millisecond)

	// Step 2.5: Send Escape to exit any vim-mode INSERT state before Enter.
	_, err = e.runner.Run(ctx, "tmux", "send-keys", "-t", e.paneTarget, "Escape")
	if err != nil {
		return fmt.Errorf("tmux send-keys Escape to %s: %w", e.paneTarget, err)
	}

	// Wake so Ink processes Escape before Enter arrives.
	e.wakeIfDetached(ctx)
	time.Sleep(100 * time.Millisecond)

	// Step 3: Send Enter key to submit the message
	_, err = e.runner.Run(ctx, "tmux", "send-keys", "-t", e.paneTarget, "Enter")
	if err != nil {
		return fmt.Errorf("tmux send-keys Enter to %s: %w", e.paneTarget, err)
	}

	// Wake so Ink processes Enter in detached sessions.
	e.wakeIfDetached(ctx)

	return nil
}

func (e *TmuxEscalator) loadEscalationBuffer(ctx context.Context, sanitized string) error {
	if len(sanitized) > tmuxSetBufferArgLimit {
		return e.loadEscalationBufferFromStdin(ctx, sanitized)
	}
	_, err := e.runner.Run(ctx, "tmux", "set-buffer", "-b", "oro-escalate", sanitized)
	if err != nil {
		return fmt.Errorf("tmux set-buffer: %w", err)
	}
	return nil
}

func (e *TmuxEscalator) loadEscalationBufferFromStdin(ctx context.Context, sanitized string) error {
	inputRunner, ok := e.runner.(InputCommandRunner)
	if !ok {
		return fmt.Errorf("tmux load-buffer: runner does not support stdin for %d-byte escalation", len(sanitized))
	}
	_, err := inputRunner.RunWithInput(ctx, sanitized, "tmux", "load-buffer", "-b", "oro-escalate", "-")
	if err != nil {
		return fmt.Errorf("tmux load-buffer: %w", err)
	}
	return nil
}

// wakeIfDetached sends SIGWINCH to the pane's process when no clients are
// attached. This wakes Claude Code's Ink render loop in detached sessions.
// Uses direct kill -WINCH via the pane PID rather than resize, which is
// more reliable at delivering the signal to Node.js/Ink.
func (e *TmuxEscalator) wakeIfDetached(ctx context.Context) {
	out, err := e.runner.Run(ctx, "tmux", "display-message", "-p", "-t", e.paneTarget, "#{session_attached}")
	if err == nil && strings.TrimSpace(string(out)) != "0" {
		return
	}
	pidOut, err := e.runner.Run(ctx, "tmux", "display-message", "-p", "-t", e.paneTarget, "#{pane_pid}")
	if err != nil {
		return
	}
	_, _ = e.runner.Run(ctx, "kill", "-WINCH", strings.TrimSpace(string(pidOut)))
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
