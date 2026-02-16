package dispatcher

import (
	"context"
	"fmt"
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
		}
	}
}

// checkPaneContexts reads context_pct for each role and signals handoff if
// threshold is exceeded and pane hasn't been signaled before. Also detects
// handoff_complete signal and triggers pane restart.
func (d *Dispatcher) checkPaneContexts(ctx context.Context, roles []string) {
	for _, role := range roles {
		// Check for handoff completion first (higher priority)
		if d.checkHandoffComplete(ctx, role) {
			continue // Skip context check if restart was triggered
		}
		// Normal context percentage check
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

// checkHandoffComplete checks if handoff_complete signal exists for a role
// and triggers pane restart if found. Returns true if restart was triggered.
func (d *Dispatcher) checkHandoffComplete(ctx context.Context, role string) bool {
	roleDir := filepath.Join(d.panesDir, role)
	completeFile := filepath.Join(roleDir, "handoff_complete")

	// Check if handoff_complete exists
	//nolint:gosec // completeFile is derived from trusted panesDir
	if _, err := os.Stat(completeFile); err != nil {
		// File doesn't exist - no restart needed
		return false
	}

	// handoff_complete exists - trigger restart
	_ = d.logEvent(ctx, "pane_handoff_complete_detected", "dispatcher", "", "",
		"role="+role)

	d.restartPane(ctx, role, roleDir)
	return true
}

// buildExecEnvCmd constructs the exec-env command for launching a role pane.
// Matches the pattern from cmd/oro/tmux.go:execEnvCmd but accesses env vars
// directly since dispatcher doesn't track project identity.
func (d *Dispatcher) buildExecEnvCmd(role string) string {
	// Read project from environment (set by oro start)
	project := os.Getenv("ORO_PROJECT")

	// Determine claude config directory for role
	var configBase string
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		configBase = dir
	} else {
		home, _ := os.UserHomeDir()
		configBase = filepath.Join(home, ".claude")
	}
	configDir := filepath.Join(configBase, "roles", role)

	base := fmt.Sprintf("exec env ORO_ROLE=%s BD_ACTOR=%s GIT_AUTHOR_NAME=%s CLAUDE_CONFIG_DIR=%s",
		role, role, role, configDir)

	if project == "" {
		return base + " claude"
	}

	// When project is set, add --add-dir and --settings flags
	oroHome := os.Getenv("ORO_HOME")
	if oroHome == "" {
		home, _ := os.UserHomeDir()
		oroHome = filepath.Join(home, ".oro")
	}
	settingsPath := filepath.Join(oroHome, "projects", project, "settings.json")
	return fmt.Sprintf("%s ORO_PROJECT=%s CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1 claude --add-dir %s --settings %s",
		base, project, oroHome, settingsPath)
}

// restartPane performs the pane restart sequence:
// 1. Delete signal files (handoff_requested, handoff_complete, context_pct)
// 2. Kill tmux pane for the role
// 3. Relaunch pane with same role configuration
// 4. Reset signaled tracking
func (d *Dispatcher) restartPane(ctx context.Context, role, roleDir string) {
	// 1. Delete signal files
	filesToDelete := []string{
		filepath.Join(roleDir, "handoff_requested"),
		filepath.Join(roleDir, "handoff_complete"),
		filepath.Join(roleDir, "context_pct"),
	}

	for _, file := range filesToDelete {
		//nolint:gosec // file paths are derived from trusted panesDir
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			_ = d.logEvent(ctx, "pane_restart_cleanup_failed", "dispatcher", "", "",
				"role="+role+" file="+file+" error="+err.Error())
		}
	}

	// 2. Kill tmux pane (ignore error if pane already dead)
	paneTarget := "oro:" + role
	d.mu.Lock()
	session := d.tmuxSession
	d.mu.Unlock()

	if session != nil {
		if err := session.KillPane(paneTarget); err != nil {
			// Log but don't fail - pane might already be dead
			_ = d.logEvent(ctx, "pane_restart_kill_failed", "dispatcher", "", "",
				"role="+role+" error="+err.Error())
		}
	}

	// 3. Relaunch pane with same role configuration
	if session != nil {
		// Build exec-env command matching cmd/oro/tmux.go:execEnvCmd
		command := d.buildExecEnvCmd(role)
		if err := session.RespawnPane(paneTarget, command); err != nil {
			_ = d.logEvent(ctx, "pane_restart_relaunch_failed", "dispatcher", "", "",
				"role="+role+" error="+err.Error())
			return
		}
	}

	// 4. Reset signaled tracking
	d.mu.Lock()
	delete(d.signaledPanes, role)
	d.mu.Unlock()

	_ = d.logEvent(ctx, "pane_restarted", "dispatcher", "", "",
		"role="+role)
}
