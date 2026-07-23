# Storage Exhaust Containment Corrective Design

**Date:** 2026-07-22

**Status:** Proposed corrective design

**Supersedes:** implementation sequencing and incomplete ownership assumptions in `2026-07-19-storage-lifecycle-and-shared-caches-design.md`; its preservation, containment, and provider-authority rules remain normative where this document does not narrow them.

## Purpose

Oro must stop creating storage that it cannot account for, retire, or safely reclaim. The immediate incident is not one cleanup defect. It is four coupled lifecycle failures:

1. `/private/tmp/oro-subprocess` is about 18.5 GiB with at least 65,533 direct namespace directories because `processenv.ForWorkdir` creates a deterministic namespace for every non-empty workdir without acquiring an owner lease or scheduling retirement.
2. Other top-level `/private/tmp/oro*` directories total about 23.8 GiB. Some are old test homes containing module caches; others are quality-gate or diagnostic residues. Their names alone do not prove Oro owns their contents.
3. `~/Library/Caches/go-build` reached about 130 GiB because one cache provider assigns both `GOCACHE` and `GOMODCACHE` the same `go-build` default. Concurrent Oro builds also caused `go clean -cache` to fail partway with `unlinkat ... directory not empty` while new files were being created.
4. Worktree cleanup is split between `git worktree prune`, a closed-task directory scan limited to `.worktrees`, and an unfinished catalog design. Open, dirty, leased, externally located, or unmerged worktrees are not represented by one retirement decision.

The fix is a single admission-to-retirement contract. No worktree-scoped process may create scratch before Oro records ownership. No cleanup may infer ownership from a basename. No shared-cache maintenance may start until all live Oro controllers have acknowledged a drain and their owned process groups have exited.

This is a product design, not authorization to clean the current machine or merge existing prototype branches.

## Evidence and Current Failure Paths

### Untracked runtime creation

`pkg/processenv/env.go:ForWorkdir` calls `runtimeToken`, creates `/tmp/oro-subprocess/<token>` with `os.MkdirAll`, and returns `TMPDIR`, `TMP`, and `TEMP`. The function returns only an environment slice, so callers cannot release ownership. Its cache-resolution error path silently returns the original environment, while later namespace creation still proceeds. There are production callers across worker, dispatcher, ops, merge, janitor, code search, agent runtime, and CLI execution.

The token root contains live namespaces as well as stale ones. Detached intake-test workers with PPID 1 still have working directories and temp namespaces after their dispatcher socket disappeared. A reconciler therefore cannot equate “old”, “unleased in the new catalog”, or “test-looking” with dead.

### Legacy reconciliation prototype violates its intended bound

The unmerged `agent/oro-run-reconcile` prototype recognizes 16-character hexadecimal names, but calls `os.ReadDir(root)`, which materializes the entire root before applying its visit counter. It also records recognized directories as released without first proving the possible owner dead, and uses one constant cursor key for multiple roots. Each choice contradicts the prior design's 1,024-visit bound and preservation rule.

### Cache-provider aliasing and incomplete maintenance

`pkg/storage/builtin_providers.go:BuiltinProviders` currently declares one `go` provider with variables `GOCACHE` and `GOMODCACHE` and one default path, `~/Library/Caches/go-build`. `pkg/storage/cache_resolver.go` applies a provider default to every variable it owns, so module downloads accumulate inside the build cache. Normal Go defaults keep these roots distinct.

The provider advertises `go clean -cache -modcache -fuzzcache`, but the current storage command only plans catalog decisions; it does not execute provider maintenance. Current status reports the managed catalog root rather than measuring each resolved provider path, so it can report zero cache bytes while the real Go cache consumes more than 100 GiB.

`go clean -cache` is not a host-wide concurrency barrier. A paused dispatcher may still have active assignments, and non-Oro Go processes are outside Oro's authority. A successful destructive provider sweep requires a completed Oro drain and must disclose the residual risk from unrelated host processes.

### Worktree retirement lacks a complete proof

`pkg/dispatcher/worktree_manager.go:GCClosedWorktrees` enumerates only `.worktrees`, consults only task closure, then calls removal and branch deletion. Startup `git worktree prune` removes stale Git metadata, not registered worktree directories. Registered worktrees outside `.worktrees` are invisible to this scan. The session-start cleanup banner is ancestry-based advice, not a retirement decision, and currently advertises open, dirty worktrees.

The required proof is conjunctive: registered by Oro, task terminal, branch merged into the recorded target, worktree clean, no live runtime lease, no recovery quarantine, and exact path/ref identity unchanged at execution time.

### Test-owned detached process leak

`cmd/oro/autonomous_work_intake_integrity_e2e_test.go` launches an external worker through `oro worker launch`, which creates a detached process group. Harness shutdown cancels the dispatcher and kills only `managedWorkerID`; it never stops `externalWorkerID`. These workers survive their tests and keep possible-live ownership evidence indefinitely.

## Goals

- Prevent new unowned runtime namespaces and cache paths.
- Bound each maintenance cycle by entries visited, directories removed, and bytes removed.
- Preserve active or ownership-uncertain processes, paths, worktrees, and refs.
- Distinguish recurrence prevention from one-time legacy adoption and cleanup.
- Make status and dry-run describe the real filesystem roots, owners, blockers, and planned actions.
- Ensure every Oro-launched detached process has a registered process-group identity and a deterministic stop/wait path.
- Make worktree retirement identical at post-merge, startup, hourly maintenance, standalone completion, and manual cleanup.
- Provide one main-branch end-to-end acceptance command that fails if components exist but are not wired.

## Non-goals

- Automatically deleting arbitrary `/private/tmp/oro-*` directories based on their name.
- Proving that all non-Oro Go, npm, uv, or lint processes on the host are idle.
- Treating caches, worktrees, branches, task state, or logs as interchangeable storage classes.
- Cleaning the current host as part of this specification.
- Merging the existing `agent/oro-heg8`, `agent/oro-run-reconcile`, `oro-run-command`, or worktree-retirement prototypes without revalidation against this design.

## Approaches Considered

### A. Admission-to-retirement control plane (recommended)

Every scratch namespace and detached process group is admitted through the storage catalog before filesystem creation. Handles own leases through child exit; retirement and reconciliation consume the same ownership records. Cache providers resolve each environment variable independently, and maintenance requires a drain epoch. Worktrees use a separate proof record but the same live-lease evidence.

This approach addresses recurrence, crash recovery, cleanup safety, and observability together. It costs a broad call-site migration, but that migration is the actual safety boundary; retaining an environment-only helper would leave an untracked creation path.

### B. Age/size janitor over known prefixes

A recurring janitor could delete old or large `oro*` paths. It is simpler and would reclaim today's disk quickly, but it cannot distinguish the live detached test workers, dirty worktrees, user-created lookalikes, or recent 100+ GiB growth. Age and basename are useful ordering signals only after ownership and liveness are proven.

Rejected as an automatic deletion policy. Retained only for reporting and candidate prioritization.

### C. Per-process temporary directories removed on exit

Each subprocess could receive a unique temp directory and defer its removal. This improves attribution during an ordinary exit, but SIGKILL, crashes, and detached descendants still leak. It also multiplies directories and cannot coordinate shared worktree limits, worktree retirement, or provider maintenance.

Rejected as the primary model. Unique child subdirectories may be used inside a leased worktree namespace when a tool requires them, but the parent handle remains authoritative.

## Chosen Architecture

### 1. Fail-closed runtime admission

Replace environment-only worktree scratch creation with an acquired runtime handle:

```go
type RuntimeHandle interface {
    Env() []string
    Namespace() string
    ProcessGroupStarted(ProcessIdentity) error
    ProcessGroupExited(ProcessIdentity, ExitOutcome) error
    Close() error
}

func AcquireRuntime(ctx context.Context, req RuntimeRequest) (RuntimeHandle, error)
```

`RuntimeRequest` contains the canonical worktree path, project identity, controller identity, purpose (`worker`, `reviewer`, `quality_gate`, `hook`, `janitor`, `acceptance`, or `command`), and resolved host policy. Acquisition performs, in order:

1. canonicalize and containment-check the worktree path;
2. resolve every shared-cache variable or fail;
3. open the catalog and verify admission epoch `open`;
4. transactionally create or refresh the namespace ownership record and lease;
5. create the namespace with no symlink traversal;
6. return the environment and handle.

If catalog access, path resolution, or cache resolution fails, no namespace is created and no child starts. There is no compatibility fallback to `ForWorkdir`. The old function may temporarily remain only as a test-detectable error shim; production compilation must contain zero calls before the runtime epic can pass.

The common spawner wrapper owns the handle across `Start`, start failure, `Wait`, cancellation, graceful termination, forced termination, and descendant process-group exit. Direct hooks and scripts enter through `oro storage exec --workdir ... -- <argv>`, which provides the same lifecycle and preserves the child's exit code.

### 2. Process identity and detached-child ownership

A process identity is PID plus start time, executable identity, controller ID, and process-group ID. PID alone is insufficient. Every detached Oro child is registered before its launcher reports success. The owner must stop the entire process group, confirm exit, and release the record on every terminal path.

Test harnesses must register cleanup before launch and call the public stop/wait boundary for every external worker they create. Test cleanup failure is a test failure, not a best-effort log. A bounded emergency cleanup may terminate a process group only when its exact registered identity still matches.

### 3. Runtime retirement and bounded reconciliation

Normal retirement is event-driven: successful/no-op merge, standalone terminal completion, proven cancellation, or abandoned-worktree retirement marks the namespace retired after all process groups exit. Deletion uses a tombstone rename within the same filesystem, then asynchronous removal.

Legacy reconciliation is migration-only and cannot manufacture proof that an old directory is dead. Each root has an independent durable cursor keyed by canonical root identity. One cycle:

- streams directory entries; it must not call an API that materializes the whole root;
- visits at most 1,024 entries;
- removes at most 256 directories or 1 GiB, whichever comes first;
- accepts only strict recognized layouts beneath an allowlisted canonical root;
- preserves unknown children and leaves their SHA-256 manifest unchanged;
- checks matching live process identities, open files when supported, registered worktrees, and active catalog leases;
- preserves on any ambiguity and emits the blocker;
- persists the cursor even when no deletion is authorized.

A 16-hex basename is a format hint, not sufficient ownership. A legacy namespace becomes deletable only through one of these proof paths:

1. its canonical worktree is recorded and retired, with no matching live owner; or
2. a versioned Oro ownership manifest inside the namespace validates, and its recorded process identity is proven dead; or
3. an operator explicitly adopts the exact canonical path after dry-run, without wildcard expansion.

Top-level `/private/tmp/oro-*` paths without a valid manifest are reported as unmanaged candidates and never automatically adopted.

### 4. Independent cache-variable providers

Provider ownership is per environment variable and resolved path, not per tool executable. Go therefore has distinct providers:

- `go-build`: owns `GOCACHE`; default is the output of a controlled `go env GOCACHE` resolution or the platform user-cache equivalent; cleaner authority is `go clean -cache -fuzzcache`.
- `go-module`: owns `GOMODCACHE`; default is the output of controlled `go env GOMODCACHE` resolution or `$GOPATH/pkg/mod`; cleaner authority is `go clean -modcache`.

The resolver rejects duplicate ownership of one environment variable. Two variables may not inherit one provider path merely because the same executable maintains them. Replaced `HOME` never changes these values after policy resolution. If the real user home/cache roots cannot be resolved, worktree-scoped admission fails closed instead of falling back into temporary HOME or `/tmp`.

Status measures each resolved provider root and reports logical and deduplicated physical bytes where roots overlap. It must show path, source of resolution, ownership class, maintenance capability, last successful sweep, and current blockers. A provider advertised in status must be executable by the maintenance runner or explicitly reported `not_implemented`; metadata alone may not imply a functioning cleaner.

### 5. Drain-gated provider maintenance

Manual or scheduled destructive provider maintenance uses a global epoch:

1. request `pause_requested` with a unique epoch;
2. every live Oro controller stops new admissions;
3. controllers cancel or wait for all owned process groups, release leases, and acknowledge `paused` for that epoch;
4. coordinator revalidates controller process identities and the absence of Oro leases;
5. run one provider-native cleaner at a time with before/after evidence;
6. record partial failure and continue only where providers are independent;
7. reopen admission with a new epoch.

An ordinary dispatcher “pause” that leaves active assignments is not a maintenance acknowledgement. Missing acknowledgements block the sweep. The CLI warns that non-Oro host processes remain outside this proof. The Go cleaner's `directory not empty` race is a recorded `concurrent_writer` failure and must never be reported as success.

### 6. Worktree retirement proof

One `RetirementDecision` service is used by post-merge cleanup, standalone completion, startup, hourly maintenance, session advice, and `oro storage clean --scope worktrees`. Eligibility requires all of:

- the exact canonical path is a currently registered Git worktree;
- Oro has a matching ownership record for project, task, branch, target, path, and creation identity;
- task state is terminal and no recovery quarantine is open;
- the exact branch tip is an ancestor of the recorded target tip;
- `git status --porcelain` is empty, including untracked files;
- no runtime lease or matching live process group exists;
- the path and refs are unchanged immediately before execution.

Failure or uncertainty produces `preserve` with a stable reason. Worktrees outside `.worktrees` are evaluated only when catalog-owned; unregistered worktrees are reported but never deleted. `git worktree prune` runs after successful managed removals and is never presented as directory cleanup.

The session-start banner must consume dry-run retirement decisions. It may advertise only `eligible` items and must display preservation counts separately. It cannot infer cleanup availability from ancestry alone.

### 7. One planner, status surface, and evidence model

All triggers call one planner and executor. A decision contains:

```text
class, canonical_path, owner_record, live_evidence,
eligibility, preserve_reason, planned_action,
visit_budget, delete_budget, bytes_before, bytes_after, run_id
```

`oro storage status --json` reports managed runtime, unmanaged candidates, worktree retirement backlog, provider roots, active leases/process groups, controller epoch acknowledgements, and cleanup failures. Filesystem measurement failures remain visible and do not become zero.

`oro storage clean --dry-run --scope ... --json` and scheduled maintenance use the same planner. `--apply` authorizes only already-proven actions; it does not weaken ownership, liveness, containment, drain, or Git proof. Decisions are revalidated immediately before mutation.

### 8. Trigger and sequencing model

Recurrence prevention ships before bulk legacy reclamation:

1. cache-variable separation and fail-closed resolution;
2. runtime admission API plus complete production call-site migration;
3. detached-process ownership and harness cleanup;
4. accurate status and dry-run;
5. normal retirement and bounded managed deletion;
6. safe worktree retirement;
7. drain-gated provider maintenance;
8. legacy adoption/reconciliation.

No later step may be used to compensate for a missing earlier safety boundary. In particular, the legacy reconciler cannot ship as the mechanism that controls continued unleased namespace creation.

Triggers are startup, hourly, post-merge/no-op completion, standalone completion, disk pressure, manual dry-run/apply, and weekly provider maintenance. Every trigger calls the same APIs; trigger-specific cleanup implementations are forbidden.

## Error Handling and Recovery

- **Catalog unavailable or corrupt:** deny new worktree scratch admission; allow read-only status; disable deletion and provider maintenance.
- **Cache resolution fails:** deny child start; do not use the caller's temporary HOME-derived default.
- **Owner liveness uncertain:** preserve and report the exact failed proof.
- **Controller fails to acknowledge drain:** block provider maintenance and leave admission paused until an operator resumes or the controller is proven dead.
- **Deletion interrupted:** resume from tombstone state within the same bounded budgets.
- **Provider cleaner partially fails:** retain before/after evidence, classify the failure, and do not claim reclaimed bytes not measured.
- **Worktree/ref changes after planning:** abort that decision and preserve.
- **Test cleanup cannot stop a detached worker:** fail the test and emit the registered process identity for recovery.

## Compatibility and Migration

- Existing cache configuration is read, but a provider claiming multiple variables must resolve a distinct path per variable or fail validation. A compatibility report shows the old and new resolved paths before enabling routing.
- Existing runtime directories are legacy candidates only. They are not inserted as released leases merely to make them deletable.
- Existing catalog rows receive schema-versioned migrations. Unknown or incomplete owner rows remain preservation-only.
- Existing `ForWorkdir` callers migrate behind compile-time and repository-wide tests. No silent compatibility path remains in production.
- Existing top-level temp directories stay untouched unless a versioned manifest or explicit exact-path adoption establishes ownership.
- Prototype branches are design input only. Their commits are not prerequisites and should be superseded or cherry-picked selectively after task-level tests prove conformance.

## Acceptance Strategy

The corrective epic has one hermetic end-to-end command that runs against `main`:

```text
Cmd: ./scripts/test_storage_exhaust_containment.sh
Assert: exit 0 and final stdout line STORAGE_EXHAUST_CONTAINMENT_PASS
```

The script uses isolated fixture roots, fake process identities, fake provider executables, and temporary Git repositories. It must not inspect or delete the developer's real `/private/tmp`, caches, processes, or worktrees. It verifies all of these observable behaviors:

1. A failed catalog/cache resolution creates no namespace and starts no child.
2. Every production worktree subprocess entry point uses a runtime handle; repository search finds zero production `ForWorkdir` calls.
3. Repeated successful commands for one worktree reuse one leased namespace and terminal retirement removes it within the configured bound.
4. A live or identity-uncertain owner blocks runtime and legacy deletion.
5. A proven-dead, strictly recognized legacy owner is reclaimable.
6. A 100,000-child fixture visits at most 1,024 entries, removes at most 256 directories or 1 GiB, advances a per-root cursor, and leaves the unknown-child manifest unchanged.
7. `GOCACHE` and `GOMODCACHE` resolve to distinct external roots under replaced `HOME`, and duplicate variable ownership is rejected.
8. Status bytes equal fixture filesystem bytes for both provider roots and unmanaged temp candidates; measurement errors are nonzero findings.
9. Provider cleanup is blocked with an active/unacknowledged controller, then runs after a completed drain and records partial cleaner failure.
10. The worktree matrix removes only registered + terminal + merged + clean + unleased worktrees; dirty, unmerged, leased, quarantined, changed, external-unowned, and unregistered worktrees are preserved.
11. Post-merge, standalone, startup, hourly, session advice, and manual cleanup produce the same decision for the same worktree fixture.
12. The autonomous intake harness exits with no registered external worker or descendant process group alive.
13. Dry-run and apply share decision IDs, while apply revalidation blocks a path whose owner or ref changed after planning.

Each work package also has focused unit/integration tests, but the epic cannot close based on package tests alone.

## Work Packages and Dependency Seams

These are decomposition boundaries, not implementation authorization:

1. **Corrective epic acceptance fixture** — establish the main-branch command and hermetic fixture utilities first; initially red.
2. **Cache variable ownership contract** — per-variable provider registration, duplicate rejection, distinct Go defaults, fail-closed home/cache resolution.
3. **Runtime catalog schema and admission handle** — namespace owner, lease, process identity, purpose, epoch, and transactional create ordering.
4. **Spawner lifecycle adapter** — one start/wait/cancel/kill adapter that owns runtime and process-group records.
5. **Production call-site migration groups** — worker/reviewer; dispatcher/merge/acceptance; ops/janitor/codesearch; CLI/standalone/hooks. Each group is independently grep- and integration-tested.
6. **Detached worker lifecycle** — public stop/wait semantics and autonomous-intake harness cleanup.
7. **Runtime retirement executor** — tombstone state, bounded removal, restart recovery.
8. **Streaming legacy reconciler** — per-root cursor, 1,024/256/1-GiB budgets, liveness proof, unknown preservation, explicit adoption.
9. **Provider measurement and status** — resolved roots, real bytes, blockers, unmanaged candidates, measurement errors.
10. **Drain epoch and maintenance runner** — acknowledgement semantics, revalidation, provider-native commands, partial-failure evidence.
11. **Worktree retirement proof service** — catalog/Git/task/dirty/lease/quarantine matrix and execution-time revalidation.
12. **Retirement trigger wiring** — post-merge, standalone, startup, hourly, pressure, manual, and session advice all call the shared service.
13. **Cross-package end-to-end closure** — make the corrective acceptance command green on `main` and prove no prototype-only or test-only wiring.

Required dependency order is 1 before all observable behavior; 2 and 3 before 4; 4 before 5 and 6; 3 before 7 and 8; 2 plus 3 before 9; 3 plus 4 plus 9 before 10; 3 plus 7 before 11; 11 before 12; all packages before 13. Legacy reconciliation is deliberately downstream of recurrence prevention.

## Rollout and Rollback

1. Land the red hermetic acceptance fixture without enabling cleanup.
2. Land cache resolution and runtime admission behind trusted user configuration, defaulting new untracked admission to denied once all production callers migrate.
3. Enable status and dry-run; compare catalog and fixture measurements before enabling deletion.
4. Enable normal retirement for newly owned namespaces.
5. Enable worktree retirement only after the preservation matrix passes.
6. Enable drain-gated provider maintenance.
7. Enable legacy reconciliation last, beginning with report-only cycles.

Rollback disables new deletion and provider maintenance while retaining status and ownership records. It must not restore environment-only scratch creation. Newly created caches are rebuildable; preserved unknown paths and worktrees remain untouched.

## Accepted Risks

- Oro cannot prove non-Oro host processes are idle. Provider maintenance exposes this boundary and may still fail safely if another writer races it.
- Accurate recursive byte measurement can be expensive. It runs outside admission hot paths, is budgeted/cached, and reports staleness.
- Preservation-first legacy handling leaves some historical disk use for explicit operator disposition. This is preferable to deleting unknown data.
- Call-site migration is broad. Compile-time API pressure plus the main-branch acceptance command is required because a partial migration recreates the incident.

## Success Criteria

The solution is complete only when the main-branch acceptance command passes, status accounts for the fixture's actual bytes, no production path can create unleased worktree scratch, cache variables have independent ownership and roots, live/uncertain owners are preserved, bounded reconciliation is proven at 100,000 children, detached tests leave no processes, and every worktree cleanup trigger reaches the same proof service.
