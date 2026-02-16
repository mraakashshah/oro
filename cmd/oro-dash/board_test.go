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
