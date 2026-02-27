# Memory & Code Search Activation

**Date:** 2026-02-27
**Status:** REVIEWED (adversarial pass — gaps fixed)

## Problem

Three related gaps in Oro's memory and code search systems:

### 1. Per-project DB storage missing

All DBs live in global `~/.oro/` (`state.db`, `code_index.db`). The `project` column on the `memories` table provides soft scoping (defaults to `"oro"` for everything), but the code index has no project scoping at all. Running Oro on a second project would overwrite the code index and leak memories across projects.

**Evidence:** `cmd/oro/paths.go:40-47` — `ResolvePaths()` resolves all paths under `~/.oro/` with no project awareness. The config externalization design (2026-02-15) moved hooks/skills/settings to `~/.oro/projects/<name>/` but left DBs global.

### 2. `oro work` bypasses memory and code search

The `oro work` design spec (2026-02-16) explicitly listed "Step 4: Fetch memory context + code search context" but it was never implemented. `newProductionDeps()` (`cmd_work.go:109-125`) creates no memory store or code index. `spawnAndWait()` (`cmd_work.go:345-355`) passes empty `MemoryContext` and `CodeSearchContext` to `AssemblePrompt`. `DrainOutput` (`cmd_work.go:371`) receives `nil` for the store, so `[MEMORY]` markers from workers are silently dropped.

Meanwhile, the dispatcher path (`cmd_start.go:417-449`, `dispatcher.go:2206-2238`) fully wires both systems.

### 3. Memory pipeline produces near-zero signal

DB contains 10 memories: 9 are reviewer rejection feedback (`daemon_extracted`), 1 is a manual CLI test (`cli`). Zero from worker `[MEMORY]` markers. Zero from implicit extraction (`ExtractImplicit`).

**Root causes:**
- `DrainOutput` (`drain.go:20`) only extracts `[MEMORY]` markers, not implicit patterns. Workers would need to literally write `[MEMORY] type=lesson tags=go: ...` — they almost never do.
- The dispatcher's `extractAndStoreLearnings` (`extractor.go`) runs on event payloads, but its primary output is reviewer rejection feedback stored as `gotcha` type memories.
- `oro work` passes `nil` store to `DrainOutput`, so even if workers did emit markers, they'd be dropped.

**Live pipeline (NOT dead code):** `savingLearningsBody()` in `prompt.go:42-53` tells workers to record in `.oro/learnings.json`. This IS read — `worker.go:1041` reads it via `readJSONStringSlice()` during handoff, and the dispatcher persists via `persistHandoffContext()`. However, this pipeline produces zero entries because workers don't actually write the file.

## Design

### Epic 1: Per-project DB paths

**Goal:** DB paths (`StateDBPath`, `CodeIndexDBPath`) resolve to `~/.oro/projects/<name>/` instead of `~/.oro/`. Daemon control paths (`PIDPath`, `SocketPath`) remain global.

**Approach:** Add a new `ResolveProjectDBPaths()` function that reads project name from either `ORO_PROJECT` env var or `.oro/config.yaml` in the current directory, then resolves DB paths under `~/.oro/projects/<name>/`. Leave `ResolvePaths()` unchanged for daemon control commands.

**Why not modify ResolvePaths():** `ResolvePaths()` has 20+ callers across `cmd_start.go` (4x), `cmd_stop.go`, `cmd_status.go`, `cmd_logs.go`, `cmd_index.go`, `cmd_worker.go`, `cmd_directive.go`, etc. Many need global PID/socket paths to find the running daemon. Changing `ResolvePaths()` would break `oro stop`, `oro status`, etc.

**Resolution order for project name:**
1. `ORO_PROJECT` env var (set by dispatcher for workers, and by `oro work`)
2. `.oro/config.yaml` in CWD (project root)
3. Empty string → fall back to global `~/.oro/` paths

**Why ORO_PROJECT first:** `oro work` runs from worktrees where `.oro/config.yaml` doesn't exist (it's gitignored). The env var must be authoritative. `oro work` will set `ORO_PROJECT` from the project root's config before entering the worktree.

**Files:**
- `cmd/oro/paths.go` — add `ResolveProjectDBPaths()`, add `readProjectName()` helper
- `cmd/oro/paths_test.go` — tests for per-project resolution, ORO_PROJECT priority, fallback to global
- `cmd/oro/store.go` — change `defaultMemoryStore()` to use `ResolveProjectDBPaths().StateDBPath`
- `cmd/oro/cmd_start.go` — change `buildDispatcher()` to use `ResolveProjectDBPaths()` for DB paths, keep `ResolvePaths()` for PID/socket
- `cmd/oro/cmd_index.go` — use `ResolveProjectDBPaths().CodeIndexDBPath`
- `cmd/oro/cmd_logs.go` — use `ResolveProjectDBPaths().StateDBPath`
- `cmd/oro/cmd_worker.go` — use `ResolveProjectDBPaths().StateDBPath`

**Callers that stay on `ResolvePaths()` (daemon control):** `cmd_stop.go`, `cmd_status.go`, `cmd_directive.go`, `cmd_dispatcher.go`, `cmd_worker_launch.go`, `cmd_worker_stop.go`, `cmd_cleanup.go`.

**Remove `MemoryDBPath`:** The `MemoryDBPath` field (memories.db) is dead code — all production code uses `StateDBPath` for the memory store. Remove it from `Paths` struct to prevent future confusion.

**Migration:** On first `ResolveProjectDBPaths()` call for a project, if the per-project directory doesn't exist but the global DB does, copy `~/.oro/state.db` → `~/.oro/projects/<name>/state.db` and `~/.oro/code_index.db` → `~/.oro/projects/<name>/code_index.db`. Both DBs remain at the global path as fallback.

**Premortem:**
| Category | Risk | Mitigation |
|----------|------|------------|
| Tiger | Running dispatcher holds global state.db open; new CLI commands read per-project DB — split brain during transition | Migration copies data. Dispatcher restart picks up new path. Document: path changes take effect at process startup. |
| Tiger | `oro work` runs from worktree where no `.oro/config.yaml` exists | `oro work` reads project name from CWD before creating worktree, sets `ORO_PROJECT` env var for the duration |
| Tiger | `oro recall`/`oro remember` from a non-project directory resolves global path | Acceptable — global is the fallback. User gets a warning if no project context detected. |
| Paper tiger | "Breaking change for existing users" | Fallback to global means zero breakage |

### Epic 2: Wire memory + code search into `oro work`

**Goal:** `oro work` gets memory recall, code search context, and memory capture — parity with the dispatcher path.

**Depends on:** Epic 1 (needs correct per-project DB path)

**Approach:** Add `memStore` and `codeIndex` fields to `workDeps`. Initialize them in `newProductionDeps()` (best-effort — nil on failure). Set `ORO_PROJECT` env var from project config before worktree creation. Pass context to `AssemblePrompt`. Pass store to `DrainOutput`.

**Files:**
- `cmd/oro/cmd_work.go` — add deps fields, init in `newProductionDeps()`, wire through `spawnAndWait()`
- `cmd/oro/cmd_work_test.go` — update mock deps, test memory/code context reaches prompt, test DrainOutput captures markers

**Design detail:**

```go
// workDeps additions:
type workDeps struct {
    // ... existing fields ...
    memStore  *memory.Store         // nil-safe, best-effort
    codeIndex *codesearch.CodeIndex // nil-safe, best-effort
}

// In newProductionDeps():
paths, _ := ResolveProjectDBPaths()
if paths != nil {
    if db, err := openDB(paths.StateDBPath); err == nil {
        deps.memStore = memory.NewStore(db)
        // ... schema init ...
    }
    if idx, err := codesearch.NewCodeIndex(paths.CodeIndexDBPath, nil); err == nil {
        deps.codeIndex = idx
    }
}

// In executeWork(), before worktree creation:
// Set ORO_PROJECT so workers spawned in the worktree resolve the same DB
projectName := readProjectName()
if projectName != "" {
    os.Setenv("ORO_PROJECT", projectName)
}

// In spawnAndWait():
var memCtx string
if deps.memStore != nil {
    memCtx, _ = memory.ForPrompt(ctx, deps.memStore, nil, cfg.bead.Title, 0)
}
var codeCtx string
if deps.codeIndex != nil {
    results, _ := deps.codeIndex.Search(ctx, cfg.bead.Title, 5)
    if len(results) > 0 {
        codeCtx = codesearch.FormatResults(results)
    }
}

// In stdout drain:
worker.DrainOutput(ctx, stdout, deps.memStore, cfg.beadID, writers...)
```

**Code search result formatting:** The dispatcher uses an unexported `formatSearchResults()` at `dispatcher.go:3558`. Rather than export it or duplicate it, add `FormatResults([]SearchResult) string` to `pkg/codesearch/` — this is a better home for it since it formats code search results. The dispatcher can call it too (future cleanup, not in scope).

**Premortem:**
| Category | Risk | Mitigation |
|----------|------|------------|
| Tiger | DB locked by concurrent dispatcher while `oro work` reads | WAL mode + busy_timeout. Both are read-mostly operations. |
| Tiger | Code index DB doesn't exist (never ran `oro start` or `oro index build`) | Graceful nil — code search context is empty, worker proceeds without it |
| Tiger | `codeIndex.Search()` with reranker spawns a Claude subprocess for reranking | Pass `nil` reranker for `oro work` — use FTS5-only search. Reranking is a dispatcher optimization, not essential. |
| Paper tiger | "Slows down `oro work` startup" | DB open + FTS5 query is <50ms |

### Epic 3: Memory signal quality

**Goal:** The memory pipeline captures useful learnings, not just reviewer rejections.

**Depends on:** Epic 1 (needs correct DB to capture into). Epic 3b has a soft dependency on Epic 2 — implicit extraction in DrainOutput is only useful for `oro work` if Epic 2 passes a non-nil store.

**Sub-problems and changes:**

#### 3a. Separate rejection history from memories

**Files:** `pkg/protocol/schema.go`, `pkg/dispatcher/dispatcher.go`, `pkg/memory/memory.go`, `pkg/memory/memory_test.go`

Create `rejection_history` table (both in `SchemaDDL` for new DBs and as a migration for existing DBs). Changes to dispatcher.go:

1. `storeRejectionFeedback()` → INSERT into `rejection_history` instead of `memories`
2. `buildRejectionMemoryContext()` → restructure to: (a) insert into `rejection_history`, (b) query `rejection_history WHERE bead_id = ?` for rejection context, (c) call `fetchBeadMemories()` for general memory context, (d) concatenate both. Currently it relies on `ForPrompt` finding the just-inserted rejection in `memories` — after migration, rejections live in a separate table so the function must explicitly query both sources.
3. Add `InsertRejection(ctx, beadID, workerID, feedback)` and `GetRejections(ctx, beadID)` methods to memory.go
4. Migration: INSERT INTO rejection_history (backfill from memories WHERE content LIKE 'Reviewer rejected%'), THEN DELETE those rows from memories.

#### 3b. Add implicit extraction to DrainOutput

**Files:** `pkg/worker/drain.go`, `pkg/worker/drain_test.go`

Currently `DrainOutput` only extracts `[MEMORY]` markers. Add line-by-line implicit extraction during the scan loop (NOT post-scan batch to avoid memory pressure from large outputs):

```go
for scanner.Scan() {
    line := scanner.Text()
    // ... echo to writers ...

    if store != nil {
        // Explicit [MEMORY] markers
        if params := memory.ParseMarker(line); params != nil {
            params.BeadID = beadID
            _, _ = store.Insert(ctx, *params)
        }
        // Implicit patterns ("I learned...", "Gotcha:", etc.)
        for _, p := range memory.ExtractImplicit(line) {
            p.BeadID = beadID
            p.Source = "worker_implicit"  // distinguish from daemon_extracted
            _, _ = store.Insert(ctx, p)
        }
    }
}
```

Use source `"worker_implicit"` (not `"daemon_extracted"`) since this runs in the worker/CLI process, not the daemon.

**Soft dependency on Epic 2:** Without Epic 2, this only benefits the dispatcher path (which already has `extractAndStoreLearnings` covering similar patterns). With Epic 2, `oro work`'s DrainOutput gets a non-nil store and can capture implicit learnings.

#### 3c. Fix `.oro/learnings.json` prompt reference

**Files:** `pkg/worker/prompt.go`, `pkg/worker/prompt_test.go`

**NOT removing the learnings.json reference** — adversarial review found that `worker.go:1041` reads `.oro/learnings.json` during handoff via `readJSONStringSlice()`, and the dispatcher persists these via `persistHandoffContext()`. This pipeline is live, not dead code.

Instead, fix the prompt to be accurate: the instruction says "Record these in `.oro/learnings.json` (array of strings). The next worker will see them in their Memory section." This is misleading — the next worker doesn't read learnings.json directly; the dispatcher does via handoff. Update the instruction to be clear about what actually happens, and emphasize `[MEMORY]` markers + natural language as the primary capture paths.

Updated `savingLearningsBody()`:
```go
return strings.Join([]string{
    "As you work, capture learnings for future sessions. Three methods:",
    "",
    "**Natural language** (just write normally — these are extracted automatically):",
    "  I learned that the FTS5 trigger must be on INSERT only",
    "  Gotcha: ruff --fix must run BEFORE pyright or types break",
    "  Note: the dispatcher retries with exponential backoff",
    "  Decision: use table-driven tests for the parser package",
    "",
    "**Explicit markers** (for structured entries):",
    "  [MEMORY] type=gotcha tags=sqlite: WAL mode required for concurrent writes",
    "  [MEMORY] type=lesson tags=go,test: table-driven tests catch edge cases",
    "",
    "Types: lesson, decision, gotcha, pattern",
    "Only save genuinely useful discoveries — not obvious facts.",
}, "\n")
```

This removes the `.oro/learnings.json` reference (which workers don't actually write to) and emphasizes the two paths that DO capture: implicit extraction and `[MEMORY]` markers. The handoff pipeline continues to work if a worker happens to write learnings.json, but we don't instruct them to.

**Test update needed:** `prompt_test.go` has `TestAssemblePrompt_SavingLearningsSection` that asserts the presence of `learnings.json` content — must be updated.

#### ~~3d. Content quality gate on memory insertion~~

**DROPPED.** Adversarial review found this is already implemented: `memory.go:198-206` already rejects content <10 chars, >2048 chars, and validates types against the canonical enum. This would be a no-op bead.

**Premortem:**
| Category | Risk | Mitigation |
|----------|------|------------|
| Tiger | Implicit extraction produces noise from chatty worker output | Patterns are strict (line-start anchored: `^Note:`, `^Gotcha:`, `I learned that`). Only exact matches extracted. |
| Tiger | Rejection history migration loses data | Backfill rejection_history BEFORE deleting from memories. Migration is additive then subtractive. |
| Tiger | buildRejectionMemoryContext restructuring breaks rejection feedback loop | Test that rejection context includes both rejection_history entries AND general memories. Dispatcher test already covers this flow. |

## Dependency Graph

```
Epic 1: Per-project DB paths
    ↓
    ├── Epic 2: Wire memory + code search into oro work
    │
    └── Epic 3: Memory signal quality
            ├── 3a: Separate rejection history
            ├── 3b: Add implicit extraction to DrainOutput (soft dep on Epic 2)
            └── 3c: Fix learnings.json prompt reference
```

Epics 2 and 3 are independent of each other but both depend on Epic 1.
Within Epic 3, sub-tasks 3a-3c are independent of each other.
Epic 3d dropped (already implemented).

## What We're NOT Changing

- `ResolvePaths()` function (daemon control commands keep using it unchanged)
- FTS5/BM25 search algorithm (works fine — problem is input quality)
- Consolidation logic (works, just has nothing to consolidate)
- Code index build pipeline (`oro index build` / background build in `oro start`)
- knowledge.jsonl format or beads-level capture hooks
- Dispatcher-side memory wiring (already correct)
- The `learnings.json` → handoff → `persistHandoffContext()` pipeline (live code, not dead)

## Adversarial Review Findings (Resolved)

| # | Severity | Finding | Resolution |
|---|----------|---------|------------|
| C1 | Critical | `.oro/learnings.json` is NOT dead — `worker.go:1041` reads it during handoff | Changed 3c from "remove" to "fix reference". Keep handoff pipeline alive. |
| C2 | Critical | `formatSearchResults` is unexported in `pkg/dispatcher`, uses wrong type | Create `codesearch.FormatResults()` in `pkg/codesearch/`. Use `Search()` not `FTS5Search()`. |
| C3 | Critical | `ResolvePaths()` has 20+ callers — changing it breaks daemon control | Added `ResolveProjectDBPaths()` as separate function. `ResolvePaths()` unchanged. |
| C4 | Critical | `oro work` from worktree has no `.oro/config.yaml` or `ORO_PROJECT` | `oro work` reads project name before worktree creation, sets `ORO_PROJECT` env var. Resolution order: env var > config file > fallback. |
| M1 | Major | `workDeps` struct needs memory/code fields | Added to design. |
| M2 | Major | Post-scan accumulation in DrainOutput wastes memory | Changed to line-by-line implicit extraction. |
| M3 | Major | `buildRejectionMemoryContext` restructuring not specified | Added explicit 4-step restructuring to 3a. |
| M4 | Major | `MemoryDBPath` is dead code | Remove it from Paths struct. |
| M5 | Minor | `daemon_extracted` source misleading for worker-side extraction | Use `worker_implicit` source. |
| M6 | Minor | Epic 3d is already implemented | Dropped. |
| M7 | Minor | 3b only useful for `oro work` with Epic 2 | Documented as soft dependency. |
