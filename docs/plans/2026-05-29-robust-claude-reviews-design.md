# Robust Claude Reviews — Skill Hardening + Durable Oro Streaming

**Date:** 2026-05-29
**Status:** Design (validated, pending adversarial review)
**Author:** Claude (spec flow, requested during P0 recovery)

## Problem

During the P0 dispatcher/worker recovery, code reviews invoked as
`timeout Ns claude -p '<review prompt>'` repeatedly **died at the wall-clock
limit with exit code 124 and no stdout/stderr**. The operator (codex) was
time-bounding `claude -p` calls; the coreutils `timeout`/`gtimeout` wrapper
SIGTERMs the child at a fixed wall-clock limit.

Diagnosis (evidence-backed):

- A bare `claude -p "PONG"` returns in **3.64s** — not a startup hang.
- The binding timeout is the **outer coreutils `timeout` wrapper** (exit 124),
  NOT oro's `OpsReview` 35-min timeout (that path uses `exec.CommandContext`,
  no shell `timeout`) and NOT codex's `yield_time_ms`.
- A plain coreutils `timeout` is **pure wall-clock — it never resets on output
  activity.** Streaming alone does not save it; it must be paired with a bigger
  budget or an idle-based watchdog.

Four compounding causes:

1. **Unbounded review work** — the review prompt mandates 6 checks ×
   read-all-affected-source + run the acceptance command
   (`pkg/ops/review_prompt.go`). Easily exceeds a 120s budget.
2. **Non-streaming output** — `claude -p` default mode flushes the result only
   at completion; *and* oro's `ExecSpawner` buffers stdout into an in-memory
   `strings.Builder` read only after `Wait()` (`pkg/ops/exec_spawner.go:54-61`).
   So "no output until done" is structural at two layers. A kill yields nothing.
3. **Box saturation** — concurrent `quality_gate.sh` / `go test` /
   golangci-lint runs inflate review wall-clock past the budget (observed: 3
   stacked quality gates during the incident).
4. **Wall-clock timeout never resets on activity** — even a working review dies
   at the fixed limit.

## Research (files read)

| Source | Finding |
|---|---|
| `pkg/ops/exec_spawner.go:54-61, 155-157` | `claude -p <prompt> --model <model>`, no streaming; stdout buffered, read post-`Wait()` |
| `pkg/ops/ops.go:126-137, 470-506` | `OpsReview` timeout 35 min, `--ops-review-timeout` override; `waitForProcess` is wall-clock only |
| `pkg/ops/review_prompt.go` | Review prompt mandates `git diff` + file reads + acceptance command — cannot be tool-free |
| `pkg/worker/worker.go:1884` | **Real stream-json precedent:** `["-p", prompt, "--model", model, "--verbose", "--output-format", "stream-json"]` |
| `pkg/worker/streamjson.go:43` | `ParseStreamEvent(line []byte) Activity` — the NDJSON line parser to reuse (NOT codesearch's single-envelope `json.Unmarshal`) |
| `pkg/worker/worker.go:240,1039,1535,1942` | Proven idle-tracking pattern: `lastSubprocOutputAt` updated per `StdoutPipe` line |
| `pkg/ops/exec_spawner.go:118,155` | `buildClaudeOpsArgs` is the **shared** `BuildArgs` for ALL ops types (review/merge/decompose/escalation) — a global switch breaks the others |
| `pkg/ops/ops.go:28` | `Process` interface = `Wait/Kill/Output` only — no accessor for an idle timestamp |
| `pkg/ops/ops.go:~642` (parseReviewOutput) / `decompose_prompt.go:64` | Review verdict parse scans final line; decompose uses `strings.Contains` — both consume raw stdout, both break on NDJSON unless result text is extracted first |
| `~/.claude/skills/adversarial-spec-review/SKILL.md:236-247` | "Running as a Subagent" gives the invocation with NO budget/streaming/early-flush guidance |
| `docs/decisions&discoveries.md`, `docs/plans/*` | No prior decision/design on review streaming or timeout — genuinely new |

**Empirically verified:** `claude -p --output-format stream-json --verbose`
emits incremental events — `hook_started`/`hook_response` (~0s), `init` (+1s),
first assistant token (+4s), `thinking_tokens` + assistant deltas throughout,
final `result` event with `duration_ms`. This makes a liveness signal and an
idle-watchdog viable, and lets a kill capture partial output.

## Consultation summary (ledger drained)

- **Real problem:** reviews die silently mid-incident, blocking P0 recovery.
- **Status quo:** operator hand-wraps `claude -p` in a too-short `timeout`;
  failures are invisible (no output).
- **Narrowest wedge:** Part A (skill invocation guidance) alone unblocks the
  operator path with zero code risk — shipped first.
- **Do nothing:** reviews keep failing during every future incident.
- **Future-fit:** Part B makes oro's own review pipeline stream + self-bound, so
  no operator ever needs a dumb wall-clock wrapper — durable capability.

## Design

### Part A — Harden `adversarial-spec-review` skill *(immediate, near-zero blast radius)*

Rewrite "Running as a Subagent" (lines 236-247) and add an **Invocation Budget &
Streaming** subsection:

- **A1. Stream by default** — invoke with
  `claude -p '<prompt>' --output-format stream-json --verbose`. Liveness +
  partial-output-on-kill.
- **A2. Idle-watchdog over fixed wall-clock** — kill only after N s of *no
  stream activity* (~120s idle). If only coreutils `timeout` is available, size
  the budget to the spec: floor **300s**, **+60s per affected package**. Never
  120s for a multi-package review.
- **A3. Pre-flight serialize** — do not launch while `quality_gate.sh` / `go
  test` saturate the box; bounded wait, then proceed.
- **A4. Early verdict flush + bounded reading** — emit a provisional `verdict:`
  block after Checks 1-2, then refine, so a late kill still yields a usable
  verdict. Reaffirm: no broad test suites (already out-of-scope, skill 36-37).

### Part B — Durable oro ops-review fix (`pkg/ops`)

**Coupling note:** plain `claude -p` only flushes at completion, so incremental
reading is worthless without stream-json. B1 and B2 are therefore coupled, and
stream-json is mandatory for any progress signal. Adversarial review added B0
(the idle signal had no consumer) and forced OpsReview-scoping (the args are
shared across all ops types).

- **B0. Extend the `Process` interface** (`ops.go:28`) with
  `LastOutputAt() time.Time` (or an idle channel). Without this, B1's timestamp
  lives on the concrete `opsProcess` and `waitForProcess` (holding only the
  interface) cannot read it — the idle-watchdog would have nothing to watch.
  **Blocks B1 and B3.**
- **B1.** `exec_spawner.go` — stream stdout incrementally via `cmd.StdoutPipe()`
  and `bufio.Scanner`, **mirroring `pkg/worker`'s proven pattern**, not reinventing
  with `os.Pipe`. Update `opsProcess.lastOutputAt` per line; still accumulate the
  full output for parsing. Implement `LastOutputAt()` from B0.
- **B2.** Emit `--verbose --output-format stream-json` **scoped to OpsReview
  only.** Mechanism (the ops `Type` is dropped before the spawner today —
  `spawnOps`/`SpawnRuntime`/`Spawn`/`BuildArgs` take no `Type`): add a
  **review-scoped spawner** — a second `ClaudeOpsSpawner` built with stream-json
  args + stream-aware output — held as a new field on `Spawner`, and have
  `run()` select it when `opsType == OpsReview`. `run()` already has the `Type`
  (it computes `effectiveTimeout(opsType)`), so selection happens there with
  **no change to the `BatchSpawner`/`ReasoningBatchSpawner`/`RuntimeBatchSpawner`/
  `RuntimeSpawnerRouter` signatures** and no per-call `Type` threading. The
  shared `buildClaudeOpsArgs` stays plain; merge/decompose/escalation keep their
  spawner and parsers untouched. Reuse `pkg/worker.ParseStreamEvent` to extract
  the assistant's final text from the `result` event
  (`Activity{Kind: ActivityResult, Text}`), then feed **that text** to
  `parseReviewOutput`; keep a text-scan fallback.
- **B3.** `ops.go:waitForProcess` — keep the 35-min ceiling, **add an
  idle-watchdog gated to OpsReview** that reads `LastOutputAt()` (B0) and kills
  early with a distinct "review wedged (no output for Nm)" error. Gate it per
  type (mirroring the existing per-type `Timeout()`): a zero/disabled idle
  threshold for non-review ops, because only the streaming review path updates
  `LastOutputAt` — a shared watchdog would false-kill plain-output
  merge/decompose. **Steady streaming must NOT trigger it** — only a true gap.
  Idle threshold configurable (like `--ops-review-timeout`).

### Acceptance tests (machine-verifiable)

- **B0/B1/B2 boundary:** feed a recorded real stream-json review transcript
  through `opsProcess.Output()` → `parseReviewOutput` → dispatcher
  `handleReviewResult`; assert `VerdictApproved`/`VerdictRejected`, **not**
  `VerdictFailed`. (Catches the NDJSON-breaks-final-line-scan regression.)
- **B3 idle-watchdog (time-driven):** a `Process` fake that emits lines then
  stalls → asserts the distinct "wedged" error before the 35-min ceiling; a fake
  that streams steadily for >threshold → asserts **no** kill.
- **Regression:** `parseMergeOutput`/`parseDecomposeOutput` still parse correctly
  (their path is unchanged — proves B2 scoping held).
- Cmd (epic acceptance): `go test ./pkg/ops/... ./pkg/dispatcher/... -count=1`
  Assert: exit 0 with the above tests present and green.

### Deferred — B4 (tracked as follow-up bead, NOT built now)

Saturation-aware review dispatch (soft-gate review spawn on concurrent
quality-gate load, `pkg/dispatcher`/`pkg/worker`). Deferred: A3 covers
serialization operator-side, and B1-B3 fix the no-output/hard-kill problem
durably. Revisit if B1-B3 prove insufficient. Risk if built prematurely:
dispatcher blast radius + review starvation.

## Premortems (risks accepted / mitigated)

| Risk | Severity | Mitigation |
|---|---|---|
| stream-json schema drift across claude versions | real | Parse defensively; keep text `VERDICT:` scan fallback (B2) |
| Idle-watchdog false-positive on a long silent think | real | Verified `thinking_tokens` + assistant deltas stream → real work emits output; idle threshold ≥120-180s |
| "Streaming changes the output contract" | paper tiger | stream-json is transport; final verdict still emitted, parsed from `result` |
| Skill is just markdown — operator may not follow it | real | **Part A is advisory-only / unverifiable by an automated gate.** Part B enforces the same behavior durably in code; A is necessary-not-sufficient |
| Saturation still blows even a bigger budget | real | A3 serialize + A4 bounded reading reduce review cost, not just raise the ceiling |
| Global stream-json switch breaks merge/decompose/escalation parsers | critical (adversarial) | B2 scopes stream-json to OpsReview **only**; regression test proves merge/decompose unchanged |
| B1 idle signal has no consumer (interface boundary) | critical (adversarial) | B0 extends `Process` with `LastOutputAt()`; B3 reads it; B0 blocks B1/B3 |
| stream-json final line is a JSON `result` event → `parseReviewOutput` returns `VerdictFailed` | critical (adversarial) | B2 extracts result-event text via `ParseStreamEvent` first, then parses; boundary integration test asserts `VerdictApproved` |
| Idle-watchdog false-kill during long final tool exec (acceptance command) | important (adversarial) | Measure max inter-event gap on a representative multi-package review **before** fixing threshold; make it configurable; ≥120-180s floor |

**Load-bearing assumption (VERIFIED):** `claude -p --output-format stream-json
--verbose` emits incrementally — and `pkg/worker` already depends on this in
production, so B1/B2/B3 reuse a proven path rather than a new bet.

## Sequencing & blast radius

1. **Part A** (skill markdown) — unblocks the P0 review path, no oro code risk, reversible.
2. **Part B** (`pkg/ops` Go) — durable; TDD, decomposed into beads. Dependency
   order: **B0 → B1 → B2**, **B0 → B3** (B0 unblocks both; B2 needs B1's stream
   infra). Blast radius confined to `pkg/ops` (B2 adds a review-scoped spawner
   field + `run()` selection by `Type`; no `BatchSpawner` signature change, so
   merge/decompose/escalation and their fakes are untouched). B0 changes the
   `Process` interface (one method `LastOutputAt()`) — all implementers/fakes
   update. Output-parsing change is medium-reversibility (text-scan fallback).

## Out of scope

- B4 saturation-aware dispatch (deferred).
- Changes to codex's operator tooling itself (we provide skill guidance; codex applies it).
- The `pkg/ops` codex (non-claude) spawner path — same streaming treatment is a follow-up if desired.
