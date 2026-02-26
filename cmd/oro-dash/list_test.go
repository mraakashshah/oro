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
		row := lm.renderRow(b, 80, styles)
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
