# Agent Runtime Agnostic Oro Design

**Date:** 2026-04-22
**Status:** Draft — researched against current code and updated after fresh-context adversarial review
**Goal:** Make Oro runtime-agnostic so the swarm can execute against Claude or Codex without baking provider assumptions into worker spawning, ops flows, prompts, asset extraction, or skills distribution.

## Problem

Oro was built around Claude Code as both execution runtime and instruction substrate. That coupling shows up in four different layers:

1. **Runtime process model**
   - Workers spawn `claude -p` directly in `pkg/worker/worker.go`.
   - Ops agents spawn `claude -p` directly in `pkg/ops/exec_spawner.go`.
   - Output parsing assumes Claude's `--output-format stream-json`.

2. **Model taxonomy**
   - Protocol and routing use Claude-native family names (`opus`, `sonnet`, `haiku`) in `pkg/protocol/types.go`.
   - Ops task types hardcode Claude-family choices in `pkg/ops/ops.go`.
   - Context thresholds are keyed by Claude families in worker prompts and `thresholds.json`.

3. **Instruction and asset layout**
   - Project instructions are `CLAUDE.MD`.
   - Assets extract into `~/.oro/.claude/...` and project `.claude/...`.
   - Review prompts read `CLAUDE.md` and `.claude/rules/`.
   - Hooks and global sync target `~/.claude/skills` and `~/.claude/hooks`.

4. **Skills availability assumptions**
   - Many prompts, beacons, and hooks assume the executing agent already sees `using-skills`, `create-handoff`, `work-bead`, `executing-beads`, etc.
   - That assumption is enforced for Claude via `.claude/skills`, SessionStart hooks, and `CLAUDE.md`.
   - Codex does not consume those same paths or hook mechanisms, so Oro workers and humans using Codex lose process guidance that Claude sessions receive automatically.

The result is that "support Codex" is not one change. It is a cross-cutting refactor of:

- execution runtime abstraction
- provider-neutral routing semantics
- asset extraction and instruction packaging
- skill distribution and bootstrap behavior

## Current State (Verified)

### Claude-specific runtime wiring

- `cmd/oro/cmd_work.go` constructs `&worker.ClaudeSpawner{}` and `ops.NewSpawner(&ops.ClaudeOpsSpawner{})` in its production dependency path.
- `cmd/oro/cmd_start.go` constructs `ops.NewSpawner(&ops.ClaudeOpsSpawner{})` in dispatcher startup.
- `pkg/worker/worker.go` defines `ClaudeSpawner`, `buildClaudeArgs`, `buildClaudeEnv`, and spawns `claude -p`.
- `pkg/ops/exec_spawner.go` defines `ClaudeOpsSpawner` and spawns `claude -p`.
- `pkg/worker/worker.go` comments and tests explicitly reference Claude's Ink TUI behavior, `CLAUDECODE`, and `stream-json`.

### Claude-family model semantics

- `pkg/protocol/types.go` exposes `ModelOpus`, `ModelSonnet`, `ModelHaiku`, `DefaultModel`.
- `pkg/ops/ops.go` maps ops task types directly to `opus`, `sonnet`, `haiku`.
- `pkg/worker/prompt.go` prints context thresholds for `opus`, `sonnet`, `haiku`.

### Claude-scoped assets

- `cmd/oro/cmd_init.go` extracts skills into `.claude/skills` and `CLAUDE.md` into `.claude/CLAUDE.md`.
- `cmd/oro/cmd_global_oro_approach.go` only syncs to `~/.claude/skills` and `~/.claude/hooks`.
- `assets/CLAUDE.md` and root `CLAUDE.MD` enforce `using-skills`.
- `pkg/ops/review_prompt.go` reads `CLAUDE.md` and `.claude/rules`.
- `assets/hooks/session_start_global.py` and `assets/hooks/session_start_extras.py` load Claude-scoped skills from `~/.claude/...`.

### Skills path assumptions

- `assets/hooks/session_start_global.py` and `session_start_extras.py` auto-load `using-skills` from Claude paths.
- Worker prompts instruct the agent to invoke skills like `create-handoff`.
- The skill corpus lives in `assets/skills/` and is currently packaged as Claude assets first, not as a provider-neutral skill bundle.

## Non-goals

- Supporting every agent runtime in one pass. This spec targets **Claude + Codex** only.
- Replacing bead semantics or the dispatcher/worker lifecycle.
- Redesigning the memory system, code search, or merge pipeline.
- Achieving perfect hook parity between Claude and Codex on day one. Codex may not expose the same SessionStart/PreToolUse/PostToolUse hook model.
- Removing Claude support. Claude remains a first-class runtime.

## Design Principles

1. **Abstract capability, not branding**
   - Code should route by workload intent and runtime capabilities, not by provider name.

2. **Preserve existing swarm behavior**
   - Beads, reviews, retries, handoffs, and merge logic should behave the same after runtime selection is introduced.

3. **Make skills a first-class Oro asset**
   - Skills belong to Oro, not to Claude. Provider-specific sync is a packaging concern.

4. **Separate portable instructions from runtime adapters**
   - Shared process guidance should live once.
   - Runtime-specific wrappers should adapt that guidance to Claude or Codex affordances.

5. **Keep Claude compatibility during migration**
   - Existing `.claude` installs and `CLAUDE.md` consumers must continue working until migration is complete.

## Architecture

### 1. Agent Runtime Abstraction

Add a new package, `pkg/agentruntime`, that owns runtime selection and provider-specific spawning.

Core interfaces:

```go
type RuntimeID string

const (
    RuntimeClaude RuntimeID = "claude"
    RuntimeCodex  RuntimeID = "codex"
)

type Tier string

const (
    TierFast       Tier = "fast"
    TierBalanced   Tier = "balanced"
    TierDeep       Tier = "deep"
    TierBackground Tier = "background"
)

type SpawnRequest struct {
    Role        string   // worker, review, diagnosis, decompose, dream
    Tier        Tier
    ModelHint   string   // optional explicit provider-native model
    Prompt      string
    Workdir     string
    ExtraPaths  []string // runtime-specific instruction/config roots
}

type StreamFormat string

const (
    StreamFormatClaudeJSON StreamFormat = "claude_stream_json"
    StreamFormatLineText   StreamFormat = "line_text"
)

type Runtime interface {
    ID() RuntimeID
    DefaultTierModel(role string, tier Tier) string
    Spawn(ctx context.Context, req SpawnRequest) (Process, io.ReadCloser, io.WriteCloser, error)
    StreamFormat() StreamFormat
    InstructionLayout() InstructionLayout
    SupportsHooks() bool
    SupportsProjectSkillInstall() bool
}
```

`pkg/worker` and `pkg/ops` stop naming Claude directly. They depend on a `Runtime`.

Mandatory wiring rule:

- no production path may directly instantiate `worker.ClaudeSpawner`, `ops.ClaudeOpsSpawner`, or `exec.Command("claude", ...)` outside a runtime adapter
- `cmd/oro/cmd_start.go` and `cmd/oro/cmd_work.go` must resolve a `Runtime` first, then derive worker and ops spawners from it
- this rule applies to tests once adapter coverage exists, except for runtime-adapter unit tests that intentionally exercise a concrete provider implementation

This refactor explicitly separates:

- **role**: worker vs review vs diagnosis vs dream
- **tier**: fast/balanced/deep/background
- **provider-native model string**: optional runtime-specific override

### 2. Provider-neutral routing semantics

Replace direct `opus`/`sonnet`/`haiku` routing with Oro-owned tiers:

| Current | New tier |
|---|---|
| `haiku` | `fast` |
| `sonnet` | `balanced` |
| `opus` | `deep` |
| dream-only `haiku` | `background` |

Protocol changes:

- `protocol.Bead.Model` becomes `ModelHint` or `RuntimeModel`.
- Add `Tier` as the provider-neutral routing field.
- Legacy metadata/model values still parse for backward compatibility.

Compatibility rules:

1. Existing bead `metadata.model=opus|sonnet|haiku` maps to `TierDeep|TierBalanced|TierFast`.
2. Existing top-level `Model` field still works as an explicit provider-native override.
3. New beads should prefer `tier=<fast|balanced|deep|background>` over provider-native names.

Ops mapping moves from:

```go
OpsReview -> "opus"
OpsEscalation -> "sonnet"
OpsDream -> "haiku"
```

to:

```go
OpsReview -> TierDeep
OpsEscalation -> TierBalanced
OpsDream -> TierBackground
```

Each runtime chooses the concrete model for that tier.

### 3. Runtime-specific spawners

Split current Claude implementations into runtime adapters:

- `pkg/agentruntime/claude`
  - wraps current `ClaudeSpawner` logic
  - retains `stream-json`
  - strips `CLAUDECODE`
  - still supports `--add-dir` / `--settings`

- `pkg/agentruntime/codex`
  - wraps Codex execution entrypoint
  - defines Codex-specific spawn args/env
  - defines its stream format and output parser
  - has no dependency on `.claude` paths

Codex v1 contract must be pinned, not left abstract. The implementation target for this migration is:

- Oro launches a local Codex CLI subprocess through the runtime adapter only
- Oro sends one fully materialized prompt string per worker or ops task
- stdout is consumed as plain text line stream unless Codex exposes a stable structured mode that Oro explicitly adopts in code and tests
- explicit memory markers remain text-based and continue to be parsed from stdout
- the adapter may inject a runtime-specific instruction prelude before the task prompt, but dispatcher/worker code remains unaware of provider specifics
- Codex support in v1 does not depend on SessionStart, PreToolUse, or PostToolUse hooks

If the chosen Codex executable or flags differ from this contract during implementation, the spec must be amended before merge so tests can target one concrete process model.

Worker output parsing becomes format-driven:

```go
switch runtime.StreamFormat() {
case StreamFormatClaudeJSON:
    // existing ParseStreamEvent path
case StreamFormatLineText:
    // plain-text drain path
}
```

This keeps dispatcher/worker lifecycle intact while removing the assumption that every runtime emits Claude NDJSON.

### 4. Instruction layout abstraction

Introduce a runtime-neutral instruction model:

```go
type InstructionLayout struct {
    ProjectInstructionsPath string   // e.g. ORO_AGENT.md, CLAUDE.md, CODEX.md
    RulesDir                string
    SkillsDir               string
    HooksDir                string
}
```

Oro should stop treating `CLAUDE.md` as the canonical source. Instead:

- add a provider-neutral source instruction file: `assets/ORO_AGENT.md`
- generate runtime-specific wrappers from it:
  - Claude: `.claude/CLAUDE.md`
  - Codex: provider-appropriate instructions path if supported

If Codex has no project instruction file equivalent, the fallback is:

- sync portable skills to Codex skill directories
- inject essential Oro bootstrap instructions via the runtime adapter prompt prelude

This fallback is required for v1. Hook-based bootstrap is not an acceptable dependency for Codex support.

### 5. Skills as a provider-neutral Oro bundle

The source of truth remains `assets/skills/`, but packaging changes:

- current: extracted only to `~/.oro/.claude/skills` and project `.claude/skills`
- new: extracted to a provider-neutral Oro asset root first, then synced into provider-specific locations

New asset layout under `~/.oro/agents/`:

```text
~/.oro/
  agents/
    shared/
      skills/
      instructions/
      beacons/
    claude/
      hooks/
      settings.json
    codex/
      bootstrap/
```

Sync targets:

- Claude:
  - `~/.claude/skills` symlinks into `~/.oro/agents/shared/skills`
  - `~/.claude/hooks` copied from `~/.oro/agents/claude/hooks`

- Codex:
  - `~/.codex/skills` symlinks or copies from `~/.oro/agents/shared/skills`
  - optional Codex-specific bootstrap artifacts under `~/.oro/agents/codex/`

The key design rule: **skills are shared; only bootstrap and hook wiring differ by runtime**.

### 6. Capability matrix

Not every runtime supports the same integration surface. Oro must model that explicitly instead of silently assuming Claude semantics.

| Capability | Claude | Codex | Oro behavior |
|---|---|---|---|
| Project skill install | Yes | Yes | Sync portable skills to both |
| SessionStart hook | Yes | Unknown / not assumed | Use hooks only where supported |
| PreToolUse/PostToolUse hooks | Yes | Unknown / not assumed | Hook-driven reminders are optional enhancements |
| Project instruction file | Yes (`CLAUDE.md`) | Runtime-specific / not assumed | Runtime adapter provides fallback prelude |
| Structured streaming JSON | Yes | Runtime-specific | Parser selected by runtime |
| Provider-native tier names | Yes | No | Oro uses neutral tiers |

Codex support is therefore **skill parity first, hook parity later**.

## Required Entry Points

This migration is not complete unless these concrete production entry points are covered:

- `cmd/oro/cmd_start.go`
- `cmd/oro/cmd_work.go`
- `pkg/worker/worker.go`
- `pkg/ops/exec_spawner.go`
- `pkg/ops/ops.go`
- `pkg/protocol/types.go`
- `pkg/protocol/message.go`
- `pkg/worker/prompt.go`
- `pkg/ops/review_prompt.go`
- `cmd/oro/cmd_init.go`
- `cmd/oro/cmd_global_oro_approach.go`
- `assets/hooks/session_start_global.py`
- `assets/hooks/session_start_extras.py`
- `assets/CLAUDE.md`
- `CLAUDE.MD`

Coverage expectation by area:

- runtime launch path: `cmd_start`, `cmd_work`, worker spawner, ops spawner
- routing semantics: protocol types, ops routing, prompt threshold language
- instruction/bootstrap path: review prompt loader, init extraction, global sync, session-start hooks
- compatibility wrappers: Claude docs/assets remain valid while shared Oro assets become canonical

## Asset and Prompt Changes

### Portable vs runtime-specific skill content

Audit the skill corpus into two buckets:

1. **Portable skills**
   - process skills: TDD, debugging, spec, review, handoff, beadcraft, work-bead
   - any content that speaks in terms of code, tests, git, and beads

2. **Claude-specific skills/assets**
   - anything that depends on Claude-only hooks, flags, or `CLAUDE.md`
   - anything that tells the agent to rely on Claude-only behavior

Portable skills move unchanged or with minor wording cleanup.

Claude-specific content is either:

- rewritten to be runtime-neutral, or
- split into `shared + claude overlay`

### Beacons and role docs

Current beacons say things like:

- "Spawn Claude subagents"
- "no using `oro` CLI commands ... through Claude tools"

Beacons should be rewritten to target the runtime-neutral Oro role:

- "spawn subagents if the current runtime supports them"
- "use the tools exposed by your runtime"

### Worker prompt language

Worker prompts should stop naming Claude families directly. Replace:

- `opus/sonnet/haiku thresholds`

with:

- `fast/balanced/deep/background thresholds`

Runtime adapters may still translate tiers into concrete model IDs for telemetry and subprocess args.

## CLI and Setup Changes

### New runtime selection

Add config and CLI flags:

```go
type AgentConfig struct {
    Runtime       string // claude | codex
    WorkerBin     string
    OpsBin        string
    DefaultTierMap map[string]string
}
```

`oro setup` and `oro start` gain:

- `--runtime claude|codex`
- runtime-aware preflight checks
  - Claude mode: check `claude`
  - Codex mode: check Codex executable

### Replace `global-skills` with runtime-aware sync

Current command:

- `oro global-skills` only targets `~/.claude`

New command:

- `oro agent-assets sync --runtime claude|codex|all`

Behavior:

- sync shared skills to requested runtime(s)
- sync runtime-specific hooks/bootstrap only where supported
- leave existing `oro global-skills` as deprecated alias to `--runtime claude`

### `oro init` asset extraction

Current extraction is Claude-shaped:

- skills -> `.claude/skills`
- commands -> `.claude/commands`
- `CLAUDE.md` -> `.claude/CLAUDE.md`

New extraction writes shared assets first, then optional runtime views.

Compatibility phase:

- keep writing `.claude/...` for existing Claude users
- additionally prepare Codex-consumable assets

Codex v1 requirement:

- `oro init` and global asset sync must install shared Oro skills in a Codex-visible location
- if Codex lacks a project instruction file equivalent, the command must still leave the runtime in a usable state through skill sync plus prompt-prelude bootstrap
- success cannot depend on Claude hooks being present on the machine

## Migration Plan

### Phase 1: Introduce runtime abstraction without behavior change

- add `pkg/agentruntime`
- move existing Claude spawners behind `RuntimeClaude`
- keep Claude as default
- no Codex support yet
- update `cmd/oro/cmd_start.go` and `cmd/oro/cmd_work.go` to resolve runtimes through the new abstraction instead of directly constructing Claude spawners

### Phase 2: Neutralize tiers and routing

- add `TierFast/Balanced/Deep/Background`
- map legacy `opus/sonnet/haiku` values
- update worker prompts, thresholds, and ops routing
- keep accepting legacy Claude-family model strings at the protocol boundary and config layer

### Phase 3: Split shared assets from Claude packaging

- add `assets/ORO_AGENT.md`
- extract shared skills into provider-neutral asset root
- keep generating Claude-compatible views
- remove hard dependency on `~/.claude/skills/using-skills/SKILL.md` from the generic bootstrap story

### Phase 4: Add Codex runtime adapter

- implement `RuntimeCodex`
- add Codex preflight/setup/runtime selection
- add Codex output parser
- define the exact Codex subprocess invocation and stdout parsing contract in code and tests

### Phase 5: Add Codex skill sync

- sync portable Oro skills into `~/.codex/skills`
- add Codex bootstrap instructions where possible
- document capabilities that lack Claude-equivalent hooks
- ensure no-hook bootstrap path works end-to-end

### Phase 6: Remove direct Claude naming from docs and prompts

- rename user-facing docs from "Claude workers" to "agent workers" where appropriate
- keep Claude examples only in Claude-specific sections

## Backward Compatibility

The following must continue to work during migration:

- existing Claude-based swarms
- existing beads with `metadata.model=opus|sonnet|haiku`
- existing `.claude/skills` and `CLAUDE.md` installs
- existing tests that assume Claude as the default runtime
- existing review flows that currently read `CLAUDE.md` and `.claude/rules`

Compatibility shims:

- `RuntimeClaude` remains the default if runtime is unset
- `oro global-skills` remains as a deprecated alias
- `CLAUDE.md` remains generated until Codex support is proven stable
- provider-native overrides remain accepted even after tier-first routing lands
- shared Oro assets become canonical source, but `.claude/...` outputs remain generated until at least one release after Codex support is stable

## Acceptance Contract

The migration is only done when all of the following are true:

1. `cmd/oro/cmd_start.go` and `cmd/oro/cmd_work.go` no longer directly construct Claude-specific spawners in production code.
2. All subprocess launches for worker and ops agents go through `pkg/agentruntime` adapters.
3. Claude remains the default runtime when no runtime is configured.
4. Legacy `opus|sonnet|haiku` bead and config values still execute with equivalent routing semantics.
5. Codex workers can run without any Claude hook installation on the machine.
6. Portable Oro skills are installable for both Claude and Codex from a shared source bundle.
7. Review/bootstrap prompts no longer assume `CLAUDE.md` is the only canonical instruction source.
8. Asset sync and init flows support both runtimes without regressing existing Claude installs.

Minimum test matrix:

- unit: tier compatibility mapping from legacy Claude-family names
- unit: runtime selection in `cmd_start` and `cmd_work`
- unit: worker and ops spawn paths reject direct provider-specific construction outside adapters
- integration: Claude default startup path still works
- integration: Codex startup path works with no hooks present
- integration: asset sync installs shared skills for Claude and Codex
- integration: review/bootstrap path resolves shared instructions plus Claude compatibility wrappers

Suggested end-to-end coverage should be split rather than hidden behind one broad test name:

- `TestClaudeRuntimeDefaultPath`
- `TestCodexRuntimeNoHookBootstrap`
- `TestLegacyModelCompatibilityMapping`
- `TestAgentAssetsSyncAllRuntimes`

## Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Runtime abstraction leaks Claude assumptions anyway | High | Force all spawn logic through `pkg/agentruntime`; forbid direct `exec.Command("claude", ...)` outside runtime adapter |
| Codex lacks equivalent hook surfaces | Medium | Design for skill parity without requiring hook parity; bootstrap through skill sync and runtime prompt prelude |
| Tier migration breaks routing semantics | Medium | Preserve legacy mapping and add compatibility tests for every old model family |
| Skill corpus contains hidden Claude-only assumptions | High | Audit every skill into portable vs runtime-specific buckets before Codex sync |
| Docs drift between shared and runtime-specific instructions | Medium | Make `assets/ORO_AGENT.md` the single source; generate wrappers |

## Open Questions

1. What is the exact Codex subprocess contract Oro should target in worker mode?
   - recommendation for v1: local CLI subprocess with plain text line streaming
   - if a structured mode is adopted, it must be explicitly committed in code/tests, not left implementation-defined

2. Does Codex support a project-level instruction file equivalent to `CLAUDE.md`?
   - if yes, generate it
   - if no, the runtime adapter must inject Oro bootstrap guidance directly

3. Does Codex expose hook points equivalent to SessionStart / PreToolUse / PostToolUse?
   - if no, some current Claude ergonomics remain Claude-only

4. Should provider-native model overrides remain user-visible, or should Oro move entirely to neutral tiers at the bead layer?
   - recommendation: keep both, but document tier-first behavior

## Recommended Bead Breakdown

Epic: `agent-runtime-agnostic`

1. `refactor(runtime): introduce agentruntime package and remove direct Claude spawner construction from cmd_start/cmd_work`
2. `refactor(worker): move worker subprocess launch and stream parsing behind runtime adapters`
3. `refactor(ops): move ops subprocess launch and review prompt loading behind runtime-aware abstractions`
4. `refactor(protocol): add provider-neutral model tiers with legacy Claude-family compatibility`
5. `refactor(prompts): replace Claude-family thresholds and wording with runtime-neutral tier language`
6. `refactor(assets): extract shared Oro instructions/skills from Claude-specific packaging`
7. `feat(init): make oro init produce shared assets plus Claude compatibility views`
8. `feat(cli): replace global-skills with runtime-aware agent-assets sync`
9. `feat(codex): add Codex runtime adapter for workers and ops agents with a pinned subprocess contract`
10. `feat(codex): implement no-hook bootstrap and sync portable Oro skills into ~/.codex/skills`
11. `docs(runtime): rewrite README, beacons, and role docs for agent-agnostic language`
12. `test(runtime): add split compatibility coverage for Claude default path, legacy model mapping, asset sync, and Codex no-hook paths`

## Recommendation

Do this as an additive migration, not a flag day rewrite.

The safest path is:

1. abstract runtime selection while keeping Claude behavior identical
2. move shared skills/instructions out of Claude-specific paths
3. add Codex as a second runtime only after the shared substrate is real

That order minimizes breakage and avoids baking a second set of provider assumptions into the codebase while trying to remove the first.
