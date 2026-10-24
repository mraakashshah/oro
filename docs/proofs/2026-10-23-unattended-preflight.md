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

## Remaining evidence

Still required: frozen managed admission, durable execution ownership, automatic
controller/supervisor recovery, protected source import, independent acceptance,
restricted real-model execution, sustained fault trials, and integration into an
unattended objective queue. A user-session LaunchAgent proof also cannot establish
recovery across logout or host failure. No production cutover is claimed here.
