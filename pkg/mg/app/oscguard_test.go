package app

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

func TestOSCGuardAllowsNormalKeys(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Normal key presses with human-speed gaps should pass through.
	msg := tea.KeyPressMsg{Code: 'j', Text: "j"}
	if filter(nil, msg) == nil {
		t.Fatal("expected normal key 'j' to pass through")
	}

	time.Sleep(50 * time.Millisecond)
	msg = tea.KeyPressMsg{Code: 'k', Text: "k"}
	if filter(nil, msg) == nil {
		t.Fatal("expected normal key 'k' to pass through")
	}
}

func TestOSCGuardSuppressesFastBurst(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Simulate control-sequence tail burst: chars arriving with no
	// sleep between calls (effectively 0ms gap).
	burstChars := []struct {
		code rune
		text string
	}{
		{';', ";"},
		{'r', "r"},
		{'g', "g"},
		{'b', "b"},
		{':', ":"},
		{'1', "1"},
		{'f', "f"},
	}

	var suppressed int
	for _, ch := range burstChars {
		msg := tea.KeyPressMsg{Code: ch.code, Text: ch.text}
		if filter(nil, msg) == nil {
			suppressed++
		}
	}

	// The first char passes (no prior timing reference), but all subsequent
	// chars should be suppressed via burst detection + window.
	if suppressed < 5 {
		t.Fatalf("expected at least 5 suppressed keys in burst, got %d", suppressed)
	}
}

func TestOSCGuardWindowSuppressesSlowFollowers(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Two fast chars to trigger burst detection.
	filter(nil, tea.KeyPressMsg{Code: '1', Text: "1"})
	filter(nil, tea.KeyPressMsg{Code: ';', Text: ";"})

	// Now wait 50ms (within the 500ms window) and send another char.
	time.Sleep(50 * time.Millisecond)
	msg := tea.KeyPressMsg{Code: ':', Text: ":"}
	if filter(nil, msg) != nil {
		t.Fatal("expected ':' to be suppressed within window")
	}
}

func TestOSCGuardAlwaysAllowsCtrlC(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Even during a burst, ctrl+c should pass through.
	filter(nil, tea.KeyPressMsg{Code: '1', Text: "1"})
	filter(nil, tea.KeyPressMsg{Code: ';', Text: ";"})

	msg := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	if filter(nil, msg) == nil {
		t.Fatal("expected ctrl+c to pass through even during burst")
	}
}

func TestOSCGuardSuppressesModifierTailsDuringWindow(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Trigger burst.
	filter(nil, tea.KeyPressMsg{Code: '1', Text: "1"})
	filter(nil, tea.KeyPressMsg{Code: ';', Text: ";"})

	// alt+\ (OSC string terminator tail) should be suppressed during window.
	msg := tea.KeyPressMsg{Code: '\\', Mod: tea.ModAlt}
	if filter(nil, msg) != nil {
		t.Fatal("expected alt+\\ to be suppressed during window")
	}

	// shift+R (CPR response tail byte) should be suppressed during window.
	msg = tea.KeyPressMsg{Code: 'r', Mod: tea.ModShift}
	if filter(nil, msg) != nil {
		t.Fatal("expected shift+r to be suppressed during window")
	}

	// shift+B (split CSI tail byte) should be suppressed during window.
	msg = tea.KeyPressMsg{Code: 'b', Mod: tea.ModShift}
	if filter(nil, msg) != nil {
		t.Fatal("expected shift+b to be suppressed during window")
	}
}

func TestOSCGuardAllowsModifierKeysOutsideWindow(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Outside any burst window, modified keys should pass through.
	msg := tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt}
	if filter(nil, msg) == nil {
		t.Fatal("expected alt+x to pass through outside window")
	}

	msg = tea.KeyPressMsg{Code: 'r', Mod: tea.ModShift}
	if filter(nil, msg) == nil {
		t.Fatal("expected shift+r to pass through outside window")
	}
}

func TestOSCGuardAllowsNonCharKeys(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Trigger burst.
	filter(nil, tea.KeyPressMsg{Code: '1', Text: "1"})
	filter(nil, tea.KeyPressMsg{Code: ';', Text: ";"})

	// Arrow keys should pass through even during burst.
	msg := tea.KeyPressMsg{Code: tea.KeyDown}
	if filter(nil, msg) == nil {
		t.Fatal("expected down arrow to pass through even during burst")
	}
}

func TestOSCGuardWindowExpires(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Trigger burst.
	filter(nil, tea.KeyPressMsg{Code: '1', Text: "1"})
	filter(nil, tea.KeyPressMsg{Code: ';', Text: ";"})

	// Wait for window to expire.
	time.Sleep(600 * time.Millisecond)

	msg := tea.KeyPressMsg{Code: '1', Text: "1"}
	if filter(nil, msg) == nil {
		t.Fatal("expected '1' to pass through after window expired")
	}
}

func TestOSCGuardSuppressesAllCharsInWindow(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Trigger burst with two fast chars.
	filter(nil, tea.KeyPressMsg{Code: '1', Text: "1"})
	filter(nil, tea.KeyPressMsg{Code: ';', Text: ";"})

	// All printable chars should be suppressed during window.
	time.Sleep(5 * time.Millisecond)

	for _, ch := range []struct {
		code rune
		text string
	}{
		{'r', "r"},
		{'g', "g"},
		{'b', "b"},
		{':', ":"},
		{'x', "x"},
		{'z', "z"},
	} {
		msg := tea.KeyPressMsg{Code: ch.code, Text: ch.text}
		if filter(nil, msg) != nil {
			t.Fatalf("expected %q to be suppressed within window", ch.text)
		}
	}
}

func TestOSCGuardSuppressesCharAfterNavKey(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Simulate: user presses 'down', then ']' arrives ~20ms later as
	// the first leaked byte of a torn control sequence.
	filter(nil, tea.KeyPressMsg{Code: tea.KeyDown})

	// The ']' arrives faster than a human could type after pressing down.
	time.Sleep(20 * time.Millisecond)
	msg := tea.KeyPressMsg{Code: ']', Text: "]"}
	if filter(nil, msg) != nil {
		t.Fatal("expected ']' to be suppressed after nav key")
	}

	// Subsequent chars should also be suppressed (window is now active).
	msg = tea.KeyPressMsg{Code: '1', Text: "1"}
	if filter(nil, msg) != nil {
		t.Fatal("expected '1' to be suppressed in window after nav-triggered suppression")
	}
}

func TestOSCGuardAllowsCharLongAfterNavKey(t *testing.T) {
	filter := NewOSCGuardFilter()

	// User presses 'down', then types 'j' 100ms later — normal usage.
	filter(nil, tea.KeyPressMsg{Code: tea.KeyDown})

	time.Sleep(100 * time.Millisecond)
	msg := tea.KeyPressMsg{Code: 'j', Text: "j"}
	if filter(nil, msg) == nil {
		t.Fatal("expected 'j' to pass through 100ms after nav key")
	}
}

func TestOSCGuardAllowsCtrlCombosInWindow(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Trigger burst.
	filter(nil, tea.KeyPressMsg{Code: '1', Text: "1"})
	filter(nil, tea.KeyPressMsg{Code: ';', Text: ";"})

	// ctrl+k should pass through during window (user shortcut).
	msg := tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}
	if filter(nil, msg) == nil {
		t.Fatal("expected ctrl+k to pass through during window")
	}

	// ctrl+n should pass through during window.
	msg = tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}
	if filter(nil, msg) == nil {
		t.Fatal("expected ctrl+n to pass through during window")
	}
}

func TestOSCGuardSuppressesKnownArtifactKeys(t *testing.T) {
	filter := NewOSCGuardFilter()

	msg := tea.KeyPressMsg{Code: '\\', Mod: tea.ModAlt}
	if filter(nil, msg) != nil {
		t.Fatal("expected alt+\\ artifact to be suppressed")
	}

	msg = tea.KeyPressMsg{Code: tea.KeyF3, Mod: tea.ModAlt | tea.ModMeta}
	if filter(nil, msg) != nil {
		t.Fatal("expected alt+meta+f3 artifact to be suppressed")
	}
}

// --- Layer 1: UnknownEvent suppression ---

func TestOSCGuardDropsUnknownEvent(t *testing.T) {
	filter := NewOSCGuardFilter()

	// uv.UnknownEvent from a torn sequence ultraviolet kept intact.
	msg := uv.UnknownEvent("\x1b]11;rgb:1f1f/2323/3535\\[5;6R")
	if filter(nil, msg) != nil {
		t.Fatal("expected uv.UnknownEvent to be dropped")
	}
}

func TestOSCGuardUnknownEventOpensWindow(t *testing.T) {
	filter := NewOSCGuardFilter()

	// UnknownEvent should open a suppression window.
	filter(nil, uv.UnknownEvent("\x1b]11;rgb:1f1f/2323/3535\\"))

	// A char arriving soon after should be caught by the window.
	time.Sleep(5 * time.Millisecond)
	msg := tea.KeyPressMsg{Code: '\\', Text: "\\"}
	if filter(nil, msg) != nil {
		t.Fatal("expected '\\' to be suppressed after UnknownEvent")
	}
}

func TestOSCGuardSuppressesTailAfterControlReply(t *testing.T) {
	filter := NewOSCGuardFilter()

	// A parsed reply should open a short tail window.
	if filter(nil, tea.CursorPositionMsg{X: 5, Y: 6}) == nil {
		t.Fatal("expected cursor position reply to pass through")
	}

	time.Sleep(5 * time.Millisecond)
	msg := tea.KeyPressMsg{Code: 'a', Mod: tea.ModShift}
	if filter(nil, msg) != nil {
		t.Fatal("expected shift+a tail to be suppressed after control reply")
	}
}

// --- Layer 3: content-aware pattern detection ---

func TestOSCGuardPatternSemiRG(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Simulate slow-dripped ;rgb: with human-scale gaps.
	// ';' and 'r' pass through (no pattern yet).
	// 'g' completes ";rg" and should be suppressed.
	filter(nil, tea.KeyPressMsg{Code: ';', Text: ";"})
	time.Sleep(50 * time.Millisecond)
	filter(nil, tea.KeyPressMsg{Code: 'r', Text: "r"})
	time.Sleep(50 * time.Millisecond)

	msg := tea.KeyPressMsg{Code: 'g', Text: "g"}
	if filter(nil, msg) != nil {
		t.Fatal("expected 'g' to be suppressed by ';rg' pattern")
	}

	// 'b' should be caught by the window opened by pattern match.
	time.Sleep(50 * time.Millisecond)
	msg = tea.KeyPressMsg{Code: 'b', Text: "b"}
	if filter(nil, msg) != nil {
		t.Fatal("expected 'b' to be suppressed in window after pattern")
	}
}

func TestOSCGuardPatternBracket1(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Simulate slow-dripped ]11; from a torn OSC 11 introducer.
	// ']' passes through, '1' completes "]1" and should be suppressed.
	filter(nil, tea.KeyPressMsg{Code: ']', Text: "]"})
	time.Sleep(50 * time.Millisecond)

	msg := tea.KeyPressMsg{Code: '1', Text: "1"}
	if filter(nil, msg) != nil {
		t.Fatal("expected '1' to be suppressed by ']1' pattern")
	}
}

func TestOSCGuardPatternBracketQuestion(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Simulate torn CSI private parameter: [?2026;2$y
	// '[' passes through, '?' completes "[?" and should be suppressed.
	filter(nil, tea.KeyPressMsg{Code: '[', Text: "["})
	time.Sleep(50 * time.Millisecond)

	msg := tea.KeyPressMsg{Code: '?', Text: "?"}
	if filter(nil, msg) != nil {
		t.Fatal("expected '?' to be suppressed by '[?' pattern")
	}
}

func TestOSCGuardPatternBracketDigit(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Slow-dripped CPR prefix: [28;135R
	// '[' passes through, '2' is enough to identify a CSI parameter prefix.
	filter(nil, tea.KeyPressMsg{Code: '[', Text: "["})
	time.Sleep(50 * time.Millisecond)

	msg := tea.KeyPressMsg{Code: '2', Text: "2"}
	if filter(nil, msg) != nil {
		t.Fatal("expected '2' to be suppressed by '[2' prefix")
	}
}

func TestOSCGuardPatternDollarY(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Simulate DECRPM terminator.
	filter(nil, tea.KeyPressMsg{Code: '$', Text: "$"})
	time.Sleep(50 * time.Millisecond)

	msg := tea.KeyPressMsg{Code: 'y', Text: "y"}
	if filter(nil, msg) != nil {
		t.Fatal("expected 'y' to be suppressed by '$y' pattern")
	}
}

func TestOSCGuardPatternRGBColon(t *testing.T) {
	filter := NewOSCGuardFilter()

	// "rgb:" matches even without leading ";".
	filter(nil, tea.KeyPressMsg{Code: 'r', Text: "r"})
	time.Sleep(50 * time.Millisecond)
	filter(nil, tea.KeyPressMsg{Code: 'g', Text: "g"})
	time.Sleep(50 * time.Millisecond)
	filter(nil, tea.KeyPressMsg{Code: 'b', Text: "b"})
	time.Sleep(50 * time.Millisecond)

	msg := tea.KeyPressMsg{Code: ':', Text: ":"}
	if filter(nil, msg) != nil {
		t.Fatal("expected ':' to be suppressed by 'rgb:' pattern")
	}
}

func TestOSCGuardPatternResetsOnNavKey(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Start accumulating suspicious chars.
	filter(nil, tea.KeyPressMsg{Code: ';', Text: ";"})
	time.Sleep(50 * time.Millisecond)

	// A nav key resets the accumulator.
	filter(nil, tea.KeyPressMsg{Code: tea.KeyDown})
	time.Sleep(100 * time.Millisecond)

	// 'r' and 'g' no longer complete ";rg" because ';' was flushed.
	filter(nil, tea.KeyPressMsg{Code: 'r', Text: "r"})
	time.Sleep(50 * time.Millisecond)

	msg := tea.KeyPressMsg{Code: 'g', Text: "g"}
	if filter(nil, msg) == nil {
		t.Fatal("expected 'g' to pass through — nav key reset accumulator")
	}
}

func TestOSCGuardPatternResetsOnLongGap(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Accumulate some chars.
	filter(nil, tea.KeyPressMsg{Code: ';', Text: ";"})
	filter(nil, tea.KeyPressMsg{Code: 'r', Text: "r"})

	// Wait > 2 seconds to reset the accumulator.
	time.Sleep(2100 * time.Millisecond)

	// 'g' should NOT match ";rg" because accumulator was reset.
	msg := tea.KeyPressMsg{Code: 'g', Text: "g"}
	if filter(nil, msg) == nil {
		t.Fatal("expected 'g' to pass through — long gap reset accumulator")
	}
}

func TestOSCGuardPatternNoFalsePositiveOnNormalTyping(t *testing.T) {
	filter := NewOSCGuardFilter()

	// Normal typing: individual chars with human-speed gaps
	// that don't form control-sequence patterns.
	chars := []struct {
		code rune
		text string
	}{
		{'h', "h"},
		{'e', "e"},
		{'l', "l"},
		{'p', "p"},
	}

	for _, ch := range chars {
		time.Sleep(50 * time.Millisecond)
		msg := tea.KeyPressMsg{Code: ch.code, Text: ch.text}
		if filter(nil, msg) == nil {
			t.Fatalf("expected %q to pass through during normal typing", ch.text)
		}
	}
}

func TestOSCGuardSuppressesDigitSoonAfterNavKey(t *testing.T) {
	filter := NewOSCGuardFilter()

	filter(nil, tea.KeyPressMsg{Code: tea.KeyDown})

	// 40ms is still too fast to be intentional "down then 1" input.
	time.Sleep(40 * time.Millisecond)
	msg := tea.KeyPressMsg{Code: '1', Text: "1"}
	if filter(nil, msg) != nil {
		t.Fatal("expected '1' to be suppressed immediately after nav key")
	}
}
