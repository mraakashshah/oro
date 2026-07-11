# Codex Direct Skill Setup Implementation Plan

> **For Claude:** Use executing-plans skill to implement this plan task-by-task.

**Goal:** Make every Oro-managed Codex launch discover Oro skills directly without marketplace state.

**Architecture:** Oro keeps `~/.oro/.claude/skills` as its canonical installed source and atomically links portable skills into `$CODEX_HOME/skills`. Startup uses one effective-runtime predicate covering environment overrides and all configured CLI routes, while hooks remain directly managed in `config.toml` and bootstrap `using-skills` directly from the Oro source.

**Tech Stack:** Go 1.26, Python 3.10+, Cobra, pytest, Codex `config.toml` hooks, filesystem symlinks.

## Global Constraints

- Marketplace registration or plugin installation must never be required.
- `ORO_AGENT_RUNTIME=codex` and any effective Codex CLI tier/role require assets before launch.
- Claude-only startup must not mutate `$CODEX_HOME`.
- Codex startup must fail when canonical `using-skills/SKILL.md` is absent; explicit `agent-assets` retains fail-open missing-source behavior.
- Existing unrelated worktree changes, including the untracked terminal-artifact path, must remain untouched.

---

### Task 1: Effective Codex Runtime Detection

**Files:**
- Modify: `pkg/agentmodel/agentmodel.go`
- Test: `pkg/agentmodel/agentmodel_test.go`

**Interfaces:**
- Consumes: existing `loadAgentConfig() loadedConfig`
- Produces: `func UsesRuntime(runtime string) bool`

**Step 1: Write the failing tests**

Add table-driven coverage proving `UsesRuntime("codex")` is true for codex-only, codex-coding, and claude-coding-codex-review configurations; false for claude-only; and true for an explicit custom Codex CLI role or tier. API-only roles do not count.

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/agentmodel -run '^TestUsesRuntime$' -count=1 -v`

Expected: compile failure because `agentmodel.UsesRuntime` does not exist.

**Step 3: Write minimal implementation**

Load the effective config once, scan every tier runtime, then every role with `Transport == "cli"`. Resolve tier-backed roles through the config's tier map. Return early on a match; otherwise false.

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/agentmodel -run '^TestUsesRuntime$' -count=1 -v`

Expected: PASS.

### Task 2: Atomic Direct Skill Linking and Startup Contract

**Files:**
- Modify: `cmd/oro/cmd_global_oro_approach.go`
- Modify: `cmd/oro/cmd_start.go`
- Modify: `cmd/oro/end_to_end_codex_test.go`
- Test: `cmd/oro/cmd_global_oro_approach_test.go`
- Test: `cmd/oro/cmd_start_test.go`

**Interfaces:**
- Consumes: `agentmodel.UsesRuntime(string) bool`, `agentruntime.ReadRuntime() string`
- Produces: `copySkills(agentAssetsConfig, io.Writer) error` with `requireUsingSkills bool` configuration; `codexAssetsRequired() bool`

**Step 1: Write the failing aggregate test**

Create exact test `TestCodexDirectSkillSetupAcceptance` with these exact subtests:

- `startup_links_skills_without_marketplace`
- `startup_rejects_missing_using_skills`
- `agent_assets_does_not_create_marketplace`
- `concurrent_sync_converges`
- `legacy_directory_recovers_without_temp_links`
- `runtime_override_links_skills_before_launch`
- `mixed_provider_links_skills_before_launch`
- `claude_only_does_not_mutate_codex_home`

Use temp Oro/Codex homes and real symlinks. Create a fake executable `oro-search-hook` where startup setup requires it. Snapshot the Claude-only Codex home before and after. Do not call `t.Skip`.

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/oro/... -run '^TestCodexDirectSkillSetupAcceptance$' -count=1 -v`

Expected: failures showing marketplace creation, missing startup links, non-fatal required source, and runtime-gating gaps.

**Step 3: Implement atomic linking**

Add `requireUsingSkills bool` to `agentAssetsConfig`. When required, verify `<oroSkillsDir>/using-skills/SKILL.md` before creating the destination. For each portable skill:

1. Create a unique temporary symlink beside the destination.
2. Defer removal of the temporary path.
3. Remove an existing real directory only when necessary.
4. Rename the temporary symlink over the destination.

Keep missing-source fail-open behavior when `requireUsingSkills` is false.

**Step 4: Wire every Codex-capable startup**

Implement `codexAssetsRequired()` as true when `agentruntime.ReadRuntime()` is explicitly Codex or `agentmodel.UsesRuntime("codex")` is true. In `ensureRuntimeProjectAssets`, return early only when this predicate is false. Before rules/hooks installation, call the required skill linker with source `<oroHome>/.claude/skills` and destination `<codexHome>/skills`.

**Step 5: Remove command-level marketplace installation**

Remove `codexPluginRoot` from `agentAssetsConfig`, its defaults, `installCodexPluginPackage`, and the Codex branch call in `syncAgentRuntimeAssets`. Replace marketplace assertions in existing sync tests with direct-link and no-creation assertions.

**Step 6: Run aggregate test to verify it passes**

Run: `go test ./cmd/oro/... -run '^TestCodexDirectSkillSetupAcceptance$' -count=1 -v`

Expected: all eight exact subtests PASS.

### Task 3: Remove Dead Marketplace Generator and Preserve Hook Coverage

**Files:**
- Delete: `pkg/agentassets/codex.go`
- Delete: `pkg/agentassets/codex_test.go`
- Modify: `pkg/agentassets/destructive_guard_test.go`
- Modify: `cmd/oro/cmd_start_test.go`

**Interfaces:**
- Consumes: `codexHookConfigBlock(string) string`
- Produces: direct hook parity assertions with no plugin generator dependency

**Step 1: Write the failing direct-hook assertions**

Expand `TestCodexHookConfigBlockReplacement` to require commands for `session_start_global.py`, `enforce_skills.py`, `destructive_command_guard.py`, `oro-search-hook`, `prompt_injection_guard.py`, `context_pruner.py`, `auto-format.sh`, `context_block_stop.py`, and `stop-checklist.sh`.

**Step 2: Verify the assertions pass before deletion**

Run: `go test ./cmd/oro -run '^TestCodexHookConfigBlockReplacement$' -count=1 -v`

Expected: PASS, proving the replacement coverage exists before generator removal.

**Step 3: Delete marketplace implementation and tests**

Delete the generator/installer files. Remove only the Codex-generator branch from `pkg/agentassets/destructive_guard_test.go`; retain Claude generator coverage. Fix imports and compile errors without changing direct hook behavior.

**Step 4: Run package tests**

Run: `go test ./cmd/oro/... ./pkg/agentassets/... -count=1`

Expected: PASS.

### Task 4: Canonical Session Bootstrap Source

**Files:**
- Modify: `assets/hooks/session_start_global.py`
- Test: `tests/test_session_start_global.py`
- Regenerate: `cmd/oro/_assets/hooks/session_start_global.py`

**Interfaces:**
- Consumes: `Path.home()`
- Produces: canonical path `~/.oro/.claude/skills/using-skills/SKILL.md`

**Step 1: Change the fixture first**

Update `_run_main` to create the skill under `.oro/.claude/skills/using-skills/SKILL.md`. Add a regression test creating conflicting `.claude` and `.oro` skill content and assert only the Oro content is injected. Preserve missing and unreadable silence tests.

**Step 2: Run test to verify it fails**

Run: `uv run pytest tests/test_session_start_global.py -q`

Expected: canonical-source tests FAIL because production still reads `.claude`.

**Step 3: Change the production path**

Set `skills_file = Path.home() / ".oro" / ".claude" / "skills" / "using-skills" / "SKILL.md"`.

**Step 4: Run test to verify it passes**

Run: `uv run pytest tests/test_session_start_global.py -q`

Expected: PASS.

**Step 5: Regenerate embedded assets**

Run: `make stage-assets`

Expected: embedded hook mirror matches `assets/`.

### Task 5: Update the Supported User Contract

**Files:**
- Modify: `README.md`
- Rewrite: `docs/runbooks/codex-setup.md`
- Modify: `docs/learnings/codex-plugin-discovery.md`

**Interfaces:**
- Produces: current documentation naming direct skills, hooks, rules, and `AGENTS.md` as canonical

**Step 1: Add a failing static documentation check**

Run: `! rg -n 'oro-marketplace|codex plugin marketplace add' README.md docs/runbooks/codex-setup.md`

Expected: FAIL because current docs instruct marketplace setup.

**Step 2: Update documentation**

Remove local marketplace layout and registration instructions. Document `$CODEX_HOME/skills` symlinks, startup enforcement, direct managed hooks, rules, `AGENTS.md`, `CODEX_HOME`, missing-source failure, and that existing legacy marketplace directories are ignored. Mark the discovery learning's marketplace proposal as historical/rejected without deleting empirical research.

**Step 3: Run documentation check**

Run: `! rg -n 'oro-marketplace|codex plugin marketplace add' README.md docs/runbooks/codex-setup.md`

Expected: PASS.

### Task 6: Full Verification and Integration

**Files:**
- Verify all files above

**Interfaces:**
- Produces: acceptance evidence and one atomic implementation commit

**Step 1: Run the design's exact acceptance command**

Run the complete heredoc under “Acceptance Test” in `docs/plans/2026-07-11-codex-direct-skill-setup-design.md`.

Expected: all eight mandatory subtests and all Go/Python suites PASS; static grep finds no production or current-doc marketplace dependency.

**Step 2: Run repository quality checks**

Run: `make fmt && make vet && make test && make lint`

Expected: all commands exit 0.

**Step 3: Review generated and unrelated changes**

Run: `git status --short && git diff --check && git diff --stat && git diff`

Expected: only scoped source, tests, docs, and generated asset mirrors changed; the pre-existing untracked terminal-artifact path is untouched.

**Step 4: Commit and push**

Create small commits as tasks become green, then push `main` after final verification.
