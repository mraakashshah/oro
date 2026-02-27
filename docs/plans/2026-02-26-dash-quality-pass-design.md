# Dashboard Quality Pass Design

**Date**: 2026-02-26
**Status**: Reviewed (4 adversarial passes)
**Covers**: View consolidation, sort strategy, detail data wiring, load-more pagination

## Implementation Order

**Section C** (protocol fields) → **Section D** (pagination, needs ClosedAt).
**Section A** (view consolidation) and **Section B** (sort strategy) are independent
of each other and of C/D. Recommended parallel tracks: A+B together, then C→D.

## Context

oro-dash has 10 views with overlap, broken sort in tree view, empty detail tabs,
and no pagination for closed beads. This spec addresses all four issues in a single
quality pass.

## A. View Consolidation

### Current State: 10 views

| View | Key | Purpose |
|------|-----|---------|
| ListView | (default) | Dense list with split-pane detail |
| BoardView | `b` | Kanban 4-column board |
| TreeView | `a` | Hierarchical tree (all beads) |
| DetailView | `enter` | Single bead deep-dive |
| SearchView | `/` | Search overlay |
| HelpView | `?` | Keybinding help overlay |
| InsightsView | `i` | Critical path, bottlenecks, triage |
| HealthView | `H` | System health |
| WorkersView | `w` | Workers table |
| StatusView | `s` | Daemon/pane/worker status with sparklines |

### Target State: 7 views (5 navigable + 2 overlays)

| View | Key | Purpose |
|------|-----|---------|
| ListView | `L` (new shortcut) | Default. Priority+deps ordering. |
| BoardView | `b` | Kanban. Recency sort. |
| DetailView | `enter` | Single bead. Tabs populated from bd show + dispatcher. |
| StatusView | `s` / `H` / `w` | Daemon + workers + sparklines. |
| InsightsView | `i` | Graph analysis. |
| SearchView | `/` | Overlay. |
| HelpView | `?` | Overlay. |

### Removals

**TreeView** (`a`): Killed. Broken sort (groups by epic, shows wall of P0 headers).
ListView with topological ordering covers the same use case better.

**HealthView** (`H`): Alias for StatusView. Phase 4 designed this merge but it wasn't
executed. `H` key now routes to StatusView.

**WorkersView** (`w`): Alias for StatusView. Same as above.

### Changes

- Remove `TreeView`, `HealthView`, `WorkersView` from `ViewType` enum
- Delete source files: `tree.go`, `tree_test.go`, `health.go`, `health_test.go`,
  `workers_table.go`, `workers_table_test.go`
- Remove `case HealthView:` and `case WorkersView:` from `model.go:View()` switch
  (calls `renderHealthView()` and `renderWorkersTable()`)
- Remove `case HealthView:` and `case WorkersView:` from `model.go:handleKeyPress()`
  switch (calls `handleHealthViewKeys()` and `handleWorkersViewKeys()`)
- Route `H` and `w` keys to StatusView in all view key handlers
- Add `case "L":` to ALL non-list view key handlers to return to ListView:
  `handleBoardViewKeys()`, `handleInsightsViewKeys()`, `handleStatusViewKeys()`,
  and the detail view key handler
  - Note: lowercase `l` is taken by BoardView vim-style column nav (`l`/`right` →
    `moveToNextColumn()` at model.go:586)
- Remove `treeModel TreeModel` field from Model struct (model.go:172) and all
  `NewTreeModel()` calls (model.go:525, 612)
- Remove `case TreeView:`, `case HealthView:`, `case WorkersView:` branches from
  `helpHintsForView()` (model.go) and `getHelpBindingsForView()` (help.go)
- Free `a` key
- Update help bindings for all affected views
- Delete orphan snapshot golden files in `testdata/` for removed views

### Test Breakage Inventory

Tests that reference removed views and must be updated or deleted:

| File | Tests | Action |
|------|-------|--------|
| `tree_test.go` | All (~8 tests) | Delete with source file |
| `health_test.go` | `TestHealthViewRender`, `TestHealthViewShowsDispatcherData` | Delete with source file |
| `workers_table_test.go` | `TestWorkersTableView`, `TestWorkersTable_EmptyState_DaemonOnline` | Delete with source file |
| `list_test.go` | Tests referencing `TreeView`/`HealthView`/`WorkersView` in escape-key and navigation assertions | Update: remove references to deleted views |
| `model_test.go` | `ViewType` enum tests | Update: remove deleted enum values |
| `status_test.go` | Key binding tests for `H`→HealthView, `w`→WorkersView | Update: change expectations to StatusView |
| `snapshot_test.go` | `WorkersView` snapshot tests | Update: remove or redirect to StatusView |

## B. Sort Strategy

### BoardView — "What's happening now"

Recency sort (UpdatedAt descending) across **all 4 columns**. Most recently touched
beads float to the top of every column.

Currently only Done column sorts by UpdatedAt (board.go:72-78). Extend the
`slices.SortStableFunc` to Ready, In Progress, and Blocked columns.

**Design tension:** The fetch layer sends `--sort priority` for open/in_progress/blocked,
so the 50 beads fetched are the highest-priority. The board then re-sorts by recency.
This is acceptable: within the top-50-by-priority, recency ordering shows "what's active
now" without losing important beads. The fetch layer acts as a priority ceiling; the
view layer orders within that ceiling by activity.

### ListView — "What to work on next"

Priority-weighted topological order within each status group:

1. **Topological sort** — prerequisites before dependents. Uses Kahn's algorithm
   already implemented in `graph.go:TopologicalOrder()`.
2. **Priority tiebreaker** — within the same topological level, P0 before P1.
3. **Dependency indicators** — lightweight suffix: `← needs oro-xyz` when
   bead has blocking dependencies (`dep.Type == "blocks"`).

#### Data structure bridge

`TopologicalOrder()` operates on `[]BeadWithDeps` (graph.go) and returns `[]string`
(ordered bead IDs). `groupBeads()` operates on `[]protocol.Bead`. The bridge must
handle a type mismatch:

- `protocol.Bead.Dependencies` is `[]protocol.Dependency` (struct with IssueID,
  DependsOnID, Type), **not** `[]string`
- `BeadWithDeps.DependsOn` is `[]string` (bead IDs)

The conversion follows the same pattern as `buildInsightsModel()` (model.go:786-791):

```go
// In list.go, new function: topoSortBeads(beads []protocol.Bead) []protocol.Bead
func topoSortBeads(beads []protocol.Bead) []protocol.Bead {
    // 1. Convert protocol.Bead → BeadWithDeps for the graph
    //    Extract DependsOnID from []Dependency (same as buildInsightsModel)
    bwd := make([]BeadWithDeps, len(beads))
    for i, b := range beads {
        var dependsOn []string
        for _, dep := range b.Dependencies {
            if dep.Type == "blocks" {
                dependsOn = append(dependsOn, dep.DependsOnID)
            }
        }
        bwd[i] = BeadWithDeps{
            ID:        b.ID,
            Status:    b.Status,
            Priority:  b.Priority,
            DependsOn: dependsOn,
        }
    }

    // 2. Run topological sort → []string of ordered IDs
    graph := NewDependencyGraph(bwd)
    order, err := graph.TopologicalOrder()
    if err != nil {
        // Cycle detected: fall back to priority-only sort
        slices.SortStableFunc(beads, func(a, b protocol.Bead) int {
            return a.Priority - b.Priority
        })
        return beads
    }

    // 3. Build index map: beadID → position in topo order
    pos := make(map[string]int, len(order))
    for i, id := range order {
        pos[id] = i
    }

    // 4. Sort beads: topo position first, priority tiebreaker
    //    When no beads have dependencies, topo sort returns all IDs in
    //    insertion order → degrades gracefully to priority-only.
    slices.SortStableFunc(beads, func(a, b protocol.Bead) int {
        pa, pb := pos[a.ID], pos[b.ID]
        if pa != pb {
            return pa - pb
        }
        return a.Priority - b.Priority
    })
    return beads
}
```

This function replaces the current priority-only sort in `groupBeads()`. Called
per-group after grouping so topo order is within each status section.

**Important type details:**
- `protocol.Dependency` has `IssueID`, `DependsOnID`, `Type` fields (types.go:9-13)
- Only `"blocks"` type dependencies are used for ordering (matches insights model)
- `BeadWithDeps.DependsOn` expects `[]string` of bead IDs

Example rendering:
```
▼ Ready (4)
  [P0] □ oro-abc  Define auth types
  [P0] □ oro-def  Implement token gen          ← needs oro-abc
  [P1] □ oro-ghi  Add rate limiting
  [P2] □ oro-jkl  Update docs
```

### Fetch Layer Defaults

| Status | Sort flag | Rationale |
|--------|-----------|-----------|
| open | `--sort priority` | Most critical beads in the 50-bead window |
| in_progress | `--sort priority` | Most critical first |
| blocked | `--sort priority` | Most critical first |
| closed | `--sort closed --reverse` | Most recently closed (already implemented) |

The fetch layer determines *which* 50 beads we get. The view layer re-sorts for display.

## C. Detail View Data Wiring

### Problem

Drill-down constructs `BeadDetail` by hand-copying 5 fields from `protocol.Bead`.
Fields like `Description`, `CloseReason`, `WorkerID` exist on `BeadDetail` but are
never populated. The `bd show <id> --json` API exists but is never called.

### Solution: Async fetch on drill-down

```
enter pressed
  → show skeleton detail (ID, title, status — from Bead already in memory)
  → fire fetchBeadDetailCmd(id)     ← NEW: calls bd show <id> --json
  → fire fetchWorkerEventsCmd(id)   ← existing
  → fire fetchWorkerOutputCmd(id)   ← existing, active workers only

beadDetailMsg arrives
  → populate description, close_reason, created_at, owner, dependents
  → re-render Overview tab with full data
```

#### Implementation: fetchBeadDetail

New function in `fetch.go`. Note: `bd show <id> --json` returns a JSON **array**
`[{...}]` (same as bd list), not a single object. Parse accordingly:

```go
// fetchBeadDetail calls `bd show <id> --json` and returns the first bead.
func fetchBeadDetail(ctx context.Context, beadID string) (*protocol.Bead, error) {
    ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
    defer cancel()
    cmd := exec.CommandContext(ctx, "bd", "show", beadID, "--json")
    out, err := cmd.Output()
    if err != nil {
        return nil, fmt.Errorf("bd show: %w", err)
    }
    var beads []protocol.Bead
    if err := json.Unmarshal(out, &beads); err != nil {
        return nil, fmt.Errorf("parse bd show JSON: %w", err)
    }
    if len(beads) == 0 {
        return nil, fmt.Errorf("bd show returned empty array")
    }
    return &beads[0], nil
}
```

New message type in `model.go`:

```go
type beadDetailMsg struct {
    bead *protocol.Bead
    err  error
}

func fetchBeadDetailCmd(id string) tea.Cmd {
    return func() tea.Msg {
        bead, err := fetchBeadDetail(context.Background(), id)
        return beadDetailMsg{bead: bead, err: err}
    }
}
```

#### beadDetailMsg handler — merge strategy

**Critical**: `bd show --json` uses expanded dependency format (`id`, `dependency_type`)
that does NOT match `protocol.Dependency` JSON tags (`issue_id`, `depends_on_id`, `type`).
Parsing `bd show` into `[]protocol.Bead` silently drops all dependencies.

Therefore the handler must **merge**, not replace. Overlay only the new scalar fields
from `bd show` onto the existing `BeadDetail` (which already has correct dependencies
from the in-memory `protocol.Bead` used to construct the skeleton):

```go
case beadDetailMsg:
    if msg.err != nil || m.beadDetail == nil {
        break
    }
    // Overlay fields that bd show provides but bd list doesn't
    b := msg.bead
    m.beadDetail.Description = b.Description
    m.beadDetail.CloseReason = b.CloseReason
    m.beadDetail.Owner = b.Owner
    // Do NOT overwrite Dependencies — bd show uses different JSON schema
    // Dependencies are already correct from the skeleton (populated from bd list data)
```

This avoids silent data loss from the JSON key mismatch between `bd list` and `bd show`.

### Tab visibility by bead status

| Tab | open/in_progress/blocked | closed |
|-----|--------------------------|--------|
| Overview | Show | Show |
| Worker | Show (from dispatcher) | Hide |
| Diff | Hide (future: needs dispatcher directive) | Hide |
| Deps | Show | Show |
| Memory | Hide (future: needs dispatcher to expose prompt) | Hide |
| Output | Show (from dispatcher socket) | Hide |

For closed beads, only Overview and Deps tabs are shown. No empty placeholders.

### protocol.Bead field additions

Add fields that `bd list --json` already returns but the struct doesn't capture:

```go
// protocol.Bead — add these fields:
ClosedAt    string `json:"closed_at,omitempty"`
CreatedAt   string `json:"created_at,omitempty"`
Description string `json:"description,omitempty"`
CloseReason string `json:"close_reason,omitempty"`
Owner       string `json:"owner,omitempty"`
```

### protocol.BeadDetail field additions

`BeadDetail` is also missing `Owner`. Add it so drill-down can display the owner:

```go
// protocol.BeadDetail — add this field:
Owner string `json:"owner,omitempty"`
```

### Wire existing dispatcher data

WorkerID and ContextPct are already in `m.assignments` and `m.workers`. Copy them
into `BeadDetail` at drill-down time instead of leaving them empty.

Wire into **all three** drill-down paths:
- `listDrillDown()` (model.go:552-576) — list view enter key (already copies Dependencies)
- `drillDownToDetail()` (model.go:1160-1166) — board view enter key (**add Dependencies,
  Status** to skeleton — currently missing)
- `handleSearchViewKeys` enter path (model.go:633-639) — search result drill-down
  (**add Dependencies, Status** to skeleton — currently missing)

```go
// Insert after BeadDetail construction in both functions:
if workerID, ok := m.assignments[b.ID]; ok {
    beadDetail.WorkerID = workerID
    for _, w := range m.workers {
        if w.ID == workerID {
            beadDetail.ContextPercent = w.ContextPct
            break
        }
    }
}
```

## D. Load-More Pagination

### Problem

`bd list` defaults to `--limit 50`. For closed beads (945 total), user sees only 50.
No way to browse history.

### Design: Cursor-based pagination

```
Initial:    bd list --status closed --sort closed --reverse --json → 50 most recent
Load more:  bd list --status closed --sort closed --reverse --closed-before <cursor> --json → next 50
```

The cursor is the **exact `closed_at` string** from the JSON response of the oldest
bead in the current set (RFC3339 format). Do not reformat or truncate — use the raw
string to avoid off-by-one boundary issues. `--closed-before` is an existing bd CLI
flag that accepts RFC3339.

### Model state

```go
extraClosed   []protocol.Bead  // beads loaded via "load more"
closedCursor  string           // oldest ClosedAt from the LAST batch (not all closed)
hasMoreClosed bool             // true when last batch == 50
loadingMore   bool             // debounce flag
```

**Cursor update rule**: `closedCursor` is set from the **last batch returned by the
load-more fetch**, not from all closed beads in `m.beads`. On initial load, it's the
oldest `ClosedAt` from the first 50 closed beads. On each subsequent load-more, it's
the oldest `ClosedAt` from that batch's response. This prevents duplicate fetching
when `extraClosed` contains older beads from previous loads.

### Tick merge

Every 2s tick replaces `m.beads` via `applyBeadsMsg`. After replacement, merge
`extraClosed` back in. No dedup needed — cursor-based fetch guarantees extraClosed
beads are strictly older than the fetched 50.

```go
func (m Model) applyBeadsMsg(msg beadsMsg) Model {
    m.beads = []protocol.Bead(msg)
    m.beads = append(m.beads, m.extraClosed...)  // preserve loaded history
    // ... recompute counts, update sub-models
}
```

### UI

- `M` key triggers load-more in both BoardView and ListView
- Bottom of Done column / closed section shows `[M] load more` when `hasMoreClosed`
- Shows `loading...` when `loadingMore` is true
- Remove hardcoded `[:10]` cap in `list.go:groupBeads`

### Prerequisite (HARD dependency — must be implemented first)

Add `ClosedAt string` to `protocol.Bead` (covered in Section C). Without this field,
the cursor value will always be empty string and pagination silently breaks.

**Verify**: Run `bd list --status closed --json | jq '.[0]'` to confirm `closed_at`
is present in the JSON output. If not, the field addition alone won't help — bd CLI
must be checked/updated to emit `closed_at`.

## Out of Scope

- **GitDiff tab**: Needs dispatcher `worker-diff` directive or direct worktree access.
  Filed as follow-up.
- **Memory tab**: Needs dispatcher to expose worker prompt context. Not persisted today.
  Filed as follow-up.
- **Persistent sort config**: Sort is per-view with sensible defaults. No config file.
- **Pagination for non-closed statuses**: Unlikely to exceed 50 open/in_progress/blocked.

## Key Decisions

| Decision | Rationale |
|----------|-----------|
| Kill TreeView | Broken sort, overlaps with ListView + topo ordering |
| H/w alias to StatusView | Phase 4 designed this, just executing |
| `L` (uppercase) shortcut for ListView | Default view needs a return key; lowercase `l` conflicts with BoardView vim column nav |
| Board = recency, List = priority+deps | Two distinct mental models, no cycling needed |
| Fetch bd show on drill-down | Lazy load — don't fetch detail for all 200 beads |
| Hide tabs for closed beads | No active worker = no runtime data, don't show empty |
| Cursor-based pagination | bd CLI has --closed-before, no offset needed |
| Remove [:10] cap | Section collapse already handles density |

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| TopologicalOrder has cycles | Crash or infinite loop | Already returns ErrCircularDependency; fall back to priority-only sort |
| bd show latency on drill-down | Sluggish detail view | Show skeleton immediately, populate async. bd show is local SQLite, should be <50ms |
| Tick clobbering extraClosed | Loaded history disappears | Merge extraClosed in applyBeadsMsg after replacement |
| Removing TreeView breaks muscle memory | Users expect `a` key | Free the key, no alias needed — tree was rarely useful |
| TopologicalOrder ↔ groupBeads type mismatch | Integration bug | Bridge function converts protocol.Bead → BeadWithDeps → []string → sorted []protocol.Bead (see Section B) |
| Deleting 6 source files breaks compilation | Build fails | Test breakage inventory (Section A) lists every affected test; delete in atomic commit |
| bd show --json returns array not object | Parse error | fetchBeadDetail parses as []protocol.Bead, takes first element (see Section C) |
| bd list --json may not include closed_at | Empty cursor, broken pagination | Verify with `bd list --json \| jq '.[0]'` before implementing pagination |
| Board fetch-by-priority vs display-by-recency | P3 beads above P0 in board | Acceptable: fetch acts as priority ceiling, view sorts by activity within that set |
