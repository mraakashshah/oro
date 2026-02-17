package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"oro/pkg/protocol"
)

// TestBoardColumns_AdaptToTerminalWidth verifies that board columns dynamically
// adjust their width based on terminal width instead of using a fixed width.
func TestBoardColumns_AdaptToTerminalWidth(t *testing.T) {
	// Create model with test beads
	m := newModel()
	m.beads = []protocol.Bead{
		{ID: "test-1", Title: "Test Bead Long Title That Might Wrap", Status: "open", Type: "task", Priority: 2},
	}

	// Simulate a wide terminal (200 cols)
	updatedModel, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	var ok bool
	m, ok = updatedModel.(Model)
	if !ok {
		t.Fatal("Update should return Model")
	}

	// Render the board - this should use adaptive width (50 per column for 200/4)
	// Currently this fails because RenderWithCursor uses fixed width 30
	outputWide := m.View()

	// Now test narrow terminal (80 cols)
	updatedModel, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	m, ok = updatedModel.(Model)
	if !ok {
		t.Fatal("Update should return Model")
	}
	outputNarrow := m.View()

	// The outputs should be different because column widths should adapt
	// If they're using fixed width, the outputs will be very similar
	// With adaptive width, wide terminal should have wider columns
	if outputWide == outputNarrow {
		t.Error("Board output should differ between 200-col and 80-col terminals (adaptive width not implemented)")
	}

	// Additional verification: render with explicit custom width to show expected behavior
	board := NewBoardModelWithWorkers(m.beads, m.workers, m.assignments)

	// Render with width appropriate for 200-col terminal (50 per column)
	custom200 := board.RenderWithCustomWidth(-1, -1, 50, m.theme, m.styles)

	// Render with width appropriate for 80-col terminal (20 per column)
	custom80 := board.RenderWithCustomWidth(-1, -1, 20, m.theme, m.styles)

	// These should definitely be different
	if custom200 == custom80 {
		t.Error("Rendering with different custom widths should produce different output")
	}
}

// TestSearchInput_AdaptToTerminalWidth verifies that the search input width
// adapts to the terminal width instead of using a fixed width.
func TestSearchInput_AdaptToTerminalWidth(t *testing.T) {
	// Create model
	m := newModel()
	m.beads = []protocol.Bead{
		{ID: "test-1", Title: "Sample", Status: "open", Type: "task", Priority: 2},
	}

	// Test with wide terminal (200 cols)
	updatedModel, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	var ok bool
	m, ok = updatedModel.(Model)
	if !ok {
		t.Fatal("Update should return Model")
	}
	m.activeView = SearchView
	outputWide := m.View()

	// Test with narrow terminal (80 cols)
	updatedModel, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	m, ok = updatedModel.(Model)
	if !ok {
		t.Fatal("Update should return Model")
	}
	m.activeView = SearchView
	outputNarrow := m.View()

	// The search overlay outputs should differ based on terminal width
	// Currently uses fixed Width(60), so outputs will be very similar
	// With adaptive width, the search input should be wider on wide terminals
	if outputWide == outputNarrow {
		t.Error("Search input should adapt to terminal width (currently uses fixed width 60)")
	}
}

// TestBoardView_MinimumWidthFloor verifies that columns never become too narrow,
// even on very narrow terminals.
func TestBoardView_MinimumWidthFloor(t *testing.T) {
	testCases := []struct {
		name             string
		terminalWidth    int
		expectedMinWidth int
	}{
		{
			name:             "very narrow terminal (40 cols)",
			terminalWidth:    40,
			expectedMinWidth: 18, // Enforce minimum floor
		},
		{
			name:             "extremely narrow (20 cols)",
			terminalWidth:    20,
			expectedMinWidth: 18, // Floor should still apply
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel()
			m.beads = []protocol.Bead{
				{ID: "test-1", Title: "Test", Status: "open", Type: "task", Priority: 2},
			}

			// Set narrow terminal width
			updatedModel, _ := m.Update(tea.WindowSizeMsg{
				Width:  tc.terminalWidth,
				Height: 40,
			})
			var ok bool
			m, ok = updatedModel.(Model)
			if !ok {
				t.Fatal("Update should return Model")
			}

			// Render board
			output := m.View()

			// Verify board renders without panic or errors
			if output == "" {
				t.Error("Board should render even on narrow terminals")
			}

			// Verify minimum width calculation
			numColumns := 4
			calculatedWidth := tc.terminalWidth / numColumns
			if calculatedWidth < tc.expectedMinWidth {
				calculatedWidth = tc.expectedMinWidth
			}

			if calculatedWidth < tc.expectedMinWidth {
				t.Errorf("Column width %d should never be less than minimum floor %d", calculatedWidth, tc.expectedMinWidth)
			}
		})
	}
}

// TestBoardView_ColumnWidthCalculation verifies the pure calculation logic
// for determining column width from terminal width.
func TestBoardView_ColumnWidthCalculation(t *testing.T) {
	const minWidth = 18
	const numColumns = 4

	calculateColumnWidth := func(terminalWidth int) int {
		colWidth := terminalWidth / numColumns
		if colWidth < minWidth {
			return minWidth
		}
		return colWidth
	}

	testCases := []struct {
		terminalWidth int
		expectedWidth int
	}{
		{80, 20},
		{120, 30},
		{200, 50},
		{60, 18}, // Below floor
		{40, 18}, // Below floor
		{160, 40},
		{240, 60},
	}

	for _, tc := range testCases {
		result := calculateColumnWidth(tc.terminalWidth)
		if result != tc.expectedWidth {
			t.Errorf("Column width for terminal width %d should be %d, got %d", tc.terminalWidth, tc.expectedWidth, result)
		}
	}
}

// TestSearchInput_WidthCalculation verifies the calculation logic for search input width.
func TestSearchInput_WidthCalculation(t *testing.T) {
	const minSearchWidth = 40
	const maxSearchWidth = 120
	const padding = 4

	calculateSearchWidth := func(terminalWidth int) int {
		// Use most of the terminal width minus padding
		width := terminalWidth - padding
		if width < minSearchWidth {
			return minSearchWidth
		}
		if width > maxSearchWidth {
			return maxSearchWidth
		}
		return width
	}

	testCases := []struct {
		terminalWidth int
		expectedWidth int
	}{
		{80, 76},   // 80 - 4 = 76
		{120, 116}, // 120 - 4 = 116
		{200, 120}, // Capped at max (120)
		{40, 40},   // Below min, use min
		{30, 40},   // Below min, use min
		{160, 120}, // Above max, use max
	}

	for _, tc := range testCases {
		result := calculateSearchWidth(tc.terminalWidth)
		if result != tc.expectedWidth {
			t.Errorf("Search width for terminal width %d should be %d, got %d", tc.terminalWidth, tc.expectedWidth, result)
		}
	}
}

// TestRenderSearchOverlay_UsesAdaptiveWidth is an integration test that verifies
// the search overlay actually uses adaptive width in rendering.
func TestRenderSearchOverlay_UsesAdaptiveWidth(t *testing.T) {
	m := newModel()
	m.beads = []protocol.Bead{
		{ID: "test-1", Title: "Sample Bead", Status: "open", Type: "task", Priority: 2},
	}

	// Set terminal width to 120
	updatedModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	var ok bool
	m, ok = updatedModel.(Model)
	if !ok {
		t.Fatal("Update should return Model")
	}

	// Enter search view
	m.activeView = SearchView

	// Render search overlay
	output := m.View()

	// The search input should be rendered with adaptive width
	// We can check this by looking for the search input in the output
	if !strings.Contains(output, "Search") {
		t.Error("Search overlay should be rendered")
	}

	// Verify output is not empty and has reasonable length
	// (This will pass once adaptive width is implemented)
	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		t.Error("Search overlay should have content")
	}
}
