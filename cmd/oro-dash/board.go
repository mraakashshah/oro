package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"oro/pkg/protocol"
)

// BoardModel holds the kanban-style board state with bead columns.
type BoardModel struct {
	columns     []boardColumn
	workers     []WorkerStatus
	assignments map[string]string // bead ID -> worker ID
}

// boardColumn represents a single column in the board view.
type boardColumn struct {
	title      string
	beads      []protocol.Bead
	totalCount int // Total count of beads (may exceed len(beads) if limited)
}

// columnForStatus returns the board column title for a given bead status.
func columnForStatus(status string) string {
	switch status {
	case "in_progress":
		return "In Progress"
	case "blocked":
		return "Blocked"
	case "closed":
		return "Done"
	default:
		return "Ready"
	}
}

// NewBoardModel groups beads into 4 columns by status:
//   - "Ready"       = status "open"
//   - "In Progress" = status "in_progress"
//   - "Blocked"     = status "blocked"
//   - "Done"        = status "closed" (limited to most recent 10)
func NewBoardModel(beads []protocol.Bead) BoardModel {
	return NewBoardModelWithWorkers(beads, nil, nil)
}

// NewBoardModelWithWorkers creates a board model with worker assignment information.
func NewBoardModelWithWorkers(beads []protocol.Bead, workers []WorkerStatus, assignments map[string]string) BoardModel {
	buckets := map[string][]protocol.Bead{
		"Ready":       {},
		"In Progress": {},
		"Blocked":     {},
		"Done":        {},
	}

	for _, b := range beads {
		col := columnForStatus(b.Status)
		buckets[col] = append(buckets[col], b)
	}

	// Preserve column ordering: Ready, In Progress, Blocked, Done.
	titles := []string{"Ready", "In Progress", "Blocked", "Done"}
	columns := make([]boardColumn, 0, len(titles))
	for _, t := range titles {
		beadsInCol := buckets[t]
		totalCount := len(beadsInCol)

		// Limit Done column to most recent 10 beads
		if t == "Done" && len(beadsInCol) > 10 {
			beadsInCol = beadsInCol[len(beadsInCol)-10:]
		}

		columns = append(columns, boardColumn{
			title:      t,
			beads:      beadsInCol,
			totalCount: totalCount,
		})
	}

	return BoardModel{
		columns:     columns,
		workers:     workers,
		assignments: assignments,
	}
}

// Render renders the board columns side-by-side using lipgloss.
func (bm BoardModel) Render() string {
	return bm.RenderWithCursor(-1, -1)
}

// RenderWithCursor renders the board with a highlighted cursor at the specified column and bead.
func (bm BoardModel) RenderWithCursor(activeCol, activeBead int) string {
	theme := DefaultTheme()
	colWidth := 30

	rendered := make([]string, 0, len(bm.columns))
	for colIdx, col := range bm.columns {
		full := bm.renderColumn(col, colIdx, activeCol, activeBead, colWidth, theme)
		rendered = append(rendered, full)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
}

// renderColumn renders a single column with its header and cards.
func (bm BoardModel) renderColumn(col boardColumn, colIdx, activeCol, activeBead, colWidth int, theme Theme) string {
	cardStyle := lipgloss.NewStyle().
		Width(colWidth-2).
		Padding(0, 1)

	activeCardStyle := lipgloss.NewStyle().
		Width(colWidth-2).
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Primary).
		Background(lipgloss.Color("#3a3a3a"))

	columnStyle := lipgloss.NewStyle().
		Width(colWidth).
		Padding(0, 1)

	header := bm.renderColumnHeader(col, colWidth, theme)

	var cardsBuilder strings.Builder
	for beadIdx, b := range col.beads {
		// Use activeCardStyle if this is the active card
		style := cardStyle
		if colIdx == activeCol && beadIdx == activeBead {
			style = activeCardStyle
		}

		cardContent := bm.renderCardContent(b, theme)
		card := style.Render(cardContent)
		cardsBuilder.WriteString(card)
		cardsBuilder.WriteString("\n")
	}
	cards := cardsBuilder.String()

	return columnStyle.Render(header + "\n" + cards)
}

// renderColumnHeader renders a column header with title and optional count.
func (bm BoardModel) renderColumnHeader(col boardColumn, colWidth int, theme Theme) string {
	// Use Success (green) color for Done column, Primary (blue) for others
	headerColor := theme.Primary
	if col.title == "Done" {
		headerColor = theme.Success
	}

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(headerColor).
		Width(colWidth).
		Align(lipgloss.Center).
		BorderBottom(true).
		BorderStyle(lipgloss.NormalBorder())

	// Format header with visible/total count for Done column
	headerText := col.title
	if col.title == "Done" && col.totalCount > 0 {
		visibleCount := len(col.beads)
		headerText = fmt.Sprintf("%s (%d/%d)", col.title, visibleCount, col.totalCount)
	}

	return headerStyle.Render(headerText)
}

// renderCardContent renders the content of a single card with enriched metadata.
func (bm BoardModel) renderCardContent(b protocol.Bead, theme Theme) string {
	var parts []string

	// Line 1: Priority badge + Type indicator + Title (truncated if needed)
	headerLine := bm.renderCardHeader(b, theme)
	parts = append(parts, headerLine)

	// Line 2: Dimmed bead ID
	idStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	parts = append(parts, idStyle.Render(b.ID))

	// Line 3 (conditional): Worker info for in-progress cards
	if b.Status == "in_progress" && bm.assignments != nil {
		if workerID, ok := bm.assignments[b.ID]; ok {
			workerLine := bm.renderWorkerInfo(workerID, theme)
			parts = append(parts, workerLine)
		}
	}

	// Line 3/4 (conditional): Blocker IDs for blocked cards
	if b.Status == "blocked" && len(b.Dependencies) > 0 {
		if blockerLine := bm.renderBlockerInfo(b, theme); blockerLine != "" {
			parts = append(parts, blockerLine)
		}
	}

	return strings.Join(parts, "\n")
}

// renderCardHeader renders the first line of a card: priority badge + type icon + title.
func (bm BoardModel) renderCardHeader(b protocol.Bead, theme Theme) string {
	headerParts := make([]string, 0, 3)

	// Priority badge with color
	priorityBadge := bm.renderPriorityBadge(b.Priority, theme)

	// Type indicator icon
	typeIcon := bm.renderTypeIndicator(b.Type)

	// Title (will be truncated by card style if too long)
	headerParts = append(headerParts, priorityBadge, typeIcon, b.Title)

	return strings.Join(headerParts, " ")
}

// renderPriorityBadge returns a colored priority badge [P0]-[P4].
func (bm BoardModel) renderPriorityBadge(priority int, theme Theme) string {
	badge := fmt.Sprintf("[P%d]", priority)

	var color lipgloss.Color
	switch priority {
	case 0:
		color = theme.ColorP0
	case 1:
		color = theme.ColorP1
	case 2:
		color = theme.ColorP2
	case 3:
		color = theme.ColorP3
	case 4:
		color = theme.ColorP4
	default:
		color = theme.Muted
	}

	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(badge)
}

// renderTypeIndicator returns an icon for the bead type.
func (bm BoardModel) renderTypeIndicator(beadType string) string {
	switch beadType {
	case "task":
		return "□"
	case "bug":
		return "⚠"
	case "feature":
		return "✦"
	case "epic":
		return "◈"
	default:
		return "•"
	}
}

// renderWorkerInfo renders worker ID, health badge, and context percentage for in-progress cards.
func (bm BoardModel) renderWorkerInfo(workerID string, theme Theme) string {
	// Find worker in workers list
	var worker *WorkerStatus
	for i := range bm.workers {
		if bm.workers[i].ID == workerID {
			worker = &bm.workers[i]
			break
		}
	}

	// If worker not found in list, just show worker ID (no health badge/context)
	if worker == nil {
		workerStyle := lipgloss.NewStyle().Foreground(theme.ColorInProgress)
		return workerStyle.Render(fmt.Sprintf("👷 %s", workerID))
	}

	// Determine health color based on heartbeat age
	healthColor := bm.healthColorForWorker(*worker, theme)
	healthBadge := lipgloss.NewStyle().Foreground(healthColor).Render("●")

	// Build worker info line: health badge, worker ID, context percentage
	parts := []string{healthBadge, workerID}

	// Add context percentage if available
	if worker.ContextPct > 0 {
		contextStr := fmt.Sprintf("%d%%", worker.ContextPct)
		parts = append(parts, contextStr)
	}

	workerStyle := lipgloss.NewStyle().Foreground(theme.ColorInProgress)
	return workerStyle.Render(fmt.Sprintf("👷 %s", strings.Join(parts, " ")))
}

// healthColorForWorker returns the health badge color based on heartbeat age.
// Green (<5s), Amber (5-15s), Red (>15s).
func (bm BoardModel) healthColorForWorker(worker WorkerStatus, theme Theme) lipgloss.Color {
	switch {
	case worker.LastProgressSecs < 5.0:
		return theme.Success // Green
	case worker.LastProgressSecs <= 15.0:
		return theme.Warning // Amber
	default:
		return theme.Error // Red
	}
}

// renderBlockerInfo renders blocker bead IDs for blocked cards.
func (bm BoardModel) renderBlockerInfo(b protocol.Bead, theme Theme) string {
	var blockerIDs []string
	for _, dep := range b.Dependencies {
		if dep.Type == "blocks" {
			blockerIDs = append(blockerIDs, dep.DependsOnID)
		}
	}

	if len(blockerIDs) == 0 {
		return ""
	}

	blockerStyle := lipgloss.NewStyle().Foreground(theme.ColorBlocked)
	blockerText := "🚧 " + strings.Join(blockerIDs, ", ")
	return blockerStyle.Render(blockerText)
}
