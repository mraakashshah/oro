package main

import tea "github.com/charmbracelet/bubbletea"

// ViewType represents different views in the dashboard.
type ViewType int

const (
	// BoardView shows the bead board.
	BoardView ViewType = iota
)

// Model is the Bubble Tea model for the oro dashboard.
type Model struct {
	activeView ViewType
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
