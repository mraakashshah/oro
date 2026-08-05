package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// noopSleep is a no-op sleeper for tests to avoid real delays.
func noopSleep(time.Duration) {}

func TestMain(m *testing.M) {
	tempRoot, err := os.MkdirTemp("", "oro-manager-runtime-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mkdir manager runtime test root: %v\n", err)
		os.Exit(1)
	}
	testMainRuntimeRoot = tempRoot
	testMainInheritedGoCache = os.Getenv("GOCACHE")
	originalHome := os.Getenv("HOME")
	if os.Getenv("GOMODCACHE") == "" && originalHome != "" {
		if err := os.Setenv("GOMODCACHE", filepath.Join(originalHome, "go", "pkg", "mod")); err != nil {
			fmt.Fprintf(os.Stderr, "set GOMODCACHE for manager runtime tests: %v\n", err)
			os.Exit(1)
		}
	}

	home := filepath.Join(tempRoot, "home")
	oroHome := filepath.Join(tempRoot, "oro-home")
	// Startup tests run the production Go cache cleaner. Keep that subprocess
	// away from the parent `go test` compiler cache.
	goCache := filepath.Join(tempRoot, "go-build")
	for _, dir := range []string{home, oroHome, goCache} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "mkdir manager runtime test dir: %v\n", err)
			os.Exit(1)
		}
	}

	previousConfigPath := managerRuntimeConfigPath
	managerRuntimeConfigPath = func() string {
		return filepath.Join(tempRoot, "project", ".oro", "config.yaml")
	}
	if err := os.Setenv("HOME", home); err != nil {
		fmt.Fprintf(os.Stderr, "set HOME for manager runtime tests: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("ORO_HOME", oroHome); err != nil {
		fmt.Fprintf(os.Stderr, "set ORO_HOME for manager runtime tests: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("GOCACHE", goCache); err != nil {
		fmt.Fprintf(os.Stderr, "set GOCACHE for manager runtime tests: %v\n", err)
		os.Exit(1)
	}
	// Clear transient ORO_* runtime vars a factory worker exports (ORO_WORKER_ID,
	// ORO_SOCKET_PATH, ...) so inherited caller state cannot bleed into tests. The
	// hermetic HOME/ORO_HOME redirect above prevents real ~/.oro pollution.
	if err := sanitizeInheritedOroEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "sanitize inherited oro env for tests: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	managerRuntimeConfigPath = previousConfigPath
	_ = os.RemoveAll(tempRoot)
	os.Exit(code)
}

func isolateAgentRuntimeConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	oroHome := filepath.Join(home, "oro-home")
	if err := os.MkdirAll(oroHome, 0o700); err != nil {
		t.Fatalf("mkdir oro home: %v", err)
	}
	projectConfigPath := filepath.Join(home, "project", ".oro", "config.yaml")
	previousConfigPath := managerRuntimeConfigPath
	managerRuntimeConfigPath = func() string { return projectConfigPath }
	t.Cleanup(func() { managerRuntimeConfigPath = previousConfigPath })
	t.Setenv("HOME", home)
	t.Setenv("ORO_HOME", oroHome)
	return oroHome
}

func writeAgentRuntimeConfig(t *testing.T, oroHome, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(oroHome, "config.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write agent config: %v", err)
	}
}

// fakeCmd records exec calls for testing without real tmux.
// It supports both single-value and sequential (multi-value) outputs per key.
// The mu mutex makes it safe for concurrent use (e.g., parallel SendKeys tests).
type fakeCmd struct {
	mu     sync.Mutex
	calls  [][]string // each call is [name, arg1, arg2, ...]
	output map[string]string
	errs   map[string]error
	seqOut map[string][]string // sequential outputs per key
	seqIdx map[string]int      // current index into seqOut per key
}

func newFakeCmd() *fakeCmd {
	return &fakeCmd{
		output: make(map[string]string),
		errs:   make(map[string]error),
		seqOut: make(map[string][]string),
		seqIdx: make(map[string]int),
	}
}

// key builds a lookup key from a command and its args.
func key(name string, args ...string) string {
	return name + " " + strings.Join(args, " ")
}

func (f *fakeCmd) Run(name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{name}, args...))
	k := key(name, args...)
	// Check for sequential output first.
	if seq, ok := f.seqOut[k]; ok {
		idx := f.seqIdx[k]
		if idx < len(seq) {
			f.seqIdx[k] = idx + 1
			return seq[idx], f.errs[k]
		}
		// Past the end of sequence: return last value.
		return seq[len(seq)-1], f.errs[k]
	}
	if err, ok := f.errs[k]; ok {
		return f.output[k], err
	}
	return f.output[k], nil
}

// getCalls returns a snapshot of the recorded calls (thread-safe).
func (f *fakeCmd) getCalls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	snapshot := make([][]string, len(f.calls))
	copy(snapshot, f.calls)
	return snapshot
}

// stubPaneReady sets up the fake so WaitForPrompt sees the ❯ prompt indicator
// (i.e., Claude's TUI is ready) and SendKeysVerified sees the nudge text in
// capture-pane output. With exec-env, no WaitForCommand stubs are needed since
// Claude IS the initial process. capture-pane is called sequentially: first by
// WaitForPrompt, then by SendKeysVerified, so we use seqOut to return ❯ first,
// then nudge text.
func stubPaneReady(fake *fakeCmd, sessionName, sessionNudge string) {
	mgrCapture := key("tmux", "capture-pane", "-p", "-t", sessionName+":manager")
	fake.seqOut[mgrCapture] = []string{
		"Welcome\n❯ \nstatus bar",                     // WaitForPrompt
		"Welcome\n❯ " + sessionNudge + "\nstatus bar", // SendKeysVerified
		"oro task status\nrunning\n",                  // VerifyBeaconReceived (async goroutine)
	}
}

// findCall returns the first call matching the given tmux subcommand, or nil.
func findCall(calls [][]string, subcmd string) []string {
	for _, call := range calls {
		if len(call) >= 2 && call[0] == "tmux" && call[1] == subcmd {
			return call
		}
	}
	return nil
}

// callHasArg checks whether a call slice contains the given argument.
func callHasArg(call []string, arg string) bool {
	for _, a := range call {
		if a == arg {
			return true
		}
	}
	return false
}

// callHasArgPair checks whether a call slice contains arg followed by val.
func callHasArgPair(call []string, arg, val string) bool {
	for i, a := range call {
		if a == arg && i+1 < len(call) && call[i+1] == val {
			return true
		}
	}
	return false
}

func TestTmuxLayout(t *testing.T) {
	isolateAgentRuntimeConfig(t)

	t.Run("Create builds managerless attach session", func(t *testing.T) {
		fake := newFakeCmd()
		// has-session returns error (session does not exist)
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create()
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify: new-session was called with -d, -s oro, and -n oro.
		newSessionCall := findCall(fake.calls, "new-session")
		if newSessionCall == nil {
			t.Fatal("expected tmux new-session to be called")
		}
		if !callHasArg(newSessionCall, "-d") {
			t.Error("new-session should use -d (detached)")
		}
		if !callHasArgPair(newSessionCall, "-s", "oro") {
			t.Error("new-session should name the session 'oro'")
		}
		if !callHasArgPair(newSessionCall, "-n", defaultTmuxWindowName) {
			t.Errorf("new-session should name the window %q", defaultTmuxWindowName)
		}

		// Verify: new-window was NOT called (managerless attach surface has one window)
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "new-window" {
				t.Error("new-window should not be called")
			}
		}

		// Verify: window-style should NOT be set (use default/white text color)
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-option" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "window-style") {
					t.Errorf("window-style should not be set (use default/white text), got: %s", joined)
				}
			}
		}
	})

	t.Run("Create reuses existing session", func(t *testing.T) {
		fake := newFakeCmd()
		// has-session succeeds (session exists)
		fake.output[key("tmux", "has-session", "-t", "oro")] = ""
		// manager pane shows claude (healthy) — Create returns without recreation
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:manager", "#{pane_current_command}")] = "claude"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.Create()
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		sess.WaitBeacon()

		// Verify: new-session was NOT called
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "new-session" {
				t.Error("should not create new session when one already exists")
			}
		}
	})

	t.Run("Kill destroys the session", func(t *testing.T) {
		fake := newFakeCmd()
		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.Kill()
		if err != nil {
			t.Fatalf("Kill returned error: %v", err)
		}

		foundKill := false
		for _, call := range fake.calls {
			if len(call) >= 4 && call[0] == "tmux" && call[1] == "kill-session" && call[2] == "-t" && call[3] == "oro" {
				foundKill = true
			}
		}
		if !foundKill {
			t.Error("expected tmux kill-session -t oro to be called")
		}
	})

	t.Run("Exists returns true when session is running", func(t *testing.T) {
		fake := newFakeCmd()
		// has-session succeeds
		fake.output[key("tmux", "has-session", "-t", "oro")] = ""

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		if !sess.Exists() {
			t.Error("expected Exists to return true when has-session succeeds")
		}
	})

	t.Run("Exists returns false when session is not running", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		if sess.Exists() {
			t.Error("expected Exists to return false when has-session fails")
		}
	})

	t.Run("ListPanes returns pane indices", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "list-panes", "-t", "oro", "-F", "#{pane_index}")] = "0\n1\n"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		panes, err := sess.ListPanes()
		if err != nil {
			t.Fatalf("ListPanes returned error: %v", err)
		}
		if len(panes) != 2 {
			t.Fatalf("expected 2 panes, got %d", len(panes))
		}
		if panes[0] != "0" || panes[1] != "1" {
			t.Errorf("expected panes [0, 1], got %v", panes)
		}
	})

	t.Run("ListPanes returns empty on no session", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "list-panes", "-t", "oro", "-F", "#{pane_index}")] = fmt.Errorf("no session")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		_, err := sess.ListPanes()
		if err == nil {
			t.Error("expected error when list-panes fails")
		}
	})

	t.Run("Create does not launch manager runtime", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create()
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		newSessionCall := findCall(fake.calls, "new-session")
		if newSessionCall == nil {
			t.Fatal("expected tmux new-session to be called")
		}
		for _, arg := range newSessionCall {
			if strings.Contains(arg, "claude") || strings.Contains(arg, "codex") || strings.Contains(arg, "ORO_ROLE=manager") {
				t.Errorf("managerless new-session should not launch a manager runtime, got: %v", newSessionCall)
			}
		}

		// Verify new-window was NOT called.
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "new-window" {
				t.Error("new-window should not be called")
			}
		}
	})

	t.Run("stop command kills tmux session", func(t *testing.T) {
		fake := newFakeCmd()
		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}

		err := sess.Kill()
		if err != nil {
			t.Fatalf("Kill returned error: %v", err)
		}

		found := false
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "kill-session" {
				found = true
			}
		}
		if !found {
			t.Error("expected kill-session to be called")
		}
	})

	t.Run("status checks session existence", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "has-session", "-t", "oro")] = ""

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		exists := sess.Exists()
		if !exists {
			t.Error("expected session to exist")
		}

		// Verify has-session was called
		found := false
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "has-session" {
				found = true
			}
		}
		if !found {
			t.Error("expected has-session to be called")
		}
	})
}

func TestWaitForCommand(t *testing.T) {
	displayKey := func(pane string) string {
		return key("tmux", "display-message", "-p", "-t", pane, "#{pane_current_command}")
	}

	t.Run("returns nil when command is claude immediately", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[displayKey("oro:architect")] = "claude"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second}
		err := sess.WaitForCommand("oro:architect")
		if err != nil {
			t.Fatalf("WaitForCommand returned error: %v", err)
		}
	})

	t.Run("polls until command changes from shell to claude", func(t *testing.T) {
		fake := newFakeCmd()
		fake.seqOut[displayKey("oro:architect")] = []string{
			"zsh",
			"zsh",
			"claude",
		}

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: 5 * time.Second}
		err := sess.WaitForCommand("oro:architect")
		if err != nil {
			t.Fatalf("WaitForCommand returned error: %v", err)
		}

		// Count display-message calls — should be exactly 3.
		count := 0
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "display-message" {
				count++
			}
		}
		if count != 3 {
			t.Errorf("expected 3 display-message calls, got %d", count)
		}
	})

	t.Run("times out when command stays as shell", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[displayKey("oro:architect")] = "zsh"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: 50 * time.Millisecond}
		err := sess.WaitForCommand("oro:architect")
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "did not start") {
			t.Errorf("expected 'did not start' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "oro:architect") {
			t.Errorf("expected pane target in error, got: %v", err)
		}
		// Should include last seen command for diagnostics
		if !strings.Contains(err.Error(), "zsh") {
			t.Errorf("expected last command in error, got: %v", err)
		}
	})

	t.Run("recognizes bash as shell", func(t *testing.T) {
		fake := newFakeCmd()
		fake.seqOut[displayKey("oro:manager")] = []string{
			"bash",
			"claude",
		}

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: 5 * time.Second}
		err := sess.WaitForCommand("oro:manager")
		if err != nil {
			t.Fatalf("WaitForCommand returned error: %v", err)
		}
	})

	t.Run("tolerates display-message errors and keeps polling", func(t *testing.T) {
		fake := newFakeCmd()
		k := displayKey("oro:architect")
		fake.seqOut[k] = []string{
			"",       // empty (error)
			"claude", // ready
		}

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: 5 * time.Second}
		err := sess.WaitForCommand("oro:architect")
		if err != nil {
			t.Fatalf("WaitForCommand returned error: %v", err)
		}
	})

	t.Run("uses default timeout when ReadyTimeout is zero", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[displayKey("oro:architect")] = "claude"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.WaitForCommand("oro:architect")
		if err != nil {
			t.Fatalf("WaitForCommand returned error: %v", err)
		}
	})
}

func TestWaitForPromptAcceptsTrustDialog(t *testing.T) {
	captureKey := key("tmux", "capture-pane", "-p", "-t", "oro:architect")

	t.Run("detects trust dialog and sends Enter to accept", func(t *testing.T) {
		fake := newFakeCmd()
		// Poll 1: trust dialog visible. Poll 2: prompt appears after acceptance.
		fake.seqOut[captureKey] = []string{
			"Quick safety check: Is this a project you created or one you trust?\n  Yes, proceed",
			"Welcome\n❯ \nstatus bar",
		}

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: 5 * time.Second}
		err := sess.WaitForPrompt("oro:architect")
		if err != nil {
			t.Fatalf("WaitForPrompt should accept trust dialog, got: %v", err)
		}

		// Verify Enter was sent to dismiss the dialog.
		found := false
		for _, call := range fake.getCalls() {
			if len(call) >= 4 && call[0] == "tmux" && call[1] == "send-keys" && call[len(call)-1] == "Enter" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected send-keys Enter to dismiss trust dialog, but no Enter was sent")
		}
	})

	t.Run("sends Enter only once even if dialog persists across polls", func(t *testing.T) {
		fake := newFakeCmd()
		fake.seqOut[captureKey] = []string{
			"Quick safety check: Is this a project you created or one you trust?",
			"Quick safety check: Is this a project you created or one you trust?",
			"Welcome\n❯ \nstatus bar",
		}

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: 5 * time.Second}
		err := sess.WaitForPrompt("oro:architect")
		if err != nil {
			t.Fatalf("WaitForPrompt should succeed, got: %v", err)
		}

		enterCount := 0
		for _, call := range fake.getCalls() {
			if len(call) >= 4 && call[0] == "tmux" && call[1] == "send-keys" && call[len(call)-1] == "Enter" {
				enterCount++
			}
		}
		if enterCount != 1 {
			t.Errorf("expected exactly 1 Enter send, got %d", enterCount)
		}
	})

	t.Run("no Enter sent when prompt appears without trust dialog", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[captureKey] = "Welcome\n❯ \nstatus bar"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: 5 * time.Second}
		err := sess.WaitForPrompt("oro:architect")
		if err != nil {
			t.Fatalf("WaitForPrompt should succeed, got: %v", err)
		}

		for _, call := range fake.getCalls() {
			if len(call) >= 4 && call[0] == "tmux" && call[1] == "send-keys" && call[len(call)-1] == "Enter" {
				t.Error("should not send Enter when no trust dialog appears")
			}
		}
	})
}

func TestVerifyBeaconReceived(t *testing.T) {
	t.Run("returns nil when indicator found immediately", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:manager")] = "some output\noro bead status\nmore output"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.VerifyBeaconReceived("oro:manager", "oro bead status", time.Second)
		if err != nil {
			t.Fatalf("VerifyBeaconReceived returned error: %v", err)
		}
	})

	t.Run("returns nil after polling succeeds on third attempt", func(t *testing.T) {
		fake := newFakeCmd()
		captureKey := key("tmux", "capture-pane", "-p", "-t", "oro:manager")
		fake.seqOut[captureKey] = []string{
			"claude loading...",
			"still waiting...",
			"running oro bead status\noutput here",
		}

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.VerifyBeaconReceived("oro:manager", "oro bead status", 5*time.Second)
		if err != nil {
			t.Fatalf("VerifyBeaconReceived returned error: %v", err)
		}

		// Count capture-pane calls — should be exactly 3.
		count := 0
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "capture-pane" {
				count++
			}
		}
		if count != 3 {
			t.Errorf("expected 3 capture-pane calls, got %d", count)
		}
	})

	t.Run("returns error on timeout with diagnostic pane content", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:manager")] = "stuck on loading screen"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.VerifyBeaconReceived("oro:manager", "oro bead status", 50*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "oro:manager") {
			t.Errorf("expected window target in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "oro bead status") {
			t.Errorf("expected indicator in error, got: %v", err)
		}
		// Error should include last captured pane content for diagnostics
		if !strings.Contains(err.Error(), "stuck on loading screen") {
			t.Errorf("expected pane content in error for diagnostics, got: %v", err)
		}
	})

	t.Run("indicator matching is substring-based", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:architect")] = "some text with > prompt visible"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.VerifyBeaconReceived("oro:architect", ">", time.Second)
		if err != nil {
			t.Fatalf("VerifyBeaconReceived returned error: %v", err)
		}
	})

	t.Run("tolerates capture-pane errors and keeps polling", func(t *testing.T) {
		fake := newFakeCmd()
		captureKey := key("tmux", "capture-pane", "-p", "-t", "oro:manager")
		// First call returns empty (simulating error), second has indicator
		fake.seqOut[captureKey] = []string{
			"",
			"oro bead status\nsome output",
		}

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.VerifyBeaconReceived("oro:manager", "oro bead status", 5*time.Second)
		if err != nil {
			t.Fatalf("VerifyBeaconReceived returned error: %v", err)
		}
	})
}

func TestManagerBinaryReadsConfiguredRuntime(t *testing.T) {
	t.Run("agent balanced runtime overrides env var", func(t *testing.T) {
		oroHome := isolateAgentRuntimeConfig(t)
		t.Setenv(agentRuntimeEnvVar, runtimeClaude)
		writeAgentRuntimeConfig(t, oroHome, `
agent:
  tiers:
    balanced:
      runtime: codex
      model: gpt-5.5
`)

		if got := activeRuntime(); got != runtimeCodex {
			t.Fatalf("activeRuntime() = %q, want %q", got, runtimeCodex)
		}
		if got := runtimeBinary(); got != "codex" {
			t.Fatalf("runtimeBinary() = %q, want codex", got)
		}
		if got := execEnvCmd("manager", ""); strings.HasSuffix(got, " claude") || strings.Contains(got, "CLAUDE_CONFIG_DIR=") {
			t.Fatalf("manager command should use codex and omit Claude config, got: %s", got)
		}
	})

	t.Run("agent balanced claude overrides codex env var", func(t *testing.T) {
		oroHome := isolateAgentRuntimeConfig(t)
		t.Setenv(agentRuntimeEnvVar, runtimeCodex)
		writeAgentRuntimeConfig(t, oroHome, `
agent:
  tiers:
    balanced:
      runtime: claude
      model: claude-sonnet-4-6
`)

		if got := activeRuntime(); got != runtimeClaude {
			t.Fatalf("activeRuntime() = %q, want %q", got, runtimeClaude)
		}
		if got := runtimeBinary(); got != "claude" {
			t.Fatalf("runtimeBinary() = %q, want claude", got)
		}
	})

	t.Run("no agent block falls back to env runtime", func(t *testing.T) {
		isolateAgentRuntimeConfig(t)
		t.Setenv(agentRuntimeEnvVar, runtimeCodex)

		if got := activeRuntime(); got != runtimeCodex {
			t.Fatalf("activeRuntime() = %q, want %q", got, runtimeCodex)
		}
	})

	t.Run("no agent block defaults to claude", func(t *testing.T) {
		isolateAgentRuntimeConfig(t)
		t.Setenv(agentRuntimeEnvVar, "")

		if got := activeRuntime(); got != runtimeClaude {
			t.Fatalf("activeRuntime() = %q, want %q", got, runtimeClaude)
		}
	})
}

func TestExecEnvCmd(t *testing.T) {
	isolateAgentRuntimeConfig(t)

	t.Run("architect role sets all three env vars", func(t *testing.T) {
		cmd := execEnvCmd("architect", "")
		for _, envVar := range []string{"ORO_ROLE=architect", "BD_ACTOR=architect", "GIT_AUTHOR_NAME=architect"} {
			if !strings.Contains(cmd, envVar) {
				t.Errorf("expected execEnvCmd to contain %s, got: %s", envVar, cmd)
			}
		}
		if !strings.HasPrefix(cmd, "exec env") {
			t.Errorf("expected execEnvCmd to start with 'exec env', got: %s", cmd)
		}
	})

	t.Run("manager role sets all three env vars", func(t *testing.T) {
		cmd := execEnvCmd("manager", "")
		for _, envVar := range []string{"ORO_ROLE=manager", "BD_ACTOR=manager", "GIT_AUTHOR_NAME=manager"} {
			if !strings.Contains(cmd, envVar) {
				t.Errorf("expected execEnvCmd to contain %s, got: %s", envVar, cmd)
			}
		}
		if !strings.HasPrefix(cmd, "exec env") {
			t.Errorf("expected execEnvCmd to start with 'exec env', got: %s", cmd)
		}
	})

	t.Run("uses exec env (not export)", func(t *testing.T) {
		cmd := execEnvCmd("worker", "")
		if !strings.Contains(cmd, "exec env") {
			t.Errorf("expected execEnvCmd to use 'exec env', got: %s", cmd)
		}
		if strings.Contains(cmd, "export") {
			t.Errorf("expected execEnvCmd to NOT use 'export', got: %s", cmd)
		}
	})

	t.Run("does not include --session-id", func(t *testing.T) {
		cmd := execEnvCmd("architect", "")
		if strings.Contains(cmd, "--session-id") {
			t.Errorf("expected execEnvCmd to NOT contain --session-id, got: %s", cmd)
		}
		if !strings.Contains(cmd, "claude") {
			t.Errorf("expected execEnvCmd to contain 'claude', got: %s", cmd)
		}
		if strings.Contains(cmd, "claude -p") {
			t.Errorf("expected interactive claude (not 'claude -p'), got: %s", cmd)
		}
	})

	t.Run("does not include --ide", func(t *testing.T) {
		cmd := execEnvCmd("architect", "")
		if strings.Contains(cmd, "--ide") {
			t.Errorf("expected execEnvCmd to NOT contain --ide flag, got: %s", cmd)
		}
	})

	t.Run("uses codex when codex runtime is configured", func(t *testing.T) {
		t.Setenv(agentRuntimeEnvVar, runtimeCodex)

		cmd := execEnvCmd("architect", "")

		if !strings.Contains(cmd, " codex") {
			t.Errorf("expected execEnvCmd to contain 'codex', got: %s", cmd)
		}
		if strings.Contains(cmd, "claude") {
			t.Errorf("expected codex command to avoid claude-specific launch, got: %s", cmd)
		}
		if strings.Contains(cmd, "CLAUDE_CONFIG_DIR=") {
			t.Errorf("expected codex command to avoid CLAUDE_CONFIG_DIR, got: %s", cmd)
		}
	})

	t.Run("preserves daemon env overrides for manager pane", func(t *testing.T) {
		t.Setenv("ORO_HOME", "/tmp/oro-home")
		t.Setenv("ORO_DB_PATH", "/tmp/oro-state.db")
		t.Setenv("ORO_SOCKET_PATH", "/tmp/oro.sock")
		t.Setenv("ORO_PID_PATH", "/tmp/oro.pid")
		t.Setenv("ORO_BEADSOURCE_MODE", "sqlite")

		cmd := execEnvCmd("manager", "envproj")

		for _, envVar := range []string{
			"ORO_HOME=/tmp/oro-home",
			"ORO_DB_PATH=/tmp/oro-state.db",
			"ORO_SOCKET_PATH=/tmp/oro.sock",
			"ORO_PID_PATH=/tmp/oro.pid",
			"ORO_BEADSOURCE_MODE=sqlite",
		} {
			if !strings.Contains(cmd, envVar) {
				t.Errorf("expected manager execEnvCmd to preserve %s, got: %s", envVar, cmd)
			}
		}
	})
}

func TestExecEnvCmdWithProject(t *testing.T) {
	isolateAgentRuntimeConfig(t)

	t.Run("includes add-dir and settings when project is provided", func(t *testing.T) {
		// Set ORO_HOME for deterministic test output
		t.Setenv("ORO_HOME", "/tmp/test-oro-home")

		cmd := execEnvCmd("architect", "myproject")

		if !strings.Contains(cmd, "--add-dir") {
			t.Errorf("expected --add-dir flag when project is provided, got: %s", cmd)
		}
		if !strings.Contains(cmd, "--settings") {
			t.Errorf("expected --settings flag when project is provided, got: %s", cmd)
		}
		if !strings.Contains(cmd, "ORO_PROJECT=myproject") {
			t.Errorf("expected ORO_PROJECT=myproject env var, got: %s", cmd)
		}
		if !strings.Contains(cmd, "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1") {
			t.Errorf("expected CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1 env var, got: %s", cmd)
		}
		// --add-dir should point to ORO_HOME
		if !strings.Contains(cmd, "--add-dir /tmp/test-oro-home") {
			t.Errorf("expected --add-dir to point to ORO_HOME, got: %s", cmd)
		}
		// --settings should point to projects/<project>/settings.json
		expectedSettings := "/tmp/test-oro-home/projects/myproject/settings.json"
		if !strings.Contains(cmd, "--settings "+expectedSettings) {
			t.Errorf("expected --settings %s, got: %s", expectedSettings, cmd)
		}
	})

	t.Run("uses default ORO_HOME when env var is not set", func(t *testing.T) {
		t.Setenv("ORO_HOME", "")

		cmd := execEnvCmd("manager", "testproj")

		if !strings.Contains(cmd, "--add-dir") {
			t.Errorf("expected --add-dir flag, got: %s", cmd)
		}
		if !strings.Contains(cmd, "--settings") {
			t.Errorf("expected --settings flag, got: %s", cmd)
		}
		// Should fall back to ~/.oro
		if !strings.Contains(cmd, "projects/testproj/settings.json") {
			t.Errorf("expected settings path to include projects/testproj/settings.json, got: %s", cmd)
		}
	})

	t.Run("still starts with exec env prefix", func(t *testing.T) {
		t.Setenv("ORO_HOME", "/tmp/test-oro-home")

		cmd := execEnvCmd("architect", "myproject")

		if !strings.HasPrefix(cmd, "exec env") {
			t.Errorf("expected command to start with 'exec env', got: %s", cmd)
		}
	})

	t.Run("includes role env vars when project is provided", func(t *testing.T) {
		t.Setenv("ORO_HOME", "/tmp/test-oro-home")

		cmd := execEnvCmd("architect", "myproject")

		for _, envVar := range []string{"ORO_ROLE=architect", "BD_ACTOR=architect", "GIT_AUTHOR_NAME=architect"} {
			if !strings.Contains(cmd, envVar) {
				t.Errorf("expected %s in command, got: %s", envVar, cmd)
			}
		}
	})

	t.Run("codex runtime keeps project env without claude flags", func(t *testing.T) {
		t.Setenv(agentRuntimeEnvVar, runtimeCodex)
		t.Setenv("ORO_HOME", "/tmp/test-oro-home")

		cmd := execEnvCmd("manager", "myproject")

		if !strings.Contains(cmd, " ORO_PROJECT=myproject ") {
			t.Errorf("expected ORO_PROJECT=myproject env var, got: %s", cmd)
		}
		if !strings.HasSuffix(cmd, " codex --sandbox danger-full-access --ask-for-approval never") {
			t.Errorf("expected full-access autonomous codex command suffix, got: %s", cmd)
		}
		if strings.Contains(cmd, "--add-dir") || strings.Contains(cmd, "--settings") {
			t.Errorf("expected codex command to avoid claude project flags, got: %s", cmd)
		}
		if strings.Contains(cmd, "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD") {
			t.Errorf("expected codex command to avoid claude env vars, got: %s", cmd)
		}
	})
}

func TestPreTrustProject(t *testing.T) {
	t.Run("sets hasTrustDialogAccepted for cwd in role .claude.json", func(t *testing.T) {
		roleDir := t.TempDir()
		cwd := "/Users/test/myproject"

		if err := preTrustProject(roleDir, cwd); err != nil {
			t.Fatalf("preTrustProject failed: %v", err)
		}

		// Read back and verify.
		data, err := os.ReadFile(filepath.Join(roleDir, ".claude.json"))
		if err != nil {
			t.Fatalf("read .claude.json: %v", err)
		}
		// Should contain the path with trust set.
		content := string(data)
		if !strings.Contains(content, cwd) {
			t.Errorf("expected cwd %q in .claude.json, got: %s", cwd, content)
		}
		if !strings.Contains(content, `"hasTrustDialogAccepted":true`) && !strings.Contains(content, `"hasTrustDialogAccepted": true`) {
			t.Errorf("expected hasTrustDialogAccepted:true in .claude.json, got: %s", content)
		}
	})

	t.Run("preserves existing projects in .claude.json", func(t *testing.T) {
		roleDir := t.TempDir()
		// Pre-populate with an existing project.
		existing := `{"projects":{"/old/project":{"hasTrustDialogAccepted":true,"allowedTools":["Read"]}}}`
		if err := os.WriteFile(filepath.Join(roleDir, ".claude.json"), []byte(existing), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		if err := preTrustProject(roleDir, "/new/project"); err != nil {
			t.Fatalf("preTrustProject failed: %v", err)
		}

		data, _ := os.ReadFile(filepath.Join(roleDir, ".claude.json"))
		content := string(data)
		if !strings.Contains(content, "/old/project") {
			t.Error("lost existing project entry")
		}
		if !strings.Contains(content, "/new/project") {
			t.Error("missing new project entry")
		}
	})

	t.Run("idempotent: safe to call twice", func(t *testing.T) {
		roleDir := t.TempDir()
		cwd := "/Users/test/proj"

		_ = preTrustProject(roleDir, cwd)
		err := preTrustProject(roleDir, cwd)
		if err != nil {
			t.Fatalf("second call should succeed: %v", err)
		}
	})

	t.Run("creates .claude.json if missing", func(t *testing.T) {
		roleDir := t.TempDir()
		cwd := "/Users/test/proj"

		if err := preTrustProject(roleDir, cwd); err != nil {
			t.Fatalf("preTrustProject failed: %v", err)
		}

		if _, err := os.Stat(filepath.Join(roleDir, ".claude.json")); err != nil {
			t.Errorf("expected .claude.json to be created: %v", err)
		}
	})

	t.Run("treats JSON null config as empty object", func(t *testing.T) {
		roleDir := t.TempDir()
		cwd := "/Users/test/proj"
		if err := os.WriteFile(filepath.Join(roleDir, ".claude.json"), []byte("null"), 0o600); err != nil {
			t.Fatalf("write .claude.json: %v", err)
		}

		if err := preTrustProject(roleDir, cwd); err != nil {
			t.Fatalf("preTrustProject failed: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(roleDir, ".claude.json"))
		if err != nil {
			t.Fatalf("read .claude.json: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, cwd) {
			t.Fatalf("expected cwd %q in .claude.json, got: %s", cwd, content)
		}
		if !strings.Contains(content, `"hasTrustDialogAccepted":true`) && !strings.Contains(content, `"hasTrustDialogAccepted": true`) {
			t.Fatalf("expected hasTrustDialogAccepted:true in .claude.json, got: %s", content)
		}
	})
}

func TestExecEnvCmdBackwardCompat(t *testing.T) {
	isolateAgentRuntimeConfig(t)

	t.Run("empty project produces same output as before", func(t *testing.T) {
		cmd := execEnvCmd("architect", "")

		// Should end with just "claude" (no --add-dir, no --settings)
		if strings.Contains(cmd, "--add-dir") {
			t.Errorf("expected no --add-dir when project is empty, got: %s", cmd)
		}
		if strings.Contains(cmd, "--settings") {
			t.Errorf("expected no --settings when project is empty, got: %s", cmd)
		}
		if strings.Contains(cmd, "ORO_PROJECT") {
			t.Errorf("expected no ORO_PROJECT when project is empty, got: %s", cmd)
		}
		if strings.Contains(cmd, "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD") {
			t.Errorf("expected no CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD when project is empty, got: %s", cmd)
		}
		if !strings.HasSuffix(cmd, " claude") {
			t.Errorf("expected command to end with ' claude' when project is empty, got: %s", cmd)
		}
	})

	t.Run("all roles work with empty project", func(t *testing.T) {
		for _, role := range []string{"architect", "manager", "worker"} {
			cmd := execEnvCmd(role, "")
			if !strings.HasPrefix(cmd, "exec env") {
				t.Errorf("execEnvCmd(%q, \"\") should start with 'exec env', got: %s", role, cmd)
			}
			if !strings.Contains(cmd, fmt.Sprintf("ORO_ROLE=%s", role)) {
				t.Errorf("execEnvCmd(%q, \"\") should contain ORO_ROLE=%s, got: %s", role, role, cmd)
			}
			if !strings.Contains(cmd, fmt.Sprintf("BD_ACTOR=%s", role)) {
				t.Errorf("execEnvCmd(%q, \"\") should contain BD_ACTOR=%s, got: %s", role, role, cmd)
			}
			if !strings.Contains(cmd, fmt.Sprintf("GIT_AUTHOR_NAME=%s", role)) {
				t.Errorf("execEnvCmd(%q, \"\") should contain GIT_AUTHOR_NAME=%s, got: %s", role, role, cmd)
			}
			if !strings.Contains(cmd, "CLAUDE_CONFIG_DIR=") {
				t.Errorf("execEnvCmd(%q, \"\") should contain CLAUDE_CONFIG_DIR=, got: %s", role, cmd)
			}
			if !strings.HasSuffix(cmd, " claude") {
				t.Errorf("execEnvCmd(%q, \"\") should end with ' claude', got: %s", role, cmd)
			}
		}
	})

	t.Run("codex runtime omits claude-only env for all roles", func(t *testing.T) {
		t.Setenv(agentRuntimeEnvVar, runtimeCodex)

		for _, role := range []string{"architect", "manager", "worker"} {
			cmd := execEnvCmd(role, "")
			if !strings.HasSuffix(cmd, " codex --sandbox danger-full-access --ask-for-approval never") {
				t.Errorf("execEnvCmd(%q, \"\") should end with autonomous codex command, got: %s", role, cmd)
			}
			if strings.Contains(cmd, "CLAUDE_CONFIG_DIR=") {
				t.Errorf("execEnvCmd(%q, \"\") should not include CLAUDE_CONFIG_DIR for codex, got: %s", role, cmd)
			}
		}
	})
}

func TestExecEnvCmdRoleHistory(t *testing.T) {
	isolateAgentRuntimeConfig(t)

	t.Run("different roles get different CLAUDE_CONFIG_DIR", func(t *testing.T) {
		archCmd := execEnvCmd("architect", "")
		mgrCmd := execEnvCmd("manager", "")

		// Extract CLAUDE_CONFIG_DIR values from both commands.
		archDir := extractEnvValue(archCmd, "CLAUDE_CONFIG_DIR")
		mgrDir := extractEnvValue(mgrCmd, "CLAUDE_CONFIG_DIR")

		if archDir == "" {
			t.Fatal("architect command should contain CLAUDE_CONFIG_DIR")
		}
		if mgrDir == "" {
			t.Fatal("manager command should contain CLAUDE_CONFIG_DIR")
		}
		if archDir == mgrDir {
			t.Errorf("architect and manager should have different CLAUDE_CONFIG_DIR, both got: %s", archDir)
		}
	})

	t.Run("CLAUDE_CONFIG_DIR contains role name", func(t *testing.T) {
		cmd := execEnvCmd("architect", "")
		dir := extractEnvValue(cmd, "CLAUDE_CONFIG_DIR")
		if !strings.Contains(dir, "architect") {
			t.Errorf("CLAUDE_CONFIG_DIR should contain role name 'architect', got: %s", dir)
		}
	})

	t.Run("CLAUDE_CONFIG_DIR is under claude config base", func(t *testing.T) {
		cmd := execEnvCmd("architect", "")
		dir := extractEnvValue(cmd, "CLAUDE_CONFIG_DIR")
		if !strings.Contains(dir, ".claude") {
			t.Errorf("CLAUDE_CONFIG_DIR should be under .claude directory, got: %s", dir)
		}
		if !strings.Contains(dir, "roles") {
			t.Errorf("CLAUDE_CONFIG_DIR should be under roles/ subdirectory, got: %s", dir)
		}
	})

	t.Run("CLAUDE_CONFIG_DIR persists across stop/start (deterministic path)", func(t *testing.T) {
		// Call twice and verify same path (deterministic, not random).
		cmd1 := execEnvCmd("architect", "")
		cmd2 := execEnvCmd("architect", "")
		dir1 := extractEnvValue(cmd1, "CLAUDE_CONFIG_DIR")
		dir2 := extractEnvValue(cmd2, "CLAUDE_CONFIG_DIR")
		if dir1 != dir2 {
			t.Errorf("CLAUDE_CONFIG_DIR should be deterministic, got %s and %s", dir1, dir2)
		}
	})

	t.Run("project mode also includes CLAUDE_CONFIG_DIR", func(t *testing.T) {
		t.Setenv("ORO_HOME", "/tmp/test-oro-home")
		cmd := execEnvCmd("architect", "myproject")
		if !strings.Contains(cmd, "CLAUDE_CONFIG_DIR=") {
			t.Errorf("project mode should also include CLAUDE_CONFIG_DIR, got: %s", cmd)
		}
		dir := extractEnvValue(cmd, "CLAUDE_CONFIG_DIR")
		if !strings.Contains(dir, "architect") {
			t.Errorf("project mode CLAUDE_CONFIG_DIR should contain role name, got: %s", dir)
		}
	})
}

// extractEnvValue extracts the value of KEY=VALUE from a command string.
func extractEnvValue(cmd, key string) string {
	prefix := key + "="
	idx := strings.Index(cmd, prefix)
	if idx < 0 {
		return ""
	}
	rest := cmd[idx+len(prefix):]
	// Value ends at next space.
	spaceIdx := strings.Index(rest, " ")
	if spaceIdx < 0 {
		return rest
	}
	return rest[:spaceIdx]
}

func TestSetupRoleConfigDir(t *testing.T) {
	t.Run("creates role directory with symlinks to shared config", func(t *testing.T) {
		// Create a fake ~/.claude structure in a temp dir.
		tmpBase := t.TempDir()
		fakeClaudeDir := filepath.Join(tmpBase, ".claude")
		if err := os.MkdirAll(fakeClaudeDir, 0o750); err != nil {
			t.Fatal(err)
		}

		// Create shared config items that should be symlinked.
		sharedItems := []string{"settings.json", "CLAUDE.md", "__store.db", "projects", "plugins", "rules", "statsig", "cache"}
		for _, item := range sharedItems {
			path := filepath.Join(fakeClaudeDir, item)
			if strings.Contains(item, ".") {
				// File
				if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				// Directory
				if err := os.MkdirAll(path, 0o750); err != nil {
					t.Fatal(err)
				}
			}
		}

		// Create history.jsonl which should NOT be symlinked.
		if err := os.WriteFile(filepath.Join(fakeClaudeDir, "history.jsonl"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}

		roleDir := filepath.Join(fakeClaudeDir, "roles", "architect")
		err := setupRoleConfigDir(fakeClaudeDir, roleDir)
		if err != nil {
			t.Fatalf("setupRoleConfigDir returned error: %v", err)
		}

		// Role directory should exist.
		if _, err := os.Stat(roleDir); os.IsNotExist(err) {
			t.Fatal("role directory should exist after setup")
		}

		// Shared items should be symlinked.
		for _, item := range sharedItems {
			link := filepath.Join(roleDir, item)
			fi, err := os.Lstat(link)
			if err != nil {
				t.Errorf("expected symlink for %s, got error: %v", item, err)
				continue
			}
			if fi.Mode()&os.ModeSymlink == 0 {
				t.Errorf("expected %s to be a symlink, got mode: %s", item, fi.Mode())
			}
			// Verify symlink target points to the original.
			target, err := os.Readlink(link)
			if err != nil {
				t.Errorf("failed to read symlink %s: %v", item, err)
				continue
			}
			expected := filepath.Join(fakeClaudeDir, item)
			if target != expected {
				t.Errorf("symlink %s points to %s, want %s", item, target, expected)
			}
		}

		// history.jsonl should NOT be symlinked (each role gets its own).
		histLink := filepath.Join(roleDir, "history.jsonl")
		_, err = os.Lstat(histLink)
		if err == nil {
			t.Error("history.jsonl should NOT be symlinked into role directory")
		}
	})

	t.Run("idempotent: second call does not error", func(t *testing.T) {
		tmpBase := t.TempDir()
		fakeClaudeDir := filepath.Join(tmpBase, ".claude")
		if err := os.MkdirAll(fakeClaudeDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fakeClaudeDir, "settings.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}

		roleDir := filepath.Join(fakeClaudeDir, "roles", "architect")
		// First call
		if err := setupRoleConfigDir(fakeClaudeDir, roleDir); err != nil {
			t.Fatalf("first setupRoleConfigDir returned error: %v", err)
		}
		// Second call (idempotent)
		if err := setupRoleConfigDir(fakeClaudeDir, roleDir); err != nil {
			t.Fatalf("second setupRoleConfigDir returned error: %v", err)
		}
	})

	t.Run("does not symlink roles directory itself", func(t *testing.T) {
		tmpBase := t.TempDir()
		fakeClaudeDir := filepath.Join(tmpBase, ".claude")
		if err := os.MkdirAll(fakeClaudeDir, 0o750); err != nil {
			t.Fatal(err)
		}
		// Pre-create roles dir (it will exist after first role setup).
		if err := os.MkdirAll(filepath.Join(fakeClaudeDir, "roles"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fakeClaudeDir, "settings.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}

		roleDir := filepath.Join(fakeClaudeDir, "roles", "manager")
		if err := setupRoleConfigDir(fakeClaudeDir, roleDir); err != nil {
			t.Fatalf("setupRoleConfigDir returned error: %v", err)
		}

		// "roles" should NOT be symlinked inside the role dir.
		rolesLink := filepath.Join(roleDir, "roles")
		if _, err := os.Lstat(rolesLink); err == nil {
			t.Error("roles/ directory should NOT be symlinked into role directory (would cause recursion)")
		}
	})
}

func TestRoleConfigDir(t *testing.T) {
	t.Run("returns path under claude base with role name", func(t *testing.T) {
		dir := roleConfigDir("/home/user/.claude", "architect")
		if dir != "/home/user/.claude/roles/architect" {
			t.Errorf("unexpected roleConfigDir: %s", dir)
		}
	})

	t.Run("different roles yield different paths", func(t *testing.T) {
		base := "/home/user/.claude"
		arch := roleConfigDir(base, "architect")
		mgr := roleConfigDir(base, "manager")
		if arch == mgr {
			t.Errorf("different roles should have different paths, both: %s", arch)
		}
	})
}

func TestSendKeysVerified(t *testing.T) {
	t.Run("succeeds on first attempt when text appears in pane", func(t *testing.T) {
		fake := newFakeCmd()
		// capture-pane returns the nudge text on first check
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:architect")] = "some output\nmy nudge text\nprompt"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.SendKeysVerified("oro:architect", "my nudge text", 3*time.Second)
		if err != nil {
			t.Fatalf("SendKeysVerified returned error: %v", err)
		}

		// Should have called send-keys with -l (literal) for the text
		var foundLiteral bool
		for _, call := range fake.calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "send-keys") && strings.Contains(joined, "-l") && strings.Contains(joined, "my nudge text") {
				foundLiteral = true
			}
		}
		if !foundLiteral {
			t.Error("expected send-keys -l with nudge text")
		}
	})

	t.Run("retries with C-u clear when text does not appear", func(t *testing.T) {
		fake := newFakeCmd()
		captureKey := key("tmux", "capture-pane", "-p", "-t", "oro:manager")
		// First capture: text not there; second: still not there; third: text appeared
		fake.seqOut[captureKey] = []string{
			"empty prompt here",
			"empty prompt here",
			"my nudge text visible now",
		}

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.SendKeysVerified("oro:manager", "my nudge text", 5*time.Second)
		if err != nil {
			t.Fatalf("SendKeysVerified returned error: %v", err)
		}

		// Should have sent C-u to clear input before retrying
		var clearCount int
		for _, call := range fake.calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "send-keys") && strings.Contains(joined, "C-u") {
				clearCount++
			}
		}
		if clearCount == 0 {
			t.Error("expected at least one C-u clear before retry")
		}
	})

	t.Run("times out when text never appears", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:architect")] = "nothing here"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.SendKeysVerified("oro:architect", "expected text", 50*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "nudge text") {
			t.Errorf("expected 'nudge text' in error, got: %v", err)
		}
	})

	t.Run("dead pane returns immediately with error containing dead and last output", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{pane_dead}")] = "1"
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:architect")] = "last pane output here"

		start := time.Now()
		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.SendKeysVerified("oro:architect", "expected text", 60*time.Second)
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("expected error for dead pane, got nil")
		}
		if !strings.Contains(err.Error(), "dead") {
			t.Errorf("expected 'dead' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "last pane output here") {
			t.Errorf("expected last pane output in error, got: %v", err)
		}
		if elapsed > 2*time.Second {
			t.Errorf("expected fast return for dead pane, took %v", elapsed)
		}
	})

	t.Run("alive pane retries normally when capture-pane does not show text yet", func(t *testing.T) {
		fake := newFakeCmd()
		// pane_dead returns "0" (alive) — isPaneDead must not block normal retry
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:manager", "#{pane_dead}")] = "0"
		// capture-pane: first attempt shows nothing, second shows text
		fake.seqOut[key("tmux", "capture-pane", "-p", "-t", "oro:manager")] = []string{
			"nothing yet",
			"nudge text visible",
		}

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.SendKeysVerified("oro:manager", "nudge text", 5*time.Second)
		if err != nil {
			t.Fatalf("expected success after retry, got: %v", err)
		}
	})
}

func TestIsPaneDead(t *testing.T) {
	t.Run("returns true when pane_dead is 1", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{pane_dead}")] = "1"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		if !sess.isPaneDead("oro:architect") {
			t.Error("expected isPaneDead to return true when pane_dead=1")
		}
	})

	t.Run("returns false when pane_dead is 0", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{pane_dead}")] = "0"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		if sess.isPaneDead("oro:architect") {
			t.Error("expected isPaneDead to return false when pane_dead=0")
		}
	})

	t.Run("returns false (fail-open) when display-message errors", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{pane_dead}")] = fmt.Errorf("tmux error")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		if sess.isPaneDead("oro:architect") {
			t.Error("expected isPaneDead to return false (fail-open) when display-message errors")
		}
	})
}

func TestAttach(t *testing.T) {
	t.Run("Attach calls tmux attach-session via CmdRunner", func(t *testing.T) {
		fake := newFakeCmd()
		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.Attach()
		if err != nil {
			t.Fatalf("Attach returned error: %v", err)
		}

		// Verify attach-session was called with correct args.
		attachCall := findCall(fake.calls, "attach-session")
		if attachCall == nil {
			t.Fatal("expected tmux attach-session to be called")
		}
		if !callHasArgPair(attachCall, "-t", "oro") {
			t.Error("attach-session should target session 'oro'")
		}
	})

	t.Run("AttachInteractive method exists and returns error on nonexistent session", func(t *testing.T) {
		// Since AttachInteractive bypasses CmdRunner and uses exec.Command directly,
		// we can't easily mock it. We verify it exists by calling it on a nonexistent
		// session and expecting an error.
		sess := &TmuxSession{Name: "nonexistent-test-session-12345"}

		// This should fail because the session doesn't exist.
		err := sess.AttachInteractive()
		if err == nil {
			t.Error("AttachInteractive should return error for nonexistent session")
		}
		// The error should mention tmux attach-session failure.
		if !strings.Contains(err.Error(), "tmux attach-session") {
			t.Errorf("expected error to mention tmux attach-session, got: %v", err)
		}
	})
}

func TestSendKeys_SendsEscapeBeforeEnter(t *testing.T) {
	fake := newFakeCmd()
	// wakeIfDetached: session is attached, so no resize signal is needed.
	fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{session_attached}")] = "1"

	sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
	err := sess.SendKeys("oro:architect", "hello world")
	if err != nil {
		t.Fatalf("SendKeys returned error: %v", err)
	}

	// Find the Escape and Enter send-keys calls, excluding the literal text call.
	escapeIdx, enterIdx := -1, -1
	for i, call := range fake.calls {
		if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
			lastArg := call[len(call)-1]
			if lastArg == "Escape" {
				escapeIdx = i
			}
			if lastArg == "Enter" && enterIdx == -1 {
				enterIdx = i
			}
		}
	}

	if escapeIdx == -1 {
		t.Fatal("expected Escape send-keys call, got none")
	}
	if enterIdx == -1 {
		t.Fatal("expected Enter send-keys call, got none")
	}
	if escapeIdx >= enterIdx {
		t.Errorf("Escape (call %d) should come before Enter (call %d)", escapeIdx, enterIdx)
	}
}

func TestSendKeys_WakesAfterEscapeInDetachedSession(t *testing.T) {
	fake := newFakeCmd()
	// Session is detached, so wakeIfDetached should send SIGWINCH.
	fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{session_attached}")] = "0"
	fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{pane_pid}")] = "12345"

	sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
	err := sess.SendKeys("oro:architect", "hello world")
	if err != nil {
		t.Fatalf("SendKeys returned error: %v", err)
	}

	// Find Escape, Enter, and any wake signals between them.
	escapeIdx, enterIdx := -1, -1
	for i, call := range fake.calls {
		if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
			lastArg := call[len(call)-1]
			if lastArg == "Escape" {
				escapeIdx = i
			}
			if lastArg == "Enter" && enterIdx == -1 {
				enterIdx = i
			}
		}
	}

	if escapeIdx == -1 {
		t.Fatal("expected Escape send-keys call")
	}
	if enterIdx == -1 {
		t.Fatal("expected Enter send-keys call")
	}

	var wakesBetween int
	for i := escapeIdx + 1; i < enterIdx; i++ {
		if len(fake.calls[i]) >= 2 && fake.calls[i][0] == "kill" && fake.calls[i][1] == "-WINCH" {
			wakesBetween++
		}
	}
	if wakesBetween == 0 {
		t.Error("expected kill -WINCH between Escape and Enter in detached session")
	}
}

func TestWakeIfDetached_SendsSIGWINCH(t *testing.T) {
	t.Run("detached session sends kill -WINCH to pane PID", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{session_attached}")] = "0"
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{pane_pid}")] = "12345"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		sess.wakeIfDetached("oro:architect")

		var killCalls [][]string
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "kill" && call[1] == "-WINCH" {
				killCalls = append(killCalls, call)
			}
		}
		if len(killCalls) != 1 {
			t.Fatalf("expected 1 kill -WINCH call, got %d: %v", len(killCalls), killCalls)
		}
		if killCalls[0][2] != "12345" {
			t.Errorf("expected PID 12345, got %s", killCalls[0][2])
		}
	})

	t.Run("attached session skips wake", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{session_attached}")] = "1"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		sess.wakeIfDetached("oro:architect")

		for _, call := range fake.calls {
			if len(call) >= 1 && call[0] == "kill" {
				t.Error("should not call kill when session is attached")
			}
		}
	})
}

func TestTmuxStatusBarColor(t *testing.T) {
	t.Run("Create sets single static manager color (no window-switch hook)", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "manager nudge")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create()
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		sess.WaitBeacon()

		// Verify set-option was called to set status-style with manager color (orange).
		var foundStatusStyle bool
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-option" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "status-style") && strings.Contains(joined, "colour208") {
					foundStatusStyle = true
				}
			}
		}
		if !foundStatusStyle {
			t.Error("expected set-option for status-style with manager colour208 (orange)")
		}

		// Must NOT set after-select-window hook (single static color, no switching).
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-hook" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "after-select-window") {
					t.Errorf("must not set after-select-window hook (single static color), got: %s", joined)
				}
			}
		}
	})
}

func TestPaneDiedHooks(t *testing.T) {
	t.Run("RegisterPaneDiedHooks registers hook for manager pane only", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "show-hooks", "-g")] = "pane-died\n"
		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}

		err := sess.RegisterPaneDiedHooks()
		if err != nil {
			t.Fatalf("RegisterPaneDiedHooks returned error: %v", err)
		}

		var managerHookSet bool
		for _, call := range fake.calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "architect") {
				t.Errorf("RegisterPaneDiedHooks must not reference architect pane, got call: %v", call)
			}
			if len(call) >= 6 && call[0] == "tmux" && call[1] == "set-hook" &&
				strings.Contains(joined, "oro:manager") && strings.Contains(joined, "pane-died") {
				managerHookSet = true
			}
		}
		if !managerHookSet {
			t.Error("expected set-hook to be called for manager pane")
		}
	})

	t.Run("RegisterPaneDiedHooks skips unsupported pane-died hook", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "show-hooks", "-g")] = "after-new-session\n"
		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}

		err := sess.RegisterPaneDiedHooks()
		if err != nil {
			t.Fatalf("RegisterPaneDiedHooks returned error: %v", err)
		}

		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-hook" {
				t.Fatalf("unsupported pane-died hook should not call set-hook, got %v", call)
			}
		}
	})

	t.Run("buildPaneDiedHook generates valid hook command", func(t *testing.T) {
		hook := buildPaneDiedHook("oro", "")

		// Hook must use run-shell and respawn-pane for crash recovery.
		if !strings.Contains(hook, "run-shell") {
			t.Errorf("hook should use run-shell, got: %s", hook)
		}
		if !strings.Contains(hook, "respawn-pane") {
			t.Errorf("hook should use respawn-pane for crash recovery, got: %s", hook)
		}
		if !strings.Contains(hook, "PANE_RESPAWNED") {
			t.Errorf("hook should mention PANE_RESPAWNED, got: %s", hook)
		}
		// Single-window: logs via UDS, not paste-buffer to a surviving pane.
		if strings.Contains(hook, "paste-buffer") {
			t.Errorf("manager hook must not use paste-buffer (no surviving pane), got: %s", hook)
		}
		if !strings.Contains(hook, "ORO_SOCKET_PATH") {
			t.Errorf("manager hook should log via dispatcher UDS using ORO_SOCKET_PATH, got: %s", hook)
		}
	})

	t.Run("manager hook logs via UDS, not send-keys to peer pane", func(t *testing.T) {
		hook := buildPaneDiedHook("oro", "")
		// No surviving pane in single-window layout.
		if strings.Contains(hook, "send-keys") {
			t.Errorf("manager hook must not use send-keys to a peer pane, got: %s", hook)
		}
		if !strings.Contains(hook, "ORO_SOCKET_PATH") {
			t.Errorf("manager hook should log via dispatcher UDS, got: %s", hook)
		}
	})

	t.Run("sanitizeForTmuxHook removes newlines", func(t *testing.T) {
		input := "line1\nline2\rline3"
		output := sanitizeForTmuxHook(input)

		if strings.Contains(output, "\n") || strings.Contains(output, "\r") {
			t.Errorf("sanitizeForTmuxHook should remove newlines, got: %q", output)
		}
		if !strings.Contains(output, "line1") || !strings.Contains(output, "line2") || !strings.Contains(output, "line3") {
			t.Errorf("sanitizeForTmuxHook should preserve content, got: %q", output)
		}
	})

	t.Run("escapeForShell wraps with single quotes and escapes internal quotes", func(t *testing.T) {
		input := "hello 'world'"
		output := escapeForShell(input)

		// Should start and end with single quotes
		if !strings.HasPrefix(output, "'") || !strings.HasSuffix(output, "'") {
			t.Errorf("escapeForShell should wrap with single quotes, got: %q", output)
		}

		// Should escape internal single quotes as '\''
		if !strings.Contains(output, "'\\''") {
			t.Errorf("escapeForShell should escape internal quotes as '\\'\\'', got: %q", output)
		}
	})

	t.Run("CleanupPaneDiedHooks unregisters hook for manager pane only", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "has-session", "-t", "oro")] = ""
		fake.output[key("tmux", "show-hooks", "-g")] = "pane-died\n"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}

		err := sess.CleanupPaneDiedHooks()
		if err != nil {
			t.Fatalf("CleanupPaneDiedHooks returned error: %v", err)
		}

		var managerHookUnset bool
		for _, call := range fake.calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "architect") {
				t.Errorf("CleanupPaneDiedHooks must not reference architect pane, got call: %v", call)
			}
			if len(call) >= 6 && call[0] == "tmux" && call[1] == "set-hook" && call[2] == "-u" &&
				strings.Contains(joined, "oro:manager") && strings.Contains(joined, "pane-died") {
				managerHookUnset = true
			}
		}
		if !managerHookUnset {
			t.Error("expected set-hook -u to be called for manager pane")
		}
	})

	t.Run("CleanupPaneDiedHooks skips cleanup when session does not exist", func(t *testing.T) {
		fake := newFakeCmd()
		// Session does not exist
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}

		err := sess.CleanupPaneDiedHooks()
		if err != nil {
			t.Fatalf("CleanupPaneDiedHooks should not fail when session doesn't exist, got: %v", err)
		}

		// Should not have called set-hook
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-hook" {
				t.Error("should not call set-hook when session doesn't exist")
			}
		}
	})

	t.Run("pane-died hook escapes special characters", func(t *testing.T) {
		hook := buildPaneDiedHook("test-session", "")

		if !strings.Contains(hook, "run-shell") {
			t.Errorf("hook should use run-shell, got: %s", hook)
		}
		if strings.Count(hook, "'") < 2 {
			t.Errorf("hook should have proper quoting, got: %s", hook)
		}
	})
}

func TestBuildPaneDiedHookContent(t *testing.T) {
	t.Run("hook message format matches escalation pattern", func(t *testing.T) {
		hook := buildPaneDiedHook("oro", "")

		if !strings.Contains(hook, "[ORO-DISPATCH] PANE_RESPAWNED") {
			t.Errorf("hook message should follow escalation format with PANE_RESPAWNED, got: %s", hook)
		}
	})

	t.Run("hook for manager references manager as dying role", func(t *testing.T) {
		hook := buildPaneDiedHook("oro", "")
		if !strings.Contains(hook, "manager pane crashed and was respawned") {
			t.Errorf("manager hook should mention manager pane crashed and was respawned, got: %s", hook)
		}
	})

	t.Run("does not double-quote escapeForShell output", func(t *testing.T) {
		hook := buildPaneDiedHook("oro", "")

		// escapeForShell wraps in single quotes — must not produce double single-quotes.
		if strings.Contains(hook, "''") {
			t.Errorf("hook should not contain double single-quotes (''), got: %s", hook)
		}

		// The log message must be single-quoted for shell safety.
		if !strings.Contains(hook, "echo '") {
			t.Errorf("hook should have single-quoted message after echo, got: %s", hook)
		}
	})

	t.Run("manager hook logs via UDS not paste-buffer", func(t *testing.T) {
		hook := buildPaneDiedHook("oro", "")

		// Single-window: no paste-buffer to a peer pane.
		if strings.Contains(hook, "paste-buffer") {
			t.Errorf("manager hook must not use paste-buffer (no surviving pane), got: %s", hook)
		}
		if !strings.Contains(hook, "ORO_SOCKET_PATH") {
			t.Errorf("manager hook should log via dispatcher UDS using ORO_SOCKET_PATH, got: %s", hook)
		}
	})
}

func TestBuildPaneDiedHook_SkipsWhenRestartingFlag(t *testing.T) {
	t.Run("hook contains restarting flag guard before respawn-pane for manager", func(t *testing.T) {
		hook := buildPaneDiedHook("oro", "")

		expectedGuard := "test \\! -f ~/.oro/panes/manager/restarting &&"
		if !strings.Contains(hook, expectedGuard) {
			t.Errorf("hook should contain restarting flag guard, expected %q in %s", expectedGuard, hook)
		}
	})

	t.Run("guard appears before respawn-pane command", func(t *testing.T) {
		hook := buildPaneDiedHook("oro", "")

		guardIdx := strings.Index(hook, "test \\! -f ~/.oro/panes/manager/restarting &&")
		respawnIdx := strings.Index(hook, "tmux respawn-pane")

		if guardIdx == -1 {
			t.Errorf("guard not found in hook: %s", hook)
		}
		if respawnIdx == -1 {
			t.Errorf("respawn-pane not found in hook: %s", hook)
		}
		if guardIdx > respawnIdx {
			t.Errorf("guard should appear before respawn-pane, guard at %d, respawn-pane at %d, hook: %s", guardIdx, respawnIdx, hook)
		}
	})
}

func TestSanitizeForTmuxHook_StripsMeta(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "strips semicolon", input: "hello; world", want: "hello world"},
		{name: "strips ampersand", input: "hello & world", want: "hello  world"},
		{name: "strips dollar", input: "$HOME", want: "HOME"},
		{name: "strips backtick", input: "`cmd`", want: "cmd"},
		{name: "strips open paren", input: "foo(bar", want: "foobar"},
		{name: "strips close paren", input: "foo)bar", want: "foobar"},
		{name: "strips all metacharacters", input: ";$&`()", want: ""},
		{name: "replaces newline with space", input: "hello\nworld", want: "hello world"},
		{name: "replaces carriage return with space", input: "hello\rworld", want: "hello world"},
		{name: "passes clean message unchanged", input: "hello world", want: "hello world"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeForTmuxHook(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeForTmuxHook(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestCreate_CleansUpOnPartialFailure(t *testing.T) {
	t.Run("when configureSessionOptions fails, kill-session is called", func(t *testing.T) {
		fake := newFakeCmd()
		// has-session returns error (no session exists)
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		// new-session succeeds (default: no error)
		// set-option fails during configureSessionOptions
		fake.errs[key("tmux", "set-option", "-t", "oro", "status-style", "bg=colour208,fg=black")] = fmt.Errorf("set-option failed")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second}
		err := sess.Create()
		if err == nil {
			t.Fatal("expected error from Create when configureSessionOptions fails, got nil")
		}
		sess.WaitBeacon()

		// Verify kill-session was called to clean up the half-created session.
		var killedSession bool
		for _, call := range fake.calls {
			if len(call) >= 4 && call[0] == "tmux" && call[1] == "kill-session" && call[2] == "-t" && call[3] == "oro" {
				killedSession = true
				break
			}
		}
		if !killedSession {
			t.Error("expected kill-session to be called for cleanup after configureSessionOptions failure")
		}
	})
}

func TestNudgeSerialization(t *testing.T) {
	t.Run("concurrent SendKeys to same target are serialized", func(t *testing.T) {
		fake := newFakeCmd()
		// wakeIfDetached: session is attached (no wake needed)
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{session_attached}")] = "1"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}

		// Track execution order with a channel
		order := make(chan string, 10)
		done := make(chan struct{})

		go func() {
			defer func() { done <- struct{}{} }()
			// This should acquire the lock first (started first)
			err := sess.SendKeys("oro:architect", "first message")
			if err != nil {
				t.Errorf("first SendKeys failed: %v", err)
			}
			order <- "first-done"
		}()

		go func() {
			defer func() { done <- struct{}{} }()
			// This should wait for first to complete
			err := sess.SendKeys("oro:architect", "second message")
			if err != nil {
				t.Errorf("second SendKeys failed: %v", err)
			}
			order <- "second-done"
		}()

		// Wait for both to complete
		<-done
		<-done

		// Both should complete without error (serialization worked)
		// The key assertion is that no send-keys calls are interleaved
		// We verify this by checking that all calls for "first message"
		// appear before all calls for "second message", or vice versa
		calls := fake.getCalls()
		var firstIdx, secondIdx int
		firstIdx, secondIdx = -1, -1
		for i, call := range calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "first message") && firstIdx == -1 {
				firstIdx = i
			}
			if strings.Contains(joined, "second message") && secondIdx == -1 {
				secondIdx = i
			}
		}
		// Both messages should have been sent
		if firstIdx == -1 || secondIdx == -1 {
			t.Fatal("expected both messages to be sent")
		}
		// They should not be at the same index (serialized, not interleaved)
		if firstIdx == secondIdx {
			t.Error("messages should be serialized, not sent simultaneously")
		}
	})

	t.Run("SendKeys to different targets are independent", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{session_attached}")] = "1"
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:manager", "#{session_attached}")] = "1"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}

		done := make(chan struct{}, 2)

		go func() {
			_ = sess.SendKeys("oro:architect", "arch msg")
			done <- struct{}{}
		}()
		go func() {
			_ = sess.SendKeys("oro:manager", "mgr msg")
			done <- struct{}{}
		}()

		<-done
		<-done

		// Both should complete — different targets use different locks
		calls := fake.getCalls()
		var foundArch, foundMgr bool
		for _, call := range calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "arch msg") {
				foundArch = true
			}
			if strings.Contains(joined, "mgr msg") {
				foundMgr = true
			}
		}
		if !foundArch || !foundMgr {
			t.Error("expected both messages to be sent to different targets")
		}
	})

	t.Run("getSessionNudgeLock returns same mutex for same target", func(t *testing.T) {
		lock1 := getSessionNudgeLock("test-target")
		lock2 := getSessionNudgeLock("test-target")
		if lock1 != lock2 {
			t.Error("expected same mutex instance for same target")
		}
	})

	t.Run("getSessionNudgeLock returns different mutex for different targets", func(t *testing.T) {
		lock1 := getSessionNudgeLock("target-a")
		lock2 := getSessionNudgeLock("target-b")
		if lock1 == lock2 {
			t.Error("expected different mutex instances for different targets")
		}
	})
}

func TestKillWithProcessCleanup(t *testing.T) {
	t.Run("Kill gets manager pane PID and calls kill-session", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:manager", "#{pane_pid}")] = "12346"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.Kill()
		if err != nil {
			t.Fatalf("Kill returned error: %v", err)
		}

		var gotMgrPid bool
		for _, call := range fake.calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "architect") {
				t.Errorf("Kill must not reference architect pane, got call: %v", call)
			}
			if strings.Contains(joined, "display-message") && strings.Contains(joined, "pane_pid") &&
				strings.Contains(joined, "oro:manager") {
				gotMgrPid = true
			}
		}
		if !gotMgrPid {
			t.Error("expected display-message for manager pane PID")
		}

		var killedSession bool
		for _, call := range fake.calls {
			if len(call) >= 3 && call[0] == "tmux" && call[1] == "kill-session" {
				killedSession = true
			}
		}
		if !killedSession {
			t.Error("expected kill-session to be called after process cleanup")
		}
	})

	t.Run("Kill succeeds even when pane PID lookup fails", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "display-message", "-p", "-t", "oro:manager", "#{pane_pid}")] = fmt.Errorf("no pane")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.Kill()
		if err != nil {
			t.Fatalf("Kill should succeed even when PID lookup fails: %v", err)
		}

		var killedSession bool
		for _, call := range fake.calls {
			if len(call) >= 3 && call[0] == "tmux" && call[1] == "kill-session" {
				killedSession = true
			}
		}
		if !killedSession {
			t.Error("expected kill-session even when PID lookup fails")
		}
	})
}

func TestStatusBarLabels(t *testing.T) {
	t.Run("Create sets status-left with window name", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "manager nudge")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create()
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		sess.WaitBeacon()

		var foundStatusLeft, foundStatusLeftLen, foundStatusRight bool
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-option" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "status-left-length") && strings.Contains(joined, "20") {
					foundStatusLeftLen = true
				} else if strings.Contains(joined, "status-left") && strings.Contains(joined, "window_name") {
					foundStatusLeft = true
				}
				if strings.Contains(joined, "status-right") && strings.Contains(joined, "oro") {
					foundStatusRight = true
				}
			}
		}
		if !foundStatusLeft {
			t.Error("expected set-option status-left containing #{window_name}")
		}
		if !foundStatusLeftLen {
			t.Error("expected set-option status-left-length 20")
		}
		if !foundStatusRight {
			t.Error("expected set-option status-right containing 'oro'")
		}
	})
}

func TestScrollbackConfiguration(t *testing.T) {
	t.Run("Create sets history-limit and does not set alternate-screen off", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "manager nudge")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create()
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		sess.WaitBeacon()

		var foundAlternateScreen, foundHistoryLimit bool
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-option" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "alternate-screen") && strings.Contains(joined, "off") {
					foundAlternateScreen = true
				}
				if strings.Contains(joined, "history-limit") && strings.Contains(joined, "50000") {
					foundHistoryLimit = true
				}
			}
		}
		if foundAlternateScreen {
			t.Error("alternate-screen off should NOT be set (breaks Ink TUI color rendering)")
		}
		if !foundHistoryLimit {
			t.Error("expected set-option history-limit 50000 to be called during Create")
		}
	})
}

func TestMouseModeEnabled(t *testing.T) {
	t.Run("Create enables mouse mode and clipboard", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "manager nudge")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create()
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		sess.WaitBeacon()

		var foundMouse, foundClipboard bool
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-option" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "mouse") && strings.Contains(joined, "on") && !strings.Contains(joined, "set-clipboard") {
					foundMouse = true
				}
				if strings.Contains(joined, "set-clipboard") && strings.Contains(joined, "on") {
					foundClipboard = true
				}
			}
		}
		if !foundMouse {
			t.Error("expected set-option mouse on to be called during Create")
		}
		if !foundClipboard {
			t.Error("expected set-option set-clipboard on to be called during Create")
		}
	})
}

func TestRemainOnExit(t *testing.T) {
	t.Run("Create sets remain-on-exit=on", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "manager nudge")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create()
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		sess.WaitBeacon()

		// Verify remain-on-exit was set
		var found bool
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-option" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "remain-on-exit") && strings.Contains(joined, "on") {
					found = true
				}
			}
		}
		if !found {
			t.Error("expected set-option remain-on-exit on to be called during Create")
		}
	})

	t.Run("pane-died hook calls respawn-pane", func(t *testing.T) {
		hook := buildPaneDiedHook("oro", "")
		if !strings.Contains(hook, "respawn-pane") {
			t.Errorf("pane-died hook should use respawn-pane for crash recovery, got: %s", hook)
		}
		if !strings.Contains(hook, "oro:manager") {
			t.Errorf("hook should respawn the dying manager pane, got: %s", hook)
		}
	})

	t.Run("RespawnPane calls tmux respawn-pane", func(t *testing.T) {
		fake := newFakeCmd()
		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.RespawnPane("oro:architect", "exec env ORO_ROLE=architect claude")
		if err != nil {
			t.Fatalf("RespawnPane returned error: %v", err)
		}

		var found bool
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "respawn-pane" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "-k") && strings.Contains(joined, "oro:architect") {
					found = true
				}
			}
		}
		if !found {
			t.Error("expected tmux respawn-pane -k to be called")
		}
	})
}

func TestStatusBarShowsQuitHint(t *testing.T) {
	fake := newFakeCmd()
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	stubPaneReady(fake, "oro", "manager nudge")

	sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
	err := sess.Create()
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	sess.WaitBeacon()

	// Check that status-right contains navigation hints instead of just 'oro | %H:%M'
	var foundStatusRight bool
	for _, call := range fake.calls {
		if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-option" {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "status-right") {
				// Should contain navigation hints: ctrl-b, switch, detach, quit
				if strings.Contains(joined, "ctrl-b") &&
					strings.Contains(joined, "switch") &&
					strings.Contains(joined, "detach") &&
					strings.Contains(joined, "quit") {
					foundStatusRight = true
				}
			}
		}
	}
	if !foundStatusRight {
		t.Error("expected status-right to contain navigation hints (ctrl-b, switch, detach, quit)")
	}
}

// TestCreateAsyncBeacon verifies that VerifyBeaconReceived does not block
// Create from returning.
func TestCreateAsyncBeacon(t *testing.T) {
	t.Run("VerifyBeaconReceived does not block Create return", func(t *testing.T) {
		// If beacon verification is async, Create should return quickly even
		// when the beacon never appears (beacon times out after BeaconTimeout).
		// Set a large BeaconTimeout and verify Create returns before it expires.

		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "mgr nudge")

		// VerifyBeaconReceived: capture-pane for manager never shows "oro task status".
		managerCapture := key("tmux", "capture-pane", "-p", "-t", "oro:manager")
		fake.seqOut[managerCapture] = []string{
			"Welcome\n❯ \nstatus bar",          // WaitForPrompt
			"Welcome\n❯ mgr nudge\nstatus bar", // SendKeysVerified
			"no beacon here",                   // VerifyBeaconReceived — never found
		}

		const beaconTimeout = 500 * time.Millisecond
		sess := &TmuxSession{
			Name:          "oro",
			Runner:        fake,
			Sleeper:       noopSleep,
			ReadyTimeout:  time.Second,
			BeaconTimeout: beaconTimeout,
		}

		start := time.Now()
		err := sess.Create()
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("Create should not fail on beacon timeout (warning only), got: %v", err)
		}
		sess.WaitBeacon()

		// Create must return well before the beacon timeout expires.
		// Allow 80% of the beacon timeout as the upper bound.
		maxAllowed := beaconTimeout * 4 / 5
		if elapsed >= maxAllowed {
			t.Errorf("Create blocked waiting for beacon: elapsed %v ≥ maxAllowed %v (beacon verification should be async)", elapsed, maxAllowed)
		}
	})
}

// TestCreateSingleSignature is a compile-time assertion that TmuxSession.Create
// has no parameters and returns error.
func TestCreateSingleSignature(t *testing.T) {
	var _ interface {
		Create() error
	} = (*TmuxSession)(nil)
}

func TestIsPreCollapseLayout(t *testing.T) {
	t.Run("returns true when architect window present", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "has-session", "-t", "oro")] = ""
		fake.output[key("tmux", "list-windows", "-t", "oro", "-F", "#{window_name}")] = "architect\nmanager"

		sess := &TmuxSession{Name: "oro", Runner: fake}
		got, err := sess.isPreCollapseLayout()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got {
			t.Error("expected true for session with architect window")
		}
	})

	t.Run("returns false when only manager window present", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "has-session", "-t", "oro")] = ""
		fake.output[key("tmux", "list-windows", "-t", "oro", "-F", "#{window_name}")] = "manager"

		sess := &TmuxSession{Name: "oro", Runner: fake}
		got, err := sess.isPreCollapseLayout()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Error("expected false for session with only manager window")
		}
	})

	t.Run("returns false nil for nonexistent session", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")

		sess := &TmuxSession{Name: "oro", Runner: fake}
		got, err := sess.isPreCollapseLayout()
		if err != nil {
			t.Fatalf("expected nil error for nonexistent session, got: %v", err)
		}
		if got {
			t.Error("expected false for nonexistent session")
		}
	})

	t.Run("returns false error when list-windows fails", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "has-session", "-t", "oro")] = ""
		fake.errs[key("tmux", "list-windows", "-t", "oro", "-F", "#{window_name}")] = fmt.Errorf("tmux error")

		sess := &TmuxSession{Name: "oro", Runner: fake}
		got, err := sess.isPreCollapseLayout()
		if err == nil {
			t.Fatal("expected error when list-windows fails")
		}
		if got {
			t.Error("expected false when list-windows fails")
		}
	})
}

func TestCreateMigratesPreCollapseSession(t *testing.T) {
	t.Run("kills and recreates when session has architect+manager windows", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "has-session", "-t", "oro")] = ""
		fake.output[key("tmux", "list-windows", "-t", "oro", "-F", "#{window_name}")] = "architect\nmanager"
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:manager", "#{session_attached}")] = "1"
		fake.seqOut[key("tmux", "capture-pane", "-p", "-t", "oro:manager")] = []string{
			"Welcome\n❯ \nstatus bar",
			"Welcome\n❯ nudge\nstatus bar",
			"oro task status\nrunning\n",
		}

		var buf strings.Builder
		sess := &TmuxSession{
			Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep,
			ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond,
			Output: &buf,
		}
		err := sess.Create()
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		sess.WaitBeacon()

		var killed bool
		for _, call := range fake.getCalls() {
			if len(call) >= 2 && call[1] == "kill-session" {
				killed = true
				break
			}
		}
		if !killed {
			t.Error("expected kill-session for pre-collapse session")
		}
		if findCall(fake.getCalls(), "new-session") == nil {
			t.Error("expected new-session after killing pre-collapse session")
		}
		if !strings.Contains(buf.String(), "migrat") {
			t.Errorf("expected migration message in output, got: %q", buf.String())
		}
	})

	t.Run("migrates legacy manager-only session", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "has-session", "-t", "oro")] = ""
		fake.output[key("tmux", "list-windows", "-t", "oro", "-F", "#{window_name}")] = "manager"
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:manager", "#{pane_current_command}")] = "claude"

		var buf strings.Builder
		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, Output: &buf}
		err := sess.Create()
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		var killed, recreated bool
		for _, call := range fake.getCalls() {
			if len(call) >= 2 && call[1] == "kill-session" {
				killed = true
			}
			if len(call) >= 2 && call[1] == "new-session" {
				recreated = true
			}
		}
		if !killed {
			t.Error("expected kill-session for legacy manager-only session")
		}
		if !recreated {
			t.Error("expected new-session after killing legacy manager-only session")
		}
		if !strings.Contains(buf.String(), "migrat") {
			t.Errorf("expected migration message for legacy manager-only session, got: %q", buf.String())
		}
	})

	t.Run("creates new session when no existing session", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		fake.seqOut[key("tmux", "capture-pane", "-p", "-t", "oro:manager")] = []string{
			"Welcome\n❯ \nstatus bar",
			"Welcome\n❯ nudge\nstatus bar",
			"oro task status\nrunning\n",
		}

		var buf strings.Builder
		sess := &TmuxSession{
			Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep,
			ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond,
			Output: &buf,
		}
		err := sess.Create()
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		sess.WaitBeacon()

		if findCall(fake.getCalls(), "new-session") == nil {
			t.Error("expected new-session for nonexistent session")
		}
		for _, call := range fake.getCalls() {
			if len(call) >= 2 && call[1] == "kill-session" {
				t.Error("should not kill session when no existing session")
			}
		}
		if strings.Contains(buf.String(), "migrat") {
			t.Errorf("should not print migration message when creating fresh session, got: %q", buf.String())
		}
	})
}

func TestVerifyBeaconReceivedUsesTaskTerminology(t *testing.T) {
	t.Run("beacon succeeds when pane contains oro task status", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:manager")] = "Working...\noro task status\nrunning\n"
		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		if err := sess.VerifyBeaconReceived("oro:manager", "oro task status", 50*time.Millisecond); err != nil {
			t.Errorf("VerifyBeaconReceived with 'oro task status' should succeed: %v", err)
		}
	})

	t.Run("beacon times out when pane only contains legacy oro bead status", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:manager")] = "Working...\noro bead status\nrunning\n"
		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		if err := sess.VerifyBeaconReceived("oro:manager", "oro task status", 10*time.Millisecond); err == nil {
			t.Error("VerifyBeaconReceived should time out when pane only shows legacy 'oro bead status'")
		}
	})
}

func TestSingleWindowLayout(t *testing.T) {
	t.Run("session has exactly one managerless window", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "nudge")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create()
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		sess.WaitBeacon()

		newSessionCall := findCall(fake.calls, "new-session")
		if newSessionCall == nil {
			t.Fatal("expected tmux new-session to be called")
		}
		if !callHasArgPair(newSessionCall, "-n", defaultTmuxWindowName) {
			t.Errorf("session window must be named %q", defaultTmuxWindowName)
		}
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "new-window" {
				t.Error("new-window must not be called (single-window session)")
			}
		}
	})

	t.Run("isHealthy checks manager pane only", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:manager", "#{pane_current_command}")] = "claude"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		if !sess.isHealthy() {
			t.Error("isHealthy should return true when manager pane has claude")
		}
		for _, call := range fake.calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "architect") {
				t.Errorf("isHealthy must not reference architect pane, got call: %v", call)
			}
		}
	})

	t.Run("Kill walks manager pane process tree only", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:manager", "#{pane_pid}")] = "42"

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		_ = sess.Kill()

		for _, call := range fake.calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "architect") {
				t.Errorf("Kill must not reference architect pane, got call: %v", call)
			}
		}
	})

	t.Run("AttachInteractive does not call select-window", func(t *testing.T) {
		fake := newFakeCmd()
		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake}
		_ = sess.AttachInteractive()

		for _, call := range fake.getCalls() {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "select-window" {
				t.Error("AttachInteractive must not call select-window (single-window session)")
			}
		}
	})

	t.Run("RegisterPaneDiedHooks registers manager pane only", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "show-hooks", "-g")] = "pane-died\n"
		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep}
		err := sess.RegisterPaneDiedHooks()
		if err != nil {
			t.Fatalf("RegisterPaneDiedHooks returned error: %v", err)
		}

		var managerHookSet bool
		for _, call := range fake.calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "architect") {
				t.Errorf("RegisterPaneDiedHooks must not reference architect pane, got call: %v", call)
			}
			if strings.Contains(joined, "set-hook") && strings.Contains(joined, "oro:manager") && strings.Contains(joined, "pane-died") {
				managerHookSet = true
			}
		}
		if !managerHookSet {
			t.Error("expected set-hook for manager pane")
		}
	})

	t.Run("status bar uses single static color with no window-switch hook", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "nudge")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create()
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		sess.WaitBeacon()

		var foundStatusStyle bool
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-option" {
				if strings.Contains(strings.Join(call, " "), "status-style") {
					foundStatusStyle = true
				}
			}
		}
		if !foundStatusStyle {
			t.Error("expected set-option status-style to be called")
		}

		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-hook" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "after-select-window") {
					t.Error("must not set after-select-window hook (single static color, no switching)")
				}
			}
		}
	})
}
