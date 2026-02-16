# Codebase Hardening: Tier 1 & Tier 2 Improvements

> **For Claude:** Use executing-plans skill to implement this plan task-by-task.

**Goal:** Fix correctness bugs (unbounded memory load, non-atomic rebuild), eliminate highest-value duplication (config paths, FTS5 sanitizer), and close silent-failure gaps.
**Architecture:** Ten independent improvements grouped into two tiers. Tier 1 fixes correctness and eliminates cross-cutting duplication. Tier 2 improves robustness and code health. All changes are backward-compatible. No new packages — use existing structure.
**Tech Stack:** Go, SQLite FTS5, cobra CLI

---

## Tier 1 — High Impact

### 1. Centralize path/config resolution (cmd/oro)

**Problem:** Magic strings `"oro.pid"`, `"oro.sock"`, `"state.db"`, `"memories.db"`, `"code_index.db"` repeated 50+ times across cmd/oro/. `oroPath()` reinvented in each command. Env vars (`ORO_HOME`, `ORO_PID_PATH`, `ORO_SOCKET_PATH`, `ORO_DB_PATH`, `ORO_MEMORY_DB`) scattered across 7 files with no unified access.

**Current state:**
- `oroPath(envKey, defaultSuffix)` in `cmd_start.go:303` — works for PID/socket/DB
- `defaultMemoryStore()` in `store.go:15` — reimplements same pattern for memory DB
- `defaultOroDir()` in `cmd_start.go:284` — another reimplementation
- `defaultIndexDBPath()` in `cmd_index.go:157` — yet another

**Design:**
- Create `cmd/oro/paths.go` with constants and a `Paths` struct:
  ```go
  const (
      PIDFile    = "oro.pid"
      SocketFile = "oro.sock"
      StateDB    = "state.db"
      MemoryDB   = "memories.db"
      IndexDB    = "code_index.db"
  )

  type Paths struct {
      PID    string
      Socket string
      State  string
      Memory string
      Index  string
      OroDir string
  }

  func ResolvePaths() (Paths, error)
  ```
- `ResolvePaths()` checks env overrides first, then falls back to `~/.oro/<file>`
- Replace all `oroPath()` call sites with `ResolvePaths()` (or cache the result per command)
- Delete `oroPath()`, `defaultOroDir()`, `defaultIndexDBPath()` after migration

**Files:**
- Create: `cmd/oro/paths.go`, `cmd/oro/paths_test.go`
- Modify: `cmd/oro/cmd_start.go`, `cmd/oro/cmd_stop.go`, `cmd/oro/cmd_status.go`, `cmd/oro/cmd_cleanup.go`, `cmd/oro/cmd_directive.go`, `cmd/oro/cmd_index.go`, `cmd/oro/store.go`, `cmd/oro/daemon.go`

**Risk:** High file count but purely mechanical replacement. Tests catch regressions.

---

### 2. Bound vectorSearch() memory usage (pkg/memory)

**Problem:** `vectorSearch()` at `memory.go:368` runs `SELECT ... FROM memories WHERE embedding IS NOT NULL` with NO LIMIT. Loads every memory with an embedding into RAM, computes cosine similarity for all, sorts, then truncates. Will OOM as memory DB grows.

**Current state:** `memory.go:373-382` — unbounded query. `memory.go:401-413` — iterates all rows into `[]scored` slice. `memory.go:419-424` — sorts and truncates after the fact.

**Design:**
- Add SQL-side `LIMIT` as a safety cap (e.g., 1000 rows, ordered by `created_at DESC` for recency bias)
- The LIMIT caps memory footprint; cosine similarity is still computed client-side (SQLite has no native vector ops)
- Add `const maxVectorCandidates = 1000` in memory.go
- Modify query: `... WHERE embedding IS NOT NULL ORDER BY created_at DESC LIMIT ?`
- Pass `maxVectorCandidates` as arg

**Files:**
- Modify: `pkg/memory/memory.go:vectorSearch`
- Test: `pkg/memory/memory_test.go` — add `TestVectorSearch_BoundsMemory`

**Edge:** If user has >1000 memories with embeddings, older ones won't be considered. Acceptable tradeoff — recency bias is desirable for code memories.

---

### 3. Wrap index rebuild in transaction (pkg/codesearch)

**Problem:** `Build()` at `index.go:120` runs `DELETE FROM chunks` then walks and inserts. Not wrapped in a transaction. Concurrent FTS5Search readers see empty index during rebuild.

**Current state:** `index.go:124` — bare DELETE. `index.go:129` — bare FTS5 rebuild command. `index.go:135-148` — walk and insert without transaction.

**Design:**
- Wrap the entire `Build()` body (DELETE + rebuild + walk/insert) in `BEGIN ... COMMIT`
- Use `ci.db.BeginTx(ctx, nil)` and pass `tx` to `indexFile()` instead of `ci.db`
- Rollback on error, commit on success
- Readers using a separate connection will see the old data until COMMIT (SQLite WAL provides this isolation)

**Files:**
- Modify: `pkg/codesearch/index.go:Build`, `pkg/codesearch/index.go:indexFile`
- Test: `pkg/codesearch/index_test.go` — add `TestBuild_AtomicRebuild` (verify search works during rebuild)

**Edge:** `indexFile()` currently takes `ci.db` implicitly via the receiver. Need to either pass `tx` explicitly or temporarily swap `ci.db`. Prefer explicit `tx` parameter.

**Premortem note:** Introduce `type dbExecer interface { ExecContext(ctx, query, args...) }` so `insertChunk` can accept either `*sql.DB` or `*sql.Tx`. Clean approach, no receiver swap needed.

---

### 4. Extract shared sanitizeFTS5Query to pkg/protocol

**Problem:** Identical `sanitizeFTS5Query()` function in both `pkg/codesearch/index.go:320` and `pkg/memory/memory.go:526`. Tests only exist in memory package.

**Current state:** Both are unexported, ~15 lines each, identical logic: split on whitespace, strip non-alphanumeric except hyphens, wrap in quotes, join with OR.

**Design:**
- Add exported `SanitizeFTS5Query()` to `pkg/protocol/` (both packages already import protocol — avoids a new package for one function)
- Move test from `memory_test.go:1296` to `pkg/protocol/`
- Replace both call sites
- Delete the two unexported copies

**Premortem note:** Originally considered `pkg/fts/` but creating a package for one function is over-engineering. `pkg/protocol` is already a shared dependency.

**Files:**
- Create: `pkg/protocol/fts.go`, `pkg/protocol/fts_test.go`
- Modify: `pkg/codesearch/index.go`, `pkg/memory/memory.go`

---

### 5. Track zombie reaper goroutine in process_manager

**Problem:** `process_manager.go:102` spawns `go func() { _ = cmd.Wait() }()` — this goroutine is not tracked by any WaitGroup. If the process never exits, the goroutine leaks. On shutdown, there's no way to verify all reapers completed.

**Current state:** The dispatcher uses `d.wg` (via `safeGo`) for all its goroutines. But `ExecProcessManager.Spawn()` uses bare `go func()`.

**Design:**
- Add `wg sync.WaitGroup` field to `ExecProcessManager`
- In `Spawn()`, use `pm.wg.Add(1)` before the goroutine, `pm.wg.Done()` in defer inside the goroutine
- Add `Wait()` method: `func (pm *ExecProcessManager) Wait() { pm.wg.Wait() }`
- Call `pm.Wait()` during dispatcher shutdown (after killing all workers)

**Files:**
- Modify: `pkg/dispatcher/process_manager.go`
- Test: `pkg/dispatcher/process_manager_test.go` — add `TestSpawn_ReaperTracked`

---

## Tier 2 — Medium Impact

### 6. Add LIMIT to mergeDuplicates O(n²) loop (pkg/memory)

**Problem:** `mergeDuplicates()` at `memory.go:992` lists up to 1000 memories, then for each one calls `store.Search()` — that's 1000 FTS5 queries. O(n²) in practice.

**Design:**
- Reduce batch size: process in pages of 100 instead of 1000
- Add early termination: stop after N merges per batch (e.g., 50)
- This is a consolidation background task — it doesn't need to be exhaustive in one pass

**Files:**
- Modify: `pkg/memory/memory.go:mergeDuplicates`
- Test: existing tests cover behavior; add `TestMergeDuplicates_BatchLimit`

---

### 7. Log errors in appendReviewPatterns (pkg/dispatcher)

**Problem:** `appendReviewPatterns()` at `dispatcher.go:1247` silently ignores ALL errors: MkdirAll, OpenFile, WriteString. If the patterns file can't be written, the user has no observability.

**Design:**
- Return error from `appendReviewPatterns()`
- Log at call site (`dispatcher.go:1150`) via `d.logEvent()` if it fails
- Not blocking — review still proceeds on error

**Files:**
- Modify: `pkg/dispatcher/dispatcher.go:appendReviewPatterns` and call site
- Test: `pkg/dispatcher/dispatcher_test.go` — verify error is logged when dir is unwritable

---

### 8. Deduplicate remember/recall command constructors (cmd/oro)

**Problem:** `newRememberCmd()` and `newRememberCmdWithStore()` are nearly identical (same for recall). The `WithStore` variants exist for testing; the non-`WithStore` variants call `defaultMemoryStore()` inline.

**Design:**
- Delete `newRememberCmd()` and `newRecallCmd()`
- Have `newRememberCmdWithStore(nil)` lazily open the store if nil
- Same pattern for recall
- Or: use a factory function that returns a store-opener closure

**Files:**
- Modify: `cmd/oro/cmd_remember.go`, `cmd/oro/cmd_recall.go`, `cmd/oro/root.go`
- Test: existing tests use `WithStore` variants, so no test changes needed

---

### 9. Extract two-phase reservation helper (pkg/dispatcher)

**Problem:** The pattern of (1) set Reserved, (2) unlock for I/O, (3) relock and verify, (4) transition to Busy appears in `qgRetryWithReservation()` and `handleReviewRejection()`. Duplicated subtle concurrency logic.

**Design:**
- Extract `withReservation(workerID string, ioFn func() error, assignFn func(w *trackedWorker) error) error`
- Handles lock/unlock/verify/cleanup
- Both call sites delegate to this helper

**Files:**
- Modify: `pkg/dispatcher/dispatcher.go`
- Test: existing tests cover both paths; add `TestWithReservation_WorkerDisconnectedDuringIO`

---

### 10. Add Validate() to AssignPayload (pkg/protocol)

**Problem:** `AssignPayload` has no validation. A dispatcher bug could send `Type: MsgAssign` with empty BeadID or nil payload. Worker would panic or produce confusing errors.

**Design:**
- Add `func (a *AssignPayload) Validate() error` — checks BeadID non-empty, Worktree non-empty
- Call in worker's `handleAssign()` before processing
- Follows existing pattern of `ReconnectPayload.Validate()`

**Files:**
- Modify: `pkg/protocol/message.go`, `pkg/worker/worker.go:handleAssign`
- Test: `pkg/protocol/message_test.go` — `TestAssignPayload_Validate`

---

## Dependency Order

```
Independent (can run in parallel):
  [2] vectorSearch bounds
  [3] index rebuild transaction
  [4] sanitizeFTS5Query extraction
  [5] zombie reaper tracking
  [6] mergeDuplicates batch limit
  [7] appendReviewPatterns logging
  [8] remember/recall dedup
  [9] two-phase reservation helper
  [10] AssignPayload validation

Depends on [4]:
  [1] path centralization (no actual dependency, just listed last because highest file count)
```

All items are independent — no dependency ordering required between them. They can be worked in parallel.

## Premortem

See premortem analysis in bead descriptions below.
