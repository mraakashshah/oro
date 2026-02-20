# Dash Phase 2: Linear-Quality Visual Design Refresh

> **For Claude:** Use `executing-plans` skill to implement this plan task-by-task.

**Goal:** Elevate oro-dash from functional to polished — Linear-level visual hierarchy, consistent spacing, discoverable keyboard shortcuts, and meaningful empty/loading states throughout all views.

**Architecture:** All changes are confined to `cmd/oro-dash/`. The primary modification surface is `theme.go` (spacing system, color refinement), with targeted changes to each view file (`board.go`, `detail.go`, `workers_table.go`, `help.go`, `health.go`, `insights.go`, `model.go`). No new views are added — this is a polish pass on the existing seven views. Phase 1 data correctness (oro-81nf) must be complete before starting because design decisions here are made against correct data shapes.

**Tech Stack:** Go, Bubble Tea (`github.com/charmbracelet/bubbletea`), lipgloss (`github.com/charmbracelet/lipgloss`), bubbles (`github.com/charmbracelet/bubbles`)

---

## Design Philosophy

Linear's TUI-equivalent design language is characterized by:

1. **Purposeful restraint** — one accent color per semantic meaning, not decorative color
2. **Breathing room** — content is never crowded; padding creates hierarchy without borders
3. **Invisible chrome** — borders and separators recede, content advances
4. **Keyboard-first discoverability** — every action is findable without reading docs
5. **Meaningful states** — empty and loading are first-class UI states, not afterthoughts

In a TUI context (no pixels, only cells), the analogues are:

| GUI concept | TUI analogue |
|-------------|--------------|
| 8px base grid | 1-cell base unit; 2 cells for comfortable padding |
| Drop shadows | Border color contrast |
| Hover states | Cursor highlight row |
| Tooltip | Inline status bar hint text |
| Skeleton loader | Spinner character or `…` placeholder text |
| Transition animation | Immediate render (TUI has no animation budget) |

---

## Current State Audit

Reading the actual source (2026-02-20) reveals:

### Spacing / Padding (board.go)
- `Card` style: `Padding(1, 2)` — 1 row top/bottom, 2 cols left/right. Comfortable.
- `ActiveCard`: same padding, background `#3a3a3a`. The background is hard-coded in `renderColumn()`, not in the theme. **Gap: active card background is not themeable.**
- `Column` style: `Padding(1)` — adds 1 cell on all sides *plus* the border. Double-padding on the card-column boundary makes cards look indented. **Gap: column padding + card border creates inconsistent visual rhythm.**

### Color Palette (theme.go)
- Current palette uses 6 semantic colors correctly (Radix-inspired hex values).
- `ColorBorder` is `#3E4347` — visible but not distinct from the active card background `#3a3a3a`. **Gap: active state and border are almost identical.**
- `StatusLabel` style has no color — renders in terminal default foreground. **Gap: status bar elements lack consistent muting.**
- No `ColorSurface` token for elevated/modal surfaces (help overlay, search overlay). Both currently use `ColorBorder` for their borders. **Gap: overlays don't feel "raised."**

### Typography Hierarchy (detail.go, model.go)
- `DetailTitle` is bold + primary color: correct.
- `DetailBold` is just bold with no color: correct for labels.
- `DetailDimItalic` is muted + italic: correct for empty states.
- Tab headers use `styles.Primary.Bold(true)` for active, `styles.Muted` for inactive. **Gap: no underline or indicator on the active tab — only color differentiates tabs, which fails at low contrast.**
- In the status bar, `StatusLabel` has no foreground and renders separators (` | `) as bright terminal default. **Gap: separators compete with data.**

### Empty States (multiple files)
- `workers_table.go`: `renderEmptyWorkersState` renders `"No active workers"` with muted style, centered in an `80×20` box. Functional but generic.
- `detail.go`: Each tab has a distinct empty message (e.g. `"Unassigned"`, `"No changes"`, `"No dependencies"`, `"No context"`, `"no output available"`). These are good candidates for enrichment — tell the user *why* it's empty and what to do.
- Board columns with zero beads show an empty area — no message, no visual affordance. **Gap: empty columns are invisible, not reassuring.**

### Loading States (detail.go)
- Worker tab: `"Loading events…"` in `DetailDimItalic`. Correct style but no visual motion. Acceptable for a TUI.
- Output tab: `"Loading output…"` — same.
- Board refresh: no indicator. The 2-second tick is invisible — data just snaps. **Gap: no indication when a refresh is in flight.**

### Keyboard Discoverability (model.go, help.go)
- `?` opens a help overlay — this is correct.
- Status bar shows inline key hints (e.g. `"hjkl nav  enter detail  / search  i insights  w workers  ? help  q quit"`). These are comma-separated with double-space separators.
- `HelpKey` style is `Width(10)` which truncates `"shift+tab"` (9 chars) at the right margin. **Gap: long key names clip.**
- `getViewName` and `getHelpBindingsForView` don't handle `HealthView`, `WorkersView`, or `TreeView`. **Gap: help overlay is context-unaware for 3 of 7 views.**

### Status Bar
- Current format: `daemon: online/offline | Workers: N | Open: N | In Progress: N [| Epic: ID (done/total - pct%)]`
- Hint text is right-aligned with `"  "` spacer. At narrow terminals (< 60 cols) hints are hidden.
- **Gap: no visual separation between the metrics block and the hints block.** They run together when both are present.

---

## Spacing / Padding System

### Design Decision: 1-Cell Base Unit

Terminal cells are not pixels. The minimum meaningful unit is 1 cell. The oro-dash spacing system uses:

| Token | Cells | Usage |
|-------|-------|-------|
| `SpaceXS` | 0 | Zero — no padding (borders do the work) |
| `SpaceSM` | 1 | Card internal top/bottom, status bar separators |
| `SpaceMD` | 2 | Card internal left/right, overlay internal padding |
| `SpaceLG` | 3 | Modal/overlay outer margin |
| `SpaceXL` | 4 | (reserved for future full-page margins) |

### Card Layout (board.go)

Current: `Padding(1, 2)` — correct. Keep unchanged.

The problem is `Column` padding (`Padding(1)`) combined with the card border creates a double-indent. Fix: remove column padding entirely; let card borders provide the visual grouping. Use `Padding(0)` on columns.

Column header: currently `Align(lipgloss.Center).BorderBottom(true)`. This is correct. Add `Padding(0, 1)` to the header text only (not the column container).

### Status Bar (model.go)

Replace the raw `" | "` separator string with a styled dim separator:

```go
separator := lipgloss.NewStyle().Foreground(theme.ColorBorder).Render(" │ ")
```

Use `│` (U+2502 box drawing vertical) instead of `|` (ASCII pipe) for visual consistency with borders elsewhere.

---

## Color Palette Refinement

### Problem: Active Card vs. Border Collision

`ColorBorder` is `#3E4347` and active card background is hard-coded `#3a3a3a`. These are 4 luminance points apart — nearly invisible difference in most terminals.

### Fix: Elevate Active Card to Surface Color

Add two new tokens to `Theme`:

```go
// ColorSurface: slightly elevated surface for modals/overlays
ColorSurface  lipgloss.Color

// ColorFocus: active/focused element background — must contrast with ColorSurface and ColorBg
ColorFocus    lipgloss.Color
```

Assign in `DefaultTheme()`:

```go
ColorSurface: lipgloss.Color("#1C1F23"),  // Slightly lighter than ColorBg (#111113)
ColorFocus:   lipgloss.Color("#2A2F35"),  // Clearly distinct from both Bg and Surface
```

Move the active card background from the hard-coded literal in `renderColumn()` to `theme.ColorFocus`:

```go
// In board.go renderColumn(), change:
activeCardStyle := styles.ActiveCard.Width(colWidth - 2).Background(lipgloss.Color("#3a3a3a"))
// To:
activeCardStyle := styles.ActiveCard.Width(colWidth - 2).Background(theme.ColorFocus)
```

`ColorSurface` is used for overlay backgrounds (search, help panels).

### Fix: Status Bar Label Muting

Assign `ColorMutedFg` token for text that is present but secondary:

```go
ColorMutedFg: lipgloss.Color("#565F6B"),  // Darker than Muted (#687076)
```

Apply to `StatusLabel` in `initStatusBarStyles`:

```go
s.StatusLabel = lipgloss.NewStyle().Foreground(theme.ColorMutedFg)
```

### Color Token Summary (additive — no removals)

| Token | Value | Used for |
|-------|-------|----------|
| `ColorSurface` | `#1C1F23` | Overlay/modal backgrounds |
| `ColorFocus` | `#2A2F35` | Active card, focused row background |
| `ColorMutedFg` | `#565F6B` | Status bar labels, secondary text |

---

## Typography Hierarchy

### Tab Indicator in Detail View (detail.go)

Current: active tab is `styles.Primary.Bold(true)`, inactive is `styles.Muted`. No structural indicator.

Linear uses underlines to indicate the active tab, not just color. In lipgloss this is `Underline(true)`.

**Fix:** Add an `ActiveTab` style to `Styles`:

```go
// In Styles struct:
ActiveTab   lipgloss.Style
InactiveTab lipgloss.Style

// In initDetailStyles():
s.ActiveTab   = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Underline(true)
s.InactiveTab = lipgloss.NewStyle().Foreground(theme.Muted)
```

Update `DetailModel.View()` in `detail.go`:

```go
for i, tab := range d.tabs {
    if i == d.activeTab {
        tabHeaders = append(tabHeaders, styles.ActiveTab.Render("["+tab+"]"))
    } else {
        tabHeaders = append(tabHeaders, styles.InactiveTab.Render(tab))
    }
}
```

### Column Header Hierarchy

Currently `Header` style is `Bold + Primary color`. This is visually dominant. The column header should feel like a label, not a headline.

**Fix:** Reduce column header weight — use `Bold(false)` with a slightly lighter foreground than card titles, but keep the bottom border:

```go
// In initBoardStyles():
s.Header = lipgloss.NewStyle().
    Foreground(theme.ColorMutedFg).  // was: theme.Primary
    Padding(0, 1).
    BorderBottom(true).
    BorderStyle(lipgloss.NormalBorder()).
    BorderForeground(theme.ColorBorder)
```

The `Done` column continues to use `theme.Success` (green) — this is a semantic color and should be preserved. Only the default `Header` style changes.

### Priority Badge Typography

Current badges are `Bold(true)` with priority color. This is good. The bracket syntax `[P0]` is dense. Consider the tradeoff: `[P0]` takes 4 characters; `P0` takes 2; `●` (dot with color) takes 1.

**Decision: keep `[P0]` syntax.** The brackets provide a clear visual boundary that helps scanning. The colored dot alternative is ambiguous at a glance. No change.

---

## Empty States

### Design Principle: Answer "What Now?"

Every empty state should answer three questions:
1. Why is this empty?
2. Is this normal or unexpected?
3. What should I do next?

### Board: Empty Columns

Currently empty columns show nothing. Add a faint placeholder that makes the column boundary visible but doesn't distract:

**Fix:** In `renderColumn()` in `board.go`, when `len(col.beads) == 0`:

```go
if len(col.beads) == 0 {
    emptyMsg := styles.Muted.Render("no items")
    return columnStyle.Render(header + "\n" + emptyMsg)
}
```

This keeps the column header (so the user knows the column exists) and fills the body with a faint `no items` label. The muted style ensures it doesn't compete with content in adjacent columns.

### Workers Table: Enriched Empty State

Current: `"No active workers"` centered in an 80×20 box.

**Fix:** Context-aware empty state that distinguishes "daemon offline" from "daemon online but no workers":

```go
func renderEmptyWorkersState(styles Styles, daemonHealthy bool) string {
    var msg string
    if daemonHealthy {
        msg = "No active workers  ·  start a worker with: oro work"
    } else {
        msg = "No active workers  ·  daemon offline (start with: oro start)"
    }
    return styles.Muted.Render(msg)
}
```

This requires passing `daemonHealthy bool` through from `Model` to `WorkersTableModel`. The field already exists on `Model` as `m.daemonHealthy`.

Update `WorkersTableModel` to carry daemon health:

```go
type WorkersTableModel struct {
    workers      []WorkerStatus
    assignments  map[string]string
    daemonOnline bool  // new field
}

func NewWorkersTableModel(workers []WorkerStatus, assignments map[string]string, daemonOnline bool) WorkersTableModel {
    return WorkersTableModel{
        workers:      workers,
        assignments:  assignments,
        daemonOnline: daemonOnline,
    }
}
```

Update call site in `model.go`:

```go
case WorkersView:
    workersTable := NewWorkersTableModel(m.workers, m.assignments, m.daemonHealthy)
    return workersTable.View(m.theme, m.styles, max(m.width, 80)) + "\n" + statusBar
```

### Detail View: Enriched Empty States Per Tab

| Tab | Current empty | Improved empty |
|-----|--------------|----------------|
| Worker | `"Unassigned"` | `"No worker assigned  ·  assign with: bd update <id> --worker <w>"` |
| Diff | `"No changes"` | `"No uncommitted changes for this bead"` |
| Deps | `"No dependencies"` | `"No dependencies  ·  add with: bd dep add <id> <dep>"` |
| Memory | `"No context"` | `"No injected context for this bead"` |
| Output | `"no output available"` | `"No output yet  ·  worker has not produced output"` |

All use `styles.DetailDimItalic` (muted + italic). The hints after `·` are in the same style — they don't need to be actionable links (TUI can't open terminal), they just orient the user.

### Insights View: No Data State

When there are no beads, the insights view currently renders empty. Add:

```go
if len(m.beads) == 0 {
    return styles.InsightsNoData.Render("No beads to analyze  ·  create a bead with: bd create")
}
```

---

## Loading States

### Principle: Show Loading Only When The Wait Is Perceptible

The 2-second tick refresh is fast enough that a global loading indicator would flicker annoyingly. Do not add a spinning indicator to the status bar.

However, the first load (before any data arrives) leaves the board blank. This is jarring.

### Fix: Initial Loading State

Add an `initialLoad bool` field to `Model` that is true until the first `beadsMsg` arrives:

```go
// In Model struct:
initialLoad bool

// In newModel():
initialLoad: true,

// In Update(), beadsMsg case:
case beadsMsg:
    m.initialLoad = false
    // ... existing handling ...
```

In `View()`, before rendering the board, check:

```go
// In View() default case (BoardView):
if m.initialLoad {
    return m.styles.Muted.Render("Loading…") + "\n" + statusBar
}
```

### Detail View: Loading Stays As-Is

`"Loading events…"` and `"Loading output…"` in `DetailDimItalic` are correct. The async fetch takes up to 2 seconds (timeout set in `fetchWorkerEventsCmd`). The italic muted style signals "transient." No change needed.

---

## Keyboard Discoverability

### Help Overlay: Fix Coverage Gaps

`getHelpBindingsForView()` falls through to `getBoardHelpBindings()` for `HealthView`, `WorkersView`, and `TreeView`. These views have their own key sets.

**Fix:** Add missing cases to `getHelpBindingsForView()` in `help.go`:

```go
case HealthView:
    return getHealthHelpBindings()
case WorkersView:
    return getWorkersHelpBindings()
case TreeView:
    return getTreeHelpBindings()
```

Add the three new helpers:

```go
func getHealthHelpBindings() []helpBinding {
    return []helpBinding{
        {"esc", "Return to board"},
        {"?", "Toggle help"},
        {"q or ctrl+c", "Quit"},
    }
}

func getWorkersHelpBindings() []helpBinding {
    return []helpBinding{
        {"esc", "Return to board"},
        {"?", "Toggle help"},
        {"q or ctrl+c", "Quit"},
    }
}

func getTreeHelpBindings() []helpBinding {
    return []helpBinding{
        {"j/k", "Navigate items"},
        {"space", "Expand/collapse"},
        {"enter", "View bead details"},
        {"esc", "Return to board"},
        {"?", "Toggle help"},
        {"q or ctrl+c", "Quit"},
    }
}
```

Also add these views to `getViewName()`:

```go
case HealthView:
    return "Health View"
case WorkersView:
    return "Workers View"
case TreeView:
    return "Tree View"
```

### Help Overlay: Fix HelpKey Width for Long Bindings

`HelpKey` is `Width(10)`. The binding `"tab/shift+tab"` is 14 characters. `"j/k or ↑/↓"` is 11. Both truncate.

**Fix:** Increase `HelpKey` width to 20 in `initHelpStyles()`. It already is 20 in `renderHelpContent()` (line: `keyStyle := m.styles.HelpKey.Width(20)`) but the base style is `Width(10)`. The render site overrides to 20 anyway, so this is a latent inconsistency but not a visual bug. Align the base style to match:

```go
s.HelpKey = lipgloss.NewStyle().Bold(true).Foreground(theme.Primary).Width(20)
```

### Status Bar: Structured Hint Area

Current status bar concatenates metrics and hints with a `"  "` spacer. On wide terminals this looks like one long string.

**Fix:** Right-align hints using lipgloss placement. In `renderStatusBar()`:

```go
hints := helpHintsForView(m.activeView, width)
if hints == "" || m.height < 30 {
    return metrics
}

// Pad metrics left, hints right to fill terminal width
hintsStyled := m.styles.StatusLabel.Render(hints)
metricsWidth := lipgloss.Width(metrics)
hintsWidth := lipgloss.Width(hintsStyled)
gap := width - metricsWidth - hintsWidth
if gap < 2 {
    gap = 2
}
spacer := strings.Repeat(" ", gap)
return metrics + spacer + hintsStyled
```

This right-aligns hints to the terminal edge, creating a clear visual split between data (left) and shortcuts (right) — the same pattern Linear uses in their command bar.

### Status Bar Separator Refinement

Replace the inline `" | "` separator strings with a styled separator. Add a `Separator` style to `Styles`:

```go
// In Styles struct:
Separator lipgloss.Style

// In initStatusBarStyles():
s.Separator = lipgloss.NewStyle().Foreground(theme.ColorBorder).Render(" │ ")
```

Note: `Separator` holds the rendered string directly, not a style. The separator is a constant (no dynamic content), so pre-rendering it is fine and avoids per-frame style allocation.

Update `renderStatusBar()` to use `m.styles.Separator` wherever `m.styles.StatusLabel.Render(" | ")` appears.

---

## View-Specific Polish

### Board View: Column Width Visual Rhythm

At standard 80-column terminals, 4 columns gives `80/4 = 20` chars per column. With the card border (`2` chars) and padding (`2` chars each side), the content area is `20 - 2 - 4 = 14` chars. This is tight for IDs like `oro-9g0y` (8 chars) + priority badge `[P0]` (4) + space + type icon (1) + title: about 14 chars for the title. Acceptable.

At 120 columns: `30 - 6 = 24` content chars. Good.
At 160 columns: `40 - 6 = 34` content chars. Comfortable.

No layout changes needed — the existing `calculateColumnWidth()` formula is correct.

**Fix only:** Remove column-level padding (`Padding(1)` → `Padding(0, 0)`) to eliminate the double-indent between column border and card border. Cards already have their own padding; the column container should provide only the border.

```go
// In initBoardStyles():
s.Column = lipgloss.NewStyle().
    Border(lipgloss.RoundedBorder()).
    BorderForeground(theme.ColorBorder).
    Padding(0)  // was: Padding(1)
```

### Search Overlay: Surface Elevation

The search overlay renders inline (no background fill). On busy boards, the card content bleeds through visually (no alt-screen separation for the overlay).

Note: The dashboard already uses `tea.WithAltScreen()`, so the background is the terminal's default, not the board. The overlay renders on top. No background needed.

**Fix only:** Add `ColorSurface` background to the `SearchInput` box for elevation:

```go
// In initSearchStyles():
s.SearchInput = lipgloss.NewStyle().
    Border(lipgloss.RoundedBorder()).
    BorderForeground(theme.Primary).
    Background(theme.ColorSurface).
    Padding(0, 1).
    Width(60)
```

### Workers Table: Header Typography

Column headers are currently `Bold(true) + Foreground(theme.Primary)`. This is heavy — the header competes with the data rows.

**Fix:** Use `ColorMutedFg` for headers, bold for differentiation:

```go
// In renderWorkersTable():
style := styles.WorkersCol.
    Width(colWidths[i]).
    Bold(true).
    Foreground(theme.ColorMutedFg)  // was: theme.Primary
```

The separator line `strings.Repeat("─", totalWidth)` uses terminal default color. This is correct — it blends into the chrome layer.

### Health View: Status Indicators

Read `health.go` to understand current rendering. The health view already uses colored indicators for `Alive` state (green/red). No structural changes needed.

**Fix:** Ensure `PaneHealth` rendering uses `HealthGreen`/`HealthRed` styles from `Styles` rather than inline foreground colors, for theme consistency. (Audit `health.go` during implementation to confirm.)

---

## Adversarial Self-Review

Running the six checks from `adversarial-spec-review` against this spec before finalization:

### Check 1: Acceptance Test

```
Cmd:    go test ./cmd/oro-dash/... -run TestPhase2Visual -v
Assert: all PASS (zero failures)
```

This requires a `TestPhase2Visual` test suite. The spec does not yet define individual tests. This is a gap — see Check 3.

**Resolution:** Each task below specifies its own test. The overall acceptance is the quality gate: `go test ./cmd/oro-dash/... -race -count=1`.

### Check 2: Wiring Audit

Key wiring risks in this spec:

1. **`daemonHealthy` → `WorkersTableModel`**: `m.daemonHealthy` exists on `Model`. The call site in `model.go:View()` creates `NewWorkersTableModel()`. This spec adds a third parameter. The wiring is explicit in Task 7 below (call site update). No gap.

2. **`ColorFocus` in `renderColumn()`**: The hard-coded `#3a3a3a` is in `board.go:renderColumn()`, not in `initBoardStyles()`. The theme is passed as a parameter to `RenderWithCustomWidth`. This spec explicitly names the exact line to change. No gap.

3. **`ActiveTab`/`InactiveTab` styles in `detail.go:View()`**: `styles` is passed to `View(styles Styles)`. Both new styles go into `Styles` struct initialized in `NewStyles()`. No import changes needed. No gap.

4. **`initialLoad` field**: Added to `Model` struct, initialized in `newModel()`, cleared in `beadsMsg` handler in `Update()`, consumed in `View()`. Full lifecycle covered. No gap.

5. **Help overlay view coverage**: `getHelpBindingsForView()` and `getViewName()` are both in `help.go`. The new cases are additive switch cases. `m.previousView` is set correctly when entering `HelpView` (existing logic in `handleKeyPress`). The new views (`HealthView`, `WorkersView`, `TreeView`) already set `m.previousView` before entering `HelpView` via the global `?` handler. No gap.

### Check 3: Traceability

| # | Criterion | Task | Test |
|---|-----------|------|------|
| 1 | Spacing/padding system | Task 1 (column padding), Task 2 (separator) | Snapshot regression |
| 2 | Color palette refinement | Task 3 (ColorSurface/Focus/MutedFg) | `TestTheme_NewColors` |
| 3 | Typography hierarchy | Task 4 (tab indicator), Task 5 (header weight) | Snapshot regression |
| 4 | Empty states | Task 6 (board), Task 7 (workers), Task 8 (detail) | `TestEmptyStates_*` |
| 5 | Loading states | Task 9 (initial load) | `TestInitialLoadState` |
| 6 | Keyboard discoverability | Task 10 (help coverage), Task 11 (status bar right-align) | `TestHelpView_*` |
| 7 | Transition polish | N/A — TUI has no animation; replaced by initial load state | covered by Task 9 |

All criteria covered. No gaps.

### Check 4: Negative Space

| Risk | Severity | Mitigation in spec |
|------|----------|--------------------|
| `initialLoad` never clears if `fetchBeadsCmd` always errors | minor | `beadsMsg` is sent even on error (empty slice); `initialLoad` clears either way |
| Column padding removal breaks snapshot tests | important | Task 1 explicitly regenerates snapshots |
| `WorkersTableModel` signature change breaks all callers | important | Task 7 identifies all call sites; `model_test.go` and `workers_table_test.go` are the only callers |
| `ColorSurface` on `SearchInput` may look wrong in 256-color terminals | minor | lipgloss handles 256-color degradation; `#1C1F23` degrades to `color235` |
| Right-aligning hints may overflow on very narrow terminals | minor | Guard with `width < 60` already present in `helpHintsForView`; the right-align code only runs when hints are non-empty |

### Check 5: Red Team

**Scenario:** Column padding is removed (`Padding(0)`) but the column border still provides 1 cell of inner space on each side. Cards render flush against the border. The fix makes things *worse*, not better.

**Mitigation:** lipgloss `RoundedBorder` does not add inner padding — the border character is adjacent to the content. Without `Padding(1)`, cards render immediately inside the border. The card's own `Padding(1, 2)` provides the breathing room. Net result: cards have 1 row top-padding from their own style, column border is immediately outside. This is the correct visual — same as removing double-margins. **The fix is safe.**

**Scenario:** `Separator` is stored as a pre-rendered string (not a style). If the theme changes at runtime (future feature), the separator won't update.

**Mitigation:** The current dashboard has no theme-switching. Storing as pre-rendered string is fine for this iteration. Document the limitation: if dynamic theming is added, `Separator` must become a `lipgloss.Style`.

### Check 6: Integration Points

Files this spec touches and which task covers each:

| File | Change | Task |
|------|--------|------|
| `cmd/oro-dash/theme.go` | Add `ColorSurface`, `ColorFocus`, `ColorMutedFg`, `Separator`, `ActiveTab`, `InactiveTab` | Task 3, 4 |
| `cmd/oro-dash/board.go` | Remove column padding, use `theme.ColorFocus` for active card, empty column state | Task 1, 6 |
| `cmd/oro-dash/model.go` | `initialLoad` field, right-align hints, use separator, pass `daemonHealthy` | Task 2, 7, 9, 11 |
| `cmd/oro-dash/detail.go` | `ActiveTab`/`InactiveTab` styles, enriched empty states | Task 4, 8 |
| `cmd/oro-dash/workers_table.go` | Add `daemonOnline` field, enriched empty state, header color | Task 7 |
| `cmd/oro-dash/help.go` | Add HealthView/WorkersView/TreeView bindings and view names | Task 10 |
| `cmd/oro-dash/insights.go` | No-data empty state | Task 6 |

No uncovered integration points identified.

---

## Implementation Tasks

### Task 1: Remove Column Double-Padding

**Files:**
- Modify: `cmd/oro-dash/theme.go:initBoardStyles()`
- Test: `cmd/oro-dash/snapshot_test.go` (regenerate)

**Step 1: Change Column style padding**

In `initBoardStyles()`, change:
```go
s.Column = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.ColorBorder).Padding(1)
```
To:
```go
s.Column = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.ColorBorder).Padding(0)
```

**Step 2: Run snapshot tests with -update flag**

```bash
go test ./cmd/oro-dash/... -run TestSnapshot -update -v
```

Expected: snapshots regenerated (visual change: cards sit directly inside column border with their own padding).

**Step 3: Run all tests**

```bash
go test ./cmd/oro-dash/... -race -count=1
```

Expected: PASS

**Step 4: Commit**

```
fix(dash): remove column double-padding for tighter visual rhythm
```

---

### Task 2: Styled Status Bar Separator

**Files:**
- Modify: `cmd/oro-dash/theme.go` (add `Separator` to `Styles` struct)
- Modify: `cmd/oro-dash/model.go:renderStatusBar()`

**Step 1: Add Separator to Styles struct**

In `theme.go`, add to `Styles` struct:
```go
// Separator is the pre-rendered styled vertical bar used between status bar elements.
Separator string
```

In `initStatusBarStyles()`:
```go
s.Separator = lipgloss.NewStyle().Foreground(theme.ColorBorder).Render(" │ ")
```

**Step 2: Update renderStatusBar() to use separator**

In `model.go:renderStatusBar()`, replace all `m.styles.StatusLabel.Render(" | ")` with `m.styles.Separator`.

**Step 3: Write test**

```go
// In model_test.go:
func TestStatusBar_UsesSeparator(t *testing.T) {
    m := newModel()
    m.width = 120
    m.height = 30
    m.daemonHealthy = true
    m.workerCount = 2
    m.openCount = 5
    m.inProgressCount = 1

    bar := m.renderStatusBar(m.width)

    // Should contain the box-drawing separator, not the pipe
    if !strings.Contains(bar, "│") {
        t.Error("status bar should use │ separator, not |")
    }
}
```

**Step 4: Run test**

```bash
go test ./cmd/oro-dash/... -run TestStatusBar_UsesSeparator -v
```

Expected: PASS

**Step 5: Commit**

```
fix(dash): use styled box-drawing separator in status bar
```

---

### Task 3: Add ColorSurface, ColorFocus, ColorMutedFg Tokens

**Files:**
- Modify: `cmd/oro-dash/theme.go` (`Theme` struct, `DefaultTheme()`, `NewStyles()`, `initStatusBarStyles()`, `initBoardStyles()`)

**Step 1: Add new fields to Theme struct**

In `theme.go`, add to `Theme` struct (after `ColorBg`/`ColorFg`):
```go
// Elevated surface color — for overlays and modals
ColorSurface lipgloss.Color

// Focus/active element background — clearly distinct from ColorBg and ColorSurface
ColorFocus lipgloss.Color

// Secondary text that is present but visually recessed
ColorMutedFg lipgloss.Color
```

**Step 2: Assign in DefaultTheme()**

```go
ColorSurface: lipgloss.Color("#1C1F23"),
ColorFocus:   lipgloss.Color("#2A2F35"),
ColorMutedFg: lipgloss.Color("#565F6B"),
```

**Step 3: Apply ColorMutedFg to StatusLabel**

In `initStatusBarStyles()`:
```go
s.StatusLabel = lipgloss.NewStyle().Foreground(theme.ColorMutedFg)
```

**Step 4: Apply ColorSurface to SearchInput background**

In `initSearchStyles()`:
```go
s.SearchInput = lipgloss.NewStyle().
    Border(lipgloss.RoundedBorder()).
    BorderForeground(theme.Primary).
    Background(theme.ColorSurface).
    Padding(0, 1).
    Width(60)
```

**Step 5: Write test**

```go
// In theme_test.go:
func TestTheme_NewColorTokensPresent(t *testing.T) {
    theme := DefaultTheme()

    if theme.ColorSurface == "" {
        t.Error("ColorSurface must be set")
    }
    if theme.ColorFocus == "" {
        t.Error("ColorFocus must be set")
    }
    if theme.ColorMutedFg == "" {
        t.Error("ColorMutedFg must be set")
    }
    // ColorFocus must differ from ColorBg and ColorBorder
    if theme.ColorFocus == theme.ColorBg {
        t.Error("ColorFocus must differ from ColorBg")
    }
    if theme.ColorFocus == theme.ColorBorder {
        t.Error("ColorFocus must differ from ColorBorder")
    }
}
```

**Step 6: Run test**

```bash
go test ./cmd/oro-dash/... -run TestTheme_NewColorTokensPresent -v
```

Expected: PASS

**Step 7: Commit**

```
feat(dash): add ColorSurface, ColorFocus, ColorMutedFg theme tokens
```

---

### Task 4: Active Card Uses ColorFocus, Column Header Uses ColorMutedFg

**Files:**
- Modify: `cmd/oro-dash/board.go:renderColumn()` (active card background)
- Modify: `cmd/oro-dash/board.go:renderColumnHeader()` (header color)

**Step 1: Fix active card background**

In `board.go:renderColumn()`, change:
```go
activeCardStyle := styles.ActiveCard.Width(colWidth - 2).Background(lipgloss.Color("#3a3a3a"))
```
To:
```go
activeCardStyle := styles.ActiveCard.Width(colWidth - 2).Background(theme.ColorFocus)
```

Note: `theme` is a parameter to `RenderWithCustomWidth` → passed through to `renderColumn`. Confirm the parameter chain — `renderColumn` currently receives `theme Theme` as a parameter. Yes, it does (signature: `func (bm BoardModel) renderColumn(col boardColumn, colIdx, activeCol, activeBead, colWidth int, theme Theme, styles Styles) string`).

**Step 2: Fix column header color (non-Done columns)**

In `board.go:renderColumnHeader()`, change:
```go
headerColor := theme.Primary
```
To:
```go
headerColor := theme.ColorMutedFg
```

Done column keeps `theme.Success` (green) — no change to that branch.

**Step 3: Write test**

```go
// In board_test.go:
func TestBoardRender_ActiveCardUsesThemeColorFocus(t *testing.T) {
    beads := []protocol.Bead{
        {ID: "oro-1", Title: "Test bead", Status: "open", Priority: 1, Type: "task"},
    }
    board := NewBoardModel(beads)
    theme := DefaultTheme()
    styles := NewStyles(theme)

    output := board.RenderWithCustomWidth(0, 0, 30, theme, styles)

    // Active card should use ColorFocus (#2A2F35), not the old hard-coded #3a3a3a
    if strings.Contains(output, "#3a3a3a") {
        t.Error("active card must not use hard-coded #3a3a3a")
    }
}
```

**Step 4: Run test**

```bash
go test ./cmd/oro-dash/... -run TestBoardRender_ActiveCardUsesThemeColorFocus -v
```

Expected: PASS

**Step 5: Commit**

```
fix(dash): use theme.ColorFocus for active card, ColorMutedFg for column headers
```

---

### Task 5: Tab Indicator with Underline

**Files:**
- Modify: `cmd/oro-dash/theme.go` (add `ActiveTab`, `InactiveTab` to `Styles` struct and `initDetailStyles()`)
- Modify: `cmd/oro-dash/detail.go:View()`

**Step 1: Add styles to Styles struct**

In `theme.go`, add to `Styles` struct (in detail view styles section):
```go
ActiveTab   lipgloss.Style
InactiveTab lipgloss.Style
```

In `initDetailStyles()`:
```go
s.ActiveTab   = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Underline(true)
s.InactiveTab = lipgloss.NewStyle().Foreground(theme.Muted)
```

**Step 2: Use new styles in detail.go View()**

In `detail.go:View()`, change:
```go
for i, tab := range d.tabs {
    if i == d.activeTab {
        tabHeaders = append(tabHeaders, styles.Primary.Bold(true).Render("["+tab+"]"))
    } else {
        tabHeaders = append(tabHeaders, styles.Muted.Render(tab))
    }
}
```
To:
```go
for i, tab := range d.tabs {
    if i == d.activeTab {
        tabHeaders = append(tabHeaders, styles.ActiveTab.Render("["+tab+"]"))
    } else {
        tabHeaders = append(tabHeaders, styles.InactiveTab.Render(tab))
    }
}
```

**Step 3: Write test**

```go
// In detail_test.go:
func TestDetailView_ActiveTabHasUnderlineStyle(t *testing.T) {
    theme := DefaultTheme()
    styles := NewStyles(theme)

    // Verify ActiveTab style has underline
    if !styles.ActiveTab.GetUnderline() {
        t.Error("ActiveTab style must have underline set to true")
    }
    // Verify InactiveTab style has no underline
    if styles.InactiveTab.GetUnderline() {
        t.Error("InactiveTab style must not have underline")
    }
}
```

**Step 4: Run test**

```bash
go test ./cmd/oro-dash/... -run TestDetailView_ActiveTabHasUnderlineStyle -v
```

Expected: PASS

**Step 5: Regenerate detail snapshots**

```bash
go test ./cmd/oro-dash/... -run TestSnapshot_DetailView -update
```

**Step 6: Commit**

```
feat(dash): add underline indicator to active tab in detail view
```

---

### Task 6: Empty States — Board and Insights

**Files:**
- Modify: `cmd/oro-dash/board.go:renderColumn()`
- Modify: `cmd/oro-dash/insights.go` (or `model.go:buildInsightsModel()`)

**Step 1: Add empty column placeholder in board.go**

In `board.go:renderColumn()`, after computing `header`, before building `cardsBuilder`:

```go
if len(col.beads) == 0 {
    emptyMsg := styles.Muted.Render("no items")
    return columnStyle.Render(header + "\n" + emptyMsg)
}
```

The existing code then continues with `var cardsBuilder strings.Builder` — the early return skips it entirely.

**Step 2: Write test for empty column**

```go
// In board_test.go:
func TestBoardRender_EmptyColumnShowsNoItems(t *testing.T) {
    // All beads in progress — Ready column will be empty
    beads := []protocol.Bead{
        {ID: "oro-1", Title: "Test", Status: "in_progress", Priority: 1, Type: "task"},
    }
    board := NewBoardModel(beads)
    theme := DefaultTheme()
    styles := NewStyles(theme)

    output := board.RenderWithCustomWidth(0, 0, 30, theme, styles)

    if !strings.Contains(output, "no items") {
        t.Error("empty column must render 'no items' placeholder")
    }
}
```

**Step 3: Add no-beads guard to insights rendering**

In `model.go`, in the `InsightsView` case of `View()`:

```go
case InsightsView:
    if len(m.beads) == 0 {
        return m.styles.InsightsNoData.Render("No beads to analyze  ·  create a bead with: bd create") + "\n" + statusBar
    }
    insights := m.buildInsightsModel()
    return insights.Render(m.styles) + "\n" + statusBar
```

**Step 4: Write test**

```go
// In model_test.go:
func TestInsightsView_EmptyBeads_ShowsNoDataMessage(t *testing.T) {
    m := newModel()
    m.activeView = InsightsView
    m.beads = nil

    output := m.View()

    if !strings.Contains(output, "No beads to analyze") {
        t.Errorf("insights view with no beads must show no-data message, got: %q", output)
    }
}
```

**Step 5: Run tests**

```bash
go test ./cmd/oro-dash/... -run TestBoardRender_EmptyColumnShowsNoItems -v
go test ./cmd/oro-dash/... -run TestInsightsView_EmptyBeads -v
```

Expected: PASS

**Step 6: Commit**

```
feat(dash): add empty state placeholders for empty board columns and insights
```

---

### Task 7: Workers Table — Daemon-Aware Empty State

**Files:**
- Modify: `cmd/oro-dash/workers_table.go` (add `daemonOnline` field, update `NewWorkersTableModel`, update `renderEmptyWorkersState`)
- Modify: `cmd/oro-dash/model.go` (pass `m.daemonHealthy` to `NewWorkersTableModel`)
- Modify: `cmd/oro-dash/workers_table_test.go` (update call sites)
- Modify: `cmd/oro-dash/model_test.go` (update call sites if any)

**Step 1: Write failing test**

```go
// In workers_table_test.go:
func TestWorkersTable_EmptyState_DaemonOnline(t *testing.T) {
    theme := DefaultTheme()
    styles := NewStyles(theme)

    wt := NewWorkersTableModel(nil, nil, true)
    output := wt.View(theme, styles, 80)

    if !strings.Contains(output, "oro work") {
        t.Error("empty state with daemon online must mention 'oro work'")
    }
}

func TestWorkersTable_EmptyState_DaemonOffline(t *testing.T) {
    theme := DefaultTheme()
    styles := NewStyles(theme)

    wt := NewWorkersTableModel(nil, nil, false)
    output := wt.View(theme, styles, 80)

    if !strings.Contains(output, "daemon offline") {
        t.Error("empty state with daemon offline must mention 'daemon offline'")
    }
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./cmd/oro-dash/... -run TestWorkersTable_EmptyState -v
```

Expected: FAIL (compile error — `NewWorkersTableModel` takes 2 args, not 3)

**Step 3: Update WorkersTableModel**

In `workers_table.go`:

```go
type WorkersTableModel struct {
    workers      []WorkerStatus
    assignments  map[string]string
    daemonOnline bool
}

func NewWorkersTableModel(workers []WorkerStatus, assignments map[string]string, daemonOnline bool) WorkersTableModel {
    return WorkersTableModel{
        workers:      workers,
        assignments:  assignments,
        daemonOnline: daemonOnline,
    }
}

func renderEmptyWorkersState(styles Styles, daemonOnline bool) string {
    var msg string
    if daemonOnline {
        msg = "No active workers  ·  start a worker with: oro work"
    } else {
        msg = "No active workers  ·  daemon offline (start with: oro start)"
    }
    return styles.Muted.Render(msg)
}
```

Update `View()` to pass `daemonOnline` to `renderEmptyWorkersState`:

```go
func (w WorkersTableModel) View(theme Theme, styles Styles, totalWidth int) string {
    if len(w.workers) == 0 {
        return renderEmptyWorkersState(styles, w.daemonOnline)
    }
    return w.renderWorkersTable(theme, styles, totalWidth)
}
```

**Step 4: Update column header color**

In `renderWorkersTable()`, change header foreground:
```go
style := styles.WorkersCol.
    Width(colWidths[i]).
    Bold(true).
    Foreground(theme.ColorMutedFg)  // was: theme.Primary
```

**Step 5: Update call sites**

In `model.go:View()` WorkersView case:
```go
case WorkersView:
    workersTable := NewWorkersTableModel(m.workers, m.assignments, m.daemonHealthy)
    return workersTable.View(m.theme, m.styles, max(m.width, 80)) + "\n" + statusBar
```

In `workers_table_test.go`, update all existing calls to `NewWorkersTableModel(workers, assignments)` → `NewWorkersTableModel(workers, assignments, true)`.

**Step 6: Run tests**

```bash
go test ./cmd/oro-dash/... -run TestWorkersTable -v
```

Expected: PASS

**Step 7: Commit**

```
feat(dash): daemon-aware empty state in workers table
```

---

### Task 8: Enriched Empty States in Detail View

**Files:**
- Modify: `cmd/oro-dash/detail.go` (5 render functions)

**Step 1: Update each empty state message**

Worker tab — `renderWorkerTab()`:
```go
if d.bead.WorkerID == "" {
    return styles.DetailDimItalic.Render("No worker assigned  ·  assign with: bd update <id> --worker <w>")
}
```

Diff tab — `renderDiffTab()`:
```go
if d.bead.GitDiff == "" {
    return styles.DetailDimItalic.Render("No uncommitted changes for this bead")
}
```

Deps tab — `renderDepsTab()`:
```go
if len(deps) == 0 {
    return styles.DetailDimItalic.Render("No dependencies  ·  add with: bd dep add <id> <dep>")
}
```

Memory tab — `renderMemoryTab()`:
```go
if d.bead.Memory == "" {
    return styles.DetailDimItalic.Render("No injected context for this bead")
}
```

Output tab — `renderOutputTab()`:
```go
if len(d.workerOutput) == 0 {
    return styles.DetailDimItalic.Render("No output yet  ·  worker has not produced output")
}
```

**Step 2: Write test**

```go
// In detail_test.go:
func TestDetailView_EmptyStates_AreInformative(t *testing.T) {
    theme := DefaultTheme()
    styles := NewStyles(theme)
    bead := protocol.BeadDetail{ID: "oro-test", Title: "Test"}
    d := newDetailModel(bead, theme, styles)

    // Worker tab — no worker assigned
    workerContent := d.renderWorkerTab(theme, styles)
    if !strings.Contains(workerContent, "No worker assigned") {
        t.Error("worker tab empty state must say 'No worker assigned'")
    }

    // Deps tab — no deps
    depsContent := d.renderDepsTab(styles)
    if !strings.Contains(depsContent, "No dependencies") {
        t.Error("deps tab empty state must say 'No dependencies'")
    }
}
```

**Step 3: Run test**

```bash
go test ./cmd/oro-dash/... -run TestDetailView_EmptyStates_AreInformative -v
```

Expected: PASS

**Step 4: Commit**

```
feat(dash): enrich empty state messages in detail view tabs
```

---

### Task 9: Initial Load State

**Files:**
- Modify: `cmd/oro-dash/model.go` (add `initialLoad` field, set in `newModel`, clear in `beadsMsg`, render in `View`)

**Step 1: Write failing test**

```go
// In model_test.go:
func TestModel_InitialLoadState(t *testing.T) {
    m := newModel()
    m.width = 80
    m.height = 30

    // Before any beadsMsg — should show loading state
    output := m.View()
    if !strings.Contains(output, "Loading") {
        t.Error("model should show loading state before first beadsMsg")
    }
}

func TestModel_InitialLoadClears_AfterBeadsMsg(t *testing.T) {
    m := newModel()
    m.width = 80
    m.height = 30

    // Simulate receiving empty beadsMsg
    updated, _ := m.Update(beadsMsg([]protocol.Bead{}))
    m = updated.(Model)

    output := m.View()
    if strings.Contains(output, "Loading") {
        t.Error("model must not show loading state after beadsMsg is received")
    }
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./cmd/oro-dash/... -run TestModel_InitialLoad -v
```

Expected: FAIL (no `initialLoad` field exists yet)

**Step 3: Implement**

In `model.go`, add to `Model` struct:
```go
initialLoad bool // True until first beadsMsg received
```

In `newModel()`:
```go
return Model{
    ...
    initialLoad: true,
}
```

In `Update()`, in the `beadsMsg` case, add before existing handling:
```go
case beadsMsg:
    m.initialLoad = false
    // ... existing code ...
```

In `View()`, in the `default` (BoardView) case, add as first check:
```go
default:
    if m.initialLoad {
        return m.styles.Muted.Render("Loading…") + "\n" + statusBar
    }
    // ... existing board rendering ...
```

**Step 4: Run tests**

```bash
go test ./cmd/oro-dash/... -run TestModel_InitialLoad -v
```

Expected: PASS

**Step 5: Commit**

```
feat(dash): show loading state before first data fetch completes
```

---

### Task 10: Help Overlay Coverage for HealthView, WorkersView, TreeView

**Files:**
- Modify: `cmd/oro-dash/help.go`

**Step 1: Write failing test**

```go
// In help_test.go:
func TestHelpBindings_HealthView(t *testing.T) {
    bindings := getHelpBindingsForView(HealthView)
    if len(bindings) == 0 {
        t.Error("HealthView must have dedicated help bindings")
    }
    // Must not fall through to board bindings (board has "enter detail" binding)
    for _, b := range bindings {
        if b.desc == "View bead details" {
            t.Error("HealthView help must not include board-specific 'View bead details' binding")
        }
    }
}

func TestGetViewName_AllViews(t *testing.T) {
    views := []ViewType{BoardView, DetailView, InsightsView, SearchView, HealthView, WorkersView, TreeView}
    for _, v := range views {
        name := getViewName(v)
        if name == "Unknown View" {
            t.Errorf("getViewName returned 'Unknown View' for ViewType %d", v)
        }
    }
}
```

**Step 2: Run tests to verify failure**

```bash
go test ./cmd/oro-dash/... -run TestHelpBindings_HealthView -v
go test ./cmd/oro-dash/... -run TestGetViewName_AllViews -v
```

Expected: FAIL

**Step 3: Add cases to getHelpBindingsForView()**

In `help.go`:

```go
func getHelpBindingsForView(view ViewType) []helpBinding {
    switch view {
    case BoardView:
        return getBoardHelpBindings()
    case DetailView:
        return getDetailHelpBindings()
    case InsightsView:
        return getInsightsHelpBindings()
    case SearchView:
        return getSearchHelpBindings()
    case HealthView:
        return getHealthHelpBindings()
    case WorkersView:
        return getWorkersHelpBindings()
    case TreeView:
        return getTreeHelpBindings()
    default:
        return getBoardHelpBindings()
    }
}

func getHealthHelpBindings() []helpBinding {
    return []helpBinding{
        {"esc", "Return to board"},
        {"?", "Toggle help"},
        {"q or ctrl+c", "Quit"},
    }
}

func getWorkersHelpBindings() []helpBinding {
    return []helpBinding{
        {"esc", "Return to board"},
        {"?", "Toggle help"},
        {"q or ctrl+c", "Quit"},
    }
}

func getTreeHelpBindings() []helpBinding {
    return []helpBinding{
        {"j/k", "Navigate items"},
        {"space", "Expand/collapse"},
        {"enter", "View bead details"},
        {"esc", "Return to board"},
        {"?", "Toggle help"},
        {"q or ctrl+c", "Quit"},
    }
}
```

Add to `getViewName()`:

```go
case HealthView:
    return "Health View"
case WorkersView:
    return "Workers View"
case TreeView:
    return "Tree View"
```

**Step 4: Run tests**

```bash
go test ./cmd/oro-dash/... -run TestHelpBindings -v
go test ./cmd/oro-dash/... -run TestGetViewName -v
```

Expected: PASS

**Step 5: Commit**

```
fix(dash): add help overlay coverage for HealthView, WorkersView, TreeView
```

---

### Task 11: Right-Align Status Bar Hints

**Files:**
- Modify: `cmd/oro-dash/model.go:renderStatusBar()`

**Step 1: Write failing test**

```go
// In model_test.go:
func TestStatusBar_HintsRightAligned(t *testing.T) {
    m := newModel()
    m.width = 120
    m.height = 35
    m.activeView = BoardView

    bar := m.renderStatusBar(m.width)

    // Find position of "q quit" hint — it should be near the end of the string
    // (right-aligned), not immediately after the metrics block.
    plainBar := stripANSI(bar)  // helper that strips escape sequences
    qQuitIdx := strings.Index(plainBar, "q quit")
    if qQuitIdx < 0 {
        t.Fatal("status bar must contain 'q quit' hint")
    }
    if qQuitIdx < len(plainBar)-20 {
        t.Errorf("'q quit' should be near end of status bar (right-aligned), found at index %d of %d", qQuitIdx, len(plainBar))
    }
}
```

Note: `stripANSI` is a test helper — add to `model_test.go`:
```go
func stripANSI(s string) string {
    re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
    return re.ReplaceAllString(s, "")
}
```

**Step 2: Run test to verify failure**

```bash
go test ./cmd/oro-dash/... -run TestStatusBar_HintsRightAligned -v
```

Expected: FAIL (hints currently left-appended with `"  "` spacer)

**Step 3: Implement right-aligned hints**

In `model.go:renderStatusBar()`, replace the final section:

```go
hints := helpHintsForView(m.activeView, width)
if hints == "" || m.height < 30 {
    return metrics
}

hintsStyled := m.styles.StatusLabel.Render(hints)
metricsWidth := lipgloss.Width(metrics)
hintsWidth := lipgloss.Width(hintsStyled)
gap := width - metricsWidth - hintsWidth
if gap < 2 {
    gap = 2
}
spacer := strings.Repeat(" ", gap)
return metrics + spacer + hintsStyled
```

**Step 4: Run tests**

```bash
go test ./cmd/oro-dash/... -run TestStatusBar_HintsRightAligned -v
go test ./cmd/oro-dash/... -race -count=1
```

Expected: PASS

**Step 5: Commit**

```
fix(dash): right-align keyboard hints in status bar
```

---

## Final Verification

After all 11 tasks complete:

```bash
go test ./cmd/oro-dash/... -race -count=1 -v
```

Expected: all PASS, no races.

Quality gate:

```bash
./quality_gate.sh
```

Expected: PASS.

---

## Key Decisions Log

| Decision | Rationale |
|----------|-----------|
| No new views — polish pass only | Phase 2 scope is refinement, not features. New views increase the regression surface. |
| Keep `[P0]` badge syntax | Brackets provide unambiguous visual boundary. Dot alternative is ambiguous. |
| No spinner for 2-second tick | Would flicker annoyingly. Only `initialLoad` state needs a loading indicator. |
| `Separator` stored as pre-rendered string | Constant, no dynamic content, avoids per-frame style allocation. Document limitation. |
| Column padding → 0 (not removed entirely) | lipgloss `Padding(0)` is explicit; cards provide their own internal padding. |
| `ColorMutedFg` darker than `Muted` | `Muted` (#687076) is used for text. `ColorMutedFg` (#565F6B) is for chrome — chrome should recede further than content. |
| Right-align hints with gap calculation | Matches Linear's command bar pattern: data left, shortcuts right. Unambiguous even at glance. |
| `daemonOnline` bool on WorkersTableModel | Minimal coupling: model passes a single bool, not the full Model struct. Keeps WorkersTableModel self-contained. |
| Underline on active tab (not background) | Background on tabs in TUI creates jagged visual artifacts with rounded borders. Underline is clean and universally readable. |
