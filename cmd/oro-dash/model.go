package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"oro/pkg/protocol"
)

// tickMsg is sent by Bubble Tea on every tick interval.
// Used to trigger periodic data refresh from bd CLI and dispatcher state.
type tickMsg time.Time

// beadsMsg carries fetched beads from the bd CLI.
type beadsMsg []protocol.Bead

// workerDataMsg carries both worker status and assignments from the dispatcher.
type workerDataMsg struct {
	workers     []WorkerStatus
	assignments map[string]string
}

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

// fetchWorkersCmd returns a tea.Cmd that fetches worker status and assignments from the dispatcher.
func fetchWorkersCmd() tea.Cmd {
	return func() tea.Msg {
		socketPath := defaultSocketPath()
		workers, assignments, _ := fetchWorkerStatus(context.Background(), socketPath)
		return workerDataMsg{
			workers:     workers,
			assignments: assignments,
		}
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
	// HelpView shows the help overlay.
	HelpView
	// HealthView shows system health status.
	HealthView
	// WorkersView shows the workers table.
	WorkersView
)

// Model is the Bubble Tea model for the oro dashboard.
type Model struct {
	activeView      ViewType
	previousView    ViewType // View to return to when help is dismissed
	daemonHealthy   bool
	workerCount     int
	openCount       int
	inProgressCount int

	// Data fetched from external sources
	beads       []protocol.Bead
	workers     []WorkerStatus
	assignments map[string]string // bead ID -> worker ID
	healthData  *HealthData       // Health data from dispatcher

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
	searchInput         textinput.Model // Bubbles textinput for search query
	searchQuery         string          // Current search query (deprecated, use searchInput.Value())
	searchSelectedIndex int             // Index of the selected search result
	searchModel         *SearchModel

	// Pre-computed styles to avoid allocations during rendering
	theme  Theme
	styles Styles

	// Split pane state
	splitRatio float64 // Ratio of board width in split view (0.2 - 0.8, default 0.4)
}

// newModel creates a new Model initialized with BoardView active.
func newModel() Model {
	theme := DefaultTheme()
	ti := textinput.New()
	ti.Placeholder = "Search by ID, title, or filter (p:0, s:open, t:bug)"
	ti.CharLimit = 100
	return Model{
		activeView:  BoardView,
		searchInput: ti,
		searchModel: &SearchModel{},
		theme:       theme,
		styles:      NewStyles(theme),
		splitRatio:  0.4, // Default 40% board, 60% detail
	}
}

// calculateColumnWidth returns the column width based on terminal width.
// Divides terminal width by 4 (number of columns) with a minimum floor of 18.
func (m Model) calculateColumnWidth() int {
	const minWidth = 18
	const numColumns = 4

	colWidth := m.width / numColumns
	if colWidth < minWidth {
		return minWidth
	}
	return colWidth
}

// calculateSearchInputWidth returns the search input width based on terminal width.
// Uses most of terminal width minus padding, with min/max constraints.
func (m Model) calculateSearchInputWidth() int {
	const minSearchWidth = 40
	const maxSearchWidth = 120
	const padding = 4

	width := m.width - padding
	if width < minSearchWidth {
		return minSearchWidth
	}
	if width > maxSearchWidth {
		return maxSearchWidth
	}
	return width
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	// Start watching .beads/ directory for changes (falls back to polling if unavailable)
	watchCmd := watchBeadsDir(".beads")
	if watchCmd != nil {
		return tea.Batch(fetchBeadsCmd(), fetchWorkersCmd(), tickCmd(), watchCmd)
	}
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
		// Clamp cursor position to ensure it's valid after bead data refresh
		m = m.clampCursor()

	case workerDataMsg:
		if msg.workers == nil {
			m.daemonHealthy = false
			m.workerCount = 0
			m.assignments = nil
		} else {
			m.daemonHealthy = true
			m.workers = msg.workers
			m.workerCount = len(msg.workers)
			m.assignments = msg.assignments
		}

	case workerEventsMsg:
		// Update detail model with fetched worker events
		if m.detailModel != nil {
			m.detailModel.workerEvents = msg.events
			m.detailModel.eventError = msg.err
			m.detailModel.loadingEvents = false
		}

	case tickMsg:
		return m, tea.Batch(fetchBeadsCmd(), fetchWorkersCmd(), tickCmd())

	case fsChangeMsg:
		// File change detected in .beads/ - fetch immediately instead of waiting for tick
		return m, fetchBeadsCmd()
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
	if key == "q" && m.activeView != SearchView && m.activeView != HelpView {
		return m, tea.Quit
	}

	// Toggle help with ? (except in SearchView where it's text input)
	if key == "?" && m.activeView != SearchView {
		if m.activeView == HelpView {
			// Dismiss help, return to previous view
			m.activeView = m.previousView
			return m, nil
		}
		// Open help, save current view
		m.previousView = m.activeView
		m.activeView = HelpView
		return m, nil
	}

	// View-specific key handling
	switch m.activeView {
	case HelpView:
		return m.handleHelpViewKeys(key)
	case DetailView:
		return m.handleDetailViewKeys(key)
	case InsightsView:
		return m.handleInsightsViewKeys(key)
	case SearchView:
		return m.handleSearchViewKeys(key, msg)
	case HealthView:
		return m.handleHealthViewKeys(key)
	case WorkersView:
		return m.handleWorkersViewKeys(key)
	default: // BoardView
		return m.handleBoardViewKeys(key)
	}
}

// handleHelpViewKeys processes keyboard input in HelpView.
func (m Model) handleHelpViewKeys(key string) (tea.Model, tea.Cmd) {
	if key == "esc" {
		m.activeView = m.previousView
	}
	return m, nil
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
	case "<":
		// Decrease board width (increase detail width)
		m.splitRatio -= 0.1
		if m.splitRatio < 0.2 {
			m.splitRatio = 0.2
		}
	case ">":
		// Increase board width (decrease detail width)
		m.splitRatio += 0.1
		if m.splitRatio > 0.8 {
			m.splitRatio = 0.8
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
	var cmd tea.Cmd
	switch key {
	case "enter":
		m, cmd = m.drillDownToDetail()
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
		m.searchInput.Focus()
		m.searchInput.SetValue("")
		m.searchQuery = ""
		m.searchSelectedIndex = 0
	case "w":
		m.activeView = WorkersView
	}
	return m, cmd
}

// handleSearchViewKeys processes keyboard input in SearchView.
func (m Model) handleSearchViewKeys(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch key {
	case "esc":
		m.activeView = BoardView
		m.searchInput.Blur()
		m.searchInput.SetValue("")
		m.searchQuery = ""
		m.searchSelectedIndex = 0
		return m, nil
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
			dm := newDetailModel(beadDetail, m.theme, m.styles)
			m.detailModel = &dm
			m.activeView = DetailView
			// Initiate async worker events fetch
			return m, fetchWorkerEventsCmd(beadDetail.WorkerID)
		}
		return m, nil
	case "down", "j":
		filtered := m.filterBeads()
		if len(filtered) > 0 && m.searchSelectedIndex < len(filtered)-1 {
			m.searchSelectedIndex++
		}
	case "up", "k":
		if m.searchSelectedIndex > 0 {
			m.searchSelectedIndex--
		}
	default:
		// Delegate all other input to textinput (handles character input, backspace, cursor movement, etc.)
		oldValue := m.searchInput.Value()
		m.searchInput, cmd = m.searchInput.Update(msg)
		// Reset selection when query changes
		if m.searchInput.Value() != oldValue {
			m.searchSelectedIndex = 0
		}
	}
	return m, cmd
}

// filterBeads filters beads based on the current search query.
// Converts protocol.Bead to local Bead type for SearchModel.Filter,
// then maps results back via ID index for O(n) lookup.
func (m Model) filterBeads() []protocol.Bead {
	query := m.searchInput.Value()
	if query == "" {
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
	filtered := m.searchModel.Filter(localBeads, query)

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
	statusBar := m.renderStatusBar(m.width)

	switch m.activeView {
	case HelpView:
		return m.renderHelpOverlay() + "\n" + statusBar
	case InsightsView:
		insights := m.buildInsightsModel()
		return insights.Render() + "\n" + statusBar
	case DetailView:
		if m.detailModel != nil {
			// Use split pane if terminal is wide enough
			if m.width >= 80 {
				return m.renderSplitPane() + "\n" + statusBar
			}
			// Narrow terminal: show detail only
			return m.detailModel.View(m.styles) + "\n" + statusBar
		}
		// Fallback to board if detailModel is nil
		board := NewBoardModelWithWorkers(m.beads, m.workers, m.assignments)
		colWidth := m.calculateColumnWidth()
		return board.RenderWithCustomWidth(m.activeCol, m.activeBead, colWidth, m.theme, m.styles) + "\n" + statusBar
	case SearchView:
		return m.renderSearchOverlay() + "\n" + statusBar
	case HealthView:
		return m.renderHealthView() + "\n" + statusBar
	case WorkersView:
		workersTable := NewWorkersTableModel(m.workers, m.assignments)
		return workersTable.View(m.theme, m.styles) + "\n" + statusBar
	default:
		board := NewBoardModelWithWorkers(m.beads, m.workers, m.assignments)
		colWidth := m.calculateColumnWidth()
		return board.RenderWithCustomWidth(m.activeCol, m.activeBead, colWidth, m.theme, m.styles) + "\n" + statusBar
	}
}

// renderSplitPane renders DetailView as a split pane with board on left and detail on right.
func (m Model) renderSplitPane() string {
	// Calculate widths based on splitRatio
	boardWidth := int(float64(m.width) * m.splitRatio)
	_ = m.width - boardWidth // detailWidth reserved for future use

	// Render board with muted colors and reduced column width
	board := NewBoardModelWithWorkers(m.beads, m.workers, m.assignments)
	// Adjust column width based on available board space
	colWidth := max((boardWidth/4)-2, 20) // 4 columns with some padding, min 20
	boardView := board.RenderWithCustomWidth(m.activeCol, m.activeBead, colWidth, m.theme, m.styles)

	// Render detail view with remaining width
	detailView := m.detailModel.View(m.styles)

	// Join horizontally
	return lipgloss.JoinHorizontal(lipgloss.Top, boardView, detailView)
}

// calculateDaysSinceUpdate calculates days since the bead was last updated.
// Returns 0 if updatedAt is empty or invalid.
func calculateDaysSinceUpdate(updatedAt string) int {
	if updatedAt == "" {
		return 0
	}

	t, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return 0
	}

	days := int(time.Since(t).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
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
			DaysSinceUpdate: calculateDaysSinceUpdate(b.UpdatedAt),
			DependsOn:       dependsOn,
		}
	}
	return NewInsightsModel(beadsWithDeps)
}

// helpHintsForView returns context-appropriate key hints for the given view.
// Returns empty string when width < 60 (narrow terminal).
func helpHintsForView(view ViewType, width int) string {
	if width < 60 {
		return ""
	}
	switch view {
	case BoardView:
		return "hjkl nav  enter detail  / search  i insights  w workers  ? help  q quit"
	case DetailView:
		return "esc back  ←→ resize  ? help  q quit"
	case SearchView:
		return "↑↓ select  enter open  esc cancel"
	case HelpView:
		return "esc close"
	case InsightsView:
		return "esc back  ? help  q quit"
	case HealthView:
		return "esc back  ? help  q quit"
	case WorkersView:
		return "esc back  ? help  q quit"
	default:
		return "? help  q quit"
	}
}

// renderStatusBar renders the status bar with daemon health, worker count, aggregate stats,
// and context-appropriate help hints. Accepts width to control hint display.
// When m.height < 30, renders a condensed single-line bar with no separators.
func (m Model) renderStatusBar(width int) string {
	var daemonStatus string
	if m.daemonHealthy {
		daemonStatus = m.styles.DaemonOnline.Render("daemon: online")
	} else {
		daemonStatus = m.styles.DaemonOffline.Render("daemon: offline")
	}

	metrics := lipgloss.JoinHorizontal(
		lipgloss.Left,
		daemonStatus,
		m.styles.StatusLabel.Render(" | Workers: "),
		m.styles.StatusPrimary.Render(fmt.Sprintf("%d", m.workerCount)),
		m.styles.StatusLabel.Render(" | Open: "),
		m.styles.StatusWarning.Render(fmt.Sprintf("%d", m.openCount)),
		m.styles.StatusLabel.Render(" | In Progress: "),
		m.styles.StatusSuccess.Render(fmt.Sprintf("%d", m.inProgressCount)),
	)

	hints := helpHintsForView(m.activeView, width)
	if hints == "" || m.height < 30 {
		// Single line: metrics only
		return metrics
	}

	// Wide bar: metrics left, hints right
	hintsStyled := m.styles.StatusLabel.Render(hints)
	return lipgloss.JoinHorizontal(lipgloss.Left, metrics, "  ", hintsStyled)
}

// renderSearchOverlay renders the search overlay with text input and filtered results.
func (m Model) renderSearchOverlay() string {
	title := m.renderSearchTitle()
	searchInput := m.renderSearchInput()
	helpText := m.renderSearchHelp()
	results := m.renderSearchResults()

	return lipgloss.JoinVertical(lipgloss.Left, title, searchInput, helpText, results)
}

// renderSearchTitle renders the search overlay title.
func (m Model) renderSearchTitle() string {
	return m.styles.SearchTitle.Render("Search Beads")
}

// renderSearchInput renders the search input field with current query.
func (m Model) renderSearchInput() string {
	// Use adaptive width based on terminal size
	width := m.calculateSearchInputWidth()
	style := m.styles.SearchInput.Width(width)
	return style.Render("Query: " + m.searchInput.View())
}

// renderSearchHelp renders the help text for search overlay.
func (m Model) renderSearchHelp() string {
	return m.styles.SearchHelp.Render("Use p:N, s:STATUS, t:TYPE filters or fuzzy search. ↑↓ navigate, Enter to view, Esc to cancel")
}

// renderSearchResults renders the list of filtered search results.
func (m Model) renderSearchResults() string {
	filtered := m.filterBeads()

	if len(filtered) == 0 {
		return m.renderNoResults()
	}

	return m.renderResultsList(filtered)
}

// renderNoResults renders the "no results" message.
func (m Model) renderNoResults() string {
	return m.styles.SearchResults.Render(m.styles.NoResults.Render("No matching beads"))
}

// renderResultsList renders the list of search results with highlighting.
func (m Model) renderResultsList(filtered []protocol.Bead) string {
	const maxResults = 10
	totalCount := len(filtered)

	if len(filtered) > maxResults {
		filtered = filtered[:maxResults]
	}

	var resultsBuilder strings.Builder
	for i, bead := range filtered {
		resultsBuilder.WriteString(m.renderSearchResultLine(i, bead))
		resultsBuilder.WriteString("\n")
	}

	if totalCount > maxResults {
		resultsBuilder.WriteString(m.styles.Muted.Render(fmt.Sprintf("  ... and %d more", totalCount-maxResults)))
	}

	return m.styles.SearchResults.Render(resultsBuilder.String())
}

// renderSearchResultLine renders a single search result line with optional highlighting.
func (m Model) renderSearchResultLine(index int, bead protocol.Bead) string {
	if index == m.searchSelectedIndex {
		return m.styles.Highlight.Render(fmt.Sprintf("▸ %s - %s", bead.ID, bead.Title))
	}

	return fmt.Sprintf("  %s - %s", m.styles.IDMuted.Render(bead.ID), bead.Title)
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

// clampCursor ensures cursor position is valid after bead data refresh.
// If current column is empty, moves to first non-empty column.
// Clamps activeBead to valid range within the current column.
func (m Model) clampCursor() Model {
	board := NewBoardModel(m.beads)

	// Validate activeCol is within bounds
	if m.activeCol >= len(board.columns) {
		m.activeCol = 0
	}

	// Check if current column is empty
	if len(board.columns[m.activeCol].beads) == 0 {
		// Find first non-empty column
		foundNonEmpty := false
		for i, col := range board.columns {
			if len(col.beads) > 0 {
				m.activeCol = i
				m.activeBead = 0
				foundNonEmpty = true
				break
			}
		}
		// If all columns empty, stay at current column and reset activeBead
		if !foundNonEmpty {
			m.activeBead = 0
			return m
		}
	}

	// Clamp activeBead to valid range [0, len(beads)-1]
	columnBeads := board.columns[m.activeCol].beads
	if len(columnBeads) > 0 {
		if m.activeBead >= len(columnBeads) {
			m.activeBead = len(columnBeads) - 1
		}
		if m.activeBead < 0 {
			m.activeBead = 0
		}
	} else {
		m.activeBead = 0
	}

	return m
}

// drillDownToDetail transitions to DetailView for the selected bead.
// Returns unchanged model if no bead is selected (empty column).
// Also returns a tea.Cmd to initiate async worker events fetch.
func (m Model) drillDownToDetail() (Model, tea.Cmd) {
	board := NewBoardModel(m.beads)
	if m.activeCol >= len(board.columns) {
		return m, nil
	}

	col := board.columns[m.activeCol]
	if len(col.beads) == 0 || m.activeBead >= len(col.beads) {
		// No beads in column or invalid bead index
		return m, nil
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
	*m.detailModel = newDetailModel(beadDetail, m.theme, m.styles)
	m.activeView = DetailView

	// Initiate async worker events fetch
	return m, fetchWorkerEventsCmd(beadDetail.WorkerID)
}
