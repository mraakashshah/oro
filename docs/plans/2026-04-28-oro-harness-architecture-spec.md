# Oro Harness Architecture — v6 Spec

**Date:** 2026-04-28
**Author:** Aakash Shah
**Status:** v6 — codex rounds 1-5 complete. **Round 5 verdict: PASS.** Trajectory: 15 → 12 → 11 → 9 → 5 findings; critical 4 → 2 → 1 → 0 → 0. Zero regressions across all rounds. R5 polish wave applied (5 implementation-contract findings); spec is implementation-ready.
**Companion specs:**
- `archive/migration-2026/2026-04-27-replatform-beads-spec.md` (v20, codex-PASS) — replaces bd CLI + Dolt with `pkg/beadstore`
- `docs/plans/2026-04-27-external-tooling-integration-spec.md` (v1) — superseded by this document

**Changelog v5 → v6 (round 5 polish — codex returned PASS at v5; v6 cleans up the 5 implementation-contract findings):**
- `PromoteClosedParentChildren` periodic sweep added (R5.1) — retries failed immediate post-close sweeps; alive-closed parents converge within one sweep interval
- Replan-driven children carry `replan_cycle:<N>` tag (R5.2); `OnReplanChildrenClosed` queries by this tag and handles the zero-children replan case explicitly
- `ExpireReviewQueueSLA` (hourly) and `SweepDeletedBeadLearnings` (5 min) added to sweeper inventory; `pkg/dispatcher/sweeper_loop.go` is the Phase F.5a runner with explicit tick intervals (R5.3, R5.4)
- `pkg/lint/closecheck` is a Phase F.5 deliverable; the three existing `d.beads.Close` callsites in `pkg/dispatcher/dispatcher.go:1652,1990,3107` are listed for migration (R5.5)

**Changelog v4 → v5 (round 4 fixes):**
- Sweeper wiring contract defined: dispatcher orchestrates close+sweep via `Dispatcher.CloseBead`; no `Store.Close` callback or import cycle (R4.1)
- `pkg/dispatcher/sweep.go` moved to Phase F.5a (was Phase G); §18.4 acceptance can now pass within Phase F (R4.2)
- Closed event enum gains `learning_deferred_to_review` and `parent_closed_promoted` (R4.3)
- Migration backfill uses `COALESCE` for `ts` so legacy beads with NULL `created_at`/`closed_at` migrate without violating NOT NULL (R4.4)
- §11.4 `SetGateState` impl signature matches §4.4: `(beadID, from, to, reason)` with CAS-on-`from` and atomic journey append (R4.5)
- Replan loop has explicit termination: `premortem_cycle_count` column on beads, incremented by `OnReplanChildrenClosed`, gates further replans at `max_cycles` (R4.6)
- `bead_learnings_pending` gains `queued_for_review_at` column; review-queue terminal state is fully expressible via 3 nullable columns; `ExpireReviewQueueSLA` sweeper enforces 60-day SLA (R4.7)
- `beads_blocked` view written in full SQL with same missing/deleted/open-parent semantics as `beads_ready`; partition invariant tested (R4.8)
- `ReadTx` parity narrowed to render-facing reads; `Export` explicitly exempt; reflection-based parity test specified (R4.9)

**Changelog v3 → v4 (round 3 fixes):**
- §4.6 Phase A.1 is now an explicit complete migration inventory: every ALTER, CHECK update, view rewrite, and new table is enumerated in one place; downstream phases cannot start until Phase A's full schema is in place
- §5.9 `confirmed` events now clear `last_contradicted_at` (R3.2 — prose recovery path matched in SQL via `clearsContradiction` flag in `scoreDelta`)
- §5.8 Phase D.5 write cutover uses the actual API names (`AppendLearningPending`, `RecordCardEvent`, `Create`, `Retire`); the prior `pkg/cards.Append` name was a leftover and has been removed
- §10.4 `beads_ready` view treats missing OR soft-deleted parents as still blocking children with `awaits_parent_close` (R3.4)
- §10.4 `pkg/dispatcher/sweep.go` is named as a Phase G deliverable with explicit functions (`PromoteChildrenOnParentClose`, `ReapDeletedParentChildren`, `OnReplanChildrenClosed`)
- §4.7 render fails closed on any error inside `WithReadTx`; the prior contradiction between fatal-and-warn handling is removed
- §4.7 `ReadTx` interface now lists every required read method; `pkg/beadstore/readtx_test.go` enforces parity with `Store` via reflection
- §9.3 `checkpointed` payload includes `checkpoint_id`; restart-time discovery of the in-flight checkpoint is defined
- §11.4 `SetGateState` takes `(from, to, reason)` and atomically appends a `gate_state_changed` journey event (added to closed enum)
- §5.7 force-close behavior for `learnings_pending` is defined: ops_review verdicts pass/fail/needs_more, force-close (review queue, 60-day SLA), bead deletion (auto-reject)
- §11.4 `gate_state='replan'` has explicit loop termination: replan re-runs only after replan-children close; max 5 premortem cycles before auto-deferral

**Changelog v2 → v3 (round 2 fixes):**
- `beads_ready` view amendment uses v20's actual schema (`bead_tags` table, not `b.tags` JSON column); preserves v20's `bead_deps` blocking semantics in concert
- `WithReadTx` is non-generic; `ReadTx` interface exposes read methods plus `Cards()` accessor for cross-store snapshot consistency in one transaction
- Beads + cards confirmed to live in **one** SQLite database (one tablespace), not attached separate DBs; cross-store transaction boundary is real
- `TransitionPipelineStage` checks `RowsAffected` and rolls back on stale stage; no journey event emitted for transitions that didn't happen
- `gate_state` mutation API exposed via `Store.SetGateState` and `UpdateParams.GateState`
- Checkpoint flow uses correlation IDs (`checkpoint_id` UUIDv7); explicit stale-ack, late-ack, and wrong-worker-id rules
- Score-suppression formula handles `last_contradicted_at IS NULL` correctly; never-contradicted cards are not suppressed
- `cards.RecordCardEvent` defined as the atomic write contract; concurrent acks/nacks serialize via single-tx UPDATE with bounded clamp
- Card schema gains `promotion_confidence` column; §18.5 verifier asserts on it (sourced from `bead_learnings_pending.candidate.confidence`)
- Prompt API migration is two-phase (compat layer → call-site migration); Phase B.3a adds `WorkerPromptParams`/`AssembleWorkerPrompt` alongside the existing API; old API kept as deprecation alias
- Worktree-collision section corrected to match actual `pkg/dispatcher/dispatcher.go:4809` path-derivation; defers to existing `applyRestoredAssignments` quarantine flow
- Glossary §20 uses the correct closed bead-type enum (`task | bug | chore | epic | research | premortem | review`)

**Changelog v1 → v2:**
- Closed journey enum + actor enum cover all referenced events (was: `pipeline_stage_changed`, `imported`, `migration` referenced but missing)
- `CardCandidate` JSON schema explicit; promotion rules can read `confidence` and `evidence` from persisted data
- Bead type enum reconciled with v20: `task | bug | epic | research | chore` + additive `premortem | review`. `implementation`/`fix` removed (were not in v20)
- Worker prompt API matches `pkg/worker/prompt.go` shape (`PromptParams` → `WorkerPromptParams`, no `ctx`, returns `string`); routing lives in `pkg/dispatcher/router.go`
- `pkg/memory` migration is read-shadow → dual-write → cutover (was: one-shot, ignoring active writers)
- Checkpoint flow uses `checkpoint_requested`/`checkpoint_acked`/`checkpoint_failed` (added to enum); pre-checkpoint worker crash and worktree-collision paths covered
- `Store.TransitionPipelineStage` is the only mutation path for `pipeline_stage`; atomic with journey append
- `CountChildren` exposed; premortem gate fires retroactively when child count crosses threshold via `gate_state` column
- Research-spawned children carry `awaits_parent_close` tag; `beads_ready` view excludes them while parent open
- Card score capped at +5.0; `last_contradicted_at` triggers per-type suppression window so contradictions stay visible
- Renders run inside read transaction (`Store.WithReadTx`); errors fail the render rather than producing inconsistent output
- Acceptance test replaced with 8 E2E tests covering checkpoint, oracle chain, learning promotion, premortem gate, edit corpus, codestruct
- Phase F.0 pre-Phase-F ouros compatibility spike added as gating; constrained-surface fallback documented if upstream API doesn't match assumptions

---

## 1. Executive Summary

Oro is a structurally mature autonomous coding swarm — bead-driven, worktree-isolated, TDD-gated, ops-reviewed. Five reference repos (`llm-tldr`, `ContinuousClaudeV4.7`, `ouros`, `fastedit`, `bloks`) collectively reveal a more capable harness architecture: closed knowledge loops, dispatcher-enforced context safety, two-agent splits (worker / oracle), and the deletion of lossy prose artifacts in favor of structured stores.

This spec absorbs that architecture into oro under a unified frame:

> **Oro becomes a programming harness with two stores and one rendering plane.**
>
> - **Beads** carry working state (per-bead journey, next action, blockers, linked artifacts, worker state).
> - **Cards** carry durable knowledge (typed, scored, lineage, decay, retirement).
> - **Renders** (`oro current`, `oro handoff`, `oro resume`) compute views on demand from those stores. Nothing is hand-written. Nothing rots.

The build items:

1. **`pkg/codestruct`** — tree-sitter-backed AST + call-graph for Go, Python, TypeScript/JavaScript on day one. Replaces the AGPL `tldr` integration. Worker prompts get structured nav-maps instead of raw bytes. Ops review gets `oro impact` for change-blast-radius.
2. **`pkg/edit`** — deterministic Go AST editor across the same four languages. Replaces fastedit's deterministic 74% path; falls through to native `Edit` for the 26% structural-rewrite minority. Drops the 1.7B merge model entirely (Go accuracy was 77%; deterministic-only is 100%).
3. **`pkg/cards`** — typed card store with ack/nack scoring, lineage, progressive disclosure, retirement. Replaces `pkg/memory`. Captures bloks' card abstraction without the library indexer.
4. **Bead schema v2** — addendum to the replatform spec. Beads absorb `journey`, `next_action`, `blockers`, `learnings_pending`, `linked_artifacts`, `worker_state`. Together with cards, this swallows current.md and handoff documents.
5. **Dispatcher-enforced context safety** — workers never degrade past threshold. Dispatcher pre-empts at configurable budget, checkpoints structured state to the bead, respawns clean. CCv4.7's three-hook loop, ported native into Go.
6. **Two-agent split (worker / oracle)** — implementation beads route to workers (Edit/Write/Read/Bash). Research beads route to oracles (ouros sandbox + web/doc search, no Edit/Write). Beads can chain: oracle produces a recommendation bead, worker implements.
7. **Ouros as a research tool** — adopted (not built), scoped to oracle agents only, plays the same architectural role as `agent-browser`. Vendored or cargo-installed at oro setup.
8. **Pipeline stages (ASSESS → PLAN → PREMORTEM → PREPARE → EXECUTE → VALIDATE → EVOLVE)** — every stage produces structured data into beads or cards. Premortem becomes a required gate for non-trivial epics. Evolve closes the knowledge loop via card promotion.
9. **Enforcement-hierarchy doctrine** — meta-rule for every other rule. *Lint > type system > formatter > pre-commit > CI > CLAUDE.md (last resort).* Audit existing rules; convert what we can to deterministic enforcement.

The deletions:

- `current.md` — replaced by `oro current` render
- `docs/handoffs/*.md` as stored artifacts — replaced by `oro handoff` render
- `pkg/memory` (flat key-value) — replaced by `pkg/cards`
- "Update current.md before starting a task" rule
- "Hand off with explicit context" rule
- create-handoff / resume-handoff as document-writing skills (become render invocations)

The architectural payoff: **oro can do what CCv4.7 cannot**. CCv4.7 lives inside Claude Code, so it has to bolt context safety, knowledge scoring, and routing on as hooks. Oro is the dispatcher — it owns the worker lifecycle. Context safety becomes a dispatcher concern, not a script-level race against the model. Routing becomes a bead-type dispatch, not a slash-command in a prompt. Knowledge scoring becomes a bead-completion side effect, not a manual `bloks learn` invocation.

---

## 2. Why This Spec Exists

### 2.1 The lossy-handoff problem

`current.md` and `docs/handoffs/*.md` are prose serializations of structured state, hand-written, never re-validated. Every problem they have flows from that:

- **Hand-written = often skipped.** "I'll update current.md" loses to "let me just push." Untouched files rot in a week.
- **Prose loses structure.** Bead IDs, file paths, decisions, blockers — all melted into paragraphs that the next session re-parses to act on.
- **Goes stale immediately.** A snapshot of state at write-time. By the next session, half of it is outdated and you can't tell which half.
- **Drifts from reality.** "Files modified: X, Y, Z" — git already knows. "Next steps: A, B" — the bead already had acceptance criteria. Duplicate state always disagrees.
- **No scoring, no decay.** Every old handoff has equal weight in the directory. There's no "this one matters because it's still in flight" signal.

The fix isn't to make prose less lossy. It's to recognize that everything in current.md and handoffs is already structured data, badly serialized — and to delete the prose layer entirely.

### 2.2 The context-degradation problem

Workers degrade silently past 45% context. The current playbook ("kill workers proactively if stuck") is reactive, lossy, and depends on a human watching `oro-dash`. CCv4.7 solves this with three hooks (status, auto-handoff-stop, pre-compact). Oro can do better — the dispatcher *is* the supervisor, so context safety becomes a control-loop primitive, not a script-level intervention.

### 2.3 The flat-memory problem

`pkg/memory` is keyed retrieval over flat entries. There's no:

- **Scoring.** A correction issued once is identical to a correction confirmed across 30 beads.
- **Lineage.** No record of which bead produced a memory, which beads confirmed it, which contradicted it.
- **Decay.** Stale entries weight equally with fresh ones.
- **Typing.** A taste rule and a library fact are stored the same way.
- **Progressive disclosure.** Workers see all-or-nothing — full memory entry or nothing.

bloks' card system shows what this should look like. CCv4.7's `bloks learn` integration shows how the loop closes.

### 2.4 The token-waste problem

Workers see raw file contents in prompts. A 500-line file consumes ~7K tokens to deliver structural information that a 30-line nav-map could carry. `tldr` solves this; we'd integrate it but pay for AGPL legal review and a Python sidecar. Building `pkg/codestruct` ourselves removes both costs and lets us tie nav-map generation to bead context (e.g., highlight the symbol the bead targets).

### 2.5 The single-agent-shape problem

Today every worker has the same prompt, the same tool surface, the same isolation level. Research beads (open-ended exploration) and implementation beads (bounded execution) want different shapes. CCv4.7's worker/oracle split is the right pattern. Oro adopts it.

### 2.6 The integration-cost problem (the original framing)

The companion `2026-04-27-external-tooling-integration-spec.md` framed this as "integrate five tools" — adopting `tldr`, `bloks`, `ouros`, `fastedit`, CCv4.7 hooks as sidecars. Three of those decisions don't survive scrutiny:

- **`tldr`** brings AGPL legal review and a Python sidecar for capability we can build natively in 2-3 weeks of Go.
- **`bloks`** brings a library indexer we don't need (oro is project-scoped) and a Rust runtime, when the abstraction we want is just a card schema.
- **`fastedit`** brings a 1.7B merge model with 77% Go accuracy, when the deterministic 74% path captures most of the value at 100% accuracy.

Two decisions stand:

- **`ouros`** is real engineering we can't reasonably reproduce; adopt as the research-agent surface.
- **CCv4.7's architecture** is a pattern catalog, not a code dependency; absorb the patterns native to oro.

---

## 3. The Unified Memory Model

### 3.1 The four layers

```
┌──────────────────────────────────────────────────────────────┐
│                  ORO MEMORY ARCHITECTURE                     │
├──────────────────────────────────────────────────────────────┤
│                                                              │
│  WORKING STATE   →  beads  (per-bead journey, next_action,   │
│                              blockers, linked_artifacts,     │
│                              worker_state, learnings_pending)│
│                                                              │
│  DURABLE KNOW.   →  cards  (typed, scored, lineage, decay,   │
│                              progressive disclosure,         │
│                              retirement / supersession)      │
│                                                              │
│  CODE STRUCTURE  →  pkg/codestruct  (computed on demand from │
│                                       tree-sitter; never     │
│                                       stored)                │
│                                                              │
│  HISTORY         →  bead store (immutable bead lifecycle) +  │
│                     git (immutable code lifecycle)           │
│                                                              │
├──────────────────────────────────────────────────────────────┤
│                                                              │
│  RENDERS (read-only, computed on demand):                    │
│   oro current   →  in-progress beads + recent journey +      │
│                    cards in flight                           │
│   oro handoff   →  same shape, scoped to session boundary    │
│   oro resume    →  routes to a bead, loads journey + cards   │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

### 3.2 Single source of truth per concern

| Concern | Truth source |
|---|---|
| What is the worker doing now? | `bead.status` + `bead.journey` (last events) |
| What did I learn from bead X? | `bead.learnings_pending` (transient) → promoted to `cards` (durable) |
| What's the codebase structure? | `pkg/codestruct` computes from current tree-sitter AST |
| What did we change? | `git log` + `bead.linked_artifacts` (commits, files, PR) |
| Why did we make decision D? | `cards` of type `decision` with lineage to producing bead |
| What's the team's taste? | `cards` of type `taste` |
| What's the next action on this bead? | `bead.next_action` (structured, not prose) |
| What's blocking this bead? | `bead.blockers` (structured `[{kind, ref, reason}]`) |
| Which previous corrections matter for this bead? | Cards filtered by relevance + score + ack history |

### 3.3 Renders are not artifacts

`oro current`, `oro handoff`, `oro resume` are **read-only views** computed from beads and cards. They produce text (markdown for human consumption, structured JSON for machine consumption), but they never write back to disk as a stored document. The truth lives in the stores; the render is ephemeral.

This is the architectural inversion. Today: write `current.md` → next session reads it. Tomorrow: next session calls `oro current` → reads beads + cards directly.

### 3.4 Why two stores, not one

Beads and cards have different write profiles, different lifecycles, different retrieval patterns:

| | Beads | Cards |
|---|---|---|
| Lifecycle | Open → in_progress → closed | Created → confirmed → retired/superseded |
| Write rate | Dozens per day per active project | One or two per bead, on average |
| Hot path | journey appends per worker turn | rare; ack/nack on bead close |
| Retrieval | by status, parent, ID | by relevance/tags, scored |
| Decay | none (immutable history once closed) | yes (staleness reduces score) |
| Renders | current/handoff/resume | injected into worker prompts |

A single store conflates these. Two stores keep schema and queries clean.

### 3.5 What the worker prompt becomes

Today's worker prompt has 12 sections; the new shape adds two and removes one:

| Section | Source | Status |
|---|---|---|
| Role | static | unchanged |
| Bead | beadstore | unchanged |
| Cards (was: Previous Feedback + Memory) | cards (progressive disclosure) | **REPLACES** Previous Feedback + Memory |
| Code Structure | pkg/codestruct | **NEW** |
| Relevant Code | pkg/codestruct (deep level) | unchanged shape, sourced from codestruct |
| Git History | git | unchanged |
| Coding Rules | static | unchanged |
| Worker Program | static | unchanged |
| TDD | static | unchanged |
| Quality Gate | static | unchanged |
| Worktree | dispatcher | unchanged |
| Context Handoff | bead.journey + bead.next_action | **REPLACES** prose handoff sections |

The worker no longer reads `current.md` or any handoff document. It reads the bead's structured journey and next_action.

---

## 4. Bead Schema v2 (Addendum to Replatform Spec)

### 4.1 Relationship to v20 replatform

The v20 `pkg/beadstore` spec defines a 12-method `Store` interface and a SQLite schema with these bead columns:

```
id, title, type, priority, status, description, acceptance_criteria,
parent_id, tags (json), created_at, updated_at, closed_at, deferred_until
```

This v2 addendum adds fields without changing the existing methods or breaking the v20 acceptance test. It introduces new methods on `Store` for journey/artifact/learning operations.

### 4.2 New columns

```sql
ALTER TABLE beads ADD COLUMN next_action TEXT;
ALTER TABLE beads ADD COLUMN blockers TEXT;          -- JSON array of {kind, ref, reason}
ALTER TABLE beads ADD COLUMN linked_artifacts TEXT;  -- JSON {worktree, commits[], files_touched[], pr_url}
ALTER TABLE beads ADD COLUMN worker_state TEXT;      -- JSON {last_context_pct, last_checkpoint_ts, retry_count, last_worker_id}
```

`learnings_pending` is its own table because it's append-heavy:

```sql
CREATE TABLE bead_learnings_pending (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  bead_id              TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
  ts                   TEXT NOT NULL,                     -- RFC3339Nano, like other timestamps
  candidate            TEXT NOT NULL,                     -- JSON CardCandidate (shape below)
  promoted_to          TEXT REFERENCES cards(id),         -- NULL until promoted
  rejected_at          TEXT,                              -- if explicitly rejected, not promoted
  reason               TEXT,
  queued_for_review_at TEXT                               -- set when candidate goes to human review queue (R4.7)
);
CREATE INDEX idx_learnings_bead    ON bead_learnings_pending(bead_id);
CREATE INDEX idx_learnings_pending ON bead_learnings_pending(promoted_to, rejected_at);
CREATE INDEX idx_learnings_review  ON bead_learnings_pending(queued_for_review_at)
  WHERE queued_for_review_at IS NOT NULL AND promoted_to IS NULL AND rejected_at IS NULL;
```

A learning has exactly four terminal-or-pending states, all expressible as a single SQL predicate over the three nullable timestamp/ref columns:

| State | promoted_to | rejected_at | queued_for_review_at |
|---|---|---|---|
| Pending (in flight) | NULL | NULL | NULL |
| Queued for review (force-close limbo) | NULL | NULL | NOT NULL |
| Promoted to card (terminal) | NOT NULL | NULL | NULL or NOT NULL |
| Rejected (terminal) | NULL | NOT NULL | NULL or NOT NULL |

A sweeper `pkg/dispatcher/sweep.go:ExpireReviewQueueSLA(ctx, store)` runs hourly:

```sql
UPDATE bead_learnings_pending
   SET rejected_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
       reason      = 'review_queue_sla_expired'
 WHERE queued_for_review_at IS NOT NULL
   AND promoted_to IS NULL
   AND rejected_at IS NULL
   AND datetime(queued_for_review_at) <
       datetime('now', printf('-%d days', :sla_days));
```

`sla_days` defaults to 60 and is configurable. After the sweep, `rejected_at` is non-NULL so the row is no longer in the review-queue query result. Every learning row eventually reaches a terminal state (`promoted_to` or `rejected_at` non-NULL); the index on `queued_for_review_at` keeps the sweep cheap regardless of total table size.

`CardCandidate` JSON shape (the value stored in `candidate`):

```json
{
  "type":         "rule|taste|pattern|decision|fact",   // required
  "title":        "string",                              // required
  "body_summary": "string, < 200 chars",                 // required
  "body_full":    "string",                              // required
  "body_deep":    "string|null",                         // optional
  "tags":         ["string"],                            // required (may be empty)
  "confidence":   0.0..1.0,                              // required; producer's self-rated confidence
  "evidence":     [                                      // required (may be empty for very low-confidence)
    {
      "kind":   "code|test|doc|external|trace|ops_review",
      "ref":    "file:line | url | bead_id | commit_sha | log_id",
      "quote":  "optional short quote or excerpt"
    }
  ],
  "source": {                                            // required
    "actor":          "worker|oracle|premortem|ops_review|human",
    "agent_session":  "subprocess id or session id",
    "supersedes":     "card_id|null",                    // if this candidate proposes replacement
    "contradicts":    ["card_id"]                        // if this candidate contradicts existing cards
  }
}
```

The `confidence` and `evidence` fields are required because §5.7 promotion rules read them. A candidate without evidence can be emitted but must have confidence ≤ 0.4 (it will fail auto-promotion thresholds). Promotion code reads only structured fields from `candidate`; it does not parse `body_full` for evidence.

`journey` is its own table because it's the hottest write path:

```sql
CREATE TABLE bead_journey (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  bead_id  TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
  ts       TEXT NOT NULL,                        -- RFC3339Nano
  actor    TEXT NOT NULL,                        -- 'worker' | 'oracle' | 'dispatcher' | 'ops_review' | 'human'
  event    TEXT NOT NULL,                        -- enum below
  payload  TEXT                                  -- JSON, event-specific
);
CREATE INDEX idx_journey_bead_ts ON bead_journey(bead_id, ts);
CREATE INDEX idx_journey_ts      ON bead_journey(ts);
```

### 4.3 Journey event vocabulary

Closed enum, not free-form:

```
claimed                | dispatcher assigned bead to worker
started                | worker began first turn
paused                 | bead paused (payload: reason)
resumed                | bead resumed from pause/defer
deferred               | bead deferred to a future date
context_warning        | dispatcher detected context% above warning threshold
checkpoint_requested   | dispatcher signalled worker to wind down for checkpoint
checkpoint_acked       | worker confirmed it has wound down (committed, no new edits)
checkpointed           | dispatcher persisted state and respawned worker
checkpoint_failed      | checkpoint flow itself failed (payload: error kind)
blocker_hit            | worker recorded a blocker
blocker_cleared        | blocker resolved
parent_blocked         | parent bead became unready, blocking this bead
qg_attempted           | quality gate run
qg_passed              | quality gate passed
qg_failed              | quality gate failed
ops_review_requested   | dispatcher submitted to ops review
ops_review_verdict     | ops review returned verdict (payload includes verdict + notes)
edit                   | worker made a code edit (payload: file, summary)
test_added             | worker added a test
commit                 | worker committed (payload: SHA, message)
learning_emitted          | worker proposed a card candidate
learning_promoted         | candidate promoted to a card (payload: card_id, learning_id)
learning_rejected         | candidate explicitly rejected (payload: learning_id, reason)
learning_deferred_to_review | candidate moved to human review queue (payload: learning_id, count, reason)
parent_closed_promoted    | child's awaits_parent_close tag stripped after parent close (payload: parent_id, child_id)
ack_card               | worker acked a card as useful (payload: card_id)
nack_card              | worker nacked a card as wrong/stale (payload: card_id, reason)
retried                | worker re-attempted after failure (payload: reason)
escalated              | worker escalated (payload: target, kind)
merged                 | dispatcher merged worktree to main
closed                 | bead closed (payload: reason)
imported               | bead/event imported from a prior store during migration
pipeline_stage_changed | bead transitioned pipeline stages (payload: {from, to})
gate_state_changed     | bead gate_state transitioned (payload: {from, to, reason})
sandbox_session_start  | oracle opened an ouros sandbox session (payload: session_id)
sandbox_session_killed | oracle's sandbox session ended (payload: session_id, reason)
premortem_veto         | premortem agent vetoed proceeding (payload: findings)
note                   | freeform note from human or agent (use sparingly)
```

Closed actor enum (§4.4 references `actor`):

```
worker      | implementation worker subprocess
oracle      | research worker subprocess
dispatcher  | dispatcher process
ops_review  | ops review process
premortem   | premortem agent
human       | human-issued event (CLI, dashboard)
migration   | one-shot migration script (used only during schema migrations)
system      | other system events (auto-promotions, sweepers)
```

Adding new events or actors requires a beadstore minor version bump and an additive migration. Renaming or removing events requires a major version bump.

### 4.4 New `Store` methods

```go
type Store interface {
    // ... 12 existing v20 methods ...

    // Journey
    AppendJourney(ctx context.Context, beadID string, evt JourneyEvent) error
    Journey(ctx context.Context, beadID string, since time.Time) ([]JourneyEvent, error)
    LatestJourney(ctx context.Context, beadID string, limit int) ([]JourneyEvent, error)

    // Working-state setters (idempotent)
    SetNextAction(ctx context.Context, beadID, nextAction string) error
    SetBlockers(ctx context.Context, beadID string, blockers []Blocker) error
    SetLinkedArtifacts(ctx context.Context, beadID string, art LinkedArtifacts) error
    SetWorkerState(ctx context.Context, beadID string, ws WorkerState) error

    // Learnings
    AppendLearningPending(ctx context.Context, beadID string, c CardCandidate) (id int64, err error)
    PromoteLearning(ctx context.Context, learningID int64, cardID string) error
    RejectLearning(ctx context.Context, learningID int64, reason string) error
    PendingLearnings(ctx context.Context, beadID string) ([]PendingLearning, error)

    // Children / dependency queries (§11.4 premortem gate, §10.4 parent-research blocking)
    CountChildren(ctx context.Context, parentID string) (int, error)
    Children(ctx context.Context, parentID string) ([]protocol.Bead, error)

    // Gate-state mutation (§11.4) — reads/writes bead.gate_state. v20's
    // UpdateParams gains a *string GateState field; this method is the
    // explicit accessor used when only the gate_state changes.
    //
    // Like TransitionPipelineStage, SetGateState appends an audit event
    // (`gate_state_changed`) atomically with the column update. Reason
    // text is required so the journey records *why* the state changed
    // (e.g., "premortem verdict=proceed", "6th child created").
    SetGateState(ctx context.Context, beadID string, from, to GateState, reason string) error

    // Pipeline stage transitions — MUST be atomic with the journey event append
    // (§11.9). Implementations execute the column update and the bead_journey
    // INSERT in a single transaction. Returns ErrStaleStage if `from` does not
    // match the current pipeline_stage.
    TransitionPipelineStage(ctx context.Context, beadID string, from, to PipelineStage) error
}
```

`AppendJourney` is the hot-path method. It must be a single INSERT with no read-modify-write cycle. Implementations should not lock or serialize; SQLite WAL mode handles concurrent appends.

`TransitionPipelineStage` is the only allowed way to mutate `bead.pipeline_stage`. Direct UPDATE statements against the column are forbidden in application code; this is enforced by a beadstore-internal check at compile time (the column is unexported in the struct used by sqlc-generated mutators). The atomic transaction shape:

```go
// pseudo-Go for clarity; the SQL is run inside a single sql.Tx
func (s *SQLiteStore) TransitionPipelineStage(ctx context.Context, beadID string, from, to PipelineStage) error {
    return s.withTx(ctx, func(tx *sql.Tx) error {
        res, err := tx.ExecContext(ctx, `
            UPDATE beads
               SET pipeline_stage = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
             WHERE id = ?
               AND pipeline_stage = ?
               AND deleted = 0
        `, to, beadID, from)
        if err != nil { return fmt.Errorf("update pipeline_stage: %w", err) }

        n, err := res.RowsAffected()
        if err != nil { return fmt.Errorf("rows affected: %w", err) }
        if n == 0 {
            // Stage drift: another writer changed the stage, or bead was deleted.
            // Roll back the transaction by returning the sentinel; do NOT append
            // the pipeline_stage_changed event for a transition that did not happen.
            return ErrStaleStage
        }

        _, err = tx.ExecContext(ctx, `
            INSERT INTO bead_journey (bead_id, ts, actor, event, payload)
            VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), 'dispatcher',
                    'pipeline_stage_changed',
                    json_object('from', ?, 'to', ?))
        `, beadID, from, to)
        if err != nil { return fmt.Errorf("append journey: %w", err) }

        return nil
    })
}
```

If `RowsAffected == 0` the function returns `ErrStaleStage`, the transaction rolls back, and **no journey event is appended** for a transition that didn't happen. The caller MUST re-fetch the bead and decide whether to retry or abandon the transition. This prevents two dispatchers (the supervised + a respawned watchdog) from racing the state machine and emitting contradictory journey events.

### 4.5 Hot-path performance budget

The journey table is the highest-write-volume table in oro. Bench targets, measured on commodity hardware (M-series Mac):

| Op | p50 | p99 |
|---|---|---|
| `AppendJourney` (single event) | < 1 ms | < 5 ms |
| `LatestJourney` (last 50 events of one bead) | < 2 ms | < 10 ms |
| `Journey` (full journey of one bead, 1000 events) | < 20 ms | < 50 ms |

If we miss these targets, the v20 spec's SQLite WAL configuration may need tuning (`busy_timeout`, `synchronous`, page size). The replatform spec's bench gate must be re-run with journey events included before declaring the v20 acceptance test passing.

### 4.6 Migration path (additive, no data loss)

#### Phase A.1 — Complete schema v20 → v3 migration

Phase A.1 is the **single inventory** of every schema mutation introduced anywhere in this spec. Running Phase A.1 to completion is the gate for every subsequent phase. If a downstream phase's acceptance test depends on a schema change, that change MUST be in this list.

**4.6.a Bead-table column additions** (§4.2):
```sql
ALTER TABLE beads ADD COLUMN next_action       TEXT;
ALTER TABLE beads ADD COLUMN blockers          TEXT;          -- JSON
ALTER TABLE beads ADD COLUMN linked_artifacts  TEXT;          -- JSON
ALTER TABLE beads ADD COLUMN worker_state      TEXT;          -- JSON
```

**4.6.b Bead-table column additions for v3 round-2 fixes**:
```sql
-- §11.4 premortem gate state
ALTER TABLE beads ADD COLUMN gate_state TEXT NOT NULL DEFAULT 'none'
  CHECK (gate_state IN ('none','eligible','satisfied','blocked','replan'));

-- §11.4 premortem cycle counter (R4.6) — bounds the replan loop
ALTER TABLE beads ADD COLUMN premortem_cycle_count INTEGER NOT NULL DEFAULT 0;

-- §11.9 pipeline stage (mutated only via TransitionPipelineStage)
ALTER TABLE beads ADD COLUMN pipeline_stage TEXT
  CHECK (pipeline_stage IN ('assess','plan','premortem','prepare','execute','validate','evolve','none'));

-- §8.6 oracle / research bead fields
ALTER TABLE beads ADD COLUMN sandbox_session       TEXT;
ALTER TABLE beads ADD COLUMN allowed_external_fns  TEXT;     -- JSON

-- §9.4 per-bead context-safety threshold overrides
ALTER TABLE beads ADD COLUMN context_thresholds TEXT;        -- JSON {warning, checkpoint}
```

**4.6.c Bead type CHECK update** (§10.2 — additively extends v20's enum):
```sql
-- Drop and recreate the CHECK constraint via table rebuild (SQLite limitation).
-- Migration tool generates the dance: rename → create with new CHECK → copy → drop old.
-- New enum: 'task','bug','chore','epic','research','premortem','review'
```
The migration tool encapsulates the CHECK rebuild dance (SQLite cannot directly modify CHECK on a column without table rebuild). Existing rows are preserved verbatim.

**4.6.d New tables**:
```sql
-- §4.2 bead_journey (hot-path, append-only)
CREATE TABLE bead_journey ( ... );
CREATE INDEX idx_journey_bead_ts ON bead_journey(bead_id, ts);
CREATE INDEX idx_journey_ts      ON bead_journey(ts);

-- §4.2 bead_learnings_pending
CREATE TABLE bead_learnings_pending ( ... );
CREATE INDEX idx_learnings_bead    ON bead_learnings_pending(bead_id);
CREATE INDEX idx_learnings_pending ON bead_learnings_pending(promoted_to, rejected_at);

-- §5.3 cards
CREATE TABLE cards ( ... );
CREATE INDEX idx_cards_type_score ON cards(type, score DESC) WHERE retired_at IS NULL;
CREATE INDEX idx_cards_tags       ON cards(tags);

-- §5.3 card_events
CREATE TABLE card_events ( ... );
CREATE INDEX idx_card_events_card_ts ON card_events(card_id, ts);
```

**4.6.e View rewrites** (§10.4 — drop-and-recreate, atomic):
```sql
DROP VIEW IF EXISTS beads_ready;
CREATE VIEW beads_ready AS ... ;        -- amended per §10.4 (bead_tags + awaits_parent_close + deleted-parent guard per R3.4)

DROP VIEW IF EXISTS beads_blocked;
CREATE VIEW beads_blocked AS ... ;      -- amended in concert per §10.4
```

**4.6.f Backfill** (data only; runs after every DDL above):
1. For every existing bead, emit a synthetic journey event with `event='imported', actor='migration', ts=COALESCE(bead.created_at, datetime('now')), payload=json_object('source','v20-migration')`. For closed beads, emit a second `event='closed'` event with `ts=COALESCE(bead.closed_at, bead.updated_at, bead.created_at, datetime('now'))`. The COALESCE chain handles legacy beads where one or more timestamps may be NULL — the migration tool selects the most authoritative non-NULL timestamp; if all are NULL, the migration's own clock is used. The journey table's `ts TEXT NOT NULL` constraint is therefore always satisfied.
2. `gate_state='none'` on every legacy bead (default).
3. `pipeline_stage='none'` on every open bead (closed beads get `pipeline_stage=NULL` since they're post-pipeline).
4. `sandbox_session=NULL`, `allowed_external_fns=NULL`, `context_thresholds=NULL` on every legacy bead.
5. No backfill for `next_action`, `blockers`, `linked_artifacts`, `worker_state` — these populate on the next worker turn.

**4.6.g Acceptance gate before Phase B**:
- All v20 acceptance tests still pass
- Journey bench targets met (§4.5)
- `oro current` runs against the migrated DB and produces a non-empty render with no errors
- `EXPLAIN QUERY PLAN SELECT * FROM beads_ready` shows the amended view uses the expected indexes
- `oro bead create --type=premortem` and `--type=review` succeed (CHECK constraint accepts new types)

If any gate item fails, Phase A is incomplete and Phase B does not start.

### 4.7 Rendering: how `oro current` reads beads

Renders run inside a single read transaction so the user sees a consistent snapshot — never a half-state where bead.status disagrees with the latest journey event because they were read at different moments.

Cards and beads live in the same SQLite database file (`$ORO_HOME/state.db`) — different tables in one schema, not attached databases. This is intentional: a single `BEGIN DEFERRED` covers reads across `beads`, `bead_journey`, `cards`, and `card_events` atomically. The earlier draft text claiming "logically separate tables" or "separate database" was wrong; it is one database, one tablespace, one transaction boundary.

The Store interfaces both expose a non-generic transaction wrapper:

```go
type Store interface {
    // ... existing methods ...

    // WithReadTx executes fn inside a SQLite BEGIN DEFERRED ... COMMIT block.
    // Implementation MUST set isolation to read-only to avoid blocking writers.
    // SQLite in WAL mode gives each reader a consistent snapshot at BEGIN time;
    // see https://www.sqlite.org/wal.html#concurrency.
    //
    // Generic methods are not legal in Go interfaces, so the result is conveyed
    // via the closure rather than a return parameter; callers capture into a
    // local variable inside fn.
    WithReadTx(ctx context.Context, fn func(tx ReadTx) error) error
}

// ReadTx is a read-only view that exposes the full query surface of Store.
// It is what the closure receives; the writer methods are unexported on
// concrete impls so a closure cannot mutate state inside a read transaction.
//
// Every read method on Store has a counterpart on ReadTx with identical
// signature. The complete set required for `oro current`, `oro handoff`,
// `oro resume`, and `oro pipeline status`:
type ReadTx interface {
    // Bead state queries (mirror Store)
    Ready(ctx context.Context) ([]protocol.Bead, error)
    InProgress(ctx context.Context) ([]protocol.Bead, error)
    Blocked(ctx context.Context) ([]protocol.Bead, error)
    Closed(ctx context.Context, limit int) ([]protocol.Bead, error)
    Show(ctx context.Context, id string) (*protocol.Bead, error)

    // Hierarchy
    HasChildren(ctx context.Context, parentID string) (bool, error)
    AllChildrenClosed(ctx context.Context, parentID string) (bool, error)
    CountChildren(ctx context.Context, parentID string) (int, error)
    Children(ctx context.Context, parentID string) ([]protocol.Bead, error)
    FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error)

    // Journey
    Journey(ctx context.Context, beadID string, since time.Time) ([]JourneyEvent, error)
    LatestJourney(ctx context.Context, beadID string, limit int) ([]JourneyEvent, error)

    // Learnings
    PendingLearnings(ctx context.Context, beadID string) ([]PendingLearning, error)

    // Cross-store accessor: same transaction extends to cards reads
    Cards() cards.ReadTx
}
```

`ReadTx` exposes every read method that any render path (`oro current`, `oro handoff`, `oro resume`, `oro pipeline status`) needs. It does **not** mirror every method on `Store` — `Export` (v20's snapshot dump for backup/audit) is intentionally absent because it has its own transactional semantics (it streams the whole DB and manages its own consistency, not part of any render). New read methods added to `Store` must be added to `ReadTx` in the same change **if** they are reachable from a render path; bulk-export methods like `Export` stay outside the parity contract.

The compatibility test (`pkg/beadstore/readtx_test.go`) uses reflection to verify every method on `ReadTx` exists on `Store` with an identical signature, and that every render path's call graph reaches only methods that exist on `ReadTx`. The reverse direction (every `Store` read is on `ReadTx`) is enforced by hand for render-relevant methods, with `Export` explicitly listed as exempt.

`cards.ReadTx` is the read-only counterpart for the cards store. Because both stores share the SQLite connection, `Store.WithReadTx` opens one transaction; the `ReadTx.Cards()` accessor returns a `cards.ReadTx` bound to the same transaction object, so cards reads inside the closure participate in the same snapshot.

The render becomes:

```go
func RenderCurrent(ctx context.Context, store Store) (CurrentView, error) {
    var out CurrentView
    err := store.WithReadTx(ctx, func(tx ReadTx) error {
        beads, err := tx.InProgress(ctx)
        if err != nil { return fmt.Errorf("InProgress: %w", err) }

        out = CurrentView{Snapshot: time.Now().UTC()}
        for _, b := range beads {
            journey, err := tx.LatestJourney(ctx, b.ID, 20)
            if err != nil { return fmt.Errorf("LatestJourney(%s): %w", b.ID, err) }

            cardSet, err := tx.Cards().Relevant(ctx, RelevanceFromBead(b))
            if err != nil { return fmt.Errorf("Cards.Relevant(%s): %w", b.ID, err) }

            out.Beads = append(out.Beads, RenderedBead{
                Bead: b,
                RecentEvents: journey,
                RelevantCards: cardSet,
            })
        }
        return nil
    })
    return out, err
}
```

Errors are surfaced; `oro current` does **not** silently skip a bead because reading its journey failed. The render fails closed — any error inside `WithReadTx` aborts the entire render and the command exits non-zero. This is the single defined behavior:

- DB unreachable (state.db deleted, locked) → exit 1, message: `"beadstore unreachable: <error>"`
- Single bead's journey fetch fails → exit 1, message: `"render failed (LatestJourney for bead-X): <error>"`
- Cards retrieval fails → exit 1, message: `"render failed (Cards.Relevant for bead-X): <error>"`

The render is a snapshot or it is nothing. There is no "partial render with warnings" mode in v3 — it was a contradiction between the pseudo-code (which returned errors) and the failure-mode text (which suggested partial output). The acceptance test (§18.1) treats any non-zero exit as a render failure.

If a future variant needs degraded rendering (for example, an `oro current --tolerate-errors` flag for use in panic-debug situations), it is added explicitly with its own acceptance criteria — not as the default behavior of `oro current`.

`oro current` calls into `RenderCurrent`, marshals to markdown for human consumption, returns. No file write. No staleness. The output prefix includes the snapshot timestamp so callers can correlate against logs.

`oro handoff` is `RenderCurrent` scoped to a session window (configurable; default: events from last N hours, where N defaults to 4) — same transactional guarantees.

`oro resume` takes a bead ID, fetches the bead + full journey + relevant cards inside one transaction, and drops the user/agent into the bead context. Replaces resume-handoff entirely.

---

## 5. Card System (`pkg/cards`)

### 5.1 Why cards, not flat memory

bloks shows that "knowledge for an AI agent" is not a single shape. A taste rule ("we always use compound components"), a pattern ("auth flow uses middleware X then validator Y"), a decision ("Drizzle over Prisma because Z"), a fact ("library W returns nil on edge case E") — these have different semantics, different relevance criteria, different decay profiles, different rendering needs.

Today `pkg/memory` flattens them all. The card system types them.

### 5.2 Card types (closed enum)

```
rule       | "Always X" / "Never Y" — actionable, deterministic
taste      | "We prefer X over Y because of design judgment Z"
pattern    | "When you need to do X, the codebase pattern is Y"
decision   | "We chose X over Y on date D for reason R" — historical
fact       | "Library X returns nil on input Y" — observed truth about external systems
```

The categories shape rendering: rules show up in worker prompts under "Coding Rules" with imperative language; tastes under "Design Considerations" with "we prefer"; patterns under "Codebase Patterns" with code excerpts; decisions under "Background" for context; facts under "Known Behaviors."

### 5.3 Card schema

```sql
CREATE TABLE cards (
  id                   TEXT PRIMARY KEY,                    -- 'card-<8char>'
  type                 TEXT NOT NULL CHECK (type IN ('rule','taste','pattern','decision','fact')),
  title                TEXT NOT NULL,
  body_summary         TEXT NOT NULL,                       -- one line, < 200 chars
  body_full            TEXT NOT NULL,                       -- prose, can include code blocks
  body_deep            TEXT,                                -- optional; deep dive, examples, edge cases
  tags                 TEXT NOT NULL DEFAULT '[]',          -- JSON array
  score                REAL NOT NULL DEFAULT 1.0,           -- usefulness; ack +, nack -, decay -
  promotion_confidence REAL,                                -- the confidence score from the promoted candidate; NULL for legacy/manual cards
  decay_anchor         TEXT NOT NULL,                       -- last-touch ts, for decay calculation
  last_contradicted_at TEXT,                                -- last `contradicted` event ts; NULL = never (§5.4 suppression)
  last_nacked_at       TEXT,                                -- last `nack` event ts; NULL = never
  created_at           TEXT NOT NULL,                       -- RFC3339Nano
  updated_at           TEXT NOT NULL,
  retired_at           TEXT,                                -- non-null when retired
  superseded_by        TEXT REFERENCES cards(id),           -- when retirement is replacement
  emerged_from         TEXT REFERENCES beads(id),           -- bead that produced this card
  retired_reason       TEXT
);
CREATE INDEX idx_cards_type_score ON cards(type, score DESC) WHERE retired_at IS NULL;
CREATE INDEX idx_cards_tags ON cards(tags);
```

`promotion_confidence` is set from `bead_learnings_pending.candidate.confidence` at promotion time. Legacy-imported cards (Phase D.2) get `promotion_confidence = NULL`. Manual cards created via `oro cards create` get `NULL` unless the user passes `--confidence`.

Card lineage events (separate table, append-only):

```sql
CREATE TABLE card_events (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  card_id   TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
  ts        TEXT NOT NULL,                              -- RFC3339Nano
  bead_id   TEXT REFERENCES beads(id),                  -- the bead acting on this card (NULL for human acks)
  actor     TEXT NOT NULL,                              -- 'worker' | 'oracle' | 'human' | 'system'
  kind      TEXT NOT NULL,                              -- enum below
  payload   TEXT                                        -- JSON
);
CREATE INDEX idx_card_events_card_ts ON card_events(card_id, ts);
```

Event kinds:

```
created        | initial creation, payload includes promotion source
ack            | bead used this card and signals it was helpful
nack           | bead found this card wrong or stale
contradicted   | a new bead's experience contradicts this card
confirmed      | a new bead's experience confirms this card
retired        | retirement event; payload includes reason
superseded     | retired in favor of another card; payload includes superseder
```

### 5.4 Scoring

Score is a real number with these update rules, applied at event-time (not lazily):

```
ack            : score += 0.3,  decay_anchor = now
confirmed      : score += 0.2,  decay_anchor = now
nack           : score -= 0.5,  decay_anchor = now (note: no auto-retire)
contradicted   : score -= 0.4,  decay_anchor = now,
                 last_contradicted_at = now
created        : score = 1.0
```

Score is **capped at +5.0** to prevent runaway accumulation. A card that has 17 acks (5.1) caps at 5.0, so a single contradicted (-0.4) drops it to 4.6 — visible. Without a cap, 100 acks would yield score ~30 and a single contradiction would be invisible.

```sql
ALTER TABLE cards ADD COLUMN last_contradicted_at TEXT;  -- RFC3339Nano
ALTER TABLE cards ADD COLUMN last_nacked_at TEXT;
```

Effective score at retrieval time uses two multipliers:

```
decay        = exp(-(now - card.decay_anchor) / half_life(type))

# Suppression is 1.0 (no suppression) when the card has never been
# contradicted (NULL) OR the most recent contradiction is older than the
# window. Otherwise 0.0 (suppressed). The IS NULL clause is required —
# a SQL NULL never compares true to a timestamp, so without it every
# never-contradicted card would be suppressed.
suppression  = (last_contradicted_at IS NULL
                OR last_contradicted_at < (now - contradiction_suppression_window))
               ? 1.0 : 0.0

effective_score(card) = card.score * decay * suppression
                       (clamped to [0, +5.0]; underflow → 0)
```

In SQL the same logic appears as:

```sql
CASE
  WHEN c.last_contradicted_at IS NULL
    OR datetime(c.last_contradicted_at) <
       datetime('now', printf('-%d seconds', :suppression_window_seconds))
  THEN 1.0
  ELSE 0.0
END AS suppression
```

So: a card with score 4.8 and a contradiction 3 days ago has `effective_score = 0` and is hidden from default retrieval until the suppression window expires. A card that has never been contradicted (`last_contradicted_at IS NULL`) is never suppressed by this rule. To restore a contradicted card, a worker must emit a `confirmed` event after the contradiction (which updates `decay_anchor` and resets suppression by writing `last_contradicted_at = NULL`).

The exp() decay is computed in seconds; for a 5-year-old card with 180-day half_life, the result is `exp(-1825 / 180) = ~3.95e-5`, well above any sensible underflow. Implementations clamp `effective_score` to `[0, +5.0]` and treat values below `1e-6` as 0.

Per-type half-lives (configurable; defaults):

```
rule    : 365 days   (rules age slowly)
taste   : 180 days
pattern : 180 days
decision: 730 days   (historical decisions stay relevant a long time)
fact    : 90 days    (external facts go stale fastest)
```

Per-type contradiction suppression windows (configurable; defaults):

```
rule    : 14 days
taste   : 14 days
pattern : 14 days
decision: 30 days    (contradictions to decisions get more scrutiny)
fact    : 7 days     (external facts can flip back fast)
```

Default-retrieval threshold: `effective_score >= 0.1`. Workers can still surface suppressed cards via explicit query (`oro cards search --include-low --include-suppressed`).

A card with raw score below `-1.0` is auto-retired with `retired_reason='auto: persistent nack'`. The score floor is `-2.0`; further nacks past that floor do not change the score (prevents extreme negative scores from being misinterpreted). (Open question: is auto-retirement the right behavior? See §17.)

### 5.5 Progressive disclosure

`body_summary` always renders in the worker prompt's Cards section. `body_full` renders only when the worker prompt has token budget (configurable per bead). `body_deep` is fetched on demand by the worker via `oro cards show <id>`.

Worker prompt section structure:

```
=== Cards (deck view) ===

[rule]    Always handle errors with %w        score 2.4   id card-a1b2c3d4
[pattern] Auth uses middleware X then val Y   score 1.8   id card-e5f6g7h8
[fact]    SQLite locks on .db-shm contention  score 0.9   id card-i9j0k1l2
...

To see full body of any card: cite the id and request the body via oro cards show.
```

If the worker prompt has space (configured per worker config), `body_full` is inlined for the top-3 highest-scored relevant cards. Otherwise the deck view is sufficient and the worker requests deep content as needed.

### 5.6 Lineage and contradiction

Every card knows the bead it emerged from (`emerged_from`). Every ack/nack records the bead doing the acking. Workers can ask "which bead taught us this?" and "which beads have used it since?"

When a bead nacks a card with `kind=contradicted`, the card's score drops but it's not retired. The pattern is: contradictions accumulate; if a card receives N independent contradictions over T time, a `pkg/cards` background sweep proposes retirement (or supersession) and an ops review confirms.

Auto-supersession is **not** implemented in v1. A worker that finds a contradiction emits a *new* card candidate (the corrected understanding) and nacks the old card. A future ops review or human action is required to retire the old card and link the supersession.

### 5.7 Card promotion path

The flow from learning to durable card:

1. **Bead in flight** — worker emits a card candidate via `bead.learnings_pending` table (kind=`learning_emitted` journey event).
2. **Bead closes** — ops review (or evolve stage) walks `learnings_pending` for the bead.
3. **Promotion decision** — for each candidate, the rule depends on the closing context:

   | Closing context | Behavior on `learnings_pending` |
   |---|---|
   | Closed via ops review with verdict=`pass` | Apply auto-promotion rules (below) |
   | Closed via ops review with verdict=`fail` | All pending candidates auto-rejected with `rejected_at=now, reason='ops_review_failed'` |
   | Closed via ops review with verdict=`needs_more` | Bead reopens; candidates remain pending |
   | Force-closed by human (no ops_review_verdict event) | All pending candidates moved to the review queue: `queued_for_review_at` set to now (NOT NULL); `promoted_to` and `rejected_at` remain NULL. The journey records `learning_deferred_to_review { count: N, reason: 'force_close_no_review' }` so the deferral is auditable. Visible via `oro cards review-queue` (which queries `WHERE queued_for_review_at IS NOT NULL AND promoted_to IS NULL AND rejected_at IS NULL`). |
   | Bead deleted (soft) | Pending candidates auto-rejected with reason `'parent_bead_deleted'`. |

4. **Promoted candidate becomes card** — `cards` row created; `learnings_pending.promoted_to` set; `card_events` row with `kind=created`.

Auto-promotion rules (only applied when verdict=`pass`):
- Auto-promote if: confidence threshold met, no near-duplicate existing card.
- Reject if: contradicts an existing high-score card without ack/nack rationale → `rejected_at=now, reason='contradicts_card_<id>_without_rationale'`.
- Defer if: needs human review (rare; for ambiguous taste/decision candidates) → moved to review queue.

Auto-promotion is conservative: only `rule` and `pattern` types auto-promote. `taste` and `decision` always require human review (one-tap "promote" command). `fact` auto-promotes when emerged from a bead with passing ops review and confirmed by a second bead within 14 days.

The review queue (`oro cards review-queue`) is bounded: candidates older than 60 days that are still unresolved are auto-rejected with `reason='review_queue_sla_expired'`. This guarantees `learnings_pending` does not grow unbounded.

### 5.8 Migration from `pkg/memory`

`pkg/memory` is **not** read-only. Active writers in the current code:
- `pkg/dispatcher/dispatcher.go:2241,2253,2266` — `d.memories.Insert(...)` for QG retry context, ops verdicts, learnings
- `pkg/worker/drain.go:24+` — `MemoryInserter` interface, called inline as worker output is parsed

A naive one-shot migration would lose any writes that land between the `cards` cutover and the `pkg/memory` retirement. Migration uses a **read-shadow → dual-write → cutover** sequence:

#### Phase D.1 — Schema, retrieval, scoring (no writes yet)
- Create `cards`, `card_events` tables.
- Implement `pkg/cards.Store` with read APIs (`Relevant`, `Show`, `List`).
- No write paths active. `pkg/memory` continues as today.

#### Phase D.2 — One-shot import (read-only on `pkg/memory`)
For every existing memory entry:
1. Classify by tag heuristic:
   - `feedback_*` → type `rule`
   - `fix_*` → type `pattern`
   - `decision_*` → type `decision`
   - everything else → type `pattern` (default)
2. Title from first line; body_summary from second line if present, else truncated body; body_full from full content.
3. `score = 1.0`, `decay_anchor = now`, `created_at = entry.created_at` if available else `now`.
4. `emerged_from = NULL` (lineage lost for legacy entries; acceptable).
5. Add `card.tags` to include `legacy_memory` so we can track imported records.
The import script is idempotent on re-run (skips cards already present with matching content hash).

#### Phase D.3 — Dual-write window (≥ 7 days)
- `pkg/memory.Insert` is wrapped by a shim in `pkg/cards/legacy_writer.go`. Every successful `memory.Insert(...)` is mirrored synchronously into `cards` as a `pattern`-type card with `tags=['legacy_memory_dual_write', <original tags>]`. Failures in the cards write log a warning and **do not** fail the memory insert.
- Workers and dispatcher continue writing to `pkg/memory` only; the shim handles cards mirroring.
- A drift detector runs daily (`oro cards check-drift`) and reports any memory entries newer than the latest cards mirror — these are dual-write failures requiring investigation.

#### Phase D.4 — Read cutover
- Worker prompt's Cards section reads from `pkg/cards` (via `Relevant`).
- `pkg/memory` retrieval paths (e.g. `ForPrompt`) become a thin shim over `pkg/cards` that converts the requested shape.
- Workers no longer see "Memory" or "Previous Feedback" sections in their prompt (these names are removed; content surfaces under "Cards").

#### Phase D.5 — Write cutover

Dispatcher and `pkg/worker/drain.go` switch from `memory.Insert` to the appropriate `pkg/cards` and `pkg/beadstore` calls. There is no `pkg/cards.Append` — the API surface is what §5.9 defines:

| Today's call | Replacement | Notes |
|---|---|---|
| `memory.Insert(ctx, params)` for a worker-emitted learning | `Store.AppendLearningPending(ctx, beadID, candidate)` | candidate is the §4.2 `CardCandidate` JSON; promotion happens later via `Store.PromoteLearning` driven by the EVOLVE stage |
| `memory.Insert(ctx, params)` for a confirmed durable knowledge entry (e.g., dispatcher logging an ops-review-confirmed pattern) | `cards.Store.Create(ctx, CardCreateParams)` | Use only when the entry is known-correct and bypasses the candidate→promotion path; rare |
| Worker emits an `ack`/`nack`/`confirmed`/`contradicted` against an existing card | `cards.Store.RecordCardEvent(ctx, CardEvent)` | Atomic write contract per §5.9 |
| Dispatcher records an explicit retirement | `cards.Store.Retire(ctx, id, reason, supersededBy)` | |

The `pkg/cards/legacy_writer.go` shim is reversed: writes go to cards (via the calls above), mirrored to memory only for the same dual-write window so any external readers of memory keep working.

#### Phase D.6 — `pkg/memory` retirement
- Dual-write disabled; memory becomes read-only.
- After 14 days with zero memory reads (verified via metrics), the package is removed.

The migration is **not** one-shot. The dual-write window is the safety mechanism that finding 7 (writes during migration) demands.

### 5.9 Retrieval and write API

```go
type RelevanceQuery struct {
    BeadType        string         // 'task', 'bug', 'chore', 'research', 'premortem', 'review'
    BeadTags        []string
    BeadDescription string
    SymbolHints     []string       // from pkg/codestruct nav-map
    MaxTokens       int            // budget for body inclusion
    IncludeLowScore bool           // include cards below default threshold
    IncludeSuppressed bool         // include cards in contradiction-suppression window
}

type RelevantCards struct {
    Deck     []CardSummary  // body_summary only, all relevant
    Inlined  []CardSummary  // body_full inlined, fits within MaxTokens
}

type cards.Store interface {
    // Read
    Relevant(ctx context.Context, q RelevanceQuery) (RelevantCards, error)
    Show(ctx context.Context, id string) (*Card, error)
    List(ctx context.Context, q ListQuery) ([]Card, error)

    // Write — all event recording is atomic with score mutation
    RecordCardEvent(ctx context.Context, e CardEvent) error
    Create(ctx context.Context, c CardCreateParams) (*Card, error)
    Retire(ctx context.Context, id, reason string, supersededBy string) error

    // Tx wrapper (mirrors beadstore §4.7)
    WithReadTx(ctx context.Context, fn func(tx ReadTx) error) error
}
```

`RecordCardEvent` is the atomic write contract for every score-affecting event (`ack`, `nack`, `confirmed`, `contradicted`, `created`, `retired`, `superseded`). Implementation runs inside a single transaction with a CAS-style update so concurrent acks/nacks don't lose updates:

```go
func (s *SQLiteCardStore) RecordCardEvent(ctx context.Context, e CardEvent) error {
    return s.withTx(ctx, func(tx *sql.Tx) error {
        // 1. Insert the event (always; even if score update is no-op)
        _, err := tx.ExecContext(ctx, `
            INSERT INTO card_events (card_id, ts, bead_id, actor, kind, payload)
            VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), ?, ?, ?, ?)
        `, e.CardID, e.BeadID, e.Actor, e.Kind, e.Payload)
        if err != nil { return fmt.Errorf("insert event: %w", err) }

        // 2. Apply score delta atomically with bounds-clamp via SQL.
        //    The `clearsContradiction` flag is true for `confirmed` events so the
        //    suppression marker is reset; without this, prose-claimed recovery
        //    via `confirmed` would not actually unsuppress the card (R3.2).
        delta, anchor, contradicted, nacked, clearsContradiction := scoreDelta(e.Kind)
        _, err = tx.ExecContext(ctx, `
            UPDATE cards
               SET score = MIN(MAX(score + ?, ?), ?),
                   decay_anchor = ?,
                   last_contradicted_at = CASE
                     WHEN ?            THEN ?       -- contradicted=true: stamp
                     WHEN ?            THEN NULL    -- clearsContradiction=true: reset
                     ELSE last_contradicted_at
                   END,
                   last_nacked_at = CASE WHEN ? THEN ? ELSE last_nacked_at END,
                   updated_at = ?
             WHERE id = ?
        `, delta, scoreFloor, scoreCap,
           anchor, contradicted, anchor, clearsContradiction, nacked, anchor, anchor, e.CardID)
        if err != nil { return fmt.Errorf("update score: %w", err) }

        // 3. Auto-retire if score crosses the floor
        _, err = tx.ExecContext(ctx, `
            UPDATE cards
               SET retired_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
                   retired_reason = 'auto: persistent nack'
             WHERE id = ? AND retired_at IS NULL AND score <= ?
        `, e.CardID, autoRetireThreshold)
        return err
    })
}
```

Because `score` is updated with `MIN(MAX(score + ?, floor), cap)` inside the same transaction that inserts the event, two concurrent `ack` events on the same card serialize via SQLite's WAL writer lock. No update is lost; both events appear in `card_events`; the score reflects both deltas.

`scoreDelta` is a pure function over event kind. Returns `(delta, anchor, setContradicted, setNacked, clearsContradiction)`:
```
ack          → (+0.3, anchor=now, false, false, false)
confirmed    → (+0.2, anchor=now, false, false, true)   ← clears suppression
nack         → (-0.5, anchor=now, false, true,  false)
contradicted → (-0.4, anchor=now, true,  false, false)
created      → (no-op via this path; created via Create)
retired      → (no-op; via Retire)
```

A `confirmed` event after a contradiction restores the card from suppression by writing `last_contradicted_at = NULL`. This matches the prose recovery path in §5.4.
```

Relevance is a weighted combination of:
- Tag overlap with bead tags (Jaccard similarity, weight 0.4)
- Description text similarity (TF-IDF over body_summary, weight 0.3)
- Symbol overlap with bead symbol hints (exact match, weight 0.2)
- Bead-type filter (rules and patterns always; tastes/decisions only when matched, weight 0.1)
- Effective score multiplier (cards below threshold dropped unless `IncludeLowScore`)

Implementation: build a per-card relevance signal, sort by effective_score × relevance_weight, return top-N for deck and top-K for inlined where K saturates `MaxTokens`.

---

## 6. Code Structure (`pkg/codestruct`)

### 6.1 Scope

Build a Go-native AST + call-graph engine that supports Go, Python, TypeScript, and JavaScript on day one. Replaces the AGPL `tldr` integration entirely. Powers:

- Worker prompt **Code Structure** section (nav-maps for relevant files)
- `oro impact <symbol>` subcommand (call-graph blast-radius for ops review)
- `pkg/edit` symbol resolution (shared tree-sitter pipeline)

### 6.2 Languages and tree-sitter

Use `github.com/smacker/go-tree-sitter` with these grammars:

- `tree-sitter-go` — Go
- `tree-sitter-python` — Python
- `tree-sitter-typescript` — TypeScript
- `tree-sitter-tsx` — TSX (React)
- `tree-sitter-javascript` — JavaScript (covers JSX too via grammar)

A file's language is detected by extension first, shebang second:

```
.go              → Go
.py, .pyi        → Python
.ts              → TypeScript
.tsx             → TSX
.js, .mjs, .cjs  → JavaScript
.jsx             → JavaScript (JSX dialect via JS grammar)
```

### 6.3 Two layers, not five

`tldr` builds five analysis layers (AST, calls, CFG, DFG, PDG). We need only two:

- **Layer 1 — Symbol map**: every public/exported function, class, method, type, interface, with line range and a one-line signature summary.
- **Layer 2 — Call graph**: edges between symbols (caller → callee) within the project tree.

Layers 3-5 (control flow, data flow, program dependence) require deep semantic analysis per language. We don't use them in any worker prompt or ops review surface today, and the marginal benefit doesn't justify the per-language complexity. If a future feature needs them, they're additive.

### 6.4 Symbol extraction per language

Each language gets a small extractor that maps tree-sitter node kinds to canonical `Symbol` records:

```go
type Symbol struct {
    Name      string         // function/method/class name
    Kind      SymbolKind     // 'func', 'method', 'class', 'type', 'interface', 'const', 'var'
    Receiver  string         // for methods (Go: "*Worker"; Python: "ClassName"; TS: "ClassName")
    Signature string         // first line of the symbol, normalized
    LineStart int
    LineEnd   int
    Visibility string        // 'exported', 'unexported', 'private' (per-language semantics)
    Decorators []string      // for Python/TS where applicable
}
```

Per-language node-kind targets (non-exhaustive):

```
Go:
  function_declaration, method_declaration, type_declaration,
  type_spec (struct/interface), const_declaration, var_declaration

Python:
  function_definition, async_function_definition, class_definition,
  decorated_definition (wraps function/class)

TypeScript:
  function_declaration, method_definition, class_declaration,
  interface_declaration, type_alias_declaration,
  variable_declaration (with arrow function)

JavaScript (incl. JSX):
  function_declaration, method_definition, class_declaration,
  variable_declaration with arrow function or function expression
```

### 6.5 Call-graph extraction

Walk every call expression node in every file. For each call, resolve the callee to a symbol when possible:

- **Same-package call**: callee name resolves to a symbol in the same file or package.
- **Imported call**: callee uses an import alias; resolve to the imported package's symbol if in-project.
- **Method call**: receiver-typed method; resolve via type information when available (Go: package + receiver type; TS: class type via `tree-sitter`'s parent class info; Python: same).
- **Unresolved**: external library, dynamic dispatch, or AST-unresolvable. Log at low level; don't fail.

Storage: call edges are stored in an in-memory graph for the project (re-built per query) plus an optional on-disk index (`pkg/codestruct/index.db`, SQLite) for faster repeated queries. Cache invalidated when any indexed file's mtime changes.

### 6.6 The on-demand model

Codestruct does **not** keep a persistent daemon (`tldr`'s daemon is its 100ms-query trick; we don't need that latency). Each `pkg/codestruct` operation:

1. Parses the requested file(s) with tree-sitter
2. Computes the requested layer (Layer 1 or Layer 2)
3. Returns the result

For very large projects (>50k files), we keep an on-disk parsed-symbol cache keyed by `(path, mtime)`. Cache hits skip tree-sitter parsing. Cache rebuilds happen incrementally when mtimes change.

### 6.7 Worker prompt Code Structure section

Today workers see raw file content. With codestruct, every "relevant file" entry in the prompt becomes:

```
=== /pkg/dispatcher/dispatcher.go (1247 lines) ===

OUTLINE:
  type Config              [12-78]
  func NewConfig(...)      [80-95]
  type Dispatcher          [97-180]
  func (*Dispatcher) Run   [182-340]    ← target symbol (highlighted)
  func (*Dispatcher) Stop  [342-365]
  func runWork(...)        [367-410]
    [calls: Dispatcher.Run, executeWork, spawnAndWait]

[Excerpt around target symbol — first 80 lines of Dispatcher.Run, with
 surrounding context if budget allows]
```

The full file is **not** in the prompt. The outline + excerpt typically saves 80-90% of tokens vs raw file. The worker can request the full file via the `Read` tool if it determines it needs more.

### 6.8 `oro impact` subcommand

Used by ops review and (optionally) by workers before risky edits:

```
$ oro impact pkg/dispatcher/dispatcher.go:Dispatcher.Run
Symbol: Dispatcher.Run
Direct callers (in-project):
  pkg/cli/cmd_start.go:runStart      [calls 3x]
  pkg/dispatcher/dispatcher_test.go:TestRun
Transitive callers (depth 2):
  cmd/oro/main.go:main → runStart → Run
Cross-package callees (in this symbol):
  pkg/worker.AssemblePrompt
  pkg/ops.Review
  pkg/protocol.Bead
External callees:
  context.WithTimeout
  json.Marshal
```

Ops review uses this to gate "you changed Dispatcher.Run; here are the 4 callers that may need updating." Workers can use it to assess change blast-radius before committing.

### 6.9 What we do not build

- Layers 3-5 (CFG/DFG/PDG)
- Semantic search (BM25 / embeddings over the codebase)
- Persistent daemon
- Cross-language call graphs (a Go function calling a Python script via subprocess is unresolvable; we don't try)
- Macro / metaprogramming expansion (Go generics: handled; Python decorators: tagged but not expanded; TS type-level metaprogramming: skipped)

If a future bead requires any of these, additive build.

### 6.10 Bench targets

| Op | p50 | p99 | Cache state |
|---|---|---|---|
| Symbol extraction (1 file, 500 LOC) | < 5 ms | < 20 ms | warm |
| Call-graph for 1 package (10 files) | < 50 ms | < 200 ms | warm |
| `oro impact` (project-wide) | < 500 ms | < 2 s | warm, project ≤ 5k files |
| Full project index (cold) | < 30 s | < 60 s | cold, 5k files |

If we miss bench targets, the on-disk cache may need optimization (binary format vs SQLite, parallel parsing, etc.).

---

## 7. Native AST Editing (`pkg/edit`)

### 7.1 Scope

Build a deterministic AST-aware editor across Go, Python, TypeScript, and JavaScript. Replaces fastedit's deterministic 74% path. Drops the 1.7B merge model. Falls through to native `Edit` for the structural-rewrite 26%.

The promise: 100% accuracy on the deterministic-eligible cases (vs fastedit's 92-77%, language-dependent), no model latency, no model accuracy floor.

### 7.2 Three edit modes

| Mode | What it does | Tokens | Accuracy |
|---|---|---|---|
| `--after symbol` | Insert text after the named symbol | 0 | 100% (deterministic) |
| `--replace symbol` | Replace symbol body with snippet via anchor splice | 0 | 100% when eligible; fall-through otherwise |
| `--rename old new` | AST-aware rename (skip strings/comments) | 0 | 100% when scope clear |

`--replace` runs anchor splice; if any rule below fails, the operation is *not* attempted — instead it returns `EFALLTHROUGH` and the worker is instructed to use native `Edit`.

### 7.3 The anchor splice algorithm

Replace mode preserves the symbol's signature and only modifies the body. The algorithm:

1. **Locate** — tree-sitter finds the symbol; extract its body line range.
2. **Classify snippet lines** — each line of the snippet is either:
   - **Anchor line** — exactly matches a non-empty line in the original body
   - **New line** — does not match any line in the original body
   - **Continuation marker** — `# ...` (or `// ...`) indicates "preserve untouched lines here"
3. **Validate** — the splice is eligible if:
   - At least 2 anchor lines (otherwise we're unsure where to splice)
   - Anchors appear in original-body order (otherwise it's a reorder, not a splice)
   - Continuation markers are unambiguous (their position relative to anchors is clear)
   - Markerless segments drop at most 20 original lines; larger gaps require a continuation marker
4. **Splice** — rebuild the body by walking the original; for each original line, output it unless a snippet sequence overrides it.
5. **Re-parse** — run tree-sitter on the resulting file; it must still parse without errors. If it doesn't, the operation rolls back.

### 7.4 Per-language considerations

**Go**: Braced, signatures end in `{`, bodies are line-based. Easiest case. Anchor splice is straightforward. Decorators don't exist; method receivers are part of the signature.

**Python**: Significant whitespace. The snippet's indentation is normalized to match the target body's indent level before splicing. Continuation marker is `# ...`. Decorators (`@foo`) are part of the symbol; preserved across edit.

**TypeScript**: Braced like Go. Decorators (`@foo`) preserved. Type parameters in signatures preserved. Arrow functions assigned to variables resolve via the variable_declaration node.

**JavaScript (incl. JSX)**: Like TypeScript without types. JSX-bodied functions are fine — JSX is parsed by the grammar.

**Per-language gotchas tracked in test corpus** (Phase C.test):

```
Go:
  - generic functions with type parameters
  - methods with pointer vs value receivers
  - struct field tags

Python:
  - async def with decorators
  - class methods with @classmethod / @staticmethod
  - functions with default args containing function calls

TypeScript:
  - overloaded function signatures (multiple decls, one impl)
  - class with abstract methods
  - generic constraints (extends X)

JavaScript:
  - arrow functions assigned to const
  - class methods with computed keys [Symbol.iterator]
  - JSX returning conditional expressions
```

### 7.5 The 12-tool surface (worker-facing)

Mirroring fastedit's MCP surface (minus model-dependent operations):

```
oro edit:after FILE SYMBOL --snippet '...'
oro edit:replace FILE SYMBOL --snippet '...'
oro edit:delete FILE SYMBOL [--force]
oro edit:rename FILE OLD NEW
oro edit:rename-all DIR OLD NEW [--only KIND] [--dry-run]
oro edit:move FILE SYMBOL --after OTHER_SYMBOL
oro edit:move-to-file SYMBOL FROM_FILE TO_FILE [--dry-run]
oro edit:read FILE                     (returns symbol map; same as codestruct)
oro edit:diff FILE                     (shows pending edits to a file)
oro edit:undo FILE                     (reverses last edit)
oro edit:batch FILE --edits '[...]'    (atomic multi-edit)
oro edit:check                         (reparses all edited files; surfaces errors)
```

Workers invoke these via Bash. The dispatcher exposes them in the worker tool prompt section.

### 7.6 Fall-through to native `Edit`

When a `--replace` operation returns `EFALLTHROUGH`, the worker sees:

```
oro edit:replace failed: SPLICE_INELIGIBLE
Reason: only 1 anchor line matched; need at least 2.
Recommendation: use Edit tool with full block.
```

The worker then reaches for `Edit` and operates as it does today. We never silently misapply.

### 7.7 What we do not build (yet)

- A merge model (fastedit's 1.7B Qwen-Coder-1.5B-derived model). The deterministic path captures most of the wins.
- Cross-file ranger refactors (rename across N files: included; move-with-importer-rewrite: included; large-scale schema migrations: not in scope, use specialized tools).
- AST-aware diff visualization (`fastedit diff` shows text diff, not structural diff).
- Multi-symbol atomic edits across files (the tool can do per-file batches; cross-file atomic is left for a follow-up).

### 7.8 Bench and accuracy targets

| Op | p50 latency | Accuracy |
|---|---|---|
| `--after` | < 10 ms | 100% (text insertion) |
| `--replace` (eligible) | < 50 ms (parse + splice + reparse) | 100% (deterministic by construction) |
| `--rename` (single file) | < 50 ms | 100% on AST-resolvable cases |
| `--rename-all` (project, 5k files) | < 5 s warm cache | 100% on AST-resolvable cases |

Test corpus: 200 representative Go edits, 100 Python, 100 TypeScript, 50 JavaScript. Each edit has a known-correct expected outcome. We require 100% pass on the corpus to ship Phase C.

---

## 8. Research Agent Surface (Ouros)

### 8.1 Scope

Adopt `ouros` as the research-agent sandbox. Mount as a CLI surface (`oro sandbox`) similar to `agent-browser`. Available exclusively to oracle-typed workers (research beads). Implementation workers do not get this surface.

### 8.2 Why adopt, not build

`ouros` is a Rust-implemented sandboxed Python interpreter with snapshot/fork/resume, ~1µs startup, explicit external-function bridging, and >70 stdlib modules. Building it is a multi-year effort. The capability — sandboxed Python with snapshotting — is unique enough that we adopt rather than approximate.

### 8.3 Installation

Two options:

- **Vendored binary**: `oro init` downloads a pre-built `ouros` binary for the host platform (mac arm64, linux x86_64, linux arm64) into `$ORO_HOME/bin/ouros`. Pinned to a specific upstream version per `oro` release.
- **Cargo install**: if the user has Rust toolchain available, `oro init --build` runs `cargo install ouros --version X.Y.Z` and symlinks into `$ORO_HOME/bin/`.

Default: vendored binary. Build path is for users who prefer reproducible builds from source.

**Vendored-binary feasibility caveat:** ouros is a Rust project with platform-specific PyO3 bindings. Pre-built binaries are not currently published by upstream. Phase F.0 (the spike, §8.10) is responsible for confirming that:
1. Upstream ouros publishes per-platform binaries on a stable release channel, OR
2. We can host pre-built binaries ourselves (CI builds across mac arm64, linux x86_64, linux arm64), OR
3. The fallback is `cargo install` with a clear error if Rust toolchain is missing.

If none of (1)-(3) is workable, the architectural fallback is to keep ouros optional and treat research beads as a best-effort capability that degrades to a constrained tool surface (web_search + scoped read only, no sandbox state) when ouros is missing.

If `ouros` is unavailable at runtime (binary missing, version mismatch), research beads emit `journey: blocker_hit { kind: 'ouros_unavailable' }` and fall back to the constrained surface described above. Implementation beads continue unaffected.

### 8.4 The `oro sandbox` surface

```
oro sandbox run --code '...' [--inputs JSON] [--external-functions f1,f2,...]
oro sandbox start --code '...' --session SESSION_ID [...]
oro sandbox resume --session SESSION_ID --return-value JSON
oro sandbox snapshot --session SESSION_ID
oro sandbox fork SESSION_ID NEW_SESSION_ID
oro sandbox list-vars --session SESSION_ID
oro sandbox get-var --session SESSION_ID --name VAR
oro sandbox kill --session SESSION_ID
```

Sessions are persisted under `$ORO_HOME/sessions/<session-id>/`. Each session has a serializable snapshot file plus metadata. Sessions auto-expire after 24 hours of inactivity (configurable).

### 8.5 External function bridge

Oracles in research beads need real-world data. The bridge exposes a tightly-scoped set of tools:

```
web_search(query, num_results=5)         → list of search hits with snippets
doc_search(library, query)               → docs.io / package-doc-search
read_file_scoped(path)                   → file read, scoped to project tree
glob_files_scoped(pattern)               → glob, scoped to project tree
llm_query(prompt, model='haiku', max_tokens=500) → sub-LLM call for synthesis
```

Each tool is a Go function exposed as an external callback to the ouros sandbox. The sandbox can only invoke functions that the oracle's bead config has explicitly allowlisted.

The sandbox cannot:
- Read files outside the project tree
- Write files anywhere except `/tmp/oro-sandbox/<session-id>/` (oro-managed scratch)
- Execute subprocesses
- Open network sockets directly (must go through `web_search` / `doc_search`)
- Access environment variables (sandbox runs with explicit empty env)

### 8.6 Oracle bead shape

A research bead has `bead.type = "research"` and additional fields:

```sql
ALTER TABLE beads ADD COLUMN sandbox_session TEXT;
ALTER TABLE beads ADD COLUMN allowed_external_fns TEXT;  -- JSON array
```

Default `allowed_external_fns` for research beads:
```json
["web_search", "doc_search", "read_file_scoped", "glob_files_scoped", "llm_query"]
```

The dispatcher injects an oracle prompt (different from the worker prompt) and routes the bead to a worker subprocess configured with:
- No `Edit`, `Write`, or `Bash` tools that touch the project tree
- `oro sandbox` available as a tool
- Cards relevant to research-type beads (different filter than implementation)
- Acceptance criteria for research beads is "produce a recommendation card or spawn an implementation bead"

### 8.7 Oracle output

Research beads produce one of two structured outputs:

- **Recommendation card**: a card candidate (type=`pattern`, `decision`, `taste`, or `fact`) emitted via `learnings_pending`, with confidence + supporting evidence
- **Spawned implementation bead**: a child bead created via `Store.Create`, with `parent_id` set to the research bead, ready for implementation routing

### 8.8 What ouros gives us beyond Bash

A worker today can run arbitrary Python via `Bash python3 -c '...'`. What ouros adds:

- **Persistence across turns** — variables survive
- **Snapshot + fork** — explore branch A, snapshot, try branch B, return
- **No host pollution** — sandbox can't write files in the worktree, no cleanup needed
- **Faster startup** — µs vs ~100ms for `python3 -c ''`
- **Type checking** — bundled `ty` from astral; catches errors before execution
- **Resource limits** — memory, allocations, time bounded; runaway code killed

For research patterns ("query an API, parse, correlate, summarize, repeat"), ouros wins. For implementation work, Bash is fine.

### 8.9 Acceptance test

```
Cmd: oro sandbox run --code 'print(2+3)' --inputs '{}'
Assert: stdout contains "5"
```

```
Cmd: oro sandbox start --code 'x = web_search(query); print(len(x))' \
       --inputs '{"query":"oro programming harness"}' \
       --external-functions web_search \
       --session test-1
Assert: returns either OurosSnapshot or completion with stdout
```

### 8.10 Phase F.0 — Pre-Phase-F compatibility spike (gating)

Before any Phase F work begins, run a one-week compatibility spike to validate the assumptions in §8.4–§8.6 against actual upstream ouros. The spike is gating: Phase F does not start until F.0 produces a green report.

**Spike deliverables:**

1. **Pinned-version verification.** Pick an ouros release (target: latest stable), pin the version, build the binary on each target platform (mac arm64, linux x86_64, linux arm64). Document build time, artifact size, and any platform-specific failures.

2. **API contract conformance.** Verify each method in §8.4's CLI surface against the actual ouros Python/CLI API:
   - Does ouros expose a CLI matching `sandbox run/start/resume/snapshot/fork/list-vars/get-var/kill`? **If no, document the actual API and either adapt §8.4 to match, or build a thin Go shim around the Python/JS bindings.**
   - Does the snapshot API serialize state to a file we can store under `$ORO_HOME/sessions/<id>/`?
   - Does the external-function callback shape match Go-side calling convention (Go function → ouros host call)?

3. **External-function bridge prototype.** Implement `web_search`, `doc_search`, `read_file_scoped`, `glob_files_scoped`, `llm_query` as Go functions registered with ouros. Verify:
   - Bridging works without leaking host file paths
   - Resource limits actually kill runaway code
   - Sandbox cannot escape via `__import__` / decoder tricks (run a small adversarial test suite)

4. **Distribution decision.** Based on (1)-(3), choose one of:
   - **(A)** Vendor pre-built binaries via oro's CI (we host them; users get them on `oro init`).
   - **(B)** Require `cargo install ouros` at oro init; fail loudly if Rust toolchain absent.
   - **(C)** Drop ouros entirely; ship the constrained-surface fallback as the only research surface.

5. **Documented fallback.** Whatever the outcome, the spec is updated to match reality before Phase F starts. If (C) wins, §8 is rewritten to describe the constrained surface as primary, ouros as a future capability.

**Gate criteria:** Phase F.0 passes when:
- An ouros version is pinned and a working vendoring story exists (one of A/B/C above)
- The §8.4 CLI surface matches ouros's actual API or the spec is updated to match
- The §8.5 external-function bridge has been implemented at least as a prototype with the 5 named functions
- The acceptance tests in §8.9 run successfully against the chosen distribution path

If the gate fails after a 2-week spike window, Phase F is descoped to (C) — the constrained-surface fallback — and the harness ships without snapshot/fork/resume capability for research beads. This is acceptable: the constrained surface still offers web_search, doc_search, scoped reads, and llm_query, which covers the majority of research-bead value.

---

## 9. Dispatcher-Enforced Context Safety

### 9.1 The control loop

Today: workers degrade past 45% context; humans watch `oro-dash` and kill stuck workers manually. Recovery is lossy (stuck-worker beads don't always preserve their journey).

New control loop: the dispatcher tracks worker context% per turn and pre-empts at threshold. The worker never reaches the degradation zone.

```
Worker turn N completes
  ↓
Dispatcher reads worker output and metadata (context%, last action)
  ↓
Append journey events for the turn (edit, test, qg_attempted, etc.)
  ↓
Check thresholds:
  - context% >= warning_threshold (default 65%): emit context_warning event,
    no other action
  - context% >= checkpoint_threshold (default 75%): trigger checkpoint
    flow (§9.3)
  ↓
If not checkpointing: dispatch turn N+1
```

### 9.2 Reading context%

Worker subprocesses (Claude Code / Codex / Gemini etc.) emit context% via:

- **Claude Code**: parse the worker's transcript or status output (`{"event":"turn_end","context_pct":68}`)
- **Codex**: equivalent emission via Codex CLI session metadata
- **Gemini**: similar

The dispatcher already parses worker output for QG events; context% parsing is additive. Each worker shim (Claude / Codex / Gemini) implements `WorkerShim.ContextPct() (float64, error)`.

### 9.3 Checkpoint flow

Each checkpoint has a unique `checkpoint_id` (a UUIDv7 generated by the dispatcher when the threshold is crossed). The id is included in `checkpoint_requested`, `checkpoint_acked`, `checkpoint_failed`, and `checkpointed` events. Acks not matching the current checkpoint_id are recorded as journey notes but do not mutate bead state.

When `context% >= checkpoint_threshold`:

1. Dispatcher emits journey event `checkpoint_requested` with payload `{checkpoint_id: <uuid>, worker_id: <pid_or_session>, trigger: "context_threshold", context_pct: <observed>, deadline_seconds: 30}`.
2. Dispatcher signals worker to **finish current action cleanly** (commit if in progress, no new edits). Signal is shim-specific:
   - Claude: in-band system message appended to next turn's input
   - Codex: equivalent CODEX_SYSTEM control message
   - Gemini: equivalent
3. Worker on next turn emits journey event `checkpoint_acked` with payload `{checkpoint_id: <uuid>, committed_sha: <sha or null>, intent_summary: <one line>}`.
   - **Stale-ack rule:** if the dispatcher receives a `checkpoint_acked` whose `checkpoint_id` is not the current one (e.g., a late ack from a prior checkpoint, or from a worker that has since been respawned), it appends the event as a `note` event with payload `{kind: "stale_checkpoint_ack", original_id: <uuid>}` and **does not** mutate `bead.next_action`, `worker_state`, or trigger any further state transitions.
   - **Late-ack rule:** if the ack arrives after `deadline_seconds`, dispatcher proceeds with `forced=true` (step 5) regardless. A subsequent late-ack with the same `checkpoint_id` is recorded as `note` with payload `{kind: "late_checkpoint_ack"}` but does not retroactively update state.
   - **Wrong-worker rule:** if the ack arrives from a `worker_id` other than the one in the current `checkpoint_requested` event, treat as stale. This prevents a respawned worker from acknowledging the previous worker's checkpoint.
   - If the worker fails to ack at all within `deadline_seconds`, dispatcher proceeds anyway and notes `forced=true` in the next `checkpointed` event payload.
4. Dispatcher persists structured state to the bead in a single transaction:
   - `SetNextAction(bead.id, <ack.intent_summary or last claimed intent>)`
   - `SetWorkerState(bead.id, {last_context_pct, last_checkpoint_ts, retry_count++, last_worker_id})`
   - `SetLinkedArtifacts(bead.id, {worktree, commits=[...current heads...], files_touched=[...], pr_url})`
5. Dispatcher emits `journey: checkpointed` event with payload `{checkpoint_id: <uuid, same as the requested>, forced: bool, retry_count, prev_worker_id}`. The `checkpoint_id` ties this terminal event back to the corresponding `checkpoint_requested` and `checkpoint_acked` events, so post-hoc audit can reconstruct the full lifecycle of any single checkpoint. After a dispatcher restart, the in-memory "current checkpoint" state is reconstructed by reading the bead's journey for the most recent `checkpoint_requested` whose corresponding `checkpoint_id` has no following `checkpointed` or `checkpoint_failed` event — that's the in-flight checkpoint, if any.
6. Dispatcher kills worker subprocess.
7. If checkpoint flow itself errored at any step (DB write failure, worktree inaccessible, etc.), dispatcher emits `checkpoint_failed` with payload `{step, error}` and increments retry_count; bead is deferred for human review if `worker_state.retry_count >= max_checkpoint_retries` (default 3).
8. Dispatcher spawns a fresh worker with:
   - The bead loaded from store (full journey, next_action, blockers, cards)
   - Worktree preserved (worker reattaches)
   - The checkpoint event noted in the prompt: "Resuming from checkpoint at TS; previous action: X; next action: Y"
9. Worker resumes work; turn 1 of new worker = turn N+1 of bead's lifetime.

The bead's journey is never lost. The bead's worktree is preserved. The bead's progress continues.

If a worker crashes between dispatcher reading `context%` and dispatcher appending journey events, the watchdog (`pkg/dispatcher/watchdog.go`) detects the dead PID and emits `journey: retried` with payload `{reason: "worker_died_pre_checkpoint"}`. The pre-crash context% is lost; this is acceptable because the next worker spawn starts at 0% and the bead's journey/next_action is intact.

Worktree paths are derived from bead.id: `filepath.Join(repoRoot, ".worktrees", beadID)` (`pkg/dispatcher/dispatcher.go:4809`). Two distinct beads cannot map to the same path by construction. The dispatcher tracks the live mapping in `worktreeByBead` (`pkg/dispatcher/dispatcher.go:637, 1808, 3437, 3479, 3517, 3529, 3594, 4639`).

The actual race the spec must handle is **dispatcher restart with stale worktrees on disk**: a previous dispatcher process crashed leaving a worktree directory, and the new dispatcher needs to either reattach (if the bead is still in_progress) or quarantine (if state is inconsistent). `pkg/dispatcher/dispatcher.go:4949 applyRestoredAssignments` already implements this discovery path; the spec defers to it. If `applyRestoredAssignments` finds a worktree directory whose corresponding bead is closed, deleted, or missing, it emits `journey: escalated { kind: "missing_worktree_path" | "stale_worktree" }` (per the existing quarantine codes at `dispatcher.go:4917-4924`) and the bead is deferred for human review.

This spec adds no new collision detection beyond what's already in the dispatcher. The §4.4 `Store.LinkedArtifacts` field captures the worktree path on each turn so a restart-survivor dispatcher has authoritative metadata to reconcile against the on-disk state.

### 9.4 Why thresholds are configurable

Different workers, different model sizes, different prompt budgets. The defaults (warning=65%, checkpoint=75%) are tuned for Claude Code with the current 12-section worker prompt. Configuration lives in `oro.toml`:

```toml
[dispatcher.context_safety]
warning_threshold = 0.65
checkpoint_threshold = 0.75
```

Per-bead overrides:

```sql
ALTER TABLE beads ADD COLUMN context_thresholds TEXT;  -- JSON {warning, checkpoint}
```

For long-running research beads, raise to 85%/90%. For high-precision implementation beads, lower to 55%/65%.

### 9.5 What CCv4.7's three hooks become

| CCv4.7 hook | Oro equivalent |
|---|---|
| `status.mjs` (statusLine) | Already done by `oro-dash`; dispatcher emits status events that dash consumes |
| `auto-handoff-stop.mjs` (Stop, 85%) | Dispatcher's checkpoint flow at 75% (lower because dispatcher pre-empts cleanly, doesn't wait for emergency Stop) |
| `pre-compact.mjs` (PreCompact) | Not applicable — oro doesn't use Claude Code's compaction; we own the worker lifecycle |

The fourth and fifth (`tldr-read.mjs`, `post-edit-diagnostics.mjs`) become non-hooks:
- `tldr-read` → `pkg/codestruct` injects nav-maps at prompt-build time, not via hook
- `post-edit-diagnostics` → ops review runs after worker turns; dispatcher invokes it natively

Net result: zero `.mjs` hooks, zero Node.js runtime dependency. Everything lives in the dispatcher's Go control loop.

### 9.6 Failure modes and recovery

| Failure | Detection | Recovery |
|---|---|---|
| Worker dies mid-turn | dispatcher process supervision | journey: `retried`, restart worker, increment retry_count |
| Worker exceeds checkpoint without notice | dispatcher reads context% in output | journey: `checkpointed (forced)`, persist what we have |
| Checkpoint flow itself fails | dispatcher exception | journey: `escalated (kind=checkpoint_failure)`, defer bead with reason |
| Bead exceeds max retries (default 5) | dispatcher counter | journey: `escalated`, bead deferred with `escalated_reason` for human review |
| Worker emits malformed context% | dispatcher parse error | warn, treat as 100% to force checkpoint conservatively |

### 9.7 Acceptance test

```
Cmd: oro test:context-safety --bead-id <test-bead> --simulate-context-pct 80
Assert: dispatcher emits journey: checkpointed within 2 turns;
        worker_state.last_context_pct = 80;
        worker subprocess restarted with same worktree;
        bead.next_action populated.
```

---

## 10. Two-Agent Split: Worker vs Oracle

### 10.1 Roles

- **Worker** — implementation. One bead → one worktree → TDD → commit → ops review → merge. Tools: `Read`, `Write`, `Edit`, `Bash` (scoped), `Grep`, `Glob`, `pkg/edit:*`, `pkg/codestruct:*`. No web access. No sandbox.
- **Oracle** — research. Open-ended exploration. Tools: `Read` (scoped), `Grep`, `Glob`, `oro sandbox`, web_search, doc_search via sandbox external functions. No `Write`, `Edit`, `Bash` (other than read-only). No worktree mutation.

### 10.2 Bead routing

The v20 replatform spec's bead type enum is `task | bug | epic | research | chore`. This spec extends that enum **additively** with two new types: `premortem` and `review`. Implementation work continues to use `task` (no separate `implementation` type — that was an error in v1 of this spec). Bug-fix work uses `bug`.

The extended enum (v2):

```
task       → worker (implementation work; was "implementation" in v1)
bug        → worker (bug fix; was "fix" in v1)
chore      → worker (maintenance / refactor)
epic       → decomposition flow (existing, no agent runs directly)
research   → oracle
premortem  → premortem agent (NEW; additive to v20 enum)
review     → ops review surface (NEW; additive to v20 enum)
```

Routing source: `pkg/dispatcher/router.go` (new) reads `bead.type` and selects prompt assembler + worker shim.

The v20 schema migration adds a `CHECK (type IN ('task','bug','chore','epic','research','premortem','review'))` constraint update as part of Phase A.1. The migration script must drop and re-create the existing CHECK to add `premortem` and `review`. Existing rows are unaffected.

### 10.3 Prompt assembly differences

The current code lives at `pkg/worker/prompt.go:13` (`PromptParams` struct, ~14 fields) and `pkg/worker/prompt.go:50` (`AssemblePrompt(params PromptParams) string`). It is parameter-object based, returns a string, and takes no `context.Context`. Existing call sites that the migration must update: `pkg/worker/worker.go:464`, `cmd/oro/cmd_work.go:477`, plus tests under `pkg/worker/`.

The reshape is a **two-phase migration**:

**Phase B.3a (compatibility layer):** Add `WorkerPromptParams` as a new struct that embeds the existing `PromptParams` plus the new fields. Add `AssembleWorkerPrompt(WorkerPromptParams) string` as a parallel function. The old `PromptParams`/`AssemblePrompt` remain and continue to work — call sites are unchanged. Internally, the old `AssemblePrompt` becomes a thin wrapper that adapts `PromptParams` into `WorkerPromptParams` with the new fields zero-valued, then calls `AssembleWorkerPrompt`. The output is byte-identical to today's prompt for any caller that doesn't pass the new fields.

**Phase B.3b (call-site migration):** Update each call site to use `WorkerPromptParams` and `AssembleWorkerPrompt` directly. Run golden-prompt tests at each step. After all call sites migrate, the old aliases (`PromptParams = WorkerPromptParams`, `AssemblePrompt = AssembleWorkerPrompt`) are kept for one release as deprecation aliases, then removed.

This keeps Phase B.3 truly additive (no breaking change), while still arriving at the new shape:

```go
// pkg/worker/prompt.go (renamed: PromptParams → WorkerPromptParams; signature unchanged shape)
type WorkerPromptParams struct {
    // existing fields preserved (BeadID, Title, Description, AcceptanceCriteria,
    // WorktreePath, Model, Attempt, Feedback, ProjectRoot, TargetBranch,
    // GitLog, WorkerProgram, CodeSearchContext)

    // REMOVED in v2 (after migration window):
    //   MemoryContext         (replaced by Cards)

    // ADDED in v2:
    Cards            CardsRender    // populated by pkg/cards via Relevant()
    CodeStruct       CodeStructRender // populated by pkg/codestruct, replaces ad-hoc CodeSearchContext for nav-maps
    JourneyTail      []JourneyEvent // last-N events of the bead, from Store.LatestJourney
    NextAction       string         // bead.next_action
    Blockers         []Blocker      // bead.blockers
    LinkedArtifacts  LinkedArtifacts // bead.linked_artifacts
}

// Worker prompt assembler — same shape as today, no ctx (consistent with current code)
func AssembleWorkerPrompt(p WorkerPromptParams) string { ... }

// New, for research beads
type OraclePromptParams struct {
    BeadID, Title, Description, AcceptanceCriteria string
    Cards            CardsRender    // filtered to taste/decision/fact for oracles
    SandboxSession   string         // bead.sandbox_session
    AllowedFns       []string       // bead.allowed_external_fns
    JourneyTail      []JourneyEvent
    Model            string
    Attempt          int
}
func AssembleOraclePrompt(p OraclePromptParams) string { ... }

// New, for premortem beads
type PremortemPromptParams struct {
    BeadID, TargetBeadID, TargetTitle, TargetDescription string
    Cards            CardsRender    // filtered to type=fact, type=pattern, retired-with-reason cards
    Methodology      string         // doctrine doc excerpt
    Model            string
}
func AssemblePremortemPrompt(p PremortemPromptParams) string { ... }

// Routing happens in pkg/dispatcher, not in pkg/worker; the dispatcher selects
// which Assemble*Prompt to call based on bead.type and constructs the params object
// from beadstore + cards + codestruct.
```

The dispatcher's bead-type → assembler routing lives in `pkg/dispatcher/router.go` (new file):

```go
func BuildPrompt(ctx context.Context, store beadstore.Store, cards cards.Store, b protocol.Bead) (string, error) {
    switch b.Type {
    case "research":
        return AssembleOraclePrompt(buildOracleParams(ctx, store, cards, b)), nil
    case "premortem":
        return AssemblePremortemPrompt(buildPremortemParams(ctx, store, cards, b)), nil
    case "task", "bug", "chore":
        return AssembleWorkerPrompt(buildWorkerParams(ctx, store, cards, b)), nil
    case "epic", "review":
        return "", fmt.Errorf("type %q is not directly executable; routed via decomposition or ops review", b.Type)
    default:
        return "", fmt.Errorf("unknown bead type %q", b.Type)
    }
}
```

Worker prompt: 12 sections (with §3.5 updates).
Oracle prompt: 8 sections (Role, Bead, Cards filtered to taste/decision/fact, Sandbox Tools, Web Tools, Output Format, Examples, Stopping Criteria).
Premortem prompt: 6 sections (Role, Target Bead/Spec, Cards filtered to past failures, Pre-mortem Methodology, Output Format, Stopping Criteria).

The migration from `PromptParams` to `WorkerPromptParams` happens in Phase D.3 (worker prompt Cards section) and Phase B.3 (worker prompt Code Structure section), with golden-prompt tests at each phase to prove the rendered prompt matches the spec.

### 10.4 Bead chaining

A research bead can produce children:

- **Recommendation cards** — promoted via the standard learnings_pending flow
- **Implementation beads** — created via `Store.Create` with `parent_id = <research bead id>`, `type = "task"` (or `"bug"` for fix-shaped work), complete with description and acceptance criteria. The created bead carries `tags ∋ "awaits_parent_close"` to signal the blocking relation explicitly.

#### Blocking semantics

v20's parent_id is metadata, not a blocker (see v20 §6.3). To make the research → implementation chain a hard block, this spec extends `beads_ready` and the dispatcher router with two rules:

1. **`beads_ready` view amendment:** v20's `beads_ready` (replatform spec §6.3) excludes beads with unmet `bead_deps` blockers. This spec extends the view in-place with one additional `NOT EXISTS` clause that excludes beads carrying the `awaits_parent_close` tag whose parent is still open. v20 stores tags in the `bead_tags` table (not as a JSON column on `beads`), so the lookup uses a join, not `json_each`. The amended view (Phase A migration; the v20 view is dropped and recreated atomically):

   ```sql
   DROP VIEW IF EXISTS beads_ready;
   CREATE VIEW beads_ready AS
   SELECT b.*
     FROM beads b
    WHERE b.deleted = 0
      AND b.status = 'open'
      AND (b.deferred_until IS NULL OR datetime(b.deferred_until) <= datetime('now'))
      -- v20 dependency-blocking semantics preserved verbatim:
      AND NOT EXISTS (
          SELECT 1 FROM bead_deps d
          JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
           WHERE d.bead_id = b.id
             AND d.type IN ('blocks','conditional-blocks')
             AND parent.status != 'closed'
      )
      -- v2 addition: research-spawned children carry awaits_parent_close
      -- and stay blocked until their parent closes.
      -- A child with awaits_parent_close is also blocked if its parent is
      -- missing OR soft-deleted, so a deleted-but-not-closed research bead
      -- cannot leak its children into the ready set (R3.4).
      AND NOT EXISTS (
          SELECT 1 FROM bead_tags t
           WHERE t.bead_id = b.id
             AND t.tag = 'awaits_parent_close'
             AND (
                  b.parent_id IS NULL                                       -- orphaned: blocked
               OR NOT EXISTS (SELECT 1 FROM beads p
                               WHERE p.id = b.parent_id
                                 AND p.deleted = 0
                                 AND p.status = 'closed')                   -- parent must be alive AND closed
             )
      );
   ```

   The `NOT EXISTS` for the parent's closed-and-alive state means: a child with `awaits_parent_close` is unblocked **only** when the parent exists, is not soft-deleted, and is closed. Any other state (missing, deleted, open, in_progress) keeps the child blocked. The parent-close sweeper (next paragraph) is responsible for removing the tag in the closed-and-alive case so the child appears in `beads_ready`.

   The matching `beads_blocked` view is amended in concert with the same parent-existence/alive/closed predicate (R4.8). Full SQL:

   ```sql
   DROP VIEW IF EXISTS beads_blocked;
   CREATE VIEW beads_blocked AS
   SELECT b.*
     FROM beads b
    WHERE b.deleted = 0
      AND b.status = 'open'
      AND (
           -- v20 dependency-blocking
           EXISTS (
               SELECT 1 FROM bead_deps d
               JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
                WHERE d.bead_id = b.id
                  AND d.type IN ('blocks','conditional-blocks')
                  AND parent.status != 'closed'
           )
           -- v2 addition: awaits_parent_close, with the same missing/deleted/open
           -- semantics as the ready view's exclusion clause.
        OR EXISTS (
               SELECT 1 FROM bead_tags t
                WHERE t.bead_id = b.id
                  AND t.tag = 'awaits_parent_close'
                  AND (
                       b.parent_id IS NULL
                    OR NOT EXISTS (SELECT 1 FROM beads p
                                    WHERE p.id = b.parent_id
                                      AND p.deleted = 0
                                      AND p.status = 'closed')
                  )
           )
      );
   ```

   By construction `beads_ready` and `beads_blocked` partition the open-bead set: a bead in `beads_ready` is not in `beads_blocked` and vice versa. The amended views are unit-tested with a fixture that includes (for each blocking case): an alive open parent, an alive in_progress parent, an alive closed parent, a deleted parent, a missing parent_id. The ready/blocked classification must be consistent across all five.

2. **Dispatcher router check:** before claiming any bead, dispatcher reads `beads_ready` (which already excludes blocked children). When a research parent closes, its children's `awaits_parent_close` tag is stripped by a sweeper (`pkg/dispatcher/sweep.go:PromoteChildrenOnParentClose`), and they become eligible.

`pkg/dispatcher/sweep.go` does not exist today; it is a **Phase F** deliverable (moved from Phase G in v3 because §18.4's verify-research-chain depends on it — Phase F's own acceptance cannot pass without it).

#### Wiring contract — no Store callback, dispatcher orchestrates

The v20 `Store.Close(ctx, id, reason)` interface has no callback or hook surface, and adding one would force `pkg/beadstore` to import `pkg/dispatcher`, creating a cycle. Instead the dispatcher orchestrates the close+sweep sequence at the call-site:

```go
// pkg/dispatcher/router.go — close-with-sweep helper, the only path that
// closes a bead in normal operation:
func (d *Dispatcher) CloseBead(ctx context.Context, beadID, reason string) error {
    // 1. Authoritative close in beadstore.
    if err := d.store.Close(ctx, beadID, reason); err != nil {
        return fmt.Errorf("Store.Close(%s): %w", beadID, err)
    }
    // 2. Run sweeper synchronously, with its own transaction. If sweeper fails,
    //    the bead is closed but children remain tagged; a periodic reap sweep
    //    (ReapDeletedParentChildren) will retry on the next tick.
    if err := sweep.PromoteChildrenOnParentClose(ctx, d.store, beadID); err != nil {
        d.log.Warn("sweeper failed", "bead_id", beadID, "err", err)
        // non-fatal — the close already happened; sweeper retries periodically
    }
    return nil
}
```

Direct `Store.Close` is reserved for the migration tool and tests; production code calls `CloseBead` exclusively. A lint check (`pkg/lint/closecheck`, Phase F.5 deliverable) flags any non-test, non-migration call site that uses `Store.Close` directly.

**Existing call-site migration** (also Phase F.5, gating for §18.4 acceptance): the v20 spec leaves three direct `d.beads.Close` callsites in `pkg/dispatcher/dispatcher.go`:

| File:line | Migration |
|---|---|
| `pkg/dispatcher/dispatcher.go:1652` | replace with `d.CloseBead(ctx, beadID, reason)` |
| `pkg/dispatcher/dispatcher.go:1990` | replace with `d.CloseBead(ctx, beadID, reason)` |
| `pkg/dispatcher/dispatcher.go:3107` | replace with `d.CloseBead(ctx, beadID, reason)` |

After Phase F.5, `pkg/lint/closecheck` runs in CI and rejects any new direct call. Tests under `pkg/dispatcher/*_test.go` and `cmd/oro/migrate*.go` are exempt via a build tag.

Spec inventory of `pkg/dispatcher/sweep.go`:

```
pkg/dispatcher/sweep.go (NEW, Phase F):
  - PromoteChildrenOnParentClose(ctx, store, parentID) — called by Dispatcher.CloseBead
    Gate: only acts on parents whose type is 'research' (other types' children
          do not carry awaits_parent_close in normal usage)
    Behavior: query children with `awaits_parent_close` + parent_id = parentID;
              for each, remove the tag via UpdateParams.Tags (v20 API);
              append journey event `parent_closed_promoted { parent_id, child_id }`
              on each child
    Transaction: one tx per child (concurrency safe; if N children, N tx).
    Idempotent: if invoked twice for the same parent, the second call finds
                no children with the tag and is a no-op.

  - PromoteClosedParentChildren(ctx, store) — periodic sweep, runs every 5 min (R5.1)
    Behavior: retry path for PromoteChildrenOnParentClose. Query children with
              `awaits_parent_close` whose parent is alive AND closed (the case
              that means the immediate post-close sweep failed mid-batch). For
              each, run the same per-child transaction as the immediate sweep;
              same `parent_closed_promoted` journey event. Idempotent across
              repeat sweeps. Convergence: every alive-closed parent's children
              get untagged within at most one sweep interval after the close.

  - ReapDeletedParentChildren(ctx, store) — periodic sweep, runs every 5 min
    Behavior: query children with `awaits_parent_close` whose parent is
              soft-deleted (per the §10.4 view amendment, those stay blocked);
              for each, escalate to human review via journey
              `escalated { kind: 'parent_deleted', parent_id, child_id }`
              and defer the child for human action via `Store.Defer`

  - OnReplanChildrenClosed(ctx, store, parentID, cycleNum) — called by
    Dispatcher.CloseBead when a child of a replan-parent closes. Also called
    immediately when a premortem returns verdict='replan' with zero spawned
    children (the zero-child case from R5.2).
    Replan child identity: every child spawned during a replan cycle is tagged
    with `replan_cycle:<N>` where N = parent.premortem_cycle_count + 1 (the
    upcoming cycle). The tag is set by the premortem agent at child-create
    time. Query: SELECT count(*) FROM beads WHERE parent_id=? AND deleted=0
    AND tags ∋ 'replan_cycle:<N>' AND status != 'closed'. Function returns
    when count=0.
    Behavior: transition parent's gate_state from 'replan' back to 'eligible'
              via SetGateState; increment premortem_cycle_count; emit
              gate_state_changed event. If premortem_cycle_count >= max_cycles,
              skip the transition and emit escalated event instead (per §11.4).

  - ExpireReviewQueueSLA(ctx, store) — periodic sweep, runs hourly (R5.3)
    Behavior: SQL UPDATE per §4.2 that auto-rejects review-queue learnings
              past the SLA window (default 60 days).

  - SweepDeletedBeadLearnings(ctx, store) — periodic sweep, runs every 5 min (R5.4)
    Behavior: query bead_learnings_pending rows whose bead is soft-deleted
              (beads.deleted=1) and whose row is still pending (promoted_to
              IS NULL AND rejected_at IS NULL); set
              rejected_at=now, reason='parent_bead_deleted'. Replaces the FK
              CASCADE assumption that doesn't apply to soft-deletes.
```

**Sweeper scheduling** (R5.3): the dispatcher process owns a sweep ticker (`pkg/dispatcher/sweeper_loop.go`, Phase F.5a deliverable). Tick intervals:
- 5 min: `PromoteClosedParentChildren`, `ReapDeletedParentChildren`, `SweepDeletedBeadLearnings`
- 60 min: `ExpireReviewQueueSLA`

The ticker is part of the dispatcher's main loop; sweepers run sequentially within a tick to keep concurrent SQLite writers low.

Phase F acceptance test (§18.4) drives `PromoteChildrenOnParentClose` and verifies the tag is stripped only when the parent is alive AND closed. The deleted-parent sweep is tested separately in §18.4 with a soft-delete fixture.

Note: the v20 spec already defines `beads_ready` and `beads_blocked` as runtime-computed views; this spec amends both via the Phase A schema migration. The `awaits_parent_close` tag is the explicit blocking marker; bare `parent_id` without that tag remains non-blocking metadata (preserves v20 semantics for non-research-spawned children). Tag mutations go through the v20 `Update`/`UpdateParams.Tags` API, which already manages `bead_tags` rows.

### 10.5 Oracle output discipline

Oracles produce structured output, not prose:

```
{
  "summary": "<one paragraph>",
  "findings": [
    {"claim": "...", "evidence": ["url1", "code:path"], "confidence": "high|med|low"}
  ],
  "recommendations": [
    {
      "kind": "card_candidate" | "implementation_bead",
      "card_candidate": {"type": "...", "title": "...", "body_summary": "...", ...},
      "implementation_bead": {"title": "...", "description": "...", "acceptance_criteria": "..."}
    }
  ]
}
```

This is parsed by the dispatcher and turned into card candidates / spawned beads. No prose hand-off.

### 10.6 Acceptance test

```
Cmd: oro bead create --type=research --title "What's the best Go tree-sitter grammar for embedded JSX?" \
       && oro work --auto
Assert: oracle agent runs, produces structured output,
        emits at least one card_candidate or implementation_bead recommendation,
        closes with verdict.
```

---

## 11. Pipeline Stages (CCv4.7-Derived)

### 11.1 The seven stages

Adopted from CCv4.7's `/autonomous` skill, mapped to oro:

```
ASSESS → PLAN → PREMORTEM → PREPARE → EXECUTE → VALIDATE → EVOLVE
```

Each stage produces structured data into beads or cards. No prose-only stages.

### 11.2 ASSESS

**Owner:** dispatcher / human via `oro current`
**Input:** project state (beads, cards, git, codestruct)
**Output:** rendered current view, recommended next bead

`oro current` enumerates in-progress beads, recent journey, blocked beads, and recommends next action ("ready to work: bead-X — claim with `oro work bead-X`"). No state change.

### 11.3 PLAN

**Owner:** beadcraft skill / human / oracle
**Input:** epic spec or feature description
**Output:** bead graph with acceptance criteria, parent_id chains, types

`/beadcraft` decomposes spec → beads. Each bead has `acceptance_criteria` set (a verifiable command + expected assertion). Implementation beads can have `next_action` pre-populated.

### 11.4 PREMORTEM

**Owner:** premortem agent (or skill, for human invocation)
**Input:** an epic bead or a complex implementation bead
**Output:** premortem bead with findings; updates to original bead if findings require it

Required-gate triggers (any one fires the gate):

- `bead.type = "epic"` — fire at the moment the epic is created with at least one child
- `bead.tags ∋ "risk:high"` or `"risk:medium"` — fire at create time
- `CountChildren(bead.id) > 5` — fire **retroactively** when the 6th child is created

The third trigger is retroactive because beads are created before children exist. The flow:

1. **At parent create:** dispatcher records `bead.gate_state = 'eligible'` for any bead matching trigger 1 or 2.
2. **At each child create:** dispatcher checks `CountChildren(parent_id)`. When count crosses the threshold (default 5), and parent's `gate_state` is not yet `satisfied`, the dispatcher transitions the parent to `gate_state = 'eligible'`.
3. **Eligibility consumed:** when an `eligible` parent attempts to enter `EXECUTE`, the dispatcher refuses unless a closed premortem bead exists with `parent_id = bead.id`. The dispatcher emits `journey: blocker_hit { kind: 'premortem_required' }` and pauses the parent.
4. **Premortem agent runs:** spawns a `type='premortem'` bead with `parent_id = original.id`. When that premortem closes (with payload `{verdict: 'proceed' | 'block' | 'replan'}`), the parent's `gate_state` transitions per the verdict:
   - `proceed` → `gate_state='satisfied'`. Parent can enter EXECUTE.
   - `block` → `gate_state='blocked'`. Parent stays paused with `journey: blocker_hit { kind: 'premortem_blocked', verdict_bead_id: <premortem.id> }`. Human action required to unblock (e.g., redefining the bead's scope and explicitly calling `oro bead gate-reset <bead-id> --reason="..."` which resets `gate_state` to `none`; this is a scoped human escape hatch).
   - `replan` → `gate_state='replan'`. Parent stays paused. The premortem agent's findings include zero or more child research/decomposition beads (already created in step 4's normal flow). When all replan-driven children close, a sweeper (`pkg/dispatcher/sweep.go:OnReplanChildrenClosed`) transitions the parent's `gate_state` back to `eligible`, increments the parent's `premortem_cycle_count` column, and the premortem cycle re-runs (a fresh premortem bead is created; the old premortem bead remains in the store with `status='closed'` — "consumed" means its verdict has been acted on, not that it is deleted). This guarantees termination because each cycle either: (a) ends with `proceed`/`block`, (b) `replan`s with strictly more child beads addressing the prior findings, or (c) consumes the human-only `gate-reset` escape hatch.

   #### Cycle counting
   Cycle count is stored on the parent, not derived. Schema addition (Phase A.1, §4.6.b):
   ```sql
   ALTER TABLE beads ADD COLUMN premortem_cycle_count INTEGER NOT NULL DEFAULT 0;
   ```
   `OnReplanChildrenClosed` increments this counter as part of its transaction:
   ```sql
   UPDATE beads SET premortem_cycle_count = premortem_cycle_count + 1 WHERE id = ?;
   ```
   When `premortem_cycle_count >= max_cycles` (default 5, configurable via `oro.toml [dispatcher.premortem] max_cycles`), the sweeper does not transition the gate back to `eligible`. Instead it leaves `gate_state='replan'` and emits `journey: escalated { kind: 'premortem_loop', cycle_count: N, max_cycles: M }` for human review. The human operator can either reset via `oro bead gate-reset --reset-cycles` (sets cycle_count back to 0 and gate_state to none) or manually close the parent.

`gate_state` is a column on `beads`:

```sql
ALTER TABLE beads ADD COLUMN gate_state TEXT NOT NULL DEFAULT 'none'
  CHECK (gate_state IN ('none','eligible','satisfied','blocked','replan'));
```

Mutation API:

```go
// Closed enum
type GateState string
const (
    GateNone      GateState = "none"
    GateEligible  GateState = "eligible"
    GateSatisfied GateState = "satisfied"
    GateBlocked   GateState = "blocked"
    GateReplan    GateState = "replan"
)

// v20 UpdateParams gains:
type UpdateParams struct {
    // ... existing fields ...
    GateState *GateState  // nil = no change
}

// Convenience method (also defined on Store, §4.4):
func (s *SQLiteStore) SetGateState(ctx context.Context, beadID string, from, to GateState, reason string) error
//
// Atomic semantics: opens one transaction; UPDATE beads SET gate_state=? WHERE id=?
// AND gate_state=? (CAS on `from` — returns ErrStaleGate if zero rows); INSERT into
// bead_journey with event='gate_state_changed', payload=json_object('from', from,
// 'to', to, 'reason', reason); COMMIT. Caller must pass the observed `from` state
// from a prior read; concurrent gate transitions serialize via the CAS.
```

Phase A migration backfills `gate_state='none'` for all legacy beads. Test coverage in §18.6 (verify-premortem-gate) verifies all five values can be set and read.

Optional gate (offered, not enforced):

- Any bead the human flags via `bead.tags ∋ "premortem:requested"`

The premortem agent runs `/premortem` (the existing skill, made callable as a non-interactive bead type). Its findings become a premortem bead linked to the target. If the verdict is `replan`, it can spawn a research bead.

**Trigger consistency:** `CountChildren` is a real-time count, not a stored field. Beads created in batches by `/beadcraft` should be wrapped in a single transaction so the gate check at the end of the batch sees the final count, not intermediate values. `pkg/dispatcher/router.go` exposes a `CreateBeadGraph(ctx, parent, children)` helper that does this.

### 11.5 PREPARE

**Owner:** dispatcher (prompt assembly)
**Input:** bead about to enter EXECUTE
**Output:** assembled prompt with cards, codestruct nav-maps, journey context

Already covered in §3.5 + §10.3.

### 11.6 EXECUTE

**Owner:** worker (or oracle for research)
**Input:** assembled prompt, worktree, tools
**Output:** journey events, edits, commits, learnings_pending entries

The execute loop, with dispatcher mediation:
```
turn 0: worker reads bead, plans approach
turn 1..N: worker writes test, sees red, writes code, sees green
turn N+1: worker runs full quality gate
turn N+2: worker emits learnings_pending entries (card candidates)
turn N+3: dispatcher submits to ops review
```

Each turn produces journey events. Dispatcher checkpoints at threshold (§9).

### 11.7 VALIDATE

**Owner:** ops review
**Input:** worker output, journey, diff
**Output:** verdict (`pass` / `fail` / `needs-more`); journey event with verdict

Existing oro flow. New: ops review reads `bead.learnings_pending` and either confirms candidates for promotion or rejects them.

### 11.8 EVOLVE

**Owner:** dispatcher (auto-promotion path) + human (review path for taste/decision)
**Input:** closed bead with learnings_pending
**Output:** new cards (auto or after review), card_events linking to bead, score deltas

Auto-promotion rules in §5.7. Review queue surfaced via `oro cards review-queue`.

### 11.9 The pipeline is a state machine

```
ASSESS    → PLAN
PLAN      → PREMORTEM | PREPARE
PREMORTEM → PREPARE | PLAN  (if findings change plan)
PREPARE   → EXECUTE
EXECUTE   → VALIDATE | EXECUTE  (retry loop with checkpoint)
VALIDATE  → EVOLVE | EXECUTE  (if needs-more)
EVOLVE    → ASSESS
```

Bead state field tracks the current pipeline stage:

```sql
ALTER TABLE beads ADD COLUMN pipeline_stage TEXT
  CHECK (pipeline_stage IN ('assess','plan','premortem','prepare','execute','validate','evolve','none'));
```

Transitions are journey events (e.g., `pipeline_stage_changed` with payload `{from, to}`).

### 11.10 Acceptance test

```
Cmd: oro pipeline status <bead-id>
Assert: returns current stage from beadstore;
        for an in-progress bead, stage is one of {execute, validate};
        for a planned bead, stage is one of {prepare, premortem};
        emits valid state transitions per §11.9.
```

---

## 12. Enforcement-Hierarchy Doctrine

### 12.1 The meta-rule

```
lint rule  >  type system  >  formatter  >  pre-commit  >  CI  >  CLAUDE.md (last resort)
```

For every "the worker should X" rule, the question is: can we make it impossible (lint), enforced at compile (type), automatic (formatter), blocked at commit (pre-commit), blocked at merge (CI), or — last resort — written down (CLAUDE.md)?

Probabilistic instructions in CLAUDE.md are the weakest enforcement; deterministic checks at lint/type level are the strongest.

### 12.2 Audit existing rules

Every rule currently in `CLAUDE.md`, `assets/`, or skill prompts gets audited:

```
For each rule:
  Q1. Can it be a lint rule? → write a custom lint check
  Q2. Can it be encoded in types? → refactor types
  Q3. Can a formatter enforce it? → configure formatter
  Q4. Can a pre-commit hook check it? → write a hook
  Q5. Can CI enforce it? → CI check
  Q6. If none of the above, it stays in CLAUDE.md as a tip — but mark it
       "BEST EFFORT" so workers know it's probabilistic.
```

Output of the audit: `assets/rules-audit.md` with each rule's enforcement level. Rules at level 6 (CLAUDE.md only) get prioritized for promotion.

### 12.3 Doctrine document

`assets/doctrine.md` is the canonical statement of the hierarchy, with examples per level:

```
LEVEL 1 — Lint: A custom rule fails CI/IDE if violated.
  Example: "no fmt.Errorf without %w when wrapping"
  Implementation: golangci-lint custom analyzer

LEVEL 2 — Types: Compiler/type-checker rejects the violation.
  Example: "context.Context is the first arg of all RPC handlers"
  Implementation: interface signature

LEVEL 3 — Formatter: Always rewritten on format.
  Example: "imports are grouped: stdlib, third-party, internal"
  Implementation: goimports / black / prettier config

LEVEL 4 — Pre-commit: Blocked at commit time.
  Example: "no committed binaries"
  Implementation: pre-commit hook checking for binary blobs

LEVEL 5 — CI: Blocked at merge time.
  Example: "all tests pass on main"
  Implementation: CI gate

LEVEL 6 — CLAUDE.md (BEST EFFORT): Probabilistic instruction.
  Use when no deterministic enforcement is feasible.
  Example: "When ambiguous, prefer simpler abstractions."
```

Workers see the doctrine in their prompt's "Coding Rules" section. The audit outcomes flow into per-language rule sets (Go rules, Python rules, TS/JS rules) that workers consume.

### 12.4 Acceptance test

```
Cmd: oro doctrine audit
Assert: produces a table of every rule, its current enforcement level,
        and its best feasible level;
        flags rules at level 6 that have known level 1-5 implementations
        in other oro projects ("low-hanging fruit").
```

---

## 13. Worker Prompt Redesign

### 13.1 Section list (after all phases land)

```
1.  Role                     (static)
2.  Bead                     (beadstore)
3.  Cards                    (cards/Relevant + progressive disclosure)
4.  Code Structure           (codestruct nav-maps)
5.  Relevant Code            (codestruct deep level / file excerpts)
6.  Git History              (git log scoped to bead)
7.  Coding Rules             (doctrine + per-language rules)
8.  Worker Program           (static; how to do TDD-Edit-QG-OpsReview)
9.  TDD                      (static)
10. Quality Gate             (static; gates derived from project config)
11. Worktree                 (dispatcher)
12. Context Handoff          (bead.journey + bead.next_action)
```

### 13.2 What disappeared

| Old section | Replacement |
|---|---|
| Previous Feedback | Cards (subsumed; all feedback becomes cards) |
| Memory | Cards |
| (No section for code structure) | Code Structure (NEW) |

### 13.3 Token budget

Per-section token budget (initial defaults, configurable):

```
Role:               300 tokens
Bead:               500
Cards (deck):       1,000
Cards (inlined):    2,000
Code Structure:     2,000
Relevant Code:      3,000
Git History:        500
Coding Rules:       1,500
Worker Program:     500
TDD:                300
Quality Gate:       400
Worktree:           300
Context Handoff:    1,500
─────────────────────
Total budget:       13,800 tokens
```

vs. today's worker prompt (estimate ~18-22K tokens for an active bead). The savings come from:

- Code Structure replacing raw file content (5-8K savings)
- Cards (deck-first) replacing full memory dumps (2-3K savings)
- Context Handoff structured (1-2K savings vs prose handoff inclusion)

### 13.4 Acceptance test

```
Cmd: oro prompt build --bead-id <test-bead> --print-sections
Assert: 12 sections present, all named, token usage per section ≤ budget,
        no prose-handoff content from current.md (file deleted),
        Cards section uses progressive disclosure format.
```

---

## 14. Renders That Replace Documents

### 14.1 `oro current`

Renders the live current state. Markdown for humans; `--format json` for tooling.

```
$ oro current
=================================
ORO CURRENT — 2026-04-28T14:23:00Z
=================================

In-Progress (3):
  oro-abc1  [task]     Replace Dolt with SQLite (replatform Phase 5)
            Last action: ran qg, 2 failures in pkg/dispatcher
            Next: fix dispatcher_test.go:TestRetry, re-run qg
            Worker: stopped at 72% context; checkpoint scheduled
            Cards in flight: card-x9y8 (rule), card-z7w6 (pattern)

  oro-def2  [research] Evaluate ouros embedding latency
            Last action: ran web_search, fetched 5 sources
            Next: synthesize into recommendation card
            Worker: oracle, 45% context

  oro-ghi3  [task]     Add `oro impact` subcommand
            ...

Ready (5):  oro-jkl4, oro-mno5, ...
Blocked (1): oro-pqr6 (blocked by oro-abc1)
```

### 14.2 `oro handoff`

Same shape as `oro current`, scoped to a session window.

```
$ oro handoff --since "1 hour ago"
=================================
ORO HANDOFF — 2026-04-28T13:23 → 14:23
=================================
[same shape, only events / changes within the window]
```

Used at session end. The output can be piped into a human-readable file if desired (`oro handoff > /tmp/handoff.txt`), but oro itself does not maintain `docs/handoffs/`.

### 14.3 `oro resume`

Routes you (or an agent) into a specific bead.

```
$ oro resume oro-abc1
[loads bead-abc1's full context]
[prints to stdout: bead, last 50 journey events, top relevant cards]
[exits 0; the agent invoking this calls back into a worker spawn]
```

For an interactive session, `oro resume oro-abc1 --interactive` opens a REPL with bead context loaded.

### 14.4 What disappears

```
- current.md
- docs/handoffs/*.md
- create-handoff.md skill (becomes invocation of `oro handoff`)
- resume-handoff.md skill (becomes invocation of `oro resume`)
- "Update current.md before starting" rule
- "Hand off with explicit context" rule
- session_protocol Start: "Check for pending work. Review any handoff notes."
  → replaced by Start: "Run `oro current`"
- session_protocol End: "Create handoff document"
  → replaced by End: "(noop; everything is already in beads + cards)"
```

### 14.5 Acceptance test

```
Cmd: oro current --format json | jq '.in_progress | length'
Assert: returns int >= 0 matching count of beads with status='in_progress'

Cmd: oro current --format json | jq '.in_progress[0] | keys' | sort
Assert: keys include bead, last_journey, next_action, relevant_cards

Cmd: oro resume <bead-id>
Assert: prints bead.title, bead.next_action, last 50 journey events,
        and top 5 relevant cards within 200ms (cache warm)
```

---

## 15. Migration Path

### 15.1 Phase ordering

```
Phase A (depends on: replatform v20 landing)
  A.1 Bead schema v2 — additive ALTERs, new tables, migration backfill
  A.2 Store interface additions — AppendJourney, etc.
  A.3 Bench gate — journey hot-path meets §4.5

Phase B
  B.1 pkg/codestruct — Go support, symbol map + call graph
  B.2 oro impact subcommand
  B.3 Worker prompt Code Structure section
  B.4 Bench gate — meets §6.10
  B.5 Add Python, TypeScript, JavaScript

Phase C
  C.1 pkg/edit — Go support, deterministic anchor splice
  C.2 12-tool worker surface
  C.3 Test corpus 100% pass
  C.4 Add Python, TypeScript, JavaScript

Phase D
  D.1 pkg/cards — schema, retrieval, scoring, decay
  D.2 Migration from pkg/memory
  D.3 Worker prompt Cards section
  D.4 Auto-promotion of learnings_pending → cards

Phase E
  E.1 Dispatcher context-safety control loop
  E.2 Per-shim ContextPct() impl (Claude, Codex, Gemini)
  E.3 Checkpoint flow + state preservation
  E.4 Acceptance: simulated context-pct test passes

Phase F
  F.0 Pre-Phase-F compatibility spike (§8.10) — GATING; max 2-week timebox
       Deliverables: pinned ouros version, API contract verified, distribution path chosen,
       external-function bridge prototype. Failure → descope F to constrained-surface fallback.
  F.1 Ouros vendoring/install (per F.0 outcome)
  F.2 oro sandbox subcommand
  F.3 External function bridge (web_search, etc.)
  F.4 Oracle prompt template
  F.5 Bead routing by type + Dispatcher.CloseBead helper
  F.5a pkg/dispatcher/sweep.go (PromoteChildrenOnParentClose, ReapDeletedParentChildren,
       OnReplanChildrenClosed) — required before F.6 acceptance
  F.6 Acceptance: oracle agent produces structured output (§18.4 verify-research-chain)

Phase G
  G.1 Pipeline stage state machine
  G.2 Premortem agent
  G.3 PREPARE / EVOLVE wiring
  G.4 oro current / handoff / resume

Phase H
  H.1 Doctrine audit; assets/doctrine.md
  H.2 Convert level-6 rules to level 1-5 where feasible
  H.3 Worker prompt Coding Rules section consumes doctrine

Phase I (cleanup)
  I.1 Delete current.md
  I.2 Delete docs/handoffs/ as stored artifacts
  I.3 Remove pkg/memory (replaced by pkg/cards)
  I.4 Remove create-handoff / resume-handoff stored docs (skills become render invocations)
  I.5 Update CLAUDE.md / ORO_AGENT.md to reference renders, not docs
```

### 15.2 Dependencies between phases

```
A → all others (bead schema is foundational)
B → C (pkg/edit shares tree-sitter pipeline with pkg/codestruct)
B → D worker prompt (codestruct nav-maps complement cards in prompt)
D → G EVOLVE stage (cards must exist for promotion to land)
E → I (context safety must work before deleting handoff fallback)
F → G PREMORTEM stage (oracle agents enable richer premortem capability)
H → all (doctrine clarifies rule expectations)
```

### 15.3 What ships when (rollout)

- After Phase A: nothing user-visible; foundation for all subsequent phases.
- After Phase B: workers see Code Structure section; ops review uses `oro impact`.
- After Phase C: workers use `pkg/edit` tools; native Edit available as fall-through.
- After Phase D: workers see Cards (replacing Memory + Previous Feedback).
- After Phase E: workers never degrade past threshold.
- After Phase F: research beads route to oracles; sandboxed exploration available.
- After Phase G: full pipeline; premortem gate; renders replace handoff docs.
- After Phase H: doctrine published; many rules promoted to deterministic enforcement.
- After Phase I: documentation cleanup; current.md deleted; harness clean.

### 15.4 Rollback strategy per phase

Every phase has a clean rollback:

- A: roll back schema migration, restore pre-v2 bead store from snapshot
- B: feature-flag Code Structure section off; workers see today's prompt
- C: feature-flag pkg/edit tools off; workers fall through to native Edit
- D: pkg/memory shim re-exposed; cards retrieval flag-off
- E: dispatcher checkpoint flow flag-off; revert to manual kill
- F: oracle routing flag-off; research beads fail fast
- G: pipeline stage tracking flag-off; renders read raw bead state
- H: doctrine audit results stay published; rule promotions reverted file-by-file
- I: current.md and handoffs restored from git history; not destructive

### 15.5 Phasing for the build calendar

The user has stated "unlimited engineering capacity" — so phases happen in dependency order, not in serialized weeks. Per phase:

- A: 1 engineer, 2 weeks
- B: 2 engineers (Go specialist + TS/Python specialist), 4-5 weeks
- C: 2 engineers, 4-5 weeks (overlaps with B)
- D: 1 engineer, 2 weeks
- E: 1 engineer (dispatcher specialist), 2 weeks
- F: 1 engineer, 2 weeks (mostly integration with vendored ouros)
- G: 2 engineers, 3 weeks
- H: 1 engineer, 1 week (audit) + ad hoc rule conversion
- I: 1 engineer, 1 week

Total: 23-28 engineer-weeks. With parallelism, calendar time is 8-10 weeks for a 4-engineer team.

---

## 16. Architectural Diagrams

### 16.1 The store layer

```
┌─────────────────────────────────────────────────────────────┐
│                   $ORO_HOME / .oro / state.db                │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  beads (v2)                  bead_journey                    │
│  ┌─────────────┐            ┌─────────────┐                 │
│  │ id          │←───────────│ bead_id     │                 │
│  │ title       │            │ ts          │                 │
│  │ status      │            │ actor       │                 │
│  │ next_action │            │ event       │                 │
│  │ blockers    │            │ payload     │                 │
│  │ ...         │            └─────────────┘                 │
│  └─────────────┘                                             │
│         │                                                    │
│         │            bead_learnings_pending                  │
│         │            ┌─────────────┐                         │
│         └───────────→│ bead_id     │                         │
│                      │ candidate   │  ──→ promoted_to        │
│                      │ promoted_to │                         │
│                      └─────────────┘                         │
│                                                              │
│  cards                       card_events                     │
│  ┌─────────────┐            ┌─────────────┐                 │
│  │ id          │←───────────│ card_id     │                 │
│  │ type        │            │ ts          │                 │
│  │ score       │            │ kind        │                 │
│  │ decay_anchor│            │ bead_id     │  ──→ beads      │
│  │ ...         │            └─────────────┘                 │
│  └─────────────┘                                             │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

### 16.2 Dispatcher control loop

```
                ┌────────────────────────────────────┐
                │           Dispatcher                │
                │                                     │
                │   ┌──────────────┐                  │
                │   │ Bead Queue   │                  │
                │   │ (from store) │                  │
                │   └──────┬───────┘                  │
                │          │                          │
                │   ┌──────▼───────┐                  │
                │   │ Route by     │                  │
                │   │ bead.type    │                  │
                │   └──────┬───────┘                  │
                │          │                          │
                │     ┌────┴────┐                     │
                │     │         │                     │
                │   Worker   Oracle                   │
                │   Prompt   Prompt                   │
                │     │         │                     │
                │     └────┬────┘                     │
                │          │ spawn                    │
                │          ▼                          │
                │   ┌─────────────────┐  per turn:    │
                │   │ Worker process  │ ◀─ stdin/out  │
                │   │ (Claude/Codex)  │ ─▶ context%   │
                │   └────────┬────────┘   journey     │
                │            │                        │
                │   ┌────────▼────────┐               │
                │   │ Append Journey  │               │
                │   └────────┬────────┘               │
                │            │                        │
                │   ┌────────▼────────┐               │
                │   │ Threshold       │ ◀─ §9.3       │
                │   │ checkpoint?     │               │
                │   └────────┬────────┘               │
                │     yes ◀──┴──▶ no                  │
                │       │            │                │
                │       ▼            ▼                │
                │   Persist     Continue              │
                │   Restart                           │
                └────────────────────────────────────┘
```

### 16.3 Pipeline state machine

```
        ┌─────────┐
        │ ASSESS  │◀────────────────┐
        └────┬────┘                 │
             │                      │
             ▼                      │
        ┌─────────┐                 │
        │  PLAN   │                 │
        └────┬────┘                 │
             │                      │
       ┌─────┴─────┐                │
       ▼           ▼                │
  ┌─────────┐ ┌─────────┐           │
  │PREMORTEM│ │ PREPARE │           │
  └────┬────┘ └────┬────┘           │
       │           │                │
       ▼           ▼                │
       └─────▶ ┌─────────┐          │
              │ EXECUTE │ ◀──┐      │
              └────┬────┘    │      │
                   │         │      │
                   ▼         │      │
              ┌─────────┐    │      │
              │ VALIDATE│────┘      │
              └────┬────┘           │
                   │                │
                   ▼                │
              ┌─────────┐           │
              │ EVOLVE  │───────────┘
              └─────────┘
```

---

## 17. Open Questions and Risks

### 17.1 Open architectural questions

| # | Question | Default proposed answer | Why we might revisit |
|---|---|---|---|
| Q1 | Auto-retire cards at score ≤ -1.0? | Yes, with `auto: persistent nack` reason | Could mask important contradictions; may want human confirm step |
| Q2 | How does codestruct handle generated code (.pb.go, .gen.ts)? | Tag as `generated`, exclude from impact unless explicit | May want to include for refactor safety |
| Q3 | Does `pkg/edit` support `import` rewrites on rename-all? | Yes, when callers are AST-resolvable | Cross-language imports (Go → embed of TS files) unresolvable |
| Q4 | Does the oracle have access to `oro impact`? | Yes (read-only); useful for research about codebase | May leak private code structure if oracle's web_search uses external API |
| Q5 | Premortem is required for which beads? | Epics + tagged risk:high/medium + > 5 children | Threshold may be too strict (false positives) or too loose |
| Q6 | Where do beads get `risk:` tags? | beadcraft sets via heuristic; human can override | Heuristic accuracy unknown; need eval |
| Q7 | When ouros is missing, do research beads degrade or fail? | Fail with clear message | Could degrade to a subset (web_search only) |
| Q8 | Card promotion confidence thresholds? | rule/pattern: 0.8; fact: 0.7; taste/decision: human always | Numbers are guesses; need calibration data |
| Q9 | Card deduplication (avoid promoting near-duplicates)? | Embedding-based similarity check at promotion time | Need an embedding model bundled or callable |
| Q10 | Renders cache or always-live? | Always-live; SQLite handles latency | At >100 in-progress beads, may need a cache layer |
| Q11 | Pipeline stage transitions enforced or advisory? | Advisory v1, enforced v2 | Need to confirm dispatcher state machine doesn't lock up |
| Q12 | Multi-project support (oro running across multiple projects)? | Out of scope for v1; one project per `$ORO_HOME` | May need to revisit when multiple repos use oro |

### 17.2 Risks (premortem-style)

| # | Risk | Severity | Mitigation |
|---|---|---|---|
| R1 | Journey table grows unbounded | High | Periodic compaction: keep last 1000 events per bead, archive rest to `bead_journey_archive` |
| R2 | Card relevance retrieval is wrong (workers see useless cards) | High | A/B compare card-only prompts against current memory-based prompts on a held-out bead set |
| R3 | Context-safety threshold too aggressive (frequent checkpoints) | Med | Tune thresholds per worker shim; expose per-bead override |
| R4 | pkg/edit produces invalid syntax in some edge case | High | Re-parse after every splice; rollback if invalid; log + escalate |
| R5 | Ouros vendoring breaks on a new platform | Med | CI runs across mac arm64, linux x86, linux arm; releases pinned per platform |
| R6 | Worker shims (Claude/Codex/Gemini) don't all emit context% | Med | Default to 100% if missing (forces conservative checkpoint); document required signal per shim |
| R7 | Migration from pkg/memory loses semantic detail | Low | One-shot migration with backfill of `created_at`; lineage_to_legacy = true marker |
| R8 | Card decay tuned wrong (rules go stale too fast) | Med | Per-type half-lives configurable; auditable via `oro cards stats` |
| R9 | Premortem gate blocks shipping on legitimate non-risky beads | Med | Human override flag; configurable thresholds |
| R10 | Doctrine audit finds zero rules to promote (all are inherently best-effort) | Low | Acceptable outcome; surfaces what's actually probabilistic |
| R11 | TS/JS edge cases break edit anchors (JSX with conditional fragments) | High | Test corpus per language; fall-through is the safety valve |
| R12 | Renders are slow on large bead sets | Med | Bench gate; cache layer if needed |

### 17.3 Things we explicitly do not solve

- Cross-project beads (one project per `$ORO_HOME`)
- Real-time multi-worker coordination (workers are bead-isolated)
- Distributed bead store (single-node SQLite)
- Embedded LLMs (we depend on external worker shims)
- IDE integration (oro is CLI + dispatcher; IDE is a future surface)
- Self-modifying agents (workers can edit oro itself, but only via the standard bead flow)

---

## 18. Acceptance Tests — Multi-Test E2E Suite

A single-command verifier is insufficient: a trivial test bead never exercises checkpointing, never spawns child beads, never tests learning promotion, and never stresses render consistency. This spec defines **eight** E2E tests, runnable individually or via `oro harness verify-all`. Each test is independent, idempotent, and uses the v20 CLI shape (`oro bead create ...`).

The tests live under `cmd/oro/harness/` and are invoked as `oro harness verify-<name>`.

### 18.1 verify-current — render correctness

```
Pre:  fresh state.db with two open beads (one task, one research) and journey events
Cmd:  oro harness verify-current
Steps:
  1. oro current --format json
  2. assert response includes both beads with correct status and last_journey
  3. assert snapshot timestamp present and within 1s of run time
  4. concurrently inject a journey event; re-run; verify the new event is present
     in next snapshot but never appears mid-render (snapshot consistency)
Pass: jq-extracted fields match expected; no half-state observed
```

### 18.2 verify-task — implementation bead end-to-end

```
Cmd:
  oro bead create --type=task --title="harness-test-task" \
      --description="touch test-evidence.txt" \
      --acceptance-criteria="Cmd: test -f test-evidence.txt | Assert: exit 0"
  oro work --auto $BEAD_ID
Asserts:
  - journey contains: claimed, started, edit, commit, qg_attempted, qg_passed,
                      ops_review_requested, ops_review_verdict, merged, closed
  - bead.status = 'closed'
  - bead.linked_artifacts.commits has at least one entry
  - bead.linked_artifacts.files_touched contains 'test-evidence.txt'
  - pipeline_stage = 'evolve' or 'none'
```

### 18.3 verify-checkpoint — context-safety control loop

```
Pre:  start a task bead with context-budget set artificially low
      (test_context_threshold = 0.20 in the bead's worker_state override)
Cmd:
  oro bead create --type=task --title="harness-test-checkpoint" \
    --description="<a description that pushes context above 20% in turn 1>" \
    --acceptance-criteria="Cmd: ... | Assert: ..."
  oro work --auto $BEAD_ID
Steps:
  1. dispatcher must trigger checkpoint at turn 1 or 2
  2. journey must contain: checkpoint_requested, checkpoint_acked OR forced flag,
     checkpointed, retried (auto, not error), then continued execution
  3. worker subprocess PID changes between turn 2 and turn 3
  4. bead.next_action populated after checkpoint
  5. bead.worker_state.last_checkpoint_ts non-null and recent
  6. eventually: closed, status='closed'
Pass: checkpoint flow completes without loss of journey/artifacts; bead reaches closed
```

### 18.4 verify-research-chain — oracle bead spawns implementation bead

```
Cmd:
  RESEARCH_ID=$(oro bead create --type=research --title="harness-test-research" \
    --description="Query: should we use approach A or B" \
    --acceptance-criteria="produce 1 recommendation_card or 1 implementation_bead")
  oro work --auto $RESEARCH_ID
Asserts:
  - journey of $RESEARCH_ID contains: sandbox_session_start (if ouros enabled)
                                       OR sandbox skipped event
  - bead_learnings_pending has ≥ 1 row OR a child bead exists
  - if child bead: child.parent_id = $RESEARCH_ID
                   child.tags ∋ 'awaits_parent_close'
                   child.status = 'open'
                   child does NOT appear in beads_ready (blocked)
  - close $RESEARCH_ID; child appears in beads_ready
  - child.tags after parent close: 'awaits_parent_close' tag stripped by sweeper
Pass: full chain plays out; blocking semantics correct
```

### 18.5 verify-learning-promotion — closed loop on cards

```
Pre:  task bead with description that produces a learnings_emitted event
Cmd:
  oro bead create --type=task --title="harness-test-learn" \
    --description="touch and commit a file; emit a learning candidate when done"
  oro work --auto $BEAD_ID
Asserts:
  - journey contains learning_emitted event
  - after ops_review_verdict=pass: journey contains learning_promoted event
  - oro cards list --since=$RUN_START shows the new card
  - card.emerged_from = $BEAD_ID
  - card.body_summary, body_full populated; card.promotion_confidence ≥ 0.5
    (sourced from bead_learnings_pending.candidate.confidence at promotion time)
Pass: full evolve loop closes
```

### 18.6 verify-premortem-gate — retroactive gate fires

```
Cmd:
  EPIC=$(oro bead create --type=epic --title="harness-test-epic" --description="..." \
    --acceptance-criteria="...")
  for i in 1..6; do
    oro bead create --type=task --title="child-$i" --parent=$EPIC \
      --description="..." --acceptance-criteria="..."
  done
Asserts:
  - after 6th child: parent's gate_state = 'eligible' (CountChildren > 5)
  - oro work --auto $EPIC: refused with blocker_hit kind=premortem_required
  - dispatcher auto-spawns a premortem bead with parent_id=$EPIC
  - close premortem with verdict=proceed: parent gate_state = 'satisfied'
  - oro work --auto $EPIC: now accepted
Pass: retroactive trigger works; gate flow completes
```

### 18.7 verify-edit-tools — pkg/edit operations against test corpus

```
Cmd:
  for lang in go python ts js; do
    for file in test/fixtures/edit-corpus/$lang/*; do
      oro edit:replace $file <symbol> --snippet="<expected snippet>"
      diff $file test/fixtures/edit-corpus/$lang/expected/$(basename $file)
    done
  done
Asserts:
  - 100% match on all fixtures (200 Go + 100 Python + 100 TS + 50 JS = 450 cases)
  - any EFALLTHROUGH return cleanly reports the reason
  - undo on every operation restores the file byte-for-byte
Pass: corpus passes; no silent misapplies
```

### 18.8 verify-codestruct — multi-language nav-map and impact

```
Cmd:
  oro impact pkg/dispatcher/dispatcher.go:Dispatcher.Run
  oro impact tests/fixtures/python/api.py:handle_request
  oro impact tests/fixtures/ts/server.ts:authMiddleware
Asserts:
  - all return at least one direct caller for symbols with known callers
  - cache hit rate >= 80% on second run
  - latency p99 < 2s for project ≤ 5k files
Pass: all three languages produce structurally-correct call graphs
```

### 18.9 The aggregator

```
oro harness verify-all
  → runs 18.1 .. 18.8 in dependency order
  → emits structured JSON report per test
  → exits 0 only if all pass
```

The aggregator is the canonical gate before releasing any oro version. A passing `verify-all` is the spec's acceptance test (replaces v1's single 10-check verifier).

---

## 19. Companion-Spec Linkages

### 19.1 With replatform v20

- Bead schema v2 (§4) is an additive amendment to v20's `pkg/beadstore`. v20's 12-method Store interface is preserved; new methods are added.
- v20's acceptance test must continue to pass after v2 schema migration.
- Phase A of this spec depends on Phase 11 of v20 (final cleanup) being complete; otherwise Phase 11's no-shim cutover and Phase A's schema migration both touch the bead store.
- Recommended ordering: complete v20 Phase 11; then begin this spec's Phase A.

### 19.2 With external-tooling v1 (superseded)

The 2026-04-27 external-tooling spec recommended adopting `tldr`, `bloks`, `ouros`, `fastedit`, CCv4.7 hooks as sidecars. This spec **supersedes** that recommendation:

- `tldr` → not adopted; `pkg/codestruct` built native
- `bloks` → not adopted; `pkg/cards` built native (bloks card abstraction is the design reference)
- `ouros` → still adopted, scoped to oracle agents only (was: research beads broadly)
- `fastedit` → not adopted; `pkg/edit` built native, deterministic-only
- CCv4.7 hooks → not adopted as `.mjs` scripts; equivalent control patterns live in dispatcher

The original spec's rationale for adoption (engineering effort) was reframed by user direction ("we have unlimited engineering capacity; build the best harness"). The architectural arguments for build-over-adopt now stand on:

- Single-binary install preserved (no Python / Rust / Node sidecars)
- AGPL exposure eliminated
- Tighter coupling to oro internals (beads, worktrees, dispatcher) than any subprocess can offer
- Per-language work distributed across own roadmap, not constrained by upstream priorities

### 19.3 With existing oro skills

| Skill | New behavior |
|---|---|
| `using-skills` | unchanged |
| `beadcraft` | runs PREMORTEM step on epic-shaped specs |
| `executing-beads` | runs through full pipeline; emits structured events |
| `work-bead` | dispatcher-mediated; checkpoint flow active |
| `create-handoff` | becomes invocation of `oro handoff` |
| `resume-handoff` | becomes invocation of `oro resume` |
| `documenting-solutions` | emits a card candidate (rule/pattern) instead of a markdown file |
| `premortem` | callable as a non-interactive bead (`bead.type=premortem`) |

---

## 20. Glossary

- **Bead**: a unit of work tracked in `pkg/beadstore`. Has type (one of `task | bug | chore | epic | research | premortem | review`), status, journey, next_action, etc.
- **Card**: a unit of durable knowledge in `pkg/cards`. Typed (rule/taste/pattern/decision/fact), scored, lineage-tracked.
- **Journey**: append-only log of events on a bead. Emitted by workers, oracles, dispatcher, ops review.
- **Render**: a read-only computed view (`oro current`, `oro handoff`, `oro resume`). Never written to disk as a stored artifact.
- **Worker**: implementation agent. Has Edit/Write/Read/Bash/edit/codestruct tools.
- **Oracle**: research agent. Has sandbox + web/doc search; no Edit/Write.
- **Pipeline**: 7-stage progression (ASSESS → PLAN → PREMORTEM → PREPARE → EXECUTE → VALIDATE → EVOLVE).
- **Doctrine**: enforcement-hierarchy meta-rule (lint > type > formatter > pre-commit > CI > CLAUDE.md).
- **Codestruct**: tree-sitter-backed AST + call-graph engine. Multi-language. On-demand.
- **Edit (`pkg/edit`)**: deterministic AST editor. Multi-language. Falls through to native `Edit` for ineligible cases.
- **Sandbox**: ouros surface; sandboxed Python with snapshot/fork/resume. Oracle-only.
- **Checkpoint**: dispatcher-initiated context save and worker respawn. Triggered at threshold.
- **Promotion**: turning a `learnings_pending` candidate into a durable card.
- **Retirement**: a card is no longer surfaced by default but persists in history.
- **Supersession**: retirement in favor of a replacement card.

---

## 21. Sign-off Checklist (for codex / human review)

- [ ] §1 executive summary maps to all sections
- [ ] §2 each problem has a section that solves it
- [ ] §3 memory model is internally consistent
- [ ] §4 bead schema v2 is additive on v20; no breaking changes
- [ ] §5 card schema covers all card types from CCv4.7 + bloks
- [ ] §6 codestruct supports Go, Python, TS, JS day one
- [ ] §7 edit is deterministic-only; no model dependency
- [ ] §8 ouros is scoped to oracles only
- [ ] §9 dispatcher control loop covers normal + failure modes
- [ ] §10 worker/oracle split is clean; tools differ; prompts differ
- [ ] §11 pipeline stages produce structured data at every stage
- [ ] §12 doctrine audit is actionable
- [ ] §13 worker prompt redesign within token budget
- [ ] §14 renders replace all stored documents
- [ ] §15 phases have clean rollbacks
- [ ] §17 open questions tracked; risks have mitigations
- [ ] §18 single-command verifier is sufficient and runnable
- [ ] §19 linkage to v20 replatform spec correct (Phase 11 → Phase A)
- [ ] No mention of "engineer weeks" as a constraint (user explicitly removed)

---

End of v1.
