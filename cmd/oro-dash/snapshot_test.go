package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/protocol"
)

// update controls whether golden files are regenerated on this run.
// Run with: go test -run TestSnapshot -update
var update = flag.Bool("update", false, "regenerate golden files")

// goldenPath returns the path to the golden file for a given test name.
func goldenPath(t *testing.T) string {
	t.Helper()
	// Sanitise the test name so it is safe as a filename.
	name := strings.ReplaceAll(t.Name(), "/", "__")
	name = strings.ReplaceAll(name, " ", "_")
	return filepath.Join("testdata", name+".golden")
}

// assertGolden compares got against the golden file for the current test.
// When -update is passed, it writes got to the golden file instead.
func assertGolden(t *testing.T, got string) {
	t.Helper()
	path := goldenPath(t)

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { //nolint:gosec // test helper directory
			t.Fatalf("assertGolden: mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil { //nolint:gosec // test data file
			t.Fatalf("assertGolden: write %s: %v", path, err)
		}
		t.Logf("updated golden file: %s", path)
		return
	}

	// On first run the file won't exist yet — auto-create it so subsequent
	// runs do the comparison.  This matches the TDD intent described in the
	// task: "generate on first run, assert on subsequent runs".
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err2 := os.MkdirAll(filepath.Dir(path), 0o750); err2 != nil { //nolint:gosec // test helper directory
			t.Fatalf("assertGolden: mkdir %s: %v", filepath.Dir(path), err2)
		}
		if err2 := os.WriteFile(path, []byte(got), 0o600); err2 != nil { //nolint:gosec // test data file
			t.Fatalf("assertGolden: write %s: %v", path, err2)
		}
		t.Logf("created golden file: %s", path)
		return
	}

	want, err := os.ReadFile(path) //nolint:gosec // path is derived from t.Name(), not user input
	if err != nil {
		t.Fatalf("assertGolden: read %s: %v", path, err)
	}

	if got != string(want) {
		t.Errorf("snapshot mismatch for %s\n--- want ---\n%s\n--- got ---\n%s",
			path, string(want), got)
	}
}

// ── Board view snapshots ──────────────────────────────────────────────────────

func TestSnapshot_BoardView_Empty(t *testing.T) {
	board := NewBoardModel(nil)
	theme := DefaultTheme()
	output := board.Render(theme, NewStyles(theme))
	assertGolden(t, output)
}

func TestSnapshot_BoardView_AllStatuses(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "oro-aaa.1", Title: "Open task", Status: "open", Priority: 1, Type: "task"},
		{ID: "oro-aaa.2", Title: "In-progress bug", Status: "in_progress", Priority: 0, Type: "bug"},
		{
			ID: "oro-aaa.3", Title: "Blocked feature", Status: "blocked", Priority: 2, Type: "feature",
			Dependencies: []protocol.Dependency{
				{IssueID: "oro-aaa.3", DependsOnID: "oro-aaa.1", Type: "blocks"},
			},
		},
		{ID: "oro-aaa.4", Title: "Closed epic", Status: "closed", Priority: 3, Type: "epic"},
	}

	board := NewBoardModel(beads)
	theme := DefaultTheme()
	output := board.Render(theme, NewStyles(theme))
	assertGolden(t, output)
}

func TestSnapshot_BoardView_WithWorkers(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "oro-bbb.1", Title: "Task with worker", Status: "in_progress", Priority: 1, Type: "task"},
		{ID: "oro-bbb.2", Title: "Stale worker task", Status: "in_progress", Priority: 2, Type: "task"},
	}

	workers := []WorkerStatus{
		{ID: "worker-healthy", Status: "busy", LastProgressSecs: 2.0, ContextPct: 35},
		{ID: "worker-stale", Status: "busy", LastProgressSecs: 20.0, ContextPct: 75},
	}

	assignments := map[string]string{
		"oro-bbb.1": "worker-healthy",
		"oro-bbb.2": "worker-stale",
	}

	board := NewBoardModelWithWorkers(beads, workers, assignments)
	theme := DefaultTheme()
	output := board.RenderWithCursor(-1, -1, theme, NewStyles(theme))
	assertGolden(t, output)
}

func TestSnapshot_BoardView_DoneColumnCapped(t *testing.T) {
	// 12 closed beads — Done column should show the most recent 10.
	closed := make([]protocol.Bead, 12)
	for i := range 12 {
		closed[i] = protocol.Bead{
			ID:     "oro-done." + string(rune('a'+i)),
			Title:  "Closed " + string(rune('A'+i)),
			Status: "closed",
		}
	}

	board := NewBoardModel(closed)
	theme := DefaultTheme()
	output := board.Render(theme, NewStyles(theme))
	assertGolden(t, output)
}

// ── Detail view snapshots ─────────────────────────────────────────────────────

func TestSnapshot_DetailView_OverviewTab(t *testing.T) {
	bead := protocol.BeadDetail{
		ID:                 "oro-det.1",
		Title:              "Detail snapshot bead",
		AcceptanceCriteria: "Must render correctly",
		Model:              "claude-opus-4-6",
	}

	theme := DefaultTheme()
	styles := NewStyles(theme)
	model := newDetailModel(bead, theme, styles)
	model.activeTab = 0
	model.width = 80
	model.height = 40

	output := model.View(styles)
	assertGolden(t, output)
}

func TestSnapshot_DetailView_WorkerTab_Assigned(t *testing.T) {
	bead := protocol.BeadDetail{
		ID:             "oro-det.2",
		Title:          "Bead with worker",
		WorkerID:       "worker-snapshot",
		ContextPercent: 58,
		LastHeartbeat:  "2026-02-19T10:00:00Z",
	}

	theme := DefaultTheme()
	styles := NewStyles(theme)
	model := newDetailModel(bead, theme, styles)
	// Override loading state so the render is deterministic (no async content).
	model.loadingEvents = false
	model.loadingOutput = false
	model.activeTab = 1
	model.width = 80
	model.height = 40

	output := model.View(styles)
	assertGolden(t, output)
}

func TestSnapshot_DetailView_WorkerTab_Unassigned(t *testing.T) {
	bead := protocol.BeadDetail{
		ID:    "oro-det.3",
		Title: "Bead without worker",
	}

	theme := DefaultTheme()
	styles := NewStyles(theme)
	model := newDetailModel(bead, theme, styles)
	model.loadingEvents = false
	model.loadingOutput = false
	model.activeTab = 1
	model.width = 80
	model.height = 40

	output := model.View(styles)
	assertGolden(t, output)
}

func TestSnapshot_DetailView_DiffTab(t *testing.T) {
	bead := protocol.BeadDetail{
		ID:    "oro-det.4",
		Title: "Bead with diff",
		GitDiff: `diff --git a/main.go b/main.go
index abc123..def456 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,5 @@
 package main
+
+// Snapshot test comment
 func main() {}`,
	}

	theme := DefaultTheme()
	styles := NewStyles(theme)
	model := newDetailModel(bead, theme, styles)
	model.loadingEvents = false
	model.loadingOutput = false
	model.activeTab = 2
	model.width = 80
	model.height = 40

	output := model.View(styles)
	assertGolden(t, output)
}

func TestSnapshot_DetailView_DepsTab(t *testing.T) {
	bead := protocol.BeadDetail{
		ID:    "oro-det.5",
		Title: "Bead deps tab",
	}

	theme := DefaultTheme()
	styles := NewStyles(theme)
	model := newDetailModel(bead, theme, styles)
	model.loadingEvents = false
	model.loadingOutput = false
	model.activeTab = 3
	model.width = 80
	model.height = 40

	output := model.View(styles)
	assertGolden(t, output)
}

func TestSnapshot_DetailView_MemoryTab(t *testing.T) {
	bead := protocol.BeadDetail{
		ID:    "oro-det.6",
		Title: "Bead with memory",
		Memory: `Previous attempt notes:
- Tried approach A, did not work
- Approach B looks promising`,
	}

	theme := DefaultTheme()
	styles := NewStyles(theme)
	model := newDetailModel(bead, theme, styles)
	model.loadingEvents = false
	model.loadingOutput = false
	model.activeTab = 4
	model.width = 80
	model.height = 40

	output := model.View(styles)
	assertGolden(t, output)
}

func TestSnapshot_DetailView_OutputTab(t *testing.T) {
	bead := protocol.BeadDetail{
		ID:       "oro-det.7",
		Title:    "Bead output tab",
		WorkerID: "worker-out",
	}

	theme := DefaultTheme()
	styles := NewStyles(theme)
	model := newDetailModel(bead, theme, styles)
	model.loadingEvents = false
	model.loadingOutput = false
	model.workerOutput = []string{
		"[INFO] Starting task",
		"[INFO] Processing step 1",
		"[DONE] Task complete",
	}
	model.activeTab = 5
	model.width = 80
	model.height = 40

	output := model.View(styles)
	assertGolden(t, output)
}

// ── Search view snapshots ─────────────────────────────────────────────────────

func TestSnapshot_SearchView_EmptyQuery(t *testing.T) {
	m := newModel()
	m.activeView = SearchView
	m.width = 80
	m.height = 40
	m.beads = []protocol.Bead{
		{ID: "oro-srch.1", Title: "First result", Status: "open"},
		{ID: "oro-srch.2", Title: "Second result", Status: "in_progress"},
	}
	m.searchInput.Focus()

	output := m.renderSearchOverlay()
	assertGolden(t, output)
}

func TestSnapshot_SearchView_WithResults(t *testing.T) {
	m := newModel()
	m.activeView = SearchView
	m.width = 80
	m.height = 40
	m.beads = []protocol.Bead{
		{ID: "oro-srch.1", Title: "Authentication fix", Status: "open", Priority: 0, Type: "bug"},
		{ID: "oro-srch.2", Title: "Dashboard feature", Status: "in_progress", Priority: 1, Type: "feature"},
		{ID: "oro-srch.3", Title: "Auth module refactor", Status: "open", Priority: 2, Type: "task"},
	}
	m.searchInput.Focus()
	m.searchInput.SetValue("auth")

	output := m.renderSearchOverlay()
	assertGolden(t, output)
}

func TestSnapshot_SearchView_NoResults(t *testing.T) {
	m := newModel()
	m.activeView = SearchView
	m.width = 80
	m.height = 40
	m.beads = []protocol.Bead{
		{ID: "oro-srch.1", Title: "Unrelated task", Status: "open"},
	}
	m.searchInput.Focus()
	m.searchInput.SetValue("xyznotfound")

	output := m.renderSearchOverlay()
	assertGolden(t, output)
}

// ── Insights view snapshots ───────────────────────────────────────────────────

func TestSnapshot_InsightsView_Empty(t *testing.T) {
	model := NewInsightsModel([]BeadWithDeps{})
	theme := DefaultTheme()
	styles := NewStyles(theme)

	output := model.Render(styles)
	assertGolden(t, output)
}

func TestSnapshot_InsightsView_WithCriticalPath(t *testing.T) {
	beads := []BeadWithDeps{
		{ID: "oro-ins.1", DependsOn: []string{"oro-ins.2"}},
		{ID: "oro-ins.2", DependsOn: []string{"oro-ins.3"}},
		{ID: "oro-ins.3"},
	}

	model := NewInsightsModel(beads)
	theme := DefaultTheme()
	styles := NewStyles(theme)

	output := model.Render(styles)
	assertGolden(t, output)
}

func TestSnapshot_InsightsView_WithBottleneck(t *testing.T) {
	beads := []BeadWithDeps{
		{ID: "oro-ins.4", DependsOn: []string{"oro-ins.6"}},
		{ID: "oro-ins.5", DependsOn: []string{"oro-ins.6"}},
		{ID: "oro-ins.6"},
	}

	model := NewInsightsModel(beads)
	theme := DefaultTheme()
	styles := NewStyles(theme)

	output := model.Render(styles)
	assertGolden(t, output)
}

func TestSnapshot_InsightsView_WithTriageFlags(t *testing.T) {
	beads := []BeadWithDeps{
		{ID: "oro-ins.7", Priority: 0, DaysSinceUpdate: 10},
		{ID: "oro-ins.8", Type: "bug", Priority: 4},
	}

	model := NewInsightsModel(beads)
	theme := DefaultTheme()
	styles := NewStyles(theme)

	output := model.Render(styles)
	assertGolden(t, output)
}

func TestSnapshot_InsightsView_WithCycle(t *testing.T) {
	beads := []BeadWithDeps{
		{ID: "oro-ins.9", DependsOn: []string{"oro-ins.10"}},
		{ID: "oro-ins.10", DependsOn: []string{"oro-ins.9"}},
	}

	model := NewInsightsModel(beads)
	theme := DefaultTheme()
	styles := NewStyles(theme)

	output := model.Render(styles)
	assertGolden(t, output)
}

// ── Workers table view snapshots ──────────────────────────────────────────────

func TestSnapshot_WorkersView_Empty(t *testing.T) {
	wt := NewWorkersTableModel(nil, nil)
	theme := DefaultTheme()
	styles := NewStyles(theme)

	output := wt.View(theme, styles)
	assertGolden(t, output)
}

func TestSnapshot_WorkersView_MultipleWorkers(t *testing.T) {
	workers := []WorkerStatus{
		{ID: "worker-green", Status: "busy", BeadID: "oro-w.1", LastProgressSecs: 1.5, ContextPct: 20},
		{ID: "worker-amber", Status: "busy", BeadID: "oro-w.2", LastProgressSecs: 9.0, ContextPct: 55},
		{ID: "worker-red", Status: "stale", BeadID: "oro-w.3", LastProgressSecs: 25.0, ContextPct: 90},
		{ID: "worker-idle", Status: "idle", BeadID: "", LastProgressSecs: 0.5, ContextPct: 0},
	}

	assignments := map[string]string{
		"oro-w.1": "worker-green",
		"oro-w.2": "worker-amber",
		"oro-w.3": "worker-red",
	}

	wt := NewWorkersTableModel(workers, assignments)
	theme := DefaultTheme()
	styles := NewStyles(theme)

	output := wt.View(theme, styles)
	assertGolden(t, output)
}
