# Codex Harness Parity Design

**Date:** 2026-05-06
**Status:** Draft — Stage 1 brainstorm
**Extends:** [2026-04-22-agent-runtime-agnostic-design.md](./2026-04-22-agent-runtime-agnostic-design.md), [2026-05-06-agent-runtime-config-design.md](./2026-05-06-agent-runtime-config-design.md)
**Goal:** Make multi-provider real. Every discipline mechanism Claude has on the Oro harness must work on Codex. The audit (Stage 2 Q1) is to confirm load-bearingness, NOT to drop hooks — the bar is "Codex sessions get the same discipline Claude sessions get." `oro-search-hook` (the AST-summary `PreToolUse` interceptor on `Read`) is called out by the user as the most load-bearing piece.

**Framing locked (Stage 2 Q1, Q3):** Discipline parity is the bar. Audit only to confirm each hook is doing real work (and to catch Codex-native duplication where it exists), not to drop scope. `oro-search-hook` is priority #1 because it's a token-efficiency cornerstone — without it, every file Read on Codex reloads full source instead of an AST summary.

**Scope locked (Stage 2 Q4):** Wedge 3 — port everything except `PreCompact` (no Codex event). All 7 user-level + 5 project-level hooks plus `oro-search-hook` get Codex equivalents. PreCompact-equivalent handling is deferred to a separate sub-decision (see Open Question 1).

**Sequencing locked (Stage 2 Q5):** Ship parity in parallel with the runtime-config epic, not gated behind it. The two epics share enough infrastructure (agent-assets sync, plugin install, hook generation) that landing them together is cheaper than serializing.

**Scope split (Stage 3 finding — adversarial review):** Two distinct cases hidden behind "Codex sessions":

- **Case A — Dispatcher-spawned Codex workers**: Oro spawns `codex exec` as a worker process. The worker registers on UDS, sends heartbeats, runs through `pkg/dispatcher/dispatcher.go:1505 handleHeartbeat → triggerCheckpoint → respawnWorker`. The existing handoff machinery is reusable as-is because the Codex worker IS a registered Oro worker. Hooks fire inside that subprocess with full UDS access.
- **Case B — Interactive Codex sessions**: User runs `codex` directly in their shell. No Oro worker registration, no UDS connection, no `workerID`. Hooks have no path to enqueue handoff because `handleHandoff` (dispatcher.go:2460) requires `workers[workerID]` lookup.

This spec covers **Case A only**. Case B (interactive Codex parity) is deferred to a future spec — the dispatcher needs a non-worker UDS signal endpoint that doesn't exist today, and designing it is its own scope. The user's #1 pain (search hook, skills enforcement) lands in Case A as long as `oro work --runtime codex` and `oro start` with Codex tier configuration both exercise the parity surface.

**Architecture locked (Stage 2 Q6):** Durable runtime adapter contract — runtime-neutral hook spec in `pkg/agentassets/spec.go`; per-runtime generators (`agentassets/claude.go`, `agentassets/codex.go`) translate it into `~/.claude/settings.json` hooks section and `~/.codex/plugins/oro/hooks.json` respectively. New runtime = new generator file. Aligns with parent runtime-agnostic spec section 4 (`InstructionLayout`) and `Runtime` interface.

This decision implicitly resolves two earlier open questions:

- **Tool-name matcher** (was Open Q2): Option A — generated runtime-specific matchers from canonical event spec — wins by construction.
- **AGENTS.md source-of-truth** (was Open Q5): single source `assets/ORO_AGENT.md`; per-runtime generators emit `CLAUDE.md` and `AGENTS.md`.

## Problem

The two parent specs assume "Codex skill parity first, hook parity later" because we believed Codex lacked a hook surface. **That premise is wrong.** Verified during research:

- Codex has stable, default-on hooks (`codex features list` confirms `codex_hooks: stable: true`).
- Hook events supported: `SessionStart`, `PreToolUse`, `PostToolUse`, `Stop`, `UserPromptSubmit`.
- Hook config format is essentially identical to Claude's (`matcher` + `hooks: [{type, command}]`).
- Codex has `~/.codex/skills/`, `~/.codex/rules/`, `~/.codex/memories/`, plus `AGENTS.md` as a repo-rooted instruction file (built-in).

### Observed pain (Stage 2 Q2)

The user (`aakash@wyndly.com`) wants to use Codex right now and the experience is not good enough yet. This is real pain, not anticipated. The runtime-config epic could ship with `tiers.balanced.runtime=codex` enabled, but the user has already tried that path and the result falls short.

Without explicit parity work, Oro workers running on Codex would lose:

- `using-skills` enforcement (Claude installs `enforce_skills.py` as a `PreToolUse` hook)
- Auto-format on Edit/Write (`auto-format.sh` as `PostToolUse` hook)
- Prompt-injection guard on Read/WebFetch/Bash (`prompt_injection_guard.py`)
- Context pruning (`context_pruner.py` as `PostToolUse`)
- Stop checklist (`stop-checklist.sh`)
- Pre-compact handoff capture (`pre_compact.py`) — **no Codex equivalent event; needs design**
- Session-start global priming (`session_start_global.py`)

## Inventory: What Oro Installs to Claude Today

Verified from `cmd/oro/cmd_global_oro_approach.go` and `~/.claude/settings.json` after running `oro global-skills`.

### User-level (`~/.claude/`)

| Artifact | Path | Source |
|---|---|---|
| Skills | `~/.claude/skills/` (symlinks) | `~/.oro/.claude/skills/` |
| Hooks (scripts) | `~/.claude/hooks/*.py`, `*.sh` | `assets/hooks/` |
| Settings hooks section | `~/.claude/settings.json` `hooks:` | merged via `updateGlobalSettings` |
| User instructions | `~/.claude/CLAUDE.md` | user-managed (Oro does NOT write) |

### Project-level (`<repo>/.claude/`)

| Artifact | Path | Source |
|---|---|---|
| Project skills | `.claude/skills/` | `assets/skills/` (selected) |
| Project commands | `.claude/commands/` | `assets/commands/` |
| Project instructions | `.claude/CLAUDE.md` | `assets/CLAUDE.md` |
| Rules | `.claude/rules/` | NOT currently extracted by `oro init` (manual) |

### Hook events Oro currently installs

User-level (`~/.claude/settings.json`):

| Event | Matcher | Script | Purpose |
|---|---|---|---|
| `SessionStart` | (any) | `session_start_global.py` | Inject `using-skills` reminder, project context |
| `PreCompact` | (any) | `pre_compact.py` | Capture handoff state before compaction |
| `PreToolUse` | (any) | `enforce_skills.py` | Block tool calls until `using-skills` checked |
| `PostToolUse` | `Read\|WebFetch\|Bash` | `prompt_injection_guard.py` | Detect prompt-injection in tool results |
| `PostToolUse` | `Edit\|Write` | `auto-format.sh` | Run language formatter on edited files |
| `PostToolUse` | (any) | `context_pruner.py` | Trim context window |
| `Stop` | (any) | `stop-checklist.sh` | Enforce git-commit / push checklist |

Project-level (`<repo>/.claude/settings.json`) — via `oro init`:

| Event | Matcher | Script | Purpose |
|---|---|---|---|
| `SessionStart` | (any) | `enforce_skills.py`, `session_start_extras.py` | Skills enforcement, project priming |
| `SessionStart` | `compact` | `session_start_compact.py` | Post-compact restoration |
| `PreCompact` | (any) | `pre_compact.py` | Pre-compact handoff capture |
| `PreToolUse` | `Read` | **`oro-search-hook`** (Go binary) | AST-summary interception for Go source — token-efficiency cornerstone |
| `PostToolUse` | (any) | `context_pct_writer.py`, `context_pruner.py` | Context tracking + pruning |

**`oro-search-hook` deep-dive** (`cmd/oro-search-hook/main.go`):

- Built during `oro init` (line 521) into `$ORO_HOME/hooks/oro-search-hook`.
- Intercepts `Read` tool calls; if file is a large Go source, returns AST summary (function signatures, type declarations) instead of raw content.
- Hook response format: `{"permissionDecision": "deny", "permissionDecisionReason": "<summary>"}` — Claude-specific schema.
- Fail-open: any error path returns `{}` (allow) so a broken hook never blocks the user.
- Matcher: `Read` (Claude tool name).

## Codex Surface (Verified)

### Hook events

Codex native binary string-table dump confirms:

- `SessionStart` ✓
- `PreToolUse` ✓
- `PostToolUse` ✓
- `Stop` ✓
- `UserPromptSubmit` (Codex-only — no Claude equivalent)
- **No `PreCompact`** — Codex uses `thread/compact` JSON-RPC method instead

### Hook config format

Same as Claude (verified via real Codex plugin `~/.codex/.tmp/plugins/plugins/figma/hooks.json`):

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Write|Edit",
        "hooks": [
          { "type": "command", "command": "./scripts/post_write_figma_parity_check.sh" }
        ]
      }
    ]
  }
}
```

### Hook discovery

- Plugin-based: hooks come via plugin packages installed through Codex's marketplace system.
- JSON-RPC method `hooks/list` aggregates hooks from all installed plugins.
- **Open question**: is there a user-level `hooks.json` file Codex reads outside the plugin system? Research did not find one. Provisional answer: **No — Oro must ship as a Codex plugin to install hooks.**

### Other surface

| Codex artifact | Path | Equivalent to |
|---|---|---|
| Skills | `~/.codex/skills/` | `~/.claude/skills/` |
| Rules | `~/.codex/rules/` | `~/.claude/rules/` (project) |
| Memories | `~/.codex/memories/` | none in Claude |
| Sessions | `~/.codex/sessions/` | `~/.claude/sessions/` |
| Config | `~/.codex/config.toml` (TOML) | `~/.claude/settings.json` (JSON) |
| Plugins | `~/.codex/.tmp/plugins/` | `~/.claude/plugins/` |
| Project instructions | `<repo>/AGENTS.md` (built-in concept, dir-scoped) | `<repo>/.claude/CLAUDE.md` |

## Parity Plan

### Strategy: Oro as a Codex plugin

Hooks delivered via a Codex plugin package named `oro`, installed by `oro init` / `oro agent-assets sync --runtime codex`. The plugin lives at `~/.codex/plugins/oro/` (or installed via marketplace pointer to `~/.oro/agents/codex/plugin/`).

Plugin contents:

```
~/.codex/plugins/oro/
├── plugin.json          # plugin manifest
├── hooks.json           # hook event → script mapping
├── scripts/
│   ├── enforce_skills.py
│   ├── auto-format.sh
│   ├── prompt_injection_guard.py
│   ├── context_pruner.py
│   ├── session_start_global.py
│   └── stop-checklist.sh
└── skills/              # optional: bundled skills
```

Oro generates `hooks.json` from the same source-of-truth used to generate `~/.claude/settings.json` hooks section. Both target the SAME script files (which can live in `~/.oro/agents/shared/hooks/` and be referenced from both runtimes' plugin/settings).

### Hook event mapping

| Oro hook | Claude event | Codex event | Notes |
|---|---|---|---|
| `enforce_skills.py` | PreToolUse | PreToolUse | direct port |
| `auto-format.sh` | PostToolUse `Edit\|Write` | PostToolUse `Edit\|Write` | direct port |
| `prompt_injection_guard.py` | PostToolUse `Read\|WebFetch\|Bash` | PostToolUse `Read\|WebFetch\|Bash` | verify Codex emits same tool names |
| `context_pruner.py` | PostToolUse | PostToolUse | direct port |
| `session_start_global.py` | SessionStart | SessionStart | direct port |
| `stop-checklist.sh` | Stop | Stop | direct port |
| `pre_compact.py` | PreCompact | (none) | **Gap**: implement as Codex `UserPromptSubmit` hook checking turn count, OR use Codex's compaction notification API |

### Tool-name compatibility

Both Claude and Codex expose tools with similar names (`Read`, `Write`, `Edit`, `Bash`, `WebFetch`). Verified from Codex system prompt (mentions `update_plan`, `apply_patch`, `command/exec`). Codex tool names may differ from Claude's:

- Claude `Edit`/`Write` → Codex `apply_patch`
- Claude `Bash` → Codex `command/exec` (via `shell_tool`)
- Claude `Read` → Codex shell `cat`/equivalent (or `fs/readFile`)

**Implication**: hook matchers may need per-runtime translation. The shared-script approach holds, but the matcher patterns diverge. Either:

- **Option A**: Two hook config files (one per runtime) generated from one source-of-truth
- **Option B**: Codex hooks list includes both Claude tool names AND Codex tool names in matcher, accept whichever the runtime emits

Recommendation: **Option A** — generated from one source-of-truth keyed by canonical event names; emitted with runtime-specific tool-name matchers.

### Instructions file: AGENTS.md vs CLAUDE.md

Codex has a built-in `AGENTS.md` concept. Oro should:

- Continue extracting `.claude/CLAUDE.md` for Claude users
- ALSO extract `AGENTS.md` for Codex users (or both, since Codex respects AGENTS.md regardless of which runtime is active)
- Source-of-truth: `assets/ORO_AGENT.md` (per parent spec); generate `CLAUDE.md` and `AGENTS.md` from it

### Skills: shared via symlink

Already partly done — `~/.codex/skills/` is symlinked to `~/.oro/.claude/skills/`. Formalize:

- `oro agent-assets sync --runtime codex` ensures `~/.codex/skills/` symlinks (or copies) from `~/.oro/agents/shared/skills/`
- Same source-of-truth as Claude's `~/.claude/skills/`

### Rules

`~/.codex/rules/default.rules` already exists. Oro currently does NOT install to `~/.claude/rules/` (verified — `cmd_global_oro_approach.go` only writes settings + hooks + skills). User-managed.

**Decision needed**: should Oro now install rules to BOTH runtimes? Or remain user-managed?

## Required Entry Points

### New code

- `cmd/oro/codex_plugin.go` (new) — generate Codex plugin package (`hooks.json`, `plugin.json`, scripts)
- `cmd/oro/cmd_agent_assets.go` (or extension to `cmd_global_oro_approach.go`) — `oro agent-assets sync --runtime codex|claude|all`
- `pkg/agentassets/` (new) — shared logic for generating Claude settings.json AND Codex hooks.json from a single hook spec

### Existing code to refactor

- `cmd/oro/cmd_global_oro_approach.go:262` `globalHooks()` — split into runtime-neutral spec + Claude-formatter + Codex-formatter
- `cmd/oro/cmd_init.go:512,656` extractAssets — also extract `AGENTS.md` alongside `.claude/CLAUDE.md`
- `assets/hooks/*.py` — verify each hook reads tool name from a runtime-neutral env or both Claude and Codex hook input shapes

### Acceptance contract

The parity work is done when:

1. After `oro agent-assets sync --runtime codex`, `codex` JSON-RPC `hooks/list` returns Oro's hooks for SessionStart, PreToolUse, PostToolUse (Edit|Write, Read|WebFetch|Bash, any), Stop.
2. A Codex session run on a project under Oro management has `using-skills` enforcement firing on first tool call.
3. After `Edit`/`apply_patch` on a Codex session, formatter runs.
4. After `Read`/`fs/readFile` returning untrusted content, prompt-injection guard fires.
5. Codex `Stop` event runs the same stop-checklist that fires on Claude.
6. Project `AGENTS.md` is generated alongside `.claude/CLAUDE.md` on `oro init`.
7. The hook scripts (`enforce_skills.py` etc.) are NOT duplicated — same files referenced from both Claude `~/.claude/settings.json` and Codex `~/.codex/plugins/oro/hooks.json`.
8. `oro agent-assets sync --runtime claude` continues to work as today (no regression for Claude users).

## Open Questions (Stage 2 Consultation)

1. **PreCompact gap (REOPENED — Stage 3 found prior lock was wrong):** The earlier "option b — UserPromptSubmit + token threshold" lock was based on an unverified premise. Empirical schema dump of the Codex Rust binary confirms the `UserPromptSubmit` hook input has fields `{cwd, hook_event_name, model, permission_mode, prompt, session_id, transcript_path, turn_id}` and **no token-usage field**. There is no token-count, no context-pct, no usage data exposed to hooks.

   Revised options:
   - (a) **Drop PreCompact-equivalent for Codex.** Codex's server-side compaction is opaque; Oro can't predict or precede it. Workers lose pre-compact handoff capture. **Implication for Case A (dispatcher-spawned Codex workers)**: dispatcher's existing `handleHeartbeat → triggerCheckpoint` already fires on its own context-pct heuristic from the worker process, NOT from the hook payload. Codex workers running through the runtime adapter still get checkpoint-driven handoff via the dispatcher's existing pct-tracking — independent of the missing PreCompact event. The hook itself is redundant for Case A.
   - (b) Parse `transcript_path` (rollout JSONL) on every UserPromptSubmit to count tokens client-side. Heavy, format may not be stable.
   - (c) Use `turn_id` as a lossy proxy. Trigger handoff after N turns regardless of size. Imprecise.

   **LOCKED — option (a) for Case A, with the redundancy reasoning above.** The dispatcher already triggers checkpoint-and-respawn from the worker side using its own heartbeat tracking (`pkg/dispatcher/dispatcher.go:1505`). Codex workers pass heartbeats through the runtime adapter (`pkg/agentruntime/codex/codex.go`); whatever context-pct signal Oro derives there feeds the existing trigger.

   What this spec needs to do: ensure the Codex runtime adapter emits heartbeats with a usable context-pct signal (read from rollout file size, transcript token estimate, or the `model_reasoning_effort` plus turn count — all approximate but available). That's a runtime-adapter change, NOT a hook port.

   Out of scope for this epic: Case B interactive-Codex pre-compact handover. Different design.

2. **Tool-name matcher translation**: Codex tools have different names. Two paths:
   - (a) Generate runtime-specific matchers from canonical event spec (more code, cleaner runtime)
   - (b) List both Claude AND Codex tool names in matcher (works on both, less elegant)
   - Recommendation: (a) — already going to have a runtime-aware generator anyway

3. **Plugin distribution (REOPENED — Stage 3 found path unverified):** Earlier lock on `~/.codex/plugins/oro/` was premature. Real Codex plugins live at `~/.codex/.tmp/plugins/plugins/<name>/.codex-plugin/plugin.json` under a curated marketplace tree (verified: `.git/`, `plugin.lock.json`, `.codex-plugin/` subdirectory present in inspected plugins). No string in the Codex binary suggests `~/.codex/plugins/` is a discovery root. Required research before locking:

   - **R1 (BLOCKING)** — Verify Codex plugin discovery roots empirically: which directories does `codex` scan at startup; does `config.toml` accept a user-specified plugin path; what does `hooks/list` JSON-RPC return for a hand-installed plugin in different candidate directories.
   - **R2** — Plugin manifest path is `.codex-plugin/plugin.json` (subdirectory), not `plugin.json` at the plugin root. Update spec accordingly.
   - **R3** — Plugin manifest schema requires `{name, version, description, author, license, interface}` fields. Generator must build a real manifest, not a stub.

   Provisional plan pending R1:
   - If a writable user-plugin directory IS discoverable → direct install there
   - If only marketplace-curated dirs are loaded → either (i) publish Oro to a marketplace, or (ii) inject into `~/.codex/.tmp/plugins/plugins/oro/` accepting that it may be clobbered by curation refresh, or (iii) use `config.toml` `[plugin]` entries if supported

   Bead breakdown adds R1 as a blocking research task before the plugin generator lands.

4. **Rules (LOCKED — option b1, both formats):** Oro becomes a rules contributor on both sides. The two runtimes use **different rule formats** that solve different problems:
   - Claude `~/.claude/rules/*.md` — Markdown prose guidance (e.g., `standards.md`, `beads.md`)
   - Codex `~/.codex/rules/default.rules` — `prefix_rule(pattern=[...], decision="allow|deny")` DSL for tool-call permissions (verified: user's existing file already has hand-edited `prefix_rule(pattern=["oro","dolt","repair"], decision="allow")` entries)

   Oro ships both, sourced per-runtime:
   - Source: `assets/rules/claude/*.md` and `assets/rules/codex/oro.rules` (per-runtime subdirs)
   - Claude generator copies markdown files to `~/.claude/rules/oro-*.md` (`oro-` prefix avoids collision with user-authored rules like `standards.md`)
   - Codex generator emits a single `~/.codex/rules/oro.rules` file with `prefix_rule(...)` entries for the canonical Oro command surface (`oro`, `bd`, `go test ./...`, `make stage-assets`, `gofmt`, `goimports`, `golangci-lint`, etc.)
   - Codex `prefix_rule` entries are auto-generated from a canonical Oro command list at install time — adding a new Oro command updates the allowlist automatically, replacing the user's manual maintenance

   Out of scope: editing the user's existing `~/.codex/rules/default.rules` (Oro writes its own `oro.rules` file alongside; never modifies user files).

5. **AGENTS.md source-of-truth**: Two paths:
   - (a) Generate from `assets/ORO_AGENT.md` (per parent spec)
   - (b) Direct copy of `assets/AGENTS.md` (separate file)
   - Recommendation: (a) — single source-of-truth aligns with parent spec

## Recommended Bead Breakdown

Epic: `codex-harness-parity` (sibling of `agent-runtime-agnostic` and `agent-runtime-config`).

**Scope: Case A only — dispatcher-spawned Codex workers.** Interactive Codex sessions (Case B) are deferred.

Order matters: research first (empirical unknowns from Stage 3 review), prerequisites second (parent spec interface), abstraction third, then per-runtime generators, then hook ports, then rules + AGENTS.md, then tests.

### Research (BLOCKING — must land before any generator code)

1. `research(codex-plugin-discovery): empirically verify which directories Codex scans for plugins on startup; document the writable user-plugin install path; check whether config.toml [plugin] entries provide an alternative; spike test by hand-installing a stub plugin in candidate locations and calling hooks/list JSON-RPC to confirm discovery`
2. `research(codex-tool-names): run a representative Codex session and capture the exact tool_name strings emitted in PreToolUse and PostToolUse hook input; produce a Claude→Codex tool-name mapping table (Read→?, Edit→?, Write→?, Bash→?, WebFetch→?)`
3. `research(codex-hook-schemas): document the full hook input JSON shape for each event Oro will emit (SessionStart, PreToolUse, PostToolUse, Stop, UserPromptSubmit) — field names, types, optional vs required. Specifically required for PreCompact-replacement work: confirm whether PostToolUse exposes any token-usage, context-pct, turn-token-count, or rollout-cursor field that a hook script can use to derive context_pct. If no field exists on any event, document the gap and the spec must adopt the rollout-file-polling fallback in bead 14.`

### Prerequisites — absorbed into this epic (Stage 3 round 2 finding)

The parent runtime-agnostic spec describes a `Runtime` interface and `InstructionLayout` struct (section 4 of `2026-04-22-agent-runtime-agnostic-design.md`). That work is NOT in any of the parent epic's open beads (verified — `oro-dv61` children do not include a Runtime-interface bead). To avoid an unbuildable cross-epic dependency, this work is absorbed into this epic:

4. `feat(runtime-interface): add Runtime interface and InstructionLayout struct to pkg/agentruntime/runtime.go per parent spec section 4. Methods: ID() RuntimeID, DefaultTierModel(role string, tier Tier) string, StreamFormat() StreamFormat, InstructionLayout() InstructionLayout, SupportsHooks() bool, SupportsProjectSkillInstall() bool. Existing ReadRuntime() preserved as helper. Test: pkg/agentruntime/runtime_test.go:TestRuntimeInterfaceImplemented for both Claude and Codex adapters | Cmd: go test ./pkg/agentruntime/... -count=1 | Assert: both adapters satisfy the interface; cross-epic dependency from agent-runtime-config epic (oro-6myx beads using pkg/agentmodel) does NOT add a transitive dependency on this bead — agentmodel imports protocol+config only`

### Durable abstraction

5. `feat(agentassets): pkg/agentassets package — runtime-neutral HookSpec data structure, generator interface; consumed by claude.go and codex.go generators; Codex-specific schema fields (no Claude-only timeout/statusMessage) explicitly stripped in the codex generator`
6. `feat(agentassets-claude): Claude generator emits ~/.claude/settings.json hooks section from HookSpec; refactor of existing globalHooks() in cmd_global_oro_approach.go:262; no behavior change for Claude users`
7. `feat(agentassets-codex): Codex generator emits the discovered plugin layout (path resolved by R1) with .codex-plugin/plugin.json (correct manifest path per R2) containing required schema fields (R3) plus hooks.json from HookSpec`
8. `refactor(project-hooks): cmd_init.go:880 buildHookConfig (project-level Claude hooks: oro-search-hook, enforce_worktree, no_cd_guard, etc.) refactored into the same agentassets HookSpec abstraction; emits both runtimes`

### Hook ports (depends on R2 tool-name mapping)

9. `feat(matchers): runtime-aware tool-name matcher generation in agentassets — Claude tool names map to Codex tool names per the R2 mapping table; matchers emitted per-runtime`
10. `feat(search-hook): oro-search-hook adds Codex hook input/output schema support; runtime selected by inspecting stdin shape (no build flag); same Go binary serves both runtimes; Codex permission decision keys verified via R3`
11. `refactor(per-hook-schema-audit): for each of the 8 hooks (auto-format.sh, prompt_injection_guard.py, context_pruner.py, stop-checklist.sh, enforce_skills.py, session_start_global.py, oro-search-hook, plus the Codex compact-threshold investigation), document Claude input fields used + Codex equivalents per R3; add test fixtures with real-shape JSON for both runtimes`
12. `feat(stop-codex): port stop-checklist.sh to Codex Stop event with verified Codex hook input shape; UserPromptSubmit hooks must always emit {continue:true, ...} — never block the user prompt`
13. `feat(autoformat-codex): port auto-format.sh to Codex PostToolUse with Codex tool-name matchers (apply_patch or whatever R2 returns)`
14. `feat(codex-context-pct): drop PreCompact hook for Codex (no event exists). Replace by giving the existing worker heartbeat path a context-pct source for Codex sessions. Heartbeats are emitted in pkg/worker/worker.go:1116 trySendHeartbeat, NOT in pkg/agentruntime/codex/codex.go. Today the heartbeat reads context_pct from <worktree>/.oro/context_pct (file written by Claude's PostToolUse hook context_pct_writer.py registered at cmd/oro/cmd_init.go:917) and from stream-parsed lines via pkg/worker/context_pct.go:43 ParseCodexContextPct. Both are inert for Codex today. Two viable paths — choose based on R3 outcome:

(a) HOOK PATH (preferred if R3 confirms a Codex hook event exposes token-usage): install context_pct_writer.py-equivalent in the Codex plugin's PostToolUse generator output (bead 7); confirm pkg/worker/worker.go:1131 reads the resulting <worktree>/.oro/context_pct file when the worker is spawned with the Codex runtime adapter; no stream-parsing change needed.

(b) ROLLOUT-POLLING PATH (fallback if R3 confirms no token-usage field exists on any Codex hook event): add pkg/worker/worker.go logic that, when StreamFormat==LineText AND runtime==codex, polls the Codex rollout JSONL file (path from session_id) at heartbeat cadence and derives context_pct from byte-size or token estimate; ParseCodexContextPct is removed if no stdout line carries context_pct.

Test: pkg/worker/worker_test.go:TestCodexContextPctReachesHeartbeat | Cmd: go test ./pkg/worker/... -run TestCodexContextPctReachesHeartbeat -count=1 | Assert: a Codex worker simulation populates context_pct via the chosen path; trySendHeartbeat sends ContextPct > 0; dispatcher.go:1530 checkpoint trigger fires when threshold crossed.

Read: pkg/worker/worker.go:1116-1140 (heartbeat), pkg/worker/context_pct.go (parser), pkg/dispatcher/dispatcher.go:1530 (trigger), assets/hooks/context_pct_writer.py (existing Claude hook), pkg/agentruntime/codex/codex.go (runtime adapter, where rollout file path may need to be exposed for path b).`

### Plugin install + CLI

15. `feat(codex-plugin-install): oro init / oro agent-assets sync --runtime codex writes plugin to verified location from R1; manifest path .codex-plugin/plugin.json; required schema fields populated; idempotent; never modifies user-customized files (mirrors cmd_global_oro_approach.go stale-removal pattern with allow-set)`
16. `feat(agent-assets-cli): oro agent-assets sync --runtime claude|codex|all; replaces oro global-skills as canonical sync command; deprecates oro global-skills as alias`

### Rules + instructions

17. `feat(rules-claude): assets/rules/claude/oro-*.md → ~/.claude/rules/ via Claude generator (oro- prefix avoids collision with user-authored rules)`
18. `feat(rules-codex): assets/rules/codex/oro.rules generated from canonical Oro command list → ~/.codex/rules/oro.rules via Codex generator; never modifies user's default.rules`
19. `feat(agents-md): assets/ORO_AGENT.md is single source; oro init emits both .claude/CLAUDE.md and AGENTS.md in project root; regeneration policy: skip if file exists with matching content; warn if user-edited divergence; document that ORO_AGENT.md is the only file the user should edit`

### Tests

20. `test(parity-search-hook): integration test runs oro-search-hook through Claude hook input shape AND Codex hook input shape from R3 fixtures; both return correct AST summary or fail-open allow`
21. `test(parity-per-hook): per-hook integration tests using R3 fixtures — enforce_skills, prompt_injection_guard, auto-format, stop-checklist, context_pruner, session_start_global all run on both runtimes' input shapes`
22. `test(parity-checkpoint): dispatcher-spawned Codex worker triggers checkpoint-and-respawn via runtime adapter heartbeat (verify the Codex PreCompact-noop path); no hook involvement`
23. `test(parity-end-to-end): launch oro start with tiers.balanced.runtime=codex; assert hooks/list returns Oro hooks; assert each hook fires when its trigger event occurs; assert AGENTS.md present in repo; assert prefix_rule entries from oro.rules are honored by Codex`
24. `docs(parity): document Codex install requirements, plugin discovery path (per R1), .codex-plugin/plugin.json layout, AGENTS.md generation, prefix_rule allowlist scope; explicitly note Case B (interactive Codex) deferred to future spec`

## Recommendation

Land in this order:
1. **Research beads R1, R2, R3** — empirical verification of Codex plugin discovery, tool-name strings, hook input schemas. Nothing else can ship without these.
2. **Parent spec Runtime interface** lands in `agent-runtime-agnostic` epic (prerequisite — tracked there, not here).
3. `pkg/agentassets` runtime-neutral HookSpec abstraction (no behavior change for Claude).
4. Claude generator (refactor of existing `globalHooks` and project-level `buildHookConfig` into the new spec — Claude behavior unchanged).
5. Codex generator + plugin install at the path discovered by R1.
6. `oro-search-hook` Codex schema support (highest user-priority hook).
7. Per-hook schema audit + remaining hook ports (enforce_skills, prompt_injection_guard, auto-format, context_pruner, session_start, stop-checklist).
8. PreCompact-equivalent: NO hook port; runtime adapter heartbeat carries derived context-pct, dispatcher's existing checkpoint trigger fires for Codex workers identically to Claude workers.
9. Rules subsystem (Claude markdown + Codex prefix_rule DSL).
10. `AGENTS.md` generation from `ORO_AGENT.md`.
11. Integration tests on both runtimes.

This sequencing:
- Keeps Claude behavior identical at every step (no regression).
- Validates every empirical assumption before code lands on it.
- Targets Case A (dispatcher-spawned Codex workers) only — the case where Oro's existing handoff machinery applies as-is.
- Defers Case B (interactive Codex) to a future spec.
- The user's #1 stated pain (search hook) lands at step 6, after the abstraction is in place.
