package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestHelpViewToggle verifies that ? key toggles help overlay on/off.
func TestHelpViewToggle(t *testing.T) {
	m := newModel()
	m.activeView = BoardView

	// Press ? to open help
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	var ok bool
	m, ok = updated.(Model)
	if !ok {
		t.Fatal("Expected Model from Update")
	}
	if cmd != nil {
		t.Errorf("Expected nil cmd when opening help, got %v", cmd)
	}

	if m.activeView != HelpView {
		t.Errorf("Expected HelpView after pressing ?, got %v", m.activeView)
	}

	// Press ? again to close help
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m, ok = updated.(Model)
	if !ok {
		t.Fatal("Expected Model from Update")
	}
	if cmd != nil {
		t.Errorf("Expected nil cmd when closing help, got %v", cmd)
	}

	if m.activeView != BoardView {
		t.Errorf("Expected BoardView after toggling help off, got %v", m.activeView)
	}
}

// TestHelpViewEscDismisses verifies that Esc key dismisses help overlay.
func TestHelpViewEscDismisses(t *testing.T) {
	m := newModel()
	m.activeView = HelpView
	m.previousView = BoardView

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	var ok bool
	m, ok = updated.(Model)
	if !ok {
		t.Fatal("Expected Model from Update")
	}
	if cmd != nil {
		t.Errorf("Expected nil cmd when dismissing help, got %v", cmd)
	}

	if m.activeView != BoardView {
		t.Errorf("Expected BoardView after Esc from help, got %v", m.activeView)
	}
}

// TestHelpViewFromDetailView verifies help can be opened from detail view.
func TestHelpViewFromDetailView(t *testing.T) {
	m := newModel()
	m.activeView = DetailView

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	var ok bool
	m, ok = updated.(Model)
	if !ok {
		t.Fatal("Expected Model from Update")
	}

	if m.activeView != HelpView {
		t.Errorf("Expected HelpView after pressing ? from detail, got %v", m.activeView)
	}
	if m.previousView != DetailView {
		t.Errorf("Expected previousView to be DetailView, got %v", m.previousView)
	}

	// Dismiss and return to detail
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m, ok = updated.(Model)
	if !ok {
		t.Fatal("Expected Model from Update")
	}

	if m.activeView != DetailView {
		t.Errorf("Expected DetailView after dismissing help, got %v", m.activeView)
	}
}

// TestHelpViewFromInsightsView verifies help can be opened from insights view.
func TestHelpViewFromInsightsView(t *testing.T) {
	m := newModel()
	m.activeView = InsightsView

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	var ok bool
	m, ok = updated.(Model)
	if !ok {
		t.Fatal("Expected Model from Update")
	}

	if m.activeView != HelpView {
		t.Errorf("Expected HelpView after pressing ? from insights, got %v", m.activeView)
	}
	if m.previousView != InsightsView {
		t.Errorf("Expected previousView to be InsightsView, got %v", m.previousView)
	}
}

// TestHelpDoesNotInterfereWithSearch verifies help doesn't interfere with search overlay.
func TestHelpDoesNotInterfereWithSearch(t *testing.T) {
	m := newModel()
	m.activeView = SearchView
	m.searchInput.Focus()
	m.searchInput.SetValue("test")

	// Pressing ? in search should type '?' not open help
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	var ok bool
	m, ok = updated.(Model)
	if !ok {
		t.Fatal("Expected Model from Update")
	}

	if m.activeView != SearchView {
		t.Errorf("Expected to stay in SearchView when typing ?, got %v", m.activeView)
	}
	if !strings.Contains(m.searchInput.Value(), "?") {
		t.Errorf("Expected search query to contain '?', got %s", m.searchInput.Value())
	}
}

// TestHelpContentBoard verifies board-specific help content is shown.
func TestHelpContentBoard(t *testing.T) {
	m := newModel()
	m.activeView = HelpView
	m.previousView = BoardView

	view := m.View()

	// Should show board-specific keys
	expectedKeys := []string{"j/k", "h/l", "enter", "i", "/", "q"}
	for _, key := range expectedKeys {
		if !strings.Contains(view, key) {
			t.Errorf("Board help missing key binding: %s", key)
		}
	}
}

// TestHelpContentDetail verifies detail-specific help content is shown.
func TestHelpContentDetail(t *testing.T) {
	m := newModel()
	m.activeView = HelpView
	m.previousView = DetailView

	view := m.View()

	// Should show detail-specific keys
	expectedKeys := []string{"tab", "esc", "q"}
	for _, key := range expectedKeys {
		if !strings.Contains(view, key) {
			t.Errorf("Detail help missing key binding: %s", key)
		}
	}
}

// TestHelpContentInsights verifies insights-specific help content is shown.
func TestHelpContentInsights(t *testing.T) {
	m := newModel()
	m.activeView = HelpView
	m.previousView = InsightsView

	view := m.View()

	// Should show insights-specific keys
	expectedKeys := []string{"esc", "q"}
	for _, key := range expectedKeys {
		if !strings.Contains(view, key) {
			t.Errorf("Insights help missing key binding: %s", key)
		}
	}
}

// TestHelpContentSearch verifies search-specific help content is shown.
func TestHelpContentSearch(t *testing.T) {
	m := newModel()
	m.activeView = HelpView
	m.previousView = SearchView

	view := m.View()

	// Should show search-specific keys
	expectedKeys := []string{"enter", "esc", "↑↓"}
	for _, key := range expectedKeys {
		if !strings.Contains(view, key) {
			t.Errorf("Search help missing key binding: %s", key)
		}
	}
}

// TestHelpRendersWithoutCrashing verifies help view renders successfully.
func TestHelpRendersWithoutCrashing(t *testing.T) {
	m := newModel()
	m.activeView = HelpView
	m.previousView = BoardView

	view := m.View()
	if view == "" {
		t.Error("Help view rendered empty string")
	}
	if !strings.Contains(view, "Help") {
		t.Error("Help view missing title")
	}
}
