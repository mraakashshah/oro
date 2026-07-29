# Memory Quality Improvements

**Date**: 2026-02-24
**Status**: Reviewed (adversarial pass)
**Epic**: TBD

## Problem

A review of all 9 memories.db entries and 22 knowledge.jsonl entries reveals systemic quality issues:

| Issue | Severity | Evidence |
|-------|----------|----------|
| Reviewer rejection spam in DB | Critical | 7 of 9 DB entries (78%) are "Reviewer rejected this bead: ..." |
| Auto-tagger misses oro-domain keywords | Medium | 10 of 22 JSONL entries (45%) have zero tags |
| knowledge.jsonl entries truncated mid-sentence | Medium | Entries #10, #11 cut off at shell quote boundary |
| No quality gate on memory insertion | Medium | Any string goes in — no min length, no relevance check |
| DB memories lack bead attribution in list output | Low | `oro memories list` doesn't show bead_id or source |

## Design

### Change 1: Separate Rejection History from Memories

**Files**: `pkg/protocol/schema.go`, `pkg/dispatcher/dispatcher.go`, `pkg/memory/memory.go`

Create a `rejection_history` table — add to **both** `SchemaDDL` (new DBs) and a new `MigrateRejectionHistory` constant (existing DBs, following the pattern at schema.go:110-144). The migration must execute at dispatcher startup BEFORE accepting events.

```sql
CREATE TABLE IF NOT EXISTS rejection_history (
  id INTEGER PRIMARY KEY,
  bead_id TEXT NOT NULL,
  worker_id TEXT,
  feedback TEXT NOT NULL,
  created_at TEXT DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_rejection_history_bead ON rejection_history(bead_id);
```

Changes to dispatcher.go:
- `storeRejectionFeedback()` → INSERT into `rejection_history` instead of `memories`
- `buildRejectionMemoryContext()` → restructure to: (1) query `rejection_history WHERE bead_id = ?` for rejection context, (2) call `fetchBeadMemories()` for general memory context, (3) concatenate both. Currently it relies on ForPrompt finding the just-inserted rejection in `memories` — after migration, rejections live in a separate table so the function must explicitly query both sources.
- Add `Store.InsertRejection(ctx, beadID, workerID, feedback)` and `Store.GetRejections(ctx, beadID)` methods to memory.go
- Migration: first INSERT INTO rejection_history (backfill from memories), THEN DELETE FROM memories WHERE source = 'daemon_extracted' AND content LIKE 'Reviewer rejected%'

### Change 2: Expand Auto-Tagger Vocabulary

**Files**: `assets/hooks/memory_capture.py` (source of truth), then copy to `.claude/hooks/` and `~/.oro/hooks/`

Add to TAG_KEYWORDS:
```text
# oro-domain
"bead", "dispatcher", "pane", "swarm", "worker", "worktree",
# tools
"claude", "subprocess", "tmux",
# operational
"deadlock", "flaky", "race", "timeout",
```

### Change 3: Content Quality Gate in memory.Insert()

**Files**: `pkg/memory/memory.go`, `pkg/memory/memory_test.go`

Canonical type enum (update InsertParams.Type comment to match):
`lesson | decision | gotcha | pattern | preference | self_report | summary`

Add validation at the top of `Insert()`:
- Reject content < 10 characters (return error)
- Reject content > 2048 characters (return error — not silent truncation, for caller visibility)
- Validate type against canonical enum (including `preference` — used in existing tests)
- Update the stale comment at InsertParams.Type to list the full enum

### Change 4: Add Min-Length Check to memory_capture.py

**Files**: `assets/hooks/memory_capture.py` (source of truth), then copy to `.claude/hooks/` and `~/.oro/hooks/`

Add minimum length check in `extract_learned()`:
```python
if len(content) < 10:
    return None  # likely truncated by shell quoting
```

Note: the worker prompt (savingLearningsBody in prompt.go) uses [MEMORY] markers, NOT `bd comments add`. The `bd comments add LEARNED:` path is a separate capture mechanism via the PostToolUse hook. No prompt changes needed — the fix is the length guard in the hook itself.

### Change 5: Include bead_id and source in JSON Output

**Files**: `cmd/oro/cmd_memories.go`, `cmd/oro/cmd_memories_test.go`

Verified: `protocol.Memory` already has `BeadID` and `Source` fields (protocol/tables.go:56-70). `listSQL` already SELECTs them. `scanMemory` already scans them. Only change needed:

Update `memoryJSONRecord` struct and `formatMemoriesJSON` mapping:
```go
type memoryJSONRecord struct {
    ID         int64   `json:"id"`
    Type       string  `json:"type"`
    Content    string  `json:"content"`
    Confidence float64 `json:"confidence"`
    CreatedAt  string  `json:"created_at"`
    BeadID     string  `json:"bead_id,omitempty"`
    Source     string  `json:"source,omitempty"`
}
```

## Non-Goals

- Changing the JSONL format or schema
- Adding a memory admin UI
- Changing the search/scoring algorithm (FTS5 + Jaccard + time decay works — problem is input quality)
- Adding memory consolidation triggers
- Modifying the worker prompt's [MEMORY] marker section

## Ordering & Dependencies

Changes 1-5 are all independent. Change 1 is highest impact (removes 78% of junk). Changes 2-5 are quick wins.

## Risks

- **Change 1 migration**: Mitigated by backfilling rejection_history BEFORE deleting from memories.
- **Change 3 length check**: "always use TDD" (14 chars) passes the 10-char minimum. Existing test with type="preference" passes because we include it in the enum.
- **Change 3 max-length error**: Dispatcher callers discard Insert errors (`_, _  = ...`) so >2048 rejections are silent there — acceptable since dispatcher content should be reasonable. CLI callers (`oro remember`) will see the error.
