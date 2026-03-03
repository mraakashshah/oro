# Memory Intake Pipeline Fix

**Date:** 2026-03-03
**Status:** Draft
**Prior art:** `2026-02-07-memory-system-spec.md`, `2026-02-10-session-end-learning-synthesis.md`, `2026-02-24-memory-quality-design.md`

## Problem

Oro's memory system has zero organic intake. 267 completed bead assignments and 7,411 events have produced zero `daemon_extracted` or `self_report` memories. The only 23 entries are bulk-imported from `knowledge.jsonl`. Four extraction paths exist — all broken:

| Path | Code | Failure mode |
|------|------|-------------|
| Daemon extraction | `extractor.go:96` reads `events` table | Event payloads are JSON blobs — regex expects prose at line start |
| `[MEMORY]` markers | `worker.go:593` | Workers never emit markers — instruction buried in section 3a of 12 |
| Implicit extraction | `worker.go:612` → `ExtractImplicit()` | `claude -p` stdout doesn't produce `^Gotcha:` at line start |
| Handoff persistence | `dispatcher.go:1438` | Workers don't populate `Learnings`/`Decisions` fields |

Additionally, `cleanWorkerLogs()` (cmd_start.go:351) runs `os.RemoveAll` on `~/.oro/workers/` at every `oro start`, destroying log files before anyone can extract from them.

## Solution

Two complementary intake paths plus cleanup of dead code.

### Path 1: Post-Completion LLM Extraction

After a bead completes, run a lightweight haiku call over the worker's session text to extract 0-5 memories. Does not depend on worker cooperation.

**`oro start` (dispatcher mode):**
- Worker's `processOutput()` accumulates all `claude -p` stdout in `sessionText` (worker.go:574)
- When stdout closes, `processOutput()` fires extraction (worker.go:612)
- **Single extraction point:** Only `processOutput` calls `ExtractWithLLM()`. Remove the `extractImplicitMemories()` calls from `SendDone` (worker.go:1034) and `SendHandoff` (worker.go:1057) to prevent double invocation. `processOutput` always finishes before `SendDone`/`SendHandoff` runs (guarded by `outputWg.Wait()` in `awaitSubprocessAndReport`), so the extraction is guaranteed to complete before the completion message is sent.
- The worker process has a memory store via `SetMemoryStore` (cmd_worker.go:69) — results insert directly
- **Dispatcher fallback removed:** `extractAndStoreLearnings()` in the dispatcher becomes a no-op (log-only). Worker-side extraction is the single source of truth. This eliminates the race condition where both worker-side and dispatcher-side extract from the same session text, producing duplicates. If the worker was killed before extraction ran, the learnings are lost — acceptable trade-off for architectural simplicity. The `oro remember` path (Path 2) covers the high-signal learnings for clean exits.

**`oro work` (single worker mode):**
- `DrainOutput()` in drain.go captures all stdout line-by-line
- Add a `strings.Builder` accumulator alongside line processing
- `DrainOutput` signature changes: add `spawner Spawner` parameter (nil-safe — skips extraction when nil)
- Call `ExtractWithLLM()` after the scanner loop completes
- `executeWork()` in cmd_work.go passes a `CLISpawner{}` as the spawner argument

**The extraction function:**

New file: `pkg/memory/extract_llm.go`

```go
// Spawner abstracts subprocess creation for testability.
type Spawner interface {
    Spawn(ctx context.Context, model, prompt string) (io.ReadCloser, error)
}

func ExtractWithLLM(ctx context.Context, spawner Spawner, sessionText, beadID string, store *Store) error
```

- Accepts a `Spawner` interface for testability (production: wraps `claude -p`, tests: returns canned output)
- Production `CLISpawner` spawns `claude -p --model haiku` with stdin set to `/dev/null` (required — claude -p hangs without it, per worker.go:1249-1256)
- Caps input at 50K chars (tail of session — learnings cluster near end)
- 30s timeout via `context.WithTimeout`
- Parses output with existing `ParseMarker()` — reuses `[MEMORY]` marker format
- Inserts results as `source=llm_extracted`, `confidence=0.7`
- Best-effort: errors logged, not propagated

**Extraction prompt:**

```
You are a learning extractor. Given a worker session log, identify 0-5 genuine
discoveries worth remembering for future sessions. Only extract non-obvious
insights — things a developer working on this codebase would benefit from knowing.

Categories:
- lesson: something that worked or a technique discovered
- gotcha: something surprising or counterintuitive
- decision: an architectural choice and why it was made
- pattern: a reusable approach that emerged

For each discovery, output exactly one line in this format:
[MEMORY] type=<type> tags=<comma-separated>: <concise description>

If the session contains no genuine learnings (routine coding, straightforward
fixes), output nothing. Most sessions will have 0-2 learnings. Do not fabricate.

Session log (last ~12K tokens):
```

**Model choice:** Haiku — cheapest (~$0.001 per call), fastest, sufficient for extraction. 267 completions would have cost ~$0.27 total.

**Failure modes:**
- Haiku unavailable → skip extraction silently (best-effort)
- Haiku hallucinates → `confidence=0.7` is lower than explicit memories (0.8), time-decay prunes bad entries, `Consolidate()` deduplicates
- Session text empty → early return, no API call

### Path 2: Explicit `oro remember` in Worker Exit Protocol

Move learnings instruction from passive section 3a to the exit protocol (section 12).

**Current exit section (prompt.go:239-249):**
```
When acceptance criteria pass and quality gate is green:
Your work is complete. The dispatcher will: [merge, close, etc.]
```

**New exit section:**
```
When acceptance criteria pass and quality gate is green:

1. Reflect: did you discover anything non-obvious? Run `oro remember` for each:
   oro remember "lesson: <what you learned>"
   oro remember "gotcha: <what surprised you>"
   oro remember "decision: <why you chose this approach>"
   Only save genuine discoveries — not obvious facts.

2. Your work is complete. The dispatcher will:
   - Merge your worktree branch to main
   - Close the bead if merge succeeds
```

**Why this works better than `[MEMORY]` markers:**
- `oro remember` is a real CLI command — workers know how to run shell commands
- Positioned at exit when the worker has full context of what it learned
- Step 1 of the exit protocol, not buried in the middle
- Inserts directly into memory store with `source=cli`, `confidence=0.8`

**Limitation:** Workers that crash, get killed, or hit context limits never reach exit. Path 1 (LLM extraction) is the reliability backstop.

**Section 3a changes:** Slim down to just showing the `## Relevant Memories` table from `ForPrompt()`. Remove instruction to write `[MEMORY]` markers. Reading memories remains useful; writing moves to exit.

### Cleanup: Remove Dead Extraction Paths

**Delete:**

| What | Location | Why |
|------|----------|-----|
| `extractionPatterns` | `extractor.go:17-34` | Regex table that never matches JSON payloads |
| `ExtractLearnings()` | `extractor.go:40-91` | Scans text against dead patterns |
| `implicitPatterns` | `memory.go:843-857` | Duplicate regex table in memory package |
| `ExtractImplicit()` | `memory.go:861-890` | Used by DrainOutput and extractImplicitMemories — both replaced |
| `extractImplicitMemories()` body | `worker.go:615-635` | Replace with `ExtractWithLLM()` call |
| `extractImplicitMemories()` call in `SendDone` | `worker.go:1034` | Remove — extraction happens once in `processOutput`, not again on send |
| `extractImplicitMemories()` call in `SendHandoff` | `worker.go:1057` | Remove — same reason |
| Per-line `ExtractImplicit` call | `drain.go:47-52` | Remove; extraction moves to post-drain |
| `cleanWorkerLogs()` call | `cmd_start.go:372` | Stops wiping logs on startup |
| Dispatcher `extractAndStoreLearnings()` body | `extractor.go:96-146` | Replace with no-op log-only (worker-side is single source of truth) |

**Keep:**

| What | Location | Why |
|------|----------|-----|
| `ParseMarker()` | `memory.go:787-810` | Used by real-time capture AND LLM output parsing |
| `[MEMORY]` capture in `processOutput()` | `worker.go:593` | Still works if workers voluntarily emit markers |
| `[MEMORY]` capture in `DrainOutput()` | `drain.go:43` | Same — real-time capture remains |
| `persistHandoffContext()` | `dispatcher.go:1438` | Works if workers populate handoff fields |
| `ForPrompt()` | `memory.go:901-963` | Memory retrieval/injection is working fine |

### Log File Lifecycle

**Current:** `cleanWorkerLogs()` runs `os.RemoveAll(~/.oro/workers/)` on every `oro start`.

**New:** `cleanStaleWorkerLogs(oroHome string, maxAge time.Duration)` — deletes worker log dirs older than 7 days. Called on startup. Worker logs survive across sessions for debugging and fallback extraction.

Per-worker log rotation already works: `openLogFile()` (worker.go:641) uses `O_APPEND`, and `closeLogFile()` + `openLogFile()` pair in `handleAssignment` (worker.go:372-373) handles new assignments.

## New Code

| File | What |
|------|------|
| `pkg/memory/extract_llm.go` | `Spawner` interface, `CLISpawner` (production), `ExtractWithLLM()` |
| `cmd/oro/cmd_start.go` | `cleanStaleWorkerLogs()` — replaces `cleanWorkerLogs()` |

## Modified Code

| File | Change |
|------|--------|
| `pkg/worker/worker.go` | `extractImplicitMemories()` → calls `ExtractWithLLM()`. Add `SetSpawner()` method. Remove `extractImplicitMemories()` calls from `SendDone` (line 1034) and `SendHandoff` (line 1057). |
| `pkg/worker/drain.go` | Add `strings.Builder` accumulator. Add `spawner Spawner` param. Call `ExtractWithLLM()` after loop. |
| `pkg/worker/prompt.go` | Move learnings to exit section, slim section 3a |
| `pkg/dispatcher/extractor.go` | `extractAndStoreLearnings()` becomes log-only no-op (worker-side is authoritative) |
| `cmd/oro/cmd_start.go` | Replace `cleanWorkerLogs()` with `cleanStaleWorkerLogs()` |
| `cmd/oro/cmd_worker.go` | Create and pass `CLISpawner` to worker via `SetSpawner()` |
| `cmd/oro/cmd_work.go` | Pass `CLISpawner{}` to `DrainOutput()` |

## Broken Tests (must update)

Removing `ExtractImplicit()`, `ExtractLearnings()`, and `cleanWorkerLogs()` breaks existing tests at compile time. These must be updated or replaced:

| File | What breaks | Fix |
|------|-------------|-----|
| `pkg/memory/memory_test.go` | `TestExtractImplicit` calls deleted `ExtractImplicit()` | Delete test (function removed) |
| `pkg/dispatcher/extractor_test.go` | 10+ `TestExtractLearnings_*` tests call deleted `ExtractLearnings()` | Delete tests. Add new tests for the no-op `extractAndStoreLearnings()`. |
| `pkg/dispatcher/extractor_test.go` | 25+ tests for `extractAndStoreLearnings()` verify event-payload parsing | Replace with tests verifying the new log-only behavior |
| `pkg/worker/drain_test.go` | Tests call `DrainOutput()` with old signature (no spawner param) | Update call sites to pass `nil` spawner (extraction skipped) |
| `cmd/oro/cmd_start_test.go` | `TestDaemonStartupCleansWorkerLogs` calls deleted `cleanWorkerLogs()` | Replace with tests for `cleanStaleWorkerLogs()` |

## Testing Strategy

- Unit test `ExtractWithLLM()` with mock `Spawner` — feed canned `[MEMORY]` output, verify inserts
- Unit test `CLISpawner` sets stdin to `/dev/null` (verify cmd.Stdin != nil)
- Unit test prompt truncation (>50K chars → takes tail)
- Unit test `cleanStaleWorkerLogs()` with temp dirs at various ages
- Unit test `DrainOutput()` with mock spawner and memory store, verify end-to-end
- Test empty session text → no API call (no spawner invocation)
- Test haiku failure → graceful degradation (no memories, no error)
- Test nil spawner → extraction skipped silently
- Existing `ParseMarker` tests remain untouched — format unchanged

## Latency Impact

LLM extraction adds up to 30s per bead completion (haiku timeout). This happens inside `processOutput()` after stdout closes, before `outputWg.Done()`. The quality gate and DONE message are blocked until extraction completes.

**Mitigation:** Extraction runs only once per bead (removed duplicate calls from `SendDone` and `SendHandoff`). In practice, haiku extraction on a truncated log takes 3-5s, not 30s. The 30s timeout is a safety cap. For a swarm of 3 workers completing ~90 beads/day, this adds ~5-8 minutes of total wall time — acceptable for the value of organic memory intake.

**Future optimization:** If latency becomes a problem, extraction can move to a background goroutine that fires after `outputWg.Done()` but before `SendDone`. This would unblock the quality gate while extraction runs in parallel. Not needed for v1.

## Non-Goals

- Changing memory retrieval (ForPrompt, Search, HybridSearch) — working fine
- Changing memory CLI (oro remember, recall, forget) — working fine
- Changing consolidation — working fine
- Adding memory admin UI
- Modifying knowledge.jsonl format or ingest path
