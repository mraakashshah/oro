# Usable managed Docker volume v1 implementation plan

> For Codex: use executing-plans and TDD for an assigned child. Root and independent review accepted this plan, including the admission-replay guard. Tracked children are `oro-mf-volume-intent`, `oro-mf-volume-adapter` and `oro-mf-volume-config`. Execute them in dependency order; native storage and daemon-recovery qualification remain required.

**Goal:** Create and inspect one fresh, owned source volume through the existing trusted HostRuntime, with durable one-shot intent and exact lifetime binding.

**Architecture:** An atomic journal reservation produces an immediately usable version-1 `prepared` intent. A private core adapter commits `dispatching`, makes the one exact create call itself, and records `acknowledged` only from that call's successful return. A separately counted inspect binds CreatedAt. The CLI supplies a narrow verified immutable configuration reader; no new host-publication protocol is required.

**Tech stack:** Existing Go journal transactions, SQLite FULL commits, HostRuntime, canonical runtime artifact reader, immutable admission view and DockerCleanupBinding.

This replaces the mandatory receipt subsystem and permanently non-executable v1 in the previous proposal. It follows independent review `/private/tmp/oro-factory-verify-ldhnhnvl/docker-storage-design-review-2026-10-24.md`, SHA256 `d9735d7e4276beaff4dd31987011d1f2e03e12d5a5798877a2425b94fb48c96a`.

## Verified seams and limits

- `managedJournal.mutate` authenticates admission inside a transaction, updates one supervision revision and appends one event only when its callback reports a change. Its event ceiling is `8*ScopeRecords+16`.
- `finishManagedAdmission` calls `initManagedSchema` under the objective lock on identical admission replay as well as first admission. New intent-table DDL therefore needs an explicit committed-admission guard; placing it in the existing unconditional `CREATE TABLE IF NOT EXISTS` batch would recreate lost history.
- `reserveManagedToken` and `journal.insertScope` already accept an existing transaction. Therefore reserve H, the create-host scope, the volume scope and the intent in **one transaction**. Do not compose `reserveManagedHostInvocation`, whose deliberately separate commits would break this atomic contract.
- `factoryLaunchdRuntime.Invoke` authenticates HELLO/GO/PUBLISHED, retires the exact host scope and verifies the pending result before returning HostResult. A nil error is the trusted completion boundary. A private adapter knows the exact call it made; it needs no caller-supplied request/result certificate.
- Runtime config and its strong file/snapshot reader currently live in `cmd/oro`; the core has no Docker config DTO. `ManagedAdmissionView` already supplies immutable root/objective/policy/Git/repository/ref/deadline/context identity. The small producer/consumer seam below fills this gap without moving the reader or expanding ScopeAuthority.
- Current Docker cleanup planners/parsers are cmd-private. The first core volume inspector must parse its own narrow bounded volume response; it must not fabricate a cleanup permit just to reuse that parser.
- There is no managed controller/storage orchestration implementation yet. These children produce a functioning private adapter plus the real CLI configuration producer, as the current private managedGitRepository does. Existing `oro-mf-docker-storage`, controller/CLI-start and native storage qualification remain the actual top-level consumers; no public test-only volume command or dummy production caller is added.

## Shared v1 record and lifecycle

```go
type managedDockerVolumeIntent struct {
    Version             int                  `json:"version"`
    AdmissionSHA256     string               `json:"admission_sha256"`
    RuntimeConfigSHA256 string               `json:"runtime_config_sha256"`
    Volume              ScopeHandle          `json:"volume"`
    CreateHost          ScopeHandle          `json:"create_host"`
    WorkDeadline        time.Time            `json:"work_deadline"`
    CreateDeadline      time.Time            `json:"create_deadline"`
    Phase               string               `json:"phase"`
    Binding             DockerCleanupBinding `json:"binding"`
}
```

`AdmissionSHA256` is SHA256 of the exact canonical journal admission bytes; the caller also validates the full immutable manifest and `managedJournalIdentity(manifest)`. RuntimeConfigSHA256 binds the already-verified artifact supplied by the trusted reader. Version is 1; phases are exactly `prepared`, `dispatching`, `acknowledged`. The volume and host handles are internally generated random 32-hex ID/nonce pairs, same current ownership generation. Return cloned maps so callers cannot mutate retained record data.

| Transition | Preconditions and meaning |
|---|---|
| Reserve -> prepared | Live current running generation, no live cleanup, immutable operation deadline within original work Deadline. One H token, two scopes and one canonical intent commit together. No Docker request has been invoked. |
| prepared -> dispatching | Exact full handles, same generation/config digest/deadline, both scopes reserved, create host PID/PGID zero, current work authority. One transactional CAS winner. Only that winner may perform the fixed create Invoke once. |
| dispatching -> acknowledged | Adapter's own Invoke returned nil error, ExitCode0, stdout exactly `volumeName+"\n"`, empty stderr, bounded output, still-live operation authority, and create-host scope is positively quiescent. No external API accepts HostResult to make this transition. |
| bind CreatedAt | A trusted inspection of a dispatching or acknowledged intent returns every immutable binding field unchanged and canonical nonzero UTC CreatedAt. Empty -> exact value once; identical replay is read-only; replacement fails. Binding does not acknowledge create or retire a scope. |
| retire prepared after owner loss | A newer live recovery generation has revoked the old owner and positively retired **all prior-generation non-Docker scopes**, including CreateHost. In one transaction require Phase=prepared and exact old scopes before marking only the unused volume scope quiescent. The same transaction/epoch checks prevent a late old-owner dispatch. No Docker call, token refund, row deletion or deadline extension. |

An ordinary current-generation prepared record can be loaded and dispatched by the adapter with its **same** CreateHost and unchanged CreateDeadline; no format migration or new reservation is needed. A new ownership generation cannot reuse an old host identity: it can positively retire a never-dispatched prepared intent using the proof above, then use remaining ordinary budgets for new work. That is existing generation isolation, not a v1 migration penalty. An expired CreateDeadline is never refreshed.

Every failure after the dispatch CAS remains dispatching/uncertain, including cancellation before actual launch, nonzero/ambiguous output, runtime retirement failure, or a crash between successful Invoke and acknowledgment commit. No create retry from dispatching. An acknowledged create whose inspection fails remains acknowledged with an unknown lifetime. Missing/corrupt database state is never equivalent to prepared.

## Exact identity and accounting

- Volume name is `oro-factory-<objectiveHash16>-volume-<ID32>-<Nonce32>`; objectiveHash is SHA256(canonical state root + NUL + objective ID), first eight bytes in hex. Host name is the existing `dev.getoro.factory.<objectiveHash16>.host.<ID>.<Nonce>`.
- Binding has Kind=volume, admitted DockerContext, exact name, local driver, empty container/image fields, and exactly six `dev.getoro.factory.{objective,scope,nonce,generation,kind,policy}` labels. Policy is the admission ManifestHash. Derived options are exactly `type=tmpfs`, `device=tmpfs`, and `o=size=<SourceBytes>,nr_inodes=<SourceInodes>,uid=1000,gid=1000` in that literal order. No caller names, flags, labels or options.
- Separate `managed_docker_intents` table: `volume_scope_id` primary key, unique `create_scope_id`, canonical body <=16384 bytes. Validate both complete referenced scope records, not only SQL IDs/FKs. Phase and binding are in the canonical body; mutations compare the previously read exact record inside the serialized transaction.
- One retained intent per volume. Count **all** rows against `ManagedHostRetention(manifest.Spec).SourceVersions`; never delete/recycle a row to refund it. Both scopes count against existing global ScopeRecords. Reservation with room for only one scope rolls back H and both inserts. No silent cap increase.
- Charge the <=16KiB intent within that volume scope's existing 1MiB ScopeMetadataBytes; generic scope body stays <=2KiB. Existing database/state ceilings and runtime artifact final/staging allowances remain unchanged.
- Normal create+inspect uses **two H tokens and three scopes**. At most ten ordinary events: atomic prepare1, dispatch1, create host grant/retire2, acknowledgment1, inspect H/scope reservations2, inspect grant/retire2, lifetime bind1. Event attribution stays below eight per scope; eventual volume retirement adds one. Prepared-only retirement is another single transition on its alternative path. Idempotent phase/lifetime reads add none. Existing generation/recovery events and all total caps still apply.
- The global scope cap can exhaust before independent H or SourceVersions maxima. Treat that as bounded admission exhaustion, not a reason to borrow records. Any guaranteed full-workload capacity change needs its own checked bound review.
- Initialize `managed_docker_intents` only after positively establishing that no committed singleton `managed_admissions` row exists. Perform this admission-row check and conditional feature-table DDL under the existing objective lock with a serialized schema transaction; query/schema errors fail closed and never count as an absent admission. Do not append this table to `initManagedSchema`'s unconditional `CREATE TABLE IF NOT EXISTS` batch. If an admission row is already committed, identical admission replay must neither create an absent intent table nor recreate a removed one. Previously admitted objectives lacking the table deny new intent operations without DDL/reset while existing metadata/admission reads remain compatible. Unknown record versions or removed tables fail closed. No automatic migration, history reconstruction or capacity refund.

## Child A — Persist usable Docker volume intent (7 minutes)

**Files:** new `pkg/factory/managed_docker_volume.go`; optional new `pkg/factory/managed_docker_volume_state.go` for transaction/codec factoring; minimal explicitly guarded new-admission DDL in `pkg/factory/managed_store.go`; one new `pkg/factory/managed_docker_volume_test.go`.

**Read:** `managed_store.go:{initManagedSchema,mutate,reserveManagedToken,checkScopeAdmission,insertScope,loadManagedScope,activeManagedGeneration,activeManagedRecovery}`; `managed_cleanup.go:{requireNoManagedCleanup,cleanupObjectiveHash,validateCleanupLabels}`; `managed_admission.go:{AdmitManaged,finishManagedAdmission,commitManagedAdmission,managedJournalIdentity,validateManagedManifest}`; existing identical-admission replay tests; `managed_host_limits.go:ManagedHostRetention`.

**Signatures:**

```go
func reserveManagedDockerVolume(ctx context.Context, manifest managedManifest,
    journal *managedJournal, generation uint64, createDeadline time.Time,
    runtimeConfigSHA256 string) (managedDockerVolumeIntent, error)
func loadManagedDockerVolume(ctx context.Context, journal *managedJournal,
    volume ScopeHandle) (managedDockerVolumeIntent, error)
func dispatchManagedDockerVolume(ctx context.Context, journal *managedJournal,
    volume ScopeHandle, runtimeConfigSHA256 string) (managedDockerVolumeIntent, error)
func acknowledgeManagedDockerVolume(ctx context.Context, journal *managedJournal,
    volume, createHost ScopeHandle) error
func bindManagedDockerVolume(ctx context.Context, journal *managedJournal,
    ownerGeneration uint64, volume ScopeHandle, observed DockerCleanupBinding) error
func retirePreparedManagedDockerVolume(ctx context.Context, journal *managedJournal,
    ownerGeneration uint64, volume ScopeHandle) error
```

The acknowledgment helper is package-private and consumes no output. Its sole production caller in child B is immediately below the successful exact Invoke checks; it is not an external result-ingestion API. Atomicity, full handles and phase invariants still apply. `dispatch` returns an intent only to the CAS winner; replay is an error, not reusable dispatch permission. `acknowledge`/`bind` exact completed replays return without a write; no expired/stale call may cause a new transition. Read/load accepts exact old handles without granting them. Bind may use the live current recovery generation for an older resource before original HardStop.

**Test:** `pkg/factory/managed_docker_volume_test.go:TestManagedDockerVolumeIntent`.

**Cmd:** `uv run --no-project --python /Users/as21/codehouse/oro/.venv/bin/python /private/tmp/oro-factory-verify-ldhnhnvl/verify_autonomy.py docker-volume-state go test -race ./pkg/factory -run '^TestManagedDockerVolumeIntent$' -count=1 -timeout=180s`.

**Assert/RED:** exact atomic delta H=1/scopes=2/intents=1/events=1; forced final-insert failure and one-scope-left exhaustion leave delta zero; all derived fields/options exact; two independent connections racing dispatch yield one winner; close/reopen prepared remains dispatchable under the same live generation/deadline; close/reopen dispatching cannot redispatch; only quiescent exact CreateHost can acknowledge; no HostResult parameter; exact lifetime replay adds no event; changed nonce/CreatedAt/options/labels/policy/config digest fails. Process-death fixtures cover committed prepared versus committed dispatching; a new generation with old owner still live cannot retire prepared, but after all old non-Docker scopes positively retire it can. Dispatching cannot use this no-effect retirement proof. Budget/SourceVersions/byte/event caps, canceled/expired/stale/stopped/revoked authority, live cleanup exclusion, malformed/noncanonical JSON and missing/unknown schema all fail without reset or partial commit.

**Admission replay regressions in the same test:** a genuinely new, uncommitted admission initializes the intent table and can reserve v1. A valid old committed objective without that table must survive repeated identical `AdmitManaged` calls with the table still absent, byte-identical committed admission/snapshot identity and unchanged supervision counters/revision, reservations, scopes and events; intent operations continue to deny. For a used v1 objective, first consume H/source capacity and advance its intent, then remove only the feature table in the test fixture and repeat identical `AdmitManaged`: the table must remain absent, all retained identity/counters/reservations/scopes/events must remain unchanged, and intent APIs must deny instead of resetting SourceVersions history or creating fresh capacity. Exercise the public admission replay path so a test cannot bypass the unconditional `finishManagedAdmission` -> `initManagedSchema` call. A failed admission-row query/schema check must not create the table or alter state.

**Dependency:** existing journal/admission/cleanup contracts. This child has a direct consumer in B; records are executable v1 from their creation.

## Child B — Execute owned volume creation (7 minutes)

**Files:** new `pkg/factory/managed_docker.go` (adapter); new `pkg/factory/managed_docker_volume_inspect.go` (narrow result decoder); new `pkg/factory/managed_docker_config.go` (DTO and core identity validation); one new `pkg/factory/managed_docker_test.go`.

**Read:** A's APIs; `source_git.go:{newManagedGitRepository,operation,invoke}`; `managed_host.go:{reserveManagedHostInvocation,validateManagedHostRequest}`; `managed_admission_view.go:verifyManagedAdmissionIdentity`; `managed_contract.go:HostRuntime`; `docker_identity.go:CanonicalDockerCreatedAt`; cmd cleanup inspection parser as a bounded parsing reference only.

**Shared producer/consumer types:**

```go
type ManagedDockerConfig struct {
    StateRoot, ObjectiveID, PolicyHash string
    Deadline, HardStop                time.Time
    RuntimeConfigPath, RuntimeConfigSHA256 string
    DockerPath, DockerSHA256, DockerConfig string
    DockerContext, DockerEndpoint     string
}
type ManagedDockerConfigReader func(context.Context) (ManagedDockerConfig, error)

func newManagedDockerVolumes(ctx context.Context, manifest managedManifest,
    generation uint64, journal *managedJournal, host HostRuntime,
    readConfig ManagedDockerConfigReader) (*managedDockerVolumes, error)
func (d *managedDockerVolumes) createVolume(ctx context.Context) (managedDockerVolumeIntent, error)
func (d *managedDockerVolumes) dispatchPrepared(ctx context.Context, volume ScopeHandle) (managedDockerVolumeIntent, error)
func (d *managedDockerVolumes) inspectVolume(ctx context.Context, volume ScopeHandle) (managedDockerVolumeIntent, bool, error)
func parseManagedDockerVolumeInspection(result HostResult,
    expected DockerCleanupBinding) (DockerCleanupBinding, bool, error)
```

The bool means **present**, never quiescent. The constructor validates complete manifest, committed admission/snapshot identity, host retention, generation shape, nonnil trusted dependencies and the first config view. It saves the complete comparable config; the reader must return exactly that view before every invocation. Core checks root/objective/policy/context/deadlines against manifest; exact derived runtime path `<root>/<id>/runtime/runtime.json`, config directory `<root>/<id>/runtime/docker`, canonical path/hash shapes and dedicated Unix endpoint. The CLI reader owns filesystem/program verification. No ambient path lookup or exec.Command is added to core.

`createVolume` holds one private operation lock, derives a single deadline=min(parent deadline, now+StepTimeout, original work Deadline), reads verified config, then calls A's atomic reservation. It delegates to the same private prepared-dispatch implementation used by `dispatchPrepared`; do not nest locks. Build and validate the fixed HostInvocation before the CAS. After CAS, invoke **that exact value once** via injected HostRuntime, check output/deadline and nil error, then call A's private acknowledgment helper. It immediately inspects using a fresh counted ordinary H scope and the same operation deadline. A missing observed volume is an error for createVolume and never marks it quiescent. Return only the acknowledged, exactly bound record on complete success.

`dispatchPrepared` reloads a usable v1 record, requires current generation plus unchanged runtime digest and original CreateDeadline, and performs the same single call path. It does not reserve a new H token or extend the deadline. It never dispatches an already-dispatching/acknowledged record. `inspectVolume` can later allocate a fresh bounded inspect operation under remaining ordinary H/work budgets; it cannot refresh the old create deadline or acknowledgment. Runtime uncertainty freezes further calls on that adapter, as managedGitRepository already does, until the controller's reconciliation path takes over.

**Exact create command:** Path=verified original DockerPath; Args=`--config <DockerConfig> --context <admittedContext> volume create --driver local`, then the six sorted literal `--label key=value` pairs, then `--opt type=tmpfs --opt device=tmpfs --opt <derived-o> <exactName>`. Env exactly `PATH=/usr/bin:/bin`, `LC_ALL=C`, `LANG=C`; Dir=canonical StateRoot; Stdin=nil; OutputLimit=1MiB; Scope=private CreateHost; Deadline=immutable operation deadline. The ordinary inspect command is only `--config ... --context ... volume inspect <exactName>` with its own counted H scope. Both pass validateManagedHostRequest. No caller-selectable command/context/options, `create` retry, direct host exec or cleanup-kind scope.

**Inspection contract:** stdout+stderr <=1MiB, stderr<=4096; success Exit0/empty stderr, one JSON object in one array, duplicate/excessive keys rejected (<=128 object fields), required non-null Name/CreatedAt/Driver/Labels/Options, bounded string maps (core cleanup cap16/128-byte keys/1024-byte values). Normalize only raw daemon CreatedAt through CanonicalDockerCreatedAt, then compare the complete binding including all six labels and exact options. Bind only an initially unknown CreatedAt or require its existing exact value. Other daemon fields may be ignored after bounded duplicate-key parsing. Exact native not-found form Exit1/stdout `[]\n`/stderr `Error response from daemon: get <name>: no such volume\n` returns unchanged binding and present=false; all other diagnostics fail. No absence result calls MarkQuiescent.

**Test:** `pkg/factory/managed_docker_test.go:TestManagedDockerVolumeCreate`.

**Cmd:** `uv run --no-project --python /Users/as21/codehouse/oro/.venv/bin/python /private/tmp/oro-factory-verify-ldhnhnvl/verify_autonomy.py docker-volume-adapter go test -race ./pkg/factory -run '^TestManagedDockerVolumeCreate$' -count=1 -timeout=180s`.

**Assert/RED:** recording trusted HostRuntime verifies the exact fixed create call sees committed dispatching before entry and the exact volume/H identity; it records proper host quiescence and returns the expected name, then the inspect fixture returns a raw `-04:00` timestamp which becomes canonical UTC. Final state=acknowledged/bound, H=2/scopes=3, original operation deadline shared. No freeform command/result method exists. Altered config/digest fails before dispatch; nonzero/extra/partial stdout, stderr, oversized output, late/canceled return, runtime error, nonquiescent host or failed acknowledgment commit leave dispatching and cannot trigger another create. A successful create followed by inspect exhaustion/absence/identity mismatch remains acknowledged but unreleased. A directly reopened prepared v1 dispatches once without new H; stale/expired or concurrently consumed records never invoke. Exact CreatedAt/labels/options mismatches, duplicates, malformed/overlong offsets and negative absence variants fail. Tests inject a trusted recording runtime; they do not claim actual launchd/Docker integration.

**Dependency:** A. This child is the real live-call consumer of A; no durable PUBLISHED/second receipt subsystem.

## Child C — Supply verified Docker runtime inputs (5 minutes)

**Files:** new `cmd/oro/cmd_factory_docker_storage_config.go`; one new `cmd/oro/cmd_factory_docker_storage_config_test.go`. No changes to launchd, host-exec, runtime artifact codec/reader or authority.

**Read:** `cmd_factory_runtime_config_read.go:readFactoryRuntimeConfig`; `cmd_factory_runtime_config.go:{factoryRuntimeConfig,factoryDockerCleanupConfig}`; `cmd_factory_launchd_cleanup.go:validateFactoryCleanupInvocation` for existing exact view comparison; `pkg/factory/managed_admission_view.go:{ManagedAdmissionView,ReadManagedAdmission}`; B's ManagedDockerConfigReader; runtime-config test fixture.

**Signature:** `func newFactoryDockerConfigReader(view factory.ManagedAdmissionView, runtimePath, runtimeSHA256 string) factory.ManagedDockerConfigReader`.

Return a callback that reads the exact immutable artifact using `readFactoryRuntimeConfig(ctx, runtimePath, runtimeSHA256)` on every call; compares StateRoot, ObjectiveID, PolicyHash, original Deadline/HardStop, cleanup timeout, Git path/hash, repository/ref and DockerContext to the supplied **previously verified immutable view**; and returns B's scalar DTO with the verified Docker identities/config directory. The core separately verifies current committed journal/snapshot identity and work authority. The callback does not claim a bare view is a new grant or initialize/refresh admission. Caller integration obtains the view through ReadManagedAdmission; artifact publication and controller construction remain CLI-start's existing responsibilities. There is no alternative path accepting ambient Docker configuration.

**Test:** `cmd/oro/cmd_factory_docker_storage_config_test.go:TestFactoryDockerStorageConfig`.

**Cmd:** `uv run --no-project --python /Users/as21/codehouse/oro/.venv/bin/python /private/tmp/oro-factory-verify-ldhnhnvl/verify_autonomy.py docker-volume-config go test -race ./cmd/oro -run '^TestFactoryDockerStorageConfig$' -count=1 -timeout=180s`.

**Assert/RED:** existing private runtime fixture produces exactly B's DTO; trusted-view mismatch in each field returns a zero DTO/error; changing artifact bytes, program hash, private snapshot or endpoint after the first read is rejected by a subsequent callback; caller cancellation propagates; reads cause no state/event/grant/artifact writes. Work-deadline expiry alone does not invalidate this immutable reader (as existing cleanup semantics require); B's ordinary work gates still deny execution after work Deadline. The test supplies a trusted-view fixture and does not invent a committed-admission proof for it.

**Dependency:** B for its consumed DTO/callback contract. This is the real filesystem-verifying producer, not a fake implementation or new authority interface. Public activation remains gated until the controller/CLI-start consumers exist.

## Remaining integration and recovery work

1. Controller/CLI-start: freeze/publish the existing bounded runtime artifact, obtain the verified admission view, instantiate its real callback and trusted HostRuntime, then construct the private volume adapter from the controller's manifest/journal/current generation. The storage consumer must propagate adapter uncertainty to reconciliation, not instantiate a replacement adapter to bypass it.
2. Extend usable intent records to exact keeper/container create/start lifetimes without silently reinterpreting volume v1; native readiness/image/guard and continuous keeper ownership remain separate. Seed only fresh owned volumes using the accepted complete-vector regular-file/symlink codec; never reseed an old volume or copy with -a/-L. Candidate stop before export and keeper continuity remain required.
3. Routine retirement uses counted H while the live controller retains work authority. Supervisor C cleanup starts only after all non-Docker scopes retire and consumes the exact typed cleanup helper result. Prepared-no-dispatch retirement is the narrow metadata proof above; it must not be generalized to dispatching/acknowledged resources.
4. Unknown post-dispatch create/start remains safe uncertainty. Actual request termination/recovery requires the separately reviewed owned-VM fence/provider mechanism; none of these children equates absence, client retirement or binding with daemon drain. Full autonomous factory/storage qualification cannot close with permanent quarantine as its normal recovery design.
5. Native integration must still exercise the actual trusted host adapter, volume create/inspect, keeper seed/stopped export, exact cleanup and the next objective after capacity release, including controller death/lost acknowledgments. The newly accepted five-entry Docker symlink capture is format evidence, not this qualification.

## Rule-of-Five audit

- **A:** exact record/API and three phases; concrete transaction deltas/crash points; bounded journal symbols; one transaction owns H/two scopes/intent; stale owner and missing-schema paths cannot fabricate no-effect proof. Explicit committed-admission guard plus both public admission replay regressions prevent feature-table recreation, silent migration and SourceVersions history reset.
- **B:** exact config/read/adapter/parser signatures and argv; positive two-call/three-scope result plus negative outputs; existing trusted Invoke contract read; real live-call caller owns acknowledgment; no caller-selected commands, runtime substitution or create replay.
- **C:** exact immutable view-to-DTO callback; each mismatch and reread tested; existing reader owns filesystem checks; B is the named consumer and CLI-start the remaining constructor; no artifact-only grant or fallback.

Each child has one acceptance test file/function and <=3 production files, including its actual seam consumer/producer. The parent docker-storage task remains open until its integrated and recovery requirements pass. No tracker/repository/Git changes or native operations were performed for this proposal.
