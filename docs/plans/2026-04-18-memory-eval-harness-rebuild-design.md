# Memory Eval Harness Rebuild — Real BGE vs TF-IDF Validation

**Date:** 2026-04-18
**Status:** design v5 / fixes from adversarial review R4
**Related:** `docs/plans/2026-04-16-semantic-memory-overhaul-design.md` (v3.2), bead `oro-pvw8` (reopened), master epic `oro-0hjm`

## Changelog

- **v5 (this)** — fixes from R4 adversarial review:
  - **Project name `oro-eval` → `oro_eval`** — hyphen caused SQL syntax error in `CREATE VIRTUAL TABLE vec_memories_oro-eval USING vec0(...)` because `pkg/memory/vec_index.go:60-63` does not quote identifiers. The regex at `vec_index.go:16` accepts hyphens but the CREATE statement doesn't tolerate them. Underscore is always safe.
  - **Determinism selector form unified** — `(id * 2654435761 + seed) % (1<<31)` used everywhere (paraphrase-caching hedge line now matches generator step 2).
  - **`inputs_sha` concatenation spec tightened** — order is exactly `corpus.jsonl → corpus_anchors.jsonl → paraphrase_cache.jsonl`; all files written LF-only by the generator.
  - **Constant locations named** — `ParaphrasePromptVersion` in `paraphrase_cache.go`, `MaxSharedContentWords` in `paraphrase_validator.go`, `WarmMRRRatio`/`ColdMRRRatio` in `metrics.go`. Residual-risk recovery paths are now findable.
  - **`make install` prereq** — labeled as explicit human precondition in "Runtime prerequisites" with a check command. Not a bead dependency (beadcraft only models inter-bead deps); bead 3's Read: field includes the check and clear error.
- **v4** — fixes from R3 adversarial review (must-fix tier):
  - **`seedStoreWithVectors` project contract pinned** — signature now `seedStoreWithVectors(store, idx, emb, anchors, project string)`. Caller MUST call `store.SetProject(project)` before invoking. `idx.Upsert` uses the same `project`. Pattern copied from `pkg/memory/hybrid_integration_test.go:160-193`.
  - **Paraphrase cache bootstrap procedure documented** — dedicated subsection below. One-time: run `rebuild_corpus --seed 42` without `--no-api`. Cache regeneration triggers when anchor set changes (state.db content drift) or `ParaphrasePromptVersion` is bumped.
  - **`eval_report.yaml` inputs_sha** — replaces single `corpus.sha`. Hashes concatenation of `corpus.jsonl` + `corpus_anchors.jsonl` + `paraphrase_cache.jsonl`. Reproducibility now captures all three input files.
  - **`--fast` sample size floor** — formula changed to `size = max(5, min(10, floor(30000/(20*X))))`. Bead 3 aborts if `floor(30000/(20*X)) < 5` (hardware too slow for meaningful CI smoke).
  - **R3 should-fix items accepted as documented residual risks** (per R3 recommendation): threshold empirical validation deferred to first-run data; 1.30×/1.20× gate thresholds annotated as inherited-from-precision-spec, re-justification follow-up bead; lemmatizer scope = plural-s only; fallback rate check fires at end of run.
- **v3** — fixes from R2 adversarial review:
  - **Build tag isolation** — explicit file-split plan for `ad_hoc/memory_eval` so `GOOS=linux CGO_ENABLED=0 go vet ./...` still passes. BGE/mattn code lives under `//go:build cgo && darwin`; metric math + paraphrase validator are untagged.
  - **Paraphrase cache fixture** — committed `ad_hoc/memory_eval/paraphrase_cache.jsonl` keyed by `(anchor_content_sha, prompt_version)`. `rebuild_corpus` reads cache first, only calls Haiku on miss. `TestRebuildCorpusDeterministic` uses a fully-populated cache → no Haiku at test time.
  - **Overlap threshold widened to ≤3 shared content words** — empirically, natural paraphrases of short technical notes ("worker respawns on crash" ↔ "how do workers recover from crashes") share ~2-3 content words. ≤3 keeps TFIDF baseline non-trivial while still forcing semantic retrieval for the rest.
  - **Fixed `NewSQLiteVecIndex(db)` signature** — 1 arg, not 2. `libPath` is loaded inside `OpenEvalDB` via `ConnectHook` before `vec_version()` runs.
  - **`eval_report.yaml` format** (renamed from .txt) — structured: `corpus_seed`, `corpus_sha`, `timestamp`, `hardware`, `model_sha` (from `KnownModels`), per-config `{MRR, Hit@10, Hit@1}`, `fallback_paraphrase_rate`, `gate_result`. Machine-parseable.
  - **MRR convention pinned** — denominator = total query count; absent-anchor contributes 0.
  - **Rerank latency measured before budget** — new prerequisite bead runs `BenchmarkBGERerankPair` to calibrate `--fast` sample size empirically.
  - **`testing.Short()` bail-out** — all BGE-loading tests `t.Skip` under `-short`. `make test-short` target added.
  - **Timing + exit-code ACs** — concrete tests: `TestCompareFastCompletesUnder30s`, `TestCompareExitCodeReflectsGate`.
  - **File lifecycle specified** — `compare.go` (`//go:build ignore`) moves to `ad_hoc/memory_eval/cmd/compare/main.go`; `RunConfig` + `embedderForCfg` deleted.
  - **Paraphrase validator terminal path** — hard-abort on triple failure; no silent fallback to bad data.
- **v2** — fixes from R1 adversarial review:
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
- **3 paraphrased queries per anchor** via `claude -p --model claude-haiku-4-5-20251001`. Prompt: rephrase as natural questions using different phrasing; **validator enforces ≤3 shared content words with source** (case-folded, stop-words excluded, lemmas compared). Returns JSON array. Paraphrases are **cached in `ad_hoc/memory_eval/paraphrase_cache.jsonl`** keyed by `(sha256(anchor_content), prompt_version)`. Cache hits skip Haiku entirely. The cache file is committed — reruns are reproducible without hitting the API.
- **4 distractors per query**, drawn from anchors of different `type`. Distractors are labeled `false` **relative to this specific query** (not necessarily non-relevant in the abstract — the metric only consumes anchor labels anyway). Total: 5 candidates × 150 queries = **750 pairs**.
- **Ground truth by construction.** Anchor→query pair is `true`; distractor→query is `false`. No human step.

Why ≤3 overlap instead of zero: with zero overlap, TFIDF baseline is mathematically forced to MRR=0 (no FTS5 match, no cosine similarity). That resurrects the 0/0 gate facade this rebuild is meant to eliminate. Natural paraphrases of short technical notes typically share 2-3 content words at the lemma level (e.g. "worker respawns on crash" ↔ "how do workers recover from crashes" shares `worker`, `crash`). Allowing ≤3 shared content words gives the baseline a realistic chance to rank the anchor in top-k while still requiring BGE to find semantically-aligned queries that TFIDF misses (queries that share 0-1 content words but describe the same concept).

### Paraphrase caching

`ad_hoc/memory_eval/paraphrase_cache.jsonl` is a committed fixture with schema `{anchor_sha: string, prompt_version: string, queries: [string, string, string]}`. Keys are `sha256(anchor_content)[:16] + "/" + prompt_version` (e.g. `"a1b2c3d4e5f67890/v1"`).

`rebuild_corpus` flow:
1. Compute `anchor_sha` for each of the 50 anchors.
2. Look up `(anchor_sha, current_prompt_version)` in the cache.
3. Cache hit → use cached queries; cache miss → call Haiku, validate, write back to cache.
4. At end, re-serialize the cache (sorted by key) back to `paraphrase_cache.jsonl`.

`TestRebuildCorpusDeterministic` pre-populates the cache with golden paraphrases for a fixed anchor set, runs `rebuild_corpus --seed=42 --no-api`, asserts byte-identical output on two runs. The `--no-api` flag errors on cache miss (never calls Haiku in tests).

`prompt_version` is a string constant in code (`const ParaphrasePromptVersion = "v1"`). Bumping it invalidates the whole cache — used when the prompt changes and we want fresh paraphrases.

#### Cache bootstrap (one-time)

The first time anyone builds a corpus — before `paraphrase_cache.jsonl` exists in the repo — someone must populate it via a live Haiku run. Documented procedure in `ad_hoc/memory_eval/README.md`:

```
# One-time cache bootstrap — requires CLAUDE CLI + HF models + make install
go run ./ad_hoc/memory_eval/cmd/rebuild_corpus \
    --db ~/.oro/projects/oro/state.db \
    --out corpus.jsonl --anchors corpus_anchors.jsonl \
    --seed 42
# Commits paraphrase_cache.jsonl with ~50 entries
git add ad_hoc/memory_eval/paraphrase_cache.jsonl
git commit -m "chore(eval): bootstrap paraphrase cache"
```

#### When the cache must be regenerated

The cache is keyed by `(sha256(anchor_content), prompt_version)`. It becomes stale when either changes:

- **Anchor set changes** — state.db has churn (new memories, deleted memories); `--seed 42` now picks a different set of 50 anchors, and their content hashes don't match cached keys. Symptom: `TestRebuildCorpusDeterministic` with `--no-api` aborts with "cache miss for anchor <sha>".
- **`ParaphrasePromptVersion` is bumped** — developer changed the prompt; every cache entry is now invalid.

Recovery (same as bootstrap): re-run `rebuild_corpus --seed 42` without `--no-api`, commit the updated cache. This is expected developer workflow when evolving the prompt or seed; the only constraint is "commit the cache when regenerating".

**Anchor-set stability hedge**: to reduce churn-driven cache misses, `rebuild_corpus` uses a stable deterministic selector (`(id * 2654435761 + seed) % (1<<31)` — same form as step 2 in the generator pseudocode) rather than `ORDER BY RANDOM()`. New memories at higher ids don't displace existing low-id selections until the selection modulo shifts. For the `--seed 42` fixture we'll commit, stability is good for thousands of new memories.

### Corpus artifacts

- `ad_hoc/memory_eval/corpus.jsonl` — 750 `CorpusEntry` lines. Existing schema. Header `# APPROVED`.
- **NEW** `ad_hoc/memory_eval/corpus_anchors.jsonl` — sidecar. One `{id, type, content}` line per unique memory referenced in corpus (anchors + distractor pool, ~50 entries since distractors come from the same pool).

### Eval harness rewrite

`ad_hoc/memory_eval/compare_impl.go:RunConfigWithEmbedder`:

- **Load anchors from sidecar**, not `builtinFixtures()`.
- **Seed store with sidecar memories**, track corpus-id → store-id map.
- **After each `Store.Insert`, explicitly call `vecIndex.Upsert(storeID, embedder.Embed(content), project)`** for BGE configs. `Store.Insert` does NOT auto-populate the vec0 table (confirmed by reading pkg/memory/memory.go:448-531 — `insertDirect`/`insertWithChunks` write the `memories` table + optional `memory_chunks` table, never `vecIndex`). The pattern is extracted into a shared helper:

  ```go
  // seedStoreWithVectors seeds store with anchors and upserts their embeddings
  // into idx. The caller is responsible for calling store.SetProject(project)
  // BEFORE invoking this helper — helper does not re-apply SetProject. The
  // same `project` MUST be passed to idx.Upsert so vec0 partition matches
  // what the store queries. This contract is lifted from
  // pkg/memory/hybrid_integration_test.go:160-193.
  func seedStoreWithVectors(store *memory.Store, idx memory.VectorIndex,
                            emb memory.Embedder, anchors []CorpusAnchor,
                            project string) (map[int64]int64, error)
  ```

  Caller sequence:

  ```go
  store.SetProject("oro_eval")                      // SOURCE OF TRUTH for scope
  idx, _ := memory.NewSQLiteVecIndex(db)            // project comes via Upsert arg
  anchorMap, _ := seedStoreWithVectors(store, idx, emb, anchors, "oro_eval")
  ```

  If the two project strings disagree, the eval runs against an empty vec0 partition — silently degrading to FTS5-only. `TestSeedFromAnchorSidecar` asserts upsert count matches seed count under a specific project name; a follow-up assertion verifies `vec_version`-scoped partition table `vec_memories_oro_eval` exists with 50 rows.
- **Run all migrations** when opening the in-memory DB: `SchemaDDL` + `MigrateSemanticMemorySearchEvents` + `MigrateSemanticMemoryChunks` + `MigrateSemanticMemoryDense` + `MigrateSemanticMemoryBackfillState`. Without these, `insertWithChunks` fails with "no such table: memory_chunks" for any anchor whose content exceeds 512 tokens.

`embedderForCfg` replaced by a per-config setup function `setupConfig(cfg) → (store, embedder, cleanup)` that wires vector index + reranker:

| Config | Embedder | Vector Index | Reranker |
|--------|----------|--------------|----------|
| `tfidf` | `memory.NewEmbedder()` | `nil` (linear scan on `memories.embedding` blob) | `nil` |
| `dispatcher-warm` | `memory.NewBGEEmbedder(~/.oro/models/bge-small-en-v1.5)` | `memory.NewSQLiteVecIndex(db)` where `db` comes from `OpenEvalDB(libPath)` | `memory.NewBGEReranker(~/.oro/models/bge-reranker-base)` + `SemanticConfig{rerankEnabled:true, rerankTopK:20, rerankFinalK:10}` |
| `solo-cli-cold` | `memory.NewBGEEmbedder(...)` | `memory.NewSQLiteVecIndex(db)` where `db` comes from `OpenEvalDB(libPath)` | `nil` |

Model dir resolution: check `ORO_MODEL_DIR` env override, else default `~/.oro/models`.

### sqlite-vec loading

Current state (R1 finding): `pkg/dbutil/openDB.go` uses `modernc.org/sqlite` (pure-Go, does not support `LoadExtension`). The driver swap to `mattn/go-sqlite3` that would have enabled sqlite-vec (beads oro-c4rm / oro-k4cp) was closed without implementation. Empirically, `go test ./pkg/memory -run TestSQLiteVecIndexUpsertSearchDelete` SKIPs today with "no such function: vec_version".

Strategy: **purpose-built helper in the eval package**, does not touch main codebase.

- **NEW** `ad_hoc/memory_eval/openevaldb.go` exposes `OpenEvalDB(dbPath string) (*sql.DB, error)`. Uses `mattn/go-sqlite3` directly with a registered driver (`"sqlite3_with_vec"`) that installs a `ConnectHook` calling `conn.LoadExtension(libPath, "sqlite3_vec_init")`. `libPath` resolves via `pkg/dbutil.ResolveSqliteVecLibPath()`.
- The eval package imports `mattn/go-sqlite3` under the `//go:build cgo && darwin` tag (same tag as BGE), so it does not affect the main binary's build.
- **Test**: `TestOpenEvalDBLoadsSqliteVec` — asserts `SELECT vec_version()` returns non-empty on an opened DB.
- If `vec_version()` returns error at harness startup, `dispatcher-warm` and `solo-cli-cold` configs fail with a clear message; `tfidf` still runs.

### Runtime prerequisites

- **`make install` — human precondition**, checked at bead 3 / bead 5 startup via:

  ```bash
  test -f ~/.oro/lib/libonnxruntime.dylib && test -f ~/.oro/lib/libtokenizers.dylib \
    || { echo "run 'make install' first"; exit 1; }
  ```

  Must have been run to place `libonnxruntime.dylib` + `libtokenizers.dylib` under `~/.oro/lib/` and wire DYLD paths. `go run ./ad_hoc/memory_eval/cmd/compare` without prior `make install` fails at ORT session init with "image not found". Not modeled as a bead dependency because beadcraft only captures inter-bead deps; callers of `oro work` on bead 3 or bead 5 must satisfy this first.
- The eval harness logs a clear error pointing to `make install` when dylibs are missing.
- Runtime budget is **empirically measured**, not assumed: a prerequisite bead runs `go test -bench BenchmarkBGERerankPair -benchtime=20x ./pkg/memory/...` to calibrate per-pair cross-encoder latency. Result is committed to `ad_hoc/memory_eval/bench.txt` with hardware tag. The `--fast` sample size is chosen so wallclock ≤ 30s on the measured hardware.
- `--fast` flag samples 10 random queries (seed-controlled) for CI smoke tests. Full eval (150 queries) is manual-run only, documented in `ad_hoc/memory_eval/README.md`.

### Build tag strategy

The eval package previously had no build tags. BGE types require `cgo && darwin`; adding mattn/go-sqlite3 doubles down on that. To keep `GOOS=linux CGO_ENABLED=0 go vet ./...` and `make test` green, files are split:

| File | Build tags | Depends on |
|------|-----------|------------|
| `corpus.go` (existing) | none | stdlib only |
| `metrics.go` (NEW) | none | stdlib only — MRR, Hit@K, CheckGate |
| `paraphrase_validator.go` (NEW) | none | stdlib — lemma overlap computation |
| `paraphrase_cache.go` (NEW) | none | stdlib — JSONL read/write |
| `extract.go` (existing) | none | `modernc.org/sqlite` via `pkg/dbutil` (already unused for eval, kept for backward compat) |
| `openevaldb.go` (NEW) | `//go:build cgo && darwin` | `github.com/mattn/go-sqlite3` — ConnectHook + LoadExtension |
| `harness.go` (NEW) | `//go:build cgo && darwin` | `pkg/memory` BGEEmbedder/BGEReranker, SQLiteVecIndex, OpenEvalDB |
| `compare_impl.go` (REWRITE) | `//go:build cgo && darwin` | harness.go |
| `cmd/compare/main.go` (NEW, replaces `compare.go`) | `//go:build cgo && darwin` | compare_impl.go |
| `cmd/rebuild_corpus/main.go` (NEW) | `//go:build cgo && darwin` | extract.go, paraphrase_cache.go |
| Tests | same tag as the file under test | |

Acceptance test: `GOOS=linux CGO_ENABLED=0 go vet ./ad_hoc/memory_eval/...` passes (vets only the untagged files). `go vet -tags "cgo darwin" ./ad_hoc/memory_eval/...` passes (vets all files). Linux CI remains green because all BGE-referencing files are excluded.

Non-cgo/non-darwin impact on main codebase: mattn/go-sqlite3 is added to `go.mod` as an indirect dependency, but only reachable under the tagged files. `GOOS=linux CGO_ENABLED=0 go build ./cmd/oro/...` continues to resolve via modernc.org/sqlite (main codebase unchanged). Verified in QG.

### `testing.Short()` contract

All tests that instantiate real BGE models (load 127MB+ ONNX files, create ORT sessions) wrap in:

```go
if testing.Short() {
    t.Skip("BGE model load; rerun without -short")
}
```

`make test-short` target added: `go test ./... -short -timeout 120s`. `make test` continues to run full suite (existing behavior).

Tests that use `FakeEmbedder` (from `pkg/memory/testhelpers`) or test pure-Go logic (MRR, validator, cache read/write) do NOT skip under `-short`.

### Corpus generator

New binary under `ad_hoc/memory_eval/cmd/rebuild_corpus/main.go`:

1. Open `~/.oro/projects/oro/state.db` (path via flag, default to this).
2. Select 50 anchors deterministically: parameterize SQLite's `RANDOM()` with `ORDER BY (id * 2654435761 + seed) % (1<<31)` (simple seeded hash, no requires for deterministic RANDOM across builds). Filter `WHERE length(content) BETWEEN 50 AND 400 AND token_count(content) <= 512` (the latter computed in Go post-fetch). Group by type to get balanced distribution.
3. For each anchor, compute `anchor_sha = sha256(content)[:16]`.
4. **Look up `(anchor_sha, ParaphrasePromptVersion)` in `paraphrase_cache.jsonl`.**
   - Cache hit: use cached 3 queries.
   - Cache miss AND `--no-api` flag set: abort with clear error listing missing anchors.
   - Cache miss: `claude -p --model claude-haiku-4-5-20251001 --output-format json --system "<prompt>" <anchor_content>` with retry logic (see below).
5. **Paraphrase retry/abort logic:**
   - Parse Haiku response as JSON array of 3 strings. On parse error: retry once. On second parse error: abort the whole run with anchor context in error message.
   - Validate each query: ≤3 shared content words with anchor (case-folded, stop-words excluded via a committed `stopwords.txt` from standard English set). On violation: re-prompt once with stricter instructions. On second violation: templated fallback using `extractVerbPhrase(anchor)` → `"how do I " + verbPhrase + " in this system"`.
   - Templated fallback is also validated. On violation: **abort with hard error**, do not silently accept. (Per R2: no silent fallback to bad data.)
   - Write validated queries back to paraphrase_cache.jsonl, re-sort by key, overwrite the file.
6. For each query: pick 4 distractors from anchors of different `type`, deterministic by seeded hash.
7. Write `corpus.jsonl` + `corpus_anchors.jsonl` + updated `paraphrase_cache.jsonl`.
8. Header in corpus.jsonl: `# APPROVED` + `# generated: <ISO ts>` + `# source_db: <path>` + `# seed: <N>` + `# fallback_rate: <pct>`.
9. **Report `fallback_paraphrase_rate` on stderr.** If `>20%`, abort with clear error — signals the threshold or prompt is misconfigured.

### Metrics (revised)

With 1 relevant memory per query, `precision@10` is capped at 0.10 — the metric hides rank information. Replaced with:

- **MRR (Mean Reciprocal Rank)** — primary. For each query, compute `1/rank` of the anchor in top-k (0 if anchor is absent from top-k). **Denominator is total query count, not queries-with-any-hit.** MRR=1.0 means anchor is always rank 1; MRR=0 means anchor never appears in top-k.
- **Hit@10 (Hit Rate)** — secondary. Fraction of queries where the anchor appears in top-10. Denominator = total query count.
- **Hit@1** — sanity. Fraction of queries where the anchor is rank 1.

Formally:

```go
MRR = (1/N) * Σᵢ (1/rankᵢ if anchor in top-K, else 0)   where N = total queries
Hit@K = (1/N) * Σᵢ 1{anchor in top-K}
Hit@1 = (1/N) * Σᵢ 1{anchor is rank 1}
```

### Gate (revised)

Replaces `warm >= 1.30 * base AND cold >= 1.20 * base`:

```go
type GateResult struct {
    Pass   bool
    Reason string
}

func CheckGate(baseMRR, warmMRR, coldMRR float64) GateResult {
    if baseMRR == 0 {
        return GateResult{Pass: false, Reason: "baseline MRR is 0 — search is broken, cannot compute ratio"}
    }
    if warmMRR < 1.30*baseMRR {
        return GateResult{Pass: false, Reason: fmt.Sprintf("warm MRR %.4f < 1.30×%.4f baseline", warmMRR, baseMRR)}
    }
    if coldMRR < 1.20*baseMRR {
        return GateResult{Pass: false, Reason: fmt.Sprintf("cold MRR %.4f < 1.20×%.4f baseline", coldMRR, baseMRR)}
    }
    return GateResult{Pass: true}
}
```

The explicit `baseMRR == 0` guard is the key fix from R1: without it, a zero baseline makes every ratio trivially pass, reproducing the facade.

### `eval_report.yaml` schema

Machine-parseable YAML, committed to `ad_hoc/memory_eval/eval_report.yaml` on every `compare` run (overwrites prior). Schema:

```yaml
timestamp: 2026-04-18T16:22:10-07:00      # RFC3339
hardware:
  arch: arm64                              # runtime.GOARCH
  os: darwin                               # runtime.GOOS
  cpu: Apple M3 Pro                        # from sysctl -n machdep.cpu.brand_string
corpus:
  seed: 42
  corpus_sha: 9a8f2e6d...                  # sha256 of corpus.jsonl only
  anchors_sha: 7b3f1c2a...                 # sha256 of corpus_anchors.jsonl only
  paraphrase_cache_sha: 4e9d0a8b...        # sha256 of paraphrase_cache.jsonl only
  inputs_sha: a1b2c3d4...                  # sha256 of corpus.jsonl || corpus_anchors.jsonl || paraphrase_cache.jsonl, in that exact order; files LF-only — reproducibility key
  num_queries_scored: 150                  # unique queries evaluated
  num_anchors: 50
  num_pairs: 750                           # total corpus entries incl. distractors
  fallback_paraphrase_rate: 0.04           # fraction of paraphrases from templated fallback (0-1)
models:
  embedder:
    name: bge-small-en-v1.5
    sha: 828e1496d7fab...                  # from memory.KnownModels, re-verified at load
  reranker:
    name: bge-reranker-base
    sha: 15b9a8c3da82e...
  tokenizer:
    sha: d241a60d5e8f0...
configs:
  tfidf:
    mrr: 0.4127
    hit_at_10: 0.6400
    hit_at_1: 0.2867
    runtime_ms: 1247
  dispatcher-warm:
    mrr: 0.6845
    hit_at_10: 0.9067
    hit_at_1: 0.5200
    runtime_ms: 58432
  solo-cli-cold:
    mrr: 0.5932
    hit_at_10: 0.8533
    hit_at_1: 0.4200
    runtime_ms: 4891
gate:
  pass: true
  warm_ratio: 1.658                        # warm_mrr / tfidf_mrr
  cold_ratio: 1.437
  reason: ""                               # populated when pass=false
```

Also prints a human-readable summary table to stdout (tab-writer), for quick inspection.

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

### Build/compile gates

1. `GOOS=linux CGO_ENABLED=0 go vet ./ad_hoc/memory_eval/...` passes (vets untagged files only; all BGE-referencing files excluded by build tag).
2. `GOOS=linux CGO_ENABLED=0 go build ./cmd/oro/...` passes (main codebase unaffected by mattn addition).
3. `go vet -tags "cgo darwin" ./ad_hoc/memory_eval/...` passes.
4. `make test` on a non-darwin or non-cgo machine does not regress (eval package excluded via tags).

### Unit tests (no BGE, no external API — always run)

1. `TestMRRSingleRelevant` — anchor at rank 1 → MRR=1.0; rank 5 → MRR=0.2; absent → contributes 0 to sum; denominator = total query count.
2. `TestCheckGateZeroBaselineFails` — `CheckGate(0, 1, 1).Pass == false`.
3. `TestCheckGatePassesWhenRatiosMet` — `CheckGate(0.5, 0.65, 0.60).Pass == true`.
4. `TestParaphraseValidator` — golden inputs: 0 overlap PASS, 2 overlap PASS, 3 overlap PASS, 4 overlap FAIL; case-folded; stop-word list applied; lemma-match for simple plurals (`worker/workers`, `crash/crashes`).
5. `TestParaphraseCacheRoundtrip` — write 3 entries, re-read, assert equal; keys sorted in file.
6. `TestRebuildCorpusDeterministic` — with `--no-api` and a fully-populated fixture cache, two runs with identical `--seed 42` produce byte-identical `corpus.jsonl` + `corpus_anchors.jsonl`.
7. `TestGroundTruthIntegrity` — every `candidate_memory_id` in `corpus.jsonl` has a matching row in `corpus_anchors.jsonl`; no null `relevant` values; every anchor appears as `relevant:true` for ≥1 query.
8. `TestFallbackRateAbort` — when fallback rate exceeds 20%, `rebuild_corpus` returns a non-zero exit.

### Integration tests (require BGE + dylibs — skipped under `-short`)

1. `TestSetupConfigWarmUsesBGE` — asserts `setupConfig("dispatcher-warm")` returns a `*BGEEmbedder`, not a `*TFIDFEmbedder`. Wrapped in `if testing.Short() { t.Skip(...) }`.
2. `TestSetupConfigColdUsesBGENoRerank` — BGE embedder + nil reranker. Same skip.
3. `TestSeedFromAnchorSidecar` — loads anchors, seeds store via helper, asserts `vecIndex.Upsert` was called N times (where N = number of anchors). Same skip.
4. `TestOpenEvalDBLoadsSqliteVec` — `SELECT vec_version()` returns non-empty; same skip.
5. `TestCompareExitCodeReflectsGate` — construct a synthetic corpus where TFIDF beats BGE (or use a mock), run `compare`, assert exit code 1 when gate fails. Assert exit 0 when gate passes.
6. `TestCompareFastCompletesUnder30s` — `go run ./cmd/compare --fast --corpus <fixture>` finishes within 30s wallclock on the test machine. Skip under `-short`.

### Command runs

1. `go run ./ad_hoc/memory_eval/cmd/rebuild_corpus --db ~/.oro/projects/oro/state.db --out ad_hoc/memory_eval/corpus.jsonl --anchors ad_hoc/memory_eval/corpus_anchors.jsonl --seed 42` produces:
    - 50-line `corpus_anchors.jsonl`
    - 750-line `corpus.jsonl` with `# APPROVED` header and `relevant: true|false` on every entry
    - Updated `paraphrase_cache.jsonl`
    - Fallback rate ≤20% (else non-zero exit)
2. `go run ./ad_hoc/memory_eval/cmd/compare --corpus ad_hoc/memory_eval/corpus.jsonl --anchors ad_hoc/memory_eval/corpus_anchors.jsonl` (after `make install`) runs end-to-end:
    - Loads all 3 configs without ORT/tokenizer/sqlite-vec errors
    - `tfidf` baseline MRR is **> 0** (guaranteed by ≤3 overlap rule)
    - Writes `eval_report.yaml` with full schema (see above)
    - Exits with code per revised `CheckGate`
3. `git diff --stat HEAD~N HEAD` shows: new corpus files + report + design doc + ~5 new source files + ~11 new test files. No changes to main binary build tags.

### Benchmark prerequisite

1. `go test -bench BenchmarkBGERerankPair -benchtime=20x ./pkg/memory/...` output committed to `ad_hoc/memory_eval/bench.txt` before finalizing `--fast` sample size. If per-pair latency measured as `X ms`, `--fast` sample size is:

    ```
    size = max(5, min(10, floor(30000 / (20 * X))))
    ```

    Floor=5 guarantees minimum statistical meaning (one query MRR is noise; 5 queries gives a discernible trend). If `floor(30000 / (20 * X)) < 5` — i.e. per-pair latency >300ms — bead 3 aborts with a clear error pointing to either faster hardware or a longer `--fast` budget. This prevents the "CI-fast but nobody runs full eval" facade.

    **Bead 3 prereq**: `make install` must have been run so ORT + tokenizer dylibs are at `~/.oro/lib/` and BGE models are under `~/.oro/models/`. Stated in bead's Read: field.

### Anti-criteria (explicit non-goals)

- **Not** required: `dispatcher-warm` MRR must beat `tfidf` by 1.30×. If BGE loses on this domain, that's a finding we report, not a failure we paper over.
- **Not** required: distractor labels validated as truly non-relevant. The metric only consumes anchor labels; distractor labels are informational only. ACs explicitly note this rather than papering over it.
- **Not** required: corpus entries exactly 100. The spec's "warning below 100" threshold is satisfied by 750.
- **Not** required: the driver swap (modernc → mattn) in the main codebase. Eval uses a purpose-built helper that bypasses the issue; main binary unchanged.
- **Not** required: paraphrases remain semantically "perfect" — the ≤3 overlap constraint + templated fallback means some paraphrases may be stilted. That's fine. Gate is what matters.

## Documented Residual Risks (accepted, not mitigated this round)

Per R3 recommendation: the following are acknowledged tradeoffs rather than design defects. If any materializes as a real problem during first-run, file a follow-up bead.

1. **≤3 overlap threshold is a first-run guess, not an empirical measurement.** R3 asked for sampling 10 anchors through live Haiku and measuring overlap distribution before committing. Not done. Risk: if actual Haiku output exceeds ≤3 on >20% of attempts, `rebuild_corpus` aborts per step 9. Recovery path: bump `MaxSharedContentWords` in `ad_hoc/memory_eval/paraphrase_validator.go` (one-line change) OR loosen the prompt instruction OR raise the fallback abort threshold. Documented in `ad_hoc/memory_eval/README.md` under "Troubleshooting".

2. **Gate thresholds 1.30× / 1.20× inherited from original precision@k spec.** MRR is scaled differently; at low baselines (e.g. MRR=0.05) a 1.30× ratio is noise. Not re-justified for v5. If the first run shows tfidf MRR in a low-signal range, file a follow-up bead to either (a) re-justify the thresholds with literature, (b) switch to absolute-delta gating (`warm_mrr - base_mrr ≥ 0.10`), or (c) require both absolute and ratio. Constants to change: `WarmMRRRatio` / `ColdMRRRatio` in `ad_hoc/memory_eval/metrics.go`. Eval still runs; gate decision is just interpretable or not.

3. **Lemmatizer scope = plural-s only.** Verb inflections (`run/ran/running`, `retry/retried`) bypass overlap detection. Over-counts as "non-overlap" where real overlap exists; TFIDF baseline slightly weaker than reality, warm more likely to beat it for a wrong reason. Acceptable for first-run signal. If gate ambiguity arises, upgrade to a proper lemmatizer (`github.com/aaaton/golem` or Snowball stemmer).

4. **Fallback rate check fires at end of run.** 50 anchors, 150 paraphrase attempts. Worst case: 31 fallbacks (21% of 150) after 49 anchors — burns all API calls before aborting. Accepted; checking per-anchor has its own problems (early-run variance).

5. **Distractor entries in `corpus.jsonl` are dead data for the current metric.** Kept for (a) future metrics that may consume them (e.g. pair-ranking loss), (b) explicit ground truth provenance. `eval_report.yaml.num_queries_scored: 150` vs `num_pairs: 750` makes the distinction explicit.

## Premortem

| Risk | Severity | Mitigation |
|------|----------|------------|
| Haiku overlap constraint violated | medium | Validator enforces ≤3 shared content words (case-folded, stop-word filtered, lemma-compared); re-prompt once; fall back to templated `"how do I <verb-phrase>"` rewrite from anchor; abort with hard error if templated also fails (no silent bad data); abort whole run if fallback rate >20% |
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

Seven beads (to be formalized by `beadcraft`). Each lists its test dependencies. Beads are tagged build-tag-isolated; run order matters.

1. **Metrics + CheckGate (pure Go, no tags)** — `ad_hoc/memory_eval/metrics.go` + tests. Implement MRR, Hit@K, CheckGate v2 per the formal definitions above. Constants to expose as package-level `const`: `WarmMRRRatio = 1.30`, `ColdMRRRatio = 1.20`. Tests: `TestMRRSingleRelevant`, `TestCheckGateZeroBaselineFails`, `TestCheckGatePassesWhenRatiosMet`. ~150 LOC.

2. **Paraphrase validator + cache (pure Go, no tags)** — `ad_hoc/memory_eval/paraphrase_validator.go` + `ad_hoc/memory_eval/paraphrase_cache.go` + tests. Content-word overlap count (stop-words excluded, lemmatized via simple plural rule), cache JSONL read/write. Constants to expose as package-level `const`: `MaxSharedContentWords = 3` (in `paraphrase_validator.go`), `ParaphrasePromptVersion = "v1"` (in `paraphrase_cache.go`). Tests: `TestParaphraseValidator`, `TestParaphraseCacheRoundtrip`. ~200 LOC. Also commits initial `stopwords.txt` (standard English).

3. **Benchmark prerequisite** — `pkg/memory/bge_reranker_bench_test.go` adds `BenchmarkBGERerankPair`. Run benchmark, commit result to `ad_hoc/memory_eval/bench.txt`. Choose `--fast` sample size from result. ~30 LOC.

4. **`OpenEvalDB` + sqlite-vec loading (`//go:build cgo && darwin`)** — `ad_hoc/memory_eval/openevaldb.go` registers `sqlite3_with_vec` driver with ConnectHook; `OpenEvalDB(dbPath)` returns `*sql.DB`. Test: `TestOpenEvalDBLoadsSqliteVec`. Adds mattn/go-sqlite3 to go.mod. QG verifies Linux/no-cgo build still passes. ~100 LOC.

5. **Harness rewrite (`//go:build cgo && darwin`)** — `ad_hoc/memory_eval/harness.go` (NEW) with `setupConfig(cfg)` + `seedStoreWithVectors(store, idx, emb, anchors)`; `compare_impl.go` rewritten to use harness. Tests: `TestSetupConfigWarmUsesBGE`, `TestSetupConfigColdUsesBGENoRerank`, `TestSeedFromAnchorSidecar`. ~400 LOC.

6. **`rebuild_corpus` CLI (`//go:build cgo && darwin` because of paraphrase cache + state.db path)** — `ad_hoc/memory_eval/cmd/rebuild_corpus/main.go`. Opens state.db, selects anchors, calls Haiku with cache, validates, writes corpus + sidecar + updated cache. Tests: `TestRebuildCorpusDeterministic`, `TestGroundTruthIntegrity`, `TestFallbackRateAbort`. Commits initial `paraphrase_cache.jsonl` fixture. ~400 LOC.

7. **`compare` CLI (`//go:build cgo && darwin`) + Run + commit numbers** — move `compare.go` to `ad_hoc/memory_eval/cmd/compare/main.go` (drop `//go:build ignore`), wire `--fast`, write `eval_report.yaml`. Tests: `TestCompareFastCompletesUnder30s`, `TestCompareExitCodeReflectsGate`. Final step: run the whole pipeline end-to-end against live state.db, commit corpus + sidecar + cache + `eval_report.yaml`. Close `oro-pvw8` with the committed report hash as evidence. This bead gates master epic `oro-0hjm`.

## Success Definition

Master epic `oro-0hjm` closes with:
- A corpus the whole team can see was built to actually test semantic retrieval
- A compare report with real BGE numbers
- Honest gate outcome (pass or fail), not a `0/0` facade

If the gate fails, file a follow-up bead with the observed numbers and the next hypothesis (chunking knobs, rerank top-k tuning, model swap). That's success too — it's the first time we'd actually know what the overhaul delivers.
