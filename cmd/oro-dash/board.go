package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

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

		// Sort all columns by UpdatedAt descending (most recent first).
		// Beads with empty UpdatedAt parse as zero-time and sort as oldest.
		slices.SortStableFunc(beadsInCol, func(a, b protocol.Bead) int {
			return parseBeadTime(b.UpdatedAt).Compare(parseBeadTime(a.UpdatedAt))
		})

		// Cap Done column to 10 most recent to keep the board scannable.
		const doneLimit = 10
		if t == "Done" && len(beadsInCol) > doneLimit {
			beadsInCol = beadsInCol[:doneLimit]
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
	var noScroll [4]int
	return bm.RenderWithScroll(activeCol, activeBead, colWidth, noScroll, 0, theme, styles)
}

// RenderWithScroll renders the board with per-column scroll offsets.
// maxVisible=0 means no limit (show all beads).
func (bm BoardModel) RenderWithScroll(activeCol, activeBead, colWidth int, scrollOffsets [4]int, maxVisible int, theme Theme, styles Styles) string {
	rendered := make([]string, 0, len(bm.columns))
	for colIdx, col := range bm.columns {
		offset := 0
		if colIdx < len(scrollOffsets) {
			offset = scrollOffsets[colIdx] //nolint:gosec // bounds checked on line above
		}
		full := bm.renderColumnWithScroll(col, colIdx, activeCol, activeBead, colWidth, offset, maxVisible, theme, styles)
		rendered = append(rendered, full)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
}

// renderColumnWithScroll renders a column with scroll offset and visible limit.
// scrollOffset is the index of the first visible bead; maxVisible=0 means show all.
func (bm BoardModel) renderColumnWithScroll(col boardColumn, colIdx, activeCol, activeBead, colWidth, scrollOffset, maxVisible int, theme Theme, styles Styles) string {
	// Pre-compute card styles with width
	cardStyle := styles.Card.Width(colWidth - 2)
	activeCardStyle := styles.ActiveCard.Width(colWidth - 2).Background(theme.ColorFocus)
	columnStyle := styles.Column.Width(colWidth)

	header := bm.renderColumnHeader(col, colWidth, theme, styles)

	var cardsBuilder strings.Builder

	// If column is empty, show "no items" placeholder
	if len(col.beads) == 0 {
		emptyMsg := styles.Muted.Render("no items")
		cardsBuilder.WriteString(emptyMsg)
		return columnStyle.Render(header + "\n" + cardsBuilder.String())
	}

	// Calculate visible window
	start := scrollOffset
	if start < 0 {
		start = 0
	}
	if start > len(col.beads) {
		start = len(col.beads)
	}
	end := len(col.beads)
	if maxVisible > 0 && start+maxVisible < end {
		end = start + maxVisible
	}

	// Show "above" indicator
	if start > 0 {
		cardsBuilder.WriteString(styles.Muted.Render(fmt.Sprintf("  ▲ %d more", start)))
		cardsBuilder.WriteString("\n")
	}

	for beadIdx := start; beadIdx < end; beadIdx++ {
		b := col.beads[beadIdx]
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

	// Show "below" indicator
	if remaining := len(col.beads) - end; remaining > 0 {
		cardsBuilder.WriteString(styles.Muted.Render(fmt.Sprintf("  ▼ %d more", remaining)))
		cardsBuilder.WriteString("\n")
	}

	cards := cardsBuilder.String()

	return columnStyle.Render(header + "\n" + cards)
}

// renderColumnHeader renders a column header with title and optional count.
func (bm BoardModel) renderColumnHeader(col boardColumn, colWidth int, theme Theme, styles Styles) string {
	// Use status-appropriate colors: each column gets its semantic color.
	var headerColor lipgloss.Color
	switch col.title {
	case "Done":
		headerColor = theme.Success
	case "Blocked":
		headerColor = theme.ColorBlocked
	case "In Progress":
		headerColor = theme.ColorInProgress
	default: // "Ready" and any unknown column
		headerColor = theme.ColorReady
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
// Title is truncated to fit within the available card width.
func (bm BoardModel) renderCardHeader(b protocol.Bead, styles Styles) string {
	headerParts := make([]string, 0, 3)

	// Priority badge with color
	priorityBadge := bm.renderPriorityBadge(b.Priority, styles)

	// Type indicator icon
	icon := renderTreeTypeIcon(b.Type)

	// Truncate title — badge(4) + space + icon(2) + space = ~8 chars overhead.
	// Use a reasonable max to prevent overflow.
	title := b.Title
	const maxTitleLen = 30
	if len([]rune(title)) > maxTitleLen {
		title = string([]rune(title)[:maxTitleLen-3]) + "..."
	}

	headerParts = append(headerParts, priorityBadge, icon, title)

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

// parseBeadTime parses an RFC3339 UpdatedAt string.
// Returns zero time on any parse failure (e.g. empty string), so such beads
// sort as the oldest possible when sorted descending.
func parseBeadTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
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
