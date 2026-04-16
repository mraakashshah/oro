# Semantic Memory Overhaul

**Date:** 2026-04-16
**Goal:** Replace oro's TF-IDF memory retrieval with BGE-small embeddings, sqlite-vec HNSW ANN, chunked embeddings, cross-encoder reranking, per-project partitions, and search telemetry. Cherry-picked from [memvid evaluation](../../archive/yap/reference/memvid/evaluation.md).

## Problem

`pkg/memory/embed.go` ships a bag-of-words TF-IDF embedder and `pkg/memory/memory.go:484-565` linear-cosine-scans up to 1000 rows per vector query. Fine at today's scale, but:

- TF-IDF captures *lexical* similarity, not *semantic*. Paraphrased queries miss — "how do I retry a failed bead" doesn't rank a memory about "worker respawn after crash."
- Linear scan caps recall at 1000 rows. A heavy user with 20k memories silently loses precision.
- Content cap of 2048 chars (memory.go:210) forces long `summary` / `decision` entries to be truncated or split upstream.

The memvid evaluation landed on three high-leverage cherry-picks (BGE-small, sqlite-vec HNSW, chunking). This spec extends with cross-encoder reranking, per-project HNSW partitions, and query telemetry — the scope the user confirmed as "most ambitious."

## Non-goals

- HyDE-style query expansion. Depends on worker loop integration; separate spec.
- LLM-driven consolidation beyond switching clustering from TF-IDF to BGE. The `dream.go` synthesis-via-LLM flow is a different spec.
- Per-query embedding cache. Trivial bolt-on, file as a follow-up bead.
- Linux support. `install.sh` is Darwin-only today; platform matrix expansion is a parallel spec.
- Replacing FTS5. BM25 via FTS5 stays — it's the text arm of the hybrid fusion.

## Architecture

### Decisions locked in brainstorm

| Q | Decision | Rationale |
|---|---|---|
| Distribution model | Cgo; bundled ORT lib + lazy model download | One binary; heavy data lazy-fetched |
| Replacement vs augmentation | Replacement. TF-IDF gone from production paths | No signal gain from 3-way RRF once one arm is semantic |
| Scope | BGE + sqlite-vec + chunking + rerank + partitions + telemetry | User confirmed "most ambitious" scope |
| Model delivery | (d) Hybrid — ORT bundled, models lazy | Models change rarely, code ships often |
| Migration | (c) Dual-column + background backfill | Instant startup, eventual full coverage |
| Failure policy | Download-then-start, `--no-semantic-memory` escape hatch | Single capability mode; explicit opt-out |
| Chunking | 1 chunk ≤512 tokens, 256/32 windows above; max-sim scoring; lift cap 2048 → 8192 chars | Most rows are short; chunking only when needed |
| Process model | (b′) Dispatcher owns ORT; CLI tries-then-falls-back | Reuses UDS infra; avoids third daemon |
| Platform | Darwin arm64 + amd64 only | Don't expand matrix mid-rebuild |
| Tests | (b) Embedder interface + deterministic fake | Classic mock pattern; real model only in integration smoke |

### Components

```
┌─────────────────────────────────────────────────────────────┐
│                      Dispatcher Process                      │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  BGEEmbedder (ORT session, loaded once)              │   │
│  │  BGEReranker (ORT session, loaded on first rerank)   │   │
│  └──────────────────────────────────────────────────────┘   │
│  UDS listener: new EmbedRequest / RerankRequest protocol    │
└─────────────────────────────────────────────────────────────┘
          ▲                              ▲
          │ UDS (existing protocol)      │ UDS
          │                              │
  ┌───────┴──────┐              ┌────────┴──────────────┐
  │   Worker 1   │  ...         │  CLI (oro recall ...) │
  │ Store (sqlite│              │  tries UDS, falls back│
  │ via cgo)     │              │  to in-proc ORT       │
  └──────────────┘              └───────────────────────┘
          │
          ▼
  ┌─────────────────────────────────────────────────────┐
  │                state.db (SQLite + sqlite-vec)        │
  │                                                      │
  │   memories (existing + embedding_dense column)       │
  │   memory_chunks (new)                                │
  │   memory_search_events (new)                         │
  │   vec0 virtual tables (one per project partition)    │
  └─────────────────────────────────────────────────────┘
```

### Package changes

- `pkg/memory/embed.go`
  - New `Embedder` interface: `Embed(text string) []float32; Dim() int; Name() string`
  - Existing TF-IDF logic moves into `TFIDFEmbedder` impl (kept for tests / `--no-semantic-memory` mode)
  - New `BGEEmbedder` in `pkg/memory/bge_embedder.go` — wraps `onnxruntime_go` session + `daulet/tokenizers` WordPiece tokenizer
  - New `Reranker` interface: `Rerank(query string, docs []string) []float64`
  - New `BGEReranker` in `pkg/memory/bge_reranker.go` — wraps `bge-reranker-base` ONNX session
  - New `VectorIndex` interface: `Upsert(id int64, vec []float32, project string) error; Search(queryVec []float32, project string, k int) ([]ANNResult, error); Delete(id int64) error`
  - New `SQLiteVecIndex` impl in `pkg/memory/vec_index.go` — `vec0` virtual table per project partition
  - New `InMemoryVecIndex` impl for tests
  - `pkg/memory/memory.go` Store gains `reranker Reranker` and `vecIndex VectorIndex` fields, set via `SetReranker` / `SetVectorIndex`

- `pkg/memory/models.go` (new) — model download / SHA256 verification / path resolution
  - `ModelPath(name string) (string, error)` — returns path under `~/.oro/models/<name>/`
  - `PrefetchModels(ctx context.Context) error` — downloads BGE-small + reranker if missing, with progress output to stderr
  - SHA256 digests hardcoded per model version, bumped in code when we upgrade model

- `pkg/protocol/types.go` — new IPC messages:
  - `EmbedRequest{Text string}` / `EmbedResponse{Vec []float32; Err string}`
  - `RerankRequest{Query string; Docs []string}` / `RerankResponse{Scores []float64; Err string}`

- `pkg/dispatcher/dispatcher.go` — dispatcher-owned `BGEEmbedder` + `BGEReranker`, lazy-init goroutine on startup, new UDS handlers route EmbedRequest/RerankRequest to in-proc models

- `cmd/oro/cmd_models.go` (new) — `oro models prefetch` / `oro models list` / `oro models verify` subcommands

- `cmd/oro/store.go` — SQLite driver swap `modernc.org/sqlite` → `mattn/go-sqlite3`; sqlite-vec extension load on every connection

- All `cmd_*memories*.go`, `cmd_recall.go`, `cmd_remember.go`, `cmd_forget.go` — add `--no-semantic-memory` flag wiring through to store construction

- `pkg/memory/dream.go` — consolidation clustering switched to BGE when backfill-complete flag is set in `kv_store`

### Schema migrations

Migration `014_semantic_memory`:

```sql
-- 1. Add dense-vector column (384-dim BGE). Old `embedding` column kept during backfill.
ALTER TABLE memories ADD COLUMN embedding_dense BLOB;
ALTER TABLE memories ADD COLUMN content_tokens INTEGER DEFAULT 0;

-- 2. Chunk child table.
CREATE TABLE memory_chunks (
    id INTEGER PRIMARY KEY,
    memory_id INTEGER NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    chunk_idx INTEGER NOT NULL,
    text TEXT NOT NULL,
    embedding BLOB NOT NULL,
    UNIQUE(memory_id, chunk_idx)
);
CREATE INDEX idx_memory_chunks_memory_id ON memory_chunks(memory_id);

-- 3. Search telemetry.
CREATE TABLE memory_search_events (
    id INTEGER PRIMARY KEY,
    ts DATETIME NOT NULL DEFAULT (datetime('now')),
    project TEXT,
    query_hash TEXT,
    top_k_ids TEXT,       -- JSON array
    top_k_scores TEXT,    -- JSON array
    latency_ms INTEGER,
    used_rerank INTEGER DEFAULT 0,
    used_bge INTEGER DEFAULT 0,
    ann_candidates INTEGER
);
CREATE INDEX idx_mse_ts ON memory_search_events(ts);

-- 4. Backfill state marker.
INSERT OR REPLACE INTO kv_store (key, value, updated_at)
VALUES ('backfill_semantic_memory_state', 'pending', datetime('now'));

-- 5. sqlite-vec virtual table (created at store open; can't be in pure SQL migration
--    since it depends on extension being loaded).
```

At store-open time, for each distinct project in `memories`, ensure a `vec_{project}` virtual table exists:

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS vec_memories_{project}
USING vec0(embedding FLOAT[384]);
```

Content cap raised from 2048 → 8192 chars:
```go
// pkg/memory/memory.go:209
if len(params.Content) > 8192 {
    return preparedFields{}, fmt.Errorf("%s: content too long (max 8192 chars, got %d)", ...
}
```

### Backfill flow

Triggered at store-open when `kv_store['backfill_semantic_memory_state'] = 'pending'`:

1. Spawn background goroutine (`pkg/memory/backfill.go`). Runs under a semaphore so at most one backfill is active.
2. Query `SELECT id, content FROM memories WHERE embedding_dense IS NULL ORDER BY created_at DESC LIMIT 100`.
3. For each batch: compute BGE embedding, chunk if >512 tokens, upsert into `memory_chunks` if chunked, update `embedding_dense` on `memories`, upsert into sqlite-vec vec0 partition for the row's project.
4. Rate-limit: max 50 embeds/sec to avoid starving worker queries.
5. On batch complete, sleep 100ms then loop.
6. When `WHERE embedding_dense IS NULL` returns 0 rows, set `kv_store['backfill_semantic_memory_state'] = 'complete'` and exit.
7. `dream.go` consolidation polls this flag; runs only when `complete`.

If backfill is interrupted mid-way, the next oro launch resumes from where it left off (idempotent `LIMIT 100` loop on remaining `NULL` rows).

### Retrieval flow (post-migration)

`Store.HybridSearch(ctx, query, opts)`:

1. FTS5 path (existing) — top-N candidates by BM25 rank.
2. ANN path — embed query via `Embedder.Embed`, `VectorIndex.Search` on project partition, top-N by HNSW cosine. For memories with chunks, each chunk is a separate vec entry; parent score = max-sim.
3. Fuse via RRF (existing `fuseRRF` in memory.go:570).
4. If `memory.semantic.rerank == true` and reranker loaded: take top-50 fused results, call `Reranker.Rerank(query, contents)`, re-sort by rerank score. Apply `ann_top_k` / `final_top_k` from config.
5. Emit a row to `memory_search_events`.
6. Return final top-k.

If `--no-semantic-memory` / `ORO_SEMANTIC_MEMORY=0`:
- Skip ANN path entirely. Run FTS5 only. No rerank. No telemetry event.
- Backfill still runs in background if applicable (it doesn't block reads).

### Dispatcher ORT lifecycle

At dispatcher startup:
1. Spawn `go dispatcher.warmupEmbedder(ctx)` which calls `memory.NewBGEEmbedder(modelPath)` and `memory.NewBGEReranker(modelPath)` if `memory.semantic.enabled == true`.
2. Workers joining after ~200ms don't see cold latency. A worker that embeds before warmup completes waits on `dispatcher.embedderReady` channel.

UDS protocol extension:
```go
// new message types in pkg/protocol
type EmbedRequest  struct { Text string }
type EmbedResponse struct { Vec []float32; Err string }
type RerankRequest struct { Query string; Docs []string }
type RerankResponse struct { Scores []float64; Err string }
```

Workers hold an `embedderClient` that writes `EmbedRequest` to dispatcher UDS, blocks on `EmbedResponse`. On dispatcher unavailability (shouldn't happen since worker is spawned by dispatcher), falls through to in-proc load.

### CLI fallback (b′)

`cmd/oro/cmd_recall.go` and siblings:
1. Try to dial `~/.oro/run/dispatcher.sock` (existing UDS).
2. On success: send EmbedRequest to dispatcher, get vector back.
3. On ENOENT or ECONNREFUSED: load ORT + BGE model in-process (one-shot, pays ~300ms init + ~200MB RAM for the CLI lifetime).

## Phased rollout — 6 beads

Epic: **oro-XXXX: semantic-memory-overhaul** (parent)

| # | Bead | Summary | Depends on | Ships standalone? |
|---|---|---|---|---|
| 1 | `feat(memory): Embedder/Reranker/VectorIndex interfaces + TFIDFEmbedder extraction` | Pure refactor. No behavior change. Unlocks testability. | — | ✅ Yes |
| 2 | `feat(memory): BGEEmbedder via onnxruntime_go + oro models prefetch + dispatcher ownership + CLI fallback` | Includes ORT bundling, downloader, IPC, `oro.toml` knobs, `--no-semantic-memory`. Triggers background backfill. | #1 | ✅ Yes — opt-in, still runs without |
| 3 | `feat(memory): sqlite-vec HNSW index + driver swap` | Swap `modernc.org/sqlite` → `mattn/go-sqlite3`. Introduce per-project vec0 virtual tables. Migrate `vectorSearch`. | #2 | ⚠️ Requires driver swap — invasive |
| 4 | `feat(memory): lift content cap to 8192 + chunking + memory_chunks table + max-sim parent scoring` | Migration `014`. Chunks only for >512-token content. | #2, #3 | ✅ Yes |
| 5 | `feat(memory): bge-reranker-base cross-encoder` | `bge-reranker-base` model, dispatcher-owned, config-gated. | #2 | ✅ Yes — behind config flag |
| 6 | `feat(memory): memory_search_events telemetry + dream.go consolidation-switch-on-backfill-complete` | Table, logging, `dream.go` clustering switches to BGE. | #2, #4 | ✅ Yes |

Critical path: 1 → 2 → 3 → 4 → 6. Bead 5 parallel after 2.

Background backfill (kicked off in bead 2) blocks bead 6's consolidation switchover but doesn't block bead 3/4/5 development.

## Testing

Per Q10 decision: Embedder interface + deterministic fake.

- **Unit tests** (`pkg/memory/*_test.go`): inject `fakeEmbedder{dim: 384, seed: hash(text)}` — deterministic pseudo-vectors, no ONNX, millisecond latency.
- **Integration tests** (`//go:build integration`): download real BGE-small on CI first run (cached between runs), exercise full HybridSearch → rerank path with known ground-truth pairs.
- **E2E smoke** (`//go:build semantic_integration`): dispatcher spawns, UDS handshake, worker calls EmbedRequest, round-trip asserts vectors.
- **Backfill test** (unit): seed DB with 1000 rows lacking `embedding_dense`, invoke backfill loop with `fakeEmbedder`, assert convergence to `backfill_semantic_memory_state = 'complete'`.
- **Tokenizer test**: golden-file compare — daulet/tokenizers output vs canonical Python `transformers.AutoTokenizer.from_pretrained("BAAI/bge-small-en-v1.5")` output for a corpus of 20 test strings. Catches tokenizer version drift.

## Risks

1. **ORT cgo dependency breaks pure-Go build story.** Mitigation: documented in release notes. Binaries still statically linkable against ORT via `CGO_ENABLED=1`. Codesign step already exists. `--no-semantic-memory` preserves old behavior for users who care.

2. **sqlite-vec extension loading may fail on macOS codesign / hardened runtime.** Mitigation: ship `.dylib` alongside `oro` binary in `~/.oro/lib/`, `install.sh` + codesign step applies ad-hoc signing to the extension as well. Verify at store-open with a no-op `SELECT vec_version()` and fall back with warning if unavailable.

3. **Driver swap from `modernc.org/sqlite` to `mattn/go-sqlite3`.** Subtle DSN differences (`file:path?_journal=WAL` vs `file:path?_journal_mode=WAL`). Every test that opens a DB needs scrutiny. Mitigation: single `cmd/oro/store.go:openDB()` helper is the only call site; run full test suite after swap; run integration/e2e tests.

4. **Backfill runs forever on massive DBs.** Mitigation: rate limit at 50/sec + batch 100. 20k rows = ~7 minutes in background. Write progress to `kv_store` every 100 rows.

5. **BGE dim mismatch between stored vectors and reloaded model.** If a future model upgrade changes dim (e.g. bge-base 768d), `embedding_dense` column breaks. Mitigation: `kv_store['embedding_dense_model']` stores model name + dim; migration re-embeds from scratch on mismatch.

6. **Dispatcher-owned embedder becomes single point of failure.** If dispatcher OOMs, all workers block on embedding. Mitigation: bounded queue on UDS; timeout per EmbedRequest (500ms); worker fallback to FTS5-only on timeout (same code path as `--no-semantic-memory`).

7. **Tokenizer cgo brittleness on macOS code sign.** `daulet/tokenizers` wraps a Rust lib. Shipping another Rust-produced dylib. Mitigation: bundle .dylib, ad-hoc codesign in install.sh (same treatment as ORT).

8. **Total install payload.** BGE-small ~133MB + reranker ~280MB + ORT ~12MB + tokenizers ~15MB + sqlite-vec ~2MB = ~440MB. Mitigation: models lazy, so cold install is ~60MB (binaries + libs). First use of semantic memory triggers model pull. `oro models prefetch` available for airgapped/CI.

## Success criteria

- **Retrieval quality:** on a benchmark of 100 historical `oro recall` queries with ground-truth relevant memories, top-10 recall improves by ≥40% over TF-IDF + linear cosine baseline. Measured once before and after via a scratch script in `ad_hoc/`.
- **Latency:** p50 HybridSearch (with rerank) < 100ms; p99 < 500ms.
- **Install UX:** fresh install → first `oro work <bead>` session → < 30s from `oro models prefetch` completion to first embedding. `oro --version` returns in < 50ms.
- **Backfill:** 10k-row database converges to `backfill_semantic_memory_state = 'complete'` within 5 minutes of first launch.
- **Backward compat:** every existing `cmd_*` test passes after phase 1 refactor; every test passes with `--no-semantic-memory` set.
- **No regression:** `oro` binary size stays < 80MB (ORT libs live separately under `~/.oro/lib/`, not baked in).

## Open questions (to be answered in implementation, not blocking)

- Exact ORT version (1.18 vs 1.19). Pinned at spec freeze; revisit if 1.19 brings material perf gains.
- Whether `bge-reranker-base` or `bge-reranker-large` for rerank — start with base (~280MB), evaluate large post-launch.
- Whether to pin the tokenizer version against a vendored HF tokenizers.json or let daulet download at runtime. Vendoring is safer; adds ~1MB to binary.
- Whether to expose `oro memory eval` subcommand for users to measure retrieval quality on their own historical queries. Nice-to-have; not blocking launch.
