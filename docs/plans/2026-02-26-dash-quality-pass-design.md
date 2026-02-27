# Dashboard Quality Pass Design

**Date**: 2026-02-26
**Status**: Draft
**Covers**: View consolidation, sort strategy, detail data wiring, load-more pagination

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
| ListView | `l` (new shortcut) | Default. Priority+deps ordering. |
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
- Remove `tree.go`, `tree_test.go` source files
- Remove `renderHealthView()` from model.go
- Route `H` and `w` keys to StatusView in all view key handlers
- Add `l` key to return to ListView from all views
- Free `a` key
- Update help bindings for all affected views

## B. Sort Strategy

### BoardView — "What's happening now"

Recency sort (UpdatedAt descending) across **all 4 columns**. Most recently touched
beads float to the top of every column.

Currently only Done column sorts by UpdatedAt (board.go:72-78). Extend the
`slices.SortStableFunc` to Ready, In Progress, and Blocked columns.

### ListView — "What to work on next"

Priority-weighted topological order within each status group:

1. **Topological sort** — prerequisites before dependents. Uses Kahn's algorithm
   already implemented in `graph.go:TopologicalOrder()`.
2. **Priority tiebreaker** — within the same topological level, P0 before P1.
3. **Dependency indicators** — lightweight suffix: `← needs oro-xyz` when
   `len(bead.Dependencies) > 0`.

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
ClosedAt    string `json:"closed_at,omitempty"`
CreatedAt   string `json:"created_at,omitempty"`
Description string `json:"description,omitempty"`
CloseReason string `json:"close_reason,omitempty"`
Owner       string `json:"owner,omitempty"`
```

### Wire existing dispatcher data

WorkerID and ContextPct are already in `m.assignments` and `m.workers`. Copy them
into `BeadDetail` at drill-down time instead of leaving them empty:

```go
// In listDrillDown / drillDownToDetail:
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

The cursor is the `ClosedAt` timestamp of the oldest bead in the current set.
`--closed-before` is an existing bd CLI flag.

### Model state

```go
extraClosed   []protocol.Bead  // beads loaded via "load more"
closedCursor  string           // oldest ClosedAt in current set
hasMoreClosed bool             // true when last batch == 50
loadingMore   bool             // debounce flag
```

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

### Prerequisite

Add `ClosedAt string` to `protocol.Bead` (covered in Section C).

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
| `l` shortcut for ListView | Default view needs a way to return to it |
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
