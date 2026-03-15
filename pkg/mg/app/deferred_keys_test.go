package app

import (
	"testing"
	"time"

	"oro/pkg/mg/data"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

func setupDeferredKeyModel(t *testing.T) (model Model, filter func(tea.Model, tea.Msg) tea.Msg) {
	t.Helper()

	guard := NewOSCGuard()
	filter = guard.Filter()
	issues := []data.Issue{
		testIssue("open-1", data.StatusOpen),
		testIssue("open-2", data.StatusOpen),
	}
	m := NewWithGuard(issues, data.Source{}, data.DefaultBlockingTypes, guard)
	m.startedAt = time.Now().Add(-time.Second)

	readyModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	return readyModel.(Model), filter
}

func sendFiltered(t *testing.T, m Model, filter func(tea.Model, tea.Msg) tea.Msg, msg tea.Msg) (Model, tea.Cmd, bool) {
	t.Helper()

	filtered := filter(nil, msg)
	if filtered == nil {
		return m, nil, false
	}

	model, cmd := m.Update(filtered)
	return model.(Model), cmd, true
}

func TestNavigationKeysProcessedImmediately(t *testing.T) {
	// Navigation keys should NOT be deferred — they should process immediately
	// without going through the 60ms deferral buffer.
	navKeys := []rune{'j', 'k', 'g', 'q', 'c', 'f', 'w', '/', '?'}
	for _, key := range navKeys {
		// Fresh model per key to avoid OSC guard accumulator interference.
		m, filter := setupDeferredKeyModel(t)
		m2, cmd, ok := sendFiltered(t, m, filter, tea.KeyPressMsg{Code: key, Text: string(key)})
		if !ok {
			t.Fatalf("expected %q to pass filter", string(key))
		}
		// Non-deferred keys should NOT produce a deferredKeyMsg command.
		// They should be processed inline (cmd may be nil or a real action, but NOT a deferred staging).
		if cmd != nil {
			msg := cmd()
			if _, isDef := msg.(deferredKeyMsg); isDef {
				t.Fatalf("key %q was deferred but should be processed immediately", string(key))
			}
		}
		if len(m2.pendingKeys) != 0 {
			t.Fatalf("key %q staged a pending key but should be processed immediately", string(key))
		}
	}
}

func TestFragmentStarterKeysStillDeferred(t *testing.T) {
	// Keys that can start a control-sequence fragment pair should still be deferred.
	fragKeys := []rune{'[', ']', ';', '0', '5', '9'}
	for _, key := range fragKeys {
		// Fresh model per key to avoid OSC guard accumulator interference.
		m, filter := setupDeferredKeyModel(t)
		m2, cmd, ok := sendFiltered(t, m, filter, tea.KeyPressMsg{Code: key, Text: string(key)})
		if !ok {
			t.Fatalf("expected %q to pass filter", string(key))
		}
		if cmd == nil {
			t.Fatalf("expected %q to be deferred (produce a staging command)", string(key))
		}
		if len(m2.pendingKeys) != 1 {
			t.Fatalf("expected %q to stage 1 pending key, got %d", string(key), len(m2.pendingKeys))
		}
	}
}

func TestDeferredKeyPassesAfterDelay(t *testing.T) {
	m, filter := setupDeferredKeyModel(t)

	// Use a fragment-starter key (;) since navigation keys are no longer deferred.
	var cmd tea.Cmd
	var ok bool
	m, cmd, ok = sendFiltered(t, m, filter, tea.KeyPressMsg{Code: ';', Text: ";"})
	if !ok {
		t.Fatal("expected ; to pass filter")
	}
	if cmd == nil {
		t.Fatal("expected deferred command after staging ;")
	}

	msg := cmd()
	deferred, ok := msg.(deferredKeyMsg)
	if !ok {
		t.Fatalf("expected deferredKeyMsg, got %T", msg)
	}

	model, resolvedCmd := m.Update(deferred)
	m = model.(Model)
	// ; is not a bound key, so it produces no action — but it should resolve without error.
	_ = resolvedCmd
	if len(m.pendingKeys) != 0 {
		t.Fatal("expected pending key queue to be cleared after deferred delivery")
	}
}

func TestDeferredKeyDropsSuspiciousPair(t *testing.T) {
	m, filter := setupDeferredKeyModel(t)

	m, firstCmd, ok := sendFiltered(t, m, filter, tea.KeyPressMsg{Code: '1', Text: "1"})
	if !ok {
		t.Fatal("expected 1 to pass filter")
	}
	if firstCmd == nil {
		t.Fatal("expected deferred command after staging 1")
	}

	time.Sleep(20 * time.Millisecond)

	m, secondCmd, ok := sendFiltered(t, m, filter, tea.KeyPressMsg{Code: ';', Text: ";"})
	if !ok {
		t.Fatal("expected ; to reach Update for pair detection")
	}
	if secondCmd != nil {
		t.Fatal("expected suspicious pair to be dropped without routing a command")
	}
	if len(m.pendingKeys) != 0 {
		t.Fatal("expected pending key queue to be cleared after suspicious pair drop")
	}

	model, cmd := m.Update(firstCmd())
	m = model.(Model)
	if cmd != nil {
		t.Fatal("expected stale deferred message to be ignored after pair drop")
	}
	if len(m.pendingKeys) != 0 {
		t.Fatal("expected no pending key after stale deferred message")
	}
}

func TestDeferredKeyDropsAfterFilterSuppression(t *testing.T) {
	m, filter := setupDeferredKeyModel(t)

	m, firstCmd, ok := sendFiltered(t, m, filter, tea.KeyPressMsg{Code: ']', Text: "]"})
	if !ok {
		t.Fatal("expected ] to pass filter and stage")
	}
	if firstCmd == nil {
		t.Fatal("expected deferred command after staging ]")
	}

	if filtered := filter(nil, tea.KeyPressMsg{Code: '1', Text: "1"}); filtered != nil {
		t.Fatal("expected 1 to be suppressed by the shared guard filter")
	}

	model, cmd := m.Update(firstCmd())
	m = model.(Model)
	if cmd != nil {
		t.Fatal("expected deferred ] to be dropped after later filter suppression")
	}
	if len(m.pendingKeys) != 0 {
		t.Fatal("expected pending key queue to be cleared after deferred drop")
	}
}

func TestDeferredQuickActionDigitWaitsForTimer(t *testing.T) {
	m, filter := setupDeferredKeyModel(t)

	m, firstCmd, ok := sendFiltered(t, m, filter, tea.KeyPressMsg{Code: '3', Text: "3"})
	if !ok {
		t.Fatal("expected 3 to pass filter and stage")
	}
	if firstCmd == nil {
		t.Fatal("expected deferred command after staging 3")
	}
	if len(m.pendingKeys) != 1 {
		t.Fatalf("expected 1 pending key after staging 3, got %d", len(m.pendingKeys))
	}

	time.Sleep(20 * time.Millisecond)

	m, secondCmd, ok := sendFiltered(t, m, filter, tea.KeyPressMsg{Code: '2', Text: "2"})
	if !ok {
		t.Fatal("expected 2 to pass filter and stage")
	}
	if secondCmd == nil {
		t.Fatal("expected second deferred command after staging 2 behind 3")
	}
	if len(m.pendingKeys) != 2 {
		t.Fatalf("expected 2 pending keys after staging 3 and 2, got %d", len(m.pendingKeys))
	}

	if filtered := filter(nil, uv.UnknownEvent("\x1b]11;rgb:1f1f/2323/3535")); filtered != nil {
		t.Fatal("expected UnknownEvent to be dropped by shared guard")
	}

	model, cmd := m.Update(firstCmd())
	m = model.(Model)
	if cmd != nil {
		t.Fatal("expected deferred 3 to be dropped after later suspicious input")
	}
	if len(m.pendingKeys) != 1 {
		t.Fatalf("expected only the second pending key to remain, got %d", len(m.pendingKeys))
	}

	model, cmd = m.Update(secondCmd())
	m = model.(Model)
	if cmd != nil {
		t.Fatal("expected deferred 2 to be dropped after later suspicious input")
	}
	if len(m.pendingKeys) != 0 {
		t.Fatal("expected pending key queue to be cleared after both deferred drops")
	}
}
