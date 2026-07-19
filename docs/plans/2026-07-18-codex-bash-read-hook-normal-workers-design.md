# Codex Bash Read-Hook for Normal Workers

**Date:** 2026-07-18
**Status:** Validated — adversarial review R2 (fixed deny-field, allow-contract, str_replace scope, live-validation gate)
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
| Codex deny shape | `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}, "systemMessage":<summary>, "permissionDecisionReason":<summary>}` — summary in **`systemMessage`** (the proven surfaced field) **and** `permissionDecisionReason` for robustness | Match the only working in-tree Codex Bash deny (`destructive_command_guard.py:146-155` surfaces `systemMessage`, emits no `permissionDecisionReason`); the model must actually receive the summary |
| Codex allow / fail-open output | **Empty stdout** (no bytes), matching the sibling hook's proven allow contract — *not* `{}` | The Codex allow contract is "no decision emitted"; `{}` is unverified on the Bash surface and could stall every non-`cat` Bash call |
| Claude path | Unchanged (top-level `permissionDecision`, `{}` allow) | No regression to the working Claude route |
| Legacy Codex `view` arm | **Keep** `handleCodexView` in the binary (harmless dead arm); only the *config* `str_replace_based_edit_tool` matcher is removed | Deleting the arm breaks four existing tests (enumerated below) for no benefit |
| Config wiring | Add `oro-search-hook` to the shared `Bash` matcher, **last** in the chain (after `enforce_skills`, `destructive_command_guard`) | Safety guards evaluate first; summary only replaces an allowed read |
| Coverage | Shared config ⇒ normal **and** Oracle workers; verified via config test **plus a live `codex exec` validation** (a JSON-to-binary test is *not* end-to-end proof) | The explicit gap this epic closes |
| Fail-open invariant | Every error / unrecognized shape → allow (empty stdout on the Codex Bash path) | A broken hook must degrade to normal reads, never block work |

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
  allow. Otherwise `SummarizeFile` and emit the **Codex deny** shape:
  `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":
  "deny"}, "systemMessage":<summary>, "permissionDecisionReason":<summary>}`.
- Summarize error → fail open.

**Allow / fail-open output on the Codex Bash path = empty stdout** (write no
bytes), matching `destructive_command_guard.py`'s proven allow contract
(`main()` returns without writing when it does not deny). This requires
`HandleHook` to return an empty slice for the Codex Bash allow/bypass/fail-open
cases so `writeOut` emits nothing — the Claude and legacy-`view` arms keep
their `{}` allow. Do **not** emit `{}` on the Bash surface: it is unverified
and could be parsed as a malformed decision, stalling every non-`cat` Bash
call.

### 2. Config — `codexHookConfigBlock` (`cmd/oro/cmd_start.go`)

- Append `oro-search-hook` as the **last** command in the existing `Bash`
  PreToolUse matcher chain.
- **Remove only the config** `matcher = "str_replace_based_edit_tool"` entry
  (dead surface). Do **not** touch the binary's `handleCodexView` arm.
- `installCodexHookConfig` already rewrites the managed block idempotently,
  so existing users pick up the new chain on their next `oro start` with no
  migration. (This is why the coverage extends to *already-installed* normal
  workers, not just fresh installs.)

Existing tests that assert the legacy `str_replace`/`view` surface — which we
therefore **keep passing unchanged** by retaining the binary arm and only
touching the config matcher: `cmd/oro/end_to_end_codex_test.go:296-306`,
`cmd/oro-search-hook/parity_test.go:41-56`,
`cmd/oro-search-hook/main_test.go:332-365,429-430`,
`assets/hooks/test_parity.py:77-82`. `cmd_start_test.go:207`
(`TestCodexHookConfigBlockReplacement`) only asserts `oro-search-hook` is
present, which the `Bash` matcher still satisfies — it does not break.

### 3. Verification

- `TestHandleCodexBashRead` (binary): a large-code `cat` → deny with
  **`systemMessage` present and non-empty** (assert this field, not only
  `permissionDecisionReason`) in the `hookSpecificOutput` deny shape;
  `sed -n`, `head`, `tail`, `rg`, chained, redirected, substituted,
  multi-file, malformed, and non-`Bash` events → **empty stdout** (assert
  zero bytes, the Codex allow contract); small/test/non-code `cat` → empty
  stdout; Claude `Read` and legacy `view` outputs unchanged (`{}` / top-level
  deny).
- `TestCodexHookConfigBashMatcherIncludesSearchHook` (config): generated
  block wires `oro-search-hook` on the `Bash` matcher **and** no longer emits
  a `str_replace_based_edit_tool` matcher.
- **Live-`codex exec` validation (epic acceptance gate).** A JSON-to-binary
  test is coverage-identical to the unit test and proves nothing about the
  consumer. Instead, gate epic acceptance on a real check: run a normal
  worker (or a minimal `codex exec` harness) against the installed
  `$CODEX_HOME/config.toml`, have it `cat` a large code file, and confirm
  (a) the read is intercepted and (b) the **summary reaches the model**
  (whichever field Codex surfaces). If a live Codex CLI is unavailable in CI,
  this is a documented manual validation step recorded in the epic's closing
  notes — explicitly **not** claimed as satisfied by the binary tests.

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
  *Mitigation:* conform to the proven sibling contract — **empty stdout** on
  allow — rather than `{}`; fail-open default; hook is last in the chain; the
  live-`codex exec` validation exercises a real non-`cat` Bash call.
- **Elephant — the summary never reaches the model.** Deny may suppress the
  read but drop the field the model reads. The in-tree
  `destructive_command_guard.py:146-155` proves Codex surfaces
  **`systemMessage`**, not `permissionDecisionReason`. *Mitigation:* emit the
  summary in `systemMessage` (proven) **and** `permissionDecisionReason`
  (belt-and-braces); the binary test asserts `systemMessage`; the live
  validation confirms the model actually receives it.
- **Paper tiger — latency.** The hook `os.Stat`s and only summarizes on a
  large-file `cat`; negligible for the common case.

## Load-Bearing Assumption

Reduced by conforming to the only working in-tree Codex Bash deny
(`destructive_command_guard.py`): we emit `systemMessage` and empty-stdout
allow rather than guessing. The **one** remaining assumption is that
**`codex exec` in worker mode fires the managed `PreToolUse` `Bash` config
hooks at all** (not just interactive Codex). If Codex ignores config-file
hooks under `codex exec`, the approach is inert regardless of output shape.
This is precisely what the live-`codex exec` validation gate (Verification §3)
exists to prove — it must pass before the epic is accepted.

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

On `main`, quality gate green, with:
1. `TestHandleCodexBashRead` passing — asserts `systemMessage` on deny and
   **empty stdout** on every allow/bypass/fail-open case; Claude + legacy
   `view` outputs unchanged.
2. `TestCodexHookConfigBashMatcherIncludesSearchHook` passing — `Bash` matcher
   includes `oro-search-hook`; no `str_replace_based_edit_tool` matcher.
3. The four legacy `str_replace`/`view` test sites still green (binary arm
   retained).
4. **Live-`codex exec` validation** recorded: a real Codex worker's large-file
   `cat` is intercepted and the summary reaches the model. This gate — not the
   binary tests — is what certifies "normal Codex workers are covered." If a
   live Codex CLI is unavailable in CI, the manual validation result is
   recorded in the epic's closing notes.
