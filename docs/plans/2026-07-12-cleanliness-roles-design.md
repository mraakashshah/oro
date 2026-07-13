# Codebase Cleanliness Roles: Janitor & Auditor Design

**Date:** 2026-07-12
**Status:** R1 FAIL (6 wiring gaps) → R2 FAIL (suppression hook on wrong
close path) → R3 FAIL (stale hook clause; missing beadstore metadata query)
→ R4 FAIL (single finding: acceptance Cmd pipe truncation +
pipeline-exit-swallowing) → R5: single-edit fix, verified inline against
`parseAcceptanceCmd` line-mode (dispatcher.go:3428–3436 — line-per-field
splits on newlines only, pipes within the Cmd line preserved). R4 cleared
everything else: 0 wiring gaps, 0 traceability gaps, ~25 code citations
verified. **Ready for beadcraft decomposition.**

## Goal

Give oro a standing codebase-cleanliness capability that exists outside any
feature request. Two distinct roles:

1. **Janitor** (`OpsJanitor`) — rot insurance. Continuous, cheap. Keeps code
   and docs maintainable: dead code, duplication, stale docs, lint-fixables.
2. **Auditor** (`OpsAudit`) — breakage insurance. Periodic, deep. Whole-repo
   audit of everything statically verifiable: code quality, test safety,
   migration/data integrity, static security, perf patterns, DX/deps.

Both roles **scan and file beads** — they never edit code. Normal workers
execute the filed beads through the existing QG + ops-review pipeline. This
reuses all safety machinery and adds no new write authority.

## Context & Prior Art

- **Dream pattern** (`docs/plans/2026-03-31-memory-dreaming-design.md`,
  `pkg/ops/dream_prompt.go`): the template for a standing role — new
  `ops.Type`, prompt builder, dispatcher activity counter, structured-output
  handling. Janitor/Auditor copy this shape.
- **Epic-fix precedent** (`pkg/ops/epic_fix_prompt.go`): an ops agent that
  files beads with machine-verifiable `Cmd:` acceptance already exists.
- **Reviewer pipeline** (`pkg/ops/review_merge.go`, `review_validation.go`):
  `Finding` schema, evidence-manifest validation (`PartitionFindings`),
  dedup (`dedupFindings`, ±3-line bucket), cross-source confidence promotion
  (`promoteFindings`), confidence gate (`gateFindings`), journey persistence
  (`persistReviewFindings` / `priorReviewFindings`). All reused as-is.
- **File-debt decision** (`docs/decisions&discoveries.md`): agent-written
  report files are debt. Output is beads + journey events, never report files.
- **Cost baseline:** ops review already spawns up to 6 personas per merged
  bead. An audit of ~6 agents every ~250 merges is negligible by comparison.

### Peak-throughput data (calibrates triggers)

Apr 25 – Jun 1: 1,517 commits, peak 201/day, avg 8.8 files & ~247 lines churn
per commit. Commits ≈ 3–5× merged beads. At peak, janitor-every-50-merges
fires roughly every 1–3 days; audit-every-5-janitors ≈ 1–2 per hot fortnight,
~0 when idle. Frequency scales with change rate by construction.

## Decisions (locked with user)

| Decision | Choice |
|---|---|
| Role power | Scan + file beads only; workers do the fixing |
| Identity | Two distinct roles: janitor + auditor |
| Environments | Local-only; auditor v1 is static-core only |
| Output | Beads for actionable findings; state/coverage in journey events |
| Janitor trigger | Every **50** merges + idle gate; force-run at 3× (150) |
| Audit trigger | Every **5th** janitor cycle (~250 merges) |
| Flood control | Janitor: top-**5** per cycle (noisy detector input). Audit: **no cap** — the confidence gate is the throttle; every gated survivor files |
| Criticals | Priority-0 beads, **no** escalation — the queue handles it |
| Detection | Deterministic detectors → single LLM triage (janitor); fan-out section agents (audit) |

## Architecture

### Shared pipeline (both roles)

```
detect → triage/judge (LLM) → validate evidence → dedup vs journey + open beads
       → top-K file as beads (with Cmd: acceptance) → persist ALL findings to journey
```

Findings use the existing `ops.Finding` schema (severity, category, evidence
with file/line/quote, confidence, origin). All findings — filed or not — are
appended to the journey of a **persistent role bead** (one `janitor` bead, one
`audit` bead per project), the same way review findings persist today.

**Role-bead lifecycle (explicit, owns three failure modes):**
- *Discovery:* deterministic — query by a dedicated marker
  (`meta_role: janitor|audit`), never by remembered ID. Dispatcher state is
  in-memory only and restarts are routine; lazy-create ONLY when the marker
  query returns nothing, so restarts never mint a second role bead and split
  the journey history that dedup depends on.
- *Non-assignable:* role beads are created `closed`. `CreateParams` has no
  `Status` field today, and create-open-then-close leaves a race window
  where the assign loop could grab an AC-less open bead — so this requires
  an optional `Status` on `CreateParams` (atomic create-closed).
  `AppendJourney` is a plain INSERT with no status check, so closed beads
  accept journey events; no GC path deletes closed beads or journeys.
  Acceptance test: role bead never appears in `Ready()` output, including
  when created mid-tick — guarding the MISSING_AC worker-churn failure.
- *Actor labels:* journey events use actors `ops_janitor` / `ops_audit`
  (persist/reload helpers gain an actor parameter; `ops_review` stays the
  review default).

**Suppression (derived at scan time — no close hook anywhere):**
R2 review proved a `dispatcher.CloseBead` hook can never see the closes that
matter: wont-fix is a human judgment, and humans close via `oro task close`
(cmd_bead.go:227–248), which opens SQLite directly and never touches the
dispatcher. So suppression is NOT event-driven. Instead, at the start of
every janitor/audit cycle, the dispatcher queries closed beads carrying
`meta_finding_id` and reads their persisted `close_reason`:

- **Reason contract:** a close reason with case-insensitive prefix
  `wont-fix` (e.g. `--reason "wont-fix: intentional export"`) →
  `Status: wont-fix`, suppressed permanently. Any other close — including a
  reasonless close, which persists `''` → `Status: fixed` (refiles only if
  the detector flags it again later). **First close wins:** `close_reason`
  is written with `COALESCE(close_reason, ?)`, so a reasonless close
  followed by a re-close with `wont-fix:` is a silent no-op; to change a
  reason, reopen (which nulls `close_reason`) then re-close. Both the prefix
  and the reopen-to-change rule are documented in every filed bead's
  description so the human knows the contract at close time. Unit test pins
  the reasonless-close → `''` → `Status: fixed` mapping.
- **Query surface:** the scan uses the new metadata-keyed beadstore query
  (see New surface) — unbounded, never `Closed(ctx, limit)`. Unit test
  proves suppression survives more closed beads than any recency limit.
- **Cross-role scope:** the derived wont-fix set is role-agnostic, and
  bucket-matching needs the original finding's evidence — so the scan reads
  the **union of both role-bead journeys**, resolving each
  `meta_finding_id` against whichever journey persisted it. Otherwise a
  wont-fixed janitor finding would be refiled by the audit (possibly at
  priority 0).
- This derived set + the role-bead journeys feed the triage prompt's
  suppressed list. Works for CLI closes, dispatcher closes, dash closes,
  and any future close path — placement-proof by construction.

Suppression matching uses the ±3-line `sameFindingBucket` logic against
prior findings, NOT exact `FindingID` equality — FindingID hashes exact line
numbers, so unrelated edits above a finding would otherwise resurrect a
wont-fixed finding as "new". Acceptance criteria: suppression survives
±3-line evidence drift, AND the integration test performs the wont-fix close
through the REAL CLI path (`beadstore.Close` with a `wont-fix:` reason), not
through `dispatcher.CloseBead`.

Unfiled survivors (beyond the janitor's top-5) refile in later cycles while
still true.

### Janitor (`OpsJanitor`)

**Trigger** — in the dispatcher, paralleling `beadsSinceDream`:

```go
d.mergesSinceJanitor++
if d.mergesSinceJanitor >= d.cfg.JanitorInterval {          // default 50
    if d.cachedQueueDepth <= d.cfg.JanitorIdleThreshold ||  // idle gate (mu-guarded field, maintained by assign loop)
        d.mergesSinceJanitor >= 3*d.cfg.JanitorInterval {   // force-run at 150
        d.mergesSinceJanitor = 0
        d.safeGo(func() { d.spawnJanitor(ctx) })
    }
}
```

`JanitorIdleThreshold` defaults to **0** — the janitor fires only when the
ready queue is empty (or the force-run trips). Deliberate: cleanup fills
true slack; raise the knob to loosen.

The force-run clause exists because rot accrues fastest exactly when the
queue never quiets (peak weeks).

**Stage 1 — deterministic detectors (no LLM).** Convention mirrors
`quality_gate.sh`: a project-owned `scripts/janitor_detect.sh` that emits
candidate findings as JSON lines (`{"detector":..., "file":..., "line":...,
"title":..., "detail":...}`). Snapshot-copied into the scan worktree like the
QG script so it can't be edited mid-run. When the script is absent, built-in
fallbacks run per detected language:

- Go: `deadcode`, `dupl`, `golangci-lint run --fix=false` (fixable subset)
- Python: `ruff check`, `vulture`
- Any: TODO/FIXME older than 60 days, broken relative links in `*.md`,
  orphan files (unreferenced assets/scripts)

**Missing-tool behavior:** a detector whose binary isn't installed is
skipped, recorded in the cycle's journey event (`skipped: [vulture]`), and —
critically — the filing path only ever embeds a detector re-run `Cmd:` for
detectors that actually ran during the scan. Otherwise a filed bead's
acceptance would be unrunnable in the worker environment and churn workers
forever.

**Stage 2 — single cheap LLM triage spawn** (model: haiku-tier via explicit
`Tier()`/`Role()` cases — unknown roles fall back to the worker model, so the
cases are mandatory, not cosmetic). Prompt receives candidates + open-bead
titles + suppressed finding IDs. Judges: real dead code vs intentional
export; dup worth extracting vs noise; doc genuinely stale vs merely old.

**Filing mechanism (single, unambiguous):** the agent NEVER files beads. It
emits structured `Finding` JSON only; the dispatcher's result handler parses
it and creates the top-5 beads via `d.beads.Create` (the existing
dispatcher-side path used by rebase/fix bead creation). Write authority stays
in the dispatcher; the epic-fix "agent shells out to `oro task create`"
pattern is explicitly NOT used here. Every filed bead gets machine-verifiable
acceptance: `Cmd: <detector re-run showing finding gone> &&
./scripts/quality_gate.sh`, is **low-priority**, and embeds its finding ID in
bead metadata (`meta_finding_id`) for the suppression write-path.

**Scan worktree:** detectors need a checkout. Reuse the `checkEpicQG`
temp-worktree pattern (dispatcher.go:2606–2643): create a throwaway worktree
from `DefaultBranch` with a unique ID, snapshot-copy `janitor_detect.sh` into
it, run detectors + triage there, remove it in a defer. Not bead-coupled —
same lifecycle as epic-QG worktrees.

### Auditor (`OpsAudit`)

**Trigger:** every 5th janitor cycle (counter `janitorRunsSinceAudit`,
incremented per janitor spawn, audits at ≥5 then resets). On the 5th tick the
audit runs **instead of** that janitor cycle (never two concurrent scans of
the same tree). Inherits the janitor's idle-gating and force-run semantics.
**Config validation:** `AuditEnabled && !JanitorEnabled` is a startup error —
the audit counter is driven by janitor cycles, so an audit without a janitor
is a dead flag; fail loudly rather than silently never running.

**Fan-out — 6 section agents**, run in waves via the existing
`collectPersonaReviews` machinery, each with a section fragment appended to a
shared audit base prompt:

| Section | Focus (from the 12-section checklist, static-core subset) |
|---|---|
| `code-quality` | readability, oversized files/functions, dead code, unnecessary abstraction, logic/presentation separation (§3) |
| `tests-safety` | coverage of critical paths, behavior-vs-implementation tests, flaky/skipped/quarantined tests, determinism (§4) |
| `data-migrations` | schema constraints, migration reversibility/safety, identifier & timestamp consistency (§6 static) |
| `security-static` | secrets in code/logs, dependency vulns, injection patterns, privileged-path review (§7 static) |
| `perf-patterns` | N+1 queries, unbounded operations, missing pagination/batching, sync work that should be async (§10 static) |
| `dx-deps-docs` | pinned versions, setup docs accuracy, outdated/abandoned deps, doc rot & broken references (§11 + docs cleanup) |

Each agent returns structured `Finding` JSON (same schema). Merge reuses the
reviewer pipeline stages `dedupFindings` → `promoteFindings` (+25 confidence
on cross-section corroboration) → `gateFindings` (≥75 confidence, or
Critical ≥50) — but NOT `buildPromptManifest`, which is diff-based
(`reviewDiffPaths` vs a base branch) and returns an **empty manifest for a
whole-repo scan**, which would silently kill every finding in
`PartitionFindings`. New component: **whole-repo manifest builder** —
`Shown` = every tracked file in the scan worktree (`git ls-files`) with its
full line range. Evidence rules extended: findings with file-only evidence
(no line — e.g. orphan file, stale doc) validate against file presence in
the manifest instead of `rangeShown`, which currently rejects
`line_start <= 0`; file-only evidence must carry an **empty quote**
(`validateLiteralQuote` needs line numbers, so quotes are line-evidence-only).

**All** gated survivors file as beads: Critical → priority 0, Important →
priority 1, Minor → low priority. No escalations, no cap — the gate
(≥75 confidence, Critical ≥50, evidence-validated, corroboration-promoted)
is the flood control; queue priority ordering ensures worst-first
consumption. Rationale for the janitor/audit asymmetry: janitor candidates
come from noisy mechanical detectors every ~50 merges, audit findings are
LLM-judged and confidence-gated every ~250 — by the time an audit finding
survives the pipeline, it has earned a bead.

**Coverage honesty:** every audit run appends a `audit_coverage` journey
event listing covered sections and `NOT COVERED: product-correctness-live,
reliability-injection, integrations-live, deploy-observability` — visible
debt, never silent omission.

### New surface (all additive)

- `pkg/ops/ops.go`: `OpsJanitor`, `OpsAudit` type constants + spawner methods
  `Janitor(ctx, JanitorOpts)`, `Audit(ctx, AuditOpts)`. **Mandatory switch
  cases** — these are the silent-no-op traps: `parseResult` (ops.go:744 —
  without a case, agent stdout is discarded and `Result.Feedback` is empty),
  `Type.Timeout()` (ops.go:135 — without a case, audit sections get the 5-min
  default and a whole-repo scan times out to VerdictFailed; janitor 10m,
  audit 20m), `Type.Tier()`/`Role()` (haiku-tier janitor triage; without
  cases both fall back to the worker model). One un-stubbed test must
  round-trip real subprocess stdout → `Result.Feedback`.
- `pkg/ops/janitor_prompt.go`, `pkg/ops/audit_prompt.go` (+ section fragments)
- `pkg/ops`: whole-repo manifest builder (`git ls-files` → full-range Shown
  map) + file-only evidence validation relaxation in `review_validation.go`
- `pkg/janitor/detect.go`: detector runner + JSON candidate parsing + built-in
  fallbacks
- Dispatcher: two counters, idle gate (`cachedQueueDepth` is already
  maintained and mu-guarded — the gate is a cheap field read),
  `spawnJanitor` / `spawnAudit`, scan-worktree lifecycle (epic-QG temp
  worktree pattern), result handlers that parse Finding JSON and create beads
  via `d.beads.Create`, scan-time suppression query (closed beads with
  `meta_finding_id` → `close_reason`; there is NO close hook — see
  Suppression section)
- `pkg/beadstore`: metadata-keyed query method (e.g.
  `FindByMetadataKey(ctx, key)` — unbounded, status-filterable), implemented
  in `sqlite.go` + `testfake.go` + `shadow.go` (ReadTx parity enforced by
  read_tx_parity_test.go). Used for BOTH `meta_role` discovery and the
  `meta_finding_id` suppression scan. The existing `Closed(ctx, limit)` is
  recency-truncated (`ORDER BY closed_at DESC LIMIT ?`) — using it would make
  "permanent" suppression silently expire once closed-bead count exceeds the
  limit. Metadata lives in the separate `bead_metadata` key/value table, so
  the query is a cheap JOIN. Implementation note: add `FindByMetadataKey` to
  the `renderFacingReadMethods` list in `read_tx_parity_test.go` — parity is
  only enforced for listed methods. Plus optional `Status` on `CreateParams`
  (atomic create-closed; additive, zero value = current behavior)
- Config: `JanitorInterval` (50), `JanitorIdleThreshold`, `AuditEveryNJanitors`
  (5), `JanitorTopK` (5), `JanitorEnabled`/`AuditEnabled`. Plumbing: values
  are set explicitly in `cmd/oro/cmd_start.go` (the `DreamInterval`
  precedent, ~line 1035) — `withDefaults` alone never reaches production.
  Enabled flags must NOT use `boolDefault` (a default-true bool becomes
  impossible to disable); use explicit flag wiring in cmd_start.
- Persistent role beads: marker-discovered (`meta_role`), created closed,
  lazily on first run (see lifecycle above)

## Premortems (resolved)

- **Garbage beads waste workers** → machine-verifiable `Cmd:` acceptance,
  top-K caps, wont-fix journey suppression, low default priority.
- **Hallucinated findings** → evidence-manifest validation
  (`PartitionFindings`) already battle-tested in multi-persona review, fed by
  the new whole-repo manifest builder (the diff-based builder returns an
  empty manifest for a no-diff scan and would silently drop everything —
  R1's top finding).
- **Janitor starves during peak (when rot grows fastest)** → 3× force-run
  clause overrides the idle gate.
- **Queue pollution** → janitor top-5 + low priority; audit relies on the
  confidence gate — accepted risk (user decision): a backlog-heavy first
  audit may file 40+ beads at once. Mitigant: severity→priority mapping
  keeps `oro task ready` ordered worst-first, and Minors sit in the
  low-priority lane where they only run on an idle queue.
- **Finding churn as code moves** → ±3-line evidence bucketing + normalized
  title matching (existing `sameFindingBucket`).
- **Detector script gamed by workers** → snapshot-copy convention identical
  to `quality_gate.sh` handling.
- **Audit re-flags what review already approved** → triage prompt receives
  suppressed IDs and open-bead titles; `origin: pre_existing` findings are
  NOT dropped here (unlike review) because pre-existing rot is exactly the
  target — instead dedup-vs-journey prevents repeat filing.
- **Two roles drift apart** → shared pipeline code; only prompts, triggers,
  and K differ.

## Load-bearing assumption

Merged-bead counters are a good proxy for "enough change to justify a scan."
If the swarm's bead size distribution changes drastically (many tiny beads),
intervals need retuning — both are config knobs, not constants.

**Accepted risks (documented, not fixed):**
- Cycle counters are in-memory (same as `beadsSinceDream`): a dispatcher
  restart resets progress toward the next janitor run. On a
  frequently-restarted factory the janitor under-fires. Same accepted
  behavior as dream today; persist counters only if this bites in practice.

## Testing strategy

The R1 review proved stub-heavy tests would green-light three silent no-ops.
Tests must exercise the real seams:

- Unit: counter/gate logic (interval, idle gate, force-run, audit cadence,
  audit-replaces-5th-janitor, `AuditEnabled && !JanitorEnabled` startup
  error), detector JSON parsing, janitor top-5 ordering, audit
  severity→priority mapping, suppression bucket-matching under ±3-line drift,
  role-bead marker discovery + never-assignable assertion, journey events
  carry actors `ops_janitor`/`ops_audit`, suppression survives more closed
  beads than any recency limit (metadata-keyed query, not `Closed(limit)`),
  reasonless close → `''` → `Status: fixed`.
- Seam tests (NO stubbed spawner): real subprocess stdout → `parseResult` →
  `Result.Feedback` round-trip for both new ops types; value-assertion unit
  test pinning `Timeout()` (janitor 10m, audit 20m), `Tier()`, and `Role()`
  for both new types (the round-trip test cannot catch a missing Timeout case
  — nothing runs 5+ minutes in tests, but production audits would silently
  VerdictFail); whole-repo manifest builder on a fixture repo → real
  `PartitionFindings` keeps line-bearing AND file-only findings.
- Config plumbing test in `cmd/oro`: asserts the start path sets
  `JanitorInterval`/`AuditEveryNJanitors` and registers the enable flags —
  `withDefaults` intentionally doesn't default interval knobs (0 = disabled,
  the `DreamInterval` precedent), so dropped cmd_start plumbing = feature
  silently disabled in production while all pkg tests stay green.
- Integration: janitor cycle end-to-end on a fixture repo with known dead
  code → scan worktree created from DefaultBranch and removed; bead created
  with correct `Cmd:` acceptance and `meta_finding_id`; REAL CLI-path
  wont-fix close (`beadstore.Close` with `wont-fix:` reason, NOT
  `dispatcher.CloseBead`) → second cycle **derives** `Status: wont-fix` at
  scan time and files nothing. Missing-tool test: detector binary absent →
  skipped + recorded in cycle journey event, and no filed bead embeds a
  `Cmd:` for a detector that didn't run.
- Audit: fan-out through the real merge path (real manifest, real gate) with
  fixture section outputs → all gated survivors filed, coverage event
  appended.

**Epic acceptance** — MUST be stored in **line-per-field format** (the
inline format splits the whole AC on `|` at `parseAcceptanceCmd`,
dispatcher.go:3437, which would truncate this Cmd at the pipe inside the
`-run` regex to an unterminated quote → permanent sh failure → endless
epic-fix churn; line mode splits on newlines and preserves pipes):

```
Cmd: go test ./pkg/dispatcher/ ./pkg/ops/ ./pkg/janitor/ ./cmd/oro/ -run 'Janitor|Audit' -count=1 -timeout 600s -v > "${TMPDIR:-/tmp}/oro-clean-accept.log" 2>&1 && grep -q -- '--- PASS: TestJanitorCycleEndToEnd' "${TMPDIR:-/tmp}/oro-clean-accept.log"
Assert: exit 0
```

Structure notes: `./cmd/oro/` is inside the acceptance boundary (R2 found
the original scope let a dropped cmd_start task pass). The `&& grep -q` for
the canonical integration test guards the `go test -run` zero-match trap
(matching no tests still exits 0) — and the `go test > log && grep` shape,
rather than a bare `go test | grep` pipeline, preserves go test's exit code
(sh runs without pipefail, so a pipeline's status is grep's alone; a red
suite with one passing canonical test would otherwise pass acceptance).

## Out of scope (v2 candidates)

- Local-boot probes (smoke test, failure injection) via per-project
  `audit.yaml`
- Operational sections (live deps, observability, backups)
- oro-dash audit panel / trend visualization
- Janitor auto-fix mode (role edits code itself)
