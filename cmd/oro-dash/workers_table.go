package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// WorkersTableModel holds the workers table state.
type WorkersTableModel struct {
	workers     []WorkerStatus
	assignments map[string]string
}

// NewWorkersTableModel creates a new workers table model.
func NewWorkersTableModel(workers []WorkerStatus, assignments map[string]string) WorkersTableModel {
	return WorkersTableModel{
		workers:     workers,
		assignments: assignments,
	}
}

// View renders the workers table.
func (w WorkersTableModel) View(theme Theme, styles Styles) string {
	if len(w.workers) == 0 {
		return renderEmptyWorkersState(styles)
	}

	return w.renderWorkersTable(theme, styles)
}

// renderEmptyWorkersState renders a message when no workers are active.
func renderEmptyWorkersState(styles Styles) string {
	msg := "No active workers"
	centered := styles.WorkersCentered.Render(styles.Muted.Render(msg))
	return centered
}

// renderWorkersTable renders the full workers table with headers and rows.
func (w WorkersTableModel) renderWorkersTable(theme Theme, styles Styles) string {
	var sb strings.Builder

	// Table headers
	headers := []string{"Worker ID", "Status", "Assigned Bead", "Health", "Context"}
	headerWidths := []int{20, 15, 20, 10, 10}

	// Render header row — use WorkersCol base style with .Width() applied per column.
	headerParts := make([]string, 0, len(headers))
	for i, header := range headers {
		style := styles.WorkersCol.
			Width(headerWidths[i]).
			Bold(true).
			Foreground(theme.Primary)
		headerParts = append(headerParts, style.Render(header))
	}
	sb.WriteString(strings.Join(headerParts, " "))
	sb.WriteString("\n")

	// Render separator
	sb.WriteString(strings.Repeat("─", 80))
	sb.WriteString("\n")

	// Render worker rows
	for _, worker := range w.workers {
		row := w.renderWorkerRow(worker, headerWidths, styles)
		sb.WriteString(row)
		sb.WriteString("\n")
	}

	return sb.String()
}

// renderWorkerRow renders a single worker row in the table.
func (w WorkersTableModel) renderWorkerRow(worker WorkerStatus, widths []int, styles Styles) string {
	// Worker ID
	workerID := truncate(worker.ID, widths[0])

	// Status
	status := truncate(worker.Status, widths[1])

	// Assigned Bead (show '-' if no assignment)
	assignedBead := "-"
	if worker.BeadID != "" {
		assignedBead = worker.BeadID
	}
	assignedBead = truncate(assignedBead, widths[2])

	// Health badge (based on heartbeat age)
	healthBadge := w.renderHealthBadge(worker, styles)

	// Context percentage
	contextStr := "-"
	if worker.ContextPct > 0 {
		contextStr = fmt.Sprintf("%d%%", worker.ContextPct)
	}
	contextStr = truncate(contextStr, widths[4])

	// Build row — use WorkersCol base style with dynamic .Width() per column (Width returns a copy).
	cells := []string{
		styles.WorkersCol.Width(widths[0]).Render(workerID),
		styles.WorkersCol.Width(widths[1]).Render(status),
		styles.WorkersCol.Width(widths[2]).Render(assignedBead),
		styles.WorkersCol.Width(widths[3]).Render(healthBadge),
		styles.WorkersCol.Width(widths[4]).Render(contextStr),
	}

	return strings.Join(cells, " ")
}

// renderHealthBadge renders the health indicator based on heartbeat age.
// Green (<5s), Amber (5-15s), Red (>15s).
func (w WorkersTableModel) renderHealthBadge(worker WorkerStatus, styles Styles) string {
	healthStyle := styles.HealthRed
	switch {
	case worker.LastProgressSecs < 5.0:
		healthStyle = styles.HealthGreen
	case worker.LastProgressSecs <= 15.0:
		healthStyle = styles.HealthAmber
	}

	return healthStyle.Render("●")
}

// handleWorkersViewKeys processes keyboard input in WorkersView.
func (m Model) handleWorkersViewKeys(key string) (tea.Model, tea.Cmd) {
	if key == "esc" {
		m.activeView = BoardView
	}
	return m, nil
}
