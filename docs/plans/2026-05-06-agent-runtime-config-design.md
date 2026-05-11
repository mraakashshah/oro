# Agent Runtime Configuration Design

**Date:** 2026-05-06
**Status:** Draft
**Extends:** [2026-04-22-agent-runtime-agnostic-design.md](./2026-04-22-agent-runtime-agnostic-design.md)
**Goal:** Make Oro's runtime + model selection configurable per role, with a per-tier wizard at `oro init`, replacing the hardcoded Claude-family routing that still survives in bead generation, dispatcher, ops, worker, codesearch, memory, hooks, and CLI flags.

## Why This Extends the Parent Spec

The parent runtime-agnostic spec landed a partial implementation:

- `pkg/agentruntime` resolver gated on `ORO_AGENT_RUNTIME` env var
- Claude and Codex adapters
- `cmd_start` and `cmd_work` resolve a runtime through the abstraction
- `protocol.Bead.Tier`, `Tier` constants, `ParseTier`, `LegacyModelToTier`, `ResolveTier`, `ResolveModel` (`pkg/protocol/types.go:52,89-151`)

What it did **not** land:

- A way to select **which model** within a runtime
- A way to mix runtimes across roles (Claude for workers, Codex for reranker, etc.)
- Auxiliary models (estimator, reranker, memory extractor) remain hardcoded const strings
- `Tier.DefaultModel()` still returns Claude families (`pkg/protocol/types.go:108`)
- Auxiliary spawners read `agentruntime.ReadRuntime()` as a global switch instead of resolving per role (`pkg/codesearch/claude_spawner.go:34`, `pkg/memory/extract_llm.go:128`)
- No `oro init` wizard for runtime/model selection
- `oro task create` has no `--tier` flag, so children of decomposed epics inherit `DefaultTier` with no way to override at creation time
- `oro start --model "sonnet"` and `oro work --model` help text still hardcoded to Claude families

This extension adds the configuration layer, the wizard, and the bead-creation flag so the runtime-agnostic substrate is actually useful.

### Already-Done Work (Out of Scope Here)

Verified during premortem and adversarial review; do NOT redo:

- `protocol.Bead.Tier` field already exists.
- `Tier` constants and parser exist.
- `LegacyModelToTier` mapping (`opus→deep`, `sonnet→balanced`, `haiku→fast`) exists.
- `Bead.ResolveTier()` and `Bead.ResolveModel()` already implement priority order.

What's missing is **config consultation** — but it cannot live in `pkg/protocol`. Adding a config dependency to `protocol` creates an import cycle (`pkg/protocol` is a leaf package today; `pkg/config/agent.go` will need `protocol.Tier`). Resolution must move OUT of `protocol`.

### Where Resolution Actually Lives

A new package (`pkg/agentmodel`) owns role→runtime+model resolution. It depends on both `pkg/protocol` and `pkg/config`. `protocol` stays pure — **zero new imports**.

```
pkg/protocol     // unchanged: types, tiers, parsers (leaf — stdlib only)
pkg/config       // new: agent.tiers + agent.api_models + agent.roles schema
pkg/agentmodel   // new: ResolveForBead(role, bead) (runtime, model, reasoning string)
                 //      depends on protocol + config
```

`Tier.DefaultModel()` is **deleted**. `Bead.ResolveModel()` becomes a pure shim that returns `b.Model` only when set, otherwise the empty string. It does NOT delegate to agentmodel. Tier-aware resolution happens at the call site (worker, dispatcher, ops), where the agentmodel package is in scope.

```go
// pkg/protocol/types.go — after refactor (still leaf)
func (b Bead) ResolveModel() string { return b.Model }     // shim, kept for back-compat
func (b Bead) ResolveTier() Tier { /* unchanged tier resolution from explicit field, legacy mapping, estimate */ }
```

```go
// pkg/agentmodel/agentmodel.go — owns the actual resolution
func ResolveForBead(role string, b protocol.Bead) (runtime, model, reasoning string) {
    if b.Model != "" { return inferRuntimeFromModel(b.Model), b.Model, "" }
    tier := b.ResolveTier()
    return resolveTierFromConfig(role, tier)
}
```

This means `protocol` has no `import "oro/pkg/config"` and no `import "oro/pkg/agentmodel"`. The boundary is enforced by acceptance #21 below.

Every call site that previously hit `Tier.DefaultModel()` or `Bead.ResolveModel()` moves to `agentmodel.ResolveForBead(role, bead)` or `agentmodel.ResolveForRole(role)`. This is a wider refactor than "swap one function" — see "All Model-Resolution Call Sites" below.

### All Model-Resolution Call Sites

The spec previously implied changing `Tier.DefaultModel()` was sufficient. It is not. Every site that picks a model must move to `agentmodel`:

- `pkg/protocol/types.go:108` — delete `Tier.DefaultModel()` entirely
- `pkg/protocol/types.go:158` — `Bead.ResolveModel()` becomes a pure shim that returns `b.Model` only. It does NOT delegate to agentmodel. Tier-aware resolution moves to call sites.
- `cmd/oro/cmd_work.go:288` — direct `protocol.DefaultModel` fallback replaced with `agentmodel.ResolveForBead(role="worker", bead)`
- `cmd/oro/cmd_work.go:409, 726` — QG/review retry escalation must update the resolved (runtime, model) pair, not just the model variable
- `pkg/dispatcher/dispatcher.go:4324` — assignment payload construction must carry runtime+model, not just model (see "AssignPayload Schema Change" below)
- `pkg/ops/ops.go` — ops Type → role mapping
- `cmd/oro/cmd_start.go:563` — manager session model

### AssignPayload Schema Change (Required for Mixing)

The spec promises "any tier or role can override runtime independently." Today the worker process is bound to a single runtime: `cmd/oro/cmd_worker.go:55` instantiates one spawner from `agent_runtime.go:35` based on env var, and `pkg/protocol/message.go:88` `AssignPayload` only carries `Model`, not runtime, tier, or estimate.

That means a `worker` role at `tier=balanced` (Claude) **cannot** escalate to `worker_escalation` at `tier=deep` (Codex) within the same process. Mixing across roles requires one of:

- **Option A (preferred):** Dispatcher resolves runtime+model BEFORE sending AssignPayload. AssignPayload gains both `Runtime` and `Model` fields, fully resolved. Worker selects spawner per-assignment from `payload.Runtime`. No worker-side tier resolution, no bead-shape lookup.
- **Option B:** Separate worker pools per runtime, dispatcher routes by tier→runtime mapping. Larger architectural change.
- **Option C:** Constrain mixing to "all CLI roles share one runtime." Drops the spec's mixing promise.

This extension picks **Option A** as the implementation target.

Critically: **all resolution happens dispatcher-side**. The worker is a dumb consumer that reads `payload.Runtime` and `payload.Model` and spawns. There is no fallback to "worker calls `ResolveForBead`" — that path is impossible because the worker process has no bead store and no config layer (verified at `pkg/worker/worker.go:417, 519` — current resolution uses only `cfg.bead.Model` or `protocol.DefaultModel`). The dispatcher MUST populate both fields on every AssignPayload it sends.

If `payload.Runtime` is empty (e.g., a stale dispatcher pre-migration), the worker logs a warning and falls back to `agentruntime.ReadRuntime()` for runtime, `cfg.bead.Model` or hardcoded fallback for model. This back-compat shim survives one release after rollout, then is removed.

Bead breakdown below sequences: (1) AssignPayload schema change, (2) dispatcher-side resolution at construction, (3) worker payload consumption, in that order.

## Scope (In)

1. New `agent` config block with per-tier, per-role, and per-API-model keys. Block lives at user level (`~/.oro/config.yaml`) by default; `--project` flag writes it to `.oro/config.yaml`. See "Config File Location and Precedence" below.
2. `oro init` wizard: 5 questions (primary runtime + 4 tiers).
3. `oro config wizard` re-runs the wizard.
4. Role resolution at runtime: explicit `role.runtime+model` → `tiers[role.tier]` → built-in default.
5. Mixing allowed: any tier or role can override runtime independently of any other.
6. Bead generation writes `tier:` instead of `metadata.model:` for new beads.
7. Legacy `metadata.model=opus|sonnet|haiku` maps to `deep|balanced|fast` tier on read; runtime resolves from config, not pinned to Claude.
8. Auxiliary models (codesearch reranker, memory extractor) become CLI roles. Estimator becomes an API role pinned to Anthropic.
9. `thresholds.json` and Claude-family hooks gain tier keys; Python hook code maps Claude `model_key` → tier internally.

## Scope (Out)

- Removing Claude support.
- Adding runtimes beyond Claude and Codex (Gemini, etc.).
- Per-bead runtime override on the wire (still allowed via legacy `Model` field for now).
- Auto-detection of installed agent CLIs.
- Replacing the parent spec's outstanding work (Codex skill sync, shared assets, `agent-assets sync` CLI). Those remain in `agent-runtime-agnostic`.

## Config Schema

The example below is the locked **explicit `agent:` end state** for `oro-6myx`. If no `agent` block is present, Oro still preserves the legacy Claude-only defaults for compatibility. Once an `agent` block exists, role routing is intentionally pinned: Claude Opus handles spec/AC/decomposition/review work, Codex 5.5 handles implementation and operational escalation with per-role reasoning.

```yaml
agent:
  # CLI tiers — used by transport=cli roles
  tiers:
    fast:       { runtime: codex, model: gpt-5.5, reasoning: low }
    balanced:   { runtime: codex, model: gpt-5.5, reasoning: low }
    deep:       { runtime: codex, model: gpt-5.5, reasoning: high }
    background: { runtime: codex, model: gpt-5.5, reasoning: low }

  # API-only models — pinned to providers; do NOT inherit from agent.tiers
  api_models:
    anthropic_fast: claude-haiku-4-5-20251001

  roles:
    spec_writer:          { transport: cli, runtime: claude, model: claude-opus-4-7 }
    spec_challenger:      { transport: cli, runtime: codex, model: gpt-5.5, reasoning: xhigh }

    worker:               { transport: cli, runtime: codex, model: gpt-5.5, reasoning: low }
    worker_escalation:    { transport: cli, runtime: codex, model: gpt-5.5, reasoning: medium }

    ops_review:           { transport: cli, runtime: claude, model: claude-opus-4-7 }
    ops_escalation:       { transport: cli, runtime: codex, model: gpt-5.5, reasoning: high }
    ops_merge:            { transport: cli, runtime: codex, model: gpt-5.5, reasoning: high }
    ops_diagnosis:        { transport: cli, runtime: codex, model: gpt-5.5, reasoning: high }

    ops_decompose:        { transport: cli, runtime: claude, model: claude-opus-4-7 }
    ops_epic_fix:         { transport: cli, runtime: claude, model: claude-opus-4-7 }
    ops_write_ac:         { transport: cli, runtime: claude, model: claude-opus-4-7 }
    ops_dream:            { tier: fast, transport: cli }

    memory_extractor:     { tier: fast,     transport: cli }
    codesearch_reranker:  { tier: fast,     transport: cli }

    # API-call roles — read from agent.api_models, NOT from agent.tiers
    estimator:            { transport: api, provider: anthropic, api_model: anthropic_fast }
```

The `agent.tiers` block is for CLI roles only. API roles read from `agent.api_models` via the role's `api_model:` key. This keeps `tiers.fast = gpt-5.5` from breaking the estimator. `reasoning` is accepted only for Codex CLI routes (`low`, `medium`, `high`, `xhigh`) and is ignored by Claude routes.

### Roles vs Transports

Each role declares a `transport`:

- `transport: cli` — Oro spawns the runtime CLI (`claude -p` or `codex exec`) with the resolved model. Both `runtime` and `model` from the resolved tier are used.
- `transport: api` — Oro calls a provider's HTTP API directly. The role's `provider` (anthropic, openai) selects the endpoint. API roles ONLY honor `api_model:` (which references a key in `agent.api_models`); they do NOT read a `model:` or `tier:` key on the role.

`transport` is NOT user-configurable in the wizard. It is fixed per role by the implementation. For `transport: api` roles, users may override only the model string by editing `agent.api_models.<key>`, and the value must stay within the role's pinned `provider`. Cross-provider model strings (e.g., `gpt-5.5` for `provider: anthropic`) fail validation at load.

This boundary exists because the estimator (`pkg/dispatcher/estimate.go:18-21`) calls `https://api.anthropic.com/v1/messages` directly with `ANTHROPIC_API_KEY` and has no provider abstraction. Adding one is out of scope for this extension.

### Resolution Rules (CLI roles)

For any `transport: cli` role:

1. If `roles[role].runtime` AND `roles[role].model` are both set → use them, plus `roles[role].reasoning` when present.
2. Else if `roles[role].tier` is set → use `tiers[tier].runtime`, `tiers[tier].model`, and `tiers[tier].reasoning`.
3. Else fall back to the built-in default tier mapping (the table above).

A CLI role MUST NOT specify a partial override (only `runtime` or only `model`). Invalid configs fail loud at load time. This keeps "what binary will spawn?" deterministic and inspectable.

### Resolution Rules (API roles)

For any `transport: api` role:

1. If `roles[role].api_model` is set → look up `agent.api_models[<key>]` (validated against `role.provider`).
2. Else fall back to the built-in default for that role.

API roles do **NOT** read `roles[role].model` and do **NOT** inherit from `agent.tiers`. The `tier` field is invalid on API roles. The loader rejects API-role config with a `tier:` key.

If validation fails (e.g., `api_models.anthropic_fast: gpt-5.5` referenced by `provider: anthropic`), the loader rejects the config with a message naming the role, the api_model key, and the conflicting model. The estimator gracefully degrades to "no estimate" if `ANTHROPIC_API_KEY` is missing — that behavior is preserved.

### Backward Compatibility

- Missing `agent` block → all built-in defaults; equivalent to current Claude-only behavior.
- `ORO_AGENT_RUNTIME` env var → still honored as the runtime when no `tiers.*.runtime` is explicitly set; explicit per-tier values win. If both `agent.tiers` and `ORO_AGENT_RUNTIME` are set and disagree, the env var is ignored with a single startup warning.
- **Legacy bead hydration (corrected)**: `pkg/beadstore/sqlite.go:693` and `pkg/beadstore/readtx.go:244` currently copy `metadata["model"]` into `Bead.Model` only when `Bead.Model == ""`. The fix MUST preserve that guard. Specifically, at hydration time:
  - If the SQLite `model` column is non-empty → set `Bead.Model` from that column verbatim. Do NOT touch `Bead.Tier`.
  - Else if `metadata.model ∈ {opus, sonnet, haiku}` AND `Bead.Tier` is empty → write the mapped tier into `Bead.Tier`. Leave `Bead.Model` empty.
  - Else if `metadata.model` is a provider-native string (e.g., `claude-opus-4-7`, `gpt-5.5`) AND `Bead.Model` column is empty → set `Bead.Model` from metadata.
  - Else → leave both empty.

  This guarantees a row with explicit provider-native `model` plus stale legacy `metadata.model` keeps the explicit column value; only legacy-only rows convert to tiers.
- `--model` CLI flag accepts both tier names (`fast|balanced|deep|background`) and provider-native strings. Tier names are stored as `Bead.Tier`; provider-native strings as `Bead.Model`.

## Wizard Flow (`oro init` and `oro config wizard`)

Five prompts, with smart defaults:

1. **Primary runtime?** `claude | codex` — default `claude`. Used as default for the next 4.
2. **Fast tier model?** Defaults: `claude-haiku-4-5-20251001` for claude, `gpt-5.5` for codex. Allow overriding runtime here.
3. **Balanced tier model?** Defaults: `claude-sonnet-4-6` / `gpt-5.5`.
4. **Deep tier model?** Defaults: `claude-opus-4-7` / `gpt-5.5` (or strongest available).
5. **Background tier model?** Defaults: `claude-haiku-4-5-20251001` / `gpt-5.5`.

After collection: write `agent.tiers` to the config file (see precedence rules below). Roles are NOT prompted; the file is generated with the default tier→role table commented to invite manual edits.

If the target config file already has `agent.tiers`, the wizard offers: (a) keep, (b) overwrite, (c) edit only one tier.

### Non-Interactive Behavior (Required)

`oro init` is non-interactive today (`cmd/oro/cmd_init.go` has zero stdin/tty handling, 1114 lines). Use the existing isatty pattern from `cmd/oro/cmd_start.go:224` and `cmd/oro/cmd_attach.go:49` (both already depend on `github.com/mattn/go-isatty`). The wizard auto-detects non-TTY and degrades:

- If `os.Stdin` is not a TTY → skip prompts, write built-in defaults (Claude runtime, Claude tier models), emit a single line on stderr: `oro: writing default agent config; run 'oro config wizard' to customize`.
- If `--skip-wizard` flag is set → same behavior, no stderr notice. (Renamed from earlier draft's `--default-config` to avoid confusion with init's existing `--check` semantics.)
- The wizard NEVER blocks `--check` or `--quiet` modes; in those modes it is unconditionally skipped.

`--no-config` was considered and dropped: it conflicts with `--local`/stealth handling already in `cmd_init.go:330`. Existing flags `--check`, `--quiet`, `--local`, `--project-root` are preserved unchanged.

This is a HARD requirement, not optional. CI invocations, Dockerfile RUN steps, and piped-stdin scripts must continue to work without changes.

### Config File Location and Precedence

The `agent` block is **per-developer** by default, not per-project. Runtime selection encodes which CLIs are installed on the developer's machine; committing it to the repo would break teammates with different setups.

Existing config layer:

- Project config: `.oro/config.yaml` (handled by `pkg/langprofile/config.go`; round-trip via `BuildYAML` at line 153)
- Per-project state: `~/.oro/projects/<project>/` (resolved by `cmd/oro/paths.go:38,65,265`)
- `ORO_HOME` env var already controls the user-level Oro root

This extension adds NO new env var. The `agent` block reads from existing layers in this precedence (first match wins):

1. `$ORO_HOME/config.yaml` if `ORO_HOME` is set
2. `~/.oro/config.yaml`
3. `.oro/config.yaml` (project-committed; only `agent` block read for project-pinned overrides)
4. Built-in defaults

Wizard writes `~/.oro/config.yaml` by default. `--project` flag writes the `agent:` block into `.oro/config.yaml` (merging with existing `languages:`, `memory:` blocks — see "YAML merge" below). Other config blocks remain project-scoped exactly as they are today.

### YAML Merge Requirement

`pkg/langprofile/config.go:153` `BuildYAML` emits only `languages:`. `cmd/oro/cmd_init.go:706` overwrites the file. Adding an `agent:` block by re-emitting `BuildYAML` would silently drop unrelated user-edited blocks.

The wizard MUST use a node-level YAML edit (`gopkg.in/yaml.v3` Node API) to:

1. Parse existing config file as a Node tree
2. Replace or insert only the `agent:` mapping
3. Preserve other top-level keys (`languages`, `memory`, `project`) and their nested content
4. Preserve comments on a best-effort basis (yaml.v3 retains node-attached HeadComment / LineComment / FootComment but may normalize whitespace; do not commit to byte-identical round-trip)

Test contract — `oro init` followed by hand-edited `languages:` followed by `oro config wizard`:

- All hand-edited keys under `languages:` and `memory:` survive verbatim (key/value structure preserved).
- The `agent:` block reflects the wizard's choices.
- Comments attached to preserved keys remain on those keys (best-effort; not byte-identical reformatting).

Acceptance is structural preservation, NOT byte-identity. Reviewers should look for "did user content survive" not "did the file hash match."

## Required Entry Points

### Config layer (new)

- `pkg/config/agent.go` — schema, loader, resolver, validator.
- `pkg/config/agent_test.go` — round-trip, partial override rejection, mixing, legacy mapping.

### Routing layer

- `pkg/protocol/types.go` — keep `Tier` constants and existing helpers; **delete** `Tier.DefaultModel()` (line 108) entirely. Resolution moves to `pkg/agentmodel`; protocol stays leaf with no config dependency.
- `pkg/agentmodel/agentmodel.go` (new) — owns `ResolveForRole(role string) (runtime, model string)` and `ResolveForBead(role string, b protocol.Bead) (runtime, model string)`. Depends on `pkg/protocol` and `pkg/config`. **All new model-resolution code MUST call this package, NOT `pkg/agentruntime`.**
- `pkg/agentruntime/runtime.go` — `ReadRuntime()` is preserved as a back-compat shim returning the default runtime when no role context is available (used by stale callers and the worker payload fallback). NO `ResolveForRole` is added here.
- `pkg/agentruntime/codex/codex.go` — `normalizeCodexModel` no longer strips legacy names blindly; legacy strings route through tier resolution (in `agentmodel`) before reaching the adapter.
- `pkg/ops/ops.go` — replace ops `Type → "opus/sonnet/haiku"` mapping (lines 57–67, 84–88) with ops `Type → role name`; callers resolve the role via `agentmodel.ResolveForRole(roleName)`.

### Worker, dispatcher, ops paths

- `cmd/oro/cmd_work.go:85` — `--model` help text rewritten; flag accepts tier names or provider-native strings.
- `cmd/oro/cmd_work.go:288` — direct `protocol.DefaultModel` fallback replaced with `agentmodel.ResolveForBead(role="worker", bead)`. This is a separate call site from `cmd_start.go`'s dispatcher path.
- `cmd/oro/cmd_work.go:409, 726` — QG/review retry escalation must update both `runtime` and `model` (resolved from `agentmodel.ResolveForRole("worker_escalation")`), not just the model variable.
- `cmd/oro/cmd_work.go:796–801` — `inferFamily()` becomes legacy-only path used to map old strings to tiers.
- `cmd/oro/cmd_start.go:563` — `--model "sonnet"` default replaced with a direct lookup of `agent.tiers.balanced.model` when the resolved runtime is Claude. There is NO `manager` role; this is a CLI flag default sourced from a tier, not a config role lookup. The `manager` role is explicitly excluded from `agent.roles`.
- `cmd/oro/cmd_start.go:731` — runtime resolver receives full role config, not just runtime ID.
- `pkg/dispatcher/dispatcher.go:4324` — `AssignPayload` constructor must populate runtime+model pair from `agentmodel.ResolveForBead(role="worker", bead)`.
- `pkg/dispatcher/estimate.go:18, 47-53` — drop `estimatorModel` const; load `roles.estimator.api_model` from config at construction; provider stays pinned to Anthropic. Estimator does NOT stamp model on the bead — `dispatcher.go:4301` continues to set `EstimatedMinutes` only, and tier resolution is downstream via `Bead.ResolveTier`.

### Worker process: per-assignment runtime selection

The current worker process is bound to one runtime via `cmd/oro/cmd_worker.go:55` and `cmd/oro/agent_runtime.go:35`. To honor mixing across roles within a single bead's lifetime (e.g., escalation from `tier=balanced` Claude to `tier=deep` Codex):

- `pkg/protocol/message.go` — `AssignPayload` struct (line 88) adds `Runtime string` field alongside `Model`. (Note: `AssignPayload` lives in `message.go`, NOT `types.go`.)
- `cmd/oro/cmd_worker.go` — instantiate Claude AND Codex spawners on startup; route per-assignment based on `payload.Runtime`.
- `pkg/worker/worker.go` — `Spawn()` accepts a runtime+model pair, not just model. Existing `ClaudeSpawner` / Codex adapter implementations remain; the dispatcher selects which one to call.
- Backward compat: when `payload.Runtime == ""` (stale dispatcher pre-migration), the worker MUST NOT call `agentmodel` — it has no config layer. It logs a warning and falls back to `agentruntime.ReadRuntime()` for runtime and the existing `cfg.bead.Model` / `protocol.DefaultModel` chain at `pkg/worker/worker.go:519` for model. This shim survives one release after rollout, then is removed.

### Auxiliary roles

These currently call `agentruntime.ReadRuntime()` as a global switch (line numbers below). They MUST be refactored to take a role name and call `ResolveForRole(role)` instead — otherwise mixing breaks silently (e.g., user sets `roles.codesearch_reranker.runtime=claude` but `tiers.balanced.runtime=codex`, and the global shim returns the wrong runtime).

- `pkg/codesearch/claude_spawner.go:16, 34, 41, 51, 77` — drop `codexRerankModel` const and hardcoded `--model haiku`; `BuildCmdInWorkdir` must accept a role name (default `"codesearch_reranker"`); resolve runtime+model via `ResolveForRole`. Branch on resolved runtime, not on `ReadRuntime()`.
- `pkg/memory/extract_llm.go:25–26, 68, 128, 141` — drop `extractionModel` and `codexExtractionModel` consts; `spawnCommand` must accept a role name (default `"memory_extractor"`); resolve via `ResolveForRole`. The legacy switch on `"haiku"|"sonnet"|"opus"` becomes a tier-mapping switch.
- `pkg/dispatcher/estimate.go:18, 47-53` — drop `estimatorModel` const; load `roles.estimator.api_model` (which references a key in `agent.api_models`) at construction; provider stays pinned to Anthropic (transport=api). When `ANTHROPIC_API_KEY` is unset OR resolved model is non-Anthropic, return the existing zero-estimate fallback.

### Asset & hook layer

`model_key` is owned by Claude Code's PostToolUse payload (verified: `assets/hooks/compact_trigger.py:72` reads it from stdin). `assets/hooks/context_pct_writer.py:146` reads transcript model directly, not `model_key`. There is no Oro adapter layer between Claude and the hook. Therefore the mapping from Claude family → tier MUST live INSIDE the Python hooks, not in a new Oro process.

- `assets/thresholds.json`, `cmd/oro/_assets/thresholds.json` — add tier keys (`fast/balanced/deep/background`) alongside legacy `opus/sonnet/haiku`. Both keying schemes coexist indefinitely.
- `assets/hooks/compact_trigger.py`, `cmd/oro/_assets/hooks/compact_trigger.py` — when `model_key` is `opus|sonnet|haiku`, look up tier-keyed threshold first; fall back to legacy key. Pure Python change, no Oro process involved.
- `assets/hooks/context_pct_writer.py`, `cmd/oro/_assets/hooks/context_pct_writer.py` — same lookup pattern for context budgets keyed by transcript model string.

### Init / config commands

`oro models` already exists for ONNX artifacts (`cmd/oro/cmd_models.go:37`). The wizard re-run command CANNOT be `oro config models`. There is no `oro config` parent command today (`cmd/oro/root.go:24-51` lists every top-level command).

- `cmd/oro/cmd_config.go` (new) — `newConfigCmd()` parent with subcommands.
- `cmd/oro/cmd_config_wizard.go` (new) — `oro config wizard` re-runs the runtime/tier wizard idempotently.
- `cmd/oro/cmd_config_show.go` (new, optional) — `oro config show` prints resolved agent config and where each value came from. Useful for debugging precedence.
- `cmd/oro/cmd_init.go` — wizard implementation hook; new `--skip-wizard` flag; isatty detection via existing `mattn/go-isatty` dependency.
- `cmd/oro/root.go` — register `newConfigCmd()` alongside existing top-level commands.

### Bead generation

Verified during premortem and adversarial review:

- `pkg/ops/decompose_prompt.go:31` instructs the agent to call `oro task create --title=... --type=task --parent=... --acceptance=... --estimate=<min>`. No model/tier is written. Children inherit `DefaultTier = TierBalanced` (`pkg/protocol/types.go:105`).
- `pkg/worker/prompt.go:265, 427` and `pkg/ops/epic_fix_prompt.go:26` ALSO instruct agents to create tasks. None thread tier today.
- `assets/skills/beadcraft/SKILL.md` and `assets/skills/spec/SKILL.md` are instruction docs for human-driven workflows. They do not emit JSONL or stamp model fields.
- `pkg/ops/ac_prompt.go`, `pkg/ops/write_ac_prompt.go` — verified they do not stamp model strings.
- `pkg/dispatcher/dispatcher.go:4301` — estimator only sets `EstimatedMinutes`; it does NOT stamp `Bead.Model`. The earlier draft's "estimator stamps Bead.Tier instead of Bead.Model" was based on a misread; estimator stamping is dropped from acceptance.

Real entry points to update — the schema changes BEFORE the CLI flag:

- `pkg/beadstore/store.go:112` — `CreateParams` struct adds `Tier string` field. (`Model` is intentionally NOT added; provider-native overrides land via `metadata` until that surface is redesigned.)
- `pkg/beadstore/sqlite.go:189` — `INSERT INTO beads (...)` adds `tier` column write.
- `pkg/beadstore/testfake.go` — same insert path.
- `pkg/beadstore/sqlite.go:693` and `pkg/beadstore/readtx.go:244` — hydration converts legacy `metadata.model=opus|sonnet|haiku` into `Bead.Tier` (NOT `Bead.Model`); see "Backward Compatibility" above.
- `cmd/oro/cmd_bead.go` — add `--tier` flag to `oro task create`. Accepts `fast|balanced|deep|background`. Empty default.
- `pkg/ops/decompose_prompt.go:31` — update the `oro task create ...` invocation to include `--tier=<inferred-from-parent>` when parent has a tier.
- `pkg/worker/prompt.go:265, 427` — same update for any worker prompt that creates tasks.
- `pkg/ops/epic_fix_prompt.go:26` — same for epic-fix flow.

### Migration scope (Dolt → SQLite)

`cmd/oro/cmd_bead_migrate.go` already reads/writes both `tier` and `model` (lines 291, 944, 1269, 1330). Tier semantics changes affect migration policy:

- Legacy bead with `metadata.model=opus|sonnet|haiku` AND empty `tier` → migrate to `tier=deep|balanced|fast` and clear `model`.
- Legacy bead with provider-native `model` AND empty `tier` → preserve `model`, leave `tier` empty.
- Legacy bead with both `tier` and `model` set → preserve both (explicit user intent).
- Reconcile path must apply the same rules so re-imports don't silently revert tier-mapped beads back to their legacy form.

Add a migration test that round-trips legacy `metadata.model=opus` through export → import and asserts `tier=deep, model=""`.

## Acceptance Contract

The extension is done when:

1. No `agent` block anywhere → existing Claude defaults preserved end-to-end. No regression in current Claude-only swarm.
2. `oro init` on a fresh project with TTY → 5-prompt wizard writes `~/.oro/config.yaml` with a populated `agent.tiers` block.
3. `oro init` on a fresh project WITHOUT TTY (CI, Dockerfile, piped stdin) → writes built-in defaults silently with one stderr notice; never hangs or errors. Verifies via existing `mattn/go-isatty` precedent (`cmd_start.go:224`, `cmd_attach.go:49`).
4. `oro init --skip-wizard` → writes built-in defaults silently, no prompts, no stderr notice.
5. `oro init --check`, `oro init --quiet` → behave identically to today; wizard is unconditionally skipped.
6. `oro config wizard` → re-runs the wizard idempotently and updates the existing block; works in TTY only (errors clearly in non-TTY).
7. `oro config show` → prints resolved agent config and source layer for each value.
8. **Mixing end-to-end test:** with `tiers.deep.runtime=codex` and `tiers.balanced.runtime=claude`, a worker assigned a `tier=balanced` bead spawns Claude, and when QG retry escalation kicks in (worker_escalation→deep) the SAME worker process spawns Codex on the next attempt. Verifies AssignPayload carries Runtime and the worker selects spawner per-assignment.
9. `roles.codesearch_reranker.runtime=claude` with `tiers.balanced.runtime=codex` correctly spawns Claude for the reranker via `agentmodel.ResolveForRole("codesearch_reranker")`, NOT via `agentruntime.ReadRuntime()`.
10. Legacy bead with `metadata.model=opus` and no `tier` field → on hydration, `Bead.Tier=deep` and `Bead.Model=""`. `agentmodel.ResolveForBead("worker", bead)` returns the configured `tiers.deep.model`+`runtime`, NOT `"opus"`. (`Bead.ResolveModel()` itself is a pure shim returning `b.Model`; the configured value comes from `agentmodel`, not from `protocol`.)
11. Estimator stays Anthropic-only: `roles.estimator.api_model: anthropic_fast` references `agent.api_models.anthropic_fast`. If a user sets `agent.api_models.anthropic_fast: gpt-5.5`, config load rejects with a clear error naming the role and provider mismatch.
12. `tiers.fast.model=gpt-5.5` does NOT affect estimator behavior. Estimator continues to call Anthropic.
13. `oro task create --tier=deep` writes `bead.Tier=deep` to the SQLite store via `CreateParams.Tier`. Verifies `pkg/beadstore/store.go:112` schema change.
14. Decomposer / epic-fix / worker prompts that create tasks include `--tier=<inferred-from-parent>` when parent has a tier.
15. `oro work --model fast` and `oro work --model claude-opus-4-7` both work; help text reflects tier-first vocabulary.
16. `thresholds.json` accepts both legacy keys (`opus/sonnet/haiku`) and tier keys (`fast/balanced/deep/background`). Hooks (`compact_trigger.py`, `context_pct_writer.py`) prefer tier keys when both present, fall back to legacy keys.
17. Partial CLI role override (`{ runtime: codex }` without `model`) fails config validation with a clear error.
18. Cross-runtime model mismatch (`tiers.deep.runtime=codex` + `tiers.deep.model=claude-opus-4-7`) fails config validation with a clear error. NO warn-and-allow.
19. YAML round-trip: `oro init` followed by hand-edited `languages:` followed by `oro config wizard` preserves the hand-edits.
20. `~/.oro/config.yaml` `agent` block takes precedence over `.oro/config.yaml` `agent` block; non-`agent` sections of `.oro/config.yaml` (project, languages) continue to be read.
21. `protocol` package has zero direct imports of `pkg/config` AND zero direct imports of `pkg/agentmodel`. Test asserts via `go list -f '{{.Imports}}' ./pkg/protocol` (direct imports only, not transitive). A grep test also verifies `pkg/protocol/*.go` files contain no `oro/pkg/config` or `oro/pkg/agentmodel` import lines.

## Minimum Test Matrix

- unit: config schema round-trip, partial CLI role override rejection, cross-runtime mismatch rejection, API-role with `tier:` key rejected
- unit: hydration legacy `metadata.model=opus|sonnet|haiku` → tier mapping when SQLite model column is empty AND `Bead.Tier` is empty (guard preserved)
- unit: hydration with explicit provider-native `model` column → `Bead.Model` preserved, `Bead.Tier` untouched
- unit: role resolution precedence (explicit role.runtime+model > tier > default)
- unit: `inferFamily()` legacy compatibility for both Claude and provider-native strings
- integration: wizard writes valid config; `oro config wizard` round-trips
- integration: estimator API call uses `roles.estimator.api_model`; `tiers.fast` change does not affect estimator
- integration: codesearch reranker uses `agentmodel.ResolveForRole("codesearch_reranker")`
- integration: legacy bead with `metadata.model=opus` and empty SQLite model column runs end-to-end through configured deep runtime
- integration: dispatcher resolves runtime+model dispatcher-side and sends both on AssignPayload; mixing escalation crosses runtime within one worker process
- structural: `pkg/protocol` direct imports contain no `pkg/config` or `pkg/agentmodel`

Suggested split test names:

- `TestAgentConfigPartialOverrideRejected`
- `TestAgentConfigCrossRuntimeMismatchRejected`
- `TestAgentConfigAPIRoleRejectsTierKey`
- `TestAgentConfigMixingClaudeAndCodex`
- `TestLegacyMetadataModelMapsToTierOnlyWhenEmpty`
- `TestExplicitModelColumnPreserved`
- `TestRoleResolutionPrecedence`
- `TestEstimatorReadsAPIModelsBlock`
- `TestEstimatorIgnoresTierChanges`
- `TestCodesearchRerankerRoleResolves`
- `TestMemoryExtractorRoleResolves`
- `TestInitWizardWritesAgentTiers`
- `TestConfigWizardCommandIdempotent`
- `TestWorkModelFlagAcceptsTierAndNative`
- `TestYAMLMergePreservesUserEdits`
- `TestProtocolPackageHasNoConfigImport`
- `TestDispatcherSendsRuntimeOnAssignPayload`
- `TestWorkerSelectsSpawnerFromPayloadRuntime`
- `TestEscalationCrossesRuntimeInOneWorker`

## Migration

Two-phase rollout to avoid breaking installed projects:

**Phase A — Config + resolver land. Bead-gen unchanged.**

- Existing beads still have `metadata.model=opus|sonnet|haiku`; loader maps to tier on read.
- Wizard available, but `.oro/config.yaml` without `agent` block uses built-in defaults.
- All roles resolved through config or defaults.
- Hardcoded auxiliary consts removed.

**Phase B — Bead-gen emits `tier:` for new beads.**

- Decompose, beadcraft, spec switch to writing `tier:`.
- Legacy beads remain readable indefinitely.
- Documentation and skills updated to use tier vocabulary.

## Risks

| Risk | Mitigation |
|---|---|
| Wizard breaks non-interactive `oro init` (CI, Docker, scripts) | Auto-detect non-TTY via existing `mattn/go-isatty` (already a dep); `--skip-wizard` as explicit escape hatch |
| Adding config dependency to `pkg/protocol` creates import cycle | New `pkg/agentmodel` package owns resolution; `protocol` stays leaf |
| Worker process bound to one runtime breaks mixing claim | `AssignPayload` carries Runtime; worker holds both spawners and selects per-assignment |
| Legacy bead hydration pins `Bead.Model="opus"` and bypasses tier resolution | Hydration converts legacy Claude model names to `Bead.Tier`, leaving `Bead.Model` empty |
| `oro init` overwrites config and drops user-edited blocks | Wizard uses node-level YAML edit (yaml.v3 Node API); round-trip test required |
| `--no-config` flag conflicts with init's stealth/local handling | Flag dropped from spec; only `--skip-wizard` added; existing `--check`/`--quiet`/`--local`/`--project-root` preserved |
| `oro config models` namespace conflicts with existing `oro models` (ONNX) | Wizard subcommand is `oro config wizard` under a new `oro config` parent |
| Estimator inheriting from CLI tiers breaks when wizard changes `tiers.fast` | `agent.api_models` is a separate block; estimator references it explicitly, never inherits from `agent.tiers` |
| `agentruntime.ReadRuntime()` global switch silently misroutes when role/tier disagree | Auxiliary spawners (codesearch, memory) refactored to take role name and call `agentmodel.ResolveForRole`; `ReadRuntime()` becomes last-resort fallback |
| Cross-runtime model mismatch (Claude model + Codex runtime) becomes runtime crash | Reject at config load for known runtimes (`claude`, `codex`); no warn-and-allow |
| `thresholds.json` rekey breaks installed Python hooks | Hooks accept BOTH keying schemes indefinitely; tier-key lookup happens INSIDE the Python hook (no Oro adapter exists or is added) |
| `CreateParams` schema lacks Tier, so `--tier` flag can't persist | Schema bead lands BEFORE the CLI flag bead in the dependency order |
| Migration import/reconcile silently re-imports legacy `metadata.model` and defeats tier routing | Migration policy in `cmd_bead_migrate.go` codified; round-trip test required |
| Project-shared config encodes per-developer setup wrongly | `agent` block defaults to user-level (`~/.oro/config.yaml`); project-level only via explicit `--project` flag |
| 24+ beads of work tracked in docs but not in beadstore | Materialize parent epic + this extension's epic in `.beads/beads.db` BEFORE any implementation bead is started |

## Decisions Locked (Previously Open)

These were Open Questions in draft v1, now decided based on premortem findings:

1. **Config location.** `agent` block is per-developer by default. Wizard writes `~/.oro/config.yaml`; `--project` flag writes `.oro/config.yaml` for genuinely-shared setups. Other config blocks (project, languages) remain project-scoped. Reason: runtime selection encodes installed-CLI presence, which differs per developer.

2. **Manager session model.** Stays a CLI flag (`cmd_start.go:563 --model`) sourced from `tiers.balanced.model` when runtime is Claude. Not added as a `manager` role. Reason: it controls the human-facing tmux pane, not a worker role; conflating them muddies the config table.

3. **Transport per role.** Added as a fixed-per-role property (`cli|api`), not user-configurable. Estimator is pinned to `transport: api, provider: anthropic`. CLI roles use `transport: cli`. Reason: estimator (`pkg/dispatcher/estimate.go`) calls Anthropic API directly with no provider abstraction; pretending it's freely configurable breaks at runtime.

4. **Already-done protocol work.** `Bead.Tier`, `Tier` constants, `LegacyModelToTier`, `ResolveTier`, `ResolveModel` are not in scope here — they exist (`pkg/protocol/types.go:52,89-151`). The remaining gap is **config-aware resolution outside `protocol`**, owned by the new `pkg/agentmodel` package. `Tier.DefaultModel()` is deleted, NOT updated to consult config.

## Remaining Open Questions

1. **First-run wizard on existing projects.** Should `oro init` re-run the wizard if it detects an existing config without `agent` block on upgrade?
   - Recommendation: yes, gated behind a one-time prompt; record completion in `.oro/.wizard-state` to avoid re-asking. Skip in non-TTY.

2. **Auxiliary role transport flexibility (future).** Should `codesearch_reranker` and `memory_extractor` ever support `transport: api` for cost-sensitive setups (cheaper than CLI spawn)?
   - Recommendation: defer. Keep them `transport: cli` in v1. Revisit if a clear cost case emerges.

3. **Validation strictness for cross-provider model strings.** ~~Warn-and-allow for tier rows~~ — REJECTED. Codex strips Claude-family names today (`pkg/agentruntime/codex/codex.go:86`); removing the strip + allowing `claude-opus-4-7 → codex exec` becomes a runtime crash, not a soft warning. Resolution: **reject mismatch at load** for both `claude` and `codex` runtimes. Future runtimes get an opt-in escape hatch only when explicitly registered.

## Recommended Bead Breakdown

Epic: `agent-runtime-config` (sibling of `agent-runtime-agnostic`).

**Tracking note:** Before any bead in this list is implemented, the parent epic (`agent-runtime-agnostic`) and this epic must both be materialized in the beadstore. Both decomp docs currently exist on disk only; neither is in `.beads/beads.db`. Implementing without bead tracking is how we ended up partially shipping the parent spec without a status surface.

Order matters: schema + YAML utility → resolver → call sites → wire format → CLI surface → assets → docs/tests. The YAML merge utility moves UP because the init wizard depends on it.

1. `feat(config): add pkg/config/agent.go with tiers + api_models + roles schema, loader, validator, cross-runtime mismatch rejection, API-role tier-key rejection`
2. `feat(yaml): node-level YAML merge utility (yaml.v3 Node API) used by config writers; preserves user-edited top-level keys`
3. `feat(agentmodel): new pkg/agentmodel package; ResolveForRole and ResolveForBead; depends on protocol + config; protocol stays leaf`
4. `refactor(beadstore): add Tier field to CreateParams; SQLite + testfake insert tier column; hydration converts legacy metadata.model={opus,sonnet,haiku} into Bead.Tier ONLY when SQLite model column is empty AND tier is empty`
5. `refactor(protocol): delete Tier.DefaultModel() in pkg/protocol/types.go; Bead.ResolveModel becomes pure shim returning b.Model; add Runtime field to AssignPayload in pkg/protocol/message.go (NOT types.go)`
6. `refactor(dispatcher): resolve runtime+model dispatcher-side via agentmodel.ResolveForBead; populate AssignPayload.Runtime and Model on every send`
7. `refactor(worker): worker process holds both Claude+Codex spawners; selects per-assignment from AssignPayload.Runtime; back-compat shim warns and uses ReadRuntime when payload.Runtime is empty`
8. `refactor(routing): cmd_work, cmd_start, ops route through agentmodel; cmd_start manager flag default reads tiers.balanced.model directly (no manager role)`
9. `refactor(cmd_work): QG/review retry escalation updates runtime+model pair via worker_escalation role`
10. `refactor(aux-roles): codesearch_reranker, memory_extractor take role name and call agentmodel.ResolveForRole; ReadRuntime() retained only as last fallback`
11. `refactor(estimator): drop estimatorModel const; load roles.estimator.api_model from agent.api_models with provider validation; estimator does NOT stamp model on bead`
12. `feat(cli): oro task create --tier flag accepting fast|balanced|deep|background; persists via CreateParams.Tier`
13. `refactor(prompts): decompose_prompt, worker prompt task-creation, epic_fix_prompt include --tier=<inferred-from-parent>`
14. `feat(init): wizard with isatty detection (mattn/go-isatty existing dep); --skip-wizard flag; preserves --check/--quiet/--local/--project-root; uses YAML merge utility from bead 2`
15. `feat(cli): new oro config parent command with config wizard and config show subcommands; register in root.go alongside oro models`
16. `refactor(cli-flags): oro work/start --model accept tier names and provider-native strings; rewrite help text`
17. `refactor(precedence): $ORO_HOME/config.yaml > ~/.oro/config.yaml > .oro/config.yaml for agent block only; non-agent blocks remain project-scoped`
18. `refactor(thresholds): assets/thresholds.json adds tier keys alongside legacy keys; compact_trigger.py and context_pct_writer.py prefer tier keys with legacy fallback (pure Python change)`
19. `refactor(migration): cmd_bead_migrate.go applies legacy-model-to-tier mapping on import and reconcile; round-trip test`
20. `docs(runtime-config): rewrite README, beacons, role docs to reference tier-first config, per-developer agent block, oro config commands`
21. `test(runtime-config): split coverage for mixing end-to-end (escalation crosses runtime), legacy hydration with both guards, wizard idempotency, non-TTY behavior, partial-override rejection, cross-runtime validation, YAML structural round-trip, migration round-trip, protocol-has-no-config-or-agentmodel-import`

## Recommendation

Land Phase A end-to-end before starting Phase B. The config + resolver layer is the dependency for everything else; bead-gen changes only become safe once readers can map legacy values to tiers. Treat this as additive: every step preserves Claude-only default behavior, and the wizard is opt-out, not opt-in.
