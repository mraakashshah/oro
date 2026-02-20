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

// calculateWorkerColumnWidths computes per-column widths for the workers table
// based on the available terminal width. Widths are proportional to content
// importance, with minimum floors to prevent truncation on very narrow terminals.
// Returns [workerID, status, assignedBead, health, context] widths.
func calculateWorkerColumnWidths(totalWidth int) []int {
	const (
		numSeparators   = 4 // single-space gaps between 5 columns
		minWorkerID     = 10
		minStatus       = 8
		minAssignedBead = 10
		minHealth       = 6
		minContext      = 6
	)

	usable := totalWidth - numSeparators
	if usable < minWorkerID+minStatus+minAssignedBead+minHealth+minContext {
		return []int{minWorkerID, minStatus, minAssignedBead, minHealth, minContext}
	}

	workerID := max(int(float64(usable)*0.28), minWorkerID)
	status := max(int(float64(usable)*0.20), minStatus)
	assignedBead := max(int(float64(usable)*0.28), minAssignedBead)
	health := max(int(float64(usable)*0.12), minHealth)
	// Context absorbs the remainder so columns exactly fill usable width.
	context := usable - workerID - status - assignedBead - health
	if context < minContext {
		context = minContext
	}

	return []int{workerID, status, assignedBead, health, context}
}

// View renders the workers table at the given total terminal width.
func (w WorkersTableModel) View(theme Theme, styles Styles, totalWidth int) string {
	if len(w.workers) == 0 {
		return renderEmptyWorkersState(styles)
	}

	return w.renderWorkersTable(theme, styles, totalWidth)
}

// renderEmptyWorkersState renders a message when no workers are active.
func renderEmptyWorkersState(styles Styles) string {
	msg := "No active workers"
	centered := styles.WorkersCentered.Render(styles.Muted.Render(msg))
	return centered
}

// renderWorkersTable renders the full workers table with headers and rows.
func (w WorkersTableModel) renderWorkersTable(theme Theme, styles Styles, totalWidth int) string {
	var sb strings.Builder

	colWidths := calculateWorkerColumnWidths(totalWidth)

	// Table headers
	headers := []string{"Worker ID", "Status", "Assigned Bead", "Health", "Context"}

	// Render header row — use WorkersCol base style with .Width() applied per column.
	headerParts := make([]string, 0, len(headers))
	for i, header := range headers {
		style := styles.WorkersCol.
			Width(colWidths[i]).
			Bold(true).
			Foreground(theme.Primary)
		headerParts = append(headerParts, style.Render(header))
	}
	sb.WriteString(strings.Join(headerParts, " "))
	sb.WriteString("\n")

	// Render separator spanning the full terminal width.
	sb.WriteString(strings.Repeat("─", totalWidth))
	sb.WriteString("\n")

	// Render worker rows
	for _, worker := range w.workers {
		row := w.renderWorkerRow(worker, colWidths, styles)
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
