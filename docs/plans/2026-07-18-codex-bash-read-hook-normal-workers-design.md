# Codex Bash Read-Hook for Normal Workers

**Date:** 2026-07-18
**Status:** Draft — pending adversarial review
**Priority:** P0

## Goal

Make `oro-search-hook` actually intercept file reads for **all** Codex
workers — normal work beads and Oracles alike — by recognizing the read
surface current Codex CLI actually uses (`Bash`), and prove the coverage
with a test that exercises a *normal* (non-Oracle) worker path.

Today the token-saving read hook works on Claude but is **dead on Codex**:
it is wired to the obsolete `str_replace_based_edit_tool` matcher while
current Codex reads files with `Bash` (`cat`, `sed`, …). Every Codex worker —
not just Oracles — loses the 95% context saving on large-file reads.

## Problem

Three facts, verified in-tree:

1. **The hook only understands two shapes.** `cmd/oro-search-hook/main.go:97`
   dispatches on `tool_name`: `Read` (Claude) and
   `str_replace_based_edit_tool` (legacy Codex `view`). Current Codex CLI does
   not emit `str_replace_based_edit_tool` for reads — it runs `Bash`.
2. **The hook is wired to the dead surface.** `cmd/oro/cmd_start.go:701`
   registers `oro-search-hook` under `matcher = "str_replace_based_edit_tool"`.
   The live `Bash` matcher (`cmd_start.go:700`) runs only `enforce_skills.py`
   and `destructive_command_guard.py`. So a Codex `cat big_file.go` sails
   through with no summarization.
3. **The config is shared, but the fix is only tasked for Oracles.**
   `installCodexHookConfig` (`cmd_start.go:602`) writes **one** managed
   `[hooks]` block into `$CODEX_HOME/config.toml`, consumed by every Codex
   session. Fixing `codexHookConfigBlock` therefore fixes normal and Oracle
   workers simultaneously — yet the only tasks that touch it (`oro-bw46`,
   `oro-9s27`, `oro-qa09`) are children of the Oracle epic `oro-9nqr`,
   framed and tested Oracle-only. Nothing guarantees or verifies that a
   *normal* Codex worker gets the hook.

By contrast, normal **Claude** workers are already covered:
`buildClaudeArgsWithReasoning` passes `--settings …/settings.json`
(`pkg/worker/worker.go:1920`) and that file wires `Read → oro-search-hook`
(`cmd/oro/cmd_init.go:977`). This design brings Codex to the same baseline.

## Prior Art and Existing Contracts

- **Origin of the paradigm:** `parcadei/Continuous-Claude-v3`
  `.claude/hooks/src/tldr-read-enforcer.ts` — a PreToolUse Read interceptor
  that blocks broad code reads and returns an L1 AST summary, using
  `hookSpecificOutput.permissionDecision`. `oro-search-hook` is the Go port.
  That upstream never targeted Codex; the Codex surface is oro-native.
- **Binary already designed by `oro-bw46`.** Its acceptance names
  `func handleCodexBash(command string) []byte`, "a captured Bash cat of large
  code returns a structural summary with hookSpecificOutput PreToolUse/deny",
  and "sed -n, head, tail, rg, chained, redirected, substituted, malformed,
  and unsupported events bypass/fail open." This design **absorbs** that task.
- **Codex Bash event contract** (verified against the sibling hook
  `.codex/hooks/destructive_command_guard.py:129-155`):
  - Input: `{"tool_name":"Bash","tool_input":{"command":"<shell string>"}}`
  - Deny: `{"hookSpecificOutput":{"hookEventName":"PreToolUse",
    "permissionDecision":"deny"}, …}`
  - Allow: emit nothing / no deny decision.
- **Bypass policy is shared and pure.** `pkg/codesearch/bypass.go:ShouldBypass`
  (≤3KB, explicit offset/limit, `.claude/`, test, non-code) — reused verbatim;
  a `sed`/`head`/`view_range` partial read maps to "explicit offset".
- **Existing tests fix the Claude shape.** `oro-bw46` acceptance requires
  "Claude response shape unchanged" — this design must not regress it.

## Decisions

| Decision | Choice | Constraint it satisfies |
|---|---|---|
| Read surface | Recognize Codex `Bash` reads; retire `str_replace_based_edit_tool` matcher | Match the surface current Codex actually uses |
| Scope of interception | **Only** a single simple `cat [--] <path>` of a large code file | Small blast radius on the hot Bash path; YAGNI |
| Everything else | `sed`, `head`, `tail`, `rg`, pipes, chains, redirects, substitutions, multiple files, malformed → **fail open** | Never break or slow a legitimate shell command |
| Codex deny shape | `hookSpecificOutput{hookEventName:"PreToolUse", permissionDecision:"deny", permissionDecisionReason:<summary>}` | Codex-native contract; the model must receive the summary |
| Claude path | Unchanged (top-level `permissionDecision`) | No regression to the working Claude route |
| Config wiring | Add `oro-search-hook` to the shared `Bash` matcher, **last** in the chain (after `enforce_skills`, `destructive_command_guard`) | Safety guards evaluate first; summary only replaces an allowed read |
| Coverage | Shared config ⇒ normal **and** Oracle workers; verified via a normal-worker test | The explicit gap this epic closes |
| Fail-open invariant | Every error / unrecognized shape → allow | A broken hook must degrade to normal reads, never block work |

## Consultation Record

Topology decision confirmed by the user 2026-07-18: **own the Codex Bash
read-hook end-to-end for all workers, absorbing `oro-bw46` and superseding
the redundant Oracle-only Codex tasks.** Assumption ledger drained below.

| Forcing question | Answer |
|---|---|
| Real problem | Every Codex worker silently loses the read-hook's context savings because the hook targets a read surface Codex no longer uses; the Oracle epic only fixes this incidentally and never tests the normal path. |
| Status quo | Normal Codex workers read whole large files raw (3k–20k tokens each). No workaround; the `str_replace` matcher never fires. |
| Desperate specificity | A normal `oro work` bead running on the Codex runtime that does `cat pkg/dispatcher/dispatcher.go` — today: full 10k-line dump into context; wanted: a structural summary. |
| Narrowest wedge | Wire the existing (soon-to-exist) `handleCodexBash` into the shared `Bash` matcher and drop the dead matcher — one config change fixes all Codex workers. |
| Do nothing | Codex workers stay context-inefficient; the Oracle epic may ship and *appear* to fix Codex while normal workers remain uncovered and untested. |
| Future-fit | Durable: the shared config becomes the single, tested source of Codex read-hook coverage for every role, not a per-role patch. |

## Design

### 1. Binary — `handleCodexBash(command string) []byte`

New dispatch arm in `HandleHook` for `tool_name == "Bash"`, reading
`tool_input.command`. Recognize **only** a lone `cat` reading one path:

- `shlex`-split the command (Go equivalent). If it is not exactly
  `cat <path>` or `cat -- <path>` (optionally a leading absolute/relative
  path arg), **fail open**.
- Reject anything with pipes `|`, chains `&&`/`;`/`||`, redirects `>`/`<`,
  command substitution `$(…)`/backticks, globs, multiple file args, or flags
  other than `--` → fail open. (Ambiguous shell semantics we won't reason about.)
- `os.Stat` the path; on error fail open. Build `codesearch.ToolInput`
  (`FilePath`, `FileSize`, `Offset:0`) and run `ShouldBypass`. If bypassed,
  allow. Otherwise `SummarizeFile` and emit the **Codex deny** shape.
- Summarize error → fail open.

Allow output for the Codex path = the same allow contract Codex expects
(no deny decision). This is a **load-bearing assumption** (see below).

### 2. Config — `codexHookConfigBlock` (`cmd/oro/cmd_start.go`)

- Append `oro-search-hook` as the **last** command in the existing `Bash`
  PreToolUse matcher chain.
- **Remove** the standalone `matcher = "str_replace_based_edit_tool"` entry
  (dead surface).
- `installCodexHookConfig` already rewrites the managed block idempotently,
  so upgrades pick up the new chain with no migration.

### 3. Verification

- `TestHandleCodexBashRead` (binary): a large-code `cat` → deny+summary in
  `hookSpecificOutput` shape; `sed -n`, `head`, `tail`, `rg`, chained,
  redirected, substituted, multi-file, malformed, and non-`Bash` events all
  allow/fail-open; small/test/non-code `cat` bypasses; Claude `Read`
  unchanged.
- `TestCodexHookConfigBashMatcherIncludesSearchHook` (config): generated
  block wires `oro-search-hook` on the `Bash` matcher **and** no longer emits
  a `str_replace_based_edit_tool` matcher.
- **Normal-worker integration** (the explicit gap): drive a representative
  normal-worker Codex `Bash` PreToolUse event JSON through the installed hook
  binary and assert the deny+summary — proving coverage without an Oracle in
  the loop.

## Alternatives Considered

- **A. Leave it to the Oracle epic (`oro-bw46`/`oro-9s27`).** Rejected: those
  are P1, unstarted, Oracle-framed, and never test the normal path. Coverage
  would be accidental and unverified.
- **B. Broad Bash read parsing (`sed`/`head`/`tail`/`rg`/pipes).** Rejected
  (YAGNI + blast radius): the marginal saving isn't worth reasoning about
  arbitrary shell on the hot path. Narrow `cat`-only is the safe wedge; widen
  later if measured.
- **C. Custom Codex read tool instead of Bash.** Rejected: we don't control
  Codex's tool surface; Bash is what it emits.

## Premortem

- **Tiger — false-positive denies break legit commands.** A mis-parsed
  compound command gets denied, stalling a worker. *Mitigation:* intercept
  only an unambiguous lone `cat`; every other shape fails open; fuzz-ish table
  test of adversarial command strings.
- **Tiger — wrong allow contract stalls every Bash call.** If Codex doesn't
  read the allow output the way we assume, we could block all shell.
  *Mitigation:* verify the allow shape against a live Codex before rollout;
  fail-open default; hook is last in the chain.
- **Elephant — the summary never reaches the model.** Deny may suppress the
  read but drop `permissionDecisionReason`, so the model gets nothing.
  *Mitigation:* integration test asserts the reason/summary is present in the
  emitted JSON; confirm which field Codex surfaces to the model.
- **Paper tiger — latency.** The hook `os.Stat`s and only summarizes on a
  large-file `cat`; negligible for the common case.

## Load-Bearing Assumption

**Codex CLI treats a `PreToolUse` `Bash` hook that emits no deny decision as
"allow", and surfaces `hookSpecificOutput.permissionDecisionReason` (or
`systemMessage`) back to the model on deny.** If false, the allow path could
block shell or the summary could be invisible. Must be validated against a
live Codex CLI before this ships — the design is otherwise correct but inert.

## Relationship to Existing Tasks

- **Absorb `oro-bw46`** (binary `handleCodexBash`) into this epic — its
  acceptance is imported verbatim as the binary task here.
- **Supersede `oro-9s27`** ("Enforce Codex Oracle hook activation") — the
  shared-config wiring in this epic delivers Codex coverage for Oracles too.
- **Narrow `oro-qa09`** — keep its Oracle-profile-refresh scope; its
  `TestCodexHookConfigUsesBashMatcher` assertion is subsumed by this epic's
  config test (avoid double-owning the matcher wiring).
- **No change to Claude tasks** in `oro-9nqr`.

## Out of Scope

- Broad Bash read parsing beyond `cat` (revisit if telemetry shows misses).
- Claude response-shape changes.
- The read-only Oracle Claude `--settings` gap (`oro-2ncc`/`oro-wg9e`) — a
  separate, Claude-side issue.
- Interactive/ambient (non-worker) Codex parity beyond what the shared config
  already provides.

## Acceptance (epic)

On `main`, with the three tests above passing and the quality gate green:
a normal Codex worker's large-file `cat` is intercepted and replaced with a
structural summary via the shared `Bash` matcher, and the dead
`str_replace_based_edit_tool` matcher is gone.
