package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"oro/pkg/protocol"
)

// makeTestBeads creates a set of test beads with an epic and child beads.
func makeTestBeads() []protocol.Bead {
	return []protocol.Bead{
		{ID: "epic-1", Title: "Epic One", Type: "epic", Status: "open", Priority: 0},
		{ID: "epic-2", Title: "Epic Two", Type: "epic", Status: "open", Priority: 1},
		{ID: "task-1", Title: "Task One", Type: "task", Status: "open", Priority: 1, Epic: "epic-1"},
		{ID: "task-2", Title: "Task Two", Type: "task", Status: "in_progress", Priority: 2, Epic: "epic-1"},
		{ID: "task-3", Title: "Task Three", Type: "task", Status: "closed", Priority: 3, Epic: "epic-1"},
		{ID: "task-4", Title: "Task Four", Type: "task", Status: "open", Priority: 0, Epic: "epic-2"},
		{ID: "orphan-1", Title: "Orphan Task", Type: "task", Status: "open", Priority: 2},
	}
}

// runeKeyMsg creates a tea.KeyMsg for a single rune key (letters, digits, space).
func runeKeyMsg(key string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
}

// escKeyMsg creates a tea.KeyMsg for the Escape key.
func escKeyMsg() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyEsc}
}

// spaceKeyMsg creates a tea.KeyMsg for the Space key.
func spaceKeyMsg() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeySpace}
}

// TestNewTreeModel_GroupsBeadsUnderEpics verifies that beads are grouped under their epic parents.
func TestNewTreeModel_GroupsBeadsUnderEpics(t *testing.T) {
	beads := makeTestBeads()
	tm := NewTreeModel(beads)

	if len(tm.groups) == 0 {
		t.Fatal("expected non-empty groups")
	}

	// Find epic-1 group
	var epicGroup *treeGroup
	for i := range tm.groups {
		if tm.groups[i].epic.ID == "epic-1" {
			epicGroup = &tm.groups[i]
			break
		}
	}

	if epicGroup == nil {
		t.Fatal("expected to find epic-1 group")
	}

	if len(epicGroup.children) != 3 {
		t.Errorf("expected 3 children under epic-1, got %d", len(epicGroup.children))
	}
}

// TestNewTreeModel_OrphanBeadsGrouped verifies orphan beads (no epic) appear under a synthetic group.
func TestNewTreeModel_OrphanBeadsGrouped(t *testing.T) {
	beads := makeTestBeads()
	tm := NewTreeModel(beads)

	var orphanGroup *treeGroup
	for i := range tm.groups {
		if tm.groups[i].epic.ID == "" {
			orphanGroup = &tm.groups[i]
			break
		}
	}

	if orphanGroup == nil {
		t.Fatal("expected to find orphan group (no epic)")
	}

	if len(orphanGroup.children) != 1 {
		t.Errorf("expected 1 orphan child, got %d", len(orphanGroup.children))
	}
	if orphanGroup.children[0].ID != "orphan-1" {
		t.Errorf("expected orphan-1, got %s", orphanGroup.children[0].ID)
	}
}

// TestNewTreeModel_NoOrphansWhenAllHaveEpics verifies no orphan group is created when all beads have epics.
func TestNewTreeModel_NoOrphansWhenAllHaveEpics(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "epic-1", Title: "Epic One", Type: "epic", Status: "open", Priority: 0},
		{ID: "task-1", Title: "Task One", Type: "task", Status: "open", Priority: 1, Epic: "epic-1"},
	}
	tm := NewTreeModel(beads)

	for _, g := range tm.groups {
		if g.epic.ID == "" {
			t.Error("should not have orphan group when all beads have epics")
		}
	}
}

// TestRenderProgressBar verifies progress bar format and fill ratio.
func TestRenderProgressBar(t *testing.T) {
	tests := []struct {
		name     string
		done     int
		total    int
		wantFill string
		wantText string
	}{
		{
			name:     "0 of 5 done",
			done:     0,
			total:    5,
			wantFill: "░",
			wantText: "0/5",
		},
		{
			name:     "3 of 5 done",
			done:     3,
			total:    5,
			wantFill: "█",
			wantText: "3/5",
		},
		{
			name:     "5 of 5 done",
			done:     5,
			total:    5,
			wantFill: "█",
			wantText: "5/5",
		},
		{
			name:     "1 of 1 done — full bar",
			done:     1,
			total:    1,
			wantFill: "█",
			wantText: "1/1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bar := renderProgressBar(tt.done, tt.total, 8)
			if !strings.Contains(bar, tt.wantFill) {
				t.Errorf("renderProgressBar(%d, %d) = %q, want fill %q", tt.done, tt.total, bar, tt.wantFill)
			}
			if !strings.Contains(bar, tt.wantText) {
				t.Errorf("renderProgressBar(%d, %d) = %q, want text %q", tt.done, tt.total, bar, tt.wantText)
			}
		})
	}
}

// TestRenderProgressBar_ZeroTotal verifies graceful handling of zero total.
func TestRenderProgressBar_ZeroTotal(t *testing.T) {
	bar := renderProgressBar(0, 0, 8)
	// Should not panic and should return something reasonable
	if bar == "" {
		t.Error("renderProgressBar(0, 0) should return non-empty string")
	}
}

// TestTreeModel_ToggleCollapse verifies that Space toggles epic expand/collapse.
func TestTreeModel_ToggleCollapse(t *testing.T) {
	beads := makeTestBeads()
	tm := NewTreeModel(beads)

	if len(tm.groups) == 0 {
		t.Fatal("expected groups")
	}

	// Groups start expanded by default
	if !tm.groups[0].expanded {
		t.Error("expected groups to be expanded by default")
	}

	// Toggle collapse on the first group
	tm = tm.toggleCollapse(0)
	if tm.groups[0].expanded {
		t.Error("expected group to be collapsed after toggle")
	}

	// Toggle again to expand
	tm = tm.toggleCollapse(0)
	if !tm.groups[0].expanded {
		t.Error("expected group to be expanded after second toggle")
	}
}

// TestTreeModel_CursorNavigation verifies j/k navigation across rows.
func TestTreeModel_CursorNavigation(t *testing.T) {
	beads := makeTestBeads()
	tm := NewTreeModel(beads)

	initial := tm.cursor
	tm = tm.moveDown()
	if tm.cursor <= initial {
		t.Errorf("moveDown should increase cursor from %d, got %d", initial, tm.cursor)
	}

	// Move back up
	tm = tm.moveUp()
	if tm.cursor != initial {
		t.Errorf("moveUp should return cursor to %d, got %d", initial, tm.cursor)
	}
}

// TestTreeModel_CursorClamping verifies cursor doesn't go out of bounds.
func TestTreeModel_CursorClamping(t *testing.T) {
	beads := makeTestBeads()
	tm := NewTreeModel(beads)

	// Clamp at 0
	tm.cursor = 0
	tm = tm.moveUp()
	if tm.cursor != 0 {
		t.Errorf("cursor should clamp at 0, got %d", tm.cursor)
	}

	// Move to end, then try to go further
	for i := 0; i < 100; i++ {
		tm = tm.moveDown()
	}
	maxCursor := tm.cursor
	tm = tm.moveDown()
	if tm.cursor != maxCursor {
		t.Errorf("cursor should clamp at max %d, got %d", maxCursor, tm.cursor)
	}
}

// TestTreeModel_View_RendersEpicHeaders verifies that epic titles appear in the view.
func TestTreeModel_View_RendersEpicHeaders(t *testing.T) {
	beads := makeTestBeads()
	tm := NewTreeModel(beads)
	theme := DefaultTheme()
	styles := NewStyles(theme)

	output := tm.View(theme, styles)

	if !strings.Contains(output, "Epic One") {
		t.Error("expected 'Epic One' in tree view output")
	}
	if !strings.Contains(output, "Epic Two") {
		t.Error("expected 'Epic Two' in tree view output")
	}
}

// TestTreeModel_View_RendersChildBeads verifies child bead titles appear when groups are expanded.
func TestTreeModel_View_RendersChildBeads(t *testing.T) {
	beads := makeTestBeads()
	tm := NewTreeModel(beads)
	theme := DefaultTheme()
	styles := NewStyles(theme)

	output := tm.View(theme, styles)

	if !strings.Contains(output, "Task One") {
		t.Error("expected 'Task One' in tree view when expanded")
	}
	if !strings.Contains(output, "Task Two") {
		t.Error("expected 'Task Two' in tree view when expanded")
	}
}

// TestTreeModel_View_CollapsedGroupHidesChildren verifies that collapsed groups hide child bead rows.
func TestTreeModel_View_CollapsedGroupHidesChildren(t *testing.T) {
	beads := makeTestBeads()
	tm := NewTreeModel(beads)

	// Find epic-1 group index and collapse it
	epicIdx := -1
	for i, g := range tm.groups {
		if g.epic.ID == "epic-1" {
			epicIdx = i
			break
		}
	}
	if epicIdx == -1 {
		t.Fatal("could not find epic-1 group")
	}
	tm = tm.toggleCollapse(epicIdx)

	theme := DefaultTheme()
	styles := NewStyles(theme)
	output := tm.View(theme, styles)

	// The epic header should still be visible
	if !strings.Contains(output, "Epic One") {
		t.Error("expected epic header 'Epic One' to be visible even when collapsed")
	}

	// Child beads should be hidden
	if strings.Contains(output, "Task One") {
		t.Error("expected 'Task One' to be hidden when epic-1 is collapsed")
	}
}

// TestTreeModel_View_ProgressBar verifies progress bar appears in epic rows.
func TestTreeModel_View_ProgressBar(t *testing.T) {
	beads := makeTestBeads()
	tm := NewTreeModel(beads)
	theme := DefaultTheme()
	styles := NewStyles(theme)

	output := tm.View(theme, styles)

	// epic-1 has 3 children: task-1 (open), task-2 (in_progress), task-3 (closed)
	// closed = done, so 1/3 complete
	if !strings.Contains(output, "1/3") {
		t.Errorf("expected progress '1/3' for epic-1 in output:\n%s", output)
	}
}

// TestTreeModel_View_EmptyBeads verifies a sensible empty state.
func TestTreeModel_View_EmptyBeads(t *testing.T) {
	tm := NewTreeModel(nil)
	theme := DefaultTheme()
	styles := NewStyles(theme)

	output := tm.View(theme, styles)

	if output == "" {
		t.Error("expected non-empty output for empty tree model")
	}
}

// TestTreeModel_SortByPriority verifies beads within an epic are sorted by priority ascending.
func TestTreeModel_SortByPriority(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "epic-1", Title: "Epic", Type: "epic", Status: "open", Priority: 0},
		{ID: "task-c", Title: "Task C", Type: "task", Status: "open", Priority: 3, Epic: "epic-1"},
		{ID: "task-a", Title: "Task A", Type: "task", Status: "open", Priority: 1, Epic: "epic-1"},
		{ID: "task-b", Title: "Task B", Type: "task", Status: "open", Priority: 2, Epic: "epic-1"},
	}
	tm := NewTreeModel(beads)

	var epicGroup *treeGroup
	for i := range tm.groups {
		if tm.groups[i].epic.ID == "epic-1" {
			epicGroup = &tm.groups[i]
			break
		}
	}
	if epicGroup == nil {
		t.Fatal("expected epic-1 group")
	}

	if len(epicGroup.children) != 3 {
		t.Fatalf("expected 3 children, got %d", len(epicGroup.children))
	}

	if epicGroup.children[0].ID != "task-a" {
		t.Errorf("expected task-a first (priority 1), got %s", epicGroup.children[0].ID)
	}
	if epicGroup.children[1].ID != "task-b" {
		t.Errorf("expected task-b second (priority 2), got %s", epicGroup.children[1].ID)
	}
	if epicGroup.children[2].ID != "task-c" {
		t.Errorf("expected task-c third (priority 3), got %s", epicGroup.children[2].ID)
	}
}

// TestModel_AKeyOpensTreeView verifies that pressing 'a' from BoardView switches to TreeView.
func TestModel_AKeyOpensTreeView(t *testing.T) {
	m := newModel()
	m.activeView = BoardView

	updated, _ := m.Update(runeKeyMsg("a"))
	updatedModel, ok := updated.(Model)
	if !ok {
		t.Fatal("expected Model type from Update")
	}

	if updatedModel.activeView != TreeView {
		t.Errorf("expected TreeView (%d), got %d", TreeView, updatedModel.activeView)
	}
}

// TestModel_EscFromTreeViewReturnsToBoardView verifies esc from TreeView returns to BoardView.
func TestModel_EscFromTreeViewReturnsToBoardView(t *testing.T) {
	m := newModel()
	m.activeView = TreeView

	updated, _ := m.Update(escKeyMsg())
	updatedModel, ok := updated.(Model)
	if !ok {
		t.Fatal("expected Model type from Update")
	}

	if updatedModel.activeView != BoardView {
		t.Errorf("expected BoardView (%d), got %d", BoardView, updatedModel.activeView)
	}
}

// TestModel_TreeViewJKNavigation verifies j/k keys navigate within TreeView.
func TestModel_TreeViewJKNavigation(t *testing.T) {
	m := newModel()
	m.activeView = TreeView
	m.beads = makeTestBeads()
	m.treeModel = NewTreeModel(m.beads)

	initialCursor := m.treeModel.cursor

	updated, _ := m.Update(runeKeyMsg("j"))
	updatedModel, ok := updated.(Model)
	if !ok {
		t.Fatal("expected Model type")
	}

	if updatedModel.treeModel.cursor <= initialCursor {
		t.Errorf("j key should move cursor down from %d, got %d", initialCursor, updatedModel.treeModel.cursor)
	}
}

// TestModel_TreeViewSpaceTogglesCollapse verifies Space collapses/expands in TreeView.
func TestModel_TreeViewSpaceTogglesCollapse(t *testing.T) {
	m := newModel()
	m.activeView = TreeView
	m.beads = makeTestBeads()
	m.treeModel = NewTreeModel(m.beads)

	// cursor is at 0, first group should be an epic
	if len(m.treeModel.groups) == 0 {
		t.Fatal("expected groups")
	}
	initialExpanded := m.treeModel.groups[0].expanded

	updated, _ := m.Update(spaceKeyMsg())
	updatedModel, ok := updated.(Model)
	if !ok {
		t.Fatal("expected Model type")
	}

	if updatedModel.treeModel.groups[0].expanded == initialExpanded {
		t.Error("Space should toggle expand/collapse of epic group at cursor")
	}
}

// TestHelpHints_TreeView verifies tree view help hints are registered.
func TestHelpHints_TreeView(t *testing.T) {
	hints := helpHintsForView(TreeView, 120)
	if hints == "" {
		t.Error("expected non-empty help hints for TreeView")
	}
}
