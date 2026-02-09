package main

import (
	"context"
	"fmt"
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

// TmuxSession manages a tmux session with the Oro layout.
type TmuxSession struct {
	Name    string
	Runner  CmdRunner
	Sleeper func(time.Duration) // optional; overrides time.Sleep for testing
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

// beaconDelay is the time to wait after launching claude before injecting a beacon.
// This gives the interactive claude process time to start and be ready for input.
const beaconDelay = 2 * time.Second

// Create creates the Oro tmux session with two panes (architect + manager).
// Both panes launch interactive claude (with ORO_ROLE set), then inject
// the role-specific beacon text via send-keys after a short delay.
// If the session already exists, it is a no-op.
func (s *TmuxSession) Create(architectBeacon, managerBeacon string) error {
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

	// Launch interactive claude with ORO_ROLE=architect in pane 0.
	architectCmd := "export ORO_ROLE=architect && claude"
	if _, err := s.Runner.Run("tmux", "send-keys", "-t", s.Name+":0.0", architectCmd, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys architect launch: %w", err)
	}

	// Launch interactive claude with ORO_ROLE=manager in pane 1.
	managerCmd := "export ORO_ROLE=manager && claude"
	if _, err := s.Runner.Run("tmux", "send-keys", "-t", s.Name+":0.1", managerCmd, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys manager launch: %w", err)
	}

	// Wait for claude to start in both panes before injecting beacons.
	s.sleep(beaconDelay)

	// Inject architect beacon into pane 0.
	if _, err := s.Runner.Run("tmux", "send-keys", "-t", s.Name+":0.0", architectBeacon, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys architect beacon: %w", err)
	}

	// Inject manager beacon into pane 1.
	if _, err := s.Runner.Run("tmux", "send-keys", "-t", s.Name+":0.1", managerBeacon, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys manager beacon: %w", err)
	}

	return nil
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
