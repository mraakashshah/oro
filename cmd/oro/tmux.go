package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
// Claude Code with SessionStart hooks (bd list, bd ready, git status, etc.)
// can take 30-45s to initialize.
const defaultReadyTimeout = 60 * time.Second

// pollInterval is the time between capture-pane readiness checks.
const pollInterval = 500 * time.Millisecond

// sendKeysDebounceMs is the delay between pasting text and pressing Enter.
// Claude Code's Ink TUI needs time to process pasted text before Enter.
// Must be long enough for Ink's render loop to process the input in detached
// sessions (where SIGWINCH timing adds latency). 2000ms is conservative.
const sendKeysDebounceMs = 2000

// defaultBeaconTimeout is the default time to wait for beacon verification.
const defaultBeaconTimeout = 60 * time.Second

// sessionNudgeLocks provides per-target mutexes to serialize concurrent nudges.
// Prevents interleaved send-keys when multiple goroutines nudge the same pane.
var sessionNudgeLocks sync.Map //nolint:gochecknoglobals // process-wide lock registry for pane targets

// getSessionNudgeLock returns the mutex for serializing nudges to a target.
// Creates a new mutex if one doesn't exist for this target.
func getSessionNudgeLock(target string) *sync.Mutex {
	actual, _ := sessionNudgeLocks.LoadOrStore(target, &sync.Mutex{})
	mu, ok := actual.(*sync.Mutex)
	if !ok {
		panic("sessionNudgeLocks: unexpected type in sync.Map")
	}
	return mu
}

// TmuxSession manages a tmux session with the Oro layout.
type TmuxSession struct {
	Name          string
	Project       string // optional project name; when set, adds --add-dir/--settings to Claude launch
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

// isHealthy checks whether Claude is running in both panes. Returns false
// if either pane shows a shell (zombie session — Claude crashed back to shell).
func (s *TmuxSession) isHealthy() bool {
	for _, window := range []string{"architect", "manager"} {
		pane := s.Name + ":" + window
		out, err := s.Runner.Run("tmux", "display-message", "-p", "-t", pane, "#{pane_current_command}")
		if err != nil {
			return false
		}
		if isShell(strings.TrimSpace(out)) {
			return false
		}
	}
	return true
}

// execEnvCmd builds an exec-env command that replaces the shell with Claude,
// setting ORO_ROLE, BD_ACTOR, and GIT_AUTHOR_NAME for the given role.
// Uses exec to eliminate the shell phase entirely — Claude IS the initial process.
// When project is non-empty, adds --add-dir and --settings flags pointing to
// the project's ORO_HOME directory and settings.json file.
func execEnvCmd(role, project string) string {
	base := fmt.Sprintf("exec env ORO_ROLE=%s BD_ACTOR=%s GIT_AUTHOR_NAME=%s", role, role, role)
	if project == "" {
		return base + " claude"
	}
	oroHome := os.Getenv("ORO_HOME")
	if oroHome == "" {
		home, _ := os.UserHomeDir()
		oroHome = filepath.Join(home, ".oro")
	}
	settingsPath := filepath.Join(oroHome, "projects", project, "settings.json")
	return fmt.Sprintf("%s ORO_PROJECT=%s CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1 claude --add-dir %s --settings %s",
		base, project, oroHome, settingsPath)
}

// Create creates the Oro tmux session with two windows (architect + manager).
// Both windows launch interactive claude with role env vars (ORO_ROLE, BD_ACTOR,
// GIT_AUTHOR_NAME) set, then poll for Claude readiness before injecting a short
// nudge via send-keys. The full role context is injected by the SessionStart hook
// reading the ORO_ROLE env var — send-keys only sends a short nudge/kick.
// If the session already exists, it is a no-op.
func (s *TmuxSession) Create(architectNudge, managerNudge string) error {
	if s.Exists() {
		if s.isHealthy() {
			return nil
		}
		// Zombie session: Claude is not running. Kill and recreate.
		_ = s.Kill()
	}

	// Create a detached session with first window named "architect".
	// The exec-env command is the last arg — Claude IS the initial process (no shell phase).
	if _, err := s.Runner.Run("tmux", "new-session", "-d", "-s", s.Name, "-n", "architect", execEnvCmd("architect", s.Project)); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}

	// Create second window named "manager".
	if _, err := s.Runner.Run("tmux", "new-window", "-t", s.Name, "-n", "manager", execEnvCmd("manager", s.Project)); err != nil {
		_ = s.Kill() // cleanup on partial creation failure
		return fmt.Errorf("tmux new-window: %w", err)
	}

	// Configure session options (status bar, labels, remain-on-exit, scrollback, mouse, clipboard).
	if err := s.configureSessionOptions(); err != nil {
		_ = s.Kill()
		return err
	}

	// Launch Claude in both windows, wait for readiness, and inject nudges.
	for _, w := range []struct {
		role, nudge string
	}{
		{"architect", architectNudge},
		{"manager", managerNudge},
	} {
		if err := s.launchAndNudge(w.role, w.nudge); err != nil {
			_ = s.Kill() // cleanup on partial creation failure
			return err
		}
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

	// Register pane-died hooks for crash detection.
	if err := s.RegisterPaneDiedHooks(); err != nil {
		// Warning only — don't fail startup.
		fmt.Fprintf(os.Stderr, "warning: failed to register pane-died hooks: %v\n", err)
	}

	return nil
}

// configureSessionOptions sets tmux session options: status bar color with
// window-switch hook, role labels, remain-on-exit, scrollback, mouse mode, and clipboard.
func (s *TmuxSession) configureSessionOptions() error {
	// Set initial status bar color to architect (green).
	if _, err := s.Runner.Run("tmux", "set-option", "-t", s.Name, "status-style", "bg=colour46,fg=black"); err != nil {
		return fmt.Errorf("tmux set-option status-style: %w", err)
	}

	// Set hook to change status bar color when switching windows.
	hookCmd := fmt.Sprintf(
		`if-shell -F "#{==:#{window_name},architect}" "set-option -t %s status-style bg=colour46,fg=black" "set-option -t %s status-style bg=colour208,fg=black"`,
		s.Name, s.Name,
	)
	if _, err := s.Runner.Run("tmux", "set-hook", "-t", s.Name, "after-select-window", hookCmd); err != nil {
		return fmt.Errorf("tmux set-hook status-style: %w", err)
	}

	// Add role labels to status bar.
	statusLeft := `#[bold] #{window_name} `
	if _, err := s.Runner.Run("tmux", "set-option", "-t", s.Name, "status-left", statusLeft); err != nil {
		return fmt.Errorf("tmux set-option status-left: %w", err)
	}
	if _, err := s.Runner.Run("tmux", "set-option", "-t", s.Name, "status-left-length", "20"); err != nil {
		return fmt.Errorf("tmux set-option status-left-length: %w", err)
	}
	statusRight := `#[default] oro | %H:%M`
	if _, err := s.Runner.Run("tmux", "set-option", "-t", s.Name, "status-right", statusRight); err != nil {
		return fmt.Errorf("tmux set-option status-right: %w", err)
	}

	// Enable remain-on-exit so panes survive process crashes.
	if _, err := s.Runner.Run("tmux", "set-option", "-t", s.Name, "remain-on-exit", "on"); err != nil {
		return fmt.Errorf("tmux set-option remain-on-exit: %w", err)
	}

	// Enable scrollback: disable alternate screen so Claude output flows into
	// tmux's scrollback buffer, and set generous history limit.
	if _, err := s.Runner.Run("tmux", "set-option", "-t", s.Name, "alternate-screen", "off"); err != nil {
		return fmt.Errorf("tmux set-option alternate-screen: %w", err)
	}
	if _, err := s.Runner.Run("tmux", "set-option", "-t", s.Name, "history-limit", "50000"); err != nil {
		return fmt.Errorf("tmux set-option history-limit: %w", err)
	}

	// Enable mouse mode and clipboard integration (following Gastown pattern).
	if _, err := s.Runner.Run("tmux", "set-option", "-t", s.Name, "mouse", "on"); err != nil {
		return fmt.Errorf("tmux set-option mouse: %w", err)
	}
	if _, err := s.Runner.Run("tmux", "set-option", "-t", s.Name, "set-clipboard", "on"); err != nil {
		return fmt.Errorf("tmux set-option set-clipboard: %w", err)
	}

	return nil
}

// launchAndNudge waits for Claude to be ready in a window (already launched via
// exec-env as the initial process), then sends the nudge message with verified
// delivery. No send-keys launch or WaitForCommand needed — Claude IS the process.
func (s *TmuxSession) launchAndNudge(role, nudge string) error {
	pane := s.Name + ":" + role
	if err := s.WaitForPrompt(pane); err != nil {
		return fmt.Errorf("wait for %s prompt: %w", role, err)
	}
	nudgeTimeout := s.ReadyTimeout
	if nudgeTimeout == 0 {
		nudgeTimeout = defaultReadyTimeout
	}
	if err := s.SendKeysVerified(pane, nudge, nudgeTimeout); err != nil {
		return fmt.Errorf("%s nudge: %w", role, err)
	}
	return nil
}

// isShell returns true if cmd matches a known shell process name
// (the foreground process hasn't changed from the login shell yet).
func isShell(cmd string) bool {
	switch cmd {
	case "zsh", "bash", "sh", "fish":
		return true
	}
	return false
}

// WaitForCommand polls tmux pane_current_command until the foreground process
// is no longer a shell, indicating Claude has started. This is more reliable
// than scraping pane content for a prompt character.
func (s *TmuxSession) WaitForCommand(paneTarget string) error {
	timeout := s.ReadyTimeout
	if timeout == 0 {
		timeout = defaultReadyTimeout
	}
	deadline := time.Now().Add(timeout)
	var lastCmd string

	for {
		out, err := s.Runner.Run("tmux", "display-message", "-p", "-t", paneTarget, "#{pane_current_command}")
		if err == nil {
			lastCmd = strings.TrimSpace(out)
			if lastCmd != "" && !isShell(lastCmd) {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("claude did not start in pane %s within %v (last command: %s)", paneTarget, timeout, lastCmd)
		}
		s.sleep(pollInterval)
	}
}

// PaneReady polls until Claude has started in the given pane.
// It delegates to WaitForCommand which checks pane_current_command.
func (s *TmuxSession) PaneReady(paneTarget string) error {
	return s.WaitForCommand(paneTarget)
}

// promptIndicator is the Unicode character Claude Code uses for its input prompt.
const promptIndicator = "❯"

// WaitForPrompt polls the pane content until Claude Code's prompt indicator (❯)
// appears, indicating the Ink TUI is rendered and ready for input. This must be
// called after PaneReady (process started) and before sending nudges, because
// Claude Code takes time to render the welcome screen and process SessionStart hooks.
func (s *TmuxSession) WaitForPrompt(paneTarget string) error {
	timeout := s.ReadyTimeout
	if timeout == 0 {
		timeout = defaultReadyTimeout
	}
	deadline := time.Now().Add(timeout)

	for {
		out, err := s.Runner.Run("tmux", "capture-pane", "-p", "-t", paneTarget)
		if err == nil && strings.Contains(out, promptIndicator) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("claude prompt %q not found in pane %s within %v", promptIndicator, paneTarget, timeout)
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

// sleep pauses for the given duration. It uses the Sleeper if set (for testing),
// otherwise falls back to time.Sleep.
func (s *TmuxSession) sleep(d time.Duration) {
	if s.Sleeper != nil {
		s.Sleeper(d)
		return
	}
	time.Sleep(d)
}

// SendKeys sends text to a Claude Code tmux pane and presses Enter.
// Uses set-buffer + paste-buffer (same as TmuxEscalator) for reliable delivery
// to Claude Code's Ink TUI, then sends Enter separately with retry.
// Finishes with a SIGWINCH wake for detached sessions.
//
// Shell commands should use Runner.Run("tmux", "send-keys", ...) directly;
// this method is for sending input to an already-running Claude session.
func (s *TmuxSession) SendKeys(paneTarget, text string) error {
	// Serialize nudges to this target to prevent interleaving
	lock := getSessionNudgeLock(paneTarget)
	lock.Lock()
	defer lock.Unlock()

	// 1. Send text using literal mode (-l) to handle special chars.
	if _, err := s.Runner.Run("tmux", "send-keys", "-t", paneTarget, "-l", text); err != nil {
		return fmt.Errorf("tmux send-keys -l to %s: %w", paneTarget, err)
	}

	// 2. Wake the pane so Ink processes the text in detached sessions.
	// Without SIGWINCH, Ink's render loop may not see the input,
	// causing Enter to act on an empty input field.
	s.wakeIfDetached(paneTarget)

	// 3. Wait for text to be processed by Ink's render loop.
	s.sleep(time.Duration(sendKeysDebounceMs) * time.Millisecond)

	// 3.5. Send Escape to exit any vim-mode INSERT state before Enter.
	// Harmless when vim mode is off; critical when it's on.
	_, _ = s.Runner.Run("tmux", "send-keys", "-t", paneTarget, "Escape")
	// Wake so Ink processes the Escape before Enter arrives.
	// Without this, both keystrokes queue up and Escape can swallow Enter
	// in detached sessions.
	s.wakeIfDetached(paneTarget)
	s.sleep(100 * time.Millisecond)

	// 4. Send Enter with retry — critical for message submission.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			s.sleep(200 * time.Millisecond)
		}
		if _, err := s.Runner.Run("tmux", "send-keys", "-t", paneTarget, "Enter"); err != nil {
			lastErr = err
			continue
		}
		// 6. Wake again so Ink processes the Enter in detached sessions.
		s.wakeIfDetached(paneTarget)
		return nil
	}
	return fmt.Errorf("failed to send Enter to %s after 3 attempts: %w", paneTarget, lastErr)
}

// sendKeysVerifiedPollInterval is the interval between capture-pane checks
// when verifying nudge text delivery.
const sendKeysVerifiedPollInterval = 500 * time.Millisecond

// verifyHintMaxLen is the max length of the text prefix used for
// capture-pane verification. Long nudges may be wrapped/truncated by the
// TUI, so we only check for a short prefix.
const verifyHintMaxLen = 30

// verifyHint returns a short prefix of text for capture-pane verification.
func verifyHint(text string) string {
	if len(text) <= verifyHintMaxLen {
		return text
	}
	return text[:verifyHintMaxLen]
}

// SendKeysVerified sends text to a pane and verifies it appeared via
// capture-pane. If the text doesn't appear, it clears the input (C-u)
// and retries. Returns error if text never appears within timeout.
// Only checks for a short prefix of the text since long nudges may be
// wrapped or truncated by Claude Code's Ink TUI.
func (s *TmuxSession) SendKeysVerified(paneTarget, text string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	hint := verifyHint(text)
	firstAttempt := true

	for {
		if !firstAttempt {
			// Clear any partial input before retrying.
			_, _ = s.Runner.Run("tmux", "send-keys", "-t", paneTarget, "C-u")
			s.sleep(100 * time.Millisecond)
		}
		firstAttempt = false

		if err := s.SendKeys(paneTarget, text); err != nil {
			if time.Now().After(deadline) {
				return fmt.Errorf("nudge text %q not delivered to %s within %v: %w", hint, paneTarget, timeout, err)
			}
			s.sleep(sendKeysVerifiedPollInterval)
			continue
		}

		// Verify text prefix appeared in pane content.
		out, err := s.Runner.Run("tmux", "capture-pane", "-p", "-t", paneTarget)
		if err == nil && strings.Contains(out, hint) {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("nudge text %q not visible in pane %s within %v", hint, paneTarget, timeout)
		}
		s.sleep(sendKeysVerifiedPollInterval)
	}
}

// wakeIfDetached sends SIGWINCH to the pane's process when no clients are
// attached. This wakes Claude Code's Ink render loop in detached sessions.
// Uses direct kill -WINCH via the pane PID rather than resize, which is
// more reliable at delivering the signal to Node.js/Ink.
func (s *TmuxSession) wakeIfDetached(paneTarget string) {
	// Check if any client is attached to this session.
	out, err := s.Runner.Run("tmux", "display-message", "-p", "-t", paneTarget, "#{session_attached}")
	if err == nil && strings.TrimSpace(out) != "0" {
		return // attached, no wake needed
	}
	// Get the pane's child PID and send SIGWINCH directly.
	pidStr, err := s.Runner.Run("tmux", "display-message", "-p", "-t", paneTarget, "#{pane_pid}")
	if err != nil {
		return
	}
	_, _ = s.Runner.Run("kill", "-WINCH", strings.TrimSpace(pidStr))
}

// processKillGracePeriod is the time to wait between SIGTERM and SIGKILL.
const processKillGracePeriod = 2 * time.Second

// getProcessGroupID returns the process group ID for a given PID.
func getProcessGroupID(pid string) string {
	out, err := exec.CommandContext(context.Background(), "ps", "-o", "pgid=", "-p", pid).Output() //nolint:gosec // pid comes from tmux pane_pid, not user input
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// getAllDescendants recursively finds all descendant PIDs of a process.
// Returns PIDs in deepest-first order so killing them doesn't orphan grandchildren.
func getAllDescendants(pid string) []string {
	var result []string
	out, err := exec.CommandContext(context.Background(), "pgrep", "-P", pid).Output() //nolint:gosec // pid comes from tmux pane_pid, not user input
	if err != nil {
		return result
	}
	children := strings.Fields(strings.TrimSpace(string(out)))
	for _, child := range children {
		result = append(result, getAllDescendants(child)...)
		result = append(result, child)
	}
	return result
}

// Kill destroys the named tmux session after cleaning up descendant processes.
// Walks the process tree to prevent orphaned Claude/node processes.
func (s *TmuxSession) Kill() error {
	// Get pane PIDs before killing session
	for _, window := range []string{"architect", "manager"} {
		pane := s.Name + ":" + window
		pidStr, err := s.Runner.Run("tmux", "display-message", "-p", "-t", pane, "#{pane_pid}")
		if err != nil {
			continue // pane may not exist
		}
		pid := strings.TrimSpace(pidStr)
		if pid == "" || pid == "0" {
			continue
		}

		// Kill process group
		pgid := getProcessGroupID(pid)
		if pgid != "" && pgid != "0" && pgid != "1" {
			pgidInt, err := strconv.Atoi(pgid)
			if err == nil {
				_ = syscall.Kill(-pgidInt, syscall.SIGTERM)
			}
		}

		// Walk descendants
		descendants := getAllDescendants(pid)
		for _, dpid := range descendants {
			_ = exec.CommandContext(context.Background(), "kill", "-TERM", dpid).Run() //nolint:gosec // dpid comes from pgrep of tmux pane_pid
		}

		// Grace period
		s.sleep(processKillGracePeriod)

		// SIGKILL remaining
		for _, dpid := range descendants {
			_ = exec.CommandContext(context.Background(), "kill", "-KILL", dpid).Run() //nolint:gosec // dpid comes from pgrep of tmux pane_pid
		}
		_ = exec.CommandContext(context.Background(), "kill", "-KILL", pid).Run() //nolint:gosec // pid comes from tmux pane_pid
	}

	// Finally kill the tmux session
	_, err := s.Runner.Run("tmux", "kill-session", "-t", s.Name)
	if err != nil {
		return fmt.Errorf("tmux kill-session: %w", err)
	}
	return nil
}

// RespawnPane kills all processes in a pane and starts a new command.
// Used for crash recovery — restarts Claude in-place without recreating the session.
func (s *TmuxSession) RespawnPane(paneTarget, command string) error {
	_, err := s.Runner.Run("tmux", "respawn-pane", "-k", "-t", paneTarget, command)
	if err != nil {
		return fmt.Errorf("tmux respawn-pane: %w", err)
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

// RegisterPaneDiedHooks registers pane-died hooks for both architect and manager
// panes to detect when either pane crashes or closes. Each hook sends an escalation
// message to the surviving pane and logs the event to the dispatcher log.
func (s *TmuxSession) RegisterPaneDiedHooks() error {
	// Register hook for architect pane
	architectPane := s.Name + ":architect"
	architectHook := buildPaneDiedHook("architect", s.Name, s.Project)
	if _, err := s.Runner.Run("tmux", "set-hook", "-t", architectPane, "pane-died", architectHook); err != nil {
		return fmt.Errorf("register pane-died hook for architect: %w", err)
	}

	// Register hook for manager pane
	managerPane := s.Name + ":manager"
	managerHook := buildPaneDiedHook("manager", s.Name, s.Project)
	if _, err := s.Runner.Run("tmux", "set-hook", "-t", managerPane, "pane-died", managerHook); err != nil {
		return fmt.Errorf("register pane-died hook for manager: %w", err)
	}

	return nil
}

// buildPaneDiedHook constructs a tmux hook command for pane-died events.
// The hook respawns the dead pane with the same exec-env command (crash recovery),
// then sends an escalation message to the surviving pane. The [ORO-DISPATCH]
// prefix in the message serves as the logging mechanism — the manager receives
// and can act on the crash notification. Since the dying pane triggers the hook,
// the message goes to the other pane (architect→manager or manager→architect).
func buildPaneDiedHook(dyingRole, sessionName, project string) string {
	respawnCmd := execEnvCmd(dyingRole, project)
	dyingPane := sessionName + ":" + dyingRole

	// Determine the surviving role and pane
	survivingRole := "manager"
	if dyingRole == "manager" {
		survivingRole = "architect"
	}
	survivingPane := sessionName + ":" + survivingRole

	// Build an escalation-style message for the surviving pane
	escalationMsg := fmt.Sprintf("[ORO-DISPATCH] PANE_RESPAWNED: %s pane crashed and was respawned.", dyingRole)

	// Respawn the dead pane with the same command, plus notify surviving pane
	// Note: escapeForShell already wraps output in single quotes, so we use %s not '%s'
	hook := fmt.Sprintf(
		"run-shell \"tmux respawn-pane -k -t %s %s; tmux set-buffer -b oro-pane-died %s; tmux paste-buffer -b oro-pane-died -t %s -d; tmux send-keys -t %s Enter\"",
		dyingPane,
		escapeForShell(respawnCmd),
		escapeForShell(sanitizeForTmuxHook(escalationMsg)),
		survivingPane,
		survivingPane,
	)
	return hook
}

// sanitizeForTmuxHook prepares a message for safe use in a tmux hook shell command.
// Strips newlines and replaces problematic characters.
func sanitizeForTmuxHook(msg string) string {
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	return msg
}

// escapeForShell escapes a string for safe use in a shell command within a tmux hook.
// Wraps with single quotes and escapes any single quotes in the content.
func escapeForShell(s string) string {
	// Replace single quotes with '\'' (end quote, escaped quote, start quote)
	escaped := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + escaped + "'"
}

// CleanupPaneDiedHooks removes the pane-died hooks from both architect and manager panes.
//
//nolint:unparam // error return kept for interface consistency; errors are logged not propagated
func (s *TmuxSession) CleanupPaneDiedHooks() error {
	// Only attempt cleanup if session exists
	if !s.Exists() {
		return nil
	}

	// Remove hook from architect pane
	architectPane := s.Name + ":architect"
	if _, err := s.Runner.Run("tmux", "set-hook", "-u", "-t", architectPane, "pane-died"); err != nil {
		// Non-fatal — hook may not have been registered
		fmt.Fprintf(os.Stderr, "warning: failed to unregister pane-died hook for architect: %v\n", err)
	}

	// Remove hook from manager pane
	managerPane := s.Name + ":manager"
	if _, err := s.Runner.Run("tmux", "set-hook", "-u", "-t", managerPane, "pane-died"); err != nil {
		// Non-fatal — hook may not have been registered
		fmt.Fprintf(os.Stderr, "warning: failed to unregister pane-died hook for manager: %v\n", err)
	}

	return nil
}
