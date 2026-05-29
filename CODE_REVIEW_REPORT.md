# Code Review Report — Dead Code & Bugs

## 1. Executive Summary

- **Scope reviewed:** Go CLI/library code under `cmd/`, `internal/`, and `pkg/`; Python hook/test tooling under `assets/hooks`, `.claude/hooks`, `scripts`, and `tests`; release/build config in `Makefile`, `.goreleaser.yml`, `go.mod`, `pyproject.toml`, and Node lint config. Approximate tree shape: 552 Go files, 54 Python files, 35 shell scripts, plus docs/assets.
- **Headline counts:** 5 confirmed findings: 2 bugs and 3 dead-code/dead-manifest findings. Severity: 3 Medium, 2 Low.
- **Most important risk:** The codebase's deterministic tooling is strong and currently clean, but the tree carries stale public/runtime surfaces from older memory, task, dashboard, and dependency work. The most visible instances are public memory and task commands that are still advertised even though representative invocations fail at runtime.

## 2. Top Priorities — Fix These First

1. **MEM-001:** Remove or redirect the advertised legacy memory commands and stale hooks/prompts/docs that still point at retired memory behavior.
2. **TASK-001:** Implement or hide public `oro task` subcommands that are help-visible but return "not implemented yet".
3. **DEPS-001:** Remove unused Go module requirements that `go mod tidy -diff` proves are no longer needed.
4. **DASH-001:** Delete or wire the unreachable `cmd/oro-dash` detail model code.
5. **CLI-001:** Remove or quarantine the retired `newBeadCmdWithStore` wrapper so tests do not keep a removed CLI surface in production files.

## 3. Findings

## Medium

### MEM-001 Advertised Memory Commands Always Fail Against Retired Store

- **Type:** Bug
- **Severity:** Medium
- **Confidence:** Confirmed — the root command registers the memory commands, help/docs/prompts/hooks advertise or call them, and executing representative commands reaches the retired store error.
- **Location:** `cmd/oro/root.go:50-54`, `cmd/oro/cmd_help.go:30-35`, `cmd/oro/store.go:13-82`, `cmd/oro/cmd_remember.go:55-101`, `cmd/oro/cmd_recall.go:72-124`, `cmd/oro/cmd_forget.go:52-66`, `cmd/oro/cmd_memories.go:139-205`, `assets/hooks/session_start_extras.py:228-242`, `assets/hooks/session_start_extras.py:616-617`, `pkg/worker/prompt.go:460-463`, `README.md:62`, `README.md:228-240`, `README.md:343-346`, `README.md:396-406`, `README.md:512`
- **Evidence:** `cmd/oro/root.go` registers `newRememberCmdWithStore(nil)`, `newRecallCmdWithStore(nil)`, `newForgetCmd()`, and `newMemoriesCmd()`. `cmd/oro/cmd_help.go` advertises these under "Memory". Their nil-store paths call `defaultMemoryStore()`/`defaultMemoriesStore()`, which now return `retiredMemoryStore`; all operational methods return `errLegacyMemoryRetired`. Runtime probes with temporary `ORO_HOME` confirmed `go run ./cmd/oro remember 'lesson: probe retired memory'` fails with `Error: remember: legacy memory has been retired; use cards instead`, `go run ./cmd/oro recall probe` fails with `Error: recall: legacy memory has been retired; use cards instead`, and `go run ./cmd/oro memories list --format=json --limit=5` fails with `Error: memories list: legacy memory has been retired; use cards instead`. `assets/hooks/session_start_extras.py` still shells out to `oro memories list --format=json --limit=N` for "Recent memories from memories.db" and silently returns no learnings on failure. `pkg/worker/prompt.go` still instructs workers to run `oro remember`, while `README.md` still describes the FTS5 memory system, `oro remember`, `oro recall`, `oro memories list`, and a `pkg/memory` tree that no longer exists.
- **Impact:** Users and workers are directed toward commands that cannot succeed. Session-start context silently drops recent-memory injection, and workers following the exit prompt lose memory writes instead of persisting them to the replacement cards system. The README also overstates a retired cross-session learning capability.
- **Suggested fix:** Either remove/hide the retired memory commands from root/help and update prompts to use cards, or turn these commands into compatibility shims that write/search the cards store with clear migration semantics.

### TASK-001 Public task subcommands are registered but still return stub errors

- **Type:** Bug
- **Severity:** Medium
- **Confidence:** Confirmed — the commands are public/help-visible and representative invocations reach the shared "not implemented yet" stub.
- **Location:** `cmd/oro/cmd_task.go:50-53`, `cmd/oro/cmd_bead.go:542-565`, `cmd/oro/cmd_bead.go:707-715`
- **Evidence:** `cmd/oro/cmd_task.go` registers `task search`, `task import`, and `task doctor` through `newBeadStubCmd`; `cmd/oro/cmd_bead.go` also wires `tag add/rm`, `meta set/get/rm`, and `note list` through that same stub factory. `go run ./cmd/oro task --help` lists `doctor`, `import`, `meta`, `note`, `search`, and `tag` as available commands. Runtime probes with temporary `ORO_HOME` confirmed `go run ./cmd/oro task search probe`, `go run ./cmd/oro task import /tmp/no-such-oro-snapshot.json`, `go run ./cmd/oro task doctor`, `go run ./cmd/oro task tag add oro-1 alpha`, `go run ./cmd/oro task meta get oro-1 key`, and `go run ./cmd/oro task note list oro-1` all fail with `oro task ... is not implemented yet`.
- **Impact:** Users can discover and invoke task-management commands that look supported but fail before doing useful work. That makes CLI help unreliable and can break automation that trusts `oro task --help` as the supported surface.
- **Suggested fix:** Implement the registered task subcommands, or hide/remove the unsupported stubs from the public command tree until they are real. If they are intentionally reserved names, make help text explicit that they are unavailable rather than presenting them as operational commands.

### DEPS-001 Unused Go Module Requirements Remain in go.mod

- **Type:** Dead code
- **Severity:** Medium
- **Confidence:** Confirmed — `go mod tidy -diff`, `go mod why -m`, and import-string search all agree these modules are not needed by the current build graph.
- **Location:** `go.mod:10-24`, `go.mod:29-31` plus stale `go.sum` entries.
- **Evidence:** `go mod tidy -diff` exits non-zero and removes direct requirements `github.com/daulet/tokenizers`, `github.com/mattn/go-sqlite3`, `github.com/yalue/onnxruntime_go`, `golang.org/x/time`, and indirect `github.com/atotto/clipboard`, along with stale sums for older transitive versions. `go mod why -m github.com/daulet/tokenizers github.com/mattn/go-sqlite3 github.com/yalue/onnxruntime_go golang.org/x/time github.com/atotto/clipboard` reports the main module does not need each module. `rg` for those import paths outside `go.mod`/`go.sum` found no code imports, only one design-doc mention of a possible future `go-sqlite3` file.
- **Impact:** Unused dependencies add supply-chain and vulnerability-audit noise. This already shows up in `govulncheck`: the code calls no vulnerable symbols, but the scan still reports vulnerabilities in imported/required packages.
- **Suggested fix:** Run `go mod tidy`, review the diff, and keep any intentionally staged future dependency only if it has an active build-tagged import or a documented temporary exception.

## Low

### DASH-001 Unreachable oro-dash Detail Model

- **Type:** Dead code
- **Severity:** Low
- **Confidence:** Confirmed — production entry points and all non-test references were searched.
- **Location:** `cmd/oro-dash/detail.go:17-53`
- **Evidence:** `go run golang.org/x/tools/cmd/deadcode@latest ./...` flags `DefaultTheme`, `NewStyles`, `newDetailModel`, and `DetailModel.renderOverviewTab` as unreachable. `rg -n "\\b(DetailModel|newDetailModel|renderOverviewTab|DefaultTheme|NewStyles|Theme|Styles)\\b" cmd/oro-dash pkg cmd -g '!**/*_test.go'` finds only declarations in `cmd/oro-dash/detail.go`; the matching test-only search finds only `cmd/oro-dash/detail_test.go`. The `cmd/oro-dash` entry path is `main` → `runCLI` → `renderHeadlessDashboardSnapshot` → `views.NewParade`, with no call into `detail.go`.
- **Impact:** This keeps an obsolete TUI detail implementation and test suite alive even though the shipped `oro-dash` command has no interactive detail path. Future dashboard work can accidentally update the wrong package instead of the active `pkg/dashboard/views`/web dashboard path.
- **Suggested fix:** Remove `detail.go` and its dedicated tests if the old `oro-dash` detail view is retired, or wire it into an actual `oro-dash` entry path before treating the tests as meaningful coverage.

### CLI-001 Retired bead Root Command Factory Is Production-Compiled But Runtime-Unreachable

- **Type:** Dead code
- **Severity:** Low
- **Confidence:** Confirmed for the wrapper only; the subcommand factories in `cmd_bead.go` are still used by `newTaskCmdWithStore` and must not be removed wholesale.
- **Location:** `cmd/oro/cmd_bead.go:20-55`
- **Evidence:** `rg -n "newBeadCmdWithStore" cmd/oro pkg internal -g '!**/*_test.go'` finds only the declaration. Test-only references remain in `cmd/oro/cmd_bead_test.go` and `cmd/oro/cmd_task_test.go`. `cmd/oro/root.go:33-73` registers `newTaskCmd()` but not `newBeadCmdWithStore()`. `cmd/oro/cmd_task_test.go:171-195` explicitly asserts `oro bead status` is an unknown command.
- **Impact:** Tests use the retired command factory as a parity oracle, so production code continues to compile an unreachable root command wrapper. This blurs the line between live CLI surface and compatibility scaffolding after the public command moved to `oro task`.
- **Suggested fix:** Move the parity fixture into `_test.go`, or replace it with a test-only expected subcommand list. Keep the shared `newBeadReadyCmd`, `newBeadListCmd`, and related factories unless/until `newTaskCmdWithStore` stops using them.

## 4. Open Questions & Unverified Items

- `cmd/hello` and `cmd/test-mgdata-parse` are not referenced by production code, `Makefile`, or `.goreleaser.yml`; searches for `cmd/hello`, `Hello from oro worker`, `cmd/test-mgdata-parse`, and `test-mgdata-parse` found only their own files/tests and incidental text. I did **not** promote these to confirmed dead because their names and comments suggest intentional manual/test-helper binaries. Owner confirmation that they are not manually invoked would promote them to confirmed dead build targets.
- `go run golang.org/x/tools/cmd/deadcode@latest ./...` produced many unreachable-symbol reports in exported `pkg/...` APIs. I treated that output as candidate-only because this repository contains library packages, command factories, plugin/runtime hooks, and test seams; exported package APIs cannot be called dead on internal main-reachability alone.
- `pkg/dbutil.ResolveSqliteVecLibPath` is production-dead by reference search outside tests/docs and is explicitly marked `//oro:testonly — wired into production by subsequent sqlite-vec load bead (oro-p545)`. I left it out of Findings because the marker indicates an intentionally dormant test seam/future hook; owner confirmation that `oro-p545` will not land would promote it to confirmed dead.
- No additional confirmed semantic bugs were found in the completed shards. This is not a proof that none exist; it means I did not find another bug with a concrete triggering path and verified line evidence within this pass.

## 5. Coverage & Limitations

- **Reviewed:** `cmd/oro`, `cmd/oro-dash`, `cmd/oro-search-hook`, command registration, build/release manifests, Go module manifest, dispatcher/web entry surfaces, dashboard package reachability, Python hook/test configuration, README memory/task references, and shell/docs lint surfaces.
- **Partially reviewed:** deep semantic behavior inside the largest dispatcher workflows, `pkg/beadstore` SQL invariants, and worker lifecycle code. These were covered by tests/lint and sampled for entry/dynamic surfaces, but not exhaustively audited line-by-line.
- **Not reviewed exhaustively:** archived docs, `node_modules`, `.venv`, `.cache`, `.worktrees`, generated/staged `cmd/oro/_assets` copies beyond their role as embedded assets.
- **Tools run:** `go list ./...`; `go test ./...`; `go test -coverprofile=/tmp/oro-review-coverage.out ./internal/... ./pkg/... ./cmd/...` with total statement coverage 80.0%; `go vet ./...`; `golangci-lint run --timeout 5m`; `nilaway -pretty-print=false -exclude-test-files -include-pkgs=oro ./cmd/... ./internal/... ./pkg/...`; `go tool govulncheck ./...`; `uv run ruff check .`; `pyright`; `npm exec -- biome check --files-ignore-unknown=true package.json biome.json context_budgets.json pruning.json assets/thresholds.json`; `npm exec -- markdownlint-cli2 --config .markdownlint.yml 'docs/**/*.md' '*.md' '!references/**' '!archive/**'`; shellcheck over repository shell scripts with quality-gate exclusions; `go mod tidy -diff`; `go mod why -m ...`; `go run golang.org/x/tools/cmd/deadcode@latest ./...`; targeted CLI probes for retired memory and task-stub commands.
- **Tool limitations:** local `staticcheck` could not run because it was built with Go 1.24.2 while the module requires Go 1.26.3. I used `golangci-lint`'s configured `staticcheck`/`unused` linters instead, which completed cleanly. I did not run `make test` or `make gate` because those targets call `stage-assets` and mutate tracked generated asset directories; I ran their read-only constituent checks directly where possible.
- **Confidence statement:** Dead-code verdicts rest on reference search plus entry-point/build-graph checks. Semantic-bug review was best-effort and not exhaustive.

## 6. Appendix — Reconnaissance Map

### Entry Points

- Go binaries: `cmd/oro/main.go`, `cmd/oro-dash/main.go`, `cmd/oro-search-hook/main.go`, `cmd/hello/main.go`, `cmd/test-mgdata-parse/main.go`.
- Main CLI routing: `cmd/oro/root.go` registers Cobra subcommands including `start`, `dispatcher`, `task`, `dashboard`, `worker`, `models`, `harness`, `resume`, and related operational commands.
- Hook binary: `cmd/oro-search-hook/main.go` reads JSON from stdin and fail-opens to `{}` unless it denies a large read with an AST summary.
- Dispatcher runtime: `pkg/dispatcher.Dispatcher.Run` initializes DB state, UDS/listener behavior, worker assignment loops, HTTP dashboard when enabled, heartbeat checks, and recovery/ops flows.
- HTTP routes: dispatcher web server registers `/healthz` and a root web handler via `pkg/web`.
- File/event consumers: dispatcher fsnotify task-data watch with polling fallback; dashboard file/CLI polling helpers.
- Migrations: `pkg/protocol/schema.go` migration constants/functions and `pkg/beadstore/migrations`.
- Scripts and build/release: `Makefile`, `scripts/install.sh`, `scripts/quality_gate.sh`, `.goreleaser.yml`.
- Tests: Go package tests under `cmd`, `internal`, `pkg`, and `tests/integration`; Python tests under `tests` and hook test files.

### Dynamic Reachability Surfaces

- Cobra command factories and aliases.
- Embedded assets via `//go:embed all:_assets` in `cmd/oro/embed.go`; `Makefile stage-assets` copies canonical `assets/` into `cmd/oro/_assets`.
- Agent hook scripts and hook binaries invoked by external agent runtimes through JSON stdin/stdout contracts.
- Runtime selection through `ORO_AGENT_RUNTIME`, config files, and role/tier routing.
- SQLite schema migrations, triggers, and row hydration into protocol/beadstore/dashboard structs.
- Interface-based runtime, worker, ops, process, and store injection.
- Subprocess execution via worker, ops, git, tmux, quality-gate, and install helpers.
- Template/rendering surfaces in quality-gate generation, dashboard/web views, and markdown rendering.

### Public API Surface

- Exported symbols in `pkg/...` are treated as internal library/public package API for this repository and were not called dead solely because a main-package reachability tool reported them unreachable.
- Published/installed command surfaces are `oro` and `oro-search-hook` per `.goreleaser.yml`; `oro-dash` exists as a command package but is not included in the current GoReleaser build list.
- User-facing assets include hooks, skills, beacons, commands, rules, `ORO_AGENT.md`, `CLAUDE.md`, `AGENTS.md`, and `thresholds.json`.

### Build Graph

- `go list ./...` reports packages under root, `cmd/hello`, `cmd/oro`, `cmd/oro-dash`, `cmd/oro-search-hook`, `cmd/test-mgdata-parse`, `internal/appversion`, and `pkg/...`.
- `.goreleaser.yml` builds `oro` and `oro-search-hook` for Darwin amd64/arm64 and includes `sqlite-vec.dylib` in archives.
- `Makefile build` stages assets, builds `./cmd/oro`, builds `./cmd/oro-search-hook`, then cleans staged assets. `make test` and `make gate` also stage/clean assets.
- Python tooling is configured by `pyproject.toml`, with Ruff, Pyright, and pytest over `tests`.
- Node tooling is private dev tooling for Biome and markdownlint.

### Conventions & Suppressions

- Hooks generally fail open so broken hook logic does not block agent operation.
- Errors are usually wrapped with context; `golangci-lint` enforces `wrapcheck`, `errcheck`, `nilerr`, `bodyclose`, `noctx`, and `gosec` with explicit exclusions.
- Public UX terminology prefers `task`; tests intentionally guard against reintroducing `bead` as a normal public command.
- `//oro:testonly` suppresses the repo's custom dead-export detector.
- Existing lint exclusions cover dashboard TUI rendering complexity, test-package choices, generated code, and accepted local-path gosec cases.
