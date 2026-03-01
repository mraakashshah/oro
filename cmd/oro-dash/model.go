package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atotto/clipboard"
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

// moreClosedMsg carries additional closed beads fetched via load-more pagination.
type moreClosedMsg []protocol.Bead

// workerDataMsg carries worker status, assignments, and focused epic from the dispatcher.
type workerDataMsg struct {
	workers             []WorkerStatus
	assignments         map[string]string
	focusedEpic         string
	uptimeSeconds       float64
	pendingHandoffCount int
	attemptCounts       map[string]int
}

// healthDataMsg carries health data from the dispatcher.
type healthDataMsg struct {
	data *HealthData
}

// beadDetailMsg carries a freshly-fetched bead from `bd show --json`.
// Used to refresh detail view data asynchronously after drill-down.
type beadDetailMsg struct {
	bead *protocol.Bead
	err  error
}

// tickCmd returns a command that sends a tickMsg after 2 seconds.
func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// fetchMoreClosedCmd returns a tea.Cmd that fetches the next page of closed beads.
// cursor is the oldest ClosedAt from the previous batch (m.closedCursor).
func fetchMoreClosedCmd(cursor string) tea.Cmd {
	return func() tea.Msg {
		beads, _ := fetchMoreClosed(context.Background(), cursor)
		return moreClosedMsg(beads)
	}
}

// fetchBeadsCmd returns a tea.Cmd that fetches beads from the bd CLI.
func fetchBeadsCmd() tea.Cmd {
	return func() tea.Msg {
		beads, _ := fetchBeads(context.Background())
		return beadsMsg(beads)
	}
}

// fetchHealthCmd returns a tea.Cmd that fetches swarm health from the dispatcher.
func fetchHealthCmd() tea.Cmd {
	return func() tea.Msg {
		socketPath := defaultSocketPath()
		hd, _ := fetchHealth(context.Background(), socketPath)
		return healthDataMsg{data: hd}
	}
}

// fetchWorkersCmd returns a tea.Cmd that fetches worker status and assignments from the dispatcher.
func fetchWorkersCmd() tea.Cmd {
	return func() tea.Msg {
		socketPath := defaultSocketPath()
		ds, _ := fetchWorkerStatus(context.Background(), socketPath)
		if ds == nil {
			return workerDataMsg{}
		}
		return workerDataMsg{
			workers:             ds.workers,
			assignments:         ds.assignments,
			focusedEpic:         ds.focusedEpic,
			uptimeSeconds:       ds.uptimeSeconds,
			pendingHandoffCount: ds.pendingHandoffCount,
			attemptCounts:       ds.attemptCounts,
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
	// ListView shows the dense list view with split-pane detail.
	ListView
	// StatusView shows system status with sections for daemon, panes, workers.
	StatusView
)

// HealthData represents the health status of the oro swarm.
type HealthData struct {
	DaemonPID     int
	DaemonState   string
	ArchitectPane PaneHealth
	ManagerPane   PaneHealth
	WorkerCount   int
}

// PaneHealth represents the health status of a tmux pane.
type PaneHealth struct {
	Name         string
	Alive        bool
	LastActivity string
}

// Model is the Bubble Tea model for the oro dashboard.
type Model struct {
	activeView      ViewType
	previousView    ViewType // View to return to when help is dismissed
	previousNavView ViewType // Nav view to return to on esc (BoardView or ListView)
	daemonHealthy   bool
	workerCount     int
	openCount       int
	inProgressCount int

	// Data fetched from external sources
	beads       []protocol.Bead
	workers     []WorkerStatus
	assignments map[string]string // bead ID -> worker ID
	healthData  *HealthData       // Health data from dispatcher
	focusedEpic string            // Epic ID currently focused by dispatcher

	// Enriched dispatcher fields (oro-yqvn.3)
	closedCount int

	// Load-more pagination state for closed beads (oro-tm8m.11)
	extraClosed  []protocol.Bead // additional closed beads appended via moreClosedMsg
	closedCursor string          // oldest ClosedAt from last closed batch; used as cursor for next fetch

	uptimeSeconds       float64
	pendingHandoffCount int
	attemptCounts       map[string]int

	// Sample collection state (oro-yqvn.4)
	metricsBuffer *MetricsBuffer
	samplePending bool // true after tickMsg, cleared when sample recorded
	beadsReady    bool // beadsMsg arrived since last sample
	workersReady  bool // workerDataMsg arrived since last sample

	// UI state
	width       int
	height      int
	initialLoad bool // True until first beadsMsg arrives
	//nolint:unused // Will be used for error display
	err error

	// Kanban navigation state
	activeCol        int    // Index of the active column (0-3: Ready, In Progress, Blocked, Done)
	activeBead       int    // Index of the active bead within the current column
	colScrollOffsets [4]int // Per-column scroll offset (first visible bead index)

	// Detail view state
	detailModel *DetailModel // Set when drilling down into a bead

	// Insights view state — cached to avoid recomputing on every View() call.
	// Rebuilt in applyBeadsMsg; nil until first beadsMsg arrives.
	insightsModel *InsightsModel

	// Search view state
	searchInput         textinput.Model // Bubbles textinput for search query
	searchSelectedIndex int             // Index of the selected search result
	searchModel         *SearchModel

	// List view state
	listModel ListModel

	// Status view state
	statusModel StatusModel

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
		activeView:      ListView,
		previousNavView: ListView,
		initialLoad:     true, // Show "Loading" until first beadsMsg
		searchInput:     ti,
		searchModel:     &SearchModel{},
		listModel:       NewListModel(),
		theme:           theme,
		styles:          NewStyles(theme),
		splitRatio:      0.4, // Default 40% board, 60% detail
		metricsBuffer:   NewMetricsBuffer(),
	}
}

// calculateColumnWidth returns the column width based on terminal width.
// Divides terminal width by 4 (number of columns) with a minimum floor of 18.
func (m Model) calculateColumnWidth() int {
	const minWidth = 18
	const numColumns = 4

	// Each column has a 2-char border (left+right), so subtract that from available width
	colWidth := (m.width - numColumns*2) / numColumns
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
//
//nolint:gocyclo // switch dispatch over message types; complexity is inherent in Bubble Tea Update
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKeyPress(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.listModel = m.listModel.resize(msg.Width, msg.Height)
		if m.detailModel != nil {
			updated := m.detailModel.handleWindowResize(msg)
			m.detailModel = &updated
		}

	case beadsMsg:
		m = m.applyBeadsMsg(msg)

	case moreClosedMsg:
		m = m.applyMoreClosedMsg(msg)

	case workerDataMsg:
		m = m.applyWorkerDataMsg(msg)

	case workerEventsMsg:
		// Update detail model with fetched worker events
		if m.detailModel != nil {
			m.detailModel.workerEvents = msg.events
			m.detailModel.eventError = msg.err
			m.detailModel.loadingEvents = false
		}

	case healthDataMsg:
		m.healthData = msg.data

	case tickMsg:
		m.samplePending = true
		return m, tea.Batch(fetchBeadsCmd(), fetchWorkersCmd(), fetchHealthCmd(), tickCmd())

	case beadDetailMsg:
		m = m.applyBeadDetailMsg(msg)

	case navigateToDepMsg:
		return m.handleNavigateToDep(msg.beadID)
	case fsChangeMsg:
		// File change detected in .beads/ - fetch immediately instead of waiting for tick
		return m, fetchBeadsCmd()
	}

	return m, nil
}

// buildCurrentSample creates a MetricsSample from the current model state.
func (m Model) buildCurrentSample() MetricsSample {
	s := MetricsSample{
		Timestamp:    time.Now(),
		BeadsClosed:  m.closedCount,
		QueueReady:   m.openCount,
		QueueWIP:     m.inProgressCount,
		WorkersTotal: len(m.workers),
	}
	s.Workers = make([]WorkerSample, len(m.workers))
	for i, w := range m.workers {
		s.Workers[i] = WorkerSample{
			ID:         w.ID,
			ContextPct: w.ContextPct,
			State:      w.Status,
			BeadID:     w.BeadID,
		}
		switch w.Status {
		case "working":
			s.WorkersActive++
		case "idle":
			s.WorkersIdle++
		}
	}
	return s
}

// maybeRecordSample records a metrics sample if samplePending and both data sources are ready.
func (m Model) maybeRecordSample() Model {
	if !m.samplePending || !m.beadsReady || !m.workersReady {
		return m
	}
	if m.metricsBuffer != nil {
		m.metricsBuffer.Record(m.buildCurrentSample())
	}
	m.samplePending = false
	m.beadsReady = false
	m.workersReady = false
	return m
}

// applyBeadsMsg updates the model with fetched bead data, recomputing status counts.
// extraClosed is preserved so that beads loaded via load-more survive periodic refreshes.
func (m Model) applyBeadsMsg(msg beadsMsg) Model {
	m.initialLoad = false
	m.beads = []protocol.Bead(msg)
	m.beadsReady = true
	m.openCount = 0
	m.inProgressCount = 0
	m.closedCount = 0
	for _, b := range m.beads {
		switch b.Status {
		case "open":
			m.openCount++
		case "in_progress":
			m.inProgressCount++
		case "closed":
			m.closedCount++
		}
	}
	// Set closedCursor to oldest ClosedAt among closed beads in this batch so that
	// fetchMoreClosed knows where to start the next page.
	m.closedCursor = oldestClosedAt([]protocol.Bead(msg))
	m = m.clampCursor()
	m.listModel = m.listModel.updateBeads(m.allBeads())
	m.insightsModel = m.buildInsightsModel()
	m = m.maybeRecordSample()
	return m
}

// applyBeadDetailMsg refreshes the active detail view with data from `bd show --json`.
// Merge strategy: if the incoming bead has no Dependencies (bd show uses different JSON
// keys than bd list, so deps unmarshal as nil), preserve the existing deps from m.beads.
func (m Model) applyBeadDetailMsg(msg beadDetailMsg) Model {
	if msg.err != nil || msg.bead == nil {
		return m
	}
	bead := *msg.bead

	// Merge: preserve existing Dependencies when bd show returned none.
	if len(bead.Dependencies) == 0 {
		for _, existing := range m.beads {
			if existing.ID == bead.ID {
				bead.Dependencies = existing.Dependencies
				break
			}
		}
	}

	// Refresh the detail model if it is currently showing this bead.
	if m.detailModel != nil && m.detailModel.bead.ID == bead.ID {
		updated := newDetailModel(protocol.BeadDetail{
			ID:                 bead.ID,
			Title:              bead.Title,
			Description:        bead.Description,
			AcceptanceCriteria: bead.AcceptanceCriteria,
			Status:             bead.Status,
			Model:              bead.Model,
			Owner:              bead.Owner,
			Dependencies:       bead.Dependencies,
			// Preserve live worker fields from the existing detail model.
			WorkerID:       m.detailModel.bead.WorkerID,
			ContextPercent: m.detailModel.bead.ContextPercent,
			LastHeartbeat:  m.detailModel.bead.LastHeartbeat,
		}, m.theme, m.styles)
		updated.activeTab = m.detailModel.activeTab
		updated.workerEvents = m.detailModel.workerEvents
		updated.workerOutput = m.detailModel.workerOutput
		m.detailModel = &updated
	}
	return m
}

// applyMoreClosedMsg appends load-more results to extraClosed and advances the cursor.
func (m Model) applyMoreClosedMsg(msg moreClosedMsg) Model {
	m.extraClosed = append(m.extraClosed, []protocol.Bead(msg)...)
	if len(msg) > 0 {
		m.closedCursor = oldestClosedAt([]protocol.Bead(msg))
	}
	m.listModel = m.listModel.updateBeads(m.allBeads())
	return m
}

// allBeads returns the combined slice of main beads plus any extra closed beads.
func (m Model) allBeads() []protocol.Bead {
	if len(m.extraClosed) == 0 {
		return m.beads
	}
	all := make([]protocol.Bead, len(m.beads), len(m.beads)+len(m.extraClosed))
	copy(all, m.beads)
	return append(all, m.extraClosed...)
}

// oldestClosedAt returns the oldest ClosedAt timestamp from a slice of beads.
// Assumes beads are sorted by ClosedAt descending (most recent first), so the last
// non-empty ClosedAt is the oldest. Falls back to linear scan for safety.
func oldestClosedAt(beads []protocol.Bead) string {
	oldest := ""
	for _, b := range beads {
		if b.ClosedAt == "" {
			continue
		}
		if oldest == "" || b.ClosedAt < oldest {
			oldest = b.ClosedAt
		}
	}
	return oldest
}

// applyWorkerDataMsg updates the model with worker status from the dispatcher.
func (m Model) applyWorkerDataMsg(msg workerDataMsg) Model {
	if msg.workers == nil {
		m.daemonHealthy = false
		m.workerCount = 0
		m.assignments = nil
		m.focusedEpic = ""
		return m
	}
	m.daemonHealthy = true
	m.workers = msg.workers
	m.workerCount = len(msg.workers)
	m.assignments = msg.assignments
	m.focusedEpic = msg.focusedEpic
	m.uptimeSeconds = msg.uptimeSeconds
	m.pendingHandoffCount = msg.pendingHandoffCount
	m.attemptCounts = msg.attemptCounts
	m.listModel = m.listModel.updateWorkers(msg.workers, msg.assignments)
	m.workersReady = true
	m = m.maybeRecordSample()
	return m
}

// handleKeyPress processes keyboard input and returns updated model with optional command.
//
//nolint:gocyclo // switch dispatch over view types; complexity is inherent in Bubble Tea key routing
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
		return m.handleDetailViewKeys(key, msg)
	case InsightsView:
		return m.handleInsightsViewKeys(key)
	case SearchView:
		return m.handleSearchViewKeys(key, msg)
	case StatusView:
		return m.handleStatusViewKeys(key)
	case ListView:
		return m.handleListViewKeys(key)
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
//
//nolint:gocyclo,funlen // switch dispatch over key bindings; complexity is inherent in key routing
func (m Model) handleDetailViewKeys(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "backspace":
		m.activeView = m.previousNavView
		m.detailModel = nil
	case "tab", "right":
		if m.detailModel != nil {
			*m.detailModel = m.detailModel.nextTab()
		}
	case "shift+tab", "left":
		if m.detailModel != nil {
			*m.detailModel = m.detailModel.prevTab()
		}
	case "j", "k", "enter", "pgup", "pgdown":
		if m.detailModel != nil {
			updated, cmd := m.detailModel.Update(msg)
			*m.detailModel = updated
			return m, cmd
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
	case "H", "w":
		m.activeView = StatusView
		m.detailModel = nil
	case "L":
		m.activeView = ListView
		m.detailModel = nil
	}
	return m, nil
}

// handleInsightsViewKeys processes keyboard input in InsightsView.
func (m Model) handleInsightsViewKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.activeView = m.previousNavView
	case "H", "w":
		m.activeView = StatusView
	case "L":
		m.activeView = ListView
	}
	return m, nil
}

// handleListViewKeys processes keyboard input in ListView.
//
//nolint:gocyclo,funlen // switch dispatch over key bindings; complexity is inherent in key routing
func (m Model) handleListViewKeys(key string) (tea.Model, tea.Cmd) {
	// Detail pane focused: handle detail-specific keys
	if m.listModel.detailFocused {
		return m.handleListDetailKeys(key)
	}
	switch key {
	case "j", "down":
		m.listModel = m.listModel.moveDown()
	case "k", "up":
		m.listModel = m.listModel.moveUp()
	case " ":
		m.listModel = m.listModel.toggleAtCursor()
	case "enter":
		return m.listDrillDown()
	case "tab", "l":
		m.listModel = m.listModel.toggleFocus()
	case "<":
		m.listModel = m.listModel.adjustSplit(-0.05)
	case ">":
		m.listModel = m.listModel.adjustSplit(0.05)
	case "o", "c", "r":
		m.listModel = m.listModel.setFilter(key)
	case "y":
		if id := m.listModel.cursorBeadID(); id != "" {
			_ = clipboard.WriteAll(id)
		}
	case "b":
		m.previousNavView = BoardView
		m.activeView = BoardView
	case "i":
		m.activeView = InsightsView
	case "/":
		m.activeView = SearchView
		m.searchInput.Focus()
		m.searchInput.SetValue("")
		m.searchSelectedIndex = 0
	case "s":
		m.activeView = StatusView
	case "H", "w":
		m.activeView = StatusView
	case "L":
		// Already in ListView, no-op
	case "m":
		// Load more closed beads (pagination).
		return m, fetchMoreClosedCmd(m.closedCursor)
	}
	return m, nil
}

// handleListDetailKeys processes keys when the detail pane is focused.
func (m Model) handleListDetailKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.listModel = m.listModel.unfocusDetail()
	case "tab", "l":
		m.listModel = m.listModel.toggleFocus()
	case " ":
		m.listModel = m.listModel.toggleDetailSection()
	case "j", "down":
		m.listModel = m.listModel.detailMoveDown()
	case "k", "up":
		m.listModel = m.listModel.detailMoveUp()
	case "<":
		m.listModel = m.listModel.adjustSplit(-0.05)
	case ">":
		m.listModel = m.listModel.adjustSplit(0.05)
	}
	return m, nil
}

// listDrillDown opens DetailView for the bead at the list cursor.
func (m Model) listDrillDown() (tea.Model, tea.Cmd) {
	beadID := m.listModel.cursorBeadID()
	if beadID == "" {
		return m, nil
	}
	for _, b := range m.beads {
		if b.ID != beadID {
			continue
		}
		beadDetail := protocol.BeadDetail{
			ID:                 b.ID,
			Title:              b.Title,
			Status:             b.Status,
			AcceptanceCriteria: b.AcceptanceCriteria,
			Model:              b.Model,
			Dependencies:       b.Dependencies,
		}
		m.wireWorkerDataToBeadDetail(&beadDetail)
		dm := newDetailModel(beadDetail, m.theme, m.styles)
		m.detailModel = &dm
		m.activeView = DetailView
		return m, fetchWorkerEventsCmd(beadDetail.WorkerID)
	}
	return m, nil
}

// handleBoardViewKeys processes keyboard input in BoardView.
func (m Model) handleBoardViewKeys(key string) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch key {
	case "enter":
		m, cmd = m.drillDownToDetail()
	case "h", "left":
		m = m.moveToPrevColumn()
	case "l", "right":
		m = m.moveToNextColumn()
	case "tab":
		m = m.moveToNextColumn()
	case "shift+tab":
		m = m.moveToPrevColumn()
	case "j", "down":
		m = m.moveToNextBead()
		m = m.ensureBoardScrollVisible()
	case "k", "up":
		m = m.moveToPrevBead()
		m = m.ensureBoardScrollVisible()
	case "i":
		m.activeView = InsightsView
	case "/":
		m.activeView = SearchView
		m.searchInput.Focus()
		m.searchInput.SetValue("")
		m.searchSelectedIndex = 0
	case "s":
		m.activeView = StatusView
	case "H", "w":
		m.activeView = StatusView
	case "L":
		m.activeView = ListView
	case "m":
		// Load more closed beads (pagination).
		return m, fetchMoreClosedCmd(m.closedCursor)
	}
	return m, cmd
}

// handleSearchViewKeys processes keyboard input in SearchView.
func (m Model) handleSearchViewKeys(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch key {
	case "esc":
		m.activeView = m.previousNavView
		m.searchInput.Blur()
		m.searchInput.SetValue("")
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
				Status:             selectedBead.Status,
				AcceptanceCriteria: selectedBead.AcceptanceCriteria,
				Model:              selectedBead.Model,
				Dependencies:       selectedBead.Dependencies,
			}
			m.wireWorkerDataToBeadDetail(&beadDetail)
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
func (m Model) filterBeads() []protocol.Bead {
	query := m.searchInput.Value()
	if query == "" {
		return m.beads
	}
	return m.searchModel.Filter(m.beads, query)
}

// wireWorkerDataToBeadDetail populates WorkerID and ContextPercent in a BeadDetail
// from the Model's assignments and workers lists.
func (m Model) wireWorkerDataToBeadDetail(detail *protocol.BeadDetail) {
	if workerID, ok := m.assignments[detail.ID]; ok {
		detail.WorkerID = workerID
		for _, w := range m.workers {
			if w.ID == workerID {
				detail.ContextPercent = w.ContextPct
				break
			}
		}
	}
}

// View implements tea.Model.
func (m Model) View() string {
	// Show loading state until first beadsMsg
	if m.initialLoad {
		statusBar := m.renderStatusBar(m.width)
		return "Loading..." + "\n" + statusBar
	}

	statusBar := m.renderStatusBar(m.width)

	switch m.activeView {
	case HelpView:
		return m.renderHelpOverlay() + "\n" + statusBar
	case InsightsView:
		im := m.insightsModel
		if im == nil {
			im = m.buildInsightsModel()
		}
		return im.Render(m.styles) + "\n" + statusBar
	case DetailView:
		if m.detailModel != nil {
			if m.width >= 100 {
				return m.renderContextSplit() + "\n" + statusBar
			}
			return m.detailModel.View(m.styles) + "\n" + statusBar
		}
		// Fallback to board if detailModel is nil
		board := NewBoardModelWithWorkers(m.beads, m.workers, m.assignments)
		colWidth := m.calculateColumnWidth()
		return board.RenderWithScroll(m.activeCol, m.activeBead, colWidth, m.colScrollOffsets, m.maxVisibleBeads(), m.theme, m.styles) + "\n" + statusBar
	case SearchView:
		return m.renderSearchOverlay() + "\n" + statusBar
	case StatusView:
		return m.statusModel.View(m.theme, m.styles, m.healthData, m.workers, m.pendingHandoffCount, m.attemptCounts, m.metricsBuffer, m.width, m.height) + "\n" + statusBar
	case ListView:
		return m.listModel.View(m.theme, m.styles, m.width, m.height-2) + "\n" + statusBar
	default:
		board := NewBoardModelWithWorkers(m.beads, m.workers, m.assignments)
		colWidth := m.calculateColumnWidth()
		return board.RenderWithScroll(m.activeCol, m.activeBead, colWidth, m.colScrollOffsets, m.maxVisibleBeads(), m.theme, m.styles) + "\n" + statusBar
	}
}

// renderContextSplit renders DetailView with a bead context panel on the left and detail on the right.
func (m Model) renderContextSplit() string {
	leftWidth := int(float64(m.width) * 0.3)
	if leftWidth < 25 {
		leftWidth = 25
	}
	rightWidth := m.width - leftWidth - 1

	// Build context panel
	d := m.detailModel
	var ctx strings.Builder
	ctx.WriteString(m.styles.Header.Render(d.bead.ID) + "\n")
	ctx.WriteString(d.bead.Title + "\n")
	ctx.WriteString(m.styles.Muted.Render(d.bead.Status) + "\n\n")

	// Description
	if d.bead.Description != "" {
		ctx.WriteString(m.styles.StatusLabel.Render("Description:") + "\n")
		ctx.WriteString(m.styles.Muted.Render(d.bead.Description) + "\n\n")
	}

	// Dependencies
	if len(d.bead.Dependencies) > 0 {
		ctx.WriteString(m.styles.StatusLabel.Render("Dependencies:") + "\n")
		for _, dep := range d.bead.Dependencies {
			ctx.WriteString("  " + m.styles.Muted.Render(dep.Type+": ") + dep.DependsOnID + "\n")
		}
	}

	// Acceptance criteria summary
	if d.bead.AcceptanceCriteria != "" {
		ctx.WriteString("\n" + m.styles.StatusLabel.Render("Acceptance:") + "\n")
		ac := d.bead.AcceptanceCriteria
		if len(ac) > leftWidth*6 {
			ac = ac[:leftWidth*6] + "..."
		}
		ctx.WriteString(m.styles.Muted.Render(ac) + "\n")
	}

	leftPanel := lipgloss.NewStyle().Width(leftWidth).Render(ctx.String())
	separator := lipgloss.NewStyle().Foreground(m.theme.ColorBorder).Render("│")
	rightPanel := lipgloss.NewStyle().Width(rightWidth - 1).Render(m.detailModel.View(m.styles))

	return lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, separator, rightPanel)
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
	if days <= 0 {
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
			Status:          b.Status,
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
		return "hjkl nav  enter detail  / search  i insights  s status  ? help  q quit"
	case DetailView:
		return "esc back  ←→ resize  ? help  q quit"
	case SearchView:
		return "↑↓ select  enter open  esc cancel"
	case HelpView:
		return "esc close"
	case InsightsView:
		return "esc back  ? help  q quit"
	case StatusView:
		return "j/k navigate  enter expand  esc back  ? help  q quit"
	case ListView:
		return "j/k navigate  space collapse  enter detail  b board  / search  ? help  q quit"
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

	// Build base metrics
	metricsComponents := []string{
		daemonStatus,
		m.styles.StatusLabel.Render(m.styles.Separator + "Workers: "),
		m.styles.StatusPrimary.Render(fmt.Sprintf("%d", m.workerCount)),
		m.styles.StatusLabel.Render(m.styles.Separator + "Open: "),
		m.styles.StatusWarning.Render(fmt.Sprintf("%d", m.openCount)),
		m.styles.StatusLabel.Render(m.styles.Separator + "In Progress: "),
		m.styles.StatusSuccess.Render(fmt.Sprintf("%d", m.inProgressCount)),
	}

	// Add epic progress if a focused epic exists
	if m.focusedEpic != "" {
		pct, total, done := GetEpicProgress(m.focusedEpic, m.beads)
		metricsComponents = append(metricsComponents,
			m.styles.StatusLabel.Render(m.styles.Separator+"Epic: "),
			m.styles.StatusPrimary.Render(fmt.Sprintf("%s (%d/%d - %d%%)", m.focusedEpic, done, total, pct)),
		)
	}

	metrics := lipgloss.JoinHorizontal(lipgloss.Left, metricsComponents...)

	hints := helpHintsForView(m.activeView, width)
	if hints == "" || m.height < 30 {
		// Single line: metrics only
		return metrics
	}

	// Wide bar: metrics left, hints right (right-aligned with gap fill)
	hintsStyled := m.styles.StatusLabel.Render(hints)

	// Calculate gap to right-align hints
	metricsWidth := lipgloss.Width(metrics)
	hintsWidth := lipgloss.Width(hints)
	gap := width - metricsWidth - hintsWidth

	// Minimum gap is 2 for separation
	if gap < 2 {
		gap = 2
	}

	gapStr := strings.Repeat(" ", gap)
	return lipgloss.JoinHorizontal(lipgloss.Left, metrics, gapStr, hintsStyled)
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

// maxVisibleBeads returns the number of beads that fit in the terminal height.
func (m Model) maxVisibleBeads() int {
	// Overhead: column header (~3 lines) + status bar (~2 lines) + column border (~2 lines)
	const overhead = 7
	const cardHeight = 4 // content(3) + margin(1), no borders
	available := m.height - overhead
	if available < cardHeight {
		return 1
	}
	return available / cardHeight
}

// ensureBoardScrollVisible adjusts the active column's scroll offset
// so the cursor (activeBead) is within the visible window.
func (m Model) ensureBoardScrollVisible() Model {
	maxVis := m.maxVisibleBeads()
	col := m.activeCol
	if col < 0 || col >= len(m.colScrollOffsets) {
		return m
	}
	offset := m.colScrollOffsets[col]
	if m.activeBead < offset {
		m.colScrollOffsets[col] = m.activeBead
	} else if m.activeBead >= offset+maxVis {
		m.colScrollOffsets[col] = m.activeBead - maxVis + 1
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
		Status:             selectedBead.Status,
		AcceptanceCriteria: selectedBead.AcceptanceCriteria,
		Model:              selectedBead.Model,
		Dependencies:       selectedBead.Dependencies,
	}

	m.wireWorkerDataToBeadDetail(&beadDetail)

	// Create detail model
	m.detailModel = &DetailModel{}
	*m.detailModel = newDetailModel(beadDetail, m.theme, m.styles)
	m.activeView = DetailView

	// Initiate async worker events fetch
	return m, fetchWorkerEventsCmd(beadDetail.WorkerID)
}

// handleNavigateToDep navigates to the detail view of a dependency bead by ID.
// It looks up the bead in the known beads list; if not found, creates a minimal detail.
func (m Model) handleNavigateToDep(beadID string) (tea.Model, tea.Cmd) {
	for _, b := range m.beads {
		if b.ID != beadID {
			continue
		}
		beadDetail := protocol.BeadDetail{
			ID:           b.ID,
			Title:        b.Title,
			Status:       b.Status,
			Model:        b.Model,
			Dependencies: b.Dependencies,
		}
		dm := newDetailModel(beadDetail, m.theme, m.styles)
		m.detailModel = &dm
		m.activeView = DetailView
		return m, fetchWorkerEventsCmd(beadDetail.WorkerID)
	}
	// Dep bead not found in known beads - navigate with minimal info.
	beadDetail := protocol.BeadDetail{ID: beadID, Title: beadID}
	dm := newDetailModel(beadDetail, m.theme, m.styles)
	m.detailModel = &dm
	m.activeView = DetailView
	return m, nil
}
