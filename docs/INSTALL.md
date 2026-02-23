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
oro setup --force         # overwrite existing config files
oro setup --dry-run       # print what would happen without executing
```

---

## For Contributors

Develop oro itself.

**Prerequisites:** macOS, Go 1.23+, Claude Code CLI

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
./quality_gate.sh         # full quality gate (lint + test + build)
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
Existing user-created files are never overwritten (use `--force` to override).
