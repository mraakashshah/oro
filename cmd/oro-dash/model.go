package main

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"oro/pkg/protocol"
)

// tickMsg is sent by Bubble Tea on every tick interval.
// Used to trigger periodic data refresh from bd CLI and dispatcher state.
type tickMsg time.Time

// tickCmd returns a command that sends a tickMsg after 2 seconds.
func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
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
	//nolint:unused // Will be used when board view integrates FetchBeads
	beads []protocol.Bead
	//nolint:unused // Will be used when workers view integrates FetchWorkers
	workers []WorkerStatus

	// UI state
	width  int
	height int
	//nolint:unused // Will be used for error display
	err error
}

// newModel creates a new Model initialized with BoardView active.
func newModel() Model {
	return Model{
		activeView: BoardView,
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tickCmd()
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tickMsg:
		return m, tickCmd()
	}

	return m, nil
}

// View implements tea.Model.
func (m Model) View() string {
	return m.renderStatusBar()
}

// renderStatusBar renders the status bar with daemon health, worker count, and aggregate stats.
func (m Model) renderStatusBar() string {
	theme := DefaultTheme()

	if !m.daemonHealthy {
		offlineStyle := lipgloss.NewStyle().Foreground(theme.Error)
		return offlineStyle.Render("offline")
	}

	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		lipgloss.NewStyle().Render("Workers: "),
		lipgloss.NewStyle().Foreground(theme.Primary).Render(fmt.Sprintf("%d", m.workerCount)),
		lipgloss.NewStyle().Render(" | Open: "),
		lipgloss.NewStyle().Foreground(theme.Warning).Render(fmt.Sprintf("%d", m.openCount)),
		lipgloss.NewStyle().Render(" | In Progress: "),
		lipgloss.NewStyle().Foreground(theme.Success).Render(fmt.Sprintf("%d", m.inProgressCount)),
	)
}
