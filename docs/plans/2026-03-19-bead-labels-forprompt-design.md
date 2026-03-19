# Bead Labels → ForPrompt Memory Search — Design Doc

**Date:** 2026-03-19
**Goal:** Wire bd labels into ForPrompt() to boost memory search relevance
**Scope:** `pkg/protocol/types.go`, `pkg/dispatcher/dispatcher.go`, `pkg/dispatcher/worker_pool.go`, `cmd/oro/cmd_work.go`

---

## Problem

bd has a labels system (`bd label add/remove/list`) with labels in active use. The memory system's `ForPrompt()` searches by bead title only. Labels that could improve search relevance (e.g., a bead labeled "auth,middleware" finding auth-related memories) are unused.

## Key Discovery

**bd ready --json ALREADY includes labels.** The original assumption that labels aren't in JSON output was wrong (verified by running `bd ready --json` after adding a label). This means no extra bd CLI calls are needed — just a struct field addition.

## Design Decision: Labels as Search Terms, Not Filters

ForPrompt's tag filtering (`SearchOpts.Tags`) is a **hard filter** — it excludes memories that don't match ANY tag. If a bead has `labels=["rebase"]` but no memories are tagged "rebase", ForPrompt returns zero results (worse than today).

**Instead:** Append labels to the search query string. Labels boost relevance via FTS5/TF-IDF scoring without filtering anything out. A bead labeled "rebase" surfaces memories mentioning "rebase" higher, without excluding unrelated but relevant memories.

```go
// Before:
memCtx, _ = memory.ForPrompt(ctx, d.memories, nil, bead.Title, 0)

// After:
searchQuery := bead.Title
if len(bead.Labels) > 0 {
    searchQuery += " " + strings.Join(bead.Labels, " ")
}
memCtx, _ = memory.ForPrompt(ctx, d.memories, nil, searchQuery, 0)
```

No ForPrompt signature change. No memory system changes. Labels are purely additive search context.

### Changes

**1. Add Labels field to Bead and BeadDetail** (`pkg/protocol/types.go`):
```go
Labels []string `json:"labels,omitempty"` // bd labels for search relevance
```

Keep existing `Tags` field as-is (different concept — Tags was for memory tagging, Labels is from bd). Add to BOTH `Bead` and `BeadDetail`.

**2. Add buildSearchQuery helper** (`pkg/dispatcher/dispatcher.go`):
```go
func buildSearchQuery(title string, labels []string) string {
    if len(labels) == 0 {
        return title
    }
    return title + " " + strings.Join(labels, " ")
}
```

**3. Update ForPrompt call sites** (4 direct sites + 1 indirect):

| Site | File:Line | Current | After |
|------|-----------|---------|-------|
| Initial assign | dispatcher.go:2319 | `ForPrompt(ctx, ..., nil, bead.Title, 0)` | `ForPrompt(ctx, ..., nil, buildSearchQuery(bead.Title, bead.Labels), 0)` |
| fetchBeadMemories | dispatcher.go:1003 | `ForPrompt(ctx, ..., nil, searchTerm, 0)` | `ForPrompt(ctx, ..., nil, searchTerm, 0)` (no change — searchTerm already constructed from bead title) |
| Handoff respawn | worker_pool.go:115 | `ForPrompt(ctx, ..., nil, h.beadID, 0)` | `ForPrompt(ctx, ..., nil, buildSearchQuery(h.title, h.labels), 0)` |
| Standalone work | cmd_work.go:410 | `ForPrompt(ctx, ..., nil, cfg.bead.Title, 0)` | `ForPrompt(ctx, ..., nil, buildSearchQuery(cfg.bead.Title, cfg.bead.Labels), 0)` |

**4. Add title and labels to pendingHandoff** (`pkg/dispatcher/dispatcher.go`):
```go
type pendingHandoff struct {
    beadID       string
    epicID       string
    worktree     string
    baseBranch   string
    targetBranch string
    model        string
    title        string   // NEW: for memory search on respawn
    labels       []string // NEW: for memory search on respawn
}
```

This also fixes an existing bug where handoff respawn searches by bead ID instead of title, producing poor memory results.

**5. Populate title/labels in handleHandoff** (`pkg/dispatcher/dispatcher.go`):
Where pendingHandoff is created, add:
```go
h.title = w.title   // need to add title to trackedWorker
h.labels = bead.Labels
```
Note: trackedWorker doesn't currently store title. Either add it, or fetch from beadSource.Show() at handoff time.

### User workflow

```bash
# Label a bead
bd label add oro-abc go auth middleware

# Memory search now includes these terms
# Worker gets memories mentioning "go", "auth", or "middleware" ranked higher
```

## Test Plan

- `TestBuildSearchQuery_WithLabels` — title + labels → concatenated string
- `TestBuildSearchQuery_NoLabels` — title only → unchanged
- `TestBuildSearchQuery_EmptyTitle` — empty title + labels → just labels
- `TestBeadLabelsDeserialized` — bd JSON with `"labels":["bug","auth"]` → Bead.Labels populated
- `TestHandoffRespawn_UsesTitle` — verify respawn searches by title (not bead ID)

## Adversarial Review (2026-03-19)

**Round 1:** 4 blocking issues found, 3 fixed (B4 moot):
- B1: bd ready --json ALREADY includes labels. Fixed: dropped FetchLabels, just add struct field.
- B2: BeadDetail needs Labels too (cmd_work.go uses BeadDetail). Fixed: add to both structs.
- B3: Handoff respawn searches by bead ID not title (existing bug). Fixed: add title+labels to pendingHandoff.
- B4: FetchLabels breaks 5+ mock implementations. Moot — FetchLabels dropped entirely.

Key design pivot: N4 revealed tag filtering is restrictive (excludes non-matching), not additive. Decision: use labels as search query terms instead of ForPrompt tag filters. No ForPrompt signature change needed.

## Risks

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Labels with spaces/special chars in search query | Low | Low | FTS5 handles multi-word terms fine |
| Too many labels dilute search relevance | Low | Low | Labels are typically 1-5 short terms |
| pendingHandoff struct grows | Low | Low | Two small fields (string + []string) |
| trackedWorker needs title field added | Low | Low | One string field, populated at assignment |

## Dependencies

None. Additive change. Existing behavior preserved when Labels is empty.
