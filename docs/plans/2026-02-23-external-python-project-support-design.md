# External Python Project Support

**Date:** 2026-02-23
**Status:** Reviewed (adversarial review pass 3)
**Depends on:** Epic B (oro-8ivg, quality gate generation), Epic C (oro-qh9b, config-driven worker prompts)

## Problem

Oro currently works only for oro development. Running `oro start` or `oro work` on an external Python project fails because:

1. Workers reference `./quality_gate.sh` which doesn't exist in the target project
2. No mechanism for interactive Claude sessions with oro hooks (and we shouldn't clobber existing `.claude/settings.json`)
3. Memories from all projects bleed into each other via a single global `state.db`
4. `oro setup` installs Go tools for Python-only projects

Epics B (quality gate generation) and C (config-driven worker prompts) address the *content* of the quality gate and worker prompts respectively. This spec addresses the remaining gaps: deployment, interactive sessions, memory isolation, and tool filtering.

## Prerequisites

- **Merge conflict in `cmd/oro/quality_gate_gen.go`** must be resolved before any work on this spec. The file currently has unresolved conflict markers from a stale rebase on `agent/oro-8ivg.1`.
- **Epic C must complete** for the end-to-end feature to work. Worker prompts currently hardcode Go-specific coding rules (gofumpt, golangci-lint) that would confuse agents working on Python-only projects. Epic C makes `AssemblePrompt` read from `.oro/config.yaml`. **Gate**: `oro start` preflight should warn if worker prompts are not yet config-driven (i.e., Epic C is not deployed).

## Non-Goals

- Beacon changes: architect/manager beacons describe the oro swarm, not the target codebase. They're correct as-is.
- Skill/CLAUDE.md changes: these describe oro's workflow conventions, applicable regardless of target project.
- Published release/installer: user builds from source for now.
- Linux support: macOS only.
- Language override mechanism (e.g., "detected Go but I only want Python tools"): YAGNI.

## Design

### 1. Quality Gate Deployment

**Decision:** Generate `quality_gate.sh` in the target project root during `oro setup` (and `oro init`). Risks accepted: user can edit the script; `oro setup --force` regenerates; default behavior preserves user edits.

#### Mechanism

`oro setup` Phase 4 (bootstrap) gains a new sub-step after `bootstrapProject()`:

**Plumbing change**: `setupPhase2Detect()` currently returns void. It must be modified to return `*langprofile.Config` so Phase 3 (tool filtering) and Phase 4 (quality gate generation) can consume it. Similarly, `createProjectAnchor()` calls `langprofile.GenerateConfig()` internally but doesn't return the config — it must return `*langprofile.Config` to avoid redundant detection.

Steps:
1. Read `langprofile.Config` from Phase 2 detection results
2. Call `generateQualityGateScript(cfg)` — produces a shell script with only the detected language lanes
3. Write atomically to `<project-root>/quality_gate.sh` (write to `.quality_gate.sh.tmp`, then `os.Rename`, mode 0755) — prevents partial writes on disk-full/crash
4. Only generate if file does not exist (`os.Stat` check). `oro setup --force` regenerates even if present. Adding a new language after initial setup requires `--force`.
5. **Print a reminder**: "quality_gate.sh generated — commit it to git so workers in worktrees can access it"

`oro init` also generates the quality gate (for `oro work` usage without full `oro setup`).

#### Worktree Visibility (Critical)

Workers run in worktrees created from HEAD. The generated `quality_gate.sh` must be committed to git before `oro start` or `oro work` can function. `RunQualityGate()` in `pkg/worker/worker.go` falls back to `git checkout HEAD -- quality_gate.sh` if the file is missing from the worktree — this fails if the file isn't tracked.

The deployment step prints a clear reminder but does NOT auto-commit (that's the user's responsibility — auto-committing unrelated files violates the principle of least surprise). The `oro start` preflight should warn if `quality_gate.sh` is untracked.

#### Generated Script Structure

The generated script follows the same architecture as oro's own (parallel lanes, tiered checks, bail on first failure) but is tailored to detected languages.

**Python-only project gets:**
- Python lane (5 tiers): `ruff format --check` → `ruff check` → `pyright` → `pytest` → (mutation testing placeholder, skipped by default)
- Shell lane: `shellcheck` on `.sh` files (skipped if no `.sh` files found)
- Docs lane: `markdownlint-cli2`, `yamllint` — **only if tools are available** (check `command -v` before running, skip gracefully). No dependency on `node_modules`.
- No Go lane, no `make stage-assets`, no Go-specific tooling

**Go+Python project gets:** Both lanes.

**Zero languages detected:** `oro init` prints a warning ("no languages detected — quality gate will be minimal") and generates a script with only the shell + docs lanes. This is not an error. **Implementation note**: `generateQualityGateScript()` currently returns an error on zero languages — this must be changed to produce a minimal gate instead.

Note: The current `generateQualityGateScript` implementation (Epic B.1) uses boolean `HasGo`/`HasPython` switches with hardcoded tool names in the template. This is sufficient for now. Config-driven tool substitution (reading exact tool names from `langprofile.Config`) is a future enhancement, not required for this spec.

**Implementation requirement for Epic B.1**: The docs lane in the generated template must guard each tool with `command -v` before running (e.g., `command -v markdownlint-cli2 >/dev/null 2>&1 && markdownlint-cli2 ...`). External projects will not have `node_modules`. No dependency on `$NODE_BIN` paths — use globally installed tools or skip gracefully.

#### Files

- `cmd/oro/quality_gate_gen.go` — `generateQualityGateScript(cfg)` already exists (Epic B.1); modify docs lane to use `command -v` guards; modify zero-language case to produce minimal gate instead of error
- `cmd/oro/cmd_setup.go` — modify `setupPhase2Detect()` to return `*langprofile.Config`; wire generation call into Phase 4
- `cmd/oro/cmd_init.go` — modify `createProjectAnchor()` to return `*langprofile.Config`; wire generation call into `bootstrapProject()`
- `cmd/oro/preflight.go` — add warning if `quality_gate.sh` is untracked in git

#### Relationship to Epic B

Epic B (oro-8ivg) builds the per-language config generators:
- `.golangci.yml` generation (done: oro-8ivg.2)
- `pyproject.toml` tool sections generation (done: oro-8ivg.3)
- Quality gate script generation (ready: oro-8ivg.1)

This spec's deployment step *consumes* Epic B's output. Epic B.1 generates the script content; this spec wires the call into `oro setup`/`oro init` and writes the file to disk.

### 2. `oro shell` — Interactive Sessions with Oro Hooks

**Decision:** New `oro shell` command instead of modifying `.claude/settings.json`. Risks accepted: user must remember to use `oro shell` instead of bare `claude` when they want oro hooks. Benefit: zero interference with existing project settings.

#### Mechanism

`oro shell` resolves the project, sets environment, and launches claude:

```bash
export ORO_HOME=~/.oro
export ORO_PROJECT=<name>
exec claude --add-dir ~/.oro --settings ~/.oro/projects/<name>/settings.json [extra args...]
```

Setting `ORO_PROJECT` is critical — hooks like `memory_capture.py`, `learning_reminder.py`, and `session_start_extras.py` read it to determine project context. Without it, hooks fall back to legacy behavior.

#### Behavior

- `oro shell` — interactive claude with oro hooks, skills, memory capture
- `oro shell --resume` — passes `--resume` through to claude
- `oro shell -- <any claude args>` — passes all args after `--` through to claude
- Resolves project name from `.oro/config.yaml` in current directory
- Errors if `.oro/config.yaml` missing ("run `oro init` first")
- Errors if `.oro/config.yaml` exists but is malformed YAML or has empty/missing `project` field (standard Go error with descriptive message)
- Errors if `~/.oro/projects/<name>/settings.json` missing ("run `oro setup` first")
- Safe to run while `oro start` is active — SQLite WAL mode handles concurrent reads/writes. `oro shell` and workers may both write memories; WAL serializes writes correctly. Application-level dedup (Jaccard similarity check in `Insert()`) is not atomic with concurrent writers, but duplicate memories are a minor nuisance, not data corruption.

#### What It Does NOT Do

- No daemon interaction — independent of `oro start`
- No worktree creation — works directly in the project
- No bead assignment — just claude with oro's hooks and skills

#### Files

- `cmd/oro/cmd_shell.go` — new command
- `cmd/oro/root.go` — register command

### 3. Memory Scoping by Project

**Decision:** Add `project` column to existing `memories` table in `state.db`. Risks accepted: schema migration on live DB (mitigated by running at store open before dispatcher starts). Backfill existing rows as `project = 'oro'`.

#### Schema Migration

```sql
-- Each statement runs independently with try/ignore (same pattern as existing migrations).
-- SQLite silently ignores ALTER TABLE if column already exists on re-run.
ALTER TABLE memories ADD COLUMN project TEXT DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_memories_project_type ON memories(project, type);
UPDATE memories SET project = 'oro' WHERE project = '';
```

Migration runs at `Store` open time, same pattern as existing migrations (`MigrateFileTracking`, `MigratePinnedMemories`). Each statement is independent and idempotent — re-running is safe.

#### Store API Change

To minimize blast radius, use a setter method instead of changing the `NewStore()` constructor signature:

```go
store, err := memory.NewStore(db)  // unchanged signature
store.SetProject("myapp")          // new method, optional
```

- `SetProject(string)` sets the project field on the Store instance
- If project is empty string, the `WHERE project = ?` clause is **omitted entirely** from queries (not `WHERE project = ''`). Insert does not stamp a project tag. This means: no filtering, no tagging — identical to current behavior. Backwards compatible.
- All existing callers (15+ test files, dispatcher, CLI commands) continue to work unchanged
- Only callers that need scoping call `SetProject()`

#### Callers That Need `SetProject()`

- `defaultMemoryStore()` in `cmd/oro/store.go` — reads `ORO_PROJECT` env var, calls `SetProject()`
- Dispatcher in `pkg/dispatcher/dispatcher.go` — reads `ORO_PROJECT` env var (already set by `oro start`), calls `SetProject()`
- `oro shell` sets `ORO_PROJECT` in env before launching claude, so hooks that use the store inherit it

#### Project Resolution for CLI Commands

When a user runs `oro recall <query>` from their terminal (not inside `oro shell` or `oro start`), `ORO_PROJECT` is typically not set. Resolution order:

1. `ORO_PROJECT` env var (set by `oro start`, `oro shell`, and worker processes)
2. Read `.oro/config.yaml` in current directory to get project name
3. If neither available: no project filter applied (returns all memories, same as `--all-projects`)

This ensures `oro recall` "just works" from any context.

#### Cross-Project Escape Hatch

- `oro recall --all-projects <query>` — searches all memories across projects
- `oro memories list --all-projects` — lists all
- Useful for transferring learnings between projects

#### Files

- `pkg/memory/memory.go` — migration, `SetProject()` method, `project` field on `Store`, filter in `Search`/`HybridSearch`/`ForPrompt`/`Insert`
- `pkg/protocol/schema.go` — update `SchemaDDL` to include `project TEXT DEFAULT ''` in the `CREATE TABLE memories` statement (so new databases get the column without migration)
- `cmd/oro/store.go` — call `SetProject()` with resolved project name (env → `.oro/config.yaml` → empty)
- `cmd/oro/cmd_recall.go` — add `--all-projects` flag
- `cmd/oro/cmd_remember.go` — project stamped automatically via store
- `cmd/oro/cmd_memories.go` — add `--all-projects` flag
- `pkg/dispatcher/dispatcher.go` — call `SetProject()` on its memory store

### 4. Language-Filtered Tool Install

**Decision:** Use existing `Category` field on `toolDef` for filtering in `oro setup` Phase 3. No new field needed.

#### Existing Category Values

The `toolDef` struct already has a `Category string` field with these values:
- `"prerequisites"`: go, python3, node, npm, brew
- `"go-tools"`: gofumpt, goimports, golangci-lint, go-arch-lint, govulncheck
- `"python-tools"`: uv, ruff, pyright
- `"system"`: tmux, shellcheck, biome, jq, ast-grep, bd

#### Setup Phase 3 Filtering

After Phase 2 detects languages:
1. Build active category set from `langprofile.Config` keys: `{"python"}` → `{"python-tools"}`
2. Always include `"prerequisites"` and `"system"`
3. Filter `defaultToolDefs` to only tools whose `Category` is in the active set
4. Python-only project: skips all `"go-tools"` (gofumpt, goimports, golangci-lint, go-arch-lint, govulncheck)

#### Init Tool Report

`oro init` reports all tools but marks language-irrelevant ones as "skipped (no Go detected)" rather than "missing." Clear signal to the user about what matters. **Skipped tools do NOT count toward the missing-tool exit code.** A Python-only project with all Python tools installed exits 0 even though Go tools are absent.

#### Error Path

If Phase 2 language detection returns zero languages, Phase 3 installs only `"prerequisites"` and `"system"` tools. A warning is printed ("no languages detected — only installing system tools"). This is not an error — the user may be setting up a docs-only project.

#### Files

- `cmd/oro/cmd_init.go` — filter logic using existing `Category` field, adjust init report formatting
- `cmd/oro/cmd_setup.go` — pass detected languages to Phase 3

## Dependency Map

```
Epic B (quality gate generation)
  └─ oro-8ivg.1: Generate quality_gate.sh template ← consumed by Gap 1

Epic C (config-driven worker prompts)
  └─ Makes AssemblePrompt read coding_rules from config ← required for end-to-end

This Spec:
  Gap 1: Quality gate deployment ← depends on Epic B.1
  Gap 2: oro shell              ← no dependencies
  Gap 3: Memory scoping         ← no dependencies
  Gap 4: Tool filtering         ← no dependencies
```

Gaps 2, 3, and 4 are independent of each other and of Epics B/C. Gap 1 depends on Epic B.1 completing (the generation function must exist before we can wire the deployment).

## Testing Strategy

- **Gap 1**: Generate quality gate for Python-only, Go-only, Go+Python, and zero-language configs. Verify Python-only has no Go lane, no `make` targets. Verify docs lane skips unavailable tools. Verify preflight warns on untracked `quality_gate.sh`. Run the generated script in a Python project.
- **Gap 2**: Test `oro shell` resolves project, sets `ORO_PROJECT`, passes flags through, errors on missing config. Test it works while `oro start` is running (concurrent SQLite access).
- **Gap 3**: Insert memories with two different projects, verify recall scopes correctly. Verify `--all-projects` returns both. Verify backfill migration idempotency (run twice). Verify `SetProject("")` disables filtering (backwards compat). Verify CLI project resolution fallback (env → config → unscoped).
- **Gap 4**: Detect Python-only project, verify Go tools filtered from install list. Verify init report shows "skipped" vs "missing." Verify zero-language detection falls back to prerequisites + system only.

## Adversarial Review Log

### Pass 1 — FAIL
Key findings addressed:
- quality_gate.sh must be committed to git for worktree visibility → added reminder + preflight warning
- `oro shell` must set `ORO_PROJECT` env var → added to mechanism
- `ORO_PROJECT` unset in bare CLI → added fallback resolution (env → config → unscoped)
- Dispatcher creates its own Store, needs project → added to callers list
- Docs lane needs node_modules → changed to check tool availability, skip gracefully
- `NewStore()` signature change blast radius → changed to `SetProject()` method
- Spec proposed `Lang` field but `Category` already exists → reuse `Category`
- Ambiguity on "missing" vs "outdated" quality gate → clarified as `os.Stat` check, `--force` to regenerate
- Zero-language detection unspecified → added explicit behavior (warning + minimal gate)
- Config-driven tool substitution ambiguity → clarified: static templates with HasGo/HasPython for now

### Pass 2 — FAIL
Key findings addressed:
- Docs lane template hardcodes `$NODE_BIN` paths → added explicit implementation requirement for Epic B.1 to use `command -v` guards
- `generateQualityGateScript()` errors on zero languages → noted as implementation change (produce minimal gate, not error)
- `setupPhase2Detect()` returns void, can't pass config to Phase 3/4 → added plumbing change note (return `*langprofile.Config`)
- `createProjectAnchor()` doesn't return config → added to files section
- No gate preventing Gaps 1-4 without Epic C → added preflight gate to prerequisites
- `protocol/schema.go` SchemaDDL missing project column for new databases → added to files list
- Empty project ambiguity (`WHERE project = ''` vs omit clause) → clarified: omit clause entirely
- Skipped tools and exit code → clarified: skipped tools don't count
- `oro shell` error paths for malformed config → added
- Atomic write for quality gate → added write-tmp-rename pattern
- Memory hook env inheritance → clarified: hooks inherit ORO_PROJECT from parent process; concurrent dedup is non-atomic but benign

### Remaining known limitations (accepted)
- Worker prompt Go-specific instructions require Epic C (gated by preflight warning)
- Application-level memory dedup is not atomic under concurrent writes (WAL handles DB integrity; duplicate memories are benign)
- `HEAD` in worktrees resolves to the worktree branch, not main — quality_gate.sh must be on the branch the worktree was created from (standard git behavior, not an oro-specific concern)
