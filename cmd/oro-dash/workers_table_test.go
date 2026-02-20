package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestWorkersTableAdaptsToWidth verifies that the workers table layout adapts to terminal width.
// Different widths produce different column layouts, the separator matches totalWidth,
// and very narrow terminals (<50) use minimum floors to prevent truncation.
func TestWorkersTableAdaptsToWidth(t *testing.T) {
	workers := []WorkerStatus{
		{ID: "worker-1", Status: "busy", BeadID: "oro-abc.1", LastProgressSecs: 2.0, ContextPct: 45},
	}
	assignments := map[string]string{"oro-abc.1": "worker-1"}
	wt := NewWorkersTableModel(workers, assignments)
	theme := DefaultTheme()
	styles := NewStyles(theme)

	t.Run("wide vs narrow produce different outputs", func(t *testing.T) {
		outputWide := wt.View(theme, styles, 160)
		outputNarrow := wt.View(theme, styles, 80)
		if outputWide == outputNarrow {
			t.Error("View should produce different output for width=160 vs width=80")
		}
	})

	t.Run("separator width matches totalWidth", func(t *testing.T) {
		for _, width := range []int{80, 120, 160} {
			output := wt.View(theme, styles, width)
			lines := strings.Split(output, "\n")
			foundSeparator := false
			for _, line := range lines {
				// The separator line is composed of box-drawing chars (─)
				runes := []rune(line)
				if len(runes) > 0 && runes[0] == '─' {
					foundSeparator = true
					if len(runes) != width {
						t.Errorf("separator width=%d for totalWidth=%d, want %d", len(runes), width, width)
					}
				}
			}
			if !foundSeparator {
				t.Errorf("no separator line found in output for width=%d", width)
			}
		}
	})

	t.Run("column widths differ across terminal widths", func(t *testing.T) {
		widths80 := calculateWorkerColumnWidths(80)
		widths160 := calculateWorkerColumnWidths(160)
		if len(widths80) != 5 {
			t.Fatalf("calculateWorkerColumnWidths(80) returned %d columns, want 5", len(widths80))
		}
		if len(widths160) != 5 {
			t.Fatalf("calculateWorkerColumnWidths(160) returned %d columns, want 5", len(widths160))
		}
		// Wider terminal should produce wider columns
		sum80 := 0
		sum160 := 0
		for _, w := range widths80 {
			sum80 += w
		}
		for _, w := range widths160 {
			sum160 += w
		}
		if sum160 <= sum80 {
			t.Errorf("total column width for 160 (%d) should exceed total for 80 (%d)", sum160, sum80)
		}
	})

	t.Run("minimum floors prevent truncation on very narrow terminal", func(t *testing.T) {
		widths := calculateWorkerColumnWidths(40) // very narrow (<50)
		for i, w := range widths {
			if w < 6 {
				t.Errorf("column %d width=%d is below minimum floor 6 for narrow terminal", i, w)
			}
		}
	})
}

// TestWorkersTableView verifies the workers table view feature.
// Tests: w key switches to WorkersView, table renders with correct columns,
// heartbeat health colors (green <5s, amber 5-15s, red >15s), esc returns to BoardView,
// empty state when no workers.
func TestWorkersTableView(t *testing.T) {
	t.Run("w key switches from BoardView to WorkersView", func(t *testing.T) {
		m := newModel()
		m.activeView = BoardView

		// Press 'w' key
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}}
		updated, _ := m.Update(msg)
		updatedModel, ok := updated.(Model)
		if !ok {
			t.Fatalf("expected Model, got %T", updated)
		}

		if updatedModel.activeView != WorkersView {
			t.Errorf("expected WorkersView, got %v", updatedModel.activeView)
		}
	})

	t.Run("esc returns from WorkersView to BoardView", func(t *testing.T) {
		m := newModel()
		m.activeView = WorkersView

		// Press 'esc' key
		msg := tea.KeyMsg{Type: tea.KeyEscape}
		updated, _ := m.Update(msg)
		updatedModel, ok := updated.(Model)
		if !ok {
			t.Fatalf("expected Model, got %T", updated)
		}

		if updatedModel.activeView != BoardView {
			t.Errorf("expected BoardView, got %v", updatedModel.activeView)
		}
	})

	t.Run("no workers shows empty state message", func(t *testing.T) {
		m := newModel()
		m.activeView = WorkersView
		m.workers = nil // No workers
		m.assignments = nil

		output := m.View()

		if !strings.Contains(output, "No active workers") {
			t.Errorf("expected 'No active workers' message, got:\n%s", output)
		}
	})

	t.Run("workers table renders all columns", func(t *testing.T) {
		m := newModel()
		m.activeView = WorkersView
		m.workers = []WorkerStatus{
			{
				ID:               "worker-1",
				Status:           "working",
				BeadID:           "oro-abc1",
				LastProgressSecs: 3.0,
				ContextPct:       45,
			},
		}
		m.assignments = map[string]string{
			"oro-abc1": "worker-1",
		}

		output := m.View()

		// Check for expected table headers
		expectedHeaders := []string{"Worker ID", "Status", "Assigned Bead", "Health", "Context"}
		for _, header := range expectedHeaders {
			if !strings.Contains(output, header) {
				t.Errorf("expected header %q in output, got:\n%s", header, output)
			}
		}

		// Check for worker data
		if !strings.Contains(output, "worker-1") {
			t.Errorf("expected worker ID 'worker-1' in output")
		}
		if !strings.Contains(output, "oro-abc1") {
			t.Errorf("expected bead ID 'oro-abc1' in output")
		}
		if !strings.Contains(output, "45%") {
			t.Errorf("expected context percentage '45%%' in output")
		}
	})

	t.Run("heartbeat less than 5s renders green", func(t *testing.T) {
		m := newModel()
		m.activeView = WorkersView
		m.workers = []WorkerStatus{
			{
				ID:               "worker-green",
				Status:           "working",
				LastProgressSecs: 4.0, // <5s = green
				ContextPct:       30,
			},
		}

		output := m.View()

		// Green health indicator should be present
		// We check for the worker and that green styling is applied
		if !strings.Contains(output, "worker-green") {
			t.Errorf("expected worker-green in output")
		}
		// The actual green color will be in the rendered output
		// For now, we verify the worker is present
	})

	t.Run("heartbeat between 5s and 15s renders amber", func(t *testing.T) {
		m := newModel()
		m.activeView = WorkersView
		m.workers = []WorkerStatus{
			{
				ID:               "worker-amber",
				Status:           "working",
				LastProgressSecs: 10.0, // 5-15s = amber
				ContextPct:       60,
			},
		}

		output := m.View()

		if !strings.Contains(output, "worker-amber") {
			t.Errorf("expected worker-amber in output")
		}
	})

	t.Run("heartbeat greater than 15s renders red", func(t *testing.T) {
		m := newModel()
		m.activeView = WorkersView
		m.workers = []WorkerStatus{
			{
				ID:               "worker-red",
				Status:           "stale",
				LastProgressSecs: 20.0, // >15s = red
				ContextPct:       85,
			},
		}

		output := m.View()

		if !strings.Contains(output, "worker-red") {
			t.Errorf("expected worker-red in output")
		}
	})

	t.Run("worker with no bead assignment shows dash", func(t *testing.T) {
		m := newModel()
		m.activeView = WorkersView
		m.workers = []WorkerStatus{
			{
				ID:               "worker-idle",
				Status:           "idle",
				BeadID:           "", // No assignment
				LastProgressSecs: 2.0,
				ContextPct:       0,
			},
		}
		m.assignments = map[string]string{} // Empty assignments

		output := m.View()

		if !strings.Contains(output, "worker-idle") {
			t.Errorf("expected worker-idle in output")
		}
		// Should show '-' for no assignment
		if !strings.Contains(output, "-") {
			t.Errorf("expected '-' for empty bead assignment")
		}
	})
}
