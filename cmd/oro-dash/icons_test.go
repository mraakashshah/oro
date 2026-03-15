package main

import (
	"testing"
)

// TestTypeIconsAreEmoji verifies that bead type icons are descriptive emoji,
// not bracketed text like [T], [B], [F], or [E].
func TestTypeIconsAreEmoji(t *testing.T) {
	tests := map[string]struct {
		beadType string
		expected string
	}{
		"task emoji":    {"task", "✅"},
		"bug emoji":     {"bug", "🐛"},
		"feature emoji": {"feature", "✨"},
		"epic emoji":    {"epic", "🎯"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := renderTreeTypeIcon(tt.beadType)
			if got != tt.expected {
				t.Errorf("renderTreeTypeIcon(%q) = %q, want %q", tt.beadType, got, tt.expected)
			}

			// Verify it's actually an emoji (not plain ASCII or bracketed text)
			if len(got) == 0 {
				t.Errorf("renderTreeTypeIcon(%q) returned empty string", tt.beadType)
			}
			if got[0] == '[' || got[len(got)-1] == ']' {
				t.Errorf("renderTreeTypeIcon(%q) = %q still looks like bracketed text", tt.beadType, got)
			}
		})
	}
}

// TestDefaultTypeIconIsEmoji verifies the default icon for unknown types is also emoji.
func TestDefaultTypeIconIsEmoji(t *testing.T) {
	got := renderTreeTypeIcon("unknown")
	if got[0] == '[' || got[len(got)-1] == ']' {
		t.Errorf("renderTreeTypeIcon(\"unknown\") = %q is bracketed text, should be emoji", got)
	}
}
