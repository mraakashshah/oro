package dispatcher

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// paneState tracks per-pane restart history to enforce cooldown and prevent
// concurrent restarts.
type paneState struct {
	lastRestartAt time.Time
	restartCount  int
	restarting    bool // guard against concurrent restart attempts
}

// paneMonitorLoop polls ~/.oro/panes/{architect,manager}/context_pct at the
// configured interval (default 5s). When a pane's context percentage exceeds
// the configured threshold, it writes a handoff_requested file to signal the
// pane to initiate a handoff. Panes are tracked in signaledPanes to prevent
// re-signaling on subsequent polls.
func (d *Dispatcher) paneMonitorLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.PaneMonitorInterval)
	defer ticker.Stop()

	roles := []string{"architect", "manager"}

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.shutdownCh:
			return
		case <-ticker.C:
			d.checkPaneContexts(ctx, roles)
			if d.testPanePollDone != nil {
				d.testPanePollDone()
			}
		}
	}
}

// checkPaneContexts reads context_pct for each role and signals handoff if
// threshold is exceeded and pane hasn't been signaled before.
func (d *Dispatcher) checkPaneContexts(ctx context.Context, roles []string) {
	for _, role := range roles {
		d.checkPaneContext(ctx, role)
	}
}

// checkPaneContext reads the context_pct file for a single role, parses the
// percentage, and either restarts (manager with PaneRestarter wired) or signals
// handoff (architect, or manager without PaneRestarter).
func (d *Dispatcher) checkPaneContext(ctx context.Context, role string) {
	// Manager with a PaneRestarter: use restart logic instead of signalHandoff.
	d.mu.Lock()
	restarter := d.paneRestarter
	d.mu.Unlock()

	if role == "manager" && restarter != nil {
		d.checkManagerPane(ctx)
		return
	}

	roleDir := filepath.Join(d.panesDir, role)
	pctFile := filepath.Join(roleDir, "context_pct")

	// Check if already signaled (early return to avoid file I/O)
	d.mu.Lock()
	alreadySignaled := d.signaledPanes[role]
	d.mu.Unlock()

	if alreadySignaled {
		return
	}

	// Read context_pct file
	//nolint:gosec // pctFile is derived from trusted panesDir
	data, err := os.ReadFile(pctFile)
	if err != nil {
		// File missing is normal (pane may not exist), skip silently
		return
	}

	// Parse percentage
	pctStr := strings.TrimSpace(string(data))
	pct, err := strconv.Atoi(pctStr)
	if err != nil {
		// Parse error, skip this role
		_ = d.logEvent(ctx, "pane_context_parse_error", "dispatcher", "", "",
			"role="+role+" error="+err.Error())
		return
	}

	// Check threshold
	threshold := d.cfg.PaneContextThreshold
	if pct >= threshold {
		// Signal handoff
		d.signalHandoff(ctx, role, roleDir, pct)
	}
}

// checkManagerPane applies restart logic for the manager pane: restarts when
// context_pct exceeds the threshold or the pane has been inactive longer than
// PaneInactivityTimeout. Cooldown and the restarting flag prevent concurrent or
// rapid-fire restarts.
func (d *Dispatcher) checkManagerPane(ctx context.Context) {
	const role = "manager"
	pctFile := filepath.Join(d.panesDir, role, "context_pct")

	// Guard: cooldown and restarting flag.
	d.mu.Lock()
	state := d.paneStates[role]
	if state == nil {
		state = &paneState{}
		d.paneStates[role] = state
	}
	if state.restarting {
		d.mu.Unlock()
		return
	}
	if !state.lastRestartAt.IsZero() && d.nowFunc().Sub(state.lastRestartAt) < d.cfg.PaneRestartCooldown {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()

	// context_pct file missing → pane may be dead, skip restart.
	info, err := os.Stat(pctFile) //nolint:gosec // derived from trusted panesDir
	if err != nil {
		return
	}

	if !d.managerRestartNeeded(pctFile, info) {
		return
	}

	// Mark restarting to prevent concurrent restart attempts.
	d.mu.Lock()
	state.restarting = true
	restarter := d.paneRestarter
	d.mu.Unlock()

	// Write restarting sentinel file so the pane-died hook can detect an
	// in-progress restart and skip its own respawn (prevents double-respawn).
	restartingFile := filepath.Join(d.panesDir, role, "restarting")
	_ = os.WriteFile(restartingFile, []byte{}, 0o644) //nolint:gosec // trusted path
	defer os.Remove(restartingFile)                   //nolint:errcheck // best-effort cleanup

	var restartErr error
	if restarter != nil {
		restartErr = restarter.Restart(role)
		if restartErr != nil {
			_ = d.logEvent(ctx, "pane_restart_failed", "dispatcher", "", "",
				"role="+role+" error="+restartErr.Error())
		}
	}

	d.mu.Lock()
	state.restarting = false
	if restartErr == nil {
		state.lastRestartAt = d.nowFunc()
		state.restartCount++
	}
	d.mu.Unlock()
}

// managerRestartNeeded returns true if the manager pane should be restarted:
// either the context_pct exceeds the threshold or the file has not been updated
// within PaneInactivityTimeout.
func (d *Dispatcher) managerRestartNeeded(pctFile string, info os.FileInfo) bool {
	// Check context threshold.
	//nolint:gosec // pctFile derived from trusted panesDir
	data, err := os.ReadFile(pctFile)
	if err == nil {
		pctStr := strings.TrimSpace(string(data))
		pct, parseErr := strconv.Atoi(pctStr)
		if parseErr == nil && pct >= d.cfg.PaneContextThreshold {
			return true
		}
	}
	// Check inactivity: file not updated for longer than PaneInactivityTimeout.
	return d.cfg.PaneInactivityTimeout > 0 &&
		d.nowFunc().Sub(info.ModTime()) >= d.cfg.PaneInactivityTimeout
}

// signalHandoff writes the handoff_requested file and marks the pane as signaled.
func (d *Dispatcher) signalHandoff(ctx context.Context, role, roleDir string, pct int) {
	handoffFile := filepath.Join(roleDir, "handoff_requested")

	// Write handoff_requested file (empty file as signal)
	//nolint:gosec // handoffFile is derived from trusted panesDir
	if err := os.WriteFile(handoffFile, []byte{}, 0o644); err != nil {
		_ = d.logEvent(ctx, "pane_handoff_signal_failed", "dispatcher", "", "",
			"role="+role+" error="+err.Error())
		return
	}

	// Mark as signaled to prevent re-signaling
	d.mu.Lock()
	d.signaledPanes[role] = true
	d.mu.Unlock()

	_ = d.logEvent(ctx, "pane_handoff_signaled", "dispatcher", "", "",
		"role="+role+" context_pct="+strconv.Itoa(pct))
}
