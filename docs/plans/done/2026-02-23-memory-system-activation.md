# Memory System Activation: Bridging knowledge.jsonl and memories.db

**Date:** 2026-02-23
**Status:** DRAFT (post adversarial review — gaps fixed)

## Problem

Oro has two memory systems that don't talk to each other:

| System | Storage | Write path | Read path | Entries |
|--------|---------|------------|-----------|---------|
| **memories.db** (SQLite) | `~/.oro/memories.db` | Worker `[MEMORY]` markers + implicit extraction | Dispatcher `ForPrompt()` → worker prompt injection | 1 |
| **knowledge.jsonl** (JSONL) | `.beads/memory/knowledge.jsonl` | `bd comments add <id> "LEARNED: ..."` via hook | Session priming `recent_learnings()` | 22 |

The SQLite pipeline is fully wired end-to-end:

- **Worker-side real-time**: `processOutput()` (worker.go:589) calls `ParseMarker()` on every stdout line for `[MEMORY]` markers, and `extractImplicitMemories()` (worker.go:608) runs `ExtractImplicit()` when stdout closes.
- **Dispatcher-side post-completion**: `extractAndStoreLearnings()` (extractor.go:96) scans event payloads for natural-language patterns after bead completion.
- **Injection**: `ForPrompt()` (memory.go:878) retrieves top 5 memories by FTS5+time decay and formats a markdown table injected into worker prompts.
- **Consolidation**: Runs after every 5 bead completions, pruning stale entries and merging duplicates.

But the pipe is nearly dry because **workers don't know they can emit `[MEMORY]` markers or natural-language patterns** — the prompt never tells them. Only 1 entry exists (a manual CLI test).

Meanwhile, knowledge.jsonl accumulates 22 entries because workers naturally use `bd comments` and the hook captures `LEARNED:` prefixes. But these entries never reach memories.db (except via the existing startup ingest at `cmd_start.go:403`).

## Goal

Bridge both systems so memories flow through the full pipeline:

1. Workers emit learnings (naturally or explicitly) → captured into memories.db
2. knowledge.jsonl entries sync to memories.db in real-time (hook) + on startup (existing)
3. Session priming surfaces memories from both sources
4. Dispatcher injects relevant memories from SQLite into worker prompts
5. All existing plumbing continues to work without changes

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Bridge both, don't unify | knowledge.jsonl is simple, portable, git-friendly. memories.db has FTS5, time decay, consolidation. Each has strengths. |
| Real-time sync on capture | `memory_capture.py` shells out to `oro remember` after writing to knowledge.jsonl. No SQLite in Python. |
| Emphasize natural language | Workers write naturally ("I learned...", "Gotcha:") with `[MEMORY]` as advanced option. Lower friction = more memories. |
| Startup ingest already exists | `ingestKnowledgeOnStartup()` at `cmd_start.go:403` already catches missed entries. No new dispatcher code needed. |

## Architecture

### Existing Extraction Paths (Already Working)

Three extraction paths already exist and are fully wired:

1. **Worker real-time markers** (`worker.go:589`): `ParseMarker()` on every stdout line → `memories.db`
2. **Worker implicit extraction** (`worker.go:608`): `ExtractImplicit()` on stdout close → `memories.db`
3. **Dispatcher post-completion** (`extractor.go:96`): `ExtractLearnings()` on event payloads → `memories.db`

Path 3 has its own pattern set in `extractor.go` (superset of `memory.go`'s patterns, includes `TIL:`, `This doesn't work because`, `The fix was`). Both sets are intentionally separate: worker-side runs on raw stdout, dispatcher-side runs on structured event payloads.

### Data Flow (After)

```
Worker stdout
  │
  ├─ [MEMORY] markers ──────► ParseMarker() ──────► memories.db  [EXISTS]
  │                                                      │
  ├─ Natural patterns ──────► ExtractImplicit() ────► memories.db  [EXISTS]
  │  ("I learned...",                                    │
  │   "Gotcha:", etc.)                                   │
  │                                                      │
  ├─ Event payloads ────────► ExtractLearnings() ───► memories.db  [EXISTS]
  │  (dispatcher-side)        (extractor.go)             │
  │                                                      ▼
  ├─ bd comments add ────────────────────────► knowledge.jsonl
  │  "LEARNED: ..."   (memory_capture.py)           │
  │                          │                      │
  │                          └─► oro remember ──► memories.db  [NEW]
  │                                                 │
  │                                                 ▼
  │                                          ForPrompt() ──► worker prompt  [EXISTS]
  │                                                 │
  └─ Session priming ◄── recent_learnings() ◄──── BOTH sources  [NEW]
```

Only two things are actually new: (a) the hook sync and (b) dual-source priming. Everything else already works — workers just need to be told how to emit learnings.

### Change 1: Worker Prompt — Memory Emission Instructions

**File:** `pkg/worker/prompt.go`
**Location:** New section between "Memory" (section 3) and "Relevant Code" (section 3b)

Add a section teaching workers how to save learnings:

```go
// 3a. Memory Emission (always present, between Memory and Relevant Code)
section(&b, "Saving Learnings", strings.Join([]string{
    "Save important discoveries for future workers. Two ways:",
    "",
    "**Natural language** (preferred — just write normally):",
    "  I learned that SQLite WAL mode is required for concurrent access",
    "  Gotcha: ruff --fix must run BEFORE pyright or types break",
    "  Note: the FTS5 triggers fire on INSERT only, not UPDATE",
    "  Decision: use table-driven tests for the parser package",
    "",
    "**Explicit markers** (for structured entries):",
    "  [MEMORY] type=gotcha tags=sqlite: WAL mode required for concurrent writes",
    "  [MEMORY] type=lesson tags=go,test: table-driven tests catch edge cases",
    "",
    "Types: lesson, decision, gotcha, pattern",
    "Only save genuinely useful discoveries — not obvious facts.",
}, "\n"))
```

**Why this wording:**
- Natural language first — lower friction, workers already write this way
- Explicit markers second — for workers that want control over type/tags
- "Only save genuinely useful discoveries" — prevents noise
- Examples are concrete and domain-relevant

### Change 2: Real-Time Sync — knowledge.jsonl → memories.db

**File:** `.claude/hooks/memory_capture.py`
**Change:** After writing to knowledge.jsonl, shell out to `oro remember`

```python
# Type detection from content (matches oro remember's parseTypePrefix patterns)
_TYPE_PREFIXES = [
    (re.compile(r"^gotcha:\s*", re.IGNORECASE), "gotcha"),
    (re.compile(r"^decision:\s*", re.IGNORECASE), "decision"),
    (re.compile(r"^pattern:\s*", re.IGNORECASE), "pattern"),
]


def detect_memory_type(content: str) -> tuple[str, str]:
    """Detect memory type from content prefix. Returns (type_prefix, clean_content)."""
    for pattern, mem_type in _TYPE_PREFIXES:
        if pattern.match(content):
            clean = pattern.sub("", content).strip()
            return f"{mem_type}: {clean}", clean
        return f"lesson: {content}", content


def sync_to_memories_db(content: str, tags: list[str]) -> None:
    """Best-effort sync to oro memories.db via CLI."""
    typed_content, _ = detect_memory_type(content)
    cmd = ["oro", "remember"]
    if tags:
        cmd.append(f"--tags={','.join(tags)}")
    cmd.append(typed_content)
    try:
        subprocess.run(cmd, capture_output=True, timeout=5)
    except (subprocess.TimeoutExpired, OSError, FileNotFoundError):
        pass  # Best-effort — knowledge.jsonl is the source of truth
```

Called after `append_entry()` in `main()`. Failure is silent — knowledge.jsonl already captured the entry. Requires adding `import subprocess` to the hook (currently not imported).

**Note:** `oro remember` supports type prefixes (`lesson:`, `gotcha:`, etc.) via `parseTypePrefix()`. The `--tags` flag must be added (Change 4 below).

### ~~Change 3: Dispatcher Startup Ingest~~ (Already Exists)

`ingestKnowledgeOnStartup()` at `cmd_start.go:403` already runs on every `oro start`, ingesting knowledge.jsonl entries into memories.db with dedup by key prefix. No new code needed — this is the "belt" to the hook sync's "suspenders."

### Change 3: Session Priming — Read from Both Sources

**File:** `.claude/hooks/session_start_extras.py`
**Change:** `recent_learnings()` also queries `oro recall` for memories.db entries

```python
def recent_memories_db(n: int = 5) -> list[dict]:
    """Fetch recent memories from oro memories.db via CLI."""
    try:
        result = subprocess.run(
            ["oro", "memories", "list", "--limit", str(n), "--format=json"],
            capture_output=True,
            text=True,
            timeout=5,
        )
        if result.returncode == 0 and result.stdout.strip():
            return json.loads(result.stdout)
    except (subprocess.TimeoutExpired, OSError, json.JSONDecodeError):
        pass
    return []
```

Merge both sources in `_format_output()`, dedup by normalized content (lowercase, strip whitespace, compare first 60 chars). Show top 5 combined.

**Dedup algorithm:** For each memories.db entry, check if any knowledge.jsonl entry has >80% character overlap in the first 60 chars. If so, skip the memories.db entry (knowledge.jsonl version is canonical since it has bead attribution).

**Note:** `oro memories list` supports `--limit` already but needs `--format=json` added.

### Change 4: `oro remember` — Add `--tags` Flag

**File:** `cmd/oro/cmd_remember.go`
**Change:** Add `--tags` flag to pass tags directly

```go
cmd.Flags().StringVar(&tags, "tags", "", "Comma-separated tags (e.g., go,sqlite,test)")
```

This enables the hook to pass auto-detected tags through to memories.db without embedding them in the content string.

### Change 5: `oro memories list` — Add `--format=json`

**File:** `cmd/oro/cmd_memories.go`
**Change:** Add `--format=json` flag for programmatic access (`--limit` already exists)

This enables session priming to query memories.db without importing SQLite in Python.

## Files Changed

| File | Change | Lines |
|------|--------|-------|
| `pkg/worker/prompt.go` | Add "Saving Learnings" section | ~15 lines added |
| `pkg/worker/prompt_test.go` | Add "Saving Learnings" to expected section headers | ~1 line |
| `.claude/hooks/memory_capture.py` | Add `subprocess` import, `detect_memory_type()`, `sync_to_memories_db()` | ~25 lines added |
| `.claude/hooks/session_start_extras.py` | Add `recent_memories_db()`, dedup merge logic | ~30 lines added |
| `cmd/oro/cmd_remember.go` | Add `--tags` flag, wire to `InsertParams.Tags` | ~5 lines added |
| `cmd/oro/cmd_memories.go` | Add `--format=json` flag | ~15 lines added |

## What We're NOT Changing

- **memories.db schema** — already correct, no migration needed
- **ParseMarker()** (`worker.go:589`) — already works, just needs input
- **ExtractImplicit()** (`worker.go:608`) — already works, just needs input
- **ExtractLearnings()** (`extractor.go:45`) — already works on event payloads
- **ForPrompt()** (`memory.go:878`) — already works, just needs data in the DB
- **Consolidation** — already runs after every 5 bead completions
- **knowledge.jsonl format** — stays as-is, remains the simple portable format
- **Dedup logic** — both systems already handle duplicates
- **Startup ingest** (`cmd_start.go:403`) — already runs `ingestKnowledgeOnStartup()`
- **Extraction pattern sets** — `extractor.go` and `memory.go` have intentionally separate patterns (different input sources)

## Premortem

### Decision: Shell out to `oro remember` from Python hook

| Category | Risk | Severity | Mitigation |
|----------|------|----------|------------|
| Tiger | `oro` binary not on PATH in hook context | Medium | Use `shutil.which("oro")` or fall back to `./oro`. Best-effort — knowledge.jsonl is the source of truth. |
| Tiger | `oro remember` fails silently, entries never reach memories.db | Low | Startup ingest (`cmd_start.go:403`) catches missed entries. Belt and suspenders. |
| Tiger | memories.db locked by concurrent workers during hook sync | Low | WAL mode + 5s busy_timeout. Hook has 5s subprocess timeout. Worst case: hook times out, startup ingest catches it. |
| Paper tiger | "Adding subprocess call slows down the hook" | — | `oro remember` is <100ms. Hook is PostToolUse, not blocking. |

### Decision: Add memory emission instructions to worker prompt

| Category | Risk | Severity | Mitigation |
|----------|------|----------|------------|
| Tiger | Workers spam low-quality memories | Medium | "Only save genuinely useful discoveries" instruction. Consolidation prunes low-confidence entries. Time decay handles the rest. |
| Tiger | Extra prompt section eats context budget | Low | ~15 lines / ~200 tokens. Workers use Sonnet with 200k context. Negligible. |
| Elephant | Workers emit so many natural-language patterns that extraction floods the DB | Low | ExtractImplicit has strict regex patterns — only exact matches like "I learned that..." at line start. Won't match casual usage. |

### Decision: Dual-source session priming

| Category | Risk | Severity | Mitigation |
|----------|------|----------|------------|
| Tiger | Same learning shows up twice (once from each source) | Medium | Dedup by normalized first-60-chars comparison (>80% overlap = skip). knowledge.jsonl version is canonical (has bead attribution). |
| Tiger | `oro memories list --format=json` doesn't exist yet | Low | Add it — small change, clearly needed for programmatic access. |

## Edge Cases

| Scenario | Behavior |
|----------|----------|
| `oro` binary not found during hook | Silent fail, knowledge.jsonl still written |
| memories.db locked during ingest | Dispatcher retries or skips — existing SQLite retry logic handles this |
| knowledge.jsonl doesn't exist | Dispatcher ingest skips (file open returns error) |
| Worker emits both `[MEMORY]` marker AND natural language for same insight | Both captured. Consolidation merges duplicates (Jaccard > 0.7). |
| 0 memories in DB | ForPrompt returns empty string. Prompt says "No prior context for this bead." Same as today. |
| Hook runs before oro binary is built | Silent fail. Startup ingest catches it next time dispatcher runs. |

## Adversarial Review Findings (Resolved)

Review found 2 critical and 5 major gaps. All addressed:

| # | Finding | Resolution |
|---|---------|------------|
| C1 | "ParseMarker will never be parsed" | **False alarm.** `worker.go:589` calls `ParseMarker()` on every stdout line. Worker struct gets memory store via `SetMemoryStore()`. Path is fully wired. |
| C2 | Design omits `extractAndStoreLearnings` in `extractor.go` | Added "Existing Extraction Paths" section documenting all 3 paths. |
| M1 | Change 3 (dispatcher ingest) redundant with `cmd_start.go:403` | Removed Change 3. Existing startup ingest is sufficient. |
| M2 | Session priming dedup unspecified | Added dedup algorithm: normalized first-60-chars, >80% overlap = skip. |
| M3 | Duplicate extraction patterns in extractor.go vs memory.go | Documented as intentional: different input sources (event payloads vs stdout). |
| M4 | `--limit` already exists on `oro memories list` | Fixed — only `--format=json` needs adding. |
| M5 | Hook hardcodes `lesson:` type | Added `detect_memory_type()` to detect gotcha/decision/pattern from content. |

## Success Criteria

After implementation, a typical swarm run should show:
1. `oro memories list` returns 10+ entries after a 30-minute run
2. Session priming "Recent Learnings" shows entries from both sources
3. Worker prompts include relevant memories from prior sessions
4. No duplicate memories accumulate (consolidation handles dedup)
