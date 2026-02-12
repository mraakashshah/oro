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

// stubPaneReady sets up the fake so WaitForCommand sees "claude" as the
// foreground process in both windows (i.e., Claude has started), and
// WaitForPrompt sees the ❯ prompt indicator (i.e., Claude's TUI is ready),
// and SendKeysVerified sees the nudge text in capture-pane output.
// capture-pane is called sequentially: first by WaitForPrompt, then by
// SendKeysVerified, so we use seqOut to return ❯ first, then nudge text.
func stubPaneReady(fake *fakeCmd, sessionName, architectNudge, managerNudge string) {
	fake.output[key("tmux", "display-message", "-p", "-t", sessionName+":architect", "#{pane_current_command}")] = "claude"
	fake.output[key("tmux", "display-message", "-p", "-t", sessionName+":manager", "#{pane_current_command}")] = "claude"
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

		// Verify: set-option was called for architect window color
		var foundArchitectColor bool
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-option" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "oro:architect") && strings.Contains(joined, "colour46") {
					foundArchitectColor = true
				}
			}
		}
		if !foundArchitectColor {
			t.Error("expected set-option for architect window with colour46")
		}

		// Verify: set-option was called for manager window color
		var foundManagerColor bool
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "set-option" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "oro:manager") && strings.Contains(joined, "colour208") {
					foundManagerColor = true
				}
			}
		}
		if !foundManagerColor {
			t.Error("expected set-option for manager window with colour208")
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

		// Verify send-keys was called for both windows:
		// - architect: launch (1), then nudge literal + Enter (2)
		// - manager: launch (1), then nudge literal + Enter (2)
		// That's 6 send-keys calls total (2 launch + 2×2 nudge).
		sendKeysCount := 0
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				sendKeysCount++
			}
		}
		if sendKeysCount < 6 {
			t.Errorf("expected at least 6 send-keys calls (2 launch + 2×2 nudge), got %d", sendKeysCount)
		}
	})

	t.Run("Create launches interactive claude with role env vars", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "architect nudge", "manager nudge")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect nudge", "manager nudge")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Collect all send-keys calls targeting each window.
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

		// Architect window: first send-keys should launch claude with all role env vars
		if len(architectCalls) < 2 {
			t.Fatalf("expected at least 2 send-keys to architect window, got %d", len(architectCalls))
		}
		archLaunch := strings.Join(architectCalls[0], " ")
		for _, envVar := range []string{"ORO_ROLE=architect", "BD_ACTOR=architect", "GIT_AUTHOR_NAME=architect"} {
			if !strings.Contains(archLaunch, envVar) {
				t.Errorf("architect window launch should set %s, got: %s", envVar, archLaunch)
			}
		}
		if !strings.Contains(archLaunch, "claude") {
			t.Errorf("architect window launch should run claude, got: %s", archLaunch)
		}
		// Must NOT use claude -p
		if strings.Contains(archLaunch, "claude -p") {
			t.Errorf("architect window should use interactive claude, not 'claude -p', got: %s", archLaunch)
		}

		// Manager window: first send-keys should launch claude with all role env vars
		if len(managerCalls) < 2 {
			t.Fatalf("expected at least 2 send-keys to manager window, got %d", len(managerCalls))
		}
		mgrLaunch := strings.Join(managerCalls[0], " ")
		for _, envVar := range []string{"ORO_ROLE=manager", "BD_ACTOR=manager", "GIT_AUTHOR_NAME=manager"} {
			if !strings.Contains(mgrLaunch, envVar) {
				t.Errorf("manager window launch should set %s, got: %s", envVar, mgrLaunch)
			}
		}
		if !strings.Contains(mgrLaunch, "claude") {
			t.Errorf("manager window launch should run claude, got: %s", mgrLaunch)
		}
		// Must NOT use claude -p
		if strings.Contains(mgrLaunch, "claude -p") {
			t.Errorf("manager window should use interactive claude, not 'claude -p', got: %s", mgrLaunch)
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

		// Architect: launch (1) + literal -l (2) + Enter (3) = 3 send-keys calls.
		if len(architectCalls) < 3 {
			t.Fatalf("expected at least 3 send-keys to architect window, got %d", len(architectCalls))
		}
		archNudge := strings.Join(architectCalls[1], " ")
		if !strings.Contains(archNudge, "-l") {
			t.Errorf("architect nudge should use literal mode (-l), got: %s", archNudge)
		}
		if !strings.Contains(archNudge, "architect nudge text here") {
			t.Errorf("architect nudge should contain nudge text, got: %s", archNudge)
		}
		archEnter := strings.Join(architectCalls[2], " ")
		if !strings.Contains(archEnter, "Enter") {
			t.Errorf("architect nudge should send Enter separately, got: %s", archEnter)
		}

		// Manager: launch (1) + literal -l (2) + Enter (3) = 3 send-keys calls.
		if len(managerCalls) < 3 {
			t.Fatalf("expected at least 3 send-keys to manager window, got %d", len(managerCalls))
		}
		mgrNudge := strings.Join(managerCalls[1], " ")
		if !strings.Contains(mgrNudge, "-l") {
			t.Errorf("manager nudge should use literal mode (-l), got: %s", mgrNudge)
		}
		if !strings.Contains(mgrNudge, "manager nudge text here") {
			t.Errorf("manager nudge should contain nudge text, got: %s", mgrNudge)
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

	t.Run("Create polls window readiness before injecting beacons", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "architect beacon", "manager beacon")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect beacon", "manager beacon")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify display-message was called to check pane_current_command for both windows.
		var checkedArchitect, checkedManager bool
		for _, call := range fake.calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "display-message") && strings.Contains(joined, "oro:architect") {
				checkedArchitect = true
			}
			if strings.Contains(joined, "display-message") && strings.Contains(joined, "oro:manager") {
				checkedManager = true
			}
		}
		if !checkedArchitect {
			t.Error("expected display-message to be called for architect window")
		}
		if !checkedManager {
			t.Error("expected display-message to be called for manager window")
		}
	})

	t.Run("Create times out when Claude never becomes ready", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		// display-message returns shell name — Claude never starts
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{pane_current_command}")] = "zsh"
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:manager", "#{pane_current_command}")] = "zsh"

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: 50 * time.Millisecond}
		err := sess.Create("architect beacon", "manager beacon")
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "did not start") {
			t.Errorf("expected 'did not start' in error, got: %v", err)
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

func TestRoleEnvCmd(t *testing.T) {
	t.Run("architect role sets all three env vars", func(t *testing.T) {
		cmd := roleEnvCmd("architect")
		for _, envVar := range []string{"ORO_ROLE=architect", "BD_ACTOR=architect", "GIT_AUTHOR_NAME=architect"} {
			if !strings.Contains(cmd, envVar) {
				t.Errorf("expected roleEnvCmd to contain %s, got: %s", envVar, cmd)
			}
		}
		if !strings.Contains(cmd, "&& claude") {
			t.Errorf("expected roleEnvCmd to end with '&& claude', got: %s", cmd)
		}
	})

	t.Run("manager role sets all three env vars", func(t *testing.T) {
		cmd := roleEnvCmd("manager")
		for _, envVar := range []string{"ORO_ROLE=manager", "BD_ACTOR=manager", "GIT_AUTHOR_NAME=manager"} {
			if !strings.Contains(cmd, envVar) {
				t.Errorf("expected roleEnvCmd to contain %s, got: %s", envVar, cmd)
			}
		}
		if !strings.Contains(cmd, "&& claude") {
			t.Errorf("expected roleEnvCmd to end with '&& claude', got: %s", cmd)
		}
	})

	t.Run("uses export for env vars", func(t *testing.T) {
		cmd := roleEnvCmd("worker")
		if !strings.Contains(cmd, "export") {
			t.Errorf("expected roleEnvCmd to use export, got: %s", cmd)
		}
	})

	t.Run("includes --session-id for history isolation", func(t *testing.T) {
		cmd := roleEnvCmd("architect")
		if !strings.Contains(cmd, "--session-id") {
			t.Errorf("expected roleEnvCmd to contain --session-id, got: %s", cmd)
		}
		// Must still launch interactive claude (not claude -p)
		if !strings.Contains(cmd, "claude") {
			t.Errorf("expected roleEnvCmd to contain 'claude', got: %s", cmd)
		}
		if strings.Contains(cmd, "claude -p") {
			t.Errorf("expected interactive claude (not 'claude -p'), got: %s", cmd)
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
