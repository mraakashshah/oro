package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"oro/pkg/protocol"
)

// DetailModel represents the detail drilldown view for a single bead.
type DetailModel struct {
	bead      protocol.BeadDetail
	activeTab int
	tabs      []string
}

// newDetailModel creates a new DetailModel for the given bead.
func newDetailModel(bead protocol.BeadDetail) DetailModel {
	return DetailModel{
		bead:      bead,
		activeTab: 0,
		tabs:      []string{"Overview", "Worker", "Diff", "Deps", "Memory"},
	}
}

// nextTab moves to the next tab, wrapping around to the first tab if at the end.
func (d DetailModel) nextTab() DetailModel {
	d.activeTab = (d.activeTab + 1) % len(d.tabs)
	return d
}

// prevTab moves to the previous tab, wrapping around to the last tab if at the start.
func (d DetailModel) prevTab() DetailModel {
	d.activeTab = (d.activeTab - 1 + len(d.tabs)) % len(d.tabs)
	return d
}

// View renders the detail view with tabs.
func (d DetailModel) View() string {
	theme := DefaultTheme()

	// Render tab headers
	var tabHeaders []string
	for i, tab := range d.tabs {
		if i == d.activeTab {
			// Active tab - highlighted
			style := lipgloss.NewStyle().
				Foreground(theme.Primary).
				Bold(true)
			tabHeaders = append(tabHeaders, style.Render("["+tab+"]"))
		} else {
			// Inactive tab - dimmed
			style := lipgloss.NewStyle().
				Foreground(theme.Muted)
			tabHeaders = append(tabHeaders, style.Render(tab))
		}
	}

	header := strings.Join(tabHeaders, " ")

	// Render active tab content
	var content string
	switch d.activeTab {
	case 0:
		content = d.renderOverviewTab()
	case 1:
		content = d.renderWorkerTab()
	case 2:
		content = d.renderDiffTab()
	case 3:
		content = d.renderDepsTab()
	case 4:
		content = d.renderMemoryTab()
	default:
		content = "Unknown tab"
	}

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		content,
	)
}

// renderOverviewTab renders the Overview tab with bead details.
func (d DetailModel) renderOverviewTab() string {
	theme := DefaultTheme()

	var lines []string

	// Title and ID
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.Primary)
	lines = append(lines,
		titleStyle.Render("Title: ")+d.bead.Title,
		"ID: "+d.bead.ID,
	)

	// Model (if specified)
	if d.bead.Model != "" {
		lines = append(lines, "Model: "+d.bead.Model)
	}

	// Acceptance Criteria
	if d.bead.AcceptanceCriteria != "" {
		lines = append(lines,
			"",
			lipgloss.NewStyle().Bold(true).Render("Acceptance Criteria:"),
			d.bead.AcceptanceCriteria,
		)
	}

	return strings.Join(lines, "\n")
}

// renderWorkerTab renders the Worker tab (placeholder for now).
func (d DetailModel) renderWorkerTab() string {
	return "Worker tab - not yet implemented"
}

// renderDiffTab renders the Diff tab (placeholder for now).
func (d DetailModel) renderDiffTab() string {
	return "Diff tab - not yet implemented"
}

// renderDepsTab renders the Deps tab with dependency information.
func (d DetailModel) renderDepsTab() string {
	theme := DefaultTheme()

	// Edge case: bead has no deps → show 'No dependencies'
	// For now, BeadDetail doesn't have dependency fields, so always show placeholder
	dimStyle := lipgloss.NewStyle().Foreground(theme.Muted).Italic(true)
	return dimStyle.Render("No dependencies")
}

// renderMemoryTab renders the Memory tab (placeholder for now).
func (d DetailModel) renderMemoryTab() string {
	return "Memory tab - not yet implemented"
}
