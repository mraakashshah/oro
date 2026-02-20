# Dashboard Phase 1: Data Correctness Implementation Plan

> **For Claude:** Use executing-plans skill to implement this plan task-by-task.

**Goal:** Fix 6 data/wiring issues in oro-dash so the dashboard shows correct data, responds fast, and has no dead views — prerequisites for the Phase 2 visual design refresh.

**Architecture:** All changes are in `cmd/oro-dash/`. Each task is self-contained with no cross-task dependencies except Task 1 (type unification) which must land first since Tasks 4 and 5 depend on `protocol.Bead` being the sole bead type. Tasks 2, 3, 6 are independent of each other.

**Tech Stack:** Go, Bubble Tea, lipgloss, `bd` CLI JSON output, dispatcher UDS protocol.

**Dependency order:** Task 1 → (Tasks 2, 3, 4, 5, 6 in parallel)

---

## Task 1: Unify Bead types and consolidate fetch to single bd call

The local `Bead` type in `main.go` has json tag `"type"` but `bd list --json` outputs `"issue_type"` — the `t:` search filter has been silently broken. Unify on `protocol.Bead` and switch fetch to the single-call path.

**Files:**
- Modify: `cmd/oro-dash/main.go` (remove local `Bead` type)
- Modify: `cmd/oro-dash/search.go` (change `Bead` → `protocol.Bead`)
- Modify: `cmd/oro-dash/datasource.go` (return `[]protocol.Bead`)
- Modify: `cmd/oro-dash/datasource_test.go` (update type references)
- Modify: `cmd/oro-dash/model.go` (remove `filterBeads` conversion layer, use `datasource.FetchBeads` path)
- Test: `cmd/oro-dash/datasource_test.go`, `cmd/oro-dash/search_test.go`

**Step 1: Update search.go to use protocol.Bead**

Change `matchesBead` and `Filter` to accept `protocol.Bead`:

```go
// In search.go
import "oro/pkg/protocol"

func matchesBead(bead protocol.Bead, filters searchFilters) bool {
    if filters.priority != nil && bead.Priority != *filters.priority {
        return false
    }
    if filters.status != "" && !strings.EqualFold(bead.Status, filters.status) {
        return false
    }
    if filters.beadType != "" && !strings.EqualFold(bead.Type, filters.beadType) {
        return false
    }
    if len(filters.fuzzyTerms) > 0 {
        searchableText := strings.ToLower(fmt.Sprintf("%s %s", bead.ID, bead.Title))
        for _, term := range filters.fuzzyTerms {
            if !strings.Contains(searchableText, term) {
                return false
            }
        }
    }
    return true
}

func (s *SearchModel) Filter(beads []protocol.Bead, query string) []protocol.Bead {
    if query == "" {
        return beads
    }
    filters := parseQuery(query)
    var result []protocol.Bead
    for _, bead := range beads {
        if matchesBead(bead, filters) {
            result = append(result, bead)
        }
    }
    return result
}
```

**Step 2: Update datasource.go to return protocol.Bead**

```go
func FetchBeads() ([]protocol.Bead, error) {
    if _, err := exec.LookPath("bd"); err != nil {
        return nil, fmt.Errorf("bd not in PATH: %w", err)
    }
    ctx := context.Background()
    cmd := exec.CommandContext(ctx, "bd", "list", "--json")
    out, err := cmd.Output()
    if err != nil {
        return nil, fmt.Errorf("bd list --json: %w", err)
    }
    output := strings.TrimSpace(string(out))
    if output == "" {
        return []protocol.Bead{}, nil
    }
    var beads []protocol.Bead
    if err := json.Unmarshal([]byte(output), &beads); err != nil {
        return nil, fmt.Errorf("parse beads JSON: %w", err)
    }
    return beads, nil
}
```

**Step 3: Remove local Bead type from main.go**

Delete lines 15-22 (the `type Bead struct` block). Keep `WorkerStatus` and everything else.

**Step 4: Simplify filterBeads in model.go**

Remove the conversion layer — `SearchModel.Filter` now takes `[]protocol.Bead` directly:

```go
func (m Model) filterBeads() []protocol.Bead {
    query := m.searchInput.Value()
    if query == "" {
        return m.beads
    }
    return m.searchModel.Filter(m.beads, query)
}
```

**Step 5: Update datasource_test.go**

Update compile-time check and test assertions to use `protocol.Bead`:

```go
var _ func() ([]protocol.Bead, error) = FetchBeads
```

**Step 6: Wire TUI fetch path to single bd call**

The TUI's actual fetch path is `fetch.go:fetchBeads()` which makes 4 separate `bd list --status X --json` calls. The model's `fetchBeadsCmd()` calls this function. Switch it to use the consolidated single-call path.

In `model.go`, change `fetchBeadsCmd`:

```go
func fetchBeadsCmd() tea.Cmd {
    return func() tea.Msg {
        beads, _ := FetchBeads()
        return beadsMsg(beads)
    }
}
```

This replaces the call to `fetchBeads(context.Background())` (from `fetch.go`, 4 subprocess calls) with `FetchBeads()` (from `datasource.go`, single subprocess call).

Then remove or deprecate the 4-call `fetchBeads()` function in `fetch.go` (keep `fetchWorkerStatus`, `fetchWorkerOutput`, and `fetchHealth` which use UDS, not bd CLI).

In `fetch.go`, delete the `fetchBeads` and `fetchBeadsWithStatus` functions (lines 22-71), and the `parseBeadsOutput` helper (lines 22-32) — all replaced by `datasource.FetchBeads()`.

Also update `robotMode()` in `main.go` which calls `fetchBeads`:

```go
func robotMode() error {
    return robotModeWithFetch(context.Background(), func(ctx context.Context) ([]protocol.Bead, error) {
        return FetchBeads()
    })
}
```

**Step 7: Add integration test for t: filter through filterBeads pipeline**

Add a test that exercises the full `searchInput → filterBeads → SearchModel.Filter` path with type filtering to verify the json tag fix works end-to-end:

```go
func TestFilterBeads_TypeFilter_Integration(t *testing.T) {
    m := newModel()
    m.beads = []protocol.Bead{
        {ID: "oro-1", Title: "Fix auth", Status: "open", Priority: 1, Type: "bug"},
        {ID: "oro-2", Title: "Add feature", Status: "open", Priority: 2, Type: "feature"},
        {ID: "oro-3", Title: "Write tests", Status: "open", Priority: 2, Type: "task"},
    }
    m.searchInput.SetValue("t:bug")

    results := m.filterBeads()

    if len(results) != 1 {
        t.Fatalf("expected 1 result for t:bug, got %d", len(results))
    }
    if results[0].ID != "oro-1" {
        t.Errorf("expected oro-1, got %q", results[0].ID)
    }
}
```

**Step 8: Run tests**

```bash
go test ./cmd/oro-dash/... -v -count=1
```

Expected: PASS (search `t:` filter now actually works since json tag matches, and TUI uses single bd call).

**Step 9: Commit**

```
fix(dash): unify Bead types and consolidate fetch to single bd call
```

---

## Task 2: Sort bottlenecks by blocked count

**Files:**
- Modify: `cmd/oro-dash/graph.go:171-173`
- Test: `cmd/oro-dash/graph_test.go`

**Step 1: Write failing test**

Add test in `graph_test.go`:

```go
func TestBottlenecksSortedByBlockedCountDescending(t *testing.T) {
    graph := NewDependencyGraph([]BeadWithDeps{
        {ID: "a", DependsOn: []string{"c"}},
        {ID: "b", DependsOn: []string{"c"}},
        {ID: "d", DependsOn: []string{"c"}},
        {ID: "e", DependsOn: []string{"f"}},
        {ID: "c"},
        {ID: "f"},
    })

    bottlenecks := graph.Bottlenecks()

    if len(bottlenecks) != 2 {
        t.Fatalf("expected 2 bottlenecks, got %d", len(bottlenecks))
    }
    // c blocks 3 beads, f blocks 1 — c should come first
    if bottlenecks[0].BeadID != "c" {
        t.Errorf("expected first bottleneck to be 'c' (blocks 3), got %q (blocks %d)",
            bottlenecks[0].BeadID, bottlenecks[0].BlockedCount)
    }
    if bottlenecks[0].BlockedCount != 3 {
        t.Errorf("expected blocked count 3, got %d", bottlenecks[0].BlockedCount)
    }
    if bottlenecks[1].BeadID != "f" {
        t.Errorf("expected second bottleneck to be 'f' (blocks 1), got %q", bottlenecks[1].BeadID)
    }
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./cmd/oro-dash/... -run TestBottlenecksSortedByBlockedCountDescending -v
```

Expected: FAIL (currently sorts by BeadID, so "c" < "f" happens to pass — adjust test IDs if needed to expose the bug, e.g. rename "c" to "z-blocker").

Actually, since "c" < "f" alphabetically, the current sort happens to put "c" first. Use IDs that expose the bug:

```go
func TestBottlenecksSortedByBlockedCountDescending(t *testing.T) {
    graph := NewDependencyGraph([]BeadWithDeps{
        {ID: "a", DependsOn: []string{"z-big-blocker"}},
        {ID: "b", DependsOn: []string{"z-big-blocker"}},
        {ID: "d", DependsOn: []string{"z-big-blocker"}},
        {ID: "e", DependsOn: []string{"a-small-blocker"}},
        {ID: "z-big-blocker"},
        {ID: "a-small-blocker"},
    })

    bottlenecks := graph.Bottlenecks()

    if len(bottlenecks) != 2 {
        t.Fatalf("expected 2 bottlenecks, got %d", len(bottlenecks))
    }
    // z-big-blocker blocks 3, a-small-blocker blocks 1
    // Sorted by blocked count descending: z-big-blocker first
    if bottlenecks[0].BeadID != "z-big-blocker" {
        t.Errorf("expected first bottleneck to be 'z-big-blocker' (blocks 3), got %q (blocks %d)",
            bottlenecks[0].BeadID, bottlenecks[0].BlockedCount)
    }
}
```

Expected: FAIL (alphabetic sort puts "a-small-blocker" first).

**Step 3: Fix sort**

In `graph.go`, change:

```go
sort.Slice(result, func(i, j int) bool {
    return result[i].BeadID < result[j].BeadID
})
```

To:

```go
sort.Slice(result, func(i, j int) bool {
    if result[i].BlockedCount != result[j].BlockedCount {
        return result[i].BlockedCount > result[j].BlockedCount
    }
    return result[i].BeadID < result[j].BeadID
})
```

**Step 4: Run test to verify it passes**

```bash
go test ./cmd/oro-dash/... -run TestBottlenecksSortedByBlockedCountDescending -v
```

Expected: PASS

**Step 5: Commit**

```
fix(dash): sort bottlenecks by blocked count descending
```

---

## Task 3: Timestamp-sort Done column

**Files:**
- Modify: `cmd/oro-dash/board.go:67-73`
- Test: `cmd/oro-dash/board_test.go`

**Step 1: Write failing test**

```go
func TestDoneColumnSortedByUpdatedAtDescending(t *testing.T) {
    beads := []protocol.Bead{
        {ID: "old", Title: "Old", Status: "closed", UpdatedAt: "2026-02-01T00:00:00Z"},
        {ID: "mid", Title: "Mid", Status: "closed", UpdatedAt: "2026-02-10T00:00:00Z"},
        {ID: "new", Title: "New", Status: "closed", UpdatedAt: "2026-02-20T00:00:00Z"},
    }

    board := NewBoardModel(beads)

    // Done is column index 3
    doneCol := board.columns[3]
    if len(doneCol.beads) != 3 {
        t.Fatalf("expected 3 done beads, got %d", len(doneCol.beads))
    }
    // Most recent first
    if doneCol.beads[0].ID != "new" {
        t.Errorf("expected first done bead to be 'new', got %q", doneCol.beads[0].ID)
    }
    if doneCol.beads[1].ID != "mid" {
        t.Errorf("expected second done bead to be 'mid', got %q", doneCol.beads[1].ID)
    }
    if doneCol.beads[2].ID != "old" {
        t.Errorf("expected third done bead to be 'old', got %q", doneCol.beads[2].ID)
    }
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./cmd/oro-dash/... -run TestDoneColumnSortedByUpdatedAtDescending -v
```

Expected: FAIL (current order is insertion order, not timestamp-sorted).

**Step 3: Add sort before slice**

In `board.go`, inside `NewBoardModelWithWorkers`, after building buckets and before slicing:

```go
import (
    "sort"
    "time"
)

// Inside NewBoardModelWithWorkers, after buckets are built:
// Sort Done beads by UpdatedAt descending (most recent first)
if t == "Done" {
    sort.Slice(beadsInCol, func(i, j int) bool {
        ti, _ := time.Parse(time.RFC3339, beadsInCol[i].UpdatedAt)
        tj, _ := time.Parse(time.RFC3339, beadsInCol[j].UpdatedAt)
        return ti.After(tj)
    })
    totalCount = len(beadsInCol)
    if len(beadsInCol) > 10 {
        beadsInCol = beadsInCol[:10]
    }
}
```

Replace the existing Done-column limiting block (lines 71-73).

**Step 4: Run test to verify it passes**

```bash
go test ./cmd/oro-dash/... -run TestDoneColumnSortedByUpdatedAtDescending -v
```

Expected: PASS

**Step 5: Regenerate golden files**

This change alters Done column semantics from "last 10 by insertion order" to "first 10 by UpdatedAt descending." Snapshot tests (e.g. `TestSnapshot_BoardView_DoneColumnCapped`) will break because the slice endpoint changed. Beads with empty `UpdatedAt` sort as zero-time (preserving relative order) then take `[:10]` instead of `[len-10:]`.

```bash
go test ./cmd/oro-dash/... -run TestSnapshot_BoardView -update
```

**Step 6: Run all tests**

```bash
go test ./cmd/oro-dash/... -v -count=1
```

Expected: PASS

**Step 7: Commit**

```
fix(dash): sort Done column by most recently updated first
```

---

## Task 4: Wire HealthView to dispatcher health directive

**Files:**
- Modify: `cmd/oro-dash/model.go` (add healthMsg, fetchHealthCmd, keybinding, tick integration)
- Modify: `cmd/oro-dash/fetch.go` (add fetchHealth function)
- Modify: `cmd/oro-dash/health.go` (update HealthData to match dispatcher SwarmHealth)
- Test: `cmd/oro-dash/health_test.go`

**Step 1: Write failing test**

```go
func TestHealthViewShowsDispatcherData(t *testing.T) {
    m := newModel()
    m.healthData = &HealthData{
        DaemonPID:   12345,
        DaemonState: "running",
        WorkerCount: 3,
        ArchitectPane: PaneHealth{Name: "architect", Alive: false},
        ManagerPane:   PaneHealth{Name: "manager", Alive: false},
    }
    m.activeView = HealthView

    output := m.renderHealthView()

    if strings.Contains(output, "No health data available") {
        t.Error("health view should render data, not empty state")
    }
    if !strings.Contains(output, "12345") {
        t.Error("health view should show daemon PID")
    }
    if !strings.Contains(output, "running") {
        t.Error("health view should show daemon state")
    }
}
```

**Step 2: Run test — should PASS** (render code already works when healthData is set)

```bash
go test ./cmd/oro-dash/... -run TestHealthViewShowsDispatcherData -v
```

This verifies the render path. Now wire the data.

**Step 3: Add fetchHealth to fetch.go**

```go
// healthResponse mirrors the dispatcher's SwarmHealth JSON structure.
type healthResponse struct {
    Daemon struct {
        PID           int     `json:"pid"`
        UptimeSeconds float64 `json:"uptime_seconds"`
        State         string  `json:"state"`
    } `json:"daemon"`
    ArchitectPane struct {
        Name         string `json:"name"`
        Alive        bool   `json:"alive"`
        LastActivity string `json:"last_activity,omitempty"`
    } `json:"architect_pane"`
    ManagerPane struct {
        Name         string `json:"name"`
        Alive        bool   `json:"alive"`
        LastActivity string `json:"last_activity,omitempty"`
    } `json:"manager_pane"`
    Workers []workerEntry `json:"workers"`
}

// fetchHealth connects to the dispatcher UDS and sends a health directive.
// Returns nil if the socket doesn't exist or the connection fails.
func fetchHealth(ctx context.Context, socketPath string) (*HealthData, error) {
    if _, err := os.Stat(socketPath); err != nil {
        return nil, nil
    }

    ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
    defer cancel()

    var d net.Dialer
    conn, err := d.DialContext(ctx, "unix", socketPath)
    if err != nil {
        return nil, nil
    }
    defer conn.Close()

    msg := protocol.Message{
        Type:      protocol.MsgDirective,
        Directive: &protocol.DirectivePayload{Op: "health"},
    }
    data, err := json.Marshal(msg)
    if err != nil {
        return nil, nil
    }
    data = append(data, '\n')

    if _, err := conn.Write(data); err != nil {
        return nil, nil
    }

    scanner := bufio.NewScanner(conn)
    if !scanner.Scan() {
        return nil, nil
    }

    var ack protocol.Message
    if err := json.Unmarshal(scanner.Bytes(), &ack); err != nil {
        return nil, nil
    }

    if ack.Type != protocol.MsgACK || ack.ACK == nil || !ack.ACK.OK {
        return nil, nil
    }

    var resp healthResponse
    if err := json.Unmarshal([]byte(ack.ACK.Detail), &resp); err != nil {
        return nil, nil
    }

    return &HealthData{
        DaemonPID:   resp.Daemon.PID,
        DaemonState: resp.Daemon.State,
        ArchitectPane: PaneHealth{
            Name:         resp.ArchitectPane.Name,
            Alive:        resp.ArchitectPane.Alive,
            LastActivity: resp.ArchitectPane.LastActivity,
        },
        ManagerPane: PaneHealth{
            Name:         resp.ManagerPane.Name,
            Alive:        resp.ManagerPane.Alive,
            LastActivity: resp.ManagerPane.LastActivity,
        },
        WorkerCount: len(resp.Workers),
    }, nil
}
```

**Step 4: Add healthMsg and fetchHealthCmd to model.go**

```go
// healthMsg carries health data from the dispatcher.
type healthMsg struct {
    data *HealthData
}

// fetchHealthCmd returns a tea.Cmd that fetches health data from the dispatcher.
func fetchHealthCmd() tea.Cmd {
    return func() tea.Msg {
        socketPath := defaultSocketPath()
        data, _ := fetchHealth(context.Background(), socketPath)
        return healthMsg{data: data}
    }
}
```

**Step 5: Handle healthMsg in Update**

Add case in `Update()` switch after `workerDataMsg`:

```go
case healthMsg:
    m.healthData = msg.data
```

**Step 6: Add to tick batch**

Change tickMsg handler from:

```go
case tickMsg:
    return m, tea.Batch(fetchBeadsCmd(), fetchWorkersCmd(), tickCmd())
```

To:

```go
case tickMsg:
    return m, tea.Batch(fetchBeadsCmd(), fetchWorkersCmd(), fetchHealthCmd(), tickCmd())
```

Also add `fetchHealthCmd()` to `Init()`:

```go
return tea.Batch(fetchBeadsCmd(), fetchWorkersCmd(), fetchHealthCmd(), tickCmd())
```

**Step 7: Add H keybinding in handleBoardViewKeys**

Add after the `case "a"` block:

```go
case "H":
    m.activeView = HealthView
```

**Step 8: Update help hints**

In `helpHintsForView`, update `BoardView` case:

```go
case BoardView:
    return "hjkl nav  enter detail  / search  i insights  w workers  a tree  H health  ? help  q quit"
```

**Step 9: Run all tests**

```bash
go test ./cmd/oro-dash/... -v -count=1
```

Expected: PASS

**Step 10: Commit**

```
feat(dash): wire HealthView to dispatcher health directive
```

---

## Task 5: Make workers table width-adaptive

**Files:**
- Modify: `cmd/oro-dash/workers_table.go`
- Modify: `cmd/oro-dash/model.go:501-502` (pass width to View)
- Test: `cmd/oro-dash/workers_table_test.go`

**Step 1: Write failing test**

```go
func TestWorkersTableAdaptsToWidth(t *testing.T) {
    workers := []WorkerStatus{
        {ID: "w1", Status: "active", BeadID: "oro-001", LastProgressSecs: 2.0, ContextPct: 45},
    }
    theme := DefaultTheme()
    styles := NewStyles(theme)

    narrow := NewWorkersTableModel(workers, nil)
    narrowOutput := narrow.View(theme, styles, 80)

    wide := NewWorkersTableModel(workers, nil)
    wideOutput := wide.View(theme, styles, 160)

    if narrowOutput == wideOutput {
        t.Error("workers table should render differently at different widths")
    }
}

func TestWorkersTableEmptyStateAdaptsToWidth(t *testing.T) {
    theme := DefaultTheme()
    styles := NewStyles(theme)

    narrow := NewWorkersTableModel(nil, nil)
    narrowOutput := narrow.View(theme, styles, 60)

    wide := NewWorkersTableModel(nil, nil)
    wideOutput := wide.View(theme, styles, 160)

    if narrowOutput == wideOutput {
        t.Error("empty state should adapt to width")
    }
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./cmd/oro-dash/... -run TestWorkersTableAdaptsToWidth -v
```

Expected: FAIL (compile error — View doesn't accept width parameter yet).

**Step 3: Update View signature and distribute widths proportionally**

In `workers_table.go`:

```go
// View renders the workers table with adaptive column widths.
func (w WorkersTableModel) View(theme Theme, styles Styles, totalWidth int) string {
    if len(w.workers) == 0 {
        return renderEmptyWorkersState(styles, totalWidth)
    }
    return w.renderWorkersTable(theme, styles, totalWidth)
}

func renderEmptyWorkersState(styles Styles, totalWidth int) string {
    msg := "No active workers"
    centered := styles.WorkersCentered.Width(totalWidth).Render(styles.Muted.Render(msg))
    return centered
}

// calculateWorkerColumnWidths distributes total width proportionally across 5 columns.
// Ratios: ID 27%, Status 17%, Bead 27%, Health 13%, Context 13%.
// Minimum floors: ID 12, Status 8, Bead 12, Health 6, Context 6.
func calculateWorkerColumnWidths(totalWidth int) []int {
    ratios := []float64{0.27, 0.17, 0.27, 0.13, 0.13}
    mins := []int{12, 8, 12, 6, 6}
    widths := make([]int, len(ratios))

    // Account for spaces between columns
    usable := totalWidth - (len(ratios) - 1)

    for i, ratio := range ratios {
        w := int(float64(usable) * ratio)
        if w < mins[i] {
            w = mins[i]
        }
        widths[i] = w
    }
    return widths
}

func (w WorkersTableModel) renderWorkersTable(theme Theme, styles Styles, totalWidth int) string {
    var sb strings.Builder

    headers := []string{"Worker ID", "Status", "Assigned Bead", "Health", "Context"}
    headerWidths := calculateWorkerColumnWidths(totalWidth)

    // Render header row
    headerParts := make([]string, 0, len(headers))
    for i, header := range headers {
        style := styles.WorkersCol.
            Width(headerWidths[i]).
            Bold(true).
            Foreground(theme.Primary)
        headerParts = append(headerParts, style.Render(header))
    }
    sb.WriteString(strings.Join(headerParts, " "))
    sb.WriteString("\n")

    // Adaptive separator
    sb.WriteString(strings.Repeat("─", totalWidth))
    sb.WriteString("\n")

    for _, worker := range w.workers {
        row := w.renderWorkerRow(worker, headerWidths, styles)
        sb.WriteString(row)
        sb.WriteString("\n")
    }

    return sb.String()
}
```

**Step 4: Update call site in model.go**

Change `model.go:501-502` from:

```go
case WorkersView:
    workersTable := NewWorkersTableModel(m.workers, m.assignments)
    return workersTable.View(m.theme, m.styles) + "\n" + statusBar
```

To:

```go
case WorkersView:
    workersTable := NewWorkersTableModel(m.workers, m.assignments)
    return workersTable.View(m.theme, m.styles, m.width) + "\n" + statusBar
```

**Step 5: Update snapshot tests for new View signature**

The snapshot tests `TestSnapshot_WorkersView_Empty` and `TestSnapshot_WorkersView_MultipleWorkers` call `View(theme, styles)` without the width parameter. Update them to pass a width:

```go
// In snapshot_test.go, update all calls to WorkersTableModel.View:
// From: wt.View(theme, styles)
// To:   wt.View(theme, styles, 80)
```

Use 80 as the standard snapshot width for deterministic golden files.

**Step 6: Regenerate golden files**

```bash
go test ./cmd/oro-dash/... -run TestSnapshot_WorkersView -update
```

**Step 7: Run tests**

```bash
go test ./cmd/oro-dash/... -v -count=1
```

Expected: PASS

**Step 8: Commit**

```
fix(dash): make workers table width-adaptive
```

---

## Task 6: Fuzzy search with edit distance

**Files:**
- Modify: `cmd/oro-dash/search.go` (add fuzzy matching)
- Test: `cmd/oro-dash/search_test.go`

**Step 1: Write failing test**

```go
func TestFuzzyMatchToleratesTypos(t *testing.T) {
    beads := []protocol.Bead{
        {ID: "oro-abc", Title: "dispatcher autoscale"},
        {ID: "oro-def", Title: "memory consolidation"},
        {ID: "oro-ghi", Title: "worker heartbeat"},
    }
    sm := &SearchModel{}

    // "dispatcer" (typo) should match "dispatcher"
    results := sm.Filter(beads, "dispatcer")
    if len(results) != 1 {
        t.Fatalf("expected 1 fuzzy match for 'dispatcer', got %d", len(results))
    }
    if results[0].ID != "oro-abc" {
        t.Errorf("expected 'oro-abc', got %q", results[0].ID)
    }

    // "heartbeet" (typo) should match "heartbeat"
    results = sm.Filter(beads, "heartbeet")
    if len(results) != 1 {
        t.Fatalf("expected 1 fuzzy match for 'heartbeet', got %d", len(results))
    }
    if results[0].ID != "oro-ghi" {
        t.Errorf("expected 'oro-ghi', got %q", results[0].ID)
    }
}

func TestFuzzyMatchExactStillWorks(t *testing.T) {
    beads := []protocol.Bead{
        {ID: "oro-abc", Title: "dispatcher autoscale"},
        {ID: "oro-def", Title: "memory consolidation"},
    }
    sm := &SearchModel{}

    // Exact match still works
    results := sm.Filter(beads, "memory")
    if len(results) != 1 {
        t.Fatalf("expected 1 exact match for 'memory', got %d", len(results))
    }
    if results[0].ID != "oro-def" {
        t.Errorf("expected 'oro-def', got %q", results[0].ID)
    }
}

func TestFuzzyMatchShortTermsFallBackToExact(t *testing.T) {
    beads := []protocol.Bead{
        {ID: "oro-abc", Title: "dispatcher autoscale"},
        {ID: "oro-def", Title: "memory consolidation"},
    }
    sm := &SearchModel{}

    // Single-char search "d" should match "dispatcher" (exact substring) but not all beads
    results := sm.Filter(beads, "d")
    if len(results) != 1 {
        t.Fatalf("expected 1 match for 'd' (exact substring), got %d", len(results))
    }
    if results[0].ID != "oro-abc" {
        t.Errorf("expected 'oro-abc', got %q", results[0].ID)
    }

    // "z" should match nothing (exact substring)
    results = sm.Filter(beads, "z")
    if len(results) != 0 {
        t.Errorf("expected 0 matches for 'z', got %d", len(results))
    }
}

func TestFuzzyMatchRejectsDistantStrings(t *testing.T) {
    beads := []protocol.Bead{
        {ID: "oro-abc", Title: "dispatcher autoscale"},
    }
    sm := &SearchModel{}

    // "zzzzz" should not match anything
    results := sm.Filter(beads, "zzzzz")
    if len(results) != 0 {
        t.Errorf("expected 0 matches for 'zzzzz', got %d", len(results))
    }
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./cmd/oro-dash/... -run TestFuzzyMatch -v
```

Expected: FAIL ("dispatcer" doesn't substring-match "dispatcher").

**Step 3: Implement fuzzy matching**

In `search.go`, add:

```go
// minEditDistance returns the minimum edit distance between needle and any
// substring of haystack with length len(needle) ± maxDist. This is a sliding
// window Levenshtein that allows fuzzy substring matching.
func minEditDistance(needle, haystack string) int {
    n := len(needle)
    h := len(haystack)

    if n == 0 {
        return 0
    }
    if h == 0 {
        return n
    }

    // Exact substring check first (fast path)
    if strings.Contains(haystack, needle) {
        return 0
    }

    best := n + 1 // worse than any real distance

    // Slide a window of size [n-margin, n+margin] across haystack
    margin := max(1, n/3)
    for winSize := max(1, n-margin); winSize <= min(h, n+margin); winSize++ {
        for start := 0; start+winSize <= h; start++ {
            window := haystack[start : start+winSize]
            d := levenshtein(needle, window)
            if d < best {
                best = d
            }
            if best == 0 {
                return 0
            }
        }
    }

    return best
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
    la, lb := len(a), len(b)
    if la == 0 {
        return lb
    }
    if lb == 0 {
        return la
    }

    // Use single-row optimization: O(min(la,lb)) space
    prev := make([]int, lb+1)
    for j := range prev {
        prev[j] = j
    }

    for i := 1; i <= la; i++ {
        curr := make([]int, lb+1)
        curr[0] = i
        for j := 1; j <= lb; j++ {
            cost := 1
            if a[i-1] == b[j-1] {
                cost = 0
            }
            curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
        }
        prev = curr
    }

    return prev[lb]
}

// fuzzyMaxDistance returns the maximum allowed edit distance for a search term.
// Allows ~1 typo per 4 characters, minimum 1.
func fuzzyMaxDistance(termLen int) int {
    d := termLen / 4
    if d < 1 {
        return 1
    }
    return d
}
```

Then update `matchesBead` to use fuzzy matching. **Important:** For short terms (1-2 chars), fall back to exact substring — edit distance 1 on a 1-char term matches every character, making search useless:

```go
if len(filters.fuzzyTerms) > 0 {
    searchableText := strings.ToLower(fmt.Sprintf("%s %s", bead.ID, bead.Title))
    for _, term := range filters.fuzzyTerms {
        // Short terms: exact substring only (fuzzy is meaningless at 1-2 chars)
        if len(term) <= 2 {
            if !strings.Contains(searchableText, term) {
                return false
            }
            continue
        }
        maxDist := fuzzyMaxDistance(len(term))
        if minEditDistance(term, searchableText) > maxDist {
            return false
        }
    }
}
```

**Step 4: Run tests**

```bash
go test ./cmd/oro-dash/... -run TestFuzzyMatch -v
```

Expected: PASS

**Step 5: Run all search tests to check no regressions**

```bash
go test ./cmd/oro-dash/... -run TestSearch -v
go test ./cmd/oro-dash/... -run TestMatch -v
go test ./cmd/oro-dash/... -run TestFilter -v
go test ./cmd/oro-dash/... -run TestParse -v
```

Expected: PASS

**Step 6: Commit**

```
feat(dash): add fuzzy search with edit-distance matching
```

---

## Final Verification

After all 6 tasks:

```bash
go test ./cmd/oro-dash/... -race -count=1 -v
```

Expected: all PASS, no races.

Regenerate golden file snapshots if any exist:

```bash
go test ./cmd/oro-dash/... -run TestSnapshot -update
```

Then commit the updated golden files.
