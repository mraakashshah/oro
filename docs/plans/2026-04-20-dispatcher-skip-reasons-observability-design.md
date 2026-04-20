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

### The 10 rejection reasons

From reading `isBeadAssignable` (dispatcher.go:3000-3033), `hasUnresolvedBlockingDep` (3040-3050), and `filterAssignable`'s second pass (2976-2983):

| # | Reason key                  | Source line | Detail field                |
|---|------------------------------|-------------|------------------------------|
| 1 | `status_closed`             | 3001        | none                          |
| 2 | `status_in_progress`        | 3006        | none (human-owned)            |
| 3 | `status_blocked`            | 3006        | none                          |
| 4 | `worktree_failure_cooldown` | 3009        | `retry_after: <timestamp>`    |
| 5 | `active_with_other_worker`  | 3012        | `worker_id: <id>`             |
| 6 | `assignment_in_flight`      | 3019        | none (race window)            |
| 7 | `merging_to_main`           | 3026        | none (race window)            |
| 8 | `exhausted` (escalation cap)| 3029        | `attempt_count: <n>`          |
| 9 | `dependency_blocked_by`     | 3045        | `blocker_id: <id>`            |
| 10| `branch_already_merged`     | 2977        | `branch: agent/<id>` (auto-closes bead) |

For scriptwriter, the most likely culprits are #6 (`assignment_in_flight` register stuck), #4 (residual cooldown), or #8 (escalation cap from prior failed attempts). With current code we cannot tell which.

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

### D3 — Data location: cached for status, fresh for doctor

`tryAssign` (dispatcher.go:2760) already calls `filterAssignable` every assign-loop tick (60s + on `.beads/` change). Piggyback:

```go
type Dispatcher struct {
    ...
    skipReasons     map[string]SkipReason  // beadID → reason; updated by filterAssignable
    skipReasonsAt   time.Time              // cache freshness
}
```

`filterAssignable` is augmented to record the rejection reason for each rejected bead before returning. This is near-zero cost — we already check the conditions; we just need to remember which one fired.

`oro status` reads `d.skipReasons` directly (under lock). Includes `cached_at` so users know freshness.

`oro doctor bead <id>` calls a new `Dispatcher.RecomputeSkipReason(ctx, beadID)` which:
1. Fetches the single bead via `d.beads.Show(ctx, id)`
2. Runs the same checks as `isBeadAssignable` against current state
3. Returns fresh `SkipReason`

Single-bead recompute is cheap (one bd show + one git merge-base check at most).

**Alternatives rejected:**
- Pure on-demand (recompute on every status call) — too slow for shells out per bead × N beads.
- Pure cached (even doctor reads cache) — defeats the "I'm debugging now" use case.

### D4 — Persistence: event log on transitions only

Two new event types in oro's existing event log (the one that emits `bead_lookup_failed`, etc.):

- `bead_skip_entered` — fired when a bead first enters skip-reason X
- `bead_skip_exited` — fired when a bead leaves skip-reason X (assigned, closed, or reason changed)

Payload includes `{bead_id, reason, detail}`.

**No event when the same reason persists across ticks.** This catches transitions ("bead Y entered `assignment_in_flight` 4 hours ago and never left") without spamming on every 60s tick.

Implementation: `filterAssignable` diffs `d.skipReasons` (current) against the previous tick's snapshot. Diffs become events.

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
| `pkg/dispatcher/dispatcher.go`    | Add `skipReasons map[string]SkipReason` + `skipReasonsAt` to `Dispatcher` struct.       |
| `pkg/dispatcher/dispatcher.go`    | Augment `filterAssignable` to populate `d.skipReasons`. Diff against prior tick to emit `bead_skip_entered` / `bead_skip_exited` events. |
| `pkg/dispatcher/dispatcher.go`    | New exported `RecomputeSkipReason(ctx, beadID) (SkipReason, error)` — fresh single-bead check. |
| `pkg/dispatcher/protocol_skip_reason.go` (new) | Define `SkipReason` struct, reason key constants, `Detail` builder helpers. |
| `pkg/dispatcher/dispatcher.go`    | Extend `statusResponse` (line 3667) with `SkippedBeads []SkipReason` + `SkipReasonsCachedAt time.Time`. |
| `cmd/oro/cmd_status.go`           | Render `Skipped beads` block. Default summary line; `-v/--verbose` expands inline.       |
| `cmd/oro/cmd_doctor.go` (new file or extension) | Add `oro doctor bead <id>` and `oro doctor queue` subcommands. |
| `pkg/dispatcher/dispatcher_test.go` | Unit tests for skip-reason recording per category (10 reasons × 1 test each).         |
| `pkg/dispatcher/dispatcher_test.go` | Test for transition event emission (no spam on persistent reason; events on enter/exit). |
| `cmd/oro/cmd_doctor_test.go`      | Tests for `oro doctor bead` and `oro doctor queue` against fake dispatcher state.         |

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

6. **Forensic gate**: after D1-D4 ship, run `oro doctor bead scriptwriter-gts.1` against the scriptwriter project's still-stuck V2 beads. Capture the output as committed evidence (`docs/plans/2026-04-20-scriptwriter-forensic.md`). The reason identified must match one of the 10 keys; that reason becomes the basis for a follow-up root-cause-fix bead.
7. `oro status` without `-v` shows a one-line summary if `SkippedBeads` is non-empty (e.g., `Skipped: 4 beads (see oro doctor queue)`). With `-v`, expands inline.
8. Event log emits `bead_skip_entered` / `bead_skip_exited` only on transitions; idempotent loop ticks emit nothing.
9. `oro doctor bead <unknown-id>` returns a clear error: "bead not found" (not a stack trace).
10. Cached `SkipReasonsCachedAt` is included in `oro status` output so users can tell freshness.
