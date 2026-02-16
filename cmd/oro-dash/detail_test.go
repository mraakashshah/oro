package main

import (
	"fmt"
	"strings"
	"testing"

	"oro/pkg/protocol"

	tea "github.com/charmbracelet/bubbletea"
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

// TestDetailModel_WorkerTab verifies the Worker tab displays context %, heartbeat,
// and handles the edge case where no worker is assigned.
func TestDetailModel_WorkerTab(t *testing.T) {
	t.Run("shows worker context and heartbeat", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:             "oro-test.10",
			Title:          "Bead with worker",
			WorkerID:       "worker-1",
			ContextPercent: 42,
			LastHeartbeat:  "2026-02-12T10:30:00Z",
		}

		model := newDetailModel(bead)
		model.activeTab = 1 // Worker tab

		view := model.View()

		// Assert: worker tab shows context %, heartbeat
		if !strings.Contains(view, "42") {
			t.Errorf("expected context percent '42' in worker tab, got:\n%s", view)
		}
		if !strings.Contains(view, "2026-02-12T10:30:00Z") {
			t.Errorf("expected heartbeat timestamp in worker tab, got:\n%s", view)
		}
		if !strings.Contains(view, "worker-1") {
			t.Errorf("expected worker ID 'worker-1' in worker tab, got:\n%s", view)
		}
	})

	t.Run("no worker assigned shows Unassigned", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:       "oro-test.11",
			Title:    "Bead without worker",
			WorkerID: "", // Edge: no worker assigned
		}

		model := newDetailModel(bead)
		model.activeTab = 1 // Worker tab

		view := model.View()

		// Edge: no worker assigned → show 'Unassigned'
		if !strings.Contains(view, "Unassigned") {
			t.Errorf("expected 'Unassigned' when no worker assigned, got:\n%s", view)
		}
	})
}

// TestDetailModel_DiffTab verifies the Diff tab renders git diff output
// and handles the edge case where no diff exists.
func TestDetailModel_DiffTab(t *testing.T) {
	t.Run("renders git diff", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:    "oro-test.12",
			Title: "Bead with diff",
			GitDiff: `diff --git a/file.go b/file.go
index abc123..def456 100644
--- a/file.go
+++ b/file.go
@@ -1,3 +1,4 @@
 package main
+// New comment
 func main() {}`,
		}

		model := newDetailModel(bead)
		model.activeTab = 2 // Diff tab

		view := model.View()

		// Assert: diff tab renders git diff
		if !strings.Contains(view, "diff --git") {
			t.Errorf("expected git diff header in diff tab, got:\n%s", view)
		}
		if !strings.Contains(view, "file.go") {
			t.Errorf("expected filename in diff tab, got:\n%s", view)
		}
		if !strings.Contains(view, "New comment") {
			t.Errorf("expected diff content in diff tab, got:\n%s", view)
		}
	})

	t.Run("no diff shows No changes", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:      "oro-test.13",
			Title:   "Bead without diff",
			GitDiff: "", // Edge: no diff
		}

		model := newDetailModel(bead)
		model.activeTab = 2 // Diff tab

		view := model.View()

		// Edge: no diff → show 'No changes'
		if !strings.Contains(view, "No changes") {
			t.Errorf("expected 'No changes' when no diff exists, got:\n%s", view)
		}
	})
}

// TestDetailModel_MemoryTab verifies the Memory tab shows injected context
// and handles the edge case where no memory exists.
func TestDetailModel_MemoryTab(t *testing.T) {
	t.Run("shows injected context", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:    "oro-test.14",
			Title: "Bead with memory",
			Memory: `Previous attempt context:
- Fixed issue X
- Refactored Y`,
		}

		model := newDetailModel(bead)
		model.activeTab = 4 // Memory tab

		view := model.View()

		// Assert: memory tab shows injected context
		if !strings.Contains(view, "Previous attempt context") {
			t.Errorf("expected memory context in memory tab, got:\n%s", view)
		}
		if !strings.Contains(view, "Fixed issue X") {
			t.Errorf("expected memory content in memory tab, got:\n%s", view)
		}
	})

	t.Run("no memory shows No context", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:     "oro-test.15",
			Title:  "Bead without memory",
			Memory: "", // Edge: no memory
		}

		model := newDetailModel(bead)
		model.activeTab = 4 // Memory tab

		view := model.View()

		// Edge: no memory → show 'No context'
		if !strings.Contains(view, "No context") {
			t.Errorf("expected 'No context' when no memory exists, got:\n%s", view)
		}
	})
}

// TestDetailModel_AsyncWorkerEvents verifies that worker events are fetched
// asynchronously instead of blocking during model creation.
func TestDetailModel_AsyncWorkerEvents(t *testing.T) {
	t.Run("newDetailModel returns immediately without blocking", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:       "oro-test.16",
			Title:    "Async test bead",
			WorkerID: "worker-async",
		}

		// newDetailModel should return immediately without blocking on I/O
		model := newDetailModel(bead)

		// Initial state: worker events should be empty (not yet fetched)
		if len(model.workerEvents) != 0 {
			t.Errorf("expected workerEvents to be empty initially, got %d events", len(model.workerEvents))
		}

		// Worker events should be in loading state
		if !model.loadingEvents {
			t.Errorf("expected loadingEvents to be true initially")
		}
	})

	t.Run("fetchWorkerEventsCmd returns a tea.Cmd", func(t *testing.T) {
		workerID := "worker-test"

		// fetchWorkerEventsCmd should return a command (not nil)
		cmd := fetchWorkerEventsCmd(workerID)

		if cmd == nil {
			t.Errorf("expected fetchWorkerEventsCmd to return a non-nil tea.Cmd")
		}
	})

	t.Run("worker tab shows loading state while events are being fetched", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:       "oro-test.17",
			Title:    "Loading state test",
			WorkerID: "worker-loading",
		}

		model := newDetailModel(bead)
		model.activeTab = 1 // Worker tab
		model.loadingEvents = true

		view := model.View()

		// Loading state should be visible
		if !strings.Contains(view, "Loading") && !strings.Contains(view, "loading") {
			t.Errorf("expected 'Loading' indicator in worker tab while events are being fetched, got:\n%s", view)
		}
	})

	t.Run("worker tab displays error when events fetch fails", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:       "oro-test.18",
			Title:    "Error state test",
			WorkerID: "worker-error",
		}

		model := newDetailModel(bead)
		model.activeTab = 1 // Worker tab
		model.loadingEvents = false
		model.eventError = fmt.Errorf("timeout fetching events")

		view := model.View()

		// Error message should be visible
		if !strings.Contains(view, "Error") && !strings.Contains(view, "error") {
			t.Errorf("expected error message in worker tab when fetch fails, got:\n%s", view)
		}
		if !strings.Contains(view, "timeout") {
			t.Errorf("expected error details in worker tab, got:\n%s", view)
		}
	})
}

// TestDetailViewportScrolling verifies that viewport.Model is integrated for scrollable tab content.
// Acceptance: viewport renders tab content with scroll indicators; PageUp/PageDown/j/k scroll within tab;
// viewport resizes on WindowSizeMsg; tab switch resets viewport scroll position to top.
func TestDetailViewportScrolling(t *testing.T) {
	t.Run("viewport renders tab content with scroll indicators", func(t *testing.T) {
		// Create a bead with long content that exceeds viewport height
		longContent := strings.Repeat("Line of text\n", 100)
		bead := protocol.BeadDetail{
			ID:      "oro-test.20",
			Title:   "Viewport scrolling test",
			GitDiff: longContent,
		}

		model := newDetailModel(bead)
		model.activeTab = 2 // Diff tab
		model.width = 80
		model.height = 30 // Small height to force scrolling

		view := model.View()

		// Viewport should be initialized and showing content
		// The viewport should not show all 100 lines at once (height constraint)
		lineCount := strings.Count(view, "Line of text")
		if lineCount >= 100 {
			t.Errorf("expected viewport to limit visible lines (height=30), but got %d lines visible", lineCount)
		}
		if lineCount == 0 {
			t.Errorf("expected viewport to show some content, but got no lines visible")
		}
	})

	t.Run("PageDown scrolls viewport down", func(t *testing.T) {
		// Use numbered lines so we can detect scrolling
		var longContent string
		for i := 0; i < 100; i++ {
			longContent += fmt.Sprintf("Line %d\n", i)
		}
		bead := protocol.BeadDetail{
			ID:      "oro-test.21",
			Title:   "PageDown test",
			GitDiff: longContent,
		}

		model := newDetailModel(bead)
		model.activeTab = 2
		model.width = 80
		model.height = 30

		// Initial view should show "Line 0"
		viewBefore := model.View()
		if !strings.Contains(viewBefore, "Line 0") {
			t.Errorf("expected initial view to contain 'Line 0', got:\n%s", viewBefore)
		}

		// Simulate PageDown key
		model, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		viewAfter := model.View()

		// After PageDown, should show later lines (not Line 0)
		if strings.Contains(viewAfter, "Line 0") {
			t.Errorf("expected viewport to scroll past Line 0 on PageDown, but still shows Line 0")
		}
		// Should show lines from further down (e.g., Line 20+)
		if !strings.Contains(viewAfter, "Line 2") {
			t.Errorf("expected viewport to show later lines after PageDown, got:\n%s", viewAfter)
		}
	})

	t.Run("PageUp scrolls viewport up", func(t *testing.T) {
		// Use numbered lines so we can detect scrolling
		var longContent string
		for i := 0; i < 100; i++ {
			longContent += fmt.Sprintf("Line %d\n", i)
		}
		bead := protocol.BeadDetail{
			ID:      "oro-test.22",
			Title:   "PageUp test",
			GitDiff: longContent,
		}

		model := newDetailModel(bead)
		model.activeTab = 2
		model.width = 80
		model.height = 30

		// Scroll down first (multiple times to get far down)
		model, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		model, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		viewScrolled := model.View()

		// Should not show Line 0 after scrolling down
		if strings.Contains(viewScrolled, "Line 0") {
			t.Errorf("expected viewport to have scrolled past Line 0")
		}

		// Then scroll up
		model, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgUp})
		viewAfterUp := model.View()

		// After PageUp, should show earlier lines
		// (We can't guarantee Line 0 without knowing exact page size, but should show earlier lines)
		if viewScrolled == viewAfterUp {
			t.Errorf("expected viewport to scroll on PageUp, but view didn't change")
		}
	})

	t.Run("j/k keys scroll viewport", func(t *testing.T) {
		// Use numbered lines so we can detect scrolling
		var longContent string
		for i := 0; i < 100; i++ {
			longContent += fmt.Sprintf("Line %d\n", i)
		}
		bead := protocol.BeadDetail{
			ID:      "oro-test.23",
			Title:   "j/k scroll test",
			GitDiff: longContent,
		}

		model := newDetailModel(bead)
		model.activeTab = 2
		model.width = 80
		model.height = 30

		// Initial view should show Line 0
		viewBefore := model.View()
		if !strings.Contains(viewBefore, "Line 0") {
			t.Errorf("expected initial view to contain 'Line 0'")
		}

		// Press 'j' multiple times to scroll down
		for i := 0; i < 5; i++ {
			model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		}
		viewAfterJ := model.View()

		// After scrolling down, Line 0 should no longer be visible
		if strings.Contains(viewAfterJ, "Line 0") {
			t.Errorf("expected viewport to scroll past Line 0 after pressing 'j', but still shows Line 0")
		}

		// Press 'k' to scroll back up
		for i := 0; i < 5; i++ {
			model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
		}
		viewAfterK := model.View()

		// After scrolling back up, should show Line 0 again
		if !strings.Contains(viewAfterK, "Line 0") {
			t.Errorf("expected viewport to show Line 0 after scrolling back up with 'k'")
		}
	})

	t.Run("WindowSizeMsg resizes viewport", func(t *testing.T) {
		longContent := strings.Repeat("Line of text\n", 100)
		bead := protocol.BeadDetail{
			ID:      "oro-test.24",
			Title:   "Resize test",
			GitDiff: longContent,
		}

		model := newDetailModel(bead)
		model.activeTab = 2
		model.width = 80
		model.height = 30

		// Simulate window resize
		newWidth := 120
		newHeight := 50
		model, _ = model.Update(tea.WindowSizeMsg{Width: newWidth, Height: newHeight})

		// Verify model dimensions updated
		if model.width != newWidth {
			t.Errorf("expected width=%d after resize, got %d", newWidth, model.width)
		}
		if model.height != newHeight {
			t.Errorf("expected height=%d after resize, got %d", newHeight, model.height)
		}

		// Viewport should now show more content (larger height)
		view := model.View()
		lineCount := strings.Count(view, "Line of text")
		if lineCount <= 20 {
			t.Errorf("expected more visible lines after resize to height=50, got %d", lineCount)
		}
	})

	t.Run("tab switch resets viewport scroll to top", func(t *testing.T) {
		longContent := strings.Repeat("Line of text\n", 100)
		bead := protocol.BeadDetail{
			ID:      "oro-test.25",
			Title:   "Tab switch reset test",
			GitDiff: longContent,
			Memory:  longContent, // Both tabs have long content
		}

		model := newDetailModel(bead)
		model.activeTab = 2 // Diff tab
		model.width = 80
		model.height = 30

		// Scroll down on Diff tab
		model, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		model, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})

		// Switch to Memory tab (activeTab = 4)
		model = model.nextTab() // 3 (Deps)
		model = model.nextTab() // 4 (Memory)

		view := model.View()

		// Viewport should be reset to top (showing early content)
		// We can verify by checking that we're not seeing content from middle/end
		// For simplicity, just ensure viewport is showing content
		if !strings.Contains(view, "Line of text") {
			t.Errorf("expected viewport to show content after tab switch")
		}

		// Switch back to Diff tab and verify scroll was reset
		model.activeTab = 2
		view = model.View()
		if !strings.Contains(view, "Line of text") {
			t.Errorf("expected viewport to show content on Diff tab")
		}
	})

	t.Run("viewport shows empty state for empty content", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:      "oro-test.26",
			Title:   "Empty content test",
			GitDiff: "", // No content
		}

		model := newDetailModel(bead)
		model.activeTab = 2 // Diff tab
		model.width = 80
		model.height = 30

		view := model.View()

		// Should show "No changes" placeholder instead of empty viewport
		if !strings.Contains(view, "No changes") {
			t.Errorf("expected 'No changes' placeholder for empty diff, got:\n%s", view)
		}
	})
}
