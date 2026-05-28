# Plan: Refresh README.md to match current Oro

**Date:** 2026-05-28
**Type:** Docs plan (no code changes)
**Status:** Approved, not yet executed
**Review:** codex adversarial challenge (1 run, 9 findings — 6 folded in, 3 → follow-up beads)

## Context

**Yes, the README needs updating.** It was last meaningfully touched at `cfa28df5`
(before the May 26 memory-retirement work) and now describes subsystems that no
longer exist. The May 26 commits (`d9c7cd63` retire legacy memory package,
`5c108162` remove retirement wait window, `543309cf` worker-owned extract
spawner, `24ef087f` remove stored handoff artifacts) removed the FTS5 memory
layer and the file-based handoff system. `README.md` still documents both as
live features, so a new user following it would hit commands that error out
(`oro remember` → *"legacy memory has been retired; use cards instead"*,
`cmd/oro/store.go:13`) and read architecture that contradicts reality.

The chosen scope is a **full refresh**: correct the stale subsystems, document the
cards knowledge layer that replaced memory, sync the entire CLI Reference to the
current command set, and fix the Project Structure tree.

This is a documentation-only change. No code changes, no tests to write (docs).

## Source of truth (verified during exploration)

- **Memory is retired.** `pkg/memory/` no longer exists (`pkg/` listing confirms).
  `cmd/oro/store.go` is now a `retiredMemoryStore` whose every method returns
  `errLegacyMemoryRetired`. The commands `remember`, `recall`, `forget`,
  `memories` are still *registered* (`cmd/oro/root.go:50-54`) but error at runtime.
- **Cards replaced it.** `pkg/cards/` is the new knowledge layer. The `oro cards`
  group (`cmd/oro/cmd_cards.go`) is currently **migration/maintenance only**:
  `import-from-memory`, `check-drift`, `memory-retirement-check`. It is NOT a
  user-facing CRUD surface yet — do not invent `oro cards list/add/search`.
- **Handoffs are renders, not files.** `ORO_AGENT.md:6` is authoritative:
  *"handoffs are renders, not stored artifacts."* `oro handoff --since 4h`
  generates on-demand output; `docs/handoffs/` was deleted.
- **Why memory/dreaming were retired** (`docs/plans/2026-04-28-oro-harness-architecture-spec.md`
  §2.3, §3): `pkg/memory` was flat FTS5 key-value retrieval with no scoring,
  lineage, decay, typing, or progressive disclosure. Replaced by `pkg/cards`
  (typed, scored, lineage, decay, retirement). Dreaming was the LLM consolidation
  agent over the flat memory table; with memory gone its input is gone, and the
  EVOLVE pipeline stage / card promotion (§92) is the structured replacement.
- **Full current top-level command set** (`cmd/oro/root.go:33-80`):
  init, setup, start, attach, shell, dispatcher, stop, status, health, ops,
  recovery, monitor, throughput, dashboard, directive, remember(retired),
  recall(retired), forget(retired), worker, memories(retired), logs, events,
  index, cleanup, help, work, task, global-oro-approach/global-skills, doctor,
  uninstall, models, outline, impact, edit, cards, doctrine, harness, current,
  handoff, resume, review-patterns.

## Changes to README.md

### 1. Replace the "Memory System" architecture section (lines 228–240)
Retitle to **"Knowledge System"**. Rewrite the three-layer table:
- **Task annotations** — keep (still accurate: `oro task show <id>`).
- **Handoffs** — change storage from "YAML files in worktree" to "On-demand
  render (`oro handoff`)"; access becomes "Rendered from live Oro state".
- **Project memory** row → replace with **Knowledge cards** | `pkg/cards`
  (SQLite) | durable rules/patterns/decisions/facts | surfaced into worker
  prompts (`pkg/worker/prompt.go:97-98` — Cards section "replaces Memory").
- Delete the `[MEMORY]` marker / dispatcher-extraction paragraph and the
  **Dreaming** paragraph (line 240). **Rationale (corrected after codex review):**
  do NOT say this code was removed. The dreaming machinery still exists and runs
  — `DreamInterval: 10` (`cmd/oro/cmd_start.go:1010`), dispatcher dream triggers
  (`pkg/dispatcher/dispatcher.go:2692-2719, 3039-3040`), `OpsDream` routed to
  background (`pkg/ops/ops.go:64,80-81`). But `newDispatcherMemoryServices`
  returns `{}` (`cmd/oro/store.go:48-50`), so `d.memories` is nil
  (`dispatcher.go:207`) and extraction is nil (`newWorkerMemoryExtractSpawner`
  returns nil). The features therefore produce **nothing observable**. The README
  is user-facing: remove the dead feature *claims* (extraction + memory-reading
  dreaming synthesize nothing), and replace with the cards knowledge layer. Do
  not assert code deletion. The dead wiring is logged as a follow-up bead below.

### 2. Fix narrative prose that asserts the memory loop
- **Philosophy** (line 58): "each with access to cross-session memory" → cards/
  durable knowledge phrasing, or soften to handoff continuity.
- **Philosophy** (line 62) — *missed in first draft, caught by codex*: the whole
  "Memory persists across sessions. Workers emit learnings... FTS5-backed memory
  store" paragraph contradicts the retired store (`cmd/oro/store.go:13,52-79`).
  Rewrite around cards as the durable knowledge layer.
- **Principle 1 "Less Context"** (line 68) — *missed in first draft, caught by
  codex*: "injects only the memories that match" is obsolete; the worker prompt
  injects cards now (`pkg/worker/prompt.go:97-98`). Reword to cards.
- **Principle 2 "Compound Learnings"** (lines 70–72): remove "FTS5"/"memory
  consolidation deduplicates and scores" specifics; reframe around knowledge
  cards. Keep the compounding-knowledge thesis (aspirational is fine; mechanism
  claims must be true).
- **Why "Oro"?** (line 49): "Every memory is a vein" — optional softening; low
  priority, keep if it reads as metaphor not feature claim.

### 3. CLI Reference — sync to the real command set (lines 352–447)
- **Remove the entire "### Memory" table** (lines 392–408): `remember`,
  `recall`, `forget`, `memories list`, `memories consolidate`. They error now.
  (Optional: a one-line note that legacy memory commands are retired in favor of
  cards, so users who saw old docs aren't confused.)
- **Add a "### Knowledge & Tasks" (or split) section** documenting the commands
  that exist but are undocumented:
  - `oro task` — native task CLI (list/show/etc.)
  - `oro current` — inspect live task queue / active work
  - `oro handoff` — render session context (`--since`)
  - `oro resume <task-id>` — continue a tracked task
  - `oro cards` — knowledge card maintenance (import-from-memory, check-drift,
    memory-retirement-check)
  - `oro edit`, `oro doctrine`, `oro events`, `oro recovery`, `oro models`,
    `oro outline`, `oro impact`, `oro attach`, `oro shell`
  Group sensibly (Lifecycle / Monitoring / Tasks & Handoffs / Knowledge /
  Code intel / Maintenance / Internal). Don't enumerate every internal flag —
  match the existing table density.
- **Basic Operations** (lines 342–346): remove the `oro remember` /
  `oro recall` examples; replace with a still-valid example (e.g. `oro current`
  or `oro handoff --since 4h`).

### 4. Handoffs concept section (lines 478–480)
Rewrite: a worker nearing context limit triggers a fresh worker that picks up
from live Oro state via a rendered handoff (`oro handoff`), not a written YAML
file. Keep the "ralph loop / no task limited by one context window" framing.
Also update the Architecture "Context exhaustion" note (line 226: "writes a
handoff file") and Philosophy line 58 ("writes a handoff") for consistency.

### 5. Project Structure tree (lines 503–535)
- Remove `│   ├── memory/  # FTS5 memory store` (line 512).
- Add `│   ├── cards/  # Knowledge cards — durable rules/patterns/decisions`.
- Add `│   ├── beadstore/  # Task/bead storage (SQLite source of truth)`.
- *Codex caught the tree is broadly stale, not just 2 lines.* `ls pkg/` shows
  these are also absent: `agentassets`, `agentmodel`, `agentruntime`,
  `codestruct`, `config`, `dbutil`, `edit`, `factoryhealth`, `lint`,
  `modelartifacts`, `processenv`, `web`. Add the meaningful ones (at minimum
  `agentruntime`, `factoryhealth`, `beadstore`, `cards`, `config`, `web`) and add
  a one-line "+ supporting packages" note rather than trying to mirror every
  directory — keep it representative but no longer obviously wrong.

### 6. Ops Agents section (line 484)
Remove the "memory dreaming (cross-session synthesis)" description as a working
feature — it reads an empty store and synthesizes nothing. *Note (codex):*
`OpsDream` is still a registered ops type routed to background, so don't phrase
this as "the dreaming code was deleted"; just stop presenting cross-session
memory synthesis as a live capability. Keep the other ops-agent roles (review,
merge resolution, diagnosis, acceptance-criteria writing) which are real.

### 7. Claude Runtime Compatibility tier table (lines 543–550)
`background` tier "Typical use" currently says "Memory extraction, dreaming, ops
subtasks" (line 548). "Memory extraction" is dead (`newWorkerMemoryExtractSpawner`
returns nil). Dreaming technically still routes to background but produces nothing.
Replace the cell with real current background work (e.g. "Ops subtasks,
lightweight background jobs") — drop the memory-extraction claim.

### 8. Table of Contents (lines 17–34)
Update anchors to match: rename "Memory System" → "Knowledge System", rename/
restructure the "Memory" CLI subsection entry to the new grouping.

### 9. Minor: duplicate reference (lines 563 & 565)
"Teresa Torres - Context Rot" is listed twice with different descriptions.
Keep one (or retitle the second to its actual distinct source if intended).

## Out of scope — but file as follow-up beads (surfaced by codex review)
These are real code/help-text staleness bugs, not README content. This docs-only
change must NOT touch them, but they should be filed so they aren't lost:
1. **`oro cards show <id>` does not exist** — `pkg/worker/prompt.go:73` tells every
   worker to run it, but `cmd/oro/cmd_cards.go:12-14` only registers
   `import-from-memory`, `check-drift`, `memory-retirement-check`. Workers are
   instructed to run a nonexistent command. (P2 bug — either add `cards show` or
   fix the prompt text.)
2. **`oro help` groups retired memory commands as live** (`cmd/oro/cmd_help.go:30-36`)
   — user-visible help still presents `remember`/`recall`/`memories` as a working
   "Memory" group. Update help grouping to reflect retirement + cards.
3. **Dead dreaming/extraction wiring** — `DreamInterval: 10`
   (`cmd/oro/cmd_start.go:1010`), dispatcher dream triggers, and `OpsDream`
   (`pkg/ops/ops.go:64`) still run against a nil memory store. Either remove the
   dead wiring or repoint dreaming at cards. Tracks with the memory retirement.
4. **`root.go:16`** Long string still says "memory interface" — minor copy fix.

## Out of scope (no follow-up needed)
- No code changes here. The retired `remember/recall/forget/memories` commands
  stay registered-but-erroring; this plan only stops documenting them.
- README "Internal" section can stay representative (it lists `worker`; commands
  like `test:context-safety`, `harness`, `ops` are internal and need not all be
  enumerated). Note: the global-assets command's canonical name is `agent-assets`;
  `global-skills`/`global-oro-approach` are aliases — don't document the aliases
  as the primary name if that group is mentioned.

## Verification
1. `rg -n "remember|recall|FTS5|dreaming|YAML handoff|pkg/memory|memory/" README.md`
   → should return no stale feature claims (only intentional "retired" notes, if kept).
2. Cross-check every command in the rewritten CLI Reference against
   `cmd/oro/root.go:33-80` — each documented command must be registered, and
   no retired command (remember/recall/forget/memories) should appear as usable.
3. Confirm Project Structure tree entries exist: `pkg/cards`, `pkg/beadstore`
   present; `pkg/memory` absent (matches real `pkg/` listing).
4. `markdownlint README.md` (part of the quality gate) passes — no broken TOC
   anchors or table formatting.
5. Read the rendered README top-to-bottom once for narrative consistency
   (handoffs described identically in Philosophy, Architecture, and Key Concepts).

## Landing
Single docs commit: `docs(readme): sync with memory retirement and cards`.
Run `make lint`/markdownlint, commit, push (per project finish protocol).
File the 4 follow-up beads from "Out of scope — but file as follow-up beads".
