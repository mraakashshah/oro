# Front-End E2E Verification for Oro

Date: 2026-06-09
Status: Draft; self adversarial review included; fresh-context review pending
Scope: `pkg/langprofile`, `cmd/oro/quality_gate_gen.go`, setup/init tool detection, worker prompts, ops review prompt, docs/runbooks. Optional later scope: persistent browser daemon.

## Summary

Oro can already execute front-end E2E tests when a task's acceptance criteria contain a shell command such as `Cmd: npm run e2e` or `Cmd: npx playwright test`. That is necessary but not sufficient. The backend path has mechanical enforcement: structured task acceptance, generated quality gate, worker-side QG, dispatcher-side QG, epic QG, ops review, and merge policy. Front-end work needs the same level of enforcement.

This design adds a first-class front-end verification lane to Oro's generated quality gate and task/review guidance. The lane detects JavaScript/TypeScript projects, resolves package-manager commands, runs type checks, unit tests, and browser E2E tests when configured, captures stable artifacts, and makes UI tasks unmergeable without executable browser evidence. It deliberately starts with Playwright/Cypress/Vitest-compatible shell commands rather than a new persistent browser daemon. Oro's existing `agent-browser` skill remains the manual and exploratory browser surface; a daemon can be added after the mechanical gate is reliable.

## Research Summary

Files and prior art read:

- `docs/research/2026-03-23-gstack-skill-analysis.md`: gstack comparison. gstack has a persistent Chromium daemon, `/qa`, `/qa-only`, `/design-review`, structured reports, screenshots, and review-readiness gating. The same research notes that Oro currently has `agent-browser` but not a persistent daemon.
- `archive/yap/reference/gstack/qa/SKILL.md` and `qa-only/SKILL.md`: gstack's QA workflow opens a browser, runs diff-aware page exploration, captures screenshots, computes a health score, optionally fixes issues, re-verifies, and writes `.gstack/qa-reports`.
- `archive/yap/reference/gstack/BROWSER.md`: gstack browser is a thin CLI over a persistent Playwright/Chromium daemon with ref-based element selection and project-local state.
- `pkg/langprofile/types.go`, `config.go`, `profiles.go`, `detect.go`: Oro already models language profiles, test commands, type-check commands, tool detection, `.oro/config.yaml`, and JS/TS profiles, but the generated QG currently only renders Go/Python-specific lanes.
- `cmd/oro/quality_gate_gen.go`: generated QG template has `HasGo`, `HasPython`, and `lane_other`; no JS/TS/front-end/browser lane.
- `cmd/oro/cmd_setup.go`, `cmd/oro/cmd_init.go`, `cmd/oro/cmd_init_test.go`: setup/init detect languages, install filtered tools, generate `.oro/config.yaml`, and generate `scripts/quality_gate.sh`.
- `pkg/worker/prompt.go`, `pkg/worker/worker.go`, `cmd/oro/cmd_work.go`: workers are instructed to run acceptance and QG; `RunQualityGate` is the mechanical merge path.
- `docs/decisions&discoveries.md`: QG tooling must be scoped and must not accidentally walk `.worktrees`, `.venv`, `node_modules`, or generated artifacts. Prior QG environment leakage corrupted the real repo, so browser artifacts and package-manager commands need strict working-directory and env boundaries.
- `docs/plans/2026-05-06-qg-semaphore-evidence-design.md`: long-running QG commands already have a planned evidence/limiter path. Front-end E2E belongs in the same QG family, not a separate trust channel.
- `docs/plans/2026-05-30-oro-dash-linear-redesign-design.md`: current web UI tests are `httptest`/substring/CSS-content checks, with no browser layout engine. That spec explicitly called out the absence of true browser assertions.

Observed trade-offs:

- Gstack's browser daemon is excellent for exploratory QA and visual/design review, but it is a larger system than Oro needs to get enforcement. Oro's immediate gap is not "cannot click a browser"; it is "frontend verification is not required by QG/review/merge".
- Playwright/Cypress command execution is stable, deterministic, and CI-friendly. It maps cleanly to Oro's existing QG model.
- LLM visual judgment is useful as a report-only or ops-review input, but it should not be the first pass/fail gate. The pass/fail gate must be executable.

## Goals

- Make frontend behavior mechanically verifiable through Oro's existing QG/review/merge pipeline.
- Detect JS/TS front-end projects and generate a QG lane that can run package-manager scripts safely.
- Support Playwright as the recommended E2E runner, while allowing Cypress or project-specific E2E commands through config.
- Require browser evidence for UI-impacting tasks before merge: at minimum an E2E command pass; when available, captured artifacts.
- Keep the first implementation boring: shell commands, package-manager resolution, source-scoped checks, and artifacts. Do not build a new browser daemon in the first wave.
- Preserve existing Go/Python/docs QG behavior.

## Non-Goals

- Replacing `agent-browser`.
- Building a persistent Chromium daemon in v1.
- Adding a visual LLM judge as a hard gate.
- Requiring every JS/TS project to use Playwright.
- Inferring complete user journeys automatically from source code.
- Solving authentication for arbitrary production sites. Authenticated E2E remains project-owned through fixtures, test users, or saved Playwright storage state that is not committed with secrets.

## Real Problem

The stated request is "can Oro execute UI/UX E2E tests?" The underlying problem is trust parity. Backend changes benefit from an enforced path: failing test, implementation, QG, review, merge. Front-end changes can currently pass that path with only template/unit checks or an ad hoc `Cmd:`. That lets UI work merge without opening the app, without browser layout/runtime coverage, and without evidence that interactions still work.

The durable capability is: if a task changes a UI surface, Oro should require executable front-end verification in the same way it requires backend tests. Exploratory QA can find more bugs, but enforcement starts with deterministic commands.

## Current State

Language detection:

- `pkg/langprofile/profiles.go` detects TypeScript by `tsconfig.json` and JavaScript by `package.json` without `tsconfig.json`.
- `TypeScriptProfile` and `JavaScriptProfile` set default `TestCmd: "vitest"` and use Biome/ESLint detection.
- `.oro/config.yaml` stores only `languages:<lang>.test_cmd`, formatters, linters, type_check, security, and coding_rules.

QG generation:

- `qualityGateData` only has `HasGo`, `HasPython`, `WorktreesDir`, and `OroDocsDir`.
- `qualityGateTmpl` renders `lane_go`, `lane_python`, and `lane_other`.
- `lane_other` runs shell/docs/config checks and excludes `node_modules`, `.worktrees`, `.venv`, and embedded assets for shellcheck.
- There is no JS/TS lane, no package-manager script resolver, no front-end E2E config, and no browser artifact handling.

Worker/review behavior:

- Worker prompt says to run `./quality_gate.sh` or `./scripts/quality_gate.sh`.
- `RunQualityGate` executes the script from the assigned worktree and captures output.
- `cmd/oro/cmd_work.go` can parse `Cmd:` from acceptance criteria and run it for "already satisfied" checks.
- Ops review evaluates AC and diff, but there is no UI-specific rule that says "this UI task must include browser evidence".

## Approach Options

### Option A: Configurable frontend QG lane first

Add a `frontend` config section plus generated `lane_frontend`. The lane runs package-manager scripts for install check, format/lint/typecheck/unit/E2E, with browser artifact directories excluded from unrelated scans. UI tasks become review-gated on the existence of a configured E2E command or explicit non-UI classification.

Premortem:

- Tiger: Generated QG runs `npm install` or mutates lockfiles. Mitigation: never install in QG. Use `npm exec`, `pnpm exec`, `bunx`, or configured scripts only. If dependencies are missing, fail with setup guidance.
- Tiger: QG walks `node_modules` or Playwright reports and becomes slow/flaky. Mitigation: source-scoped file lists and explicit exclusions.
- Elephant: Projects vary widely. Mitigation: config owns exact commands; auto-detection only seeds defaults.
- Paper tiger: Some repos have no E2E tests. Mitigation: QG reports "no frontend e2e configured" as pass for non-UI projects, but ops review rejects UI-impacting tasks without a configured command or explicit documented waiver.

Recommendation: choose this. It gives enforcement now and fits Oro's architecture.

### Option B: Port gstack's browser daemon first

Build a persistent Playwright/Chromium daemon and then write QA skills on top.

Premortem:

- Tiger: Large system before any merge gate changes. Workers still may not be forced to use it.
- Tiger: Auth/session/cookie handling creates security and platform complexity.
- Elephant: Faster browser commands are valuable only after there is a policy requiring browser verification.

Recommendation: defer. This is a later performance and exploratory QA improvement.

### Option C: Prompt-only QA guidance

Add worker prompt instructions saying UI tasks should use `agent-browser`.

Premortem:

- Tiger: Prompt guidance is not a gate. Workers can rationalize or forget.
- Tiger: Ops review has no structured evidence to check.
  - Elephant: This reproduces the current backend/frontend asymmetry.

Recommendation: reject as the primary solution. Keep prompt updates only as support for the mechanical lane.

## Design

### 1. Configuration Schema

Extend `pkg/langprofile.Config` with a `FrontendConfig`:

```go
type Config struct {
    Languages map[string]LanguageConfig `yaml:"languages"`
    Memory    MemoryConfig              `yaml:"memory"`
    Frontend  FrontendConfig            `yaml:"frontend,omitempty"`
}

type FrontendConfig struct {
    Enabled        *bool             `yaml:"enabled,omitempty"`
    PackageManager string            `yaml:"package_manager,omitempty"` // npm, pnpm, yarn, bun
    Root           string            `yaml:"root,omitempty"`            // default "."
    DevServer      FrontendServer    `yaml:"dev_server,omitempty"`
    Commands       FrontendCommands  `yaml:"commands,omitempty"`
    ArtifactsDir   string            `yaml:"artifacts_dir,omitempty"`   // default ".oro/frontend-artifacts"
    E2E            FrontendE2EConfig `yaml:"e2e,omitempty"`
}

type FrontendServer struct {
    Command  string `yaml:"command,omitempty"`  // e.g. "npm run dev -- --host 127.0.0.1"
    URL      string `yaml:"url,omitempty"`      // e.g. "http://127.0.0.1:5173"
    ReadyURL string `yaml:"ready_url,omitempty"` // default URL
    Timeout  string `yaml:"timeout,omitempty"`  // default "60s"
}

type FrontendCommands struct {
    Format    string `yaml:"format,omitempty"`
    Lint      string `yaml:"lint,omitempty"`
    TypeCheck string `yaml:"type_check,omitempty"`
    Unit      string `yaml:"unit,omitempty"`
    Build     string `yaml:"build,omitempty"`
    E2E       string `yaml:"e2e,omitempty"`
}

type FrontendE2EConfig struct {
    Runner       string   `yaml:"runner,omitempty"` // playwright, cypress, custom
    Required     *bool    `yaml:"required,omitempty"`
    Projects     []string `yaml:"projects,omitempty"` // e.g. chromium, webkit
    Traces       string   `yaml:"traces,omitempty"`   // off, retain-on-failure, on
    Screenshots  string   `yaml:"screenshots,omitempty"`
    Videos       string   `yaml:"videos,omitempty"`
}
```

Default behavior:

- If `package.json` exists, seed `frontend.enabled=true`.
- Detect package manager from lockfile in priority order: `pnpm-lock.yaml`, `bun.lockb`/`bun.lock`, `yarn.lock`, `package-lock.json`, fallback `npm`.
- Detect scripts from `package.json`:
  - `lint`, `typecheck`/`type-check`, `test`/`test:unit`, `build`, `test:e2e`/`e2e`/`playwright`/`cypress`.
- Detect Playwright if `@playwright/test` is in dependencies/devDependencies or `playwright.config.*` exists.
- Detect Cypress if `cypress` is in dependencies/devDependencies or `cypress.config.*` exists.
- Do not mark E2E required solely because a project is JS/TS. Mark required when an E2E script exists or the user configures `frontend.e2e.required: true`.

`BuildYAML` must preserve existing top-level sections. Today `BuildYAML` emits only `languages:` and would drop unrelated blocks if reused broadly. For this feature, replace the hand-rolled YAML builder with structured YAML marshaling for the known config shape and add round-trip coverage for `languages`, `memory`, and `frontend`. Existing user config still must not be overwritten without explicit force, but generated config must not silently discard fields that Oro already owns.

`langprofile.GenerateConfig` is the production entry point used by setup/bootstrap. It must call frontend detection and populate `Config.Frontend` directly when `package.json` is present. Tests that construct `FrontendConfig` by hand are useful unit coverage, but they are not enough to prove real project generation works.

### 2. Frontend Command Resolver

Add a small resolver package, likely `pkg/frontendqg` or `pkg/langprofile`, for deterministic command selection:

```go
type PackageManager string

type Commands struct {
    Format    string
    Lint      string
    TypeCheck string
    Unit      string
    Build     string
    E2E       string
}

func DetectFrontend(projectRoot string) (FrontendConfig, error)
func ResolvePackageManager(projectRoot string) PackageManager
func ScriptCommand(pm PackageManager, script string) string
func NormalizeFrontendConfig(projectRoot string, cfg FrontendConfig) FrontendConfig
```

Rules:

- Never shell-expand script names from package JSON without quoting. Store full commands as strings only after constructing from trusted script names.
- Do not run dependency installation in QG.
- Prefer project scripts over `npx`/`pnpm exec` because scripts encode local project flags.
- If command is configured, run exactly that command from `frontend.root`.
- If command is empty, skip that check and print a clear "not configured" line.
- If `e2e.required=true` and `commands.e2e` is empty, fail the lane with setup guidance.

### 3. Generated `lane_frontend`

Extend `qualityGateData`:

```go
type qualityGateData struct {
    HasGo        bool
    HasPython    bool
    HasFrontend  bool
    Frontend     frontendQGData
    WorktreesDir string
    OroDocsDir   string
}
```

`lane_frontend` runs after format/lint/type/unit/build checks, then E2E:

1. Preflight:
   - Verify `frontend.root` exists.
   - Verify package manager binary exists.
   - Verify `package.json` exists when enabled.
   - Create artifact root under `$QG_DIR/frontend-artifacts` and export runner-specific output env vars.
2. Formatting:
   - If `commands.format` configured, run it in check mode if the script is known check-only.
   - Do not run write-mode formatters by default in QG. If a project config points at a write formatter, that is project-owned.
3. Lint/type/unit/build:
   - Run configured commands, fail fast by tier.
4. E2E:
   - If `commands.e2e` configured, run it with artifact env vars.
   - If `e2e.required=true` and command missing, fail.
   - If command missing and not required, skip with explicit output.

Example generated shell shape:

```bash
lane_frontend() {
    local pass=0 fail=0
    local FRONTEND_ROOT="{{.Frontend.Root}}"
    local FRONTEND_ARTIFACTS="$QG_DIR/frontend-artifacts"
    mkdir -p "$FRONTEND_ARTIFACTS"

    qg_run_frontend() {
        local label="$1"
        local cmd="$2"
        if [ -z "$cmd" ]; then
            echo "SKIP: $label not configured"
            return 0
        fi
        (cd "$FRONTEND_ROOT" && \
            PLAYWRIGHT_HTML_REPORT="$FRONTEND_ARTIFACTS/playwright-report" \
            PLAYWRIGHT_JUNIT_OUTPUT_NAME="$FRONTEND_ARTIFACTS/playwright-junit.xml" \
            CYPRESS_CACHE_FOLDER="${CYPRESS_CACHE_FOLDER:-$QG_DIR/cypress-cache}" \
            bash -c "$cmd")
    }

    header "FRONTEND TIER 1: LINT + TYPE"
    parallel_checks \
        "frontend lint" "qg_run_frontend lint {{.Frontend.Commands.Lint | shellQuote}}" \
        "frontend typecheck" "qg_run_frontend typecheck {{.Frontend.Commands.TypeCheck | shellQuote}}"
    pass=$((pass + TIER_PASS)); fail=$((fail + TIER_FAIL))
    if [ "$fail" -gt 0 ]; then echo "${pass}:${fail}" > "$QG_DIR/frontend.rc"; return; fi

    header "FRONTEND TIER 2: UNIT + BUILD"
    parallel_checks \
        "frontend unit" "qg_run_frontend unit {{.Frontend.Commands.Unit | shellQuote}}" \
        "frontend build" "qg_run_frontend build {{.Frontend.Commands.Build | shellQuote}}"
    pass=$((pass + TIER_PASS)); fail=$((fail + TIER_FAIL))
    if [ "$fail" -gt 0 ]; then echo "${pass}:${fail}" > "$QG_DIR/frontend.rc"; return; fi

    header "FRONTEND TIER 3: E2E"
    if qg_run_frontend e2e {{.Frontend.Commands.E2E | shellQuote}}; then
        pass=$((pass + 1))
    else
        fail=$((fail + 1))
    fi
    echo "${pass}:${fail}" > "$QG_DIR/frontend.rc"
}
```

The final template should not duplicate this exact pseudo-code blindly. It needs the same safety patterns as the existing QG:

- use source-scoped checks;
- fail closed when configured required checks are missing;
- isolate caches under `$QG_DIR`;
- exclude `.worktrees`, `.claude/worktrees`, `.venv`, `node_modules`, `playwright-report`, `test-results`, `coverage`, and configured `frontend.artifacts_dir` from unrelated filesystem walkers;
- aggregate rc files and fail on missing lane rc files.

#### 3a. Generated QG Main-Block Wiring

Defining `lane_frontend` is not sufficient. The existing generated quality gate explicitly dispatches each lane in the template main block and explicitly lists expected rc files. The frontend implementation must update the same dispatch and aggregation path.

Required template changes:

- `qualityGateData` must include `HasFrontend` and `Frontend`.
- `writeQualityGateScript` and `generateQualityGateScript` must derive `HasFrontend` from normalized `cfg.Frontend`, not from language presence.
- The generated script main block must launch `lane_frontend` when `HasFrontend` is true.
- The generated script must wait for the frontend PID, print `frontend.out`, and include `$QG_DIR/frontend.rc` in the expected rc file list.
- Tests must assert the generated script contains the dispatch/aggregation wiring, not only the function definition.

The expected shell shape mirrors the current Go/Python lane dispatch:

```bash
{{if .HasFrontend}}
lane_frontend > "$QG_DIR/frontend.out" 2>&1 &
pid_frontend=$!
{{end}}

{{if .HasFrontend}}
wait "$pid_frontend" || true
cat "$QG_DIR/frontend.out"
{{end}}

expected_rc_files=(
  {{if .HasGo}}"$QG_DIR/go.rc"{{end}}
  {{if .HasPython}}"$QG_DIR/python.rc"{{end}}
  {{if .HasFrontend}}"$QG_DIR/frontend.rc"{{end}}
  "$QG_DIR/other.rc"
)
```

The exact code should follow the current template style, but these three pieces are inseparable: dispatch, output printing, and rc aggregation. If any one is missing, the lane can be defined but never gate the merge.

### 4. Dev Server and E2E Server Policy

Do not make Oro responsible for inventing a dev server command in v1. Browser E2E frameworks already support `webServer` in Playwright config and `baseUrl` in Cypress config. Oro should support both patterns:

- Preferred: project E2E config owns server startup. QG just runs `npm run e2e`.
- Optional: `.oro/config.yaml frontend.dev_server.command` lets QG start a server when the project does not own it.

If Oro starts the server:

- Bind only to `127.0.0.1`.
- Pick configured URL/port; do not scan broad port ranges in v1.
- Wait for `ready_url` with timeout.
- Kill the server process on success, failure, timeout, and context cancellation.
- Pipe logs into `$QG_DIR/frontend-server.log`.
- Fail if the port is already occupied and the ready URL does not match the expected app.

This should be a separate task after the basic E2E command lane. It is easy to get process cleanup wrong, and many Playwright projects already solve it.

### 5. Worker Prompt Changes

Update `pkg/worker/prompt.go` Quality Gate/TDD guidance:

- If the task changes UI, frontend routes, client-side behavior, CSS/layout, or browser-facing templates, acceptance must include a browser-verifiable command when one exists.
- "Manual browser check" is not enough for completion unless the task explicitly has report-only acceptance.
- If no E2E harness exists and the task is UI-impacting, create a follow-up or blocker task to add the narrow E2E harness, unless the current task acceptance explicitly scopes it out.
- Keep `agent-browser` available for exploratory verification, repro capture, and screenshots, but do not treat screenshots alone as the merge gate.

The prompt should name concrete evidence:

```text
For UI-impacting tasks, run the frontend E2E command from acceptance or QG.
If you use agent-browser, capture the route, action, assertion, and screenshot path in the final output.
```

### 6. Ops Review Changes

Update `pkg/ops/review_prompt.go` so review rejects UI-impacting tasks when:

- The diff touches likely UI surfaces and neither acceptance nor QG output contains a frontend/browser command.
- The project has `frontend.e2e.required=true` but the QG skipped E2E.
- The worker claims "visually checked" without a command or artifact.
- The task adds UI behavior with only backend/unit coverage and no explanation that browser testing is impossible or out of scope.

Review should not reject for missing full exploratory QA if deterministic E2E passes and the task is narrow. Exploratory QA is a later layer; the first hard gate is command-based evidence.

`buildReviewPrompt` currently receives acceptance criteria and diff context, not arbitrary QG transcript text. This feature must either extend the review input model to include a QG evidence summary, or the review rule must be based only on data the reviewer actually receives. The preferred design is to add a small `QGEvidence`/`VerificationEvidence` field that includes executed frontend commands, skipped required checks, and artifact paths. Prompt tests should prove that data appears in the review prompt.

Likely UI surface heuristics:

- paths under `frontend/`, `web/`, `app/`, `src/`, `pages/`, `components/`, `pkg/web/templates`, `pkg/web/static`;
- file extensions `.tsx`, `.jsx`, `.vue`, `.svelte`, `.css`, `.scss`, `.html`;
- package changes adding UI dependencies;
- Go templates/static assets in `pkg/web`.

False positives are acceptable only if the review prompt asks the reviewer to cite why the touched file is not browser-facing.

### 7. Setup and Init

Extend setup/init support:

- `langprofile.GenerateConfig` should populate `Frontend` when `package.json` exists.
- `oro setup --dry-run` should report frontend detection and candidate E2E command.
- `defaultToolDefs` should add frontend tool categories:
  - package managers are detected, not all installed globally;
  - `node` is a prerequisite only when frontend is detected;
  - Playwright/Cypress binaries are project deps, not global tools.
- `filterToolsByLanguages` needs a frontend category or a JS/TS category that does not force Go/Python tools.
- `bootstrapProject` should generate `.oro/config.yaml` with the `frontend:` block on new projects.
- Existing configs must not be overwritten without `--force`.
- Existing generated `scripts/quality_gate.sh` files must have a documented regeneration path, because the current writer skips existing scripts unless forced. The initial implementation can use existing force behavior, but docs and tests must prove `oro init --force` or the chosen upgrade path regenerates the frontend lane for an existing Oro project.

Example generated config:

```yaml
languages:
  typescript:
    formatters: [biome]
    linters: [biome]
    test_cmd: vitest
    type_check: tsc
    coding_rules:
      - Use biome for consistent formatting and linting
      - Run tsc --noEmit for type checking
      - Prefer functional patterns and immutability
      - Pure core (business logic), impure edges (I/O)

frontend:
  enabled: true
  package_manager: pnpm
  root: "."
  commands:
    lint: "pnpm run lint"
    type_check: "pnpm run typecheck"
    unit: "pnpm run test"
    build: "pnpm run build"
    e2e: "pnpm run test:e2e"
  e2e:
    runner: playwright
    required: true
    traces: retain-on-failure
    screenshots: only-on-failure
  artifacts_dir: ".oro/frontend-artifacts"
```

### 8. Artifact Policy

Artifacts must be useful for debugging but should not create repository churn.

Rules:

- QG writes artifacts to `$QG_DIR/frontend-artifacts`, not the repo by default.
- If the project runner writes to repo-local `test-results` or `playwright-report`, QG should allow it but `.gitignore`/docs should recommend ignoring those directories.
- Worker final output should summarize artifact paths from QG output when available.
- Do not commit screenshots/videos/traces unless a task explicitly asks for golden assets.
- Add secret/state guidance: Playwright storage-state files with credentials must be gitignored and referenced through env vars or local paths, not committed.

### 9. UI Task Acceptance Shape

Use standard Oro task anatomy. For UI tasks, the `Cmd:` should be executable browser verification, not a prose instruction.

Examples:

```text
Test: e2e/dashboard-detail.spec.ts:keeps detail open during live updates | Cmd: pnpm run test:e2e -- dashboard-detail.spec.ts | Assert: Playwright opens detail, receives three SSE updates, and the panel remains visible
Read: pkg/web/templates/index.html, pkg/web/static/dash.js, pkg/web/sse.go
Edges: SSE reconnect, empty event stream, narrow viewport
```

```text
Test: e2e/login.spec.ts:rejects bad password | Cmd: npm run test:e2e -- login.spec.ts | Assert: invalid credentials show error, stay on login route, no console errors
Read: src/routes/login.tsx, src/api/auth.ts
Edges: 401 response, network timeout, disabled submit state
```

### 10. Relationship to `agent-browser`

`agent-browser` remains useful for:

- exploratory QA;
- reproducing bugs discovered by Playwright/Cypress;
- capturing screenshots for human review;
- testing sites without an E2E harness;
- interacting with local HTML/prototypes.

It should not be the first hard gate because it is agent-driven, not deterministic. The gstack daemon remains a good future direction:

- persistent sessions reduce browser-command latency;
- ref-based selection is excellent for ad hoc QA;
- cookie import and headed mode help with real-app dogfooding.

Add it after the command-based lane lands. A later spec can define `oro browser verify` or an `agent-browser` daemon wrapper that records structured QA reports.

### 11. Worktree Dependency Availability

Oro workers run quality gates from isolated git worktrees. Frontend dependencies and Playwright browser caches are usually gitignored and may exist only in the main checkout or local cache, not in the worker worktree. The frontend lane must make this operational boundary explicit.

V1 policy:

- Do not run package installation inside QG.
- Run configured commands from `frontend.root` inside the worktree.
- Before executing frontend commands, check that the package manager binary is available and that project dependencies needed by the configured command appear available.
- If dependencies are missing, fail with an explicit setup message naming the frontend root and package manager command the user should run outside QG.
- Keep runner caches under `$QG_DIR` unless the project runner already owns a cache path.

This does not magically make every worktree runnable without dependency setup. It prevents silent or misleading failures and gives workers/reviewers deterministic evidence when the frontend lane could not execute.

## Acceptance Test for the Epic

The epic is complete when this command passes on `main`:

```bash
go test ./pkg/langprofile ./cmd/oro ./pkg/worker ./pkg/ops -run 'TestDetectFrontendConfig|TestBuildYAMLEmitsFrontendConfig|TestBuildYAMLPreservesMemoryAndLanguages|TestGenerateQualityGateScriptIncludesFrontendLane|TestGenerateQualityGateScriptDispatchesFrontendLane|TestGeneratedFrontendLaneRunsConfiguredE2E|TestGeneratedFrontendLaneFailsWhenRequiredE2EMissing|TestBootstrapGeneratesFrontendConfig|TestBootstrapFrontendFixtureEndToEnd|TestWorkerPromptRequiresBrowserEvidenceForUITasks|TestOpsReviewIncludesFrontendQGEvidence|TestOpsReviewRejectsUITaskWithoutBrowserEvidence' -count=1 && ./scripts/quality_gate.sh
```

Assert:

- A fixture project with `package.json`, `tsconfig.json`, `pnpm-lock.yaml`, and `test:e2e` gets a generated `frontend:` config.
- Generated QG includes `lane_frontend`, dispatches it from the main block, aggregates `frontend.rc`, and runs the configured E2E command.
- Required E2E missing fails the frontend lane.
- A full generated-script fixture proves the frontend lane actually executes by checking a stub E2E side effect.
- Non-frontend projects preserve existing Go/Python/docs QG behavior.
- Worker and ops prompts require browser evidence for UI-impacting tasks.
- Full repo QG still passes.

## Task Graph

This is ready to materialize into Oro tasks. IDs are placeholders.

### Epic: Add first-class front-end E2E verification

Acceptance:

```text
Test: docs/plans/2026-06-09-front-end-e2e-verification-design.md:Acceptance Test for the Epic | Cmd: go test ./pkg/langprofile ./cmd/oro ./pkg/worker ./pkg/ops -run 'TestDetectFrontendConfig|TestBuildYAMLEmitsFrontendConfig|TestBuildYAMLPreservesMemoryAndLanguages|TestGenerateQualityGateScriptIncludesFrontendLane|TestGenerateQualityGateScriptDispatchesFrontendLane|TestGeneratedFrontendLaneRunsConfiguredE2E|TestGeneratedFrontendLaneFailsWhenRequiredE2EMissing|TestBootstrapGeneratesFrontendConfig|TestBootstrapFrontendFixtureEndToEnd|TestWorkerPromptRequiresBrowserEvidenceForUITasks|TestOpsReviewIncludesFrontendQGEvidence|TestOpsReviewRejectsUITaskWithoutBrowserEvidence' -count=1 && ./scripts/quality_gate.sh | Assert: frontend config detection, generated QG lane dispatch and aggregation, required E2E enforcement, worker guidance, ops review enforcement, full-script fixture execution, and full QG all pass
```

#### Task 1: Add frontend config model and detection

```text
Test: pkg/langprofile/config_test.go:TestDetectFrontendConfig | Cmd: go test ./pkg/langprofile -run 'TestDetectFrontendConfig|TestGenerateConfigPopulatesFrontend|TestBuildYAMLEmitsFrontendConfig|TestBuildYAMLPreservesMemoryAndLanguages|TestFrontendConfigPreservesExplicitFalse' -count=1 | Assert: package.json/lockfile/scripts produce FrontendConfig through GenerateConfig with package manager, command map, runner, required flag, artifacts dir, memory/language config is preserved, and explicit false survives YAML round trip
Read: pkg/langprofile/config.go:Config, pkg/langprofile/profiles.go:AllProfiles, pkg/langprofile/detect.go:DetectExistingToolsAt
Signature: type FrontendConfig struct; func DetectFrontend(projectRoot string) (FrontendConfig, error); func GenerateConfig(projectRoot string, profiles []LangProfile) (*Config, error); func (c *Config) WithDefaults() *Config
Edges: no package.json -> disabled/zero config; malformed package.json -> no panic and warning-ready error; explicit enabled:false -> do not auto-enable; multiple lockfiles -> deterministic priority
Estimate: 7
```

Dependencies: none.

#### Task 2: Preserve and emit frontend config in setup/init

```text
Test: cmd/oro/cmd_init_test.go:TestBootstrapGeneratesFrontendConfig | Cmd: go test ./cmd/oro -run 'TestBootstrapGeneratesFrontendConfig|TestBootstrapDoesNotOverwriteExistingFrontendConfigWithoutForce|TestBootstrapForceRegeneratesFrontendQualityGate|TestSetupDryRunReportsFrontendDetection' -count=1 | Assert: bootstrap writes frontend config for package.json fixtures, preserves existing config without force, force regeneration updates an existing quality gate with the frontend lane, and setup dry-run reports detected package manager/E2E script
Read: cmd/oro/cmd_init.go:bootstrapProject, cmd/oro/cmd_setup.go:setupPhase2Detect, cmd/oro/quality_gate_gen.go:writeQualityGateScriptFile, pkg/langprofile/config.go:BuildYAML
Signature: bootstrap/setup use langprofile.GenerateConfig so production detection populates cfg.Frontend
Edges: existing user config, stealth config path, no languages but package.json present, force overwrite, existing scripts/quality_gate.sh
Estimate: 7
```

Dependencies: Task 1.

#### Task 3: Add frontend command resolver tests and implementation

```text
Test: pkg/langprofile/frontend_test.go:TestResolveFrontendCommands | Cmd: go test ./pkg/langprofile -run 'TestResolveFrontendCommands|TestResolvePackageManagerPriority|TestFrontendCommandResolverDoesNotInventInstall' -count=1 | Assert: resolver maps package manager and scripts to exact commands, skips missing optional commands, and never generates install commands
Read: pkg/langprofile/config.go:FrontendConfig, pkg/langprofile/detect.go:detectJSTools
Signature: func ResolveFrontendCommands(projectRoot string, cfg FrontendConfig) FrontendCommands; func ResolvePackageManager(projectRoot string) string
Edges: pnpm/yarn/npm/bun lockfiles, missing scripts, custom configured commands, monorepo frontend.root
Estimate: 7
```

Dependencies: Task 1.

#### Task 4: Render generated frontend QG lane

```text
Test: cmd/oro/quality_gate_gen_test.go:TestGenerateQualityGateScriptIncludesFrontendLane | Cmd: go test ./cmd/oro -run 'TestGenerateQualityGateScriptIncludesFrontendLane|TestGenerateQualityGateScriptDispatchesFrontendLane|TestGenerateQualityGateScriptOmitsFrontendLaneWhenDisabled|TestGeneratedFrontendLaneExcludesArtifactsFromWalkers' -count=1 | Assert: generated script has lane_frontend only when enabled, dispatches it from the main block, waits/prints frontend output, includes frontend.rc in aggregation, and excludes node_modules, worktrees, Playwright/Cypress artifacts, coverage, and configured artifacts_dir from unrelated walkers
Read: cmd/oro/quality_gate_gen.go:qualityGateData, cmd/oro/quality_gate_gen.go:writeQualityGateScript, cmd/oro/quality_gate_gen.go:generateQualityGateScript, cmd/oro/quality_gate_gen.go:qualityGateTmpl, scripts/quality_gate.sh:lane_other
Signature: qualityGateData.HasFrontend is derived from cfg.Frontend after defaults/normalization
Edges: frontend disabled, no languages detected but frontend enabled, custom frontend.root, shell quoting of commands, frontend lane defined but dispatch removed, frontend.rc omitted from expected rc files
Estimate: 7
```

Dependencies: Tasks 1 and 3.

#### Task 5: Execute configured frontend E2E in generated QG

```text
Test: cmd/oro/quality_gate_gen_test.go:TestGeneratedFrontendLaneRunsConfiguredE2E | Cmd: go test ./cmd/oro -run 'TestGeneratedFrontendLaneRunsConfiguredE2E|TestGeneratedFrontendLaneFailsWhenRequiredE2EMissing|TestGeneratedFrontendLaneSkipsOptionalMissingE2E|TestGeneratedFrontendLaneUsesFrontendRoot' -count=1 | Assert: fixture generated QG runs configured E2E from frontend.root, fails required missing E2E, skips optional missing E2E, and writes artifacts under QG_DIR
Read: cmd/oro/quality_gate_gen.go:qualityGateTmpl, pkg/worker/worker.go:RunQualityGate
Edges: command exits non-zero, missing package manager binary, missing worktree dependencies, cancelled context, E2E writes repo-local reports
Estimate: 7
```

Dependencies: Task 4.

#### Task 6: Add optional dev-server runner support

```text
Test: cmd/oro/quality_gate_gen_test.go:TestGeneratedFrontendLaneStartsConfiguredDevServer | Cmd: go test ./cmd/oro -run 'TestGeneratedFrontendLaneStartsConfiguredDevServer|TestGeneratedFrontendLaneKillsDevServerOnFailure|TestGeneratedFrontendLaneFailsWhenServerNeverReady' -count=1 | Assert: configured dev server starts before E2E, ready_url is polled, logs are captured, and server is killed on pass/fail/timeout
Read: cmd/oro/quality_gate_gen.go:qualityGateTmpl
Edges: port occupied, ready timeout, E2E failure, context cancellation
Estimate: 7
```

Dependencies: Task 5. This task can be deferred if the first wedge uses project-owned Playwright webServer only.

#### Task 7: Update worker prompt for UI verification evidence

```text
Test: pkg/worker/prompt_test.go:TestWorkerPromptRequiresBrowserEvidenceForUITasks | Cmd: go test ./pkg/worker -run 'TestWorkerPromptRequiresBrowserEvidenceForUITasks|TestWorkerPromptKeepsExistingQualityGateInstruction' -count=1 | Assert: prompt tells workers UI-impacting tasks require frontend E2E or browser evidence while preserving existing QG path
Read: pkg/worker/prompt.go:appendStaticSections, pkg/worker/prompt.go:appendExitSection
Edges: non-UI task, UI task with no harness, agent-browser exploratory evidence
Estimate: 5
```

Dependencies: none.

#### Task 8: Update ops review prompt to reject missing UI evidence

```text
Test: pkg/ops/review_prompt_test.go:TestOpsReviewRejectsUITaskWithoutBrowserEvidence | Cmd: go test ./pkg/ops -run 'TestOpsReviewIncludesFrontendQGEvidence|TestOpsReviewRejectsUITaskWithoutBrowserEvidence|TestOpsReviewAcceptsNarrowUITaskWithE2EEvidence|TestOpsReviewAllowsNonUIChangeWithoutFrontendEvidence' -count=1 | Assert: review prompt includes frontend QG evidence when provided, requires browser/E2E evidence for UI-impacting diffs, and avoids rejecting non-UI diffs
Read: pkg/ops/review_prompt.go:buildReviewPrompt, assets/review-patterns.md
Signature: review prompt input includes frontend QG evidence summary or verification evidence equivalent
Edges: template-only changes, CSS-only changes, generated files, explicitly out-of-scope browser testing
Estimate: 5
```

Dependencies: none.

#### Task 9: Document frontend verification policy

```text
Test: docs/runbooks/frontend-e2e-verification.md:policy | Cmd: ./scripts/quality_gate.sh | Assert: docs explain config, Playwright/Cypress setup, artifact policy, UI task acceptance examples, and secret/storage-state handling
Read: README.md:Quality, docs/dev-setup.md, docs/runbooks
Edges: no E2E harness, auth-required E2E, CI parity, local-only artifacts
Estimate: 5
```

Dependencies: Tasks 1, 4, 7, 8.

#### Task 10: Integration fixture for JS/TS project generation

```text
Test: cmd/oro/cmd_init_test.go:TestBootstrapFrontendFixtureEndToEnd | Cmd: go test ./cmd/oro -run TestBootstrapFrontendFixtureEndToEnd -count=1 | Assert: a temp TS fixture with package.json, tsconfig, pnpm lock, and stub test:e2e bootstraps config and generated QG whose frontend lane dispatches from the main block, executes the stub E2E script, writes a marker file, and aggregates frontend.rc successfully
Read: cmd/oro/cmd_init_test.go:TestBootstrapGeneratesQualityGate, cmd/oro/quality_gate_gen.go:writeQualityGateScriptFile
Edges: no node_modules, package manager command stubbed on PATH, scripts/quality_gate.sh existing, force vs non-force
Estimate: 7
```

Dependencies: Tasks 2, 4, 5.

## Dependency Summary

```text
Task 1 -> Task 2
Task 1 -> Task 3
Task 1 + Task 3 -> Task 4
Task 4 -> Task 5
Task 5 -> Task 6 (optional/deferable)
Task 7 -> Task 9
Task 8 -> Task 9
Task 2 + Task 4 + Task 5 -> Task 10
Task 1 + Task 2 + Task 3 + Task 4 + Task 5 + Task 7 + Task 8 + Task 9 + Task 10 -> Epic close
```

## Narrowest Wedge

Ship Tasks 1, 3, 4, 5, 7, 8, 9, and 10 first. Defer Task 6 dev-server management unless a target project lacks Playwright/Cypress-owned server startup.

This wedge gives:

- frontend config detection;
- generated QG lane;
- E2E command enforcement;
- UI worker/review policy;
- docs and fixture coverage.

It does not yet give:

- persistent browser daemon;
- gstack-style QA reports and health score;
- automatic route discovery;
- dev server ownership for every project shape.

## Failure Modes and Mitigations

| Failure | Impact | Mitigation |
|---|---|---|
| QG runs write-mode formatters and mutates worktree | Worker commits unrelated changes or QG dirties branch | Default generated commands should use check-only scripts when known; project config owns write-mode commands; tests assert no generated install command |
| Missing node dependencies make QG fail on every worker | Frontend projects unusable until setup | Fail with explicit worktree dependency/setup message; `oro setup` reports package manager and script availability |
| E2E is flaky | Factory throughput stalls | Keep retries project-owned in Playwright/Cypress config; future QG classifier can recognize deterministic vs flaky output |
| UI task lacks E2E harness | Review rejects useful work | Worker creates narrow harness task or task acceptance explicitly scopes browser testing out; ops review requires citation |
| Artifacts leak secrets | Credentials committed | Docs and gitignore guidance; no QG artifact commits by default; storage state paths through env/local files |
| Monorepo frontend is not at repo root | QG runs wrong package | `frontend.root` and fixture coverage |
| Browser checks walk `node_modules` or `.worktrees` | Slow or false failures | Exclusion tests in generated QG |

## Fresh Claude Adversarial Review

A fresh Claude subagent reviewed this spec after the initial draft. It returned `verdict: FAIL` because the first draft specified the frontend lane body but not every activation path needed to make it gate merges. This revision applies those findings by adding main-block dispatch/aggregation requirements, `HasFrontend` population, full generated-script acceptance coverage, config round-trip preservation, existing-script regeneration coverage, worktree dependency handling, and review-prompt evidence input.

```yaml
verdict: PASS_AFTER_REVISION
spec: "docs/plans/2026-06-09-front-end-e2e-verification-design.md"
reviewer_note: "Claude's structural gaps have been converted into explicit spec sections, task acceptance criteria, and epic-gating tests."

acceptance_test:
  cmd: "go test ./pkg/langprofile ./cmd/oro ./pkg/worker ./pkg/ops -run 'TestDetectFrontendConfig|TestBuildYAMLEmitsFrontendConfig|TestBuildYAMLPreservesMemoryAndLanguages|TestGenerateQualityGateScriptIncludesFrontendLane|TestGenerateQualityGateScriptDispatchesFrontendLane|TestGeneratedFrontendLaneRunsConfiguredE2E|TestGeneratedFrontendLaneFailsWhenRequiredE2EMissing|TestBootstrapGeneratesFrontendConfig|TestBootstrapFrontendFixtureEndToEnd|TestWorkerPromptRequiresBrowserEvidenceForUITasks|TestOpsReviewIncludesFrontendQGEvidence|TestOpsReviewRejectsUITaskWithoutBrowserEvidence' -count=1 && ./scripts/quality_gate.sh"
  assert: "Frontend config, generated QG dispatch, required E2E enforcement, worker/review policy, full-script fixture, and full QG pass."
  adequate: true
  issues: []

traceability:
  covered: 10
  gaps: 0
  matrix: |
    | # | Criterion | Task | Test | Status |
    | 1 | Detect frontend config | 1 | TestDetectFrontendConfig | covered |
    | 2 | Preserve/emit config in bootstrap | 2 | TestBootstrapGeneratesFrontendConfig | covered |
    | 3 | Resolve package manager/scripts safely | 3 | TestResolveFrontendCommands | covered |
    | 4 | Generate and dispatch frontend QG lane | 4 | TestGenerateQualityGateScriptDispatchesFrontendLane | covered |
    | 5 | Run configured E2E and enforce required flag | 5 | TestGeneratedFrontendLaneRunsConfiguredE2E | covered |
    | 6 | Optional dev server lifecycle | 6 | TestGeneratedFrontendLaneStartsConfiguredDevServer | covered/deferable |
    | 7 | Worker prompt requires UI evidence | 7 | TestWorkerPromptRequiresBrowserEvidenceForUITasks | covered |
    | 8 | Ops review rejects missing UI evidence | 8 | TestOpsReviewRejectsUITaskWithoutBrowserEvidence | covered |
    | 9 | Documentation and artifact policy | 9 | ./scripts/quality_gate.sh | covered |
    | 10 | End-to-end fixture generation | 10 | TestBootstrapFrontendFixtureEndToEnd | covered |

wiring_gaps:
  - finding: "Frontend lane could be defined but never called."
    fix: "Section 3a and Task 4 require dispatch, wait/print, and frontend.rc aggregation tests."
  - finding: "HasFrontend could remain false because qualityGateData population ignored cfg.Frontend."
    fix: "Task 4 now reads and tests writeQualityGateScript/generateQualityGateScript population."
  - finding: "GenerateConfig could omit frontend detection while unit tests hand-built FrontendConfig."
    fix: "Task 1 and Task 2 now require production GenerateConfig coverage."

negative_space:
  - area: "Existing config rewrite preservation"
    severity: important
    fix: "Task 1 requires structured YAML preservation tests; Task 2 requires non-force preservation and force regeneration."
  - area: "Dev server management"
    severity: minor
    fix: "Task 6 is explicitly deferable; first wedge relies on project-owned Playwright/Cypress server startup."
  - area: "Existing generated QG scripts"
    severity: important
    fix: "Task 2 requires force regeneration coverage; docs must explain the upgrade path."
  - area: "Worktree dependency availability"
    severity: important
    fix: "Section 11 and Task 5 require explicit setup failure behavior."

red_team_scenarios:
  - scenario: "Frontend lane is generated and tests pass, but setup never emits frontend config, so real projects never run the lane."
    beads_pass: false
    feature_works: false
    root_cause: "Bootstrap wiring missing."
    fix: "Task 2 and Task 10 explicitly cover bootstrap and fixture end-to-end."
  - scenario: "E2E command exists, but generated QG treats missing E2E as optional even when required."
    beads_pass: false
    feature_works: false
    root_cause: "Required flag not wired into lane."
    fix: "Task 5 includes TestGeneratedFrontendLaneFailsWhenRequiredE2EMissing."
  - scenario: "Ops review still approves UI tasks with only unit tests."
    beads_pass: false
    feature_works: false
    root_cause: "Review policy not updated."
    fix: "Task 8 adds explicit prompt coverage."
  - scenario: "QG scans Playwright artifacts and node_modules, creating false failures."
    beads_pass: false
    feature_works: false
    root_cause: "Filesystem walkers not scoped."
    fix: "Task 4 requires artifact and dependency exclusions."

integration_points:
  covered:
    - "pkg/langprofile/config.go:Config"
    - "pkg/langprofile/profiles.go:TypeScriptProfile"
    - "pkg/langprofile/profiles.go:JavaScriptProfile"
    - "pkg/langprofile/detect.go:detectJSTools"
    - "cmd/oro/quality_gate_gen.go:qualityGateData"
    - "cmd/oro/quality_gate_gen.go:qualityGateTmpl"
    - "cmd/oro/cmd_init.go:bootstrapProject"
    - "cmd/oro/cmd_setup.go:setupPhase2Detect"
    - "pkg/worker/prompt.go:appendStaticSections"
    - "pkg/ops/review_prompt.go:buildReviewPrompt"
  uncovered: []
```

## Future Work

- Persistent browser daemon: port the useful parts of gstack's browser model after deterministic E2E enforcement lands.
- `oro qa` command: report-only exploratory browser QA that writes `.oro/qa-reports` with screenshots, health score, and repro steps.
- Visual regression: optional Playwright screenshot baseline support with explicit committed golden assets.
- QG evidence integration: include frontend lane metadata in the planned QG evidence object.
- Canary checks: post-merge/deploy browser monitoring for configured local/staging URLs.
