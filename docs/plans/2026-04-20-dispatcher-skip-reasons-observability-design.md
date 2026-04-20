# Dispatcher skip-reasons: observability for ready-but-unassigned beads

**Date:** 2026-04-20
**Status:** Design
**Owner:** as21
**Trigger:** scriptwriter session, 2026-04-19. 4 V2 beads (gts.1, gts.2, gts.3 + epic) appeared in `oro directive status` queue (`queue_depth: 4`) but were never assigned to any of 3 idle workers. Dispatcher restart didn't fix; bd ready confirmed unblocked; assignments map stayed empty `{}`. The user had to fall back to `oro work <bead>` to bypass the dispatcher entirely. **Root cause is unknown** because the dispatcher provides no per-bead "why isn't this assigned" surface.

---

## Problem

The dispatcher decides which beads workers receive via `filterAssignable()` (`pkg/dispatcher/dispatcher.go:2943`). Beads can be rejected for **10 distinct reasons** (table below), but every rejection is silent — no log, no status field, no per-bead reason exposed.

When workers are idle and `queue_depth > 0`, the user sees the same status payload as "everything healthy, nothing to do." There is no way to tell the difference between:
- "the queue is genuinely empty"
- "the queue has 4 beads, all silently rejected by `filterAssignable` for reason X"

This is the bug class scriptwriter hit. We don't yet know *which* of the 10 reasons applied; we have no way to find out without modifying the dispatcher.

### Two layers of silent rejection

The dispatcher has two distinct surfaces where a ready bead can be silently dropped:

**Layer A — `filterAssignable` (pre-attempt).** From `isBeadAssignable` (dispatcher.go:3000-3033), `hasUnresolvedBlockingDep` (3040-3050), and `filterAssignable`'s second pass (2976-2983):

| # | Reason key                  | Source line | Detail field                |
|---|------------------------------|-------------|------------------------------|
| A1 | `status_closed`             | 3001        | none                          |
| A2 | `status_in_progress`        | 3006        | none (human-owned)            |
| A3 | `status_blocked`            | 3006        | none                          |
| A4 | `worktree_failure_cooldown` | 3009        | `retry_after: <RFC3339>` (computed: `failedAt + worktreeFailureCooldown`) |
| A5 | `active_with_other_worker`  | 3012        | `worker_id`                   |
| A6 | `assignment_in_flight`      | 3019        | none (race window)            |
| A7 | `merging_to_main`           | 3026        | none (race window)            |
| A8 | `exhausted` (escalation cap)| 3029        | `attempt_count` (sourced from `d.attemptCounts`) |
| A9 | `dependency_blocked_by`     | 3045        | `blocker_id`                  |
| A10| `branch_already_merged`     | 2977        | `branch: agent/<id>` (auto-closes bead) |

**Layer B — `assignBead` / `checkBeadReady` / `checkEpicAssignable` (post-filter, mid-attempt).** A bead passes `filterAssignable` but is silently rejected during the assignment attempt itself. From reading dispatcher.go:3070-3163, 3194, 3229-3260, 3331, 3423-3446:

| # | Reason key                          | Source line | Detail field                       |
|---|--------------------------------------|-------------|--------------------------------------|
| B1 | `invalid_bead_id`                    | 3071        | `error`                              |
| B2 | `bead_status_changed_during_assign`  | 3077        | `current_status`                     |
| B3 | `missing_acceptance_criteria`        | 3082        | (escalated; also writes A4 cooldown — the misleading-reason hazard) |
| B4 | `oversized_bead`                     | 3087        | `module_count` (also writes A4)     |
| B5 | `epic_show_error`                    | 3113-3125   | `error`, `epic_id`                   |
| B6 | `epic_branch_pending`                | 3143        | `epic_id`, `epic_status`             |
| B7 | `epic_branch_missing` (post-escalate)| 3157        | `epic_id`, `branch`                  |
| B8 | `epic_branch_create_failed`          | 3194        | `error`, `epic_id`                   |
| B9 | `epic_has_children_error`            | 3429        | `error`                              |
| B10| `epic_all_children_closed_error`     | 3438        | `error`                              |
| B11| `epic_has_open_children`             | 3445        | `child_count` (computed via HasChildren) |
| B12| `worktree_create_failed`             | 3331        | `error`                              |
| B13| `update_status_failed`               | 3260        | `error`                              |
| B14| `assignment_race_detected`           | 3237/3248   | `winning_worker_id`                  |

**Total: 24 silent-rejection paths.** Both layers must be instrumented.

**Scriptwriter root cause hypothesis (likely):** V2 was an epic + 3 children. The epic hits **B11** (`epic_has_open_children` at line 3445) — silent skip, no event, returned to ready next tick. The children pass `filterAssignable` (their dep on the epic is `parent-child`, non-blocking), enter `assignBead`, hit **B6** (`epic_branch_pending` at line 3143) because the epic was never assigned for decomposition (so `agent/<epic-id>` branch doesn't exist) — reset to ready, removed from `assigningBeads`, infinite silent loop. Without instrumenting layer B, `oro doctor bead` would return either "no skip reason" (if filterAssignable didn't reject it that tick) or `worktree_failure_cooldown` (if a prior B3/B4 escalation set the cooldown — wrong reason). Layer B instrumentation is mandatory.

## Goals

- Per-bead "why isn't this being assigned" answer, surfaced in two places:
  - `oro status` — at-a-glance summary (cached snapshot, ≤60s stale)
  - `oro doctor bead <id>` — fresh deep dive for one bead
- Historical trail of skip-reason transitions in oro's event log
- Use the new surface to identify and fix scriptwriter's actual root cause (the **A** in the user's "B + A" framing)

## Non-Goals

- Add new commands under `oro directive`. Directives are imperative ("do X"); these are queries.
- Change `filterAssignable`'s rejection logic itself. We're observing existing behavior, not modifying it.
- Persist skip-reason data to disk beyond the existing event log.
- Build dashboards / web UI for skip-reasons. CLI only.

## Decisions

### D1 — CLI surface: extend existing user-facing commands

**`oro status`** gains a "Skipped beads" block listing each ready-but-unassigned bead with primary reason. Default: collapsed summary ("4 beads skipped: see `oro doctor queue`"). Verbose flag (`--verbose` or `-v`): expand inline.

**`oro doctor bead <id>`** is a new subcommand. Returns:
- The skip reason for that bead, freshly computed
- Detail field appropriate to that reason (e.g., `blocker_id` for `dependency_blocked_by`)
- Last 5 transitions for that bead from the event log
- Suggested remediation (e.g., for `assignment_in_flight`: "register may be stuck; see `oro doctor reset-in-flight`" — though that command is out of scope for this spec)

**`oro doctor queue`** is also added — lists all currently-skipped beads with reasons.

**Alternatives rejected:**
- `oro directive why <id>` — wrong namespace (directives are imperative).
- New top-level `oro why <id>` — surface bloat for one diagnostic.

### D2 — Skip-reason data shape: single primary reason + reason-specific detail

```go
type SkipReason struct {
    BeadID    string                 `json:"bead_id"`
    Reason    string                 `json:"reason"`            // one of the 10 keys
    Detail    map[string]any         `json:"detail,omitempty"`  // reason-specific
    DetectedAt time.Time              `json:"detected_at"`
}
```

The `Detail` map's keys depend on `Reason`:

| Reason                       | Detail keys                                 |
|------------------------------|----------------------------------------------|
| `status_closed`              | (empty)                                      |
| `status_in_progress`         | (empty)                                      |
| `status_blocked`             | (empty)                                      |
| `worktree_failure_cooldown`  | `retry_after` (RFC3339 timestamp)            |
| `active_with_other_worker`   | `worker_id`                                  |
| `assignment_in_flight`       | (empty)                                      |
| `merging_to_main`            | (empty)                                      |
| `exhausted`                  | `attempt_count`                              |
| `dependency_blocked_by`      | `blocker_id`                                 |
| `branch_already_merged`      | `branch`                                     |

First-match short-circuit: the order in `isBeadAssignable` is the order of evaluation, and the first match wins. If a bead is both `status_blocked` AND has an unresolved dep, the user sees `status_blocked` (the more fundamental reason). If they fix that, the next status call surfaces the dep reason.

### D3 — Data location: cached at both filter and assign points; fresh for doctor

`tryAssign` (dispatcher.go:2760) calls `filterAssignable` and then loops `assignBead` over every accepted candidate per tick. Both must populate the cache:

```go
type Dispatcher struct {
    ...
    skipReasons     map[string]SkipReason  // beadID → reason; populated by recordSkip()
    prevSkipReasons map[string]SkipReason  // snapshot from prior tick for diff
    skipReasonsAt   time.Time              // cache freshness (last full tick completion)
}
```

A single `recordSkip(beadID, reason, detail)` helper writes into `d.skipReasons` under `d.mu`. Both `filterAssignable` and every silent-return site in `assignBead`/`checkBeadReady`/`checkEpicAssignable`/`handleEpicBranchMissing` call it. This makes adding new skip reasons in the future a one-line discipline.

**Cache lifecycle per `tryAssign` tick:**
1. At start: `d.prevSkipReasons = d.skipReasons; d.skipReasons = make(map[string]SkipReason)`.
2. `filterAssignable` runs, calls `recordSkip` for every layer-A rejection.
3. `assignBead` runs for each filter-survivor, calls `recordSkip` for every layer-B rejection.
4. At end: `d.skipReasonsAt = now`; emit transition events via diff (D4).

**Throttle-cache interaction (review fix):** `applyStatus` (dispatcher.go:3567) caches the JSON statusResponse for `statusThrottleWindow`. `SkipReasonsCachedAt` MUST be marshaled into the cached JSON itself (not added at serve time) — otherwise a throttle-cached response would carry a stale tick-time tagged with a fresh serve-time, mis-attributing freshness. Implementation: build SkipReasonsCachedAt into `buildStatusJSON` at compose time. Status JSON also includes a separate `built_at` field reflecting when the JSON was assembled, so users can distinguish (a) when the skip-reason data was computed from (b) when the response payload was built.

**`oro doctor bead <id>` recompute path (review fix — explicit lock dance):**

```
RecomputeSkipReason(ctx, beadID):
  bead := d.beads.Show(ctx, beadID)            // LOCK-FREE — subprocess shellout
  if bead == nil { return ErrBeadNotFound }

  // Critical section: snapshot all maps the synchronous checks need.
  d.mu.Lock()
  snap := snapshotForCheck{
    workerStates:     copyWorkerStates(d.workers),
    activeBeads:      copyMap(d.activeBeads),
    assigningBeads:   copyMap(d.assigningBeads),
    mergingBeads:     copyMap(d.mergingBeads),
    exhaustedBeads:   copyMap(d.exhaustedBeads),
    worktreeFailures: copyMap(d.worktreeFailures),
    attemptCounts:    copyMap(d.attemptCounts),
  }
  d.mu.Unlock()

  // Run synchronous (no-I/O) checks against the snapshot.
  reason := computeSkipReasonFromSnapshot(bead, snap, d.nowFunc())
  if reason != nil { return reason }

  // Last-resort: branch-merged check shells out to git.
  if d.isBranchMerged(ctx, beadID) {
    return SkipReason{Reason: "branch_already_merged", ...}
  }

  // No layer-A reason: re-run assignBead-equivalent checks (also lock-free except
  // for the snapshot-based intermediate state). If a layer-B reason fires, return it.
  return runAssignBeadDryRun(ctx, bead, snap)
}
```

Lock is held only across in-memory map copies — never across `bd show`, `git merge-base`, or the assign-loop tick. No deadlock risk with the assign loop.

**Alternatives rejected:**
- Pure on-demand for status — too slow.
- Pure cached for doctor — defeats "I'm debugging now."

### D4 — Persistence: event log on transitions only

Three new event types in oro's existing event log (alongside `bead_lookup_failed`, `bead_branch_already_merged`, etc.):

- `bead_skip_entered` — fired when a bead first appears in `d.skipReasons` for reason X
- `bead_skip_exited` — fired when a bead leaves `d.skipReasons` entirely (assigned, closed, or no longer skipped)
- `bead_skip_reason_changed` — fired when a bead's reason mutates (was X, now Y) — emits both old reason and new reason in payload

Payload includes `{bead_id, reason, prev_reason, detail}`.

**No event when the same reason persists across ticks.** This catches transitions ("bead Y entered `epic_branch_pending` 4 hours ago and never left") without spamming on every 60s tick.

**Lock discipline (review fix):** the diff-and-emit logic runs OUTSIDE `d.mu`. `tryAssign` snapshots `d.prevSkipReasons` and `d.skipReasons` under the lock at end of the tick, then computes the diff and emits events lock-free using `logEvent` (the unlocked variant — `logEventLocked` is for callers that already hold the lock). This avoids holding `d.mu` across N SQLite writes.

Implementation:

```
tryAssign tick end:
  d.mu.Lock()
  prev := d.prevSkipReasons
  next := d.skipReasons
  d.mu.Unlock()

  for id, reason := range next {
    if _, wasSkipped := prev[id]; !wasSkipped {
      logEvent("bead_skip_entered", {...})        // new entry
    } else if prev[id].Reason != reason.Reason {
      logEvent("bead_skip_reason_changed", {...}) // mutated
    }
  }
  for id := range prev {
    if _, stillSkipped := next[id]; !stillSkipped {
      logEvent("bead_skip_exited", {...})         // gone
    }
  }
```

**Alternatives rejected:**
- Per-tick events — log spam (a stuck bead fires every 60s for hours).
- Counter aggregates only — no transition history; can't answer "when did this start?"
- No persistence — loses retroactive debugging.

### D5 — The A (forensic) phase: ship B, then investigate scriptwriter

Once D1-D4 ship, the user runs against scriptwriter's still-stuck state:

```
oro doctor bead scriptwriter-gts.1
```

Output identifies the actual rejection reason. From there:
- If `assignment_in_flight`: investigate why the in-flight register isn't being cleared. Likely a worker-crash or socket-disconnect cleanup gap.
- If `worktree_failure_cooldown`: check `worktreeFailureCooldown` constant + when it expires; understand what failed earlier.
- If `exhausted`: review escalation history; bead may need reset.
- Other: branch case-by-case.

Forensic finding becomes a separate root-cause-fix bead, scoped after evidence is in hand. Spec does not predict the fix.

## Architecture

### New / changed code

| File                              | Change                                                                                  |
|-----------------------------------|------------------------------------------------------------------------------------------|
| `pkg/dispatcher/dispatcher.go`    | Add `skipReasons` and `prevSkipReasons map[string]SkipReason` + `skipReasonsAt` to `Dispatcher` struct. |
| `pkg/dispatcher/dispatcher.go`    | New `recordSkip(beadID, reason, detail)` helper (acquires `d.mu`). Single discipline used by both layers. |
| `pkg/dispatcher/dispatcher.go`    | Augment `filterAssignable` to call `recordSkip` for layers A1-A10 (10 sites). |
| `pkg/dispatcher/dispatcher.go`    | Augment `assignBead`, `checkBeadReady`, `checkEpicAssignable`, `handleEpicBranchMissing` to call `recordSkip` for layers B1-B14 (14 sites). |
| `pkg/dispatcher/dispatcher.go`    | At end of `tryAssign` tick: snapshot, diff, emit `bead_skip_entered` / `bead_skip_reason_changed` / `bead_skip_exited` events lock-free. |
| `pkg/dispatcher/dispatcher.go`    | New exported `RecomputeSkipReason(ctx, beadID) (SkipReason, error)` — fresh single-bead check. Lock dance per D3 (snapshot maps under lock, run synchronous checks against snapshot, shellouts lock-free). |
| `pkg/dispatcher/protocol_skip_reason.go` (new) | Define `SkipReason` struct, 24 reason key constants (A1-A10, B1-B14), `Detail` builder helpers documenting the source-of-truth field for each detail key (e.g., `retry_after = failedAt + worktreeFailureCooldown`; `attempt_count` from `d.attemptCounts[id]`). |
| `pkg/dispatcher/dispatcher.go`    | Extend `statusResponse` (the dispatcher-side definition at line 3473) with `SkippedBeads []SkipReason` + `SkipReasonsCachedAt time.Time` + `BuiltAt time.Time`. |
| `cmd/oro/cmd_status.go`           | **Update the duplicate `statusResponse` definition at cmd_status.go:25** to mirror dispatcher-side fields. Render `Skipped beads` block. Default summary line; `-v/--verbose` expands inline. Add a regression test that the two struct definitions remain field-compatible (use reflection or a generated-shared types file as a follow-up). |
| `cmd/oro/cmd_doctor.go`           | Add `oro doctor bead <id>` and `oro doctor queue` subcommands (sibling to existing `recover-dolt`). |
| `pkg/dispatcher/dispatcher_test.go` | Unit tests for `recordSkip` per category (24 reasons × 1 test each — synthesize state to trigger each layer-A and layer-B reason). |
| `pkg/dispatcher/dispatcher_test.go` | Test for transition event emission: enter/exit/change. No event on persistent reason. |
| `pkg/dispatcher/dispatcher_test.go` | Concurrency test: `RecomputeSkipReason` runs while `tryAssign` runs in parallel, with `-race` — must not deadlock or corrupt cache. |
| `pkg/dispatcher/dispatcher_test.go` | Throttle-cache test: confirm `SkipReasonsCachedAt` round-trips through the throttle cache without being mutated at serve time. |
| `cmd/oro/cmd_doctor_test.go`      | Tests for `oro doctor bead` and `oro doctor queue` against fake dispatcher state, including the "no skip reason found" case. |

### Data flow (status path)

```
oro status
  └─ queryDispatcherStatus → DirectiveStatus IPC → applyStatus
       └─ buildStatusJSON
            └─ statusResponse{
                 ..existing fields..,
                 SkippedBeads: copyOf(d.skipReasons),
                 SkipReasonsCachedAt: d.skipReasonsAt,
               }
       cmd/oro/cmd_status.go renders summary or inline (per --verbose)
```

### Data flow (doctor path)

```
oro doctor bead scriptwriter-gts.1
  └─ doctorBeadCmd → IPC DoctorBead{bead_id} → dispatcher.RecomputeSkipReason
       └─ d.beads.Show(ctx, "scriptwriter-gts.1")    [fresh fetch]
       └─ run isBeadAssignable + hasUnresolvedBlockingDep + isBranchMerged checks
       └─ build SkipReason{Reason, Detail, DetectedAt: now}
       └─ fetch last 5 events for bead (existing event-log read)
  └─ render: reason + detail + transition history + suggested remediation
```

### Event-log diff logic

```
filterAssignable(allBeads):
  prev := d.skipReasons    // snapshot before update
  next := computeSkipReasons(allBeads)

  for id, reason := range next {
    if prev[id].Reason != reason.Reason {
      logEvent("bead_skip_entered", {bead_id: id, reason: reason.Reason, detail: reason.Detail})
    }
  }
  for id := range prev {
    if _, stillSkipped := next[id]; !stillSkipped {
      logEvent("bead_skip_exited", {bead_id: id, reason: prev[id].Reason})
    }
  }

  d.skipReasons = next
  d.skipReasonsAt = now
```

## Testing Strategy

- **Unit:** synthetic `Dispatcher` state with crafted `workers`, `assigningBeads`, `worktreeFailures`, `exhaustedBeads`, `mergingBeads` maps. For each of the 10 reasons, set up state that triggers it, run `filterAssignable`, assert `d.skipReasons[bead] == expected`.
- **Unit:** transition events. Run `filterAssignable` twice with different state. Assert events emitted on enter/exit; no event when reason unchanged.
- **Integration:** spin up a real `Dispatcher` against fake `BeadSource`, run a full assign loop tick, send `DirectiveStatus` IPC, parse response, verify `SkippedBeads` is populated.
- **Integration:** end-to-end `oro doctor bead <id>` against in-memory dispatcher with a known-skipped bead. Verify reason + detail + remediation message.

## Out of Scope

- Auto-recovery commands (`oro doctor reset-in-flight`, etc.) — surface the symptom; remediation is a follow-up.
- Tuning `worktreeFailureCooldown`, escalation caps, or any other dispatcher knob.
- Modifying `filterAssignable`'s logic.
- Replacing the `oro directive` machinery for queries (keep directives imperative).

## Acceptance Criteria

A single shell-runnable acceptance script `scripts/test-skip-reasons.sh` exercises the happy path:

1. Start a test dispatcher against a synthetic bead source returning a bead that triggers each of the 10 reasons.
2. Send `DirectiveStatus`; assert `skipped_beads` contains all 10 with correct reason+detail per D2 table.
3. Run `oro doctor bead <id>` against the `assignment_in_flight` test bead; assert response includes:
   - `reason: "assignment_in_flight"`
   - empty `detail`
   - last-5-events block (may be empty for fresh test)
4. Trigger a transition (clear `assigningBeads[bead]`); assert next status call shows the bead removed from `skipped_beads`, AND the event log contains `bead_skip_exited` for that bead.
5. Run `oro doctor queue`; assert it lists all currently-skipped beads with reason summary.

Additional criteria:

6. **Forensic gate**: after D1-D4 ship, run `oro doctor bead scriptwriter-gts.1` (and the other 3 stuck beads) against the scriptwriter project's still-stuck V2 beads. Capture the output as committed evidence (`docs/plans/2026-04-20-scriptwriter-forensic.md`). The reason identified must match one of the 24 keys (A1-A10 or B1-B14). Hypothesis to confirm: the epic returns `epic_has_open_children` (B11), the children return `epic_branch_pending` (B6). If either matches, the forensic gate succeeds and a follow-up root-cause-fix bead is scoped (likely "auto-assign epic for decomposition before children attempt to assign"). If `oro doctor bead` returns "no skip reason," the diagnostic missed a 25th path — file a spec-revision bead.
7. `oro status` without `-v` shows a one-line summary if `SkippedBeads` is non-empty (e.g., `Skipped: 4 beads (see oro doctor queue)`). With `-v`, expands inline.
8. Event log emits `bead_skip_entered` / `bead_skip_exited` only on transitions; idempotent loop ticks emit nothing.
9. `oro doctor bead <unknown-id>` returns a clear error: "bead not found" (not a stack trace).
10. Cached `SkipReasonsCachedAt` is included in `oro status` output so users can tell freshness.
