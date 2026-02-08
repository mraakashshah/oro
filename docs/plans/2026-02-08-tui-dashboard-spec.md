# Oro TUI Dashboard Spec

**Date:** 2026-02-08
**Status:** Draft (updated with reference research)

## Design Philosophy

Principles distilled from studying 7 beads viewers and Linear's design approach:

**Speed is the feature.** Every interaction must feel instant. Local-first data,
optimistic rendering, zero network latency. Actions happen on the client before
syncing — never wait for a round-trip. (Linear principle: speed eliminates friction
and keeps developers in flow state.)

**Keyboard-first, zero-mouse workflow.** Every action reachable via short key
combos. Vim-style `hjkl` navigation. Global fuzzy search with `/`. No action
should require reaching for the mouse. (From: beads_viewer, Perles, Linear's
Cmd+K pattern.)

**Opinionated defaults, minimal configuration.** Ship a fixed 4-column board
(Ready → In Progress → Blocked → Done). Don't expose column customization in v1.
Reduce decision fatigue by choosing good defaults. (Linear principle: be opinionated
at the atomic level, flexible at the organizational level.)

**Information density without clutter.** Show priority, status, worker assignment,
and context % on cards — but nothing more. Use color and iconography instead of
text labels. Dimmed text for secondary info. (From: beads_viewer's ultra-wide mode,
beads-dashboard's donut charts, Linear's compact issue rows.)

**Graph-aware, not list-aware.** Dependencies are first-class. The DAG structure
surfaces bottlenecks, critical paths, and blocking chains that flat lists hide.
(From: beads_viewer's 9 graph metrics, Perles' dependency explorer.)

**Agent-friendly output.** Robot modes with deterministic JSON output for AI agent
integration. Token-optimized formats for LLM context efficiency. (From:
beads_viewer's `--robot-triage` and `--robot-plan` modes.)

### Reference Projects Studied

| Project | Stack | Key Insight Adopted |
|---------|-------|-------------------|
| [beads_viewer (bv)](https://github.com/Dicklesworthstone/beads_viewer) | Go + Bubble Tea | Graph metrics (PageRank, critical path), split-view dashboard, robot/agent output modes, file watching for live reload |
| [beads-task-issue-tracker](https://github.com/w3dev33/beads-task-issue-tracker) | Tauri + Nuxt 4 | Multi-dimensional filtering (type/status/priority/label/assignee), exclusion filters, epic hierarchy with collapsible groups |
| [beadspace](https://github.com/cameronsjo/beadspace) | Vanilla HTML/CSS/JS | Triage auto-flagging (stale P0/P1, misprioritized bugs), pure-CSS data viz without charting libs |
| [Beads-Kanban-UI](https://github.com/AvivK5498/Beads-Kanban-UI) | Next.js + Rust | Status donuts for multi-project overview, epic progress bars, memory panel for knowledge base, agents config panel |
| [beads-dashboard](https://github.com/rhydlewis/beads-dashboard) | React + Socket.IO | Lean metrics (lead time scatterplot, aging WIP, cumulative flow diagram, throughput histogram), real-time WebSocket sync |
| [beads-ui](https://github.com/mantoni/beads-ui) | React + Node.js | Zero-setup `bdui start --open`, live DB monitoring, full keyboard nav, multi-workspace support |
| [Perles](https://github.com/zjrosen/perles) | Go + TUI | BQL query-driven columns, dual-mode (kanban + search), dependency explorer with tree traversal, saved searches as columns |
| [Linear](https://linear.app) | — | Speed-as-feature, keyboard-first, opinionated defaults, taste-driven design, optimize for end users not buyers |

## Overview

A Charm-based (Bubble Tea + Bubbles + Lip Gloss) terminal dashboard for monitoring
oro's bead execution and agent activity. Runs as a peer panel alongside architect,
manager, and code editor. Invoked via `oro dash`.

## Tech Stack

- **Bubble Tea** — MVU architecture, alt-screen, tick-based live updates
- **Bubbles** — table, list, viewport, spinner components
- **Lip Gloss** — styling, borders, layout, color themes

## Architecture

```
cmd/oro-dash/main.go          Entry point (tea.NewProgram)
cmd/oro-dash/model.go          Root model, view routing, key bindings
cmd/oro-dash/board.go          Kanban board view (default)
cmd/oro-dash/detail.go         Bead detail drilldown (tabbed)
cmd/oro-dash/workers.go        Workers table view
cmd/oro-dash/tree.go           All-beads tree view
cmd/oro-dash/insights.go       Graph metrics & flow health
cmd/oro-dash/search.go         Fuzzy search overlay
cmd/oro-dash/theme.go          Linear-inspired color theme
cmd/oro-dash/datasource.go     Data fetching (bd CLI + dispatcher state)
cmd/oro-dash/graph.go          Dependency graph analysis (critical path, blockers)
```

## Data Sources

| Source | Method | Refresh |
|--------|--------|---------|
| Beads (backlog, status, deps) | `bd ready --json`, `bd list --json`, `bd show <id> --json` | 2s tick |
| Agent/worker status | Read dispatcher SQLite (`~/.oro/state.db`) or UDS query | 1s tick |
| Daemon health | PID file (`~/.oro/oro.pid`) + process check | 5s tick |
| Git worktree state | `git worktree list --porcelain` | 5s tick |
| Stats | `bd stats --json` | 10s tick |
| File system changes | Watch `.beads/beads.jsonl` for mutations (immediate refresh) | fsnotify |

**Live reload** (from beads_viewer): Watch the `.beads/` directory for file changes.
When the underlying data changes (e.g., another agent closes a bead), trigger
an immediate refresh instead of waiting for the next tick. This makes the dashboard
feel alive — changes from any source (CLI, agent, editor) appear instantly.

## Views

### 1. Kanban Board (Default View)

Linear-style board with 4 columns:

```
 ┌─ Ready ──────┐ ┌─ In Progress ─┐ ┌─ Blocked ─────┐ ┌─ Done (recent) ┐
 │ [P1] oro-ujb  │ │ ● oro-ujb.2   │ │ ⊘ oro-ujb.5   │ │ ✓ oro-98q      │
 │ [P2] oro-bat  │ │   worker: w-1 │ │   blocked by: │ │ ✓ oro-r7b      │
 │               │ │   ctx: 34%    │ │   ujb.2,ujb.3 │ │ ✓ oro-bat      │
 │               │ │ ● oro-ujb.3   │ │               │ │                │
 │               │ │   worker: w-2 │ │               │ │                │
 └───────────────┘ └───────────────┘ └───────────────┘ └────────────────┘
                          ↑ cursor here, Enter to drill down
```

**Card contents:**
- Priority badge (P0-P4, color-coded)
- Bead ID + title (truncated)
- Type icon (epic/task/bug/feature)
- If in_progress: assigned worker ID, context %, spinner
- If blocked: blocker IDs
- If epic: progress fraction (e.g., `3/5 done`) — from Beads-Kanban-UI

**Navigation:**
- `h/l` or `Tab/Shift-Tab` — move between columns
- `j/k` or arrows — move within column
- `Enter` — drill into selected bead
- `/` — fuzzy search overlay (searches ID, title, description)
- `1-4` — jump to column
- `r` — force refresh
- `q` — quit
- `?` — help overlay
- `y` — copy bead ID to clipboard (from Perles)

### 2. Bead Detail Drilldown (Enter on a card)

Split-view layout inspired by beads_viewer: left panel shows card list (dimmed),
right panel shows full detail. This preserves board context while drilling down.

Tabbed detail with 5 tabs:

```
 ┌─ oro-ujb.2 ─────────────────────────────────────────────────┐
 │ [Overview] [Worker] [Diff] [Deps] [Memory]                  │
 ├─────────────────────────────────────────────────────────────┤
 │ Title: Production BeadSource: shell out to bd ready/show    │
 │ Status: in_progress  Priority: P2  Type: task               │
 │ Owner: mraakashshah   Created: 2026-02-08                   │
 │                                                             │
 │ Acceptance Criteria:                                        │
 │ Unit tests with exec mock, integration test that calls      │
 │ real bd binary. Test: pkg/dispatcher/beadsource_test.go.    │
 │                                                             │
 │ Description:                                                │
 │ Implement BeadSource interface (dispatcher.go) with a       │
 │ production struct that shells out to 'bd ready --json'...   │
 └─────────────────────────────────────────────────────────────┘
```

**Tabs:**

| Tab | Content |
|-----|---------|
| **Overview** | Title, status, priority, type, owner, dates, acceptance criteria, description |
| **Worker** | Assigned worker ID, context %, heartbeat age, stdout tail (last 20 lines via viewport), ralph cycle count |
| **Diff** | `git diff` output from the worker's worktree (scrollable viewport) |
| **Deps** | Dependency tree — what this bead blocks and is blocked by, with status indicators. Traversable: `Enter` on a dep navigates to it (from Perles' dependency explorer) |
| **Memory** | Memory context that was injected into this bead's worker prompt (`memory.ForPrompt` output) |

**Navigation:**
- `Tab/Shift-Tab` or `1-5` — switch tabs
- `Esc` or `Backspace` — back to board
- `j/k` or arrows — scroll within tab viewport

### 3. Workers View (`w` to toggle)

Table of active agents with their assignments and health:

```
 ┌─ Active Workers ────────────────────────────────────────────┐
 │ ID    │ Bead       │ Status    │ Context │ Heartbeat │ Cyc  │
 │───────┼────────────┼───────────┼─────────┼───────────┼──────│
 │ w-1   │ oro-ujb.2  │ executing │ 34%     │ 2s ago    │ 0    │
 │ w-2   │ oro-ujb.3  │ executing │ 12%     │ 1s ago    │ 0    │
 │ w-3   │ —          │ idle      │ —       │ —         │ —    │
 └─────────────────────────────────────────────────────────────┘
```

- `Enter` on a worker row drills into its assigned bead detail
- Shows ralph cycle count, context %, last heartbeat
- Idle workers shown dimmed
- Color-coded heartbeat: green (<5s), amber (5-15s), red (>15s stale)

### 4. All Beads View (`a` to toggle)

Sortable table of every bead with parent/child and dependency info:

```
 ┌─ All Beads ─────────────────────────────────────────────────┐
 │ ID         │ Title                    │ P  │ Status │ Parent│
 │────────────┼──────────────────────────┼────┼────────┼───────│
 │ oro-ujb    │ Epic: End-to-end MVP     │ P1 │ open   │ —     │
 │  └ ujb.2   │ Production BeadSource    │ P2 │ done   │ ujb   │
 │  └ ujb.3   │ Production WorktreeMgr   │ P2 │ done   │ ujb   │
 │  └ ujb.5   │ Wire dispatcher.Run      │ P2 │ blocked│ ujb   │
 │    └→ ujb.6│ (blocked by ujb.6)       │    │        │       │
 └─────────────────────────────────────────────────────────────┘
```

- Tree-indented by parent/child hierarchy
- Dependency arrows shown inline
- Sort by: priority (`p`), status (`s`), created (`c`)
- Filter by status, type, or search (`/`)
- Epic rows show progress bar: `[████░░] 3/5` (from Beads-Kanban-UI)
- Collapsible groups: `Space` to toggle expand/collapse on epics (from beads-task-issue-tracker)

### 5. Insights View (`i` to toggle)

Graph-aware metrics panel inspired by beads_viewer and beads-dashboard. Shows
flow health and dependency analysis without leaving the terminal.

```
 ┌─ Flow Health ───────────────────────────────────────────────┐
 │                                                             │
 │ Critical Path: oro-ujb.5 → ujb.1 (2 hops, blocked)         │
 │ Bottlenecks:   ujb.2 (blocks 3), ujb.3 (blocks 2)          │
 │ Cycle Check:   ✓ No circular dependencies                   │
 │                                                             │
 │ Throughput (7d):  ████████████░░░░  12 closed                │
 │ Avg Lead Time:    3.2h                                      │
 │ Aging WIP:        2 items (both < 1d) ✓                     │
 │                                                             │
 │ Triage Flags:                                               │
 │  ⚠ oro-xyz: P0 open > 24h — needs attention                │
 │  ⚠ oro-abc: bug at P4 — consider reprioritizing            │
 └─────────────────────────────────────────────────────────────┘
```

**Metrics computed locally (from beads_viewer):**
- **Critical path**: Longest dependency chain through open beads
- **Bottleneck detection**: Beads with highest out-degree (block the most work)
- **Cycle detection**: Flag circular dependencies as errors
- **Throughput**: Beads closed per day/week (bar sparkline)
- **Lead time**: Average time from open to closed
- **Aging WIP**: Color-coded scatter (green <1d, amber <3d, red >3d)

**Triage auto-flags (from beadspace):**
- P0/P1 beads open > 24h without a worker
- Bugs filed at P3/P4 (likely misprioritized)
- Epics with all children closed but epic still open
- Beads with stale workers (heartbeat > 60s)

### 6. Search Overlay (`/` from any view)

Full-screen fuzzy search inspired by Perles and Linear's Cmd+K:

```
 ┌─ Search ────────────────────────────────────────────────────┐
 │ > ujb                                                       │
 ├─────────────────────────────────────────────────────────────┤
 │ ● oro-ujb      Epic: End-to-end MVP               P1 open  │
 │ ● oro-ujb.2    Production BeadSource               P2 done  │
 │ ● oro-ujb.3    Production WorktreeMgr              P2 done  │
 │ ● oro-ujb.5    Wire dispatcher.Run                 P2 block │
 └─────────────────────────────────────────────────────────────┘
```

- Fuzzy matches across bead ID, title, and description
- Results update as you type (no debounce — local data is fast)
- `Enter` navigates to selected bead's detail view
- `Esc` dismisses search, returns to previous view
- Filter prefixes: `p:0` (priority), `s:open` (status), `t:bug` (type)

### 7. Status Bar (always visible, bottom)

```
 ┌─────────────────────────────────────────────────────────────┐
 │ ● oro: running  │ w: 2/3 active  │ 3 ready 2 wip 1 blocked │
 │ last merge: 2m ago  │  lead: 3.2h  │  /:search ?:help q:quit│
 └─────────────────────────────────────────────────────────────┘
```

Condensed to single line when terminal height < 30 rows.

## Theme (Linear-inspired)

```go
var (
    // Column headers
    ColorReady      = lipgloss.Color("#6E56CF")  // Purple
    ColorInProgress = lipgloss.Color("#E5A836")  // Amber
    ColorBlocked    = lipgloss.Color("#E5484D")  // Red
    ColorDone       = lipgloss.Color("#30A46C")  // Green

    // Priority badges
    ColorP0 = lipgloss.Color("#E5484D")  // Critical — red
    ColorP1 = lipgloss.Color("#E5A836")  // High — amber
    ColorP2 = lipgloss.Color("#6E56CF")  // Medium — purple
    ColorP3 = lipgloss.Color("#889096")  // Low — gray
    ColorP4 = lipgloss.Color("#687076")  // Backlog — dim gray

    // Chrome
    ColorBorder = lipgloss.Color("#3E4347")
    ColorBg     = lipgloss.Color("#111113")
    ColorFg     = lipgloss.Color("#EDEEF0")
    ColorDim    = lipgloss.Color("#687076")

    // Heartbeat health (from worker monitoring)
    ColorHealthy = lipgloss.Color("#30A46C")  // Green — recent heartbeat
    ColorWarn    = lipgloss.Color("#E5A836")  // Amber — aging heartbeat
    ColorStale   = lipgloss.Color("#E5484D")  // Red — stale/dead worker
)
```

## Model Structure

```go
type Model struct {
    // View state
    activeView   View  // BoardView | DetailView | WorkersView | TreeView | InsightsView
    board        BoardModel
    detail       DetailModel
    workers      WorkersModel
    tree         TreeModel
    insights     InsightsModel
    search       SearchModel

    // Data
    beads        []Bead
    workerState  []WorkerStatus
    daemon       DaemonHealth
    stats        Stats
    graph        DependencyGraph  // computed DAG for critical path, bottlenecks

    // UI
    width, height int
    lastRefresh   time.Time
    err           error
}

type BoardModel struct {
    columns    [4]list.Model  // ready, in_progress, blocked, done
    activeCol  int
    filterText string
}

type DetailModel struct {
    bead       BeadDetail
    activeTab  int  // 0-4
    viewport   viewport.Model
    tabs       []string
}

type SearchModel struct {
    input      textinput.Model
    results    []Bead
    cursor     int
    active     bool
}

type InsightsModel struct {
    criticalPath []string       // bead IDs in longest chain
    bottlenecks  []Bottleneck   // beads ranked by out-degree
    throughput   []DailyCount   // closed per day, last 14 days
    leadTime     time.Duration  // average open→closed
    triageFlags  []TriageFlag   // auto-detected issues
}

type DependencyGraph struct {
    nodes map[string]*GraphNode
    // Computed lazily on data refresh
}
```

## Tick / Refresh Strategy

- **1s tick**: worker heartbeat status (lightweight — read SQLite)
- **2s tick**: bead list refresh (`bd list --json`)
- **5s tick**: daemon health, worktree state
- **fsnotify**: watch `.beads/` directory for immediate refresh on external changes
- **On Enter**: full bead detail fetch + git diff
- **On `r`**: force full refresh of everything
- **On view switch**: recompute view-specific data (e.g., graph metrics for insights)

## CLI Integration

```
oro dash              # Launch TUI dashboard
oro dash --no-color   # Disable colors (CI/piping)
oro dash --json       # Robot mode: dump current state as JSON and exit (for agents)
```

**Robot mode** (from beads_viewer): `oro dash --json` outputs a structured snapshot
of all dashboard data (beads, workers, bottlenecks, triage flags) as JSON. Useful
for AI agents that want a quick project health overview without launching the TUI.

Registered as a cobra subcommand in `cmd/oro/root.go`, but the TUI binary
lives in `cmd/oro-dash/` for clean separation.

## Implementation Phases

1. **Scaffold** — Bubble Tea program, root model, view routing, theme, `oro dash` command
2. **Kanban board** — 4 columns from `bd list --json`, card rendering, navigation
3. **Data source** — Live tick-based refresh, bd CLI integration, fsnotify watcher
4. **Search overlay** — Fuzzy search across all beads, filter prefixes
5. **Detail drilldown** — Split-view with tabbed detail (overview + deps tabs)
6. **Worker tab** — Live worker status from dispatcher state, heartbeat health colors
7. **Diff tab** — Git diff viewport for worktree changes
8. **Memory tab** — Memory context display
9. **All Beads tree** — Hierarchical table with collapsible epics, progress bars
10. **Insights view** — Dependency graph analysis, flow metrics, triage auto-flags
11. **Status bar** — Condensed daemon health, aggregate stats, help hints
12. **Robot mode** — `--json` flag for agent-friendly output
