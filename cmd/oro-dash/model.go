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
//
//nolint:unused // Will be used when tick-based refresh is implemented
type tickMsg time.Time

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
	//nolint:unused // Will be used for responsive layout
	width int
	//nolint:unused // Will be used for responsive layout
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
	return nil
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}

// View implements tea.Model.
func (m Model) View() string {
	return ""
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
