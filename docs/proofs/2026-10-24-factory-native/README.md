# Production guard-image native evidence snapshot

Preserved on 2026-10-24 for resumption after an extended pause. These are selected
byte-for-byte copies of historical evidence, not a newly executed qualification.
The source fixture was
`/private/tmp/oro-factory-verify-ldhnhnvl/guard-image-native-73164a1597a3e2e24c8119d060a6ddbf`.

Start with the [resume guide](../../plans/2026-10-24-factory-resume-guide.md).

## What passed and what did not

Commit `bc4dca68` added the actual production image-builder/watchdog native test.
The recorded native test source SHA-256 is
`bf7b660492da04d94e85f808713b864a5115ad3a508c1d0201b931e5dbe055e9`.
It ran against the dedicated `colima-oro-factory` Docker context. The test passed
in 36.15 seconds, package 37.372 seconds, harness 37.633 seconds. It made 45 native
calls: 42 recorded host calls retired, plus three internal builder calls.
Five additional independent read-only calls brought observed calls to 50,
within the fixed 64-call budget.

Observed results include writable seeded source as UID1000, denied attacks on
the UID65534 watchdog, detached-writer progress followed by original-deadline
namespace expiry, exit zero/PID zero/no OOM, unchanged sentinel, and removal of
both owned containers, source volume, and produced image. FinishedAt was about
2.5 milliseconds after the admitted deadline. Existing Colima profiles were not
stopped, restarted, pruned, or made the active context.

This proves the scoped image/watchdog behavior. It does **not** prove production
managed source preparation, real-model delivery, full Oro checks, or automatic
controller/supervisor/VM recovery.

## Preserved files

| File | Purpose |
| --- | --- |
| [result.json](result.json) | Exact test command, historical environment, source hashes, exit and harness time |
| [test.log](test.log) | Native test result and fixture location |
| [native-receipt.json](native-receipt.json) | Aggregate call/resource observations retained by the native fixture |
| [independent.json](independent.json) | Independent absence probes, sentinel/retirement checks, timing and diagnostic summary |
| [call-18.json](call-18.json) | Produced image inspection, including inherited provenance label |
| [call-20.json](call-20.json) | Stopped container inspection showing inherited image label alongside fixture ownership labels |
| [build-receipt.json](build-receipt.json) | Pinned base, guard digest, produced image identity and build label |
| [image-Dockerfile](image-Dockerfile) | Minimal image recipe; the guard executable itself is not archived here |
| [SHA256SUMS](SHA256SUMS) | Hashes for copied evidence and both adjacent research snapshots |

The JSON preserves historical absolute paths; those paths need not exist to read
this snapshot. The complete temporary tree, caches, binaries, individual call
files other than 18/20, and VM state are not archived. The aggregate receipt
retains the recorded host-call observations. Reproduction requires freshly
verified prerequisites and rebuilt/pinned artifacts; do not replay old resource
IDs, old executable paths, or treat an image ID as a retained local image.

## Integration lesson

The native fixture propagated the image's complete labels into its expected
container identity. Its labels differ from production ownership labels. The
production keeper initially expected only its six mandatory ownership labels;
Docker would additionally inherit `dev.getoro.factory.guard-build`. Recording
fixtures did not model this inheritance. The native image test could pass while
the production keeper rejected its real container before READY.

Commit `f95030ca` preserves an unfinished repair that binds the bounded complete
observed map at the acknowledged stopped inspection, then compares that full map
exactly on subsequent operations. See the resume guide for malformed-value,
coverage, review, and actual managed-consumer verification still required.

## Verify preservation

From this directory:

```sh
shasum -a 256 -c SHA256SUMS
```

The adjacent [runtime inventory](../2026-10-24-factory-check-runtime-inventory.md)
and [VM-owner research](../2026-10-24-factory-owned-vm-research.md) are also exact
research snapshots. Their proposed steps were not executed by this archival work.
