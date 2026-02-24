# Dash Phase 3: Dense List View with Split-Pane Detail

**Date:** 2026-02-23
**Status:** DRAFT (post adversarial review — gaps fixed)

## Goal

Add a high-density list view as the default navigation surface for oro-dash. Inspired by Linear's issue list, Beads Viewer's split-pane layout, and Perles' information density. The list view shows 3-4x more beads on screen than the current kanban board while preserving quick access to full detail via a side-by-side split pane.

## Design Philosophy

**Density without clutter.** Every character on screen communicates. One line per bead, grouped by status, with a persistent detail pane that follows the cursor. The board (kanban) becomes a secondary view for spatial thinkers — the list is for velocity.

**Two-tier detail.** Quick scanning happens in the split-pane detail (condensed, expandable sections). Deep inspection happens in the full-screen detail view (existing 6-tab interface). Enter escalates from quick to deep.

**Responsive degradation.** On wide terminals (>120), the split pane shows comfortably. On narrow terminals (<100), the detail pane auto-hides and Enter opens a full-screen detail instead.

## Architecture

### View Hierarchy (Post Phase 3)

`ListView` is appended to the enum (not inserted at 0) to avoid renumbering existing ViewType constants. The default view is changed in `newModel()`.

```go
// Existing (unchanged values):
// BoardView    ViewType = 0
// InsightsView ViewType = 1
// DetailView   ViewType = 2
// SearchView   ViewType = 3
// HelpView     ViewType = 4
// HealthView   ViewType = 5
// WorkersView  ViewType = 6
// TreeView     ViewType = 7

// NEW (appended):
// ListView     ViewType = 8
```

| Key | ViewType | View | Notes |
|-----|----------|------|-------|
| (default) | `ListView` (8) | Split-pane list + detail | NEW — primary navigation |
| `b` | `BoardView` (0) | Kanban board | Demoted from default, unchanged enum value |
| `i` | `InsightsView` (1) | Graph metrics | Unchanged |
| Enter | `DetailView` (2) | Full-screen 6-tab detail | Unchanged |
| `/` | `SearchView` (3) | Fuzzy search overlay | Unchanged |
| `?` | `HelpView` (4) | Context-aware help | Updated with list bindings |
| `H` | `HealthView` (5) | System health | Unchanged |
| `w` | `WorkersView` (6) | Workers table | Unchanged |
| `a` | `TreeView` (7) | Hierarchical tree | Unchanged |

### Navigation Return ("Home View")

All `esc`/`backspace` handlers that currently hardcode `m.activeView = BoardView` must be updated to use a `previousNavView` pattern:

```go
// Add to Model struct:
previousNavView ViewType  // tracks which view launched detail/search/insights

// Before entering detail/search/insights, store the current view:
m.previousNavView = m.activeView
m.activeView = DetailView

// On esc/back, return to the launching view:
m.activeView = m.previousNavView
```

**Hardcoded esc handlers to update** (found by adversarial review):
- `model.go` — `handleDetailViewKeys` esc case
- `model.go` — `handleInsightsViewKeys` esc case
- `model.go` — `handleSearchViewKeys` esc case
- `health.go` — esc handler
- `tree.go` — esc handler
- `workers_table.go` — esc handler (if applicable)

For health/tree/workers, esc should return to `ListView` (the new home). Use `m.activeView = ListView` explicitly, since these views are navigated from the home view.

**File changes:**
- NEW: `cmd/oro-dash/list.go` — ListView model, rendering, navigation
- MODIFY: `cmd/oro-dash/model.go` — default view, view routing, key dispatch, previousNavView tracking, esc handlers
- MODIFY: `cmd/oro-dash/theme.go` — list-specific styles, fix initCommonStyles no-op
- MODIFY: `cmd/oro-dash/help.go` — list view help bindings, getViewName, getHelpBindingsForView
- MODIFY: `cmd/oro-dash/health.go` — esc handler update
- MODIFY: `cmd/oro-dash/tree.go` — esc handler update

### Data Model

```go
type ListModel struct {
    groups           []listGroup        // status-grouped beads
    cursor           int                // global cursor position across all visible rows
    focusPane        listPane           // ListPane or DetailPane
    detailBead       *protocol.Bead     // currently previewed bead (follows cursor)
    expandedSections map[string]bool    // which detail sections are expanded (global, not per-bead)
    viewport         viewport.Model     // detail pane scrollable viewport
    splitRatio       float64            // 0.55 default, adjustable with </> keys
    filter           listFilter         // active quick filter (none, open, closed, ready)
    width, height    int                // pane dimensions (from parent Model via WindowSizeMsg)

    // Worker/assignment data needed for Worker and Ctx% columns
    workers          []WorkerStatus     // from workerDataMsg
    assignments      map[string]string  // beadID -> workerID, from workerDataMsg
}

type listGroup struct {
    label     string           // "In Progress", "Ready", "Blocked", "Done"
    status    string           // protocol status value
    beads     []protocol.Bead
    collapsed bool
    color     lipgloss.Color   // group header color (matches kanban column colors)
}

type listPane int
const (
    ListPane   listPane = iota
    DetailPane
)

type listFilter int
const (
    FilterNone   listFilter = iota
    FilterOpen              // open + in_progress
    FilterClosed
    FilterReady             // open + not blocked
)
```

### Data Flow

1. `beadsMsg` arrives (same 2s tick + fsnotify as today)
2. `Model.Update()` stores beads on `m.beads`, then calls `m.listModel.updateBeads(m.beads)` to partition into groups by status, sort by priority
3. `workerDataMsg` arrives — `Model.Update()` stores on `m.workers`/`m.assignments`, then calls `m.listModel.updateWorkers(m.workers, m.assignments)` for row Worker/Ctx% columns
4. `tea.WindowSizeMsg` arrives — `Model.Update()` calls `m.listModel.resize(width, height)` to update responsive layout
5. Cursor position preserved across refreshes (matched by bead ID, not index)
6. `detailBead` updated to match current cursor position
7. Detail pane re-renders with new bead data (includes worker info from `assignments`/`workers` join)

No new data fetching required — the list view uses the same `[]protocol.Bead` and `[]WorkerStatus` already fetched. Worker/context data for the detail pane is derived by joining `detailBead.ID` with `assignments` and `workers`.

### Debounce for Detail Pane

When the user holds `j/k` for rapid cursor movement, the detail pane should not re-render on every keystroke. Use a debounce pattern:

```go
// On j/k keypress in list pane:
m.listModel.detailDebounceTimer = time.AfterFunc(50*time.Millisecond, func() {
    p.Send(detailUpdateMsg{beadID: currentBeadID})
})
// Cancel previous timer on each keypress
```

Alternatively, compute `detailBead` at render time (in `View()`) by looking up the cursor bead. This is cheaper than async — the data is already in memory. **Preferred approach: compute in View(), no debounce needed.** The detail pane renders from in-memory data; no fetch happens on cursor movement.

---

## List Row Format

Each bead occupies exactly **one terminal line**:

```
StatusIcon [Priority] ID          Title (truncated)        Worker  Ctx%
──────────────────────────────────────────────────────────────────────
●          [P0]       oro-evtf    Flaky QG test fix        w-2     34%
           [P1]       oro-pm5m    Kill workers on shutdown  —       —
⊥          [P2]       oro-frg2.5  Skip mgr in hooks        —       —
✓          [P2]       oro-1eo8    Preserve worktrees       —       —
```

### Column Layout

| Column | Width | Content | Styling |
|--------|-------|---------|---------|
| Status icon | 2 chars | `●` in_progress (amber), ` ` ready, `⊥` blocked (red), `✓` done (green) | Foreground: status color |
| Priority | 5 chars | `[P0]`–`[P4]` | Foreground: priority color, bold |
| ID | 12 chars | Bead ID | Foreground: ColorMutedFg |
| Title | flexible | Truncated to fill remaining space | Foreground: ColorFg |
| Worker | 5 chars | Worker ID or `—` | Foreground: ColorMutedFg |
| Context % | 4 chars | `34%` or `—` | Foreground: health color (green/amber/red) |

### Responsive Column Hiding

| Terminal width | Visible columns |
|----------------|-----------------|
| > 120 cols | All columns |
| 100–120 cols | Hide Context % |
| 80–100 cols | Hide Worker + Context % |
| < 80 cols | Hide Worker + Context %, truncate ID to 8 chars |

### Group Headers

```
── In Progress (2) ──────────────────────────────
```

- Styled with `ColorMutedFg` foreground
- Repeated `─` fill to pane width
- Count in parentheses
- Group header color matches kanban column color (e.g., amber for In Progress)
- Collapsible: `Space` on a group header toggles collapsed state
- When collapsed, shows group label + count only, beads hidden

### Group Order

1. **In Progress** — active work first (amber)
2. **Ready** — actionable next (purple)
3. **Blocked** — needs attention (red)
4. **Done** — recent completions, max 10 shown (green)

### Active Row Highlighting

The cursor row gets:
- `Background(theme.ColorFocus)` — same as active card in board view
- Full-width highlight (fills the list pane width)
- No other rows have background — only the cursor row

---

## Detail Pane (Hybrid Condensed + Expandable)

The right pane (40-45% width) shows a condensed overview of the cursor's bead:

```
┌─ Detail ──────────────────────────────┐
│ oro-pm5m · P1 bug · ready             │
│                                       │
│ Kill managed worker processes         │
│ during dispatcher shutdown            │
│                                       │
│ ▸ Acceptance ─────────────────────── │
│   oro stop kills child processes      │
│   verified by ps aux                  │
│                                       │
│ ▸ Worker ─────────────────────────── │
│   Unassigned                          │
│                                       │
│ ▾ Dependencies ───────────────────── │
│                                       │
│ ▸ Notes ──────────────────────────── │
│   (design notes if present)           │
│                                       │
│     Enter → full detail   </> resize │
└───────────────────────────────────────┘
```

### Sections

| Section | Default State | Content | Visibility |
|---------|--------------|---------|------------|
| Header | Always visible | ID + priority badge + type badge + status badge | Always |
| Description | Always visible | Title and/or description text | Always |
| Acceptance | Expanded | Acceptance criteria text | Always (even if empty — shows "No acceptance criteria") |
| Worker | Expanded if worker assigned, collapsed if unassigned | Worker ID, context %, heartbeat age, status | Always |
| Dependencies | Collapsed | Blocks/blocked-by list with status icons | Only if deps exist |
| Notes | Collapsed | Design/notes field content | Only if non-empty |

### Section Indicators

Following standard tree widget convention:
- `▾` = expanded (content visible below, pointing down = "open")
- `▸` = collapsed (content hidden, pointing right = "closed")
- Section headers styled with `ColorMutedFg`, `Bold(true)`

### Interactions in Detail Pane

- `j/k` scrolls the viewport (when content exceeds pane height)
- `Space` toggles expand/collapse on the section nearest to the scroll position
- `Enter` opens full-screen detail view (existing 6-tab interface)
- `Tab` or `Esc` returns focus to list pane

### Focus Indicators

- **List pane focused**: list border uses `ColorBorder`, detail border uses dimmer border (e.g., `ColorBorder` at 50% opacity → use a darker hex)
- **Detail pane focused**: detail border uses `Primary` color, list border uses `ColorBorder`

New theme tokens:
```go
ColorBorderDim lipgloss.Color  // Dimmer border for unfocused pane: "#2A2D30"
```

---

## Keyboard Navigation

### ListView — List Pane Focused

| Key | Action |
|-----|--------|
| `j` / `k` / `↑` / `↓` | Move cursor (detail follows) |
| `Space` | Collapse/expand group header (when cursor is on header) |
| `Enter` | Open full-screen detail (6-tab view) |
| `Tab` | Move focus to detail pane |
| `<` / `>` | Resize split ratio (shrink/grow list pane by 5%) |
| `b` | Switch to board view |
| `i` | Switch to insights view |
| `w` | Switch to workers view |
| `H` | Switch to health view |
| `a` | Switch to tree view |
| `/` | Open search overlay |
| `o` | Toggle filter: open beads only |
| `c` | Toggle filter: closed beads only |
| `r` | Toggle filter: ready (unblocked) only |
| `y` | Copy bead ID to clipboard |
| `h` | No-op (reserved, avoid muscle-memory confusion from board) |
| `l` | Move focus to detail pane (alias for Tab) |
| `?` | Help overlay |
| `q` / `Ctrl+C` | Quit |

**`Space` on non-header rows:** No-op. `Space` only toggles group collapse when the cursor is on a group header row. On a bead row, `Space` does nothing in the list pane.

### ListView — Detail Pane Focused

| Key | Action |
|-----|--------|
| `j` / `k` / `↑` / `↓` | Scroll detail viewport |
| `Space` | Toggle section expand/collapse |
| `Enter` | Open full-screen detail (6-tab view) |
| `Tab` / `Esc` | Return focus to list pane |
| `?` | Help overlay |
| `q` / `Ctrl+C` | Quit |

### Quick Filter Behavior

Filters are toggles — pressing the same key again clears the filter:
- `o` → show open + in_progress (press again → clear)
- `c` → show closed only (press again → clear)
- `r` → show ready/unblocked only (press again → clear)

Active filter shown in status bar: `filter: open` / `filter: ready` / etc.

### Cursor Persistence

When beads refresh (2s tick), the cursor re-positions to the same bead ID. If the bead moved groups (e.g., became in_progress), the cursor follows it to the new group. If the bead disappeared (closed by another agent), the cursor moves to the nearest remaining bead.

---

## Responsive Layout

### Terminal Width Breakpoints

| Width | Layout |
|-------|--------|
| > 120 cols | Split-pane: list (55%) + detail (45%) |
| 100–120 cols | Split-pane: list (60%) + detail (40%), hide Context % column |
| 80–100 cols | List only (full width), no detail pane. Enter opens full-screen detail. |
| < 80 cols | List only, truncated columns, minimal chrome |

**Focus recovery on resize:** When terminal width drops below 100 cols and `focusPane == DetailPane`, automatically reset to `focusPane = ListPane`. The detail pane disappears; focus must not be orphaned.

### Split Ratio Adjustment

`<` and `>` adjust `splitRatio` in 5% increments, clamped to `[0.35, 0.75]`. The ratio persists for the session but is not saved to disk.

### Terminal Height

If height < 20 rows, the status bar collapses to minimal (same as today). Group headers still render — they're only 1 line each.

---

## Status Bar Updates

The status bar gains a filter indicator when a quick filter is active:

```
● oro: running │ w: 2/3 │ 3 ready 2 wip │ filter: ready │  b:board /search ?help q:quit
```

When no filter is active, the filter segment is omitted.

---

## Premortem Analysis

### Decision: List as default view

| Category | Risk | Severity | Mitigation |
|----------|------|----------|------------|
| Tiger | Users accustomed to board are disoriented | Low | Board is one key away (`b`). Status bar shows current view. Help overlay lists all views. |
| Elephant | Users never discover the board exists | Low | `b` shown in status bar hints. Help overlay. |
| Paper tiger | "Users won't like change" | — | Board still exists, just not default. |

### Decision: Side-by-side split layout

| Category | Risk | Severity | Mitigation |
|----------|------|----------|------------|
| Tiger | Narrow terminals (<100) make both panes unusable | Medium | Auto-collapse to list-only on <100 cols. Responsive degradation. |
| Elephant | Detail pane re-rendering on every j/k keystroke causes lag | Medium | Detail computed at render time from in-memory data (no async fetch). All bead + worker data already loaded. No debounce needed. |
| Tiger | Two-pane focus model is confusing | Low | Clear visual indicator: active pane border changes color. Tab toggle is simple. |

### Decision: Hybrid expandable detail sections

| Category | Risk | Severity | Mitigation |
|----------|------|----------|------------|
| Tiger | Section expand/collapse state resets when cursor moves | Low | Section state is per-bead (store in map[beadID]sectionState) or global (all beads share same expanded set). Choose global — simpler, consistent. |
| Elephant | Scrolling within detail + section collapse creates confusing position jumps | Medium | Reset viewport scroll to top when cursor moves to a new bead. Only preserve scroll when scrolling within the same bead. |
| Paper tiger | "Too complex to implement" | — | Each section is just a conditional string block. Expand/collapse is a bool toggle. |

### Decision: Group by status, sort by priority

| Category | Risk | Severity | Mitigation |
|----------|------|----------|------------|
| Tiger | Done group grows unbounded, pushing other groups off screen | Medium | Cap Done at 10 most recent. Collapsed by default after 10+. |
| Tiger | Epic hierarchy lost in flat grouping | Low | Tree view (`a`) preserves epic grouping. List is for status-oriented workflow. |
| Paper tiger | "Grouping adds overhead" | — | 4 group headers = 4 lines. Negligible. |

### Decision: Quick filters (o, c, r)

| Category | Risk | Severity | Mitigation |
|----------|------|----------|------------|
| Tiger | `c` for closed could conflict with future "create" action | Low | Filters are list-view-only. Create would be a command palette action (future). |
| Tiger | User forgets filter is active, sees partial data | Medium | Filter shown prominently in status bar. Active filter badge in list pane header. |

---

## Implementation Tasks (High-Level)

### Task 0: Pre-requisite fixes (from adversarial review)
- **Fix `initCommonStyles` no-op bug**: `theme.go:174` has `_, _ = s.initCommonStyles, theme` which never calls the function. Change to `s.initCommonStyles(theme)`. Without this, `styles.Muted`, `styles.Bold`, `styles.Primary` are zero-value and all list view rendering using these styles will be invisible.
- Test: existing snapshot tests should still pass (or update if styles now render differently)

### Task 1: ListModel scaffold + wiring
- Create `list.go` with `ListModel` struct, `NewListModel()`, empty `View()`, `Update()`
- **Append** `ListView` to ViewType enum (do NOT insert at 0 — keep existing values stable)
- Add `listModel ListModel` field to `Model` struct
- Wire `beadsMsg` handler to call `m.listModel.updateBeads(m.beads)`
- Wire `workerDataMsg` handler to call `m.listModel.updateWorkers(m.workers, m.assignments)`
- Wire `tea.WindowSizeMsg` to call `m.listModel.resize(width, height)`
- Set `ListView` as default in `newModel()`
- Add `previousNavView ViewType` field to `Model` struct
- **Update all esc/back handlers** to use `m.previousNavView` or `ListView`:
  - `model.go` handleDetailViewKeys esc → `m.activeView = m.previousNavView`
  - `model.go` handleSearchViewKeys esc → `m.activeView = m.previousNavView`
  - `model.go` handleInsightsViewKeys esc → `m.activeView = m.previousNavView`
  - `health.go` esc → `m.activeView = ListView`
  - `tree.go` esc → `m.activeView = ListView`
- Before entering detail/search/insights: `m.previousNavView = m.activeView`
- Test: view switching roundtrips (list → detail → esc → list, board → detail → esc → board)

### Task 2: List row rendering
- Implement `renderRow()` with all columns (icon, priority, ID, title, worker, ctx%)
- **Worker/ctx% columns**: look up worker via `m.assignments[bead.ID]` → find worker in `m.workers` → extract ctx%
- Implement `renderGroupHeader()` with styled separators
- Implement `groupBeads()` to partition by status and sort by priority
- **Cap Done group at 10 most recently closed** (sort by closed date descending, take first 10)
- **Empty groups are hidden entirely** — no group header rendered for zero-bead groups
- Test: snapshot test with sample beads, verify Done cap, verify empty group hiding

### Task 3: Cursor navigation + group collapse
- Implement j/k navigation with group awareness (skip collapsed groups, skip group headers when navigating)
- `Space` on group header toggles collapse; `Space` on bead row is no-op
- Implement cursor persistence across data refresh (match by bead ID, not index; fallback to nearest remaining bead)
- **Handle edge case**: all visible beads filtered out or in collapsed groups → show "No beads match" message
- Test: navigation tests, collapse tests, cursor persistence tests

### Task 4: Detail pane rendering
- Implement split-pane layout with `lipgloss.JoinHorizontal`
- Render condensed detail: header, description, expandable sections
- **Detail computed at render time** in `View()` — looks up cursor bead from groups, joins with workers/assignments for worker section. No async fetch, no debounce needed.
- Reset viewport scroll to top when cursor moves to a new bead
- Sections: Acceptance (expanded), Worker (expanded if assigned), Dependencies (collapsed), Notes (collapsed if non-empty, hidden if empty)
- Section indicators follow convention: `▾` expanded, `▸` collapsed
- Test: detail content matches cursor bead, worker data shown when assigned

### Task 5: Detail pane interactivity
- Implement Tab/l to switch focus between panes, Esc in detail returns to list
- Focus indicator: active pane border uses `Primary`, inactive uses `ColorBorderDim`
- Implement section expand/collapse with Space (global state, not per-bead)
- Implement viewport scrolling with j/k in detail pane
- Implement </> split ratio adjustment (5% increments, clamped [0.35, 0.75])
- Test: focus switching, section toggle, resize

### Task 6: Quick filters
- Implement o/c/r filter toggles
- Filter persists across data refresh
- Status bar shows active filter: `filter: open` / `filter: ready`
- **Handle filter + collapse interaction**: if all visible beads in collapsed groups, show message
- Test: filter reduces visible beads, toggle clears, filter indicator in status bar

### Task 7: Responsive layout
- Implement width-based column hiding (>120: all, 100-120: hide ctx%, 80-100: hide worker+ctx%, <80: truncate ID)
- Implement auto-collapse to list-only on narrow terminals (<100 cols)
- **Focus recovery**: if width drops below 100 and `focusPane == DetailPane`, reset to `ListPane`
- Test: different widths produce correct layouts, focus recovery on narrow resize

### Task 8: Keyboard shortcuts + help
- Wire Enter to full-screen detail view (sets `previousNavView = ListView`)
- Wire b to board view
- Wire y to clipboard copy (use `golang.design/x/clipboard` or exec `pbcopy` on macOS)
- **Add `ListView` case to**:
  - `helpHintsForView()` in model.go
  - `getHelpBindingsForView()` in help.go
  - `getViewName()` in help.go
- Add list view help bindings (j/k nav, Tab focus, Space collapse, Enter detail, etc.)
- Test: all key bindings functional, help shows correct bindings for list view

### Task 9: Theme additions
- Add `ColorBorderDim` token (`#2A2D30`)
- Add list-specific styles: `ListRow`, `ListActiveRow`, `ListGroupHeader`, `ListDetailBorder`, `ListDetailBorderDim`
- Verify `initCommonStyles` fix from Task 0 — `styles.Muted` renders correctly
- Test: theme token tests, style render tests

### Task 10: Polish + snapshot tests
- Visual regression snapshots for list view (all variants: normal, filtered, collapsed groups, narrow, detail focused)
- Edge cases: 0 beads, 1 bead, all same status, all blocked, all done
- Status bar filter indicator
- Final quality gate: `go test ./cmd/oro-dash/... -race -count=1`

---

## Edge Cases

| Scenario | Behavior |
|----------|----------|
| 0 beads | Show empty state: "No beads yet · create with: bd create" |
| 1 bead | Single group with 1 row, detail pane shows that bead |
| All beads same status | Only one group rendered, others hidden |
| All beads blocked | Only "Blocked" group visible |
| All beads done | Only "Done" group visible (capped at 10) |
| Filter hides all beads | Show "No beads match filter · press o/c/r to clear" |
| All groups collapsed | Show group headers only, detail pane shows last-selected bead or empty |
| Bead disappears during refresh | Cursor moves to nearest remaining bead by index |
| Terminal resizes during use | Responsive breakpoints applied immediately; focus recovered if detail pane disappears |
| Very long bead title | Truncated with ellipsis to fit list pane width |
| Bead with no acceptance criteria | Acceptance section shows "No acceptance criteria" in muted style |

---

## Adversarial Review Findings (Resolved)

The adversarial review (2026-02-23) found 3 critical and 9 major gaps. All have been addressed:

| # | Finding | Resolution |
|---|---------|------------|
| 1 | ViewType enum renumbering breaks esc handlers | Append ListView (don't insert); update all esc handlers |
| 2 | Detail esc hardcodes BoardView | Added `previousNavView` tracking field |
| 3 | Worker/Ctx% data not on protocol.Bead | ListModel carries workers/assignments; join at render time |
| 4 | beadsMsg doesn't propagate to ListModel | Explicit wiring in Task 1 |
| 5 | Detail pane Worker section needs data join | Documented in Task 4 |
| 6 | Space on non-header row unspecified | Explicitly no-op |
| 7 | No WindowSizeMsg propagation | Wired in Task 1 |
| 8 | No debounce for cursor movement | Detail computed at render time (View()), no async fetch needed |
| 9 | Focus orphaned on narrow resize | Focus recovery added to Task 7 |
| 10 | Search/Insights esc hardcode BoardView | Updated to use previousNavView |
| 11 | help/hints/viewName missing ListView | Explicitly added to Task 8 |
| 12 | initCommonStyles never called | Fixed in Task 0 |
| 13 | Done cap not in task | Added to Task 2 |
| 14 | Clipboard dependency | Noted in Task 8 |
| 15 | Section indicators swapped | Fixed to convention (▾=expanded, ▸=collapsed) |
| 16 | h/l keys undefined | h=no-op, l=alias for Tab |
| 17 | Empty groups unspecified | Hidden entirely |
| 18 | Filter+collapse can orphan cursor | "No beads match" message added |

---

## Key Decisions Log

| Decision | Rationale |
|----------|-----------|
| List as default, board via `b` | Linear's core insight: the dense list is the primary navigation surface. Board is a spatial alternative. |
| Side-by-side split (not top-bottom) | Maximizes vertical space for the list (more rows visible). Linear and bv both use horizontal splits. |
| Hybrid expandable detail (not tabs) | Quick scanning needs all-at-once overview. Tabs force sequential discovery. Expandable sections give both density and depth. |
| Group by status (not epic) | Status grouping mirrors the kanban mental model. Epic grouping available via tree view (`a`). |
| Quick filters via single keys | Inspired by bv's `o`/`c`/`r` filters. Fastest possible scope reduction — zero typing required. |
| Responsive degradation at <100 cols | Two panes at <50 cols each are unreadable. Better to give the list full width and use Enter for detail. |
| Global section expand state (not per-bead) | Simpler implementation. Users set their preferred section visibility once; it applies to all beads. |
| Cursor follows bead ID on refresh | Prevents jarring cursor jumps when beads move between groups during live updates. |
| Done group capped at 10 | Prevents unbounded growth. Recent completions are useful context; ancient history is noise. |
| Append ListView to enum (not insert at 0) | Avoids silent breakage of all existing view constants and their consumers. |
| `previousNavView` tracking | Enables correct esc behavior when entering detail from list vs. board. |
| Detail computed at render time | No async fetch needed — all data in memory. Eliminates debounce complexity. |
| `initCommonStyles` fix as Task 0 | Pre-existing bug that would silently break list view. Must fix first. |
