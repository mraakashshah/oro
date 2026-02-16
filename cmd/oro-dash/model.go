package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"oro/pkg/protocol"
)

// tickMsg is sent by Bubble Tea on every tick interval.
// Used to trigger periodic data refresh from bd CLI and dispatcher state.
type tickMsg time.Time

// beadsMsg carries fetched beads from the bd CLI.
type beadsMsg []protocol.Bead

// workersMsg carries fetched worker status from the dispatcher.
// nil means the daemon is offline.
type workersMsg []WorkerStatus

// tickCmd returns a command that sends a tickMsg after 2 seconds.
func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// fetchBeadsCmd returns a tea.Cmd that fetches beads from the bd CLI.
func fetchBeadsCmd() tea.Cmd {
	return func() tea.Msg {
		beads, _ := fetchBeads(context.Background())
		return beadsMsg(beads)
	}
}

// fetchWorkersCmd returns a tea.Cmd that fetches worker status from the dispatcher.
func fetchWorkersCmd() tea.Cmd {
	return func() tea.Msg {
		socketPath := defaultSocketPath()
		workers, _ := fetchWorkerStatus(context.Background(), socketPath)
		return workersMsg(workers)
	}
}

// defaultSocketPath returns the dispatcher socket path from env or default.
func defaultSocketPath() string {
	if v := os.Getenv("ORO_SOCKET_PATH"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, protocol.OroDir, "oro.sock")
}

// ViewType represents different views in the dashboard.
type ViewType int

const (
	// BoardView shows the bead board.
	BoardView ViewType = iota
	// InsightsView shows dependency graph analysis.
	InsightsView
	// DetailView shows detailed information about a single bead.
	DetailView
	// SearchView shows the search overlay.
	SearchView
)

// Model is the Bubble Tea model for the oro dashboard.
type Model struct {
	activeView      ViewType
	daemonHealthy   bool
	workerCount     int
	openCount       int
	inProgressCount int

	// Data fetched from external sources
	beads   []protocol.Bead
	workers []WorkerStatus

	// UI state
	width  int
	height int
	//nolint:unused // Will be used for error display
	err error

	// Kanban navigation state
	activeCol  int // Index of the active column (0-3: Ready, In Progress, Blocked, Done)
	activeBead int // Index of the active bead within the current column

	// Detail view state
	detailModel *DetailModel // Set when drilling down into a bead

	// Search view state
	searchQuery         string // Current search query
	searchSelectedIndex int    // Index of the selected search result
	searchModel         *SearchModel
}

// newModel creates a new Model initialized with BoardView active.
func newModel() Model {
	return Model{
		activeView:  BoardView,
		searchModel: &SearchModel{},
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(fetchBeadsCmd(), fetchWorkersCmd(), tickCmd())
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKeyPress(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case beadsMsg:
		m.beads = []protocol.Bead(msg)
		m.openCount = 0
		m.inProgressCount = 0
		for _, b := range m.beads {
			switch b.Status {
			case "open":
				m.openCount++
			case "in_progress":
				m.inProgressCount++
			}
		}

	case workersMsg:
		if msg == nil {
			m.daemonHealthy = false
			m.workerCount = 0
		} else {
			m.daemonHealthy = true
			m.workers = []WorkerStatus(msg)
			m.workerCount = len(m.workers)
		}

	case tickMsg:
		return m, tea.Batch(fetchBeadsCmd(), fetchWorkersCmd(), tickCmd())
	}

	return m, nil
}

// handleKeyPress processes keyboard input and returns updated model with optional command.
func (m Model) handleKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Global keys (work in all views except SearchView where text input is active)
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if key == "q" && m.activeView != SearchView {
		return m, tea.Quit
	}

	// View-specific key handling
	switch m.activeView {
	case DetailView:
		return m.handleDetailViewKeys(key)
	case InsightsView:
		return m.handleInsightsViewKeys(key)
	case SearchView:
		return m.handleSearchViewKeys(key, msg)
	default: // BoardView
		return m.handleBoardViewKeys(key)
	}
}

// handleDetailViewKeys processes keyboard input in DetailView.
func (m Model) handleDetailViewKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "backspace":
		m.activeView = BoardView
		m.detailModel = nil
	case "tab":
		if m.detailModel != nil {
			*m.detailModel = m.detailModel.nextTab()
		}
	case "shift+tab":
		if m.detailModel != nil {
			*m.detailModel = m.detailModel.prevTab()
		}
	}
	return m, nil
}

// handleInsightsViewKeys processes keyboard input in InsightsView.
func (m Model) handleInsightsViewKeys(key string) (tea.Model, tea.Cmd) {
	if key == "esc" {
		m.activeView = BoardView
	}
	return m, nil
}

// handleBoardViewKeys processes keyboard input in BoardView.
func (m Model) handleBoardViewKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter":
		m = m.drillDownToDetail()
	case "h":
		m = m.moveToPrevColumn()
	case "l":
		m = m.moveToNextColumn()
	case "tab":
		m = m.moveToNextColumn()
	case "shift+tab":
		m = m.moveToPrevColumn()
	case "j", "down":
		m = m.moveToNextBead()
	case "k", "up":
		m = m.moveToPrevBead()
	case "i":
		m.activeView = InsightsView
	case "/":
		m.activeView = SearchView
		m.searchQuery = ""
		m.searchSelectedIndex = 0
	}
	return m, nil
}

// handleSearchViewKeys processes keyboard input in SearchView.
func (m Model) handleSearchViewKeys(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.activeView = BoardView
		m.searchQuery = ""
		m.searchSelectedIndex = 0
	case "enter":
		// Navigate to detail view for selected search result
		filtered := m.filterBeads()
		if len(filtered) > 0 && m.searchSelectedIndex < len(filtered) {
			selectedBead := filtered[m.searchSelectedIndex]
			beadDetail := protocol.BeadDetail{
				ID:                 selectedBead.ID,
				Title:              selectedBead.Title,
				AcceptanceCriteria: selectedBead.AcceptanceCriteria,
				Model:              selectedBead.Model,
			}
			dm := newDetailModel(beadDetail)
			m.detailModel = &dm
			m.activeView = DetailView
		}
	case "down", "j":
		filtered := m.filterBeads()
		if len(filtered) > 0 && m.searchSelectedIndex < len(filtered)-1 {
			m.searchSelectedIndex++
		}
	case "up", "k":
		if m.searchSelectedIndex > 0 {
			m.searchSelectedIndex--
		}
	case "backspace":
		if m.searchQuery != "" {
			runes := []rune(m.searchQuery)
			m.searchQuery = string(runes[:len(runes)-1])
			// Reset selection when query changes
			m.searchSelectedIndex = 0
		}
	default:
		// Handle text input
		if len(msg.Runes) > 0 {
			m.searchQuery += string(msg.Runes)
			// Reset selection when query changes
			m.searchSelectedIndex = 0
		}
	}
	return m, nil
}

// filterBeads filters beads based on the current search query.
// Converts protocol.Bead to local Bead type for SearchModel.Filter,
// then maps results back via ID index for O(n) lookup.
func (m Model) filterBeads() []protocol.Bead {
	if m.searchQuery == "" {
		return m.beads
	}

	// Build index and local slice in one pass
	index := make(map[string]protocol.Bead, len(m.beads))
	localBeads := make([]Bead, len(m.beads))
	for i, pb := range m.beads {
		index[pb.ID] = pb
		localBeads[i] = Bead{
			ID:       pb.ID,
			Title:    pb.Title,
			Status:   pb.Status,
			Priority: pb.Priority,
			Type:     pb.Type,
		}
	}

	// Filter using SearchModel
	filtered := m.searchModel.Filter(localBeads, m.searchQuery)

	// Map back via index — O(n) not O(n*m)
	result := make([]protocol.Bead, 0, len(filtered))
	for _, fb := range filtered {
		if pb, ok := index[fb.ID]; ok {
			result = append(result, pb)
		}
	}

	return result
}

// View implements tea.Model.
func (m Model) View() string {
	statusBar := m.renderStatusBar()

	switch m.activeView {
	case InsightsView:
		insights := m.buildInsightsModel()
		return statusBar + "\n" + insights.Render()
	case DetailView:
		if m.detailModel != nil {
			return statusBar + "\n" + m.detailModel.View()
		}
		// Fallback to board if detailModel is nil
		board := NewBoardModel(m.beads)
		return statusBar + "\n" + board.RenderWithCursor(m.activeCol, m.activeBead)
	case SearchView:
		return statusBar + "\n" + m.renderSearchOverlay()
	default:
		board := NewBoardModel(m.beads)
		return statusBar + "\n" + board.RenderWithCursor(m.activeCol, m.activeBead)
	}
}

// buildInsightsModel creates an InsightsModel from the current beads.
func (m Model) buildInsightsModel() *InsightsModel {
	beadsWithDeps := make([]BeadWithDeps, len(m.beads))
	for i, b := range m.beads {
		// Extract DependsOn IDs from Dependencies
		var dependsOn []string
		for _, dep := range b.Dependencies {
			// Only include "blocks" type dependencies
			if dep.Type == "blocks" {
				dependsOn = append(dependsOn, dep.DependsOnID)
			}
		}

		beadsWithDeps[i] = BeadWithDeps{
			ID:              b.ID,
			Priority:        b.Priority,
			Type:            b.Type,
			DaysSinceUpdate: 0, // TODO: calculate from updated timestamp
			DependsOn:       dependsOn,
		}
	}
	return NewInsightsModel(beadsWithDeps)
}

// renderStatusBar renders the status bar with daemon health, worker count, and aggregate stats.
func (m Model) renderStatusBar() string {
	theme := DefaultTheme()

	var daemonStatus string
	if m.daemonHealthy {
		daemonStatus = lipgloss.NewStyle().Foreground(theme.Success).Render("daemon: online")
	} else {
		daemonStatus = lipgloss.NewStyle().Foreground(theme.Error).Render("daemon: offline")
	}

	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		daemonStatus,
		lipgloss.NewStyle().Render(" | Workers: "),
		lipgloss.NewStyle().Foreground(theme.Primary).Render(fmt.Sprintf("%d", m.workerCount)),
		lipgloss.NewStyle().Render(" | Open: "),
		lipgloss.NewStyle().Foreground(theme.Warning).Render(fmt.Sprintf("%d", m.openCount)),
		lipgloss.NewStyle().Render(" | In Progress: "),
		lipgloss.NewStyle().Foreground(theme.Success).Render(fmt.Sprintf("%d", m.inProgressCount)),
	)
}

// renderSearchOverlay renders the search overlay with text input and filtered results.
func (m Model) renderSearchOverlay() string {
	theme := DefaultTheme()
	title := m.renderSearchTitle(theme)
	searchInput := m.renderSearchInput(theme)
	helpText := m.renderSearchHelp(theme)
	results := m.renderSearchResults(theme)

	return lipgloss.JoinVertical(lipgloss.Left, title, searchInput, helpText, results)
}

// renderSearchTitle renders the search overlay title.
func (m Model) renderSearchTitle(theme Theme) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.Primary).Padding(1, 0)
	return titleStyle.Render("Search Beads")
}

// renderSearchInput renders the search input field with current query.
func (m Model) renderSearchInput(theme Theme) string {
	inputStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Primary).
		Padding(0, 1).
		Width(60)
	searchPrompt := "Query: " + m.searchQuery + "▌"
	return inputStyle.Render(searchPrompt)
}

// renderSearchHelp renders the help text for search overlay.
func (m Model) renderSearchHelp(theme Theme) string {
	helpStyle := lipgloss.NewStyle().Foreground(theme.Muted).Padding(1, 0)
	return helpStyle.Render("Use p:N, s:STATUS, t:TYPE filters or fuzzy search. ↑↓ navigate, Enter to view, Esc to cancel")
}

// renderSearchResults renders the list of filtered search results.
func (m Model) renderSearchResults(theme Theme) string {
	filtered := m.filterBeads()
	resultsStyle := lipgloss.NewStyle().Padding(1, 0)

	if len(filtered) == 0 {
		return m.renderNoResults(theme, resultsStyle)
	}

	return m.renderResultsList(theme, filtered, resultsStyle)
}

// renderNoResults renders the "no results" message.
func (m Model) renderNoResults(theme Theme, resultsStyle lipgloss.Style) string {
	noResultsStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	return resultsStyle.Render(noResultsStyle.Render("No matching beads"))
}

// renderResultsList renders the list of search results with highlighting.
func (m Model) renderResultsList(theme Theme, filtered []protocol.Bead, resultsStyle lipgloss.Style) string {
	const maxResults = 10
	totalCount := len(filtered)

	if len(filtered) > maxResults {
		filtered = filtered[:maxResults]
	}

	var resultsBuilder strings.Builder
	for i, bead := range filtered {
		resultsBuilder.WriteString(m.renderSearchResultLine(theme, i, bead))
		resultsBuilder.WriteString("\n")
	}

	if totalCount > maxResults {
		moreStyle := lipgloss.NewStyle().Foreground(theme.Muted)
		resultsBuilder.WriteString(moreStyle.Render(fmt.Sprintf("  ... and %d more", totalCount-maxResults)))
	}

	return resultsStyle.Render(resultsBuilder.String())
}

// renderSearchResultLine renders a single search result line with optional highlighting.
func (m Model) renderSearchResultLine(theme Theme, index int, bead protocol.Bead) string {
	if index == m.searchSelectedIndex {
		highlightStyle := lipgloss.NewStyle().
			Background(theme.Primary).
			Foreground(lipgloss.Color("#ffffff")).
			Bold(true).
			Padding(0, 1)
		return highlightStyle.Render(fmt.Sprintf("▸ %s - %s", bead.ID, bead.Title))
	}

	idStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	return fmt.Sprintf("  %s - %s", idStyle.Render(bead.ID), bead.Title)
}

// moveToNextColumn moves the cursor to the next non-empty column (wraps/clamps at boundary).
func (m Model) moveToNextColumn() Model {
	board := NewBoardModel(m.beads)
	startCol := m.activeCol

	// Try to find next non-empty column
	for i := 1; i <= len(board.columns); i++ {
		nextCol := m.activeCol + i
		if nextCol >= len(board.columns) {
			// Clamp at last column
			return m
		}

		if len(board.columns[nextCol].beads) > 0 {
			m.activeCol = nextCol
			m.activeBead = 0 // Reset to first bead in new column
			return m
		}
	}

	// All remaining columns are empty, stay at current position
	if startCol == m.activeCol && len(board.columns[startCol].beads) == 0 {
		// Current column is also empty, stay put
		return m
	}

	return m
}

// moveToPrevColumn moves the cursor to the previous non-empty column (wraps/clamps at boundary).
func (m Model) moveToPrevColumn() Model {
	board := NewBoardModel(m.beads)

	// Clamp at first column
	if m.activeCol <= 0 {
		return m
	}

	// Try to find previous non-empty column
	for i := 1; i <= m.activeCol; i++ {
		prevCol := m.activeCol - i
		if prevCol < 0 {
			// Clamp at first column
			return m
		}

		if len(board.columns[prevCol].beads) > 0 {
			m.activeCol = prevCol
			m.activeBead = 0 // Reset to first bead in new column
			return m
		}
	}

	// All previous columns are empty, stay at current position
	return m
}

// moveToNextBead moves the cursor to the next bead in the current column (clamps at boundary).
func (m Model) moveToNextBead() Model {
	board := NewBoardModel(m.beads)
	if m.activeCol >= len(board.columns) {
		return m
	}

	col := board.columns[m.activeCol]
	if len(col.beads) == 0 {
		return m
	}

	// Clamp at last bead
	if m.activeBead < len(col.beads)-1 {
		m.activeBead++
	}

	return m
}

// moveToPrevBead moves the cursor to the previous bead in the current column (clamps at boundary).
func (m Model) moveToPrevBead() Model {
	board := NewBoardModel(m.beads)
	if m.activeCol >= len(board.columns) {
		return m
	}

	col := board.columns[m.activeCol]
	if len(col.beads) == 0 {
		return m
	}

	// Clamp at first bead
	if m.activeBead > 0 {
		m.activeBead--
	}

	return m
}

// drillDownToDetail transitions to DetailView for the selected bead.
// Returns unchanged model if no bead is selected (empty column).
func (m Model) drillDownToDetail() Model {
	board := NewBoardModel(m.beads)
	if m.activeCol >= len(board.columns) {
		return m
	}

	col := board.columns[m.activeCol]
	if len(col.beads) == 0 || m.activeBead >= len(col.beads) {
		// No beads in column or invalid bead index
		return m
	}

	// Get the selected bead
	selectedBead := col.beads[m.activeBead]

	// Convert protocol.Bead to protocol.BeadDetail
	beadDetail := protocol.BeadDetail{
		ID:                 selectedBead.ID,
		Title:              selectedBead.Title,
		AcceptanceCriteria: selectedBead.AcceptanceCriteria,
		Model:              selectedBead.Model,
		// Other fields would be populated from fetched data in a real implementation
	}

	// Create detail model
	m.detailModel = &DetailModel{}
	*m.detailModel = newDetailModel(beadDetail)
	m.activeView = DetailView

	return m
}
