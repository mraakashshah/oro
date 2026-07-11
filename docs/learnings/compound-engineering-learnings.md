# Compound Engineering Plugin Learnings

> Distilled from `EveryInc/compound-engineering-plugin` at its current tip (plugin v3.19.0).
> The plugin was re-architected since the earlier snapshot: it is now **skills-only** (no
> standalone agent definitions) and ships as a **multi-host plugin** with a Bun/TypeScript
> converter CLI. Counts and structure below reflect the current repo.

## Core Philosophy: Compounding Engineering

Each unit of engineering work should make subsequent units easier, not harder.

**Traditional:** every feature adds complexity; every fix leaves local knowledge someone must rediscover. The codebase grows, context gets harder to hold, the next change is slower.

**Compound:** invert it. ~80% of the effort is planning and review, ~20% is execution. A good brainstorm makes the plan sharper; a good plan makes execution smaller; a good review catches the *pattern*, not just the bug; a good compound note means the next agent doesn't relearn the lesson.

The point is leverage, not ceremony.

## The Compound Loop

The core loop is six skills, each handing a durable artifact to the next, then repeated with better context:

```
brainstorm → plan → work → simplify → review → compound → (repeat, smarter)
```

| Skill | Role in the loop |
|-------|------------------|
| `/ce-brainstorm` | Interactive Q&A that writes a **requirements-only** unified plan (the WHAT) |
| `/ce-plan` | Enriches that same artifact into an **implementation-ready** plan (the HOW) |
| `/ce-work` | Executes implementation-ready plans with worktrees and task tracking |
| `/ce-simplify-code` | Tightens freshly written code for clarity/reuse before review |
| `/ce-code-review` | Multi-persona review against the plan before merging |
| `/ce-compound` | Captures the learning into `docs/solutions/` so the next loop starts smarter |

The return arrow is the whole point: `/ce-compound` writes learnings that the next `/ce-brainstorm` and `/ce-plan` read as grounding.

### Unified plan artifact contract

`ce-brainstorm` and `ce-plan` now write to **one** artifact in `docs/plans/`, keyed by a readiness field:

- `ce-brainstorm` emits `artifact_readiness: requirements-only` (WHAT).
- `ce-plan` enriches that same file in place to `artifact_readiness: implementation-ready` (HOW) rather than creating a second doc.

Legacy `docs/brainstorms/*-requirements.*` files remain readable inputs and are not migrated. Both skills support an `output:html` mode (see HTML output invariants below).

### Autonomous mode

- `/lfg` runs the whole loop hands-off from a brainstorm: plan → work → simplify → code review + apply fixes → browser tests → commit → push → open PR → watch CI and repair until green. Start it after `/ce-brainstorm` so it plans against real requirements.
- `/ce-dogfood` does hands-off, diff-scoped browser QA of the active branch with autonomous fixes.

## Skills-Only Architecture (29 skills, 0 standalone agents)

The plugin ships **29 skills** and **0 standalone agent definitions**. This is the biggest structural change: the old `agents/` tree (review/research/design/workflow definitions) is gone. Specialist review, research, and workflow behavior now lives **inside the owning skill** as skill-local prompt assets, and skills dispatch *generic* subagents seeded with that prompt material.

### Skills grouped by role

**Core loop (6):** `ce-brainstorm`, `ce-plan`, `ce-work`, `ce-simplify-code`, `ce-code-review`, `ce-compound`.

**Upstream / product (5):** `ce-strategy` (creates & maintains `STRATEGY.md`, read as grounding by ideate/brainstorm/plan), `ce-ideate` (generate + critically rank grounded ideas before the loop), `ce-pov` (decisive project-grounded verdict on an external tech/library/pattern), `ce-product-pulse` (time-windowed report on what users actually experienced → `docs/pulse-reports/`).

**Knowledge (3):** `ce-compound`, `ce-compound-refresh` (refresh stale/drifting learnings), `ce-explain` (dense personal explainer for a concept/diff/idea/week-of-work, with an optional predict-then-reveal check-in).

**Debug / quality (5):** `ce-debug` (reproduce → trace root cause → fix → polish), `ce-optimize` (iterative optimization loops), `ce-polish` (start a dev server, iterate on UX polish), `ce-doc-review` (review requirements/plan docs), `ce-simplify-code`.

**Ship (5):** `ce-commit`, `ce-commit-push-pr` (commit + push + open a PR that *teaches* any new concept it introduces), `ce-worktree` (ensure work runs in an isolated worktree), `ce-resolve-pr-feedback`, `ce-promote` (draft announcement copy).

**Feedback / testing (5):** `ce-sweep` (sweep feedback sources, track item lifecycle, emit an `/lfg`-ready plan), `ce-riffrec-feedback-analysis`, `ce-test-browser`, `ce-test-xcode` (iOS on simulator), `ce-dogfood`.

**Meta (3):** `ce-setup` (diagnose optional tool capabilities + project config), `ce-proof` (Proof documents), `lfg` (full autonomous pipeline).

*(Skills serve several roles; totals overlap. The authoritative inventory is the README table and `docs/skills/README.md`; each skill's runtime spec is `skills/<skill>/SKILL.md`.)*

### Skill-local specialist prompt assets

Because there is no `agents/` tree, specialist personas ship *inside* the skill that uses them, under `references/personas/` or `references/agents/`, with **no YAML frontmatter** (model/tool/dispatch policy lives in the calling `SKILL.md`, not the asset). Names are descriptive and unprefixed (`learnings-researcher.md`, not `ce-learnings-researcher.md`) because they are internal, not externally exposed component names. The repo carries dozens of these across skills.

Example: `ce-code-review` dispatches a dynamically selected panel of ~16 reviewer personas as parallel subagents (correctness, security, performance, maintainability, testing, reliability, adversarial, agent-native, api-contract, data-migration, project-standards, previous-comments, deployment-verification, frontend-races, swift-ios, …), each returning structured JSON that the skill merges and de-duplicates.

## Multi-Host Plugin + Converter CLI

The repo root **is** the plugin. Rather than one Claude-only bundle, it ships **native plugin manifests for many hosts** plus a Bun/TypeScript **converter CLI** for the rest.

### Native manifests (install directly from the repo)

The root carries per-host manifest directories: `.claude-plugin/`, `.codex-plugin/`, `.cursor-plugin/`, `.devin-plugin/`, `.grok-plugin/`, `.kimi-plugin/`, `.cline/`, `.opencode/`, `.pi/`, `.agy/` (Antigravity). Documented install paths cover Claude Code, Cursor, Codex (App + CLI), Kimi Code, **Cline** (on-demand `SKILL.md` dirs via `.cline/scripts/install-skills.sh`), Grok Build CLI, Devin CLI, GitHub Copilot, Factory Droid, Qwen Code, OpenCode, Pi, and Antigravity (`agy`). Each install is self-contained — no separate custom-agent install step, because specialists are skill-local.

### Converter CLI (`src/`)

For hosts that need conversion, `src/` is a Bun/TypeScript CLI (`convert` / `install --to <provider> --also`) that parses the Claude plugin and re-emits it per target. Four converter/writer targets are implemented in `src/targets/index.ts`: **opencode, codex, pi, antigravity** (a `kiro` writer also exists). Each target has an explicit **Converter** (Claude → target in-memory Bundle, mapping tools/permissions/hooks/model-names) and a **Writer** (emits the Bundle to that target's expected paths + merge semantics). Bun is only needed for repo development and converter maintenance — normal installs use native manifests.

### Install manifest invariant (preserve user content)

Each Writer records an **install manifest** of exactly which paths it created, so later installs distinguish tool-owned content from user-managed content. The load-bearing rule: **a Writer never claims a path it did not write.** A path the user has replaced (a symlink into a personal fork, a hand-authored dir) is excluded from the manifest and preserved on reinstall, and the ledger is self-healing (removing an override lets the next install resume tracking). Recent work extended this preservation to the codex and opencode writers.

## Cross-Host Authoring Constraints

Because the plugin is authored once and converted, skills follow strict portability rules:

- **Self-contained skill dirs.** A `SKILL.md` may only reference files within its own tree (`references/`, `assets/`, `scripts/`) via relative paths. No traversal into sibling skills, no absolute paths, no cached-install paths — the converter copies each skill dir as an isolated unit. If two skills need the same file, **duplicate it** (byte-for-byte, guarded by a parity test).
- **No unguarded platform variables.** `${CLAUDE_SKILL_DIR}`, `${CLAUDE_PLUGIN_ROOT}`, etc. are empty on non-Claude hosts. For executed shell, the house pattern is the **model-filled `SKILL_DIR` anchor**: the agent fills in the absolute skill dir it just read and sets it inline in the same Bash call (shell state doesn't persist between calls). This works on every host because it depends on no host env var. Read-time `references/*.md` pointers need no anchor.
- **ASCII identifiers, portable shell.** File/skill/command names are ASCII (converters/regex depend on it). Pre-resolution commands must be shell-portable so skills load under PowerShell too.
- **Context-reference over filename.** On the read path, skills say "the project's active instructions and conventions already in your context" instead of naming `AGENTS.md`/`CLAUDE.md`/`GEMINI.md` — those are auto-injected per host under different names, and naming them trips prompt-injection guards. A concrete filename is used only when *writing a convention back* or reading genuinely non-auto-loaded content.

## Skill-Authoring Discipline

Every line of skill prose must change agent behavior — apply the **deletion test**: if removing a line wouldn't change output, delete it. Generic exhortations ("be thorough", "world-class", "high quality") are no-ops. A line earns its place only if it states a falsifiable constraint (threshold/format/path/schema/ordering), counters a known default tendency (a negative constraint or guard), or supplies domain knowledge the agent lacks.

**Inline the trigger, not the content.** `SKILL.md` loads at session start; `references/` load on demand. Inline only load-bearing instructions (the action, the routing that invokes the next step, the instruction to load the reference). Do **not** inline a *summary* of a reference — a paraphrase both drifts from the source and suppresses the load (the agent judges it "has enough" and never opens the file). Extract a block to `references/` when it is **conditional** or **late-sequence** *and* is ~20%+ of the skill, replacing it with a 1–3 line stub naming the condition + a backtick path (never `@`, which inlines at load time).

## Model Tiers & Orchestration Patterns

- **Model tier** — a skill declares a semantic cost class per dispatched subagent (extraction = cheapest capable; generation = mid-tier; ceiling = the orchestrator's own model) referenced by *tier name* so model names never hardcode into skill content. Where a host can't pick models per agent, everything runs on the inherited model and cost control falls back to read budgets and output caps. Reasoning-heavy brainstorm/plan steps now dispatch to Fable on Claude Code.
- **Evidence dossier** — a cheap scout agent writes bulk verbatim quotes + source pointers to scratch storage; the orchestrator carries only a short gist and downstream agents read the full dossier themselves.
- **Confidence anchor** — review findings are gated/ranked by a discrete self-scored confidence on a fixed small scale (each level tied to a behavioral criterion), not a continuous score. Corroboration across personas promotes a finding by one level.
- **Autofix class** — every review finding is classified by how safely its fix applies: silent, confirm-first, human-only, or advisory.
- **Headless mode** — an explicit unattended opt-in that produces a written report and conservatively *defers* ambiguous decisions rather than guessing (used by pipeline callers like `/lfg`).

### Shared repo-grounding profile cache

Grounding skills (`ce-pov`, `ce-plan`, `ce-optimize`, `ce-ideate`, `ce-brainstorm`, `ce-code-review`, plus lighter consumers) reuse **one cached, question-agnostic project profile** (stack, deps, conventions, structure) instead of each re-deriving it. It's git-keyed at `/tmp/compound-engineering/repo-profile/<root-sha>/<head-sha>.json` (`get` → HIT / MISS derive-and-`put` / NO-CACHE derive-fresh). The cache is an optimization, never a correctness dependency — question-specific grounding always runs fresh, and `docs/solutions/` enumeration + subdirectory instruction files are always re-globbed (never cached) to avoid serving a just-written learning stale. Mechanism = three byte-duplicated assets per consumer (schema/protocol reference, `repo-profile-cache.py`, `repo-profiler.md` persona), guarded by a parity test.

## Knowledge Management

- **`docs/plans/`** — unified plan artifacts (requirements-only → implementation-ready via the readiness field). Living documents.
- **`docs/solutions/`** — **Learnings**: documented solutions to past problems (bug fixes, conventions, workflow patterns) with YAML frontmatter (`module`/`category`, `tags`, `problem_type`); creation date lives in the entry, not the filename. Categorized from the *end user's* perspective: `developer-experience/` (contributing to this repo), `integrations/` (plugin output on a target platform/OS), `workflow/`, `skill-design/`, `best-practices/`, `conventions/`. A **Pattern doc** generalizes several Learnings into a broader rule (higher leverage, higher risk when stale). `ce-compound` writes them; `ce-compound-refresh` keeps them from drifting, validating doc claims at write time.
- **`docs/skills/`** — one page per user-facing skill + a catalog `README.md`; kept in sync with the root README inventory and the skill-count assertion in `tests/release-metadata.test.ts`.
- **`docs/specs/`** — target platform format specs. **`CONCEPTS.md`** — shared domain glossary that accretes as `ce-compound` processes learnings.
- **`STRATEGY.md`** — an upstream product anchor maintained by `ce-strategy`, read as grounding by ideate/brainstorm/plan.

## HTML Output Invariants

`ce-brainstorm` and `ce-plan` support an `output:html` mode. Each carries `references/html-rendering.md` / `markdown-rendering.md` so the rendered artifact stays consistent — the plan/brainstorm content is authoritative and the HTML is a faithful render of it, not a divergent second copy.

## Skill Structure

```markdown
---
name: ce-<skill>
description: "What it does + when to use it (informs auto-invocation)."
argument-hint: "[optional args] [output:html]"
---

# Title
## When to Use
## Workflow (phased)
```

- Frontmatter is minimal: `name`, `description`, optional `argument-hint`. `mode:agent` / headless flags gate pipeline (report-only) behavior.
- Progressive disclosure: keep `SKILL.md` lean, push conditional/late-sequence substance into `references/`, scripts into `scripts/`, personas into `references/personas/`.
- A **beta skill** is a `-beta` copy trialed alongside the stable one with auto-invocation disabled; promoting it moves every caller in the same change and registers the retired name for stale-artifact cleanup.

## Directory Structure

```
compound-engineering-plugin/        (repo root == the plugin)
├── skills/                 # 29 skills; each self-contained
│   └── ce-<skill>/
│       ├── SKILL.md
│       ├── references/     # loaded on demand (incl. personas/, agents/)
│       ├── scripts/
│       └── assets/
├── src/                    # Bun/TS converter CLI (parsers, converters, target writers)
│   └── targets/            # opencode, codex, pi, antigravity (+ kiro)
├── .claude-plugin/         # Claude manifest + marketplace catalog
├── .codex-plugin/ .cursor-plugin/ .devin-plugin/ .grok-plugin/
├── .kimi-plugin/ .cline/ .opencode/ .pi/ .agy/   # native host manifests
├── tests/                  # converter/writer/CLI + release-metadata tests
├── docs/                   # skills/, solutions/, plans/, specs/, ...
├── AGENTS.md               # canonical repo instructions (CLAUDE.md is a shim)
└── CONCEPTS.md             # domain glossary
```

## Key Learnings

1. **Skills replaced agents.** There are no standalone agent components anymore; specialists are skill-local prompt assets with no frontmatter, dispatched to generic subagents. This is the defining architectural shift.
2. **Count accuracy is test-enforced.** Skill count (29) is asserted in `tests/release-metadata.test.ts` and must match the README inventory, `docs/skills/README.md`, and manifests. Adding/removing a skill touches all of them.
3. **Multi-host by construction.** One authored source, many install targets — half via native manifests, four via the converter CLI. Portability rules (self-contained dirs, `SKILL_DIR` anchor, context-reference over filename) exist to keep the single source installing cleanly everywhere.
4. **Never claim a path you didn't write.** The install-manifest invariant preserves user-managed content (symlinks, hand-authored dirs) across reinstalls and is self-healing.
5. **Releases are automated.** Don't hand-bump plugin/marketplace versions or hand-author `CHANGELOG.md`; conventional commit prefixes (scoped by intent, never scope `compound-engineering`) drive release-please. `bun run release:validate` guards consistency.
6. **Every skill line must change behavior.** The deletion test, "inline the trigger not the content," and reference extraction keep skills lean under progressive disclosure.
7. **Grounding is cached, correctness is not.** The question-agnostic repo profile is reused across grounding skills; question-specific grounding and `docs/solutions/` enumeration always run fresh so a stale profile can't change an output.
