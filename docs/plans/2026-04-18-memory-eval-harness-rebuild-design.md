# Memory Eval Harness Rebuild — Real BGE vs TF-IDF Validation

**Date:** 2026-04-18
**Status:** design / pending adversarial review
**Related:** `docs/plans/2026-04-16-semantic-memory-overhaul-design.md` (v3.2), bead `oro-pvw8` (reopened), master epic `oro-0hjm`

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
- **3 paraphrased queries per anchor** via `claude -p --model claude-haiku-4-5-20251001`. Prompt: rephrase as natural questions, MUST NOT reuse any content word from the source (excluding common stop words). Returns JSON array.
- **4 distractors per query**, drawn from anchors of different `type`. Distractors are labeled `false` by construction. Total: 5 candidates × 150 queries = **750 pairs**.
- **Ground truth by construction.** Anchor→query pair is `true`; distractor→query is `false`. No human step.

### Corpus artifacts

- `ad_hoc/memory_eval/corpus.jsonl` — 750 `CorpusEntry` lines. Existing schema. Header `# APPROVED`.
- **NEW** `ad_hoc/memory_eval/corpus_anchors.jsonl` — sidecar. One `{id, type, content}` line per unique memory referenced in corpus (anchors + distractor pool, ~50 entries since distractors come from the same pool).

### Eval harness rewrite

`ad_hoc/memory_eval/compare_impl.go:RunConfigWithEmbedder`:

- **Load anchors from sidecar**, not `builtinFixtures()`.
- **Seed store with sidecar memories**, track corpus-id → store-id map.

`embedderForCfg` replaced by a per-config setup function that also wires vector index + reranker:

| Config | Embedder | Vector Index | Reranker |
|--------|----------|--------------|----------|
| `tfidf` | `memory.NewEmbedder()` | `nil` (linear scan on `memories.embedding` blob) | `nil` |
| `dispatcher-warm` | `memory.NewBGEEmbedder(~/.oro/models/bge-small-en-v1.5)` | `memory.NewSQLiteVecIndex(...)` with sqlite-vec extension | `memory.NewBGEReranker(~/.oro/models/bge-reranker-base)` + `SemanticConfig{rerankEnabled:true}` |
| `solo-cli-cold` | `memory.NewBGEEmbedder(...)` | `memory.NewSQLiteVecIndex(...)` | `nil` |

Model dir resolution: check `ORO_MODEL_DIR` env override, else default `~/.oro/models`.

### Corpus generator

New binary under `ad_hoc/memory_eval/cmd/rebuild_corpus/main.go` (or extension of `extract.go`):

1. Open `~/.oro/projects/oro/state.db` (path via flag, default to this).
2. Select 50 anchors: `SELECT id, type, content FROM memories WHERE length(content) BETWEEN 50 AND 400 GROUP BY type ORDER BY RANDOM() LIMIT 50` — varied length, varied types, deterministic seed via flag.
3. For each anchor: shell `claude -p --model claude-haiku-4-5-20251001 --output-format json` with a structured prompt. Parse the JSON response; validate lexical overlap constraint; retry once if violated, fall back to templated paraphrase if retry fails.
4. For each query: pick 4 distractors from anchors of different `type`.
5. Write `corpus.jsonl` + `corpus_anchors.jsonl`.
6. Header: `# APPROVED` + `# generated: <ISO ts>` + `# source_db: <path>`.

### Data flow

```
state.db (699 memories)
   │
   ▼
rebuild_corpus: pick 50 anchors, paraphrase, build pairs
   │
   ├──► corpus_anchors.jsonl (50 lines: id, type, content)
   └──► corpus.jsonl (750 lines: query, candidate_memory_id, relevant, source)
                │
                ▼
  compare.go (3 configs)
   │
   ├──► tfidf: TFIDFEmbedder + linear scan + no rerank
   ├──► dispatcher-warm: BGEEmbedder + SQLiteVecIndex + BGEReranker
   └──► solo-cli-cold: BGEEmbedder + SQLiteVecIndex + no rerank
                │
                ▼
         precision@5, precision@10 per config
                │
                ▼
         gate: warm ≥ 1.30×base AND cold ≥ 1.20×base
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

1. `go test ./ad_hoc/memory_eval/... -count=1` passes, including a new `TestRunConfigWarmUsesBGE` that asserts `embedderForCfg("dispatcher-warm")` returns a `*BGEEmbedder`, not a `*TFIDFEmbedder`.
2. `go run ad_hoc/memory_eval/cmd/rebuild_corpus/main.go --db ~/.oro/projects/oro/state.db --out ad_hoc/memory_eval/corpus.jsonl --anchors ad_hoc/memory_eval/corpus_anchors.jsonl` produces:
   - 50-line `corpus_anchors.jsonl`
   - 750-line `corpus.jsonl` with `# APPROVED` header and `relevant: true|false` on every entry (no nulls)
3. `go run ad_hoc/memory_eval/compare.go --corpus ad_hoc/memory_eval/corpus.jsonl --k 10` runs end-to-end, loads all 3 embedders successfully (no ORT/tokenizer errors), prints a per-config precision table with non-zero values for at least the `tfidf` config (proves search is working).
4. The per-config `ratio_vs_baseline` printed is the real computed ratio — not a `0/0` artifact.
5. The exit code reflects the actual gate outcome (0 = pass, 1 = fail). Report is committed to `ad_hoc/memory_eval/eval_report.txt`.

### Anti-criteria (explicit non-goals)

- **Not** required: `dispatcher-warm` must beat `tfidf` by 1.30×. If BGE loses on this domain, that's a finding we report, not a failure we paper over.
- **Not** required: every distractor validated as `false`. We trust the type-disjoint construction and accept some noise.
- **Not** required: corpus entries exactly 100. The spec's "warning below 100" threshold is satisfied by 750.

## Premortem

| Risk | Severity | Mitigation |
|------|----------|------------|
| Haiku leaks source words into paraphrased queries | medium | Prompt emphasizes zero overlap; validator checks; re-prompt once; fallback to templated `"how do I <first-verb-phrase>"` rewrite |
| BGE genuinely underperforms TF-IDF on keyword-heavy technical notes | low (this is a finding, not failure) | Report per-k table; gate fails honestly; follow-up bead to revisit thresholds or embedding strategy |
| Distractor accidentally relevant to query | medium | Draw from different `type` than anchor; smoke-check first 10 samples post-generation |
| ORT runtime library missing on this machine | high but observable | `NewBGEEmbedder` returns clear error at startup; investigate and install before proceeding |
| SQLiteVecIndex load extension fails | medium | `~/.oro/lib/sqlite-vec.dylib` exists; `ResolveSqliteVecLibPath` already handles env override |
| Corpus anchor IDs collide with in-memory SQLite autoincrement during seeding | medium | Use `anchorIDMap[corpus_id] = store_id` indirection (already present in current code as `fixtureIDMap`) |
| Haiku API unavailable / rate-limited | low | Serialize calls; retry with exponential backoff; 50 calls fit easily inside normal quotas |

## Implementation Outline

Roughly three beads (to be formalized by `beadcraft`):

1. **Rewrite `compare_impl.go`** to seed from sidecar + use real BGE/HNSW/reranker for warm and cold. TDD: `TestRunConfigWarmUsesBGE`, `TestRunConfigColdUsesBGENoRerank`, `TestSeedFromAnchorSidecar`.
2. **Build `cmd/rebuild_corpus/main.go`** — `claude -p` paraphrase loop + anchor selection + distractor assembly + sidecar write.
3. **Run + commit** — execute, capture real numbers, commit corpus + sidecar + `eval_report.txt`. Close `oro-pvw8` properly (or supersede it).

## Success Definition

Master epic `oro-0hjm` closes with:
- A corpus the whole team can see was built to actually test semantic retrieval
- A compare report with real BGE numbers
- Honest gate outcome (pass or fail), not a `0/0` facade

If the gate fails, file a follow-up bead with the observed numbers and the next hypothesis (chunking knobs, rerank top-k tuning, model swap). That's success too — it's the first time we'd actually know what the overhaul delivers.
