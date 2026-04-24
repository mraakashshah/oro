# Collapse architect persona into standard Claude/Codex in spec-mode

**Date**: 2026-04-24
**Status**: design — pending adversarial review
**Scope**: architect-only (manager untouched)

## Context

Oro currently runs two always-on Claude Code panes: **architect** (planning) and **manager** (runtime operator). Each has a bespoke beacon, isolated config, role-specific env vars, and hook-enforced tool scoping.

The architect pane exists to enforce *workflow discipline* — keep planning separate from implementation so specs don't drift into code. But the discipline is actually delivered by the `/spec` skill chain (`brainstorming` → `adversarial-spec-review` → `beadcraft`), which runs inside whatever Claude/Codex session invokes it. The persistent architect pane adds no capability the skill doesn't already enforce.

In practice, humans drive `/spec` through any Claude or Codex session they already have open — the dedicated architect pane is never the only path to planning, and having it sit idle burns tmux real estate, isolated-config state, and user cognitive load.

## Goal

Delete the architect persona as a standing presence. Preserve all planning discipline via the existing `/spec` skill chain. Reduce Oro's persistent agent count from two to one.

## Non-goals

- **Manager is untouched.** Manager has live dispatcher-driven responsibilities (MERGE_CONFLICT / STUCK_WORKER / MERGE_COMPLETE / PRIORITY_CONTENTION / WORKER_CRASH). Collapsing manager is a separate spec and requires answering "do these responsibilities move into dispatcher Go code, or become ephemeral ops-agent spawns?"
- **No new `oro spec` subcommand.** Humans talk to Claude/Codex outside the oro tmux layout; `/spec` runs where the human already is. Oro is not involved in the planning conversation; it only picks up the beads afterward.
- **No spec-bead type.** Beads produced by `/spec` are normal beads; the swarm dispatches them unchanged.
- **No `/spec` skill changes** beyond optionally absorbing the AskUserQuestion 4-part pattern (see "What migrates").

## Current state (what we're deleting)

From the research:

- **Beacon**: `cmd/oro/architect.go` (ArchitectBeacon, ArchitectNudge) + `assets/beacons/architect.md`
- **Tmux window**: created in `cmd/oro/tmux.go:267-327`; selected on attach at `tmux.go:869-875`; pane-died cross-wiring at `tmux.go:888-950`
- **Role config**: `~/.claude/roles/architect/` set up by `setupRoleConfigDir()` in `tmux.go:149-188`; pre-trust in `tmux.go:195-228`
- **Env vars**: `ORO_ROLE=architect`, `BD_ACTOR=architect`, `GIT_AUTHOR_NAME=architect` set in `execEnvCmd()` (`tmux.go:235-259`)
- **SessionStart hook**: `assets/hooks/session_start_extras.py:85-98` loads beacon based on ORO_ROLE
- **PreToolUse hook**: `assets/hooks/architect_router.py` — blocks `git add/commit/push`, `oro`, build commands when `ORO_ROLE=architect`
- **PostToolUse hook**: `notify_manager_on_bead_create.py` (or similar) — notifies manager when architect creates a bead
- **Dispatcher tracking**: `pkg/dispatcher/pane_monitor.go` polls architect pane's `context_pct`; `pkg/dispatcher/health.go` has `ArchitectPane` field; crash restart logic
- **Router**: `cmd/oro/router.go:19-45` has `ArchitectLocal` vs `ForwardToManager` branch
- **Tests**: `architect_test.go`, `test_architect_router.py`, `test_architect_router_new.py`, plus architect-touching assertions in `tmux_test.go`, `pane_monitor_test.go`, `health_test.go`, `start_test.go`, `start_full_test.go`, `cmd_start_test.go`, `router_test.go`, `cmd_attach_test.go`, `cmd_init_test.go`, `pane_restarter_test.go`

## Proposed design

### What goes away entirely

- `cmd/oro/architect.go` — deleted
- `cmd/oro/architect_test.go` — deleted
- `assets/beacons/architect.md` and its staged copy under `cmd/oro/_assets/beacons/` — deleted
- `assets/hooks/architect_router.py` (and `_assets/` staged copy) — deleted
- `tests/test_architect_router.py`, `tests/test_architect_router_new.py` — deleted
- `notify_manager_on_bead_create` hook — deleted (the architect is gone; humans writing beads directly via `bd create` in their own Claude/Codex session don't need to poke a manager pane — the dispatcher is already watching `bd ready`)
- `ORO_ROLE=architect` code path in `session_start_extras.py` — deleted
- Architect window creation in `tmux.go` — deleted
- Architect-specific pane-died cross-wiring — deleted (manager still gets its own crash handling)
- `ArchitectLocal` routing branch in `router.go` — deleted
- `ArchitectPane` field in `pkg/dispatcher/health.go` — deleted
- Architect polling in `pkg/dispatcher/pane_monitor.go` — deleted
- `~/.claude/roles/architect/` setup in `setupRoleConfigDir()` — deleted (only `manager` remains)

### What changes (but survives)

- **Tmux layout**: single-window session containing only the manager pane. On `oro start`, the user attaches to the manager window (the *only* window).
- **Manager beacon** (`cmd/oro/manager.go:16`): references to "Architect (pane 0) — the human operator" rewritten to "Human operator — drives direction via `bd` and ad-hoc Claude/Codex sessions." Remove any manager-side assumption that a peer architect pane exists.
- **ORO_ROLE env var**: kept (manager still uses it), but `architect` is no longer a valid value. Session-start hook treats `ORO_ROLE=architect` as an error (loud, not silent) to catch stale settings from upgrades.
- **`oro start` UX**: same command, one less window to navigate. No new flags; no `oro attach` subcommand changes (already attaches to the session).
- **Tests**: architect-specific test files deleted; mixed-concern tests (tmux_test, pane_monitor_test, health_test, start_test, etc.) rewritten to assert the single-pane layout.

### What migrates (vs. dies in place)

The architect beacon contains some patterns worth preserving. Audit:

| Content | Fate |
|---|---|
| Role framing ("senior systems architect") | Delete — persona-specific |
| System Map ("you are pane 0…") | Delete — obsolete once pane is gone |
| Core Skills (CODE READING, SPEC WRITING, etc.) | Delete — duplicative of `/spec`, `explore`, `beadcraft` |
| Engineering Cognitive Patterns (5 patterns) | **Migrate** — fold into `brainstorming` skill (section: "Design Heuristics") |
| Output Contract ("beads are primary") | Delete — already in `beadcraft` skill |
| Bead Craft rules | Delete — fully covered by `beadcraft` |
| Strategic Decomposition | Delete — covered by `beadcraft` |
| Research ("spawn subagents") | Delete — covered by `explore` skill |
| AskUserQuestion 4-part (Reground/Simplify/Recommend/Options) | **Migrate** — fold into `brainstorming` skill (section: "Question Format") |
| Beads CLI reference | Delete — in `beads` skill |
| Anti-sycophancy rule | **Migrate** — fold into global `~/.claude/CLAUDE.md` if user wants; otherwise delete (it's a general agent-hygiene rule, not architect-specific) |
| Anti-patterns list | Delete — covered elsewhere |

Net migration: two short sections into the `brainstorming` skill. Everything else sunsets.

### Data flow after collapse

```
Human → (any Claude or Codex session) → /spec skill chain → beads committed to bd
                                                               ↓
                                              dispatcher polls bd ready
                                                               ↓
                                     manager (persistent pane) + workers execute
```

No change to the post-bead execution path. The architect pane was purely a capture surface for human intent; humans now capture intent in their own agent session.

## Decisions & premortems

### D1: Delete architect pane entirely (not replace with on-demand spawn)

**Chosen**: no persistent pane, no dispatcher-spawned ephemeral spec agent. `/spec` runs in the human's own Claude/Codex session.

- **Tiger**: muscle memory — users `oro start` and expect to type into pane 0. Mitigation: release note; manager pane still attaches on start, so the session isn't empty.
- **Elephant**: loss of the `architect_router.py` "git mutations blocked" guardrail means a human could accidentally `git commit` code from a spec session. Mitigation: `/spec` skill commits only the design doc; humans review diffs at the normal boundaries; no regression vs. any other dev session they already run.
- **Paper tiger**: "planning quality will drop without a dedicated agent." False — quality comes from the skill chain (brainstorming → adversarial review → beadcraft), which is independent of which Claude instance runs it.

### D2: Delete `architect_router.py` (do not repurpose as a "spec-mode guard")

**Chosen**: delete outright. The hook's entire purpose was enforcing role scope for a pane that no longer exists.

- **Tiger**: some project has a workflow depending on the blocklist. Mitigation: grep for `architect_router` in project configs; remove hook registration from `settings.local.json`. The hook only fires when `ORO_ROLE=architect`, so deleting it is safe for any session that doesn't set that var.
- **Elephant**: future "spec mode" might want the blocklist back. Mitigation: it's preserved in git history; trivial to resurrect if we ever do.

### D3: Keep manager pane as-is; single-window tmux

**Chosen**: `oro start` creates exactly one tmux window (manager), user attaches there. No flag changes, no new `oro attach` semantics.

- **Tiger**: manager beacon references "Architect (pane 0)" and assumes a peer. Mitigation: surgical beacon rewrite listed in "What changes."
- **Elephant**: existing tests asserting two windows. Mitigation: rewrite those tests; part of the scope.
- **Paper tiger**: "users will be confused that there's no architect." Mitigation: `oro start` output mentions the change once; nothing forces the human to notice.

### D4: Treat `ORO_ROLE=architect` as an error (not a silent fallback)

**Chosen**: if a stale env var or leftover config sets `ORO_ROLE=architect`, `session_start_extras.py` prints a loud warning pointing to the release notes.

- **Tiger**: a user upgrades mid-session; the old env var carries over and the hook does nothing useful. Loud warning surfaces this immediately, no silent breakage.

### D5: Leave `~/.claude/roles/architect/` orphaned on disk

**Chosen**: do not ship a migration script to delete it. On new installs, nothing creates it. On upgraded installs, the directory becomes inert.

- **Tiger**: accumulated cruft in user homedir. Mitigation: release notes note that users can `rm -rf ~/.claude/roles/architect` if they want; idempotent from oro's perspective.
- **Paper tiger**: "stale directory will cause bugs." False — nothing reads it once the code paths referencing it are gone.

### D6: Migrate two beacon patterns into `brainstorming` skill

**Chosen**: migrate the AskUserQuestion 4-part pattern and the Engineering Cognitive Patterns list into the `brainstorming` skill. Delete everything else from the beacon.

- **Tiger**: `brainstorming` skill grows. Mitigation: both additions are short (under 30 lines combined); the skill already recommends "one question at a time" but doesn't prescribe the format — formalizing it is a net improvement.
- **Elephant**: global CLAUDE.md becomes the home for the anti-sycophancy rule. Decision deferred — user can pull that into their own CLAUDE.md if they want, or it just dies.

## Migration path

1. Ship the deletion in one atomic change set (so there's no intermediate state where the architect pane half-exists).
2. Release note: "Architect pane removed. `/spec` in any Claude/Codex session replaces it. `~/.claude/roles/architect/` is safe to delete manually."
3. Version bump to signal the break (user's call on major vs. minor).
4. No data migration required — beads, design docs, and the manager pane are unchanged.

## Testing plan

- **Delete**: `architect_test.go`, `test_architect_router.py`, `test_architect_router_new.py`.
- **Rewrite**: `tmux_test.go` (assert single-window layout, no architect window), `pane_monitor_test.go` / `health_test.go` (remove architect polling assertions), `start_test.go` / `start_full_test.go` / `cmd_start_test.go` (no architect window creation), `router_test.go` (no `ArchitectLocal` branch), `cmd_attach_test.go` (attach lands on manager), `cmd_init_test.go` (role dir setup for manager only), `pane_restarter_test.go`.
- **Add**: one integration test asserting `oro start` produces exactly one tmux window named `manager`.
- **Quality gate**: full `make test` + `make lint` green before merge.

## Open follow-ups (not in this spec)

- Manager collapse evaluation: once architect-free, re-examine whether manager needs to be persistent or can become event-driven. Separate spec.
- `/spec` skill polish: if the absorbed AskUserQuestion pattern proves useful, consider extracting it into its own micro-skill. Later decision.

## Summary

Architect dies as a persona. `/spec` in any Claude/Codex session picks up the work. One fewer tmux window, one fewer beacon, one fewer router hook, one fewer isolated config dir, one fewer mental model. Planning discipline unchanged because it never lived in the pane — it lived in the skill chain, and the skill chain is untouched.
