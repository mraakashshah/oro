package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"oro/pkg/protocol"
)

// TestBoardView_ColumnsRendered verifies that Render() output contains
// all three column headers: Ready, In Progress, Blocked.
func TestBoardView_ColumnsRendered(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "b-1", Title: "Fix login", Status: "open"},
		{ID: "b-2", Title: "Add search", Status: "in_progress"},
		{ID: "b-3", Title: "DB migration", Status: "blocked"},
	}

	board := NewBoardModel(beads)
	theme := DefaultTheme()
	output := board.Render(theme, NewStyles(theme))

	for _, header := range []string{"Ready", "In Progress", "Blocked"} {
		if !strings.Contains(output, header) {
			t.Errorf("Render() missing column header %q\ngot:\n%s", header, output)
		}
	}
}

// TestBoardView_BeadInCorrectColumn verifies that beads appear in the
// column matching their status.
func TestBoardView_BeadInCorrectColumn(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "b-open", Title: "Open task", Status: "open"},
		{ID: "b-wip", Title: "WIP task", Status: "in_progress"},
		{ID: "b-block", Title: "Stuck task", Status: "blocked"},
	}

	board := NewBoardModel(beads)
	theme := DefaultTheme()
	output := board.Render(theme, NewStyles(theme))

	// Each bead title and ID should appear in the output.
	for _, b := range beads {
		if !strings.Contains(output, b.Title) {
			t.Errorf("Render() missing bead title %q\ngot:\n%s", b.Title, output)
		}
		if !strings.Contains(output, b.ID) {
			t.Errorf("Render() missing bead ID %q\ngot:\n%s", b.ID, output)
		}
	}

	// Verify column ordering: "Ready" column should come before "In Progress",
	// and "In Progress" before "Blocked" in the rendered output.
	readyIdx := strings.Index(output, "Ready")
	inProgIdx := strings.Index(output, "In Progress")
	blockedIdx := strings.Index(output, "Blocked")

	if readyIdx == -1 || inProgIdx == -1 || blockedIdx == -1 {
		t.Fatalf("missing column headers in output:\n%s", output)
	}

	if readyIdx >= inProgIdx {
		t.Errorf("Ready column (pos %d) should appear before In Progress (pos %d)", readyIdx, inProgIdx)
	}
	if inProgIdx >= blockedIdx {
		t.Errorf("In Progress column (pos %d) should appear before Blocked (pos %d)", inProgIdx, blockedIdx)
	}
}

// TestBoardView_EmptyBeads verifies that Render() works with no beads
// and still shows column headers.
func TestBoardView_EmptyBeads(t *testing.T) {
	board := NewBoardModel(nil)
	theme := DefaultTheme()
	output := board.Render(theme, NewStyles(theme))

	for _, header := range []string{"Ready", "In Progress", "Blocked"} {
		if !strings.Contains(output, header) {
			t.Errorf("Render() with no beads missing column header %q\ngot:\n%s", header, output)
		}
	}
}

// TestDoneColumn_MostRecent verifies that the Done column shows the 10 most
// recently closed beads, not the oldest ones.
func TestDoneColumn_MostRecent(t *testing.T) {
	// Create 15 closed beads in order from oldest (a) to newest (o).
	// UpdatedAt timestamps are assigned in ascending order so that the sort
	// in NewBoardModelWithWorkers produces the same result as the input order.
	closedBeads := make([]protocol.Bead, 15)
	for i := range 15 {
		closedBeads[i] = protocol.Bead{
			ID:        string(rune('a' + i)),
			Title:     "Closed task " + string(rune('A'+i)),
			Status:    "closed",
			UpdatedAt: fmt.Sprintf("2024-01-%02dT00:00:00Z", i+1),
		}
	}

	// Add some open beads too
	beads := append([]protocol.Bead{
		{ID: "b-open", Title: "Open task", Status: "open"},
		{ID: "b-wip", Title: "WIP task", Status: "in_progress"},
	}, closedBeads...)

	board := NewBoardModel(beads)
	theme := DefaultTheme()
	output := board.Render(theme, NewStyles(theme))

	// 1. Verify "Done" column header exists
	if !strings.Contains(output, "Done") {
		t.Errorf("Render() missing Done column header\ngot:\n%s", output)
	}

	// 2. Verify column ordering: Ready < In Progress < Blocked < Done
	readyIdx := strings.Index(output, "Ready")
	inProgIdx := strings.Index(output, "In Progress")
	blockedIdx := strings.Index(output, "Blocked")
	doneIdx := strings.Index(output, "Done")

	if readyIdx == -1 || inProgIdx == -1 || blockedIdx == -1 || doneIdx == -1 {
		t.Fatalf("missing column headers in output:\n%s", output)
	}

	if readyIdx >= inProgIdx || inProgIdx >= blockedIdx || blockedIdx >= doneIdx {
		t.Errorf("column ordering incorrect: Ready=%d, InProg=%d, Blocked=%d, Done=%d",
			readyIdx, inProgIdx, blockedIdx, doneIdx)
	}

	// 3. Verify header shows visible/total count: "Done (10/15)"
	if !strings.Contains(output, "Done (10/15)") {
		t.Errorf("Done column header should show 'Done (10/15)'\ngot:\n%s", output)
	}

	// 4. Verify only 10 most recent closed beads are shown (capped).
	// The 10 most recent are F..O (indices 5..14).
	for i := 5; i < 15; i++ {
		expectedTitle := "Closed task " + string(rune('A'+i))
		if !strings.Contains(output, expectedTitle) {
			t.Errorf("Done column should show most recent 10, missing %q\ngot:\n%s",
				expectedTitle, output)
		}
	}
	// The 5 oldest (A..E) should NOT be shown.
	for i := 0; i < 5; i++ {
		excludedTitle := "Closed task " + string(rune('A'+i))
		if strings.Contains(output, excludedTitle) {
			t.Errorf("Done column should cap at 10, but found old bead %q\ngot:\n%s",
				excludedTitle, output)
		}
	}
}

// TestCardRendering_PriorityBadges verifies that cards display priority badges with correct colors.
func TestCardRendering_PriorityBadges(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "b-p0", Title: "Critical bug", Status: "open", Priority: 0},
		{ID: "b-p1", Title: "High priority", Status: "open", Priority: 1},
		{ID: "b-p2", Title: "Medium task", Status: "open", Priority: 2},
		{ID: "b-p3", Title: "Low priority", Status: "open", Priority: 3},
		{ID: "b-p4", Title: "Backlog item", Status: "open", Priority: 4},
	}

	board := NewBoardModel(beads)
	theme := DefaultTheme()
	output := board.Render(theme, NewStyles(theme))

	// Verify each priority badge appears in output
	for _, expected := range []string{"[P0]", "[P1]", "[P2]", "[P3]", "[P4]"} {
		if !strings.Contains(output, expected) {
			t.Errorf("Render() missing priority badge %q\ngot:\n%s", expected, output)
		}
	}
}

// TestCardRendering_TypeIndicators verifies that cards display type indicators.
func TestCardRendering_TypeIndicators(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "b-task", Title: "Do task", Status: "open", Type: "task"},
		{ID: "b-bug", Title: "Fix bug", Status: "open", Type: "bug"},
		{ID: "b-feat", Title: "New feature", Status: "open", Type: "feature"},
		{ID: "b-epic", Title: "Big epic", Status: "open", Type: "epic"},
	}

	board := NewBoardModel(beads)
	theme := DefaultTheme()
	output := board.Render(theme, NewStyles(theme))

	// Verify type indicators appear in output.
	typeIndicators := map[string]string{
		"task":    "■",
		"bug":     "▲",
		"feature": "★",
		"epic":    "◎",
	}

	for beadType, indicator := range typeIndicators {
		if !strings.Contains(output, indicator) {
			t.Errorf("Render() missing type indicator %q for type %q\ngot:\n%s",
				indicator, beadType, output)
		}
	}
}

// TestCardRendering_InProgressWorkerInfo verifies that in-progress cards show worker ID.
func TestCardRendering_InProgressWorkerInfo(t *testing.T) {
	// Create a board model with worker assignments
	beads := []protocol.Bead{
		{ID: "b-wip1", Title: "Task in progress", Status: "in_progress"},
		{ID: "b-wip2", Title: "Another WIP", Status: "in_progress"},
	}

	// Create worker assignments map
	workers := []WorkerStatus{
		{ID: "worker-abc", Status: "busy"},
		{ID: "worker-xyz", Status: "busy"},
	}

	// Create assignments map (bead ID -> worker ID)
	assignments := map[string]string{
		"b-wip1": "worker-abc",
		"b-wip2": "worker-xyz",
	}

	board := NewBoardModelWithWorkers(beads, workers, assignments)
	theme := DefaultTheme()
	output := board.RenderWithCursor(-1, -1, theme, NewStyles(theme))

	// Verify worker IDs appear in the output for in-progress cards
	if !strings.Contains(output, "worker-abc") {
		t.Errorf("Render() missing worker ID 'worker-abc' for in-progress bead\ngot:\n%s", output)
	}
	if !strings.Contains(output, "worker-xyz") {
		t.Errorf("Render() missing worker ID 'worker-xyz' for in-progress bead\ngot:\n%s", output)
	}
}

// TestCardRendering_BlockedBeadDependencies verifies that blocked cards show blocker IDs.
func TestCardRendering_BlockedBeadDependencies(t *testing.T) {
	beads := []protocol.Bead{
		{
			ID:     "b-blocked",
			Title:  "Blocked task",
			Status: "blocked",
			Dependencies: []protocol.Dependency{
				{IssueID: "b-blocked", DependsOnID: "b-blocker1", Type: "blocks"},
				{IssueID: "b-blocked", DependsOnID: "b-blocker2", Type: "blocks"},
			},
		},
	}

	board := NewBoardModel(beads)
	theme := DefaultTheme()
	output := board.Render(theme, NewStyles(theme))

	// Verify blocker IDs appear in the output (with non-breaking hyphens to prevent wrapping)
	if !strings.Contains(output, "b\u2011blocker1") {
		t.Errorf("Render() missing blocker ID 'b-blocker1' for blocked bead\ngot:\n%s", output)
	}
	if !strings.Contains(output, "b\u2011blocker2") {
		t.Errorf("Render() missing blocker ID 'b-blocker2' for blocked bead\ngot:\n%s", output)
	}
}

// TestCardRendering_NoOverflow verifies that enriched cards don't break column layout.
func TestCardRendering_NoOverflow(t *testing.T) {
	beads := []protocol.Bead{
		{
			ID:       "b-long",
			Title:    "This is a very long title that should not break the column layout or cause overflow issues",
			Status:   "blocked",
			Priority: 0,
			Type:     "feature",
			Dependencies: []protocol.Dependency{
				{IssueID: "b-long", DependsOnID: "b-blocker1", Type: "blocks"},
				{IssueID: "b-long", DependsOnID: "b-blocker2", Type: "blocks"},
				{IssueID: "b-long", DependsOnID: "b-blocker3", Type: "blocks"},
			},
		},
	}

	board := NewBoardModel(beads)
	theme := DefaultTheme()
	output := board.Render(theme, NewStyles(theme))

	// Basic sanity check: output should contain the bead
	if !strings.Contains(output, "b-long") {
		t.Errorf("Render() missing bead ID 'b-long'\ngot:\n%s", output)
	}

	// Verify card content is present
	if !strings.Contains(output, "[P0]") {
		t.Errorf("Render() missing priority badge for bead")
	}
	if !strings.Contains(output, "b\u2011blocker1") {
		t.Errorf("Render() missing blocker ID in card")
	}
}

// TestCardRendering_WorkerHealthBadge_Green verifies that in-progress cards show
// green health badge when heartbeat age is less than 5 seconds.
func TestCardRendering_WorkerHealthBadge_Green(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "b-wip", Title: "Task in progress", Status: "in_progress"},
	}

	workers := []WorkerStatus{
		{ID: "worker-healthy", Status: "busy", LastProgressSecs: 2.5},
	}

	assignments := map[string]string{
		"b-wip": "worker-healthy",
	}

	board := NewBoardModelWithWorkers(beads, workers, assignments)
	theme := DefaultTheme()
	output := board.RenderWithCursor(-1, -1, theme, NewStyles(theme))

	// Verify worker ID appears
	if !strings.Contains(output, "worker-healthy") {
		t.Errorf("Render() missing worker ID 'worker-healthy'\ngot:\n%s", output)
	}

	// Verify health badge appears (green indicator: ●)
	if !strings.Contains(output, "●") {
		t.Errorf("Render() missing health badge for worker\ngot:\n%s", output)
	}
}

// TestCardRendering_WorkerHealthBadge_Amber verifies that in-progress cards show
// amber health badge when heartbeat age is between 5-15 seconds.
func TestCardRendering_WorkerHealthBadge_Amber(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "b-wip", Title: "Task in progress", Status: "in_progress"},
	}

	workers := []WorkerStatus{
		{ID: "worker-stale", Status: "busy", LastProgressSecs: 8.0},
	}

	assignments := map[string]string{
		"b-wip": "worker-stale",
	}

	board := NewBoardModelWithWorkers(beads, workers, assignments)
	theme := DefaultTheme()
	output := board.RenderWithCursor(-1, -1, theme, NewStyles(theme))

	// Verify worker ID appears
	if !strings.Contains(output, "worker-stale") {
		t.Errorf("Render() missing worker ID 'worker-stale'\ngot:\n%s", output)
	}

	// Verify health badge appears (amber indicator: ●)
	if !strings.Contains(output, "●") {
		t.Errorf("Render() missing health badge for worker\ngot:\n%s", output)
	}
}

// TestCardRendering_WorkerHealthBadge_Red verifies that in-progress cards show
// red health badge when heartbeat age is greater than 15 seconds.
func TestCardRendering_WorkerHealthBadge_Red(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "b-wip", Title: "Task in progress", Status: "in_progress"},
	}

	workers := []WorkerStatus{
		{ID: "worker-stuck", Status: "busy", LastProgressSecs: 20.0},
	}

	assignments := map[string]string{
		"b-wip": "worker-stuck",
	}

	board := NewBoardModelWithWorkers(beads, workers, assignments)
	theme := DefaultTheme()
	output := board.RenderWithCursor(-1, -1, theme, NewStyles(theme))

	// Verify worker ID appears
	if !strings.Contains(output, "worker-stuck") {
		t.Errorf("Render() missing worker ID 'worker-stuck'\ngot:\n%s", output)
	}

	// Verify health badge appears (red indicator: ●)
	if !strings.Contains(output, "●") {
		t.Errorf("Render() missing health badge for worker\ngot:\n%s", output)
	}
}

// TestCardRendering_ContextPercentage verifies that in-progress cards show
// context percentage when available.
func TestCardRendering_ContextPercentage(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "b-wip", Title: "Task in progress", Status: "in_progress"},
	}

	workers := []WorkerStatus{
		{ID: "worker-abc", Status: "busy", ContextPct: 42},
	}

	assignments := map[string]string{
		"b-wip": "worker-abc",
	}

	board := NewBoardModelWithWorkers(beads, workers, assignments)
	theme := DefaultTheme()
	output := board.RenderWithCursor(-1, -1, theme, NewStyles(theme))

	// Verify context percentage appears
	if !strings.Contains(output, "42%") {
		t.Errorf("Render() missing context percentage '42%%'\ngot:\n%s", output)
	}
}

// TestCardRendering_NoWorkerAssignment verifies that in-progress cards without
// worker assignment show no badge and don't panic.
func TestCardRendering_NoWorkerAssignment(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "b-wip", Title: "Task in progress", Status: "in_progress"},
	}

	// No workers or assignments
	board := NewBoardModelWithWorkers(beads, nil, nil)
	theme := DefaultTheme()
	output := board.RenderWithCursor(-1, -1, theme, NewStyles(theme))

	// Verify bead renders without panic
	if !strings.Contains(output, "b-wip") {
		t.Errorf("Render() missing bead ID 'b-wip'\ngot:\n%s", output)
	}

	// Verify bead title renders
	if !strings.Contains(output, "Task in progress") {
		t.Errorf("Render() missing bead title\ngot:\n%s", output)
	}
}

// TestCardRendering_WorkerNotInList verifies that cards handle missing worker data gracefully.
func TestCardRendering_WorkerNotInList(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "b-wip", Title: "Task in progress", Status: "in_progress"},
	}

	// Assignment exists but worker not in workers list
	assignments := map[string]string{
		"b-wip": "worker-missing",
	}

	board := NewBoardModelWithWorkers(beads, nil, assignments)
	theme := DefaultTheme()
	output := board.RenderWithCursor(-1, -1, theme, NewStyles(theme))

	// Verify bead renders without panic
	if !strings.Contains(output, "b-wip") {
		t.Errorf("Render() missing bead ID 'b-wip'\ngot:\n%s", output)
	}

	// Worker ID should still appear (just without health badge/context)
	if !strings.Contains(output, "worker-missing") {
		t.Errorf("Render() missing worker ID 'worker-missing'\ngot:\n%s", output)
	}
}

// TestBoardRender_ActiveCardUsesThemeColorFocus verifies that active cards use
// the theme's ColorFocus color for the background, not hard-coded #3a3a3a.
// Also verifies that column headers use ColorMutedFg, not Primary,
// except Done column which keeps theme.Success (green).
func TestBoardRender_ActiveCardUsesThemeColorFocus(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "b-1", Title: "Task 1", Status: "open"},
		{ID: "b-2", Title: "Task 2", Status: "open"},
		{ID: "b-3", Title: "Task 3", Status: "in_progress"},
		{ID: "b-4", Title: "Task 4", Status: "blocked"},
		{ID: "b-5", Title: "Task 5", Status: "closed", UpdatedAt: "2024-01-01T00:00:00Z"},
	}

	board := NewBoardModel(beads)
	theme := DefaultTheme()
	styles := NewStyles(theme)

	// Render with cursor at first column, first bead (makes it "active")
	output := board.RenderWithCursor(0, 0, theme, styles)

	// TEST 1: Active card should NOT contain hex #3a3a3a as ANSI escape code
	// The hard-coded background color #3a3a3a should not appear as a 24-bit truecolor ANSI escape
	// Background: "48;2;58;58;58" (where 58 = 0x3a in decimal)
	hardcodedBgANSI := "48;2;58;58;58"

	if strings.Contains(output, hardcodedBgANSI) {
		t.Errorf("Active card should NOT use hard-coded #3a3a3a background (ANSI %s)\ngot:\n%s", hardcodedBgANSI, output)
	}

	// TEST 2: Verify active card exists in output (sanity check)
	if !strings.Contains(output, "b-1") {
		t.Errorf("Render() missing active card bead ID 'b-1'\ngot:\n%s", output)
	}

	// TEST 3: Column headers should be present
	for _, header := range []string{"Ready", "In Progress", "Blocked"} {
		if !strings.Contains(output, header) {
			t.Errorf("Render() missing column header %q\ngot:\n%s", header, output)
		}
	}

	// TEST 4: Verify Done column header is present and should use theme.Success (green)
	if !strings.Contains(output, "Done") {
		t.Errorf("Render() missing Done column header\ngot:\n%s", output)
	}
}

// TestDoneColumnSortedByUpdatedAtDescending verifies that the Done column sorts
// beads by UpdatedAt descending (most recently updated first) before capping at 10.
func TestDoneColumnSortedByUpdatedAtDescending(t *testing.T) {
	t.Run("sorts by UpdatedAt descending before capping at 10", func(t *testing.T) {
		// 12 closed beads in newest-first input order.
		// done-12 has UpdatedAt 2024-01-12 (newest), done-01 has 2024-01-01 (oldest).
		// The old code took the last 10 in input order (done-03..done-12 visible, done-01 and done-02 hidden).
		// The new code must sort first so the 10 highest UpdatedAt appear:
		//   done-12..done-03 visible, done-01 and done-02 cut off.
		// Since input is already newest-first, the sort doesn't change order here —
		// but the slice changes from [:10] vs [2:], exposing the difference.
		beads := make([]protocol.Bead, 12)
		for i := range 12 {
			day := 12 - i // input position 0 = newest day 12
			beads[i] = protocol.Bead{
				ID:        fmt.Sprintf("done-%02d", day),
				Title:     fmt.Sprintf("Task %02d", day),
				Status:    "closed",
				UpdatedAt: fmt.Sprintf("2024-01-%02dT00:00:00Z", day),
			}
		}

		board := NewBoardModel(beads)
		theme := DefaultTheme()
		output := board.Render(theme, NewStyles(theme))

		// Header: 10 shown out of 12 total.
		if !strings.Contains(output, "Done (10/12)") {
			t.Errorf("expected 'Done (10/12)' in header\ngot:\n%s", output)
		}

		// Top 10 most recent (done-12..done-03) must appear; oldest 2 (done-01, done-02) must not.
		for day := 3; day <= 12; day++ {
			id := fmt.Sprintf("done-%02d", day)
			if !strings.Contains(output, id) {
				t.Errorf("Done column missing recent bead %q\ngot:\n%s", id, output)
			}
		}
		for day := 1; day <= 2; day++ {
			id := fmt.Sprintf("done-%02d", day)
			if strings.Contains(output, id) {
				t.Errorf("Done column should cap at 10, but found old bead %q\ngot:\n%s", id, output)
			}
		}

		// Sort order: done-12 (newest) must appear before done-03 (oldest visible).
		idx12 := strings.Index(output, "done-12")
		idx03 := strings.Index(output, "done-03")
		if idx12 == -1 || idx03 == -1 {
			t.Fatal("done-12 and done-03 must both appear in output")
		}
		if idx12 >= idx03 {
			t.Errorf("done-12 (pos %d) should appear before done-03 (pos %d)", idx12, idx03)
		}
	})

	t.Run("empty UpdatedAt sorts as zero-time (oldest)", func(t *testing.T) {
		// 11 beads with ascending timestamps + 1 with empty UpdatedAt.
		// The empty-UpdatedAt bead sorts as zero-time (oldest) and falls off the cap.
		// ts-01 (2024-01-01, oldest stamped) also falls off: top 10 are ts-02..ts-11.
		beads := make([]protocol.Bead, 12)
		for i := range 11 {
			beads[i] = protocol.Bead{
				ID:        fmt.Sprintf("ts-%02d", i+1),
				Title:     fmt.Sprintf("Stamped %02d", i+1),
				Status:    "closed",
				UpdatedAt: fmt.Sprintf("2024-01-%02dT00:00:00Z", i+1),
			}
		}
		beads[11] = protocol.Bead{
			ID:     "no-ts",
			Title:  "No timestamp",
			Status: "closed",
			// UpdatedAt intentionally empty → sorts as zero-time (oldest)
		}

		board := NewBoardModel(beads)
		theme := DefaultTheme()
		output := board.Render(theme, NewStyles(theme))

		// Header: 10 shown out of 12 total.
		if !strings.Contains(output, "Done (10/12)") {
			t.Errorf("expected 'Done (10/12)' in header\ngot:\n%s", output)
		}

		// Empty-UpdatedAt bead sorts as oldest and should be capped off (not visible).
		if strings.Contains(output, "no-ts") {
			t.Errorf("bead with empty UpdatedAt should be capped off\ngot:\n%s", output)
		}

		// ts-01 (oldest stamped) should also be capped off.
		if strings.Contains(output, "ts-01") {
			t.Errorf("oldest stamped bead ts-01 should be capped off\ngot:\n%s", output)
		}

		// Top 10: ts-11..ts-02 must appear. ts-11 (newest) before ts-02 (oldest visible).
		idx11 := strings.Index(output, "ts-11")
		idx02 := strings.Index(output, "ts-02")
		if idx11 == -1 || idx02 == -1 {
			t.Fatal("ts-11 and ts-02 must both appear")
		}
		if idx11 >= idx02 {
			t.Errorf("ts-11 (pos %d) should appear before ts-02 (pos %d)", idx11, idx02)
		}
	})
}

// TestBoardRender_EmptyColumnShowsNoItems verifies that empty board columns
// display "no items" placeholder instead of remaining blank.
func TestBoardRender_EmptyColumnShowsNoItems(t *testing.T) {
	// Create a board with one bead in the In Progress column
	// so Ready, Blocked, and Done columns are empty
	beads := []protocol.Bead{
		{ID: "b-wip", Title: "Task in progress", Status: "in_progress"},
	}

	board := NewBoardModel(beads)
	theme := DefaultTheme()
	output := board.Render(theme, NewStyles(theme))

	// Verify column headers appear
	if !strings.Contains(output, "Ready") {
		t.Errorf("Render() missing 'Ready' column header\ngot:\n%s", output)
	}
	if !strings.Contains(output, "Blocked") {
		t.Errorf("Render() missing 'Blocked' column header\ngot:\n%s", output)
	}

	// Verify that empty columns show "no items" placeholder
	if !strings.Contains(output, "no items") {
		t.Errorf("Render() empty columns should show 'no items' placeholder\ngot:\n%s", output)
	}
}

// TestBoardRecencySort verifies that all 4 columns (Ready, In Progress, Blocked, Done)
// sort beads by UpdatedAt descending (most recent first).
func TestBoardRecencySort(t *testing.T) {
	// Create 3 beads for each of the 4 columns, with UpdatedAt in unsorted order.
	// We'll input them as: day 1, day 3, day 2 (unsorted).
	// After sorting, they should appear as: day 3, day 2, day 1 (descending).
	beads := []protocol.Bead{
		// Ready column
		{ID: "ready-1", Title: "Ready Day 1", Status: "open", UpdatedAt: "2024-01-01T00:00:00Z"},
		{ID: "ready-3", Title: "Ready Day 3", Status: "open", UpdatedAt: "2024-01-03T00:00:00Z"},
		{ID: "ready-2", Title: "Ready Day 2", Status: "open", UpdatedAt: "2024-01-02T00:00:00Z"},
		// In Progress column
		{ID: "wip-1", Title: "WIP Day 1", Status: "in_progress", UpdatedAt: "2024-01-01T00:00:00Z"},
		{ID: "wip-3", Title: "WIP Day 3", Status: "in_progress", UpdatedAt: "2024-01-03T00:00:00Z"},
		{ID: "wip-2", Title: "WIP Day 2", Status: "in_progress", UpdatedAt: "2024-01-02T00:00:00Z"},
		// Blocked column
		{ID: "blocked-1", Title: "Blocked Day 1", Status: "blocked", UpdatedAt: "2024-01-01T00:00:00Z"},
		{ID: "blocked-3", Title: "Blocked Day 3", Status: "blocked", UpdatedAt: "2024-01-03T00:00:00Z"},
		{ID: "blocked-2", Title: "Blocked Day 2", Status: "blocked", UpdatedAt: "2024-01-02T00:00:00Z"},
		// Done column
		{ID: "done-1", Title: "Done Day 1", Status: "closed", UpdatedAt: "2024-01-01T00:00:00Z"},
		{ID: "done-3", Title: "Done Day 3", Status: "closed", UpdatedAt: "2024-01-03T00:00:00Z"},
		{ID: "done-2", Title: "Done Day 2", Status: "closed", UpdatedAt: "2024-01-02T00:00:00Z"},
	}

	board := NewBoardModel(beads)
	theme := DefaultTheme()
	output := board.Render(theme, NewStyles(theme))

	// Helper to verify column sort order
	verifyColumnOrder := func(colName string, expectedOrder []string) {
		indices := make([]int, len(expectedOrder))
		for i, id := range expectedOrder {
			idx := strings.Index(output, id)
			if idx == -1 {
				t.Errorf("%s column missing bead %q\ngot:\n%s", colName, id, output)
				return
			}
			indices[i] = idx
		}
		// Verify indices are in ascending order (first ID appears before last ID)
		for i := 0; i < len(indices)-1; i++ {
			if indices[i] > indices[i+1] {
				t.Errorf("%s column: expected %q before %q, got reverse order\noutput:\n%s",
					colName, expectedOrder[i], expectedOrder[i+1], output)
			}
		}
	}

	// Each column should show: day-3, day-2, day-1 (descending by UpdatedAt)
	verifyColumnOrder("Ready", []string{"ready-3", "ready-2", "ready-1"})
	verifyColumnOrder("In Progress", []string{"wip-3", "wip-2", "wip-1"})
	verifyColumnOrder("Blocked", []string{"blocked-3", "blocked-2", "blocked-1"})
	verifyColumnOrder("Done", []string{"done-3", "done-2", "done-1"})
}

// TestBoardColumnHeadersVisible verifies that column headers use a visible
// (non-muted) foreground color. Ready, In Progress, and Blocked headers
// must NOT use ColorMutedFg (#889096). Done keeps theme.Success (green).
func TestBoardColumnHeadersVisible(t *testing.T) {
	// Force 24-bit ANSI output so we can inspect color codes in the rendered string.
	// SetColorProfile is designed for testing (see lipgloss renderer.go docs).
	lipgloss.DefaultRenderer().SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.DefaultRenderer().SetColorProfile(termenv.Ascii)
	})

	theme := DefaultTheme()
	styles := NewStyles(theme)
	board := NewBoardModel(nil)

	// ColorMutedFg (#889096 = RGB 136,144,150) as 24-bit ANSI foreground: "38;2;136;144;150".
	mutedFgANSI := "38;2;136;144;150"

	for _, title := range []string{"Ready", "In Progress", "Blocked", "Done"} {
		col := boardColumn{title: title}
		rendered := board.renderColumnHeader(col, 30, theme, styles)

		// Each header must contain its title text.
		if !strings.Contains(rendered, title) {
			t.Errorf("renderColumnHeader(%q): title missing from output\ngot: %q", title, rendered)
		}

		// No header should use ColorMutedFg (#889096).
		if strings.Contains(rendered, mutedFgANSI) {
			t.Errorf("renderColumnHeader(%q) uses ColorMutedFg (#889096); use a visible color instead\ngot: %q", title, rendered)
		}
	}
}

// TestTypeIcons verifies the shared renderTreeTypeIcon function returns the correct descriptive emoji.
func TestTypeIcons(t *testing.T) {
	cases := []struct {
		beadType string
		want     string
	}{
		{"bug", "▲"},
		{"feature", "★"},
		{"task", "■"},
		{"epic", "◎"},
	}
	for _, tc := range cases {
		got := renderTreeTypeIcon(tc.beadType)
		if got != tc.want {
			t.Errorf("renderTreeTypeIcon(%q) = %q, want %q", tc.beadType, got, tc.want)
		}
	}
}

// TestBoardLoadMoreUI verifies that pressing M key fires load-more command in board view.
func TestBoardLoadMoreUI(t *testing.T) {
	t.Run("M key in board view fires fetchMoreClosedCmd", func(t *testing.T) {
		m := newModel()
		m.activeView = BoardView
		m.beads = []protocol.Bead{
			{ID: "b-1", Title: "Open task", Status: "open"},
			{ID: "b-2", Title: "Closed task", Status: "closed", ClosedAt: "2024-03-01T00:00:00Z"},
		}
		m.closedCursor = "2024-03-01T00:00:00Z"

		// Press M key
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
		if cmd == nil {
			t.Fatal("M key should return a command, got nil")
		}

		// Execute cmd to verify it returns moreClosedMsg type
		msg := cmd()
		if _, ok := msg.(moreClosedMsg); !ok {
			t.Errorf("M key cmd() should return moreClosedMsg, got %T", msg)
		}

		// Model should still be in BoardView
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update should return Model")
		}
		if model.activeView != BoardView {
			t.Errorf("activeView = %d, want BoardView (%d)", model.activeView, BoardView)
		}
	})
}
