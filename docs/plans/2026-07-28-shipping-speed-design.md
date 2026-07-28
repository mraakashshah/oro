# Design: Reduce Oro's Time-to-Ship

**Date:** 2026-07-28
**Status:** Draft — pending adversarial review
**Author:** architecture review follow-up
**Related:** `docs/audits/2026-07-28-architecture-review.md`

---

## Problem

Oro takes too long to ship. The audit established that this is *not* a diff-size problem — merged commits are appropriately small (p50 = 59 insertions, p90 = 318 across 327 recent commits).

The cost is structural, in three compounding layers:

1. **Tasks get shattered.** A hardcoded gate refuses any task whose acceptance criteria cite more than two directories, and the only available remedy is decomposition into more tasks. Fired **1,104 times** — the largest escalation category.
2. **Every fragment pays full ceremony.** Each child task independently pays a full `go test ./internal/... ./pkg/...` run (~2:20 wall clock) plus an opus-high review, whether it is a one-line clamp or a 500-line feature. With 5,840 assignments, the Go test lane alone accounts for roughly 227 hours of wall clock.
3. **Fragments ship unfinished layers.** Decomposition produces horizontal slices where early children ship exports nothing calls yet, legitimized by `//oro:testonly` with a comment promising the wiring "lands in a dependent task." 232 such annotations exist; **89 are explicit future-task promises**. `pkg/storage` is the terminal state of this pattern: 13 tables, 41 suppressions, zero rows, wired fail-closed into `oro start`.

### Evidence

| Signal | Value | Source |
|---|---|---|
| `OVERSIZED_BEAD` escalations | **1,104** | live `state.db` |
| `decompose` ops runs | 214 of 240 total | `ops_runs` |
| Assignments per executed task | **3.5×** (5,840 / 1,655) | `assignments` |
| Assignments in worst 1% of tasks | **35%** (2,068) | `assignments` |
| `//oro:testonly` repo-wide | **232**, 89 are future-task promises | `rg 'oro:testonly'` |
| Epic integration failures vs merges | **3,562 : 205** | `events` |
| Mean task estimate | 16 min (max 4,800 — garbage outlier) | `beads.estimated_minutes` |

Decomposition converts the parent to `--type=epic` (`decompose_prompt.go` step 6), which routes it into the epic-branch machinery — the subsystem failing 17:1. **Decomposition is the primary load source for Oro's least reliable subsystem.**

## Non-goals

- Fixing epic integration itself (tracked separately in the audit's P2).
- Building the delivery stage (deploy/release/rollback) — audit P3.
- Reducing review quality. Rejection rate is 37%; this design must not lower the bar, only the cost of clearing it.

---

## Root cause: the gate measures the wrong thing

`pkg/protocol/types.go:218`:

```go
func CountDistinctModules(acceptance string) int {
	seen := make(map[string]struct{})
	for _, line := range strings.Split(acceptance, "\n") {
		if !strings.HasPrefix(line, "Read:") { continue }
		...
		seen[filepath.Dir(stripMirrorPrefix(part))] = struct{}{}
	}
	return len(seen)
}
```

Consumed at `pkg/dispatcher/dispatcher.go:7011`:

```go
if modules := protocol.CountDistinctModules(acceptance); modules > 2 {
	if !isEpic && !hasChildren {
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscOversizedBead, bead.ID,
			fmt.Sprintf("touches %d modules — needs decomposition", modules), ""), bead.ID, workerID)
		d.recordAssignmentFailure(bead.ID)
		return title, "", false
	}
}
```

This counts **directories named in the acceptance criteria's `Read:` lines**. That measures how thoroughly the AC author cited their research, not how large the change is.

Consequences:

- A well-researched AC listing three files is rejected as oversized.
- An AC with **no `Read:` line counts zero modules and always passes**.
- The system therefore trains its own task-writers toward thin acceptance criteria — plausibly contributing to the 833 combined `MISSING_AC` (254) and `NON_TDD_AC` (579) escalations.
- The heuristic has been patched twice for over-counting: symbol suffixes (`types.go:240-245`) and mirror prefixes (`types.go:251-275`). Both patches exist to suppress false positives it structurally cannot avoid.

**Raising the threshold would relocate this incentive, not remove it.** The gate must measure something honest or not exist.

---

## Design

Five changes, ordered by dependency. Each is independently shippable and independently revertible.

### Design principles

1. **Every new gate fails open.** If a gate cannot evaluate itself, it admits the work and logs. `pkg/storage` is the cautionary example — a fail-closed no-op that can prevent `oro start` from booting.
2. **Ratchets, not thresholds, for debt.** Debt gates compare against a checked-in baseline and only fail when debt *increases*. Downward movement always passes and re-baselines.
3. **No gate may reduce existing coverage.** Any scoping change must strictly increase what is checked relative to today.

---

### C1 — Replace the oversized heuristic with a persisted estimate

**Decision:** gate on `beads.estimated_minutes`, produced once at task creation.

**Changes:**

| File | Change |
|---|---|
| `cmd/oro/cmd_bead.go` | In `oro task create`, when `--estimate` is absent, call the estimator once and persist the result |
| `pkg/dispatcher/dispatcher.go:7011` | Replace `CountDistinctModules` check with `bead.EstimatedMinutes > cfg.MaxTaskMinutes` |
| `pkg/dispatcher/dispatcher.go:7433` | Delete the per-assignment re-estimation; read the persisted value |
| `pkg/config` | Add `oversize.max_task_minutes` (default **90**) |
| `pkg/protocol/types.go` | Delete `CountDistinctModules`, `mirrorPrefixes`, `stripMirrorPrefix`, `parenAnnotation`, `isAllDigits` if unused (~120 LOC) |
| migration | Backfill `estimated_minutes` for the 1,578 beads lacking one |

**Fail-open behavior:** if the estimator errors or times out (`estimatorTimeout = 5s`), persist `0` and **admit** the task. Zero means "unknown", and unknown is never oversized.

**Clamping:** clamp estimator output to `[1, 480]`. The live max of 4,800 minutes (80 hours) is garbage; an unclamped value would create a new escalation loop — the exact failure mode this design exists to remove.

**Cost:** one estimator call per *task* instead of one per *assignment*. At 3.5 assignments per task, this is a ~70% reduction in estimator LLM calls.

**Expected effect:** with a mean estimate of 16 minutes against a 90-minute threshold, `OVERSIZED_BEAD` should fall from 1,104 to near zero. This is intentional. The gate becomes an outlier catcher, not a routine obstacle. If it turns out to fire never, deleting it entirely becomes the correct follow-up — and that will then be an evidence-backed decision rather than a guess.

---

### C2 — Give "oversized" a simplify exit

Today `routedOpsRunType` (`dispatcher.go:9773`) maps `EscOversizedBead → ops.OpsDecompose` and nothing else. The system structurally cannot respond to "too big" with "then build less."

**Changes:**

- Extend `decompose_prompt.go` to require the agent to choose and print one of three verdicts **before** creating any child task:
  - `VERDICT: simplify` — the acceptance criteria demand more than the outcome requires. Rewrite the AC to the minimum that satisfies the intent; do not create children.
  - `VERDICT: decompose` — the work is genuinely multiple independent outcomes. Create children.
  - `VERDICT: resolved` — the acceptance command already passes (this path already exists at step 2).
- Add `ops.VerdictSimplify` and route it to an AC rewrite rather than child creation.
- Remove the hardcoded **"Create 2-4 smaller child tasks"** fan-out. Replace with: *"create the fewest children that each deliver an independently verifiable outcome."*

**Rationale:** the prompt currently presupposes decomposition is correct and only asks *how many*. Making the agent justify decomposition against a simpler alternative is the cheapest possible intervention on the largest cost driver.

---

### C3 — Fix the quality gate's `cmd/` blind spot (prerequisite for C4)

**This must land before C4.** Scoping the gate while it has a hole would make the hole permanent.

| File | Change |
|---|---|
| `scripts/quality_gate.sh:1290` | `go test ... ./internal/... ./pkg/...` → add `./cmd/...` |
| `scripts/quality_gate.sh:1201` | `check_dead_exports` scans `pkg/ internal/` → add `cmd/` |
| `cmd/oro/remote_capabilities_test.go` | Fix the 3 fixtures broken by `e33f7187` (missing `private_key_ref`) |

Expect an initial burst of failures — 24,398 lines of source and 39,297 lines of tests have never been gated. Budget for a stabilization pass. The coverage threshold (`enforce_go_coverage_threshold`) must be recomputed against the new denominator or it will fail spuriously.

---

### C4 — Scale gate and review cost to change size

The dominant per-task cost is a fixed-price quality gate and a fixed-tier review. Both should be proportional to the change.

**C4a — Import-graph-scoped test lane (retry lane only).**

On a **QG retry**, run tests only for packages in the transitive reverse-dependency closure of the changed files:

```sh
changed=$(git diff --name-only "$base" -- '*.go' | xargs -n1 dirname | sort -u)
scope=$(go list -deps -test ./... | ...)   # reverse-dep closure of $changed
go test $race_flag $scope
```

**The full gate still runs before merge, unconditionally.** Scoping applies only to the retry loop, which is where the repeated cost lives (5,840 assignments against 1,655 tasks). This bounds the blast radius: a scoping bug can slow down convergence but cannot let an untested change merge.

*Why reverse-dependency closure and not just changed packages:* the `remotegate` regression is the proof case — a change in `pkg/config` broke tests in `cmd/oro`. Naive changed-package scoping would have missed it. The closure catches it.

**C4b — Review tier by diff size.**

`pkg/ops/ops.go:85` hardcodes `OpsReview → TierDeep` (opus, `reasoning: high`). Make it size-dependent:

| Diff size | Tier |
|---|---|
| < 50 changed lines | `balanced` |
| ≥ 50 changed lines | `deep` (unchanged) |
| any diff touching security-sensitive paths | `deep` (override) |

p50 diff is 59 insertions, so this routes roughly the lower half of changes to a cheaper reviewer. Configurable via `review.deep_tier_min_lines`.

**Guardrail:** track rejection rate and post-merge regression rate split by tier. If `balanced` reviews show a higher escaped-defect rate, raise the threshold. This is the one change in the design that could plausibly reduce quality, so it ships last and behind a metric.

---

### C5 — Ratchet `//oro:testonly` and expire its promises

232 annotations exist; 89 are "lands in a future task" promises with nothing verifying the future task exists.

**C5a — Freeze the count.**

```sh
count=$(rg -c --glob '!*_test.go' 'oro:testonly' pkg/ internal/ cmd/ | awk -F: '{s+=$2} END{print s}')
baseline=$(cat .oro/testonly-baseline)
if [ "$count" -gt "$baseline" ]; then
    echo "FAIL: testonly suppressions rose $baseline -> $count"
    exit 1
fi
if [ "$count" -lt "$baseline" ]; then
    echo "$count" > .oro/testonly-baseline   # ratchet down
fi
```

New speculative code cannot merge. Existing debt is grandfathered and drains monotonically.

**C5b — Promises must name a live task.**

New form: `//oro:testonly(oro-abcd) — wiring lands in <task>`. The gate fails when the cited task is `closed` while the symbol still has no production caller. This converts an untracked promise into a tracked one and makes abandonment visible.

**C5c — Fix the advertised escape hatch.**

`quality_gate.sh:1252` currently reads:

> `Fix: wire these functions from production code, remove them, or add //oro:testonly above.`

Reorder and qualify so suppression is last and conditional: wire it, delete it, or — only when a live task will wire it — annotate with that task ID.

**C5d — Remove the fail-closed no-op.**

Delete the `openStorageCatalog` call at `cmd/oro/cmd_start.go:1123`. It opens a database, creates 13 tables, closes it, discards the handle, and aborts `oro start` on error. This is independent of whether `pkg/storage` is deleted and should land regardless.

---

## Premortem

Classified after verifying each against the source.

### Tigers (mitigated)

| Risk | Mitigation |
|---|---|
| **Estimator garbage creates a new escalation loop.** Live max is 4,800 minutes. | Clamp to `[1, 480]`; estimator failure persists `0` and admits (C1 fail-open). |
| **Scoped tests hide cross-package regressions.** This is precisely how the `remotegate` defect escaped. | Reverse-dependency closure, not changed-packages. Full gate still mandatory pre-merge. Scoping is retry-lane only (C4a). |
| **`balanced` review tier lets defects through.** | Ships last, behind per-tier rejection and escaped-defect metrics. Revert is a one-line config change. |
| **C3 causes a failure burst that blocks all work.** 39,297 lines of never-gated tests. | Land C3 on its own, with a dedicated stabilization pass, before anything depends on it. Recompute the coverage denominator in the same change. |
| **New gates repeat the fail-closed mistake.** | Explicit design principle 1. Every gate in C1/C4/C5 admits on internal error. |

### Paper tigers

| Concern | Why it is acceptable |
|---|---|
| Baseline file churn in C5a | It is a ratchet — downward movement auto-passes and re-baselines. Only increases fail. |
| Removing "2-4 children" guidance yields one giant child | The estimate gate (C1) still catches genuinely oversized children. |
| Estimating at creation slows `oro task create` | One call, ≤5s, once per task, replacing ~3.5 calls per task at assign time. Net reduction. |

### Elephants (named, not solved)

1. **This does not fix epic integration.** The 17:1 failure ratio is untouched. C1 and C2 reduce *how much load* reaches that subsystem by producing fewer epics, but the subsystem remains the audit's largest single liability. Tracked as audit P2.
2. **This design adds gates to fix a problem caused by gates.** C5a and C5b are new failure modes. The justification is that they are ratchets on debt rather than thresholds on work — they can only ever demand that things not get worse. That distinction is load-bearing; if C5 starts blocking ordinary work, it has failed and should be reverted to report-only.
3. **The estimate is an LLM guess.** C1 replaces a deterministic-but-wrong signal with a nondeterministic-but-honest one. The same task may estimate differently on different days. This is acceptable only because the gate is a wide outlier catcher (90 min against a 16 min mean), not a tight bound. If the threshold is ever tightened, this becomes a real problem.

---

## Rollout

| Phase | Changes | Gate to proceed |
|---|---|---|
| 1 | C3 (`cmd/` in QG), C5d (remove fail-closed boot call) | Full suite green on `main` |
| 2 | C1 (estimate gate), C5a/C5c (ratchet + message) | `OVERSIZED_BEAD` rate drops; no new escalation class appears |
| 3 | C2 (simplify verdict), C5b (promise expiry) | Decompose:simplify verdict ratio observable |
| 4 | C4a (scoped retry lane) | Retry wall-clock down; zero escaped defects attributable to scoping |
| 5 | C4b (review tier by size) | Per-tier escaped-defect rate flat vs baseline |

Phases 1–3 carry low risk. Phase 4 needs a week of observation. Phase 5 is the only one that can degrade quality and is explicitly last.

## Success metrics

Baselines from the live database, measured before phase 1.

| Metric | Baseline | Target |
|---|---|---|
| `OVERSIZED_BEAD` escalations / 100 tasks | 28.2 | < 3 |
| Assignments per executed task | 3.5 | < 2.0 |
| Assignments consumed by worst 1% of tasks | 35% | < 15% |
| `//oro:testonly` count | 232 | monotonically decreasing |
| Unfulfilled future-task promises | 89 | 0 |
| Median QG wall clock on retry | ~2:20 | < 0:45 |
| Review rejection rate | 37% | ≤ 37% (must not worsen) |
| Escaped defects (broken `main`) | 1 open | 0 |

## Out of scope

- Epic integration machinery (audit P2)
- Delivery stage: deploy, release, rollback, production observation (audit P3)
- `dispatcher.go` decomposition into packages (audit P3)
- Deleting `pkg/storage` — only its boot-path call is removed here (C5d)
