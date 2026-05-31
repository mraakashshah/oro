# Oro Memory Recall Deepspec (re-grounded) — Spec 08

**Date:** 2026-05-31
**Status:** Design. None of the four phases are implemented.
**Re-grounds:** `archive/yap/reference/_deepdive/specs/08-memory-recall.md` (50KB, read in full). That spec's design is strong but its current-state `file:line` anchors have DRIFTED against `main`, and the legacy `pkg/memory` package was retired (commit `d9c7cd63`) — taking the concrete ONNX embedder with it. This doc re-verifies every anchor against current code (2026-05-31) and adapts the design to today's reality.

The four-phase shape is preserved:
- **Phase 1** (zero ML, ships first): SEE-ALSO relation graph — new `card_relations` + `card_symbols` tables, additive multi-signal ranking (call=3 / comention=2 / namespace=1, accumulate on conflict), Tier-1-(file-path)-first `ResolveCallee` in `pkg/codestruct`, lineage traversal, `[[wikilink]]` parsing, context-carrying `card_events` (add `session_id`, write `view` events), and finally feeding the dead `SymbolHints` signal.
- **Phase 2**: vector recall fused not swapped — `embedding`/`embedding_model` columns on `cards`, a concrete ONNX embedder in new `pkg/embed` reusing the dispatcher's dormant `Embedder` seam + pinned bge-small-en-v1.5 artifacts, RRF fusion (k=60) + 0.7/0.3 cosine blend + floor-gated bounded boosts, finishing the dead `RouteSemantic`, optional cross-encoder rerank (tail-preserve, fail-open).
- **Phase 3**: Dream propose → grade → calibrate — grade lifecycle columns on `cards`, proposals queue (excluded from recall) instead of direct-apply, an oro-written grade prompt + evidence retriever, a confidence gate (default OFF), calibration scorecard, voice-gate on generated prose; explicitly reconciled with the existing `bead_learnings_pending` + `DecidePromotion` flow.
- **Phase 4**: continuous hook-driven capture — PostToolUse buffer hook (fail-open, privacy-strip), async batch compression on Stop/flush reusing the Haiku extract path instead of only the trailing-50KB-at-task-end.

---

## Drift corrections (archived → actual, VERIFIED today)

The single most important reality the archived spec did not capture: **the legacy `pkg/memory` package was retired in commit `d9c7cd63 refactor(memory): retire legacy package`, taking the concrete ONNX embedder with it.** The `Embedder` interface, `embedderFactory`, and warm-up gate still live in `pkg/dispatcher/dispatcher.go` but are DORMANT and unconsumed — no factory is registered, so `WaitForEmbedder` returns `ErrSemanticDisabled`. This is the central thing Phase 2 must rebuild.

| Archived anchor | Actual (verified 2026-05-31) |
| --- | --- |
| `pkg/cards/store.go:357` `relevanceScore` | `pkg/cards/store.go:497` (`relevanceScore`); helpers below it: `jaccardSimilarity`:506, `wordOverlap`:532, `tokenize`:554, `symbolOverlap`:567, `beadTypeMatch`:584 |
| `pkg/cards/store.go:379` `Relevant` | `Relevant` is at `store.go:379` (matches); `scoreCardForRelevance`:409, `collectScoredCards`:427, `buildInlined`:478. Also a `readTxImpl.Relevant`:995 |
| `EffectiveScore` location | `pkg/cards/scoring.go:68`; `DecayMultiplier`:36, `SuppressionMultiplier`:49, `scoreDelta`:91, constants `ScoreCap=5/ScoreFloor=-2/AutoRetireThresh=-1/DefaultThreshold=0.1`:9-14 |
| `pkg/dispatcher/dispatcher.go:2899` `handleDreamResult` | `handleDreamResult` is at `dispatcher.go:2899` (matches) and **applies directly** via `dreamExecuteFn` → `memoryServices.ExecuteDream` (:2909-2922). Triggers: `maybeTriggerDream`:2834, `triggerDream`:2855, `maybeConsolidateMemory`:2805, `dumpMemoriesForDream`:2876, `ParseDreamActions`:180. `MemoryServices`:130, `DreamAction`:130-ish (`Op/IDs/CardType/Tags/Content`) |
| concrete ONNX embedder exists | **RETIRED (d9c7cd63).** Dormant seam at `dispatcher.go`: `Embedder` iface:98 (`Embed/Dim/Name`), `Reranker` iface:93, `embedderFactory`:785, `rerankerFactory`:792, `embedderReady chan`:783, `warmupEmbedder`:1098, `WaitForEmbedder`:1079, `SemanticModelDir` cfg:640, `ErrSemanticDisabled`:80, `ErrEmbedderUnavailable`:84. No `pkg/embed`, no `pkg/memory` |
| Phase 3 builds on nothing | A `bead_learnings_pending` table (`schema.go:30`) + `DecidePromotion` (`promotion.go:35`, promote/defer/reject) ALREADY EXIST. They are NOT the spec's grade/calibrate loop — Phase 3 reconciles with them (see Design 5.3) |

Other corrected/confirmed realities:

- **Card kinds enum = `rule | taste | pattern | decision | fact`** (`cards.go:26-32` `CardType*`; SQL CHECK at `schema.go:8`). NOT "fact/pattern/gotcha/decision/convention/glossary". `isValidCardType`:98. The worker memory extractor (`memory_boundary.go:113`) only ever emits `pattern` or `decision`.
- **`SymbolHints` already exists** on `RelevanceQuery` (`cards.go:179`) but is **unfed at every call site**: `pkg/dispatcher/assign_payload.go:111` `buildCardContext` and `cmd/oro/cmd_current.go:170` `beadRelevanceQuery` both leave it empty (verified: repo-wide `SymbolHints` appears only in `cards.go`, `store.go`, and tests). So `symbolOverlap` (`store.go:567`) is dead today.
- **`card_events` columns = `(id, card_id, ts, bead_id, actor, kind, payload)`** (`schema.go:46-54`) — already carries `bead_id` + `payload`, but **NO `session_id`**. Events are written only on `created`/`retired`/score-mutating kinds (`store.go:663`, `:876`, `RecordCardEvent`:789) and never read back for ranking. Phase 1 adds `session_id` + `view` events.
- **Schema is a single idempotent `schemaDDL` const** (`schema.go:5-57`), NOT versioned migrations. SQLite has no `ADD COLUMN IF NOT EXISTS`, so new columns need a Go-side `pragma_table_info` guard (the spec's "additive ALTER" posture, but there is no existing migration helper to mirror — build one).
- **`chunks` (codesearch) has NO vector column** (`index.go:43-53`: id, file_path, name, kind, start_line, end_line, content, updated_at; + `chunks_fts` FTS5 virtual table + sync triggers). `RouteSemantic` is a dead stub: `ClassifyQuery` (`classifier.go:119`) returns `QuerySemantic` for natural-language prefixes (:76-90) and `RouteGrep` (`router.go:46`) maps it to `RouteSemantic` (:54), but **nothing consumes `RouteSemantic`** — there is no `SearchSemantic`/handler at all. `CodeIndex.reranker` (`index.go:21`) is the Claude-prompt reranker (`rerank.go`), not a cross-encoder.
- **BGE artifacts still pinned** (`modelartifacts/models.go:31` `KnownModels`): `bge-small-en-v1.5` SHA `828e1496…cf35` (embedder), `bge-reranker-base` SHA `15b9a8c3…6d2b`, `bge-reranker-tokenizer` SHA `9eb652ac…489e`, `bge-tokenizer` SHA `d241a60d…5c66`. `PrefetchModels`:86, `VerifyModel`:63. Dim 384 is NOT in this struct (it was on the retired embedder); `ModelSpec`:22 has `Name/URL/SHA256/Filename` only.
- **`SemanticMemoryConfig` exists, consumed by nothing meaningful** (`langprofile/config.go:42`): `Enabled *bool`, `Rerank *bool`, `ANNTopK int`, `FinalTopK int`, `ModelDir string`. `EnabledOrDefault`:53 defaults **true**, `RerankOrDefault`:63 defaults **true** — note these are marked `//oro:testonly`. `WithDefaults`:78 sets ANNTopK=50, FinalTopK=10, ModelDir=~/.oro/models. There are NO cosine/keyword/rrf fields yet (the archived spec's `CosineWeight/KeywordWeight/RRFk` do not exist).
- **`oro-search-hook` is PreToolUse only** (`cmd/oro-search-hook/main.go`): stdin JSON `{hook_type, tool_name, tool_input{file_path,offset,limit}}` (also a Codex shape), stdout `{}` (allow) or `{"permissionDecision":"deny","permissionDecisionReason":<summary>}`. `HandleHook`:91 is fail-open on every error path (:76-78). No PostToolUse, no capture buffer, no `symbol_hints` output.
- **`impact.go`** has `ComputeImpact`:27 → `ImpactResult{Symbol,File,DirectCallers,TransitiveCallers,CrossPkgCallees,ExternalCallees}`:13 (reverse-edge blast radius). No `Lineage`/`LatestInChain` anywhere — Phase 1 adds those in `pkg/cards`, not here.

---

## Problem & motivation

Today recall is a single keyword/overlap scorer over card tags + summary text, gated by effective score (decay × suppression). Concretely (`store.go:497`):

```
relevanceScore = jaccard(tags) * 0.4 + wordOverlap(summary, desc) * 0.3 + symbolOverlap(tags, hints) * 0.2 + beadTypeMatch * 0.1
```

Four gaps, mapped to the archived spec's four phases:

1. **No relation graph.** A card about `processCall` won't surface the card about `BuildCallGraph` though they're one call edge apart. `Card.SupersededBy`/`EmergedFrom` pointers (`cards.go:51-52`) exist but there is no traversal API and no inter-card edge table. The call graph (`codestruct/call_graph.go`) is used for impact only, never fed into recall.
2. **`symbolOverlap` is dead.** The field, scorer, and a test all exist, but no call site populates `SymbolHints` — so code-symbol matches contribute exactly zero.
3. **No semantic recall.** Natural-language queries classify to `RouteSemantic` (`router.go`), which dead-ends with no handler. The embedder seam exists but is dormant after the `pkg/memory` retirement.
4. **Dream applies blindly.** `handleDreamResult` (`dispatcher.go:2899`) parses `[CREATE]/[MERGE]/[DELETE]` actions and applies them directly via `dreamExecuteFn` → `memoryServices.ExecuteDream` — no judge, no confidence gate, no calibration. The only quality bar anywhere is `DecidePromotion` (`promotion.go:35`), a different, narrower heuristic flow over `bead_learnings_pending`.
5. **Capture is end-of-task only.** `ExtractMemoriesWithLLMInWorkdir` (`memory_boundary.go:70`) runs one Haiku call over the trailing `maxMemorySessionBytes = 50_000` (:20) at task end. Mid-task discoveries that scroll past 50KB are lost.

---

## Current oro state (file:line, VERIFIED today)

### `pkg/cards/schema.go` — full current DDL (verbatim, single `schemaDDL` const, lines 5-57)

```sql
CREATE TABLE IF NOT EXISTS cards (
  id                   TEXT PRIMARY KEY,
  type                 TEXT NOT NULL CHECK (type IN ('rule','taste','pattern','decision','fact')),
  title                TEXT NOT NULL,
  body_summary         TEXT NOT NULL,
  body_full            TEXT NOT NULL,
  body_deep            TEXT,
  tags                 TEXT NOT NULL DEFAULT '[]',
  score                REAL NOT NULL DEFAULT 1.0,
  promotion_confidence REAL,
  decay_anchor         TEXT NOT NULL,
  last_contradicted_at TEXT,
  last_nacked_at       TEXT,
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  retired_at           TEXT,
  superseded_by        TEXT REFERENCES cards(id),
  emerged_from         TEXT,
  retired_reason       TEXT
);
CREATE INDEX IF NOT EXISTS idx_cards_type_score ON cards(type, score DESC) WHERE retired_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_cards_tags       ON cards(tags);

CREATE TABLE IF NOT EXISTS bead_learnings_pending (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  bead_id              TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
  ts                   TEXT NOT NULL,
  candidate            TEXT NOT NULL,            -- JSON CardCandidate
  promoted_to          TEXT REFERENCES cards(id),
  rejected_at          TEXT,
  reason               TEXT,
  queued_for_review_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_learnings_bead    ON bead_learnings_pending(bead_id);
CREATE INDEX IF NOT EXISTS idx_learnings_pending ON bead_learnings_pending(promoted_to, rejected_at);
CREATE INDEX IF NOT EXISTS idx_learnings_review  ON bead_learnings_pending(queued_for_review_at)
  WHERE queued_for_review_at IS NOT NULL AND promoted_to IS NULL AND rejected_at IS NULL;

CREATE TABLE IF NOT EXISTS card_events (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  card_id   TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
  ts        TEXT NOT NULL,
  bead_id   TEXT,
  actor     TEXT NOT NULL,
  kind      TEXT NOT NULL,
  payload   TEXT
);
CREATE INDEX IF NOT EXISTS idx_card_events_card_ts ON card_events(card_id, ts);
```

(No embedding column; no relations/symbols tables; `card_events` has no `session_id`.)

### `pkg/cards` Go surface (verified)

- `cards.go`: `CardType` enum:26-32 (`rule/taste/pattern/decision/fact`), `isValidCardType`:98; `Card`:35; `CardCandidate`:57 (`Type/Title/BodySummary/BodyFull/Confidence/Evidence/Tags/Confirmed`); `PendingLearning`:69; `DeckCard`:119; `InlinedCard`:129; `CardSummary = InlinedCard`:141; `CardEvent`:144 (`CardID/BeadID/Actor/Kind/Payload`); `CardCreateParams`:153; `ListQuery`:166; `RelevanceQuery`:175 (`BeadType/BeadTags/BeadDescription/SymbolHints/MaxTokens/IncludeLowScore/IncludeSuppressed`); `RelevantCards`:186 (`Deck/Inlined`); `ReadTx`:192.
- `store.go`: `Store` iface:20 (`Relevant, Show, List, PendingLearnings, ReviewQueue, RecordCardEvent, AppendLearningPending, PromoteLearning, RejectLearning, DeferToReviewQueue, Create, Retire, WithReadTx`); `SQLiteCardStore`:39; `NewStore`:48 (runs `schemaDDL`); `Relevant`:379; `scoreCardForRelevance`:409; `collectScoredCards`:427; `buildInlined`:478; `relevanceScore`:497; `jaccardSimilarity`:506; `wordOverlap`:532; `symbolOverlap`:567 (dead); `beadTypeMatch`:584; `estimateTokens`:604 (4 chars/token); `insertCard`:628 (writes `created` event); `RecordCardEvent`:789; `Retire`:854.
- `scoring.go`: constants:9-14; `halfLifeDays`:17; `suppressionWindowDays`:26; `DecayMultiplier`:36; `SuppressionMultiplier`:49; `EffectiveScore`:68; `scoreDelta`:91 (ack +0.3 / confirmed +0.2 clears-contradiction / nack -0.5 / contradicted -0.4).
- `promotion.go`: `PromotionConfidenceThreshold = 0.7`:10; `PromotionAction`:18 (`promote/defer/reject`); `PromotionDecision`:28; `DecidePromotion(c, verdict, existing)`:35 — pure conservative rules; rule/pattern auto-promote at conf≥0.7, taste/decision defer to human, fact promotes only if `Confirmed && conf≥0.7`, near-duplicate→defer, contradiction-without-rationale→reject. **This is NOT an LLM grade/calibrate loop.**

### `pkg/codestruct` (verified, `//go:build cgo`)

- `symbol.go`: `Symbol`:18 (`Name/Kind/Receiver/Signature/LineStart/LineEnd/Visibility/Decorators`); `CallEdge`:30 (`CallerFile/CallerSymbol/CalleeName/CalleeFile/CalleeSymbol/Line/Resolved`).
- `call_graph.go`: `BuildCallGraph(files, pkgSymbols)`:22 → builds `allSymsByName` (first-writer-wins, :24-31) and `pkgDirFiles` (:35-39); `goCallWalker`:73; `processCall`:107 (identifier→`resolveSimple`; selector→import-alias→`resolvePkg` else `resolveSimple` by name only, "receiver-type disambiguation deferred to Layer 3":134); `resolveSimple`:141 (**name-first**, global `allSymsByName`); `resolvePkg`:167 (import-scoped); `extractGoImportsMap`:199 (alias→last-path-segment). There is **no file-path-first `ResolveCallee`** — resolution is name-first, the inverse of bloks's order.
- `impact.go`: `ImpactResult`:13; `ComputeImpact`:27; reverse-edge `classifyEdges`:62; `relativeRef`:153. No lineage.

### `pkg/codesearch` (verified)

- `router.go`: `GrepRoute`:4 (`RouteRipgrep/RouteAST/RouteSemantic`); `RouteGrep`:46 (maps `QuerySemantic`→`RouteSemantic` :54). **No semantic search handler exists.**
- `classifier.go`: `ClassifyQuery`:119; `semanticPrefixes`:76 ("where is", "how does", "what handles", …).
- `index.go`: `CodeIndex{db, reranker}`:18; `indexSchemaDDL`:43 (chunks + chunks_fts, **no vector col**); `NewCodeIndex`:81.

### `pkg/dispatcher` (verified, `dispatcher.go` is 9635 lines)

- Embedder seam (DORMANT): `Reranker` iface:93 (`Rerank(query, docs) []float64`), `Embedder` iface:98 (`Embed(text) []float32; Dim() int; Name() string`), `embedderFactory`:785, `rerankerFactory`:792, `embedderReady chan`:783, `warmupEmbedder`:1098 (calls `embedderFactory(cfg.SemanticModelDir)`:1116), `WaitForEmbedder`:1079, `EmbedderWaiter` iface:1071, `SemanticModelDir` cfg:640, `ErrSemanticDisabled`:80, `ErrEmbedderUnavailable`:84.
- Dream path (DIRECT-APPLY): `MemoryServices{..., Consolidate, ExecuteDream, ...}`:130; `ParseDreamActions`:180 + regexes `dreamDeleteRe`:174 / `dreamCreateRe`:175 / `dreamMergeRe`:176; `ConsolidateAfterN` cfg:622 (default 5, :707); `DreamInterval` cfg:623 (default 10, 0 disables, :709); `maybeConsolidateMemory`:2805; `maybeTriggerDream`:2834; `triggerDream`:2855; `dumpMemoriesForDream`:2876; `handleDreamResult`:2899 → `dreamExecuteFn`/`memoryServices.ExecuteDream`:2909-2922 (no grading).
- `assign_payload.go`: `buildAssignPayload`:39; `buildCardContext`:107 (builds `RelevanceQuery` with `BeadType/BeadTags/BeadDescription/MaxTokens=2000`, **SymbolHints unset**); `trimAssignmentCardContext`:125.

### Other (verified)

- `pkg/ops/dream_prompt.go`: `buildDreamPrompt(opts DreamOpts)`:7 — asks the model to "emit a distilled summary" / "list any new insights as bullet points". (Note: the prompt text does not itself emit the `[CREATE]/[MERGE]/[DELETE]` grammar the parser expects — that grammar is enforced elsewhere; Phase 3 replaces this whole path.)
- `pkg/worker/memory_boundary.go`: `memoryExtractionModel = "haiku"`:18; `memoryExtractTimeout = 30s`:19; `maxMemorySessionBytes = 50_000`:20; `memoryExtractionPrompt`:23 (categories lesson/gotcha/decision/pattern, emits `[MEMORY] type= tags=: …`); `ParseMemoryMarker`:46; `ExtractMemoriesWithLLMInWorkdir`:70 (trailing-50KB, once); `appendMemoryMarker`:99 → `AppendLearningPending`; `cardCandidateFromMemoryMarker`:113 (maps to `pattern` or `decision` only).
- `pkg/modelartifacts/models.go`: `ModelSpec`:22; `KnownModels`:31 (4 BGE artifacts, SHAs above); `VerifyModel`:63; `PrefetchModels`:86.
- `pkg/langprofile/config.go`: `SemanticMemoryConfig`:42; `EnabledOrDefault`:53 (default true, testonly); `WithDefaults`:78.
- `cmd/oro-search-hook/main.go`: `run`:22; `HandleHook`:91 (PreToolUse, fail-open); `handleClaudeRead`:117; `handleCodexView`:140; `summarizeAndDeny`:166.
- `cmd/oro/cmd_current.go`: `beadRelevanceQuery`:170 (SymbolHints unset); call site `tx.Cards().Relevant`:126.

---

## Source techniques (donor attributions)

- **bloks** — SEE-ALSO relation graph with additive multi-signal edge weights (call/comention/namespace), accumulating on conflict (`ON CONFLICT DO UPDATE SET strength = strength + excluded.strength`); two-tier file-path-first → import-scoped callee resolution that defeats the ambiguous-bare-name explosion; `[[wikilink]]` body cross-references; lineage chains (backward `card_lineage`, forward `latest_in_chain`); a context-carrying `card_events(session_id, context)` log. Basis for all of Phase 1. (Debunked: bloks's no-decay usage ratio — oro's decay/suppression is richer; bloks's crude substring co-mention — we tighten to word-boundary.)
- **gbrain** — RRF fusion (k=60, Cormack-2009) of keyword + vector ranks; a 0.7-rrf / 0.3-cosine blend; bounded log-compressed boosts with a floor-ratio gate computed once; cross-encoder rerank as a fail-open, tail-preserving slot; and the propose→grade→calibrate loop with a confidence gate (single-judge ≥0.95, 3-judge ensemble for borderline, `unresolvable` never counts) + content/prompt-version idempotency + voice-gate. Basis for Phase 2 fusion and Phase 3. (Debunked: gbrain ships the grade prompt and evidence retriever as `-stub`s — we write our own; its boost constants are uncalibrated and its floor gate ships dormant — we ship flag-gated/default-off.)
- **claude-mem** — continuous PostToolUse capture streaming `{tool_name, tool_input, tool_response, cwd}` to a background queue; async cheap-model batch compression off the critical path; fail-open hook contract; privacy stripping at the edge; authoritative-SQLite + disposable-accelerator posture. Basis for Phase 4. (Debunked: its always-on per-session observer subprocess is fragile — we batch-compress on Stop/flush instead; its "semantic recall" is an outsourced Chroma MCP — not borrowed.)
- **CortexaDB** — fusion shape (similarity + importance + recency in one ranked score) as a reference that maps onto oro's existing `EffectiveScore`. (Debunked/skip: its WAL/segment storage spine is redundant atop SQLite; its HNSW is buggy and off-by-default — we use exact in-process cosine and scope at the candidate stage.)

---

## Design (oro-native, per phase)

> **Invariant:** `Relevant` returns a sensible `{Deck, Inlined}` with every new signal disabled. New signals are additive or floor-gated; the keyword `relevanceScore` (`store.go:497`) is never removed — it becomes the fusion floor. New tables are `CREATE TABLE IF NOT EXISTS`; new columns are nullable/`DEFAULT` added via a Go-side `pragma_table_info` guard (no migration helper exists yet — build a small `ensureColumn(db, table, col, ddl)` in `schema.go`).

### Phase 1 — SEE-ALSO relation graph (zero ML, default ON)

**Schema additions** (append to `schemaDDL`, `schema.go`):

```sql
CREATE TABLE IF NOT EXISTS card_relations (
  source_id TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
  target_id TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
  signal    TEXT NOT NULL,         -- 'call' | 'namespace' | 'comention' | 'wikilink' | 'lineage'
  strength  INTEGER NOT NULL,      -- call=3, comention=2, namespace=1, wikilink=2
  PRIMARY KEY (source_id, target_id, signal)
);
CREATE INDEX IF NOT EXISTS idx_card_relations_source ON card_relations(source_id);

CREATE TABLE IF NOT EXISTS card_symbols (
  card_id TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
  symbol  TEXT NOT NULL,           -- canonical "relpath.go:Symbol" or bare symbol
  PRIMARY KEY (card_id, symbol)
);
CREATE INDEX IF NOT EXISTS idx_card_symbols_symbol ON card_symbols(symbol);

-- card_events gains session context (Go-side ensureColumn ALTER)
ALTER TABLE card_events ADD COLUMN session_id TEXT;   -- nullable; existing rows NULL
```

Additive accumulation (per-signal idempotent; total related-strength = `SUM(strength)` across signals):

```sql
INSERT INTO card_relations(source_id, target_id, signal, strength) VALUES (?, ?, ?, ?)
ON CONFLICT(source_id, target_id, signal) DO UPDATE SET strength = excluded.strength;
```

**Code (new file `pkg/cards/relations.go`):**
- `type RelationSignal string` (`call`/`namespace`/`comention`/`wikilink`/`lineage`) with `func (RelationSignal) Strength() int` → call=3, comention=2, wikilink=2, namespace=1.
- `AddRelation(ctx, srcID, dstID string, sig RelationSignal) error` — rejects `src==dst`; upserts both directions for symmetric signals (namespace/comention), one direction for lineage.
- `SeeAlso(ctx, cardID string, limit int) ([]CardSummary, error)` — one-hop traversal ordered by `SUM(strength)` desc.
- `Lineage(ctx, id string) ([]Card, error)` — walk `superseded_by` backward, **visited-set cycle guard**.
- `LatestInChain(ctx, id string) (*Card, error)` — walk forward to the newest non-retired card.
- `ParseWikilinks(body string) []string` — extract `[[target]]` tokens → `wikilink` relations (strength 2) at card write time.
- Add `AddRelation/SeeAlso/Lineage/LatestInChain` to the `Store` interface (`store.go:20`).

**Code (`pkg/codestruct/relate.go`, new):**
- `ResolveCallee(e CallEdge, importsByFile map[string]map[string]string, symsByFile map[string][]Symbol) (ref string, ok bool)` — **Tier-1 file-path-first**: (1) if `e.Resolved && e.CalleeFile != ""` use `"<relpath>:<CalleeSymbol>"`; (2) else import-scope the caller's imports and match the name within that module only; (3) **give up (ok=false) rather than fall back to a global bare-name match** — the bloks explosion is the failure being prevented. `resolveSimple`/`resolvePkg` (`call_graph.go`) keep working for unambiguous calls; impact analysis is untouched.
- `MineCardRelations(...)` (in `pkg/cards`, run during the dream/consolidation cycle or `oro cards relate`): call edges between symbols keyed to cards → `call` (3); same package dir → `namespace` (1); word-boundary mention of another card's title short-name (≥4 chars, NOT substring) → `comention` (2).

**Feed `SymbolHints`** (revives the dead `symbolOverlap`, `store.go:567`): populate `RelevanceQuery.SymbolHints` at `assign_payload.go:111` `buildCardContext` (from the bead's touched files resolved via `ResolveCallee`) and `cmd_current.go:170` `beadRelevanceQuery`.

**Additive ranking** (`store.go:497` `relevanceScore`): keep the base sum exactly; add a SEE-ALSO term — `base + wSeeAlso * graphBonus(c, seeds, relationStrengths)`, where `graphBonus` is the log-compressed (`1 + 0.1·ln(1+strength)`) sum of edge strengths from the top-N keyword "seeds" into card `c`. `wSeeAlso=0` until the graph is populated → byte-identical to today. Two-pass `Relevant`: keyword-rank, then expand via `card_relations`. The now-live `symbolOverlap` keeps its 0.2 weight.

**Context-carrying events:** `RecordCardEvent` (`store.go:789`) gains `SessionID` on `CardEvent`; write a `view` event whenever a card is inlined into a worker prompt (`buildCardContext`) or surfaced by the hook. Add `Provenance(ctx, id)` returning the event timeline.

### Phase 2 — vector recall, fused not swapped (default OFF)

**Schema additions** (`ensureColumn` ALTERs on `cards`):

```sql
ALTER TABLE cards ADD COLUMN embedding       BLOB;   -- []float32 LE; NULL = not embedded
ALTER TABLE cards ADD COLUMN embedding_model TEXT;   -- e.g. 'bge-small-en-v1.5'; NULL when embedding NULL
```

**New package `pkg/embed`** (restores what `d9c7cd63` removed):
- `pkg/embed/onnx.go` — `ONNXEmbedder` implementing `dispatcher.Embedder` (`Embed(text) []float32`, `Dim() int`, `Name() string`), build-tagged for onnxruntime with a nocgo stub mirroring `codestruct/extractors_nocgo.go`. Loads `modelartifacts` `bge-small-en-v1.5` (dim 384). Reintroduce the 384 dim constant lost with the old embedder.
- `pkg/embed/factory.go` — `NewEmbedder(modelDir string) (dispatcher.Embedder, error)`, wired as the dispatcher's `embedderFactory` (`dispatcher.go:785`) so `warmupEmbedder`/`WaitForEmbedder` stop being inert when `SemanticModelDir` is set and `enabled=true`. Mirror for `rerankerFactory` (`bge-reranker-base`).
- Cards embedded on `Create` (`store.go:628`) over `Title + "\n" + BodySummary`; legacy `embedding IS NULL` cards backfilled lazily (`oro memory reindex` or first enabled session).

**Vector storage = a column on `cards`, brute-force cosine in Go** over non-retired embedded cards (corpus is hundreds; this is CortexaDB's own exact default and sidesteps HNSW entirely). No new store, no ANN.

**Finish the semantic route:** add a `SearchSemantic` handler in `pkg/codesearch` and a card-level vector candidate path; `RouteSemantic` (`router.go:54`) finally has a consumer.

**Fusion (`pkg/cards/fusion.go`, new):** RRF (k=60) of the keyword-ranked and vector-ranked lists → blend `0.7·rrf_norm + 0.3·cosine` → floor-gate (threshold = `topScore·floorRatio`, computed once; default `floorRatio=0` = OFF) → bounded log-compressed boosts. The fused score **multiplies** `EffectiveScore` so decay/suppression still gate eligibility. New config fields drive weights (below).

**Optional cross-encoder rerank** (gbrain): rerank top-N via `rerankerFactory`; **append the un-reranked tail unchanged** (preserve recall); on ANY error return the input untouched (fail-open). Gated by `SemanticMemoryConfig.Rerank` (default OFF per this spec).

### Phase 3 — Dream propose → grade → calibrate (default OFF; reconciled with `DecidePromotion`)

**Schema additions** (`ensureColumn` on `cards`):

```sql
ALTER TABLE cards ADD COLUMN grade_state      TEXT;  -- NULL/'active'(legacy) | 'proposed' | 'graded' | 'applied' | 'rejected'
ALTER TABLE cards ADD COLUMN grade_verdict    TEXT;  -- 'correct'|'incorrect'|'partial'|'unresolvable'
ALTER TABLE cards ADD COLUMN grade_confidence REAL;  -- judge self-reported [0,1]
ALTER TABLE cards ADD COLUMN proposal_hash    TEXT;  -- sha256(content|prompt_version), idempotent
CREATE INDEX IF NOT EXISTS idx_cards_grade_state ON cards(grade_state) WHERE retired_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_cards_proposal_hash ON cards(proposal_hash) WHERE proposal_hash IS NOT NULL;
```

**Flow change:**
- `handleDreamResult` (`dispatcher.go:2899`) stops calling `memoryServices.ExecuteDream` directly. Parsed actions are written as cards with `grade_state='proposed'`, `proposal_hash` set (re-dreaming an unchanged insight is a no-op via the unique index). **Proposed cards are excluded from recall** — add `grade_state IS NULL OR grade_state NOT IN ('proposed','rejected')` to the candidate SQL in `Relevant` (`store.go:381`).
- **Grade phase:** new `pkg/ops/grade_prompt.go` `buildGradePrompt(proposal, evidence)` (we own it — gbrain ships only a stub) + an oro-native evidence retriever: the proposed card's `card_events` history (was a similar prior card nacked/contradicted?), related cards via Phase 1 `SeeAlso`, Phase 2 vector neighbours, and the originating bead's QG/merge outcome. A grade worker emits `{verdict, confidence, reasoning}`; stored on the card.
- **Confidence gate (default OFF):** when on, single judge auto-applies at `confidence ≥ 0.95`; borderline `0.6 ≤ c < 0.95` → 3-judge ensemble requiring 3/3 unanimous AND min-conf ≥ 0.85; `unresolvable` never counts toward consensus. `correct`→`applied` (recall-eligible); `incorrect`→`rejected`+retire; `partial`/`unresolvable`→stay queued.
- **Calibration scorecard:** `Calibration(ctx)` aggregates resolved verdicts (accuracy by card-type × bead-type; Brier = historical bucket rate, no model); surfaces `active_bias_tags` injected as anti-bias context into the next propose prompt; cold-start <5 resolved → skip.
- **Voice-gate** (cheapest standalone win, do first): a Haiku judge grades a proposed card's prose against a rubric before queueing; ≤2 regenerate-with-feedback retries; parse failure → deterministic fallback to raw extracted text (never silently drop).

**Reconciliation with `DecidePromotion` / `bead_learnings_pending`:**
- `DecidePromotion` (`promotion.go:35`) is a **separate, narrower flow**: it decides whether a *bead learning* (captured at task end into `bead_learnings_pending`) becomes a card. It is heuristic-only — no LLM judge, no confidence, no calibration — and governs a different producer than dream.
- Phase 3's grade loop governs **dream-proposed** edits, which `DecidePromotion` never touched.
- **Decision:** Phase 3 does NOT delete `promotion.go`. Instead, `bead_learnings_pending` becomes a *second producer* into the same proposal queue: a `PromotionActionPromote` result inserts the card as `grade_state='proposed'` (via the proposal path) rather than directly via `PromoteLearning` (`store.go:683`), then runs the same grade gate; `Defer`/`Reject` behavior is unchanged. The conservative heuristic stays as the cheap front-door pre-filter; the LLM judge is the back-door, default-OFF. With the gate off, today's behavior is preserved exactly (`PromoteLearning` applies directly).

### Phase 4 — continuous hook-driven capture (default OFF)

- **PostToolUse buffer hook:** extend `cmd/oro-search-hook` (or a sibling `cmd/oro-capture-hook`) reusing the proven stdin/stdout + fail-open contract (`HandleHook`:91). On PostToolUse, append a **privacy-stripped** `{ts, tool_name, tool_input, tool_response, cwd}` record to a per-task append-only buffer (JSONL under the worktree `.oro/`, keyed by `ORO_WORKER_BEAD_ID`). Strip `<private>`/`<system-reminder>` **before** the write. Any error → exit 0, no capture (never block the tool). Tolerate a missing buffer dir (worktree GC hazard) by skipping, not erroring.
- **Async batch compression on Stop/flush:** refactor `ExtractMemoriesWithLLMInWorkdir` (`memory_boundary.go:70`) to accept a source (`io.Reader`/byte slice) so the 50KB-trailing path and the buffer path share it. On Stop (or a `flush_bytes` threshold mid-session), run the Haiku extractor over the *buffer* — fixing the core gap (mid-task discoveries that scrolled past 50KB are now captured). `maxMemorySessionBytes` (:20) becomes a per-batch cap, not the only window. Output continues into `bead_learnings_pending` (existing producer), which in Phase 3 flows into the proposal queue.
- **SessionStart injection is already keyword/recency — leave it** (claude-mem's own injection is keyword/recency too); it picks up Phase 2 fusion automatically when vectors are on.

---

## Interface / API / config

**Config** (`langprofile/config.go` — extend `SemanticMemoryConfig`:42 and add a sibling recall block). NOTE: `EnabledOrDefault`/`RerankOrDefault` currently default **true** but are `//oro:testonly`; introduce production accessors that default **false** for this rollout (don't silently flip semantic on for every project).

```yaml
memory:
  semantic:
    enabled: false        # *bool — Phase 2 master switch + embedder warm-up gate
    rerank:  false        # *bool — Phase 2 cross-encoder rerank
    ann_top_k: 50         # existing
    final_top_k: 10       # existing
    model_dir: ~/.oro/models
  fusion:                 # NEW (none of these fields exist today)
    rrf_k: 60
    cosine_weight: 0.7
    keyword_weight: 0.3
    floor_ratio: 0.0      # gbrain floor gate; 0 = OFF (ship dormant)
  see_also:               # NEW — Phase 1, cheap, default ON
    enabled: true
    max_hops: 1
    w_see_also: 0.0       # additive graph-bonus weight; 0.0 = exact back-compat until populated
  dream:                  # NEW — Phase 3 confidence gate
    grade_gate_enabled: false
    auto_apply_confidence: 0.95
    ensemble_min_confidence: 0.85
  capture:                # NEW — Phase 4
    continuous: false
    flush_bytes: 200000
```

**CLI (additive):** `oro cards relate` (re-mine relations/symbols); `oro cards relations <id>` / `oro cards lineage <id>`; `oro cards proposals [--pending]` + `oro cards grade <id>` + `oro cards review [--apply|--reject]` (Phase 3); `oro cards calibration`; `oro memory reindex` (Phase 2 re-embed); `oro models prefetch` (exists).

**`RelevanceQuery` (`cards.go:175`):** no new fields needed (SymbolHints already exists, finally fed); add an internal `SeededCardIDs []string` for the two-pass expansion.

**`Store` interface (`store.go:20`):** add `AddRelation, SeeAlso, Lineage, LatestInChain, Provenance` (P1); `Proposals, Grade, Calibration` (P3).

---

## Edge cases & failure modes

- **SEE-ALSO / lineage cycles** → visited-set guard in `Lineage`/`SeeAlso`; chains can loop after merges.
- **Self-relations** → reject `src==dst` in `AddRelation`.
- **Unresolved callees** → `ResolveCallee` returning `ok=false` must NOT create a `card_symbols` row for a garbage token; only file-path-qualified resolutions stored. Ambiguous bare names (`new`/`build`) give up rather than emit a global edge.
- **Embedding model mismatch** → `embedding_model` gates cosine; a card embedded under model A is treated as un-embedded for a model-B query and falls back to keyword.
- **Embedder missing/slow** → `WaitForEmbedder` returns `ErrSemanticDisabled`/timeout → semantic stays off, keyword serves (fail-open).
- **Cross-encoder failure** → fail-open to pre-rerank order; never drop the keyword tail.
- **Floor violation** → a cosine-only match below the keyword floor is boost-capped; cannot displace a genuine keyword hit. Floor gate ships default-off (gbrain's own caution).
- **Grade `unresolvable` / judges disagree** → never auto-applies; proposal stays queued (excluded from recall).
- **Proposal dedup** → identical `proposal_hash` collapses via the unique index; the dream loop won't flood the queue.
- **Capture hook errors / GC'd worktree** → always fail-open exit 0; tolerate a missing buffer dir.
- **Privacy** → strip secrets/`<private>` before the buffer write, not after.
- **CHECK constraint** → no phase adds a new card kind; if one is ever needed it requires a `cards.type` CHECK migration (`schema.go:8`).

---

## Backward-compat & blast radius

- **Keyword recall stays the floor in every phase.** All new signals are additive or floor-gated; deleting any phase's data degrades to today's behavior. Every existing `pkg/cards` ranking test stays green with defaults.
- **Default ON:** Phase 1 SEE-ALSO + SymbolHints feeding (zero ML). New tables `IF NOT EXISTS`; new columns nullable via `ensureColumn`.
- **Default OFF:** semantic (`enabled=false`), cross-encoder rerank, grade gate, continuous capture. With everything off, the only behavior change vs. today is Phase 1 SEE-ALSO (which is `w_see_also=0` until the graph is mined → identical until populated).
- **Direct-apply preserved** until `grade_gate_enabled=true`; `DecidePromotion`/`PromoteLearning` semantics unchanged unless the gate is on. The `grade_state` recall-exclusion is the only recall behavior change, and only when proposals exist.
- **No removals:** `promotion.go`, `bead_learnings_pending`, the 50KB end-of-task extract all remain; Phase 3/4 extend, not replace.
- **Blast radius:** `pkg/cards` (core), `pkg/codestruct` (additive resolver, impact untouched), `pkg/codesearch` (dead route activated), `pkg/dispatcher` (dream path + embedder factory registration), `pkg/worker` (capture), new `pkg/embed`. No protocol wire-format change; no beadstore change.

---

## TDD testing plan (red-first)

Existing suite to extend: `pkg/cards/store_test.go`, `store_internal_test.go`, `scoring`-related tests, `promotion_test.go`, `record_card_event_test.go`, `candidate_test.go`, `learnings_test.go`; `pkg/codestruct/call_graph_test.go`, `impact_test.go`; `pkg/codesearch/router_test.go`, `classifier_test.go`; `cmd/oro-search-hook` tests. (Build/test notes from project memory: `make build` not `go build`; `go test ./pkg/dispatcher/... -count=1 -timeout 180s`; `go test ./pkg/worker/... -v -count=1`; ONNX tests build-tagged so the nocgo stub runs in CI.)

**Phase 1** — `pkg/cards/relations_test.go`, `pkg/codestruct/relate_test.go`, `pkg/cards/schema_test.go`:
- `TestAddRelation_AccumulatesStrength` — `AddRelation(A,B,call)` then `AddRelation(A,B,comention)` → total related-strength 5 (3+2), per-signal rows not overwritten.
- `TestAddRelation_RejectsSelf` — `AddRelation(A,A,call)` → error/no-op, no row.
- `TestSeeAlso_OrdersByStrength` — A→B(3), A→C(5) → `SeeAlso(A)` = [C, B].
- `TestSeeAlso_CycleSafe` — A↔B → traversal terminates, no dup.
- `TestResolveCallee_FilePathFirst` — fixture with same-file def + import-qualified + bare name → file-path candidate wins; assert the canonical ref.
- `TestResolveCallee_AmbiguousGivesUp` — a `new`/`build` collision → `ok=false`, no `card_symbols` row (RED against any global-bare-name fallback).
- `TestParseWikilinks_Extracts` — body `"see [[Foo]] and [[Bar/Baz]]"` → `["Foo","Bar/Baz"]`.
- `TestComention_WordBoundaryNotSubstring` — "config" must NOT match "configure" (RED against bloks's substring bug).
- `TestLineage_CycleDetected` / `TestLatestInChain_ReturnsTip`.
- `TestRelevant_SymbolHintsNowContributes` — card with matching `card_symbols` + query `SymbolHints=["pkg/x:Foo"]` ranks above an identical card without the symbol (replaces the current "noop" expectation).
- `TestRelevant_SeeAlsoAdditiveButFloorPreserved` — a card one SEE-ALSO hop from a strong keyword hit gets a positive boost but never outranks the direct keyword hit.
- `TestRelevant_WSeeAlsoZeroIsLegacyIdentical` — golden test: `w_see_also=0` ⇒ byte-identical ranking to today.
- `TestRecordCardEvent_WritesSessionID` + `TestSchema_AddsSessionIDAndRelationTables`.

**Phase 2** — `pkg/embed/onnx_test.go`, `pkg/codesearch/router_test.go`, `pkg/cards/fusion_test.go`:
- `TestONNXEmbedder_DimIs384` and `TestONNXEmbedder_Deterministic` (same text → same vector); nocgo build runs a stub.
- `TestSearchSemantic_NoLongerDeadEnds` — with a stub embedder registered, the semantic path returns ranked results (RED: today there is no handler).
- `TestRRFFusion_K60` — two hand-built rank lists → fused order matches hand-computed `Σ 1/(60+rank)`.
- `TestBlend_0703` — cosine 0.9 / keyword-rrf 0.2 with 0.7/0.3 → assert blended value.
- `TestFloorGate_CosineCannotVaultBelowFloor` — a cosine-0.99 card below the keyword floor does not outrank a keyword hit.
- `TestRerank_FailOpenPreservesTail` — 35 candidates, reranker reorders top-30, tail 31-35 unchanged; reranker error → input untouched.
- `TestRecall_EmbeddingModelMismatchFallsBack` — card embedded "modelA", query model "modelB" → keyword fallback, no cosine compare.
- `TestFusion_NilEmbedderEqualsPhase1` — fail-open: no embedder ⇒ pure-keyword result equals the Phase-1 result.

**Phase 3** — `pkg/dispatcher/dream_test.go`, `pkg/ops/grade_prompt_test.go`, `pkg/cards/promotion_test.go` (extend), `schema_test.go`:
- `TestHandleDreamResult_WritesProposalNotApplied` (gate ON) — dream actions land `grade_state='proposed'`, excluded from recall (RED: today `ExecuteDream` applies directly).
- `TestProposalsExcludedFromRecall` — a `proposed`/`rejected` card never appears in `Relevant`.
- `TestProposalHash_Dedups` — two identical proposals collapse to one row (unique index).
- `TestGradeGate_AcceptsAtThreshold` — single judge 0.96 → accept; 0.94 → escalate to ensemble.
- `TestGradeGate_EnsembleUnanimityRequired` — 2/3 does NOT apply; 3/3@0.9 does; one `unresolvable` drops consensus.
- `TestGradeGate_UnresolvableNeverAccepts`.
- `TestPromotedLearning_EntersProposalQueue` — `DecidePromotion`→promote now inserts `grade_state='proposed'` (not direct card); defer/reject unchanged.
- `TestGateOff_PreservesDirectApply` — default OFF ⇒ `handleDreamResult`/`PromoteLearning` apply directly (today's behavior unchanged).
- `TestCalibration_ReportsRates` — seeded verdicts → scorecard counts.
- `TestVoiceGate_RejectsOffVoiceProse` + `TestVoiceGate_ParseFailureFallsBackToRaw`.

**Phase 4** — `cmd/oro-capture-hook` (or extend search-hook) tests, `pkg/worker/memory_boundary_test.go`:
- `TestCaptureHook_FailOpenOnError` — malformed payload → exit 0, no write (mirror existing search-hook fail-open tests).
- `TestCaptureHook_PrivacyStrip` — `<private>`/secret token never reaches the buffer file.
- `TestCaptureBuffer_BoundedAndMissingDirTolerant` — over-capacity drops oldest; missing `.oro/` dir → skip not error.
- `TestExtractLearnings_AcceptsReaderSource` — refactored extractor works over both a 50KB slice and a buffer.
- `TestFlush_CapturesMidSessionDiscovery` — an insight present only in the mid-session buffer (scrolled out of trailing 50KB) IS captured (the core regression the phase fixes; RED against today).

**Recall hit-rate replay harness (cross-phase, the acceptance metric):** new `pkg/cards/replay_test.go` + fixtures `testdata/recall_replay/*.json`, each `{cards[], query, expected_top_k_ids[]}` (seed from real `card_events` where a surfaced card was later acked). `TestRecallReplay_HitRate` computes recall@k + MRR and asserts a per-phase floor: capture P0 baseline first (RED), then assert P1 ≥ baseline, P2 ≥ P1 on the paraphrase subset (queries whose wording diverges from the card). This is the objective regression gate for "did recall actually improve," and it directly feeds task decomposition.

---

## Effort & sequencing (ROI order)

| Order | Item | Depends on | ML? | Default | Effort |
| --- | --- | --- | --- | --- | --- |
| 1 | Phase 1 SEE-ALSO graph + SymbolHints feed + `ResolveCallee` + wikilinks + `session_id`/`view` events | — | no | ON | M |
| 2 | Recall replay harness (baseline + P1 gate) | P1 | no | — | S |
| 3 | Phase 3 **voice-gate only** (cheap quality win, no judge, no embedder) | P1 | light | OFF | S |
| 4 | Phase 2 `pkg/embed` ONNX embedder + register `embedderFactory` | modelartifacts pin | yes | OFF | L |
| 5 | Phase 2 fusion (RRF + blend + floor gate) + finish `SearchSemantic` | #4 | yes | OFF | M |
| 6 | Phase 2 cross-encoder rerank (fail-open, tail-preserve) | #5 | yes | OFF | M |
| 7 | Phase 3 propose→grade→calibrate + reconcile `promotion.go` | P1, #5 (evidence retriever) | yes | OFF | L |
| 8 | Phase 4 PostToolUse capture + async flush | P3 producer | light | OFF | M |

Rationale matches the archived spec's ROI order: Phase 1 first (zero ML, revives a dead signal, default ON); replay harness immediately after so every later phase has a regression gate; the voice-gate cherry-picked early (cheap, no embedder); Phase 2 is the heavy lift (rebuilding the retired embedder); Phase 3's full grade loop needs the Phase 2 evidence retriever; Phase 4 last (a producer into the now-existing proposal pipeline; it also enriches `card_symbols`/cards, strengthening Phases 1-3).

---

## Out of scope / open questions

- **ONNX runtime dependency** — onnxruntime-go (cgo, matches the tree-sitter build-tag pattern) vs. a pure-Go embedder. The cgo/runtime weight is what made `pkg/memory` heavy enough to retire (`d9c7cd63`); decide before Phase 4-order step #4.
- **Re-embedding on model bump** — lazy (on next recall) vs. eager (`oro memory reindex`). Default lazy + manual reindex verb.
- **Vector storage** — `embedding BLOB` + in-process exact cosine (chosen, simplest at card scale) vs. a `sqlite-vec` extension if scan cost bites.
- **Judge model for grading** — Haiku-class (cheap) vs. the worker model; start Haiku, measure on the calibration scorecard.
- **3-judge ensemble cost** — borderline escalation triples judge calls; needs a budget cap.
- **`card_symbols` keying for legacy cards** with no `files_modified` provenance — key by tag tokens, or a one-time backfill that re-reads `emerged_from` source.
- **Two-pass `Relevant` cost** — keyword-rank-then-expand doubles the in-Go scan; negligible at hundreds of cards, revisit past ~5k.
- **Calibration window** — fixed-N vs. time-window for the scorecard.
- **Cross-language `ResolveCallee`** — Tier-1 file-path-first is defined for Go; per-language resolvers (python/js/ts extractors exist) are a follow-up.
- **Should `partial`-graded cards ever surface** with a confidence caveat, or stay fully hidden? Default hidden; revisit if recall suffers.
