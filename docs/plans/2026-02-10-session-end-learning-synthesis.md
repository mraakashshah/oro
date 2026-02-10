# Session-End Learning Synthesis Spec

**Date:** 2026-02-10
**Bead:** oro-p6t
**Status:** Decided
**Depends on:** `2026-02-07-memory-system-spec.md` (Layer 3: Project Memory), `memory_capture.py` (LEARNED extraction hook)

## Problem

Learnings captured via `bd comment <id> "LEARNED: ..."` flow into `knowledge.jsonl` and the SQLite memory store, but they never get promoted to durable project documentation. The capture-to-documentation gap means:

1. **Transient knowledge stays transient.** Learnings in `knowledge.jsonl` are surfaced at session start (via `session_start_extras.py`) but decay, get buried by newer entries, or are missed entirely when they match no bead tag.
2. **Recurring patterns are never codified.** The same gotcha discovered 3+ times across sessions should become a skill, rule, or solution doc. Today there is no mechanism to detect frequency or propose codification.
3. **Session boundaries are the right moment.** At handoff or checkpoint time, the agent has full conversation context and can review what was learned. The Stop hook fires AFTER the response and cannot inject context -- it is the wrong trigger.

## Goal

Define triggers, checks, and proposals that surface undocumented learnings at session-transition moments. Keep it lightweight: summarize and propose, never auto-generate files. The human (or supervisor agent) decides what to codify.

## Existing Infrastructure

| Component | Location | Role |
|-----------|----------|------|
| `memory_capture.py` | `.claude/hooks/memory_capture.py` | PostToolUse hook on Bash. Intercepts `bd comment <id> "LEARNED: ..."`, writes to `knowledge.jsonl` with auto-tagging. |
| `knowledge.jsonl` | `.beads/memory/knowledge.jsonl` | Append-only JSONL. Each entry: `{key, type, content, bead, tags, ts}`. Deduped by key on read. |
| `session_start_extras.py` | `.claude/hooks/session_start_extras.py` | SessionStart hook. Surfaces 5 most recent learnings, stale beads, merged worktrees. |
| `pkg/memory/memory.go` | `pkg/memory/memory.go` | Go memory Store with SQLite FTS5 backend. Insert, Search, HybridSearch, Consolidate. Types: lesson, decision, gotcha, pattern, preference. |
| `create-handoff` skill | `.claude/skills/create-handoff/SKILL.md` | Session-end skill. Step 4 already says "ask yourself: did I learn anything?" and suggests `bd comment <id> "LEARNED: ..."`. |
| `context-checkpoint` skill | `.claude/skills/context-checkpoint/SKILL.md` | Bead-boundary skill. Fires after every bead completion. Manages compact/handoff two-stage response. |
| Stop hook | `.claude/hooks/stop-checklist.sh` | Fires AFTER response. Cannot inject context. Currently a no-op (`echo '{}'`). |

## Design

### Trigger Points

Three triggers, each at a different granularity:

```
Bead completion              Session end              Work-landed moment
      |                           |                          |
  context-checkpoint        create-handoff         PostToolUse(git commit)
      |                           |                          |
  "Batch review:             "Final review:           "Just committed:
   any learnings              full session scan        any LEARNED for
   from this bead?"           before context dies"     this bead?"
```

#### Trigger 1: `context-checkpoint` skill (bead boundary)

**When:** After every bead completion, before starting next bead.

**What it checks:**
1. Scan `knowledge.jsonl` for entries where `bead` matches the just-completed bead ID.
2. Query `pkg/memory` Store for memories with matching `bead_id`.
3. Count unique tags across these entries. Check frequency map (see Frequency Thresholds below).

**What it proposes:**
- If learnings exist for this bead but are only in `knowledge.jsonl` (not yet in SQLite memory store): propose `oro remember` to promote them.
- If a tag or pattern has hit the 3+ frequency threshold: propose codification (see Decision Tree below).
- Format: inject a brief `## Learning Checkpoint` section into the agent's context via the checkpoint protocol.

**Implementation approach:** Add a `learning_checkpoint()` function to the checkpoint skill instructions. The skill already runs at bead boundaries. The function reads `knowledge.jsonl`, filters by bead ID, and formats a summary. No new hook needed -- this is a skill instruction update.

#### Trigger 2: `create-handoff` skill (session end)

**When:** Session ending (explicit handoff, context threshold breach, user request).

**What it checks:**
1. Scan ALL `knowledge.jsonl` entries from this session (filter by timestamp > session start, or by bead IDs worked this session).
2. Cross-reference with `docs/decisions-and-discoveries.md` to find undocumented learnings.
3. Run frequency analysis across all entries, not just this session's.
4. Check for learnings that appear in multiple beads (cross-bead patterns).

**What it proposes:**
- List undocumented learnings with a one-line summary each.
- For any learning at 3+ frequency: propose specific codification target (skill, rule, or solution doc) per the Decision Tree.
- For cross-bead patterns: propose a `docs/decisions-and-discoveries.md` entry.
- Format: inject a `## Undocumented Learnings` section into the handoff YAML as a new `learnings:` field.

**Implementation approach:** Add steps to the `create-handoff` skill between Step 3 (Field Guide) and Step 4 (Capture Learnings). The current Step 4 says "ask yourself" -- the new step makes this concrete with a scan-and-summarize protocol. No new hook needed.

#### Trigger 3: PostToolUse hook on `git commit` (work-landed moment)

**When:** After a `git commit` command succeeds in Bash.

**What it checks:**
1. Parse the commit message for a bead reference (e.g., `(bd-xyz)` or `[bd-xyz]`).
2. If a bead is referenced, scan `knowledge.jsonl` for entries with that bead ID.
3. Check if any LEARNED entries exist that are not yet reflected in committed documentation.

**What it proposes:**
- If LEARNED entries exist for this bead: inject a brief reminder via `additionalContext`:
  ```
  You have N undocumented learnings for bead <id>. Consider:
  - Adding to docs/decisions-and-discoveries.md
  - Running `oro remember "..."` to promote to project memory
  ```
- If no entries: no output (lightweight, zero-cost when nothing to surface).

**Implementation approach:** Extend `memory_capture.py` (already a PostToolUse hook on Bash) or create a sibling hook `learning_reminder.py`. The existing hook pattern matches `bd comment` -- the new pattern matches `git commit`. Advantage of a separate hook: single-responsibility, testable independently.

### Frequency Thresholds

Adapted from CC-v3's compound-learnings system. Frequency is counted by tag or content-similarity cluster across all `knowledge.jsonl` entries (not just current session).

| Count | Level | Action |
|-------|-------|--------|
| 1 | Note | Surface at session start (already done by `session_start_extras.py`). No additional action. |
| 2 | Consider | Surface at checkpoint with "this has come up before" context. Include the previous occurrence for comparison. |
| 3+ | Create | Propose specific codification target. The agent should suggest (not auto-create) a concrete artifact. |

**Frequency calculation:**
- **By tag:** Count entries sharing the same tag. E.g., 3 entries tagged `git` -> `git` tag is at "Create" level.
- **By content similarity:** Use the `slugify()` function from `memory_capture.py` to detect near-duplicate keys. Entries with Jaccard similarity > 0.5 on their slug tokens are considered the same pattern.
- **Cross-session:** Frequency counts span all time, not just the current session. Recent entries (< 7 days) get 2x weight in frequency scoring to prioritize active patterns.

### Decision Tree: What to Codify

When frequency hits 3+ (Create level), the type of artifact depends on the learning's nature:

```
Is the learning a repeatable sequence of steps?
  YES -> Propose a SKILL (.claude/skills/<name>/SKILL.md)
         Example: "Always run ruff before pyright" -> lint-order skill

Is it triggered by a specific event or condition?
  YES -> Propose a HOOK (.claude/hooks/<name>.py)
         Example: "Check for worktree before rebase" -> already exists as rebase_worktree_guard.py

Is it a heuristic, rule of thumb, or constraint?
  YES -> Propose a RULE (add to .claude/rules/<file>.md or CLAUDE.md)
         Example: "Never cd into worktrees" -> already in standards

Is it a solved problem with context?
  YES -> Propose a SOLUTION DOC (docs/decisions-and-discoveries.md entry)
         Example: "modernc sqlite doesn't support FTS5 bm25()" -> decision entry

None of the above?
  -> Promote to project memory via `oro remember` with appropriate type tag
```

The decision tree is surfaced as part of the proposal, not executed automatically. The agent (or human) reads the proposal and decides.

### Integration with Existing Memory System

```
                    +-------------------------+
                    |  bd comment "LEARNED:..."  |
                    +------------+------------+
                                 |
                    +------------v------------+
                    |   memory_capture.py      |
                    |   (PostToolUse hook)     |
                    |   -> knowledge.jsonl     |
                    +------------+------------+
                                 |
              +------------------+------------------+
              |                  |                   |
    +---------v--------+ +------v-------+ +--------v--------+
    | context-checkpoint| |create-handoff| | git commit hook  |
    | (bead boundary)  | |(session end) | |(work landed)     |
    +---------+--------+ +------+-------+ +--------+--------+
              |                  |                   |
              v                  v                   v
         Scan by bead_id   Scan by session     Scan by bead_id
         + frequency check  + cross-reference   + existence check
              |                  |                   |
              v                  v                   v
         Propose:           Propose:             Remind:
         - promote to       - codify (3+)        - "N undocumented
           memory store     - document             learnings exist"
         - codify (3+)       cross-bead
                             patterns
              |                  |                   |
              v                  v                   v
    +---------------------------------------------------------+
    |           Human/Supervisor decides what to create        |
    |                                                          |
    |  oro remember "..."     docs/decisions-and-discoveries   |
    |  .claude/skills/...     .claude/rules/...                |
    |  .claude/hooks/...                                       |
    +---------------------------------------------------------+
```

### Data Flow Example

Session works on beads `oro-abc` and `oro-def`. During work:

1. Agent runs `bd comment oro-abc "LEARNED: git rebase fails if branch is checked out in any worktree"` -- `memory_capture.py` writes to `knowledge.jsonl` with tags `[git]`.
2. Agent completes `oro-abc`. Context-checkpoint fires:
   - Finds 1 learning for `oro-abc`.
   - Checks frequency: `git` tag has 3 entries total across all time.
   - Proposes: "Tag `git` has reached 3+ frequency. Consider creating a rule or skill. Most recent entries: [list]. Decision tree suggests: **Rule** (heuristic: 'remove worktrees before rebase')."
3. Agent works on `oro-def`, discovers similar issue, adds another LEARNED entry.
4. Agent runs `git commit -m "fix(rebase): handle worktree conflicts (bd-def)"`. Git commit hook fires:
   - Finds 1 undocumented learning for `oro-def`.
   - Injects: "1 undocumented learning for bd-def. Consider documenting."
5. Session ends. Create-handoff fires:
   - Scans all entries from this session: 2 learnings (oro-abc, oro-def).
   - Cross-references: both relate to git rebase + worktrees. Cross-bead pattern detected.
   - Frequency: `git` tag now at 4. Content similarity between the two entries is high (Jaccard > 0.5).
   - Proposes: "Cross-bead pattern: git rebase + worktree conflicts. Appeared in oro-abc and oro-def. Suggest: `docs/decisions-and-discoveries.md` entry + `.claude/rules/` addition."
6. Handoff YAML includes:
   ```yaml
   learnings:
     undocumented:
       - "git rebase fails if branch checked out in worktree" (oro-abc, git)
       - "worktree removal must precede rebase" (oro-def, git)
     proposed_codification:
       - type: rule
         target: .claude/rules/standards.md
         content: "Always remove worktrees before attempting git rebase"
         frequency: 4
         tag: git
   ```

### What This Does NOT Do

- **Auto-create files.** All proposals are surfaced as context. The agent or human decides.
- **Replace `session_start_extras.py`.** That hook surfaces recent learnings at session start. This spec covers session-end synthesis -- different moment, different purpose.
- **Run LLM extraction.** No spawning headless Claude to analyze learnings. The agent itself does the analysis when prompted by the trigger context. Keeps cost at zero.
- **Modify `knowledge.jsonl` format.** The existing schema is sufficient. Frequency analysis is computed on-read, not stored.
- **Replace the SQLite memory store.** `knowledge.jsonl` is the fast-capture layer (Claude Code hooks are Python). The Go memory Store is the durable layer. This spec bridges them by proposing promotion at the right moments.

## Implementation Beads

This is a spec document. Implementation should be broken into separate beads:

1. **Trigger 2 first (create-handoff skill update):** Highest value, lowest effort. Update the skill's Step 4 with a concrete scan-and-propose protocol. No code changes, just skill instructions.
2. **Trigger 1 (context-checkpoint skill update):** Add learning checkpoint to the checkpoint protocol. Also instruction-only.
3. **Trigger 3 (git commit hook):** New Python hook `learning_reminder.py`. Requires code + tests.
4. **Frequency analysis library:** Shared Python module for tag frequency counting and content similarity. Used by triggers 1-3. Requires code + tests.

## Risks

| Risk | Severity | Mitigation |
|------|----------|------------|
| Agent ignores proposals (context overload) | MEDIUM | Keep proposals short (3-5 lines max). Use structured format. Place at end of context, not buried in middle. |
| False frequency triggers (unrelated entries share a tag) | LOW | Content similarity check (Jaccard > 0.5) supplements tag counting. Two entries tagged `git` but about different things won't cluster. |
| Overhead on every bead completion | LOW | `knowledge.jsonl` is typically <100 entries. Scanning is O(n) with n small. No API calls, no DB queries in the hook path. |
| Stale proposals ("you should document X" repeated every session) | MEDIUM | Track which learnings have been proposed before. Add a `proposed_at` field to entries on first proposal. Skip re-proposing within 7 days. |
