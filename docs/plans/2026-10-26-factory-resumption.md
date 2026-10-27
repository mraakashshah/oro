# Factory resumption checkpoint

Date: 2026-10-26. The user explicitly said **proceed** after reading the resume
guide. Implementation is authorized to continue; this is a context handoff, not
a new user-requested pause. Preserve the full unattended factory goal and the
Astra architect / Terra engineer role agreement.

## Latest public-start checkpoint

Public managed start is reviewed and integrated at `3bcff84f`. Its production
source exactly matches Terra `a09a7e63`; root verified an empty cmd/oro/pkg/factory
diff between those revisions. The accepted sequence includes verified pending
identity, frozen runtime publication, authenticated hidden supervisor/controller,
bounded host retirement, durable prepared service and registration, then real
Cobra `factory start`. Do not reimplement accepted leaves because tracker status
remains in progress under broader qualification constraints.

The [CLI foundations proof](../proofs/2026-10-26-managed-cli-foundations.md)
records component commits, reviews, failures and evidence. Public start commits
are `c02d6c20`, `2c3b8a55`, `03930252`, `3bcff84f` (Terra sources `f6cddc34`,
`8ccc7ee3`, `cb73ffc9`, `a09a7e63`). Source worker worktrees are preserved for
provenance, not unfinished implementation. No implementation agent is active for
this accepted slice.

Review found a real API-level gap: reserve spec A, admit spec B with the same
reservation tuple, then start A could register B. The accepted repair carries
existing issuer SpecSHA256 in HostReservation and compares the submitted input
before executable canonicalization or admission effects. Shared
PrepareManagedSpecIdentity validates/canonicalizes Repository only/hashes the
whole spec; it does not touch original model/Git paths. The CLI retains its
existing allocation retention/minimum policy. Valid digest-bound historical B32
metadata remains readable/resumable, but execution and new allocation remain
denied below current bounds. Historical state missing the digest fails closed
without backfill. Preserve all these regression assertions.

Astra accepted the final source with no Critical/Important findings. At frozen
Terra `a09a7e63`, full factory race passed in 475.999 seconds and affected CLI race
passed in 59.554 seconds; package lint reported zero issues and standalone gofumpt
was clean. Root independently ran scoped race with coverage: factory 2.189 seconds
/18.6% statements and CLI 11.561 seconds/12.7%, 30.782 seconds harness. These are
scoped profiles, not full-package coverage. Evidence prefix is
`start-identity-reviewed-race` in `/private/tmp/oro-factory-verify-ldhnhnvl/`.
Worker full-race log is `/tmp/oro-reservation-factory-final-race.log`.

Integrated jobs at `3bcff84f` are terminal: `start-integrated-race` passed in
132.109 seconds package /147.442 seconds harness, 25.9% CLI statements. The gate
passed fifteen checks and failed only DecodeDockerCleanupResult (176.876 seconds).
Formatting, lint and NilAway pass. Start consumes AdmitManaged and ActivateManaged.
Later repository runtime/security/build tiers remain unqualified by this early
gate. Evidence prefixes are `start-integrated-race` / `start-integrated-gate`.
No root verification handle remains live; earlier jobs are terminal too.

The [runbook](../runbooks/factory-core.md) distinguishes legacy trusted-adapter
run from managed startup. Success acknowledges registration, not service health,
autonomous uncertain-bootstrap recovery or delivery. No native launchctl, Docker,
VM or model call ran for these increments; existing Colima remains untouched.
Capacity remains charged on uncertain outcomes. Preserve the external service
lock at root/.managed/locks/id/service.lock, released budget allocation lock first,
and active replay without reacquiring supervisor lifetime ownership.

## Candidate execution checkpoint

Candidate intent is reviewed and integrated in `9e0d037b`, with owned bounded
leader subprocesses (`1b5c3e18`) and real child coverage (`21956184`). It reserves
one candidate, container scope and create invocation atomically, checks canonical
source/keeper/workspace history, and persists the worker deadline once. The
checkout deadline is not reused for worker execution; replay cannot renew either.
Root's final intent race passed in 74.714 seconds with 105 of 154 new candidate
coverage blocks exercised. Integrated CLI race passed in 168.687 seconds.

The full integrated factory race at `21956184` **failed**: a workspace COMMIT
fault fixture's 150ms context expired before reaching COMMIT under instrumentation;
the package later reached its 600s test limit while still making progress. This
is not a passing full profile. `1e20587f` integrates the reviewed test repair:
allow three seconds to reach the real SQLite one-second COMMIT busy timeout,
retain exact rollback/state assertions, and defer reader rollback. Root's focused
repair race passed (44.791 seconds harness). Production deadlines are unchanged.
The gate at `21956184` passed fifteen checks and failed only the unused
`DecodeDockerCleanupResult`; later runtime tiers remain unqualified.

Candidate execution is reviewed and integrated through `79b3b87d`
(`92cd5545`, `82960726`, `50976804`, `79b3b87d`; Terra originals
`b2789795`, `43b78bef`, `250b8469`, `e251f890`). The actual managed controller
reserves the candidate, validates keeper continuity under the original worker
bound, creates and inspects a hardened sandbox, runs authenticated READY, and
binds its running lifetime. Candidate raw namespace inspection rejects JSON case
aliases and requires explicit Init=false and private UTS. The command explicitly
sets --init=false. Expired checkout does not reseed source or renew deadlines.
Root independently verified the final source race (154.381 seconds harness).
The integrated candidate/workspace race passed at `79b3b87d` (165.145 seconds
harness, mode `candidate-execute-integrated-race`).

The consumer deliberately returns `engineer-adapter-unavailable`: no Terra model
or tool execution exists yet. Reviewed engine fixtures are integrated in
`91c0357a`, `9f8e111b`, `7b28d29a` (originals `44b3ef14`, `fee60917`,
`069745f7`); the actual CLI fixture is `e09f2c7d` (original `54b8a52c`).
Engine child ownership, independent timeout and real coverage forwarding are
included; worker final engine race passed in 31.068 seconds with 51.4% coverage.
The CLI preserves authenticated controller/host/READY transport, independent
hardening assertions, twenty host calls and zero-work revoked mode.

The stricter actual CLI fixture exposed production SQLite writer contention:
the supervisor could enter stopping on SQLITE_BUSY. Reviewed fixes are integrated
through `2ce97949`: `9efc53d6`, `e1b4a735`, `6fd30aab`, `2ce97949`
(Terra originals `5da1d90f`, `c8840e5d`, `5c21da81`, `4c022098`). Retry is
limited to unsuccessful BEGIN, with cancellable local-lock acquisition, exact
SQLITE_BUSY classification and no callback/write/COMMIT replay. Review reproduced
a deadline crossing; four authority callbacks now check max(caller-observed,
current) time after acquisition. Historical metadata remains readable after
HardStop. Cleanup fixtures reserve real targets while live then wait for expiry,
without fabricated past-time authority. Worker final authority race passed in
23.619 seconds, affected cleanup/contention race in 78.146 seconds, and actual CLI
covered race count=3 in 61.305 seconds. Lint reports zero issues. The unchanged
SQLite one-second busy timeout can delay returning a short context error; do not
claim a hard RPC-return bound.

Root combined verification at frozen Go revision `2ce97949` is terminal:

- `candidate-journal-integrated-cli`: PASS, 124.503 seconds package /135.033
  seconds harness, 26.3% CLI statement coverage.
- `candidate-journal-integrated-full`: FAIL, 829.559 seconds package /834.080
  seconds harness, 86.5% statements, shuffle seed `1793064629213050000`.
  The only reported failure is `TestFactoryDependencyBoundary`: direct modernc
  driver imports in managed_transaction.go violate factory's stdlib/dbutil-only
  boundary. No runtime assertion failed, but this is not a passing full suite.
- `candidate-journal-integrated-gate`: FAIL, 217.690 seconds, fifteen passes and
  only unused `DecodeDockerCleanupResult` failing. Lint, NilAway and formatting
  passed; later runtime tiers remain unqualified.

Evidence lives under `/private/tmp/oro-factory-verify-ldhnhnvl/`. Preserve failed
results. The reviewed dependency repair is integrated as `defaff52` (Terra `e620d00e`).
Factory calls dbutil.IsExactSQLiteBusy, preserving typed exact base-code matching;
real extended BUSY, LOCKED, wrapped and untyped errors are tested. Root independent
race passed in 27.466 seconds. No new full rerun is active; the original full
profile remains failed, not retroactively green.

Keeper init policy (`oro-mf-keeper-init-policy`) is reviewed and integrated in
`fdf9e6e3` / `b134fd5e` (Terra `ca7fed55` / `09e4f046`). Creation explicitly
sets --init=false; shared strict raw Init/UTS checks apply before keeper
stopped/running decode. Root focused race passed in 25.137 seconds. Two worker
broader candidate runs timed out at their fixed 180s child limit under overlapping
load. Root's independent combined candidate/keeper race subsequently passed in
170.622 seconds (`keeper-candidate-reviewed-race`), with the limit unchanged.
Root's final narrow integrated race at `b134fd5e` passed in 44.848 seconds
harness (`keeper-boundary-integrated-race`), covering dbutil exact classification,
dependency boundary, journal transactions and keeper/raw namespace validation. No native
qualification is implied. Existing Colima and user checkout changes remain intact.

Ready-only candidate retirement is being implemented by Terra in
`.worktrees/factory-candidate-retire`, branch `feature/factory-candidate-retire`,
task `oro-mf-candidate-retire` under managed delivery. The actual execute path
must reserve irreversible retirement, invoke authenticated counted cleanup,
decode a positive exact stopped result, record quiescence, and retain the explicit
engineer-adapter-unavailable interruption. No removal/export or success receipt.
A typed exception may retain only the exact durable parent controller while all
other non-Docker scopes are quiescent. Ordinary execution grants keep original
work-deadline/no-cleanup gates. Shared identity-only extraction `84b66987` is
reviewed but not integrated; all retirement authorization needs separate review.
Full inherited labels remain exact; oversized existing 16KiB cleanup permits
must fail before fence/token/scope mutation. Uncertain retirement never retries
or renews its fixed deadline.

Retirement remains WIP, not accepted. Preliminary review found omitted immutable
snapshot checks in typed reload; Terra is restoring validateManagedOwnerSnapshot
and regressions. A real tiny-source/inherited-label fixture also reproduced a
permit larger than ArchiveBytes: typed cleanup preflights its existing 16KiB
permit and complete 64MiB request envelope before committing the fence or token.
Cleanup permit retention was already included in metadata allowances; no global
source/archive quota change is permitted. Actual CLI helper validation must match.

Astra resolved initial reservation after objective expiry as recovery authority:
validate the structural canonical candidate, saved immutable snapshot/source,
exact granted parent PID/PGID, current running generation and runtime, under a
fixed cleanup deadline no later than HardStop. Ordinary execution grants remain
denied after work expiry. Only execute owns a separate bounded cleanup context;
it may ignore an inherited DeadlineExceeded whose deadline equals the objective
work deadline exactly. Earlier caller deadlines and explicit cancellation must
propagate. Direct retireCandidate never detaches its supplied context. Legal
stop/epoch revocation still disables the live-parent exception. These require
frozen-source review and tests, not inference from the design.


The [restricted Terra adapter research](../proofs/2026-10-26-terra-adapter-research.md)
proposes an Oro-owned Responses function loop; it is research, not an implemented
adapter. Model/tool execution, stopped export/import, protected acceptance/review,
cleanup and uncertain-service/VM reconciliation, native qualification and sustained
unattended exact delivery remain unfinished. No native Docker/launchctl/VM or
model calls ran for these increments. Direct Terra engineering and Astra review
are development coordination, not autonomous factory evidence. The full goal
remains active and unachieved.

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
