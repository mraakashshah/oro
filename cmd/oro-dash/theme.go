package main

import "github.com/charmbracelet/lipgloss"

// Theme defines the visual styling for the oro dashboard.
type Theme struct {
	Primary   lipgloss.Color
	Secondary lipgloss.Color
	Success   lipgloss.Color
	Warning   lipgloss.Color
	Error     lipgloss.Color
	Muted     lipgloss.Color
}

// DefaultTheme returns the default theme for oro dash.
func DefaultTheme() Theme {
	return Theme{
		Primary:   lipgloss.Color("12"),  // Blue
		Secondary: lipgloss.Color("14"),  // Cyan
		Success:   lipgloss.Color("10"),  // Green
		Warning:   lipgloss.Color("11"),  // Yellow
		Error:     lipgloss.Color("9"),   // Red
		Muted:     lipgloss.Color("240"), // Gray
	}
}
