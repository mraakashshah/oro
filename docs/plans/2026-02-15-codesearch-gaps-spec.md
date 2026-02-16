# Code Search Gaps: Index Build, Test Fix, Dispatcher Context

**Date:** 2026-02-15
**Status:** Proposed

## Context

The code search system (`pkg/codesearch/`) is fully implemented but partially wired:
- The Read hook (`oro-search-hook`) is live and saving tokens on every session
- `oro index build` / `oro index search` CLI commands exist but the index has never been built
- The Grep router/classifier is implemented but not wired (intentionally deferred)
- `TestHookEndToEnd/settings_json_registration` is broken after config externalization
- `ast-grep` is already in `oro init --check` (no work needed)

This spec addresses three gaps: auto-building the index, fixing the broken test, and injecting relevant code context into worker prompts.

---

## 1. Wire `oro index build` into `oro start`

### Goal
Build the FTS5 code search index at dispatcher startup so it's always fresh.

### Design

In `cmd/oro/cmd_start.go`, inside `buildDispatcher()`, after `migrateStateDB(db)` (line 331) and before `dispatcher.New()` (line 354):

```go
// Best-effort: build code search index. Never blocks startup.
go func() {
    idxPath := defaultIndexDBPath() // ~/.oro/code_index.db
    idx, err := codesearch.NewCodeIndex(idxPath, nil)
    if err != nil {
        log.Printf("code index: skip (open failed: %v)", err)
        return
    }
    defer idx.Close()
    stats, err := idx.Build(context.Background(), repoRoot)
    if err != nil {
        log.Printf("code index: skip (build failed: %v)", err)
        return
    }
    log.Printf("code index: indexed %d chunks from %d files in %s",
        stats.ChunksIndexed, stats.FilesProcessed, stats.Duration.Round(time.Millisecond))
}()
```

- Runs in a goroutine so dispatcher startup is never blocked
- Uses `repoRoot` (already available from `os.Getwd()` at line 334)
- `defaultIndexDBPath()` already exists in `cmd_index.go` → `~/.oro/code_index.db`
- Errors are logged, never fatal

### Testing

- `TestBuildDispatcher_BuildsIndex`: verify `code_index.db` is created after `buildDispatcher()` completes
- Reuse existing `pkg/codesearch` tests for Build correctness (already passing)

---

## 2. Fix `TestHookEndToEnd/settings_json_registration`

### Problem

The integration test in `cmd/oro-search-hook/integration_test.go` looks for the hook registration at:
```
<repo_root>/.claude/settings.json
```

But after config externalization (oro-etu3), settings live at:
```
~/.oro/projects/<name>/settings.json
```

The project's `.claude/` directory only contains `settings.local.json` (MCP permissions).

### Design

Convert the sub-test from a filesystem assertion to a unit-level assertion:

1. Extract the hook config shape into a test constant or golden fixture
2. Instead of reading a file from disk, call the hook config builder directly (or assert against a known-good JSON structure)
3. If package visibility is an issue (the test is `package main_test`), use an exported test helper or move the sub-test into `package main`

Alternatively (simpler): read from the externalized path using `ORO_HOME` env var resolution:

```go
t.Run("settings_json_registration", func(t *testing.T) {
    oroHome := os.Getenv("ORO_HOME")
    if oroHome == "" {
        home, _ := os.UserHomeDir()
        oroHome = filepath.Join(home, ".oro")
    }
    project := os.Getenv("ORO_PROJECT")
    if project == "" {
        t.Skip("ORO_PROJECT not set, skipping settings registration check")
    }
    settingsPath := filepath.Join(oroHome, "projects", project, "settings.json")
    // ... rest of test unchanged
})
```

This skips in CI (where `ORO_PROJECT` isn't set) and works locally where the settings exist.

### Testing

The fix IS the test. Verify it passes with `go test ./cmd/oro-search-hook/ -run TestHookEndToEnd/settings_json_registration`.

---

## 3. Dispatcher injects code search context into worker prompts

### Goal

When the dispatcher assigns a bead, search the code index for chunks relevant to the bead title and inject them into the worker prompt alongside memory context.

### Design

**New field in `AssignPayload`** (`pkg/protocol/message.go`):
```go
CodeSearchContext string `json:"code_search_context,omitempty"`
```

**New field in `PromptParams`** (`pkg/worker/prompt.go`):
```go
CodeSearchContext string
```

**New prompt section** in `AssemblePrompt()` — after `## Memory`, before `## Coding Rules`:
```go
// 3b. Relevant Code
if params.CodeSearchContext != "" {
    section(&b, "Relevant Code", params.CodeSearchContext)
}
```

**Dispatcher search** in `assignBead()` (`pkg/dispatcher/dispatcher.go`), after the `memory.ForPrompt()` call (line 1597), outside the lock:

```go
var codeCtx string
if d.codeIndex != nil {
    results, err := d.codeIndex.FTS5Search(ctx, bead.Title, 5)
    if err == nil && len(results) > 0 {
        codeCtx = formatCodeResults(results)
    }
}
```

`formatCodeResults` produces a compact markdown listing: file path, function name, first few lines of each chunk. Same pattern as `memory.ForPrompt()` — compact, token-efficient, with pointers to full content.

**FTS5 only, no reranker.** The dispatcher runs in a daemon process. Spawning `claude -p` for reranking on every bead assignment adds latency and cost. FTS5 BM25 scoring is good enough for "find code related to this bead title." If reranking is needed later, it's additive.

**Dispatcher gets the index** — pass `*codesearch.CodeIndex` into `dispatcher.New()`:

```go
func New(cfg Config, db *sql.DB, ..., codeIdx *codesearch.CodeIndex) (*Dispatcher, error)
```

This is opened in `buildDispatcher()` alongside the memory store. If the index doesn't exist (hasn't been built yet), pass `nil` — the dispatcher handles nil gracefully (same pattern as `d.memories`).

### Testing

- `TestAssignBead_InjectsCodeContext`: mock code index, verify `AssignPayload.CodeSearchContext` is populated
- `TestAssemblePrompt_CodeSearchSection`: verify prompt includes `## Relevant Code` when context is non-empty, omits it when empty
- `TestFormatCodeResults`: verify compact markdown output format

---

## Dependency graph

```
[1] Wire index build into oro start
         │
         └──▶ [3] Dispatcher injects code search context
                    (needs index to exist)

[2] Fix TestHookEndToEnd
         (independent)
```

Bead 2 is independent and can be done in parallel with beads 1 and 3.
