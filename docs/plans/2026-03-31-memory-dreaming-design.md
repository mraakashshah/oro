# Memory Dreaming & Staleness Awareness Design

**Date:** 2026-03-31
**Status:** Brainstormed — ready for adversarial review.

## Goal

Make oro's agents smarter by adding two memory patterns from Claude Code:
1. **Dreaming** — an LLM-powered ops agent that reads the memories table every 5 completed beads, synthesizes cross-session patterns, resolves contradictions, and prunes obsolete memories.
2. **Staleness warnings** — annotate injected memories with age so workers verify old claims instead of trusting them blindly.

## Context: What Exists Today

Oro has a working memory pipeline:
- **336 memories** accumulated over 4 weeks (184 self-report, 129 LLM-extracted, 23 knowledge-imported)
- **Real-time extraction:** `drain.go` parses `[MEMORY]` markers from worker stdout as they stream
- **Post-session extraction:** `ExtractWithLLM()` sends tail 50KB of session text to haiku, extracts 0-5 learnings
- **Storage:** SQLite `memories` table with FTS5 full-text index, TF-IDF embeddings, RRF hybrid search
- **Injection:** `ForPrompt()` retrieves top 5 memories by relevance (FTS5 + vector), injects into worker prompt section 4
- **Consolidation:** Manual `oro memories consolidate` — mechanical pruning (decay score < 0.1) + merging (FTS5 similarity > 0.8)
- **Decay:** Half-life 30 days (`confidence * 0.5^(age_days/30)`), pinned memories exempt

### What's Missing

1. **No cross-session synthesis.** Extraction sees one session at a time. If 5 workers independently discover the same worktree cleanup gotcha, that's 5 separate memories competing for top-5 slots. Nobody synthesizes "this is a systemic pattern."
2. **No contradiction resolution.** Memory #234 says X about merge conflicts, memory #298 says the opposite after a refactor. Both persist. Workers get confused.
3. **No semantic pruning.** Decay is time-based, not relevance-based. A 60-day-old memory about dolt corruption is critical; a 10-day-old memory about a renamed test is worthless. Mechanical decay can't tell the difference.
4. **No staleness warning.** Workers receive memories with no age context. A 25-day-old memory about a function that was refactored gets trusted blindly.

## Pattern 1: Dreaming

### What It Does

An ops agent (spawned by the dispatcher) reads the entire memories table, synthesizes cross-memory patterns, resolves contradictions, merges duplicates with real understanding, and prunes memories whose referenced code no longer exists. Full create/merge/delete power — consistent with how mechanical consolidation already works, just smarter.

### Trigger

Every 10 completed beads, OR when an epic closes. Epic close is a natural consolidation point — a body of related work just finished, good time to synthesize what was learned. The dispatcher increments a counter in `handleDone()` after successful merge; when counter hits 10, it spawns the dreaming ops agent and resets the counter. Epic close triggers dreaming unconditionally (in `checkEpicAutoClose()` after successful close).

### Implementation

**Dreaming as an ops agent.** Oro already has an ops spawner pattern (`pkg/ops/`) — short-lived Claude processes for judgment-heavy operations. Dreaming fits this pattern exactly.

1. **New ops type:** `OpsDream` in `pkg/ops/dream_prompt.go`
   - Prompt: "Here are all current memories. Read them. Then: (1) Merge duplicates — if multiple memories describe the same insight, keep the best-worded one, delete the rest. (2) Resolve contradictions — if two memories disagree, check which one's referenced code still exists, keep the correct one. (3) Synthesize patterns — if 3+ memories describe the same class of problem, create one authoritative memory and delete the originals. (4) Prune obsolete — if a memory references a function/file that no longer exists, delete it. Output your actions as structured commands."
   - Input: Full memories dump (from `SELECT * FROM memories ORDER BY created_at DESC`)
   - Model: haiku (cheap, sufficient for this task)
   - Timeout: 60s (larger input than extraction)

2. **Output format:** The dreaming agent outputs structured actions:
   ```
   [DELETE] id=234 reason=references renamed function foo()
   [DELETE] id=298 reason=contradicted by #301, code confirms #301
   [MERGE] keep=156 delete=189,203 reason=all describe worktree cleanup ordering
   [CREATE] type=pattern tags=worktree,cleanup: Worktree cleanup must defer until escalation completes — 3 independent discoveries confirmed this pattern
   ```

3. **Action executor:** `pkg/memory/dream.go` parses the output and executes actions against the memories table. Each action is logged to the events table for auditability.

4. **Dispatcher wiring:** In `handleDone()`, after successful merge:
   ```go
   d.beadsSinceDream++
   if d.beadsSinceDream >= d.cfg.DreamInterval { // default 10
       d.beadsSinceDream = 0
       go d.spawnDream(ctx)
   }
   // Also triggered unconditionally in checkEpicAutoClose() after epic closes.
   ```

**Files modified:**
- `pkg/dispatcher/dispatcher.go` — Add beadsSinceDream counter, DreamInterval to Config, spawnDream() call in handleDone()
- `pkg/memory/memory.go` — Add DumpAll() method for full export, DeleteByID(), MergeMemories()

**Files created:**
- `pkg/ops/dream_prompt.go` — Dream prompt builder
- `pkg/ops/dream_prompt_test.go`
- `pkg/memory/dream.go` — Action parser + executor
- `pkg/memory/dream_test.go`

### What Dreaming Does NOT Do

- Does not read raw worker session logs (extraction already handles that)
- Does not run during bead execution (only between beads)
- Does not touch rejection_history table (separate concern)
- Does not require new dependencies (uses existing ops spawner)

## Pattern 2: Staleness Warnings

### What It Does

When memories are injected into the worker prompt, each memory is annotated with its age. Memories older than 7 days include a warning that the worker should verify claims against current code before relying on them.

### Implementation

1. **Modify `ForPrompt()`** (`pkg/memory/memory.go:852-914`) to include age in the formatted output:
   ```
   ## Memory

   | ID | Type | Content | Age |
   |----|------|---------|-----|
   | 336 | pattern | GetConfig() testonly accessor on Dispatcher... | 6d |
   | 298 | gotcha | Worktree cleanup must defer until... | 18d ⚠ |

   ⚠ = older than 7 days. Verify these claims against current code before relying on them.
   ```

2. **Add staleness threshold to Config:** `MemoryStaleDays int` (default 7). Memories older than this get the ⚠ marker.

3. **Add warning text to worker prompt.** In `pkg/worker/prompt.go`, after the Memory section injection, append: "Memories marked ⚠ are older than 7 days. Their claims about code behavior may be outdated — verify by reading the actual source before acting on them."

**Files modified:**
- `pkg/memory/memory.go` — Modify ForPrompt() to include age column and warning marker
- `pkg/worker/prompt.go` — Add staleness warning text after memory injection

**Files created:**
- None (changes to existing files only)

## Dependency

Staleness warnings are independent — can ship immediately.
Dreaming depends on nothing new — uses existing ops spawner and memories table.
They can be implemented in parallel.

## Risk Assessment

| Risk | Severity | Mitigation |
|------|----------|------------|
| Dreaming agent times out on large memory table | Low | 60s timeout. No actions applied on timeout (output fully parsed before execution). At 1000 memories (~100K chars) still within haiku context. |
| Dreaming outputs malformed actions | Low | Parser skips unrecognized lines. No action applied unless format matches exactly. |
| Staleness warning causes workers to distrust all memories | Low | Threshold is 7 days, not 1 day. Most injected memories are recent. Warning is mild ("verify"), not "ignore." |

## What We're NOT Doing

- No neural embeddings for memory search (TF-IDF + FTS5 is sufficient for bead-title queries)
- No side-query memory selection with a fast model (FTS5+RRF is appropriate for oro's structured queries)
- No background memory extraction daemon (inline extraction in drain.go is adequate)
- No synthesis-enforced coordination (handoff quality is not a reported problem)
