# Worker Prompt Enrichment — Design Doc

**Date:** 2026-03-19
**Goal:** Reduce QG retry cycles by giving workers better context on first attempt
**Scope:** `pkg/worker/prompt.go`, `pkg/worker/worker.go`, `pkg/protocol/message.go`, `pkg/dispatcher/dispatcher.go`, `pkg/dispatcher/worker_pool.go`

---

## Problem

Workers frequently fail QG on first attempt because:

1. **No commit history visibility** — Workers on retries/handoffs can't see what's already been committed in their worktree. They repeat failed approaches or undo prior work.
2. **Implicit autonomy** — The prompt says what to do but never says "don't stall." Workers waste cycles sending STATUS messages when they should push through.
3. **No behavioral customization** — Changing worker behavior requires Go code changes + rebuild. Operators can't tune worker instructions per-project.
4. **Divergent AssignPayload construction** — 4 dispatcher call sites populate different subsets of fields. Retries/handoffs omit Title, AcceptanceCriteria, ProjectRoot, TargetBranch, CodeSearchContext. Description is missing from ALL sites.

The divergent call sites are the root cause: workers on retries get degraded prompts missing bead metadata, coding rules (no ProjectRoot), and merge target info.

## Design

### Part 1: Centralize AssignPayload Construction

Introduce `buildAssignPayload` on the Dispatcher:

```go
func (d *Dispatcher) buildAssignPayload(
    ctx context.Context,
    w *trackedWorker,
    attempt int,
    feedback string,
    memCtx string,
) *protocol.AssignPayload
```

This method:
1. Re-reads bead metadata (Title, Description, AC) from `d.beads.Show(ctx, w.beadID)`. On error (bead closed externally, db failure), logs warning and falls back to empty strings — the prompt still renders, just without bead metadata.
2. Sets `ProjectRoot` from `filepath.Dir(d.beadsDir)` (the repo root, already available on Dispatcher).
3. Computes `GitLog` via `git log --oneline -20` in `w.worktree` (2s timeout, empty fallback on error). **Skipped when `w.isEpicDecomp` is true** (epic decomposition has no worktree code to log).
4. Reads `.oro/worker-program.md` from ProjectRoot (empty fallback if missing, **capped at 32KB** with log warning if truncated). **Skipped when `w.isEpicDecomp` is true.**
5. Populates `CodeSearchContext` from code index when available (same as current initial assign).
6. Carries forward `trackedWorker` fields: worktree, model, targetBranch, isEpicDecomp.

All 4 call sites replaced with:
```go
d.sendToWorker(w, protocol.Message{
    Type:   protocol.MsgAssign,
    Assign: d.buildAssignPayload(ctx, w, attempt, feedback, memCtx),
})
```

#### Lock/IO ordering with withReservation

The QG retry and rejection retry sites use `withReservation(ioFn, assignFn)`. The `ioFn` runs outside the lock (for IO), and `assignFn` runs inside the lock. `buildAssignPayload` does IO (beads.Show, git log, file read) but also reads `w.worktree` and `w.isEpicDecomp` which are set under lock.

**Solution:** The worktree and isEpicDecomp fields are stable once set on a reserved worker (they don't change between reservation and assignment). So `buildAssignPayload` is called inside `ioFn` (outside lock), reading the stable `w` fields. The `assignFn` then only does `d.sendToWorker(w, msg)` under lock.

#### Handoff respawn site (worker_pool.go)

`registerWorker` currently has no `ctx` parameter. **Fix:** Add `ctx context.Context` as first parameter, threaded from the caller (`acceptLoop` which has `ctx`). This also benefits the existing `memory.ForPrompt` call inside `registerWorker` which currently uses `context.Background()`.

#### Current call sites and what changes

| Site | Location | Current fields | After |
|------|----------|---------------|-------|
| Initial assign | dispatcher.go:2339 | 9 of 13 (missing Description, ProjectRoot, GitLog, WorkerProgram) | `buildAssignPayload(ctx, w, 0, "", memCtx)` |
| QG retry | dispatcher.go:949 | 6 of 13 (missing Title, Description, AC, ProjectRoot, TargetBranch, CodeSearch, GitLog, WorkerProgram) | `buildAssignPayload(ctx, w, attempt, qgOutput, memCtx)` |
| Rejection retry | dispatcher.go:1625 | 6 of 13 (same gaps as QG retry) | `buildAssignPayload(ctx, w, count, feedback, memCtx)` |
| Handoff respawn | worker_pool.go:127 | 5 of 13 (same gaps + missing Attempt, Feedback) | `buildAssignPayload(ctx, w, 0, "", memCtx)` |

### Part 2: New AssignPayload Fields

Add to `protocol.AssignPayload`:

```go
GitLog        string `json:"git_log,omitempty"`        // git log --oneline -20 from worktree
WorkerProgram string `json:"worker_program,omitempty"` // contents of .oro/worker-program.md (max 32KB)
```

`ProjectRoot` and `Description` already exist on the struct — just need to be populated by dispatcher.

### Part 3: New PromptParams Fields

Add to `worker.PromptParams`:

```go
GitLog        string // git log output from worktree (may be empty)
WorkerProgram string // contents of .oro/worker-program.md (may be empty)
```

Update `BuildAssignPrompt` bridge (worker.go:444) to map:
```go
GitLog:        a.GitLog,
WorkerProgram: a.WorkerProgram,
```

### Part 4: New Prompt Sections

#### Git History (after Worktree, before Merge Target)

```markdown
## Git History

Recent commits in your worktree:

<git log output>
```

Omitted entirely when GitLog is empty (fresh worktree, no commits yet).

**Implementation:** Insert in `appendStaticSections` between the Worktree section (line 206) and the Merge Target section (line 211). Requires passing `params.GitLog` to `appendStaticSections` (already receives full `params`).

#### Worker Program (after Coding Rules, before TDD)

```markdown
## Worker Program

<contents of .oro/worker-program.md>
```

Omitted entirely when WorkerProgram is empty (file doesn't exist).

**Implementation:** Insert in `appendStaticSections` between the Coding Rules section (line 203) and the TDD section (line 204).

#### Autonomy (after Constraints, before Context Handoff)

```markdown
## Autonomy

You have full authority to implement within the acceptance criteria. Follow these rules:

- Do not ask for permission or confirmation — execute autonomously.
- If your first approach fails, try a different approach. Exhaust at least 3 strategies before escalating.
- If stuck on a test or lint error, read the error message carefully and fix the root cause — do not apply workarounds.
- If the quality gate fails, read the output, identify the specific failing check, and fix it directly.
- The operator may be unavailable. Do not wait for guidance.
```

This section is static text — no new fields needed. Always present.

**Implementation:** Insert in `appendStaticSections` between Constraints (line 226) and `appendContextHandoffSection` (line 227).

### Part 5: Test Plan

#### prompt_test.go changes
- `expectedSectionHeaders` gains **1** new entry: `"## Autonomy"` (always present). Git History and Worker Program are conditional — NOT added to this list.
- Existing `TestAssemblePrompt_SectionOrder` continues to pass because new sections are inserted between existing ones, preserving relative order.

#### New prompt tests
- `TestAssemblePrompt_GitHistoryPresent` — params with GitLog set → section header and content present
- `TestAssemblePrompt_GitHistoryOmittedWhenEmpty` — params with empty GitLog → no `## Git History` header
- `TestAssemblePrompt_WorkerProgramPresent` — params with WorkerProgram set → section header and content present
- `TestAssemblePrompt_WorkerProgramOmittedWhenEmpty` — params with empty WorkerProgram → no `## Worker Program` header
- `TestAssemblePrompt_AutonomySectionPresent` — verifies autonomy directives always present, contains key phrases ("full authority", "3 strategies")

#### New dispatcher tests
- `TestBuildAssignPayload_PopulatesAllFields` — mock beads.Show returns full metadata, verify all AssignPayload fields populated
- `TestBuildAssignPayload_ShowFailsFallback` — beads.Show returns error, verify payload still has worktree/model/attempt but empty Title/AC/Description
- `TestBuildAssignPayload_EpicSkipsGitLogAndProgram` — isEpicDecomp=true, verify GitLog and WorkerProgram are empty
- `TestBuildAssignPayload_WorkerProgramSizeCap` — .oro/worker-program.md > 32KB, verify truncation

### Section Ordering (final)

1. Role
2. Bead
3. Previous Feedback (conditional: retry only)
4. Memory
5. Relevant Code (conditional: non-empty only)
6. Coding Rules
7. Worker Program (conditional: .oro/worker-program.md exists)
8. TDD
9. Quality Gate
10. Worktree
11. Git History (conditional: non-empty only)
12. Merge Target
13. Git
14. Beads Tools
15. Constraints
16. Autonomy
17. Context Handoff
18. Failure
19. Exit

## Risks & Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| `git log` shell-out adds latency to assignment | Low | Low | 2s timeout, empty fallback. Local git, typically <50ms |
| `d.beads.Show()` on every retry adds latency | Low | Low | Local SQLite, <1ms |
| `d.beads.Show()` fails (bead closed externally) | Low | Medium | Log warning, fall back to empty strings. Prompt renders without bead metadata |
| Autonomy directives cause workers to go off-rails | Medium | Low | QG + ops review catch bad output. Tune wording based on observed behavior |
| Worker program .md gets stale or contradicts code sections | Low | Low | Opt-in file. Behavioral guidance only — structural sections stay in Go |
| Worker program .md too large (>32KB) | Low | Low | Truncate at 32KB with log warning. Well under 1MB MaxMessageSize |
| Prompt token growth pushes context exhaustion faster | Low | Low | ~500 tokens added out of 200K+. Measure after deploy |
| Epic decomposition gets unnecessary git log / file reads | Low | Low | Short-circuit: skip when `w.isEpicDecomp` is true |

## Adversarial Review (2026-03-19)

**Round 1:** 4 blocking issues found and fixed:
- B1: Description missing from ALL 4 sites (not just retries). Fixed call-site table.
- B2: Wrong field names (d.beadSource → d.beads, d.repoRoot → filepath.Dir(d.beadsDir)). Fixed.
- B3: registerWorker has no ctx param. Fix: thread ctx from acceptLoop caller.
- B4: expectedSectionHeaders can't gain conditional sections. Fix: only add Autonomy.

8 non-blocking issues incorporated: PromptParams + BuildAssignPrompt bridge (N1), 32KB size cap (N2), epic decomp short-circuit (N3), Show() error fallback (N4), appendStaticSections insertion points (N5), edge case tests (N6), worker.go in scope (N7), withReservation lock ordering (N8).

## Out of Scope

- Tiered QG (separate spec)
- Revert-on-regression (separate spec)
- Results ledger (separate spec)
- Customizable section ordering
- Template variable substitution in worker program

## Dependencies

None. All changes are additive. Existing behavior preserved when new fields are empty.
