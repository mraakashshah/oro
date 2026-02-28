package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"oro/pkg/protocol"
)

// listRow identifies a single visible row in the list.
type listRow struct {
	isHeader bool
	status   string         // group status key (e.g. "open", "in_progress")
	bead     *protocol.Bead // non-nil for bead rows
}

// ListModel holds state for the dense list view.
type ListModel struct {
	beads        []protocol.Bead
	workers      []WorkerStatus
	assignments  map[string]string
	width        int
	height       int
	cursor       int             // flat row index into flatRows()
	collapsed    map[string]bool // status -> collapsed
	activeFilter string          // quick filter: "", "o", "c", "r"
	// Split-pane focus and detail state
	detailFocused  bool            // true when detail pane has focus
	detailSections map[string]bool // section name -> expanded (nil uses defaults)
	detailCursor   int             // section index for j/k in detail pane
	splitRatio     float64         // list pane width ratio [0.35, 0.75]
}

// NewListModel creates a new empty ListModel.
func NewListModel() ListModel {
	return ListModel{
		splitRatio:     0.5,
		detailSections: defaultDetailSections(),
	}
}

// updateBeads stores refreshed bead data and restores cursor by bead ID.
func (lm ListModel) updateBeads(beads []protocol.Bead) ListModel {
	savedID := lm.cursorBeadID()
	lm.beads = beads
	lm.restoreCursor(savedID)
	return lm
}

// updateWorkers stores refreshed worker data.
func (lm ListModel) updateWorkers(workers []WorkerStatus, assignments map[string]string) ListModel {
	lm.workers = workers
	lm.assignments = assignments
	return lm
}

// resize updates the available dimensions.
// Resets detail focus when width drops below 100 (no detail pane at narrow widths).
func (lm ListModel) resize(width, height int) ListModel {
	lm.width = width
	lm.height = height
	if width < 100 {
		lm.detailFocused = false
	}
	return lm
}

// listRenderOrder returns the canonical order for status groups.
func listRenderOrder() []string {
	return []string{"open", "closed"}
}

// flatRows returns the visible rows: headers + bead rows for expanded groups.
// Empty groups are omitted. Collapsed groups show only their header.
func (lm ListModel) flatRows() []listRow {
	groups := groupBeads(lm.filteredBeads())
	rows := make([]listRow, 0, len(lm.beads)+4)
	for _, status := range listRenderOrder() {
		beads := groups[status]
		if len(beads) == 0 {
			continue
		}
		rows = append(rows, listRow{isHeader: true, status: status})
		if !lm.collapsed[status] {
			for i := range beads {
				rows = append(rows, listRow{status: status, bead: &beads[i]})
			}
		}
	}
	return rows
}

// moveDown moves the cursor one row down (clamps at last visible row).
func (lm ListModel) moveDown() ListModel {
	rows := lm.flatRows()
	if lm.cursor < len(rows)-1 {
		lm.cursor++
	}
	return lm
}

// moveUp moves the cursor one row up (clamps at 0).
func (lm ListModel) moveUp() ListModel {
	if lm.cursor > 0 {
		lm.cursor--
	}
	return lm
}

// toggleAtCursor toggles collapse on a header row. No-op on bead rows.
func (lm ListModel) toggleAtCursor() ListModel {
	rows := lm.flatRows()
	if lm.cursor < 0 || lm.cursor >= len(rows) {
		return lm
	}
	row := rows[lm.cursor]
	if !row.isHeader {
		return lm
	}
	if lm.collapsed == nil {
		lm.collapsed = map[string]bool{}
	}
	lm.collapsed[row.status] = !lm.collapsed[row.status]
	// Clamp cursor to new visible range
	newRows := lm.flatRows()
	if lm.cursor >= len(newRows) {
		lm.cursor = len(newRows) - 1
	}
	if lm.cursor < 0 {
		lm.cursor = 0
	}
	return lm
}

// cursorBeadID returns the bead ID at the current cursor, or "" if on a header.
func (lm ListModel) cursorBeadID() string {
	rows := lm.flatRows()
	if lm.cursor < 0 || lm.cursor >= len(rows) {
		return ""
	}
	row := rows[lm.cursor]
	if row.isHeader || row.bead == nil {
		return ""
	}
	return row.bead.ID
}

// restoreCursor finds a bead by ID in the flat rows and sets the cursor to it.
// If beadID is empty (first load), advances to the first non-header row.
// If the bead is not found, clamps the cursor to the valid range.
func (lm *ListModel) restoreCursor(beadID string) {
	if beadID == "" {
		rows := lm.flatRows()
		for i, row := range rows {
			if row.bead != nil {
				lm.cursor = i
				return
			}
		}
		return
	}
	rows := lm.flatRows()
	for i, row := range rows {
		if row.bead != nil && row.bead.ID == beadID {
			lm.cursor = i
			return
		}
	}
	// Bead not found — clamp cursor
	if lm.cursor >= len(rows) {
		lm.cursor = len(rows) - 1
	}
	if lm.cursor < 0 {
		lm.cursor = 0
	}
}

// setFilter sets or toggles a quick filter. Same filter again clears it.
func (lm ListModel) setFilter(f string) ListModel {
	if lm.activeFilter == f {
		lm.activeFilter = ""
	} else {
		lm.activeFilter = f
	}
	return lm
}

// filteredBeads returns beads matching the active filter. No filter returns all.
func (lm ListModel) filteredBeads() []protocol.Bead {
	if lm.activeFilter == "" {
		return lm.beads
	}
	result := make([]protocol.Bead, 0, len(lm.beads))
	for _, b := range lm.beads {
		if lm.matchesFilter(b) {
			result = append(result, b)
		}
	}
	return result
}

// matchesFilter checks if a bead matches the active filter.
func (lm ListModel) matchesFilter(b protocol.Bead) bool {
	switch lm.activeFilter {
	case "o":
		return b.Status == "open" || b.Status == "in_progress"
	case "c":
		return b.Status == "closed"
	case "r":
		return b.Status == "open"
	default:
		return true
	}
}

// filterLabel returns a display label for the active filter, or "" if none.
func (lm ListModel) filterLabel() string {
	switch lm.activeFilter {
	case "o":
		return "Open"
	case "c":
		return "Closed"
	case "r":
		return "Ready"
	default:
		return ""
	}
}

// toggleFocus switches focus between list and detail panes.
func (lm ListModel) toggleFocus() ListModel {
	lm.detailFocused = !lm.detailFocused
	return lm
}

// unfocusDetail returns focus to the list pane.
func (lm ListModel) unfocusDetail() ListModel {
	lm.detailFocused = false
	return lm
}

// detailSectionKeys returns the ordered section keys for the current bead.
func detailSectionKeys() []string {
	return []string{"acceptance", "worker", "deps", "notes"}
}

// toggleDetailSection toggles the section at detailCursor.
func (lm ListModel) toggleDetailSection() ListModel {
	keys := detailSectionKeys()
	if lm.detailCursor < 0 || lm.detailCursor >= len(keys) {
		return lm
	}
	if lm.detailSections == nil {
		lm.detailSections = defaultDetailSections()
	}
	key := keys[lm.detailCursor]
	lm.detailSections[key] = !lm.detailSections[key]
	return lm
}

// detailMoveDown moves the detail cursor down (clamps at last section).
func (lm ListModel) detailMoveDown() ListModel {
	keys := detailSectionKeys()
	if lm.detailCursor < len(keys)-1 {
		lm.detailCursor++
	}
	return lm
}

// detailMoveUp moves the detail cursor up (clamps at 0).
func (lm ListModel) detailMoveUp() ListModel {
	if lm.detailCursor > 0 {
		lm.detailCursor--
	}
	return lm
}

// adjustSplit changes the split ratio by delta, clamped to [0.35, 0.75].
func (lm ListModel) adjustSplit(delta float64) ListModel {
	lm.splitRatio += delta
	if lm.splitRatio < 0.35 {
		lm.splitRatio = 0.35
	}
	if lm.splitRatio > 0.75 {
		lm.splitRatio = 0.75
	}
	return lm
}

// hasVisibleBeads returns true if any bead rows are visible (not all collapsed).
func (lm ListModel) hasVisibleBeads() bool {
	for _, row := range lm.flatRows() {
		if !row.isHeader {
			return true
		}
	}
	return false
}

// cursorBead returns the bead at the current cursor position, or nil if on a header row.
func (lm ListModel) cursorBead() *protocol.Bead {
	rows := lm.flatRows()
	if lm.cursor < 0 || lm.cursor >= len(rows) {
		return nil
	}
	row := rows[lm.cursor]
	if row.isHeader || row.bead == nil {
		return nil
	}
	return row.bead
}

// renderList renders the flat bead list with headers and row highlighting.
func (lm ListModel) renderList(styles Styles, width, height int) string {
	rows := lm.flatRows()
	groups := groupBeads(lm.filteredBeads())

	var out strings.Builder

	if label := lm.filterLabel(); label != "" {
		out.WriteString(styles.StatusLabel.Render(fmt.Sprintf("Filter: %s", label)) + "\n")
	}

	lastStatus := ""
	for i, row := range rows {
		active := i == lm.cursor
		if row.isHeader {
			if lastStatus != "" {
				out.WriteString("\n")
			}
			out.WriteString(lm.renderHeaderRow(row.status, len(groups[row.status]), active, styles) + "\n")
			lastStatus = row.status
			continue
		}
		line := lm.renderRow(*row.bead, width, styles)
		if active {
			line = styles.Highlight.Render(line)
		}
		out.WriteString(line + "\n")
	}

	return lipgloss.NewStyle().Width(width).Height(height).Render(out.String())
}

// View renders the list view as a dense table of beads grouped by status.
// When detailFocused=true and width >= 100, renders a split-pane layout with
// the list on the left and detail pane on the right.
func (lm ListModel) View(_ Theme, styles Styles, width, height int) string {
	if len(lm.beads) == 0 {
		return styles.Muted.Render("No beads found. Run `bd create` to get started.")
	}

	if !lm.hasVisibleBeads() {
		return styles.Muted.Render("No beads match")
	}

	if lm.detailFocused && width >= 100 {
		bead := lm.cursorBead()
		if bead != nil {
			listWidth := int(float64(width) * lm.splitRatio)
			detailWidth := width - listWidth
			listPane := lm.renderList(styles, listWidth, height)
			detailPane := renderDetailPane(*bead, lm.workers, lm.assignments, lm.detailSections, styles, detailWidth, height)
			return lipgloss.JoinHorizontal(lipgloss.Top, listPane, detailPane)
		}
	}

	return lm.renderList(styles, width, height)
}

// topoGraph holds the data structures for topological sort.
type topoGraph struct {
	beadMap     map[string]protocol.Bead
	prereqCount map[string]int
	successors  map[string][]string
}

// buildTopoGraph builds prereqCount and successors from "blocks" deps only.
// prereqCount[id] = number of prerequisites bead id must wait for.
// successors[id] = beads that list id as a prerequisite.
func buildTopoGraph(beads []protocol.Bead) topoGraph {
	g := topoGraph{
		beadMap:     make(map[string]protocol.Bead, len(beads)),
		prereqCount: make(map[string]int, len(beads)),
		successors:  make(map[string][]string, len(beads)),
	}
	for _, b := range beads {
		g.beadMap[b.ID] = b
		g.prereqCount[b.ID] = 0
	}
	for _, b := range beads {
		for _, dep := range b.Dependencies {
			if dep.Type != "blocks" {
				continue
			}
			g.prereqCount[b.ID]++
			g.successors[dep.DependsOnID] = append(g.successors[dep.DependsOnID], b.ID)
		}
	}
	return g
}

// topoSortBeads returns beads in topological order (prerequisites before dependents).
// Within the same topological level, beads are sorted by priority ascending (P0 first).
// Only "blocks"-type dependencies are considered. If a cycle is detected, falls back
// to a pure priority sort.
func topoSortBeads(beads []protocol.Bead) []protocol.Bead {
	if len(beads) == 0 {
		return beads
	}

	g := buildTopoGraph(beads)

	byPriority := func(a, b string) int { return g.beadMap[a].Priority - g.beadMap[b].Priority }

	// Seed queue with nodes that have no prerequisites, sorted by priority.
	queue := make([]string, 0, len(beads))
	for _, b := range beads {
		if g.prereqCount[b.ID] == 0 {
			queue = append(queue, b.ID)
		}
	}
	slices.SortStableFunc(queue, byPriority)

	// Kahn's algorithm: always pick the highest-priority ready node.
	order := make([]string, 0, len(beads))
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		order = append(order, node)

		for _, succ := range g.successors[node] {
			g.prereqCount[succ]--
			if g.prereqCount[succ] == 0 {
				queue = append(queue, succ)
			}
		}
		slices.SortStableFunc(queue, byPriority)
	}

	// Cycle detected → fall back to priority-only sort.
	if len(order) != len(beads) {
		result := make([]protocol.Bead, len(beads))
		copy(result, beads)
		slices.SortStableFunc(result, func(a, b protocol.Bead) int { return a.Priority - b.Priority })
		return result
	}

	result := make([]protocol.Bead, 0, len(beads))
	for _, id := range order {
		result = append(result, g.beadMap[id])
	}
	return result
}

// groupBeads groups beads into two buckets:
//   - "open": in_progress, open, blocked, and any unknown status — sorted by priority ascending.
//   - "closed": closed beads — sorted by UpdatedAt descending (newest first).
func groupBeads(beads []protocol.Bead) map[string][]protocol.Bead {
	groups := map[string][]protocol.Bead{
		"open":   {},
		"closed": {},
	}
	for _, b := range beads {
		if b.Status == "closed" {
			groups["closed"] = append(groups["closed"], b)
		} else {
			groups["open"] = append(groups["open"], b)
		}
	}

	// Sort open by priority ascending (P0 = most critical).
	slices.SortStableFunc(groups["open"], func(a, b protocol.Bead) int {
		return a.Priority - b.Priority
	})

	// Sort closed by UpdatedAt descending (newest first).
	slices.SortStableFunc(groups["closed"], func(a, b protocol.Bead) int {
		ta := parseBeadTime(a.UpdatedAt)
		tb := parseBeadTime(b.UpdatedAt)
		if ta.After(tb) {
			return -1
		}
		if tb.After(ta) {
			return 1
		}
		return 0
	})

	return groups
}

// renderHeaderRow renders a group header with collapse indicator and optional cursor highlight.
func (lm ListModel) renderHeaderRow(status string, count int, active bool, styles Styles) string {
	indicator := "▼"
	if lm.collapsed[status] {
		indicator = "▶"
	}
	header := fmt.Sprintf("%s %s", indicator, renderGroupHeader(status, count, styles))
	if active {
		header = styles.Highlight.Render(header)
	}
	return header
}

// renderGroupHeader renders a group header like "Open (3)".
func renderGroupHeader(status string, count int, styles Styles) string {
	labels := map[string]string{
		"open":   "Open",
		"closed": "Closed",
	}
	label := labels[status]
	if label == "" {
		label = status
	}
	return styles.Header.Render(fmt.Sprintf("%s (%d)", label, count))
}

// renderRow renders a single bead as a compact list row showing
// icon + priority + ID + title + worker + ctx%.
// Column visibility adapts to terminal width:
//   - >120: worker ID + ctx%
//   - 100-120: worker ID only (hide ctx%)
//   - <100: no worker info (list-only mode)
//   - <80: truncate bead ID (first 5 chars + "...")
func (lm ListModel) renderRow(b protocol.Bead, width int, styles Styles) string {
	icon := renderTreeTypeIcon(b.Type)
	priority := renderTreePriorityBadge(b.Priority, styles)

	// Truncate bead ID at narrow widths (<80).
	idText := b.ID
	if width < 80 && len([]rune(idText)) > 8 {
		idText = string([]rune(idText)[:5]) + "..."
	}
	id := styles.IDMuted.Render(idText)

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

	// Look up worker assignment — only shown when width >= 100.
	workerPart := renderWorkerPart(lm.workers, lm.assignments, b.ID, width, styles)

	return fmt.Sprintf("  %s %s %s  %-*s%s", icon, priority, id, maxTitle, title, workerPart)
}

// renderWorkerPart returns the styled worker portion of a list row.
// width>120: "workerID pct%", width 100-120: "workerID", width<100: "".
func renderWorkerPart(workers []WorkerStatus, assignments map[string]string, beadID string, width int, styles Styles) string {
	if width < 100 || assignments == nil {
		return ""
	}
	workerID, ok := assignments[beadID]
	if !ok || workerID == "" {
		return ""
	}
	if width > 120 {
		ctxPct := workerContextPct(workers, workerID)
		return styles.Muted.Render(fmt.Sprintf(" %s %d%%", workerID, ctxPct))
	}
	return styles.Muted.Render(fmt.Sprintf(" %s", workerID))
}

// workerContextPct returns the ContextPct for the given workerID, or 0 if not found.
func workerContextPct(workers []WorkerStatus, workerID string) int {
	for _, w := range workers {
		if w.ID == workerID {
			return w.ContextPct
		}
	}
	return 0
}

// defaultDetailSections returns the default expanded/collapsed state for detail sections.
// Acceptance and Worker are expanded; Deps and Notes are collapsed.
func defaultDetailSections() map[string]bool {
	return map[string]bool{
		"acceptance": true,
		"worker":     true,
		"deps":       false,
		"notes":      false,
	}
}

// renderSection renders a collapsible section with a title and body.
// expanded=true shows ▼ + title + body; expanded=false shows ▶ + title only.
func renderSection(title, body string, expanded bool, styles Styles) string {
	indicator := "▶"
	if expanded {
		indicator = "▼"
	}
	header := fmt.Sprintf("%s %s", indicator, styles.StatusLabel.Render(title))
	if !expanded {
		return header
	}
	return header + "\n" + styles.Muted.Render(body)
}

// renderDetailPane renders the detail pane for a bead with expandable sections.
func renderDetailPane(b protocol.Bead, workers []WorkerStatus, assignments map[string]string, sections map[string]bool, styles Styles, width, height int) string {
	var out strings.Builder

	// Header: ID + title + status
	out.WriteString(styles.Header.Render(b.ID) + "\n")
	out.WriteString(b.Title + "\n")
	out.WriteString(styles.Muted.Render(b.Status) + "\n\n")

	// Acceptance section (only if content exists)
	if b.AcceptanceCriteria != "" {
		out.WriteString(renderSection("Acceptance", b.AcceptanceCriteria, sections["acceptance"], styles) + "\n\n")
	}

	// Worker section (only if assigned)
	if assignments != nil {
		if workerID, ok := assignments[b.ID]; ok && workerID != "" {
			ctxPct := 0
			for _, w := range workers {
				if w.ID == workerID {
					ctxPct = w.ContextPct
					break
				}
			}
			body := fmt.Sprintf("%s  %d%%", workerID, ctxPct)
			out.WriteString(renderSection("Worker", body, sections["worker"], styles) + "\n\n")
		}
	}

	// Dependencies section (only if deps exist)
	if len(b.Dependencies) > 0 {
		depLines := make([]string, 0, len(b.Dependencies))
		for _, dep := range b.Dependencies {
			depLines = append(depLines, fmt.Sprintf("%s: %s", dep.Type, dep.DependsOnID))
		}
		out.WriteString(renderSection("Dependencies", strings.Join(depLines, "\n"), sections["deps"], styles) + "\n\n")
	}

	// Notes section (only if notes exist)
	if b.Notes != "" {
		out.WriteString(renderSection("Notes", b.Notes, sections["notes"], styles) + "\n\n")
	}

	return lipgloss.NewStyle().Width(width).Height(height).Render(out.String())
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

// renderTreeTypeIcon returns the emoji icon for a bead type.
// Used by both the list and board views.
func renderTreeTypeIcon(beadType string) string {
	switch beadType {
	case "task":
		return "📋"
	case "bug":
		return "🐛"
	case "feature":
		return "🪶"
	case "epic":
		return "🎯"
	default:
		return ""
	}
}
