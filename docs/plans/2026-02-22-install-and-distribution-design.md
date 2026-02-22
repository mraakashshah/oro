# Oro Install & Distribution: Making It Easy for People

**Date:** 2026-02-22
**Status:** Draft
**Author:** architect
**Refines:** `2026-02-17-oro-distribution-and-setup.md` (same design, updated for implementation)

## Problem

Oro today can only be used by its own developers. There is no distribution
path — no release binaries, no install script, no way for someone to download
oro and use it on their own project. The quality gate is hardcoded for oro's
own stack, and worker prompts hardcode oro-specific coding rules. Even if
someone got the binary, it would enforce oro-internal conventions on their code.

## Audience

Two audiences:

1. **Users** — developers who want to use oro to build their own software.
   They have macOS with Homebrew, Claude Code CLI with an API key, and a Go
   and/or Python project.
2. **Contributors** — developers working on oro itself. They clone the repo
   and need the full dev toolchain.

Pre-alpha: working is the goal, polish comes later.

## Goal

**For users:**
```bash
curl -fsSL https://raw.githubusercontent.com/mraakashshah/oro/main/scripts/install.sh | bash
cd my-project
oro setup
oro start
```

**For contributors:**
```bash
git clone git@github.com:mraakashshah/oro.git
cd oro
make setup && make install
oro init
```

---

## Design

### 1. Distribution: GitHub Releases + Install Script

#### GoReleaser Configuration (`.goreleaser.yml`)

Builds 3 binaries for darwin/amd64 and darwin/arm64:

```yaml
version: 2

before:
  hooks:
    - make stage-assets

builds:
  - id: oro
    main: ./cmd/oro
    binary: oro
    env: [CGO_ENABLED=0]
    goos: [darwin]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X oro/internal/appversion.version={{.Version}}

  - id: oro-dash
    main: ./cmd/oro-dash
    binary: oro-dash
    env: [CGO_ENABLED=0]
    goos: [darwin]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X oro/internal/appversion.version={{.Version}}

  - id: oro-search-hook
    main: ./cmd/oro-search-hook
    binary: oro-search-hook
    env: [CGO_ENABLED=0]
    goos: [darwin]
    goarch: [amd64, arm64]

archives:
  - id: default
    builds: [oro, oro-dash, oro-search-hook]
    format: tar.gz
    name_template: "oro_{{ .Version }}_{{ .Os }}_{{ .Arch }}"

checksum:
  name_template: checksums.txt
  algorithm: sha256

release:
  github:
    owner: mraakashshah
    name: oro
  draft: true
  prerelease: auto
```

All three binaries ship in a single archive. `draft: true` allows review
before publishing.

#### Release CI Workflow (`.github/workflows/release.yml`)

```yaml
name: Release
on:
  push:
    tags: ['v*']

jobs:
  release:
    runs-on: macos-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Install npm deps (for stage-assets)
        run: npm install
      - uses: goreleaser/goreleaser-action@v6
        with:
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

macOS runner for darwin builds. `make stage-assets` runs as a GoReleaser
before hook.

#### CGO_ENABLED=0 Validation

Add a CI job that builds with `CGO_ENABLED=0` and runs the test suite:

```yaml
  cgo-free-check:
    runs-on: macos-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: make stage-assets
      - run: CGO_ENABLED=0 go build ./cmd/oro
      - run: CGO_ENABLED=0 go test -race -count=1 ./...
      - run: make clean-assets
```

This runs on every push/PR to catch CGO regressions before they affect
releases.

#### Install Script (`scripts/install.sh`)

Modeled on beads' install.sh. Steps:

1. Detect platform (darwin_amd64 or darwin_arm64; fail on non-macOS)
2. Fetch latest release JSON from GitHub API
3. Construct download URL: `oro_<version>_darwin_<arch>.tar.gz`
4. Download + verify checksum
5. Extract tarball to temp directory
6. Install `oro` to `/usr/local/bin/` (writable) or `~/.local/bin/` (fallback)
7. Create `~/.oro/bin/` and `~/.oro/hooks/` directories
8. Install `oro-dash` to `~/.oro/bin/`
9. Install `oro-search-hook` to `~/.oro/hooks/`
10. Re-sign binaries for macOS (codesign — avoids Gatekeeper delay)
11. Check PATH for `oro`, `~/.oro/bin`, print instructions if missing
12. Print: `Run 'cd your-project && oro setup' to get started`

The script supports upgrading: it overwrites existing binaries.

---

### 2. `oro setup` Command

New subcommand. Six phases, run in order. Safe to run multiple times
(idempotent).

#### Phase 1: Prerequisites Check

Verify hard prerequisites that can't be auto-installed:

```
Checking prerequisites...
  [ok] claude 1.0.44
  [ok] git 2.47.0
  [!!] brew not found — required for tool installation
       Install: /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
```

Fail fast if `claude` or `brew` is missing. `git` is also required.

#### Phase 2: Detect Project

Detect languages present in the project:

| Language | Marker files |
|----------|-------------|
| Go | `go.mod` |
| Python | `pyproject.toml`, `setup.py`, or `requirements.txt` |

Uses the existing `langprofile` package. Reads Go module name from `go.mod`
for config generation. Creates `.oro/config.yaml` with detected languages
and coding rules via `langprofile.GenerateConfig()`.

#### Phase 3: Generate Quality Gate

Generate `quality_gate.sh` in the project root — a standalone, self-contained
shell script tailored to the detected languages. This script is committed to
the project repo and becomes the user's source of truth. It evolves
independently from oro.

Also generates language-specific config files:

**For Go projects — `.golangci.yml`** (only if file doesn't exist):

A comprehensive config with 30+ linters enabled: staticcheck, govet, errcheck,
errorlint, wrapcheck, gocyclo, gocognit, funlen, gosec, gocritic, revive,
and more. `{{MODULE_NAME}}` in the goimports local-prefixes setting is
replaced with the actual Go module name.

Test files get relaxed rules (no funlen, gocyclo, wrapcheck, etc.).

**For Python projects — `pyproject.toml` sections** (only add sections that
don't already exist):

```toml
[tool.ruff]
line-length = 120

[tool.ruff.lint]
select = ["E", "F", "W", "I", "N", "UP", "B", "A", "SIM", "RUF"]

[tool.pyright]
venvPath = "."
venv = ".venv"

[tool.pytest.ini_options]
testpaths = ["tests"]
```

If `pyproject.toml` doesn't exist, create it with a minimal `[project]`
section. If it exists, only append missing `[tool.*]` sections. Never
overwrite existing tool configuration.

**Generated `quality_gate.sh`:**

```bash
#!/usr/bin/env bash
set -euo pipefail

# Generated by oro setup — this file is yours to evolve.
# Oro workers run this script after every bead. All checks must pass.

PASS=0
FAIL=0

check() {
    local name="$1"; shift
    if "$@" > /dev/null 2>&1; then
        printf "  [\033[32mPASS\033[0m] %s\n" "$name"
        ((PASS++))
    else
        printf "  [\033[31mFAIL\033[0m] %s\n" "$name"
        ((FAIL++))
    fi
}

# === Go checks ===  (present only if go.mod detected)

echo "Go checks..."
check "gofumpt"         bash -c 'test -z "$(gofumpt -l .)"'
check "goimports"       bash -c 'test -z "$(goimports -l .)"'
check "golangci-lint"   golangci-lint run --timeout 5m ./...
check "go test"         go test -race -shuffle=on -coverprofile=coverage.out ./...
check "coverage >= 70%" bash -c '
    pct=$(go tool cover -func=coverage.out | tail -1 | awk "{print \$NF}" | tr -d "%")
    awk "BEGIN {exit ($pct < 70.0)}"
'
check "govulncheck"     govulncheck ./...
check "go build"        go build ./...
check "go vet"          go vet ./...

# === Python checks ===  (present only if pyproject.toml/setup.py detected)

echo "Python checks..."
check "ruff format"     ruff format --check .
check "ruff check"      ruff check .
check "pyright"         pyright
check "pytest"          uv run pytest

# === Docs & config ===  (always present)

echo "Docs & config checks..."
if command -v markdownlint > /dev/null 2>&1; then
    check "markdownlint" markdownlint '**/*.md'
fi
if command -v yamllint > /dev/null 2>&1; then
    check "yamllint" yamllint .
fi
if command -v shellcheck > /dev/null 2>&1; then
    check "shellcheck" bash -c 'find . -name "*.sh" -not -path "./.git/*" | xargs shellcheck'
fi

# === Summary ===

echo ""
echo "Results: $PASS passed, $FAIL failed"
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
```

Language sections are conditionally included based on detection. Docs checks
use `command -v` guards — they run if the tool is installed, skip silently
if not.

#### Phase 4: Install Tools

**Tier 1: Oro runtime** (always installed):

| Tool | Install method |
|------|----------------|
| `tmux` | `brew install tmux` |
| `jq` | `brew install jq` |
| `rg` | `brew install ripgrep` |
| `uv` | `brew install uv` |
| `bd` | `brew install beads` |

**Tier 2: Go tools** (installed if Go project detected):

| Tool | Install method |
|------|----------------|
| `golangci-lint` | `brew install golangci-lint` |
| `gofumpt` | `go install mvdan.cc/gofumpt@latest` |
| `goimports` | `go install golang.org/x/tools/cmd/goimports@latest` |
| `govulncheck` | `go install golang.org/x/vuln/cmd/govulncheck@latest` |

**Tier 3: Python tools** (installed if Python project detected):

| Tool | Install method |
|------|----------------|
| `ruff` | `uv tool install ruff` |
| `pyright` | `uv tool install pyright` |

All brew installs are batched:

```bash
HOMEBREW_NO_AUTO_UPDATE=1 brew install tmux jq ripgrep uv beads golangci-lint
```

Skip any tool already on PATH. Print status for each:

```
Installing tools...
  [1/7] tmux ............. found (3.5a)
  [2/7] jq ............... found (1.7.1)
  [3/7] rg ............... installing via brew... done (14.1.1)
  [4/7] uv ............... found (0.6.0)
  [5/7] bd ............... installing via brew... done (0.22.0)
  [6/7] golangci-lint .... found (2.1.0)
  [7/7] gofumpt .......... installing via go install... done
```

After install, check whether `~/go/bin` and `~/.local/bin` are on PATH.
Print exact export lines if not:

```
  [!!] ~/go/bin is not on PATH — Go tools may not be found
       Add to your ~/.zshrc:  export PATH="$HOME/go/bin:$PATH"
```

**Developer tools** (`oro setup --dev`) adds:

| Tool | Install method |
|------|----------------|
| `markdownlint` | `brew install markdownlint-cli` |
| `yamllint` | `uv tool install yamllint` |
| `shellcheck` | `brew install shellcheck` |
| `biome` | `brew install biome` |

#### Phase 5: Bootstrap Project

Reuses existing `bootstrapProject()` logic from `cmd_init.go`:

1. `.oro/config.yaml` (created in Phase 2)
2. `.gitignore` entries for `.oro/` and `.beads`
3. `~/.oro/projects/<name>/` directory tree + handoffs dir
4. `.beads` symlink → `~/.oro/projects/<name>/beads/`
5. Extract embedded assets to `~/.oro/` (additive only — never overwrite
   existing files, never delete user-created files)
6. Discover + install companion binaries:
   - Find `oro-dash` and `oro-search-hook` as siblings of the running `oro`
     binary (`os.Executable()` + sibling lookup). Fall back to PATH.
   - Copy to `~/.oro/bin/oro-dash` and `~/.oro/hooks/oro-search-hook`
7. Generate `settings.json` with absolute paths (always overwrite — idempotent)
8. Create `.beads/config.yaml`

#### Phase 6: Doctor Check

```
Verification:
  [ok] claude 1.0.44
  [ok] tmux 3.5a
  [ok] bd 0.22.0
  [ok] quality_gate.sh (executable)
  [ok] .golangci.yml
  [ok] ~/.oro/hooks/oro-search-hook
  [ok] ~/.oro/bin/oro-dash
  [ok] .oro/config.yaml
  [ok] .beads/ initialized

Setup complete. Run 'oro start' to launch the swarm.

If the quality gate fails on first run, open Claude Code in your project
and ask it to run ./quality_gate.sh and fix any issues. The gate is a
regular shell script — Claude can read the errors and adjust it for your
project.
```

Actionable errors for failures:

```
  [!!] golangci-lint not found
       Run: brew install golangci-lint
       Go linting will fail in the quality gate without this.
```

#### Flags

| Flag | Behavior |
|------|----------|
| `--dev` | Install doc/config linting tools (markdownlint, yamllint, shellcheck, biome) |
| `--dry-run` | Print what would be installed/created, do nothing |
| `--skip-tools` | Skip tool installation, only bootstrap project + generate gate |
| `--force-gate` | Regenerate `quality_gate.sh` even if it already exists |
| `--force` | Overwrite all generated files AND oro-provided assets |

#### `oro init` Relationship

`oro init` is simplified to Phase 2 + Phase 5 only (detect + bootstrap).
Use it for initializing additional projects without reinstalling tools.

`oro setup` = prerequisites + detect + generate gate + install tools +
bootstrap + doctor.

---

### 3. Config-Driven Worker Prompts

**Current problem:** `prompt.go` hardcodes oro-specific coding rules
(functional first, go-arch-lint, etc.). Workers on external projects receive
wrong instructions.

**Fix:** `AssemblePrompt` reads `coding_rules` from `.oro/config.yaml` instead
of using hardcoded strings. The `coding_rules` field already exists per
language in the config — it's generated by `langprofile.GenerateConfig()`.

The worker prompt's "Coding Rules" section becomes:

```go
// Read from .oro/config.yaml languages.<lang>.coding_rules
// Concatenate rules from all detected languages
section(b, "Coding Rules", strings.Join(configCodingRules, "\n"))
```

This means `oro setup`'s generated config directly controls what workers are
told about coding style. Users can edit `.oro/config.yaml` to customize.

---

### 4. Additive Asset Extraction

Oro ships built-in skills, hooks, beacons, and commands embedded in the binary.
Users can also create their own. The extraction strategy is **additive**:

- **New file (not on disk)**: write it
- **Existing file**: skip (preserve user's version)
- **User-created file (not in embedded asset list)**: never touched

This means:
- Running `oro setup` after updating oro adds new skills but doesn't overwrite
  existing ones. Users who want updated builtins run `oro setup --force`.
- Users can create custom skills and they survive any number of re-runs.

---

### 5. First-Run Experience

When `oro start` launches with an empty backlog (no beads), the architect
pane displays a welcome message instead of a blank screen:

```
Welcome to oro.

No beads found. To get started, tell me what you'd like to build.
Describe your goal and I'll break it down into a plan.

Examples:
  "Add user authentication with JWT tokens"
  "Refactor the database layer to use connection pooling"
  "Fix the race condition in the order processing pipeline"
```

Triggered by checking `bd ready` + `bd stats` in the architect beacon.

---

### 6. Developer Path Updates

#### `install.md` Rewrite

Two clear paths side by side:

**For users (using oro on your project):**
```bash
curl -fsSL https://raw.githubusercontent.com/mraakashshah/oro/main/scripts/install.sh | bash
cd my-project
oro setup
oro start
```

**For contributors (developing oro itself):**
```bash
git clone git@github.com:mraakashshah/oro.git
cd oro
make setup        # installs dev tools
make install      # builds + installs oro binary
oro init           # bootstraps oro for oro development
```

---

## Idempotency

`oro setup` is safe to run multiple times:

| Resource | Behavior |
|----------|----------|
| Tools already installed | Skip (print "found") |
| `.golangci.yml` exists | Skip (never overwrite) |
| `pyproject.toml` tool sections exist | Skip those sections |
| `quality_gate.sh` exists | Skip. Use `--force-gate` to regenerate. |
| `.oro/config.yaml` exists | Skip |
| Assets (skills, hooks, beacons) | Additive only (new files written, existing preserved) |
| Companion binaries | Reinstall (always update to current version) |
| `.beads` | Skip if initialized |
| `settings.json` | Regenerate (always overwrite — idempotent) |
| Role directories | Re-symlink |

---

## Risks & Mitigations

| Risk | Severity | Mitigation |
|------|----------|------------|
| Worker prompt hardcodes oro-specific coding rules | HIGH | Wire `AssemblePrompt` to read from `.oro/config.yaml` (section 3) |
| CGO_ENABLED=0 may break sqlite at runtime | HIGH | CI validation step: build + test with CGO_ENABLED=0 on every push |
| Companion binaries not found after install | HIGH | `os.Executable()` sibling lookup + PATH fallback + actionable warning |
| `go install` / `uv tool install` binaries not on PATH | HIGH | Check PATH after install, print exact export line for `.zshrc` |
| GoReleaser `make stage-assets` fails in CI | HIGH | CI runs on full checkout with npm installed. Tested by release workflow. |
| User's existing `.golangci.yml` gets overwritten | HIGH | Only create if file doesn't exist |
| User's existing `quality_gate.sh` gets overwritten | HIGH | Skip if exists. Require `--force-gate` |
| User-created skills/hooks deleted by re-run | HIGH | Additive extraction: only write new files |
| `$HOME` literal in settings.json | HIGH | Use `os.UserHomeDir()` for absolute paths |
| Quality gate fails on first run (unusual test setup) | MEDIUM | Print troubleshooting note pointing to Claude-assisted fix |
| First-run blank screen confuses new users | MEDIUM | Welcome message in architect beacon |
| `brew install beads` may not work yet | MEDIUM | Verify bd install path. Fallback: `go install` |
| Generated gate references tools not yet installed | LOW | Phase 4 (install) runs before gate is needed. Gate is generated but not executed until `oro start`. |

---

## Files Changed

| File | Change |
|------|--------|
| `.goreleaser.yml` (new) | GoReleaser config for 3 binaries, darwin only |
| `.github/workflows/release.yml` (new) | Release CI workflow on `v*` tags |
| `scripts/install.sh` (new) | Curl install script for users |
| `cmd/oro/cmd_setup.go` (new) | `oro setup` command — 6 phases |
| `cmd/oro/gate_template.go` (new) | Quality gate script template + generation logic |
| `cmd/oro/golangci_template.go` (new) | `.golangci.yml` template + module name substitution |
| `cmd/oro/pyproject.go` (new) | `pyproject.toml` section merger |
| `cmd/oro/tools_setup.go` (new) | Tiered tool manifest, install logic, PATH checks |
| `cmd/oro/doctor.go` (new) | Doctor verification checks + companion binary discovery |
| `cmd/oro/cmd_init.go` | Simplify to Phase 2 + Phase 5 only, extract shared functions |
| `pkg/worker/prompt.go` | Read coding rules from `.oro/config.yaml` instead of hardcoding |
| `assets/beacons/architect.md` | Add empty-backlog welcome message |
| `install.md` | Rewrite for dual-path install |

---

## Out of Scope

- Multi-OS support (Linux, Windows) — macOS only for now
- Homebrew tap — separate future effort
- Languages beyond Go and Python
- `oro start` auto-launch after setup
- Upgrading between oro versions (idempotent re-run is the upgrade path)
- Installing Claude Code — prerequisite, user's responsibility
- Project-specific quality gate customization UI — users edit the script directly
