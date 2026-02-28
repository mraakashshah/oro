package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestHelpBindings_ListView verifies that ListView has complete help bindings
// in getHelpBindingsForView, getViewName, and helpHintsForView.
func TestHelpBindings_ListView(t *testing.T) {
	// getHelpBindingsForView must return ListView-specific bindings.
	bindings := getHelpBindingsForView(ListView)
	bindingMap := make(map[string]string)
	for _, b := range bindings {
		bindingMap[b.key] = b.desc
	}

	// enter → full detail
	enterDesc, hasEnter := bindingMap["enter"]
	if !hasEnter {
		t.Error("ListView bindings missing 'enter' key")
	} else if !strings.Contains(strings.ToLower(enterDesc), "detail") {
		t.Errorf("ListView 'enter' desc should mention 'detail', got %q", enterDesc)
	}

	// b → board
	boardDesc, hasBoard := bindingMap["b"]
	if !hasBoard {
		t.Error("ListView bindings missing 'b' key")
	} else if !strings.Contains(strings.ToLower(boardDesc), "board") {
		t.Errorf("ListView 'b' desc should mention 'board', got %q", boardDesc)
	}

	// y → clipboard
	clipDesc, hasClip := bindingMap["y"]
	if !hasClip {
		t.Error("ListView bindings missing 'y' key")
	} else if !strings.Contains(strings.ToLower(clipDesc), "clipboard") {
		t.Errorf("ListView 'y' desc should mention 'clipboard', got %q", clipDesc)
	}

	// getViewName returns a meaningful name (not the fallthrough "Unknown View").
	name := getViewName(ListView)
	if name == "Unknown View" || name == "" {
		t.Errorf("getViewName(ListView) = %q, want meaningful name", name)
	}

	// helpHintsForView returns non-empty hints for ListView at a wide terminal.
	hints := helpHintsForView(ListView, 80)
	if hints == "" {
		t.Error("helpHintsForView(ListView, 80) returned empty string, want hints")
	}
}

// TestHelpViewToggle verifies that ? key toggles help overlay on/off.
func TestHelpViewToggle(t *testing.T) {
	m := newModel()
	m.initialLoad = false // Skip loading state for this test
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
	m.initialLoad = false // Skip loading state for this test
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
	m.initialLoad = false // Skip loading state for this test
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
	m.initialLoad = false // Skip loading state for this test
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
	m.initialLoad = false // Skip loading state for this test
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
	m.initialLoad = false // Skip loading state for this test
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
	m.initialLoad = false // Skip loading state for this test
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
	m.initialLoad = false // Skip loading state for this test
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
	m.initialLoad = false // Skip loading state for this test
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

// TestHelpBindings_AllKeysPresent verifies that all functional key bindings are
// documented in the help overlay for BoardView, ListView, and DetailView.
func TestHelpBindings_AllKeysPresent(t *testing.T) {
	// helper: build key → desc map from a slice of bindings.
	keyMap := func(bindings []helpBinding) map[string]string {
		m := make(map[string]string, len(bindings))
		for _, b := range bindings {
			m[b.key] = b.desc
		}
		return m
	}

	// helper: assert a binding exists and its description contains a substring.
	assertBinding := func(t *testing.T, bm map[string]string, view, key, wantSubstr string) {
		t.Helper()
		desc, ok := bm[key]
		if !ok {
			t.Errorf("%s help missing key %q", view, key)
			return
		}
		if !strings.Contains(strings.ToLower(desc), strings.ToLower(wantSubstr)) {
			t.Errorf("%s help key %q desc = %q, want it to contain %q", view, key, desc, wantSubstr)
		}
	}

	t.Run("BoardView", func(t *testing.T) {
		bm := keyMap(getBoardHelpBindings())
		assertBinding(t, bm, "BoardView", "s", "status")
		assertBinding(t, bm, "BoardView", "m", "load")
		assertBinding(t, bm, "BoardView", "</>", "resize")
	})

	t.Run("ListView", func(t *testing.T) {
		bm := keyMap(getListViewHelpBindings())
		assertBinding(t, bm, "ListView", "s", "status")
		assertBinding(t, bm, "ListView", "m", "load")
		assertBinding(t, bm, "ListView", "</>", "resize")
		assertBinding(t, bm, "ListView", "i", "insights")
	})

	t.Run("DetailView", func(t *testing.T) {
		bm := keyMap(getDetailHelpBindings())
		assertBinding(t, bm, "DetailView", "j/k", "scroll")
		assertBinding(t, bm, "DetailView", "enter", "dep")
		assertBinding(t, bm, "DetailView", "</>", "split")
	})
}

// TestHelpBindingsComplete verifies all functional key bindings are documented.
func TestHelpBindingsComplete(t *testing.T) {
	keyMap := func(bindings []helpBinding) map[string]string {
		m := make(map[string]string, len(bindings))
		for _, b := range bindings {
			m[b.key] = b.desc
		}
		return m
	}
	hasKey := func(bm map[string]string, key string) bool {
		_, ok := bm[key]
		return ok
	}
	descContains := func(bm map[string]string, key, sub string) bool {
		d, ok := bm[key]
		return ok && strings.Contains(strings.ToLower(d), strings.ToLower(sub))
	}

	// (1) ListView help section contains 'm' key with 'load more closed' description.
	t.Run("ListView_m_load_more_closed", func(t *testing.T) {
		bm := keyMap(getListViewHelpBindings())
		if !descContains(bm, "m", "load more closed") {
			t.Errorf("ListView bindings: 'm' desc should contain 'load more closed', got %q", bm["m"])
		}
	})

	// (2) DetailView help section contains '<' and '>' keys with 'adjust split width' description.
	t.Run("DetailView_split_resize_keys", func(t *testing.T) {
		bm := keyMap(getDetailHelpBindings())
		// accept either separate keys or combined "</>", but desc must contain "adjust split"
		foundLt := hasKey(bm, "<") || hasKey(bm, "</>")
		foundGt := hasKey(bm, ">") || hasKey(bm, "</>")
		if !foundLt || !foundGt {
			t.Errorf("DetailView bindings missing '<' and/or '>' key (keys present: %v)", bm)
		}
		for _, k := range []string{"<", ">", "</>"} {
			if d, ok := bm[k]; ok {
				if !strings.Contains(strings.ToLower(d), "split") {
					t.Errorf("DetailView binding %q desc = %q, want it to contain 'split'", k, d)
				}
			}
		}
	})

	// (3) BoardView help section contains all keys currently handled in handleBoardViewKeys.
	// Specifically: H/w (status) and L (list view) must be present.
	t.Run("BoardView_HwL_present", func(t *testing.T) {
		bm := keyMap(getBoardHelpBindings())
		hwKey := hasKey(bm, "H/w") || hasKey(bm, "H") || hasKey(bm, "w")
		if !hwKey {
			t.Error("BoardView bindings missing H/w key for status navigation")
		}
		if !hasKey(bm, "L") {
			t.Error("BoardView bindings missing 'L' key for list view")
		}
	})

	// (4) H/w and L keys documented in all views that handle them.
	t.Run("AllViews_HwL_documented", func(t *testing.T) {
		views := []struct {
			name     string
			bindings []helpBinding
			hasHw    bool
			hasL     bool
		}{
			{"BoardView", getBoardHelpBindings(), true, true},
			{"DetailView", getDetailHelpBindings(), true, true},
			{"InsightsView", getInsightsHelpBindings(), true, true},
			{"ListView", getListViewHelpBindings(), true, false}, // L is no-op in ListView
		}
		for _, v := range views {
			bm := keyMap(v.bindings)
			if v.hasHw {
				hwFound := hasKey(bm, "H/w") || hasKey(bm, "H") || hasKey(bm, "w")
				if !hwFound {
					t.Errorf("%s bindings missing H/w key for status navigation", v.name)
				}
			}
			if v.hasL {
				if !hasKey(bm, "L") {
					t.Errorf("%s bindings missing 'L' key for list view", v.name)
				}
			}
		}
	})
}

// TestHelpRendersWithoutCrashing verifies help view renders successfully.
func TestHelpRendersWithoutCrashing(t *testing.T) {
	m := newModel()
	m.initialLoad = false // Skip loading state for this test
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
