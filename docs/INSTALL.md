# Installing Oro

Two paths depending on your goal.

---

## For Users

Run oro on your own project.

**Prerequisites:** macOS, [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-cli)

```bash
curl -fsSL https://raw.githubusercontent.com/mraakashshah/oro/main/scripts/install.sh | bash
cd my-project
oro setup
oro start
```

The install script downloads pre-built binaries for your platform, installs `oro`
to `/usr/local/bin` (or `~/.local/bin` as a fallback), and places companion
binaries `oro-dash` and `oro-search-hook` under `~/.oro/`.

`oro setup` then:

1. Checks prerequisites (`claude`, `git`, `brew`)
2. Detects your project's languages (Go, Python)
3. Installs required tools via Homebrew
4. Bootstraps the oro config, hooks, and skills
5. Runs a health check

`oro start` launches the agent swarm.

### Install options

```bash
# Preview without making changes
bash <(curl -fsSL .../install.sh) --dry-run

# Install a specific version
bash <(curl -fsSL .../install.sh) --version v0.1.0
```

### Setup options

```bash
oro setup                 # standard setup
oro setup --skip-tools    # bootstrap only, skip tool installation
oro setup --force         # refresh generated assets and quality gate files
oro setup --dry-run       # print what would happen without executing
```

### Stealth mode (zero-footprint)

For projects where you don't want any oro files in the repo:

```bash
oro init --stealth
oro start
```

Stealth mode stores all config, task data, and quality gate scripts under
`~/.oro/projects/s-<hash>/` (where `<hash>` is derived from the repo path).
No `.oro/` directory is created in the project root.

Git pre-commit and pre-push hooks are installed automatically to prevent
accidental commits of Oro artifacts and direct publication of `agent/*` or
`epic/*` refs. Ordinary pushes leave the authoritative full quality gate to
GitHub Actions; the generated quality-gate script remains available for
explicit local runs.

---

## For Contributors

Develop oro itself.

**Prerequisites:** macOS, Go 1.23+, Claude Code CLI

The beadstore replatform has moved operator workflows to the native `oro task`
commands. Contributors and operators should inspect, create, update, and close
work items through `oro task` rather than installing or invoking the legacy
tracker.

```bash
git clone git@github.com:mraakashshah/oro.git
cd oro
make setup && make install
oro init
```

`make setup` installs the full dev toolchain (golangci-lint, gofumpt, goimports,
govulncheck, markdownlint, yamllint, shellcheck, biome).

`make install` builds all three binaries and installs them:

```bash
go install ./cmd/oro
go install ./cmd/oro-dash
go install ./cmd/oro-search-hook
```

`oro init` bootstraps oro's config for the oro repo itself.

### Dev workflow

```bash
make stage-assets         # embed skills, hooks, and beacons into the binary
go test ./...             # run the test suite
./scripts/quality_gate.sh # full quality gate (lint + test + build)
make clean-assets         # remove staged assets
```

---

## After Installation

Verify the install:

```bash
oro --version
```

If `oro` is not found, check your PATH. The install script prints exact
`export PATH=...` instructions if the install directory is missing from PATH.
Reload your shell after updating `~/.zshrc` (or `~/.bash_profile`).

---

## Troubleshooting

**`oro` not found after install**
Follow the PATH instructions printed by the installer, then `source ~/.zshrc`.

**Prerequisites missing during `oro setup`**
`oro setup` fails fast with an install hint for whichever prerequisite is absent.
Install it and re-run `oro setup`.

**Re-running setup is safe**
`oro setup` is idempotent. Run it again to install missing tools or repair config.
Existing project config is preserved. Use `--force` to refresh generated Oro
assets and quality gate files.

**No quality gate generated for my project**
`oro init` now generates a quality gate even when no languages are detected.
The fallback gate runs shellcheck and markdownlint so workers always have a gate
to pass. Re-run `oro init` (or `oro init --stealth`) to regenerate.

**Worker stuck / not responding**
Dead tmux panes are detected automatically. When the dispatcher sends a command to
a worker pane that has exited, it fails fast with an error instead of hanging.
Check `oro logs` for "dead pane" errors, then run `oro cleanup` to clear stale state.
