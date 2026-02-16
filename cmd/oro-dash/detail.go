package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"oro/pkg/protocol"
)

// DetailModel represents the detail drilldown view for a single bead.
type DetailModel struct {
	bead         protocol.BeadDetail
	activeTab    int
	tabs         []string
	workerEvents []WorkerEvent // Cached worker events for Worker tab
}

// newDetailModel creates a new DetailModel for the given bead.
func newDetailModel(bead protocol.BeadDetail) DetailModel {
	// Fetch worker events if worker is assigned
	var events []WorkerEvent
	if bead.WorkerID != "" {
		// Best-effort fetch (ignores errors, returns nil on failure)
		events, _ = fetchWorkerEvents(context.Background(), bead.WorkerID, 10)
	}

	return DetailModel{
		bead:         bead,
		activeTab:    0,
		tabs:         []string{"Overview", "Worker", "Diff", "Deps", "Memory"},
		workerEvents: events,
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

// renderWorkerTab renders the Worker tab with worker context %, heartbeat, and event history.
func (d DetailModel) renderWorkerTab() string {
	theme := DefaultTheme()

	// Edge case: no worker assigned → show 'Unassigned'
	if d.bead.WorkerID == "" {
		dimStyle := lipgloss.NewStyle().Foreground(theme.Muted).Italic(true)
		return dimStyle.Render("Unassigned")
	}

	var lines []string

	// Worker ID
	lines = append(lines, "Worker: "+d.bead.WorkerID)

	// Context percentage
	if d.bead.ContextPercent > 0 {
		lines = append(lines, "Context: "+fmt.Sprintf("%d", d.bead.ContextPercent)+" %")
	}

	// Last heartbeat
	if d.bead.LastHeartbeat != "" {
		lines = append(lines, "Last Heartbeat: "+d.bead.LastHeartbeat)
	}

	// Worker event history
	lines = append(lines, "", renderWorkerEvents(d.workerEvents, theme))

	return strings.Join(lines, "\n")
}

// renderDiffTab renders the Diff tab with git diff output.
func (d DetailModel) renderDiffTab() string {
	theme := DefaultTheme()

	// Edge case: no diff → show 'No changes'
	if d.bead.GitDiff == "" {
		dimStyle := lipgloss.NewStyle().Foreground(theme.Muted).Italic(true)
		return dimStyle.Render("No changes")
	}

	return d.bead.GitDiff
}

// renderDepsTab renders the Deps tab with dependency information.
func (d DetailModel) renderDepsTab() string {
	theme := DefaultTheme()

	// Edge case: bead has no deps → show 'No dependencies'
	// For now, BeadDetail doesn't have dependency fields, so always show placeholder
	dimStyle := lipgloss.NewStyle().Foreground(theme.Muted).Italic(true)
	return dimStyle.Render("No dependencies")
}

// renderMemoryTab renders the Memory tab with injected context.
func (d DetailModel) renderMemoryTab() string {
	theme := DefaultTheme()

	// Edge case: no memory → show 'No context'
	if d.bead.Memory == "" {
		dimStyle := lipgloss.NewStyle().Foreground(theme.Muted).Italic(true)
		return dimStyle.Render("No context")
	}

	return d.bead.Memory
}
