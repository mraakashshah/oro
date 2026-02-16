# Knowledge Ingest: One-Way Bridge from Human Sessions to Workers

**Date:** 2026-02-15
**Status:** Proposed
**Bead:** TBD

## Problem

Oro has two parallel learning systems that don't communicate:

| System | Storage | Writers | Readers |
|--------|---------|---------|---------|
| Go memory store | `~/.oro/state.db` (SQLite FTS5) | Dispatcher, workers | Dispatcher (injects into worker prompts) |
| Python hooks | `.beads/memory/knowledge.jsonl` | Interactive Claude sessions (`memory_capture.py`) | `session_start_extras.py`, `learning_reminder.py` |

Human-discovered learnings (e.g., "rebase fails if branch checked out in another worktree") sit in `knowledge.jsonl` and never reach workers. Workers re-learn things the human already figured out.

## Solution

One-way bridge: import `knowledge.jsonl` entries into the Go memory store at dispatcher startup. Human learnings flow to workers. Workers don't pollute `knowledge.jsonl`.

## Design

### New function: `IngestKnowledge()`

**Package:** `pkg/memory`
**File:** `ingest.go`

```go
func IngestKnowledge(ctx context.Context, store *Store, r io.Reader) (int, error)
```

- Reads JSONL from `r` (one JSON object per line)
- Deduplicates by `key` field (skip if key already ingested)
- Maps each entry to `InsertParams` and calls `Store.Insert()`
- Returns count of newly inserted entries

### JSONL → SQLite mapping

| JSONL field | SQLite column | Transformation |
|-------------|---------------|----------------|
| `content` | `content` | Direct |
| `type` (`"learned"`) | `type` | Map to `"lesson"` |
| `tags` (array) | `tags` | JSON marshal |
| `bead` | `bead_id` | Direct |
| `ts` | `created_at` | Parse ISO 8601, reformat to `"2006-01-02 15:04:05"` |
| — | `source` | `"knowledge_import"` |
| — | `confidence` | `0.9` (human-sourced, high trust) |
| — | `pinned` | `false` |

### Dedup strategy

The JSONL `key` field (e.g., `"learned-never-chain-rebasemerge-for-..."`) has no analog in the SQLite schema. Rather than adding a column, store the key in the `source` field as `"knowledge_import:<key>"`. Before inserting, query for existing rows where `source LIKE 'knowledge_import:%'` and compare keys. This is cheap (small result set) and avoids schema migration.

The existing `Store.Insert()` Jaccard dedup (threshold 0.7) provides a second safety net — even if the key check fails, near-duplicate content won't create a second row.

### CLI command: `oro ingest`

```
oro ingest [--file <path>] [--dry-run]
```

- `--file`: Path to knowledge.jsonl. Default: discover via project config (`.oro/config.yaml` → `project_root` → `.beads/memory/knowledge.jsonl`)
- `--dry-run`: Print what would be ingested without writing
- Writes to `state.db` (same DB the dispatcher uses)
- Prints: `Ingested 3 new learnings (6 skipped, already imported)`

### Wiring into dispatcher startup

In `cmd/oro/cmd_start.go`, after opening `state.db` and before starting the dispatcher loop:

```go
if knowledgePath != "" {
    f, err := os.Open(knowledgePath)
    if err == nil {
        defer f.Close()
        n, _ := memory.IngestKnowledge(ctx, memStore, f)
        if n > 0 {
            log.Printf("Ingested %d new learnings from knowledge.jsonl", n)
        }
    }
}
```

Best-effort: missing file or read errors don't block startup.

### Knowledge file discovery

Resolution order:
1. `--file` flag (CLI only)
2. `ORO_KNOWLEDGE_FILE` env var
3. `<project_root>/.beads/memory/knowledge.jsonl` (from config)

For dispatcher startup, use options 2 and 3 (no flag available).

## What this does NOT do

- Does not write worker learnings back to `knowledge.jsonl` (worker → human direction)
- Does not unify `memories.db` and `state.db` (separate concern)
- Does not change the Python hooks in any way
- Does not add a new SQLite column for the JSONL key

## Testing

- `TestIngestKnowledge`: parse valid JSONL, verify InsertParams mapping
- `TestIngestKnowledge_Dedup`: run twice, second run inserts zero
- `TestIngestKnowledge_EmptyFile`: returns 0, no error
- `TestIngestKnowledge_MalformedLines`: skips bad lines, ingests good ones
- `TestIngestKnowledge_TypeMapping`: `"learned"` → `"lesson"`
- CLI: `TestCmdIngest_DryRun`, `TestCmdIngest_Integration`
