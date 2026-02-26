package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"oro/pkg/protocol"
)

// ListModel holds state for the dense list view.
type ListModel struct {
	beads       []protocol.Bead
	workers     []WorkerStatus
	assignments map[string]string
	width       int
	height      int
}

// NewListModel creates a new empty ListModel.
func NewListModel() ListModel {
	return ListModel{}
}

// updateBeads stores refreshed bead data.
func (lm ListModel) updateBeads(beads []protocol.Bead) ListModel {
	lm.beads = beads
	return lm
}

// updateWorkers stores refreshed worker data.
func (lm ListModel) updateWorkers(workers []WorkerStatus, assignments map[string]string) ListModel {
	lm.workers = workers
	lm.assignments = assignments
	return lm
}

// resize updates the available dimensions.
func (lm ListModel) resize(width, height int) ListModel {
	lm.width = width
	lm.height = height
	return lm
}

// View renders the list view as a dense table of beads grouped by status.
func (lm ListModel) View(_ Theme, styles Styles, width, height int) string {
	if len(lm.beads) == 0 {
		return styles.Muted.Render("No beads found. Run `bd create` to get started.")
	}

	// Group beads by status
	groups := map[string][]protocol.Bead{
		"in_progress": {},
		"open":        {},
		"blocked":     {},
		"closed":      {},
	}
	for _, b := range lm.beads {
		status := b.Status
		if _, ok := groups[status]; !ok {
			status = "open"
		}
		groups[status] = append(groups[status], b)
	}

	var out strings.Builder
	renderOrder := []string{"in_progress", "open", "blocked", "closed"}
	for _, status := range renderOrder {
		beads := groups[status]
		if len(beads) == 0 {
			continue
		}
		header := statusHeader(status, len(beads), styles)
		out.WriteString(header + "\n")
		for _, b := range beads {
			out.WriteString(renderListRow(b, width, styles) + "\n")
		}
		out.WriteString("\n")
	}

	return lipgloss.NewStyle().Width(width).Height(height).Render(out.String())
}

// statusHeader renders a group header like "In Progress (3)".
func statusHeader(status string, count int, styles Styles) string {
	labels := map[string]string{
		"in_progress": "In Progress",
		"open":        "Ready",
		"blocked":     "Blocked",
		"closed":      "Done",
	}
	label := labels[status]
	if label == "" {
		label = status
	}
	return styles.Header.Render(fmt.Sprintf("%s (%d)", label, count))
}

// renderListRow renders a single bead as a compact row.
func renderListRow(b protocol.Bead, width int, styles Styles) string {
	id := styles.IDMuted.Render(b.ID)
	title := b.Title
	maxTitle := width - 30
	if maxTitle < 10 {
		maxTitle = 10
	}
	if len(title) > maxTitle {
		title = title[:maxTitle-3] + "..."
	}
	priority := fmt.Sprintf("P%d", b.Priority)
	return fmt.Sprintf("  %s  %-*s  %s", id, maxTitle, title, styles.Muted.Render(priority))
}
