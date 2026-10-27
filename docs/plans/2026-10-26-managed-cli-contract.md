# Managed CLI continuation contract

Status: core consumer, concrete launch wrapper, runtime publication and hidden
commands, durable prepared service, registration and public start implemented.
Do not claim native startup, delivery or recovery qualification from
this document.
Keep `oro-mf-cli-start` and the full autonomy objective open through real wiring.

## First actual core callers and authenticated controller launch

Add hidden `_supervise` and `_controller` commands only when their complete
call paths compile. `_supervise` reads committed admission and active runtime
reference, requires exact supplied reference and original HardStop equality,
verifies frozen artifacts, constructs the concrete supervisor backend, and calls
`factory.Supervise`. Core chooses and spends its supervisor generation.

Controller launch consumes the exact counted scope. Persist a bounded canonical
request binding complete scope, root/id, active runtime reference and original
deadline before bootstrap. Use a separate one-shot controller plist invoking
frozen Oro; do not send controllers through ordinary `Invoke` or its result
publication protocol. Start returns after authenticated HELLO/GO, not completion.
Core snapshots carry the outcome. Lost acknowledgment remains uncertain/charged.

Reuse existing private artifact/socket and frame primitives. Authenticate real
Unix peer UID/PID and Getpgid(peerPID), with PID=PGID>1; compare complete request
digest and scope. Call RecordHello, Observe and Granted before GO, serialized
against retirement. `_controller` does no work before GO. Afterwards it re-reads
admission/active reference, verifies runtime bytes, constructs the real host
runtime and `newFactoryDockerConfigReader(view, ref.Path, ref.SHA256)`, then calls
the implemented `factory.RunManagedController` with scope generation and ID.

The implemented concrete supervisor wrapper provides StartController, Retire,
Quiescent and ReleaseHostReservation while reusing the existing host runtime.
Controller startup completes after GO; ordinary host invocation continues through
PUBLISHED. Separate records and tombstones preserve that distinction. Keep
`Invoke` rejecting controllers. Positive group absence
and durable exact ownership establish quiescence; probe errors are uncertainty.
Release delegates to the existing ledger and core eligibility check.

Likely files are controller/supervisor commands, Darwin launch transport and
unsupported-platform counterpart, plus command registration. This contract is
larger than one bounded task: decompose without leaving fake entrypoints.

## Frozen runtime publication

Private proposed entry:

```go
func publishFactoryRuntime(ctx context.Context, view factory.ManagedAdmissionView,
    inputs factoryRuntimeInputs) (factory.ManagedRuntimeReference, error)
```

Committed view supplies admission fields; inputs identify initial Oro/Docker
binaries and endpoint. Publish frozen Oro and canonical runtime/context through
exclusive bounded staging, file/directory fsync and atomic publication. Reuse
canonicalFactoryRuntimeConfig, factoryDockerContextSnapshot,
factoryRuntimeConfigPath and the existing reader for post-publication checks.

Respect existing Oro 512 MiB, JSON 64 KiB, context 8 KiB and runtime-bundle 1 MiB
limits plus one equal staging bundle. Verify the retained layout against three
program allowances and twice RuntimeConfigBytes; do not enlarge capacity or copy
Docker as another executable. Identical interrupted publication keeps original
content; conflicting content fails. Active replay gets authority from
ReadManagedRuntimeReference, never a fresh digest of mutable current files.
Activation follows verified publication; publication alone grants no execution.

## Public start and durable service registration

Execute this contract in three sequential leaves:

1. `oro-mf-service-state`: new `cmd_factory_service_state.go`,
   `cmd_factory_service_files.go`, `cmd_factory_service_plist.go` and one test.
   `lockFactoryService(ctx, root, id)` returns an owned `factoryServiceAccess`
   with Close, prepare and transition methods. Publish the initial intent,
   plist and prepared registration as a complete staged directory, then rename
   and parent fsync. Existing final content is verified, never regenerated.
   Interrupted staging retains its nonce; recover only a fully validated
   identical bundle, otherwise return uncertainty. Updates use a fixed exclusive
   stage and atomic registration replacement with exact prior-phase comparison.
2. `oro-mf-service-register` consumes that access through
   `registerFactoryServiceLocked`.
   Darwin's `bootstrapFactoryService` calls existing runFactoryLaunchctl with
   bootstrap, gui/UID and the exact committed plist. A narrow private transport
   parameter permits recording tests. Persist dispatching before the transport,
   then registered only after success; neither uncertain nor registered replay
   bootstraps again. No native launchctl during implementation tests.
3. `oro-mf-start-command` exposes public `factory start`, consumes both previous
   leaves and exposes the strict
   decoder with its actual CLI caller. Use one new start source, existing command
   registration and managed-contract source, plus one focused test. Exercise real
   budget admission, publication, activation and registration through Cobra;
   preserve active replay while the supervisor lifetime lock is held.

The registration leaf uses new `cmd_factory_service.go`,
`cmd_factory_service_darwin.go`, `cmd_factory_service_other.go` and one focused
test file. Private `registerFactoryService(ctx, view, ref) error` uses immutable
versioned `factoryServiceIntent` (root/id, nonce, label, runtime ref, HardStop,
plist digest) and `factoryServiceRegistration` (version, intent digest, phase).
Phases are prepared, dispatching and registered. Canonical records are at most
16 KiB each; plist at most 64 KiB; fixed files/stages and metadata must fit the
existing fixed 1 MiB allowance without a quota increase.

Keep immutable intent/plist and bounded registration in a fixed private objective
service directory; the lock stays outside it. Verify current admission, active
reference, published runtime and live original work deadline under the lock.
Prepared may perform the first dispatch. Persist dispatching before bootstrap;
only exit zero permits registered. Dispatching replay returns uncertainty without
redispatch, false acknowledgement or a new nonce. Missing/corrupt identity fails
closed. Registration acknowledgement is not service health or completed delivery.
Uncertain-bootstrap ownership reconciliation is a required later design, not
something inferred from an existing plist or launchctl print output.

Order: bounded strict spec, real host-budget admission, AdmitManaged,
ReadManagedAdmission, runtime publication/verification, ActivateManaged, durable
service intent, then bootstrap. Reject unsupported platforms before reservation.
Keep legacy run behavior and managed-ID rejection intact.

The public-start consumer change should expose and call existing strict
`decodeManagedSpec` as `factory.DecodeManagedSpec(io.Reader)`, preserving its
1 MiB/duplicate/case-alias/unknown/trailing validation. Do not introduce a
permissive second decoder. Initial options are spec, state-dir, Docker executable
and explicit Unix endpoint; initial Oro is the invoking executable. Existing
active start verifies the identical reservation/spec and committed admission
without blindly calling AdmitManaged while its supervisor lock is held.
Only errors.Is with ErrManagedRuntimePending selects publication; all other
reference-read errors stop. Active replay uses the authoritative reference and
never reconstructs artifacts or manufactures authority from fresh hashes.

The CLI budget ledger already compares the complete caller specification on
replay. Close the separate committed-reservation link with the narrow immediate
consumer `factory.VerifyManagedHostReservation(ctx, stateDir, id, want) error`.
Use the existing read-only openManagedActivation verifier under a 30-second bound
and compare its manifest reservation tuple exactly; do not canonicalize original
executable paths or add a second spec decoder/hash. This helper does not activate
anything, despite the reused internal verifier's name. Cover mismatch, corrupt
identity, removed original executables and unchanged journal state.

**Review correction:** comparing the existing reservation tuple alone is
insufficient. A reservation issued for A could be used to admit B with the same
ID and resource budget; then start A passed both disjoint checks. Carry the
ledger's existing complete-input SpecSHA256 in HostReservation and verify it at
core admission before executable-path canonicalization. Persist the digest
through the exact manifest/journal identity and compare it on replay. The hash
normalizes repository only, matching the issuer; never resolve vanished original
program aliases on active replay. Old manifest reservations missing this digest
must fail closed. This correction is required before public-start acceptance;
the previous narrow verifier is not a substitute for the durable spec binding.
The shared identity helper validates the spec and canonicalizes Repository only;
current retention/minimum allocation checks remain in the CLI issuer, separate
from core metadata admission. Valid digest-bound historical B32 metadata remains
readable/resumable, while existing Git construction and CLI minima deny its
execution/new allocation. Preserve this compatibility test rather than changing
it to expect rejection merely to accommodate the new hash helper.

The production disk probe walks only ENOENT upward to a canonical existing
directory, creates nothing, and uses checked capacity/available-byte arithmetic
from Statfs (Bavail, not Bfree). Check context around traversal and the syscall;
the syscall itself is not context-interruptible. Active replay still requires the
admitted external Docker executable/endpoint; original Oro/model/Git inputs may
be absent because their frozen copies are authoritative.

Immutable bounded service intent binds root/id, active reference, HardStop,
frozen Oro, exact plist digest and one persisted random nonce. Stable name:
`dev.getoro.factory.<objectiveHash16>.supervisor.<persistedNonce32>`.
Use `<root>/.managed/locks/<id>/service.lock`, distinct from core's
`objective.lock`. Complete/release budget allocation first, then acquire the
context-aware service lock BEFORE existing-admission reading or guarded admission.
This serializes duplicate starts without creating an objective before its managed
marker. Reuse verified committed admission without reacquiring a running
supervisor's lifetime lock. Hold service lock through publication/activation and
registration; expose private locked registration to avoid recursive locking.
Never replace or unlink the lock inode.
Persist bootstrap-issued before launchctl and acknowledged
only after success. Repeated acknowledged start reuses intent; lost
acknowledgment preserves uncertainty and capacity without a replacement identity.

Render unsuccessful-exit KeepAlive, bounded throttle, original hard-stop argv and
private streams. Hidden supervisor checks hard stop before mutable authority.
Uncounted startup/reservation failures emit bounded diagnostics and exit zero to
avoid uncounted service loops. Existing Supervise is one lifecycle; KeepAlive
does not turn it into automatic recovery. No attempt refunds or CLI retry loop.
Start/publication/uncertain-registration outcomes never release live capacity.

## Evidence required

Registered Cobra commands, real Unix peers in actual process-group children, and
recording launchctl/Git/Docker transports must reach the real core callers.
Cover changed digest, forged peer, revoked GO, lost bootstrap acknowledgment,
concurrent starts, low disk, publication crash and retained reservations. Then
perform separately enabled native Darwin launchd/Docker qualification; recording
tests do not qualify native behavior. No native action was run by this proposal.

## Production execution after public start

Astra traced the next real failure point to managedDeliveryBackend.execute,
called by controller.attempt after saving the ready workspace. No restricted
model/MCP execution or managed acceptance implementation exists yet. Do not
replace the unavailable result with the legacy worker or a shell in the keeper.
The engineer provider must support the user's Terra role; historical Claude-only
adapter filenames are design history, not a decision to use Claude for engineering.

The proposed next pair of leaves is a durable candidate-container intent and its
actual worker-stage consumer. Bind the exact granted owner, attempt/workspace/base,
runtime reference, original worker deadline, container and host handles, dispatch
phase, complete ID and lifetime identity. Derive resources/labels from admission.
Use a separate container namespace with admitted source volume and bounded scratch,
not the keeper. Existing READY supports container scopes, but core must verify
exact admitted mounts/resources/image/labels/guard lifetime, beyond READY's generic
finite-limit checks. Unknown dispatch remains charged and non-redispatchable.

Astra resolved stage deadlines against the current code: controller.execute passes
the objective deadline; checkout's workspace OperationDeadline governs export and
seeding, not worker execution. On the first worker-stage reservation persist one
UTC execution start and deadline equal to the minimum of now plus StepTimeout,
objective deadline and caller deadline. Replays never refresh either. Preserve
the original source operation record. A ready source whose checkout deadline has
expired may enter its first still-live worker stage; an expired worker stage
cannot be renewed.

The durable-intent leaf uses new `managed_docker_candidate.go`, existing
`managed_store.go` and `managed_activation.go`, plus one new test. Private
`reserveManagedCandidate(ctx, owner, workspaceID)` atomically verifies
workspaceOwner, readManagedWorkspace (not workspaceCurrent's preparation-expiry
gate), ready phase, exact snapshot attempt/Worktree/BaseSHA, runtime, seeded
keeper/volume association and absence of cleanup conflicts. Reserve the container
and counted create-host handle with the bounded canonical record/event in one
transaction. Use a unique workspace association and existing retention/scope
ceilings; add guarded schema/history validation. Missing historical tables fail
closed. Lost dispatch acknowledgement stays charged and non-redispatchable.

The immediate consumer leaf changes managed_backend.go and adds
managed_backend_execute.go/execution_docker_candidate.go, plus one new test.
Actual worker execute reserves once and uses that persisted deadline throughout.
Do not call resumeSource/inspectSource, which intentionally enforce checkout's
deadline: perform candidate-stage keeper continuity through existing inspection
parser and the original binding/StartedAt, under a counted candidate-deadline
host call. Never reseed or rewrite source authority. Create/READY/inspect a distinct
hardened container, including exact resources, mounts and bounded scratch. Tests
must reach actual RunManagedController/attempt/execute. After real preparation,
return explicit engineer-adapter-unavailable interruption until the restricted
Terra/Codex model/tool consumer exists; preserve candidate/workspace/attempt
identity. Container readiness is not a completed worker result.

Real model execution then needs an admitted tool bridge bound to the exact ready
container, counted tool/host/model calls, fixed UID1000/cwd/endpoint, bounded output
and original deadlines. Model-controlled input cannot choose host paths, container,
environment or Docker flags. Frozen model/tool-inventory qualification is required.
Finally, commit must follow positive candidate retirement, keeper continuity,
bounded stopped export and existing managed Git exact commit/CAS. Independent
acceptance and review remain separate protected stages. Namespace startup is not
worker execution, delivery or autonomous recovery qualification.
