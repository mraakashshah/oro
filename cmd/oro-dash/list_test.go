package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"oro/pkg/protocol"
)

// TestListModel_NewAndEmpty verifies the ListModel scaffold and ViewType wiring.
func TestListModel_NewAndEmpty(t *testing.T) {
	t.Run("ListView is in ViewType enum", func(t *testing.T) {
		// ListView should be appended after TreeView (not inserted at 0)
		if ListView <= TreeView {
			t.Errorf("ListView (%d) should be greater than TreeView (%d)", ListView, TreeView)
		}
	})

	t.Run("default view is ListView", func(t *testing.T) {
		m := newModel()
		if m.activeView != ListView {
			t.Errorf("default activeView = %d, want ListView (%d)", m.activeView, ListView)
		}
	})

	t.Run("NewListModel returns valid struct", func(t *testing.T) {
		lm := NewListModel()
		// Should render without panic on empty data
		out := lm.View(DefaultTheme(), NewStyles(DefaultTheme()), 80, 24)
		if out == "" {
			t.Error("ListModel.View returned empty string on empty data")
		}
	})

	t.Run("esc from DetailView returns to previousNavView", func(t *testing.T) {
		m := newModel()
		m.activeView = DetailView
		m.previousNavView = ListView
		m.detailModel = &DetailModel{}

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		rm, _ := updated.(Model)
		if rm.activeView != ListView {
			t.Errorf("esc from DetailView: activeView = %d, want ListView (%d)", rm.activeView, ListView)
		}
	})

	t.Run("esc from SearchView returns to previousNavView", func(t *testing.T) {
		m := newModel()
		m.activeView = SearchView
		m.previousNavView = ListView

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		rm, _ := updated.(Model)
		if rm.activeView != ListView {
			t.Errorf("esc from SearchView: activeView = %d, want ListView (%d)", rm.activeView, ListView)
		}
	})

	t.Run("esc from InsightsView returns to previousNavView", func(t *testing.T) {
		m := newModel()
		m.activeView = InsightsView
		m.previousNavView = ListView

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		rm, _ := updated.(Model)
		if rm.activeView != ListView {
			t.Errorf("esc from InsightsView: activeView = %d, want ListView (%d)", rm.activeView, ListView)
		}
	})

	t.Run("esc from HealthView returns to previousNavView", func(t *testing.T) {
		m := newModel()
		m.activeView = HealthView
		m.previousNavView = ListView

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		rm, _ := updated.(Model)
		if rm.activeView != ListView {
			t.Errorf("esc from HealthView: activeView = %d, want ListView (%d)", rm.activeView, ListView)
		}
	})

	t.Run("esc from WorkersView returns to previousNavView", func(t *testing.T) {
		m := newModel()
		m.activeView = WorkersView
		m.previousNavView = ListView

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		rm, _ := updated.(Model)
		if rm.activeView != ListView {
			t.Errorf("esc from WorkersView: activeView = %d, want ListView (%d)", rm.activeView, ListView)
		}
	})

	t.Run("esc from TreeView returns to previousNavView", func(t *testing.T) {
		m := newModel()
		m.activeView = TreeView
		m.previousNavView = ListView

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		rm, _ := updated.(Model)
		if rm.activeView != ListView {
			t.Errorf("esc from TreeView: activeView = %d, want ListView (%d)", rm.activeView, ListView)
		}
	})

	t.Run("ListModel updateBeads stores beads", func(t *testing.T) {
		lm := NewListModel()
		beads := []protocol.Bead{
			{ID: "test-1", Title: "Test bead", Status: "open"},
		}
		lm = lm.updateBeads(beads)
		if len(lm.beads) != 1 {
			t.Errorf("updateBeads: got %d beads, want 1", len(lm.beads))
		}
	})

	t.Run("ListModel updateWorkers stores workers", func(t *testing.T) {
		lm := NewListModel()
		workers := []WorkerStatus{
			{ID: "w1", Status: "idle"},
		}
		lm = lm.updateWorkers(workers, map[string]string{"test-1": "w1"})
		if len(lm.workers) != 1 {
			t.Errorf("updateWorkers: got %d workers, want 1", len(lm.workers))
		}
	})

	t.Run("ListModel resize updates dimensions", func(t *testing.T) {
		lm := NewListModel()
		lm = lm.resize(120, 40)
		if lm.width != 120 || lm.height != 40 {
			t.Errorf("resize: got %dx%d, want 120x40", lm.width, lm.height)
		}
	})

	t.Run("b key switches from ListView to BoardView", func(t *testing.T) {
		m := newModel()
		if m.activeView != ListView {
			t.Skip("default view is not ListView yet")
		}
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
		rm, _ := updated.(Model)
		if rm.activeView != BoardView {
			t.Errorf("b from ListView: activeView = %d, want BoardView (%d)", rm.activeView, BoardView)
		}
	})

	t.Run("handleKeyPress routes ListView to handleListViewKeys", func(t *testing.T) {
		m := newModel()
		// Verify the switch statement in handleKeyPress includes ListView
		m.activeView = ListView
		// i key should go to InsightsView
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
		rm, _ := updated.(Model)
		if rm.activeView != InsightsView {
			t.Errorf("i from ListView: activeView = %d, want InsightsView (%d)", rm.activeView, InsightsView)
		}
	})
}

// TestListNav_JK verifies cursor navigation, group collapse, and cursor persistence.
func TestListNav_JK(t *testing.T) {
	makeBeads := func() []protocol.Bead {
		return []protocol.Bead{
			{ID: "ip-1", Title: "In progress bead", Status: "in_progress", Priority: 1, Type: "task"},
			{ID: "op-1", Title: "Open bead one", Status: "open", Priority: 2, Type: "task"},
			{ID: "op-2", Title: "Open bead two", Status: "open", Priority: 1, Type: "task"},
			{ID: "bl-1", Title: "Blocked bead", Status: "blocked", Priority: 0, Type: "bug"},
		}
	}

	t.Run("flatRows includes headers and bead rows for expanded groups", func(t *testing.T) {
		lm := NewListModel().updateBeads(makeBeads())
		rows := lm.flatRows()
		// 3 non-empty groups: in_progress(1 header + 1 bead), open(1 header + 2 beads), blocked(1 header + 1 bead)
		// = 3 headers + 4 beads = 7 rows
		if len(rows) != 7 {
			t.Errorf("flatRows: got %d rows, want 7", len(rows))
		}
		// First row should be a header
		if !rows[0].isHeader {
			t.Error("flatRows: first row should be a header")
		}
		// Second row should be a bead
		if rows[1].isHeader {
			t.Error("flatRows: second row should be a bead row")
		}
	})

	t.Run("j moves cursor down", func(t *testing.T) {
		lm := NewListModel().updateBeads(makeBeads())
		if lm.cursor != 0 {
			t.Fatalf("initial cursor = %d, want 0", lm.cursor)
		}
		lm = lm.moveDown()
		if lm.cursor != 1 {
			t.Errorf("after moveDown: cursor = %d, want 1", lm.cursor)
		}
	})

	t.Run("k moves cursor up", func(t *testing.T) {
		lm := NewListModel().updateBeads(makeBeads())
		lm.cursor = 3
		lm = lm.moveUp()
		if lm.cursor != 2 {
			t.Errorf("after moveUp: cursor = %d, want 2", lm.cursor)
		}
	})

	t.Run("k clamps at 0", func(t *testing.T) {
		lm := NewListModel().updateBeads(makeBeads())
		lm = lm.moveUp()
		if lm.cursor != 0 {
			t.Errorf("moveUp from 0: cursor = %d, want 0", lm.cursor)
		}
	})

	t.Run("j clamps at last visible row", func(t *testing.T) {
		lm := NewListModel().updateBeads(makeBeads())
		rows := lm.flatRows()
		lm.cursor = len(rows) - 1
		lm = lm.moveDown()
		if lm.cursor != len(rows)-1 {
			t.Errorf("moveDown from last: cursor = %d, want %d", lm.cursor, len(rows)-1)
		}
	})

	t.Run("space toggles collapse on header row", func(t *testing.T) {
		lm := NewListModel().updateBeads(makeBeads())
		// cursor is at 0 which is the "in_progress" header
		if !lm.flatRows()[0].isHeader {
			t.Fatal("row 0 should be a header")
		}
		beforeCount := len(lm.flatRows())
		lm = lm.toggleAtCursor()
		afterCount := len(lm.flatRows())
		// Collapsing in_progress (1 bead) should remove 1 row
		if afterCount != beforeCount-1 {
			t.Errorf("after collapse: %d rows, want %d", afterCount, beforeCount-1)
		}
	})

	t.Run("space is no-op on bead row", func(t *testing.T) {
		lm := NewListModel().updateBeads(makeBeads())
		lm.cursor = 1 // first bead row (in_progress bead)
		if lm.flatRows()[1].isHeader {
			t.Fatal("row 1 should be a bead row")
		}
		beforeCount := len(lm.flatRows())
		lm = lm.toggleAtCursor()
		afterCount := len(lm.flatRows())
		if afterCount != beforeCount {
			t.Errorf("space on bead: rows changed from %d to %d", beforeCount, afterCount)
		}
	})

	t.Run("space re-expand shows beads again", func(t *testing.T) {
		lm := NewListModel().updateBeads(makeBeads())
		beforeCount := len(lm.flatRows())
		lm = lm.toggleAtCursor() // collapse
		lm = lm.toggleAtCursor() // re-expand
		afterCount := len(lm.flatRows())
		if afterCount != beforeCount {
			t.Errorf("after re-expand: %d rows, want %d", afterCount, beforeCount)
		}
	})

	t.Run("j skips collapsed group beads", func(t *testing.T) {
		lm := NewListModel().updateBeads(makeBeads())
		// Collapse in_progress group (cursor at header row 0)
		lm = lm.toggleAtCursor()
		// Now row 0 = in_progress header (collapsed), row 1 = open header, row 2 = open bead 1...
		lm = lm.moveDown() // should go to row 1 (open header)
		row := lm.flatRows()[lm.cursor]
		if !row.isHeader || row.status != "open" {
			t.Errorf("after j from collapsed header: expected open header, got isHeader=%v status=%s", row.isHeader, row.status)
		}
	})

	t.Run("cursorBeadID returns bead ID when on bead row", func(t *testing.T) {
		lm := NewListModel().updateBeads(makeBeads())
		lm.cursor = 1 // first bead (ip-1)
		id := lm.cursorBeadID()
		if id != "ip-1" {
			t.Errorf("cursorBeadID on bead row: got %q, want %q", id, "ip-1")
		}
	})

	t.Run("cursorBeadID returns empty on header row", func(t *testing.T) {
		lm := NewListModel().updateBeads(makeBeads())
		lm.cursor = 0 // header
		id := lm.cursorBeadID()
		if id != "" {
			t.Errorf("cursorBeadID on header: got %q, want empty", id)
		}
	})

	t.Run("cursor persists by bead ID across refresh", func(t *testing.T) {
		lm := NewListModel().updateBeads(makeBeads())
		lm.cursor = 4 // should be op-2 (open header=3, op-2=4 due to priority sort)
		savedID := lm.cursorBeadID()
		if savedID == "" {
			t.Fatal("expected a bead ID at cursor 4")
		}
		// Simulate refresh: new data with same beads + one new bead
		newBeads := append(makeBeads(), protocol.Bead{ID: "op-3", Title: "New open", Status: "open", Priority: 3, Type: "task"})
		lm = lm.updateBeads(newBeads)
		// Cursor should have been restored to the same bead ID
		restoredID := lm.cursorBeadID()
		if restoredID != savedID {
			t.Errorf("cursor not persisted: got %q, want %q", restoredID, savedID)
		}
	})

	t.Run("all groups collapsed shows No beads match", func(t *testing.T) {
		lm := NewListModel().updateBeads(makeBeads())
		theme := DefaultTheme()
		styles := NewStyles(theme)
		// Collapse all groups
		rows := lm.flatRows()
		for i := len(rows) - 1; i >= 0; i-- {
			if rows[i].isHeader {
				lm.cursor = i
				lm = lm.toggleAtCursor()
				rows = lm.flatRows() // refresh after collapse
			}
		}
		out := lm.View(theme, styles, 80, 24)
		if !strings.Contains(out, "No beads match") {
			t.Errorf("all collapsed: View should contain 'No beads match', got %q", out)
		}
	})

	t.Run("handleListViewKeys wires j/k/space to ListModel", func(t *testing.T) {
		m := newModel()
		m.activeView = ListView
		beads := makeBeads()
		m.listModel = m.listModel.updateBeads(beads)
		m.beads = beads

		// j key should move cursor down
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		rm, _ := updated.(Model)
		if rm.listModel.cursor != 1 {
			t.Errorf("j key: cursor = %d, want 1", rm.listModel.cursor)
		}

		// k key should move cursor back up
		updated, _ = rm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
		rm, _ = updated.(Model)
		if rm.listModel.cursor != 0 {
			t.Errorf("k key: cursor = %d, want 0", rm.listModel.cursor)
		}

		// space key on header should toggle collapse
		beforeRows := len(rm.listModel.flatRows())
		updated, _ = rm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
		rm, _ = updated.(Model)
		afterRows := len(rm.listModel.flatRows())
		if afterRows >= beforeRows {
			t.Errorf("space on header: rows didn't decrease (%d -> %d)", beforeRows, afterRows)
		}
	})

	t.Run("enter on bead row opens detail view", func(t *testing.T) {
		m := newModel()
		m.activeView = ListView
		beads := makeBeads()
		m.listModel = m.listModel.updateBeads(beads)
		m.beads = beads
		m.listModel.cursor = 1 // bead row (ip-1)

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		rm, _ := updated.(Model)
		if rm.activeView != DetailView {
			t.Errorf("enter on bead: activeView = %d, want DetailView (%d)", rm.activeView, DetailView)
		}
		if rm.detailModel == nil {
			t.Error("enter on bead: detailModel is nil")
		}
	})
}

// TestListRow_Render verifies list row rendering: icon+priority+ID+title+worker+ctx%,
// status grouping, priority sort, Done cap at 10, and empty group hiding.
func TestListRow_Render(t *testing.T) {
	theme := DefaultTheme()
	styles := NewStyles(theme)

	t.Run("renderRow shows type icon for task", func(t *testing.T) {
		b := protocol.Bead{ID: "abc-1", Title: "Do the thing", Status: "open", Priority: 2, Type: "task"}
		lm := NewListModel()
		row := lm.renderRow(b, 80, styles)
		if !strings.Contains(row, "□") {
			t.Errorf("renderRow missing task icon '□': %q", row)
		}
	})

	t.Run("renderRow shows type icon for bug", func(t *testing.T) {
		b := protocol.Bead{ID: "bug-1", Title: "Fix crash", Status: "open", Priority: 0, Type: "bug"}
		lm := NewListModel()
		row := lm.renderRow(b, 80, styles)
		if !strings.Contains(row, "⚠") {
			t.Errorf("renderRow missing bug icon '⚠': %q", row)
		}
	})

	t.Run("renderRow shows priority badge", func(t *testing.T) {
		b := protocol.Bead{ID: "abc-2", Title: "Some task", Status: "open", Priority: 1, Type: "task"}
		lm := NewListModel()
		row := lm.renderRow(b, 80, styles)
		if !strings.Contains(row, "P1") {
			t.Errorf("renderRow missing priority badge 'P1': %q", row)
		}
	})

	t.Run("renderRow shows muted bead ID", func(t *testing.T) {
		b := protocol.Bead{ID: "oro-xyz9", Title: "Check it", Status: "open", Priority: 3, Type: "task"}
		lm := NewListModel()
		row := lm.renderRow(b, 80, styles)
		if !strings.Contains(row, "oro-xyz9") {
			t.Errorf("renderRow missing bead ID 'oro-xyz9': %q", row)
		}
	})

	t.Run("renderRow shows title", func(t *testing.T) {
		b := protocol.Bead{ID: "t-1", Title: "Implement caching", Status: "open", Priority: 2, Type: "feature"}
		lm := NewListModel()
		row := lm.renderRow(b, 80, styles)
		if !strings.Contains(row, "Implement caching") {
			t.Errorf("renderRow missing title: %q", row)
		}
	})

	t.Run("renderRow shows worker ID and ctx% when assigned", func(t *testing.T) {
		b := protocol.Bead{ID: "wip-1", Title: "In flight task", Status: "in_progress", Priority: 2, Type: "task"}
		workers := []WorkerStatus{
			{ID: "worker-a", Status: "working", BeadID: "wip-1", ContextPct: 55},
		}
		assignments := map[string]string{"wip-1": "worker-a"}
		lm := NewListModel().updateWorkers(workers, assignments)
		row := lm.renderRow(b, 140, styles)
		if !strings.Contains(row, "worker-a") {
			t.Errorf("renderRow missing worker ID 'worker-a': %q", row)
		}
		if !strings.Contains(row, "55%") {
			t.Errorf("renderRow missing ctx%% '55%%': %q", row)
		}
	})

	t.Run("renderRow shows no worker info when not assigned", func(t *testing.T) {
		b := protocol.Bead{ID: "open-1", Title: "Unassigned task", Status: "open", Priority: 2, Type: "task"}
		lm := NewListModel()
		row := lm.renderRow(b, 80, styles)
		if strings.Contains(row, "worker") {
			t.Errorf("renderRow shows unexpected worker info: %q", row)
		}
	})

	t.Run("renderRow truncates long title", func(t *testing.T) {
		longTitle := strings.Repeat("X", 200)
		b := protocol.Bead{ID: "trunc-1", Title: longTitle, Status: "open", Priority: 2, Type: "task"}
		lm := NewListModel()
		row := lm.renderRow(b, 80, styles)
		// Title should be truncated — row must not contain the full 200-char string
		if strings.Contains(row, longTitle) {
			t.Errorf("renderRow did not truncate long title")
		}
		if !strings.Contains(row, "...") {
			t.Errorf("renderRow truncated title missing ellipsis: %q", row)
		}
	})

	t.Run("groupBeads partitions beads by status", func(t *testing.T) {
		beads := []protocol.Bead{
			{ID: "a", Status: "open", Priority: 2},
			{ID: "b", Status: "in_progress", Priority: 1},
			{ID: "c", Status: "blocked", Priority: 0},
			{ID: "d", Status: "closed", Priority: 3},
		}
		groups := groupBeads(beads)
		if len(groups["open"]) != 1 || groups["open"][0].ID != "a" {
			t.Errorf("groupBeads open group: got %v", groups["open"])
		}
		if len(groups["in_progress"]) != 1 || groups["in_progress"][0].ID != "b" {
			t.Errorf("groupBeads in_progress group: got %v", groups["in_progress"])
		}
		if len(groups["blocked"]) != 1 || groups["blocked"][0].ID != "c" {
			t.Errorf("groupBeads blocked group: got %v", groups["blocked"])
		}
		if len(groups["closed"]) != 1 || groups["closed"][0].ID != "d" {
			t.Errorf("groupBeads closed group: got %v", groups["closed"])
		}
	})

	t.Run("groupBeads sorts by priority ascending within group", func(t *testing.T) {
		beads := []protocol.Bead{
			{ID: "lo", Status: "open", Priority: 3},
			{ID: "hi", Status: "open", Priority: 0},
			{ID: "mid", Status: "open", Priority: 2},
		}
		groups := groupBeads(beads)
		got := groups["open"]
		if len(got) != 3 {
			t.Fatalf("expected 3 open beads, got %d", len(got))
		}
		if got[0].ID != "hi" || got[1].ID != "mid" || got[2].ID != "lo" {
			ids := make([]string, len(got))
			for i, b := range got {
				ids[i] = b.ID
			}
			t.Errorf("groupBeads open not sorted by priority: got %v, want [hi mid lo]", ids)
		}
	})

	t.Run("groupBeads maps unknown status to open", func(t *testing.T) {
		beads := []protocol.Bead{
			{ID: "unk", Status: "weird_status", Priority: 1},
			{ID: "real", Status: "open", Priority: 2},
		}
		groups := groupBeads(beads)
		if len(groups["open"]) != 2 {
			t.Errorf("groupBeads should map unknown status to open: got %d open beads, want 2", len(groups["open"]))
		}
		// The unknown-status bead should be in the open group
		found := false
		for _, b := range groups["open"] {
			if b.ID == "unk" {
				found = true
				break
			}
		}
		if !found {
			t.Error("groupBeads: bead with unknown status not found in open group")
		}
	})

	t.Run("groupBeads caps closed group at 10", func(t *testing.T) {
		beads := make([]protocol.Bead, 15)
		for i := range beads {
			beads[i] = protocol.Bead{
				ID:     fmt.Sprintf("done-%02d", i),
				Status: "closed",
			}
		}
		groups := groupBeads(beads)
		if len(groups["closed"]) > 10 {
			t.Errorf("groupBeads closed group not capped: got %d, want <= 10", len(groups["closed"]))
		}
	})

	t.Run("View hides empty groups", func(t *testing.T) {
		lm := NewListModel()
		lm = lm.updateBeads([]protocol.Bead{
			{ID: "only-open", Title: "An open task", Status: "open", Priority: 2, Type: "task"},
		})
		out := lm.View(theme, styles, 80, 24)
		if !strings.Contains(out, "Ready") {
			t.Errorf("View missing Ready group header")
		}
		// Empty groups must not appear
		for _, label := range []string{"In Progress", "Blocked", "Done"} {
			if strings.Contains(out, label) {
				t.Errorf("View shows empty group %q", label)
			}
		}
	})

	t.Run("View shows empty state message when no beads", func(t *testing.T) {
		lm := NewListModel()
		out := lm.View(theme, styles, 80, 24)
		if !strings.Contains(out, "bd create") {
			t.Errorf("View empty state missing 'bd create' hint: %q", out)
		}
	})

	t.Run("renderGroupHeader shows label and count", func(t *testing.T) {
		header := renderGroupHeader("in_progress", 7, styles)
		if !strings.Contains(header, "In Progress") {
			t.Errorf("renderGroupHeader missing label 'In Progress': %q", header)
		}
		if !strings.Contains(header, "7") {
			t.Errorf("renderGroupHeader missing count '7': %q", header)
		}
	})
}

// TestListDetail_FollowsCursor verifies the split-pane detail rendering.
func TestListDetail_FollowsCursor(t *testing.T) {
	styles := NewStyles(DefaultTheme())

	makeBead := func() protocol.Bead {
		return protocol.Bead{
			ID:                 "test-1",
			Title:              "Test bead",
			Status:             "open",
			Priority:           2,
			Type:               "task",
			AcceptanceCriteria: "Must pass all tests",
			Dependencies: []protocol.Dependency{
				{Type: "blocks", DependsOnID: "dep-1"},
			},
		}
	}

	t.Run("detail pane shows cursor bead title and ID", func(t *testing.T) {
		b := makeBead()
		sections := map[string]bool{"acceptance": true, "worker": true}
		out := renderDetailPane(b, nil, nil, sections, styles, 40, 20)
		if !strings.Contains(out, "test-1") {
			t.Errorf("detail pane missing bead ID: %q", out)
		}
		if !strings.Contains(out, "Test bead") {
			t.Errorf("detail pane missing bead title: %q", out)
		}
	})

	t.Run("Acceptance section expanded by default", func(t *testing.T) {
		b := makeBead()
		sections := map[string]bool{"acceptance": true}
		out := renderDetailPane(b, nil, nil, sections, styles, 40, 20)
		if !strings.Contains(out, "▼") {
			t.Errorf("expanded section missing ▼ indicator: %q", out)
		}
		if !strings.Contains(out, "Must pass all tests") {
			t.Errorf("expanded Acceptance section missing content: %q", out)
		}
	})

	t.Run("Deps section collapsed by default", func(t *testing.T) {
		b := makeBead()
		// deps not in sections map → collapsed
		sections := map[string]bool{"acceptance": true}
		out := renderDetailPane(b, nil, nil, sections, styles, 40, 20)
		if !strings.Contains(out, "▶") {
			t.Errorf("collapsed section missing ▶ indicator: %q", out)
		}
		// Collapsed deps section should NOT show the dep ID
		if strings.Contains(out, "dep-1") {
			t.Errorf("collapsed Deps section should not show dep content: %q", out)
		}
	})

	t.Run("Worker section expanded shows worker info when assigned", func(t *testing.T) {
		b := makeBead()
		workers := []WorkerStatus{{ID: "w-1", Status: "working", ContextPct: 42}}
		assignments := map[string]string{"test-1": "w-1"}
		sections := map[string]bool{"worker": true}
		out := renderDetailPane(b, workers, assignments, sections, styles, 40, 20)
		if !strings.Contains(out, "w-1") {
			t.Errorf("worker section missing worker ID: %q", out)
		}
		if !strings.Contains(out, "42%") {
			t.Errorf("worker section missing context pct: %q", out)
		}
	})

	t.Run("Deps section collapsed hides content", func(t *testing.T) {
		b := makeBead()
		// deps not in sections → collapsed
		sections := map[string]bool{}
		out := renderDetailPane(b, nil, nil, sections, styles, 40, 20)
		if strings.Contains(out, "dep-1") {
			t.Errorf("collapsed Deps section should hide content: %q", out)
		}
	})

	t.Run("Deps section expanded shows content", func(t *testing.T) {
		b := makeBead()
		sections := map[string]bool{"deps": true}
		out := renderDetailPane(b, nil, nil, sections, styles, 40, 30)
		if !strings.Contains(out, "dep-1") {
			t.Errorf("expanded Deps section missing dep ID: %q", out)
		}
	})

	t.Run("renderSection with expanded=true shows content", func(t *testing.T) {
		out := renderSection("Test Section", "Body text here", true, styles)
		if !strings.Contains(out, "▼") {
			t.Errorf("expanded section missing ▼: %q", out)
		}
		if !strings.Contains(out, "Test Section") {
			t.Errorf("section missing title: %q", out)
		}
		if !strings.Contains(out, "Body text here") {
			t.Errorf("expanded section missing body: %q", out)
		}
	})

	t.Run("renderSection with expanded=false hides content", func(t *testing.T) {
		out := renderSection("Test Section", "Body text here", false, styles)
		if !strings.Contains(out, "▶") {
			t.Errorf("collapsed section missing ▶: %q", out)
		}
		if strings.Contains(out, "Body text here") {
			t.Errorf("collapsed section should hide body: %q", out)
		}
	})

	t.Run("detail pane with no acceptance criteria hides section", func(t *testing.T) {
		b := makeBead()
		b.AcceptanceCriteria = ""
		sections := map[string]bool{"acceptance": true}
		out := renderDetailPane(b, nil, nil, sections, styles, 60, 20)
		if strings.Contains(out, "Acceptance") {
			t.Errorf("detail pane should hide empty Acceptance section: %q", out)
		}
	})

	t.Run("default sections: acceptance expanded, deps/notes collapsed", func(t *testing.T) {
		sections := defaultDetailSections()
		if !sections["acceptance"] {
			t.Error("acceptance should be expanded by default")
		}
		if !sections["worker"] {
			t.Error("worker should be expanded by default")
		}
		if sections["deps"] {
			t.Error("deps should be collapsed by default")
		}
		if sections["notes"] {
			t.Error("notes should be collapsed by default")
		}
	})
}

// TestListFocus_TabSwitch verifies focus switching, section interactivity, and split ratio.
func TestListFocus_TabSwitch(t *testing.T) {
	makeModel := func() Model {
		m := newModel()
		m.activeView = ListView
		beads := []protocol.Bead{
			{
				ID: "b-1", Title: "Bead one", Status: "open", Priority: 2, Type: "task",
				AcceptanceCriteria: "Test criteria",
			},
		}
		m.beads = beads
		m.listModel = m.listModel.updateBeads(beads)
		m.listModel.cursor = 1 // first bead row
		m.width = 120
		m.height = 40
		return m
	}

	t.Run("tab switches focus from list to detail", func(t *testing.T) {
		m := makeModel()
		if m.listModel.detailFocused {
			t.Fatal("should start with list focused")
		}
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		rm, _ := updated.(Model)
		if !rm.listModel.detailFocused {
			t.Error("tab should switch focus to detail pane")
		}
	})

	t.Run("tab switches focus from detail back to list", func(t *testing.T) {
		m := makeModel()
		m.listModel.detailFocused = true
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		rm, _ := updated.(Model)
		if rm.listModel.detailFocused {
			t.Error("tab should switch focus back to list pane")
		}
	})

	t.Run("l is alias for tab", func(t *testing.T) {
		m := makeModel()
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
		rm, _ := updated.(Model)
		if !rm.listModel.detailFocused {
			t.Error("l should switch focus to detail pane")
		}
	})

	t.Run("esc in detail pane returns to list pane", func(t *testing.T) {
		m := makeModel()
		m.listModel.detailFocused = true
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		rm, _ := updated.(Model)
		if rm.listModel.detailFocused {
			t.Error("esc should return focus to list pane")
		}
		if rm.activeView != ListView {
			t.Errorf("esc in detail pane should stay in ListView, got %d", rm.activeView)
		}
	})

	t.Run("space in detail toggles section", func(t *testing.T) {
		m := makeModel()
		m.listModel.detailFocused = true
		m.listModel.detailSections = defaultDetailSections()
		// acceptance starts expanded
		if !m.listModel.detailSections["acceptance"] {
			t.Fatal("acceptance should start expanded")
		}
		// detailCursor=0 → acceptance section
		m.listModel.detailCursor = 0
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
		rm, _ := updated.(Model)
		if rm.listModel.detailSections["acceptance"] {
			t.Error("space should toggle acceptance to collapsed")
		}
	})

	t.Run("j/k in detail scrolls detailCursor", func(t *testing.T) {
		m := makeModel()
		m.listModel.detailFocused = true
		m.listModel.detailSections = defaultDetailSections()
		m.listModel.detailCursor = 0
		// j should move cursor down
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		rm, _ := updated.(Model)
		if rm.listModel.detailCursor != 1 {
			t.Errorf("j: detailCursor = %d, want 1", rm.listModel.detailCursor)
		}
		// k should move cursor back
		updated, _ = rm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
		rm, _ = updated.(Model)
		if rm.listModel.detailCursor != 0 {
			t.Errorf("k: detailCursor = %d, want 0", rm.listModel.detailCursor)
		}
	})

	t.Run("less-than decreases splitRatio", func(t *testing.T) {
		m := makeModel()
		m.listModel.splitRatio = 0.5
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("<")})
		rm, _ := updated.(Model)
		if rm.listModel.splitRatio >= 0.5 {
			t.Errorf("< should decrease splitRatio, got %f", rm.listModel.splitRatio)
		}
	})

	t.Run("greater-than increases splitRatio", func(t *testing.T) {
		m := makeModel()
		m.listModel.splitRatio = 0.5
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(">")})
		rm, _ := updated.(Model)
		if rm.listModel.splitRatio <= 0.5 {
			t.Errorf("> should increase splitRatio, got %f", rm.listModel.splitRatio)
		}
	})

	t.Run("splitRatio clamps at 0.35 minimum", func(t *testing.T) {
		m := makeModel()
		m.listModel.splitRatio = 0.35
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("<")})
		rm, _ := updated.(Model)
		if rm.listModel.splitRatio < 0.35 {
			t.Errorf("splitRatio below minimum: %f", rm.listModel.splitRatio)
		}
	})

	t.Run("splitRatio clamps at 0.75 maximum", func(t *testing.T) {
		m := makeModel()
		m.listModel.splitRatio = 0.75
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(">")})
		rm, _ := updated.(Model)
		if rm.listModel.splitRatio > 0.75 {
			t.Errorf("splitRatio above maximum: %f", rm.listModel.splitRatio)
		}
	})
}

// TestListFilter_Toggle verifies quick filter keys o/c/r.
func TestListFilter_Toggle(t *testing.T) {
	allBeads := []protocol.Bead{
		{ID: "ip-1", Status: "in_progress", Priority: 1, Type: "task"},
		{ID: "op-1", Status: "open", Priority: 2, Type: "task"},
		{ID: "bl-1", Status: "blocked", Priority: 0, Type: "bug"},
		{ID: "cl-1", Status: "closed", Priority: 3, Type: "task"},
	}

	t.Run("o filter shows open and in_progress only", func(t *testing.T) {
		lm := NewListModel().updateBeads(allBeads)
		lm = lm.setFilter("o")
		filtered := lm.filteredBeads()
		for _, b := range filtered {
			if b.Status != "open" && b.Status != "in_progress" {
				t.Errorf("o filter: unexpected status %q for %s", b.Status, b.ID)
			}
		}
		if len(filtered) != 2 {
			t.Errorf("o filter: got %d beads, want 2", len(filtered))
		}
	})

	t.Run("c filter shows closed only", func(t *testing.T) {
		lm := NewListModel().updateBeads(allBeads)
		lm = lm.setFilter("c")
		filtered := lm.filteredBeads()
		for _, b := range filtered {
			if b.Status != "closed" {
				t.Errorf("c filter: unexpected status %q for %s", b.Status, b.ID)
			}
		}
		if len(filtered) != 1 {
			t.Errorf("c filter: got %d beads, want 1", len(filtered))
		}
	})

	t.Run("r filter shows ready (open and not blocked)", func(t *testing.T) {
		lm := NewListModel().updateBeads(allBeads)
		lm = lm.setFilter("r")
		filtered := lm.filteredBeads()
		for _, b := range filtered {
			if b.Status != "open" {
				t.Errorf("r filter: unexpected status %q for %s", b.Status, b.ID)
			}
		}
		if len(filtered) != 1 {
			t.Errorf("r filter: got %d beads, want 1", len(filtered))
		}
	})

	t.Run("toggle same filter clears it", func(t *testing.T) {
		lm := NewListModel().updateBeads(allBeads)
		lm = lm.setFilter("o")
		if lm.activeFilter != "o" {
			t.Fatalf("filter not set: %q", lm.activeFilter)
		}
		lm = lm.setFilter("o") // toggle off
		if lm.activeFilter != "" {
			t.Errorf("toggle should clear filter, got %q", lm.activeFilter)
		}
		if len(lm.filteredBeads()) != len(allBeads) {
			t.Error("cleared filter should show all beads")
		}
	})

	t.Run("filter persists across refresh", func(t *testing.T) {
		lm := NewListModel().updateBeads(allBeads)
		lm = lm.setFilter("c")
		// Simulate refresh with new data
		newBeads := append(allBeads, protocol.Bead{ID: "cl-2", Status: "closed", Priority: 2, Type: "task"})
		lm = lm.updateBeads(newBeads)
		if lm.activeFilter != "c" {
			t.Errorf("filter not persisted: %q", lm.activeFilter)
		}
		filtered := lm.filteredBeads()
		if len(filtered) != 2 {
			t.Errorf("after refresh: got %d closed beads, want 2", len(filtered))
		}
	})

	t.Run("filterLabel returns display name", func(t *testing.T) {
		lm := NewListModel()
		lm = lm.setFilter("o")
		label := lm.filterLabel()
		if label == "" {
			t.Error("filterLabel should return non-empty for active filter")
		}
	})

	t.Run("filter+collapse empties all visible beads shows message", func(t *testing.T) {
		theme := DefaultTheme()
		styles := NewStyles(theme)
		// Only closed beads, filter to open → nothing visible
		closedOnly := []protocol.Bead{
			{ID: "cl-1", Status: "closed", Priority: 1, Type: "task"},
		}
		lm := NewListModel().updateBeads(closedOnly)
		lm = lm.setFilter("o")
		out := lm.View(theme, styles, 80, 24)
		if !strings.Contains(out, "No beads match") {
			t.Errorf("empty filter result should show 'No beads match': %q", out)
		}
	})

	t.Run("handleListViewKeys wires o/c/r to filter", func(t *testing.T) {
		m := newModel()
		m.activeView = ListView
		m.beads = allBeads
		m.listModel = m.listModel.updateBeads(allBeads)

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
		rm, _ := updated.(Model)
		if rm.listModel.activeFilter != "o" {
			t.Errorf("o key: filter = %q, want 'o'", rm.listModel.activeFilter)
		}
	})
}

// TestListView_SplitPaneRendersDetailPane verifies the split-pane layout wiring in View().
func TestListView_SplitPaneRendersDetailPane(t *testing.T) {
	theme := DefaultTheme()
	styles := NewStyles(theme)

	makeBead := func() protocol.Bead {
		return protocol.Bead{
			ID:                 "test-1",
			Title:              "Test bead title",
			Status:             "open",
			Priority:           2,
			Type:               "task",
			AcceptanceCriteria: "Must pass all tests",
		}
	}

	t.Run("detailFocused=true shows bead ID title and acceptance criteria", func(t *testing.T) {
		lm := NewListModel()
		lm = lm.updateBeads([]protocol.Bead{makeBead()})
		lm.detailFocused = true
		lm.cursor = 1 // row 0 = header, row 1 = bead
		lm.detailSections = defaultDetailSections()

		out := lm.View(theme, styles, 120, 30)

		if !strings.Contains(out, "test-1") {
			t.Errorf("split pane missing bead ID 'test-1': %q", out)
		}
		if !strings.Contains(out, "Test bead title") {
			t.Errorf("split pane missing bead title: %q", out)
		}
		// Acceptance criteria content only appears in detail pane, not in list rows
		if !strings.Contains(out, "Must pass all tests") {
			t.Errorf("split pane missing acceptance criteria content: %q", out)
		}
	})

	t.Run("detailFocused=false renders list only without detail pane content", func(t *testing.T) {
		lm := NewListModel()
		lm = lm.updateBeads([]protocol.Bead{makeBead()})
		lm.detailFocused = false
		lm.cursor = 1

		out := lm.View(theme, styles, 120, 30)

		// Acceptance criteria only appears in detail pane — must be absent in list-only mode
		if strings.Contains(out, "Must pass all tests") {
			t.Errorf("list-only view should not show acceptance criteria: %q", out)
		}
	})

	t.Run("width < 100 forces list-only even when detailFocused=true", func(t *testing.T) {
		lm := NewListModel()
		lm = lm.updateBeads([]protocol.Bead{makeBead()})
		lm.detailFocused = true
		lm.cursor = 1
		lm.detailSections = defaultDetailSections()

		out := lm.View(theme, styles, 90, 30)

		if strings.Contains(out, "Must pass all tests") {
			t.Errorf("narrow view (width<100) should not show detail pane: %q", out)
		}
	})
}

// TestFilterLabelRendered verifies that the active filter label appears in View() output.
func TestFilterLabelRendered(t *testing.T) {
	theme := DefaultTheme()
	styles := NewStyles(theme)

	allBeads := []protocol.Bead{
		{ID: "ip-1", Title: "In progress task", Status: "in_progress", Priority: 1, Type: "task"},
		{ID: "op-1", Title: "Open task one", Status: "open", Priority: 2, Type: "task"},
		{ID: "bl-1", Title: "Blocked task", Status: "blocked", Priority: 0, Type: "bug"},
		{ID: "cl-1", Title: "Closed task", Status: "closed", Priority: 3, Type: "task"},
	}

	t.Run("filter o renders Open label", func(t *testing.T) {
		lm := NewListModel().updateBeads(allBeads)
		lm = lm.setFilter("o")
		out := lm.View(theme, styles, 80, 24)
		if !strings.Contains(out, "Filter: Open") {
			t.Errorf("filter 'o': View() should contain 'Filter: Open', got:\n%s", out)
		}
	})

	t.Run("filter c renders Closed label", func(t *testing.T) {
		lm := NewListModel().updateBeads(allBeads)
		lm = lm.setFilter("c")
		out := lm.View(theme, styles, 80, 24)
		if !strings.Contains(out, "Filter: Closed") {
			t.Errorf("filter 'c': View() should contain 'Filter: Closed', got:\n%s", out)
		}
	})

	t.Run("filter r renders Ready label", func(t *testing.T) {
		lm := NewListModel().updateBeads(allBeads)
		lm = lm.setFilter("r")
		out := lm.View(theme, styles, 80, 24)
		if !strings.Contains(out, "Filter: Ready") {
			t.Errorf("filter 'r': View() should contain 'Filter: Ready', got:\n%s", out)
		}
	})

	t.Run("no filter renders no filter label", func(t *testing.T) {
		lm := NewListModel().updateBeads(allBeads)
		// No filter set — activeFilter is ""
		out := lm.View(theme, styles, 80, 24)
		if strings.Contains(out, "Filter:") {
			t.Errorf("no filter: View() should NOT contain 'Filter:', got:\n%s", out)
		}
	})
}

// TestListResponsive verifies responsive column visibility by terminal width.
func TestListResponsive(t *testing.T) {
	theme := DefaultTheme()
	styles := NewStyles(theme)

	makeLM := func() ListModel {
		beads := []protocol.Bead{
			{ID: "oro-abc123", Title: "Task one", Status: "open", Priority: 2, Type: "task"},
		}
		workers := []WorkerStatus{
			{ID: "worker-a", Status: "working", BeadID: "oro-abc123", ContextPct: 55},
		}
		assignments := map[string]string{"oro-abc123": "worker-a"}
		lm := NewListModel()
		lm = lm.updateBeads(beads)
		lm = lm.updateWorkers(workers, assignments)
		return lm
	}

	t.Run("width>120 shows worker ID and ctx%", func(t *testing.T) {
		lm := makeLM()
		out := lm.View(theme, styles, 140, 30)
		if !strings.Contains(out, "worker-a") {
			t.Errorf("width 140: missing worker ID 'worker-a': %q", out)
		}
		if !strings.Contains(out, "55%") {
			t.Errorf("width 140: missing ctx%% '55%%': %q", out)
		}
	})

	t.Run("width 100-120 shows worker ID but hides ctx%", func(t *testing.T) {
		lm := makeLM()
		out := lm.View(theme, styles, 110, 30)
		if !strings.Contains(out, "worker-a") {
			t.Errorf("width 110: missing worker ID 'worker-a': %q", out)
		}
		if strings.Contains(out, "55%") {
			t.Errorf("width 110: should not show ctx%% '55%%': %q", out)
		}
	})

	t.Run("width 80-100 list only no detail pane no worker info", func(t *testing.T) {
		lm := makeLM()
		lm.detailFocused = true
		out := lm.View(theme, styles, 90, 30)
		// No detail pane content
		if strings.Contains(out, "Must pass all tests") {
			t.Errorf("width 90: should not show detail pane")
		}
		// No worker info in rows
		if strings.Contains(out, "worker-a") {
			t.Errorf("width 90: should not show worker ID: %q", out)
		}
	})

	t.Run("width<80 truncates bead ID to 8 chars", func(t *testing.T) {
		lm := makeLM()
		out := lm.View(theme, styles, 70, 30)
		// Full ID "oro-abc123" (10 chars) should NOT appear
		if strings.Contains(out, "oro-abc123") {
			t.Errorf("width 70: bead ID should be truncated, found full ID: %q", out)
		}
		// Truncated form: first 5 chars + "..." = "oro-a..."
		if !strings.Contains(out, "oro-a"+"\u2026") && !strings.Contains(out, "oro-a...") {
			t.Errorf("width 70: missing truncated bead ID 'oro-a...' or 'oro-a\u2026': %q", out)
		}
	})

	t.Run("focus resets to list pane when width drops below 100 while detail focused", func(t *testing.T) {
		lm := makeLM()
		lm.detailFocused = true
		lm = lm.resize(90, 30)
		if lm.detailFocused {
			t.Error("resize to width<100 should reset detailFocused to false")
		}
	})
}
