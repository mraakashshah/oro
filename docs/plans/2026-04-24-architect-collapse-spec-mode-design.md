# Collapse architect persona into Claude Code spec-mode

**Date**: 2026-04-24
**Status**: design — R1 review complete, R2 pending
**Scope**: architect-only (manager untouched); Claude Code runtime only (Codex is a follow-up)

## Context

Oro currently runs two always-on Claude Code panes: **architect** (planning) and **manager** (runtime operator). Each has a bespoke beacon, isolated config, role-specific env vars, and hook-enforced tool scoping.

The architect pane exists to enforce *workflow discipline* — keep planning separate from implementation so specs don't drift into code. But the discipline is actually delivered by the `/spec` skill chain (`brainstorming` → `adversarial-spec-review` → `beadcraft`), which runs inside whatever Claude Code session invokes it. The persistent architect pane adds no capability the skill doesn't already enforce.

In practice, humans drive `/spec` through their own Claude Code session — the dedicated architect pane is never the only path to planning, and having it sit idle burns tmux real estate, isolated-config state, hook complexity, and user cognitive load.

## Goal

Delete the architect persona as a standing presence. Preserve all planning discipline via the existing `/spec` skill chain running in any human's Claude Code session. Reduce Oro's persistent agent count from two to one.

## Non-goals

- **Manager is untouched.** Manager has live dispatcher-driven responsibilities (MERGE_CONFLICT / STUCK_WORKER / MERGE_COMPLETE / PRIORITY_CONTENTION / WORKER_CRASH). Collapsing manager is a separate spec.
- **Codex is out of scope.** `/spec` is a Claude Code skill (`.claude/skills/spec/SKILL.md`) loaded by Claude Code's session-start auto-loader. Codex CLI has no equivalent skill loader. Framing this change as "Claude or Codex" would overpromise. If/when someone wants `/spec` under Codex, that's a separate design (port the skill chain into a Codex-compatible prompt, or embed the chain in an ephemeral `oro spec` wrapper).
- **No new `oro spec` subcommand.** Humans run `/spec` in their own Claude Code session; oro is not involved in the planning conversation.
- **No spec-bead type.** Beads produced by `/spec` are normal beads; the swarm dispatches them unchanged.
- **No `/spec` skill changes** beyond absorbing two patterns from the architect beacon (AskUserQuestion 4-part, Engineering Cognitive Patterns) into the `brainstorming` skill.
- **No historical data migration.** `.beads/issues.jsonl` has ~20+ rows with `"created_by":"architect"`. These are immutable historical records and are deliberately left alone.

## Current state (what we're deleting)

From research (verified by grep):

**Go sources** (architect-touching code):
- `cmd/oro/architect.go` — `architectBeacon` constant, `ArchitectBeacon()`, `architectNudge`, `ArchitectNudge()`
- `cmd/oro/architect_test.go` — unit tests for beacon/nudge
- `cmd/oro/tmux.go` — 20+ sites (enumerated below under "Proposed design")
- `cmd/oro/manager.go` — 3 beacon-string references (lines 12, 16, 140)
- `cmd/oro/router.go` — `ArchitectLocal` routing branch
- `cmd/oro/cmd_init.go:1009` — generated settings.json registers `architect_router.py` as PreToolUse:Bash hook
- `cmd/oro/cmd_start.go:202, 313` — calls `sess.Create(ArchitectNudge(), ManagerNudge())`
- `cmd/oro/cmd_directive.go` — architect refs (audit during impl)
- `cmd/oro/cmd_cleanup.go` — architect refs (audit during impl)
- `pkg/dispatcher/health.go` — `ArchitectPane` field
- `pkg/dispatcher/pane_monitor.go` — architect pane polling
- `pkg/protocol/schema.go:77` — comment `-- 'architect' | 'manager'` for `pane_activity` table
- `pkg/memory/extract_llm.go` — architect ref (audit during impl; likely harmless text in prompt)
- `pkg/ops/review_prompt.go` — architect ref (audit during impl; likely harmless text)

**Assets** (source + staged copies; both must be deleted):
- `assets/beacons/architect.md` + `cmd/oro/_assets/beacons/architect.md`
- `assets/hooks/architect_router.py` + `cmd/oro/_assets/hooks/architect_router.py`
- `assets/hooks/notify_manager_on_bead_create.py` + `cmd/oro/_assets/hooks/notify_manager_on_bead_create.py`
- `assets/hooks/bd_create_notifier.py` + `cmd/oro/_assets/hooks/bd_create_notifier.py` (imports `from architect_router import send_to_manager_pane` — will ImportError without architect_router)

**Hooks that reference `ORO_ROLE=architect` and survive (but change behavior)**:
- `assets/hooks/session_start_extras.py` (+ staged) — currently loads beacon for `ORO_ROLE=architect`; must change to loud-warn on that value
- `assets/hooks/context_pct_writer.py` (+ staged) — currently writes context_pct for architect role; must also loud-warn or early-return

**Tests**:
- `cmd/oro/architect_test.go` — delete
- `tests/test_architect_router.py` — delete
- `tests/test_architect_router_new.py` — delete
- `assets/hooks/test_architect_router.py` (if exists) — delete
- Rewrite (not delete): `cmd/oro/tmux_test.go`, `cmd/oro/cmd_start_test.go`, `cmd/oro/start_test.go`, `cmd/oro/start_full_test.go`, `cmd/oro/router_test.go`, `cmd/oro/cmd_attach_test.go`, `cmd/oro/cmd_init_test.go`, `cmd/oro/cmd_global_oro_approach_test.go`, `cmd/oro/startup_log_test.go`, `cmd/oro/tmux_name_test.go` (if refs), `pkg/dispatcher/pane_monitor_test.go`, `pkg/dispatcher/pane_restarter_test.go`, `pkg/dispatcher/health_test.go`, `pkg/protocol/schema_test.go`, `tests/test_no_cd_guard.py` (architect role fixtures), `tests/test_session_start_extras.py` (architect-branch tests)

**Documentation** (stale after collapse; must update):
- `README.md` — lines 5, 184, 194, 311 describe "architect pane" + "architect designs, manager judges" model
- `.claude/skills/oro/SKILL.md:135` — references architect pane
- `.claude/skills/watching-oro/SKILL.md:58, 81`
- `.claude/skills/watching-oro/references/deep-observation.md:150, 154, 157, 162`
- `.claude/skills/watching-oro/scripts/oro-monitor.sh:54, 59, 98, 112` — monitor script captures `oro:0` (architect pane)
- `.claude/commands/restart-oro.md:69` (+ staged copies under `cmd/oro/_assets/commands/` and `assets/commands/`) — tells users to `tmux capture-pane -t oro:0`
- `.claude/skills/workflow-routing/SKILL.md:19` — references "architect" as workflow keyword; clarify this is the role concept, not the pane (no change needed)
- `ORO_AGENT.md` (if references exist) — audit during impl
- All staged skill mirrors under `assets/skills/*` and `cmd/oro/_assets/skills/*`

## Proposed design

### 1. `cmd/oro/tmux.go` — full site enumeration

Every line that hard-codes `"architect"`:

| Line | Function | Current behavior | New behavior |
|---|---|---|---|
| 91 | `isHealthy()` | iterates `[]string{"architect", "manager"}` | iterates `[]string{"manager"}` |
| 261 | doc comment | "two windows (architect + manager)" | "one window (manager)" |
| 267 | `Create()` signature | `Create(architectNudge, managerNudge string)` | `Create(managerNudge string)` |
| 283 | role-dir setup | iterates `[]string{"architect", "manager"}` | iterates `[]string{"manager"}` |
| 298 | `new-session` invocation | first window `-n architect`, runs `execEnvCmd("architect", ...)` | first window `-n manager`, runs `execEnvCmd("manager", ...)`; delete the subsequent `new-window -n manager` call |
| 315 | `launchAndNudgeAll` call | `launchAndNudgeAll(architectNudge, managerNudge)` | `launchAndNudgeAll(managerNudge)` |
| 332–342 | status-bar color switch | `set-hook after-select-window` with `if-shell` comparing `#{window_name}` to `architect` (green vs orange) | delete the hook (single window — no switching needed); set static manager color once |
| 384 | `launchAndNudgeAll` signature | `launchAndNudgeAll(architectNudge, managerNudge string)` | `launchAndNudgeAll(managerNudge string)`; remove `{"architect", architectNudge}` entry from the loop |
| 745 | `Kill()` | iterates `[]string{"architect", "manager"}` | iterates `[]string{"manager"}` |
| 867–876 | `AttachInteractive()` | calls `tmux select-window -t oro:architect` before attach; warns on failure | delete the `select-window` call entirely (single window makes it unnecessary); drop the architect-specific doc comment |
| 888–907 | `RegisterPaneDiedHooks()` | registers pane-died hooks for both architect and manager | register hook only on manager |
| 914–942 | `buildPaneDiedHook()` | computes `survivingRole` as "the other pane"; sends `[ORO-DISPATCH] PANE_RESPAWNED` via `send-keys` to survivor | delete the `survivingRole`/`send-keys` arm entirely; PANE_RESPAWNED notifications go via dispatcher UDS log only (no peer pane to notify) |
| 963–979 | `CleanupPaneDiedHooks()` | unregisters hooks on both panes | unregister only on manager |
| 990 | doc comment | "returns a feedback message to display to the architect" | "returns a feedback message to display to the manager" (or delete) |

### 2. `cmd/oro/manager.go` — beacon rewrite

Every architect reference in the manager beacon:

| Line | Current | New |
|---|---|---|
| 5 | comment | (leave — "architecture spec" is unrelated) |
| 12 | "report status to the human architect" | "report status to the human operator" |
| 16 | "**Architect** (pane 0) — the human operator. They set direction, approve priorities, and answer questions." | "**Human operator** — drives direction via `bd` and ad-hoc Claude Code sessions. Sets priorities and answers questions from the swarm." |
| 140 | "Everything without the `[ORO-DISPATCH]` prefix is human input. Treat it as a directive from the architect." | "Everything without the `[ORO-DISPATCH]` prefix is human input. Treat it as a directive from the human operator." |

Acceptance: `grep -c "architect" cmd/oro/manager.go` returns 0 (or 1 if line 5's architecture-spec comment is kept).

### 3. `cmd/oro/cmd_init.go:1009`

Delete the line:
```go
{Type: "command", Command: py("architect_router.py")},
```
Update `cmd_init_test.go` assertions (new `oro init` output should not register `architect_router.py` in the PreToolUse:Bash hook list).

### 4. `cmd/oro/cmd_start.go:202, 313`

`Create(ArchitectNudge(), ManagerNudge())` → `Create(ManagerNudge())`. These are the only callers in non-test code.

### 5. Change sequencing (avoiding broken-build mid-sequence)

The R1 review flagged that deleting `architect.go` breaks `cmd_start.go:202` immediately (`ArchitectNudge()` undefined). The fix is atomicity: **ship the full collapse as a single reviewable commit set**, not one-file-per-bead. Breakdown must respect this. Proposed shape (see beadcraft):

1. One bead for the "Go core" deletion: touches `architect.go`, `architect_test.go`, `tmux.go`, `manager.go`, `cmd_start.go`, `cmd_init.go`, `router.go`, `cmd_directive.go`, `cmd_cleanup.go`, plus every Go test that references architect. Builds must be green at HEAD after this bead lands.
2. Parallel-safe beads (depend on #1 only for grep-cleanliness):
   - Hook + asset deletion (Python + markdown + staged mirrors)
   - Dispatcher state deletion (`health.go`, `pane_monitor.go`)
   - Protocol schema update (`schema.go:77` comment + `schema_test.go`)
   - Documentation updates (README, skills, restart-oro)
   - Migration-detection bead (see D7)
   - `brainstorming` skill migration (AskUserQuestion + Engineering Cognitive Patterns)

### 6. What migrates (precise paths, no ambiguity)

Two patterns absorbed into `brainstorming` skill (`/Users/as21/.claude/skills/brainstorming/SKILL.md`):
- **AskUserQuestion 4-part structure** (Reground / Simplify / Recommend / Options) — from `architect.go:89-98`
- **Engineering Cognitive Patterns** (5 patterns, max 5 active) — from `architect.go:40-48`

Everything else from the architect beacon either duplicates existing skills (`beadcraft`, `explore`, `beads`) or is obsolete (System Map, Role framing). Delete, do not migrate.

The **anti-sycophancy rule** (`architect.go:126` + `manager.go:159-168`) is general agent hygiene. Option: fold into the user's global `~/.claude/CLAUDE.md`. Decision: leave this to the user; not part of this spec.

## Decisions & premortems

### D1: Delete architect pane entirely (no on-demand spawn)

**Chosen**: no persistent pane, no dispatcher-spawned ephemeral spec agent. `/spec` runs in the human's own Claude Code session.

- **Tiger**: muscle memory — users `oro start` expecting to type into pane 0. Mitigation: release note; manager pane still attaches on start.
- **Elephant**: loss of the `architect_router.py` "git mutations blocked" guardrail. Mitigation: `/spec` skill commits only the design doc; humans review diffs normally; no regression vs. any dev session they already run.
- **Paper tiger**: "planning quality will drop without a dedicated agent." Quality lives in the skill chain, not the pane.

### D2: Delete `architect_router.py` (do not repurpose)

**Chosen**: delete outright. The hook's entire purpose was enforcing role scope for a pane that no longer exists.

- **Tiger**: some project may register the hook via `settings.local.json` or `cmd_init.go`. Mitigation: delete registration at `cmd_init.go:1009` + grep `settings.*.json` for stale references.
- **Elephant**: future "spec mode" guard wanted back. Mitigation: git history preserves it.

### D3: Keep manager pane as-is; single-window tmux

**Chosen**: `oro start` creates exactly one tmux window (manager), user attaches there. No flag changes; no new subcommands.

- **Tiger**: manager beacon references architect in 3 spots. Mitigation: surgical rewrite enumerated under "Proposed design § 2."
- **Elephant**: 16+ test callsites for `TmuxSession.Create`. Mitigation: signature change is mechanical; impl bead must update all callers atomically.

### D4: Treat `ORO_ROLE=architect` as a loud error — explicit behavior

Previously underspecified; now explicit:

- `assets/hooks/session_start_extras.py`:
  - If `ORO_ROLE == "architect"`: print a one-line warning to stderr (`[oro] ORO_ROLE=architect is no longer supported — this value was removed in <release>. See release notes.`) AND continue session startup normally with no beacon injection (session still works; user just gets a plain Claude Code session).
  - Exit code: 0 (do not break the session).
- `assets/hooks/context_pct_writer.py`:
  - If `ORO_ROLE == "architect"`: silently no-op (do not write `context_pct` to the now-orphaned `~/.oro/panes/architect/` dir). No warning (the SessionStart hook already warned).

Rationale: loud but non-fatal. A stale env var shouldn't brick the user's session, but they must see the signal to update their shell rc.

### D5: Leave `~/.claude/roles/architect/` orphaned on disk

**Chosen**: no migration script; release notes offer `rm -rf ~/.claude/roles/architect` as optional user cleanup.

- **Tiger**: cruft accumulates. Mitigation: idempotent from oro's POV.

### D6: Migrate two beacon patterns into `brainstorming` skill

**Chosen**: migrate AskUserQuestion 4-part + Engineering Cognitive Patterns into `brainstorming` skill. Delete everything else from the beacon.

- **Tiger**: `brainstorming` grows by ~30 lines. Mitigation: both are load-bearing and the skill already recommends "one question at a time" — formalizing format is net-positive.

### D7 (new): Detect and force-recreate pre-collapse tmux sessions on upgrade

R1 found that `TmuxSession.isHealthy()` currently checks for both panes. On upgrade, a running oro session from the previous version still has an architect pane — so `isHealthy()` returns true after the binary is updated, and `oro start` no-ops instead of recreating. The user keeps their broken two-pane session indefinitely.

**Chosen**: On `oro start`, detect the pre-collapse session shape (has an `:architect` window) and kill it before recreating. Print a user-visible one-liner: `[oro] Detected pre-collapse session layout — recreating with the new single-window layout.`

- **Tiger**: user has in-progress work in their architect pane when they run `oro start`. Mitigation: architect is conversational history only; no code state lives there. Manager pane recreates cleanly (it's stateless modulo the dispatcher).
- **Elephant**: detection logic could false-positive on unrelated sessions sharing the `oro` name. Mitigation: scope detection to sessions where both `oro:architect` and `oro:manager` exist (current oro-specific layout).

### D8 (new): README/docs truth sync

**Chosen**: update `README.md`, `.claude/skills/oro/`, `.claude/skills/watching-oro/`, `.claude/commands/restart-oro.md` in the same decomposition (parallel-safe bead). Stale docs are a silent regression.

## Migration path (per D7)

1. Ship deletion + migration-detection atomically.
2. On `oro start`:
   - If no existing session: create single-window manager layout.
   - If existing session has only `:manager`: healthy, no-op.
   - If existing session has `:architect`: kill session, recreate with new layout, print one-liner.
3. Release notes:
   - "Architect pane removed. `/spec` in any Claude Code session replaces it."
   - "`~/.claude/roles/architect/` is safe to delete manually."
   - "`ORO_ROLE=architect` in your shell rc should be removed; oro will warn but continue."

No data migration: beads, design docs, `.beads/issues.jsonl` historical rows (including `created_by=architect`), manager pane state — all unchanged.

## Testing plan

### Deletions
- `cmd/oro/architect_test.go`
- `tests/test_architect_router.py`
- `tests/test_architect_router_new.py`
- Any `test_notify_manager_on_bead_create.py` / `test_bd_create_notifier.py` if present

### Rewrites (non-exhaustive — grep in impl phase)
- `cmd/oro/tmux_test.go` — specifically: `TestAttachInteractiveFocusesArchitectPane` (delete entirely); `TestRespawnPane` subtests with `oro:architect` (update to manager); all `Create` signature call sites (~16); any assertion on env vars containing `architect`
- `cmd/oro/cmd_start_test.go` — Create call sites; startup assertions
- `cmd/oro/start_test.go`, `start_full_test.go` — Create call sites
- `cmd/oro/router_test.go` — delete `ArchitectLocal` branch tests
- `cmd/oro/cmd_attach_test.go` — lines 91, 117, 144 reference `oro:architect`
- `cmd/oro/cmd_init_test.go` — assert `architect_router.py` not in generated hook list
- `cmd/oro/cmd_global_oro_approach_test.go:314, 338` — remove architect fixtures
- `cmd/oro/startup_log_test.go:29, 47, 66` — remove "Architect ready" fixtures
- `cmd/oro/tmux_name_test.go` — if architect refs, remove
- `pkg/dispatcher/pane_monitor_test.go` — remove architect polling assertions
- `pkg/dispatcher/health_test.go` — remove `ArchitectPane` assertions
- `pkg/dispatcher/pane_restarter_test.go` — line 79 calls `r.Restart("architect")`; update
- `pkg/protocol/schema_test.go:70, 75, 81` — remove architect fixtures; update comment expectation
- `tests/test_no_cd_guard.py:152-283` — replace `ORO_ROLE=architect` in fixtures with `manager` or `worker`
- `tests/test_session_start_extras.py:220-294` — rewrite architect-branch tests to assert loud-warning behavior per D4 (not beacon-load success)

### New tests
- Integration test: `oro start` in an empty dir produces exactly one tmux window named `manager` (assert `tmux list-windows -t oro -F '#{window_name}'` returns `manager` and nothing else).
- Unit test: `session_start_extras.py` — `ORO_ROLE=architect` produces the documented stderr warning and exit code 0.
- Unit test: `context_pct_writer.py` — `ORO_ROLE=architect` silently no-ops.
- Unit test (D7): migration detection — when `tmux list-windows` shows both `architect` and `manager`, `Create()` kills the session first.

### Acceptance test (goes in the epic bead AC)

```
1. `go test ./...` passes with zero architect-referencing test failures.
2. `uv run pytest tests/ assets/hooks/` passes.
3. `grep -rn "architect" cmd/ pkg/ assets/hooks/ assets/beacons/ --include="*.go" --include="*.py" --include="*.md"` returns:
   - zero matches in Go production code paths (excluding historical handoffs in docs/plans/ and docs/handoffs/)
   - zero matches in Python hook sources (source + staged)
   - zero matches in asset beacons
4. `oro start` on a clean machine produces exactly one tmux window named `manager`.
5. `oro start` on a pre-collapse session layout (architect + manager windows) kills and recreates as single-window, printing the migration one-liner.
6. `oro attach` succeeds without stderr warnings about select-window.
7. Manager beacon contains zero occurrences of the word "architect" (or one, at line 5's unrelated "architecture spec" comment — acceptable).
```

## Follow-ups (explicitly out of scope)

- **Manager collapse evaluation.** Once architect-free, re-examine whether manager needs to be persistent or can become event-driven. Separate spec.
- **Codex `/spec` port.** If Codex users want planning discipline, port the skill chain into a Codex-compatible form (prompt template, wrapper command, or skill-loader equivalent). Separate spec.
- **Anti-sycophancy rule consolidation.** Consider folding into global `~/.claude/CLAUDE.md` or a new standalone skill. Out of scope here.

## Summary

Architect dies as a persona. `/spec` in any Claude Code session picks up the work. One fewer tmux window, one fewer beacon, one fewer router hook, one fewer isolated config dir, one fewer mental model. Planning discipline unchanged because it never lived in the pane — it lived in the skill chain, and the skill chain is untouched.

Concrete deletion surface: ~4 Go source files (plus partial edits to ~12 others), 4 Python hooks × 2 staged copies = 8 files, 1 asset beacon × 2 = 2 files, plus test rewrites across ~14 Go test files and ~2 Python test files, plus doc updates across README + 4 skills + 1 slash command. Full collapse ships atomically to avoid broken-build mid-sequence.
