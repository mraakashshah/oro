package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
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
	bead              protocol.BeadDetail
	activeTab         int
	tabs              []string
	workerEvents      []WorkerEvent  // Cached worker events for Worker tab
	loadingEvents     bool           // True while events are being fetched asynchronously
	eventError        error          // Error from async worker events fetch
	tabViewport       viewport.Model // Viewport for scrollable tab content
	viewportActiveTab int            // Track which tab content is currently in viewport
	width             int            // Terminal width
	height            int            // Terminal height
	theme             Theme          // Pre-computed theme for render methods
	styles            Styles         // Pre-computed styles for render methods
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
func newDetailModel(bead protocol.BeadDetail, theme Theme, styles Styles) DetailModel {
	// Mark as loading if worker is assigned (events will be fetched asynchronously)
	loading := bead.WorkerID != ""

	// Initialize viewport with default dimensions
	vp := viewport.New(80, 20)

	d := DetailModel{
		bead:              bead,
		activeTab:         0,
		tabs:              []string{"Overview", "Worker", "Diff", "Deps", "Memory"},
		workerEvents:      nil,
		loadingEvents:     loading,
		eventError:        nil,
		tabViewport:       vp,
		viewportActiveTab: 0,
		width:             80,
		height:            24,
		theme:             theme,
		styles:            styles,
	}

	// Set initial viewport content
	d.tabViewport.SetContent(d.getActiveTabContent())

	return d
}

// nextTab moves to the next tab, wrapping around to the first tab if at the end.
func (d DetailModel) nextTab() DetailModel {
	d.activeTab = (d.activeTab + 1) % len(d.tabs)
	d.setViewportContent(d.getActiveTabContent())
	return d
}

// prevTab moves to the previous tab, wrapping around to the last tab if at the start.
func (d DetailModel) prevTab() DetailModel {
	d.activeTab = (d.activeTab - 1 + len(d.tabs)) % len(d.tabs)
	d.setViewportContent(d.getActiveTabContent())
	return d
}

// Update handles messages for DetailModel, including viewport scrolling and window resize.
//
//nolint:unparam // tea.Cmd return required by Bubble Tea Update interface
func (d DetailModel) Update(msg tea.Msg) (DetailModel, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Update dimensions
		d.width = msg.Width
		d.height = msg.Height

		// Resize viewport (accounting for tab bar and status bar)
		statusBarHeight := 2
		tabBarHeight := 2
		viewportHeight := msg.Height - statusBarHeight - tabBarHeight
		if viewportHeight < 1 {
			viewportHeight = 1
		}
		d.tabViewport.Width = msg.Width
		d.tabViewport.Height = viewportHeight

	case tea.KeyMsg:
		// Sync viewport content if tab changed (direct activeTab assignment)
		if d.viewportActiveTab != d.activeTab {
			d.tabViewport.SetContent(d.getActiveTabContent())
			d.tabViewport.GotoTop()
			d.viewportActiveTab = d.activeTab
		}

		// Handle viewport scrolling keys
		d.tabViewport, cmd = d.tabViewport.Update(msg)
	}

	return d, cmd
}

// setViewportContent updates the viewport with new content and resets scroll to top.
func (d *DetailModel) setViewportContent(content string) {
	d.tabViewport.SetContent(content)
	d.tabViewport.GotoTop()
	d.viewportActiveTab = d.activeTab
}

// getActiveTabContent returns the content for the currently active tab.
func (d DetailModel) getActiveTabContent() string {
	switch d.activeTab {
	case 0:
		return d.renderOverviewTab(d.styles)
	case 1:
		return d.renderWorkerTab(d.theme, d.styles)
	case 2:
		return d.renderDiffTab(d.styles)
	case 3:
		return d.renderDepsTab(d.styles)
	case 4:
		return d.renderMemoryTab(d.styles)
	default:
		return "Unknown tab"
	}
}

// View renders the detail view with tabs.
func (d DetailModel) View(styles Styles) string {
	// Render tab headers
	var tabHeaders []string
	for i, tab := range d.tabs {
		if i == d.activeTab {
			// Active tab - highlighted
			tabHeaders = append(tabHeaders, styles.Primary.Bold(true).Render("["+tab+"]"))
		} else {
			// Inactive tab - dimmed
			tabHeaders = append(tabHeaders, styles.Muted.Render(tab))
		}
	}

	header := strings.Join(tabHeaders, " ")

	// Prepare viewport for rendering
	vp := d.tabViewport

	// Sync viewport dimensions with model (for tests that set width/height directly)
	statusBarHeight := 2
	tabBarHeight := 2
	viewportHeight := d.height - statusBarHeight - tabBarHeight
	if viewportHeight < 1 {
		viewportHeight = 1
	}
	if vp.Width != d.width || vp.Height != viewportHeight {
		vp.Width = d.width
		vp.Height = viewportHeight
	}

	if d.viewportActiveTab != d.activeTab {
		// Tab was changed directly (e.g., in tests) - sync viewport content
		vp.SetContent(d.getActiveTabContent())
		vp.GotoTop()
	}

	// Render viewport content
	viewportContent := vp.View()

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		viewportContent,
	)
}

// renderOverviewTab renders the Overview tab with bead details.
func (d DetailModel) renderOverviewTab(styles Styles) string {
	var lines []string

	// Title and ID
	lines = append(lines,
		styles.DetailTitle.Render("Title: ")+d.bead.Title,
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
			styles.DetailBold.Render("Acceptance Criteria:"),
			d.bead.AcceptanceCriteria,
		)
	}

	return strings.Join(lines, "\n")
}

// renderWorkerTab renders the Worker tab with worker context %, heartbeat, and event history.
func (d DetailModel) renderWorkerTab(theme Theme, styles Styles) string {
	// Edge case: no worker assigned → show 'Unassigned'
	if d.bead.WorkerID == "" {
		return styles.DetailDimItalic.Render("Unassigned")
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
		lines = append(lines, "", styles.DetailDimItalic.Render("Loading events..."))
	case d.eventError != nil:
		lines = append(lines, "", styles.DetailError.Render("Error loading events: "+d.eventError.Error()))
	default:
		lines = append(lines, "", renderWorkerEvents(d.workerEvents, theme, styles))
	}

	return strings.Join(lines, "\n")
}

// renderDiffTab renders the Diff tab with git diff output.
func (d DetailModel) renderDiffTab(styles Styles) string {
	// Edge case: no diff → show 'No changes'
	if d.bead.GitDiff == "" {
		return styles.DetailDimItalic.Render("No changes")
	}

	return d.bead.GitDiff
}

// renderDepsTab renders the Deps tab with dependency information.
func (d DetailModel) renderDepsTab(styles Styles) string {
	// Edge case: bead has no deps → show 'No dependencies'
	// For now, BeadDetail doesn't have dependency fields, so always show placeholder
	return styles.DetailDimItalic.Render("No dependencies")
}

// renderMemoryTab renders the Memory tab with injected context.
func (d DetailModel) renderMemoryTab(styles Styles) string {
	// Edge case: no memory → show 'No context'
	if d.bead.Memory == "" {
		return styles.DetailDimItalic.Render("No context")
	}

	return d.bead.Memory
}
