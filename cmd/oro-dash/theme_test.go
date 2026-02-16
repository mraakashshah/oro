package main

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestThemeColors(t *testing.T) {
	theme := DefaultTheme()

	// Test new hex color fields exist and are set
	tests := []struct {
		name  string
		color lipgloss.Color
		want  string
	}{
		// Status colors
		{"ColorReady", theme.ColorReady, "#6E56CF"},
		{"ColorInProgress", theme.ColorInProgress, "#E5A836"},
		{"ColorBlocked", theme.ColorBlocked, "#E5484D"},
		{"ColorDone", theme.ColorDone, "#30A46C"},

		// Priority colors
		{"ColorP0", theme.ColorP0, "#E5484D"},
		{"ColorP1", theme.ColorP1, "#E5A836"},
		{"ColorP2", theme.ColorP2, "#6E56CF"},
		{"ColorP3", theme.ColorP3, "#889096"},
		{"ColorP4", theme.ColorP4, "#687076"},

		// Chrome colors
		{"ColorBorder", theme.ColorBorder, "#3E4347"},
		{"ColorBg", theme.ColorBg, "#111113"},
		{"ColorFg", theme.ColorFg, "#EDEEF0"},

		// Heartbeat health colors
		{"ColorHealthy", theme.ColorHealthy, "#30A46C"},
		{"ColorWarn", theme.ColorWarn, "#E5A836"},
		{"ColorStale", theme.ColorStale, "#E5484D"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.color) != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, string(tt.color), tt.want)
			}
		})
	}
}

func TestThemeBackwardCompat(t *testing.T) {
	theme := DefaultTheme()

	// Test that legacy fields still exist and work
	tests := []struct {
		name  string
		color lipgloss.Color
	}{
		{"Primary", theme.Primary},
		{"Secondary", theme.Secondary},
		{"Success", theme.Success},
		{"Warning", theme.Warning},
		{"Error", theme.Error},
		{"Muted", theme.Muted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.color == "" {
				t.Errorf("%s is empty, expected a color value", tt.name)
			}
		})
	}
}

func TestThemeLegacyMapping(t *testing.T) {
	theme := DefaultTheme()

	// Verify that legacy fields map to sensible new colors
	// Primary should map to something (spec doesn't define exact mapping, but it should exist)
	if theme.Primary == "" {
		t.Error("Primary color should not be empty")
	}
	if theme.Secondary == "" {
		t.Error("Secondary color should not be empty")
	}
	if theme.Success == "" {
		t.Error("Success color should not be empty")
	}
	if theme.Warning == "" {
		t.Error("Warning color should not be empty")
	}
	if theme.Error == "" {
		t.Error("Error color should not be empty")
	}
	if theme.Muted == "" {
		t.Error("Muted color should not be empty")
	}
}
