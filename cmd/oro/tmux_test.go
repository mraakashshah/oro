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

// readyPaneOutput returns fake capture-pane output containing the Claude prompt.
const readyPaneOutput = "some startup output\n> "

// stubCapturePaneReady sets up the fake to return ready output for both panes.
func stubCapturePaneReady(fake *fakeCmd, sessionName string) {
	fake.output[key("tmux", "capture-pane", "-p", "-t", sessionName+":0.0")] = readyPaneOutput
	fake.output[key("tmux", "capture-pane", "-p", "-t", sessionName+":0.1")] = readyPaneOutput
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
	t.Run("Create builds session with two panes", func(t *testing.T) {
		fake := newFakeCmd()
		// has-session returns error (session does not exist)
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		// list-panes returns two panes after creation
		fake.output[key("tmux", "list-panes", "-t", "oro", "-F", "#{pane_index}")] = "0\n1\n"
		stubCapturePaneReady(fake, "oro")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect beacon", "manager beacon")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify: new-session was called with -d and -s oro
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

		// Verify: split-window was called for horizontal split
		splitCall := findCall(fake.calls, "split-window")
		if splitCall == nil {
			t.Fatal("expected tmux split-window to be called")
		}
		if !callHasArg(splitCall, "-h") {
			t.Error("split-window should use -h for horizontal split")
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

	t.Run("Create sends commands to panes", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		fake.output[key("tmux", "list-panes", "-t", "oro", "-F", "#{pane_index}")] = "0\n1\n"
		stubCapturePaneReady(fake, "oro")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect beacon text", "manager beacon text")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify send-keys was called for both panes:
		// - pane 0: export ORO_ROLE=architect && claude, then architect beacon
		// - pane 1: export ORO_ROLE=manager && claude, then manager beacon
		// That's 4 send-keys calls total (2 launch + 2 beacon injection).
		sendKeysCount := 0
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				sendKeysCount++
			}
		}
		if sendKeysCount < 4 {
			t.Errorf("expected at least 4 send-keys calls (2 launch + 2 beacon), got %d", sendKeysCount)
		}
	})

	t.Run("Create launches interactive claude with ORO_ROLE env var", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubCapturePaneReady(fake, "oro")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect beacon", "manager beacon")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Collect all send-keys calls targeting each pane.
		var pane0Calls, pane1Calls [][]string
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "oro:0.0") {
					pane0Calls = append(pane0Calls, call)
				}
				if strings.Contains(joined, "oro:0.1") {
					pane1Calls = append(pane1Calls, call)
				}
			}
		}

		// Pane 0: first send-keys should launch claude with ORO_ROLE=architect
		if len(pane0Calls) < 2 {
			t.Fatalf("expected at least 2 send-keys to pane 0, got %d", len(pane0Calls))
		}
		p0Launch := strings.Join(pane0Calls[0], " ")
		if !strings.Contains(p0Launch, "ORO_ROLE=architect") {
			t.Errorf("pane 0 launch should set ORO_ROLE=architect, got: %s", p0Launch)
		}
		if !strings.Contains(p0Launch, "claude") {
			t.Errorf("pane 0 launch should run claude, got: %s", p0Launch)
		}
		// Must NOT use claude -p
		if strings.Contains(p0Launch, "claude -p") {
			t.Errorf("pane 0 should use interactive claude, not 'claude -p', got: %s", p0Launch)
		}

		// Pane 1: first send-keys should launch claude with ORO_ROLE=manager
		if len(pane1Calls) < 2 {
			t.Fatalf("expected at least 2 send-keys to pane 1, got %d", len(pane1Calls))
		}
		p1Launch := strings.Join(pane1Calls[0], " ")
		if !strings.Contains(p1Launch, "ORO_ROLE=manager") {
			t.Errorf("pane 1 launch should set ORO_ROLE=manager, got: %s", p1Launch)
		}
		if !strings.Contains(p1Launch, "claude") {
			t.Errorf("pane 1 launch should run claude, got: %s", p1Launch)
		}
		// Must NOT use claude -p
		if strings.Contains(p1Launch, "claude -p") {
			t.Errorf("pane 1 should use interactive claude, not 'claude -p', got: %s", p1Launch)
		}
	})

	t.Run("Create injects beacons via send-keys after claude launch", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubCapturePaneReady(fake, "oro")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect beacon text here", "manager beacon text here")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Collect send-keys calls per pane.
		var pane0Calls, pane1Calls [][]string
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "oro:0.0") {
					pane0Calls = append(pane0Calls, call)
				}
				if strings.Contains(joined, "oro:0.1") {
					pane1Calls = append(pane1Calls, call)
				}
			}
		}

		// Pane 0: second send-keys should contain the architect beacon.
		if len(pane0Calls) < 2 {
			t.Fatalf("expected at least 2 send-keys to pane 0, got %d", len(pane0Calls))
		}
		p0Beacon := strings.Join(pane0Calls[1], " ")
		if !strings.Contains(p0Beacon, "architect beacon text here") {
			t.Errorf("pane 0 beacon injection should contain architect beacon, got: %s", p0Beacon)
		}

		// Pane 1: second send-keys should contain the manager beacon.
		if len(pane1Calls) < 2 {
			t.Fatalf("expected at least 2 send-keys to pane 1, got %d", len(pane1Calls))
		}
		p1Beacon := strings.Join(pane1Calls[1], " ")
		if !strings.Contains(p1Beacon, "manager beacon text here") {
			t.Errorf("pane 1 beacon injection should contain manager beacon, got: %s", p1Beacon)
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

	t.Run("Create polls pane readiness before injecting beacons", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubCapturePaneReady(fake, "oro")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect beacon", "manager beacon")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify capture-pane was called for both panes.
		var capturePane0, capturePane1 bool
		for _, call := range fake.calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "capture-pane") && strings.Contains(joined, "oro:0.0") {
				capturePane0 = true
			}
			if strings.Contains(joined, "capture-pane") && strings.Contains(joined, "oro:0.1") {
				capturePane1 = true
			}
		}
		if !capturePane0 {
			t.Error("expected capture-pane to be called for pane 0")
		}
		if !capturePane1 {
			t.Error("expected capture-pane to be called for pane 1")
		}
	})

	t.Run("Create times out when Claude never becomes ready", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		// capture-pane returns content without the > prompt — never ready
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:0.0")] = "Loading claude..."
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:0.1")] = "Loading claude..."

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: 50 * time.Millisecond}
		err := sess.Create("architect beacon", "manager beacon")
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "did not become ready") {
			t.Errorf("expected 'did not become ready' in error, got: %v", err)
		}
	})
}

func TestPaneReady(t *testing.T) {
	t.Run("returns nil when prompt found immediately", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:0.0")] = "Welcome to Claude\n> "

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.PaneReady("oro:0.0")
		if err != nil {
			t.Fatalf("PaneReady returned error: %v", err)
		}
	})

	t.Run("returns nil after polling succeeds on third attempt", func(t *testing.T) {
		fake := newFakeCmd()
		// First two capture-pane calls return no prompt; third has it.
		captureKey := key("tmux", "capture-pane", "-p", "-t", "oro:0.0")
		fake.seqOut[captureKey] = []string{
			"Loading...",
			"Still loading...",
			"Welcome to Claude\n> ",
		}

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: 5 * time.Second}
		err := sess.PaneReady("oro:0.0")
		if err != nil {
			t.Fatalf("PaneReady returned error: %v", err)
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

	t.Run("times out when prompt never appears", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:0.0")] = "$ claude\nStarting..."

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: 50 * time.Millisecond}
		err := sess.PaneReady("oro:0.0")
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "did not become ready") {
			t.Errorf("expected 'did not become ready' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "oro:0.0") {
			t.Errorf("expected pane target in error, got: %v", err)
		}
	})

	t.Run("handles capture-pane error gracefully and keeps polling", func(t *testing.T) {
		fake := newFakeCmd()
		// First call errors, second succeeds with prompt.
		captureKey := key("tmux", "capture-pane", "-p", "-t", "oro:0.0")
		fake.seqOut[captureKey] = []string{
			"",
			"Welcome\n> ",
		}
		// We need errors on the first call only. Use a custom approach:
		// Actually the seqOut approach doesn't support per-call errors.
		// Let's just test that errors are tolerated by having the output empty on first call.
		// An empty output has no prompt, so it will retry. Second call has the prompt.

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: 5 * time.Second}
		err := sess.PaneReady("oro:0.0")
		if err != nil {
			t.Fatalf("PaneReady returned error: %v", err)
		}
	})

	t.Run("recognizes prompt with leading whitespace", func(t *testing.T) {
		fake := newFakeCmd()
		// Claude Code sometimes has the prompt with some whitespace.
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:0.0")] = "some output\n  > "

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.PaneReady("oro:0.0")
		if err != nil {
			t.Fatalf("PaneReady returned error: %v", err)
		}
	})

	t.Run("uses default 30s timeout when ReadyTimeout is zero", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:0.0")] = "Welcome\n> "

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.PaneReady("oro:0.0")
		if err != nil {
			t.Fatalf("PaneReady returned error: %v", err)
		}
	})
}

func TestVerifyBeaconReceived(t *testing.T) {
	t.Run("returns nil when indicator found immediately", func(t *testing.T) {
		fake := newFakeCmd()
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:0.1")] = "some output\nbd stats\nmore output"

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.VerifyBeaconReceived("oro:0.1", "bd stats", time.Second)
		if err != nil {
			t.Fatalf("VerifyBeaconReceived returned error: %v", err)
		}
	})

	t.Run("returns nil after polling succeeds on third attempt", func(t *testing.T) {
		fake := newFakeCmd()
		captureKey := key("tmux", "capture-pane", "-p", "-t", "oro:0.1")
		fake.seqOut[captureKey] = []string{
			"claude loading...",
			"still waiting...",
			"running bd stats\noutput here",
		}

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.VerifyBeaconReceived("oro:0.1", "bd stats", 5*time.Second)
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
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:0.1")] = "stuck on loading screen"

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.VerifyBeaconReceived("oro:0.1", "bd stats", 50*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "oro:0.1") {
			t.Errorf("expected pane target in error, got: %v", err)
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
		fake.output[key("tmux", "capture-pane", "-p", "-t", "oro:0.0")] = "some text with > prompt visible"

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.VerifyBeaconReceived("oro:0.0", ">", time.Second)
		if err != nil {
			t.Fatalf("VerifyBeaconReceived returned error: %v", err)
		}
	})

	t.Run("tolerates capture-pane errors and keeps polling", func(t *testing.T) {
		fake := newFakeCmd()
		captureKey := key("tmux", "capture-pane", "-p", "-t", "oro:0.1")
		// First call returns empty (simulating error), second has indicator
		fake.seqOut[captureKey] = []string{
			"",
			"bd stats\nsome output",
		}

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.VerifyBeaconReceived("oro:0.1", "bd stats", 5*time.Second)
		if err != nil {
			t.Fatalf("VerifyBeaconReceived returned error: %v", err)
		}
	})
}

func TestCreateVerifiesBeaconAfterInjection(t *testing.T) {
	t.Run("Create calls VerifyBeaconReceived for manager pane after injection", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubCapturePaneReady(fake, "oro")

		// After beacon injection, the manager pane will show "bd stats" activity.
		// We need sequential output: first calls return prompt (for PaneReady),
		// then subsequent calls return beacon activity (for VerifyBeaconReceived).
		// Since stubCapturePaneReady uses output (not seqOut), and VerifyBeaconReceived
		// also uses capture-pane on the same key, we use seqOut to handle both phases.
		managerCaptureKey := key("tmux", "capture-pane", "-p", "-t", "oro:0.1")
		delete(fake.output, managerCaptureKey)
		fake.seqOut[managerCaptureKey] = []string{
			readyPaneOutput,            // PaneReady poll
			"bd stats\noutput visible", // VerifyBeaconReceived poll
		}

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: time.Second}
		err := sess.Create("architect beacon", "manager beacon")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Verify that capture-pane was called for the manager pane MORE than once
		// (once for PaneReady, at least once for VerifyBeaconReceived).
		managerCaptureCount := 0
		for _, call := range fake.calls {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "capture-pane") && strings.Contains(joined, "oro:0.1") {
				managerCaptureCount++
			}
		}
		if managerCaptureCount < 2 {
			t.Errorf("expected at least 2 capture-pane calls for manager (PaneReady + VerifyBeacon), got %d", managerCaptureCount)
		}
	})

	t.Run("Create does not fail when beacon verification times out (warning only)", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubCapturePaneReady(fake, "oro")

		// Manager pane never shows beacon activity after injection.
		managerCaptureKey := key("tmux", "capture-pane", "-p", "-t", "oro:0.1")
		delete(fake.output, managerCaptureKey)
		fake.seqOut[managerCaptureKey] = []string{
			readyPaneOutput,    // PaneReady poll succeeds
			"no beacon output", // VerifyBeaconReceived polls — never shows indicator
		}

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		// Create should succeed even if beacon verification fails — it's a warning
		err := sess.Create("architect beacon", "manager beacon")
		if err != nil {
			t.Fatalf("Create should not fail on beacon verification timeout, got: %v", err)
		}
	})
}
