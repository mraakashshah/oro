# Unattended factory preflight — 2026-10-23

This records infrastructure and gate-repair observations while the unattended
runtime is being built. It does **not** establish unattended factory operation.
The earlier [core proof](2026-10-23-factory-core.md) remains a historical record
of the experimental trusted-adapter lifecycle.

## Dedicated execution VM

The following command completed successfully on the development Mac:

```sh
colima start oro-factory \
  --runtime docker --vm-type vz --arch aarch64 \
  --cpus 2 --memory 4 --disk 30 \
  --mount none --template=false \
  --activate=false --ssh-agent=false --ssh-config=false \
  --network-address=false --network-host-addresses=false \
  --port-forwarder=none --vz-rosetta=false --binfmt=false
```

Observed afterward:

- The existing `default` profile remained stopped; Docker's current context
  remained `default`.
- The new `colima-oro-factory` context uses
  `unix:///Users/as21/.colima/oro-factory/docker.sock`.
- Generated Lima configuration contains no host filesystem mounts or SSH-agent
  forwarding. `colima -p oro-factory ssh -- mount` showed guest disks and virtual
  filesystems, with no host filesystem exports.
- Docker reports AppArmor, its built-in seccomp profile, and cgroup namespaces.

The initial Go qualification image was pulled and resolved to:

```text
golang@sha256:27f2d3b83cfc5803c8078f8e730af0f7c431bf067afaec8f60a36fb763187cba
```

A disposable preflight container used `--network none`, `--user 1000:1000`,
`--read-only`, `--cap-drop ALL`, `--security-opt no-new-privileges`,
`--pids-limit 64`, `--memory 512m`, `--cpus 1`, and a 64 MiB private `/tmp` tmpfs.
It reported:

```text
uid=1000 gid=1000 groups=1000
go version go1.26.10 linux/arm64
CapEff:     0000000000000000
NoNewPrivs: 1
Seccomp:    2
isolated-preflight-passed
```

The same command asserted that `/Users/as21`, the Docker socket, and host Claude
and Codex credential paths were absent, that only loopback existed, that a write
to the root filesystem failed, and that a scratch write succeeded. This is a
configuration preflight, not a hostile escape test or a controller-death proof.
The preflight container exited and was removed. The dedicated VM remains running
for implementation and qualification; no existing profile was changed.

## Qualification repairs

| Commit | Repair | Observed verification |
| --- | --- | --- |
| `44a901b1` | Guard custom SQLite `As` results that leave the target unset | Regression reproduced the panic; full `pkg/dbutil` race tests, lint, and NilAway passed |
| `960acbaa` | Give concurrent quality-gate worktrees an independent Git repository | Existing cache-concurrency test failed against protected shared Git metadata before the change and passed under the unchanged containment profile afterward |
| `893c3e84` | Reject missing/nil checkpoint worker owners locally | Seven regression cases failed before repair; focused release suite passed with the race detector; changed-code lint and production NilAway passed |
| `2c75b103` | Select patched Go 1.26.10 through module-driven build/gate defaults | Launcher RED/GREEN, real default Make version, lint/NilAway smoke, shell checks, and module tidy passed |
| `cf959ea0` | Update vulnerable Go modules and their required dependency closure | Actual-importer module scan failed before update; importer/CLI module scans and full symbol scan then reported no vulnerabilities; affected-package race tests and readonly build passed |
| `84f56489` | Initialize fresh task schemas atomically on the production path | Rollback and concurrent-initialization regressions failed before repair; protocol race suite, lint, independent review, and contained CLI database tests passed |
| `1c32209e` | Remove unreachable reservation/manifest lifecycle code | Active storage race suite and focused CLI tests passed; a raw-SQL preservation test retained schema version 5, historical rows, and artifacts across reopening |
| `a1e98afd` | Format retained core proof JSON | Biome passed; parsed evidence values remained unchanged |

The Go 1.26.10 vulnerability scan reported zero called-code and zero
imported-package findings. Its six unused-module findings (five in
`golang.org/x/net`, one in `golang.org/x/text`) were subsequently removed by
`cf959ea0`. Local evidence is retained at
`/private/tmp/oro-fg-toolchain.JiVdrV/` and
`/private/tmp/oro-fg-dependencies.f1tdJE/`.

Seven earlier process-cleanup failures were reproduced as a macOS containment
limitation: `/bin/ps` is setuid and cannot execute inside `sandbox-exec`, even with
an otherwise permissive profile. A relocated copy was not a working substitute.
After source review, the exact seven tests passed natively with private Oro state
and caches under Go 1.26.10 (1.042 seconds). They used actual process discovery and
owned test processes; no production scanner was replaced or weakened.

The same seven tests subsequently passed under the unchanged containment profile
(1.439 seconds) using a local read-only process-list bridge. The bridge runs real
`/bin/ps` outside the profile and accepts only the scanner's three query forms:
`x -o pid=,pgid=`, `-o state= -p POSITIVE_PID`, and
`-o uid= -p POSITIVE_PID`. It cannot execute arbitrary commands or signal processes.
Its request-policy test was observed failing before implementation and passing
afterward. Source is retained at
`/private/tmp/oro-factory-verify-ldhnhnvl/ps_broker.py`. This bridge restores process
observation while retaining home/shared-Git write protection. It is verification
infrastructure, not a factory execution sandbox. The broad repository suite has
**not** yet passed its final qualification run.

The complete `internal/...` and `pkg/...` runtime lane subsequently passed with
shuffle and coverage enabled (163.491 seconds). Dispatcher coverage was 86.4%;
factory coverage was 86.9%. This baseline used source `1c32209e`, before the new
managed runtime components. The retained log and coverage are
`/private/tmp/oro-factory-verify-ldhnhnvl/autonomy-runtime.log` and
`autonomy-coverage.out` in the same directory. The full project gate remains
outstanding; its retained `CheckRuntimeBudget` entry point must be wired to real
managed admission before dead-code qualification can pass.

## Independently bounded namespace primitive

Commit `4acc2407` adds `cmd/oro-factory-guard`. Its Linux PID1 process runs as
UID/GID 65534, installs a monotonic timer before writing `READY`, and exits at its
immutable deadline. Candidate commands run as UID/GID 1000. A blocked readiness
write cannot extend the timer. The watchdog has no extension channel.

Two independent live runs of the following test passed against the dedicated VM:

```sh
ORO_FACTORY_INTEGRATION=1 \
ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory \
GOTOOLCHAIN=go1.26.10 \
go test ./cmd/oro-factory-guard -run '^TestNamespaceDeadline$' \
  -count=1 -timeout=180s
```

The fixture verifies the actual container isolation settings, attempts to signal
and replace the watchdog as the candidate user, and launches a double-forked
`setsid` writer. It confirms that PID1 adopted the writer and that its heartbeat
was advancing. After killing and reaping the attaching host Docker client, the
fixture makes no Docker calls until after the original deadline. Both runs found
the container stopped with PID 0, exit code 0, no OOM, and no running descendants;
observed termination was approximately 15 and 20 milliseconds after the deadline.
Owned test containers and images were removed.

The tested watchdog source SHA-256 was
`ad9523d32b1d70c5a8b78267d75e459e4cebff72cbadd9be1a8f04e1d286faa0`.
Fresh unit race tests passed with 89.5% statement coverage; scoped vet, lint, and
independent source review passed. The live test proves this Linux arm64 primitive.
It has no controller or journal, so it does not yet prove recovery after killing
actual managed controller/supervisor processes or losing their journal.

## Candidate archive boundary

Commit `cf0ced11` adds bounded USTAR decoding without host filesystem extraction.
Hostile paths, aliases, case collisions, links, special entries, extended
metadata, malformed trailers, and byte/inode/path limit overflow are rejected.
Initial support accepts printable ASCII paths and rejects PAX/GNU extensions.
Ordinary Docker copy output was inspected for compatibility.

Independent review caught group/other-only execute permissions being incorrectly
promoted to Git executable mode. A real Git fixture confirmed that only the owner
execute bit determines canonical mode. The regression failed before correction;
the final archive and architecture race tests passed in 1.315 seconds, with scoped
lint reporting zero issues. Container export, exact Git tree construction, and
managed delivery integration remain separate required work.

## Durable state and host wrappers

Commit `f28ab781` bounds managed SQLite data to 64 MiB and reserves 256 MiB for
the database and rollback journal. All managed writers use verified FULL/DELETE
configuration; legacy WAL remains unchanged. Tests exercised a 62,077,616-byte
live journal, killed an actual committing process, and verified atomic recovery.
Pinned-reader failures preserve committed state and release failed transaction
ownership. Independent review caught whole-objective-directory loss resetting
spent budgets; a durable root-level initialization reservation now prevents it.

The full factory race/shuffle suite passed in 209.200 seconds at 85.4% coverage
before that final no-reset repair. After the repair, affected race tests passed
in 62.742 seconds; a subsequent capability-path cleanup passed focused race tests
in 5.560 seconds and package lint with zero issues. These distinct snapshots and
their logs are retained under `/private/tmp/oro-factory-verify-ldhnhnvl/`.

Commit `b6fb7521` adds the authenticated host wrapper. Independent pipe completion
prevents ordinary nonzero exits from hiding truncated output or retained input
pipes. Publication requires a complete successful capture, the original deadline,
and the authenticated PUBLISHED protocol. Root's focused race suite passed in
7.669 seconds; wrapper coverage was 82.5%, including 100% of its new drain helpers.
Independent review passed after reproducing and repairing deadline, replay, and
combined nonzero-exit/retained-pipe failures.

Native backend tests exposed a renderer defect missed by `plutil`: launchd rejects
expanded boolean elements. Commit `ec787eaf` emits self-closing elements while
preserving escaped literal strings. The regression witnessed RED, then passed
under race in 1.449 seconds; independent source review and CLI lint passed.
The real backend subsequently passed seven launchd scenarios, including lost
acknowledgment, timeout, concurrent retirement, and pending-byte tampering.
Commit `b5c2a99c` includes the reviewed runtime. Independent review reproduced
cleanup exceeding the hard stop and a mismatched handle canceling an active
invocation with the same ID. The repairs bound cleanup by the original hard stop
and compare the complete handle before cancellation. The repaired native race
suite passed independently in 24.503 seconds; runtime coverage was 82.3% and CLI
lint passed. All seven owned job labels were absent and all five granted process
groups returned ESRCH. These scenarios do not establish complete
controller/supervisor recovery or qualify the unfinished service.

## Exact source and capacity foundations

Commit `8158d076` exports raw Git objects, verifies every blob and reconstructed
tree, and imports deterministic exact trees without candidate filters or hooks.
Every Git invocation consumes persisted budget and scope authority before the
HostRuntime call. Real Git/journal tests passed independently in 24.253 seconds;
root's final focused suite passed in 7.758 seconds and package lint passed. The
new production scope had 84.3% statement coverage. Those tests use a test host
runtime; they do not prove native isolation or managed delivery integration.

On this Mac, `/usr/bin/git` is an Apple developer-tool launcher whose copied
binary dies by signal. The actual executable resolved by `xcrun --find git`
works after freezing. Admission must qualify that identity rather than falling
back to a mutable original installation.

Commits `d98371c4`, `f20278a5`, and `8e1322dc` add shared retained-byte accounting,
exact terminal release eligibility, and a durable bounded host capacity ledger.
The ledger uses a canonical parent lock, counts active reservations, and retains
released IDs. Cross-process tests cover competing admission and SIGKILL before
and after publication rename. Independent race verification passed in 6.359
seconds; root's fresh run passed in 6.252 seconds. Both production ledger entry
points and the capacity calculation reached 100% function coverage; CLI lint
passed. Missing or corrupt authority retains capacity.

Failed initialization still needs durable proof that execution was never enabled
before releasing a reservation. The first-ledger bootstrap and restricted cleanup
authority now have component evidence below; their public start and automatic
recovery integration remains required.

The complete Oro tree at `7d658569` contains 1,655 blobs totaling 33,127,289 raw
bytes and two tracked symlinks. It exceeds the current 24,379,392-byte inline
archive ceiling and the initial no-symlink policy. Full Oro self-hosting requires
bounded bulk transport and safe exact symlink representation, followed by a real
full-tree trial. Omitting tracked files or qualifying only small fixtures would
not satisfy that requirement.

## Container transport and recovery components

Commits `a5beaab3` and `365b9c53` account for Docker's extra root header, strictly
decode its `./` root/prefix, and encode deterministic USTAR source owned by
UID/GID 1000. Native copy probes established that omitting `docker cp -a`
preserves candidate ownership; `-a` rewrote it to the guard UID. Export remained
identical after the writer stopped while the keeper retained the volume.
Independent codec race tests passed in 7.603 seconds and root's run in 7.900
seconds; package lint passed. The measured archive bound is
`A = S + 1024F + 1536`. The full-tree limit above remains unresolved.

Commit `f4feb722` adds the guarded Docker readiness helper. It verifies the exact
never-started container before attaching, accepts only timely exact READY, and
joins the attaching client while leaving the keeper alive. Native proof used
the actual CLI and private context despite polluted ambient Docker settings;
its receipt is `/private/tmp/oro-docker-ready-native.hD7Ccc/receipt.json`.
Root's final repeated race suite passed in 24.921 seconds; changed-code CLI lint
passed and production coverage was 94.30%. A timeout-path test was corrected to
distinguish explicit reap uncertainty from successful joined-child completion;
production deadline and success rules were unchanged. This is not yet a
journal-to-storage integration proof.

Commit `35779f3d` recovers first-ledger initialization from one bounded durable
bootstrap record without changing its original reservation. Actual SIGKILL tests
cover publication boundaries. Independent review caught and verified a repair
requiring parent-directory durability before recovery creates dependent state;
injected sync failure leaves no root or initialized seal. Independent race tests
passed in 8.472 seconds and the separate ordering regression in 1.492 seconds;
CLI lint passed and affected production coverage was 83.04%. Missing initialized
or legacy authority still cannot reset the ledger. Activation/abort release is
a separate outstanding protocol.

Commits `7de2810d` and `324dc1ac` add a separate immutable cleanup allowance C,
count its retained bytes and records, and atomically journal cleanup tokens,
scopes, and exact target permits. Stopping revokes ordinary work; final stopped
requires every scope positively quiescent in the same transaction. Cleanup and
ordinary work exclude each other in both allocation directions. Independent
cleanup/journal, release, and adapter race tests passed in 5.194, 1.972, and
1.451 seconds. Root's combined component suite passed in 14.859 seconds; full
factory/CLI package lint passed. The allocator had 92.9% coverage and its public
getter/decoder/target checks had 100%. C is supervisor recovery authority after
controller retirement; a live controller uses ordinary H for routine retirement.
The fixed Docker helper and launchd wiring remain separate required work.

Commit `51df7500` defines and reads the frozen runtime configuration and exact
private Docker context snapshot. It verifies canonical bytes, hashes, file and
directory identities, ownership, and the dedicated Unix endpoint without
creating state or granting authority. Independent root race verification passed
in 1.691 seconds; the author's affected suite passed in 9.397 seconds, with
90.32% production coverage and CLI package lint passing. Foreign-owner rejection
is covered by the metadata predicate; its filesystem subcase skips for non-root
users and is not a native foreign-UID proof. CLI start/resume must still produce
the artifact and compare it with the immutable admission identity.

Commit `73855c20` repairs a measured Go rooted-file behavior: `os.Root` can follow
contained symlinks despite `O_NOFOLLOW`. Managed artifact and initialization-marker
reads now bind a regular leaf before and after opening to the same inode.
Only genuine initial absence can reach initialization logic. Actual filesystem
replacement regressions cover aliases, FIFOs, disappearing paths, and unchanged
existing database state. The full affected race suite passed in 64.876 seconds;
root's independent focused run passed in 2.488 seconds and package lint passed.
The shared opener reached 94.7% coverage and the marker reader 100%.

Commit `6aefdff8` adds the shared canonical cleanup-result codec. It validates
exact permit/resource identity and all 36 operation, observation, and
acknowledgment combinations, including constrained discovery of an unknown
container or volume lifetime. Independent race verification passed in 1.472
seconds; extra map-independence and 16,385-byte rejection checks passed in 1.284
seconds. Decoder/validation coverage was 100%, encoder coverage 91.7%, and package
lint passed. A result never establishes pending-request drain or quiescence.

The combined factory core through `6aefdff8` passed a full package run with race
detection and test-order shuffling in 231.042 seconds, with 87.2% statement
coverage. Evidence is `factory-core-components.log` and
`factory-core-components.cover` under the verification directory above. This
checks the combined core changes; live Docker/launchd integration and unattended
delivery remain separate operational qualification requirements.

## Remaining evidence

Managed contract validation, immutable admission, durable execution ownership,
and kernel Unix socket peer authentication are committed as component foundations
(`e4d35865`, `a3602210`, `8daf3a85`, and `2ac91908`). A full factory race/shuffle
run passed in 151.943 seconds with 85.3% coverage before a final behavior-preserving
admission helper extraction; focused admission/process-lock tests and lint passed
after that extraction. This is component evidence, not unattended delivery.

Native launchd preflight with the actual wrapper binary established PID=PGID,
inherited child/grandchild groups, and positive group ESRCH after normal exit,
active bootout, and timeout. Withholding GO created no payload or pending result.
Transient EPERM after bootout reproduced and was treated as uncertain until
ESRCH. The final cleanup found all six owned labels absent and all five
authenticated groups empty. Evidence lives in
`/private/tmp/oro-host-preflight-mvd1u_mg/`, including `gui/report.json`,
`gui-withheld-retry/report.json`, and `final-cleanup.json`. These tests preceded
the latest wrapper output-completion repair and did not exercise the durable
runtime backend; they establish the platform primitive only.

Still required: automatic controller/supervisor recovery, protected source import,
independent acceptance,
restricted real-model execution, sustained fault trials, and integration into an
unattended objective queue. A user-session LaunchAgent proof also cannot establish
recovery across logout or host failure. No production cutover is claimed here.
