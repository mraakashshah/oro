# Pressure Test of `2026-04-22-technical-audit.md`

## A. Audit score

- Accuracy: 5/10
- Depth: 6/10
- Signal-to-noise ratio: 5/10
- Practical usefulness: 6/10

Overall: **5.5/10**.

The audit found some real issues, but it overstates evidence, duplicates related problems as separate top findings, and misses at least two higher-risk issues. Assuming at least 20% is wrong or overstated is warranted; the overstatement rate is closer to 35-45%.

## B. Reconstructed context

Oro is a macOS-oriented local multi-agent software orchestration system. `cmd/oro` drives startup and lifecycle; `pkg/dispatcher` assigns beads to workers over a Unix socket, manages git worktrees, runs pre-merge quality gates, merges results, logs runtime state in SQLite, and coordinates memory consolidation. `pkg/memory` stores/searches cross-session memory. `cmd/oro-dash` and `pkg/web` provide dashboards.

The audit understood the broad system correctly. It did not handle project identity as a first-class cross-cutting concern, and that caused it to miss the worst variant of the project-name bug.

## C. Validation of audit findings

### F-001 Failed/QG-rejected work is auto-committed, then later force-deleted

Status: **confirmed**

Evidence is direct:

- Pre-merge QG failure/error calls cleanup in [pkg/dispatcher/dispatcher.go](/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go:1535) and [pkg/dispatcher/dispatcher.go](/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go:1548).
- Cleanup calls `Remove()` and then `DeleteBranch()` in [pkg/dispatcher/dispatcher.go](/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go:1776).
- `Remove()` auto-commits dirty state before removal in [pkg/dispatcher/worktree_manager.go](/Users/as21/codehouse/oro/pkg/dispatcher/worktree_manager.go:165) and [pkg/dispatcher/worktree_manager.go](/Users/as21/codehouse/oro/pkg/dispatcher/worktree_manager.go:203).
- `DeleteBranch()` uses `git branch -D` in [pkg/dispatcher/worktree_manager.go](/Users/as21/codehouse/oro/pkg/dispatcher/worktree_manager.go:214).
- Reassignment also deletes stale `agent/<bead>` branches up front in [pkg/dispatcher/dispatcher.go](/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go:4590).

Severity is justified as high. The audit was directionally right.

### F-002 Project name path traversal allows writes outside `~/.oro/projects`

Status: **confirmed, but understated**

The path traversal is real:

- `ResolveDaemonPaths()` joins raw project text into `~/.oro/projects/<project>` in [cmd/oro/paths.go](/Users/as21/codehouse/oro/cmd/oro/paths.go:61).
- `detectProjectMode()` returns raw `ORO_PROJECT` or raw `project:` text in [cmd/oro/paths.go](/Users/as21/codehouse/oro/cmd/oro/paths.go:291) and [cmd/oro/paths.go](/Users/as21/codehouse/oro/cmd/oro/paths.go:300).
- `readProjectConfig()` also returns raw text in [cmd/oro/cmd_start.go](/Users/as21/codehouse/oro/cmd/oro/cmd_start.go:729).
- `migrateGlobalDBs()` joins raw project text into the destination path in [cmd/oro/paths.go](/Users/as21/codehouse/oro/cmd/oro/paths.go:362).

The audit missed the worse consequence: the same unsanitized `project` value is interpolated into tmux shell commands in [cmd/oro/tmux.go](/Users/as21/codehouse/oro/cmd/oro/tmux.go:233) and passed to `tmux new-session/new-window` in [cmd/oro/tmux.go](/Users/as21/codehouse/oro/cmd/oro/tmux.go:272). This is not just path traversal; it is likely shell-command injection if a malicious project name contains shell metacharacters.

### F-003 SQLite foreign-key cascades are assumed but disabled

Status: **confirmed**

The code facts are straightforward:

- `OpenDB()` sets WAL and busy timeout, but not `PRAGMA foreign_keys=ON`, in [pkg/dbutil/openDB.go](/Users/as21/codehouse/oro/pkg/dbutil/openDB.go:37).
- Schema comments rely on `ON DELETE CASCADE` for `memory_chunks` in [pkg/protocol/schema.go](/Users/as21/codehouse/oro/pkg/protocol/schema.go:220).
- Real delete paths exist in [pkg/memory/memory.go](/Users/as21/codehouse/oro/pkg/memory/memory.go:1423), [pkg/memory/memory.go](/Users/as21/codehouse/oro/pkg/memory/memory.go:1520), [pkg/memory/memory.go](/Users/as21/codehouse/oro/pkg/memory/memory.go:1563), and [pkg/memory/memory.go](/Users/as21/codehouse/oro/pkg/memory/memory.go:1866).

Severity is high enough to matter, though the audit could have been tighter by citing an actual delete path instead of only the schema comment.

### F-004 Branch deletion is forceful despite comments/docs claiming safe delete

Status: **partially correct**

Yes, the comment is false: [pkg/dispatcher/worktree_manager.go](/Users/as21/codehouse/oro/pkg/dispatcher/worktree_manager.go:212) says `-d`, but implementation uses `-D` in [pkg/dispatcher/worktree_manager.go](/Users/as21/codehouse/oro/pkg/dispatcher/worktree_manager.go:214).

What is overstated:

- As a standalone top-5 risk, this is mostly a duplicate of F-001.
- The real issue is destructive cleanup semantics, not “comment drift”.
- The audit treated this as an independent medium-severity bug when it is mainly supporting evidence for the data-loss finding.

### F-005 CI/test matrix gives false confidence on platform-specific runtime paths

Status: **partially correct**

Evidence exists:

- CI is Ubuntu-only in `.github/workflows/ci.yml` at [ci.yml](/Users/as21/codehouse/oro/.github/workflows/ci.yml:10), [ci.yml](/Users/as21/codehouse/oro/.github/workflows/ci.yml:82), and [ci.yml](/Users/as21/codehouse/oro/.github/workflows/ci.yml:141).
- README says runtime is macOS-only.
- Python CI ignores three hook test files in [ci.yml](/Users/as21/codehouse/oro/.github/workflows/ci.yml:174).
- `pytest_collection_modifyitems` also conditionally skips the same class of tests in [tests/conftest.py](/Users/as21/codehouse/oro/tests/conftest.py:17).

What is overstated:

- “False confidence” is plausible, but the audit did not tie this to an observed failure in a macOS-only path.
- This is a test gap, not one of the top product risks.

### F-006 Vendored native library target mismatch is already visible during test/build

Status: **weak / speculative**

The warning is real; I reproduced linker warnings while running targeted `go test ./pkg/dispatcher`.

What is overstated:

- The audit jumps from a linker warning to a release portability hazard without showing a failing build, failing runtime load, or unsupported deployment target policy.
- This is operational debt. It is not a justified top-6 repo risk ahead of project-identity injection or unsafe DB migration.

## D. Biggest problems with the audit

1. It missed the most serious variant of the project-name bug: unsanitized `project` flows into tmux shell commands, not just filesystem paths.
2. It split one destructive-cleanup problem into multiple “top risks” (`auto-commit/delete` and `-D vs -d`) and inflated the count of distinct issues.
3. It asserted some impacts without enough chain-of-evidence. F-003 should have cited actual delete paths; F-006 should not have been elevated without a demonstrated failure mode.
4. It under-analyzed migrations and persistence integrity. The repo has a more serious DB-migration risk than the native-lib warning.
5. It treated CI/environment gaps as product risk at the same level as correctness/data-loss issues, which is poor prioritization.

## E. Missed high-risk issues

### 1. Unsanitized project names reach tmux shell commands

Severity: **critical**

This is the biggest miss.

- Raw project name enters via env/config in [cmd/oro/paths.go](/Users/as21/codehouse/oro/cmd/oro/paths.go:291) and [cmd/oro/cmd_start.go](/Users/as21/codehouse/oro/cmd/oro/cmd_start.go:740).
- It is formatted directly into a shell command string in [cmd/oro/tmux.go](/Users/as21/codehouse/oro/cmd/oro/tmux.go:233).
- That string is passed to tmux as the pane command in [cmd/oro/tmux.go](/Users/as21/codehouse/oro/cmd/oro/tmux.go:272), [cmd/oro/tmux.go](/Users/as21/codehouse/oro/cmd/oro/tmux.go:277), and reused in the pane-died respawn hook in [cmd/oro/tmux.go](/Users/as21/codehouse/oro/cmd/oro/tmux.go:886).

If project names are attacker-controlled through `.oro/config.yaml` or `ORO_PROJECT`, this is likely command execution, not just misplaced files.

### 2. `migrateGlobalDBs()` copies live WAL databases unsafely

Severity: **high**

- SQLite databases are opened in WAL mode in [pkg/dbutil/openDB.go](/Users/as21/codehouse/oro/pkg/dbutil/openDB.go:58).
- Migration copies only the main `state.db` and `code_index.db` files via `os.ReadFile`/`os.WriteFile` in [cmd/oro/paths.go](/Users/as21/codehouse/oro/cmd/oro/paths.go:369) and [cmd/oro/paths.go](/Users/as21/codehouse/oro/cmd/oro/paths.go:393).

That ignores `-wal`/`-shm` state and does not use SQLite backup APIs or checkpointing. On first-use migration, recent writes can be lost or the copy can be inconsistent.

### 3. Project identity parsing is ad hoc and not YAML-safe

Severity: **medium**

The repo already depends on `gopkg.in/yaml.v3`, but project identity is parsed with manual line scanning in multiple places:

- [cmd/oro/paths.go](/Users/as21/codehouse/oro/cmd/oro/paths.go:296)
- [cmd/oro/cmd_start.go](/Users/as21/codehouse/oro/cmd/oro/cmd_start.go:737)
- [cmd/oro/cmd_shell.go](/Users/as21/codehouse/oro/cmd/oro/cmd_shell.go:118)

Quoted values, inline comments, malformed YAML, or duplicate keys can produce wrong project names and amplify the path/shell issues.

## F. Corrected findings

1. **Critical: project identity is untrusted input and is reused across filesystem paths, tmux session identity, and tmux shell commands.**
   Failure mode: a malicious `project` value can redirect state under arbitrary directories and likely inject shell tokens into tmux-managed commands. Evidence: [cmd/oro/paths.go](/Users/as21/codehouse/oro/cmd/oro/paths.go:61), [cmd/oro/paths.go](/Users/as21/codehouse/oro/cmd/oro/paths.go:291), [cmd/oro/cmd_start.go](/Users/as21/codehouse/oro/cmd/oro/cmd_start.go:740), [cmd/oro/tmux.go](/Users/as21/codehouse/oro/cmd/oro/tmux.go:233), [cmd/oro/tmux.go](/Users/as21/codehouse/oro/cmd/oro/tmux.go:272).

2. **High: failed pre-merge cleanup mutates and destroys rejected work instead of preserving it for inspection/retry.**
   Failure mode: on pre-merge QG fail/error, worktree cleanup auto-commits dirty changes, removes the worktree, and force-deletes the branch; later reassignment also deletes any surviving stale branch. Evidence: [pkg/dispatcher/dispatcher.go](/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go:1535), [pkg/dispatcher/dispatcher.go](/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go:1776), [pkg/dispatcher/worktree_manager.go](/Users/as21/codehouse/oro/pkg/dispatcher/worktree_manager.go:165), [pkg/dispatcher/worktree_manager.go](/Users/as21/codehouse/oro/pkg/dispatcher/worktree_manager.go:214), [pkg/dispatcher/dispatcher.go](/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go:4590).

3. **High: first-use migration of global SQLite DBs is not WAL-safe.**
   Failure mode: migration can copy an incomplete snapshot of a live DB because it only copies the main file and ignores WAL state. Evidence: [pkg/dbutil/openDB.go](/Users/as21/codehouse/oro/pkg/dbutil/openDB.go:58), [cmd/oro/paths.go](/Users/as21/codehouse/oro/cmd/oro/paths.go:392).

4. **High: SQLite foreign-key enforcement is off, so `memory_chunks` referential cleanup is unreliable.**
   Failure mode: deleting memories leaves orphaned chunk rows and diverging semantic-memory state. Evidence: [pkg/dbutil/openDB.go](/Users/as21/codehouse/oro/pkg/dbutil/openDB.go:37), [pkg/protocol/schema.go](/Users/as21/codehouse/oro/pkg/protocol/schema.go:220), [pkg/memory/memory.go](/Users/as21/codehouse/oro/pkg/memory/memory.go:1423).

5. **Medium: project config parsing is brittle and duplicated.**
   Failure mode: comments/quotes/malformed YAML can poison project identity and cause wrong path resolution, wrong settings file selection, or broken shell commands. Evidence: [cmd/oro/cmd_start.go](/Users/as21/codehouse/oro/cmd/oro/cmd_start.go:737), [cmd/oro/paths.go](/Users/as21/codehouse/oro/cmd/oro/paths.go:296), [cmd/oro/cmd_shell.go](/Users/as21/codehouse/oro/cmd/oro/cmd_shell.go:118).

6. **Medium: CI under-covers the documented runtime environment and explicitly omits some hook tests.**
   Failure mode: green CI can miss macOS-only startup/hook regressions. Evidence: [ci.yml](/Users/as21/codehouse/oro/.github/workflows/ci.yml:10), [ci.yml](/Users/as21/codehouse/oro/.github/workflows/ci.yml:174), [tests/conftest.py](/Users/as21/codehouse/oro/tests/conftest.py:17).

7. **Low-medium: native library deployment-target mismatch is a warning worth fixing, but it is not top-tier risk.**
   Failure mode: future build/runtime portability issues on older macOS targets. Evidence currently shows linker warnings, not a demonstrated product failure.

## G. Corrected priority list

1. Validate and canonicalize project identity once at ingress; reject separators, traversal, shell metacharacters, whitespace ambiguity, and malformed YAML values.
2. Remove shell-string command construction for tmux startup/respawn, or quote every component correctly.
3. Stop auto-committing and force-deleting failed worktree branches on QG fail/error paths.
4. Fix SQLite invariants: enable `PRAGMA foreign_keys=ON` on every connection.
5. Replace raw file-copy migration of WAL databases with SQLite backup/checkpoint-based migration.
6. Consolidate project config parsing behind a real YAML parser and a single `ProjectIdentity` type.
7. Add macOS CI smoke coverage for startup, hooks, and native-memory loading.

## H. Confidence

**Medium-high.**

I verified the major claims directly in code and reproduced the native-linker warning during targeted tests. I did not execute full end-to-end tmux exploitation or a live WAL-corruption reproduction in this pass, so the exact exploitability/blast radius there is inferred from code paths rather than demonstrated in a full runtime scenario.
