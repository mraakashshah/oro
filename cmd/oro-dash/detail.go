package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"oro/pkg/protocol"
)

// workerEventsMsg carries the result of async worker events fetch.
type workerEventsMsg struct {
	events []WorkerEvent
	err    error
}

// DetailModel represents the detail drilldown view for a single bead.
type DetailModel struct {
	bead          protocol.BeadDetail
	activeTab     int
	tabs          []string
	workerEvents  []WorkerEvent // Cached worker events for Worker tab
	loadingEvents bool          // True while events are being fetched asynchronously
	eventError    error         // Error from async worker events fetch
}

// fetchWorkerEventsCmd returns a tea.Cmd that fetches worker events asynchronously.
// Fetches with a 2-second timeout to prevent indefinite hang.
func fetchWorkerEventsCmd(workerID string) tea.Cmd {
	return func() tea.Msg {
		if workerID == "" {
			return workerEventsMsg{events: nil, err: nil}
		}

		// Create context with timeout to prevent indefinite hang
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		events, err := fetchWorkerEvents(ctx, workerID, 10)
		return workerEventsMsg{events: events, err: err}
	}
}

// newDetailModel creates a new DetailModel for the given bead.
// Worker events are fetched asynchronously - use fetchWorkerEventsCmd to initiate the fetch.
func newDetailModel(bead protocol.BeadDetail) DetailModel {
	// Mark as loading if worker is assigned (events will be fetched asynchronously)
	loading := bead.WorkerID != ""

	return DetailModel{
		bead:          bead,
		activeTab:     0,
		tabs:          []string{"Overview", "Worker", "Diff", "Deps", "Memory"},
		workerEvents:  nil,
		loadingEvents: loading,
		eventError:    nil,
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

	// Worker event history - show loading state or error
	switch {
	case d.loadingEvents:
		dimStyle := lipgloss.NewStyle().Foreground(theme.Muted).Italic(true)
		lines = append(lines, "", dimStyle.Render("Loading events..."))
	case d.eventError != nil:
		errorStyle := lipgloss.NewStyle().Foreground(theme.Error)
		lines = append(lines, "", errorStyle.Render("Error loading events: "+d.eventError.Error()))
	default:
		lines = append(lines, "", renderWorkerEvents(d.workerEvents, theme))
	}

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
