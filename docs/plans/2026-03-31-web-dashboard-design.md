# Web Dashboard Design — Replacing oro mg

**Date:** 2026-03-31
**Status:** R1 FAIL (4 gaps fixed), ready for R2.

## Goal

Replace the slow bubbletea TUI (oro mg, 45K LOC) with a fast local web dashboard. Server-rendered HTML via Go templates, live updates via SSE, htmx for interactivity. No JS build step, no npm.

## Context: What Exists Today

**oro mg** (`pkg/mg/`, 45K LOC) is a Mardi Gras-themed bubbletea TUI with:
- Parade view: beads grouped by status (Queued Up → Rolling → Stalled → Finished)
- Detail panel: split-pane with scrollable viewport, lazy-loaded rich detail via `bd show`
- Header: counts (stalled/rolling/done), worker count, progress bar
- Features: fuzzy search, multi-select, age heat colors, blocker hints, toast notifications, confetti
- **Problem: slow initial load, unresponsive to keypresses**

**Dispatcher data sources** (all available today, no new plumbing needed):
- `SwarmHealth` struct via `applyHealth()` — daemon status, pane status, worker snapshots
- `statusResponse` via `oro status` — workers with state/beadID/context%/heartbeat, queue depth, assignments
- `BeadSource` interface — full bead objects with priority, AC, deps, labels
- `events` table — all dispatcher lifecycle events
- `assignments` table — worker-to-bead history
- 18 directives via UDS including `health`, `status`

## Architecture

### Stack

- **Server:** Go `net/http` inside dispatcher process, enabled via `oro start --web` flag
- **Port:** `:4444` (hardcoded default, configurable via `--web-addr`)
- **Templates:** Go `html/template` for server-rendered HTML
- **Interactivity:** htmx for partial page updates
- **Live updates:** Server-Sent Events (SSE) push dispatcher events to browser
- **Styling:** Vanilla CSS, no framework. Clean, informational, not festive.

### Why Inside the Dispatcher

The dispatcher has all state in memory — worker pool, bead queue, assignments, health, events. A separate binary would need to poll via UDS and rebuild state, adding latency for no benefit. The web server is a read-only view over dispatcher internals, same as `applyHealth()` already is.

`--web` flag keeps it opt-in. When disabled, zero overhead. When enabled, one extra goroutine serving HTTP. Panics caught by existing `safeGo()` pattern.

### Data Flow

```
Browser (localhost:4444)
    ↓ GET / (initial page load)
Dispatcher HTTP handler
    → reads SwarmHealth, BeadSource, events table
    → renders full HTML via Go templates
    → returns to browser

Browser
    ↓ SSE connection (/events)
Dispatcher SSE handler
    → pushes events as they happen (bead completed, QG failed, worker heartbeat, etc.)
    → browser uses htmx to swap updated HTML fragments

Browser
    ↓ htmx GET /fragments/parade (triggered by SSE)
Dispatcher fragment handler
    → re-renders just the parade section
    → returns HTML fragment, htmx swaps it in
```

## Design Direction

**Linear meets Mardi Gras.** Linear's layout discipline and information density, but wearing Mardi Gras colors and speaking in parade metaphors. Clean and fast, but with personality.

**From Linear:**
- Dark background (#0A0A0B), not pure black
- Minimal chrome — separation through spacing and subtle background shifts, not borders
- Clean sans-serif typography, generous line height, muted secondary text
- Status colors as small accents (dots/icons), not full-row highlighting
- Compact density — tight rows, no wasted vertical space

**From oro mg (keeping the whimsy):**
- Mardi Gras color palette — purple (#9B59B6), gold (#F1C40F), green (#2ECC71) as accents
- Parade metaphor and section names
- Status symbols (♪ ● ⊘ ✓)
- Bead string decorative separator — subtle CSS shimmer animation (slow gradient shift, 5-10 second cycle, barely noticeable until you stare at it)
- Confetti on epic completion (CSS-only animation)
- Age-based heat gradient on bead IDs (green → gold → red over 30 days)

## Layout

Single page, two-column layout. Parade dominates left two-thirds. Right sidebar stacks three compact panels.

```
┌─────────────────────────────┬──────────────────┐
│  ◆─◇─◆─◇─◆─◇─◆─◇─◆─◇─◆  │  Worker Grid     │
│                             │  w1 ● oro-abc 42%│
│  Parade                     │  w2 ● oro-def 18%│
│                             │  w3 ○ idle       │
│  ♪ Queued Up                ├──────────────────┤
│    oro-xyz  Fix the thing   │  Event Feed      │
│                             │  12:01 ✓ merged  │
│  ● Rolling                  │  12:00 ✗ QG fail │
│    oro-abc  Add feature     │  11:58 ⚠ stuck   │
│    oro-def  Write tests     │  11:55 ✓ merged  │
│                             ├──────────────────┤
│  ⊘ Stalled                  │  Throughput      │
│    oro-ghi  blocked → xyz   │  3.2 beads/hr    │
│                             │  $1.80/hr        │
│  ✓ Finished                 │  4/4 workers     │
│    oro-jkl  Done            │  uptime 2h 14m   │
└─────────────────────────────┴──────────────────┘
```

## Panels

### 1. Parade (left, primary)

The parade metaphor from oro mg — beads are a procession flowing through the pipeline. The dispatcher moves them, not the user. You watch, not drag.

**Sections (top to bottom):**
- **Queued Up** (♪) — ready beads, no blockers, waiting for a worker
- **Rolling** (●) — in progress, worker assigned
- **Stalled** (⊘) — blocked on dependencies or stuck worker
- **Finished** (✓) — completed, collapsed by default

**Each bead card shows:**
- Status symbol + bead ID (age-based heat color: green → gold → red over 30 days)
- Title (truncated)
- Priority badge
- Worker badge (if assigned — worker ID)
- Blocker hint (if stalled — "→ oro-xyz" with blocking bead ID and title)

**Click a bead** → expands detail inline (AC, description, dependencies, worker info). Uses htmx to fetch `/fragments/detail/{id}` and swap below the card.

**Data source:** `BeadSource.Ready()` for Queued Up, `BeadSource.InProgress()` for Rolling. Stalled and Finished require new BeadSource methods (see "BeadSource Extensions" below). Dispatcher's `snapshotWorkers()` for worker assignments. Bead dependency graph for blocker hints.

### 2. Worker Grid (right sidebar, top)

**Each worker row:**
- Worker ID (short hash)
- State indicator: ● busy (green), ○ idle (dim), ⚠ stuck (amber)
- Current bead ID (if busy)
- Context % — numeric + thin bar. Color shifts green → amber → red as context grows.
- Heartbeat age — "4s ago", "32s ago". Amber if >30s.

**Data source:** `snapshotWorkers()` — already returns ID, state, beadID, contextPct, lastSeen for each worker.

### 3. Event Feed (right sidebar, middle)

Scrolling list of recent dispatcher events, newest on top. Auto-scrolls as new events arrive via SSE.

**Event types shown:**
- ✓ `merged` — bead ID, branch
- ✗ `quality_gate_rejected` — bead ID
- ⚠ `merge_conflict` — bead ID
- ⚠ `qg_stuck_detected` — bead ID, worker ID
- ↻ `handoff` — bead ID, old worker → new worker
- ▲ `escalation` — bead ID, type
- ✓ `epic_acceptance_passed` — epic ID
- ✗ `epic_acceptance_failed` — epic ID

Each event: timestamp (HH:MM) + symbol + short description.

**Data source:** `events` table, last 50 events. SSE pushes new events as they're logged via `logEvent()`.

### 4. Throughput (right sidebar, bottom)

Compact numbers panel:
- **Beads/hour** — completed beads in last hour (from `events` table, type=`merged`)
- **$/hour** — requires cost data from DonePayload (depends on memory spec's P1c cost pipeline — if not available yet, show "—")
- **Workers** — "3/4 active" (active count / target count)
- **Uptime** — dispatcher uptime from `SwarmHealth.Daemon.UptimeSeconds`

**Data source:** Computed from `events` table (bead count) + `SwarmHealth` (workers, uptime). Cost depends on the cost pipeline from the enterprise readiness work — degrade gracefully if unavailable.

## SSE Event Stream

The dispatcher emits events via `logEvent()` (133 call sites) and `logEventLocked()` (7 call sites). The SSE handler maintains a channel per connected browser client. When either function fires, it also pushes to all SSE channels.

**Implementation:** Add an `SSEBroadcaster` to the Dispatcher struct:
```go
type SSEBroadcaster struct {
    mu       sync.RWMutex
    clients  map[chan string]bool
}

func (b *SSEBroadcaster) Send(eventType, beadID, workerID string) // non-blocking, drops on full channel
func (b *SSEBroadcaster) Subscribe() chan string                    // returns new client channel
func (b *SSEBroadcaster) Unsubscribe(ch chan string)               // removes client on disconnect
```

Both `logEvent()` (line ~3617) and `logEventLocked()` (line ~3627) call `d.sseBroadcaster.Send()` after the SQL INSERT. SSEBroadcaster.Send() is non-blocking — if a client channel is full, the event is dropped for that client (prevents slow browsers from blocking the dispatcher). On write failure (broken pipe), the client is removed.

SSEBroadcaster is initialized in `New()` even when WebEnabled=false — uses the same struct with zero clients, so Send() is a no-op (iterates empty map). No nil checks needed in the logEvent hot path.

**SSE message format:**
```
event: parade-update
data: {}

event: worker-update
data: {}

event: new-event
data: {"type":"merged","bead_id":"oro-abc","time":"12:01"}
```

Browser-side htmx triggers:
- `parade-update` → `hx-get="/fragments/parade"` swaps parade panel
- `worker-update` → `hx-get="/fragments/workers"` swaps worker grid
- `new-event` → prepend to event feed

## Implementation

### BeadSource Extensions

The existing `BeadSource` interface has `Ready()` and `InProgress()` but no way to get blocked or closed beads. Add:

```go
// In pkg/dispatcher/dispatcher.go BeadSource interface:
Blocked(ctx context.Context) ([]protocol.Bead, error)   // beads with unresolved blocking deps
Closed(ctx context.Context, limit int) ([]protocol.Bead, error)  // recently closed beads
```

`CLIBeadSource` implements these by shelling out to `bd list --status=blocked --json` and `bd list --status=closed --json --limit N`.

### Public Interface for pkg/web

`pkg/web/` cannot access unexported Dispatcher fields. Define a public interface:

```go
// In pkg/web/server.go:
type DashboardData interface {
    Health() (dispatcher.SwarmHealth, error)
    ReadyBeads(ctx context.Context) ([]protocol.Bead, error)
    InProgressBeads(ctx context.Context) ([]protocol.Bead, error)
    BlockedBeads(ctx context.Context) ([]protocol.Bead, error)
    ClosedBeads(ctx context.Context, limit int) ([]protocol.Bead, error)
    ShowBead(ctx context.Context, id string) (*protocol.BeadDetail, error)
    RecentEvents(ctx context.Context, limit int) ([]Event, error)
    SubscribeSSE() chan string
    UnsubscribeSSE(ch chan string)
}
```

The Dispatcher implements this interface. `pkg/web` handlers accept `DashboardData`, never touch Dispatcher internals directly.

### Dispatcher Changes

**`pkg/dispatcher/dispatcher.go`:**
- Add `SSEBroadcaster` field to Dispatcher struct, initialize in `New()` (even when web disabled — empty-clients no-op)
- Add `WebAddr string`, `WebEnabled bool` to Config
- In `Run()`: if WebEnabled, start HTTP server goroutine via `safeGo`. Store `*http.Server` on Dispatcher struct. If bind fails, log `web_server_bind_failed` event and continue (non-fatal — dispatcher works without dashboard).
- In `logEvent()` AND `logEventLocked()`: call `d.sseBroadcaster.Send()` after SQL INSERT
- In `withDefaults()`: WebAddr defaults to `:4444`
- In `shutdownWithTimeout()`: call `d.httpServer.Shutdown(ctx)` before `d.wg.Wait()` if httpServer is non-nil
- Implement `DashboardData` interface methods (thin wrappers over existing BeadSource/health/events)

### New Package: `pkg/web/`

**`pkg/web/server.go`** — HTTP handler setup:
- `GET /` — full page render
- `GET /fragments/parade` — parade HTML fragment
- `GET /fragments/workers` — worker grid fragment
- `GET /fragments/detail/{id}` — bead detail fragment
- `GET /fragments/events` — recent events fragment
- `GET /fragments/throughput` — throughput numbers fragment
- `GET /events` — SSE endpoint

**`pkg/web/sse.go`** — SSEBroadcaster implementation

**`pkg/web/templates/`** — Go HTML templates:
- `layout.html` — page shell, CSS, htmx script tag
- `parade.html` — parade sections with bead cards
- `workers.html` — worker grid rows
- `detail.html` — expanded bead detail
- `events.html` — event feed entries
- `throughput.html` — numbers panel

**`pkg/web/static/`** — embedded via `go:embed`:
- `style.css` — vanilla CSS, grid layout, status colors
- htmx JS (single file, ~14KB gzipped, vendored)

### CLI Changes

**`cmd/oro/cmd_start.go`:**
- Add `--web` flag (default false)
- Add `--web-addr` flag (default `:4444`)
- Pass to Config

### Files Modified
- `pkg/dispatcher/dispatcher.go` — SSEBroadcaster field + init in New(), WebAddr/WebEnabled Config, Run() HTTP server via safeGo, *http.Server stored for shutdown, logEvent() AND logEventLocked() SSE broadcast, withDefaults() WebAddr default, shutdownWithTimeout() HTTP shutdown, DashboardData interface implementation, BeadSource interface extended with Blocked()/Closed()
- `cmd/oro/cmd_start.go` — --web and --web-addr flags, pass WebAddr/WebEnabled to buildDispatcher() (line ~636-646)

### Files Created
- `pkg/web/server.go` — HTTP handlers
- `pkg/web/server_test.go`
- `pkg/web/sse.go` — SSE broadcaster
- `pkg/web/sse_test.go`
- `pkg/web/templates/*.html` — HTML templates (6 files)
- `pkg/web/static/style.css`
- `pkg/web/static/htmx.min.js` (vendored)
- `pkg/web/embed.go` — `go:embed` for templates + static

## What We're NOT Doing

- No React, Preact, Alpine, or any JS framework — htmx + SSE only
- No npm, no node, no JS build step
- No drag-and-drop on kanban — the dispatcher moves beads, not the user
- No heavy animations — bead string shimmer is CSS-only (no JS), confetti is CSS-only on epic completion
- No authentication — localhost only
- No cost panel data until cost pipeline lands (degrade to "—")
- No mobile responsiveness — this is a dev tool on localhost
- No replacing oro mg's data layer (`pkg/mg/data/`) — web dashboard reads from dispatcher directly, not through bd CLI

## What We ARE Keeping from oro mg

- Parade metaphor and status groupings (Queued Up/Rolling/Stalled/Finished)
- Status symbols (♪ ● ⊘ ✓)
- Mardi Gras color palette (purple/gold/green accents)
- Bead string decorative separator with subtle CSS shimmer animation
- Age-based heat colors on bead IDs
- Blocker hints (→ blocking bead)
- Priority badges
- Worker badges on beads
- Collapsible "Finished" section
- Confetti on epic completion

## Risk Assessment

| Risk | Severity | Mitigation |
|------|----------|------------|
| SSE connections leak if browser tabs close uncleanly | Low | SSEBroadcaster detects closed channels on write failure, removes client |
| Template rendering slow with 100+ beads | Low | Templates are simple string concatenation, not DOM diffing. 100 beads = ~10KB HTML. |
| htmx adds 14KB JS | Low | Single vendored file, no CDN dependency, cached after first load |
| Web server panic takes down dispatcher | Low | Started via safeGo() which recovers panics. Dispatcher continues. |
