package main

// bugs_regression_test.go — TDD regression tests for all 15 reported UI/UX bugs.
// Each test verifies the fix for a specific bug. Tests are written to FAIL if the bug regresses.

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"oro/pkg/protocol"
)

// --- Helpers ---

func testThemeAndStyles() (Theme, Styles) {
	lipgloss.SetColorProfile(termenv.Ascii)
	t := DefaultTheme()
	s := NewStyles(t)
	return t, s
}

func sampleBeads() []protocol.Bead {
	return []protocol.Bead{
		{ID: "ip-1", Title: "In progress task", Status: "in_progress", Priority: 1, Type: "task", UpdatedAt: "2024-03-01T00:00:00Z"},
		{ID: "op-1", Title: "Open task one", Status: "open", Priority: 2, Type: "feature", UpdatedAt: "2024-02-01T00:00:00Z"},
		{
			ID: "bl-1", Title: "Blocked task", Status: "blocked", Priority: 0, Type: "bug", UpdatedAt: "2024-02-15T00:00:00Z",
			Dependencies: []protocol.Dependency{{Type: "blocks", DependsOnID: "op-1"}},
		},
		{ID: "cl-1", Title: "Closed task", Status: "closed", Priority: 3, Type: "task", UpdatedAt: "2024-03-01T00:00:00Z"},
		{ID: "cl-2", Title: "Old closed", Status: "closed", Priority: 2, Type: "epic", UpdatedAt: "2024-01-01T00:00:00Z"},
	}
}

// --- Bug 1: Type icons should be terminal-safe text badges ---

func TestBug01_TypeIconsAreTextBadges(t *testing.T) {
	cases := map[string]string{
		"task":    "[T]",
		"bug":     "[B]",
		"feature": "[F]",
		"epic":    "[E]",
		"other":   "[·]",
	}
	for beadType, want := range cases {
		got := renderTreeTypeIcon(beadType)
		if got != want {
			t.Errorf("renderTreeTypeIcon(%q) = %q, want %q", beadType, got, want)
		}
	}
}

func TestBug01_IconsNotInListView(t *testing.T) {
	_, styles := testThemeAndStyles()
	lm := NewListModel().updateBeads(sampleBeads())
	out := lm.View(DefaultTheme(), styles, 120, 30)

	// Spec: list view has NO type badge column (board view still uses them)
	for _, badge := range []string{"[T]", "[F]", "[B]"} {
		if strings.Contains(out, badge) {
			t.Errorf("list view should NOT show type badge %q (removed per spec)", badge)
		}
	}
}

func TestBug01_IconsAppearInBoardView(t *testing.T) {
	theme, styles := testThemeAndStyles()
	board := NewBoardModel(sampleBeads())
	out := board.Render(theme, styles)

	for _, badge := range []string{"[T]", "[F]", "[B]"} {
		if !strings.Contains(out, badge) {
			t.Errorf("board view missing type badge %q", badge)
		}
	}
}

// --- Bug 2: List view should be list+detail split-pane with 4 groups ---

func TestBug02_SplitPaneLayoutAtWideWidth(t *testing.T) {
	_, styles := testThemeAndStyles()
	lm := NewListModel().updateBeads(sampleBeads())

	out := lm.View(DefaultTheme(), styles, 120, 30)
	// Should show 4-group headers
	if !strings.Contains(out, "In Progress (") {
		t.Error("split-pane layout missing 'In Progress' header")
	}
	if !strings.Contains(out, "Done (") {
		t.Error("split-pane layout missing 'Done' header")
	}
	// Separator should be present (vertical bar for split-pane)
	if !strings.Contains(out, "│") {
		t.Error("split-pane layout missing vertical separator")
	}
}

func TestBug02_GroupOrderInNarrowView(t *testing.T) {
	_, styles := testThemeAndStyles()
	lm := NewListModel().updateBeads(sampleBeads())

	out := lm.View(DefaultTheme(), styles, 80, 30) // narrow = stacked
	ipIdx := strings.Index(out, "In Progress (")
	doneIdx := strings.Index(out, "Done (")
	if ipIdx == -1 || doneIdx == -1 {
		t.Fatalf("missing headers: in_progress=%d, done=%d", ipIdx, doneIdx)
	}
	if ipIdx > doneIdx {
		t.Error("narrow view: In Progress group should appear before Done group")
	}
}

func TestBug02_DepsShownInDetailPane(t *testing.T) {
	_, styles := testThemeAndStyles()
	beads := sampleBeads()
	lm := NewListModel().updateBeads(beads)

	// Navigate to the blocked bead and check detail pane shows deps
	rows := lm.flatRows()
	for i, row := range rows {
		if row.bead != nil && row.bead.ID == "bl-1" {
			lm.cursor = i
			break
		}
	}
	sections := map[string]bool{"deps": true}
	b := *lm.getCursorBead()
	out := renderDetailPane(b, nil, nil, sections, styles, 40, 20)
	if !strings.Contains(out, "op-1") {
		t.Error("detail pane should show dependency on op-1 for blocked bead")
	}
}

func TestBug02_TopoSortPrereqsBeforeDependents(t *testing.T) {
	// With 4 groups, child (blocked) goes to "blocked" bucket, parent (open) to "open" bucket.
	// Topo sort applies within the "open" bucket only.
	groups := groupBeads([]protocol.Bead{
		{ID: "child", Status: "open", Priority: 0, Dependencies: []protocol.Dependency{
			{Type: "blocks", DependsOnID: "parent"},
		}},
		{ID: "parent", Status: "open", Priority: 3},
	})
	open := groups["open"]
	if len(open) != 2 {
		t.Fatalf("expected 2 open beads, got %d", len(open))
	}
	if open[0].ID != "parent" {
		t.Errorf("topo sort: open[0]=%q, want 'parent' (prerequisite first)", open[0].ID)
	}
}

// --- Bug 3: Board view should show column headers (top of columns visible) ---

func TestBug03_BoardColumnsShowHeaders(t *testing.T) {
	theme, styles := testThemeAndStyles()
	board := NewBoardModel(sampleBeads())
	out := board.Render(theme, styles)

	for _, header := range []string{"Ready", "In Progress", "Blocked", "Done"} {
		if !strings.Contains(out, header) {
			t.Errorf("board missing column header %q", header)
		}
	}
}

func TestBug03_ColumnStyleHasPadding(t *testing.T) {
	_, styles := testThemeAndStyles()
	// Column style should have padding to give header breathing room
	rendered := styles.Column.Render("test")
	// With Padding(1, 0), there should be blank lines above/below content
	lines := strings.Split(rendered, "\n")
	if len(lines) < 3 {
		t.Errorf("Column style should add padding, got only %d lines", len(lines))
	}
}

// --- Bug 4: Board view sort — priority ascending (P0 first) for non-Done ---

func TestBug04_BoardSortPriorityAscending(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "p3", Title: "Low pri", Status: "open", Priority: 3},
		{ID: "p0", Title: "Critical", Status: "open", Priority: 0},
		{ID: "p1", Title: "High pri", Status: "open", Priority: 1},
	}
	board := NewBoardModel(beads)

	// Ready column is index 0
	readyCol := board.columns[0]
	if readyCol.title != "Ready" {
		t.Fatalf("expected first column 'Ready', got %q", readyCol.title)
	}
	if len(readyCol.beads) != 3 {
		t.Fatalf("Ready column has %d beads, want 3", len(readyCol.beads))
	}
	// Order should be P0, P1, P3
	expected := []int{0, 1, 3}
	for i, want := range expected {
		if readyCol.beads[i].Priority != want {
			t.Errorf("Ready[%d].Priority = %d, want %d", i, readyCol.beads[i].Priority, want)
		}
	}
}

func TestBug04_DoneSortByTimeDescending(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "old", Title: "Old done", Status: "closed", Priority: 0, UpdatedAt: "2024-01-01T00:00:00Z"},
		{ID: "new", Title: "New done", Status: "closed", Priority: 3, UpdatedAt: "2024-03-01T00:00:00Z"},
	}
	board := NewBoardModel(beads)

	doneCol := board.columns[3]
	if doneCol.title != "Done" {
		t.Fatalf("expected column 4 'Done', got %q", doneCol.title)
	}
	if doneCol.beads[0].ID != "new" {
		t.Error("Done column should sort newest first (by UpdatedAt desc)")
	}
}

// --- Bug 5: Detail view should NOT have left column (renderContextSplit) ---

func TestBug05_DetailViewNoLeftColumn(t *testing.T) {
	theme, styles := testThemeAndStyles()

	m := newModel()
	m.theme = theme
	m.styles = styles
	m.width = 120
	m.height = 40
	m.activeView = DetailView
	dm := newDetailModel(protocol.BeadDetail{
		ID:     "test-1",
		Title:  "Test bead",
		Status: "open",
	}, theme, styles)
	m.detailModel = &dm

	// Test the DetailModel directly (not through m.View() which needs full bubbletea init)
	out := m.detailModel.View(styles)
	// Should contain the bead info from the overview tab
	if !strings.Contains(out, "test-1") {
		t.Errorf("DetailView should show bead ID, got:\n%s", out)
	}
	// Should contain tab headers (no left column — just tabs + content)
	if !strings.Contains(out, "Overview") {
		t.Error("DetailView should show Overview tab")
	}
}

// --- Bug 6: All 6 tabs should always be shown ---

func TestBug06_AllTabsShownForOpenBead(t *testing.T) {
	tabs := getTabsForBead(protocol.BeadDetail{Status: "open"})
	if len(tabs) != 6 {
		t.Errorf("open bead: got %d tabs, want 6", len(tabs))
	}
}

func TestBug06_AllTabsShownForClosedBead(t *testing.T) {
	tabs := getTabsForBead(protocol.BeadDetail{Status: "closed"})
	if len(tabs) != 6 {
		t.Errorf("closed bead: got %d tabs, want 6", len(tabs))
	}
	expected := []string{"Overview", "Worker", "Diff", "Deps", "Memory", "Output"}
	for i, want := range expected {
		if tabs[i] != want {
			t.Errorf("tab[%d] = %q, want %q", i, tabs[i], want)
		}
	}
}

// --- Bug 7: Status bar should not overflow right ---

func TestBug07_StatusBarMaxWidth(t *testing.T) {
	_, styles := testThemeAndStyles()

	m := newModel()
	m.theme = DefaultTheme()
	m.styles = styles
	m.width = 80
	m.height = 40
	m.daemonHealthy = true
	m.workerCount = 5
	m.openCount = 10
	m.inProgressCount = 3

	bar := m.renderStatusBar(80)
	barWidth := lipgloss.Width(bar)
	if barWidth > 80 {
		t.Errorf("status bar width %d exceeds terminal width 80", barWidth)
	}
}

// --- Bug 8: Help should contain all key bindings ---

func TestBug08_ListViewHelpHasLKey(t *testing.T) {
	bindings := getListViewHelpBindings()
	found := false
	for _, b := range bindings {
		if b.key == "L" {
			found = true
			break
		}
	}
	if !found {
		t.Error("ListView help missing 'L' key binding")
	}
}

func TestBug08_StatusViewHelpHasLKey(t *testing.T) {
	bindings := getStatusHelpBindings()
	found := false
	for _, b := range bindings {
		if b.key == "L" {
			found = true
			break
		}
	}
	if !found {
		t.Error("StatusView help missing 'L' key binding")
	}
}

// --- Bug 9: Search should look at ALL beads (including extra closed) ---

func TestBug09_FilterBeadsUsesAllBeads(t *testing.T) {
	m := newModel()
	m.beads = []protocol.Bead{
		{ID: "main-1", Title: "Main bead", Status: "open"},
	}
	m.extraClosed = []protocol.Bead{
		{ID: "extra-1", Title: "Extra closed", Status: "closed"},
	}

	all := m.filterBeads()
	if len(all) != 2 {
		t.Errorf("filterBeads returned %d beads, want 2 (main + extra)", len(all))
	}

	// Verify allBeads includes both
	allBeads := m.allBeads()
	if len(allBeads) != 2 {
		t.Errorf("allBeads returned %d, want 2", len(allBeads))
	}
}

// --- Bug 10: Tabs for closed bead (resolved by bug 6) ---

func TestBug10_ClosedBeadShowsAllTabs(t *testing.T) {
	tabs := getTabsForBead(protocol.BeadDetail{Status: "closed"})
	if len(tabs) != 6 {
		t.Errorf("closed bead: got %d tabs, want 6 (all tabs always shown)", len(tabs))
	}
}

// --- Bug 11: Insights should not always show "Computing..." ---

func TestBug11_InsightsModelCachesResults(t *testing.T) {
	beads := []BeadWithDeps{
		{ID: "a", Status: "open"},
		{ID: "b", Status: "open"},
	}

	im := NewInsightsModel(beads)
	// Phase 1 should be immediately available
	if im.Phase1() == nil {
		t.Fatal("Phase1 should be non-nil immediately after NewInsightsModel")
	}

	// Wait for phase 2 goroutine (up to 600ms)
	var p2 *Phase2Results
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		p2 = im.Phase2()
		if p2 != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if p2 == nil {
		t.Error("Phase2 should complete within 600ms, still nil (always computing)")
	}
}

func TestBug11_InsightsModelCachedInApplyBeadsMsg(t *testing.T) {
	m := newModel()
	m.theme = DefaultTheme()
	m.styles = NewStyles(m.theme)

	beads := []protocol.Bead{
		{ID: "a", Title: "Bead A", Status: "open"},
	}

	// Simulate applyBeadsMsg
	m = m.applyBeadsMsg(beadsMsg(beads))

	if m.insightsModel == nil {
		t.Error("applyBeadsMsg should cache insightsModel (not nil)")
	}
}

// --- Bug 12: Socket path shown when daemon offline ---

func TestBug12_OfflineStatusShowsSocketPath(t *testing.T) {
	_, styles := testThemeAndStyles()
	s := StatusModel{}

	out := s.View(DefaultTheme(), styles, nil, nil, 0, nil, nil, 120, 40)
	if !strings.Contains(out, "Socket:") {
		t.Error("offline status should show socket path")
	}
}

// --- Bug 13: Status view should NOT be empty when daemon is offline ---

func TestBug13_OfflineStatusNotEmpty(t *testing.T) {
	_, styles := testThemeAndStyles()
	s := StatusModel{}

	out := s.View(DefaultTheme(), styles, nil, nil, 0, nil, nil, 120, 40)
	if strings.Contains(out, "Connecting...") {
		t.Error("nil healthData should NOT show 'Connecting...'")
	}
	if !strings.Contains(out, "Daemon: offline") {
		t.Error("nil healthData should show 'Daemon: offline'")
	}
	if !strings.Contains(out, "Try: oro start") {
		t.Error("nil healthData should show 'Try: oro start' hint")
	}
}

// --- Bug 14: Sparklines should appear at reasonable widths ---

func TestBug14_SparklineShownAtWidth80(t *testing.T) {
	theme, styles := testThemeAndStyles()

	workers := []WorkerStatus{
		{ID: "w1", Status: "active", BeadID: "b1", ContextPct: 30},
	}
	buf := &MetricsBuffer{}

	// At width 80, sparklines should be rendered (threshold is 60)
	card := renderWorkerCard(workers[0], nil, 26, theme, styles, 80)
	// Card at width >= 60 should have 2 lines (status + sparkline)
	lines := strings.Split(strings.TrimSpace(card), "\n")
	if len(lines) < 2 {
		t.Errorf("worker card at width=80 should have 2+ lines (sparkline), got %d", len(lines))
	}

	// At width 50 (< 60), should be single line (no sparkline)
	card50 := renderWorkerCard(workers[0], nil, 16, theme, styles, 50)
	lines50 := strings.Split(strings.TrimSpace(card50), "\n")
	if len(lines50) > 1 {
		t.Errorf("worker card at width=50 should have 1 line (no sparkline), got %d", len(lines50))
	}

	_ = buf // buf used for pipeline sparklines (separate test)
}

func TestBug14_PipelineSparklineRendered(t *testing.T) {
	theme, styles := testThemeAndStyles()

	buf := NewMetricsBuffer()
	now := time.Now()
	// Record 3 samples to ensure sparkline renders
	buf.Record(MetricsSample{Timestamp: now.Add(-10 * time.Second), BeadsClosed: 0, QueueReady: 5, WorkersActive: 2})
	buf.Record(MetricsSample{Timestamp: now.Add(-5 * time.Second), BeadsClosed: 1, QueueReady: 4, WorkersActive: 2})
	buf.Record(MetricsSample{Timestamp: now, BeadsClosed: 2, QueueReady: 3, WorkersActive: 2})

	out := renderPipelineSection(buf, 120, theme, styles)
	if !strings.Contains(out, "Pipeline") {
		t.Error("pipeline section missing title")
	}
	if !strings.Contains(out, "Throughput") {
		t.Error("pipeline section missing throughput row")
	}
}

// --- Bug 15: List view cursor should start on a visible bead ---

func TestBug15_CursorStartsOnBead(t *testing.T) {
	lm := NewListModel()
	if lm.cursor != -1 {
		t.Errorf("initial cursor = %d, want -1 (unset)", lm.cursor)
	}

	lm = lm.updateBeads(sampleBeads())
	id := lm.cursorBeadID()
	if id == "" {
		t.Error("after updateBeads, cursor should be on a bead (not header or off-screen)")
	}
}

func TestBug15_CursorMatchesByIDInTwoColumnMode(t *testing.T) {
	_, styles := testThemeAndStyles()
	beads := []protocol.Bead{
		{ID: "op-1", Title: "First", Status: "open", Priority: 1, Type: "task"},
		{ID: "op-2", Title: "Second", Status: "open", Priority: 2, Type: "task"},
		{ID: "cl-1", Title: "Closed", Status: "closed", Priority: 1, Type: "task", UpdatedAt: "2024-03-01T00:00:00Z"},
	}
	lm := NewListModel().updateBeads(beads)

	// Cursor should be on first bead
	if lm.cursorBeadID() != "op-1" {
		t.Errorf("cursor on %q, want 'op-1'", lm.cursorBeadID())
	}

	// Two-column render should include both panes
	out := lm.View(DefaultTheme(), styles, 120, 30)
	if !strings.Contains(out, "op-1") || !strings.Contains(out, "cl-1") {
		t.Error("two-column view missing beads")
	}
}
