package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestStatusView_Scaffold verifies the StatusView scaffold:
// - StatusView is appended to the ViewType enum
// - s key from ListView/BoardView opens StatusView
// - H key still opens HealthView (not StatusView)
// - w key still opens WorkersView (not StatusView)
// - handleStatusViewKeys handles j/k/Enter/Esc
// - System section shows daemon+uptime+panes
// - Nil healthData shows "Connecting..."
func TestStatusView_Scaffold(t *testing.T) {
	t.Run("StatusView is a valid ViewType", func(t *testing.T) {
		// StatusView should be defined and distinct from other views
		sv := StatusView
		if sv == BoardView || sv == InsightsView || sv == DetailView ||
			sv == SearchView || sv == HelpView || sv == HealthView ||
			sv == WorkersView || sv == TreeView || sv == ListView {
			t.Error("StatusView must be a distinct ViewType")
		}
	})

	t.Run("s key from ListView switches to StatusView", func(t *testing.T) {
		m := newModel()
		m.activeView = ListView
		m.initialLoad = false

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != StatusView {
			t.Errorf("expected StatusView after s key from ListView, got %v", model.activeView)
		}
	})

	t.Run("s key from BoardView switches to StatusView", func(t *testing.T) {
		m := newModel()
		m.activeView = BoardView
		m.previousNavView = BoardView
		m.initialLoad = false

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != StatusView {
			t.Errorf("expected StatusView after s key from BoardView, got %v", model.activeView)
		}
	})

	t.Run("H key from ListView still opens HealthView", func(t *testing.T) {
		m := newModel()
		m.activeView = ListView
		m.initialLoad = false

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("H")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != HealthView {
			t.Errorf("expected HealthView after H key, got %v", model.activeView)
		}
	})

	t.Run("w key from ListView still opens WorkersView", func(t *testing.T) {
		m := newModel()
		m.activeView = ListView
		m.initialLoad = false

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != WorkersView {
			t.Errorf("expected WorkersView after w key, got %v", model.activeView)
		}
	})

	t.Run("handleStatusViewKeys: Esc returns to previousNavView", func(t *testing.T) {
		m := newModel()
		m.activeView = StatusView
		m.previousNavView = ListView
		m.initialLoad = false

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != ListView {
			t.Errorf("expected ListView after Esc from StatusView, got %v", model.activeView)
		}
	})

	t.Run("handleStatusViewKeys: j/k navigate sections", func(t *testing.T) {
		m := newModel()
		m.activeView = StatusView
		m.initialLoad = false
		m.healthData = &HealthData{
			DaemonPID:   12345,
			DaemonState: "running",
			WorkerCount: 2,
		}

		// j moves cursor down
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.statusModel.cursor != 1 {
			t.Errorf("expected cursor=1 after j, got %d", model.statusModel.cursor)
		}

		// k moves cursor back up
		updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
		model, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.statusModel.cursor != 0 {
			t.Errorf("expected cursor=0 after k, got %d", model.statusModel.cursor)
		}
	})

	t.Run("handleStatusViewKeys: k at top clamps to 0", func(t *testing.T) {
		m := newModel()
		m.activeView = StatusView
		m.initialLoad = false

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.statusModel.cursor != 0 {
			t.Errorf("expected cursor=0 after k at top, got %d", model.statusModel.cursor)
		}
	})

	t.Run("System section shows daemon status, uptime, pane count", func(t *testing.T) {
		m := newModel()
		m.activeView = StatusView
		m.initialLoad = false
		m.width = 120
		m.height = 40
		m.healthData = &HealthData{
			DaemonPID:     12345,
			DaemonState:   "running",
			ArchitectPane: PaneHealth{Name: "architect", Alive: true},
			ManagerPane:   PaneHealth{Name: "manager", Alive: true},
			WorkerCount:   3,
		}

		view := m.View()
		plain := stripANSI(view)

		// Should show daemon status
		if !strings.Contains(plain, "running") {
			t.Errorf("StatusView missing daemon state 'running', got:\n%s", plain)
		}

		// Should show pane info
		if !strings.Contains(plain, "architect") {
			t.Errorf("StatusView missing architect pane, got:\n%s", plain)
		}
		if !strings.Contains(plain, "manager") {
			t.Errorf("StatusView missing manager pane, got:\n%s", plain)
		}

		// Should show worker count
		if !strings.Contains(plain, "3") {
			t.Errorf("StatusView missing worker count, got:\n%s", plain)
		}
	})

	t.Run("nil healthData shows Connecting...", func(t *testing.T) {
		m := newModel()
		m.activeView = StatusView
		m.initialLoad = false
		m.width = 120
		m.height = 40
		m.healthData = nil

		view := m.View()
		plain := stripANSI(view)

		if !strings.Contains(plain, "Connecting...") {
			t.Errorf("StatusView with nil healthData should show 'Connecting...', got:\n%s", plain)
		}
	})

	t.Run("StatusView renders via View() dispatch", func(t *testing.T) {
		m := newModel()
		m.activeView = StatusView
		m.initialLoad = false
		m.width = 120
		m.height = 40
		m.healthData = &HealthData{
			DaemonPID:   99,
			DaemonState: "running",
			WorkerCount: 1,
		}

		view := m.View()
		// Should produce non-empty output
		if len(view) == 0 {
			t.Error("StatusView produced empty output")
		}
	})

	t.Run("helpHintsForView includes StatusView", func(t *testing.T) {
		hints := helpHintsForView(StatusView, 120)
		if hints == "" {
			t.Error("helpHintsForView returned empty for StatusView")
		}
		if !strings.Contains(hints, "esc") {
			t.Error("StatusView help hints should mention esc")
		}
	})
}
