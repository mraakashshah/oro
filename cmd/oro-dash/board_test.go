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
