# Oro Dash — Linear-Grade Redesign

**Date:** 2026-05-30
**Status:** Draft → Stage 2 Consultation pending → Adversarial review pending → beadcraft
**Scope:** `pkg/web/` (web dashboard at `:4444`) + one additive aggregation query in `pkg/dispatcher/`. **No TUI** — the TUI is retired; `pkg/dashboard/views` and `cmd/oro-dash` are out of scope.

---

## 1. Goal & Reframe

Make the oro web dashboard look *and feel* like Linear: clear navigation, the right information at the right time, and a calm, restrained visual system. This is a **UX redesign**, not a reskin.

The current dashboard was deliberately built as "Linear meets Mardi Gras" (`docs/plans/2026-03-31-web-dashboard-design.md` line 69): Linear's bones wearing Mardi Gras clothing (purple/gold/green palette, an animated shimmer gradient header, parade metaphor, confetti). This spec removes the chosen whimsy and goes the rest of the way to Linear's restraint — and, more importantly, reorganizes the information around what a human operator actually needs to know.

### The reframe (from the requester, verbatim intent)

The dashboard's primary job is **to inform on how the factory is progressing and what work is being worked on.** Concretely:

1. **Is everything healthy?** If healthy → show throughput. If not → show the issue.
2. **What epics are being worked on now? What epics are next?** *The epic is more important than the specific task.*
3. **What's next, and what needs to happen before what's next?**

And a constraint that shapes everything: **the dashboard is for a human who cannot instinctively decode bead IDs.** `oro-6wxx` means nothing at a glance. Titles lead; IDs are demoted to small secondary tags; status is described in plain language.

The current dashboard violates all of this: it's a flat list of cryptic IDs grouped by low-level status (Queued Up / Rolling / Stalled / Finished), with no epic grouping, no progress, no "what's next," and no sense of health beyond a thin error banner.

### Load-bearing assumption (verified)

Every bead carries `Bead.Epic` (JSON `parent`) = its parent epic ID; epics are beads with `Type == "epic"` (`pkg/protocol/types.go:49-50`). So bead→epic mapping exists. **But** the current `DashboardData` interface only exposes flat Ready/InProgress/Blocked/Closed lists with Closed capped at 20 — insufficient to compute epic progress (done/total) or list not-yet-started epics. **The epic-centric design requires one new aggregation query** (§5). If that query cannot be built cheaply, the epic-centric layout collapses to a richer-but-flat list. This is the single assumption the whole design rests on.

---

## 2. The Questions The Dashboard Answers (IA backbone)

The layout is derived from the questions an operator brings, ranked:

| # | Question | Primary? | Where it's answered |
|---|----------|----------|---------------------|
| 1 | Is the factory healthy — can I relax, or do I need to act? | **Yes** | Status header (always visible) |
| 2 | If healthy: how fast / how costly? | **Yes** | Status header (throughput inline) |
| 3 | If not healthy: what's wrong? | **Yes** | Status header swaps to the issue + routes to "Needs you" |
| 4 | What epics are being worked on right now? | **Yes** | "Epics in progress" (main column) |
| 5 | For each epic: how far along, who/what is moving it, what's next? | **Yes** | Epic card: progress bar + current task + `next →` |
| 6 | What epics are coming next, and what gates them? | **Yes** | "Next epics" lane |
| 7 | What needs *my* attention specifically? | High | "Needs you" panel (right) |
| 8 | What's running right now (workers)? | Medium | Workers panel (right) |
| 9 | What just happened? | Medium | Recent events panel (right) |
| 10 | Tell me everything about this one item. | On demand | Slide-over detail panel |
| 11 | How do I get to X fast? | Always | ⌘K palette + keyboard nav + filters |

Anything not serving a question above is cut (parade metaphor naming, decorative shimmer, confetti, age-heat colors on IDs).

---

## 3. Known Defects This Redesign Fixes

Two reported bugs, both diagnosed from the current code — neither is "random":

### Defect A — open detail closes itself on every dispatcher event
**Root cause:** the expanded detail (`.bead-detail-slot`) is rendered *inside* the parade fragment. `index.html` wires `#parade` with `hx-trigger="dashboard-parade from:body"` + `hx-swap="innerHTML"`, and SSE `parade-update` fires on essentially every bead change. Each event re-fetches `/fragments/parade` and replaces its innerHTML, destroying any open detail.
**Fix (by design):** detail moves to a **persistent slide-over panel rendered outside any SSE-swapped fragment**. Live list updates can re-render the list without touching the open panel. Acceptance: open a detail, trigger ≥3 SSE list updates, panel stays open and current.

### Defect B — content overflows horizontally / "doesn't stay on screen"
**Root cause:** detail renders acceptance criteria in `<pre class="detail-ac">` (no wrap) and long single-line signatures; `.bead-detail` has no `max-width`/overflow handling, so long lines run off the right edge and shove the sidebar (visible in the report screenshot).
**Fix (by design):** detail panel has a fixed width with internal vertical scroll; all text wraps (`white-space: pre-wrap; overflow-wrap: anywhere` for code/AC); the main grid uses `min-width: 0` on flex/grid children so nothing can force horizontal page overflow. Acceptance: render a bead whose AC contains a 400-char single-line signature; no horizontal scrollbar appears on the page; text wraps inside the panel.

---

## 4. Information Architecture & Layout

Single page. Fixed status header, scrollable main column (epics), fixed-ish right rail (3 stacked panels), on-demand slide-over detail.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│  oro   ● Healthy   3.2 beads/hr · $1.80/hr · 4/4 workers · up 2h 14m   [⌘K]  │  HEADER (always visible)
│  ── degraded variant ──────────────────────────────────────────────────────  │
│  oro   ⚠ Needs you · 2   quality gate stuck on "Add cards show command"      │
├────────────────────────────────────────────────────────┬────────────────────┤
│  EPICS IN PROGRESS                          [All ▾]      │  NEEDS YOU · 2     │
│                                                          │  ⚠ QG stuck        │
│  Cards migration                       ███████░░  7 / 9  │    Add cards show… │
│    ● w2 · writing the promotion loop            40% ctx  │  ⊘ blocked 3d      │
│      Finish write/promotion loop                         │    Memory eval…    │
│      next →  Add cards show command                      ├────────────────────┤
│             waits on  Define card relevance wire types   │  WORKERS · 4       │
│                                                          │  ● w1  Cards…  42% │
│  Stop-hook hardening                   ███░░░░░░  2 / 6  │  ● w2  Promo…  40% │
│    ● w1 · wiring the Stop hook into settings    18% ctx  │  ○ w3  idle        │
│      Wire Stop hook + stage assets                       │  ● w4  Stop…   55% │
│      next →  Resolve hard threshold                      ├────────────────────┤
│                                                          │  RECENT            │
│  ───────────────  NEXT EPICS  ───────────────           │  12:01 ✓ merged    │
│  Beadstore recovery                    0 / 4    ready    │    Add cards show…  │
│  Memory eval harness rebuild           0 / 7    blocked  │  12:00 ✗ QG reject  │
│    first needs  Define relevance wire types              │  11:58 ↻ handoff    │
└──────────────────────────────────────────────────────────┴────────────────────┘
  ⌘K command palette  ·  j / k move  ·  enter open  ·  /  search  ·  esc close
```

### Layout grid
- Header: full width, fixed height (~52px), sticky top.
- Body: CSS grid `grid-template-columns: minmax(0, 1fr) 340px`. The `minmax(0, …)` is mandatory — it prevents the main column's content from forcing page-level horizontal overflow (Defect B).
- Main column: scrolls vertically. Right rail: each of the three panels scrolls internally if needed.
- Slide-over detail: `position: fixed; right: 0; top: header; width: min(560px, 90vw); height: calc(100vh - header)`, internal scroll, `transform: translateX(100%)` → `0` transition. Rendered as a sibling of the layout, **not** inside `#parade`/`#epics`.
- Responsive: this is a localhost dev tool; support down to ~1024px. Below that, the right rail stacks under the main column. No mobile target.

---

## 5. Data Layer Changes

### 5.1 New aggregation: `Epics()`

Add to the `web.DashboardData` interface and implement on `*dispatcher.Dispatcher` (in `pkg/dispatcher/dashboard.go`, alongside the existing thin wrappers):

```go
// In pkg/web/server.go
type EpicProgress struct {
    Total      int // child beads in epic
    Closed     int // closed children
    InProgress int
    Blocked    int
    Ready      int
}

type EpicChildRef struct {
    ID    string
    Title string
}

type EpicSummary struct {
    ID         string        // epic bead ID
    Title      string        // human-readable epic title
    Status     string        // epic's own status
    Priority   int
    Progress   EpicProgress
    // Active is the child currently in_progress (nil if none).
    Active     *EpicChildRef
    ActiveWorkerID   string
    ActiveContextPct int
    // Next is the highest-priority ready child the dispatcher will pick up next.
    Next       *EpicChildRef
    // NextBlockedBy is the human-readable blocker when Next is nil but work remains.
    NextBlockedBy *EpicChildRef
}

// Epics returns every epic with computed child rollups, ordered:
// in-progress epics first (by priority), then not-started epics ("next epics").
Epics(ctx context.Context) ([]EpicSummary, error)
```

**Implementation note:** the dispatcher already queries beads from its store. `Epics()` loads all non-closed beads + a bounded window of closed beads, groups children by `Bead.Epic`, and computes counts. Epics with ≥1 in-progress child render in "Epics in progress"; epics with 0 started children render in "Next epics." An epic is "ready" if it has ≥1 ready child, "blocked" if all remaining children are blocked. **Beads with no epic** (`Epic == ""`) are grouped under a synthetic "Unfiled" epic so nothing is dropped.

This is additive — it does not change existing `ReadyBeads`/`InProgressBeads`/etc., which the slide-over and filters still use.

### 5.2 Event title resolution

Events carry only `BeadID`, not titles (`pkg/protocol` Event). To render the human-readable Recent panel ("merged *Add cards show command*"), the index/fragment handler builds a `map[beadID]title` from the bead lists it already loads and passes it to the events template. Unknown IDs (old/closed-out beads) degrade gracefully to the ID. No new query needed.

### 5.3 Health header data

The header consumes `HealthError()` (already present) + `Throughput()` (already present) + a health *state* string. `HealthError()` returns nil when healthy. The header shows:
- **Healthy** (`HealthError()==nil`): green dot + throughput line.
- **Degraded / needs-you** (`HealthError()!=nil` OR Needs-you count > 0): amber/red + the issue summary + the needs-you count.

"Needs you" items are derived (§6.4) from blocked beads, escalation events, dead-heartbeat workers, and recovery quarantines (human-owned) — no new query strictly required for v1 (derive from existing lists + events), though a dedicated `NeedsAttention()` query is a clean follow-up.

---

## 6. Components

### 6.1 Status header
- Left: `oro` wordmark (small, muted).
- Center/right when healthy: `● Healthy` (green) + `beads/hr · $/hr · active/total workers · uptime`, tabular numerics, muted separators.
- When degraded: `⚠ Needs you · N` (amber) + one-line issue summary. Clicking it scrolls/opens the Needs-you panel.
- Far right: `⌘K` affordance (opens command palette).

### 6.2 Epic card (main column, "in progress")
- Line 1: **epic title** (primary text, ~15px) + right-aligned progress bar + `closed / total` count.
- Line 2: `● wN · <plain-language activity>` + right-aligned `NN% ctx`. Activity is derived from the active child's title/state ("writing the promotion loop"). If no active child: `—`.
- Line 3: active child title (secondary).
- Line 4: `next → <title>`; if next is blocked, a 5th line `waits on <blocker title>`.
- Whole card is one keyboard-navigable row group; `enter` opens the epic in the slide-over; the active/next child titles are independently focusable to open *their* detail.
- Progress bar: 8 segments, accent fill (Linear indigo) for closed, faint track for remainder.

### 6.3 Next-epics lane
- Divider label "NEXT EPICS".
- Each row: epic title + `0 / total` + a state chip (`ready` green / `blocked` amber). If blocked, a sub-line `first needs <title>`. Ordered by priority.

### 6.4 Needs-you panel (right rail, top)
Ranked list of items requiring a human, each as `<icon> <kind> · <age>` + human-readable title:
- Escalations from the events table (types: `STUCK`, `STUCK_WORKER`, `MERGE_CONFLICT`, `WORKER_CRASH`, `MISSING_AC`, `DEPENDENCY_CYCLE`, `OVERSIZED_BEAD`, `NON_TDD_AC`, `MANUAL_INTEGRATION`, `PRIORITY_CONTENTION`) — see `pkg/protocol/types.go:181-194`.
- Beads blocked beyond a threshold age.
- Workers with stale heartbeat (> threshold).
- Recovery quarantines (operator-owned).
Empty state: "Nothing needs you — the factory is running clean." (this is a *good* state, styled calm/positive, not a void).

### 6.5 Workers panel (right rail, middle)
Per worker: state dot (busy/idle/stuck) + `wN` + **bead title** (not ID) truncated + context-% (numeric, color-graded at 60/80) . Heartbeat age on hover/secondary. Idle workers dimmed.

### 6.6 Recent panel (right rail, bottom)
Reverse-chron events, `HH:MM` + symbol + **plain summary with title** (§5.2). Color only on the symbol. Internal scroll, ~last 50.

### 6.7 Slide-over detail panel
Opens on row/epic click or `enter`. Lives outside SSE-swapped fragments (Defect A). Contents:
- Header: title (large) + status chip (plain word) + close (×, or `esc`).
- Meta row: type · epic (as title, linked) · model · priority · worker.
- Sections (each wraps, panel scrolls): Description, Acceptance Criteria (monospace, wrapped), Dependencies (as titles + resolved/unresolved state), Worker + context, recent events for this bead.
- For an **epic**: show child list grouped by status with progress, instead of AC.
- Deep-link: `#<bead-id>` in URL opens that detail on load (shareable, survives reload).

### 6.8 Command palette (⌘K) + keyboard nav
- `⌘K` / `Ctrl-K`: overlay input. Fuzzy-search beads & epics **by title** (the human can't type IDs). Enter opens detail. Also lists view-switch actions (All / In progress / Blocked / Needs you / Done).
- Global keys: `j`/`k` move selection through visible rows, `enter` open, `esc` close panel/palette, `/` focus search, `g` then `h` jump to top.
- Implementation: a single small vanilla JS file (`dash.js`, ~150 LOC) for palette + keyboard + slide-over toggling. No framework, consistent with the existing "no build step" constraint. htmx still drives live fragment swaps.

---

## 7. Visual System (Linear-grade)

Replace the Mardi Gras palette and kill all decorative motion.

### Color (dark, near-monochrome + one accent)
| Token | Value | Use |
|-------|-------|-----|
| `--bg` | `#08090A` | page background |
| `--surface` | `#0E0F11` | panels, header |
| `--surface-2` | `#16171A` | hover, slide-over |
| `--border` | `#1C1D20` | 1px separators (chrome recedes) |
| `--text` | `#F7F8F8` | primary text, titles |
| `--text-2` | `#9CA0A8` | secondary text |
| `--text-3` | `#62666D` | labels, faint meta |
| `--accent` | `#5E6AD2` | selection, focus ring, progress fill, primary action (Linear indigo) |
| `--green` | `#4CB782` | healthy, merged, ready |
| `--amber` | `#E2A336` | warn, blocked, needs-you |
| `--red` | `#EB5757` | failure, dead worker |

Semantic colors appear only as small dots/symbols/text — never full-row fills.

### Typography
- Font: `Inter` if available, else the existing system stack. (Optionally vendor Inter as a woff2 in `static/` — decide in consultation; default = system stack to keep zero new assets.)
- Scale: 15px epic/detail titles · 13px body · 12px secondary · 11px uppercase labels (letter-spacing 0.06em, `--text-3`).
- IDs: 11px monospace, `--text-3`, never the leading element.
- Tabular numerics on all counts/percentages/throughput.

### Spacing & shape
- 4px base grid. Row height ~32px. Panel padding 16px. Section gaps 24px.
- Borders 1px `--border`; radius 6px on cards/panel, 8px on slide-over.
- No shadows except a single subtle one on the slide-over for elevation.

### Motion
- Hover/selection: 120ms ease background/opacity.
- Slide-over: 180ms ease-out transform.
- **Removed:** `bead-string` shimmer (infinite gradient), confetti, age-heat ID colors. Live updates flash a 1-frame subtle highlight on changed rows (≤300ms), nothing looping.

---

## 8. Live Updates (SSE) — stability rules

Keep htmx + SSE. The redesign re-partitions what each SSE event swaps so updates never destroy user state:
- `epics-update` → swaps the **epics list fragment only** (`#epics`).
- `workers-update` → swaps `#workers`.
- `new-event` → swaps `#recent` + recomputes `#needs-you` + header counts.
- The slide-over (`#detail`) and command palette are **never** SSE targets. They live outside the swapped containers.
- Selection state (`j/k` cursor) is restored after a list swap via a stable `data-id` key, so live updates don't lose your place.

---

## 9. What We're NOT Doing (YAGNI)

- No JS framework, no npm, no build step (htmx + one small vanilla file).
- No drag-and-drop — the dispatcher moves work, not the user.
- No auth (localhost only).
- No mobile layout (dev tool; degrade gracefully to ~1024px).
- No write actions from the dashboard in v1 (read-only view; ⌘K navigates, doesn't mutate). Re-prioritizing/retrying from the UI is a future epic.
- No historical charts/trends in v1 (throughput is current-rate only).
- No theming/light mode in v1.
- Not touching `pkg/dashboard/views` or `cmd/oro-dash` (retired TUI).

---

## 10. Premortem

| Risk (tiger) | Severity | Mitigation |
|--------------|----------|------------|
| `Epics()` is expensive / N+1 over the store on every SSE tick | High | Compute from a single bounded bead load; cache per render; SSE `epics-update` is debounced server-side (coalesce bursts). Measure with 200+ beads. |
| Epic grouping wrong when beads lack `parent` | Medium | Synthetic "Unfiled" epic catches orphans; covered by a test with mixed parented/orphan beads. |
| Slide-over + htmx swaps still race (panel references a row that got swapped away) | High | Panel content is fetched independently by ID and owns its own DOM subtree; list swaps can't touch it. Test: open detail, fire 3 list swaps, assert panel intact. |
| Command palette / keyboard JS grows into a framework-shaped blob | Medium | Hard cap: one file, no deps; if it exceeds ~200 LOC, reconsider scope. |
| "Needs you" derivation produces false positives (noisy) | Medium | Conservative thresholds; only surfaced escalation types listed in §6.4; empty state is the expected steady state. |
| Removing parade metaphor/heat colors loses info users relied on | Low | Heat/age still available in detail; parade naming carried no data. Reversible (CSS/templates). |

**Paper tigers (looked scary, aren't):** removing the shimmer/confetti (pure CSS deletion); palette swap (CSS variables); Inter font (optional, default keeps system stack).

**Elephant in the room:** the real value is the *epic-centric IA + the two stability fixes*, not the palette. If the `Epics()` query proves costly, ship the visual system + stability fixes + human-readable titles first (still a big win), and land epic rollups as a fast-follow. The spec is decomposed so that ordering is possible.

---

## 11. Adversarial Self-Review (pre-formal-review)

- **Acceptance test exists?** Each component below gets a Go test in `pkg/web` (template render asserts) + the two defect-regression tests (A: panel survives swap; B: no horizontal overflow with long AC). The headless render harness (`cmd/oro-dash --headless` is gone; web uses `httptest` against handlers) — use `httptest.Server` + golden HTML assertions.
- **Wiring:** new `Epics()` must be added to the interface *and* implemented on Dispatcher *and* called by a handler *and* rendered by a template *and* wired to an SSE trigger. All five are named in §5/§6/§8. beadcraft must trace each.
- **Negative space:** orphan beads (no epic), zero epics, zero workers, healthy-with-empty-needs-you, a 400-char AC line — all have specified states.
- **Red team — "all tasks pass but feature fails":** templates could render perfectly while the page still overflows because a *different* element (e.g. the Recent panel's long event text) lacks `min-width:0`. Mitigation: Defect-B test asserts page-level `scrollWidth <= clientWidth` against a fixture containing long content in *every* panel, not just detail.

---

## 12. Resolved Decisions (consultation 2026-05-30)

- [x] **Font:** keep the system font stack for v1 — zero new assets. Revisit Inter only if it doesn't read as Linear-enough.
- [x] **`Epics()` scope:** full epic-centric v1. The epic rollups are the core of the reframe ("the epic matters more than the task"). Decomposition orders the visual system + both stability fixes first so they can land independently if `Epics()` perf needs tuning, but epic grouping/progress ships in v1.
- [x] **Needs-you:** derive in the handler from existing lists + events for v1; promote to a dedicated `NeedsAttention()` query only if the logic grows.
```
