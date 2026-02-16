package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
}

// newModel creates a new Model initialized with BoardView active.
func newModel() Model {
	return Model{
		activeView: BoardView,
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
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
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
	}
	return m, nil
}

// View implements tea.Model.
func (m Model) View() string {
	board := NewBoardModel(m.beads)
	return m.renderStatusBar() + "\n" + board.RenderWithCursor(m.activeCol, m.activeBead)
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
