package main

import (
	"fmt"
	"slices"
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

	groups := groupBeads(lm.beads)

	var out strings.Builder
	renderOrder := []string{"in_progress", "open", "blocked", "closed"}
	for _, status := range renderOrder {
		beads := groups[status]
		if len(beads) == 0 {
			continue
		}
		out.WriteString(renderGroupHeader(status, len(beads), styles) + "\n")
		for _, b := range beads {
			out.WriteString(lm.renderRow(b, width, styles) + "\n")
		}
		out.WriteString("\n")
	}

	return lipgloss.NewStyle().Width(width).Height(height).Render(out.String())
}

// groupBeads groups beads by status, sorts each group by priority (ascending),
// and caps the "closed" group at 10.
func groupBeads(beads []protocol.Bead) map[string][]protocol.Bead {
	groups := map[string][]protocol.Bead{
		"in_progress": {},
		"open":        {},
		"blocked":     {},
		"closed":      {},
	}
	for _, b := range beads {
		status := b.Status
		if _, ok := groups[status]; !ok {
			status = "open"
		}
		groups[status] = append(groups[status], b)
	}

	// Sort each group by priority ascending (0 = most critical).
	for status, group := range groups {
		slices.SortStableFunc(group, func(a, b protocol.Bead) int {
			return a.Priority - b.Priority
		})
		groups[status] = group
	}

	// Cap Done (closed) at 10.
	if len(groups["closed"]) > 10 {
		groups["closed"] = groups["closed"][:10]
	}

	return groups
}

// renderGroupHeader renders a group header like "In Progress (3)".
func renderGroupHeader(status string, count int, styles Styles) string {
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

// renderRow renders a single bead as a compact list row showing
// icon + priority + ID + title + worker + ctx%.
func (lm ListModel) renderRow(b protocol.Bead, width int, styles Styles) string {
	icon := renderTreeTypeIcon(b.Type)
	priority := renderTreePriorityBadge(b.Priority, styles)
	id := styles.IDMuted.Render(b.ID)

	// Truncate title to fit within available width.
	// Reserve space for: 2 indent + icon(1) + 1 + priority(4) + 1 + id + 2 + worker(~15) + margin.
	maxTitle := width - 40
	if maxTitle < 10 {
		maxTitle = 10
	}
	title := b.Title
	if len([]rune(title)) > maxTitle {
		title = string([]rune(title)[:maxTitle-3]) + "..."
	}

	// Look up worker assignment.
	workerPart := ""
	if lm.assignments != nil {
		if workerID, ok := lm.assignments[b.ID]; ok && workerID != "" {
			ctxPct := 0
			for _, w := range lm.workers {
				if w.ID == workerID {
					ctxPct = w.ContextPct
					break
				}
			}
			workerPart = styles.Muted.Render(fmt.Sprintf(" %s %d%%", workerID, ctxPct))
		}
	}

	return fmt.Sprintf("  %s %s %s  %-*s%s", icon, priority, id, maxTitle, title, workerPart)
}
