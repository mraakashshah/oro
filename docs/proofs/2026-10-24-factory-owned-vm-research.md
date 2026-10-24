# Direct VZ owner: bounded native qualification contract

Read-only research, 2026-10-24. No VM, launchd, Docker, network, source or Git changes were performed. Only this research artifact was written. This is the next provider prototype test, not platform-ledger implementation or full factory recovery qualification.

## Load-bearing platform primitive

Use a small trusted Darwin helper retaining exactly one `VZVirtualMachine` object on one serial dispatch queue. The local Apple SDK supplies the authoritative primitive:

- `/Library/Developer/CommandLineTools/SDKs/MacOSX15.2.sdk/System/Library/Frameworks/Virtualization.framework/Versions/A/Headers/VZVirtualMachine.h`: `stopWithCompletionHandler:` stops a Running or Paused machine without guest cooperation; its completion reports nil error only after successful stop. Check `state == VZVirtualMachineStateStopped` on the same object's queue after that completion. The header requires `com.apple.security.virtualization` entitlement and serial-queue use.
- `VZVirtualMachineDelegate.h` in the same directory: guest-stop and stop-with-error callbacks exist, but neither a requested stop nor a missing heartbeat substitutes for successful force-stop completion.
- `VZDiskImageStorageDeviceAttachment.h` in the same directory supports raw disk images and explicit full synchronization. It does **not** document exclusive disk attachment. Do not claim that opening a disk proves old VM retirement.
- Hypervisor SDK `.../Hypervisor.framework/Headers/hv_vm.h` describes a VM associated with the current process. That does not establish that a Lima hostagent is the actual Hypervisor owner: this machine has `com.apple.Virtualization.VirtualMachine.xpc` with Application service type and multiple instances. Do not infer VZ retirement from limactl wait/flock alone.

Unmodified Lima is therefore not the first full-lifetime proof target. Its inherited-FD/foreground/launchd mechanics can be tested separately, but a public CLI stop/status or hostagent disappearance has not exposed the direct successful-stop contract. A direct helper gives us the smallest usable lifecycle capability without a general hypervisor abstraction.

## Local immutable prerequisites

No download is needed for the candidate boot disk. Copy/decompress the cached immutable image into a fresh private evidence directory; never open the existing Colima writable disk for use or copy its live contents.

| Asset | Observed bytes | SHA256 |
|---|---:|---|
| `/Users/as21/Library/Caches/colima/caches/b0992ab88f5a3c0c436bbb3065c01466f20dc1dd0eb0a60299d410176f21a1c3` (gzip raw ARM64 EFI image) | 332354401 | `1fc0354f4f99734ce3886628cc7af8b0437c1a1d391b126bd09cba0df35ee53f` |
| Entire decompressed raw image, streamed and hashed without writing | 3758096384 | `7ab64981326b7c34f4822d58764e83d2b4cb097472fa1c23975f3bb7b53790d0` |
| `/opt/homebrew/Cellar/lima/2.2.1/bin/limactl` (only for a later Lima mechanics comparison) | 32974672 | `c793937fef02687ba8eb03f2b0a4655ad26583d94701e8659492933b6c398a68` |
| `/opt/homebrew/Cellar/lima/2.2.1/share/lima/lima-guestagent.Linux-aarch64.gz` (not required by direct lifecycle test) | 7425356 | `0b222c558d8a47861d37a3da6a981d8bf0572a433d8d709248fb0bb0587cdbc1` |

The gzip stream fully decoded within a 4GiB bound; raw header is protective MBR plus GPT. GPT disk GUID bytes are `fae36b1b3ba40f41923ce1abd1dd7fbe`. The other cached image is QCOW2 v3, virtual size3758096384, no backing file; do not introduce a qemu-img dependency. The selected raw cache is the boot input named by the existing external Colima configuration, but that configuration, its provisioning, networks and active20GiB disk are not adopted.

Before any VM effect, verify regular-file/no-symlink/private destination ancestry, gzip digest, exact decompressed length/digest, frozen helper executable digest/code signature/entitlement, fixed manifest digest and same-filesystem inode identities. Record actual macOS build, VZ framework version (observed162.10.2), SDK/compiler identity and helper hash. Guest boot compatibility is a native assertion, not inferred from the cache filename. Fixed VZ EFI boot loader uses its own new bounded variable store.

## One narrow test and two fixture files

Proposed test: `cmd/oro/cmd_factory_owned_vm_darwin_test.go:TestFactoryOwnedVMProviderLifetime` plus a small `cmd/oro/testdata/factory_owned_vm_owner.swift` fixture. No production factory entry point, platform ledger, generalized provider or existing Colima control is added in this slice. Compile/sign the helper to the private fixture directory before admission; signing and compilation are prerequisites, not run while a VM is live.

Native command, only after explicit implementation/native authorization:

`ORO_FACTORY_OWNED_VM_NATIVE=1 ORO_FACTORY_EVIDENCE_DIR=<persistent-private-parent> GOTOOLCHAIN=go1.26.10 go test -race ./cmd/oro -run '^TestFactoryOwnedVMProviderLifetime$' -count=1 -timeout=420s`

Use the approved direct native lane, not the protected sandbox lane. Preserve HOME/CODEX_HOME. Enabled missing prerequisites fail; no network downloads, skips or existing-profile fallback. Pure helpers/lint remain protected.

## Fixed resources and launch authority

- At most one live VM; at most two sequential boots/helpers. Each VM: 2vCPU, 2GiB RAM, one writable raw disk of exactly3758096384 bytes, no grow/extra disk, no networking, host mounts, SSH forwarding, shared folders, Rosetta, saved state or restore operation. One entropy device and bounded serial output are sufficient. This test proves lifecycle/boot, not Docker availability.
- Reserve8GiB fixture host capacity plus1GiB available margin before writes, using real Statfs and same filesystem. Disk remains finite even if guest fills it. Include image copy/decompression staging, helper/build output, EFI state and logs; reject if their measured aggregate exceeds8GiB. Require a private persistent evidence parent, never a t.TempDir that erases uncertain ownership. Observed host available disk was210701398016 bytes, memory32GiB; remeasure at admission.
- Per helper/VM original absolute lifetime120s; boot readiness<=60s; force-stop completion<=10s; final group retirement<=10s. Total qualification<=360s. These are proposed test budgets, not observed boot measurements; exceeding them fails without silently extending them. Aggregate captured serial/log/protocol data<=16MiB; protocol frames<=16KiB. Drain bounded serial data continuously or stop on overflow, so pipe blocking cannot stall ownership cleanup.
- Exact random launchd label and immutable plist, KeepAlive=false, finite ExitTimeOut, no broad bootout/kill/search-derived ownership. A test-owned launcher retains the child wait handle. To test FD inheritance, it opens/flocks the permanent lifetime inode and passes the descriptor as ExtraFiles to the fixed helper, then closes its own duplicate only after authenticated helper readiness. A separate opener must still fail nonblocking flock while that helper is alive. This tests lock inheritance; it does not establish VZ retirement.
- The helper alone retains the VZ object. Authenticate the private Unix control channel using kernel UID/PID, actual inherited launchd process group and an unguessable epoch nonce. Every reply binds nonce, config digest, helper instance identity, original deadline and the same internally assigned VZ object identity. No arbitrary disk/program/profile/VM ID enters a stop request.

## Successful stop and reacquisition sequence

1. Boot the immutable private raw disk using a valid fixed VZ configuration. Require start completion=nil, state Running and real bounded serial boot progress from the guest/firmware. Keep that object alive; there is no auto-restart or helper relaunch under the same epoch.
2. Kill only the test-owned coordinator process using its retained child handle. The VM owner service survives independently. Start a fresh coordinator, reopen the original manifest and lock inode, authenticate the same live helper/nonce/config/object, and observe Running. It must not spawn another owner or refresh any deadline.
3. Ask the helper to force-stop. It irreversibly marks this object closing before calling `stopWithCompletionHandler:`. No start/resume/restore command exists after closing. On the object's serial queue, successful completion plus Stopped sets a retained terminal result. Reply only after that point.
4. Exercise lost acknowledgment by dropping the actual client connection after the helper has entered stop processing, then reconnect from a fresh coordinator to the same live helper. The returned terminal observation comes from that retained object's successful stop result/state, not a caller assertion, log, PID file or reconstructed object. A STOPPED JSON frame is trusted only through this exact authenticated helper capability.
5. Only after this stop evidence, release the VZ object/device attachments, close the inherited lifetime lock, exit/reap the owned helper, retire the exact launchd job/group and acquire the original inode lock. Record positive process retirement as an additional ownership condition, not the VM-stop proof itself. Frozen closing prevents a stopped object restarting before retirement.
6. Start the second new helper/epoch on the same private raw disk under a new nonce. Prove Running/serial progress again, then leave the coordinator disconnected and require the helper's original absolute timer to invoke the same force-stop primitive. Reconnect and obtain the same successful-stop result without a deadline refresh. Retire it identically.

Controls: pause the VM through the test-owned helper before one stop (the force-stop API also accepts Paused); wrong nonce/config/object, a changed lock inode, or replay to another helper must reject before effects. Stop idempotence never calls start/resume. Keep a read-only initial/final identity inventory for existing Colima profile files; do not stop, enter, reconfigure or connect to that VM in this prototype. Existing unrelated activity can change its logs, so do not equate log-byte drift with interference.

## Explicit remaining boundary

This prototype qualifies **coordinator death, lost stop acknowledgment, bounded noncooperative guest stop and a live independent owner's deadline**. It does not yet qualify death/SIGSTOP of the helper that owns the VZ object, death of all observers, host crash, or durable cross-process retirement after loss of the actual object. A cached receipt, released flock, parent wait, heartbeat silence, disk reopen or launchd absence cannot fill that gap.

If the retained helper's force-stop errors or times out, retain evidence/charge and emit no successful retirement result. Do not automatically boot another VM. A later narrowly scoped native ownership/failure test must identify the actual VZ service lifetime capability or documented kernel contract for that path before claiming unattended recovery from owner failure. The concrete next result can therefore be PASS for the above live-owner capability while that harder required recovery task stays explicitly open; it is not permanent quarantine presented as completed factory behavior.
