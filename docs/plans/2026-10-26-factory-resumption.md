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
- `16f55188`: Terra implemented durable keeper cleanup authority. Reservation and
  permit reload compare complete identity against the canonical keeper record in
  the current transaction. Astra found no defects; root independently verified.
- `47827496`: Terra rejected empty managed Git command vectors before effects.
  Behavioral RED exposed panics, spent authority, and poisoned state; the fix
  removes both source_git.go NilAway findings. Astra review found no defects;
  root independently ran the command/authority race tests successfully (7.893 s).
- These code commits were pushed. A documentation commit may follow this checkpoint.
- Latest full factory shuffled race run at `16f55188` passed in **364.538 seconds**,
  with **89.2%** statement coverage. The later Git guard has focused raw-Git race
  verification and package lint; do not attribute the earlier full profile to it.
- Last full repository gate at `16f55188` was **red**: six nil-safety diagnostics
  and three managed APIs without production callers. The later Git guard removes
  two findings in the package check; a combined repository rerun remains pending.
  Broad Go runtime/security/build tiers were not reached by that earlier gate.

See the [initial verification record](../proofs/2026-10-26-factory-resumption.md)
and [cleanup binding record](../proofs/2026-10-26-keeper-cleanup-binding.md) for
commands, scope, provenance, and remaining gate findings. No Docker/VM
qualification or real autonomous delivery trial ran. The cleanup increment used
a direct `gpt-6.1-terra` implementation agent and `gpt-6-astra` reviewer. This is
development coordination, not a managed Oro factory trial. Local `.oro/config.yaml`
still names `gpt-5.6-terra`; it was not changed to launch this direct worker.

## Cleanup identity task: implemented, qualification pending

`oro-mf-keeper-cleanup-binding` is implemented in `16f55188` and remains tracked
in progress pending global gate/qualification constraints. It is not awaiting
more Terra implementation. Do not repeat it without a new defect. The following
contract is retained for review and regression protection. Its parent
`oro-mf-keeper-image-labels` and the full autonomy objective remain in progress.

Astra's implemented decision enforces identity **inside existing reservation
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

## Next: clear the remaining gates and advance production delivery

`oro-mf-git-tree-topology` is actively delegated to Terra in
`/Users/as21/codehouse/oro/.worktrees/factory-git-tree-topology`, branch
`feature/factory-git-tree-topology`, base `47827496`. Agent task name:
`/root/terra_git_tree_topology`. It owns only `source_git_objects.go` and new
`source_git_objects_test.go`. Do not duplicate its implementation. Observe actual
agent/process status; a polling timeout is not evidence that it stopped. Review
and integrate its committed result before the next combined gate run.

The [startup producer/consumer proposal](2026-10-26-managed-startup-contract.md)
resolves the missing authority producer and explicit config-reader handoff. It is
an unimplemented integration design, not three tiny executable tasks. The next
context must reconcile it against current code and existing task contracts,
then split its large phases into bounded leaves while retaining real producer
and consumer wiring. Do not substitute an unused startup helper or fake test
authority for the actual supervisor/controller path.

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

Astra recommends an explicit `ManagedDockerConfigReader` argument on the
not-yet-implemented `RunManagedController`, reusing CLI
`newFactoryDockerConfigReader` instead of extending every HostRuntime or adding a
provider/options framework. This changes a proposed signature, not existing Go
callers. A material prerequisite remains: select and implement the actual counted,
authenticated controller startup/activation producer and matching core gate.
Finding a token in `managed_reservations` alone is not authority to allocate
sources. The proposed hidden CLI controller command must consume startup-bound
runtime identity and valid activation; never invent the producer only in tests.
Reconcile this proposal with the existing startup/supervisor task contracts before
creating or implementing the next slice.

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
  `oro-mf-gate-format`, `oro-mf-keeper-cleanup-binding`,
  `oro-mf-git-command-guard`, `oro-mf-git-tree-topology`, plus prior
  storage/READY/backend component claims. Cleanup code is verified and integrated;
  its task notes explain the remaining qualification state.
- Open downstream: `oro-mf-managed-delivery`, `oro-mf-cli-start`,
  `oro-mf-qualification`; parent `oro-factory-autonomy` is open and unachieved.
- No task was closed merely because scoped tests passed while the full gate and
  integration requirements remain unresolved.
- At this checkpoint the tree-topology Terra agent is active; the other current
  implementation/review agents and verification sessions finished. No native
  fixture resources were created by these increments.
- Preserve unrelated primary-checkout changes and all existing Colima state.
- Read this checkpoint plus the earlier resume guide before selecting work. The
  user's proceed instruction remains in force; do not ask to resume again solely
  because the older guide or goal metadata still says paused.
