# Oro Memory System: Spec & Premortem

**Date:** 2026-02-07
**Status:** Decided — all open questions resolved 2026-02-07

## Problem

Oro workers ralph-loop (cycle context). Each fresh worker starts cold. Handoff YAML carries immediate task context, but the system has no way to recall cross-session learnings: what patterns work, what failed, architectural decisions, gotchas discovered during implementation.

Without memory, the same mistakes repeat across worker sessions. Decisions get re-debated. Patterns get re-discovered.

## Reference Implementations

### BCR (dual-backend, most feature-complete)

Source: `references/bcr/docs/analysis/memory-system-analysis.md`

| Component | Implementation | Notes |
|-----------|---------------|-------|
| **Light Memory** | JSONL file (`.bcr/memories.jsonl`) | Keyword substring search, no deps, deduplication by exact match |
| **Rich Memory** | SQLite + ONNX embeddings (`memories.db`) | Cosine similarity, confidence scoring, 30-day decay half-life |
| **Extraction** | Claude API (Haiku) + regex fallback | Patterns: "I learned...", "Note:", "Important:", "Gotcha:", "Pattern:" |
| **Daemon (memd)** | Watchfiles-based session log watcher | Auto-extracts from completed session `.jsonl` files |
| **Storage** | `bcr remember "..."` CLI | Manual memory creation with tags and context |
| **Recall** | `recall.recall(query)` module | Vector similarity × confidence × time decay — but never wired to CLI |
| **Consolidation** | `bcr memories consolidate` | Merges duplicates, prunes stale entries by similarity threshold |

**Worked:** Auto-extraction from session logs, regex fallback (no API key needed), JSONL as simple backend, memory types (fact/lesson/decision/preference/gotcha), time decay scoring.

**Failed:** Recall never exposed via CLI. Two backends created confusion. Embeddings fragile (fastembed ONNX crashed on arm64 SIGILL). Daemon required separate start. Decision detection didn't feed memory. **Extracted but never injected back into prompts** — the critical gap.

### Continuous Claude v3 (most comprehensive memory)

Source: `docs/learnings/continuous-claude-v3-learnings.md`

| Component | Implementation | Notes |
|-----------|---------------|-------|
| **Storage** | PostgreSQL + pgvector | `archival_memory` table with 1024-dim BGE embeddings, HNSW index |
| **Search** | Hybrid RRF (Reciprocal Rank Fusion) | `Score = 1/(60 + text_rank) + 1/(60 + vector_rank)` — combines BM25 lexical + vector semantic |
| **Learning types** | `WORKING_SOLUTION`, `FAILED_APPROACH`, `ARCHITECTURAL_DECISION` | Richer than BCR's categories |
| **Extraction** | SessionEnd daemon | Spawns headless Claude (Sonnet) to extract learnings from thinking blocks |
| **Injection** | SessionStart hook (`memory-awareness`) | Surfaces relevant learnings at session start — **closes the loop BCR missed** |
| **Commands** | `/recall "query"`, `remember "learning"` | Both exposed and working |
| **Context integration** | Staggered warnings (70%→80%→90%) | Auto-creates continuity checkpoint before compaction |

**Key insight CC-v3 gets right:** Memory is part of the **continuity loop** — Phase 1 loads learnings, Phase 4 extracts them. BCR only had Phase 4.

**Why not adopt CC-v3 wholesale:** PostgreSQL + pgvector is heavy infrastructure for a local-first CLI tool. The hybrid RRF search pattern is excellent but needs a simpler backend.

### OpenClaw (per-agent memory isolation)

Source: `docs/learnings/openclaw-learnings.md`

| Component | Implementation | Notes |
|-----------|---------------|-------|
| **Memory tools** | `memory_search` + `memory_get` in system prompt | Agent has memory as a tool, not just context |
| **Backend** | LanceDB (vector search) | Embedded, no server needed |
| **Isolation** | Per-agent workspace | Each agent's memory is isolated — no cross-contamination |

**Key insight:** Memory as a **tool** the agent can query mid-task, not just context injected at start. This lets the agent pull memories when relevant rather than being pre-loaded with potentially noisy ones.

### Compound Engineering (knowledge codification)

Source: `docs/learnings/compound-engineering-learnings.md`

| Component | Implementation | Notes |
|-----------|---------------|-------|
| **Knowledge store** | `docs/solutions/` with YAML frontmatter | Searchable by category, tags, date |
| **Extraction** | `/workflows:compound` command | Parallel subagents (Context Analyzer, Solution Extractor) document solved problems |
| **Recall** | `learnings-researcher` agent | Queries institutional knowledge during planning phase |

**Key insight:** Compound treats memory as **documented solutions**, not raw learnings. YAML frontmatter makes them structured and searchable. The `learnings-researcher` agent queries them during planning — memory feeds into the *plan*, not just the *prompt*.

### Cross-Reference Summary

| Dimension | BCR | CC-v3 | OpenClaw | Compound |
|-----------|-----|-------|----------|----------|
| **Backend** | JSONL + SQLite | PostgreSQL + pgvector | LanceDB | Markdown + YAML |
| **Search** | Keyword + cosine sim | Hybrid RRF (BM25 + vector) | Vector | Agent grep |
| **Extraction** | Daemon + regex | SessionEnd daemon (headless Claude) | N/A (manual) | `/workflows:compound` |
| **Injection** | Never wired | SessionStart hook | Tool (on-demand) | Planning agent |
| **Loop closed?** | No | Yes | Partially | Yes |
| **Infrastructure** | None / fastembed | PostgreSQL server | Embedded DB | Filesystem |

**The pattern that works:** Extract → Store → **Inject back**. BCR built 2/3. CC-v3 and Compound close the loop.

## Proposed Design for Oro

### Principles

1. **One backend** — no dual-backend confusion. Pick one and commit.
2. **Memory feeds back into prompts** — extraction without injection is useless.
3. **Automatic** — daemon extracts, prompt construction injects. Human effort = zero.
4. **Crash-safe** — memory survives worker crashes, Manager restarts, ralph handoffs.
5. **Queryable** — both programmatic (Go API) and human (`oro recall <query>`).

### Architecture

Three memory layers (distinct purposes, clear ownership):

```
┌──────────────────────────────────────────────────────────────┐
│  Layer 1: Bead Annotations (already designed)                │
│  Owner: beads DB (bd)                                        │
│  Scope: per-bead merge context, acceptance criteria, notes   │
│  Lifetime: permanent, tied to work unit                      │
│  Access: bd show <id>                                        │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│  Layer 2: Handoff Files (already designed, R3)               │
│  Owner: worktree filesystem                                  │
│  Scope: immediate task context for ralph continuation        │
│  Lifetime: ephemeral, consumed by next worker                │
│  Access: .oro/handoff.yaml in worktree                       │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│  Layer 3: Project Memory (NEW — this spec)                   │
│  Owner: .oro/state.db (SQLite, memories table)               │
│  Scope: cross-session learnings, patterns, gotchas           │
│  Lifetime: permanent, with decay scoring on retrieval        │
│  Access: oro remember / oro recall                           │
│  Search: FTS5 (BM25) now, + embeddings column later          │
└──────────────────────────────────────────────────────────────┘
```

### Layer 3 Detail: Project Memory

**Storage:** `.oro/state.db` — `memories` table in the same SQLite database used for runtime state.

```sql
CREATE TABLE memories (
    id INTEGER PRIMARY KEY,
    content TEXT NOT NULL,
    type TEXT NOT NULL,  -- lesson | decision | gotcha | pattern | preference
    tags TEXT,           -- JSON array
    source TEXT NOT NULL, -- 'self_report' | 'daemon_extracted'
    bead_id TEXT,
    worker_id TEXT,
    confidence REAL DEFAULT 0.8,
    created_at TEXT DEFAULT (datetime('now')),
    embedding BLOB       -- populated by oro-ldf epic (local embeddings + hybrid RRF search)
);

CREATE VIRTUAL TABLE memories_fts USING fts5(content, tags, content=memories, content_rowid=id);
```

**Types:** `lesson`, `decision`, `gotcha`, `pattern`, `preference`

**Why SQLite, not JSONL:**
- Retrieval is the hard problem, not storage. JSONL grep can't rank results.
- FTS5 gives BM25 ranked search out of the box — keyword "database timeout" surfaces related "SQLite lock contention" via term proximity.
- Already in the stack for runtime state — one DB, one Go driver, one dependency.
- Embeddings column reserved for future semantic search (cosine similarity) without schema migration.
- Structured queries (`WHERE type='gotcha' AND bead_id=?`) are free.
- Time decay scoring is a SQL expression: `confidence * (0.5 ^ ((julianday('now') - julianday(created_at)) / 30.0))`

### Extraction (DECIDED: Option C — Hybrid)

**Path 1: Worker self-reports (real-time)**

Worker Go binary monitors Claude's stdout for extraction markers:
```
[MEMORY] type=gotcha: ruff --fix must run before pyright
[MEMORY] type=lesson: SQLite WAL mode requires single-writer for consistency
```

Claude is instructed (in bead prompt, R5) to emit `[MEMORY]` lines. Go binary parses and INSERTs to `memories` table with `source='self_report'`.

**Path 2: Daemon post-session extraction (background)**

Daemon watches session logs after worker completes, extracts implicit learnings via regex patterns ("I learned", "Note:", "Gotcha:", "Pattern:") and optionally Claude API (Haiku). INSERTs with `source='daemon_extracted'`.

BCR's daemon extraction worked — the failure was never wiring recall. Oro closes that loop via prompt injection (see below).

**Path 3: Periodic consolidation**

Manager runs `oro memories consolidate` between beads: deduplicates by FTS5 similarity, merges related memories, prunes low-confidence stale entries. Not a daemon — triggered by Manager's event loop.

### Injection (Memory → Prompt)

This is where BCR failed — it extracted but never injected. Two complementary approaches:

**Approach 1: Prompt injection (CC-v3 pattern)**

Go wrapper queries `.oro/memories.jsonl` before constructing bead prompt (R5):
1. Filter by tags matching bead tags
2. Score by keyword overlap with bead description × time decay
3. Inject top 3 into prompt as a `## Relevant Memories` section (200 token cap)

**Approach 2: Memory as a tool (OpenClaw pattern)**

Instruct workers that they can query memory mid-task:
```
oro recall "SQLite WAL" --limit=5
```
Worker reads results and incorporates as needed. This avoids pre-loading noise — the agent pulls memories when it knows what's relevant.

**Recommendation:** Start with Approach 1 (prompt injection) for simplicity. Add Approach 2 when `oro recall` is built into the Go binary and workers can shell out to it.

### CLI Commands

Built into the Go binary (same binary as Manager/Worker):

```
oro remember "content" --type=lesson --tags=x,y    # Manual INSERT
oro recall "query" [--limit=10] [--type=gotcha]     # FTS5 ranked search
oro memories [--tag=x] [--type=y]                   # List/browse
oro memories consolidate                             # Dedup + prune via FTS5 similarity
```

---

## Premortem

*It's 3 months from now. Oro's memory system failed. Why?*

### Tigers (high probability, high impact)

| # | Risk | Severity | Mitigation |
|---|------|----------|------------|
| T1 | **Claude ignores [MEMORY] markers** — extraction rate drops to near zero because workers don't reliably emit markers | HIGH | Make marker instruction prominent in bead prompt. Add fallback: Go binary also scans for "I learned", "Note:", "Gotcha:" regex patterns in stdout (BCR's proven regex set). Belt + suspenders. |
| T2 | **Memory injection is noise** — irrelevant memories in prompt waste tokens and confuse workers | HIGH | Start with tag-only filtering (high precision, low recall). Only inject memories whose tags overlap with bead tags. Let workers ignore irrelevant ones. Cap at 3 memories, not 5. Keep token budget tiny (200 tokens). |
| T3 | **Memories table grows unbounded** — 5 workers × many beads × many sessions = thousands of memories | LOW | SQLite handles millions of rows. Consolidation pass prunes low-confidence stale entries. FTS5 index keeps search fast. Not a real problem. |

### Elephants (known issues, accepted)

| # | Risk | Notes |
|---|------|-------|
| E1 | **FTS5 misses semantic matches** | Accepted. "database timeout" won't match "SQLite lock contention" unless terms overlap. Tag-based filtering + BM25 term proximity compensate. Embeddings column reserved for future upgrade — no schema change needed. |
| E2 | **Memory quality varies** | Some memories will be trivial or wrong. Confidence scoring + decay means bad memories fade. Consolidation can prune low-confidence entries. |
| E3 | **No cross-project memory** | Memories are per-project (`.oro/memories.jsonl`). Fine for now. Global memory is a future concern. |

### Paper Tigers (seem scary, actually fine)

| # | Risk | Why fine |
|---|------|---------|
| P1 | **Concurrent SQLite writes** | Manager is single writer for main DB. Workers write to their own worktree SQLite copies. Manager merges rows on bead completion. No concurrent writes to same DB. |
| P2 | **Memory conflicts with beads DB** | Different scopes — beads own work artifacts, memories own cross-session learnings. Clear ownership boundary. |

---

## Resolved Questions

1. **Extraction method: Option C (hybrid).** Self-report markers for real-time capture + daemon for implicit extraction + periodic consolidation for quality. Maximizes capture rate.
2. **Where does Manager merge worker memories?** Worker INSERTs to a local SQLite in its worktree (`.oro/state.db`). On bead completion (merge step), Manager copies new rows to the main `.oro/state.db`. Same merge-on-completion flow as code.
3. **Should memories be git-tracked?** No. Memories live in SQLite (binary, doesn't diff). SQLite is runtime-adjacent state in `.oro/state.db`. Project knowledge worth preserving long-term goes to `docs/decisions-and-discoveries.md` (human-curated, git-tracked).
4. **Why SQLite, not JSONL?** Retrieval quality. FTS5 gives BM25 ranked search; JSONL grep can't rank. Already in the stack for runtime state — one DB, one driver. Embeddings column reserved for future semantic search.
5. **Why not LanceDB?** No Go SDK. Manager is a Go binary. SQLite FTS5 + future embeddings column covers the same ground without adding a language bridge.
