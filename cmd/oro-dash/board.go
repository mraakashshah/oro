package main

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"

	"oro/pkg/protocol"
)

// BoardModel holds the kanban-style board state with bead columns.
type BoardModel struct {
	columns []boardColumn
}

// boardColumn represents a single column in the board view.
type boardColumn struct {
	title string
	beads []protocol.Bead
}

// columnForStatus returns the board column title for a given bead status.
func columnForStatus(status string) string {
	switch status {
	case "in_progress":
		return "In Progress"
	case "blocked":
		return "Blocked"
	default:
		return "Ready"
	}
}

// NewBoardModel groups beads into 3 columns by status:
//   - "Ready"       = status "open"
//   - "In Progress" = status "in_progress"
//   - "Blocked"     = status "blocked"
func NewBoardModel(beads []protocol.Bead) BoardModel {
	buckets := map[string][]protocol.Bead{
		"Ready":       {},
		"In Progress": {},
		"Blocked":     {},
	}

	for _, b := range beads {
		col := columnForStatus(b.Status)
		buckets[col] = append(buckets[col], b)
	}

	// Preserve column ordering: Ready, In Progress, Blocked.
	titles := []string{"Ready", "In Progress", "Blocked"}
	columns := make([]boardColumn, 0, len(titles))
	for _, t := range titles {
		columns = append(columns, boardColumn{
			title: t,
			beads: buckets[t],
		})
	}

	return BoardModel{columns: columns}
}

// Render renders the board columns side-by-side using lipgloss.
func (bm BoardModel) Render() string {
	theme := DefaultTheme()

	colWidth := 30

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(theme.Primary).
		Width(colWidth).
		Align(lipgloss.Center).
		BorderBottom(true).
		BorderStyle(lipgloss.NormalBorder())

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
		header := headerStyle.Render(col.title)

		cards := ""
		for _, b := range col.beads {
			card := cardStyle.Render(
				fmt.Sprintf("%s\n%s", b.Title, idStyle.Render(b.ID)),
			)
			cards += card + "\n"
		}

		full := columnStyle.Render(header + "\n" + cards)
		rendered = append(rendered, full)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
}
