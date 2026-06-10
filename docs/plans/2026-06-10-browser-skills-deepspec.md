# Oro Browser Skills Deepspec

Date: 2026-06-10
Status: Draft deepspec v5. Local deep premortem completed. Claude Fable adversarial review passed on round 4 for v4; v5 incorporates the follow-up gstack adoption list and requires task-graph review refresh after decomposition.
Related specs:

- `docs/plans/2026-06-09-openai-harness-engineering-comparison-design.md`
- `docs/plans/2026-06-09-front-end-e2e-verification-design.md`
- `docs/research/2026-03-23-gstack-skill-analysis.md`
- `docs/research/2026-06-10-gstack-cookie-import-vs-agent-browser.md`

## Summary

Oro should have its own browser-skills layer rather than treating either gstack
or `agent-browser` as the product. The durable capability is an Oro-owned
format, runner, auth-state policy, report artifact contract, and dispatcher
integration that lets agents repeatedly inspect, QA, and verify real
application behavior.

The first implementation should wrap the existing `agent-browser` CLI. That
gets ref-based snapshots, clicks, fills, screenshots, state save/load, CDP
attachment, and mobile support without first rebuilding Playwright automation.
After the Oro browser-skill format and reports are stable, add an Oro-managed
persistent daemon for gstack-like latency, project/worktree isolation, and
better lifecycle control.

This v5 makes the gstack adoption targets explicit: persistent browser daemon,
skillification loop, report-only QA, domain-scoped auth bundles,
imported-auth guardrails, local picker UI, browser artifact contract, and
daemon isolation by worktree. The implementation order still starts with the
Oro-owned contracts so the later daemon and picker have a stable product surface
to plug into.

Local cookie import should be first-class, explicit, scoped, inspectable, and
never live-read by workers from a user's real browser profile. Imported state is
copied into an Oro auth bundle, tied to a project/app/host/environment, and
redacted from all prompts and reports.

Important correction from review: committed browser skills must not live under
`.oro/browser-skills/` because Oro init writes `.oro/` to the project's global
gitignore. The committed v1 location is `docs/browser-skills/<name>/`; runtime
state, reports, auth bundles, and temporary generated skills remain under
`$ORO_HOME/projects/<name>/...`.

## Research Summary

Files and references read:

- `docs/plans/2026-06-09-openai-harness-engineering-comparison-design.md`:
  identifies browser skills as part of Oro's broader application/runtime
  legibility gap and proposes Oro-owned skills over an `agent-browser` backend.
- `docs/plans/2026-06-09-front-end-e2e-verification-design.md`: deliberately
  starts with deterministic generated QG lanes and defers a persistent browser
  daemon, so this spec must not replace that gate.
- `docs/research/2026-03-23-gstack-skill-analysis.md`: documents gstack's
  browser daemon, cookie import, QA skills, and recommendation to upgrade Oro's
  browser surface.
- `docs/research/2026-06-10-gstack-cookie-import-vs-agent-browser.md`:
  confirms gstack has a native Chromium-family cookie importer with profile
  discovery, platform decryption, picker routes, and imported-domain guardrails,
  while Oro's packaged `agent-browser` documents login/state reuse and CDP
  attachment but not local browser cookie import.
- `archive/yap/reference/gstack/BROWSER.md`: gstack's persistent Chromium
  daemon, plain CLI/stdout interface, `@e` refs, browser-skills runtime,
  `/scrape` + `/skillify` loop, workspace isolation, auth, and prompt-injection
  defenses.
- `archive/yap/reference/gstack/qa/SKILL.md` and
  `archive/yap/reference/gstack/qa-only/SKILL.md`: browser-backed QA and
  report-only QA patterns.
- `cmd/oro/_assets/skills/agent-browser/SKILL.md` plus references under
  `cmd/oro/_assets/skills/agent-browser/references/`: existing browser
  commands, state save/load, session management, screenshots, video, console,
  network, semantic locators, CDP, and mobile providers.
- `docs/decisions&discoveries.md`: relevant constraints around project-scoped
  daemon state, worktree safety, QG enforcement, and not turning every judgment
  workflow into a hook.

Observed trade-offs:

- Gstack's browser daemon is excellent for repeated, exploratory, and
  report-generating work. Its most important product idea is not "use
  Playwright"; it is turning a successful exploration into a codified,
  low-latency browser skill.
- Oro already has `agent-browser` packaged as a skill. Reusing it first lowers
  risk and lets the design focus on the Oro-native contracts: skill schema,
  auth bundles, dispatcher payloads, reports, and review consumption.
- `agent-browser` is not a substitute for a local browser-cookie import layer.
  It can drive pages and preserve its own state, but the gstack importer shows
  the separate policy surface Oro needs: browser/profile/domain selection,
  copied auth bundles, redaction, and scoped worker access.
- Deterministic front-end E2E remains the merge gate. Browser skills are the
  richer exploratory and reusable harness lane that can provide evidence to the
  gate, ops review, and future app harness.

## Real Problem

The immediate question is "shouldn't we make our own browser-skills, and can
they use local cookies?" The underlying problem is agent legibility for running
apps. Oro workers can edit code and run QG, but they do not have a standard,
repeatable way to open the app, reuse authenticated state, inspect console and
network failures, record screenshots and structured artifacts, and convert a
successful manual browser path into a reusable skill.

The user pain is highest for UI, local web app, dashboard, and auth-gated
features. Without browser skills, each worker rediscovers the app manually,
browser evidence is inconsistent, and authenticated flows either get skipped or
depend on ad hoc credentials in prompts. With browser skills, the system can say:
"run the checkout smoke skill against this worktree app URL with this local
auth bundle, produce a report, and include the artifact manifest in ops review."

## Relationship To Adjacent Specs

This spec is not the front-end E2E merge-gate spec. The front-end E2E design
adds deterministic project commands to generated QG. That remains the first hard
gate for UI work.

This spec is the reusable browser capability layer:

- Humans and agents can explore a page.
- Successful explorations can be codified into browser skills.
- Browser skills can run inside assignments, ops review, canaries, and
  report-only QA.
- The same layer can later sit on top of a per-worktree app harness and local
  observability harness.

The OpenAI harness comparison is the parent strategic context. This deepspec is
the focused implementation shape for its "persistent browser daemon and browser
skills" portion.

## Epic Acceptance Test

The implemented feature is not complete until a single command can prove the
CLI, runner, auth redaction, payload wiring, and review wiring are connected
without a vacuous `go test -run` pass:

```text
Cmd: ./scripts/verify_browser_skills_epic.sh
Assert: exit 0; script runs the required cross-package Go tests from inside a real git worktree checkout with no `.oro/config.yaml`, fails if any named test is absent or skipped, proves fixture skill runs through `oro browser-skill run` using the payload-provided `--base-url`, emits a passing report.json under the expected bead ID, seeded fake cookie values are absent from all report artifacts, AssignPayload contains browser skills/auth bundle IDs when configured, worker prompt includes the browser run command, and ops review prompt includes report paths discovered from disk.
```

This acceptance command intentionally spans packages. Component tests alone can
pass while the feature is dead in production.

## Plan-Level Premortem

```yaml
premortem:
  mode: deep
  context: "Oro browser-skills deepspec before decomposition"
  tigers:
    - risk: "Committed skills under `.oro/browser-skills` never reach worker worktrees because `.oro/` is globally gitignored by Oro init."
      severity: high
      location: "cmd/oro/cmd_init.go global gitignore entries; original spec lines 304-311 before v2"
      mitigation_checked: "Original v1 had no task proving skill files are tracked or visible from a fresh worktree."
      resolution: "v2 moves committed skills to `docs/browser-skills/<name>/` and adds a fresh-worktree visibility acceptance criterion."
    - risk: "The task graph could build schemas, runner, and CLI but never expose browser evidence to workers or ops review."
      severity: high
      location: "docs/plans/2026-06-10-browser-skills-deepspec.md dispatcher/worker integration section"
      mitigation_checked: "v1 named BRS-6 but did not enumerate `AssignPayload`, worker prompt, solo `oro work`, or `ReviewOpts` call sites."
      resolution: "v2 names the exact integration files and adds prompt/review tests to the epic acceptance command."
    - risk: "Skillify ships against fixture transcripts but real browser sessions never produce transcripts."
      severity: high
      location: "docs/plans/2026-06-10-browser-skills-deepspec.md skillify flow"
      mitigation_checked: "v1 had no producer for `record --from-session <id>`."
      resolution: "v2 requires session journaling in the browser CLI task before skillify."
  elephants:
    - risk: "Browser skills depend on an app URL, but Oro's per-worktree app harness is not implemented yet."
      resolution: "v2 adds a minimal browser app config/base-url contract and keeps full app lifecycle as adjacent future work."
  paper_tigers:
    - risk: "Using `agent-browser` first is not as fast as gstack's daemon."
      reason: "The adapter is a deliberate wedge; the daemon phase implements the same backend interface after the skill/report/auth contract is proven."
```

## Claude Fable Adversarial Review

`claude --model fable` ran the six-check `adversarial-spec-review` workflow four
times on 2026-06-10.

Round 1 returned `FAIL` against v1. Load-bearing findings incorporated in v2:

- `.oro/browser-skills` conflicted with Oro's global `.oro/` gitignore and would
  make committed skills invisible in worker worktrees.
- The assignment payload example depended on app URL/log command producers that
  no task created.
- Ops review had no concrete `ReviewOpts` field or dispatcher plumbing for
  browser report paths.
- Solo `oro work` and `pkg/dispatcher/router.go` build prompts independently of
  `buildAssignPayload`; wiring only the dispatcher payload path would miss them.
- `browser-skill record --from-session` had no production transcript producer.
- Dispatcher cleanup had no task to stop sessions or clear transient state.
- The adapter phase needed explicit per-worktree/session isolation, not only the
  deferred daemon phase.

Round 2 returned `FAIL` against v2. It confirmed the round-1 fixes landed and
found narrower evidence-chain gaps incorporated in v3:

- Browser reports were keyed by `<bead-id>`, but `browser-skill run` had no
  `--bead`, env, or worktree-basename rule to supply that ID.
- `ReviewOpts.BrowserReports` could render if injected, but no production path
  discovered `report.json` files from disk before review.
- `cmd/oro/paths.go` is `package main`, so `pkg/browserharness` and
  `pkg/dispatcher` cannot import its `$ORO_HOME/projects/<name>/` helpers.
- Dispatcher cleanup said "sessions it starts," but workers start browser
  sessions through CLI subprocesses; teardown must discover sessions from the
  handle store.
- The epic acceptance command could pass vacuously if `go test -run` matched no
  tests.
- V1 promised traces without a backend method or task; traces are deferred.

Round 3 returned `FAIL` against v3. It confirmed the round-1 and round-2 fixes
landed and found one remaining production-context gap incorporated in v4:

- Workers invoke `oro browser-skill run` from inside worktrees where
  `.oro/config.yaml` is absent, so the payload `run_command` must carry the
  dispatcher-resolved `--base-url`.
- The bead env tier should use the real production variable
  `ORO_WORKER_BEAD_ID`, not a new `ORO_BEAD_ID`.
- Skill discovery must define standard and stealth-mode roots explicitly.
- `pkg/projpaths` must preserve `ORO_PROJECT` precedence so worker-side report
  paths and dispatcher-side discovery agree.

Ralph Loop requirement: after v4 is decomposed into beads, rerun a fresh-context
adversarial review before implementation begins.

Round 4 returned `PASS` against v4. Non-blocking notes folded in after the pass:

- The epic script should use a stub `agent-browser` executable or explicit fake
  backend so the run-proof is hermetic.
- The anti-skip guard must exclude the optional real-`agent-browser` integration
  smoke because that test is allowed to skip on machines without the binary.
- `browser.apps` requires structured YAML parsing of `.oro/config.yaml` without
  breaking existing line-based readers.
- Report discovery should label/order reports so stale failed attempts do not
  confuse review.
- Missing `value_env` must fail before any browser step executes.

## Current Oro State

Oro already has the ingredients but not the product boundary:

- `agent-browser` is installed as an Oro-distributed skill asset and supports
  navigation, snapshots, `@e` refs, interactions, waits, screenshots, PDFs,
  state save/load, session persistence, CDP attachment, headed mode, recording,
  mobile providers, console, network, and storage commands.
- Oro workers already get assignment payloads, worktree isolation, QG, ops
  review, durable journey logs, and cards.
- Oro has project-scoped daemon path precedent in `docs/decisions&discoveries.md`
  from the per-project daemon isolation decision.
- There is no Oro-owned `browser-skill` schema.
- There is no structured browser run report contract consumed by dispatcher or
  ops review.
- There is no Oro auth bundle policy for copied cookies/local storage.
- There is no local auth picker UI, browser/profile/domain selection flow, or
  imported-domain guardrail comparable to gstack's cookie picker.
- There is no app/worktree-aware browser lifecycle in the dispatcher.
- There is no way to distinguish a task-specific journey from a reusable
  browser skill that can be matched by trigger and maintained over time.

## Goals

- Define an Oro-owned browser-skill schema and runner.
- Use `agent-browser` as the first backend through a small adapter.
- Support explicit, scoped local cookie/auth import into Oro-managed bundles.
- Provide a local picker UI for browser/profile/domain auth import without
  exposing cookie values.
- Track imported-auth domains and block dangerous cross-origin browser actions
  when copied sensitive state is loaded.
- Keep auth artifacts out of git, prompts, and reports.
- Produce structured reports with screenshots, console, network, and
  assertion results.
- Provide report-only browser QA that creates evidence and review input without
  editing source.
- Integrate browser evidence into worker assignments and ops review.
- Preserve deterministic QG as the hard merge gate while making browser skills
  reusable evidence and QA tools.
- Leave room for a native persistent daemon after the runner/report contract is
  proven.

## Non-Goals

- Do not port gstack wholesale.
- Do not make production browser automation available to workers by default.
- Do not read or mutate a user's live browser profile during normal worker runs.
- Do not give workers permission to import from local browser profiles; workers
  may only consume pre-approved copied Oro auth bundles.
- Do not replace Playwright/Cypress/project E2E commands in generated QG.
- Do not solve arbitrary third-party website automation without explicit user
  authorization and host allowlists.
- Do not require every repository to define browser skills.
- Do not put cookies, local storage, bearer tokens, page HTML with secrets, or
  screenshots containing secrets into prompts.

## Approach Options

### Option A: Oro browser skills with `agent-browser` adapter first

Build the Oro schemas, CLI commands, reports, auth bundles, and dispatcher
hooks. The runner shells out to `agent-browser` for actual browser control.

Premortem:

- Tiger: Wrapper inherits `agent-browser` process/session quirks. Mitigation:
  keep a backend interface and fake backend tests; wrap commands with timeouts
  and structured parsing.
- Tiger: Auth state leaks into reports. Mitigation: central redaction, artifact
  manifest instead of raw auth contents, chmod 0600, report schema tests.
- Elephant: Skill format changes after usage. Mitigation: version the schema
  from day one and keep v1 intentionally small.
- Paper tiger: Shelling out feels inelegant. That is acceptable while proving
  the product contract.

Recommendation: choose this. It provides the user's desired capability quickly
without committing to daemon internals before the Oro abstraction is clear.

### Option B: Native persistent daemon first

Build an Oro Playwright daemon before browser-skill schema and reports.

Premortem:

- Tiger: Large surface area before proving workflow value: daemon lifecycle,
  ports, auth, storage, browser install, logs, crashes, multi-worktree cleanup.
- Tiger: The daemon may become a second product with no dispatcher/report
  contract.
- Elephant: Latency matters most after repeated browser runs are common.

Recommendation: defer. Build after Option A proves the runner and skill format.

### Option C: Use `agent-browser` prompts only

Teach workers to invoke `agent-browser` directly and skip a new Oro layer.

Premortem:

- Tiger: No reusable skill format, no auth policy, no evidence schema, no ops
  review contract.
- Tiger: Workers keep improvising and producing inconsistent browser evidence.
- Elephant: This is the status quo with slightly better instructions.

Recommendation: reject as the product direction. Keep direct `agent-browser`
usage as a debugging escape hatch.

## Gstack Adoption Targets

The following gstack ideas are in scope, adapted to Oro's worktree and
dispatcher model:

1. Persistent browser daemon: keep a warm browser context per
   project/worktree/app profile after the backend interface is proven.
2. Browser skillification loop: record a successful session, synthesize a
   temporary skill, test it, and only then move it into
   `docs/browser-skills/<name>/`.
3. Report-only QA: run browser checks that produce screenshots, console and
   network findings, DOM notes, and assertion results without source edits.
4. Domain-scoped auth bundles: import selected domains into copied Oro auth
   bundles, with domain counts before decrypting and no live browser reads by
   workers.
5. Imported-auth guardrails: track allowed domains for each loaded bundle and
   block arbitrary cross-origin JavaScript or storage inspection once sensitive
   cookies are present.
6. Local picker UI: expose a localhost-only picker for browser, profile, and
   domain selection; show metadata and counts, never cookie values.
7. Browser artifact contract: standardize `report.json`, screenshots, traces,
   console, network, DOM summaries, redacted backend logs, and their paths.
8. Daemon isolation by worktree: isolate ports, tokens, browser contexts,
   session handles, logs, cookies, tabs, and storage by project/worktree/run.

Premortem:

- Tiger: These additions turn the v1 wedge into a daemon rewrite. Mitigation:
  BRS-0 through BRS-6 still ship first; daemon, picker, and native import are
  later tasks behind the same interfaces and report schema.
- Tiger: Local cookie import leaks high-value credentials. Mitigation:
  human-only import, copied bundles, host allowlists, redaction tests,
  chmod 0700/0600, production opt-in, and imported-domain guardrails.
- Elephant: Report-only QA is valuable only if review consumes it. Mitigation:
  BRS-6 wires report discovery into worker prompts and ops review before adding
  dashboard UX.

## Architecture

### Package Shape

Add a small internal browser harness rather than mixing browser behavior into
the dispatcher:

```text
pkg/browserharness/
  schema.go          # skill, flow, auth bundle, report schemas
  runner.go          # skill runner and assertion engine
  backend.go         # BrowserBackend interface
  agentbrowser.go    # agent-browser backend adapter
  daemon.go          # later persistent daemon backend implementation
  config.go          # browser app/base-url config
  auth.go            # bundle metadata, host/environment validation
  picker.go          # localhost auth picker routes and session auth
  guardrails.go      # imported-auth domain enforcement
  report.go          # artifact manifest and redaction
  match.go           # trigger/app/host matching

pkg/projpaths/
  paths.go           # importable $ORO_HOME project path resolution

cmd/oro/
  cmd_browser.go
  cmd_browser_skill.go
  cmd_browser_auth.go
```

The dispatcher should call package APIs, not shell out to `oro browser-skill`
internally. The CLI is for humans, workers, and test fixtures.

### Backend Interface

The first backend shells out to `agent-browser`; the later daemon implements the
same interface.

```go
type BrowserBackend interface {
    Start(ctx context.Context, spec SessionSpec) (Session, error)
    Open(ctx context.Context, s Session, url string) error
    Snapshot(ctx context.Context, s Session, opts SnapshotOpts) (Snapshot, error)
    Click(ctx context.Context, s Session, target Target) error
    Fill(ctx context.Context, s Session, target Target, value string) error
    Select(ctx context.Context, s Session, target Target, value string) error
    Wait(ctx context.Context, s Session, wait WaitSpec) error
    Screenshot(ctx context.Context, s Session, path string, opts ScreenshotOpts) error
    Console(ctx context.Context, s Session) ([]ConsoleEvent, error)
    Network(ctx context.Context, s Session) ([]NetworkEvent, error)
    Storage(ctx context.Context, s Session) (StorageState, error)
    Stop(ctx context.Context, s Session) error
}
```

Adapter rules:

- Every backend command gets a timeout.
- Every command records stdout/stderr in a redacted debug log.
- `@e` refs are scoped to a session and invalidated after actions that can
  navigate or rerender.
- Backend output is normalized before reaching reports or prompts.
- The adapter never writes auth bundles directly; it asks the auth layer for a
  temporary backend-readable state file.
- The adapter uses a unique `agent-browser --session` name per
  project/worktree/run so concurrent workers do not share cookies, storage, or
  element refs.
- The CLI stores session handles in `$ORO_HOME/projects/<name>/browser-sessions/`
  so `oro browser start`, `snapshot`, `click`, and `stop` work across separate
  process invocations.

### App URL Contract

Full per-worktree app lifecycle belongs to the app harness. Browser skills still
need a v1 URL producer so the feature is not dead code while that harness is
pending.

Add a minimal browser config section to project config:

```yaml
browser:
  apps:
    web:
      base_url: "http://127.0.0.1:5173"
      base_url_env: ORO_APP_URL
      logs_command: "oro app logs --worktree ${worktree}"
```

Rules:

- `base_url_env`, when set and non-empty, wins over `base_url`.
- `--base-url` on `oro browser-skill run` wins over both.
- `logs_command` is optional and may reference future app-harness commands; v1
  must not require `oro app logs` to exist.
- If no URL can be resolved, `browser-skill run` fails before loading auth.
- Dispatcher and solo `oro work` payloads use this same resolver.

### CLI Surface

Browser session commands:

```text
oro browser start --worktree <path> --app web
oro browser open --worktree <path> --app web /
oro browser snapshot --worktree <path> --interactive
oro browser click --worktree <path> @e12
oro browser fill --worktree <path> @e3 value
oro browser console --worktree <path> --errors
oro browser screenshot --worktree <path> --full
oro browser qa --worktree <path> --app web --report-only
oro browser stop --worktree <path>
```

Browser skill commands:

```text
oro browser-skill list
oro browser-skill match "checkout works" --app web
oro browser-skill run checkout-smoke --worktree <path> --app web --bead <bead-id>
oro browser-skill record checkout-smoke --from-session <id>
oro browser-skill test checkout-smoke
oro browser-skill report <run-id>
```

Auth commands:

```text
oro browser-auth import chrome --profile Default --app web --host localhost:3000 --environment local
oro browser-auth import agent-browser-state ./auth.json --app web --host localhost:3000 --environment local
oro browser-auth picker --app web --environment local
oro browser-auth list
oro browser-auth inspect web-localhost
oro browser-auth revoke web-localhost
```

The initial `chrome` importer can be platform-gated. If macOS Keychain cookie
decryption is not ready in v1, the command should say so and point users to the
`agent-browser-state` path. The important contract is the Oro bundle format and
policy, not completing every browser importer at once.

Bead ID rules:

- `oro browser-skill run` accepts `--bead <id>`.
- If `--bead` is absent, `ORO_WORKER_BEAD_ID` is used when set.
- If both are absent, Oro infers the bead ID from the basename of `--worktree`.
  This matches production worktree paths from both dispatcher and solo `oro work`.
- The report path is always
  `$ORO_HOME/projects/<name>/browser-runs/<bead-id>/<run-id>/report.json`.

## Browser Skill Format

Committed skills live under `docs/browser-skills/<name>/`:

```text
docs/browser-skills/checkout-smoke/
  skill.yaml
  flow.yaml
  assertions.yaml
  README.md
```

`skill.yaml`:

```yaml
schema: oro.browser-skill/v1
name: checkout-smoke
description: Verify the cart checkout path reaches confirmation.
app: web
triggers:
  - checkout works
  - purchase flow
  - cart smoke
auth:
  required: true
  bundle: web-local
  environments: [local]
hosts:
  allow:
    - localhost
    - 127.0.0.1
mutation:
  allowed: true
  external_systems: false
artifacts:
  screenshots: on_failure
  console: always
  network: on_failure
```

`flow.yaml`:

```yaml
start_url: /cart
steps:
  - click:
      selector: "[data-testid=checkout]"
  - fill:
      selector: "[name=email]"
      value_env: ORO_TEST_EMAIL
  - click:
      selector: "[data-testid=submit-order]"
  - wait:
      text: "Order confirmed"
      timeout_ms: 10000
```

`assertions.yaml`:

```yaml
assertions:
  - text: "Order confirmed"
  - console_errors: none
  - network_errors: none
  - screenshot:
      name: confirmation
budgets:
  max_step_ms: 3000
  max_total_ms: 15000
```

Rules:

- `schema` is required.
- `name`, `app`, and at least one `trigger` are required.
- `hosts.allow` is required when auth is used.
- Mutating skills must declare `mutation.allowed: true`.
- External-system mutations require a separate explicit human command; workers
  cannot infer consent from the skill file alone.
- `value_env` is preferred over hard-coded secret values.

## Auth And Cookie Import

### Auth Bundle Model

Auth bundles are copied state, not live links to the user's browser profile.
They live outside committed source:

```text
$ORO_HOME/projects/<name>/browser-auth/<bundle-id>/
  bundle.yaml
  storage-state.json
  imported-from.txt
```

`bundle.yaml`:

```yaml
schema: oro.browser-auth/v1
id: web-local
project: oro
app: web
environment: local
hosts:
  allow:
    - localhost:3000
    - 127.0.0.1:3000
created_at: "2026-06-10T12:00:00Z"
source:
  kind: chrome
  profile: Default
permissions:
  worker_use: true
  production: false
redaction:
  report_cookie_names: false
  report_storage_keys: false
```

Security rules:

- Bundle directories are chmod 0700; files are chmod 0600.
- Bundle contents are never committed and never copied into `docs/browser-skills`.
- Reports may say which bundle ID was used, but not cookie names, values, local
  storage keys, or local storage values.
- Production bundles are disabled by default and require a human command with an
  explicit `--allow-production` flag.
- Worker use requires both the bundle and the app profile to opt in.
- Host allowlists are enforced before loading auth state into a backend session.
- Imported auth state expires by policy; v1 can warn after 30 days before adding
  hard expiry.
- Bundles record allowed domains imported from the source browser. Browser
  sessions loading a bundle must enforce those domains before running arbitrary
  JavaScript, reading storage, or navigating to unrelated hosts with the bundle
  still active.

### Import Paths

Support these import paths in order:

1. `agent-browser state save` JSON imported via
   `oro browser-auth import agent-browser-state`.
2. Playwright-compatible `storageState` JSON imported via
   `oro browser-auth import storage-state`.
3. Chrome profile import on macOS with Keychain-backed cookie decryption when
   available.
4. Chromium-family import on Linux/Windows after platform decryption and v20
   fallback risks are designed and reviewed.
5. Safari/Firefox import later.

This answers the local-cookie requirement while limiting blast radius. The user
can authenticate locally once, export/copy that state into Oro, inspect the
bundle metadata, and allow specific workers or skills to use it.

### Local Picker UI

`oro browser-auth picker` starts a localhost-only picker server and opens the
system browser. The picker is for humans, not workers.

Requirements:

- One-time code for first access; short-lived picker session cookie after that.
- Main Oro command tokens are not valid for picker routes, and picker session
  cookies are not valid for normal browser commands.
- Browser list comes from a hardcoded supported registry or platform detector,
  not arbitrary user-supplied profile paths.
- Profile names reject traversal, slashes, backslashes, and control characters.
- Domain/count metadata is visible before decryption; cookie values are never
  rendered.
- Import writes a copied auth bundle and closes all plaintext temp files before
  returning.

### Imported-Auth Guardrails

When a session loads an auth bundle with imported cookies, the backend receives
an `AuthScope` containing allowed hosts/domains and a reason string. Guardrails
apply before:

- JavaScript evaluation.
- Storage inspection/export.
- Cookie inspection/export.
- Navigating a loaded-auth session to a host outside the bundle allowlist.

The error should explain which bundle is loaded, which domains are allowed, and
how to start a separate unauthenticated session if the user wants to inspect
another host.

## Dispatcher And Worker Integration

Assignment payload additions:

```json
{
  "app": {
    "profile": "web",
    "url": "http://127.0.0.1:5173",
    "logs_command": "oro app logs --worktree ..."
  },
  "browser": {
    "available_skills": ["checkout-smoke", "settings-save"],
    "auth_bundles": ["web-local"],
    "run_command": "oro browser-skill run <name> --worktree ... --bead <bead-id> --base-url <resolved-url>"
  }
}
```

Concrete integration points:

- `pkg/protocol/message.go`: add optional browser/app fields to
  `AssignPayload`, with compatibility tests that pin JSON field names.
- `pkg/dispatcher/assign_payload.go`: discover valid `docs/browser-skills`
  entries, resolve app URLs from browser config, list auth bundle IDs, and
  include report/run commands with `--bead <id>` and dispatcher-resolved
  `--base-url <url>`.
- `pkg/worker/worker.go`: map payload browser fields into prompt params so
  spawned workers actually see the browser section.
- `pkg/dispatcher/router.go` and `cmd/oro/cmd_work.go`: update non-dispatcher
  `AssemblePrompt`/review construction paths or explicitly pass empty browser
  context. V1 should support solo `oro work`; do not scope it out silently.

Worker prompt additions should be short and tool-oriented:

- Use browser skills when acceptance criteria mention a supported flow.
- For UI-impacting work, include the browser report path in the final summary.
- Do not paste cookies, storage state, page HTML with secrets, or private
  screenshots into the response.

Ops review additions:

- If a task is UI-impacting and a relevant browser skill exists, review expects
  a passing browser report or an explicit waiver.
- Ops review consumes `report.json` and artifact manifest, not raw screenshots
  unless needed.
- Browser evidence can strengthen a finding but does not override deterministic
  failing QG.
- `pkg/ops/ops.go` gets a `BrowserReports []string` field on `ReviewOpts`.
- `pkg/ops/review_prompt.go` renders a browser report section when
  `BrowserReports` is non-empty.
- The dispatcher and `cmd/oro/cmd_work.go` pass report paths into `ReviewOpts`.
- Before spawning review, `pkg/dispatcher/dispatcher.go`,
  `pkg/dispatcher/ops_runs.go`, and `cmd/oro/cmd_work.go` discover
  `$ORO_HOME/projects/<name>/browser-runs/<bead-id>/*/report.json` on disk and
  pass those paths into `ReviewOpts`.

V1 UI-impacting detection is explicit. Ops review requires browser evidence only
when the task acceptance criteria include `BrowserSkill: <name>` or
`BrowserEvidence: required`. Touched-file heuristics can be added later, but the
first gate must be binary and testable.

Cleanup:

- Dispatcher cleanup stops browser sessions it started.
- Worktree removal also removes transient session state.
- Auth bundles remain project-scoped and survive sessions until revoked.
- Hook cleanup through `pkg/dispatcher/dispatcher.go` worktree teardown,
  including `removeWorktreeAndClearTracking`, and clear browser-session
  bookkeeping even if backend stop fails.

## Reports And Artifacts

Reports live outside committed source by default:

```text
$ORO_HOME/projects/<name>/browser-runs/<bead-id>/<run-id>/
  report.json
  report.md
  screenshots/
  traces/
  dom-summary.json
  console.jsonl
  network.jsonl
  backend-debug.redacted.log
```

`report.json` includes:

```json
{
  "schema": "oro.browser-report/v1",
  "run_id": "br_...",
  "bead_id": "oro-...",
  "skill": "checkout-smoke",
  "app": "web",
  "url": "http://127.0.0.1:5173/cart",
  "status": "passed",
  "auth_bundle": "web-local",
  "started_at": "2026-06-10T12:00:00Z",
  "duration_ms": 4210,
  "steps": [],
  "assertions": [],
  "artifacts": [],
  "dom_summary": "dom-summary.json",
  "redactions": ["cookies", "local_storage", "authorization_headers"]
}
```

The markdown report is for humans. The JSON report is the contract for workers,
ops review, dashboards, and future canaries.

Report-only QA is a first-class mode over the same contract. It may open pages,
run assertions, capture screenshots/traces/console/network summaries, and write
reports, but it must not edit source files, commit changes, mutate external
systems unless the skill declares mutation, or paste raw artifacts into prompts.

## Skillify Flow

Oro should eventually have a `browser-skillify` workflow like gstack, but it
must be provenance-guarded:

1. Start from a completed browser session or journey with a successful report.
2. Extract only the final successful command slice and user intent.
3. Synthesize a temporary skill under an uncommitted temp directory.
4. Run `oro browser-skill test <temp-skill>`.
5. Only then move it into `docs/browser-skills/<name>/`.

Do not synthesize browser skills from vague chat history. This avoids permanent
skills that never actually worked.

Production prerequisite: `oro browser` commands must append a redacted session
journal under `$ORO_HOME/projects/<name>/browser-runs/sessions/<session-id>/`.
`record --from-session` reads only that journal. Fixture-only transcripts are
not sufficient.

## Native Daemon Phase

After the schema, reports, and auth layer are proven, add an Oro daemon backend:

- One daemon per project/worktree/app profile.
- State file under `$ORO_HOME/projects/<name>/browser-daemon/<worktree-hash>/`.
- Random port and bearer token, chmod 0600.
- Idle shutdown.
- Crash detection and restart on next command.
- Plain CLI/stdout remains the user-facing interface.
- Browser context isolated per worktree.
- Separate contexts per run when auth bundles differ.
- No shared cookies, local storage, tabs, element refs, traces, logs, or command
  tokens across worktrees.
- Daemon stores logs under the same browser run artifact tree.

This should implement the same `BrowserBackend` interface and require no skill
format changes.

## Task Graph

### BRS-0: Resolve committed skill location, app URL config, and state paths

Acceptance criteria:

- Add `scripts/verify_browser_skills_epic.sh`; it runs the epic acceptance
  tests, fails if any required test is absent/skipped, and fails on any missing
  redaction/report/prompt assertion.
- The script uses a stub `agent-browser` executable on `PATH` or an explicit
  fake backend so the run-proof is hermetic. Its required-test anti-skip guard
  excludes the optional real-`agent-browser` integration smoke.
- Committed skills live under `docs/browser-skills/<name>/`; no v1 committed
  skill path is under `.oro/`.
- `cmd/oro/cmd_init.go` global gitignore behavior is either left unchanged with
  `docs/browser-skills` verified as tracked, or explicitly tested if any
  exception is added later.
- A test proves a skill committed in `docs/browser-skills` is visible from a
  fresh git worktree checkout.
- Browser state paths preserve the existing `$ORO_HOME/projects/<name>/`
  behavior by extracting the relevant helpers from `cmd/oro/paths.go` into an
  importable package such as `pkg/projpaths`.
- `cmd/oro`, `pkg/dispatcher`, and `pkg/browserharness` all use the same
  `pkg/projpaths` helpers, with tests pinning standard and stealth-mode paths
  to the same values as the current CLI behavior.
- `pkg/projpaths` preserves current `ORO_PROJECT`-first precedence; a test
  proves project-name resolution from a worker worktree CWD with `ORO_PROJECT`
  set equals resolution from the main repo root.
- Skill discovery root is explicit: standard mode reads
  `<repo-root>/docs/browser-skills`; stealth mode reads the configured Oro docs
  root only when that root is populated, otherwise browser skills are disabled
  with remediation text.
- Project config can define `browser.apps.<name>.base_url`,
  `base_url_env`, and optional `logs_command`.
- The config schema/read path is covered in `pkg/config` and command setup
  paths, not only in browserharness fixtures.
- Implementing `browser.apps` introduces structured YAML parsing of
  `.oro/config.yaml`; keep existing line-based readers compatible until they are
  deliberately replaced.
- `oro browser-skill run` accepts `--base-url`, falls back to config/env, and
  fails before loading auth when no base URL is available.
- BRS-0 tests invoke `oro browser-skill run` from inside a real worktree checkout
  that has no `.oro/config.yaml`, proving payload-provided `--base-url` is
  sufficient in production worker context.

### BRS-1: Define schemas and load/validate browser skills

Acceptance criteria:

- `pkg/browserharness` loads `skill.yaml`, `flow.yaml`, and `assertions.yaml`.
- Invalid schema version, missing trigger, missing host allowlist with auth, and
  invalid mutation policy fail with actionable errors.
- Unit tests cover valid and invalid fixtures.
- Fixture skills are read from `docs/browser-skills`, not `.oro/browser-skills`.

### BRS-2: Add auth bundle model and import from storage state

Acceptance criteria:

- `oro browser-auth import agent-browser-state` creates a bundle under
  `$ORO_HOME/projects/<name>/browser-auth`.
- Bundle permissions are 0700/0600 on Unix.
- Bundle metadata records source kind, app, environment, host allowlist,
  imported domains, worker-use permission, production permission, and redaction
  settings.
- Host and environment checks are enforced before use.
- Production bundles are disabled by default and require explicit
  `--allow-production`.
- Worker use requires both bundle opt-in and app/profile opt-in.
- Reports can reference bundle ID but cannot include cookie/storage contents.
- A fake bundle containing seeded cookie/storage secrets is used in tests, and
  generated reports/prompts must not contain those seed values.

### BRS-3: Implement fake backend and runner assertion engine

Acceptance criteria:

- Runner executes a v1 skill against a fake backend.
- Assertions cover text, console errors, network errors, screenshots, and timing
  budgets.
- A step referencing unset `value_env` fails before any browser action executes,
  and the error names the missing variable.
- Failure reports include failed step/assertion and artifact manifest.

### BRS-4: Implement `agent-browser` backend adapter

Acceptance criteria:

- Adapter can open, snapshot, click, fill, wait, screenshot, collect console,
  and stop using `agent-browser`.
- Commands have timeouts and redacted debug logs.
- Adapter sessions are named uniquely per project/worktree/run and two
  concurrent sessions do not share storage state.
- Adapter receives `AuthScope` when a bundle is loaded and enforces
  imported-auth guardrails before JavaScript/storage/cookie inspection commands.
- Tests use a fake command runner; one optional integration test is skipped when
  `agent-browser` is unavailable.

### BRS-5: Add CLI commands and session transcript journaling

Acceptance criteria:

- `oro browser-skill list/run/test/report` works against fixtures.
- `oro browser-skill match` works against triggers, app, and host constraints.
- `oro browser-auth list/inspect/revoke` works.
- `oro browser-skill run` derives bead ID from `--bead`, `ORO_WORKER_BEAD_ID`,
  or the `--worktree` basename, in that order.
- A run against `.worktrees/oro-test1` without `--bead` writes
  `$ORO_HOME/projects/<name>/browser-runs/oro-test1/<run-id>/report.json`.
- `oro browser start/open/snapshot/click/fill/screenshot/stop` delegates through
  the backend across separate CLI process invocations using stored session
  handles.
- Session handles are keyed by normalized worktree path or worktree hash so
  teardown can discover worker-started sessions later.
- Every `oro browser` command appends a redacted transcript entry under
  `$ORO_HOME/projects/<name>/browser-runs/sessions/<session-id>/`.
- The transcript includes command, normalized target, timestamp, artifact
  references, and redaction metadata, but not auth contents.
- CLI errors are concise and include remediation.

### BRS-6: Wire reports into worker assignment and ops review

Acceptance criteria:

- `pkg/protocol/message.go` adds optional browser/app fields to `AssignPayload`
  with JSON compatibility tests.
- `pkg/dispatcher/assign_payload.go` discovers valid `docs/browser-skills`
  entries, resolves app URL from BRS-0 config, lists allowed auth bundle IDs,
  and includes browser run/report commands.
- Dispatcher config plumbing carries browser config and project paths through
  `pkg/dispatcher/dispatcher.go` config/defaults, `pkg/config`, and
  `cmd/oro/cmd_start.go`.
- `pkg/worker/worker.go` maps payload browser fields into `PromptParams`; a
  golden prompt test proves the browser section appears.
- `pkg/dispatcher/router.go` and `cmd/oro/cmd_work.go` solo prompt paths either
  include browser context or explicitly pass empty browser context with tests.
- `pkg/ops/ops.go` adds `ReviewOpts.BrowserReports []string`.
- `pkg/ops/review_prompt.go` renders browser report paths when present.
- Dispatcher review call sites in `pkg/dispatcher/dispatcher.go`,
  resumed/recovered review paths in `pkg/dispatcher/ops_runs.go`, and
  `cmd/oro/cmd_work.go` pass browser report paths into `ReviewOpts`.
- Review population tests seed
  `$ORO_HOME/projects/<name>/browser-runs/<bead-id>/<run-id>/report.json` on
  disk and assert the real discovered path reaches the review prompt; injecting
  fake `BrowserReports` directly is not sufficient.
- Report discovery labels each report with status, skill, run ID, and timestamp,
  and orders reports newest-first so stale failed attempts do not look like the
  current result.
- Acceptance criteria containing `BrowserSkill: <name>` or
  `BrowserEvidence: required` cause review guidance to require a passing report
  or explicit waiver.
- Tests prove no auth contents enter worker or review prompts.

### BRS-7: Stop browser sessions during dispatcher cleanup

Acceptance criteria:

- Worktree teardown, including `removeWorktreeAndClearTracking`, discovers
  browser sessions from the BRS-5 handle store keyed by worktree path/hash.
- Teardown stops worker-started browser sessions and removes transient session
  state.
- Dispatcher shutdown and closed-worktree garbage collection sweep dangling
  browser-session handles and stop live sessions when possible.
- Cleanup bookkeeping is cleared even when backend stop returns an error.
- Tests cover merge cleanup, abandoned worktree cleanup, and stop failure.

### BRS-8: Add skillify prototype

Acceptance criteria:

- `oro browser-skill record --from-session` can create a temporary skill from a
  successful production session transcript written by BRS-5.
- The temp skill must pass `oro browser-skill test` before moving into
  `docs/browser-skills`.
- Failed synthesis leaves no partial committed skill.

### BRS-9: Add report-only QA command

Acceptance criteria:

- `oro browser qa --report-only` runs a browser skill or explicit URL/assertion
  bundle without modifying source files.
- The command writes the same `report.json`/artifact tree as
  `browser-skill run`.
- Reports include screenshots, console/network summaries, DOM summary, trace
  references when captured, assertion status, and mutation mode.
- Report-only QA transcripts are marked `mutation: false` unless the invoked
  skill explicitly declares allowed mutation.
- Ops review can consume report-only QA output through the same BRS-6 report
  discovery path.
- Tests prove a report-only QA run with a fake backend cannot write inside the
  repo except through configured report paths outside committed source.

### BRS-10: Add local auth picker and native browser import

Acceptance criteria:

- `oro browser-auth picker` starts a localhost-only picker with one-time-code
  access and separate picker session cookies.
- Picker routes list supported browsers, profiles, and domain/count metadata
  without displaying cookie values.
- Import creates a copied Oro auth bundle with imported-domain metadata and
  closes/removes plaintext temp files.
- Worker commands cannot invoke picker or live profile import paths.
- Chrome/Chromium-family import is platform-gated; unsupported platforms fail
  with remediation to `agent-browser-state` or `storage-state` import.
- Tests cover picker auth separation, profile path validation, redaction, bundle
  metadata, and unsupported-platform errors.

### BRS-11: Add native daemon backend

Acceptance criteria:

- Daemon implements `BrowserBackend`.
- Per-project/worktree state is isolated.
- Daemon keying includes project, worktree hash, app profile, run ID when
  required, and loaded auth bundle identity.
- Idle shutdown, dead-daemon detection, and cleanup are tested.
- Existing browser skills run unchanged on the daemon backend.
- Tests prove two worktrees cannot see each other's cookies, tabs, logs, traces,
  ports, bearer tokens, session handles, or element refs.

## Testing Strategy

- Schema tests with checked-in fixtures.
- Auth tests using temp `$ORO_HOME`, permission checks, and redaction assertions.
- Runner tests against fake backend for deterministic flow coverage.
- Adapter tests with fake command runner output.
- CLI tests for human-facing errors and JSON output.
- Dispatcher/prompt tests proving payload and report paths are present but auth
  contents are absent.
- One local integration smoke for `agent-browser` if the binary exists.
- Dogfood with a tiny local HTML/server fixture before enabling review guidance.

## Risks And Mitigations

- Secret leakage: centralize redaction, never put storage state in reports,
  enforce artifact paths outside git, and add tests that search generated
  reports for known fake cookie values.
- Browser flake: browser skills provide evidence, but deterministic QG remains
  the hard gate. Skills get retries only when explicitly configured.
- Platform auth complexity: start with `agent-browser`/Playwright storage state
  import and gate Chrome Keychain import by platform.
- Scope creep into web scraping: require host allowlists and mutation policy;
  classify scraped page text as untrusted prompt input.
- Daemon lifecycle bugs: defer daemon until the adapter contract exists; use the
  existing project-scoped daemon path precedent.
- Picker security bugs: keep picker routes localhost-only, require one-time
  code/session auth, separate picker auth from command auth, and never expose
  cookie values.

## Resolved V1 Decisions

- Committed browser skills live under `docs/browser-skills/<name>/`.
  `.oro/browser-skills` is not a v1 committed location because `.oro/` is
  globally gitignored by Oro init.
- V1 browser evidence enforcement is explicit: acceptance criteria must include
  `BrowserSkill: <name>` or `BrowserEvidence: required`. Touched-file heuristics
  are deferred.
- Production auth is unavailable to workers by default. A production bundle
  requires an explicit human import command with `--allow-production`; worker
  use also requires bundle and app/profile opt-in.
- Native daemon implementation language remains deferred until BRS-0 through
  BRS-10 prove the backend interface, report contract, and auth policy.
- `oro dash` rendering is deferred. V1 stores `report.json` paths in review
  context and leaves dashboard UX to a later spec.

## Narrowest Wedge

Ship BRS-0 through BRS-6 first:

1. committed skill location and app/base-url config.
2. v1 browser-skill schema.
3. auth bundle model with storage-state import.
4. runner and report contract.
5. `agent-browser` backend adapter.
6. CLI/session transcript journaling.
7. worker assignment and ops review wiring.

That gives Oro its own browser-skills without overbuilding the daemon. It also
answers the local-cookie requirement safely because the first usable auth path
is explicit copied state, not live browser-profile access.

Then ship BRS-7 through BRS-11 for gstack parity:

1. cleanup for browser sessions.
2. skillify prototype.
3. report-only QA command.
4. local auth picker and native browser import.
5. native persistent daemon with worktree isolation.
