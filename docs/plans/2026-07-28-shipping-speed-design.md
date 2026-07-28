# Design: Reduce Oro's Time-to-Ship

**Date:** 2026-07-28
**Status:** Revision 3 — post second adversarial review (rev 1 FAIL, rev 2 FAIL)
**Related:** `docs/audits/2026-07-28-architecture-review.md`

---

## Problem

Oro takes too long to ship. This is *not* a diff-size problem — merged commits are appropriately small (p50 = 59 insertions, p90 = 318 across 327 recent commits).

The cost is structural:

1. **Tasks get shattered.** A hardcoded gate refuses any task whose acceptance criteria cite more than two directories; the only remedy is decomposition into more tasks. Fired **1,104 times** — the largest escalation category.
2. **Every fragment pays full ceremony.** Each child independently pays a full quality-gate run plus an opus-high review, identically for a one-line clamp and a 500-line feature.
3. **Fragments ship unfinished layers.** Decomposition produces horizontal slices whose early children export symbols nothing calls, legitimized by `//oro:testonly` with a comment promising wiring "lands in a dependent task." 232 such annotations exist; **89 are explicit future-task promises**. `pkg/storage` is the terminal state: 13 tables, 41 suppressions, zero rows, wired fail-closed into `oro start`.

Decomposition converts the parent to `--type=epic` (`decompose_prompt.go:51`), routing it into the epic-branch machinery that fails **17:1** against successful merges. **Decomposition is the primary load source for Oro's least reliable subsystem.**

### Evidence

| Signal | Value | Source |
|---|---|---|
| `OVERSIZED_BEAD` escalations | **1,104** | live `state.db` |
| `decompose` ops runs | 214 of 240 | `ops_runs` |
| Assignments per executed task | **3.5×** | `assignments` |
| Assignments in worst 1% of tasks | **35%** | `assignments` |
| `//oro:testonly` repo-wide | **232**, 89 promises | `rg -c` (verified) |
| Epic integration failures vs merges | **3,562 : 205** | `events` |

## Non-goals

- Fixing epic integration itself (audit P2).
- Building the delivery stage (audit P3).
- Lowering the review bar. Rejection rate is 37%; this design reduces the *cost* of clearing it, never the height.

---

## Corrections carried from adversarial review

Rev 1 asserted several things that verification refuted. Recorded so they are not reintroduced:

| Rev 1 claim | Reality |
|---|---|
| The estimator is production-wired | `estimate.go:64` is `//oro:testonly`; `estimate_test.go:104` asserts `d.estimator == nil` in production and `:122` asserts no estimator spawns during assignment |
| An estimate gate at 90 min would catch outliers | `estimate.go:36` prompts for "an integer between 1 and 30"; `estimate.go:112` returns `0` for `n>30`. A 90-min threshold is structurally unreachable |
| The estimate at `dispatcher.go:7433` is discarded | It is consumed six lines later at `:7439` by `agentmodel.ResolveForBead` → `agentmodel.go:138` → `types.go:158`; it selects the **worker model tier** |
| `CountDistinctModules` has one caller | Two: `dispatcher.go:7011` and `escalation_precheck.go:119` |
| No other size contract exists | `pkg/taskcontract/validator.go:63` declares `[1,7]` minutes and has **zero production callers** |
| `cmd/` tests have never been gated | `.github/workflows/ci.yml:73` and `:137` run them on every push. The gap is the **local merge gate** only |
| `go list -deps -test ./...` computes the closure | It does not run here (`archive/` has intentionally broken Go; `cmd/oro/embed.go:9` needs `_assets` from `make stage-assets`). `-deps` returns a flat union with no edges |

---

## Design

### Principles

1. **Every gate fails open on internal error** — and an empty scope is an internal error, not a pass. `pkg/storage` is the cautionary tale: a fail-closed no-op that can block boot.
2. **Ratchets, not thresholds, for debt.** Debt gates compare to a checked-in baseline; only increases fail.
3. **No change may reduce what is checked** relative to today.
4. **Measure before optimizing.** Any performance work names its profile first.

---

### C0 — Profile the gate before optimizing it (blocks C4a)

Rev 1 attributed the dominant cost to the Go test lane without measurement. A stronger candidate exists: `check_dead_exports` (`quality_gate.sh:1201-1256`) runs a **full-tree `grep -rn` once per exported function** (`:1236`, inside the `while` loop over every `^func [A-Z]` match). That is on the order of a thousand whole-repository greps per gate run.

**Task:** instrument each lane and check with wall-clock timing; publish the breakdown. C4a does not proceed until this exists.

If `check_dead_exports` dominates, the correct fix is a single `go list`-based caller index — not import-graph test scoping. That would make C4a unnecessary, which is the cheapest possible outcome.

---

### C1 — Delete the oversized gate

**Decision:** delete, do not replace. An estimate-based gate would require wiring a deliberately-disabled estimator, raising its ceiling, reconciling `taskcontract`'s conflicting `[1,7]`, covering 8 non-CLI creation paths, pinning a worker-tier side effect, and backfilling 1,578 beads — to buy a gate that a 30-minute ceiling makes nearly unreachable anyway.

**Delete:**

| Site | What |
|---|---|
| `pkg/protocol/types.go:211-278` | `CountDistinctModules`, `mirrorPrefixes`, `stripMirrorPrefix`, and `parenAnnotation`/`isAllDigits` if they become unused |
| `pkg/dispatcher/dispatcher.go:7011-7022` | The admission gate, including its `isEpic`/`hasChildren` bypass |
| `pkg/dispatcher/escalation_precheck.go:119` | The retry predicate — **must land in the same change or the build breaks** |
| `pkg/protocol/types_test.go:287-539` | 6 test functions |
| `scripts/test_managerless_oversized_e2e.sh` | Orphaned e2e (reads `CountDistinctModules` at `:117`); tests a flow that ceases to exist |

**`retryOversizedBead` is removed as a whole**, not repointed. Verified: it has exactly one caller (`dispatcher.go:9815`) and its short-circuits at `escalation_precheck.go:109-118` serve no other escalation type — every other type has its own predicate (`:9805`–`:9817`).

> **The switch case must be repointed, not deleted.** `dispatcher.go:9803-9823` ends in `default: return true` ("Unknown escType: always retry"). Removing `case protocol.EscOversizedBead:` (`:9814`) drops control into that arm and converts all **1,104** open escalations into permanent retries — the exact failure this section claims to prevent. Required:
> ```go
> case protocol.EscOversizedBead:
> 	return false   // gate deleted; escalation is stale, drain it
> ```
> plus a regression test asserting `shouldRetryEscalation` returns `false` for `OVERSIZED_BEAD`, and a one-time drain in the same change:
> `UPDATE escalations SET status='acked' WHERE type='OVERSIZED_BEAD' AND status='pending'`

**Remaining consumers become dead branches.** `dispatcher.go:7017` is the **sole producer** of `EscOversizedBead` (verified: only `FormatEscalation` call site). After deletion these still reference an escalation nothing raises: `:9440-9452`, `:9666`, `:9773`, `:9850`, `:9906`, `:9941-9980`, `:10165-10178`, and `factoryhealth/health.go:1013`. They are harmless but must be enumerated and consciously left or removed — not discovered later.

**Oversized becomes a review verdict, not an admission gate.** A too-large task is admitted, attempted, and either passes (it wasn't too large) or fails review. Note this routes through `routeReviewOpsRun` (`ops_runs.go:460`), **not** through `EscOversizedBead` — see C2.

**Acceptance (runnable):**
```
Cmd: ! rg -q 'CountDistinctModules' pkg/ cmd/ scripts/ && make stage-assets && go build ./cmd/... ./internal/... ./pkg/... && go test ./pkg/dispatcher/... ./pkg/protocol/...
Assert: exit 0
```
(`go build ./...` cannot pass here — `archive/` and `_assets`, per the corrections table above.)

**Follow-up, not in scope:** if an outlier catcher proves necessary, wire the existing `pkg/taskcontract` (re-deriving its range from live data) rather than adding new config. That pays down unwired debt instead of adding a gate.

---

### C2 — Reduce decomposition fan-out *(descoped)*

**Rev 2 proposed a three-verdict grammar. That is withdrawn.** C1 deletes `dispatcher.go:7017`, the sole producer of `EscOversizedBead` — so `OpsDecompose` would never spawn, and a five-file grammar change plus parser rewrite would ship into a code path with no trigger. Rev 2 scheduled it two phases *after* C1, guaranteeing it was dead on arrival.

**What remains is one string.** `decompose_prompt.go:44` currently reads *"Create 2-4 smaller child tasks."* Replace with:

> *"Create the fewest child tasks that each deliver an independently verifiable outcome. Prefer rewriting the acceptance criteria to the minimum that satisfies the intent over splitting the work."*

This still lands the intended behavior change — decomposition must justify itself against a simpler alternative — for the residual paths that spawn `OpsDecompose`, at a fraction of the surface.

**Unresolved contradiction, recorded not fixed:** `ops.go:73` documents `OpsDecompose` as *"spawned when a bead exhausts all worker retry attempts,"* but the only wired spawn is the oversized path that C1 removes. Either the doc comment is aspirational or a retry-exhaustion trigger was intended and never wired. Resolve before building anything further on decompose.

**Acceptance:**
```
Cmd: rg -q 'fewest child tasks' pkg/ops/decompose_prompt.go && ! rg -q '2-4 smaller' pkg/ops/decompose_prompt.go && go test ./pkg/ops/...
Assert: exit 0
```

---

### C3 — Close the merge gate's `cmd/` hole

**Premise corrected.** CI already tests `./cmd/...` (`ci.yml:73`, `:137`). The hole is in the **local merge gate**. This is a smaller, safer change than rev 1 claimed — and the dead-export burst is bounded: 72 exported functions across `cmd/`, and the *caller* search at `quality_gate.sh:1236` already includes `cmd/`; only the *definition* scan at `:1245` needs widening.

| File | Change |
|---|---|
| `scripts/quality_gate.sh:1291` | Add `./cmd/...` to the test lane |
| `scripts/quality_gate.sh:1245` | Add `cmd/` to the `check_dead_exports` definition scan |
| `cmd/oro/remote_capabilities_test.go` | Fix the 3 fixtures broken by `e33f7187` (missing `private_key_ref`) |

**Do not move the coverage threshold.** `ci.yml:75-77` already solved this correctly: test `cmd/`, exclude it from the coverage denominator, with a written rationale. Mirror that — leave `-coverprofile` scoped to `./internal/... ./pkg/...`. Rev 1's proposal to recompute the threshold would have permanently weakened the bar for `pkg/` and `internal/` to accommodate `cmd/`.

Separately, `should_enforce_go_coverage_threshold` (`:776-793`) derives its diff from `internal/ pkg/` only, so `cmd/`-only changes skip the coverage gate entirely. That is now the *documented* contract rather than an accident — state it in the script.

**The real risk is concurrency, not correctness.** `cmd/oro` tests install git hooks (`cmd_init.go:543`), resolve paths under `~/.oro` (`paths.go:230-248`), and spawn tmux. CI runs them once on a clean runner; the local gate's main phase is deliberately **lockless across worktrees** (`quality_gate.sh:471-473`), so N concurrent gates would share one `$HOME` and one tmux server. `-p 3 -shuffle=on` is *not* the hazard — `-p` parallelizes packages as separate processes, so the 499 `t.Setenv` and 31 `os.Chdir` uses are process-local and safe.

**Prerequisite task:** audit `cmd/oro` tests for `$HOME` / tmux / git-hook isolation and add host-touching ones to `pkg/dispatcher/testdata/serial_lane_tests.txt` **before** widening the lane.

---

### C3b — Pause admission when `main`'s CI is red *(rescoped)*

The corrected audit finding stands: CI catches these defects, **CI is red on 4 of the last 5 `main` runs**, and merges proceed anyway.

**Rev 2's shape was unworkable and its justification was wrong.** Two verified errors:

- `ci.yml:3-7` triggers **only** on `push: [main]` and `pull_request: [main]`. Oro ff-merges `agent/*` branches locally and never opens PRs, so the commit at pre-FF time has never had a CI run and never will. Combined with fail-open, a pre-merge check admits 100% of merges — a no-op that reads as shipped.
- The claim that `pkg/remotegate` "already models this (GitHub CLI, workflow status, `MaxInFlight`)" is false. Verified: `MaxInFlight` does not exist in the package, nothing in it invokes `gh` (`github/preflight.go:17` takes an injected `APIReader`), and it inspects workflow *registration*, never run *conclusions*. That sentence is withdrawn.

**Correct shape — gate admission, not merge.** Before assigning new work, if `main`'s most recent CI conclusion is `failure`, pause the assign loop and escalate. This reads a commit CI has actually observed, needs no merge-path surgery, and directly serves the "red CI runs on `main`: 4 → 0" metric.

**Reuse `pkg/janitor`.** `detect.go:201` already runs exactly the needed query:
```go
runCIProbe(ctx, worktree, gh, "run", "list", "--branch", targetBranch,
    "--limit", "100", "--json", "databaseId,workflowDatabaseId,workflowName,conclusion,url")
```
with `errDetectorSkipped` on missing `gh` (`:182`) and on unauthenticated (`:202`) — **fail-open is already built**. Do not reimplement.

**Sequencing:** must land after `main` is green (phase 1), or it halts the factory on arrival.

---

### C4 — Scale gate and review cost to change size

**Gated on C0.** If profiling shows `check_dead_exports` dominates, fix that instead and drop C4a.

**C4a — Scoped retry lane.**

*Prerequisite plumbing (its own task).* There is no retry lane today: the worker runs the identical script with an identical environment on attempt 1 and attempt N (`worker.go:957-969`, `qualityGateEnv` at `:1938` sets only `ORO_SKIP_MUTATION` and `ORO_MUTATION_BASE`). `AssignPayload.Attempt` exists (`protocol/message.go:127`) and the dispatcher computes it (`dispatcher.go:2389`), but it is never threaded to the gate invocation. Required: dispatcher populates `Attempt` on the qg-retry ASSIGN → worker stores it → `qualityGateEnv` emits `ORO_QG_SCOPE_BASE` only when `Attempt > 1`.

**`ORO_QG_SCOPE_BASE` must be added to `processenv.StripQualityGateEnv` (`pkg/processenv/env.go:97-104`).** That list exists precisely because a leaked `ORO_QG_*` seam "would let a leaked daemon environment skip the entire gate and pass with zero checks" (`env.go:89-91`). A scope variable is exactly such a seam.

*The closure computation.* Must run **after** `ensure_stage_assets` (`quality_gate.sh:740`) and must never use `./...` (see the script's own rationale at `:1300-1306`):

```sh
mod=$(go list -m)
changed=$(git diff --name-only "$base" -- '*.go' | xargs -n1 dirname | sort -u | sed "s|^|$mod/|")
pat=$(printf '%s|' $changed); pat="(^| )(${pat%|})( |\$)"
scope=$(go list -test -f '{{.ImportPath}} {{join .Deps " "}}' \
          ./cmd/... ./internal/... ./pkg/... |
        grep -E "$pat" | awk '{print $1}' |
        sed -E 's/ \[.*\]$//; s/\.test$//; s/_test$//' | sort -u)
if [ -z "$scope" ]; then
    echo "FAIL: empty test scope — refusing to run a vacuous lane"
    return 1
fi
go test $race_flag $scope
```

`$base` reuses `mutation_base_ref()` (`quality_gate.sh:751-773`) — do not introduce a second base concept. The `s/_test$//` arm is **required**: without it the pipeline emits 8 unbuildable names for a `pkg/config` change (`oro/pkg/config_test`, `oro/pkg/dispatcher_test`, …) and every retry errors. Acceptance must assert every element of `$scope` is accepted by `go list`.

`-test` is load-bearing: a plain package's `.Deps` omits test-only imports, so the `cmd/oro`-depends-on-`pkg/config` case only appears via synthetic `.test` rows. Verified to produce a 17-package closure for a `pkg/config` change.

**Empty scope hard-fails.** A `git diff` limited to `assets/`, `testdata/`, `scripts/`, or `go.mod` yields no Go dirs; an empty `$scope` would make `go test` test only the current directory and exit 0 — a silent near-total skip. That is the inverse of principle 1 and must be an explicit failure, not a fallback that quietly passes.

**The full gate still runs pre-merge, unconditionally.** This is genuinely enforceable: the pre-merge path is a different code path with a different env builder (`d.qgRunner.Run` at `dispatcher.go:2803/2959/3993`, `qgRunnerEnv` at `:572`) which never sets the scope variable.

**C4b — Review tier by diff size.**

`ops.go:84-85` hardcodes `OpsReview → TierDeep`. `Type.Tier()` is a pure method on a string type with no access to a diff, so this cannot be changed in place.

| File | Change |
|---|---|
| `pkg/ops/ops.go:79-130` | `Tier()`/`Model()`/`Role()` — resolve tier at spawn time instead |
| `pkg/ops/ops.go:600-640` | `spawnRouting` override |
| `pkg/dispatcher/ops_runs.go:616` | `agentmodel.ResolveForRole` call site |
| `pkg/ops/ops_test.go:912`, `:933` | **Expected to change** — currently pin `OpsReview → TierDeep`/opus |

Rule: `< review.deep_tier_min_lines` (default 50) → `balanced`; otherwise `deep`. Ships last, behind per-tier escaped-defect metrics. Revert is a one-line config change.

---

### C5 — Ratchet `//oro:testonly` and expire its promises

**C5a — Freeze the count.** Baseline snapshotted **after C3 stabilizes**, because C3 widens `check_dead_exports` to `cmd/` and the gate's own advertised remedy is to add a suppression — C3 predictably drives the count *up* first.

```sh
count=$(rg -c --glob '!*_test.go' 'oro:testonly' pkg/ internal/ cmd/ 2>/dev/null | awk -F: '{s+=$2} END{print s+0}')
```

`rg -c` exits 1 on no matches, so `2>/dev/null` plus `s+0` is required or the ratchet crashes at zero. Note it counts *lines*: two annotations on one line undercount. Acceptable — the ratchet only needs monotonicity.

**Check-only. No auto-ratchet.** Rev 2 proposed "report-only in worktrees, enforcing pre-merge." That is unimplementable on three counts, all verified: the pre-merge gate **also** runs in a worktree (`dispatcher.go:3130` `finalWorktree`, `:2952` for epic QG), so `--git-dir`/`--git-common-dir` cannot discriminate; that worktree is **deleted** (`defer d.worktrees.Remove`, `:2957`), so a downward re-baseline is discarded every time; and the main phase is explicitly lockless across worktrees (`quality_gate.sh:471-473`), so "single-threaded" is false.

Therefore the gate **only fails on increase and never writes**. Lowering the baseline is a separate, explicit, human-committed step. This removes the detection problem, the persistence problem, and the concurrency problem at once, and loses nothing — the ratchet needs monotonicity, not automation.

**C5b — Promises name a live task.** Form: `//oro:testonly(oro-abcd) — wiring lands in <task>`. Gate fails when the cited task is `closed` and the symbol still has no production caller. **Must fail open when no bead store is reachable** — `quality_gate.sh` is generated for other projects by `cmd/oro/quality_gate_gen.go:251-404`, where no bead DB exists.

**C5c — Fix the advertised escape hatch.** `quality_gate.sh:1252` currently reads *"wire these functions from production code, remove them, or add //oro:testonly above."* Reorder so suppression is last and conditional on a live task ID.

**C5d — Remove the fail-closed boot call.** Delete `openStorageCatalog` at `cmd/oro/cmd_start.go:1123`. Independently verified safe: `storage_controller.go:22-24` returns `true` when the controller is nil, so all five admission gates are already fail-open. `openStorageCatalog` (`cmd/oro/db.go:30`) then has only test callers; it is unexported so `check_dead_exports` (which matches `^func [A-Z]`) won't flag it, but budget for golangci-lint's `unused` analyzer.

---

## Premortem

### Tigers (mitigated)

| Risk | Mitigation |
|---|---|
| Deleting the gate admits genuinely huge tasks | They fail review and route to decompose via the existing path. The gate never measured size anyway |
| Stranded `OVERSIZED_BEAD` escalations retry forever | `retryOversizedBead` removed wholesale in the same change as the gate (C1) |
| Scoped tests hide cross-package regressions | Reverse-dep closure via `-test`; full gate mandatory pre-merge; scope var stripped from the pre-merge env |
| Empty scope silently passes | Hard-fail, not fallback (C4a) |
| C3 flakes under concurrent worktrees | Host-isolation audit is a prerequisite task; host-touching tests go to the serial lane |
| `balanced` review lets defects through | Ships last, behind per-tier metrics; one-line revert |
| C5a blocks unrelated work via baseline conflicts | Report-only in worktrees; enforcing only pre-merge |
| C5b breaks generated gates | Fails open with no bead store |

### Paper tigers

| Concern | Why acceptable |
|---|---|
| Removing "2-4 children" yields one giant child | Review catches it; the numeric guidance was arbitrary |
| `rg -c` line-counting undercounts | Ratchet needs monotonicity, not exactness |

### Elephants (named, not solved)

1. **This does not fix epic integration.** The 17:1 ratio is untouched. C1/C2 reduce the *load* reaching it by producing fewer epics. Audit P2.
2. **C0 may invalidate C4a entirely** — and that is the preferred outcome. If a caller index fixes `check_dead_exports`, the riskiest change in this plan is unnecessary.
3. **Deleting C1's gate removes the only pre-assignment size check.** Nothing replaces it. This is deliberate: an unreachable gate provided no protection, and the honest position is to admit that rather than simulate coverage.
4. **C3b depends on CI being trustworthy.** CI is currently red; wiring merge to CI while CI is red would halt the factory. C3b must land *after* `main` is green.

---

## Rollout

| Phase | Changes | Gate to proceed |
|---|---|---|
| 0 | C0 profiling; C5d (boot call) | Lane timing breakdown published (state the measured grep count; C3 raises it ~10%) |
| 1 | C3 host-isolation audit → C3 (`cmd/` in gate), fix 3 fixtures | Full suite **and CI** green on `main` |
| 2 | C1 (delete gate + repoint switch case + drain 1,104 rows), C2 (prompt string), C5c | `OVERSIZED_BEAD` → 0 **and** escalation queue drains — verify both; a stuck queue is the C1 failure mode |
| 3 | C3b (pause admission on red `main`), C5a (check-only ratchet, baseline now) | Admission pauses correctly on a seeded red run; never blocks when `gh` is absent |
| 4 | C5b (promise expiry) | Stale promises detected in a seeded case |
| 5 | C4a — **only if C0 justifies it** | Every `$scope` element accepted by `go list`; retry wall-clock down; zero escaped defects |
| 6 | C4b (review tier) | Per-tier escaped-defect rate flat |

C2 moves to phase 2 alongside C1 — it is now a single prompt string, and sequencing it after C1 was what made rev 2's version unreachable.

## Epic acceptance

```
Cmd: git checkout main && ! rg -q 'CountDistinctModules' pkg/ cmd/ && ORO_QG_CONTEXT=local ./scripts/quality_gate.sh
Assert: exit 0
```
(Symbol gone repo-wide **and** the full local gate green with `cmd/` in the test lane.)

## Success metrics

| Metric | Baseline | Target |
|---|---|---|
| `OVERSIZED_BEAD` per 100 tasks | 28.2 | **0** (gate deleted) |
| Assignments per executed task | 3.5 | < 2.0 |
| Assignments in worst 1% of tasks | 35% | < 15% |
| `//oro:testonly` count | 232 | monotonically decreasing |
| Unfulfilled future-task promises | 89 | 0 |
| Gate wall clock (dominant lane, from C0) | TBD by C0 | −50% |
| Review rejection rate | 37% | ≤ 37% (must not worsen) |
| Red CI runs on `main` (last 5) | **4** | 0 |

*The rev 1 metric "median QG wall clock on retry 2:20 → <0:45" is withdrawn: the baseline predates C3, which adds `./cmd/...` to the lane, so it was confounded. C0 establishes an honest baseline.*

## Out of scope

- Epic integration machinery (audit P2)
- Delivery stage (audit P3)
- `dispatcher.go` package decomposition (audit P3)
- Deleting `pkg/storage` — only its boot-path call (C5d)
- Wiring `pkg/taskcontract` — noted as C1's follow-up, not committed
