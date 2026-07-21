# Durable janitor cadence across dispatcher restarts

## Purpose

Preserve the project-wide janitor and audit cadence when an Oro dispatcher is
stopped and started again. A completion is one bead whose gated integration has
completed and whose closure succeeded; it is not one branch. This includes a
normal integrated bead and a proven no-op integration. Janitor and audit always
inspect `main`.

## Evidence and constraints

- `pkg/dispatcher/dispatcher.go:2966` and `:3079` are the only two successful
  completion paths; both call `maybeTriggerJanitor` after assignment cleanup,
  bead closure, and integration logging.
- `pkg/dispatcher/dispatcher.go:3177` currently keeps
  `mergesSinceJanitor` and `janitorRunsSinceAudit` only in the `Dispatcher`
  struct. It runs once the interval is reached and the queue is idle, forces
  at three intervals, and replaces every configured Nth eligible janitor cycle
  with an audit.
- `pkg/dispatcher/dispatcher.go:3232` and `:3268` restore counters when a
  janitor or audit fails; durable state must preserve the same retry semantics.
- `pkg/protocol/schema.go:160` already creates the dispatcher-owned SQLite
  `kv_store`; `pkg/beadstore/shadow.go:96` shows the established guarded
  load-or-initialize pattern. Values are text, so a JSON representation can
  preserve the existing `uint64` counter semantics without a table migration.
- `cmd/oro/db.go:46` applies the state schema and `:202` backfills `kv_store`
  for existing state databases before a dispatcher is built.

## Success criteria

1. A dispatcher restarted against the same state database resumes the
   project-wide counters and any pending role exactly.
2. The 40th safely integrated bead schedules a cycle when the queue is empty;
   a busy queue defers it, but the 120th safely integrated bead schedules one
   regardless of queue depth.
3. Every fourth eligible cycle schedules an audit in place of the janitor,
   including when the first three cycles occurred before a restart.
4. Normal integrations and proven no-op integrations each advance the same
   durable counter once. Any path where `CloseBead` fails does not advance it.
5. A scheduled role is durably pending before launch, is cleared only after a
   successful complete cycle, and is reconciled against `main` before a
   restarted dispatcher can count further beads.
6. A persistence failure does not silently advance, reset, launch, or clear a
   role.

## Options considered

### A. Derive progress from closed beads or cleanliness journeys

Rejected. Closed beads do not reliably distinguish a gated integration from
other closure causes, and role-bead journeys do not preserve the exact deferred
merge budget or whether a selected cycle was restored after failure. Replaying
history would also be unbounded and brittle against retention.

### B. Add a dedicated SQLite cadence table

Rejected for this change. It makes the state shape visible and queryable, but
adds a migration solely for two counters. SQLite's signed integer type would
also require changing the current `uint64` saturation behavior.

### C. Store a versioned JSON record in `kv_store` (chosen)

Use one project-wide key with counters encoded as decimal strings and a pending
role marker. This is migration-free, preserves `uint64`, and uses the existing
state-DB durability boundary.

## Chosen design

### Durable record

Create a dispatcher-local `janitorCadenceStore` backed by `*sql.DB` with a
single key:

```
janitor_cadence/v1
```

The value is canonical JSON:

```json
{"version":1,"merges_since_janitor":"39","janitor_runs_since_audit":"3","pending_role":""}
```

The record applies to the project, not a target branch. Every role receives the
literal target branch `main`, independent of the base branch used for ordinary
bead integration. The store validates both decimal fields as unsigned 64-bit
integers and `pending_role` as empty, `janitor`, or `audit`. `version` is
required to equal `1`; unknown versions and unknown JSON fields are rejected
with a contextual startup/read error. An absent key means `{1,0,0,""}` and is
initialized atomically with `INSERT ... ON CONFLICT DO UPDATE` only when the
first successful mutation is committed.

### Dispatcher integration

`New` constructs the cadence store only when janitor cadence is enabled. It
loads the project record before returning and hydrates the existing in-memory
fields. A nil database or failed load is an explicit construction error for an
enabled cadence; disabled janitor behavior remains a no-op and does not read or
write cadence state. Production wiring through
`buildDispatcherWithReviewTimeoutsAndCleanliness` must preserve this behavior.

For this feature, **cadence-active** means `JanitorEnabled &&
JanitorInterval > 0`. When cadence is disabled, startup neither replays nor
clears an existing pending marker; it remains durable until a cadence-active
startup resumes it. This preserves an operator's explicit disablement without
discarding required maintenance.

Add `func (d *Dispatcher) recoverPendingCadence(ctx context.Context) error`.
`Dispatcher.Run` calls it after construction/state initialization and before it
opens its socket, starts background loops, or accepts assignment/merge work.
If `pending_role` is non-empty, the helper runs that role synchronously against
`main`. A fully successful cycle atomically clears `pending_role`; a failed or
interrupted one leaves it unchanged and returns an error, preventing the
dispatcher from processing further beads. This makes restart recovery explicit
rather than silently dropping a selected cycle.

The existing scan boundary (`withScanWorktree`) and janitor detector invocation
must receive the literal branch `main`, not `Config.DefaultBranch`. This applies
to normal selected cycles and startup recovery, and must be proven with a
non-main `DefaultBranch` fixture.

Replace direct counter mutation in `maybeTriggerJanitor`,
`restoreJanitorCadenceAfterFailure`, and
`restoreAuditCadenceAfterFailure` with one lock-held transition:

1. Copy the in-memory counters and apply the existing arithmetic and gate
   decision.
2. When a role is selected, persist the resulting pair plus its `pending_role`
   synchronously with an upsert.
3. Only after persistence succeeds, publish the state in memory, release
   `d.mu`, and launch the selected role asynchronously.

If the write fails, retain the prior state, emit a
`janitor_cadence_persist_failed` dispatcher event, and do not launch or clear a
cleanliness role. A successful role atomically clears its marker. A role failure
leaves its marker present for startup recovery rather than restoring counters
and allowing a later cycle to overtake it.

While `pending_role` is non-empty, every safely closed bead still increments
and persists `merges_since_janitor`, but cannot select, replace, or launch a
second role. When the pending role clears successfully, the dispatcher evaluates
the accumulated count without incrementing it and reserves the next eligible
role if the idle/forced gate permits. This prevents loss of completions while
guaranteeing one durable role reservation at a time.

The mutex already serializes counter changes inside one dispatcher. The
synchronous SQLite write establishes the crash boundary: a restart observes
either the prior state (no cycle was scheduled), or an explicit pending role;
it never observes an in-memory-only increment, reset, or completed role.

### Semantic boundaries

- Production defaults become `--janitor-interval=40` and
  `--audit-every-n-janitors=4`; the forced-run multiplier remains three, so its
  bound is 120 safely integrated beads. Explicit flag values continue to
  override these defaults.
- Keep cadence settings runtime-only. Changing explicit settings does not
  reinterpret or reset saved progress; the next completion evaluates the
  persisted counters under the newly supplied settings.
- A janitor cycle is *eligible* when it passes the existing idle gate or forced
  three-interval gate. Only then does `janitorRunsSinceAudit` advance.
- The audit replacement is counted as an eligible cycle, matching current
  behavior. It resets both counters and records `pending_role:"audit"` before
  execution; success clears the marker and failure leaves it for retry.
- No-op integrations remain eligible only after proof and safe bead closure.
  Both merge completion paths must return before cadence mutation when
  `CloseBead` fails.
- Findings created by janitor/audit are ordinary beads. When one is safely
  integrated, it counts as one of the 40 project-wide beads.

## Error handling and observability

- Invalid durable JSON is not treated as zero: startup fails with the key and
  parsing cause so an operator can repair the state deliberately rather than
  silently losing cadence.
- A transient write failure leaves both memory and disk unchanged. Log the
  persistence failure with operation (`advance`, `reserve`, or `clear`) but
  never counter values that could be stale after a failed write.
- Restart recovery failure is surfaced before the dispatcher accepts work, with
  the pending role preserved for a safe retry.
- Existing janitor/audit failure events remain intact. This change does not add
  a public CLI flag or alter their worktree, triage, or filing behavior.

## Test plan

Primary epic acceptance:

```
Cmd: test "$(git branch --show-current)" = main && go test ./pkg/dispatcher/... -run '^(TestJanitorCadencePersistsAcrossDispatcherRestart|TestJanitorCadenceTransitions|TestJanitorCadenceExcludesFailedClose|TestCadenceScansMainRegardlessOfDefaultBranch|TestJanitorStartPlumbing)$' -count=1
Assert: on main, restart persistence, 40/120 gating, fourth-cycle audit substitution, failed-close exclusion, literal-main scans, and 40/4 production defaults all pass.
```

Add focused dispatcher/store tests for:

1. Loading a missing record as zero and round-tripping counters, including
   `math.MaxUint64` values and a required `version:1` field.
2. Rejecting malformed JSON, unknown version, missing fields, and non-uint64
   strings without overwriting the record.
3. Restart after 39 safely integrated beads: bead 40 schedules a janitor only
   when the queue is within the idle threshold; restart after 119 busy beads:
   bead 120 force-schedules it.
4. Restart after three eligible janitor cycles: the next eligible cycle schedules
   only the audit and resets the durable audit counter.
5. Reservation is durable before launch; a restarted dispatcher runs its pending
   role against `main` before it accepts/merges another bead, then clears the
   marker only after success.
6. Both `finalizeSuccessfulMerge` and `handleNoopMerge` advance once after a
   successful close; merge failure, close failure, and cadence write failure do
   not.
7. Completions during a blocked pending role accumulate durably but cannot
   replace its marker; clearing the first role evaluates the accumulated budget
   exactly once.

Run the focused package suite and the full dispatcher package after integration:

```
go test ./pkg/dispatcher/... -count=1 -timeout 180s
```

## Delivery map

| Workstream | Delivers | Verification |
|---|---|---|
| Cadence record | Strict versioned JSON codec, `kv_store` load/upsert, malformed-state rejection | `TestJanitorCadenceStoreRoundTrip` |
| Counter transition | 40/120 gate, fourth-cycle substitution, durable reserve/clear, pending guard | `TestJanitorCadenceTransitions` |
| Safe completion boundary | Count only after `CloseBead` succeeds in normal and no-op paths | `TestJanitorCadenceExcludesFailedClose` |
| Startup recovery | Replay pending role before listener/assignment startup; retain marker on failure | `TestJanitorCadencePersistsAcrossDispatcherRestart` |
| Main scan pinning | Worktree and detector targets use literal `main` even with another default branch | `TestCadenceScansMainRegardlessOfDefaultBranch` |
| Production defaults | Start config defaults to 40 and 4, while explicit settings retain their values | `TestJanitorStartPlumbing` |

The Oro task graph will split these workstreams into independently verifiable
leaves after the design gate passes.

## Risks and mitigations

- **Tiger — crash between reservation and role completion:** retain the pending
  marker and reconcile it against `main` before processing new beads. A crash
  after a role completes but before its marker clears can repeat a scan; that is
  safe and preferable to silently skipping maintenance.
- **Tiger — malformed operator-edited state:** fail closed at startup and name
  the exact key; never reset it automatically.
- **Paper tiger — concurrent counter updates:** dispatcher `d.mu` serializes
  local mutations; SQLite serializes the durable upsert. Oro has one dispatcher
  per project/socket, so cross-process arbitration is out of scope.
- **Elephant addressed — target-branch mixing:** cadence is deliberately
  project-wide while cleanliness scans are deliberately pinned to `main`.

## Non-goals

- Reconstructing cadence from historic beads or retroactively counting prior
  completions.
- Persisting other in-memory dispatcher counters.
- Changing queue-depth rules, the forced-run multiplier, or audit content.
- Introducing a user-facing cadence status command.

## Rollback

Disable janitor with the existing `--janitor-enabled=false` flag. The retained
`kv_store` entry is inert and can be reused safely if cadence is re-enabled;
the implementation should make no destructive schema change.
