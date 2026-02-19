package main

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"oro/pkg/protocol"
)

// treeGroup represents an epic (or the synthetic "no epic" group) and its child beads.
type treeGroup struct {
	epic     protocol.Bead   // The epic bead; ID="" for orphan group
	children []protocol.Bead // Child beads belonging to this epic
	expanded bool            // Whether the group is expanded
}

// TreeModel holds the state for the "All Beads" tree view.
type TreeModel struct {
	groups []treeGroup
	cursor int // Flat row index: 0 = first epic header, 1..N = children of that epic, etc.
}

// NewTreeModel builds a TreeModel from a flat bead slice.
// Epics become collapsible group headers; non-epic beads are grouped under their epic.
// Beads with no epic are placed in a synthetic "Untracked" group at the end.
// Within each group children are sorted by priority (ascending), then by ID for stability.
// Epics themselves are sorted by priority (ascending).
func NewTreeModel(beads []protocol.Bead) TreeModel {
	epicMap, epicOrder, orphans := partitionBeads(beads)
	groups := buildSortedGroups(epicMap, epicOrder, orphans)
	return TreeModel{groups: groups, cursor: 0}
}

// partitionBeads separates beads into epic groups and orphans.
// Returns epicMap (epic ID -> group), epicOrder (insertion order), and orphan beads.
func partitionBeads(beads []protocol.Bead) (epicMap map[string]*treeGroup, epicOrder []string, orphans []protocol.Bead) {
	epicMap = map[string]*treeGroup{}
	orphans = make([]protocol.Bead, 0)

	for _, b := range beads {
		if b.Type == "epic" {
			upsertEpicGroup(epicMap, &epicOrder, b)
			continue
		}
		if b.Epic != "" {
			ensureEpicGroup(epicMap, &epicOrder, b.Epic)
			epicMap[b.Epic].children = append(epicMap[b.Epic].children, b)
		} else {
			orphans = append(orphans, b)
		}
	}
	return epicMap, epicOrder, orphans
}

// upsertEpicGroup ensures an epic bead is recorded as a group header.
func upsertEpicGroup(epicMap map[string]*treeGroup, epicOrder *[]string, b protocol.Bead) {
	if _, ok := epicMap[b.ID]; !ok {
		epicMap[b.ID] = &treeGroup{epic: b, expanded: true}
		*epicOrder = append(*epicOrder, b.ID)
	} else {
		// Fill in the epic bead (may have been pre-created for child routing)
		epicMap[b.ID].epic = b
	}
}

// ensureEpicGroup creates a placeholder group for an epic ID if not yet seen.
func ensureEpicGroup(epicMap map[string]*treeGroup, epicOrder *[]string, epicID string) {
	if _, ok := epicMap[epicID]; !ok {
		epicMap[epicID] = &treeGroup{epic: protocol.Bead{ID: epicID}, expanded: true}
		*epicOrder = append(*epicOrder, epicID)
	}
}

// buildSortedGroups assembles the final ordered group slice from the partitioned data.
func buildSortedGroups(epicMap map[string]*treeGroup, epicOrder []string, orphans []protocol.Bead) []treeGroup {
	sortEpicOrder(epicMap, epicOrder)

	groups := make([]treeGroup, 0, len(epicOrder)+1)
	for _, id := range epicOrder {
		g := *epicMap[id]
		sortBeadsByPriority(g.children)
		groups = append(groups, g)
	}

	if len(orphans) > 0 {
		sortBeadsByPriority(orphans)
		groups = append(groups, treeGroup{
			epic:     protocol.Bead{ID: "", Title: "Untracked"},
			children: orphans,
			expanded: true,
		})
	}
	return groups
}

// sortEpicOrder sorts epic IDs by the priority of their epic bead, then by ID for stability.
func sortEpicOrder(epicMap map[string]*treeGroup, epicOrder []string) {
	sort.SliceStable(epicOrder, func(i, j int) bool {
		pi := epicMap[epicOrder[i]].epic.Priority
		pj := epicMap[epicOrder[j]].epic.Priority
		if pi != pj {
			return pi < pj
		}
		return epicOrder[i] < epicOrder[j]
	})
}

// sortBeadsByPriority sorts a bead slice by priority ascending, then by ID for stability.
func sortBeadsByPriority(beads []protocol.Bead) {
	sort.SliceStable(beads, func(i, j int) bool {
		if beads[i].Priority != beads[j].Priority {
			return beads[i].Priority < beads[j].Priority
		}
		return beads[i].ID < beads[j].ID
	})
}

// flatRows returns the list of visible row indices as (groupIdx, childIdx) pairs.
// groupIdx is the treeGroup index; childIdx = -1 means the row is the epic header.
// Only rows that are visible (i.e. groups are expanded or it's a header) are returned.
func (tm TreeModel) flatRows() []treeRow {
	rows := make([]treeRow, 0, len(tm.groups)*4)
	for gi, g := range tm.groups {
		rows = append(rows, treeRow{groupIdx: gi, childIdx: -1})
		if g.expanded {
			for ci := range g.children {
				rows = append(rows, treeRow{groupIdx: gi, childIdx: ci})
			}
		}
	}
	return rows
}

// treeRow identifies a single visible row in the tree.
type treeRow struct {
	groupIdx int // index into TreeModel.groups
	childIdx int // -1 = epic header row; >= 0 = child bead row
}

// toggleCollapse toggles the expanded state of the group at groupIdx.
func (tm TreeModel) toggleCollapse(groupIdx int) TreeModel {
	if groupIdx < 0 || groupIdx >= len(tm.groups) {
		return tm
	}
	// Copy groups slice to avoid mutating the original
	newGroups := make([]treeGroup, len(tm.groups))
	copy(newGroups, tm.groups)
	newGroups[groupIdx].expanded = !newGroups[groupIdx].expanded
	tm.groups = newGroups
	return tm
}

// moveDown moves the cursor one row down (clamps at last visible row).
func (tm TreeModel) moveDown() TreeModel {
	rows := tm.flatRows()
	if tm.cursor < len(rows)-1 {
		tm.cursor++
	}
	return tm
}

// moveUp moves the cursor one row up (clamps at 0).
func (tm TreeModel) moveUp() TreeModel {
	if tm.cursor > 0 {
		tm.cursor--
	}
	return tm
}

// toggleAtCursor toggles the collapse state of the epic group at the current cursor position.
// If the cursor is on a child row, it toggles that child's parent group.
func (tm TreeModel) toggleAtCursor() TreeModel {
	rows := tm.flatRows()
	if tm.cursor < 0 || tm.cursor >= len(rows) {
		return tm
	}
	row := rows[tm.cursor]
	return tm.toggleCollapse(row.groupIdx)
}

// View renders the tree as a string suitable for the dashboard.
func (tm TreeModel) View(theme Theme, styles Styles) string {
	if len(tm.groups) == 0 {
		return styles.Muted.Render("No beads found.")
	}

	rows := tm.flatRows()
	var sb strings.Builder

	// Title
	sb.WriteString(styles.Header.Render("All Beads"))
	sb.WriteString("\n")
	sb.WriteString(styles.Muted.Render("j/k navigate  space expand/collapse  esc back"))
	sb.WriteString("\n\n")

	for rowIdx, row := range rows {
		g := tm.groups[row.groupIdx]
		active := rowIdx == tm.cursor

		if row.childIdx == -1 {
			sb.WriteString(tm.renderEpicRow(g, active, theme, styles))
		} else {
			child := g.children[row.childIdx]
			sb.WriteString(tm.renderChildRow(child, active, styles))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// renderEpicRow renders a single epic group header row.
func (tm TreeModel) renderEpicRow(g treeGroup, active bool, theme Theme, styles Styles) string {
	indicator := "▼"
	if !g.expanded {
		indicator = "▶"
	}

	epicTitle := g.epic.Title
	if epicTitle == "" {
		epicTitle = g.epic.ID
	}
	if epicTitle == "" {
		epicTitle = "Untracked"
	}

	done, total := countEpicProgress(g.children)
	progressBar := renderProgressBar(done, total, 8)

	priorityBadge := ""
	if g.epic.ID != "" {
		priorityBadge = renderTreePriorityBadge(g.epic.Priority, styles) + " "
	}

	childCount := fmt.Sprintf("(%d)", len(g.children))
	line := fmt.Sprintf("%s %s%s %s %s", indicator, priorityBadge, epicTitle, childCount, progressBar)

	epicHeaderStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.Primary)
	if active {
		epicHeaderStyle = epicHeaderStyle.Background(lipgloss.Color("#3a3a3a"))
	}
	return epicHeaderStyle.Render(line)
}

// renderChildRow renders a single child bead row indented under its epic.
func (tm TreeModel) renderChildRow(b protocol.Bead, active bool, styles Styles) string {
	priorityBadge := renderTreePriorityBadge(b.Priority, styles)
	statusBadge := renderTreeStatusBadge(b.Status, styles)
	typeIcon := renderTreeTypeIcon(b.Type)

	line := fmt.Sprintf("  %s %s %s %s  %s", priorityBadge, statusBadge, typeIcon, b.Title, styles.IDMuted.Render(b.ID))

	if active {
		return lipgloss.NewStyle().Background(lipgloss.Color("#3a3a3a")).Render(line)
	}
	return line
}

// renderProgressBar renders a block progress bar of the given width.
// Format: [████░░░░] 3/5
func renderProgressBar(done, total, width int) string {
	if total == 0 {
		empty := strings.Repeat("░", width)
		return fmt.Sprintf("[%s] 0/0", empty)
	}

	filled := (done * width) / total
	if filled > width {
		filled = width
	}
	empty := width - filled

	bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)
	return fmt.Sprintf("[%s] %d/%d", bar, done, total)
}

// countEpicProgress counts done and total children for a group.
// "closed" and "done" statuses count as done.
func countEpicProgress(children []protocol.Bead) (done, total int) {
	total = len(children)
	for _, c := range children {
		if c.Status == "closed" || c.Status == "done" {
			done++
		}
	}
	return done, total
}

// renderTreePriorityBadge returns a compact priority badge string with color.
func renderTreePriorityBadge(priority int, styles Styles) string {
	badge := fmt.Sprintf("[P%d]", priority)
	switch priority {
	case 0:
		return styles.BadgeP0.Render(badge)
	case 1:
		return styles.BadgeP1.Render(badge)
	case 2:
		return styles.BadgeP2.Render(badge)
	case 3:
		return styles.BadgeP3.Render(badge)
	default:
		return styles.BadgeP4.Render(badge)
	}
}

// renderTreeStatusBadge returns a compact colored status badge.
func renderTreeStatusBadge(status string, styles Styles) string {
	switch status {
	case "open":
		return styles.BadgeReady.Render("[open]")
	case "in_progress":
		return styles.BadgeInProgress.Render("[wip]")
	case "blocked":
		return styles.BadgeBlocked.Render("[blk]")
	case "closed":
		return styles.Success.Render("[done]")
	default:
		return styles.Muted.Render(fmt.Sprintf("[%s]", status))
	}
}

// renderTreeTypeIcon returns a single-character icon for a bead type.
func renderTreeTypeIcon(beadType string) string {
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

// handleTreeViewKeys processes keyboard input in TreeView.
func (m Model) handleTreeViewKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.activeView = BoardView
	case "j", "down":
		m.treeModel = m.treeModel.moveDown()
	case "k", "up":
		m.treeModel = m.treeModel.moveUp()
	case " ":
		m.treeModel = m.treeModel.toggleAtCursor()
	}
	return m, nil
}
