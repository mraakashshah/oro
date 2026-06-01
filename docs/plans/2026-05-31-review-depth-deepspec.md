# Spec 07 (re-grounded) — Review depth: regression-revert, structured findings, multi-persona fan-out, cheap-then-deep gate, persistent triage

**Status:** Design doc (deepspec). Re-grounded against `main` on 2026-05-31. None of Spec 07 is implemented yet.
**Source:** `archive/yap/reference/_deepdive/specs/07-review-depth.md` (anchors re-verified below).
**Author note:** This rewrite corrects the archived spec's drifted `file:line` anchors. See **Drift corrections** immediately below before trusting any line number.

---

## Drift corrections (read this first)

The archived spec's anchors were verified against current `main`. The most important corrections:

| Symbol | Archived spec said | Actual (verified 2026-05-31) | How verified |
|---|---|---|---|
| `Spawner.Review` | `ops.go:267-281` | **`ops.go:278-292`** | full read of ops.go |
| docs-only short-circuit | `ops.go:268-277` | **`ops.go:279-288`** | full read |
| `buildReviewPrompt` call | `ops.go:279` | **`ops.go:290`** | full read |
| `s.run(...)` review spawn | `ops.go:280` | **`ops.go:291`** | full read |
| `Spawner.run` | `ops.go:397-446` | **`ops.go:408-461`** | full read |
| role resolve inside run | `ops.go:409` | **`ops.go:420`** (`agentmodel.ResolveForRole(opsType.Role())`) | full read |
| `spawnOps` | `ops.go:448-468` | **`ops.go:463-483`** | full read |
| `parseResult` | `ops.go:441, 571-596` | **`parseResult` `ops.go:632-657`** (review case `ops.go:643-644`) | full read |
| `parseReviewOutput` | `ops.go:642-658` | **`ops.go:703-720`** | full read |
| `Result` struct | `ops.go:151-157` | **`ops.go:154-160`** | full read |
| `ReviewOpts` struct | `ops.go:174-184` | **`ops.go:177-187`** | full read |
| `Verdict` enum | `ops.go:143-148` | **`ops.go:143-151`** | full read |
| `OpsReview` role string | `ops.go:101-103` | **`Type.Role()` `ops.go:103-124`; `OpsReview`→`"ops_review"` at `ops.go:105-106`** | full read |
| `OpsReview.Tier()` = TierDeep | `ops.go:73-74` | **`Type.Tier()` `ops.go:71-88`; review→`TierDeep` at `ops.go:75-76`** | full read |
| `BatchSpawner` / reasoning / runtime ifaces | `ops.go:34-48` | **`BatchSpawner` `ops.go:38-40`, `ReasoningBatchSpawner` `ops.go:44-46`, `RuntimeBatchSpawner` `ops.go:49-51`** | full read |
| `CancelForBead` | `ops.go:352-369` | **`ops.go:363-380`** | full read |
| `s.active` tracking | `ops.go:430-432` | **`ops.go:445-447`** (inside `run`) | full read |
| `handleQGFailure` | `dispatcher.go:1915` | **`dispatcher.go:1966`** | grep verified |
| `reserveQGRetryAttempt` | `dispatcher.go:1984` | **`dispatcher.go:2035`** | grep verified |
| `qgRetryWithReservation` | `dispatcher.go:2057` | **`dispatcher.go:2108`** | grep verified |
| `checkPreMergeQG` | `dispatcher.go:2316` | **`dispatcher.go:2367`** (callsite `:2637`) | grep verified |
| `handleQGExhausted` | `dispatcher.go:9150` | **`dispatcher.go:9434`** | grep verified |
| `QGRunner.Run` | `dispatcher.go:405-450` | **`QGRunner` iface `:433`; `ShellQGRunner.Run(ctx, worktree, skipMutation bool)` `:445`** | grep verified |
| `maxReviewRejections` | `dispatcher.go:3554` | **`dispatcher.go:3750` (=2)** | grep verified |
| `maxQGRetries` | (not cited) | **`dispatcher.go:3237` (=3)** | grep verified |
| `handleReviewResult` | `dispatcher.go:3573` | **`dispatcher.go:3769`** | grep verified |
| `handleReviewApproved` | `dispatcher.go:3594` | **`dispatcher.go:3790`** | grep verified |
| `handleReviewRejection` | `dispatcher.go:3760` | **`dispatcher.go:3956`** | grep verified |
| `ExtractPatterns` consumed | `dispatcher.go:3607,3611-3619` | **`dispatcher.go:3808`** | grep verified |
| worktree git plumbing | `dispatcher.go:3450` | **`git status --porcelain -z` via `(&ExecCommandRunner{Dir: worktree})` at `dispatcher.go:3646`** | grep verified |
| `Config.withDefaults` | `dispatcher.go:198` | **`dispatcher.go:696`** | grep verified |
| agentmodel role map | `agentmodel.go:71-85` inline map | **the literal map exists ONLY in `legacyAgentConfig()` `agentmodel.go:63-87` (fallback when no agent block); production resolves through `ResolveForRole`→`resolveRole` `:122`→`resolveTier` `:157` against `config.AgentConfig.Roles`/`.Tiers`. Persona roles must be added to BOTH the config defaults and `legacyAgentConfig`, not just an inline literal.** | full read |

> **All anchors above are verified against `main` on 2026-05-31.** Re-grep before implementing only if `dispatcher.go` has churned since.

---

## 1. Title & goal

Deepen oro's single-pass pre-merge review into a layered pipeline over one shared structured-finding spine. Today review is one prompt → one prose verdict (`pkg/ops/review_prompt.go`, `Spawner.Review` `pkg/ops/ops.go:278-292`), and the QG retry loop reruns a script and trusts a green result with **no** regression check and **no** finding persistence.

Five phases, each shippable independently, cheapest/safest first:

- **P3 (ship first) — Post-fix regression-revert** in the QG retry loop: snapshot HEAD + per-test outcomes before a retry, diff after, `git reset --hard` to revert the retry if a previously-green sibling test went red. Default **ON** (pure safety, no new spawn). Touches `pkg/dispatcher`.
- **P0 (spine) — Structured Finding schema** + content-addressed `FindingID` + prompt-manifest evidence validation + two-layer JSON parse. New files in `pkg/ops`. Prerequisite for P1/P2/P4.
- **P1 — Multi-persona parallel fan-out** via the existing runtime/batch spawner seam, merged/deduped/promoted/gated deterministically in Go; per-persona agentmodel roles for per-reviewer tiering.
- **P2 — Cheap-then-deep triage gate**: a cheap fast-tier pass scopes the deep personas; only survivors hit the deep reviewers.
- **P4 — Content-addressed finding persistence** in the bead journey + merge-preserving triage + `oro review triage` CLI verb.

This composes with **Spec 05** (Codex second-opinion lane): Spec 05 reconciles at the *verdict* level across runtimes; this spec reconciles at the *finding* level across personas. They share the schema (P0) and the spawn seam; §5.5 specifies the merge so they do not conflict.

---

## 2. Problem & motivation

oro is a software factory. Its expensive failure is a **false negative** — a real bug merged to `main`. Its cheap failure is a false positive — one extra worker re-spin. The current review path under-defends the expensive case in four ways:

1. **One model, correlated blind spots.** `Spawner.Review` (`ops.go:278-292`) builds exactly one prompt (`buildReviewPrompt(opts)` at `ops.go:290`) and runs exactly one spawn (`s.run(...)` at `ops.go:291` → resolves one `(runtime,model,reasoning)` at `ops.go:420` → one `spawnOps` at `ops.go:425`). A single reviewer cannot triangulate. Donor: compound-engineering — N focused personas merged deterministically.

2. **No cost discipline on a deep pass.** `Type.Tier()` returns `TierDeep` for `OpsReview` (`ops.go:75-76`) → Opus on every review over the whole diff. Donor: adamsreview — cheap Sonnet triage, then Opus only on survivors.

3. **No post-fix regression check — the crown-jewel gap.** The QG retry loop reruns `quality_gate.sh` (via `QGRunner.Run`) and trusts a green result. A retry that makes the AC pass while silently breaking a sibling test is counted as progress. The review prompt *describes* the standard ("a real regression in any existing task-relevant test is a Critical finding → REJECTED", `review_prompt.go` verdict section) but **nothing mechanical enforces it**. Donor: adamsreview Phase 9 — hold the fix uncommitted, review per group, and *revert* regressed groups before commit.

4. **Findings are not durable.** Review output is parsed for one terminal verdict line (`parseReviewOutput` `ops.go:703-720`); findings live only in the prose `Result.Feedback` blob (`Result` `ops.go:154-160`) and the journey trail. Re-review re-derives everything and cannot remember "this was triaged false-positive." Donor: clawpatch — content-address findings so re-runs merge into prior triage.

**Debunked donor claims we will not chase** (carried from the archived spec, still valid): clawpatch "git worktree fix isolation" is a myth — oro's per-worker worktrees are already strictly stronger; clawpatch "streaming" is a myth — strict JSON, parsed whole; compound-engineering "deterministic merge math" is LLM-executed prose — **oro implements the merge as real Go**, removing the caveat.

---

## 3. Current oro state (file:line, VERIFIED 2026-05-31)

All paths under `/Users/as21/codehouse/oro/`. Every anchor below was verified against `main` on 2026-05-31 (`pkg/ops`, `pkg/agentmodel`, `pkg/beadstore` via full-file reads; `pkg/dispatcher/dispatcher.go` via grep).

### 3.1 Review is single-pass, single-runtime, prose-verdict (`pkg/ops/ops.go`)

- `Spawner.Review` (`ops.go:278-292`): docs-only short-circuit auto-approves (`ops.go:279-288`, calls `isDocsOnlyDiff` `ops.go:580-615`); otherwise `prompt := buildReviewPrompt(opts)` (`ops.go:290`) then `return s.run(ctx, OpsReview, opts.BeadID, opts.Worktree, prompt)` (`ops.go:291`) — **one** spawn.
- `Spawner.run` (`ops.go:408-461`): launches a goroutine, resolves **one** `(runtime, model, reasoning)` via `agentmodel.ResolveForRole(opsType.Role())` (`ops.go:420`), uses `s.reviewSpawner` when set for `OpsReview` (`ops.go:421-424`), spawns once via `spawnOps` (`ops.go:425`), waits (`waitForProcess` `ops.go:488-540`), parses one result (`parseResult` `ops.go:456`). Returns a **buffered** `<-chan Result` (`ch := make(chan Result, 1)` at `ops.go:409`) — so fan-out is non-blocking and channel-safe.
- `spawnOps` (`ops.go:463-483`): prefers `RuntimeBatchSpawner.SpawnRuntime` (`ops.go:464-470`), then `ReasoningBatchSpawner.SpawnWithReasoning` (`ops.go:471-477`), else plain `BatchSpawner.Spawn` (`ops.go:478-482`).
- Spawner interfaces: `BatchSpawner` (`ops.go:38-40`), `ReasoningBatchSpawner` (`ops.go:44-46`), `RuntimeBatchSpawner` (`ops.go:49-51`).
- `Type.Tier()` (`ops.go:71-88`): `OpsReview` → `protocol.TierDeep` (`ops.go:75-76`).
- `Type.Role()` (`ops.go:103-124`): `OpsReview` → `"ops_review"` (`ops.go:105-106`); default → `"worker"` (`ops.go:121-122`).
- `Type.Timeout()` (`ops.go:129-140`): `OpsReview` → 35 min (`ops.go:131-132`).
- `parseReviewOutput` (`ops.go:703-720`): requires the final non-empty line (after `reviewOutputText` strips stream-JSON envelopes, `ops.go:722-753`) to be exactly `VERDICT: APPROVED` / `VERDICT: REJECTED`; else `VerdictFailed`. **No JSON, no per-finding structure, no evidence schema.**
- `parseResult` (`ops.go:632-657`): for `OpsReview` calls `parseReviewOutput` (`ops.go:643-644`).
- `Result` (`ops.go:154-160`): `{Type, BeadID, Verdict, Feedback, Err}` — single verdict, single prose blob.
- `Verdict` enum (`ops.go:143-151`): `VerdictApproved|VerdictRejected|VerdictResolved|VerdictFailed`.
- `ReviewOpts` (`ops.go:177-187`): `{BeadID, BeadTitle, Worktree, AcceptanceCriteria, BaseBranch, ProjectRoot, AgentInstructions, ClaudeMD, ReviewPatterns}` — all additive new fields land here.
- `CancelForBead` (`ops.go:363-380`): kills all active agents for a bead; `s.active` registered per agent inside `run` (`ops.go:445-447`). Covers a future fan-out automatically.

### 3.2 Review prompt (`pkg/ops/review_prompt.go`)

- `buildReviewPrompt` assembles one free-form prompt via `writeHeader` / `writeContext` / `writeProjectContext` / `writePhases` / `writeVerdictAndOutput`. The Phase-2 critique lenses (Absence / Adversarial / Design / Architecture / Test-as-spec / Anti-patterns) are listed **inside one prompt**, not dispatched as separate reviewers.
- Verdict rubric in-prompt: any Critical OR any Important → REJECTED. Output format mandates a terminal `VERDICT: APPROVED|REJECTED` line (this is the contract `parseReviewOutput` enforces).
- Pattern side-channel: `ExtractPatterns` scrapes `PATTERN:` lines; consumed on the dispatcher side. **This must keep working** — the structured path is additive.

### 3.3 Spawn infrastructure (`pkg/ops/exec_spawner.go`)

- `RuntimeSpawnerRouter`: constructed via `NewRuntimeSpawnerRouter(claude, codex)`; `SpawnRuntime` switches on `"claude"`/`"codex"`, errors on unknown/unconfigured, and prefers `SpawnWithReasoning` when the underlying spawner implements `ReasoningBatchSpawner`. This is the production implementation of `RuntimeBatchSpawner` that `spawnOps` (`ops.go:464`) routes through.
- `ClaudeOpsSpawner` + `buildClaudeOpsArgs` → `claude -p <prompt> --model <model>`; `ExecSpawner.SpawnWithReasoning` exists for Codex-style reasoning effort.

### 3.4 Roles / tiers (`pkg/agentmodel/agentmodel.go`)

- `ResolveForRole(role)` (`agentmodel.go:21`) → `(runtime, model, reasoning)`, internally `resolveRole` (`:122`) → `resolveTier` (`:157`), with the tier→runtime/model mapping coming from config (`cfg.Tiers`). **Unknown roles fall through to `cfg.Roles["worker"]`** (`:125`), so adding persona roles that an install hasn't configured degrades gracefully rather than erroring.
- The literal role map (`ops_review` → `TierDeep` at `:74`, `worker_escalation` → `TierDeep` at `:73`, etc.) lives ONLY in `legacyAgentConfig()` (`:63-87`) — the fallback used when there is no agent block / config load fails. When an agent block IS present, roles come from `config.AgentConfig.Roles` merged with `config.DefaultAgentConfig()` via `withDefaults` (`:89`).
- **NEW REALITY vs archived spec:** there is no single editable literal at `agentmodel.go:74` that governs production. Per-persona roles (§5.2/§5.3) must be registered in BOTH `config.DefaultAgentConfig()` (so configured installs pick them up) AND `legacyAgentConfig()` (so no-config installs do), the same way the existing `ops_*` roles appear in both.

### 3.5 QG retry loop has NO baseline, NO test-diff, NO revert (`pkg/dispatcher/`)

- `handleQGFailure` (`dispatcher.go:1966`): `touchProgress` → `evaluateQGFailure`/`logQGFailureRejection` → stuck check (`isQGStuck`→`handleQGStuckDetected` `:1973-1976`) → **transient branch** when `classification.Decision == QGFailureDecisionBackoffRetry` (`handleTransientQGFailure` `:1981`, in `qg_transient.go`) → `reserveQGRetryAttempt` (`:1985`/def `:2035`) → if exhausted, `handleQGExhausted` (`:1987`) → else `qgRetryWithReservation` (`:1993`/def `:2108`).
- `reserveQGRetryAttempt` (`dispatcher.go:2035`): runs entirely **under `d.mu.Lock()`** — it only increments `d.attemptCounts[beadID]` (`:2039`), caps at `maxQGRetries` (`:2043`), and reserves the worker. **It does NO I/O.** So the P3 baseline capture (which runs git + tests) must go in `qgRetryWithReservation`'s I/O phase, NOT here (see §5.1 correction).
- `qgRetryWithReservation` (`dispatcher.go:2108`): captures an Opus-escalation snapshot under lock (`:2112`), then in the `withReservation` **I/O phase** (runs outside the lock) builds `payload = d.buildAssignPayload(ctx, &snap, attempt, qgOutput, "")` (`:2119`) carrying **raw `qgOutput` as feedback** — no structured "what to try", no pre-fix snapshot. This I/O phase is the correct hook point for the P3 baseline capture.
- `handleQGExhausted` (`dispatcher.go:9434`): on cap, classify and reopen/triage; never reverts a regressing edit.
- `checkPreMergeQG` (`dispatcher.go:2367`, callsite in `mergeAndComplete` at `:2637`): `qgPassed, qgOutput, qgErr := d.qgRunner.Run(ctx, worktree, !d.cfg.MutationTesting)` then trusts pass/fail (`:2368-2375`). `QGRunner` is an interface (`:433`); `ShellQGRunner.Run(ctx, worktree, skipMutation bool) (passed bool, output string, err error)` (`:445`) is the production impl.
- **NEW REALITY — transient-QG handling:** `pkg/dispatcher/qg_transient.go` now exists and classifies transient (infra/flake) QG failures separately from real failures via `handleTransientQGFailure`. **P3 must hook the regression check on the real-failure / real-retry path only**, after the transient branch has been excluded, so a transient retry does not trigger a spurious revert. There are also `qg_failure_classifier.go`, `qg_failure_notes.go`, `qg_failure_store.go`, and `qg_stuck.go` — the QG path is more decomposed than the archived spec assumed; P3 plugs into the existing retry seam rather than a monolithic function.
- **Confirmed gap:** no `git reset --hard`, no `git checkout --`, no baseline-SHA capture before retry, and no broader-test-set diff anywhere on the QG path. The regression standard exists only as reviewer prompt text. Worktrees are isolated per worker (`pkg/dispatcher/worktree_manager.go`), so a hard reset there is safe.

### 3.6 Finding persistence: only the journey trail exists (`pkg/beadstore/`)

- `bead_journey` table created in `pkg/beadstore/migrations/migrate_v3.go`: columns `{id, bead_id, ts, actor, event, payload TEXT(JSON)}`.
- `JourneyEvent` (`pkg/beadstore/v3types.go`): `{ID, BeadID, TS, Actor, Event, Payload}`; the `Actor` vocabulary explicitly includes `ops_review`.
- `AppendJourney` and `LatestJourney` live in `pkg/beadstore/v3methods.go`. No `findings` table; review findings are not first-class, content-addressed, or re-runnable with preserved triage.
- **NEW REALITY — migration v4 exists** (`pkg/beadstore/migrations/migrate_v4.go`). If a `bead_learnings_pending` table or similar exists there, the next dedicated-findings migration is **v5** (see §11). For v1, P4 reuses `bead_journey` and adds no migration. (Implementer: confirm the highest applied migration version before numbering any new one.)

### 3.7 Verdict consumption (dispatcher side, unchanged contract)

- `handleReviewResult` (`dispatcher.go:3769`, spawned via `safeGo` at `:3619`) branches on `VerdictApproved` → `handleReviewApproved` (`:3790`) / `VerdictRejected` → `handleReviewRejection` (`:3956`) / default. `handleReviewApproved` extracts patterns via `ops.ExtractPatterns(feedback)` (`:3808`) and fails closed to `handleReviewRejection("Review failed: ...")` (`:3838`) on `result.Err != nil`. `handleReviewRejection` escalates when `count > maxReviewRejections` (`:3964`; `maxReviewRejections = 2` at `:3750`). **All five phases must preserve the `<-chan Result` + `Verdict` enum contract** — the merge always emits exactly one `VerdictApproved`/`VerdictRejected`.

---

## 4. Source techniques (donor attributions preserved)

### P1 — Multi-persona parallel fan-out (donor: compound-engineering-plugin)
- Orchestrator selects a reviewer team and spawns **each persona as its own sub-agent** ("parallel sub-agents, NOT one prompt with many personas"), bounded by an active-subagent limit with overflow queued. Each sub-agent gets persona file + shared diff-scope rules + JSON findings schema + run-id, and returns **compact merge-tier JSON**.
- **Deterministic merge:** `confidence ∈ {0,25,50,75,100}` anchors → dedup by fingerprint `normalize(file)+line_bucket(line,±3)+normalize(title)` → **cross-reviewer promotion** (2+ agreeing bumps one anchor step) → separate pre-existing → **confidence gate LAST** (suppress `<75`, except a critical/P0 at `50+` survives).
- **Per-reviewer tiering:** correctness/security/adversarial on the session (deep) model; others mid-tier — omitting the override "silently 3-4x's the cost."
- **Debunked:** "deterministic merge math" is LLM prose in the donor — **oro does the merge in Go**, removing the caveat.

### P2 — Cheap-then-deep validation gate (donor: adamsreview)
- **Cheap pass:** chunked-batch Sonnet scoring, rubric 0-100 with an **err-up** instruction ("when uncertain between two levels pick the HIGHER" — a false positive costs one deep investigation; a missed bug ships) and an **anti-anchor-clustering** instruction.
- **Gate:** `advances = (score >= 45) OR (>=2 distinct source_families)`; gated-out → `below_gate`.
- **Deep pass:** deep lane (correctness+security) is **one sub-agent per candidate** (never batch deep-lane); light lane chunked. Structural `--expected $N` guard catches collapsed batches.
- **Debunked:** none material — adamsreview is code-backed; the economic argument is the load-bearing idea.

### P3 — Post-fix regression-revert (donor: adamsreview Phase 9) — top single win
- Fix agent edits the tree but **nothing is committed**; an independent reviewer decides per fix-group `verified | partial | regression` with a checklist incl. an **adjacent-regression sweep** (new bug in changed hunk ±20 lines → `regression`). Priority `regression > partial > verified`.
- **Revert:** regressed groups reverted via `git checkout -- <files>` + `rm -f <created>`; survivors staged by explicit name; file lists rebuilt from **git ground-truth** (`git status --porcelain` + `git cat-file -e`), not agent self-report.
- oro's unit of revert is simpler: **one worker = one fix-group**, so no union-find — a single `git reset --hard <baselineSHA>` in the isolated worktree replaces the per-file checkout/rm.

### P4 — Content-addressed finding triage (donor: clawpatch)
- **Identity:** `signature = stableId("sig", [featureId, category, title, canonicalEvidence(evidence)])`; `findingId = stableId("fnd", [signature])`; `canonicalEvidence` sorts refs into a stable string so ordering doesn't change identity. On write, `mergeFinding` keeps incoming content but **preserves prior `status`, `history`, `createdAt`**.
- **Status enum + audit:** `open|false-positive|fixed|wont-fix|uncertain` + append-only `history[]`.
- **Evidence validation against a recorded manifest:** pure line/string arithmetic — path-in-context, line-range-within-shown-range, quote-literal-match, no `../` escape.
- **Debunked:** identity is best-effort (a reworded title or ±1-line shift mints a new id); no worktree isolation, no streaming.

---

## 5. Design (oro-native)

All review-side changes live in `pkg/ops/`; the regression-revert lives in `pkg/dispatcher/`; persistence reuses `pkg/beadstore`. The five phases share one data structure — the **Finding**.

### 5.0 Shared finding schema (P0 spine)

**New file `pkg/ops/finding.go`:**

```go
// Severity mirrors the existing prompt vocabulary (review_prompt.go verdict section).
type Severity string
const (
    SevCritical  Severity = "critical"
    SevImportant Severity = "important"
    SevMinor     Severity = "minor"
)

// Evidence pins a finding to a file:line(:quote) the reviewer was shown.
type Evidence struct {
    File      string `json:"file"`
    LineStart int    `json:"line_start"`
    LineEnd   int    `json:"line_end"`
    Quote     string `json:"quote,omitempty"` // if present, must match literally
}

// Finding is the unit every reviewer (persona, cheap, deep, codex) emits and
// the Go merge consumes. Confidence uses CE anchors {0,25,50,75,100}.
type Finding struct {
    ID         string     `json:"id"`          // content-addressed; computed by FindingID()
    Severity   Severity   `json:"severity"`
    Category   string     `json:"category"`    // correctness|security|design|test|architecture|absence
    Title      string     `json:"title"`
    Detail     string     `json:"detail"`      // why_it_matters + fix direction
    Evidence   []Evidence `json:"evidence"`
    Confidence int        `json:"confidence"`  // 0|25|50|75|100
    Sources    []string   `json:"sources"`     // reviewer/persona ids (unioned on dedup)
    Origin     string     `json:"origin"`      // introduced|pre_existing
    Status     string     `json:"status,omitempty"` // P4: open|false-positive|fixed|wont-fix|uncertain
}

// ReviewReport is the parsed structured output of one reviewer pass.
type ReviewReport struct {
    Reviewer string    `json:"reviewer"`
    Findings []Finding `json:"findings"`
    Verdict  Verdict   `json:"verdict"` // self-verdict; merge recomputes the real one
    Raw      string    // unparsed stdout, for fail-open + journey payload
}
```

**Content-addressed id** (`pkg/ops/finding.go`):

```go
func canonicalEvidence(ev []Evidence) string // sort by File,LineStart,LineEnd; json.Marshal
func normalizeTitle(s string) string         // lowercase, collapse whitespace, strip trailing punct

func FindingID(beadID string, f Finding) string {
    h := sha256.Sum256([]byte(strings.Join([]string{
        beadID, f.Category, normalizeTitle(f.Title), canonicalEvidence(f.Evidence),
    }, "\x00")))
    return "fnd_" + hex.EncodeToString(h[:8])
}
```

**Prompt manifest + evidence validation** — **new file `pkg/ops/review_validation.go`** (mirrors clawpatch §4). The structured prompt records which files+line-ranges were shown; validation is pure arithmetic — **drop (not fail)** any finding citing a path not in the manifest, a line outside the shown range, or a quote that is not a literal substring:

```go
type PromptManifest struct{ Shown map[string][][2]int } // file -> shown [start,end] ranges

type DroppedFinding struct {
    Finding Finding
    Layer   string // "validation" | "schema"
    Reason  string
}

func ValidateFinding(m PromptManifest, repoRoot string, f Finding) error
func PartitionFindings(m PromptManifest, repoRoot string, in []Finding) (kept []Finding, dropped []DroppedFinding)
```

**Parsing** — **new file `pkg/ops/review_parse.go`** (two-layer, clawpatch §3): whole-document JSON parse fast path; on failure, validate the container then `json.Unmarshal` each finding element individually; a bad element becomes `DroppedFinding{Layer:"schema"}` rather than failing the whole pass. The legacy `parseReviewOutput` (`ops.go:703`) stays as the fallback when a reviewer emits no JSON block (back-compat).

**Verdict mapping** (preserves the `ops.go:703-720` contract): after merge+gate, reject if any surviving finding has `Severity ∈ {critical, important}` → `VerdictRejected`, else `VerdictApproved`. The terminal `VERDICT:` line stays required so unstructured fallback still works.

### 5.1 P3 — Post-fix regression-revert in the QG retry loop (ship first; independent of P0)

Only phase touching `pkg/dispatcher`. Operates on **test outcomes**, needs no finding schema → ships before P0.

**New file `pkg/dispatcher/qg_regression.go`:**

```go
// qgBaseline is captured BEFORE a retry edit is dispatched.
type qgBaseline struct {
    beadID     string
    worktree   string
    headSHA    string          // git rev-parse HEAD
    testRes    map[string]bool // test name -> passed, broader task-relevant set
    capturedAt time.Time
}

// qgRegression is the verdict after a retry.
type qgRegression struct {
    regressed []string // green at baseline, red now
    reverted  bool
}

func (d *Dispatcher) captureQGBaseline(ctx context.Context, beadID, worktree string) (qgBaseline, error)
func (d *Dispatcher) detectQGRegression(ctx context.Context, base qgBaseline, worktree string) (qgRegression, error)
func (d *Dispatcher) revertRegressedRetry(ctx context.Context, base qgBaseline, worktree string) error // git reset --hard base.headSHA
func parseTestOutcomes(output string) map[string]bool // pure
```

**Control flow** — extend the existing retry path (no new top-level loop). All hook lines verified 2026-05-31.

1. **Capture goes in `qgRetryWithReservation`'s I/O phase, NOT `reserveQGRetryAttempt`.** `reserveQGRetryAttempt` (`dispatcher.go:2035`) runs entirely under `d.mu.Lock()` and does no I/O — running git/tests there would hold the dispatcher lock across subprocess time (deadlock risk). Instead, in `qgRetryWithReservation` (`dispatcher.go:2108`), inside the `withReservation` I/O closure (the same outside-the-lock phase that builds the payload at `:2118-2119`), and only on the **non-transient** path (the transient branch already returned at `handleQGFailure:1981`), capture `qgBaseline`: `git rev-parse HEAD` in the worktree (via `(&ExecCommandRunner{Dir: worktree}).Run(ctx, "git", ...)`, the pattern at `dispatcher.go:3646`) + run the **broader test set** once, recording per-test pass/fail. Store in a new `d.qgBaselines map[string]qgBaseline` keyed by `beadID` (guarded by `d.mu`). Cache by `headSHA` so it runs at most once per bead-retry-cycle.
2. After the retried worker reports QG result and **before** `checkPreMergeQG` (`dispatcher.go:2367`, called from `mergeAndComplete` at `:2637`) proceeds to merge, run the broader test set again and diff against `baseline.testRes`. `true`→`false` = regression.
3. On regression: `git reset --hard <baseline.headSHA>` in the worker worktree, then treat the retry as **not progress** — do not advance the attempt toward "approved", feed a structured `regressed-tests` message into the next retry's `buildAssignPayload` feedback (`assign_payload.go:39`), and emit a `qg_regression_reverted` journey/log event (use `d.logEvent`).
4. If revert fails (dirty tree, detached state): do **not** merge — escalate via the existing `d.escalate` path (`dispatcher.go:7762`).

**Broader test set + baseline (the hard part):** v1 reuses `d.qgRunner.Run(ctx, worktree, true)` (`QGRunner.Run`, `ShellQGRunner` impl at `dispatcher.go:445`; `skipMutation=true`) as the broader set, parsing per-test pass/fail with `parseTestOutcomes` (Go `--- PASS/FAIL: TestName`; pytest `PASSED/FAILED`). Baseline = the QG run green on the **prior** accepted state, or a fresh run on `baseline.headSHA` for the first retry. If outcomes are unparseable, fall back to the coarse suite-level rule: any test the post-edit QG reports failing that the baseline reported passing is a regression.

**Where it hooks:** capture in `qgRetryWithReservation`'s I/O phase (`dispatcher.go:2108`, non-transient path); detect+revert in a new guard called from `mergeAndComplete` immediately before `checkPreMergeQG` (`dispatcher.go:2637`), and reused inside `checkPreMergeQG` (`:2367`) so a late retry's regression is caught on the happy path. Gated by `d.cfg.RegressionRevert` (**default true**, set in `Config.withDefaults` `dispatcher.go:696`).

### 5.2 P1 — Multi-persona parallel fan-out (depends on P0)

**Personas as Go-side prompt fragments**, not files (oro is the orchestrator, no plugin runtime). **New file `pkg/ops/personas.go`:**

```go
type Persona struct {
    ID       string // correctness|security|adversarial|design|test|architecture
    Role     string // agentmodel role, e.g. "ops_review_correctness"
    Fragment string // appended to the shared buildReviewPrompt body
}
func selectPersonas(opts ReviewOpts) []Persona // team selection; trivial/docs-only -> none
```

**Fan-out — `Spawner.Review` becomes the fan-out point** (`ops.go:278`). Introduce a shared parameterized helper `runWith(ctx, opsType, routing spawnRouting{role, runtimeOverride}, beadID, worktree, prompt)` that generalizes the current `run` (which hardcodes `opsType.Role()` at `ops.go:420`). `run` becomes `runWith` with `routing.role = opsType.Role()`, behavior-preserving:

```go
func (s *Spawner) Review(ctx context.Context, opts ReviewOpts) <-chan Result {
    // docs-only short-circuit unchanged (ops.go:279-288)
    if !opts.MultiPersona {
        return s.run(ctx, OpsReview, opts.BeadID, opts.Worktree, buildReviewPrompt(opts)) // back-compat
    }
    personas := selectPersonas(opts)
    manifest, prompt := buildStructuredReviewPrompt(opts) // emits finding schema + records manifest
    chans := make([]<-chan Result, 0, len(personas))
    for _, p := range personas {
        chans = append(chans, s.runWith(ctx, OpsReview,
            spawnRouting{role: p.Role}, opts.BeadID, opts.Worktree, prompt+p.Fragment))
    }
    out := make(chan Result, 1)
    go func() { out <- mergeReports(collect(chans), manifest, opts) }()
    return out
}
```

- **Bounded parallelism:** cap concurrent persona spawns at `opts.MaxReviewers` (default 4); queue overflow. Each `runWith` already runs in its own goroutine with a buffered channel (`ops.go:409`), so collection is awaiting N channels.
- **Per-reviewer tiering:** register roles `ops_review_correctness`, `ops_review_security`, `ops_review_adversarial` → `TierDeep`; `ops_review_design`, `ops_review_test`, `ops_review_architecture` → `TierBalanced`, the same way existing `ops_*` roles are wired into `agentmodel`'s `resolveRole`/`resolveTier` (NOT by editing a literal). Unknown-role fallback to `worker` tier means unconfigured installs degrade gracefully.

**Deterministic Go merge** — **new file `pkg/ops/review_merge.go`** (real code, kills the prose-determinism caveat):

```go
func mergeReports(reports []ReviewReport, m PromptManifest, opts ReviewOpts) Result {
    all := flatten(reports)                  // collect findings + Sources
    all, _ = validateAndPartition(all, m)    // P0 evidence validation (drop hallucinated)
    groups := dedup(all)                     // normalize(file)+bucket(line,±3)+normalize(title)
    for _, g := range groups { unionSources(g) } // duplication strengthens, not noise
    promote(groups)                          // 2+ distinct sources -> +1 confidence anchor
    separatePreExisting(groups)              // origin==pre_existing -> report-only
    survivors := gate(groups)                // confidence>=75 OR (critical at >=50); gate LAST
    return toResult(survivors, opts.BeadID)  // any surviving Critical/Important -> REJECTED
}
```

`dedup`, `promote`, `gate` are pure → directly unit-testable. Gate runs **last** so promotion can rescue an anchor-50 finding before any drop.

### 5.3 P2 — Cheap-then-deep gate (depends on P0; composes with P1)

Insert a cheap triage spawn *before* the deep personas, on large diffs only:

```go
if opts.CheapThenDeep && diffSizeExceeds(opts, opts.CheapGateThreshold) {
    cand := s.runCheapTriage(ctx, opts)       // one fast-tier spawn, scores concerns 0-100
    survivors := cheapGate(cand)              // score>=45 OR >=2 source_families
    opts = scopeToSurvivors(opts, survivors)  // deep personas only review survivor regions/concerns
}
// ... P1 fan-out over the (possibly narrowed) scope ...
```

- **Cheap pass** resolves a new `ops_review_triage` role → `TierFast`/`TierBalanced`; prompt carries the **err-up** + **anti-anchor** instructions.
- **Gate:** `score>=45 OR >=2 distinct source_families`. Gated-out concerns recorded as `below_gate` findings (persisted for P4, never block).
- **Economic rationale:** a cheap-gate false positive costs one deep investigation; a missed bug ships. Marginal on small task-scoped diffs → `diffSizeExceeds` gating so small beads skip the cheap pass and go straight to P1.

### 5.4 P4 — Content-addressed finding persistence + merge-preserving triage (depends on P0)

**Persist to the bead journey first.** v1 persists each merged finding as a `bead_journey` row (`migrate_v3.go`) with `actor="ops_review"` (already in the `Actor` vocabulary, `v3types.go`), `event="review_finding"`, `payload` = the JSON `Finding` (id content-addressed). Reuse `AppendJourney`/`LatestJourney` (`v3methods.go`). No migration for v1.

**Merge-preserving triage** (clawpatch §5): on re-review, before persisting, scan `LatestJourney(beadID)` for prior `review_finding` rows; if a new finding's `FindingID` matches a prior one whose payload carries `status ∈ {false-positive, wont-fix}`, refresh the new content but **preserve prior status + history**, and **exclude** that finding from the gate (it cannot re-block the merge). A `mergeFinding` helper appends a `history` entry rather than clobbering.

**Triage CLI verb:** `oro review triage <bead> <finding-id> --status=false-positive --note=...` appends a `kind:"triage"` history entry (new subcommand under `cmd/oro/`). Promote to a dedicated `review_findings` table (**migration v5** — confirm v4 is the current max first) only if journey-row volume/latency demands it (§11).

### 5.5 Composition with Spec 05 (Codex second-opinion)

Spec 05's `runWithRuntime(opsType, runtimeOverride)` seam and this spec's per-persona `runWith(spawnRouting{role})` seam are the **same parameterized helper** — implement one `runWith(opts spawnRouting{role, runtimeOverride})`. Spec 05 reconciles at the *verdict* level; this spec at the *finding* level (`mergeReports`). When both are enabled, the Codex pass is **one more reviewer** (`Sources=["codex"]`) feeding `mergeReports`; a Codex-only Critical still rejects. The finding schema (P0) is the shared contract.

---

## 6. Interface / API / config

`ReviewOpts` (`ops.go:177-187`) gains additive fields (zero values = today's behavior):
```go
MultiPersona       bool   // P1
MaxReviewers       int    // P1, default 4
CheapThenDeep      bool   // P2
CheapGateThreshold int    // P2, diff lines above which the cheap pass runs (default 400)
PersistFindings    bool   // P4
```
P3's `RegressionRevert bool` is plumbed via the dispatcher `Config` struct (built in `withDefaults`), **not** `ReviewOpts`.

Config keys (read where `ReviewOpts` / dispatcher `Config` are built — `cmd/oro/cmd_start.go`, dispatcher `withDefaults`):
- `ops.review.multi_persona` / `.max_reviewers` / `.personas` (subset selection)
- `ops.review.cheap_then_deep` / `.cheap_gate_threshold`
- `ops.review.persist_findings`
- `dispatcher.regression_revert` (P3) — **default true**; one flag to disable for debugging.

New agentmodel roles (registered like existing `ops_*` roles): `ops_review_{correctness,security,adversarial,design,test,architecture,triage}`. Absent-role fallback already exists (`resolveRole` → `worker`/default tier), so unconfigured installs degrade gracefully.

CLI: `oro review triage <bead> <finding-id> --status --note` (P4).

**No change to:** `parseReviewOutput` (kept as fallback, `ops.go:703`), `Verdict` enum (`ops.go:143-151`), `<-chan Result` contract, `handleReviewResult`, `ExtractPatterns` pattern side-channel.

---

## 7. Edge cases & failure modes

- **One persona errors/times out (P1)** — its channel returns `VerdictFailed`; `mergeReports` treats it as a missing report (fail-open per reviewer), merges the rest, notes degraded coverage. Review never hangs (buffered channels, `ops.go:409`).
- **All personas fail (P1)** — fall back to the legacy single `run` path + prose verdict.
- **Hallucinated finding (P0/P1)** — `ValidateFinding` drops findings citing unseen files/lines/quotes; recorded as `DroppedFinding{Layer:"validation"}`, never gated in.
- **Malformed JSON (P0)** — two-layer partition keeps valid findings, drops bad elements (`Layer:"schema"`); no JSON block at all → fall back to `parseReviewOutput`.
- **Cheap-gate false-negative (P2)** — err-up rubric + `>=2 source_families` auto-graduate; gated-out persists as `below_gate`; P2 skipped below `CheapGateThreshold`.
- **Regression-revert: outcomes unparseable (P3)** — suite-level conservative rule (any newly-failing test the baseline passed = regression). Better one re-spin than a shipped regression.
- **Regression-revert: `git reset --hard` fails (P3)** — do not merge; escalate.
- **Regression-revert: baseline test already red (P3)** — only green→red counts; pre-existing failures out of scope.
- **Transient QG retry (P3, NEW)** — the regression check runs only on the real-failure retry path, **after** `handleTransientQGFailure` has classified the failure as non-transient, so a flake-driven retry does not trigger a spurious revert.
- **Flaky test (P3)** — a flaky green→red flip would falsely revert. v1 accepts this; mitigation in §11 (re-run the single regressed test once).
- **Finding id drift (P4)** — reworded title / ±1-line shift mints a new id; `line_bucket(±3)` absorbs small shifts within a run; cross-run drift accepted as best-effort.
- **Triage hides a now-real bug (P4)** — a `false-positive` finding is excluded from the gate; if the surrounding code changes materially the id changes and the new finding gates normally. Documented limitation.
- **Cost blow-up (P1+P2)** — mitigated by per-reviewer tiering, cheap-gate scoping, `MaxReviewers` cap, docs-only short-circuit (`ops.go:279-288`). All review-side phases default off.
- **Concurrency / cancellation** — `s.active` (registered at `ops.go:445-447`) tracks every persona agent; `CancelForBead` (`ops.go:363-380`) kills all agents for a bead, covering the fan-out.

---

## 8. Backward-compat & blast radius

- **All review-side phases default OFF** (`MultiPersona`, `CheapThenDeep`, `PersistFindings` = false). With flags unset, `Spawner.Review` takes the existing single-`run` branch verbatim → byte-for-byte current behavior. Existing `pkg/ops` tests stay green.
- **P3 defaults ON** — pure safety, adds no new spawn (reuses `QGRunner.Run`). Blast radius: it can revert a worker's retry edit. Mitigations: only green→red counts; `git reset --hard` targets a captured SHA in an **isolated** worktree (never `main`); revert failure escalates instead of merging; transient retries excluded; one config flag disables it.
- **`run` → `runWith` refactor** is internal and behavior-preserving; Merge/Diagnose/Escalate/WriteAC/Decompose/Dream (`ops.go:295-344`) keep calling the unchanged `run` wrapper.
- **Finding schema additive** — the terminal `VERDICT:` line is still emitted and `parseReviewOutput` (`ops.go:703`) remains the fallback. The `ExtractPatterns` `PATTERN:` side-channel is untouched.
- **Journey persistence (P4)** uses an existing table + existing `actor` value — no migration for v1.
- **Verdict/dispatcher contract unchanged** — `handleReviewResult` / `maxReviewRejections` untouched; merge always emits one `VerdictApproved`/`VerdictRejected`.
- **Composes with Spec 05** (§5.5) via shared schema + shared `runWith` seam.

---

## 9. TDD testing plan (red-first)

Tests in `pkg/ops/` (`finding_test.go`, `review_validation_test.go`, `review_merge_test.go`, `review_parse_test.go`, `personas_test.go`) and `pkg/dispatcher/` (`qg_regression_test.go`), using the existing fake `BatchSpawner`/`RuntimeBatchSpawner` and `mockQGRunner` patterns already in `dispatcher_test.go`. Per project memory: `go test ./pkg/dispatcher/...` needs **120s+** timeout; build via `make build` (embed.go needs staged assets).

**P0 — schema + validation (pure, fastest, red first) — `pkg/ops`:**
1. `finding_test.go::TestFindingID_StableAcrossEvidenceReorder` — two `Finding`s with evidence in different order → identical `FindingID`. `TestFindingID_ChangesOnTitleOrCategoryOrFile` — changing any of title/category/file changes the id.
2. `review_validation_test.go::TestValidateFinding_RejectsHallucinations` — table: path-not-in-manifest → err; line-outside-shown-range → err; quote-not-literal → err; `../escape` path → err; valid line-range-only (no quote) within shown range → nil.
3. `review_validation_test.go::TestPartitionFindings` — input {1 valid, 1 invalid} → `kept=[valid]`, `dropped=[{Layer:"validation"}]`.
4. `review_parse_test.go::TestTwoLayerParse` — whole-doc valid JSON → all findings; one malformed array element → that element dropped (`Layer:"schema"`), siblings kept; no JSON block at all → falls back to `parseReviewOutput` returning the legacy verdict.

**P3 — regression-revert (ship first; red first) — `pkg/dispatcher`:**
5. `qg_regression_test.go::TestParseTestOutcomes` — Go input `--- PASS: TestA` / `--- FAIL: TestB` and pytest `test_a PASSED` / `test_b FAILED` → `{TestA:true, TestB:false, ...}`.
6. `qg_regression_test.go::TestDetectQGRegression` — baseline `{A:true,B:true}`, post `{A:true,B:false}` → `regressed=[B]`; baseline `{B:false}`, post `{B:false}` → no regression (pre-existing red); baseline `{A:true}`, post `{A:true}` → none.
7. `qg_regression_test.go::TestRevertRegressedRetry_IssuesResetHard` — assert (via a fake/recording `ExecCommandRunner`) the exact command `git reset --hard <headSHA>` runs with `Dir == worktree`.
8. `qg_regression_test.go::TestRetryFlipsSiblingRed_Reverted` (integration with fake `QGRunner` returning differing pre/post outcomes) — retry that flips a sibling test red → reverted, **not** merged, attempt **not** advanced, `qg_regression_reverted` event emitted.
9. `qg_regression_test.go::TestRevertFailure_Escalates` — fake reset returns error → escalation path invoked, no merge.
10. `qg_regression_test.go::TestRegressionRevertFlagOff_NoBaselineCapture` — `RegressionRevert=false` → `captureQGBaseline` never called, old behavior (regression guard).
11. `qg_regression_test.go::TestTransientRetry_SkipsRegressionCheck` — transient-classified failure → no baseline capture, no revert (guards the NEW transient seam).

**P1 — fan-out + merge (red first) — `pkg/ops`:**
12. `personas_test.go::TestReviewMultiPersona_SpawnsPerPersona` — `Review(MultiPersona:true)` spawns exactly `len(selectPersonas)` passes; assert via a recording spawner that each spawn used its persona role/tier.
13. `personas_test.go::TestReviewSinglePass_WhenMultiPersonaFalse` — `Review(MultiPersona:false)` spawns exactly one pass (no-fan-out regression).
14. `review_merge_test.go::TestDedupAndUnionSources` — two findings same file/±3 line/normalized title from sources `["a"]` and `["b"]` → one group with `Sources=["a","b"]`.
15. `review_merge_test.go::TestPromote` — a finding from 2 distinct sources at confidence 50 → 75 (+1 anchor).
16. `review_merge_test.go::TestGateRunsLast` — confidence-50 design finding suppressed; confidence-50 critical survives (P0 escape); a finding promoted 50→75 by two sources survives (proves gate after promotion).
17. `review_merge_test.go::TestOnePersonaErrors_MergesSurvivors` — one report is `VerdictFailed` → merged result from the rest, no hang.

**P2 — cheap gate (red first) — `pkg/ops`:**
18. `review_merge_test.go::TestCheapGate` — score 50 single-source advances; score 30 with `>=2 source_families` advances; score 30 single-source → `below_gate`.
19. `personas_test.go::TestCheapThenDeep_SkipsBelowThreshold` — `Review(CheapThenDeep:true)` with diff under `CheapGateThreshold` → cheap pass not spawned (small-diff regression).

**P4 — persistence + triage (red first) — `pkg/ops` + `cmd/oro`:**
20. `finding_test.go::TestReReviewPreservesTriage` — prior journey row has matching `FindingID` with `status="false-positive"` → re-review refreshes content, preserves status+history, and excludes it from the gate (does not re-block).
21. `cmd/oro` triage test::`TestReviewTriage_AppendsHistory` — `oro review triage` appends a `kind:"triage"` history entry; invalid `--status` value rejected (enum validation).

Run: `go test ./pkg/ops/... -count=1` and `go test ./pkg/dispatcher/... -count=1 -timeout 180s`.

---

## 10. Effort & sequencing

| Phase | Size | Depends on | Ships when |
|---|---|---|---|
| **P3 regression-revert** | **M** (dispatcher git plumbing + test-diff; reuses `QGRunner`, worktree git; must respect the transient-QG seam) | none | **First** — pure safety, default on, no schema needed |
| **P0 finding schema + validation** | **M** (pure Go: id hash + line/string validation + two-layer parse) | none | Second — spine for P1/P2/P4 |
| **P1 multi-persona fan-out + Go merge** | **L** (personas, `runWith` seam, bounded fan-out, deterministic merge, 6 new roles) | P0; shares `runWith` seam with Spec 05 | Third |
| **P2 cheap-then-deep gate** | **M** (cheap triage spawn + gate + diff-size scoping) | P0; composes with P1 | Fourth |
| **P4 persistent finding triage** | **M** (journey persistence + merge-preserving triage + CLI verb) | P0 | Fifth |

- **Cross-spec dep:** P1 and Spec 05 must agree on the `runWith(spawnRouting{role, runtimeOverride})` seam — implement it once. If Spec 05 lands first, P1 generalizes its `runWithRuntime`; if P1 lands first, Spec 05's Codex pass plugs in as one persona.
- **Recommended order:** P3 → P0 → P1 → P2 → P4.

---

## 11. Out of scope / open questions

- **Union-find fix grouping** — unnecessary: oro is one-worker-one-group, so P3 reverts a single retry.
- **Wave-2 adjacent-candidate sweep / cross-cutting groups** — deferred; v1 P1 is one round of personas.
- **`auto_fix_hint` two-pass gen+verify** — deferred; orthogonal to gating.
- **Per-phase token tally** — observability, low priority vs correctness.
- **`DIFF_START`/`DIFF_END` injection hardening** for adversarial diffs — shared with Spec 05; defer to a hardening pass.
- **Dedicated `review_findings` table** vs journey rows — start with journey (no migration). **Confirm the current max migration version** (v4 exists; next would be **v5**) before numbering. Open.
- **Flaky-test re-run before P3 revert** — should `detectQGRegression` re-run a single green→red test once before reverting? Likely yes; tune after observing real flip rates.
- **Auto-enable thresholds** — should `MultiPersona`/`CheapThenDeep` auto-enable above a diff-size threshold? Defer tuning.
- **Sub-bead diff slicing** — oro already reviews per-bead; lower priority than P1-P4.
- **`oro current` finding surfacing** — UI for open finding counts; out of scope.
- **`bead_learnings_pending` interaction (NEW)** — migration v4 introduced new tables; confirm whether review findings should feed the learnings pipeline rather than (or in addition to) the journey. Open.
```
