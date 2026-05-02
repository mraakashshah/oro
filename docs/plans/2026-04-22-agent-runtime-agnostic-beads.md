# Agent Runtime Agnostic Bead Decomposition

**Date:** 2026-04-22
**Phase 10 note:** The runtime-agnostic decomposition remains useful for provider work, but any lines that describe bd/Dolt as the current source of truth are historical pre-migration context. Current bead state is the native SQLite beadstore.
**Spec:** [2026-04-22-agent-runtime-agnostic-design.md](./2026-04-22-agent-runtime-agnostic-design.md)
**Epic Slug:** `agent-runtime-agnostic`
**Intent:** Decompose the runtime-agnostic migration into executable Oro beads with explicit dependencies and acceptance criteria.

## Epic

### `epic(agent-runtime-agnostic): make Oro runtime-agnostic across Claude and Codex`

- Type: `epic`
- Priority: `P1`
- Estimate: `240`
- Labels: `runtime`, `codex`, `claude`, `skills`
- Description:
  Make Oro route work through a provider-neutral runtime layer so worker and ops flows can run on Claude or Codex without Claude-specific assumptions in launch paths, prompt routing, asset extraction, or skill bootstrap.
- Acceptance:
  `All child beads closed. Claude remains default runtime when unset. Legacy opus/sonnet/haiku routing still works. Codex path works without Claude hooks installed. Shared Oro skills install for both runtimes. Spec: docs/plans/2026-04-22-agent-runtime-agnostic-design.md`

## Child Beads

### 1. `refactor(runtime): resolve worker and ops runtimes in cmd_start/cmd_work`

- Type: `task`
- Priority: `P1`
- Estimate: `45`
- Depends on: none
- Why first:
  This is the main bypass called out by adversarial review. If this does not land first, the abstraction can exist while production still hard-codes Claude.
- Acceptance:
  `Test: cmd/oro/cmd_work_test.go:TestBuildDepsResolvesRuntime, cmd/oro/cmd_start_test.go:TestBuildDispatcherResolvesOpsRuntime | Cmd: go test ./cmd/oro/... -run 'TestBuildDepsResolvesRuntime|TestBuildDispatcherResolvesOpsRuntime' -count=1 | Assert: production dependency wiring no longer directly constructs worker.ClaudeSpawner or ops.ClaudeOpsSpawner; runtime selection happens through a runtime resolver and Claude remains default when runtime is unset
Read: cmd/oro/cmd_work.go, cmd/oro/cmd_start.go, docs/plans/2026-04-22-agent-runtime-agnostic-design.md
Signature: add runtime resolver helper(s) consumed by cmd_work and cmd_start; remove direct concrete spawner construction from production paths
Edges: unset runtime -> Claude default; unknown runtime -> explicit user-facing error; tests may still construct concrete adapters only inside adapter-focused unit tests`

### 2. `refactor(worker): move worker subprocess launch behind agentruntime adapter`

- Type: `task`
- Priority: `P1`
- Estimate: `45`
- Depends on: bead 1
- Acceptance:
  `Test: pkg/worker/worker_test.go:TestWorkerUsesRuntimeSpawn, pkg/worker/worker_test.go:TestWorkerDrainSelectsParserByRuntimeFormat | Cmd: go test ./pkg/worker/... -run 'TestWorkerUsesRuntimeSpawn|TestWorkerDrainSelectsParserByRuntimeFormat' -count=1 | Assert: worker execution launches only through a runtime adapter; Claude JSON stream and plain-text line stream are selected by runtime format; explicit memory markers still parse in the plain-text path
Read: pkg/worker/worker.go, pkg/worker/drain.go, pkg/protocol/message.go
Signature: replace ClaudeSpawner-specific worker launch with runtime-backed SpawnRequest and format-driven drain logic
Edges: runtime spawn failure preserves current error surfacing; plain-text runtimes still support memory marker extraction; Claude path behavior unchanged`

### 3. `refactor(ops): move ops subprocess launch and review prompt loading behind runtime-aware abstractions`

- Type: `task`
- Priority: `P1`
- Estimate: `45`
- Depends on: bead 1
- Acceptance:
  `Test: pkg/ops/exec_spawner_test.go:TestOpsSpawnerUsesRuntime, pkg/ops/review_prompt_test.go:TestReviewPromptResolvesSharedInstructions | Cmd: go test ./pkg/ops/... -run 'TestOpsSpawnerUsesRuntime|TestReviewPromptResolvesSharedInstructions' -count=1 | Assert: ops flows no longer spawn Claude directly; review/bootstrap prompt loading can resolve shared Oro instructions plus Claude compatibility paths instead of assuming CLAUDE.md is canonical
Read: pkg/ops/exec_spawner.go, pkg/ops/ops.go, pkg/ops/review_prompt.go
Signature: introduce runtime-backed ops spawner and instruction loader abstraction used by review/escalation flows
Edges: Claude review path remains valid during migration; missing shared instruction file falls back to existing Claude wrapper until shared assets land`

### 4. `refactor(protocol): add neutral runtime tiers with legacy Claude-family compatibility`

- Type: `task`
- Priority: `P1`
- Estimate: `35`
- Depends on: bead 1
- Acceptance:
  `Test: pkg/protocol/types_test.go:TestLegacyModelMappingToTier, pkg/ops/ops_test.go:TestOpsTasksRouteByTier | Cmd: go test ./pkg/protocol ./pkg/ops -run 'TestLegacyModelMappingToTier|TestOpsTasksRouteByTier' -count=1 | Assert: opus/sonnet/haiku values map to deep/balanced/fast tiers; ops task routing uses tiers internally; explicit provider-native overrides still parse and survive round-trip where supported
Read: pkg/protocol/types.go, pkg/ops/ops.go, pkg/protocol/message.go
Signature: add Tier fields/constants and compatibility mapper from legacy model names to neutral tiers
Edges: unknown legacy model -> explicit error or pass-through override per design; existing serialized bead payloads remain backward compatible`

### 5. `refactor(prompts): replace Claude-family threshold language with neutral tier language`

- Type: `task`
- Priority: `P2`
- Estimate: `25`
- Depends on: bead 4
- Acceptance:
  `Test: pkg/worker/prompt_test.go:TestPromptUsesNeutralTierLanguage | Cmd: go test ./pkg/worker/... -run TestPromptUsesNeutralTierLanguage -count=1 | Assert: worker and decomposition prompts refer to fast/balanced/deep/background tiers rather than opus/sonnet/haiku while preserving the same routing semantics and guardrails
Read: pkg/worker/prompt.go, thresholds.json, docs/plans/2026-04-22-agent-runtime-agnostic-design.md
Signature: update prompt builders and threshold labels to use neutral tier terminology
Edges: legacy Claude-family names may still appear in compatibility or migration notes, but not as the primary control surface`

### 6. `refactor(assets): extract shared Oro instructions and skills from Claude-specific packaging`

- Type: `task`
- Priority: `P1`
- Estimate: `40`
- Depends on: beads 3, 5
- Acceptance:
  `Test: cmd/oro/cmd_init_test.go:TestExtractAgentAssetsSharedSource, pkg/ops/review_prompt_test.go:TestReviewPromptPrefersSharedInstructions | Cmd: go test ./cmd/oro/... ./pkg/ops/... -run 'TestExtractAgentAssetsSharedSource|TestReviewPromptPrefersSharedInstructions' -count=1 | Assert: a shared Oro instruction source exists; skill packaging is sourced from shared Oro assets instead of Claude-only paths; Claude wrappers remain generated for compatibility
Read: assets/CLAUDE.md, CLAUDE.MD, cmd/oro/cmd_init.go, pkg/ops/review_prompt.go
Signature: add shared instruction asset(s) and teach asset extraction/review prompt loading to prefer them
Edges: if shared asset missing during upgrade, existing Claude asset path still works; no behavior regression for current Claude installs`

### 7. `feat(init): make oro init generate shared assets plus runtime-specific compatibility views`

- Type: `task`
- Priority: `P2`
- Estimate: `35`
- Depends on: bead 6
- Acceptance:
  `Test: cmd/oro/cmd_init_test.go:TestOroInitGeneratesSharedAndClaudeViews | Cmd: go test ./cmd/oro/... -run TestOroInitGeneratesSharedAndClaudeViews -count=1 | Assert: oro init writes shared Oro assets and still generates Claude-compatible project views; generated layout is sufficient for later Codex bootstrap work
Read: cmd/oro/cmd_init.go, assets/skills/, docs/plans/2026-04-22-agent-runtime-agnostic-design.md
Signature: extend init extraction flow to materialize shared assets plus runtime-specific wrappers
Edges: repeat init is idempotent; existing .claude project assets are preserved or updated compatibly`

### 8. `feat(cli): add runtime-aware agent-assets sync and deprecate global-skills`

- Type: `task`
- Priority: `P2`
- Estimate: `35`
- Depends on: bead 6
- Acceptance:
  `Test: cmd/oro/cmd_global_oro_approach_test.go:TestAgentAssetsSyncSupportsClaudeAndCodex | Cmd: go test ./cmd/oro/... -run TestAgentAssetsSyncSupportsClaudeAndCodex -count=1 | Assert: shared Oro skills can be synced to Claude and Codex targets from one source bundle; oro global-skills remains as deprecated Claude alias during migration
Read: cmd/oro/cmd_global_oro_approach.go, assets/hooks/session_start_global.py, assets/hooks/session_start_extras.py
Signature: add runtime-aware sync command/path and preserve global-skills as compatibility wrapper
Edges: sync for runtimes without hooks installs skills without failing on missing hook support; repeated sync updates cleanly`

### 9. `feat(codex): implement Codex runtime adapter with pinned subprocess contract`

- Type: `task`
- Priority: `P1`
- Estimate: `50`
- Depends on: beads 2, 3, 4
- Acceptance:
  `Test: pkg/agentruntime/codex/codex_test.go:TestCodexRuntimeSpawnContract, pkg/worker/worker_test.go:TestWorkerRunsWithCodexLineStream | Cmd: go test ./pkg/agentruntime/... ./pkg/worker/... -run 'TestCodexRuntimeSpawnContract|TestWorkerRunsWithCodexLineStream' -count=1 | Assert: Codex runtime launches through one concrete subprocess contract, sends one materialized prompt per task, emits plain-text line stream unless explicitly configured otherwise, and integrates with worker parsing without Claude-specific flags or paths
Read: docs/plans/2026-04-22-agent-runtime-agnostic-design.md, pkg/worker/worker.go, pkg/ops/exec_spawner.go
Signature: add pkg/agentruntime/codex adapter implementing Runtime interface and concrete spawn contract for worker and ops flows
Edges: missing Codex executable -> preflight/runtime error with actionable message; contract changes require spec and test updates in the same change`

### 10. `feat(codex): implement no-hook bootstrap and shared skill access for Codex`

- Type: `task`
- Priority: `P1`
- Estimate: `40`
- Depends on: beads 6, 8, 9
- Acceptance:
  `Test: cmd/oro/cmd_work_test.go:TestCodexBootstrapWithoutHooks, cmd/oro/cmd_global_oro_approach_test.go:TestCodexSkillSyncInstallsPortableSkills | Cmd: go test ./cmd/oro/... -run 'TestCodexBootstrapWithoutHooks|TestCodexSkillSyncInstallsPortableSkills' -count=1 | Assert: Codex sessions remain usable without Claude SessionStart hooks; portable Oro skills are installed in a Codex-visible location; essential bootstrap guidance is provided by shared assets and/or runtime prompt prelude instead of Claude hook dependence
Read: assets/hooks/session_start_global.py, assets/hooks/session_start_extras.py, cmd/oro/cmd_global_oro_approach.go, cmd/oro/cmd_work.go
Signature: add Codex bootstrap path that does not require hook surfaces and can inject runtime prelude or equivalent shared guidance
Edges: Codex installs lacking a project-instruction-file equivalent still work; lack of hook support does not block startup`

### 11. `docs(runtime): rewrite runtime-facing docs, beacons, and role guidance to be agent-agnostic`

- Type: `task`
- Priority: `P3`
- Estimate: `25`
- Depends on: beads 5, 6, 10
- Acceptance:
  `Test: n/a-docs | Cmd: rg -n 'Claude workers|Spawn Claude subagents|\\.claude/skills' README.md assets docs pkg | head -n 50 | Assert: user-facing runtime docs, beacons, and role guidance describe agent-agnostic behavior by default; remaining Claude mentions are clearly compatibility-specific
Read: README.md, assets/CLAUDE.md, CLAUDE.MD, docs/plans/2026-04-22-agent-runtime-agnostic-design.md
Signature: update docs and role guidance so shared process language is runtime-neutral and Claude-only content is isolated to compatibility sections
Edges: retain necessary Claude references where they document compatibility behavior or migration steps`

### 12. `test(runtime): add end-to-end compatibility matrix for Claude default, legacy mapping, asset sync, and Codex no-hook flow`

- Type: `task`
- Priority: `P1`
- Estimate: `40`
- Depends on: beads 7, 8, 9, 10
- Acceptance:
  `Test: go test ./... -run 'TestClaudeRuntimeDefaultPath|TestLegacyModelCompatibilityMapping|TestAgentAssetsSyncAllRuntimes|TestCodexRuntimeNoHookBootstrap' -count=1 | Cmd: go test ./... -run 'TestClaudeRuntimeDefaultPath|TestLegacyModelCompatibilityMapping|TestAgentAssetsSyncAllRuntimes|TestCodexRuntimeNoHookBootstrap' -count=1 | Assert: split compatibility coverage proves Claude default path still works, legacy model names still map correctly, shared assets sync to both runtimes, and Codex runs without Claude hooks
Read: docs/plans/2026-04-22-agent-runtime-agnostic-design.md
Signature: add integration coverage matching the acceptance contract named in the spec
Edges: tests that require runtime binaries should skip cleanly with explicit reason when binary is unavailable in CI/dev`

## Dependency Shape

- Critical path:
  1 -> 2, 3, 4
  4 -> 5
  3, 5 -> 6
  6 -> 7, 8
  2, 3, 4 -> 9
  6, 8, 9 -> 10
  7, 8, 9, 10 -> 12

- Documentation can trail implementation:
  5, 6, 10 -> 11

## Recommended Creation Order

1. Epic
2. Beads 1, 2, 3, 4
3. Beads 5, 6
4. Beads 7, 8, 9
5. Beads 10, 12
6. Bead 11

## Tracker Note

Historical pre-migration note: when this decomposition was written, `bd` creation was blocked because the configured Dolt server port seen by `bd` did not match live Dolt metadata, so this file was used as a temporary source of truth for bead creation. Current bead state lives in the native SQLite beadstore.
