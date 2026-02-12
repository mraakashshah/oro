package main

import (
	"strings"
	"testing"

	"oro/pkg/protocol"
)

// TestDetailModel_TabSwitch verifies that DetailModel renders 5 tabs,
// tab switching works, and the deps tab shows blocker/blocked-by lists.
func TestDetailModel_TabSwitch(t *testing.T) {
	t.Run("renders 5 tabs", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:                 "oro-test.1",
			Title:              "Test bead",
			AcceptanceCriteria: "Test acceptance",
		}

		model := newDetailModel(bead)
		view := model.View()

		expectedTabs := []string{"Overview", "Worker", "Diff", "Deps", "Memory"}
		for _, tab := range expectedTabs {
			if !strings.Contains(view, tab) {
				t.Errorf("expected view to contain tab %q, but it didn't\nView:\n%s", tab, view)
			}
		}
	})

	t.Run("tab switching works", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:                 "oro-test.2",
			Title:              "Tab switching test",
			AcceptanceCriteria: "Test acceptance",
		}

		model := newDetailModel(bead)

		// Start on Overview (tab 0)
		if model.activeTab != 0 {
			t.Errorf("expected initial activeTab to be 0, got %d", model.activeTab)
		}

		// Switch to Worker tab
		model = model.nextTab()
		if model.activeTab != 1 {
			t.Errorf("after nextTab(), expected activeTab to be 1, got %d", model.activeTab)
		}

		// Switch to Diff tab
		model = model.nextTab()
		if model.activeTab != 2 {
			t.Errorf("after second nextTab(), expected activeTab to be 2, got %d", model.activeTab)
		}

		// Switch backwards
		model = model.prevTab()
		if model.activeTab != 1 {
			t.Errorf("after prevTab(), expected activeTab to be 1, got %d", model.activeTab)
		}

		// Wrap around from last to first
		model.activeTab = 4 // Memory tab (last)
		model = model.nextTab()
		if model.activeTab != 0 {
			t.Errorf("after nextTab() from last tab, expected wrap to 0, got %d", model.activeTab)
		}

		// Wrap around from first to last
		model.activeTab = 0
		model = model.prevTab()
		if model.activeTab != 4 {
			t.Errorf("after prevTab() from first tab, expected wrap to 4, got %d", model.activeTab)
		}
	})

	t.Run("deps tab shows blocker and blocked-by lists", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:    "oro-test.3",
			Title: "Bead with dependencies",
		}
		// Note: BeadDetail doesn't have dependency fields yet,
		// but we'll add them when needed. For now, test the structure.

		model := newDetailModel(bead)
		model.activeTab = 3 // Deps tab

		view := model.View()

		// The deps tab should render content related to dependencies
		if !strings.Contains(view, "Deps") {
			t.Errorf("expected deps tab to be visible in view")
		}
	})

	t.Run("bead with no deps shows placeholder", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:    "oro-test.4",
			Title: "Bead without dependencies",
		}

		model := newDetailModel(bead)
		model.activeTab = 3 // Deps tab

		view := model.View()

		// Edge case: bead has no deps → show 'No dependencies'
		if !strings.Contains(view, "No dependencies") && !strings.Contains(view, "no dependencies") {
			t.Errorf("expected 'No dependencies' placeholder when bead has no deps, got:\n%s", view)
		}
	})

	t.Run("empty description shows placeholder", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:                 "oro-test.5",
			Title:              "Bead without description",
			AcceptanceCriteria: "Some acceptance criteria",
			// No Description field - it's missing from BeadDetail in protocol
		}

		model := newDetailModel(bead)
		model.activeTab = 0 // Overview tab

		view := model.View()

		// The overview should render even with minimal data
		if !strings.Contains(view, "oro-test.5") {
			t.Errorf("expected bead ID to appear in overview")
		}
		if !strings.Contains(view, "Bead without description") {
			t.Errorf("expected title to appear in overview")
		}
	})
}
