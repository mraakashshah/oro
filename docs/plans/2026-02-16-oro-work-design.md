# `oro work` — Standalone Bead Executor

**Date:** 2026-02-16
**Status:** Draft

## Goal

Add `oro work <bead-id>` — a CLI command that drives the full bead lifecycle
(execute → QG → ops review → merge → close) without the dispatcher or swarm.
Runnable by a human or a claude agent.

## Motivation

Today the full lifecycle requires the dispatcher orchestrating async workers
over UDS. There's no way to:
- Execute a single bead end-to-end from the command line
- Resume a partially-completed bead (worktree exists, code committed, just needs QG/review/merge)
- Run outside the swarm for debugging or small tasks

All building blocks exist as library calls. The gap is a synchronous orchestrator.

## Non-Goals

- Replacing the dispatcher/swarm architecture
- Extracting a shared `pkg/pipeline` abstraction (the async and sync models are too different)
- Interactive claude sessions (this uses `claude -p`, same as workers)

## Design

### Command Interface

```
oro work <bead-id> [flags]

Flags:
  --model <model>     Starting model (default: claude-sonnet-4-5-20250929)
  --timeout <dur>     Per-claude-spawn timeout (default: 15m)
  --skip-review       Skip ops review gate
  --resume            Resume from existing worktree (skip claude, jump to QG)
  --dry-run           Show execution plan without running
```

### Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Bead merged to main and closed |
| 1 | Exhausted retries (QG or review) |
| 2 | Merge failed |
| 3 | Bead not found or missing acceptance criteria |
| 130 | Interrupted (SIGINT) |

### Execution Flow

```
oro work oro-xyz
│
├─ 1. Load bead (bd show --json)
│     Validate: title and acceptance criteria must exist
│
├─ 2. Mark in_progress (bd update --status=in_progress)
│
├─ 3. Create worktree (.worktrees/oro-xyz, branch agent/oro-xyz)
│     Guard: if .worktrees/oro-xyz exists and --resume not set → error
│     If --resume: skip to step 7
│
├─ 4. Fetch memory context + code search context
│     Optional: degrade gracefully if DB unavailable
│
├─ 5. Assemble 12-section prompt (worker.AssemblePrompt)
│
├─ 6. Spawn claude -p
│     stdin=/dev/null (same as workers)
│     stdout piped through memory extraction
│     Timeout: --timeout flag (default 15m), kill process group on expiry
│
├─ 7. Run quality gate (worker.RunQualityGate)
│     └─ Fail? → retry loop:
│        Attempt 1-3: re-spawn claude with feedback, same model
│        Attempt 4+: escalate to opus, reset attempt counter
│        Attempt 7 (3 on opus): exhausted → exit 1
│
├─ 8. Ops review (ops.Spawner.Review)
│     └─ Rejected? → retry loop:
│        Re-spawn claude with review feedback, model=opus
│        After 2 rejections: exhausted → exit 1
│     └─ --skip-review? → skip this step
│
├─ 9. Merge to main (merge.Coordinator.Merge)
│     └─ Conflict? → spawn ops merge resolver, retry once
│     └─ Fail? → exit 2
│
├─ 10. Close bead (bd close)
│
└─ 11. Remove worktree
```

### Retry Constants (shared with dispatcher)

```go
const (
    maxQGRetries        = 3  // per model tier
    maxReviewRejections = 2
    defaultTimeout      = 15 * time.Minute
)
```

These match `pkg/dispatcher/dispatcher.go` constants. Import them directly
or duplicate with a comment referencing the source.

### Resume Flow (--resume)

When `--resume` is passed and `.worktrees/<bead-id>` exists:

1. Check if branch `agent/<bead-id>` has commits ahead of main
2. If yes: skip claude spawn, jump directly to QG (step 7)
3. If no commits ahead: re-spawn claude in existing worktree
4. If worktree doesn't exist: error

This enables recovering from interrupted runs without losing work.

### Concurrency Safety

**Swarm collision guard:** Before creating a worktree, check if
`.worktrees/<bead-id>` already exists. If it does and `--resume` is not set,
fail with: "bead is already being worked in .worktrees/<bead-id>. Use --resume
to continue, or remove the worktree first."

**Merge serialization:** The `merge.Coordinator` mutex is per-process. When
running alongside the swarm, two processes could merge simultaneously. This is
safe because the merge coordinator uses rebase which handles moved refs — if
main moved during rebase, git will re-resolve. The worst case is a transient
failure that succeeds on retry.

### Dependencies — All Library Calls

| Step | Package | Function | Standalone? |
|------|---------|----------|-------------|
| Load bead | `pkg/dispatcher` | `CLIBeadSource.Show()` | Yes |
| Mark in_progress | `pkg/dispatcher` | `CLIBeadSource.Update()` | Yes |
| Create worktree | `pkg/dispatcher` | `GitWorktreeManager.Create()` | Yes |
| Memory context | `pkg/memory` | `memory.ForPrompt()` | Yes (needs DB) |
| Code search | `pkg/codesearch` | `CodeIndex.FTS5Search()` | Yes (needs DB) |
| Assemble prompt | `pkg/worker` | `AssemblePrompt()` | Yes (pure function) |
| Spawn claude | `pkg/worker` | `ClaudeSpawner.Spawn()` | Yes |
| Quality gate | `pkg/worker` | `RunQualityGate()` | Yes (exported function) |
| Ops review | `pkg/ops` | `Spawner.Review()` | Yes |
| Merge | `pkg/merge` | `Coordinator.Merge()` | Yes |
| Close bead | `pkg/dispatcher` | `CLIBeadSource.Close()` | Yes |
| Remove worktree | `pkg/dispatcher` | `GitWorktreeManager.Remove()` | Yes |

### Memory Extraction

`worker.processOutput` is a method on `Worker` struct with internal state
(mutex, logWriter, memStore). For `oro work`, extract a standalone function:

```go
func DrainOutput(ctx context.Context, stdout io.ReadCloser, store *memory.Store, beadID string) {
    scanner := bufio.NewScanner(stdout)
    for scanner.Scan() {
        line := scanner.Text()
        fmt.Println(line) // echo to terminal
        if store != nil {
            if params := memory.ParseMarker(line); params != nil {
                params.BeadID = beadID
                _, _ = store.Insert(ctx, *params)
            }
        }
    }
}
```

~15 lines, no log file management needed (stdout already visible in terminal).

### Output (TTY vs non-TTY)

TTY (human):
```
✓ Loaded oro-xyz: Fix dispatcher MISSING_AC check
✓ Worktree: .worktrees/oro-xyz
◐ Running claude (sonnet)...
✓ Claude completed (47s)
◐ Quality gate...
✓ Quality gate passed
◐ Ops review (opus)...
✓ Review: APPROVED
◐ Merging to main...
✓ Merged (abc1234)
✓ Bead closed
```

Non-TTY (agent):
```
Loaded oro-xyz: Fix dispatcher MISSING_AC check
Worktree: .worktrees/oro-xyz
Running claude (sonnet)...
Claude completed (47s)
Quality gate passed
Ops review (opus): APPROVED
Merged (abc1234)
Bead closed
```

Use `isatty.IsTerminal` (already imported in `cmd_start.go`).

### Premortem

**Tiger — QG/review retry drift from dispatcher:**
Both `oro work` and the dispatcher have retry loops with the same constants.
*Mitigation:* Share the constants. The loop logic is <20 lines each and the
sync vs async models are structurally different enough that sharing would be
forced. Acceptable duplication.

**Tiger — claude -p hangs:**
Known issue (Ink/setRawMode). Same mitigation as workers: stdin=/dev/null +
timeout with SIGKILL on process group.

**Elephant — merge race with swarm:**
If swarm and `oro work` merge simultaneously, one may fail.
*Accepted:* Rebase-merge handles moved refs. Transient failure retries once.
In practice, `oro work` is for single-bead use — you wouldn't run it during
a full swarm session.

**Paper tiger — "duplicating orchestration logic":**
~40 lines of retry-loop code. The sync and async models are fundamentally
different. Forced abstraction would be worse than the duplication.

## File Plan

| File | Change |
|------|--------|
| `cmd/oro/cmd_work.go` | **New.** Command definition, flag parsing, orchestrator loop (~200 lines) |
| `cmd/oro/cmd_work_test.go` | **New.** Tests for orchestrator with mock spawner/beadsource |
| `pkg/worker/drain.go` | **New.** `DrainOutput()` standalone function (~15 lines) |
| `pkg/worker/drain_test.go` | **New.** Test memory extraction from stdout |
| `cmd/oro/main.go` | Add `newWorkCmd()` to root command |

## Acceptance Criteria

- [ ] `oro work <bead-id>` executes full lifecycle: worktree → claude → QG → review → merge → close
- [ ] QG failures retry up to 3x per model tier, escalating from sonnet to opus
- [ ] Review rejections retry up to 2x with feedback, model escalated to opus
- [ ] `--resume` skips claude spawn when worktree has commits ahead of main
- [ ] `--skip-review` bypasses ops review gate
- [ ] Collision guard prevents clobbering existing worktrees
- [ ] SIGINT kills subprocess and leaves worktree intact for resume
- [ ] Exit codes match spec (0/1/2/3/130)
- [ ] Non-TTY output degrades gracefully (no spinners/ANSI)
- [ ] Tests cover happy path, QG retry, review rejection, resume, collision guard
