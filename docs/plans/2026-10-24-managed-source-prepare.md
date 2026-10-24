# Managed source preparation implementation plan

Date: 2026-10-24. Tracked task `oro-mf-source-prepare`; root owns integration. Root and independent contract review accepted this implementation plan. This plan consumes committed stopped-keeper creation and verified Oro identity producer/consumer commit `b0d816d2`. Native qualification remains separate.

## Ownership decision, supported by pinned code and actual evidence

Retain the fixed command `docker --config <privateDir> --context <admitted> cp - <fullKeeperID>:/source`, with the production encoder's USTAR stdin and **without -a or -L**. This is specifically a trusted stdin-archive policy, not a claim that ordinary host-path copying preserves ownership.

The generic [Docker cp documentation](https://docs.docker.com/reference/cli/docker/container/cp/) says copies into containers are root-owned by default, while separately documenting stdin archive input. That summary alone is insufficient to choose the flags for this archive path.

In [Docker CLI v29.9.2 cp.go](https://github.com/docker/cli/blob/v29.9.2/cli/command/container/cp.go#L381-L436), stdin is passed directly as content rather than rebuilt from host paths; CopyUIDGID is set from the archive flag. The no-terminal path suppresses progress output. We supply a complete validated archive, not candidate-selected bytes or CLI options.

In [Moby docker-v29.5.2 archive_unix.go](https://github.com/moby/moby/blob/docker-v29.5.2/daemon/archive_unix.go#L134-L145), extraction uses default options unless CopyUIDGID is true. [Default options](https://github.com/moby/moby/blob/docker-v29.5.2/daemon/archive_tarcopyoptions.go#L8-L12) apply the daemon identity mapping but no ownership override. [The archive-flag override](https://github.com/moby/moby/blob/docker-v29.5.2/daemon/archive_tarcopyoptions_unix.go#L16-L28) sets ChownOpts from the container's Config.User. Here that is 65534:65534. [go-archive v0.2.0 extraction](https://github.com/moby/go-archive/blob/v0.2.0/archive.go#L464-L475), the dependency pinned by this daemon, uses each header's UID/GID when ChownOpts is absent. Therefore the raw UID/GID1000 headers survive the no-a path; enabling -a would instead select UID/GID65534 in this configuration.

Actual existing evidence corroborates that code path:

- `/tmp/oro-mf-storage-probe.x1Szg9/probe.go:104` uses `cp - keeper:/source`; lines105-106 run a separate writer with `exec --user 1000:1000`, assert file owner1000:1000, overwrite the seeded0644 short.txt and execute the seeded0755 run.sh. It uses the derived guard image and two guard containers sharing the tmpfs. No root exec is used.
- `probe-without-archive-flag.log` records `1000:1000 644 /source/short.txt`, `1000:1000 755 /source/run.sh`, executable success and exported short.txt content `edited`. It also records the implicit0755 directory as UID/GID1000 and identical export after positively stopping the writer.
- Earlier `probe.log` records the archive-flag experiment's copied files and implicit directory as UID/GID65534, consistent with the override above. The retained probe.go is the later no-a variant; do not present it as the exact source snapshot of the earlier experiment.
- The accepted production-codec native capture `/private/tmp/oro-factory-verify-ldhnhnvl/docker-links-native-301230466/headers.json` shows every exported regular file,0755 implicit directory and symlink as UID/GID1000. `source_docker_links_native_test.go:roundTrip` uses the production encoder and exact no-a stdin command, and `inspectDockerLinksNativeTar` asserts every header's ownership. This holder was /bin/sleep on the base image, so it is transport evidence, not guard lifecycle qualification.

Evidence SHA256:

| Artifact | SHA256 |
|---|---|
| probe.go | de3bfd9c5d0f2109e2163ea05e262746d9ca945a8c139bbb2c489b5746a370ee |
| probe-without-archive-flag.log | dd459c21d1dfdfca8bc744af5cbd974be762afa52b7a0b50665d5b0b1f1195ec |
| probe.log | 59025e09a484db92c6fd1fdb332eb31583ebb975ac6d318a6ae9130062b6c3d7 |
| production native headers.json | 9f5eaa25008f6c8ca3bd394e7537d0901a3d1ffd50407ace4fec9d97a2f147b3 |

This resolves the command-selection blocker for the recorded client29.9.2/server29.5.2 backend. It does not establish all future Docker versions or daemon mappings. Native qualification of the new production preparation path must explicitly stat the source root, a copied0644 file, copied0755 executable and copied0755 nested directory as1000:1000; then a candidate exec at1000:1000 must overwrite the file, execute the executable and create a new file/subdirectory inside the copied nested directory. The old probe demonstrated file overwrite and root-directory creation; it did not explicitly perform that last nested-directory write. Failure is qualification failure, not permission to add root exec/chown, change candidate UID or silently toggle flags. Preserve the existing version/hash/endpoint provenance and raw exported header evidence. Native actions require their separately approved test leaf.

## Production scope and APIs

Tracked child: `oro-mf-source-prepare`, under storage. Dependencies: committed source-keeper creation, accepted READY helper and source codecs, plus parent's actual verified Oro identity producer/consumer seam.

Files:

- New `pkg/factory/execution_docker_source.go`: complete production prepare/resume operation and fixed HostRuntime invocations.
- New `pkg/factory/managed_docker_source.go`: bounded canonical source-state table codec and atomic transitions.
- Existing `pkg/factory/managed_docker_keeper_inspect.go`: factor common identity/config/mount decoding from stopped-state validation, then add a separate running-state inspector. Keep stopped-create validation unchanged.
- Existing `pkg/factory/managed_store.go`: minimal fixed DDL through the existing fresh-admission/history guard.
- New `pkg/factory/execution_docker_source_test.go:TestManagedSourcePrepare`.

Read: `execution_docker_storage.go:{createSourceKeeper,dispatchPreparedKeeper}`, `managed_docker_keeper.go:{loadManagedDockerKeeper,changeManagedDockerKeeper}`, `managed_docker.go:{ready,operation,invoke}`, `managed_host.go:{reserveManagedHostInvocation,validateManagedHostRequest}`, `source_docker_archive.go:encodeDockerSourceArchive`, `source_git.go:newManagedGitRepository` for source-limit derivation, and `cmd_factory_docker_ready.go` for exact helper arguments/output.

```go
type managedDockerSource struct { Keeper ScopeHandle }

func (d *managedDockerVolumes) prepareSource(ctx context.Context,
    entries []sourceEntry) (managedDockerSource, error)
func (d *managedDockerVolumes) resumeSource(ctx context.Context,
    keeper ScopeHandle, entries []sourceEntry) (managedDockerSource, error)

func parseManagedDockerRunningKeeperInspection(result HostResult,
    keeper managedDockerKeeperIntent, volume managedDockerVolumeIntent,
    expectedStartedAt string) (string, error)
```

The returned handle selects a journaled source; it is not caller-supplied readiness authority. Later users reload the complete canonical source/keeper/volume records. They never accept `stopped=true` or a returned StartedAt field as proof.

## One immutable operation and fresh seed

`prepareSource` derives one context bounded by parent deadline, now+StepTimeout and original work Deadline. Check cancellation before and after encoding. Use the same sourceLimits as managed Git (SourceBytes for total/per-file, MaxFiles, retained MaxPathBytes/ArchiveBytes). `encodeDockerSourceArchive` validates the entire vector, links, modes, implicit directories, representation and quotas before any resource reservation. It produces independent immutable bytes. Hash those exact bytes and retain the length; do not persist another archive copy outside existing source/request allowances.

Preflight source-state schema availability read-only before resource creation; a committed objective missing that table cannot initialize history. Recheck the full verified runtime config including parent's Oro path/hash fields. Then call actual `createSourceKeeper` under that same parent context. It performs the accepted four create/inspect calls; this operation does not replace or imitate them.

The following start/seed invocations all use the unchanged operation deadline. Keeper guard deadline remains original HardStop. Helper/config work cannot refresh either deadline. Every request passes validateManagedHostRequest; env stays exactly PATH=/usr/bin:/bin, LC_ALL=C, LANG=C, cwd canonical StateRoot. No ambient executable/context/config lookup.

## Durable source state

One `managed_docker_sources` record per exact keeper and volume; unique keeper/volume keys, canonical snake_case JSON <=16384 bytes, no omitted fields. It contains Version1, AdmissionSHA256, RuntimeConfigSHA256, Volume/Keeper/StartHost/SeedHost full handles, OperationDeadline, SeedSHA256, SeedBytes, Phase and StartedAt. SeedHost is explicitly zero until reserved; StartedAt is empty until the initial post-start inspection.

Every transition reloads and validates complete canonical records, referenced scope bodies and redundant columns, original deadlines, H reservations and same generation/config identity. The source record must reference the acknowledged, bound keeper and volume. All retained rows count; no deletion/refund. The record and keeper intent together remain within the existing keeper scope's1MiB metadata allowance. Count <=SourceVersions and existing global scopes/events/H limits. No generic scope-body growth.

Use the existing private fresh-admission/history schema guard. Repeated admission never reconstructs missing used source history. Old committed objectives lacking the source table deny this feature while metadata remains readable. No migration or volume/keeper format reinterpretation.

Private state operations (not external result-ingestion APIs):

```go
reserveManagedDockerSource(ctx, manifest, journal, generation, keeper,
    operationDeadline, seedSHA256, seedBytes) (sourceRecord, error)
loadManagedDockerSource(ctx, journal, keeper) (sourceRecord, error)
dispatchManagedDockerSourceStart(ctx, journal, keeper, runtimeSHA256) (sourceRecord, error)
acknowledgeManagedDockerSourceStart(ctx, journal, keeper, startHost) error
recordManagedDockerSourceReady(ctx, journal, generation, keeper, startedAt) error
reserveManagedDockerSeed(ctx, journal, generation, keeper) (sourceRecord, error)
dispatchManagedDockerSeed(ctx, journal, keeper, seedSHA256, seedBytes) (sourceRecord, error)
acknowledgeManagedDockerSeed(ctx, journal, keeper, seedHost) error
completeManagedDockerSeed(ctx, journal, generation, keeper, startedAt) error
```

These are typed private functions using the existing concrete types (`context.Context`, `managedManifest`, `*managedJournal`, `uint64`, `ScopeHandle`, `time.Time`, `string`, `int64`); name the new record `managedDockerSourceRecord`. No generic caller-selected operation/command/result argument is introduced.

| Phase/transition | Meaning |
|---|---|
| reserve -> start_prepared | Atomically H+StartHost+source record, seed hash/length and original operation deadline frozen. No start request dispatched. |
| start_prepared -> start_dispatching | One CAS winner; exact current owner/config/unused host. Winner invokes only the fixed READY helper once. |
| start_dispatching -> start_acknowledged | Private adapter's own exact helper return passes Exit0/framing/stderr/deadline/host-retirement checks. No generic supplied HostResult accepted. |
| start_acknowledged -> ready | Separately counted exact inspection proves running guard identity, config, mount and canonical StartedAt; persist StartedAt. |
| ready -> seed_prepared | Atomically reserve H+SeedHost with the unchanged operation deadline and existing frozen seed identity. |
| seed_prepared -> seed_dispatching | One CAS winner after verifying exact archive hash/length and current authority. |
| seed_dispatching -> seed_acknowledged | Own exact cp return Exit0, stdout/stderr empty, timely and host positively retired. |
| seed_acknowledged -> seeded | Separately counted post-copy inspection proves unchanged complete keeper lifetime/config/mount and StartedAt. |

Host reservation transactions each produce one event. Never refresh the deadline or refund H. Partial/nonzero/late/oversized/unknown helper or cp output, runtime error, nonretired host or failed acknowledgment commit leaves its dispatching phase and freezes the adapter. Notfound does not release the volume/keeper or authorize replay. Inspection failure after an acknowledgment preserves that acknowledgment but returns no usable source.

`resumeSource` first loads the durable record and applies its original OperationDeadline before config reads. Re-encode the complete entries and match exact archive hash and length before any subsequent effects. Same-generation prepared phases use their original host reservation once. Acknowledged phases may finish inspections; ready may reserve seed; seed_acknowledged may finish verification; a seeded replay requires a fresh counted continuity inspection before returning usable source. Each continuation consumes ordinary remaining budgets. Start_dispatching/seed_dispatching deny all replay and await reconciliation. A later ownership generation cannot reuse old host handles. No constructor replacement bypasses this journal phase gate.

## Exact trusted calls

READY HostInvocation Path is the verified frozen OroPath. Args are `factory _docker-start-ready` and all existing required literal flags: Docker path/hash/private config/context; keeper full containerID, scope ID/nonce/generation/kind=keeper; objective hash16; policy hash; exact derived image ID; original HardStop as --guard-deadline; unchanged operation deadline as --deadline. Stdin=nil, OutputLimit256. Require exactly `READY <fullID> <HardStop>\n`, ExitCode0 and empty stderr. The existing helper already owns/joins its attaching client; this code adds no stream protocol.

Initial running inspection is only `docker --config <privateDir> --context <admitted> container inspect <fullID>`, with its own H. Reuse complete identity, canonical CreatedAt, six exact labels, guard argv/User, hardening/resource limits and one exact source mount checks. Require State.Status=running, Running=true, PID>1, Paused/Restarting/Dead=false, ExitCode0, Error empty and FinishedAt zero. Normalize the raw nonzero StartedAt with the same strict timestamp lexical rules used for Docker creation time before persisting it. Do not lose raw lexical validation through permissive time.Time decoding. A nonempty expectedStartedAt must match exactly. Changed StartedAt loses source continuity even when cleanup ownership remains intact.

Seed invocation is exactly the verified Docker path with `--config <privateDir> --context <admitted> cp - <fullKeeperID>:/source`; Stdin is the production encoder's frozen bytes; normal bounded metadata output limit1MiB. Both outputs must be empty. No -a/-L, no host paths, no extraction on host, no candidate executable and no root exec/chown. Post-copy inspection repeats the fixed exact inspect and unchanged StartedAt comparison before setting seeded. Runtime config is reread before every HostRuntime call.

## Accounting and acceptance

Additional happy path: H4/scopes4/events20 (start prepare/dispatch/host grant-retire/ack/post-inspect, then the analogous seed sequence). With accepted volume+stopped-keeper creation: **H8/scopes10/events40**. Existing fixed caps can exhaust; no arithmetic or admission increase is implied. Repeated recovery inspections cost extra H/scopes/events under unchanged caps.

```sh
uv run --no-project --python /Users/as21/codehouse/oro/.venv/bin/python \
 /private/tmp/oro-factory-verify-ldhnhnvl/verify_autonomy.py source-prepare \
 go test -race ./pkg/factory -run '^TestManagedSourcePrepare$' -count=1 -timeout=180s
```

Recording-runtime tests must prove actual eight-call production composition; no resource effects for any invalid complete source vector; exact helper flags/receipt; trusted archive bytes and every USTAR header UID/GID1000; regulars/directories before links; exact no-a/no-L argv; original shared deadline and guard HardStop; canonical running state and StartedAt continuity; single start/seed CAS winners; crash/reopen before/after dispatch; no changed seed or unknown-copy replay; acknowledgment only from own return; quota/state transaction rollback; exact intended budget errors; full canonical record corruption and missing-table replay denial. Inspect failures must never publish a source or release resource capacity. Unit tests do not claim UID1000 runtime writability.

Separate native qualification must consume the production builder's derived guard image and this actual prepareSource path through the trusted HostRuntime, then run the stat/overwrite/nested-directory-write/execute checks described above at UID/GID1000, followed by positive candidate stop, export and exact cleanup with foreign sentinel unchanged. Enabled backend unavailability fails, never skips. The test author must obtain explicit authorization for that bounded native fixture; this plan performs none.

## Downstream and remaining recovery

Candidate creation/exec attaches this source only after reloading seeded state and verifying keeper continuity; every candidate exec stays1000:1000. Stopped export must positively verify the exact candidate namespace stopped and unchanged keeper lifetime before trusted cp export/decode. Managed delivery then calls managed Git commit on the resulting vector; acceptance/review use independent fresh sources and scratch. No public managed controller activation is claimed by this leaf.

The owned VM/provider epoch fence remains necessary to recover unknown daemon requests and release their capacity. Positive client retirement, missing object, a discovered binding or a replacement adapter are not daemon-drain evidence. The fence does not block a fully acknowledged happy path; it blocks complete autonomous recovery qualification. H routine retirement and later C cleanup retain their existing authority separation.
