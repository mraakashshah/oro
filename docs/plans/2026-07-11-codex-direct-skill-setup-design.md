# Codex Direct Skill Setup Design

**Status:** Validated by user on 2026-07-11

## Goal

Make Oro-managed Codex sessions load Oro skills and discipline without depending on marketplace registration, plugin installation, or interactive Codex UI state.

## Research

- `cmd/oro/cmd_global_oro_approach.go` already links portable skills from `~/.oro/.claude/skills` into `$CODEX_HOME/skills` for `oro agent-assets --runtime codex`.
- `cmd/oro/cmd_start.go:ensureRuntimeProjectAssets` installs Codex rules and direct `config.toml` hooks, but currently writes an unused local marketplace package instead of ensuring the skill links.
- `assets/hooks/session_start_global.py` force-loads `using-skills` from `~/.claude/skills`, even though Oro owns the canonical source under `~/.oro/.claude/skills`.
- `docs/learnings/codex-plugin-discovery.md` demonstrates that marketplace presence is not installation: registration and activation are separate, and non-interactive workers cannot rely on either.
- The live machine confirms the direct path works: `$CODEX_HOME/skills` contains Oro symlinks, while the generated Oro plugin is neither registered nor enabled and contains no `skills/` directory.

## Decision

The canonical Codex integration consists of four direct assets:

1. Portable skill symlinks from `~/.oro/.claude/skills/*` to `$CODEX_HOME/skills/*`.
2. A managed hooks block written directly to `$CODEX_HOME/config.toml`.
3. `using-skills` bootstrap content loaded directly from `~/.oro/.claude/skills/using-skills/SKILL.md`.
4. Project `AGENTS.md` generated from Oro's shared agent instructions.

`oro agent-assets --runtime codex` and every `oro start` configuration capable of launching any Codex subprocess must ensure the skill symlinks. The startup predicate must cover the `ORO_AGENT_RUNTIME=codex` override and Codex assigned to any configured CLI tier or role, including mixed-provider review modes. Neither path may generate, register, install, or require an Oro marketplace package. Startup must verify that the canonical `using-skills/SKILL.md` source exists before launching a dispatcher that can route to Codex; the explicit asset-sync command retains its current non-fatal missing-source behavior.

The existing marketplace generator and its tests are removed because there is no remaining supported caller or user-facing optional command. Existing `$CODEX_HOME/oro-marketplace` directories are left untouched during upgrades; deleting user-home state is outside normal sync/start behavior.

## Alternatives Considered

### Agent-assets only

Keep direct symlinks only in `oro agent-assets`. This is the smallest change, but a fresh dispatcher-started Codex worker can still lack all Oro skills if the user did not run the separate command. Rejected because worker startup must be self-contained.

### Direct sync during agent-assets and startup

Use the existing link implementation in both entry points. This is the selected option: it is deterministic, idempotent, non-interactive, and keeps edits live through symlinks.

### Copy embedded skills into Codex home

Copy skills rather than link them. This avoids symlink concerns, but duplicates content and can leave stale skills after Oro upgrades. Rejected because the installed `~/.oro` bundle is already the stable source of truth.

## Data Flow

```text
Oro install/update
  -> ~/.oro/.claude/skills/*

oro agent-assets --runtime codex OR Codex-backed oro start
  -> link portable skills into $CODEX_HOME/skills/*
  -> write $CODEX_HOME/rules/oro.rules
  -> write managed hooks block in $CODEX_HOME/config.toml

Codex SessionStart
  -> ~/.oro/hooks/session_start_global.py
  -> read ~/.oro/.claude/skills/using-skills/SKILL.md
  -> inject bootstrap context
```

## Error Handling

- A missing Oro skill source remains a non-fatal no-op for the explicit asset sync, matching current behavior.
- Codex-backed startup treats a missing canonical `using-skills/SKILL.md` or any skill-linking error as fatal because launching an undisciplined worker would violate the runtime contract.
- The bootstrap hook remains fail-open if `using-skills` is missing or unreadable; it still injects the compact discipline block.
- Existing non-Oro entries in `$CODEX_HOME/skills` remain untouched unless they have the same name as an Oro-managed skill, matching current idempotent sync semantics.
- Skill replacement creates a temporary symlink and renames it into place. This makes concurrent startup and explicit sync safe for normal file/symlink destinations; a legacy real directory is removed before the atomic rename.
- Runtime gating uses one shared effective predicate: an explicit `ORO_AGENT_RUNTIME=codex` override is authoritative; otherwise startup inspects every configured CLI tier and role after provider-mode expansion. Claude-only configurations do not mutate `$CODEX_HOME`.

## Premortem

```yaml
premortem:
  mode: deep
  context: "replace Codex marketplace packaging with direct skill setup"
  tigers:
    - risk: "Fresh oro start does not run agent-assets, so Codex starts without Oro skills"
      severity: high
      mitigation_checked: "ensureRuntimeProjectAssets currently installs rules/hooks and marketplace files but does not call copySkills"
      mitigation: "Call the direct skill-link sync from Codex-backed startup and cover it in the end-to-end test"
    - risk: "Codex bootstrap reads Claude's destination instead of Oro's canonical source"
      severity: high
      mitigation_checked: "session_start_global.py hard-codes ~/.claude/skills/using-skills/SKILL.md"
      mitigation: "Load ~/.oro/.claude/skills/using-skills/SKILL.md and test the exact path"
    - risk: "Concurrent startup and explicit sync race between destination removal and symlink creation"
      severity: high
      mitigation_checked: "copySkills currently performs Lstat, RemoveAll, and Symlink as separate operations"
      mitigation: "Create a unique temporary symlink and atomically rename it over file/symlink destinations; add a concurrent sync test"
  elephants:
    - risk: "Old $CODEX_HOME/oro-marketplace directories remain after upgrade"
      mitigation: "Stop generating and documenting them; do not delete user-home state during normal startup"
  paper_tigers:
    - risk: "Symlinks are less portable than copies"
      reason: "Oro already uses and tests symlinks on its supported macOS environment, and live edits are a desired property"
```

## Acceptance Criteria

1. `oro agent-assets --runtime codex` links portable skills into `$CODEX_HOME/skills` and does not create `$CODEX_HOME/oro-marketplace`.
2. A Codex-backed `oro start` links portable skills before launching the dispatcher without requiring marketplace state.
3. The Codex SessionStart hook loads `using-skills` from `~/.oro/.claude/skills` and remains silent when the file is missing.
4. Direct Codex rules, managed hooks, and project `AGENTS.md` behavior continue to pass existing parity tests.
5. User-facing documentation describes direct skill discovery as canonical and contains no marketplace registration instructions.
6. Production code contains no Oro Codex marketplace generator or installer.
7. Codex startup fails before dispatcher launch when the canonical `using-skills/SKILL.md` source is missing.
8. Direct managed-hook tests cover `session_start_global.py`, `enforce_skills.py`, `destructive_command_guard.py`, `oro-search-hook`, `prompt_injection_guard.py`, `auto-format.sh`, `context_pruner.py`, and `stop-checklist.sh` without relying on the removed plugin generator.
9. Concurrent Codex skill syncs converge on valid symlinks without errors.
10. Startup ensures Codex assets when selected by `ORO_AGENT_RUNTIME=codex` or by any configured CLI tier/role, including `claude-coding-codex-review`; Claude-only routing skips Codex asset mutation.

## Implementation Tasks

### Task 1: Direct and required skill synchronization

**Read:** `cmd/oro/cmd_global_oro_approach.go`, `cmd/oro/cmd_global_oro_approach_test.go`, `cmd/oro/cmd_start.go`, `cmd/oro/cmd_start_test.go`, `cmd/oro/end_to_end_codex_test.go`, `cmd/oro/agent_runtime.go`, `pkg/agentruntime/runtime.go`, `pkg/agentmodel/agentmodel.go`, `pkg/agentmodel/agentmodel_test.go`, `pkg/config/agent.go`, `pkg/ops/ops.go`, `pkg/ops/exec_spawner.go`

- Make the shared skill linker optionally require `using-skills/SKILL.md`.
- Replace remove-then-create for file/symlink destinations with temporary-symlink plus rename.
- Call the required linker from `ensureRuntimeProjectAssets` before dispatcher launch.
- Add a shared `agentmodel` query that reports whether any effective CLI tier or role uses Codex, and combine it with the explicit runtime override when deciding whether startup installs Codex assets.
- Add non-skipping tests for successful startup linking, missing-source failure, no marketplace creation, and concurrent sync.
- Put all structural cases under the exact aggregate test `TestCodexDirectSkillSetupAcceptance`; this name and its subtest names are part of the acceptance contract:
  - `startup_links_skills_without_marketplace`
  - `startup_rejects_missing_using_skills`
  - `agent_assets_does_not_create_marketplace`
  - `concurrent_sync_converges`
  - `legacy_directory_recovers_without_temp_links`
  - `runtime_override_links_skills_before_launch`
  - `mixed_provider_links_skills_before_launch`
  - `claude_only_does_not_mutate_codex_home`
- Remove each temporary symlink on every rename/error path and test recovery when a legacy real directory must be removed before replacement.
- Extend the optional Codex end-to-end test to assert the skill symlink and absence of a newly created marketplace directory.

### Task 2: Remove marketplace production paths and preserve direct hook parity

**Read:** `cmd/oro/cmd_global_oro_approach.go`, `cmd/oro/cmd_start.go`, `cmd/oro/cmd_global_oro_approach_test.go`, `cmd/oro/cmd_start_test.go`, `pkg/agentassets/codex.go`, `pkg/agentassets/codex_test.go`, `pkg/agentassets/destructive_guard_test.go`

- Remove `codexPluginRoot`, plugin installation calls, and the now-dead marketplace generator/installer.
- Replace marketplace-oriented command tests with behavioral assertions that no marketplace path is created or mutated.
- Move all Codex hook-command assertions to `codexHookConfigBlock` tests before removing generator-backed coverage.

### Task 3: Canonical bootstrap source

**Read:** `assets/hooks/session_start_global.py`, `tests/test_session_start_global.py`, `assets/hooks/test_parity.py`, `assets/hooks/test_hook_schemas.py`

- Load `using-skills` from `~/.oro/.claude/skills/using-skills/SKILL.md`.
- Update fixtures to create the exact canonical path.
- Preserve silent missing and unreadable-file behavior.
- Regenerate the embedded `cmd/oro/_assets` mirror with `make stage-assets`.

### Task 4: User-facing contract

**Read:** `README.md`, `docs/runbooks/codex-setup.md`, `docs/learnings/codex-plugin-discovery.md`

- Describe `$CODEX_HOME/skills` symlinks, direct hooks, rules, and `AGENTS.md` as the supported integration.
- Remove Oro marketplace creation and registration instructions from current user-facing documentation.
- Retain the historical discovery learning as research context, clearly marking marketplace packaging as rejected for Oro workers.

## Traceability

| Criterion | Task | Verification |
|---|---|---|
| 1 | 1, 2 | aggregate `agent_assets_does_not_create_marketplace` subtest plus existing skill-sync coverage |
| 2 | 1 | aggregate `startup_links_skills_without_marketplace` subtest plus optional end-to-end assertion |
| 3 | 3 | exact-path integration test in `tests/test_session_start_global.py` |
| 4 | 1, 2 | existing rules/AGENTS tests and expanded direct-hook block test |
| 5 | 4 | documentation grep in acceptance command |
| 6 | 2 | production static grep in acceptance command |
| 7 | 1 | aggregate `startup_rejects_missing_using_skills` subtest |
| 8 | 2 | expanded non-skipping `codexHookConfigBlock` test |
| 9 | 1 | aggregate `concurrent_sync_converges` subtest |
| 10 | 1 | aggregate runtime-override, mixed-provider, and Claude-only no-mutation subtests plus `agentmodel` unit coverage |

## Acceptance Test

```bash
# Cmd (run from the repository root):
bash -euo pipefail <<'EOF'
out="$(mktemp)"
trap 'rm -f "$out"' EXIT

go test ./cmd/oro/... -run '^TestCodexDirectSkillSetupAcceptance$' -count=1 -v | tee "$out"
for name in \
  startup_links_skills_without_marketplace \
  startup_rejects_missing_using_skills \
  agent_assets_does_not_create_marketplace \
  concurrent_sync_converges \
  legacy_directory_recovers_without_temp_links \
  runtime_override_links_skills_before_launch \
  mixed_provider_links_skills_before_launch \
  claude_only_does_not_mutate_codex_home
do
  rg -Fq -- "--- PASS: TestCodexDirectSkillSetupAcceptance/$name " "$out"
done

go test ./cmd/oro/... ./pkg/agentassets/...
uv run pytest tests/test_session_start_global.py assets/hooks/test_parity.py assets/hooks/test_hook_schemas.py
! rg -n --glob '!**/*_test.go' \
  'oro-marketplace|InstallCodexPluginPackage|PluginPackage' \
  cmd/oro pkg/agentassets README.md docs/runbooks/codex-setup.md
EOF
```

Assert: the shell exits 0; verbose output contains pass lines—not skip lines—for all eight required aggregate subtests; full direct skill, hook, rules, and AGENTS.md tests pass; no production or user-facing marketplace dependency remains.
```
