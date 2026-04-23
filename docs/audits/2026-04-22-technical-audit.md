# Technical Audit

Date: 2026-04-22
Repository: `oro`

## A. Executive Summary

Oro appears to be a Go-based local orchestration CLI for running a multi-agent software workflow: a dispatcher manages workers over a Unix socket, tracks runtime state in SQLite, creates git worktrees per bead, runs quality gates, merges results, and persists cross-session memory. The main runtime lives in `cmd/oro` and `pkg/dispatcher`, with supporting packages for workers, merge coordination, memory, and web/dashboard.

Top 5 risks:

1. Critical: unvalidated project names can escape `~/.oro/projects/...` and redirect sockets/DB/settings writes elsewhere on disk.
2. High: pre-merge/QG-failure cleanup auto-commits failed work, then later force-deletes the branch, which can silently lose or mutate rejected work.
3. High: SQLite foreign keys are never enabled, so `ON DELETE CASCADE` guarantees for `memory_chunks` are false.
4. Medium: destructive branch deletion uses `git branch -D` despite comments/docs claiming safe `-d`, increasing work-loss blast radius.
5. Medium: CI and much of the test suite exercise Ubuntu/CGO-free paths while the product is documented as macOS-only and several hook tests are explicitly skipped.

Overall confidence: medium-high on the core dispatcher/memory/worktree findings; lower on peripheral packages not deeply inspected (`pkg/web`, `pkg/codesearch`, some scripts/docs).

## B. System Map

### Main entrypoints

- `cmd/oro/main.go` and `cmd/oro/root.go`: Cobra CLI root.
- `cmd/oro/cmd_start.go`: launches dispatcher/daemon.
- `cmd/oro/cmd_work.go`: single-bead execution path.
- `cmd/oro-search-hook/main.go`: search hook binary.

### Core modules/components

- `pkg/dispatcher`: worker lifecycle, assignment, QG, merge, escalation.
- `pkg/worker`: worker subprocess control, reconnect, handoff, QG.
- `pkg/memory`: persistent memory store, embeddings, reranking.
- `pkg/merge`: merge coordinator.
- `cmd/oro/dolt.go`: shared/per-project Dolt lifecycle.
- `cmd/oro/paths.go`: project/home/path scoping.

### Data/storage model

- SQLite `state.db` for runtime events, assignments, commands, escalations, memories, KV state.
- Git worktrees under `.worktrees/` or stealth-mode path.
- `.beads/metadata.json` and related Dolt/PID/port files.
- `~/.oro/projects/<project>/...` for per-project state/settings.

### Critical runtime paths

- `oro start` -> path resolution -> DB open/migrate -> dispatcher start -> worker spawn -> assign bead -> worker runs subprocess -> QG -> merge -> cleanup.
- Memory insert/search/backfill from workers and dispatcher.
- Dolt server ensure/setup/repair before bead operations.

### Key operational dependencies

- Git, tmux, beads `bd`, Dolt, optional native tokenizer/ONNX/sqlite-vec libs, launchd on macOS.
- CI: mostly Ubuntu in `.github/workflows/ci.yml`.

## C. Findings Table

| ID | Title | Severity | Confidence | Category | Location | Evidence | Why it matters | Real failure mode | Reproduction or triggering conditions | Suggested fix | Suggested regression test |
|---|---|---|---|---|---|---|---|---|---|---|---|
| F-001 | Failed/QG-rejected work is auto-committed, then later force-deleted | high | confirmed | correctness / reliability / data corruption | `pkg/dispatcher/dispatcher.go`, `pkg/dispatcher/worktree_manager.go` | On pre-merge QG failure, dispatcher reopens the bead and calls cleanup. `Remove()` stages and commits dirty worktrees, and later reassignment proactively deletes the stale `agent/<bead>` branch. `DeleteBranch()` uses `-D`. | A rejected attempt should remain inspectable or resumable; instead the system mutates git history and can erase the only copy of failed work. | Failing changes get an automatic commit, then the next assignment deletes that branch before a human or ops agent can inspect it. | Any pre-merge QG failure or QG error on a dirty worktree. | Never auto-commit on failure cleanup. Preserve the worktree/branch for retry, or archive it explicitly. Only force-delete after confirmed merge or explicit abandonment. | Real git repo test: dirty worktree + failing pre-merge QG must leave branch/worktree intact and uncommitted. |
| F-002 | Project name path traversal allows writes outside `~/.oro/projects` | critical | confirmed | security / reliability | `cmd/oro/cmd_init.go`, `cmd/oro/paths.go`, `cmd/oro/cmd_start.go` | `resolveProjectName()` returns raw user input. `detectProjectMode()` and `readProjectConfig()` also accept raw `project:` values. Those names are fed into `filepath.Join(oroHome, "projects", project)`; e.g. `../../.ssh` resolves outside the intended subtree. | A malicious repo config or env var can redirect sockets, PID files, DBs, and settings into arbitrary user-writable paths. | Starting oro in an untrusted repo can create/overwrite files under `~/.ssh`, sibling project dirs, or other unintended locations. | `ORO_PROJECT=../../...`, `oro init ../../...`, or committed `.oro/config.yaml` with traversal segments. | Validate project names against a strict allowlist and reject path separators, `..`, absolute paths, and control chars before any join/write. | Table-driven tests for env/config/CLI project names including `../x`, `/tmp/x`, `a/b`, quoted/commented YAML values. |
| F-003 | SQLite foreign-key cascades are assumed but disabled | high | confirmed | reliability / data corruption | `pkg/dbutil/openDB.go`, `pkg/protocol/schema.go` | `OpenDB()` enables WAL and busy timeout but never `PRAGMA foreign_keys=ON`. A direct check via `dbutil.OpenDB()` returned `PRAGMA foreign_keys = 0`. Meanwhile schema comments claim `ON DELETE CASCADE ensures` orphan cleanup for `memory_chunks`. | Memory deletion/merge/consolidation can leave orphaned chunk rows and silently corrupt semantic-memory invariants. | Deleting parent memories does not cascade to `memory_chunks`; chunk table grows stale and diverges from `memories`. | Any `DELETE FROM memories` path: merge, forget, retention, rejection migration, dream consolidation. | Enable `PRAGMA foreign_keys=ON` on every opened connection and add a startup invariant check. | Open DB, assert `PRAGMA foreign_keys=1`, insert parent+child, delete parent, assert child row removed. |
| F-004 | Branch deletion is forceful despite comments and safety docs claiming safe delete | medium | confirmed | maintainability / correctness | `pkg/dispatcher/worktree_manager.go`, `cmd/oro/_assets/skills/destructive-command-safety/SKILL.md` | Comment says `git branch -d` and “Uses -d (not -D)”, but implementation runs `git branch -D`. Internal safety docs also warn that `-D` is dangerous. | Operators and maintainers are told cleanup is guarded when it is not. This amplifies F-001 and makes branch cleanup semantics easy to misuse elsewhere. | Unmerged or diagnostic branches are deleted without Git’s merge-safety check. | Any call to `DeleteBranch()` or startup stale-branch pruning. | Either switch to `-d` where safety is expected, or rename/comment the API to make force-delete explicit and constrained. | Tests should assert safe-delete behavior for unmerged branches, plus separate explicit force-delete APIs where needed. |
| F-005 | CI/test matrix gives false confidence on platform-specific runtime paths | medium | high | test gap / ops | `.github/workflows/ci.yml`, `README.md` | README says “Runtime requirements (macOS only)”, but CI Go/Python jobs run on Ubuntu. Python CI explicitly skips three hook tests. Local package test/builds emitted native-lib warnings; `go test ./cmd/oro` did not complete promptly in the audit time budget. | The repo’s most important runtime paths are launchd/tmux/macOS/home-dir/native-lib paths, but CI mostly validates different execution environments. | Green CI can miss macOS-only failures, skipped hook regressions, and native-lib packaging problems. | Changes to launchd, hooks, install/setup, Dolt lifecycle, native semantic-memory integration. | Add a macOS CI lane for runtime-critical packages and stop skipping hook tests by default; isolate truly env-dependent tests behind targeted fixtures. | Add macOS integration smoke tests for `oro start`, `oro dolt setup/repair`, shell/hooks, and semantic-memory library loading. |
| F-006 | Vendored native library target mismatch is already visible during test/build | medium | confirmed | ops / dependency | `pkg/memory/lib/libtokenizers.a` | Running `go test ./pkg/memory` produced repeated linker warnings that `libtokenizers.a` was built for newer macOS 14.4 than the current link target 14.0. | This is a release portability hazard: builds may be green on one dev machine and degraded or broken on another macOS target. | Older-target builds can fail late or ship binaries with fragile native-link assumptions. | Building/testing on older macOS SDK targets or different runners. | Rebuild/pin native artifacts against the supported deployment target and validate them in CI. | CI step that links semantic-memory packages on macOS and fails on SDK/version mismatch warnings. |

## D. Test Coverage Assessment

### Areas well covered

- Dispatcher unit tests are extensive, especially around assignment, QG retries, reconnects, and worktree manager call sequences.
- Memory package has strong unit coverage for insert/search/chunking behavior.
- CLI commands have many command-level tests.

### Areas under-tested

- Real git side effects behind mocked runners.
- macOS-only launchd/setup/runtime flows.
- Path sanitization and malicious config/env inputs.
- SQLite integrity behavior (`foreign_keys`, real cascades).
- Native semantic-memory dependency loading and portability.

### Tests that give false confidence

- Worktree tests mostly assert that cleanup methods are called, not the resulting git history/data preservation.
- CI coverage numbers are collected on Ubuntu while the runtime is documented as macOS-only.
- Python CI skips hook tests that guard destructive or completion behavior.

### Highest-value tests to add next

1. Real-repo test for QG failure cleanup preserving rejected work.
2. Project-name sanitization tests across CLI arg, env, and config file.
3. DB test asserting `foreign_keys=ON` and actual cascade behavior.
4. macOS integration smoke for launchd/Dolt lifecycle.
5. Native-lib load/link validation on macOS CI.

## E. Architecture / Maintainability Assessment

### Design debt that is likely to cause future bugs

- Path/project identity is treated as trusted text and re-parsed ad hoc in multiple places.
- Cleanup semantics are split across dispatcher, worktree manager, and merge coordinator with different hidden assumptions about when branches are disposable.
- “Best-effort” fallbacks and suppressed errors are common in startup/lifecycle code, which is dangerous in an orchestrator.

### Tight coupling / hidden invariants / unsafe abstractions

- `DeleteBranch()` looks safe by name/comment but is forceful in behavior.
- Memory integrity relies on schema comments and implicit SQLite behavior rather than enforced connection invariants.
- The same state DB carries both runtime coordination and memory concerns.

### Refactors worth doing

- Centralize validated project identity/path construction in one package.
- Split safe cleanup from destructive cleanup in the worktree API.
- Introduce DB-open invariants object that enforces all required PRAGMAs.
- Reduce mocked command-runner reliance for a few critical end-to-end git tests.

## F. Quick Wins

1. Reject unsafe project names at ingress.
2. Turn on `PRAGMA foreign_keys=ON` in `OpenDB()`.
3. Stop auto-committing dirty worktrees during failure cleanup.
4. Replace force-delete branch cleanup with explicit safe/unsafe APIs.
5. Add a real git integration test for rejected-work preservation.
6. Add macOS CI for runtime-critical packages.
7. Unskip the Python hook tests by giving them isolated fixtures.
8. Fail CI on native-lib deployment-target mismatch warnings.
9. Replace line-based config parsing with a real YAML parser for `project` and `default_branch`.
10. Audit and reduce `nilerr` / “best-effort” silent fallbacks in startup and path resolution.

## Validation Notes

Verified during the audit:

- `dbutil.OpenDB()` leaves `PRAGMA foreign_keys` at `0`.
- `go test ./pkg/memory` passed locally but emitted `libtokenizers.a` macOS target warnings.
- `go test ./pkg/dispatcher` and `go test ./cmd/oro` did not complete within the audit time budget, so the suite was not treated as a reliable full validation signal.
