# Decisions and Discoveries

## 2026-02-07: Add reflection step to finishing-work
**Tags:** #skills #workflow #feedback-loops
**Context:** Reviewed aleiby/claude-skills/tackle — its reflect phase logs friction after every PR as queryable data
**Decision:** Added Step 4 (Reflect) to finishing-work skill. Captures off-script moments, slowdowns, and improvement suggestions before cleanup.
**Implications:** Skills can self-improve over time if friction is consistently logged. "Clean run" should be rare — most work has learnable friction.

## 2026-02-07: Skip autoskill pattern (user-correction-driven learning)
**Tags:** #skills #decisions #philosophy
**Context:** Reviewed AI-Unleashed/Claude-Skills/autoskill — watches for user corrections during sessions and proposes skill edits
**Decision:** Not adopted. We prefer self-directed reflection (agent notices its own friction) over user-directed correction harvesting.
**Implications:** The reflect step in finishing-work is our feedback loop. Keep it self-directed.

## 2026-02-07: Memory system — SQLite + hybrid extraction, not JSONL
**Tags:** #memory #architecture #decisions
**Context:** Resolving open questions in memory system spec. JSONL was proposed for simplicity but retrieval (finding the right 3 memories for a 200-token prompt budget) is the hard problem, not storage. Keyword grep can't rank results.
**Decision:** Single SQLite DB (`.oro/state.db`) for both runtime state and memories. FTS5 for BM25 ranked search. Embeddings column reserved for future semantic search. Hybrid extraction: worker self-report markers (real-time) + daemon post-session extraction (background) + periodic consolidation. LanceDB rejected — no Go SDK.
**Implications:** One DB, one Go driver, one dependency. Memories not git-tracked (SQLite binary doesn't diff). Human-curated knowledge goes to `docs/decisions-and-discoveries.md`. CC-v3's retrieval architecture on a local-first backend.

## 2026-02-07: Create review-docs and review-implementation skills
**Tags:** #skills #review #quality
**Context:** Reviewed Xexr/marketplace review-documentation (1200-line multi-LLM orchestration) and review-implementation skills
**Decision:** Created two lean skills (<300 words each). Extracted structured review categories and severity-weighted output format from Xexr. Skipped multi-LLM dispatch, mermaid diagrams, execution checklists — Claude Code only.
**Implications:** Doc review and implementation review are separate concerns with different triggers. Both use read-only review phase before fixes.
