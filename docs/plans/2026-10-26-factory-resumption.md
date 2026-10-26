# Factory resumption checkpoint

Date: 2026-10-26. The user explicitly said **proceed** after reading the resume
guide. Implementation is authorized to continue; this is a context handoff, not
a new user-requested pause. Preserve the full unattended factory goal and the
Astra architect / Terra engineer role agreement.

## Latest CLI continuation

This section supersedes historical checkpoints below. `d3ea8d2d` implements core
source execution; full shuffled race passed in 477.308 seconds with 82.3% coverage.
Formatting was repaired in `8ace9029`; `e208d2ba` adds verified pending identity
classification. `9e1e6877` implements authenticated controller launch;
`0e5d7fc6` implements immutable runtime publication; `fb2fd519` fixes recording
peer cleanup. `0e38106b` now registers actual hidden `_supervise` / `_controller`
callers. All component source and evidence followups received review and root
verification; do not reimplement accepted leaves because tracker status remains
in progress under broader qualification constraints.

The [CLI foundations proof](../proofs/2026-10-26-managed-cli-foundations.md)
records scope and evidence. At `0e5d7fc6`, factory-related CLI race passed in
78.565 seconds / 89.672 seconds harness; the gate passed fifteen checks and
failed only five dead exports. Lint/NilAway and formatting passed. Integrated
command checks at `0e38106b` are terminal: the coverage-enabled CLI race failed
the exact Docker-create acknowledgment check (101.439 seconds harness); the gate
passed fifteen checks and failed only three dead exports (163.681 seconds).
Remaining exports are AdmitManaged, ActivateManaged and DecodeDockerCleanupResult.
Evidence is under `/private/tmp/oro-factory-verify-ldhnhnvl/`, prefixes
`managed-commands-integrated-race` and `managed-commands-integrated-gate`.
Task `oro-mf-command-coverage-protocol` investigates the instrumented helper;
coverage telemetry on stderr is a hypothesis, not a confirmed cause. Preserve
the production exact acknowledgment contract. All older jobs are terminal.

The accepted hidden-command source is
exactly Terra `7c2a5136`; root final race passed in 23.149 seconds. Preserve worker
worktrees for provenance, not as unfinished implementation. Recording helpers
exercise real kernel peers and host wrappers but fake Docker; no native runtime
or complete delivery qualification follows.

Next: implement durable service registration, then public start consuming the
existing strict managed-spec decoder. Use
`<root>/.managed/locks/<id>/service.lock`, outside the objective and distinct from
core's `objective.lock`. Release the budget allocation lock first; then service
lock covers existing-read/admit-if-needed, publication/activation and locked
registration. Active replay must not reacquire the supervisor's lifetime lock.
The [CLI contract](2026-10-26-managed-cli-contract.md) records the resolved order.

Terra is investigating coverage in `.worktrees/factory-command-coverage` and
ordinary host retirement in `.worktrees/factory-host-retirement`, task
`oro-mf-host-retirement-bound`. Astra is decomposing service/public-start wiring.
Service-bootstrap uncertainty still needs a proven ownership/reconciliation
mechanism. Candidate execution, independent acceptance, native qualification and
real-model sustained delivery/self-healing trials remain unfinished. The full
goal is active, not paused or complete.

## Earlier controller-source checkpoint

The updates below supersede the earlier verification and next-work snapshot.
`1847b365` and `6d572f54` fix keeper cardinality and Git listing-vector NilAway
findings; `f83e17f9` fixes the CLI prepared-result finding. Activation persistence
is integrated in `26d3858d` after Terra implementation and independent Astra
review, including regressions for all three review findings. Root independently
verified the activation/admission/journal/view race suite before integration.
See the [activation proof](../proofs/2026-10-26-managed-activation.md) for evidence
and the exact limited acceptance boundary.

At `f83e17f9`, the full repository gate passed 15 checks and failed only dead
exports: five managed APIs still lack production callers. NilAway and package
lint passed. The integrated CLI race passed; full shuffled factory race passed
in 409.564 seconds with 89.0% statement coverage (415.953 seconds harness).
All three verification jobs are terminal; do not repeat them absent new changes.

`oro-mf-controller-intent` is implemented in `c8dc8169`, with exact content parity
to Terra's reviewed `eadcaa80`. It atomically reserves
the counted controller token, canonical scope and event after checking active
activation, current supervisor/generation, original deadline, elapsed backoff,
no active cleanup, and positive quiescence of every old scope. The supervisor
reservation's initial generation may be below the current epoch; it must remain
positive and no greater than the current generation.

The [controller-intent proof](../proofs/2026-10-26-controller-intent.md) records
Astra acceptance, root independent final race (25.245 seconds), integrated race
(26.031 seconds), and zero package lint issues. Full package coverage and full
repository gate above predate this new producer. Do not reimplement it merely
because task status remains in progress pending broader qualification.

Grant enforcement is now integrated in `9bdcf40a`; its full shuffled factory
race passed in 453.366 seconds with 89.0% coverage. `6b374d62` implements the
first actual `Supervise → reserveManagedController → StartController` lifecycle.
`055e56f8` adds the optional process-bound snapshot ownership guard. Both received
independent Astra acceptance and root focused race verification. The owner guard
is not yet bound by an actual `RunManagedController` constructor. See the
[startup and ownership proof](../proofs/2026-10-26-controller-startup.md).

At `055e56f8`, the full gate passed 15 checks and failed only six dead exports,
including the new `Supervise` awaiting its CLI caller. NilAway and lint passed.
The combined full race at that revision **failed** after 481.003 seconds on the
supervisor cancellation-cause race; it is terminal, not a passing profile.
`6269d528` fixes that reproduced defect and passed independent review and root
supervisor race tests. The original failure remains preserved in mode
`supervisor-owner-integrated-full` (exit one, 488.537 seconds harness).

`e3df6dde` integrates managed interruption propagation. `c627f6c3` integrates
workspace persistence after independent Astra review and root focused races.
The new full race **passed** in mode `workspace-interruption-integrated-full`:
480.631 seconds, 85.2% statement coverage, 487.817 seconds harness, at frozen Go
code `c627f6c3`. Its terminal result is in
`/private/tmp/oro-factory-verify-ldhnhnvl/`; do not rerun absent new changes. Mode
`workspace-interruption-integrated-gate` is terminal: 175.878 seconds, fifteen
checks passed, six dead exports remain; lint/NilAway passed. Only checkpoint
documentation changes in the core worktree during this run.

`oro-mf-controller-source` is now accepted and integrated in `d3ea8d2d`, exact
four-file parity with Terra's final `4a008b48`. Both independent Astra reviews
accepted the typed-nil fix and actual continuity/public-owner/candidate-stage
regressions. Worker final matrix passed in 42.995 seconds; root final entry race
passed in 20.145 seconds. See the
[actual source-consumer proof](../proofs/2026-10-26-managed-controller-source.md).
No implementation agent remains active for this leaf; preserve its worktree for
provenance and do not repeat it without a new defect.

Full verification at `d3ea8d2d` is terminal: race PASS477.308 seconds / 82.3%;
the initial gate failed formatting and was later corrected. Earlier profiles
must not be attributed to later revisions.

The workspace, interruption, owner and supervisor component implementations are
accepted and integrated, not awaiting reimplementation merely because tracker
statuses remain in progress under broader qualification constraints.

Root owns integration. Actual managed backend checkout and RunManagedController
now connect raw export and production source preparation to controller.attempt.
Next: decompose and implement real CLI/service callers and authenticated launch;
no new public preparation API or fake production reference. Candidate execution
and full delivery remain unfinished. Both prior verification handles are
terminal; the new entry's two verification jobs above are the only live checks.

Typed HELLO/GO and generic controller-scope denial are implemented. Real
CLI consumers remain. Existing generic budget tests
continue to pass; controller-kind fixtures now use the typed producer. No component
task is a substitute for the full startup or autonomous delivery milestone.
The parent autonomy goal remains active and unachieved.
The updated [startup contract](2026-10-26-managed-startup-contract.md) preserves
the actual consumer contract. Astra's next CLI proposal is in
[the CLI contract](2026-10-26-managed-cli-contract.md); it still needs bounded
task decomposition and concrete runtime/retirement reconciliation before coding.

## Earlier implementation and verification history

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
- `e16b7079` (Terra source commit `717261ab`): invalid internal Git directory
  topology returns an error instead of panicking or producing an accepted tree.
  Astra found no defects; root's raw-Git/topology race run passed (19.373 s).
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

`oro-mf-git-tree-topology` is implemented, reviewed, and integrated. Its worker
branch/worktree remains preserved for provenance, not active implementation.
Do not repeat either Git repair without a new defect. The combined integrated
raw-Git race passed (26.509 s package / 30.693 s harness). The repository-scope
NilAway recheck, using the gate's exact flags, reports only the three remaining
production findings below. Evidence prefixes are `git-guards-integrated` and
`git-guards-nilaway-gate` in the existing private evidence directory. The earlier
`git-guards-nilaway` run used default test-file reporting and is not the required
gate scope. Revalidate result/session status before rerunning anything; an
observation timeout is not evidence of termination.

The remaining nil-safety work concerns keeper running-inspection `raw[0]`, raw
Git listing `sizes`, and CLI host runtime's prepared-result pointer. The latter
two parser/vector findings need invariant-preserving fixes, not checker
suppression. Pair gate repairs with the real startup integration below; do not
keep expanding disconnected infrastructure while delaying production wiring.

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
- All current implementation/review agents and integrated verification sessions
  finished. No native fixture resources were created by these increments.
- Preserve unrelated primary-checkout changes and all existing Colima state.
- Read this checkpoint plus the earlier resume guide before selecting work. The
  user's proceed instruction remains in force; do not ask to resume again solely
  because an older checkpoint says paused. The thread goal is now active.
