# Collapse architect persona into Claude Code spec-mode

**Date**: 2026-04-24
**Status**: design — R2 review complete, R3 pending
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
- **No `~/.oro/state.db::pane_activity` row cleanup.** Legacy rows with `pane='architect'` persist in the user's state DB. The schema tolerates any string; nothing in post-collapse code reads these rows (`ArchitectPane` field is removed). They are harmless historical records.
- **No `docs/handoffs/` rewrite.** Handoff YAMLs reference architect pane workflows historically; they're immutable session records.

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

**Assets** (three locations per file; all must be deleted):

Each hook exists in three places: the source under `assets/hooks/`, the binary-staged copy under `cmd/oro/_assets/hooks/` (produced by `make stage-assets`, embedded via `embed.go`), and the project-local dogfood copy under `.claude/hooks/` (used when a Claude Code session opens in the oro repo itself). Every asset-touching bead MUST touch all three and explicitly run `make stage-assets` so the staged tree doesn't drift.

- `assets/beacons/architect.md` + `cmd/oro/_assets/beacons/architect.md`
- `assets/hooks/architect_router.py` + `cmd/oro/_assets/hooks/architect_router.py` + `.claude/hooks/architect_router.py`
- `assets/hooks/notify_manager_on_bead_create.py` + `cmd/oro/_assets/hooks/notify_manager_on_bead_create.py` + `.claude/hooks/notify_manager_on_bead_create.py`
- `assets/hooks/bd_create_notifier.py` + `cmd/oro/_assets/hooks/bd_create_notifier.py` + `.claude/hooks/bd_create_notifier.py` (imports `from architect_router import send_to_manager_pane` — will ImportError without architect_router)
- `.claude/hooks/test_architect_router.py`, `.claude/hooks/test_architect_router_new.py`, `.claude/hooks/test_notify_manager_on_bead_create.py` — delete (project-local test copies)

**Project-local settings** (checked in, required update — distinct from the `cmd_init.go` template):
- `/Users/as21/codehouse/oro/.claude/settings.json:108` — PreToolUse:Bash hook entry `python3 .claude/hooks/architect_router.py` must be deleted. After the hook file is removed but this entry remains, every Bash tool call by any Claude Code session opened in the oro repo fails. This is the repo's own dogfood config and is separate from `cmd_init.go:1009` (which templates *future* projects).

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

### 5. Change sequencing — compile-safe sub-beads

The R1 review flagged broken-build risk on naive deletion. The R2 review flagged that bundling everything into one "Go core" bead (19 files) violates beadcraft's `>4 source / >1 test = too large` rule and risks triggering the dead-code no-op anti-pattern from MEMORY.md:oro-7nzy. Fix: decompose into **compile-safe sub-beads** where each bead leaves `go build ./...` + `go test ./...` green at HEAD.

Ordered decomposition:

1. **Add parallel `Create(managerNudge string)` signature**. Keep old `Create(architectNudge, managerNudge string)` as a thin wrapper calling the new one with `architectNudge=""` (ignored). New in-code uses are blocked until later beads. Touches: `cmd/oro/tmux.go`. Builds green.
2. **Migrate `Create` callers**. Update `cmd_start.go:202` and `cmd_start.go:313` to call the new signature; update unit tests that call `sess.Create(...)`. Touches: `cmd/oro/cmd_start.go`, `cmd/oro/tmux_test.go`, `cmd/oro/cmd_start_test.go`, `cmd/oro/start_test.go`, `cmd/oro/start_full_test.go`. Builds green.
3. **Delete old `Create` wrapper + `architect.go` + `architect_test.go`**. Remove the compat wrapper and the no-longer-referenced beacon/nudge. Touches: `cmd/oro/tmux.go` (wrapper removal), `cmd/oro/architect.go`, `cmd/oro/architect_test.go`. Builds green (no callers remain).
4. **Retrofit `tmux.go` hot spots** — `isHealthy`, `Kill`, `AttachInteractive`, status-bar hook, `launchAndNudgeAll`, `new-session` window name, `RegisterPaneDiedHooks`, `buildPaneDiedHook`, `CleanupPaneDiedHooks`. Co-edit `tmux_test.go` subtests. This is the single oversize bead and is marked **explicit oversize exception** per beadcraft policy — the functions are too tightly coupled to split further without intermediate broken-build states. Touches: `cmd/oro/tmux.go`, `cmd/oro/tmux_test.go`. Builds green.
5. **Delete `cmd_init.go:1009` + update `cmd_init_test.go`**. Generated settings no longer register `architect_router.py`. Touches: `cmd/oro/cmd_init.go`, `cmd/oro/cmd_init_test.go`. Builds green.
6. **Delete `router.go` ArchitectLocal branch + `router_test.go`**. Touches: `cmd/oro/router.go`, `cmd/oro/router_test.go`. Builds green.
7. **Update manager beacon** (§ 2 table). Touches: `cmd/oro/manager.go`. Builds green.
8. **Delete `ArchitectPane` from `pkg/dispatcher`**. Touches: `pkg/dispatcher/health.go`, `pkg/dispatcher/health_test.go`, `pkg/dispatcher/pane_monitor.go`, `pkg/dispatcher/pane_monitor_test.go`, `pkg/dispatcher/pane_restarter_test.go`. Verified (by grep) that no `cmd/oro` code reads the field; independent of other beads. Builds green.
9. **Update `pkg/protocol/schema.go:77` comment + `schema_test.go`**. Pure doc/test change. Builds green.
10. **Audit + clean `cmd_directive.go`, `cmd_cleanup.go`, `cmd_global_oro_approach_test.go`, `startup_log_test.go`, `tmux_name_test.go`, `cmd_attach_test.go`** for residual architect references. Builds green.
11. **Asset + hook deletion**. Touches: all Python hooks + beacons + `.claude/hooks/` + `.claude/settings.json` edit + `cmd_init.go` settings template + `make stage-assets` run. Independent of Go beads but must land before testing the full collapse.
12. **Hook behavior updates (D4)**. Touches: `assets/hooks/session_start_extras.py`, `assets/hooks/context_pct_writer.py`, their `_assets/` + `.claude/hooks/` copies, + associated Python tests. Per D4.
13. **Migration detection (D7)**. Adds `isPreCollapseLayout()` check at the top of `tmux.go:Create()` before the `Exists/isHealthy` early-return. Kills stale session + prints one-liner. Unit test + integration test. Touches: `cmd/oro/tmux.go`, `cmd/oro/tmux_test.go`.
14. **Documentation sync (D8)**. Touches: `README.md`, `.claude/skills/oro/SKILL.md`, `.claude/skills/watching-oro/SKILL.md`, `.claude/skills/watching-oro/references/deep-observation.md`, `.claude/skills/watching-oro/scripts/oro-monitor.sh`, `.claude/commands/restart-oro.md` + all staged mirrors under `cmd/oro/_assets/` and `assets/`.
15. **Brainstorming skill migration (D6)**. In-repo targets only (see § 6).

Beads 8, 9, 11, 14, 15 are truly parallel-safe (independent file sets). Beads 1-7 are strictly sequential. Bead 12 depends on bead 11. Bead 13 depends on bead 4.

**Anti-pattern guard** (explicit bead AC): no bead may pass QG via the dead-code no-op pattern (replacing a call with `_, _ = fn, arg`). Every deletion must be a true deletion — the grep on `ArchitectNudge|ArchitectBeacon|ArchitectPane|ArchitectLocal` in the final tree must return zero production-code hits.

### 6. What migrates (precise paths, in-repo only)

Two patterns absorbed into the `brainstorming` skill. **Migration targets are in-repo copies, not the user's private global.** The repo has three brainstorming copies that must stay in sync:
- `assets/skills/brainstorming/SKILL.md` — source of truth (edited first)
- `cmd/oro/_assets/skills/brainstorming/SKILL.md` — staged copy (produced by `make stage-assets`)
- `.claude/skills/brainstorming/SKILL.md` — project-local dogfood copy (kept in sync manually or via the staging pipeline)

The user's private `~/.claude/skills/brainstorming/SKILL.md` is outside this repo and out of scope.

Patterns to migrate:
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

### D4: Treat `ORO_ROLE=architect` as a loud warning — explicit behavior

Previously underspecified; now explicit per R2 feedback.

`assets/hooks/session_start_extras.py` when `ORO_ROLE == "architect"`:
- **Print to stderr**: `[oro] ORO_ROLE=architect is no longer supported — this value was removed in <release>. See release notes.`
- **Skip beacon injection** (legacy `architectBeacon` is gone; there is no beacon to load).
- **Skip `update_pane_activity("architect")`** — do not INSERT a new row into `~/.oro/state.db::pane_activity` with `pane='architect'`. (Legacy rows already there are harmless per Non-goals; we just stop adding new ones.)
- **Keep all other injections**: Superpowers auto-loader, auto-skills, handoff banner, stale-bead banner, situational context. The user should get a normal Claude Code session minus the role-specific bits.
- **Exit code**: 0. Do not brick the session.

`assets/hooks/context_pct_writer.py` when `ORO_ROLE == "architect"`:
- Silently no-op. No warning (SessionStart already warned). Do not write to `~/.oro/panes/architect/`.

`assets/hooks/pane_handoff_reminder.py`: no change required — it already early-returns on non-`manager` roles (and won't read the orphaned architect panes dir with any new behavior).

Rationale: loud but non-fatal. A stale env var shouldn't brick the user's session, but they must see the signal to update their shell rc. `update_pane_activity` gate prevents new legacy rows from contaminating the DB.

### D5: Leave `~/.claude/roles/architect/` orphaned on disk

**Chosen**: no migration script; release notes offer `rm -rf ~/.claude/roles/architect` as optional user cleanup.

- **Tiger**: cruft accumulates. Mitigation: idempotent from oro's POV.

### D6: Migrate two beacon patterns into `brainstorming` skill

**Chosen**: migrate AskUserQuestion 4-part + Engineering Cognitive Patterns into `brainstorming` skill. Delete everything else from the beacon.

- **Tiger**: `brainstorming` grows by ~30 lines. Mitigation: both are load-bearing and the skill already recommends "one question at a time" — formalizing format is net-positive.

### D7 (new): Detect and force-recreate pre-collapse tmux sessions on upgrade

R1 found that `TmuxSession.isHealthy()` currently checks for both panes. After the Go-core beads land, `isHealthy()` iterates only `{"manager"}`. A running oro session from the previous version still has both windows — the manager check alone returns healthy, so `Create()` at `tmux.go:270` early-returns without recreating. The user keeps their broken two-pane session indefinitely.

**Chosen**: Add `isPreCollapseLayout()` check at the **top of `tmux.go:Create()`**, before the `if s.Exists() && s.isHealthy() { return nil }` early-return. If detection fires, kill the session and fall through to the normal Create path. Print a user-visible one-liner: `[oro] Detected pre-collapse session layout — recreating with the new single-window layout.`

**Integration site (required for correctness)**: detection lives **inside `Create()`**, not in `cmd_start.go`. Both `oro start` code paths (`startSwarm` at `cmd_start.go:202` and `reconnectTmux` at `cmd_start.go:313`) route through `Create()`, so placing the check there covers both without duplication.

**Detection logic**: session exists with name `oro` AND `tmux list-windows -t oro -F '#{window_name}'` returns a window named `architect`. Scoping to this specific shape avoids false-positives on user-named unrelated sessions.

- **Tiger**: user has in-progress work in their architect pane when they run `oro start`. Mitigation: architect is conversational history only; no code state lives there. Manager pane recreates cleanly (it's stateless modulo the dispatcher).
- **Elephant**: detection only looks at window names; if a user renamed `architect` → something else, detection misses. Mitigation: acceptable — they've left the default layout.

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

### Rewrites (exhaustive — any additional hits found during impl must be added by the worker as part of the bead)
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
1. `make build && go test ./...` passes. Zero test failures whose stderr contains /architect|ArchitectPane|ArchitectNudge|ArchitectBeacon|ArchitectLocal|architect_router/.
2. `uv run pytest tests/ assets/hooks/ .claude/hooks/` passes.
3. `grep -rn "architect" cmd/ pkg/ assets/ .claude/hooks/ .claude/skills/ --include="*.go" --include="*.py" --include="*.md" --include="*.sh" --include="*.json" --exclude-dir=testdata` returns:
   - zero matches in Go production code paths
   - zero matches in Python hook sources (assets/hooks/, cmd/oro/_assets/hooks/, .claude/hooks/)
   - zero matches in asset beacons (assets/beacons/, cmd/oro/_assets/beacons/)
   - zero matches in skill docs or oro-monitor.sh
   - zero matches in .claude/settings.json
   - matches in .git-blame-ignore-revs, docs/plans/, docs/handoffs/, and testdata/ are excluded as historical/fixture data
4. `oro start` on a clean machine produces EXACTLY one tmux window, named `manager`. Assertion: `tmux list-windows -t oro -F '#{window_name}' | sort -u` equals the single line `manager`. No `architect` window present.
5. `oro start` on a pre-collapse session layout (architect + manager windows) kills the old session and recreates as single-window, printing the D7 migration one-liner to stdout. Verified by test setup that pre-creates the two-window layout via `tmux new-session ... -n architect && tmux new-window ... -n manager`.
6. `oro attach` succeeds without any stderr output containing `select-window` or `architect`.
7. Manager beacon: `grep -c "architect" cmd/oro/manager.go` returns 0 (or 1 if line 5's unrelated "architecture spec" comment is kept — acceptable; line 5 is the only allowable residual).
8. `.claude/settings.json` has no `architect_router.py` hook entry. Assertion: `grep -c architect_router .claude/settings.json` returns 0.
9. `make stage-assets` produces no diff after the asset beads land (staging is up to date).
10. D7 unit test: `oro start` on a fake session with both architect + manager windows triggers the kill+recreate path; on a fake session with only manager, no-ops.
11. D4 unit test: `ORO_ROLE=architect python3 assets/hooks/session_start_extras.py < /dev/null` prints the documented warning to stderr, exits 0, and does not attempt to load a beacon or call `update_pane_activity`.
12. Brainstorming skill: `.claude/skills/brainstorming/SKILL.md`, `assets/skills/brainstorming/SKILL.md`, and `cmd/oro/_assets/skills/brainstorming/SKILL.md` all contain the AskUserQuestion 4-part section and the Engineering Cognitive Patterns section. (String-match each heading.)
```

## Follow-ups (explicitly out of scope)

- **Manager collapse evaluation.** Once architect-free, re-examine whether manager needs to be persistent or can become event-driven. Separate spec.
- **Codex `/spec` port.** If Codex users want planning discipline, port the skill chain into a Codex-compatible form (prompt template, wrapper command, or skill-loader equivalent). Separate spec.
- **Anti-sycophancy rule consolidation.** Consider folding into global `~/.claude/CLAUDE.md` or a new standalone skill. Out of scope here.

## Summary

Architect dies as a persona. `/spec` in any Claude Code session picks up the work. One fewer tmux window, one fewer beacon, one fewer router hook, one fewer isolated config dir, one fewer mental model. Planning discipline unchanged because it never lived in the pane — it lived in the skill chain, and the skill chain is untouched.

Concrete deletion surface: ~4 Go source files (plus partial edits to ~15 others), 4 Python hooks × 3 copies (source + staged + `.claude/hooks/`) = 12 files, 1 asset beacon × 2 = 2 files, 1 project settings file (`.claude/settings.json`), plus test rewrites across ~14 Go test files and ~4 Python test files, plus doc updates across README + 4 skills + 1 slash command + 3 brainstorming skill copies. Full collapse ships as 15 compile-safe sub-beads per § 5, each leaving `go build` + `go test` green at HEAD.
