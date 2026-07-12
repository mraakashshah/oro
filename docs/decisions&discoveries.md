# Decisions and Discoveries

## 2026-07-12: All code writes must land in a worktree (enforced by hook)
**Tags:** #worktree #hooks #isolation #enforcement #concurrency
**Context:** Investigated how superpowers (`using-git-worktrees`) and the
compound-engineering-plugin (`ce-worktree`) handle worktrees. Both are
detect-first *discipline* with zero write-time enforcement — they rely on the
harness creating a worktree at session start. Oro's dispatcher already requires
a worktree for swarm workers (`AssignPayload.Validate`), but the main session
and foreground/absolute-path writes were unguarded, so a concurrent agent could
edit the shared primary checkout at any time.
**Decision:** Added `enforce_worktree_writes.py`, a PreToolUse hook on
`Write|Edit|NotebookEdit` that DENIES writes whose target resolves inside a git
*primary* checkout. Allowed: linked worktrees, out-of-repo paths, allow-listed
prefixes (`docs/`, `.worktrees/`, `.claude/` — `.claude/` kept writable so the
policy can always be disabled), and the `ORO_ALLOW_MAIN_WRITES=1` escape hatch.
Primary-vs-linked detection follows ce-worktree (resolved `--absolute-git-dir`
vs `--git-common-dir`). Wired for all projects via `buildHookConfig` in
`cmd/oro/cmd_init.go` and into this repo's `.claude/settings.json`.
**Implications:** In any oro-managed project, agents (including the main
session) must create/enter a `.worktrees/<branch>` worktree before editing code;
non-code surfaces (docs, config) stay editable in place. Takes effect on the
next session start after settings load. To extend the allow-list, edit
`ALLOWLIST_PREFIXES` in the hook.

## 2026-05-19: Managerless operation is the default
**Tags:** #managerless #dispatcher #ops-runs #operations
**Context:** The managerless orchestration design moved routine factory progress
off an optional legacy manager console and onto durable dispatcher-owned state.
**Decision:** Default operator guidance must use `oro health --json`,
`oro status`, `oro monitor`, events, and `ops_runs` for routine progress.
Optional legacy manager-console material is non-authoritative, and
historical `pane_activity` notes are not a default liveness or progress
dependency.
**Implications:** New runbooks, skills, and user-facing text should not tell
operators to depend on an optional legacy manager pane for task assignment,
decomposition, escalation handling, health reporting, or recovery. If optional
console support remains, label it non-authoritative and keep
health/status/ops-run surfaces as the source of truth.

## 2026-04-28: Phase 0 schema sign-off
**Reviewer:** Codex manual adversarial review (GPT-5 coding agent), 2026-04-28.
**Scope:** Replatform beads spec sections 6, 9.6, and 12.1.
**Decision:** Sign off for Phase 1 implementation with the Phase 0 audit notes treated as required inputs, not optional commentary.
**Review:** The core tables, tombstone handling, two-pass import, and trigger envelope are structurally sound for the replatform path. The review did find material readiness and interface-drift risks, but those are already captured in the related plan notes.
**Comments resolved/escalated:** Phase 1 must preserve ready/blocked/deferred task semantics while reshaping the target store interface.

## 2026-04-01: Epic management has 6 failure modes — design doc written
**Tags:** #dispatcher #epic #bugs #architecture
**Context:** During a live swarm session, observed: workers idle with work available (type promotion confused dispatcher), `bead_closed_externally` spam, zombie bead reassignment, ff-merge infinite retry loops, false STUCK_WORKER escalations for missing epic branches. Deep-explored all epic code paths in dispatcher.go.
**Discovery:** 4 root cause patterns: (1) state captured once at assignment, never revalidated (isEpicDecomp, bead Type), (2) error paths missing cleanup (assignment DB not cleared on worker delete), (3) no escalation on ff-merge failure (epic stays open forever), (4) mergingBeads guard race between check cycles. Design doc: `docs/plans/2026-04-01-epic-management-fixes-design.md`.
**Implications:** 6 independent fixes needed, all in dispatcher.go. Fix before adding new epics to the swarm.

## 2026-04-01: Memory dreaming — LLM-powered cross-session memory synthesis
**Tags:** #memory #architecture #decisions #claude-code
**Context:** Analyzed Claude Code's memory system (auto-extraction, dreaming/consolidation, staleness awareness). Oro had 336 memories in 4 weeks but no cross-session synthesis — if 5 workers independently discover the same gotcha, that's 5 competing memories. Manual `oro memories consolidate` is mechanical pruning only.
**Decision:** Added two patterns: (1) Dreaming — ops agent reads entire memories table every 10 completed beads or on epic close, synthesizes cross-memory patterns, resolves contradictions, prunes obsolete. Full create/merge/delete power via structured [DELETE]/[MERGE]/[CREATE] actions. Model: haiku. (2) Staleness warnings — ForPrompt annotates memories >7 days with age marker, worker prompt warns to verify before trusting. Merged in epic oro-kpwx (commit aeaa362).
**Implications:** Memory quality improves automatically over time. Workers get warned about stale knowledge. Dreaming runs as an ops agent (existing spawner pattern) — no new infrastructure.

## 2026-04-01: Epic QG check before ff-merge to main
**Tags:** #dispatcher #quality #epic #merge
**Context:** Workers merged code to epic branches with per-task QG, but the final epic→main ff-merge had NO quality gate. Lint issues (staticcheck) landed on main unchecked. Observed during swarm session when `fmt.Fprintf` vs `WriteString(Sprintf)` broke CI after epic merge.
**Decision:** Added `checkEpicQG` — creates temp worktree from epic branch, runs full QG, removes worktree. Called in `tryCloseEpic` before `completeEpicClose`. QG failure creates a fix task (same pattern as per-task QG failure). Merged in commit b7bdc1e.
**Implications:** No code reaches main without passing QG. Adds ~2min to epic close (acceptable tradeoff).

## 2026-04-01: Stealth mode epics should open PRs, not ff-merge
**Tags:** #stealth #epic #pr #decisions
**Context:** Stealth mode leaves zero footprint on the repo. But `completeEpicClose` ff-merges to local main silently — contradicts stealth's promise. A PR gives the human a review gate before code hits main.
**Decision:** In stealth mode: rebase epic branch onto main (no conflicts), push to origin, `gh pr create`, fire and forget. Epic marked complete, human merges when ready. Non-stealth: existing ff-merge unchanged. In progress (oro-552k).
**Implications:** Stealth mode gains a human review gate. Requires `gh` CLI installed. Non-stealth behavior unchanged.

## 2026-03-13: Per-project daemon isolation (PID/socket scoping)
**Tags:** #architecture #multi-project #daemon #paths
**Context:** Running `oro start` in two different projects simultaneously clashed because PID file and UDS socket were global at `~/.oro/oro.pid` and `~/.oro/oro.sock`. Only one daemon could run at a time.
**Decision/Discovery:** Made `ResolvePaths()` project-aware: when a project name is detected (via `ORO_PROJECT` env var or `.oro/config.yaml`), PID, socket, state DB, and code index all resolve to `~/.oro/projects/<name>/`. Without a project name, paths fall back to global `~/.oro/` for backward compatibility. Env var overrides (`ORO_PID_PATH`, `ORO_SOCKET_PATH`) still take precedence. `ResolveProjectDBPaths()` is now a deprecated alias for `ResolvePaths()`. Added `oro stop --all` to discover and stop daemons across all projects. Updated `oro-dash` to resolve project-scoped socket paths.
**Implications:** Multiple oro instances can run concurrently without ORO_HOME workarounds. Existing single-project setups are unaffected. Legacy global PID files are detected by `oro stop --all` and `discoverProjectDaemons()`.

## 2026-02-19: Hook audit: settings.local.json and worker settings.json are identical

**Tags:** #hooks #audit #worker #settings #no-gap
**Context:** Concern that ~/.oro/projects/oro/settings.json (worker settings, passed via --settings flag) and .claude/settings.local.json (local Claude Code session settings) evolved independently and diverged, leaving workers missing hooks or vice versa.
**Decision/Discovery:** Audit found both files are byte-for-byte equivalent in content. All 16 hook entries (across SessionStart, PreToolUse, PostToolUse, Stop) are present in both files with identical matchers and commands. The permissions section is also identical. No gaps exist as of 2026-02-19.

Hook inventory (same in both files):
| Event | Matcher | Command |
|---|---|---|
| SessionStart | (all) | enforce-skills.sh |
| SessionStart | (all) | session_start_extras.py |
| PreToolUse | Bash | architect_router.py |
| PreToolUse | Bash | worktree_guard.py |
| PreToolUse | Bash | no_cd_guard.py |
| PreToolUse | Bash | rebase_worktree_guard.py |
| PreToolUse | Read | oro-search-hook |
| PreToolUse | Task | enforce_worktree.py |
| PostToolUse | (all) | context_pct_writer.py |
| PostToolUse | Read\|WebFetch\|Bash | prompt_injection_guard.py |
| PostToolUse | Edit\|Write | auto-format.sh |
| PostToolUse | Bash | memory_capture.py |
| PostToolUse | Bash | learning_reminder.py |
| PostToolUse | Bash | notify_manager_on_bead_create.py |
| PostToolUse | Task | validate_agent_completion.py |
| Stop | (all) | stop-checklist.sh |

**Implications:** No follow-up tasks needed for missing hooks. Files appear to be kept in sync. Future changes to either file should be mirrored in the other. Consider consolidating to a single source of truth or adding a CI check to detect drift.

## 2026-02-18: Workers receive context7 MCP permissions in generated settings.json
**Tags:** #workers #mcp #context7 #settings #permissions
**Context:** `~/.oro/projects/<project>/settings.json` is generated by `generateSettings()` in `cmd/oro/cmd_init.go` and passed to worker Claude sessions via `--settings`. The interactive `.claude/settings.local.json` includes permissions for `mcp__context7__resolve-library-id` and `mcp__context7__query-docs`. Workers were missing this block, so any `oro init` run would overwrite the manually-added permissions. Evaluated in oro-r8ov.
**Decision:** Workers DO need context7 permissions. Workers are the primary code-writing agents in the oro pipeline and regularly implement features touching Go stdlib, third-party libraries (cobra, bbolt, etc.), and external APIs. Denying them library/API doc lookups would force them to guess APIs or fall back to WebFetch for documentation — slower and less accurate. The interactive and worker contexts have the same needs here. Added permissions block to `generateSettings()` so it is preserved across `oro init` re-runs.
**Implications:** `generateSettings()` now emits a `permissions` key alongside `hooks`. The existing settings.json on disk already had the permissions (manually patched); the code change makes it permanent and idempotent. Test coverage added in `cmd_init_test.go`.

## 2026-02-14: GIT_DIR/GIT_WORK_TREE env leak from git hooks corrupted main branch
**Tags:** #git #hooks #env-leak #testing #root-cause #quality-gate
**Context:** Investigated suspicious commits on main with author `test@test.com`, vague messages ("initial", "init"), a 14MB binary, and 346 deleted files. The architecture review agent, manager, quality gate, and commit skills appeared to have been completely bypassed.
**Discovery:** The commits were NOT made by Claude agents. They were made by Go test suites (`worker_test.go`, `exec_runner_test.go`, `merge_test.go`) whose git operations were accidentally redirected to the real repository. Root cause: when `quality_gate.sh` runs inside a git pre-push hook, git sets `GIT_DIR` and `GIT_WORK_TREE` environment variables. These leaked into `go test` subprocesses. Tests that create git repos in temp dirs (e.g., `TestRunQualityGate_RestoresDeletedScript` with `git config user.email test@test.com` and `git commit -m "initial"`) had their git operations hit the real repo instead of the temp dir. The corrupted branch was then merged to main by the merge coordinator, deleting 72,701 lines across 346 files.
**Evidence chain:** (1) Reflog showed direct `commit:` entries, not `cherry-pick:` or `merge:` from the worker pipeline. (2) Author `test@test.com` matched `worker_test.go:506` exactly; `test@example.com` matched `exec_runner_test.go:66`. (3) Branch names `agent/test-merged` and `agent/unmerged` came from merge test fixtures. (4) All bad commits happened in a 53-second burst — a single `go test` run. (5) Fix commit `7461500` confirmed the mechanism: `unset GIT_DIR GIT_WORK_TREE` at the top of `quality_gate.sh`.
**Fix:** Added `unset GIT_DIR GIT_WORK_TREE` to the top of `quality_gate.sh` (commit `7461500`).
**Implications:** Any script that runs inside a git hook and spawns subprocesses that use git MUST unset `GIT_DIR` and `GIT_WORK_TREE`. This is not optional — leaked env vars silently redirect all git operations to the parent repo. Go tests with `t.TempDir()` and `cmd.Dir = tmpDir` are NOT sufficient isolation when `GIT_DIR` overrides the directory. Consider also adding `unset GIT_DIR GIT_WORK_TREE` to individual test helpers that create git repos, as defense in depth.

## 2026-02-08: Never cd into worktrees — bash dies when worktree is removed
**Tags:** #worktree #bash #cwd #session-killer
**Context:** During work-bead execution, a `cd` into `.worktrees/bead-oro-by8/` was followed by `git worktree remove`. This deleted the shell's cwd. On macOS, a process whose cwd is deleted cannot execute any commands — every bash call silently returns exit code 1 with no output. The session became unrecoverable.
**Decision:** Never `cd` into a worktree. Always use absolute paths. If a `cd` happened, always `cd` back to the main repo before removing the worktree. Documented in `docs/solutions/2026-02-08-bash-dies-after-worktree-remove.md`.
**Implications:** The `work-bead` and `using-git-worktrees` skills must enforce absolute-path-only worktree access. This is a session-killing bug with no recovery — prevention is the only fix.

## 2026-02-08: Never clone repos with .claude/ into yap/reference/ without renaming
**Tags:** #hooks #yap #reference #gotcha
**Context:** Cloned gastown into `yap/reference/gastown`. The clone has its own `.claude/hooks/`. Shell CWD moved into the clone dir during `git clone`, and Claude Code's hook resolution found the *clone's* `.claude/` instead of ours. Every subsequent tool call failed — hooks couldn't find their scripts. Had to ask the user to manually fix it. Twice.
**Decision:** Always rename `.claude` → `dot-claude` atomically with the clone: `git clone <repo> yap/reference/<name> && mv yap/reference/<name>/.claude yap/reference/<name>/dot-claude`. This matches the existing pattern (e.g., Continuous-Claude-v3/dot-claude/).
**Implications:** Any future reference repo additions must follow this pattern. Consider adding a helper script to `yap/reference/` or updating `update_repos.sh`.

## 2026-02-07: Parallel agents skip quality gate — need pre-push hook
**Tags:** #agents #quality-gate #hooks #process-gap
**Context:** Dispatched two parallel agents to implement protocol tasks. Both committed and pushed code that passed tests but failed golangci-lint, go-arch-lint, and gofumpt. Pre-commit hook only checks per-file lint on staged files, not cross-cutting checks.
**Decision:** Created P0 task (oro-t3u) for a pre-push hook that runs the full 18-check quality gate. Agent prompts must also include quality gate as a task completion step.
**Implications:** Until the hook exists, manually run `./quality_gate.sh` after agent work before pushing. Never trust agent commits without verification.

## 2026-02-07: Use task annotations, not output files, for agent results
**Tags:** #agents #beads #dispatching #file-debt
**Context:** Dispatching skill recommended agents write `docs/agent-output-*.md` files. This creates file debt — orphan files nobody cleans up, duplicating what task annotations already capture.
**Decision:** Agents close tasks with `bd close <id> --reason="summary"`. Task completion notifications provide session-level summaries. Updated dispatching-parallel-agents skill.
**Implications:** No output files to clean up. The task is the durable, queryable record. Fallback to tmp files only when no issue tracker exists.

## 2026-02-07: go-arch-lint pkg component needs self-dependency
**Tags:** #go #go-arch-lint #gotcha
**Context:** `pkg/protocol` tests import `oro/pkg/protocol` (external test package). go-arch-lint flagged this as "pkg shouldn't depend on oro/pkg/protocol" because `pkg` component had no `mayDependOn` rule for itself.
**Decision:** Added `pkg: mayDependOn: [pkg]` to `.go-arch-lint.yml`.
**Implications:** Any new top-level component needs a self-dependency rule if its tests use external test packages.

## 2026-02-07: bash ((PASS++)) kills scripts under set -e
**Tags:** #bash #gotcha #set-e
**Context:** quality_gate.sh silently exited after the first check with no error message.
**Discovery:** `((PASS++))` returns exit code 1 when PASS is 0 (0 is falsy in bash arithmetic). Combined with `set -e`, this silently kills the script. No error, no output — just stops.
**Fix:** Use `PASS=$((PASS + 1))` instead. This always returns exit 0.
**Implications:** Never use `((var++))` in `set -e` scripts. This is a well-known bash trap but produces zero diagnostic output, making it hard to debug.

## 2026-02-07: go-arch-lint v3 config — correct key is mayDependOn, excludeFiles uses regex
**Tags:** #go #tooling #go-arch-lint
**Context:** Tried `canDependOn`, `anyDependOn` — both rejected as unknown keys. `excludeFiles` uses Go regex, not globs — `**` is invalid regex.
**Discovery:** v3 config uses `mayDependOn` for dependency rules. `excludeFiles` takes Go regex patterns: `"yap/.+"` works, `"yap/**"` doesn't. Internal packages importing themselves (e.g., test files) requires `internal` in its own `mayDependOn` list.
**Implications:** Always check `go-arch-lint schema` for valid config keys. Use `.+` not `**` in excludeFiles.

## 2026-02-07: biome v2 config — no ignore field in files section
**Tags:** #tooling #biome #json
**Context:** biome v2.3.11 rejected `ignores` key in `files` section. Only `includes`, `maxSize`, `ignoreUnknown`, `experimentalScannerIgnores` are valid.
**Discovery:** biome v2 removed the simple `ignore` field. VCS integration (`useIgnoreFile: true`) handles gitignored dirs, but submodules and tracked dirs need explicit scoping in the CLI command or `experimentalScannerIgnores`.
**Fix:** Scope biome in the quality gate command to project source and config paths rather than scanning `.`.
**Implications:** When biome can't exclude via config, scope via CLI args. Always run `biome migrate --write` after version bumps.

## 2026-02-07: Quality gate scoping — never scan . for tools that walk directories
**Tags:** #tooling #quality-gate #architecture
**Context:** gofumpt, goimports, biome, go-arch-lint all hung or failed when scanning `.` because `references/` and `yap/` contain thousands of files from submodules and reference repos.
**Decision:** Every tool in quality_gate.sh must be explicitly scoped to source directories (GO_DIRS, explicit paths) — never `.` or `./...` for tools that walk the filesystem. Go toolchain (`go test ./...`, `go build ./...`) is fine because Go respects module boundaries.
**Implications:** When adding new tools to the gate, always specify explicit directories. Test with the full repo, not just src dirs.

## 2026-02-07: golangci-lint v2 gofumpt version mismatch
**Tags:** #go #tooling #golangci-lint #gofumpt
**Context:** During oro-fza foundation setup, files formatted by standalone `gofumpt` (v0.9.2) were still flagged as "not properly formatted" by golangci-lint v2.8.0's bundled gofumpt formatter.
**Decision:** Don't enable gofumpt as a golangci-lint formatter. Run standalone `gofumpt` in the quality gate and Makefile instead. Keep golangci-lint for linting only.
**Implications:** Formatting and linting are separate concerns with separate tools. Version coupling between golangci-lint's bundled formatters and standalone tools causes false positives. Also: golangci-lint v2 moved formatters to a `formatters:` section (not `linters:`), and requires `version: "2"` at the top of the config.

## 2026-02-07: Add reflection step to finishing-work
**Tags:** #skills #workflow #feedback-loops
**Context:** Reviewed aleiby/claude-skills/tackle — its reflect phase logs friction after every PR as queryable data
**Decision:** Added Step 4 (Reflect) to finishing-work skill. Captures off-script moments, slowdowns, and improvement suggestions before cleanup.
**Implications:** Skills can self-improve over time if friction is consistently logged. "Clean run" should be rare — most work has learnable friction.

## 2026-02-07: Skip autoskill pattern (user-correction-driven learning)
**Tags:** #skills #decisions #philosophy
**Context:** Reviewed AI-Unleashed/Claude-Skills/autoskill — watches for user corrections during sessions and proposes skill edits
**Decision:** Not adopted. We prefer self-directed reflection (agent notices its own friction) over user-directed correction harvesting.
**Implications:** The reflect step in finishing-work is our feedback loop. Keep it self-directed.

## 2026-02-07: Memory system — SQLite + hybrid extraction, not JSONL
**Tags:** #memory #architecture #decisions
**Context:** Resolving open questions in memory system spec. JSONL was proposed for simplicity but retrieval (finding the right 3 memories for a 200-token prompt budget) is the hard problem, not storage. Keyword grep can't rank results.
**Decision:** Single SQLite DB (`.oro/state.db`) for both runtime state and memories. FTS5 for BM25 ranked search. Embeddings column reserved for future semantic search. Hybrid extraction: worker self-report markers (real-time) + daemon post-session extraction (background) + periodic consolidation. LanceDB rejected — no Go SDK.
**Implications:** One DB, one Go driver, one dependency. Memories not git-tracked (SQLite binary doesn't diff). Human-curated knowledge goes to `docs/decisions&discoveries.md`. CC-v3's retrieval architecture on a local-first backend.

## 2026-02-07: Create review-docs and review-implementation skills
**Tags:** #skills #review #quality
**Context:** Reviewed Xexr/marketplace review-documentation (1200-line multi-LLM orchestration) and review-implementation skills
**Decision:** Created two lean skills (<300 words each). Extracted structured review categories and severity-weighted output format from Xexr. Skipped multi-LLM dispatch, mermaid diagrams, execution checklists — Claude Code only.
**Implications:** Doc review and implementation review are separate concerns with different triggers. Both use read-only review phase before fixes.

## 2026-02-07: Brainstorming research guard — skill discipline, not hook
**Tags:** #hooks #skills #brainstorming #decisions
**Context:** Evaluated whether to enforce "read reference implementations before proposing designs" via a PreToolUse hook or via skill discipline (oro-2md). Analyzed existing hooks (worktree_guard, inject_context_usage, memory_capture) for patterns.
**Decision:** Skill-only enforcement. A hook is not viable for three reasons: (1) Design proposals are text output, not tool calls -- there is no tool to intercept that signals "proposing a design without research." (2) Determining what counts as "sufficient research" requires judgment (how many files? which files?) that cannot be reduced to a mechanical check. (3) The hook would fire on every Edit/Write call and cannot distinguish brainstorming context from routine edits, producing overwhelming false positives. Instead, strengthened the brainstorming skill with an explicit research gate: a mandatory checklist that must be completed before Step 3 (Explore Approaches), a "what counts as research" table, and a self-check question ("Can I cite specific files I read?").
**Implications:** Not every process enforcement belongs in a hook. Hooks work for mechanical invariants (cwd inside worktree, context token count, git command patterns). Judgment-dependent gates belong in skills where the agent self-enforces with clear checklists. The brainstorming skill's research gate is now the strongest-worded step in the skill.

## 2026-03-04: Early-exit worker pattern is already-done detection
**Tags:** #workers #debugging #oro-work
**Context:** Handoff flagged "4 tool calls then quit" pattern in worker runs. Investigated all available worker logs (oro-tm8m.2, .14, .17, .19).
**Discovery:** All early-exit runs were the same root cause — code already implemented on main. Workers confirmed AC satisfied, produced zero commits, oro work reported failure. No distinct "premature quit" pattern exists beyond already-done detection. Scanner buffer overflow (fixed with 10MB buffer in prior session) may have been a separate historical issue but is no longer reproducing.
**Implications:** Fixed in commit 1393331 (bd-oro-ummw). `noCommitsResult` now parses structured AC (Test:/Cmd: fields), verifies test file exists, runs the specific AC command, and closes the task on success. No further investigation needed.
