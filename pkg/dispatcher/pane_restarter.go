package dispatcher

import (
	"context"
	"errors"
	"fmt"
)

// ErrNoCmdStr is returned by TmuxPaneRestarter.Restart when cmdStr is empty.
var ErrNoCmdStr = errors.New("cmdStr is required")

// PaneRestarter restarts a named tmux pane.
type PaneRestarter interface {
	Restart(role string) error
}

// TmuxPaneRestarter implements PaneRestarter by respawning a tmux pane with a
// configured command. It uses `tmux respawn-pane -k` to kill any running
// process in the pane and start fresh with cmdStr.
type TmuxPaneRestarter struct {
	sessionName string
	cmdStr      string
	runner      CommandRunner
}

// NewTmuxPaneRestarter creates a TmuxPaneRestarter for the given session and command.
func NewTmuxPaneRestarter(sessionName, cmdStr string, runner CommandRunner) *TmuxPaneRestarter {
	return &TmuxPaneRestarter{
		sessionName: sessionName,
		cmdStr:      cmdStr,
		runner:      runner,
	}
}

// SetPaneRestarter sets the PaneRestarter on a Dispatcher.
// This is used by cmd/oro to wire up the production TmuxPaneRestarter
// after constructing the Dispatcher.
func (d *Dispatcher) SetPaneRestarter(r PaneRestarter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.paneRestarter = r
}

// Restart respawns the tmux pane identified by role (target: sessionName:role)
// with the configured command. Returns ErrNoCmdStr if cmdStr is empty.
func (r *TmuxPaneRestarter) Restart(role string) error {
	if r.cmdStr == "" {
		return ErrNoCmdStr
	}
	target := r.sessionName + ":" + role
	_, err := r.runner.Run(context.Background(), "tmux", "respawn-pane", "-k", "-t", target, r.cmdStr)
	if err != nil {
		return fmt.Errorf("tmux respawn-pane %s: %w", target, err)
	}
	return nil
}
