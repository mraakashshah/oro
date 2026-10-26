# Managed CLI continuation contract

Status: Astra proposal, unimplemented. Core controller source consumption is
being implemented as `oro-mf-controller-source`. Do not call nonexistent core
APIs or claim startup/delivery/recovery qualification from this proposal.
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

Concrete backend must implement StartController, Retire, Quiescent and
ReleaseHostReservation while reusing the existing host runtime. Controller
retirement needs its own exact persisted request lifecycle; ordinary host
invocation state assumes result publication. Resolve whether to use the earlier
proposed supervisor-backend wrapper or narrowly extend the runtime before
implementation. Keep `Invoke` rejecting controllers. Positive group absence
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

Order: bounded strict spec, real host-budget admission, AdmitManaged,
ReadManagedAdmission, runtime publication/verification, ActivateManaged, durable
service intent, then bootstrap. Reject unsupported platforms before reservation.
Keep legacy run behavior and managed-ID rejection intact.

Immutable bounded service intent binds root/id, active reference, HardStop,
frozen Oro, exact plist digest and one persisted random nonce. Stable name:
`dev.getoro.factory.<objectiveHash16>.supervisor.<persistedNonce32>`.
Serialize with a dedicated objective service lock, not the supervisor lock held
throughout Supervise. Persist bootstrap-issued before launchctl and acknowledged
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
