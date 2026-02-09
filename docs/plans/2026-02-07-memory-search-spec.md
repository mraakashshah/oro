# Oro Memory Search/Retrieval Algorithm Spec

**Date:** 2026-02-07
**Bead:** oro-seg
**Status:** Decided
**Depends on:** `2026-02-07-memory-system-spec.md` (Layer 3: Project Memory schema)

## Problem

The memory system spec (oro-ef8) defines *what* gets stored and *where*. This spec defines *how memories are retrieved*: the scoring algorithm, ranking pipeline, deduplication strategy, and injection budget. Without a well-defined retrieval algorithm, memories are either noise (low precision) or invisible (low recall).

## Goal

Design a retrieval pipeline that:
1. Returns the most relevant memories for a given query context (bead description, worker task)
2. Balances lexical precision (exact term matches) with recency and confidence
3. Starts simple (FTS5 BM25 day-one) with a clear upgrade path to hybrid RRF (FTS5 + embeddings)
4. Respects a strict token budget so injected memories don't crowd out task context

## Prior Art Summary

### BCR: Three-Factor Scoring

**Source:** `references/bcr/src/bcr/recall.py`

BCR scores each memory as:

```
final_score = cosine_similarity(query_embedding, memory_embedding)
            * confidence
            * decay_factor
```

Where `decay_factor = 0.5 ^ (age_days / 30)` (30-day half-life, exponential decay relative to newest memory).

**Strengths:** Simple multiplicative model. Decay prevents stale memories from dominating. Confidence weighting lets extraction quality influence ranking.

**Weaknesses:** Pure vector search -- no lexical component. Requires embeddings for every query (fastembed ONNX crashed on arm64). Never wired to CLI or prompt injection, so never battle-tested.

### CC-v3: Hybrid RRF

**Source:** `yap/reference/Continuous-Claude-v3/opc/scripts/core/recall_learnings.py`

CC-v3 offers three search modes:

1. **SQLite FTS5 (BM25):** `bm25(archival_fts)` with OR-joined query words. BM25 returns negative scores (lower = better), normalized to 0.0-1.0 via `min(1.0, max(0.0, -raw_rank / 25.0))`.

2. **PostgreSQL vector search:** Cosine distance via pgvector with optional recency boost: `(1 - recency_weight) * similarity + recency_weight * recency`, where recency is `max(0, 1.0 - age_seconds / (30 * 86400))`.

3. **Hybrid RRF:** Combines FTS + vector via Reciprocal Rank Fusion:
   ```
   rrf_score = 1/(k + fts_rank) + 1/(k + vec_rank)
   ```
   Where `k = 60` (the standard constant from Cormack et al. 2009).

**Strengths:** RRF elegantly fuses lexical and semantic rankings without score normalization. FTS5 standalone mode works without any ML dependencies. The `k=60` constant is battle-tested across information retrieval literature.

**Weaknesses:** PostgreSQL + pgvector is heavy infrastructure. RRF requires both rankers to produce results -- documents only in one index get a partial score (one term is 0), which may under-rank them.

### Literature: RRF k-Constant

The `k=60` constant is the standard default across OpenSearch, Azure AI Search, and academic literature. Lower values (20-40) amplify top-ranked results; higher values (80-100) flatten the contribution curve. Empirically, `k=60` is robust across diverse datasets and requires no tuning.

## Algorithm Design

### Day-One: FTS5 BM25 Standalone

No embeddings, no ML dependencies, no external services. Pure SQLite FTS5.

#### Query Pipeline

```
Input: query string, optional type filter, optional tag filter
  |
  v
[1. Tokenize] -- split query, OR-join words for broader matching
  |
  v
[2. FTS5 MATCH] -- BM25 ranked search against memories_fts
  |
  v
[3. Decay Score] -- multiply BM25 score by time decay factor
  |
  v
[4. Confidence Weight] -- multiply by stored confidence value
  |
  v
[5. Dedup Check] -- skip if near-duplicate of higher-ranked result
  |
  v
[6. Token Budget] -- accumulate token cost, stop when budget exhausted
  |
  v
Output: ranked list of memories (max N, within token budget)
```

#### Scoring Formula (Day-One)

```
final_score = bm25_normalized * confidence * decay
```

Where:
- `bm25_normalized = min(1.0, max(0.0, -bm25_raw / 25.0))` (BM25 returns negative scores in SQLite FTS5; dividing by 25.0 maps typical scores to 0.0-1.0)
- `confidence` = stored confidence value (0.0-1.0, default 0.8)
- `decay = 0.5 ^ (age_days / HALF_LIFE_DAYS)`

#### SQL Implementation

```sql
SELECT
    m.id,
    m.content,
    m.type,
    m.tags,
    m.confidence,
    m.created_at,
    bm25(memories_fts) as bm25_raw,
    -- Normalized BM25 score
    MIN(1.0, MAX(0.0, -bm25(memories_fts) / 25.0)) as bm25_norm,
    -- Time decay
    POWER(0.5, (julianday('now') - julianday(m.created_at)) / :half_life) as decay,
    -- Final composite score
    MIN(1.0, MAX(0.0, -bm25(memories_fts) / 25.0))
        * m.confidence
        * POWER(0.5, (julianday('now') - julianday(m.created_at)) / :half_life)
    as final_score
FROM memories m
JOIN memories_fts f ON m.id = f.rowid
WHERE memories_fts MATCH :query
    AND (:type IS NULL OR m.type = :type)
ORDER BY final_score DESC
LIMIT :candidate_limit;
```

Note: `POWER()` is not built into SQLite. The Go binary will either register a custom function or compute decay in application code after fetching FTS5 results. Application-side scoring is preferred for testability.

### Future: Hybrid RRF (FTS5 + Embeddings)

When the `embedding` column is populated (oro-ldf epic), upgrade to hybrid search:

```
rrf_score = 1/(k + fts_rank) + 1/(k + vec_rank)
final_score = rrf_score * confidence * decay
```

The RRF score replaces the raw BM25 score. Confidence and decay remain multiplicative factors.

For memories that appear in only one index (FTS5 match but no embedding match, or vice versa), the missing rank term contributes 0, so the score is `1/(k + rank)` from the single index. This is acceptable -- a document highly ranked in one system should still surface.

## Design Decisions

### 1. RRF k-Constant: 60

| Option | Trade-off |
|--------|-----------|
| k=20 | Top results dominate heavily. Good if FTS5/vector top-1 is almost always correct. Risky if either ranker has noise at top. |
| **k=60** | **Standard default. Smooth contribution curve. Robust across datasets. No tuning needed.** |
| k=100 | Very flat curve. Reduces differentiation between rank 1 and rank 10. Useful only with many rankers. |

**Decision:** k=60. It is the literature standard, CC-v3 uses it, and there is no evidence our domain needs deviation.

### 2. Decay Function: Exponential, 30-Day Half-Life

| Option | Trade-off |
|--------|-----------|
| No decay | Old memories compete equally. Stale gotchas about deleted code clutter results. |
| Linear decay (zero at 90 days) | Hard cutoff is arbitrary. Memories at day 89 suddenly disappear. |
| **Exponential, 30-day half-life** | **Smooth. A 30-day-old memory scores 0.5x, 60-day scores 0.25x, 90-day scores 0.125x. Old but high-BM25 memories can still surface.** |
| Exponential, 14-day half-life | Too aggressive. Architectural decisions from 2 weeks ago drop to 0.5x. |
| Exponential, 60-day half-life | Too gentle. Stale memories persist too long. |

**Decision:** 30-day half-life, matching BCR. Architectural decisions and preferences should be stored with higher confidence (0.9-1.0) which compensates for decay.

### 3. Deduplication: FTS5 Overlap at Write Time

| Option | Trade-off |
|--------|-----------|
| No dedup | Table grows unbounded with near-identical learnings from repeated sessions. |
| Exact content match | Only catches identical strings. "SQLite WAL is important" and "SQLite WAL mode matters" both survive. |
| **FTS5 overlap check at write time** | **Before INSERT, query FTS5 for the new content. If top result has BM25 score above threshold, skip or merge. Simple, no embeddings needed.** |
| Cosine similarity threshold (0.85) | Requires embeddings. CC-v3 uses this but we don't have embeddings day-one. Future upgrade. |

**Decision:** FTS5 overlap check at write time (day-one). Before inserting a new memory, run its content as an FTS5 query. If the top result's normalized BM25 score exceeds **0.7**, treat as duplicate:
- If existing memory has lower confidence, update confidence to max of both
- If same confidence, skip the insert
- Log the dedup decision for debugging

When embeddings are available (future), add cosine similarity threshold of **0.85** as a second dedup check. This catches semantic duplicates that FTS5 misses (e.g., "SQLite WAL mode" vs "write-ahead logging in SQLite").

### 4. Max Memories Injected: 5 (with token cap as true limit)

| Option | Trade-off |
|--------|-----------|
| 1-2 memories | Minimal noise but may miss critical context. |
| 3 memories | BCR's recommended cap. Conservative. |
| **5 memories** | **Matches CC-v3's default `k=5`. Token budget is the real limiter, not count.** |
| 10 memories | Too many. Most will be marginal relevance. Wastes tokens. |

**Decision:** Max 5 memories, but token budget is the binding constraint (see below). If 3 memories exhaust the token budget, stop at 3.

### 5. Token Budget: 500 Tokens Per Injection

| Option | Trade-off |
|--------|-----------|
| 200 tokens | Memory system spec's original suggestion. Very tight -- ~2 short memories. May truncate useful context. |
| **500 tokens** | **~4-5 short memories or 2-3 detailed ones. ~0.5% of a 100K context window. Negligible cost, meaningful signal.** |
| 1000 tokens | Generous but risks noise. If 5 memories average 200 tokens each, 1000 fills up with marginal results. |
| 2000 tokens | Too much. Memory injection competes with bead description, handoff context, and code. |

**Decision:** 500 tokens. Memories are injected as a `## Relevant Memories` section in the worker prompt. Each memory is rendered as:

```
- [type] content (confidence: 0.8, age: 3d)
```

Token counting: use a simple heuristic of `len(content) / 4` (average ~4 chars per token for English). Iterate through ranked results, accumulate token cost, stop when budget is exhausted or max count (5) is reached.

### 6. Day-One vs Full Hybrid Architecture

| Option | Trade-off |
|--------|-----------|
| **FTS5-only day-one** | **Zero ML dependencies. Ships immediately. Covers exact and near-exact term matches. Misses pure semantic matches ("database timeout" vs "SQLite lock contention").** |
| Full hybrid day-one | Requires embedding model selection, ONNX runtime or API calls, additional latency per query. Blocks shipping on ML infrastructure. |

**Decision:** FTS5-only for day-one. The `embedding BLOB` column exists in the schema (reserved in memory system spec). When populated later (oro-ldf epic), the retrieval pipeline upgrades to hybrid RRF with no schema migration. The scoring formula already accommodates both modes.

## Data Flow

### Day-One: Write Path

```
Worker emits [MEMORY] marker
  |
  v
Go binary parses marker -> (content, type, tags, confidence)
  |
  v
Dedup check: FTS5 query content against memories_fts
  |
  v
If top BM25 > 0.7 threshold -> skip/merge
  |
  v
INSERT INTO memories + trigger memories_fts sync
```

### Day-One: Read Path (Prompt Injection)

```
Worker Go binary prepares prompt for bead
  |
  v
Extract query context: bead title + description + tags
  |
  v
FTS5 MATCH query (OR-joined significant terms)
  |
  v
Application-side scoring: bm25_norm * confidence * decay
  |
  v
Sort by final_score DESC
  |
  v
Iterate top results, accumulate tokens, stop at 500 tokens or 5 memories
  |
  v
Render as "## Relevant Memories" section in worker prompt
```

### Future: Read Path (Hybrid RRF)

```
Worker Go binary prepares prompt for bead
  |
  v
Extract query context: bead title + description + tags
  |
  v
[Parallel]
  FTS5 MATCH -> ranked list (fts_rank)
  Embedding cosine distance -> ranked list (vec_rank)
  |
  v
RRF fusion: score = 1/(60 + fts_rank) + 1/(60 + vec_rank)
  |
  v
Multiply by confidence * decay
  |
  v
Sort, iterate, accumulate tokens, render
```

## Minimum Relevance Threshold

Results below a minimum score should be discarded rather than injected as noise.

**Day-one threshold:** `final_score >= 0.05`. A memory with BM25 norm of 0.1, confidence of 0.8, and 30-day decay (0.5) produces `0.1 * 0.8 * 0.5 = 0.04` -- below threshold. This filters out very old, marginally relevant memories.

**Future hybrid threshold:** `final_score >= 0.01`. RRF scores are inherently smaller (max theoretical ~0.033 for rank-1 in both lists with k=60), so the threshold must be lower.

## Accepted Trade-offs and Risks

| # | Trade-off | Mitigation |
|---|-----------|------------|
| 1 | **FTS5 misses semantic matches** ("database timeout" vs "SQLite lock contention") | Accepted for day-one. Tags and type filters provide a second axis. Hybrid RRF upgrade path is clear. |
| 2 | **BM25 normalization is approximate** (dividing by 25.0 is empirical, not principled) | Monitor actual BM25 score distributions in practice. Adjust divisor if scores cluster unexpectedly. The 25.0 constant comes from CC-v3's production usage. |
| 3 | **FTS5 dedup misses semantic duplicates** | Accepted for day-one. Consolidation pass (`oro memories consolidate`) catches what write-time dedup misses. Cosine dedup (0.85 threshold) comes with embeddings. |
| 4 | **Token budget heuristic (len/4) is imprecise** | Good enough for budgeting. Over-counting by 10-20% is acceptable -- we'd rather inject slightly fewer memories than blow the budget. |
| 5 | **No per-type weighting** | All memory types scored equally. A `decision` isn't weighted higher than a `gotcha`. Future work could add type-specific confidence floors or score multipliers. |
| 6 | **Decay penalizes long-lived truths** | Architectural decisions stored with confidence 0.95+ partially compensate. Future: add a `pinned` boolean for memories that should never decay. |

## Summary of Concrete Parameters

| Parameter | Day-One Value | Future (Hybrid) Value |
|-----------|--------------|----------------------|
| Search method | FTS5 BM25 | FTS5 + cosine via RRF |
| RRF k-constant | N/A | 60 |
| Decay function | Exponential | Exponential |
| Decay half-life | 30 days | 30 days |
| Dedup method | FTS5 overlap (BM25 > 0.7) | FTS5 + cosine (> 0.85) |
| Max memories injected | 5 | 5 |
| Token budget | 500 tokens | 500 tokens |
| Min relevance threshold | 0.05 | 0.01 |
| BM25 normalization divisor | 25.0 | 25.0 |
| Confidence default | 0.8 | 0.8 |
