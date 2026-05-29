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
| `pkg/codesearch/claude_spawner.go:60` | Precedent: `--output-format json` already used |
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

- **B1.** `exec_spawner.go` — stream stdout incrementally (`os.Pipe` +
  `bufio.Scanner`) instead of buffer-then-read-post-`Wait()`; accumulate full
  output **and** track a last-output timestamp.
- **B2.** `buildClaudeOpsArgs` — add `--output-format stream-json --verbose`;
  parse the `result` event for the terminal `VERDICT`, with a **fallback** to
  the existing text `VERDICT:` scan for robustness.
- **B3.** `ops.go:waitForProcess` — keep the 35-min ceiling, **add an
  idle-watchdog** that kills early with a distinct "review wedged (no output for
  Nm)" error, so a truly hung review fails fast instead of burning 35 min.

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
| Skill is just markdown — operator may not follow it | real | Part B enforces the same behavior durably in code; A is necessary-not-sufficient |
| Saturation still blows even a bigger budget | real | A3 serialize + A4 bounded reading reduce review cost, not just raise the ceiling |

**Load-bearing assumption (VERIFIED):** `claude -p --output-format stream-json
--verbose` emits incrementally.

## Sequencing & blast radius

1. **Part A** (skill markdown) — unblocks the P0 review path, no oro code risk, reversible.
2. **Part B** (`pkg/ops` Go) — durable; TDD, decomposed into beads. Blast radius confined to `pkg/ops`; B2 output-parsing change is medium-reversibility (behind the spawner interface, with fallback).

## Out of scope

- B4 saturation-aware dispatch (deferred).
- Changes to codex's operator tooling itself (we provide skill guidance; codex applies it).
- The `pkg/ops` codex (non-claude) spawner path — same streaming treatment is a follow-up if desired.
