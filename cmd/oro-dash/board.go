package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"oro/pkg/protocol"
)

// BoardModel holds the kanban-style board state with bead columns.
type BoardModel struct {
	columns []boardColumn
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
			beadsInCol = beadsInCol[:10]
		}

		columns = append(columns, boardColumn{
			title:      t,
			beads:      beadsInCol,
			totalCount: totalCount,
		})
	}

	return BoardModel{columns: columns}
}

// Render renders the board columns side-by-side using lipgloss.
func (bm BoardModel) Render() string {
	theme := DefaultTheme()

	colWidth := 30

	cardStyle := lipgloss.NewStyle().
		Width(colWidth-2).
		Padding(0, 1)

	idStyle := lipgloss.NewStyle().
		Foreground(theme.Muted)

	columnStyle := lipgloss.NewStyle().
		Width(colWidth).
		Padding(0, 1)

	rendered := make([]string, 0, len(bm.columns))
	for _, col := range bm.columns {
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

		header := headerStyle.Render(headerText)

		var cardsBuilder strings.Builder
		for _, b := range col.beads {
			card := cardStyle.Render(
				fmt.Sprintf("%s\n%s", b.Title, idStyle.Render(b.ID)),
			)
			cardsBuilder.WriteString(card)
			cardsBuilder.WriteString("\n")
		}
		cards := cardsBuilder.String()

		full := columnStyle.Render(header + "\n" + cards)
		rendered = append(rendered, full)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
}
