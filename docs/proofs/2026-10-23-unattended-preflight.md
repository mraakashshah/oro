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
bytes and two tracked symlinks. At that revision it exceeded the 24,379,392-byte
inline archive ceiling and the initial no-symlink policy. Full Oro self-hosting requires
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
`A = S + 1024F + 1536`. Newer transport components are recorded below;
full-tree native delivery remains required.

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
The fixed Docker helper and launchd invocation are verified below; their
supervisor and storage callers remain separate required work.

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

Commit `56044245` constrains cleanup to exact Docker inspect, stop, and remove
commands with complete ownership binding. Independent race tests passed in
1.663 seconds and adversarial checks in 1.485 seconds; production coverage was
95.36%, with CLI package lint passing. Native exact-object missing diagnostics
were measured before encoding the absence parser.

Commit `eb8d737d` adds the read-only immutable admission view needed by CLI
publication. It verifies frozen artifacts, then compares the committed journal
and snapshot identities in a read-only transaction. Independent race tests
passed in 10.580 seconds; author acceptance and architecture checks passed in
15.300 seconds, with 94.12% production coverage and package lint passing. It does
not initialize admission or authorize activation. Preparing/armed/aborted
activation and the runtime bundle publisher remain required work.

Native volume inspection exposed a local-offset timestamp that the original
strict ownership comparison rejected. Commits `898c56a1` and `1e209868` normalize
daemon timestamps to the exact UTC instant at ingestion; persisted permits and
results remain strictly canonical. Independent race runs passed in 1.300 and
1.793 seconds. The new normalizer and ingestion statements had 100% coverage;
factory and CLI lint passed. A different instant or malformed/excess-precision
timestamp still fails before mutation.

Commit `48d7e432` adds the hidden frozen-Oro cleanup helper. It reopens durable
authority and rechecks the identical permit, granted process group, immutable
runtime artifacts, and original deadline before every fixed Docker child and
result. Real inherited stdin on Darwin required an owned nonblocking descriptor
duplicate to support deadlines; tests cover cancellation, closure, flag
restoration, and withheld EOF without spawning Docker.

The helper's independent protected race run passed in 29.032 seconds. The final
actual-binary native suite passed in 30.439 seconds, with 90.80% helper production
coverage and CLI lint passing. Under an environment containing only PATH,
LC_ALL, and LANG, it stopped an owned running container, removed another, and
removed an owned tmpfs volume after the original work deadline. The volume's raw
`2026-10-24T02:08:11-04:00` identity became `2026-10-24T06:08:11Z`. Each result
decoded against its exact permit with mutation acknowledgment; foreign sentinel
inspection bytes stayed unchanged, and all owned fixtures were retired.

Evidence is `cleanup-helper-independent.log`, `cleanup-helper-final-native.log`,
and the latter's `-result.json` and `.out` files under the verification directory.
This fixture seeds valid admitted journal/PGID state. It proves the real helper
and Docker operations; the production launchd invocation is verified separately
below. Supervisor recovery and public activation remain pending.

Commit `5a42bf2f` binds cleanup invocations to exact frozen runtime configuration
and canonical authenticated permit bytes. Root's independent race test passed
in 1.585 seconds; the affected suite passed in 2.395 seconds, new validation
statements had 100% coverage, and CLI lint passed. Both optional configuration
fields absent preserves ordinary host/observer construction and denies cleanup.

Commit `55eb33eb` connects that restriction to the shared launchd scope renderer
and native runtime. The runtime reads the current reserved permit before request
creation or bootstrap. An independent review found no issues, and its protected
race tests passed in 2.114 seconds. The author's affected regression suite passed
in 7.020 seconds, renderer tests in 1.461 seconds, and CLI lint passed. All four
new permit-lookup/error/restriction statements were covered; the full switch had
seven of eight statements covered because exact scope validation makes its
defensive default unreachable.

The direct native race test passed in 21.820 seconds. An actual launchd job
completed HELLO/GO/PUBLISHED, removed its exact owned tmpfs volume after the work
deadline, and left a foreign sentinel unchanged. The canonical result reported
successful removal; the cleanup token count remained one. Before `Invoke`
returned, the scope was quiescent and its authenticated PID/PGID 34053 returned
ESRCH. Replaying the consumed scope was denied before launch. Fixture teardown
retired all owned resources. This seeds legitimate admitted authority; it does
not demonstrate public admission, supervisor allocation, or unattended recovery.

Initial runs under the protected test harness failed at bootstrap. Exact-job
macOS logs show that launchd rejected the sandboxed caller before HELLO. The
successful run reused the established narrowly scoped native lane with private
Oro state/caches and unchanged HOME/CODEX_HOME. Production launch behavior was
not changed to accommodate the harness. Evidence is
`cleanup-launchd-sandbox-denial.log`, `cleanup-launchd-native-final.log`,
`cleanup-launchd-native-final-result.json`, and `cleanup-launchd-independent.log`
under the verification directory; `run_cleanup_launchd_native.py` reproduces the
exact native command and records its environment and before/after source hashes.

## Full-source transport components

Commit `e2ce78e9` raises the complete encoded host request cap to 64 MiB while
charging both retained copies. The archive allowance tops out at 49,545,216 bytes;
the next byte rejects. Exact accounting, overflow and Docker-root tests passed,
with all 43 retention statements covered. Independent retention and existing
wrapper race runs passed in 1.304 and 6.445 seconds. Ledger limits remain unchanged.
Commit `0e0720b4` tests historical reservations: metadata stays readable, insufficient
capacity denies new Git execution, and attempted top-ups leave identities,
progress, events and counters unchanged. Independent race passed in 1.817 seconds.

Commits `05dbd0cc`, `fb2ede84` and `e53954e4` preserve restricted internal symlinks
in source vectors, exact Git objects and Docker seed archives. Validation checks
every raw traversal prefix before a later `..`, counts target bytes, and rejects
unsafe links before effects. Both real SHA-1 and SHA-256 Git fixtures preserve
raw link bytes through safe changes without filters or hooks. Docker codec tests
verify Size0/raw Linkname, regular entries before links and quota boundaries.
The final Git regression race passed in 17.659 seconds; independent combined
Git/Docker tests passed in 6.050 seconds. Those initial Docker codec fixtures
were synthetic. Commit `d2b112ae` adds the separate real Docker copy round-trip:
production seed encoding, actual copy in/out without `-a`/`-L`, and production
decoding preserved all five entries and three raw link targets. The protected
native test passed in 1.37 seconds (package race run 2.638 seconds) with 29 joined
CLI commands. The 7,168-byte actual export has SHA-256
`a9387c7a16044dff69271d836002bf70324608dc1dc60124048e158582bc34a4`.
The holder was positively stopped with PID0 before removal; both volumes and the
holder were removed and verified absent. The sentinel's complete inspection was
unchanged before its separate teardown. Root independently inspected the actual
archive, receipt, command transcript and source. Package lint passed. Artifacts
are in `docker-links-native-301230466/` beneath the verification directory. This
proves native codec compatibility for the fixture, not managed storage lifecycle,
full-tree delivery, quota enforcement or daemon-crash recovery.

Commit `df3a41ce` queries locally computed blob identities in one bounded Git call
and writes only missing blobs after strict response validation. A one-blob repair
uses six host calls; no-op uses one, and k missing blobs use 5+k. Actual corrupted
loose-object contents still fail subsequent raw export verification. The final
Git regression race passed in 29.082 seconds; independent verification passed in
13.176 seconds. Both new functions have 100% statement coverage.

Commit `bcce6b2d` tests the wrapper's actual complete 64 MiB JSON/base64 envelope,
canonical limit+1 rejection, binary stdin, escaping and malformed inputs. Invalid
UTF-8 strings cannot silently become executable changed arguments: the reader
rejects their noncanonical replacement encoding. Explicit valid replacement runes
and arbitrary binary stdin remain distinct positive controls. Final race passed
in 14.581 seconds; independent full verification passed in 15.093 seconds, with
the final digest-case style adjustment checked separately in 1.491 seconds.
This codec test does not replace the separate conservative core admission gate.

All these components passed package lint and independent source review. Logs
remain under `/private/tmp/oro-factory-verify-ldhnhnvl`, including `source-links-*`,
`git-links-*`, `transport-t4-*`, `transport-t1b-*`, `git-presence-*` and
`transport-t6a-*`.

Commit `12abedb9` adds historical capacity-ledger regressions. Old active and
released reservations retain their exact canonical ledger/bootstrap identities
when new execution or a top-up is denied. Missing execution state cannot release
an old charge; a fresh objective succeeds with sufficient independently computed
capacity. Final race passed in 1.686 seconds and independent race in 1.664 seconds.

Commit `9fcb5045` adds the actual native 32 MiB binary round-trip through launchd
and real Git. A fresh Statfs probe and the serialized allocator reserved
8,407,095,296 bytes with a separate 1 GiB free-space margin. The input and returned
output share SHA-256
`d3a1bd28eba27b766ac3f76d348eba04bf40fd89a9e313e0ec0c0311bfb01a71`.
The largest encoded request was 44,740,284 bytes; retained logical files totaled
201,018,202 bytes. Both authenticated process groups were positively absent before
the production release predicate released the unchanged reservation tuple. Both
spent H tokens remained in the journal. Native race passed in 30.285 seconds,
with 9.120 seconds spent in the round-trip; package lint passed. Root verified
the retained artifact hashes, journal rows and group absence independently.

The test process's OS RSS high-water mark was 1,423,130,624 bytes. Wrapper RSS
samples reached 298,696,704 and 222,101,504 bytes at a 20 ms sampling interval;
these samples are not proven process peaks or hard memory limits. Evidence is in
`host-bulk-native.log`, `host-bulk-native-result.json` and
`host-bulk-678148128/receipt.json` under the verification directory. Native evidence
uses source hash `5002405d59005b8213f8406c102864e89c767422fcbdee3100ebed9f20e016c4`;
the committed formatting-only revision has hash
`a0f64e25d32d72a7ba073d67b38c31fc6537659e12396ed004dc1951803ad199`
and passed the pinned formatter and compilation. This fixture seeds private
journal rows; it proves transport and actual allocation, not public startup.

The full factory package race/shuffle run after the transport changes passed in
254.366 seconds with 88.7% statement coverage (`transport-core-integrated.log`).
Commit `20e72f52` adds native large-transfer failure coverage. Cancellation occurs
after an authenticated child has consumed a 128 KiB prefix of 32 MiB input;
the output test exceeds a 33,554,431-byte cap by one byte. Both exercise actual
`Invoke`. Missing and late publication cases use a test driver around the real
runtime components and authenticated socket, not unmodified `Invoke`: a complete
pending result cannot substitute for publication, and holding the actual frame's
newline beyond its original deadline causes rejection. All four cases returned
no consumable result, preserved five spent H tokens including the seed call, and
positively retired all five groups before release. Native race passed in
50.748 seconds; lint, formatting and independent source/artifact review passed.
The fault sequence took 26.744 seconds and retained 432,061,113 bytes. Evidence is
`host-bulk-faults-native.log`, `host-bulk-faults-native-result.json` and
`host-bulk-4252998245/fault-receipt.json` under the verification directory. The
reused base fixture's zero elapsed field is not timing evidence.

Complete Oro delivery, managed execution/recovery and sustained factory fault
qualification remain required.

## Managed Docker volume intents

Commit `786fe679` persists an executable volume intent and atomically reserves
its H token, host scope and volume scope. Prepared records survive controller
process death without a fresh reservation; dispatching records cannot create
again. Exact lifetime binding is separate from acknowledgment and quiescence.
Admission replay cannot recreate missing intent history, including when a missing
admission row coexists with retained delivery history. Tests reproduce and reject
inconsistent scope columns and JSON lifecycle overrides before volume transitions.

Affected race tests passed in 17.265 seconds; independent verification passed in
17.117 seconds. New production coverage is 241/278 statements (86.69%); package
lint and the pinned formatter passed. Evidence is `docker-volume-state-seal.log`,
`docker-volume-state-seal-coverage.out`, `docker-volume-state-lint-seal.log` and
`docker-volume-root.log` under the verification directory.

This is journal component evidence. The create/inspect adapter, real CLI reader,
keeper/source integration and daemon lost-acknowledgment recovery remain required.
Review also identified the same JSON lifecycle overwrite in the shared scope
loader outside the locally guarded volume path. Commit `8364dfd9` separates
canonical identity decoding from authoritative lifecycle columns. Behavioral
regressions demonstrated false grants, false quiescence and reopened cleanup
permits before the repair. Author race passed in 17.680 seconds and independent
race in 17.802 seconds; both decoder functions and the loader have 100% statement
coverage. The real launchd bulk and fault regressions passed again in 61.383
seconds with unchanged relevant source hashes. Root checked all seven new groups
absent and all seven spent tokens retained. Evidence is `scope-integrity-final`,
`scope-integrity-root` and `scope-integrity-native` logs under the verification
directory. No candidate journal-write capability was demonstrated; final release
already used a separate canonical reader.

Commit `52d4e3a9` applies the same validation before both cleanup exclusion
filters, so an invalid kind or backend column cannot hide a live helper or host.
All three reported paths failed their behavioral regressions before repair.
Author race passed in 17.915 seconds and independent race in 20.621 seconds;
package lint, formatting and independent review passed. Evidence uses the
`cleanup-scope-integrity-*` log prefix. The stricter guard also exposed two older
volume test fixtures that failed on malformed setup before reaching their intended
budget/exclusion checks. Commit `d6cdd53b` repairs those fixtures with canonical
scopes and exact expected errors, including complete authority rollback checks.
Author race passed in 13.704 seconds and root targeted regressions in 5.884
seconds; package lint and independent review passed.

Commit `2e9c8961` adds the private create/inspect adapter and scalar configuration
reader contract. It emits fixed commands, acknowledges only its own trusted
invocation after exact retirement, and preserves uncertainty after failed
completion. Inspection binds exact lifetime without claiming quiescence. Review
reproduced and repaired an unbounded prepared configuration reread and a missing
`o=` driver-option key; the latter now has an independently parsed option-map test.
Final affected race passed in 21.379 seconds; root's full review run passed in
18.391 seconds and the final command regressions in 1.904 seconds. New production
coverage is 233/250 statements (93.20%); package lint and independent review passed.
Evidence uses `docker-volume-adapter-options-*` and `docker-volume-options-root`.
These tests use a recording trusted runtime. Actual Docker lifecycle integration,
keeper continuity and daemon recovery remain required.

Commit `28c1f12f` supplies the CLI configuration reader. Every callback reopens
and verifies frozen artifacts and compares all eleven represented immutable
admission fields; errors return an empty configuration. Tests cover mutations,
cancellation, ambient Docker configuration and read-only behavior. Root race
passed in 1.635 seconds, author race in 1.950 seconds, command-package lint
reported zero issues, and the new producer covers 8/8 statements. Independent
review matched the exact source hashes. Evidence uses `docker-volume-config-root`,
`docker-volume-config-final` and `docker-volume-config-lint-final`. The first RED
was a missing-symbol compile failure, not a behavioral regression. This reader
does not itself grant execution; controller construction and native storage
lifecycle remain required.

The accumulated factory changes passed the full `pkg/factory` race/shuffle run
with coverage collection (`volume-core-integrated.log` and its result metadata).
The harness completed in 287.894 seconds. This verifies the package tests, not
end-to-end unattended delivery.

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
