# Storage Lifecycle and Shared Caches Design

**Date:** 2026-07-19
**Status:** Validated design; consultation and adversarial review pending

## Summary

Oro must stop treating disposable storage as an unbounded side effect of task execution. It will isolate only worktree scratch data, reuse caches that are external to a worktree, retire task-owned storage at the same lifecycle boundary as worktree and branch cleanup, and run bounded global maintenance on startup, hourly, after merges, after janitor or audit cycles, under disk pressure, and weekly.

The design has four ordered cleanup concerns:

1. Eliminate and migrate the legacy per-worktree cache tree at `~/Library/Caches/oro/subprocess`, and lifecycle-manage `/tmp/oro-subprocess`.
2. Remove eligible local worktrees plus their safe local and remote branches.
3. Clean only an explicit disposable allowlist under `~/.oro`; indexes are always preserved.
4. Run a reusable weekly developer-tool cache sweep with durable before/after proof.

The core cache rule is repository-agnostic:

> Oro isolates worktree scratch, but preserves and reuses every cache that is external to the worktree.

Go, uv, golangci-lint, and npm are initial cache providers, not hardcoded architectural special cases. The provider contract can represent other ecosystems used by non-Oro repositories.

## Incident Evidence

Read-only measurements during the 2026-07-19 incident found:

| Root | Allocated size |
|---|---:|
| `~/Library/Caches/oro` | 82.8 GiB |
| `~/Library/Caches/go-build` | 24.8 GiB |
| `~/.oro` | 6.0 GiB |
| `/tmp/oro-subprocess` | 130.0 GiB |
| **Total** | **243.5 GiB** |

The largest token-paired runtime namespace was 24.0 GiB: 4.54 GiB of isolated Go build cache and 19.4 GiB of temporary data. The temporary data was dominated by repeated `oro-config-test-*` directories of roughly 0.6–0.7 GiB each. Inspection showed about 680 MiB of Go modules under each temporary HOME's `go/pkg/mod`. This occurred because a test replaced `HOME`, and an absent explicit `GOMODCACHE` caused Go to derive a new module cache inside the temporary HOME.

The incident invalidates age-only retention as the primary control. Roughly 145 GiB of factory storage appeared within one day, while the existing manual cache cleanup retained namespaces for seven days.

## Prior Art and Constraints

### Repository evidence

- `pkg/processenv/env.go` currently hashes a worktree path into a token and creates per-token Go, golangci-lint, uv, and temporary directories. It has no owner, lease, or cleanup lifecycle.
- `pkg/processenv/cache_cleanup.go` prunes only the user cache root by directory age. It does not cover `/tmp`, byte budgets, live owners, or disk pressure.
- `cmd/oro/cmd_cleanup.go` exposes that prune only through a broad interactive cleanup command.
- `pkg/dispatcher/sweeper_loop.go` already provides periodic dispatcher maintenance ticks.
- `pkg/dispatcher/dispatcher.go` has an authoritative post-merge boundary that removes worktrees and local branches. Storage retirement belongs at that same boundary.
- `pkg/dispatcher/worktree_manager.go` already contains safe ancestry-aware worktree and local branch cleanup patterns.
- `scripts/quality_gate.sh` and the generated quality-gate template explicitly create per-run Go, uv, and golangci-lint caches. Changing `processenv.ForWorkdir` alone is insufficient.
- `pkg/testutil/configenv/configenv.go` replaces `HOME` in tests; shared cache variables must be explicit before that replacement can cause tool defaults to move into temp.
- `docs/decisions&discoveries.md` documents a stale `/tmp/oro-subprocess` hook-path incident and the need for hermetic test environments.

### Tool evidence

- The Go command documents that its build cache is safe for concurrent invocations and correctly keys source, compiler, and option inputs. It also periodically trims unused data.
- uv documents its cache as thread-safe and append-only. `uv cache prune` is safe to run periodically and honors in-use locking unless forced.
- golangci-lint defaults to one cache under the user cache directory and exposes status and clean operations.
- Tool-native cleanup is safer than directly removing a cache whose internal locking and reachability rules Oro does not own.

### Platform constraint

Darwin Unix sockets have a short path limit. Oro must continue to provide a short `/tmp/oro-subprocess/<token>` path for `TMPDIR`, `TMP`, and `TEMP`. Moving worktree scratch beneath the much longer user cache path would regress socket-using tests and tools.

## Goals

- Prevent Oro from exhausting the host filesystem through cache or temporary-data growth.
- Share external caches across sibling worktrees and projects according to provider scope.
- Remove task scratch promptly after merge or other safe retirement.
- Bound maintenance work so the dispatcher hot path never scans an entire large root.
- Coordinate cleanup across every Oro dispatcher on the host.
- Clean worktrees and owned local or remote branches only with proof that deletion is safe.
- Provide a reusable, scheduled developer-tool cache sweep with exact before/after evidence.
- Preserve all indexes and all unknown or durable state under `~/.oro`.
- Work for arbitrary repositories and toolchains rather than assuming Oro builds only itself.

## Non-goals

- Oro will not infer that an arbitrary external directory is disposable.
- Oro will not execute arbitrary repository-provided cleanup commands during global maintenance.
- Oro will not delete unmerged, dirty, protected, user-owned, advanced, or ownership-uncertain refs or worktrees.
- Oro will not promise that every third-party tool cache is concurrency-safe. Providers declare concurrency requirements.
- Oro will not synchronously delete large trees on the merge completion path.
- This design does not clean the current machine as part of specification work; it defines the product behavior that safely performs cleanup.

## Architecture

### Global storage manager

A new global storage manager coordinates disposable storage for all Oro projects and processes on the host. Its durable SQLite catalog and advisory lock live under `~/.oro` and are explicitly excluded from cleanup.

The catalog records:

- cache provider definitions and resolved roots;
- project and repository identities;
- worktree runtime namespace token and canonical worktree path;
- namespace canonical path, lifecycle state, estimated bytes, and last observation;
- active leases with process identity and heartbeat;
- retirement reason and timestamps;
- tombstone deletion state and retry schedule;
- managed worktree and branch cleanup state;
- weekly sweep due time and completed-run evidence;
- incremental legacy-reconciliation cursors.

Only one process performs global maintenance at a time using an OS advisory lock. Advisory ownership disappears automatically when a process exits. Normal lease and lifecycle transactions are short and do not wait for a recursive deletion pass.

Catalog loss or corruption puts automated deletion into preservation mode. A bounded reconciler can rebuild known records, but absence of a record is never treated as deletion authority.

### Worktree runtime leases

`processenv` must acquire a runtime handle, not merely return an environment slice. The handle represents the worktree's short temp namespace and must be released after the spawned command exits. Workers, quality gates, reviewers, hooks, and dispatcher commands that use worktree scratch all hold leases.

A lease includes a process identity stronger than PID alone, such as PID plus observed start time and Oro owner identity. Heartbeats allow stale-lease recovery. Oro expires a lease only after proving that its owning process identity no longer exists.

The runtime handle resolves shared cache environment variables before returning the final subprocess environment. Existing call sites of `processenv.ForWorkdir` must migrate to an acquire/release API or to a spawner wrapper that owns the handle for the child lifetime.

## Scope-based Shared Cache Contract

The cache abstraction is based on storage scope, not language. A `CacheProvider` describes:

- stable provider ID;
- environment variable names;
- tool-standard default resolver;
- sharing scope: user, project, or repository;
- concurrency mode: concurrent, serialized, or no automated maintenance;
- ownership: tool-native or Oro-managed;
- optional status operation;
- optional tool-native prune or clean operation;
- whether absence of the tool is a valid skip.

Initial built-in providers cover:

- Go build, test, fuzz, and module caches;
- uv;
- golangci-lint;
- npm and npx.

The same interface must support Cargo, Gradle, Maven, pnpm, pip, and other ecosystems without dispatcher changes.

### Environment resolution

For every registered cache variable, Oro resolves storage in this order:

1. Preserve an explicit absolute path already outside both the worktree and worktree temp namespace.
2. If the path points inside either boundary, redirect it to the provider's external shared location.
3. If the variable is absent, use the provider's tool-standard external default.
4. Preserve unknown external state unchanged.
5. Report an unknown cache-like path inside a worktree; do not guess its semantics, move it, or later delete it.

In particular, Go must receive explicit external `GOCACHE` and `GOMODCACHE` values even when a caller replaces `HOME`. This prevents tool defaults from silently relocating module data into an isolated temporary HOME.

Generated and checked-in quality gates must inherit this resolved environment. They must not create per-run Go, uv, lint, or equivalent caches beneath `QG_DIR` or `TMPDIR`.

### Provider trust and deletion authority

External means reusable, not deletable. Oro may delete or invoke cleanup only when a provider supplies explicit authority:

- Tool-native caches are cleaned through fixed executable and argument vectors supported by the tool.
- Oro-managed provider roots must resolve beneath a canonical allowlisted Oro cache root.
- Repository configuration may declare cache variables, path templates, and scope, but may not introduce automatic cleaner commands.
- Custom cleaner commands are accepted only from trusted user-level configuration.
- Unknown external caches are observable and reusable but never automatically deleted.

A provider can require global idleness or serialization before status or cleanup. One provider failure does not prevent subsequent providers from running.

## Worktree Scratch Lifecycle

The sole worktree runtime namespace is `/tmp/oro-subprocess/<token>`, where the token is a deterministic hash of the canonical worktree path. All subprocesses for that worktree share the same lease-protected namespace.

### Limits

- Per-namespace warning: 0.25 GiB (256 MiB).
- Per-namespace stop threshold: 0.5 GiB (512 MiB).
- Aggregate Oro-managed temp target: 2 GiB.
- Aggregate admission ceiling: 3 GiB.

These limits apply only to actual worktree scratch. Shared caches and Git worktree checkout bytes are measured separately.

At the warning threshold, Oro emits a finding and samples usage more frequently. At the stop threshold, it prevents new subprocesses in that namespace and requests graceful cancellation of the active writer. After 30 seconds, Oro terminates a writer that is still running or growing. Under critical host pressure, the grace period is skipped. The worktree and diagnostic evidence are preserved, and the attempt is classified as `storage_limit` rather than as an ordinary test failure.

Before enforcement is enabled by default, an acceptance run must prove that a normal full quality gate stays below 0.5 GiB of scratch after all shared-cache routing is active. A project can explicitly configure a higher limit; overrides are logged and never silently inferred.

### Retirement and deletion

Successful or no-op merge marks the namespace retired at the same dispatcher boundary that starts safe worktree cleanup. Cancellation and proven abandoned-worktree cleanup can also retire it.

The asynchronous cleanup worker:

1. claims the catalog record;
2. revalidates that no live lease exists;
3. verifies the target is a direct child matching Oro's token format;
4. rejects symlinks, containment failures, ownership uncertainty, or catalog/path disagreement;
5. renames the directory into a root-local tombstone;
6. removes the tombstone recursively;
7. records bytes reclaimed or durable retry state.

Interrupted tombstone deletion resumes on a later cycle. Recursive deletion never blocks merge completion and cleanup failure never reverses merge success.

Cleanup priority is:

1. retired namespaces;
2. interrupted tombstones;
3. recognized legacy namespaces;
4. abandoned unleased namespaces older than 24 hours;
5. oldest unleased namespaces until the 2 GiB target is restored.

## Cleanup Triggers and Pressure

Cleanup is triggered:

- on dispatcher startup, including missed-schedule catch-up;
- after every successful or no-op merge;
- hourly;
- after every janitor or audit cycle;
- before new assignment or quality-gate admission when pressure is elevated;
- immediately on critical pressure;
- weekly for the developer-tool provider sweep.

The janitor is a trigger, not the owner of cleanup. This prevents maintenance starvation when there are no merges, janitor is disabled, or the factory cannot reach its first successful merge.

A cheap filesystem-space probe runs before admissions and on the maintenance cadence:

- Warning pressure: free space below the greater of 10% of filesystem capacity or 50 GiB.
- Critical pressure: free space below the greater of 5% of capacity or 20 GiB.

Warning pressure prioritizes reclamation. Critical pressure pauses admissions, reclaims all safe unleased data, runs eligible cache providers early using the idle/drain policy, and cancels active temp writers if necessary. Cleanup scans, size observations, and deletion work are time-, entry-, and byte-bounded per cycle.

## Worktree and Branch Policy

### Worktrees

- After successful or no-op merge, remove the Oro-registered worktree once all leases are released.
- Recurring cleanup may remove an Oro-registered worktree only when its task is closed or cancelled and its branch is proven merged into the target.
- Dirty, leased, unmerged, unregistered, or ownership-uncertain worktrees are preserved and produce an actionable finding.
- Oro never deletes arbitrary unregistered worktrees.
- `git worktree prune` runs only after managed worktree removals.

### Local branches

A local branch is deleted only after its worktree is removed, no lease remains, and its exact tip is an ancestor of the local target branch. Safe lowercase branch deletion semantics are required; force deletion is outside automated policy.

### Remote branches

A remote branch is eligible only when Oro created and owns it. Oro must fetch current refs, prove the exact branch tip is contained in the remote target, reject protected/default/user-owned refs, and compare the expected remote SHA immediately before deletion. A remotely advanced ref is preserved. Transient failures are retried, and remote cleanup never changes merge success.

The weekly cycle retries eligible leftover worktrees and local or remote branches.

## Weekly Developer-tool Sweep

The global catalog stores the weekly due time. Oro waits for a globally idle Oro window. If no idle window occurs within 24 hours after the sweep becomes due, Oro pauses new admissions, drains active Oro work, and runs the sweep. Oro cannot prove that unrelated non-Oro processes are idle, so operator documentation must state that boundary.

The initial built-in sweep performs independently evidenced provider steps equivalent to:

```sh
go clean -cache
go clean -modcache
go clean -fuzzcache
uv cache prune
golangci-lint cache clean
npm cache clean --force
# guarded removal equivalent for ~/.npm/_npx
```

Implementation must not execute a concatenated shell script. It resolves known executables and fixed argument vectors, enforces timeouts, and records each exit status. The npx path is canonicalized, checked against the current user's npm root, rejected if symlinked or uncertain, and removed through the same guarded filesystem primitive as other Oro-owned deletion.

Before and after each run, Oro obtains exact filesystem free bytes using OS APIs. It also records per-provider size when supported. Human output ends with the equivalent of:

```text
Freed N GiB — now M GiB free
```

JSON retains exact byte values, provider results, durations, and skips. Disk pressure may request the same sweep early; the same idle/drain and evidence rules apply.

## `~/.oro` Strict Allowlist

Oro never broadly age-prunes `~/.oro`. It cleans only:

- worker and hook logs: retain seven days, capped at 512 MiB total;
- rendered handoffs: retain 30 days and always keep the newest ten per project;
- database recovery backups: retain the newest three per database plus everything created within seven days;
- known Oro temporary files: remove after 24 hours when unleased;
- inactive SQLite WALs: checkpoint through SQLite APIs, never delete directly.

Oro always preserves:

- every index and index database;
- live databases and WALs;
- models;
- configuration;
- task data;
- memories and cards;
- catalog and maintenance state;
- active recovery evidence;
- every unknown path.

The cleanup planner exposes which allowlist rule authorized every candidate.

## Operator Surface

### Status

`oro storage status [--json]` reports:

- filesystem capacity and free space;
- current pressure state;
- aggregate and per-namespace temp usage;
- provider cache roots and status where supported;
- catalog and reconciliation health;
- active leases;
- retired and tombstoned backlog;
- managed worktree and branch cleanup backlog;
- `~/.oro` allowlist usage;
- last and next weekly sweep.

### One-shot cleanup

`oro storage clean --scope <runtime|worktrees|oro-home|dev-tools|all> [--dry-run] [--json]`

- Omission of `--apply` defaults to dry-run.
- `--apply` authorizes the already proven candidates; it never bypasses ownership, containment, lease, ancestry, remote-SHA, or provider rules.
- `oro storage clean --scope dev-tools --apply` is the reusable equivalent of the requested developer-tool shell one-shot.
- Scheduled maintenance and the CLI invoke the same planner and executor.

## Evidence and Health

Each cleanup run receives a durable ID and records:

- trigger and policy version;
- thresholds and pressure state;
- candidates and preserve/delete decisions;
- ownership and containment proof;
- commands and fixed arguments without secrets;
- exit codes and timing;
- before/after sizes and filesystem free bytes;
- retry and incident identifiers.

Factory health surfaces:

- overdue weekly sweeps;
- catalog corruption or reconciliation failure;
- blocked retirement;
- repeated cleanup failures;
- excessive namespace growth;
- active-writer cancellation;
- admission pause and resume;
- worktree or branch preservation reasons.

Repeated identical failures deduplicate into one incident. Full environments, tokens, credentials, and secrets are never persisted in evidence.

## Failure Handling

- Item and provider failures are independent.
- Failed namespace deletion remains tombstoned and retryable.
- Failed worktree or branch cleanup leaves merge success intact.
- Failed provider cleanup does not prevent later providers.
- Missing tools are successful skips when the provider allows absence.
- Backoff is bounded and recurring failure becomes a durable health finding.
- Symlink, containment, lease, ownership, dirty-state, ancestry, protected-ref, or expected-SHA uncertainty always results in preservation.
- Catalog corruption disables automated deletion until conservative reconciliation restores authority.
- Pressure mode never weakens deletion proofs.

## Legacy Migration

`~/Library/Caches/oro/subprocess` becomes legacy-only; new processes no longer write per-token caches there. On first upgraded startup, Oro:

1. checks for older live Oro processes that do not participate in the new lease protocol;
2. begins bounded discovery of direct token children in the old cache and temp roots;
3. pairs records by token when possible;
4. preserves anything with a possible live owner;
5. immediately prioritizes safely unowned recognized legacy content until headroom and targets are restored;
6. persists the reconciliation cursor and proof for restart.

The migration is safe to repeat. It never traverses arbitrary paths supplied by directory contents and never treats an unknown child as disposable.

## Rollout

Rollout is staged so observation precedes enforcement:

1. Add the catalog, provider model, storage status, and dry-run planner.
2. Route cache environments through shared external providers and remove per-run quality-gate cache injection.
3. Add temp leases, post-merge retirement, bounded deletion, and legacy reconciliation.
4. Add worktree and local/remote branch retry cleanup.
5. Add pressure findings and admission pause without active cancellation.
6. Prove normal quality-gate peak scratch remains below 0.5 GiB, then enable active enforcement.
7. Add `~/.oro` allowlist cleanup.
8. Add weekly provider scheduling, overdue drain, and before/after proof.

Every stage exposes status and dry-run output. Shared caches are rebuildable, and shared-cache routing can be disabled through trusted user configuration during rollback. Deleted legacy caches require rebuild but contain no durable state.

## Testing Strategy

### Cache resolution

- Preserve every registered external path.
- Redirect registered cache paths inside a worktree or temp namespace.
- Resolve absent cache variables to external provider defaults.
- Explicitly provide external `GOCACHE` and `GOMODCACHE` when `HOME` is replaced.
- Report but do not relocate or delete unknown cache-like paths.
- Verify provider user/project/repository scoping.
- Verify generated and checked-in quality gates do not invent per-run caches.
- Run conflicting Go and uv worktrees concurrently and prove correct outputs and cache reuse.

### Runtime lifecycle

- Lease acquire/release and last-lease retirement.
- PID reuse and process-start identity verification.
- Crash recovery and stale-lease handling.
- Successful/no-op merge retirement.
- Cancellation and abandoned namespace handling.
- Tombstone restart and partial deletion.
- 0.25/0.5 GiB namespace policy.
- 2/3 GiB aggregate policy.
- Admission pause/resume and active cancellation.
- Symlink, traversal, ownership mismatch, and injected `ENOSPC` preservation.

### Scale and migration

- Synthetic roots with at least 100,000 children.
- Prove bounded memory, entries, wall time, and deletions per cycle.
- Prove a live legacy process blocks migration cleanup.
- Prove cursor persistence and restart.
- Prove unknown legacy children remain untouched.

### Worktrees and refs

- Clean merged worktree removal.
- Dirty, leased, unmerged, and unregistered preservation.
- Local target ancestry proof.
- Remote target containment and protected-ref rejection.
- Expected-SHA compare-and-delete failure after remote advancement.
- Transient retry without changing merge result.

### Weekly sweep and `~/.oro`

- Fake executables prove exact command arguments and timeouts.
- Idle-window waiting and 24-hour overdue admission drain.
- Missing-tool skips and provider-failure isolation.
- Guarded npx target validation.
- Exact before/after free-space evidence.
- Indexes, databases, models, configuration, unknown files, and active WALs are never deleted.
- Retention counts, ages, and caps are deterministic.

### Epic acceptance

Run a full quality gate from multiple sibling worktrees and prove:

1. shared cache paths are external and equal where provider scope requires;
2. cache hits occur across worktrees without correctness contamination;
3. peak worktree scratch stays below 0.5 GiB;
4. merging a task retires and removes its exact temp namespace;
5. its worktree and eligible local branch are removed;
6. its remote branch is deleted only after the remote target contains the expected tip;
7. legacy reconciliation makes bounded progress without touching unknown paths;
8. the weekly sweep produces complete per-provider and before/after evidence;
9. all indexes under `~/.oro` remain byte-for-byte present.

## Alternatives Rejected

### Age-based filesystem sweeps

Lower implementation cost, but the incident grew roughly 145 GiB in one day while the existing threshold was seven days. Root scans over more than 100,000 directories are too slow for the dispatcher hot path, and age cannot identify active ownership.

### External cron or launchd only

Useful as an optional operator wrapper around the one-shot, but it lacks merge lifecycle, leases, pressure admission, worktree/ref proof, and reliable startup catch-up. Weekly cleanup alone cannot prevent a one-day exhaustion event.

### Per-worktree reusable caches with faster deletion

Lifecycle deletion would bound long-term retention but still multiplies identical caches across concurrent tasks. The observed 4.54 GiB isolated Go cache makes the 0.5 GiB scratch ceiling impossible and wastes compilation and download work.

### Heuristic deletion of all cache-looking paths

This could mistake credentials, durable tool state, or mutable environments for disposable cache. Unknown external paths are reused and observed but require an explicit trusted provider before cleanup.

## Resolved Premortem

### Tigers

- **Cleanup races an active worker or quality gate.** Mitigation: process-identity leases, final revalidation, and preservation on uncertainty.
- **A full scan stalls the dispatcher.** Mitigation: global catalog, incremental reconciliation cursor, and strict per-cycle bounds.
- **Broad `~/.oro` cleanup destroys durable state or indexes.** Mitigation: explicit allowlist, indexes permanently excluded, unknown paths preserved.
- **Per-worktree caches continue through quality-gate scripts.** Mitigation: change both process environment resolution and generated/checked-in QG templates; acceptance inspects final child environments.
- **A temporary HOME recreates module caches in scratch.** Mitigation: explicitly resolve `GOMODCACHE` externally even when absent.
- **Shared mutable caches contaminate sibling worktrees.** Mitigation: provider concurrency declaration, tool-native guarantees, serialized providers where required, and conflicting-worktree integration tests.
- **An active writer fills the disk while leases correctly prevent deletion.** Mitigation: namespace monitoring, admission stop, graceful cancellation, critical immediate cancellation.
- **Remote branch advances after eligibility check.** Mitigation: fresh fetch and expected-SHA compare-and-delete.
- **Weekly full cache cleanup races active builds.** Mitigation: global Oro idle window, 24-hour overdue drain, provider-native locking, and documented non-Oro process boundary.

### Elephant

- **Janitor-only cleanup can starve before the first merge or when janitor is disabled.** Mitigation: janitor is only one of startup, hourly, post-merge, pressure, and weekly triggers.

### Paper tigers

- **Deleting inactive content-addressed caches changes build correctness.** Go and uv caches are rebuildable and content-addressed; deletion costs latency, not source-state loss. Mutable or undocumented providers do not receive automatic cleanup authority.
- **Removing legacy per-worktree caches loses durable project state.** The old root contains tool caches by construction. Migration still requires recognized format, no live owner, canonical containment, and dry-run proof.

## Load-bearing Assumption

The design assumes that provider-declared shared caches are either documented safe for concurrent use or are serialized by Oro. If a provider's cache correctness depends on hidden worktree-local state, that provider must use project/repository scope or opt out of shared routing and automated cleanup.

## Expected Implementation Areas

- `pkg/processenv`: runtime acquisition, cache resolution, shared provider environment, legacy compatibility.
- New storage package under `pkg/`: catalog, advisory coordination, leases, planner, executor, reconciliation, pressure policy, evidence.
- `pkg/dispatcher`: startup/hourly/janitor/post-merge/pressure triggers, admission control, lifecycle events, active cancellation.
- `pkg/dispatcher/worktree_manager.go`: recurring safe worktree and local/remote branch retirement integration.
- `pkg/factoryhealth`: storage findings and incidents.
- `cmd/oro`: `oro storage status` and `oro storage clean`.
- `scripts/quality_gate.sh` and `cmd/oro/quality_gate_gen.go`: inherit shared caches and remove per-run cache isolation.
- `pkg/testutil/configenv`: explicit shared cache preservation under temporary HOME.
- Configuration schema and docs: cache providers, thresholds, trusted custom cleaners, operator runbook.
