package main

import "github.com/charmbracelet/lipgloss"

// Theme defines the visual styling for the oro dashboard.
type Theme struct {
	// Legacy fields (backward compatibility)
	Primary   lipgloss.Color
	Secondary lipgloss.Color
	Success   lipgloss.Color
	Warning   lipgloss.Color
	Error     lipgloss.Color
	Muted     lipgloss.Color

	// Status colors
	ColorReady      lipgloss.Color
	ColorInProgress lipgloss.Color
	ColorBlocked    lipgloss.Color
	ColorDone       lipgloss.Color

	// Priority colors
	ColorP0 lipgloss.Color
	ColorP1 lipgloss.Color
	ColorP2 lipgloss.Color
	ColorP3 lipgloss.Color
	ColorP4 lipgloss.Color

	// Chrome colors
	ColorBorder lipgloss.Color
	ColorBg     lipgloss.Color
	ColorFg     lipgloss.Color

	// Heartbeat health colors
	ColorHealthy lipgloss.Color
	ColorWarn    lipgloss.Color
	ColorStale   lipgloss.Color
}

// DefaultTheme returns the default theme for oro dash.
func DefaultTheme() Theme {
	return Theme{
		// Legacy fields (backward compatibility) - map to sensible new colors
		Primary:   lipgloss.Color("#6E56CF"), // Purple (maps to ColorP2)
		Secondary: lipgloss.Color("#E5A836"), // Amber (maps to ColorInProgress)
		Success:   lipgloss.Color("#30A46C"), // Green (maps to ColorDone)
		Warning:   lipgloss.Color("#E5A836"), // Amber (maps to ColorWarn)
		Error:     lipgloss.Color("#E5484D"), // Red (maps to ColorBlocked)
		Muted:     lipgloss.Color("#687076"), // Dim gray (maps to ColorP4)

		// Status colors
		ColorReady:      lipgloss.Color("#6E56CF"), // Purple
		ColorInProgress: lipgloss.Color("#E5A836"), // Amber
		ColorBlocked:    lipgloss.Color("#E5484D"), // Red
		ColorDone:       lipgloss.Color("#30A46C"), // Green

		// Priority colors
		ColorP0: lipgloss.Color("#E5484D"), // Critical — red
		ColorP1: lipgloss.Color("#E5A836"), // High — amber
		ColorP2: lipgloss.Color("#6E56CF"), // Medium — purple
		ColorP3: lipgloss.Color("#889096"), // Low — gray
		ColorP4: lipgloss.Color("#687076"), // Backlog — dim gray

		// Chrome colors
		ColorBorder: lipgloss.Color("#3E4347"),
		ColorBg:     lipgloss.Color("#111113"),
		ColorFg:     lipgloss.Color("#EDEEF0"),

		// Heartbeat health colors
		ColorHealthy: lipgloss.Color("#30A46C"), // Green — recent heartbeat
		ColorWarn:    lipgloss.Color("#E5A836"), // Amber — aging heartbeat
		ColorStale:   lipgloss.Color("#E5484D"), // Red — stale/dead worker
	}
}
