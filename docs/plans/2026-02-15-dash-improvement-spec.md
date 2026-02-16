# Oro Dash Improvement Spec

**Date:** 2026-02-15
**Status:** Draft
**References:** [beads_viewer](https://github.com/Dicklesworthstone/beads_viewer), [perles](https://github.com/zjrosen/perles), [existing spec](2026-02-08-tui-dashboard-spec.md)

## Current State

oro dash is a Bubble Tea TUI (`cmd/oro-dash/`) with:
- 3-column kanban (Ready/In Progress/Blocked) — no Done column
- Minimal card rendering (title + dimmed ID, no priority/type/worker info)
- Status bar (daemon health, workers,  /in-progress counts)
- 2-second tick refresh via `bd list --json` + dispatcher UDS
- Only `q`/`ctrl+c` work — zero navigation, zero interactivity
- Several sub-models **built but not wired**: detail (5 tabs), search (fuzzy+prefix), graph/insights (critical path, bottleneck, cycle detection, triage flags)
- Simplified 6-color ANSI theme (spec defines richer hex palette)
- No `bubbles` package — all components hand-rolled
- No `fsnotify` — pure polling

## Gap Analysis: Spec vs. Implementation

The [existing spec](2026-02-08-tui-dashboard-spec.md) already incorporates learnings from 7 reference projects. The gap is in execution — roughly phases 1-2 of 12 are done.

| Spec Feature | Status | Notes |
|---|---|---|
| 4-column kanban | Partial | 3 columns, no Done |
| hjkl/tab navigation | Not started | Only q/ctrl+c |
| Card with priority/type/worker | Not started | Only title + ID |
| Detail drilldown (5 tabs) | Model built, not wired | No keyboard routing |
| Search overlay (/) | Logic built, no UI | No textinput component |
| Workers table view (w) | Not started | — |
| All Beads tree view (a) | Not started | — |
| Insights view (i) | Logic built, not rendered | graph.go has algorithms |
| fsnotify live reload | Not started | Pure polling |
| Robot mode --json | Function exists, no flag | — |
| Help overlay (?) | Not started | — |
| Hex color theme | Not started | Using ANSI codes |
| Adaptive layout | Not started | Fixed 30-char columns |
| Status bar at bottom | Partial | At top, minimal info |

## Lessons From Reference Projects

### From beads_viewer — Graph Intelligence & Performance

| Pattern | What It Does | Applicability |
|---|---|---|
| **Two-phase async analysis** | Show UI instantly, compute graph metrics in background goroutines with 500ms timeout. Results swapped via atomic pointer. | High — our graph.go already has the algorithms, just need async execution |
| **Pre-computed style delegates** | Build lipgloss styles once at init, reuse per frame. Eliminates ~16 NewStyle() allocations per visible item per render. | High — our board.go calls DefaultTheme() and builds styles every Render() |
| **Windowed rendering** | Render only visible rows via `visibleRange()`. O(viewport) not O(n). | Medium — needed when bead count grows |
| **Adjustable split-pane** | `<`/`>` keys resize list vs. detail at runtime. Default 40/60 split. | Medium — nice for detail view |
| **Swimlane cycling** | Same kanban, three grouping modes (status/priority/type) via single key. | Low — nice but not essential |
| **WCAG-compliant adaptive colors** | Detect TrueColor/ANSI256/16-color, degrade gracefully. AdaptiveColor with light/dark pairs. | Medium — important for accessibility |
| **Sparklines** | Unicode `▁▂▃▄▅▆▇█` for inline metrics. Pure functions over normalized scalars. | Low — polish for insights view |
| **Recipe system** | Saved filter+sort presets in YAML. Shareable across team. | Low — defer to v2 |
| **Calculation proofs** | Show formula and inputs that produced a metric score. Builds trust. | Low — nice for insights |

### From perles — Composability & Polish

| Pattern | What It Does | Applicability |
|---|---|---|
| **BQL query-driven columns** | Every column defined by a query, not hardcoded. Full lexer/parser/SQL transpiler. | Low for v1 — impressive but overkill. Our fixed 4-column layout is fine. |
| **Tab-in-border rendering** | Tabs rendered inside the top border line (`--- Tab1 --- Tab2 ---`). Very space-efficient. | Medium — good for detail view tabs |
| **Command palette** | Fuzzy-filtered overlay (ctrl+k), like VS Code/Linear. Centered modal. | Low — search overlay covers this |
| **Toast notifications** | Ephemeral feedback (4 styles: success/error/info/warn, auto-dismiss). | Low — no user actions yet that need feedback |
| **Golden file testing** | Snapshot tests for rendered views. Catches rendering regressions. | High — we should do this for all views |
| **Shared focus pointer** | `focused *int` shared across columns for coordinated focus. | High — elegant solution for our kanban navigation |
| **Mouse zones via bubblezone** | Every clickable element gets a unique zone ID. Adds discoverability. | Medium — defer until keyboard-first is solid |
| **Auto-refresh via fsnotify** | Watch `.beads/` for changes, reload instantly. Reactive, not just polling. | High — already in our spec, high-value |
| **60+ design tokens** with presets | Named tokens, 7 theme presets, per-token overrides in YAML. | Low for v1 — but our theme needs to grow beyond 6 colors |
| **Save search as column** | ctrl+s saves current search as a kanban column. Bridges exploration and curation. | Low — no user-defined columns in v1 |

## Improvement Waves

### Wave 1: Wire the Skeleton (connect existing code to keyboard events)

**Goal:** Make oro dash interactive. Everything built but not wired becomes usable.

1. **Add cursor-based kanban navigation** — `h/l` between columns, `j/k` within columns, visual cursor highlighting on the selected card. Add `activeCol` and `activeBead` fields to the root Model. Route KeyMsg in Update().

2. **Wire detail drilldown** — `Enter` on a selected card transitions to DetailView. `Esc`/`Backspace` returns to BoardView. `Tab`/`Shift-Tab` cycles detail tabs. Wire detail.nextTab()/prevTab() into Update().

3. **Wire search overlay** — `/` opens search, `Esc` closes. Add `bubbles/textinput` for the input field. Connect SearchModel.Filter() to live results. `Enter` on a result navigates to detail.

4. **Wire insights view** — `i` toggles to InsightsView. Render InsightsModel with graph analysis output. `Esc` returns to board.

5. **Add Done column** — 4th kanban column for `closed` status beads, limited to most recent N.

6. **Add help overlay** — `?` toggles a key-binding reference panel on the right side. Context-aware (shows different bindings per view).

7. **Enrich card rendering** — Priority badge `[P0]`-`[P4]` with per-priority colors. Type icon. Worker ID + context % on in-progress cards. Blocker IDs on blocked cards.

8. **Upgrade to spec theme** — Replace ANSI codes with hex colors from the spec. Add per-priority colors (ColorP0-P4), per-status colors (ColorReady/InProgress/Blocked/Done), heartbeat health colors.

### Wave 2: Real UX (professional polish)

**Goal:** Bring the UX to parity with reference projects. Feels like a real product.

1. **Integrate bubbles components** — `list.Model` for kanban columns (built-in pagination, filtering), `viewport.Model` for scrollable detail tabs, `textinput.Model` for search input. Remove hand-rolled equivalents.

2. **Split-pane detail layout** — When entering detail view, show dimmed board on left (40%), detail on right (60%). Preserves board context during drilldown. `<`/`>` to adjust ratio.

3. **fsnotify live reload** — Watch `.beads/` directory. On change, trigger immediate data refresh instead of waiting for next tick. Makes the dashboard feel alive.

4. **Workers table view** — `w` key toggles. Sortable table showing worker ID, assigned bead, status, context %, heartbeat age, cycle count. Color-coded heartbeat (green <5s, amber 5-15s, red >15s).

5. **Adaptive layout** — Three width breakpoints: compact (<80 cols), standard (80-140), wide (140+). Compact: single-column stacked. Standard: 4-column kanban. Wide: extra card info (description preview, sparklines).

6. **Richer status bar** — Move to bottom. Add help hints (`/ search  ? help  q quit`), last merge time, lead time metric. Condense to single line when height < 30.

7. **Robot mode flag** — `--json` flag on `oro-dash` binary. Calls existing `robotMode()`, prints JSON, exits. Wire through `oro dash --json` in `cmd_dash.go`.

8. **Pre-computed styles** — Build all lipgloss styles once in `initStyles()` at model creation. Eliminate `DefaultTheme()` and `NewStyle()` calls inside `Render()`/`View()`.

### Wave 3: Power Features (differentiation)

**Goal:** Features that make oro dash uniquely valuable, not just "another kanban."

1. **All Beads tree view** — `a` key. Hierarchical table with epics as collapsible groups. `Space` to expand/collapse. Progress bars on epic rows (`[████░░] 3/5`). Sort by priority/status/created.

2. **Windowed rendering** — Only render visible rows in all list/tree views. `visibleRange()` function based on viewport height. Critical for repos with 50+ beads.

3. **Two-phase async graph analysis** — Phase 1 (degree, topo sort) runs immediately. Phase 2 (critical path, bottlenecks, cycle detection) runs in background goroutine with 500ms timeout. Results swapped atomically.

4. **Golden file tests** — Snapshot tests for board, detail, search, insights, workers views. Catch rendering regressions automatically.

5. **Mouse zone support** — `bubblezone` for clickable cards, tabs, column headers. Adds discoverability without replacing keyboard-first workflow.

6. **Dependency tree in detail** — Deps tab shows interactive dependency tree. `Enter` on a dependency navigates to that bead's detail. Traversable graph exploration.

## Implementation Priorities

Wave 1 items 1-8 should be a single epic. Each item is a standalone bead that can be implemented independently. Items 1-4 (keyboard wiring) should come first as they unblock all other interactivity. Items 5-8 are visual polish that can happen in parallel.

Wave 2 items 9-16 depend on Wave 1 being complete. Item 9 (bubbles) is foundational for items 10-14. Items 11, 15, 16 are independent.

Wave 3 items 17-22 are nice-to-haves that layer on top of a working dashboard.
