# Oro Install & Distribution: Making It Easy for People

**Date:** 2026-02-22
**Status:** Reviewed (adversarial review passed after revisions)
**Author:** architect
**Refines:** `2026-02-17-oro-distribution-and-setup.md` (same design, updated for implementation)

## Problem

Oro today can only be used by its own developers. There is no distribution
path — no release binaries, no install script, no way for someone to download
oro and use it on their own project. The quality gate is hardcoded for oro's
own stack, and worker prompts hardcode oro-specific coding rules.

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

## Epic Structure

Adversarial review identified scope creep. Split into 3 independent epics:

| Epic | Scope | Ships independently |
|------|-------|---------------------|
| **A: Distribution** | GoReleaser + install.sh + `oro setup` (phases 1,2,4,5,6) + install.md rewrite | Yes — core value |
| **B: Quality Gate Generation** | `oro setup` phase 3 + templates (.golangci.yml, pyproject.toml, quality_gate.sh) | Yes — enhances setup |
| **C: Config-Driven Prompts** | Rewire `AssemblePrompt` to read from `.oro/config.yaml` + worktree path resolution | Yes — requires careful worktree handling |

Epic A is the minimum viable distribution. Epics B and C layer on top.

---

## Epic A: Distribution

### 1. GoReleaser Configuration (`.goreleaser.yml`)

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

### 2. Release CI Workflow (`.github/workflows/release.yml`)

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

### 3. CGO_ENABLED=0 Validation

Add a CI job to the existing `ci.yml` that builds with `CGO_ENABLED=0` and
runs the test suite. Note: `-race` requires CGO, so use `-count=1` without
`-race` for the CGO-free check:

```yaml
  cgo-free-check:
    runs-on: macos-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: make stage-assets
      - run: CGO_ENABLED=0 go build ./cmd/oro ./cmd/oro-dash ./cmd/oro-search-hook
      - run: CGO_ENABLED=0 go test -count=1 ./...
      - run: make clean-assets
```

### 4. Install Script (`scripts/install.sh`)

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
10. Re-sign binaries for macOS (codesign — avoids Gatekeeper delay; skip
    gracefully if codesign unavailable)
11. Check PATH for `oro`, `~/.oro/bin`, print instructions if missing
12. Print: `Run 'cd your-project && oro setup' to get started`

The script supports upgrading: it overwrites existing binaries.

### 5. `oro setup` Command (Epic A scope — without quality gate generation)

Five phases (Phase 3/quality gate deferred to Epic B):

#### Phase 1: Prerequisites Check

```
Checking prerequisites...
  [ok] claude 1.0.44
  [ok] git 2.47.0
  [!!] brew not found — required for tool installation
       Install: /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
```

Fail fast if `claude` or `brew` is missing. `git` is also required.

#### Phase 2: Detect Project

Detect languages via existing `langprofile` package:

| Language | Marker files |
|----------|-------------|
| Go | `go.mod` |
| Python | `pyproject.toml`, `setup.py`, or `requirements.txt` |

Creates `.oro/config.yaml` with detected languages and coding rules.

#### Phase 3: Install Tools

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

Brew installs batched: `HOMEBREW_NO_AUTO_UPDATE=1 brew install ...`

Skip any tool already on PATH. After install, check PATH for `~/go/bin`
and `~/.local/bin`. Print exact export lines if missing.

**Developer tools** (`oro setup --dev`) adds markdownlint, yamllint,
shellcheck, biome.

#### Phase 4: Bootstrap Project

Reuses existing `bootstrapProject()` from `cmd_init.go`:

1. `.oro/config.yaml` (from Phase 2)
2. `.gitignore` entries for `.oro/` and `.beads`
3. `~/.oro/projects/<name>/` directory tree
4. `.beads` symlink
5. Extract embedded assets to `~/.oro/` (**additive only** — see note below)
6. Companion binary discovery (os.Executable sibling lookup + PATH fallback)
7. `settings.json` with `$HOME` prefix (Claude Code expands $HOME in hooks)
8. `.beads/config.yaml`

**IMPORTANT: extractAssets() must be fixed for additive behavior.**
Current `extractAssets()` always overwrites files. Must add existence check:
skip files that already exist on disk unless `--force` flag is set. This is
a code change to `cmd_init.go`.

#### Phase 5: Doctor Check

```
Verification:
  [ok] claude 1.0.44
  [ok] tmux 3.5a
  [ok] bd 0.22.0
  [ok] ~/.oro/hooks/oro-search-hook
  [ok] ~/.oro/bin/oro-dash
  [ok] .oro/config.yaml
  [ok] .beads/ initialized

Setup complete. Run 'oro start' to launch the swarm.
```

#### Flags

| Flag | Behavior |
|------|----------|
| `--dev` | Install doc/config linting tools |
| `--dry-run` | Print what would happen, do nothing |
| `--skip-tools` | Skip tool installation, only bootstrap |
| `--force` | Overwrite existing files and oro-provided assets |

#### `oro init` Relationship

`oro init` is simplified to Phase 2 + Phase 4 only (detect + bootstrap).
For initializing additional projects without reinstalling tools. The
current `oro init` behavior (installing tools) moves to `oro setup`.

### 6. Developer Path Updates

#### `install.md` Rewrite

Two clear paths:

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
make setup && make install
oro init
```

---

## Epic B: Quality Gate Generation (deferred)

`oro setup` Phase 3 — generates `quality_gate.sh`, `.golangci.yml`, and
`pyproject.toml` tool sections based on detected languages. The full design
is in the original spec sections above (Section 2, Phase 3). Ships as a
standalone enhancement after Epic A.

Adds `--force-gate` flag to `oro setup`.

---

## Epic C: Config-Driven Worker Prompts (deferred)

Rewires `AssemblePrompt` to read `coding_rules` from `.oro/config.yaml`.
Requires solving worktree path resolution (config is gitignored and absent
in worktrees). Ships independently after Epic A.

---

## Adversarial Review Findings (resolved)

| Finding | Resolution |
|---------|------------|
| Worktree path resolution for config-driven prompts | Deferred to Epic C — not blocking |
| `$HOME` literal in settings.json | VALIDATED: Claude Code expands `$HOME` in hook commands. Current approach is correct. |
| `extractAssets()` overwrites existing files | Called out explicitly in Epic A Phase 4 as a required code change |
| Scope creep (5 concerns bundled) | Split into 3 epics (A, B, C) |
| `-race` flag requires CGO | CGO-free CI check uses `-count=1` without `-race` |
| Partial tool installation failure | Continue-on-error with doctor check at end. User re-runs `oro setup`. |
| codesign unavailable on machines without Xcode | Skip gracefully with warning |

---

## Idempotency

`oro setup` is safe to run multiple times:

| Resource | Behavior |
|----------|----------|
| Tools already installed | Skip (print "found") |
| `.oro/config.yaml` exists | Skip |
| Assets (skills, hooks, beacons) | Additive only (new files written, existing preserved) |
| Companion binaries | Reinstall (always update to current version) |
| `.beads` | Skip if initialized |
| `settings.json` | Regenerate (always overwrite — idempotent) |

---

## Risks & Mitigations

| Risk | Severity | Mitigation |
|------|----------|------------|
| CGO_ENABLED=0 may break sqlite at runtime | HIGH | CI validation: build + test without CGO on every push |
| Companion binaries not found after install | HIGH | os.Executable() sibling lookup + PATH fallback + actionable warning |
| go install / uv tool install binaries not on PATH | HIGH | Check PATH after install, print exact export line |
| GoReleaser make stage-assets fails in CI | HIGH | CI runs on full checkout with npm installed |
| User-created skills/hooks deleted by re-run | HIGH | Additive extraction: fix extractAssets() to skip existing files |
| brew install beads may not work yet | MEDIUM | Verify bd install path. Fallback: go install |
| First-run blank screen | LOW | Deferred to future enhancement |

---

## Files Changed (Epic A only)

| File | Change |
|------|--------|
| `.goreleaser.yml` (new) | GoReleaser config for 3 binaries, darwin only |
| `.github/workflows/release.yml` (new) | Release CI workflow on `v*` tags |
| `.github/workflows/ci.yml` | Add cgo-free-check job |
| `scripts/install.sh` (new) | Curl install script for users |
| `cmd/oro/cmd_setup.go` (new) | `oro setup` command — 5 phases |
| `cmd/oro/tools_setup.go` (new) | Tiered tool manifest, install logic, PATH checks |
| `cmd/oro/doctor.go` (new) | Doctor verification checks + companion binary discovery |
| `cmd/oro/cmd_init.go` | Fix extractAssets() for additive behavior; simplify init to Phase 2+4 |
| `cmd/oro/root.go` | Register new setup command |
| `install.md` | Rewrite for dual-path install |

---

## Out of Scope

- Multi-OS support (Linux, Windows) — macOS only for now
- Homebrew tap — separate future effort
- Languages beyond Go and Python
- `oro start` auto-launch after setup
- Upgrading between oro versions (idempotent re-run is the upgrade path)
- Installing Claude Code — prerequisite, user's responsibility
- Quality gate generation (Epic B)
- Config-driven worker prompts (Epic C)
- First-run welcome message (future enhancement)
