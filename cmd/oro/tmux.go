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

// Create creates the Oro tmux session with two panes (architect + manager).
// Both panes launch interactive claude with role env vars (ORO_ROLE, BD_ACTOR,
// GIT_AUTHOR_NAME) set, then poll for Claude readiness before injecting a short
// nudge via send-keys. The full role context is injected by the SessionStart hook
// reading the ORO_ROLE env var — send-keys only sends a short nudge/kick.
// If the session already exists, it is a no-op.
func (s *TmuxSession) Create(architectNudge, managerNudge string) error {
	if s.Exists() {
		return nil
	}

	// Create a detached session — left pane (pane 0) is the architect.
	if _, err := s.Runner.Run("tmux", "new-session", "-d", "-s", s.Name); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}

	// Split horizontally to create right pane (pane 1) — the manager.
	if _, err := s.Runner.Run("tmux", "split-window", "-h", "-t", s.Name); err != nil {
		return fmt.Errorf("tmux split-window: %w", err)
	}

	// Launch interactive claude with role env vars in pane 0 (architect).
	architectCmd := roleEnvCmd("architect")
	if _, err := s.Runner.Run("tmux", "send-keys", "-t", s.Name+":0.0", architectCmd, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys architect launch: %w", err)
	}

	// Launch interactive claude with role env vars in pane 1 (manager).
	managerCmd := roleEnvCmd("manager")
	if _, err := s.Runner.Run("tmux", "send-keys", "-t", s.Name+":0.1", managerCmd, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys manager launch: %w", err)
	}

	// Poll both panes until Claude is ready (prompt appears).
	architectPane := s.Name + ":0.0"
	if err := s.PaneReady(architectPane); err != nil {
		return fmt.Errorf("wait for architect pane ready: %w", err)
	}
	managerPane := s.Name + ":0.1"
	if err := s.PaneReady(managerPane); err != nil {
		return fmt.Errorf("wait for manager pane ready: %w", err)
	}

	// Inject short architect nudge into pane 0.
	if _, err := s.Runner.Run("tmux", "send-keys", "-t", s.Name+":0.0", architectNudge, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys architect nudge: %w", err)
	}

	// Inject short manager nudge into pane 1.
	if _, err := s.Runner.Run("tmux", "send-keys", "-t", s.Name+":0.1", managerNudge, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys manager nudge: %w", err)
	}

	// Verify manager received nudge (look for bd stats execution).
	beaconTimeout := s.BeaconTimeout
	if beaconTimeout == 0 {
		beaconTimeout = defaultBeaconTimeout
	}
	if err := s.VerifyBeaconReceived(s.Name+":0.1", "bd stats", beaconTimeout); err != nil {
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
