package main

import (
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
