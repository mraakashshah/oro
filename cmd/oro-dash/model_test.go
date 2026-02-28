package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"oro/pkg/protocol"
)

// stripANSI removes ANSI color codes from a string.
func stripANSI(s string) string {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return ansi.ReplaceAllString(s, "")
}

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

			statusBar := m.renderStatusBar(120)

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

	bar := m.renderStatusBar(120)
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

// TestStatusBar_HintsRightAligned verifies that hints are right-aligned with gap fill.
func TestStatusBar_HintsRightAligned(t *testing.T) {
	tests := []struct {
		name       string
		width      int
		height     int
		activeView ViewType
		wantHints  bool // true if hints should be visible
	}{
		{
			name:       "board view hints right-aligned at width 100",
			width:      100,
			height:     40,
			activeView: BoardView,
			wantHints:  true,
		},
		{
			name:       "detail view hints right-aligned at width 120",
			width:      120,
			height:     40,
			activeView: DetailView,
			wantHints:  true,
		},
		{
			name:       "hints hidden when width < 60",
			width:      50,
			height:     40,
			activeView: BoardView,
			wantHints:  false,
		},
		{
			name:       "hints hidden when height < 30",
			width:      100,
			height:     20,
			activeView: BoardView,
			wantHints:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				daemonHealthy:   true,
				workerCount:     3,
				openCount:       5,
				inProgressCount: 2,
				activeView:      tt.activeView,
				height:          tt.height,
			}

			bar := m.renderStatusBar(tt.width)
			barClean := stripANSI(bar)

			if !tt.wantHints {
				if strings.Contains(barClean, "q quit") {
					t.Errorf("renderStatusBar() should not show 'q quit' when width < 60 or height < 30, got: %s", barClean)
				}
				return
			}

			// Hints should be visible with "q quit"
			if !strings.Contains(barClean, "q quit") {
				t.Fatalf("renderStatusBar() missing 'q quit' hint, got: %s", barClean)
			}

			// Verify gap before hints enforces right-alignment (minimum gap of 2)
			idx := strings.Index(barClean, "q quit")
			beforeHints := barClean[:idx]
			gapCount := 0
			for i := len(beforeHints) - 1; i >= 0 && beforeHints[i] == ' '; i-- {
				gapCount++
			}

			if gapCount < 2 {
				t.Errorf("gap before 'q quit' is %d, want >= 2 (right-align gap fill). Bar: %s", gapCount, barClean)
			}
		})
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
		m.activeView = BoardView
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
		m.activeView = BoardView
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
		m.activeView = BoardView
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
		m.activeView = BoardView
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
		m.activeView = BoardView
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
		m.activeView = BoardView
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
			name:         "Esc key switches from InsightsView to previousNavView",
			initialView:  InsightsView,
			key:          "esc",
			expectedView: ListView, // default previousNavView
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

	t.Run("Esc from DetailView returns to previousNavView with cursor preserved", func(t *testing.T) {
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

		// Default previousNavView is ListView
		if model.activeView != ListView {
			t.Errorf("after Esc, activeView = %v, want ListView", model.activeView)
		}

		// Verify cursor position preserved
		if model.activeCol != 0 {
			t.Errorf("cursor column should be preserved, got %d, want 0", model.activeCol)
		}
		if model.activeBead != 1 {
			t.Errorf("cursor bead should be preserved, got %d, want 1", model.activeBead)
		}
	})

	t.Run("Backspace from DetailView returns to previousNavView", func(t *testing.T) {
		m := newModel()
		m.activeView = DetailView
		m.detailModel = &DetailModel{}

		// Press Backspace
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		// Default previousNavView is ListView
		if model.activeView != ListView {
			t.Errorf("after Backspace, activeView = %v, want ListView", model.activeView)
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

	t.Run("Esc from SearchView returns to previousNavView", func(t *testing.T) {
		m := newModel()
		m.activeView = SearchView

		// Press Esc
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		// Default previousNavView is ListView
		if model.activeView != ListView {
			t.Errorf("after Esc, activeView = %v, want ListView", model.activeView)
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
		m.searchInput.SetValue("auth")

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
		m.searchInput.SetValue("auth")
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
		m.searchInput.SetValue("nonexistent")
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
		m.searchInput.SetValue("auth")
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
		m.searchInput.SetValue("auth")
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
		m.searchInput.SetValue("auth")
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
		m.searchInput.SetValue("auth")
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

// TestSearchTextInput verifies that the search input uses bubbles textinput.Model
// for proper character handling, cursor movement, and focus/blur behavior.
func TestSearchTextInput(t *testing.T) {
	t.Run("searchInput field exists and is textinput.Model", func(t *testing.T) {
		m := newModel()
		// Verify the field exists and has textinput methods (compile-time type check)
		_ = m.searchInput.Value()
		_ = m.searchInput.Focused()
	})

	t.Run("entering SearchView focuses textinput", func(t *testing.T) {
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "oro-test1", Title: "Test Bead 1", Status: "open"},
		}

		// Trigger search view
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
		m, ok := updated.(Model)
		if !ok {
			t.Fatal("Update did not return Model")
		}

		// Verify we're in SearchView
		if m.activeView != SearchView {
			t.Fatalf("expected SearchView, got %v", m.activeView)
		}

		// Verify textinput is focused
		if !m.searchInput.Focused() {
			t.Error("expected searchInput to be focused in SearchView")
		}
	})

	t.Run("leaving SearchView blurs textinput", func(t *testing.T) {
		m := newModel()
		m.activeView = SearchView
		m.searchInput.Focus()

		// Press Esc to leave SearchView
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m, ok := updated.(Model)
		if !ok {
			t.Fatal("Update did not return Model")
		}

		// Verify we're back in the default nav view (ListView)
		if m.activeView != ListView {
			t.Fatalf("expected ListView, got %v", m.activeView)
		}

		// Verify textinput is blurred
		if m.searchInput.Focused() {
			t.Error("expected searchInput to be blurred after leaving SearchView")
		}
	})

	t.Run("character input updates searchInput value", func(t *testing.T) {
		m := newModel()
		m.activeView = SearchView
		m.searchInput.Focus()

		// Type 'a'
		m.searchInput, _ = m.searchInput.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
		if m.searchInput.Value() != "a" {
			t.Errorf("expected searchInput value 'a', got '%s'", m.searchInput.Value())
		}

		// Type 'b'
		m.searchInput, _ = m.searchInput.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
		if m.searchInput.Value() != "ab" {
			t.Errorf("expected searchInput value 'ab', got '%s'", m.searchInput.Value())
		}
	})

	t.Run("backspace removes character", func(t *testing.T) {
		m := newModel()
		m.activeView = SearchView
		m.searchInput.Focus()
		m.searchInput.SetValue("test")

		// Press backspace
		m.searchInput, _ = m.searchInput.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		if m.searchInput.Value() != "tes" {
			t.Errorf("expected 'tes', got '%s'", m.searchInput.Value())
		}
	})

	t.Run("search results update on keystroke via searchInput.Value()", func(t *testing.T) {
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "oro-abc1", Title: "Authentication Bug", Status: "open"},
			{ID: "oro-xyz2", Title: "Dashboard Feature", Status: "open"},
		}
		m.activeView = SearchView
		m.searchInput.Focus()
		m.searchInput.SetValue("auth")

		// Filter should use searchInput.Value() instead of searchQuery
		filtered := m.filterBeads()

		if len(filtered) != 1 {
			t.Errorf("expected 1 result for 'auth', got %d", len(filtered))
		}
		if len(filtered) > 0 && filtered[0].ID != "oro-abc1" {
			t.Errorf("expected oro-abc1, got %s", filtered[0].ID)
		}
	})

	t.Run("textinput handles cursor movement", func(t *testing.T) {
		m := newModel()
		m.activeView = SearchView
		m.searchInput.Focus()
		m.searchInput.SetValue("test")

		// Move cursor left
		m.searchInput, _ = m.searchInput.Update(tea.KeyMsg{Type: tea.KeyLeft})
		// Verify cursor moved (Position should be less than length)
		if m.searchInput.Position() == 4 {
			t.Error("expected cursor to move left from end position")
		}

		// Move cursor right
		m.searchInput, _ = m.searchInput.Update(tea.KeyMsg{Type: tea.KeyRight})
		// Cursor should move back
		if m.searchInput.Position() != 4 {
			t.Errorf("expected cursor at position 4, got %d", m.searchInput.Position())
		}
	})

	t.Run("handleSearchViewKeys delegates to textinput for character input", func(t *testing.T) {
		m := newModel()
		m.beads = []protocol.Bead{
			{ID: "oro-test1", Title: "Test", Status: "open"},
		}
		m.activeView = SearchView
		m.searchInput.Focus()

		// Send character input through handleSearchViewKeys
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}
		updated, _ := m.handleSearchViewKeys(msg.String(), msg)
		m, ok := updated.(Model)
		if !ok {
			t.Fatal("handleSearchViewKeys did not return Model")
		}

		// Verify the character was added via textinput
		if m.searchInput.Value() != "x" {
			t.Errorf("expected 'x', got '%s'", m.searchInput.Value())
		}
	})
}

// TestDaysSinceUpdateCalculation verifies DaysSinceUpdate is calculated from bead UpdatedAt timestamp.
func TestDaysSinceUpdateCalculation(t *testing.T) {
	tests := []struct {
		name                string
		updatedAt           string
		wantDaysSinceUpdate int
	}{
		{
			name:                "updated today shows 0 days",
			updatedAt:           time.Now().Format(time.RFC3339),
			wantDaysSinceUpdate: 0,
		},
		{
			name:                "updated 8 days ago shows 8 days",
			updatedAt:           time.Now().AddDate(0, 0, -8).Format(time.RFC3339),
			wantDaysSinceUpdate: 8,
		},
		{
			name:                "updated 3 days ago shows 3 days",
			updatedAt:           time.Now().AddDate(0, 0, -3).Format(time.RFC3339),
			wantDaysSinceUpdate: 3,
		},
		{
			name:                "empty UpdatedAt shows 0 days",
			updatedAt:           "",
			wantDaysSinceUpdate: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel()
			m.beads = []protocol.Bead{
				{ID: "b-1", Title: "Test", Status: "open", Priority: 0, Type: "bug", UpdatedAt: tt.updatedAt},
			}

			// Convert beads to BeadsWithDeps (simulating what buildInsightsModel does)
			insights := m.buildInsightsModel()
			beadsWithDeps := insights.graph.beads

			if len(beadsWithDeps) != 1 {
				t.Fatalf("expected 1 bead, got %d", len(beadsWithDeps))
			}

			got := beadsWithDeps[0].DaysSinceUpdate
			if got != tt.wantDaysSinceUpdate {
				t.Errorf("DaysSinceUpdate = %d, want %d", got, tt.wantDaysSinceUpdate)
			}
		})
	}
}

// TestTriageFlagFiresForStaleP0 verifies stale P0 triage flag uses calculated DaysSinceUpdate.
func TestTriageFlagFiresForStaleP0(t *testing.T) {
	m := newModel()
	m.beads = []protocol.Bead{
		{
			ID:        "b-1",
			Title:     "Stale P0 Bug",
			Status:    "open",
			Priority:  0,
			Type:      "bug",
			UpdatedAt: time.Now().AddDate(0, 0, -10).Format(time.RFC3339), // 10 days ago
		},
		{
			ID:        "b-2",
			Title:     "Recent P0 Bug",
			Status:    "open",
			Priority:  0,
			Type:      "bug",
			UpdatedAt: time.Now().Format(time.RFC3339), // today
		},
	}

	insights := m.buildInsightsModel()
	flags := insights.graph.TriageFlags()

	// Should have exactly 1 flag for the stale P0
	if len(flags) != 1 {
		t.Fatalf("expected 1 triage flag for stale P0, got %d", len(flags))
	}

	if flags[0].BeadID != "b-1" {
		t.Errorf("triage flag should be for b-1, got %s", flags[0].BeadID)
	}

	if flags[0].Severity != "high" {
		t.Errorf("triage flag severity should be 'high', got %s", flags[0].Severity)
	}

	if !strings.Contains(flags[0].Reason, "stale P0") {
		t.Errorf("triage flag reason should mention 'stale P0', got %s", flags[0].Reason)
	}
}

// TestFilterBeads_TypeFilter_Integration verifies the full filterBeads pipeline filters by type.
// Validates: SearchModel.Filter accepts protocol.Bead, filterBeads returns only matching types.
func TestFilterBeads_TypeFilter_Integration(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "oro-001", Title: "Fix crash", Status: "open", Priority: 0, Type: "bug"},
		{ID: "oro-002", Title: "Add dashboard", Status: "open", Priority: 1, Type: "feature"},
		{ID: "oro-003", Title: "Refactor DB", Status: "in_progress", Priority: 2, Type: "task"},
		{ID: "oro-004", Title: "Another bug fix", Status: "open", Priority: 1, Type: "bug"},
	}

	// SearchModel.Filter must accept []protocol.Bead directly (no local Bead conversion).
	sm := &SearchModel{}
	filtered := sm.Filter(beads, "t:bug")
	if len(filtered) != 2 {
		t.Fatalf("SearchModel.Filter(\"t:bug\") returned %d beads, want 2", len(filtered))
	}
	for _, b := range filtered {
		if b.Type != "bug" {
			t.Errorf("SearchModel.Filter returned non-bug bead %s (type=%s)", b.ID, b.Type)
		}
	}

	// filterBeads through the full Model pipeline must return only bugs.
	m := newModel()
	m.beads = beads
	m.searchInput.SetValue("t:bug")
	result := m.filterBeads()
	if len(result) != 2 {
		t.Fatalf("filterBeads() with \"t:bug\" returned %d beads, want 2", len(result))
	}
	for _, b := range result {
		if b.Type != "bug" {
			t.Errorf("filterBeads() returned non-bug bead %s (type=%s)", b.ID, b.Type)
		}
	}
}

// TestModel_InitialLoadState verifies the initial loading state behavior:
// - Before beadsMsg: View shows "Loading"
// - After beadsMsg: View shows board (even if empty)
// - Empty beadsMsg slice still clears initialLoad
func TestModel_InitialLoadState(t *testing.T) {
	t.Run("initial state shows Loading message", func(t *testing.T) {
		m := newModel()

		// Before any beadsMsg, initialLoad should be true
		if !m.initialLoad {
			t.Errorf("initial state should have initialLoad=true, got false")
		}

		// View should contain "Loading"
		view := m.View()
		if !strings.Contains(view, "Loading") {
			t.Errorf("initial View() should contain 'Loading', got: %s", view)
		}
	})

	t.Run("after beadsMsg, board is shown instead of Loading", func(t *testing.T) {
		m := newModel()
		beads := []protocol.Bead{
			{ID: "b-1", Title: "Test Task", Status: "open"},
		}

		// Send beadsMsg
		updated, _ := m.Update(beadsMsg(beads))
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		// initialLoad should now be false
		if model.initialLoad {
			t.Errorf("after beadsMsg, initialLoad should be false, got true")
		}

		// View should show board content, not "Loading"
		view := model.View()
		if strings.Contains(view, "Loading") {
			t.Errorf("View() after beadsMsg should not contain 'Loading', got: %s", view)
		}

		// View should contain bead content (check for bead ID which is guaranteed to appear)
		if !strings.Contains(view, "b-1") {
			t.Errorf("View() after beadsMsg should contain bead ID, got: %s", view)
		}

		// Board structure should be present (column headers)
		if !strings.Contains(view, "Ready") {
			t.Errorf("View() after beadsMsg should contain board structure, got: %s", view)
		}
	})

	t.Run("empty beadsMsg slice still clears initialLoad", func(t *testing.T) {
		m := newModel()

		// Verify initial state
		if !m.initialLoad {
			t.Errorf("initial state should have initialLoad=true")
		}

		// Send empty beadsMsg
		updated, _ := m.Update(beadsMsg([]protocol.Bead{}))
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		// initialLoad should be false even with empty slice
		if model.initialLoad {
			t.Errorf("after empty beadsMsg, initialLoad should be false, got true")
		}

		// Beads should be empty but present
		if len(model.beads) != 0 {
			t.Errorf("after empty beadsMsg, beads should be empty, got %d", len(model.beads))
		}
	})
}

// TestStatusBarBottom verifies status bar is at bottom of View() output, includes help hints,
// and handles narrow/short terminal edge cases.
func TestStatusBarBottom(t *testing.T) {
	t.Run("View renders content first then status bar", func(t *testing.T) {
		m := Model{
			daemonHealthy:   true,
			workerCount:     2,
			openCount:       3,
			inProgressCount: 1,
			activeView:      BoardView,
			width:           120,
			height:          40,
		}
		m.styles = NewStyles(m.theme)

		view := m.View()
		// status bar should be at the bottom (last line)
		lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
		lastLine := lines[len(lines)-1]
		if !strings.Contains(lastLine, "daemon") && !strings.Contains(lastLine, "Workers") {
			t.Errorf("last line should be status bar, got: %q", lastLine)
		}
		// content should appear before the status bar — view should have more than 1 line
		if len(lines) < 2 {
			t.Errorf("View() should have content + status bar (>=2 lines), got %d lines", len(lines))
		}
	})

	t.Run("status bar includes help hints on wide terminal", func(t *testing.T) {
		m := Model{
			daemonHealthy: true,
			activeView:    BoardView,
			width:         120,
			height:        40,
		}
		m.styles = NewStyles(m.theme)

		bar := m.renderStatusBar(120)
		// should contain some key hints
		if !strings.Contains(bar, "?") && !strings.Contains(bar, "q") {
			t.Errorf("status bar should include help hints on wide terminal, got: %s", bar)
		}
	})

	t.Run("width < 60 omits help hints", func(t *testing.T) {
		m := Model{
			daemonHealthy: true,
			activeView:    BoardView,
			width:         50,
			height:        40,
		}
		m.styles = NewStyles(m.theme)

		bar := m.renderStatusBar(50)
		// Should have daemon status but no help hints like "? help"
		if strings.Contains(bar, "? help") || strings.Contains(bar, "q quit") {
			t.Errorf("status bar should omit help hints on narrow terminal (<60), got: %s", bar)
		}
		if !strings.Contains(bar, "daemon") {
			t.Errorf("status bar should still show daemon status on narrow terminal, got: %s", bar)
		}
	})

	t.Run("height < 30 produces single line bar", func(t *testing.T) {
		m := Model{
			daemonHealthy: true,
			activeView:    BoardView,
			width:         120,
			height:        25,
		}
		m.styles = NewStyles(m.theme)

		bar := m.renderStatusBar(120)
		// single line: no newlines in the bar
		if strings.Contains(bar, "\n") {
			t.Errorf("status bar should be single line when height < 30, got: %s", bar)
		}
		if !strings.Contains(bar, "daemon") {
			t.Errorf("status bar should still show daemon status on short terminal, got: %s", bar)
		}
	})

	t.Run("context-appropriate hints for InsightsView", func(t *testing.T) {
		m := Model{
			daemonHealthy: true,
			activeView:    InsightsView,
			width:         120,
			height:        40,
		}
		m.styles = NewStyles(m.theme)

		bar := m.renderStatusBar(120)
		// InsightsView should show esc to go back
		if !strings.Contains(bar, "esc") {
			t.Errorf("InsightsView status bar should contain 'esc' hint, got: %s", bar)
		}
	})
}

// TestStatusBar_UsesSeparator verifies status bar uses box-drawing separator │ not ASCII |.
func TestStatusBar_UsesSeparator(t *testing.T) {
	m := Model{
		daemonHealthy:   true,
		workerCount:     3,
		openCount:       10,
		inProgressCount: 5,
	}
	m.styles = NewStyles(m.theme)

	bar := m.renderStatusBar(120)

	// Should contain box-drawing separator
	if !strings.Contains(bar, "│") {
		t.Errorf("renderStatusBar() should use box-drawing │ separator, got: %s", bar)
	}

	// Should NOT contain ASCII pipe as separator
	if strings.Contains(bar, " | ") {
		t.Errorf("renderStatusBar() should not use ASCII | separator, got: %s", bar)
	}
}

// TestDrillDownWiresWorkerData verifies drill-down paths include WorkerID, ContextPercent, and Dependencies.
func TestDrillDownWiresWorkerData(t *testing.T) {
	t.Run("drillDownToDetail (board) includes WorkerID and ContextPercent", func(t *testing.T) {
		beads := []protocol.Bead{
			{
				ID:     "oro-board.1",
				Title:  "Board task",
				Status: "open",
				Dependencies: []protocol.Dependency{
					{IssueID: "oro-board.1", DependsOnID: "oro-dep.1", Type: "depends_on"},
				},
			},
		}
		workers := []WorkerStatus{
			{ID: "worker-2", ContextPct: 60},
		}

		m := newModel()
		m.beads = beads
		m.workers = workers
		m.assignments = map[string]string{"oro-board.1": "worker-2"}
		m.activeView = BoardView
		m.activeCol = 0
		m.activeBead = 0

		// Drill down via Enter key
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.detailModel == nil {
			t.Fatal("detailModel should be set after drillDownToDetail")
		}

		// Verify WorkerID and ContextPercent are populated
		if model.detailModel.bead.WorkerID != "worker-2" {
			t.Errorf("BeadDetail.WorkerID = %q, want %q", model.detailModel.bead.WorkerID, "worker-2")
		}
		if model.detailModel.bead.ContextPercent != 60 {
			t.Errorf("BeadDetail.ContextPercent = %d, want %d", model.detailModel.bead.ContextPercent, 60)
		}

		// Verify Dependencies are included
		if len(model.detailModel.bead.Dependencies) != 1 {
			t.Errorf("BeadDetail.Dependencies length = %d, want 1", len(model.detailModel.bead.Dependencies))
		}
	})

	t.Run("search view includes WorkerID and Dependencies", func(t *testing.T) {
		beads := []protocol.Bead{
			{
				ID:    "oro-search.1",
				Title: "Searchable task",
				Dependencies: []protocol.Dependency{
					{IssueID: "oro-search.1", DependsOnID: "oro-parent.1", Type: "blocks"},
					{IssueID: "oro-search.1", DependsOnID: "oro-parent.2", Type: "depends_on"},
				},
			},
		}
		workers := []WorkerStatus{
			{ID: "worker-3", ContextPct: 75},
		}

		m := newModel()
		m.beads = beads
		m.workers = workers
		m.assignments = map[string]string{"oro-search.1": "worker-3"}
		m.activeView = SearchView
		m.searchInput.SetValue("searchable")
		m.searchSelectedIndex = 0

		// Navigate to detail via search result
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.detailModel == nil {
			t.Fatal("detailModel should be set after search Enter")
		}

		// Verify WorkerID and ContextPercent are populated
		if model.detailModel.bead.WorkerID != "worker-3" {
			t.Errorf("BeadDetail.WorkerID = %q, want %q", model.detailModel.bead.WorkerID, "worker-3")
		}
		if model.detailModel.bead.ContextPercent != 75 {
			t.Errorf("BeadDetail.ContextPercent = %d, want %d", model.detailModel.bead.ContextPercent, 75)
		}

		// Verify Dependencies are included
		if len(model.detailModel.bead.Dependencies) != 2 {
			t.Errorf("BeadDetail.Dependencies length = %d, want 2", len(model.detailModel.bead.Dependencies))
		}
	})

	t.Run("unassigned bead has empty WorkerID but still has Dependencies", func(t *testing.T) {
		beads := []protocol.Bead{
			{
				ID:    "oro-unassigned.1",
				Title: "Unassigned task",
				Dependencies: []protocol.Dependency{
					{IssueID: "oro-unassigned.1", DependsOnID: "oro-blocker.1", Type: "depends_on"},
				},
			},
		}

		m := newModel()
		m.beads = beads
		m.workers = []WorkerStatus{}
		m.assignments = make(map[string]string) // No assignment
		m.activeView = BoardView
		m.activeCol = 0
		m.activeBead = 0

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.detailModel == nil {
			t.Fatal("detailModel should be set")
		}

		// WorkerID should be empty for unassigned beads
		if model.detailModel.bead.WorkerID != "" {
			t.Errorf("BeadDetail.WorkerID should be empty for unassigned bead, got %q", model.detailModel.bead.WorkerID)
		}

		// But Dependencies should still be populated
		if len(model.detailModel.bead.Dependencies) != 1 {
			t.Errorf("BeadDetail.Dependencies should be populated even for unassigned bead, got length %d", len(model.detailModel.bead.Dependencies))
		}
	})
}

// TestLoadMoreClosed verifies load-more pagination for closed beads.
func TestLoadMoreClosed(t *testing.T) {
	t.Run("moreClosedMsg appends to extraClosed and sets cursor to oldest ClosedAt", func(t *testing.T) {
		m := newModel()

		// Send a batch of 3 closed beads sorted by ClosedAt descending (most recent first).
		batch := moreClosedMsg{
			{ID: "oro-c.1", Status: "closed", ClosedAt: "2024-03-01T12:00:00Z"},
			{ID: "oro-c.2", Status: "closed", ClosedAt: "2024-02-15T08:00:00Z"},
			{ID: "oro-c.3", Status: "closed", ClosedAt: "2024-01-10T06:00:00Z"},
		}

		updated, _ := m.Update(batch)
		got, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if len(got.extraClosed) != 3 {
			t.Errorf("extraClosed length = %d, want 3", len(got.extraClosed))
		}

		// Cursor should be oldest ClosedAt from the batch (last element when sorted desc).
		wantCursor := "2024-01-10T06:00:00Z"
		if got.closedCursor != wantCursor {
			t.Errorf("closedCursor = %q, want %q", got.closedCursor, wantCursor)
		}
	})

	t.Run("moreClosedMsg appends to existing extraClosed (cumulative)", func(t *testing.T) {
		m := newModel()
		m.extraClosed = []protocol.Bead{
			{ID: "oro-c.0", Status: "closed", ClosedAt: "2024-04-01T00:00:00Z"},
		}

		batch := moreClosedMsg{
			{ID: "oro-c.1", Status: "closed", ClosedAt: "2024-03-01T12:00:00Z"},
		}

		updated, _ := m.Update(batch)
		got, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if len(got.extraClosed) != 2 {
			t.Errorf("extraClosed length = %d, want 2 (cumulative append)", len(got.extraClosed))
		}
	})

	t.Run("applyBeadsMsg preserves extraClosed", func(t *testing.T) {
		m := newModel()
		m.extraClosed = []protocol.Bead{
			{ID: "oro-extra.1", Status: "closed", ClosedAt: "2024-01-01T00:00:00Z"},
		}

		// beadsMsg should not wipe extraClosed.
		msg := beadsMsg{
			{ID: "oro-open.1", Status: "open"},
			{ID: "oro-closed.1", Status: "closed", ClosedAt: "2024-03-01T00:00:00Z"},
		}

		updated, _ := m.Update(msg)
		got, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if len(got.extraClosed) != 1 {
			t.Errorf("extraClosed after beadsMsg = %d, want 1 (preserved)", len(got.extraClosed))
		}
		if got.extraClosed[0].ID != "oro-extra.1" {
			t.Errorf("extraClosed[0].ID = %q, want %q", got.extraClosed[0].ID, "oro-extra.1")
		}
	})

	t.Run("groupBeads does not cap closed at 10", func(t *testing.T) {
		var beads []protocol.Bead
		for i := range 15 {
			beads = append(beads, protocol.Bead{
				ID:     fmt.Sprintf("oro-c.%d", i),
				Status: "closed",
			})
		}

		groups := groupBeads(beads)
		if len(groups["closed"]) != 15 {
			t.Errorf("groupBeads closed count = %d, want 15 (no cap)", len(groups["closed"]))
		}
	})
}

// TestDrillDownPreservesStatusForClosedBead verifies that drillDownToDetail and
// handleSearchViewKeys pass the bead's Status to BeadDetail, so getTabsForBead
// can correctly limit closed beads to Overview+Deps tabs.
func TestDrillDownPreservesStatusForClosedBead(t *testing.T) {
	t.Run("board drilldown — closed bead gets 2 tabs", func(t *testing.T) {
		bead := protocol.Bead{
			ID:     "oro-closed.1",
			Title:  "Closed task",
			Status: "closed",
		}

		m := newModel()
		m.beads = []protocol.Bead{bead}
		m.activeView = BoardView
		m.activeCol = 3 // "Done" column (closed beads) — order: Ready(0), InProgress(1), Blocked(2), Done(3)
		m.activeBead = 0

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if model.detailModel == nil {
			t.Fatal("detailModel should be set")
		}

		got := model.detailModel.tabs
		if len(got) != 2 {
			t.Errorf("closed bead tabs = %v (len %d), want [Overview Deps] (len 2)", got, len(got))
		}
	})

	t.Run("search drilldown — closed bead gets 2 tabs", func(t *testing.T) {
		bead := protocol.Bead{
			ID:     "oro-closed.2",
			Title:  "Closed search result",
			Status: "closed",
		}

		m := newModel()
		m.beads = []protocol.Bead{bead}
		m.activeView = SearchView
		m.searchInput.SetValue("closed")
		m.searchSelectedIndex = 0

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if model.detailModel == nil {
			t.Fatal("detailModel should be set")
		}

		got := model.detailModel.tabs
		if len(got) != 2 {
			t.Errorf("closed bead tabs = %v (len %d), want [Overview Deps] (len 2)", got, len(got))
		}
	})
}

// TestViewTypeEnum verifies the ViewType enum contains exactly the 6 expected
// views. Deletion of any constant is enforced at compile time; this test asserts
// all remaining views have distinct values and no duplicates crept in.
func TestViewTypeEnum(t *testing.T) {
	views := []struct {
		name  string
		value ViewType
	}{
		{"BoardView", BoardView},
		{"DetailView", DetailView},
		{"SearchView", SearchView},
		{"ListView", ListView},
		{"StatusView", StatusView},
		{"HelpView", HelpView},
	}
	seen := make(map[ViewType]string)
	for _, v := range views {
		if prev, dup := seen[v.value]; dup {
			t.Errorf("ViewType collision: %s and %s share value %d", prev, v.name, int(v.value))
		}
		seen[v.value] = v.name
	}
	if len(seen) != 6 {
		t.Errorf("expected 6 distinct ViewType values, got %d", len(seen))
	}
}

// TestKeyBindings_HWRouteToStatus verifies H and w keys navigate to StatusView,
// and L (uppercase) navigates to ListView from all views.
func TestKeyBindings_HWRouteToStatus(t *testing.T) {
	tests := []struct {
		name       string
		startView  ViewType
		key        string
		wantView   ViewType
		wantStatus string
	}{
		// H key routes to StatusView from various views
		{
			name:      "H key from BoardView to StatusView",
			startView: BoardView,
			key:       "H",
			wantView:  StatusView,
		},
		{
			name:      "H key from InsightsView to StatusView",
			startView: InsightsView,
			key:       "H",
			wantView:  StatusView,
		},
		{
			name:      "H key from ListView to StatusView",
			startView: ListView,
			key:       "H",
			wantView:  StatusView,
		},
		{
			name:      "H key from DetailView to StatusView",
			startView: DetailView,
			key:       "H",
			wantView:  StatusView,
		},
		// w key routes to StatusView from various views
		{
			name:      "w key from BoardView to StatusView",
			startView: BoardView,
			key:       "w",
			wantView:  StatusView,
		},
		{
			name:      "w key from InsightsView to StatusView",
			startView: InsightsView,
			key:       "w",
			wantView:  StatusView,
		},
		{
			name:      "w key from ListView to StatusView",
			startView: ListView,
			key:       "w",
			wantView:  StatusView,
		},
		{
			name:      "w key from DetailView to StatusView",
			startView: DetailView,
			key:       "w",
			wantView:  StatusView,
		},
		// L key routes to ListView from various views
		{
			name:      "L key from StatusView to ListView",
			startView: StatusView,
			key:       "L",
			wantView:  ListView,
		},
		{
			name:      "L key from BoardView to ListView",
			startView: BoardView,
			key:       "L",
			wantView:  ListView,
		},
		{
			name:      "L key from InsightsView to ListView",
			startView: InsightsView,
			key:       "L",
			wantView:  ListView,
		},
		{
			name:      "L key from DetailView to ListView",
			startView: DetailView,
			key:       "L",
			wantView:  ListView,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel()
			m.activeView = tt.startView

			// For DetailView, set up a bead detail model
			if tt.startView == DetailView {
				m.detailModel = &DetailModel{
					bead: protocol.BeadDetail{
						ID:    "test-001",
						Title: "Test Bead",
					},
					theme:  m.theme,
					styles: m.styles,
				}
			}

			// Send the key press
			keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.key)}
			updated, _ := m.Update(keyMsg)
			mUpdated, ok := updated.(Model)
			if !ok {
				t.Fatalf("Update() returned %T, expected Model", updated)
			}

			if mUpdated.activeView != tt.wantView {
				t.Errorf("key %q: got view %v, want %v", tt.key, mUpdated.activeView, tt.wantView)
			}
		})
	}
}

// TestDetailViewKeys_ForwardToDetailModel verifies that j/k/enter/pgup/pgdown
// in DetailView are forwarded to DetailModel.Update() as required by AC (oro-tm8m.14).
func TestDetailViewKeys_ForwardToDetailModel(t *testing.T) {
	theme := DefaultTheme()
	styles := NewStyles(theme)

	makeModel := func(dm DetailModel) Model {
		m := newModel()
		m.activeView = DetailView
		m.detailModel = &dm
		return m
	}

	t.Run("j_scrolls_viewport_down_on_non-Deps_tab", func(t *testing.T) {
		bead := protocol.BeadDetail{Status: "in_progress", Title: "Test"}
		dm := newDetailModel(bead, theme, styles)
		dm.tabViewport.SetContent(strings.Repeat("line\n", 50))
		dm.activeTab = 0
		dm.viewportActiveTab = 0

		m := makeModel(dm)
		initialY := m.detailModel.tabViewport.YOffset

		result, _ := m.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		updated, ok := result.(Model)
		if !ok {
			t.Fatal("j: handleKeyPress did not return Model")
		}
		if updated.detailModel.tabViewport.YOffset <= initialY {
			t.Errorf("j: viewport YOffset = %d, want > %d", updated.detailModel.tabViewport.YOffset, initialY)
		}
	})

	t.Run("k_scrolls_viewport_up_on_non-Deps_tab", func(t *testing.T) {
		bead := protocol.BeadDetail{Status: "in_progress", Title: "Test"}
		dm := newDetailModel(bead, theme, styles)
		dm.tabViewport.SetContent(strings.Repeat("line\n", 50))
		dm.tabViewport.SetYOffset(10)
		dm.activeTab = 0
		dm.viewportActiveTab = 0

		m := makeModel(dm)
		initialY := m.detailModel.tabViewport.YOffset

		result, _ := m.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
		updated, ok := result.(Model)
		if !ok {
			t.Fatal("k: handleKeyPress did not return Model")
		}
		if updated.detailModel.tabViewport.YOffset >= initialY {
			t.Errorf("k: viewport YOffset = %d, want < %d", updated.detailModel.tabViewport.YOffset, initialY)
		}
	})

	t.Run("j_moves_dep_cursor_down_on_Deps_tab", func(t *testing.T) {
		bead := protocol.BeadDetail{
			Status: "in_progress",
			Title:  "Test",
			Dependencies: []protocol.Dependency{
				{DependsOnID: "oro-dep1"},
				{DependsOnID: "oro-dep2"},
			},
		}
		dm := newDetailModel(bead, theme, styles)
		for dm.tabs[dm.activeTab] != "Deps" {
			dm = dm.nextTab()
		}
		dm.viewportActiveTab = dm.activeTab
		dm.depSelectedIdx = 0

		m := makeModel(dm)

		result, _ := m.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		updated, ok := result.(Model)
		if !ok {
			t.Fatal("j on Deps: handleKeyPress did not return Model")
		}
		if updated.detailModel.depSelectedIdx != 1 {
			t.Errorf("j on Deps: depSelectedIdx = %d, want 1", updated.detailModel.depSelectedIdx)
		}
	})

	t.Run("enter_on_Deps_tab_navigates_to_dep_bead", func(t *testing.T) {
		bead := protocol.BeadDetail{
			Status: "in_progress",
			Title:  "Test",
			Dependencies: []protocol.Dependency{
				{DependsOnID: "oro-dep1"},
			},
		}
		dm := newDetailModel(bead, theme, styles)
		for dm.tabs[dm.activeTab] != "Deps" {
			dm = dm.nextTab()
		}
		dm.viewportActiveTab = dm.activeTab
		dm.depSelectedIdx = 0

		m := makeModel(dm)

		_, cmd := m.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("enter on Deps: expected non-nil command")
		}
		msg := cmd()
		nav, ok := msg.(navigateToDepMsg)
		if !ok {
			t.Fatalf("enter on Deps: expected navigateToDepMsg, got %T", msg)
		}
		if nav.beadID != "oro-dep1" {
			t.Errorf("enter on Deps: beadID = %q, want %q", nav.beadID, "oro-dep1")
		}
	})

	t.Run("pgup_scrolls_viewport_up_on_non-Deps_tab", func(t *testing.T) {
		bead := protocol.BeadDetail{Status: "in_progress", Title: "Test"}
		dm := newDetailModel(bead, theme, styles)
		dm.tabViewport.SetContent(strings.Repeat("line\n", 50))
		dm.tabViewport.SetYOffset(20)
		dm.activeTab = 0
		dm.viewportActiveTab = 0

		m := makeModel(dm)
		initialY := m.detailModel.tabViewport.YOffset

		result, _ := m.handleKeyPress(tea.KeyMsg{Type: tea.KeyPgUp})
		updated, ok := result.(Model)
		if !ok {
			t.Fatal("pgup: handleKeyPress did not return Model")
		}
		if updated.detailModel.tabViewport.YOffset >= initialY {
			t.Errorf("pgup: viewport YOffset = %d, want < %d", updated.detailModel.tabViewport.YOffset, initialY)
		}
	})

	t.Run("pgdn_scrolls_viewport_down_on_non-Deps_tab", func(t *testing.T) {
		bead := protocol.BeadDetail{Status: "in_progress", Title: "Test"}
		dm := newDetailModel(bead, theme, styles)
		dm.tabViewport.SetContent(strings.Repeat("line\n", 50))
		dm.activeTab = 0
		dm.viewportActiveTab = 0

		m := makeModel(dm)
		initialY := m.detailModel.tabViewport.YOffset

		result, _ := m.handleKeyPress(tea.KeyMsg{Type: tea.KeyPgDown})
		updated, ok := result.(Model)
		if !ok {
			t.Fatal("pgdn: handleKeyPress did not return Model")
		}
		if updated.detailModel.tabViewport.YOffset <= initialY {
			t.Errorf("pgdn: viewport YOffset = %d, want > %d", updated.detailModel.tabViewport.YOffset, initialY)
		}
	})
}
