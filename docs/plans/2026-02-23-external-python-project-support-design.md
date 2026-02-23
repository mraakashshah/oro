# External Python Project Support

**Date:** 2026-02-23
**Status:** Draft
**Depends on:** Epic B (oro-8ivg, quality gate generation), Epic C (oro-qh9b, config-driven worker prompts)

## Problem

Oro currently works only for oro development. Running `oro start` or `oro work` on an external Python project fails because:

1. Workers reference `./quality_gate.sh` which doesn't exist in the target project
2. No `.claude/settings.json` is generated for interactive sessions (and we shouldn't clobber existing ones)
3. Memories from all projects bleed into each other via a single global `state.db`
4. `oro setup` installs Go tools for Python-only projects

Epics B (quality gate generation) and C (config-driven worker prompts) address the *content* of the quality gate and worker prompts respectively. This spec addresses the remaining gaps: deployment, interactive sessions, memory isolation, and tool filtering.

## Non-Goals

- Beacon changes: architect/manager beacons describe the oro swarm, not the target codebase. They're correct as-is.
- Skill/CLAUDE.md changes: these describe oro's workflow conventions, applicable regardless of target project.
- Published release/installer: user builds from source for now.
- Linux support: macOS only.

## Design

### 1. Quality Gate Deployment

**Decision:** Generate `quality_gate.sh` in the target project root during `oro setup` (and `oro init`). Risks accepted: user can edit the script; `oro setup --force` regenerates; default behavior preserves user edits.

#### Mechanism

`oro setup` Phase 4 (bootstrap) gains a new sub-step after `bootstrapProject()`:

1. Read `langprofile.Config` from Phase 2 detection results
2. Call `generateQualityGate(cfg)` — produces a shell script with only the detected language lanes
3. Write to `<project-root>/quality_gate.sh` (mode 0755)
4. Only generate if file is missing. `oro setup --force` regenerates.

`oro init` also generates the quality gate (for `oro work` usage without full `oro setup`).

#### Generated Script Structure

The generated `quality_gate.sh` follows the same architecture as oro's own (parallel lanes, tiered checks, bail on first failure) but is tailored to detected languages:

**Python-only project gets:**
- Python lane (5 tiers): `ruff format --check` → `ruff check` → `pyright` → `pytest` → (mutation testing placeholder, skipped by default)
- Shell lane: `shellcheck` on `.sh` files
- Docs lane: `markdownlint-cli2`, `yamllint`
- No Go lane, no `make stage-assets`, no Go-specific tooling

**Go+Python project gets:** Both lanes.

The generation function reads tool names and test commands from `langprofile.Config`, so projects with custom tools (e.g., black instead of ruff, detected via `pyproject.toml [tool.black]`) get the right commands.

#### Files

- `cmd/oro/quality_gate_gen.go` — add `generateQualityGate(cfg *langprofile.Config) (string, error)`
- `cmd/oro/cmd_setup.go` — call generation in Phase 4
- `cmd/oro/cmd_init.go` — call generation in `bootstrapProject()`

#### Relationship to Epic B

Epic B (oro-8ivg) builds the per-language config generators:
- `.golangci.yml` generation (done: oro-8ivg.2)
- `pyproject.toml` tool sections generation (done: oro-8ivg.3)
- Quality gate template generation (ready: oro-8ivg.1)

This spec's quality gate deployment step *consumes* Epic B's output. Epic B.1 generates the shell script template; this spec wires the call into `oro setup`/`oro init` and writes the file to disk.

### 2. `oro shell` — Interactive Sessions with Oro Hooks

**Decision:** New `oro shell` command instead of modifying `.claude/settings.json`. Risks accepted: user must remember to use `oro shell` instead of bare `claude` when they want oro hooks. Benefit: zero interference with existing project settings.

#### Mechanism

`oro shell` is a thin wrapper that launches:

```
claude --add-dir ~/.oro --settings ~/.oro/projects/<name>/settings.json
```

#### Behavior

- `oro shell` — interactive claude with oro hooks, skills, memory capture
- `oro shell --resume` — passes `--resume` through to claude
- Resolves project name from `.oro/config.yaml` in current directory
- Errors if `.oro/config.yaml` missing ("run `oro init` first")
- Errors if `~/.oro/projects/<name>/settings.json` missing ("run `oro setup` first")

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
ALTER TABLE memories ADD COLUMN project TEXT DEFAULT '';
CREATE INDEX idx_memories_project_type ON memories(project, type);
UPDATE memories SET project = 'oro' WHERE project = '';
```

Migration runs at `Store` open time, same pattern as existing migrations.

#### Insert Path

- `NewStore()` takes a `project string` parameter
- All `Insert()` calls automatically stamp the store's project on new memories
- `defaultMemoryStore()` in `store.go` reads `ORO_PROJECT` env var, passes to `NewStore()`
- Workers already have `ORO_PROJECT` set by the dispatcher — their memories get tagged automatically

#### Search Path

- `Search()` and `HybridSearch()` add `AND project = ?` filter when project is set on the store
- `ForPrompt()` scopes to current project automatically
- FTS5 index is unaffected (FTS doesn't index the project column; filtering is on the outer query)

#### Cross-Project Escape Hatch

- `oro recall --all-projects <query>` — searches all memories across projects
- `oro memories list --all-projects` — lists all
- Useful for transferring learnings between projects

#### Files

- `pkg/memory/memory.go` — migration, `project` field on `Store`, filter in `Search`/`HybridSearch`/`ForPrompt`
- `cmd/oro/store.go` — pass `ORO_PROJECT` to `NewStore()`
- `cmd/oro/cmd_recall.go` — add `--all-projects` flag
- `cmd/oro/cmd_remember.go` — project stamped automatically
- `cmd/oro/cmd_memories.go` — add `--all-projects` flag

### 4. Language-Filtered Tool Install

**Decision:** Tag tool defs with language, filter in `oro setup` Phase 3. Risks accepted: prerequisite tools (go, python3, node, npm, brew) always checked regardless of detected languages.

#### Tool Def Tagging

Add `Lang string` field to the tool def struct. Tags:

| Tag | Tools |
|-----|-------|
| `"prerequisite"` | go, python3, node, npm, brew |
| `"go"` | gofumpt, goimports, golangci-lint, go-arch-lint, govulncheck |
| `"python"` | uv, ruff, pyright |
| `"system"` | tmux, shellcheck, biome, jq, ast-grep, bd |

#### Setup Phase 3 Filtering

After Phase 2 detects languages:
1. Build active tag set from `langprofile.Config` keys (e.g., `{"python"}`)
2. Always include `"prerequisite"` and `"system"`
3. Filter `defaultToolDefs` to only tools whose `Lang` is in the active set
4. Python-only project: skips gofumpt, goimports, golangci-lint, go-arch-lint, govulncheck

#### Init Tool Report

`oro init` reports all tools but marks language-irrelevant ones as "skipped (no Go detected)" rather than "missing." Clear signal to the user about what matters.

#### Files

- `cmd/oro/cmd_init.go` — add `Lang` field, tag all 19 defs, filter logic, adjust init report

## Dependency Map

```
Epic B (quality gate generation)
  └─ oro-8ivg.1: Generate quality_gate.sh template ← consumed by Gap 1

Epic C (config-driven worker prompts)
  └─ oro-qh9b.1: ReadConfig + ResolveProjectRoot  ← consumed by Gap 1 (config reading)

This Spec:
  Gap 1: Quality gate deployment ← depends on Epic B.1, C.1
  Gap 2: oro shell              ← no dependencies
  Gap 3: Memory scoping         ← no dependencies
  Gap 4: Tool filtering         ← no dependencies
```

Gaps 2, 3, and 4 are independent of each other and of Epics B/C. Gap 1 depends on Epic B.1 completing (the generation function must exist before we can wire the deployment).

## Testing Strategy

- **Gap 1**: Generate quality gate for Python-only, Go-only, Go+Python configs. Verify Python-only has no Go lane, no `make` targets. Run the generated script in a Python project.
- **Gap 2**: Test `oro shell` resolves project, passes flags, errors on missing config.
- **Gap 3**: Insert memories with two different projects, verify recall scopes correctly. Verify `--all-projects` returns both. Verify backfill migration.
- **Gap 4**: Detect Python-only project, verify Go tools filtered from install list. Verify init report shows "skipped" vs "missing."
