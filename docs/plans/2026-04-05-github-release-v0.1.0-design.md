# GitHub Release v0.1.0 Design

**Date:** 2026-04-05
**Status:** Approved (R1 FAIL → fixed, R2 PASS)
**Goal:** Ship oro v0.1.0 on GitHub Releases with clean curl install/uninstall.

## User Experience

```bash
# Install
curl -fsSL https://raw.githubusercontent.com/mraakashshah/oro/main/scripts/install.sh | bash

# Use
cd your-project
oro setup

# Uninstall
oro uninstall
```

Model: gstack-style. Install brings everything (beads, hooks, tmux, etc). Uninstall leaves no trace.

## What Already Exists

| Component | File | Status |
|-----------|------|--------|
| GoReleaser config | `.goreleaser.yml` | Complete — darwin amd64+arm64, 2 binaries (`oro` + `oro-search-hook`), checksums, auto-changelog |
| Release workflow | `.github/workflows/release.yml` | Complete — triggers on `v*` tags |
| Version injection | `internal/appversion/buildinfo.go` | Complete — ldflags sets `appversion.version` |
| Asset auto-extract | `cmd/oro/preflight.go:203` | Complete — `checkAssetVersion` re-extracts on version mismatch |
| Bootstrap command | `cmd/oro/cmd_setup.go` | Complete — prereqs, lang detect, tools, assets, doctor |

**Note:** `cmd/oro-dash/` is a library package (no `main.go`), NOT a binary. GoReleaser only builds `oro` and `oro-search-hook`. The install script must NOT attempt to install `oro-dash`.

## What Needs to Be Built

### 1. Install Script (`scripts/install.sh`)

~120-line bash script. Flow:

1. **Detect platform** — `uname -s` must be `Darwin` (fail with message otherwise)
2. **Detect arch** — `uname -m`: `arm64` stays `arm64`, `x86_64` maps to `amd64`
3. **Resolve version** — `--version` flag if set, otherwise follow GitHub `releases/latest` redirect URL (avoids API rate limit)
4. **Download archive** — `oro_${VERSION}_darwin_${ARCH}.tar.gz` from GitHub Releases
5. **Download checksums** — `checksums.txt` from same release
6. **Verify checksum** — `grep <archive> checksums.txt | shasum -a 256 -c --quiet`
7. **Extract** — to temp directory
8. **Install `oro`** — to `/usr/local/bin` (if writable; fall back to `~/.local/bin`)
9. **Install `oro-search-hook`** — to `~/.oro/hooks/` (create dir if needed)
10. **Codesign** — ad-hoc `codesign --force --sign -` (skip gracefully if unavailable)
11. **PATH check** — warn if install dir not in PATH
12. **Print next steps** — `oro --version` to verify, `oro setup` to bootstrap

**Binaries in the archive:** `oro` and `oro-search-hook` only. No `oro-dash`.

**Design decisions:**

- **No source build fallback.** oro is pure Go (`CGO_ENABLED=0`). Pre-built binaries always work.
- **darwin-only.** oro requires `claude` CLI + `tmux` + macOS.
- **Checksum verification mandatory.** Fail if checksum doesn't match.
- **`/usr/local/bin` primary, `~/.local/bin` fallback.** Standard macOS convention.
- **`oro-search-hook` goes to `~/.oro/hooks/`.** Same location as `make install`. Preflight expects it there.
- **Redirect-based version resolution.** `curl -w '%{url_effective}' .../releases/latest` avoids the 60/hr GitHub API rate limit.

### 2. Uninstall Command (`oro uninstall`)

New subcommand registered via `cmd.AddCommand(newUninstallCmd())` in `cmd/oro/root.go`. Clean removal of all oro artifacts.

**Flow:**

1. **Stop running oro** — kill daemon (`discoverProjectDaemons` from `cmd_stop.go`), stop tmux sessions
2. **Unload launchd agent** — call `uninstallLaunchAgent` (`launchd.go:141`) to remove `~/Library/LaunchAgents/dev.getoro.dolt.plist` and unload from launchd
3. **Clean project artifacts** — for each project in `~/.oro/projects/`:
   - Read `project.root` to find the project directory
   - Remove `.beads` symlink (created by `setupBeadsSymlink`, `cmd_init.go:878`)
   - Remove `.worktrees/` dir if present (created by standard mode, usually empty after `oro stop`)
   - Remove `.oro/` anchor dir (standard mode projects only)
   - Remove oro-managed git hooks (`pre-push`, `pre-commit`); restore `.user` backups via `uninstallHookWrapper` (`hooks.go:108`)
4. **Clean global gitignore** — remove `.beads/`, `.beads`, `.oro/`, `.dolt/` entries (from `oroGitignoreEntries`, `cmd_init.go:773`)
5. **Remove `~/.oro/`** — prompt for confirmation (contains databases, bead history, project configs)
6. **Remove binaries** — detect location via `os.Executable()`, remove `oro`; also check/remove `oro-search-hook` from `~/.oro/hooks/` and `/usr/local/bin/`
7. **Print summary** — what was removed, manual steps remaining (`scripts/quality_gate.sh` if present in project dirs), `hash -r` hint

**Flags:**

- `--force` — skip confirmation prompt (for scripted use)
- `--keep-data` — remove binaries and hooks but preserve `~/.oro/` (databases, bead history)

**Design decisions:**

- **`oro uninstall` over a separate script.** The binary knows where everything is (project paths, hook locations, global gitignore path, launchd plist label).
- **Confirmation prompt required by default.** `~/.oro/` contains bead history and databases. `--force` for scripted use, `--keep-data` to preserve data.
- **Best-effort cleanup.** If some files are already gone or permissions are wrong, continue and report. Don't fail hard.
- **Self-delete last.** Remove the binary itself as the final step (after all other cleanup).
- **Stealth projects included.** Stealth projects (`s-<hash>`) are stored in `~/.oro/projects/` alongside standard projects. The uninstall iterates all entries, reads `project.root`, and cleans git hooks regardless of mode.

**What gets cleaned:**

| Artifact | Created by | Cleaned by uninstall |
|----------|-----------|---------------------|
| `/usr/local/bin/oro` (or `~/.local/bin/oro`) | install.sh | Yes — via `os.Executable()` |
| `~/.oro/hooks/oro-search-hook` | install.sh / make install | Yes |
| `~/.oro/` (hooks, skills, databases, projects, beacons) | oro init / oro setup / runtime | Yes — with confirmation (or `--keep-data` to preserve) |
| `~/Library/LaunchAgents/dev.getoro.dolt.plist` | `oro dolt setup` | Yes — `uninstallLaunchAgent` |
| `.oro/` in project dirs (standard mode) | oro init | Yes — for all known projects |
| `.beads` symlinks in project dirs | `setupBeadsSymlink` (`cmd_init.go:878`) | Yes — for all known projects |
| `.worktrees/` in project dirs | standard mode runtime | Yes — for all known projects |
| `scripts/quality_gate.sh` in project dirs | `oro init` (standard mode) | No — lives in user's `scripts/` dir; mentioned in uninstall summary |
| `pre-push` / `pre-commit` git hooks | oro init | Yes — restores `.user` backups |
| `~/.gitignore_global` entries (`.oro/`, `.beads/`, `.dolt/`) | `ensureGlobalGitignore` | Yes — removes oro-added lines only |

### 3. Makefile `release` Target

```makefile
release:
	@if [ -z "$(V)" ]; then echo "Usage: make release V=0.1.0"; exit 1; fi
	git tag -a "v$(V)" -m "Release v$(V)"
	git push origin "v$(V)"
```

### 4. README Install Section

Add after project description:

```markdown
## Install

\```bash
curl -fsSL https://raw.githubusercontent.com/mraakashshah/oro/main/scripts/install.sh | bash
\```

Then in your project:

\```bash
cd your-project
oro setup
\```

### Uninstall

\```bash
oro uninstall
\```
```

## Premortem

| Risk | Type | Mitigation |
|------|------|------------|
| GitHub API rate limit (60/hr unauthenticated) | Tiger | Use `releases/latest` redirect URL, not API (implemented in install.sh) |
| No sudo / `/usr/local/bin` not writable | Tiger | Fall back to `~/.local/bin`, warn if not in PATH |
| `oro uninstall` can't find all projects | Tiger | Scan `~/.oro/projects/` which tracks all known projects (both standard and stealth) |
| Uninstall fails mid-way | Tiger | Best-effort: continue on errors, print summary of what remains |
| User loses bead history on uninstall | Elephant | Confirmation prompt warns about database loss; `--keep-data` flag preserves `~/.oro/` |
| Global gitignore has user-added `.oro/` entry too | Paper tiger | Only remove entries matching exact `oroGitignoreEntries()` list |
| Binary self-deletes but PATH is cached | Paper tiger | `hash -r` hint in final output |
| Dangling `.beads` symlinks after uninstall | Tiger | Uninstall removes `.beads` symlinks before removing `~/.oro/` targets |
| Zombie launchd agent after uninstall | Tiger | Call `uninstallLaunchAgent` before removing binaries/data |

## What Does NOT Change

- `.goreleaser.yml` — already correct (2 builds: `oro`, `oro-search-hook`)
- `.github/workflows/release.yml` — already correct
- `internal/appversion/buildinfo.go` — already correct
- `cmd/oro/preflight.go` — auto-extract already handles fresh installs

## Release Checklist (v0.1.0)

1. Merge install script + uninstall command + README changes to `main`
2. `make release V=0.1.0`
3. Wait for GitHub Actions to build + publish
4. Verify: `curl -fsSL ... | bash` works on clean machine
5. Verify: `oro --version` prints `oro 0.1.0`
6. Verify: `oro uninstall --force` leaves no trace (no `~/.oro/`, no dangling symlinks, no launchd plist, no git hooks)

## Bead Decomposition

Three beads:

1. **Install script** — fix existing `scripts/install.sh` (remove oro-dash refs, add checksum verification, use redirect-based version resolution)
2. **Uninstall command** — `cmd/oro/cmd_uninstall.go` + tests (TDD). Must wire into `root.go` via `AddCommand`. Covers: daemon stop, launchd cleanup, project artifacts (`.beads` symlinks, `.oro/` dirs, git hooks), global gitignore cleanup, `~/.oro/` removal, binary self-delete.
3. **Release plumbing** — Makefile `release` target, README install section

Dependencies: none between them. All three must land before tagging v0.1.0.
