# Dash Phase 4: Unified Status Dashboard with Sparklines

**Date:** 2026-02-23
**Status:** DRAFT (post adversarial review — gaps fixed)
**Depends on:** Phase 3 (list view) — but can be implemented independently

## Goal

Replace the separate Health (`H`) and Workers (`w`) views with a single unified Status Dashboard (`s`). Add in-memory time-series collection with Unicode sparklines for throughput, queue depth, and worker utilization. Transform oro-dash from a state viewer into a live operational monitoring tool.

## Design Philosophy

**Glanceable health.** A swarm operator should be able to assess pipeline health in under 2 seconds by looking at three sparklines: throughput (are we making progress?), queue depth (how much is left?), worker utilization (are resources engaged?).

**Trends over snapshots.** The current dashboard shows "3 workers active." The new dashboard shows "3 workers active, utilization has been steady at 100% for 20 minutes" via a sparkline. Trends reveal problems that snapshots hide — a slowly draining queue looks fine at any single point but the sparkline shows it plateauing.

**In-memory, session-scoped.** Data lives in a ring buffer that starts fresh each `oro dash` session. No persistence, no SQLite. The buffer holds ~30 minutes of 2-second samples (900 data points). This is enough for live monitoring without storage complexity.

---

## Architecture

### View Change

The unified Status Dashboard **replaces** `HealthView` and `WorkersView`:

- `s` or `S` opens the Status Dashboard (new `StatusView`)
- `H` and `w` become aliases for `s` (backwards compatibility)
- The old `health.go` and `workers_table.go` rendering code is deprecated but kept for robot mode compatibility

### Ring Buffer (Time-Series Store)

```go
// In-memory ring buffer for time-series metrics.
// Stores ~30 minutes of data at 2-second sample intervals (900 samples).
type MetricsBuffer struct {
    mu        sync.RWMutex
    samples   [900]MetricsSample  // fixed-size ring
    head      int                 // next write index
    count     int                 // number of valid samples (0-900)
    startTime time.Time           // when buffer was created (session start)
}

type MetricsSample struct {
    Timestamp     time.Time

    // Pipeline metrics
    BeadsClosed   int     // cumulative beads closed since session start
    QueueReady    int     // beads in ready state
    QueueWIP      int     // beads in_progress
    QueueBlocked  int     // beads blocked

    // Worker metrics
    WorkersActive int     // workers in executing state
    WorkersIdle   int     // workers in idle state
    WorkersTotal  int     // total worker count

    // Per-worker snapshots (for per-worker sparklines)
    Workers []WorkerSample
}

type WorkerSample struct {
    ID         string
    ContextPct int
    State      string  // executing, idle, etc.
    BeadID     string
}
```

**Sampling:** On every 2-second tick (same tick that fetches beads/workers), a new `MetricsSample` is appended to the ring buffer. No additional fetches required — the sample is derived from data already fetched.

**Derived metrics computed at render time:**

| Metric | Computation |
|--------|-------------|
| Throughput (beads/hr) | `(latest.BeadsClosed - oldest.BeadsClosed) / timeDelta * 3600` |
| Throughput sparkline | Delta between consecutive `BeadsClosed` values, quantized to 8 levels |
| Queue depth sparkline | `QueueReady + QueueWIP` per sample |
| Worker utilization % | `WorkersActive / WorkersTotal * 100` per sample |
| Worker utilization sparkline | Utilization % per sample, quantized to 8 levels |
| Per-worker context sparkline | `ContextPct` per sample for each worker ID |
| Done count per worker | Track `BeadsClosed` transitions where a worker was assigned the closing bead |

### Sparkline Rendering

Use Unicode block characters (8 levels) — no external library needed:

```go
var sparkBlocks = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// renderSparkline takes a slice of float64 values (0.0-1.0 normalized)
// and returns a string of Unicode block characters.
func renderSparkline(values []float64, width int) string {
    // Downsample if len(values) > width
    // Quantize each value to nearest block character
    // Return colored string
}
```

**Sparkline width:** 20 characters (covers ~40 seconds of history at default zoom, or the full buffer downsampled).

**Color:** Sparklines use the metric's semantic color:
- Throughput: `ColorDone` (green) — higher is better
- Queue depth: `ColorInProgress` (amber) — context-dependent
- Worker utilization: `ColorReady` (purple) — higher is better
- Context % per worker: health colors (green <50%, amber 50-80%, red >80%)

---

## Layout

### Full Status Dashboard

```
┌─ Status ─────────────────────────────────────────────────────────┐
│                                                                   │
│  ── System ───────────────────────────────────────────────────── │
│  Daemon: ● online (PID 28797) · Uptime: 47m                      │
│  Architect: ● alive (2s ago)   Manager: ● alive (1s ago)          │
│                                                                   │
│  ── Pipeline ──────────────────────────────────────────────────  │
│  Throughput    ▁▂▃▅▇█▇▅▃▅▇█▇▅▃▅▇█▇▅  4.2/hr (12 done)          │
│  Queue         █▇▅▃▂▁▁▁▂▃▂▁▁▁▂▃▂▁▁▁  3 ready · 2 wip           │
│  Utilization   ▅▅▅▅▅▅▃▃▅▅▅▅▅▅▅▅▃▃▅▅  67% (2/3 active)          │
│                                                                   │
│  ── Workers ──────────────────────────────────────────────────── │
│  w-1  ● executing  oro-evtf        34%  ● 2s ago                 │
│       ▁▂▃▅▇  2 done  0 fail  cycle: 0  elapsed: 12m              │
│                                                                   │
│  w-2  ● executing  oro-frg2.1      12%  ● 1s ago                 │
│       ▁▁▂▃▃  1 done  0 fail  cycle: 0  elapsed: 8m               │
│                                                                   │
│  w-3  ○ idle       —                —    ● 0s ago                 │
│       ▁▁▁▁▁  0 done  0 fail  cycle: 0  elapsed: —                │
│                                                                   │
│  ── Session ──────────────────────────────────────────────────── │
│  Handoffs: 2 · Respawns: 1 · QG runs: 14 · QG pass: 92%         │
│                                                                   │
└───────────────────────────────────────────────────────────────────┘
```

### Sections

#### 1. System (2-3 lines)

| Field | Source | Styling |
|-------|--------|---------|
| Daemon status | `healthData.DaemonPID`, `healthData.DaemonState` | ● green if alive, ● red if dead |
| Uptime | `statusResponse.UptimeSeconds` (already fetched, currently unused) | Formatted as `Xh Ym` or `Xm` |
| Architect pane | `healthData.ArchitectPane.Alive`, `.LastActivity` | ● green/red + relative time |
| Manager pane | `healthData.ManagerPane.Alive`, `.LastActivity` | ● green/red + relative time |

Layout: Daemon on its own line (most important). Architect and Manager on same line (secondary).

#### 2. Pipeline (3 lines + sparklines)

| Metric | Sparkline | Current Value | Source |
|--------|-----------|---------------|--------|
| Throughput | 20-char, green | `X.X/hr (N done)` | Derived from `BeadsClosed` delta |
| Queue | 20-char, amber | `N ready · N wip` | From `QueueReady` + `QueueWIP` |
| Utilization | 20-char, purple | `N% (X/Y active)` | From `WorkersActive`/`WorkersTotal` |

Each line format: `MetricLabel  sparkline  currentValue`

Sparkline represents the last 20 samples (40 seconds at 2s intervals) by default. The full 30-minute buffer is available but downsampled to fit 20 chars.

#### 3. Workers (2 lines per worker)

**Line 1:**
```
w-1  ● executing  oro-evtf        34%  ● 2s ago
```

| Column | Width | Content |
|--------|-------|---------|
| Worker ID | 5 | `w-1` |
| Health dot | 2 | `●` colored by heartbeat age |
| Status | 10 | `executing`, `idle`, etc. |
| Bead ID | 14 | Assigned bead or `—` |
| Context % | 4 | `34%` or `—` |
| Heartbeat | 8 | `● Ns ago` colored by age |

**Line 2:**
```
      ▁▂▃▅▇  2 done  0 fail  cycle: 0  elapsed: 12m
```

| Column | Content |
|--------|---------|
| Context sparkline | 5-char sparkline of context % over last 10 samples |
| Done count | Beads completed by this worker this session |
| Fail count | QG failures or task failures |
| Cycle count | Ralph/handoff cycles (context exhaustion restarts) |
| Elapsed | Time since worker was last assigned a task |

**Idle workers** are dimmed (muted foreground). Active workers use full foreground.

**Worker ordering:** Active workers first (sorted by worker ID), then idle workers.

#### 4. Session (1 line)

Aggregate session counters:

| Metric | Source |
|--------|--------|
| Handoffs | `statusResponse.PendingHandoffCount` (already fetched) |
| Respawns | Count of worker state transitions to "idle" that were preceded by "executing" |
| QG runs | Total quality gate invocations this session |
| QG pass rate | `passed / total * 100` |

Note: Some of these counters (respawns, QG runs, QG pass rate) require tracking state transitions. The ring buffer captures worker states on each tick; transitions are detected by comparing consecutive samples.

---

## Data Collection

### What's Already Available (No New Fetches)

| Data | Source | Currently Used | Phase 4 Usage |
|------|--------|----------------|---------------|
| Beads by status | `beadsMsg` | Board columns, counts | Queue depth sparkline |
| Worker states | `workerDataMsg` | Workers table | Utilization sparkline, per-worker sparklines |
| Context % | `workerDataMsg.ContextPct` | Workers table column | Context sparkline per worker |
| Heartbeat age | `workerDataMsg.LastProgressSecs` | Health badge | Health dot + relative time |
| Daemon PID/state | `healthDataMsg` | Health view | System section |
| Pane alive/activity | `healthDataMsg` | Health view | System section |
| Uptime seconds | `statusResponse.UptimeSeconds` | **NOT USED** | System section uptime |
| Pending handoffs | `statusResponse.PendingHandoffCount` | **NOT USED** | Session handoff counter |
| Attempt counts | `statusResponse.AttemptCounts` | **NOT USED** | Session QG/retry counters |

### What Needs Derivation (Computed from Ring Buffer)

| Metric | Computation |
|--------|-------------|
| Throughput (beads/hr) | `ΔBeadsClosed / ΔTime * 3600` between first and last sample |
| Throughput sparkline | Per-sample `ΔBeadsClosed`, normalized, quantized to 8 levels |
| Queue sparkline | `QueueReady + QueueWIP` per sample, normalized to max |
| Utilization sparkline | `WorkersActive / max(WorkersTotal, 1) * 100` per sample |
| Per-worker context sparkline | Filter samples by worker ID, extract `ContextPct` |
| Done count per worker | Count transitions where `sample[n].Workers[id].BeadID != sample[n-1].Workers[id].BeadID` and old bead moved to closed |
| Fail count per worker | Count transitions where worker goes from executing to idle without bead closing |
| Respawn count | Count transitions where a worker ID disappears and reappears |

### Sampling Integration

In `model.go`, in the `Update()` method, after processing `beadsMsg` and `workerDataMsg`:

```go
case beadsMsg:
    m.beads = msg
    // ... existing code ...
    m.metricsBuffer.Record(m.buildCurrentSample())

case workerDataMsg:
    m.workers = msg.Workers
    // ... existing code ...
    // Sample recorded on beadsMsg (both arrive on same tick)
```

Or record on every tick callback:

```go
case tickMsg:
    m.metricsBuffer.Record(m.buildCurrentSample())
    return m, tea.Batch(fetchBeadsCmd(), fetchWorkersCmd(), fetchHealthCmd(), tickCmd())
```

**Preferred:** Record on `tickMsg` — ensures consistent 2-second intervals regardless of message ordering.

---

## Keyboard Navigation

### In Status Dashboard

| Key | Action |
|-----|--------|
| `j` / `k` / `↑` / `↓` | Scroll viewport (dashboard may be taller than terminal) |
| `Enter` | On a worker row: navigate to that worker's assigned bead detail |
| `Esc` | Return to previous view (ListView) |
| `?` | Help overlay |
| `q` / `Ctrl+C` | Quit |

### Global Key Aliases

| Key | Action |
|-----|--------|
| `s` or `S` | Open Status Dashboard |
| `H` | Alias for `s` (backwards compat — was HealthView) |
| `w` | Alias for `s` (backwards compat — was WorkersView) |

---

## Responsive Layout

| Width | Adaptation |
|-------|------------|
| > 120 cols | Full layout as shown |
| 100–120 cols | Worker line 2: hide `cycle` and `elapsed` columns |
| 80–100 cols | Sparklines shrink to 10 chars. Worker line 2: hide `fail` and `cycle` |
| < 80 cols | No sparklines. Workers show 1 line only. Pipeline shows current values only. |

---

## Theme Additions

```go
// New styles for Status Dashboard
StatusSection    lipgloss.Style  // Section header ("── System ──")
SparkGreen       lipgloss.Style  // Throughput sparkline color
SparkAmber       lipgloss.Style  // Queue depth sparkline color
SparkPurple      lipgloss.Style  // Utilization sparkline color
WorkerActive     lipgloss.Style  // Active worker row foreground
WorkerIdle       lipgloss.Style  // Idle worker row (muted)
```

---

## Premortem Analysis

### Decision: Merge Health + Workers into unified view

| Category | Risk | Severity | Mitigation |
|----------|------|----------|------------|
| Tiger | Users who memorized `H` and `w` are confused | Low | Both keys alias to `s`. Help overlay shows the mapping. |
| Tiger | Robot mode output changes break agent consumers | Medium | Keep old `health.go`/`workers_table.go` rendering for `--json` mode. New view is TUI only. |
| Paper tiger | "Too much info on one screen" | — | Sections are scannable. System is 2 lines. Pipeline is 3 lines. Workers scale with count. |

### Decision: In-memory ring buffer (no persistence)

| Category | Risk | Severity | Mitigation |
|----------|------|----------|------------|
| Tiger | Buffer fills after 30 min, old data lost | Low | 30 min is the monitoring window. Older data is noise for live ops. |
| Tiger | Dashboard restart loses all history | Low | Expected behavior for a live monitor. Session metrics start fresh. |
| Paper tiger | "We'll need persistence later" | — | Ring buffer is a clean abstraction. Can add SQLite persistence behind same interface if needed. |

### Decision: 2-second sample interval

| Category | Risk | Severity | Mitigation |
|----------|------|----------|------------|
| Elephant | 900 samples * N workers in memory | Low | Each sample is ~200 bytes. 900 * 200 = 180KB. With 10 workers: 1.8MB. Negligible. |
| Tiger | 2s is too coarse for fast events (worker crash + respawn within 2s) | Low | Events that happen faster than 2s are transient. The sample captures the resulting state. |

### Decision: Throughput computed from BeadsClosed delta

| Category | Risk | Severity | Mitigation |
|----------|------|----------|------------|
| Tiger | `BeadsClosed` is a count from `bd list --status=closed`. If the close count doesn't change monotonically (e.g., a bead is re-opened), throughput goes negative. | Medium | Clamp delta to ≥0. Re-opening a closed bead is extremely rare in oro workflows. |
| Elephant | Early in a session, throughput is meaningless (0 done in first 5 minutes) | Low | Show `—` for throughput until at least 2 beads are closed or 10 minutes have elapsed. |

### Decision: Worker done/fail counts from state transitions

| Category | Risk | Severity | Mitigation |
|----------|------|----------|------------|
| Tiger | Worker ID recycling (new worker gets same ID) misattributes history | Low | Worker IDs in oro are UUIDs. No recycling. |
| Tiger | Worker completes bead between two samples (assigned and done within 2s) — the bead never appears in a sample | Low | The bead will show up in `BeadsClosed` count. Per-worker attribution is best-effort. |
| Elephant | Fail detection heuristic (executing→idle without bead closing) can false-positive during normal shutdown | Medium | Don't count transitions during `oro stop`. Track daemon state — if daemon is shutting down, suppress fail counting. |

---

## Implementation Tasks (High-Level)

### Task 0: Ring buffer implementation
- Create `cmd/oro-dash/metrics.go` with `MetricsBuffer`, `MetricsSample`, `WorkerSample`
- Implement `Record()`, `Last(n)`, `Range(from, to)`, `Len()`
- Thread-safe with `sync.RWMutex`
- Test: concurrent read/write safety, ring wrap-around, Last(n) with fewer than n samples

### Task 1: Sparkline renderer
- Create `cmd/oro-dash/sparkline.go` with `renderSparkline(values []float64, width int, color lipgloss.Color) string`
- 8-level Unicode blocks (`▁▂▃▄▅▆▇█`)
- Normalize to local min/max within the window
- Handle edge cases: all zeros, single value, width > len(values)
- Test: known input → expected output, edge cases

### Task 2: Expand fetch.go + add closedCount
- **CRITICAL (from adversarial review):** Add `UptimeSeconds float64`, `PendingHandoffCount int`, `AttemptCounts map[string]int` to `statusResponse` struct in `cmd/oro-dash/fetch.go` — these fields exist in the dispatcher's JSON but are silently dropped by the dashboard's local struct
- Either expand `fetchWorkerStatus()` return to a richer struct or add new fields to the `workerDataMsg`
- **CRITICAL:** Add `closedCount int` to `Model`. In `beadsMsg` handler, count beads with `status == "closed"` (currently only `open` and `in_progress` are counted)
- Test: verify new fields are parsed from dispatcher response, closedCount matches bead data

### Task 3: Sample collection wiring
- Add `metricsBuffer *MetricsBuffer` field to `Model`
- Initialize in `newModel()`
- **Sampling contract (from adversarial review):** Record sample AFTER both `beadsMsg` and `workerDataMsg` are processed, not on `tickMsg` (which fires before data arrives). Use a `samplePending` flag: set on `tickMsg`, record on whichever of `beadsMsg`/`workerDataMsg` arrives last in the cycle.
- `buildCurrentSample()` derives `MetricsSample` from `m.beads`, `m.workers`, `m.healthData`, `m.closedCount`
- **Guard:** If `timeDelta == 0` between samples, skip throughput calculation (return `—`)
- Test: samples accumulate correctly, stale-data scenario verified, division-by-zero guarded

### Task 4: StatusView scaffold + key handling
- Create `cmd/oro-dash/status.go` with `StatusModel` struct
- Add `StatusView` to ViewType enum (append, don't insert)
- Wire `s`, `H`, `w` keys to StatusView in `handleBoardViewKeys` (and equivalent in list/other view handlers)
- **CRITICAL (from adversarial review):** Add `case StatusView:` to `handleKeyPress` switch in `model.go` — calls new `handleStatusViewKeys()` function
- **CRITICAL:** Add `case StatusView:` to `View()` switch in `model.go`
- Create `handleStatusViewKeys()` with j/k scroll, Enter on worker → bead detail, Esc → `m.previousNavView`
- Esc returns to `m.previousNavView` (which may be ListView from Phase 3, or BoardView if Phase 3 not yet implemented)
- Render System section (daemon, uptime, panes)
- **Handle nil/empty:** System section shows "Connecting..." if healthData is nil
- Update `previousNavView` tracking
- Test: view switching roundtrips, key handling, nil health data

### Task 5: Pipeline section with sparklines
- Compute throughput, queue depth, utilization from MetricsBuffer
- Render 3 sparkline rows with current values
- **Sparkline normalization:** if min == max (all-same values), render all as middle block (`▄`)
- Handle early-session edge case (insufficient data → show `—`)
- Test: sparkline renders correctly, throughput calculation, all-same-value normalization

### Task 6: Workers section (2-line cards)
- Render worker cards: line 1 (status/bead/ctx%) + line 2 (sparkline/done/fail/cycle/elapsed)
- Per-worker context sparkline from MetricsBuffer (filter by worker ID)
- **New worker with < width samples:** pad sparkline left with `▁` (baseline)
- Done/fail counting from state transitions between consecutive samples
- **Worker IDs are short sequential (`w-1`, not UUIDs):** key per-worker history on `ID + firstSeenTimestamp` composite to handle recycling
- Active workers first, idle workers dimmed
- **Empty state:** if no workers, show "No active workers · start with: oro work" (or daemon-aware variant)
- Test: worker card rendering, idle vs active styling, transition counting, empty state, recycled ID handling

### Task 7: Session section + aggregate counters
- Render session summary line (handoffs, respawns, QG runs, QG pass rate)
- Respawn detection from worker state transitions
- **Suppress false positives:** don't count executing→idle transitions during daemon shutdown
- QG run counting from `AttemptCounts` (now available via Task 2 fetch expansion)
- Test: counter accuracy, respawn detection, shutdown suppression

### Task 8: Responsive layout + viewport
- Width-dependent column hiding and sparkline sizing
- **Add viewport.Model or manual scroll offset to StatusModel** for tall dashboards
- Height handling: if dashboard content exceeds terminal height, enable j/k scrolling with scroll indicator
- Enter on worker row → navigate to bead detail
- Test: narrow/wide rendering, scrolling, navigation, height overflow

### Task 9: Theme + help + polish
- Add Status Dashboard styles to theme
- **Fix `initCommonStyles` no-op bug** (theme.go:174) — same fix as Phase 3 Task 0
- Add `StatusView` case to `helpHintsForView`, `getHelpBindingsForView`, `getViewName`
- Update `H` and `w` key references in help text to show they alias to Status Dashboard
- Snapshot tests for all variants (full, narrow, empty, nil health, no workers)
- Final quality gate

---

## Key Decisions Log

| Decision | Rationale |
|----------|-----------|
| Merge Health + Workers into unified Status | One view for all operational data. Reduces key bindings, eliminates context switching between `H` and `w`. |
| In-memory ring buffer, no persistence | Live monitoring doesn't need cross-session history. Simpler, no SQLite dependency for the dashboard. |
| 900 samples (30 min at 2s) | Covers typical swarm monitoring session. Old data is noise for live ops. |
| Record after both msgs arrive (not on tickMsg) | tickMsg fires before data arrives — sampling on tick captures stale data. Use pending flag instead. |
| 8-level Unicode sparklines | No library dependency. Works in all modern terminals. |
| Sparkline width: 20 chars | Covers ~40s of raw data or full buffer downsampled. Readable at a glance. |
| Throughput from BeadsClosed delta | Simple, accurate, uses data already fetched. Clamp to ≥0 for safety. |
| Per-worker done/fail from state transitions | Best-effort attribution. Worker IDs are short sequential — use composite key (ID+firstSeen) to handle recycling. |
| `H` and `w` as aliases for `s` | Backwards compatibility for muscle memory. |
| Two-line worker cards | More info per worker (sparkline, done, fail, cycle, elapsed). Worth the vertical space. |
| Suppress fail counting during shutdown | Prevents false positives when `oro stop` cleanly kills workers. |
