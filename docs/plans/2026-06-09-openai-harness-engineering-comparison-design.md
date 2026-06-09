# OpenAI Harness Engineering Comparison for Oro

Date: 2026-06-09
Status: Draft strategic spec; implementation decomposition pending
Source: https://openai.com/index/harness-engineering/

## Summary

OpenAI's "Harness engineering: leveraging Codex in an agent-first world" describes a repository and runtime environment optimized around one premise: humans steer, agents execute, and the scarce resource is human attention. The practical architecture is not just stronger models. It is an agent-legible codebase, per-worktree execution environments, mechanical invariants, direct observability access, agent-to-agent review loops, and recurring garbage collection of entropy.

Oro is already pointed in the same direction. It has a task-driven swarm, isolated worktrees, quality gates, ops-agent review, context continuation, durable cards, managerless ops runs, Codex runtime work, `codestruct`, `oro impact`, and a finite dogfood harness. The largest remaining gap is that Oro's harness is strongest around code workflow and weakest around application/runtime legibility: agents do not yet get a standard per-worktree app environment with browser-driving, logs, metrics, traces, and journey assertions.

This spec captures the comparison and proposes the next additions to make Oro's harness more like the article's agent-first system.

## Research Summary

External source read:

- OpenAI article, "Harness engineering: leveraging Codex in an agent-first world", published 2026-02-11. Browser fetch succeeded; direct `curl` download was blocked by the site with HTTP 403. Key themes: agent legibility, repo-local knowledge, per-worktree app boot, browser automation via Chrome DevTools, local observability stacks, strict architecture/taste invariants, agent-to-agent review, short-lived PRs, rising autonomy, and continuous cleanup.

Repo sources read:

- `README.md`: Oro philosophy, worker lifecycle, quality gate, memory layers, worktrees, Codex support.
- `docs/plans/2026-04-28-oro-harness-architecture-spec.md`: Harness v6: cards, codestruct, context checkpointing, worker/oracle split, structured renders.
- `docs/plans/2026-05-06-codex-harness-parity-design.md`: dispatcher-spawned Codex worker parity, plugin/hooks, AGENTS.md, Codex compact gap.
- `docs/plans/2026-05-20-autonomous-factory-reliability-v2-design.md`: finite dogfood harness, ops failure visibility, target-aware cleanup, long-subprocess progress.
- `pkg/worker/prompt.go`: cards, Code Structure, Relevant Code, handoff, edit tools, QG instructions in worker prompts.
- `cmd/oro/cmd_work.go`: code-search and Code Structure prompt context construction.
- `pkg/dispatcher/assign_payload.go`: single assignment payload path, relevant card injection, symbol hints from acceptance criteria.
- `pkg/cards/cards.go` and `pkg/cards/store.go`: typed durable knowledge, relevance scoring, pending learning promotion.
- `pkg/agentassets/spec.go` and `pkg/agentassets/codex.go`: Claude/Codex hook generation and Codex plugin package.
- `pkg/agentruntime/codex/codex.go`: Codex worker and ops subprocess adapter.
- `pkg/protocol/schema.go` and `pkg/dispatcher/dispatcher.go`: durable ops runs, managerless escalation routing, startup reconciliation.
- `cmd/oro/cmd_harness.go` and `cmd/oro/cmd_harness_dogfood.go`: checkpoint and finite dogfood verification commands.

## OpenAI Article: Harness Principles

### Humans steer, agents execute

The article's core operating model is that engineers no longer primarily write code. They design environments, specify intent, and build feedback loops that let agents do reliable work. When a task fails, the question is not "how do we prompt harder?" but "what capability, tool, guardrail, or repository-local knowledge was missing?"

### Application legibility

OpenAI found human QA became the bottleneck as code throughput rose. Their response was to make the application directly legible to Codex:

- The app can boot per git worktree.
- Codex can drive the UI through Chrome DevTools Protocol.
- Agents can take DOM snapshots, screenshots, trigger user paths, observe events, and loop until validation passes.
- Each worktree gets an ephemeral local observability stack.
- Logs, metrics, and traces can be queried by agents via LogQL, PromQL, and TraceQL-style APIs.

This turns prompts such as "startup must complete under 800ms" or "no span in these journeys exceeds two seconds" into executable harness work.

### Repository knowledge as system of record

The article rejects a giant `AGENTS.md`. Instead, `AGENTS.md` is a table of contents and the repository contains structured docs, plans, product specs, generated schemas, references, quality scores, reliability docs, and architecture maps. Agents get a map, then progressively disclose deeper sources.

The key rule is: if Codex cannot access it from the repo, it does not exist for the agent.

### Mechanical architecture and taste

Documentation is not enough. The article emphasizes strict dependency direction, custom linters, structural tests, file-size rules, schema/name conventions, structured logging rules, and remediation-oriented lint messages. Human taste is fed back into tooling, not left as repeated review comments.

### Throughput changes merge/review philosophy

When agent throughput is high, waiting is expensive and correction is cheap. OpenAI's system relies heavily on agent-to-agent review loops, short-lived PRs, CI feedback handling, and automated cleanup instead of requiring human review as the main gate.

### Continuous garbage collection

Agent-generated systems accumulate entropy because agents copy existing patterns. OpenAI encodes "golden principles" as mechanical rules and runs background cleanup agents to scan, update quality grades, and open targeted refactor PRs.

## Oro Current State

### Strong alignment

Oro already embodies several harness-engineering principles:

- **Swarm execution:** `README.md` describes multiple workers executing tracked tasks in isolated worktrees, with context continuation when workers exhaust their window.
- **Quality mechanisms:** every task is intended to pass TDD, generated quality gate, ops review, and merge policy.
- **Durable knowledge:** cards replace flat memory and carry rules, patterns, decisions, facts, taste, scores, contradictions, retirement metadata, and lineage.
- **Context renders:** `oro current` and `oro handoff` are renders over state, not static truth files.
- **Agent legibility:** `pkg/codestruct`, `worker.FormatNavMap`, Code Structure prompt sections, symbol hints, and `oro impact` reduce raw source dumping.
- **Managerless recovery:** `ops_runs` makes rare judgment/recovery work durable instead of depending on a resident manager pane.
- **Codex runtime path:** Codex worker/ops adapters and Codex plugin generation exist.
- **Dogfood harness:** finite seed/run/assert scenarios exist for monitor/action hardening.

### Implemented examples

- `pkg/cards/cards.go` defines durable card records and pending learnings.
- `pkg/cards/store.go` scores relevant cards by tags, text, symbols, and bead type.
- `pkg/dispatcher/assign_payload.go` injects relevant cards and symbol hints into assignments.
- `pkg/worker/prompt.go` renders Cards, Code Structure, Relevant Code, Git History, Edit Tools, QG, and Context Handoff sections.
- `cmd/oro/cmd_work.go` builds Code Structure nav maps from acceptance criteria.
- `pkg/protocol/schema.go` defines `ops_runs` and `monitor_actions`.
- `pkg/dispatcher/dispatcher.go` reconciles ops runs on startup and routes oversized/missing-AC/recovery work through ops agents.
- `cmd/oro/cmd_harness_dogfood.go` seeds, runs, and asserts finite dogfood scenarios.

## Gap Analysis

### Gap 1: App/runtime legibility is not first-class

OpenAI's biggest capability gap relative to Oro is per-worktree application observability. Oro can run commands and quality gates, but it does not yet provide a standard harness for:

- starting an app per worker worktree,
- assigning stable per-worktree ports,
- driving browser journeys,
- capturing screenshots/videos/traces,
- querying logs/metrics/traces,
- enforcing UI or service-level journey budgets.

There is a related design in `docs/plans/2026-06-09-front-end-e2e-verification-design.md`, but that starts with generated QG lanes. The article points to a broader runtime harness: application process management plus browser and observability APIs that agents can use during development, not only at merge gate time.

### Gap 2: Repo knowledge has structure, but not enough hygiene automation

Oro has plans, cards, decisions, runbooks, and context renders. It does not yet have a complete mechanical doc-quality system:

- docs index/freshness checks,
- generated architecture maps,
- quality grades by subsystem,
- owner/staleness metadata,
- recurring doc-gardening agent,
- automatic promotion of repeated review feedback into docs, cards, skills, or lints.

### Gap 3: Taste rules are partly prompt/process, not all mechanical

Oro has strong process skills and QG, but repeated "taste" and architecture failures should migrate into custom lints with remediation text. The OpenAI article treats custom lint messages as agent-facing teaching tools.

### Gap 4: PR lifecycle automation is not the central loop

Oro's dispatcher can merge locally and run ops review, but the article's end-to-end loop includes opening PRs, requesting agent reviews, responding to human and agent feedback, fixing CI failures, and merging. Oro has GitHub skills/tooling around this, but not a unified `oro pr drive` lifecycle.

### Gap 5: Long-run autonomy needs a richer control plane

Oro has context continuation, ops runs, monitor, health, and dogfood. The article describes six-hour single-agent runs that can inspect app state, retry, and recover while humans sleep. Oro needs richer task-level state around active app processes, validation journeys, artifacts, and run budgets to make those long runs observable and resumable.

## Proposed Direction

### Phase 1: Per-worktree app harness

Add a project-local app environment layer.

Possible user surface:

```text
oro app detect
oro app start --worktree <path> --profile dev
oro app status --worktree <path> --json
oro app stop --worktree <path>
oro app logs --worktree <path> --tail 200
```

Core behavior:

- Detect app profiles from `.oro/config.yaml`, `package.json`, `go.mod`, `docker-compose.yml`, `Procfile`, or explicit config.
- Allocate stable per-worktree ports through the existing port-registry direction.
- Start processes with isolated env and logs under `.oro/worktrees/<id>/`.
- Write a machine-readable manifest containing URLs, ports, process IDs, log paths, and readiness state.
- Tear down app processes when the assignment completes or the worktree is removed.

Why first:

- It is the foundation for browser testing and observability.
- It is useful outside front-end projects: APIs, CLIs with local servers, web dashboards, and integration services all benefit.
- It keeps Oro's existing worker/QG/review architecture intact.

### Phase 2: Browser journey harness

Add a browser-driving validation layer that can be used by workers and review agents.

Possible user surface:

```text
oro journey run docs/journeys/settings-save.yaml --worktree <path>
oro journey record --url http://127.0.0.1:<port>
oro journey artifacts --bead <id>
```

Journey spec shape:

```yaml
name: settings-save
app: web
start_url: /settings
steps:
  - click: "[data-testid=enable-sync]"
  - click: "[data-testid=save]"
assertions:
  - text: "Saved"
  - screenshot: settings-saved
  - console_errors: none
budgets:
  max_step_ms: 2000
  max_total_ms: 10000
```

Core behavior:

- Prefer Playwright as the first implementation backend.
- Capture screenshots, videos/traces on failure, console errors, network failures, and DOM snapshots.
- Let acceptance criteria reference journeys with `Journey: docs/journeys/settings-save.yaml`.
- Make ops review able to rerun referenced journeys.

This complements, rather than replaces, the front-end E2E QG lane. QG can run the project's test suite; journeys give Oro agent-readable task-specific evidence.

### Phase 3: Local observability harness

Expose logs, metrics, and traces through stable commands and APIs.

Possible user surface:

```text
oro observe logs --worktree <path> --query "error OR panic"
oro observe metrics --worktree <path> --query "startup_duration_ms"
oro observe traces --worktree <path> --journey settings-save
oro observe assert --worktree <path> docs/observability/startup.yaml
```

Initial version can be deliberately boring:

- structured process logs in SQLite or JSONL,
- readiness and startup timing events,
- command duration metrics,
- browser console/network events,
- OpenTelemetry ingestion later if the project already emits it.

Do not start by cloning a full production observability stack. Start with the smallest agent-readable signal store, then add adapters.

### Phase 4: Knowledge gardening and quality grades

Add recurring repo hygiene runs:

```text
oro docs check
oro docs garden
oro cards garden
oro quality grade
```

Checks:

- stale docs that reference moved/removed files,
- plans marked active but superseded by code,
- cards contradicted repeatedly,
- repeated review patterns not promoted to lints/skills,
- missing architecture maps for packages touched often,
- quality grades by subsystem: tests, docs, observability, agent-legibility, QG coverage.

Output should be tasks or PRs, not prose-only reports.

### Phase 5: Mechanical taste invariants

Promote repeated agent failures into rules:

- architecture boundary lints,
- max file/function size,
- structured error/logging rules,
- no ad hoc JSON/string parsing where typed parsers exist,
- UI artifact and test evidence requirements,
- "acceptance criteria must cite a command or journey" rules for executable tasks.

Error messages should be written for agents:

```text
dispatcher: dependency from pkg/web to pkg/dispatcher is forbidden.
Use pkg/dashboard/data as the read boundary, or add a provider interface.
```

### Phase 6: PR drive loop

Add a GitHub-backed lifecycle for repos that want PR flow:

```text
oro pr drive <bead-id>
oro pr review-loop <pr>
oro pr fix-ci <pr>
```

Behavior:

- open PR from worker branch,
- request configured agent reviewers,
- summarize and respond to comments,
- apply fixes,
- watch CI,
- retry failed checks when appropriate,
- merge under configured policy.

This should be optional. Oro's local merge path remains valuable for solo/local dogfood.

## Recommended Narrowest Wedge

Start with **Phase 1: per-worktree app harness**.

It is the load-bearing missing capability. Browser journeys, observability, and richer front-end E2E all depend on workers having a reliable way to boot and inspect the application in their isolated worktree. It also fits Oro's current architecture: the dispatcher already owns worktrees and assignment lifecycle, so app process lifecycle should be dispatcher-owned rather than prompt-owned.

Minimum useful version:

- `.oro/config.yaml` app profile with command, root, readiness URL, env, and artifact dir.
- `oro app start/status/stop/logs`.
- Per-worktree port allocation.
- Assignment payload includes app URL and log command when an app profile exists.
- Worker prompt teaches "use `oro app status/logs` and app URL for runtime validation."
- Dogfood smoke proves start/status/stop on an isolated test app.

## Premortem

### Tigers

- **App harness mutates or depends on live developer state.** Mitigation: app state must live under the worktree or isolated Oro state paths; dogfood defaults to temp state.
- **Port allocation races between workers.** Mitigation: use a shared port registry with leases keyed by worktree/assignment, and release on completion.
- **Long-running app processes leak after worktree removal.** Mitigation: process group tracking, teardown during assignment cleanup, startup reconciliation that reaps orphaned app processes.
- **Browser journey flakiness erodes trust.** Mitigation: first make app start/status/logs stable; introduce journeys with artifact capture and clear retry policy.
- **Observability stack becomes too heavy.** Mitigation: start with local logs/events/metrics in SQLite/JSONL before adding full OpenTelemetry adapters.

### Elephants

- This does not replace the front-end E2E QG lane; it provides a runtime substrate that lane can use later.
- This does not guarantee agents can fix every runtime failure; it makes runtime failures visible and repeatable.
- This does not solve interactive Codex parity. It improves dispatcher-spawned workers first.

### Paper Cuts

- Different project types need different start commands.
- Readiness probes need timeouts and useful failure messages.
- Artifact directories need QG exclusions to avoid linting generated traces/screenshots.
- App profiles need a dry-run/check mode so `oro init --check` can explain missing commands.

## Open Decisions

- Should app harness config live under a top-level `apps:` section or a single `app:` section first?
- Should dispatcher start the app automatically on assignment, or should the worker explicitly request `oro app start`?
- Should browser journey execution be allowed in worker prompts before it is part of QG?
- Should observability use SQLite/JSONL first, or adopt an existing local stack immediately?
- Should PR drive be a first-class Oro command or remain a GitHub skill workflow until app/runtime harnessing lands?

Recommendation: choose one app profile first, worker-started but dispatcher-tracked. That keeps the first implementation small while still giving the dispatcher enough state to clean up reliably.

## Acceptance for This Spec

This document is only a strategic comparison and design seed. It is not implementation-ready until the open decisions are answered and the narrowest wedge is decomposed into tasks.

Implementation planning should begin with:

```text
docs/plans/2026-06-09-openai-harness-engineering-comparison-design.md
docs/plans/2026-06-09-front-end-e2e-verification-design.md
docs/plans/2026-04-28-oro-harness-architecture-spec.md
docs/plans/2026-05-20-autonomous-factory-reliability-v2-design.md
```

First implementation epic should target:

```text
Title: Per-worktree app harness for Oro workers
Test: go test ./cmd/oro ./pkg/dispatcher ./pkg/protocol -run 'AppHarness|PortRegistry|AssignmentAppContext' -count=1
Cmd: scripts/oro-dogfood-smoke.sh --iterations 3 --workers 2
Assert: app profiles can start/status/stop/log per isolated worktree; ports do not collide; assignment cleanup tears down app processes; dogfood leaves no active app processes, active assignments, quarantines, QG incidents, or failed/stale ops runs.
```
