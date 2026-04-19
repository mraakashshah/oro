# Changelog

All notable changes to oro. Entries are grouped by theme, not release — oro does not yet tag releases.

## 2026-04-19 — Eval Harness Rebuild + BGE Correctness Fixes

The 2026-04-18 eval gate passed trivially with a 0/0/0 facade (see below). Rebuilt the harness to actually test BGE vs TFIDF, then found and fixed three compounding bugs that had been masking BGE's real performance.

### Eval harness rebuild (epic `oro-d1d3`, 15 beads)

Design doc `docs/plans/2026-04-18-memory-eval-harness-rebuild-design.md` after 5 rounds of adversarial review. New harness:

- 50 anchor memories sampled deterministically from `state.db` (`SelectAnchors`)
- 3 paraphrased queries per anchor with overlap validator (`CountSharedContentWords ≤ 3`)
- 4 distractors per query (different type), 750 corpus pairs total
- Metrics: MRR + Hit@10 + Hit@1 (precision@10 capped at 0.10 with 1-relevant-per-query)
- `CheckGate v2` with explicit `baseMRR == 0 → GateFail` guard
- Purpose-built `OpenEvalDB` using `mattn/go-sqlite3` + `LoadExtension` for `sqlite-vec`
- Per-project HNSW isolation (project=`oro_eval`, SQL-safe identifier)
- Paraphrase cache fixture (committed JSONL, keyed by anchor sha + prompt version)
- `eval_report.yaml` with `inputs_sha` reproducibility key
- `--fast` sampling path with `max(5, min(10, floor(30000/(20·X))))` sizing
- Build-tag split: metrics/validator/cache untagged; harness/OpenEvalDB/BGE-specific files `//go:build cgo && darwin`

### BGE correctness fixes (three compounding bugs)

The rebuilt harness produced `MRR warm=0.390 < tfidf=0.498` on first run — an "honest fail" suggesting BGE underperforms. Root cause: BGE was producing garbage, not bad rankings.

- `fix(memory)`: BGE embedder was missing `token_type_ids` input. `newORTSession` declared only `[input_ids, attention_mask]`, but `bge-small-en-v1.5` is a BERT model declaring three inputs. ONNX logged per-call `Missing Input: token_type_ids` errors; embeddings were near-degenerate. Fix: introspect model with `GetInputOutputInfo`, bind declared input names, synthesise all-zeros `token_type_ids` tensor inside `Run`. Also discover output name dynamically (embedder: `last_hidden_state`; reranker: `logits`).
- `fix(memory)`: BGE reranker was using the wrong tokenizer. `KnownModels` registered only one `bge-tokenizer` (BERT WordPiece for `bge-small`), and the reranker integration test copied that file to the reranker's model dir. But `bge-reranker-base` is XLM-RoBERTa (SentencePiece Unigram) with completely different vocabulary. Loading it produced token IDs that landed in meaningless positions of the reranker's embedding matrix → ~uniform -10 logits for every pair. Added `bge-reranker-tokenizer` entry with pinned sha256 and updated the test download.
- `fix(memory)`: BGE reranker was tokenising query+doc as a single sentence. Cross-encoder is trained on pair inputs `[<s> query </s></s> doc </s>]`; the code was running `EncodeWithOptions(query+" "+doc, true)` which applies the single-sentence template. Fix: tokenize query and doc separately with `addSpecialTokens=false` and assemble the pair sequence from raw IDs using XLM-R specials (BOS=0, EOS=2), reserving half the token budget for each side with doc-first truncation.
- `fix(memory)`: HybridSearch sort was non-deterministic on ties. `sortByScoreDesc` used `sort.Slice` (unstable) on results built by iterating a map (`fuseRRF`'s `byID`). Go map iteration is randomised; ties in RRF scores — which are common, as scores come from a small set of rank reciprocals — produced different orderings on identical inputs. Same issue in `vectorSearchLinear`/`vectorSearchLinearRowOnly`. Fix: `sort.SliceStable` with deterministic tiebreak by ID ascending. tfidf MRR is now bit-stable across runs; BGE paths are within ~2% (residual ORT FP noise).

### Canonical eval results (commit `af7216b2` + `6aaaeaab`)

**Templated corpus** (150 queries, 50 anchors, 750 entries):

| config | MRR | Hit@10 | Hit@1 |
|---|---|---|---|
| tfidf | 0.258 | 0.889 | 0.141 |
| dispatcher-warm | 0.683 | 0.930 | 0.570 |
| solo-cli-cold | 0.500 | 0.944 | 0.352 |

warm ratio 2.65×, cold ratio 1.94× — gate passes.

**Haiku-paraphrase corpus** (90 queries, 30 anchors, 450 entries, generated via `claude-haiku-4-5-20251001`):

| config | MRR | Hit@10 | Hit@1 |
|---|---|---|---|
| tfidf | 0.427 | 0.900 | 0.244 |
| dispatcher-warm | 0.954 | 0.989 | 0.933 |
| solo-cli-cold | 0.590 | 1.000 | 0.389 |

warm ratio 2.23×, cold ratio 1.38× — gate passes.

BGE dominates both corpora. The 2026-04-18 "honest fail" was an artefact of three compounding bugs, not a real BGE weakness.

### Follow-ups filed

- Check if `ad_hoc/memory_eval/eval_report.txt` (the pre-fix artifact) is referenced anywhere — deleted during the rebuild but watch for stale references
- `semantic_memory_search_events` telemetry table is not yet migrated into production `state.db` — when wired, becomes a third matrix axis (real user queries)

## 2026-04-16 → 2026-04-18 — Semantic Memory Overhaul

Replace TF-IDF memory retrieval with BGE-small embeddings (ONNX), sqlite-vec HNSW ANN, chunked embeddings (content cap 2048 → 8192), cross-encoder reranker, per-project HNSW partitions, and query telemetry. Design: `docs/plans/2026-04-16-semantic-memory-overhaul-design.md` (v3.2). Darwin-only, cgo; dispatcher owns ORT with CLI fallback.

### Phase 1 — `pkg/dbutil` extraction

Centralized all SQLite opens behind `dbutil.OpenDB` with WAL + busy-timeout + Ping contract — prerequisite for the driver swap in Phase 4.

- `feat(dbutil)`: extract `OpenDB` into `pkg/dbutil` with WAL/busy-timeout/Ping contract (oro-z4im)
- `refactor`: migrate 23 `sql.Open` sites (memory, eventlog, protocol, worker, dispatcher, integration, cmd/oro tests) to `dbutil.OpenDB`

### Phase 2 — `Embedder` / `Reranker` / `VectorIndex` interfaces

Decoupled the concrete TF-IDF embedder from callers so Phase 3/5 can swap in BGE + HNSW without touching `Store`.

- `feat(memory)`: `Embedder` / `Reranker` / `VectorIndex` interfaces + `ANNResult` (oro-ln8x)
- `feat(memory)`: `VocabPersister` interface for optional vocab persistence
- `refactor(memory)`: rename concrete `Embedder` → `TFIDFEmbedder` implementing `Embedder` + `VocabPersister` (oro-krvk)
- `refactor(memory)`: widen `Store.SetEmbedder` to `Embedder` interface (oro-ot51)
- `feat(memory)`: `InMemoryVecIndex` test double + hold RLock across `Search` iteration (oro-85m4)
- `feat(memory/testhelpers)`: `FakeEmbedder` (FNV-32 hash-trick, ~Jaccard cosine) for ONNX-free unit tests (oro-rrcr.2)

### Phase 3 — `BGEEmbedder` + dispatcher ownership + CLI fallback + backfill

Real bge-small-en-v1.5 embeddings via `onnxruntime_go`. Dispatcher owns one ORT session (warms on start); worker CLI forwards over UDS. Solo-CLI fallback loads ORT on demand. Backfill worker re-embeds old TF-IDF rows at 50/sec with CAS owner lock.

- `feat(protocol)`: `EmbedRequest` / `EmbedResponse` message types
- `feat(langprofile)`: `SemanticMemoryConfig` + `MemoryConfig` + `WithDefaults` (oro-ooge)
- `feat(protocol)`: `MigrateSemanticMemoryDense` + `MigrateSemanticMemoryBackfillState` migrations
- `feat(install)`: bundle ORT + tokenizer dylibs under `~/.oro/lib/` (oro-36t2)
- `feat(memory)`: `models.go` — `ModelPath`, `PrefetchModels`, SHA256 digests (oro-u218)
- `feat(memory)`: `Store.checkEmbedderModelMatch()` to guard vocab vs model mismatch
- `feat(cli)`: `oro models list|verify|prefetch` subcommands (oro-e9rt)
- `feat(memory)`: `BGEEmbedder` gated behind `cgo && darwin`, fix Embed/Close race + ORT session release (oro-mrf7)
- `feat(dispatcher)`: embedder fields + sentinels + `WaitForEmbedder` (oro-p545.1)
- `feat(dispatcher)`: `warmupEmbedder` goroutine + `SemanticModelDir` config (oro-y5bg)
- `feat(memory)`: backfill CAS owner-lock + TOCTOU-safe stale steal + `ErrBackfillLocked` (oro-p545.5)
- `feat(memory)`: `backfillWorker` — UPDATE/INSERT loop + 50/sec rate-limit + completion (oro-p545.6)
- `feat(memory)`: 30-day retention trim wired into dream trigger (oro-s3g7)
- `feat(eval)`: extract 100 candidate query→memory pairs to `ad_hoc/memory_eval/corpus.jsonl` (oro-qwq8)

### Phase 4 — sqlite-vec HNSW + driver swap

Replace the 1000-row linear cosine scan with real HNSW ANN. One `vec0` virtual table per project for isolation.

- `feat(install)`: bundle `sqlite-vec.dylib` + `vendor-sqlite-vec` Makefile target, SHA256-pinned (oro-05fa)
- `feat(dbutil)`: `ResolveSqliteVecLibPath` honouring `ORO_SQLITE_VEC_LIB`
- `feat(memory)`: `SQLiteVecIndex` with per-project `vec0` tables (oro-y4i1)
- `refactor(memory)`: replace `vectorSearch` linear-scan with `VectorIndex` HNSW path (oro-d6j9)
- `test(memory)`: integration test for `HybridSearch` with `SQLiteVecIndex` (oro-jm2g)

### Phase 5 — chunking + `memory_chunks` + max-sim + `RerankByIDs` protocol

Long memories now chunked on insert (256/32 sliding window) and scored with max-sim parent aggregation. Dispatcher gets `RerankByIDsRequest` IPC primitive for Phase 6.

- `feat(memory)`: lift content cap 2048 → 8192 in `prepareInsert` + FTS5 smoke test
- `feat(protocol)`: `RerankByIDsRequest` / `RerankByIDsResponse` types + marshaled-size test (oro-mhio)
- `feat(memory)`: `chunkContent` with 256/32 sliding window + `Chunk` struct (oro-6cha)
- `feat(protocol)`: `MigrateSemanticMemoryChunks` schema constant
- `feat(memory)`: populate `memory_chunks` atomically on Insert when > 512 tokens (oro-t4ml)
- `feat(memory)`: max-sim parent scoring in `HybridSearch` vector arm (oro-2jms)

### Phase 6 — bge-reranker-base cross-encoder (lazy, config-gated)

Real cross-encoder reranking. Dispatcher lazy-loads on first request via `sync.Once`; solo CLI cleanly skips rerank with a 2s timeout fallback.

- `feat(memory)`: `BGEReranker` impl in `pkg/memory/bge_reranker.go` (oro-lhln)
- `test(memory)`: `TestRerankerModelEntry` — KnownModels registry + tokenizer.json co-download
- `feat(dispatcher)`: UDS handler for `RerankByIDsRequest` with lazy-load on first request (oro-dupu)
- `feat(memory)`: rerank hook in `HybridSearch` with config gate + 2s timeout fallback (oro-23l2)

### Phase 7 — telemetry + eval + precision@k gate

Every hybrid search logs a `SearchEvent`. Precision@k CLI validates the overhaul's success criteria.

- `feat(protocol)`: `MigrateSemanticMemorySearchEvents` schema constant (oro-1knj)
- `feat(memory)`: `SearchEvent` struct + `logSearchEvent` hook (oro-qtdo)
- `feat(memory)`: wire `logSearchEvent` into `HybridSearch` (oro-rlde)
- `feat(eval)`: `ad_hoc/memory_eval/compare.go` precision@k CLI + gate (dispatcher-warm ≥ 1.30× baseline, solo-cli-cold ≥ 1.20×) (oro-jqwh)
- `test(memory)`: `BGERerankerScores` integration test + `HybridSearchRerankSkippedSoloCLI` unit test (oro-092z)

## Supporting Infrastructure

Dispatcher/merge/protocol fixes landed alongside the overhaul, mostly self-filed by the swarm or addressed mid-session.

### Merge + worktree reliability
- `fix(merge)`: retry rebase onto primary HEAD instead of stale `effectiveTarget` (oro-nhja) — kills the ff-only exit-128 loop
- `fix(dispatcher)`: delete stale agent branches before worktree creation (oro-t6h7)
- `fix(dispatcher)`: restore `resetOrphanedBeads` doc + cover startup prune (oro-t6h7)

### Dolt health probe + recovery
- `feat(dispatcher)`: `checkDoltHealth` — dolt-reachability probe (oro-ryco)
- `feat(dispatcher)`: `recoverDolt` — backoff + escalation (oro-lh9u)
- `feat(dispatcher)`: wire dolt probe + recovery into `heartbeatLoop` (oro-i3ky)
- `feat(dispatcher)`: `maybeChangeDetectionBackup` for heartbeat-triggered backups

### OVERSIZED_BEAD guard fixes
- `fix(protocol)`: strip parentheticals and handle semicolons in `CountDistinctModules` (oro-c6w9)
- `fix(protocol)`: skip bare line numbers + symbol/non-path continuations (oro-vpjt)

### CLI + misc
- `feat(cli)`: document `max-workers` directive in help + e2e test (oro-i148)
- `fix(dispatcher)`: remove dead `LLMEstimator`; bump stale model IDs to current
