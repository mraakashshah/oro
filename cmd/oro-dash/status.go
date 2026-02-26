package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// StatusModel holds the state for the StatusView.
type StatusModel struct {
	cursor int // Index of the currently selected section
}

// statusSectionCount is the number of navigable sections in the status view.
const statusSectionCount = 3 // System, Panes, Workers

// View renders the StatusView showing system sections.
// When healthData is nil, shows "Connecting..." placeholder.
func (s StatusModel) View(_ Theme, styles Styles, healthData *HealthData, width, height int) string {
	if healthData == nil {
		return lipgloss.NewStyle().Width(width).Height(height).
			Render(styles.Muted.Render("Connecting..."))
	}

	sections := append(make([]string, 0, statusSectionCount),
		renderSystemSection(healthData, styles),
		renderPanesStatusSection(healthData, styles),
		renderWorkersStatusSection(healthData, styles),
	)

	return lipgloss.NewStyle().Width(width).Height(height).
		Render(lipgloss.JoinVertical(lipgloss.Left, sections...))
}

// renderSystemSection renders the System section with daemon status and uptime.
func renderSystemSection(hd *HealthData, styles Styles) string {
	title := styles.SectionTitle.Render("System")
	stateLine := fmt.Sprintf("Daemon: %s (PID %d)", hd.DaemonState, hd.DaemonPID)

	return lipgloss.JoinVertical(lipgloss.Left, title, stateLine)
}

// renderPanesStatusSection renders the Panes section with architect and manager status.
func renderPanesStatusSection(hd *HealthData, styles Styles) string {
	title := styles.SectionTitle.Render("Panes")

	var sb strings.Builder
	sb.WriteString(renderPaneStatusLine(hd.ArchitectPane))
	sb.WriteString("\n")
	sb.WriteString(renderPaneStatusLine(hd.ManagerPane))

	return lipgloss.JoinVertical(lipgloss.Left, title, sb.String())
}

// renderPaneStatusLine renders a single pane's status as a line.
func renderPaneStatusLine(pane PaneHealth) string {
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

// renderWorkersStatusSection renders the Workers section with active count.
func renderWorkersStatusSection(hd *HealthData, styles Styles) string {
	title := styles.SectionTitle.Render("Workers")
	countLine := fmt.Sprintf("Active: %d", hd.WorkerCount)

	return lipgloss.JoinVertical(lipgloss.Left, title, countLine)
}

// handleStatusViewKeys processes keyboard input in StatusView.
func (m Model) handleStatusViewKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.activeView = m.previousNavView
	case "j", "down":
		if m.statusModel.cursor < statusSectionCount-1 {
			m.statusModel.cursor++
		}
	case "k", "up":
		if m.statusModel.cursor > 0 {
			m.statusModel.cursor--
		}
	case "enter":
		// Placeholder for future section expansion
	}
	return m, nil
}
