# Managed source keeper implementation plan

> For Codex: use executing-plans and test-driven-development. Tracked task: `oro-mf-source-keeper-create`. Root and independent contract review accepted this stopped-keeper slice. Start, seed, native qualification and recovery remain required.

Goal: create an owned stopped keeper on an exactly bound source volume.
Architecture: consume the accepted volume adapter through journaled one-shot keeper creation and inspection. READY, seeding, export and controller wiring follow as explicit consumers.
Tech stack: existing Go journal, trusted HostRuntime and fixed Docker commands.

Date: 2026-10-24. Based on accepted A/B/C; C is `28c1f12f`.

## Decision

Implement one real next operation: **create a stopped source keeper**. Its production call chain consumes the accepted volume adapter, creates the exact stopped guard container on that volume, and durably binds its returned ID and inspected lifetime. It does not pretend that stopped creation preserves tmpfs contents: the following READY/seed step must start the keeper before any source copy.

This is the smallest useful integration increment; doing keeper start, seeding, candidate execution, export, controller extraction and CLI publication in the same task would hide several independently reviewable failure boundaries. No new publication receipts, generic command API or alternate host executor are needed.

## What exists and where the real caller belongs

`engine.go:controller.attempt/check` still uses concrete legacy `gitRepository`, host worktrees and `executeCommand`. `cmd_factory.go:factory run` still calls legacy `factory.Run`. There is no managed controller or execution backend to attach storage to today. Calling Docker from that legacy path would bypass the intended managed delivery boundary.

The accepted production leaves are `managedDockerVolumes.createVolume/inspectVolume`, `managedGitRepository.export/commit`, the archive codecs, and the trusted CLI configuration reader. The immediate operation below has a genuine production consumer relationship with `createVolume`; it is not activated by a fake CLI or dummy constructor call. Its next consumer is `prepareSource`, followed by the real `RunManagedController`/private delivery backend under existing `oro-mf-managed-delivery` and `oro-mf-cli-start`. Those top-level tasks remain open.

## Next executable child: Create stopped source keepers

Tracked ID: `oro-mf-source-keeper-create`, child of storage. One acceptance function and one test file; five production files including the minimal schema hook and shared guard extraction. The parent retains its complete native and recovery acceptance criteria.

Files:

- New `pkg/factory/execution_docker_storage.go`: production `createSourceKeeper` orchestration and fixed create/inspect calls.
- New `pkg/factory/managed_docker_keeper.go`: bounded keeper record, atomic reservations, CAS and own-call acknowledgment/lifetime binding.
- New `pkg/factory/managed_docker_keeper_inspect.go`: bounded stopped-container parser and exact creation/mount/security checks.
- Minimal `pkg/factory/managed_store.go` hook for the separately guarded keeper table initializer.
- Narrow `pkg/factory/managed_docker_volume_state.go` extraction of the existing guarded schema transaction, shared only by the fixed volume and keeper initializers. Preserve volume v1 and its missing-table replay behavior.
- New `pkg/factory/execution_docker_storage_test.go:TestManagedSourceKeeperCreate`.

Read: `managed_docker.go:{createVolume,operation,ready,invoke}`, `managed_docker_volume.go` and `managed_docker_volume_state.go` for the accepted transaction/identity rules, `managed_store.go:{mutate,checkScopeAdmission,insertScope,loadManagedScope}`, `managed_host.go:reserveManagedHostInvocation`, `managed_host_limits.go:ManagedHostRetention`, `cmd_factory_docker_ready_inspect.go` for guard/keeper checks, and design sections on source storage/guard image.

Concrete interfaces:

```go
type managedDockerKeeperIntent struct {
    Version int
    AdmissionSHA256, RuntimeConfigSHA256 string
    Volume, Keeper, CreateHost ScopeHandle
    WorkDeadline, CreateDeadline, GuardDeadline time.Time
    Phase string // prepared, dispatching, acknowledged
    Binding DockerCleanupBinding
}

func (d *managedDockerVolumes) createSourceKeeper(ctx context.Context) (managedDockerKeeperIntent, error)
func (d *managedDockerVolumes) createKeeperForVolume(ctx context.Context, volume ScopeHandle) (managedDockerKeeperIntent, error)
func (d *managedDockerVolumes) dispatchPreparedKeeper(ctx context.Context, keeper ScopeHandle) (managedDockerKeeperIntent, error)
func reserveManagedDockerKeeper(ctx context.Context, manifest managedManifest,
    journal *managedJournal, generation uint64, volume ScopeHandle,
    createDeadline time.Time, runtimeConfigSHA256 string) (managedDockerKeeperIntent, error)
func loadManagedDockerKeeper(ctx context.Context, journal *managedJournal,
    keeper ScopeHandle) (managedDockerKeeperIntent, error)
func dispatchManagedDockerKeeper(ctx context.Context, journal *managedJournal,
    keeper ScopeHandle, runtimeConfigSHA256 string) (managedDockerKeeperIntent, error)
func acknowledgeManagedDockerKeeper(ctx context.Context, journal *managedJournal,
    keeper, createHost ScopeHandle, returnedID string) error
func bindManagedDockerKeeper(ctx context.Context, journal *managedJournal,
    ownerGeneration uint64, keeper ScopeHandle, observed DockerCleanupBinding) error
func parseManagedDockerKeeperInspection(result HostResult,
    expected managedDockerKeeperIntent, volume managedDockerVolumeIntent) (DockerCleanupBinding, error)
```

All record fields use explicit snake_case JSON tags, canonical bytes and complete validation. Acknowledgment remains private, with its sole production caller immediately after the adapter's own successful exact Invoke; it does not accept externally supplied HostResult. `returnedID` is only that call's validated full ID. Reuse existing complete scope-record decoding; do not trust redundant columns or unmarshal lifecycle overrides.

Behavior:

1. Derive one operation context bounded by parent, StepTimeout and original work Deadline. Reject non-config-ID image identities before resource effects: this storage path requires `sha256:<64>` for the **derived guard image**, as the design already specifies. Never treat the pinned golang base or a tag as that image. Recheck verified runtime config normally.
2. `createSourceKeeper` calls `createVolume` with that bounded context, then `createKeeperForVolume` on its exact returned volume. These existing/new methods take their own adapter locks; do not hold the lock across a call to the locking `createVolume`. Preserve the same adapter failure state. A failure after volume creation leaves that durable resource allocated; no rollback-by-absence or new adapter bypass.
3. Reload the full acknowledged, lifetime-bound volume in the keeper reservation transaction. Require current ownership, exact context/config and no previous keeper for that volume. Atomically reserve one H, its create-host scope, one keeper scope and the intent. Keeper scope and guard deadline are original HardStop; create-host deadline is the immutable operation deadline at or before work Deadline. Persist the volume reference and all derivable expected configuration before any Docker effect.
4. Commit the ordinary `prepared -> dispatching` CAS and invoke one fixed Docker create. Verify nil runtime error, ExitCode0, empty stderr, stdout exactly full64ID plus newline, original live authority and positively retired create-host scope. Only then record acknowledged plus that ID. Any lost/nonzero/late/ambiguous result remains dispatching; never replay create. An observed ID never substitutes for acknowledgment. `dispatchPreparedKeeper` reloads the exact existing prepared record, applies its original CreateDeadline before reading config, and uses that same reserved host once; it never refreshes a deadline or reserves another H.
5. A separate counted H performs `container inspect <fullID>` under the same deadline. Bind canonical CreatedAt only after complete exact lifetime/config/mount verification. Return only acknowledged, bound, never-started keeper. Inspection failure leaves the create acknowledged and capacity retained. There is no start, seed, readiness claim or cleanup side effect in this child.

Fixed command: supplied verified Docker path; explicit `--config <privateDir> --context <admitted>`; `container create --name <derivedKeeperName> --pull=never`, six exact sorted ownership labels, network none, read-only rootfs, cap-drop ALL, no added caps, no privilege, no-new-privileges, default seccomp, private IPC/cgroup/PID namespace, restart no, log none, memory/swap 64MiB, CPU0.25, PID16, shm1MiB, user `65534:65534`, entrypoint `/usr/local/bin/oro-factory-guard`, one `--mount type=volume,source=<exactName>,target=/source,volume-nocopy`, exact admitted derived image ID, then `--deadline <originalHardStop>`. Do not set TTY/open stdin or add host binds. Environment/cwd/output follow the accepted Docker adapter. Inspector compares all labels, ID/name/CreatedAt/image/guard/config and the exact one RW local source mount; State must be created, not running, PID0 and zero StartedAt/FinishedAt. Bound combined output to1MiB/stderr4096 and reject duplicate/ambiguous JSON as the volume inspector does.

Accounting/schema:

- Separate `managed_docker_keepers` table, canonical body <=16384 bytes, keeper ID primary key, unique volume ID and create-host ID. Volume v1 bytes and meaning remain unchanged.
- Exactly one retained keeper per retained source-volume intent, therefore <=SourceVersions; all retained records count, with no deletion/refund. Record bytes fit the existing keeper scope's1MiB metadata allowance. Global scope/event/H caps remain unchanged.
- Keeper create+inspect adds H2, scopes3 and at most10 events, matching the volume lifecycle structure. Complete volume+stopped-keeper path is H4/scopes6/at most20 events before later retirement. Enforce rollback when only one of the keeper transaction's two scope slots fits.
- Initialize only for genuinely uncommitted objectives using the same admission-plus-history guard. Existing committed objectives with no keeper table deny keeper operations; repeated admission never recreates a removed used table. Metadata compatibility remains intact. No silent migration or reset.

Test/command:

```sh
uv run --no-project --python /Users/as21/codehouse/oro/.venv/bin/python \
  /private/tmp/oro-factory-verify-ldhnhnvl/verify_autonomy.py source-keeper-create \
  go test -race ./pkg/factory -run '^TestManagedSourceKeeperCreate$' -count=1 -timeout=180s
```

Behavioral assertions: recording trusted runtime sees exactly volume create/inspect then keeper create/inspect, with durable dispatch before each create and exact full argv parsed independently; final counters H4/scopes6 and exact bound volume+keeper; guard uses unchanged HardStop while all H operations share original operation deadline. Test wrong image, mount/labels/limits/guard/UID/state, malformed/late/nonzero create and inspect, exhausted H/scopes, config drift, canceled/expired authority, duplicate keeper and persisted record corruption. Process-death/reopen after dispatch cannot create again; a prepared keeper can use its same reserved host/deadline once. Unknown outcomes never mark either Docker scope quiescent. Tests are recording-runtime proof, not native storage qualification.

## Immediate downstream consumer and remaining narrow work

The next storage operation is `prepareSource(ctx, entries []sourceEntry) (managedDockerSource, error)` in `execution_docker_storage.go`, with `managedDockerSource` holding the exact volume/keeper handles and persisted readiness identity. It first calls `encodeDockerSourceArchive` on the entire vector before resource effects, then calls `createSourceKeeper`, starts that exact ID through the already accepted frozen-Oro READY helper, post-inspects exact running identity/StartedAt and mount, and seeds once with fixed `docker cp - <ID>:/source` without `-a` or `-L`. A start dispatch and a seed dispatch must be durably distinguishable from their own successful acknowledgment; reuse the same one-shot pattern, not a new receipt system. No reseed after a partial/unknown copy. Keeper continuity begins at the acknowledged start and must survive every later source use.

Concrete producer gap for that next child: `ManagedDockerConfig` currently omits OroPath/OroSHA256, while the verified runtime artifact already supplies both. Add those two fields to the DTO and the existing real CLI producer together when wiring READY; do not discover Oro via PATH, derive a mutable path or add a fourth executable copy. All helper args derive from immutable manifest/config/keeper intent.

Trusted export belongs to `exportStoppedSource(ctx, source managedDockerSource, candidate ScopeHandle) ([]sourceEntry, error)`: the container scope consumer must provide a journaled candidate attached to that exact source; storage itself verifies positively stopped candidate identity and continuous keeper identity/StartedAt before/after fixed bounded `docker cp <keeperID>:/source/. -`, then calls `decodeDockerSourceArchive`. A caller bool such as `stopped=true` is not authority. Candidate scope creation/stop is the existing `oro-mf-docker-scope` work. Avoid a dependency cycle by finishing storage's fresh-volume/keeper/seed API first, then candidate stop/export integration; the original broad storage qualification can remain an epic depending on both.

Production managed delivery then performs `managedGitRepository.export(base) -> prepareSource -> candidate execution/positive stop -> exportStoppedSource -> managedGitRepository.commit(base, entries)`. Acceptance/review call `prepareSource` separately with read-only candidate mounts and independent scratch. The private delivery-backend extraction in existing backend-seam/managed-delivery tasks preserves legacy attempt/check/feedback/promotion behavior. CLI-start publishes the existing runtime artifact, reads the verified admission view, creates the real config callback/HostRuntime and passes them to that managed controller. No public activation claim until those callers land.

## Recovery and qualification boundary

The owned VM fence **does not block the fully acknowledged live happy path**: exact creates/starts/copies return successfully, current authority persists and inspections prove the intended objects/continuous keeper. It **does block autonomous recovery from unknown post-dispatch daemon requests**, including create/start/copy, and consequently full storage/factory qualification. Client retirement, notfound, binding and a newly constructed adapter cannot prove daemon drain. The eventual epoch fence must positively retire the dedicated VM/provider epoch across every sharing objective before reconciling old intents and releasing capacity.

Routine retirement while a live controller has work authority uses counted H. Supervisor C cleanup starts only after required prior non-Docker retirement and consumes typed helper output with ExitCode0; neither output absence nor mutation acknowledgment alone is a quiescence proof for an unknown earlier request. The stopped-keeper child retains uncertainty rather than solving or bypassing this separate recovery requirement.

Native follow-up must use an actual derived guard image (base/guard/result digests recorded), actual trusted HostRuntime, continuous keeper mounting, fresh volume seed, stopped export, exact cleanup and a subsequent healthy objective. Prior links capture used a /bin/sleep holder on the base image; it proves archive transport, not this guard/source lifecycle. No new native actions are requested by this proposal.
