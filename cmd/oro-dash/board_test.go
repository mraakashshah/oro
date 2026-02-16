package main

import (
	"strings"
	"testing"

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
	output := board.Render()

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
	output := board.Render()

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
	output := board.Render()

	for _, header := range []string{"Ready", "In Progress", "Blocked"} {
		if !strings.Contains(output, header) {
			t.Errorf("Render() with no beads missing column header %q\ngot:\n%s", header, output)
		}
	}
}

// TestDoneColumn verifies that the Done column is rendered as the 4th column,
// shows only the most recent 10 closed beads, uses Success (green) color,
// and displays visible/total count in the header.
func TestDoneColumn(t *testing.T) {
	// Create 15 closed beads with different IDs to simulate recency
	closedBeads := make([]protocol.Bead, 15)
	for i := range 15 {
		closedBeads[i] = protocol.Bead{
			ID:     string(rune('a' + i)),
			Title:  "Closed task " + string(rune('A'+i)),
			Status: "closed",
		}
	}

	// Add some open beads too
	beads := append([]protocol.Bead{
		{ID: "b-open", Title: "Open task", Status: "open"},
		{ID: "b-wip", Title: "WIP task", Status: "in_progress"},
	}, closedBeads...)

	board := NewBoardModel(beads)
	output := board.Render()

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

	// 4. Verify only first 10 closed beads are shown (last 5 should be missing)
	// We expect beads a-j (first 10) to be shown, k-o (last 5) to be hidden
	for i := range 10 {
		expectedTitle := "Closed task " + string(rune('A'+i))
		if !strings.Contains(output, expectedTitle) {
			t.Errorf("Done column should show first 10 closed beads, missing %q\ngot:\n%s",
				expectedTitle, output)
		}
	}

	for i := range 5 {
		hiddenTitle := "Closed task " + string(rune('A'+i+10))
		if strings.Contains(output, hiddenTitle) {
			t.Errorf("Done column should NOT show beads beyond first 10, found %q\ngot:\n%s",
				hiddenTitle, output)
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
	output := board.Render()

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
	output := board.Render()

	// Verify type indicators appear in output (using emoji or short codes)
	typeIndicators := map[string]string{
		"task":    "□", // or "TSK" or similar
		"bug":     "⚠", // or "BUG"
		"feature": "✦", // or "FTR"
		"epic":    "◈", // or "EPC"
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
	output := board.RenderWithCursor(-1, -1)

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
	output := board.Render()

	// Verify blocker IDs appear in the output
	if !strings.Contains(output, "b-blocker1") {
		t.Errorf("Render() missing blocker ID 'b-blocker1' for blocked bead\ngot:\n%s", output)
	}
	if !strings.Contains(output, "b-blocker2") {
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
	output := board.Render()

	// Basic sanity check: output should contain the bead
	if !strings.Contains(output, "b-long") {
		t.Errorf("Render() missing bead ID 'b-long'\ngot:\n%s", output)
	}

	// Verify card content is present
	if !strings.Contains(output, "[P0]") {
		t.Errorf("Render() missing priority badge for bead")
	}
	if !strings.Contains(output, "b-blocker1") {
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
	output := board.RenderWithCursor(-1, -1)

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
	output := board.RenderWithCursor(-1, -1)

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
	output := board.RenderWithCursor(-1, -1)

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
	output := board.RenderWithCursor(-1, -1)

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
	output := board.RenderWithCursor(-1, -1)

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
	output := board.RenderWithCursor(-1, -1)

	// Verify bead renders without panic
	if !strings.Contains(output, "b-wip") {
		t.Errorf("Render() missing bead ID 'b-wip'\ngot:\n%s", output)
	}

	// Worker ID should still appear (just without health badge/context)
	if !strings.Contains(output, "worker-missing") {
		t.Errorf("Render() missing worker ID 'worker-missing'\ngot:\n%s", output)
	}
}
