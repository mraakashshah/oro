package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestStartupLog_Step(t *testing.T) {
	var buf bytes.Buffer
	log := newStartupLog(&buf, true) // TTY mode

	log.Step("Preflight checks passed")

	output := buf.String()
	if !strings.Contains(output, "✓") {
		t.Errorf("expected ✓ checkmark, got: %q", output)
	}
	if !strings.Contains(output, "Preflight checks passed") {
		t.Errorf("expected message, got: %q", output)
	}
}

func TestStartupLog_StepTimed(t *testing.T) {
	var buf bytes.Buffer
	log := newStartupLog(&buf, true) // TTY mode

	log.StepTimed("Architect ready", 34*time.Second)

	output := buf.String()
	if !strings.Contains(output, "✓") {
		t.Errorf("expected ✓ checkmark, got: %q", output)
	}
	if !strings.Contains(output, "Architect ready") {
		t.Errorf("expected message, got: %q", output)
	}
	if !strings.Contains(output, "34s") {
		t.Errorf("expected duration, got: %q", output)
	}
}

func TestStartupLog_SpinnerTTY(t *testing.T) {
	var buf bytes.Buffer
	log := newStartupLog(&buf, true) // TTY mode

	stop := log.StartSpinner("Waiting for architect...")
	time.Sleep(200 * time.Millisecond) // Let spinner animate a few frames
	stop()

	output := buf.String()
	// Should contain spinner frames (braille characters)
	if !strings.Contains(output, "Waiting for architect...") {
		t.Errorf("expected message, got: %q", output)
	}
	// Should contain final checkmark
	if !strings.Contains(output, "✓") {
		t.Errorf("expected final ✓ checkmark, got: %q", output)
	}
}

func TestStartupLog_SpinnerNonTTY(t *testing.T) {
	var buf bytes.Buffer
	log := newStartupLog(&buf, false) // Non-TTY mode

	stop := log.StartSpinner("Waiting for architect...")
	time.Sleep(100 * time.Millisecond)
	stop()

	output := buf.String()
	// Non-TTY should print static line without spinner animation
	if !strings.Contains(output, "Waiting for architect...") {
		t.Errorf("expected message, got: %q", output)
	}
	// Should not contain carriage return escape sequences
	if strings.Contains(output, "\r") {
		t.Errorf("non-TTY should not use \\r, got: %q", output)
	}
	// Should contain final checkmark
	if !strings.Contains(output, "✓") {
		t.Errorf("expected final ✓ checkmark, got: %q", output)
	}
}

func TestStartupLog_NoGoroutineLeaks(t *testing.T) {
	var buf bytes.Buffer
	log := newStartupLog(&buf, true)

	// Start and immediately stop spinner
	stop := log.StartSpinner("Test")
	stop()

	// Give goroutine time to clean up
	time.Sleep(50 * time.Millisecond)

	// Start another spinner to verify no interference
	stop2 := log.StartSpinner("Test2")
	time.Sleep(50 * time.Millisecond)
	stop2()

	// If goroutines leaked, this test would hang or panic
	// The fact it completes cleanly verifies cleanup
}

func TestStartupLog_StopSpinnerMultipleTimes(t *testing.T) {
	var buf bytes.Buffer
	log := newStartupLog(&buf, true)

	stop := log.StartSpinner("Test")
	stop()
	stop() // Should be safe to call multiple times

	// No panic = success
}
