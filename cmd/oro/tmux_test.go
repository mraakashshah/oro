package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// noopSleep is a no-op sleeper for tests to avoid real delays.
func noopSleep(time.Duration) {}

// fakeCmd records exec calls for testing without real tmux.
// It supports both single-value and sequential (multi-value) outputs per key.
type fakeCmd struct {
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

// stubPaneReady sets up the fake so WaitForPrompt sees the ❯ prompt indicator
// (i.e., Claude's TUI is ready) and SendKeysVerified sees the nudge text in
// capture-pane output. With exec-env, no WaitForCommand stubs are needed since
// Claude IS the initial process. capture-pane is called sequentially: first by
// WaitForPrompt, then by SendKeysVerified, so we use seqOut to return ❯ first,
// then nudge text.
func stubPaneReady(fake *fakeCmd, sessionName, architectNudge, managerNudge string) {
	archCapture := key("tmux", "capture-pane", "-p", "-t", sessionName+":architect")
	mgrCapture := key("tmux", "capture-pane", "-p", "-t", sessionName+":manager")
	fake.seqOut[archCapture] = []string{
		"Welcome\n❯ \nstatus bar",                       // WaitForPrompt
		"Welcome\n❯ " + architectNudge + "\nstatus bar", // SendKeysVerified
	}
	fake.seqOut[mgrCapture] = []string{
		"Welcome\n❯ \nstatus bar",                     // WaitForPrompt
		"Welcome\n❯ " + managerNudge + "\nstatus bar", // SendKeysVerified
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
	t.Run("Create builds session with two windows", func(t *testing.T) {
		fake := newFakeCmd()
		// has-session returns error (session does not exist)
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		// list-panes returns two panes after creation
		fake.output[key("tmux", "list-panes", "-t", "oro", "-F", "#{pane_index}")] = "0\n1\n"
		stubPaneReady(fake, "oro", "architect beacon", "manager beacon")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect beacon", "manager beacon")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify: new-session was called with -d, -s oro, and -n architect
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
		if !callHasArgPair(newSessionCall, "-n", "architect") {
			t.Error("new-session should name the first window 'architect'")
		}

		// Verify: new-window was called to create manager window
		newWindowCall := findCall(fake.calls, "new-window")
		if newWindowCall == nil {
			t.Fatal("expected tmux new-window to be called")
		}
		if !callHasArgPair(newWindowCall, "-t", "oro") {
			t.Error("new-window should target session 'oro'")
		}
		if !callHasArgPair(newWindowCall, "-n", "manager") {
			t.Error("new-window should name the window 'manager'")
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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.Create("architect beacon", "manager beacon")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify: new-session was NOT called
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "new-session" {
				t.Error("should not create new session when one already exists")
			}
		}
	})

	t.Run("Kill destroys the session", func(t *testing.T) {
		fake := newFakeCmd()
		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		if !sess.Exists() {
			t.Error("expected Exists to return true when has-session succeeds")
		}
	})

	t.Run("Exists returns false when session is not running", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		if sess.Exists() {
			t.Error("expected Exists to return false when has-session fails")
		}
	})

	t.Run("ListPanes returns pane indices", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "list-panes", "-t", "oro", "-F", "#{pane_index}")] = "0\n1\n"

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		_, err := sess.ListPanes()
		if err == nil {
			t.Error("expected error when list-panes fails")
		}
	})

	t.Run("Create sends commands to windows", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		fake.output[key("tmux", "list-panes", "-t", "oro", "-F", "#{pane_index}")] = "0\n1\n"
		stubPaneReady(fake, "oro", "architect nudge text", "manager nudge text")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect nudge text", "manager nudge text")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify send-keys was called for both windows (nudge only, no launch):
		// - architect: nudge literal + Escape + Enter (3)
		// - manager: nudge literal + Escape + Enter (3)
		// That's 6 send-keys calls total (2×3 nudge). No launch send-keys with exec-env.
		sendKeysCount := 0
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				sendKeysCount++
			}
		}
		if sendKeysCount < 6 {
			t.Errorf("expected at least 6 send-keys calls (2×3 nudge), got %d", sendKeysCount)
		}
	})

	t.Run("Create launches interactive claude with role env vars via exec env", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "architect nudge", "manager nudge")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect nudge", "manager nudge")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Architect: verify new-session command has exec env with role env vars.
		newSessionCall := findCall(fake.calls, "new-session")
		if newSessionCall == nil {
			t.Fatal("expected tmux new-session to be called")
		}
		archCmd := newSessionCall[len(newSessionCall)-1]
		for _, envVar := range []string{"ORO_ROLE=architect", "BD_ACTOR=architect", "GIT_AUTHOR_NAME=architect"} {
			if !strings.Contains(archCmd, envVar) {
				t.Errorf("new-session command should set %s, got: %s", envVar, archCmd)
			}
		}
		if !strings.Contains(archCmd, "claude") {
			t.Errorf("new-session command should run claude, got: %s", archCmd)
		}
		if strings.Contains(archCmd, "claude -p") {
			t.Errorf("should use interactive claude, not 'claude -p', got: %s", archCmd)
		}

		// Manager: verify new-window command has exec env with role env vars.
		newWindowCall := findCall(fake.calls, "new-window")
		if newWindowCall == nil {
			t.Fatal("expected tmux new-window to be called")
		}
		mgrCmd := newWindowCall[len(newWindowCall)-1]
		for _, envVar := range []string{"ORO_ROLE=manager", "BD_ACTOR=manager", "GIT_AUTHOR_NAME=manager"} {
			if !strings.Contains(mgrCmd, envVar) {
				t.Errorf("new-window command should set %s, got: %s", envVar, mgrCmd)
			}
		}
		if !strings.Contains(mgrCmd, "claude") {
			t.Errorf("new-window command should run claude, got: %s", mgrCmd)
		}
		if strings.Contains(mgrCmd, "claude -p") {
			t.Errorf("should use interactive claude, not 'claude -p', got: %s", mgrCmd)
		}
	})

	t.Run("Create injects nudges via SendKeys (literal + wake + debounce + Enter)", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "architect nudge text here", "manager nudge text here")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect nudge text here", "manager nudge text here")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Collect send-keys calls per window.
		var architectCalls, managerCalls [][]string
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "oro:architect") {
					architectCalls = append(architectCalls, call)
				}
				if strings.Contains(joined, "oro:manager") {
					managerCalls = append(managerCalls, call)
				}
			}
		}

		// Architect: literal -l (0) + Escape (1) + Enter (2) = 3 send-keys calls (no launch).
		if len(architectCalls) < 3 {
			t.Fatalf("expected at least 3 send-keys to architect window, got %d", len(architectCalls))
		}
		archNudge := strings.Join(architectCalls[0], " ")
		if !strings.Contains(archNudge, "-l") {
			t.Errorf("architect nudge should use literal mode (-l), got: %s", archNudge)
		}
		if !strings.Contains(archNudge, "architect nudge text here") {
			t.Errorf("architect nudge should contain nudge text, got: %s", archNudge)
		}
		archEscape := strings.Join(architectCalls[1], " ")
		if !strings.Contains(archEscape, "Escape") {
			t.Errorf("architect nudge should send Escape before Enter, got: %s", archEscape)
		}
		archEnter := strings.Join(architectCalls[2], " ")
		if !strings.Contains(archEnter, "Enter") {
			t.Errorf("architect nudge should send Enter separately, got: %s", archEnter)
		}

		// Manager: literal -l (0) + Escape (1) + Enter (2) = 3 send-keys calls (no launch).
		if len(managerCalls) < 3 {
			t.Fatalf("expected at least 3 send-keys to manager window, got %d", len(managerCalls))
		}
		mgrNudge := strings.Join(managerCalls[0], " ")
		if !strings.Contains(mgrNudge, "-l") {
			t.Errorf("manager nudge should use literal mode (-l), got: %s", mgrNudge)
		}
		if !strings.Contains(mgrNudge, "manager nudge text here") {
			t.Errorf("manager nudge should contain nudge text, got: %s", mgrNudge)
		}
		mgrEscape := strings.Join(managerCalls[1], " ")
		if !strings.Contains(mgrEscape, "Escape") {
			t.Errorf("manager nudge should send Escape before Enter, got: %s", mgrEscape)
		}
		mgrEnter := strings.Join(managerCalls[2], " ")
		if !strings.Contains(mgrEnter, "Enter") {
			t.Errorf("manager nudge should send Enter separately, got: %s", mgrEnter)
		}
	})

	t.Run("stop command kills tmux session", func(t *testing.T) {
		fake := newFakeCmd()
		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}

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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
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

	t.Run("Create polls prompt readiness before injecting beacons", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "architect beacon", "manager beacon")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect beacon", "manager beacon")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify capture-pane was called for both windows (WaitForPrompt).
		var checkedArchitect, checkedManager bool
		for _, call := range fake.calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "capture-pane") && strings.Contains(joined, "oro:architect") {
				checkedArchitect = true
			}
			if strings.Contains(joined, "capture-pane") && strings.Contains(joined, "oro:manager") {
				checkedManager = true
			}
		}
		if !checkedArchitect {
			t.Error("expected capture-pane to be called for architect window")
		}
		if !checkedManager {
			t.Error("expected capture-pane to be called for manager window")
		}
	})

	t.Run("Create times out when Claude prompt never appears", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		// capture-pane never shows prompt indicator — Claude never becomes ready.
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:architect")] = "loading..."

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: 50 * time.Millisecond}
		err := sess.Create("architect beacon", "manager beacon")
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "prompt") {
			t.Errorf("expected 'prompt' in error, got: %v", err)
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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second}
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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: 5 * time.Second}
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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: 50 * time.Millisecond}
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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: 5 * time.Second}
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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: 5 * time.Second}
		err := sess.WaitForCommand("oro:architect")
		if err != nil {
			t.Fatalf("WaitForCommand returned error: %v", err)
		}
	})

	t.Run("uses default timeout when ReadyTimeout is zero", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[displayKey("oro:architect")] = "claude"

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.WaitForCommand("oro:architect")
		if err != nil {
			t.Fatalf("WaitForCommand returned error: %v", err)
		}
	})
}

func TestVerifyBeaconReceived(t *testing.T) {
	t.Run("returns nil when indicator found immediately", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:manager")] = "some output\nbd stats\nmore output"

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.VerifyBeaconReceived("oro:manager", "bd stats", time.Second)
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
			"running bd stats\noutput here",
		}

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.VerifyBeaconReceived("oro:manager", "bd stats", 5*time.Second)
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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.VerifyBeaconReceived("oro:manager", "bd stats", 50*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "oro:manager") {
			t.Errorf("expected window target in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "bd stats") {
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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
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
			"bd stats\nsome output",
		}

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.VerifyBeaconReceived("oro:manager", "bd stats", 5*time.Second)
		if err != nil {
			t.Fatalf("VerifyBeaconReceived returned error: %v", err)
		}
	})
}

func TestCreateVerifiesBeaconAfterInjection(t *testing.T) {
	t.Run("Create calls VerifyBeaconReceived for manager window after injection", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "architect nudge", "manager nudge")

		// capture-pane is used by WaitForPrompt (needs ❯), SendKeysVerified
		// (needs nudge text), and VerifyBeaconReceived (needs "bd stats").
		managerCapture := key("tmux", "capture-pane", "-p", "-t", "oro:manager")
		fake.seqOut[managerCapture] = []string{
			"Welcome\n❯ \nstatus bar",              // WaitForPrompt
			"Welcome\n❯ manager nudge\nstatus bar", // SendKeysVerified
			"bd stats\n❯ output visible\n",         // VerifyBeaconReceived
		}

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: time.Second}
		err := sess.Create("architect nudge", "manager nudge")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify that capture-pane was called for manager (WaitForPrompt + SendKeysVerified + VerifyBeacon).
		managerCaptureCount := 0
		for _, call := range fake.calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "capture-pane") && strings.Contains(joined, "oro:manager") {
				managerCaptureCount++
			}
		}
		if managerCaptureCount < 3 {
			t.Errorf("expected at least 3 capture-pane calls for manager (WaitForPrompt + SendKeysVerified + VerifyBeacon), got %d", managerCaptureCount)
		}
	})

	t.Run("Create does not fail when nudge verification times out (warning only)", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "architect nudge", "manager nudge")

		// capture-pane is used by WaitForPrompt (needs ❯), SendKeysVerified
		// (needs nudge text), then VerifyBeaconReceived (beacon not found → timeout).
		managerCapture := key("tmux", "capture-pane", "-p", "-t", "oro:manager")
		fake.seqOut[managerCapture] = []string{
			"Welcome\n❯ \nstatus bar",              // WaitForPrompt succeeds
			"Welcome\n❯ manager nudge\nstatus bar", // SendKeysVerified succeeds
			"no beacon output",                     // VerifyBeaconReceived times out
		}

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		// Create should succeed even if nudge verification fails — it's a warning
		err := sess.Create("architect nudge", "manager nudge")
		if err != nil {
			t.Fatalf("Create should not fail on nudge verification timeout, got: %v", err)
		}
	})
}

func TestExecEnvCmd(t *testing.T) {
	t.Run("architect role sets all three env vars", func(t *testing.T) {
		cmd := execEnvCmd("architect")
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
		cmd := execEnvCmd("manager")
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
		cmd := execEnvCmd("worker")
		if !strings.Contains(cmd, "exec env") {
			t.Errorf("expected execEnvCmd to use 'exec env', got: %s", cmd)
		}
		if strings.Contains(cmd, "export") {
			t.Errorf("expected execEnvCmd to NOT use 'export', got: %s", cmd)
		}
	})

	t.Run("includes --session-id for history isolation", func(t *testing.T) {
		cmd := execEnvCmd("architect")
		if !strings.Contains(cmd, "--session-id") {
			t.Errorf("expected execEnvCmd to contain --session-id, got: %s", cmd)
		}
		if !strings.Contains(cmd, "claude") {
			t.Errorf("expected execEnvCmd to contain 'claude', got: %s", cmd)
		}
		if strings.Contains(cmd, "claude -p") {
			t.Errorf("expected interactive claude (not 'claude -p'), got: %s", cmd)
		}
	})

	t.Run("includes --ide flag for IDE integration", func(t *testing.T) {
		cmd := execEnvCmd("architect")
		if !strings.Contains(cmd, "--ide") {
			t.Errorf("expected execEnvCmd to contain --ide flag for IDE integration, got: %s", cmd)
		}
	})
}

func TestSendKeysVerified(t *testing.T) {
	t.Run("succeeds on first attempt when text appears in pane", func(t *testing.T) {
		fake := newFakeCmd()
		// capture-pane returns the nudge text on first check
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:architect")] = "some output\nmy nudge text\nprompt"

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.SendKeysVerified("oro:architect", "expected text", 50*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "nudge text") {
			t.Errorf("expected 'nudge text' in error, got: %v", err)
		}
	})
}

func TestAttach(t *testing.T) {
	t.Run("Attach calls tmux attach-session via CmdRunner", func(t *testing.T) {
		fake := newFakeCmd()
		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
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

func TestCreate_KillsZombieSession(t *testing.T) {
	t.Run("kills and recreates session when both panes show shell", func(t *testing.T) {
		fake := newFakeCmd()

		// has-session succeeds (session exists).
		fake.output[key("tmux", "has-session", "-t", "oro")] = ""

		// isHealthy checks pane_current_command: returns shell (zombie).
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{pane_current_command}")] = "zsh"

		// wakeIfDetached session_attached check.
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{session_attached}")] = "1"
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:manager", "#{session_attached}")] = "1"

		// capture-pane for WaitForPrompt + SendKeysVerified + VerifyBeacon.
		fake.seqOut[key("tmux", "capture-pane", "-p", "-t", "oro:architect")] = []string{
			"Welcome\n❯ \nstatus bar",                // WaitForPrompt
			"Welcome\n❯ architect nudge\nstatus bar", // SendKeysVerified
		}
		fake.seqOut[key("tmux", "capture-pane", "-p", "-t", "oro:manager")] = []string{
			"Welcome\n❯ \nstatus bar",              // WaitForPrompt
			"Welcome\n❯ manager nudge\nstatus bar", // SendKeysVerified
			"bd stats\n❯ output\n",                 // VerifyBeaconReceived
		}

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: time.Second}
		err := sess.Create("architect nudge", "manager nudge")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify kill-session was called.
		var killedSession bool
		for _, call := range fake.calls {
			if len(call) >= 3 && call[1] == "kill-session" && call[3] == "oro" {
				killedSession = true
				break
			}
		}
		if !killedSession {
			t.Error("expected kill-session to be called for zombie session")
		}
	})

	t.Run("keeps session when Claude is running in panes", func(t *testing.T) {
		fake := newFakeCmd()
		// Session exists.
		fake.output[key("tmux", "has-session", "-t", "oro")] = ""

		// Both panes show Claude (healthy session).
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{pane_current_command}")] = "claude"
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:manager", "#{pane_current_command}")] = "claude"

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.Create("architect nudge", "manager nudge")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify kill-session was NOT called.
		for _, call := range fake.calls {
			if len(call) >= 2 && call[1] == "kill-session" {
				t.Error("should NOT kill-session when Claude is running")
			}
		}

		// Verify new-session was NOT called (reused existing).
		for _, call := range fake.calls {
			if len(call) >= 2 && call[1] == "new-session" {
				t.Error("should NOT create new session when Claude is running")
			}
		}
	})
}

func TestSendKeys_SendsEscapeBeforeEnter(t *testing.T) {
	fake := newFakeCmd()
	// wakeIfDetached: session is attached (no resize needed)
	fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{session_attached}")] = "1"

	sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
	err := sess.SendKeys("oro:architect", "hello world")
	if err != nil {
		t.Fatalf("SendKeys returned error: %v", err)
	}

	// Find the Escape and Enter send-keys calls (not the literal text one)
	var escapeIdx, enterIdx int
	escapeIdx, enterIdx = -1, -1
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
	// Session is detached — wakeIfDetached should send SIGWINCH.
	fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{session_attached}")] = "0"
	fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{pane_pid}")] = "12345"

	sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
	err := sess.SendKeys("oro:architect", "hello world")
	if err != nil {
		t.Fatalf("SendKeys returned error: %v", err)
	}

	// Find indices of: Escape send-keys, the kill -WINCH calls after Escape
	// (wake after Escape), and Enter send-keys.
	var escapeIdx, enterIdx int
	escapeIdx, enterIdx = -1, -1
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

	// There must be a kill -WINCH (SIGWINCH wake) between Escape and Enter
	// so Ink processes Escape before Enter arrives.
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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		sess.wakeIfDetached("oro:architect")

		// Should call kill -WINCH 12345.
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

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		sess.wakeIfDetached("oro:architect")

		for _, call := range fake.calls {
			if len(call) >= 1 && call[0] == "kill" {
				t.Error("should not call kill when session is attached")
			}
		}
	})
}

func TestTmuxStatusBarColor(t *testing.T) {
	t.Run("Create sets initial status-style to architect color", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "architect nudge", "manager nudge")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect nudge", "manager nudge")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify set-option was called to set status-style with architect color (green).
		var foundStatusStyle bool
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-option" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "status-style") && strings.Contains(joined, "colour46") {
					foundStatusStyle = true
				}
			}
		}
		if !foundStatusStyle {
			t.Error("expected set-option for status-style with architect colour46 (green)")
		}
	})

	t.Run("Create sets hook to change status-style when switching windows", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "architect nudge", "manager nudge")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect nudge", "manager nudge")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Collect set-hook calls and find the after-select-window hook.
		var hookArgs string
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-hook" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "after-select-window") {
					hookArgs = joined
					break
				}
			}
		}
		if hookArgs == "" {
			t.Fatal("expected set-hook with after-select-window for status bar color switching")
		}
		if !strings.Contains(hookArgs, "colour46") {
			t.Errorf("hook should reference architect colour46, got: %s", hookArgs)
		}
		if !strings.Contains(hookArgs, "colour208") {
			t.Errorf("hook should reference manager colour208, got: %s", hookArgs)
		}
	})
}

func TestCreate_ExecEnvPattern(t *testing.T) {
	// stubExecEnvReady stubs only WaitForPrompt + SendKeysVerified for exec-env
	// pattern (no WaitForCommand needed since Claude IS the initial process).
	stubExecEnvReady := func(fake *fakeCmd, sessionName, architectNudge, managerNudge string) {
		archCapture := key("tmux", "capture-pane", "-p", "-t", sessionName+":architect")
		mgrCapture := key("tmux", "capture-pane", "-p", "-t", sessionName+":manager")
		fake.seqOut[archCapture] = []string{
			"Welcome\n❯ \nstatus bar",                       // WaitForPrompt
			"Welcome\n❯ " + architectNudge + "\nstatus bar", // SendKeysVerified
		}
		fake.seqOut[mgrCapture] = []string{
			"Welcome\n❯ \nstatus bar",                     // WaitForPrompt
			"Welcome\n❯ " + managerNudge + "\nstatus bar", // SendKeysVerified
		}
	}

	t.Run("new-session receives exec env command as last arg", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubExecEnvReady(fake, "oro", "architect nudge", "manager nudge")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect nudge", "manager nudge")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify new-session has exec env command as last arg.
		newSessionCall := findCall(fake.calls, "new-session")
		if newSessionCall == nil {
			t.Fatal("expected tmux new-session to be called")
		}
		lastArg := newSessionCall[len(newSessionCall)-1]
		if !strings.Contains(lastArg, "exec env") {
			t.Errorf("new-session last arg should contain 'exec env', got: %s", lastArg)
		}
		if !strings.Contains(lastArg, "ORO_ROLE=architect") {
			t.Errorf("new-session command should set ORO_ROLE=architect, got: %s", lastArg)
		}
		if !strings.Contains(lastArg, "claude") {
			t.Errorf("new-session command should launch claude, got: %s", lastArg)
		}

		// Verify new-window also has exec env command as last arg.
		newWindowCall := findCall(fake.calls, "new-window")
		if newWindowCall == nil {
			t.Fatal("expected tmux new-window to be called")
		}
		lastArg = newWindowCall[len(newWindowCall)-1]
		if !strings.Contains(lastArg, "exec env") {
			t.Errorf("new-window last arg should contain 'exec env', got: %s", lastArg)
		}
		if !strings.Contains(lastArg, "ORO_ROLE=manager") {
			t.Errorf("new-window command should set ORO_ROLE=manager, got: %s", lastArg)
		}
	})

	t.Run("no send-keys for launch (exec env eliminates shell phase)", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubExecEnvReady(fake, "oro", "arch nudge", "mgr nudge")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("arch nudge", "mgr nudge")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// No send-keys should contain export or execEnvCmd patterns.
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "export ORO_ROLE") {
					t.Errorf("should not send-keys with shell export command (exec env eliminates this), got: %s", joined)
				}
			}
		}
	})

	t.Run("no WaitForCommand polling (Claude is initial process)", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubExecEnvReady(fake, "oro", "arch nudge", "mgr nudge")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("arch nudge", "mgr nudge")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// No display-message calls for pane_current_command during Create
		// (isHealthy check is only on pre-existing sessions, not fresh creation).
		for _, call := range fake.calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "display-message") && strings.Contains(joined, "pane_current_command") {
				t.Errorf("should not poll pane_current_command during fresh Create with exec env, got: %s", joined)
			}
		}
	})
}

func TestPaneDiedHooks(t *testing.T) {
	t.Run("RegisterPaneDiedHooks registers hooks for both panes", func(t *testing.T) {
		fake := newFakeCmd()
		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}

		err := sess.RegisterPaneDiedHooks()
		if err != nil {
			t.Fatalf("RegisterPaneDiedHooks returned error: %v", err)
		}

		// Verify set-hook was called for architect pane
		var architectHookSet bool
		var managerHookSet bool
		for _, call := range fake.calls {
			if len(call) >= 6 && call[0] == "tmux" && call[1] == "set-hook" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "oro:architect") && strings.Contains(joined, "pane-died") {
					architectHookSet = true
				}
				if strings.Contains(joined, "oro:manager") && strings.Contains(joined, "pane-died") {
					managerHookSet = true
				}
			}
		}
		if !architectHookSet {
			t.Error("expected set-hook to be called for architect pane")
		}
		if !managerHookSet {
			t.Error("expected set-hook to be called for manager pane")
		}
	})

	t.Run("buildPaneDiedHook generates valid hook command", func(t *testing.T) {
		hook := buildPaneDiedHook("architect", "oro")

		// Hook should contain run-shell, set-buffer, paste-buffer, and reference to manager pane
		if !strings.Contains(hook, "run-shell") {
			t.Errorf("hook should use run-shell, got: %s", hook)
		}
		if !strings.Contains(hook, "set-buffer") {
			t.Errorf("hook should use set-buffer, got: %s", hook)
		}
		if !strings.Contains(hook, "paste-buffer") {
			t.Errorf("hook should use paste-buffer, got: %s", hook)
		}
		if !strings.Contains(hook, "oro:manager") {
			t.Errorf("hook should reference surviving manager pane, got: %s", hook)
		}
		if !strings.Contains(hook, "PANE_DIED") {
			t.Errorf("hook should mention PANE_DIED escalation, got: %s", hook)
		}
		if !strings.Contains(hook, "architect") {
			t.Errorf("hook should mention dying architect role, got: %s", hook)
		}
	})

	t.Run("buildPaneDiedHook references correct surviving pane", func(t *testing.T) {
		// When architect dies, message goes to manager
		architectHook := buildPaneDiedHook("architect", "oro")
		if !strings.Contains(architectHook, "oro:manager") {
			t.Errorf("architect hook should send to manager, got: %s", architectHook)
		}

		// When manager dies, message goes to architect
		managerHook := buildPaneDiedHook("manager", "oro")
		if !strings.Contains(managerHook, "oro:architect") {
			t.Errorf("manager hook should send to architect, got: %s", managerHook)
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

	t.Run("CleanupPaneDiedHooks unregisters hooks for both panes", func(t *testing.T) {
		fake := newFakeCmd()
		// Session exists
		fake.output[key("tmux", "has-session", "-t", "oro")] = ""

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}

		err := sess.CleanupPaneDiedHooks()
		if err != nil {
			t.Fatalf("CleanupPaneDiedHooks returned error: %v", err)
		}

		// Verify set-hook -u was called for both panes
		var architectHookUnset bool
		var managerHookUnset bool
		for _, call := range fake.calls {
			if len(call) >= 6 && call[0] == "tmux" && call[1] == "set-hook" && call[2] == "-u" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "oro:architect") && strings.Contains(joined, "pane-died") {
					architectHookUnset = true
				}
				if strings.Contains(joined, "oro:manager") && strings.Contains(joined, "pane-died") {
					managerHookUnset = true
				}
			}
		}
		if !architectHookUnset {
			t.Error("expected set-hook -u to be called for architect pane")
		}
		if !managerHookUnset {
			t.Error("expected set-hook -u to be called for manager pane")
		}
	})

	t.Run("CleanupPaneDiedHooks skips cleanup when session does not exist", func(t *testing.T) {
		fake := newFakeCmd()
		// Session does not exist
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}

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
		// Test hook generation with special characters that need escaping
		hook := buildPaneDiedHook("architect", "test-session")

		// Should be a valid shell command (starts with run-shell)
		if !strings.Contains(hook, "run-shell") {
			t.Errorf("hook should use run-shell, got: %s", hook)
		}

		// Should properly escape quotes for shell safety
		if strings.Count(hook, "'") < 2 {
			t.Errorf("hook should have proper quoting, got: %s", hook)
		}
	})
}

func TestBuildPaneDiedHookContent(t *testing.T) {
	t.Run("hook message format matches escalation pattern", func(t *testing.T) {
		hook := buildPaneDiedHook("architect", "oro")

		// Message should start with [ORO-DISPATCH] and include PANE_DIED
		if !strings.Contains(hook, "[ORO-DISPATCH] PANE_DIED") {
			t.Errorf("hook message should follow escalation format, got: %s", hook)
		}
	})

	t.Run("hook for architect references architect as dying role", func(t *testing.T) {
		hook := buildPaneDiedHook("architect", "oro")
		if !strings.Contains(hook, "architect pane crashed") {
			t.Errorf("architect hook should mention architect pane, got: %s", hook)
		}
	})

	t.Run("hook for manager references manager as dying role", func(t *testing.T) {
		hook := buildPaneDiedHook("manager", "oro")
		if !strings.Contains(hook, "manager pane crashed") {
			t.Errorf("manager hook should mention manager pane, got: %s", hook)
		}
	})

	t.Run("does not double-quote escapeForShell output", func(t *testing.T) {
		// escapeForShell already wraps in single quotes ('...')
		// The format string must not add another layer like '%s' which produces ''...''
		hook := buildPaneDiedHook("architect", "oro")

		// Check for the problematic pattern: ''...''. In shell, '' is an empty string,
		// so this would leave the content unquoted and [ORO-DISPATCH] becomes a glob.
		// We should have single-quoted content, not double single-quoted.

		// The hook should contain the message wrapped in single quotes exactly once.
		// escapeForShell produces 'content', so the format string should use %s not '%s'.
		// Look for the pattern: set-buffer -b oro-pane-died 'content'
		// NOT: set-buffer -b oro-pane-died ''content''

		if strings.Contains(hook, "''") {
			t.Errorf("hook should not contain double single-quotes (''), got: %s", hook)
		}

		// Verify that the escaped message appears exactly once between single quotes
		// The message should be properly quoted as a single shell argument
		if !strings.Contains(hook, "set-buffer -b oro-pane-died '") {
			t.Errorf("hook should have single-quoted message after set-buffer, got: %s", hook)
		}
	})

	t.Run("paste-buffer uses -d flag consistent with TmuxEscalator pattern", func(t *testing.T) {
		// TmuxEscalator.Escalate() uses paste-buffer with -d flag to delete the buffer after paste
		// buildPaneDiedHook should follow the same pattern for consistency
		hook := buildPaneDiedHook("architect", "oro")

		// The hook should contain: paste-buffer -b oro-pane-died -t <pane> -d
		if !strings.Contains(hook, "paste-buffer -b oro-pane-died") {
			t.Errorf("hook should use paste-buffer with named buffer, got: %s", hook)
		}

		// Verify -d flag is present
		if !strings.Contains(hook, "paste-buffer -b oro-pane-died -t oro:manager -d") &&
			!strings.Contains(hook, "paste-buffer -b oro-pane-died -t oro:architect -d") {
			t.Errorf("hook should use -d flag on paste-buffer (consistent with TmuxEscalator), got: %s", hook)
		}
	})
}
