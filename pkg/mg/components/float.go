package components

import (
	"oro/pkg/mg"

	"charm.land/lipgloss/v2"
)

// Float renders a decorative ASCII parade float. Stub for v1.
//
//oro:testonly
func Float(title string, width int) string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(mg.BrightGold).
		Foreground(mg.BrightPurple).
		Bold(true).
		Width(width-4).
		Align(lipgloss.Center).
		Padding(0, 1)

	return style.Render(mg.FleurDeLis + " " + title + " " + mg.FleurDeLis)
}
