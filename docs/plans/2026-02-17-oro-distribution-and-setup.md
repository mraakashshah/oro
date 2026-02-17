# Oro Distribution & Setup: From Download to `oro start`

**Date:** 2026-02-17
**Status:** Draft
**Author:** architect
**Supersedes:** `2026-02-15-oro-setup-design.md` (incorporated and extended)

## Problem

Oro today can only be used by its own developers. There is no distribution
path — no release binaries, no install script, no way for someone to download
oro and use it on their own project. The quality gate is hardcoded for oro's
own stack (go-arch-lint, dead export detection, `make stage-assets`), so even
if someone got the binary, it would enforce oro-internal checks on their code.

## Audience

Developers who want to use oro to build their own software. They have:

- macOS with Homebrew
- Claude Code CLI installed with an API key
- A Go and/or Python project they want to manage with oro

Pre-alpha: clunky is acceptable. Polished onboarding is not the goal —
*working* onboarding is.

## Goal

```
# Download from GitHub Releases (pre-alpha)
# Future: brew install oro

cd my-project
oro setup
oro start
```

`oro setup` detects the project's languages, generates a tailored quality gate
with strong defaults, installs runtime dependencies, bootstraps the project
for oro, and verifies everything works.

---

## Design

### 1. Distribution: GitHub Releases

Use [GoReleaser](https://goreleaser.com/) to build and publish release
binaries on GitHub. Triggered by git tags (`v0.x.x`).

#### `.goreleaser.yml`

```yaml
builds:
  - id: oro
    main: ./cmd/oro
    binary: oro
    goos: [darwin]
    goarch: [amd64, arm64]
    ldflags:
      - -X oro/internal/appversion.version={{ .Version }}
    hooks:
      pre: make stage-assets
      post: make clean-assets

  - id: oro-dash
    main: ./cmd/oro-dash
    binary: oro-dash
    goos: [darwin]
    goarch: [amd64, arm64]

  - id: oro-search-hook
    main: ./cmd/oro-search-hook
    binary: oro-search-hook
    goos: [darwin]
    goarch: [amd64, arm64]

archives:
  - id: default
    builds: [oro, oro-dash, oro-search-hook]
    format: tar.gz
    name_template: "oro_{{ .Version }}_{{ .Os }}_{{ .Arch }}"

checksum:
  name_template: checksums.txt

release:
  github:
    owner: <owner>
    name: oro
  draft: true
  prerelease: auto
```

All three binaries ship in a single archive. The user downloads one tarball,
extracts it, and adds the directory to PATH (or moves all 3 binaries to
`/usr/local/bin/`).

#### Companion binary discovery

`oro setup` needs to find `oro-dash` and `oro-search-hook` to copy them to
`~/.oro/bin/` and `~/.oro/hooks/`. Strategy: call `os.Executable()` to find
the running `oro` binary's path, then look for `oro-dash` and `oro-search-hook`
as siblings in the same directory. This works because the release tarball
extracts all 3 binaries together. If not found as siblings, check PATH. If
still not found, print a warning with instructions.

#### PATH verification

After installing Go tools (`go install`) and Python tools (`uv tool install`),
check whether `~/go/bin` and `~/.local/bin` are on the current PATH. If not,
print a warning with the exact export line to add:

```
  [!!] ~/go/bin is not on PATH — Go tools may not be found
       Add to your ~/.zshrc:  export PATH="$HOME/go/bin:$PATH"
```

#### GitHub Actions release workflow

```yaml
# .github/workflows/release.yml
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
      - uses: goreleaser/goreleaser-action@v6
        with:
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

macOS-only for now. `draft: true` lets us review before publishing.

### 2. `oro setup` command

Six phases, run in order. Safe to run multiple times (idempotent).

#### Phase 1: Prerequisites check

Verify hard prerequisites that can't be auto-installed:

```
Checking prerequisites...
  [ok] claude 1.0.44
  [ok] git 2.47.0
  [!!] brew not found — required for tool installation
```

Fail fast if `claude` or `brew` is missing.

#### Phase 2: Detect project

Detect languages present in the project:

| Language | Marker files |
|----------|-------------|
| Go | `go.mod` |
| Python | `pyproject.toml`, `setup.py`, or `requirements.txt` |

Use the existing `langprofile` package. Read the Go module name from `go.mod`
(first line: `module <name>`) for use in generated configs.

Create `.oro/config.yaml` with detected language profiles using
`langprofile.GenerateConfig()` (already implemented).

#### Phase 3: Generate quality gate

Generate `quality_gate.sh` in the project root — a standalone, self-contained
shell script tailored to the detected languages. This script is committed to
the project repo and becomes the source of truth. It evolves independently
from the config that generated it.

Also generate language-specific config files:

**For Go projects — `.golangci.yml`** (only if file does not already exist):

```yaml
version: "2"

run:
  timeout: 5m
  modules-download-mode: readonly

linters:
  default: none
  enable:
    # Correctness
    - staticcheck
    - govet
    - ineffassign
    - unused
    - errcheck

    # Error handling
    - errorlint
    - wrapcheck
    - nilerr
    - errname

    # Complexity
    - gocyclo
    - gocognit
    - funlen
    - nestif

    # Structure
    - gochecknoglobals
    - gochecknoinits
    - testpackage

    # Resource safety
    - bodyclose
    - noctx
    - durationcheck

    # Cleanup
    - unconvert
    - unparam
    - nakedret
    - whitespace

    # Duplication
    - dupl

    # Testing
    - thelper

    # Security
    - gosec

    # Style
    - gocritic
    - revive
    - misspell

    # Performance
    - prealloc

  settings:
    gocyclo:
      min-complexity: 15
    gocognit:
      min-complexity: 20
    funlen:
      lines: 60
      statements: 40
      ignore-comments: true
    nestif:
      min-complexity: 4
    errcheck:
      check-type-assertions: true
      exclude-functions:
        - (io.Closer).Close
        - (*os.File).Close
    wrapcheck:
      ignore-sigs:
        - .Errorf(
        - errors.New(
        - errors.Join(
    nakedret:
      max-func-lines: 10
    dupl:
      threshold: 200
    gocritic:
      enabled-tags:
        - diagnostic
        - style
        - performance
      disabled-checks:
        - hugeParam
        - rangeValCopy
        - commentedOutCode
    revive:
      severity: warning
      rules:
        - name: blank-imports
        - name: context-as-argument
        - name: context-keys-type
        - name: dot-imports
        - name: error-return
        - name: error-strings
        - name: error-naming
        - name: exported
          arguments:
            - checkPrivateReceivers
            - sayRepetitiveInsteadOfStutters
        - name: if-return
        - name: increment-decrement
        - name: var-naming
        - name: var-declaration
        - name: package-comments
        - name: range
        - name: receiver-naming
        - name: time-naming
        - name: unexported-return
        - name: indent-error-flow
        - name: errorf
        - name: empty-block
        - name: superfluous-else
        - name: unreachable-code
        - name: redefines-builtin-id
        - name: get-return
        - name: string-of-int
        - name: early-return
        - name: unnecessary-stmt
    prealloc:
      simple: true
      range-loops: true
      for-loops: true

  exclusions:
    generated: lax
    presets:
      - std-error-handling
    rules:
      - path: _test\.go
        linters:
          - funlen
          - gocyclo
          - gocognit
          - gochecknoglobals
          - wrapcheck
          - noctx
          - unparam
          - dupl
          - prealloc
          - gocritic
      - path: main\.go
        linters:
          - gochecknoinits
          - gochecknoglobals

formatters:
  enable:
    - goimports
  settings:
    gofumpt:
      extra-rules: true
    goimports:
      local-prefixes:
        - {{MODULE_NAME}}
```

`{{MODULE_NAME}}` is replaced with the Go module name read from `go.mod`.

**For Python projects — `pyproject.toml` additions** (only add sections that
do not already exist in the file):

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
section plus the tool sections above. If it exists but is missing specific
`[tool.*]` sections, append only the missing sections. Never overwrite
existing tool configuration.

**Generated `quality_gate.sh`:**

The script follows this structure:

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

Key differences from oro's own quality_gate.sh:

| Removed (oro-specific) | Kept (universal) |
|------------------------|-------------------|
| `make stage-assets` / `make clean-assets` | gofumpt, goimports |
| go-arch-lint | golangci-lint (full config) |
| Dead export detection | go test -race with 70% coverage |
| Specific directory exclusions | govulncheck, go build, go vet |
| 85% coverage minimum | ruff, pyright, pytest |
| | Docs checks (optional, only if tool installed) |

The docs/config checks (markdownlint, yamllint, shellcheck) are wrapped in
`command -v` guards — they run if the tool is installed, skip silently if not.
This avoids forcing users to install doc-linting tools they may not want.

#### Phase 4: Install tools

Install runtime tools needed by oro and the generated quality gate.

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

All brew installs are batched into a single call:

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

**Developer tools** (`oro setup --dev`) adds:

| Tool | Install method |
|------|----------------|
| `markdownlint` | `brew install markdownlint-cli` |
| `yamllint` | `uv tool install yamllint` |
| `shellcheck` | `brew install shellcheck` |
| `biome` | `brew install biome` |

These correspond to the `command -v` guarded checks in the quality gate — the
gate works without them, but `--dev` installs them for full coverage.

#### Phase 5: Bootstrap project

This phase sets up oro's infrastructure in the project. Reuses existing logic
from `bootstrapProject()` in `cmd_init.go`:

1. Create `.oro/config.yaml` (done in Phase 2)
2. Update `.gitignore` with `.oro/` and `.beads`
3. Create `~/.oro/projects/<name>/` directory tree
4. Symlink `.beads` -> `~/.oro/projects/<name>/beads/`
5. Extract assets from embedded FS -> `~/.oro/` (hooks, skills, beacons,
   commands). **Additive only** — only write files that don't already exist.
   User-created skills, hooks, and commands are never overwritten or deleted.
   Oro-provided files that the user has modified are also preserved. Use
   `--force` to overwrite oro-provided files (user-created files are still
   never deleted).
6. Install companion binaries:
   - `oro-dash` -> `~/.oro/bin/oro-dash`
   - `oro-search-hook` -> `~/.oro/hooks/oro-search-hook`
   (From the release archive: copy to destination. From source: build via
   `go build`.)
7. Generate `~/.oro/projects/<name>/settings.json` with absolute paths
8. Create `~/.claude/roles/architect/` and `~/.claude/roles/manager/`
9. Write `.beads/config.yaml`

#### Phase 6: Doctor check

Verify everything is in place:

```
Verification:
  [ok] claude 1.0.44
  [ok] tmux 3.5a
  [ok] bd 0.22.0
  [ok] golangci-lint 2.1.0
  [ok] quality_gate.sh (executable)
  [ok] .golangci.yml
  [ok] ~/.oro/hooks/session_start_extras.py (executable)
  [ok] ~/.oro/hooks/oro-search-hook
  [ok] ~/.oro/bin/oro-dash
  [ok] ~/.oro/beacons/architect.md
  [ok] ~/.oro/beacons/manager.md
  [ok] .oro/config.yaml
  [ok] .beads/ initialized
  [ok] ~/.claude/roles/architect/ wired
  [ok] ~/.claude/roles/manager/ wired

Setup complete. Run 'oro start' to launch the swarm.
```

Actionable errors for failures:

```
  [!!] golangci-lint not found
       Run: brew install golangci-lint
       Go linting will fail in the quality gate without this.
```

### 3. Additive asset extraction

Oro ships built-in skills, hooks, beacons, and commands embedded in the binary.
Users can also create their own. The extraction strategy is **additive**:

- **New file (not on disk)**: write it
- **Existing file**: skip (preserve user's version, whether modified or not)
- **User-created file (not in embedded asset list)**: never touched

This means:
- Running `oro setup` after updating oro adds new skills but doesn't overwrite
  existing ones. Users who want updated builtins run `oro setup --force`.
- Users can create custom skills in `~/.oro/skills/my-custom-skill/` and they
  will survive any number of `oro setup` runs.
- Users can modify oro-provided skills (e.g., tweak the TDD skill for their
  workflow) and those modifications persist.

### 4. Worker prompt: coding rules from config

**Current problem:** `prompt.go` hardcodes oro-specific coding rules
(functional first, go-arch-lint, etc.). Workers on other projects receive
wrong instructions.

**Fix:** `AssemblePrompt` reads coding rules from `.oro/config.yaml` instead
of using hardcoded strings. The `coding_rules` field already exists per
language in the config — it's just not wired up.

The worker prompt's "Coding Rules" section becomes:

```go
// Read from .oro/config.yaml languages.<lang>.coding_rules
// Concatenate rules from all detected languages
section(b, "Coding Rules", strings.Join(configCodingRules, "\n"))
```

This means `oro setup`'s generated config directly controls what workers are
told about coding style. Users can edit `.oro/config.yaml` to change the rules
workers follow.

### 5. First-run experience

When `oro start` launches and there are no beads in the backlog, the architect
pane should display an onboarding message instead of a blank screen:

```
Welcome to oro.

No beads found. To get started, tell me what you'd like to build.
Describe your goal and I'll break it down into a plan.

Examples:
  "Add user authentication with JWT tokens"
  "Refactor the database layer to use connection pooling"
  "Fix the race condition in the order processing pipeline"
```

This is triggered by the architect beacon — add a startup check: if
`bd ready` returns 0 items and `bd stats` shows 0 total, print the welcome.

### 6. Quality gate troubleshooting

When `oro setup` completes, print a note about troubleshooting:

```
If the quality gate fails on first run, open Claude Code in your project
and ask it to run ./quality_gate.sh and fix any issues. The gate is a
regular shell script — Claude can read the errors and adjust it for your
project.
```

This turns "integration tests need Docker" and similar first-run failures into
a self-healing loop: the user's own oro instance fixes the gate.

### 7. Idempotency

`oro setup` is safe to run multiple times:

- Tools already installed -> skip (print "found")
- `.golangci.yml` exists -> skip (never overwrite)
- `pyproject.toml` tool sections exist -> skip those sections
- `quality_gate.sh` exists -> **skip** (script is source of truth; user may
  have evolved it). Print note: "quality_gate.sh already exists, skipping.
  Use --force-gate to regenerate."
- `.oro/config.yaml` exists -> skip
- Assets (skills, hooks, beacons, commands) -> **additive only** (see section 3)
- Companion binaries -> reinstall (always update to current version)
- `.beads` -> skip if initialized
- `settings.json` -> regenerate (always overwrite, idempotent)
- Role directories -> re-symlink

### 8. Flags

| Flag | Behavior |
|------|----------|
| `--dev` | Install doc/config linting tools (markdownlint, yamllint, shellcheck, biome) |
| `--dry-run` | Print what would be installed/created, do nothing |
| `--skip-tools` | Skip tool installation, only bootstrap project + generate gate |
| `--force-gate` | Regenerate `quality_gate.sh` even if it already exists |
| `--force` | Overwrite all generated files (config, gate, golangci) AND oro-provided assets |

### 9. `oro init` relationship

`oro init` continues to exist for initializing additional projects without
reinstalling tools. It does Phase 2 + Phase 5 only (detect + bootstrap).

`oro setup` = prerequisites + detect + generate gate + install tools +
bootstrap + doctor.

---

## Risks & Mitigations

| Risk | Severity | Mitigation |
|------|----------|------------|
| Worker prompt hardcodes oro-specific coding rules | HIGH | Wire `AssemblePrompt` to read `coding_rules` from `.oro/config.yaml` (section 4) |
| Companion binaries not found after install | HIGH | `os.Executable()` sibling lookup + PATH fallback + actionable warning (section 1) |
| `go install` / `uv tool install` binaries not on PATH | HIGH | Check PATH after install, print exact export line for `.zshrc` (section 1) |
| GoReleaser pre-build hook runs `make stage-assets` which needs repo structure | HIGH | GoReleaser runs in CI from a full checkout. The hook only runs during release builds, not on user machines. |
| User's existing `.golangci.yml` gets overwritten | HIGH | Only create if file doesn't exist. Never overwrite. |
| User's `pyproject.toml` tool sections get overwritten | HIGH | Only add sections that don't already exist. |
| User's existing `quality_gate.sh` gets overwritten on re-run | HIGH | Skip if exists. Require `--force-gate` to regenerate. |
| User-created skills/hooks deleted by setup re-run | HIGH | Additive extraction: only write files that don't exist (section 3) |
| `$HOME` literal in settings.json | HIGH | Use `os.UserHomeDir()` for absolute paths |
| Integration tests fail in generated quality gate | MEDIUM | Print troubleshooting note: "open Claude and ask it to fix" (section 6) |
| First-run blank screen confuses new users | MEDIUM | Architect beacon shows welcome message when backlog is empty (section 5) |
| `brew install beads` URL may be wrong | MEDIUM | Verify bd install path before implementation |
| Generated gate references tools not yet installed | MEDIUM | Phase 4 (install) runs before Phase 3 output is needed. Gate is generated in Phase 3 but not executed until `oro start`. |
| Coverage check awk parsing fragile | LOW | Standard go tool cover output format, well-established pattern |

---

## Relationship to Prior Specs

This spec supersedes `2026-02-15-oro-setup-design.md`. Key changes:

| Topic | Previous spec | This spec |
|-------|---------------|-----------|
| Distribution | Out of scope | GitHub Releases via GoReleaser |
| Quality gate | Not addressed | Generated, language-aware, evolves independently |
| `.golangci.yml` | Not addressed | Shipped as strong default with 30+ linters |
| `pyproject.toml` | Not addressed | Sections added if missing |
| Coverage | Not addressed | 70% default |
| Companion binaries | Build during `make install` | Ship in release archive + install during setup |
| Tool tiers | User vs developer | Oro runtime + language-specific + developer (3 tiers) |
| Languages | Not specified | Go and Python (extensible later) |

The Phase 5 (bootstrap) logic is carried forward from the previous spec's
Phase 3, with minor adjustments.

---

## Files Changed

| File | Change |
|------|--------|
| `.goreleaser.yml` (new) | GoReleaser config for 3 binaries |
| `.github/workflows/release.yml` (new) | Release CI workflow |
| `cmd/oro/cmd_setup.go` (new) | The `oro setup` command — 6 phases |
| `cmd/oro/gate_template.go` (new) | Quality gate script template + generation logic |
| `cmd/oro/golangci_template.go` (new) | `.golangci.yml` template + module name substitution |
| `cmd/oro/pyproject.go` (new) | `pyproject.toml` section merger |
| `cmd/oro/tools.go` (new) | Tool manifest, install logic, version detection, PATH checks |
| `cmd/oro/doctor.go` (new) | Doctor verification checks + companion binary discovery |
| `cmd/oro/cmd_init.go` | Extract shared bootstrap functions for reuse by setup |
| `pkg/worker/prompt.go` | Read coding rules from `.oro/config.yaml` instead of hardcoding |
| `assets/beacons/architect.md` | Add empty-backlog welcome message |
| `install.md` | Rewrite for new install flow |

## Out of Scope

- Multi-OS support (Linux, Windows) — macOS only for now
- Homebrew tap — separate future epic
- Languages beyond Go and Python
- `oro start` auto-launch after setup
- Upgrading between oro versions
- Installing Claude Code — prerequisite, user's responsibility
- Project-specific quality gate customization UI — users edit the script directly
