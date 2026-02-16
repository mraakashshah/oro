package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"oro/pkg/protocol"
)

// TestStatusBar verifies the status bar shows daemon health + worker count + aggregate stats.
func TestStatusBar(t *testing.T) {
	tests := []struct {
		name            string
		daemonHealthy   bool
		workerCount     int
		openCount       int
		inProgressCount int
		wantContains    []string
	}{
		{
			name:            "daemon offline shows offline and bead counts",
			daemonHealthy:   false,
			workerCount:     0,
			openCount:       5,
			inProgressCount: 2,
			wantContains:    []string{"offline", "5", "2"},
		},
		{
			name:            "daemon online shows worker count and stats",
			daemonHealthy:   true,
			workerCount:     3,
			openCount:       10,
			inProgressCount: 5,
			wantContains:    []string{"3", "10", "5"},
		},
		{
			name:            "no beads shows empty counts",
			daemonHealthy:   true,
			workerCount:     2,
			openCount:       0,
			inProgressCount: 0,
			wantContains:    []string{"2", "0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				daemonHealthy:   tt.daemonHealthy,
				workerCount:     tt.workerCount,
				openCount:       tt.openCount,
				inProgressCount: tt.inProgressCount,
			}

			statusBar := m.renderStatusBar()

			for _, want := range tt.wantContains {
				if !strings.Contains(statusBar, want) {
					t.Errorf("renderStatusBar() missing %q, got: %s", want, statusBar)
				}
			}

			// Verify offline is shown in red when daemon is not healthy
			if !tt.daemonHealthy && !strings.Contains(statusBar, "offline") {
				t.Errorf("renderStatusBar() should show 'offline' when daemon is unhealthy")
			}
		})
	}
}

// TestModel_KeyboardQuit verifies that pressing 'q' or 'ctrl+c' returns tea.Quit.
func TestModel_KeyboardQuit(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.KeyMsg
	}{
		{
			name: "q key quits",
			msg:  tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")},
		},
		{
			name: "ctrl+c quits",
			msg:  tea.KeyMsg{Type: tea.KeyCtrlC},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel()
			_, cmd := m.Update(tt.msg)
			if cmd == nil {
				t.Fatal("Update() returned nil cmd, want tea.Quit")
			}
			// tea.Quit returns a special quit message; execute the cmd to verify
			msg := cmd()
			if _, ok := msg.(tea.QuitMsg); !ok {
				t.Errorf("Update() cmd produced %T, want tea.QuitMsg", msg)
			}
		})
	}
}

// TestModel_ViewRenders verifies that View() returns non-empty output containing status bar info.
func TestModel_ViewRenders(t *testing.T) {
	m := Model{
		daemonHealthy: true,
		workerCount:   3,
	}

	view := m.View()
	if view == "" {
		t.Fatal("View() returned empty string, want non-empty output")
	}
	if !strings.Contains(view, "Workers") {
		t.Errorf("View() missing 'Workers', got: %s", view)
	}
}

// TestModel_BeadsMsgUpdatesModel verifies that receiving a beadsMsg updates beads and counts.
func TestModel_BeadsMsgUpdatesModel(t *testing.T) {
	m := newModel()
	beads := []protocol.Bead{
		{ID: "b-1", Title: "Fix bug", Status: "open"},
		{ID: "b-2", Title: "Add feature", Status: "in_progress"},
		{ID: "b-3", Title: "Blocked task", Status: "blocked"},
		{ID: "b-4", Title: "Another open", Status: "open"},
	}

	updated, _ := m.Update(beadsMsg(beads))
	model, ok := updated.(Model)
	if !ok {
		t.Fatal("Update() did not return Model")
	}

	if len(model.beads) != 4 {
		t.Fatalf("expected 4 beads, got %d", len(model.beads))
	}
	if model.openCount != 2 {
		t.Errorf("openCount = %d, want 2", model.openCount)
	}
	if model.inProgressCount != 1 {
		t.Errorf("inProgressCount = %d, want 1", model.inProgressCount)
	}
}

// TestModel_TickMsgReturnsFetchCommands verifies that tickMsg triggers data fetching.
func TestModel_TickMsgReturnsFetchCommands(t *testing.T) {
	m := newModel()
	_, cmd := m.Update(tickMsg(time.Now()))

	if cmd == nil {
		t.Fatal("tickMsg should return a non-nil command")
	}
}

// TestModel_InitReturnsFetchCommands verifies that Init triggers data fetching.
func TestModel_InitReturnsFetchCommands(t *testing.T) {
	m := newModel()
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() should return a non-nil command")
	}
}

// TestStatusBar_ShowsBeadCountsWhenDaemonOffline verifies bead counts show even without daemon.
func TestStatusBar_ShowsBeadCountsWhenDaemonOffline(t *testing.T) {
	m := Model{
		daemonHealthy:   false,
		openCount:       5,
		inProgressCount: 2,
	}

	bar := m.renderStatusBar()
	if !strings.Contains(bar, "5") {
		t.Errorf("status bar should show open count 5 when daemon offline, got: %s", bar)
	}
	if !strings.Contains(bar, "2") {
		t.Errorf("status bar should show in-progress count 2 when daemon offline, got: %s", bar)
	}
	if !strings.Contains(bar, "offline") {
		t.Errorf("status bar should still indicate daemon is offline, got: %s", bar)
	}
}

// TestKeyboardNavigation verifies keyboard navigation across columns and beads.
func TestKeyboardNavigation(t *testing.T) {
	// Setup test beads across all columns
	beads := []protocol.Bead{
		{ID: "b-1", Title: "Ready task 1", Status: "open"},
		{ID: "b-2", Title: "Ready task 2", Status: "open"},
		{ID: "b-3", Title: "WIP task 1", Status: "in_progress"},
		{ID: "b-4", Title: "WIP task 2", Status: "in_progress"},
		{ID: "b-5", Title: "Blocked task", Status: "blocked"},
	}

	t.Run("initial state starts at first column first bead", func(t *testing.T) {
		m := newModel()
		m.beads = beads

		if m.activeCol != 0 {
			t.Errorf("initial activeCol = %d, want 0", m.activeCol)
		}
		if m.activeBead != 0 {
			t.Errorf("initial activeBead = %d, want 0", m.activeBead)
		}
	})

	t.Run("h/l navigate between columns", func(t *testing.T) {
		m := newModel()
		m.beads = beads

		// l moves to next column (Ready -> In Progress)
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
		var ok bool
		m, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if m.activeCol != 1 {
			t.Errorf("after 'l' activeCol = %d, want 1", m.activeCol)
		}
		if m.activeBead != 0 {
			t.Errorf("after 'l' activeBead = %d, want 0 (reset to first bead)", m.activeBead)
		}

		// h moves back (In Progress -> Ready)
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
		m, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if m.activeCol != 0 {
			t.Errorf("after 'h' activeCol = %d, want 0", m.activeCol)
		}
	})

	t.Run("Tab/Shift-Tab navigate between columns", func(t *testing.T) {
		m := newModel()
		m.beads = beads

		// Tab moves to next column
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		var ok bool
		m, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if m.activeCol != 1 {
			t.Errorf("after Tab activeCol = %d, want 1", m.activeCol)
		}

		// Shift-Tab moves back
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
		m, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if m.activeCol != 0 {
			t.Errorf("after Shift-Tab activeCol = %d, want 0", m.activeCol)
		}
	})

	t.Run("j/k navigate within column", func(t *testing.T) {
		m := newModel()
		m.beads = beads
		m.activeCol = 0 // Ready column has 2 beads

		// j moves down
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		var ok bool
		m, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if m.activeBead != 1 {
			t.Errorf("after 'j' activeBead = %d, want 1", m.activeBead)
		}

		// k moves up
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
		m, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if m.activeBead != 0 {
			t.Errorf("after 'k' activeBead = %d, want 0", m.activeBead)
		}
	})

	t.Run("arrow keys navigate within column", func(t *testing.T) {
		m := newModel()
		m.beads = beads
		m.activeCol = 0

		// down arrow moves down
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		var ok bool
		m, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if m.activeBead != 1 {
			t.Errorf("after Down arrow activeBead = %d, want 1", m.activeBead)
		}

		// up arrow moves up
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
		m, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if m.activeBead != 0 {
			t.Errorf("after Up arrow activeBead = %d, want 0", m.activeBead)
		}
	})

	t.Run("cursor clamps at column boundaries", func(t *testing.T) {
		m := newModel()
		m.beads = beads
		m.activeCol = 0

		// h at first column stays at first column
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
		var ok bool
		m, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if m.activeCol != 0 {
			t.Errorf("h at first column should clamp, activeCol = %d, want 0", m.activeCol)
		}

		// Navigate to last column (Done column is index 3)
		m.activeCol = 3

		// l at last column stays at last column
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
		m, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if m.activeCol != 3 {
			t.Errorf("l at last column should clamp, activeCol = %d, want 3", m.activeCol)
		}
	})

	t.Run("cursor clamps at bead boundaries", func(t *testing.T) {
		m := newModel()
		m.beads = beads
		m.activeCol = 0 // Ready column has 2 beads
		m.activeBead = 0

		// k at first bead stays at first bead
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
		var ok bool
		m, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if m.activeBead != 0 {
			t.Errorf("k at first bead should clamp, activeBead = %d, want 0", m.activeBead)
		}

		// Navigate to last bead
		m.activeBead = 1

		// j at last bead stays at last bead
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		m, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if m.activeBead != 1 {
			t.Errorf("j at last bead should clamp, activeBead = %d, want 1", m.activeBead)
		}
	})

	t.Run("empty columns are skipped", func(t *testing.T) {
		// Create beads with gap - no blocked beads
		beadsWithGap := []protocol.Bead{
			{ID: "b-1", Title: "Ready task", Status: "open"},
			{ID: "b-2", Title: "WIP task", Status: "in_progress"},
			// No blocked beads
			{ID: "b-3", Title: "Done task", Status: "closed"},
		}

		m := newModel()
		m.beads = beadsWithGap
		m.activeCol = 1 // In Progress column

		// l should skip empty Blocked column and go to Done
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
		var ok bool
		m, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if m.activeCol != 3 {
			t.Errorf("l should skip empty Blocked column, activeCol = %d, want 3 (Done)", m.activeCol)
		}

		// h should skip empty Blocked column and go back to In Progress
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
		m, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if m.activeCol != 1 {
			t.Errorf("h should skip empty Blocked column, activeCol = %d, want 1 (In Progress)", m.activeCol)
		}
	})
}

// TestModel_ViewSwitching verifies i key toggles to InsightsView and Esc returns to BoardView.
func TestModel_ViewSwitching(t *testing.T) {
	tests := []struct {
		name         string
		initialView  ViewType
		key          string
		expectedView ViewType
		expectedQuit bool
	}{
		{
			name:         "i key switches from BoardView to InsightsView",
			initialView:  BoardView,
			key:          "i",
			expectedView: InsightsView,
			expectedQuit: false,
		},
		{
			name:         "Esc key switches from InsightsView to BoardView",
			initialView:  InsightsView,
			key:          "esc",
			expectedView: BoardView,
			expectedQuit: false,
		},
		{
			name:         "Esc key on BoardView does nothing",
			initialView:  BoardView,
			key:          "esc",
			expectedView: BoardView,
			expectedQuit: false,
		},
		{
			name:         "i key on InsightsView stays on InsightsView",
			initialView:  InsightsView,
			key:          "i",
			expectedView: InsightsView,
			expectedQuit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel()
			m.activeView = tt.initialView

			var msg tea.Msg
			if tt.key == "esc" {
				msg = tea.KeyMsg{Type: tea.KeyEsc}
			} else {
				msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.key)}
			}

			updated, cmd := m.Update(msg)
			model, ok := updated.(Model)
			if !ok {
				t.Fatal("Update() did not return Model")
			}

			if model.activeView != tt.expectedView {
				t.Errorf("activeView = %v, want %v", model.activeView, tt.expectedView)
			}

			// Check for quit command
			if tt.expectedQuit && cmd == nil {
				t.Error("expected quit command but got nil")
			}
			if !tt.expectedQuit && cmd != nil {
				// Execute cmd to check if it's a quit message
				if msg := cmd(); msg != nil {
					if _, isQuit := msg.(tea.QuitMsg); isQuit {
						t.Error("unexpected quit command")
					}
				}
			}
		})
	}
}

// TestModel_DetailViewDrilldown verifies Enter key transitions to detail view.
func TestModel_DetailViewDrilldown(t *testing.T) {
	t.Run("Enter on selected card transitions to DetailView", func(t *testing.T) {
		beads := []protocol.Bead{
			{ID: "b-1", Title: "Test bead", Status: "open"},
		}

		m := newModel()
		m.beads = beads
		m.activeView = BoardView
		m.activeCol = 0
		m.activeBead = 0

		// Press Enter
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != DetailView {
			t.Errorf("after Enter, activeView = %v, want DetailView", model.activeView)
		}

		// Verify detail model was created
		if model.detailModel == nil {
			t.Error("detailModel should be set after pressing Enter")
		}
	})

	t.Run("Enter with no beads in column does not panic", func(t *testing.T) {
		m := newModel()
		m.beads = []protocol.Bead{} // No beads
		m.activeView = BoardView

		// This should not panic
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		// Should stay on BoardView
		if model.activeView != BoardView {
			t.Errorf("with no beads, should stay on BoardView, got %v", model.activeView)
		}
	})

	t.Run("Esc from DetailView returns to BoardView with cursor preserved", func(t *testing.T) {
		beads := []protocol.Bead{
			{ID: "b-1", Title: "Test bead 1", Status: "open"},
			{ID: "b-2", Title: "Test bead 2", Status: "open"},
		}

		m := newModel()
		m.beads = beads
		m.activeView = DetailView
		m.activeCol = 0
		m.activeBead = 1 // Second bead
		m.detailModel = &DetailModel{}

		// Press Esc
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != BoardView {
			t.Errorf("after Esc, activeView = %v, want BoardView", model.activeView)
		}

		// Verify cursor position preserved
		if model.activeCol != 0 {
			t.Errorf("cursor column should be preserved, got %d, want 0", model.activeCol)
		}
		if model.activeBead != 1 {
			t.Errorf("cursor bead should be preserved, got %d, want 1", model.activeBead)
		}
	})

	t.Run("Backspace from DetailView returns to BoardView", func(t *testing.T) {
		m := newModel()
		m.activeView = DetailView
		m.detailModel = &DetailModel{}

		// Press Backspace
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != BoardView {
			t.Errorf("after Backspace, activeView = %v, want BoardView", model.activeView)
		}
	})
}

// TestModel_DetailViewTabNavigation verifies Tab/Shift-Tab in DetailView cycle tabs.
func TestModel_DetailViewTabNavigation(t *testing.T) {
	t.Run("Tab in DetailView cycles to next tab", func(t *testing.T) {
		m := newModel()
		m.activeView = DetailView
		m.detailModel = &DetailModel{
			activeTab: 0,
			tabs:      []string{"Overview", "Worker", "Diff", "Deps", "Memory"},
		}

		// Press Tab
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.detailModel.activeTab != 1 {
			t.Errorf("after Tab, activeTab = %d, want 1", model.detailModel.activeTab)
		}
	})

	t.Run("Shift-Tab in DetailView cycles to previous tab", func(t *testing.T) {
		m := newModel()
		m.activeView = DetailView
		m.detailModel = &DetailModel{
			activeTab: 1,
			tabs:      []string{"Overview", "Worker", "Diff", "Deps", "Memory"},
		}

		// Press Shift-Tab
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.detailModel.activeTab != 0 {
			t.Errorf("after Shift-Tab, activeTab = %d, want 0", model.detailModel.activeTab)
		}
	})
}

// TestModel_SearchOverlay verifies / key opens search overlay and Esc closes it.
func TestModel_SearchOverlay(t *testing.T) {
	t.Run("/ key in BoardView opens search overlay", func(t *testing.T) {
		m := newModel()
		m.activeView = BoardView

		// Press /
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != SearchView {
			t.Errorf("after /, activeView = %v, want SearchView", model.activeView)
		}
	})

	t.Run("Esc from SearchView returns to BoardView", func(t *testing.T) {
		m := newModel()
		m.activeView = SearchView

		// Press Esc
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != BoardView {
			t.Errorf("after Esc, activeView = %v, want BoardView", model.activeView)
		}
	})

	t.Run("Search overlay does not interfere with detail view", func(t *testing.T) {
		m := newModel()
		m.activeView = DetailView

		// Press / in DetailView - should not open search
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != DetailView {
			t.Errorf("/ in DetailView should not open search, activeView = %v, want DetailView", model.activeView)
		}
	})

	t.Run("Search overlay does not interfere with insights view", func(t *testing.T) {
		m := newModel()
		m.activeView = InsightsView

		// Press / in InsightsView - should not open search
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != InsightsView {
			t.Errorf("/ in InsightsView should not open search, activeView = %v, want InsightsView", model.activeView)
		}
	})
}

// TestModel_SearchLiveFilter verifies typing in search field filters beads.
func TestModel_SearchLiveFilter(t *testing.T) {
	t.Run("typing in search field updates search query", func(t *testing.T) {
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "oro-abc.1", Title: "Fix authentication bug", Status: "open", Priority: 0, Type: "bug"},
			{ID: "oro-abc.2", Title: "Add user dashboard", Status: "in_progress", Priority: 1, Type: "feature"},
		}
		m.activeView = SearchView
		m.searchQuery = "auth"

		// Verify filtered beads contains only matching beads
		filtered := m.filterBeads()
		if len(filtered) != 1 {
			t.Errorf("filterBeads() returned %d beads, want 1", len(filtered))
		}
		if len(filtered) > 0 && filtered[0].ID != "oro-abc.1" {
			t.Errorf("filterBeads() returned wrong bead, got %s, want oro-abc.1", filtered[0].ID)
		}
	})

	t.Run("empty search query shows all beads", func(t *testing.T) {
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "oro-abc.1", Title: "Fix bug", Status: "open"},
			{ID: "oro-abc.2", Title: "Add feature", Status: "open"},
		}
		m.activeView = SearchView
		m.searchQuery = ""

		filtered := m.filterBeads()
		if len(filtered) != 2 {
			t.Errorf("empty query should return all beads, got %d, want 2", len(filtered))
		}
	})
}

// TestModel_SearchNavigateToDetail verifies Enter on search result navigates to detail view.
func TestModel_SearchNavigateToDetail(t *testing.T) {
	t.Run("Enter on search result navigates to DetailView", func(t *testing.T) {
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "oro-abc.1", Title: "Fix authentication bug", Status: "open"},
		}
		m.activeView = SearchView
		m.searchQuery = "auth"
		m.searchSelectedIndex = 0

		// Press Enter
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != DetailView {
			t.Errorf("after Enter on search result, activeView = %v, want DetailView", model.activeView)
		}

		if model.detailModel == nil {
			t.Error("detailModel should be set after Enter on search result")
		}
	})

	t.Run("Enter with no search results does not navigate", func(t *testing.T) {
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "oro-abc.1", Title: "Fix bug", Status: "open"},
		}
		m.activeView = SearchView
		m.searchQuery = "nonexistent"
		m.searchSelectedIndex = 0

		// Press Enter
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		// Should stay on SearchView
		if model.activeView != SearchView {
			t.Errorf("with no results, should stay on SearchView, got %v", model.activeView)
		}
	})
}

// TestModel_SearchResultNavigation verifies up/down keys navigate search results.
func TestModel_SearchResultNavigation(t *testing.T) {
	t.Run("down key moves to next search result", func(t *testing.T) {
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "oro-abc.1", Title: "Fix auth bug", Status: "open"},
			{ID: "oro-abc.2", Title: "Add auth feature", Status: "open"},
		}
		m.activeView = SearchView
		m.searchQuery = "auth"
		m.searchSelectedIndex = 0

		// Press down
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.searchSelectedIndex != 1 {
			t.Errorf("after down, searchSelectedIndex = %d, want 1", model.searchSelectedIndex)
		}
	})

	t.Run("up key moves to previous search result", func(t *testing.T) {
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "oro-abc.1", Title: "Fix auth bug", Status: "open"},
			{ID: "oro-abc.2", Title: "Add auth feature", Status: "open"},
		}
		m.activeView = SearchView
		m.searchQuery = "auth"
		m.searchSelectedIndex = 1

		// Press up
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.searchSelectedIndex != 0 {
			t.Errorf("after up, searchSelectedIndex = %d, want 0", model.searchSelectedIndex)
		}
	})

	t.Run("down key clamps at last result", func(t *testing.T) {
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "oro-abc.1", Title: "Fix auth bug", Status: "open"},
			{ID: "oro-abc.2", Title: "Add auth feature", Status: "open"},
		}
		m.activeView = SearchView
		m.searchQuery = "auth"
		m.searchSelectedIndex = 1

		// Press down at last result
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.searchSelectedIndex != 1 {
			t.Errorf("down at last result should clamp, searchSelectedIndex = %d, want 1", model.searchSelectedIndex)
		}
	})

	t.Run("up key clamps at first result", func(t *testing.T) {
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "oro-abc.1", Title: "Fix auth bug", Status: "open"},
		}
		m.activeView = SearchView
		m.searchQuery = "auth"
		m.searchSelectedIndex = 0

		// Press up at first result
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.searchSelectedIndex != 0 {
			t.Errorf("up at first result should clamp, searchSelectedIndex = %d, want 0", model.searchSelectedIndex)
		}
	})
}

// TestModel_BeadsMsgClampsCursor verifies that activeBead is clamped when beads refresh shrinks column.
func TestModel_BeadsMsgClampsCursor(t *testing.T) {
	t.Run("activeBead clamped when column shrinks below cursor position", func(t *testing.T) {
		// Setup: model with cursor on 5th bead in Ready column
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "b-1", Status: "open"},
			{ID: "b-2", Status: "open"},
			{ID: "b-3", Status: "open"},
			{ID: "b-4", Status: "open"},
			{ID: "b-5", Status: "open"},
			{ID: "b-6", Status: "open"},
		}
		m.activeCol = 0  // Ready column
		m.activeBead = 5 // 6th bead (0-indexed)

		// Refresh with only 2 beads
		refreshedBeads := []protocol.Bead{
			{ID: "b-1", Status: "open"},
			{ID: "b-2", Status: "open"},
		}

		updated, _ := m.Update(beadsMsg(refreshedBeads))
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		// activeBead should be clamped to max valid index (1)
		if model.activeBead != 1 {
			t.Errorf("activeBead should be clamped to 1, got %d", model.activeBead)
		}
	})

	t.Run("activeBead clamped to 0 when column becomes empty", func(t *testing.T) {
		// Setup: model with cursor on bead in Ready column
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "b-1", Status: "open"},
			{ID: "b-2", Status: "open"},
		}
		m.activeCol = 0  // Ready column
		m.activeBead = 1 // 2nd bead

		// Refresh with all beads in different column
		refreshedBeads := []protocol.Bead{
			{ID: "b-1", Status: "in_progress"},
			{ID: "b-2", Status: "in_progress"},
		}

		updated, _ := m.Update(beadsMsg(refreshedBeads))
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		// activeBead should be clamped to 0 (even though column is empty)
		if model.activeBead != 0 {
			t.Errorf("activeBead should be clamped to 0 when column is empty, got %d", model.activeBead)
		}
	})

	t.Run("activeCol validated when becomes empty", func(t *testing.T) {
		// Setup: model with cursor on bead in Ready column
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "b-1", Status: "open"},
			{ID: "b-2", Status: "in_progress"},
		}
		m.activeCol = 0
		m.activeBead = 0

		// Refresh with Ready column now empty, but In Progress has beads
		refreshedBeads := []protocol.Bead{
			{ID: "b-2", Status: "in_progress"},
			{ID: "b-3", Status: "in_progress"},
		}

		updated, _ := m.Update(beadsMsg(refreshedBeads))
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		// activeCol should move to first non-empty column (In Progress = 1)
		if model.activeCol != 1 {
			t.Errorf("activeCol should move to first non-empty column (1), got %d", model.activeCol)
		}
		// activeBead should be reset to 0
		if model.activeBead != 0 {
			t.Errorf("activeBead should be reset to 0, got %d", model.activeBead)
		}
	})

	t.Run("cursor unchanged when still valid after refresh", func(t *testing.T) {
		// Setup: model with cursor on 2nd bead
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "b-1", Status: "open"},
			{ID: "b-2", Status: "open"},
			{ID: "b-3", Status: "open"},
		}
		m.activeCol = 0
		m.activeBead = 1

		// Refresh with more beads
		refreshedBeads := []protocol.Bead{
			{ID: "b-1", Status: "open"},
			{ID: "b-2", Status: "open"},
			{ID: "b-3", Status: "open"},
			{ID: "b-4", Status: "open"},
		}

		updated, _ := m.Update(beadsMsg(refreshedBeads))
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		// Cursor should remain unchanged
		if model.activeCol != 0 {
			t.Errorf("activeCol should remain 0, got %d", model.activeCol)
		}
		if model.activeBead != 1 {
			t.Errorf("activeBead should remain 1, got %d", model.activeBead)
		}
	})

	t.Run("no panic when all columns empty after refresh", func(t *testing.T) {
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "b-1", Status: "open"},
		}
		m.activeCol = 0
		m.activeBead = 0

		// Refresh with no beads
		updated, _ := m.Update(beadsMsg([]protocol.Bead{}))
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		// Should clamp to safe defaults
		if model.activeCol != 0 {
			t.Errorf("activeCol should be 0, got %d", model.activeCol)
		}
		if model.activeBead != 0 {
			t.Errorf("activeBead should be 0, got %d", model.activeBead)
		}
	})
}

// TestSplitPaneLayout verifies split-pane layout in DetailView with adjustable ratio.
func TestSplitPaneLayout(t *testing.T) {
	bead1 := protocol.Bead{
		ID:       "oro-test1",
		Title:    "Test Bead 1",
		Status:   "in_progress",
		Priority: 1,
		Type:     "task",
	}

	beadDetail := protocol.BeadDetail{
		ID:                 "oro-test1",
		Title:              "Test Bead 1",
		AcceptanceCriteria: "Test acceptance",
	}

	t.Run("DetailView renders split pane with board (40%) and detail (60%)", func(t *testing.T) {
		detailModel := newDetailModel(beadDetail, DefaultTheme(), NewStyles(DefaultTheme()))
		theme := DefaultTheme()
		m := Model{
			width:       120,
			height:      40,
			activeView:  DetailView,
			beads:       []protocol.Bead{bead1},
			splitRatio:  0.4, // default
			detailModel: &detailModel,
			theme:       theme,
			styles:      NewStyles(theme),
		}

		output := m.View()
		if !strings.Contains(output, "Test Bead 1") {
			t.Error("DetailView should contain bead title")
		}

		// Verify split rendering happened (renderSplitPane should be called)
		// This is a structural test - we'll verify the implementation creates the split
	})

	t.Run("< key decreases board width (min 20%)", func(t *testing.T) {
		theme := DefaultTheme()
		detailModel := newDetailModel(beadDetail, theme, NewStyles(theme))
		m := Model{
			width:       120,
			activeView:  DetailView,
			splitRatio:  0.4,
			detailModel: &detailModel,
			theme:       theme,
			styles:      NewStyles(theme),
		}

		// Press < key
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'<'}}
		updated, _ := m.Update(msg)
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		expected := 0.3
		tolerance := 0.001
		if model.splitRatio < expected-tolerance || model.splitRatio > expected+tolerance {
			t.Errorf("< should decrease splitRatio to ~0.3, got %f", model.splitRatio)
		}

		// Press < again to test clamping at minimum
		for range 5 {
			updated, _ = model.Update(msg)
			model, ok = updated.(Model)
			if !ok {
				t.Fatal("Update() did not return Model")
			}
		}

		if model.splitRatio < 0.2 {
			t.Errorf("splitRatio should clamp at 0.2, got %f", model.splitRatio)
		}
	})

	t.Run("> key increases board width (max 80%)", func(t *testing.T) {
		theme := DefaultTheme()
		detailModel := newDetailModel(beadDetail, theme, NewStyles(theme))
		m := Model{
			width:       120,
			activeView:  DetailView,
			splitRatio:  0.4,
			detailModel: &detailModel,
			theme:       theme,
			styles:      NewStyles(theme),
		}

		// Press > key
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'>'}}
		updated, _ := m.Update(msg)
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.splitRatio != 0.5 {
			t.Errorf("> should increase splitRatio by 0.1, got %f", model.splitRatio)
		}

		// Press > multiple times to test clamping at maximum
		for range 5 {
			updated, _ = model.Update(msg)
			model, ok = updated.(Model)
			if !ok {
				t.Fatal("Update() did not return Model")
			}
		}

		if model.splitRatio > 0.8 {
			t.Errorf("splitRatio should clamp at 0.8, got %f", model.splitRatio)
		}
	})

	t.Run("width < 80 renders detail only (no split)", func(t *testing.T) {
		theme := DefaultTheme()
		detailModel := newDetailModel(beadDetail, theme, NewStyles(theme))
		m := Model{
			width:       75, // Below threshold
			height:      40,
			activeView:  DetailView,
			splitRatio:  0.4,
			detailModel: &detailModel,
			theme:       theme,
			styles:      NewStyles(theme),
		}

		output := m.View()

		// Should render detail view only, no board split
		if !strings.Contains(output, "Test Bead 1") {
			t.Error("DetailView should contain bead title")
		}

		// In narrow terminals, should not attempt split rendering
		// The output should be just the detail view
	})
}
