# Oro Dash — Linear-Grade Redesign

**Date:** 2026-05-30
**Status:** Draft → Stage 2 Consultation pending → Adversarial review pending → beadcraft
**Scope:** `pkg/web/` (web dashboard at `:4444`) + one additive aggregation query in `pkg/dispatcher/`. **No TUI as a product surface.** `cmd/oro-dash` still exists in the tree as a live headless diff-test harness (`cmd/oro-dash/main.go`, `cli_test.go:TestHeadlessDiffTest`) and `pkg/dashboard/views` is its render lib — **both are out of scope and must not be modified** by this work, but they are not deleted/gone.

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
**Fix (by design):** detail panel has a fixed width with internal vertical scroll; all text wraps (`white-space: pre-wrap; overflow-wrap: anywhere` for code/AC); the main grid uses `minmax(0, 1fr)` and **every truncating flex child gets `min-width: 0`** so nothing can force horizontal page overflow. This must cover *all* panels, not just detail — today `.event-feed__text` (`style.css:369`), `.worker-row__bead` (`:291`), and `.bead-card__title` (`:148`) use `white-space:nowrap` inside flex children with no `min-width:0`, so a long single-token event summary or worker bead title overflows just like the AC does.

**Acceptance (testable in the httptest harness via CSS-content + template-structure assertions — see §11 on why a real layout assertion isn't available):**
1. CSS-content test: assert `.layout` (or its successor) declares `minmax(0, 1fr)` and that *every* class that sets `text-overflow: ellipsis` also sets `min-width: 0` on itself or its flex parent.
2. CSS-content test: assert the detail/AC rule uses `overflow-wrap: anywhere` (or `word-break`) and `white-space: pre-wrap`.
3. Template-structure test: render a fixture where the detail AC, a recent-event summary, a worker bead field, **and** an epic title each contain a 400-char single-token string simultaneously; assert the rendered HTML places them inside the wrapping/scoped containers (not raw `<pre>` without the wrap class).

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

**Implementation note (CORRECTED after adversarial review — the original "load from the store" plan was non-viable).** The `beadstore` interface exposes only `Ready`/`InProgress`/`Blocked`/`Closed(limit)`/`AllChildrenClosed` — there is **no enumerate-all / list-epics / children-of method**, and `Closed` is limited, so a store-based rollup would *undercount done* for any epic with >limit closed children. Do **not** use the store for this.

Instead, implement `Epics()` as a **direct SQL aggregation over `d.db`** (the `*sql.DB` on the Dispatcher, `dispatcher.go:737`), exactly mirroring the existing `RecentEvents()` (`dashboard.go:101`) and `Throughput()` (`dashboard.go:174`) which already run ad-hoc SQL. The `beads` table has `parent_id TEXT REFERENCES beads(id)` with index `idx_beads_parent` (`schema.go:258,301`). Rollups come from one grouped query, **no row limit**:

```sql
-- counts per epic (parent_id), all statuses, no limit
SELECT parent_id,
       COUNT(*)                                              AS total,
       SUM(status='closed')                                  AS closed,
       SUM(status='in_progress')                             AS in_progress,
       SUM(status='blocked')                                 AS blocked,
       SUM(status='open')                                    AS ready
FROM beads
WHERE deleted = 0 AND parent_id IS NOT NULL
GROUP BY parent_id;
```

Plus: a query for epic beads themselves (`issue_type='epic'`), the active child per epic (`status='in_progress'`, with worker/context from the live worker snapshot), and the next child (lowest priority number among `status='open'` children; if none and work remains, the blocking dep title). Orphan beads (`parent_id IS NULL`) are grouped under a synthetic **"Unfiled"** epic so nothing is dropped. An epic with ≥1 in-progress child → "Epics in progress"; 0 started children → "Next epics" (`ready` if ≥1 open child, else `blocked`). Closed children beyond any window are still counted because the aggregate has no `LIMIT`.

This is additive — it does not change existing `ReadyBeads`/`InProgressBeads`/etc., which the slide-over and filters still use.

**Interface placement:** add `Epics()` to `web.DashboardData` (`pkg/web/server.go:35`, the handler's dependency) and implement it as a concrete method on `*Dispatcher` in `pkg/dispatcher/dashboard.go`. Note that `Workers`/`Throughput`/`HealthError` are concrete `*Dispatcher` methods that are **not** all mirrored in any second interface — follow that existing precedent; do not invent a parallel interface.

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
- Escalations from the events table. **Note:** an escalation event has `Event.Type == "escalation"` (see `helpers.go:98`); the *subtype* (`STUCK`, `STUCK_WORKER`, `MERGE_CONFLICT`, `WORKER_CRASH`, `MISSING_AC`, `DEPENDENCY_CYCLE`, `OVERSIZED_BEAD`, `NON_TDD_AC`, `MANUAL_INTEGRATION`, `PRIORITY_CONTENTION` — `pkg/protocol/types.go:181-194`) lives in `Event.Payload`, not the type column. The handler must parse `Payload` to classify and to decide which escalations are human-actionable vs. informational (e.g. `DRAIN_COMPLETE`/`MERGE_COMPLETE`/`EPIC_COMPLETE` are not "needs you").
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

Keep htmx + SSE. **Reuse the existing emitted event names — do NOT invent new ones.** `formatSSEEvent`/`dashboardEventNames` (`pkg/web/sse.go:76-96`) emit exactly: `new-event` (always), `parade-update`, `worker-update`, and `throughput-update` (on merged/epic-acceptance). The browser bridge that turns these into htmx triggers lives in `index.html:42-56`. The redesign re-partitions *what each existing event swaps*, keeping the names:

- `parade-update` → swaps the **epics list fragment** (`#epics`, served by a new `/fragments/epics` handler). (Same trigger that drives the list today; only the container/handler change. No `sse.go` edit needed.)
- `worker-update` → swaps `#workers`.
- `new-event` → swaps `#recent`, and re-triggers `#needs-you` + the header counts fragment.
- `throughput-update` → refreshes the header throughput line.
- The slide-over (`#detail`) and command palette are **never** SSE targets — they live outside every swapped container (this is the Defect-A fix; see §3A and the index.html structural change in §13).
- Selection state (`j/k` cursor) is restored after a list swap via a stable `data-id` key, so live updates don't lose your place.

A test must assert that whatever trigger name the `#epics` container listens for is exactly a name `dashboardEventNames` returns (single source of truth), so the epics list cannot silently go stale.

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

## 11. Test Harness Reality & Adversarial Self-Review

**Harness facts (verified, correcting earlier draft errors):**
- `pkg/web` tests use `httptest` + **substring assertions** over rendered HTML (`server_test.go`, `css_test.go` reads `style.css` and greps). There are **no golden-HTML files** and no browser/layout engine. So a true `scrollWidth <= clientWidth` layout assertion is **not available** in this harness, and adding a headless browser would violate the "no build step / no new deps" constraint (§9).
- Therefore Defect-B is verified by **CSS-content + template-structure assertions** (§3B), in the same style as the existing `css_test.go`. This proves the wrap/`min-width:0`/`minmax(0,1fr)` rules are present and that long content is rendered into the wrapping containers — it does not pixel-measure layout. Accepted limitation, called out so beadcraft doesn't encode an untestable criterion.
- `cmd/oro-dash` is **not gone** — it is a live headless diff-test harness and is simply out of scope here.

**Self-review checks:**
- **Acceptance test exists?** Each component gets a `pkg/web` template/handler test, plus the two defect-regression tests (A: detail survives ≥3 list swaps; B: §3B CSS-content + structure), plus a **page-level acceptance** (§13) asserting the epic layout *replaced* the parade.
- **Wiring:** new `Epics()` must be (1) added to `web.DashboardData` (`server.go:35`), (2) implemented on `*Dispatcher` via direct `d.db` SQL (`dashboard.go`), (3) called by a new `/fragments/epics` handler + index handler, (4) rendered by `epics.html`, (5) the `#epics` container wired to the **existing** `parade-update` SSE trigger. All five are named in §5/§8/§13. beadcraft must trace each.
- **CSS test:** `css_test.go` currently *requires* `.bead-string`, `@keyframes shimmer`, and the Mardi Gras hexes (`css_test.go:25-86`). The redesign deletes all of them, so this test **must be rewritten** as its own task (§13) or the theme literally cannot land green.
- **Negative space:** orphan beads (→ "Unfiled" epic), zero epics, zero workers, healthy-with-empty-needs-you (positive empty state), epic with >limit closed children (rollup still correct because SQL has no `LIMIT`), an escalation whose subtype is informational (filtered out), a 400-char single token in every panel — all specified.
- **Red team — "all tasks pass but feature fails":** see the three scenarios the formal review surfaced, each now mitigated:
  1. *Page still shows old parade.* Mitigation: §13 page-level acceptance asserts GET `/` contains epic markers (`NEXT EPICS`, a `n / m` rollup) **and does NOT contain** `Queued Up`/`Rolling`/`Stalled`.
  2. *Epics list renders once then goes stale.* Mitigation: §8 keeps the real `parade-update` name + a test asserting the `#epics` trigger equals a `dashboardEventNames` output.
  3. *Detail fixed but page still overflows from another panel.* Mitigation: §3B fixture injects long content into **every** panel and asserts wrap/`min-width:0` rules on all truncating classes.

---

## 12. Resolved Decisions (consultation 2026-05-30)

- [x] **Font:** keep the system font stack for v1 — zero new assets. Revisit Inter only if it doesn't read as Linear-enough.
- [x] **`Epics()` scope:** full epic-centric v1. The epic rollups are the core of the reframe ("the epic matters more than the task"). Decomposition orders the visual system + both stability fixes first so they can land independently if `Epics()` perf needs tuning, but epic grouping/progress ships in v1.
- [x] **Needs-you:** derive in the handler from existing lists + events for v1; promote to a dedicated `NeedsAttention()` query only if the logic grows.

---

## 13. Integration Touchpoints & Acceptance (for beadcraft)

Every file the redesign must touch, so no wiring is dropped. beadcraft must trace each.

| File | Change | Why it's load-bearing |
|------|--------|------------------------|
| `pkg/dispatcher/dashboard.go` | Add `Epics()` via direct `d.db` SQL (§5.1) | Rollup data source; store can't do it |
| `pkg/web/server.go` | Add `Epics()` to `DashboardData` iface; add `EpicSummary`/`EpicProgress`/`EpicChildRef` types; new `/fragments/epics` handler; index handler passes epics + a beadID→title map (§5.2) + needs-you items + health state | Handler wiring |
| `pkg/web/templates/index.html` | **Replace** the `#parade` block with `#epics` (hx-trigger `parade-update`); move the detail target to a **slide-over `#detail` sibling OUTSIDE the swapped container** (Defect-A structural fix); rework SSE JS bridge (lines 42-56) to map existing events to new containers; add `#needs-you` panel | Defect A lives here, not in detail.html |
| `pkg/web/templates/epics.html` | **New** — epic cards (in-progress) + next-epics lane (§6.2/6.3) | Primary view |
| `pkg/web/templates/detail.html` | Slide-over content; wrap AC (`pre-wrap`+`overflow-wrap:anywhere`); epic variant shows child list | Defect B |
| `pkg/web/templates/needs-you.html` | **New** — ranked human-action items (§6.4) | Q7 |
| `pkg/web/templates/workers.html` | Show bead **title** not ID; calm styling | Human-readable |
| `pkg/web/templates/events.html` | Plain summaries with **titles** via the title map | Human-readable |
| `pkg/web/templates/throughput.html` | Fold into header line | §6.1 |
| `pkg/web/helpers.go` | Drop/repurpose `heatColor` (age-heat removed) and `statusSymbol`’s `♪/●/⊘/✓` parade glyphs; add epic-progress / plain-status / title-lookup / escalation-subtype-parse helpers. Coordinate the funcmap (`TemplateFuncMap`) with the templates that referenced the removed funcs | Funcmap + template must stay in sync or templates panic |
| `pkg/web/static/style.css` | New Linear token system (§7); **delete** `.bead-string`, `@keyframes shimmer`, age-heat classes, Mardi Gras hexes; add `#epics`/epic-card/slide-over/needs-you rules; `minmax(0,1fr)` + `min-width:0` everywhere | Visual + Defect B |
| `pkg/web/static/dash.js` | **New** ~150 LOC vanilla: ⌘K palette, j/k nav, slide-over toggle, deep-link `#id` (§6.8) | Navigation |
| `pkg/web/css_test.go` | **Rewrite** — remove shimmer/bead-string/Mardi-Gras assertions; assert new tokens, `minmax(0,1fr)`, `min-width:0` on truncating classes, no `@keyframes shimmer` | Otherwise theme can't land green |
| `pkg/web/embed.go` | Ensure new templates + `dash.js` are embedded | New assets must ship in binary |
| `pkg/web/server_test.go` | Update substring expectations for new layout | Existing assertions reference old markup |

**Page-level acceptance (the end-to-end test that catches "all tasks pass, feature still wrong"):**
```
GET /  (against a Dispatcher seeded with: 1 epic w/ in-progress child, 1 not-started epic, 1 orphan bead)
ASSERT body contains:  "NEXT EPICS"  AND  a rollup count matching /\d+\s*\/\s*\d+/  AND the epic titles
ASSERT body does NOT contain:  "Queued Up"  "Rolling"  "Stalled"  "bead-string"  "shimmer"
ASSERT an epics-list SSE trigger name == one of dashboardEventNames(...) outputs
```

**Out of scope, do not modify:** `cmd/oro-dash/*`, `pkg/dashboard/views/*` (retired-from-product headless diff-test harness, still live in tree).
```
