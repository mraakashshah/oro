package main

import (
	"strings"
	"testing"
)

// TestDashboardShowsFocus verifies that renderFocusLine formats the focused
// epic correctly in the dashboard status output.
func TestDashboardShowsFocus(t *testing.T) {
	titles := map[string]string{
		"oro-abc": "My Epic Title",
		"oro-xyz": "Another Epic",
	}
	lookup := func(id string) string { return titles[id] }

	t.Run("focused epic with title shows id and title", func(t *testing.T) {
		got := renderFocusLine("oro-abc", lookup)
		if !strings.Contains(got, "Focused:") {
			t.Errorf("renderFocusLine() missing 'Focused:', got: %q", got)
		}
		if !strings.Contains(got, "oro-abc") {
			t.Errorf("renderFocusLine() missing epic id 'oro-abc', got: %q", got)
		}
		if !strings.Contains(got, "My Epic Title") {
			t.Errorf("renderFocusLine() missing title 'My Epic Title', got: %q", got)
		}
	})

	t.Run("empty focusedEpic returns empty string", func(t *testing.T) {
		got := renderFocusLine("", lookup)
		if got != "" {
			t.Errorf("renderFocusLine() with empty id = %q, want empty string", got)
		}
	})

	t.Run("epic not found shows id only", func(t *testing.T) {
		got := renderFocusLine("oro-unknown", lookup)
		if !strings.Contains(got, "Focused:") {
			t.Errorf("renderFocusLine() missing 'Focused:', got: %q", got)
		}
		if !strings.Contains(got, "oro-unknown") {
			t.Errorf("renderFocusLine() missing epic id 'oro-unknown', got: %q", got)
		}
	})
}
