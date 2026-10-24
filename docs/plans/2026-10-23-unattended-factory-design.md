# Unattended factory design

Status: Gate 1 and native task coverage Gate 2 PASS after independent review and
contract repairs. Runtime qualification remains outstanding.

## Goal and acceptance

Extend the existing delivery lifecycle into an unattended service that can run a
real coding model, execute its candidate safely, independently accept an exact
commit, and recover automatically from controller and supervisor death.
Routine human restart, cleanup, review approval, and repair are not acceptable
steps in the qualification scenario. Failure must remain bounded and truthful.

The implementation epic's binary acceptance command is:

```sh
./scripts/qualify_factory.sh --docker-context colima-oro-factory --iterations 10
```

The script builds the current checkout's CLI, records its source revision and
binary digest, creates only private fixture repositories/state, and exits zero
only if every scenario below passes. Run on the implementation branch during
development and again on the integrated main revision before cutover. Missing
Docker, launchd, model authentication, or required test scenarios is a failure,
not a skip. A JSON evidence report records every scenario and invocation count.

1. Real Claude produces and repairs an implementation of an admitted objective;
   held-out acceptance and independent review precede exact-SHA promotion.
2. At least ten objectives across three fixture specifications finish without
   human actions. The fixture expectations are fixed before model execution.
3. Kill the actual controller during an active model/container invocation; only
   observe afterward. Its replacement delivers, old scopes are gone before new
   work, the interrupted attempt remains spent, and the deadline is unchanged.
4. Kill the supervisor during active work; launchd restarts it and the same
   cleanup-before-replacement invariants hold.
5. Kill after real Git CAS but before durable completion; automatic observation
   records the original exact candidate, including on the final allowed start
   and after the work deadline. No worker/check/review is repeated.
6. Attack protected authority, forge receipt-looking output, detach children,
   change checker source, and return unsafe archives. No protected hash changes,
   forged success, surviving old execution, or out-of-root import is accepted.
7. Repeated crashes, unavailable backends, exhausted budgets, duplicate starts,
   corrupted frozen artifacts, and cleanup uncertainty all stop within limits.
   Unknown promotion outcome remains unknown; no false success or false failure.
8. Execute delivered code again from a fresh export of the promoted commit.
9. With controller and supervisor killed and their journal unavailable, a detached
   container writer still dies at its admitted deadline. It cannot signal or
   replace the trusted deadline watchdog. Missing watchdog readiness prevents GO.
10. Live source/scratch byte and inode exhaustion stays within assigned mounts;
    a subsequent objective remains healthy. Attribute tricks cannot hide or
    substitute committed blobs from either checker. An unavailable supervisor
    reservation store causes one bounded stopped incarnation, not a restart loop.

This milestone is necessary, not sufficient, for the user's full factory goal.
Subsequent qualification must cover autonomous intake, specification/decomposition,
task scheduling, failure repair objectives, dependency outages, and sustained
delivery of changes to Oro itself. Do not mark the parent autonomy epic complete
after this single-objective service. No old dispatcher cutover until those paths
have equally concrete operational evidence.

## Constraints and trust boundary

- Keep `pkg/factory` limited to standard library plus `pkg/dbutil`; preserve the
  architecture test. Host adapters at the CLI boundary may use independent leaves.
- Preserve legacy `factory run` and its trusted-adapter semantics. A managed
  objective rejects ordinary `Run` so callers cannot bypass its isolation.
- Use the dedicated `oro-factory` Colima VM: no host filesystem mounts, no SSH
  agent, no active-context replacement. The existing default profile is unrelated.
- Pin the qualification base image to
  `golang@sha256:27f2d3b83cfc5803c8078f8e730af0f7c431bf067afaec8f60a36fb763187cba`.
  Build the trusted watchdog into a derived image using only that base and the
  statically built watchdog artifact. Record the base digest, watchdog digest,
  and resulting immutable image ID; admit the exact derived image ID, never its
  mutable tag. Build context contains no source repository or host credentials.
- Candidate code receives no host paths, credentials, Docker socket, controller
  state, original repository metadata, policy files, or peer volumes.
- Host Oro/Git/Claude/MCP runtimes, launchd, Docker, the VM, and kernel are trusted.
  Candidate source, test processes, stdout, and model tool arguments are untrusted.
- A per-user LaunchAgent proves availability within the logged-in session. Host
  failure, logout, VM reboot, and unattended login are separate required platform
  qualification before claiming continuous operation across those events.
- Fixed original objective deadline, attempt count, model-call/cost reservation,
  controller-start count, and supervisor-start count survive all restarts.

## Managed admission and public surface

Add `factory start --spec FILE --state-dir ABSOLUTE_DIR`, `factory stop`, and
`factory wait`; extend `factory status` with supervision status. `start` returns
after durable admission/service registration, not after delivery. `wait` exits
zero only for completed delivery; durable stopped/failed outcomes exit nonzero.
`status` remains a read-only observation command.

Use a distinct strict-JSON managed specification, without legacy executable
worker/checker fields. Its objective fields are ID, objective text, repository,
target branch, required check IDs, maximum attempts, lifetime seconds, and step
seconds. Additional frozen fields are:

```go
type RecoveryPolicy struct {
    MaxControllerStarts, MaxSupervisorStarts int
    RestartDelaySeconds, CleanupTimeoutSeconds int
    ReconciliationSeconds, MaxReconciliationInvocations int
}
type RuntimePolicy struct {
    DockerContext, ImageDigest string
    MemoryBytes, ScratchBytes, SourceBytes, HostRequiredBytes, HostReserveBytes int64
    CPUs float64
    PIDs, MaxFiles, SourceInodes, ScratchInodes int
    MaxModelCalls, MaxModelTurns, MaxToolCalls, MaxHostInvocations int
    Model, ModelExecutable, GitExecutable string
    MaxModelInvocationUSD string // positive decimal, converted to integer cents
}
type Check struct {
    ID string
    Args []string // literal argv inside the candidate container
    Stdin, ExpectedStdout string
    ExpectedExit int
}
type ManagedSpec struct {
    ID, Objective, Repository, TargetBranch string
    RequiredChecks []string
    MaxAttempts, TimeoutSeconds, StepTimeoutSeconds int
    Recovery RecoveryPolicy
    Runtime RuntimePolicy
    Acceptance []Check
    ReviewInstructions string
}
type HostReservation struct {
    ID, StateRoot, ObjectiveID string
    Bytes int64
}
```

The manifest freezes acceptance checks, independent reviewer instructions,
runtime policy, recovery policy, canonical repository identity, and executable
identities/hashes. Every receipt binds its complete manifest hash, base SHA, and
candidate SHA. Reject zero/unbounded/overflowing budgets, empty checks, duplicate
IDs, unknown JSON fields, mutable image tags, executable paths inside source,
invalid branch/path identities, and policies incompatible with runtime limits.
Bound the entire specification stream to 1 MiB, including whitespace; reject
overflow distinctly from EOF. Direct typed admission also enforces this encoded
artifact bound, including JSON escape expansion, before creating durable state.

Admission uses fixed lock order: supervisor lock, then objective lock. Publish a
managed marker before objective creation, so interruption cannot let legacy Run
adopt this ID. Freeze artifacts atomically, then transactionally write the
objective and managed admission identity. No service is registered before this
commit. Repeated identical admission is idempotent; changed identity is rejected.
An interrupted admission either resumes identical frozen content or reports an
actionable bounded error; it must never silently replace artifacts.

`AdmitManaged(ctx, stateDir, spec, reservation) (Status, error)` and
`RunManagedController(ctx, stateDir, id, generation, token, host) (Status, error)`
are the package boundaries. Resume uses admitted artifacts exclusively, even if
the original input file is removed. The CLI supplies a host adapter, not a
mutable path from candidate output. Keep legacy `Run` policy loading unchanged.

Before allocating an objective/scope, the CLI adapter invokes the existing pure
`storage.CheckRuntimeBudget` with a canonical existing parent, admitted host byte
requirements, reserve margin, and `Fresh=true`. Require `BudgetFresh` explicitly:
insufficient space can return `BudgetDenied,nil`. Failed probe or denial creates
no scope, spawns no controller, and consumes no attempt. Compute a conservative
maximum retained host byte count from source/attempt limits, bounded command
outputs/transcripts, frozen artifacts, and bounded SQLite pages/events; reject
HostRequiredBytes below that amount. Every writer enforces its limit before
writing, including archive streaming and model transcript capture. Count all
host invocations and MCP calls, not only model turns. Initial managed support
requires source repository and state on the same filesystem; reject otherwise
until separate filesystem reservations are implemented.

Serialize admission under the canonical factory root's allocation lock and
persist outstanding host reservations before scope allocation. Subtract active
reservations from observed available capacity; do not let simultaneous objectives
both consume the same headroom. Recovery preserves reservations while cleanup is
uncertain. Release a reservation only when its bounded work is terminal and all
scopes are retired; existing retained bytes remain reflected in measured free
space. Container limits enforce separate VM/candidate policy. Do not introduce
the unused legacy assignment reservation/catalog lifecycle.

The CLI admission helper returns a persisted HostReservation, never a release
closure. Bind its ID/root/objective/byte tuple in managed admission. `start` must
not release it on successful return. The supervisor backend supplies
ReleaseHostReservation(ctx, id), reopening the canonical root's ledger in its
own process. Release is a durable idempotent CAS after terminal state and all
scope quiescence; stop and terminal recovery use the same path. If death occurs
after terminal commit but before release, a new incarnation finishes release.
Uncertain cleanup preserves the reservation. Cross-process tests must prove each
case and that the next objective can use released capacity.

## Durable supervision and process ownership

Use dedicated SQLite tables/events for supervision and scope ownership, separate
from the objective snapshot revision. Persist supervisor/controller starts,
generation, desired state, next eligible start time, stop reason, and scope rows.
Each row includes random ID/nonce, generation, kind, exact backend name, phase,
authenticated PGID when relevant, and cleanup evidence. Intent precedes creation.

```go
type ScopeHandle struct {
    ID, Nonce, Kind, Backend, Name string
    Generation uint64
}
type HostInvocation struct {
    Scope ScopeHandle
    Path string
    Args, Env []string
    Dir string
    Stdin []byte
    Deadline time.Time
    OutputLimit int64 // admitted bound; bulk Git transport is distinct from logs
}
type HostResult struct { Stdout, Stderr []byte; ExitCode int }
type HostRuntime interface {
    Invoke(context.Context, HostInvocation) (HostResult, error)
    Retire(context.Context, ScopeHandle) error
}
```

`HostRuntime` is a trusted dependency. `Invoke` returns only after process-group
quiescence; `Retire` returns nil only after exact-scope retirement is established.
Both enforce bounded time and output. Backend uncertainty is an error, never an
empty success. Production implementation is launchd on this host; unsupported
platforms fail admission explicitly until their backend is qualified.

The backend receives an admission-bound scope authority with methods to record an
authenticated handshake, check its grant, observe persisted ownership, and mark
quiescence. These operations
compare objective ID, generation, nonce, and current lifecycle phase in one
transaction. Host backend code verifies the peer's real PID/UID before submitting
the handshake; a claimed JSON PID is insufficient. It cannot grant a revoked or
superseded generation. Bulk transfer limits come from frozen source limits;
diagnostic output defaults to 1 MiB per stream. All retained representations,
including JSON/base64 overhead, count toward the host byte reservation.

`Observe(ctx, handle)` returns the exact scope's persisted phase, authenticated
PID/PGID, and deadline. It permits reading revoked old epochs for cleanup, never
grants them, and rejects unknown or mismatched handles. A fresh host backend must
use this operation to recover ownership evidence; it must not infer process
identity from names or independently query the journal schema.

Service structure:

```text
launchd -> stable objective supervisor (restart on unsuccessful exit)
launchd -> unique generation controller (one-shot)
launchd -> unique host invocation wrapper (one-shot) -> Claude / MCP / Git
dedicated VM -> scope-labelled candidate containers and private volumes
```

Labels use a project-owned prefix plus objective hash and random nonce; never
reuse `dev.getoro.oro`. One-shot jobs set KeepAlive=false,
AbandonProcessGroup=false, finite ExitTimeOut. Trusted children inherit the job
PGID; the managed path does not use legacy `runProcess`'s Setpgid behavior.

The host job renderer receives a private wrapper invocation, with the admitted
Oro executable as its program. Its literal arguments identify an immutable
`request.json`, its SHA-256, and an owned Unix socket; the renderer appends the
fixed deadline and output bound. The request contains the actual command. No
payload command is launched directly by a plist. Host scope labels are
`dev.getoro.factory.<objectiveHash16>.<host-or-observer>.<ID32hex>.<Nonce32hex>`; the runtime
binds the objective hash and verifies socket ownership. Jobs disable globbing,
use umask 077, and send launchd streams to `/dev/null`; the wrapper separately
writes bounded pending results. The initial finite exit timeout is five seconds.
The private `factoryHostExecRequest` contains the actual `HostInvocation` and an
explicit `ResultPath`. The wrapper obtains objective-bound authority through the
authenticated socket; it does not independently open a supplied journal path.
The hidden CLI requires `ResultPath` to equal the request file's private directory
plus `result.pending.json`; command working directories never select receipt
destinations. The runtime verifies the canonical parent and ownership before
launch. An authenticated GO acknowledgment is one-shot for its exact scope;
revocation followed by whole-job retirement prevents an old scope overlapping
its replacement. The wrapper rechecks the immutable deadline before spawning.

Before bootstrap, journal the scope. A wrapper starts behind an authenticated
Unix-socket handshake: HELLO includes scope nonce/PID/PGID; validate caller and
scope, durably record ownership/grant, then send GO. Before GO it creates no
descendants or mutable work. On recovery revoke old grants first. A lost or late
bootstrap acknowledgment cannot produce an untracked mutator. Persisted PIDs
are diagnostics, never authority for signalling recovered processes.

Stop exactly journaled job labels with `launchctl bootout`. After any grant,
require `kill(-recordedPGID, 0)==ESRCH` within cleanup timeout. Probe only: never
kill a stored numeric PID. A reused/surviving group conservatively blocks. Do not
parse `launchctl print` as an API. Ungranted jobs cannot have descendants or
effects; stale grants cannot be replayed. Preserve cleanup intents across death.

For normal completion, `_host-exec` atomically writes a bounded pending result,
exits, and launchd retires remaining same-group children. The controller consumes
that result only after quiescence. Timeout also retires the whole invocation.
Trusted runtimes must not deliberately detach from their job PGID; all untrusted
code executes in containers, whose PID namespace includes detached descendants.

Supervisor entry reserves an incarnation before backend actions, holds its lock
through cleanup/backoff, retires every old host/container scope, and requires
positive quiescence before reserving a new controller start. It journals each
start before bootstrap. No new attempt is refunded after interruption. Duplicate
supervisors cannot both dispatch. `stop` durably revokes desired-running state,
retires owned scopes, and unregisters only owned services.

The supervisor job uses KeepAlive.SuccessfulExit=false and bounded throttle.
Every durable stopped outcome exits zero to prevent a hot loop; `wait/status`
carry the actual failure. Admission also fixes a hard-stop timestamp in service
argv, equal to work deadline plus bounded reconciliation/cleanup allowance.
Failure to reserve an incarnation performs bounded diagnostics, performs no
backend actions, and exits zero: launchd must not repeat an uncounted incarnation.
Distinguish admitted incarnations from crashes before reservation in evidence.
The immutable hard stop remains an outer bound, including startup failures before
ordinary service control can run. Database corruption never permits a fresh
objective with reset budgets. Preserve diagnostics when state cannot be read.
Container expiry below does not require the journal, Docker client, controller,
or supervisor to remain available.

After old scopes are gone, always allow one bounded observation/finalization
pass, including after deadline/start exhaustion. If promoting and target equals
candidate, complete. If unchanged base, no late mutation. If target differs,
preserve it and retry only within original budgets. If unreadable, keep promoting
and its proofs while supervision stops with promotion_outcome_unknown.

SupervisorBackend embeds HostRuntime explicitly. Supervise uses it directly for
read-only Git observation/finalization when no controller starts remain; it does
not start a special uncounted controller or use an undocumented type assertion.
Reserve each observation from the separate immutable
MaxReconciliationInvocations allowance before launch, bounded by the hard stop.
Use a fresh ownership epoch for these observer scopes after revoking old grants;
their admitted command set is read-only Git observation. They cannot perform
update-ref or resume work. Tests require zero additional StartController calls
and unchanged attempt/controller counts after exhausted-start reconciliation.
Observer scopes use explicit kind `observer`, require a same-generation
reconciliation reservation whose token equals the scope ID, and may use only the
fixed hard-stop allowance. Ordinary host scopes retain the work-deadline bound.
Advancing an ownership epoch within a live supervisor spends a separately bounded
epoch token and never spends another supervisor incarnation. The persisted
current supervisor token fences a superseded supervisor from advancing epochs.

## Candidate execution, source import, and checks

The controller owns all Git authority. Export committed source without `.git`;
never give a linked worktree or host bind mount to a candidate. Initial support
rejects symlinks, submodules, hardlinks, devices, FIFOs, absolute/traversal paths,
duplicate paths, case-colliding names, and any case-folded `.git` component.
Bound archive bytes, expanded bytes, file count, per-file size, and path length.
Parse and validate before importing; no extraction into controller/origin paths.

Export through raw `ls-tree`/`cat-file` object traversal and verify each resulting
path, mode, and blob against the selected commit. Never use attribute-sensitive
`git archive`: `export-ignore` and `export-subst` must have no effect. Apply this
same equivalence check to worker input, acceptance, review, and the final fresh
execution proof. Test a failing file marked export-ignore and a blob containing
export-subst syntax; every committed byte must remain visible unchanged.

Construct candidate objects with trusted `hash-object --no-filters`, tree
construction, and `commit-tree`, preserving executable file modes and expected
base parent. Do not run checkout/add filters, smudge/clean programs, or hooks on
candidate-controlled attributes. Keep existing expected-old update-ref CAS and
receipt-bound exact SHA promotion. Never rebase after acceptance.

Journal container/volume names and nonce labels before Docker create. Create
stopped, persist returned ID, then start. Recover by exact pre-recorded labels
and names, including create-success/ack-loss; inspect ownership before removal.
Do not rely on `docker run --rm`. All Docker operations are bounded host
invocations; daemon side effects are reconciled separately from client exit.

Each worker gets private source/scratch mounts. Source is a Docker local-driver
tmpfs volume with explicit byte and inode limits, held mounted continuously by a
trusted keeper container until candidate namespace retirement and export finish.
Plain per-container tmpfs is insufficient for source: it disappears on stop.
Scratch is a separate size/inode-bounded per-container tmpfs. Journal keepers and
volumes before creation; lost keeper state fails the attempt rather than importing
partial source. Acceptance and review get separate read-only source volumes and
independent scratch. Account for source, scratch, keepers, and runtime memory
when admitting VM capacity; cap all Docker logs with log-driver none and capture
only bounded host invocation output. Export through trusted Docker copy/object
handling, never by executing candidate scripts in the keeper.

Use network none, fixed candidate UID/GID 1000 on EVERY Docker exec (including
MCP calls), read-only rootfs, cap-drop ALL,
no-new-privileges, default seccomp, private PID/IPC/cgroup namespaces, numeric
CPU/memory/PID bounds. Verify actual inspection fields before execution.
Copy archives through the Docker API/CLI, with no VM host mounts. Candidate
namespace must be stopped and inspected empty before export/import or reuse.

Every candidate and keeper namespace starts with an immutable trusted PID1
deadline watchdog under a distinct unprivileged UID (65534), with no control
channel exposed to candidate code. Its fixed absolute deadline and monotonic
timer are installed before readiness; candidate work cannot start before that
readiness proof. Candidate UID1000 has no capability to signal, stop, replace,
or reconfigure the watchdog. On expiry PID1 exits, causing the kernel to terminate
the namespace including double-forked/setsid descendants. Worker/checker deadlines
never exceed their original step/objective deadline. Keeper expiry is fixed at
admission to the objective hard stop, allowing bounded stopped-source export and
cleanup. No refresh/heartbeat can extend either deadline. Test signal attempts
and expiry with host control processes dead and ownership state unreadable.

Refactor controller execution behind a private delivery backend while retaining
its existing state/receipt/CAS transitions. The legacy backend preserves current
behavior; the managed backend performs export -> container -> stopped import ->
commit, and separate checker scopes. Existing lifecycle tests must stay green.
All managed host Git calls, including observation and CAS, use HostRuntime; no
late Git process may survive into a successor generation. Preserve that adapter
when constructing fresh Git observers in `observeTarget` and the completed
artifact check; the legacy constructors currently copy only path/timeouts.

Acceptance is a trusted host comparison of each frozen Check's bounded result
from a read-only candidate container. Host code creates the receipt only when
all required IDs pass. Candidate stdout is never parsed directly as a receipt.
Review uses a separate restricted model with read-only source and independent
instructions, then validates a strict structured verdict. Positive prose does
not replace valid receipts. Failure feedback is bounded untrusted text.

## Real model adapter

Keep OAuth on the host. Run authenticated Claude from an empty trusted working
directory with `--restricted --tools "" --strict-mcp-config`, a sole explicit
MCP server, `--allowedTools mcp__factory__exec`, `--permission-mode dontAsk`,
`--permission-prompts none`, disabled slash commands/browser/session persistence,
and stream JSON output. Freeze executable identity, model, turn limit, per-call
cost limit, and total invocation reservations. Use supported `--max-budget-usd`;
reserve full call allowance before launch and never refund after a crash.

The stdio MCP server exposes only exec(command, timeout_seconds). Its container,
working directory, backend endpoint, and absolute deadline come from trusted
startup state. Tool arguments cannot select host paths, Docker flags/container
IDs, daemon endpoints, or environment overrides. Shell interpretation occurs
only inside the fixed container. Reject unknown fields/oversized JSON and bound
requests/results. The checker/reviewer bridge cannot write the source mount.

Validate initial model tool inventory and fail closed on unexpected tools or
connectors. Exercise actual authenticated CLI behavior before qualifying this
adapter; matching command-line strings alone is insufficient. A prompt asking
for host reads/execution must not create a host effect. Defer Codex until its
complete host-tool boundary has equivalent proof.

## Implementation graph and coverage

Every row becomes a tracked child with exact signatures from this document,
test-first RED/GREEN, independent review, and one atomic commit. Split a row
further when its implementation exceeds a small reviewable change.

| Task | Deliverable / wiring | Required acceptance tests | Depends on |
| --- | --- | --- | --- |
| U1 | Managed manifest validation/admission; serialized CLI host reservation before allocation | TestManagedAdmission, TestManagedAdmissionBudget, TestConcurrentHostAdmission | — |
| U2 | Separate durable supervision/scope journal; immutable counters/grants | TestSupervisionJournal, TestScopeGrantCAS | U1 |
| U3 | Launchd host invocation backend and hidden `_host-exec` CLI | TestHostScopeNormalExit, TestHostScopeLostAck, TestHostScopeTimeout | U2 |
| U4 | Exact raw-tree export, validated import, filter-free Git object creation | TestCandidateExportExactTree, TestCandidateImportRejectsEscapes, TestCandidateCommitAvoidsFilters | U1 |
| U5 | Journaled Docker scopes/keepers, enforced quotas and independent watchdog deadlines | TestExecutionAuthorityIsolation, TestExecutionCreateAckLoss, TestCheckerSourceReadOnly, TestExecutionStorageLimits, TestExecutionIndependentDeadline | U2,U3,U4 |
| U6 | Controller delivery-backend seam; legacy behavior preserved | existing factory suite; TestManagedControllerBackendWiring | U1,U3,U4,U5 |
| U7 | Frozen host acceptance and receipt authority | TestReceiptAuthorityIsolation, TestManagedAcceptanceExactCandidate | U6 |
| U8 | Scoped MCP + real Claude adapter and review | TestMCPAuthorityBoundary, TestModelToolSurface, TestRealModelRepair | U3,U5,U7 |
| U9 | Supervisor loop, frozen resume, cleanup, budget exhaustion | TestSupervisionRecovery, TestSupervisorBudget, TestSupervisorReservationUnavailable, TestManagedResumeFrozenInputs | U2,U3,U5,U6 |
| U10 | `start/wait/stop/status`, service registration, controller startup gate | TestFactoryManagedCLI, TestManagedRunCannotBypass | U8,U9 |
| U11 | Real SIGKILL fault harness through shipped CLI | TestControllerDeathReapsExecutionScope, TestSupervisorDeathRecovery, TestDeathAfterPromotion | U10 |
| U12 | Sustained qualification script, proof/runbook, integration | single acceptance command above; fresh delivered-code checks | U11 |

Mandatory source integration points: `cmd/oro/root.go` existing factory command
registration; `cmd/oro/cmd_factory.go` subcommands; `pkg/factory/engine.go`
admission/resume/attempt/check/promotion; `contract.go` status and frozen policy;
`store.go` durable state; `runner.go` legacy execution preserved; `git.go` managed
host invocation seam; `architecture_test.go` dependency guard. New files should
separate managed contract/journal, supervisor, source archive, Docker operations,
MCP/model adapters, and Darwin launchd CLI backend.

The post-CAS real CLI fault test may admit a trusted qualification Git wrapper
that executes real Git, signals after successful update-ref, then blocks. Kill
only the actual controller after the barrier. Do not expose production fault
environment switches or manually clean orphan workers to manufacture recovery.

## Rollout and completion limits

Keep the old dispatcher stopped for these trials and leave its unrelated task
queue untouched. Use fixture repositories until isolation and failure tests pass.
Then qualify against a disposable Oro clone with independent repository gates.
Integrate the service only after review and rerun proof on the merged revision.
Preserve user changes in the primary checkout throughout.

No code inside this design alone delivers autonomous requirement quality,
arbitrary deployment, unlimited self-modification, or recovery from every host
failure. The continuing parent goal must add those concrete workflows and their
failure budgets before claiming a trustworthy unattended software factory.
