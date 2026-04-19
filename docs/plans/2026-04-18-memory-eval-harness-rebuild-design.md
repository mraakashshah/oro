# Memory Eval Harness Rebuild — Real BGE vs TF-IDF Validation

**Date:** 2026-04-18
**Status:** design v2 / fixes from adversarial review R1
**Related:** `docs/plans/2026-04-16-semantic-memory-overhaul-design.md` (v3.2), bead `oro-pvw8` (reopened), master epic `oro-0hjm`

## Changelog

- **v2 (this)** — fixes from R1 adversarial review:
  - Replaced precision@k with **MRR + Hit@10** as primary metrics (1-relevant-per-query made precision@10 mathematically capped at 0.10)
  - Added explicit `vecIndex.Upsert` step alongside `Store.Insert` during seed (BGE configs would otherwise query empty vec0 table)
  - Replaced "zero-overlap paraphrase" with "≤2 shared content words" (zero-overlap made TFIDF baseline deterministically 0 → re-introduced 0/0 gate facade)
  - Redesigned CheckGate: fails when base_MRR == 0 (explicit guard against facade regression)
  - Added sqlite-vec loading strategy: purpose-built `dbutil.OpenEvalDB` using mattn/go-sqlite3 + LoadExtension; does **not** touch main codebase's modernc driver
  - Added `MigrateSemanticMemoryChunks` + `MigrateSemanticMemoryDense` + `MigrateSemanticMemoryBackfillState` to in-memory setup
  - Documented ORT/tokenizers dylib prereq (`make install` required before running eval)
  - Added 4 missing tests + runtime budget cap
- v1 — initial design

## Problem

The semantic-memory overhaul (7 phases, 54 tasks) landed end-to-end but was never validated. `ad_hoc/memory_eval/compare.go` reports a "gate PASSED" with `0.0000` precision for all three configs. This is architecturally guaranteed, not a numerical coincidence.

### Root causes

1. **`compare_impl.go:embedderForCfg` uses `TFIDFEmbedder` for BOTH `tfidf` and `dispatcher-warm`.** The BGE path is never exercised. Code comment admits "eval uses TF-IDF proxy".
2. **`compare_impl.go:RunConfigWithEmbedder` seeds the store from `builtinFixtures()` (12 hardcoded notes about TDD/WAL).** These are unrelated to the corpus's candidate memory IDs. The `fixtureIDMap[e.CandidateMemoryID]` lookup silently drops every corpus entry → every query has `relevantSet = nil` → precision=0 always.
3. **Corpus queries are drawn from memory contents then cross-joined with random memories.** This tests lexical overlap (TF-IDF's strength), not semantic retrieval (BGE's point). Even if (1) and (2) were fixed, the eval would favor the baseline.
4. **All 100 current corpus entries have `relevant: null`** — no ground truth.
5. **`pkg/memory/models.go` shipped with `SHA256: "TODO_fill_after_download"` for all 3 BGE artifacts.** Fixed in commit `8a428e5b`. No model could be downloaded in any environment until that fix.

## Design

A corpus that actually tests semantic retrieval + an eval harness that actually runs BGE.

### Corpus shape

- **50 anchor memories** drawn from `~/.oro/projects/oro/state.db` (699 memories). Diverse by `type` field: `gotcha`, `lesson`, `pattern`, `decision`, `self_report`.
- **3 paraphrased queries per anchor** via `claude -p --model claude-haiku-4-5-20251001`. Prompt: rephrase as natural questions using different phrasing; **validator enforces ≤2 shared content words with source** (case-folded, stop-words excluded). Returns JSON array.
- **4 distractors per query**, drawn from anchors of different `type`. Distractors are labeled `false` **relative to this specific query** (not necessarily non-relevant in the abstract — the metric only consumes anchor labels anyway). Total: 5 candidates × 150 queries = **750 pairs**.
- **Ground truth by construction.** Anchor→query pair is `true`; distractor→query is `false`. No human step.

Why ≤2 overlap instead of zero: with zero overlap, TFIDF baseline is mathematically forced to MRR=0 (no FTS5 match, no cosine similarity). That resurrects the 0/0 gate facade this rebuild is meant to eliminate. Allowing ≤2 shared content words gives the baseline a realistic chance to rank the anchor in top-k while still testing semantic retrieval (BGE should find the anchor through conceptual similarity, not just 2-word overlap).

### Corpus artifacts

- `ad_hoc/memory_eval/corpus.jsonl` — 750 `CorpusEntry` lines. Existing schema. Header `# APPROVED`.
- **NEW** `ad_hoc/memory_eval/corpus_anchors.jsonl` — sidecar. One `{id, type, content}` line per unique memory referenced in corpus (anchors + distractor pool, ~50 entries since distractors come from the same pool).

### Eval harness rewrite

`ad_hoc/memory_eval/compare_impl.go:RunConfigWithEmbedder`:

- **Load anchors from sidecar**, not `builtinFixtures()`.
- **Seed store with sidecar memories**, track corpus-id → store-id map.
- **After each `Store.Insert`, explicitly call `vecIndex.Upsert(storeID, embedder.Embed(content), project)`** for BGE configs. `Store.Insert` does NOT auto-populate the vec0 table (confirmed by reading pkg/memory/memory.go:448-531 — `insertDirect`/`insertWithChunks` write the `memories` table + optional `memory_chunks` table, never `vecIndex`). The pattern is extracted into a shared helper `seedStoreWithVectors(store, idx, embedder, anchors)` — same pattern as `pkg/memory/hybrid_integration_test.go:186-193`.
- **Run all migrations** when opening the in-memory DB: `SchemaDDL` + `MigrateSemanticMemorySearchEvents` + `MigrateSemanticMemoryChunks` + `MigrateSemanticMemoryDense` + `MigrateSemanticMemoryBackfillState`. Without these, `insertWithChunks` fails with "no such table: memory_chunks" for any anchor whose content exceeds 512 tokens.

`embedderForCfg` replaced by a per-config setup function `setupConfig(cfg) → (store, embedder, cleanup)` that wires vector index + reranker:

| Config | Embedder | Vector Index | Reranker |
|--------|----------|--------------|----------|
| `tfidf` | `memory.NewEmbedder()` | `nil` (linear scan on `memories.embedding` blob) | `nil` |
| `dispatcher-warm` | `memory.NewBGEEmbedder(~/.oro/models/bge-small-en-v1.5)` | `memory.NewSQLiteVecIndex(db, libPath)` via `dbutil.OpenEvalDB` (see below) | `memory.NewBGEReranker(~/.oro/models/bge-reranker-base)` + `SemanticConfig{rerankEnabled:true, rerankTopK:20, rerankFinalK:10}` |
| `solo-cli-cold` | `memory.NewBGEEmbedder(...)` | `memory.NewSQLiteVecIndex(db, libPath)` | `nil` |

Model dir resolution: check `ORO_MODEL_DIR` env override, else default `~/.oro/models`.

### sqlite-vec loading

Current state (R1 finding): `pkg/dbutil/openDB.go` uses `modernc.org/sqlite` (pure-Go, does not support `LoadExtension`). The driver swap to `mattn/go-sqlite3` that would have enabled sqlite-vec (beads oro-c4rm / oro-k4cp) was closed without implementation. Empirically, `go test ./pkg/memory -run TestSQLiteVecIndexUpsertSearchDelete` SKIPs today with "no such function: vec_version".

Strategy: **purpose-built helper in the eval package**, does not touch main codebase.

- **NEW** `ad_hoc/memory_eval/openevaldb.go` exposes `OpenEvalDB(dbPath string) (*sql.DB, error)`. Uses `mattn/go-sqlite3` directly with a registered driver (`"sqlite3_with_vec"`) that installs a `ConnectHook` calling `conn.LoadExtension(libPath, "sqlite3_vec_init")`. `libPath` resolves via `pkg/dbutil.ResolveSqliteVecLibPath()`.
- The eval package imports `mattn/go-sqlite3` under the `//go:build cgo && darwin` tag (same tag as BGE), so it does not affect the main binary's build.
- **Test**: `TestOpenEvalDBLoadsSqliteVec` — asserts `SELECT vec_version()` returns non-empty on an opened DB.
- If `vec_version()` returns error at harness startup, `dispatcher-warm` and `solo-cli-cold` configs fail with a clear message; `tfidf` still runs.

### Runtime prerequisites

- **`make install`** must have been run to place `libonnxruntime.dylib` + `libtokenizers.dylib` under `~/.oro/lib/` and wire DYLD paths. `go run ad_hoc/memory_eval/compare.go` without prior `make install` will fail at ORT session init with "image not found".
- The eval harness logs a clear error pointing to `make install` when dylibs are missing.
- Runtime budget: with `rerankTopK=20` (top 20 fused results fed to reranker), `dispatcher-warm` on 150 queries is ~150 × 20 × 0.02s ≈ 60s total (M-series CPU). Acceptable for manual run, too slow for CI. Add `--fast` flag to `compare.go` that uses a 10-query random sample for CI smoke tests.

### Corpus generator

New binary under `ad_hoc/memory_eval/cmd/rebuild_corpus/main.go` (or extension of `extract.go`):

1. Open `~/.oro/projects/oro/state.db` (path via flag, default to this).
2. Select 50 anchors: `SELECT id, type, content FROM memories WHERE length(content) BETWEEN 50 AND 400 GROUP BY type ORDER BY RANDOM() LIMIT 50` — varied length, varied types, deterministic seed via flag.
3. For each anchor: shell `claude -p --model claude-haiku-4-5-20251001 --output-format json` with a structured prompt. Parse the JSON response; validate lexical overlap constraint; retry once if violated, fall back to templated paraphrase if retry fails.
4. For each query: pick 4 distractors from anchors of different `type`.
5. Write `corpus.jsonl` + `corpus_anchors.jsonl`.
6. Header: `# APPROVED` + `# generated: <ISO ts>` + `# source_db: <path>`.

### Metrics (revised)

With 1 relevant memory per query, `precision@10` is capped at 0.10 — the metric hides rank information. Replaced with:

- **MRR (Mean Reciprocal Rank)** — primary. For each query, `1/rank` of the anchor in top-k, averaged. MRR=1.0 means anchor is always #1; MRR=0 means anchor never appears in top-k.
- **Hit@10 (Hit Rate)** — secondary. Fraction of queries where the anchor appears in top-10.
- **Hit@1** — sanity. Fraction of queries where the anchor is the #1 result.

### Gate (revised)

Replaces `warm >= 1.30 * base AND cold >= 1.20 * base`:

```go
func CheckGate(baseMRR, warmMRR, coldMRR float64) GateResult {
    if baseMRR == 0 {
        return GateFail{Reason: "baseline MRR is 0 — search is broken, cannot compute ratio"}
    }
    if warmMRR < 1.30*baseMRR {
        return GateFail{Reason: fmt.Sprintf("warm MRR %.4f < 1.30×%.4f baseline", warmMRR, baseMRR)}
    }
    if coldMRR < 1.20*baseMRR {
        return GateFail{Reason: fmt.Sprintf("cold MRR %.4f < 1.20×%.4f baseline", coldMRR, baseMRR)}
    }
    return GatePass{}
}
```

The explicit `baseMRR == 0` guard is the key fix from R1: without it, a zero baseline makes every ratio trivially pass, reproducing the facade.

### Data flow

```
state.db (699 memories)
   │
   ▼
rebuild_corpus: pick 50 anchors, paraphrase (≤2-word overlap), build pairs
   │
   ├──► corpus_anchors.jsonl (50 lines: id, type, content)
   └──► corpus.jsonl (750 lines: query, candidate_memory_id, relevant, source)
                │
                ▼
  compare.go (3 configs) — requires `make install` for dylibs
   │
   ├──► tfidf: TFIDFEmbedder + linear scan + no rerank
   ├──► dispatcher-warm: BGEEmbedder + SQLiteVecIndex (eval DB via mattn+LoadExtension)
   │                     + BGEReranker (topK=20, finalK=10)
   └──► solo-cli-cold: BGEEmbedder + SQLiteVecIndex + no rerank
                │
                ▼
         MRR, Hit@10, Hit@1 per config
                │
                ▼
         gate: fails if base_MRR == 0 OR warm < 1.30×base OR cold < 1.20×base
```

### Dependencies

- `~/.oro/models/bge-small-en-v1.5/model.onnx` (127MB, SHA-verified) — present
- `~/.oro/models/bge-reranker-base/model.onnx` (~1.1GB, SHA-verified) — present
- `~/.oro/models/bge-tokenizer/tokenizer.json` — present
- `~/.oro/lib/sqlite-vec.dylib` — present
- `claude` CLI 2.1.114+ on PATH

### Out of scope

- Rewriting the whole `ExtractCorpus` API (existing `extract.go` left in place, new `rebuild_corpus` is additive).
- Reranker variants beyond BGE.
- Automatic distractor verification (distractors assumed `false` by type-disjoint construction; spot-check via smoke test only).
- Dispatcher-driven eval runs (harness stands alone, uses `NewBGEEmbedder` directly — dispatcher is just a deployment wrapper).

## Acceptance Criteria

1. `go test ./ad_hoc/memory_eval/... -count=1 -tags "cgo darwin"` passes, including:
   - `TestSetupConfigWarmUsesBGE` — asserts `setupConfig("dispatcher-warm")` returns a `*BGEEmbedder`, not a `*TFIDFEmbedder`
   - `TestSetupConfigColdUsesBGENoRerank` — BGE embedder + nil reranker
   - `TestSeedFromAnchorSidecar` — loading anchors from sidecar seeds store with correct IDs and calls `vecIndex.Upsert` for BGE configs
   - `TestOpenEvalDBLoadsSqliteVec` — `SELECT vec_version()` returns non-empty
   - `TestMRRSingleRelevant` — anchor at rank 1 → MRR=1.0; at rank 5 → MRR=0.2; absent → MRR=0
   - `TestCheckGateZeroBaselineFails` — `CheckGate(0, 1, 1)` returns `GateFail` (prevents facade regression)
   - `TestParaphraseValidator` — golden inputs: 0 overlap PASS, 2 overlap PASS, 3 overlap FAIL (case-folded, stop-words excluded)
   - `TestRebuildCorpusDeterministic` — two runs with identical `--seed` produce byte-identical `corpus.jsonl` + `corpus_anchors.jsonl`
   - `TestGroundTruthIntegrity` — every `candidate_memory_id` in `corpus.jsonl` has a matching row in `corpus_anchors.jsonl`; no null `relevant` values; every anchor appears as `relevant:true` for ≥1 query
2. `go run ./ad_hoc/memory_eval/cmd/rebuild_corpus --db ~/.oro/projects/oro/state.db --out ad_hoc/memory_eval/corpus.jsonl --anchors ad_hoc/memory_eval/corpus_anchors.jsonl --seed 42` produces:
   - 50-line `corpus_anchors.jsonl`
   - 750-line `corpus.jsonl` with `# APPROVED` header and `relevant: true|false` on every entry (no nulls)
   - Every anchor content ≤512 tokens (token check enforced by generator)
3. `go run ./ad_hoc/memory_eval/cmd/compare --corpus ad_hoc/memory_eval/corpus.jsonl --anchors ad_hoc/memory_eval/corpus_anchors.jsonl` (after `make install`) runs end-to-end:
   - Loads all 3 configs without ORT/tokenizer/sqlite-vec errors
   - Prints per-config table with MRR, Hit@10, Hit@1
   - `tfidf` baseline MRR is **> 0** (proves search works; guaranteed by ≤2-word overlap rule)
4. `go run ./ad_hoc/memory_eval/cmd/compare --fast` completes in <30s on M-series CPU (10-query smoke set).
5. Exit code reflects the real gate outcome per the revised `CheckGate`. Report committed to `ad_hoc/memory_eval/eval_report.txt` with all three metrics.

### Anti-criteria (explicit non-goals)

- **Not** required: `dispatcher-warm` MRR must beat `tfidf` by 1.30×. If BGE loses on this domain, that's a finding we report, not a failure we paper over.
- **Not** required: distractor labels validated as truly non-relevant. The metric only consumes anchor labels; distractor labels are informational only.
- **Not** required: corpus entries exactly 100. The spec's "warning below 100" threshold is satisfied by 750.
- **Not** required: the driver swap (modernc → mattn) in the main codebase. Eval uses a purpose-built helper that bypasses the issue; main binary unchanged.

## Premortem

| Risk | Severity | Mitigation |
|------|----------|------------|
| Haiku overlap constraint violated | medium | Validator enforces ≤2 shared content words (case-folded, stop-word filtered); re-prompt once with stricter phrasing; fall back to templated `"how do I <verb-phrase>"` rewrite from anchor |
| BGE genuinely underperforms TF-IDF on keyword-heavy technical notes | low (this is a finding, not failure) | Report MRR+Hit@10 table; gate fails honestly; follow-up bead to revisit thresholds or embedding strategy |
| Distractor accidentally relevant to query | low (doesn't affect metric) | Metric only consumes anchor labels; distractors exist to pad candidate count, not to gate precision |
| ORT runtime library missing | high but observable | `NewBGEEmbedder` returns clear error at startup pointing to `make install`; investigate and install before proceeding |
| sqlite-vec extension fails to load | medium | Purpose-built `OpenEvalDB` uses mattn+LoadExtension; `TestOpenEvalDBLoadsSqliteVec` asserts at test time; runtime failure returns clear error with lib path |
| Corpus anchor IDs collide with in-memory SQLite autoincrement | medium | Use `anchorIDMap[corpus_id] = store_id` indirection (pattern from current `fixtureIDMap`) |
| Haiku API unavailable / rate-limited | low | Serialize calls; retry with exponential backoff; 50 calls fit easily inside normal quotas |
| Anchor content exceeds 512 tokens → `insertWithChunks` path | medium | Generator filters by token count (not char length); migrations `MemoryChunks`+`Dense` applied to in-memory DB so insertWithChunks also works as a fallback |
| Rerank path too slow for CI | medium | `--fast` flag samples 10 queries; CI runs fast mode; full eval is manual-run only |
| 0/0 gate facade regression | **eliminated** | Explicit `baseMRR == 0 → GateFail` guard + `TestCheckGateZeroBaselineFails` |

## Implementation Outline

Roughly five beads (to be formalized by `beadcraft`). Order matters — each depends on the previous:

1. **`OpenEvalDB` + sqlite-vec loading** — `ad_hoc/memory_eval/openevaldb.go` (+ `openevaldb_test.go`). TDD: `TestOpenEvalDBLoadsSqliteVec`. Smallest, unblocks everything.
2. **MRR + Hit@k metrics + CheckGate v2** — replace precision@k in `compare_impl.go`. TDD: `TestMRRSingleRelevant`, `TestCheckGateZeroBaselineFails`.
3. **Paraphrase validator + rebuild_corpus CLI** — `ad_hoc/memory_eval/paraphrase.go` + `cmd/rebuild_corpus/main.go`. TDD: `TestParaphraseValidator`, `TestRebuildCorpusDeterministic`, `TestGroundTruthIntegrity`.
4. **Harness wiring for BGE configs** — rewrite `setupConfig` + `RunConfigWithEmbedder` to seed from sidecar with `vecIndex.Upsert` and use BGE/reranker. TDD: `TestSetupConfigWarmUsesBGE`, `TestSetupConfigColdUsesBGENoRerank`, `TestSeedFromAnchorSidecar`.
5. **Run + commit** — execute end-to-end, capture real numbers, commit corpus + sidecar + `eval_report.txt`. Close `oro-pvw8` properly (or supersede it). This bead gates the master epic `oro-0hjm`.

## Success Definition

Master epic `oro-0hjm` closes with:
- A corpus the whole team can see was built to actually test semantic retrieval
- A compare report with real BGE numbers
- Honest gate outcome (pass or fail), not a `0/0` facade

If the gate fails, file a follow-up bead with the observed numbers and the next hypothesis (chunking knobs, rerank top-k tuning, model swap). That's success too — it's the first time we'd actually know what the overhaul delivers.
