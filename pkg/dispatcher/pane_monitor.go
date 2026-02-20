package dispatcher

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

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
// percentage, and signals handoff if it exceeds the threshold.
func (d *Dispatcher) checkPaneContext(ctx context.Context, role string) {
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
