# Native qualification of the production guard image builder

Tracked task: `oro-mf-guard-image-native`, under `oro-mf-docker-storage`.
Depends on `oro-mf-guard-image-build` (production commit `08a3e01a`) and the
existing watchdog contract. The storage/keeper native qualification depends on
this produced-image proof; this task does not close managed factory readiness.

## Scope and entry point

- One new file: `cmd/oro-factory-image/native_test.go`, Darwin build constraint.
- Test: `TestGuardImageNative`. No production changes in this task.
- Read: `cmd/oro-factory-image/{main.go,build.go}`: `runCLI`,
  `buildGuardImage`, `validateGuardImageInputs`, `retireGuardGroup`;
  `cmd/oro-factory-guard/main_test.go`: `TestNamespaceDeadline`,
  `assertGuardIsolation`, `startGuardAndDropHostClient`,
  `assertCandidateCannotControlGuard`, `startDetachedWriter`,
  `assertNamespaceExpired`; `pkg/factory/source_docker_archive.go`:
  `dockerSourceHeader`; `source_docker_links_native_test.go`: exact resource
  identities and bounded capture/cleanup patterns.
- Source implementation is authorized after root review; native effects remain prohibited for the author.
  Root owns the actual native run, tracker and Git.

## Inputs and invocation

Without `ORO_FACTORY_INTEGRATION=1`, skip before any backend call. When enabled,
all missing prerequisites fail; never fall back to a context, image or program.
Require these explicit inputs:

```
ORO_FACTORY_INTEGRATION=1
ORO_FACTORY_DOCKER_CONTEXT=colima-oro-factory
ORO_FACTORY_DOCKER_BIN=<canonical absolute original Docker executable>
ORO_FACTORY_DOCKER_SHA256=<exact lowercase SHA256>
ORO_FACTORY_DOCKER_ENDPOINT=unix://<canonical dedicated owned socket>
ORO_FACTORY_COLIMA_BIN=<canonical absolute Colima executable>
ORO_FACTORY_COLIMA_SHA256=<exact lowercase SHA256>
ORO_FACTORY_GUARD_BIN=<canonical private static Linux arm64 guard artifact>
ORO_FACTORY_GUARD_SHA256=<exact lowercase SHA256>
ORO_FACTORY_EVIDENCE_DIR=<existing canonical owned private persistent directory>
```

Root builds the guard before the test, with bounded compiler output/deadline,
using the pinned toolchain and the trusted source checkout:

```
GOTOOLCHAIN=go1.26.10 CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -buildvcs=false -o <private-artifact>/guard ./cmd/oro-factory-guard
```

Record this command, `go version`, source HEAD, source hash and guard hash in the
root run metadata. This source directory is never a Docker build context.

Protected source verification, with integration disabled:

```
uv run --no-project --python /Users/as21/codehouse/oro/.venv/bin/python \
  /private/tmp/oro-factory-verify-ldhnhnvl/verify_autonomy.py guard-image-native-unit \
  go test -race ./cmd/oro-factory-image -run '^TestGuardImage(Build|Native)$' \
  -count=1 -timeout=180s
```

After independent source review, root uses the already-approved DIRECT scoped
native lane, preserving HOME/CODEX_HOME and redirecting Oro/build/cache state to
the established private verification directory. No sandbox-exec native claim:

```
go test -race ./cmd/oro-factory-image -run '^TestGuardImageNative$' \
  -count=1 -timeout=240s -v \
  -coverprofile=<evidence>/guard-image-native.out
```

The test calls the actual production `runCLI` with all seven documented flags,
real verified Docker, a new output directory and no runner injection. Unit tests
already exercise a standalone CLI process peer; this native test establishes the
real builder/daemon/image result. It must not call the test-only buildGuardImage
from the guard package or manufacture a receipt.

## Capacity and lifetime

Before image/container/volume creation, collect fresh host Statfs. Require a
512 MiB fixture allowance plus 1 GiB free margin. Root's pinned Go compilation
is prior preparation with separate cache evidence, not charged as fixture work.
Read the existing dedicated profile without changing it: explicit Colima binary,
`-p oro-factory ssh -- df -Pk /var/lib/docker` and fixed `/proc/meminfo` read.
Require at least 2 GiB available guest disk and 512 MiB MemAvailable. Record
profile no-host-mount/no-agent-forwarding preflight and Docker Linux/aarch64
identity. No VM stop, restart, creation, resize, pull or context switch.

The cached exact base is mandatory. Budget at most 64 daemon/preflight child
calls, each with aggregate output <=1 MiB and a five-second deadline, except the
actual production builder's existing 120-second overall/60-second-child bounds.
The fixture has one original 210-second hard stop, including a final 30-second
cleanup allowance; `go test` has a 240-second ceiling. Each phase inherits that
original bound; no retry, timeout or restart refreshes it. Namespace work uses a
separate immutable wall-clock deadline chosen once immediately before its create,
`now + 30s`, with all setup/seed/readiness/attack checks counted against it.
If those checks leave insufficient time, fail rather than extending the deadline.

Each fixture container has network none, readonly rootfs, drop ALL capabilities,
no-new-privileges, private IPC and cgroup namespace, no host PID namespace,
memory=swap=64 MiB, CPUs=0.5, pids=64, log driver none, shm=1 MiB and only a
4 MiB/1024-inode nosuid,nodev,noexec scratch tmpfs. Source is a separately bounded
1 MiB/32-inode local tmpfs volume owned by UID/GID1000. No host bind mounts,
Docker socket, credentials or other source volumes enter either container.

## Native assertions

1. Capture exact verified input binary hashes, Docker version, cached pinned base
   and VM/capacity evidence. Run production CLI. Require its canonical <=4 KiB
   fsynced receipt with pinned base reference, resolved base ID, exact guard hash,
   linux/arm64 platform, unique build label and derived immutable image ID.
   Independently inspect USER65534:65534, guard entrypoint, platform and label.
   Independently verify the retained build context contains exactly Dockerfile
   and byte-identical guard. Never admit or run the mutable tag or Golang base as
   the guard image.
2. Create a separate fixture-owned foreign sentinel using the cached base and
   fixed `/bin/sleep 120`, with a different role/name label and the same finite
   isolation limits. Record full ID, name, Created, StartedAt, PID, image and all
   labels while positively running. It must remain untouched during guard work.
3. Create one exact nonce-owned source tmpfs volume with
   `type=tmpfs,device=tmpfs,o=size=1048576,nr_inodes=32,uid=1000,gid=1000`.
   Record full options, name, CreatedAt and labels. Create the stopped guard
   container from receipt.ImageID with UID/GID65534 and the original deadline,
   mounting only that exact volume at `/source` with volume-nocopy. Inspect the
   immutable container identity and every isolation/resource field before start.
4. Start/attach and require exact `READY\n` within five seconds with bounded
   stdout/stderr. Kill and join only that owned attaching host process group,
   requiring positive ESRCH. The guard remains running without its host client.
   Positively inspect the same immutable guard identity running after client
   retirement. Only then seed: the keeper must remain running so the tmpfs
   source remains continuously mounted throughout copying and the UID probe.
5. Seed a fixed trusted USTAR of at most 8 KiB, containing exactly `nested/`
   (0755), `nested/original.txt` (0644) and `seed.txt` (0644), all UID/GID1000 and
   <=64 bytes total regular-file contents. Use exactly `docker cp - ID:/source`,
   without `-a` or `-L`. This is a narrow stdin-tar ownership compatibility probe,
   not a replacement for the production encoder's existing native roundtrip test.
   Preserve tar bytes/hash and headers. Prior actual evidence supports this exact
   policy: `/tmp/oro-mf-storage-probe.x1Szg9/probe-without-archive-flag.log` and
   `docker-links-native-301230466/headers.json`. Host-path cp documentation does not
   define this stdin-tar behavior; no flag redesign is part of this task.
6. Execute only a fixed trusted diagnostic through `docker exec --user1000:1000`
   to assert UID/GID1000 for all seeded nodes and modes0644/0755; append to the
   existing0644 file and create a new file inside the existing0755 directory;
   verify exact resulting bytes. Do not root-exec/chown, loosen modes, or execute
   any seeded/candidate program to make the probe pass.
7. Reuse the existing fixed guard diagnostic assertions: UID1000 cannot signal
   PID1, rewrite the guard, extend its input/memory or start an alternate guard.
   Start a fixed detached setsid heartbeat writer, prove its PPID1/session
   isolation and increasing heartbeat. Record readiness/host-client ESRCH and
   progress times. Then issue no Docker command or cleanup while crossing the
   original guard deadline.
8. After deadline, positively inspect stopped/PID0/exit0/notOOM and FinishedAt in
   [original deadline, deadline+2s]. A bounded top/exec refusal confirms no live
   namespace, not merely a dead attaching client. This is the actual produced
   image's watchdog proof.
9. Verify the foreign sentinel still has identical immutable identity,
   StartedAt/PID and running state before guard cleanup. Retire resources only
   after exact identity checks; collect positive absence observations afterward.

## Cleanup and persistent evidence

Never adopt a similarly named preexisting object. Persist attempted resource
names and ownership nonce before creating them. Any missing acknowledgment is
uncertainty, never proof of absence or permission to remove an unknown object.
Keep evidence on every failure; no t.TempDir removal of the fixture root.

After positive guard stop, inspect exact ID/name/Created/image/all labels, then
remove that exact container without `-v`. Remove the exact source volume only
after rechecking name/lifetime/labels/options and container absence; no forced
volume removal. Inspect the produced image by immutable ID and require its exact
build label and sole expected nonce tag before removing that ID; no base deletion,
force image deletion, cache pruning or unrelated cleanup. The sentinel is removed
in a separate fixture teardown only after rechecking its distinct exact identity;
stop/inspect/remove are all bounded. Failure cleanup may force only the already
fully identified owned container, with explicit failure evidence. Unknown or
changed identity remains retained and fails qualification.

Persist `<evidence>/guard-image-native-<nonce>/` with bounded command transcripts,
production output directory/receipt, fixed seed.tar/hash/headers, preflight,
container/volume/image identities, guard deadline and FinishedAt, sentinel
before/after observations and final exact cleanup results. `native-receipt.json`
is <=1 MiB and says passed only when all native assertions and exact cleanup
complete. Build cache can remain; no claim that image removal drains all daemon
cache. Root run metadata records before/after source hashes and exact exit status.

This qualifies the production builder and its produced guard image against the
dedicated VM. It does not grant admission, prove launchd wiring, qualify daemon
lost-ack recovery, or demonstrate end-to-end unattended source delivery.
