package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNoCmdStr is returned by TmuxPaneRestarter.Restart when cmdStr is empty.
var ErrNoCmdStr = errors.New("cmdStr is required")

// PaneRestarter restarts a named tmux pane.
type PaneRestarter interface {
	Restart(role string) error
}

// PaneLivenessChecker reports whether a pane is still running the runtime CLI.
type PaneLivenessChecker interface {
	Alive(ctx context.Context, role string) bool
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
//
//oro:testonly
func NewTmuxPaneRestarter(sessionName, cmdStr string, runner CommandRunner) *TmuxPaneRestarter {
	return &TmuxPaneRestarter{
		sessionName: sessionName,
		cmdStr:      cmdStr,
		runner:      runner,
	}
}

// CmdStr returns the command string for this TmuxPaneRestarter.
// This is used for testing to verify the correct cmdStr was configured.
//
//oro:testonly
func (r *TmuxPaneRestarter) CmdStr() string {
	return r.cmdStr
}

// SetPaneRestarter sets the PaneRestarter on a Dispatcher.
//
//oro:testonly
func (d *Dispatcher) SetPaneRestarter(r PaneRestarter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.paneRestarter = r
}

// GetPaneRestarter returns the currently set PaneRestarter, or nil if not set.
// This is used for testing to verify that SetPaneRestarter was called.
//
//oro:testonly
func (d *Dispatcher) GetPaneRestarter() PaneRestarter {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.paneRestarter
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

// Alive reports whether the tmux pane exists, is not dead, and has not fallen
// back to a shell after the runtime process exited.
func (r *TmuxPaneRestarter) Alive(ctx context.Context, role string) bool {
	target := r.sessionName + ":" + role
	out, err := r.runner.Run(ctx, "tmux", "display-message", "-p", "-t", target, "#{pane_dead} #{pane_current_command}")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "0" {
		return false
	}
	return !isShellCommand(fields[1])
}

func isShellCommand(cmd string) bool {
	switch strings.TrimSpace(cmd) {
	case "sh", "bash", "zsh", "fish", "dash", "ksh", "tcsh", "csh":
		return true
	default:
		return false
	}
}
