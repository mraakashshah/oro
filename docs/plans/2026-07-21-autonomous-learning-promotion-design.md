# Autonomous Learning Promotion — Build the Grading Loop, Remove the Human

Date: 2026-07-21
Epic: (to be created) — factory-autonomy defect
Status: revised after adversarial-spec-review FAIL (v1 falsely assumed the grade
loop already ran)

## Summary

The factory is autonomous ("the operator is an observer … routine progress must
never require an operator action"), yet the card learning-promotion pipeline
routes every `decision`/`taste` learning to a `human_review_required` queue that
no human drains and a 60-day SLA sweep silently discards. The factory throws
away its own architectural decisions.

The fix is NOT a routing change onto an existing judge. Adversarial review
confirmed the grade machinery is **unwired dead code**: `gradeGate`,
`ensembleGradeGate`, `GradeOutcome`, `buildGradePrompt`, and `GradeEvidence`
have **zero production callers**; `ops.Spawner` has `Dream()` but no `Grade()`;
nothing reads `grade_state='proposed'` to resolve it. `GradeGateEnabled` is
false, and flipping it true today would REGRESS `rule`/`pattern` auto-promotion
and dream mutations into an undrained `proposed` limbo.

So we must **build the autonomous grading loop**, route all learnings through
it, and only then flip the enable flag.

## Problem (verified against code)

1. `pkg/cards/promotion.go:DecidePromotion` sends `CardTypeTaste,
   CardTypeDecision` to `human_review_required` before any judge runs.
2. The grade gate (`gradeGate`/grade worker in `pkg/ops/grade_prompt.go`) is
   dead code — no driver spawns it, no consumer applies its `GradeOutcome`, no
   reader resolves `proposed` cards.
3. `GradeGateEnabled` defaults false (`cmd/oro/cmd_start.go` sets only
   `DreamInterval:10`); flipping it true without a drain regresses working
   behavior.
4. The grader is the cheapest model: `.oro/config.yaml` `ops_dream` =
   `gpt-5.6-luna`/`low`, while `ops_review` = `claude-opus-4-8`/`xhigh`.
5. Other defer reasons (`near_duplicate`, `confidence_below_threshold` for
   rule/pattern, `fact_unconfirmed`) ALSO route to the human-drained review
   queue — not just decision/taste.

## Goals

- No `human_review_required` state and no undrained `proposed` limbo exist.
- Every emitted learning reaches a terminal `applied` or `rejected` state
  autonomously, verified by an on-`main` acceptance test.
- Subjective learnings are graded by a model strong enough to promote durable
  knowledge, via a cost-aware synchronous escalation, before being dropped.
- The grading loop runs by default with no operator action.

## Non-Goals

- Bumping sibling cheap roles (`memory_extractor`, `codesearch_reranker`).
- Building a new judge algorithm; `gradeGate`'s decision logic is reused — but
  its driver, spawn, and resolver are built.
- Changing `rule`/`pattern` behavior beyond the baseline grader-tier bump and
  giving below-threshold cases a terminal.

## Components to build (this is the wiring gap)

1. **`ops.Grade` spawn** (`pkg/ops/ops.go`) — spawns the grade worker role,
   feeding `buildGradePrompt`/`GradeEvidence`, parsing via
   `parseGradeWorkerOutput`. New sibling of `Dream()`.
2. **Grade role config** — a `grade` role (or reuse `ops_dream`) with a typed
   escalation ladder of (model, effort) rungs; see Decision.
3. **Store resolver** (`pkg/cards/store.go`) — `ResolveProposal(cardID,
   GradeOutcome)` transitions `grade_state` `proposed → applied|rejected` and
   records `grade_verdict`/`grade_confidence`. Consumer of `GradeOutcome`.
4. **Async grade drain** (`pkg/dispatcher/sweeper_loop.go`) — a sweeper tick
   that lists `proposed` cards and, per card, runs the escalation (below) to a
   terminal. Decoupled from `DreamInterval`; runs off the bead-close hot path.
5. **Escalation driver** — per proposed card, grade → apply/reject/escalate
   synchronously within one drain pass.

## Decision — synchronous grade-and-escalate ladder

`DecidePromotion` stops emitting `human_review_required`; `decision`/`taste`
(and below-threshold rule/pattern) are promoted **as proposals** and resolved by
the drain. Within one drain pass for a card, the driver escalates synchronously:

| Rung | Model / effort | `correct` (≥ rung threshold) | `incorrect` | ambiguous (`partial`/`unresolvable`/below threshold) |
|---|---|---|---|---|
| 1 — all types | **terra / low** | apply | reject | `rule`/`pattern` → reject; `decision`/`taste` → rung 2 |
| 2 — decision/taste | **sol / high** | apply | reject | → rung 3 |
| 3 — decision/taste | **sol / xhigh** | apply | reject | **reject** |

- Escalation is **synchronous within one drain pass** (grade, and if ambiguous
  re-spawn the grader at the next rung immediately). No persisted `grade_rung`
  column or migration is required, and no "re-grades at the same effort forever"
  bug is possible.
- Each rung is a **single grade** (`singleGradeGate`), each with its own
  `AutoApplyConfidence`; the ensemble path is not used, so `EnsembleMinConfidence`
  stays out of scope.
- After rung 3, ambiguous → **reject** (no human to escalate to).
- `gpt-5.6-sol` at `reasoning: xhigh` is a valid config (`checkReasoning`
  accepts `low|medium|high|xhigh` for codex with no per-model restriction —
  confirmed).

## Terminals for the other defer reasons (no path may dead-end at a human)

- `near_duplicate_<id>` → **reject** (the knowledge already exists as a card).
- `fact_unconfirmed` → **reject** (unconfirmed facts do not promote).
- `confidence_below_threshold` (rule/pattern) → promote as proposal and run the
  rung-1 grade; apply if `correct`, else reject. No human queue.
- `unknown_verdict` → **reject** (fail-closed). This is a LIVE path, not
  defensive: `CloseBead` (router.go:70) runs `runLearningPromotion` on every
  close with `verdict = promotionVerdictFromCloseReason(reason)`, which returns
  `""` for any reason that isn't merge/review-fail (duplicate, wontfix,
  obsolete, plain manual close) → `DecidePromotion` verdict-switch default →
  `unknown_verdict`. Must be terminal.
- `invalid_card_type` (type-switch default, defensive) → **reject**.
- DecidePromotion emits NO `deferPromotion(_, "human_review_required")` and no
  reachable `DeferToReviewQueue` for any reason. Only once EVERY defer reason
  above is terminal is `ExpireReviewQueueSLA` / `review_queue_sla_expired`
  retired; its tests are updated to assert the queue is unreachable rather than
  SLA-swept.

## Sequencing (ordering is load-bearing)

1. Build spawn + resolver + drain + escalation (feature-flag OFF).
2. Land the on-`main` acceptance test (below) proving autonomous terminal.
3. Route `DecidePromotion` reasons to proposals/terminals.
4. Backfill the 35.
5. **Only then** default `GradeGateEnabled=true` and set the grade-drain cadence
   in `cmd/oro/cmd_start.go` + `withDefaults`. Flipping earlier regresses
   working rule/pattern auto-promotion into undrained limbo.

## Epic acceptance test (machine-verifiable, on `main`)

```
Cmd: go test ./pkg/dispatcher/ -run TestSubjectiveLearningReachesTerminalAutonomously -count=1
Assert: (a) a decision/taste learning emitted at a merge close ends in
grade_state applied|rejected with zero human/CLI promote calls and no
bead_learnings_pending row left with queued_for_review_at set; AND
(b) a learning emitted at a NON-merge close (reason=duplicate/wontfix, so
verdict="" → unknown_verdict) also leaves no queued_for_review_at row —
proving the residual defer paths are terminal, not just the subjective happy path.
```

## Backfill

After the driver lands, re-enter the ~35 `human_review_required` candidates as
proposals at **rung 1**, exactly once, idempotently (the `promoted_to` guard on
`PromoteLearningAsProposal` prevents double-promotion). Backfill is inert
without the driver, so it is sequenced after it.

## Risks

- **Tiger — deck poisoning.** Auto-applying a wrong subjective decision injects
  false truth into every worker prompt. Mitigations: apply only on a confident
  `correct` at `sol/xhigh`; ambiguous always rejects (fail-closed); existing
  contradiction-suppression + decay + calibration bury wrong high-score cards.
- **Elephant — grading cost.** Up to 3 sequential grade-agent spawns per
  ambiguous subjective candidate. Bounded: only decision/taste that stay
  ambiguous climb; `correct`/`incorrect` at any rung terminate; async drain
  keeps it off the bead-close path.
- **Regression via premature enable** — mitigated by the sequencing above.

## Affected code

- `pkg/ops/ops.go` — add `Grade` spawn.
- `pkg/config/agent.go`, `.oro/config.yaml` — grade role + escalation ladder;
  baseline `luna/low → terra/low`.
- `pkg/cards/store.go` — `ResolveProposal`; reader for `proposed` cards.
- `pkg/cards/promotion.go` — `DecidePromotion` terminal mapping; per-rung
  thresholds.
- `pkg/dispatcher/promotion.go` — route reasons to proposals; escalation driver.
- `pkg/dispatcher/sweeper_loop.go` — grade-drain sweeper + cadence.
- `pkg/dispatcher/sweep.go` — retire `ExpireReviewQueueSLA`; update tests.
- `cmd/oro/cmd_start.go`, `pkg/dispatcher/dispatcher.go` — enable-flag +
  cadence defaults (LAST).
