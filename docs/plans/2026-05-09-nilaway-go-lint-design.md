# NilAway Go Lint Design

Date: 2026-05-09

## Goal

Add Uber's NilAway analyzer to Oro's Go lint path so likely nil panics are caught before merge, without breaking every worker quality gate on the current baseline.

## Research

Files read:

- `Makefile`: `lint` currently runs `golangci-lint run --timeout 5m`; `setup` installs pinned `golangci-lint v2.10.1`.
- `.golangci.yml`: v2 config, no custom linters today.
- `.github/workflows/ci.yml`: CI uses `golangci/golangci-lint-action@v9` for Go lint and does not run custom lint binaries.
- `scripts/quality_gate.sh`: worker quality gate Go tier 2 runs `golangci-lint`, dead export detection, beadstore import checks, and optionally `go-arch-lint`.

Upstream NilAway notes:

- NilAway is a `go/analysis` analyzer that can run through analyzer drivers or as a standalone checker.
- Upstream recommends `include-pkgs` so third-party dependencies do not dominate reports.
- golangci-lint integration is supported through a custom module-plugin binary, not by simply adding `nilaway` to the built-in `enable` list.
- The standalone checker supports `exclude-test-files`, `include-errors-in-files`, and `exclude-errors-in-files`, but it does not give the same suppression ergonomics as golangci-lint.

Local spike:

```bash
go install go.uber.org/nilaway/cmd/nilaway@v0.0.0-20260318203545-ad240b12fb4c
nilaway -pretty-print=false -exclude-test-files -include-pkgs=oro ./cmd/... ./internal/... ./pkg/...
```

Result: exits nonzero with existing production findings. The first groups are in:

- `pkg/beadstore/v3methods.go`
- `pkg/dispatcher/bead_graph.go`
- `pkg/dispatcher/dispatcher.go`
- `pkg/dispatcher/worker_pool.go`
- `cmd/oro/cmd_bead_migrate.go`
- `cmd/oro/cmd_global_oro_approach.go`
- `cmd/oro/tmux.go`
- `pkg/edit/splice.go`

This means a one-step blocking addition would make `make lint`, CI, and worker quality gates fail immediately.

## Decision

Adopt NilAway in two phases:

1. Add a pinned standalone NilAway target and make it runnable from the Go lint surface.
2. Clean up or explicitly suppress the current baseline, then make NilAway blocking in `make lint`, CI, and `scripts/quality_gate.sh`.

Use standalone NilAway first. It is simpler than maintaining a custom golangci-lint binary, and it still gives the exact analyzer signal. Revisit golangci-lint module-plugin integration after the baseline is green if cache behavior or suppression ergonomics become painful.

## Implementation Shape

Add Makefile variables:

```make
NILAWAY_VERSION ?= v0.0.0-20260318203545-ad240b12fb4c
NILAWAY_PACKAGES ?= ./cmd/... ./internal/... ./pkg/...
NILAWAY_FLAGS ?= -pretty-print=false -exclude-test-files -include-pkgs=oro
```

Add targets:

```make
install-nilaway:
        go install go.uber.org/nilaway/cmd/nilaway@$(NILAWAY_VERSION)

nilaway:
        nilaway $(NILAWAY_FLAGS) $(NILAWAY_PACKAGES)
```

Phase 1 can keep `lint` unchanged and document `make nilaway` as the adoption check. Phase 2 changes `lint` to:

```make
lint:
        golangci-lint run --timeout 5m
        $(MAKE) nilaway
```

Update CI:

- Phase 1: add a separate non-blocking or manually runnable NilAway workflow step only if the team wants visibility before fixes.
- Phase 2: install pinned NilAway and run `make nilaway` as a blocking step after golangci-lint.

Update `scripts/quality_gate.sh`:

- Add a Go tier 2 check named `nilaway`.
- Run it only when `nilaway` is installed.
- Once `setup` and CI install it, this becomes blocking in normal developer and worker runs.
- Use the same command as `make nilaway`:

```bash
nilaway -pretty-print=false -exclude-test-files -include-pkgs=oro ./cmd/... ./internal/... ./pkg/...
```

## Baseline Policy

Prefer real fixes for straightforward findings:

- initialize slices/maps before writes where NilAway catches true nil writes
- guard returned pointers before dereference
- guard map lookups before using pointer values
- return empty slices instead of nil when callers slice or index without nil checks

Use suppression only when NilAway cannot express an invariant already enforced by surrounding code. Suppressions must be narrow and include the invariant in a comment. Avoid directory-wide suppression except as a temporary phase-1 baseline.

## Acceptance

Final state:

- `make setup` installs the pinned NilAway binary.
- `make nilaway` runs the pinned analyzer command.
- `make lint` fails when NilAway reports a production nil panic.
- GitHub Actions installs and runs NilAway on PRs.
- `scripts/quality_gate.sh` includes NilAway in Go tier 2.
- `./scripts/quality_gate.sh` passes on the repo.

## Risks

- NilAway currently reports existing production findings, so blocking integration must wait for baseline cleanup.
- Standalone NilAway may be slower than module-plugin golangci-lint because upstream notes standalone facts stay in memory.
- The upstream project warns about false positives and breaking changes; pinning is required for reproducibility.
- Running NilAway against `./...` includes tests unless `exclude-test-files` is passed; this repo should start with production-only enforcement.

## Task Graph

Epic: Add NilAway to Go lint

1. Add a pinned NilAway make target
   - Test: `Makefile` target exists and `make nilaway` invokes `nilaway $(NILAWAY_FLAGS) $(NILAWAY_PACKAGES)`.
   - Cmd: `make -n nilaway`
   - Assert: output contains `nilaway -pretty-print=false -exclude-test-files -include-pkgs=oro ./cmd/... ./internal/... ./pkg/...`
   - Read: `Makefile:lint`, `Makefile:setup`

2. Capture and triage the current NilAway baseline
   - Test: write a baseline report under `docs/plans/notes/`.
   - Cmd: `nilaway -pretty-print=false -exclude-test-files -include-pkgs=oro ./cmd/... ./internal/... ./pkg/... > /tmp/nilaway.out 2>&1; test -s /tmp/nilaway.out`
   - Assert: report groups findings by package with fix-or-suppress decision.
   - Read: `pkg/beadstore/v3methods.go`, `pkg/dispatcher/worker_pool.go`, `pkg/edit/splice.go`, `cmd/oro/tmux.go`

3. Fix first-party true positives
   - Test: focused Go tests for each touched package.
   - Cmd: `go test ./pkg/beadstore/... ./pkg/dispatcher/... ./pkg/edit/... ./cmd/oro/... -count=1`
   - Assert: tests pass and NilAway finding count decreases.
   - Read: files identified by the baseline report.

4. Update developer setup and CI
   - Test: setup and workflow install the pinned version.
   - Cmd: `make -n setup`
   - Assert: output includes `go install go.uber.org/nilaway/cmd/nilaway@$(NILAWAY_VERSION)`; CI has a blocking NilAway step.
   - Read: `Makefile:setup`, `.github/workflows/ci.yml`

5. Wire NilAway into blocking lint
   - Test: Makefile and quality gate include NilAway.
   - Cmd: `make lint && ./scripts/quality_gate.sh`
   - Assert: both pass and fail if a synthetic nil dereference is temporarily introduced.
   - Read: `Makefile:lint`, `scripts/quality_gate.sh:lane_go`, `.github/workflows/ci.yml`
