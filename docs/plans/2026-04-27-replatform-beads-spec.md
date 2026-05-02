# Replatform Beads onto Oro-Native SQLite

**Author:** Aakash Shah
**Date:** 2026-04-27
**Status:** Approved — v20 (codex round 11 confirmed v19 fixed the §2.2/§8.4/§11.2/§14.5/§12.10-12.11/§16.3-16.11 contradictions but flagged 5 more in §9.4 (bd-on-PATH for shim), §5.2 ("strict additive / callers untouched"), §5.3 diagram (alias arrow), Appendix A (alias listing), §1.4 ("~10-method"). v20 sweeps each. **Codex's standing judgment across rounds 6/9/10/11: the architecture is sound; remaining cleanup is prose drift, not structural objection.**)
**Revision history:**
- **v2** (2026-04-27) addressed six review critiques: package placement, type signatures, shadow-mode design, interface location references, effort estimate honesty, and `BeadsDir` retention staging.
- **v3** (2026-04-27) addressed eight tigers + three elephants surfaced by the v2 premortem.
- **v4** (2026-04-27) addressed seven findings from the v3 adversarial review.
- **v5** (2026-04-27) addressed findings from the v4 adversarial review: WAL location, BeadDetail runtime fields, wiring pseudocode, migration concurrency lock, runbook, acceptance-test inventory.
- **v6** (2026-04-27) addressed three findings from the v5 adversarial review: lock.go ownership, MemoryFetcher callback, shadow-mode divergence semantics, plus bd-version pin and cron spec.
- **v7** (2026-04-27) addressed five findings from the v6 adversarial review: per-project dispatcher PID lock; `pkg/memory.Store.ForBead` as new Phase 1 deliverable; `shadowStartedAt` persistence; migration tool dispatcher-lock check; bd-version SemVer comparison.
- **v8** (2026-04-27) added v7 minor follow-ups (no-expiry shadow timestamp, mtime PID-recycle guard, override-flag audit). Claimed steady state.
- **v9** (2026-04-27) — v8 was sent to **codex challenge** (cross-model adversarial review) and surfaced 11 real findings, 4 production-breakers, all missed by 5 prior Claude-on-Claude iterations. The lesson: same-model review converges on document self-consistency but shares the author's priors about the codebase, so it cannot catch contract violations against the actual code. v9 fixes:
  - **#1 (consistency):** every "11-method" stale reference replaced with "13-method"; `ShadowStore.Create` signature corrected to positional.
  - **#2/#3 (shadow classifier):** drift rule now keys on `updated_at`, not `created_at`. A pre-shadow bead whose `updated_at` falls within the window is drift, not real.
  - **#4 (status vocabulary):** schema `CHECK` expanded to accept the full set the dispatcher actually writes (`open`, `in_progress`, `closed`, `blocked`, `ready`). Migration normalization documented.
  - **#5 (timestamp format):** triggers and `Update`/`Close` use `strftime('%Y-%m-%dT%H:%M:%fZ','now')` to emit RFC3339 — matching bd's format. Reconcile string `>` comparison now compares like-for-like.
  - **#6 (deletion parity):** count parity check uses `WHERE deleted=0`.
  - **#7 (bd-shim):** table extended with `bd stats`, `bd blocked`, bare `bd list`, `--acceptance-criteria` long-form, `--estimate` flag passthrough; `oro bead create` learns `--estimate`.
  - **#8 (MemoryFetcher signature):** callback redesigned to match `pkg/memory.ForPrompt`'s actual contract: takes tags + description + maxTokens, not bead id. Recursion through `Show()` avoided by having the dispatcher compose the memory call from the bead row before assembling `BeadDetail`.
  - **#9 (lock scope, again):** PID lock path is now `filepath.Dir(StateDBPath)/dispatcher.lock`, not `OroProjectDir/dispatcher.lock`. v7's "fix" was wrong — `OroProjectDir` (`<repo>/.oro`) and `StateDBPath` (`~/.oro/projects/<project>/state.db`) live in different directories. Two checkouts of the same repo share the DB but had different lock files. v9 fixes by colocating the lock with the DB.
  - **#10 (trigger coverage):** AU triggers added for `bead_deps`, `bead_tags`, `bead_labels`, `bead_notes` (previously only `bead_metadata` had one).
  - **#11 (phase ordering):** Phase 1 acceptance runs against a fresh schema-only state.db (the migration tool from Phase 3 is not required); Phase 3 acceptance runs the migration-tool-fed test.

**Lesson learned (recorded for future spec work):** Claude-on-Claude review is a self-consistency tool, not a correctness tool. Cross-model review (or, more practically, "force the reviewer to grep against actual source paths and quote line numbers") is what catches the contract-violation class of bug. The 5 Claude iterations were not wasted — they fixed real issues — but they hit a ceiling around shared priors that Codex broke through in one pass.

- **v10** (2026-04-27) — codex round 2 against v9 found 7 more, mostly proving that several v9 patches didn't go far enough. Each fix below cites the codex round-2 finding number it addresses:
  - **R2#1 (lock path over-serialization):** v9 used `filepath.Dir(StateDBPath) + "/dispatcher.lock"`, which collides for two different DBs in the same directory (`ORO_DB_PATH=/tmp/oro/a.db` vs `/tmp/oro/b.db`). v10 fix: lock path is **`StateDBPath + ".lock"`** — one lock per database file, not per directory.
  - **R2#2 (migration liveness check stale path):** §9.10 still referenced `<ProjectPaths.OroProjectDir>/dispatcher.lock` after v9 moved the lock. v10 fixes §9.10 to use `StateDBPath + ".lock"` to match.
  - **R2#3 (timestamp string comparison still unsound):** bd's existing `updated_at` values are RFC3339 *without* fractional seconds (e.g. `2026-04-27T15:04:03Z`); v9's strftime emits *with* fractional seconds (`2026-04-27T15:04:03.000Z`). Lexicographic `>` between these is wrong. **v10 fix:** reconcile and triggers wrap timestamps in `datetime(...)` SQLite function for comparison, which parses both forms; storage format remains strftime-RFC3339 going forward, but old-form bd values continue to compare correctly.
  - **R2#4 (import + triggers violate verbatim `updated_at`):** during migration, inserting child rows fires the parent-touch triggers, which overwrite `beads.updated_at` to import-time. The migration explicitly preserves bd's `updated_at`, but triggers wipe it before the migration commits. **v10 fix:** migration tool sets `PRAGMA defer_foreign_keys=ON; PRAGMA recursive_triggers=OFF;` and uses an explicit `INSERT OR REPLACE INTO beads(..., updated_at)` *as the final statement* for each bead, after child rows. SQLite triggers fire after the child insert with the wrong updated_at, but the final beads row write overrides them. Alternative — supported as the primary path — disable user-defined triggers via `DROP TRIGGER ... ; <import> ; CREATE TRIGGER ...` bracketing the import. Migration tool documents the chosen approach in its source.
  - **R2#5 (UPDATE triggers fire on no-op rewrites):** SQLite `AFTER UPDATE` fires even when the new value equals the old. v10 adds `WHEN <field>-comparison` guards to every AU trigger so a no-op `UPDATE bead_tags SET tag=tag` doesn't bump the parent's `updated_at`.
  - **R2#6 (bd-shim still incomplete):** v10 adds five more rows to the translation table — `bd list --json --limit 0 --all`, `bd list --json --status=closed --sort=closed --limit N`, `bd show --current --json`, `bd update <id> --claim`, `bd sync`.
  - **R2#7 (MemoryFetcher closure refers to non-existent field):** v9 said `d.memoryFetcher` and `d.memoryStore`. The actual `Dispatcher` field is `memories *memory.Store` (`pkg/dispatcher/dispatcher.go:424`). v10 corrects all references.
- **v11** (2026-04-27) — codex round 3 against v10 found 8 more drift-cleanup items. The architectural fixes from rounds 1-2 hold; round 3 is mostly spec-text staleness and edge cases:
  - **R3#1 (shadow classifier raw string compare):** v10 wrapped reconcile in `datetime()` but the shadow classifier (§8.6 `classify()`) still does raw `string >`. v11 fix: classifier parses both timestamps via `time.Parse(time.RFC3339Nano, ...)` (with fallback to `time.RFC3339`) and compares as `time.Time`. Documented in `classify()` doc-comment.
  - **R3#2 (Ready view lex-compares deferred_until):** the `beads_ready` view's `deferred_until <= strftime(...)` is a string compare, broken across RFC3339 variants (offset vs Z). v11 fix: wrap in `datetime()`: `datetime(b.deferred_until) <= datetime('now')`.
  - **R3#3 (symlinked DB paths split lock):** `StateDBPath + ".lock"` is per-string-path, not per-inode. Two paths via symlink to the same DB get different locks. v11 fix: lock path is computed by `filepath.EvalSymlinks(StateDBPath) + ".lock"` so symlinks resolve to the same canonical lock.
  - **R3#4 (`ProjectPaths.StateDBPath` does not exist):** the spec's text at multiple sites referenced `ProjectPaths.StateDBPath`, but the actual `StateDBPath` field is on the daemon-paths struct returned by `ResolveDaemonPaths()` (`cmd/oro/paths.go:65-70`), not on `ProjectPaths`. v11 fix: every spec reference now reads "the `StateDBPath` returned by `ResolveDaemonPaths()`," matching the source.
  - **R3#5 (MemoryFetcher leftover `ForBead` references):** §8.3 `Show()` sketch and a stale migration-mapping line still mentioned `pkg/memory.ForBead(id)`. v11 fix: replaced with the v9 closure pattern.
  - **R3#6 (AU trigger guards miss columns):** `bead_deps` AU watches `type` + `depends_on_id` but ignores `created_at` + `created_by`; `bead_notes` AU watches `content` + `author` but ignores `created_at`. If those fields are updated (e.g., correcting a bad import), parent `updated_at` doesn't advance. v11 fix: AU `WHEN` guards expanded to all non-PK columns on each child table.
  - **R3#7 (§9.7 prose stale):** §9.7 narrative still says "Six triggers total. Four child tables. INSERT + DELETE only." Appendix B has 15 triggers across 5 child tables (AI + AU + AD per table, with the AU guards from R3#6/v10). v11 fix: §9.7 rewritten to match Appendix B exactly.
  - **R3#8 (bd-shim still incomplete):** three more emitted forms: `bd list --json --limit 0` (`pkg/mg/data/source.go:112-125`), `bd list --json --status=closed --limit 0`, and a non-JSON `bd list --status=closed --sort=closed --limit=0` from a hook (`assets/hooks/session_start_extras.py:241-243`). v11 fix: three more rows added to the translation table.
- **v12** (2026-04-27) — codex round 4 against v11 found 4 inventory-completeness gaps. Architectural fixes from rounds 1-3 hold; round 4 is about surfaces the prior inventory missed:
  - **R4#1 (stealth bd path is `beads`, not `.beads/`):** §2.4 said the stealth migration source is `~/.oro/projects/s-<hash>/.beads/`. Actual stealth path is `~/.oro/projects/s-<hash>/beads` with no leading dot (`cmd/oro/paths.go:180`, `cmd/oro/cmd_bd.go:73`). v12 fix: §2.4 corrected.
  - **R4#2 (acceptance test contradicts shadow drift design):** §14.5 had `beads_ready count == bd ready count` as a Phase 4 check. But §9.4 step 3 (the v9 classifier) explicitly tolerates drift on beads updated during shadow. Strict count equality cannot pass under the read-only shadow design once the dispatcher does anything. v12 fix: replace count-equality with "zero real divergences (classifier-based) over 100+ Ready() calls."
  - **R4#3 (cmd_cleanup.go still has bd shell-outs not on the conversion list):** `cmd/oro/cmd_cleanup.go:456,474` calls `bd list` and `bd update` for the cleanup path; v11's inventory only mentioned removing the dolt-process cleanup. v12 fix: §11.2 modified-files table extends `cmd_cleanup.go`'s scope to "rewrite `cleanupBeads()` to use `beadstore.Store`."
  - **R4#4 (shipped skills emit bd commands not in inventory):** `assets/skills/work-bead/SKILL.md:21`, `assets/skills/beadcraft/SKILL.md:108,294` teach `bd update --status in_progress`, positional `bd create "<title>"`, and `bd dep remove` — none in the v11 shim translation table; the latter two are not in any oro-emitted prompt either. v12 fix: extend §11.4 hook list with these skill files; add `bd dep remove` and the positional-create form to the shim table.
- **v13** (2026-04-27) — codex round 5 against v12 returned "structurally close, not PASS" with 2 medium + 2 low inventory items. Architectural fixes from rounds 1-4 hold. v13 fixes:
  - **R5#1 (skill-asset inventory incomplete):** v12 named only 3 skill files explicitly; codex enumerated 9 with `bd` command-prefix patterns. v13 lists all 9 by file:line: `assets/skills/{executing-beads,context-checkpoint,beadcraft,adversarial-spec-review,beads,resume-handoff,work-bead,dispatching-parallel-agents,spec}/SKILL.md`. Plus a Phase 6 hard gate: `git grep -l 'bd[[:space:]]' assets/skills/` returns zero files.
  - **R5#2 (shim table still misses skill-emitted forms):** space-form `--status in_progress` (vs equals-form), `--reason "..."` space-form, `bd create "<title>" --type ...`, `bd create "title" -p <priority>` (short flag), and the `assets/skills/beads/SKILL.md` advanced subcommand teaching (`bd prime`, `bd mol`, `bd pour`, `bd wisp`). v13 fix: shim table extended with the additional flag-format normalizations; `assets/skills/beads/SKILL.md` is **deprecated and removed in Phase 6** — it's general bd documentation that becomes obsolete when oro switches to native beadstore. The advanced subcommands are not part of the oro-emitted surface and are not translated.
  - **R5#3 (cmd_mg.go missing from inventory):** `cmd/oro/cmd_mg.go:36-38,73-77,143-160` still checks `.beads/` and bd-on-PATH and loads via bd-backed `mg` functions. v13 fix: §11.2 modified-files table extends to include `cmd/oro/cmd_mg.go`.
  - **R5#4 (timestamp equality in parseTS-compare):** v11's classifier sketch said "parseTS-equal." Go `time.Time` equality with `==` is too strict (compares wall+monotonic). v13 fix: classifier doc-comment specifies `t1.Equal(t2)` for instant-equality, not `==`.

**Convergence (codex round 6, 2026-04-27):** PASS. Codex's exact judgment: *"I do not see a remaining architectural or contractual blocker that static text should solve before implementation starts. The core contracts are now named: 13-method store surface, shadow drift classifier, timestamp parsing, migration trigger hazards, lock path, mg/cmd cleanup stragglers, shim deny-default, and prompt/skill/hook cutover. […] convergence reached. Further rounds are now mostly inventory whack-a-mole; the remaining surface is better found by Phase 6 grep gates, shim-table extraction tests, prompt golden refreshes, and compiler/test failures."*

**Final iteration record:**

| Round | Findings | Class |
|---|---|---|
| Premortem v2→v3 | 8 tigers + 3 elephants | Risk taxonomy + concept drift |
| Claude v3→v4 | 7 (3 real + 4 misreadings rebutted) | Document consistency, scope |
| Claude v4→v5 | 4 real | Document consistency |
| Claude v5→v6 | 3 critical + 2 medium | Critical: lock ownership, MemoryFetcher signature, shadow semantics |
| Claude v6→v7 | 5 critical + minor | Critical: per-project lock, ForBead, shadowStartedAt persistence |
| Claude v7→PASS (v8) | 0 structural + 3 polish | (declared steady state — incorrectly) |
| **Codex round 1 (v8→v9)** | **11 real (4 production-breakers)** | **Cross-model: contract violations against actual code that Claude couldn't see** |
| Codex round 2 (v9→v10) | 7 real | Incomplete v9 patches |
| Codex round 3 (v10→v11) | 8 real | Drift + leftover staleness |
| Codex round 4 (v11→v12) | 4 real | Inventory completeness |
| Codex round 5 (v12→v13) | 4 real | More inventory |
| **Codex round 6 (v13)** | **PASS** | **Convergence** |

**The architecture-vs-inventory split** is visible in the trend: codex rounds 1–2 found contract violations the same-model loop missed (interface signature, status vocabulary, timestamp format, lock scope). Rounds 3–6 caught smaller drift and inventory gaps that any sufficiently-careful pass would surface eventually. Round 6 explicitly judged that the remaining surface — finding the last skill file, finding one more bd shell-out — is implementation-discovery work, not spec-fixable work.

- **v15** (2026-04-27) — **need-first pivot.** Through v14, the spec was ~70% bd-parity-driven: byte-identical interface, status vocabulary expanded to fit existing dispatcher writes (`ready`, `blocked`), bd-shim translator with 30+ translation rules, JSON output matching bd's field-name choices, AC-in-markdown extraction during migration, vestigial `Sync()` no-op kept just for interface compat. The motivation was honest — risk reduction during cutover and test-budget pressure — but the cost was permanent ergonomic debt locked into oro's own package indefinitely. ("Post-migration cleanup specs are notoriously deferred indefinitely; see Phase 11's forcing function.") v15 flips six items from parity to need-first:

| # | What v14 had | v15 changes to |
|---|---|---|
| 1 | `Store` byte-identical to `BeadSource` (13 methods, mixed return types, positional `Create`, no-op `Sync`) | Reshaped `Store` (**12 methods** — v16 corrected from v15's 10; HasChildren is non-redundant), single `Bead` type for all reads (`*BeadDetail` becomes a type alias during migration, removed in Phase 10), `CreateParams`/`UpdateParams` structs, no `Sync` |
| 2 | Schema `CHECK status IN ('open','in_progress','closed','blocked','ready')` matching what dispatcher writes today | `CHECK status IN ('open','in_progress','blocked','closed')`; `ready` remains derived via `beads_ready`, while `blocked` is stored for manually blocked/imported bd rows and also derived for dependency-blocked open rows via `beads_blocked`; dispatcher's `Update(..., "ready")` callsites patched to write `"open"` |
| 3 | `bd-shim` translator binary with ~30 translation rules + deny-default + test extraction | **No shim.** Cutover protocol: operators restart all workers; in-flight prompts emitting `bd …` fail; that's intentional and observable. One inconvenient hour vs permanent translator code. |
| 4 | JSON output schema "matches bd verbatim" (`parent`, `issue_type`, etc.) | Clean schema (`parent_id`, `type`, RFC3339 timestamps); `pkg/mg/data` parsers update in lockstep with the rewrite already scheduled in Phase 5 |
| 5 | Migration extracts AC from `## Acceptance Criteria` markdown headers in description, leaves header in description | Migration normalizes: AC text moves to `acceptance_criteria` column; `## Acceptance Criteria` header *and* its body are stripped from `description`. One-time clean-up. |
| 6 | `Sync()` no-op method kept on `Store` for `BeadSource` interface compat | Dropped. No method, no caller. |

**Cost / benefit (honest):** ~+0.5 to +1 week of engineering work. v15 *adds* the interface-rename pass (one-shot mechanical: `BeadSource` → `beadstore.Store`, ~30 call sites) and the dispatcher `Update()` callsite fixes (6 lines per the codex inventory). v15 *removes* the bd-shim entirely (estimated 4–5 days of work in v14 — design + translation table + tests + extraction harness), the AC-extraction logic in the migration (1 day), and the JSON-shape preservation work in mg/data (factored into the 4–5 day mg/data refactor anyway). Net effort delta ≈ +3 to +5 person-days. Risk profile changes: cutover is one-shot (workers restart at cutover; prompts that still emit `bd` fail loudly until restarted) instead of gradual via shim. The trade is more cutover-day attention for permanently cleaner code.

- **v16** (2026-04-27) — codex round 7 against v15 caught four real execution bugs in the need-first reshape. The pivot direction stays; v16 patches the execution:
  - **R7#1: HasChildren restored.** v15 collapsed `HasChildren` and `AllChildrenClosed` claiming overlap. They're not equivalent: `AllChildrenClosed("epic-x")` returns `(true, nil)` for "no children" (vacuously) AND for "all children closed" — the dispatcher needs to distinguish "epic needs decomposition" (no children) from "epic is in flight or done." `pkg/dispatcher/dispatcher.go:3209,3570,3575` and `escalation_precheck.go:76` use both methods. v16 keeps both. Store interface is now **12 methods**, not 10.
  - **R7#2: Unified Bead struct extension is a real shape change, not a "mechanical rewrite."** v15 said "make BeadDetail's optional fields appear on the unified Bead." That requires extending `protocol.Bead` to add `WorkerID`, `ContextPercent`, `LastHeartbeat`, `GitDiff`, `Memory` (with `omitempty`). v16 §8.2.1 specifies the exact migration: add the fields in Phase 1, add a `BeadDetail = Bead` alias for compile compatibility, drop the alias in Phase 10. Wire-shape impact called out: every `Ready`/list response now includes 5 omitempty fields.
  - **R7#3: AC-stripping needs a real parser contract.** v15 reused `extractACFromDescription()` which returns AC text but no offsets — naive strip-from-header-to-end deletes subsequent sections. v16 specifies a new `extractAndStripAC(description) (ac, descWithoutAC, err)` helper with a precise contract (preserve content before AC; strip from `## Acceptance Criteria` to the next H2 header or EOF; warn on multiple AC headers) and an 8-fixture test plan.
  - **R7#4: JSON schema flip leaves mg hierarchy hanging.** `pkg/mg/data.Issue` has `json:"issue_type"` and computes parent via dotted-ID parsing in `ParentID()` method. v15 said "consume oro-native JSON" without explicitly addressing the Issue struct change OR the hierarchy-builder fallout. v16 §12.6 (Phase 5) deliverable list now includes: rename JSON tag to `type`, add explicit `ParentID` *field*, update every caller of the `ParentID()` *method* to prefer the field. Acceptance test: a bead with explicit `parent_id` and flat ID is hierarchically placed correctly.
  - **R7 procedural note also addressed:** Phase 8 deliverable list now explicitly includes "restart all workers + strip bd from worker PATH" — v15 had this rule only in §7.3 cutover-protocol prose, which wouldn't have been a hard deliverable.
- **v17** (2026-04-27) — codex round 8 caught two v16 incomplete-cleanup items:
  - **R8#1 (stale legacy-shape text in unconverted sections):** v16 reshaped §8.2 and most of §8.3 but missed §8.1 (layout comment still said "13 methods matching BeadSource exactly"), §8.3b (MemoryFetcher Show signature), §14.5 acceptance inventory ("Sync no-op", "Show returns *BeadDetail", "All 13 BeadSource methods"), and Appendix D glossary. v17 cleans every stale reference.
  - **R8#2 (Issue.ParentID field/method collision):** v16 said add `ParentID string` with `json:"parent_id,omitempty"` while keeping the existing `ParentID()` *method* on the same struct (`pkg/mg/data/issue.go:268`). Go does not allow a struct field and method to share a selector name on the same type — this would not compile. v17 fix: rename the field to `ParentIDValue string` with `json:"parent_id,omitempty"`, and reshape `ParentID()` into a unified accessor that returns `ParentIDValue` when non-empty, falling back to the dotted-ID parser. Every existing caller of `issue.ParentID()` continues to work; the new oro-native JSON path populates `ParentIDValue` and the accessor returns it.

**v14 — `--parent` semantic contract pinned into Phase 2 acceptance** (preserved from v14, still applies in v15):

- **Contract:** `oro bead create --parent=<parent-id>` sets `beads.parent_id = <parent-id>` **and inserts zero rows into `bead_deps`.** It does not create a "blocks" or any other dependency edge as a side effect.
- **Why this matters:** the legacy bd CLI had a quirk where `--parent` was sometimes interpreted as also implying a backward dependency (the parent depends on the child, or the child blocks the parent — observed inconsistently). Several oro prompts use `--parent` for the clean "child of an epic" semantic; `pkg/ops/epic_fix_prompt.go:27` is the canonical example. `oro bead create` must be unambiguous.
- **Phase 2 acceptance test row** (added to §14.5 inventory): a synthetic bead created via `oro bead create --title='child' --parent=<epic-id>` is followed by `SELECT COUNT(*) FROM bead_deps WHERE bead_id=<new-id> OR depends_on_id=<new-id>` — must be 0. The `beads.parent_id` of the new bead must equal `<epic-id>`. Both assertions are part of the Phase 2 CLI test.
- **Migration parity:** `oro bead migrate-from-dolt` does not invent dep edges from bd's `parent` field either. If a bd-side bead had both a `parent` and a separate `blocks` dep on that parent, both transfer; if it had only `parent`, only `parent_id` is set on the new row.
**Audience:** Engineering leadership; reviewers familiar with the oro codebase
**Type:** Architecture decision + phased implementation plan
**Companion doc:** `docs/plans/2026-04-27-external-tooling-integration-spec.md` (this spec lands ahead of it; see §20)

---

## 0. Document Conventions

- File paths absolute under `/Users/as21/codehouse/oro/` unless noted.
- "bd" = the external `beads` CLI binary (third-party, written in Go, currently used by oro via `os/exec`).
- "Dolt" = the SQL versioning database that bd uses as its storage engine.
- "Oro-native SQLite" = the SQLite database `state.db` already owned by oro at `~/.oro/state.db` (or per-project `~/.oro/projects/<hash>/state.db`).
- "BeadSource" = the Go interface defined in `pkg/dispatcher/dispatcher.go` whose `CLIBeadSource` implementation lives in `pkg/dispatcher/beadsource.go` and shells out to `bd`.
- "Worker", "dispatcher", "ops review", "QG" — same definitions as the companion integration spec.
- Effort estimates are calendar-week ranges for one full-time engineer with oro context.
- Where I'm uncertain or extrapolating, I tag the paragraph **[ASSUMPTION]** with what would resolve it.
- All file:line references are from the codebase as it stood at HEAD on 2026-04-27.

---

## 1. Executive Summary

### 1.1 The proposal in one paragraph

Oro's bead state is split across two stores: bd's Dolt database (authoritative for status, description, dependencies, parent/child, tags, metadata) and oro's own SQLite at `~/.oro/state.db` (authoritative for assignments, retry counters, worktree paths, events, escalations). All writes to the bd half flow through `os/exec` shell-outs to the bd CLI. This division causes recurring operational pain — three documented dolt-database destructions, JSONL sync that broke so often that `.beads/issues.jsonl` was gitignored on 2026-04-27, bd CLI quirks like `defer_until` not clearing on status change, no transactions across the bd↔oro boundary, and a hard install dependency on a third-party binary. This spec proposes folding all bead state into oro's existing SQLite via a new **`pkg/beadstore` package** containing a reshaped `Store` interface (12 methods, inspired by but not identical to the legacy `BeadSource` — see §8.2 for the v15 need-first reshape) and a `SQLiteStore` implementation, gated behind a config flag for parallel-run validation, with a one-shot migration tool, an `oro bead ...` CLI surface that subsumes the bd subcommands oro actually uses, and a deliberate `oro bead export` command that produces a JSONL snapshot on demand for backup or audit. Total engineering effort: **8.5–10 weeks** for one engineer. The architectural keystone — that the bead-source surface is already an interface — collapses what looks like a 430-file change into a focused replacement of one Go package plus targeted updates to ops/worker prompts and ~30 supporting files.

### 1.2 Why this is much smaller than it looks

Of the ~430 files that reference `bd`, `dolt`, or `.beads/`:

- **368+ are test files** that mock bd CLI calls via a `fakeRunner` pattern. Most are mechanically redirectable to a fake `Store`, but a non-trivial subset (~50) assert on verbatim bd command strings inside rendered prompts (golden-file tests in `pkg/ops/*_test.go`) and need hand-fixes plus golden regen. Realistic conversion budget: **10–14 days**, not 5.
- **~28 are Go files that shell out to bd.** Of these, **all but ~6 go through the `BeadSource` interface defined in `pkg/dispatcher/dispatcher.go` (with `CLIBeadSource` implementing it in `pkg/dispatcher/beadsource.go`)**. Replacing the implementation lives in a new package.
- **The remaining ~6 Go shell-outs are dolt-management commands** (`oro dolt setup/start/stop/teardown/repair`, `oro doctor`'s dolt corruption check, `oro stop`'s dolt flush). All are deletable in this migration.
- **~13 prompt/skill/hook files** mention bd in instructions. Each is a search-and-replace: `bd create` → `oro bead create`, etc.
- **2 documentation files** mention bd setup.

The high-leverage seam is the bead-source interface. Get the new implementation right and most of oro doesn't notice the change. The new home is `pkg/beadstore` (not inside `pkg/dispatcher`); see §5.3 and §8 for the package re-org rationale.

### 1.3 The decision matrix in one table

| Component | Verdict | Effort | Risk |
|---|---|---|---|
| Add `beads`, `bead_deps`, `bead_tags`, `bead_labels`, `bead_metadata`, `bead_notes` tables to `pkg/protocol` | **DO** | 3 days | Low |
| Create `pkg/beadstore` package with `Store` interface + `SQLiteStore` impl | **DO** | 5 days | Low |
| Rename/migrate `BeadSource` call sites to `beadstore.Store` (v15: no alias — shapes differ) | **DO** | 3 days | Low |
| Add `oro bead` Cobra subcommand tree (imports `pkg/beadstore` directly) | **DO** | 3 days | Low |
| One-shot migration tool: dolt → oro SQLite | **DO** | 4 days | Medium |
| Parallel-run mode (**read-only shadow**, no dual-write) for validation | **DO** | 2 days | Low |
| Cutover via config flag | **DO** | 1 day | Low |
| `oro bead export` JSONL snapshot command | **DO** | 1 day | None |
| Update worker prompt (`pkg/worker/prompt.go`) | **DO** | 1 day | Low |
| Update 5 ops prompts (`pkg/ops/*_prompt.go`) | **DO** | 2 days | Low |
| Update 7 hook scripts (`assets/hooks/`) | **DO** | 1 day | Low |
| **Test mock conversion** (368+ files; ~50 with verbatim-string asserts) | **DO** | **10–14 days** | **Medium** |
| Delete dolt management code (`cmd/oro/cmd_dolt.go`, `pkg/dispatcher/dolt_recovery.go`, etc.) | **DO** | 2 days | Low |
| Remove bd from `oro init` tool list | **DO** | 0.5 day | None |
| Update `docs/INSTALL.md`, `README.md`, dev docs | **DO** | 0.5 day | None |
| Stealth-mode preservation | **DO** | included in cutover | Low |
| `oro bead import <jsonl>` for round-trip | **OPTIONAL** | 1 day | None |

**Total: 8.5–10 weeks** for one engineer, including cutover, observation period, and dolt-code deletion. Estimate evolution: v1 = 5–6 weeks (naive), v2 = 7–9, v3 = 8–10 (added Phase 0 audits + bd-shim work), v15 = 8.5–10 (added interface-rename + status-vocabulary fixes; removed bd-shim entirely; net +2 to +3 person-days vs v14, structurally cleaner).

### 1.4 What we keep, what we drop

**Keep:**

- Beads as a concept (bead = unit of work).
- The data model: id, title, description, AC, status, priority, type, deps, tags, metadata, parent/child epic, defer_until, owner.
- `bd ready` semantics (open + no unmet deps + not deferred). Derived in oro via the `beads_ready` view.
- The status machine (`open` / `in_progress` / `blocked` / `closed`). `ready` is derived via `beads_ready`; `blocked` is both stored for manual/imported blocked rows and derived for dependency-blocked open rows.
- Hierarchical dotted ID format ("oro-7nzy", "mg-007.2.1") — still referenced by tmux pane names, branch names, event log payloads.
- Audit history — `updated_at` preserved verbatim during migration so historical timestamps match.
- Stealth mode (zero-footprint projects under `~/.oro/projects/s-<hash>/`).
- JSONL export — but as on-demand backup, not a continuous sync target.

**Drop:**

- The `bd` binary as a runtime dependency.
- Dolt entirely (no more `.beads/dolt/`, no more journal corruption, no more `bd init --force` foot-gun).
- The `dolt-server.port` per-project port registry.
- All `oro dolt ...` subcommands.
- The bd-CLI-mediated write path (`os/exec` to `bd update/close/create`).
- The `.beads/backup/full-state.jsonl` heartbeat backup (replaced by on-demand snapshot).
- Cross-process race conditions between dispatcher writes and external bd writes (since there are no external bd writes anymore).
- **(v15) The 13-method byte-identical interface.** Replaced with a reshaped 12-method `Store` interface using a single `Bead` type, `CreateParams`/`UpdateParams` structs, and no `Sync`. (v15 first cut said 10; v16 added `HasChildren` back; v17 final count 12.)
- **(v15) `Sync()` no-op method.** Pure interface ceremony. Drop entirely.
- **(v15/v18) `ready` as a stored status.** `ready` is derivable runtime state; the dispatcher's `Update(..., "ready")` callsites are patched to write `"open"` (semantically: "this bead is now assignable again"). `blocked` remains an allowed stored status for manually blocked/imported bd rows, and dependency-blocked open rows are still derived through `beads_blocked`.
- **(v15) The bd-shim translator binary.** No shim, no translation table, no deny-default. Cutover restarts workers; in-flight `bd …` prompts fail.
- **(v15) JSON output parity with bd.** `pkg/mg/data` parsers update to read oro's clean JSON (`parent_id`, `type`, RFC3339 fields).
- **(v15) AC-in-markdown extraction at read time.** Migration normalizes AC into the column once; `description` no longer carries a `## Acceptance Criteria` section after migration.

### 1.5 What we gain

- **One data store.** `~/.oro/state.db` becomes the single source of truth. No more "is this in dolt or in protocol?" mental tax.
- **Real transactions.** SQLite supports `BEGIN/COMMIT`. The "check ready → mark in_progress → record assignment" flow becomes one atomic transaction.
- **Schema control.** `ALTER TABLE` runs in oro's migration system. The `defer_until`-not-clearing bug becomes a one-line fix in oro's code rather than something to memorize as a bd workaround.
- **Telemetry for free.** Every status transition writes to the existing `events` table. Auditing and dashboards become trivial.
- **Removed install step.** Users no longer need to `go install github.com/steveyegge/beads/cmd/bd@latest`.
- **Fewer recovery rituals.** The dolt-recovery ladder in `MEMORY.md` (3 entries: `feedback_never_bd_init_force`, `feedback_dolt_recovery`, `feedback_dolt_doltcfg_conflict`) becomes obsolete.

---

## 2. Background: The Current Architecture

### 2.1 The split-brain state model

Today, bead state lives in **two databases**, written by **two processes**, with **no transactional bridge between them**.

| Database | Engine | Owned by | Authoritative for |
|---|---|---|---|
| `.beads/dolt/` | Dolt SQL | `bd` binary | `id`, `title`, `description`, `acceptance_criteria` (embedded in description markdown), `status`, `priority`, `type`, `parent_id`, `dependencies`, `tags`, `labels`, `metadata`, `notes`, `created_at`, `updated_at`, `closed_at`, `close_reason`, `deferred_until`, `owner` |
| `~/.oro/state.db` (or `~/.oro/projects/<hash>/state.db`) | SQLite | oro dispatcher | `assignments` (bead_id, worker_id, worktree, status, attempt_count, handoff_count), `events` (audit log), `commands` (manager directives), `escalations`, `memories` (semantic memory), `memory_chunks`, `memory_search_events`, `rejection_history`, `kv_store`, `pane_activity` |

The two stores are connected by:

- **Read path:** `pkg/dispatcher/beadsource.go:CLIBeadSource` shells out to `bd ready`, `bd show`, `bd list`, `bd export`. Returns parsed JSON. **Never persists what it reads** — every call is a fresh process.
- **Write path:** Same `CLIBeadSource` shells out to `bd close`, `bd update`, `bd create`. **Best-effort, no transactions.** A post-write `bd show` verify exists in `Update()` (beadsource.go:266–278) but doesn't help if a *concurrent external* bd invocation lands in between.
- **No sync, no cache, no reconciliation.** Oro's SQLite is a pure event log + worker tracker; bd is the bead truth.

### 2.2 The 13 BeadSource methods (verified at `pkg/dispatcher/dispatcher.go:79–93`)

The interface, copied verbatim from the source on 2026-04-27:

```go
type BeadSource interface {
    Ready(ctx context.Context) ([]protocol.Bead, error)
    InProgress(ctx context.Context) ([]protocol.Bead, error)
    Blocked(ctx context.Context) ([]protocol.Bead, error)
    Closed(ctx context.Context, limit int) ([]protocol.Bead, error)
    Show(ctx context.Context, id string) (*protocol.BeadDetail, error)
    Close(ctx context.Context, id string, reason string) error
    Create(ctx context.Context, title, beadType string, priority int,
           description, parent, acceptanceCriteria string) (string, error)
    Update(ctx context.Context, id, status string) error
    Sync(ctx context.Context) error
    AllChildrenClosed(ctx context.Context, epicID string) (bool, error)
    HasChildren(ctx context.Context, epicID string) (bool, error)
    FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error)
    Export(ctx context.Context) ([]byte, error)
}
```

Three observations about the **legacy** interface (what `pkg/dispatcher.BeadSource` looks like *today*, before migration). v15's need-first pivot reshapes the new `Store` interface (§8.2); these observations document the source state, not the target.

1. **Legacy has 13 methods.** `Sync(ctx)` and `AllChildrenClosed(ctx, epicID)` are present and load-bearing. `Sync` is a no-op pass-through (delegates to `bd sync` / `bd export`); `AllChildrenClosed` is used by the dispatcher to decide whether an epic can auto-close. v15 drops `Sync` and keeps `AllChildrenClosed` (plus `HasChildren`); the new `Store` is 12 methods.
2. **Legacy `Show` returns `*protocol.BeadDetail`.** `BeadDetail` is a richer struct (`pkg/protocol/types.go:68–69`) with runtime fields beyond the lightweight `Bead`. v15 unifies them: `Bead` absorbs `BeadDetail`'s 5 runtime fields with `omitempty`, and `BeadDetail` becomes a Phase-10-removable type alias (§8.2.1). The new `Store.Show` returns `*protocol.Bead`.
3. **Legacy `Create` is positional and returns `(string, error)`.** Not a struct-of-params and not a `Bead`. v15 reshapes to `Create(ctx, CreateParams) (*protocol.Bead, error)` (§8.2). The interface-rename pass in Phase 1 transforms each call site mechanically.

| Method | Today's implementation | Frequency |
|---|---|---|
| `Ready(ctx)` | `bd ready --json` | Every assign loop (~2–5s) |
| `InProgress(ctx)` | `bd list --status=in_progress --json` | Heartbeat / dashboard |
| `Blocked(ctx)` | `bd list --status=blocked --json` | Dashboard |
| `Closed(ctx, limit)` | `bd list --status=closed --json --limit=N` | Dashboard |
| `Show(ctx, id) → *BeadDetail` | `bd show <id> --json` | Per-bead detail fetch |
| `Close(ctx, id, reason)` | `bd close <id> --reason=<reason>` | Worker success |
| `Update(ctx, id, status)` | `bd update <id> --status=<status>` | Status transitions |
| `Create(ctx, title, type, prio, desc, parent, ac) → string` | `bd create --title=... --type=... --priority=N ...` | Epic decomposition, escalation |
| `Sync(ctx)` | `bd sync` (no-op today) | Compat-stub |
| `AllChildrenClosed(ctx, epicID)` | derived from `bd list --parent=<epicID>` | Epic auto-close gate |
| `HasChildren(ctx, epicID)` | `bd list --parent=<epicID> --json` | Epic completion check |
| `FindByParentAndTag(ctx, parent, tag)` | `bd list --parent=<id> --tag=<tag> --json` | Sibling discovery |
| `Export(ctx) → []byte` | `bd export` (raw JSONL bytes) | Heartbeat backup |

There is a parallel UI-side wrapper at `pkg/mg/data/{source,mutate}.go` that does its own bd shell-outs (`source.go:64–285`, `mutate.go:10–37`). This is duplicated logic — the migration consolidates it onto `BeadSource`.

### 2.3 The bd CLI surface oro actually consumes

Across all of oro (inventory agent's count):

| bd subcommand | Oro callers | Use |
|---|---|---|
| `bd ready` | dispatcher, hooks, manager prompt | List unblocked open beads |
| `bd list --status=…` | dispatcher, dashboard, hooks | Status-filtered list |
| `bd list --parent=…` | dispatcher (epic check) | Children of an epic |
| `bd show <id>` | dispatcher, dashboard, ops prompts | Single bead detail |
| `bd close <id>` | dispatcher | Mark closed |
| `bd update <id> --status=…` | dispatcher | Status change |
| `bd update <id> --notes=…` | ops prompts (escalation) | Append notes |
| `bd update <id> --acceptance=…` | ops prompts (write-AC) | Add AC text |
| `bd update <id> --priority=…` | dashboard | Re-prioritize |
| `bd update <id> --type=…` | ops prompts (oversized→epic) | Convert to epic |
| `bd update <id> --parent=…` | worker, ops prompts | Parent assignment |
| `bd create` | worker, ops prompts | New beads |
| `bd dep add` | worker, ops prompts | Declare blocker |
| `bd context --json` | dashboard | Workspace info |
| `bd doctor --agent --json` | dashboard | Health check |
| `bd export` | dispatcher heartbeat | Backup |
| `bd init` / `bd init --from-jsonl` | `oro init` | Setup |
| `bd dolt status/start/commit` | doctor, stop, recovery | Dolt mgmt |
| `bd import <jsonl>` | recovery | Restore |
| `bd --version` | dashboard | Health |

That's 19 distinct subcommands. The migration only needs to replicate the **first 13** functionally — the dolt-management ones go away with dolt itself.

### 2.4 Stealth mode (per-project state.db)

`cmd/oro/cmd_bd.go:30–82` injects `--db ~/.oro/projects/s-<hash>/beads` into bd commands when stealth mode is active. This lets oro track beads without `.beads/` in the project root.

The new architecture preserves stealth mode because **every project — standard or stealth — gets its own `state.db`**. Standard mode: `~/.oro/projects/<project>/state.db`. Stealth mode: `~/.oro/projects/s-<hash>/state.db`. The path is resolved by `cmd/oro/paths.go:ResolveDaemonPaths()` (lines 51–70) which returns a daemon-paths struct carrying `StateDBPath`. **v11 correction (codex round-3 #4):** earlier drafts referenced `ProjectPaths.StateDBPath`; that field does not exist on `ProjectPaths` (which holds project-artifact paths only — `cmd/oro/paths.go:83–95`). Every spec reference uses "the `StateDBPath` returned by `ResolveDaemonPaths()`."

**Crucial implication:** the bead schema migration (§6) runs *per-project*, not globally. Every `state.db` opened by oro applies all `pkg/protocol` migrations on first contact, which means each project has its own complete bead-table schema, isolated from other projects. There is no shared global bead namespace. This matches today's Dolt model (each project has its own `.beads/dolt/` directory) and preserves stealth-mode isolation.

**What the migration tool does in stealth mode:** when `oro bead migrate-from-dolt` runs in a stealth project, it reads from `~/.oro/projects/s-<hash>/beads/` (the legacy stealth-mode bd location — note no leading dot; v12 corrects v9–v11 which incorrectly wrote `.beads/`) and writes to `~/.oro/projects/s-<hash>/state.db`. The actual path is constructed by `stealthProjectPaths` at `cmd/oro/paths.go:180` and used by the bd-wrapper at `cmd/oro/cmd_bd.go:73`. Path resolution goes through the same `resolveProjectPaths` flow; nothing project-aware is special-cased in the migration logic.

**Hooks that reference `.beads/`:** the seven hook scripts in `assets/hooks/` (per §11.4) currently call `bd …` directly. After Phase 6 they call `oro bead …` instead, which goes through `resolveProjectPaths` automatically — no per-hook awareness of stealth vs standard is needed. The hooks themselves don't need to know where state.db lives; the `oro bead` CLI handles path resolution.

This addresses adversarial review C6: stealth-mode bead tracking is preserved because schema, CLI, and hooks all route through one path-resolver that already handles both modes correctly.

### 2.6 The Dolt server

bd's data lives in a Dolt SQL server, either per-project (a process per project) or shared on port 13307. `cmd/oro/cmd_dolt.go` provides `oro dolt setup/start/stop/status/teardown/repair`. A stale dolt process detected by `cmd_cleanup.go` via `pgrep -f 'dolt sql-server.*\.beads/dolt'`. After this migration, all of that disappears — SQLite is in-process.

---

## 3. The Diagnosis

A short rehearsal of the documented pain. From `MEMORY.md` lines 26, 28, 31, 46, 47, 48:

1. **`bd init --force` has destroyed bead history three times.** Recovery ladder documented in `feedback_never_bd_init_force.md`.
2. **`bd update --status=open` does not clear `defer_until`.** Bead becomes invisible to `bd ready` even though status reads `open`. Documented workaround: `bd defer X --until=2026-01-01 && bd undefer X`. (`feedback_bd_zombie_defer_until.md`.)
3. **Dolt journal corruption recovery.** `dolt fsck` then `dolt fsck --revive-journal-with-data-loss`. Never `rm -rf` the dolt dir. (`feedback_dolt_recovery.md`.)
4. **"Multiple .doltcfg directories" error.** Red herring — solved by `rm .beads/.doltcfg` (the config dir, not the data dir). (`feedback_dolt_doltcfg_conflict.md`.)
5. **`bd export` is the command, not `bd sync`.** Old reflex from prior naming. (`MEMORY.md` line 28.)
6. **Stale beads reappear as open after `bd export`.** Workaround: re-close. (`MEMORY.md` line 29.)
7. **JSONL sync was so unreliable that `.beads/issues.jsonl` is now gitignored** as of decision 2026-04-27. Local Dolt is the source of truth in the current architecture. The git half of the sync was abandoned.
8. **bd CLI quirks across versions.** The `feedback_bd_init_cleanup.md` note ("After bd init/setup, remove AGENTS.md, beads skill, and bd prime hooks — we don't use them") suggests bd's defaults don't match oro's needs and have to be cleaned up after every install.

These are eight distinct, recurring, manually-mitigated failure modes. Each is a tax on operator attention. Each disappears when bd and dolt go away.

---

## 4. Options Considered

For completeness, the alternatives I weighed before landing on the recommendation.

### 4.1 Status quo — keep bd + dolt

- **Pros:** No work.
- **Cons:** Eight documented failure modes recur. Operational tax compounds.
- **Verdict:** Unacceptable.

### 4.2 Keep bd, swap dolt for sqlite via bd's storage abstraction

- **Pros:** No oro changes.
- **Cons:** [ASSUMPTION] bd's storage is hardcoded to dolt; switching would require upstream PRs to a third-party project. Doesn't address bd CLI quirks. Doesn't remove the install dependency.
- **Verdict:** Not in our control.

### 4.3 Vendor the bd source into oro

- **Pros:** Schema control without re-implementation.
- **Cons:** Carries dolt with it. Forks a third-party project. Maintenance burden compounds with upstream drift.
- **Verdict:** Worse than reimplementing.

### 4.4 SQLite database file checked into git (the user's first proposal)

- **Pros:** Easy mental model: "the data is in the repo."
- **Cons:** Binary file, opaque diffs, merge conflicts on a constantly-mutating file destroy data. Reinvents Dolt poorly. Concurrent writers (dispatcher + human + workers) corrupt the file.
- **Verdict:** Wrong tool for the job.

### 4.5 SQLite database NOT in git, with on-demand JSONL export (this spec)

- **Pros:** SQLite is single-process-friendly with WAL. Already deployed in oro for `pkg/memory` and `pkg/protocol` — proven path. JSONL export gives a diffable, committable snapshot when wanted. Removes bd, removes dolt, gains transactions and telemetry.
- **Cons:** ~5–6 weeks of work. Loss of dolt's branching/merging features (verified unused — see §11.1).
- **Verdict:** Recommended.

### 4.6 Pure JSONL, no SQLite

- **Pros:** Maximum simplicity, fully diffable.
- **Cons:** Slow scans on large bead sets, no FTS, no joins. Oro's existing event log and assignments live in SQLite — splitting would create a worse version of the same problem.
- **Verdict:** Rejected.

---

## 5. Decision & Architecture

### 5.1 Decision

**Create a new `pkg/beadstore` package containing a reshaped 12-method `Store` interface (v15 need-first design — see §8.2; inspired by the legacy `pkg/dispatcher.BeadSource` but with `CreateParams`/`UpdateParams`, unified `Bead` type, no `Sync`) and a `SQLiteStore` implementation. Add bead tables to `pkg/protocol/schema.go`. Add `oro bead` CLI subtree that imports `pkg/beadstore` directly (no dispatcher dependency). Provide one-shot dolt→sqlite migration. Keep on-demand JSONL export. Delete dolt management code. Run a *read-only* shadow validation period before flipping the default. Retain `CLIBeadSource` and `BeadsDir` as legacy paths for one release cycle past cutover.**

### 5.2 The keystone insight: the bead-source surface is already an interface

`pkg/dispatcher/dispatcher.go` defines the 13-method `BeadSource` interface. `CLIBeadSource` (in `pkg/dispatcher/beadsource.go`) is the only production implementation. Every consumer in oro — dispatcher, dashboard (via mg/data wrappers), ops review, workers indirectly via `AssignPayload` — depends on the interface, not the implementation.

Adding a SQLite-backed implementation alongside `CLIBeadSource` is the architectural seam. The dispatcher initialization picks one or the other at startup based on a config flag. **v15's need-first reshape (§8.2) makes the new `Store` interface different from the legacy `BeadSource`** — so unlike v9–v14's parity-first plan, callers are *not* untouched: Phase 1 includes a one-shot interface-rename pass (§11.2 + §12.2) that updates ~30 call sites mechanically, and Phase 7 converts ~50 verbatim-string-asserting tests. The seam still works (the dispatcher field flips from `BeadSource` to `beadstore.Store` with a config-gated implementation choice), but the migration is no longer fully invisible to callers.

This is what makes the project tractable. Without this seam, the migration would be a 30+ file rewrite. With it, the core is one new package.

**Why `pkg/beadstore` and not `pkg/dispatcher/sqlitebeadsource.go`:** putting the implementation inside `pkg/dispatcher` forces `cmd/oro/cmd_bead.go` (the CLI) and worker subprocesses to import dispatcher internals (worker pool, tmux glue, escalation router) just to do `oro bead create`. A focused `pkg/beadstore` package — owning the `Store` interface, the `SQLiteStore`, the migration logic, the test fake — lets the CLI and workers depend on a small, pure data-access surface. The dispatcher then depends on `beadstore.Store` instead of owning the interface. **v15 update (no alias):** v9–v14's plan was `type BeadSource = beadstore.Store` for migration compat. v15's reshape changes the interface shape, so an alias would not compile; the migration replaces `BeadSource` with `beadstore.Store` mechanically across ~30 call sites in Phase 1.

### 5.3 Component architecture (after migration)

```
                       ┌─────────────────────────────────────┐
                       │           pkg/beadstore             │
                       │  (NEW — pure data-access surface)   │
                       │                                     │
                       │   store.go      Store interface     │
                       │   sqlite.go     SQLiteStore impl    │
                       │   shadow.go     ShadowStore wrapper │
                       │   migrate.go    dolt → sqlite tool  │
                       │   testfake.go   FakeStore for tests │
                       └────────────┬────────────────────────┘
                                    │
                                    │ depends on
                                    ▼
                       ┌─────────────────────────────────────┐
                       │           pkg/protocol              │
                       │  state.db schema (existing) +       │
                       │  NEW: beads, bead_deps, bead_tags,  │
                       │  bead_labels, bead_metadata,        │
                       │  bead_notes, beads_fts, views       │
                       └─────────────────────────────────────┘
                                    ▲
                  imports beadstore │
        ┌───────────────────────────┴──────────────────────────────┐
        │                                                          │
┌───────────────────────────┐                       ┌──────────────────────────┐
│      pkg/dispatcher       │                       │     cmd/oro/cmd_bead.go  │
│                           │                       │  (Cobra `oro bead ...`)  │
│  Dispatcher.beads:        │                       │                          │
│    beadstore.Store        │                       │  uses beadstore.Store    │
│  (interface dep, not impl)│                       │  directly — no           │
│                           │                       │  dispatcher import       │
│  CLIBeadSource retained   │                       └──────────────────────────┘
│  in pkg/dispatcher/       │                                ▲
│  beadsource.go for one    │                                │
│  release cycle (legacy).  │                                │ subprocess
│                           │                                │
│  v15: no type alias —     │                       ┌──────────────────────────┐
│  shapes differ. Phase 1   │                       │  Workers (Claude Code)   │
│  rename pass replaces     │                       │                          │
│  ~30 call sites.          │                       │                          │
└───────────────────────────┘                       │  invoke `oro bead …`     │
        │                                           │  via prompts, which      │
        │ writes assignments,                       │  shell-out to the same   │
        │ events, escalations                       │  CLI binary (above).     │
        ▼                                           └──────────────────────────┘
   ~/.oro/state.db
   (one SQLite file, WAL mode, project-scoped)
```

Key invariants of the diagram:

- `pkg/beadstore` does not import `pkg/dispatcher`. (`go list -deps` in CI enforces this.)
- `cmd/oro/cmd_bead.go` does not import `pkg/dispatcher`. The CLI is dispatcher-free.
- Workers invoking `oro bead create` re-enter the same binary via subprocess; they hit `pkg/beadstore` through the CLI, not via any dispatcher RPC.
- All writes flow through transactions in `pkg/beadstore.SQLiteStore`. SQLite WAL mode handles cross-process safety.

What disappears:

- The Dolt server (per-project or shared port 13307).
- `cmd/oro/cmd_dolt.go`.
- `pkg/dispatcher/dolt_recovery.go`.
- `cmd/oro/cmd_bd.go` (the bd-wrapper).
- `cmd/oro/port_registry.go` (only managed dolt ports).
- The `.beads/dolt/` directory.
- The `.beads/backup/` directory.

What stays:

- `~/.oro/state.db`.
- Stealth mode's `~/.oro/projects/s-<hash>/state.db` per-project file.
- `.beads/issues.jsonl` snapshots written by `oro bead export` if the user asks for them. Gitignored as of the 2026-04-27 decision.

### 5.4 Concurrency model

SQLite in WAL mode supports a single writer + multiple readers. Oro's dispatcher is the writer for assignment/event state; concurrency comes from:

1. The dispatcher main loop.
2. Worker subprocesses invoking `oro bead close X` etc.
3. Humans running `oro bead update X --priority=…`.
4. The dashboard reading.

Workers' `oro bead ...` calls open a connection to the same `state.db`, perform the write, close. SQLite's WAL handles coordination. The dispatcher's existing `d.mu` mutex serializes its in-memory state; database transactions handle cross-process safety.

The classic race window — "is this bead still ready by the time I claim it?" — closes:

```sql
-- Atomic claim, in one transaction:
BEGIN;
SELECT id FROM beads
  WHERE id = ?
    AND status = 'open'
    AND id NOT IN (
      SELECT bead_id FROM bead_deps bd
      JOIN beads parent ON parent.id = bd.depends_on_id
      WHERE bd.bead_id = ? AND bd.type IN ('blocks','conditional-blocks','parent-child')
        AND parent.status != 'closed'
    );
-- if row returned:
UPDATE beads SET status = 'in_progress', updated_at = ? WHERE id = ?;
INSERT INTO assignments (bead_id, worker_id, worktree, ...) VALUES (...);
INSERT INTO events (type, source, bead_id, worker_id, payload, ...) VALUES ('assigned', ...);
COMMIT;
```

That is genuinely impossible to do safely with bd today, because the dispatcher's `bd update` and the assignments INSERT are different processes touching different databases.

---

## 6. Schema Design

### 6.1 New tables added to `pkg/protocol/schema.go`

The naming follows the existing `pkg/protocol` convention (snake_case, lowercase, plural).

```sql
-- ─────────────────────────────────────────────────────────────────────
-- Bead master record. One row per bead.
-- ─────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS beads (
    id                    TEXT PRIMARY KEY,
    title                 TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    acceptance_criteria   TEXT NOT NULL DEFAULT '',  -- dedicated column, no markdown extraction
    -- v18: 'ready' is derived runtime state computed by beads_ready.
    -- 'blocked' is allowed as persisted operator/import state, while
    -- beads_blocked also derives dependency-blocked open rows.
    -- Phase 1 patches dispatcher callsites that wrote "ready" to write
    -- "open" instead (semantically: "this bead is now assignable again").
    status                TEXT NOT NULL CHECK (status IN
                          ('open','in_progress','blocked','closed')),
    priority              INTEGER NOT NULL DEFAULT 2,
    type                  TEXT NOT NULL DEFAULT 'task',   -- task | bug | epic | research | chore
    parent_id             TEXT REFERENCES beads(id),
    owner                 TEXT,
    estimated_minutes     INTEGER,
    tier                  TEXT,
    model                 TEXT,
    deferred_until        TEXT,                        -- ISO8601 or NULL
    close_reason          TEXT,
    -- v9: timestamps emit RFC3339 (e.g. 2026-04-27T15:04:03.000Z) to match bd's
    -- export format. v8 used datetime('now') which returns 'YYYY-MM-DD HH:MM:SS'
    -- and broke string-comparison reconcile against bd's RFC3339 timestamps.
    created_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    closed_at             TEXT,
    -- soft-delete flag for "import wiped" cases; default 0 = live row
    deleted               INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_beads_status     ON beads(status) WHERE deleted = 0;
CREATE INDEX IF NOT EXISTS idx_beads_parent     ON beads(parent_id) WHERE deleted = 0;
CREATE INDEX IF NOT EXISTS idx_beads_type       ON beads(type) WHERE deleted = 0;
CREATE INDEX IF NOT EXISTS idx_beads_priority   ON beads(priority) WHERE deleted = 0;
CREATE INDEX IF NOT EXISTS idx_beads_deferred   ON beads(deferred_until) WHERE deleted = 0;

-- ─────────────────────────────────────────────────────────────────────
-- Bead dependency graph. Many-to-many over beads.
-- ─────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS bead_deps (
    bead_id          TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    depends_on_id    TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    type             TEXT NOT NULL DEFAULT 'blocks',
                     -- blocks | conditional-blocks | related-to | parent | duplicate
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    created_by       TEXT,
    PRIMARY KEY (bead_id, depends_on_id, type)
);
CREATE INDEX IF NOT EXISTS idx_bead_deps_depends_on ON bead_deps(depends_on_id);

-- ─────────────────────────────────────────────────────────────────────
-- Bead tags (free-form classification).
-- ─────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS bead_tags (
    bead_id    TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    tag        TEXT NOT NULL,
    PRIMARY KEY (bead_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_bead_tags_tag ON bead_tags(tag);

-- ─────────────────────────────────────────────────────────────────────
-- Bead labels (separate from tags by convention; bd distinguishes them).
-- ─────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS bead_labels (
    bead_id    TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    label      TEXT NOT NULL,
    PRIMARY KEY (bead_id, label)
);
CREATE INDEX IF NOT EXISTS idx_bead_labels_label ON bead_labels(label);

-- ─────────────────────────────────────────────────────────────────────
-- Bead metadata: arbitrary key-value pairs (e.g., model override, branch).
-- ─────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS bead_metadata (
    bead_id    TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    PRIMARY KEY (bead_id, key)
);

-- ─────────────────────────────────────────────────────────────────────
-- Bead notes. Append-only journal entries per bead.
-- ─────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS bead_notes (
    id          INTEGER PRIMARY KEY,
    bead_id     TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    author      TEXT,
    content     TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_bead_notes_bead ON bead_notes(bead_id);

-- ─────────────────────────────────────────────────────────────────────
-- FTS5 over bead title + description + AC.
-- ─────────────────────────────────────────────────────────────────────
CREATE VIRTUAL TABLE IF NOT EXISTS beads_fts USING fts5(
    title, description, acceptance_criteria,
    content='beads', content_rowid='rowid'
);
CREATE TRIGGER IF NOT EXISTS beads_fts_ai AFTER INSERT ON beads BEGIN
  INSERT INTO beads_fts(rowid, title, description, acceptance_criteria)
  VALUES (new.rowid, new.title, new.description, new.acceptance_criteria);
END;
CREATE TRIGGER IF NOT EXISTS beads_fts_ad AFTER DELETE ON beads BEGIN
  INSERT INTO beads_fts(beads_fts, rowid, title, description, acceptance_criteria)
  VALUES ('delete', old.rowid, old.title, old.description, old.acceptance_criteria);
END;
CREATE TRIGGER IF NOT EXISTS beads_fts_au AFTER UPDATE ON beads BEGIN
  INSERT INTO beads_fts(beads_fts, rowid, title, description, acceptance_criteria)
  VALUES ('delete', old.rowid, old.title, old.description, old.acceptance_criteria);
  INSERT INTO beads_fts(rowid, title, description, acceptance_criteria)
  VALUES (new.rowid, new.title, new.description, new.acceptance_criteria);
END;
```

### 6.2 Existing tables that get a new FK

```sql
-- assignments.bead_id should reference beads(id), but we don't add the
-- constraint at migration time because existing rows may reference bd-only
-- ids that haven't been imported yet. Defer the FK addition to post-migration
-- in a follow-up migration after we've verified all assignments resolve.

-- See §9 (Migration Plan) for the staging.
```

### 6.3 Useful views

These aren't tables, they're queries we'll define as views for readability:

```sql
-- "Ready" beads: open, not deferred, no unmet blocking deps.
CREATE VIEW IF NOT EXISTS beads_ready AS
SELECT b.*
FROM beads b
WHERE b.deleted = 0
  AND b.status = 'open'
  AND (b.deferred_until IS NULL OR b.deferred_until = '')
  AND NOT EXISTS (
    SELECT 1 FROM assignments a
    WHERE a.bead_id = b.id
      AND a.status = 'active'
  )
  AND NOT EXISTS (
    SELECT 1 FROM bead_deps d
    LEFT JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
    WHERE d.bead_id = b.id
      AND d.type IN ('blocks','conditional-blocks','parent-child')
      AND (parent.id IS NULL OR parent.status != 'closed')
  );

-- "Blocked" beads: stored blocked rows plus open rows with unmet deps.
CREATE VIEW IF NOT EXISTS beads_blocked AS
SELECT b.*
FROM beads b
WHERE b.deleted = 0
  AND b.status IN ('open','blocked')
  AND (
    b.status = 'blocked'
    OR b.deferred_until IS NULL
    OR b.deferred_until = ''
    OR EXISTS (
      SELECT 1 FROM bead_deps d
      LEFT JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
      WHERE d.bead_id = b.id
        AND d.type IN ('blocks','conditional-blocks')
        AND (parent.id IS NULL OR parent.status != 'closed')
    )
  )
  AND NOT EXISTS (
    SELECT 1 FROM assignments a
    WHERE a.bead_id = b.id
      AND a.status = 'active'
  )
  AND (
    b.status = 'blocked'
    OR EXISTS (
      SELECT 1 FROM bead_deps d
      LEFT JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
      WHERE d.bead_id = b.id
        AND d.type IN ('blocks','conditional-blocks','parent-child')
        AND (parent.id IS NULL OR parent.status != 'closed')
    )
  );
```

These views replicate `bd ready` and `bd list --status=blocked` semantics.

### 6.4 Schema migrations

`pkg/protocol/schema.go` already manages migrations through a sequence of `MigrateXxx` functions (see the schema-mapping agent's enumeration: 10 migrations today). This becomes migration #11 — `MigrateBeadSchema`.

```go
// In pkg/protocol/schema.go, append:
func MigrateBeadSchema(ctx context.Context, db *sql.DB) error {
    _, err := db.ExecContext(ctx, beadSchemaDDL)
    return err
}

// Call from initialization sequence after the existing migrations.
```

The migration is purely additive — no existing tables are touched.

### 6.5 What about ID format?

bd uses dot-separated hierarchical IDs ("oro-7nzy", "mg-007.2.1"). The hierarchy is decoded by the existing `ParentID()` function in `pkg/mg/data/issue.go:268`. We preserve the format exactly. The `parent_id` column is set explicitly during migration and creation, but we keep ID parsing as a fallback for legacy data.

---

## 7. CLI Design (`oro bead` Subcommands)

### 7.1 Command tree

The new subcommands are added to `cmd/oro/cmd_bead.go` (new file). Cobra structure mirrors the bd commands oro actually uses:

```
oro bead
  ready                                List unblocked open beads
  list [--status=…] [--parent=…] [--tag=…] [--limit=N]   Filtered list
  show <id> [--long]                   Single bead detail
  create --title=… [--type=…] [--priority=N] [--parent=…] [--description=…]
                   [--acceptance=… | --acceptance-criteria=…] [--estimate=<minutes>] [--tag=…]
  update <id> [--status=…] [--priority=…] [--type=…] [--parent=…] [--notes=…] [--acceptance=…] [--owner=…]
  close <id> [--reason=…]
  reopen <id>
  defer <id> --until=<iso8601>
  undefer <id>                         Clears deferred_until
  dep
    add <bead-id> <depends-on-id> [--type=blocks|conditional-blocks|related-to]
    rm <bead-id> <depends-on-id>
    list <bead-id>
  tag
    add <bead-id> <tag>...
    rm <bead-id> <tag>...
  meta
    set <bead-id> <key>=<value>
    get <bead-id> <key>
    rm <bead-id> <key>
  note
    add <bead-id> <text>
    list <bead-id>
  search <query>                       FTS over title/description/AC
  export [--out=path] [--format=jsonl|json]   Snapshot to disk
  import <path>                        Planned JSONL/JSON upsert; current shipped command is a stub
  doctor                               Health check
  status                               Quick stats: open/in-progress/closed counts
  --json                               Global flag for machine-readable output
```

### 7.2 Output format

Every subcommand supports `--json` for structured output. Default is human-readable. **(v15 reshaped):** the JSON schema is oro-native, **not** a copy of bd's output. Field names follow Go-idiomatic conventions: `parent_id` (not `parent`), `type` (not `issue_type`), RFC3339Nano timestamps with explicit zones, `null` for absent values (not omitted keys). `pkg/mg/data` parsers are rewritten in Phase 5 to consume the new schema directly. This is the intentional flip from v9–v14's "match bd verbatim" stance per the v15 need-first pivot.

The mg/data layer (`pkg/mg/data/source.go`) already parses bd JSON into a `data.Issue` struct. Our JSON output produces the same struct shape so the dashboard works without changes.

### 7.3 Cutover protocol (no shim — v15 need-first)

v9–v14 specified a `bd-shim` translator binary with ~30 translation rules + deny-default + test extraction harness. v15 deletes it.

**Why v15 has no shim:** the shim's only job was making in-flight worker prompts emit `bd …` invocations work *after* the storage replatform, before Phase 6 (prompt updates) had landed in those running sessions. That's a one-window problem worth one window of inconvenience, not permanent translator code. v14's shim machinery was ~30 translation rules, a deny-default policy, an automated extraction harness, and a Phase-9-through-Phase-10 lifecycle window for the binary itself — all to avoid telling operators "restart your workers at cutover."

**Cutover protocol:**

1. **Phase 6 lands first** — every prompt, hook, skill, and manager/architect template now emits `oro bead` syntax. CI gate: `git grep -l 'bd ' assets/skills/ pkg/worker/ pkg/ops/ cmd/oro/manager.go cmd/oro/architect.go assets/hooks/` returns zero files. (`bd` references in this doc, in changelog history, and in the migration tool itself are exempted by allow-list.)
2. **Phase 8 (migration day)** — operator runs `oro bead migrate-from-dolt`, validates the native store directly, then sets `ORO_BEADSOURCE_MODE=sqlite` and restarts the dispatcher. **And restarts every worker.** New worker spawns will use Phase-6-updated prompts emitting `oro bead` natively.
3. **Any in-flight worker not restarted** continues with its old prompt. When it eventually emits `bd update ...`, the call fails with `command not found` — loudly, traceably. The dispatcher's existing failure-recovery path retries the bead with a fresh worker.
4. **bd binary remains installed through Phase 10** only as a precaution for the migration tool itself (`bd export` for re-reads if `--reconcile` needs to re-fetch). Workers do not see it on PATH; the dispatcher's worker-spawn config strips bd from worker `PATH` at Phase 8.

**What this trades:** one-hour cutover-attention spike (operator restarts workers; one or two workers may need manual restart if missed) versus permanent shim code (30+ translation rules, test extraction, deny-default, lifecycle management). Net: simpler, smaller, no permanent runtime code.

**Acceptance test row** (Phase 8 inventory, replaces the bd-shim row): post-cutover, `git grep -l 'bd ' assets/skills/ pkg/worker/ pkg/ops/ cmd/oro/manager.go cmd/oro/architect.go assets/hooks/` returns zero files; new worker spawns produce no `bd: command not found` events in the event log over a 4-hour window.

*The v9–v14 translation table that lived here has been removed in v15. The full table (~30 rows) was the bd-shim's job description. With no shim, no table.*

*Historical note for future readers:* the table catalogued every bd invocation emitted in oro's prompts, hooks, and skill assets — `bd ready`, `bd list`, `bd show`, `bd close`, `bd update --status=<s>` and its space-form variants, `bd create` positional and flag-form, `bd dep add/remove`, `bd context`, `bd doctor`, `bd export`, `bd --version`, plus dispatcher-internal forms like `bd list --json --limit 0 --all` and `bd update <id> --claim`. v15 replaces the translation effort with a hard cutover: those invocations all change shape to `oro bead …` in Phase 6, and worker restart at Phase 8 ensures only the new shape runs in production.

### 7.4 Worker-callable subcommands

Workers in their prompts are told to call:

| Old | New |
|---|---|
| `bd create --title="…" --type=… …` | `oro bead create --title="…" --type=… …` |
| `bd update <id> --parent <p>` | `oro bead update <id> --parent=<p>` |
| `bd dep add <id> <dep>` | `oro bead dep add <id> <dep>` |
| `bd close <id>` | `oro bead close <id>` (rare; usually dispatcher closes) |

Every worker prompt + every ops prompt mentioning bd must be updated. See §11.5.

### 7.5 Idempotence

`create` accepts `--id=<explicit-id>` for idempotent re-creation (the migration tool relies on this). Without `--id`, oro generates one in the existing oro-ID format.

`update` is idempotent on identical inputs. `close` is idempotent (closing an already-closed bead is a no-op with a warning).

`dep add` / `tag add` use SQLite's `INSERT OR IGNORE`.

---

## 8. Library Design (Go API)

### 8.1 The new package: `pkg/beadstore`

Layout:

```
pkg/beadstore/
  store.go          // Store interface (v15/v16 reshape — 12 methods, single Bead type,
                    // CreateParams/UpdateParams structs; see §8.2)
  sqlite.go         // SQLiteStore implementation
  shadow.go         // ShadowStore wrapper for read-only parallel validation
  migrate.go        // dolt → sqlite migration tool used by oro bead migrate-from-dolt
  testfake.go       // FakeStore for tests (replaces fakeRunner-based bd mocks)
  types.go          // Re-exports protocol.Bead aliases if needed; otherwise empty
  store_test.go
  sqlite_test.go
  shadow_test.go
  migrate_test.go
```

`pkg/beadstore` imports `pkg/protocol` (for `Bead` struct, schema, sql helpers) and the standard library. It must **not** import `pkg/dispatcher`, `pkg/worker`, `pkg/ops`, or `pkg/mg`. A CI rule enforces this.

### 8.2 The `Store` interface — reshaped (v15 need-first)

v9–v14 specified a byte-identical interface to bd's `BeadSource` (13 methods, mixed return types, positional `Create`, no-op `Sync`). v15 reshapes:

```go
// Package beadstore — bead state.
//
// v15 design principles:
// - One `Bead` struct for all reads. No separate BeadDetail.
// - CreateParams struct, not 7-arg positional Create.
// - No Sync. (Was a bd no-op kept for interface ceremony.)
// - Stored statuses are open/in_progress/blocked/closed; ready is derived.
package beadstore

import (
    "context"

    "github.com/<oro-module>/pkg/protocol"
)

// Store is the bead-state interface. 12 methods (v16 corrects v15's drop of
// HasChildren — it's not redundant with AllChildrenClosed; see §8.2.1).
type Store interface {
    // Reads return []Bead value slices. Show returns a single *Bead with
    // optional runtime fields (Memory, GitDiff, ContextPercent, WorkerID,
    // LastHeartbeat) populated when a fetcher is wired and an active assignment
    // exists; nil pointer = "not found".
    Ready(ctx context.Context) ([]protocol.Bead, error)
    InProgress(ctx context.Context) ([]protocol.Bead, error)
    Blocked(ctx context.Context) ([]protocol.Bead, error)
    Closed(ctx context.Context, limit int) ([]protocol.Bead, error)
    Show(ctx context.Context, id string) (*protocol.Bead, error)

    // Writes.
    Create(ctx context.Context, params CreateParams) (*protocol.Bead, error)
    Update(ctx context.Context, id string, params UpdateParams) error
    Close(ctx context.Context, id, reason string) error

    // Graph queries (epic decomposition gate, sibling discovery).
    HasChildren(ctx context.Context, epicID string) (bool, error)
    AllChildrenClosed(ctx context.Context, epicID string) (bool, error)
    FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error)

    // Backup.
    Export(ctx context.Context) ([]byte, error)
}

type CreateParams struct {
    Title              string   // required
    Type               string   // task | bug | epic | research | chore (default: task)
    Priority           int      // default 2; 0 = highest
    Description        string
    AcceptanceCriteria string
    ParentID           string   // sets beads.parent_id only; no implicit dep edge (v14 contract)
    Tags               []string
    Labels             []string
    Metadata           map[string]string
    EstimatedMinutes   int
    ID                 string   // optional; for idempotent re-creation by migration tool
}

type UpdateParams struct {
    Status   *string   // pointer: nil = no change. Valid values: open|in_progress|closed.
    Priority *int
    Type     *string
    ParentID *string   // nil = no change; "" sentinel = clear parent
    Owner    *string
    // ... etc; nil-pointer-means-no-change semantics throughout
}
```

**Why this shape:**
- `Show` returns the same `Bead` struct as `Ready`, with optional fields. `BeadDetail` (the v14 split-type) was a bd inheritance that forced consumers to know which type they got. One type, optional zero-valued fields.
- `CreateParams`/`UpdateParams` structs replace 7-arg positional methods. New required fields don't break call sites.
- No `Sync()`. It was a bd no-op kept for interface compat. With no compat to maintain, it's gone.
- ~~`HasChildren` is collapsed into `AllChildrenClosed`~~ **— v16 reverts this.** Codex round 7 correctly observed: `AllChildrenClosed("epic-x")` returns `(true, nil)` for *both* "no children" (vacuously true) and "all children closed." The dispatcher uses `HasChildren` to distinguish "this epic needs decomposition" (no children → assign for decomposition) from "this epic is in flight or complete" (`pkg/dispatcher/dispatcher.go:3209,3570,3575` and `escalation_precheck.go:76`). Conflating the two methods loses that distinction. Keep both.
- 12 methods (was 13 in v14, was incorrectly 10 in v15's first cut, was 11 in v16's first claim, now correctly 12).

**Migration of existing call sites.** v14 used `type BeadSource = beadstore.Store` to keep all callers compiling. v15 has no such alias because the shapes differ. Phase 1 includes a one-shot mechanical pass:

| Call-site change | Count (estimated) | Mechanism |
|---|---|---|
| `dispatcher.BeadSource` type references → `beadstore.Store` | ~15 | search-and-replace |
| `Show(ctx, id)` callers consuming `*BeadDetail` → consume `*Bead` | ~5 | field-rename pass; missing fields populated from fetcher |
| `Create(ctx, title, type, prio, desc, parent, ac)` → `Create(ctx, beadstore.CreateParams{...})` | ~6 | mechanical transform; 6 call sites in dispatcher + ops |
| `Update(ctx, id, status)` → `Update(ctx, id, beadstore.UpdateParams{Status: &s})` | ~10 | same pattern |
| Removed `.Sync(ctx)` calls | ~2 | delete |

Total: ~40 mechanical edits across the dispatcher, ops, and mg/data packages. Estimate **3 days** for the rename + adapter pass — included in the v15 +0.5–1 week effort delta.

**Why not keep the alias and reshape later?** Because "later" never happens (Phase 11 forcing-function clause exists for exactly this reason). The reshape is small, mechanical, and has no semantic risk: every transformed call site is doing the same operation with a different argument shape, and CI catches any compile error immediately.

### 8.2.1 Unified `Bead` shape (v16 — addresses codex round 7 #2)

v15 said "`Show` returns the same `Bead` struct as `Ready`, with optional fields." That was the right *direction* but glossed over the actual struct change. The current `protocol.Bead` (`pkg/protocol/types.go:43-66`) does **not** carry `WorkerID`, `ContextPercent`, `LastHeartbeat`, `GitDiff`, or `Memory`. Those fields live on `protocol.BeadDetail` (`pkg/protocol/types.go:68-89`). Production code reads them: `escalation_precheck.go:37` reads `WorkerID`, `pkg/web/server.go:40` renders `ContextPercent` for the dashboard.

**v16 plan:** Phase 1 extends `protocol.Bead` to absorb the five runtime/detail fields with `,omitempty` JSON tags:

```go
// pkg/protocol/types.go (v16 — after extension):
type Bead struct {
    // ... existing fields (id, title, description, status, etc.) ...

    // Runtime/detail fields (v16 — moved from BeadDetail). All optional.
    // Populated by Show(); zero-valued for Ready()/Blocked()/Closed() reads
    // unless explicitly enriched. JSON omitempty keeps the wire shape clean.
    WorkerID       string `json:"worker_id,omitempty"`
    ContextPercent int    `json:"context_percent,omitempty"`
    LastHeartbeat  string `json:"last_heartbeat,omitempty"`
    GitDiff        string `json:"git_diff,omitempty"`
    Memory         string `json:"memory,omitempty"`
}

// BeadDetail is removed in Phase 10 (deferred 30 days like other cleanup).
// Until then, BeadDetail is a type alias: type BeadDetail = Bead.
```

**Migration steps:**

1. **Phase 1** extends `protocol.Bead` with the five fields. Adds `type BeadDetail = Bead` alias so existing `*BeadDetail` consumers still compile.
2. **Phase 1** also reshapes the `Store.Show` return to `*protocol.Bead` (no longer `*BeadDetail`), and the alias makes existing call sites continue to work.
3. **Phase 5** updates `pkg/mg/data.Issue` to consume the unified `Bead` shape directly via the new oro-native JSON.
4. **Phase 10** drops the `BeadDetail = Bead` alias; any remaining call sites switch to `*Bead` in a mechanical pass.

**JSON wire impact (called out explicitly per codex):** every `Ready`/`InProgress`/`Blocked`/`Closed` response now includes 5 additional `omitempty` fields. Consumers ignore them by default (they're absent when zero). Consumers that *did* depend on `Bead` not having these fields (e.g., a strict-decoder test) need updating. CI test: round-trip a `Bead` through JSON; assert `omitempty` keeps the marshaled output identical to v14 when the runtime fields are zero.

**Why this is acceptable:** the alternative — keeping `BeadDetail` as a separate type — preserves the awkward bd-inheritance forever. Extending `Bead` once and dropping `BeadDetail` is the clean end-state. The migration intermediate (alias) keeps every call site compiling without semantic change.

### 8.3 `SQLiteStore` implementation

`SQLiteStore` opens the database via the existing `cmd/oro/db.go:openDB()` (which already configures `journal_mode=WAL` and `busy_timeout=5000` per `cmd/oro/db_test.go:25–80`). The new package does **not** roll its own connection setup — it accepts `*sql.DB` from the caller. This avoids drift between dispatcher and CLI database configuration.

```go
// Package beadstore — SQLite-backed Store implementation. Reads/writes
// pkg/protocol's bead tables in oro's state.db. WAL mode is set by the
// caller's openDB, not by this package, so CLI/dispatcher share configuration.
package beadstore

import (
    "context"
    "database/sql"
    "fmt"
    "time"

    "github.com/<oro-module>/pkg/protocol"
)

type SQLiteStore struct {
    db *sql.DB
}

// NewSQLiteStore wraps an already-open *sql.DB. Caller is responsible for
// configuring the connection (WAL, busy_timeout) — typically via
// cmd/oro/db.go:openDB.
func NewSQLiteStore(db *sql.DB) *SQLiteStore { return &SQLiteStore{db: db} }

// Ready returns all open, non-deferred, non-blocked beads, ordered by priority.
// Signature: matches BeadSource exactly — []protocol.Bead value slice.
func (s *SQLiteStore) Ready(ctx context.Context) ([]protocol.Bead, error) {
    rows, err := s.db.QueryContext(ctx,
        `SELECT * FROM beads_ready ORDER BY priority ASC, created_at ASC`)
    if err != nil {
        return nil, fmt.Errorf("beadstore: ready query: %w", err)
    }
    defer rows.Close()
    return s.scanBeads(ctx, rows)
}

// Show returns the unified *protocol.Bead. v16 reshape per §8.2.1: BeadDetail
// is a type alias for Bead during the migration; Bead absorbs the five
// runtime/telemetry fields with omitempty JSON tags. Phase 10 drops the alias.
//
// Five runtime fields are sourced at read time, not from the beads row:
//   - WorkerID         ← assignments.worker_id WHERE bead_id=? AND status='active'
//   - ContextPercent   ← latest 'context' event for this bead from events table
//   - LastHeartbeat    ← latest 'heartbeat' event for this bead from events table
//   - GitDiff          ← computed from worktree (assignments.worktree path); may be ""
//   - Memory           ← MemoryFetcher callback (see §8.3b); may be ""
// Persisted fields (id, title, description, AC, status, etc.) come from the
// beads row + child tables. Show returns nil if the bead is not found.
func (s *SQLiteStore) Show(ctx context.Context, id string) (*protocol.Bead, error) {
    // 1. SELECT bead row + LEFT JOIN deps/tags/labels/metadata/notes
    // 2. SELECT bead's active assignment (if any) for WorkerID + worktree
    // 3. SELECT latest context/heartbeat events for ContextPercent, LastHeartbeat
    // 4. Compute GitDiff via worktree path (or skip if no active assignment)
    // 5. Compose Memory via MemoryFetcher callback (tags + description from
    //    the loaded bead row, no recursion through Show). See §8.3b.
    return nil, fmt.Errorf("beadstore: show: not implemented in spec sketch")
}

// Create takes CreateParams and returns the new *Bead (v15/v16 reshape).
// Wraps create + child rows in a single transaction.
func (s *SQLiteStore) Create(ctx context.Context, p CreateParams) (*protocol.Bead, error) {
    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil { return nil, err }
    defer tx.Rollback()

    id := p.ID
    if id == "" { id = generateBeadID(p.Type) } // see §8.4
    if p.Type == "" { p.Type = "task" }
    if p.Priority == 0 { p.Priority = 2 }
    now := time.Now().UTC().Format(time.RFC3339Nano)

    if _, err := tx.ExecContext(ctx,
        `INSERT INTO beads (id, title, type, priority, description,
            acceptance_criteria, parent_id, status, estimated_minutes,
            created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), 'open', ?, ?, ?)`,
        id, p.Title, p.Type, p.Priority, p.Description, p.AcceptanceCriteria,
        p.ParentID, p.EstimatedMinutes, now, now,
    ); err != nil {
        return nil, err
    }
    // Insert tags, labels, metadata if present.
    // Audit event.
    if _, err := tx.ExecContext(ctx,
        `INSERT INTO events (type, source, bead_id, payload, created_at)
         VALUES ('bead_created', 'beadstore', ?, ?, ?)`,
        id, fmt.Sprintf(`{"type":%q,"priority":%d}`, p.Type, p.Priority), now,
    ); err != nil {
        return nil, err
    }
    if err := tx.Commit(); err != nil { return nil, err }
    return s.Show(ctx, id) // round-trip to return the inserted row
}

// Close transitions a bead to closed within a transaction.
func (s *SQLiteStore) Close(ctx context.Context, id string, reason string) error {
    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil { return err }
    defer tx.Rollback()

    now := time.Now().UTC().Format(time.RFC3339)
    res, err := tx.ExecContext(ctx, `
        UPDATE beads SET status='closed', closed_at=?, close_reason=?, updated_at=?
        WHERE id=? AND deleted=0`,
        now, reason, now, id)
    if err != nil { return err }
    if affected, _ := res.RowsAffected(); affected == 0 {
        return fmt.Errorf("beadstore: bead %q not found or deleted", id)
    }
    if _, err := tx.ExecContext(ctx,
        `INSERT INTO events (type, source, bead_id, payload, created_at)
         VALUES ('bead_closed', 'beadstore', ?, ?, ?)`,
        id, fmt.Sprintf(`{"reason":%q}`, reason), now); err != nil {
        return err
    }
    return tx.Commit()
}

// Update applies non-nil fields from UpdateParams (v15/v16 reshape).
// Pointer-nil = "no change"; pointer-set = "apply this value." Empty-string
// pointers explicitly clear (e.g., ParentID=&"" clears parent_id).
// Fixes the bd zombie-defer-until bug: any move to status='open' clears
// deferred_until in the same statement.
func (s *SQLiteStore) Update(ctx context.Context, id string, p UpdateParams) error {
    if p.Status != nil && !validStatus(*p.Status) {
        return fmt.Errorf("beadstore: invalid status %q", *p.Status)
    }
    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil { return err }
    defer tx.Rollback()

    now := time.Now().UTC().Format(time.RFC3339Nano)
    // Build a dynamic SET clause from non-nil fields.
    // Special-case Status: open clears deferred_until in the same UPDATE.
    if p.Status != nil && *p.Status == "open" {
        if _, err := tx.ExecContext(ctx,
            `UPDATE beads SET status=?, deferred_until=NULL, updated_at=?
             WHERE id=? AND deleted=0`, *p.Status, now, id); err != nil { return err }
    } else if p.Status != nil {
        if _, err := tx.ExecContext(ctx,
            `UPDATE beads SET status=?, updated_at=?
             WHERE id=? AND deleted=0`, *p.Status, now, id); err != nil { return err }
    }
    // Other UpdateParams fields (Priority, Type, ParentID, Owner) handled here
    // via additional conditional UPDATE statements within the same transaction.

    // Audit event records only the fields that changed. Build the payload from
    // non-nil pointer fields. (v18 fix per codex round 9: the v15-v17 sketch
    // referenced an undefined `status` symbol; v18 uses the actual *p.Status.)
    payload := buildUpdatePayload(p) // helper that emits {"status":"x","priority":N,...}
    if _, err := tx.ExecContext(ctx,
        `INSERT INTO events (type, source, bead_id, payload, created_at)
         VALUES ('bead_updated', 'beadstore', ?, ?, ?)`,
        id, payload, now); err != nil {
        return err
    }
    return tx.Commit()
}

// (v15/v16: Sync method removed from the Store interface — was a bd no-op.)

// HasChildren reports whether the given epic has any children at all
// (open or closed). Used by the dispatcher to gate epic decomposition vs
// auto-close (pkg/dispatcher/dispatcher.go:3209,3570). v16 restores this
// after v15 incorrectly collapsed it into AllChildrenClosed.
func (s *SQLiteStore) HasChildren(ctx context.Context, epicID string) (bool, error) {
    var n int
    err := s.db.QueryRowContext(ctx,
        `SELECT COUNT(*) FROM beads WHERE parent_id=? AND deleted=0`,
        epicID).Scan(&n)
    if err != nil { return false, err }
    return n > 0, nil
}

// AllChildrenClosed reports whether every child of the given epic has status='closed'.
// Used by the dispatcher's epic auto-close gate.
func (s *SQLiteStore) AllChildrenClosed(ctx context.Context, epicID string) (bool, error) {
    var openCount int
    err := s.db.QueryRowContext(ctx,
        `SELECT COUNT(*) FROM beads
         WHERE parent_id = ? AND deleted = 0 AND status != 'closed'`,
        epicID).Scan(&openCount)
    if err != nil { return false, err }
    return openCount == 0, nil
}

func validStatus(s string) bool {
    return s == "open" || s == "in_progress" || s == "closed"
}

// scanBeads reads main rows + issues batched follow-up queries for
// tags/labels/metadata/deps joined by bead_id. Avoids N+1.
func (s *SQLiteStore) scanBeads(ctx context.Context, rows *sql.Rows) ([]protocol.Bead, error) {
    return nil, fmt.Errorf("beadstore: scanBeads not implemented in spec sketch")
}

// InProgress, Blocked, Closed, HasChildren, FindByParentAndTag, Export
// follow the same patterns. All match BeadSource signatures exactly.
```

**Transaction discipline (addresses adversarial review C4):** every state-changing method wraps its work in a single `BeginTx`/`Commit` block. Triggers fire within that transaction's scope; SQLite guarantees that a transaction commits atomically or rolls back atomically. The agent's specific concern that "triggers are not atomic" was technically incorrect for trigger-within-transaction semantics, but the underlying point is real: every multi-row write (e.g., creating an epic with N children, where each child INSERT also fires the parent-touch trigger) **must** be wrapped in a transaction. Methods that bulk-insert child rows (notes, tags, deps, etc. via future `oro bead dep add`-style commands) follow the same pattern. CI test: deliberately break a transaction mid-flight (kill the process between two child inserts), confirm both are rolled back.

Total file estimate: ~900–1,100 LOC including helpers, scanBeads batching, generateBeadID. `CLIBeadSource` is ~400 LOC. Larger because we're doing joins ourselves and `BeadDetail` assembly is more elaborate than parsing pre-formed JSON.

### 8.3b `Show()` assembly: the `MemoryFetcher` injection (v9 redesigned)

**v8 was wrong.** v8 designed `MemoryFetcher func(ctx, beadID string) (string, error)` and proposed binding it to a hypothetical `pkg/memory.Store.ForBead(ctx, beadID)`. Codex finding #8 (verified against actual code) showed the real `pkg/memory` API is `ForPrompt(ctx, store, beadTags, beadDesc, maxTokens)` at `pkg/memory/memory.go:1649`. A bead-id-only callback would either degrade memory relevance (the existing query relies on tags + description, not id) or recurse through `Show()` to fetch them — both bad.

**v9 redesigns to match the existing memory contract:**

```go
// pkg/beadstore/sqlite.go

// MemoryFetcher matches pkg/memory.ForPrompt's contract. Inputs are taken
// directly from the bead row already loaded by Show(); no re-query, no recursion.
// Returns the prompt-ready memory block, or "" if memory is unavailable.
type MemoryFetcher func(ctx context.Context, tags []string, description string, maxTokens int) (string, error)

type Option func(*SQLiteStore)

func WithMemoryFetcher(f MemoryFetcher) Option {
    return func(s *SQLiteStore) { s.memory = f }
}

func NewSQLiteStore(db *sql.DB, opts ...Option) *SQLiteStore {
    s := &SQLiteStore{db: db, memMaxTokens: 2000} // dispatcher-tunable default
    for _, o := range opts { o(s) }
    return s
}

// Show fetches the bead row + child rows first, then composes Memory using the
// already-loaded tags and description. No second SELECT, no recursion.
// v17 reshape per §8.2.1: returns *protocol.Bead; the unified type carries the
// 5 runtime fields (WorkerID, ContextPercent, etc.) with omitempty.
func (s *SQLiteStore) Show(ctx context.Context, id string) (*protocol.Bead, error) {
    bead, tags, deps, /* ... */, err := s.loadBeadComposite(ctx, id)
    if err != nil { return nil, err }
    if bead == nil { return nil, nil } // not found
    bead.Dependencies = deps
    // bead.Tags / bead.Labels populated by loadBeadComposite from child tables.

    // Fill runtime fields from assignments + events (when an active assignment exists).
    if a, _ := s.activeAssignment(ctx, id); a != nil {
        bead.WorkerID       = a.WorkerID
        bead.LastHeartbeat  = s.latestHeartbeat(ctx, id)
        bead.ContextPercent = s.latestContextPercent(ctx, id)
        bead.GitDiff        = s.computeGitDiff(a.Worktree)
    }

    if s.memory != nil {
        if m, err := s.memory(ctx, tags, bead.Description, s.memMaxTokens); err == nil {
            bead.Memory = m // log on err, leave Memory="" on failure
        }
    }
    return bead, nil
}
```

**Per-path injection (v9):**

| Path (per §8.8) | Memory callback wiring |
|---|---|
| **A: Dispatcher** | `beadstore.NewSQLiteStore(db, beadstore.WithMemoryFetcher(d.beadMemoryFetcher()))` where `d.beadMemoryFetcher()` returns `func(ctx, tags, desc, max) (string, error) { return memory.ForPrompt(ctx, d.memories, tags, desc, max) }`. v10 fix per codex round-2 #7: the `Dispatcher` field is `memories *memory.Store` (`pkg/dispatcher/dispatcher.go:424`), not `memoryStore`. **No new method on `pkg/memory.Store` is required** — v9/v10 use the existing `ForPrompt` directly. (v6–v8 incorrectly proposed adding `ForBead`; v9 retracted that.) |
| **B: `oro bead` CLI** | `beadstore.NewSQLiteStore(db)` (no callback) — CLI does not have memory access; `Show()` returns `Memory=""`. The CLI's `oro bead show` is an operator/debugging surface, not the worker-context-injection path. |
| **C: mg/data dashboard** | Constructed by the dispatcher with the same closure as Path A. `mgdata.NewSource(d.beads)` inherits `d.beads`'s already-injected fetcher. |

**Why v9 is correct vs v8:** the fetcher takes the *same arguments* `pkg/memory.ForPrompt` already takes, so the closure is one-line and the existing memory-relevance logic (which keys on tags + description) is used directly. No degradation, no recursion, no new public API.

**Phase 1 deliverable correction:** v6/v7/v8 listed `pkg/memory.Store.ForBead` as a Phase 1 new method. **v9 removes that line item.** No changes to `pkg/memory` are required by this spec.

**Acceptance test row** (Phase 1 inventory): `Show()` returns Memory="" when callback is nil; returns the callback's value when set; the callback receives the bead's tags and description as loaded by `Show()` (verified by a passthrough fake fetcher); survives callback-error gracefully (logs, returns Memory="").

### 8.4 Bead ID generation

`generateBeadID(beadType)` produces ids in bd's existing hierarchical format: `<project-prefix>-<base32-suffix>`, e.g., `oro-7nzy`. Algorithm:

1. Read the project prefix from `.oro/config.yaml` (`project_id` field).
2. Generate 4 random base32 characters (Crockford alphabet for legibility).
3. Verify uniqueness via `SELECT 1 FROM beads WHERE id=?`; retry on collision.
4. For epic children, callers may pass a parent id and the generator extends it dotwise: `mg-007` parent → `mg-007.1`, `mg-007.2`, etc. The first available numeric suffix is used.

Format compatibility with bd is mandatory because git branch names, tmux pane names, worktree paths, and event-log payloads all reference the id verbatim.

### 8.4 `protocol.Bead` struct

The `protocol.Bead` struct exists today and v15/v16 *extends* its shape (per §8.2.1) rather than leaving it untouched. The extension absorbs 5 runtime fields from `BeadDetail` (`WorkerID`, `ContextPercent`, `LastHeartbeat`, `GitDiff`, `Memory`) with `omitempty` JSON tags so the wire shape for zero-valued reads is unchanged. If any field used by `CLIBeadSource` isn't currently persisted to oro's tables, the migration adds the necessary columns/joins. Field-by-field check is part of Phase 1 acceptance.

### 8.5 Selection at startup

In dispatcher initialization:

```go
// In pkg/dispatcher/dispatcher.go (or wherever Dispatcher.beads is constructed):
import "github.com/<oro-module>/pkg/beadstore"

func selectStore(cfg Config, db *sql.DB) (beadstore.Store, error) {
    switch cfg.BeadSourceMode {
    case "sqlite", "":              // default after cutover
        return beadstore.NewSQLiteStore(db), nil
    case "cli":                      // legacy path
        return NewCLIBeadSource(...), nil // still implements beadstore.Store
    case "shadow":                   // read-only validation
        return beadstore.NewShadowStore(
            NewCLIBeadSource(...),       // primary (authoritative)
            beadstore.NewSQLiteStore(db),// secondary (validation only)
        ), nil
    default:
        return nil, fmt.Errorf("unknown BeadSourceMode: %q", cfg.BeadSourceMode)
    }
}
```

`CLIBeadSource` (in `pkg/dispatcher/beadsource.go`) gets a single trivial change: it must satisfy `beadstore.Store`'s value-slice signature. Today's CLIBeadSource already returns value slices — confirm in the audit (§11.1) and adjust if not.

### 8.6 `ShadowStore` — read-only parallel validation

The v1 spec implied a dual-write design ("dispatcher mirrors writes to SQLite via a write-through layer"). v2 explicitly rejects this. Shadow mode is **read-only**: bd remains authoritative for both reads and writes during the shadow window. SQLite is populated once by the migration tool and goes stale during the week. At cutover, a final reconcile catches the drift.

Why: dual-writes need deterministic translation, retry behavior on partial failure (bd succeeds, SQLite fails or vice versa), reconciliation, and handling of external bd writes (humans running `bd update`) that bypass the write-through. Each is a load-bearing piece of complexity. Read-only shadow eliminates all of them at the cost of a single reconcile pass at cutover.

```go
// pkg/beadstore/shadow.go
type ShadowStore struct {
    primary   Store         // bd-backed, authoritative for reads and writes
    secondary Store         // sqlite-backed, validation only — never written to here
    logger    *slog.Logger
}

func NewShadowStore(primary, secondary Store, log *slog.Logger) *ShadowStore {
    return &ShadowStore{primary: primary, secondary: secondary, logger: log}
}

// Ready: read both, compare, return primary's result.
func (s *ShadowStore) Ready(ctx context.Context) ([]protocol.Bead, error) {
    p, errP := s.primary.Ready(ctx)
    sec, errS := s.secondary.Ready(ctx)
    s.compare("Ready", p, errP, sec, errS)
    return p, errP
}

// Show, InProgress, Blocked, Closed, HasChildren, FindByParentAndTag, Export
// follow the same dual-read pattern.

// Close, Update, Create — DO NOT MIRROR. Forwarded to primary only.
// Secondary is intentionally allowed to drift; reconcile at cutover.
// Signatures match v16's reshaped Store (§8.2).
func (s *ShadowStore) Close(ctx context.Context, id, reason string) error {
    return s.primary.Close(ctx, id, reason)
}

func (s *ShadowStore) Update(ctx context.Context, id string, p UpdateParams) error {
    return s.primary.Update(ctx, id, p)
}

func (s *ShadowStore) Create(ctx context.Context, p CreateParams) (*protocol.Bead, error) {
    return s.primary.Create(ctx, p)
}

// HasChildren and AllChildrenClosed forward to primary (read-only path).
func (s *ShadowStore) HasChildren(ctx context.Context, epicID string) (bool, error) {
    return s.primary.HasChildren(ctx, epicID)
}

// (No Sync method — dropped in v15.)

// compare partitions divergences into "real" (logged toward the cutover gate)
// and "expected drift" (visibility-only). See §9.4 step 3 for the classification
// rules. ShadowStartedAt is the timestamp at which shadow mode began; beads
// created/updated after that are tolerated drift.
func (s *ShadowStore) compare(op string, primary []protocol.Bead, errP error,
                              secondary []protocol.Bead, errS error) {
    if errP != nil || errS != nil {
        s.event("beadstore_divergence", op, "real", "error",
            "errP", errP, "errS", errS)
        return
    }
    real, drift := classify(primary, secondary, s.shadowStartedAt)
    if real {
        s.event("beadstore_divergence", op, "real", "read result mismatch",
            "primary_n", len(primary), "secondary_n", len(secondary))
    }
    if drift {
        s.event("beadstore_divergence", op, "drift", "expected shadow drift",
            "primary_n", len(primary), "secondary_n", len(secondary))
    }
}

// classify returns (anyReal, anyDrift). v9 keys on UPDATED_AT, not CREATED_AT.
// v11 (per codex round-3 #1): timestamps are parsed before compare, never
// string-compared. protocol.Bead.UpdatedAt is a string; bd may emit RFC3339
// with or without fractional seconds, with offset or Z. parseTS handles both.
//
// Beads in primary but not secondary: drift if primary.updated_at >= shadowStartedAt
// (likely a status change written to bd during shadow); else real (SQLiteStore
// missed an import).
// Beads in both with content differences: drift if primary.updated_at >= shadowStartedAt
// AND parseTS(primary.updated_at).After(parseTS(secondary.updated_at)); else real.
// Beads with parseTS-equal updated_at but different content: ALWAYS real,
// regardless of timestamp position relative to shadowStartedAt.
//
// v13 per codex round-5 #4: equality is via `t1.Equal(t2)`, NOT `t1 == t2`.
// time.Time values produced by time.Parse with different zones (offset vs Z)
// can represent the same instant; `==` compares wall+monotonic+location bytes
// and rejects equality. `.Equal()` compares the absolute time only.
func classify(primary, secondary []protocol.Bead, shadowStart time.Time) (bool, bool) {
    // ... see implementation in pkg/beadstore/shadow.go
    return false, false
}

// parseTS handles both RFC3339Nano (with fractional seconds, with or without Z/offset)
// and RFC3339 (no fractional). Returns zero time on parse failure (which sorts
// before all valid times). Used by classify() and reconcile.
func parseTS(s string) time.Time {
    if t, err := time.Parse(time.RFC3339Nano, s); err == nil { return t }
    if t, err := time.Parse(time.RFC3339, s); err == nil { return t }
    return time.Time{}
}

// ShadowStore persists shadowStartedAt to survive dispatcher restarts.
// Without persistence (v6 bug), restart resets the timestamp to "now" and
// every pre-restart drift becomes a "real divergence" on classification —
// gate fails for design-correct behavior. v7 fix: persist + recover.
//
// Storage: kv_store row (key='beadstore_shadow_started_at', value=RFC3339).
// On ShadowStore construction: read kv_store; if present, reuse (no expiry).
// If absent, write current time and use that. There is no automatic expiry —
// the timestamp persists across dispatcher restarts indefinitely. Operator
// resets it explicitly via `oro bead admin reset-shadow-window` when
// intentionally restarting validation; this is the only path that clears it.
func NewShadowStore(primary, secondary Store, db *sql.DB, log *slog.Logger) (*ShadowStore, error) {
    started, err := loadOrInitShadowStart(db)
    if err != nil { return nil, err }
    return &ShadowStore{primary: primary, secondary: secondary,
                       shadowStartedAt: started, db: db, logger: log}, nil
}

func loadOrInitShadowStart(db *sql.DB) (time.Time, error) {
    var s string
    err := db.QueryRow(
        `SELECT value FROM kv_store WHERE key='beadstore_shadow_started_at'`,
    ).Scan(&s)
    if err == nil {
        if t, perr := time.Parse(time.RFC3339, s); perr == nil { return t, nil }
    }
    now := time.Now().UTC()
    _, _ = db.Exec(
        `INSERT OR REPLACE INTO kv_store (key, value, updated_at)
         VALUES ('beadstore_shadow_started_at', ?, ?)`,
        now.Format(time.RFC3339), now.Format(time.RFC3339))
    return now, nil
}
```

Trade-off captured explicitly: shadow mode validates *reads* (the dependency query, FTS, parent/child traversal — the parts with non-trivial logic). Writes are validated by unit tests against `SQLiteStore`, not by shadow comparison. If end-to-end write-equivalence is critical we add an offline replay tool that takes a recorded sequence of `bd update/close/create` calls and re-runs them against a SQLite copy, diffing the result. That's a post-cutover follow-up, not part of v2 critical-path.

### 8.7 mg/data layer

`pkg/mg/data/source.go` and `mutate.go` currently shell out to bd directly. After migration, both files take a `beadstore.Store` via constructor and route through it. This consolidates the duplicate write path. Estimate: **4–5 days**, not 2 — the dashboard rendering has tight coupling to the existing `data.Issue` shape and the JSON-decoding contract. Several call sites read fields that today come from bd's JSON (`labels`, `metadata`) and assume specific empty-vs-nil semantics; matching those exactly is fiddly.

### 8.8 Concrete wiring (initialization paths)

The v4 adversarial review correctly flagged that v3/v4 hand-waved how each consumer obtains its `*sql.DB` and `beadstore.Store`. v5 specifies the three call paths:

**Path A — Dispatcher init** (`pkg/dispatcher/dispatcher.go`):

```go
// Existing init flow (simplified):
func NewDispatcher(cfg Config) (*Dispatcher, error) {
    paths, err := cmdoro.ResolveProjectPaths(...)
    if err != nil { return nil, err }
    db, err := dbutil.OpenDB(paths.StateDBPath)              // existing
    if err != nil { return nil, err }
    if err := protocol.MigrateAll(ctx, db); err != nil {     // existing + migration #11
        return nil, err
    }
    store, err := selectStore(cfg, db, runner)                // NEW
    if err != nil { return nil, err }
    return &Dispatcher{db: db, beads: store, ...}, nil
}

// selectStore picks an implementation per ORO_BEADSOURCE_MODE.
func selectStore(cfg Config, db *sql.DB, runner CommandRunner) (beadstore.Store, error) {
    switch cfg.BeadSourceMode {
    case "sqlite", "":
        return beadstore.NewSQLiteStore(db), nil
    case "cli":
        return NewCLIBeadSource(runner), nil // legacy
    case "shadow":
        return beadstore.NewShadowStore(
            NewCLIBeadSource(runner),
            beadstore.NewSQLiteStore(db),
            slog.Default(),
        ), nil
    default:
        return nil, fmt.Errorf("unknown ORO_BEADSOURCE_MODE: %q", cfg.BeadSourceMode)
    }
}
```

**Path B — `oro bead` CLI** (`cmd/oro/cmd_bead.go`):

```go
// CLI subcommands take a Cobra context; they construct their own db and Store.
// They do NOT import pkg/dispatcher.
func newBeadCmd() *cobra.Command {
    return &cobra.Command{
        Use: "bead",
        PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
            paths, err := ResolveProjectPaths(...)
            if err != nil { return err }
            db, err := openDB(paths.StateDBPath)              // existing helper, delegates to dbutil.OpenDB
            if err != nil { return err }
            if err := protocol.MigrateAll(cmd.Context(), db); err != nil {
                return err
            }
            cmd.SetContext(beadstoreCtx.WithStore(cmd.Context(),
                beadstore.NewSQLiteStore(db)))
            return nil
        },
        // ... subcommands fetch the Store from context and use it
    }
}
```

**Path C — mg/data dashboard** (`pkg/mg/data/source.go`):

```go
// The dashboard takes a Store via constructor; tests inject FakeStore.
type Source struct {
    store beadstore.Store
}

func NewSource(store beadstore.Store) *Source { return &Source{store: store} }

// In production, the dispatcher constructs the dashboard with its own Store:
//   dashboard := mgdata.NewSource(d.beads)
// In tests:
//   dashboard := mgdata.NewSource(beadstore.NewFakeStore())
```

**Why three separate db connections (one per process):** the dispatcher process and the `oro bead` CLI subprocess each open their own `*sql.DB` against the same `state.db` file. SQLite WAL coordinates them. Workers running `oro bead create` open a third connection (the CLI subprocess they spawn), and so on. There is no shared connection pool across processes; that's normal for SQLite and is exactly what WAL is designed for.

**Concurrency invariant test:** the acceptance test (§14.5) includes a check that 10 concurrent worker subprocesses each calling `oro bead create` produce 10 distinct beads with no collisions and no dropped writes. This exercises the cross-process WAL discipline end-to-end.

---

## 9. Migration Plan (from Dolt)

### 9.1 The migration tool

A new subcommand `oro bead migrate-from-dolt` that:

1. Reads bd's full state via `bd export` (uses bd as it exists today; one-time use).
2. Parses the JSONL output.
3. For each row, upserts into the new `beads` table.
4. For each dependency in the row, upserts into `bead_deps`.
5. For each tag/label/metadata/note, populates the corresponding table.
6. Logs row counts and any malformed rows to stderr.
7. Writes a mandatory source backup snapshot to `OroHome/migrations/<timestamp>-pre-migration.jsonl` before the first import write.
8. Verifies counts post-migration.

Initial migration backup is mandatory on apply and automatic. Dry-run skips all SQLite writes and migration-lock acquisition, so it also skips the backup write.

Pseudocode:

```go
func MigrateFromDolt(ctx context.Context, store *beadstore.SQLiteStore, doltExport []byte) (*MigrationReport, error) {
    report := &MigrationReport{Started: time.Now()}
    tx, err := store.BeginTx(ctx, nil) // SQLiteStore exposes BeginTx for migration tooling
    if err != nil { return nil, err }
    defer tx.Rollback()

    scanner := bufio.NewScanner(bytes.NewReader(doltExport))
    for scanner.Scan() {
        var bd BdBeadExport
        if err := json.Unmarshal(scanner.Bytes(), &bd); err != nil {
            report.Errors = append(report.Errors, fmt.Errorf("parse: %w", err))
            continue
        }
        if err := upsertBead(ctx, tx, &bd); err != nil {
            report.Errors = append(report.Errors, fmt.Errorf("upsert %s: %w", bd.ID, err))
            continue
        }
        report.Imported++
    }
    if err := scanner.Err(); err != nil { return report, err }

    if err := tx.Commit(); err != nil { return report, err }

    report.Finished = time.Now()
    return report, nil
}
```

### 9.2 Edge cases the migration tool handles

- **AC embedded in description markdown** (v15 normalize, v16 contract-tightened): extract the `## Acceptance Criteria` section into the dedicated `acceptance_criteria` column **and strip the section (header + body up to the next H2 header or end-of-string) from `description`** — preserving everything before the AC header and everything from the next H2 onward. v15 said "use the existing `extractACFromDescription()` logic" — but codex round 7 #3 correctly observed that the existing function returns AC text only, not section offsets. Naive strip-from-header-to-end would silently delete subsequent sections. **v16 fix:** Phase 3 adds a new `extractAndStripAC(description string) (ac, descWithoutAC string, err error)` helper to the migration tool. Contract:
  - If `## Acceptance Criteria` not present: returns `("", description, nil)`.
  - If present: AC = the body between that header and the next H2 header (or EOF); `descWithoutAC` = original with `## Acceptance Criteria\n<body>` removed *and exactly one trailing newline preserved* between the prior content and the next H2 header (if any).
  - If multiple `## Acceptance Criteria` headers (malformed): only the first is extracted and stripped; subsequent ones logged to the migration error report and left in `description` for operator review.
  - Acceptance test in `pkg/beadstore/migrate_test.go`: 8 fixtures covering AC at start / mid / end of description, AC followed by other H2 sections, malformed nested headers, no AC at all, AC with sub-headers (H3) inside its body. Each asserts both `ac` and `descWithoutAC` round-trip correctly.
- **Numeric vs string priorities**: bd uses int; we keep int.
- **Status normalization**: bd may produce `pending` or `to-do` historically; map to `open`. bd `blocked` is stored as native `blocked` so manual/imported blocked rows survive migration. bd `deferred` is logged and stored as native `open` with `deferred_until` preserved.
- **Deferred-until handling**: if bd's status is `open` or `deferred` and `defer_until`/`deferred_until` is set, we set `status='open'` and `deferred_until=<value>`. If bd exports `status='deferred'` without a defer timestamp, the migration stores a far-future `deferred_until` sentinel and warns rather than making the bead immediately ready. The view `beads_ready` excludes deferred beads correctly.
- **Parent IDs that haven't been imported yet**: deferred FK enforcement. We do a two-pass import (insert all rows, then add FKs).
- **Soft-deleted beads**: bd may keep tombstones; we set `deleted=1` for them and skip in views.

### 9.2b Partial dolt corruption pre-flight (addresses adversarial review C5)

A subtle and dangerous failure mode: dolt is *partially* corrupted. `bd export` succeeds but returns fewer rows than dolt actually contains (it stops at the first unreadable row, silently). The migration's row-count parity check (§9.6) compares a configured-source `SELECT COUNT(*) FROM issues` against `bd export | jq -s length` — both are 1,100, but dolt actually has 1,200. The 100 missing beads are silently lost.

**Pre-flight gate.** Before any migration runs, the tool independently verifies dolt's internal row count via dolt's own metadata, not via `bd export`:

```
$ oro bead migrate-from-dolt --dry-run
[oro] Pre-flight: querying dolt directly...
[oro] dolt internal count: 1,200 beads
[oro] bd export count:     1,100 beads
[oro] MISMATCH: dolt has 100 more beads than bd export returned.
       This indicates partial dolt corruption.
       Run `dolt fsck` against .beads/dolt/ to investigate.
       Override with --force-recover to migrate the 1,100 readable beads
       (acknowledging 100 beads are unrecoverable).
[oro] Aborting.
```

If `dolt fsck` reports clean and the export is still short, that's a bd-side bug, not corruption. Either way, the operator decides explicitly via `--force-recover`. **No silent data loss.**

The pre-flight queries dolt by spawning `dolt sql` against the configured source and running `SELECT COUNT(*) FROM issues;`. Server-mode projects select the host, port, and `dolt_database` from `.beads/metadata.json`; local projects use the resolved project `BeadsDir/dolt` as the Dolt data directory. If dolt itself fails to start (terminal corruption), the migration falls back to the JSONL path (§9.9) with the same explicit-acknowledgment requirement.

**Resolved real-data blocker (2026-04-29 audit, fixed 2026-04-30):** the observed preflight output was:

```
bd export count: 1718
dolt internal count error: ... no database selected
Aborting.
```

The blocker was fixed by selecting the configured Dolt database and counting `issues`. Real migration must not run unless `./oro bead migrate-from-dolt --dry-run` passes without `--force-recover`. A dry-run that needs `--force-recover`, cannot query dolt's internal count, or reports any count mismatch is a migration-day blocker, not an operator-warning-only condition.

**Phase 0 deliverable** captures the dolt internal count alongside the bd export count, so any drift between Phase 0 audit and Phase 8 migration day is also visible.

### 9.3 The migration UX

```
$ oro bead migrate-from-dolt --dry-run
[oro] Reading dolt export via bd...
[oro] Found 1,247 beads.
[oro] Would import 1,247 beads, 3,109 dependencies, 412 tags, 891 metadata entries.
[oro] DRY RUN — no writes performed.

$ oro bead migrate-from-dolt
[oro] Reading dolt export via bd...
[oro] Found 1,247 beads.
[oro] Backing up to OroHome/migrations/2026-04-27T15-04-03-pre-migration.jsonl
[oro] Importing... 1,247/1,247 ✓
[oro] Verifying... ready=18, in_progress=2, closed=1227, ✓
[oro] Migration complete in 3.1s.
[oro] Next step: run the native validation gate, then set ORO_BEADSOURCE_MODE=sqlite.
```

### 9.4 The cutover sequence

This is the critical section. The full sequence:

1. **Day 0 (post-merge of new code, default still `cli`):**
   - `pkg/beadstore` and `SQLiteStore` are shipped but inactive (`ORO_BEADSOURCE_MODE=cli`).
   - Dolt + bd remain authoritative.
   - Operators verify the build, smoke-test `oro bead` subcommands manually against an empty `beads` table.

2. **Day 1 (migration):**
   - Stop the dispatcher and every worker. Run the safe gate sequence from
     `docs/runbooks/beadstore-native-cutover.md`: `scripts/check-bd-version.sh`;
     dispatcher count check covering both `oro start` and `oro dispatcher start`;
     worker/any direct-`bd` process/direct-native-`oro bead`
     mutator/other-migration process check; fail-closed `ORO_BEADSOURCE_MODE`
     check requiring empty or `cli`; `./oro bead migrate-from-dolt --help`;
     `./oro bead migrate-from-dolt --dry-run`.
   - Confirm the dry-run passes without `--force-recover`. If it does not, stop. Do not run real migration.
   - Create and integrity-check a pre-migration `state.db` SQLite backup snapshot per `docs/runbooks/beadstore-native-cutover.md`.
   - Run `oro bead migrate-from-dolt`.
   - Verify counts (`oro bead status`).
   - Run the native validation gate from
     `docs/runbooks/beadstore-native-cutover.md`: native `ready`, `blocked`,
     `show`, controlled create/close smoke bead, and SQLite integrity checks.
   - Set `ORO_BEADSOURCE_MODE=sqlite`.
   - Restart the dispatcher and every worker from that environment with the
     executable runbook sequence: record the old PID from `oro.pid`, build a
     stripped `PATH` that resolves the reviewed `oro` binary, `claude`, and
     `git` but not `bd`, run `ORO_HUMAN_CONFIRMED=1 ./oro stop --force`, then
     run `PATH="$cutover_path" ORO_BEADSOURCE_MODE=sqlite "$oro_bin"
     dispatcher start --force --workers <count>`.
   - Verify the restarted dispatcher inherited `ORO_BEADSOURCE_MODE=sqlite`.
   - Verify worker subprocesses have `bd` stripped from `PATH`, still have `oro`
     available, and that each controlled test bead assigned after recording the
     worker log byte offset appears in the new log segment with native
     `oro bead` commands and no `bd` command invocation.

3. **Cutover (same maintenance window):**
   - SQLite is validated as the authority directly. bd/Dolt is the import source,
     audit trail, and rollback reference, not the cutover veto.
   - Stop if native validation fails. Fix the native store or restore the
     recorded SQLite backup only if data corruption is proven.
   - Proceed with `ORO_BEADSOURCE_MODE=sqlite` once native validation passes.

4. **Post-cutover:**
   - Restart dispatcher.
   - SQLite is now authoritative. bd is no longer queried.
   - Worker subprocesses continue with the Phase 8 `bd` PATH strip (v15 no-shim);
     the dispatcher retains bd on its own PATH only for migration audit/recovery
     tooling until Phase 10.

5. **Days 1–30 (sqlite primary, bd binary still installed):**
   - All bead state lives in SQLite.
   - Workers are using `oro bead` via prompts (v15: no shim — see §7.3).
   - Nightly: `oro bead export --out=.beads/snapshots/$(date +%F).jsonl` for backup.
   - Operator verifies no anomalies for 4 weeks.

6. **Day 30+ (cleanup):**
   - Delete `cmd/oro/cmd_dolt.go`, `cmd/oro/cmd_bd.go`, `pkg/dispatcher/dolt_recovery.go`, `pkg/dispatcher/cli_beadsource.go` (and rename `pkg/dispatcher/beadsource.go`'s interface file as needed).
   - Remove `bd` from `oro init` tool list (`cmd/oro/cmd_init.go:76`).
   - (v15 has no bd-shim; this v14 cleanup item is N/A.)
   - Update `docs/INSTALL.md` and `README.md` to drop bd as a dependency.
   - Optionally: archive `~/.oro/projects/<hash>/.beads/dolt/` directories.

### 9.5 Reversibility

Until Day 30, the recorded SQLite backups and JSONL source backups preserve a
recovery path when the runbook gates are followed. `ORO_BEADSOURCE_MODE=cli`
reverts to bd only after bd has been refreshed from SQLite or an explicit
data-loss decision is recorded. The dolt database is preserved as an audit and
fallback source. The migration tool's JSONL backup file lives at
`OroHome/migrations/<ts>-pre-migration.jsonl`; rollback of a failed SQLite
target uses the operator's pre-migration `state.db` SQLite backup snapshot, not
JSONL import.

After Day 30 cleanup: rollback requires re-cloning bd, restoring dolt from a backup, and reverting the deletions. Plan accordingly — don't do Day 30 cleanup until you're confident.

### 9.6 Field-by-field bd → SQLite mapping (the migration contract)

Every field bd persists must have an explicit destination in oro's schema, or be explicitly dropped with rationale. No silent drops. This table is the migration contract:

| bd field (in `bd export` JSON) | SQLite destination | Notes |
|---|---|---|
| `id` | `beads.id` | preserve hierarchical format ("oro-7nzy", "mg-007.2.1") |
| `title` | `beads.title` | NOT NULL |
| `description` | `beads.description` + AC extraction | extract `## Acceptance Criteria` markdown to `acceptance_criteria` column; remaining markdown stays in `description` |
| `acceptance_criteria` (separate field if present) | `beads.acceptance_criteria` | takes precedence over markdown extraction |
| `status` | `beads.status` | normalize `pending`/`to-do` → `open`; reject other unknown values to errors slice |
| `priority` | `beads.priority` | int; default 2 if missing |
| `type` | `beads.type` | string; preserve `task`, `bug`, `epic`, `research`, `chore`; default `task` |
| `parent` / `parent_id` | `beads.parent_id` | resolved second-pass after all rows inserted |
| `owner` | `beads.owner` | nullable |
| `estimated_minutes` | `beads.estimated_minutes` | nullable int |
| `tier` | `beads.tier` | nullable |
| `model` | `beads.model` | nullable; bd may store this in metadata — extract from there if not top-level |
| `created_at` | `beads.created_at` | RFC3339; bd uses ISO8601, normalize |
| `updated_at` | `beads.updated_at` | preserved verbatim — used by `--reconcile` last-writer-wins |
| `closed_at` | `beads.closed_at` | nullable |
| `close_reason` | `beads.close_reason` | nullable |
| `deferred_until` / `defer_until` | `beads.deferred_until` | bd uses both names — accept either |
| `dependencies` (array of `{depends_on_id, type, ...}`) | `bead_deps` rows | one row per dep; preserve `type` (blocks, conditional-blocks, related-to, etc.) |
| `tags` (array) | `bead_tags` rows | one row per tag |
| `labels` (array) | `bead_labels` rows | distinct from tags by bd convention; preserve |
| `metadata` (object) | `bead_metadata` rows | one row per key-value pair |
| `notes` (string or array) | `bead_notes` rows | if string: single row with `author='migration', created_at=<bead.updated_at>`. If array of `{author, content, created_at}`: one row each |
| `assignee` (legacy alias for `owner`) | `beads.owner` | only if `owner` itself is empty |
| **Unknown fields** | **logged to migration report stderr** | NOT silently dropped |

**Fields explicitly NOT migrated** (because they are not bd-persisted state — they are runtime/telemetry assembled at `Show()` time per §8.3):

| BeadDetail field | Source | Why not migrated |
|---|---|---|
| `WorkerID` | oro `assignments` table (already in state.db) | runtime; populated by the dispatcher when assigning |
| `ContextPercent` | latest `context` event in `events` table | per-turn telemetry; ephemeral |
| `LastHeartbeat` | latest `heartbeat` event in `events` table | live process state |
| `GitDiff` | computed from worktree filesystem | derived at request time |
| `Memory` | `MemoryFetcher(ctx, tags, desc, max)` callback → `memory.ForPrompt(...)` | a separate subsystem with its own lifecycle; closure injected per §8.3b |

These fields are assembled by `SQLiteStore.Show()` from existing oro tables and live data, not migrated from bd. The v4 adversarial review flagged them as "missing from the migration mapping" — which is correct that they're missing, but the framing was wrong: they were never bd-persisted, so they have nothing to migrate. v5 documents this explicitly to remove the ambiguity.

**Two-pass execution (v10 — corrected per codex round-2 #4):**

The naive "insert beads then insert children" approach silently corrupts `beads.updated_at`. Inserting child rows fires the parent-touch triggers (§9.7), which overwrite `beads.updated_at` with import-time. The migration is supposed to preserve bd's `updated_at` verbatim — without this fix, every migrated bead would post-migration appear newer than bd, causing reconcile to skip real bd-side updates at cutover.

**Mitigation:** the migration tool wraps the entire import in a `DROP TRIGGER ... ; <import> ; CREATE TRIGGER ...` envelope. With user-defined triggers temporarily absent, child INSERTs don't touch the parent row. After all rows are loaded, the triggers are re-created from the schema-migration DDL.

```sql
-- Pseudocode for the migration transaction:
BEGIN IMMEDIATE;

-- 1. Drop the parent-touch triggers (idempotent if absent).
DROP TRIGGER IF EXISTS bead_deps_touch_parent_ai;
DROP TRIGGER IF EXISTS bead_deps_touch_parent_au;
DROP TRIGGER IF EXISTS bead_deps_touch_parent_ad;
-- ...repeat for tags, labels, metadata, notes (15 triggers total)...

-- 2. Pass 1: insert all `beads` rows with parent_id=NULL, including the verbatim
--    bd-supplied created_at and updated_at values. Insert child rows.
INSERT INTO beads (id, title, ..., created_at, updated_at, ...) VALUES (?, ?, ..., ?, ?, ...);
-- ...etc...

-- 3. Pass 2: resolve parent_id, insert bead_deps, validate orphans.
UPDATE beads SET parent_id=? WHERE id=?;
INSERT INTO bead_deps (...) VALUES (...);

-- 4. Re-create the parent-touch triggers from canonical schema DDL.
CREATE TRIGGER bead_deps_touch_parent_ai AFTER INSERT ON bead_deps ...;
-- ...etc...

COMMIT;
```

**Why this is correct:** the entire envelope is one transaction. If any step fails, rollback restores both the data state *and* the trigger definitions. Post-migration, the triggers are present exactly as the schema migration #11 declared them.

**Acceptance test row** (Phase 3 inventory): post-migration, pick 5 beads where bd's `updated_at` differs from "now" by >1 hour; assert `SELECT updated_at FROM beads WHERE id=?` returns bd's verbatim value, not import-time. Without the trigger envelope, this test fails on every bead.

Pre-v10 alternative considered and rejected: `INSERT OR REPLACE INTO beads(..., updated_at)` as the *final* statement per bead, intended to override the trigger-corrupted value. Rejected because it requires per-bead scripting (5× the SQL volume), and a re-import bug could leave the corrupted value in place if the override INSERT is skipped or reordered. The DROP TRIGGER envelope is a single, transactional, all-or-nothing approach.

**Verification (mandatory, post-import):**
- **Live row count parity:** `SELECT COUNT(*) FROM beads WHERE deleted=0` matches `bd export | jq -s 'length'`. v9 fix per codex finding #6: the v8 spec used unfiltered `COUNT(*)` which would have included soft-deleted tombstones and either always passed or always failed depending on intent. `deleted=0` is the only correct comparison against bd's live-bead count.
- **Tombstones surfaced separately:** `SELECT COUNT(*) FROM beads WHERE deleted=1` reported in the migration report (visible but not part of the parity assertion).
- Sum of dependency counts: `SELECT COUNT(*) FROM bead_deps` matches sum of `len(dependencies)` across bd export.
- Spot-check 10 random beads field-by-field against `bd show <id> --json`.
- Run the extraction sanity test: pick 5 beads with `## Acceptance Criteria` in their description; confirm AC text round-trips.

**Real-data dry-run (replaces v2's synthetic 100-bead fixture):** Phase 3 acceptance is now: `oro bead migrate-from-dolt --dry-run` against a recent snapshot of the *production* dolt database (sanitized of any sensitive metadata if needed; the migration runs against a copy at `/tmp/oro-migration-test-<ts>/`). Reports row counts, errors, unknown-field log. Reviewed by one engineer before Phase 8.

### 9.7 `updated_at` maintenance contract

Reconcile depends on `beads.updated_at` being a faithful "last meaningful change to this bead's data" timestamp. Writes that touch only *child* tables (deps, tags, labels, metadata, notes) — not `beads` directly — must still bump the parent's `updated_at`. Storage-layer triggers enforce this.

**Coverage** (canonical DDL in Appendix B; this section is the contract narrative):

- **Five child tables:** `bead_deps`, `bead_tags`, `bead_labels`, `bead_metadata`, `bead_notes`.
- **Three triggers per table:** AFTER INSERT, AFTER UPDATE (with `WHEN` no-op guards per v10/v11 — see Appendix B), AFTER DELETE.
- **Total: 15 parent-touch triggers**, plus the 3 FTS5 triggers on `beads` (AI/AD/AU) for full-text-search index maintenance.

**Why `WHEN` guards on AU:** SQLite's `AFTER UPDATE` fires even on no-op rewrites (`UPDATE bead_tags SET tag=tag`). Without the guard, parent `updated_at` would advance for non-changes, corrupting LWW reconcile. v11 (codex round-3 #6) extends each AU `WHEN` clause to compare *every* non-PK column on the child table, so no meaningful field-edit goes undetected.

**Why triggers, not application code:** application paths (`oro bead dep add`, etc.) could forget to bump `updated_at`. Triggers enforce the invariant at the storage layer.

**CI tests:**
- Insert a bead, sleep 1s, insert a dep — assert `beads.updated_at > beads.created_at`.
- For each child table: insert a row, then `UPDATE child SET <pk-col>=<pk-col>` (no-op) — assert `beads.updated_at` did *not* advance.
- For each child table: insert a row, then `UPDATE child SET <non-pk-col>=<new>` — assert `beads.updated_at` advanced.

The previous v8/v9 description of this section ("Six triggers total. INSERT + DELETE only. Four child tables.") was stale once v10 added AU triggers and v9 added the fifth child table. v11 corrects the prose to match Appendix B.

### 9.8 `--reconcile` semantics (load-bearing)

The shadow window is read-only on the SQLite side, so SQLite drifts behind bd by however many `bd close`/`bd update`/`bd create` calls happened during the week. `--reconcile` closes that gap. Algorithm:

1. **Re-export bd** to a fresh JSONL: `bd export > /tmp/reconcile-<ts>.jsonl`.
2. **Read the SQLite side** via `oro bead export` to a sibling JSONL.
3. **For each bead id in the bd export:** comparisons use **`datetime(bd_updated_at)` vs `datetime(sqlite_updated_at)`** — wrapping in SQLite's `datetime()` parses both RFC3339-with-fractional and RFC3339-without-fractional and produces canonical comparable values. v10 fix per codex round-2 #3: lex `>` between `'2026-04-27T15:04:03Z'` and `'2026-04-27T15:04:03.000Z'` is wrong; `datetime()` normalizes both to `'2026-04-27 15:04:03'` (or with fractional, depending on input).
   - If absent from SQLite → INSERT (creation during shadow window).
   - If present and `datetime(bd.updated_at) > datetime(sqlite.updated_at)` → UPDATE all fields, refresh deps/tags/metadata.
   - If present and `datetime(bd.updated_at) < datetime(sqlite.updated_at)` → no-op (SQLite is newer; should not happen during read-only shadow but tolerate gracefully).
   - If present and **timestamps tie** under `datetime()` (equal to within 1s) → **field-by-field comparison** (addresses adversarial review C3b). Any single field that differs surfaces as a conflict in step 6. Do NOT silently no-op on tied timestamps.
4. **For each bead id in SQLite but not in bd → soft-delete** (`deleted=1`). This catches beads removed via `bd` during the window.
5. **Verify**: post-reconcile counts match `bd export | jq -s length`.
6. **Surface conflicts**: any bead whose dolt vs sqlite content differs by something other than timestamps (e.g., independent edits to the same field) is logged to stderr with both versions. The operator inspects and decides; default is bd-wins because bd was authoritative.
7. **Idempotent**: re-running `--reconcile` twice produces zero net changes.

`--reconcile` requires `--apply` to actually mutate. Without `--apply`, it prints what it would do (a dry-run for reconcile specifically). This is mandatory: a buggy reconcile pass touching production data without explicit consent is the worst-case failure mode of this whole project.

If both `--dry-run` and `--apply` are present, `--dry-run` wins. `oro bead migrate-from-dolt --dry-run --reconcile --apply` must not apply changes.

```bash
$ oro bead migrate-from-dolt --reconcile
[oro] Reading bd export... 1,256 beads.
[oro] Reading SQLite state... 1,247 beads.
[oro] Diff: +9 new in bd, ~14 updated in bd, -0 deleted in bd, 0 conflicts.
[oro] Pass --apply to write changes.

$ oro bead migrate-from-dolt --reconcile --apply
[oro] Backing up SQLite to OroHome/migrations/2026-05-04T15-04-pre-reconcile-sqlite.jsonl
[oro] Applying... 9 inserts, 14 updates, 0 deletes ✓
[oro] Verifying... 1,256 beads in both stores ✓
```

### 9.9 JSONL-fallback migration path

**When triggered:** dolt is unrecoverable (corrupted beyond `dolt fsck --revive-journal-with-data-loss`, or destroyed by a `bd init --force` that nobody noticed in time). `bd export` returns nothing or errors. Phase 8 cannot proceed via the dolt path.

**Source priority** (in order):

1. **`.beads/backup/full-state.jsonl`** — heartbeat backup written every ~30s. Stale by up to one heartbeat interval; otherwise complete.
2. **Most recent `oro bead export` output** — if operator has been doing nightly cron exports per §10.2, prefer the latest.
3. **`.beads/issues.jsonl`** — only if not gitignored on this operator's machine; the 2026-04-27 decision gitignored this in oro itself but operator setups vary.
4. **Operator-supplied path** — `--from-jsonl <path>` lets the operator point at an arbitrary recovered snapshot.

**CLI:**

```bash
$ oro bead migrate-from-dolt --from-jsonl=.beads/backup/full-state.jsonl
[oro] Source: JSONL fallback (dolt unavailable)
[oro] Reading 1,247 beads from .beads/backup/full-state.jsonl
[oro] WARNING: snapshot is N seconds older than current state.
[oro] Importing... 1,247/1,247 ✓
[oro] DATA LOSS WARNING: any bead writes since snapshot are not migrated.
[oro] Resolution: after migration, verify against operator memory or
       recent worker output. Manual reconciliation required for last N seconds.
```

**The data-loss window is explicit and visible.** The operator decides whether to proceed; this is not an automatic recovery. If the operator declines, the migration aborts and they recover dolt before retrying.

**Acceptance:** the JSONL-fallback path is exercised in Phase 3 by simulating a "no dolt" condition (rename the dolt directory) and confirming migration proceeds via `--from-jsonl=.beads/backup/full-state.jsonl`.

### 9.10 Migration concurrency lock (addresses v4 adversarial review #8 + v6 #4)

Two operators running `oro bead migrate-from-dolt` simultaneously against the same `state.db` would race on the import transaction, the backup write, and the verification step. SQLite's WAL serializes the writes themselves, but the migration tool does multiple discrete operations (pre-flight count, backup, import, verify, log), and interleaving them produces nonsense reports even if the data ends up consistent.

Separately, an operator who runs migration *while a dispatcher is alive* causes a different problem: bd is being written by the dispatcher mid-export, producing a torn snapshot. v6 relied on operator discipline ("stop dispatcher first" in the Phase 8 deliverable). v7 makes the migration tool actively check.

**Two-lock protocol:**

1. **Dispatcher liveness check (v7 addition; v10 path correction per codex round-2 #2).** The migration tool reads `StateDBPath + ".lock"` (matching §16.14's v10 dispatcher-lock path). If the file exists and the named PID is alive: refuse with `oro bead migrate-from-dolt: dispatcher is running (PID N) against this state.db; stop it first via 'oro stop' or whatever your invocation pattern is`. Override with `--allow-running-dispatcher` (require explicit operator acknowledgment for unusual scenarios — e.g., a read-only smoke test of the migration code path).
2. **Migration self-lock.** Acquire `StateDBPath + ".migrate.lock"` containing the migration process's own PID. Algorithm:
   - Open with `O_CREAT|O_EXCL`. If exists, read PID and check `kill(pid, 0)`.
     - If alive AND lock file mtime < 1 hour old → refuse (live lock).
     - If alive AND lock file mtime ≥ 1 hour old → likely PID recycle (post-reboot, fresh process inherited an old PID). Log a warning and reclaim.
     - If dead → reclaim.
   - The mtime guard defends against PID recycling after a long uptime + reboot. v7 had only the `kill(pid, 0)` check; v8 adds the mtime fallback.
   - Write current PID; run migration; remove lock on exit.

The same two-stage check (`kill` + `mtime < 1h`) applies to the dispatcher PID lock at `StateDBPath + ".lock"` per §16.14.

**Why two locks:** the dispatcher lock is per-dispatcher-instance and held for the dispatcher's whole lifetime; the migration lock is per-migration-invocation and held briefly. They don't compete for the same identity but both must be checked.

**Override-flag note (2026-04-29 audit):** durable waiver events are not implemented. Do not rely on the migration tool to write durable event rows for waivers. Operators must capture any waiver in the runbook log and command transcript instead.

**Note on `oro bead` CLI subprocess locking:** the v4 reviewer asked whether worker `oro bead create` invocations need their own lock. They do not. SQLite WAL serializes writes natively; multiple workers calling `oro bead create` concurrently is safe and expected. The migration tool's lock is *only* about preventing two migration runs (which involve pre-flight checks, backup writes, and verification — operations that don't compose under interleaving). The dispatcher's PID lock (§16.14) is about preventing two dispatchers. Three locks total, each with a distinct scope:

| Lock | What it protects | Held during |
|---|---|---|
| `StateDBPath + ".lock"` | Single-dispatcher invariant per state.db | Dispatcher process lifetime |
| `StateDBPath + ".migrate.lock"` | Single-migration invariant per state.db | `migrate-from-dolt` execution |
| (none) | `oro bead create/update/close` | SQLite WAL handles serialization |

---

## 10. Export & Backup

### 10.1 The export model

Export is **on-demand**, not continuous. Two reasons:

1. The continuous JSONL sync was the problem (`MEMORY.md` line 26: gitignored due to broken sync). Repeating that pattern would repeat the pain.
2. SQLite WAL handles the runtime safety case. Backup is for humans, not for the runtime.

`oro bead export` produces:

```bash
$ oro bead export --out=beads-snapshot.jsonl
[oro] Exported 1,247 beads, 3,109 deps, 412 tags to beads-snapshot.jsonl
$ oro bead export --format=json --out=beads-snapshot.json
[oro] Exported full state to beads-snapshot.json
```

Default location if `--out` omitted: `.oro/exports/<timestamp>.jsonl`.

### 10.2 Backup strategies (operator choice)

| Strategy | Command | Notes |
|---|---|---|
| Manual snapshot | `oro bead export --out=foo.jsonl` | Best for one-off captures |
| Nightly cron | `0 2 * * * cd /path && oro bead export --out=.oro/backups/$(date +\%F).jsonl` | Built-in habit |
| Pre-merge snapshot | git pre-commit hook calls `oro bead export` if needed | Off by default |
| Litestream (advanced) | Replicate `state.db` to S3 | For high-availability ops |
| Optional acceptance-test cron | `0 3 * * * cd /path && oro bead acceptance-test \|\| <alert>` | Optional post-cutover operational hygiene. It is not a Phase 9 cutover gate; Phase 9 is validated by the native-first evidence in §12.10. |

The default oro setup adds the nightly cron suggestion to `oro readiness`'s checklist (Companion spec §4.5/§7.7).

### 10.3 Import / round-trip

Target design, not shipped yet: `oro bead import <file.jsonl>` will read JSONL and upsert into a clean or operator-approved target. It is intended for:

- Disaster recovery from a backup.
- Migrating beads from one machine to another.
- Restoring a known-good state.

Current shipped behavior: `oro bead import` is still a stub. Do not use it as Phase 8 rollback evidence, and do not treat migration JSONL backups as an in-place SQLite restore path until a native restore/import implementation is reviewed and exercised.

Planned conflict policy after native import ships: skip by default, with explicit overwrite or merge modes. No conflict-policy flag is supported by the current stub.

### 10.4 What we explicitly do NOT do

- We do not write JSONL on every transition.
- We do not auto-commit JSONL to git.
- We do not gate dispatcher startup on a fresh export.
- We do not maintain bidirectional sync between any two stores.

These are the patterns that broke before. We don't repeat them.

---

## 11. Touch Point Inventory & Migration Map

Compiled from the inventory agent's enumeration. Every file that needs to change.

### 11.1 First, an audit gate

**Before any code change**, run this verification to confirm dolt's branching/merging features are unused:

```bash
cd ~/path/to/oro/.beads/dolt
dolt log --oneline | head -20            # Are there meaningful commits?
dolt branch                              # Are there branches besides main?
dolt remote -v                           # Are there remotes?
```

Expected: `main` branch only, no remotes, commits only from `bd` automated writes (boilerplate messages). If this is what you see, the migration loses nothing of value. If you find real branching/remote usage — stop and reconsider.

### 11.2 Go code (28 shell-out sites + 6 dolt sites)

**Files that change because they go through the `BeadSource`/`Store` interface (the easy ones):**

| File | Change |
|---|---|
| `pkg/dispatcher/dispatcher.go` | Replace `BeadSource` interface declaration with consumption of `beadstore.Store`; replace bead-source construction with `selectStore(...)`. v15 reshape: no type alias (interface shapes differ); ~30 call sites updated mechanically. |
| `pkg/dispatcher/beadsource.go` | Keep `CLIBeadSource` impl; mark legacy; verify it still satisfies `beadstore.Store` (value-slice signatures) |
| `pkg/dispatcher/health.go:51` | Replace `bd dolt status` health check with SQLite ping |

**Files that need direct rewrites (bypass `BeadSource` today):**

| File | Lines | Change |
|---|---|---|
| `pkg/mg/data/source.go` | 64–285 | Take `beadstore.Store` via constructor; route reads through it |
| `pkg/mg/data/mutate.go` | 10–37 | Same — use `beadstore.Store` for writes |
| `cmd/oro/cmd_mg.go` | 36–38, 73–77, 143–160 | **v13 add per codex R5#3** — wrapper that bootstraps mg/data; checks `.beads/` and bd-on-PATH today. Update to: (a) drop the `.beads/`-presence and bd-on-PATH preflight, (b) construct the new `mgdata.Source` with a `beadstore.Store` instead of the bd-backed loader, (c) verify `state.db` is reachable via `dbutil.OpenDB(StateDBPath)` instead. |

**Files that get deleted (Phase 10, deferred 30 days):**

| File | Why |
|---|---|
| `cmd/oro/cmd_dolt.go` | Dolt server management; no longer needed |
| `cmd/oro/cmd_bd.go` | bd-CLI wrapper for stealth-mode `--db` injection; no longer needed |
| `pkg/dispatcher/dolt_recovery.go` | Dolt-specific recovery; SQLite has its own recovery path |
| `cmd/oro/port_registry.go` | Only managed dolt ports |
| `pkg/dispatcher/beadsource.go` | The legacy `CLIBeadSource`; deletable in Phase 10 |

**Files with smaller changes:**

| File | Change |
|---|---|
| `cmd/oro/cmd_init.go:76` | Remove bd from tool list; remove `bd init` invocation |
| `cmd/oro/cmd_doctor.go` | Remove dolt corruption detection; add SQLite integrity check |
| `cmd/oro/cmd_cleanup.go` | Remove `pgrep dolt` cleanup; add stale `state.db-wal` cleanup if needed; **rewrite `cleanupBeads()` (lines ~456 and ~474) to use `beadstore.Store.InProgress()` + `Update()` instead of `bd list` + `bd update` shell-outs** (v12 fix per codex round-4 #3 — v11 inventory missed these call sites; if not rewritten, `oro cleanup` breaks the day bd is uninstalled in Phase 10) |
| `cmd/oro/cmd_stop.go:291` | Remove dolt flush logic |
| `cmd/oro/cmd_uninstall.go` | Update `.beads/` cleanup to use `LegacyBeadsDir` (not removed; see §11.10) |
| `cmd/oro/paths.go:69` | **Rename** `BeadsDir` → `LegacyBeadsDir`; add doc comment; do **not** remove until Phase 11. See §11.10 |

**New files (Phase 1–3):**

| File | Purpose |
|---|---|
| `pkg/beadstore/store.go` | Reshaped 12-method `Store` interface (v15 need-first design — inspired by `pkg/dispatcher.BeadSource` but with `CreateParams`, `UpdateParams`, unified `Bead` type for all reads, no `Sync`) + supporting types |
| `pkg/beadstore/sqlite.go` | `SQLiteStore` implementation — main work |
| `pkg/beadstore/shadow.go` | `ShadowStore` read-only validation wrapper |
| `pkg/beadstore/migrate.go` | dolt → sqlite migration + `--reconcile` |
| `pkg/beadstore/testfake.go` | `FakeStore` for tests (replaces `fakeRunner` mocks of bd) |
| `pkg/beadstore/*_test.go` | Comprehensive unit tests |
| `cmd/oro/cmd_bead.go` | `oro bead` subcommand tree (imports `pkg/beadstore` directly) |
| `cmd/oro/cmd_bead_migrate.go` | `oro bead migrate-from-dolt` tool |

### 11.3 Tests (368+ files)

The test mocks fall into categories — and the realistic conversion budget is **10–14 days**, not 5. Categories:

| Pattern | Count (approx) | Effort | Migration |
|---|---|---|---|
| `fakeRunner` mocking `bd <args>` calls | ~250 | mostly mechanical (codemod gets ~80%) | Replace with `beadstore.FakeStore.AddBead(...)` etc. |
| Setup of `.beads/` directory in tests | ~50 | mechanical | Remove; tests get an in-memory SQLite |
| **Asserting on verbatim bd command strings in prompt tests** | **~50** | **hand-fix + golden regen + review** | Update assertions to expect `oro bead` strings; regen golden files; review each diff |
| Integration tests calling real `bd` | ~10 | careful | Rewrite to use `beadstore.SQLiteStore` against an in-memory or temp-file SQLite |
| Tests of `cmd_dolt.go`, `cmd_bd.go` | ~10 | delete | Delete in Phase 10 |
| Tests of `dolt_recovery.go` | ~5 | delete | Delete |

The 50 prompt-string-asserting tests are where the v1 estimate broke. Each one has hand-written expected text containing `bd create --title="…"` and similar; updating means changing the literal in the test, regenerating the golden file, and reviewing the diff to make sure no semantic drift sneaks in. Average ~30 min per such test. 50 × 30 min ≈ 25 hours = 4 person-days *just for that subset*. The remaining mechanical work fills the other 6–9 days.

Total budget: **10–14 person-days**, parallelizable to 5–7 calendar days if two engineers split the prompt-string subset and the mechanical subset.

### 11.4 Asset / hook scripts

| File | Change |
|---|---|
| `assets/hooks/bd_create_notifier.py` | Convert to listen on oro events table or rewrite as DB trigger |
| `assets/hooks/notify_manager_on_bead_create.py` | Same |
| `assets/hooks/session_start_extras.py` | Replace `bd ready`/`bd list` with `oro bead ready`/`oro bead list` |
| `assets/hooks/session_start_compact.py` | Same |
| `assets/hooks/architect_router.py` | Replace `bd create` routing with `oro bead create` |
| `assets/hooks/pre_compact.py` | Update bead query to use `oro bead show` |
| `assets/hooks/validate_agent_completion.py` | Update `bd close` parsing to `oro bead close` |
| `assets/skills/dispatching-parallel-agents/SKILL.md:31` | Remove `.beads/issues.jsonl` merge-friction note (no longer relevant); replace `bd` command invocations |
| `assets/skills/work-bead/SKILL.md:19,21,136` | **v12/v13 per codex R4#4 + R5#2** — `bd update --status in_progress` → `oro bead update --status=in_progress`; `bd close <id> --reason "..."` → `oro bead close <id> --reason="..."` |
| `assets/skills/executing-beads/SKILL.md:19,21,117` | **v13 per codex R5#1** — same patterns as work-bead |
| `assets/skills/beadcraft/SKILL.md:108,119,230,289,294` | **v12/v13** — replace positional `bd create "<title>" --type ...`, `bd dep remove` with `oro bead create --title=...` and `oro bead dep rm` |
| `assets/skills/context-checkpoint/SKILL.md:64` | **v13 per codex R5#1** — replace bd reference with oro bead equivalent |
| `assets/skills/adversarial-spec-review/SKILL.md:233` | **v13** — same |
| `assets/skills/resume-handoff/SKILL.md:51` | **v13** — same |
| `assets/skills/spec/SKILL.md:61` | **v13** — same |
| `assets/skills/beads/SKILL.md` | **v13: DELETE in Phase 6** — this is general bd documentation that becomes obsolete after the migration. The advanced bd subcommands taught here (`bd prime`, `bd mol`, `bd pour`, `bd wisp`) are not part of oro's emitted surface; users who want them after Phase 10 must install bd separately. |
| **Phase 6 hard gate** | `git grep -l 'bd ' assets/skills/` returns zero files. CI step in Phase 6 enforces this. |

### 11.5 Worker prompt (`pkg/worker/prompt.go`)

Specific lines to update (per inventory agent):

- **Lines 109–119** (BuildEpicDecompositionPrompt): `bd create --title=...` → `oro bead create --title=...`; `bd dep add` → `oro bead dep add`.
- **Line 144**: `bd show <epicID>` → `oro bead show <epicID>`.
- **Lines 154, 164, 165**: same find-and-replace pattern.
- **Lines 234–236** (Beads Tools section): rewrite as "Bead Tools" with `oro bead create`, `oro bead dep add`.
- **Lines 294–300** (Failure section): `bd create --title="P0:..."` → `oro bead create --title="P0:..."`; etc.

### 11.6 Ops prompts (`pkg/ops/`)

5 files, each with targeted edits per inventory agent:

| File | Lines | Edit |
|---|---|---|
| `pkg/ops/escalation_prompt.go` | 85, 87, 104, 114, 116, 123, 124, 126, 132–136 | All `bd <verb>` → `oro bead <verb>` |
| `pkg/ops/ac_prompt.go` | 49, 51, 84 | Same |
| `pkg/ops/decompose_prompt.go` | 27, 31–34 | Same |
| `pkg/ops/epic_fix_prompt.go` | 27 | Same |
| `pkg/ops/review_prompt.go` | (verify) | Same; ops review may not currently emit bd commands but check |

Add a one-time prompt golden-file refresh after these updates.

### 11.7 Manager / architect inline prompts

| File | Lines | Edit |
|---|---|---|
| `cmd/oro/manager.go` | 32, 55–71, 104–110 | Find-and-replace |
| `cmd/oro/architect.go` | (per inventory) | Same |

### 11.8 Scripts and docs

| File | Edit |
|---|---|
| `scripts/quality_gate.sh` | Remove `.beads/` lint exclusion (nothing there anymore); keep if `.oro/` has some; update accordingly |
| `scripts/test_git_hooks.sh:8` | Update or remove "No bd shim" comment |
| `docs/INSTALL.md` | Remove bd installation step; document SQLite (already there) and `oro bead` |
| `README.md` | Update system requirements |
| `docs/dev-setup.md` | Remove bd from setup |

### 11.9 Configuration

`.oro/config.yaml` adds an optional `beadsource_mode` field (default `sqlite` after Day 7). `.beads/config.yaml` becomes vestigial — document deprecation, plan removal in Phase 11.

Environment variables: `ORO_BEADSOURCE_MODE` (cli|sqlite|shadow). All `BD_*` and `DOLT_*` env vars become dead code; document in changelog and ignore.

### 11.10 Legacy path retention (`BeadsDir` → `LegacyBeadsDir`)

The v1 spec proposed deleting `BeadsDir` from `cmd/oro/paths.go` in Phase 10. That's premature. Several call sites need to *find* the legacy `.beads/` directory after migration:

| Call site | Why it still needs the legacy path |
|---|---|
| `oro bead migrate-from-dolt` | Reads bd's dolt directory to perform the initial export |
| `oro bead migrate-from-dolt --reconcile` | Same — for the cutover reconcile pass |
| `oro uninstall` | Cleans up old `.beads/dolt/`, `.beads/.doltcfg`, `.beads/backup/` for users uninstalling oro after migration |
| `oro doctor` | Detects orphaned `.beads/` directories from old projects and recommends cleanup |
| `oro bead import` (planned; current shipped command is a stub) | If users hand-edit `.beads/issues.jsonl` snapshots and want them imported after the native import implementation lands |
| Documentation tooling | Generates "if you have an old `.beads/` directory, here's how to clean it up" guidance |

**Phase 10 staging:**

- Rename `BeadsDir` → `LegacyBeadsDir` in `cmd/oro/paths.go`.
- Add a comment: `// LegacyBeadsDir resolves the pre-replatform .beads/ directory. Retained for migration tooling, oro uninstall, and orphan detection. Marked for removal in Phase 11 (~1–2 release cycles after cutover).`
- Stop using it for any *active* dispatcher data path (the dispatcher uses `state.db` exclusively).
- Update only the consumer call sites that genuinely need it.

**Phase 11 (separate, deferred ≥1 release cycle past Phase 10):**

- Audit: are there any oro installations still containing `.beads/dolt/`? If migration ran cleanly, no. Confirm by running a survey: `oro doctor --legacy-scan` reports any remaining dolt directories.
- If clean: delete `LegacyBeadsDir`, delete the migration tool itself (it's a one-shot artifact whose value expires), update `oro uninstall` to drop the legacy-cleanup branch.
- If unclean: extend Phase 11 by another release cycle.

This staging adds maybe 1–2 days of total effort to retain the legacy path through Phase 10. It's load-bearing for users who migrate late.

---

## 12. Implementation Phases

### 12.1 Phase 0 — Pre-flight (5 days)

**Deliverables:**

- **Audit gate** (§11.1): confirm dolt's branching/merging is unused.
- **Interface byte-match audit:** capture the existing `BeadSource` interface verbatim from `pkg/dispatcher/dispatcher.go`. Every method's full signature (name, parameters, return types) goes into a doc-comment block in the new `pkg/beadstore/store.go` as a frozen reference. Any drift (extra method like `Sync()`, slightly different return shape) is resolved here, before Phase 1 starts. Cheap to do early; expensive if discovered in Phase 7 testing.
- **bd-ready behavior audit:** read bd's source for its `ready` computation (the third-party project at `github.com/steveyegge/beads`). Document every filter, sort key, and edge case it applies. Compare against the spec's `beads_ready` view definition (§6.3). Resolve divergences in v3.1 of this spec before Phase 1. The deliverable is a `docs/plans/notes/bd-ready-semantics.md` file with a side-by-side comparison table.
- **bd version pin:** record the exact bd commit/tag in use (`bd --version` output and `go list -m github.com/steveyegge/beads@<ver>` if vendored). Pin in `docs/INSTALL.md` as the supported version through Phase 9 cutover. Operators must not upgrade bd between Phase 0 and Phase 9; documented as an explicit warning in the runbook.
- **JSONL-fallback inventory:** identify every JSONL snapshot that exists today (`.beads/issues.jsonl` if not gitignored on this operator's machine, `.beads/backup/full-state.jsonl` heartbeat backup, ad-hoc `bd export > foo.jsonl` artifacts). Document discovered locations. The migration tool gains a `--from-jsonl <path>` mode (see §9.9) to use these if dolt is unrecoverable at Phase 8.
- **Migration and recovery runbooks:** create
  `docs/runbooks/beadstore-native-cutover.md` as the current operator source of
  truth for native-first migration-day commands, and keep
  `docs/runbooks/beadstore-recovery.md` for recovery procedures plus the legacy
  shadow-mode path.
- **Bead-source caller grep:** verify `BeadSource` interface is the only seam at all caller sites. Any stray bd shell-outs that bypass the interface get listed and either (a) routed through the interface or (b) explicitly accepted as legacy with a comment.
- **Baseline capture:** record current bead counts, ready count, in-progress count, average `Ready()` latency, dispatcher startup time. Provides B1–B5 baselines for §18.
- **Schema sign-off** with one other engineer reviewing §6 and the field mapping in §9.6.

**Acceptance:**

- All five deliverables on disk; reviewer sign-off documented in `docs/decisions&discoveries.md` per oro convention.

### 12.2 Phase 1 — Schema + `pkg/beadstore` (6 days)

**Deliverables:**

- Migration #11 in `pkg/protocol/schema.go`: `MigrateBeadSchema`. Adds the 6 tables + FTS5 + 2 views.
- New package `pkg/beadstore`:
  - `store.go`: reshaped `Store` interface per §8.2 (**12 methods**, single `Bead` type, `CreateParams`/`UpdateParams` structs, no `Sync`).
  - `pkg/protocol/types.go` extension: `Bead` absorbs the 5 runtime fields from `BeadDetail` (per §8.2.1); `BeadDetail` becomes `type BeadDetail = Bead` alias for the duration of the migration.
  - `sqlite.go`: `SQLiteStore` — full implementation with transactions, including the `MemoryFetcher`-based `Show()` per §8.3b.
  - `testfake.go`: `FakeStore` — replaces `fakeRunner`-style bd mocks across the test base.
- **Single-dispatcher PID lock**: `pkg/dispatcher/lock.go` + tests, lock path `canonicalDBPath + ".lock"` via `filepath.EvalSymlinks`. Tests (a)–(e) per §16.14.
- **No `pkg/memory` changes required.** Existing `memory.ForPrompt(ctx, store, tags, desc, maxTokens)` is the contract.
- **Interface migration pass (v15 — replaces the v14 alias):** mechanical rewrite of ~40 call sites across `pkg/dispatcher`, `pkg/ops`, `pkg/mg/data` to consume the reshaped `Store` interface — `BeadSource` references → `beadstore.Store`, positional `Create(...)` → `Create(ctx, beadstore.CreateParams{...})`, status-`Update(...,"x")` → `Update(ctx, id, UpdateParams{Status: &s})`, `Show()` consumers handle `*Bead` instead of `*BeadDetail`, `.Sync()` calls deleted. Estimate: 3 days. CI catches every miss at compile time.
- **Dispatcher `Update("ready")` patch (v15/v18):** 6 call sites (`pkg/dispatcher/dispatcher.go:3234, 3245, 3263, 3277, 3412, 3449, 3461, 3477` per the inventory) currently write `"ready"` via `Update()`. v15 patches them to write `"open"` — semantically identical ("this bead is now assignable") and compatible with the v18 schema CHECK (`open`, `in_progress`, `blocked`, `closed`). No callers consume the literal `"ready"` value via `Show()` in production paths; the `beads_ready` view derives readiness from `(status='open' AND no unmet deps AND not deferred)`.
- CI rule: `go vet`/import-graph check that `pkg/beadstore` does not import `pkg/dispatcher`, `pkg/worker`, `pkg/ops`, or `pkg/mg`.
- `pkg/beadstore/sqlite_test.go`: in-memory SQLite tests for every method, including:
  - Ready returns correct rows when deps are open vs closed.
  - Update validates state transitions.
  - Close is idempotent.
  - Defer/undefer round-trips.
  - `deferred_until` clears on `Update(..., "open")` (regression-test the bd zombie-defer-until bug).
- Schema migration test: clean DB → migrate → verify all tables/indexes/views/triggers exist.

**Acceptance:**

- All `Store` methods pass behavioral parity tests against fixture data.
- Race test: 10 goroutines concurrently calling Ready/Show/Close — no panics, no data corruption.
- Import-graph check passes in CI.

### 12.3 Phase 2 — `oro bead` CLI (3 days)

**Deliverables:**

- `cmd/oro/cmd_bead.go`: full Cobra subcommand tree per §7.1.
- Each subcommand calls `beadstore.Store` directly (no remote call to dispatcher; CLI imports `pkg/beadstore`, not `pkg/dispatcher`).
- `--json` flag on every subcommand; output is oro-native schema (v15 — no longer parity with bd; see §7.2).
- `cmd/oro/cmd_bead_test.go`: end-to-end tests covering each subcommand.

**Acceptance:**

- `oro bead ready --json` produces output that round-trips through `pkg/mg/data` parsing without modification.
- `oro bead create` followed by `oro bead show` round-trips title/description/AC/tags/deps.

### 12.4 Phase 3 — Migration tool (3 days)

**Deliverables:**

- `cmd/oro/cmd_bead_migrate.go`: `oro bead migrate-from-dolt [--dry-run] [--reconcile] [--apply] [--from-jsonl <path>] [--from-fixture <path>] [--ignore-version-drift] [--allow-running-dispatcher] [--force-recover]`.
- Handles all edge cases per §9.2.
- Writes a backup snapshot before mutating.
- Produces a migration report with counts and errors.
- Backup is mandatory for initial apply.

**Acceptance:**

- On a fixture dolt export of 100 beads with deps, tags, AC-in-markdown: post-migration `oro bead show <id>` matches `bd show <id>` for every field.
- Re-run with `--reconcile` is idempotent (zero net changes if dolt unchanged).

### 12.5 Phase 4 — Shadow mode (2 days)

**Deliverables:**

- `pkg/beadstore/shadow.go`: read-only dual-read wrapper. Writes forward to primary only — secondary intentionally drifts.
- `selectStore` in dispatcher init: picks based on `ORO_BEADSOURCE_MODE` (default `cli`, opt-in `shadow`, post-cutover `sqlite`).
- Divergence logging into `events` table with type `beadstore_divergence`.
- A new view query: `oro events --type=beadstore_divergence` for quick auditing.

**Acceptance:**

- Shadow mode runs for 1 hour against a real bead set with zero read-divergences across at least 100 read calls.
- Write-divergence is *expected* (SQLite is intentionally stale during shadow); reconcile pass is what closes the gap at cutover. This is documented in Phase 8 acceptance, not Phase 4.

### 12.6 Phase 5 — mg/data refactor (5–6 days, v16 expanded)

**Deliverables:**

- `pkg/mg/data/source.go` and `mutate.go` rewritten to use `beadstore.Store` instead of bd shell-outs.
- **`pkg/mg/data/issue.go` Issue struct updated for v15 oro-native JSON (v17 — corrects v16's compile-error per codex round-8 #2):**
  - JSON tag `json:"issue_type"` → `json:"type"` (matches new oro emission).
  - **Add explicit field — but with a non-colliding name.** v16 named the field `ParentID`, which collides with the existing `ParentID()` *method* on the same struct (Go forbids field+method sharing a selector). v17 introduces:
    - Field: `ParentIDValue string \`json:"parent_id,omitempty"\`` (distinct selector from the method).
    - Method: `ParentID() string` is reshaped into a unified accessor — returns `i.ParentIDValue` when non-empty, falls back to dotted-ID parsing of `i.ID` otherwise. Existing call sites of `issue.ParentID()` continue to work without edits; the new oro-native JSON populates `ParentIDValue` and the accessor returns it.
  - No callers of `ParentID()` need to change. The method's signature is unchanged; its internal logic gains a field-first path with the dotted-ID parser as fallback.
- Constructor accepts a `beadstore.Store`; tests inject `beadstore.FakeStore`.
- All test mocks of bd CLI in mg/data tests removed.
- Verify dashboard's empty-vs-nil semantics for `Labels`, `Metadata`, `Tags` match the new oro-native JSON shape — this is fiddly and the budget reflects it.

**Acceptance:**

- Dashboard renders correctly under shadow mode against a sample dataset.
- A snapshot of dashboard HTML before/after shows zero diff for identical bead state.
- Hierarchy-builder test: a bead with explicit `ParentIDValue=abc-1` but a flat ID (e.g., id=`xyz-3`) appears as a child of `abc-1` in the parade view, not as orphaned. v9–v14 would have failed this test because the dotted-ID parser would not have found a parent in `xyz-3`. v17's accessor (`ParentID()` method returns the field value when set, dotted-ID parse otherwise) covers both old and new sources transparently.

### 12.7 Phase 6 — Prompt + asset updates (2 days)

**Deliverables:**

- `pkg/worker/prompt.go` updated per §11.5.
- All 5 ops prompts updated per §11.6.
- All 7 hook scripts updated per §11.4.
- Manager/architect inline strings updated per §11.7.
- Golden-file regen for prompt tests.

**Acceptance:**

- Prompt diff review with one other engineer.
- Worker spawned with new prompt successfully creates a bead via `oro bead create`.

### 12.8 Phase 7 — Test mock conversion (10–14 days)

**Deliverables:**

- All ~368 test files updated. Three sub-tracks (parallelizable):
  - **Track A (mechanical, ~6–8 days):** Replace `fakeRunner` mocks of bd with `beadstore.FakeStore`. Codemod handles ~80% via regex; the 20% manual.
  - **Track B (verbatim-string asserts, ~4 days):** Update ~50 prompt-test golden files where `bd <verb>` strings are checked literally. Each is hand-fix + golden regen + diff review.
  - **Track C (delete, ~0.5 day):** Tests of `cmd_dolt.go`, `cmd_bd.go`, `dolt_recovery.go` come out wholesale.

**Acceptance:**

- `make test-all` passes.
- Test coverage on `pkg/beadstore` ≥ 85%.
- Prompt-test golden files reviewed by one other engineer (no semantic drift in worker/ops instructions).

### 12.9 Phase 8 — Migration day (1 day)

**Deliverables (in order):**

- **bd-version pin check:** run `scripts/check-bd-version.sh` and confirm `bd --version` matches the version recorded in Phase 0. **Comparison strategy (v7):** parse the version string as `vMAJOR.MINOR.PATCH[-prerelease][+build]` per SemVer 2.0; compare on `MAJOR.MINOR` only. Build metadata (commit hash, "-dirty" suffix) and patch level are ignored — they're irrelevant for the JSON-output contract this migration relies on. If `MAJOR.MINOR` drifted, abort with: `bd version drifted from <pinned major.minor> to <current major.minor>; reinstall pinned version or restart from Phase 0`. `--ignore-version-drift` appears in `migrate-from-dolt --help` but is not implemented for initial migration. The only approved waiver path is `scripts/check-bd-version.sh --ignore-version-drift`, with the waiver recorded in the operator log. This addresses v3 risk R-BD-VERSION (§16) and v4/v6 review notes.
- **(v16 explicit per codex round 7) Worker-restart + bd-PATH-strip:** after migration succeeds, native validation passes, and `ORO_BEADSOURCE_MODE=sqlite` is set, **the operator restarts every worker** before resuming traffic. Workers inherit the dispatcher daemon environment, so the Phase 8 restart starts the dispatcher itself from a generated `PATH` that resolves the reviewed `oro` binary, `claude`, and `git` but not `bd`, using the executable sequence in `docs/runbooks/beadstore-native-cutover.md`. v15's no-shim cutover only works if both these steps happen at Phase 8; without them, in-flight workers would still emit `bd …` and silently fail on `command not found`. This is a hard Phase 8 deliverable, not a runbook footnote.
- **Single-dispatcher invariant:** confirm only one dispatcher is running (no stale PID lock, no orphan dispatcher process).
- **No concurrent bead writers:** confirm no workers, direct `bd` processes, direct native `oro bead` mutator processes, or other migration commands are running before dry-run, apply, native validation, or restart. The gate intentionally treats read-only `bd` commands as stop-the-world conflicts so it cannot miss newly added bd mutators. Keep them stopped until the sqlite-mode cutover restart.
- **Real-data dry-run gate:** run `./oro bead migrate-from-dolt --dry-run` and require success without `--force-recover`. The 2026-04-29 audit failure (`bd export count: 1718`; `dolt internal count error: ... no database selected`; `Aborting.`) was fixed on 2026-04-30 by selecting the configured Dolt database and counting `issues`; rerun the gate on migration day and treat any fresh failure as blocking.
- **Pre-migration SQLite rollback gate:** create a pre-migration `state.db` SQLite backup snapshot after checkpointing WAL, run `sqlite3 "$snapshot_dir/state.db" 'PRAGMA integrity_check;'`, require `ok`, and record the snapshot path before real apply. The migration JSONL backup is required source/audit data, not a restore command for a failed SQLite target.
- Run migration on production state.db.
- Validate native SQLite directly with `ORO_BEADSOURCE_MODE=sqlite` against
  `ready`, `blocked`, `show`, and a controlled create/close smoke bead.
- Set `ORO_BEADSOURCE_MODE=sqlite`.
- Restart dispatcher with `ORO_HUMAN_CONFIRMED=1 ./oro stop --force` followed by the stripped-`PATH` `dispatcher start --force --workers <count>` sequence in `docs/runbooks/beadstore-native-cutover.md`.
- Treat bd/Dolt as import source, audit trail, and rollback reference. bd parity
  does not veto cutover when divergence is caused by bd/Dolt failure, stale bd
  state, or bd unavailability.

**Acceptance:**

- bd-version match.
- Dispatcher, workers, direct bd processes, direct native `oro bead` mutators,
  and other migration commands are stopped before dry-run, real apply, and
  reconcile preview/apply.
- Real-data dry-run exits 0 without `--force-recover` and without a non-empty native target error. Initial migration must fail closed if the native `beads` table already contains any rows, including soft-deleted rows; retry requires restoring or clearing `state.db` through the reviewed runbook rollback path.
- Pre-migration `state.db` SQLite backup snapshot path recorded, and `PRAGMA integrity_check` on the snapshot returns exactly `ok` before real apply.
- Real migration report shows zero validation errors and records the mandatory JSONL backup path under `OroHome/migrations/<timestamp>-pre-migration.jsonl`.
- Native SQLite validation passes directly with `ORO_BEADSOURCE_MODE=sqlite`:
  `ready` and `blocked` return valid JSON arrays, `show` works for migrated
  rows, `scripts/check-native-beadstore-invariants.py` reports zero mismatches
  for the ready/blocked views and assignment/blocker invariants, a controlled
  smoke bead can be created and closed, and `PRAGMA integrity_check` returns
  exactly `ok` before and after the smoke.
- `ORO_BEADSOURCE_MODE=sqlite` is exported only after a clean real migration and native validation, and the restarted dispatcher process is verified to inherit it.
- Dispatcher and workers restarted; every restarted worker subprocess has `bd`
  absent from `PATH`, `oro` present, and a controlled per-worker log segment
  captured after a pre-assignment byte offset proving the assigned test bead
  was handled with native `oro bead` commands and no `bd` command invocation.
- The operator log links to `docs/runbooks/beadstore-native-cutover.md` and
  records why any bd/Dolt divergence was treated as non-veto.

### 12.10 Phase 9 — Native cutover evidence

**Deliverables:**

- Native SQLite remains the authority after Phase 8 cutover evidence passes.
- bd/Dolt is retained only as import source, audit trail, and rollback
  reference. bd parity does not veto cutover when divergence is caused by
  bd/Dolt failure, stale bd state, or bd unavailability.
- Dispatcher and worker startup are proven with `ORO_BEADSOURCE_MODE=sqlite`
  and a `PATH` that resolves `oro` but not `bd`.
- Operator evidence is recorded in
  `docs/plans/notes/phase9-observation-report.md`.

**Acceptance:**

- `scripts/check-phase8-no-writers.py` reports `active_writer_count=0`
  before and after validation.
- Native commands work directly against the target `state.db`: `oro bead
  status`, `ready --json`, `blocked --json`, and `show <representative-id>
  --json`.
- `scripts/check-native-beadstore-invariants.py` reports zero ready/blocked
  view mismatches, zero ready/blocked overlap, zero assignment/blocker
  mismatches, and `PRAGMA integrity_check` returns `ok`.
- A controlled sqlite/no-bd dispatcher-worker smoke records native `spawn_for`
  and `assign` events for the requested bead.
- Bead operation latency is improved or unchanged versus the Phase 0 bd
  baseline in `docs/plans/notes/baseline-metrics.md`.

### 12.11 Phase 10 — Cleanup pass 1 (2 days, deferred 30 days)

**Deliverables:**

- Delete: `cmd/oro/cmd_dolt.go`, `cmd/oro/cmd_bd.go`, `pkg/dispatcher/dolt_recovery.go`, `pkg/dispatcher/beadsource.go` (`CLIBeadSource`), `cmd/oro/port_registry.go`.
- Remove bd from `cmd/oro/cmd_init.go`'s tool list.
- (v15 has no bd-shim; this v14 cleanup item is N/A.)
- Update INSTALL.md, README.md, dev-setup.md.
- **Retain `LegacyBeadsDir`** in `cmd/oro/paths.go`. See §11.10 — uninstall, doctor, migration tooling all still reference it.
- (v15 has no `BeadSource` type alias — the rename pass in Phase 1 already replaced every call site mechanically. This v14-era cleanup step is N/A.)
- Archive `.beads/dolt/` directories on operator machines or document their manual removal.
- Remove dolt-recovery entries from MEMORY.md (mark as superseded; do not delete the historical context).

**Acceptance:**

- `make build && make install` produces a working oro without bd installed.
- Fresh-machine setup test: clean macOS user, install oro, run a bead, succeed. If the historical `fresh-mac:latest` image is unavailable, run `scripts/check-phase10-no-bd-install.sh` and record the missing image as an acceptance-harness replacement, not a product blocker.
- `oro uninstall` still cleanly removes a legacy `.beads/` directory if present.

### 12.12 Phase 11 — Cleanup pass 2 (1 day, deferred ~1–2 release cycles past Phase 10)

**Owner:** explicitly named at Phase 10 sign-off. The Phase 10 approval task includes "Designate Phase 11 owner with calendar reminder set for `<Phase 10 date> + 90 days`." Without a named owner and a calendar reminder, this phase reliably never happens (per the v2 premortem elephant E-PHASE-11-NEVER).

**Deliverables:**

- Survey: run `oro doctor --legacy-scan` across operator machines. Confirm no live `.beads/dolt/` directories remain.
- Delete `LegacyBeadsDir` from `cmd/oro/paths.go`.
- Delete `oro bead migrate-from-dolt` (one-shot tool whose value has expired).
- Drop the legacy-cleanup branch from `oro uninstall`.
- Drop `.beads/` references from `assets/skills/dispatching-parallel-agents/SKILL.md`.
- File a "Phase 11 complete" entry in `docs/decisions&discoveries.md`.

**Acceptance:**

- `git grep -i 'BeadsDir\|migrate-from-dolt'` returns no live code references.

**Forcing function** if Phase 11 stalls: each subsequent quarterly retrospective surfaces the open Phase 11 task. After two quarters of no progress, the task is either executed or formally written off (with a documented reason for keeping `LegacyBeadsDir` indefinitely — for instance, "we still get monthly migration requests from internal users on stale forks"). Either outcome resolves the elephant; silent indefinite deferral does not.

### 12.13 Total timeline

| Phase | Effort (engineer-days, v15) | Calendar |
|---|---|---|
| Phase 0 (pre-flight) | 5 | 1 week |
| Phase 1 (schema + beadstore + interface rename + dispatcher Update("ready")→"open" patches) | 8 (was 6 in v14; +2 for the rename pass) | 1.5 weeks |
| Phase 2 (CLI; **no bd-shim**) | 3 (was 4 in v14; -1 for shim removal) | 3 days |
| Phase 3 (migration tool + reconcile + JSONL fallback + AC normalization) | 5 | 1 week |
| Phase 4 (shadow) | 2 | 2 days |
| Phase 5 (mg/data; consumes oro-native JSON) | 4–5 | 1 week |
| Phase 6 (prompts/assets — **must complete before Phase 8** since no shim safety net) | 3 (was 2 in v14; +1 for stricter cutover gating) | 3 days |
| Phase 7 (tests; **no bd-shim test extraction**) | 9–13 (was 10–14 in v14; -1 for no shim tests) | 1.5–2 weeks |
| Phase 8 (migration day; **operator restarts all workers**) | 1 | 1 day |
| Phase 9 (native cutover evidence) | active validation | same day after Phase 8 proof |
| Phase 10 (cleanup pass 1) | 2 | deferred 30 days |
| Phase 11 (cleanup pass 2) | 1 | deferred ~1–2 release cycles |
| **Total active engineering** | **43–48 days** | **≈ 8.5–10 weeks** |

v15 net effort delta vs v14: **+2 to +3 person-days.** The interface-rename + dispatcher Update() patches add ~3 days; the bd-shim removal saves ~5 days of design + table + tests + extraction; mg/data is cleaner without parity preservation (factored into the existing 4–5 day budget); Phase 6 gains a hard CI gate (cost: +1 day). Net: roughly even, possibly slightly faster than v14 — and structurally cleaner forever after.

Phases 1, 2, 3 can overlap (different packages). Phase 7 (tests) overlaps Phase 5 and 6. Phase 8 is a stop-the-world event. Phases 9, 10, 11 are calendar-bound.

---

## 13. Concurrency, Transactions, Atomicity

### 13.1 SQLite WAL + busy timeout

WAL mode and `busy_timeout=5000` are configured in `pkg/dbutil/openDB.go:OpenDB()` (lines 37–68 — `PRAGMA journal_mode=WAL` at line 58, `PRAGMA busy_timeout=5000` at line 63). The `cmd/oro/db.go:openDB()` function (line 15) is a thin wrapper that delegates to `dbutil.OpenDB` and adds schema-migration glue. v4 of this spec incorrectly cited `cmd/oro/db.go` as the source-of-truth; v5 corrects this.

All callers — dispatcher init, `oro bead` CLI, mg/data — must construct their `*sql.DB` via `dbutil.OpenDB()` (or `cmd/oro/db.go:openDB()` which delegates to it) rather than calling `sql.Open("sqlite3", ...)` directly. This is enforced by a CI lint: any `sql.Open` outside `pkg/dbutil/openDB.go` fails the build (with whitelist exceptions documented per use).

The new `pkg/beadstore.SQLiteStore` accepts an `*sql.DB` from the caller and does not configure connection-level pragmas itself. This avoids drift between dispatcher-side and CLI-side database settings.

**Phase 1 acceptance** explicitly verifies `PRAGMA journal_mode='wal'` and `PRAGMA busy_timeout=5000` against a freshly-**schema-migrated** `state.db` (i.e., a `state.db` that has had Phase 1's `MigrateBeadSchema` applied via `dbutil.OpenDB` + `protocol.MigrateAll`, with no bead rows). v9 fix per codex finding #11: the v8 spec said "freshly-migrated state.db" which was ambiguous and could be read as requiring `oro bead migrate-from-dolt` (a Phase 3 deliverable). v9 makes explicit that Phase 1's WAL check uses only Phase 1 deliverables — schema migrations and the existing `dbutil.OpenDB` — not the dolt-import migration tool.

**Phase 3 acceptance**, in contrast, runs `oro bead migrate-from-dolt --dry-run` against a real dolt snapshot and verifies the resulting schema-plus-data state. Phase 1 vs Phase 3 acceptance use different fixtures and different harnesses.

### 13.2 Transactions per write operation

Every state-changing BeadSource method opens a transaction:

- `Create`: insert beads + deps + tags + metadata + event in one tx.
- `Update`: row update + event in one tx.
- `Close`: row update + event in one tx.

This is genuinely atomic. The dispatcher's "claim a ready bead and assign it to a worker" flow becomes:

```sql
BEGIN IMMEDIATE;
SELECT id FROM beads_ready WHERE id = ? AND status = 'open' LIMIT 1;
UPDATE beads SET status = 'in_progress', updated_at = ... WHERE id = ?;
INSERT INTO assignments (...) VALUES (...);
INSERT INTO events (...) VALUES (...);
COMMIT;
```

If a concurrent worker tries to close the same bead first, one of them gets a serialization failure and retries. With bd today, this could silently produce double-claims.

### 13.3 The dispatcher mutex still exists

`d.mu` continues to guard in-memory state. SQLite handles cross-process safety; `d.mu` handles intra-process coherence. Both are necessary; neither is sufficient alone.

### 13.4 Reader concurrency

WAL allows unlimited concurrent readers without blocking writers. Dashboard polls, hook scripts running `oro bead ready`, and workers calling `oro bead show` all coexist with the dispatcher's writes.

### 13.5 Connection pooling

Go's `database/sql` connection pool is configured in `pkg/protocol`. Verify max-open-connections is appropriate for the worker count. A safe default: max(10, 2× expected concurrent workers).

### 13.6 Backup safety

`oro bead export` reads in a transaction, so the snapshot is consistent even with concurrent writes. Implementation: `BEGIN; SELECT *; COMMIT;` — SQLite's MVCC gives a consistent view.

---

## 14. Worker & Ops Prompt Updates

### 14.1 Worker prompt section "Beads Tools" → "Bead Tools"

Old (per inventory agent, `pkg/worker/prompt.go:234–236`):

```
Beads Tools:
- `bd create` — decompose a bead into smaller sub-beads
- `bd dep add` — declare a blocker dependency
```

New:

```
Bead Tools:
- `oro bead create --title='…' --type=task --priority=N` — create a sub-bead
- `oro bead dep add <bead-id> <depends-on-id>` — declare a blocker dependency
- `oro bead update <bead-id> --parent=<parent-id>` — set parent for epic hierarchy
- `oro bead close <bead-id> --reason='…'` — close on completion (rare; dispatcher usually closes)
```

### 14.2 Worker escalation flow

Old (lines 294–300):

```
bd create --title="P0: <title>" --type=bug --priority=0 ...
bd update <child-id> --parent <bead-id>
bd dep add <bead-id> <child-id>
```

New:

```
oro bead create --title="P0: <title>" --type=bug --priority=0 ...
oro bead update <child-id> --parent=<bead-id>
oro bead dep add <bead-id> <child-id>
```

### 14.3 Ops escalation playbooks

`pkg/ops/escalation_prompt.go` carries 8 playbooks (STUCK_WORKER, PRIORITY_CONTENTION, MISSING_AC, OVERSIZED_BEAD, etc.). Each references bd commands. Each gets find-and-replace per §11.6.

The MISSING_AC playbook (line 116):

Old: `bd update <bead-id> --acceptance "..."`
New: `oro bead update <bead-id> --acceptance="..."`

The OVERSIZED_BEAD playbook (lines 124–126):

Old:
```
bd update <bead-id> --type epic
bd dep add <epic-id> <child-id>
```

New:
```
oro bead update <bead-id> --type=epic
oro bead dep add <epic-id> <child-id>
```

### 14.4 Backwards compatibility at cutover (v15 — no shim)

v9–v14 had a "shim window" here describing how worker prompts emitting `bd <verb>` would be caught and translated to `oro bead <verb>` during a transition period. **v15 deleted the shim**; v18 cleans up this section to match.

Worker prompts in flight at Phase 8 cutover that still emit `bd <verb>` will fail with `command not found`. The dispatcher's existing failure-recovery path retries the bead with a fresh worker. The Phase 8 deliverable list (§12.9) explicitly includes "restart all workers" + "strip bd from worker PATH" — together they ensure no worker continues running with the old prompt. Phase 6's `git grep -l 'bd ' …` CI gate ensures no in-tree prompts emit `bd` post-Phase-6. The combination produces a clean cutover with no permanent translator code. Trade: more cutover-day attention; permanently simpler runtime.

### 14.5 End-to-end acceptance test (`oro bead acceptance-test`) (addresses adversarial review C1)

The v3 spec had per-phase acceptance gates but no single command verifying that
the migration is *globally* complete and correct. The old Phase 9 passive
observation window is superseded by the native-first evidence gate in §12.10,
but the end-to-end acceptance command remains useful as an operator check:

```
$ oro bead acceptance-test
[oro] Running checks (count grows per phase; see "Acceptance test inventory" below)...
  ✓  state.db schema integrity (PRAGMA integrity_check)
  ✓  WAL mode enabled (journal_mode=wal)
  ✓  busy_timeout=5000
  ✓  All 6 bead tables exist with expected columns
  ✓  Native ready/blocked commands return JSON arrays and `scripts/check-native-beadstore-invariants.py` reports zero ready/blocked view mismatches, ready/blocked overlap, active-assignment leaks, and ready rows with unclosed hard blockers. bd/Dolt parity is audit-only after migration and is not a cutover veto when divergence is caused by bd/Dolt failure, stale bd state, or bd unavailability.
  ✓  Native validation proves a representative migrated bead with `oro bead show --json` and a controlled native create/show/close/show smoke bead against the target `state.db`.
  ✓  Roundtrip: oro bead create → show → close → show
  ✓  oro bead create --parent=<epic> sets parent_id only (zero bead_deps rows for new bead) — v14 contract
  ✓  Roundtrip: oro bead create → dep add → ready (filtered out) → close dep → ready (returned)
  ✓  defer/undefer clears deferred_until (regression-test for bd zombie-defer-until bug)
  ✓  Update(open) clears deferred_until
  ✓  AllChildrenClosed correctly identifies completed epics
  ✓  HasChildren returns true iff the epic has any children (open or closed) — distinct from AllChildrenClosed
  ✓  Show returns *protocol.Bead with persisted fields populated; runtime fields (WorkerID, ContextPercent, etc.) populated when assignment+fetcher exist, omitted via omitempty otherwise
  ✓  Create takes CreateParams, returns *protocol.Bead with id populated (v15/v16 reshape)
  ✓  Export produces valid JSONL parseable by jq
  ✓  Export parity: `oro bead export` JSONL parses and matches bd-compatible export data after migration; native import roundtrip is deferred until `oro bead import` ships
  ✓  beads_fts indexes are queryable
  ✓  Triggers fire: insert dep → parent updated_at advances
  ✓  Triggers fire: delete tag → parent updated_at advances
  ✓  Concurrent reads: 10 goroutines × 100 reads, no errors
  ✓  Concurrent writes: 10 goroutines × 10 creates, all unique ids
  ✓  PID lock prevents second dispatcher
  ✓  Stealth-mode test: state.db at ~/.oro/projects/s-<hash>/state.db works
  ✓  Hooks execute against state.db successfully (sample: bd_create_notifier)
  ✓  Migration tool dry-run completes against fixture dolt export
  ✓  Reconcile detects timestamp ties as conflicts (not no-ops)
  ✓  Partial-dolt-corruption pre-flight refuses without --force-recover
  ✓  JSONL fallback path works when dolt is unavailable
  ✓  Worker prompt rendering produces no `bd ` references (post-Phase-6)
  ✓  Ops prompts produce no `bd ` references (post-Phase-6)
  ✓  Hooks produce no `bd ` references (post-Phase-6)
  ✓  Phase 6 CI gate: `git grep -l 'bd ' assets/skills/ pkg/worker/ pkg/ops/ cmd/oro/manager.go cmd/oro/architect.go assets/hooks/` returns zero files (replaces v14's bd-shim coverage check)
  ✓  Phase 8 cutover: workers restarted; bd stripped from worker PATH; new spawns produce no `bd: command not found` events over 4-hour window (replaces v14's shim deny-default check)
  ✓  CI lint: pkg/beadstore does not import pkg/dispatcher
  ✓  CI lint: no sql.Open outside cmd/oro/db.go
  ✓  CLIBeadSource (legacy) implements beadstore.Store after Phase 1 adapter pass: var _ beadstore.Store = (*dispatcher.CLIBeadSource)(nil)
  ✓  SQLiteStore implements beadstore.Store: var _ beadstore.Store = (*beadstore.SQLiteStore)(nil)
  ✓  All 12 v15/v16/v17 reshaped Store methods implemented by SQLiteStore: Ready, InProgress, Blocked, Closed, Show, Create, Update, Close, HasChildren, AllChildrenClosed, FindByParentAndTag, Export
PASS — checks succeeded in 4.3s
```

**Single command, binary pass/fail.** Returns exit code 0 on full pass; non-zero with the failed check name on any failure. This is the acceptance test for the migration as a whole.

**Acceptance test inventory by phase** (the v4 review correctly noted the inventory wasn't enumerated; v5 fixes this):

| Phase that adds it | Check |
|---|---|
| 1 | PRAGMA integrity_check; journal_mode=wal; busy_timeout=5000; bead tables exist with expected columns; FTS5 indexes queryable; SQLiteStore implements beadstore.Store (compile-time `var _ beadstore.Store = (*SQLiteStore)(nil)` assertion); CLIBeadSource adapter still implements beadstore.Store; all 12 reshaped Store methods present; HasChildren and AllChildrenClosed both return correct values for an epic with zero, some, and all-closed children |
| 1 | Triggers fire: insert dep → parent updated_at advances; delete tag → parent updated_at advances; insert metadata → parent updated_at advances |
| 1 | Transaction discipline: deliberately interrupted multi-row insert leaves no partial state |
| 2 | `oro bead create` → `show` → `close` round-trip preserves all fields; `Show` returns `*protocol.Bead` with persisted fields populated and runtime fields populated when assignment + fetcher are available; `Create` takes `CreateParams` and returns `*protocol.Bead` with the new id (v15/v16 reshape) |
| 2 | **`oro bead create --parent=<epic>` sets `beads.parent_id` only — zero rows in `bead_deps` for the new bead.** v14 contract per codex round-6 note. Synthetic test: `oro bead create --title=child --parent=epic-x`, then assert `SELECT parent_id FROM beads WHERE id=<new>` = `epic-x` AND `SELECT COUNT(*) FROM bead_deps WHERE bead_id=<new> OR depends_on_id=<new>` = 0. |
| 2 | `oro bead defer` then `oro bead update --status=open` clears `deferred_until` (zombie-defer regression test) |
| 2 | `oro bead dep add` → `oro bead ready` filters; close dep → `oro bead ready` returns the now-unblocked bead |
| 2 | `oro bead --json` emits the oro-native schema (v15) and round-trips through the rewritten `pkg/mg/data` parsers — verified against fixtures using `parent_id`, `type`, RFC3339Nano timestamps |
| 2 | Phase 6 CI gate verified: `git grep -l 'bd[[:space:]]' assets/skills/ pkg/worker/ pkg/ops/ cmd/oro/manager.go cmd/oro/architect.go assets/hooks/` returns zero files post-Phase-6 (v15 no-shim cutover; replaces v14's bd-shim translation-table coverage row) |
| 3 | Migration tool dry-run completes against real-data dolt snapshot; row-count parity matches; partial-corruption pre-flight refuses without `--force-recover`; JSONL fallback path works with dolt absent; reconcile detects timestamp ties as conflicts not no-ops; reconcile is idempotent |
| 3 | Migration concurrency lock refuses second invocation on same project |
| 4 | Shadow mode writes divergence events on intentional drift; primary read is authoritative |
| 5 | Dashboard renders identically before/after under shadow against same dataset |
| 6 | Worker prompt rendering produces no `bd` command references; ops prompts produce no `bd` command references; hooks produce no `bd` command references |
| 7 | CI lint: `pkg/beadstore` does not import `pkg/dispatcher`; no `sql.Open` outside `pkg/dbutil/openDB.go` |
| 8 | Stealth-mode test: state.db at `~/.oro/projects/s-<hash>/state.db` migrates and tracks beads correctly |
| 8 | Concurrent reads: 10 goroutines × 100 reads each → no errors; concurrent writes: 10 goroutines × 10 creates → 100 distinct ids, no dropped writes |
| 9 | Cross-process WAL: 10 concurrent `oro bead create` subprocesses → 10 distinct beads, no corruption |
| 9 | PID lock prevents second dispatcher; stale-lock detection reclaims dead-process locks |
| 9 | Hooks execute against `state.db` successfully (sample run of each of the 7 hook scripts) |

**Phase 9 acceptance** is amended: do not require a passive shadow or cron soak.
The gate is the native-first evidence listed in §12.10 and the current
operator runbook. After that evidence passes, failures are native beadstore bugs
unless data corruption requires restoring a recorded SQLite backup.

**Phase 1 deliverable** includes the initial acceptance-test stub. Each phase
adds its row(s) from the table above. By Phase 9, the full inventory can be run
on demand; scheduled runs are optional post-cutover hygiene, not a cutover gate.

---

## 15. Test Migration Strategy

### 15.1 The fake BeadSource

```go
// In pkg/dispatcher/test/fake_beadsource.go (new):
type FakeStore struct {
    beads      map[string]*protocol.Bead
    deps       map[string][]string  // bead_id → depends_on_ids
    closeCalls []CloseCall
    // ... etc.
}
```

Tests that previously did:

```go
runner := &fakeRunner{}
runner.On("Run", mock.Anything, "bd", "ready", "--json").Return(`[{"id":"x"}]`, nil)
```

become:

```go
fake := dispatchertest.NewFakeStore()
fake.AddBead(&protocol.Bead{ID: "x", Status: "open"})
```

Cleaner tests. ~30% LOC reduction in test-mock setup.

### 15.2 Conversion strategy

Mechanical, file-by-file. Two patterns dominate:

**Pattern 1: Mock setup** — replace `runner.On(...)` calls with `fake.AddBead(...)` calls. Tooling: a regex-based codemod can do 80% of this; the remaining 20% are bespoke and need eyes.

**Pattern 2: Assertions on bd commands** — tests that asserted "the dispatcher called `bd close X`" become "the dispatcher called `BeadSource.Close(ctx, X)`". Same intent, different surface.

### 15.3 Integration tests

`pkg/integration/e2e_lifecycle_test.go` currently spins up a real bd setup. We add a parallel test that uses an in-memory SQLite. Both run in CI during the shadow window; the real-bd test goes away in Phase 10.

### 15.4 Fixtures

A new fixture directory `pkg/dispatcher/testdata/beads/` holds JSONL fixtures for integration tests. Until native `oro bead import` is implemented, migration fixtures use `oro bead migrate-from-dolt --from-jsonl` or package-level test helpers. Sample fixtures: one ready bead, one in-progress bead, one epic with three children, one chain of three blocking deps.

---

## 16. Risk Register

### 16.1 R-MIGRATION-DATA-LOSS

- **Severity:** High — historical bead data lost.
- **Likelihood:** Low — migration is idempotent, has dry-run, has backup.
- **Mitigation:** `--dry-run` first; backup written to `OroHome/migrations/` before mutation; reconcile pass after Phase 8.
- **Detection:** Post-migration counts comparison.
- **Fallback:** Restore `state.db` from the operator's pre-migration SQLite backup snapshot, preserve the JSONL pre-migration backup for audit/recovery tooling, and revert `ORO_BEADSOURCE_MODE` to `cli`.

### 16.2 R-NATIVE-READ-CORRECTNESS

- **Severity:** High — `SQLiteStore` returns internally wrong `Ready`,
  `Blocked`, or `Show` results after cutover.
- **Likelihood:** Medium — first time we run the new code against real load.
  Most likely cause: subtle differences in dependency-graph computation, active
  assignment semantics, tag/label set ordering, or AC extraction from markdown.
- **Mitigation:** Cutover is gated on direct native validation, not bd parity:
  `ORO_BEADSOURCE_MODE=sqlite` must return valid JSON for `ready` and
  `blocked`, `show` must work for migrated rows, a controlled smoke bead must
  create and close successfully, and `PRAGMA integrity_check` must return `ok`
  before and after the smoke. bd/Dolt divergence is recorded for audit but does
  not veto cutover when bd/Dolt is stale, broken, or unavailable.
- **Detection:** The native validation gate in
  `docs/runbooks/beadstore-native-cutover.md`, especially
  `scripts/check-native-beadstore-invariants.py`, plus post-cutover operator
  checks and native beadstore incidents.
- **Fallback:** Stop dispatcher and workers, preserve `state.db` and logs, fix
  the native beadstore, and restore the recorded SQLite backup only if data
  corruption is proven. Reverting to bd requires first exporting SQLite and
  importing native-side writes into bd, unless an explicit data-loss decision is
  recorded.

### 16.2b R-RECONCILE-CORRECTNESS (NEW)

- **Severity:** High — reconcile pass at cutover is now load-bearing (replaces dual-write). A buggy reconcile applies wrong updates and corrupts SQLite right before flipping authority to it.
- **Likelihood:** Low if `--apply` is gated and dry-run is exercised, but the algorithm has edge cases (e.g., timestamp-tied updates with content drift).
- **Mitigation:**
  - `--reconcile` requires `--apply` to mutate; default behavior is a diff report.
  - Take an operator `state.db` SQLite backup snapshot and verify `PRAGMA integrity_check` before any reconcile-apply.
  - Backup SQLite to `OroHome/migrations/` before any reconcile-apply.
  - Conflict detection (§9.6 step 6) surfaces non-timestamp drift to operator.
  - Reconcile preview is idempotent. If a reconcile apply fails or corrupts SQLite, restore the pre-reconcile `state.db` SQLite backup snapshot or move the failed DB aside before rerunning preview/apply.
  - Test fixture: a 100-bead synthetic shadow window (some inserted, some updated, some deleted in bd) with a known reconcile-result; CI checks the algorithm matches.
- **Detection:** Post-reconcile bead count comparison against `bd export | wc -l`; spot-check 10 random beads field-by-field.
- **Fallback:** Restore `state.db` from the operator's pre-reconcile SQLite backup snapshot, preserve the JSONL pre-reconcile backup for audit/recovery tooling, fix the reconcile bug, then rerun.

### 16.3 R-WORKER-CMD-CHANGE (v15 reshape — no shim)

- **Severity:** Medium — at Phase 8 cutover, workers in flight with v14-era prompts will emit `bd …` invocations that fail with `command not found`.
- **Likelihood:** High during the cutover hour, zero thereafter (v15 has no shim; the failures are by design).
- **Mitigation:** Phase 6 lands prompt updates first (`git grep -l 'bd ' …` returns zero in-tree files); Phase 8 deliverable list explicitly includes "restart all workers" + "strip bd from worker PATH"; the dispatcher's existing failure-recovery path retries failed beads with fresh workers (whose prompts use `oro bead`).
- **Detection:** `bd: command not found` events in the event log; should drop to zero within the cutover hour.
- **Fallback:** if any worker survives the restart sweep, kill it manually; re-run with `oro bead`-emitting prompts. v15 trades shim complexity for one-hour cutover attention.

### 16.4 R-PERF-REGRESSION

- **Severity:** Low — SQLite local should beat dolt subprocess; unlikely to regress.
- **Likelihood:** Low.
- **Mitigation:** Benchmark `Ready()` and `Show()` latency before and after.
- **Detection:** Bead-operation latency telemetry.
- **Fallback:** Add indexes; profile and tune queries.

### 16.5 R-TEST-FRAGILITY

- **Severity:** Medium — converting 368 tests has tedium-driven error rate; ~50 prompt golden files need careful diff review.
- **Likelihood:** Medium-High during the conversion window.
- **Mitigation:** Phase 7 has **10–14 days** budgeted (revised up from v1's 5); codemod for the ~80% mechanical subset; pair-review on the verbatim-string golden-file subset.
- **Detection:** CI failures; visual diff review on golden files.
- **Fallback:** Keep `CLIBeadSource` integration tests running in CI under a build tag through Phase 11 release cycle.

### 16.6 R-DELETED-CODE-RESURRECTION

- **Severity:** Low — someone re-introduces a `bd` shell-out post-cleanup.
- **Likelihood:** Low.
- **Mitigation:** Add a CI lint rule that fails the build if `os/exec` is used with `"bd"` as the binary, after Phase 10.
- **Detection:** CI.
- **Fallback:** Revert.

### 16.7 R-DOLT-RECOVERY-NEEDED

- **Severity:** Medium — pre-cutover dolt corruption could block migration.
- **Likelihood:** Low — dolt has been stable recently per MEMORY.md.
- **Mitigation:** Run `dolt fsck` on dolt before migration; have `feedback_dolt_recovery` ladder ready.
- **Detection:** `bd export` failure during migration.
- **Fallback:** Do dolt recovery first, then migrate.

### 16.8 R-STEALTH-MODE-BREAKAGE

- **Severity:** Medium — stealth mode currently relies on `--db` flag injection into bd; new path must preserve zero-footprint behavior.
- **Likelihood:** Low — `state.db` already lives at `~/.oro/projects/s-<hash>/state.db` for stealth.
- **Mitigation:** Stealth mode test in Phase 2 acceptance.
- **Detection:** New stealth project fails to track beads.
- **Fallback:** Hotfix path resolution.

### 16.9 R-EXPORT-DOES-NOT-MATCH-BD-FORMAT

- **Severity:** Low — tools that consume `bd export` output may break if our JSONL format diverges.
- **Likelihood:** Low — we deliberately emit bd-compatible JSON.
- **Mitigation:** Export parity test: compare `bd export` against `oro bead export` after migration. A native import round-trip is deferred until `oro bead import` ships.
- **Detection:** Export parity diff fails.
- **Fallback:** Adjust the export format to match bd's exactly.

### 16.10 R-FK-CASCADE-SURPRISES

- **Severity:** Low — `ON DELETE CASCADE` on bead_deps could remove deps when a bead is deleted in unexpected ways.
- **Likelihood:** Very low — we don't delete beads in the dispatcher path; closure is a status update.
- **Mitigation:** No DELETE statements in the dispatcher; soft-delete via `deleted=1` flag.
- **Detection:** Audit.
- **Fallback:** N/A.

### 16.11 R-SHIM-LIFETIME-CREEP — RETIRED in v15

- v9–v14 carried this risk because the shim was a real artifact at risk of permanent retention. **v15 deletes the shim entirely** (§7.3 cutover protocol). The risk no longer applies. Slot retained as 16.11 for changelog continuity; the section header notes the retirement.

### 16.12 R-OPS-PROMPT-DRIFT

- **Severity:** Medium — ops prompts containing stale `bd …` instructions could mis-route ops agents.
- **Likelihood:** Low if Phase 6 is done thoroughly.
- **Mitigation:** Prompt golden-files regen + diff review.
- **Detection:** Ops review failures with weird command attempts.
- **Fallback:** Quick prompt fix.

### 16.13 R-RECOVERY-LADDER-GAP (NEW, surfaced by v2 premortem)

- **Severity:** Medium — if Phase 8 or Phase 9 fail mid-flight, operators are between two recovery doctrines: the dolt ladder no longer applies because the migration is half-done, and a SQLite-recovery ladder hasn't been written yet.
- **Likelihood:** Low (each phase has explicit gates) but consequences are confusion at the worst time.
- **Mitigation:** Phase 0 deliverable adds `docs/runbooks/beadstore-recovery.md` with these scenarios:
  1. **Migration aborted mid-import.** Recovery: treat any non-zero migration error count as a possible partial SQLite import, because row-level insert failures can be collected while other rows commit. Preserve the failed `state.db` and command transcript, then restore a known-good pre-migration `state.db` SQLite backup snapshot or move the failed DB aside before retrying the full gate sequence.
  2. **Reconcile produced corrupt SQLite.** Recovery: preserve `OroHome/migrations/<ts>-pre-reconcile-sqlite.jsonl` for audit/recovery tooling, restore `state.db` from an operator-taken SQLite backup snapshot, then rerun reconcile after fix. `oro bead import` is still a stub and `migrate-from-dolt --from-jsonl` is not an in-place SQLite restore command.
  3. **Cutover flipped to sqlite, then a critical bug found.** Recovery: stop dispatcher and workers; export SQLite with `oro bead export`; run `bd import --dry-run` and `bd import` so bd has SQLite-side writes; only then set `ORO_BEADSOURCE_MODE=cli` and restart dispatcher and workers. Documented step-by-step in §17.
  4. **bd/Dolt disagrees with native during cutover.** Recovery: treat bd/Dolt as
     import source and audit trail, not as the veto. If native validation passes,
     record the divergence and proceed. If native validation fails, stop the
     dispatcher/workers, preserve `state.db`, and fix the native store or restore
     the recorded SQLite backup if data corruption is proven.
  5. **Dolt destroyed before or during import** (e.g., another operator runs `bd init --force`). Recovery: choose an operator-reviewed JSONL snapshot, rerun the matching dry-run gate before any apply (`./oro bead migrate-from-dolt --dry-run --from-jsonl <path>` for JSONL fallback), and apply JSONL fallback only against a clean target DB.
- The dolt ladder entries in `MEMORY.md` stay through Phase 10. Only at Phase 10 do we mark them "superseded by docs/runbooks/beadstore-recovery.md" — and only if the new runbook has been exercised in at least one drill.
- **Detection:** any operator question of the form "what do I do when X" should map to a runbook entry.
- **Fallback:** if a scenario isn't in the runbook, document it as soon as it's resolved and add to the runbook.

### 16.14 R-TWO-DISPATCHERS (v9: colocated with state.db)

- **Severity:** Medium — two dispatcher processes against the same `state.db` cause double-assignment, zombie workers, in-memory state divergence. Today partly hidden by bd's separate-process model; SQLite-native makes the issue more visible (and the symptoms cleaner — SQLite serializes writes, but dispatchers' in-memory caches diverge).
- **Likelihood:** Low in normal operation; medium during operator chaos (forgotten dispatcher process, restart races, accidental second `oro start` against the same DB).
- **Scope (v11 — codex round-3 finding #3):** the lock is **per-inode**, not per-string-path. **Path: `filepath.EvalSymlinks(StateDBPath) + ".lock"`** (with `filepath.Abs` fallback for not-yet-existent files). Standard mode: `~/.oro/projects/<project>/state.db.lock`. Stealth: `~/.oro/projects/s-<hash>/state.db.lock`. Two DBs in the same directory get distinct locks; two paths that symlink to the same DB get the same lock.
  - **Path-fix history:** v6 used `~/.oro/dispatcher.lock` (global) — broke multi-project. v7/v8 used `<OroProjectDir>/dispatcher.lock` — broke same-project-different-checkout. v9 used `filepath.Dir(StateDBPath)/dispatcher.lock` — collided for sibling DBs in one directory. v10 used `StateDBPath + ".lock"` — distinct strings via symlinks produced distinct locks. v11 canonicalizes via `EvalSymlinks` — one lock per inode, which is the correct invariant.
- **Mitigation:**
  - PID-file lock at `filepath.EvalSymlinks(StateDBPath) + ".lock"` containing the running dispatcher's PID. Second `oro start` against the same DB checks the lock; if the PID is alive, abort with: `oro: dispatcher already running against state.db at <canonical-path> (PID N); stop it first or remove stale lock`.
  - Stale-lock detection: if the PID is dead OR the lock file's mtime is >1h old (PID-recycle defense per v8), the new instance acquires the lock cleanly (after logging the reclamation).
  - **Three integration tests** for the lock invariant:
    - (a) two dispatchers concurrently against the same `state.db` (same project, same checkout) → second exits 65.
    - (b) dispatchers for two different projects (different `StateDBPath` values) → both succeed.
    - (c) two checkouts of the same project (same `state.db`, different repo paths) → second exits 65. **This test is the one that catches the v7/v8 bug.**
- **Detection:** lock file presence; `oro doctor` reports if a stale lock is found in the current project's DB directory.
- **Fallback:** explicit operator action (kill the orphan, remove the lock).

---

## 17. Rollback Plan

Phased reversibility:

| Phase | Rollback | Cost |
|---|---|---|
| Phases 1–7 (code shipped, mode `cli`) | Revert code; bd remains authoritative the whole time | Engineering time |
| Phase 8 (migration imported, before sqlite restart) | Stop dispatcher and workers if any were started; preserve current `state.db`; restore the recorded SQLite backup only if data corruption is proven | <15 min |
| Phase 9 (native SQLite authority) | Stop dispatcher and workers; preserve current `state.db`, WAL/SHM, command transcript, and logs; restore the recorded SQLite backup only if data corruption is proven. If bd must be used for forensic recovery, first export SQLite with `oro bead export` so native-side writes are not silently lost. | <30 min for backup restore; longer if forensic bd reimport is required |
| Phase 10 (cleanup deletions merged) | Revert deletion commit; reinstall bd; reconcile data | Hours-to-day depending on data drift |

The key principle: **do not let bd/Dolt veto cutover after native validation is
clean.** Reversibility comes from recorded SQLite backups and JSONL audit
exports, not from a long-running bd-primary shadow period.

---

## 18. Success Metrics

### 18.1 Pre-migration baselines (capture before Phase 8)

- **B1:** Bead-operation latency p50 / p95: `Ready()`, `Show()`, `Close()`, `Update()`. Measure via dispatcher telemetry.
- **B2:** Dispatcher startup time (cold).
- **B3:** Failed bead-state operations per week (count of bd CLI errors, dolt recovery invocations).
- **B4:** Stuck-bead incidents per week.
- **B5:** Manual operator interventions per week related to bd or dolt.

### 18.2 Post-migration thresholds (measure during native validation and normal operation)

- **K1:** Bead-operation latency p95 ≤ B1's p95 × 0.5 (we expect roughly 2× speedup from in-process SQLite vs subprocess + dolt). Soft target.
- **K2:** Dispatcher startup ≤ B2 (no regression).
- **K3:** **Zero bd-related incidents** post-Phase-9. Hard target. The whole point of this work.
- **K4:** **Zero dolt-recovery invocations.** Hard target.
- **K5:** MEMORY.md's three dolt-recovery entries can be archived as historical. Hard target.

### 18.3 Counter-metrics

- **C1:** No new SQLite-related issues that didn't exist before.
- **C2:** Bead-data integrity verified by spot-check: 10 random beads chosen from pre-migration backup, compared field-by-field against post-migration state.

---

## 19. Open Questions

### 19.1 Required before Phase 0

- **Q1:** Audit confirmation that dolt's branching/remotes are unused. (Owner: engineer doing Phase 0. Quick command: `dolt branch && dolt log --oneline | head -50`.)

### 19.2 Required during Phase 1

- **Q2:** Schema review with one other engineer. Sign-off on column types and constraints.
- **Q3:** Confirm `pkg/protocol` initializes SQLite in WAL mode. If not, add it as part of this migration.

### 19.3 Required during Phase 2

- **Q4:** `oro bead` vs `oro b` — do we want a short alias? Operator ergonomics question. Proposed: ship full `bead`, add `b` alias if asked.
- **Q5:** Output format for human-readable mode. Match bd's tabular output, or design new? Proposed: match bd to minimize cognitive load.

### 19.4 Required during Phase 4

- **Q6:** Resolved on 2026-04-30. The current Phase 8 gate does not require a
  24-hour shadow soak; it uses the native validation gate in
  `docs/runbooks/beadstore-native-cutover.md`.

### 19.5 Required during Phase 8

- **Q7:** Migration timing. Is there a quiet window? Proposed: weekend / off-hours.

### 19.6 Strategic / out-of-scope-for-this-spec

- **Q8:** Multi-machine sync. Today there's no oro cluster. If that changes, the SQLite source-of-truth needs a sync strategy (Litestream, periodic export to S3, etc.). Out of scope until needed.
- **Q9:** Schema evolution discipline. Once `pkg/protocol` owns the bead schema, we own the migration story for it. Ensure migration helpers are well-documented.
- **Q10:** Bead identifiers. We preserve bd's hierarchical IDs ("oro-7nzy", "mg-007.2.1"). Do we want to switch to UUIDs in the future? Defer; not blocking.

---

## 20. Compatibility With the Integration Spec

The companion document `docs/plans/2026-04-27-external-tooling-integration-spec.md` is largely orthogonal to this one. Specific interactions:

### 20.1 The cards table (companion spec §4.5, Phase 4)

The cards table is added to the same `state.db`. It coexists cleanly with bead tables — no FK relationship is required; cards may reference beads via tags but it's optional.

### 20.2 The `oro readiness` command (companion spec §7.7)

Add a new readiness criterion: "bead store is SQLite (not dolt)." Trivial; one extra check.

### 20.3 Phase ordering

This spec lands **before** the companion's Phase 4 (cards). Reason: the schema migration system gets exercised first on a clean change (adding bead tables), then again with cards. The cards migration is simpler to debug if the schema infra has just been validated.

### 20.4 Bead schema delta from the companion spec

Companion spec §A.5 proposed adding `Research`, `TargetSymbol`, `TargetPath`, `FastEdit` fields to a Bead struct. With this spec, those become native columns in the `beads` table:

```sql
ALTER TABLE beads ADD COLUMN research INTEGER NOT NULL DEFAULT 0;
ALTER TABLE beads ADD COLUMN target_symbol TEXT;
ALTER TABLE beads ADD COLUMN target_path TEXT;
ALTER TABLE beads ADD COLUMN fast_edit INTEGER NOT NULL DEFAULT 0;
```

Add this as part of the companion spec's Phase 5 (sandbox) or as a small follow-up migration. No conflict with this spec's schema.

### 20.5 No dependencies on AGPL or external tools

This spec touches no AGPL software. tldr, ouros, fastedit are all separately scoped to the companion spec. This work can ship completely on its own.

---

## 21. Approval Sign-Off

| Gate | Approver | Required by |
|---|---|---|
| Spec accepted | Engineering lead | Phase 0 start |
| Schema reviewed | One other engineer | Phase 1 start |
| Phase 4 (shadow) results | Engineering lead | Phase 8 start |
| Migration day go/no-go | Engineering lead + operator | Phase 8 start |
| Phase 9 → Phase 10 (cleanup) | Engineering lead | Phase 10 start |

**Spec review checklist:**

- [ ] §3 diagnosis matches lived experience.
- [ ] §4 alternatives considered match the actual tradeoff space.
- [ ] §6 schema covers all bead fields used in oro today.
- [ ] §11 touch-point inventory is complete.
- [ ] §12 phasing fits team capacity.
- [ ] §16 risk register is complete.
- [ ] §17 rollback story is acceptable.
- [ ] §18 success metrics are measurable.
- [ ] §19 open questions are resolved or deferred consciously.

---

## Appendix A — File-Path Index

**New files (Phase 1–3):**

- `pkg/beadstore/store.go` — `Store` interface + `CreateParams`
- `pkg/beadstore/sqlite.go` — `SQLiteStore` implementation
- `pkg/beadstore/sqlite_test.go`
- `pkg/beadstore/shadow.go` — `ShadowStore` (read-only validation)
- `pkg/beadstore/shadow_test.go`
- `pkg/beadstore/migrate.go` — dolt → sqlite migration + `--reconcile`
- `pkg/beadstore/migrate_test.go`
- `pkg/beadstore/testfake.go` — `FakeStore` for tests
- `pkg/beadstore/testdata/beads/*.jsonl` (fixtures)
- `cmd/oro/cmd_bead.go` — `oro bead` Cobra subcommand tree
- `cmd/oro/cmd_bead_test.go`
- `cmd/oro/cmd_bead_migrate.go`
- `cmd/oro/cmd_bead_migrate_test.go`
- `pkg/protocol/bead_schema.sql` (or inline DDL in schema.go)
- `docs/runbooks/beadstore-recovery.md` — recovery scenarios per §16.13 (5 numbered scenarios from migration-aborted-mid-import through dolt-destroyed-during-shadow)
- ~~`scripts/bd-shim.go`~~ — **dropped in v15.** No shim, no translation table, no extraction harness. See §7.3 cutover protocol.
- `pkg/dispatcher/lock.go` and `lock_test.go` — single-dispatcher PID lock per §16.14

**Modified files:**

- `pkg/protocol/schema.go` (+ migration #11)
- `pkg/dispatcher/dispatcher.go` (replace `BeadSource` interface declaration with `beadstore.Store` consumption; replace bead-source construction with `selectStore(...)`. v15: no type alias because the reshape changes the interface shape; Phase 1 rename pass updates ~30 call sites.)
- `pkg/dispatcher/beadsource.go` (mark `CLIBeadSource` legacy; verify it satisfies `beadstore.Store`)
- `pkg/dispatcher/health.go` (line 51: replace `bd dolt status` with SQLite ping)
- `pkg/mg/data/source.go` (rewrite to take `beadstore.Store` via constructor)
- `pkg/mg/data/mutate.go` (same)
- `pkg/worker/prompt.go` (lines 109–119, 144, 154, 164–165, 234–236, 294–300)
- `pkg/ops/escalation_prompt.go` (lines 85, 87, 104, 114, 116, 123, 124, 126, 132–136)
- `pkg/ops/ac_prompt.go` (lines 49, 51, 84)
- `pkg/ops/decompose_prompt.go` (lines 27, 31–34)
- `pkg/ops/epic_fix_prompt.go` (line 27)
- `cmd/oro/manager.go` (lines 32, 55–71, 104–110)
- `cmd/oro/architect.go` (per inventory)
- `cmd/oro/cmd_init.go` (line 76: remove bd from tool list)
- `cmd/oro/cmd_doctor.go` (remove dolt corruption detection; add SQLite integrity check)
- `cmd/oro/cmd_cleanup.go` (remove `pgrep dolt`)
- `cmd/oro/cmd_stop.go` (line 291: remove dolt flush)
- `cmd/oro/cmd_uninstall.go` (use `LegacyBeadsDir`; do not remove)
- `cmd/oro/paths.go` (line 69: rename `BeadsDir` → `LegacyBeadsDir`; retained per §11.10)
- `assets/hooks/bd_create_notifier.py`
- `assets/hooks/notify_manager_on_bead_create.py`
- `assets/hooks/session_start_extras.py`
- `assets/hooks/session_start_compact.py`
- `assets/hooks/architect_router.py`
- `assets/hooks/pre_compact.py`
- `assets/hooks/validate_agent_completion.py`
- `assets/skills/dispatching-parallel-agents/SKILL.md`
- `scripts/quality_gate.sh`
- `scripts/test_git_hooks.sh`
- `docs/INSTALL.md`
- `README.md`
- `docs/dev-setup.md`
- (~368 test files — mostly mechanical, ~50 require golden-file regen)

**Deleted files (Phase 10, deferred ≥30 days):**

- `cmd/oro/cmd_dolt.go`
- `cmd/oro/cmd_bd.go`
- `cmd/oro/port_registry.go`
- `pkg/dispatcher/dolt_recovery.go`
- `pkg/dispatcher/dolt_recovery_test.go`
- `pkg/dispatcher/cli_beadsource.go` (after extracting any shared helpers)

---

## Appendix B — DDL Bundle

Full DDL for the new tables, ready to paste into `pkg/protocol/bead_schema.sql`:

```sql
-- Migration #11: bead schema.
-- Applied via pkg/protocol/schema.go:MigrateBeadSchema.

CREATE TABLE IF NOT EXISTS beads (
    id                    TEXT PRIMARY KEY,
    title                 TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    acceptance_criteria   TEXT NOT NULL DEFAULT '',
    -- v18: ready is derived via views; blocked is stored for manual/imported
    -- rows and also derived for dependency-blocked open rows.
    status                TEXT NOT NULL CHECK (status IN
                          ('open','in_progress','blocked','closed')),
    priority              INTEGER NOT NULL DEFAULT 2,
    type                  TEXT NOT NULL DEFAULT 'task',
    parent_id             TEXT REFERENCES beads(id),
    owner                 TEXT,
    estimated_minutes     INTEGER,
    tier                  TEXT,
    model                 TEXT,
    deferred_until        TEXT,
    close_reason          TEXT,
    created_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    closed_at             TEXT,
    deleted               INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_beads_status     ON beads(status) WHERE deleted = 0;
CREATE INDEX IF NOT EXISTS idx_beads_parent     ON beads(parent_id) WHERE deleted = 0;
CREATE INDEX IF NOT EXISTS idx_beads_type       ON beads(type) WHERE deleted = 0;
CREATE INDEX IF NOT EXISTS idx_beads_priority   ON beads(priority) WHERE deleted = 0;
CREATE INDEX IF NOT EXISTS idx_beads_deferred   ON beads(deferred_until) WHERE deleted = 0;

CREATE TABLE IF NOT EXISTS bead_deps (
    bead_id          TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    depends_on_id    TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    type             TEXT NOT NULL DEFAULT 'blocks',
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    created_by       TEXT,
    PRIMARY KEY (bead_id, depends_on_id, type)
);
CREATE INDEX IF NOT EXISTS idx_bead_deps_depends_on ON bead_deps(depends_on_id);

CREATE TABLE IF NOT EXISTS bead_tags (
    bead_id    TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    tag        TEXT NOT NULL,
    PRIMARY KEY (bead_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_bead_tags_tag ON bead_tags(tag);

CREATE TABLE IF NOT EXISTS bead_labels (
    bead_id    TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    label      TEXT NOT NULL,
    PRIMARY KEY (bead_id, label)
);
CREATE INDEX IF NOT EXISTS idx_bead_labels_label ON bead_labels(label);

CREATE TABLE IF NOT EXISTS bead_metadata (
    bead_id    TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    PRIMARY KEY (bead_id, key)
);

CREATE TABLE IF NOT EXISTS bead_notes (
    id          INTEGER PRIMARY KEY,
    bead_id     TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    author      TEXT,
    content     TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_bead_notes_bead ON bead_notes(bead_id);

CREATE VIRTUAL TABLE IF NOT EXISTS beads_fts USING fts5(
    title, description, acceptance_criteria,
    content='beads', content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS beads_fts_ai AFTER INSERT ON beads BEGIN
  INSERT INTO beads_fts(rowid, title, description, acceptance_criteria)
  VALUES (new.rowid, new.title, new.description, new.acceptance_criteria);
END;

CREATE TRIGGER IF NOT EXISTS beads_fts_ad AFTER DELETE ON beads BEGIN
  INSERT INTO beads_fts(beads_fts, rowid, title, description, acceptance_criteria)
  VALUES ('delete', old.rowid, old.title, old.description, old.acceptance_criteria);
END;

CREATE TRIGGER IF NOT EXISTS beads_fts_au AFTER UPDATE ON beads BEGIN
  INSERT INTO beads_fts(beads_fts, rowid, title, description, acceptance_criteria)
  VALUES ('delete', old.rowid, old.title, old.description, old.acceptance_criteria);
  INSERT INTO beads_fts(rowid, title, description, acceptance_criteria)
  VALUES (new.rowid, new.title, new.description, new.acceptance_criteria);
END;

-- Child-table touch triggers: keep beads.updated_at honest so reconcile's
-- last-writer-wins logic doesn't get fooled by writes that only touched
-- children (deps, tags, labels, metadata, notes). See §9.7.
-- v9 fix per codex finding #10: every child table has AI + AU + AD triggers.
-- v8 spec only had AU on bead_metadata. UPDATE bead_deps/tags/labels/notes
-- (e.g. UPDATE bead_deps SET type=... or UPDATE bead_notes SET content=...)
-- now correctly bumps parent updated_at, preserving reconcile's LWW contract.

CREATE TRIGGER IF NOT EXISTS bead_deps_touch_parent_ai AFTER INSERT ON bead_deps BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
-- v10 fix per codex round-2 #5: AU triggers add WHEN guards so no-op rewrites
-- (UPDATE bead_tags SET tag=tag) don't bump parent updated_at and corrupt the
-- LWW contract. Each AU trigger fires only when at least one significant
-- column actually changes.

CREATE TRIGGER IF NOT EXISTS bead_deps_touch_parent_au AFTER UPDATE ON bead_deps
  WHEN old.type IS NOT new.type
    OR old.depends_on_id IS NOT new.depends_on_id
    OR old.created_at IS NOT new.created_at
    OR old.created_by IS NOT new.created_by
BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_deps_touch_parent_ad AFTER DELETE ON bead_deps BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = old.bead_id;
END;

CREATE TRIGGER IF NOT EXISTS bead_tags_touch_parent_ai AFTER INSERT ON bead_tags BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_tags_touch_parent_au AFTER UPDATE ON bead_tags
  WHEN old.tag IS NOT new.tag
BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_tags_touch_parent_ad AFTER DELETE ON bead_tags BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = old.bead_id;
END;

CREATE TRIGGER IF NOT EXISTS bead_labels_touch_parent_ai AFTER INSERT ON bead_labels BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_labels_touch_parent_au AFTER UPDATE ON bead_labels
  WHEN old.label IS NOT new.label
BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_labels_touch_parent_ad AFTER DELETE ON bead_labels BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = old.bead_id;
END;

CREATE TRIGGER IF NOT EXISTS bead_metadata_touch_parent_ai AFTER INSERT ON bead_metadata BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_metadata_touch_parent_au AFTER UPDATE ON bead_metadata
  WHEN old.value IS NOT new.value OR old.key IS NOT new.key
BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_metadata_touch_parent_ad AFTER DELETE ON bead_metadata BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = old.bead_id;
END;

CREATE TRIGGER IF NOT EXISTS bead_notes_touch_parent_ai AFTER INSERT ON bead_notes BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_notes_touch_parent_au AFTER UPDATE ON bead_notes
  WHEN old.content IS NOT new.content
    OR old.author IS NOT new.author
    OR old.created_at IS NOT new.created_at
BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = new.bead_id;
END;
CREATE TRIGGER IF NOT EXISTS bead_notes_touch_parent_ad AFTER DELETE ON bead_notes BEGIN
  UPDATE beads SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = old.bead_id;
END;

CREATE VIEW IF NOT EXISTS beads_ready AS
SELECT b.*
FROM beads b
WHERE b.deleted = 0
  AND b.status = 'open'
  AND (b.deferred_until IS NULL OR b.deferred_until = '')
  AND NOT EXISTS (
    SELECT 1 FROM assignments a
    WHERE a.bead_id = b.id
      AND a.status = 'active'
  )
  AND NOT EXISTS (
    SELECT 1 FROM bead_deps d
    LEFT JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
    WHERE d.bead_id = b.id
      AND d.type IN ('blocks','conditional-blocks','parent-child')
      AND (parent.id IS NULL OR parent.status != 'closed')
  );

CREATE VIEW IF NOT EXISTS beads_blocked AS
SELECT b.*
FROM beads b
WHERE b.deleted = 0
  AND b.status IN ('open','blocked')
  AND (
    b.status = 'blocked'
    OR b.deferred_until IS NULL
    OR b.deferred_until = ''
    OR EXISTS (
      SELECT 1 FROM bead_deps d
      LEFT JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
      WHERE d.bead_id = b.id
        AND d.type IN ('blocks','conditional-blocks')
        AND (parent.id IS NULL OR parent.status != 'closed')
    )
  )
  AND NOT EXISTS (
    SELECT 1 FROM assignments a
    WHERE a.bead_id = b.id
      AND a.status = 'active'
  )
  AND (
    b.status = 'blocked'
    OR EXISTS (
      SELECT 1 FROM bead_deps d
      LEFT JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
      WHERE d.bead_id = b.id
        AND d.type IN ('blocks','conditional-blocks','parent-child')
        AND (parent.id IS NULL OR parent.status != 'closed')
    )
  );
```

---

## Appendix C — `oro bead` CLI Reference

Compact reference card for operators and worker prompts.

```
oro bead ready                              # list unblocked open beads
oro bead list [--status=open|in_progress|closed]
              [--parent=<id>]
              [--tag=<tag>]
              [--limit=N]
              [--json]
oro bead show <id> [--long] [--json]
oro bead create --title='...'
                [--type=task|bug|epic|research|chore]
                [--priority=N]                       # default 2; 0 = highest
                [--parent=<id>]
                [--description='...']
                [--acceptance='...' | --acceptance-criteria='...']  # aliases
                [--estimate=<minutes>]               # persists to beads.estimated_minutes
                [--tag=<tag>] (repeatable)
                [--id=<explicit-id>]                 # idempotent
oro bead update <id> [--status=...]
                     [--priority=...]
                     [--type=...]
                     [--parent=<id>]
                     [--notes='...']
                     [--acceptance='...']
                     [--owner='...']
oro bead close <id> [--reason='...']
oro bead reopen <id>
oro bead defer <id> --until=<iso8601>
oro bead undefer <id>                       # clears deferred_until
oro bead dep add <bead-id> <depends-on-id> [--type=blocks|conditional-blocks|related-to]
oro bead dep rm  <bead-id> <depends-on-id>
oro bead dep list <bead-id>
oro bead tag  add <bead-id> <tag>...
oro bead tag  rm  <bead-id> <tag>...
oro bead meta set <bead-id> <key>=<value>
oro bead meta get <bead-id> <key>
oro bead meta rm  <bead-id> <key>
oro bead note add <bead-id> '...'
oro bead note list <bead-id>
oro bead search <query>                     # FTS5 over title/description/AC
oro bead export [--out=path] [--format=jsonl|json]
oro bead import <path>                     # planned; current shipped command is a stub
oro bead doctor
oro bead status                             # open / in_progress / closed counts
oro bead migrate-from-dolt [--dry-run] [--reconcile] [--apply] [--from-jsonl <path>] [--from-fixture <path>] [--ignore-version-drift] [--allow-running-dispatcher] [--force-recover]
```

---

## Appendix D — Glossary

- **bd** — the external `beads` CLI binary (`github.com/steveyegge/beads/cmd/bd`), used today via `os/exec`.
- **`pkg/beadstore`** — the new Go package introduced by this spec, owning the `Store` interface and the `SQLiteStore` / `ShadowStore` / `FakeStore` implementations and migration tooling.
- **`Store`** — the bead-source interface, defined in `pkg/beadstore/store.go`. **12 methods** (v15/v16 reshape; v17 corrected). Single `protocol.Bead` return type for all reads. `Create` takes `CreateParams` and returns `(*protocol.Bead, error)`. `Update` takes `UpdateParams` (pointer-fields = "set"). No `Sync`. `HasChildren` and `AllChildrenClosed` are distinct methods.
- **BeadSource** — legacy interface in `pkg/dispatcher/dispatcher.go`. Retained as the implementation backing `CLIBeadSource` until Phase 10; v15/v16 do **not** alias it to `beadstore.Store` because the shapes differ. Phase 1 includes a one-shot mechanical rename pass (~30 call sites) to switch from `BeadSource` to `beadstore.Store`.
- **`BeadDetail = Bead` alias** — Phase 1 introduces `type BeadDetail = Bead` in `pkg/protocol/types.go` after extending `Bead` with the 5 runtime fields (per §8.2.1). Phase 10 removes the alias.
- **CLIBeadSource** — legacy implementation in `pkg/dispatcher/beadsource.go`; shells out to bd. Adapted in Phase 1 to satisfy `beadstore.Store` (the reshaped interface). Retained through Phase 10; deleted in Phase 10 cleanup pass.
- **SQLiteStore** — the new implementation in `pkg/beadstore/sqlite.go`; reads/writes oro's `state.db`.
- **ShadowStore** — wrapper in `pkg/beadstore/shadow.go` running primary and secondary `Store`s on reads, primary-only on writes. Used during the read-only shadow validation window.
- **Dolt** — bd's storage engine; SQL with git-like versioning; not used after this migration.
- **state.db** — oro's native SQLite at `~/.oro/state.db` or `~/.oro/projects/<hash>/state.db`.
- **Stealth mode** — projects without `.beads/` in the repo; storage under `~/.oro/projects/s-<hash>/`.
- **`LegacyBeadsDir`** — renamed from `BeadsDir`. Resolves the pre-replatform `.beads/` directory. Retained for migration tooling, `oro uninstall`, `oro doctor` orphan detection. Removed in Phase 11.
- **WAL** — SQLite Write-Ahead Logging; enables concurrent readers with one writer.
- **FTS5** — SQLite's built-in full-text search.
- **Migration #11** — the new schema migration this spec adds (`MigrateBeadSchema`).
- **Shadow window** — legacy validation design where bd was authoritative for
  reads and writes while SQLite validated parity. Superseded for cutover by the
  native-first Phase 9 evidence gate.
- **Read-only shadow** — the v2 design choice: shadow mode never mirrors writes
  to SQLite. Kept as historical recovery context, not the current cutover gate.
- **`--reconcile`** — the migration-tool flag that applies a week's worth of bd-only writes onto SQLite before cutover. Idempotent; gated by `--apply`.
- **Cutover** — the moment `ORO_BEADSOURCE_MODE` flips from `shadow` to `sqlite`.
- **Phase 10** — first cleanup pass; deletes dolt management code; retains `LegacyBeadsDir`. Runs after Phase 9 native evidence passes.
- **Phase 11** — second cleanup pass; removes `LegacyBeadsDir` and the migration tool. Runs ≥1–2 release cycles after Phase 10.

---

*End of spec.*
