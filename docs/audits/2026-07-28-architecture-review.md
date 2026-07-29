# Oro Architecture Review — Skeptical but Fair

**Date:** 2026-07-28
**Commit reviewed:** `8f4c1bca` (main)
**Method:** built from source, ran full Go test suite, queried the live production state database (`~/.oro/projects/oro/state.db`, 426 MB, 3,919 tasks, 353,151 events), traced the assign→QG→review→merge path, and analyzed 4,616 commits of history.

---

## 0. Headline

**The test suite does not pass on `main` at HEAD.** Three tests in `cmd/oro` fail, introduced by the two most recent commits (`e33f7187`, `8f4c1bca`). The reason they escaped is structural, not incidental:

```sh
# scripts/quality_gate.sh:1290
GOFLAGS=-buildvcs=false go test $race_flag -shuffle=on -p 3 \
    -coverprofile="$COVERAGE_FILE" ./internal/... ./pkg/... || return 1
```

The 1,677-line quality gate lints `./cmd/...` (line 1156), runs NilAway on `./cmd/...` (line 1260), and builds `./cmd/...` (line 1309) — but **never tests it**. That is 24,398 lines of production source and 39,297 lines of tests that the merge gate never executes.

> **Correction (post-review).** An earlier draft of this section implied those tests are never run anywhere. That is wrong: `.github/workflows/ci.yml:73` runs `go test -race -shuffle=on ./internal/... ./pkg/... ./cmd/...` on every push, and `:137` repeats it CGO-free. The gap is specific to the **local worker quality gate** — the thing that actually gates a merge.
>
> The corrected picture is worse, not better. CI *does* catch this, and **CI is red**: 4 of the last 5 runs on `main` failed (`gh run list`), including the two most recent. So the signal exists, fires correctly, and merges proceed anyway — because nothing in the dispatcher's merge path consults CI. Oro's merge gate and Oro's CI disagree about what "green" means, and only the weaker one is enforced.

Everything else in this review is downstream of that fact. A factory whose defining claim is mechanically-enforced correctness has a 28%-of-source blind spot in the mechanism, and a real defect walked through it in the last 48 hours.

### The same pattern, twice: checks taught to stay quiet

The `./cmd/...` gap is not isolated. The quality gate also runs a dead-export detector, suppressed by an `//oro:testonly` annotation. `pkg/storage` alone carries **41 of those annotations across 9 files** — one unused package has trained the merge gate to ignore 41 exported symbols.

And one of those annotations is now false:

```go
//oro:testonly — production wiring lands when the catalog foundation is integrated.
func OpenCatalog(ctx context.Context, path string) (*Catalog, error) {
```

`OpenCatalog` acquired a production caller at `cmd/oro/cmd_start.go:1123` — on the boot path — while still claiming to be test-only. The annotation was a promise about the future that nobody revisited when the future arrived.

Both mechanisms designed to catch "built but not wired" have been progressively configured to stay silent about the largest instance of it in the repository. That is a more serious finding than either subsystem's line count, because it means the quality signal itself is degrading in the same direction as the code.

---

## 1. The actual job

Before judging the implementation, here is the minimum a credible software factory needs. I will hold Oro to this, not to a size budget.

| # | Capability | Minimum bar |
|---|---|---|
| 1 | Accept and clarify an objective | A human states intent; ambiguity is surfaced before code |
| 2 | Produce a sufficient spec/plan | Enough that a competent agent doesn't guess |
| 3 | Decompose without losing coherence | Sub-tasks are individually executable and jointly complete |
| 4 | Execute safely, possibly in parallel | Isolation so concurrent agents can't corrupt each other |
| 5 | Test, review, integrate | Automated gate + one independent judgment pass + merge |
| 6 | Survive failures and context limits | Durable state; resume without losing work |
| 7 | Escalate genuine judgment calls | Humans see the decisions only humans should make |
| 8 | **Deliver functioning software** | Not "code on main" — running, observed, revertible software |

### Intended operating scale

This matters enormously and Oro's architecture is calibrated for a scale it does not operate at.

- **Actual:** one operator, `MaxWorkers` default **10** (`pkg/dispatcher/dispatcher.go:708`), one primary repository (Oro itself), macOS laptop, single host.
- **Architected for:** multi-tenant. `pkg/storage/catalog.go` defines `namespaces`, `providers`, `controllers`, `leases`, `runtime_leases`, `runtime_pause_epochs`, `runtime_reconciliation_cursors`, `runtime_tombstones` — a distributed-systems control plane.

`~/.oro/projects/` has 38 directories, which looks like multi-repo scale. It is not: they include `foo`, `testproject`, `checkonly-proj`, and one directory literally named

```
2026-03-13-fda-letter 2026-03-13-sp-ai-gist foo test_project wyndly-gh personal-finance-analysis shopify_order_data_processing scriptwriter aipkm df132422 df2026051416530428526 dfmgr2026051419513591360 wh-allergy-test testproject
```

— a directory created by an unquoted shell variable expansion. The multi-project surface is mostly test residue plus a bug.

**Verdict on scale: the architecture is provisioned for dozens of agents across many repos; the demonstrated workload is ~10 local agents on one repo.**

---

## 2. Verdict

> **Oro is simultaneously overengineered as an orchestration system and underengineered as a software factory — and the overengineering is now measurably consuming the capacity that would fix the underengineering.**
>
> The core loop is real and works: 61% of assigned tasks (1,003 of 1,655) complete in a single worker assignment, and 3,654 tasks have closed. Durable SQLite state, git-worktree isolation, heartbeat liveness, and a scripted quality gate are all *earning their keep* and should be kept. But around that ~15k-line core sit ~70k more lines of orchestration whose failure events outnumber its successes: the epic-integration machinery alone emitted **3,562 failure events against 205 successful merges** (17:1). The system's own event log is 89.6% a single hot-loop spam event. 1% of tasks consumed 35% of all worker assignments in non-converging rejection loops — including 217 agent runs to consolidate four call sites and 123 runs to clamp an integer. Meanwhile the pipeline terminates, by the README's own words (line 90, line 184), at **"Code on main"**: there is no deployment, no release, no rollback, no e2e/product acceptance, no production observation, and no user feedback anywhere in 84,651 lines of source. The `fix:feat` commit ratio has worsened monotonically every month from **0.90 → 2.36**, which is the signature of a system that has crossed the point where added machinery costs more than it returns.

### Confidence

**High (~85%)** on the overengineering-plus-underengineering verdict. The load-bearing evidence is live production data and a currently-red test suite, not aesthetic reaction.

### Strongest evidence *against* my own conclusion

I want to state this fairly, because it is genuinely strong:

1. **The factory built itself.** 4,616 commits in ~6 months from one human, and 4,559 of them are authored through the machine. Whatever the inefficiency, the artifact exists and is substantial. A simpler system that produced less would not obviously be better.
2. **61% first-pass success is a real number.** 1,003 tasks went assign→QG→review→merge with zero retries. That is the loop working exactly as designed, and it is not trivial to achieve.
3. **The pathologies are concentrated, not diffuse.** 35% of waste sits in 16 tasks; 89.6% of DB bloat is one event type; the test blind spot is one missing `./cmd/...`. These are *bugs in specific subsystems*, not proof that the architecture is wrong. A defender can reasonably argue: fix five things, and the same architecture looks appropriately engineered.
4. **Test-to-source ratio of 2.1:1** (177,555 test LOC vs 84,651 source) reflects genuine discipline, and much of the "bloat" I'm counting is tests that a leaner project simply wouldn't have written.
5. **Some complexity I'd call premature is <2 weeks old.** `pkg/remotegate` may be mid-delivery of a real capability (CI-gated PRs) that would meaningfully close the delivery gap. Judging it as waste at day 10 may be unfair.

The honest summary of the counter-case: *Oro's problems may be a maintenance backlog rather than an architectural verdict.* I weight that at ~15% because the `fix:feat` trend is monotonic across five months — backlogs that are being worked off don't look like that.

---

## 3. Complexity quantified

| Dimension | Measurement | Source |
|---|---|---|
| Non-test Go LOC | **84,651** | `find . -name '*.go' ! -name '*_test.go'` |
| Test Go LOC | **177,555** (2.1:1) | same |
| Go files | 782 | same |
| Go packages | 33 in `pkg/` | `ls pkg/` |
| Binaries | 6 (`oro`, `oro-dash`, `oro-capture-hook`, `oro-search-hook`, …) | `ls cmd/` |
| **Largest single file** | **`pkg/dispatcher/dispatcher.go` — 11,345 lines, 444 functions** | `wc -l`, `rg -c '^func '` |
| `Dispatcher` struct | **224 fields/lines** | `sed -n '/^type Dispatcher struct/,/^}/p'` |
| Background goroutine loops | 14 named loops + 136 `go` statements in `pkg/dispatcher` | `rg 'func \(d \*Dispatcher\) \w*[Ll]oop'` |
| **SQLite tables** | **61**, across **6 independent schema owners** | `rg 'CREATE TABLE'` |
| Schema owners | `protocol/schema.go` (36 tables), `storage/catalog.go` (13), `cards/schema.go` (5), `dispatcher/store.go` (2), `codesearch/index.go` (1), `cmd/oro/db.go` | — |
| **Distinct event types in production** | **116** | `SELECT COUNT(DISTINCT type) FROM events` |
| Escalation types | 14 | `pkg/protocol/types.go:182-196` |
| Worker states | 7 (incl. 3 transient) | `pkg/protocol/types.go:169-176` |
| Configured agent roles | **17** | `.oro/config.yaml` |
| CLI verbs | **~120** | `rg -o 'Use:\s+"..."' cmd/oro/*.go` |
| Shell/Python script LOC | 7,581; `quality_gate.sh` alone **1,677** | `find scripts -type f` |
| Agent hooks | 27 Python/shell hooks | `ls assets/hooks/` |
| **Dead-export suppressions in one unused package** | **41 `//oro:testonly` in `pkg/storage`**, across 9 files — one of them now false | `rg 'oro:testonly' pkg/storage` |
| Docs | 192 markdown files, 4.4 MB | `find docs -name '*.md'` |
| Runbooks | 11 | `ls docs/runbooks/` |
| **Live `state.db`** | **426 MB** | `ls -la ~/.oro/projects/oro/` |
| Orphaned WAL | **462 MB** `code_index.db-wal` never checkpointed | same |
| Orphaned zero-byte DBs | 7 (`state.db`, `.oro/oro.db`, `.oro/state.db`, `.oro/state.sqlite`, `beadstore.db`, `events.db`, `oro.db`) | `ls`, `find` |

### Processes and communication paths

For a single task, the live paths are: `oro` CLI → SQLite; CLI → **Unix domain socket** → dispatcher; dispatcher → **agent subprocess** (`claude -p` / `codex`) → filesystem worktree; worker → UDS → dispatcher; dispatcher → **ops agent subprocess**; dispatcher → **git subprocess**; dispatcher → **`quality_gate.sh` subprocess**; dispatcher → **tmux** panes; dispatcher → **HTTP/SSE** server; `oro-dash` TUI → SQLite; hooks → files in `.oro/`. That is **7 distinct IPC mechanisms** (UDS, subprocess exec, SQLite, filesystem sentinels, tmux, HTTP/SSE, signals) for a single-host single-operator system.

### Ways a task can fail, retry, or escalate

14 escalation types × 4 assignment terminal statuses (`completed`, `quarantined`, `requeued`, `abandoned`) × 8 recovery-quarantine reasons × QG retry (max 8 store attempts) × review retry (`maxReviewRejections = 2`, `dispatcher.go:4916`) × context-exhaustion handoff × epic-rebase recovery paths. Empirically, the live DB records **116 event types**, of which ~60 are failure/recovery transitions.

**Operational knowledge required to debug it:** 11 runbooks, plus — from the operator's own memory file — 15+ distinct hand-learned heuristics just for adjudicating *rebase ancestry* edge cases, with titles like `rebase-ours-merge-then-rebase-onto-epictip-drops-main-is-reject`. That is a strong signal: when a subsystem needs a 15-entry decision tree of memorized special cases to operate, the subsystem is wrong, not the operator.

---

## 4. Does it deliver software, or code on main?

I searched all 84,651 lines of non-test source for delivery vocabulary:

| Term | Hits | What they actually are |
|---|---|---|
| `deploy` | 24 | `warnIfEpicCNotDeployed` (a prompt-config warning) and `autoRedeployablePreservedWorktrees` (recovery). **Zero software deployment.** |
| `release` | 102 | `releaseWorkerAfterDoneTerminal`, `ReleaseLease`, `releaseAssignmentReservation` — **all mutex/lease release.** Zero software release. |
| `rollback` | 26 | SQLite transaction rollback and `epic_preserve_rolled_back`. Zero deployment rollback. |
| `production` | 2 | Only in `pkg/testdata/sample.jsonl` fixture text. |
| `canary`, `e2e`, `smoke` | **0** | — |
| `staging` | 2 | Unrelated. |

The README's own pipeline diagram (lines 92–185) terminates at:

```
 │  11. MERGE + PUSH                                       │
 └─────────────────────────────────────────────────────────┘
                    Code on main
```

**Assessment of the eight capabilities:**

| Capability | Status | Evidence |
|---|---|---|
| 1. Accept/clarify objective | ✅ Strong | `brainstorming` skill, `spec` Stage-2 consultation gate |
| 2. Spec/plan | ✅ Strong | `spec`, `premortem`, `writing-plans` skills; `docs/plans/` |
| 3. Decompose | ⚠️ **Weak in practice** | **1,104 `OVERSIZED_BEAD` escalations** — decomposition produces too-large tasks more often than any other failure |
| 4. Execute in parallel | ✅ Strong | Worktrees + UDS + assignment leases; genuinely works |
| 5. Test/review/integrate | ⚠️ **Holed** | QG never runs `./cmd/...`; review capped at 2 cycles; integration is the largest failure source |
| 6. Survive failures | ✅ Strong | SQLite durability, 245 context handoffs across 122 assignments, checkpointing |
| 7. Escalate to human | ⚠️ **Miscalibrated** | 4,195 escalations for 3,919 tasks — ~1:1. `MERGE_COMPLETE` (1,063) is a *success notification* filed as an escalation |
| 8. **Deliver software** | ❌ **Absent** | No deploy, release, rollback, e2e, observation, or feedback |

**Also absent:** environment provisioning, product-level acceptance testing, production observability, user feedback ingestion, product iteration loops, cross-repository work, and long-running ownership after initial implementation.

This is the crux. Oro has built an extremely elaborate apparatus for the *middle* of the value chain (task → merged commit) and nothing at all for the end (merged commit → working software in front of a user). The name "software factory" overstates it by one full stage. It is a **merge factory**.

---

## 5. What the live data says about throughput

Queried from `~/.oro/projects/oro/state.db`.

### Task outcomes
| Metric | Value |
|---|---|
| Tasks (`beads`) total | 3,919 |
| Closed | 3,654 (93%) |
| Tasks that ever received an assignment | 1,655 |
| Total assignments | **5,840** → **3.5 assignments per executed task** |
| Assignments completed / quarantined / requeued / abandoned | 5,626 / 87 / 64 / 63 |
| Context-exhaustion handoffs | 245 across 122 assignments (max 5 on one task) |

### The distribution is the finding

```
assignments  tasks
     1       1003   ← 61% one-and-done. This is the system working.
     2        279
     3        120
   4-10       195
  11-50        45
  51-100        4
 101-236       11   ← 1% of tasks
```

**16 tasks consumed 2,068 of 5,840 assignments — 35% of all worker capacity.** And they are not hard tasks:

| Task | Assignments | Title |
|---|---|---|
| `oro-jwwt.1` | **236** | Create rejection_history table and migrate rejection feedback |
| `oro-cmwv` | **217** | Consolidate 4 AssignPayload call sites to use buildAssignPayload |
| `oro-aers` | **189** | P0: TestPriorityContention flaky under parallel QG load |
| `oro-to1u` | **166** | oro-dash shows offline — raw socket connections get no ACK |
| `oro-80fk` | **148** | Write project.root in init, consolidate stopConfig |
| `oro-ep57` | **138** | Add `oro attach` command to connect to running tmux session |
| `oro-8smd` | **128** | rebase: integrate oro-05e8 into epic branch |
| `oro-ryt7` | **123** | applyScaleDirective does not clamp targetWorkers to MaxWorkers |
| `oro-iedk` | **111** | Wire ensureDoltMetadata into oro init bootstrapProject |

"Consolidate 4 call sites" is a 20-minute refactor. It took **217 full agent runs**. "Clamp an integer to a maximum" took **123**. These are not tasks the orchestration made faster; these are tasks the orchestration could not converge on. The retry/review/QG loop has no damping term — nothing detects "this task has been attempted 50 times, the acceptance criteria or the gate is the problem, stop and ask a human."

### Escalations: 4,195 for 3,919 tasks

| Type | Count | Reading |
|---|---|---|
| `OVERSIZED_BEAD` | 1,104 | **Decomposition stage systematically fails.** Largest single category. |
| `MERGE_COMPLETE` | 1,063 | A *success notification* stored in the escalation table — inflates the count and pollutes the operator's queue |
| `NON_TDD_AC` | 579 | Acceptance criteria written without a runnable test |
| `STUCK` | 476 | Liveness |
| `MERGE_CONFLICT` | 314 | Integration |
| `MISSING_AC` | 254 | Task-craft quality |
| `STUCK_WORKER` | 239 | Liveness |
| `WORKER_CRASH` | 158 | Liveness |

Excluding the mis-filed `MERGE_COMPLETE`: **3,132 genuine escalations for 3,919 tasks = 0.80 per task.** For a system whose thesis is autonomy, ~4 of every 5 tasks generate something demanding human or ops attention. `NON_TDD_AC` + `MISSING_AC` = 833 failures caused by *the system's own upstream task-crafting stage*.

### The event log

| | |
|---|---|
| Total events | 353,151 |
| `recovery_quarantined_bead_skipped` | **316,517 (89.6%)** |
| Distinct beads generating that spam | **67**, over 7 days |

That is ~4,724 log writes per quarantined bead per week — a hot loop re-logging a skip decision on every scheduler pass. It is the direct cause of the 426 MB `state.db`. **90% of the durable "observability" investment is one unthrottled log line.**

### Integration machinery: 17 failures per success

| Event | Count |
|---|---|
| `epic_branch_prepare_failed` | **2,755** |
| `epic_rebase_child_prepare_diverged` | 383 |
| `epic_deterministic_rebase_conflict` | 146 |
| `epic_preserve_verify_failed` | 139 |
| `epic_preserve_rolled_back` | 139 |
| **Total epic-integration failures** | **3,562** |
| `merged` | **205** |
| `review_approved` / `review_rejected` | 265 / 154 |
| `quality_gate_rejected` | 436 |

**585 commits (12.7% of all history) touch rebase/merge/worktree/epic/ancestry.** And the machinery corrupts history: 31 commits on `main` share the subject `fix(storage): rebuild incompatible catalog schemas`, all with **identical author timestamps** and only **2 distinct patch-ids** — the same change replayed onto main 31 times by the ancestry-preservation logic. Across all history, **599 commits share a duplicated subject** (205 distinct duplicated subjects).

This subsystem is a net negative. It fails more than it succeeds, it pollutes the artifact it is meant to protect, and it requires a 15-entry memorized decision tree to adjudicate.

### The trend

| Month | fix | feat | ratio |
|---|---|---|---|
| 2026-02 | 405 | 451 | **0.90** |
| 2026-03 | 190 | 130 | **1.46** |
| 2026-04 | 229 | 120 | **1.91** |
| 2026-05 | 413 | 194 | **2.13** |
| 2026-07 | 404 | 171 | **2.36** |

Monotonic. Overall 1,589 fix vs 1,125 feat. `fix(dispatcher)` alone is 468 commits — 29% of all fixes in one package. The system now spends more than twice as much output repairing itself as extending itself.

---

## 6. Subsystem-by-subsystem

| Subsystem | Purpose | Necessity | Complexity cost | Evidence | Recommendation |
|---|---|---|---|---|---|
| **Dispatcher daemon** | Assign ready tasks to idle workers, drive lifecycle | **Essential now** | `dispatcher.go` 11,345 LOC / 444 funcs / 224-field struct; 14 loops; 1,057 commits; 468 `fix(dispatcher)` | Core loop demonstrably works (61% one-pass) | **Keep the role, split the file.** God-object is the single biggest maintenance liability. Extract assign, review, QG-retry, epic-integration, recovery into packages behind interfaces |
| **Worker lifecycle + UDS protocol** | Spawn agents, heartbeat, ASSIGN/DONE | **Essential now** | `pkg/protocol` 2,754 LOC; `worker_pool.go` 919 | 5,840 assignments executed; heartbeat catches crashes (158 `WORKER_CRASH`) | **Keep as-is.** Best-engineered part of the system |
| **Git worktree isolation** | Concurrent agents can't collide | **Essential now** | `worktree_manager.go` 763 LOC | No cross-worker corruption observed | **Keep** |
| **SQLite task state + event log** | Durability, resume | **Essential now** | 36 tables in `protocol/schema.go` (1,521 LOC) | Survives restarts; 245 handoffs recovered | **Keep, but prune.** 61 tables → target ~20 |
| **Quality gate script** | Mechanical correctness bar | **Essential now — but broken** | 1,677 LOC bash; 45 `fix(qg)` + 33 `fix(quality-gate)` commits | **Never runs `./cmd/...` (line 1290)**, so 39,297 test LOC are outside the merge gate. CI does run them (`ci.yml:73`) and **CI is red on 4 of the last 5 `main` runs** — nothing in the merge path consults it | **Fix immediately** (add `./cmd/...`, copying `ci.yml:75-77`'s coverage exclusion), and make merge consult CI status |
| **Ops review agent** | Independent judgment pass | **Essential now** | `pkg/ops` 3,401 LOC | 265 approvals / 154 rejections | **Keep.** Cap review cycles is right; add a global attempt ceiling |
| **Epic branch / rebase / ancestry preservation** | Keep epic branches integrable | **Wrong abstraction** | 585 commits (12.7%); 3,562 failure events vs 205 merges; 31 duplicated commits on main; 15-rule operator decision tree | Fails 17:1 against success | **Delete.** Replace with: task branches off `main`, serialized fast-forward merge, and on conflict → escalate to human. Epics become metadata, not git objects |
| **Recovery quarantines** | Isolate unsafe work | **Useful but over-scaled** | `recovery_quarantine.go` 495 LOC; 8 reason codes; 395 quarantines | Generates 316,517 spam events (89.6% of log); `requeue-preserved` mode feeds rejection loops (operator's own note) | **Simplify to 2 states** (retryable / human-owned). Fix the log spam. Delete `requeue-preserved` |
| **Escalation system** | Route judgment calls | **Useful, miscalibrated** | 14 types | 4,195 for 3,919 tasks; `MERGE_COMPLETE` (1,063) is a success notice | **Split notifications from escalations.** Collapse 14 types → ~5 |
| **`pkg/storage` catalog** | Namespaces/providers/leases control plane | **Premature — and fail-closed on the boot path** | 2,273 LOC + 1,605 test LOC; 13 tables; 69 `fix(storage)` commits incl. 62 duplicated; **41 `//oro:testonly` suppressions** | Every one of 13 tables has 0 rows. `storage.NewController` exists **only in tests**, so all 4 admission gates and `storageControllerLoop` are no-ops. But `cmd_start.go:1123` opens the catalog, migrates it, **closes it, discards the handle** — and a failure **aborts `oro start`** | **Delete.** Priority is *"remove a fail-closed no-op from the boot path,"* not "remove dead weight" |
| **`pkg/remotegate`** | CI-gated remote PR flow | **Premature** | 1,456 LOC + 1,869 test; 5 most-recent commits | `remote_gates` table: **0 rows**. Its new validation is what **broke the test suite** | **Finish or revert.** Currently pure liability. If it lands the *deployment* gap, it is the most valuable unfinished thing here |
| **`pkg/cards` (knowledge cards)** | Durable learnings → future workers | **Useful but optional** | 2,862 LOC | 4,252 cards, but **1,123 pending learnings never promoted**; `card_relations`: 0 rows | **Keep core, delete relations/contradiction machinery.** Promotion backlog says the loop doesn't close |
| **`pkg/codesearch`** | Semantic retrieval into prompts | **Useful but optional** | 2,628 LOC; **462 MB orphaned WAL**; `memory_search_events`: 0 rows | Wired into `AssignPayload.CodeSearchContext` (`dispatcher.go:7485`) | **Keep, fix WAL checkpointing.** Modern agents do their own retrieval — re-evaluate against a no-codesearch A/B |
| **`pkg/dashboard` (TUI)** | Operator visibility | **Duplicative** | 4,056 LOC — and **explicitly stripped from coverage** (`quality_gate.sh:1297`) | 6 commits total; excluded from the quality bar it enforces on others | **Merge into one surface** with web |
| **`pkg/web` (HTTP/SSE)** | Operator visibility | **Duplicative** | 783 LOC, 10 GET routes, **read-only** | Third monitoring surface | **Pick one.** Web is the cheapest; delete the TUI |
| **tmux integration** | Pane monitoring/control | **Duplicative** | `cmd/oro/tmux.go` 988 LOC + `pane_monitor.go` 209 + `pane_restarter.go` 106; 24 `fix(tmux)` commits | Fourth surface; workers are subprocesses, not interactive TTYs | **Delete.** Keep `oro logs`/`oro status` |
| **CLI (~120 verbs)** | Control surface | **Over-scaled** | `cmd/oro` 24,398 LOC; 942 commits; **untested by QG** | Verbs incl. `doctrine`, `dream`, `dogfood`, `future-mutation`, `parade` | **Cut to ~25 verbs.** Move the rest behind `oro debug` |
| **Janitor + audit agents** | Background hygiene | **Premature** | `janitor.go` 772 + `audit.go` 401 + `janitor_cycle.go` 337 + `janitor_filing.go` 284 | `ops_runs` records only `decompose` (214) and `escalation` (26) — **no janitor/audit runs** | **Delete or prove.** No live evidence of execution |
| **`pkg/workproposal`** | Proposal intake | **Premature** | 247 LOC + 156 test; 3 tables | **6 rows** | Defer |
| **`pkg/leakscan`, `codestruct`, `langprofile`, `modelartifacts`, `edit`** | Assorted | **Useful but optional** | ~3,200 LOC combined; `modelartifacts` has 1 commit | Thin usage | Consolidate or defer |
| **Runtime adapters (Claude/Codex), tiers, roles** | Model routing | **Essential now** | `pkg/agentruntime` 357 LOC; 17 roles in `.oro/config.yaml` | Actively used; config-driven | **Keep — this is the model to copy.** Policy in YAML, not Go |
| **27 agent hooks** | Enforce discipline in-agent | **Useful but over-scaled** | 640 KB; 50 `fix(hooks)` commits | Real value (worktree write guard); but a Codex/Claude parity bug class | **Cut to ~8** |
| **Setup/init/QG generation** | Onboard a new repo | **Useful but over-scaled** | `cmd_init.go` 1,299 + `quality_gate_gen.go` 1,648 + `cmd_setup.go` 340 | Generating a 1,677-line bash script per project is fragile | **Ship 3 templates**, let users edit |
| **Mandatory brainstorm→premortem→spec→plan→taskcraft→observe→TDD stages** | Front-loaded quality | **Correctly placed** | In skills/prompts, **not** in Go — README:187 | Right call architecturally | **Keep.** But 1,104 `OVERSIZED_BEAD` + 833 AC failures say the *content* needs work, not the placement |

---

## 7. The five most valuable architectural choices

1. **Git worktrees as the isolation primitive** (`worktree_manager.go`). Cheap, native, debuggable by hand, and it makes concurrent agents genuinely safe. Zero cross-worker corruption in 5,840 assignments. This is the single best decision in the system.
2. **SQLite as durable state with an event log.** Survives crashes, restarts, and context exhaustion; 245 handoffs recovered work that would otherwise be lost. Single-file, inspectable with `sqlite3`, no server.
3. **Workflow policy in skills/prompts, not in the engine** (README:187). The brainstorm→premortem→spec→plan chain lives in markdown skills. This is exactly right and is the thing most orchestration projects get wrong. The engine enforces only two mechanical gates (QG pass, review approval).
4. **Config-driven model routing** (`.oro/config.yaml`, 17 roles × tiers × runtimes). Swapping a role from Codex to Claude Opus is a YAML edit. Clean separation of policy from mechanism — the pattern the rest of the system should follow.
5. **The quality gate as an external script, not Go code.** `scripts/quality_gate.sh` is per-project, editable, runnable by a human outside the daemon. Correct boundary (the bug is its *scope*, not its form).

---

## 8. The five clearest examples of unnecessary or premature complexity

1. **Epic branch ancestry-preservation machinery.** 585 commits, 3,562 failure events against 205 merges, 31 duplicate commits polluting `main`, and a 15-rule memorized operator decision tree covering cases like "`-s ours` merge is APPROVE unless followed by a rebase-onto-epic-tip." Nothing in the demonstrated workload (10 local agents, one repo) requires epic branches at all.
2. **`pkg/storage` distributed control plane.** 2,273 + 1,605 LOC, 13 tables (`namespaces`, `providers`, `controllers`, `leases`, `runtime_pause_epochs`, `runtime_tombstones`, `runtime_reconciliation_cursors`), a dispatcher goroutine, 69 `fix(storage)` commits — and **every table has zero rows in production**. Built for multi-tenant scale that does not exist: one operator, one host, so `namespace` has exactly one value.

    Worse than unused, it is *fail-closed*. The controller is never constructed outside tests, so every admission gate short-circuits to `true` and `storageControllerLoop` exits on its first line. The subsystem's entire production behavior is `cmd_start.go:1123` — open a database, create 13 tables, close it, throw the handle away — and if that migration errors, **`oro start` does not boot**. A test-only-annotated function is a hard dependency of starting the factory, in exchange for nothing. (`dispatcher.exit.log` shows no storage failures to date, so the risk is latent, not realized.)
3. **Four overlapping monitoring surfaces.** TUI dashboard (4,056 LOC, itself excluded from coverage enforcement), HTTP/SSE server (783 LOC), tmux panes (1,303 LOC), and ~15 CLI status verbs. All read-only; all showing the same state. One operator needs one.
4. **`recovery_quarantined_bead_skipped` at 316,517 events.** 89.6% of the entire durable event log, generated by 67 beads over 7 days, driving `state.db` to 426 MB. Extensive observability machinery whose dominant output is noise — and which still cannot answer the basic question "what is our lead time?" (`assignments.completed_at` gives an average of 6,711 minutes and a max of 79,322 minutes for tasks that are minutes of work; the column is written at cleanup, not completion).
5. **~120 CLI verbs across 24,398 lines that the quality gate never tests.** Including `oro doctrine`, `oro dream`, `oro future-mutation`, `oro dogfood`, `oro parade`. The 39,297 lines of tests written for this code have never run in a merge gate.

---

## 9. Simplified target architecture

### The baseline that preserves ~80% of the value

```
┌──────────────────────────────────────────────────────────┐
│  oro CLI  (~25 verbs)                                    │
│  task / start / stop / status / logs / review / recover  │
└───────────────────────┬──────────────────────────────────┘
                        │  UDS
┌───────────────────────▼──────────────────────────────────┐
│  Supervisor  (~6-8k LOC, 5 packages)                     │
│  ready→assign→execute→gate→review→merge                  │
│  heartbeat · retry (bounded) · escalate                  │
└──┬──────────┬──────────┬──────────┬──────────────────────┘
   │          │          │          │
   ▼          ▼          ▼          ▼
 agent      agent      agent      agent      (claude -p / codex)
   │          │          │          │
   ▼          ▼          ▼          ▼
 worktree   worktree   worktree   worktree   (branch off main)
                        │
                        ▼
        quality_gate.sh  →  ops review  →  serialized FF merge
                        │
                        ▼
              ┌──────────────────────┐
              │  DELIVERY (new)      │
              │  deploy · e2e ·      │
              │  rollback · observe  │
              └──────────────────────┘

State: ONE SQLite db, ~20 tables
Surfaces: ONE read-only web page (SSE)
```

### Delete

| Target | LOC saved (src+test) | Justification |
|---|---|---|
| Epic branch/rebase/ancestry machinery | ~4,000 | 17:1 failure ratio; replaced by branch-off-main + escalate-on-conflict |
| `pkg/storage` catalog | ~3,900 | 0 rows in production |
| `pkg/dashboard` TUI | ~9,000 | Duplicative; excluded from own coverage bar |
| tmux integration | ~1,500 | Duplicative; workers aren't TTYs |
| Janitor + audit agents | ~2,000 | No `ops_runs` evidence of execution |
| `pkg/workproposal` | ~400 | 6 rows |
| ~95 CLI verbs | ~12,000 | Untested; unused |
| Card relations / contradiction handling | ~800 | 0 rows |
| **Total** | **~33,600** | ~13% of all Go LOC, ~40% of non-test dispatcher-adjacent surface |

### Merge

- **Four monitoring surfaces → one.** Keep `pkg/web` (783 LOC, SSE, cheapest). Delete TUI and tmux.
- **Six schema owners → one.** `pkg/protocol/schema.go` owns all state; delete `storage/catalog.go`, fold `cards/schema.go` and `dispatcher/store.go` in.
- **14 escalation types → 5** (`NEEDS_HUMAN`, `STUCK`, `CONFLICT`, `BAD_TASK`, `CRASH`). Move `MERGE_COMPLETE`/`EPIC_COMPLETE` out of escalations into a notifications view.
- **8 quarantine reasons → 2** (`retryable`, `human-owned`).
- **`dispatcher.go` 11,345 lines → 5 packages** (`assign`, `gate`, `review`, `integrate`, `recover`), each behind an interface, none over ~2,000 lines.

### Move into configuration

- `maxReviewRejections = 2` (`dispatcher.go:4916`) → `.oro/config.yaml`
- QG lane definitions, timeouts, coverage threshold → YAML, not 1,677 lines of bash control flow
- The ~28 hardcoded `time.Duration` constants in `pkg/dispatcher` → config with defaults
- Quality-gate *generation* (1,648 LOC) → 3 checked-in templates

### Defer

- `pkg/remotegate` — finish it as the **delivery** layer or revert it. Do not leave it half-landed breaking tests.
- `pkg/codesearch` — keep, but run an A/B: does injecting `CodeSearchContext` measurably raise first-pass success? Modern agents retrieve on their own.
- Multi-repo / multi-tenant anything — until there is a second real repository.

### Build (the underengineered half)

This is where the ~33,600 deleted lines of capacity should go:

1. **`oro deploy`** — a configurable per-project deploy command with a recorded artifact identity.
2. **Product-level acceptance** — an e2e lane distinct from unit QG, run against a deployed instance, not a worktree.
3. **`oro rollback`** — revert-and-redeploy on failed post-merge signal.
4. **Post-merge observation** — a health probe window after deploy; auto-file a task on regression.
5. **A real lead-time metric** — `completed_at` written at completion, not cleanup.

---

## 10. What would break or materially worsen under the simplification

I am not claiming the deletions are free. Concretely:

| Removal | What genuinely gets worse |
|---|---|
| Epic branches | **Large multi-task features lose a staging area.** Today an epic can integrate 10 child tasks before touching `main`. Without it, 10 tasks land individually and `main` sees more intermediate states. Mitigation: dependency-ordered serialized merge to `main` gives the same net result with far less machinery — but there *will* be a window where `main` holds partially-complete features. This is the most substantive loss and should be piloted before committing. |
| `pkg/storage` catalog | Nothing today (0 rows). But if Oro ever runs multi-tenant, this gets rebuilt. That is the correct trade: rebuild it when a second tenant exists. |
| TUI dashboard | Operators lose a dense terminal view. The web page is a real substitute, but the ergonomics of `oro-dash` in a tmux pane are genuinely nicer for live watching. Real, modest loss. |
| tmux integration | Loses the ability to attach to and steer a live agent pane. If interactive intervention on a running worker matters, this is a real capability. Evidence suggests it is rarely used, but I did not measure it directly — **verify before deleting.** |
| Janitor/audit agents | If they run outside `ops_runs` tracking, I would be deleting working code. **Verify execution before deleting** — my evidence is absence-of-record, which is weaker than absence-of-function. |
| 95 CLI verbs | Some are load-bearing for debugging (`oro recovery`, `oro doctor`, `oro storage`). Move behind `oro debug <verb>` rather than deleting outright. |
| 14 → 5 escalation types | Loses diagnostic granularity in the event log. Mitigate by keeping the detail in the escalation payload rather than the type enum. |
| Card relations | Zero rows today, but the contradiction-handling design may be the right long-term answer to stale knowledge. Deleting is reversible; the design doc should survive. |

**The honest risk:** this plan removes ~33,600 lines from a system whose 61% first-pass success rate is real. If any of that machinery is quietly load-bearing for that 61%, the simplification would *reduce* reliability. That is why the plan below is ordered by evidence strength, gated on metrics, and starts with fixes rather than deletions.

---

## 11. Prioritized simplification plan

Ordered so that **every step either fixes a demonstrated defect or removes something proven unused.** No aesthetic rewrites. Nothing in P0–P1 risks the working core.

### P0 — Stop the bleeding (days, near-zero risk)

| # | Action | Evidence | Effort |
|---|---|---|---|
| 1 | **Add `./cmd/...` to `quality_gate.sh:1290`.** Fix the 3 failing `remote_capabilities_test.go` fixtures. | Test suite red on main; 39,297 test LOC never executed | 1 hour |
| 2 | **Throttle `recovery_quarantined_bead_skipped`** — log once per bead per state change, not per scheduler pass. `VACUUM` `state.db`. | 316,517 events = 89.6% of log; 426 MB DB | 2 hours |
| 3 | **Add a global per-task attempt ceiling** (~10). On exceed: stop, mark `NEEDS_HUMAN`, do not requeue. | 16 tasks burned 2,068 assignments; 217 runs on a 4-call-site refactor | 4 hours |
| 4 | **Checkpoint `code_index.db` WAL** on close. | 462 MB orphaned WAL | 1 hour |
| 5 | **Move `MERGE_COMPLETE`/`EPIC_COMPLETE` out of `escalations`** into a notifications table. | 1,063 of 4,195 "escalations" are success notices | 3 hours |
| 6 | **Fix `assignments.completed_at`** to be written at completion. | Avg 6,711 min / max 79,322 min is unusable | 2 hours |

**After P0, re-measure.** Items 1–3 alone may change the picture materially. Do not proceed to P2 until a full week of clean metrics exists.

### P1 — Remove the proven-unused (1–2 weeks, low risk)

| # | Action | Gate before acting |
|---|---|---|
| 7 | **Remove the `openStorageCatalog` call from `cmd_start.go:1123` first** — it is a fail-closed no-op on the boot path. Then delete `pkg/storage` (13 tables, 0 rows) | Already confirmed: `NewController` is test-only; nothing writes the catalog. Do the boot-path removal even if the delete slips |
| 7b | **Audit all `//oro:testonly` annotations for stale claims.** Fail the gate when an annotated symbol acquires a production caller | `OpenCatalog` is annotated test-only and called from the boot path. 41 suppressions live in `pkg/storage` alone |
| 8 | Delete `pkg/workproposal` (6 rows) | — |
| 9 | Delete card relations / contradiction machinery (0 rows) | Keep the design doc |
| 10 | **Verify** janitor/audit actually execute; delete if not | Absence-of-record is weak evidence — check logs first |
| 11 | **Verify** tmux attach is used; delete if not | Ask the operator directly |
| 12 | Move `maxReviewRejections` and the ~28 duration constants to config | — |
| 13 | Resolve or revert `pkg/remotegate` | It broke main; decide its fate |

### P2 — Fix the worst subsystem (3–4 weeks, moderate risk — pilot first)

| # | Action | Notes |
|---|---|---|
| 14 | **Pilot: branch tasks off `main` directly**, serialized FF merge, escalate on conflict. Run alongside epics for 2 weeks. | This is the big one — 3,562 failure events |
| 15 | If the pilot holds, delete the epic rebase/ancestry machinery (~4,000 LOC) and the 15-rule operator decision tree with it | Measure merge success rate before/after |
| 16 | Collapse quarantine reasons 8→2; delete `requeue-preserved` | Operator's own note: it feeds rejection loops |
| 17 | Consolidate to one monitoring surface (`pkg/web`) | Delete TUI + tmux monitoring |

### P3 — Address the real gap (ongoing)

| # | Action |
|---|---|
| 18 | **Fix decomposition.** 1,104 `OVERSIZED_BEAD` + 833 AC-quality escalations are the largest failure class and they originate in Oro's own task-crafting stage. Add a size/AC pre-check *before* a task enters the ready queue. |
| 19 | Split `dispatcher.go` into 5 packages behind interfaces |
| 20 | **Build the delivery stage**: `oro deploy`, e2e lane, `oro rollback`, post-merge health probe |
| 21 | Cut CLI to ~25 verbs; rest behind `oro debug` |

---

## 12. Metrics Oro should track to prove its complexity pays

None of these exist today in usable form — which is itself notable for a system with 116 event types and 4 monitoring surfaces. Each maps to a query over existing tables.

| Metric | Definition | Current value | Target |
|---|---|---|---|
| **Autonomous completion rate** | tasks closed with 1 assignment, 0 escalations / all closed | **~61%** (1,003/1,655) | >80% |
| **Human interventions per task** | genuine escalations / tasks | **0.80** | <0.15 |
| **Assignment amplification** | total assignments / tasks executed | **3.5×** | <1.5× |
| **Tail concentration** | % of assignments in worst 1% of tasks | **35%** | <10% |
| **Cost per accepted change** | agent-minutes (or tokens) per merged commit | *unmeasurable today* | instrument first |
| **Lead time** | ready → merged, p50/p90 | *unmeasurable* (`completed_at` broken) | p50 <1h |
| **Review rejection rate** | `review_rejected` / (approved + rejected) | **37%** (154/419) | <20% |
| **QG rejection rate** | `quality_gate_rejected` / assignments | **7.5%** (436/5,840) | <5% |
| **Integration success ratio** | `merged` / epic-integration failure events | **1:17** | >5:1 |
| **Regression rate** | commits on main that break the suite | **currently 1 open** | 0 |
| **Recovery rate** | quarantines auto-resolved without human | measurable from `recovery_quarantines` | >90% |
| **Net throughput** | merged commits per agent-hour, 1 worker vs 4 vs 10 | **never measured** | must exceed 1× |

**The last one is the whole thesis.** Oro's core claim is that a swarm beats a single agent. `scripts/proof_swarm_throughput.sh` exists but no result is recorded anywhere in the repo or the state DB. With 3.5× assignment amplification, 37% review rejection, 314 merge conflicts, and 3,562 integration failures, it is entirely possible that net throughput at 10 workers is *below* net throughput at 2. **Until that number exists, the central justification for most of this architecture is unmeasured.** Measuring it is a day of work and it should be the next thing done.

---

## 13. Closing

The parts of Oro that touch reality — worktrees, SQLite, UDS, heartbeats, an external quality-gate script, one independent review pass, policy-in-prompts — are well chosen and should survive any simplification. That core is roughly 15,000 lines and it delivers a genuine 61% first-pass autonomous completion rate.

The parts built for a scale that never arrived — a distributed storage control plane with zero rows, epic ancestry preservation failing 17:1, four monitoring surfaces, 120 CLI verbs, a 90%-noise event log — are now consuming more engineering output than they return, and the `fix:feat` trend line (0.90 → 2.36 over five months) says that gap is widening, not closing.

And the stage that would make "software factory" literally true — deploy, verify in a real environment, observe, roll back, iterate on feedback — does not exist in any form. The README is precise about this and should be believed: the pipeline ends at **"Code on main."**

The most valuable next actions are also the cheapest: add seven characters to line 1290 of `quality_gate.sh`, cap task attempts at 10, and run the swarm throughput proof. Those three will tell you more about whether this architecture is earning its keep than any amount of further design.
