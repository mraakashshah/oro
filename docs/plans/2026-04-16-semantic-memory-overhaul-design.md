# Semantic Memory Overhaul

**Date:** 2026-04-16
**Status:** v2 — revised after adversarial review (Stage 2 FAIL → fix → re-review)
**Goal:** Replace oro's TF-IDF memory retrieval with BGE-small embeddings, sqlite-vec HNSW ANN, chunked embeddings, cross-encoder reranking, per-project partitions, and search telemetry. Cherry-picked from [memvid evaluation](../../archive/yap/reference/memvid/evaluation.md).

## Problem

`pkg/memory/embed.go` ships a bag-of-words TF-IDF embedder and `pkg/memory/memory.go:484-565` linear-cosine-scans up to 1000 rows per vector query. Fine at today's scale, but:

- TF-IDF captures *lexical* similarity, not *semantic*. Paraphrased queries miss — "how do I retry a failed bead" doesn't rank a memory about "worker respawn after crash."
- Linear scan caps recall at 1000 rows. A heavy user with 20k memories silently loses precision.
- Content cap of 2048 chars (memory.go:210) forces long `summary` / `decision` entries to be truncated or split upstream.

The memvid evaluation landed on three cherry-picks (BGE-small, sqlite-vec HNSW, chunking). This spec extends with cross-encoder reranking, per-project HNSW partitions, and query telemetry — the scope confirmed as "most ambitious."

## Non-goals

- HyDE-style query expansion. Separate spec.
- LLM-driven consolidation beyond what `dream.go` already does. `dream.go` uses LLM-produced `[DELETE]/[CREATE]/[MERGE]` actions — it does not cluster today, so nothing to "switch." Unchanged by this spec.
- Per-query embedding cache. Trivial bolt-on, follow-up bead.
- Linux support. `install.sh` is Darwin-only today; platform matrix expansion is a parallel spec.
- Replacing FTS5. BM25 via FTS5 stays — text arm of the hybrid fusion.

## Architecture

### Decisions locked in brainstorm

| Q | Decision | Rationale |
|---|---|---|
| Distribution model | Cgo; bundled ORT lib + lazy model download | One binary; heavy data lazy-fetched |
| Replacement vs augmentation | Replacement. TF-IDF gone from production paths | No signal gain from 3-way RRF once one arm is semantic |
| Scope | BGE + sqlite-vec + chunking + rerank + partitions + telemetry | Confirmed "most ambitious" |
| Model delivery | (d) Hybrid — ORT bundled, models lazy | Models change rarely, code ships often |
| Migration | (c) Dual-column + background backfill | Instant startup, eventual full coverage |
| Failure policy | Download-then-start, `--no-semantic-memory` escape hatch | Single capability mode; explicit opt-out |
| Chunking | 1 chunk ≤512 tokens, 256/32 windows above; max-sim scoring; lift cap 2048 → 8192 chars | Most rows are short; chunking only when needed |
| Process model | (b′) Dispatcher owns ORT; CLI tries-then-falls-back | Reuses UDS; avoids third daemon |
| Platform | Darwin arm64 + amd64 only | Don't expand matrix mid-rebuild |
| Tests | (b) Embedder interface + **token-Jaccard fake** | Classic mock; token-Jaccard gives real RRF signal |

### Components

```
┌─────────────────────────────────────────────────────────────┐
│                      Dispatcher Process                      │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  BGEEmbedder (ORT session, loaded at warmup)         │   │
│  │  BGEReranker (ORT session, loaded on first rerank)   │   │
│  │  Dispatcher's own Store (reads memories for rerank)  │   │
│  └──────────────────────────────────────────────────────┘   │
│  UDS listener: new EmbedRequest / RerankByIDsRequest        │
└─────────────────────────────────────────────────────────────┘
          ▲                              ▲
          │ UDS (existing protocol)      │ UDS (CLI: try, else in-proc)
          │                              │
  ┌───────┴──────┐              ┌────────┴──────────────┐
  │   Worker 1   │  ...         │  CLI (oro recall ...) │
  │ Store (sqlite│              │ Dispatcher-mode:       │
  │ cgo)         │              │   UDS → embedder       │
  └──────────────┘              │ Solo/no-dispatcher:   │
                                │   in-proc ORT load     │
                                │   OR FTS5-only         │
                                └───────────────────────┘
          │
          ▼
  ┌─────────────────────────────────────────────────────┐
  │                state.db (SQLite + sqlite-vec)        │
  │                                                      │
  │   memories (+ embedding_dense column, 384d BGE)      │
  │   memory_chunks (new)                                │
  │   memory_search_events (new)                         │
  │   vec0 virtual tables, one per project partition     │
  └─────────────────────────────────────────────────────┘
```

### Solo-CLI mode clarification (C3 fix)

Per the adversarial review, **dispatcher is not always running**. Bare `oro recall`, `oro remember`, `oro memories`, `oro forget`, and `oro work` (single-worker, no-swarm) all open state.db directly without a dispatcher.

**Decision:** Solo-CLI commands follow this order:

1. Try UDS → dispatcher embedder (fast path, ≤5ms).
2. If dispatcher unavailable and `--no-semantic-memory` OR `ORO_SEMANTIC_MEMORY=0` is set → FTS5-only retrieval. Deterministic, no model load.
3. If dispatcher unavailable and semantic memory is enabled → **documented cold-start path**: load ORT + BGE in-process. ~300ms + ~200MB RAM for the CLI lifetime. Rerank is *skipped* in solo-CLI mode (loading the ~280MB reranker for a one-shot invocation isn't worth it).

Success criteria split (below) reflect this: dispatcher-mode has tight latency SLOs; solo-CLI-cold has relaxed ones.

### Package changes

- `pkg/memory/embed.go`
  - New `Embedder` interface: `Embed(text string) []float32; Dim() int; Name() string`
  - New side interface `VocabPersister` (`ExportVocab()/ImportVocab()`) — only `TFIDFEmbedder` implements. `Store.SaveVocab`/`LoadVocab` type-assert and no-op on non-vocab embedders.
  - Existing TF-IDF logic → `TFIDFEmbedder` (implements both interfaces)
  - `BGEEmbedder` in `pkg/memory/bge_embedder.go` — wraps `onnxruntime_go` session + `daulet/tokenizers` WordPiece
  - `Reranker` interface: `Rerank(query string, docs []string) []float64`
  - `BGEReranker` in `pkg/memory/bge_reranker.go` — wraps `bge-reranker-base` ONNX
  - `VectorIndex` interface: `Upsert(id int64, vec []float32, project string) error; Search(queryVec []float32, project string, k int) ([]ANNResult, error); Delete(id int64) error`
  - `SQLiteVecIndex` impl in `pkg/memory/vec_index.go` — vec0 virtual table per partition
  - `InMemoryVecIndex` test impl
  - `Store.SetEmbedder(Embedder)` signature widens from `*Embedder` (concrete) to interface.
- `pkg/memory/models.go` (new) — `ModelPath(name string)`, `PrefetchModels(ctx)`. SHA256 digests hardcoded per model version.
- `pkg/protocol/types.go` — new message types:
  - `EmbedRequest{Text string}` / `EmbedResponse{Vec []float32; Err string}`
  - **`RerankByIDsRequest{Query string; MemoryIDs []int64; Project string}`** / `RerankByIDsResponse{Scores []float64; Err string}` — IDs only. Dispatcher loads content from its own Store. Keeps max message size ≤ a few KB regardless of rerank pool.
- `pkg/dispatcher/dispatcher.go` — dispatcher-owned `BGEEmbedder`; warmup goroutine at startup; new UDS handlers; the dispatcher's own Store is the lookup source for rerank contents.
- `cmd/oro/cmd_models.go` (new) — `oro models prefetch` / `list` / `verify`.
- `cmd/oro/db.go` — **SQLite driver swap `modernc.org/sqlite` → `mattn/go-sqlite3`** at the single `openDB` helper (line 18).
- `pkg/eventlog/query.go:61`, `pkg/codesearch/index.go:87` — **production `sql.Open("sqlite", ...)` call sites** that must migrate. Consolidated through the existing `openDB` (Bead #0).
- All `cmd_*memories*.go`, `cmd_recall.go`, `cmd_remember.go`, `cmd_forget.go` — add `--no-semantic-memory` flag wiring.
- `pkg/memory/dream.go` — **no changes** (spec v1 incorrectly claimed consolidation was clustering-based; it is LLM-action-driven).

### Schema migrations — fits the existing swallowed-ALTER pattern

oro has no migration runner. `cmd/oro/db.go:migrateStateDB` is a sequence of `_, _ = db.ExecContext(...)` calls with intentionally-ignored errors. New migrations append constants to `pkg/protocol/schema.go` (existing pattern: `MigrateAssignmentCounts`, `MigrateFileTracking`, `MigratePinnedMemories`, `MigrateKVStore`, `MigrateRejectionHistory`).

Appending four new constants — all `CREATE TABLE IF NOT EXISTS` / `ALTER TABLE ADD COLUMN` style, safe to re-run:

```sql
-- MigrateSemanticMemoryDense
ALTER TABLE memories ADD COLUMN embedding_dense BLOB;
ALTER TABLE memories ADD COLUMN content_tokens INTEGER DEFAULT 0;

-- MigrateSemanticMemoryChunks
CREATE TABLE IF NOT EXISTS memory_chunks (
    id INTEGER PRIMARY KEY,
    memory_id INTEGER NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    chunk_idx INTEGER NOT NULL,
    text TEXT NOT NULL,
    embedding BLOB NOT NULL,
    UNIQUE(memory_id, chunk_idx)
);
CREATE INDEX IF NOT EXISTS idx_memory_chunks_memory_id ON memory_chunks(memory_id);

-- MigrateSemanticMemorySearchEvents
CREATE TABLE IF NOT EXISTS memory_search_events (
    id INTEGER PRIMARY KEY,
    ts DATETIME NOT NULL DEFAULT (datetime('now')),
    project TEXT,
    query_hash TEXT,
    top_k_ids TEXT,
    top_k_scores TEXT,
    latency_ms INTEGER,
    used_rerank INTEGER DEFAULT 0,
    used_bge INTEGER DEFAULT 0,
    ann_candidates INTEGER
);
CREATE INDEX IF NOT EXISTS idx_mse_ts ON memory_search_events(ts);

-- MigrateSemanticMemoryBackfillState
INSERT OR IGNORE INTO kv_store (key, value, updated_at)
VALUES ('backfill_semantic_memory_state', 'pending', datetime('now'));

-- Embedding dim sentinel (detects future model upgrade)
INSERT OR IGNORE INTO kv_store (key, value, updated_at)
VALUES ('embedding_dense_model', 'bge-small-en-v1.5', datetime('now'));
```

`ALTER TABLE ADD COLUMN` is not safely re-runnable in stock SQLite (it errors on "duplicate column"), but `migrateStateDB`'s intentional error-swallow pattern already absorbs this. Matches existing convention.

`vec_memories_{project}` virtual tables are created at Store open via sqlite-vec extension load, not in the migration constants (extension loading is runtime-dependent).

Content cap raised 2048 → 8192 chars in `prepareInsert` (memory.go:209). `checkDuplicate`'s FTS5 MATCH on 8192-char inputs: verified-on-test (FTS5 handles it; no token-length limit triggered; smoke test in bead #4).

### Backfill — crash-safe and concurrency-safe (H2 fix)

Flow at Store open when `kv_store['backfill_semantic_memory_state'] = 'pending'`:

1. **Advisory owner lock.** CAS `kv_store['backfill_owner_pid'] = os.Getpid() + start_ts` using `INSERT OR IGNORE` followed by `SELECT` — only the first process succeeds. Other processes see an existing non-stale owner and skip spawning their own goroutine. Stale owner (process dead or owner_ts > 10min old) is stolen.
2. Spawn background goroutine.
3. Query `SELECT id, content FROM memories WHERE embedding_dense IS NULL ORDER BY created_at DESC LIMIT 100`.
4. Per row: compute BGE → chunk if >512 tokens → **`INSERT OR IGNORE INTO memory_chunks`** → **`UPDATE memories SET embedding_dense=? WHERE id=? AND embedding_dense IS NULL`** (idempotent WHERE-guard prevents dup work) → upsert sqlite-vec partition entry.
5. Rate-limit: max 50 embeds/sec.
6. Empty batch → set state to `complete`, clear `backfill_owner_pid`.
7. Process exit without completion → next launch's CAS picks up where we left off. Stale lock is stolen after 10min.

Concurrency model:
- At most one backfill worker across all oro processes on this DB (CAS-guarded).
- Regular worker inserts (non-backfill) write to `memories.embedding_dense` directly on Insert; backfill skips those via the `WHERE ... IS NULL` guard.
- No races with `dream.go` — dream operates on LLM action verbs, doesn't touch `embedding_dense`.

### Retrieval flow (post-migration)

`Store.HybridSearch(ctx, query, opts)`:

1. FTS5 top-N candidates by BM25 rank.
2. ANN path — `Embedder.Embed(query)` → `VectorIndex.Search` on project partition → top-N by HNSW cosine. Chunks score their parent via max-sim.
3. Fuse via RRF (existing `fuseRRF`).
4. If rerank enabled, reranker present (dispatcher mode), and pool size warrants it:
   - Worker/CLI sends `RerankByIDsRequest{Query, MemoryIDs: top50.IDs, Project}` over UDS.
   - Dispatcher reads those IDs from its own Store, constructs the docs slice, runs ORT reranker, returns `[]float64` scores.
   - Response size = 50 × 8 bytes ≈ 400B. Well under MaxMessageSize.
5. Re-sort by rerank score. Apply final top-k.
6. Log row to `memory_search_events`.

If `--no-semantic-memory`: skip steps 2, 4, 6. FTS5 only.

Solo-CLI cold-path: skip step 4 entirely (reranker doesn't load inline). Step 2 pays ORT cold-start cost.

### Dispatcher ORT lifecycle

- Startup: `go dispatcher.warmupEmbedder(ctx)` if `memory.semantic.enabled`.
- Workers block on `dispatcher.embedderReady` channel before sending EmbedRequest.
- Reranker lazy-loads on first `RerankByIDsRequest`.
- On dispatcher shutdown: models freed with ORT session.

### CLI fallback (b′)

`cmd/oro/cmd_recall.go` and siblings:
1. `net.Dial("unix", ~/.oro/run/dispatcher.sock)` with 100ms timeout.
2. On success: EmbedRequest/RerankByIDsRequest over UDS.
3. On ENOENT / ECONNREFUSED / timeout: `memory.NewBGEEmbedder(modelPath)` inline. Skip rerank. Accept ~300ms cold latency.

## Phased rollout — 7 beads

Epic: **oro-XXXX: semantic-memory-overhaul** (parent)

| # | Bead | Summary | Depends on |
|---|---|---|---|
| **0** | `refactor(db): consolidate sql.Open("sqlite",...) through openDB helper` | All 23 call sites route through `cmd/oro/db.go:openDB`. Includes `pkg/eventlog/query.go`, `pkg/codesearch/index.go`, all test helpers. Driver name stays `sqlite` until bead #3. Zero behavior change. | — |
| **1** | `feat(memory): Embedder + Reranker + VectorIndex interfaces; TFIDFEmbedder extraction; VocabPersister side interface` | Extract interfaces. Move TF-IDF into `TFIDFEmbedder` impl. `Store.SetEmbedder` widens to interface. `SaveVocab`/`LoadVocab` type-assert `VocabPersister`, no-op on non-vocab embedders. Zero production behavior change. | #0 |
| **2** | `feat(memory): BGEEmbedder via onnxruntime_go + oro models prefetch + dispatcher ownership + CLI fallback + --no-semantic-memory` | ORT lib bundling in installer/codesign, `daulet/tokenizers` cgo dep, dispatcher warmup goroutine, UDS `EmbedRequest` handler, CLI fallback path, config knobs. Triggers crash-safe backfill (CAS owner lock + idempotent UPDATE/INSERT OR IGNORE). Migration appended to schema constants. | #1 |
| **3** | `feat(memory): sqlite-vec HNSW + SQLite driver swap to mattn/go-sqlite3` | Single driver name change at `openDB` (possible because of #0). sqlite-vec extension load at every connection. Per-project `vec_memories_{project}` virtual tables. Migrate `vectorSearch` to HNSW. | #2 |
| **4** | `feat(memory): lift content cap to 8192 + chunking + memory_chunks + max-sim parent scoring + RerankByIDsRequest protocol` | Migration constants for `memory_chunks`. Chunking only for >512-token content. RerankByIDs protocol (dispatcher looks up docs from its own Store, keeps msg size ≤ few KB). FTS5 8192-char smoke test. | #2, #3 |
| **5** | `feat(memory): bge-reranker-base cross-encoder in dispatcher` | Second ORT session for reranker. Config-gated. Skip in solo-CLI mode. | #2, #4 |
| **6** | `feat(memory): memory_search_events telemetry + eval corpus + before/after precision script` | Telemetry table, logging hook in HybridSearch. `ad_hoc/memory_eval/` with 100 hand-labeled query→memory pairs. `compare.go` script measures precision@5/10. Retention cron trims events >30d. | #2, #3, #4 |

Critical path: 0 → 1 → 2 → 3 → 4 → 6. #5 parallel after #4.

Background backfill kicks off in bead #2. Consolidation (`dream.go`) is not changed by this spec.

## Testing

Per Q10 decision + M1 fix: **token-Jaccard fake**, not hash-fake.

- **Fake embedder** in test helpers:
  ```go
  // fakeEmbedder produces deterministic vectors where cosine(a,b) ≈ Jaccard(tokens(a), tokens(b))
  // via a fixed hash-trick projection. "cat"/"cats" share tokens → high cosine; unrelated strings → low.
  ```
  Gives RRF fusion meaningful semantic signal without ONNX.
- **Unit tests** use the fake. Milliseconds; no ONNX dep.
- **Integration tests** (`//go:build integration`): download real BGE-small on CI first run (cached). Exercise HybridSearch → rerank path.
- **E2E smoke** (`//go:build semantic_integration`): dispatcher spawns → UDS handshake → worker EmbedRequest round-trip.
- **Backfill test**: 1000-row seed, fake embedder, invoke backfill goroutine, assert convergence + idempotence on a forced re-run.
- **Concurrent-backfill test**: two goroutines both call `Store.MaybeStartBackfill()`; assert only one actually runs; assert correctness under stale-owner steal.
- **Tokenizer golden test**: `daulet/tokenizers` output vs canonical Python `transformers.AutoTokenizer.from_pretrained("BAAI/bge-small-en-v1.5")` for 20 test strings. Committed as `testdata/tokenizer_golden.json`.
- **RerankByIDs size test**: simulate 50 MemoryIDs + query; assert marshaled size < 16 KB (far below MaxMessageSize).
- **FTS5 8192-char test**: insert a memory with max-size content, run search, assert no MATCH error.

## Risks

1. **ORT cgo dependency** breaks pure-Go build. Documented in release notes. Users opt out via `--no-semantic-memory`.
2. **sqlite-vec extension codesign** on macOS hardened runtime. Bundle `.dylib` under `~/.oro/lib/`, ad-hoc codesign in `install.sh` (already done for `oro` binary, line 250).
3. **Driver swap scope (C1).** 23 `sql.Open("sqlite", ...)` call sites including production in `pkg/eventlog` and `pkg/codesearch`. Bead #0 consolidates first; bead #3 then does a one-line driver name swap in `openDB`. **Full `go test ./...` is the acceptance gate for bead #3.**
4. **Backfill crash/concurrency (H2).** CAS owner lock in `kv_store` + idempotent `WHERE ... IS NULL` UPDATE + `INSERT OR IGNORE` on chunks. Stale owner stolen after 10 min. Explicit concurrent test (bead #2 AC).
5. **BGE dim mismatch** on future model upgrade. `kv_store['embedding_dense_model']` sentinel; migration detects mismatch and re-embeds.
6. **Dispatcher OOM = all workers block.** 500ms timeout per EmbedRequest; worker falls back to FTS5-only on timeout (same code path as `--no-semantic-memory`).
7. **MaxMessageSize (C2).** RerankByIDs sends IDs (400B at top-50), not content. No change to `MaxMessageSize = 1MB` needed.
8. **Tokenizer cgo brittleness.** `daulet/tokenizers` wraps a Rust lib, ships a `.dylib`. Same bundle + ad-hoc-codesign treatment as ORT.
9. **Total install payload.** BGE-small ~133MB + reranker ~280MB + ORT ~12MB + tokenizers ~15MB + sqlite-vec ~2MB ≈ 440MB. Cold install ~60MB (binaries + libs). First semantic use triggers model pull. `oro models prefetch` for airgapped/CI.
10. **Solo-CLI cold-start latency (C3).** Documented expected behavior. Users who hit this pattern and care about latency run the swarm (which keeps a dispatcher up), use `--no-semantic-memory`, or accept ~300ms first hit.
11. **Success-criterion ground truth (M2).** Bead #6 builds the eval corpus as part of its own AC. 100 hand-labeled pairs committed to `ad_hoc/memory_eval/corpus.jsonl`. Before/after comparison runs against the same corpus.

## Success criteria

**Retrieval quality** (bead #6 delivers the harness):
- Against the 100-query hand-labeled corpus: top-10 precision improves by ≥30% over TF-IDF+linear-cosine baseline. (Lowered from the original ≥40% — conservative vs no prior benchmark data.)
- Measured via `ad_hoc/memory_eval/compare.go` running the same queries before (TF-IDF, captured from existing prod) and after (BGE+HNSW+rerank).

**Latency (dispatcher mode, dispatcher warm):**
- p50 HybridSearch-with-rerank < 100ms
- p99 < 500ms

**Latency (solo-CLI cold path):**
- First `oro recall` after process start: ≤1.5s (model load + embed + FTS5 + ANN)
- Subsequent in same process: ≤150ms

**Install UX:**
- `curl | bash` install → `oro --version` < 100ms
- First semantic-memory operation → `oro models prefetch` blocks visibly, completes ≤5 min on a fast connection
- `oro --version` independent of model presence

**Backfill:**
- 10k-row database → `backfill_semantic_memory_state = 'complete'` within 5 min of first launch
- Interrupted backfill resumes correctly on next launch (tested in bead #2 AC)

**Backward compat:**
- Every existing `cmd_*` test passes after bead #1 refactor
- Every test passes with `--no-semantic-memory` set
- `pkg/eventlog`, `pkg/codesearch` tests pass after bead #3 driver swap

**No regression:**
- `oro` binary < 80MB; ORT/tokenizer dylibs live in `~/.oro/lib/`
- Existing memory rows readable during backfill window (they just score via FTS5 + old TF-IDF vectors during that window; HybridSearch degrades gracefully)

## Integration points per bead (adversarial-review H1 / C1 plug)

| File | Bead | Why it's in scope |
|---|---|---|
| `cmd/oro/db.go` | #0, #3 | openDB helper — driver name swap |
| `pkg/eventlog/query.go:61` | #0 | Production `sql.Open("sqlite", ...)` — route through openDB |
| `pkg/codesearch/index.go:87` | #0 | Production `sql.Open("sqlite", ...)` — route through openDB |
| All `*_test.go` with `sql.Open("sqlite", ...)` | #0 | Test helpers — route through openDB or accept dual-driver |
| `pkg/memory/memory.go:25,36,62,101` | #1 | `*Embedder` → `Embedder` interface; vocab via type-assert |
| `pkg/memory/embed_test.go` | #1 | All tests reference concrete `*Embedder` |
| `pkg/dispatcher/dispatcher.go:563-564` | #1, #2 | `memStore.SetEmbedder(memory.NewEmbedder())` — widens; later dispatcher owns BGE |
| `cmd/oro/cmd_worker.go:92-97` | #1, #2 | Worker Store construction; `LoadVocab` conditional on TF-IDF |
| `cmd/oro/cmd_start.go:604` | #2 | Another `openStateDB` call site (warmup path) |
| `pkg/protocol/message.go:13` | — | `MaxMessageSize` unchanged (C2 avoided by IDs-only rerank) |
| `pkg/protocol/schema.go` | #2, #4, #6 | Append migration constants |
| `scripts/install.sh:240-260` | #2 | Bundle ORT/tokenizer/sqlite-vec dylibs, ad-hoc-codesign each |
| `Makefile` | #2 | Download ORT artifact in build; include in release tarball |

## Open questions (implementation, not blocking)

- ORT version (1.18 vs 1.19). Pin at spec freeze.
- `bge-reranker-base` vs `bge-reranker-large` (~280MB vs ~1.3GB). Start with base.
- Vendored vs downloaded tokenizer.json. Vendoring is safer; ~1MB overhead.
- Whether to expose `oro memory eval` subcommand (likely yes, as part of bead #6's deliverable).
