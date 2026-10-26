# Factory resumption checkpoint

Date: 2026-10-26. The user explicitly said **proceed** after reading the resume
guide. Implementation is authorized to continue; this is a context handoff, not
a new user-requested pause. Preserve the full unattended factory goal and the
Astra architect / Terra engineer role agreement.

## Current code and verification

Worktree: `/Users/as21/codehouse/oro/.worktrees/factory-core`.
Branch: `feature/factory-core`.

- `3d63aeb4`: reject raw null keeper string-map values before JSON decoding can
  normalize them to empty strings. Tests reproduce initial discovery and bound
  stopped/running inspection failures, preserve legitimate empty strings, and
  cover other non-string values. Existing authority counters remain unchanged.
- `c2f8e932`: gofumpt-only corrections in six factory-related Go files and a
  formatting-only Biome exception for the exact archived native JSON directory.
  JSON checking remains enabled; all ten evidence hashes still match.
- Both code commits were pushed. A documentation commit may follow this checkpoint.
- Full factory shuffled race run passed in **366.800 seconds**, with **89.1%**
  statement coverage. Package lint passed with zero issues.
- Full repository gate remains **red**: formatting fixes exposed six nil-safety
  diagnostics and three managed APIs without production callers. Broad Go runtime,
  security, and build tiers were not reached by that gate.

See the [verification record](../proofs/2026-10-26-factory-resumption.md) for exact
commands, scope, commit provenance, and remaining gate findings. No Docker/VM
qualification, Terra worker, or real autonomous delivery trial ran in this
increment. Astra performed bounded read-only repair and architecture reviews.
The configured Terra worker model is `gpt-5.6-terra` in local `.oro/config.yaml`;
this observation is not proof of a successful model invocation.

## First next task: enforce durable cleanup identity

`oro-mf-keeper-cleanup-binding` is open and ready. It is a child/dependency of
`oro-mf-keeper-image-labels`, which remains in progress. The parser fix alone does
not complete that parent: existing cleanup transport tests compare against the
provided permit but do not link it back to the saved keeper record.

Astra's concrete decision is to enforce identity **inside existing reservation
and permit-reload transactions**, not only in a future controller caller:

- In `pkg/factory/managed_cleanup.go`, add private
  `requireCleanupKeeperBinding(context.Context, *sql.Tx, *managedJournal,
  DockerCleanupPermit) error` and call it from
  `reserveManagedCleanupInvocation` before tokens/scopes/events are inserted,
  and from `loadCleanupPermit` after target validation.
- Non-keeper targets retain existing semantics.
- For keeper targets call `readManagedDockerKeeper` with the **current tx**.
  Do not use `loadManagedDockerKeeper`, which starts another transaction.
  Canonical keeper, volume, scope, and reservation validation must all hold.
  Missing/corrupt records fail closed; no compatibility fallback.
- A bound keeper requires full exact durable Binding equality for inspect,
  stop, and remove, including every inherited label and lifetime field. Do not
  permit downgrade to an unknown inspection descriptor.
- An unbound keeper permits **inspection only**, using its exact durable binding
  with ContainerID and CreatedAt cleared. No invented lifetime, extra labels,
  mutation, binding/adoption, or unknown-create replay is authorized.
- Replace the synthetic keeper branch of
  `pkg/factory/managed_cleanup_process_test.go:testManagedCleanupKinds` with real
  keeper-record scenarios, reusing existing fixture helpers. Container/volume
  coverage remains intact. The slice needs one source and one test file.

RED cases: add/change/remove inherited labels, changed image/lifetime, bound-to-
unknown downgrade, canonically tampered permits after reservation, and missing/
corrupt keeper records. Reservation failures must not change counters/revision;
reload/HELLO/grant paths must not issue grants. Exact bound operations pass.
Prepared/dispatching and acknowledged-unbound records allow only the exact
unknown inspect shape. This does not prove successful unknown-create discovery
or recovery; that remains unresolved and must not be manufactured by weakening
comparison.

## After that: advance the real production path

Astra traced the existing flow:

```text
cmd/oro factory run -> factory.Run -> legacyDeliveryBackend
controller attempt -> target -> checkout -> execute -> commit
controller check -> independent checkout -> pristine -> execute -> pristine
```

Managed source preparation and raw Git transport exist, but managed controller,
candidate create/exec/retire/export orchestration, and `RunManagedController` are
absent. The **first production consumer** of `prepareSource` should be managed
worker checkout, followed by candidate execution and stopped import.

The real `factoryLaunchdRuntime` can run Docker and the frozen Oro READY helper.
It is private to `cmd/oro`, while source construction/preparation is private to
`pkg/factory`. Add the actual managed controller boundary to connect them; a CLI
native test can then pass the real runtime through that entry. Do not expose a
test-only preparation API or reproduce launchd in a package fixture.

Resolve the narrow trusted `ManagedDockerConfigReader` dependency when defining
controller construction; `HostRuntime` currently does not supply it. Preserve
generation/token admission, durable attempt/stage-to-source identity, original
deadlines/budgets, and the explicit backend interface. A managed workspace string
must resolve to journaled identity rather than candidate-controlled host paths.
Partial wiring must fail closed before execution/receipts; it is not delivery.

Next stages must reload seeded keeper/volume identity, run the candidate as
UID1000 with bounded scratch, positively stop and inspect the exact candidate,
recheck keeper continuity, decode bounded stopped export, and use existing
managed Git commit/re-export/CAS. Acceptance/review require separate sources and
trusted frozen checks/receipts. Legacy stdout receipt parsing does not qualify
managed acceptance.

The gate's dead exports should acquire their actual startup/recovery callers,
not dummy references or `testonly` annotations. Resolve nil-safety diagnostics
with tested guards/invariants, not suppression. Full Oro check-runtime supply and
unknown daemon/VM epoch recovery remain the previously documented prerequisites.

## Tracker snapshot and continuation rules

- In progress: `oro-mf-keeper-image-labels`, `oro-mf-source-prepare`,
  `oro-mf-gate-format`, plus prior storage/READY/backend component claims.
- Open ready next leaf: `oro-mf-keeper-cleanup-binding`.
- Open downstream: `oro-mf-managed-delivery`, `oro-mf-cli-start`,
  `oro-mf-qualification`; parent `oro-factory-autonomy` is open and unachieved.
- No task was closed merely because scoped tests passed while the full gate and
  integration requirements remain unresolved.
- At this checkpoint both independent agents finished, all verification sessions
  exited, and no native fixture resources were created by this increment.
- Preserve unrelated primary-checkout changes and all existing Colima state.
- Read this checkpoint plus the earlier resume guide before selecting work. The
  user's proceed instruction remains in force; do not ask to resume again solely
  because the older guide or goal metadata still says paused.
