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
func (bm BoardModel) Render(theme Theme, styles Styles) string {
	return bm.RenderWithCursor(-1, -1, theme, styles)
}

// RenderWithCursor renders the board with a highlighted cursor at the specified column and bead.
func (bm BoardModel) RenderWithCursor(activeCol, activeBead int, theme Theme, styles Styles) string {
	return bm.RenderWithCustomWidth(activeCol, activeBead, 30, theme, styles)
}

// RenderWithCustomWidth renders the board with a custom column width.
func (bm BoardModel) RenderWithCustomWidth(activeCol, activeBead, colWidth int, theme Theme, styles Styles) string {
	rendered := make([]string, 0, len(bm.columns))
	for colIdx, col := range bm.columns {
		full := bm.renderColumn(col, colIdx, activeCol, activeBead, colWidth, theme, styles)
		rendered = append(rendered, full)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
}

// renderColumn renders a single column with its header and cards.
func (bm BoardModel) renderColumn(col boardColumn, colIdx, activeCol, activeBead, colWidth int, theme Theme, styles Styles) string {
	// Pre-compute card styles with width
	cardStyle := styles.Card.Width(colWidth - 2)
	activeCardStyle := styles.ActiveCard.Width(colWidth - 2).Background(lipgloss.Color("#3a3a3a"))
	columnStyle := styles.Column.Width(colWidth)

	header := bm.renderColumnHeader(col, colWidth, theme, styles)

	var cardsBuilder strings.Builder
	for beadIdx, b := range col.beads {
		// Use activeCardStyle if this is the active card
		style := cardStyle
		if colIdx == activeCol && beadIdx == activeBead {
			style = activeCardStyle
		}

		cardContent := bm.renderCardContent(b, styles)
		card := style.Render(cardContent)
		cardsBuilder.WriteString(card)
		cardsBuilder.WriteString("\n")
	}
	cards := cardsBuilder.String()

	return columnStyle.Render(header + "\n" + cards)
}

// renderColumnHeader renders a column header with title and optional count.
func (bm BoardModel) renderColumnHeader(col boardColumn, colWidth int, theme Theme, styles Styles) string {
	// Use Success (green) color for Done column, Primary (blue) for others
	headerColor := theme.Primary
	if col.title == "Done" {
		headerColor = theme.Success
	}

	// Use pre-computed header style and override color/width
	headerStyle := styles.Header.
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
func (bm BoardModel) renderCardContent(b protocol.Bead, styles Styles) string {
	var parts []string

	// Line 1: Priority badge + Type indicator + Title (truncated if needed)
	headerLine := bm.renderCardHeader(b, styles)
	parts = append(parts,
		headerLine,
		// Line 2: Dimmed bead ID
		styles.IDMuted.Render(b.ID),
	)

	// Line 3 (conditional): Worker info for in-progress cards
	if b.Status == "in_progress" && bm.assignments != nil {
		if workerID, ok := bm.assignments[b.ID]; ok {
			workerLine := bm.renderWorkerInfo(workerID, styles)
			parts = append(parts, workerLine)
		}
	}

	// Line 3/4 (conditional): Blocker IDs for blocked cards
	if b.Status == "blocked" && len(b.Dependencies) > 0 {
		if blockerLine := bm.renderBlockerInfo(b, styles); blockerLine != "" {
			parts = append(parts, blockerLine)
		}
	}

	return strings.Join(parts, "\n")
}

// renderCardHeader renders the first line of a card: priority badge + type icon + title.
func (bm BoardModel) renderCardHeader(b protocol.Bead, styles Styles) string {
	headerParts := make([]string, 0, 3)

	// Priority badge with color
	priorityBadge := bm.renderPriorityBadge(b.Priority, styles)

	// Type indicator icon
	typeIcon := bm.renderTypeIndicator(b.Type)

	// Title (will be truncated by card style if too long)
	headerParts = append(headerParts, priorityBadge, typeIcon, b.Title)

	return strings.Join(headerParts, " ")
}

// renderPriorityBadge returns a colored priority badge [P0]-[P4].
func (bm BoardModel) renderPriorityBadge(priority int, styles Styles) string {
	badge := fmt.Sprintf("[P%d]", priority)

	var style lipgloss.Style
	switch priority {
	case 0:
		style = styles.BadgeP0
	case 1:
		style = styles.BadgeP1
	case 2:
		style = styles.BadgeP2
	case 3:
		style = styles.BadgeP3
	case 4:
		style = styles.BadgeP4
	default:
		style = styles.Muted
	}

	return style.Render(badge)
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
func (bm BoardModel) renderWorkerInfo(workerID string, styles Styles) string {
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
		return styles.WorkerStyle.Render(fmt.Sprintf("👷 %s", workerID))
	}

	// Determine health style based on heartbeat age
	healthStyle := bm.healthStyleForWorker(*worker, styles)
	healthBadge := healthStyle.Render("●")

	// Build worker info line: health badge, worker ID, context percentage
	parts := []string{healthBadge, workerID}

	// Add context percentage if available
	if worker.ContextPct > 0 {
		contextStr := fmt.Sprintf("%d%%", worker.ContextPct)
		parts = append(parts, contextStr)
	}

	return styles.WorkerStyle.Render(fmt.Sprintf("👷 %s", strings.Join(parts, " ")))
}

// healthStyleForWorker returns the health badge style based on heartbeat age.
// Green (<5s), Amber (5-15s), Red (>15s).
func (bm BoardModel) healthStyleForWorker(worker WorkerStatus, styles Styles) lipgloss.Style {
	switch {
	case worker.LastProgressSecs < 5.0:
		return styles.HealthGreen
	case worker.LastProgressSecs <= 15.0:
		return styles.HealthAmber
	default:
		return styles.HealthRed
	}
}

// renderBlockerInfo renders blocker bead IDs for blocked cards.
func (bm BoardModel) renderBlockerInfo(b protocol.Bead, styles Styles) string {
	var blockerIDs []string
	for _, dep := range b.Dependencies {
		if dep.Type == "blocks" {
			blockerIDs = append(blockerIDs, dep.DependsOnID)
		}
	}

	if len(blockerIDs) == 0 {
		return ""
	}

	// Replace hyphens with non-breaking hyphens to prevent word wrapping within IDs
	for i, id := range blockerIDs {
		blockerIDs[i] = strings.ReplaceAll(id, "-", "\u2011")
	}

	// Join with non-breaking spaces after commas
	blockerText := "🚧 " + strings.Join(blockerIDs, ",\u00A0")
	return styles.BlockerStyle.Render(blockerText)
}
