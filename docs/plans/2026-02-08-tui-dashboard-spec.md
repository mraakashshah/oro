# Oro TUI Dashboard Spec

**Date:** 2026-02-08
**Status:** Draft

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
cmd/oro-dash/agents.go         Active agents panel
cmd/oro-dash/theme.go          Linear-inspired color theme
cmd/oro-dash/datasource.go     Data fetching (bd CLI + dispatcher state)
```

## Data Sources

| Source | Method | Refresh |
|--------|--------|---------|
| Beads (backlog, status, deps) | `bd ready --json`, `bd list --json`, `bd show <id> --json` | 2s tick |
| Agent/worker status | Read dispatcher SQLite (`~/.oro/state.db`) or UDS query | 1s tick |
| Daemon health | PID file (`~/.oro/oro.pid`) + process check | 5s tick |
| Git worktree state | `git worktree list --porcelain` | 5s tick |
| Stats | `bd stats --json` | 10s tick |

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

**Navigation:**
- `h/l` or `Tab/Shift-Tab` — move between columns
- `j/k` or arrows — move within column
- `Enter` — drill into selected bead
- `/` — filter/search beads (fuzzy)
- `1-4` — jump to column
- `r` — force refresh
- `q` — quit
- `?` — help overlay

### 2. Bead Detail Drilldown (Enter on a card)

Tabbed view with 5 tabs:

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
| **Deps** | Dependency tree — what this bead blocks and is blocked by, with status indicators |
| **Memory** | Memory context that was injected into this bead's worker prompt (`memory.ForPrompt` output) |

**Navigation:**
- `Tab/Shift-Tab` or `1-5` — switch tabs
- `Esc` or `Backspace` — back to board
- `j/k` or arrows — scroll within tab viewport

### 3. Status Bar (always visible, bottom)

```
 ┌─────────────────────────────────────────────────────────────┐
 │ ● dispatcher: running (PID 12345)  │ workers: 2/2 active   │
 │ beads: 3 ready, 2 in_progress, 1 blocked, 51 closed        │
 │ last merge: 2m ago  │  lead time: 3.2h  │  q:quit ?:help   │
 └─────────────────────────────────────────────────────────────┘
```

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
)
```

## Model Structure

```go
type Model struct {
    // View state
    activeView   View  // BoardView | DetailView
    board        BoardModel
    detail       DetailModel

    // Data
    beads        []Bead
    workers      []WorkerStatus
    daemon       DaemonHealth
    stats        Stats

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
```

## Tick / Refresh Strategy

- **1s tick**: worker heartbeat status (lightweight — read SQLite)
- **2s tick**: bead list refresh (`bd list --json`)
- **5s tick**: daemon health, worktree state
- **On Enter**: full bead detail fetch + git diff
- **On `r`**: force full refresh of everything

## CLI Integration

```
oro dash              # Launch TUI dashboard
oro dash --no-color   # Disable colors (CI/piping)
```

Registered as a cobra subcommand in `cmd/oro/root.go`, but the TUI binary
lives in `cmd/oro-dash/` for clean separation.

## Implementation Phases

1. **Scaffold** — Bubble Tea program, root model, view routing, theme, `oro dash` command
2. **Kanban board** — 4 columns from `bd list --json`, card rendering, navigation
3. **Data source** — Live tick-based refresh, bd CLI integration, SQLite reader
4. **Detail drilldown** — Tabbed view with overview + deps tabs
5. **Worker tab** — Live worker status from dispatcher state
6. **Diff tab** — Git diff viewport for worktree changes
7. **Memory tab** — Memory context display
8. **Status bar** — Daemon health, aggregate stats, help hints
