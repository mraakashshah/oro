# Durable janitor cadence across dispatcher restarts

## Purpose

Preserve janitor and audit cadence progress when an Oro dispatcher is stopped
and started again with the same cleanliness settings. A completion is counted
only after gated integration has completed and the bead is safely closed. This
includes both normal integrated branches and proven no-op merges.

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

1. A dispatcher restarted against the same state database and target branch
   resumes both counters exactly.
2. The 50th completion schedules a cycle when the queue is empty; a busy queue
   defers it, but the 150th completion schedules one regardless of queue depth.
3. Every fifth eligible cycle schedules an audit in place of the janitor,
   including when the first four cycles occurred before a restart.
4. Normal integrations and proven no-op merges each advance the same durable
   counter once, and no unsuccessful merge path advances it.
5. If scheduling or the selected cleanliness cycle fails, the persisted state
   retains the current retry behavior; persistence failure itself must not
   silently advance, reset, or schedule a cycle.

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

Use one key per target branch, with counters encoded as decimal strings. This
is migration-free, preserves `uint64`, isolates independent `main` and epic
branch runs, and uses the existing state-DB durability boundary.

## Chosen design

### Durable record

Create a dispatcher-local `janitorCadenceStore` backed by `*sql.DB` with a
single key namespace:

```
janitor_cadence/v1/<target-branch>
```

The value is canonical JSON:

```json
{"merges_since_janitor":"49","janitor_runs_since_audit":"4"}
```

The target branch is `Config.DefaultBranch` after defaults are applied. Branch
scoping prevents merges to an epic branch from changing the cleanup cadence of
`main`. The store validates both decimal fields as unsigned 64-bit integers and
rejects missing/unknown schema versions or malformed values with a contextual
startup/read error. An absent key means `{0,0}` and is initialized atomically
with `INSERT ... ON CONFLICT DO UPDATE` only when the first successful mutation
is committed.

### Dispatcher integration

`New` constructs the cadence store only when janitor cadence is enabled. It
loads the branch record before returning and hydrates the existing in-memory
fields. A nil database or failed load is an explicit construction error for an
enabled cadence; disabled janitor behavior remains a no-op and does not read or
write cadence state.

Replace direct counter mutation in `maybeTriggerJanitor`,
`restoreJanitorCadenceAfterFailure`, and
`restoreAuditCadenceAfterFailure` with one lock-held transition:

1. Copy the in-memory counters and apply the existing arithmetic and gate
   decision.
2. Persist the resulting pair synchronously with an upsert.
3. Only after persistence succeeds, publish the pair in memory, release
   `d.mu`, and spawn the selected janitor or audit asynchronously.

If the write fails, retain the prior pair, emit a `janitor_cadence_persist_failed`
dispatcher event, and do not launch a cleanliness role. The next real
completion retries the write from the unchanged state. Cycle failures use the
same transition/persist/publish sequence for their restoration arithmetic.

The mutex already serializes counter changes inside one dispatcher. The
synchronous SQLite write establishes the crash boundary: a restart observes
either the prior state (no cycle was scheduled) or the selected/reset state (a
cycle was scheduled), never an in-memory-only increment or reset.

### Semantic boundaries

- Keep cadence settings runtime-only. Changing `--janitor-interval` or
  `--audit-every-n-janitors` does not reinterpret or reset saved progress; the
  next completion evaluates the persisted counters under the newly supplied
  settings.
- A janitor cycle is *eligible* when it passes the existing idle gate or forced
  three-interval gate. Only then does `janitorRunsSinceAudit` advance.
- The audit replacement is counted as an eligible cycle, matching current
  behavior. It resets both counters before its asynchronous execution begins;
  audit failure restores one interval and leaves the audit counter at `N-1`.
- No-op merges remain eligible because their existing path invokes the shared
  trigger only after proof and safe bead closure.

## Error handling and observability

- Invalid durable JSON is not treated as zero: startup fails with the key and
  parsing cause so an operator can repair the state deliberately rather than
  silently losing cadence.
- A transient write failure leaves both memory and disk unchanged. Log the
  persistence failure with target branch and operation (`advance`,
  `restore_janitor`, or `restore_audit`) but never counter values that could be
  stale after a failed write.
- Existing janitor/audit failure events remain intact. This change does not add
  a public CLI flag or alter their worktree, triage, or filing behavior.

## Test plan

Primary epic acceptance:

```
Cmd: go test ./pkg/dispatcher/... -run '^TestJanitorCadencePersistsAcrossDispatcherRestart$' -count=1
Assert: a fresh dispatcher reusing the same SQLite state database retains the pre-restart merge and eligible-cycle counters, schedules the expected janitor/audit at the configured threshold, and writes the resulting counters back to the same branch-scoped key.
```

Add focused dispatcher/store tests for:

1. Loading a missing record as zero and round-tripping counters, including
   `math.MaxUint64` values.
2. Rejecting malformed JSON, unknown version, missing fields, and non-uint64
   strings without overwriting the record.
3. Restart after 49 completions: completion 50 schedules a janitor only when
   the queue is within the idle threshold; restart after 149 busy completions:
   completion 150 force-schedules it.
4. Restart after four eligible janitor cycles: the next eligible cycle schedules
   only the audit and resets the durable audit counter.
5. Both `finalizeSuccessfulMerge` and `handleNoopMerge` advance once; merge
   failure and cadence write failure do not.
6. Janitor and audit failure restoration persists and is still present after a
   new dispatcher is constructed.

Run the focused package suite and the full dispatcher package after integration:

```
go test ./pkg/dispatcher/... -count=1 -timeout 180s
```

## Risks and mitigations

- **Tiger — crash between persistence and role execution:** the counter is
  already reset, so a crash can defer that selected scan until later progress.
  This is no worse than the current asynchronous spawn boundary and avoids
  duplicate roles. Future work can add an in-flight lease only if missed scans
  become operationally unacceptable.
- **Tiger — malformed operator-edited state:** fail closed at startup and name
  the exact key; never reset it automatically.
- **Paper tiger — concurrent counter updates:** dispatcher `d.mu` serializes
  local mutations; SQLite serializes the durable upsert. Oro has one dispatcher
  per project/socket, so cross-process arbitration is out of scope.
- **Elephant addressed — target-branch mixing:** state is branch-scoped rather
  than global, because janitor and audit inspect the selected target branch.

## Non-goals

- Reconstructing cadence from historic beads or retroactively counting prior
  completions.
- Persisting other in-memory dispatcher counters.
- Changing cadence flags, defaults, queue-depth rules, forced-run multiplier,
  or audit content.
- Introducing a user-facing cadence status command.

## Rollback

Disable janitor with the existing `--janitor-enabled=false` flag. The retained
`kv_store` entry is inert and can be reused safely if cadence is re-enabled;
the implementation should make no destructive schema change.
