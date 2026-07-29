# Cache lifecycle discoveries

**Date:** 2026-07-29
**Component:** Oro subprocess environment, quality gates, and host storage
**Severity:** high

## Symptom

Oro-created caches and temporary files grew without a reliable lifecycle. The
2026-07-19 read-only measurement found **243.5 GiB** across the main affected
roots: 130.0 GiB in `/tmp/oro-subprocess`, 82.8 GiB in
`~/Library/Caches/oro`, 24.8 GiB in `~/Library/Caches/go-build`, and 6.0 GiB
in `~/.oro`. About 145 GiB appeared in a single day, so a seven-day,
age-based prune could not contain the incident.

## Root cause

Worktree execution mixed two categories with opposite lifecycle needs:

- Short-lived worktree scratch was isolated but had no owner, lease, or
  retirement path.
- Reusable tool caches were sometimes placed inside that scratch. In
  particular, tests that replaced `HOME` without an explicit `GOMODCACHE`
  caused Go to create a module cache inside each temporary home. Repeated
  `oro-config-test-*` directories held roughly 680 MiB of modules each.

Quality gates also created per-run cache paths, so correcting only
`processenv.ForWorkdir` would not have covered every writer.

## Discoveries and decisions

1. **Separate scratch from caches.** Worktree scratch remains short and local
   at `/tmp/oro-subprocess/<token>` for Darwin Unix-socket compatibility.
   Reusable caches live outside the worktree and are shared at the appropriate
   user, project, or repository scope.
2. **Set cache locations explicitly.** Go receives external `GOCACHE` and
   `GOMODCACHE` even when `HOME` is replaced. Initial providers also cover uv,
   golangci-lint, npm, and npx. A quality gate may use an ephemeral lint cache
   to prevent stale sibling-worktree diagnostics, while keeping Go and Python
   caches shared.
3. **External does not mean deletable.** Unknown external cache-like paths may
   be observed and reused, but never guessed at or deleted. Tool-owned caches
   use their documented cleanup commands; Oro-owned roots must be canonically
   contained by an explicit allowlist.
4. **Lifecycle beats age.** Each scratch namespace needs a lease, process
   identity, heartbeat, retirement reason, and asynchronous deletion record.
   Retire it at the same safe boundary as worktree/branch cleanup; do not make
   merge completion wait for recursive deletion.
5. **Containment needs admission control.** Global cleanup is coordinated by a
   durable catalog and advisory lock. A host-wide pause drains active
   lease-holders before destructive sweeps; catalog loss or ownership doubt
   fails closed instead of allowing untracked scratch execution.
6. **Bound the failure mode.** The planned thresholds are 256 MiB warning and
   512 MiB stop per namespace, a 2 GiB aggregate target, and a 3 GiB admission
   ceiling. Scratch overflow is typed `storage_limit` evidence, not an
   ordinary test failure.
7. **Use a narrow emergency procedure.** Before manual deletion, prove
   quiescence using process and open-file checks. Delete only named,
   direct-child, non-symlink, user-owned scratch/test/QG roots. Preserve
   unknown `/tmp` paths, durable `~/.oro` state, and worktrees.

## Prevention

- Any new subprocess entry point must use the resolved cache environment and
  participate in runtime lease ownership.
- Any new cache provider must declare its scope, concurrency, ownership, and
  deletion authority. Repository configuration cannot add an arbitrary cleanup
  command or weaken host safety thresholds.
- Run cleanup on startup, hourly, after safe merges and janitor/audit cycles,
  under pressure, and weekly for eligible developer-tool providers. Record
  before/after evidence.
- Treat the session-start worktree cleanup banner as a lead, not deletion
  authority: safely removing a worktree still requires proof that it is
  closed, clean, merged to its target, and unleased.

## Related

- [Full storage lifecycle and shared-cache design](../plans/2026-07-19-storage-lifecycle-and-shared-caches-design.md)
- [Daily disk-containment runbook](../runbooks/cache-cleanup.md)
- `pkg/processenv/env.go` — current cache environment routing
- `pkg/storage` — provider policy, planning, and evidence
