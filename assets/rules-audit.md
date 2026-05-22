# Oro Rule Enforcement Audit

Task: `oro-1f1d8`

Spec basis: `docs/plans/2026-04-28-oro-harness-architecture-spec.md` section 12.2.

This audit uses the section 12 doctrine scale:

| Level | Name | Meaning |
| --- | --- | --- |
| 1 | Lint | A static analyzer or custom check rejects the violation. |
| 2 | Types | The compiler or type checker makes the violation impossible. |
| 3 | Formatter | A formatter rewrites the code into compliance. |
| 4 | Hook | A pre-action, pre-commit, or runtime hook blocks or warns before the violation lands. |
| 5 | CI or quality gate | The quality gate, CI, or dispatcher gate rejects the violation before merge. |
| 6 | Best effort prompt | The rule only lives in Claude/skill/prompt text. |

Runtime agent hooks are recorded as Level 4 because they execute before the
agent action or stop event, the same practical tier as pre-commit hooks.

## Rule Inventory

| ID | Rule | Primary sources | Current | Best feasible | Evidence and action |
| --- | --- | --- | --- | --- | --- |
| R001 | Invoke `using-skills` before any action. | `AGENTS.md`, `assets/CLAUDE.md`, `assets/ORO_AGENT.md`, `assets/rules/claude/oro-worker.md`, `assets/skills/using-skills/SKILL.md` | 4 | 4 | `assets/hooks/enforce_skills.py` is installed by agent assets and covered by `assets/hooks/test_enforce_skills.py`. Keep as runtime hook because intent depends on conversation context. |
| R002 | Work one assigned task at a time. | `assets/rules/claude/oro-worker.md`, `assets/skills/executing-beads/SKILL.md`, `assets/skills/work-bead/SKILL.md`, `assets/skills/oro/SKILL.md` | 6 | 5 | Dispatcher assignment state enforces one active assignment per worker, but manual sessions rely on prompt text. Add a future invariant check for multi-claim manual work if needed. |
| R003 | Keep changes scoped to the active task. | `assets/rules/claude/oro-worker.md`, `assets/skills/executing-beads/SKILL.md`, `assets/skills/work-bead/SKILL.md` | 6 | 5 | Ops review compares diff to acceptance criteria. Low-hanging fruit: add a review checklist or scripted scope-drift summary for changed files versus task `Read:` paths. |
| R004 | Use TDD for feature work and bug fixes. | `assets/rules/claude/oro-worker.md`, `assets/skills/test-driven-development/SKILL.md`, `assets/skills/executing-beads/SKILL.md`, `assets/skills/work-bead/SKILL.md` | 6 | 6 | Red-first timing is not statically provable from final code. Keep as best effort plus review discipline. |
| R005 | Parse acceptance before implementation; unclear acceptance blocks work. | `assets/skills/executing-beads/SKILL.md`, `assets/skills/work-bead/SKILL.md`, `assets/skills/beadcraft/SKILL.md` | 5 | 5 | Dispatcher has missing-AC quarantine tests in `pkg/dispatcher/missing_ac_test.go`. Keep quality-gate style checks around `Test:`, `Cmd:`, `Assert:`, and `Read:` task fields. |
| R006 | Task criteria must include binary `Test`, `Cmd`, `Assert`, and `Read` fields. | `assets/skills/beadcraft/SKILL.md`, `assets/skills/adversarial-spec-review/SKILL.md`, `assets/skills/oro/SKILL.md` | 5 | 5 | Missing-AC paths are quarantined before worker assignment. Expand task creation validation when beadcraft output is wired directly into Oro. |
| R007 | One task equals one atomic commit and one closure reason. | `assets/skills/executing-beads/SKILL.md`, `assets/skills/work-bead/SKILL.md`, `assets/skills/git-commits/SKILL.md` | 6 | 5 | `oro work` naturally commits and closes per task. Manual work is prompt-only; low-hanging fruit is a close-time check for a referenced commit hash. |
| R008 | Run the task acceptance command before claiming completion. | `assets/skills/executing-beads/SKILL.md`, `assets/skills/work-bead/SKILL.md`, `assets/skills/verification-before-completion/SKILL.md` | 5 | 5 | Worker, manager-side quality gate, and ops review require acceptance evidence before merge. Preserve this in dispatcher review prompts. |
| R009 | Run the project quality gate before merge or task closure. | `assets/skills/executing-beads/SKILL.md`, `assets/skills/work-bead/SKILL.md`, `assets/skills/finishing-work/SKILL.md`, `assets/skills/oro/SKILL.md` | 5 | 5 | `scripts/quality_gate.sh`, `cmd/oro` worker flows, and CI enforce this. |
| R010 | Use `make build` when assets must be embedded; do not rely on bare `go build` for asset-sensitive verification. | `assets/skills/oro/references/gotchas.md`, `assets/skills/oro/SKILL.md` | 6 | 5 | Quality gate stages assets for broad verification, but the wording is still mostly guidance. Add a check for fresh worktree asset staging before full Go test runs. |
| R011 | Use `make stage-assets` before broad Go tests in fresh worktrees. | `assets/skills/oro/references/gotchas.md` | 6 | 5 | This is a known gotcha rather than a hook. Low-hanging fruit: make the test helper detect missing `_assets` and print the command. |
| R012 | Never force-initialize or nuke the native beadstore/Dolt history. | `assets/skills/oro/SKILL.md`, `assets/skills/oro/references/gotchas.md` | 6 | 4 | Dangerous-command hooks can catch known command strings. Promote to a runtime Bash hook denylist if the old command names remain reachable. |
| R013 | Do not use destructive commands without explicit confirmation. | `assets/skills/destructive-command-safety/SKILL.md` | 6 | 4 | Some worktree/rebase guards exist, but generic `rm`, `git reset`, and `git checkout` still rely on prompt discipline. Promote with a Bash pre-tool hook. |
| R014 | Prefer archiving over deleting when intent is ambiguous. | `assets/skills/destructive-command-safety/SKILL.md` | 6 | 6 | Requires judgment about user intent. Keep best effort. |
| R015 | Use safe branch deletion (`git branch -d`), not force deletion, unless explicitly safe. | `assets/skills/destructive-command-safety/SKILL.md`, `assets/skills/oro/references/gotchas.md` | 6 | 4 | Recovery hardening moved production branch cleanup to safe delete. Add runtime hook coverage for manual `git branch -D`. |
| R016 | Use git worktrees for isolated feature work and avoid editing shared roots for parallel work. | `assets/skills/using-git-worktrees/SKILL.md`, `assets/skills/work-bead/SKILL.md`, `assets/skills/dispatching-parallel-agents/SKILL.md` | 4 | 4 | `assets/hooks/worktree_guard.py`, `assets/hooks/enforce_worktree.py`, and worktree workflow docs cover this. |
| R017 | Do not remove a worktree until all useful work is committed to its branch. | `assets/skills/using-git-worktrees/SKILL.md`, `assets/skills/work-bead/SKILL.md` | 4 | 4 | Worktree guards and Oro cleanup preserve this in managed paths. Keep hook-level protection. |
| R018 | Do not use interactive task editing; use explicit `oro task update` flags. | `assets/skills/oro/references/gotchas.md` | 6 | 1 | Low-hanging fruit: static grep docs/prompts for "interactive task editing" and CLI-level deprecation if an interactive path exists. |
| R019 | `oro task create --parent` only sets hierarchy; add dependency edges explicitly. | `assets/skills/oro/references/gotchas.md`, `assets/skills/oro/SKILL.md`, `assets/skills/beadcraft/SKILL.md`, `scripts/check-native-beadstore-invariants.py` | 6 | 5 | `check-native-beadstore-invariants.py` enforces that open epic children have explicit `blocks` or `conditional-blocks` edges from the child to the epic; hierarchy alone is not a blocker dependency. |
| R020 | Use `rg`/fast search primitives during exploration. | `assets/skills/explore/SKILL.md` | 6 | 6 | Tool choice depends on availability and user request. Keep best effort. |
| R021 | Exploration is read-only unless explicitly moving to implementation. | `assets/skills/explore/SKILL.md`, `assets/skills/observe-before-editing/SKILL.md` | 6 | 4 | Could be partially enforced by mode-aware hooks, but current workflow is prompt-only. |
| R022 | Observe actual failure output before editing a bug fix. | `assets/skills/observe-before-editing/SKILL.md`, `assets/skills/systematic-debugging/SKILL.md` | 6 | 6 | Requires reasoning over causality and evidence. Keep best effort. |
| R023 | Systematic debugging must follow reproduce, isolate, hypothesize, verify. | `assets/skills/systematic-debugging/SKILL.md` | 6 | 6 | Human reasoning process cannot be fully deterministic. Keep best effort. |
| R024 | Stop after three failed debugging hypotheses and escalate or create a blocker. | `assets/skills/systematic-debugging/SKILL.md`, `pkg/worker/prompt.go` | 6 | 5 | Dispatcher can detect repeated QG/review churn. Low-hanging fruit: emit a named incident when a worker repeats the same failed hypothesis marker. |
| R025 | Use adversarial spec review after writing specs or task trees. | `assets/skills/spec/SKILL.md`, `assets/skills/adversarial-spec-review/SKILL.md`, `assets/skills/brainstorming/SKILL.md` | 6 | 5 | Prompt-only today. Low-hanging fruit: require a recorded review artifact before epic priority is raised above a threshold. |
| R026 | Specs must trace production wiring, not just describe desired behavior. | `assets/skills/adversarial-spec-review/SKILL.md`, `assets/skills/completion-check/SKILL.md` | 6 | 6 | Requires code understanding. Keep review discipline. |
| R027 | Completion claims require fresh verification output; avoid "should/probably/seems". | `assets/skills/verification-before-completion/SKILL.md` | 6 | 5 | Quality gates prove commands; phrasing remains prompt-level. A review linter could flag banned completion words in final worker messages. |
| R028 | Before declaring done, verify the new behavior is actually wired and invoked. | `assets/skills/completion-check/SKILL.md`, `assets/skills/review-implementation/SKILL.md` | 6 | 6 | Requires semantic tracing. Keep as review rule. |
| R029 | After task completion, run context checkpoint and hand off before context degradation. | `assets/skills/context-checkpoint/SKILL.md`, `assets/hooks/context_pct_writer.py`, `assets/hooks/compact_trigger.py`, `assets/hooks/pre_compact.py` | 4 | 4 | Runtime hooks and dispatcher context thresholds enforce the hard boundary. |
| R030 | Handoffs must include compact YAML and task state. | `assets/skills/create-handoff/SKILL.md`, `assets/skills/resume-handoff/SKILL.md` | 6 | 5 | Low-hanging fruit: validate handoff files for required `tasks:` keys before accepting a continuation task. |
| R031 | Use Oro task-primary commands for factory status and control. | `assets/beacons/manager.md`, `assets/skills/oro/SKILL.md` | 6 | 5 | CLI tests cover manager copy. Keep docs aligned with task-primary naming checks. |
| R032 | Manager should proceed autonomously for routine operations. | `assets/beacons/manager.md`, `assets/skills/watching-oro/SKILL.md` | 6 | 6 | Requires judgment about routine versus rare cases. Keep best effort. |
| R033 | Manager must not write code, manage worktrees, merge/rebase, or talk to workers directly in optional legacy console mode. | `assets/beacons/manager.md` | 6 | 5 | Low-hanging fruit: console prompt tests can assert this wording; actual behavior is enforced by not giving that role write tools. |
| R034 | Do not stop or drain workers unless requested or a health finding requires it. | `assets/beacons/manager.md`, `assets/skills/watching-oro/SKILL.md` | 6 | 5 | `oro monitor --act` health gates can enforce unsafe cases. Prompt-only for discretionary stops. |
| R035 | Use `oro monitor` and event-driven observation; avoid tight polling loops. | `assets/beacons/manager.md`, `assets/skills/watching-oro/SKILL.md` | 6 | 5 | Low-hanging fruit: replace loop snippets in docs with monitor command examples and test docs for stale loops. |
| R036 | Monitor must detect stuck workers, QG churn, recovery quarantines, and unsafe health before acting. | `assets/skills/watching-oro/SKILL.md`, `assets/skills/watching-oro/references/deep-observation.md` | 5 | 5 | `cmd/oro/cmd_monitor_test.go` covers QG, ops, recovery, and unsafe-health blocks. |
| R037 | Prompt-injection detection warns but never blocks user progress. | `assets/hooks/prompt_injection_guard.py`, `pkg/agentassets/claude_test.go`, `pkg/agentassets/codex_test.go` | 4 | 4 | Runtime hook emits additional context and is registered in agent settings. |
| R038 | Stop checklist must not block the user. | `assets/hooks/stop-checklist.sh`, `assets/hooks/test_stop_checklist.py` | 4 | 4 | Hook exits successfully by design and has tests. |
| R039 | Auto-format on agent edits. | `assets/hooks/auto-format.sh`, `pkg/agentassets/claude_test.go` | 3 | 3 | Formatter hook plus quality gate format lanes enforce final state. |
| R040 | Agent asset mirrors must stay in parity across bundled Claude/Codex assets. | `assets/hooks/test_parity.py`, `scripts/check-agent-asset-mirrors.sh`, `pkg/agentassets/*_test.go` | 5 | 5 | CI and tests cover asset mirror drift. |
| R041 | Agent-browser actions must snapshot before interacting and resnapshot after page changes. | `assets/skills/agent-browser/SKILL.md`, duplicate `assets/skills/agent-browser/agent-browser/SKILL.md`, references under `agent-browser/references/` | 6 | 6 | Browser DOM refs are runtime state. Keep best effort and examples. |
| R042 | Agent-browser JavaScript should use stdin or base64 for complex eval payloads. | `assets/skills/agent-browser/SKILL.md`, `assets/skills/agent-browser/references/commands.md` | 6 | 6 | Shell quoting risk is contextual. Keep as best effort. |
| R043 | Never commit browser state files or hardcoded credentials. | `assets/skills/agent-browser/references/authentication.md`, `assets/skills/agent-browser/references/session-management.md` | 6 | 4 | Low-hanging fruit: add secret/state-file patterns to pre-commit and CI scans. |
| R044 | Use environment variables and short-lived sessions for browser credentials. | `assets/skills/agent-browser/references/authentication.md`, `assets/skills/agent-browser/references/proxy-support.md` | 6 | 5 | Secret scanning can catch committed credentials; session lifetime remains best effort. |
| R045 | GitHub work should use `gh`, verify auth, specify repo when needed, and prefer view/diff over checkout for reviews. | `assets/skills/github/SKILL.md` | 6 | 6 | Depends on external repository context. Keep best effort. |
| R046 | Use tmux only for interactive TTY needs; prefer background execution otherwise. | `assets/skills/tmux/SKILL.md` | 6 | 6 | Tool choice is contextual. Keep best effort. |
| R047 | Run requested code review with findings first and severity classification. | `assets/skills/requesting-code-review/SKILL.md`, `assets/skills/receiving-code-review/SKILL.md`, `assets/skills/review-implementation/SKILL.md` | 6 | 6 | Review quality is semantic. Keep best effort plus review prompts. |
| R048 | Skill files need trigger-only descriptions, concise bundled resources, and no time-sensitive facts. | `assets/skills/writing-skills/SKILL.md`, `assets/skills/skill-creator` equivalent in installed skills | 6 | 5 | Low-hanging fruit: static SKILL.md linter for frontmatter description shape and banned workflow summaries. |
| R049 | Writing plans must include test-fail and test-pass steps and route to `executing-plans`. | `assets/skills/writing-plans/SKILL.md`, `assets/skills/executing-plans/SKILL.md` | 6 | 5 | Plan artifacts can be linted for required step language. |
| R050 | Parallel agent work must use separate worktrees, verify commits compile, and merge linearly. | `assets/skills/dispatching-parallel-agents/SKILL.md` | 6 | 5 | Oro worker flow enforces isolated worktrees; manual parallel work remains prompt-only. |
| R051 | Avoid TaskOutput/tail-style transcript polling in long-running agent orchestration. | `assets/skills/dispatching-parallel-agents/SKILL.md`, `assets/skills/watching-oro/SKILL.md` | 6 | 4 | Low-hanging fruit: runtime hook denylist for known hanging commands such as `tail -f` on task output. |
| R052 | Prefer proven, boring solutions and simplify before designing. | `assets/skills/brainstorming/SKILL.md`, `assets/skills/premortem/SKILL.md`, `assets/skills/refactor/SKILL.md` | 6 | 6 | Engineering judgment cannot be linted. Keep best effort. |
| R053 | Document non-trivial verified solutions for future search. | `assets/skills/documenting-solutions/SKILL.md`, `assets/skills/finishing-work/SKILL.md` | 6 | 5 | Low-hanging fruit: finishing checklist can require either a note or explicit "no new pattern" statement. |
| R054 | Review docs after code changes that affect user or operator behavior. | `assets/skills/review-docs/SKILL.md`, `assets/skills/finishing-work/SKILL.md` | 6 | 5 | Scope can be inferred from changed files. Add a changed-files-to-docs checklist in ops review for CLI/prompt changes. |
| R055 | Use session logs only for prior conversation/history questions. | `assets/skills/session-logs/SKILL.md` | 6 | 6 | Intent-dependent. Keep as best effort. |
| R056 | Quality gate must run Go format, lint, tests, build, vet, vulnerability, docs/config, Python format/lint/type/test lanes. | `scripts/quality_gate.sh`, `.github/workflows/ci.yml`, `Makefile`, `assets/skills/verification-before-completion/SKILL.md` | 5 | 5 | CI and local QG already enforce. |
| R057 | Python tooling should avoid pyenv shim leakage and operate on tracked source scopes. | `scripts/quality_gate.sh`, `scripts/test_quality_gate.sh` | 5 | 5 | QG helper tests cover resolver behavior. |
| R058 | Native beadstore ready/blocked invariants must hold. | `scripts/check-native-beadstore-invariants.py`, `pkg/beadstore`, `pkg/dispatcher` tests | 5 | 5 | Script checks invalid statuses, blocked-view mismatches, ready-blocked overlap, and assignment conflicts. |
| R059 | Task terminology should be task-primary; bead wording is compatibility-only. | `scripts/check-task-terminology.sh`, manager and CLI tests | 5 | 5 | Terminology script is wired into checks and has explicit allowlists. |
| R060 | Agent/epic branches should not be pushed directly. | `cmd/oro/init_stealth.go`, `cmd/oro/init_stealth_test.go` | 4 | 4 | Installed pre-push hook blocks protected branch patterns. |

## Level 6 Rules With Clear Promotion Paths

| Rule ID | Promotion target | Suggested follow-up |
| --- | --- | --- |
| R003 | Level 5 | Add scripted scope-drift summary comparing changed files to task `Read:` and acceptance. |
| R007 | Level 5 | Require close reasons to include a commit hash or explicitly say "no code change". |
| R010 | Level 5 | Detect missing staged assets before broad Go tests in fresh worktrees. |
| R012 | Level 4 | Add Bash pre-tool denylist for known force-initialization and destructive beadstore commands. |
| R013 | Level 4 | Add Bash pre-tool confirmation gate for `rm`, `git reset`, `git checkout`, and force push. |
| R015 | Level 4 | Add Bash pre-tool denial for manual `git branch -D` unless an override env var is present. |
| R018 | Level 1 | Add CLI/docs scan for interactive task editing references or code paths. |
| R019 | Level 5 | Native invariant check flags an epic with open children but no explicit child blocker edges; keep CLI guidance clear that `--parent` only records hierarchy. |
| R025 | Level 5 | Store spec-review artifacts and require them before running high-priority epics. |
| R030 | Level 5 | Validate handoff YAML includes `tasks.completed`, `tasks.in_progress`, and `tasks.remaining`. |
| R043 | Level 4 | Add browser state and credential patterns to pre-commit/CI secret scanning. |
| R048 | Level 5 | Add `SKILL.md` frontmatter/description linter. |
| R049 | Level 5 | Add plan artifact linter for RED/GREEN verification steps. |
| R051 | Level 4 | Add runtime hook warning/deny for known hanging transcript commands. |
| R054 | Level 5 | Add ops-review doc-staleness checklist for CLI, prompt, and operator behavior changes. |

## Source Coverage

| Source | Covered by |
| --- | --- |
| `AGENTS.md` | R001 |
| `assets/CLAUDE.md` | R001 |
| `assets/ORO_AGENT.md` | R001 |
| `assets/rules/claude/oro-worker.md` | R001, R002, R003, R004 |
| `assets/beacons/manager.md` | R031, R032, R033, R034, R035 |
| `assets/commands/restart-oro.md` | R031, R034, R035, R036 |
| `assets/commands/toggle-priming.md` | R031 |
| `assets/hooks/*.py`, `assets/hooks/*.sh` | R001, R016, R017, R029, R037, R038, R039, R040 |
| `assets/skills/adversarial-spec-review/SKILL.md` | R006, R025, R026 |
| `assets/skills/agent-browser/**` | R041, R042, R043, R044 |
| `assets/skills/beadcraft/SKILL.md` | R005, R006, R019 |
| `assets/skills/brainstorming/SKILL.md` | R025, R052 |
| `assets/skills/completion-check/SKILL.md` | R026, R028 |
| `assets/skills/context-checkpoint/SKILL.md` | R029 |
| `assets/skills/create-handoff/SKILL.md` | R030 |
| `assets/skills/destructive-command-safety/SKILL.md` | R013, R014, R015 |
| `assets/skills/dispatching-parallel-agents/SKILL.md` | R016, R050, R051 |
| `assets/skills/documenting-solutions/SKILL.md` | R053 |
| `assets/skills/executing-beads/SKILL.md` | R002, R003, R005, R007, R008, R009 |
| `assets/skills/executing-plans/SKILL.md` | R049 |
| `assets/skills/explore/SKILL.md` | R020, R021 |
| `assets/skills/finishing-work/SKILL.md` | R009, R053, R054 |
| `assets/skills/git-commits/SKILL.md` | R007 |
| `assets/skills/github/SKILL.md` | R045 |
| `assets/skills/observe-before-editing/SKILL.md` | R021, R022 |
| `assets/skills/oro/SKILL.md` and `assets/skills/oro/references/gotchas.md` | R002, R010, R011, R012, R018, R019, R031 |
| `assets/skills/premortem/SKILL.md` | R052 |
| `assets/skills/receiving-code-review/SKILL.md` | R047 |
| `assets/skills/refactor/SKILL.md` | R052 |
| `assets/skills/requesting-code-review/SKILL.md` | R047 |
| `assets/skills/resume-handoff/SKILL.md` | R030 |
| `assets/skills/review-docs/SKILL.md` | R054 |
| `assets/skills/review-implementation/SKILL.md` | R028, R047 |
| `assets/skills/session-logs/SKILL.md` | R055 |
| `assets/skills/spec/SKILL.md` | R025, R026 |
| `assets/skills/systematic-debugging/SKILL.md` | R022, R023, R024 |
| `assets/skills/test-driven-development/SKILL.md` | R004 |
| `assets/skills/tmux/SKILL.md` | R046 |
| `assets/skills/using-git-worktrees/SKILL.md` | R016, R017 |
| `assets/skills/using-skills/SKILL.md` | R001 |
| `assets/skills/verification-before-completion/SKILL.md` | R008, R027, R056 |
| `assets/skills/watching-oro/**` | R031, R034, R035, R036, R051 |
| `assets/skills/work-bead/SKILL.md` | R002, R003, R005, R007, R008, R009, R016 |
| `assets/skills/workflow-routing/SKILL.md` | R052 |
| `assets/skills/writing-plans/SKILL.md` | R049 |
| `assets/skills/writing-skills/SKILL.md` | R048 |
| `assets/thresholds.json` | R029 |
| `scripts/quality_gate.sh`, `.github/workflows/ci.yml`, `Makefile` | R056, R057 |
| `scripts/check-native-beadstore-invariants.py` | R058 |
| `scripts/check-task-terminology.sh` | R059 |
| `cmd/oro/init_stealth.go` | R060 |

## Verification

Task acceptance command:

```bash
ls assets/rules-audit.md && grep -c '^|' assets/rules-audit.md
```
