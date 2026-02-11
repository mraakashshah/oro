package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CmdRunner abstracts command execution for testability.
type CmdRunner interface {
	Run(name string, args ...string) (string, error)
}

// ExecRunner implements CmdRunner using os/exec.
type ExecRunner struct{}

// Run executes a command and returns its combined output.
func (e *ExecRunner) Run(name string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// defaultReadyTimeout is the default time to wait for Claude to become ready.
const defaultReadyTimeout = 30 * time.Second

// pollInterval is the time between capture-pane readiness checks.
const pollInterval = 500 * time.Millisecond

// defaultBeaconTimeout is the default time to wait for beacon verification.
const defaultBeaconTimeout = 60 * time.Second

// TmuxSession manages a tmux session with the Oro layout.
type TmuxSession struct {
	Name          string
	Runner        CmdRunner
	Sleeper       func(time.Duration) // optional; overrides time.Sleep for testing
	ReadyTimeout  time.Duration       // timeout for Claude readiness polling; 0 means defaultReadyTimeout
	BeaconTimeout time.Duration       // timeout for beacon verification polling; 0 means defaultBeaconTimeout
}

// NewTmuxSession creates a TmuxSession with the default ExecRunner.
func NewTmuxSession(name string) *TmuxSession {
	return &TmuxSession{Name: name, Runner: &ExecRunner{}}
}

// Exists checks whether the named tmux session is running.
func (s *TmuxSession) Exists() bool {
	_, err := s.Runner.Run("tmux", "has-session", "-t", s.Name)
	return err == nil
}

// roleEnvCmd builds a shell command that exports ORO_ROLE, BD_ACTOR, and
// GIT_AUTHOR_NAME for the given role, then launches interactive claude.
func roleEnvCmd(role string) string {
	return fmt.Sprintf("export ORO_ROLE=%s BD_ACTOR=%s GIT_AUTHOR_NAME=%s && claude", role, role, role)
}

// Create creates the Oro tmux session with two windows (architect + manager).
// Both windows launch interactive claude with role env vars (ORO_ROLE, BD_ACTOR,
// GIT_AUTHOR_NAME) set, then poll for Claude readiness before injecting a short
// nudge via send-keys. The full role context is injected by the SessionStart hook
// reading the ORO_ROLE env var — send-keys only sends a short nudge/kick.
// If the session already exists, it is a no-op.
func (s *TmuxSession) Create(architectNudge, managerNudge string) error {
	if s.Exists() {
		return nil
	}

	// Create a detached session with first window named "architect".
	if _, err := s.Runner.Run("tmux", "new-session", "-d", "-s", s.Name, "-n", "architect"); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}

	// Create second window named "manager".
	if _, err := s.Runner.Run("tmux", "new-window", "-t", s.Name, "-n", "manager"); err != nil {
		return fmt.Errorf("tmux new-window: %w", err)
	}

	// Apply color theming to architect window (bright green).
	if _, err := s.Runner.Run("tmux", "set-option", "-t", s.Name+":architect", "window-style", "fg=colour46,bold"); err != nil {
		return fmt.Errorf("tmux set-option architect color: %w", err)
	}

	// Apply color theming to manager window (orange).
	if _, err := s.Runner.Run("tmux", "set-option", "-t", s.Name+":manager", "window-style", "fg=colour208,bold"); err != nil {
		return fmt.Errorf("tmux set-option manager color: %w", err)
	}

	// Launch interactive claude with role env vars in architect window.
	architectCmd := roleEnvCmd("architect")
	if _, err := s.Runner.Run("tmux", "send-keys", "-t", s.Name+":architect", architectCmd, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys architect launch: %w", err)
	}

	// Launch interactive claude with role env vars in manager window.
	managerCmd := roleEnvCmd("manager")
	if _, err := s.Runner.Run("tmux", "send-keys", "-t", s.Name+":manager", managerCmd, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys manager launch: %w", err)
	}

	// Poll both windows until Claude is ready (prompt appears).
	architectPane := s.Name + ":architect"
	if err := s.PaneReady(architectPane); err != nil {
		return fmt.Errorf("wait for architect window ready: %w", err)
	}
	managerPane := s.Name + ":manager"
	if err := s.PaneReady(managerPane); err != nil {
		return fmt.Errorf("wait for manager window ready: %w", err)
	}

	// Inject short architect nudge into architect window.
	if _, err := s.Runner.Run("tmux", "send-keys", "-t", s.Name+":architect", architectNudge, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys architect nudge: %w", err)
	}

	// Inject short manager nudge into manager window.
	if _, err := s.Runner.Run("tmux", "send-keys", "-t", s.Name+":manager", managerNudge, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys manager nudge: %w", err)
	}

	// Verify manager received nudge (look for bd stats execution).
	beaconTimeout := s.BeaconTimeout
	if beaconTimeout == 0 {
		beaconTimeout = defaultBeaconTimeout
	}
	if err := s.VerifyBeaconReceived(s.Name+":manager", "bd stats", beaconTimeout); err != nil {
		// Warning only — don't fail startup.
		fmt.Fprintf(os.Stderr, "warning: manager nudge may not have been received: %v\n", err)
	}

	return nil
}

// PaneReady polls the pane content via tmux capture-pane until Claude's input
// prompt (a line starting with ">") appears. It polls every 500ms and times out
// after ReadyTimeout (default 30s).
func (s *TmuxSession) PaneReady(paneTarget string) error {
	timeout := s.ReadyTimeout
	if timeout == 0 {
		timeout = defaultReadyTimeout
	}
	deadline := time.Now().Add(timeout)

	for {
		out, err := s.Runner.Run("tmux", "capture-pane", "-p", "-t", paneTarget)
		if err == nil && hasPrompt(out) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("claude did not become ready in pane %s within %v", paneTarget, timeout)
		}
		s.sleep(pollInterval)
	}
}

// VerifyBeaconReceived polls the pane content via tmux capture-pane until the
// given indicator string appears, confirming the Claude session received and
// started processing the beacon. It polls every 1s and returns an error with
// diagnostic pane content on timeout.
func (s *TmuxSession) VerifyBeaconReceived(paneTarget, indicator string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastOutput string

	for {
		out, err := s.Runner.Run("tmux", "capture-pane", "-p", "-t", paneTarget)
		if err == nil {
			lastOutput = out
			if strings.Contains(out, indicator) {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("beacon indicator %q not found in pane %s within %v; last pane content:\n%s",
				indicator, paneTarget, timeout, lastOutput)
		}
		s.sleep(1 * time.Second)
	}
}

// hasPrompt checks whether captured pane output contains Claude's input prompt.
// It looks for a line that starts with ">" (optionally preceded by whitespace).
func hasPrompt(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, ">") {
			return true
		}
	}
	return false
}

// sleep pauses for the given duration. It uses the Sleeper if set (for testing),
// otherwise falls back to time.Sleep.
func (s *TmuxSession) sleep(d time.Duration) {
	if s.Sleeper != nil {
		s.Sleeper(d)
		return
	}
	time.Sleep(d)
}

// Kill destroys the named tmux session.
func (s *TmuxSession) Kill() error {
	_, err := s.Runner.Run("tmux", "kill-session", "-t", s.Name)
	if err != nil {
		return fmt.Errorf("tmux kill-session: %w", err)
	}
	return nil
}

// ListPanes returns the pane indices for the named session.
func (s *TmuxSession) ListPanes() ([]string, error) {
	out, err := s.Runner.Run("tmux", "list-panes", "-t", s.Name, "-F", "#{pane_index}")
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes: %w", err)
	}
	var panes []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			panes = append(panes, line)
		}
	}
	return panes, nil
}

// Attach attaches to the named tmux session (replaces current terminal).
func (s *TmuxSession) Attach() error {
	_, err := s.Runner.Run("tmux", "attach-session", "-t", s.Name)
	if err != nil {
		return fmt.Errorf("tmux attach-session: %w", err)
	}
	return nil
}

// AttachInteractive attaches to the named tmux session with real terminal I/O.
// This bypasses the CmdRunner interface to connect stdin/stdout/stderr directly,
// allowing interactive use. It blocks until the session is detached or exits.
func (s *TmuxSession) AttachInteractive() error {
	cmd := exec.CommandContext(context.Background(), "tmux", "attach-session", "-t", s.Name) //nolint:gosec // s.Name is controlled by codebase, not user input
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux attach-session: %w", err)
	}
	return nil
}
