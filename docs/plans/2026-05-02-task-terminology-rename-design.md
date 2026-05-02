# Rename Beads To Tasks

**Date:** 2026-05-02
**Status:** Draft v6 — researched against current code; adversarial reviews rejected earlier drafts for missing hook/ops/asset/beacon/dependency/parity/guard/protocol/startup gates, addressed below; pending final review and beadcraft decomposition
**Goal:** Rename Oro's user-facing work-item vocabulary from "bead" to "task" without breaking the native SQLite runtime, existing data, migration audit trails, worker/dispatcher assignment protocol, or historical references.

## Context

Oro currently uses "bead" for every work item: CLI commands (`oro bead`), storage (`pkg/beadstore`, SQLite `beads` tables), protocol structs (`protocol.Bead`), worker branch names (`agent/<id>`), worker prompts, README copy, skills, hooks, event payloads, and migration runbooks.

The native beadstore migration has just completed through Phase 10. That makes this rename unusually risky: the word "bead" is now both product language and storage/protocol language. A mechanical global rename would touch thousands of references, break JSON contracts, invalidate migration docs, and make rollback hard.

## Research Summary

Files and references read:

- `cmd/oro/cmd_bead.go`: owns the public `oro bead` Cobra subtree. It contains user-facing command names, help text, flags, JSON error paths, and direct calls into `pkg/beadstore`.
- `cmd/oro/root.go` and `cmd/oro/cmd_help.go`: register `newBeadCmd()` and advertise `bead` under Workflow.
- `cmd/oro/architect.go` and `cmd/oro/manager.go`: active SessionStart role beacons and nudges tell architect/manager agents to orient with and create work through `oro bead ...`.
- `cmd/oro/tmux.go` and `cmd/oro/tmux_test.go`: startup nudge/beacon verification currently keys off `oro bead status` and must move with the manager nudge.
- `assets/hooks/session_start_extras.py`: active SessionStart hook loads role beacons and prepends its own Oro-specific context.
- `assets/beacons/architect.md`, `assets/beacons/manager.md`, `.claude/hooks/beacons/architect.md`, and `.claude/hooks/beacons/manager.md`: actual role beacon text injected by SessionStart, including active `oro bead ...` guidance.
- `cmd/oro/cmd_init.go`: installs embedded beacons into `$ORO_HOME/beacons`, which is the active path when `ORO_PROJECT` is set.
- `cmd/oro/cmd_work.go`: exposes `oro work <bead-id>` and loads/validates a `protocol.Bead`.
- `pkg/protocol/types.go`: defines `type Bead` and JSON fields. Some JSON keys are already legacy-shaped (`issue_type`, `parent`) and must not be casually churned.
- `pkg/protocol/tables.go`: assignment, event, escalation, and memory rows expose `bead_id` JSON fields.
- `pkg/protocol/schema.go`: SQLite schema creates `beads`, `bead_deps`, `bead_tags`, `bead_labels`, `bead_metadata`, `bead_notes`, and `beads_fts`.
- `pkg/beadstore/store.go` and `pkg/beadstore/sqlite.go`: the native store interface and implementation are named around beads.
- `pkg/dispatcher/dispatcher.go`: dispatcher state fields, assignment routing, counters, events, and spawn-for targeting all use bead terminology.
- `pkg/worker/prompt.go`: worker prompts instruct agents to execute one bead, decompose epics with `oro bead create`, and create blocker/handoff beads.
- `pkg/ops/ac_prompt.go`, `pkg/ops/decompose_prompt.go`, `pkg/ops/epic_fix_prompt.go`, and `pkg/ops/escalation_prompt.go`: ops prompts instruct agents and operators to inspect, create, update, and wire beads with `oro bead ...`.
- `assets/hooks/architect_router.py` and generated/mirrored hook copies: active command policy currently allows `oro bead ...` but would reject `oro task ...` in architect contexts.
- `assets/hooks/notify_manager_on_bead_create.py` and `assets/hooks/bd_create_notifier.py`: active notifications detect only `oro bead create` and tell managers to run `oro bead ready`.
- `pkg/protocol/constants.go`: worker branches use `agent/<id>`, not a bead-specific branch prefix.
- `docs/plans/2026-04-27-replatform-beads-spec.md`: explicitly made beads the native concept and storage seam during migration.
- `docs/plans/notes/bd-callsites.md`: documents `beadstore.Store` as the post-Phase-10 production seam.
- `README.md`: public docs define beads as work items and describe the worker lifecycle around beads.

Observed scope:

- `rg` found roughly 7,500 current "bead" references across code, tests, docs, assets, and scripts, excluding some generated/historical folders.
- The current public command is `oro bead`; there is no `oro task` command.
- Storage and protocol are stable and working after the native migration. Renaming them now has data-migration and compatibility costs unrelated to the user-facing terminology goal.

External prior art was skipped: this is an internal product/API terminology migration, not an algorithm or library-selection problem.

## Problem

"Bead" is now overloaded:

1. **Product language:** the human-facing concept should be "task".
2. **CLI language:** `oro bead` is unintuitive for new users; `oro task` is clearer.
3. **Storage/protocol language:** `beads` tables, `bead_id` fields, `protocol.Bead`, and `pkg/beadstore` are working persistence contracts.
4. **Historical language:** migration runbooks, old plans, and audit trails intentionally reference beads/bd/Dolt.

The rename needs to separate those layers instead of trying to erase every occurrence in one pass.

## Non-Goals

- No immediate SQLite table rename from `beads` to `tasks`.
- No immediate JSON field rename from `bead_id` to `task_id` in protocol/event/memory rows.
- No rewrite of historical runbooks, old design docs, migration audit logs, or closed work records.
- No deletion of `oro bead` in the first release after `oro task` lands.
- No change to task type values. `type=task|bug|epic|research|chore` remains exactly as today.
- No new work tracker. This is a terminology/API compatibility layer over the existing native SQLite store.
- No change to worker branch prefix. `protocol.BranchPrefix` remains `agent/` in this spec.

## Design Decision

Preferred design: **public alias first, internals later**.

Add `oro task` as the preferred public command tree. Keep `oro bead` as a hidden or clearly deprecated compatibility alias for at least one release cycle. The underlying implementation can initially call the same command constructors and store interfaces.

Then migrate prompts, docs, skills, hooks, and operator-facing output to say "task". After those pass, evaluate whether internal package/type/schema names are still worth changing. The default assumption is that storage/protocol names remain `bead*` until a separate compatibility spec proves the value exceeds the migration risk.

### Alternatives Considered

**A. Hard rename everything now**

Rename `bead` to `task` across packages, structs, tables, JSON, branches, docs, prompts, and tests in one project.

- Benefit: clean final vocabulary.
- Cost: enormous blast radius, table migration, JSON compatibility break, branch/worktree naming churn, and high chance of breaking the just-proven sqlite dispatcher/worker runtime.
- Premortem: a worker passes package tests but misses an event JSON field or migration report field; external scripts fail silently because `bead_id` disappeared. This is a high-severity tiger.

**B. Public alias first**

Add `oro task` and update operator/worker-facing text while preserving storage/protocol names.

- Benefit: users see the new vocabulary quickly; runtime state remains stable; `oro bead` continues to work for old scripts and workers.
- Cost: two terms coexist for a while.
- Premortem: docs can become inconsistent if old and new terms are mixed without a style guide. This is manageable with grep-based acceptance tests.

**C. Documentation-only rename**

Call work items tasks in README and prompts, but keep only `oro bead` in the CLI.

- Benefit: minimal code change.
- Cost: creates a worse mismatch: docs say "task", commands say "bead".
- Premortem: workers emit `oro task` commands that do not exist, or humans keep asking whether task and bead are different. This is a real product failure.

Decision: choose **B**.

## Premortem

```yaml
premortem:
  mode: quick
  context: "rename user-facing bead terminology to task"
  tigers:
    - risk: "Hard-renaming storage/protocol breaks native sqlite runtime and historical migration contracts."
      severity: high
      evidence: "pkg/protocol/schema.go creates beads/bead_* tables; pkg/protocol/tables.go emits bead_id JSON; docs/plans/notes/bd-callsites.md records beadstore.Store as the production seam."
      mitigation: "Do not rename schema, JSON fields, or pkg/beadstore in this spec."
    - risk: "Prompts/docs move to task before CLI exists, causing workers to emit invalid oro task commands."
      severity: high
      evidence: "pkg/worker/prompt.go currently prints command examples such as oro bead create and oro bead dep add."
      mitigation: "First implementation bead adds and tests oro task alias before any prompt/docs rewrite."
    - risk: "Two terms coexist indefinitely without an intentional boundary."
      severity: medium
      evidence: "README.md and worker prompts are public surfaces; protocol/storage names are internal compatibility surfaces."
      mitigation: "Add glossary and grep-based acceptance: current docs/prompts use task for new user-facing prose, while storage/protocol docs may retain bead."
  elephants:
    - risk: "The project name beadstore may remain forever even after the product says task."
      mitigation: "Accept for first release; revisit only after public task surfaces are stable."
  paper_tigers:
    - risk: "Existing IDs like oro-abc need to change."
      reason: "IDs are not bead-prefixed; existing hierarchical IDs are already neutral enough."
    - risk: "The type value task conflicts with the renamed object."
      reason: "The existing type vocabulary already uses task as one type among bug/epic/research/chore; docs can call the object a work item/task and the type a task-type leaf."
```

## Architecture

### 1. CLI compatibility layer

Add `newTaskCmdWithStore(store beadstore.Store) *cobra.Command`.

The command should expose the same subcommands as `oro bead`:

- `ready`
- `list`
- `show`
- `create`
- `update`
- `close`
- `reopen`
- `defer`
- `undefer`
- `blocked`
- `closed`
- `dep`
- `tag`
- `meta`
- `note`
- `search`
- `export`
- `import`
- `doctor`
- `status`
- `migrate-from-dolt` remains under `oro bead` only unless explicitly needed for migration operators.

Recommended first cut:

- Factor shared constructor logic into a helper that accepts `noun`/`plural`/`includeMigration`.
- Register `newTaskCmd()` in `cmd/oro/root.go`.
- Keep `newBeadCmd()` registered for compatibility.
- Update root categorized help to show `task` as preferred and `bead` as compatibility/legacy.

Acceptance must prove:

- `oro task create/show/update/close/ready/list/status` works against native sqlite.
- `oro bead ...` still works.
- Help lists `task` as the preferred command.

### 2. User-facing text migration

After `oro task` exists, update live user-facing text:

- Root help and command descriptions in `cmd/oro/cmd_help.go`, `cmd/oro/cmd_bead.go`, and `cmd/oro/cmd_work.go`.
- Active architect/manager nudge constants in `cmd/oro/architect.go` and `cmd/oro/manager.go`.
- Actual SessionStart role beacon assets under `assets/beacons/` and `.claude/hooks/beacons/`, plus the loader/context in `assets/hooks/session_start_extras.py`.
- Worker prompts in `pkg/worker/prompt.go`.
- Ops/review prompts that instruct agents to create or close work items.
- Assets and skills under `assets/skills/`, `.claude/skills/`, and generated `_assets` mirrors.
- Hook messages under `assets/hooks/` and `.claude/hooks/` where they display instructions to humans/agents.
- README current public sections.

Do not rewrite historical docs under `docs/plans/done/`, old runbooks, audit logs, migration reports, or closed-bead notes.

### 3. Protocol and storage compatibility

Keep these stable in this spec:

- `pkg/beadstore`
- `beads` and `bead_*` tables
- `beads_ready` and `beads_blocked` views
- `protocol.Bead`
- JSON fields `bead_id`, `issue_id`, `depends_on_id`, `issue_type`
- event names containing `bead`, unless a separate compatibility plan maps event aliases
- worker branch prefix `agent/<id>`
- `ORO_BEADSOURCE_MODE`

Rationale: these are persistence and observability contracts. A public noun rename does not require storage churn.

### 4. Glossary and operator guidance

Add a glossary to README:

- **Task:** preferred public term for an Oro work item.
- **Bead:** legacy/internal term still visible in storage, historical docs, compatibility CLI, and migration artifacts.
- **Task type:** the `type` field, whose values include `task`, `bug`, `epic`, `research`, and `chore`.

This prevents confusion while both words exist.

### 5. Future internal rename gate

A later spec may rename internals only if all of these are true:

- `oro task` has been stable for one release.
- Compatibility tests prove `oro bead` still aliases correctly or has an explicit removal plan.
- JSON/event consumers are inventoried.
- SQLite migration and rollback for table/view/trigger names are designed and tested.
- Native worker/dispatcher proof passes in sqlite mode after the migration.

## Adversarial Review Findings Addressed

The first two adversarial reviews rejected earlier drafts. The rejected points are now explicit gates:

- Epic acceptance must build a fresh temporary `oro` binary from the checked-out source before running smoke commands. A stale `./oro` binary is not proof.
- Prompt tests must include `pkg/ops`, not only `pkg/worker`.
- Hook policy and notification code must support `oro task create` before prompts tell architects to use it.
- `oro task migrate-from-dolt` must remain absent unless a future migration spec intentionally aliases it.
- `oro bead migrate-from-dolt --help` must remain available for migration/recovery operators.
- `oro task dep add/list/rm` must work because workers and ops prompts will make dependency wiring a primary task workflow.
- `oro task` must expose the same command tree as `oro bead` except `migrate-from-dolt`; smoke coverage alone is not enough.
- Generated asset mirrors must be verified after `make stage-assets`.
- `.claude/skills` and `.claude/commands` must be checked against their source assets, not only hooks.
- Actual SessionStart beacon assets and installed `$ORO_HOME/beacons` output must be task-primary; Go nudge constants alone are not enough.
- Worker branch terminology must refer to the actual `agent/<id>` prefix.
- The task CLI smoke must cover the common lifecycle, not only create/show.
- Active manager/architect SessionStart beacons and nudges must be task-primary and tested.
- Focused Go acceptance must use deliberate test names or a verified `-run` pattern that includes existing `TestCmdBead*`, `TestBead*`, task alias/parity tests, work/help terminology tests, beacon tests, hook tests, prompt tests, and terminology guard tests.
- README/current-operator docs must be checked directly by a guard script or test.
- Existing-data compatibility beyond a fresh sqlite smoke is an accepted risk for this spec because the storage schema remains unchanged; table/data migration belongs to a separate internal rename spec.
- Tmux startup/beacon verification must be updated and tested so `oro start` does not warn because it is still waiting for `oro bead status` after the manager nudge moves to `oro task status`.
- Storage/protocol compatibility must be guarded directly with `pkg/protocol` and `pkg/beadstore` focused tests plus cheap greps for `bead_id` JSON tags and `beads`/`bead_*` schema names.

## Acceptance Test

Epic acceptance should run against `main`:

```bash
tmp=$(mktemp -d) &&
make stage-assets &&
go build -o "$tmp/oro" ./cmd/oro &&
go test -list 'Test.*(CmdBead|Bead|Task|Work|Help|Prompt|Architect|Manager|Terminology)' ./cmd/oro ./pkg/worker ./pkg/dispatcher ./pkg/ops | tee "$tmp/test-list" &&
rg '^Test(CmdBead|Bead)' "$tmp/test-list" &&
rg '^TestTaskCommandAliasLifecycle$' "$tmp/test-list" &&
rg '^TestTaskCommandSubcommandParity$' "$tmp/test-list" &&
rg '^TestWorkCommandTaskTerminology$' "$tmp/test-list" &&
rg '^TestHelpTaskTerminology$' "$tmp/test-list" &&
rg '^Test.*(Architect|Manager).*(Task|Terminology)' "$tmp/test-list" &&
rg '^Test.*(Tmux|Start|Beacon|Nudge).*(Task|Terminology|Verification)' "$tmp/test-list" &&
rg '^Test.*Init.*Beacon.*Task' "$tmp/test-list" &&
rg '^Test.*Prompt.*TaskTerminology' "$tmp/test-list" &&
rg '^TestTaskTerminologyGuard' "$tmp/test-list" &&
go test ./cmd/oro ./pkg/worker ./pkg/dispatcher ./pkg/ops -run 'Test.*(CmdBead|Bead|Task|Work|Help|Prompt|Architect|Manager|Tmux|Start|Nudge|Beacon|Terminology|Verification)' -count=1 &&
go test ./pkg/protocol ./pkg/beadstore -run 'Test.*(Bead|Schema|Fields|JSON|Assignment|Event|Memory|Migrate|SQLiteStore|Dependency)' -count=1 &&
rg 'json:"bead_id' pkg/protocol/tables.go pkg/protocol/message.go cmd/oro/cmd_memories.go &&
rg 'CREATE TABLE IF NOT EXISTS beads|CREATE TABLE IF NOT EXISTS bead_' pkg/protocol/schema.go &&
python3 -m pytest tests/test_architect_router.py tests/test_architect_router_new.py tests/test_notify_manager_on_bead_create.py tests/test_session_start_extras.py tests/test_asset_mirrors.py &&
scripts/check-agent-asset-mirrors.sh &&
scripts/check-task-terminology.sh &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" task create --id oro-task-rename-smoke --title "Task rename smoke" --type task --priority 4 --acceptance "smoke" &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" task create --id oro-task-rename-blocker --title "Task rename blocker" --type task --priority 4 --acceptance "smoke" &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" task show oro-task-rename-smoke --json | jq -e '.id=="oro-task-rename-smoke"' &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" task dep add oro-task-rename-smoke oro-task-rename-blocker &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" task dep list oro-task-rename-smoke --json | jq -e 'map(.depends_on_id) | index("oro-task-rename-blocker")' &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" task dep rm oro-task-rename-smoke oro-task-rename-blocker &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" bead dep add oro-task-rename-smoke oro-task-rename-blocker &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" bead dep list oro-task-rename-smoke --json | jq -e 'map(.depends_on_id) | index("oro-task-rename-blocker")' &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" bead dep rm oro-task-rename-smoke oro-task-rename-blocker &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" task update oro-task-rename-smoke --status in_progress &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" task list --json | jq -e 'map(.id) | index("oro-task-rename-smoke")' &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" task ready --json | jq -e 'type=="array"' &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" task status &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" task close oro-task-rename-smoke --reason "task rename smoke complete" &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" bead show oro-task-rename-smoke --json | jq -e '.status=="closed"' &&
ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" bead migrate-from-dolt --help >/dev/null &&
! ORO_HOME="$tmp/home" ORO_DB_PATH="$tmp/state.db" ORO_BEADSOURCE_MODE=sqlite "$tmp/oro" task migrate-from-dolt --help &&
make stage-assets &&
git diff --exit-code -- cmd/oro/_assets assets .claude
```

Assert:

- command exits 0
- the focused Go `-run` pattern is auditable through `go test -list`, and the listed tests include bead compatibility, exact task alias lifecycle, exact task command parity, work/help terminology, beacon terminology, prompt terminology, and guardrail coverage
- `oro task` can create, show, update, list, report ready/status, close, and manage dependency add/list/rm for native sqlite tasks
- `oro task` command-tree parity with `oro bead` is tested, excluding `migrate-from-dolt`
- `oro bead` compatibility can show the same row after closure and still manage dependency add/list/rm
- `oro bead migrate-from-dolt --help` remains available
- `oro task migrate-from-dolt` is intentionally unavailable
- focused Go tests pass for CLI, worker prompts, dispatcher surfaces, ops prompts, and active manager/architect beacons
- tmux/startup beacon verification tests pass and no longer depend only on `oro bead status`
- protocol and beadstore compatibility tests pass, preserving `protocol.Bead`, `bead_id` JSON fields, and `beads`/`bead_*` schema names
- active hook tests pass for every `oro task` command used by architect role text and for `oro task create` notification behavior
- SessionStart role beacon tests prove injected architect/manager beacon output is task-primary
- generated assets are staged; `.claude/hooks`, `.claude/hooks/beacons`, `.claude/skills`, and `.claude/commands` mirror their source assets without drift
- installed/embedded `$ORO_HOME/beacons` output is tested as task-primary
- README/current docs terminology guard passes

## Implementation Phases

### Phase 1: Add `oro task` CLI alias

Add the new command tree and tests. Do not change prompts/docs yet.

Primary files:

- `cmd/oro/cmd_bead.go`
- `cmd/oro/root.go`
- `cmd/oro/cmd_help.go`
- `cmd/oro/cmd_bead_test.go` or new `cmd/oro/cmd_task_test.go`

Acceptance:

- `oro task create/show/update/list/ready/status/close` works in sqlite mode.
- `oro task dep add/list/rm` works in sqlite mode.
- `TestTaskCommandSubcommandParity` proves the task command tree matches the bead command tree except `migrate-from-dolt`.
- `oro bead show` remains compatible for a row created through `oro task`.
- `oro task migrate-from-dolt --help` exits nonzero; `oro bead migrate-from-dolt --help` exits zero.
- Root help advertises `task` as preferred and does not remove the compatibility `bead` command.

### Phase 2: Update direct CLI/user help text

Make `task` preferred in command help and `oro work` text while keeping `bead` compatibility wording where necessary.

Primary files:

- `cmd/oro/cmd_help.go`
- `cmd/oro/cmd_work.go`
- `cmd/oro/tmux.go`
- `cmd/oro/tmux_test.go`
- `cmd/oro/architect.go`
- `cmd/oro/architect_test.go`
- `cmd/oro/manager.go`
- `cmd/oro/manager_test.go`
- `assets/beacons/architect.md`
- `assets/beacons/manager.md`
- `.claude/hooks/beacons/architect.md`
- `.claude/hooks/beacons/manager.md`
- `assets/hooks/session_start_extras.py`
- `.claude/hooks/session_start_extras.py`
- `tests/test_session_start_extras.py`
- `cmd/oro/cmd_init.go` and `cmd/oro/cmd_init_test.go` for installed beacon assertions
- `cmd/oro/*_test.go` help-output tests

Acceptance:

- Root help and `oro work` text say task as the primary public term.
- Architect beacon/nudge uses `oro task create/show/dep add/status/ready` for normal work creation and orientation; any `oro bead` mention is explicitly legacy/internal.
- Manager beacon/nudge uses `oro task status/ready/blocked/create/show/close/dep/list` for backlog operations; dispatcher directive guidance remains unchanged.
- Existing `cmd/oro/architect_test.go` and `cmd/oro/manager_test.go` expectations are updated away from primary `oro bead ...` assertions.
- Tmux startup beacon verification matches task-primary nudge text or explicitly accepts both legacy and task text without false warnings.
- `TestTmuxManagerBeaconVerificationUsesTaskTerminology` or equivalent proves the verification indicator is not stuck on only `oro bead status`.
- `tests/test_session_start_extras.py` proves `role_beacon("architect")` and `role_beacon("manager")` inject task-primary beacon text from the active beacon assets.
- `assets/hooks/session_start_extras.py` superpowers/additional context no longer includes primary `oro bead ready` or `oro bead close` guidance.
- `cmd/oro/cmd_init_test.go` proves installed or embedded `$ORO_HOME/beacons/{architect,manager}.md` content is task-primary.

### Phase 3: Update active hook policy and notifications

Before prompts/docs switch architects to `oro task create`, active hooks must allow and react to that command.

Primary files:

- `assets/hooks/architect_router.py`
- `.claude/hooks/architect_router.py`, regenerated through `make stage-assets` if this is a staged mirror
- `assets/hooks/session_start_extras.py`
- `.claude/hooks/session_start_extras.py`, regenerated through `make stage-assets` if this is a staged mirror
- `assets/hooks/notify_manager_on_bead_create.py`
- `.claude/hooks/notify_manager_on_bead_create.py`, regenerated through `make stage-assets` if this is a staged mirror
- `assets/hooks/bd_create_notifier.py`
- `.claude/hooks/bd_create_notifier.py`, regenerated through `make stage-assets` if this is a staged mirror
- `tests/test_architect_router.py`
- `tests/test_architect_router_new.py`
- `tests/test_notify_manager_on_bead_create.py`

Acceptance:

- Architect hook allows every normal task command used by `ArchitectBeacon` and `ArchitectNudge`, including `oro task status`, `oro task ready`, `oro task show`, `oro task create`, and `oro task dep add`.
- Architect hook still rejects unrelated high-risk commands such as `oro start` in protected contexts.
- Manager notification hooks detect both `oro task create` and `oro bead create`.
- Human-facing notification text points to `oro task ready` while preserving compatibility where useful.
- `make stage-assets` leaves source hooks, `.claude` mirrors, and `cmd/oro/_assets` consistent.

### Phase 4: Update worker and ops prompts

Workers should receive `oro task ...` examples. Compatibility examples can mention `oro bead` once as legacy fallback, but not as the primary instruction.

Primary files:

- `pkg/worker/prompt.go`
- `pkg/worker/prompt_test.go`
- `pkg/ops/*prompt*.go`
- corresponding ops tests

Acceptance:

- Worker prompt tests assert primary creation, dependency, show, and failure/handoff examples use `oro task ...`.
- Ops prompt tests assert acceptance-criteria, decomposition, epic-fix, and escalation prompts use `oro task ...`.
- Worker exit/completion guardrails still forbid workers from closing their own assigned work directly unless dispatcher owns closure.

### Phase 5: Update assets, skills, and generated mirrors

Update active skills/hooks/assets that teach agents commands.

Primary paths:

- `assets/skills/`
- `.claude/skills/`
- `assets/hooks/`
- `.claude/hooks/`
- `assets/beacons/`
- `.claude/hooks/beacons/`
- `assets/commands/`
- `.claude/commands/`
- `cmd/oro/_assets/` after `make stage-assets`

Acceptance:

- Active skills and commands teach `oro task ...` as the primary command.
- Historical or compatibility references to `oro bead` are either under historical paths or have nearby text explaining legacy/internal context.
- `tests/test_asset_mirrors.py` and `scripts/check-agent-asset-mirrors.sh` verify `.claude/hooks`, `.claude/hooks/beacons`, `.claude/skills`, and `.claude/commands` mirror source assets where the repository treats `.claude` as an active checked-in mirror.
- `make stage-assets` has been run and `git diff --exit-code -- cmd/oro/_assets assets .claude` passes after intentional edits are staged.

### Phase 6: Update public docs

Update README and current operator docs. Historical migration docs retain bead language unless they describe current commands.

Primary files:

- `README.md`
- `docs/INSTALL.md`
- current runbooks that operators still execute
- `docs/plans/notes/bd-callsites.md` only if a short addendum is needed

Acceptance:

- README glossary defines task, legacy/internal bead, and task type.
- Current operator docs use `oro task ...` for normal work-item operations.
- Migration/recovery runbooks that must use legacy `oro bead migrate-from-dolt` state that this is intentionally legacy/migration-only.
- `scripts/check-task-terminology.sh` directly checks README and current operator docs for task-primary wording and the glossary.

### Phase 7: Add terminology guardrails

Add focused grep/test guardrails that prevent new public-facing docs/prompts from reintroducing "bead" as the primary product term, while allowing storage/protocol/internal/historical contexts.

Primary files:

- `cmd/oro/terminology_test.go` or `scripts/check-task-terminology.sh`
- `scripts/quality_gate.sh` only if the guard is stable and cheap

Acceptance:

- Guardrail tests have an explicit allowlist for storage/protocol/schema, historical docs, migration-only commands, closed work records, and compatibility CLI references.
- Guardrail tests fail on a newly introduced public prompt/doc that says `oro bead create` as the primary instruction.
- The guard is cheap enough for local focused runs; if it is added to QG, it must not invoke mutation testing.
- Top-level acceptance runs `scripts/check-task-terminology.sh` directly and also verifies a `TestTaskTerminologyGuard*` test is present in `go test -list`.

### Phase 8: Assert storage and protocol compatibility

The public terminology rename must not churn native persistence or protocol contracts.

Primary files:

- `pkg/protocol/types.go`
- `pkg/protocol/tables.go`
- `pkg/protocol/schema.go`
- `pkg/protocol/message.go`
- `pkg/protocol/*_test.go`
- `pkg/beadstore/store.go`
- `pkg/beadstore/sqlite.go`
- `pkg/beadstore/*_test.go`

Acceptance:

- `go test ./pkg/protocol ./pkg/beadstore -run 'Test.*(Bead|Schema|Fields|JSON|Assignment|Event|Memory|Migrate|SQLiteStore|Dependency)' -count=1` passes.
- `rg 'json:"bead_id' pkg/protocol/tables.go pkg/protocol/message.go cmd/oro/cmd_memories.go` finds preserved protocol/memory JSON tags.
- `rg 'CREATE TABLE IF NOT EXISTS beads|CREATE TABLE IF NOT EXISTS bead_' pkg/protocol/schema.go` finds preserved native schema names.
- Any internal rename of `protocol.Bead`, `pkg/beadstore`, `beads`, `bead_*`, or `bead_id` is explicitly out of scope and requires a separate compatibility spec.

## Rollback

Rollback is straightforward through Phase 5 because no data migration occurs:

- Remove `newTaskCmd()` registration.
- Revert prompt/docs changes.
- Keep `oro bead` untouched throughout.

Do not remove `oro bead` or rename storage/protocol in this spec; that is what keeps rollback low-risk.

## Open Questions

1. Should `oro task migrate-from-dolt` exist as an alias, or should migration remain intentionally under `oro bead` because it is historical/legacy?
2. Should worker branches remain `agent/<id>` forever, or should a later spec introduce a task-named branch prefix while preserving old branch cleanup logic?
3. Should the mg dashboard labels change in this pass, or be handled when mg is replaced/retired?

Recommended answers for this spec:

1. Keep migration under `oro bead`.
2. Keep branch prefix `agent/` for now.
3. Update only clearly active mg user-facing labels if tests are cheap; do not make mg the critical path.
