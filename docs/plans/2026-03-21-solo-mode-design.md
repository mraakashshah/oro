# Stealth Mode Design: Zero-Footprint Oro

**Date:** 2026-03-21
**Status:** Draft (post-adversarial review)
**Problem:** Oro currently pollutes repos with 186 tracked files (.claude/, CLAUDE.md, docs/plans/, .beads/issues.jsonl). This prevents using oro on team repos, OSS repos, and client repos where you don't own the codebase.

## Decision: Two Modes

- `oro init` — **Standard mode** (default). Current behavior, everything committed.
- `oro init --stealth` — **Stealth mode**. Zero git-tracked footprint.

Mode stored in `~/.oro/projects/<path-hash>/config.yaml` as `mode: standard|stealth`.

**Prior art:** beads upstream (`bd init --stealth`) uses `.git/info/exclude` to hide `.beads/` in the repo root. Our approach goes further — moving data to `~/.oro/` so not even a hidden directory exists in the repo.

**Project identity**: Use FNV-32a hash of the repo's absolute path (same approach as `DerivePort`), not the directory name. This prevents collisions when two clones of the same repo exist at different paths.

## Solo Mode: Where Everything Lives

### Fully External (no repo files at all)

| Artifact | Stealth Location | Standard Location |
|----------|--------------|---------------|
| Beads (dolt data) | `~/.oro/projects/<path-hash>/beads/` | `.beads/` |
| Worktrees | `~/.oro/projects/<path-hash>/worktrees/` | `.worktrees/` |
| Daemon PID/socket | `~/.oro/projects/<path-hash>/` | `~/.oro/projects/<path-hash>/` |
| CLAUDE.md | `~/.claude/projects/<path-hash>/CLAUDE.md` | `./CLAUDE.md` |
| Project config | `~/.oro/projects/<path-hash>/config.yaml` | `~/.oro/projects/<path-hash>/config.yaml` |

### In-Repo but Gitignored (via `.git/info/exclude`)

| Artifact | Stealth Location | Notes |
|----------|--------------|-------|
| Plans & handoffs | `oro-docs/plans/`, `oro-docs/handoffs/` | Blocked from commit by pre-commit hook |
| Hooks | `.claude/settings.local.json` | Additive merge only |
| Skills | `.claude/skills/` | Additive only, never delete existing |

### Git Safety

| Protection | Mechanism |
|------------|-----------|
| `oro-docs/` not tracked | `.git/info/exclude` entry |
| `oro-docs/` can't be committed | Pre-commit hook rejects staged `oro-docs/` files |
| `agent/*` branches can't be pushed | Pre-push hook blocks `agent/*` in stealth mode |
| Existing git hooks preserved | Hooks use wrapper pattern: check for existing hook, run it first, then run oro's check |

## `oro init --stealth` Behavior

1. Resolve `<path-hash>` from repo root absolute path
2. Create `~/.oro/projects/<path-hash>/` with `config.yaml` (mode: solo, project_root: <abs-path>)
3. Initialize beads at `~/.oro/projects/<path-hash>/beads/` via `bd init --db <path>`
4. Write `~/.claude/projects/<claude-path-hash>/CLAUDE.md` with oro instructions
5. Merge oro hooks into `.claude/settings.local.json` (additive — read existing, add missing keys)
6. Copy oro skills to `.claude/skills/` (additive — skip if skill dir already exists)
7. Create `oro-docs/` directory
8. Add to `.git/info/exclude`: `oro-docs/`
9. Install git hooks with wrapper pattern (preserve existing hooks)
10. Do NOT create `.oro/` in repo root
11. Do NOT modify `.gitignore`
12. Do NOT create `CLAUDE.md` in repo root

## `oro init` Standard Behavior

Same as today. Commits `.claude/`, `CLAUDE.md`, `docs/plans/`, `docs/handoffs/` to the repo. `.beads/` and `.worktrees/` gitignored as today.

## `bd` Integration

`bd` CLI uses `--db` flag for path override (not env var — `BEADS_DIR` does not exist in `bd`).

- `CLIBeadSource`: in stealth mode, append `--db ~/.oro/projects/<path-hash>/beads/` to every `bd` invocation
- `ExecCommandRunner`: add optional `extraArgs []string` field that `CLIBeadSource` populates from config
- `oro bd <args>`: wrapper command that resolves project from CWD, prepends `--db`, then runs `bd`
- Standalone `bd` from terminal: user runs `oro bd list` instead of `bd list`

## Path Parameterization

Currently hardcoded paths that need mode-aware resolution:

| Component | Current | Solo Mode |
|-----------|---------|-----------|
| `protocol.WorktreesDir` | `.worktrees` | `~/.oro/projects/<hash>/worktrees` |
| `WorktreeManager.Create` | `filepath.Join(repoRoot, ".worktrees", beadID)` | `filepath.Join(oroProjectDir, "worktrees", beadID)` |
| `cleanupWorktreeDir` | `filepath.Join(".", ".worktrees")` | reads from config |
| `GCClosedWorktrees` | reads `protocol.WorktreesDir` | reads from config |
| `absoluteBeadsDir` in cmd_start.go | `.beads` relative to CWD | from config |
| `quality_gate.sh` | repo root | `~/.oro/projects/<hash>/quality_gate.sh` (solo) |

Resolution: Add a `Paths` struct to dispatcher config that holds resolved absolute paths for beadsDir, worktreesDir, oroDocsDir. Populated at startup from mode + config.

## Git Hook Wrapper Pattern

To avoid overwriting existing hooks:

```bash
#!/bin/sh
# oro pre-commit wrapper
# Run existing hook first (if it was renamed)
if [ -x .git/hooks/pre-commit.user ]; then
    .git/hooks/pre-commit.user "$@" || exit $?
fi
# Then run oro's checks
# ... oro-specific logic ...
```

On install: if `.git/hooks/pre-commit` exists, rename to `.git/hooks/pre-commit.user`, install wrapper. On uninstall (`oro deinit`): restore `.user` backup.

## Edge Cases

- **Repo moved/renamed**: `config.yaml` stores `project_root` absolute path. On `oro start`, if CWD doesn't match stored path, warn and update.
- **Multiple clones**: path-hash is unique per absolute path, so each clone gets independent state.
- **CI/CD**: Stealth mode is desktop-only. CI doesn't have `~/.oro/` or `.git/info/exclude` entries. This is by design — stealth mode is personal.
- **`oro deinit`**: Removes oro-docs/, .claude/settings.local.json oro entries, .git/hooks wrappers, .git/info/exclude entries. Does NOT delete `~/.oro/projects/<hash>/` (user's data).

## What Solo Mode Does NOT Do

- Does not modify `.gitignore`
- Does not create `CLAUDE.md` in repo root
- Does not create `.oro/` in repo root
- Does not track `issues.jsonl` in git
- Does not write to `docs/` in the repo
- Does not push `agent/*` branches
- Does not overwrite existing git hooks
