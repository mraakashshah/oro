package main

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// renderHealthView renders the health view with daemon PID, pane statuses, and worker count.
func (m Model) renderHealthView() string {
	if m.healthData == nil {
		return m.styles.Error.Render("No health data available")
	}

	sections := make([]string, 0, 3)

	// Daemon section
	daemonSection := m.renderDaemonSection()
	sections = append(sections, daemonSection)

	// Panes section
	panesSection := m.renderPanesSection()
	sections = append(sections, panesSection)

	// Workers section
	workersSection := m.renderWorkersSection()
	sections = append(sections, workersSection)

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

// renderDaemonSection renders the daemon health section.
func (m Model) renderDaemonSection() string {
	title := m.styles.SectionTitle.Render("Daemon")
	pidLine := fmt.Sprintf("PID: %d", m.healthData.DaemonPID)
	stateLine := fmt.Sprintf("State: %s", m.healthData.DaemonState)

	return lipgloss.JoinVertical(lipgloss.Left, title, pidLine, stateLine)
}

// renderPanesSection renders the panes health section.
func (m Model) renderPanesSection() string {
	title := m.styles.SectionTitle.Render("Panes")

	architectStatus := m.renderPaneStatus(m.healthData.ArchitectPane)
	managerStatus := m.renderPaneStatus(m.healthData.ManagerPane)

	return lipgloss.JoinVertical(lipgloss.Left, title, architectStatus, managerStatus)
}

// renderPaneStatus renders a single pane's status.
func (m Model) renderPaneStatus(pane PaneHealth) string {
	status := "offline"
	if pane.Alive {
		status = "alive"
	}

	line := fmt.Sprintf("%s: %s", pane.Name, status)
	if pane.LastActivity != "" {
		line += fmt.Sprintf(" (last: %s)", pane.LastActivity)
	}

	return line
}

// renderWorkersSection renders the workers health section.
func (m Model) renderWorkersSection() string {
	title := m.styles.SectionTitle.Render("Workers")
	countLine := fmt.Sprintf("Active: %d", m.healthData.WorkerCount)

	return lipgloss.JoinVertical(lipgloss.Left, title, countLine)
}

// handleHealthViewKeys processes keyboard input in HealthView.
func (m Model) handleHealthViewKeys(key string) (tea.Model, tea.Cmd) {
	if key == "esc" {
		m.activeView = BoardView
	}
	return m, nil
}
