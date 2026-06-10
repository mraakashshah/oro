# Oro Browser Skills Deepspec

Date: 2026-06-10
Status: Draft deepspec. Implementation pending. Fresh-context adversarial review pending.
Related specs:

- `docs/plans/2026-06-09-openai-harness-engineering-comparison-design.md`
- `docs/plans/2026-06-09-front-end-e2e-verification-design.md`
- `docs/research/2026-03-23-gstack-skill-analysis.md`

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

Local cookie import should be first-class, explicit, scoped, inspectable, and
never live-read by workers from a user's real browser profile. Imported state is
copied into an Oro auth bundle, tied to a project/app/host/environment, and
redacted from all prompts and reports.

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
- Deterministic front-end E2E remains the merge gate. Browser skills are the
  richer exploratory and reusable harness lane that can provide evidence to the
  gate, ops review, and future app harness.

## Real Problem

The immediate question is "shouldn't we make our own browser-skills, and can
they use local cookies?" The underlying problem is agent legibility for running
apps. Oro workers can edit code and run QG, but they do not have a standard,
repeatable way to open the app, reuse authenticated state, inspect console and
network failures, record screenshots/traces, and convert a successful manual
browser path into a reusable skill.

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
- There is no app/worktree-aware browser lifecycle in the dispatcher.
- There is no way to distinguish a task-specific journey from a reusable
  browser skill that can be matched by trigger and maintained over time.

## Goals

- Define an Oro-owned browser-skill schema and runner.
- Use `agent-browser` as the first backend through a small adapter.
- Support explicit, scoped local cookie/auth import into Oro-managed bundles.
- Keep auth artifacts out of git, prompts, and reports.
- Produce structured reports with screenshots, traces, console, network, and
  assertion results.
- Integrate browser evidence into worker assignments and ops review.
- Preserve deterministic QG as the hard merge gate while making browser skills
  reusable evidence and QA tools.
- Leave room for a native persistent daemon after the runner/report contract is
  proven.

## Non-Goals

- Do not port gstack wholesale.
- Do not make production browser automation available to workers by default.
- Do not read or mutate a user's live browser profile during normal worker runs.
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
  auth.go            # bundle metadata, host/environment validation
  report.go          # artifact manifest and redaction
  match.go           # trigger/app/host matching

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
oro browser stop --worktree <path>
```

Browser skill commands:

```text
oro browser-skill list
oro browser-skill match "checkout works" --app web
oro browser-skill run checkout-smoke --worktree <path> --app web
oro browser-skill record checkout-smoke --from-session <id>
oro browser-skill test checkout-smoke
oro browser-skill report <run-id>
```

Auth commands:

```text
oro browser-auth import chrome --profile Default --app web --host localhost:3000 --environment local
oro browser-auth import agent-browser-state ./auth.json --app web --host localhost:3000 --environment local
oro browser-auth list
oro browser-auth inspect web-localhost
oro browser-auth revoke web-localhost
```

The initial `chrome` importer can be platform-gated. If macOS Keychain cookie
decryption is not ready in v1, the command should say so and point users to the
`agent-browser-state` path. The important contract is the Oro bundle format and
policy, not completing every browser importer at once.

## Browser Skill Format

Committed skills live under `.oro/browser-skills/<name>/`:

```text
.oro/browser-skills/checkout-smoke/
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
  trace: on_failure
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
$ORO_HOME/projects/<slug>/browser-auth/<bundle-id>/
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
- Bundle contents are never committed and never copied into `.oro/browser-skills`.
- Reports may say which bundle ID was used, but not cookie names, values, local
  storage keys, or local storage values.
- Production bundles are disabled by default and require a human command with an
  explicit `--allow-production` flag.
- Worker use requires both the bundle and the app profile to opt in.
- Host allowlists are enforced before loading auth state into a backend session.
- Imported auth state expires by policy; v1 can warn after 30 days before adding
  hard expiry.

### Import Paths

Support these import paths in order:

1. `agent-browser state save` JSON imported via
   `oro browser-auth import agent-browser-state`.
2. Playwright-compatible `storageState` JSON imported via
   `oro browser-auth import storage-state`.
3. Chrome profile import on macOS with Keychain-backed cookie decryption when
   available.
4. Safari/Firefox import later.

This answers the local-cookie requirement while limiting blast radius. The user
can authenticate locally once, export/copy that state into Oro, inspect the
bundle metadata, and allow specific workers or skills to use it.

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
    "run_command": "oro browser-skill run <name> --worktree ..."
  }
}
```

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

Cleanup:

- Dispatcher cleanup stops browser sessions it started.
- Worktree removal also removes transient session state.
- Auth bundles remain project-scoped and survive sessions until revoked.

## Reports And Artifacts

Reports live outside committed source by default:

```text
$ORO_HOME/projects/<slug>/browser-runs/<bead-id>/<run-id>/
  report.json
  report.md
  screenshots/
  traces/
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
  "redactions": ["cookies", "local_storage", "authorization_headers"]
}
```

The markdown report is for humans. The JSON report is the contract for workers,
ops review, dashboards, and future canaries.

## Skillify Flow

Oro should eventually have a `browser-skillify` workflow like gstack, but it
must be provenance-guarded:

1. Start from a completed browser session or journey with a successful report.
2. Extract only the final successful command slice and user intent.
3. Synthesize a temporary skill under an uncommitted temp directory.
4. Run `oro browser-skill test <temp-skill>`.
5. Only then move it into `.oro/browser-skills/<name>/`.

Do not synthesize browser skills from vague chat history. This avoids permanent
skills that never actually worked.

## Native Daemon Phase

After the schema, reports, and auth layer are proven, add an Oro daemon backend:

- One daemon per project/worktree/app profile.
- State file under `$ORO_HOME/projects/<slug>/browser-daemon/<worktree-hash>/`.
- Random port and bearer token, chmod 0600.
- Idle shutdown.
- Crash detection and restart on next command.
- Plain CLI/stdout remains the user-facing interface.
- Browser context isolated per worktree.
- Daemon stores logs under the same browser run artifact tree.

This should implement the same `BrowserBackend` interface and require no skill
format changes.

## Task Graph

### BRS-1: Define schemas and load/validate browser skills

Acceptance criteria:

- `pkg/browserharness` loads `skill.yaml`, `flow.yaml`, and `assertions.yaml`.
- Invalid schema version, missing trigger, missing host allowlist with auth, and
  invalid mutation policy fail with actionable errors.
- Unit tests cover valid and invalid fixtures.

### BRS-2: Add auth bundle model and import from storage state

Acceptance criteria:

- `oro browser-auth import agent-browser-state` creates a bundle under
  `$ORO_HOME/projects/<slug>/browser-auth`.
- Bundle permissions are 0700/0600 on Unix.
- Host and environment checks are enforced before use.
- Reports can reference bundle ID but cannot include cookie/storage contents.

### BRS-3: Implement fake backend and runner assertion engine

Acceptance criteria:

- Runner executes a v1 skill against a fake backend.
- Assertions cover text, console errors, network errors, screenshots, and timing
  budgets.
- Failure reports include failed step/assertion and artifact manifest.

### BRS-4: Implement `agent-browser` backend adapter

Acceptance criteria:

- Adapter can open, snapshot, click, fill, wait, screenshot, collect console,
  and stop using `agent-browser`.
- Commands have timeouts and redacted debug logs.
- Tests use a fake command runner; one optional integration test is skipped when
  `agent-browser` is unavailable.

### BRS-5: Add CLI commands

Acceptance criteria:

- `oro browser-skill list/run/test/report` works against fixtures.
- `oro browser-auth list/inspect/revoke` works.
- `oro browser snapshot/click/fill/screenshot` delegates through the backend.
- CLI errors are concise and include remediation.

### BRS-6: Wire reports into worker assignment and ops review

Acceptance criteria:

- Assignment payload exposes app URL, available browser skills, and auth bundle
  IDs when configured.
- Worker prompt includes browser report expectations without leaking secrets.
- Ops review prompt can consume `report.json`.
- UI-impacting tasks with known relevant browser skills require a report or a
  waiver in review guidance.

### BRS-7: Add skillify prototype

Acceptance criteria:

- `oro browser-skill record --from-session` can create a temporary skill from a
  successful session transcript.
- The temp skill must pass `oro browser-skill test` before moving into
  `.oro/browser-skills`.
- Failed synthesis leaves no partial committed skill.

### BRS-8: Add native daemon backend

Acceptance criteria:

- Daemon implements `BrowserBackend`.
- Per-project/worktree state is isolated.
- Idle shutdown, dead-daemon detection, and cleanup are tested.
- Existing browser skills run unchanged on the daemon backend.

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

## Open Decisions

- Whether `.oro/browser-skills` should be the only committed location, or
  whether `docs/browser-skills` should also be supported for repos that prefer
  docs-owned artifacts.
- Whether UI-impacting task detection comes from acceptance criteria tags,
  touched files, explicit bead labels, or a combination.
- Whether production auth should be entirely forbidden for workers or allowed
  behind an explicit per-run human approval.
- Whether the native daemon should be written in Go with CDP, or in Node/Bun
  with Playwright behind the Go CLI.
- How browser-skill reports should appear in `oro dash`.

## Narrowest Wedge

Ship BRS-1 through BRS-4 first:

1. v1 browser-skill schema.
2. auth bundle model with storage-state import.
3. runner and report contract.
4. `agent-browser` backend adapter.

That gives Oro its own browser-skills without overbuilding the daemon. It also
answers the local-cookie requirement safely because the first usable auth path
is explicit copied state, not live browser-profile access.
