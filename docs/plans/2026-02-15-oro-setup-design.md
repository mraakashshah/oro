# Oro Setup: One-Command Bootstrap

**Date:** 2026-02-15
**Status:** Draft
**Author:** architect

## Problem

Installing oro today requires 7+ manual steps with undocumented ordering
constraints. A chicken-and-egg problem means the binary needs `~/.oro` assets
that only the binary can create. Silent failures mean users get a broken swarm
with no error messages — Claude starts without role context, hooks don't fire,
the search hook times out on every Read.

## Audience

**Users who want to use oro**, not develop it. They have:

- macOS with Homebrew
- Claude Code CLI installed with an API key
- A project they want to manage with oro

They do NOT need the oro development toolchain (golangci-lint, gofumpt,
go-arch-lint, govulncheck, shellcheck, markdownlint, yamllint, etc.).

## Goal

```
brew install oro          # future: Homebrew tap (until then: clone + make install)
cd my-project
oro setup
oro start                 # printed as prompt, not automatic
```

`oro setup` installs runtime dependencies, bootstraps the project, wires Claude
Code, and verifies everything works. It ends with a message prompting the user
to run `oro start`.

---

## Design

### 1. Check assets into the repo (solves the chicken-and-egg)

**Current state:** Assets (hooks, skills, beacons, commands, CLAUDE.md) live only
in `~/.oro/`. The Makefile copies from `~/.oro/` into `cmd/oro/_assets/` at build
time for `go:embed`. Fresh clone on a fresh machine = empty embed = broken binary
that extracts nothing.

**Change:** Add `assets/` to the repo as the canonical source:

```
assets/
  hooks/           # production hook scripts only (no test_*.py)
  skills/          # skill directories
  beacons/         # architect.md, manager.md
  commands/        # slash commands
  CLAUDE.md        # project-level instructions
```

Update Makefile `stage-assets` to copy from `assets/` instead of `$(ORO_HOME)`.
The repo becomes source of truth. `~/.oro/` becomes the deploy target.

Test files (`test_*.py`) stay in a separate `tests/hooks/` directory in the repo,
not in `assets/hooks/`. They are not embedded into the binary.

**Dev workflow:** Contributors edit `assets/hooks/*.py` in the repo, then run
`make dev-sync` (copies `assets/` → `~/.oro/`) to deploy locally without
rebuilding the binary. Changes are versioned and reviewable in PRs.

### 2. Tool tiers: user vs developer

Split the tool manifest into two tiers:

#### Tier 1: User tools (installed by `oro setup`)

These are what you need to RUN oro — manage a Claude Code swarm.

| Tool | Why needed | Install method |
|------|-----------|----------------|
| `tmux` | Session management (architect + manager panes) | `brew install tmux` |
| `python3` | Hook scripts are Python | Version manager or `brew install python3` |
| `jq` | auto-format.sh hook uses it | `brew install jq` |
| `bd` | Issue tracking for beads | `brew install beads` or `go install` |
| `git` | Worktree management, sync | Pre-installed (verify only) |

Claude Code (`claude`) is a prerequisite — verified, not installed.

**That's 4-5 tools.** Not 20+. Most users already have python3 and git.

#### Tier 2: Developer tools (installed by `oro setup --dev`)

These are what you need to DEVELOP oro or run the quality gate on target projects.

| Tool | Category | Install method |
|------|----------|----------------|
| `go` | Runtime | Version manager or brew |
| `node`/`npm` | Runtime | Version manager or brew |
| `uv` | Python toolchain | `brew install uv` |
| `ruff` | Python lint/format | `uv tool install ruff` |
| `pyright` | Python type checking | `npm install -g pyright` |
| `biome` | JS/JSON lint/format | `brew install biome` |
| `ast-grep` | Code search | `npm install -g @ast-grep/cli` |
| `typescript` | TS type checking | `npm install -g typescript` |
| `gofumpt` | Go formatting | `go install mvdan.cc/gofumpt@latest` |
| `goimports` | Go import ordering | `go install golang.org/x/tools/cmd/goimports@latest` |
| `golangci-lint` | Go linting | `brew install golangci-lint` |
| `go-arch-lint` | Architecture rules | `go install github.com/fe3dback/go-arch-lint/v4@latest` |
| `govulncheck` | Security scanning | `go install golang.org/x/vuln/cmd/govulncheck@latest` |
| `shellcheck` | Shell linting | `brew install shellcheck` |
| `markdownlint` | Doc linting | `npm install -g markdownlint-cli` |
| `yamllint` | YAML linting | `uv tool install yamllint` |

`oro setup --dev` installs Tier 1 + Tier 2.

#### Version manager detection

For runtimes (go, node, python), before falling back to brew:

1. Check if already installed → skip
2. Detect version manager on PATH → use it:
   - `mise` / `asdf` / `goenv` for Go
   - `mise` / `asdf` / `fnm` / `nvm` for Node
   - `mise` / `asdf` / `pyenv` for Python
3. Fallback: `brew install <tool>`

Only use version managers already set up on the user's machine. Never install
a version manager — that's the user's choice.

#### Brew batching

All brew installs are batched into a single call with `HOMEBREW_NO_AUTO_UPDATE=1`
to avoid repeated auto-updates:

```
HOMEBREW_NO_AUTO_UPDATE=1 brew install tmux jq python3
```

One `brew update` runs upfront if needed.

#### npm permission handling

Before any `npm install -g`, check if `npm config get prefix` is writable.
If not, prefer brew alternatives where available (`brew install biome`,
`brew install ast-grep`). For tools without brew formulas, set
`npm config set prefix ~/.npm-global` and add to PATH.

### 3. `oro setup` phases

#### Phase 1: Prerequisites check

Verify Claude Code is installed and configured:

```
Checking prerequisites...
  [ok] claude 1.0.44
  [ok] git 2.47.0
  [!!] tmux not found — will install
```

Fail fast if `claude` is missing — it can't be auto-installed.

#### Phase 2: Install tools

Install Tier 1 tools (or Tier 1 + Tier 2 with `--dev`). Progress output:

```
Installing tools...
  [1/4] tmux ................ installing via brew... done (3.5a)
  [2/4] python3 ............. found (3.12.1 via pyenv)
  [3/4] jq .................. found (1.7.1)
  [4/4] bd .................. installing via brew... done (0.21.0)
```

#### Phase 3: Bootstrap project

1. Resolve project name (argument, `.oro/config.yaml`, or directory basename)
2. Create `.oro/config.yaml` with project name + auto-detected language profiles
3. Ensure `.gitignore` has `.oro/` and `.beads/`
4. Create `~/.oro/projects/<name>/` directory tree
5. Symlink `.beads` → `~/.oro/projects/<name>/beads/`
6. Extract assets from embedded FS → `~/.oro/` (hooks, skills, beacons, commands)
7. Write `.beads/config.yaml` directly (no `bd init` dependency)
8. Generate `settings.json` with **absolute paths** (using `os.UserHomeDir()`,
   NOT `$HOME` — eliminates shell expansion dependency)

#### Phase 4: Build companion binaries

`oro` ships two companion binaries that are currently separate build targets:

| Binary | Purpose | Without it |
|--------|---------|------------|
| `oro-search-hook` | PreToolUse:Read hook — structural code search via ast-grep | Every Read tool call hits 5s timeout silently. No error shown. |
| `oro-dash` | TUI dashboard for swarm status | `oro dash` command unavailable |

**Current problem:** Both require `make build-search-hook` / `make build-dash`
from the oro source repo. Users of arbitrary projects can't build them. This
means code search is silently broken for anyone who didn't manually run make.

**Solution — build during `make install`:**

Update Makefile so `make install` builds all three binaries:

<!-- markdownlint-disable MD010 -->

```makefile
install: stage-assets
	go install $(LDFLAGS) ./cmd/oro
	go build $(LDFLAGS) -o $(ORO_HOME)/hooks/oro-search-hook ./cmd/oro-search-hook
	go build $(LDFLAGS) -o $(ORO_HOME)/bin/oro-dash ./cmd/oro-dash
	@$(MAKE) clean-assets
```

<!-- markdownlint-enable MD010 -->

`oro setup` then verifies the binaries exist in Phase 6 (doctor check) and
prints actionable errors if missing.

**Future — bundle into main binary:**

For the `brew install oro` path (no source repo), companion binaries should
either be:
1. Subcommands of `oro` itself (`oro search-hook`, `oro dash`) — single binary
2. Separate binaries in the GoReleaser release archive — multi-binary install
3. Built from embedded source at `oro setup` time (requires Go toolchain)

Option 1 (subcommands) is cleanest — one binary, no deployment coordination.
This is a separate bead.

#### Phase 5: Wire Claude Code

1. Create `~/.claude/roles/architect/` and `~/.claude/roles/manager/`
2. Symlink shared Claude config into each role dir
3. Verify hook scripts are executable

#### Phase 6: Doctor check

```
Verification:
  [ok] claude 1.0.44
  [ok] tmux 3.5a
  [ok] bd 0.21.0
  [ok] python3 3.12.1
  [ok] ~/.oro/hooks/session_start_extras.py (executable)
  [ok] ~/.oro/hooks/enforce-skills.sh (executable)
  [ok] ~/.oro/beacons/architect.md
  [ok] ~/.oro/beacons/manager.md
  [ok] ~/.oro/projects/myproject/settings.json
  [ok] .oro/config.yaml
  [ok] .beads/ initialized
  [ok] ~/.claude/roles/architect/ wired
  [ok] ~/.claude/roles/manager/ wired

✓ Setup complete. Run 'oro start' to launch the swarm.
```

If any check fails, print actionable error:

```
  [!!] ~/.oro/hooks/oro-search-hook missing
       Run: make build-search-hook (from the oro source repo)
       Code search will be degraded without this.
```

### 4. Idempotency

`oro setup` is safe to run multiple times:

- Tools already installed → skip (print "found")
- Assets already extracted → overwrite with latest from embedded FS
- `.beads` already initialized → skip
- `settings.json` → regenerate (always overwrite — idempotent)
- Role directories → re-symlink
- Companion binaries → rebuild

### 5. Flags

| Flag | Behavior |
|------|----------|
| `--dev` | Install Tier 2 (developer) tools in addition to Tier 1 |
| `--dry-run` | Print what would be installed/created, do nothing |
| `--skip-tools` | Skip tool installation, only bootstrap project |
| `--force` | Overwrite existing config even if it looks customized |

### 6. `oro init` relationship

`oro init` continues to exist for initializing additional projects without
reinstalling tools. It does Phase 3 only (bootstrap project).

`oro setup` = check prereqs + install tools + `oro init` + build companions +
wire Claude + doctor.

---

## Known Risks and Mitigations

| Risk | Severity | Mitigation |
|------|----------|------------|
| `$HOME` literal in settings.json | HIGH | Use `os.UserHomeDir()` for absolute paths |
| Existing `~/.oro/hooks/` dir → overwrite | MEDIUM | Overwrite from embedded FS (assets are canonical). Warn if local modifications detected. |
| Hook cross-imports (learning_reminder→learning_analysis) | LOW | Keep all hooks in one flat directory. Document constraint. |
| npm global install EACCES | HIGH | Prefer brew alternatives. Fallback: set npm prefix to `~/.npm-global`. |
| bd install URL wrong (oro-09ws) | HIGH | Fix module path in defaultToolDefs before implementation. |
| brew auto-update latency | MEDIUM | Batch all brew installs. `HOMEBREW_NO_AUTO_UPDATE=1`. |
| Test files mixed with production hooks | LOW | Separate: `assets/hooks/` (production), `tests/hooks/` (tests). |
| `oro setup` in wrong directory | LOW | Skip companion binary build if `cmd/oro-search-hook/` absent. |
| Companion binaries silently missing | HIGH | `make install` builds all 3 binaries. Doctor check verifies they exist. |

---

## Files Changed

| File | Change |
|------|--------|
| `assets/` (new dir) | Production hooks, skills, beacons, commands, CLAUDE.md |
| `tests/hooks/` (new dir) | Hook test files (moved from `~/.oro/hooks/test_*.py`) |
| `Makefile` | `stage-assets` reads from `assets/`; `install` builds all 3 binaries; add `dev-sync` target |
| `cmd/oro/cmd_setup.go` (new) | The `oro setup` command |
| `cmd/oro/cmd_init.go` | Extract shared functions for reuse by setup |
| `cmd/oro/tools.go` (new) | Tiered tool manifest, version manager detection |
| `cmd/oro/doctor.go` (new) | Doctor verification checks |
| `install.md` | Rewrite for new install flow |
| `.gitignore` | Ensure `cmd/oro/_assets/` is gitignored |

## Out of Scope

- Multi-OS support (Linux, Windows) — macOS only for now
- Homebrew tap / GoReleaser / release binaries — separate epic
- `oro start` auto-launch — user runs manually
- Upgrading between oro versions — `oro setup` is idempotent, not a migration tool
- Installing Claude Code — prerequisite, user's responsibility

## Future: `brew install oro`

Once we have GoReleaser + a Homebrew tap, the flow becomes:

```
brew install oro
cd my-project
oro setup
oro start
```

No clone, no `make install`, no Go toolchain needed. The binary ships with
embedded assets. `oro setup` extracts them and wires everything.
